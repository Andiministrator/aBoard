package mail

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func newCardOpsTestBoards(t *testing.T) (*storage.BoardRepo, func()) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := storage.NewBoardRepo(db)
	return repo, func() { db.Close() }
}

// seedServiceBoardForTest legt das Service-Board kanonisch an.
func seedServiceBoardForTest(t *testing.T, repo *storage.BoardRepo) {
	t.Helper()
	ctx := context.Background()
	state := storage.CanonicalServiceBoardState(storage.ServiceBoardID, "Service")
	if _, err := repo.Insert(ctx, storage.ServiceBoardID, "Service", "2", state); err != nil {
		t.Fatalf("seedService: %v", err)
	}
}

func TestSanitizeTitle(t *testing.T) {
	cases := []struct{ in, out string }{
		{"  Mein Subject  ", "Mein Subject"},
		{"a\nb\tc", "a b c"},
		{"", "(kein Betreff)"},
	}
	for _, c := range cases {
		got := SanitizeTitle(c.in)
		if got != c.out {
			t.Errorf("SanitizeTitle(%q) = %q, want %q", c.in, got, c.out)
		}
	}
	// Truncate-Verhalten
	long := strings.Repeat("x", 250)
	got := SanitizeTitle(long)
	if len([]rune(got)) > 200 {
		t.Errorf("SanitizeTitle Truncate: %d Zeichen, max 200", len([]rune(got)))
	}
}

func TestCreateCardInServiceBoard(t *testing.T) {
	repo, done := newCardOpsTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	ctx := context.Background()

	cardID, displayID, err := CreateCardInServiceBoard(ctx, repo, NewServiceCard{
		Title:       "Test-Mail",
		Description: "Body-Inhalt",
		SenderEmail: "user@example.org",
		MessageID:   "<msg-1@example.org>",
	})
	if err != nil {
		t.Fatalf("CreateCardInServiceBoard: %v", err)
	}
	_ = displayID // wird weiter unten via board-State geprüft
	if cardID == "" {
		t.Fatalf("cardID leer")
	}

	// Re-Read und prüfen
	b, _ := repo.Get(ctx, storage.ServiceBoardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	boardObj := state["board"].(map[string]any)
	sprints := boardObj["sprints"].([]any)
	sp := sprints[0].(map[string]any)
	cards := sp["cards"].([]any)
	if len(cards) != 1 {
		t.Fatalf("erwartet 1 Karte, bekam %d", len(cards))
	}
	card := cards[0].(map[string]any)
	if card["id"].(string) != cardID {
		t.Errorf("card.id mismatch")
	}
	if card["columnId"].(string) != "col-inbox" {
		t.Errorf("columnId = %v, erwartet col-inbox", card["columnId"])
	}
	if card["senderEmail"].(string) != "user@example.org" {
		t.Errorf("senderEmail = %v", card["senderEmail"])
	}
	if card["messageId"].(string) != "<msg-1@example.org>" {
		t.Errorf("messageId = %v", card["messageId"])
	}
	if card["displayId"].(float64) != 1 {
		t.Errorf("displayId = %v, erwartet 1", card["displayId"])
	}
	if next := boardObj["nextDisplayId"].(float64); next != 2 {
		t.Errorf("nextDisplayId = %v, erwartet 2", next)
	}
}

func TestCreateCardInServiceBoard_Spam(t *testing.T) {
	repo, done := newCardOpsTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	ctx := context.Background()

	_, _, err := CreateCardInServiceBoard(ctx, repo, NewServiceCard{
		Title:       "Spammy",
		Description: "viagra",
		SenderEmail: "spammer@x.io",
		IsSpam:      true,
	})
	if err != nil {
		t.Fatalf("CreateCardInServiceBoard: %v", err)
	}

	b, _ := repo.Get(ctx, storage.ServiceBoardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	cards := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)
	if cards[0].(map[string]any)["columnId"].(string) != "col-spam" {
		t.Errorf("Spam-Karte landete nicht in col-spam")
	}
}

func TestAddCommentToCard(t *testing.T) {
	repo, done := newCardOpsTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	ctx := context.Background()

	// Erst eine Karte anlegen, dann Comment dranhängen
	cardID, _, _ := CreateCardInServiceBoard(ctx, repo, NewServiceCard{
		Title:       "Ticket",
		SenderEmail: "u@x",
	})
	loc := CardLocation{
		BoardID:   storage.ServiceBoardID,
		SprintID:  "__service__",
		CardID:    cardID,
		DisplayID: 1,
	}

	err := AddCommentToCard(ctx, repo, loc, "cm-1", "andi@x", "Mein Kommentar", "2026-05-21T12:00:00Z", nil)
	if err != nil {
		t.Fatalf("AddCommentToCard: %v", err)
	}

	b, _ := repo.Get(ctx, storage.ServiceBoardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	cards := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)
	comments := cards[0].(map[string]any)["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("erwartet 1 Kommentar, bekam %d", len(comments))
	}
	cm := comments[0].(map[string]any)
	if cm["id"].(string) != "cm-1" {
		t.Errorf("comment.id mismatch: %v", cm["id"])
	}
	if cm["author"].(string) != "📬 andi@x" {
		t.Errorf("author = %v", cm["author"])
	}
	if cm["text"].(string) != "Mein Kommentar" {
		t.Errorf("text = %v", cm["text"])
	}
}

func TestAddCommentToCard_IdempotentSameID(t *testing.T) {
	repo, done := newCardOpsTestBoards(t)
	defer done()
	seedServiceBoardForTest(t, repo)
	ctx := context.Background()
	cardID, _, _ := CreateCardInServiceBoard(ctx, repo, NewServiceCard{Title: "T", SenderEmail: "u@x"})
	loc := CardLocation{BoardID: storage.ServiceBoardID, CardID: cardID}

	_ = AddCommentToCard(ctx, repo, loc, "fixed-id", "a@x", "body1", "", nil)
	_ = AddCommentToCard(ctx, repo, loc, "fixed-id", "a@x", "body2", "", nil)

	b, _ := repo.Get(ctx, storage.ServiceBoardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	cards := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)
	comments := cards[0].(map[string]any)["comments"].([]any)
	if len(comments) != 1 {
		t.Errorf("Idempotent-Check: erwartet 1 Kommentar, bekam %d", len(comments))
	}
}

func TestDeriveCommentIDFromMessageID_Stable(t *testing.T) {
	a := DeriveCommentIDFromMessageID("<abc@example.org>")
	b := DeriveCommentIDFromMessageID("<abc@example.org>")
	if a != b {
		t.Errorf("derive instabil: %q vs %q", a, b)
	}
	c := DeriveCommentIDFromMessageID("<other@example.org>")
	if a == c {
		t.Errorf("derive kollidiert für unterschiedliche IDs")
	}
	if DeriveCommentIDFromMessageID("") != "" {
		t.Errorf("leere Message-ID sollte leere Comment-ID liefern")
	}
}
