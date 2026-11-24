package api

// Karte #1072 (ADR-0149 §1, §2): Anmeldeseite /anmeldung, Sprache,
// ABOARD_AUTH_METHODS. Testdaten nur mit example-Adressen.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// seitenTabelle: Anbieter mit unerreichbarem Issuer — die Seite darf nie
// die Discovery abfragen.
func seitenTabelle(labels ...string) (*Anmeldungen, *http.ServeMux) {
	t := &Anmeldungen{}
	kuerzel := []string{"google", "authelia", "heim"}
	for i, l := range labels {
		t.Anbieter = append(t.Anbieter, &OIDCAnmeldung{Kuerzel: kuerzel[i], Label: l, Issuer: "https://127.0.0.1:1",
			ClientID: "c", ClientSecret: "s", Basis: "https://board.example", Schluessel: AnmeldeSchluessel("k")})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+AnmeldungSeitePfad, t.Seite)
	mux.HandleFunc("GET "+AnmeldungStartPfad, t.Start)
	mux.HandleFunc("GET "+AnmeldungRueckrufPfad+"/{kuerzel}", t.RueckrufKuerzel)
	return t, mux
}

func holeSeite(mux *http.ServeMux, pfad, sprache string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+pfad, nil)
	if sprache != "" {
		r.Header.Set("Accept-Language", sprache)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func TestAnmeldeSeite1072_ZweiAnbieter(t *testing.T) {
	_, mux := seitenTabelle("Google", "Heim <script>alert(1)</script>")
	rec := holeSeite(mux, AnmeldungSeitePfad, "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("Status %d", rec.Code)
	}
	for _, want := range []string{
		`href="/anmeldung/start?anbieter=google"`, `href="/anmeldung/start?anbieter=authelia"`,
		">Mit Google anmelden<", "Mit Heim &lt;script&gt;alert(1)&lt;/script&gt; anmelden",
		`<html lang="de">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fehlt %q in %s", want, body)
		}
	}
	if strings.Contains(body, "<script>") {
		t.Errorf("Label roh ausgegeben: %s", body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("Content-Security-Policy") != AnmeldeSeitenCSP {
		t.Errorf("CSP %q", rec.Header().Get("Content-Security-Policy"))
	}
	if rec.Header().Get("Content-Language") != "de" {
		t.Errorf("Content-Language %q", rec.Header().Get("Content-Language"))
	}
	// Kontowahl wird an jeden Link weitergereicht; andere Werte nicht.
	body = holeSeite(mux, AnmeldungSeitePfad+"?konto=1", "").Body.String()
	for _, k := range []string{"google", "authelia"} {
		if !strings.Contains(body, `href="/anmeldung/start?anbieter=`+k+`&amp;konto=1"`) {
			t.Errorf("konto=1 nicht an %s weitergereicht: %s", k, body)
		}
	}
	if body := holeSeite(mux, AnmeldungSeitePfad+"?konto=2", "").Body.String(); strings.Contains(body, "konto=") {
		t.Errorf("konto=2 weitergereicht: %s", body)
	}
}

func TestAnmeldeSeite1072_EinAnbieterLeitetDirekt(t *testing.T) {
	_, mux := seitenTabelle("Google")
	rec := holeSeite(mux, AnmeldungSeitePfad, "")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/anmeldung/start?anbieter=google" {
		t.Fatalf("ein Anbieter: %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	rec = holeSeite(mux, AnmeldungSeitePfad+"?konto=1", "")
	if rec.Header().Get("Location") != "/anmeldung/start?anbieter=google&konto=1" {
		t.Fatalf("ein Anbieter mit Kontowahl: Location=%q", rec.Header().Get("Location"))
	}
}

func TestAnmeldeSeite1072_OhneErreichbarenAnbieter(t *testing.T) {
	_, mux := seitenTabelle("Google", "Heim")
	t0 := time.Now()
	rec := holeSeite(mux, AnmeldungSeitePfad, "")
	if rec.Code != http.StatusOK || time.Since(t0) > time.Second {
		t.Fatalf("Seite mit unerreichbarem Issuer: %d nach %v", rec.Code, time.Since(t0))
	}
	// Erst der Klick auf den unerreichbaren Anbieter meldet 503.
	if rec := holeSeite(mux, AnmeldungStartPfad+"?anbieter=authelia", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("Start des unerreichbaren Anbieters: %d", rec.Code)
	}
}

func TestAnmeldeSeite1072_Sprache(t *testing.T) {
	for kopf, want := range map[string]string{
		"en-US,en;q=0.9":     "en",
		"fr, en;q=0.5":       "en",
		"fr":                 "de",
		"de;q=0.1, en;q=0.9": "en",
		"":                   "de",
		"en;q=0, de":         "de",
		"EN":                 "en",
		"de-AT, en":          "de",
		"en;q=abc":           "de",
		"*":                  "de",
	} {
		if got := WaehleSprache(kopf); got != want {
			t.Errorf("WaehleSprache(%q) = %q, erwartet %q", kopf, got, want)
		}
	}
	_, mux := seitenTabelle("Google", "Heim")
	rec := holeSeite(mux, AnmeldungSeitePfad, "en-US,en;q=0.9")
	body := rec.Body.String()
	if rec.Header().Get("Content-Language") != "en" || !strings.Contains(body, `<html lang="en">`) ||
		!strings.Contains(body, "Sign in with Google") || !strings.Contains(body, "Choose how to sign in.") {
		t.Errorf("englische Seite: %s", body)
	}
}

func TestAnmeldungStart1072_OhneKuerzelZurSeite(t *testing.T) {
	_, mux := seitenTabelle("Google", "Heim")
	rec := holeSeite(mux, AnmeldungStartPfad, "")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/anmeldung" {
		t.Fatalf("Start ohne Kürzel: %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	rec = holeSeite(mux, AnmeldungStartPfad+"?konto=1", "")
	if rec.Header().Get("Location") != "/anmeldung?konto=1" {
		t.Fatalf("Start ohne Kürzel mit Kontowahl: Location=%q", rec.Header().Get("Location"))
	}
}

func TestFiltereAnbieter1072(t *testing.T) {
	alle := []AnbieterKonfig{{Kuerzel: "standard"}, {Kuerzel: "google"}, {Kuerzel: "authelia"}}
	namen := func(l []AnbieterKonfig) string {
		var s []string
		for _, a := range l {
			s = append(s, a.Kuerzel)
		}
		return strings.Join(s, ",")
	}
	for _, leer := range []string{"", "  "} {
		if got, f := FiltereAnbieter(alle, leer); len(f) != 0 || namen(got) != "standard,google,authelia" {
			t.Errorf("leer %q: %s %v", leer, namen(got), f)
		}
	}
	// Teilmenge, Reihenfolge der Tabelle bleibt.
	if got, f := FiltereAnbieter(alle, "authelia, standard"); len(f) != 0 || namen(got) != "standard,authelia" {
		t.Errorf("Teilmenge: %s %v", namen(got), f)
	}
	for _, fall := range []struct{ methoden, want string }{
		{"googel", `"googel" ist kein eingerichteter Anmeldeweg`},
		{"google,password,google", `"google" steht doppelt`},
		{"google,google", `"google" steht doppelt`},
		{" , ", "enthält keinen Anmeldeweg"},
	} {
		got, f := FiltereAnbieter(alle, fall.methoden)
		if len(got) != 0 || len(f) == 0 || !strings.Contains(strings.Join(f, "|"), fall.want) {
			t.Errorf("%q: %v %v, erwartet Fehler mit %q", fall.methoden, namen(got), f, fall.want)
		}
	}
}

func TestAnbieterMitAuswahl1072(t *testing.T) {
	basis := map[string]string{
		"ABOARD_OIDC_PROVIDERS":        "google,authelia",
		"ABOARD_OIDC_GOOGLE_CLIENT_ID": "g", "ABOARD_OIDC_GOOGLE_CLIENT_SECRET": "gs",
		"ABOARD_OIDC_AUTHELIA_ISSUER": "https://auth.example.org", "ABOARD_OIDC_AUTHELIA_CLIENT_ID": "a",
		"ABOARD_OIDC_AUTHELIA_CLIENT_SECRET": "as",
	}
	mit := func(methoden string) ([]AnbieterKonfig, []string) {
		m := map[string]string{AuthMethodsEnv: methoden}
		for k, v := range basis {
			m[k] = v
		}
		getenv, env := umgebung(m)
		a, _, f := AnbieterMitAuswahl(getenv, env)
		return a, f
	}
	if a, f := mit(""); len(a) != 2 || len(f) != 0 {
		t.Errorf("leer: %v %v", a, f)
	}
	if a, f := mit("authelia"); len(a) != 1 || a[0].Kuerzel != "authelia" || len(f) != 0 {
		t.Errorf("authelia: %v %v", a, f)
	}
	if _, f := mit("authelia,passwort"); len(f) == 0 {
		t.Errorf("Tippfehler angenommen")
	}
}

func TestSitzungsweiche1072_SeitenaufrufZurAnmeldeseite(t *testing.T) {
	// Der Seitenaufruf ohne Sitzung landet auf /anmeldung (nicht /anmeldung/start).
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/board", nil)
	r.Header.Set("Accept", "text/html")
	ohneSitzungAbweisen(rec, r)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/anmeldung" {
		t.Fatalf("%d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
}
