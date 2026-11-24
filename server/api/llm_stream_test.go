package api_test

// llm_stream_test.go — Tests für POST /boards/.../llm-send-stream
// (#300, ADR-0096 Etappe 1). Folgt dem newLLMSendEnv-Pattern aus
// llm_send_test.go, aber mit einem StreamingProvider-Mock und SSE-Body-
// Parser für die Response.

import (
	"bufio"
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

// ─── Streaming-Mock ─────────────────────────────────────────────────────

// streamingMockProvider implementiert Provider UND StreamingProvider.
// Die Stream-Chunks werden über `streamChunks` vorab konfiguriert.
type streamingMockProvider struct {
	name            string
	streamChunks    []string            // jeder String wird als 1 Text-Chunk emittet
	streamToolCalls []llm.ToolCallChunk // optional — Etappe 4 (#300, ADR-0096 §3)
	streamResult    llm.SendResult
	streamErr       error
	calls           atomic.Int32
	streamCalls     atomic.Int32
	lastUserPrompt  string
	lastEnableMuts  bool
	inPM, outPM     float64
	vorPause        time.Duration // Karte #665: „Denkphase“ vor dem ersten Wort
}

func (m *streamingMockProvider) Name() string                          { return m.name }
func (m *streamingMockProvider) TestKey(context.Context, string) error { return nil }
func (m *streamingMockProvider) Pricing(string) (float64, float64)     { return m.inPM, m.outPM }
func (m *streamingMockProvider) Send(_ context.Context, _ string, req llm.SendRequest) (llm.SendResult, error) {
	m.calls.Add(1)
	m.lastUserPrompt = req.UserPrompt
	return m.streamResult, nil
}
func (m *streamingMockProvider) SendStream(ctx context.Context, _ string, req llm.SendRequest, onChunk func(llm.StreamChunk) error) (llm.SendResult, error) {
	m.streamCalls.Add(1)
	m.lastUserPrompt = req.UserPrompt
	m.lastEnableMuts = req.EnableMutations
	if m.vorPause > 0 {
		select {
		case <-time.After(m.vorPause):
		case <-ctx.Done():
			return llm.SendResult{}, ctx.Err()
		}
	}
	if m.streamErr != nil {
		return llm.SendResult{}, m.streamErr
	}
	for _, t := range m.streamChunks {
		if err := ctx.Err(); err != nil {
			return llm.SendResult{}, err
		}
		if err := onChunk(llm.StreamChunk{Text: t}); err != nil {
			return llm.SendResult{}, err
		}
	}
	// Tool-Calls (Etappe 4) — vom Provider als StreamChunk.ToolCall emittiert.
	for _, tc := range m.streamToolCalls {
		if err := ctx.Err(); err != nil {
			return llm.SendResult{}, err
		}
		tcc := tc // pointer-stable Kopie
		if err := onChunk(llm.StreamChunk{ToolCall: &tcc}); err != nil {
			return llm.SendResult{}, err
		}
	}
	// Result.Text aggregiert die Chunks (Handler verwendet result.Text für
	// die Persistierung).
	var sb strings.Builder
	for _, t := range m.streamChunks {
		sb.WriteString(t)
	}
	res := m.streamResult
	res.Text = sb.String()
	return res, nil
}

// ─── Test-Environment ───────────────────────────────────────────────────

type llmStreamEnv struct {
	srv      *httptest.Server
	db       *sql.DB
	boards   *storage.BoardRepo
	users    *storage.UserRepo
	keys     *storage.LLMKeyRepo
	convs    *storage.LLMConversationRepo
	acl      *storage.AclRepo
	provider *streamingMockProvider
	locks    *llm.LockManager
	boardID  string
	cardID   string
}

func newLLMStreamEnv(t *testing.T) *llmStreamEnv {
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

	state := `{"version":"2","board":{"id":"b1","title":"Test","sprints":[{"id":"s1","title":"S","cards":[{"id":"c1","displayId":42,"title":"Karte","description":"Hi","comments":[],"activity":[],"links":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "Test", "2", []byte(state)); err != nil {
		t.Fatalf("seed board: %v", err)
	}

	mp := &streamingMockProvider{
		name:         "anthropic",
		streamChunks: []string{"Hello", " ", "world"},
		streamResult: llm.SendResult{InputTokens: 100, OutputTokens: 50, Model: "claude-opus-4-7", FinishReason: "end_turn"},
		inPM:         15.0,
		outPM:        75.0,
	}
	registry := llm.NewRegistry()
	registry.Register(mp)

	locks := llm.NewLockManager(0)
	convs := storage.NewLLMConversationRepo(db)
	sendAPI := api.NewLLMSendAPI(boards, users, keys, registry, locks).
		WithConversations(convs)

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

	return &llmStreamEnv{
		srv: srv, db: db, boards: boards, users: users, keys: keys, convs: convs, acl: acl,
		provider: mp, locks: locks, boardID: "b1", cardID: "c1",
	}
}

func (e *llmStreamEnv) seedUserWithKey(t *testing.T, userID, email, apiKey string) {
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

func (e *llmStreamEnv) grant(t *testing.T, email, role string) {
	t.Helper()
	if err := e.acl.Set(context.Background(), e.boardID, email, role); err != nil {
		t.Fatalf("seed acl: %v", err)
	}
}

func (e *llmStreamEnv) do(t *testing.T, method, path, email string, body any) *http.Response {
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

func streamPath(boardID, cardID string) string {
	return fmt.Sprintf("/boards/%s/cards/%s/llm-send-stream", boardID, cardID)
}

// sseEvent ist ein gelesenes SSE-Event aus der Stream-Response.
type sseEvent struct {
	Event string
	Data  string
}

func parseSSEResponse(t *testing.T, body io.Reader) []sseEvent {
	t.Helper()
	var (
		out      []sseEvent
		curEvent string
		curData  strings.Builder
	)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	flush := func() {
		if curEvent == "" && curData.Len() == 0 {
			return
		}
		out = append(out, sseEvent{Event: curEvent, Data: curData.String()})
		curEvent = ""
		curData.Reset()
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			curEvent = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			if curData.Len() > 0 {
				curData.WriteByte('\n')
			}
			curData.WriteString(strings.TrimSpace(line[len("data:"):]))
		}
	}
	flush()
	return out
}

// ─── Tests ──────────────────────────────────────────────────────────────

func TestLLMStream_OK_EmitsTokensAndPersistsComment(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
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
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: %q, erwartet text/event-stream", ct)
	}

	events := parseSSEResponse(t, resp.Body)
	resp.Body.Close()

	// Mindestens 3 token + 1 done
	var tokens []string
	var done sseEvent
	for _, e := range events {
		switch e.Event {
		case "token":
			var p struct{ Text string }
			_ = json.Unmarshal([]byte(e.Data), &p)
			tokens = append(tokens, p.Text)
		case "done":
			done = e
		case "error":
			t.Fatalf("unerwarteter error-Event: %s", e.Data)
		}
	}
	if got, want := strings.Join(tokens, ""), "Hello world"; got != want {
		t.Errorf("Tokens-Concat: %q, erwartet %q", got, want)
	}
	if done.Event != "done" {
		t.Fatal("kein done-Event in der Antwort")
	}
	var donePayload struct {
		CommentID       string  `json:"commentId"`
		InputTokens     int     `json:"inputTokens"`
		OutputTokens    int     `json:"outputTokens"`
		CostEstimateUSD float64 `json:"costEstimateUSD"`
		FinishReason    string  `json:"finishReason"`
	}
	_ = json.Unmarshal([]byte(done.Data), &donePayload)
	if donePayload.CommentID == "" {
		t.Errorf("done.commentId leer")
	}
	if donePayload.InputTokens != 100 || donePayload.OutputTokens != 50 {
		t.Errorf("done.tokens: %d/%d", donePayload.InputTokens, donePayload.OutputTokens)
	}
	wantCost := (100.0/1_000_000)*15.0 + (50.0/1_000_000)*75.0
	if donePayload.CostEstimateUSD < wantCost*0.99 || donePayload.CostEstimateUSD > wantCost*1.01 {
		t.Errorf("done.cost: %v, erwartet ~%v", donePayload.CostEstimateUSD, wantCost)
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
	if cm["text"].(string) != "Hello world" {
		t.Errorf("Comment.text: %q", cm["text"])
	}
	activity := card["activity"].([]any)
	if len(activity) != 1 {
		t.Fatalf("Activity: %d", len(activity))
	}
	a := activity[0].(map[string]any)
	if a["action"].(string) != "llm-sent" {
		t.Errorf("action: %v", a["action"])
	}

	if env.provider.streamCalls.Load() != 1 {
		t.Errorf("Provider-Stream-Calls: %d", env.provider.streamCalls.Load())
	}
	if env.provider.calls.Load() != 0 {
		t.Errorf("Provider.Send darf NICHT aufgerufen werden im Stream-Pfad")
	}
}

// F-398: userPromptOverride muss vom Stream-Endpoint akzeptiert werden,
// sonst läuft der PII-Filter (ADR-0097) am Stream-Pfad vorbei.
func TestLLMStream_UserPromptOverride_BypassesBuildPrompt(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	override := "# Karte: redactet\n\n## Beschreibung\n\n<REDACTED>\n"
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
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
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if env.provider.lastUserPrompt != override {
		t.Errorf("Provider erhielt UserPrompt %q, erwartet Override %q",
			env.provider.lastUserPrompt, override)
	}
}

// F-398 / F-392-Muster: bei aktivem Override markiert contextNote im Audit-
// Trail den Client-Override. Sonst sieht Compliance heimlich auf
// includeFields, während der echte Inhalt aus dem Override kam.
func TestLLMStream_UserPromptOverride_MarksAuditTrail(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	override := "## Beschreibung\n\n<REDACTED>\n"
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
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
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	b, _ := env.boards.Get(context.Background(), env.boardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	card := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	activity := card["activity"].([]any)
	if len(activity) == 0 {
		t.Fatal("kein Activity-Eintrag")
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

func TestLLMStream_NoOverride_UsesBuildPrompt(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider":      "anthropic",
			"model":         "claude-opus-4-7",
			"systemPrompt":  "x",
			"includeFields": []string{"description"},
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if env.provider.lastUserPrompt == "" {
		t.Errorf("UserPrompt war leer, erwartet BuildPrompt-Output")
	}
	if !strings.Contains(env.provider.lastUserPrompt, "# Karte") {
		t.Errorf("UserPrompt enthält keinen Header: %q", env.provider.lastUserPrompt)
	}
}

func TestLLMStream_Locked_423_JSONBody(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	_, ok, _ := env.locks.Acquire(llm.SperrSchluessel(env.boardID, env.cardID), "u-other", "openai", "gpt-4o")
	if !ok {
		t.Fatal("Lock-Setup gescheitert")
	}

	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusLocked {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("erwartet 423 Locked, got %d (%s)", resp.StatusCode, raw)
	}
	// Vor Stream-Start ist JSON-Body Vertrag — kein SSE.
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("Content-Type bei Locked: %q, erwartet json", ct)
	}
	var body struct {
		Kind   string `json:"kind"`
		Holder struct {
			User string `json:"user"`
		} `json:"holder"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Kind != "locked" || body.Holder.User != "u-other" {
		t.Errorf("Body: %+v", body)
	}
}

func TestLLMStream_StreamNotSupported_400(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	// Provider durch einen nur-Provider-Mock ersetzen (kein StreamingProvider).
	// Wir registrieren ihn unter neuem Namen.
	plainOnly := &mockProvider{name: "openai", sendResult: llm.SendResult{}, inPM: 1, outPM: 1}
	registry := llm.NewRegistry()
	registry.Register(env.provider) // anthropic = streaming
	registry.Register(plainOnly)    // openai = nicht-streaming

	// Neuen Mux mit dieser Registry — einfacher Weg: separater Sub-Test-Server.
	mux := http.NewServeMux()
	api.NewLLMSendAPI(env.boards, env.users, env.keys, registry, env.locks).Register(mux)
	bootstrap := storage.NewBootstrapRepo(env.db)
	guard := api.NewACLGuard(env.acl, env.users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv2 := httptest.NewServer(h)
	defer srv2.Close()

	body, _ := json.Marshal(map[string]any{"provider": "openai", "model": "gpt-4o"})
	r, _ := http.NewRequest(http.MethodPost, srv2.URL+streamPath(env.boardID, env.cardID), bytes.NewReader(body))
	r.Header.Set("Remote-Email", "a@x.de")
	r.Header.Set("Remote-User", "a@x.de")
	r.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("erwartet 400, got %d (%s)", resp.StatusCode, raw)
	}
	var errBody struct{ Kind string }
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Kind != "stream-not-supported" {
		t.Errorf("kind: %q, erwartet stream-not-supported", errBody.Kind)
	}
}

func TestLLMStream_ProviderError_SSEErrorEvent(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	env.provider.streamErr = llm.ErrProviderUnreachable

	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusOK {
		// Provider-Fehler nach Stream-Start = 200 mit error-Event,
		// nicht 4xx/5xx (sonst hängt der Frontend-Decoder).
		t.Errorf("Status: %d, erwartet 200 (Fehler kommt als SSE-Event)", resp.StatusCode)
	}
	events := parseSSEResponse(t, resp.Body)
	resp.Body.Close()

	var foundError bool
	for _, e := range events {
		if e.Event == "error" {
			foundError = true
			if !strings.Contains(e.Data, "unreachable") {
				t.Errorf("error-Event-Body: %q", e.Data)
			}
		}
		if e.Event == "done" {
			t.Errorf("done-Event darf bei Fehler NICHT kommen: %q", e.Data)
		}
	}
	if !foundError {
		t.Errorf("kein error-Event empfangen, Events: %+v", events)
	}

	// Kein Kommentar persistiert (Cancel-Vertrag).
	b, _ := env.boards.Get(context.Background(), env.boardID)
	var state map[string]any
	_ = json.Unmarshal(b.StateJSON, &state)
	card := state["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)[0].(map[string]any)
	if comments := card["comments"].([]any); len(comments) != 0 {
		t.Errorf("Kommentare nach Stream-Fehler: %d, erwartet 0", len(comments))
	}
}

func TestLLMStream_CardNotFound_404(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, streamPath(env.boardID, "nope"), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("erwartet 404, got %d", resp.StatusCode)
	}
}

func TestLLMStream_NoKey_412(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "") // kein Key
	env.grant(t, "a@x.de", storage.RoleEditor)

	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Errorf("erwartet 412, got %d", resp.StatusCode)
	}
}

// ─── Multi-Turn-Tests (#300, ADR-0096 §2 Etappe 2) ──────────────────────

// seedConv legt eine Conversation auf der Test-Karte an und liefert die ID.
func (e *llmStreamEnv) seedConv(t *testing.T, createdBy, provider, model string) string {
	t.Helper()
	c, err := e.convs.CreateConversation(context.Background(), storage.CreateConversationParams{
		ID: "conv-" + provider, BoardID: e.boardID, CardID: e.cardID,
		Provider: provider, Model: model, CreatedBy: createdBy,
	})
	if err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	return c.ID
}

func TestLLMStream_MultiTurn_HappyPath_HistoryAndPersist(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	convID := env.seedConv(t, "a@x.de", "anthropic", "claude-opus-4-7")

	// Vorher: 2 Turns als Verlauf einspielen
	for _, p := range []storage.AppendTurnParams{
		{ID: "t1", ConversationID: convID, Role: "user", Content: "Erste Frage"},
		{ID: "t2", ConversationID: convID, Role: "assistant", Content: "Erste Antwort",
			InputTokens: ptrInt(100), OutputTokens: ptrInt(30), CostEstimate: ptrF64(0.005)},
	} {
		if _, err := env.convs.AppendTurn(context.Background(), p); err != nil {
			t.Fatalf("seed turn: %v", err)
		}
	}

	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider":       "anthropic",
			"model":          "claude-opus-4-7",
			"conversationId": convID,
			"includeFields":  []string{"description"},
		})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Status %d: %s", resp.StatusCode, raw)
	}
	events := parseSSEResponse(t, resp.Body)
	resp.Body.Close()

	var done sseEvent
	for _, e := range events {
		if e.Event == "done" {
			done = e
		}
		if e.Event == "error" {
			t.Fatalf("error-Event: %s", e.Data)
		}
	}
	if done.Event == "" {
		t.Fatal("kein done-Event")
	}

	// done-Payload trägt Turn-IDs
	var payload struct {
		UserTurnID      string `json:"userTurnId"`
		AssistantTurnID string `json:"assistantTurnId"`
	}
	_ = json.Unmarshal([]byte(done.Data), &payload)
	if payload.UserTurnID == "" || payload.AssistantTurnID == "" {
		t.Errorf("Turn-IDs im done-Event fehlen: %s", done.Data)
	}

	// Turn-Persistenz prüfen: 2 alte + 2 neue = 4
	turns, err := env.convs.LoadTurns(context.Background(), convID)
	if err != nil {
		t.Fatalf("LoadTurns: %v", err)
	}
	if len(turns) != 4 {
		t.Fatalf("Turns nach Stream: %d, erwartet 4", len(turns))
	}
	if turns[2].Role != "user" || turns[3].Role != "assistant" {
		t.Errorf("Neue Turn-Reihenfolge: %s,%s, erwartet user,assistant", turns[2].Role, turns[3].Role)
	}

	// Provider hat History gesehen — Mock-Stub speichert nur den letzten
	// UserPrompt, History-Check geht über die SendStream-Aufruf-Zähler +
	// indirekt über die Persistenz der vorhergehenden Turns.
	if env.provider.streamCalls.Load() != 1 {
		t.Errorf("streamCalls: %d, erwartet 1", env.provider.streamCalls.Load())
	}
}

func TestLLMStream_MultiTurn_CrossCard_404(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	// Conversation auf einer "anderen" Karte
	_, err := env.convs.CreateConversation(context.Background(), storage.CreateConversationParams{
		ID: "conv-other", BoardID: env.boardID, CardID: "c-foreign",
		Provider: "anthropic", Model: "claude-opus-4-7", CreatedBy: "a@x.de",
	})
	if err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider": "anthropic", "model": "claude-opus-4-7",
			"conversationId": "conv-other",
		})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Cross-Card-Conv: Status %d, erwartet 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLLMStream_MultiTurn_NotCreator_403(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	// User b ist auch editor, aber Conv gehört a
	if _, err := env.db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, 0)`,
		"u-b", storage.NormalizeEmail("b@x.de"), "B", "2026-01-01"); err != nil {
		t.Fatalf("seed user b: %v", err)
	}
	if err := env.keys.Set(context.Background(), "u-b", "anthropic", "sk-ant-b"); err != nil {
		t.Fatalf("key b: %v", err)
	}
	env.grant(t, "b@x.de", storage.RoleEditor)
	convID := env.seedConv(t, "a@x.de", "anthropic", "claude-opus-4-7")

	// User b versucht in a's Conv zu senden
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "b@x.de",
		map[string]any{
			"provider": "anthropic", "model": "claude-opus-4-7",
			"conversationId": convID,
		})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Fremder Creator: Status %d, erwartet 403", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLLMStream_MultiTurn_ProviderMismatch_400(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	convID := env.seedConv(t, "a@x.de", "openai", "gpt-4o")

	// Request mit anthropic, Conv ist openai
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider": "anthropic", "model": "claude-opus-4-7",
			"conversationId": convID,
		})
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Provider-Mismatch: Status %d (%s), erwartet 400", resp.StatusCode, raw)
	}
	resp.Body.Close()
}

func TestLLMStream_MultiTurn_Archived_400(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	convID := env.seedConv(t, "a@x.de", "anthropic", "claude-opus-4-7")
	if err := env.convs.ArchiveConversation(context.Background(), convID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider": "anthropic", "model": "claude-opus-4-7",
			"conversationId": convID,
		})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Archived: Status %d, erwartet 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestLLMStream_MultiTurn_ContextBudget_400(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	convID := env.seedConv(t, "a@x.de", "anthropic", "claude-opus-4-7")
	// Anthropic-Limit ist 200000; 80 % = 160000. Wir spielen einen Turn mit
	// rund 200000 Tokens Text als History — der Gate muss zuschlagen. Seit
	// F-1108 zählt der Text der History (4 Zeichen je Token), nicht mehr die
	// gemeldeten input_tokens früherer Turns.
	if _, err := env.convs.AppendTurn(context.Background(), storage.AppendTurnParams{
		ID: "huge", ConversationID: convID, Role: "assistant", Content: strings.Repeat("x", 200000*4),
		InputTokens: ptrInt(1), OutputTokens: ptrInt(0),
	}); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider": "anthropic", "model": "claude-opus-4-7",
			"conversationId": convID,
		})
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("Budget-Gate: Status %d (%s), erwartet 400", resp.StatusCode, raw)
	}
	var errBody struct{ Kind string }
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	resp.Body.Close()
	if errBody.Kind != "context-budget" {
		t.Errorf("kind: %q, erwartet 'context-budget'", errBody.Kind)
	}
}

func TestLLMStream_MultiTurn_ConvNotFound_404(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider": "anthropic", "model": "claude-opus-4-7",
			"conversationId": "nonexistent",
		})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Conv-not-found: Status %d, erwartet 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func ptrInt(v int) *int         { return &v }
func ptrF64(v float64) *float64 { return &v }

// ─── Tool-Use / Mutations (#300 Etappe 4, ADR-0096 §3+§4) ───────────────

func TestLLMStream_Mutations_AllowedToolCall_EmitsToolCallEvent(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	env.provider.streamToolCalls = []llm.ToolCallChunk{
		{ID: "tc1", Name: "set-field", Args: json.RawMessage(`{"field":"qa","value":true}`)},
	}
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider": "anthropic", "model": "claude-opus-4-7",
			"enableMutations": true,
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d, erwartet 200", resp.StatusCode)
	}
	events := parseSSEResponse(t, resp.Body)
	resp.Body.Close()
	if !env.provider.lastEnableMuts {
		t.Errorf("Provider hat EnableMutations=false bekommen — sollte true sein")
	}
	var found *sseEvent
	for i := range events {
		if events[i].Event == "tool-call" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("kein tool-call-Event in SSE-Stream (got: %+v)", events)
	}
	var pl map[string]any
	if err := json.Unmarshal([]byte(found.Data), &pl); err != nil {
		t.Fatalf("tool-call-Payload kein JSON: %v (raw=%s)", err, found.Data)
	}
	if pl["id"] != "tc1" || pl["name"] != "set-field" {
		t.Errorf("Payload id/name: %v / %v", pl["id"], pl["name"])
	}
	if pl["allowed"] != true {
		t.Errorf("allowed: %v, erwartet true", pl["allowed"])
	}
	if pl["args"] == nil {
		t.Errorf("args fehlt im Payload")
	}
}

func TestLLMStream_Mutations_BlockedAction_EmitsBlocked(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	// Halluzinierter Blocklist-Action (LLM versucht trotz fehlender Tool-Spec).
	env.provider.streamToolCalls = []llm.ToolCallChunk{
		{ID: "tcX", Name: "card.delete", Args: json.RawMessage(`{}`)},
	}
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider": "anthropic", "model": "claude-opus-4-7",
			"enableMutations": true,
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d, erwartet 200", resp.StatusCode)
	}
	events := parseSSEResponse(t, resp.Body)
	resp.Body.Close()
	var found *sseEvent
	for i := range events {
		if events[i].Event == "tool-call" {
			found = &events[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("kein tool-call-Event (got: %+v)", events)
	}
	var pl map[string]any
	_ = json.Unmarshal([]byte(found.Data), &pl)
	if pl["blocked"] != true {
		t.Errorf("blocked: %v, erwartet true", pl["blocked"])
	}
	if pl["reason"] == nil || pl["reason"] == "" {
		t.Errorf("reason fehlt: %v", pl["reason"])
	}
	if pl["allowed"] == true {
		t.Errorf("allowed darf bei blocked nicht true sein")
	}
}

func TestLLMStream_Mutations_CapExceeded_EmitsTooManyError(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	// 6 ToolCalls → Cap (5) wird beim 6. ausgelöst.
	tcs := make([]llm.ToolCallChunk, 6)
	for i := range tcs {
		tcs[i] = llm.ToolCallChunk{
			ID:   fmt.Sprintf("tc%d", i),
			Name: "add-label",
			Args: json.RawMessage(fmt.Sprintf(`{"label":"L%d"}`, i)),
		}
	}
	env.provider.streamToolCalls = tcs
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider": "anthropic", "model": "claude-opus-4-7",
			"enableMutations": true,
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d, erwartet 200", resp.StatusCode)
	}
	events := parseSSEResponse(t, resp.Body)
	resp.Body.Close()

	var toolCallCount int
	var errKind string
	for _, e := range events {
		if e.Event == "tool-call" {
			toolCallCount++
		}
		if e.Event == "error" {
			var pl map[string]any
			_ = json.Unmarshal([]byte(e.Data), &pl)
			if k, _ := pl["kind"].(string); k != "" {
				errKind = k
			}
		}
	}
	if toolCallCount != llm.MaxMutationProposalsPerResponse {
		t.Errorf("tool-call-Events: %d, erwartet %d (Cap)", toolCallCount, llm.MaxMutationProposalsPerResponse)
	}
	if errKind != "too-many-mutations" {
		t.Errorf("error.kind: %q, erwartet 'too-many-mutations' (events=%+v)", errKind, events)
	}
}

func TestLLMStream_Mutations_Disabled_DropsHallucinatedToolCalls(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	// Provider halluziniert tool_use trotz fehlendem enableMutations.
	env.provider.streamToolCalls = []llm.ToolCallChunk{
		{ID: "tcX", Name: "set-field", Args: json.RawMessage(`{"field":"qa","value":true}`)},
	}
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{
			"provider": "anthropic", "model": "claude-opus-4-7",
			// enableMutations bewusst weggelassen
		})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d, erwartet 200", resp.StatusCode)
	}
	events := parseSSEResponse(t, resp.Body)
	resp.Body.Close()
	if env.provider.lastEnableMuts {
		t.Errorf("Provider hat EnableMutations=true bekommen — sollte false sein")
	}
	for _, e := range events {
		if e.Event == "tool-call" {
			t.Errorf("unerwartetes tool-call-Event (enableMutations=false): %s", e.Data)
		}
	}
}
