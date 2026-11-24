package llm

// providers_probetimeout_test.go — F-1143 (G5, Sprint 71): TestKey() setzt
// für ALLE VIER Anbieter eine eigene Gesamtfrist per
// context.WithTimeout(ctx, ProbeTimeout) (siehe Doku-Kommentar auf
// ProbeTimeout in providers.go: "TestKey: eigene Gesamtfrist ProbeTimeout
// per context.WithTimeout"). Der Mechanismus stand bisher nur im
// Kommentar — kein Test bewies, dass ein Anbieter, der nach den Headern nie
// antwortet, tatsächlich nach ProbeTimeout abbricht statt beliebig lange zu
// hängen. Die bestehenden Tests in providers_test.go (TestAnthropic_TestKey_
// OK_And_401 & Co.) prüfen nur die Antwort-Klassifikation (200/401/5xx),
// nie die Frist selbst.
//
// ProbeTimeout ist package-var (siehe providers.go) — für die Testlaufzeit
// auf Millisekunden verkleinert, Original per t.Cleanup zurückgesetzt
// (gleiches Muster wie heartbeatInterval in api/events.go).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTestKey_ProbeTimeout_AlleVierAnbieter(t *testing.T) {
	orig := ProbeTimeout
	ProbeTimeout = 60 * time.Millisecond
	t.Cleanup(func() { ProbeTimeout = orig })

	// Handler schweigt deutlich länger als ProbeTimeout, bevor er überhaupt
	// antwortet — simuliert einen Anbieter, der (noch) nicht reagiert.
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hang.Close)

	cases := []struct {
		name    string
		testKey func(ctx context.Context) error
	}{
		{"anthropic", func(ctx context.Context) error {
			p := &AnthropicProvider{Client: hang.Client(), UA: "test/1.0", Endpoint: hang.URL}
			return p.TestKey(ctx, "sk-ant")
		}},
		{"openai", func(ctx context.Context) error {
			p := &OpenAIProvider{Client: hang.Client(), UA: "test/1.0", Endpoint: hang.URL}
			return p.TestKey(ctx, "sk-openai")
		}},
		{"google", func(ctx context.Context) error {
			p := &GoogleProvider{Client: hang.Client(), UA: "test/1.0", Endpoint: hang.URL}
			return p.TestKey(ctx, "gemini-key")
		}},
		{"mistral", func(ctx context.Context) error {
			p := &MistralProvider{Client: hang.Client(), UA: "test/1.0", Endpoint: hang.URL}
			return p.TestKey(ctx, "mistral-key")
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			// UNBEGRENZTER Eltern-Kontext — wie ein echter Aufrufer aus
			// api/llm_keys.go (r.Context() trägt von sich aus keine
			// Deadline). Ohne die interne
			// context.WithTimeout(ctx, ProbeTimeout)-Zeile im jeweiligen
			// TestKey haengt dieser Aufruf bis zum Handler-Ende (500ms).
			err := c.testKey(context.Background())
			elapsed := time.Since(start)
			if err == nil {
				t.Fatalf("TestKey nach %v ohne Fehler zurückgekehrt — erwartet Abbruch nach ProbeTimeout (%v), Handler schweigt 500ms", elapsed, ProbeTimeout)
			}
			// Grosszügige Toleranz nach oben (CI-Jitter), aber deutlich
			// unter den 500ms des Handlers — sonst hat schlicht der
			// Handler zuerst geantwortet, nicht ProbeTimeout gegriffen.
			if elapsed >= 400*time.Millisecond {
				t.Fatalf("TestKey(%s) brauchte %v (ProbeTimeout=%v, Handler-Verzoegerung 500ms) — Abbruch kam offenbar nicht von ProbeTimeout", c.name, elapsed, ProbeTimeout)
			}
		})
	}
}
