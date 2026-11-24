package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistry_RegisterGetList(t *testing.T) {
	r := NewRegistry()
	RegisterDefaults(r, "test/1.0", "")
	names := r.List()
	want := []string{"anthropic", "google", "mistral", "openai"}
	if len(names) != len(want) {
		t.Fatalf("Liste %v, erwartet %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("Sortierung Pos %d: %q, erwartet %q", i, names[i], n)
		}
	}

	if _, err := r.Get("openai"); err != nil {
		t.Errorf("Get(openai): %v", err)
	}
	if _, err := r.Get("foo"); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("Get(foo) erwartet ErrUnknownProvider, got %v", err)
	}
}

// stub liefert einen httptest.Server, der einen festen Status zurückliefert
// und optional den Authorization-/x-api-key-Header validiert.
func stub(t *testing.T, status int, wantAuthPrefix string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantAuthPrefix != "" {
			h := r.Header.Get("Authorization")
			if h == "" {
				h = r.Header.Get("x-api-key")
			}
			if !strings.HasPrefix(h, wantAuthPrefix) {
				t.Errorf("auth header %q hat nicht prefix %q", h, wantAuthPrefix)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAnthropic_TestKey_OK_And_401(t *testing.T) {
	ok := stub(t, http.StatusOK, "")
	bad := stub(t, http.StatusUnauthorized, "")
	for _, c := range []struct {
		name   string
		ep     string
		key    string
		expect error
	}{
		{"ok", ok.URL, "sk-ant-key", nil},
		{"401", bad.URL, "sk-ant-bad", ErrInvalidKey},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &AnthropicProvider{Client: ok.Client(), UA: "test/1.0", Endpoint: c.ep}
			err := p.TestKey(context.Background(), c.key)
			if c.expect == nil && err != nil {
				t.Errorf("erwartet nil, got %v", err)
			}
			if c.expect != nil && !errors.Is(err, c.expect) {
				t.Errorf("erwartet %v, got %v", c.expect, err)
			}
		})
	}
}

func TestOpenAI_TestKey_BearerHeader(t *testing.T) {
	srv := stub(t, http.StatusOK, "Bearer sk-openai")
	p := &OpenAIProvider{Client: srv.Client(), UA: "test/1.0", Endpoint: srv.URL}
	if err := p.TestKey(context.Background(), "sk-openai"); err != nil {
		t.Fatalf("TestKey: %v", err)
	}
}

func TestOpenAI_TestKey_401(t *testing.T) {
	srv := stub(t, http.StatusUnauthorized, "")
	p := &OpenAIProvider{Client: srv.Client(), UA: "test/1.0", Endpoint: srv.URL}
	err := p.TestKey(context.Background(), "sk-bad")
	if !errors.Is(err, ErrInvalidKey) {
		t.Errorf("erwartet ErrInvalidKey, got %v", err)
	}
}

func TestGoogle_TestKey_KeyAsQuery(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "gemini-key" {
			t.Errorf("key-Query fehlt: %s", r.URL.RawQuery)
		}
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := &GoogleProvider{Client: srv.Client(), UA: "test/1.0", Endpoint: srv.URL}
	if err := p.TestKey(context.Background(), "gemini-key"); err != nil {
		t.Fatalf("TestKey: %v", err)
	}
	if !hit {
		t.Error("Stub-Endpoint wurde nicht aufgerufen")
	}
}

func TestMistral_TestKey_BearerHeader(t *testing.T) {
	srv := stub(t, http.StatusOK, "Bearer mistral-key")
	p := &MistralProvider{Client: srv.Client(), UA: "test/1.0", Endpoint: srv.URL}
	if err := p.TestKey(context.Background(), "mistral-key"); err != nil {
		t.Fatalf("TestKey: %v", err)
	}
}

func TestClassify5xx_AsUnreachable(t *testing.T) {
	srv := stub(t, http.StatusInternalServerError, "")
	p := &OpenAIProvider{Client: srv.Client(), UA: "test/1.0", Endpoint: srv.URL}
	err := p.TestKey(context.Background(), "key")
	if !errors.Is(err, ErrProviderUnreachable) {
		t.Errorf("5xx sollte ErrProviderUnreachable sein, got %v", err)
	}
}

func TestNetworkError_AsUnreachable(t *testing.T) {
	// Connect-Refused: nicht-erreichbarer Port.
	p := &OpenAIProvider{Client: defaultClient(), UA: "test/1.0", Endpoint: "http://127.0.0.1:1"}
	err := p.TestKey(context.Background(), "key")
	if !errors.Is(err, ErrProviderUnreachable) {
		t.Errorf("network-error sollte ErrProviderUnreachable sein, got %v", err)
	}
}
