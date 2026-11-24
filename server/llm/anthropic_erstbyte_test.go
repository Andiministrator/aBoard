package llm

// anthropic_erstbyte_test.go — F-1140 (Sprint 71): belegt am realen
// SendStream()-Pfad (nicht nur am streamIdleGuard-Mechanismus in
// idleguard_test.go), dass Anthropic.SendStream() die Erstbyte-Frist
// tatsächlich VERDRAHTET hat — also streamIdleGuard(ctx, streamErstbyteTimeout,
// streamIdleTimeout) aufruft und nicht mehr (wie vor dem Fix)
// streamIdleGuard(ctx, streamIdleTimeout) mit nur einer Frist für alles.
//
// streamErstbyteTimeout/streamIdleTimeout sind package-vars (siehe
// providers.go) — für die Testlaufzeit auf Millisekunden verkleinert, Original
// per t.Cleanup zurückgesetzt (gleiches Muster wie in
// providers_streamtimeout_test.go / idleguard_test.go).

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSendStream_Anthropic_DenkphaseUnterErstbyteFrist_LiefertVollstaendig —
// ein Stub, der NACH den Headern länger als die (kurz gewählte) Idle-Frist,
// aber kürzer als die Erstbyte-Frist schweigt, bevor er das erste Token
// schickt. Vor F-1140 hätte der Idle-Guard (der schon vor dem Request zu
// laufen beginnt) hier bereits abgebrochen, weil touch() erst mit der ersten
// SSE-Zeile kommt.
func TestSendStream_Anthropic_DenkphaseUnterErstbyteFrist_LiefertVollstaendig(t *testing.T) {
	origErst, origIdle := streamErstbyteTimeout, streamIdleTimeout
	streamErstbyteTimeout = 300 * time.Millisecond
	streamIdleTimeout = 60 * time.Millisecond
	t.Cleanup(func() { streamErstbyteTimeout, streamIdleTimeout = origErst, origIdle })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fl.Flush() // Header sofort raus — die folgende Stille ist reine Denkphase.
		// 150ms Denkphase: über der Idle-Frist (60ms), unter der
		// Erstbyte-Frist (300ms).
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, anthropicSSEEvent("message_start",
			`{"type":"message_start","message":{"usage":{"input_tokens":3}}}`))
		fl.Flush()
		_, _ = io.WriteString(w, anthropicSSEEvent("content_block_delta",
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"spaet aber da"}}`))
		fl.Flush()
		_, _ = io.WriteString(w, anthropicSSEEvent("message_delta",
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`))
		fl.Flush()
		_, _ = io.WriteString(w, anthropicSSEEvent("message_stop", `{"type":"message_stop"}`))
		fl.Flush()
	}))
	defer srv.Close()

	p := &AnthropicProvider{Client: defaultClient(), UA: "test/1.0", Endpoint: srv.URL}
	var received strings.Builder
	result, err := p.SendStream(context.Background(), "sk-test",
		SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(c StreamChunk) error {
			received.WriteString(c.Text)
			return nil
		})
	if err != nil {
		t.Fatalf("SendStream trotz Denkphase (150ms) < Erstbyte-Frist (%v) abgebrochen: %v", streamErstbyteTimeout, err)
	}
	if received.String() != "spaet aber da" {
		t.Fatalf("Text = %q, erwartet vollständige Antwort %q", received.String(), "spaet aber da")
	}
	if result.OutputTokens != 3 {
		t.Errorf("OutputTokens = %d, erwartet 3 (aus dem finalen message_delta, kommt nur bei vollständigem Stream an)", result.OutputTokens)
	}
}

// TestSendStream_Anthropic_ErstbyteFristUeberschritten_Bricht_Ab — Gegenstück:
// ein Stub, der auch nach der Erstbyte-Frist noch nichts liefert, muss zum
// Abbruch führen. Belegt, dass die Erstbyte-Frist eine ECHTE Obergrenze ist,
// keine Abschaffung des Idle-Guards für die Denkphase.
func TestSendStream_Anthropic_ErstbyteFristUeberschritten_BrichtAb(t *testing.T) {
	origErst, origIdle := streamErstbyteTimeout, streamIdleTimeout
	streamErstbyteTimeout = 150 * time.Millisecond
	streamIdleTimeout = 60 * time.Millisecond
	t.Cleanup(func() { streamErstbyteTimeout, streamIdleTimeout = origErst, origIdle })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		fl.Flush()
		// Schweigt länger als die Erstbyte-Frist (150ms) — ein Modell, das
		// nie ein erstes Token liefert.
		time.Sleep(500 * time.Millisecond)
		_, _ = io.WriteString(w, anthropicSSEEvent("content_block_delta",
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"zu spaet"}}`))
		fl.Flush()
	}))
	defer srv.Close()

	p := &AnthropicProvider{Client: defaultClient(), UA: "test/1.0", Endpoint: srv.URL}
	start := time.Now()
	_, err := p.SendStream(context.Background(), "sk-test",
		SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(c StreamChunk) error { return nil })
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("SendStream nach %v ohne Fehler zurückgekehrt — erwartet Abbruch nach der Erstbyte-Frist (%v), Stub schweigt 500ms", elapsed, streamErstbyteTimeout)
	}
	// Grosszügige Toleranz nach oben (CI-Jitter), aber deutlich unter den
	// 500ms des Stubs — sonst hat schlicht der Stub geantwortet, nicht die
	// Erstbyte-Frist gegriffen.
	if elapsed >= 400*time.Millisecond {
		t.Fatalf("SendStream brauchte %v (Erstbyte-Frist=%v, Stub-Verzoegerung 500ms) — Abbruch kam offenbar nicht von der Erstbyte-Frist", elapsed, streamErstbyteTimeout)
	}
}
