package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// Karte #827 E4 (Plan D9): Die Folge einer Einstellung läuft in derselben
// Transaktion. Scheitert sie, bleibt die Einstellung unverändert; sie bekommt
// den alten und den neuen Klartext.
func TestKonfigFolge827(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	cfg := NewConfigRepo(db, nil, uuid.NewString)
	var alt, neu []string
	merke := func(_ context.Context, _ *sql.Conn, a, n string) error {
		alt, neu = append(alt, a), append(neu, n)
		return nil
	}
	if err := cfg.SetMitFolge(ctx, "x.y", "eins", false, "t", merke); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetMitFolge(ctx, "x.y", "zwei", false, "t", merke); err != nil {
		t.Fatal(err)
	}
	if err := cfg.DeleteMitFolge(ctx, "x.y", "t", merke); err != nil {
		t.Fatal(err)
	}
	// Löschen ohne Eintrag: keine Folge.
	if err := cfg.DeleteMitFolge(ctx, "x.y", "t", merke); err != nil {
		t.Fatal(err)
	}
	if len(alt) != 3 || alt[0] != "" || neu[0] != "eins" || alt[1] != "eins" || neu[1] != "zwei" || alt[2] != "zwei" || neu[2] != "" {
		t.Errorf("Folge sah alt=%q neu=%q", alt, neu)
	}

	if err := cfg.Set(ctx, "x.y", "bleibt", false, "t"); err != nil {
		t.Fatal(err)
	}
	kaputt := errors.New("folge kaputt")
	scheitert := func(ctx context.Context, conn *sql.Conn, _, _ string) error {
		// Die Folge schreibt erst etwas — auch das muss zurückrollen.
		if _, err := conn.ExecContext(ctx, `INSERT INTO server_config (key, value_plain, is_secret, updated_at) VALUES ('folge.spur', 'x', 0, 'x')`); err != nil {
			return err
		}
		return kaputt
	}
	if err := cfg.SetMitFolge(ctx, "x.y", "neu", false, "t", scheitert); !errors.Is(err, kaputt) {
		t.Fatalf("Set: %v", err)
	}
	if err := cfg.DeleteMitFolge(ctx, "x.y", "t", scheitert); !errors.Is(err, kaputt) {
		t.Fatalf("Delete: %v", err)
	}
	if v, err := cfg.Get(ctx, "x.y"); err != nil || v.Value != "bleibt" {
		t.Errorf("nach gescheiterter Folge: %+v %v", v, err)
	}
	if _, err := cfg.Get(ctx, "folge.spur"); !errors.Is(err, ErrConfigKeyNotFound) {
		t.Errorf("Spur der Folge nicht zurückgerollt: %v", err)
	}
}

// Plan D9: KopieAnlegen vergibt dem Team die Rolle nach dem Schalter, gelesen
// in seiner Transaktion; die Wiederaufnahme gleicht die Rolle an, weil ein
// Umschalten eine entkoppelte Kopie nicht erreicht.
func TestBoardpaar827_TeamRolleBeimAnlegen(t *testing.T) {
	ctx := context.Background()
	db := kopieTestDB(t)
	r := NewBoardpaarRepo(db)
	cfg := NewConfigRepo(db, nil, uuid.NewString)
	acl := NewAclRepo(db)
	team := []string{"team@team.example"}
	// #836: freigegebene Kopien — hier entscheidet allein der Server-Schalter;
	// bearbeitbar = 0 prüft boardpaar_bearbeitbar_836_test.go.
	frei := true
	rolle := func(board string) string {
		t.Helper()
		ro, ok, err := acl.GetRole(ctx, board, "team@team.example")
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", board, ok, err)
		}
		return ro
	}

	if _, err := r.KopieAnlegenMit(ctx, kopieID, "K", "https://i.example/#board=b", "s", team, &frei); err != nil {
		t.Fatal(err)
	}
	if ro := rolle(kopieID); ro != RoleViewer {
		t.Errorf("ohne Schalter: %s, erwartet viewer", ro)
	}
	if err := cfg.Set(ctx, KonfigBoardpaarRueckwegSchluessel, " an ", false, "t"); err != nil {
		t.Fatal(err)
	}
	zwei := "0e5d8a2c-1111-5222-8333-44445555dddd"
	if _, err := r.KopieAnlegenMit(ctx, zwei, "K2", "https://i.example/#board=b", "s", team, &frei); err != nil {
		t.Fatal(err)
	}
	if ro := rolle(zwei); ro != RoleEditor {
		t.Errorf("mit Schalter an: %s, erwartet editor", ro)
	}

	// Wiederaufnahme: Kopie 1 (viewer) entkoppeln, Schalter steht an.
	markiert := `{"version":"2","board":{"id":"` + kopieID + `","sprints":[{"id":"l","spiegel":{"id":"k"},"cards":[]}]}}`
	if _, err := db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, markiert, kopieID); err != nil {
		t.Fatal(err)
	}
	if err := r.AussenEntkoppeln(ctx, kopieID); err != nil {
		t.Fatal(err)
	}
	erg, err := r.KopieAnlegenMit(ctx, kopieID, "K", "https://i.example/#board=b", "s", team, &frei)
	if err != nil || !erg.Wiederaufgenommen {
		t.Fatalf("Wiederaufnahme: %+v %v", erg, err)
	}
	if ro := rolle(kopieID); ro != RoleEditor {
		t.Errorf("nach Wiederaufnahme mit an: %s, erwartet editor", ro)
	}
	// Und umgekehrt: Schalter aus, entkoppeln, wieder aufnehmen → viewer.
	if err := cfg.Set(ctx, KonfigBoardpaarRueckwegSchluessel, "aus", false, "t"); err != nil {
		t.Fatal(err)
	}
	if err := r.AussenEntkoppeln(ctx, kopieID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.KopieAnlegenMit(ctx, kopieID, "K", "https://i.example/#board=b", "s", team, &frei); err != nil {
		t.Fatal(err)
	}
	if ro := rolle(kopieID); ro != RoleViewer {
		t.Errorf("nach Wiederaufnahme mit aus: %s, erwartet viewer", ro)
	}
	// Die Wiederaufnahme fasst nur ihr Board an: Kopie 2 bleibt editor.
	if ro := rolle(zwei); ro != RoleEditor {
		t.Errorf("andere Kopie: %s, erwartet editor", ro)
	}
}
