package main

// Karte #1072 (ADR-0149 §1, §2): /anmeldung über die echte Kette und der
// Filter ABOARD_AUTH_METHODS beim Start. Testdaten nur mit example-Adressen.

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

func TestAnmeldungKonfig1072_AuthMethods(t *testing.T) {
	setzeZweiAnbieter(t)
	t.Setenv(api.AuthMethodsEnv, "")
	if err := checkAnmeldungKonfig(); err != nil {
		t.Fatalf("leer: %v", err)
	}
	t.Setenv(api.AuthMethodsEnv, "google")
	if err := checkAnmeldungKonfig(); err != nil {
		t.Fatalf("Teilmenge: %v", err)
	}
	for wert, want := range map[string]string{
		"googel":            `"googel" ist kein eingerichteter Anmeldeweg`,
		"password,password": "steht doppelt",
		"google,google":     "steht doppelt",
		" , ":               "enthält keinen Anmeldeweg",
	} {
		t.Setenv(api.AuthMethodsEnv, wert)
		if err := checkAnmeldungKonfig(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ABOARD_AUTH_METHODS=%q: %v, erwartet Abbruch mit %q", wert, err, want)
		}
	}
	// Ohne Schalter bleibt die Variable fail-closed.
	t.Setenv(api.AuthMethodsEnv, "google")
	t.Setenv("ABOARD_ANMELDUNG", "")
	t.Setenv("ABOARD_AUTH_MODE", "")
	if err := checkAnmeldungKonfig(); err == nil || !strings.Contains(err.Error(), api.AuthMethodsEnv) {
		t.Errorf("ohne Schalter: %v", err)
	}
}

func einrichtenMitMethoden(t *testing.T, methoden string) http.Handler {
	t.Helper()
	setzeZweiAnbieter(t)
	t.Setenv(api.VorabListeEnvAlt, "")
	t.Setenv(api.VorabListeEnv, "anna@example.com")
	t.Setenv(api.AuthMethodsEnv, methoden)
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
	return hauptHandler(mux, func(h http.Handler) http.Handler { return h }, nil, false, opts...)
}

func hole1072(h http.Handler, pfad string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "https://board.example"+pfad, nil)
	r.Header.Set("Accept", "text/html")
	h.ServeHTTP(rec, r)
	return rec
}

func TestAnmeldungEinrichten1072_SeiteOhneSitzung(t *testing.T) {
	h := einrichtenMitMethoden(t, "")
	rec := hole1072(h, "/anmeldung")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Mit Google anmelden") || !strings.Contains(body, "Mit Heim anmelden") {
		t.Fatalf("/anmeldung ohne Sitzung: %d %s", rec.Code, body)
	}
	// Die App (418 im Test) wird ohne Sitzung nicht ausgeliefert.
	if rec := hole1072(h, "/board"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/anmeldung" {
		t.Errorf("Seitenaufruf ohne Sitzung: %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := hole1072(h, "/anmeldung/start"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/anmeldung" {
		t.Errorf("Start ohne Kürzel: %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := hole1072(h, "/anmeldung/abgemeldet"); !strings.Contains(rec.Body.String(), `href="/anmeldung?konto=1"`) {
		t.Errorf("Abgemeldet: Link auf /anmeldung?konto=1 fehlt: %s", rec.Body.String())
	}
}

func TestAnmeldungEinrichten1072_FilterWirktAufTabelle(t *testing.T) {
	h := einrichtenMitMethoden(t, "google")
	// Ein Anbieter übrig: /anmeldung leitet direkt.
	if rec := hole1072(h, "/anmeldung"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/anmeldung/start?anbieter=google" {
		t.Errorf("/anmeldung mit einem Anbieter: %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	// Der ausgeschlossene Anbieter ist ganz abgeschaltet: Start und Rückruf 404.
	if rec := hole1072(h, "/anmeldung/start?anbieter=authelia"); rec.Code != http.StatusNotFound {
		t.Errorf("Start des ausgeschlossenen Anbieters: %d", rec.Code)
	}
	if rec := hole1072(h, "/anmeldung/rueckruf/authelia"); rec.Code != http.StatusNotFound {
		t.Errorf("Rückruf des ausgeschlossenen Anbieters: %d", rec.Code)
	}
	// Der eingeschlossene ist verdrahtet (Rückruf ohne Cookie: 400).
	if rec := hole1072(h, "/anmeldung/rueckruf/google"); rec.Code != http.StatusBadRequest {
		t.Errorf("Rückruf google: %d", rec.Code)
	}
}
