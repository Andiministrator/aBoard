package schleuse

// VR2-1 (Verify Sprint-82-Abschluss Runde 2): Namen der Liste werden wie der
// Text bereinigt. Sonst verlangte die Suche Zeichen, die Normalisiere im Text
// schon entfernt hat, und der Namensteil ginge im Klartext hinaus.

import (
	"strings"
	"testing"
)

func TestNamensliste_NameMitUnsichtbaremZeichenWirdGefunden(t *testing.T) {
	faelle := []struct{ text, name, rest string }{
		{"Termin mit 葛\U000E0100城 Taro morgen", "葛\U000E0100城 Taro", "城"},
		{"Bitte Anna️ Müller fragen", "Anna️ Müller", "Anna"},
		{"Bitte Zoë Klein fragen", "Zoë Klein", "Klein"}, // NFD-Name gegen NFC-Text
	}
	for _, f := range faelle {
		out, _ := nacharbeitLauf(t, f.text, f.name)
		if strings.Contains(out, f.rest) || !strings.Contains(out, "[PERSON_") {
			t.Errorf("Name %q nicht ersetzt: %q", f.name, out)
		}
	}
}
