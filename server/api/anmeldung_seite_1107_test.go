package api

// Karte #1107: gemeinsamer Seitenrahmen der Anmeldeseiten — Logo, Karte,
// hell/dunkel, Anbieter als Knöpfe, Trenner „oder“ nur zwischen Formular und
// Anbieter-Knöpfen. Nichts wird nachgeladen (CSP default-src 'none').

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func rahmen(a seitenAnsicht) string {
	rec := httptest.NewRecorder()
	anmeldeSeiteAnsicht(rec, http.StatusOK, a)
	return rec.Body.String()
}

func TestAnmeldeSeite1107_Rahmen(t *testing.T) {
	body := rahmen(seitenAnsicht{Lang: "de", Titel: "Anmelden", Text: "Wähle", Formular: "<form></form>", Oder: "oder",
		Links: []seitenLink{{Text: "Mit Google anmelden", Ziel: "/anmeldung/start?anbieter=google"}}})
	for _, want := range []string{
		"<main>", `<div class="marke">` + anmeldeLogo + "aBoard</div>", "<h1>Anmelden</h1>",
		"@media (prefers-color-scheme:dark)", `<div class="oder">oder</div>`,
		`<a class="knopf" href="/anmeldung/start?anbieter=google">Mit Google anmelden</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("fehlt %q", want)
		}
	}
	// Trenner steht zwischen Formular und Knopf.
	if !(strings.Index(body, "</form>") < strings.Index(body, `class="oder"`) && strings.Index(body, `class="oder"`) < strings.Index(body, `class="knopf"`)) {
		t.Error("Reihenfolge Formular → oder → Knopf stimmt nicht")
	}
	// Nichts Nachgeladenes, kein Skript, keine style-Attribute (CSP).
	for _, verboten := range []string{"<script", "<link", "<img", "url(", "@import", "http://", "https://", ` style="`} {
		if strings.Contains(body, verboten) {
			t.Errorf("Seite enthält %q", verboten)
		}
	}
}

func TestAnmeldeSeite1107_TrennerNurMitBeidem(t *testing.T) {
	ohneFormular := rahmen(seitenAnsicht{Lang: "de", Titel: "Abgemeldet", Text: "x", Oder: "oder",
		Links: []seitenLink{{Text: "Neu anmelden", Ziel: AnmeldungSeitePfad}}})
	ohneLinks := rahmen(seitenAnsicht{Lang: "de", Titel: "Anmelden", Text: "x", Oder: "oder", Formular: "<form></form>"})
	ohneText := rahmen(seitenAnsicht{Lang: "de", Titel: "Fehler", Text: "x", Formular: "<form></form>",
		Links: []seitenLink{{Text: "Neu anmelden", Ziel: AnmeldungSeitePfad}}})
	for name, b := range map[string]string{"ohne Formular": ohneFormular, "ohne Links": ohneLinks, "ohne Oder-Text": ohneText} {
		if strings.Contains(b, `class="oder"`) {
			t.Errorf("%s: Trenner, obwohl nicht beides da ist", name)
		}
	}
}
