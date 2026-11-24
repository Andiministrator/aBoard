package api_test

// Tests für MitCSRFSchutz (Sprint-72-Abschluss, Kritiker S72-1).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

func TestCSRFSchutz(t *testing.T) {
	var erreicht int
	h := api.MitCSRFSchutz(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		erreicht++
		w.WriteHeader(http.StatusOK)
	}))
	faelle := []struct {
		name, methode string
		header        map[string]string
		wantStatus    int
	}{
		{"fremde Seite per Sec-Fetch-Site", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site", "Content-Type": "text/plain"}, http.StatusForbidden},
		{"Nachbar-Anwendung derselben Site", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"alter Browser: fremder Origin", http.MethodPost, map[string]string{"Origin": "https://boese.example"}, http.StatusForbidden},
		{"DELETE fremd", http.MethodDelete, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"eigene Seite", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://board.example"}, http.StatusOK},
		{"eigener Origin ohne Sec-Fetch-Site", http.MethodPost, map[string]string{"Origin": "https://board.example"}, http.StatusOK},
		{"kein Browser (curl, Agent)", http.MethodPost, nil, http.StatusOK},
		{"GET von fremder Seite bleibt erlaubt", http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusOK},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			erreicht = 0
			r := httptest.NewRequest(f.methode, "https://board.example/boards/b1/ops", strings.NewReader(`{"ops":[]}`))
			for k, v := range f.header {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != f.wantStatus {
				t.Fatalf("Status %d, erwartet %d", w.Code, f.wantStatus)
			}
			if f.wantStatus == http.StatusForbidden && erreicht != 0 {
				t.Fatal("abgewiesener Request hat den Handler trotzdem erreicht")
			}
		})
	}
}
