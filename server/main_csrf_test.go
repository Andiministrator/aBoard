package main

// Verdrahtung des CSRF-Schutzes im Menschen-Listener (Sprint-72-Abschluss,
// Kritiker S72-1): Der Test prüft die echte Kette aus menschenHandler, nicht
// nur die Middleware — ein vergessener Aufruf in main fiele sonst nicht auf.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMenschenHandler_WeistFremdeSchreibRequestsAb(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	var erreicht bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /boards/{id}/ops", func(w http.ResponseWriter, r *http.Request) {
		erreicht = true
		w.WriteHeader(http.StatusOK)
	})
	ohneACL := func(h http.Handler) http.Handler { return h }
	h := menschenHandler(mux, ohneACL)

	r := httptest.NewRequest(http.MethodPost, "https://board.example/boards/b1/ops", strings.NewReader("ops=x"))
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || erreicht {
		t.Fatalf("fremder POST: Status %d, Handler erreicht=%v — erwartet 403 ohne Durchgriff", w.Code, erreicht)
	}

	r = httptest.NewRequest(http.MethodPost, "https://board.example/boards/b1/ops", strings.NewReader(`{}`))
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !erreicht {
		t.Fatalf("eigener POST: Status %d, erreicht=%v — erwartet 200", w.Code, erreicht)
	}
}
