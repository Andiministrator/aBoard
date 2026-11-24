package storage

// vorschlaege.go — offene Vorschläge des internen Agenten auf dem externen
// Server (ADR-0140 §8, Karte #783, Migration 0038).
//
// Ein Vorschlag liegt hier, bis ein Mensch ihn bestätigt oder ablehnt oder
// er abläuft. In allen drei Fällen wird die Zeile HART gelöscht (Vertrag
// Push-Skript, Ziffer 9.6 (c)); übrig bleibt nur ein Protokolleintrag ohne Nutzlast, den
// die API schreibt. Was ein Vorschlag bewirkt und wer ihn sehen darf,
// entscheidet api/vorschlaege.go; hier nur, was die Datenbank dafür hält.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// VorschlagGueltig: so lange bleibt ein Vorschlag offen (Vertrag Push-Skript, Ziffer 9.6 (c),
	// ADR-0140 §8: höchstens 24 h).
	VorschlagGueltig = 24 * time.Hour
	// VorschlaegeJeTokenMax: höchstens so viele offene Vorschläge je Token
	// (Vertrag Push-Skript, Ziffer 9.6 (d)).
	VorschlaegeJeTokenMax = 10
)

// Arten eines Vorschlags (ADR-0140 §8). Der CHECK in Migration 0038 nennt
// dieselbe Liste.
const (
	VorschlagKarteAnlegen     = "karte.anlegen"
	VorschlagKarteText        = "karte.text"
	VorschlagKarteBearbeiter  = "karte.bearbeiter"
	VorschlagKommentar        = "kommentar"
	VorschlagKarteLoeschen    = "karte.loeschen"
	VorschlagKarteArchivieren = "karte.archivieren"
)

var (
	// ErrVorschlagGrenze: Das Token hat schon VorschlaegeJeTokenMax offene.
	ErrVorschlagGrenze = errors.New("storage: zu viele offene vorschläge für dieses token")
	// ErrVorschlagDoppelt: Derselbe Vorschlag (Token und Hash) liegt schon offen vor.
	ErrVorschlagDoppelt = errors.New("storage: derselbe vorschlag liegt schon vor")
	// ErrVorschlagNochmal: zweimal hintereinander verschwand ein Doppel zwischen
	// Einfügen und Nachfrage — weder Doppel noch Grenze; der Aufrufer versucht
	// es gleich noch einmal (Verify 2 W-2).
	ErrVorschlagNochmal = errors.New("storage: vorschlag gerade nicht anlegbar, bitte erneut")
	// ErrVorschlagUnbekannt: Es gibt den Vorschlag (auf diesem Board) nicht
	// oder nicht mehr.
	ErrVorschlagUnbekannt = errors.New("storage: vorschlag unbekannt")
)

// Vorschlag ist eine Zeile der Tabelle vorschlaege.
type Vorschlag struct {
	ID        string
	BoardID   string
	TokenID   string
	TokenName string
	Art       string
	// KarteID: Ziel-Karte bei Arten, die eine bestehende Karte betreffen.
	KarteID  string
	Nutzlast string
	Hash     string
	Fuer     string
	Angelegt string
	LaeuftAb string
}

// VorschlagRepo verwaltet die Tabelle vorschlaege.
type VorschlagRepo struct {
	db *sql.DB
}

func NewVorschlagRepo(db *sql.DB) *VorschlagRepo {
	return &VorschlagRepo{db: db}
}

// Vorschlaege: der Speicher auf derselben Datenbank wie die Boards — über das
// BoardRepo, damit jede Stelle, die Boards hat, auch die Vorschläge hat
// (main.go und alle Test-Aufbauten), wie LoeschCodes.
func (r *BoardRepo) Vorschlaege() *VorschlagRepo {
	return NewVorschlagRepo(r.db)
}

// AgentTokens: das Token-Repo auf derselben Datenbank — für die Prüfung beim
// Bestätigen, ob der Zugang hinter einem Vorschlag noch gilt.
func (r *BoardRepo) AgentTokens() *AgentTokenRepo {
	return NewAgentTokenRepo(r.db)
}

// vorschlagJetzt: die Vergleichszeit in der Form von laeuft_ab (lexikalisch
// sortierbar, UTC, Sekunden).
func vorschlagJetzt() string {
	return timeNow().UTC().Format(agentAblaufFormat)
}

// Anlegen speichert einen Vorschlag. ID, Angelegt und LaeuftAb setzt der
// Speicher (Angelegt = jetzt, LaeuftAb = jetzt + VorschlagGueltig).
//
// Grenze je Token und Doppel-Prüfung stehen in DEMSELBEN Statement wie das
// Einfügen (Kritik K-B1, K-B9, K-A8): Ein einzelnes INSERT … SELECT … WHERE
// nimmt die Schreibsperre sofort und wartet über busy_timeout — eine
// DEFERRED-Transaktion aus Zählen und Einfügen scheiterte bei parallelem
// Zugriff mit „database is locked“ (gemessen: 500 statt 429), und das
// Doppel lag ganz außerhalb. Abgelaufene Vorschläge zählen nicht mit.
//
// Liegt vom selben Token schon ein offener Vorschlag mit demselben Hash vor:
// ErrVorschlagDoppelt, und vorhanden ist dieser Vorschlag.
func (r *VorschlagRepo) Anlegen(ctx context.Context, v *Vorschlag) (vorhanden *Vorschlag, err error) {
	if v.ID == "" || v.BoardID == "" || v.TokenID == "" || v.TokenName == "" || v.Art == "" || v.Nutzlast == "" || v.Hash == "" {
		return nil, errors.New("vorschlaege.Anlegen: pflichtfeld fehlt")
	}
	// Zwei Versuche: Verschwindet ein Doppel zwischen dem INSERT und der
	// Nachfrage (bestätigt, abgelehnt, abgelaufen), ist weder „Doppel“ noch
	// „Grenze“ wahr — dann einmal neu versuchen statt „schon 10 offene“ zu
	// melden (Verify V-11).
	for versuch := 0; versuch < 2; versuch++ {
		jetzt := timeNow().UTC()
		v.Angelegt = jetzt.Format(agentAblaufFormat)
		v.LaeuftAb = jetzt.Add(VorschlagGueltig).Format(agentAblaufFormat)
		res, err := r.db.ExecContext(ctx, `INSERT INTO vorschlaege
			(id, board_id, token_id, token_name, art, karte_id, nutzlast, hash, fuer, angelegt, laeuft_ab)
			SELECT ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?
			 WHERE NOT EXISTS (SELECT 1 FROM vorschlaege WHERE token_id = ? AND hash = ? AND laeuft_ab > ?)
			   AND (SELECT COUNT(*) FROM vorschlaege WHERE token_id = ? AND laeuft_ab > ?) < ?`,
			v.ID, v.BoardID, v.TokenID, v.TokenName, v.Art, v.KarteID, v.Nutzlast, v.Hash, v.Fuer, v.Angelegt, v.LaeuftAb,
			v.TokenID, v.Hash, v.Angelegt,
			v.TokenID, v.Angelegt, VorschlaegeJeTokenMax)
		if err != nil {
			return nil, fmt.Errorf("vorschlaege.Anlegen: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return nil, fmt.Errorf("vorschlaege.Anlegen: %w", err)
		} else if n == 1 {
			return nil, nil
		}
		alt, err := scanVorschlag(r.db.QueryRowContext(ctx, `SELECT `+vorschlagSpalten+` FROM vorschlaege
			WHERE token_id = ? AND hash = ? AND laeuft_ab > ? ORDER BY angelegt LIMIT 1`, v.TokenID, v.Hash, v.Angelegt))
		if err == nil {
			return alt, ErrVorschlagDoppelt
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("vorschlaege.Anlegen doppel: %w", err)
		}
		var offen int
		if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vorschlaege WHERE token_id = ? AND laeuft_ab > ?`,
			v.TokenID, v.Angelegt).Scan(&offen); err != nil {
			return nil, fmt.Errorf("vorschlaege.Anlegen zählen: %w", err)
		}
		if offen >= VorschlaegeJeTokenMax {
			return nil, ErrVorschlagGrenze
		}
	}
	return nil, ErrVorschlagNochmal
}

const vorschlagSpalten = `id, board_id, token_id, token_name, art, COALESCE(karte_id, ''), nutzlast, hash, fuer, angelegt, laeuft_ab`

func scanVorschlag(s interface{ Scan(...any) error }) (*Vorschlag, error) {
	v := &Vorschlag{}
	if err := s.Scan(&v.ID, &v.BoardID, &v.TokenID, &v.TokenName, &v.Art, &v.KarteID, &v.Nutzlast, &v.Hash,
		&v.Fuer, &v.Angelegt, &v.LaeuftAb); err != nil {
		return nil, err
	}
	return v, nil
}

// Offene liefert die offenen (nicht abgelaufenen) Vorschläge eines Boards,
// älteste zuerst.
func (r *VorschlagRepo) Offene(ctx context.Context, boardID string) ([]Vorschlag, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+vorschlagSpalten+` FROM vorschlaege
		WHERE board_id = ? AND laeuft_ab > ? ORDER BY angelegt, id`, boardID, vorschlagJetzt())
	if err != nil {
		return nil, fmt.Errorf("vorschlaege.Offene: %w", err)
	}
	defer rows.Close()
	aus := []Vorschlag{}
	for rows.Next() {
		v, err := scanVorschlag(rows)
		if err != nil {
			return nil, fmt.Errorf("vorschlaege.Offene scan: %w", err)
		}
		aus = append(aus, *v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vorschlaege.Offene: %w", err)
	}
	return aus, nil
}

// Hole liefert einen Vorschlag des Boards — auch einen abgelaufenen, damit
// der Aufrufer „abgelaufen“ von „gibt es nicht“ unterscheiden kann
// (Abgelaufen). Fremdes Board oder unbekannte id: ErrVorschlagUnbekannt.
func (r *VorschlagRepo) Hole(ctx context.Context, boardID, id string) (*Vorschlag, error) {
	v, err := scanVorschlag(r.db.QueryRowContext(ctx, `SELECT `+vorschlagSpalten+` FROM vorschlaege
		WHERE board_id = ? AND id = ?`, boardID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrVorschlagUnbekannt
	}
	if err != nil {
		return nil, fmt.Errorf("vorschlaege.Hole: %w", err)
	}
	return v, nil
}

// Abgelaufen: Ist die Frist des Vorschlags vorbei?
func (v *Vorschlag) Abgelaufen() bool {
	return v.LaeuftAb <= vorschlagJetzt()
}

// Entfernen löscht einen Vorschlag des Boards hart. Gab es ihn nicht (mehr):
// ErrVorschlagUnbekannt — so gewinnt von zwei gleichzeitigen Bestätigungen
// genau eine.
func (r *VorschlagRepo) Entfernen(ctx context.Context, boardID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM vorschlaege WHERE board_id = ? AND id = ?`, boardID, id)
	if err != nil {
		return fmt.Errorf("vorschlaege.Entfernen: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("vorschlaege.Entfernen: %w", err)
	} else if n == 0 {
		return ErrVorschlagUnbekannt
	}
	return nil
}

// Wiederherstellen legt einen eben entfernten Vorschlag unverändert wieder an
// (dieselbe id, dieselben Fristen) — für den Fall, dass seine Ausführung nach
// dem Entfernen abgelehnt wurde, ohne etwas zu schreiben. Ohne Grenzprüfung:
// Der Vorschlag hat seinen Platz nie wirklich verloren.
func (r *VorschlagRepo) Wiederherstellen(ctx context.Context, v *Vorschlag) error {
	// Kein OR IGNORE (Kritik K-B10): Das schluckte jede Constraint-Verletzung.
	// Nur „gibt es schon“ (dieselbe id) ist kein Fehler.
	if _, err := r.db.ExecContext(ctx, `INSERT INTO vorschlaege
		(id, board_id, token_id, token_name, art, karte_id, nutzlast, hash, fuer, angelegt, laeuft_ab)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`,
		v.ID, v.BoardID, v.TokenID, v.TokenName, v.Art, v.KarteID, v.Nutzlast, v.Hash, v.Fuer, v.Angelegt, v.LaeuftAb); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: vorschlaege.id") {
			return nil
		}
		return fmt.Errorf("vorschlaege.Wiederherstellen: %w", err)
	}
	return nil
}

// AbgelaufeneLoeschen löscht alle abgelaufenen Vorschläge hart und liefert
// sie ohne Nutzlast (für den Protokolleintrag). Ein Statement mit RETURNING
// (Kritik K-B1): kein Hochstufen einer Lese- zur Schreibtransaktion, und
// gelöscht wird genau, was zurückkommt. Die Frist-Prüfung (uhr) macht der
// Aufrufer.
func (r *VorschlagRepo) AbgelaufeneLoeschen(ctx context.Context) ([]Vorschlag, error) {
	rows, err := r.db.QueryContext(ctx, `DELETE FROM vorschlaege WHERE laeuft_ab <= ? RETURNING id, board_id, token_name, art`, vorschlagJetzt())
	if err != nil {
		return nil, fmt.Errorf("vorschlaege.AbgelaufeneLoeschen: %w", err)
	}
	defer rows.Close()
	var weg []Vorschlag
	for rows.Next() {
		var v Vorschlag
		if err := rows.Scan(&v.ID, &v.BoardID, &v.TokenName, &v.Art); err != nil {
			return nil, fmt.Errorf("vorschlaege.AbgelaufeneLoeschen scan: %w", err)
		}
		weg = append(weg, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vorschlaege.AbgelaufeneLoeschen: %w", err)
	}
	return weg, nil
}
