package llm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func newCardOpsTestRepo(t *testing.T) *storage.BoardRepo {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return storage.NewBoardRepo(db)
}

func seedBoardWithCard(t *testing.T, repo *storage.BoardRepo, boardID, cardID string) {
	t.Helper()
	ctx := context.Background()
	state := `{"version":"2","board":{"id":"` + boardID + `","title":"T","sprints":[{"id":"s1","title":"S","cards":[{"id":"` + cardID + `","displayId":1,"title":"Karte","description":"x","comments":[],"activity":[]}]}]}}`
	if _, err := repo.Insert(ctx, boardID, "T", "2", []byte(state)); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestAddLLMReply_AppendsCommentAndActivity(t *testing.T) {
	repo := newCardOpsTestRepo(t)
	seedBoardWithCard(t, repo, "b1", "c1")
	ctx := context.Background()

	err := AddLLMReply(ctx, repo, LLMReplyParams{
		BoardID:         "b1",
		CardID:          "c1",
		CommentID:       "cm-A",
		Author:          "🤖 anthropic/claude-opus-4-7 (im Auftrag von alice@x)",
		Body:            "## Antwort\n\nIch denke...",
		Created:         "2026-05-23T12:00:00Z",
		ActivityTS:      "2026-05-23T12:00:00Z",
		ActivityActor:   "alice@x.de",
		Provider:        "anthropic",
		Model:           "claude-opus-4-7",
		InputTokens:     1234,
		OutputTokens:    567,
		CostEstimateUSD: 0.063,
		ContextNote:     "Beschreibung + 3 Kommentare",
	})
	if err != nil {
		t.Fatalf("AddLLMReply: %v", err)
	}

	b, _ := repo.Get(ctx, "b1")
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	cards := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)
	card := cards[0].(map[string]any)
	comments := card["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("Comments: %d, erwartet 1", len(comments))
	}
	cm := comments[0].(map[string]any)
	if cm["id"].(string) != "cm-A" {
		t.Errorf("comment.id falsch: %v", cm["id"])
	}
	if cm["text"].(string) != "## Antwort\n\nIch denke..." {
		t.Errorf("comment.text falsch: %v", cm["text"])
	}
	// ADR-0136 §1: Die Antwort gehört der Person, die gefragt hat.
	if cm["authorEmail"] != "alice@x.de" {
		t.Errorf("comment.authorEmail: %v, erwartet alice@x.de", cm["authorEmail"])
	}

	activity := card["activity"].([]any)
	if len(activity) != 1 {
		t.Fatalf("Activity: %d, erwartet 1", len(activity))
	}
	a := activity[0].(map[string]any)
	if a["action"].(string) != "llm-sent" {
		t.Errorf("action: %v", a["action"])
	}
	if a["to"].(string) != "anthropic/claude-opus-4-7" {
		t.Errorf("to: %v", a["to"])
	}
	meta := a["meta"].(map[string]any)
	if meta["commentId"].(string) != "cm-A" {
		t.Errorf("meta.commentId: %v", meta["commentId"])
	}
	if int(meta["inputTokens"].(float64)) != 1234 {
		t.Errorf("meta.inputTokens: %v", meta["inputTokens"])
	}
	if int(meta["outputTokens"].(float64)) != 567 {
		t.Errorf("meta.outputTokens: %v", meta["outputTokens"])
	}
	if meta["costEstimateUSD"].(float64) != 0.063 {
		t.Errorf("meta.costEstimateUSD: %v", meta["costEstimateUSD"])
	}
	if meta["contextNote"].(string) != "Beschreibung + 3 Kommentare" {
		t.Errorf("meta.contextNote: %v", meta["contextNote"])
	}
}

func TestAddLLMReply_IdempotentSameCommentID(t *testing.T) {
	repo := newCardOpsTestRepo(t)
	seedBoardWithCard(t, repo, "b1", "c1")
	ctx := context.Background()
	p := LLMReplyParams{
		BoardID: "b1", CardID: "c1", CommentID: "fixed",
		Author: "🤖", Body: "x", Created: "2026-05-23T12:00:00Z",
		ActivityTS: "2026-05-23T12:00:00Z", ActivityActor: "a@x.de",
		Provider: "openai", Model: "gpt-4o",
	}
	if err := AddLLMReply(ctx, repo, p); err != nil {
		t.Fatalf("erster Aufruf: %v", err)
	}
	// Zweiter Aufruf — identische CommentID → no-op (kein Doppel-Comment, kein Doppel-Activity)
	p.Body = "geänderter Inhalt" // sollte ignoriert werden
	if err := AddLLMReply(ctx, repo, p); err != nil {
		t.Fatalf("zweiter Aufruf: %v", err)
	}
	b, _ := repo.Get(ctx, "b1")
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	card := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	if len(card["comments"].([]any)) != 1 {
		t.Errorf("comments doubled bei identischer ID: %d", len(card["comments"].([]any)))
	}
	if len(card["activity"].([]any)) != 1 {
		t.Errorf("activity doubled bei identischer ID: %d", len(card["activity"].([]any)))
	}
	// Body darf nicht überschrieben sein
	if cm := card["comments"].([]any)[0].(map[string]any); cm["text"].(string) != "x" {
		t.Errorf("idempotent: body wurde überschrieben: %v", cm["text"])
	}
}

func TestAddLLMReply_UnknownCard_Error(t *testing.T) {
	repo := newCardOpsTestRepo(t)
	seedBoardWithCard(t, repo, "b1", "c1")
	ctx := context.Background()
	err := AddLLMReply(ctx, repo, LLMReplyParams{
		BoardID: "b1", CardID: "nonexistent", CommentID: "cm-1",
		Author: "x", Body: "y", Provider: "openai", Model: "gpt-4o",
	})
	if err == nil {
		t.Fatalf("erwartet Fehler bei unbekannter Karte")
	}
}

func TestAddLLMReply_MissingIDs(t *testing.T) {
	repo := newCardOpsTestRepo(t)
	ctx := context.Background()
	cases := []LLMReplyParams{
		{BoardID: "", CardID: "c1", CommentID: "cm"},
		{BoardID: "b1", CardID: "", CommentID: "cm"},
		{BoardID: "b1", CardID: "c1", CommentID: ""},
	}
	for i, p := range cases {
		if err := AddLLMReply(ctx, repo, p); err == nil {
			t.Errorf("case %d: erwartet Fehler bei fehlender ID, params=%+v", i, p)
		}
	}
}

// N3 aus dem Verify-Lauf: Der Audit-Kanal für mitgeschickte Anhänge
// (ADR-0121 §6) war implementiert, aber von keinem Test berührt. Geprüft
// wird die Eigenschaft, auf die es ankommt: die **id** landet im meta, nicht
// nur der Dateiname — der steht im Karten-Zustand, ist nicht eindeutig und
// jederzeit änderbar.
func TestAddLLMReplyAuditFelder(t *testing.T) {
	repo := newCardOpsTestRepo(t)
	seedBoardWithCard(t, repo, "b1", "c1")
	ctx := context.Background()
	boardID, cardID := "b1", "c1"

	err := AddLLMReply(ctx, repo, LLMReplyParams{
		BoardID: boardID, CardID: cardID,
		CommentID: "cm-audit", Author: "🤖 test", Body: "Antwort", Created: "2026-08-28",
		ActivityTS: "2026-08-28T10:00:00Z", ActivityActor: "alice@example.com",
		Provider: "anthropic", Model: "claude-opus-4-7",
		FetchedCards: []string{"#2"},
		Attachments:  []string{"att-1 (image/png, 3 B)"},
	})
	if err != nil {
		t.Fatalf("AddLLMReply: %v", err)
	}

	meta := letztesActivityMetaFuerTest(t, repo, boardID, cardID)
	anh, _ := meta["attachments"].([]any)
	if len(anh) != 1 || anh[0] != "att-1 (image/png, 3 B)" {
		t.Fatalf("meta.attachments = %+v, erwartet den id-basierten Eintrag", meta["attachments"])
	}
	geladen, _ := meta["fetchedCards"].([]any)
	if len(geladen) != 1 || geladen[0] != "#2" {
		t.Fatalf("meta.fetchedCards = %+v", meta["fetchedCards"])
	}
}

// Ohne Anhänge und ohne Abrufe bleibt der meta-Block frei von diesen Feldern
// — eine Null in jedem Eintrag ist das Rauschen, das dazu führt, dass
// niemand mehr hinsieht.
func TestAddLLMReplyOhneAuditFelder(t *testing.T) {
	repo := newCardOpsTestRepo(t)
	seedBoardWithCard(t, repo, "b1", "c1")
	ctx := context.Background()
	boardID, cardID := "b1", "c1"

	err := AddLLMReply(ctx, repo, LLMReplyParams{
		BoardID: boardID, CardID: cardID,
		CommentID: "cm-leer", Author: "🤖 test", Body: "Antwort", Created: "2026-08-28",
		ActivityTS: "2026-08-28T10:00:00Z", ActivityActor: "alice@example.com",
		Provider: "anthropic", Model: "claude-opus-4-7",
	})
	if err != nil {
		t.Fatalf("AddLLMReply: %v", err)
	}
	meta := letztesActivityMetaFuerTest(t, repo, boardID, cardID)
	for _, feld := range []string{"attachments", "fetchedCards", "fetchesRejected"} {
		if _, da := meta[feld]; da {
			t.Fatalf("meta.%s steht im Eintrag, obwohl nichts vorlag: %+v", feld, meta)
		}
	}
}

// letztesActivityMetaFuerTest liest den meta-Block des jüngsten
// Aktivitäts-Eintrags der Karte aus dem persistierten state_json.
func letztesActivityMetaFuerTest(t *testing.T, repo *storage.BoardRepo, boardID, cardID string) map[string]any {
	t.Helper()
	b, err := repo.Get(context.Background(), boardID)
	if err != nil {
		t.Fatalf("board lesen: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b.StateJSON, &root); err != nil {
		t.Fatalf("state parsen: %v", err)
	}
	sprints := root["board"].(map[string]any)["sprints"].([]any)
	for _, sp := range sprints {
		for _, c := range sp.(map[string]any)["cards"].([]any) {
			cm := c.(map[string]any)
			if cm["id"] != cardID {
				continue
			}
			act, _ := cm["activity"].([]any)
			if len(act) == 0 {
				t.Fatal("kein Aktivitaets-Eintrag an der Karte")
			}
			meta, _ := act[len(act)-1].(map[string]any)["meta"].(map[string]any)
			if meta == nil {
				t.Fatal("Aktivitaets-Eintrag ohne meta")
			}
			return meta
		}
	}
	t.Fatalf("Karte %s nicht gefunden", cardID)
	return nil
}
