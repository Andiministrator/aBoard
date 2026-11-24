package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Liefert "/" das (Platzhalter-)index.html mit Status 200?
func TestHandlerServesIndexAtRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "aBoard") {
		t.Fatalf("Body enthält kein erwartetes Frontend-Markup: %q", rec.Body.String())
	}
}

// Existierende eingebettete Datei wird über den FileServer-Pfad bedient
// (nicht über den SPA-Fallback). http.FileServer kanonisiert `/index.html`
// dokumentiert auf `./` (301) — das beweist, dass die Datei im Embed-FS
// gefunden wurde (sonst hätte der Fallback 200 + index.html geliefert).
func TestHandlerServesEmbeddedFileViaFileServer(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.html", nil))

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("Status = %d, erwartet 301 (FileServer-Kanonisierung)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc == "" {
		t.Fatalf("Location-Header fehlt bei 301")
	}
}

// Unbekannter Nicht-Asset-Pfad → SPA-Fallback auf index.html (Status 200),
// damit Deep-Links im Single-Page-Frontend funktionieren.
func TestHandlerSPAFallback(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/board/xyz/deep-link", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("SPA-Fallback Status = %d, erwartet 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "aBoard") {
		t.Fatalf("SPA-Fallback lieferte nicht index.html: %q", rec.Body.String())
	}
}

// Nicht-GET/HEAD → 405 mit Allow-Header.
func TestHandlerMethodGuard(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Status = %d, erwartet 405", rec.Code)
	}
	if a := rec.Header().Get("Allow"); a != "GET, HEAD" {
		t.Fatalf("Allow = %q, erwartet \"GET, HEAD\"", a)
	}
}

// ServeMux-Precedence (Go 1.22): ein spezifischeres API-Muster muss den
// "/"-Catch-all schlagen — sonst würde der Frontend-Handler die API schatten
// (genau die Regression, die F-248 verhindern soll).
func TestServeMuxPrecedenceAPIWinsOverCatchAll(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("HEALTH-OK"))
	})
	mux.Handle("/", Handler())

	// /healthz → API-Handler, nicht das Frontend
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := rec.Body.String(); got != "HEALTH-OK" {
		t.Fatalf("/healthz lieferte %q — Catch-all schattet die API", got)
	}

	// / → Frontend
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rec.Body.String(), "aBoard") {
		t.Fatalf("/ lieferte nicht das Frontend: %q", rec.Body.String())
	}
}
