package main

// import_1685_test.go — der CLI-Import über ein bestehendes Board behält die
// Finding-Nummern des mitgebrachten Stands (F-1685), wie die Rückspielung.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestImportFile_F1685_BehaeltFindingNummer(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	jsonPath := filepath.Join(t.TempDir(), "aboard-sync.json")
	schreib := func(s string) {
		t.Helper()
		if err := os.WriteFile(jsonPath, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := importFile(ctx, dbPath, jsonPath); err != nil {
			t.Fatalf("Import: %v", err)
		}
	}
	// Server-Stand: F-7 inzwischen gelöscht, Zähler 9.
	schreib(`{"version":"2","board":{"id":"imp","title":"I","nextFindingNr":9,"sprints":[{"id":"s","cards":[]}]}}`)
	// Mitgebrachter Stand mit F-7.
	schreib(`{"version":"2","board":{"id":"imp","title":"I","nextFindingNr":8,"sprints":[{"id":"s","cards":[` +
		`{"id":"k7","kind":"finding","finding":{"nr":7,"typ":"K","prioritaet":"P2","status":"offen"}}]}]}}`)
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, err := storage.NewBoardRepo(db).Get(ctx, "imp")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Board struct {
			Next    int `json:"nextFindingNr"`
			Sprints []struct {
				Cards []struct {
					Finding struct {
						Nr int `json:"nr"`
					} `json:"finding"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(b.StateJSON, &s); err != nil {
		t.Fatal(err)
	}
	if got := s.Board.Sprints[0].Cards[0].Finding.Nr; got != 7 || s.Board.Next != 9 {
		t.Errorf("nach Import: F-%d, Zähler %d — erwartet F-7, Zähler 9", got, s.Board.Next)
	}
}
