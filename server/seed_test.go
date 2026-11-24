package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// openTestRepo öffnet eine frische Test-DB und liefert ctx + Repo.
func openTestRepo(t *testing.T) (context.Context, *storage.BoardRepo) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return ctx, storage.NewBoardRepo(db)
}

// TestSeedDefaultBoard_LeereDB: ADR-0054 Teil B (F-253, L1) — eine leere DB
// bekommt genau ein Board mit fester id `default`, Titel „Board", Version „2"
// und exakt dem kanonischen Seed-JSON.
func TestSeedDefaultBoard_LeereDB(t *testing.T) {
	ctx, repo := openTestRepo(t)

	if err := seedDefaultBoard(ctx, repo, nil); err != nil {
		t.Fatalf("seedDefaultBoard: %v", err)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List-Länge = %d, erwartet 1", len(list))
	}
	if list[0].ID != "default" {
		t.Errorf("Board-id = %q, erwartet %q", list[0].ID, "default")
	}

	b, err := repo.Get(ctx, "default")
	if err != nil {
		t.Fatalf("Get(default): %v", err)
	}
	if b.Title != "Board" {
		t.Errorf("Title = %q, erwartet %q", b.Title, "Board")
	}
	if b.Version != "2" {
		t.Errorf("Version = %q, erwartet %q", b.Version, "2")
	}
	if string(b.StateJSON) != defaultBoardSeed {
		t.Errorf("StateJSON weicht vom kanonischen Seed ab:\n got: %s\nwant: %s",
			b.StateJSON, defaultBoardSeed)
	}
}

// TestSeedDefaultBoard_Idempotent: zweiter Aufruf legt kein weiteres Board an.
func TestSeedDefaultBoard_Idempotent(t *testing.T) {
	ctx, repo := openTestRepo(t)

	for i := 0; i < 3; i++ {
		if err := seedDefaultBoard(ctx, repo, nil); err != nil {
			t.Fatalf("seedDefaultBoard (Lauf %d): %v", i, err)
		}
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List-Länge = %d nach 3 Aufrufen, erwartet 1 (idempotent)", len(list))
	}
}

// TestSeedDefaultBoard_BestandUnberuehrt: existiert schon ein Board, wird
// weder ein `default`-Board angelegt noch der Bestand verändert.
func TestSeedDefaultBoard_BestandUnberuehrt(t *testing.T) {
	ctx, repo := openTestRepo(t)

	bestand := []byte(`{"version":"2","board":{"id":"vorhanden","title":"Echt"}}`)
	if _, err := repo.Insert(ctx, "vorhanden", "Echt", "2", bestand); err != nil {
		t.Fatalf("Insert Bestand: %v", err)
	}

	if err := seedDefaultBoard(ctx, repo, nil); err != nil {
		t.Fatalf("seedDefaultBoard: %v", err)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List-Länge = %d, erwartet 1 (kein Default-Board bei Bestand)", len(list))
	}
	if list[0].ID != "vorhanden" {
		t.Errorf("Board-id = %q, erwartet %q (Bestand unverändert)", list[0].ID, "vorhanden")
	}
	if _, err := repo.Get(ctx, "default"); err == nil {
		t.Errorf("Get(default) lieferte ein Board — erwartet: kein Default-Board bei Bestand")
	}
}

// TestDefaultBoardSeed_StrukturGueltig: das Seed-JSON ist ein vollständiges
// v2-Board mit dem kanonischen Spaltensatz und der Backlog-Swimlane — Schutz
// gegen Schema-Drift gegenüber Model.createBoard (src/model.js).
func TestDefaultBoardSeed_StrukturGueltig(t *testing.T) {
	var s struct {
		Version string `json:"version"`
		Board   struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			NextDisplayID int    `json:"nextDisplayId"`
			ColumnDefs    []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"columnDefs"`
			Sprints []struct {
				ID    string `json:"id"`
				Kind  string `json:"kind"`
				Cards []any  `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal([]byte(defaultBoardSeed), &s); err != nil {
		t.Fatalf("defaultBoardSeed ist kein gültiges JSON: %v", err)
	}

	if s.Version != "2" {
		t.Errorf("version = %q, erwartet %q", s.Version, "2")
	}
	if s.Board.ID != "default" {
		t.Errorf("board.id = %q, erwartet %q", s.Board.ID, "default")
	}
	if s.Board.NextDisplayID != 1 {
		t.Errorf("nextDisplayId = %d, erwartet 1", s.Board.NextDisplayID)
	}

	wantCols := []string{"col-backlog", "col-ready", "col-in-arbeit", "col-erledigt"}
	if len(s.Board.ColumnDefs) != len(wantCols) {
		t.Fatalf("columnDefs-Länge = %d, erwartet %d", len(s.Board.ColumnDefs), len(wantCols))
	}
	for i, want := range wantCols {
		if s.Board.ColumnDefs[i].ID != want {
			t.Errorf("columnDefs[%d].id = %q, erwartet %q", i, s.Board.ColumnDefs[i].ID, want)
		}
	}

	if len(s.Board.Sprints) != 1 {
		t.Fatalf("sprints-Länge = %d, erwartet 1", len(s.Board.Sprints))
	}
	if s.Board.Sprints[0].ID != "__backlog__" {
		t.Errorf("sprints[0].id = %q, erwartet %q", s.Board.Sprints[0].ID, "__backlog__")
	}
	if s.Board.Sprints[0].Kind != "backlog" {
		t.Errorf("sprints[0].kind = %q, erwartet %q", s.Board.Sprints[0].Kind, "backlog")
	}
	if len(s.Board.Sprints[0].Cards) != 0 {
		t.Errorf("sprints[0].cards-Länge = %d, erwartet 0 (leeres Board)", len(s.Board.Sprints[0].Cards))
	}
}
