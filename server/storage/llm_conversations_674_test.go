package storage

// llm_conversations_674_test.go — Karte #674 (ADR-0133 Nachtrag 2 A5):
// ListConversationsForCardBy filtert in der Abfrage nach dem Ersteller.

import (
	"context"
	"testing"
)

func TestLLMConv_ListBy_NurErsteller(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()
	for _, p := range []struct{ id, wer string }{{"k-a", "a@x.de"}, {"k-b", "b@x.de"}} {
		if _, err := repo.CreateConversation(ctx, CreateConversationParams{
			ID: p.id, BoardID: "b1", CardID: "c1", Provider: "openai", Model: "m", CreatedBy: p.wer,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Gross-/Kleinschreibung und Leerraum im Aufruf spielen keine Rolle.
	list, err := repo.ListConversationsForCardBy(ctx, "b1", "c1", " A@X.de ", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "k-a" {
		t.Errorf("ListBy(a) = %+v, erwartet nur k-a", list)
	}
	// Leerer Ersteller hiesse „alle“ — das muss ein Fehler sein.
	if _, err := repo.ListConversationsForCardBy(ctx, "b1", "c1", "  ", false); err == nil {
		t.Error("ListBy mit leerem Ersteller ohne Fehler")
	}
	// Die bisherige Liste bleibt ungefiltert.
	if all, _ := repo.ListConversationsForCard(ctx, "b1", "c1", false); len(all) != 2 {
		t.Errorf("ListConversationsForCard = %d Einträge, erwartet 2", len(all))
	}
}
