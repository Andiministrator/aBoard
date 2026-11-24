package llm

// send_test.go — Send-Pfad-Tests pro Provider + Fehler-Klassifikation (#299, ADR-0095 §1).
//
// Provider-Stubs liefern handgepflegte JSON-Antworten in den jeweiligen
// API-Schemas; getestet wird Parsing, Token-Counts, FinishReason, FehlerPfade.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ctxBg() context.Context { return context.Background() }

// sendStub liefert einen httptest.Server, der requests inspiziert + eine
// vorgegebene Response zurückgibt. assertReq darf nil sein.
func sendStub(t *testing.T, status int, respBody string, assertReq func(*http.Request, []byte)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if assertReq != nil {
			body, _ := io.ReadAll(r.Body)
			assertReq(r, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ── Anthropic Send ─────────────────────────────────────────────────

func TestAnthropic_Send_OK(t *testing.T) {
	resp := `{
		"stop_reason": "end_turn",
		"model": "claude-opus-4-7",
		"content": [{"type":"text","text":"Hallo Welt"}],
		"usage": {"input_tokens": 42, "output_tokens": 7}
	}`
	var seenHeader string
	srv := sendStub(t, 200, resp, func(r *http.Request, body []byte) {
		seenHeader = r.Header.Get("x-api-key")
		// Schema-Check: system + messages
		var parsed struct {
			Model    string `json:"model"`
			System   string `json:"system"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("request body invalid: %v", err)
		}
		if parsed.Model != "claude-opus-4-7" {
			t.Errorf("model %q != claude-opus-4-7", parsed.Model)
		}
		if parsed.System != "Du bist hilfreich." {
			t.Errorf("system fehlt/falsch: %q", parsed.System)
		}
		if len(parsed.Messages) != 1 || parsed.Messages[0].Role != "user" {
			t.Errorf("messages-Layout falsch: %+v", parsed.Messages)
		}
		if parsed.MaxTokens != 4096 {
			t.Errorf("default MaxTokens=4096 erwartet, got %d", parsed.MaxTokens)
		}
	})
	p := &AnthropicProvider{Client: srv.Client(), UA: "test", Endpoint: srv.URL}
	got, err := p.Send(ctxBg(), "sk-ant-xyz", SendRequest{
		Model: "claude-opus-4-7", System: "Du bist hilfreich.", UserPrompt: "# Karte: foo",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Text != "Hallo Welt" {
		t.Errorf("Text %q != Hallo Welt", got.Text)
	}
	if got.InputTokens != 42 || got.OutputTokens != 7 {
		t.Errorf("Tokens (%d,%d) != (42,7)", got.InputTokens, got.OutputTokens)
	}
	if got.Model != "claude-opus-4-7" {
		t.Errorf("Model %q != claude-opus-4-7", got.Model)
	}
	if got.FinishReason != "end_turn" {
		t.Errorf("FinishReason %q != end_turn", got.FinishReason)
	}
	if seenHeader != "sk-ant-xyz" {
		t.Errorf("x-api-key fehlt im Header: %q", seenHeader)
	}
}

func TestAnthropic_Send_401(t *testing.T) {
	srv := sendStub(t, 401, `{"error":"unauthorized"}`, nil)
	p := &AnthropicProvider{Client: srv.Client(), UA: "test", Endpoint: srv.URL}
	_, err := p.Send(ctxBg(), "bad", SendRequest{Model: "claude-haiku-4-5", UserPrompt: "x"})
	if !errors.Is(err, ErrInvalidKey) {
		t.Errorf("erwartet ErrInvalidKey, got %v", err)
	}
}

func TestAnthropic_Send_5xx(t *testing.T) {
	srv := sendStub(t, 500, `{"error":"oops"}`, nil)
	p := &AnthropicProvider{Client: srv.Client(), UA: "test", Endpoint: srv.URL}
	_, err := p.Send(ctxBg(), "key", SendRequest{Model: "claude-haiku-4-5", UserPrompt: "x"})
	if !errors.Is(err, ErrProviderUnreachable) {
		t.Errorf("erwartet ErrProviderUnreachable, got %v", err)
	}
}

// ── OpenAI Send ───────────────────────────────────────────────────

func TestOpenAI_Send_OK(t *testing.T) {
	resp := `{
		"model": "gpt-4o-mini",
		"choices": [{"finish_reason": "stop", "message": {"content": "OpenAI antwortet"}}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5}
	}`
	srv := sendStub(t, 200, resp, func(r *http.Request, body []byte) {
		// Bearer-Header
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("Bearer-Header fehlt: %q", got)
		}
		// System als erste Message (OpenAI-Schema)
		var parsed struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &parsed)
		if len(parsed.Messages) != 2 || parsed.Messages[0].Role != "system" || parsed.Messages[1].Role != "user" {
			t.Errorf("messages-Layout falsch: %+v", parsed.Messages)
		}
	})
	p := &OpenAIProvider{Client: srv.Client(), UA: "test", SendEndpoint: srv.URL}
	got, err := p.Send(ctxBg(), "sk-openai", SendRequest{
		Model: "gpt-4o-mini", System: "sys", UserPrompt: "frage",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Text != "OpenAI antwortet" {
		t.Errorf("Text %q falsch", got.Text)
	}
	if got.InputTokens != 10 || got.OutputTokens != 5 {
		t.Errorf("Tokens (%d,%d) != (10,5)", got.InputTokens, got.OutputTokens)
	}
	if got.FinishReason != "stop" {
		t.Errorf("FinishReason %q != stop", got.FinishReason)
	}
}

func TestOpenAI_Send_NoSystem_OmitsRole(t *testing.T) {
	srv := sendStub(t, 200, `{"choices":[{"message":{"content":""}}],"usage":{}}`, func(r *http.Request, body []byte) {
		var parsed struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(body, &parsed)
		if len(parsed.Messages) != 1 || parsed.Messages[0].Role != "user" {
			t.Errorf("Ohne System soll nur user-Msg drin sein, got %+v", parsed.Messages)
		}
	})
	p := &OpenAIProvider{Client: srv.Client(), UA: "test", SendEndpoint: srv.URL}
	_, err := p.Send(ctxBg(), "key", SendRequest{Model: "gpt-4o-mini", UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// ── Mistral Send (wiederverwendet sendOpenAICompatible) ──────────

func TestMistral_Send_OK(t *testing.T) {
	resp := `{"model":"mistral-large-latest","choices":[{"message":{"content":"oui"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
	srv := sendStub(t, 200, resp, nil)
	p := &MistralProvider{Client: srv.Client(), UA: "test", SendEndpoint: srv.URL}
	got, err := p.Send(ctxBg(), "key", SendRequest{Model: "mistral-large-latest", UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Text != "oui" || got.InputTokens != 3 {
		t.Errorf("got %+v", got)
	}
}

// ── Google Send ──────────────────────────────────────────────────

func TestGoogle_Send_OK(t *testing.T) {
	resp := `{
		"candidates": [{"finishReason":"STOP","content":{"parts":[{"text":"Bonjour"}]}}],
		"usageMetadata": {"promptTokenCount": 8, "candidatesTokenCount": 2}
	}`
	srv := sendStub(t, 200, resp, func(r *http.Request, body []byte) {
		// Key als Query
		if r.URL.Query().Get("key") != "gemini-xyz" {
			t.Errorf("key-Query fehlt: %s", r.URL.RawQuery)
		}
		// Schema: system_instruction + contents[0].parts[0].text
		var parsed struct {
			SystemInstruction struct {
				Text string `json:"text"`
			} `json:"system_instruction"`
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		_ = json.Unmarshal(body, &parsed)
		if parsed.SystemInstruction.Text != "Du bist hilfreich." {
			t.Errorf("system_instruction fehlt/falsch: %+v", parsed.SystemInstruction)
		}
		if len(parsed.Contents) != 1 || len(parsed.Contents[0].Parts) != 1 {
			t.Errorf("contents-Layout falsch: %+v", parsed.Contents)
		}
	})
	p := &GoogleProvider{Client: srv.Client(), UA: "test", SendEndpoint: srv.URL}
	got, err := p.Send(ctxBg(), "gemini-xyz", SendRequest{
		Model: "gemini-1.5-flash", System: "Du bist hilfreich.", UserPrompt: "salut",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Text != "Bonjour" {
		t.Errorf("Text %q != Bonjour", got.Text)
	}
	if got.InputTokens != 8 || got.OutputTokens != 2 {
		t.Errorf("Tokens (%d,%d) != (8,2)", got.InputTokens, got.OutputTokens)
	}
	if got.Model != "gemini-1.5-flash" {
		t.Errorf("Model %q != Eingang-Modell", got.Model)
	}
	if got.FinishReason != "STOP" {
		t.Errorf("FinishReason %q", got.FinishReason)
	}
}

// ── Pricing-Lookup ───────────────────────────────────────────────

func TestPricing_Anthropic_Opus(t *testing.T) {
	p := &AnthropicProvider{}
	in, out := p.Pricing("claude-opus-4-7")
	if in != 15.0 || out != 75.0 {
		t.Errorf("Opus pricing (in=%v, out=%v) erwartet (15.0, 75.0)", in, out)
	}
}

func TestPricing_OpenAI_4oMini(t *testing.T) {
	p := &OpenAIProvider{}
	in, out := p.Pricing("gpt-4o-mini")
	if in != 0.15 || out != 0.60 {
		t.Errorf("gpt-4o-mini pricing (in=%v, out=%v) erwartet (0.15, 0.60)", in, out)
	}
}

func TestPricing_Unknown_ReturnsZero(t *testing.T) {
	p := &AnthropicProvider{}
	in, out := p.Pricing("unknown-model-x")
	if in != 0 || out != 0 {
		t.Errorf("Unknown model: erwartet (0,0), got (%v,%v)", in, out)
	}
}

func TestAllPricing_HasAllProviders(t *testing.T) {
	all := AllPricing()
	for _, p := range []string{"anthropic", "openai", "google", "mistral"} {
		if _, ok := all[p]; !ok {
			t.Errorf("Provider %q fehlt in AllPricing", p)
		}
		if len(all[p]) == 0 {
			t.Errorf("Provider %q hat 0 Modelle", p)
		}
	}
}
