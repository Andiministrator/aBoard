package llm

// Karte #780 (ADR-0140 §1, B-3): Die LLM-Antwort (und ihr Verwerfen) schreibt
// den Board-Zustand, nie die Schatten.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten/schattentest"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestSchatten780_LLMAntwort(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := storage.NewBoardRepo(db)
	seedBoardWithCard(t, repo, "b1", "c1")
	schattentest.Bestuecke(t, db, "b1")
	schattentest.MussBestueckt(t, db, "b1")
	vorher := schattentest.Momentaufnahme(t, db)

	if err := AddLLMReply(ctx, repo, LLMReplyParams{BoardID: "b1", CardID: "c1", CommentID: "cm-780",
		Author: "🤖 test", Body: "Antwort 780", Created: "2026-10-10T12:00:00Z",
		ActivityTS: "2026-10-10T12:00:00Z", ActivityActor: "anna@example.com"}); err != nil {
		t.Fatalf("AddLLMReply: %v", err)
	}
	if b, _ := repo.Get(ctx, "b1"); !strings.Contains(string(b.StateJSON), "Antwort 780") {
		t.Fatal("AddLLMReply hat nicht geschrieben")
	}
	schattentest.Unveraendert(t, db, vorher, "AddLLMReply")
}
