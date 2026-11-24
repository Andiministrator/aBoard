package api_test

// Karte #602 (ADR-0130 §6/§8): gemeinsame Personenkennung E-Mail —
// board.awaiting nur für die eigene Person (F-1310) und die Auskunft
// GET /boards/{id}/ich, aus der das Werkzeug die Person liest.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (e *env608) tokenFuer(t *testing.T, name, person string) string {
	t.Helper()
	body := `{"name":"` + name + `","board":"b1","role":"editor"`
	if person != "" {
		body += `,"user":"` + person + `"`
	}
	s, b := e.anfrage(t, e.mensch, "POST", "/admin/agent-tokens", "admin@firma.de", body+`}`)
	if s != http.StatusCreated {
		t.Fatalf("Token %s anlegen: %d %s", name, s, b)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(b, &out)
	return out.Token
}

func awaitingOp(person string, frage bool) string {
	wert := `null`
	if frage {
		wert = `{"question":"A oder B?","since":"2026-09-30T10:00:00.000Z","askedBy":"Claude"}`
	}
	return `{"ops":[{"type":"board.awaiting","payload":{"person":"` + person + `","value":` + wert + `}}]}`
}

func TestAgent602_AwaitingNurEigenePerson(t *testing.T) {
	e := newEnv608(t)
	anna := e.tokenFuer(t, "annas-claude", "anna@firma.de")
	projekt := e.tokenFuer(t, "team-container", "")

	// Eigene Person — auch in anderer Schreibweise, wie im Browser verglichen.
	for _, person := range []string{"anna@firma.de", " Anna@Firma.DE "} {
		if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", anna, awaitingOp(person, true)); s != http.StatusOK {
			t.Fatalf("eigene Person %q: %d %s", person, s, b)
		}
	}
	// Der Schlüssel bleibt wie gesendet (Verify F-C) — ein vorhandener
	// gemischtschreibiger Eintrag ist mit genau diesem Schlüssel löschbar.
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", anna, awaitingOp(" Anna@Firma.DE ", false)); s != http.StatusOK {
		t.Fatalf("Löschen mit vorhandenem Schlüssel: %d %s", s, b)
	}
	if b1, _ := e.boards.Get(context.Background(), "b1"); strings.Contains(string(b1.StateJSON), "Anna@Firma.DE") {
		t.Fatalf("gemischtschreibiger Eintrag nicht gelöscht: %s", b1.StateJSON)
	}
	// Fremde Person: setzen und löschen → 400, nichts geschrieben.
	for _, frage := range []bool{true, false} {
		s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", anna, awaitingOp("admin@firma.de", frage))
		if s != http.StatusBadRequest || !strings.Contains(string(b), "own person") {
			t.Fatalf("fremde Person (frage=%v): %d %s", frage, s, b)
		}
	}
	// Anzeigename statt E-Mail zählt nicht als eigene Person.
	if s, _ := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", anna, awaitingOp("Anna", true)); s != http.StatusBadRequest {
		t.Fatalf("Anzeigename: %d", s)
	}
	// Projekt-Token ohne Person: gar nicht.
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", projekt, awaitingOp("anna@firma.de", true)); s != http.StatusBadRequest || !strings.Contains(string(b), "project tokens") {
		t.Fatalf("Projekt-Token: %d %s", s, b)
	}
	// Menschen sind nicht betroffen: der Admin setzt Annas Eintrag im Browser-Weg.
	if s, b := e.anfrage(t, e.mensch, "POST", "/boards/b1/ops", "admin@firma.de", awaitingOp("anna@firma.de", false)); s != http.StatusOK {
		t.Fatalf("Mensch: %d %s", s, b)
	}
}

// TestAgent645_AwaitingJeSitzung (F-1336, Karte #645): Zwei Sitzungen
// derselben Person setzen je einen Eintrag; Löschen mit Sitzung trifft nur
// die eigene. Die Personenprüfung (F-1310) gilt unverändert: eine Sitzung
// ändert nichts daran, wessen Eintrag es ist.
func TestAgent645_AwaitingJeSitzung(t *testing.T) {
	e := newEnv608(t)
	anna := e.tokenFuer(t, "annas-claude", "anna@firma.de")
	op := func(person, session string, frage bool) string {
		wert := `null`
		if frage {
			wert = `{"question":"Frage ` + session + `?","since":"2026-10-03T10:00:00.000Z","askedBy":"Claude"}`
		}
		return `{"ops":[{"type":"board.awaiting","payload":{"person":"` + person + `","session":"` + session + `","value":` + wert + `}}]}`
	}
	for _, sid := range []string{"sitzung-1", "sitzung-2"} {
		if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", anna, op("anna@firma.de", sid, true)); s != http.StatusOK {
			t.Fatalf("setzen %s: %d %s", sid, s, b)
		}
	}
	zustand := func() string {
		b1, _ := e.boards.Get(context.Background(), "b1")
		return string(b1.StateJSON)
	}
	if z := zustand(); !strings.Contains(z, "Frage sitzung-1?") || !strings.Contains(z, "Frage sitzung-2?") {
		t.Fatalf("beide Sitzungs-Einträge erwartet: %s", z)
	}
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", anna, op("anna@firma.de", "sitzung-1", false)); s != http.StatusOK {
		t.Fatalf("löschen sitzung-1: %d %s", s, b)
	}
	if z := zustand(); strings.Contains(z, "Frage sitzung-1?") || !strings.Contains(z, "Frage sitzung-2?") {
		t.Fatalf("nur sitzung-1 gelöscht erwartet: %s", z)
	}
	// Fremde Person bleibt verboten, auch mit Sitzung.
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", anna, op("admin@firma.de", "sitzung-1", true)); s != http.StatusBadRequest || !strings.Contains(string(b), "own person") {
		t.Fatalf("fremde Person mit Sitzung: %d %s", s, b)
	}
	// Ungültige Sitzungskennung: der Fold verwirft (kein 200, nichts geschrieben).
	vorher := zustand()
	if s, _ := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", anna, op("anna@firma.de", "mit leerzeichen", true)); s == http.StatusOK {
		t.Fatalf("ungültige Sitzung angenommen")
	}
	if zustand() != vorher {
		t.Fatalf("ungültige Sitzung hat geschrieben")
	}
}

func TestAgent602_Ich(t *testing.T) {
	e := newEnv608(t)
	anna := e.tokenFuer(t, "annas-claude", "anna@firma.de")
	projekt := e.tokenFuer(t, "team-container", "")

	s, b := e.anfrage(t, e.agent, "GET", "/boards/b1/ich", anna, "")
	var ich map[string]any
	_ = json.Unmarshal(b, &ich)
	if s != http.StatusOK || ich["onBehalfOf"] != "anna@firma.de" || ich["actor"] != "agent:annas-claude" || ich["board"] != "b1" || ich["role"] != "editor" {
		t.Fatalf("ich (Person): %d %s", s, b)
	}
	if strings.Contains(string(b), "abt_") {
		t.Fatalf("Auskunft enthält Token: %s", b)
	}
	s, b = e.anfrage(t, e.agent, "GET", "/boards/b1/ich", projekt, "")
	ich = nil
	_ = json.Unmarshal(b, &ich)
	if v, ok := ich["onBehalfOf"]; s != http.StatusOK || !ok || v != nil {
		t.Fatalf("ich (Projekt): %d %s", s, b)
	}
	// Fremdes Board und Mensch: keine Auskunft.
	if s, _ := e.anfrage(t, e.agent, "GET", "/boards/b2/ich", anna, ""); s != http.StatusForbidden {
		t.Fatalf("fremdes Board: %d", s)
	}
	if s, _ := e.anfrage(t, e.mensch, "GET", "/boards/b1/ich", "anna@firma.de", ""); s != http.StatusNotFound {
		t.Fatalf("Mensch: %d", s)
	}
}

// Karte #602: `onBehalfOf` an der Übernahme stempelt nur der Server — ein
// mitgeschickter Wert zählt weder beim Agenten noch beim Menschen.
func TestAgent602_UebernahmeStempeltPerson(t *testing.T) {
	e := newEnv608(t)
	anna := e.tokenFuer(t, "annas-claude", "anna@firma.de")
	claim := func(wer string, srv *httptest.Server, sitzung string) map[string]any {
		t.Helper()
		body := `{"ops":[{"type":"card.claim","payload":{"cardId":"k1","session":"` + sitzung + `","ONBEHALFOF":"chef@firma.de"}}]}`
		if s, b := e.anfrage(t, srv, "POST", "/boards/b1/ops", wer, body); s != http.StatusOK {
			t.Fatalf("claim %s: %d %s", sitzung, s, b)
		}
		b1, _ := e.boards.Get(context.Background(), "b1")
		var st struct {
			Board struct {
				Sprints []struct {
					Cards []struct {
						Claim map[string]any `json:"claim"`
					} `json:"cards"`
				} `json:"sprints"`
			} `json:"board"`
		}
		_ = json.Unmarshal(b1.StateJSON, &st)
		return st.Board.Sprints[0].Cards[0].Claim
	}
	if c := claim(anna, e.agent, "s1"); c["onBehalfOf"] != "anna@firma.de" || c["by"] != "agent:annas-claude" {
		t.Fatalf("Agent mit Person: %v", c)
	}
	// Erneuerung durch denselben Halter behält den Stempel.
	if c := claim(anna, e.agent, "s1"); c["onBehalfOf"] != "anna@firma.de" {
		t.Fatalf("Erneuerung: %v", c)
	}
	// Freigeben, dann ein Mensch mit untergeschobenem Wert: kein Stempel.
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", anna, `{"ops":[{"type":"card.release","payload":{"cardId":"k1","session":"s1"}}]}`); s != http.StatusOK {
		t.Fatalf("release: %d %s", s, b)
	}
	if c := claim("admin@firma.de", e.mensch, "m1"); c["onBehalfOf"] != nil {
		t.Fatalf("Mensch mit untergeschobenem onBehalfOf: %v", c)
	}
}
