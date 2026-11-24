package schleuse

// reident.go — Re-Identifizierung der Antwort eines externen Modells
// (ADR-0134 §3.6; Karte #686).
//
// Die Antwort kommt pseudonymisiert zurück. Auf dem Server werden die
// Platzhalter, die die Schleuse vergeben hat, durch ihre Originale ersetzt —
// KANONISCH, nicht wörtlich (Nachtrag KS-8): Je Platzhalter hält die
// Zuordnung EIN Original, den Namen in Board-Schreibweise. Das Ergebnis ist
// deshalb nicht zwingend der Ausgangstext, sondern dieselben Personen und
// Dinge in Board-Schreibweise.
//
// Was das Modell verändert hat, bleibt stehen und wird gemeldet:
//   - ein Platzhalter in der richtigen Form, den die Zuordnung nicht kennt
//     („[PERSON_9]“, erfunden oder abgeschrieben),
//   - eine abgewandelte Form („[Person_1]“, „[PERSON 1]“, „(PERSON_1)“,
//     „PERSON_1“ ohne Klammern).
//
// Ersetzt wird in EINEM Durchlauf über den Antworttext: Ein Original, das
// selbst wie ein Platzhalter aussieht, wird nie ein zweites Mal ersetzt.
//
// Nicht zurückgeführt wird ein Platzhalter in einem Link- oder Bildziel
// (Kritiker KS-3, Sprint-82-Abschluss Runde 2): „[x](https://fremd/?d=
// [PERSON_1])“ trüge den Klarnamen sonst mit einem Klick (oder beim Laden
// eines Bildes) zu einem fremden Host. Er bleibt stehen und wird mit „🔗 “
// davor gemeldet — sprachneutral wie die übrigen Meldungen.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// arten: alle Arten, die die Schleuse vergibt — für die Suche nach
// abgewandelten Platzhaltern.
var arten = []string{ArtPerson, ArtFirma, ArtBoard, ArtEmail, ArtTelefon, ArtIBAN, ArtURL, ArtOrt, ArtKennung, ArtSonstiges}

// abgewandelt findet Formen, die ein Modell aus einem Platzhalter macht:
// beliebige Klammern oder Schreibweise mit Trenner, oder ohne Klammern mit
// Unterstrich. „Person 1“ ohne Klammern zählt nicht — das ist zu oft
// gewöhnlicher Text.
//
// Kritiker KS3-8: auch mit mehreren Trennern („[PERSON__1]“), Zusatz hinter
// der Zahl („[PERSON_1a]“), Ziffern anderer Schriften („[PERSON_１]“) und
// Klammern voller Breite („［PERSON_1］“).
//
// Abwägung (Verify VS3-4, Sitzungs-Kritiker SK-1): Ein verpasster Hinweis
// lässt einen Namen unaufgelöst und unbemerkt im Kommentar stehen; ein
// Fehlalarm ist nur ein überflüssiger Hinweis. Die Schutzrichtung entscheidet
// (Fehlerklasse 127). Gemeldet werden deshalb in jeder Schreibweise der Art:
//   - in Klammern mit oder ohne Trenner und ein- bis dreistelliger Zahl
//     („[Person 1]“, „(Person-1)“, „<Email 3>“, „[PERSON_1a]“),
//   - ohne Klammern mit Unterstrich oder Bindestrich („Person_1“, „PERSON-1“).
//
// Ausgenommen sind nur Zahlen ab vier Stellen — Jahreszahlen („(Firma 2024)“)
// —, und ohne Klammern und Trenner („Person 1“) gilt es als gewöhnlicher Text.
var abgewandelt = func() *regexp.Regexp {
	a := strings.Join(arten, "|")
	auf, zu := `[\[\(\{<«［（]\s*`, `\s*[\]\)\}>»］）]`
	zahl := `\p{Nd}{1,3}(?:[\p{L}_][\p{L}\p{Nd}_]*)?`
	return regexp.MustCompile(`(?i)` + auf + `(?:` + a + `)\s*[_\- ]*\s*` + zahl + zu +
		`|(?i)\b(?:` + a + `)[_\-]+\p{Nd}{1,3}\b`)
}()

// urlKontextMuster findet die Stellen, an denen ein Text zur Adresse wird:
//   - das Ziel eines Markdown-Links oder -Bildes „](…)“ ab der öffnenden
//     Klammer bis zur schließenden oder zum Zeilenende — so liest es
//     src/markdown.js (INLINE_RE). Die eckige Klammer davor gehört nicht
//     dazu: „[PERSON_1](Vertrieb)“ ist ein Name mit Zusatz, kein Linkziel,
//   - ein HTML-Attribut, das eine Adresse trägt (href, src, srcset, action,
//     formaction, poster, data, xlink:href), in Anführungszeichen oder ohne,
//   - eine Adresse mit Schema („https://“, „mailto:“, auch in „<…>“) oder
//     „www.“ bis zum nächsten Leerraum oder Begrenzer.
//
// Lieber zu weit: Ein Platzhalter, der fälschlich als Adresse gilt, bleibt
// sichtbar stehen und wird gemeldet; einer, der fälschlich zurückgeführt
// wird, verlässt das Board (Fehlerklasse 127).
//
// Referenz-Definitionen („[x]: ziel“) kennt src/markdown.js nicht; eine
// Adresse darin trifft die letzte Regel.
var (
	mdZielMuster     = regexp.MustCompile(`\](\([^)\n]*\)?)`)
	urlKontextMuster = regexp.MustCompile(`(?i)\b(?:href|src|srcset|action|formaction|poster|data|xlink:href)\s*=\s*(?:"[^"]*"?|'[^']*'?|[^\s>]*)` +
		`|(?i)(?:\b[a-z][a-z0-9+.\-]*://|\bmailto:|\bwww\.)[^\s<>"'` + "`" + `]*`)
)

// urlBereiche liefert die Byte-Bereiche der Adress-Kontexte in text.
func urlBereiche(text string) [][]int {
	var out [][]int
	for _, m := range mdZielMuster.FindAllStringSubmatchIndex(text, -1) {
		out = append(out, m[2:4])
	}
	return append(out, urlKontextMuster.FindAllStringIndex(text, -1)...)
}

// inBereich: Überschneidet [a,e) einen der Bereiche?
func inBereich(bereiche [][]int, a, e int) bool {
	for _, b := range bereiche {
		if a < b[1] && e > b[0] {
			return true
		}
	}
	return false
}

// ReIdentErgebnis ist die re-identifizierte Antwort samt Meldungen.
type ReIdentErgebnis struct {
	Text string
	// Ersetzt zählt die zurückgeführten Platzhalter (Vorkommen).
	Ersetzt int
	// ImLink zählt bekannte Platzhalter, die in einem Link- oder Bildziel
	// standen und deshalb NICHT zurückgeführt wurden (KS-3).
	ImLink int
	// Veraendert: unbekannte oder abgewandelte Platzhalter, sortiert und
	// ohne Doppel. Sie stehen unverändert im Text.
	Veraendert []string
}

// meldungHoechstLen begrenzt eine einzelne Meldung (Zeichen) und
// meldungenHoechst ihre Anzahl — die Liste geht an die Oberfläche.
const (
	meldungHoechstLen = 60
	meldungenHoechst  = 50
)

// ReIdentifiziere ersetzt die bekannten Platzhalter in antwort durch ihre
// Originale und meldet die übrigen.
func ReIdentifiziere(antwort string, zuordnung map[string]string) ReIdentErgebnis {
	var b strings.Builder
	b.Grow(len(antwort))
	gemeldet := map[string]bool{}
	melde := func(s string) {
		if utf8.RuneCountInString(s) > meldungHoechstLen {
			s = string([]rune(s)[:meldungHoechstLen]) + "…"
		}
		gemeldet[s] = true
	}
	erg := ReIdentErgebnis{}
	frei := func(teil string) {
		for _, m := range abgewandelt.FindAllString(teil, -1) {
			melde(m)
		}
		b.WriteString(teil)
	}
	pos := 0
	urls := urlBereiche(antwort)
	for _, t := range PlatzhalterMuster.FindAllStringIndex(antwort, -1) {
		frei(antwort[pos:t[0]])
		ph := antwort[t[0]:t[1]]
		if orig, ok := zuordnung[ph]; ok && orig != "" && inBereich(urls, t[0], t[1]) {
			melde("🔗 " + ph)
			b.WriteString(ph)
			erg.ImLink++
		} else if ok && orig != "" {
			b.WriteString(orig)
			erg.Ersetzt++
		} else {
			melde(ph)
			b.WriteString(ph)
		}
		pos = t[1]
	}
	frei(antwort[pos:])
	erg.Text = b.String()
	erg.Veraendert = make([]string, 0, len(gemeldet))
	for s := range gemeldet {
		erg.Veraendert = append(erg.Veraendert, s)
	}
	sort.Strings(erg.Veraendert)
	if n := len(erg.Veraendert); n > meldungenHoechst {
		// Nicht still kappen (KS3-8): die letzte Meldung sagt, wie viele fehlen.
		// Sprachneutral (Verify VS3-3): Die Liste geht an Oberflächen in jeder Sprache.
		erg.Veraendert = append(erg.Veraendert[:meldungenHoechst], fmt.Sprintf("… +%d", n-meldungenHoechst))
	}
	return erg
}
