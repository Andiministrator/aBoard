package api

// Kritiker Sprint-Abschluss 109 (F-2879): Die Log-Zeile bei vertauschtem
// Kürzel im Rückruf war nicht gedrosselt. Ein einmal geholtes Anmelde-Cookie
// lässt sich zehn Minuten lang beliebig oft am falschen Rückruf vorzeigen.
// Gegenprobe des Kritikers, übernommen aus tmp/sprint109/abschluss-k/.

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestAbschluss109_KuerzelLogGedrosselt(t *testing.T) {
	kuerzelLogZuletzt.Store(0)
	t.Cleanup(func() { kuerzelLogZuletzt.Store(0) })
	x := neuerMehrAnbieterTest(t, "anna@example.org")
	c, q := x.starte(t, "authelia")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	for i := 0; i < 50; i++ {
		x.melde(t, x.google, "google", c, q, "v1", nil)
	}
	n := strings.Count(buf.String(), "für einen Vorgang bei")
	if n != 1 {
		t.Fatalf("%d Log-Zeilen nach 50 vertauschten Rückrufen mit demselben Cookie, erwartet genau 1 (gedrosselt)", n)
	}
}

// Verify Sprint-Abschluss 109 (F-2883): Auch wenn schon die Anbieter-Tabelle
// Fehler hat (früher Rückgabeweg in AnbieterMitAuswahl), meldet der Start den
// Tippfehler im Schalternamen gleich mit — nicht erst im nächsten Lauf.
func TestAbschluss109_SchalterFehlerTrotzAnbieterFehler(t *testing.T) {
	getenv, env := umgebung(map[string]string{
		"ABOARD_OIDC_PROVIDERS":       "authelia",
		"ABOARD_OIDC_AUTHELIA_ISSUER": "https://auth.example.org", // halb eingerichtet
		"ABOARD_AUTH_METHOD":          "authelia",                 // Tippfehler
	})
	_, _, fehler := AnbieterMitAuswahl(getenv, env)
	alle := strings.Join(fehler, "\n")
	if !strings.Contains(alle, "halb eingerichtet") {
		t.Errorf("Anbieter-Fehler fehlt: %q", alle)
	}
	if !strings.Contains(alle, "ABOARD_AUTH_METHOD ist unbekannt") {
		t.Errorf("Schalter-Fehler fehlt im frühen Rückgabeweg: %q", alle)
	}
}
