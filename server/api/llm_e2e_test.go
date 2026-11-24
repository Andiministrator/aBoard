package api_test

// llm_e2e_test.go — End-to-End-Test #299/ADR-0095 (Etappe 4d).
//
// Chained-Test: REAL HTTP-Server + REAL AnthropicProvider gegen HTTP-Stub
// (Anthropic-API simuliert) + Send → Comment persistiert → Discard →
// Comment entfernt + 2 Activity-Entries. Schließt die Lücke zwischen
// llm_send_test.go (mockProvider direkt, ohne HTTP-Schicht) und
// send_test.go (Provider-HTTP isoliert, ohne Endpoint-Stack).

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestLLM_E2E_SendThenDiscard_FullChain(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()

	// ─── Fake Anthropic API ────────────────────────────────────────────
	// Antwortet auf POST /v1/messages mit einer plausiblen Antwort.
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"missing key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"stop_reason": "end_turn",
			"model": "claude-opus-4-7",
			"content": [{"type":"text","text":"## Antwort\n\nE2E-Hallo"}],
			"usage": {"input_tokens": 77, "output_tokens": 33}
		}`))
	}))
	defer anthropic.Close()

	// ─── Storage + Repos + ACLs ────────────────────────────────────────
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	boards := storage.NewBoardRepo(db)
	users := storage.NewUserRepo(db)
	acl := storage.NewAclRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)

	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, _ := secretcrypto.New(hex.EncodeToString(k))
	keys := storage.NewLLMKeyRepo(db, box)

	// Test-Board mit einer Karte
	state := `{"version":"2","board":{"id":"b1","title":"E2E","sprints":[{"id":"s1","title":"S","cards":[{"id":"c1","displayId":42,"title":"Karte","description":"Was ist die Antwort?","comments":[],"activity":[],"links":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "E2E", "2", []byte(state)); err != nil {
		t.Fatalf("seed board: %v", err)
	}

	// User + Key
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-alice', ?, 'A', '2026-01-01', 0)`, storage.NormalizeEmail("alice@x.de")); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := keys.Set(ctx, "u-alice", "anthropic", "sk-ant-real-test"); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	if err := acl.Set(ctx, "b1", "alice@x.de", storage.RoleEditor); err != nil {
		t.Fatalf("seed acl: %v", err)
	}

	// ─── Provider-Registry mit REAL AnthropicProvider (Endpoint → Fake) ─
	registry := llm.NewRegistry()
	registry.Register(&llm.AnthropicProvider{
		Client:   anthropic.Client(),
		UA:       "aboard-e2e",
		Endpoint: anthropic.URL,
	})

	locks := llm.NewLockManager(0)
	sendAPI := api.NewLLMSendAPI(boards, users, keys, registry, locks)

	mux := http.NewServeMux()
	sendAPI.Register(mux)

	guard := api.NewACLGuard(acl, users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	defer srv.Close()

	doReq := func(method, path string, body any) *http.Response {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rdr = bytes.NewReader(raw)
		}
		r, _ := http.NewRequest(method, srv.URL+path, rdr)
		r.Header.Set("Remote-Email", "alice@x.de")
		r.Header.Set("Remote-User", "alice@x.de")
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatalf("http.Do: %v", err)
		}
		return resp
	}

	// ─── Schritt 1: POST /llm-send ─────────────────────────────────────
	resp := doReq(http.MethodPost, "/boards/b1/cards/c1/llm-send", map[string]any{
		"provider":      "anthropic",
		"model":         "claude-opus-4-7",
		"systemPrompt":  "Antworte knapp.",
		"includeFields": []string{"description"},
	})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("send: Status %d, body=%s", resp.StatusCode, raw)
	}
	var sendBody struct {
		Ok           bool   `json:"ok"`
		CommentID    string `json:"commentId"`
		ReplyText    string `json:"replyText"`
		InputTokens  int    `json:"inputTokens"`
		OutputTokens int    `json:"outputTokens"`
		FinishReason string `json:"finishReason"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&sendBody)
	if !sendBody.Ok || sendBody.CommentID == "" {
		t.Fatalf("send-response: %+v", sendBody)
	}
	if sendBody.ReplyText != "## Antwort\n\nE2E-Hallo" {
		t.Errorf("replyText: %q", sendBody.ReplyText)
	}
	if sendBody.InputTokens != 77 || sendBody.OutputTokens != 33 {
		t.Errorf("tokens: %d/%d", sendBody.InputTokens, sendBody.OutputTokens)
	}
	if sendBody.FinishReason != "end_turn" {
		t.Errorf("finishReason: %q", sendBody.FinishReason)
	}

	// State-Verifikation: Kommentar + Activity persistiert
	b, _ := boards.Get(ctx, "b1")
	var s1 map[string]any
	_ = json.Unmarshal(b.StateJSON, &s1)
	card := s1["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	comments := card["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("nach Send: comments=%d, erwartet 1", len(comments))
	}
	cm := comments[0].(map[string]any)
	if cm["id"].(string) != sendBody.CommentID {
		t.Errorf("commentID-Mismatch: %v vs %v", cm["id"], sendBody.CommentID)
	}
	activity := card["activity"].([]any)
	if len(activity) != 1 || activity[0].(map[string]any)["action"].(string) != "llm-sent" {
		t.Fatalf("nach Send: activity=%+v", activity)
	}

	// ─── Schritt 2: GET /llm-pending (Lock ist released — Send-Defer) ─
	resp = doReq(http.MethodGet, "/boards/b1/cards/c1/llm-pending", nil)
	var pendBody struct {
		Holder map[string]any `json:"holder"`
		IsSelf bool           `json:"isSelf"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&pendBody)
	if pendBody.Holder != nil {
		t.Errorf("pending nach Send-Done: holder=%+v, erwartet null", pendBody.Holder)
	}

	// ─── Schritt 3: POST /llm-discard ──────────────────────────────────
	resp = doReq(http.MethodPost, "/boards/b1/cards/c1/llm-discard", map[string]any{
		"commentId": sendBody.CommentID,
		"provider":  "anthropic",
		"model":     "claude-opus-4-7",
	})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("discard: Status %d, body=%s", resp.StatusCode, raw)
	}

	// State-Verifikation: Kommentar weg, Activity hat 2 Einträge (llm-sent + llm-response-discarded)
	b2, _ := boards.Get(ctx, "b1")
	var s2 map[string]any
	_ = json.Unmarshal(b2.StateJSON, &s2)
	card2 := s2["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	comments2 := card2["comments"].([]any)
	if len(comments2) != 0 {
		t.Errorf("nach Discard: comments=%d, erwartet 0", len(comments2))
	}
	activity2 := card2["activity"].([]any)
	if len(activity2) != 2 {
		t.Fatalf("nach Discard: activity=%d, erwartet 2", len(activity2))
	}
	if activity2[0].(map[string]any)["action"].(string) != "llm-sent" {
		t.Errorf("activity[0]: %v", activity2[0])
	}
	discarded := activity2[1].(map[string]any)
	if discarded["action"].(string) != "llm-response-discarded" {
		t.Errorf("activity[1].action: %v", discarded["action"])
	}
	if discarded["to"].(string) != "anthropic/claude-opus-4-7" {
		t.Errorf("activity[1].to: %v (Audit-Symmetrie verletzt)", discarded["to"])
	}
	meta := discarded["meta"].(map[string]any)
	if meta["commentId"].(string) != sendBody.CommentID {
		t.Errorf("activity[1].meta.commentId: %v", meta["commentId"])
	}

	// ─── Schritt 4: zweiter Discard → 404 (Idempotenz) ─────────────────
	resp = doReq(http.MethodPost, "/boards/b1/cards/c1/llm-discard", map[string]any{
		"commentId": sendBody.CommentID,
		"provider":  "anthropic",
		"model":     "claude-opus-4-7",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("zweiter Discard: %d, erwartet 404", resp.StatusCode)
	}
}

// TestLLM_E2E_Send_InvalidKey_NoCommentPersisted — wenn Anthropic 401 liefert
// (Key vom Provider abgelehnt), darf der Server-Endpoint keinen Kommentar
// anhängen (Audit-Korrektheit). Status 422 invalid-key.
func TestLLM_E2E_Send_InvalidKey_NoCommentPersisted(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()

	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"invalid key"}}`))
	}))
	defer anthropic.Close()

	db, _ := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	defer db.Close()
	boards := storage.NewBoardRepo(db)
	users := storage.NewUserRepo(db)
	acl := storage.NewAclRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, _ := secretcrypto.New(hex.EncodeToString(k))
	keys := storage.NewLLMKeyRepo(db, box)

	state := `{"version":"2","board":{"id":"b1","title":"T","sprints":[{"id":"s1","title":"S","cards":[{"id":"c1","displayId":1,"title":"K","description":"x","comments":[],"activity":[]}]}]}}`
	_, _ = boards.Insert(ctx, "b1", "T", "2", []byte(state))
	_, _ = db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-a', 'a@x.de', 'A', '2026-01-01', 0)`)
	_ = keys.Set(ctx, "u-a", "anthropic", "sk-ant-bad")
	_ = acl.Set(ctx, "b1", "a@x.de", storage.RoleEditor)

	registry := llm.NewRegistry()
	registry.Register(&llm.AnthropicProvider{Client: anthropic.Client(), UA: "t", Endpoint: anthropic.URL})
	locks := llm.NewLockManager(0)
	sendAPI := api.NewLLMSendAPI(boards, users, keys, registry, locks)
	mux := http.NewServeMux()
	sendAPI.Register(mux)
	guard := api.NewACLGuard(acl, users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	defer srv.Close()

	raw, _ := json.Marshal(map[string]any{
		"provider": "anthropic", "model": "claude-opus-4-7", "systemPrompt": "x",
	})
	r, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/boards/b1/cards/c1/llm-send", srv.URL),
		bytes.NewReader(raw))
	r.Header.Set("Remote-Email", "a@x.de")
	r.Header.Set("Remote-User", "a@x.de")
	r.Header.Set("Content-Type", "application/json")
	resp, _ := http.DefaultClient.Do(r)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("Status %d, erwartet 422", resp.StatusCode)
	}

	// Kein Kommentar persistiert
	b, _ := boards.Get(ctx, "b1")
	var s map[string]any
	_ = json.Unmarshal(b.StateJSON, &s)
	card := s["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	comments := card["comments"].([]any)
	if len(comments) != 0 {
		t.Errorf("invalid-key: erwartet 0 Kommentare, got %d", len(comments))
	}
	// Lock muss freigegeben sein (defer Release in handleSend)
	if _, ok := locks.Holder(llm.SperrSchluessel("b1", "c1")); ok {
		t.Error("Lock nach Provider-Error nicht released")
	}
}
