package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func testBoardJSON(t *testing.T, id, title, version string) []byte {
	t.Helper()
	m := map[string]any{
		"version": version,
		"board":   map[string]any{"id": id, "title": title},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return raw
}

func TestImportFile_NeuesBoard(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "aboard-sync.json")

	if err := os.WriteFile(jsonPath, testBoardJSON(t, "board-1", "Mein Testboard", "2"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := importFile(ctx, dbPath, jsonPath); err != nil {
		t.Fatalf("importFile: %v", err)
	}

	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	b, err := storage.NewBoardRepo(db).Get(ctx, "board-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if b.Title != "Mein Testboard" {
		t.Errorf("Title = %q, erwartet %q", b.Title, "Mein Testboard")
	}
	if b.Version != "2" {
		t.Errorf("Version = %q, erwartet %q", b.Version, "2")
	}
}

func TestImportFile_UeberschreibtVorhandenes(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "aboard-sync.json")

	if err := os.WriteFile(jsonPath, testBoardJSON(t, "board-upsert", "Alter Titel", "2"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := importFile(ctx, dbPath, jsonPath); err != nil {
		t.Fatalf("Erst-Import: %v", err)
	}

	if err := os.WriteFile(jsonPath, testBoardJSON(t, "board-upsert", "Neuer Titel", "2"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := importFile(ctx, dbPath, jsonPath); err != nil {
		t.Fatalf("Re-Import: %v", err)
	}

	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	b, err := storage.NewBoardRepo(db).Get(ctx, "board-upsert")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if b.Title != "Neuer Titel" {
		t.Errorf("Title = %q, erwartet %q", b.Title, "Neuer Titel")
	}
}

func TestImportFile_FehlendeID_WirdGeneriert(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "aboard-sync.json")

	raw, _ := json.Marshal(map[string]any{
		"version": "2",
		"board":   map[string]any{"title": "Board ohne ID"},
	})
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := importFile(ctx, dbPath, jsonPath); err != nil {
		t.Fatalf("importFile: %v", err)
	}

	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	list, err := storage.NewBoardRepo(db).List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, erwartet 1", len(list))
	}
	if list[0].Title != "Board ohne ID" {
		t.Errorf("Title = %q", list[0].Title)
	}
}

func TestImportFile_FehlendeTitle_Fehler(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "aboard-sync.json")

	raw, _ := json.Marshal(map[string]any{
		"version": "2",
		"board":   map[string]any{"id": "b-notitle"},
	})
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := importFile(ctx, dbPath, jsonPath); err == nil {
		t.Fatal("erwartet Fehler bei fehlendem title, keiner erhalten")
	}
}

func TestImportFile_UngueltigesJSON_Fehler(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "aboard-sync.json")

	if err := os.WriteFile(jsonPath, []byte("kein json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := importFile(ctx, dbPath, jsonPath); err == nil {
		t.Fatal("erwartet JSON-Fehler, keiner erhalten")
	}
}

func TestImportFile_FehlendeID_IDInJSONGespeichert(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "aboard-sync.json")

	raw, _ := json.Marshal(map[string]any{
		"version": "2",
		"board":   map[string]any{"title": "Board ohne ID"},
	})
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := importFile(ctx, dbPath, jsonPath); err != nil {
		t.Fatalf("importFile: %v", err)
	}

	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	repo := storage.NewBoardRepo(db)
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, erwartet 1", len(list))
	}
	dbID := list[0].ID

	b, err := repo.Get(ctx, dbID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// state_json muss dieselbe board.id enthalten die in der DB steht.
	var envelope struct {
		Board struct {
			ID string `json:"id"`
		} `json:"board"`
	}
	if err := json.Unmarshal(b.StateJSON, &envelope); err != nil {
		t.Fatalf("Unmarshal state_json: %v", err)
	}
	if envelope.Board.ID != dbID {
		t.Errorf("state_json board.id = %q, erwartet DB-ID %q", envelope.Board.ID, dbID)
	}
}

func TestImportFile_DateiNichtVorhanden_Fehler(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")

	err := importFile(ctx, dbPath, "/tmp/existiert-nicht-aboard.json")
	if err == nil {
		t.Fatal("erwartet Fehler für nicht-vorhandene Datei, keiner erhalten")
	}
}

// F-1572: Der Import prüft Finding-Karten wie die Ops; bei einem ungültigen
// Stand wird nichts angelegt. Ältere Sicherungen ohne Findings sind nicht
// betroffen (TestImportFile_NeuesBoard).
func TestImportFile_LehntUngueltigeFindingsAb(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "aboard-sync.json")
	karte := func(id, nr string) string {
		return `{"id":"` + id + `","kind":"finding","finding":{"nr":` + nr + `,"typ":"K","prioritaet":"P2","status":"offen"}}`
	}
	schlecht := `{"version":"2","board":{"id":"fb","title":"F","sprints":[{"id":"s","cards":[` + karte("a", "1") + `,` + karte("b", "1") + `]}]}}`
	if err := os.WriteFile(jsonPath, []byte(schlecht), 0o644); err != nil {
		t.Fatal(err)
	}
	err := importFile(ctx, dbPath, jsonPath)
	if err == nil || !strings.Contains(err.Error(), "more than one card") {
		t.Fatalf("Fehler = %v, erwartet Ablehnung wegen doppelter nr", err)
	}
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := storage.NewBoardRepo(db).Get(ctx, "fb"); !errors.Is(err, storage.ErrBoardNotFound) {
		t.Fatalf("Board angelegt trotz Ablehnung: %v", err)
	}
	gut := `{"version":"2","board":{"id":"fb","title":"F","sprints":[{"id":"s","cards":[` + karte("a", "1") + `,` + karte("b", "2") + `]}]}}`
	if err := os.WriteFile(jsonPath, []byte(gut), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := importFile(ctx, dbPath, jsonPath); err != nil {
		t.Fatalf("gültiger Stand: %v", err)
	}
}

// KA-7: Eine Datei mit Finding-Verstoß (doppelte nr) wird standardmäßig
// abgelehnt; der Notfall-Schalter schreibt trotzdem.
func TestImportFile_FindingVerstossNotfallSchalter(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "altlast.json")
	roh := `{"version":"2","board":{"id":"alt","title":"Altlast","sprints":[{"id":"s","cards":[` +
		`{"id":"a","kind":"finding","finding":{"nr":1,"typ":"K","prioritaet":"P2","status":"offen"}},` +
		`{"id":"b","kind":"finding","finding":{"nr":1,"typ":"K","prioritaet":"P2","status":"offen"}}]}]}}`
	if err := os.WriteFile(jsonPath, []byte(roh), 0o644); err != nil {
		t.Fatal(err)
	}
	err := importFile(ctx, dbPath, jsonPath)
	if err == nil || !strings.Contains(err.Error(), "--findings-pruefung=aus") {
		t.Fatalf("Standard: err=%v, erwartet Ablehnung mit Hinweis auf den Schalter", err)
	}
	if err := runImportMitArgs(dbPath, []string{"--findings-pruefung=aus", jsonPath}); err != nil {
		t.Fatalf("mit Schalter: %v", err)
	}
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := storage.NewBoardRepo(db).Get(ctx, "alt"); err != nil {
		t.Fatalf("Board nicht angelegt: %v", err)
	}
}
