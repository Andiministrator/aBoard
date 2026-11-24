package llm

// schleuse_lokal_umleitung_test.go — Kritiker KS-4 (Sprint-82-Abschluss,
// Runde 2): Der Client der zweiten Stufe folgte Redirects; bei 307/308 ging
// der noch nicht durchgesehene Text ein zweites Mal hinaus, an ein Ziel, das
// niemand geprüft hat.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Andiministrator/aBoard/server/ausgang"
)

func TestSchleuseModellAufruf_FolgtKeinerUmleitung(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(false)))
	var amZiel atomic.Int32
	var zielBody atomic.Value
	ziel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		amZiel.Add(1)
		roh, _ := io.ReadAll(r.Body)
		zielBody.Store(string(roh))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"lokal","choices":[{"finish_reason":"stop","message":{"content":"{\"funde\":[]}"}}]}`)
	}))
	defer ziel.Close()
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect, http.StatusFound} {
		lokal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, ziel.URL+r.URL.Path, code)
		}))
		f, _ := NeuerFirmenmodus("server-key", []string{"lokal"})
		aufruf := SchleuseModellAufruf(f, lokal.URL+"/v1", "ua", "lokal")
		_, err := aufruf(context.Background(), "SYSTEM", "GEHEIMER AUSZUG")
		lokal.Close()
		if err == nil {
			t.Errorf("%d: Umleitung als Erfolg gewertet", code)
		}
	}
	if n := amZiel.Load(); n != 0 {
		t.Errorf("Umleitungsziel erreicht (%d-mal), Body %q", n, zielBody.Load())
	}
}
