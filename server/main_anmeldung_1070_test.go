package main

// Karte #1070 (ADR-0149 §1, §5.1, §5.8): Schalter builtin neben oidc,
// Fail-closed per Präfix, ein Tor über die echte Kette. Testdaten nur mit
// example-Adressen.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestAnmeldungKonfig1070_Schalter(t *testing.T) {
	for _, f := range []struct {
		name, modus, alt string
		ok               bool
	}{
		{"pm unverändert", "", "oidc", true},
		{"neuer Name", "builtin", "", true},
		{"neuer Name, alter Wert", "OIDC", "", true},
		{"beide, gleiche Bedeutung", "builtin", "oidc", true},
		{"beide gleich", "builtin", "builtin", true},
		{"neuer Name ungültig", "passwort", "", false},
		{"alter Name ungültig neben neuem", "builtin", "ja", false},
	} {
		t.Run(f.name, func(t *testing.T) {
			setzeOIDCUmgebung(t)
			t.Setenv(api.AuthModeEnvVar, f.modus)
			t.Setenv(api.AnmeldungEnvVar, f.alt)
			err := checkAnmeldungKonfig()
			if (err == nil) != f.ok {
				t.Fatalf("checkAnmeldungKonfig: %v, erwartet ok=%v", err, f.ok)
			}
			if f.ok && !api.AnmeldungOIDCAktiv() {
				t.Errorf("eingebaute Anmeldung nicht aktiv")
			}
		})
	}
}

// ADR-0149 §1 Kritik W2: Jede ABOARD_OIDC_*- und ABOARD_AUTH_*-Variable ohne
// Schalter hält den Start an — auch Namen, die es erst mit #1071/#1072 gibt.
func TestAnmeldungKonfig1070_FailClosedPerPraefix(t *testing.T) {
	for _, v := range []string{"ABOARD_AUTH_METHODS", "ABOARD_INVITE_METHODS", "ABOARD_OIDC_PROVIDERS",
		"ABOARD_OIDC_GOOGLE_CLIENT_ID", "ABOARD_OIDC_ALLOWED", "ABOARD_OIDC_ZUGELASSEN"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(api.AuthModeEnvVar, "")
			t.Setenv(api.AnmeldungEnvVar, "")
			for _, w := range []string{"ABOARD_OIDC_ISSUER", "ABOARD_OIDC_CLIENT_ID", "ABOARD_OIDC_CLIENT_SECRET", "ABOARD_OIDC_ZUGELASSEN"} {
				t.Setenv(w, "")
			}
			t.Setenv(v, "x")
			err := checkAnmeldungKonfig()
			if err == nil || !strings.Contains(err.Error(), v) {
				t.Fatalf("%s ohne Schalter: %v, erwartet Startabbruch mit Namen", v, err)
			}
		})
	}
	if got := gesetzteAnmeldeVariablen([]string{"ABOARD_AUTH_MODE=builtin", "ABOARD_AUTH_METHODS=", "ABOARD_OIDCX=1",
		"ABOARD_OIDC_B=1", "ABOARD_AUTH_A=1", "PATH=/bin"}); strings.Join(got, ",") != "ABOARD_AUTH_A,ABOARD_OIDC_B" {
		t.Errorf("gesetzteAnmeldeVariablen: %v", got)
	}
}

// Das Tor über die echte Kette (anmeldungEinrichten + hauptHandler):
// aktives Konto ohne Listeneintrag kommt herein und überlebt einen Neustart;
// gesperrt ist draussen, auch auf der Liste; die Ausnahme-Pfade antworten
// ohne Sitzung mit ihrem Platzhalter statt mit der App.
func TestAnmeldungEinrichten1070_TorUeberDieKette(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv(api.AnmeldungEnvVar, "")
	t.Setenv(api.AuthModeEnvVar, "builtin")
	t.Setenv("ABOARD_OIDC_ISSUER", "https://127.0.0.1:1")
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, abbrechen := context.WithCancel(context.Background())
	t.Cleanup(abbrechen)

	baue := func() http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
		opts, err := anmeldungEinrichten(ctx, db, mux, "https://board.example")
		if err != nil || len(opts) != 1 {
			t.Fatalf("anmeldungEinrichten: %v (%d Optionen)", err, len(opts))
		}
		return hauptHandler(mux, func(h http.Handler) http.Handler { return h }, nil, false, opts...)
	}
	// Carla steht nicht auf der Liste (nur Anna), hat aber ein aktives Konto.
	if _, err := db.Exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-carla', 'carla@example.com', 'active', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	sitz := storage.NewSitzungRepo(db)
	carla, _, err := sitz.Anlegen(ctx, "carla@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	anna, _, err := sitz.Anlegen(ctx, "anna@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	anfrage := func(h http.Handler, id string) int {
		r := httptest.NewRequest(http.MethodGet, "https://board.example/boards", nil)
		r.AddCookie(&http.Cookie{Name: api.SitzungsCookie, Value: id})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	h := baue() // Start: BeendeNichtZugelassene läuft
	if c := anfrage(h, carla); c != http.StatusTeapot {
		t.Fatalf("aktives Konto nach Neustart: %d, erwartet durchgelassen", c)
	}
	if c := anfrage(h, anna); c != http.StatusTeapot {
		t.Fatalf("Anna (Liste): %d", c)
	}

	// Anna wird gesperrt, obwohl sie auf der Liste steht. Die laufende Kette
	// merkt es spätestens nach TorCacheTTL; der Neustart sofort und räumt ihre
	// Sitzung ab.
	if _, err := db.Exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-anna', 'anna@example.com', 'blocked', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	h = baue()
	if c := anfrage(h, anna); c != http.StatusUnauthorized {
		t.Fatalf("gesperrt trotz Liste: %d, erwartet 401", c)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sitzungen WHERE email = 'anna@example.com'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Sitzung der Gesperrten nach Neustart: %d (%v), erwartet 0", n, err)
	}

	// Ausnahme-Pfade ohne Sitzung: Platzhalter, nie die App (418 hier).
	token := strings.Repeat("A", api.EinladungTokenLaenge)
	for _, f := range []struct {
		methode, pfad string
		code          int
	}{
		// Seit #1074: ABOARD_AUTH_METHODS leer = auch der Passwort-Weg; die
		// Seite leitet nicht mehr direkt weiter (ADR-0149 §1, „Folge für
		// bestehende Betriebe“), und POST /anmeldung/passwort ist gebaut
		// (leere Eingabe → 401 „stimmt nicht“).
		{http.MethodGet, "/anmeldung", http.StatusOK},
		{http.MethodGet, "/anmeldung/rueckruf/google", http.StatusNotFound},
		{http.MethodPost, "/anmeldung/passwort", http.StatusUnauthorized},
		// Seit #1075 gebaut: ohne Zwischen-Cookie 401 „abgelaufen“.
		{http.MethodPost, "/anmeldung/code", http.StatusUnauthorized},
		{http.MethodGet, "/einladung/" + token, http.StatusNotFound},
		{http.MethodPost, "/einladung/" + token, http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(f.methode, "https://board.example"+f.pfad, nil))
		if rec.Code != f.code {
			t.Errorf("%s %s ohne Sitzung: %d, erwartet %d", f.methode, f.pfad, rec.Code, f.code)
		}
		if f.code == http.StatusFound && rec.Header().Get("Location") != api.AnmeldungStartPfad+"?anbieter=standard" {
			t.Errorf("%s %s: Location %q", f.methode, f.pfad, rec.Header().Get("Location"))
		}
	}
}
