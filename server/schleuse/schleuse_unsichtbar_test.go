package schleuse

// schleuse_unsichtbar_test.go — Kritiker KS-1 (Sprint-82-Abschluss, Runde 2):
// Unsichtbare Zeichen außerhalb der Kategorie Cf überlebten Normalisiere.
// Damit ließ sich Inhalt unsichtbar durch die Durchsicht tragen, und ein
// Selektor mitten im Namen entging der festen Stufe.

import (
	"strings"
	"testing"
)

// Variation Selectors (auch die Emoji-Smuggling-Form U+E0100+Byte),
// mongolische Selektoren, CGJ und Hangul-Füller fallen weg; VS16 bleibt nur
// hinter einem Symbol oder in einer Tastenkappe, und nur einmal.
func TestNormalisiere_UnsichtbareSelektorenEntfallen(t *testing.T) {
	var geschmuggelt strings.Builder
	geschmuggelt.WriteString("Bitte prüfen.")
	for _, c := range []byte("Anna Müller") {
		geschmuggelt.WriteRune(rune(0xE0100 + int(c)))
	}
	faelle := []struct{ name, ein, aus string }{
		{"Emoji-Smuggling U+E01xx", geschmuggelt.String(), "Bitte prüfen."},
		{"VS1 im Wort", "An\uFE00na", "Anna"},
		{"VS16 hinter Buchstabe", "A\uFE0Fb", "Ab"},
		{"CGJ", "A\u034Fb", "Ab"},
		{"mongolische Selektoren", "A\u180B\u180C\u180D\u180Fb", "Ab"},
		{"Hangul-Füller", "A\u3164b\uFFA0c", "Abc"},
		{"VS16 hinter Symbol bleibt", "❤\uFE0F gut", "❤\uFE0F gut"},
		{"VS16 doppelt", "❤\uFE0F\uFE0F", "❤\uFE0F"},
		{"Tastenkappe bleibt", "1\uFE0F\u20E3", "1\uFE0F\u20E3"},
		{"VS16 hinter Ziffer ohne Kappe", "1\uFE0F2", "12"},
	}
	for _, f := range faelle {
		got, err := Normalisiere(f.ein)
		if err != nil {
			t.Fatal(err)
		}
		if got != f.aus {
			t.Errorf("%s: %q, erwartet %q", f.name, got, f.aus)
		}
		if nochmal, _ := Normalisiere(got); nochmal != got {
			t.Errorf("%s: nicht idempotent: %q", f.name, nochmal)
		}
	}
}

// U+200C/U+200D bleiben nur einzeln zwischen zwei sichtbaren Zeichen.
func TestNormalisiere_VerbinderNurEinzelnZwischenSichtbarem(t *testing.T) {
	faelle := []struct{ name, ein, aus string }{
		{"Emoji-Folge bleibt", "👨\u200D👩", "👨\u200D👩"},
		{"persisch bleibt", "می\u200Cخواهم", "می\u200Cخواهم"},
		{"Verbinder im Namen bleibt", "An\u200Dna", "An\u200Dna"},
		{"Folge", "a\u200D\u200C\u200Db", "ab"},
		{"Anfang", "\u200Dab", "ab"},
		{"Ende", "ab\u200C", "ab"},
		{"an Leerraum", "a \u200D b\u200C\nc", "a  b\nc"},
		{"Bitfolge zwischen Wörtern", "Hallo \u200C\u200D\u200C\u200D Welt", "Hallo  Welt"},
	}
	for _, f := range faelle {
		got, err := Normalisiere(f.ein)
		if err != nil {
			t.Fatal(err)
		}
		if got != f.aus {
			t.Errorf("%s: %q, erwartet %q", f.name, got, f.aus)
		}
		if nochmal, _ := Normalisiere(got); nochmal != got {
			t.Errorf("%s: nicht idempotent: %q", f.name, nochmal)
		}
	}
}

// Ein Selektor mitten im Namen entgeht der festen Stufe nicht mehr — weder
// einer, den Normalisiere entfernt, noch ein verbliebenes Kombinationszeichen
// (Kategorie Mn), das die Suche überspringt.
func TestFesteStufe_NameMitSelektorOderMn(t *testing.T) {
	for name, ein := range map[string]string{
		"VS1":            "Termin mit An\uFE00na Müller morgen",
		"VS17 (E0100)":   "Termin mit Ann\U000E0100a Müller morgen",
		"CGJ":            "Termin mit Anna Mü\u034Fller morgen",
		"Kombinationsz.": "Termin mit An\u0333na Müller morgen",
	} {
		text, _ := nacharbeitLauf(t, ein, "Anna Müller")
		if text != "Termin mit [PERSON_1] morgen" {
			t.Errorf("%s: %q", name, text)
		}
	}
}

// Die Modellstufe überspringt verbliebene Mn-Zeichen ebenso (toleranterAusdruck).
func TestToleranterAusdruck_UeberspringtMn(t *testing.T) {
	re := namensMuster("Anna", true)
	if !re.MatchString("An\u0333na") {
		t.Errorf("Kombinationszeichen im Namen nicht übersprungen: %s", re)
	}
}

// Normalisiere bleibt idempotent — der Hash-Vertrag hängt daran: Der
// gespeicherte Text ist bereits normalisiert, ein erneutes Speichern darf ihn
// nicht weiter verändern. Saat mit den Grenzfällen der zwei Durchgänge.
func FuzzNormalisiere_Idempotent(f *testing.F) {
	zwj, zwnj, vs16, kappe := string(rune(0x200D)), string(rune(0x200C)), string(rune(0xFE0F)), string(rune(0x20E3))
	for _, s := range []string{
		"a" + zwj + zwj + "b", "a" + zwj + " b", zwnj + "a", "❤" + vs16 + vs16 + zwj + "🔥",
		"1" + vs16 + kappe, "1" + string(rune(0xE0101)) + vs16 + kappe, "n" + string(rune(0x0303)) + zwj + "x",
		"a" + string(rune(0xFE00)) + zwj + "b", "\r\n" + zwj + "\r", "Mu" + string(rune(0x0308)) + zwj + "ller",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		einmal, err := Normalisiere(s)
		if err != nil {
			return
		}
		if zweimal, _ := Normalisiere(einmal); zweimal != einmal {
			t.Fatalf("nicht idempotent: %q → %q → %q", s, einmal, zweimal)
		}
	})
}
