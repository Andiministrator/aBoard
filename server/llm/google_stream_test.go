package llm_test

// google_stream_test.go — Tests für GoogleProvider.SendStream. Google
// streamGenerateContent mit ?alt=sse liefert SSE-Frames im Format:
//   data: {"candidates":[{"content":{"parts":[{"text":"…"}]}}]}
// Kein [DONE]-Marker — der Stream endet einfach mit dem letzten Frame, der
// finishReason + usageMetadata trägt.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
)

func fakeGoogleStream(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// alt=sse + key sind Pflicht-Query-Parameter
		q, _ := url.ParseQuery(r.URL.RawQuery)
		if q.Get("alt") != "sse" {
			t.Errorf("google stream: alt=sse fehlt, got %q", q.Get("alt"))
		}
		if q.Get("key") == "" {
			t.Errorf("google stream: key-Query fehlt")
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

func googleHappyFrames() []string {
	return []string{
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hallo\"}],\"role\":\"model\"},\"index\":0}]}\n\n",
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" Welt\"}]},\"index\":0}]}\n\n",
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"!\"}]},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":4,\"candidatesTokenCount\":3,\"totalTokenCount\":7}}\n\n",
	}
}

func TestGoogleSendStream_HappyPath(t *testing.T) {
	srv := fakeGoogleStream(t, googleHappyFrames())
	t.Cleanup(srv.Close)
	p := &llm.GoogleProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}

	var chunks []string
	res, err := p.SendStream(context.Background(), "test-key",
		llm.SendRequest{Model: "gemini-1.5-pro", UserPrompt: "Hi"},
		func(c llm.StreamChunk) error {
			if c.Text != "" {
				chunks = append(chunks, c.Text)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}
	if got, want := strings.Join(chunks, ""), "Hallo Welt!"; got != want {
		t.Errorf("Chunks: %q, erwartet %q", got, want)
	}
	if res.Text != "Hallo Welt!" {
		t.Errorf("Aggregat: %q", res.Text)
	}
	if res.InputTokens != 4 || res.OutputTokens != 3 {
		t.Errorf("Tokens: %d/%d, erwartet 4/3 (aus usageMetadata)", res.InputTokens, res.OutputTokens)
	}
	if res.FinishReason != "STOP" {
		t.Errorf("FinishReason: %q, erwartet STOP", res.FinishReason)
	}
}

func TestGoogleSendStream_InvalidKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	p := &llm.GoogleProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}
	_, err := p.SendStream(context.Background(), "bad",
		llm.SendRequest{Model: "gemini-1.5-pro", UserPrompt: "x"},
		func(c llm.StreamChunk) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "api key invalid") {
		t.Errorf("InvalidKey-Klassifikation: %v, erwartet ErrInvalidKey", err)
	}
}

func TestGoogleSendStream_KeineUsage_ToleriertEmptyResponse(t *testing.T) {
	// Ein einzelner Frame ohne usageMetadata + ohne finishReason — kann
	// passieren wenn Google den Stream vorzeitig abbricht. Wir wollen kein
	// Crash, sondern den bisherigen Text + Default-Stop.
	frames := []string{
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"OK\"}]}}]}\n\n",
	}
	srv := fakeGoogleStream(t, frames)
	t.Cleanup(srv.Close)
	p := &llm.GoogleProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}

	res, err := p.SendStream(context.Background(), "k",
		llm.SendRequest{Model: "gemini-1.5-pro", UserPrompt: "x"},
		func(c llm.StreamChunk) error { return nil })
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}
	if res.Text != "OK" {
		t.Errorf("Aggregat: %q", res.Text)
	}
	if res.FinishReason != "stop" {
		t.Errorf("FinishReason: %q, erwartet Default 'stop'", res.FinishReason)
	}
}
