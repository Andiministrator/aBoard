package api

// Kritiker Sprint 82, Runde 2, KO-1: Die Wiederholungsschutz-Tabelle der
// Werkzeuge ist für alle Personen gemeinsam. Ohne Kontingent je Person füllte
// EINE Person sie mit werkzeugIdemMax verschiedenen karte_anlegen-Aufrufen;
// danach erkannte der Server zehn Minuten lang bei niemandem eine Wiederholung
// ohne Idempotency-Key (Open WebUI schickt keinen). Vorlage: Messtest des
// Kritikers (tmp/sprint-82/abschluss-r2/k2/…/zz_k2_idem_internal_test.go).

import (
	"fmt"
	"net/http/httptest"
	"testing"
)

func idemKO1(t *testing.T, a *BoardsAPI, p *AgentPrincipal, titel string) wkIdemEintrag {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/werkzeuge/boards/b1/karten", nil)
	e, ok := a.werkzeugIdemBeginn(w, r, p, "b1", "karte_anlegen", titel, "", "")
	if !ok {
		t.Fatalf("werkzeugIdemBeginn(%q) abgelehnt: %d %s", titel, w.Code, w.Body.String())
	}
	return e
}

func TestWerkzeugIdemKO1_EinePersonFuelltDieTabelleNichtFuerAlle(t *testing.T) {
	a := &BoardsAPI{}
	angreifer := &AgentPrincipal{TokenID: "t1", Name: "open-webui", OnBehalfOf: "mallory@firma.de", Delegiert: true}
	opfer := &AgentPrincipal{TokenID: "t1", Name: "open-webui", OnBehalfOf: "anna@firma.de", Delegiert: true}

	// Ein Eintrag der Person anna, BEVOR die andere füllt: darf nicht verdrängt werden.
	vorher := idemKO1(t, a, opfer, "Frühe Karte")

	var letzte wkIdemEintrag
	for i := 0; i < werkzeugIdemMax; i++ {
		letzte = idemKO1(t, a, angreifer, fmt.Sprintf("titel-%d", i))
	}
	z := a.werkzeugIdem
	if n := z.jePerson["mallory@firma.de"]; n > werkzeugIdemJePersonMax {
		t.Errorf("Person mallory hält %d Einträge, Kontingent ist %d", n, werkzeugIdemJePersonMax)
	}
	if len(z.eintraege) >= werkzeugIdemMax {
		t.Errorf("Tabelle von einer Person gefüllt: %d Einträge (Max %d)", len(z.eintraege), werkzeugIdemMax)
	}
	// Zählung je Person stimmt mit der Tabelle überein.
	zaehlung := map[string]int{}
	for _, e := range z.eintraege {
		zaehlung[e.person]++
	}
	for person, n := range zaehlung {
		if z.jePerson[person] != n {
			t.Errorf("jePerson[%q] = %d, Tabelle zählt %d", person, z.jePerson[person], n)
		}
	}

	// Die früher angelegte Karte der Person anna wird weiter als Wiederholung erkannt.
	if nochmal := idemKO1(t, a, opfer, "Frühe Karte"); nochmal.clientID != vorher.clientID {
		t.Errorf("Wiederholung der frühen Karte von anna nicht erkannt (%s != %s) — von einer anderen Person verdrängt", nochmal.clientID, vorher.clientID)
	}
	// Eine neue Karte der Person anna und ihre Wiederholung.
	e1 := idemKO1(t, a, opfer, "Wichtige Karte")
	e2 := idemKO1(t, a, opfer, "Wichtige Karte")
	if e1.clientID != e2.clientID {
		t.Errorf("Wiederholung der Person anna nicht erkannt (clientID %s != %s) — Tabelle von einer anderen Person gefüllt", e1.clientID, e2.clientID)
	}
	// Verdrängt werden die ÄLTESTEN Einträge der füllenden Person, nicht der jüngste.
	if wieder := idemKO1(t, a, angreifer, fmt.Sprintf("titel-%d", werkzeugIdemMax-1)); wieder.clientID != letzte.clientID {
		t.Errorf("jüngster Aufruf der füllenden Person nicht mehr als Wiederholung erkannt")
	}
}
