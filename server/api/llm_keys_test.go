package api_test

// llm_keys_test.go — REST-Endpunkte für /users/me/llm-keys/* (#298, ADR-0094).
//
// Provider-Test-Calls werden gegen einen httptest.Server gestubbt — die
// Default-Provider in llm.RegisterDefaults gehen ins Internet, also bauen
// wir eine custom Registry mit Provider-Endpoints, die auf das Stub-URL
// zeigen.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

type llmKeysEnv struct {
	srv      *httptest.Server
	db       *sql.DB
	users    *storage.UserRepo
	keys     *storage.LLMKeyRepo
	registry *llm.Registry
	stub     *httptest.Server
	stubStat int /* der Stub liefert diesen Status zurück */
	api      *api.LLMKeysAPI
}

func newLLMKeysEnv(t *testing.T) *llmKeysEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	users := storage.NewUserRepo(db)
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, err := secretcrypto.New(hex.EncodeToString(k))
	if err != nil {
		t.Fatalf("box: %v", err)
	}
	keys := storage.NewLLMKeyRepo(db, box)

	env := &llmKeysEnv{db: db, users: users, keys: keys, stubStat: http.StatusOK}

	// Stub-Provider-Endpoint: liefert env.stubStat zurück.
	env.stub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(env.stubStat)
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(env.stub.Close)

	// Registry mit 4 Stub-Providern (statt der echten Provider-URLs).
	env.registry = llm.NewRegistry()
	env.registry.Register(&llm.AnthropicProvider{Client: env.stub.Client(), UA: "test/1", Endpoint: env.stub.URL})
	env.registry.Register(&llm.OpenAIProvider{Client: env.stub.Client(), UA: "test/1", Endpoint: env.stub.URL})
	env.registry.Register(&llm.GoogleProvider{Client: env.stub.Client(), UA: "test/1", Endpoint: env.stub.URL})
	env.registry.Register(&llm.MistralProvider{Client: env.stub.Client(), UA: "test/1", Endpoint: env.stub.URL})

	llmAPI := api.NewLLMKeysAPI(keys, users, env.registry)
	env.api = llmAPI

	mux := http.NewServeMux()
	llmAPI.Register(mux)

	var h http.Handler = mux
	h = api.WithForwardAuthIfEnabled(h)
	env.srv = httptest.NewServer(h)
	t.Cleanup(func() {
		env.srv.Close()
		_ = db.Close()
	})
	return env
}

func (e *llmKeysEnv) seedUser(t *testing.T, id, email string) {
	t.Helper()
	if _, err := e.db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, 0)`,
		id, storage.NormalizeEmail(email), "N", "2026-01-01"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func (e *llmKeysEnv) do(t *testing.T, method, path, email string, body any) *http.Response {
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

// Seit ADR-0129 §2 (F-1124) weist die Forward-Auth-Middleware einen Request
// ohne Identität mit 401 ab, bevor die 403-Prüfung des Handlers greift.
func TestLLMKeys_RequiresAuthentication(t *testing.T) {
	env := newLLMKeysEnv(t)
	resp := env.do(t, http.MethodGet, "/users/me/llm-keys", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("erwartet 401 ohne Identität, got %d", resp.StatusCode)
	}
}

func TestLLMKeys_SetAndList(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")
	env.stubStat = http.StatusOK

	resp := env.do(t, http.MethodPost, "/users/me/llm-keys/anthropic", "a@x.de",
		map[string]any{"key": "sk-ant-key"})
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST: %d (%s)", resp.StatusCode, raw)
	}

	resp = env.do(t, http.MethodGet, "/users/me/llm-keys", "a@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	var body struct {
		Providers []string `json:"providers"`
		Keys      []struct {
			Provider   string `json:"provider"`
			CreatedAt  string `json:"created_at"`
			LastUsedAt string `json:"last_used_at"`
		} `json:"keys"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Providers) != 1 || body.Providers[0] != "anthropic" {
		t.Errorf("Providers: %+v", body.Providers)
	}
	if len(body.Keys) != 1 || body.Keys[0].LastUsedAt == "" {
		t.Errorf("Keys: %+v (last_used_at sollte vom Test-Pre-Call gesetzt sein)", body.Keys)
	}
}

func TestLLMKeys_KeyResponseNeverLeaksPlaintext(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")
	env.stubStat = http.StatusOK

	_ = env.do(t, http.MethodPost, "/users/me/llm-keys/openai", "a@x.de",
		map[string]any{"key": "sk-LEAK-INDICATOR-VERY-UNIQUE"})

	resp := env.do(t, http.MethodGet, "/users/me/llm-keys", "a@x.de", nil)
	raw, _ := io.ReadAll(resp.Body)
	if bytes.Contains(raw, []byte("LEAK-INDICATOR")) {
		t.Errorf("GET response leakt Klartext: %s", raw)
	}
}

func TestLLMKeys_InvalidKey_422_OhneSchreiben(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")
	env.stubStat = http.StatusUnauthorized

	resp := env.do(t, http.MethodPost, "/users/me/llm-keys/openai", "a@x.de",
		map[string]any{"key": "sk-bad"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("erwartet 422, got %d", resp.StatusCode)
	}

	// Es darf NIX gespeichert sein.
	got, _ := env.keys.List(context.Background(), "u-1")
	if len(got) != 0 {
		t.Errorf("Bei 422 darf kein Key gespeichert sein, got %+v", got)
	}
}

func TestLLMKeys_ProviderUnreachable_502_OhneSchreiben(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")
	env.stubStat = http.StatusInternalServerError

	resp := env.do(t, http.MethodPost, "/users/me/llm-keys/openai", "a@x.de",
		map[string]any{"key": "sk-anything"})
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("erwartet 502, got %d", resp.StatusCode)
	}
	got, _ := env.keys.List(context.Background(), "u-1")
	if len(got) != 0 {
		t.Errorf("Bei 502 darf kein Key gespeichert sein")
	}
}

func TestLLMKeys_UnknownProvider_400(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")

	resp := env.do(t, http.MethodPost, "/users/me/llm-keys/twitter", "a@x.de",
		map[string]any{"key": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("erwartet 400 für unbekannten Provider, got %d", resp.StatusCode)
	}
}

func TestLLMKeys_Delete(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")
	env.stubStat = http.StatusOK

	_ = env.do(t, http.MethodPost, "/users/me/llm-keys/anthropic", "a@x.de",
		map[string]any{"key": "sk-ant"})
	resp := env.do(t, http.MethodDelete, "/users/me/llm-keys/anthropic", "a@x.de", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Delete Status: %d", resp.StatusCode)
	}
	resp = env.do(t, http.MethodGet, "/users/me/llm-keys", "a@x.de", nil)
	var body struct {
		Providers []string `json:"providers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Providers) != 0 {
		t.Errorf("Nach Delete: %+v", body.Providers)
	}
}

func TestLLMKeys_Test_NoStoredKey_404(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")

	resp := env.do(t, http.MethodPost, "/users/me/llm-keys/anthropic/test", "a@x.de", map[string]any{})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("erwartet 404 ohne gespeicherten Key, got %d", resp.StatusCode)
	}
}

func TestLLMKeys_Test_StoredKey_OK(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")
	env.stubStat = http.StatusOK
	_ = env.do(t, http.MethodPost, "/users/me/llm-keys/anthropic", "a@x.de",
		map[string]any{"key": "sk-ant"})

	resp := env.do(t, http.MethodPost, "/users/me/llm-keys/anthropic/test", "a@x.de", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Test Status: %d", resp.StatusCode)
	}
}

func TestLLMKeys_PerUserIsolation_KeineCrossLeaks(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-a", "a@x.de")
	env.seedUser(t, "u-b", "b@x.de")
	env.stubStat = http.StatusOK

	_ = env.do(t, http.MethodPost, "/users/me/llm-keys/anthropic", "a@x.de", map[string]any{"key": "A-only"})
	_ = env.do(t, http.MethodPost, "/users/me/llm-keys/openai", "b@x.de", map[string]any{"key": "B-only"})

	// User A sieht nur sein Provider (anthropic), nicht Bs (openai).
	resp := env.do(t, http.MethodGet, "/users/me/llm-keys", "a@x.de", nil)
	var body struct {
		Providers []string `json:"providers"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Providers) != 1 || body.Providers[0] != "anthropic" {
		t.Errorf("Cross-User-Leak A: %+v", body.Providers)
	}
}

func TestLLMKeys_RateLimit_10PerHour(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")
	env.stubStat = http.StatusOK

	for i := 0; i < 10; i++ {
		resp := env.do(t, http.MethodPost, "/users/me/llm-keys/openai", "a@x.de",
			map[string]any{"key": "sk-loop"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Lauf %d: %d", i, resp.StatusCode)
		}
	}
	resp := env.do(t, http.MethodPost, "/users/me/llm-keys/openai", "a@x.de",
		map[string]any{"key": "sk-loop"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("11. Mutation: erwartet 429, got %d", resp.StatusCode)
	}
}

func TestLLMKeys_503_WhenBoxMissing(t *testing.T) {
	// Repo == nil simuliert "Master-Key fehlt".
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	users := storage.NewUserRepo(db)
	if _, err := db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u','a@x.de','N','2026-01-01',0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	reg := llm.NewRegistry()
	llm.RegisterDefaults(reg, "test/1", "")
	llmAPI := api.NewLLMKeysAPI(nil /* keys nil */, users, reg)

	mux := http.NewServeMux()
	llmAPI.Register(mux)
	var h http.Handler = mux
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	defer srv.Close()

	r, _ := http.NewRequest(http.MethodGet, srv.URL+"/users/me/llm-keys", nil)
	r.Header.Set("Remote-Email", "a@x.de")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("erwartet 503 ohne Box, got %d", resp.StatusCode)
	}
}

// K-B1/K-B2 (Session-Kritiker Sprint 72): Ist „openai“ umgestellt, erfährt
// die Oberfläche Ziel (nur Host, keine URL) und Modellnamen; sonst fehlt das
// Feld ganz.
func TestLLMKeys_List_MeldetOpenAIKompatibel(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")

	lies := func() map[string]any {
		resp := env.do(t, http.MethodGet, "/users/me/llm-keys", "a@x.de", nil)
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return body
	}
	if _, ok := lies()["openai_kompatibel"]; ok {
		t.Error("ohne Basis darf das Feld nicht erscheinen")
	}
	env.api.WithOpenAIKompatibel("http://llm.example:8000/v1", []string{"llama-3.1-70b"})
	k, _ := lies()["openai_kompatibel"].(map[string]any)
	if k == nil || k["ziel"] != "llm.example:8000" {
		t.Fatalf("openai_kompatibel = %v", k)
	}
	if m, _ := k["modelle"].([]any); len(m) != 1 || m[0] != "llama-3.1-70b" {
		t.Errorf("modelle = %v", k["modelle"])
	}
}

// F-1549 (Karte #681): ABOARD_LLM_OPENAI_WERKZEUGE=aus meldet der Server der
// Oberfläche als openai_werkzeuge:"aus"; ohne den Schalter fehlt das Feld
// (Antwort bit-identisch zu vorher).
func TestLLMKeys_List_MeldetWerkzeugeAus(t *testing.T) {
	env := newLLMKeysEnv(t)
	env.seedUser(t, "u-1", "a@x.de")
	vorher := llm.SetzeOpenAIWerkzeugeAus(false)
	t.Cleanup(func() { llm.SetzeOpenAIWerkzeugeAus(vorher) })

	lies := func() map[string]any {
		resp := env.do(t, http.MethodGet, "/users/me/llm-keys", "a@x.de", nil)
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return body
	}
	if _, ok := lies()["openai_werkzeuge"]; ok {
		t.Error("ohne Schalter darf das Feld nicht erscheinen")
	}
	llm.SetzeOpenAIWerkzeugeAus(true)
	if got := lies()["openai_werkzeuge"]; got != "aus" {
		t.Errorf("openai_werkzeuge = %v, erwartet \"aus\"", got)
	}
}
