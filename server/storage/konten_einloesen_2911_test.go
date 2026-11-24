package storage

import (
	"strings"
	"testing"
)

// F-2911 Teil 3 / F-2961: gemeldeteAdresse kürzt nur noch; kein Zusatz im Wert.
func TestGemeldeteAdresse_F2911(t *testing.T) {
	if g := gemeldeteAdresse(" a@example.org "); g != "a@example.org" {
		t.Errorf("Adresse: %q", g)
	}
	if g := gemeldeteAdresse("   "); g != "" {
		t.Errorf("leer: %q", g)
	}
	if g := gemeldeteAdresse(strings.Repeat("a", 500) + "@example.org"); len(g) > 320 || strings.Contains(g, "unbest") {
		t.Errorf("lang: %d", len(g))
	}
}

// F-2961: Das Flag gilt nur, wenn überhaupt eine Adresse gespeichert wird.
func TestUnbestaetigtFlag_F2961(t *testing.T) {
	if !unbestaetigtFlag(Einloesung{GemeldeteEmail: "a@example.org", GemeldeteEmailUnbestaetigt: true}) {
		t.Error("Adresse + unbestätigt: Flag erwartet")
	}
	if unbestaetigtFlag(Einloesung{GemeldeteEmail: "  ", GemeldeteEmailUnbestaetigt: true}) {
		t.Error("leere Adresse: kein Flag")
	}
	if unbestaetigtFlag(Einloesung{GemeldeteEmail: "a@example.org"}) {
		t.Error("bestätigt: kein Flag")
	}
}
