package api_test

// owui_anbindung_test.go — Karte #672 (ADR-0133 §7): frame-ancestors-Kopfzeile
// und die ABOARD_OWUI_URL-Weitergabe über /me.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/web"
)

func TestFrameAncestors_API_Antworten(t *testing.T) {
	mux := http.NewServeMux()
	api.RegisterMeHandler(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
		t.Fatalf("CSP auf /me = %q, erwartet frame-ancestors 'self'", got)
	}
}

func TestFrameAncestors_Frontend_Auslieferung(t *testing.T) {
	for _, pfad := range []string{"/", "/irgendein/deep-link"} {
		rec := httptest.NewRecorder()
		web.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, pfad, nil))
		if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
			t.Fatalf("CSP auf %s = %q, erwartet frame-ancestors 'self'", pfad, got)
		}
	}
}

func TestMe_OWUIURL_Weitergabe(t *testing.T) {
	lies := func(opts ...api.MeOption) map[string]any {
		mux := http.NewServeMux()
		api.RegisterMeHandler(mux, nil, opts...)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if got := lies()["owui_url"]; got != "" {
		t.Fatalf("ohne Option owui_url = %v, erwartet leer", got)
	}
	if got := lies(api.MeWithOWUIURL("https://chat.example.de"))["owui_url"]; got != "https://chat.example.de" {
		t.Fatalf("owui_url = %v", got)
	}
}

func TestValidateOWUIURL(t *testing.T) {
	gut := map[string]string{
		"":                         "",
		"  ":                       "",
		"https://chat.example.de":  "https://chat.example.de",
		"https://chat.example.de/": "https://chat.example.de",
		"http://localhost:3000":    "http://localhost:3000",
		"https://example.de/owui/": "https://example.de/owui",
	}
	for in, want := range gut {
		got, err := api.ValidateOWUIURL(in)
		if err != nil || got != want {
			t.Errorf("ValidateOWUIURL(%q) = %q, %v; erwartet %q", in, got, err, want)
		}
	}
	schlecht := []string{
		"ftp://example.de", "javascript:alert(1)", "https://", "https://u:p@example.de",
		"https://u@example.de", "https://example.de/?x=1", "https://example.de/#a",
		"https://exa mple.de", "example.de", "https://example.de?",
	}
	for _, in := range schlecht {
		if got, err := api.ValidateOWUIURL(in); err == nil {
			t.Errorf("ValidateOWUIURL(%q) = %q, erwartet Fehler", in, got)
		}
	}
}
