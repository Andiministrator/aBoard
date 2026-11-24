package api_test

// spam_test.go — POST /mail-spam-report (#294 Etappe 3, ADR-0092 §5).
//
// Geprüfte Szenarien:
//   - Editor am Service-Board darf adden / undo-en (Round-Trip).
//   - Viewer / no-ACL → 403.
//   - Unauthentifizierter Request (kein Forward-Auth-Header) → 403.
//   - Ungültiges JSON / leere Email → 400.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type spamEnv struct {
	srv  *httptest.Server
	spam *storage.SpamRepo
}

func newSpamEnv(t *testing.T) *spamEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	users := storage.NewUserRepo(db)
	acl := storage.NewAclRepo(db)
	spam := storage.NewSpamRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)
	boards := storage.NewBoardRepo(db)

	// Service-Board minimal anlegen (für FK aus acl.board_id).
	state := storage.CanonicalServiceBoardState(storage.ServiceBoardID, "Service")
	if _, err := boards.Insert(ctx, storage.ServiceBoardID, "Service", "2", state); err != nil {
		t.Fatalf("seed service-board: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES
		 ('u-ed','editor@example.org','Ed','2026-01-01',0),
		 ('u-vw','viewer@example.org','Vw','2026-01-01',0)`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if err := acl.Set(ctx, storage.ServiceBoardID, "editor@example.org", storage.RoleEditor); err != nil {
		t.Fatalf("acl editor: %v", err)
	}
	if err := acl.Set(ctx, storage.ServiceBoardID, "viewer@example.org", storage.RoleViewer); err != nil {
		t.Fatalf("acl viewer: %v", err)
	}

	spamAPI := api.NewSpamAPI(spam, acl, users)
	mux := http.NewServeMux()
	spamAPI.Register(mux)
	guard := api.NewACLGuard(acl, users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return &spamEnv{srv: srv, spam: spam}
}

func (e *spamEnv) do(t *testing.T, email string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/mail-spam-report", rdr)
	if email != "" {
		r.Header.Set("Remote-Email", email)
		r.Header.Set("Remote-User", email)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func TestSpam_EditorRoundtrip(t *testing.T) {
	env := newSpamEnv(t)
	ctx := context.Background()

	// Add
	resp := env.do(t, "editor@example.org", map[string]any{"senderEmail": "Bad@Example.com"})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Add status %d: %s", resp.StatusCode, raw)
	}
	listed, _ := env.spam.SpamSenderListed(ctx, "bad@example.com")
	if !listed {
		t.Errorf("Sender nicht gelistet nach Add")
	}

	// Undo
	resp = env.do(t, "editor@example.org", map[string]any{"senderEmail": "bad@example.com", "undo": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Undo status %d", resp.StatusCode)
	}
	listed, _ = env.spam.SpamSenderListed(ctx, "bad@example.com")
	if listed {
		t.Errorf("Sender immer noch gelistet nach Undo")
	}
}

func TestSpam_ViewerForbidden(t *testing.T) {
	env := newSpamEnv(t)
	resp := env.do(t, "viewer@example.org", map[string]any{"senderEmail": "spam@example.com"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Viewer Status = %d, erwartet 403", resp.StatusCode)
	}
}

// Seit ADR-0129 §2 (F-1124) antwortet die Forward-Auth-Middleware ohne
// Identität mit 401, bevor ACLGuard und Handler laufen.
func TestSpam_Unauthenticated401(t *testing.T) {
	env := newSpamEnv(t)
	resp := env.do(t, "", map[string]any{"senderEmail": "spam@example.com"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Unauth Status = %d, erwartet 401", resp.StatusCode)
	}
}

func TestSpam_InvalidEmail(t *testing.T) {
	env := newSpamEnv(t)
	for _, bad := range []string{"", "not-an-email", "@", "x@"} {
		resp := env.do(t, "editor@example.org", map[string]any{"senderEmail": bad})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("senderEmail=%q Status = %d, erwartet 400", bad, resp.StatusCode)
		}
	}
}

func TestSpam_InvalidJSON(t *testing.T) {
	env := newSpamEnv(t)
	r, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/mail-spam-report", strings.NewReader("{ broken"))
	r.Header.Set("Remote-Email", "editor@example.org")
	r.Header.Set("Remote-User", "editor@example.org")
	r.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(r)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Invalid-JSON Status = %d, erwartet 400", resp.StatusCode)
	}
}
