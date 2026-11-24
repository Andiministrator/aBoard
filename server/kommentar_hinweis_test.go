package main

// F-1614 (Karte #693): Die Meldung „dein Kommentar wurde gelöscht“ geht nur an
// einen Autor, der am Board noch eine Rolle hat (viewer reicht).

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/notifications"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestKommentarLoeschHinweis_NurMitRolleAmBoard(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	users := storage.NewUserRepo(db)
	if _, err := boards.Insert(ctx, "b1", "Board", "2", []byte(`{"version":"2","board":{"id":"b1","title":"Board","sprints":[]}}`)); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][]any{{"u-anna", "anna@firma.de"}, {"u-ex", "ex@firma.de"}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', 0)`, u...); err != nil {
			t.Fatal(err)
		}
	}
	if err := acl.Set(ctx, "b1", "anna@firma.de", "viewer"); err != nil {
		t.Fatal(err)
	}
	var gesendet []notifications.Event
	hinweis := kommentarLoeschHinweis(users, boards, acl, func(e notifications.Event) { gesendet = append(gesendet, e) })

	g := api.KommentarGeloescht{BoardID: "b1", CardID: "k1", CommentID: "c1", Loescher: "olga@firma.de", Textanfang: "x"}
	g.AutorEmail = "ex@firma.de" // Konto ja, Rolle am Board nein
	hinweis(ctx, g)
	if len(gesendet) != 0 {
		t.Fatalf("Meldung an Autor ohne Rolle: %+v", gesendet)
	}
	g.AutorEmail = "anna@firma.de" // viewer
	hinweis(ctx, g)
	if len(gesendet) != 1 || gesendet[0].ExplicitRecipientUserIDs[0] != "u-anna" || gesendet[0].BoardName != "Board" {
		t.Fatalf("Meldung an viewer: %+v", gesendet)
	}
}
