package storage

// finding_zaehler_676_test.go — BoardRepo.Update senkt board.nextFindingNr nie
// ab (F-1544, Karte #676).

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func zaehlerRepo(t *testing.T, state string) *BoardRepo {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := NewBoardRepo(db)
	if _, err := repo.Insert(ctx, "b", "B", "1", []byte(state)); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return repo
}

func TestUpdate_SenktFindingZaehlerNichtAb(t *testing.T) {
	ctx := context.Background()
	// Gespeichert: F-3 vorhanden, F-4 war vergeben und ist gelöscht → Zähler 5.
	repo := zaehlerRepo(t, `{"version":"1","board":{"id":"b","title":"B","nextFindingNr":5,"sprints":[]}}`)

	// Älterer Browserstand ohne das Feld (PUT „meins gewinnt“).
	b, err := repo.Update(ctx, "b", "B", "1",
		[]byte(`{"version":"1","board":{"id":"b","title":"B","sprints":[]}}`), 0)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := findingZaehler(b.StateJSON); got != 5 {
		t.Errorf("nextFindingNr nach PUT ohne Feld = %d, erwartet 5", got)
	}
	gelesen, _ := repo.Get(ctx, "b")
	if got := findingZaehler(gelesen.StateJSON); got != 5 {
		t.Errorf("gespeichertes nextFindingNr = %d, erwartet 5", got)
	}

	// Kleinerer Wert wird ebenfalls angehoben.
	b, err = repo.Update(ctx, "b", "B", "1",
		[]byte(`{"version":"1","board":{"id":"b","title":"B","nextFindingNr":2,"sprints":[]}}`), 0)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := findingZaehler(b.StateJSON); got != 5 {
		t.Errorf("nextFindingNr nach PUT mit 2 = %d, erwartet 5", got)
	}
}

func TestUpdate_FindingZaehlerHoeherOderOhneAltwertBleibtByteGleich(t *testing.T) {
	ctx := context.Background()
	repo := zaehlerRepo(t, `{"version":"1","board":{"id":"b","title":"B","nextFindingNr":5,"sprints":[]}}`)
	neu := []byte(`{"version":"1","board":{"id":"b","title":"B","sprints":[],"nextFindingNr":9}}`)
	b, err := repo.Update(ctx, "b", "B", "1", neu, 0)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !bytes.Equal(b.StateJSON, neu) {
		t.Errorf("höherer Zähler: Bytes verändert:\n%s", b.StateJSON)
	}

	// Board ohne Findings (kein Zähler gespeichert): nichts wird ergänzt.
	repo2 := zaehlerRepo(t, `{"version":"1","board":{"id":"b","title":"B","sprints":[]}}`)
	neu2 := []byte(`{"version":"1","board":{"id":"b","title":"B","sprints":[],"x":1}}`)
	b, err = repo2.Update(ctx, "b", "B", "1", neu2, 0)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !bytes.Equal(b.StateJSON, neu2) {
		t.Errorf("ohne Altwert: Bytes verändert:\n%s", b.StateJSON)
	}
}
