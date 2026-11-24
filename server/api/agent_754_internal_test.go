package api

// Karte #754 (ADR-0139 §5): Personen-Token für mehrere Boards — Middleware,
// Wache und Ausfall zu bei einem Prüfer ohne Board-Liste.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

func mehrBoardEnv754(t *testing.T) (*storage.AgentTokenRepo, func(string, ...any), string) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, id := range []string{"b1", "b2"} {
		if _, err := storage.NewBoardRepo(db).Insert(ctx, id, id, "2", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1', 'anna@firma.de', 'A', '2026-01-01', 0)`)
	exec(`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b1', 'anna@firma.de', 'editor', '2026-01-01')`)
	exec(`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b2', 'anna@firma.de', 'editor', '2026-01-01')`)
	repo := storage.NewAgentTokenRepo(db)
	klar, _, err := repo.Anlegen(ctx, storage.AgentTokenAnlage{Name: "anna-mehrere", UserID: "u1", Role: storage.AgentRolleEditor,
		CreatedBy: "cli", BoardIDs: []string{"b1"}})
	if err != nil {
		t.Fatal(err)
	}
	return repo, exec, klar
}

func rufe754(z *AgentZugang, klar, methode, pfad string) (int, *AgentPrincipal) {
	var p *AgentPrincipal
	h := z.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p = AgentFromContext(r.Context()) }))
	r := httptest.NewRequest(methode, "http://agent"+pfad, nil)
	r.Header.Set("Authorization", "Bearer "+klar)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, p
}

func TestAgent754_MiddlewareJeBoard(t *testing.T) {
	repo, exec, klar := mehrBoardEnv754(t)
	z := NewAgentZugang(repo)

	// Board der Liste: Principal mit Board und Rolle aus der ACL.
	s, p := rufe754(z, klar, "GET", "/werkzeuge/boards/b1")
	if s != http.StatusOK || p == nil || p.BoardID != "b1" || p.Role != storage.AgentRolleEditor || !p.MehrBoard || p.OnBehalfOf != "anna@firma.de" {
		t.Fatalf("b1: %d %+v", s, p)
	}
	// Nicht in der Liste (Person ist dort editor), unbekannt: 403.
	for _, pfad := range []string{"/werkzeuge/boards/b2", "/werkzeuge/boards/gibts-nicht", "/werkzeuge/boards/" + storage.ServiceBoardID} {
		if s, _ := rufe754(z, klar, "GET", pfad); s != http.StatusForbidden {
			t.Errorf("%s: %d, erwartet 403", pfad, s)
		}
	}
	// Rohe Wege: nie, auch nicht für ein Board der Liste.
	for _, w := range [][2]string{{"GET", "/boards/b1"}, {"POST", "/boards/b1/ops"}, {"GET", "/boards/b1/events"}, {"GET", "/boards/b1/ich"}} {
		if s, _ := rufe754(z, klar, w[0], w[1]); s != http.StatusForbidden {
			t.Errorf("%s %s: %d, erwartet 403", w[0], w[1], s)
		}
	}
	// Ohne Board im Pfad: die Boards der Liste mit Rolle.
	s, p = rufe754(z, klar, "GET", "/werkzeuge/boards")
	if s != http.StatusOK || p == nil || len(p.boardRollen) != 1 || p.boardRollen["b1"] != storage.AgentRolleEditor || p.BoardID != "" {
		t.Fatalf("boards: %d %+v", s, p)
	}
	// Beschreibung und MCP: ohne Board.
	for _, w := range [][2]string{{"GET", "/openapi.json"}, {"POST", "/mcp"}} {
		if s, p := rufe754(z, klar, w[0], w[1]); s != http.StatusOK || p == nil || p.BoardID != "" || p.Role != "" {
			t.Errorf("%s %s: %d %+v", w[0], w[1], s, p)
		}
	}
	// Je Aufruf gegen die ACL: Entzug wirkt beim nächsten Aufruf.
	exec(`DELETE FROM acl WHERE board_id = 'b1' AND user_email = 'anna@firma.de'`)
	if s, _ := rufe754(z, klar, "GET", "/werkzeuge/boards/b1"); s != http.StatusForbidden {
		t.Errorf("b1 nach Entzug: %d, erwartet 403", s)
	}
	// Das Token selbst gilt weiter (401 wäre falsch): die Liste ist nur leer.
	if s, p := rufe754(z, klar, "GET", "/werkzeuge/boards"); s != http.StatusOK || len(p.boardRollen) != 0 {
		t.Errorf("boards nach Entzug: %d %+v", s, p)
	}
	// Widerruf: 401 wie bei jedem Token.
	l, _ := repo.List(context.Background())
	if _, err := repo.RevokeID(context.Background(), l[0].ID, "", "cli"); err != nil {
		t.Fatal(err)
	}
	if s, _ := rufe754(z, klar, "GET", "/werkzeuge/boards"); s != http.StatusUnauthorized {
		t.Errorf("widerrufen: %d, erwartet 401", s)
	}
}

// Ein Prüfer ohne Board-Liste (Stub) darf ein Mehr-Board-Token nie
// durchlassen — sonst fiele es auf die Token-Rolle zurück.
func TestAgent754_PrueferOhneListeAusfallZu(t *testing.T) {
	stub := neuerStub()
	stub.tokens["abt_mehr"] = &storage.AgentToken{ID: "t2", Name: "m", Role: "editor", UserID: "u1", UserEmail: "a@b.de", Mehrere: true}
	stub.aktiv["t2"] = true
	z := NewAgentZugang(stub)
	for _, pfad := range []string{"/werkzeuge/boards/b1", "/werkzeuge/boards", "/boards/b1"} {
		if s, _ := rufe754(z, "abt_mehr", "GET", pfad); s != http.StatusForbidden {
			t.Errorf("%s: %d, erwartet 403", pfad, s)
		}
	}
}

// Die Wache schliesst eine offene Verbindung, sobald die Person das Board
// verliert — auch wenn das Token selbst weiter gilt.
func TestAgent754_WacheBeiEntzug(t *testing.T) {
	repo, exec, klar := mehrBoardEnv754(t)
	z := NewAgentZugang(repo)
	z.pruefIntervall = 10 * time.Millisecond
	gestartet := make(chan struct{})
	ende := make(chan struct{})
	h := z.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(gestartet)
		<-r.Context().Done()
	}))
	go func() {
		r := httptest.NewRequest("GET", "http://agent/werkzeuge/boards/b1", nil)
		r.Header.Set("Authorization", "Bearer "+klar)
		h.ServeHTTP(httptest.NewRecorder(), r)
		close(ende)
	}()
	select {
	case <-gestartet:
	case <-time.After(2 * time.Second):
		t.Fatal("Request startet nicht")
	}
	select {
	case <-ende:
		t.Fatal("Verbindung ohne Entzug geschlossen")
	case <-time.After(60 * time.Millisecond):
	}
	exec(`DELETE FROM acl WHERE board_id = 'b1' AND user_email = 'anna@firma.de'`)
	select {
	case <-ende:
	case <-time.After(2 * time.Second):
		t.Fatal("Wache hat den Entzug nicht bemerkt")
	}
}

// Zweite Hürde einzeln (Klasse 180): agentDarf sperrt die rohen Wege für ein
// Mehr-Board-Token auch dann, wenn der Principal schon ein Board trägt —
// unabhängig davon, dass mehrBoardPrincipalFuer sie vorher ablehnt.
func TestAgent754_AgentDarfRoheWegeZu(t *testing.T) {
	p := &AgentPrincipal{Name: "m", BoardID: "b1", Role: storage.AgentRolleEditor, MehrBoard: true, OnBehalfOf: "a@b.de"}
	for _, w := range [][2]string{{"GET", "/boards/b1"}, {"POST", "/boards/b1/ops"}, {"GET", "/boards/b1/events"}, {"GET", "/boards/b1/attachments/k/a"}} {
		if agentDarf(p, httptest.NewRequest(w[0], "http://agent"+w[1], nil)) {
			t.Errorf("%s %s erlaubt", w[0], w[1])
		}
	}
	if !agentDarf(p, httptest.NewRequest("GET", "http://agent/werkzeuge/boards/b1", nil)) {
		t.Errorf("Werkzeug-Weg verboten")
	}
}
