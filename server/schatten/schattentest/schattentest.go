// Package schattentest hilft den Nachweis-Tests der Karte #780 (ADR-0140 §1):
// Es koppelt ein Board als „außen“, legt Schatten über den einzigen
// Schreibweg (SchattenRepo.Ersetze) an und hält den Inhalt BEIDER
// Schatten-Tabellen als Momentaufnahme fest. Ein Board-Schreibweg gilt als
// nachgewiesen schattenfrei, wenn die Momentaufnahme vor und nach ihm
// zeichengleich ist — verglichen wird der genaue Zustand aller Zeilen aller
// Boards, nicht „kein Fehler“ (Fehlerklassen 163, 214).
package schattentest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

// Marker steht in jedem Schatten-Titel, den Bestuecke anlegt. Ein Leser, der
// Schatten ausliefert, verrät sich, sobald der Marker in seiner Antwort steht.
const Marker = "SCHATTEN-MARKER-780"

// Nachbar ist ein zweites gekoppeltes Board mit eigenen Schatten, das
// Bestuecke beim ersten Aufruf je Datenbank anlegt. Ein Schreibweg auf dem
// geprüften Board, der die Schatten eines ANDEREN Boards berührt, hätte
// sonst keine Zeile, an der er sich zeigt (Kritik K-9).
const Nachbar = "nachbar-780"

// Bestuecke koppelt boardID als „außen“ (das Board muss existieren) und legt
// drei Schatten an — Karte, vertrauliche Karte, Termin —, damit jede Spalte
// der Tabelle einen Wert trägt. Dazu, einmal je Datenbank, das Board Nachbar
// mit denselben drei Schatten.
func Bestuecke(t testing.TB, db *sql.DB, boardID string) {
	t.Helper()
	ctx := context.Background()
	boards := storage.NewBoardRepo(db)
	if _, err := boards.Get(ctx, Nachbar); err != nil {
		if _, err := boards.Insert(ctx, Nachbar, "Nachbar", "2",
			[]byte(`{"version":"2","board":{"id":"`+Nachbar+`","title":"Nachbar","sprints":[]}}`)); err != nil {
			t.Fatalf("schattentest: Nachbar anlegen: %v", err)
		}
		bestuecke(t, db, Nachbar)
	}
	bestuecke(t, db, boardID)
}

func bestuecke(t testing.TB, db *sql.DB, boardID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := storage.NewBoardpaarRepo(db).AussenKoppeln(ctx, boardID,
		"https://innen.example/#board=innen-"+boardID, "admin"); err != nil {
		t.Fatalf("schattentest: koppeln %s: %v", boardID, err)
	}
	menge := []schatten.Eingang{eingang(1, false), eingang(2, true), termin(3)}
	if _, err := storage.NewSchattenRepo(db).Ersetze(ctx, boardID, menge); err != nil {
		t.Fatalf("schattentest: Ersetze %s: %v", boardID, err)
	}
}

func sid(n int) string {
	return strings.Replace(fmt.Sprintf("%043d", n), "0", "M", 1)
}

func eingang(n int, vertraulich bool) schatten.Eingang {
	titel := fmt.Sprintf("%s Karte %d", Marker, n)
	sp, pr := "In Arbeit", "hoch"
	nr := 700 + n
	e := schatten.Eingang{SchattenID: sid(n), Art: schatten.ArtKarte, Nr: &nr, Titel: &titel,
		Spalte: &sp, Prioritaet: &pr, Bearbeiter: []string{"Gerd Muster"}, VonAgent: n%2 == 0}
	if vertraulich {
		e.Vertraulich = true
		e.Titel, e.Nr, e.Bearbeiter = nil, nil, nil
	}
	return e
}

func termin(n int) schatten.Eingang {
	titel := fmt.Sprintf("%s Termin %d", Marker, n)
	sd, sz, ed, ez := "2026-10-20", "09:00", "2026-10-20", "10:00"
	return schatten.Eingang{SchattenID: sid(n), Art: schatten.ArtTermin, Titel: &titel,
		StartDatum: &sd, StartZeit: &sz, EndeDatum: &ed, EndeZeit: &ez}
}

// Momentaufnahme gibt den vollständigen Inhalt von schatten und
// schatten_abgleich zurück, Zeile für Zeile, sortiert. Leere Tabellen ergeben
// den Text „leer“, damit ein Test, der vergessen hat zu bestücken, nicht
// unbemerkt zwei leere Aufnahmen vergleicht — dafür gibt es MussBestueckt.
func Momentaufnahme(t testing.TB, db *sql.DB) string {
	t.Helper()
	var sb strings.Builder
	for _, q := range []string{
		`SELECT * FROM schatten ORDER BY board_id, schatten_id`,
		`SELECT * FROM schatten_abgleich ORDER BY board_id`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatalf("schattentest: %s: %v", q, err)
		}
		cols, _ := rows.Columns()
		n := 0
		for rows.Next() {
			werte := make([]any, len(cols))
			ziele := make([]any, len(cols))
			for i := range werte {
				ziele[i] = &werte[i]
			}
			if err := rows.Scan(ziele...); err != nil {
				_ = rows.Close()
				t.Fatalf("schattentest: scan: %v", err)
			}
			// Typ und gequoteter Wert: 0 und "0", ein Wert mit „|“ und zwei
			// Felder bleiben unterscheidbar (Kritik K-7, Klasse 152).
			for i, w := range werte {
				if b, ok := w.([]byte); ok {
					w = string(b)
				}
				fmt.Fprintf(&sb, "%s=%T:%q|", cols[i], w, fmt.Sprint(w))
			}
			sb.WriteString("\n")
			n++
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("schattentest: rows: %v", err)
		}
		_ = rows.Close()
		fmt.Fprintf(&sb, "-- %d Zeile(n)\n", n)
	}
	return sb.String()
}

// MussBestueckt bricht ab, wenn boardID oder der Nachbar keine drei Schatten
// trägt. Schützt den Vergleich davor, zwei leere Aufnahmen gleichzusetzen.
func MussBestueckt(t testing.TB, db *sql.DB, boardID string) {
	t.Helper()
	for _, b := range []string{boardID, Nachbar} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schatten WHERE board_id = ?`, b).Scan(&n); err != nil {
			t.Fatalf("schattentest: zählen: %v", err)
		}
		if n != 3 {
			t.Fatalf("schattentest: %s trägt %d Schatten statt 3", b, n)
		}
	}
}

// Unveraendert vergleicht eine frühere Momentaufnahme mit dem jetzigen Stand.
func Unveraendert(t testing.TB, db *sql.DB, vorher, weg string) {
	t.Helper()
	if nachher := Momentaufnahme(t, db); nachher != vorher {
		t.Errorf("%s hat die Schatten verändert:\nvorher:\n%s\nnachher:\n%s", weg, vorher, nachher)
	}
}
