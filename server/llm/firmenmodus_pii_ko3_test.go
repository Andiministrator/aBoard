package llm

// Kritiker Sprint 82, Runde 2, KO-3: ABOARD_LLM_PII=aus wirkt auch ohne
// Firmenmodus (Entscheidung: der ausdrücklich gesetzte Betreiberwert gilt).
// Damit das nicht still geschieht, liefert PIIOhneFirmenmodusWarnung einen
// Warnsatz für das Start-Log — nur bei „aus“ ohne Firmenmodus.

import (
	"strings"
	"testing"
)

func TestPIIOhneFirmenmodusWarnungKO3(t *testing.T) {
	for _, fall := range []struct {
		v           PIIVorgabe
		firmenmodus bool
		warnen      bool
	}{
		{PIIAus, false, true},
		{PIIAus, true, false},
		{PIIPflicht, false, false},
		{PIIPflicht, true, false},
		{PIINutzer, false, false},
		{"", false, false},
	} {
		w := PIIOhneFirmenmodusWarnung(fall.v, fall.firmenmodus)
		if fall.warnen {
			for _, muss := range []string{PIIEnvVar + "=aus", "ohne Firmenmodus", "ungefiltert", "nicht einschalten"} {
				if !strings.Contains(w, muss) {
					t.Errorf("(%q, firmenmodus=%v): Warnung %q enthält %q nicht", fall.v, fall.firmenmodus, w, muss)
				}
			}
		} else if w != "" {
			t.Errorf("(%q, firmenmodus=%v): Warnung %q, erwartet keine", fall.v, fall.firmenmodus, w)
		}
	}
	// Die Startfunktion liest den Wert wirklich so: „aus“ ohne Firmenmodus bleibt „aus“.
	v, err := ParsePIIVorgabe("aus", false, "")
	if err != nil || v != PIIAus {
		t.Fatalf("ParsePIIVorgabe(aus, ohne Firmenmodus) = %q, %v", v, err)
	}
	if PIIOhneFirmenmodusWarnung(v, false) == "" {
		t.Error("geparstes „aus“ ohne Firmenmodus ergibt keine Warnung")
	}
}
