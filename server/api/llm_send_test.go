package api_test

// llm_send_test.go — Tests für POST /boards/.../llm-send + GET /llm/pricing
// (#299, ADR-0095 Etappe 2b).
//
// Stub-Provider implementiert llm.Provider direkt im Test (kein HTTP-Stub
// nötig) — voller Kontrolle über Result/Error/Pricing.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// ─── Mock-Provider ───────────────────────────────────────────────────────

type mockProvider struct {
	name           string
	sendResult     llm.SendResult
	sendErr        error
	calls          atomic.Int32
	inPM           float64
	outPM          float64
	lastUserPrompt string // letzten UserPrompt mitschneiden (für Override-Tests, #301)
	lastSystem     string
	sendDelay      time.Duration // simulierte Antwortzeit des Anbieters (#544-Nacharbeit)
	pruefeKontext  func(context.Context)
}

func (m *mockProvider) Name() string                          { return m.name }
func (m *mockProvider) TestKey(context.Context, string) error { return nil }
func (m *mockProvider) Pricing(string) (float64, float64)     { return m.inPM, m.outPM }
func (m *mockProvider) Send(ctx context.Context, _ string, req llm.SendRequest) (llm.SendResult, error) {
	m.calls.Add(1)
	if m.pruefeKontext != nil {
		m.pruefeKontext(ctx)
	}
	if m.sendDelay > 0 {
		select {
		case <-time.After(m.sendDelay):
		case <-ctx.Done():
			return llm.SendResult{}, ctx.Err()
		}
	}
	m.lastUserPrompt = req.UserPrompt
	m.lastSystem = req.System
	if m.sendErr != nil {
		return llm.SendResult{}, m.sendErr
	}
	return m.sendResult, nil
}

// ─── Test-Environment ────────────────────────────────────────────────────

type llmSendEnv struct {
	srv      *httptest.Server
	db       *sql.DB
	boards   *storage.BoardRepo
	users    *storage.UserRepo
	keys     *storage.LLMKeyRepo
	acl      *storage.AclRepo
	provider *mockProvider
	locks    *llm.LockManager
	boardID  string
	cardID   string
}

func newLLMSendEnv(t *testing.T) *llmSendEnv {
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

	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, _ := secretcrypto.New(hex.EncodeToString(k))
	keys := storage.NewLLMKeyRepo(db, box)

	// Test-Board mit einer Karte.
	state := `{"version":"2","board":{"id":"b1","title":"Test","sprints":[{"id":"s1","title":"S","cards":[{"id":"c1","displayId":42,"title":"Karte","description":"Hi","comments":[],"activity":[],"links":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "Test", "2", []byte(state)); err != nil {
		t.Fatalf("seed board: %v", err)
	}

	// Mock-Provider in eigene Registry.
	mp := &mockProvider{
		name: "anthropic",
		sendResult: llm.SendResult{
			Text: "## Antwort\n\nHallo Welt", InputTokens: 100, OutputTokens: 50,
			Model: "claude-opus-4-7", FinishReason: "end_turn",
		},
		inPM: 15.0, outPM: 75.0,
	}
	registry := llm.NewRegistry()
	registry.Register(mp)

	locks := llm.NewLockManager(0)
	sendAPI := api.NewLLMSendAPI(boards, users, keys, registry, locks)

	mux := http.NewServeMux()
	sendAPI.Register(mux)

	guard := api.NewACLGuard(acl, users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)

	t.Cleanup(func() {
		srv.Close()
		_ = db.Close()
	})

	return &llmSendEnv{
		srv: srv, db: db, boards: boards, users: users, keys: keys, acl: acl,
		provider: mp, locks: locks, boardID: "b1", cardID: "c1",
	}
}

func (e *llmSendEnv) seedUserWithKey(t *testing.T, userID, email, apiKey string) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, 0)`,
		userID, storage.NormalizeEmail(email), "N", "2026-01-01"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if apiKey != "" {
		if err := e.keys.Set(ctx, userID, "anthropic", apiKey); err != nil {
			t.Fatalf("seed key: %v", err)
		}
	}
}

func (e *llmSendEnv) grant(t *testing.T, email, role string) {
	t.Helper()
	if err := e.acl.Set(context.Background(), e.boardID, email, role); err != nil {
		t.Fatalf("seed acl: %v", err)
	}
}

func (e *llmSendEnv) do(t *testing.T, method, path, email string, body any) *http.Response {
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

func sendPath(boardID, cardID string) string {
	return fmt.Sprintf("/boards/%s/cards/%s/llm-send", boardID, cardID)
}

func discardPath(boardID, cardID string) string {
	return fmt.Sprintf("/boards/%s/cards/%s/llm-discard", boardID, cardID)
}

// ─── Tests ───────────────────────────────────────────────────────────────

func TestLLMSend_OK_PersistsCommentAndActivity(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider":      "anthropic",
			"model":         "claude-opus-4-7",
			"systemPrompt":  "Hilf knapp.",
			"includeFields": []string{"description"},
		})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status %d: %s", resp.StatusCode, raw)
	}
	var body struct {
		Ok              bool    `json:"ok"`
		CommentID       string  `json:"commentId"`
		ReplyText       string  `json:"replyText"`
		InputTokens     int     `json:"inputTokens"`
		OutputTokens    int     `json:"outputTokens"`
		CostEstimateUSD float64 `json:"costEstimateUSD"`
		FinishReason    string  `json:"finishReason"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !body.Ok || body.CommentID == "" {
		t.Errorf("Response: %+v", body)
	}
	if body.ReplyText != "## Antwort\n\nHallo Welt" {
		t.Errorf("ReplyText: %q", body.ReplyText)
	}
	if body.InputTokens != 100 || body.OutputTokens != 50 {
		t.Errorf("Tokens: %d/%d", body.InputTokens, body.OutputTokens)
	}
	// Cost: 100/1M * 15 + 50/1M * 75 = 0.0015 + 0.00375 = 0.00525
	wantCost := (100.0/1_000_000)*15.0 + (50.0/1_000_000)*75.0
	if body.CostEstimateUSD < wantCost*0.99 || body.CostEstimateUSD > wantCost*1.01 {
		t.Errorf("CostEstimateUSD: %v, erwartet ~%v", body.CostEstimateUSD, wantCost)
	}
	if body.FinishReason != "end_turn" {
		t.Errorf("FinishReason: %q", body.FinishReason)
	}

	// State persistiert: Kommentar + Activity-Entry
	b, _ := env.boards.Get(context.Background(), env.boardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	card := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	comments := card["comments"].([]any)
	if len(comments) != 1 {
		t.Fatalf("Comments: %d, erwartet 1", len(comments))
	}
	cm := comments[0].(map[string]any)
	if cm["id"].(string) != body.CommentID {
		t.Errorf("CommentID-Mismatch")
	}
	if cm["text"].(string) != "## Antwort\n\nHallo Welt" {
		t.Errorf("Comment.text: %q", cm["text"])
	}
	if author := cm["author"].(string); author == "" {
		t.Errorf("Comment.author leer")
	}
	activity := card["activity"].([]any)
	if len(activity) != 1 {
		t.Fatalf("Activity: %d", len(activity))
	}
	a := activity[0].(map[string]any)
	if a["action"].(string) != "llm-sent" {
		t.Errorf("action: %v", a["action"])
	}
	if a["to"].(string) != "anthropic/claude-opus-4-7" {
		t.Errorf("to: %v", a["to"])
	}
	meta := a["meta"].(map[string]any)
	if int(meta["inputTokens"].(float64)) != 100 {
		t.Errorf("meta.inputTokens: %v", meta["inputTokens"])
	}
	if meta["contextNote"].(string) != "Beschreibung" {
		t.Errorf("meta.contextNote: %v", meta["contextNote"])
	}

	if env.provider.calls.Load() != 1 {
		t.Errorf("Provider-Send-Calls: %d, erwartet 1", env.provider.calls.Load())
	}
}

func TestLLMSend_Locked_BlocksSecondSend(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	// Lock manuell setzen (simuliert laufenden Send).
	_, ok, _ := env.locks.Acquire(llm.SperrSchluessel(env.boardID, env.cardID), "u-other", "openai", "gpt-4o")
	if !ok {
		t.Fatal("Lock-Setup gescheitert")
	}

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusLocked {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("erwartet 423 Locked, got %d (%s)", resp.StatusCode, raw)
	}
	var body struct {
		Kind   string `json:"kind"`
		Holder struct {
			User     string `json:"user"`
			Provider string `json:"provider"`
		} `json:"holder"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Kind != "locked" {
		t.Errorf("kind: %q", body.Kind)
	}
	if body.Holder.User != "u-other" || body.Holder.Provider != "openai" {
		t.Errorf("Holder: %+v", body.Holder)
	}
	if env.provider.calls.Load() != 0 {
		t.Errorf("Provider darf bei Lock-Konflikt NICHT gerufen werden: %d", env.provider.calls.Load())
	}
}

func TestLLMSend_LockReleasedAfterSuccess(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	// Erster Send: OK
	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("1. Send: %d", resp.StatusCode)
	}
	// Lock muss freigegeben sein.
	if _, held := env.locks.Holder(llm.SperrSchluessel(env.boardID, env.cardID)); held {
		t.Errorf("Lock noch gehalten nach erfolgreichem Send")
	}
	// Zweiter Send sollte ebenfalls 200 sein.
	resp = env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("2. Send: %d", resp.StatusCode)
	}
}

func TestLLMSend_LockReleasedAfterProviderError(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	env.provider.sendErr = llm.ErrProviderUnreachable

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("erwartet 502 Bad Gateway, got %d", resp.StatusCode)
	}
	if _, held := env.locks.Holder(llm.SperrSchluessel(env.boardID, env.cardID)); held {
		t.Errorf("Lock muss auch nach Provider-Fehler freigegeben werden (defer Release)")
	}
}

func TestLLMSend_InvalidKey_422(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	env.provider.sendErr = llm.ErrInvalidKey

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("erwartet 422, got %d", resp.StatusCode)
	}
	var body struct {
		Kind string `json:"kind"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Kind != "invalid-key" {
		t.Errorf("kind: %q", body.Kind)
	}
}

func TestLLMSend_NoStoredKey_412(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "")
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("erwartet 412 ohne gespeicherten Key, got %d", resp.StatusCode)
	}
	var body struct {
		Kind string `json:"kind"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Kind != "invalid-key" {
		t.Errorf("kind: %q", body.Kind)
	}
}

func TestLLMSend_ACL_Viewer_Forbidden(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-v", "viewer@x.de", "sk-ant")
	env.grant(t, "viewer@x.de", storage.RoleViewer)

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "viewer@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("erwartet 403 für viewer (editor+ verlangt), got %d", resp.StatusCode)
	}
	if env.provider.calls.Load() != 0 {
		t.Errorf("Provider darf bei ACL-Block NICHT gerufen werden")
	}
}

func TestLLMSend_ACL_NoEntry_Forbidden(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-x", "x@x.de", "sk-ant")
	// Kein grant für x@x.de

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "x@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("erwartet 403 ohne ACL-Eintrag, got %d", resp.StatusCode)
	}
}

func TestLLMSend_RequiresAuthentication(t *testing.T) {
	env := newLLMSendEnv(t)
	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	// Forward-Auth ohne Identity → 401 aus der Middleware (ADR-0129 §2,
	// F-1124). Bis Sprint 70: ACL-Bypass (Legacy), dann 403 im Handler.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("ohne Identity: erwartet 401, got %d", resp.StatusCode)
	}
}

func TestLLMSend_UnknownProvider_400(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "openai", "model": "gpt-4o"}) // openai nicht registriert
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("erwartet 400 für unbekannten Provider, got %d", resp.StatusCode)
	}
}

func TestLLMSend_MissingProviderModel_400(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic"}) // model fehlt
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("erwartet 400 ohne model, got %d", resp.StatusCode)
	}
}

// #301/ADR-0097 — Wenn userPromptOverride nicht-leer ist, nimmt der Server
// ihn statt BuildPrompt. Erlaubt dem Client (PII-Preview-Modal), den
// gefilterten Prompt zu senden.
func TestLLMSend_UserPromptOverride_BypassesBuildPrompt(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	override := "# Karte: redactet\n\n## Beschreibung\n\n<REDACTED> und [E-Mail #1]\n"
	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider":           "anthropic",
			"model":              "claude-opus-4-7",
			"systemPrompt":       "Antworte knapp.",
			"includeFields":      []string{"description"},
			"userPromptOverride": override,
		})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status %d: %s", resp.StatusCode, raw)
	}
	if env.provider.lastUserPrompt != override {
		t.Errorf("Provider erhielt UserPrompt %q, erwartet Override %q", env.provider.lastUserPrompt, override)
	}
}

// Kritiker-Fix S-P1: bei aktivem Override markiert contextNote den Override-Pfad,
// damit der Audit-Trail nicht heimlich auf IncludeFields zeigt, während der
// echte Inhalt aus dem Client-Override kam.
func TestLLMSend_UserPromptOverride_MarksAuditTrail(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	override := "## Beschreibung\n\n<REDACTED>\n"
	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider":           "anthropic",
			"model":              "claude-opus-4-7",
			"systemPrompt":       "x",
			"includeFields":      []string{"description"},
			"userPromptOverride": override,
		})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status %d: %s", resp.StatusCode, raw)
	}
	// Activity laden — contextNote muss "Client-Override" enthalten.
	b, _ := env.boards.Get(context.Background(), env.boardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	card := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	activity := card["activity"].([]any)
	if len(activity) == 0 {
		t.Fatal("kein Activity-Eintrag persistiert")
	}
	last := activity[len(activity)-1].(map[string]any)
	meta := last["meta"].(map[string]any)
	note, _ := meta["contextNote"].(string)
	if !strings.Contains(note, "Client-Override") {
		t.Errorf("contextNote markiert Override nicht: %q", note)
	}
	if !strings.Contains(note, fmt.Sprintf("%d B", len(override))) {
		t.Errorf("contextNote enthält Override-Länge nicht: %q", note)
	}
}

// Gegenprobe: ohne Override greift weiterhin BuildPrompt (Default-Pfad).
func TestLLMSend_NoOverride_UsesBuildPrompt(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider":      "anthropic",
			"model":         "claude-opus-4-7",
			"systemPrompt":  "x",
			"includeFields": []string{"description"},
		})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status %d: %s", resp.StatusCode, raw)
	}
	if env.provider.lastUserPrompt == "" {
		t.Errorf("UserPrompt war leer, erwartet BuildPrompt-Output")
	}
	// BuildPrompt erzeugt mindestens den Header.
	if !strings.Contains(env.provider.lastUserPrompt, "# Karte") {
		t.Errorf("UserPrompt enthält keinen Header: %q", env.provider.lastUserPrompt)
	}
}

func TestLLMSend_CardNotFound_404(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, "ghost-card"), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("erwartet 404, got %d", resp.StatusCode)
	}
}

func TestLLMSend_KeysDisabled_503(t *testing.T) {
	// LLMSendAPI mit keys=nil → 503
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	boards := storage.NewBoardRepo(db)
	users := storage.NewUserRepo(db)
	acl := storage.NewAclRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)
	if _, err := boards.Insert(ctx, "b1", "T", "2", []byte(`{"version":"2","board":{"id":"b1","title":"T","sprints":[{"id":"s1","title":"S","cards":[{"id":"c1","title":"x","comments":[],"activity":[]}]}]}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u','a@x.de','N','2026-01-01',0)`); err != nil {
		t.Fatal(err)
	}
	_ = acl.Set(ctx, "b1", "a@x.de", storage.RoleEditor)

	reg := llm.NewRegistry()
	reg.Register(&mockProvider{name: "anthropic"})
	sendAPI := api.NewLLMSendAPI(boards, users, nil /* keys nil */, reg, llm.NewLockManager(0))

	mux := http.NewServeMux()
	sendAPI.Register(mux)
	guard := api.NewACLGuard(acl, users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	defer srv.Close()

	r, _ := http.NewRequest(http.MethodPost, srv.URL+"/boards/b1/cards/c1/llm-send",
		bytes.NewReader([]byte(`{"provider":"anthropic","model":"claude-opus-4-7"}`)))
	r.Header.Set("Remote-Email", "a@x.de")
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("keys=nil: erwartet 503, got %d", resp.StatusCode)
	}
}

func TestLLMSend_BodyCap_AppliedToReply(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	// Antwort > 256 KB
	huge := make([]byte, api.MaxLLMReplyBodyBytes+1024)
	for i := range huge {
		huge[i] = 'x'
	}
	env.provider.sendResult = llm.SendResult{Text: string(huge), InputTokens: 1, OutputTokens: 1, Model: "claude-opus-4-7"}

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d", resp.StatusCode)
	}
	b, _ := env.boards.Get(context.Background(), env.boardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	card := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	text := card["comments"].([]any)[0].(map[string]any)["text"].(string)
	if len(text) > api.MaxLLMReplyBodyBytes+50 /* Truncation-Suffix */ {
		t.Errorf("Reply nicht gekappt: %d Bytes", len(text))
	}
	// Etappe 3: replyText im Response ist identisch zum persistierten Body (auch gecappt).
	var body struct {
		ReplyText string `json:"replyText"`
	}
	resp2 := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	_ = json.NewDecoder(resp2.Body).Decode(&body)
	if len(body.ReplyText) > api.MaxLLMReplyBodyBytes+50 {
		t.Errorf("Response.replyText nicht gekappt: %d Bytes", len(body.ReplyText))
	}
}

// ─── Pricing-Endpoint ────────────────────────────────────────────────────

func TestLLMPricing_OK(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "")
	resp := env.do(t, http.MethodGet, "/llm/pricing", "a@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d", resp.StatusCode)
	}
	// Seit F-858 steht neben den Anbietern das Feld `limits` auf oberster
	// Ebene — deshalb erst roh, dann je Anbieter dekodieren.
	var roh map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&roh); err != nil {
		t.Fatalf("Antwort nicht dekodierbar: %v", err)
	}
	body := map[string]map[string]map[string]any{}
	for _, p := range []string{"anthropic", "openai", "google", "mistral"} {
		r, ok := roh[p]
		if !ok {
			t.Errorf("Provider %q fehlt in /llm/pricing", p)
			continue
		}
		var tabelle map[string]map[string]any
		if err := json.Unmarshal(r, &tabelle); err != nil {
			t.Fatalf("Tabelle %q nicht dekodierbar: %v", p, err)
		}
		body[p] = tabelle
	}
	opus, ok := body["anthropic"]["claude-opus-4-7"]
	if !ok {
		t.Fatalf("anthropic.claude-opus-4-7 fehlt")
	}
	if opus["InputPerMillionUSD"] != 15.0 || opus["OutputPerMillionUSD"] != 75.0 {
		t.Errorf("Opus-Preise: %+v", opus)
	}
}

// F-858: Die drei Bild-Grenzen kommen über GET /llm/pricing im Feld `limits`
// — Vertrag mit dem Frontend (src/llm-tab.js): genau die Schlüssel
// maxImageBytes, maxImages, maxImageBytesTotal, als ganze Zahlen, mit den
// Werten, die der Server beim Senden tatsächlich durchsetzt.
func TestLLMPricing_LiefertBildGrenzen(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "")
	resp := env.do(t, http.MethodGet, "/llm/pricing", "a@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d", resp.StatusCode)
	}
	var roh map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&roh); err != nil {
		t.Fatalf("Antwort nicht dekodierbar: %v", err)
	}
	r, ok := roh["limits"]
	if !ok {
		t.Fatalf("Feld limits fehlt: Schluessel %v", func() []string {
			var k []string
			for s := range roh {
				k = append(k, s)
			}
			return k
		}())
	}
	var limits map[string]json.Number
	dec := json.NewDecoder(bytes.NewReader(r))
	dec.UseNumber()
	if err := dec.Decode(&limits); err != nil {
		t.Fatalf("limits nicht dekodierbar: %v (%s)", err, r)
	}
	if len(limits) != 3 {
		t.Fatalf("limits hat %d Schluessel, Vertrag sind genau 3: %s", len(limits), r)
	}
	// Die Werte werden gegen die Konstanten geprüft, die ladeBilder beim
	// Senden durchsetzt — geprüft ist die Verdrahtung, nicht die Zahl.
	erwartet := map[string]int64{
		"maxImageBytes":      llm.MaxImageBytes,
		"maxImages":          llm.MaxImagesPerSend,
		"maxImageBytesTotal": llm.MaxImageBytesTotal,
	}
	for k, want := range erwartet {
		v, ok := limits[k]
		if !ok {
			t.Errorf("limits.%s fehlt: %s", k, r)
			continue
		}
		got, err := v.Int64()
		if err != nil {
			t.Errorf("limits.%s ist keine ganze Zahl: %q", k, v)
			continue
		}
		if got != want {
			t.Errorf("limits.%s = %d, erwartet %d", k, got, want)
		}
	}
}

// ─── Discard-Endpoint (Etappe 4) ─────────────────────────────────────────

// seedCardWithLLMComment hängt einen LLM-Kommentar mit gegebener CommentID
// und einen passenden llm-sent-Activity-Entry an die Test-Karte (für Discard-
// Tests).
func (e *llmSendEnv) seedCardWithLLMComment(t *testing.T, commentID, provider, model string) {
	t.Helper()
	ctx := context.Background()
	b, err := e.boards.Get(ctx, e.boardID)
	if err != nil {
		t.Fatalf("seedCardWithLLMComment.Get: %v", err)
	}
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	card := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	comments, _ := card["comments"].([]any)
	card["comments"] = append(comments, map[string]any{
		"id":      commentID,
		"author":  "🤖 " + provider + "/" + model + " (im Auftrag von a@x.de)", // Server-Label (ADR-0136 Nachtrag §5)
		"text":    "## Antwort\n\nHallo",
		"created": "2026-05-25T10:00:00Z",
	})
	activity, _ := card["activity"].([]any)
	card["activity"] = append(activity, map[string]any{
		"ts": "2026-05-25T10:00:00Z", "actor": "a@x.de", "action": "llm-sent",
		"to":   provider + "/" + model,
		"meta": map[string]any{"commentId": commentID, "inputTokens": 100, "outputTokens": 50},
	})
	newJSON, _ := json.Marshal(state)
	if _, err := e.boards.Update(ctx, b.ID, b.Title, b.Version, newJSON, b.UpdatedAt); err != nil {
		t.Fatalf("seedCardWithLLMComment.Update: %v", err)
	}
}

func TestLLMDiscard_OK_RemovesCommentAndAppendsActivity(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	commentID := "cm-discard-1"
	env.seedCardWithLLMComment(t, commentID, "anthropic", "claude-opus-4-7")

	resp := env.do(t, http.MethodPost, discardPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"commentId": commentID,
			"provider":  "anthropic",
			"model":     "claude-opus-4-7",
		})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status %d: %s", resp.StatusCode, raw)
	}

	// State: Kommentar entfernt + Activity-Entry mit action='llm-response-discarded' angehängt
	b, _ := env.boards.Get(context.Background(), env.boardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	card := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	comments := card["comments"].([]any)
	for _, c := range comments {
		cm := c.(map[string]any)
		if cm["id"] == commentID {
			t.Errorf("Kommentar nicht entfernt: %+v", cm)
		}
	}
	activity := card["activity"].([]any)
	// llm-sent + llm-response-discarded = 2 Einträge
	if len(activity) != 2 {
		t.Fatalf("Activity-Länge: %d (erwartet 2)", len(activity))
	}
	discarded := activity[1].(map[string]any)
	if discarded["action"].(string) != "llm-response-discarded" {
		t.Errorf("Discard-Action: %v", discarded["action"])
	}
	if discarded["to"].(string) != "anthropic/claude-opus-4-7" {
		t.Errorf("Discard-To: %v", discarded["to"])
	}
	meta := discarded["meta"].(map[string]any)
	if meta["commentId"].(string) != commentID {
		t.Errorf("Discard-Meta-CommentID: %v", meta["commentId"])
	}
}

func TestLLMDiscard_NotFound_When_CommentMissing(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	// KEIN seedCardWithLLMComment — Kommentar existiert nie

	resp := env.do(t, http.MethodPost, discardPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"commentId": "cm-nicht-da", "provider": "anthropic", "model": "claude-opus-4-7",
		})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Status %d, erwartet 404", resp.StatusCode)
	}
}

func TestLLMDiscard_BadRequest_When_CommentIDMissing(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, discardPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Status %d, erwartet 400", resp.StatusCode)
	}
}

func TestLLMDiscard_Forbidden_When_NotEditor(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleViewer) // nur viewer

	resp := env.do(t, http.MethodPost, discardPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"commentId": "x", "provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Status %d, erwartet 403", resp.StatusCode)
	}
}

func TestLLMDiscard_Idempotent_SecondCall404(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	commentID := "cm-1"
	env.seedCardWithLLMComment(t, commentID, "openai", "gpt-4o")

	// Erster Discard: 200 OK
	r1 := env.do(t, http.MethodPost, discardPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"commentId": commentID, "provider": "openai", "model": "gpt-4o"})
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("erster Discard: %d", r1.StatusCode)
	}
	// Zweiter Discard mit derselben commentId: 404 (Kommentar ist weg)
	r2 := env.do(t, http.MethodPost, discardPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"commentId": commentID, "provider": "openai", "model": "gpt-4o"})
	if r2.StatusCode != http.StatusNotFound {
		t.Errorf("zweiter Discard: %d, erwartet 404", r2.StatusCode)
	}
}

// ─── Pending-Endpoint (Etappe 4) ─────────────────────────────────────────

func pendingPath(boardID, cardID string) string {
	return fmt.Sprintf("/boards/%s/cards/%s/llm-pending", boardID, cardID)
}

func TestLLMPending_NoLock_ReturnsNullHolder(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "")
	env.grant(t, "a@x.de", storage.RoleViewer) // viewer reicht für GET

	resp := env.do(t, http.MethodGet, pendingPath(env.boardID, env.cardID), "a@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d", resp.StatusCode)
	}
	var body struct {
		Holder map[string]any `json:"holder"`
		IsSelf bool           `json:"isSelf"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Holder != nil {
		t.Errorf("erwartet holder=null, got %+v", body.Holder)
	}
	if body.IsSelf {
		t.Errorf("erwartet isSelf=false")
	}
}

func TestLLMPending_OtherUserLock_HolderEmailMapped_IsSelfFalse(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-alice", "alice@x.de", "")
	env.seedUserWithKey(t, "u-bob", "bob@x.de", "")
	env.grant(t, "bob@x.de", storage.RoleViewer)
	// alice hält den Lock, bob fragt ab
	env.locks.Acquire(llm.SperrSchluessel(env.boardID, env.cardID), "u-alice", "anthropic", "claude-opus-4-7")

	resp := env.do(t, http.MethodGet, pendingPath(env.boardID, env.cardID), "bob@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d", resp.StatusCode)
	}
	var body struct {
		Holder map[string]any `json:"holder"`
		IsSelf bool           `json:"isSelf"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Holder == nil {
		t.Fatalf("erwartet holder ≠ null")
	}
	// User-ID → Email-Mapping
	if body.Holder["user"].(string) != "alice@x.de" {
		t.Errorf("Holder-User: %v, erwartet alice@x.de", body.Holder["user"])
	}
	if body.Holder["provider"].(string) != "anthropic" {
		t.Errorf("Holder-Provider: %v", body.Holder["provider"])
	}
	if body.Holder["model"].(string) != "claude-opus-4-7" {
		t.Errorf("Holder-Model: %v", body.Holder["model"])
	}
	if body.IsSelf {
		t.Errorf("erwartet isSelf=false (bob fragt fremden Lock ab)")
	}
}

func TestLLMPending_SelfLock_IsSelfTrue(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-alice", "alice@x.de", "")
	env.grant(t, "alice@x.de", storage.RoleViewer)
	env.locks.Acquire(llm.SperrSchluessel(env.boardID, env.cardID), "u-alice", "openai", "gpt-4o")

	resp := env.do(t, http.MethodGet, pendingPath(env.boardID, env.cardID), "alice@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d", resp.StatusCode)
	}
	var body struct {
		IsSelf bool `json:"isSelf"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !body.IsSelf {
		t.Errorf("erwartet isSelf=true")
	}
}

func TestLLMPending_Forbidden_When_NoBoardAccess(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-alice", "alice@x.de", "")
	// KEIN grant für alice → ACL-Guard sollte 403 liefern

	resp := env.do(t, http.MethodGet, pendingPath(env.boardID, env.cardID), "alice@x.de", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Status %d, erwartet 403", resp.StatusCode)
	}
}

// TestLLMSend_UeberlebtServerSchreibfrist — #544-Nacharbeit: Die Antwort
// eines nicht gestreamten Aufrufs, der länger braucht als der WriteTimeout
// des http.Servers (30 s in main.go, hier 300 ms stellvertretend), kommt
// trotzdem an. Ohne die verlängerte Schreibfrist im Handler bricht die
// Verbindung beim Schreiben ab (EOF).
func TestLLMSend_UeberlebtServerSchreibfrist(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	env.provider.sendDelay = 700 * time.Millisecond

	handler := env.srv.Config.Handler
	env.srv.Close()
	srv := httptest.NewUnstartedServer(handler)
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	env.srv = srv

	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status %d: %s", resp.StatusCode, raw)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Antwort abgeschnitten: %v", err)
	}
	if !strings.Contains(string(raw), "Hallo Welt") {
		t.Fatalf("Antworttext fehlt: %s", raw)
	}
}

// TestLLMSend_GesamtfristGreift — Kritiker Sprint 70, K-A15 (G3): Ein
// Anbieter, der nicht antwortet, hält den Send-Aufruf höchstens
// llmSendTimeout fest. Der Test prüft, dass der Kontext, den der Anbieter
// bekommt, eine Deadline in dieser Grössenordnung trägt.
func TestLLMSend_GesamtfristGreift(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	var rest time.Duration
	env.provider.pruefeKontext = func(ctx context.Context) {
		if d, ok := ctx.Deadline(); ok {
			rest = time.Until(d)
		}
	}
	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	resp.Body.Close()
	if rest <= 0 || rest > 2*time.Minute+time.Second {
		t.Fatalf("Restfrist beim Anbieter = %v, erwartet knapp unter 2 min (llmSendTimeout)", rest)
	}
}

// F-1538: Die Kartensperre gilt für die ganze Laufzeit des Anbieter-Aufrufs,
// nicht nur für die TTL. Der Handler muss sie auffrischen — sonst bekommt ein
// zweiter Send desselben Nutzers sie, während der erste noch läuft. TTL hier
// 60 ms (echte Uhr), der Anbieter braucht 250 ms.
func TestLLMSend_F1538_SperreHaeltUeberTTL(t *testing.T) {
	env := newLLMSendEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	locks := llm.NewLockManager(60 * time.Millisecond)
	registry := llm.NewRegistry()
	registry.Register(env.provider)
	mux := http.NewServeMux()
	api.NewLLMSendAPI(env.boards, env.users, env.keys, registry, locks).Register(mux)
	var h http.Handler = mux
	h = api.NewACLGuard(env.acl, env.users, storage.NewBootstrapRepo(env.db)).Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	env.srv = srv

	var zweiterBekamSperre atomic.Bool
	env.provider.pruefeKontext = func(context.Context) {
		time.Sleep(250 * time.Millisecond) // > 4 × TTL
		_, ok, _ := locks.Acquire(llm.SperrSchluessel(env.boardID, env.cardID), "u-a", "anthropic", "claude-opus-4-7")
		zweiterBekamSperre.Store(ok)
	}
	resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Send: Status %d", resp.StatusCode)
	}
	if zweiterBekamSperre.Load() {
		t.Fatal("zweiter Send bekam die Sperre, während der erste noch lief")
	}
	if _, ok := locks.Holder(llm.SperrSchluessel(env.boardID, env.cardID)); ok {
		t.Error("Sperre nach Ende des Laufs nicht freigegeben")
	}
}
