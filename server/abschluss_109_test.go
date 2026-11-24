package main

// Kritiker Sprint-Abschluss 109 (F-2878): Bei eingeschalteter Anmeldung
// wurden unbekannte ABOARD_AUTH_*- und ABOARD_INVITE_*-Namen still
// übergangen; ein Tippfehler in ABOARD_AUTH_METHODS schaltete die Auswahl ab
// (alle eingerichteten Anbieter wurden angeboten). Gegenprobe des Kritikers,
// übernommen aus tmp/sprint109/abschluss-k/.

import (
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

func TestAbschluss109_TippfehlerAuthPraefix(t *testing.T) {
	// ABOARD_INVITE_METHODS stand hier bis #1077 mit drin (der Wert hielt den
	// Start an, solange es keine Einladung gab); seither ist er eine echte
	// Auswahl, geprüft in TestEinladungsWege1077_Startpruefung.
	for _, v := range []string{"ABOARD_AUTH_METHOD", "ABOARD_AUTH_METHODES", "ABOARD_INVITE_METHOD"} {
		t.Run(v, func(t *testing.T) {
			setzeOIDCUmgebung(t)
			t.Setenv(api.AuthModeEnvVar, "builtin")
			t.Setenv(v, "standard")
			err := checkAnmeldungKonfig()
			if err == nil {
				t.Fatalf("%s=standard bei eingeschalteter Anmeldung: kein Startabbruch, Variable still übergangen", v)
			}
			if !strings.Contains(err.Error(), v) {
				t.Errorf("Meldung nennt %s nicht: %v", v, err)
			}
		})
	}
}

// Gegenstück: die bekannten Schalter mit gültigen Werten halten den Start
// nicht an (sonst wäre die Prüfung oben zu grob).
func TestAbschluss109_BekannteSchalterStarten(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv(api.AuthModeEnvVar, "builtin")
	t.Setenv(api.AuthMethodsEnv, "standard")
	if err := checkAnmeldungKonfig(); err != nil {
		t.Fatalf("gültige Schalter: unerwarteter Startabbruch: %v", err)
	}
}

// F-2881: Die Meldungen zu ABOARD_PUBLIC_BASE_URL nennen beide Schalternamen.
func TestAbschluss109_BasisMeldungNenntBeideSchalter(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv(api.AuthModeEnvVar, "builtin")
	for _, basis := range []string{"http://pm.example.org", "https://pm.example.org/unter"} {
		err := pruefeAnmeldungBasis(basis)
		if err == nil {
			t.Fatalf("%s: kein Fehler", basis)
		}
		if !strings.Contains(err.Error(), api.AuthModeEnvVar) {
			t.Errorf("%s: Meldung nennt %s nicht: %v", basis, api.AuthModeEnvVar, err)
		}
	}
}

// Sitzungs-Kritik 03.11. (F-2887): Die Meldung für einen unbekannten Namen
// nennt alle drei bekannten Schalter, auch ABOARD_INVITE_METHODS.
func TestAbschluss109_MeldungNenntAlleBekanntenSchalter(t *testing.T) {
	setzeOIDCUmgebung(t)
	t.Setenv(api.AuthModeEnvVar, "builtin")
	t.Setenv("ABOARD_INVITE_METHOD", "standard")
	err := checkAnmeldungKonfig()
	if err == nil {
		t.Fatal("ABOARD_INVITE_METHOD: kein Startabbruch")
	}
	_, rest, ok := strings.Cut(err.Error(), "ABOARD_INVITE_METHOD ist unbekannt — Tippfehler? (bekannt: ")
	bekannt, _, _ := strings.Cut(rest, ")")
	if !ok {
		t.Fatalf("Meldung zum unbekannten Namen fehlt: %v", err)
	}
	for _, name := range []string{api.AuthModeEnvVar, api.AuthMethodsEnv, api.InviteMethodsEnv} {
		if !strings.Contains(bekannt, name) {
			t.Errorf("„bekannt: %s“ nennt %s nicht", bekannt, name)
		}
	}
}
