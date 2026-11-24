package llm_test

// anthropic_stream_test.go — Tests für AnthropicProvider.SendStream
// (#300, ADR-0096 Etappe 1). Testet den SSE-Parser gegen einen
// httptest-Server, der ein realistisches Anthropic-Stream-Event-Format
// emittet (siehe https://docs.anthropic.com/en/api/messages-streaming).

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/llm"
)

// fakeAnthropicStream — minimaler Server-Stub, der eine vorgegebene Sequenz
// von SSE-Events ausspuckt. Pro Event: "event: <type>\ndata: <json>\n\n".
func fakeAnthropicStream(events []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, e := range events {
			_, _ = io.WriteString(w, e)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
}

// happyPathEvents — minimaler Anthropic-Stream: 1× message_start, 2×
// content_block_delta, 1× message_delta, 1× message_stop.
func happyPathEvents() []string {
	return []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-7\",\"usage\":{\"input_tokens\":42,\"output_tokens\":0}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
}

func newAnthropicProviderAgainst(srv *httptest.Server) *llm.AnthropicProvider {
	return &llm.AnthropicProvider{
		Client:   srv.Client(),
		UA:       "aboard-test/0",
		Endpoint: srv.URL,
	}
}

func TestAnthropicSendStream_HappyPath_StreamsTokensAndAggregates(t *testing.T) {
	srv := fakeAnthropicStream(happyPathEvents())
	t.Cleanup(srv.Close)
	p := newAnthropicProviderAgainst(srv)

	var chunks []string
	result, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
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
		t.Errorf("Aggregat: %q, erwartet %q", result.Text, "Hello world")
	}
	if result.InputTokens != 42 {
		t.Errorf("InputTokens: %d, erwartet 42 (aus message_start)", result.InputTokens)
	}
	if result.OutputTokens != 7 {
		t.Errorf("OutputTokens: %d, erwartet 7 (aus message_delta.usage)", result.OutputTokens)
	}
	if result.FinishReason != "end_turn" {
		t.Errorf("FinishReason: %q, erwartet end_turn", result.FinishReason)
	}
}

func TestAnthropicSendStream_OnChunkError_StopsStream(t *testing.T) {
	srv := fakeAnthropicStream(happyPathEvents())
	t.Cleanup(srv.Close)
	p := newAnthropicProviderAgainst(srv)

	abortAt := 1 // nach dem ersten Token abbrechen
	var calls int
	wantErr := fmt.Errorf("client cancel")

	_, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(c llm.StreamChunk) error {
			calls++
			if calls > abortAt {
				return wantErr
			}
			return nil
		})
	if err == nil {
		t.Fatal("erwartet Fehler vom onChunk, got nil")
	}
	// Wir müssen den Abbruch-Fehler durchreichen — sonst kann der Handler
	// keinen Cap-Exceeded/Cancel-Pfad korrekt detektieren.
	if !strings.Contains(err.Error(), "client cancel") {
		t.Errorf("Fehler-Propagation: %v, erwartet 'client cancel'", err)
	}
	if calls < 2 {
		t.Errorf("onChunk-Aufrufe: %d, erwartet ≥ 2 (1 OK + 1 Abbruch)", calls)
	}
}

func TestAnthropicSendStream_InvalidKey_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	p := newAnthropicProviderAgainst(srv)

	_, err := p.SendStream(context.Background(), "sk-bad",
		llm.SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(llm.StreamChunk) error { return nil })
	if err == nil {
		t.Fatal("erwartet Fehler bei 401, got nil")
	}
	if !strings.Contains(err.Error(), "api key invalid") {
		t.Errorf("Fehler-Klassifikation: %v, erwartet ErrInvalidKey", err)
	}
}

func TestAnthropicSendStream_ProviderError_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	p := newAnthropicProviderAgainst(srv)

	_, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(llm.StreamChunk) error { return nil })
	if err == nil {
		t.Fatal("erwartet Fehler bei 503, got nil")
	}
	if !strings.Contains(err.Error(), "provider unreachable") {
		t.Errorf("Fehler-Klassifikation: %v, erwartet ErrProviderUnreachable", err)
	}
}

func TestAnthropicSendStream_ContextCancel_StopsImmediately(t *testing.T) {
	// Server emittet langsam, damit der Cancel im Lauf greift.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n")
		if fl != nil {
			fl.Flush()
		}
		time.Sleep(500 * time.Millisecond) // wartend
	}))
	t.Cleanup(srv.Close)
	p := newAnthropicProviderAgainst(srv)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := p.SendStream(ctx, "sk-test",
		llm.SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(llm.StreamChunk) error { return nil })
	if err == nil {
		t.Fatal("erwartet Fehler bei Context-Cancel, got nil")
	}
	if time.Since(start) > 400*time.Millisecond {
		t.Errorf("Cancel hat zu lange gedauert: %v", time.Since(start))
	}
}

// Sanity-Check: der Provider gibt das gewünschte Stream-Flag ab.
func TestAnthropicSendStream_RequestBodyHasStreamTrue(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seenBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// minimaler valider Stream — keine Tokens, nur Abschluss
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	p := newAnthropicProviderAgainst(srv)

	_, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(llm.StreamChunk) error { return nil })
	if err != nil {
		t.Fatalf("SendStream err: %v", err)
	}
	if !strings.Contains(seenBody, `"stream":true`) {
		t.Errorf("Request-Body enthält kein 'stream:true': %s", seenBody)
	}
}

// Vergewisserung: der Parser ist tolerant gegenüber Multi-Line "data:"-
// Akkumulation (Anthropic schickt typischerweise einzeilig, der SSE-Standard
// erlaubt aber Mehrzeiler).
func TestAnthropicSendStream_MultiLineDataAccumulation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// "data: {\n data: ...}" wäre der pathologische Fall — wir bauen
		// stattdessen einen JSON-Body, der mit eingebettetem Newline
		// über zwei data:-Zeilen verteilt ist.
		body := "event: message_start\ndata: {\"type\":\"message_start\",\ndata: \"message\":{\"usage\":{\"input_tokens\":3}}}\n\n"
		_, _ = io.WriteString(w, body)
		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	p := newAnthropicProviderAgainst(srv)

	res, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(llm.StreamChunk) error { return nil })
	if err != nil {
		t.Fatalf("SendStream err: %v", err)
	}
	// InputTokens sollten aus dem akkumulierten Multi-Line-JSON kommen.
	if res.InputTokens != 3 {
		t.Errorf("InputTokens nach Multi-Line-Akkumulation: %d, erwartet 3", res.InputTokens)
	}
}

// Hilfsfunktion: gibt für Debug-Zwecke eine SSE-Antwort als Liste der
// gelesenen Zeilen zurück (in einem Test optional aufrufbar).
func dumpStreamLines(body io.Reader) []string {
	var lines []string
	sc := bufio.NewScanner(body)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

var _ = dumpStreamLines // ungenutzt im Standardlauf, nur Debug-Hilfe
