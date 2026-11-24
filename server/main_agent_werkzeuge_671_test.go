package main

// Karte #671: Die Werkzeug-Wege über den Pfad-Eingang, wie Open WebUI sie
// aufruft — Spezifikation unter /agent/openapi.json, Werkzeuge unter
// /agent/werkzeuge/… — im strengen Forward-Auth-Modus. Am Menschen-Pfad gibt
// es sie nicht.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestAgentPfad671_WerkzeugeUeberAgentPraefix(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	t.Setenv("ABOARD_FORWARD_AUTH_SECRET", pfadTestSecret)
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	state := `{"version":"2","board":{"id":"b1","title":"Pfadboard","nextDisplayId":2,
	  "columnDefs":[{"id":"col-backlog","title":"Backlog"}],
	  "sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[{"id":"k1","displayId":1,"title":"K","columnId":"col-backlog","comments":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "Pfadboard", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	motor, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	bapi := api.NewBoardsAPI(boards).WithACL(storage.NewAclRepo(db)).WithActivities(storage.NewActivityRepo(db)).WithFold(motor)
	bapi.Register(mux)
	bapi.RegisterWerkzeuge(mux)
	tokens := storage.NewAgentTokenRepo(db)
	token, _, err := tokens.Create(ctx, "projekt-x", "b1", storage.AgentRolleEditor, "test")
	if err != nil {
		t.Fatal(err)
	}
	ohneACL := func(h http.Handler) http.Handler { return h }
	srv := httptest.NewServer(hauptHandler(mux, ohneACL, api.NewAgentZugang(tokens), true))
	t.Cleanup(srv.Close)

	faelle := []struct {
		name, methode, pfad, token, body string
		kopf                             map[string]string
		want                             int
	}{
		{"Spezifikation", "GET", "/agent/openapi.json", token, "", nil, 200},
		{"Spezifikation ohne Token", "GET", "/agent/openapi.json", "", "", nil, 401},
		{"boards", "GET", "/agent/werkzeuge/boards", token, "", nil, 200},
		{"board_lesen", "GET", "/agent/werkzeuge/boards/b1", token, "", nil, 200},
		{"karte_anlegen", "POST", "/agent/werkzeuge/boards/b1/karten", token, `{"titel":"Über den Pfad"}`, nil, 200},
		{"kommentieren", "POST", "/agent/werkzeuge/boards/b1/karten/1/kommentare", token, `{"text":"hallo"}`, nil, 200},
		// Am Menschen-Pfad: angemeldeter Mensch bekommt 404, ohne Anmeldung 401.
		{"Mensch Spezifikation", "GET", "/openapi.json", "", "", menschKopf, 404},
		{"Mensch boards", "GET", "/werkzeuge/boards", "", "", menschKopf, 404},
		{"Mensch karte_anlegen", "POST", "/werkzeuge/boards/b1/karten", "", `{"titel":"x"}`, menschKopf, 404},
		{"ohne Anmeldung", "GET", "/werkzeuge/boards", "", "", nil, 401},
	}
	for _, f := range faelle {
		if s := agentAnfrage(t, srv, f.methode, f.pfad, f.token, f.body, f.kopf); s != f.want {
			t.Errorf("%s: %d, erwartet %d", f.name, s, f.want)
		}
	}
	var st string
	if err := db.QueryRow(`SELECT state_json FROM boards WHERE id='b1'`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st, `"title":"Über den Pfad"`) || !strings.Contains(st, `"author":"agent:projekt-x"`) || strings.Contains(st, `"title":"x"`) {
		t.Fatalf("Stand: %s", st)
	}
}
