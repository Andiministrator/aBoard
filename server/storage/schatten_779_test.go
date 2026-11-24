package storage_test

// Karte #779 (ADR-0140 §1, §3): Schatten in eigener Tabelle — Vollabgleich
// je Board, Aktualität, Kaskade beim Entkoppeln und Board-Löschen.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

func schattenTestDB(t *testing.T) (*sql.DB, *storage.SchattenRepo, *storage.BoardpaarRepo, *storage.BoardRepo) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	for _, id := range []string{"aussen1", "aussen2", "frei"} {
		if _, err := boards.Insert(ctx, id, "B", "2", []byte(`{"version":"2","board":{"id":"`+id+`","sprints":[]}}`)); err != nil {
			t.Fatal(err)
		}
	}
	paar := storage.NewBoardpaarRepo(db)
	for _, id := range []string{"aussen1", "aussen2"} {
		if _, err := paar.AussenKoppeln(ctx, id, "https://innen.example/#board=innen-"+id, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	return db, storage.NewSchattenRepo(db), paar, boards
}

func sid779(n int) string {
	return strings.Replace(fmt.Sprintf("%043d", n), "0", "S", 1)
}

func schatten779(n int, titel string) schatten.Eingang {
	sp, pr := "In Arbeit", "hoch"
	nr := n
	return schatten.Eingang{SchattenID: sid779(n), Art: schatten.ArtKarte, Nr: &nr, Titel: &titel,
		Spalte: &sp, Prioritaet: &pr, Bearbeiter: []string{"Gerd Muster"}}
}

func zaehle779(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSchatten779_Vollabgleich(t *testing.T) {
	ctx := context.Background()
	db, repo, _, _ := schattenTestDB(t)

	erg, err := repo.Ersetze(ctx, "aussen1", []schatten.Eingang{schatten779(1, "Eins"), schatten779(2, "Zwei"), schatten779(3, "Drei")})
	if err != nil || erg.Neu != 3 || erg.Geaendert != 0 || erg.Entfernt != 0 || erg.Anzahl != 3 {
		t.Fatalf("erster Lauf: %+v %v", erg, err)
	}
	stand1, _ := repo.Stand(ctx, "aussen1")
	liste1, _ := repo.Liste(ctx, "aussen1")
	if stand1 == nil || stand1.Anzahl != 3 || stand1.LetzteAenderung == "" || stand1.LetzteAenderung != stand1.LetzterAbgleich {
		t.Fatalf("Stand nach Lauf 1: %+v", stand1)
	}

	// Derselbe Lauf noch einmal: keine Änderung, geaendert_at und
	// letzte_aenderung bleiben, letzter_abgleich wird neu gesetzt.
	erg, err = repo.Ersetze(ctx, "aussen1", []schatten.Eingang{schatten779(3, "Drei"), schatten779(1, "Eins"), schatten779(2, "Zwei")})
	if err != nil || erg.HatAenderung() || erg.Anzahl != 3 {
		t.Fatalf("Wiederholung: %+v %v", erg, err)
	}
	stand2, _ := repo.Stand(ctx, "aussen1")
	liste2, _ := repo.Liste(ctx, "aussen1")
	if stand2.LetzteAenderung != stand1.LetzteAenderung || stand2.LetzterAbgleich == stand1.LetzterAbgleich {
		t.Errorf("Stand nach Wiederholung: %+v (vorher %+v)", stand2, stand1)
	}
	for i := range liste1 {
		if liste1[i].GeaendertAt != liste2[i].GeaendertAt {
			t.Errorf("geaendert_at bewegt ohne Änderung: %s → %s", liste1[i].GeaendertAt, liste2[i].GeaendertAt)
		}
	}

	// Neu, geändert, entfernt in einem Lauf.
	geaendert := schatten779(2, "Zwei neu")
	vertraulich := schatten779(3, "")
	vertraulich.Titel, vertraulich.Vertraulich = nil, true
	termin := schatten.Eingang{SchattenID: sid779(4), Art: schatten.ArtTermin, Bearbeiter: []string{}, VonAgent: true}
	erg, err = repo.Ersetze(ctx, "aussen1", []schatten.Eingang{geaendert, vertraulich, termin})
	if err != nil || erg.Neu != 1 || erg.Geaendert != 2 || erg.Entfernt != 1 || erg.Anzahl != 3 {
		t.Fatalf("gemischter Lauf: %+v %v", erg, err)
	}
	liste3, _ := repo.Liste(ctx, "aussen1")
	if len(liste3) != 3 {
		t.Fatalf("Liste: %+v", liste3)
	}
	nach := map[string]storage.SchattenZeile{}
	for _, z := range liste3 {
		nach[z.SchattenID] = z
	}
	if z := nach[sid779(3)]; !z.Vertraulich || z.Titel != nil {
		t.Errorf("vertraulich: %+v", z)
	}
	if z := nach[sid779(2)]; *z.Titel != "Zwei neu" || z.GeaendertAt == liste1[1].GeaendertAt {
		t.Errorf("geändert: %+v", z)
	}
	if z := nach[sid779(4)]; z.Art != schatten.ArtTermin || z.Nr != nil || z.Spalte != nil || !z.VonAgent || z.Bearbeiter == nil {
		t.Errorf("Termin: %+v", z)
	}

	// Eine gültige leere Liste entfernt alle (Vertrag Push-Skript, Ziffer 9.3) — keine Bremse.
	erg, err = repo.Ersetze(ctx, "aussen1", nil)
	if err != nil || erg.Entfernt != 3 || erg.Anzahl != 0 {
		t.Fatalf("leere Liste: %+v %v", erg, err)
	}
	if n := zaehle779(t, db, `SELECT COUNT(*) FROM schatten WHERE board_id = 'aussen1'`); n != 0 {
		t.Errorf("nach leerer Liste: %d Zeilen", n)
	}
	if s, _ := repo.Stand(ctx, "aussen1"); s.Anzahl != 0 {
		t.Errorf("Stand nach leerer Liste: %+v", s)
	}
}

// Boards sind getrennt: Ein Lauf für aussen1 berührt aussen2 nicht.
func TestSchatten779_BoardsGetrennt(t *testing.T) {
	ctx := context.Background()
	_, repo, _, _ := schattenTestDB(t)
	if _, err := repo.Ersetze(ctx, "aussen2", []schatten.Eingang{schatten779(1, "Zwei")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Ersetze(ctx, "aussen1", nil); err != nil {
		t.Fatal(err)
	}
	if l, _ := repo.Liste(ctx, "aussen2"); len(l) != 1 {
		t.Errorf("aussen2 berührt: %+v", l)
	}
}

// Nicht gekoppelt (auch Service-Board, leere ID): Fehler, nichts geschrieben.
func TestSchatten779_NichtGekoppelt(t *testing.T) {
	ctx := context.Background()
	db, repo, _, _ := schattenTestDB(t)
	for _, b := range []string{"frei", "gibtsnicht", storage.ServiceBoardID, ""} {
		if _, err := repo.Ersetze(ctx, b, []schatten.Eingang{schatten779(1, "x")}); !errors.Is(err, storage.ErrBoardpaarNichtGekoppelt) {
			t.Errorf("%q: %v", b, err)
		}
	}
	if n := zaehle779(t, db, `SELECT COUNT(*) FROM schatten`) + zaehle779(t, db, `SELECT COUNT(*) FROM schatten_abgleich`); n != 0 {
		t.Errorf("%d Zeilen geschrieben", n)
	}
}

// Ganz oder gar nicht: Scheitert der Lauf mittendrin, bleibt der alte
// Bestand vollständig stehen, auch der Abgleich-Stand.
func TestSchatten779_ScheiternRolltZurueck(t *testing.T) {
	ctx := context.Background()
	db, repo, _, _ := schattenTestDB(t)
	if _, err := repo.Ersetze(ctx, "aussen1", []schatten.Eingang{schatten779(1, "Alt"), schatten779(2, "Alt")}); err != nil {
		t.Fatal(err)
	}
	vorher, _ := repo.Stand(ctx, "aussen1")
	// Doppelte Kennung am Ende — nach einem Einfügen und vor dem Entfernen.
	_, err := repo.Ersetze(ctx, "aussen1", []schatten.Eingang{schatten779(3, "Neu"), schatten779(3, "Neu")})
	if err == nil {
		t.Fatal("doppelte Kennung angenommen")
	}
	l, _ := repo.Liste(ctx, "aussen1")
	if len(l) != 2 || l[0].SchattenID != sid779(1) || *l[0].Titel != "Alt" {
		t.Errorf("Bestand nach Scheitern: %+v", l)
	}
	if n := zaehle779(t, db, `SELECT COUNT(*) FROM schatten WHERE schatten_id = ?`, sid779(3)); n != 0 {
		t.Errorf("halber Lauf geschrieben")
	}
	if nachher, _ := repo.Stand(ctx, "aussen1"); *nachher != *vorher {
		t.Errorf("Stand bewegt: %+v → %+v", vorher, nachher)
	}
}

// Katalog „Board löschen bzw. entkoppeln → keine Schatten-Zeile übrig“
// (aus #788 hierher übergeben): Die Kaskade läuft über boardpaar_aussen.
func TestSchatten779_Kaskade(t *testing.T) {
	ctx := context.Background()
	db, repo, paar, boards := schattenTestDB(t)
	for _, b := range []string{"aussen1", "aussen2"} {
		if _, err := repo.Ersetze(ctx, b, []schatten.Eingang{schatten779(1, "x"), schatten779(2, "y")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := paar.AussenEntkoppeln(ctx, "aussen1"); err != nil {
		t.Fatal(err)
	}
	if n := zaehle779(t, db, `SELECT COUNT(*) FROM schatten WHERE board_id = 'aussen1'`) +
		zaehle779(t, db, `SELECT COUNT(*) FROM schatten_abgleich WHERE board_id = 'aussen1'`); n != 0 {
		t.Errorf("nach Entkoppeln: %d Zeilen", n)
	}
	if err := boards.Delete(ctx, "aussen2"); err != nil {
		t.Fatal(err)
	}
	if n := zaehle779(t, db, `SELECT COUNT(*) FROM schatten`) + zaehle779(t, db, `SELECT COUNT(*) FROM schatten_abgleich`); n != 0 {
		t.Errorf("nach Board-Löschen: %d Zeilen", n)
	}
	// Neu koppeln bringt nichts zurück.
	if _, err := paar.AussenKoppeln(ctx, "aussen1", "https://innen.example/#board=x", "admin"); err != nil {
		t.Fatal(err)
	}
	if l, _ := repo.Liste(ctx, "aussen1"); len(l) != 0 {
		t.Errorf("nach erneutem Koppeln: %+v", l)
	}
}

// Kein Board-Schreibweg berührt die Tabelle: Der Abgleich ändert weder den
// Board-Zustand noch updated_at (ADR-0140 §1, Nebenwirkungen §3).
func TestSchatten779_BoardUnberuehrt(t *testing.T) {
	ctx := context.Background()
	_, repo, _, boards := schattenTestDB(t)
	vorher, err := boards.Get(ctx, "aussen1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Ersetze(ctx, "aussen1", []schatten.Eingang{schatten779(1, "x")}); err != nil {
		t.Fatal(err)
	}
	nachher, _ := boards.Get(ctx, "aussen1")
	if nachher.UpdatedAt != vorher.UpdatedAt || string(nachher.StateJSON) != string(vorher.StateJSON) {
		t.Errorf("Board verändert: %v → %v", vorher.UpdatedAt, nachher.UpdatedAt)
	}
}

// 5000 Schatten in einem Lauf (Zählgrenze des Eingangs) gehen durch.
func TestSchatten779_Volllast(t *testing.T) {
	ctx := context.Background()
	_, repo, _, _ := schattenTestDB(t)
	menge := make([]schatten.Eingang, schatten.EingangMaxSchatten)
	for i := range menge {
		menge[i] = schatten779(i+1, "Karte mit einem Titel")
	}
	if erg, err := repo.Ersetze(ctx, "aussen1", menge); err != nil || erg.Neu != len(menge) {
		t.Fatalf("%+v %v", erg, err)
	}
	if erg, err := repo.Ersetze(ctx, "aussen1", menge); err != nil || erg.HatAenderung() {
		t.Fatalf("Wiederholung: %+v %v", erg, err)
	}
}

// K-8 (Kritiker #779): Hält ein anderer Schreiber die Sperre, liefert
// Ersetze ErrDBBelegt statt eines allgemeinen
// Fehlers — der Eingang antwortet dann 503.
func TestSchatten779_BelegtIstErrDBBelegt(t *testing.T) {
	ctx := context.Background()
	db, repo, _, _ := schattenTestDB(t)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()
	kurz, abbruch := context.WithTimeout(ctx, 15*time.Second)
	defer abbruch()
	_, err = repo.Ersetze(kurz, "aussen1", nil)
	// Die Frist (15 s) liegt weit über busy_timeout (5 s): Es muss die Sperre
	// sein, die zuschlägt, nicht die Frist.
	if !errors.Is(err, storage.ErrDBBelegt) {
		t.Errorf("Fehler %v, erwartet ErrDBBelegt", err)
	}
}
