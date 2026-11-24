package llm_test

// openai_stream_test.go — Tests für OpenAIProvider.SendStream + Mistral
// (Re-Use von streamOpenAICompatible). Beide Provider sprechen dasselbe
// chat-completions-Streaming-Protokoll: "data: <json>\n\n"-Frames mit
// optional finalem "data: [DONE]\n\n". Usage wird durch
// "stream_options.include_usage": true im finalen Frame mitgeschickt.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
)

// fakeOpenAIStream — Server-Stub, der eine vorgegebene Sequenz von
// chat-completion-Stream-Frames ausliefert. Validiert nebenbei, dass der
// Request "stream": true setzt (sonst läuft der Mock ins Leere).
func fakeOpenAIStream(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Body-Inspektion: stream=true Pflicht.
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("body decode: %v", err)
		}
		if b, _ := body["stream"].(bool); !b {
			t.Errorf("openai stream: stream:true fehlt im Request-Body")
		}
		// stream_options.include_usage:true für Token-Counts
		if so, ok := body["stream_options"].(map[string]any); ok {
			if iu, _ := so["include_usage"].(bool); !iu {
				t.Errorf("openai stream: stream_options.include_usage=true fehlt")
			}
		} else {
			t.Errorf("openai stream: stream_options-Feld fehlt")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
}

func openAIHappyFrames() []string {
	return []string{
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}],\"model\":\"gpt-4o\"}\n\n",
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"}}]}\n\n",
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"}}]}\n\n",
		"data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":2,\"total_tokens\":14}}\n\n",
		"data: [DONE]\n\n",
	}
}

func TestOpenAISendStream_HappyPath(t *testing.T) {
	srv := fakeOpenAIStream(t, openAIHappyFrames())
	t.Cleanup(srv.Close)
	p := &llm.OpenAIProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}

	var chunks []string
	result, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "gpt-4o", UserPrompt: "Hi", System: "be brief"},
		func(c llm.StreamChunk) error {
			if c.Text != "" {
				chunks = append(chunks, c.Text)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("SendStream err: %v", err)
	}
	if got, want := strings.Join(chunks, ""), "Hello world"; got != want {
		t.Errorf("Chunks: %q, erwartet %q", got, want)
	}
	if result.Text != "Hello world" {
		t.Errorf("Aggregat: %q, erwartet 'Hello world'", result.Text)
	}
	if result.InputTokens != 12 {
		t.Errorf("InputTokens: %d, erwartet 12 (aus finalem usage-Frame)", result.InputTokens)
	}
	if result.OutputTokens != 2 {
		t.Errorf("OutputTokens: %d, erwartet 2", result.OutputTokens)
	}
	if result.FinishReason != "stop" {
		t.Errorf("FinishReason: %q, erwartet 'stop'", result.FinishReason)
	}
	if result.Model != "gpt-4o" {
		t.Errorf("Model: %q, erwartet 'gpt-4o'", result.Model)
	}
}

func TestOpenAISendStream_InvalidKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	p := &llm.OpenAIProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}
	_, err := p.SendStream(context.Background(), "sk-bad",
		llm.SendRequest{Model: "gpt-4o", UserPrompt: "Hi"},
		func(c llm.StreamChunk) error { return nil })
	if err == nil || err.Error() == "" {
		t.Fatalf("erwarte Fehler, got nil")
	}
	// classifyHTTPStatus → ErrInvalidKey
	if !strings.Contains(err.Error(), "api key invalid") {
		t.Errorf("Fehler-Klassifikation: %v, erwartet 'api key invalid'", err)
	}
}

func TestOpenAISendStream_TolerantBeiKaputtemFrame(t *testing.T) {
	// Ein nicht-parsebarer Frame mitten im Stream darf den ganzen Stream
	// nicht killen — wir wollen die folgenden Tokens trotzdem sehen.
	frames := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"A\"}}]}\n\n",
		"data: {kaputt\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"B\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	srv := fakeOpenAIStream(t, frames)
	t.Cleanup(srv.Close)
	p := &llm.OpenAIProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}

	res, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "gpt-4o", UserPrompt: "x"},
		func(c llm.StreamChunk) error { return nil })
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}
	if res.Text != "AB" {
		t.Errorf("Aggregat: %q, erwartet 'AB' (kaputter Frame übersprungen)", res.Text)
	}
}

func TestOpenAISendStream_OnChunkError_BrichtAb(t *testing.T) {
	srv := fakeOpenAIStream(t, openAIHappyFrames())
	t.Cleanup(srv.Close)
	p := &llm.OpenAIProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}

	wantErr := fmt.Errorf("cap exceeded")
	var seen int
	_, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "gpt-4o", UserPrompt: "Hi"},
		func(c llm.StreamChunk) error {
			seen++
			if seen >= 1 {
				return wantErr
			}
			return nil
		})
	if err == nil || !strings.Contains(err.Error(), "cap exceeded") {
		t.Fatalf("erwarte cap-exceeded-Propagation, got %v", err)
	}
}

func TestMistralSendStream_NutztOpenAIPfad(t *testing.T) {
	// Mistral spricht dasselbe Protokoll wie OpenAI — der streamOpenAICompatible-
	// Helper wird re-used. Hier mit einem Mistral-typischen "stop"-finish.
	frames := []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"M\"}}],\"model\":\"mistral-large\"}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"istral\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n",
		"data: [DONE]\n\n",
	}
	srv := fakeOpenAIStream(t, frames)
	t.Cleanup(srv.Close)
	p := &llm.MistralProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}

	res, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "mistral-large", UserPrompt: "Hi"},
		func(c llm.StreamChunk) error { return nil })
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}
	if res.Text != "Mistral" {
		t.Errorf("Aggregat: %q", res.Text)
	}
	if res.InputTokens != 5 || res.OutputTokens != 2 {
		t.Errorf("Tokens: %d/%d, erwartet 5/2", res.InputTokens, res.OutputTokens)
	}
	if res.Model != "mistral-large" {
		t.Errorf("Model: %q, erwartet 'mistral-large'", res.Model)
	}
}
