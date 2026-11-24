package llm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Andiministrator/aBoard/server/ausgang"
)

// Karte #569: Alle vier Anbieter teilen den Client aus defaultClient()
// (RegisterDefaults). Bei ABOARD_AUSGAENGE=aus erreicht keine Anfrage das
// Ziel; Gegenprobe: mit dem Ziel auf der Liste kommt sie an.
func TestAusgang_LLMClient(t *testing.T) {
	var treffer atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		treffer.Add(1)
	}))
	defer srv.Close()
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true)))

	c := defaultClient()
	_, err := c.Get(srv.URL + "/v1/models")
	if !errors.Is(err, ausgang.ErrGesperrt) {
		t.Fatalf("erwartet ErrGesperrt, bekam %v", err)
	}
	if treffer.Load() != 0 {
		t.Fatalf("%d Anfrage(n) trotz aus", treffer.Load())
	}
	ausgang.Setze(ausgang.Neu(true, strings.TrimPrefix(srv.URL, "http://")))
	res, err := c.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("erlaubtes Ziel: %v", err)
	}
	res.Body.Close()
	if treffer.Load() != 1 {
		t.Fatalf("Gegenprobe: %d Anfragen, erwartet 1", treffer.Load())
	}
}

// Die Registry baut ihre Anbieter mit genau diesem Client — sonst liefe ein
// Anbieter am Schalter vorbei.
func TestAusgang_RegistryNutztGeschuetztenClient(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true)))
	r := NewRegistry()
	RegisterDefaults(r, "t", "")
	for _, name := range []string{"anthropic", "openai", "google", "mistral"} {
		p, err := r.Get(name)
		if err != nil {
			t.Fatalf("Anbieter %s fehlt: %v", name, err)
		}
		var c *http.Client
		switch v := p.(type) {
		case *AnthropicProvider:
			c = v.Client
		case *OpenAIProvider:
			c = v.Client
		case *GoogleProvider:
			c = v.Client
		case *MistralProvider:
			c = v.Client
		}
		if c == nil {
			t.Fatalf("%s: kein Client", name)
		}
		if _, err := c.Get("https://api.example.invalid/"); !errors.Is(err, ausgang.ErrGesperrt) {
			t.Errorf("%s: Client läuft am Schalter vorbei: %v", name, err)
		}
	}
}
