package main

// Karte #1074: Passwort-Weg über die echte Kette (anmeldungEinrichten +
// hauptHandler, mit CSRF-Schutz und Sitzungsweiche). Testdaten nur example.org.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func baueKette1074(t *testing.T) (http.Handler, *storage.KontoRepo) {
	t.Helper()
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
		t.Fatalf("anmeldungEinrichten: %v (%d Optionen)", err, len(opts))
	}
	if _, err := db.Exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-dora', 'dora@example.org', 'active', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	konten := storage.NewKontoRepo(db)
	h, err := api.PasswortHashen(ctx, "doras-langes-passwort")
	if err != nil {
		t.Fatal(err)
	}
	if err := konten.SetzePasswortHash(ctx, "k-dora", h, 2); err != nil {
		t.Fatal(err)
	}
	return hauptHandler(mux, func(h http.Handler) http.Handler { return h }, nil, false, opts...), konten
}

func postPasswort(h http.Handler, email, pw string, kopf ...string) *httptest.ResponseRecorder {
	form := url.Values{"email": {email}, "passwort": {pw}}
	r := httptest.NewRequest(http.MethodPost, "https://board.example"+api.AnmeldungPasswortPfad, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for i := 0; i+1 < len(kopf); i += 2 {
		r.Header.Set(kopf[i], kopf[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// Nur der Passwort-Weg (keine OIDC-Variable): Der Server startet, die Seite
// zeigt das Formular, Abmelden funktioniert, und eine richtige Eingabe endet
// ohne 2FA-Schritt (#1075) bei „Zwei-Faktor fehlt“ — nie mit einer Sitzung.
func TestAnmeldung1074_NurPasswort(t *testing.T) {
	setzeOIDCUmgebung(t)
	for _, v := range []string{"ABOARD_OIDC_ISSUER", "ABOARD_OIDC_CLIENT_ID", "ABOARD_OIDC_CLIENT_SECRET", "ABOARD_ANMELDUNG"} {
		t.Setenv(v, "")
	}
	t.Setenv(api.AuthModeEnvVar, "builtin")
	if err := checkAnmeldungKonfig(); err != nil {
		t.Fatalf("Startprüfung nur Passwort: %v", err)
	}
	h, _ := baueKette1074(t)

	for pfad, inhalt := range map[string]string{
		api.AnmeldungSeitePfad:      `action="/anmeldung/passwort"`,
		api.AnmeldungAbgemeldetPfad: "abgemeldet",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://board.example"+pfad, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), inhalt) {
			t.Errorf("GET %s: %d\n%s", pfad, rec.Code, rec.Body.String())
		}
	}

	// Falsches Passwort: 401 von der Anmeldung, nicht von der Weiche.
	rec := postPasswort(h, "dora@example.org", "falsches-passwort")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "stimmt nicht") {
		t.Fatalf("falsch: %d %s", rec.Code, rec.Body.String())
	}
	// Direkt danach gilt die Wartezeit der Adresse (1 s), auch für das
	// richtige Passwort. Den Weg bis „Zwei-Faktor fehlt“ prüft
	// TestAnmeldung1074_RichtigOhneZweiFaktor.
	rec = postPasswort(h, "dora@example.org", "doras-langes-passwort")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("Wartezeit nach Fehlversuch: %d", rec.Code)
	}
}

func TestAnmeldung1074_RichtigOhneZweiFaktor(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv(api.AuthModeEnvVar, "builtin")
	t.Setenv("ABOARD_ANMELDUNG", "")
	t.Setenv("ABOARD_OIDC_ISSUER", "https://127.0.0.1:1")
	h, _ := baueKette1074(t)
	rec := postPasswort(h, "dora@example.org", "doras-langes-passwort")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Zwei-Faktor fehlt") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Header().Values("Set-Cookie") {
		if strings.Contains(c, api.SitzungsCookie) {
			t.Fatalf("Sitzung ohne 2FA: %s", c)
		}
	}
	// Login-CSRF: ein Formular einer fremden Seite kommt nicht an.
	rec = postPasswort(h, "dora@example.org", "doras-langes-passwort", "Sec-Fetch-Site", "cross-site")
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "Zwei-Faktor") {
		t.Fatalf("cross-site: %d %s", rec.Code, rec.Body.String())
	}
	rec = postPasswort(h, "dora@example.org", "doras-langes-passwort", "Origin", "https://fremd.example")
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "Zwei-Faktor") {
		t.Fatalf("fremder Origin: %d %s", rec.Code, rec.Body.String())
	}
}

// ABOARD_AUTH_METHODS ohne `password` (Betrieb nur mit Anbieter-Anmeldung): wie
// vor #1074 — Direkt-Weiterleitung und kein Passwort-Weg.
func TestAnmeldung1074_OhnePasswortWeg(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv("ABOARD_OIDC_ISSUER", "https://127.0.0.1:1")
	t.Setenv(api.AuthMethodsEnv, "standard")
	h, _ := baueKette1074(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "https://board.example"+api.AnmeldungSeitePfad, nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != api.AnmeldungStartPfad+"?anbieter=standard" {
		t.Fatalf("Seite: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := postPasswort(h, "dora@example.org", "doras-langes-passwort"); rec.Code != http.StatusNotFound {
		t.Fatalf("Passwort-Weg trotz Ausschluss: %d", rec.Code)
	}
}
