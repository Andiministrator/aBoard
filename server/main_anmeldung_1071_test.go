package main

// Karte #1071 (ADR-0149 §1, §4): Verdrahtung mehrerer Anbieter und die
// englischen Namen der Vorab-Liste. Testdaten nur mit example-Adressen.

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

func setzeZweiAnbieter(t *testing.T) {
	t.Helper()
	setzeOIDCUmgebung(t)
	for k, v := range map[string]string{
		"ABOARD_OIDC_ISSUER": "", "ABOARD_OIDC_CLIENT_ID": "", "ABOARD_OIDC_CLIENT_SECRET": "",
		"ABOARD_OIDC_PROVIDERS":        "google,authelia",
		"ABOARD_OIDC_GOOGLE_CLIENT_ID": "g", "ABOARD_OIDC_GOOGLE_CLIENT_SECRET": "gs",
		// nicht erreichbar: die Einrichtung muss trotzdem gelingen
		"ABOARD_OIDC_AUTHELIA_ISSUER": "https://127.0.0.1:1", "ABOARD_OIDC_AUTHELIA_CLIENT_ID": "a",
		"ABOARD_OIDC_AUTHELIA_CLIENT_SECRET": "as", "ABOARD_OIDC_AUTHELIA_LABEL": "Heim",
	} {
		t.Setenv(k, v)
	}
}

func TestAnmeldungKonfig1071_ZweiAnbieterUndNamen(t *testing.T) {
	setzeZweiAnbieter(t)
	if err := checkAnmeldungKonfig(); err != nil {
		t.Fatalf("zwei Anbieter: %v", err)
	}
	t.Setenv("ABOARD_OIDC_AUTHELIA_CLIENT_SECRET", "")
	if err := checkAnmeldungKonfig(); err == nil || !strings.Contains(err.Error(), "ABOARD_OIDC_AUTHELIA_CLIENT_SECRET") {
		t.Fatalf("halb eingerichteter Anbieter: %v", err)
	}
	t.Setenv("ABOARD_OIDC_AUTHELIA_CLIENT_SECRET", "as")

	// Vorab-Liste unter beiden Namen: gleich ok, verschieden Abbruch.
	t.Setenv(api.VorabListeEnv, "anna@example.com")
	if err := checkAnmeldungKonfig(); err != nil {
		t.Fatalf("beide Namen gleich: %v", err)
	}
	t.Setenv(api.VorabListeEnv, "bert@example.com")
	if err := checkAnmeldungKonfig(); err == nil || !strings.Contains(err.Error(), api.VorabListeEnv) {
		t.Fatalf("Namen widersprechen sich: %v", err)
	}
	t.Setenv(api.VorabListeEnvAlt, "")
	t.Setenv(api.VorabListeEnv, "@example")
	if err := checkAnmeldungKonfig(); err == nil {
		t.Fatalf("ungültiger Eintrag unter dem neuen Namen angenommen")
	}
}

func TestAnmeldungEinrichten1071_ZweiAnbieter(t *testing.T) {
	setzeZweiAnbieter(t)
	t.Setenv(api.VorabListeEnvAlt, "")
	t.Setenv(api.VorabListeEnv, "anna@example.com")
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
		t.Fatalf("anmeldungEinrichten: %v", err)
	}
	h := hauptHandler(mux, func(h http.Handler) http.Handler { return h }, nil, false, opts...)
	hole := func(pfad string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://board.example"+pfad, nil))
		return rec
	}
	// Ohne Kürzel führt der Start zur Anmeldeseite, die beide Knöpfe zeigt.
	if rec := hole(api.AnmeldungStartPfad); rec.Code != http.StatusFound || rec.Header().Get("Location") != api.AnmeldungSeitePfad {
		t.Fatalf("Start ohne Kürzel: %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	rec := hole(api.AnmeldungSeitePfad)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Mit Google anmelden") || !strings.Contains(rec.Body.String(), "Mit Heim anmelden") {
		t.Fatalf("Auswahl: %d %s", rec.Code, rec.Body.String())
	}
	// Der nicht erreichbare Anbieter antwortet 503; der andere ist davon unberührt
	// (er ist als eigener Eintrag verdrahtet, nicht über die Auswahl blockiert).
	if rec := hole(api.AnmeldungStartPfad + "?anbieter=authelia"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unerreichbarer Anbieter: %d, erwartet 503", rec.Code)
	}
	// Rückruf je Kürzel ist verdrahtet (ohne Cookie: 400, nicht der 404-Platzhalter
	// und nicht die App).
	for _, k := range []string{"google", "authelia"} {
		if rec := hole(api.AnmeldungRueckrufPfad + "/" + k); rec.Code != http.StatusBadRequest {
			t.Errorf("Rückruf %s: %d, erwartet 400", k, rec.Code)
		}
	}
	if rec := hole(api.AnmeldungRueckrufPfad + "/github"); rec.Code != http.StatusNotFound {
		t.Errorf("unbekanntes Kürzel: %d", rec.Code)
	}
	// Kein standard eingerichtet: der alte Rückruf ist unbekannt.
	if rec := hole(api.AnmeldungRueckrufPfad); rec.Code != http.StatusNotFound {
		t.Errorf("alter Rückruf ohne standard: %d", rec.Code)
	}
}
