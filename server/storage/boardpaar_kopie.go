package storage

// boardpaar_kopie.go — Board-Paar Stufe 2a: externe Kopie anlegen
// (ADR-0141 §3, Karte #825, Migration 0040).
//
// Innen:
//   - Ziele (`boardpaar_ziele`): die Namen der externen Server, die das Skript
//     meldet — nur Namen, keine Adressen (ADR-0140 §7).
//   - Aufträge (`boardpaar_auftraege`): „Kopie dieses Boards auf Ziel X
//     anlegen“. Ein Admin legt sie an, das Skript holt sie (abs_) und
//     quittiert sie (abr_). Der Auftrag nimmt das Board in die Partnerliste
//     innen auf.
//
// Außen:
//   - KopieAnlegen: legt das Board an, trägt es mit kopie = 1 in die
//     Partnerliste außen ein und gibt es den Admins und dem Board-Paar-Team —
//     alles in EINER Transaktion. Wiederholbar: Gibt es die Kopie schon, kommt
//     sie unverändert zurück (VF-7).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	// ErrBoardpaarZielName: Name eines Ziels ungültig (Regel wie Zugangsnamen).
	ErrBoardpaarZielName = errors.New("storage: ungültiger name eines ziels")
	// ErrBoardpaarZuVieleZiele: mehr Ziele als BoardpaarZieleMax.
	ErrBoardpaarZuVieleZiele = errors.New("storage: zu viele ziele")
	// ErrBoardpaarAuftragOffen: Für Board und Ziel liegt schon ein offener Auftrag.
	ErrBoardpaarAuftragOffen = errors.New("storage: für dieses board und ziel liegt schon ein offener auftrag")
	// ErrBoardpaarAuftragUnbekannt: Auftrag gibt es nicht.
	ErrBoardpaarAuftragUnbekannt = errors.New("storage: auftrag unbekannt")
	// ErrBoardpaarAuftragErledigt: Ein erledigter Auftrag lässt sich nicht zurückziehen.
	ErrBoardpaarAuftragErledigt = errors.New("storage: auftrag ist schon erledigt")
	// ErrBoardpaarKopieID: Die Außen-Board-ID ist keine UUID.
	ErrBoardpaarKopieID = errors.New("storage: board-id der kopie muss eine uuid sein")
	// ErrBoardpaarKopieTitel: Titel der Kopie ungültig.
	ErrBoardpaarKopieTitel = errors.New("storage: titel der kopie ungültig")
	// ErrBoardpaarKopieBelegt: Unter der ID steht schon ein Board, das keine
	// Kopie ist — nie überschreiben.
	ErrBoardpaarKopieBelegt = errors.New("storage: unter dieser id gibt es schon ein board, das keine kopie ist")
)

// BoardpaarZieleMax: so viele Ziele nimmt der Server höchstens an.
const BoardpaarZieleMax = 20

// KopieTitelMax: Zeichen eines Board-Titels der Kopie.
const KopieTitelMax = 200

// BoardpaarZiel ist ein gemeldetes Ziel.
type BoardpaarZiel struct {
	Name       string `json:"name"`
	GemeldetAt string `json:"gemeldet_at"`
}

// BoardpaarAuftrag ist ein Auftrag „Kopie anlegen“.
type BoardpaarAuftrag struct {
	ID             string `json:"id"`
	BoardID        string `json:"board_id"`
	Ziel           string `json:"ziel"`
	InnenLinkBasis string `json:"innen_link_basis"`
	Status         string `json:"status"`
	AngelegtAt     string `json:"angelegt_at"`
	Von            string `json:"von,omitempty"`
	ErledigtAt     string `json:"erledigt_at,omitempty"`
	// Titel des Boards beim Lesen (nicht gespeichert).
	Titel string `json:"titel"`
}

// Status eines Auftrags.
const (
	AuftragOffen    = "offen"
	AuftragErledigt = "erledigt"
)

// ZieleSetzen ersetzt die Menge der Ziele (ADR-0141 §3.1). Doppelte Namen
// zählen einmal; gemeldet_at bleibt bei Namen, die schon da waren.
func (r *BoardpaarRepo) ZieleSetzen(ctx context.Context, namen []string) ([]BoardpaarZiel, error) {
	menge := []string{}
	gesehen := map[string]bool{}
	for _, n := range namen {
		n = strings.TrimSpace(n)
		if !gueltigerAusgangName(n) {
			return nil, ErrBoardpaarZielName
		}
		if gesehen[n] {
			continue
		}
		gesehen[n] = true
		menge = append(menge, n)
	}
	if len(menge) > BoardpaarZieleMax {
		return nil, ErrBoardpaarZuVieleZiele
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.ziele-setzen: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT name FROM boardpaar_ziele`)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.ziele-setzen: %w", err)
	}
	var weg []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("boardpaar.ziele-setzen: %w", err)
		}
		if !gesehen[n] {
			weg = append(weg, n)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("boardpaar.ziele-setzen: %w", err)
	}
	for _, n := range weg {
		if _, err := tx.ExecContext(ctx, `DELETE FROM boardpaar_ziele WHERE name = ?`, n); err != nil {
			return nil, fmt.Errorf("boardpaar.ziele-setzen: %w", err)
		}
	}
	jetzt := nowISO()
	for _, n := range menge {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO boardpaar_ziele (name, gemeldet_at) VALUES (?, ?) ON CONFLICT(name) DO NOTHING`, n, jetzt); err != nil {
			return nil, fmt.Errorf("boardpaar.ziele-setzen: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("boardpaar.ziele-setzen: %w", err)
	}
	return r.ZieleListe(ctx)
}

// ZieleListe liefert die gemeldeten Ziele, nach Name sortiert.
func (r *BoardpaarRepo) ZieleListe(ctx context.Context) ([]BoardpaarZiel, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT name, gemeldet_at FROM boardpaar_ziele ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.ziele-liste: %w", err)
	}
	defer rows.Close()
	out := []BoardpaarZiel{}
	for rows.Next() {
		var z BoardpaarZiel
		if err := rows.Scan(&z.Name, &z.GemeldetAt); err != nil {
			return nil, fmt.Errorf("boardpaar.ziele-liste: %w", err)
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

// AuftragAnlegen legt einen Auftrag „Kopie anlegen“ an und nimmt das Board in
// die Partnerliste innen auf — in einer Transaktion (ADR-0141 §3.2). Das Ziel
// muss nicht gemeldet sein: Ein Admin darf einen Auftrag vorbereiten, bevor
// das Skript zum ersten Mal läuft. linkBasis ist die Adresse des Boards im
// Deep-Link-Format; sie wird geprüft und normalisiert.
func (r *BoardpaarRepo) AuftragAnlegen(ctx context.Context, boardID, ziel, linkBasis, von string) (*BoardpaarAuftrag, error) {
	if err := r.koppelbar(ctx, boardID); err != nil {
		return nil, err
	}
	ziel = strings.TrimSpace(ziel)
	if !gueltigerAusgangName(ziel) {
		return nil, ErrBoardpaarZielName
	}
	norm, err := PruefeInnenLinkBasis(linkBasis)
	if err != nil {
		return nil, err
	}
	a := &BoardpaarAuftrag{ID: uuid.NewString(), BoardID: boardID, Ziel: ziel, InnenLinkBasis: norm,
		Status: AuftragOffen, AngelegtAt: nowISO(), Von: von}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.auftrag-anlegen: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO boardpaar_innen (board_id, seit, von) VALUES (?, ?, ?) ON CONFLICT(board_id) DO NOTHING`,
		boardID, a.AngelegtAt, von); err != nil {
		return nil, koppelSchreibfehler("auftrag-anlegen", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO boardpaar_auftraege (id, board_id, ziel, innen_link_basis, status, angelegt_at, von)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`, a.ID, a.BoardID, a.Ziel, a.InnenLinkBasis, a.Status, a.AngelegtAt, a.Von); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, ErrBoardpaarAuftragOffen
		}
		return nil, koppelSchreibfehler("auftrag-anlegen", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("boardpaar.auftrag-anlegen: %w", err)
	}
	return a, nil
}

const kopieAuftragSpalten = `a.id, a.board_id, a.ziel, a.innen_link_basis, a.status, a.angelegt_at, a.von,
	COALESCE(a.erledigt_at, ''), COALESCE(b.title, '')`

func scanAuftraege(rows *sql.Rows) ([]BoardpaarAuftrag, error) {
	defer rows.Close()
	out := []BoardpaarAuftrag{}
	for rows.Next() {
		var a BoardpaarAuftrag
		if err := rows.Scan(&a.ID, &a.BoardID, &a.Ziel, &a.InnenLinkBasis, &a.Status, &a.AngelegtAt, &a.Von,
			&a.ErledigtAt, &a.Titel); err != nil {
			return nil, fmt.Errorf("boardpaar.auftraege scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AuftraegeOffen liefert die offenen Aufträge, deren Board noch in der
// Partnerliste innen steht — so sieht das Skript sie (GET
// /agent/boardpaar/auftraege). Ein entkoppeltes Board hat keinen
// abholbaren Auftrag mehr.
func (r *BoardpaarRepo) AuftraegeOffen(ctx context.Context) ([]BoardpaarAuftrag, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+kopieAuftragSpalten+`
		FROM boardpaar_auftraege a
		JOIN boardpaar_innen i ON i.board_id = a.board_id
		LEFT JOIN boards b ON b.id = a.board_id
		WHERE a.status = 'offen' ORDER BY a.angelegt_at, a.id`)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.auftraege-offen: %w", err)
	}
	return scanAuftraege(rows)
}

// AuftraegeListe liefert alle Aufträge (Admin-Anzeige), neueste zuerst,
// höchstens 100.
// HatAuftrag: Gibt es für das Board einen Auftrag „Externe Kopie anlegen“
// (offen oder erledigt)? Kritik Sprint 93 S-1: Nur dann liefert der Hinweg v2
// die Titel vertraulicher Karten — für reine Stufe-1-Boards gilt weiter
// ADR-0140 („den Titel kennt nur der interne Server“). Ein zurückgezogener
// Auftrag ist gelöscht und zählt nicht.
func (r *BoardpaarRepo) HatAuftrag(ctx context.Context, boardID string) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM boardpaar_auftraege WHERE board_id = ?`, boardID).Scan(&n); err != nil {
		return false, fmt.Errorf("boardpaar.hat-auftrag: %w", err)
	}
	return n > 0, nil
}

func (r *BoardpaarRepo) AuftraegeListe(ctx context.Context) ([]BoardpaarAuftrag, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+kopieAuftragSpalten+`
		FROM boardpaar_auftraege a LEFT JOIN boards b ON b.id = a.board_id
		ORDER BY a.angelegt_at DESC, a.id LIMIT 100`)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.auftraege-liste: %w", err)
	}
	return scanAuftraege(rows)
}

// AuftragErledigt quittiert einen Auftrag (ADR-0141 §3.3). Wiederholbar:
// Ein schon erledigter Auftrag bleibt es, ohne Fehler. Unbekannt →
// ErrBoardpaarAuftragUnbekannt.
func (r *BoardpaarRepo) AuftragErledigt(ctx context.Context, id string) (*BoardpaarAuftrag, error) {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE boardpaar_auftraege SET status = 'erledigt', erledigt_at = ? WHERE id = ? AND status = 'offen'`,
		nowISO(), id); err != nil {
		return nil, fmt.Errorf("boardpaar.auftrag-erledigt: %w", err)
	}
	return r.auftrag(ctx, id)
}

// AuftragZurueckziehen löscht einen OFFENEN Auftrag (Admin). Die Kopplung
// innen bleibt; entkoppeln ist ein eigener Schritt.
func (r *BoardpaarRepo) AuftragZurueckziehen(ctx context.Context, id string) error {
	a, err := r.auftrag(ctx, id)
	if err != nil {
		return err
	}
	if a.Status != AuftragOffen {
		return ErrBoardpaarAuftragErledigt
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM boardpaar_auftraege WHERE id = ? AND status = 'offen'`, id)
	if err != nil {
		return fmt.Errorf("boardpaar.auftrag-zurueckziehen: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBoardpaarAuftragErledigt
	}
	return nil
}

func (r *BoardpaarRepo) auftrag(ctx context.Context, id string) (*BoardpaarAuftrag, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+kopieAuftragSpalten+`
		FROM boardpaar_auftraege a LEFT JOIN boards b ON b.id = a.board_id WHERE a.id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.auftrag: %w", err)
	}
	liste, err := scanAuftraege(rows)
	if err != nil {
		return nil, err
	}
	if len(liste) == 0 {
		return nil, ErrBoardpaarAuftragUnbekannt
	}
	return &liste[0], nil
}

// --- außen: Kopie anlegen ------------------------------------------------------

// KopieErgebnis ist das Ergebnis von KopieAnlegen.
type KopieErgebnis struct {
	BoardID        string `json:"id"`
	Titel          string `json:"titel"`
	InnenLinkBasis string `json:"innen_link_basis"`
	// Wiederholt: Die Kopie gab es schon; nichts geschrieben.
	Wiederholt bool `json:"wiederholt"`
	// Wiederaufgenommen: Das Board war eine Kopie, die außen entkoppelt
	// wurde (Herkunft, keine Zeile in boardpaar_aussen); die Kopplung
	// ist neu angelegt, Board und Mitglieder sind unverändert (Kritik
	// Sprint 93 K-3/R-1); nur die Rolle des Teams folgt dem Schalter
	// boardpaar.rueckweg (Plan D9).
	Wiederaufgenommen bool `json:"wiederaufgenommen"`
	// Mitglieder: wer das Board beim Anlegen bekommen hat (Admins als owner,
	// Team als viewer bzw. editor). Bei einer Wiederholung oder
	// Wiederaufnahme leer.
	Mitglieder []string `json:"mitglieder"`
	// Bearbeitbar (ADR-0141 Nachtrag #836): der Schalter „bearbeitbar“ der
	// Kopie nach dem Aufruf.
	Bearbeitbar bool `json:"bearbeitbar"`
}

// KopieTitelNormalform bringt einen internen Board-Titel in die Form, die
// PruefeKopieTitel außen annimmt: Steuerzeichen werden zu Leerzeichen, Ränder
// getrimmt, auf KopieTitelMax Zeichen gekürzt; leer → „Board“. Kritik
// Sprint 93 K-7: Innen sind Board-Titel nicht begrenzt; ein Auftrag mit einem
// langen Titel bliebe außen sonst dauerhaft bei 422 hängen.
func KopieTitelNormalform(roh string) string {
	t := strings.ToValidUTF8(roh, "")
	t = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, t)
	t = strings.TrimSpace(t)
	if utf8.RuneCountInString(t) > KopieTitelMax {
		t = strings.TrimSpace(string([]rune(t)[:KopieTitelMax]))
	}
	if t == "" {
		return "Board"
	}
	return t
}

// PruefeKopieTitel prüft den Titel einer Kopie und liefert die Normalform
// (getrimmt). 1–KopieTitelMax Zeichen, gültiges UTF-8, keine Steuerzeichen.
func PruefeKopieTitel(roh string) (string, error) {
	t := strings.TrimSpace(roh)
	if t == "" || !utf8.ValidString(t) || utf8.RuneCountInString(t) > KopieTitelMax ||
		strings.IndexFunc(t, unicode.IsControl) >= 0 {
		return "", ErrBoardpaarKopieTitel
	}
	return t, nil
}

// KonfigBoardpaarRueckwegSchluessel: die Einstellung boardpaar.rueckweg (außen,
// ADR-0141 §7; api.KonfigBoardpaarRueckweg). Hier gebraucht, weil KopieAnlegen
// sie in seiner Transaktion liest.
const KonfigBoardpaarRueckwegSchluessel = "boardpaar.rueckweg"

// ErrBoardpaarTeamUnlesbar: boardpaar.team ließ sich in der Transaktion von
// KopieAnlegenTeamAusKonfig nicht lesen (Datenbankfehler oder Geheimwert) —
// der Eingang antwortet 503 wie vorher bei jedem Lesefehler (Verify V-2).
var ErrBoardpaarTeamUnlesbar = errors.New("boardpaar.team nicht lesbar")

// KonfigBoardpaarTeamSchluessel: die Einstellung boardpaar.team (außen).
const KonfigBoardpaarTeamSchluessel = "boardpaar.team"

// TeamNachsichtig liest boardpaar.team Eintrag für Eintrag: Ein ungültiger
// Eintrag (gespeichert vor einer Regeländerung) fällt mit einer Log-Zeile
// weg, die übrigen bleiben (Kritik Sprint 93 K-6 — vorher fiel das ganze
// Team still weg).
func TeamNachsichtig(roh string) []string {
	if _, liste, err := TeamAusText(roh); err == nil {
		return liste
	}
	team := []string{}
	for _, teil := range strings.FieldsFunc(roh, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ';' }) {
		_, eins, err := TeamAusText(teil)
		if err != nil {
			log.Printf("boardpaar: team-eintrag übersprungen: %v", err)
			continue
		}
		for _, e := range eins {
			if !slices.Contains(team, e) {
				team = append(team, e)
			}
		}
	}
	return team
}

// rueckwegAnTx: Steht boardpaar.rueckweg auf „an“? Gelesen auf der
// Verbindung der laufenden Transaktion. Liest dieselbe Zeile wie der
// Schalter-Weg, der die Rolle beim Umschalten angleicht; beide laufen unter
// BEGIN IMMEDIATE und schließen sich deshalb aus.
func rueckwegAnTx(ctx context.Context, conn zeilenAbfrager) (bool, error) {
	v, err := KonfigKlartextTx(ctx, conn, KonfigBoardpaarRueckwegSchluessel)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(v) == "an", nil
}

// teamRolleTx: die Rolle des Board-Paar-Teams auf einer Kopie — editor nur,
// wenn der Rückweg auf ihr wirkt (boardpaar.rueckweg = an UND bearbeitbar,
// ADR-0141 Nachtrag #836 Punkt 6), sonst viewer wie bei „aus“ (Plan D9).
func teamRolleTx(ctx context.Context, conn *sql.Conn, bearbeitbar bool) (string, error) {
	an, err := rueckwegAnTx(ctx, conn)
	if err != nil {
		return "", err
	}
	if an && bearbeitbar {
		return RoleEditor, nil
	}
	return RoleViewer, nil
}

// BoardpaarTeamRolleTx gleicht die Rolle des Board-Paar-Teams auf den Kopien an
// (ADR-0141 „Umsetzung 2c“, Plan D9; je Kopie seit #836): an ist der
// Server-Schalter boardpaar.rueckweg. Auf einer Kopie, auf der der Rückweg
// wirkt (an UND bearbeitbar = 1), werden Zeilen mit Rolle viewer, deren
// Subjekt in team steht, editor; auf jeder anderen Kopie werden solche Zeilen
// mit Rolle editor wieder viewer (Schreibschutz aus 2b, Nachtrag Punkt 6).
// Andere Rollen und andere Subjekte bleiben, wie sie sind (owner, von Hand
// geteilte Personen). boardID leer heißt alle Kopien (kopie = 1), sonst nur
// dieses Board. Läuft auf der Verbindung einer Transaktion; liefert die Zahl
// der geänderten Zeilen.
func BoardpaarTeamRolleTx(ctx context.Context, conn *sql.Conn, boardID string, team []string, an bool) (int64, error) {
	anZahl := 0
	if an {
		anZahl = 1
	}
	type schritt struct {
		von, nach, kopien string
		args              []any
	}
	schritte := []schritt{
		// Rückweg wirkt nicht: editor → viewer.
		{RoleEditor, RoleViewer, `SELECT aussen_board_id FROM boardpaar_aussen WHERE kopie = 1 AND (? = 0 OR bearbeitbar = 0)`, []any{anZahl}},
	}
	if an {
		// Rückweg wirkt: viewer → editor.
		schritte = append(schritte, schritt{RoleViewer, RoleEditor,
			`SELECT aussen_board_id FROM boardpaar_aussen WHERE kopie = 1 AND bearbeitbar = 1`, nil})
	}
	var n int64
	for _, s := range team {
		s = NormalizeEmail(s)
		if s == "" {
			continue
		}
		for _, st := range schritte {
			qq := `UPDATE acl SET role = ? WHERE role = ? AND user_email = ? AND board_id IN (` + st.kopien + `)`
			args := append([]any{st.nach, st.von, s}, st.args...)
			if boardID != "" {
				qq += ` AND board_id = ?`
				args = append(args, boardID)
			}
			res, err := conn.ExecContext(ctx, qq, args...)
			if err != nil {
				return n, fmt.Errorf("boardpaar.team-rolle: %w", err)
			}
			k, _ := res.RowsAffected()
			n += k
		}
	}
	return n, nil
}

// BoardpaarTeamEntziehenTx nimmt Subjekten, die aus boardpaar.team gefallen
// sind, ihre Rollen viewer und editor auf allen Kopien (kopie = 1 und außen
// entkoppelte mit Herkunft) —
// Sprint-94-Kritik S-2: sonst läse eine entfernte Person weiter alles mit
// (nach an → aus sogar als editor). owner bleibt. Eine Person, die zusätzlich
// von Hand mit derselben Rolle geteilt war, ist davon nicht zu unterscheiden
// und verliert die Zeile ebenfalls — ein Owner teilt dann neu. Läuft auf der
// Verbindung einer Transaktion; liefert die Zahl der gelöschten Zeilen.
func BoardpaarTeamEntziehenTx(ctx context.Context, conn *sql.Conn, subjekte []string) (int64, error) {
	var n int64
	for _, s := range subjekte {
		s = NormalizeEmail(s)
		if s == "" {
			continue
		}
		// Verify F-V2: auch außen entkoppelte Kopien (nur noch die Herkunft
		// aus 0042) — sie tragen weiter den zuletzt gespiegelten Inhalt.
		res, err := conn.ExecContext(ctx, `DELETE FROM acl WHERE user_email = ? AND role IN (?, ?)
		        AND (board_id IN (SELECT aussen_board_id FROM boardpaar_aussen WHERE kopie = 1)
		             OR board_id IN (SELECT board_id FROM boardpaar_kopie_herkunft))`, s, RoleViewer, RoleEditor)
		if err != nil {
			return n, fmt.Errorf("boardpaar.team-entziehen: %w", err)
		}
		k, _ := res.RowsAffected()
		n += k
	}
	return n, nil
}

// KopieAnlegen legt außen die Kopie eines internen Boards an (ADR-0141 §3.3):
// Board (kanonisch leer, Titel), Eintrag in boardpaar_aussen mit kopie = 1,
// Mitglieder (alle Server-Admins als owner, das Board-Paar-Team als viewer,
// bei boardpaar.rueckweg = an UND bearbeitbar als editor — Kritik Sprint 93
// K-2, Plan D9, #836; der Schalter wird in derselben Transaktion gelesen).
// Alles in einer Transaktion (BEGIN IMMEDIATE).
//
// Wiederholbar (VF-7): Steht unter id schon eine Kopie, kommt sie mit
// Wiederholt = true zurück, ohne etwas zu ändern — auch nicht Titel oder
// Adresse. Steht dort eine außen entkoppelte Kopie (Eintrag in
// boardpaar_kopie_herkunft, keine Zeile in boardpaar_aussen), wird sie wieder aufgenommen: Zeile mit kopie = 1
// und der neuen Adresse, Board und Mitglieder bleiben (die Team-Rolle folgt dem
// Schalter). Steht dort ein Board,
// das keine Kopie ist (oder das Service-Board), → ErrBoardpaarKopieBelegt. team sind ACL-Subjekte (E-Mail oder
// „@domain“); eines, das PruefeACLSubjekt nicht besteht, fällt mit einer
// Log-Zeile weg, statt das Anlegen scheitern zu lassen.
func (r *BoardpaarRepo) KopieAnlegen(ctx context.Context, id, titel, linkBasis, von string, team []string) (*KopieErgebnis, error) {
	return r.kopieAnlegen(ctx, id, titel, linkBasis, von, team, false, nil)
}

// KopieAnlegenMit wie KopieAnlegen, mit dem optionalen Schalter „bearbeitbar“
// (#836, Bedeutung wie bei KopieAnlegenTeamAusKonfigMit).
func (r *BoardpaarRepo) KopieAnlegenMit(ctx context.Context, id, titel, linkBasis, von string, team []string, bearbeitbar *bool) (*KopieErgebnis, error) {
	return r.kopieAnlegen(ctx, id, titel, linkBasis, von, team, false, bearbeitbar)
}

// KopieAnlegenTeamAusKonfig ist KopieAnlegen mit dem Team aus boardpaar.team,
// gelesen in derselben Transaktion wie der Rückweg-Schalter (Sitzungs-QA #835
// K-3): Vorher las der Eingang das Team vor BEGIN IMMEDIATE; ein dazwischen
// bestätigter Austritt (PUT /admin/boardpaar/team) kam dann auf der neuen
// Kopie wieder als Leser an. Ein Geheimwert ist ErrKonfigGeheimwert.
func (r *BoardpaarRepo) KopieAnlegenTeamAusKonfig(ctx context.Context, id, titel, linkBasis, von string) (*KopieErgebnis, error) {
	return r.kopieAnlegen(ctx, id, titel, linkBasis, von, nil, true, nil)
}

// KopieAnlegenTeamAusKonfigMit wie KopieAnlegenTeamAusKonfig, mit dem
// optionalen Schalter „bearbeitbar“ (ADR-0141 Nachtrag #836): nil heißt
// „nicht gesendet“ — eine neue Kopie bekommt 0, eine bestehende behält ihren
// Wert. Gesendet gilt er auch bei einer Wiederholung (das Skript setzt so,
// was es je Board freigibt) und bei der Wiederaufnahme, in derselben
// Transaktion.
func (r *BoardpaarRepo) KopieAnlegenTeamAusKonfigMit(ctx context.Context, id, titel, linkBasis, von string, bearbeitbar *bool) (*KopieErgebnis, error) {
	return r.kopieAnlegen(ctx, id, titel, linkBasis, von, nil, true, bearbeitbar)
}

func (r *BoardpaarRepo) kopieAnlegen(ctx context.Context, id, titel, linkBasis, von string, team []string, teamAusKonfig bool, bearbeitbar *bool) (*KopieErgebnis, error) {
	if _, err := uuid.Parse(id); err != nil || id != strings.ToLower(id) || len(id) != 36 {
		return nil, ErrBoardpaarKopieID
	}
	t, err := PruefeKopieTitel(titel)
	if err != nil {
		return nil, err
	}
	norm, err := PruefeInnenLinkBasis(linkBasis)
	if err != nil {
		return nil, err
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-anlegen: conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if istLockKonflikt(err) {
			return nil, fmt.Errorf("%w: %v", ErrDBBelegt, err)
		}
		return nil, fmt.Errorf("boardpaar.kopie-anlegen: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if teamAusKonfig {
		roh, err := KonfigKlartextTx(ctx, conn, KonfigBoardpaarTeamSchluessel)
		if err != nil {
			return nil, fmt.Errorf("boardpaar.kopie-anlegen: %w: %w", ErrBoardpaarTeamUnlesbar, err)
		}
		team = TeamNachsichtig(roh)
	}

	var vorhanden int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM boards WHERE id = ?`, id).Scan(&vorhanden); err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-anlegen: %w", err)
	}
	if vorhanden > 0 {
		var altTitel string
		var altLink sql.NullString
		var kopie, altBearbeitbar sql.NullInt64
		var herkunft int
		err := conn.QueryRowContext(ctx,
			`SELECT b.title, a.innen_link_basis, a.kopie, a.bearbeitbar,
			        EXISTS (SELECT 1 FROM boardpaar_kopie_herkunft h WHERE h.board_id = b.id)
			 FROM boards b LEFT JOIN boardpaar_aussen a ON a.aussen_board_id = b.id WHERE b.id = ?`, id).
			Scan(&altTitel, &altLink, &kopie, &altBearbeitbar, &herkunft)
		if err != nil {
			return nil, fmt.Errorf("boardpaar.kopie-anlegen: %w", err)
		}
		if kopie.Valid && kopie.Int64 == 1 {
			erg := &KopieErgebnis{BoardID: id, Titel: altTitel, InnenLinkBasis: altLink.String, Wiederholt: true,
				Mitglieder: []string{}, Bearbeitbar: altBearbeitbar.Int64 == 1}
			// #836: Ein gesendeter Schalter gilt auch bei der Wiederholung,
			// samt Team-Rolle (Punkt 6, auch ohne Wechsel); ohne Feld ändert
			// sie nichts (VF-7) und schreibt nichts.
			if bearbeitbar != nil {
				if err := bearbeitbarSetzenMitTeamTx(ctx, conn, id, *bearbeitbar, team); err != nil {
					return nil, fmt.Errorf("boardpaar.kopie-anlegen: %w", err)
				}
				if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
					return nil, fmt.Errorf("boardpaar.kopie-anlegen: commit: %w", err)
				}
				committed = true
				erg.Bearbeitbar = *bearbeitbar
				log.Printf("boardpaar: kopie %s bearbeitbar=%v (beim wiederholten anlegen)", id, erg.Bearbeitbar)
			}
			return erg, nil
		}
		// Wiederaufnahme nur für ein Board, das hier als Kopie ENTSTANDEN ist
		// (Herkunft, Migration 0042) — nicht an den Spiegel-Marken: die kann
		// jeder Owner per PUT oder Op an sein Board setzen und bekäme so beim
		// nächsten Auftrag die Spiegel eines internen Boards in sein eigenes
		// (Nacharbeit #831, K-1).
		if kopie.Valid || id == ServiceBoardID || herkunft == 0 {
			return nil, ErrBoardpaarKopieBelegt
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO boardpaar_aussen (aussen_board_id, innen_link_basis, seit, von, kopie) VALUES (?, ?, ?, ?, 1)`,
			id, norm, nowISO(), von); err != nil {
			return nil, fmt.Errorf("boardpaar.kopie-anlegen: wiederaufnahme: %w", err)
		}
		// #836: Die neue Zeile beginnt mit bearbeitbar = 0 (Migration 0044).
		if bearbeitbar != nil && *bearbeitbar {
			if err := bearbeitbarSetzenTx(ctx, conn, id, true); err != nil {
				return nil, fmt.Errorf("boardpaar.kopie-anlegen: wiederaufnahme: %w", err)
			}
		}
		// Plan D9: Während die Kopie entkoppelt war, hat ein Umschalten ihre
		// Mitglieder nicht erreicht (nur kopie = 1). Jetzt angleichen — je
		// Kopie nach Server-Schalter und bearbeitbar (#836).
		an, err := rueckwegAnTx(ctx, conn)
		if err != nil {
			return nil, fmt.Errorf("boardpaar.kopie-anlegen: %w", err)
		}
		if _, err := BoardpaarTeamRolleTx(ctx, conn, id, team, an); err != nil {
			return nil, fmt.Errorf("boardpaar.kopie-anlegen: wiederaufnahme: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return nil, fmt.Errorf("boardpaar.kopie-anlegen: commit: %w", err)
		}
		committed = true
		log.Printf("boardpaar: kopie %s wieder aufgenommen (war außen entkoppelt)", id)
		return &KopieErgebnis{BoardID: id, Titel: altTitel, InnenLinkBasis: norm, Wiederaufgenommen: true,
			Mitglieder: []string{}, Bearbeitbar: bearbeitbar != nil && *bearbeitbar}, nil
	}

	ms := nowMillis()
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, t, "2", CanonicalEmptyBoardState(id, t), ms, ms); err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-anlegen: board: %w", err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO boardpaar_aussen (aussen_board_id, innen_link_basis, seit, von, kopie) VALUES (?, ?, ?, ?, 1)`,
		id, norm, nowISO(), von); err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-anlegen: kopplung: %w", err)
	}
	// #836: ohne Feld bleibt die Voreinstellung 0 (Migration 0044).
	if bearbeitbar != nil && *bearbeitbar {
		if err := bearbeitbarSetzenTx(ctx, conn, id, true); err != nil {
			return nil, fmt.Errorf("boardpaar.kopie-anlegen: %w", err)
		}
	}
	// Herkunft (Migration 0042): bleibt beim Entkoppeln stehen und ist das
	// einzige Merkmal einer entkoppelten Kopie — kein Client schreibt sie.
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO boardpaar_kopie_herkunft (board_id, angelegt_at) VALUES (?, ?)`, id, nowISO()); err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-anlegen: herkunft: %w", err)
	}

	erg := &KopieErgebnis{BoardID: id, Titel: t, InnenLinkBasis: norm, Mitglieder: []string{}, Bearbeitbar: bearbeitbar != nil && *bearbeitbar}
	rows, err := conn.QueryContext(ctx, `SELECT email FROM users WHERE is_admin = 1 ORDER BY email`)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-anlegen: admins: %w", err)
	}
	var admins []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("boardpaar.kopie-anlegen: admins: %w", err)
		}
		admins = append(admins, NormalizeEmail(e))
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-anlegen: admins: %w", err)
	}
	// #836: Eine neue Kopie ohne Freigabe bekommt das Team als Leser.
	teamRolle, err := teamRolleTx(ctx, conn, bearbeitbar != nil && *bearbeitbar)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-anlegen: %w", err)
	}
	jetzt := nowISO()
	gesetzt := map[string]bool{}
	setze := func(subjekt, rolle string) error {
		if subjekt == "" || gesetzt[subjekt] {
			return nil
		}
		gesetzt[subjekt] = true
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO acl (board_id, user_email, role, created) VALUES (?, ?, ?, ?)`, id, subjekt, rolle, jetzt); err != nil {
			return fmt.Errorf("boardpaar.kopie-anlegen: mitglied: %w", err)
		}
		erg.Mitglieder = append(erg.Mitglieder, subjekt+" ("+rolle+")")
		return nil
	}
	for _, a := range admins {
		if err := setze(a, RoleOwner); err != nil {
			return nil, err
		}
	}
	for _, s := range team {
		s = NormalizeEmail(s)
		if s == "" || gesetzt[s] {
			continue
		}
		if err := PruefeACLSubjektFuerBoard(id, s, teamRolle); err != nil {
			log.Printf("boardpaar: kopie %s — team-mitglied %q übersprungen: %v", id, s, err)
			continue
		}
		if err := setze(s, teamRolle); err != nil {
			return nil, err
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("boardpaar.kopie-anlegen: commit: %w", err)
	}
	committed = true
	return erg, nil
}

// TeamAusText zerlegt den Wert der Einstellung boardpaar.team (Komma- oder
// Zeilenliste von ACL-Subjekten) und prüft jedes. Liefert die Normalform
// (ein Subjekt je Eintrag, kommagetrennt) und die Liste. Ein ungültiges
// Subjekt ist ein Fehler — beim Speichern, nicht erst beim Anlegen.
func TeamAusText(roh string) (string, []string, error) {
	var liste []string
	gesehen := map[string]bool{}
	for _, teil := range strings.FieldsFunc(roh, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ';' }) {
		s := NormalizeEmail(teil)
		if s == "" || gesehen[s] {
			continue
		}
		if !strings.Contains(s, "@") || strings.ContainsAny(s, " \t") {
			return "", nil, fmt.Errorf("boardpaar.team: %q ist weder eine E-Mail-Adresse noch @domain", s)
		}
		if err := PruefeACLSubjekt(s, RoleEditor); err != nil {
			return "", nil, fmt.Errorf("boardpaar.team: %q: %w", s, err)
		}
		gesehen[s] = true
		liste = append(liste, s)
	}
	if len(liste) > 100 {
		return "", nil, fmt.Errorf("boardpaar.team: höchstens 100 Einträge")
	}
	return strings.Join(liste, ","), liste, nil
}
