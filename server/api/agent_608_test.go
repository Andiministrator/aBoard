package api_test

// Karte #608 (ADR-0130 §6): Agenten-Token je (Person, Board) über die echten
// Wege — Selbstbedienung am Menschen-Weg, Ops über den Agenten-Zugang,
// Protokoll lesen, Rolle ≤ ACL, Entfernen vom Board.

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type env608 struct {
	mensch, agent *httptest.Server
	acl           *storage.AclRepo
	boards        *storage.BoardRepo
	db            *sql.DB
	// nutzerAnlegen legt eine weitere Person an (Karte #638).
	nutzerAnlegen func(t *testing.T, id, email string)
}

func newEnv608(t *testing.T) *env608 {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	users := storage.NewUserRepo(db)
	state := `{"version":"2","board":{"id":"b1","title":"B","nextDisplayId":2,
	  "columnDefs":[{"id":"col-ready","title":"Ready"}],
	  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"K","columnId":"col-ready","comments":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "B", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	for _, u := range [][]any{{"u-anna", "anna@firma.de", 0}, {"u-admin", "admin@firma.de", 1}} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', ?)`, u...); err != nil {
			t.Fatal(err)
		}
	}
	for email, rolle := range map[string]string{"anna@firma.de": "editor", "admin@firma.de": "owner"} {
		if err := acl.Set(ctx, "b1", email, rolle); err != nil {
			t.Fatal(err)
		}
	}
	repo := storage.NewAgentTokenRepo(db)
	zugang := api.NewAgentZugang(repo)
	mux := http.NewServeMux()
	api.NewBoardsAPI(boards).WithACL(acl).WithActivities(storage.NewActivityRepo(db)).WithFold(testMotor).Register(mux)
	adm := api.NewAdminAPI(storage.NewConfigRepo(db, nil, nil), users, "test", time.Now())
	adm.RegisterAgentTokens(mux, repo, zugang)
	api.RegisterAgentTokensSelbst(mux, repo, users, zugang, storage.NewConfigRepo(db, nil, nil))
	e := &env608{acl: acl, boards: boards, db: db}
	e.nutzerAnlegen = func(t *testing.T, id, email string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', 0)`, id, email); err != nil {
			t.Fatal(err)
		}
	}
	e.mensch = httptest.NewServer(api.WithForwardAuthIfEnabled(api.NewACLGuard(acl, users, storage.NewBootstrapRepo(db)).Middleware(mux)))
	t.Cleanup(e.mensch.Close)
	e.agent = httptest.NewServer(zugang.Middleware(mux))
	t.Cleanup(e.agent.Close)
	return e
}

func (e *env608) anfrage(t *testing.T, srv *httptest.Server, methode, pfad, wer, body string, kopf ...string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(methode, srv.URL+pfad, strings.NewReader(body))
	if srv == e.mensch && wer != "" {
		req.Header.Set("Remote-Email", wer)
		req.Header.Set("Remote-User", wer)
	}
	if srv == e.agent && wer != "" {
		req.Header.Set("Authorization", "Bearer "+wer)
	}
	for i := 0; i+1 < len(kopf); i += 2 {
		req.Header.Set(kopf[i], kopf[i+1])
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestAgent608_SelbstbedienungStempelUndRolle(t *testing.T) {
	e := newEnv608(t)
	anlegen := `{"board":"b1","name":"annas-claude"}`

	// Ab Werk aus.
	if s, b := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "anna@firma.de", anlegen); s != http.StatusForbidden {
		t.Fatalf("Selbstbedienung aus: %d %s", s, b)
	}
	// Nur Admins stellen den Schalter.
	if s, _ := e.anfrage(t, e.mensch, "PUT", "/admin/boards/b1/agent-selbstbedienung", "anna@firma.de", `{"an":true}`); s != http.StatusForbidden {
		t.Fatalf("Schalter als Nicht-Admin: %d", s)
	}
	if s, b := e.anfrage(t, e.mensch, "PUT", "/admin/boards/b1/agent-selbstbedienung", "admin@firma.de", `{"an":true}`); s != http.StatusOK {
		t.Fatalf("Schalter: %d %s", s, b)
	}
	s, b := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "anna@firma.de", anlegen)
	if s != http.StatusCreated {
		t.Fatalf("anlegen: %d %s", s, b)
	}
	var angelegt struct {
		Token string `json:"token"`
		Agent struct {
			ID        string `json:"id"`
			ExpiresAt string `json:"expires_at"`
			Kennung   string `json:"kennung"`
		} `json:"agent"`
	}
	_ = json.Unmarshal(b, &angelegt)
	if !strings.HasPrefix(angelegt.Token, "abt_"+angelegt.Agent.Kennung+"_") || angelegt.Agent.ExpiresAt == "" {
		t.Fatalf("Token/Ablauf: %s", b)
	}
	// Liste: eigene Tokens ohne Klartext, und das Board mit Selbstbedienung.
	s, b = e.anfrage(t, e.mensch, "GET", "/users/me/agent-tokens", "anna@firma.de", "")
	geheim := angelegt.Token[len("abt_"+angelegt.Agent.Kennung+"_"):]
	if s != http.StatusOK || strings.Contains(string(b), geheim) || !strings.Contains(string(b), `"id":"b1"`) || !strings.Contains(string(b), "annas-claude") {
		t.Fatalf("Liste: %d %s", s, b)
	}

	// Agent schreibt einen Kommentar — der Server stempelt Autor und „für wen“,
	// ein mitgeschicktes onBehalfOf zählt nicht.
	ops := `{"ops":[{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"c1","author":"anna@firma.de","onBehalfOf":"chef@firma.de","text":"hallo","created":"2026-10-01"}}}]}`
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", angelegt.Token, ops, api.SitzungHeader, "sitzung-42"); s != http.StatusOK {
		t.Fatalf("ops: %d %s", s, b)
	}
	b1, err := e.boards.Get(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if st := string(b1.StateJSON); !strings.Contains(st, `"author":"agent:annas-claude"`) || !strings.Contains(st, `"onBehalfOf":"anna@firma.de"`) || strings.Contains(st, "chef@firma.de") {
		t.Fatalf("Kommentar-Stempel: %s", st)
	}
	s, b = e.anfrage(t, e.mensch, "GET", "/boards/b1/activities?card=k1", "anna@firma.de", "")
	if s != http.StatusOK || !strings.Contains(string(b), `"on_behalf_of":"anna@firma.de"`) || !strings.Contains(string(b), `"session":"sitzung-42"`) {
		t.Fatalf("Protokoll: %d %s", s, b)
	}
	// Ungültige Sitzungskennung → 400.
	if s, _ := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", angelegt.Token, ops, api.SitzungHeader, strings.Repeat("x", 201)); s != http.StatusBadRequest {
		t.Fatalf("zu lange Sitzung: %d", s)
	}

	// Agenten erreichen die Selbstbedienung nicht (Positivliste) — mit
	// GÜLTIGEM Token, sonst wäre 401 trivial (Session-Kritiker Sprint 78, K-6).
	for _, pfad := range []string{"/users/me/agent-tokens", "/admin/agent-tokens"} {
		if s, _ := e.anfrage(t, e.agent, "GET", pfad, angelegt.Token, ""); s != http.StatusForbidden {
			t.Fatalf("Agent an %s: %d (erwartet 403)", pfad, s)
		}
	}

	// Rolle sinkt mit der ACL: viewer → Schreiben 403, Lesen 200.
	ctx := context.Background()
	if err := e.acl.Set(ctx, "b1", "anna@firma.de", storage.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", angelegt.Token, ops); s != http.StatusForbidden {
		t.Fatalf("viewer-Person schreibt: %d", s)
	}
	if s, _ := e.anfrage(t, e.agent, "GET", "/boards/b1", angelegt.Token, ""); s != http.StatusOK {
		t.Fatalf("viewer-Person liest: %d", s)
	}
	// Von DIESEM Board entfernt (Nutzer bleibt) → 401 (K-6).
	if err := e.acl.Delete(ctx, "b1", "anna@firma.de"); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.anfrage(t, e.agent, "GET", "/boards/b1", angelegt.Token, ""); s != http.StatusUnauthorized {
		t.Fatalf("entfernte Person: %d", s)
	}
	// Wieder aufgenommen; eigenes Token widerrufen → 401; fremdes → 404.
	if err := e.acl.Set(ctx, "b1", "anna@firma.de", storage.RoleEditor); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.anfrage(t, e.mensch, "DELETE", "/users/me/agent-tokens/"+angelegt.Agent.ID, "admin@firma.de", ""); s != http.StatusNotFound {
		t.Fatalf("fremdes Token widerrufen: %d", s)
	}
	if s, b := e.anfrage(t, e.mensch, "DELETE", "/users/me/agent-tokens/"+angelegt.Agent.ID, "anna@firma.de", ""); s != http.StatusOK {
		t.Fatalf("eigenes widerrufen: %d %s", s, b)
	}
	if s, _ := e.anfrage(t, e.agent, "GET", "/boards/b1", angelegt.Token, ""); s != http.StatusUnauthorized {
		t.Fatalf("nach Widerruf: %d", s)
	}

}

func TestAgent608_AdminWegePerson(t *testing.T) {
	e := newEnv608(t)
	s, b := e.anfrage(t, e.mensch, "POST", "/admin/agent-tokens", "admin@firma.de", `{"name":"fuer-anna","board":"b1","role":"editor","user":"anna@firma.de","gueltig_tage":30}`)
	if s != http.StatusCreated {
		t.Fatalf("anlegen für Person: %d %s", s, b)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(b, &out)
	if s, b := e.anfrage(t, e.mensch, "POST", "/admin/agent-tokens", "admin@firma.de", `{"name":"x","board":"b1","role":"editor","gueltig_tage":400}`); s != http.StatusBadRequest {
		t.Fatalf("400 Tage: %d %s", s, b)
	}
	s, b = e.anfrage(t, e.mensch, "GET", "/admin/agent-tokens", "admin@firma.de", "")
	if s != http.StatusOK || !strings.Contains(string(b), `"user_email":"anna@firma.de"`) || !strings.Contains(string(b), `"expires_at":`) {
		t.Fatalf("Admin-Liste: %d %s", s, b)
	}
	if s, b := e.anfrage(t, e.mensch, "POST", "/admin/users/u-anna/agent-tokens/revoke", "admin@firma.de", ""); s != http.StatusOK || !strings.Contains(string(b), `"widerrufen":1`) {
		t.Fatalf("alle einer Person widerrufen: %d %s", s, b)
	}
	if s, _ := e.anfrage(t, e.agent, "GET", "/boards/b1", out.Token, ""); s != http.StatusUnauthorized {
		t.Fatalf("nach Personen-Widerruf: %d", s)
	}
}
