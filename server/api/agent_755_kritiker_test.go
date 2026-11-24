package api_test

// Karte #755 Teil 2 — Nacharbeit zum Sonnet-Kritiker (K-1 bis K-9).

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// K-1: Open WebUI setzt beim Anlegen weder Spalte noch Owner.
func TestAgent755K1_DelegiertOhneSpalteUndOwner(t *testing.T) {
	e := newEnv671(t, true)
	vorher := e.stand(t, "b1")
	for _, body := range []string{`{"titel":"x","spalte":"Ready"}`, `{"titel":"x","owner":"Claude Opus"}`} {
		if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, s, b)
		}
	}
	if e.stand(t, "b1") != vorher {
		t.Fatal("geschrieben")
	}
	// Leer oder null zählt nicht als Wunsch (Verify V-1).
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"z","spalte":"","owner":null}`); s != http.StatusOK {
		t.Errorf("leere spalte/owner: %d %s", s, b)
	}
	// labels und Frist bleiben erlaubt.
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"y","labels":["a"],"faellig":"2026-12-01"}`); s != http.StatusOK {
		t.Errorf("labels/faellig: %d %s", s, b)
	}
}

// K-2: Nach dem Löschen der Swimlane legt dieselbe Anfrage sie neu an.
func TestAgent755K2_SwimlaneNachLoeschenNeu(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	s, m := e.schreibe755(t, "/swimlanes", `{"titel":"Thema Y"}`)
	if s != http.StatusOK {
		t.Fatalf("%d %v", s, m)
	}
	e.zuruecksetzen755(t) // Swimlane weg
	s, m = e.schreibe755(t, "/swimlanes", `{"titel":"Thema Y"}`)
	if s != http.StatusOK || m["wiederholt"] == true {
		t.Fatalf("nach Löschen: %d %v", s, m)
	}
	if l := e.lanes755(t)[m["swimlaneId"].(string)]; l == nil || l["title"] != "Thema Y" {
		t.Errorf("Swimlane fehlt: %v", m)
	}
}

// K-2 mit Idempotency-Key: derselbe Schlüssel ist dieselbe Anfrage — nach
// dem Löschen nichts neu anlegen (wie karte_anlegen, KA-4).
func TestAgent755K2_SwimlaneMitKopfNichtNeu(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	rufe := func() (int, string) {
		req, _ := http.NewRequest("POST", e.agent.URL+"/werkzeuge/boards/b1/swimlanes", strings.NewReader(`{"titel":"Thema Z"}`))
		req.Header.Set("Authorization", "Bearer "+e.projekt)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "schluessel-1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b strings.Builder
		_, _ = io.Copy(&b, resp.Body)
		return resp.StatusCode, b.String()
	}
	if s, b := rufe(); s != http.StatusOK {
		t.Fatalf("%d %s", s, b)
	}
	e.zuruecksetzen755(t)
	if s, b := rufe(); s != http.StatusOK || !strings.Contains(b, "inzwischen gelöscht") {
		t.Errorf("mit Schlüssel nach Löschen: %d %s", s, b)
	}
	if n := len(e.lanes755(t)); n != 3 {
		t.Errorf("%d Swimlanes, erwartet 3 (nichts neu)", n)
	}
}

// K-3: Ein gewollter Eintrag, den der Bestand doppelt führt, bleibt einmal.
func TestAgent755K3_ListenDoppelImBestand(t *testing.T) {
	e := newEnv671(t, true)
	e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"owner":"Anna",`, `"owner":"Anna","labels":["a","a","b","b"],`))
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"labels":["a"]}`); s != http.StatusOK {
		t.Fatalf("%d %v", s, m)
	}
	if got := jsonText(e.karte755(t, "b1", 1)["labels"]); got != `["a"]` {
		t.Errorf("labels %s", got)
	}
}

// K-4: swimlane_aendern schreibt nur die genannten Meta-Felder.
func TestAgent755K4_SwimlaneNurGenannteFelder(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	if s, m := e.schreibe755(t, "/swimlanes/s1", `{"titel":"Neu"}`); s != http.StatusOK {
		t.Fatalf("%d %v", s, m)
	}
	// Die Op trägt nur title — im Protokoll des Stands nicht sichtbar, deshalb
	// über die Wirkung: archived bleibt UNGESETZT (board755 führt es bei s1 nicht).
	if l := e.lanes755(t)["s1"]; l["title"] != "Neu" || l["kind"] != "sprint" {
		t.Errorf("s1: %v", l)
	} else if _, da := l["archived"]; da {
		t.Errorf("archived mitgeschrieben: %v", l)
	}
}

// K-5: Sprint-Meta-Karten bekommen keine Verweise und keine Übernahme.
func TestAgent755K5_SprintMetaGeschuetzt(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	vorher := e.stand(t, "b1")
	for pfad, body := range map[string]string{"/karten/4/verweise": `{"ziel":"1","typ":"related"}`,
		"/karten/1/verweise": `{"ziel":"4","typ":"related"}`, "/karten/4/uebernehmen": `{"session":"s"}`} {
		if s, m := e.schreibe755(t, pfad, body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %v", pfad, s, m)
		}
	}
	if e.stand(t, "b1") != vorher {
		t.Fatal("geschrieben")
	}
	// Aufräumen bleibt erlaubt: Verweis entfernen, Übernahme freigeben.
	if s, m := e.schreibe755(t, "/karten/4/verweise/entfernen", `{"ziel":"1","typ":"related"}`); s != http.StatusOK {
		t.Errorf("entfernen an Meta-Karte: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/4/freigeben", `{"session":"s"}`); s != http.StatusOK {
		t.Errorf("freigeben an Meta-Karte: %d %v", s, m)
	}
}

// K-7: Varianten derselben Person: genau ein Eintrag bleibt, die exakte Schreibweise.
func TestAgent755K7_WartenVariantenDeterministisch(t *testing.T) {
	e := newEnv671(t, true)
	annaB1, _ := e.anna755(t)
	eintrag := `{"question":"alt","since":"2026-10-01T00:00:00.000Z","askedBy":"x"}`
	e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"nextDisplayId":10,`,
		`"nextDisplayId":10,"awaitingUser":{"Anna@Firma.de":`+eintrag+`,"anna@firma.de":`+eintrag+`,"ANNA@FIRMA.DE":`+eintrag+`},`))
	if s, b := e.rufe(t, annaB1, "", "POST", "/werkzeuge/boards/b1/warten", `{"frage":"neu"}`); s != http.StatusOK {
		t.Fatalf("%d %s", s, b)
	}
	a := e.brett755(t, "b1")["awaitingUser"].(map[string]any)
	if len(a) != 1 || a["anna@firma.de"] == nil || a["anna@firma.de"].(map[string]any)["question"] != "neu" {
		t.Errorf("awaitingUser: %v", a)
	}
	// Ohne exakte Schreibweise: die erste in Sortierung, deterministisch (V-9).
	for lauf := 0; lauf < 5; lauf++ {
		e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"nextDisplayId":10,`,
			`"nextDisplayId":10,"awaitingUser":{"Anna@Firma.de":`+eintrag+`,"ANNA@FIRMA.DE":`+eintrag+`},`))
		if s, b := e.rufe(t, annaB1, "", "POST", "/werkzeuge/boards/b1/warten", `{"frage":"neu"}`); s != http.StatusOK {
			t.Fatalf("%d %s", s, b)
		}
		a = e.brett755(t, "b1")["awaitingUser"].(map[string]any)
		if len(a) != 1 || a["ANNA@FIRMA.DE"] == nil {
			t.Fatalf("Lauf %d: %v", lauf, a)
		}
	}
}

// K-9: agentKontext nur für editor-Tokens.
func TestAgent755K9_AgentKontextNurEditor(t *testing.T) {
	e := newEnv671(t, true)
	e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"owner":"Anna",`, `"owner":"Anna","agentContext":"Geheimer Plan",`))
	if _, b := e.rufe(t, e.leser, "", "GET", "/werkzeuge/boards/b1/karten/1?format=json", ""); strings.Contains(string(b), "Geheimer Plan") {
		t.Errorf("viewer sieht agentKontext")
	}
	if _, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1?format=json", ""); !strings.Contains(string(b), "Geheimer Plan") {
		t.Errorf("editor sieht agentKontext nicht")
	}
}
