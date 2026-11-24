package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// TestAgentHandler_IdentitaetsKopfzeilenEntfernt (F-2102): Am Agenten-Eingang
// kommt keine Identitäts-Kopfzeile der Anmeldung im Handler an — weder auf dem
// Board-Paar-Zweig (abs_/abe_, an der Agenten-Middleware vorbei) noch auf dem
// normalen Agentenweg, und Remote-Name gehört dazu.
func TestAgentHandler_IdentitaetsKopfzeilenEntfernt(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := storage.NewBoardRepo(db).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1","title":"B","sprints":[]}}`)); err != nil {
		t.Fatal(err)
	}
	tokens := storage.NewAgentTokenRepo(db)
	editor, _, err := tokens.Create(ctx, "projekt-x", "b1", storage.AgentRolleEditor, "test")
	if err != nil {
		t.Fatal(err)
	}
	kopfzeilen := []string{"Remote-User", "Remote-Email", "Remote-Groups", "Remote-Name", "X-Aboard-Forward-Auth-Secret",
		api.OWUIUserJWTHeader, "X-OpenWebUI-User-Email"}
	var gesehen http.Header
	aufnehmen := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gesehen = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	})
	mux := http.NewServeMux()
	mux.Handle("GET /schatten/{board}", aufnehmen)
	mux.Handle("GET /boards/{boardId}", aufnehmen)
	srv := httptest.NewServer(agentHandler(mux, api.NewAgentZugang(tokens)))
	t.Cleanup(srv.Close)

	for _, fall := range []struct{ name, pfad, token string }{
		{"Board-Paar abs_", "/schatten/b1", storage.SchattenAusgangPraefix + "beliebig"},
		{"Agent editor", "/boards/b1", editor},
	} {
		t.Run(fall.name, func(t *testing.T) {
			gesehen = nil
			req, _ := http.NewRequest(http.MethodGet, srv.URL+fall.pfad, nil)
			req.Header.Set("Authorization", "Bearer "+fall.token)
			for _, h := range kopfzeilen {
				req.Header.Set(h, "client-wert")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if gesehen == nil {
				t.Fatalf("Handler nicht erreicht: Status %d", resp.StatusCode)
			}
			for _, h := range kopfzeilen {
				if v := gesehen.Get(h); v != "" {
					t.Errorf("%s kommt im Handler an: %q", h, v)
				}
			}
		})
	}
}
