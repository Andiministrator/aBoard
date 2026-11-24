package api

// Nacharbeit Sitzungs-Kritik 02.11. abends (Sprint 109, K1-1, K1-3, Testlücke
// gesperrt im ungebundenen Pfad). Testdaten nur mit example-Adressen.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// K1-1: Die eigene CSP der Anmeldeseiten ersetzt die aus withSecurityHeaders;
// sie muss das Einbetten selbst verbieten.
func TestNacharbeit109_AnmeldeseitenNichtEinbettbar(t *testing.T) {
	x := neuerMehrAnbieterTest(t, "anna@example.com")
	for name, h := range map[string]http.Handler{
		"Seite":      WithSecurityHeaders(http.HandlerFunc(x.tabelle.Seite)),
		"Abgemeldet": WithSecurityHeaders(http.HandlerFunc(x.tabelle.Anbieter[0].Abgemeldet)),
		"Kein Zugang": WithSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			textSeite(w, r, http.StatusForbidden, sNichtFrei, false)
		})),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://board.example/anmeldung", nil))
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: CSP %q ohne frame-ancestors 'none'", name, csp)
		}
	}
}

// K1-3: Zwei Kürzel mit demselben Aussteller hielten den Start nicht an —
// jede Bindung gälte dann für beide.
func TestNacharbeit109_GleicherIssuerZweimal(t *testing.T) {
	getenv, environ := umgebung(map[string]string{
		"ABOARD_OIDC_ISSUER": GoogleIssuer, "ABOARD_OIDC_CLIENT_ID": "c", "ABOARD_OIDC_CLIENT_SECRET": "s",
		"ABOARD_OIDC_PROVIDERS": "google", "ABOARD_OIDC_GOOGLE_CLIENT_ID": "g", "ABOARD_OIDC_GOOGLE_CLIENT_SECRET": "gs",
	})
	_, _, fehler := AnbieterAusUmgebung(getenv, environ)
	if !strings.Contains(strings.Join(fehler, "\n"), "denselben Issuer") {
		t.Fatalf("Fehler %v, erwartet Abbruch wegen doppeltem Issuer", fehler)
	}
}

// Testlücke aus K1: gesperrt und auf der Liste, noch ohne Bindung → 403, keine
// Bindung, keine Sitzung.
func TestNacharbeit109_GesperrtOhneBindung(t *testing.T) {
	x := neuerMehrAnbieterTest(t, "anna@example.com")
	x.exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-anna', 'anna@example.com', 'blocked', 1, 1)`)
	c, q := x.starte(t, "google")
	rec := x.melde(t, x.google, "google", c, q, "g1", nil)
	if rec.Code != http.StatusForbidden || sitzungsCookie(rec) != nil {
		t.Fatalf("gesperrt ohne Bindung: %d, erwartet 403", rec.Code)
	}
	if _, err := x.tabelle.Anbieter[0].Bindungen.NachKonto(t.Context(), x.google.srv.URL, "sub-anna"); err == nil {
		t.Fatalf("Gesperrte wurde trotzdem gebunden")
	}
}
