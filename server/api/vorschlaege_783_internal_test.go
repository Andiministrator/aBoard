package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	"github.com/Andiministrator/aBoard/server/storage"
)

// TestKarteExakt783 (Verify V-3): karteExakt findet eine Karte nur, wenn die
// Werkzeug-Suche (Nummer vor id) genau sie liefert — nicht im Mail-Eingang,
// nicht bei doppelter Nummer, nicht bei einer id, die wie eine andere Nummer
// aussieht.
func TestKarteExakt783(t *testing.T) {
	wb := &wkBoard{Swimlanes: []wkSwimlane{
		{ID: "__backlog__", Kind: "backlog", Karten: []wkKarte{
			{ID: "k1", DisplayID: 1}, {ID: "5", DisplayID: 9}, {ID: "k5", DisplayID: 5},
			{ID: "d1", DisplayID: 7}, {ID: "d2", DisplayID: 7},
		}},
		{ID: "lane-service", Kind: "service", Karten: []wkKarte{{ID: "mail1", DisplayID: 20}}},
	}}
	for id, soll := range map[string]bool{"k1": true, "k5": true, "5": false, "d1": true, "mail1": false, "gibt-es-nicht": false} {
		if got := karteExakt(wb, id) != nil; got != soll {
			t.Errorf("karteExakt(%q) = %v, erwartet %v", id, got, soll)
		}
	}
	if k := karteExakt(wb, "k5"); k == nil || k.ID != "k5" {
		t.Errorf("k5: %+v", k)
	}
}

// TestPaarHinweisInternerAgent808 (F-2078, K-1/K-2): Der Hinweistext der Rolle
// intern nennt genau die Vorschlagsarten des Codes — jede Art mit ihrem Wort,
// keine fehlt — und führt Spalte und Priorität als sofort wirksam, nicht als
// Vorschlag. Kommt eine Vorschlagsart hinzu, wird dieser Test rot, bis der
// Text und die Wortliste nachgezogen sind.
func TestPaarHinweisInternerAgent808(t *testing.T) {
	wort := map[string]string{
		"karte.anlegen": "Anlegen", "karte.text": "Text", "karte.bearbeiter": "Bearbeiter",
		"kommentar": "Kommentar", "karte.loeschen": "Löschen", "karte.archivieren": "Archivieren",
	}
	if len(wort) != len(vorschlagFelder) {
		t.Fatalf("Wortliste (%d) und vorschlagFelder (%d) weichen ab", len(wort), len(vorschlagFelder))
	}
	for _, hinweis := range []string{paarHinweisInternerAgent, paarHinweisInternerAgentKopieAus, paarHinweisInternerAgentKopieAn} {
		pruefeHinweisInternerAgent808(t, hinweis, wort)
	}
}

func pruefeHinweisInternerAgent808(t *testing.T, hinweis string, wort map[string]string) {
	t.Helper()
	rest := hinweis
	if i := strings.Index(hinweis, "gehen nur:"); i >= 0 {
		rest = hinweis[i:]
	} else {
		t.Fatalf("Hinweis ohne „gehen nur:“: %q", hinweis)
	}
	for art, felder := range vorschlagFelder {
		w, ok := wort[art]
		if !ok {
			t.Errorf("Vorschlagsart %q fehlt in der Wortliste", art)
			continue
		}
		if !strings.Contains(rest, w) {
			t.Errorf("Vorschlagsart %q (%q) fehlt im Hinweis nach „gehen nur:“: %q", art, w, hinweis)
		}
		for _, f := range felder {
			if f == "prioritaet" || (f == "spalte" && art != "karte.anlegen") {
				t.Errorf("Art %q führt %q — sofort wirksames Feld wäre ein Vorschlag", art, f)
			}
		}
	}
	// W-2: Im Vorschlagsteil dürfen Spalte und Priorität nicht vorkommen.
	if strings.Contains(rest, "Spalte") || strings.Contains(rest, "Priorität") {
		t.Errorf("Spalte/Priorität im Vorschlagsteil: %q", rest)
	}
	// W-3: Der Text sagt, dass anderes nicht geht, und verspricht nicht „alles andere“.
	if !strings.Contains(hinweis, "anderes geht hier nicht") || strings.Contains(hinweis, "alles andere") {
		t.Errorf("Begrenzung fehlt oder zu weit: %q", hinweis)
	}
	vorher := hinweis[:strings.Index(hinweis, "gehen nur:")]
	if !strings.Contains(vorher, "Spalte und Priorität") || !strings.Contains(vorher, "sofort") {
		t.Errorf("Spalte und Priorität nicht als sofort wirksam genannt: %q", hinweis)
	}
	if strings.Contains(hinweis, "Neue Karten gehören nach innen") {
		t.Errorf("Text für Agenten außen im Hinweis: %q", hinweis)
	}
}

// K-4 (#808): 403-Meldung und Such-Hinweis je Rolle.
func TestTexteJeRolle808(t *testing.T) {
	intern := &AgentPrincipal{Name: "assistent", BoardID: "b1", Role: storage.AgentRolleIntern, tokenRolle: storage.AgentRolleIntern}
	aussen := &AgentPrincipal{Name: "p", BoardID: "b1", Role: storage.AgentRolleEditor, tokenRolle: storage.AgentRolleEditor}
	if m := verbotenMeldungFuer(intern); strings.Contains(m, "boards") || !strings.Contains(m, "/agent/intern/") {
		t.Errorf("verbotenMeldungFuer(intern): %q", m)
	}
	// Q-5: Der Text nennt den Weg, aber keine Zahl über die Zahl der Wege
	// (weder Ziffer noch Zahlwort): Sie veraltete mit dem nächsten Weg.
	m := verbotenMeldungFuer(intern)
	if strings.ContainsAny(m, "0123456789") {
		t.Errorf("verbotenMeldungFuer(intern) enthält eine Ziffer: %q", m)
	}
	for _, zahlwort := range []string{"ein Weg", "zwei", "drei", "vier", "fünf", "sechs", "sieben", "acht", "neun", "zehn"} {
		if strings.Contains(strings.ToLower(m), zahlwort) {
			t.Errorf("verbotenMeldungFuer(intern) enthält das Zahlwort %q: %q", zahlwort, m)
		}
	}
	if verbotenMeldungFuer(aussen) != werkzeugVerbotenMeldung || verbotenMeldungFuer(nil) != werkzeugVerbotenMeldung {
		t.Error("außen/ohne Principal: Standardtext erwartet")
	}
	if m := sucheEingrenzen(intern); strings.Contains(m, "boardId") {
		t.Errorf("sucheEingrenzen(intern): %q", m)
	}
	if !strings.Contains(sucheEingrenzen(aussen), "boardId") {
		t.Error("sucheEingrenzen(außen): boardId erwartet")
	}
}

// TestUnsichtbarAlleSelektoren783 (Sprint-91-Kritiker K-S1): Jeder Variation
// Selector und jeder Default-Ignorable-Codepunkt gilt als unsichtbar — nicht nur
// die Bereiche, an die man beim Schreiben gedacht hat. FE0E/FE0F sind nach einem
// Bildzeichen erlaubt und stehen deshalb hier ohne Bildzeichen davor.
func TestUnsichtbarAlleSelektoren783(t *testing.T) {
	n := 0
	for _, tab := range []*unicode.RangeTable{unicode.Variation_Selector, unicode.Other_Default_Ignorable_Code_Point} {
		for _, r16 := range tab.R16 {
			for r := rune(r16.Lo); r <= rune(r16.Hi); r += rune(r16.Stride) {
				n++
				if !hatUnsichtbareZeichen("ab"+string(r)+"c", true) {
					t.Errorf("U+%04X gilt nicht als unsichtbar", r)
				}
			}
		}
		for _, r32 := range tab.R32 {
			for r := rune(r32.Lo); r <= rune(r32.Hi); r += rune(r32.Stride) {
				n++
				if !hatUnsichtbareZeichen("ab"+string(r)+"c", true) {
					t.Errorf("U+%04X gilt nicht als unsichtbar", r)
				}
			}
		}
	}
	if n < 250 {
		t.Fatalf("nur %d Codepunkte geprüft — Tabellen leer?", n)
	}
}

// TestVorschlagFehlerText783 (Sprint-91-Kritiker K-S3): Nur der sichere Fall sagt
// „nicht ausgeführt“; ein unklarer Ausgang schickt die Person ins Board.
func TestVorschlagFehlerText783(t *testing.T) {
	if got := vorschlagFehlerText(true); got != "Der Vorschlag wurde nicht ausgeführt." {
		t.Errorf("freigebbar: %q", got)
	}
	if got := vorschlagFehlerText(false); strings.Contains(got, "nicht ausgeführt") || !strings.Contains(got, "offen") {
		t.Errorf("unklar: %q", got)
	}
}

// TestVorschlagMenschSperrtAgenten783 (Sprint-91-Kritiker K-S6): Die Agenten-Sperre
// in vorschlagMensch hält auch dann, wenn ein Agent mit Identität bis dorthin
// käme — heute fangen ihn agentDarf und die fehlende Identität vorher ab, diese
// Schicht war deshalb ohne Test.
func TestVorschlagMenschSperrtAgenten783(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/boards/b1/vorschlaege", nil)
	req.SetPathValue("id", "b1")
	ctx := context.WithValue(req.Context(), contextKeyAgent, &AgentPrincipal{Name: "assistent", BoardID: "b1"})
	ctx = context.WithValue(ctx, contextKeyIdentity, &ForwardAuthIdentity{Email: "anna@example.org"})
	rec := httptest.NewRecorder()
	board, email := (&BoardsAPI{}).vorschlagMensch(rec, req.WithContext(ctx))
	// Die Meldung trennt: ohne die Sperre endet der Weg an der fehlenden ACL, ebenfalls 403.
	if board != "" || email != "" || rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "forbidden for agent") {
		t.Fatalf("Agent mit Identität: board=%q email=%q status=%d %s", board, email, rec.Code, rec.Body.String())
	}
}

// TestVorschlagText_Inhaltszeichen2120 (F-2120): Ein Pflichtfeld nur aus
// Kombinationszeichen passiert unsichtbaresZeichen, braucht aber zusätzlich
// ein Zeichen aus L, N, P oder S. Die Zeichen entstehen per rune(), nicht
// als Escape im Quelltext.
func TestVorschlagText_Inhaltszeichen2120(t *testing.T) {
	akzent, ring := string(rune(0x300)), string(rune(0x20DD))
	build := func(s string) map[string]json.RawMessage {
		b, _ := json.Marshal(s)
		return map[string]json.RawMessage{"titel": b}
	}
	for name, s := range map[string]string{"U+0300": akzent, "U+20DD": ring, "beides": akzent + ring} {
		if _, grund := vorschlagText(build(s), "titel", 100, true, false); grund == "" {
			t.Errorf("%s: Pflichtfeld nur aus Kombinationszeichen angenommen", name)
		}
	}
	// Nicht-Pflichtfeld bleibt unberührt.
	if _, grund := vorschlagText(build(akzent), "titel", 100, false, false); grund != "" {
		t.Errorf("optionales Feld abgelehnt: %s", grund)
	}
	// e + U+0301 (zerlegtes é) bleibt zulässig.
	if w, grund := vorschlagText(build("Cafe"+string(rune(0x301))+" planen"), "titel", 100, true, false); grund != "" || w == "" {
		t.Errorf("normaler Titel mit Kombinationszeichen abgelehnt: %q", grund)
	}
}
