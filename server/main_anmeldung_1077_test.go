package main

// Karte #1077 (ADR-0149 §1, §5.4): ABOARD_INVITE_METHODS als echte Auswahl
// und das Einlösen über die echte Kette. Testdaten nur example.org.

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

func TestEinladungsWege1077_Startpruefung(t *testing.T) {
	for _, f := range []struct {
		name, auth, invite string
		ok                 bool
	}{
		{"leer: alle Wege", "", "", true},
		{"Teilmenge", "", "standard", true},
		{"nur Passwort", "", "password", true},
		{"nicht eingerichtet", "", "google", false},
		{"nicht angeboten", "standard", "password", false},
		{"doppelt", "", "password,password", false},
	} {
		t.Run(f.name, func(t *testing.T) {
			setzeOIDCUmgebung(t)
			t.Setenv(api.AuthModeEnvVar, "builtin")
			t.Setenv(api.AuthMethodsEnv, f.auth)
			t.Setenv(api.InviteMethodsEnv, f.invite)
			err := checkAnmeldungKonfig()
			if (err == nil) != f.ok {
				t.Fatalf("%v, erwartet ok=%v", err, f.ok)
			}
			if err != nil && !strings.Contains(err.Error(), api.InviteMethodsEnv) {
				t.Errorf("Meldung nennt %s nicht: %v", api.InviteMethodsEnv, err)
			}
		})
	}
}

// Ohne Sitzung erreicht die Person die Einlöse-Seite über die echte Kette
// (anmeldungEinrichten + hauptHandler), nie die App; die Seite bietet nur die
// Wege aus ABOARD_INVITE_METHODS an.
func TestAnmeldungEinrichten1077_EinloeseSeiteUeberDieKette(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv(api.AnmeldungEnvVar, "")
	t.Setenv(api.AuthModeEnvVar, "builtin")
	t.Setenv("ABOARD_OIDC_ISSUER", "https://127.0.0.1:1")
	t.Setenv(api.InviteMethodsEnv, "password")
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, abbrechen := context.WithCancel(context.Background())
	t.Cleanup(abbrechen)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	opts, err := anmeldungEinrichten(ctx, db, mux, "https://board.example")
	if err != nil || len(opts) != 1 {
		t.Fatalf("anmeldungEinrichten: %v (%d)", err, len(opts))
	}
	h := hauptHandler(mux, func(h http.Handler) http.Handler { return h }, nil, false, opts...)
	e, err := storage.NewEinladungRepo(db).Einladen(ctx, "anna@example.org")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+api.EinladungPfadPraefix+e.Token, nil)
	r.Header.Set("Accept", "text/html")
	h.ServeHTTP(rec, r)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "anna@example.org") || !strings.Contains(body, `name="passwort2"`) ||
		strings.Contains(body, "Mit 127.0.0.1") {
		t.Fatalf("Einlöse-Seite: %d %s", rec.Code, body)
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Referrer-Policy %q", rec.Header().Get("Referrer-Policy"))
	}
}
