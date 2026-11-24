package api_test

// Karte #755 (ADR-0139 Stufe 4a, Teil 2): Inhaltstests der Schreib-Werkzeuge —
// was sie am Board wirklich ändern, und wo sie ablehnen. Rechte prüft die
// Rollen-Matrix (agent_752_test.go, agent_753_test.go); hier geht es um die
// Wirkung. Jeder Test startet von board755 (agent_755_matrix_test.go).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// brett755: das Board-Objekt aus dem gespeicherten Stand.
func (e *env671) brett755(t *testing.T, board string) map[string]any {
	t.Helper()
	var s struct {
		Board map[string]any `json:"board"`
	}
	if err := json.Unmarshal([]byte(e.stand(t, board)), &s); err != nil {
		t.Fatal(err)
	}
	return s.Board
}

// karte755: die Karte mit dieser Nummer aus dem gespeicherten Stand.
func (e *env671) karte755(t *testing.T, board string, nr int) map[string]any {
	t.Helper()
	for _, l := range e.brett755(t, board)["sprints"].([]any) {
		for _, k := range l.(map[string]any)["cards"].([]any) {
			if km := k.(map[string]any); km["displayId"] == float64(nr) {
				return km
			}
		}
	}
	t.Fatalf("Karte #%d fehlt", nr)
	return nil
}

// schreibe755: POST an ein Werkzeug von Karte 1 auf b1 mit dem Projekt-Token.
func (e *env671) schreibe755(t *testing.T, pfad, body string) (int, map[string]any) {
	t.Helper()
	s, b := e.rufe(t, e.projekt, "", "POST", "/werkzeuge/boards/b1"+pfad, body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return s, m
}

func TestAgent755_KarteAendern(t *testing.T) {
	e := newEnv671(t, true)
	stand := e.stand755(t)
	vorher := e.stand(t, "b1")
	// Freitext ohne oder mit falschem stand: nichts geschrieben.
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"titel":"Neu"}`); s != http.StatusBadRequest {
		t.Errorf("ohne stand: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"titel":"Neu","stand":"0000000000000000"}`); s != http.StatusConflict {
		t.Errorf("falscher stand: %d %v", s, m)
	}
	if e.stand(t, "b1") != vorher {
		t.Fatal("ohne passenden stand geschrieben")
	}
	s, m := e.schreibe755(t, "/karten/1/aendern", `{"stand":"`+stand+`","titel":"Neu","beschreibung":"B","agentKontext":"AK",
		"owner":"Bert","beteiligte":["Anna","Bert","Anna"],"labels":["x"],"faellig":"2026-11-01","faelligUhrzeit":"09:30",
		"start":"2026-10-20","startUhrzeit":"08:00","prioritaet":"hoch","komplexitaet":"mittel","aufwand":2.5,
		"kritisch":true,"blockiert":false,"qa":true}`)
	if s != http.StatusOK {
		t.Fatalf("ändern: %d %v", s, m)
	}
	k := e.karte755(t, "b1", 1)
	for feld, soll := range map[string]any{"title": "Neu", "description": "B", "agentContext": "AK", "owner": "Bert",
		"dueDate": "2026-11-01", "dueTime": "09:30", "startDate": "2026-10-20", "startTime": "08:00", "priority": "hoch",
		"complexity": "mittel", "effort": 2.5, "critical": true, "blocked": false, "qa": true} {
		if k[feld] != soll {
			t.Errorf("%s = %v, erwartet %v", feld, k[feld], soll)
		}
	}
	if jsonText(k["assignees"]) != `["Anna","Bert"]` || jsonText(k["labels"]) != `["x"]` {
		t.Errorf("Listen: %v %v", k["assignees"], k["labels"])
	}
	// Der neue stand stimmt mit karte_zeigen überein; der alte gilt nicht mehr.
	_, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1?format=json", "")
	if neu := json671(t, b)["stand"]; m["stand"] != neu || neu == stand {
		t.Errorf("stand nach dem Ändern: %v, karte_zeigen %v, vorher %s", m["stand"], neu, stand)
	}
	if s, _ := e.schreibe755(t, "/karten/1/aendern", `{"titel":"Noch neuer","stand":"`+stand+`"}`); s != http.StatusConflict {
		t.Errorf("alter stand nach Änderung: %d", s)
	}
	// Datum entfernen nimmt die Uhrzeit mit; Uhrzeit ohne Datum: 400.
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"faellig":null}`); s != http.StatusOK {
		t.Fatalf("faellig null: %d %v", s, m)
	}
	if k := e.karte755(t, "b1", 1); k["dueDate"] != nil || k["dueTime"] != nil {
		t.Errorf("Frist nicht entfernt: %v %v", k["dueDate"], k["dueTime"])
	}
	for _, body := range []string{`{"faelligUhrzeit":"10:00"}`, `{"prioritaet":"dringend"}`, `{"komplexitaet":"riesig"}`, `{"aufwand":-1}`,
		`{"aufwand":"zwei"}`, `{"kritisch":"ja"}`, `{"faellig":"2026-02-30"}`, `{"startUhrzeit":"25:00"}`, `{"labels":"x"}`, `{"labels":[""]}`,
		`{"farbe":"rot"}`, `{}`, `{"stand":"x"}`, `{"owner":"a\nb"}`} {
		if s, m := e.schreibe755(t, "/karten/1/aendern", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, s, m)
		}
	}
}

// Labels und Beteiligte gehen als Mengen-Ops: Was nicht zur Änderung gehört,
// bleibt — auch ein Eintrag, den es doppelt gibt, wird nur einmal entfernt.
func TestAgent755_KarteAendernListen(t *testing.T) {
	e := newEnv671(t, true)
	e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"owner":"Anna",`, `"owner":"Anna","labels":["A","B"],"assignees":["Anna"],`))
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"labels":["B","C"],"beteiligte":[]}`); s != http.StatusOK {
		t.Fatalf("%d %v", s, m)
	}
	k := e.karte755(t, "b1", 1)
	if jsonText(k["labels"]) != `["B","C"]` || jsonText(k["assignees"]) != `[]` {
		t.Errorf("labels %v, assignees %v", k["labels"], k["assignees"])
	}
	// Dieselbe Liste noch einmal: nichts zu tun, kein Fehler.
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"labels":["B","C"]}`); s != http.StatusOK || !strings.Contains(jsonText(m["ergebnis"]), "nichts zu ändern") {
		t.Errorf("unverändert: %d %v", s, m)
	}
}

// Eine Beschreibung, die karte_zeigen gekürzt zeigt, ersetzt das Werkzeug
// nicht — der ungesehene Rest wäre weg.
func TestAgent755_KarteAendernGekuerzteBeschreibung(t *testing.T) {
	e := newEnv671(t, true)
	lang := strings.Repeat("a", 8001)
	e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"description":"Text\n- [ ] Eins\n- [x] Zwei"`, `"description":"`+lang+`"`))
	_, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1?format=json", "")
	ansicht := json671(t, b)
	if ansicht["beschreibungGekuerzt"] != true {
		t.Fatalf("beschreibungGekuerzt fehlt: %.200s", b)
	}
	stand := ansicht["stand"].(string)
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"stand":"`+stand+`","beschreibung":"kurz"}`); s != http.StatusConflict {
		t.Errorf("gekürzte Beschreibung ersetzt: %d %v", s, m)
	}
	if e.karte755(t, "b1", 1)["description"] != lang {
		t.Fatal("Beschreibung verändert")
	}
	// Der Titel derselben Karte bleibt änderbar.
	if s, m := e.schreibe755(t, "/karten/1/aendern", `{"stand":"`+stand+`","titel":"Neu"}`); s != http.StatusOK {
		t.Errorf("Titel: %d %v", s, m)
	}
}

func TestAgent755_KarteAendernSprintMeta(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	_, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/4?format=json", "")
	stand := json671(t, b)["stand"].(string)
	if s, m := e.schreibe755(t, "/karten/4/aendern", `{"stand":"`+stand+`","titel":"Umbenannt"}`); s != http.StatusBadRequest {
		t.Errorf("Meta-Titel: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/4/aendern", `{"stand":"`+stand+`","beschreibung":"Notiz"}`); s != http.StatusOK {
		t.Errorf("Meta-Beschreibung: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/4/archivieren", `{}`); s != http.StatusBadRequest {
		t.Errorf("Meta archivieren: %d %v", s, m)
	}
}

func TestAgent755_Checkliste(t *testing.T) {
	e := newEnv671(t, true)
	stand := e.stand755(t)
	s, m := e.schreibe755(t, "/karten/1/checkliste", `{"punkt":1,"stand":"`+stand+`"}`)
	if s != http.StatusOK || m["erledigt"] != true || m["text"] != "Eins" {
		t.Fatalf("umschalten: %d %v", s, m)
	}
	if d := e.karte755(t, "b1", 1)["description"]; d != "Text\n- [x] Eins\n- [x] Zwei" {
		t.Errorf("Beschreibung: %q", d)
	}
	stand = m["stand"].(string)
	if s, m := e.schreibe755(t, "/karten/1/checkliste", `{"punkt":1,"erledigt":true,"stand":"`+stand+`"}`); s != http.StatusOK || !strings.Contains(jsonText(m["ergebnis"]), "schon") {
		t.Errorf("schon abgehakt: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/1/checkliste", `{"punkt":2,"erledigt":false,"stand":"`+stand+`"}`); s != http.StatusOK {
		t.Errorf("zurücksetzen: %d %v", s, m)
	}
	if d := e.karte755(t, "b1", 1)["description"]; d != "Text\n- [x] Eins\n- [ ] Zwei" {
		t.Errorf("Beschreibung: %q", d)
	}
	for _, body := range []string{`{"punkt":3,"stand":"STAND"}`, `{"punkt":0,"stand":"STAND"}`, `{"punkt":1.5,"stand":"STAND"}`, `{"stand":"STAND"}`, `{"punkt":1}`} {
		_, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1?format=json", "")
		body = strings.ReplaceAll(body, "STAND", json671(t, b)["stand"].(string))
		if s, m := e.schreibe755(t, "/karten/1/checkliste", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, s, m)
		}
	}
	// Zeilen in einem Code-Block zählen nicht.
	e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"description":"Text\n- [ ] Eins\n- [x] Zwei"`,
		`"description":"`+"```\\n- [ ] Im Code\\n```\\n- [ ] Echt"+`"`))
	_, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1?format=json", "")
	if s, m := e.schreibe755(t, "/karten/1/checkliste", `{"punkt":1,"stand":"`+json671(t, b)["stand"].(string)+`"}`); s != http.StatusOK || m["text"] != "Echt" {
		t.Fatalf("Code-Block: %d %v", s, m)
	}
	if d := e.karte755(t, "b1", 1)["description"]; d != "```\n- [ ] Im Code\n```\n- [x] Echt" {
		t.Errorf("Code-Block verändert: %q", d)
	}
}

func TestAgent755_ArchivierenWiederherstellen(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	if s, m := e.schreibe755(t, "/karten/1/archivieren", `{}`); s != http.StatusOK || e.karte755(t, "b1", 1)["archived"] != true {
		t.Fatalf("archivieren: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/1/archivieren", `{}`); s != http.StatusOK || !strings.Contains(jsonText(m["ergebnis"]), "schon") {
		t.Errorf("zweimal: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/1/wiederherstellen", `{}`); s != http.StatusOK || e.karte755(t, "b1", 1)["archived"] != false {
		t.Fatalf("wiederherstellen: %d %v", s, m)
	}
	// Karte in einer archivierten Swimlane: Hinweis auf swimlane_aendern.
	if s, m := e.schreibe755(t, "/karten/7/archivieren", `{}`); s != http.StatusOK || !strings.Contains(jsonText(m["hinweis"]), "swimlane_aendern") {
		t.Errorf("archivierte Swimlane: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/1/archivieren", `{"grund":"x"}`); s != http.StatusBadRequest {
		t.Errorf("unbekanntes Feld: %d %v", s, m)
	}
}

func TestAgent755_KommentarBearbeiten(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	if s, m := e.schreibe755(t, "/karten/1/kommentare/k-projekt", `{"text":"Neu formuliert"}`); s != http.StatusOK || m["kommentarId"] != "k-projekt" {
		t.Fatalf("eigener: %d %v", s, m)
	}
	var eigen, fremd map[string]any
	for _, c := range e.karte755(t, "b1", 1)["comments"].([]any) {
		switch cm := c.(map[string]any); cm["id"] {
		case "k-projekt":
			eigen = cm
		case "k-leser":
			fremd = cm
		}
	}
	if eigen["text"] != "Neu formuliert" || eigen["edited"] == nil || eigen["author"] != "agent:projekt" {
		t.Errorf("eigener Kommentar: %v", eigen)
	}
	vorher := e.stand(t, "b1")
	if s, m := e.schreibe755(t, "/karten/1/kommentare/k-leser", `{"text":"übernommen"}`); s != http.StatusForbidden {
		t.Errorf("fremder Kommentar: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/1/kommentare/gibt-es-nicht", `{"text":"x"}`); s != http.StatusNotFound {
		t.Errorf("unbekannter Kommentar: %d %v", s, m)
	}
	if e.stand(t, "b1") != vorher || fremd["text"] != "Von leser" {
		t.Fatal("fremder Kommentar verändert")
	}
}

// links755: die Verweise einer Karte als „typ:ziel“.
func (e *env671) links755(t *testing.T, nr int) []string {
	t.Helper()
	var aus []string
	l, _ := e.karte755(t, "b1", nr)["links"].([]any)
	for _, x := range l {
		v := x.(map[string]any)
		aus = append(aus, v["type"].(string)+":"+v["targetId"].(string))
	}
	return aus
}

func TestAgent755_Verweise(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	if s, m := e.schreibe755(t, "/karten/1/verweise", `{"ziel":"2","typ":"blocks"}`); s != http.StatusOK {
		t.Fatalf("setzen: %d %v", s, m)
	}
	if got := strings.Join(e.links755(t, 1), ","); got != "related:k5,blocks:k2" {
		t.Errorf("Karte 1: %s", got)
	}
	if got := strings.Join(e.links755(t, 2), ","); got != "blocked-by:k1" {
		t.Errorf("Karte 2: %s", got)
	}
	if s, m := e.schreibe755(t, "/karten/1/verweise", `{"ziel":"#2","typ":"blocks"}`); s != http.StatusOK || !strings.Contains(jsonText(m["ergebnis"]), "bestand schon") {
		t.Errorf("zweimal: %d %v", s, m)
	}
	for _, body := range []string{`{"ziel":"1","typ":"related"}`, `{"ziel":"2","typ":"haengt-an"}`, `{"ziel":"2"}`} {
		if s, m := e.schreibe755(t, "/karten/1/verweise", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, s, m)
		}
	}
	if s, _ := e.schreibe755(t, "/karten/1/verweise", `{"ziel":"99","typ":"related"}`); s != http.StatusNotFound {
		t.Errorf("unbekannte Zielkarte: %d", s)
	}
	// Entfernen: beide Seiten.
	if s, m := e.schreibe755(t, "/karten/1/verweise/entfernen", `{"ziel":"5","typ":"related"}`); s != http.StatusOK {
		t.Fatalf("entfernen: %d %v", s, m)
	}
	if got := strings.Join(append(e.links755(t, 1), e.links755(t, 5)...), ","); got != "blocks:k2" {
		t.Errorf("nach dem Entfernen: %s", got)
	}
	// Hängender Verweis mit Zusatzschlüssel: über die id entfernbar.
	e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"links":[{"targetId":"k5","type":"related","created":"2026-10-01"}],"comments":[{"id":"k-projekt"`,
		`"links":[{"targetId":"weg","type":"depends-on","notiz":"x"}],"comments":[{"id":"k-projekt"`))
	if s, m := e.schreibe755(t, "/karten/1/verweise/entfernen", `{"ziel":"weg","typ":"depends-on"}`); s != http.StatusOK {
		t.Fatalf("hängend: %d %v", s, m)
	}
	if got := e.links755(t, 1); len(got) != 0 {
		t.Errorf("hängender Verweis bleibt: %v", got)
	}
}

func TestAgent755_UebernehmenFreigeben(t *testing.T) {
	e := newEnv671(t, true)
	annaB1, _ := e.anna755(t)
	e.zuruecksetzen755(t)
	s, m := e.schreibe755(t, "/karten/1/uebernehmen", `{"session":"a","dauerMinuten":30}`)
	if s != http.StatusOK {
		t.Fatalf("übernehmen: %d %v", s, m)
	}
	bis, err := time.Parse(time.RFC3339, m["bis"].(string))
	if err != nil || bis.Before(time.Now().Add(29*time.Minute)) || bis.After(time.Now().Add(31*time.Minute)) {
		t.Errorf("bis %v (%v)", m["bis"], err)
	}
	if c := e.karte755(t, "b1", 1)["claim"].(map[string]any); c["by"] != "agent:projekt" || c["session"] != "a" {
		t.Errorf("claim: %v", c)
	}
	// Ein anderer Agent bekommt 409 mit dem Halter; freigeben darf er nicht.
	if s, b := e.rufe(t, annaB1, "", "POST", "/werkzeuge/boards/b1/karten/1/uebernehmen", `{"session":"b"}`); s != http.StatusConflict || !strings.Contains(string(b), "agent:projekt") {
		t.Errorf("fremd übernehmen: %d %s", s, b)
	}
	if s, b := e.rufe(t, annaB1, "", "POST", "/werkzeuge/boards/b1/karten/1/freigeben", `{"session":"b"}`); s != http.StatusConflict {
		t.Errorf("fremd freigeben: %d %s", s, b)
	}
	// Dieselbe Sitzung verlängert; andere Sitzung desselben Tokens ist fremd.
	if s, m := e.schreibe755(t, "/karten/1/freigeben", `{"session":"x"}`); s != http.StatusConflict {
		t.Errorf("falsche Sitzung: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/karten/1/freigeben", `{"session":"a"}`); s != http.StatusOK {
		t.Fatalf("freigeben: %d %v", s, m)
	}
	if _, da := e.karte755(t, "b1", 1)["claim"]; da {
		t.Error("claim bleibt")
	}
	if s, m := e.schreibe755(t, "/karten/1/freigeben", `{"session":"a"}`); s != http.StatusOK || !strings.Contains(jsonText(m["ergebnis"]), "nicht übernommen") {
		t.Errorf("zweimal: %d %v", s, m)
	}
	for _, body := range []string{`{}`, `{"session":""}`, `{"session":"a","dauerMinuten":0}`, `{"session":"a","dauerMinuten":2000}`} {
		if s, m := e.schreibe755(t, "/karten/1/uebernehmen", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, s, m)
		}
	}
}

// Hält ein Mensch die Karte, nennt die Ablehnung ihn mit Namen, die Adresse
// nur einer owner-Person (wie board_verlauf).
func TestAgent755_UebernehmenMenschAlsHalter(t *testing.T) {
	e := newEnv671(t, true)
	annaB1, annaB2 := e.anna755(t)
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-bert', 'bert@firma.de', 'Bert', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	claim := `"claim":{"by":"bert@firma.de","session":"tab","since":"2026-10-08T10:00:00.000Z","until":"2099-01-01T00:00:00.000Z"},`
	for _, board := range []string{"b1", "b2"} {
		e.setzeStand755(t, board, ersetze755(t, board755(board, "X"), `"owner":"Anna",`, `"owner":"Anna",`+claim))
	}
	if s, b := e.rufe(t, annaB1, "", "POST", "/werkzeuge/boards/b1/karten/1/uebernehmen", `{"session":"s"}`); s != http.StatusConflict ||
		!strings.Contains(string(b), "Bert") || strings.Contains(string(b), "bert@") {
		t.Errorf("Halter für Nicht-owner: %d %s", s, b)
	}
	if s, b := e.rufe(t, annaB2, "", "POST", "/werkzeuge/boards/b2/karten/1/uebernehmen", `{"session":"s"}`); s != http.StatusConflict ||
		!strings.Contains(string(b), "bert@firma.de") {
		t.Errorf("Halter für owner: %d %s", s, b)
	}
}

func TestAgent755_TerminAendern(t *testing.T) {
	e := newEnv671(t, true)
	e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"erstellt":"2026-10-01"}`, `"erstellt":"2026-10-01","erstelltVon":"anna@firma.de","personen":["Anna"]}`))
	termin := func() map[string]any {
		for _, x := range e.brett755(t, "b1")["termine"].([]any) {
			if tm := x.(map[string]any); tm["id"] == "t-1" {
				return tm
			}
		}
		t.Fatal("Termin weg")
		return nil
	}
	if s, m := e.schreibe755(t, "/termine/t-1", `{"ort":"Raum 1","von":"10:00","bis":"11:00"}`); s != http.StatusOK || m["ort"] != "Raum 1" {
		t.Fatalf("ändern: %d %v", s, m)
	}
	tm := termin()
	if tm["ort"] != "Raum 1" || tm["von"] != "10:00" || tm["titel"] != "Termin" || tm["erstellt"] != "2026-10-01" ||
		tm["erstelltVon"] != "anna@firma.de" || jsonText(tm["personen"]) != `["Anna"]` {
		t.Errorf("Termin: %v", tm)
	}
	if s, m := e.schreibe755(t, "/termine/t-1", `{"von":null,"ort":""}`); s != http.StatusOK {
		t.Fatalf("entfernen: %d %v", s, m)
	}
	if tm := termin(); tm["von"] != nil || tm["ort"] != nil || tm["bis"] != "11:00" {
		t.Errorf("nach dem Entfernen: %v", tm)
	}
	vorher := e.stand(t, "b1")
	for _, body := range []string{`{"titel":""}`, `{"datum":"morgen"}`, `{"von":"12:00"}`, `{}`, `{"farbe":"rot"}`} {
		if s, m := e.schreibe755(t, "/termine/t-1", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, s, m)
		}
	}
	if s, _ := e.schreibe755(t, "/termine/t-2", `{"ort":"x"}`); s != http.StatusNotFound {
		t.Errorf("unbekannter Termin: %d", s)
	}
	if e.stand(t, "b1") != vorher {
		t.Error("abgelehnte Änderung geschrieben")
	}
}

// lanes755: Titel und Art der Swimlanes eines Boards.
func (e *env671) lanes755(t *testing.T) map[string]map[string]any {
	aus := map[string]map[string]any{}
	for _, l := range e.brett755(t, "b1")["sprints"].([]any) {
		lm := l.(map[string]any)
		aus[lm["id"].(string)] = lm
	}
	return aus
}

func TestAgent755_Swimlanes(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	s, m := e.schreibe755(t, "/swimlanes", `{"titel":"Thema X"}`)
	if s != http.StatusOK || m["art"] != "lane" {
		t.Fatalf("anlegen: %d %v", s, m)
	}
	id := m["swimlaneId"].(string)
	if l := e.lanes755(t)[id]; l == nil || l["title"] != "Thema X" || l["kind"] != "lane" {
		t.Errorf("Swimlane: %v", l)
	}
	// Wiederholung: dieselbe Swimlane, keine zweite.
	if s, m := e.schreibe755(t, "/swimlanes", `{"titel":"Thema X"}`); s != http.StatusOK || m["wiederholt"] != true || m["swimlaneId"] != id {
		t.Errorf("Wiederholung: %d %v", s, m)
	}
	if n := len(e.lanes755(t)); n != 4 {
		t.Errorf("%d Swimlanes, erwartet 4", n)
	}
	for _, body := range []string{`{"titel":"sprint 1"}`, `{"titel":"Altlasten"}`, `{"titel":"Neu","art":"backlog"}`, `{"titel":""}`} {
		if s, m := e.schreibe755(t, "/swimlanes", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, s, m)
		}
	}
	if s, m := e.schreibe755(t, "/swimlanes", `{"titel":"Sprint 2 — Neu","art":"sprint"}`); s != http.StatusOK || m["art"] != "sprint" {
		t.Errorf("Sprint: %d %v", s, m)
	}
	// Ändern: umbenennen, archivieren, zurückholen; Karten bleiben.
	if s, m := e.schreibe755(t, "/swimlanes/Sprint%201", `{"titel":"Sprint 1 — Umbenannt","archiviert":true}`); s != http.StatusOK {
		t.Fatalf("ändern: %d %v", s, m)
	}
	if l := e.lanes755(t)["s1"]; l["title"] != "Sprint 1 — Umbenannt" || l["archived"] != true || l["kind"] != "sprint" || len(l["cards"].([]any)) != 3 {
		t.Errorf("s1: %v", l)
	}
	if s, m := e.schreibe755(t, "/swimlanes/alt", `{"archiviert":false,"art":"sprint"}`); s != http.StatusOK || e.lanes755(t)["alt"]["archived"] != false {
		t.Errorf("zurückholen: %d %v", s, m)
	}
	for pfad, soll := range map[string]int{"/swimlanes/__backlog__": 404, "/swimlanes/Backlog": 404, "/swimlanes/gibt-es-nicht": 404} {
		if s, m := e.schreibe755(t, pfad, `{"titel":"x"}`); s != soll {
			t.Errorf("%s: %d %v", pfad, s, m)
		}
	}
	if s, m := e.schreibe755(t, "/swimlanes/s1", `{}`); s != http.StatusBadRequest {
		t.Errorf("ohne Änderung: %d %v", s, m)
	}
}

func TestAgent755_AufAntwortWarten(t *testing.T) {
	e := newEnv671(t, true)
	annaB1, _ := e.anna755(t)
	e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"nextDisplayId":10,`,
		`"nextDisplayId":10,"awaitingUser":{"Anna@Firma.de":{"question":"alt","since":"2026-10-01T00:00:00.000Z","askedBy":"x"},"bert@firma.de":{"question":"b","since":"2026-10-01T00:00:00.000Z","askedBy":"x"}},`))
	warte := func(body string) (int, string) {
		s, b := e.rufe(t, annaB1, "", "POST", "/werkzeuge/boards/b1/warten", body)
		return s, string(b)
	}
	if s, b := warte(`{"frage":"Welche Farbe?"}`); s != http.StatusOK {
		t.Fatalf("setzen: %d %s", s, b)
	}
	// Der vorhandene Schlüssel der Person wird genommen, kein zweiter.
	a := e.brett755(t, "b1")["awaitingUser"].(map[string]any)
	eintrag, _ := a["Anna@Firma.de"].(map[string]any)
	if len(a) != 2 || eintrag["question"] != "Welche Farbe?" || eintrag["askedBy"] != "agent:anna-b1" {
		t.Errorf("awaitingUser: %v", a)
	}
	if s, b := warte(`{"frage":null}`); s != http.StatusOK {
		t.Fatalf("entfernen: %d %s", s, b)
	}
	a = e.brett755(t, "b1")["awaitingUser"].(map[string]any)
	if _, da := a["Anna@Firma.de"]; da || a["bert@firma.de"] == nil {
		t.Errorf("nach dem Entfernen: %v", a)
	}
	if s, b := warte(`{"frage":""}`); s != http.StatusOK || !strings.Contains(b, "keinen Eintrag") {
		t.Errorf("nichts zu entfernen: %d %s", s, b)
	}
	if s, b := warte(`{}`); s != http.StatusBadRequest {
		t.Errorf("ohne frage: %d %s", s, b)
	}
	if s, _ := e.schreibe755(t, "/warten", `{"frage":"x"}`); s != http.StatusForbidden {
		t.Errorf("Projekt-Token: %d", s)
	}
}

func TestAgent755_KarteAnlegenErweitert(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	s, m := e.schreibe755(t, "/karten", `{"titel":"Mit allem","spalte":"Ready","swimlane":"Sprint 1","owner":"Bert","labels":["a","b"],"faellig":"2026-12-01","faelligUhrzeit":"14:00"}`)
	if s != http.StatusOK || m["spalte"] != "Ready" || m["swimlane"] != "Sprint 1" {
		t.Fatalf("anlegen: %d %v", s, m)
	}
	k := e.karte755(t, "b1", int(m["displayId"].(float64)))
	for feld, soll := range map[string]any{"columnId": "col-ready", "owner": "Bert", "dueDate": "2026-12-01", "dueTime": "14:00"} {
		if k[feld] != soll {
			t.Errorf("%s = %v", feld, k[feld])
		}
	}
	if jsonText(k["labels"]) != `["a","b"]` {
		t.Errorf("labels %v", k["labels"])
	}
	for _, body := range []string{`{"titel":"x","faelligUhrzeit":"14:00"}`, `{"titel":"x","spalte":"Gibt es nicht"}`, `{"titel":"x","labels":"a"}`,
		`{"titel":"x","faellig":"01.12.2026"}`} {
		if s, m := e.schreibe755(t, "/karten", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %v", body, s, m)
		}
	}
}

// karte_zeigen nennt, was die Schreib-Werkzeuge brauchen; den Agent-Kontext
// nie einem delegierenden Token (Open WebUI).
func TestAgent755_KarteZeigenNeueFelder(t *testing.T) {
	e := newEnv671(t, true)
	e.setzeStand755(t, "b1", ersetze755(t, board755("b1", "Marketing"), `"owner":"Anna",`, `"owner":"Anna","agentContext":"Geheimer Plan","priority":"hoch","effort":3,"critical":true,`))
	_, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1?format=json", "")
	ansicht := json671(t, b)
	if ansicht["agentKontext"] != "Geheimer Plan" || ansicht["prioritaet"] != "hoch" || ansicht["aufwand"] != 3.0 || ansicht["kritisch"] != true {
		t.Errorf("Felder: %v", ansicht)
	}
	if v := jsonText(ansicht["verweise"]); v != `[{"id":"k5","karte":5,"titel":"Im Sprint","typ":"related"}]` {
		t.Errorf("verweise: %s", v)
	}
	if !strings.Contains(string(b), `"id":"k-projekt"`) {
		t.Errorf("Kommentar-ids fehlen: %.300s", b)
	}
	for _, format := range []string{"json", "html"} {
		_, b = e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1/karten/1?format="+format, "")
		if strings.Contains(string(b), "Geheimer Plan") {
			t.Errorf("Agent-Kontext an Open WebUI (%s)", format)
		}
	}
}

// Der Wiederholungsschutz gehört dem Token, nicht nur der Person: Dieselbe
// Karte über Open WebUI und danach über ein Personen-Token ist kein Doppel,
// sondern ein zweiter Wunsch — vorher bekam der zweite Aufruf dieselbe Op-id
// und, war die erste Karte gelöscht, eine 400 vom Ledger.
func TestAgent755_WiederholungsschutzJeToken(t *testing.T) {
	e := newEnv671(t, true)
	annaB1, _ := e.anna755(t)
	body := `{"titel":"Gleich"}`
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", body); s != http.StatusOK {
		t.Fatalf("Open WebUI: %d %s", s, b)
	}
	// Karte des ersten Aufrufs weg (Board zurückgesetzt).
	e.zuruecksetzen755(t)
	s, b := e.rufe(t, annaB1, "", "POST", "/werkzeuge/boards/b1/karten", body)
	if s != http.StatusOK || strings.Contains(string(b), "bereits") {
		t.Fatalf("Personen-Token nach Löschen: %d %s", s, b)
	}
	// Steht die erste Karte noch, legt das zweite Token trotzdem seine an.
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"Zweimal"}`); s != http.StatusOK {
		t.Fatalf("%d %s", s, b)
	}
	if s, b := e.rufe(t, annaB1, "", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"Zweimal"}`); s != http.StatusOK || strings.Contains(string(b), "bereits") {
		t.Errorf("zweites Token, erste Karte da: %d %s", s, b)
	}
	if n := strings.Count(e.stand(t, "b1"), `"title":"Zweimal"`); n != 2 {
		t.Errorf("%d Karten „Zweimal“, erwartet 2", n)
	}
}

// Derselbe Aufruf über MCP wirkt wie über OpenAPI (ADR-0139 §6.5) — hier mit
// einer Liste im Body.
func TestAgent755_MCPGleicheWirkung(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	_, m := e.mcpWerkzeug(t, e.projekt, "", "karte_aendern", `{"boardId":"b1","karte":"1","labels":["über MCP"],"aufwand":4}`)
	if fehler, text, _ := ergebnis753(t, m); fehler {
		t.Fatalf("MCP: %s", text)
	}
	k := e.karte755(t, "b1", 1)
	if jsonText(k["labels"]) != `["über MCP"]` || k["effort"] != 4.0 {
		t.Errorf("Karte: %v %v", k["labels"], k["effort"])
	}
}
