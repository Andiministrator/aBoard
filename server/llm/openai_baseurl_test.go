package llm_test

// openai_baseurl_test.go — Karte #570 (ADR-0129 §5): konfigurierbare
// Basis-URL des OpenAI-kompatiblen Providers (lokales vLLM ohne Internet).
//
// Zwei Dinge werden geprüft:
//  1. ValidateOpenAIBaseURL: Schema/Host/Query/Fragment/Credentials/
//     Leerzeichen, Slash-Toleranz.
//  2. OpenAIProvider.BaseURL: TestKey trifft "<basis>/models", Send/SendStream
//     treffen "<basis>/chat/completions" — auch mit abschliessendem Slash in
//     der konfigurierten Basis.

import (
	"context"
	"errors"
	"github.com/Andiministrator/aBoard/server/ausgang"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
)

func TestValidateOpenAIBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"leer bleibt leer", "", "", false},
		{"nur Whitespace bleibt leer", "   ", "", false},
		{"gueltig ohne Slash", "http://llm.example:8000/v1", "http://llm.example:8000/v1", false},
		{"gueltig mit Slash wird getrimmt", "http://llm.example:8000/v1/", "http://llm.example:8000/v1", false},
		{"https gueltig", "https://llm.example/v1", "https://llm.example/v1", false},
		{"schema ftp abgelehnt", "ftp://llm.example:8000/v1", "", true},
		{"schema fehlt abgelehnt", "llm.example:8000/v1", "", true},
		{"host fehlt abgelehnt", "http:///v1", "", true},
		{"query abgelehnt", "http://llm.example:8000/v1?x=1", "", true},
		{"fragment abgelehnt", "http://llm.example:8000/v1#frag", "", true},
		{"user-pass abgelehnt", "http://user:pass@llm.example:8000/v1", "", true}, // abzug:testwert
		{"leerzeichen abgelehnt", "http://llm .example:8000/v1", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := llm.ValidateOpenAIBaseURL(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("erwartet Fehler für %q, bekam nil (Ergebnis %q)", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unerwarteter Fehler für %q: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("ValidateOpenAIBaseURL(%q) = %q, erwartet %q", c.in, got, c.want)
			}
		})
	}
}

// baseURLStub liefert einen httptest-Server, der GET .../models und
// POST .../chat/completions unterscheidet und je einen Treffer-Zaehler führt.
func baseURLStub(t *testing.T) (srv *httptest.Server, modelsHit, chatHit *int) {
	t.Helper()
	m, c := 0, 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		// K-B4: exakter Pfad — mit HasSuffix blieben ein verlorenes /v1 oder
		// ein doppelter Slash unbemerkt, und genau die brechen ein vLLM (404).
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			m++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
			c++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"model":"llama","choices":[{"finish_reason":"stop","message":{"content":"hi"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		default:
			t.Errorf("unerwarteter Pfad %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &m, &c
}

func TestOpenAIProvider_BaseURL_TestKeyTrifftModels(t *testing.T) {
	srv, modelsHit, _ := baseURLStub(t)
	p := &llm.OpenAIProvider{Client: geschuetzt(t, srv), UA: "test/1.0", BaseURL: srv.URL + "/v1"}
	if err := p.TestKey(context.Background(), "irgendein-key"); err != nil {
		t.Fatalf("TestKey: %v", err)
	}
	if *modelsHit != 1 {
		t.Fatalf("models-Endpoint %d mal getroffen, erwartet 1", *modelsHit)
	}
}

func TestOpenAIProvider_BaseURL_MitAbschliessendemSlash(t *testing.T) {
	srv, modelsHit, _ := baseURLStub(t)
	// Slash-Toleranz: Basis wie sie ValidateOpenAIBaseURL NICHT erst
	// getrimmt hat, weil ein Aufrufer BaseURL auch direkt setzen kann.
	p := &llm.OpenAIProvider{Client: geschuetzt(t, srv), UA: "test/1.0", BaseURL: srv.URL + "/v1/"}
	if err := p.TestKey(context.Background(), "k"); err != nil {
		t.Fatalf("TestKey mit Slash: %v", err)
	}
	if *modelsHit != 1 {
		t.Fatalf("models-Endpoint %d mal getroffen, erwartet 1", *modelsHit)
	}
}

func TestOpenAIProvider_BaseURL_SendTrifftChatCompletions(t *testing.T) {
	srv, _, chatHit := baseURLStub(t)
	p := &llm.OpenAIProvider{Client: geschuetzt(t, srv), UA: "test/1.0", BaseURL: srv.URL + "/v1"}
	res, err := p.Send(context.Background(), "k", llm.SendRequest{Model: "llama", UserPrompt: "hallo"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Text != "hi" {
		t.Errorf("Text = %q, erwartet \"hi\"", res.Text)
	}
	if *chatHit != 1 {
		t.Fatalf("chat/completions-Endpoint %d mal getroffen, erwartet 1", *chatHit)
	}
}

func TestOpenAIProvider_BaseURL_SendStreamTrifftChatCompletions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" { // K-B4: exakter Pfad
			t.Errorf("unerwarteter Pfad %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()
	p := &llm.OpenAIProvider{Client: geschuetzt(t, srv), UA: "test/1.0", BaseURL: srv.URL + "/v1"}
	var got strings.Builder
	res, err := p.SendStream(context.Background(), "k", llm.SendRequest{Model: "llama", UserPrompt: "hallo"},
		func(ch llm.StreamChunk) error {
			got.WriteString(ch.Text)
			return nil
		})
	if err != nil {
		t.Fatalf("SendStream: %v", err)
	}
	if got.String() != "hi" {
		t.Errorf("gestreamter Text = %q, erwartet \"hi\"", got.String())
	}
	if res.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, erwartet stop", res.FinishReason)
	}
}

// TestOpenAIProvider_EndpointOverrideHatVorrangVorBaseURL — Endpoint/
// SendEndpoint sind Test-Overrides für andere Suiten (providers_test.go,
// openai_stream_test.go) und dürfen von BaseURL nicht verdrängt werden.
func TestOpenAIProvider_EndpointOverrideHatVorrangVorBaseURL(t *testing.T) {
	overrideSrv := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		overrideSrv++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	// K-B5: geschützter Client — bei einer Regression scheitert die Anfrage
	// an die .invalid-Basis am Ausgangs-Schalter statt an einer DNS-Abfrage.
	p := &llm.OpenAIProvider{
		Client:   geschuetzt(t, srv),
		UA:       "test/1.0",
		BaseURL:  "http://sollte-nie-angefragt-werden.invalid/v1",
		Endpoint: srv.URL,
	}
	if err := p.TestKey(context.Background(), "k"); err != nil {
		t.Fatalf("TestKey: %v", err)
	}
	if overrideSrv != 1 {
		t.Fatalf("Endpoint-Override-Server %d mal getroffen, erwartet 1 — BaseURL hat faelschlich Vorrang", overrideSrv)
	}
}

// K-B5: auch SendEndpoint hat Vorrang vor BaseURL.
func TestOpenAIProvider_SendEndpointHatVorrangVorBaseURL(t *testing.T) {
	srv, _, chatHit := baseURLStub(t)
	p := &llm.OpenAIProvider{
		Client:       geschuetzt(t, srv),
		UA:           "test/1.0",
		BaseURL:      "http://sollte-nie-angefragt-werden.invalid/v1",
		SendEndpoint: srv.URL + "/v1/chat/completions",
	}
	if _, err := p.Send(context.Background(), "k", llm.SendRequest{Model: "m", UserPrompt: "x"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if *chatHit != 1 {
		t.Fatalf("SendEndpoint-Stub %d mal getroffen, erwartet 1", *chatHit)
	}
}

// TestRegisterDefaults_UebergibtOpenAIBaseURL — RegisterDefaults reicht die
// Basis-URL an den registrierten OpenAI-Provider durch (nicht bloss an
// irgendeinen frisch gebauten).
func TestRegisterDefaults_UebergibtOpenAIBaseURL(t *testing.T) {
	// setzt den globalen Kompatibel-Schalter — für die übrigen Tests zurücksetzen
	defer llm.SetzeOpenAIKompatibel(llm.SetzeOpenAIKompatibel(false))
	r := llm.NewRegistry()
	llm.RegisterDefaults(r, "test/1.0", "http://llm.example:8000/v1")
	p, err := r.Get("openai")
	if err != nil {
		t.Fatalf("Get(openai): %v", err)
	}
	op, ok := p.(*llm.OpenAIProvider)
	if !ok {
		t.Fatalf("Provider ist kein *OpenAIProvider: %T", p)
	}
	if op.BaseURL != "http://llm.example:8000/v1" {
		t.Errorf("BaseURL = %q, erwartet http://llm.example:8000/v1", op.BaseURL)
	}
}

// Sanity: ErrInvalidKey/ErrProviderUnreachable bleiben unabhängig von
// BaseURL korrekt klassifiziert (Regressionsschutz für die Umstellung von
// String-Literal auf Methode).
func TestOpenAIProvider_BaseURL_401BleibtErrInvalidKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := &llm.OpenAIProvider{Client: geschuetzt(t, srv), UA: "test/1.0", BaseURL: srv.URL + "/v1"}
	err := p.TestKey(context.Background(), "bad")
	if !errors.Is(err, llm.ErrInvalidKey) {
		t.Errorf("erwartet ErrInvalidKey, got %v", err)
	}
}

// geschuetzt liefert einen Client wie im Betrieb (Ausgangs-Schalter vor dem
// Transport) und setzt den Schalter für die Dauer des Tests auf „aus“ mit nur
// dem Stub auf der Liste. Ignoriert der Provider die BaseURL und fällt auf
// api.openai.com zurück, scheitert die Anfrage mit ErrGesperrt — statt ins
// Internet zu gehen (so geschehen in der ersten Gegenprobe dieser Karte).
func geschuetzt(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	alt := ausgang.Setze(ausgang.Neu(true, strings.TrimPrefix(srv.URL, "http://")))
	t.Cleanup(func() { ausgang.Setze(alt) })
	return &http.Client{Transport: ausgang.Transport(srv.Client().Transport)}
}
