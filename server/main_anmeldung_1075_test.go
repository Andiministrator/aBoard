package main

// Karte #1075: Passwort und 2FA-Code über die echte Kette bis zur Sitzung.
// Testdaten nur example.org.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
)

func TestAnmeldung1075_PasswortUndCodeBisZurSitzung(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv(api.AuthModeEnvVar, "builtin")
	t.Setenv("ABOARD_ANMELDUNG", "")
	t.Setenv("ABOARD_OIDC_ISSUER", "https://127.0.0.1:1")
	h, konten := baueKette1074(t)

	box, err := secretcrypto.New(testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	e, err := api.NeueTOTPEinrichtung("dora@example.org")
	if err != nil {
		t.Fatal(err)
	}
	versiegelt, err := box.Seal(e.Geheimnis)
	if err != nil {
		t.Fatal(err)
	}
	if err := konten.TOTPEinrichten(context.Background(), "k-dora", versiegelt, e.CodeHashes, 3); err != nil {
		t.Fatal(err)
	}

	rec := postPasswort(h, "dora@example.org", "doras-langes-passwort")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `action="/anmeldung/code"`) {
		t.Fatalf("Passwort: %d %s", rec.Code, rec.Body.String())
	}
	var zwischen *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == api.ZweiterSchrittCookie {
			zwischen = c
		}
		if c.Name == api.SitzungsCookie {
			t.Fatal("Sitzung schon nach dem Passwort")
		}
	}
	if zwischen == nil {
		t.Fatal("kein Zwischen-Cookie")
	}

	code := func(wert string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "https://board.example"+api.AnmeldungCodePfad, strings.NewReader(url.Values{"code": {wert}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(zwischen)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	// Ein Wiederherstellungscode führt bis zur Sitzung.
	rec = code(e.Codes[0])
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("Code: %d %s", rec.Code, rec.Body.String())
	}
	var sitzung *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == api.SitzungsCookie && c.Value != "" {
			sitzung = c
		}
	}
	if sitzung == nil {
		t.Fatal("keine Sitzung nach dem Code")
	}
	// Mit der Sitzung erreicht die Anfrage die App (418 hier).
	r := httptest.NewRequest(http.MethodGet, "https://board.example/boards", nil)
	r.AddCookie(sitzung)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("mit Sitzung: %d", rec.Code)
	}
	// Das Zwischen-Cookie gilt nicht noch einmal.
	if rec := code(e.Codes[1]); rec.Code != http.StatusUnauthorized {
		t.Fatalf("Zwischen-Cookie zweimal: %d", rec.Code)
	}
}
