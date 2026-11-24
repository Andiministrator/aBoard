package storage

// transfer_finding_676_test.go — Finding-Karten beim Kartentransfer
// (F-1542, Karte #676, ADR-0133 §1 „keine Doppel").
//
// Die Finding-Nummer gehört zum Board: Im Ziel wird sie neu vergeben, der
// Ziel-Zähler `nextFindingNr` wird angehoben, und die alte Nummer bleibt über
// einen Kommentar (Board-Suche) und den Activity-Eintrag auffindbar.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func findingKarte(id string, displayID, nr int) map[string]any {
	return map[string]any{
		"id": id, "displayId": float64(displayID), "title": "Befund " + id,
		"description": "", "columnId": "col-backlog", "kind": "finding",
		"finding": map[string]any{
			"nr": float64(nr), "typ": "K", "prioritaet": "P2", "status": "offen",
		},
		"labels": []any{}, "comments": []any{}, "activity": []any{},
	}
}

func boardMitKarten(nextFindingNr any, karten ...map[string]any) []byte {
	cards := make([]any, 0, len(karten))
	for _, k := range karten {
		cards = append(cards, k)
	}
	board := map[string]any{
		"columnDefs": []any{
			map[string]any{"id": "col-backlog", "title": "Backlog"},
			map[string]any{"id": "col-in-arbeit", "title": "In Arbeit"},
		},
		"nextDisplayId": float64(20),
		"sprints": []any{map[string]any{
			"id": "__backlog__", "title": "Backlog", "kind": "backlog", "cards": cards,
		}},
	}
	if nextFindingNr != nil {
		board["nextFindingNr"] = nextFindingNr
	}
	state, _ := json.Marshal(map[string]any{"version": "1", "board": board})
	return state
}

func transferFindingRepo(t *testing.T, src, dst []byte) *BoardRepo {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, b := range []struct {
		id, title string
		state     []byte
	}{{"src", "Quelle", src}, {"dst", "Ziel", dst}} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			b.id, b.title, "1", b.state, int64(100), int64(100)); err != nil {
			t.Fatalf("insert %s: %v", b.id, err)
		}
	}
	return NewBoardRepo(db)
}

func dstBoard(t *testing.T, repo *BoardRepo) map[string]any {
	t.Helper()
	b, err := repo.Get(context.Background(), "dst")
	if err != nil {
		t.Fatalf("Get dst: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(b.StateJSON, &state); err != nil {
		t.Fatalf("parse dst: %v", err)
	}
	return state["board"].(map[string]any)
}

func TestTransferCard_FindingBekommtNeueNummerImZiel(t *testing.T) {
	// Quelle: F-3 wird übertragen. Ziel hat schon F-3 und F-7, Zähler 8.
	src := boardMitKarten(float64(4), findingKarte("f-src", 1, 3))
	dst := boardMitKarten(float64(8), findingKarte("f-a", 2, 3), findingKarte("f-b", 3, 7))
	repo := transferFindingRepo(t, src, dst)

	res, err := repo.TransferCard(context.Background(), TransferCardParams{
		SrcBoardID: "src", DstBoardID: "dst", CardID: "f-src", DstColumnID: "col-backlog", Actor: "andi@example.com",
	})
	if err != nil {
		t.Fatalf("TransferCard: %v", err)
	}
	if res.NewFindingNr != 8 || res.OldFindingNr != 3 {
		t.Fatalf("Finding-Nummern: neu %d / alt %d, erwartet 8 / 3", res.NewFindingNr, res.OldFindingNr)
	}

	board := dstBoard(t, repo)
	if v, _ := board["nextFindingNr"].(float64); v != 9 {
		t.Errorf("dst.nextFindingNr = %v, erwartet 9", v)
	}
	// Keine Nummer doppelt im Ziel.
	gesehen := map[float64]string{}
	var moved map[string]any
	for _, c := range board["sprints"].([]any)[0].(map[string]any)["cards"].([]any) {
		cm := c.(map[string]any)
		nr := cm["finding"].(map[string]any)["nr"].(float64)
		if andere, dop := gesehen[nr]; dop {
			t.Errorf("F-%v doppelt im Ziel: %s und %s", nr, andere, cm["id"])
		}
		gesehen[nr] = cm["id"].(string)
		if cm["id"] == "f-src" {
			moved = cm
		}
	}
	if moved == nil {
		t.Fatal("übertragene Karte fehlt im Ziel")
	}
	// Die alte Nummer bleibt über die Suche (Kommentare) auffindbar.
	comments, _ := moved["comments"].([]any)
	gefunden := false
	for _, c := range comments {
		if text, _ := c.(map[string]any)["text"].(string); strings.Contains(text, "F-3") && strings.Contains(text, "F-8") {
			gefunden = true
		}
	}
	if !gefunden {
		t.Errorf("kein Kommentar mit alter (F-3) und neuer Nummer (F-8): %v", comments)
	}
	// … und steht im Activity-Eintrag.
	act := moved["activity"].([]any)
	last := act[len(act)-1].(map[string]any)
	if v, _ := last["from"].(map[string]any)["findingNr"].(float64); v != 3 {
		t.Errorf("activity.from.findingNr = %v, erwartet 3", v)
	}
	if v, _ := last["to"].(map[string]any)["findingNr"].(float64); v != 8 {
		t.Errorf("activity.to.findingNr = %v, erwartet 8", v)
	}
}

func TestTransferCard_FindingZaehlerFehltImZiel(t *testing.T) {
	// Ziel ohne nextFindingNr (älterer Stand): Nummer = max(nr) + 1, nicht 1
	// und nicht die übernommene Quellnummer 5 (die im Ziel schon vergeben ist).
	src := boardMitKarten(float64(6), findingKarte("f-src", 1, 5))
	dst := boardMitKarten(nil, findingKarte("f-a", 2, 5))
	repo := transferFindingRepo(t, src, dst)

	res, err := repo.TransferCard(context.Background(), TransferCardParams{
		SrcBoardID: "src", DstBoardID: "dst", CardID: "f-src", DstColumnID: "col-backlog", Actor: "x",
	})
	if err != nil {
		t.Fatalf("TransferCard: %v", err)
	}
	if res.NewFindingNr != 6 {
		t.Errorf("NewFindingNr = %d, erwartet 6", res.NewFindingNr)
	}
	if v, _ := dstBoard(t, repo)["nextFindingNr"].(float64); v != 7 {
		t.Errorf("dst.nextFindingNr = %v, erwartet 7", v)
	}
}

func TestTransferCard_NormaleKarteOhneFindingNummer(t *testing.T) {
	repo, cardID := newTransferTestRepo(t)
	res, err := repo.TransferCard(context.Background(), TransferCardParams{
		SrcBoardID: "src", DstBoardID: "dst", CardID: cardID, DstColumnID: "col-backlog", Actor: "x",
	})
	if err != nil {
		t.Fatalf("TransferCard: %v", err)
	}
	if res.NewFindingNr != 0 || res.OldFindingNr != 0 {
		t.Errorf("Finding-Nummern bei normaler Karte: %d / %d, erwartet 0 / 0", res.NewFindingNr, res.OldFindingNr)
	}
	if _, da := dstBoard(t, repo)["nextFindingNr"]; da {
		t.Error("normale Karte darf dst.nextFindingNr nicht anlegen")
	}
}
