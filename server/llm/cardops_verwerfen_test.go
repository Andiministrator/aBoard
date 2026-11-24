package llm

// ADR-0136 Nachtrag (Karte #693): llm-discard entfernt nur KI-Antworten der
// Person, die verwirft — sonst ginge jeder Kommentar über diesen Weg am
// Löschrecht vorbei.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func setzeKommentareFuerVerwerfen(t *testing.T, kommentare string) *storage.BoardRepo {
	t.Helper()
	repo := newCardOpsTestRepo(t)
	ctx := context.Background()
	state := `{"version":"2","board":{"id":"b1","title":"T","sprints":[{"id":"s1","title":"S","cards":[{"id":"c1","displayId":1,"title":"Karte","comments":` +
		kommentare + `,"activity":[]}]}]}}`
	if _, err := repo.Insert(ctx, "b1", "T", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestDiscardLLMReply_NurEigeneKIAntwort(t *testing.T) {
	kommentare := `[` +
		`{"id":"mensch","author":"Anna","authorEmail":"anna@x.de","text":"von Anna"},` +
		`{"id":"ki-anna","author":"🤖 a/m (im Auftrag von anna@x.de)","authorEmail":"anna@x.de","text":"KI"},` +
		`{"id":"ki-alt","author":"🤖 a/m (im Auftrag von anna@x.de)","text":"alte KI"}]`
	for _, f := range []struct {
		name, comment, wer string
		erlaubt            bool
	}{
		{"Menschen-Kommentar, eigene E-Mail", "mensch", "anna@x.de", false},
		{"Menschen-Kommentar, fremd", "mensch", "bert@x.de", false},
		{"eigene KI-Antwort", "ki-anna", "anna@x.de", true},
		{"fremde KI-Antwort", "ki-anna", "bert@x.de", false},
		{"alte KI-Antwort über das Label", "ki-alt", "anna@x.de", true},
		{"alte KI-Antwort, fremd", "ki-alt", "bert@x.de", false},
	} {
		t.Run(f.name, func(t *testing.T) {
			r := setzeKommentareFuerVerwerfen(t, kommentare)
			err := DiscardLLMReply(context.Background(), r, DiscardLLMReplyParams{
				BoardID: "b1", CardID: "c1", CommentID: f.comment, ActivityTS: "2026-10-04T00:00:00Z", ActivityActor: f.wer,
			})
			if f.erlaubt && err != nil {
				t.Fatalf("erwartet erlaubt: %v", err)
			}
			if !f.erlaubt && !errors.Is(err, ErrLLMCommentNotFound) {
				t.Fatalf("erwartet ErrLLMCommentNotFound, bekam %v", err)
			}
			b, _ := r.Get(context.Background(), "b1")
			var s struct {
				Board struct {
					Sprints []struct {
						Cards []struct {
							Comments []map[string]any `json:"comments"`
						} `json:"cards"`
					} `json:"sprints"`
				} `json:"board"`
			}
			_ = json.Unmarshal(b.StateJSON, &s)
			da := false
			for _, k := range s.Board.Sprints[0].Cards[0].Comments {
				if k["id"] == f.comment {
					da = true
				}
			}
			if da == f.erlaubt {
				t.Errorf("Kommentar %s noch da: %v (erlaubt: %v)", f.comment, da, f.erlaubt)
			}
		})
	}
}
