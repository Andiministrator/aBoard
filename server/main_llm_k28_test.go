package main

// main_llm_k28_test.go — Kritiker K2-8 (Sprint 82): Im Firmenmodus warnt der
// Start bei http:// zu einem fremden Rechner ausdrücklich vor dem
// SERVERSCHLÜSSEL im Klartext, nicht vor „den Schlüsseln der Nutzer“.

import (
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/ausgang"
)

func TestOpenAIBasisWarnungen_K28_FirmenmodusKlartext(t *testing.T) {
	an := ausgang.Neu(false)
	w := strings.Join(openAIBasisWarnungen("http://llm.example:8000/v1", []string{"m"}, an, true), "\n")
	for _, teil := range []string{"Firmenmodus", "SERVERSCHLÜSSEL", "Klartext", "ABOARD_LLM_SERVERSCHLUESSEL_FILE"} {
		if !strings.Contains(w, teil) {
			t.Errorf("Firmenmodus-Warnung ohne %q: %s", teil, w)
		}
	}
	if strings.Contains(w, "Schlüssel der Nutzer") {
		t.Errorf("Firmenmodus warnt vor Nutzerschlüsseln, die es dort nicht gibt: %s", w)
	}
	// Ohne Firmenmodus: die bisherige Warnung über Nutzerschlüssel.
	w = strings.Join(openAIBasisWarnungen("http://llm.example:8000/v1", []string{"m"}, an, false), "\n")
	if !strings.Contains(w, "Schlüssel der Nutzer") || strings.Contains(w, "SERVERSCHLÜSSEL") {
		t.Errorf("ohne Firmenmodus: %s", w)
	}
	// Loopback und https: auch im Firmenmodus keine Klartext-Warnung.
	for _, basis := range []string{"http://127.0.0.1:8000/v1", "https://llm.example/v1"} {
		if w := openAIBasisWarnungen(basis, []string{"m"}, an, true); len(w) != 0 {
			t.Errorf("%s: unerwartete Warnung %v", basis, w)
		}
	}
}
