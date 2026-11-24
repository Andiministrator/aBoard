package api_test

// Karte #757 (ADR-0139 Stufe 4b): Löschen zweistufig mit Bestätigungscode,
// board_aendern für owner, board.field-Sperre auf dem rohen Weg. Die
// Rollen-Matrix steht in agent_752_test.go (über MCP agent_753_test.go); hier
// die Fälle aus ADR-0139 §6.3 und die Wirkung.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// nurMitPerson757: Werkzeuge der Board-Struktur — tools/list zeigt sie nur
// Tokens mit Person (Projekt-Tokens haben keine).
var nurMitPerson757 = map[string]bool{"spalte_loeschen_pruefen": true, "spalte_loeschen": true, "board_aendern": true}

// mitCode757: Steht im Body „CODE“, holt es den Code über das Prüf-Werkzeug
// (GET auf denselben Pfad) mit demselben Zugang. Ohne Code (Prüfen
// abgelehnt): ein ungültiger — der Aufruf soll dann ohnehin scheitern.
func (e *env671) mitCode757(t *testing.T, token, person, pfad, board, agent, body string) string {
	t.Helper()
	if !strings.Contains(body, "CODE") {
		return body
	}
	code := "lc1-ungueltig"
	if s, b := e.rufe(t, token, person, "GET", pfad755(pfad, board, agent), ""); s == http.StatusOK {
		code, _ = json671(t, b)["code"].(string)
	}
	return strings.ReplaceAll(body, "CODE", code)
}

// pruefe757: GET …/loeschen → Code (Fatal, wenn es nicht 200 ist).
func (e *env671) pruefe757(t *testing.T, token, pfad string) (string, map[string]any) {
	t.Helper()
	s, b := e.rufe(t, token, "", "GET", pfad, "")
	if s != http.StatusOK {
		t.Fatalf("prüfen %s: %d %s", pfad, s, b)
	}
	m := json671(t, b)
	code, _ := m["code"].(string)
	if !strings.HasPrefix(code, "lc1-") {
		t.Fatalf("prüfen %s: kein Code: %s", pfad, b)
	}
	return code, m
}

func (e *env671) loesche757(t *testing.T, token, pfad, code string) (int, map[string]any) {
	t.Helper()
	s, b := e.rufe(t, token, "", "POST", pfad, `{"code":"`+code+`"}`)
	return s, json671(t, b)
}

// personOwner757: Personen-Token von Anna auf b2 (dort owner) und ein
// Mehr-Board-Token über b1 (editor) und b2 (owner).
func (e *env671) personOwner757(t *testing.T) (owner, mehrere string) {
	t.Helper()
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna', 'anna@firma.de', 'Anna', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	owner, _, err := e.repo.Anlegen(t.Context(), storage.AgentTokenAnlage{Name: "anna-b2", BoardID: "b2", UserID: "u-anna", Role: storage.AgentRolleEditor, CreatedBy: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	mehrere, _, err = e.repo.Anlegen(t.Context(), storage.AgentTokenAnlage{Name: "anna-mehrere-0", UserID: "u-anna", Role: storage.AgentRolleEditor,
		CreatedBy: "cli", BoardIDs: []string{"b1", "b2"}})
	if err != nil {
		t.Fatal(err)
	}
	return owner, mehrere
}

const karte1Pfad757 = "/werkzeuge/boards/b1/karten/1/loeschen"

// Falscher, verfälschter, fremder Code (Objekt, Token, Board) → 403;
// abgelaufen → 410; geänderter Stand → 409; zweimal → 409.
func TestAgent757_Bestaetigungscode(t *testing.T) {
	e := newEnv671(t, true)
	_, mehrere := e.personOwner757(t)
	e.zuruecksetzen755(t)

	code, vorschau := e.pruefe757(t, e.projekt, karte1Pfad757)
	v, _ := vorschau["vorschau"].(map[string]any)
	if v["titel"] != "Offene Karte" || v["kommentare"].(float64) != float64(len(agentNamen755)) || v["anhaenge"].(float64) != 1 || v["rueckverweise"].(float64) != 1 {
		t.Errorf("Vorschau: %v", v)
	}
	if !strings.Contains(e.stand(t, "b1"), `"title":"Offene Karte"`) {
		t.Fatal("Prüfen hat gelöscht")
	}

	// Ohne Code, kein Code, verfälscht.
	for _, body := range []string{`{}`, `{"code":""}`, `{"code":"abc"}`} {
		if s, b := e.rufe(t, e.projekt, "", "POST", karte1Pfad757, body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, s, b)
		}
	}
	// Verfälscht im Zufallswert (vorn): Die Bindung passt nicht mehr → 403.
	verfaelscht := "lc1-" + map[bool]string{true: "AA", false: "BB"}[!strings.HasPrefix(code, "lc1-AA")] + code[6:]
	if s, m := e.loesche757(t, e.projekt, karte1Pfad757, verfaelscht); s != http.StatusForbidden {
		t.Errorf("verfälscht: %d %v", s, m)
	}
	// Fremdes Objekt: Code der Karte 1 für Karte 2.
	if s, m := e.loesche757(t, e.projekt, "/werkzeuge/boards/b1/karten/2/loeschen", code); s != http.StatusForbidden {
		t.Errorf("fremdes Objekt: %d %v", s, m)
	}
	// Fremde Objektart (andere id): Code der Karte k1 als Swimlane-Code. Gleiche
	// id in zwei Arten: TestAgent757_KritikerNacharbeit (K-5).
	if s, m := e.loesche757(t, e.projekt, "/werkzeuge/boards/b1/swimlanes/s1/loeschen", code); s != http.StatusForbidden {
		t.Errorf("fremde Art: %d %v", s, m)
	}
	// Fremdes Token, gleiches Board (Anna, Mehr-Board-Token).
	if s, m := e.loesche757(t, mehrere, karte1Pfad757, code); s != http.StatusForbidden {
		t.Errorf("fremdes Token: %d %v", s, m)
	}
	// Anderes Board mit gleicher Objekt-ID (Verify W-1): Annas Code für k1 auf
	// b1 passt nicht auf k1 auf b2 — gleiches Token, gleiche Karte, gleicher Inhalt.
	codeB1, _ := e.pruefe757(t, mehrere, karte1Pfad757)
	if s, m := e.loesche757(t, mehrere, "/werkzeuge/boards/b2/karten/1/loeschen", codeB1); s != http.StatusForbidden {
		t.Errorf("anderes Board: %d %v", s, m)
	}
	if !strings.Contains(e.stand(t, "b2"), `"title":"Offene Karte"`) {
		t.Fatal("anderes Board: Karte auf b2 gelöscht")
	}

	// Geänderter Stand: ein neuer Kommentar macht den Code ungültig.
	if s, b := e.rufe(t, e.projekt, "", "POST", "/werkzeuge/boards/b1/karten/1/kommentare", `{"text":"noch etwas"}`); s != http.StatusOK {
		t.Fatalf("kommentieren: %d %s", s, b)
	}
	if s, m := e.loesche757(t, e.projekt, karte1Pfad757, code); s != http.StatusConflict || !strings.Contains(m["error"].(string), "geändert") {
		t.Errorf("geänderter Stand: %d %v", s, m)
	}

	// Abgelaufen.
	e.zuruecksetzen755(t)
	code, _ = e.pruefe757(t, e.projekt, karte1Pfad757)
	zurueck := api.SetzeLoeschCodeUhrFuerTest(func() time.Time { return time.Now().Add(11 * time.Minute) })
	s, m := e.loesche757(t, e.projekt, karte1Pfad757, code)
	zurueck()
	if s != http.StatusGone {
		t.Errorf("abgelaufen: %d %v", s, m)
	}

	// Gültig → gelöscht; derselbe Code nach dem Wiederanlegen (gleiche id,
	// gleicher Inhalt) → schon benutzt.
	if s, m := e.loesche757(t, e.projekt, karte1Pfad757, code); s != http.StatusOK || m["ergebnis"] != "gelöscht" {
		t.Fatalf("gültig: %d %v", s, m)
	}
	if strings.Contains(e.stand(t, "b1"), `"id":"k1"`) {
		t.Fatal("Karte k1 noch da")
	}
	e.zuruecksetzen755(t)
	if s, m := e.loesche757(t, e.projekt, karte1Pfad757, code); s != http.StatusConflict || !strings.Contains(m["error"].(string), "schon benutzt") {
		t.Errorf("nach Wiederanlegen: %d %v", s, m)
	}
	if !strings.Contains(e.stand(t, "b1"), `"id":"k1"`) {
		t.Fatal("nach Wiederanlegen gelöscht")
	}
}

// Zeitlücke Stand-Prüfung ↔ Op (ADR-0139 E-2): Ändert jemand die Karte
// zwischen Code-Prüfung und Schreiben, bemerkt es die Vorbedingung unter der
// Sperre — nichts gelöscht, und der Code ist nicht verbraucht.
func TestAgent757_Zeitluecke(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	code, _ := e.pruefe757(t, e.projekt, karte1Pfad757)
	geaendert := false
	zurueck := api.SetzeVorDemSchreibenFuerTest(func() {
		if geaendert {
			return
		}
		geaendert = true
		stand := strings.Replace(e.stand(t, "b1"), `"title":"Offene Karte"`, `"title":"Offene Karte (geändert)"`, 1)
		if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b1'`, stand); err != nil {
			t.Fatal(err)
		}
	})
	s, m := e.loesche757(t, e.projekt, karte1Pfad757, code)
	zurueck()
	if s != http.StatusConflict || !strings.Contains(m["error"].(string), "gerade eben geändert") {
		t.Fatalf("Zeitlücke: %d %v", s, m)
	}
	if !strings.Contains(e.stand(t, "b1"), `"id":"k1"`) {
		t.Fatal("Zeitlücke: trotzdem gelöscht")
	}
	// Code nicht verbraucht: zurück auf den bestätigten Stand → gilt wieder.
	e.zuruecksetzen755(t)
	if s, m := e.loesche757(t, e.projekt, karte1Pfad757, code); s != http.StatusOK {
		t.Errorf("Code nach abgelehntem Schreiben: %d %v", s, m)
	}
}

// Wirkung: Karte samt Dateien und Rückverweisen; Meta-Karten nie; Swimlane
// mit Karten in den Backlog; Termin; Anhang (Karte und Board).
func TestAgent757_Wirkung(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	if err := e.anhaenge.Save("b1", "k1", "a-1", "notiz.txt", strings.NewReader("Text")); err != nil {
		t.Fatal(err)
	}
	code, _ := e.pruefe757(t, e.projekt, karte1Pfad757)
	if s, m := e.loesche757(t, e.projekt, karte1Pfad757, code); s != http.StatusOK {
		t.Fatalf("Karte: %d %v", s, m)
	}
	stand := e.stand(t, "b1")
	if strings.Contains(stand, `"targetId":"k1"`) {
		t.Error("Rückverweis auf die gelöschte Karte bleibt stehen")
	}
	if f, _, err := e.anhaenge.Open("b1", "k1", "a-1"); err == nil {
		_ = f.Close()
		t.Error("Datei des Anhangs nicht entfernt")
	}

	// Meta-Karte (#4): nicht löschbar, schon beim Prüfen.
	if s, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/4/loeschen", ""); s != http.StatusForbidden {
		t.Errorf("Meta-Karte: %d %s", s, b)
	}
	// Allgemeiner Backlog: nicht löschbar.
	if s, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/swimlanes/__backlog__/loeschen", ""); s != http.StatusForbidden {
		t.Errorf("Backlog: %d %s", s, b)
	}

	// Swimlane s1: Karten wandern in den Backlog, die Meta-Karte fällt weg.
	e.zuruecksetzen755(t)
	code, v := e.pruefe757(t, e.projekt, "/werkzeuge/boards/b1/swimlanes/Sprint%201/loeschen")
	if vs := v["vorschau"].(map[string]any); vs["karten"].(float64) != 2 || vs["sprintKarten"].(float64) != 1 {
		t.Errorf("Swimlane-Vorschau: %v", vs)
	}
	if s, m := e.loesche757(t, e.projekt, "/werkzeuge/boards/b1/swimlanes/s1/loeschen", code); s != http.StatusOK {
		t.Fatalf("Swimlane: %d %v", s, m)
	}
	stand = e.stand(t, "b1")
	if strings.Contains(stand, `"id":"s1"`) || !strings.Contains(stand, `"id":"k5"`) || strings.Contains(stand, `"id":"m1"`) {
		t.Errorf("Swimlane gelöscht, Karten im Backlog, Meta-Karte weg — Stand: %.300s", stand)
	}

	// Termin.
	e.zuruecksetzen755(t)
	code, _ = e.pruefe757(t, e.projekt, "/werkzeuge/boards/b1/termine/t-1/loeschen")
	if s, m := e.loesche757(t, e.projekt, "/werkzeuge/boards/b1/termine/t-1/loeschen", code); s != http.StatusOK {
		t.Fatalf("Termin: %d %v", s, m)
	}
	if strings.Contains(e.stand(t, "b1"), `"id":"t-1"`) {
		t.Error("Termin noch da")
	}

	// Anhang an einer Karte: Metadaten und Datei weg, Karte bleibt.
	e.zuruecksetzen755(t)
	if err := e.anhaenge.Save("b1", "k1", "a-1", "notiz.txt", strings.NewReader("Text")); err != nil {
		t.Fatal(err)
	}
	code, _ = e.pruefe757(t, e.projekt, "/werkzeuge/boards/b1/anhaenge/a-1/loeschen")
	if s, m := e.loesche757(t, e.projekt, "/werkzeuge/boards/b1/anhaenge/a-1/loeschen", code); s != http.StatusOK {
		t.Fatalf("Anhang: %d %v", s, m)
	}
	stand = e.stand(t, "b1")
	if strings.Contains(stand, `"id":"a-1"`) || !strings.Contains(stand, `"id":"k1"`) {
		t.Errorf("Anhang: Stand %.300s", stand)
	}
	if f, _, err := e.anhaenge.Open("b1", "k1", "a-1"); err == nil {
		_ = f.Close()
		t.Error("Datei des Anhangs nicht entfernt")
	}

	// Anhang eines Kommentars und das Hintergrundbild: nicht.
	e.zuruecksetzen755(t)
	stand = strings.Replace(e.stand(t, "b1"), `"id":"a-1","filename"`, `"id":"a-1","commentId":"k-projekt","filename"`, 1)
	stand = strings.Replace(stand, `"nextDisplayId":10,`, `"nextDisplayId":10,"attachments":[{"id":"bg-1","filename":"bild.png","mime":"image/png","size":4}],"background":{"attachmentId":"bg-1"},`, 1)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b1'`, stand); err != nil {
		t.Fatal(err)
	}
	if s, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/anhaenge/a-1/loeschen", ""); s != http.StatusForbidden {
		t.Errorf("Kommentar-Anhang: %d %s", s, b)
	}
	if s, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/anhaenge/bg-1/loeschen", ""); s != http.StatusConflict {
		t.Errorf("Hintergrundbild: %d %s", s, b)
	}
}

// Spalte: nur owner, nur leer, nie die letzte; board_aendern nur owner.
func TestAgent757_SpalteUndBoardAendern(t *testing.T) {
	e := newEnv671(t, true)
	owner, mehrere := e.personOwner757(t)
	e.zuruecksetzen755(t)

	// Ohne owner (Anna ist auf b1 editor) → 403, schon beim Prüfen.
	if s, b := e.rufe(t, mehrere, "", "GET", "/werkzeuge/boards/b1/spalten/col-leer/loeschen", ""); s != http.StatusForbidden {
		t.Errorf("Spalte ohne owner: %d %s", s, b)
	}
	if s, b := e.rufe(t, mehrere, "", "POST", "/werkzeuge/boards/b1/aendern", `{"titel":"X"}`); s != http.StatusForbidden {
		t.Errorf("board_aendern ohne owner: %d %s", s, b)
	}
	// Belegte Spalte: Vorschau nennt die Karten, kein Code.
	s, b := e.rufe(t, owner, "", "GET", "/werkzeuge/boards/b2/spalten/Ready/loeschen", "")
	if m := json671(t, b); s != http.StatusConflict || m["code"] != nil || len(m["karten"].([]any)) != 3 {
		t.Errorf("belegte Spalte: %d %s", s, b)
	}
	// Leere Spalte → gelöscht.
	code, _ := e.pruefe757(t, owner, "/werkzeuge/boards/b2/spalten/Leer/loeschen")
	if s, m := e.loesche757(t, owner, "/werkzeuge/boards/b2/spalten/col-leer/loeschen", code); s != http.StatusOK {
		t.Fatalf("Spalte: %d %v", s, m)
	}
	if strings.Contains(e.stand(t, "b2"), `"col-leer"`) {
		t.Error("Spalte noch da")
	}

	// board_aendern: ohne Felder nur lesen.
	s, b = e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{}`)
	if m := json671(t, b); s != http.StatusOK || m["ergebnis"] != "nichts geändert" || m["einstellungen"] == nil {
		t.Fatalf("leer: %d %s", s, b)
	}
	// Spalten: umbenennen, ordnen, neu; eine fehlt → 400.
	s, b = e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"spalten":[{"id":"col-ready","titel":"Bereit"},{"id":"col-backlog","titel":"Backlog"}]}`)
	if s != http.StatusBadRequest || !strings.Contains(string(b), "spalte_loeschen") {
		t.Errorf("Spalte fehlt: %d %s", s, b)
	}
	s, b = e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"titel":"Technik neu","spalten":[{"id":"col-ready","titel":"Bereit"},{"id":"col-backlog","titel":"Backlog"},{"id":"col-in-arbeit","titel":"In Arbeit"},{"id":"col-erledigt","titel":"Erledigt"},{"titel":"Review"}],"vorlagen":[{"titel":"Fehler","labels":["bug"],"komplexitaet":"einfach","aufwand":1}]}`)
	if s != http.StatusOK {
		t.Fatalf("ändern: %d %s", s, b)
	}
	var stand struct {
		Board struct {
			Title      string `json:"title"`
			ColumnDefs []struct {
				ID, Title string
			} `json:"columnDefs"`
			Templates []map[string]any `json:"templates"`
		} `json:"board"`
	}
	if err := json.Unmarshal([]byte(e.stand(t, "b2")), &stand); err != nil {
		t.Fatal(err)
	}
	if stand.Board.Title != "Technik neu" || len(stand.Board.ColumnDefs) != 5 || stand.Board.ColumnDefs[0].ID != "col-ready" ||
		stand.Board.ColumnDefs[0].Title != "Bereit" || stand.Board.ColumnDefs[4].Title != "Review" || !strings.HasPrefix(stand.Board.ColumnDefs[4].ID, "col-") ||
		len(stand.Board.Templates) != 1 || stand.Board.Templates[0]["title"] != "Fehler" || stand.Board.Templates[0]["isTemplate"] != true {
		t.Errorf("Stand nach board_aendern: %+v", stand.Board)
	}
	// Hintergrund: nur ein Bild unter den Board-Anhängen.
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"hintergrund":{"anhang":"gibt-es-nicht"}}`); s != http.StatusBadRequest {
		t.Errorf("Hintergrund: %d %s", s, b)
	}

	// Zeitlücke: Ein Mensch ändert die Spalten zwischen Lesen und Schreiben →
	// 409, nichts geschrieben.
	zurueck := api.SetzeVorDemSchreibenFuerTest(func() {
		st := strings.Replace(e.stand(t, "b2"), `"title":"Bereit"`, `"title":"Bereit (Mensch)"`, 1)
		if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b2'`, st); err != nil {
			t.Fatal(err)
		}
	})
	s, b = e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"spalten":[{"id":"col-ready","titel":"Bereit 2"},{"id":"col-backlog","titel":"Backlog"},{"id":"col-in-arbeit","titel":"In Arbeit"},{"id":"col-erledigt","titel":"Erledigt"},{"id":"`+stand.Board.ColumnDefs[4].ID+`","titel":"Review"}]}`)
	zurueck()
	if s != http.StatusConflict || !strings.Contains(e.stand(t, "b2"), "Bereit (Mensch)") {
		t.Errorf("Zeitlücke board_aendern: %d %s", s, b)
	}
}

// board.field-Sperre (ADR-0139 E-3): Auf dem rohen Weg schreibt ein Agent
// Titel, Spalten, Hintergrund und Vorlagen nicht; die Werkzeuge trifft sie
// nicht (board_aendern oben); card.delete bleibt roh erlaubt (E-2).
func TestAgent757_RohSperre(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	for _, feld := range []string{"title", "columnDefs", "background", "templates"} {
		body := `{"ops":[{"type":"board.field","payload":{"field":"` + feld + `","value":null}}]}`
		if s, b := e.rufe(t, e.projekt, "", "POST", "/boards/b1/ops", body); s != http.StatusBadRequest || !strings.Contains(string(b), "board_aendern") {
			t.Errorf("roh %s: %d %s", feld, s, b)
		}
	}
	// attachments ist nicht gesperrt — ergänzen bleibt erlaubt, kürzen nicht
	// (Karte #766, agent_766_test.go); card.delete bleibt roh erlaubt (E-2).
	for _, body := range []string{
		`{"ops":[{"type":"board.field","payload":{"field":"attachments","value":[]}}]}`,
		`{"ops":[{"type":"card.delete","payload":{"cardId":"k2"}}]}`,
	} {
		if s, b := e.rufe(t, e.projekt, "", "POST", "/boards/b1/ops", body); s != http.StatusOK {
			t.Errorf("roh erlaubt: %d %s", s, b)
		}
	}
}

// tools/list: Prüf-Werkzeuge lesend, Löschen zerstörerisch; die Board-Struktur
// nur mit Person; ein Viewer sieht keins der Paare.
func TestAgent757_MCPListe(t *testing.T) {
	e := newEnv671(t, true)
	owner, _ := e.personOwner757(t)
	hinweise := func(token string) map[string]map[string]any {
		_, m := e.mcpRufe(t, token, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{`+meta753()+`}}`, nil)
		aus := map[string]map[string]any{}
		for _, x := range m["result"].(map[string]any)["tools"].([]any) {
			w := x.(map[string]any)
			aus[w["name"].(string)], _ = w["annotations"].(map[string]any)
		}
		return aus
	}
	ownerListe := hinweise(owner)
	for _, art := range []string{"karte", "swimlane", "termin", "anhang", "spalte"} {
		p, l := ownerListe[art+"_loeschen_pruefen"], ownerListe[art+"_loeschen"]
		if p == nil || l == nil || p["readOnlyHint"] != true || p["destructiveHint"] != false || l["destructiveHint"] != true || l["readOnlyHint"] != false {
			t.Errorf("%s: pruefen %v, loeschen %v", art, p, l)
		}
	}
	if h := ownerListe["board_aendern"]; h == nil || h["destructiveHint"] != true {
		t.Errorf("board_aendern: %v", h)
	}
	projekt := hinweise(e.projekt)
	for name := range nurMitPerson757 {
		if projekt[name] != nil {
			t.Errorf("Projekt-Token sieht %s", name)
		}
	}
	if projekt["karte_loeschen_pruefen"] == nil {
		t.Error("Projekt-Token sieht karte_loeschen_pruefen nicht")
	}
	for name := range hinweise(e.leser) {
		if strings.Contains(name, "loeschen") || name == "board_aendern" {
			t.Errorf("Viewer sieht %s", name)
		}
	}
}

// Kritiker #757 K-1: Die Vorbedingung prüfte nur das Objekt, geschrieben wird
// aber eine ganze Liste (columnDefs, attachments). Ändert ein Mensch zwischen
// Lesen und Sperre einen ANDEREN Eintrag derselben Liste, muss die Löschung
// scheitern statt seine Änderung mit der alten Liste zu überschreiben.
func TestAgent757_ZeitlueckeListe(t *testing.T) {
	e := newEnv671(t, true)
	owner, _ := e.personOwner757(t)
	for _, fall := range []struct{ name, token, board, pfad, alt, neu, erhalten string }{
		{"Spalte", owner, "b2", "/werkzeuge/boards/b2/spalten/col-leer/loeschen",
			`{"id":"col-erledigt","title":"Erledigt"}`, `{"id":"col-erledigt","title":"Erledigt (Mensch)"}`, "Erledigt (Mensch)"},
		{"Anhang", e.projekt, "b1", "/werkzeuge/boards/b1/anhaenge/a-1/loeschen",
			`"attachments":[{"id":"a-1"`, `"attachments":[{"id":"a-neu","filename":"neu.txt","mime":"text/plain","size":1},{"id":"a-1"`, `"a-neu"`},
	} {
		e.zuruecksetzen755(t)
		code, _ := e.pruefe757(t, fall.token, fall.pfad)
		einmal := false
		zurueck := api.SetzeVorDemSchreibenFuerTest(func() {
			if einmal {
				return
			}
			einmal = true
			st := e.stand(t, fall.board)
			if strings.Count(st, fall.alt) != 1 {
				t.Fatalf("%s: Anker %d×", fall.name, strings.Count(st, fall.alt))
			}
			if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, strings.Replace(st, fall.alt, fall.neu, 1), fall.board); err != nil {
				t.Fatal(err)
			}
		})
		s, m := e.loesche757(t, fall.token, fall.pfad, code)
		zurueck()
		if s != http.StatusConflict || !strings.Contains(e.stand(t, fall.board), fall.erhalten) {
			t.Errorf("%s: %d %v — Änderung des Menschen erhalten: %v", fall.name, s, m, strings.Contains(e.stand(t, fall.board), fall.erhalten))
		}
	}
}

// Kritiker #757 K-2 bis K-5, K-7.
func TestAgent757_KritikerNacharbeit(t *testing.T) {
	e := newEnv671(t, true)
	owner, _ := e.personOwner757(t)
	setze := func(board, alt, neu string) {
		t.Helper()
		st := e.stand(t, board)
		if strings.Count(st, alt) != 1 {
			t.Fatalf("Anker %q %d×", alt, strings.Count(st, alt))
		}
		if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, strings.Replace(st, alt, neu, 1), board); err != nil {
			t.Fatal(err)
		}
	}

	// K-2: Teilangabe ändert nur die genannten Felder; fehlende Vorlage → 400.
	e.zuruecksetzen755(t)
	setze("b2", `"nextDisplayId":10,`, `"nextDisplayId":10,"templates":[{"id":"v1","isTemplate":true,"title":"V","description":"lang","labels":["a"],"owner":"Anna","complexity":"hoch","effort":3},{"id":"v2","isTemplate":true,"title":"W"}],`)
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"vorlagen":[{"id":"v1","titel":"V neu"}]}`); s != http.StatusBadRequest || !strings.Contains(string(b), "v2") {
		t.Errorf("fehlende Vorlage: %d %s", s, b)
	}
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"vorlagen":[{"id":"v2","titel":"W"},{"id":"v1","titel":"V neu","labels":["b"]}]}`); s != http.StatusOK {
		t.Fatalf("Teilangabe: %d %s", s, b)
	}
	if st := e.stand(t, "b2"); !strings.Contains(st, `"description":"lang"`) || !strings.Contains(st, `"effort":3`) || !strings.Contains(st, `"owner":"Anna"`) ||
		!strings.Contains(st, `"labels":["b"]`) || !strings.Contains(st, `"title":"V neu"`) || strings.Index(st, `"id":"v2"`) > strings.Index(st, `"id":"v1"`) {
		t.Errorf("Teilangabe: Felder verloren oder Reihenfolge falsch: %.600s", st[strings.Index(st, `"templates"`):])
	}

	// K-4: Bildwechsel behält die Darstellung.
	e.zuruecksetzen755(t)
	setze("b2", `"nextDisplayId":10,`, `"nextDisplayId":10,"attachments":[{"id":"bg1","filename":"a.png","mime":"image/png","size":1},{"id":"bg2","filename":"b.png","mime":"image/png","size":1}],"background":{"attachmentId":"bg1","darstellung":"voll"},`)
	if s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"hintergrund":{"anhang":"bg2"}}`); s != http.StatusOK {
		t.Fatalf("Hintergrund: %d %s", s, b)
	}
	if st := e.stand(t, "b2"); !strings.Contains(st, `"background":{"attachmentId":"bg2","darstellung":"voll"}`) {
		t.Errorf("Darstellung verloren: %.400s", st[strings.Index(st, `"background"`):])
	}

	// K-3: Board ohne allgemeinen Backlog — Swimlane mit Karten nicht löschen.
	e.zuruecksetzen755(t)
	setze("b1", `{"id":"__backlog__","title":"Backlog","kind":"backlog"`, `{"id":"bl-alt","title":"Backlog","kind":"lane"`)
	if s, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/swimlanes/s1/loeschen", ""); s != http.StatusConflict {
		t.Errorf("ohne Backlog: %d %s", s, b)
	}

	// K-5: gleiche id in zwei Objektarten — der Termin-Code passt nicht auf
	// die Karte mit derselben id.
	e.zuruecksetzen755(t)
	setze("b1", `"id":"k2","displayId":2,`, `"id":"t-1","displayId":2,`)
	code, _ := e.pruefe757(t, e.projekt, "/werkzeuge/boards/b1/termine/t-1/loeschen")
	if s, m := e.loesche757(t, e.projekt, "/werkzeuge/boards/b1/karten/t-1/loeschen", code); s != http.StatusForbidden {
		t.Errorf("Termin-Code für Karte gleicher id: %d %v", s, m)
	}

	// K-7: Die Erfolgsantwort trägt Fremdtext — mit Sicherheitshinweis.
	e.zuruecksetzen755(t)
	code, _ = e.pruefe757(t, e.projekt, "/werkzeuge/boards/b1/termine/t-1/loeschen")
	if s, m := e.loesche757(t, e.projekt, "/werkzeuge/boards/b1/termine/t-1/loeschen", code); s != http.StatusOK || m["sicherheitshinweis"] == nil {
		t.Errorf("Erfolgsantwort: %d %v", s, m)
	}
}

// Verify #757 V-1: Entfernt ein Mensch das neue Hintergrundbild zwischen
// Lesen und Schreiben, setzt board_aendern keinen hängenden Verweis.
// V-5: Eine Vorlage ohne id sperrt das Werkzeug nicht und bleibt erhalten.
func TestAgent757_VerifyNacharbeit(t *testing.T) {
	e := newEnv671(t, true)
	owner, _ := e.personOwner757(t)
	e.zuruecksetzen755(t)
	st := strings.Replace(e.stand(t, "b2"), `"nextDisplayId":10,`, `"nextDisplayId":10,"attachments":[{"id":"bg1","filename":"a.png","mime":"image/png","size":1},{"id":"bg2","filename":"b.png","mime":"image/png","size":1}],"background":{"attachmentId":"bg1"},"templates":[{"title":"Ohne id"},{"id":"v1","title":"V"}],`, 1)
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b2'`, st); err != nil {
		t.Fatal(err)
	}
	einmal := false
	zurueck := api.SetzeVorDemSchreibenFuerTest(func() {
		if einmal {
			return
		}
		einmal = true
		jetzt := strings.Replace(e.stand(t, "b2"), `,{"id":"bg2","filename":"b.png","mime":"image/png","size":1}`, ``, 1)
		if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b2'`, jetzt); err != nil {
			t.Fatal(err)
		}
	})
	s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"hintergrund":{"anhang":"bg2"}}`)
	zurueck()
	if s != http.StatusConflict || strings.Contains(e.stand(t, "b2"), `"attachmentId":"bg2"`) {
		t.Errorf("Hintergrund auf entfernten Anhang: %d %s", s, b)
	}
	s, b = e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"vorlagen":[{"id":"v1","titel":"V neu"}]}`)
	// Q-2: an der bisherigen Stelle (vorn), nicht ans Ende gewandert.
	if st := e.stand(t, "b2"); s != http.StatusOK || !strings.Contains(st, `"title":"Ohne id"`) || !strings.Contains(st, `"title":"V neu"`) ||
		strings.Index(st, `"title":"Ohne id"`) > strings.Index(st, `"title":"V neu"`) {
		t.Errorf("Vorlage ohne id: %d %s", s, b)
	}
}
