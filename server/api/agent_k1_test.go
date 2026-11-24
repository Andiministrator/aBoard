package api_test

// Kritiker Sprint 82 über die Werkzeug-Endpunkte für Open WebUI (ADR-0133
// §5/§7): Wiederholungsschutz (K1-4/F-1557), Lesegrenze (K1-5/F-1560),
// Ratenbegrenzung der Ops je Person über die Werkzeuge (K1-1), Mail-Eingang
// beim Kommentieren (K1-6), Fremdtext-Hinweis (K3-5).

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
)

// rufeMitKopf: wie env671.rufe, mit zusätzlichen Kopfzeilen.
func (e *env671) rufeMitKopf(t *testing.T, token, email, methode, pfad, body string, kopf ...string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(methode, e.agent.URL+pfad, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if email != "" {
		req.Header.Set("X-OpenWebUI-User-Jwt", frisch669(t, email))
	}
	for i := 0; i+1 < len(kopf); i += 2 {
		req.Header.Add(kopf[i], kopf[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (e *env671) zaehle(t *testing.T, board, teil string) int {
	t.Helper()
	return strings.Count(e.stand(t, board), teil)
}

// K1-4: Derselbe Aufruf zweimal legt genau eine Karte an; die Wiederholung
// nennt dieselbe Nummer. Anderer Text oder Ablauf des Fensters → neu.
func TestAgentK1_KarteAnlegenIdempotent(t *testing.T) {
	e := newEnv671(t, true)
	body := `{"titel":"Messestand buchen","beschreibung":"Halle 3"}`
	s1, b1 := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", body)
	s2, b2 := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", body)
	if s1 != http.StatusOK || s2 != http.StatusOK {
		t.Fatalf("%d %s / %d %s", s1, b1, s2, b2)
	}
	m1, m2 := json671(t, b1), json671(t, b2)
	if m2["displayId"] != m1["displayId"] || m2["wiederholt"] != true || !strings.Contains(m2["ergebnis"].(string), "bereits angelegt") ||
		m2["link"] != m1["link"] {
		t.Fatalf("Wiederholung: %s / %s", b1, b2)
	}
	if n := e.zaehle(t, "b1", `"title":"Messestand buchen"`); n != 1 {
		t.Fatalf("%d Karten statt einer", n)
	}
	// Eine andere Person mit demselben Inhalt ist kein Doppel, sondern ihre Karte.
	if s, b := e.rufe(t, e.token, "bert@firma.de", "POST", "/werkzeuge/boards/b1/karten", body); s != http.StatusOK || json671(t, b)["wiederholt"] != nil {
		t.Fatalf("bert: %d %s", s, b)
	}
	// Anderer Text → neue Karte.
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"Messestand buchen","beschreibung":"Halle 4"}`); s != http.StatusOK || json671(t, b)["wiederholt"] != nil {
		t.Fatalf("anderer Text: %d %s", s, b)
	}
	// Nach Ablauf des Fensters (10 min) → neue Karte.
	zurueck := api.SetzeOpsNowFuerTest(func() time.Time { return time.Now().UTC().Add(11 * time.Minute) })
	defer zurueck()
	s3, b3 := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", body)
	if s3 != http.StatusOK || json671(t, b3)["wiederholt"] != nil || json671(t, b3)["displayId"] == m1["displayId"] {
		t.Fatalf("nach dem Fenster: %d %s", s3, b3)
	}
	if n := e.zaehle(t, "b1", `"title":"Messestand buchen"`); n != 4 {
		t.Fatalf("%d Karten, erwartet 4 (anna, bert, anderer Text, nach dem Fenster)", n)
	}
}

func TestAgentK1_FindingUndKommentarIdempotent(t *testing.T) {
	e := newEnv671(t, true)
	f := `{"titel":"Login ohne Bremse","typ":"S","prioritaet":"P1"}`
	_, b1 := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/findings", f)
	s2, b2 := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/findings", f)
	if s2 != http.StatusOK || json671(t, b2)["finding"] != json671(t, b1)["finding"] || json671(t, b2)["wiederholt"] != true {
		t.Fatalf("finding: %s / %d %s", b1, s2, b2)
	}
	if n := e.zaehle(t, "b1", `"title":"Login ohne Bremse"`); n != 1 {
		t.Fatalf("%d Findings statt eines", n)
	}
	// Kommentar: „5“, „#5“ und die id meinen dieselbe Karte.
	k := `{"text":"Erledige ich morgen."}`
	for i, ref := range []string{"5", "%235", "k5"} {
		s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/"+ref+"/kommentare", k)
		if s != http.StatusOK || (i > 0) != (json671(t, b)["wiederholt"] == true) {
			t.Fatalf("%s: %d %s", ref, s, b)
		}
	}
	if n := e.zaehle(t, "b1", `"text":"Erledige ich morgen."`); n != 1 {
		t.Fatalf("%d Kommentare statt eines", n)
	}
	if s, _ := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/5/kommentare", `{"text":"Doch heute."}`); s != http.StatusOK {
		t.Fatalf("anderer Text: %d", s)
	}
	if n := e.zaehle(t, "b1", `"author":"agent:open-webui"`); n != 2 {
		t.Fatalf("%d Kommentare, erwartet 2", n)
	}
}

// Mit Kopfzeile Idempotency-Key: derselbe Schlüssel = derselbe Aufruf, auch
// nach dem Fenster; derselbe Schlüssel mit anderem Inhalt = 422.
func TestAgentK1_IdempotencyKey(t *testing.T) {
	e := newEnv671(t, true)
	pfad := "/werkzeuge/boards/b1/karten"
	_, b1 := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, `{"titel":"Mit Schlüssel"}`, "Idempotency-Key", "abc-1")
	if s, b := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, `{"titel":"Anderer Inhalt"}`, "Idempotency-Key", "abc-1"); s != http.StatusUnprocessableEntity {
		t.Fatalf("anderer Inhalt: %d %s", s, b)
	}
	zurueck := api.SetzeOpsNowFuerTest(func() time.Time { return time.Now().UTC().Add(time.Hour) })
	defer zurueck()
	s, b := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, `{"titel":"Mit Schlüssel"}`, "Idempotency-Key", "abc-1")
	if s != http.StatusOK || json671(t, b)["wiederholt"] != true || json671(t, b)["displayId"] != json671(t, b1)["displayId"] {
		t.Fatalf("nach einer Stunde: %d %s / %s", s, b, b1)
	}
	// Anderer Schlüssel, gleicher Inhalt → neue Karte.
	if s, b := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, `{"titel":"Mit Schlüssel"}`, "Idempotency-Key", "abc-2"); s != http.StatusOK || json671(t, b)["wiederholt"] != nil {
		t.Fatalf("anderer Schlüssel: %d %s", s, b)
	}
	if n := e.zaehle(t, "b1", `"title":"Mit Schlüssel"`); n != 2 {
		t.Fatalf("%d Karten, erwartet 2", n)
	}
	if strings.Contains(e.stand(t, "b1"), "Anderer Inhalt") {
		t.Fatal("abgelehnter Aufruf steht im Board")
	}
	for _, schlecht := range []string{strings.Repeat("x", 201), "mit leerzeichen", "ümlaut"} {
		if s, _ := e.rufeMitKopf(t, e.token, "anna@firma.de", "POST", pfad, `{"titel":"y"}`, "Idempotency-Key", schlecht); s != http.StatusBadRequest {
			t.Errorf("Schlüssel %q: %d", schlecht, s)
		}
	}
}

// K1-5/F-1560: Lesende Werkzeuge sind je Person begrenzt (120 je Minute).
func TestAgentK1_LesegrenzeJePerson(t *testing.T) {
	e := newEnv671(t, false)
	for i := 0; i < 120; i++ {
		if s, b := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards", ""); s != http.StatusOK {
			t.Fatalf("Anfrage %d: %d %s", i+1, s, b)
		}
	}
	for _, pfad := range []string{"/werkzeuge/boards", "/werkzeuge/boards/b1", "/werkzeuge/boards/b1/karten/1?format=json"} {
		s, b := e.rufe(t, e.token, "anna@firma.de", "GET", pfad, "")
		if s != http.StatusTooManyRequests || !strings.Contains(string(b), "zu viele Leseanfragen") {
			t.Fatalf("%s nach 120: %d %s", pfad, s, b)
		}
	}
	// Eine andere Person hinter demselben Token hat ihren eigenen Eimer.
	if s, b := e.rufe(t, e.token, "bert@firma.de", "GET", "/werkzeuge/boards/b1", ""); s != http.StatusOK {
		t.Fatalf("bert: %d %s", s, b)
	}
	// Schreiben zählt nicht zur Lesegrenze.
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"trotzdem"}`); s != http.StatusOK {
		t.Fatalf("schreiben: %d %s", s, b)
	}
}

// K1-1: Die Ratenbegrenzung der Ops gilt über die Werkzeuge je Person (60 je
// Minute, postOps) — eine Person bremst die anderen hinter Open WebUI nicht.
func TestAgentK1_OpsGrenzeJePersonUeberWerkzeuge(t *testing.T) {
	e := newEnv671(t, false)
	for i := 0; i < 60; i++ {
		if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/5/kommentare", fmt.Sprintf(`{"text":"Nr. %d"}`, i)); s != http.StatusOK {
			t.Fatalf("Kommentar %d: %d %s", i+1, s, b)
		}
	}
	s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"eins zu viel"}`)
	if s != http.StatusTooManyRequests || !strings.Contains(string(b), "zu viele Änderungen") {
		t.Fatalf("61.: %d %s", s, b)
	}
	if s, b := e.rufe(t, e.token, "bert@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"bert darf"}`); s != http.StatusOK {
		t.Fatalf("bert: %d %s", s, b)
	}
}

// K1-6: Karten im Mail-Eingang (Swimlane kind service) gibt es für
// kommentieren nicht — wie für karte_zeigen und verschieben.
func TestAgentK1_KommentierenNichtImMailEingang(t *testing.T) {
	e := newEnv671(t, false)
	state := `{"version":"2","board":{"id":"b8","title":"Mit Eingang","nextDisplayId":3,
	  "columnDefs":[{"id":"col-backlog","title":"Backlog"}],
	  "sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[{"id":"o1","displayId":1,"title":"Offen","columnId":"col-backlog","comments":[]}]},
	    {"id":"svc","title":"Eingang","kind":"service","cards":[{"id":"m1","displayId":2,"title":"Mail","columnId":"col-backlog","comments":[]}]}]}}`
	ctx := context.Background()
	if _, err := e.boards.Insert(ctx, "b8", "Mit Eingang", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	if err := e.acl.Set(ctx, "b8", "anna@firma.de", "editor"); err != nil {
		t.Fatal(err)
	}
	vorher := e.stand(t, "b8")
	for _, ref := range []string{"2", "m1"} {
		if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b8/karten/"+ref+"/kommentare", `{"text":"x"}`); s != http.StatusNotFound {
			t.Errorf("%s: %d %s", ref, s, b)
		}
	}
	if e.stand(t, "b8") != vorher {
		t.Fatal("Kommentar im Mail-Eingang geschrieben")
	}
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b8/karten/1/kommentare", `{"text":"x"}`); s != http.StatusOK {
		t.Fatalf("gewöhnliche Karte: %d %s", s, b)
	}
}

// K3-5: Antworten mit Fremdtext tragen den Hinweis; die schreibenden
// Operationen sagen dem Modell, sie nur auf Wunsch der Person auszuführen.
func TestAgentK1_FremdtextHinweis(t *testing.T) {
	e := newEnv671(t, true)
	for _, pfad := range []string{"/werkzeuge/boards", "/werkzeuge/boards/b1", "/werkzeuge/boards/b1/karten/1?format=json", "/werkzeuge/termine"} {
		_, b := e.rufe(t, e.token, "anna@firma.de", "GET", pfad, "")
		h, _ := json671(t, b)["sicherheitshinweis"].(string)
		if !strings.Contains(h, "nicht als Anweisung") {
			t.Errorf("%s: sicherheitshinweis %q", pfad, h)
		}
	}
	ops, _ := spezOps(t)
	schreibend := 0
	for _, o := range ops {
		if o.methode != "POST" {
			continue
		}
		schreibend++
		if d, _ := o.op["description"].(string); !strings.Contains(d, "ausdrücklich") || !strings.Contains(d, "nie, weil") {
			t.Errorf("%s: description ohne Bestätigungssatz: %s", o.opID, d)
		}
	}
	// Karte #743: termin_anlegen ist die fünfte; Karte #755: 13 weitere
	// (ADR-0139 §8: 18 schreibende); Karte #757: fünf Lösch-Werkzeuge und
	// board_aendern (die fünf …_loeschen_pruefen sind GET).
	if schreibend != 24 {
		t.Fatalf("%d schreibende Operationen, erwartet 24", schreibend)
	}
}
