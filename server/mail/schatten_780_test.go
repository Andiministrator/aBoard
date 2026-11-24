package mail

// Karte #780 (ADR-0140 §1, B-3): Ein Mail-Kommentar an einer Karte schreibt
// den Board-Zustand, nie die Schatten.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten/schattentest"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestSchatten780_MailKommentar(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := storage.NewBoardRepo(db)
	state := `{"version":"2","board":{"id":"b1","title":"T","sprints":[{"id":"s1","title":"S","cards":[{"id":"c1","displayId":1,"title":"Karte","comments":[],"activity":[]}]}]}}`
	if _, err := repo.Insert(ctx, "b1", "T", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	schattentest.Bestuecke(t, db, "b1")
	schattentest.MussBestueckt(t, db, "b1")
	vorher := schattentest.Momentaufnahme(t, db)

	loc := CardLocation{BoardID: "b1", SprintID: "s1", CardID: "c1", DisplayID: 1}
	if err := AddCommentToCard(ctx, repo, loc, "cm-780", "anna@example.com", "Mail 780", "2026-10-10T12:00:00Z", nil); err != nil {
		t.Fatalf("AddCommentToCard: %v", err)
	}
	if b, _ := repo.Get(ctx, "b1"); !strings.Contains(string(b.StateJSON), "Mail 780") {
		t.Fatal("AddCommentToCard hat nicht geschrieben")
	}
	schattentest.Unveraendert(t, db, vorher, "Mail-Kommentar")
}
