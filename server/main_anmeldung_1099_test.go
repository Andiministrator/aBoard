package main

// Karte #1099 (ADR-0149 Nachtrag 2026-11-04): Schalter
// ABOARD_ACCOUNT_SELF_PASSWORD — Startprüfungen und Verdrahtung über die echte
// Kette. Testdaten nur example.org/example.com.

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

func TestSelbstPasswort1099_Startpruefung(t *testing.T) {
	for _, f := range []struct {
		name, modus, wert, methoden string
		ok                          bool
	}{
		{"aus, Anmeldung aus", "", "", "", true},
		{"0, Anmeldung aus", "", "0", "", true},
		{"an, Anmeldung aus", "", "1", "", false},
		{"Tippfehler, Anmeldung aus", "", "aus", "", false},
		{"an, alle Wege", "builtin", "true", "", true},
		{"an, password angeboten", "builtin", "on", "password,standard", true},
		{"an, ohne password", "builtin", "1", "standard", false},
		{"aus, ohne password", "builtin", "off", "standard", true},
		{"Tippfehler", "builtin", "nein", "", false},
		{"Tippfehler flase", "builtin", "flase", "", false},
	} {
		t.Run(f.name, func(t *testing.T) {
			setzeOIDCUmgebung(t)
			t.Setenv(api.AnmeldungEnvVar, "")
			if f.modus == "" {
				for _, v := range []string{"ABOARD_OIDC_ISSUER", "ABOARD_OIDC_CLIENT_ID", "ABOARD_OIDC_CLIENT_SECRET", "ABOARD_OIDC_ZUGELASSEN"} {
					t.Setenv(v, "")
				}
			}
			t.Setenv(api.AuthModeEnvVar, f.modus)
			t.Setenv(api.AuthMethodsEnv, f.methoden)
			t.Setenv(api.KontoSelbstPasswortEnv, f.wert)
			err := checkAnmeldungKonfig()
			if (err == nil) != f.ok {
				t.Fatalf("%v, erwartet ok=%v", err, f.ok)
			}
			if err != nil && !strings.Contains(err.Error(), api.KontoSelbstPasswortEnv) {
				t.Errorf("Meldung nennt %s nicht: %v", api.KontoSelbstPasswortEnv, err)
			}
		})
	}
}

// Über die echte Kette: Schalter aus → 404, an → der Endpunkt antwortet (ohne
// Nachweis bzw. ohne Person 409, nie 404).
func TestSelbstPasswort1099_Kette(t *testing.T) {
	for _, f := range []struct {
		wert   string
		status int
	}{{"", http.StatusNotFound}, {"1", http.StatusConflict}} {
		t.Run("wert="+f.wert, func(t *testing.T) {
			setzeOIDCUmgebung(t)
			t.Setenv(api.AnmeldungEnvVar, "")
			t.Setenv(api.AuthModeEnvVar, "builtin")
			t.Setenv("ABOARD_OIDC_ISSUER", "https://127.0.0.1:1")
			t.Setenv(api.KontoSelbstPasswortEnv, f.wert)
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
			id, _, err := storage.NewSitzungRepo(db).Anlegen(ctx, "anna@example.com", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"/passwort/anlegen/start", "/passwort/anlegen/abschluss"} {
				r := httptest.NewRequest(http.MethodPost, "https://board.example"+api.KontoPfad+p, strings.NewReader(`{}`))
				r.Header.Set("Content-Type", "application/json")
				r.AddCookie(&http.Cookie{Name: api.SitzungsCookie, Value: id})
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				if rec.Code != f.status {
					t.Errorf("%s: %d %s, erwartet %d", p, rec.Code, rec.Body.String(), f.status)
				}
			}
		})
	}
}
