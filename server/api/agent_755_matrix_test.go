package api_test

// Karte #755 (ADR-0139 Stufe 4a, Teil 2): Hilfen für die Rollen-Matrix der
// Schreib-Werkzeuge. Schreib-Werkzeuge ändern den Stand (archivieren, abhaken,
// Kommentar bearbeiten …); damit jede Zelle denselben Ausgangsstand sieht, setzt
// die Matrix die Boards vor jedem Aufruf auf board755 zurück.

import (
	"encoding/json"
	"strings"
	"testing"
)

// agentNamen755: die Namen aller Tokens, die newEnv671 und die Matrix anlegen
// — je Name ein eigener Kommentar „k-<name>“ an Karte 1, damit
// kommentar_bearbeiten in jeder Zelle einen EIGENEN Kommentar vorfindet.
var agentNamen755 = []string{"projekt", "leser", "open-webui", "recherche", "anna-b1", "anna-b1-lesen", "anna-b2",
	"anna-mehrere-0", "anna-mehrere-1"}

// board755: board671 plus Checkliste und Verweis an Karte 1, Rückverweis an
// Karte 5, je Agent ein Kommentar an Karte 1 und ein Termin t-1. Karte #757:
// ein Anhang a-1 an Karte 1 und eine leere Spalte col-leer.
func board755(id, titel string) string {
	var kommentare []string
	for _, n := range agentNamen755 {
		kommentare = append(kommentare, `{"id":"k-`+n+`","author":"agent:`+n+`","text":"Von `+n+`","created":"2026-10-01"}`)
	}
	s := board671(id, titel)
	for _, r := range [][2]string{
		{`"nextDisplayId":10,`, `"nextDisplayId":10,"termine":[{"id":"t-1","titel":"Termin","datum":"2026-10-20","erstellt":"2026-10-01"}],`},
		{`{"id":"col-erledigt","title":"Erledigt"}]`, `{"id":"col-erledigt","title":"Erledigt"},{"id":"col-leer","title":"Leer"}]`},
		{`"owner":"Anna","dueDate":"2026-10-10","comments":[]}`, `"owner":"Anna","dueDate":"2026-10-10","description":"Text\n- [ ] Eins\n- [x] Zwei",` +
			`"links":[{"targetId":"k5","type":"related","created":"2026-10-01"}],"comments":[` + strings.Join(kommentare, ",") + `]}`},
		{`"title":"Im Sprint","columnId":"col-in-arbeit","comments":[]}`, `"title":"Im Sprint","columnId":"col-in-arbeit",` +
			`"links":[{"targetId":"k1","type":"related","created":"2026-10-01"}],"comments":[]}`},
		{`"description":"Text\n- [ ] Eins\n- [x] Zwei",`, `"description":"Text\n- [ ] Eins\n- [x] Zwei",` +
			`"attachments":[{"id":"a-1","filename":"notiz.txt","mime":"text/plain","size":4,"created":"2026-10-01"}],`},
	} {
		if strings.Count(s, r[0]) != 1 {
			panic("board755: Anker nicht eindeutig: " + r[0])
		}
		s = strings.Replace(s, r[0], r[1], 1)
	}
	return s
}

// zuruecksetzen755 setzt b1, b2 und b3 auf board755.
func (e *env671) zuruecksetzen755(t *testing.T) {
	t.Helper()
	for id, titel := range map[string]string{"b1": "Marketing", "b2": "Technik", "b3": "Geheim"} {
		if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, board755(id, titel), id); err != nil {
			t.Fatal(err)
		}
	}
}

// stand755: der stand von Karte 1 im Ausgangsstand (auf allen drei Boards
// gleich — Titel, Beschreibung und Agent-Kontext sind dieselben).
func (e *env671) stand755(t *testing.T) string {
	t.Helper()
	e.zuruecksetzen755(t)
	s, b := e.rufe(t, e.projekt, "", "GET", "/werkzeuge/boards/b1/karten/1?format=json", "")
	stand, _ := json671(t, b)["stand"].(string)
	if s != 200 || stand == "" {
		t.Fatalf("stand: %d %s", s, b)
	}
	return stand
}

// koerper755: Body je Schreib-Werkzeug aus #755 (STAND wird ersetzt).
var koerper755 = map[string]string{
	"karte_aendern":        `{"kritisch":true,"labels":["Matrix"]}`,
	"checkliste_abhaken":   `{"punkt":1,"stand":"STAND"}`,
	"archivieren":          `{}`,
	"wiederherstellen":     `{}`,
	"kommentar_bearbeiten": `{"text":"bearbeitet"}`,
	"verweis_setzen":       `{"ziel":"2","typ":"blocks"}`,
	"verweis_entfernen":    `{"ziel":"5","typ":"related"}`,
	"uebernehmen":          `{"session":"matrix"}`,
	"freigeben":            `{"session":"matrix"}`,
	"termin_aendern":       `{"ort":"Raum 1"}`,
	"swimlane_anlegen":     `{"titel":"Neu aus der Matrix"}`,
	"swimlane_aendern":     `{"titel":"Sprint 1 neu"}`,
	"auf_antwort_warten":   `{"frage":"Matrix?"}`,
	// Karte #757: CODE ersetzt mitCode757 durch den Code des Prüf-Werkzeugs.
	"karte_loeschen":    `{"code":"CODE"}`,
	"swimlane_loeschen": `{"code":"CODE"}`,
	"termin_loeschen":   `{"code":"CODE"}`,
	"anhang_loeschen":   `{"code":"CODE"}`,
	"spalte_loeschen":   `{"code":"CODE"}`,
	"board_aendern":     `{"titel":"Neu aus der Matrix"}`,
}

// pfad755: Pfad-Parameter der neuen Werkzeuge; kommentar je Agent.
func pfad755(pfad, board, agent string) string {
	return strings.NewReplacer("{boardId}", board, "{karte}", "1", "{kommentar}", "k-"+agent, "{termin}", "t-1", "{swimlane}", "s1",
		"{anhang}", "a-1", "{spalte}", "col-leer").Replace(pfad)
}

// mcpArgumente755: Argumente eines Schreib-Werkzeugs aus #755 für tools/call —
// Body (koerper755, STAND ersetzt) plus die Pfad-Parameter des Werkzeugs.
func mcpArgumente755(t *testing.T, pfad, name, board, agent, stand string) string {
	t.Helper()
	args := map[string]any{}
	body := koerper755[name]
	if body == "" {
		body = "{}" // Karte #757: Prüf-Werkzeuge haben nur Pfad-Parameter
	}
	if err := json.Unmarshal([]byte(strings.ReplaceAll(body, "STAND", stand)), &args); err != nil {
		t.Fatal(err)
	}
	for param, wert := range map[string]string{"boardId": board, "karte": "1", "kommentar": "k-" + agent, "termin": "t-1", "swimlane": "s1",
		"anhang": "a-1", "spalte": "col-leer"} {
		if strings.Contains(pfad, "{"+param+"}") {
			args[param] = wert
		}
	}
	roh, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return string(roh)
}
