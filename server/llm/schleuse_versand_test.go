package llm

// schleuse_versand_test.go — Der echte Versand-Transport (ohne Test-Umlenkung)
// lehnt für jede Anbieter-Art ein internes Ziel ab, bevor eine Verbindung
// zustande kommt (Karte #686, Kritiker KS3-10; Messung des Kritikers
// übernommen).

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/schleuse"
)

func TestSchleuseVersenden_EchterTransportLehntInterneZieleAb(t *testing.T) {
	var treffer int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&treffer, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	for _, art := range []string{schleuse.AnbieterAnthropic, schleuse.AnbieterOpenAIKompatibel, schleuse.AnbieterMistral, schleuse.AnbieterGoogle} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := SchleuseVersenden(ctx, SchleuseVersand{Anbieter: art, BasisURL: srv.URL, Modell: "m", Schluessel: "k", Text: "x", UA: "t"})
		cancel()
		if g := SchleuseFehlerGrund(err); g != "interne-adresse" {
			t.Errorf("%s: grund %q (err %v)", art, g, err)
		}
	}
	if n := atomic.LoadInt32(&treffer); n != 0 {
		t.Errorf("internes Ziel erreicht: %d Anfragen", n)
	}
}

// Karte #687 (ADR-0134 §5): Freigegebene Bilder gehen mit der Anfrage hinaus
// — bei jeder Anbieter-Art, die Bilder senden kann. Mistral kann es nicht:
// Dort kommt keine Anfrage zustande, statt die Bilder still wegzulassen.
func TestSchleuseVersenden_BilderGehenMit(t *testing.T) {
	bild := []byte("\x89PNG\r\n\x1a\nbereinigte-bytes-des-bildes")
	kodiert := base64.StdEncoding.EncodeToString(bild)
	var koerper atomic.Value
	var treffer int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&treffer, 1)
		b, _ := io.ReadAll(r.Body)
		koerper.Store(string(b))
		w.WriteHeader(http.StatusBadRequest) // Antwort egal — geprüft wird die Anfrage
	}))
	defer srv.Close()
	for _, art := range []string{schleuse.AnbieterAnthropic, schleuse.AnbieterOpenAIKompatibel, schleuse.AnbieterGoogle} {
		koerper.Store("")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = SchleuseVersenden(ctx, SchleuseVersand{Anbieter: art, BasisURL: srv.URL, Modell: "m", Schluessel: "k",
			Text: "Auszug", UA: "t", Transport: http.DefaultTransport,
			Bilder: []ImageAttachment{{ID: "a1", Filename: "geheim-Anna.png", MIME: "image/png", Data: bild}}})
		cancel()
		k, _ := koerper.Load().(string)
		if !strings.Contains(k, kodiert) {
			t.Errorf("%s: Bild nicht in der Anfrage: %.200s", art, k)
		}
		if strings.Contains(k, "geheim-Anna") {
			t.Errorf("%s: Dateiname geht mit hinaus", art)
		}
	}
	vorher := atomic.LoadInt32(&treffer)
	_, err := SchleuseVersenden(context.Background(), SchleuseVersand{Anbieter: schleuse.AnbieterMistral, BasisURL: srv.URL,
		Modell: "m", Schluessel: "k", Text: "Auszug", UA: "t", Transport: http.DefaultTransport,
		Bilder: []ImageAttachment{{ID: "a1", MIME: "image/png", Data: bild}}})
	if !errors.Is(err, ErrSchleuseKeineBilder) || SchleuseFehlerGrund(err) != "keine-bilder" {
		t.Fatalf("mistral mit Bild: %v", err)
	}
	if atomic.LoadInt32(&treffer) != vorher {
		t.Fatal("mistral: Anfrage trotz Bildern ohne Bildversand gesendet")
	}
}
