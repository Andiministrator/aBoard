package storage

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// jsListe liest eine `export const NAME = … Object.freeze([...])`-Liste aus
// src/finding.js.
func jsListe(t *testing.T, quelle, name string) []string {
	t.Helper()
	re := regexp.MustCompile(`(?s)export const ` + name + ` = [^\[]*\[(.*?)\]`)
	m := re.FindStringSubmatch(quelle)
	if m == nil {
		t.Fatalf("%s in src/finding.js nicht gefunden", name)
	}
	var out []string
	for _, w := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, w[1])
	}
	return out
}

// Drift-Wächter (F-1572): Die Go-Listen müssen dem Wortschatz in
// src/finding.js entsprechen — Wert für Wert, in derselben Reihenfolge.
func TestFindingWortschatz_GleichJS(t *testing.T) {
	roh, err := os.ReadFile("../../src/finding.js")
	if err != nil {
		t.Fatalf("src/finding.js nicht lesbar: %v", err)
	}
	q := string(roh)
	for _, c := range []struct {
		name string
		go_  []string
	}{
		{"FINDING_TYPEN", findingTypen},
		{"FINDING_PRIORITAETEN", findingPrioritaeten},
		{"FINDING_STATUS", findingStatus},
		{"FINDING_FELDER", findingFelder},
	} {
		js := jsListe(t, q, c.name)
		if strings.Join(js, "|") != strings.Join(c.go_, "|") || len(js) == 0 {
			t.Errorf("%s driftet: JS %v, Go %v", c.name, js, c.go_)
		}
	}
}

func stand680(karten ...string) []byte {
	return []byte(`{"version":"2","board":{"id":"b","title":"t","sprints":[{"id":"s","cards":[` +
		strings.Join(karten, ",") + `]}]}}`)
}

func finding680(id, nr, typ, prio, status string) string {
	return `{"id":"` + id + `","kind":"finding","finding":{"nr":` + nr + `,"typ":"` + typ + `","prioritaet":"` + prio + `","status":"` + status + `"}}`
}

func TestFindingVerstoesse(t *testing.T) {
	gut := stand680(finding680("a", "1", "K", "P2", "offen"), finding680("b", "2", "S", "P0", "teilweise behoben"), `{"id":"x","title":"normal"}`)
	if v := FindingVerstoesse(gut); len(v) != 0 {
		t.Fatalf("gültiger Stand: %v", v)
	}
	for name, c := range map[string]struct {
		stand []byte
		teil  string
	}{
		"doppelte nr":        {stand680(finding680("a", "3", "K", "P2", "offen"), finding680("b", "3", "K", "P2", "offen")), "used on more than one card"},
		"Typ":                {stand680(finding680("a", "1", "X", "P2", "offen")), "finding.typ"},
		"Prioritaet":         {stand680(finding680("a", "1", "K", "P9", "offen")), "finding.prioritaet"},
		"Status mit Zusatz":  {stand680(finding680("a", "1", "K", "P2", "eingeplant (Sprint 3)")), "finding.status"},
		"ohne finding":       {stand680(`{"id":"a","kind":"finding"}`), "without a finding object"},
		"fremder Schluessel": {stand680(`{"id":"a","kind":"finding","finding":{"nr":1,"typ":"K","prioritaet":"P2","status":"offen","prio":"P1"}}`), "finding.prio is not a known field"},
		"nr fehlt":           {stand680(`{"id":"a","kind":"finding","finding":{"typ":"K","prioritaet":"P2","status":"offen"}}`), "finding.nr is missing"},
	} {
		v := FindingVerstoesse(c.stand)
		if len(v) == 0 || !strings.Contains(strings.Join(v, ";"), c.teil) {
			t.Errorf("%s: %v, erwartet %q", name, v, c.teil)
		}
	}
	if v := FindingVerstoesse([]byte("kein json")); v != nil {
		t.Errorf("unlesbar: %v", v)
	}
}

func TestNeueFindingVerstoesse_AlteBleibenDurch(t *testing.T) {
	alt := stand680(finding680("a", "3", "K", "P2", "offen"), finding680("b", "3", "K", "P2", "offen"))
	// Derselbe Verstoß wie vorher: kein neuer.
	if v := NeueFindingVerstoesse(alt, alt); len(v) != 0 {
		t.Fatalf("alter Verstoß gilt als neu: %v", v)
	}
	// Ein zusätzlicher Verstoß: wird gemeldet.
	neu := stand680(finding680("a", "3", "K", "P2", "offen"), finding680("b", "3", "K", "P2", "offen"), finding680("c", "4", "Q", "P2", "offen"))
	if v := NeueFindingVerstoesse(alt, neu); len(v) != 1 || !strings.Contains(v[0], "card c") {
		t.Fatalf("neuer Verstoß: %v", v)
	}
	// Neues Board (kein alter Stand): alles zählt.
	if v := NeueFindingVerstoesse(nil, alt); len(v) != 1 {
		t.Fatalf("ohne alten Stand: %v", v)
	}
}

// KA-3: Der Vergleich läuft je Karten-id, nicht über Meldungstexte.
func TestNeueFindingVerstoesse_Umgehungen(t *testing.T) {
	// (a) neue Karte mit einer Nummer, die im Altstand schon doppelt ist
	alt := stand680(finding680("x1", "7", "K", "P3", "offen"), finding680("x2", "7", "K", "P3", "offen"))
	neu := stand680(finding680("x1", "7", "K", "P3", "offen"), finding680("x2", "7", "K", "P3", "offen"), finding680("x3", "7", "K", "P3", "offen"))
	if v := NeueFindingVerstoesse(alt, neu); len(v) == 0 {
		t.Fatal("(a) neue Karte mit doppelter Nummer nicht gemeldet")
	}
	// (b) Nummernwechsel einer bestehenden Karte
	alt = stand680(finding680("y1", "3", "K", "P3", "offen"))
	if v := NeueFindingVerstoesse(alt, stand680(finding680("y1", "99", "K", "P3", "offen"))); len(v) == 0 {
		t.Fatal("(b) Nummernwechsel nicht gemeldet")
	}
	// (c) Umwidmung: Finding-Objekt ohne kind
	if v := NeueFindingVerstoesse(alt, stand680(`{"id":"y1","finding":{"nr":3,"typ":"K","prioritaet":"P3","status":"offen"}}`)); len(v) == 0 {
		t.Fatal("(c) Umwidmung nicht gemeldet")
	}
	// (c2) normale Karte wird zum Finding
	if v := NeueFindingVerstoesse(stand680(`{"id":"z"}`), stand680(finding680("z", "1", "K", "P3", "offen"))); len(v) == 0 {
		t.Fatal("(c2) Umwidmung zum Finding nicht gemeldet")
	}
	// Zwei ungültige Werte mit gleichem 40-Zeichen-Anfang gelten nicht als alt.
	lang := strings.Repeat("x", 45)
	a := stand680(finding680("w", "1", lang+"A", "P3", "offen"))
	n := stand680(finding680("w", "1", lang+"B", "P3", "offen"))
	if v := NeueFindingVerstoesse(a, n); len(v) == 0 {
		t.Fatal("geänderter ungültiger Wert gilt als alt")
	}
	// Unveränderte alte Verstöße und eine unbeteiligte Änderung bleiben durch.
	if v := NeueFindingVerstoesse(alt, alt); len(v) != 0 {
		t.Fatalf("unverändert: %v", v)
	}
	// (d) Verify VO-2: neue normale Karte mit Finding-Objekt — trifft nur die
	// Regel „finding ohne kind“, nicht die Umwidmung (die Karte fehlt im Altstand).
	if v := NeueFindingVerstoesse(stand680(`{"id":"q0"}`),
		stand680(`{"id":"q0"}`, `{"id":"q1","finding":{"nr":5,"typ":"K","prioritaet":"P3","status":"offen"}}`)); len(v) == 0 {
		t.Fatal("(d) neue Karte mit finding-Objekt ohne kind nicht gemeldet")
	}
}

// Verify VO-1: Eine Altlast-Doppelnummer sperrt die Bearbeitung der beteiligten
// Karten nicht. Der Fold lässt eine Statusänderung über Ops durch; der PUT darf
// nicht strenger sein. Eine NEU vergebene Doppelnummer bleibt ein Verstoß.
func TestNeueFindingVerstoesse_AltlastDoppelBleibtBearbeitbar(t *testing.T) {
	alt := stand680(finding680("d1", "3", "K", "P3", "offen"), finding680("d2", "3", "K", "P3", "offen"))
	neu := stand680(finding680("d1", "3", "K", "P3", "behoben"), finding680("d2", "3", "K", "P3", "offen"))
	if v := NeueFindingVerstoesse(alt, neu); len(v) != 0 {
		t.Fatalf("Statusänderung an Altlast-Doppel abgelehnt: %v", v)
	}
	// Nummernwechsel auf eine schon belegte Nummer bleibt gemeldet — auch als Doppelung.
	alt = stand680(finding680("e1", "3", "K", "P3", "offen"), finding680("e2", "4", "K", "P3", "offen"))
	neu = stand680(finding680("e1", "3", "K", "P3", "offen"), finding680("e2", "3", "K", "P3", "offen"))
	v := NeueFindingVerstoesse(alt, neu)
	if !slices.ContainsFunc(v, func(s string) bool { return strings.Contains(s, "more than one card") }) {
		t.Fatalf("neu entstandene Doppelung nicht gemeldet: %v", v)
	}
}
