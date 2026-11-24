package storage

// schleuse_transfer_test.go — Kartentransfer und Ausgangsschleuse (Karte
// #686, Kritiker KS3-1/KS3-2): Die Aufträge wandern mit, ein Kommentar
// „extern“ ohne Auftrag verliert seine Kennzeichnung.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTransferCard_SchleuseAuftraegeUndExternKommentare(t *testing.T) {
	repo, cardID := newTransferTestRepo(t)
	ctx := context.Background()
	// Zwei Kommentare „extern“: c-echt hat einen Auftrag (vom Server
	// geschrieben), c-falsch nicht (eingeschleust).
	var st string
	if err := repo.db.QueryRow(`SELECT state_json FROM boards WHERE id = 'src'`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal([]byte(st), &root)
	karte := root["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	karte["comments"] = []any{
		map[string]any{"id": "c-echt", "author": "🌐 a/m", "authorEmail": "anna@x", "text": "echt", "kind": "extern", "schleuse": map[string]any{"auftragId": "auf-1"}},
		map[string]any{"id": "c-falsch", "author": "🌐 a/m", "authorEmail": "bert@x", "text": "falsch", "kind": "extern", "schleuse": map[string]any{"auftragId": "erfunden"}},
	}
	neu, _ := json.Marshal(root)
	mustExec(t, repo.db, `UPDATE boards SET state_json = ? WHERE id = 'src'`, string(neu))
	mustExec(t, repo.db, `INSERT INTO schleuse_auftraege (id, board_id, card_id, user_id, user_email, freigabe_id, anbieter, modell, zustand, text, text_sha256, freigabe_sha256, freigegeben_von, freigegeben_am, gesendet_am, kommentar_id, erstellt_am, geaendert_am)
		VALUES ('auf-1', 'src', ?, 'u-a', 'anna@x', 'fg', 'anthropic', 'm', 'beantwortet', 't', 'h', 'h', 'anna@x', 'x', 'x', 'c-echt', 'x', 'x')`, cardID)

	if _, err := repo.TransferCard(ctx, TransferCardParams{SrcBoardID: "src", DstBoardID: "dst", CardID: cardID, DstColumnID: "col-in-arbeit", Actor: "anna@x"}); err != nil {
		t.Fatalf("TransferCard: %v", err)
	}
	var board string
	_ = repo.db.QueryRow(`SELECT board_id FROM schleuse_auftraege WHERE id = 'auf-1'`).Scan(&board)
	if board != "dst" {
		t.Errorf("Auftrag nicht mitgewandert: %q", board)
	}
	_ = repo.db.QueryRow(`SELECT state_json FROM boards WHERE id = 'dst'`).Scan(&st)
	_ = json.Unmarshal([]byte(st), &root)
	var ks []any
	for _, sp := range root["board"].(map[string]any)["sprints"].([]any) {
		for _, k := range sp.(map[string]any)["cards"].([]any) {
			if km := k.(map[string]any); km["id"] == cardID {
				ks, _ = km["comments"].([]any)
			}
		}
	}
	art := map[string]any{}
	for _, k := range ks {
		km := k.(map[string]any)
		art[km["id"].(string)] = km["kind"]
		if km["id"] == "c-falsch" && km["schleuse"] != nil {
			t.Errorf("eingeschleuster Kommentar behält schleuse: %v", km)
		}
	}
	if art["c-echt"] != "extern" || art["c-falsch"] != nil {
		t.Errorf("Kennzeichnung nach Transfer: %v", art)
	}
}

// Sitzungs-Kritiker SK-2: Läuft für die Karte ein Versand (Zustand
// „gesendet“, kommentar_id noch leer), kann ein Kommentar „extern“ ohne
// Treffer genau dessen Antwort sein — der Transfer entkennzeichnet nichts.
func TestTransferCard_LaufenderVersandEntkennzeichnetNicht(t *testing.T) {
	repo, cardID := newTransferTestRepo(t)
	var st string
	_ = repo.db.QueryRow(`SELECT state_json FROM boards WHERE id = 'src'`).Scan(&st)
	var root map[string]any
	_ = json.Unmarshal([]byte(st), &root)
	karte := root["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	karte["comments"] = []any{map[string]any{"id": "c-neu", "author": "a", "authorEmail": "anna@x", "text": "t", "kind": "extern", "schleuse": map[string]any{"auftragId": "auf-2"}}}
	neu, _ := json.Marshal(root)
	mustExec(t, repo.db, `UPDATE boards SET state_json = ? WHERE id = 'src'`, string(neu))
	mustExec(t, repo.db, `INSERT INTO schleuse_auftraege (id, board_id, card_id, user_id, user_email, freigabe_id, anbieter, modell, zustand, text, text_sha256, freigabe_sha256, freigegeben_von, freigegeben_am, gesendet_am, erstellt_am, geaendert_am)
		VALUES ('auf-2', 'src', ?, 'u-a', 'anna@x', 'fg', 'anthropic', 'm', 'gesendet', 't', 'h', 'h', 'anna@x', 'x', 'x', 'x', 'x')`, cardID)
	if _, err := repo.TransferCard(context.Background(), TransferCardParams{SrcBoardID: "src", DstBoardID: "dst", CardID: cardID, DstColumnID: "col-in-arbeit", Actor: "anna@x"}); err != nil {
		t.Fatal(err)
	}
	_ = repo.db.QueryRow(`SELECT state_json FROM boards WHERE id = 'dst'`).Scan(&st)
	if !strings.Contains(st, `"kind":"extern"`) {
		t.Errorf("Antwort eines laufenden Versands entkennzeichnet")
	}
}
