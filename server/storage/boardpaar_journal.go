package storage

// boardpaar_journal.go — Journal außen und wartende Spiegel des Board-Paars,
// Stufe 2c (ADR-0141 §5.1, §5.2, Karte #827, Migration 0041).
//
// Geschrieben wird das Journal nur am Ops-Weg, in derselben Transaktion wie
// das Board (imSchreibvorgang von ApplyOpsIdempotent, D3 des Etappenplans).
// Ganz-PUT und Transfer lehnen Änderungen an Spiegeln und wartenden Spiegeln
// ab, statt zu journalisieren (D2). Der Eingang (ErsetzeKopie) schreibt keine
// Einträge.
//
// Zeiten stehen im festen Format jZeitFormat (UTC, Millisekunden, immer gleich
// lang): Nur so sind die Fristen (25 Tage „veraltet“, 30 Tage Löschen) als
// Zeichenkettenvergleich in SQL richtig — RFC3339Nano kürzt Nullen am Ende.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Andiministrator/aBoard/server/schatten"
)

const jZeitFormat = "2006-01-02T15:04:05.000Z"

func jZeit(t time.Time) string { return t.UTC().Format(jZeitFormat) }

// Abfrager: Verbindung mit Lese- und Schreibzugriff (die gepinnte Verbindung
// von ApplyOpsIdempotent ist ein *sql.Conn und erfüllt das).
type Abfrager interface {
	Ausfuehrer
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ErrKeinAbfrager: Der Schreibvorgang bekam eine Verbindung ohne Lesezugriff.
var ErrKeinAbfrager = errors.New("storage: journal braucht eine Verbindung mit Lesezugriff")

// AlsAbfrager: der Ausfuehrer eines Schreibvorgangs als Abfrager.
func AlsAbfrager(ex Ausfuehrer) (Abfrager, error) {
	q, ok := ex.(Abfrager)
	if !ok {
		return nil, ErrKeinAbfrager
	}
	return q, nil
}

// BoardpaarWartend liest die Außen-IDs der wartenden Spiegel eines Boards.
func BoardpaarWartend(ctx context.Context, q Abfrager, boardID string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT aussen_id FROM boardpaar_wartend WHERE board_id = ?`, boardID)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.wartend: %w", err)
	}
	defer rows.Close()
	aus := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("boardpaar.wartend: %w", err)
		}
		aus[id] = true
	}
	return aus, rows.Err()
}

// BoardpaarSchutz liest, was der Eingang außen beachten muss (ADR-0141 §4,
// ab 2c): die unbestätigten Journal-Stellen und die wartenden Spiegel des
// Boards — auf der Verbindung des Eingangs, in seiner Transaktion.
func BoardpaarSchutz(ctx context.Context, q Abfrager, boardID string) (*schatten.Schutz, error) {
	s := &schatten.Schutz{Wartend: map[string]schatten.WartendInfo{}}
	rows, err := q.QueryContext(ctx, `SELECT objekt, kennung, wartend, art, stelle FROM boardpaar_journal
		WHERE board_id = ? AND bestaetigt_at IS NULL`, boardID)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.schutz: journal: %w", err)
	}
	for rows.Next() {
		var objekt, kennung, art, stelle string
		var w int
		if err := rows.Scan(&objekt, &kennung, &w, &art, &stelle); err != nil {
			rows.Close()
			return nil, fmt.Errorf("boardpaar.schutz: journal: %w", err)
		}
		s.MerkeOffen(objekt, kennung, w == 1, art, stelle)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("boardpaar.schutz: journal: %w", err)
	}
	rows, err = q.QueryContext(ctx, `SELECT aussen_id, objekt, seq_angelegt FROM boardpaar_wartend WHERE board_id = ?`, boardID)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.schutz: wartend: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var w schatten.WartendInfo
		if err := rows.Scan(&id, &w.Objekt, &w.SeqAngelegt); err != nil {
			return nil, fmt.Errorf("boardpaar.schutz: wartend: %w", err)
		}
		s.Wartend[id] = w
	}
	return s, rows.Err()
}

// BoardpaarWartendErledigt löscht die Zeilen wartender Spiegel, die der
// Eingang markiert oder entfernt hat — in seiner Transaktion.
func BoardpaarWartendErledigt(ctx context.Context, q Ausfuehrer, boardID string, ids []string) error {
	for _, id := range ids {
		if _, err := q.ExecContext(ctx, `DELETE FROM boardpaar_wartend WHERE board_id = ? AND aussen_id = ?`, boardID, id); err != nil {
			return fmt.Errorf("boardpaar.wartend: erledigt: %w", err)
		}
	}
	return nil
}

// verschmelzbar: Arten, bei denen ein späterer Eintrag zur selben Stelle den
// früheren ersetzt (vorher des ersten, nachher des letzten). Mengen-, Anlege-
// und Kommentar-Anlage-Einträge bleiben einzeln (ihre Reihenfolge trägt).
var verschmelzbar = map[string]bool{
	schatten.JArtFeld: true, schatten.JArtVerschoben: true, schatten.JArtReihenfolge: true,
	schatten.JArtCheckpunkt: true, schatten.JArtKommentarEdit: true, schatten.JArtTerminFeld: true,
}

func rohOderNull(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return string(r)
}

// gleichWert: zwei Werte inhaltlich gleich (Schlüsselreihenfolge egal).
func gleichWert(a, b string) bool {
	if a == b {
		return true
	}
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	ka, _ := json.Marshal(x)
	kb, _ := json.Marshal(y)
	return string(ka) == string(kb)
}

// BoardpaarJournalSchreiben schreibt die Einträge eines Schreibvorgangs und
// die neuen wartenden Spiegel — auf der Verbindung des Schreibvorgangs, vor
// dem COMMIT.
//
// Verschmelzen (§5.1, VF-3): Liegt zur selben Stelle (Kennung, wartend, Art,
// Stelle) ein unbestätigter Eintrag derselben Person, der noch NICHT abgeholt
// ist, wird er fortgeschrieben (nachher; zeit bleibt die des ersten Eintrags);
// ergibt das keine Änderung mehr (vorher = nachher), fällt er weg. Ist er
// schon abgeholt, entsteht ein neuer Eintrag mit neuer seq. Einträge mit Anker
// (verschoben, reihenfolge) bekommen beim Verschmelzen eine NEUE seq (alter
// Eintrag weg, neuer hinten): ihr neuer Anker kann eine Karte sein, die erst
// nach dem alten Eintrag angelegt oder verschoben wurde (Verify V-1).
func BoardpaarJournalSchreiben(ctx context.Context, q Abfrager, boardID string, eintraege []schatten.JournalEintrag,
	neuWartend map[string]string, person, akteurArt string, zeit time.Time) error {
	z := jZeit(zeit)
	angelegtSeq := map[string]int64{}
	for _, e := range eintraege {
		wartend := 0
		if e.Wartend {
			wartend = 1
		}
		if verschmelzbar[e.Art] {
			var seq int64
			var vorher sql.NullString
			var ersteZeit string
			err := q.QueryRowContext(ctx, `SELECT seq, vorher_json, zeit FROM boardpaar_journal
				WHERE board_id = ? AND kennung = ? AND wartend = ? AND art = ? AND stelle = ? AND bestaetigt_at IS NULL
				  AND abgeholt_at IS NULL AND person = ?
				  AND seq = (SELECT MAX(seq) FROM boardpaar_journal WHERE board_id = ? AND kennung = ? AND wartend = ?
				             AND stelle = ? AND bestaetigt_at IS NULL)`,
				boardID, e.Kennung, wartend, e.Art, e.Stelle, person, boardID, e.Kennung, wartend, e.Stelle).Scan(&seq, &vorher, &ersteZeit)
			switch {
			case err == nil:
				nachher := string(e.Nachher)
				if vorher.Valid && len(e.Nachher) > 0 && gleichWert(vorher.String, nachher) {
					if _, err := q.ExecContext(ctx, `DELETE FROM boardpaar_journal WHERE seq = ?`, seq); err != nil {
						return fmt.Errorf("boardpaar.journal: verschmelzen: %w", err)
					}
					continue
				}
				if e.Art == schatten.JArtVerschoben || e.Art == schatten.JArtReihenfolge {
					if _, err := q.ExecContext(ctx, `DELETE FROM boardpaar_journal WHERE seq = ?`, seq); err != nil {
						return fmt.Errorf("boardpaar.journal: verschmelzen: %w", err)
					}
					if _, err := q.ExecContext(ctx, `INSERT INTO boardpaar_journal
						(board_id, objekt, kennung, wartend, art, stelle, vorher_json, nachher_json, person, akteur_art, zeit)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
						boardID, e.Objekt, e.Kennung, wartend, e.Art, e.Stelle, vorher, rohOderNull(e.Nachher), person, akteurArt, ersteZeit); err != nil {
						return fmt.Errorf("boardpaar.journal: verschmelzen: %w", err)
					}
					continue
				}
				// zeit bleibt die des ersten Eintrags: „veraltet“ (25 Tage) misst
				// das Alter des unbestätigten Eintrags, nicht die letzte Änderung
				// (Sitzungs-Kritik A-6; Grenze ist op_ids mit 30 Tagen).
				if _, err := q.ExecContext(ctx, `UPDATE boardpaar_journal SET nachher_json = ?, akteur_art = ? WHERE seq = ?`,
					rohOderNull(e.Nachher), akteurArt, seq); err != nil {
					return fmt.Errorf("boardpaar.journal: verschmelzen: %w", err)
				}
				continue
			case !errors.Is(err, sql.ErrNoRows):
				return fmt.Errorf("boardpaar.journal: verschmelzen: %w", err)
			}
		}
		res, err := q.ExecContext(ctx, `INSERT INTO boardpaar_journal
			(board_id, objekt, kennung, wartend, art, stelle, vorher_json, nachher_json, person, akteur_art, zeit)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			boardID, e.Objekt, e.Kennung, wartend, e.Art, e.Stelle, rohOderNull(e.Vorher), rohOderNull(e.Nachher), person, akteurArt, z)
		if err != nil {
			return fmt.Errorf("boardpaar.journal: schreiben: %w", err)
		}
		if e.Art == schatten.JArtAngelegt {
			seq, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("boardpaar.journal: seq: %w", err)
			}
			angelegtSeq[e.Kennung] = seq
		}
	}
	for id, objekt := range neuWartend {
		seq, ok := angelegtSeq[id]
		if !ok {
			return fmt.Errorf("boardpaar.journal: wartend %q ohne Eintrag angelegt", id)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO boardpaar_wartend (board_id, aussen_id, objekt, seq_angelegt) VALUES (?, ?, ?, ?)
			ON CONFLICT(board_id, aussen_id) DO NOTHING`, boardID, id, objekt, seq); err != nil {
			return fmt.Errorf("boardpaar.journal: wartend: %w", err)
		}
	}
	return nil
}

// JournalAbrufMax: höchstens so viele Einträge je Abruf (§5.2, L-4).
const JournalAbrufMax = 200

// JournalVeraltetNach: ein Eintrag, der so lange unbestätigt ist, macht den
// Abruf „veraltet“ (§5.2; Grenze ist op_ids mit 30 Tagen).
const JournalVeraltetNach = 25 * 24 * time.Hour

// JournalBehaltenNach: bestätigte Einträge werden danach gelöscht (§5.2).
const JournalBehaltenNach = 30 * 24 * time.Hour

// JournalAusgabe ist ein Eintrag, wie ihn das Skript abholt.
type JournalAusgabe struct {
	Seq     int64           `json:"seq"`
	Objekt  string          `json:"objekt"`
	Kennung string          `json:"kennung"`
	Wartend bool            `json:"wartend"`
	Art     string          `json:"art"`
	Stelle  string          `json:"stelle"`
	Vorher  json.RawMessage `json:"vorher"`
	Nachher json.RawMessage `json:"nachher"`
	Person  string          `json:"person"`
	Akteur  string          `json:"akteur_art"`
	Zeit    string          `json:"zeit"`
}

// JournalAbruf ist die Antwort auf GET …/aenderungen.
type JournalAbruf struct {
	Eintraege []JournalAusgabe `json:"eintraege"`
	Weitere   bool             `json:"weitere"`
	LetzteSeq int64            `json:"letzte_seq"`
	Veraltet  bool             `json:"veraltet"`
}

// ErrJournalCursor: nach liegt jenseits des Journal-Endes (Rücksprung von seq,
// etwa nach einer eingespielten Sicherung außen, §5.1 PK-5 d). Das Skript geht
// in HALT.
var ErrJournalCursor = errors.New("storage: cursor liegt jenseits des journal-endes")

// JournalAbholen liefert die unbestätigten Einträge mit seq > nach (höchstens
// max, höchstens JournalAbrufMax) und setzt ihr abgeholt_at — in einer
// Transaktion mit Schreibsperre (BEGIN IMMEDIATE; §50: kein Lesen-dann-
// Schreiben in einer DEFERRED-Transaktion). Nur für Kopien (kopie = 1), sonst
// ErrBoardpaarNichtGekoppelt.
func (r *BoardRepo) JournalAbholen(ctx context.Context, boardID string, nach int64, max int, jetzt time.Time, nurLesen bool) (*JournalAbruf, error) {
	if max <= 0 || max > JournalAbrufMax {
		max = JournalAbrufMax
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.journal: conn: %w", err)
	}
	defer conn.Close()
	beginn := "BEGIN IMMEDIATE"
	if nurLesen {
		beginn = "BEGIN"
	}
	if _, err := conn.ExecContext(ctx, beginn); err != nil {
		if istLockKonflikt(err) {
			return nil, fmt.Errorf("%w: %v", ErrDBBelegt, err)
		}
		return nil, fmt.Errorf("boardpaar.journal: begin: %w", err)
	}
	fertig := false
	defer func() {
		if !fertig {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	kopie, err := istKopieMit(ctx, conn, boardID)
	if err != nil {
		return nil, err
	}
	if !kopie {
		return nil, ErrBoardpaarNichtGekoppelt
	}
	// Hochwassermarke statt MAX(seq): Bestätigte Einträge werden nach 30
	// Tagen gelöscht — MAX(seq) fiele dann auf 0, und ein ruhiges Board
	// meldete einen Rücksprung, den es nie gab (Sitzungs-Kritik A-1). Die
	// AUTOINCREMENT-Marke in sqlite_sequence fällt nie, außer eine Sicherung
	// wird eingespielt — genau der Fall, den ErrJournalCursor melden soll.
	// Sie gilt für alle Boards; seq ist datenbankweit eindeutig.
	var hoechste sql.NullInt64
	err = conn.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name = 'boardpaar_journal'`).Scan(&hoechste)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("boardpaar.journal: ende: %w", err)
	}
	if nach > hoechste.Int64 {
		return nil, ErrJournalCursor
	}
	rows, err := conn.QueryContext(ctx, `SELECT seq, objekt, kennung, wartend, art, stelle, vorher_json, nachher_json, person, akteur_art, zeit
		FROM boardpaar_journal WHERE board_id = ? AND bestaetigt_at IS NULL AND seq > ? ORDER BY seq LIMIT ?`, boardID, nach, max+1)
	if err != nil {
		return nil, fmt.Errorf("boardpaar.journal: lesen: %w", err)
	}
	abruf := &JournalAbruf{Eintraege: []JournalAusgabe{}, LetzteSeq: nach}
	grenze := jZeit(jetzt.Add(-JournalVeraltetNach))
	for rows.Next() {
		var e JournalAusgabe
		var w int
		var vorher, nachher sql.NullString
		if err := rows.Scan(&e.Seq, &e.Objekt, &e.Kennung, &w, &e.Art, &e.Stelle, &vorher, &nachher, &e.Person, &e.Akteur, &e.Zeit); err != nil {
			rows.Close()
			return nil, fmt.Errorf("boardpaar.journal: lesen: %w", err)
		}
		if len(abruf.Eintraege) == max {
			abruf.Weitere = true
			break
		}
		e.Wartend = w == 1
		e.Vorher, e.Nachher = json.RawMessage("null"), json.RawMessage("null")
		if vorher.Valid {
			e.Vorher = json.RawMessage(vorher.String)
		}
		if nachher.Valid {
			e.Nachher = json.RawMessage(nachher.String)
		}
		if e.Zeit < grenze {
			abruf.Veraltet = true
		}
		abruf.Eintraege = append(abruf.Eintraege, e)
		abruf.LetzteSeq = e.Seq
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("boardpaar.journal: lesen: %w", err)
	}
	if len(abruf.Eintraege) > 0 && !nurLesen {
		if _, err := conn.ExecContext(ctx, `UPDATE boardpaar_journal SET abgeholt_at = ? WHERE board_id = ? AND seq > ? AND seq <= ?
			AND bestaetigt_at IS NULL AND abgeholt_at IS NULL`, jZeit(jetzt), boardID, nach, abruf.LetzteSeq); err != nil {
			return nil, fmt.Errorf("boardpaar.journal: abgeholt: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("boardpaar.journal: commit: %w", err)
	}
	fertig = true
	return abruf, nil
}

// JournalBestaetigen setzt bestaetigt_at für alle abgeholten Einträge bis
// einschließlich bis. Ein Eintrag, der noch nie abgeholt war, wird nicht
// bestätigt (das Skript hat ihn nicht gesehen). Liefert die Zahl der neu
// bestätigten Einträge. Bestätigte Einträge, die älter als
// JournalBehaltenNach sind, löscht ein eigener Aufräumer (mit Uhr-Prüfung).
func (r *BoardRepo) JournalBestaetigen(ctx context.Context, boardID string, bis int64, jetzt time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE boardpaar_journal SET bestaetigt_at = ?
		WHERE board_id = ? AND seq <= ? AND bestaetigt_at IS NULL AND abgeholt_at IS NOT NULL
		  AND EXISTS (SELECT 1 FROM boardpaar_aussen WHERE aussen_board_id = ? AND kopie = 1)`,
		jZeit(jetzt), boardID, bis, boardID)
	if err != nil {
		return 0, fmt.Errorf("boardpaar.journal: bestätigen: %w", err)
	}
	return res.RowsAffected()
}

// JournalAufraeumen löscht bestätigte Einträge, deren Bestätigung älter als
// JournalBehaltenNach ist (alle Boards). Nach Alter — der Aufrufer prüft
// vorher, ob die Uhr gestellt ist (uhr.Gestellt, knowledge §38).
//
// Dazu die Reste aufgelöster Paare (F-2208): Boards, die nicht (mehr) als
// Kopie gekoppelt sind. Nicht beim Entkoppeln, sondern nach Frist — eine
// entkoppelte Kopie wird beim nächsten Auftrag wieder aufgenommen (ADR-0141
// „Umsetzung 2a“ Punkt 8), und ihre offenen Einträge sind Änderungen außen,
// die innen noch nicht angekommen sind. Gelöscht werden
//   - unbestätigte Einträge, die älter als JournalVeraltetNach sind: Sie
//     brächten das Skript nach der Wiederaufnahme ohnehin in HALT
//     („veraltet“), und die Op-IDs innen halten nur 30 Tage;
//   - mit ihnen die wartenden Zeilen, deren Eintrag „angelegt“ darunter ist:
//     Diese Karte ist innen nie angekommen und bleibt rein außen.
//
// Eine wartende Zeile, deren Eintrag „angelegt“ bestätigt war, bleibt: Die
// Karte gibt es innen, und nach einer Wiederaufnahme muss der Eingang sie
// markieren können, sonst legte der Hinweg sie ein zweites Mal an.
// Gekoppelte Kopien fasst das nicht an: Dort räumt der Eingang auf (E2).
func (r *BoardRepo) JournalAufraeumen(ctx context.Context, jetzt time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM boardpaar_journal WHERE bestaetigt_at IS NOT NULL AND bestaetigt_at < ?`,
		jZeit(jetzt.Add(-JournalBehaltenNach)))
	if err != nil {
		return 0, fmt.Errorf("boardpaar.journal: aufräumen: %w", err)
	}
	bestaetigt, _ := res.RowsAffected()

	// Reste entkoppelter Boards in EINER Transaktion mit Schreibsperre
	// (knowledge §49/§50). Erst die wartenden Zeilen (sie erkennt man nur über
	// ihren Eintrag), dann die Einträge; ohne gemeinsame Transaktion bliebe
	// bei einer Wiederaufnahme zwischen beiden ein Eintrag „angelegt“ ohne
	// wartende Zeile.
	grenze := jZeit(jetzt.Add(-JournalVeraltetNach))
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return bestaetigt, fmt.Errorf("boardpaar.journal: aufräumen: conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if istLockKonflikt(err) {
			return bestaetigt, fmt.Errorf("%w: %v", ErrDBBelegt, err)
		}
		return bestaetigt, fmt.Errorf("boardpaar.journal: aufräumen: begin: %w", err)
	}
	fertig := false
	defer func() {
		if !fertig {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	res, err = conn.ExecContext(ctx, `DELETE FROM boardpaar_wartend
		WHERE board_id NOT IN (SELECT aussen_board_id FROM boardpaar_aussen WHERE kopie = 1)
		  AND seq_angelegt IN (SELECT seq FROM boardpaar_journal WHERE bestaetigt_at IS NULL AND zeit < ?)`, grenze)
	if err != nil {
		return bestaetigt, fmt.Errorf("boardpaar.journal: aufräumen (wartend): %w", err)
	}
	n, _ := res.RowsAffected()
	res, err = conn.ExecContext(ctx, `DELETE FROM boardpaar_journal WHERE bestaetigt_at IS NULL AND zeit < ?
		AND board_id NOT IN (SELECT aussen_board_id FROM boardpaar_aussen WHERE kopie = 1)`, grenze)
	if err != nil {
		return bestaetigt, fmt.Errorf("boardpaar.journal: aufräumen (entkoppelt): %w", err)
	}
	m, _ := res.RowsAffected()
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return bestaetigt, fmt.Errorf("boardpaar.journal: aufräumen: commit: %w", err)
	}
	fertig = true
	return bestaetigt + n + m, nil
}

// Wartend: die Außen-IDs der wartenden Spiegel dieses Boards (F-2242, für die
// Upload-Sperre am Anhang-Weg).
func (r *BoardRepo) Wartend(ctx context.Context, boardID string) (map[string]bool, error) {
	return BoardpaarWartend(ctx, r.db, boardID)
}
