package api_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

// F-2116 und F-2118 (Verify #799): Semikolon und reine Kombinationszeichen in
// Anbieter/Beleg werden abgelehnt; Komma und zusammengesetzte Akzente bleiben
// erlaubt. Die Escapes stehen als Go-Escape im Quelltext, nicht als rohes Zeichen.
func TestHerkunft799_SemikolonUndKombinationszeichen(t *testing.T) {
	e := newEnv608(t)
	abgelehnt := map[string][2]string{
		"semikolon anbieter":    {"A;beleg=vertrag.md", ""},
		"semikolon user":        {"x;user=u-admin", "B"},
		"semikolon beleg":       {"A", "a;b"},
		"kombinationszeichen":   {"\u0300", "B"},
		"kombinationszeichen 2": {"A", "\u0300\u0301"},
	}
	angenommen := map[string][2]string{
		"komma":         {"Beispiel-Anbieter, Geschäftstarif", "vertraege/vertrag.md"},
		"akzent zusgs.": {"Cafe\u0301", "B"},
		"umlaut":        {"Müller & Söhne", "Ü"},
	}
	for _, seite := range []string{api.BoardpaarSeiteAussen, api.BoardpaarSeiteInnen} {
		setzeSeite799(t, e, seite)
		n := 0
		for fall, p := range abgelehnt {
			// Leerer Beleg ist innen erlaubt und gehört zum Semikolon-Fall; außen
			// lehnt ihn schon die Pflicht ab - 400 in beiden Fällen.
			n++
			s, b := e.anfrage(t, e.mensch, "POST", "/admin/agent-tokens", "admin@firma.de", koerper799(fmt.Sprintf("a%s%d", seite, n), p[0], p[1]))
			if s != http.StatusBadRequest {
				t.Errorf("%s/%s: %d %s (400 erwartet)", seite, fall, s, b)
			}
		}
		for fall, p := range angenommen {
			n++
			s, b := e.anfrage(t, e.mensch, "POST", "/admin/agent-tokens", "admin@firma.de", koerper799(fmt.Sprintf("g%s%d", seite, n), p[0], p[1]))
			if s != http.StatusCreated {
				t.Errorf("%s/%s: %d %s (201 erwartet)", seite, fall, s, b)
			}
		}
	}
}
