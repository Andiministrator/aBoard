package storage

// Karte #778 (ADR-0140 §10, Stelle 3): BoardRepo.Update lässt „vertraulich“
// stehen — auch auf dem Restore-Weg (MitFindingNummernBehalten), ohne
// Ausnahme.

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestVertraulichBewahren778(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewBoardRepo(db)
	stand := func(k, tm string) []byte {
		return []byte(`{"version":"2","board":{"id":"b1","title":"B","sprints":[{"id":"s","cards":[{"id":"k1","title":"x"` + k +
			`},{"id":"k2","title":"y"}]}],"termine":[{"id":"t1","titel":"z","datum":"2026-10-01"` + tm + `}]}}`)
	}
	if _, err := repo.Insert(ctx, "b1", "B", "2", stand(`,"confidential":null`, `,"confidential":"ja"`)); err != nil {
		t.Fatal(err)
	}
	// Restore-Weg: kein Ausnahmefenster.
	b, err := repo.Update(MitFindingNummernBehalten(ctx), "b1", "B", "2", stand(`,"confidential":false`, ``), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.VertraulichBewahrt, []string{"k1", "t1"}) {
		t.Fatalf("bewahrt: %v", b.VertraulichBewahrt)
	}
	if !bytes.Contains(b.StateJSON, []byte(`"confidential":null`)) || !bytes.Contains(b.StateJSON, []byte(`"confidential":"ja"`)) {
		t.Fatalf("Bestandswerte nicht übernommen: %s", b.StateJSON)
	}
	// Eine neue vertrauliche Karte darf dazukommen; nichts zu bewahren → Bytes unverändert.
	neu := stand(`,"confidential":null`, `,"confidential":"ja"`)
	neu = bytes.Replace(neu, []byte(`{"id":"k2","title":"y"}`), []byte(`{"id":"k2","title":"y","confidential":true}`), 1)
	b, err = repo.Update(ctx, "b1", "B", "2", neu, 0)
	if err != nil || len(b.VertraulichBewahrt) != 0 || !bytes.Equal(b.StateJSON, neu) {
		t.Fatalf("ohne Anlass verändert: %v %v %s", err, b.VertraulichBewahrt, b.StateJSON)
	}
	// Unlesbarer neuer Stand bei vertraulichem Bestand: Fehler statt still speichern.
	if _, err := repo.Update(ctx, "b1", "B", "2", []byte(`{"board":`), 0); err == nil {
		t.Fatal("unlesbarer Stand gespeichert")
	}
}

// Kritiker #778, K-1/K-7: Ein PUT, der die Verbindung eines Termins zu
// seiner vertraulichen Karte löst, lässt den Termin vertraulich (er bekommt
// den Schalter selbst). Zahlen laufen dabei nicht durch float64.
func TestVertraulichBewahrenVererbung778(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewBoardRepo(db)
	alt := []byte(`{"version":"2","board":{"id":"b1","title":"B","sprints":[{"id":"s","cards":[{"id":"k","title":"x","confidential":true}]}],
		"termine":[{"id":"t","titel":"z","datum":"2026-10-01","karteId":"k"}]}}`)
	if _, err := repo.Insert(ctx, "b1", "B", "2", alt); err != nil {
		t.Fatal(err)
	}
	neu := []byte(`{"version":"2","board":{"id":"b1","title":"B","gross":12345678901234567890,"sprints":[{"id":"s","cards":[{"id":"k","title":"x","confidential":true}]}],
		"termine":[{"id":"t","titel":"z","datum":"2026-10-01"}]}}`)
	b, err := repo.Update(ctx, "b1", "B", "2", neu, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.VertraulichBewahrt, []string{"t"}) {
		t.Fatalf("bewahrt: %v", b.VertraulichBewahrt)
	}
	var st struct {
		Board struct{ Termine []map[string]any }
	}
	if err := json.Unmarshal(b.StateJSON, &st); err != nil || len(st.Board.Termine) != 1 || st.Board.Termine[0]["confidential"] != true {
		t.Fatalf("Termin ohne Schalter: %s", b.StateJSON)
	}
	if !bytes.Contains(b.StateJSON, []byte(`12345678901234567890`)) {
		t.Fatalf("Zahl verändert: %s", b.StateJSON)
	}
}

// Verify #778, V-1/V-2/V-4: Bewahren greift auch ohne „confidential“ im
// Altstand (Termin an fehlender Karte) und bei escaptem Schlüssel; Daten
// hinter dem Objekt sind ein Fehler.
func TestVertraulichBewahrenVorfilter778(t *testing.T) {
	ohne := []byte(`{"version":"2","board":{"id":"b1","title":"B","sprints":[{"id":"s","cards":[]}],"termine":[{"id":"t","titel":"z","datum":"2026-10-01"}]}}`)
	alt := []byte(`{"version":"2","board":{"id":"b1","title":"B","sprints":[{"id":"s","cards":[]}],"termine":[{"id":"t","titel":"z","datum":"2026-10-01","karteId":"weg"}]}}`)
	neu, bewahrt, err := vertraulichBewahren(alt, ohne)
	if err != nil || !reflect.DeepEqual(bewahrt, []string{"t"}) || !bytes.Contains(neu, []byte(`"confidential":true`)) {
		t.Fatalf("fehlende Karte: %v %v %s", err, bewahrt, neu)
	}
	esc := []byte(`{"version":"2","board":{"id":"b1","title":"B","sprints":[{"id":"s","cards":[{"id":"k","title":"x","\u0063onfidential":true}]}]}}`)
	if bytes.Contains(esc, []byte("confidential")) {
		t.Fatal("Testfall ohne Escape")
	}
	klar := []byte(`{"version":"2","board":{"id":"b1","title":"B","sprints":[{"id":"s","cards":[{"id":"k","title":"x"}]}]}}`)
	if _, bewahrt, err := vertraulichBewahren(esc, klar); err != nil || len(bewahrt) != 1 {
		t.Fatalf("escapter Schlüssel: %v %v", err, bewahrt)
	}
	for _, rest := range []string{` {"x":1}`, `}`, `]`, ` x`} {
		if _, _, err := vertraulichBewahren(alt, append(append([]byte{}, ohne...), []byte(rest)...)); err == nil {
			t.Fatalf("Daten hinter dem Objekt angenommen: %q", rest)
		}
	}
	// Leerraum am Ende ist gültig.
	if _, _, err := vertraulichBewahren(alt, append(append([]byte{}, ohne...), []byte(" \n")...)); err != nil {
		t.Fatalf("Leerraum am Ende abgelehnt: %v", err)
	}
}
