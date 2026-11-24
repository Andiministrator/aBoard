package api_test

// llm_conversations_test.go — End-to-End-Tests für die Conversation-CRUD-Endpoints
// (#300, ADR-0096 §2 Etappe 2). Geht durch ACLGuard + Forward-Auth, prüft
// IDOR-Maskierung, created_by-Schutz, includeArchived-Filter.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type llmConvEnv struct {
	srv     *httptest.Server
	convs   *storage.LLMConversationRepo
	acl     *storage.AclRepo
	boardID string
	cardID  string
}

func newLLMConvEnv(t *testing.T) *llmConvEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	boards := storage.NewBoardRepo(db)
	users := storage.NewUserRepo(db)
	acl := storage.NewAclRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)
	convs := storage.NewLLMConversationRepo(db)

	state := `{"version":"2","board":{"id":"b1","title":"Test","sprints":[{"id":"s1","title":"S","cards":[{"id":"c1","displayId":7,"title":"K","description":"D","comments":[],"activity":[],"links":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "Test", "2", []byte(state)); err != nil {
		t.Fatalf("seed board: %v", err)
	}

	convAPI := api.NewLLMConversationsAPI(convs, users)
	mux := http.NewServeMux()
	convAPI.Register(mux)

	guard := api.NewACLGuard(acl, users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)

	t.Cleanup(func() {
		srv.Close()
		_ = db.Close()
	})

	// Seed-User
	if _, err := db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, 0)`,
		"u-a", storage.NormalizeEmail("a@x.de"), "A", "2026-01-01"); err != nil {
		t.Fatalf("seed user a: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, 0)`,
		"u-b", storage.NormalizeEmail("b@x.de"), "B", "2026-01-01"); err != nil {
		t.Fatalf("seed user b: %v", err)
	}

	return &llmConvEnv{srv: srv, convs: convs, acl: acl, boardID: "b1", cardID: "c1"}
}

func (e *llmConvEnv) grant(t *testing.T, email, role string) {
	t.Helper()
	if err := e.acl.Set(context.Background(), e.boardID, email, role); err != nil {
		t.Fatalf("acl.Set: %v", err)
	}
}

func (e *llmConvEnv) do(t *testing.T, method, path, email string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r, _ := http.NewRequest(method, e.srv.URL+path, rdr)
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

func TestLLMConv_API_CreateAndList(t *testing.T) {
	env := newLLMConvEnv(t)
	env.grant(t, "a@x.de", storage.RoleEditor)

	// Create
	resp := env.do(t, http.MethodPost, "/boards/b1/cards/c1/conversations", "a@x.de",
		map[string]string{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Create-Status %d: %s", resp.StatusCode, raw)
	}
	var created storage.LLMConversation
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.ID == "" || created.CreatedBy != "a@x.de" || created.BoardID != "b1" || created.CardID != "c1" {
		t.Fatalf("Create-Response unerwartet: %+v", created)
	}

	// List
	resp = env.do(t, http.MethodGet, "/boards/b1/cards/c1/conversations", "a@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("List-Status %d", resp.StatusCode)
	}
	var list []storage.LLMConversation
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].ID != created.ID {
		t.Errorf("List unerwartet: %+v", list)
	}
}

func TestLLMConv_API_ListRequiresViewer(t *testing.T) {
	env := newLLMConvEnv(t)
	// Kein ACL-Eintrag → ACLGuard 403
	resp := env.do(t, http.MethodGet, "/boards/b1/cards/c1/conversations", "a@x.de", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Status %d, erwartet 403 (kein ACL)", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLLMConv_API_CreateRequiresEditor(t *testing.T) {
	env := newLLMConvEnv(t)
	env.grant(t, "a@x.de", storage.RoleViewer)
	resp := env.do(t, http.MethodPost, "/boards/b1/cards/c1/conversations", "a@x.de",
		map[string]string{"provider": "anthropic", "model": "m"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Status %d, erwartet 403 (Viewer darf nicht POSTen)", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLLMConv_API_ArchiveAndDelete_NurCreator(t *testing.T) {
	env := newLLMConvEnv(t)
	env.grant(t, "a@x.de", storage.RoleEditor)
	env.grant(t, "b@x.de", storage.RoleEditor)

	// User a legt Conversation an
	resp := env.do(t, http.MethodPost, "/boards/b1/cards/c1/conversations", "a@x.de",
		map[string]string{"provider": "openai", "model": "gpt-4o"})
	var c storage.LLMConversation
	_ = json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()

	// User b versucht zu archivieren → 403
	resp = env.do(t, http.MethodPost, "/boards/b1/cards/c1/conversations/"+c.ID+"/archive", "b@x.de", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Archive durch fremden User: Status %d, erwartet 403", resp.StatusCode)
	}
	resp.Body.Close()

	// User b versucht zu löschen → 403
	resp = env.do(t, http.MethodDelete, "/boards/b1/cards/c1/conversations/"+c.ID, "b@x.de", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Delete durch fremden User: Status %d, erwartet 403", resp.StatusCode)
	}
	resp.Body.Close()

	// User a archiviert
	resp = env.do(t, http.MethodPost, "/boards/b1/cards/c1/conversations/"+c.ID+"/archive", "a@x.de", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("Archive durch Creator: Status %d, erwartet 204", resp.StatusCode)
	}
	resp.Body.Close()

	// List ohne includeArchived → leer
	resp = env.do(t, http.MethodGet, "/boards/b1/cards/c1/conversations", "a@x.de", nil)
	var list []storage.LLMConversation
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 0 {
		t.Errorf("List nach Archive (default) liefert %d, erwartet 0", len(list))
	}

	// List mit includeArchived=true → 1
	resp = env.do(t, http.MethodGet, "/boards/b1/cards/c1/conversations?includeArchived=true", "a@x.de", nil)
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].ArchivedAt == nil {
		t.Errorf("List(includeArchived=true) liefert %+v", list)
	}

	// User a löscht
	resp = env.do(t, http.MethodDelete, "/boards/b1/cards/c1/conversations/"+c.ID, "a@x.de", nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("Delete durch Creator: Status %d, erwartet 204", resp.StatusCode)
	}
	resp.Body.Close()

	// Existiert nicht mehr
	resp = env.do(t, http.MethodGet, "/boards/b1/cards/c1/conversations?includeArchived=true", "a@x.de", nil)
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 0 {
		t.Errorf("List nach Delete liefert %d, erwartet 0", len(list))
	}
}

func TestLLMConv_API_IDOR_FremdeKarteAls404(t *testing.T) {
	env := newLLMConvEnv(t)
	env.grant(t, "a@x.de", storage.RoleEditor)
	// Conversation auf einer "anderen" Karte direkt im Repo anlegen — kein
	// ACL-Pfad, simuliert einen anderen Karten-Slot im selben Board.
	_, err := env.convs.CreateConversation(context.Background(), storage.CreateConversationParams{
		ID: "conv-other", BoardID: "b1", CardID: "c-other", Provider: "anthropic", Model: "m", CreatedBy: "a@x.de",
	})
	if err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	// User a fragt sie unter /cards/c1/... an → 404 (cross-card-Maskierung)
	resp := env.do(t, http.MethodGet, "/boards/b1/cards/c1/conversations/conv-other/turns", "a@x.de", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Cross-Card-IDOR: Status %d, erwartet 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLLMConv_API_ListTurns_LeereListeOK(t *testing.T) {
	env := newLLMConvEnv(t)
	env.grant(t, "a@x.de", storage.RoleEditor)
	resp := env.do(t, http.MethodPost, "/boards/b1/cards/c1/conversations", "a@x.de",
		map[string]string{"provider": "google", "model": "gemini-1.5-pro"})
	var c storage.LLMConversation
	_ = json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()

	resp = env.do(t, http.MethodGet, "/boards/b1/cards/c1/conversations/"+c.ID+"/turns", "a@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if got := string(bytes.TrimSpace(raw)); got != "[]" {
		t.Errorf("Body: %q, erwartet '[]' für leere Conversation", got)
	}
}
