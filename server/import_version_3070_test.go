package main

// F-3070 (Sitzungs-Kritik 11.11.): Der Import liest `version` auch als Zahl 1/2.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestF3070_ImportVersion(t *testing.T) {
	gut := map[string]string{``: "2", `null`: "2", `""`: "2", `"2"`: "2", `"1"`: "1", `2`: "2", `1`: "1"}
	for roh, soll := range gut {
		ist, err := importVersion(json.RawMessage(roh))
		if err != nil || ist != soll {
			t.Errorf("%q: %q, %v — erwartet %q", roh, ist, err, soll)
		}
	}
	for _, roh := range []string{`3`, `2.5`, `true`, `{}`} {
		if _, err := importVersion(json.RawMessage(roh)); err == nil {
			t.Errorf("%q: kein Fehler", roh)
		}
	}
}

// Sprint-118-Kritik (Fund 1): Nach dem Import einer Datei mit `"version": 2` steht auch im
// state_json Text — ein Leser mit `Version string` (wie boardbackup/restore.go) scheitert sonst.
// F-3092 (Verify 2b): dasselbe für fehlende, null- und leere version.
func TestImport_VersionZahlImBlobAlsText(t *testing.T) {
	for name, kopf := range map[string]string{
		"zahl":    `"version": 2, `,
		"fehlend": ``,
		"null":    `"version": null, `,
		"leer":    `"version": "", `,
	} {
		t.Run(name, func(t *testing.T) { pruefeVersionImBlob(t, kopf) })
	}
}

func pruefeVersionImBlob(t *testing.T, kopf string) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "aboard-sync.json")
	if err := os.WriteFile(jsonPath, []byte(`{`+kopf+`"board": {"id": "b-zahl", "title": "Zahl", "sprints": []}}`), 0o644); err != nil {
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
	b, err := storage.NewBoardRepo(db).Get(ctx, "b-zahl")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var huelle struct {
		Version string `json:"version"`
		Board   struct {
			Title   string          `json:"title"`
			Sprints json.RawMessage `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(b.StateJSON, &huelle); err != nil {
		t.Fatalf("state_json mit Version string nicht lesbar: %v — %s", err, b.StateJSON)
	}
	if b.Version != "2" { // Spalte (Verify 2 Sprint 118)
		t.Errorf("Spalte version = %q, erwartet \"2\"", b.Version)
	}
	if huelle.Version != "2" || huelle.Board.Title != "Zahl" || string(huelle.Board.Sprints) != "[]" {
		t.Errorf("state_json verändert: %s", b.StateJSON)
	}
}
