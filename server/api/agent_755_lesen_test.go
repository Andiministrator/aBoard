package api_test

// Karte #755 (ADR-0139 Stufe 4a, Teil 2, Schritt 1): Inhaltstests der
// Lese-Werkzeuge aus Teil 1 — mitglieder (Adressen nur für eine owner-Person),
// karten_suchen (Fundort, Erledigt/Archiv, erlaubte Boards, Mail-Eingang) und
// anhang_lesen (Inhalt nur für kleine, gültige Text-Anhänge).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// anna755 legt Anna als Konto an und gibt ihre Personen-Tokens zurück: b1
// (dort editor) und b2 (dort owner).
func (e *env671) anna755(t *testing.T) (b1, b2 string) {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna', 'anna@firma.de', 'Anna', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	for i, board := range []string{"b1", "b2"} {
		k, _, err := e.repo.Anlegen(context.Background(), storage.AgentTokenAnlage{Name: "anna-" + board, BoardID: board, UserID: "u-anna",
			Role: storage.AgentRolleEditor, CreatedBy: "cli"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			b1 = k
		} else {
			b2 = k
		}
	}
	return b1, b2
}

// setzeStand755 ersetzt den Stand eines Boards (Test-Ausgangslage).
func (e *env671) setzeStand755(t *testing.T, board, stand string) {
	t.Helper()
	if !json.Valid([]byte(stand)) {
		t.Fatalf("Stand ist kein JSON: %.200s", stand)
	}
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, stand, board); err != nil {
		t.Fatal(err)
	}
}

// ersetze755: ersetzt einen eindeutigen Anker im Stand.
func ersetze755(t *testing.T, s, alt, neu string) string {
	t.Helper()
	if strings.Count(s, alt) != 1 {
		t.Fatalf("Anker nicht eindeutig (%d): %s", strings.Count(s, alt), alt)
	}
	return strings.Replace(s, alt, neu, 1)
}

func TestAgent755_Mitglieder(t *testing.T) {
	e := newEnv671(t, true)
	annaB1, annaB2 := e.anna755(t)
	ctx := context.Background()
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-bert', 'bert@firma.de', 'Bert', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	for _, x := range [][3]string{{"b2", "bert@firma.de", "editor"}, {"b2", "@firma.de", "viewer"}} {
		if err := e.acl.Set(ctx, x[0], x[1], x[2]); err != nil {
			t.Fatal(err)
		}
	}
	// Projekt-Token und Person ohne owner: Namen, keine Adressen, keine Domain.
	for _, fall := range []struct{ token, board string }{{e.projekt, "b1"}, {annaB1, "b1"}} {
		s, b := e.rufe(t, fall.token, "", "GET", "/werkzeuge/boards/"+fall.board+"/mitglieder", "")
		text := string(b)
		if s != http.StatusOK || strings.Contains(text, "@") || !strings.Contains(text, `"name":"Anna"`) || !strings.Contains(text, `"hinweis"`) {
			t.Errorf("ohne owner: %d %s", s, b)
		}
		// cora hat kein Konto: kein Name, und nie der Teil vor dem @.
		if !strings.Contains(text, `"name":"(Person)"`) || strings.Contains(text, "cora") {
			t.Errorf("Person ohne Konto: %s", b)
		}
	}
	// owner-Person: Adressen und Domain-Freigabe.
	s, b := e.rufe(t, annaB2, "", "GET", "/werkzeuge/boards/b2/mitglieder", "")
	text := string(b)
	for _, soll := range []string{`"email":"anna@firma.de"`, `"email":"bert@firma.de"`, `"name":"alle mit @firma.de"`, `"rolle":"owner"`} {
		if s != http.StatusOK || !strings.Contains(text, soll) {
			t.Errorf("owner-Person: %s fehlt — %d %s", soll, s, b)
		}
	}
	if strings.Contains(text, `"hinweis"`) {
		t.Errorf("owner-Person bekommt den Hinweis: %s", b)
	}
}

func TestAgent755_KartenSuchen(t *testing.T) {
	e := newEnv671(t, true)
	annaB1, _ := e.anna755(t)
	mehrEditor, mehrViewer := mehrereTokens754(t, e.repo)
	stand := board671("b1", "Marketing")
	stand = ersetze755(t, stand, `"title":"Im Sprint","columnId":"col-in-arbeit","comments":[]}`,
		`"title":"Im Sprint","description":"Das Zauberwort steht hier","labels":["Wichtig"],"columnId":"col-in-arbeit","comments":[]}`)
	// Eine Swimlane des Mail-Eingangs auf demselben Board: für Werkzeuge unsichtbar.
	stand = ersetze755(t, stand, `{"id":"alt",`, `{"id":"mail","title":"Eingang","kind":"service","cards":[
		{"id":"km","displayId":8,"title":"Offene Karte aus der Mail","columnId":"col-backlog","comments":[]}]},{"id":"alt",`)
	e.setzeStand755(t, "b1", stand)

	suche := func(token, abfrage string) (int, map[string]any) {
		s, b := e.rufe(t, token, "", "GET", "/werkzeuge/karten?"+abfrage, "")
		if s != http.StatusOK {
			return s, nil
		}
		return s, json671(t, b)
	}
	treffer := func(m map[string]any) []string {
		var aus []string
		l, _ := m["treffer"].([]any)
		for _, x := range l {
			h := x.(map[string]any)
			aus = append(aus, h["boardId"].(string)+"#"+strings.TrimSuffix(strings.TrimSuffix(jsonText(h["displayId"]), ".0"), "")+":"+h["fundort"].(string))
		}
		return aus
	}
	// Standard: ohne Erledigt, Archiv, Mail-Eingang, Sprint-Meta.
	if _, m := suche(e.projekt, "q=Karte"); strings.Join(treffer(m), ",") != "b1#1:titel" {
		t.Errorf("Standard: %v", treffer(m))
	}
	if _, m := suche(e.projekt, "q=karte&mitErledigten=true"); strings.Join(treffer(m), ",") != "b1#1:titel,b1#2:titel" {
		t.Errorf("mitErledigten: %v", treffer(m))
	}
	if _, m := suche(e.projekt, "q=Karte&mitErledigten=true&mitArchivierten=true"); strings.Join(treffer(m), ",") != "b1#1:titel,b1#2:titel,b1#3:titel" {
		t.Errorf("mitArchivierten: %v", treffer(m))
	}
	if _, m := suche(e.projekt, "q=versteckt&mitArchivierten=true"); strings.Join(treffer(m), ",") != "b1#7:titel" {
		t.Errorf("archivierte Swimlane: %v", treffer(m))
	}
	for abfrage, soll := range map[string]string{"q=zauberwort": "b1#5:beschreibung", "q=anna": "b1#1:owner", "q=wichtig": "b1#5:label",
		"q=Sprint-Start": "", "q=Mail": ""} {
		if _, m := suche(e.projekt, abfrage); strings.Join(treffer(m), ",") != soll {
			t.Errorf("%s: %v, erwartet %q", abfrage, treffer(m), soll)
		}
	}
	// Erlaubte Boards: boardId außerhalb → 403; zu kurzer Begriff → 400.
	if s, _ := suche(e.projekt, "q=Karte&boardId=b3"); s != http.StatusForbidden {
		t.Errorf("fremdes Board: %d", s)
	}
	if s, _ := suche(e.projekt, "q=K"); s != http.StatusBadRequest {
		t.Errorf("ein Zeichen: %d", s)
	}
	// Personen-Token eines Boards: nur dieses, auch wenn die Person anderswo owner ist.
	if _, m := suche(annaB1, "q=Offene"); strings.Join(treffer(m), ",") != "b1#1:titel" {
		t.Errorf("Personen-Token: %v", treffer(m))
	}
	// Mehr-Board-Tokens: die eigene Liste, sonst nichts.
	if _, m := suche(mehrEditor, "q=Offene"); strings.Join(treffer(m), ",") != "b1#1:titel" {
		t.Errorf("Mehr-Board (b1): %v", treffer(m))
	}
	if _, m := suche(mehrViewer, "q=Offene"); strings.Join(treffer(m), ",") != "b1#1:titel,b2#1:titel" {
		t.Errorf("Mehr-Board (b1, b2): %v", treffer(m))
	}
	if s, _ := suche(mehrEditor, "q=Offene&boardId=b2"); s != http.StatusForbidden {
		t.Errorf("Mehr-Board, Board außerhalb der Liste: %d", s)
	}
}

// jsonText: Zahl aus einer JSON-Antwort als Text („1“, nicht „1.0“).
func jsonText(v any) string {
	roh, _ := json.Marshal(v)
	return string(roh)
}

func TestAgent755_AnhangInhalt(t *testing.T) {
	e := newEnv671(t, true)
	gross := strings.Repeat("x", 33<<10)
	for id, inhalt := range map[string]string{"a-txt": "Hallo Welt", "a-gross": gross, "a-kaputt": "a\xffb", "a-pdf": "%PDF"} {
		if err := e.anhaenge.Save("b1", "k1", id, id+".txt", strings.NewReader(inhalt)); err != nil {
			t.Fatal(err)
		}
	}
	stand := ersetze755(t, board671("b1", "Marketing"), `"title":"Offene Karte",`, `"title":"Offene Karte","attachments":[
		{"id":"a-txt","filename":"notiz.md","mime":"text/markdown","size":10},
		{"id":"a-gross","filename":"gross.txt","mime":"text/plain","size":33792},
		{"id":"a-kaputt","filename":"kaputt.txt","mime":"text/plain; charset=utf-8","size":3},
		{"id":"a-pdf","filename":"x.pdf","mime":"application/pdf","size":4},
		{"id":"a-fehlt","filename":"weg.txt","mime":"text/plain","size":3}],`)
	e.setzeStand755(t, "b1", stand)
	lies := func(anhang string) (int, map[string]any) {
		s, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1/anhaenge?anhang="+anhang, "")
		if s != http.StatusOK {
			return s, nil
		}
		return s, json671(t, b)
	}
	if _, m := lies("a-txt"); m["inhalt"] != "Hallo Welt" {
		t.Errorf("Text: %v", m)
	}
	for id, hinweis := range map[string]string{"a-gross": "Zu groß", "a-kaputt": "UTF-8", "a-pdf": "Kein Text-Anhang", "a-fehlt": "nicht lesbar"} {
		s, m := lies(id)
		if _, da := m["inhalt"]; s != http.StatusOK || da || !strings.Contains(jsonText(m["hinweis"]), hinweis) {
			t.Errorf("%s: %d %v", id, s, m)
		}
	}
	if s, _ := lies("gibt-es-nicht"); s != http.StatusNotFound {
		t.Errorf("unbekannter Anhang: %d", s)
	}
}
