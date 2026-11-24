package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type agentAdminEnv struct {
	admin, agent *httptest.Server
	cfg          *storage.ConfigRepo
	gestartet    chan struct{}
}

// newAgentAdminEnv: Menschen-Server mit Admin-Routen (Forward-Auth, Trust an)
// und daneben ein Agenten-Server auf DEMSELBEN Token-Repo und AgentZugang —
// wie in run(). Auf dem Agenten-Server hält GET /boards/{id}/events offen,
// bis der Kontext endet (Stellvertreter für einen SSE-Stream).
func newAgentAdminEnv(t *testing.T) *agentAdminEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := storage.NewBoardRepo(db).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1","title":"B"}}`)); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][]any{{"u-admin", "admin@firma.de", 1}, {"u-normal", "normal@firma.de", 0}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', ?)`, u...); err != nil {
			t.Fatal(err)
		}
	}
	cfg := storage.NewConfigRepo(db, nil, nil)
	repo := storage.NewAgentTokenRepo(db)
	zugang := api.NewAgentZugang(repo)

	menschMux := http.NewServeMux()
	adm := api.NewAdminAPI(cfg, storage.NewUserRepo(db), "test", time.Now())
	adm.RegisterAgentTokens(menschMux, repo, zugang)
	env := &agentAdminEnv{cfg: cfg, gestartet: make(chan struct{}, 1)}
	env.admin = httptest.NewServer(api.WithForwardAuthIfEnabled(menschMux))
	t.Cleanup(env.admin.Close)

	agentMux := http.NewServeMux()
	agentMux.HandleFunc("GET /boards/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		env.gestartet <- struct{}{}
		<-r.Context().Done()
	})
	env.agent = httptest.NewServer(zugang.Middleware(agentMux))
	t.Cleanup(env.agent.Close)
	return env
}

func (e *agentAdminEnv) admAnfrage(t *testing.T, methode, pfad, email, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(methode, e.admin.URL+pfad, strings.NewReader(body))
	if email != "" {
		req.Header.Set("Remote-Email", email)
		req.Header.Set("Remote-User", email)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAdminAgentTokens_NurAdmins(t *testing.T) {
	e := newAgentAdminEnv(t)
	for _, r := range [][2]string{{"GET", "/admin/agent-tokens"}, {"POST", "/admin/agent-tokens"}, {"DELETE", "/admin/agent-tokens/x"}} {
		if s, _ := e.admAnfrage(t, r[0], r[1], "normal@firma.de", `{}`); s != http.StatusForbidden {
			t.Errorf("%s %s als Nicht-Admin: %d", r[0], r[1], s)
		}
	}
}

// TestAdminAgentTokens_Lebenszyklus: anlegen (Klartext genau einmal),
// auflisten (ohne Klartext), widerrufen — und der Widerruf schliesst eine
// offene Verbindung des Agenten sofort (K-4).
func TestAdminAgentTokens_Lebenszyklus(t *testing.T) {
	e := newAgentAdminEnv(t)
	s, out := e.admAnfrage(t, "POST", "/admin/agent-tokens", "admin@firma.de", `{"name":"projekt-x","board":"b1","role":"editor"}`)
	if s != http.StatusCreated {
		t.Fatalf("anlegen: %d %v", s, out)
	}
	token, _ := out["token"].(string)
	if !strings.HasPrefix(token, storage.AgentTokenPraefix) {
		t.Fatalf("Token %q", token)
	}
	for body, want := range map[string]int{
		`{"name":"projekt-x","board":"b1","role":"editor"}`: http.StatusConflict,
		`{"name":"y","board":"b1","role":"owner"}`:          http.StatusBadRequest,
		`{"name":"y","board":"gibtsnicht","role":"viewer"}`: http.StatusBadRequest,
		`kein json`: http.StatusBadRequest,
	} {
		if s, _ := e.admAnfrage(t, "POST", "/admin/agent-tokens", "admin@firma.de", body); s != want {
			t.Errorf("anlegen %s: %d, erwartet %d", body, s, want)
		}
	}

	req, _ := http.NewRequest("GET", "http://x/admin/agent-tokens", nil)
	req.Header.Set("Remote-Email", "admin@firma.de")
	req.Header.Set("Remote-User", "admin@firma.de")
	req.URL, _ = req.URL.Parse(e.admin.URL + "/admin/agent-tokens")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var liste []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&liste)
	resp.Body.Close()
	if len(liste) != 1 || liste[0]["name"] != "projekt-x" {
		t.Fatalf("Liste: %v", liste)
	}
	roh, _ := json.Marshal(liste)
	if strings.Contains(string(roh), token[len(storage.AgentTokenPraefix):]) {
		t.Fatal("Liste enthält das Token")
	}

	// Offene Verbindung des Agenten.
	ende := make(chan int, 1)
	go func() {
		r, _ := http.NewRequest("GET", e.agent.URL+"/boards/b1/events", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			ende <- -1
			return
		}
		resp.Body.Close()
		ende <- resp.StatusCode
	}()
	select {
	case <-e.gestartet:
	case <-time.After(2 * time.Second):
		t.Fatal("Agenten-Verbindung startet nicht")
	}
	s, out = e.admAnfrage(t, "DELETE", "/admin/agent-tokens/projekt-x", "admin@firma.de", "")
	if s != http.StatusOK || out["verbindungen_geschlossen"] != float64(1) {
		t.Fatalf("widerrufen: %d %v", s, out)
	}
	select {
	case <-ende:
	case <-time.After(2 * time.Second):
		t.Fatal("Verbindung nach Widerruf noch offen")
	}
	if s, _ := e.admAnfrage(t, "DELETE", "/admin/agent-tokens/projekt-x", "admin@firma.de", ""); s != http.StatusNotFound {
		t.Errorf("zweiter Widerruf: %d", s)
	}
	// Das widerrufene Token wirkt nicht mehr.
	r, _ := http.NewRequest("GET", e.agent.URL+"/boards/b1/events", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("widerrufenes Token: %d", resp.StatusCode)
	}

	// Audit mit Admin-ID (K-13).
	eintraege, _ := e.cfg.Audit(context.Background(), "", 50)
	aktionen := map[string]string{}
	for _, a := range eintraege {
		if a.Key == "agent-token:projekt-x" {
			aktionen[a.Action] = a.UserID
		}
	}
	if aktionen["agent-token-create"] != "u-admin" || aktionen["agent-token-revoke"] != "u-admin" {
		t.Fatalf("Audit: %v", aktionen)
	}
}
