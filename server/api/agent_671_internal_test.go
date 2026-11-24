package api

// Karte #671: Die Positivliste werkzeugDarf erlaubt genau die Wege der
// Spezifikation — unabhängig davon, ob der Mux einen Weg ohnehin mit 405
// beantwortet. Der Mux ist die zweite Hürde, nicht die erste: Wer später eine
// Route ergänzt, soll sie nicht schon offen vorfinden.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestWerkzeugDarf671_GenauDieSpezifikation(t *testing.T) {
	var spez struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(WerkzeugSpezifikation(), &spez); err != nil {
		t.Fatal(err)
	}
	beschrieben := map[string]bool{"GET /openapi.json": true}
	pfade := []string{"/openapi.json"}
	for pfad, ops := range spez.Paths {
		pfade = append(pfade, pfad)
		for m := range ops {
			beschrieben[strings.ToUpper(m)+" "+pfad] = true
		}
	}
	// Daneben liegende Pfade, die es nicht geben darf.
	pfade = append(pfade, "/werkzeuge", "/werkzeuge/boards/{boardId}/karten/{karte}", "/werkzeuge/boards/{boardId}/x",
		"/werkzeuge/boards/{boardId}/karten/{karte}/kommentare/{kommentar}/x", "/werkzeuge/boards/{boardId}/karten/{karte}/verweise/x",
		"/werkzeuge/boards/{boardId}/termine/{termin}/x", "/werkzeuge/boards/{boardId}/swimlanes/{swimlane}/x", "/werkzeuge/x/{boardId}",
		// Karte #757: neben den Lösch-Paaren.
		"/werkzeuge/boards/{boardId}/spalten/{spalte}", "/werkzeuge/boards/{boardId}/anhaenge/{anhang}",
		"/werkzeuge/boards/{boardId}/karten/{karte}/loeschen/x", "/werkzeuge/boards/{boardId}/loeschen")
	editor := &AgentPrincipal{Name: "open-webui", BoardID: "b1", Role: storage.AgentRolleEditor, OnBehalfOf: "anna@firma.de",
		Delegiert: true, tokenRolle: storage.AgentRolleEditor}
	// Karte #755: Werkzeuge, die Open WebUI nicht sieht, prüft dieser Test mit
	// einem Projekt-Token; dass ein delegierendes sie NICHT darf, prüft die
	// Rollen-Matrix (agent_752_test.go).
	var delegiertSpez struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(WerkzeugSpezifikationDelegiert(), &delegiertSpez); err != nil {
		t.Fatal(err)
	}
	// Mit Person (Personen-Token): auf_antwort_warten gibt es nur mit Person;
	// dass ein Projekt-Token ohne Person es nicht darf, prüft die Rollen-Matrix.
	projekt := &AgentPrincipal{Name: "anna-b1", BoardID: "b1", Role: storage.AgentRolleEditor, tokenRolle: storage.AgentRolleEditor, OnBehalfOf: "anna@firma.de"}
	ersetzen := strings.NewReplacer("{boardId}", "b1", "{karte}", "1", "{kommentar}", "k-1", "{termin}", "t-1", "{swimlane}", "s1", "{anhang}", "a-1", "{spalte}", "col-ready")
	for _, pfad := range pfade {
		for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
			segs, ok := agentSegmente(ersetzen.Replace(pfad))
			if !ok {
				t.Fatal(pfad)
			}
			soll := beschrieben[m+" "+pfad]
			// Die Board-Liste hat kein Board; dort zählt die Person.
			p := editor
			if werkzeugOhneBoard(segs) || werkzeugIstSpezifikation(segs) {
				p = &AgentPrincipal{Name: "open-webui", OnBehalfOf: "anna@firma.de", Delegiert: true, tokenRolle: storage.AgentRolleEditor}
			}
			if _, fuerOWUI := delegiertSpez.Paths[pfad]; !fuerOWUI && pfad != "/openapi.json" && (beschrieben["GET "+pfad] || beschrieben["POST "+pfad]) {
				p = projekt
			}
			if ist := werkzeugDarf(p, m, segs); ist != soll {
				t.Errorf("werkzeugDarf(%s %s) = %v, Spezifikation sagt %v", m, pfad, ist, soll)
			}
		}
	}
	// Leserecht: nur die GET-Wege mit Board — ohne die Prüf-Werkzeuge der
	// Lösch-Paare (Karte #757): Die sind lesend, gehören aber zum Löschen.
	leser := *projekt
	leser.Role = storage.AgentRolleViewer
	for weg := range beschrieben {
		m, pfad, _ := strings.Cut(weg, " ")
		segs, _ := agentSegmente(ersetzen.Replace(pfad))
		if werkzeugOhneBoard(segs) || werkzeugIstSpezifikation(segs) {
			continue
		}
		if ist := werkzeugDarf(&leser, m, segs); ist != (m == http.MethodGet && !strings.HasSuffix(pfad, "/loeschen")) {
			t.Errorf("viewer %s: %v", weg, ist)
		}
	}
}
