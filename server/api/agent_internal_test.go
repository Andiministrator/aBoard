package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// TestAgentDarf_Positivliste: Jede Zeile ist eine Route, die ein Agent
// erreichen könnte. Erlaubt ist nur, was ADR-0129 §3 nennt, für das eigene
// Board und — bei Mutationen — nur als editor.
func TestAgentDarf_Positivliste(t *testing.T) {
	editor := &AgentPrincipal{Name: "e", BoardID: "b1", Role: storage.AgentRolleEditor}
	viewer := &AgentPrincipal{Name: "v", BoardID: "b1", Role: storage.AgentRolleViewer}
	faelle := []struct {
		methode, pfad string
		editor        bool
		viewer        bool
	}{
		{"GET", "/boards/b1", true, true},
		{"HEAD", "/boards/b1", true, true},
		{"PUT", "/boards/b1", false, false}, // Ganz-Board-PUT: nur Ops
		{"DELETE", "/boards/b1", false, false},
		{"GET", "/boards/b1/events", true, true},
		{"GET", "/boards/b1/ich", true, true},
		{"POST", "/boards/b1/ich", false, false},
		{"POST", "/boards/b1/ops", true, false},
		{"GET", "/boards/b1/ops", false, false},
		{"POST", "/boards/b1/attachments/k1", true, false},
		{"DELETE", "/boards/b1/attachments/k1", false, false}, // alle Anhänge einer Karte
		{"GET", "/boards/b1/attachments/k1/a1", true, true},
		{"DELETE", "/boards/b1/attachments/k1/a1", false, false}, // F-1882: nur anhang_loeschen
		{"POST", "/boards/b1/board-attachments", true, false},
		{"GET", "/boards/b1/board-attachments/a1", true, true},
		{"DELETE", "/boards/b1/board-attachments/a1", false, false}, // F-1882
		// fremdes Board
		{"GET", "/boards/b2", false, false},
		{"POST", "/boards/b2/ops", false, false},
		// alles andere unter /boards
		{"GET", "/boards", false, false},
		{"POST", "/boards", false, false},
		{"GET", "/boards/b1/acl", false, false},
		{"POST", "/boards/b1/acl", false, false},
		{"POST", "/boards/b1/transfer", false, false},
		{"GET", "/boards/b1/activities", false, false},
		{"POST", "/boards/b1/llm/send", false, false},
		{"GET", "/boards/b1/backups", false, false},
		// ausserhalb /boards
		{"GET", "/known-users", false, false},
		{"GET", "/clients", false, false},
		{"GET", "/projects", false, false},
		{"GET", "/me/cards", false, false},
		{"GET", "/admin/config", false, false},
		{"POST", "/admin/agent-tokens", false, false},
		{"GET", "/healthz", false, false},
		{"GET", "/", false, false},
		// Pfad-Tricks: Die Prüfung muss dasselbe Board sehen wie der Mux.
		{"POST", "/boards/b1/../b2/ops", false, false},
		{"POST", "/boards/b1/./ops", false, false},
		{"POST", "/boards/b1%2F..%2Fb2/ops", false, false},
		{"POST", "/boards//b1/ops", false, false},
		{"POST", "/boards/b1/ops/", false, false},
		{"GET", "/boards/b%31", true, true}, // %31 = "1": der Mux dekodiert genauso
	}
	for _, f := range faelle {
		for _, p := range []*AgentPrincipal{editor, viewer} {
			r := httptest.NewRequest(f.methode, "http://agent"+f.pfad, nil)
			want := f.editor
			if p == viewer {
				want = f.viewer
			}
			if got := agentDarf(p, r); got != want {
				t.Errorf("%s %s als %s: %v, erwartet %v", f.methode, f.pfad, p.Role, got, want)
			}
		}
	}
}

type stubTokens struct {
	mu       sync.Mutex
	tokens   map[string]*storage.AgentToken
	aktiv    map[string]bool
	aktivErr error
}

func (s *stubTokens) Lookup(_ context.Context, k string) (*storage.AgentToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tokens[k]; ok && s.aktiv[t.ID] {
		return t, nil
	}
	return nil, storage.ErrAgentTokenUnbekannt
}

func (s *stubTokens) IstAktiv(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.aktiv[id], s.aktivErr
}

func (s *stubTokens) widerrufe(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aktiv[id] = false
}

func neuerStub() *stubTokens {
	return &stubTokens{
		tokens: map[string]*storage.AgentToken{"abt_gut": {ID: "t1", Name: "agent-a", BoardID: "b1", Role: "editor"}},
		aktiv:  map[string]bool{"t1": true},
	}
}

func TestAgentZugang_Antworten(t *testing.T) {
	var gesehen *AgentPrincipal
	var remoteUser string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gesehen = AgentFromContext(r.Context())
		remoteUser = r.Header.Get("Remote-User")
		if IdentityFromContext(r.Context()) != nil {
			t.Error("Agent trägt eine ForwardAuthIdentity")
		}
		w.WriteHeader(http.StatusTeapot)
	})
	h := NewAgentZugang(neuerStub()).Middleware(next)
	faelle := []struct {
		name, auth, pfad string
		status           int
	}{
		{"ohne Token", "", "/boards/b1", 401},
		{"Basic statt Bearer", "Basic abc", "/boards/b1", 401},
		{"unbekanntes Token", "Bearer abt_falsch", "/boards/b1", 401},
		{"unbekanntes Token, unbekannter Pfad", "Bearer abt_falsch", "/admin/config", 401},
		{"gültig, verbotene Route", "Bearer abt_gut", "/boards", 403},
		{"gültig, fremdes Board", "Bearer abt_gut", "/boards/b2", 403},
		{"gültig, erlaubt", "Bearer abt_gut", "/boards/b1", 418},
		{"gültig, Kleinschreibung", "bearer abt_gut", "/boards/b1", 418},
	}
	for _, f := range faelle {
		gesehen = nil
		r := httptest.NewRequest("GET", "http://agent"+f.pfad, nil)
		if f.auth != "" {
			r.Header.Set("Authorization", f.auth)
		}
		r.Header.Set("Remote-User", "chef@firma.de")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != f.status {
			t.Errorf("%s: Status %d, erwartet %d", f.name, w.Code, f.status)
		}
		if f.status == 401 && w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: WWW-Authenticate fehlt", f.name)
		}
		if f.status == 418 {
			if gesehen == nil || gesehen.Name != "agent-a" || gesehen.Akteur() != "agent:agent-a" {
				t.Errorf("%s: Principal %+v", f.name, gesehen)
			}
			if remoteUser != "" {
				t.Errorf("%s: Identitäts-Header nicht entfernt", f.name)
			}
		}
	}
}

// laufenderRequest startet einen Request, der offen bleibt, bis sein
// Kontext endet (wie ein SSE-Stream), und meldet dessen Ende.
func laufenderRequest(t *testing.T, z *AgentZugang) (beendet <-chan struct{}) {
	t.Helper()
	gestartet := make(chan struct{})
	ende := make(chan struct{})
	h := z.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(gestartet)
		<-r.Context().Done()
	}))
	go func() {
		r := httptest.NewRequest("GET", "http://agent/boards/b1/events", nil)
		r.Header.Set("Authorization", "Bearer abt_gut")
		h.ServeHTTP(httptest.NewRecorder(), r)
		close(ende)
	}()
	select {
	case <-gestartet:
	case <-time.After(2 * time.Second):
		t.Fatal("Request startet nicht")
	}
	return ende
}

// TestAgentZugang_WiderrufSchliesstSofort (K-4): Widerruf über die Admin-API
// im selben Prozess bricht offene Verbindungen ab.
func TestAgentZugang_WiderrufSchliesstSofort(t *testing.T) {
	z := NewAgentZugang(neuerStub())
	z.pruefIntervall = time.Hour // die Wache darf hier nicht der Grund sein
	ende := laufenderRequest(t, z)
	if n := z.SchliesseAlle("t1"); n != 1 {
		t.Fatalf("%d Verbindungen geschlossen, erwartet 1", n)
	}
	select {
	case <-ende:
	case <-time.After(2 * time.Second):
		t.Fatal("Verbindung nach SchliesseAlle noch offen")
	}
	if n := z.SchliesseAlle("t1"); n != 0 {
		t.Fatalf("Verbindung nach Ende nicht abgemeldet (%d)", n)
	}
}

// TestAgentZugang_WacheBemerktWiderrufVonAussen (K-4): Ein Widerruf über die
// CLI (anderer Prozess) erreicht offene Verbindungen über die Wache.
func TestAgentZugang_WacheBemerktWiderrufVonAussen(t *testing.T) {
	stub := neuerStub()
	z := NewAgentZugang(stub)
	z.pruefIntervall = 10 * time.Millisecond
	ende := laufenderRequest(t, z)
	stub.widerrufe("t1")
	select {
	case <-ende:
	case <-time.After(2 * time.Second):
		t.Fatal("Wache hat den Widerruf nicht bemerkt")
	}
}

// TestAgentZugang_WacheBeiDBFehlerOffen: Ist die Prüfung gestört, bleibt die
// Verbindung offen (Ausfall in Richtung Nichtstun).
func TestAgentZugang_WacheBeiDBFehlerOffen(t *testing.T) {
	stub := neuerStub()
	stub.aktivErr = errors.New("db weg")
	stub.aktiv["t1"] = true
	z := NewAgentZugang(stub)
	z.pruefIntervall = 5 * time.Millisecond
	ende := laufenderRequest(t, z)
	select {
	case <-ende:
		t.Fatal("Verbindung trotz Prüffehler geschlossen")
	case <-time.After(60 * time.Millisecond):
	}
	z.SchliesseAlle("t1")
	<-ende
}

func TestOhneBearer(t *testing.T) {
	h := OhneBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for auth, want := range map[string]int{"": 418, "Bearer abt_gut": 401, "bearer abt_x": 401, "Basic abc": 418,
		// Kritiker Sprint 71: Ein fremder Bearer (etwa vom Proxy) sperrt nicht aus.
		"Bearer eyJhbGciOi.proxy": 418} {
		r := httptest.NewRequest("GET", "http://mensch/boards/b1", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Authorization %q: %d, erwartet %d", auth, w.Code, want)
		}
	}
}

// rennStub: Lookup sagt gültig, die zweite Prüfung danach sagt widerrufen —
// genau das Fenster, in dem ein Widerruf zwischen Lookup und Anmelden liegt.
type rennStub struct{}

func (rennStub) Lookup(context.Context, string) (*storage.AgentToken, error) {
	return &storage.AgentToken{ID: "t1", Name: "a", BoardID: "b1", Role: "editor"}, nil
}
func (rennStub) IstAktiv(context.Context, string) (bool, error) { return false, nil }

// TestAgentZugang_WiderrufZwischenLookupUndAnmelden (Kritiker Sprint 71,
// K-A1): Der Request darf den Handler nicht erreichen.
func TestAgentZugang_WiderrufZwischenLookupUndAnmelden(t *testing.T) {
	erreicht := false
	h := NewAgentZugang(rennStub{}).Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { erreicht = true }))
	r := httptest.NewRequest("POST", "http://agent/boards/b1/ops", nil)
	r.Header.Set("Authorization", "Bearer abt_x")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if erreicht || w.Code != http.StatusUnauthorized {
		t.Fatalf("Handler erreicht=%v, Status %d — widerrufenes Token kam durch", erreicht, w.Code)
	}
}
