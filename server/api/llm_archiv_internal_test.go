package api

// llm_archiv_internal_test.go — F-859 (Karte #488): Was der Nutzer im Board
// weggeräumt hat, geht nicht unaufgefordert an ein fremdes Modell.
//
// Die Prüfung sitzt in loadCardForLLM und ist eine Unterscheidung zwischen
// zwei Indizes, nicht ein Filter: Die GEÖFFNETE Karte muss erreichbar
// bleiben, auch wenn sie archiviert ist — ihr den KI-Tab zu verweigern wäre
// eine Regression ohne Anlass. Der NACHLADE-Snapshot dagegen führt sie nicht.
//
// Beide Richtungen werden geprüft. Ein Test, der nur zeigt, dass archivierte
// Karten fehlen, bliebe auch dann grün, wenn gar nichts mehr indiziert wird.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// archivBoard baut ein Board mit zwei Swimlanes: eine offene und eine
// archivierte. `c1` ist die Quellkarte und verweist auf alle anderen.
func archivBoard(t *testing.T) (*storage.BoardRepo, string) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	k := func(id string, archiviert bool, ziele ...string) map[string]any {
		links := make([]any, 0, len(ziele))
		for _, z := range ziele {
			links = append(links, map[string]any{"type": "related", "targetId": z})
		}
		return map[string]any{
			"id": id, "displayId": float64(1), "title": "Karte " + id,
			"description": "Inhalt von " + id, "links": links, "archived": archiviert,
		}
	}
	state := map[string]any{
		"version": "2",
		"board": map[string]any{
			"id": "b1",
			"sprints": []any{
				map[string]any{
					"id": "s-offen", "title": "Offen", "kind": "sprint", "archived": false,
					"cards": []any{
						k("c1", false, "c2", "c3", "c4"), // Quellkarte
						k("c2", false),                   // normal
						k("c3", true),                    // Karte archiviert
					},
				},
				map[string]any{
					"id": "s-archiviert", "title": "Weggeräumt", "kind": "sprint", "archived": true,
					"cards": []any{
						k("c4", false), // Karte NICHT archiviert — aber ihre Lane
						k("c5", false),
					},
				},
			},
		},
	}
	roh, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("state bauen: %v", err)
	}
	boards := storage.NewBoardRepo(db)
	if _, err := boards.Insert(ctx, "b1", "b1", "2", roh); err != nil {
		t.Fatalf("board anlegen: %v", err)
	}
	return boards, "b1"
}

func TestNachladeSnapshotLaesstArchiviertesAus(t *testing.T) {
	boards, boardID := archivBoard(t)
	r := httptest.NewRequest("GET", "/", nil)

	ctx, err := loadCardForLLM(r, boards, boardID, "c1")
	if err != nil {
		t.Fatalf("loadCardForLLM: %v", err)
	}

	if _, da := ctx.CardsByID["c2"]; !da {
		t.Error("die nicht archivierte Karte c2 fehlt im Snapshot — dann filtert der Index zu viel")
	}
	if _, da := ctx.CardsByID["c3"]; da {
		t.Error("c3 ist archiviert und steht trotzdem im Nachlade-Snapshot")
	}
	// ADR-0117: Das Swimlane-Flag kaskadiert bewusst NICHT auf die Karten —
	// c4 trägt `archived: false`. Für die Frage „hat der Nutzer das
	// weggeräumt?" zählt die Lane trotzdem, denn sie verbirgt alles darin.
	if _, da := ctx.CardsByID["c4"]; da {
		t.Error("c4 liegt in einer archivierten Swimlane und steht trotzdem im Nachlade-Snapshot")
	}
	if _, da := ctx.CardsByID["c5"]; da {
		t.Error("c5 liegt in einer archivierten Swimlane und steht trotzdem im Nachlade-Snapshot")
	}
}

func TestRefsZeigenNichtAufArchivierteKarten(t *testing.T) {
	boards, boardID := archivBoard(t)
	r := httptest.NewRequest("GET", "/", nil)

	ctx, err := loadCardForLLM(r, boards, boardID, "c1")
	if err != nil {
		t.Fatalf("loadCardForLLM: %v", err)
	}

	// c1 verweist auf c2, c3 und c4 — nur c2 darf als Ref auftauchen.
	if _, da := ctx.RefsByID["c2"]; !da {
		t.Error("die Ref auf die offene Karte c2 fehlt")
	}
	for _, id := range []string{"c3", "c4"} {
		if _, da := ctx.RefsByID[id]; da {
			t.Errorf("Ref auf %s aufgelöst, obwohl archiviert (bzw. in archivierter Lane)", id)
		}
	}
}

func TestArchivierteKarteBleibtSelbstBearbeitbar(t *testing.T) {
	// Die Gegenrichtung, und die wichtigere Hälfte: Wer eine archivierte
	// Karte öffnet, hat sie ausdrücklich im Blick. Ihr den KI-Tab zu
	// verweigern wäre eine Regression, die dieser Fix nicht einführen darf.
	boards, boardID := archivBoard(t)
	r := httptest.NewRequest("GET", "/", nil)

	for _, id := range []string{"c3", "c4", "c5"} {
		ctx, err := loadCardForLLM(r, boards, boardID, id)
		if err != nil {
			t.Fatalf("loadCardForLLM(%s) scheitert, obwohl die Karte geöffnet ist: %v", id, err)
		}
		if got, _ := ctx.Card["id"].(string); got != id {
			t.Errorf("loadCardForLLM(%s) liefert Karte %q", id, got)
		}
	}
}
