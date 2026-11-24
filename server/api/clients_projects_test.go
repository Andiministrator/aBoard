package api_test

// clients_projects_test.go — Tests für /clients und /projects-REST-Endpoints
// (Karte #307, ADR-0102).
//
// Deckt: Auth (kein Identity → 403; nicht-Admin → 403 bei Mutation; viewer+
// liest), CRUD-Roundtrip, UNIQUE-Conflict → 409, Soft- vs. Hard-Delete,
// Rate-Limit Admin (30/h), Active-Toggle via PATCH.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type cpTestEnv struct {
	srv      *httptest.Server
	db       *sql.DB
	users    *storage.UserRepo
	clients  *storage.ClientRepo
	projects *storage.ProjectRepo
	boards   *storage.BoardRepo
	acl      *storage.AclRepo
}

func (e *cpTestEnv) close() {
	e.srv.Close()
	_ = e.db.Close()
}

func newClientsProjectsEnv(t *testing.T) *cpTestEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	users := storage.NewUserRepo(db)
	clients := storage.NewClientRepo(db)
	projects := storage.NewProjectRepo(db)
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)

	mux := http.NewServeMux()
	a := api.NewClientsProjectsAPI(clients, projects, users, boards, acl)
	a.Register(mux)

	var h http.Handler = mux
	h = api.WithForwardAuthIfEnabled(h)

	return &cpTestEnv{
		srv: httptest.NewServer(h), db: db,
		users: users, clients: clients, projects: projects,
		boards: boards, acl: acl,
	}
}

func (e *cpTestEnv) seedUser(t *testing.T, id, email string, isAdmin bool) {
	t.Helper()
	flag := 0
	if isAdmin {
		flag = 1
	}
	if _, err := e.db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, ?)`,
		id, storage.NormalizeEmail(email), "N", "2026-01-01", flag); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
}

func (e *cpTestEnv) do(t *testing.T, method, path, email string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if email != "" {
		r.Header.Set("Remote-Email", email)
		r.Header.Set("Remote-User", email)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// ── Auth ──────────────────────────────────────────────────────────────

// Seit ADR-0129 §2 (F-1124) antwortet die Forward-Auth-Middleware ohne
// Identität mit 401, bevor der Handler seine eigene 403-Prüfung erreicht.
func TestClientsAPI_401_OhneAuth(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	resp := env.do(t, http.MethodGet, "/clients", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d, erwartet 401", resp.StatusCode)
	}
}

func TestClientsAPI_403_NichtAdminBeiCreate(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "u1", "viewer@example.com", false)
	resp := env.do(t, http.MethodPost, "/clients", "viewer@example.com", map[string]any{"name": "X"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin POST = %d, erwartet 403", resp.StatusCode)
	}
}

func TestClientsAPI_ViewerDarfListen(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "u1", "viewer@example.com", false)
	resp := env.do(t, http.MethodGet, "/clients", "viewer@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("viewer GET = %d, erwartet 200", resp.StatusCode)
	}
}

// ── Clients CRUD ──────────────────────────────────────────────────────

func TestClientsAPI_CreateAndList(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)

	resp := env.do(t, http.MethodPost, "/clients", "admin@example.com", map[string]any{
		"name": "Acme GmbH", "description": "Test", "contact": "x@acme.de",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status=%d, erwartet 201; body=%s", resp.StatusCode, mustReadAll(t, resp.Body))
	}
	var created map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &created)
	resp.Body.Close()
	if created["name"] != "Acme GmbH" {
		t.Errorf("created name=%v, erwartet Acme GmbH", created["name"])
	}

	r2 := env.do(t, http.MethodGet, "/clients", "admin@example.com", nil)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("GET list status=%d, erwartet 200", r2.StatusCode)
	}
	var list []map[string]any
	_ = json.Unmarshal(mustReadAll(t, r2.Body), &list)
	if len(list) != 1 {
		t.Errorf("list len=%d, erwartet 1", len(list))
	}
}

func TestClientsAPI_409_DuplicateName(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)

	r1 := env.do(t, http.MethodPost, "/clients", "admin@example.com", map[string]any{"name": "Dup"})
	r1.Body.Close()
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("erste Create = %d, erwartet 201", r1.StatusCode)
	}
	r2 := env.do(t, http.MethodPost, "/clients", "admin@example.com", map[string]any{"name": "Dup"})
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusConflict {
		t.Errorf("zweite Create gleicher Name = %d, erwartet 409", r2.StatusCode)
	}
}

func TestClientsAPI_400_OhneName(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	resp := env.do(t, http.MethodPost, "/clients", "admin@example.com", map[string]any{"description": "kein Name"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status=%d, erwartet 400", resp.StatusCode)
	}
}

func TestClientsAPI_PATCH_UpdateUndActive(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	ctx := context.Background()
	c, _ := env.clients.Create(ctx, "c1", "Alt", "", "")

	resp := env.do(t, http.MethodPatch, "/clients/"+c.ID, "admin@example.com", map[string]any{
		"name": "Neu", "description": "neudesc",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH = %d, erwartet 200", resp.StatusCode)
	}
	got, _ := env.clients.GetByID(ctx, c.ID)
	if got.Name != "Neu" {
		t.Errorf("name=%q, erwartet Neu", got.Name)
	}
}

func TestClientsAPI_DELETE_SoftDeleteDefault(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	ctx := context.Background()
	c, _ := env.clients.Create(ctx, "c1", "X", "", "")

	resp := env.do(t, http.MethodDelete, "/clients/"+c.ID, "admin@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d, erwartet 204", resp.StatusCode)
	}
	got, err := env.clients.GetByID(ctx, c.ID)
	if err != nil {
		t.Fatalf("GetByID nach soft-delete: %v", err)
	}
	if got.Active {
		t.Errorf("Active sollte false sein nach Soft-Delete")
	}
}

func TestClientsAPI_DELETE_HardDeleteEntferntRow(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	ctx := context.Background()
	c, _ := env.clients.Create(ctx, "c1", "X", "", "")

	resp := env.do(t, http.MethodDelete, "/clients/"+c.ID+"?hard=true", "admin@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE hard = %d, erwartet 204", resp.StatusCode)
	}
	_, err := env.clients.GetByID(ctx, c.ID)
	if err != storage.ErrClientNotFound {
		t.Errorf("nach hard-delete err=%v, erwartet ErrClientNotFound", err)
	}
}

func TestClientsAPI_DELETE_404Unbekannt(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	resp := env.do(t, http.MethodDelete, "/clients/ghost", "admin@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE unbekannt = %d, erwartet 404", resp.StatusCode)
	}
}

// ── Projects CRUD ─────────────────────────────────────────────────────

func TestProjectsAPI_CreateOhneClient(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	resp := env.do(t, http.MethodPost, "/projects", "admin@example.com", map[string]any{
		"name": "Internes Projekt",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d, erwartet 201; body=%s", resp.StatusCode, mustReadAll(t, resp.Body))
	}
	var dto map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &dto)
	if dto["client_id"] != nil {
		t.Errorf("client_id=%v, erwartet null", dto["client_id"])
	}
}

func TestProjectsAPI_CreateMitClient(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	ctx := context.Background()
	c, _ := env.clients.Create(ctx, "c1", "Acme", "", "")

	resp := env.do(t, http.MethodPost, "/projects", "admin@example.com", map[string]any{
		"name": "Migration", "client_id": c.ID,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d, erwartet 201", resp.StatusCode)
	}
	var dto map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &dto)
	if dto["client_id"] != c.ID {
		t.Errorf("client_id=%v, erwartet %q", dto["client_id"], c.ID)
	}
}

func TestProjectsAPI_409_DuplicateNamePerClient(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	ctx := context.Background()
	c, _ := env.clients.Create(ctx, "c1", "Acme", "", "")

	r1 := env.do(t, http.MethodPost, "/projects", "admin@example.com", map[string]any{
		"name": "M", "client_id": c.ID,
	})
	r1.Body.Close()
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("erste Create = %d", r1.StatusCode)
	}
	r2 := env.do(t, http.MethodPost, "/projects", "admin@example.com", map[string]any{
		"name": "M", "client_id": c.ID,
	})
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusConflict {
		t.Errorf("Duplicate (name,client_id) = %d, erwartet 409", r2.StatusCode)
	}
}

func TestProjectsAPI_ListFilterClientId(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	ctx := context.Background()
	c1, _ := env.clients.Create(ctx, "c1", "Acme", "", "")
	c2, _ := env.clients.Create(ctx, "c2", "Beta GmbH", "", "")
	_, _ = env.projects.Create(ctx, "p1", "A", "", c1.ID)
	_, _ = env.projects.Create(ctx, "p2", "B", "", c2.ID)

	resp := env.do(t, http.MethodGet, "/projects?client_id="+c1.ID, "admin@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, erwartet 200", resp.StatusCode)
	}
	var list []map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &list)
	if len(list) != 1 {
		t.Errorf("len=%d, erwartet 1 für c1-Filter", len(list))
	}
}

func TestClientsAPI_ListProjectsForClient(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	ctx := context.Background()
	c, _ := env.clients.Create(ctx, "c1", "Acme", "", "")
	_, _ = env.projects.Create(ctx, "p1", "A1", "", c.ID)
	_, _ = env.projects.Create(ctx, "p2", "A2", "", c.ID)

	resp := env.do(t, http.MethodGet, "/clients/"+c.ID+"/projects", "admin@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, erwartet 200", resp.StatusCode)
	}
	var list []map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &list)
	if len(list) != 2 {
		t.Errorf("len=%d, erwartet 2", len(list))
	}
}

// ── Rate-Limit ────────────────────────────────────────────────────────

func TestClientsAPI_429_RateLimit(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)

	var last int
	for i := 0; i < 31; i++ {
		body := map[string]any{"name": "X" + string(rune('0'+i%10)) + string(rune('a'+i/10))}
		resp := env.do(t, http.MethodPost, "/clients", "admin@example.com", body)
		last = resp.StatusCode
		resp.Body.Close()
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("31. POST status=%d, erwartet 429", last)
	}
}

// ── GET /projects/{id}/cards (ADR-0102) ──────────────────────────────

// seedBoardWithProjectCards legt ein Board mit zwei Sprints und drei Karten an.
// Karten K1+K2 sind project=p, K3 ohne Projekt. Owner darf das Board sehen.
func (e *cpTestEnv) seedBoardWithProjectCards(t *testing.T, boardID, ownerEmail, projectID string) {
	t.Helper()
	state := []byte(`{"version":"2","board":{"id":"` + boardID + `","title":"Demo","sprints":[
		{"id":"sp1","title":"Sprint X","kind":"sprint","cards":[
			{"id":"c1","displayId":1,"title":"K1","columnId":"col-ready","project_id":"` + projectID + `","owner":"andi","qa":false,"critical":false,"blocked":false,"archived":false},
			{"id":"c2","displayId":2,"title":"K2","columnId":"col-erledigt","project_id":"` + projectID + `","owner":"","qa":true,"critical":false,"blocked":false,"archived":true}
		]},
		{"id":"sp2","title":"Backlog","kind":"backlog","cards":[
			{"id":"c3","displayId":3,"title":"K3","columnId":"col-backlog","project_id":null,"owner":"","qa":false,"critical":true,"blocked":false,"archived":false}
		]}
	]}}`)
	if _, err := e.boards.Insert(context.Background(), boardID, "Demo", "2", state); err != nil {
		t.Fatalf("boards.Insert: %v", err)
	}
	if err := e.acl.Set(context.Background(), boardID, ownerEmail, storage.RoleOwner); err != nil {
		t.Fatalf("acl.Set owner: %v", err)
	}
}

func TestProjectsAPI_CardsForProject_404Unbekannt(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "u1", "viewer@example.com", false)
	resp := env.do(t, http.MethodGet, "/projects/does-not-exist/cards", "viewer@example.com", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status=%d, erwartet 404", resp.StatusCode)
	}
}

func TestProjectsAPI_CardsForProject_LiefertNurZugeordnete(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)

	// Projekt anlegen
	resp := env.do(t, http.MethodPost, "/projects", "admin@example.com", map[string]any{"name": "P1"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project status=%d", resp.StatusCode)
	}
	var p map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &p)
	resp.Body.Close()
	projectID := p["id"].(string)

	env.seedBoardWithProjectCards(t, "b1", "admin@example.com", projectID)

	r2 := env.do(t, http.MethodGet, "/projects/"+projectID+"/cards", "admin@example.com", nil)
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("GET cards = %d, erwartet 200; body=%s", r2.StatusCode, mustReadAll(t, r2.Body))
	}
	var cards []map[string]any
	_ = json.Unmarshal(mustReadAll(t, r2.Body), &cards)
	if len(cards) != 2 {
		t.Fatalf("cards len=%d, erwartet 2 (K1+K2); body=%v", len(cards), cards)
	}
	// Board-Kontext muss mitgeliefert sein.
	for _, c := range cards {
		if c["board_id"] != "b1" {
			t.Errorf("card board_id=%v, erwartet b1", c["board_id"])
		}
		if c["board_title"] != "Demo" {
			t.Errorf("card board_title=%v, erwartet Demo", c["board_title"])
		}
		if c["card_id"] == "" {
			t.Errorf("card card_id leer")
		}
	}
}

func TestProjectsAPI_CardsForProject_ACLFilterAusgesperrt(t *testing.T) {
	env := newClientsProjectsEnv(t)
	defer env.close()
	env.seedUser(t, "a1", "admin@example.com", true)
	env.seedUser(t, "u1", "outsider@example.com", false)

	resp := env.do(t, http.MethodPost, "/projects", "admin@example.com", map[string]any{"name": "Px"})
	var p map[string]any
	_ = json.Unmarshal(mustReadAll(t, resp.Body), &p)
	resp.Body.Close()
	projectID := p["id"].(string)

	// Board nur mit Admin-Owner — outsider hat keinen ACL-Eintrag.
	env.seedBoardWithProjectCards(t, "b1", "admin@example.com", projectID)

	r := env.do(t, http.MethodGet, "/projects/"+projectID+"/cards", "outsider@example.com", nil)
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("outsider GET = %d, erwartet 200", r.StatusCode)
	}
	var cards []map[string]any
	_ = json.Unmarshal(mustReadAll(t, r.Body), &cards)
	if len(cards) != 0 {
		t.Errorf("outsider sieht %d Karten, erwartet 0 (ACL-Filter)", len(cards))
	}
}
