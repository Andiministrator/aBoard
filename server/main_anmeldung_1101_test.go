package main

// Karte #1101 (ADR-0149 Nachtrag 2026-11-09): Schalter
// ABOARD_OIDC_REAUTH_FALLBACK — Startprüfung (Kritik 10.11., F-B c).

import (
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

func TestReauthFallback1101_Startpruefung(t *testing.T) {
	for _, f := range []struct {
		name, modus, wert string
		ok                bool
		nennt             string
	}{
		{"aus, Anmeldung aus", "", "", true, ""},
		{"an, Anmeldung aus", "", "1", false, api.KontoReauthFallbackEnv},
		{"Tippfehler, Anmeldung aus", "", "aus", false, api.KontoReauthFallbackEnv},
		{"an, Anmeldung an", "builtin", "1", true, ""},
		{"aus, Anmeldung an", "builtin", "off", true, ""},
		{"Tippfehler, Anmeldung an", "builtin", "jein", false, api.KontoReauthFallbackEnv},
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
			t.Setenv(api.KontoReauthFallbackEnv, f.wert)
			err := checkAnmeldungKonfig()
			if (err == nil) != f.ok {
				t.Fatalf("%v, erwartet ok=%v", err, f.ok)
			}
			if err != nil && !strings.Contains(err.Error(), f.nennt) {
				t.Errorf("Meldung nennt %s nicht: %v", f.nennt, err)
			}
		})
	}
}
