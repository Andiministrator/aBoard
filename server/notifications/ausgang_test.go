package notifications

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Andiministrator/aBoard/server/ausgang"
	"github.com/Andiministrator/aBoard/server/storage"
)

// Karte #569: Gotify samt Testversand baut bei ABOARD_AUSGAENGE=aus keine
// Verbindung auf. Gotify verlangt https — deshalb ein TLS-httptest-Server,
// dessen Client-Transport das Test-Zertifikat kennt; der Schalter sitzt als
// ausgang.Transport davor wie im Standard-Client.
func TestAusgang_GotifyTestversand(t *testing.T) {
	var treffer atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		treffer.Add(1)
	}))
	defer srv.Close()
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true).MitGotifyIntern(strings.TrimPrefix(srv.URL, "https://"))))

	client := &http.Client{Transport: ausgang.Transport(srv.Client().Transport)}
	g := NewGotifySender(nil, client)
	cfg := storage.GotifyConfig{URL: srv.URL, Token: "t"}
	if err := g.TestConnection(context.Background(), cfg); !errors.Is(err, ausgang.ErrGesperrt) {
		t.Fatalf("erwartet ErrGesperrt, bekam %v", err)
	}
	if treffer.Load() != 0 {
		t.Fatalf("%d Anfrage(n) trotz aus", treffer.Load())
	}
	ausgang.Setze(ausgang.Neu(true, strings.TrimPrefix(srv.URL, "https://")).MitGotifyIntern(strings.TrimPrefix(srv.URL, "https://")))
	if err := g.TestConnection(context.Background(), cfg); err != nil {
		t.Fatalf("erlaubtes Ziel: %v", err)
	}
	if treffer.Load() != 1 {
		t.Fatalf("Gegenprobe: %d Anfragen, erwartet 1", treffer.Load())
	}
}

// Der Standard-Client (client nil, so verdrahtet main.go) trägt den Schalter.
func TestAusgang_GotifyStandardClient(t *testing.T) {
	defer ausgang.Setze(ausgang.Setze(ausgang.Neu(true)))
	g := NewGotifySender(nil, nil)
	err := g.TestConnection(context.Background(), storage.GotifyConfig{URL: "https://gotify.example.invalid", Token: "t"})
	if !errors.Is(err, ausgang.ErrGesperrt) {
		t.Fatalf("Standard-Client läuft am Schalter vorbei: %v", err)
	}
}
