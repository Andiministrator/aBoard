package storage_test

// Karte #780 (ADR-0140 §1, B-3): Die Board-Schreibwege der Speicherschicht,
// die kein HTTP-Handler allein abdeckt, lassen die Schatten unberührt —
// Cross-Board-Transfer und die beiden Einzelspalten-Schalter auf boards.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten/schattentest"
	"github.com/Andiministrator/aBoard/server/storage"
)

func boardMitKarte780(id, kartenID string) []byte {
	karten := []any{}
	if kartenID != "" {
		karten = append(karten, map[string]any{"id": kartenID, "displayId": 5, "title": "Wanderkarte",
			"description": "", "columnId": "col-backlog", "labels": []any{}, "comments": []any{}, "activity": []any{}})
	}
	raw, _ := json.Marshal(map[string]any{"version": "2", "board": map[string]any{
		"id": id, "title": id, "nextDisplayId": 9,
		"columnDefs": []any{
			map[string]any{"id": "col-backlog", "title": "Backlog"},
			map[string]any{"id": "col-in-arbeit", "title": "In Arbeit"},
		},
		"sprints": []any{map[string]any{"id": "__backlog__", "title": "Backlog", "kind": "backlog", "cards": karten}},
	}})
	return raw
}

func TestSchatten780_TransferCard(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	if _, err := boards.Insert(ctx, "src", "src", "2", boardMitKarte780("src", "k-wander")); err != nil {
		t.Fatal(err)
	}
	if _, err := boards.Insert(ctx, "dst", "dst", "2", boardMitKarte780("dst", "")); err != nil {
		t.Fatal(err)
	}
	// Beide Seiten gekoppelt: Der Transfer schreibt beide Boards.
	schattentest.Bestuecke(t, db, "src")
	schattentest.Bestuecke(t, db, "dst")
	schattentest.MussBestueckt(t, db, "src")
	schattentest.MussBestueckt(t, db, "dst")
	vorher := schattentest.Momentaufnahme(t, db)

	if _, err := boards.TransferCard(ctx, storage.TransferCardParams{SrcBoardID: "src", DstBoardID: "dst",
		CardID: "k-wander", DstColumnID: "col-in-arbeit", Actor: "anna@example.com"}); err != nil {
		t.Fatalf("TransferCard: %v", err)
	}
	if b, _ := boards.Get(ctx, "dst"); !strings.Contains(string(b.StateJSON), "k-wander") {
		t.Fatal("Transfer hat nicht geschrieben")
	}
	schattentest.Unveraendert(t, db, vorher, "TransferCard")
}

func TestSchatten780_Einzelschalter(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := storage.NewBoardRepo(db).Insert(ctx, "b1", "b1", "2", boardMitKarte780("b1", "")); err != nil {
		t.Fatal(err)
	}
	schattentest.Bestuecke(t, db, "b1")
	schattentest.MussBestueckt(t, db, "b1")
	vorher := schattentest.Momentaufnahme(t, db)

	if err := storage.NewAgentTokenRepo(db).SetzeSelbstbedienung(ctx, "b1", true, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetzeSchleuseBoardSperre(ctx, db, "b1", true, "admin"); err != nil {
		t.Fatal(err)
	}
	var sb, sg int
	if err := db.QueryRow(`SELECT agent_selbstbedienung, schleuse_gesperrt FROM boards WHERE id = 'b1'`).Scan(&sb, &sg); err != nil || sb != 1 || sg != 1 {
		t.Fatalf("Schalter nicht geschrieben: %d %d %v", sb, sg, err)
	}
	schattentest.Unveraendert(t, db, vorher, "Einzelschalter auf boards")
}
