package storage

// schatten.go — Board-Paar intern/extern, Seite außen: die Schatten interner
// Karten und Termine (ADR-0140 §1, §3, Karte #779).
//
// Schatten liegen in einer eigenen Tabelle, nicht im Board-Zustand. Der
// einzige Schreibweg ist Ersetze (Schatten-Eingang, PUT der ganzen Menge);
// kein Board-Schreibweg kommt hierher, und kein Blob-Leser sieht die Zeilen.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Andiministrator/aBoard/server/schatten"
)

// SchattenAbgleich ist das Ergebnis eines Laufs (Antwort des Eingangs).
type SchattenAbgleich struct {
	Neu       int `json:"neu"`
	Geaendert int `json:"geaendert"`
	Entfernt  int `json:"entfernt"`
	Anzahl    int `json:"anzahl"`
	// Zeitpunkte nach dem Lauf; LetzteAenderung ist leer, solange nie
	// etwas geändert wurde.
	LetzterAbgleich string `json:"-"`
	LetzteAenderung string `json:"-"`
}

// HatAenderung meldet, ob der Lauf etwas am Bestand geändert hat.
func (a *SchattenAbgleich) HatAenderung() bool {
	return a.Neu+a.Geaendert+a.Entfernt > 0
}

// SchattenStand: Aktualität eines Boards (für die Anzeige außen, #781).
type SchattenStand struct {
	LetzterAbgleich string
	LetzteAenderung string
	Anzahl          int
}

// SchattenZeile ist ein gespeicherter Schatten.
type SchattenZeile struct {
	schatten.Eingang
	GeaendertAt string
	// Fassung (Migration 0040): 1 = Stufe 1, 2 = Hinweg v2 (ADR-0141). Nur
	// Fassung 2 zeigt den Titel eines vertraulichen Schattens.
	Fassung int
}

// SchattenRepo verwaltet die Tabellen schatten und schatten_abgleich.
type SchattenRepo struct {
	db *sql.DB
}

func NewSchattenRepo(db *sql.DB) *SchattenRepo {
	return &SchattenRepo{db: db}
}

// inhalt: der Vergleichswert eines Schattens — alle Felder außer der
// Kennung. Gleicher Inhalt heißt: keine Änderung, geaendert_at bleibt.
func inhalt(e schatten.Eingang, fassung int) string {
	b, _ := json.Marshal([]any{e.Art, e.Nr, e.Titel, e.Vertraulich, e.Spalte, e.Prioritaet,
		e.StartDatum, e.StartZeit, e.EndeDatum, e.EndeZeit, e.Bearbeiter, e.VonAgent, e.Lane, e.SpalteID, fassung})
	return string(b)
}

func nullText(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func nullZahl(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

// zeilenAbfrage: Spalten in der Reihenfolge von scanZeile.
const schattenSpalten = `schatten_id, art, nr, titel, vertraulich, spalte, prioritaet,
	start_datum, start_zeit, ende_datum, ende_zeit, bearbeiter, von_agent, geaendert_at, lane, spalte_id, fassung`

type zeilenScanner interface {
	Scan(dest ...any) error
}

func scanZeile(s zeilenScanner) (SchattenZeile, error) {
	var z SchattenZeile
	var nr sql.NullInt64
	var titel, spalte, prio, sd, sz, ed, ez, lane, spalteID sql.NullString
	var vertraulich, vonAgent int
	var bearbeiter string
	if err := s.Scan(&z.SchattenID, &z.Art, &nr, &titel, &vertraulich, &spalte, &prio,
		&sd, &sz, &ed, &ez, &bearbeiter, &vonAgent, &z.GeaendertAt, &lane, &spalteID, &z.Fassung); err != nil {
		return z, err
	}
	text := func(n sql.NullString) *string {
		if !n.Valid {
			return nil
		}
		v := n.String
		return &v
	}
	if nr.Valid {
		n := int(nr.Int64)
		z.Nr = &n
	}
	z.Titel, z.Spalte, z.Prioritaet = text(titel), text(spalte), text(prio)
	z.StartDatum, z.StartZeit, z.EndeDatum, z.EndeZeit = text(sd), text(sz), text(ed), text(ez)
	z.Lane, z.SpalteID = text(lane), text(spalteID)
	z.Vertraulich, z.VonAgent = vertraulich == 1, vonAgent == 1
	if err := json.Unmarshal([]byte(bearbeiter), &z.Bearbeiter); err != nil || z.Bearbeiter == nil {
		z.Bearbeiter = []string{}
	}
	return z, nil
}

// Ersetze setzt die Schatten eines gekoppelten Boards auf genau die
// gegebene Menge (ADR-0140 §3): Neue werden angelegt, geänderte
// überschrieben (geaendert_at nur bei inhaltlicher Änderung), fehlende
// gelöscht. Eine leere Menge entfernt alle. Alles in EINER Transaktion
// (BEGIN IMMEDIATE); die Kopplung wird darin geprüft, damit ein
// gleichzeitiges Entkoppeln keine Zeilen ohne Kopplung hinterlässt.
// Nicht gekoppelt → ErrBoardpaarNichtGekoppelt, nichts geschrieben.
//
// Die Menge muss geprüft sein (schatten.LiesEingang): Ersetze prüft keine
// Feldinhalte, nur dass jede Kennung höchstens einmal vorkommt.
func (r *SchattenRepo) Ersetze(ctx context.Context, boardID string, menge []schatten.Eingang) (*SchattenAbgleich, error) {
	if boardID == "" || boardID == ServiceBoardID {
		return nil, ErrBoardpaarNichtGekoppelt
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("schatten.ersetze: conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if istLockKonflikt(err) {
			return nil, fmt.Errorf("%w: %v", ErrDBBelegt, err)
		}
		return nil, fmt.Errorf("schatten.ersetze: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Der alte Eingang gilt nur für Boards mit kopie = 0 (ADR-0141 §3, W2-1):
	// Eine Kopie bekommt Schatten nur über den Eingang v2 (ErsetzeKopie).
	var kopie int
	err = conn.QueryRowContext(ctx, `SELECT kopie FROM boardpaar_aussen WHERE aussen_board_id = ?`, boardID).Scan(&kopie)
	if errors.Is(err, sql.ErrNoRows) || err == nil && kopie != 0 {
		return nil, ErrBoardpaarNichtGekoppelt
	}
	if err != nil {
		return nil, fmt.Errorf("schatten.ersetze: kopplung: %w", err)
	}
	erg, err := ersetzeAufConn(ctx, conn, boardID, menge, 1)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("schatten.ersetze: commit: %w", err)
	}
	committed = true
	return erg, nil
}

// ersetzeAufConn: der gemeinsame Teil von Ersetze und ErsetzeKopie — in der
// schon offenen Transaktion auf conn, Kopplung bereits geprüft. Kein COMMIT.
func ersetzeAufConn(ctx context.Context, conn *sql.Conn, boardID string, menge []schatten.Eingang, fassung int) (*SchattenAbgleich, error) {
	bestand := map[string]string{}
	rows, err := conn.QueryContext(ctx, `SELECT `+schattenSpalten+` FROM schatten WHERE board_id = ?`, boardID)
	if err != nil {
		return nil, fmt.Errorf("schatten.ersetze: lesen: %w", err)
	}
	for rows.Next() {
		z, err := scanZeile(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("schatten.ersetze: scan: %w", err)
		}
		bestand[z.SchattenID] = inhalt(z.Eingang, z.Fassung)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("schatten.ersetze: lesen: %w", err)
	}

	jetzt := nowISO()
	erg := &SchattenAbgleich{Anzahl: len(menge)}
	einfuegen, err := conn.PrepareContext(ctx, `INSERT INTO schatten (board_id, `+schattenSpalten+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(board_id, schatten_id) DO UPDATE SET art = excluded.art, nr = excluded.nr,
		  titel = excluded.titel, vertraulich = excluded.vertraulich, spalte = excluded.spalte,
		  prioritaet = excluded.prioritaet, start_datum = excluded.start_datum, start_zeit = excluded.start_zeit,
		  ende_datum = excluded.ende_datum, ende_zeit = excluded.ende_zeit, bearbeiter = excluded.bearbeiter,
		  von_agent = excluded.von_agent, geaendert_at = excluded.geaendert_at,
		  lane = excluded.lane, spalte_id = excluded.spalte_id, fassung = excluded.fassung`)
	if err != nil {
		return nil, fmt.Errorf("schatten.ersetze: prepare: %w", err)
	}
	defer einfuegen.Close()
	gesehen := make(map[string]bool, len(menge))
	for _, e := range menge {
		if gesehen[e.SchattenID] {
			return nil, fmt.Errorf("schatten.ersetze: schattenId doppelt")
		}
		gesehen[e.SchattenID] = true
		alt, gibtEs := bestand[e.SchattenID]
		if gibtEs && alt == inhalt(e, fassung) {
			continue
		}
		if gibtEs {
			erg.Geaendert++
		} else {
			erg.Neu++
		}
		bearbeiter, _ := json.Marshal(e.Bearbeiter)
		if e.Bearbeiter == nil {
			bearbeiter = []byte("[]")
		}
		if _, err := einfuegen.ExecContext(ctx, boardID, e.SchattenID, e.Art, nullZahl(e.Nr), nullText(e.Titel),
			bit(e.Vertraulich), nullText(e.Spalte), nullText(e.Prioritaet), nullText(e.StartDatum), nullText(e.StartZeit),
			nullText(e.EndeDatum), nullText(e.EndeZeit), string(bearbeiter), bit(e.VonAgent), jetzt,
			nullText(e.Lane), nullText(e.SpalteID), fassung); err != nil {
			return nil, fmt.Errorf("schatten.ersetze: schreiben: %w", err)
		}
	}
	if len(bestand) > 0 {
		entfernen, err := conn.PrepareContext(ctx, `DELETE FROM schatten WHERE board_id = ? AND schatten_id = ?`)
		if err != nil {
			return nil, fmt.Errorf("schatten.ersetze: prepare: %w", err)
		}
		defer entfernen.Close()
		for id := range bestand {
			if gesehen[id] {
				continue
			}
			if _, err := entfernen.ExecContext(ctx, boardID, id); err != nil {
				return nil, fmt.Errorf("schatten.ersetze: entfernen: %w", err)
			}
			erg.Entfernt++
		}
	}

	// Aktualität: letzter_abgleich bei jedem gültigen Lauf,
	// letzte_aenderung nur bei inhaltlicher Änderung.
	var aenderung any
	if erg.HatAenderung() {
		aenderung = jetzt
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO schatten_abgleich (board_id, letzter_abgleich, letzte_aenderung, anzahl)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(board_id) DO UPDATE SET letzter_abgleich = excluded.letzter_abgleich,
		  letzte_aenderung = COALESCE(excluded.letzte_aenderung, schatten_abgleich.letzte_aenderung),
		  anzahl = excluded.anzahl`, boardID, jetzt, aenderung, len(menge)); err != nil {
		return nil, fmt.Errorf("schatten.ersetze: abgleich: %w", err)
	}
	var letzte sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT letzter_abgleich, letzte_aenderung FROM schatten_abgleich WHERE board_id = ?`,
		boardID).Scan(&erg.LetzterAbgleich, &letzte); err != nil {
		return nil, fmt.Errorf("schatten.ersetze: abgleich lesen: %w", err)
	}
	erg.LetzteAenderung = letzte.String
	return erg, nil
}

// Liste liefert die Schatten eines Boards, nach Kennung sortiert. Nur für
// Leser, die Schatten ausdrücklich holen (Anzeige, Kalender, Agentensicht
// außen — #781, #782, #789).
func (r *SchattenRepo) Liste(ctx context.Context, boardID string) ([]SchattenZeile, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+schattenSpalten+` FROM schatten WHERE board_id = ? ORDER BY schatten_id`, boardID)
	if err != nil {
		return nil, fmt.Errorf("schatten.liste: %w", err)
	}
	defer rows.Close()
	out := []SchattenZeile{}
	for rows.Next() {
		z, err := scanZeile(rows)
		if err != nil {
			return nil, fmt.Errorf("schatten.liste scan: %w", err)
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

// Stand liefert die Aktualität eines Boards; nil, wenn es noch keinen
// gültigen Lauf gab.
func (r *SchattenRepo) Stand(ctx context.Context, boardID string) (*SchattenStand, error) {
	var s SchattenStand
	var letzte sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT letzter_abgleich, letzte_aenderung, anzahl FROM schatten_abgleich WHERE board_id = ?`,
		boardID).Scan(&s.LetzterAbgleich, &letzte, &s.Anzahl)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("schatten.stand: %w", err)
	}
	s.LetzteAenderung = letzte.String
	return &s, nil
}

// KopieAbgleich ist das Ergebnis eines Laufs des Eingangs v2 (ADR-0141 §4).
type KopieAbgleich struct {
	SchattenAbgleich
	// StrukturGeaendert: Der Board-Zustand wurde geschrieben (Lanes oder
	// Spalten haben sich geändert). Nur dann gibt es board:updated.
	StrukturGeaendert bool `json:"struktur_geaendert"`
	// UpdatedAt: Stand des Boards nach dem Lauf (für das Ereignis).
	UpdatedAt int64 `json:"-"`
	// Bearbeitbar: der Schalter „bearbeitbar“ der Kopie nach dem Lauf
	// (ADR-0141 Nachtrag #836); BearbeitbarGeaendert, wenn der Lauf ihn
	// umgestellt hat — dann meldet der Eingang es den offenen Browsern.
	Bearbeitbar          bool `json:"bearbeitbar"`
	BearbeitbarGeaendert bool `json:"-"`
}

// ErsetzeKopie ist der Schreibweg des Eingangs v2 (PUT
// /agent/boardpaar/spiegel, ADR-0141 §4): Struktur in den Board-Zustand und
// Schatten in die Tabelle, in EINER Transaktion (BEGIN IMMEDIATE — dieselbe
// Schreibsperre wie ApplyOpsIdempotent). Nur für Boards in boardpaar_aussen
// mit kopie = 1, sonst ErrBoardpaarNichtGekoppelt. Ohne Journal-Schutz;
// der Eingang ab 2c nimmt ErsetzeKopieMit, seit #836 ErsetzeKopieFreigabe
// (dort steht der Schreibvorgang; die beiden anderen reichen nur durch).
//
// struktur bekommt den gespeicherten Zustand und liefert den neuen; liefert
// sie byte-gleich den alten, wird der Zustand NICHT geschrieben (updated_at
// bleibt, offene Tabs bekommen keinen Versionskonflikt, PK-11). Ein Fehler
// von struktur rollt alles zurück. Die fünfte Schreibstelle auf
// boards.state_json neben Update, ApplyOps, TransferCard und Insert —
// erzeugt keine Journal-Einträge (ADR-0141 §5.1, ab 2c). Die Schreibstellen
// zählt der Wächter server/main_boardpaar_journal_827_test.go auf.
func (r *SchattenRepo) ErsetzeKopie(ctx context.Context, boardID string, struktur func(state []byte) ([]byte, error), menge []schatten.Eingang) (*KopieAbgleich, error) {
	return r.ErsetzeKopieMit(ctx, boardID, func(state []byte, _ *schatten.Schutz) ([]byte, []string, error) {
		neu, err := struktur(state)
		return neu, nil, err
	}, menge)
}

// ErsetzeKopieMit wie ErsetzeKopie, für den Merge-Eingang ab 2c (ADR-0141 §4,
// Karte #827 E2): struktur bekommt zusätzlich den Schutz aus dem Journal außen
// (unbestätigte Stellen, wartende Spiegel), gelesen in DERSELBEN Transaktion —
// ohne Rennen mit dem Ops-Weg, der Journal und Board unter derselben Sperre
// schreibt. Sie liefert neben dem Zustand die Außen-IDs der wartenden Spiegel,
// die sie markiert oder entfernt hat; deren Zeilen löscht ErsetzeKopieMit vor
// dem COMMIT. Der Eingang schreibt kein Journal (§5.1, W2-8).
func (r *SchattenRepo) ErsetzeKopieMit(ctx context.Context, boardID string,
	struktur func(state []byte, schutz *schatten.Schutz) ([]byte, []string, error), menge []schatten.Eingang) (*KopieAbgleich, error) {
	return r.ErsetzeKopieFreigabe(ctx, boardID, nil, struktur, menge)
}

// ErsetzeKopieFreigabe wie ErsetzeKopieMit, dazu der Schalter „bearbeitbar“
// des Hinwegs (ADR-0141 Nachtrag #836): nil heißt „nicht gesendet“, der Wert
// bleibt; sonst wird er in DERSELBEN Transaktion gesetzt, samt der Rolle des
// Teams auf dieser Kopie (Nachtrag Punkt 6) — ein abgelehnter Hinweg ändert
// beides nicht. Das Setzen allein ändert weder state_json
// noch updated_at (kein board:updated, kein Versionskonflikt).
func (r *SchattenRepo) ErsetzeKopieFreigabe(ctx context.Context, boardID string, bearbeitbar *bool,
	struktur func(state []byte, schutz *schatten.Schutz) ([]byte, []string, error), menge []schatten.Eingang) (*KopieAbgleich, error) {
	if boardID == "" || boardID == ServiceBoardID {
		return nil, ErrBoardpaarNichtGekoppelt
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("schatten.ersetze-kopie: conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if istLockKonflikt(err) {
			return nil, fmt.Errorf("%w: %v", ErrDBBelegt, err)
		}
		return nil, fmt.Errorf("schatten.ersetze-kopie: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var kopie, altBearbeitbar int
	err = conn.QueryRowContext(ctx, `SELECT kopie, bearbeitbar FROM boardpaar_aussen WHERE aussen_board_id = ?`, boardID).
		Scan(&kopie, &altBearbeitbar)
	if errors.Is(err, sql.ErrNoRows) || err == nil && kopie != 1 {
		return nil, ErrBoardpaarNichtGekoppelt
	}
	if err != nil {
		return nil, fmt.Errorf("schatten.ersetze-kopie: kopplung: %w", err)
	}
	var state []byte
	var ts int64
	err = conn.QueryRowContext(ctx, `SELECT state_json, updated_at FROM boards WHERE id = ?`, boardID).Scan(&state, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBoardpaarNichtGekoppelt
	}
	if err != nil {
		return nil, fmt.Errorf("schatten.ersetze-kopie: board: %w", err)
	}
	schutz, err := BoardpaarSchutz(ctx, conn, boardID)
	if err != nil {
		return nil, fmt.Errorf("schatten.ersetze-kopie: %w", err)
	}
	neu, erledigt, err := struktur(state, schutz)
	if err != nil {
		return nil, err
	}
	erg := &KopieAbgleich{UpdatedAt: ts, Bearbeitbar: altBearbeitbar == 1}
	if !bytes.Equal(neu, state) {
		var env struct {
			Version string `json:"version"`
			Board   struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"board"`
		}
		if err := json.Unmarshal(neu, &env); err != nil || env.Board.ID != boardID || env.Board.Title == "" || env.Version == "" {
			return nil, fmt.Errorf("%w (eingang v2)", ErrHuelleVerletzt)
		}
		erg.UpdatedAt = max(nowMillis(), ts+1)
		if _, err := conn.ExecContext(ctx,
			`UPDATE boards SET title = ?, version = ?, state_json = ?, updated_at = ? WHERE id = ?`,
			env.Board.Title, env.Version, neu, erg.UpdatedAt, boardID); err != nil {
			return nil, fmt.Errorf("schatten.ersetze-kopie: board schreiben: %w", err)
		}
		erg.StrukturGeaendert = true
	}
	if err := BoardpaarWartendErledigt(ctx, conn, boardID, erledigt); err != nil {
		return nil, fmt.Errorf("schatten.ersetze-kopie: %w", err)
	}
	if bearbeitbar != nil {
		// #836 Punkt 6: die Team-Rolle dieser Kopie folgt in derselben
		// Transaktion (nur bei boardpaar.rueckweg = an) — bei jedem
		// gesendeten Feld, nicht nur bei einem Wechsel: Nach der Migration
		// stehen Team-Bearbeiter auf Kopien mit 0, die so mit dem ersten
		// Hinweg Leser werden. Ohne Abweichung ändert das nichts.
		if err := bearbeitbarSetzenMitTeamTx(ctx, conn, boardID, *bearbeitbar, nil); err != nil {
			return nil, fmt.Errorf("schatten.ersetze-kopie: %w", err)
		}
		erg.BearbeitbarGeaendert = *bearbeitbar != erg.Bearbeitbar
		erg.Bearbeitbar = *bearbeitbar
	}
	abgleich, err := ersetzeAufConn(ctx, conn, boardID, menge, 2)
	if err != nil {
		return nil, err
	}
	erg.SchattenAbgleich = *abgleich
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("schatten.ersetze-kopie: commit: %w", err)
	}
	committed = true
	return erg, nil
}
