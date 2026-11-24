package api_test

// Karte #778 (ADR-0140 §2): Marke titelVonAgent — gesetzt, wenn ein Agent
// den Titel ändert, entfernt, wenn ein Mensch ihn ändert; was ein Schreiber
// selbst mitschickt, zählt nicht. Dieselbe Mechanik wie fristVonAgent
// (TestKalender748_MarkeAnKarten), die Umgebung von dort.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func titelMarkeAnKarte(c map[string]any) []string {
	var k []string
	for x := range c {
		if strings.EqualFold(x, "titelVonAgent") {
			k = append(k, fmt.Sprintf("%s=%v", x, c[x]))
		}
	}
	return k
}

func TestTitelMarke778(t *testing.T) {
	e := newEnv748(t)
	assistent := e.agentToken(t)
	pruefe := func(schritt, id string, soll bool) {
		t.Helper()
		got := titelMarkeAnKarte(e.karte(t, id))
		if soll && (len(got) != 1 || got[0] != "titelVonAgent=true") || !soll && len(got) != 0 {
			t.Fatalf("%s: Marke %v, erwartet %v", schritt, got, soll)
		}
	}
	pruefe("Ausgangslage", "k1", false)

	// Agent ändert die Frist → keine Titel-Marke (fristVonAgent zählt nicht).
	e.ops(t, e.agent, assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"dueDate","value":"2026-10-21"}}`)
	pruefe("Agent ändert Frist", "k1", false)
	// Agent ändert den Titel → Marke.
	e.ops(t, e.agent, assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Angebot Müller GmbH"}}`)
	pruefe("Agent ändert Titel", "k1", true)
	// Mensch ändert etwas anderes → Marke bleibt.
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"card.field","payload":{"cardId":"k1","field":"dueTime","value":"14:00"}}`)
	pruefe("Mensch ändert Uhrzeit", "k1", true)
	// Mensch schickt die ganze Karte ohne Marke: kein 422, Marke bleibt.
	k := e.karte(t, "k1")
	delete(k, "titelVonAgent")
	k["description"] = "neu"
	roh, _ := json.Marshal(k)
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"card.upsert","payload":{"card":`+string(roh)+`}}`)
	pruefe("Mensch-Upsert ohne Marke, Titel gleich", "k1", true)
	// Agent kann die Marke nicht selbst wegnehmen, in keiner Schreibweise.
	e.ops(t, e.agent, assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"titelVonAgent","value":false}}`)
	e.ops(t, e.agent, assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"TITELVONAGENT","value":false}}`)
	pruefe("Agent setzt Marke selbst", "k1", true)
	// Mensch ändert den Titel → Marke weg.
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Angebot"}}`)
	pruefe("Mensch ändert Titel", "k1", false)
	// Ein Mensch kann sie nicht setzen.
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"card.field","payload":{"cardId":"k1","field":"titelVonAgent","value":true}}`)
	pruefe("Mensch setzt Marke", "k1", false)
	// Neue Karte vom Agenten → Marke, auch wenn er false mitschickt.
	e.ops(t, e.agent, assistent, `{"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"k5","title":"Rückruf","titelVonAgent":false}}}`)
	pruefe("neue Agenten-Karte", "k5", true)
	// Neue Karte vom Menschen mit erfundener Marke → keine.
	e.ops(t, e.mensch, "anna@firma.de", `{"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"k6","title":"Notiz","titelVonAgent":true}}}`)
	pruefe("neue Menschen-Karte", "k6", false)
}
