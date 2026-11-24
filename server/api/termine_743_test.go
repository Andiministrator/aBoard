package api_test

// Karte #743 (ADR-0138 §4): Termine ohne Karte am Server — über die echten
// Wege (Agenten-Zugang, Werkzeuge, postOps, Fold, Feed, Restore).
//
// Teil des Pflicht-Nachweises „board.termine bleibt in allen Lade- und
// Speicherwegen erhalten“ (ADR-0138 §4 letzter Punkt): Server-Speichern per
// PUT (state_json opak), per Ops, und Board-Restore. Die Browser-Wege prüft
// tests/termine-743.test.js.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
)

// termine743 liest board.termine aus einem Board-Stand.
func termine743(t *testing.T, state string) []map[string]any {
	t.Helper()
	var s struct {
		Board struct {
			Termine []map[string]any `json:"termine"`
		} `json:"board"`
	}
	if err := json.Unmarshal([]byte(state), &s); err != nil {
		t.Fatalf("Stand ist kein JSON: %v", err)
	}
	return s.Board.Termine
}

func terminMitID743(t *testing.T, state, id string) map[string]any {
	t.Helper()
	for _, x := range termine743(t, state) {
		if x["id"] == id {
			return x
		}
	}
	return nil
}

// --- termin_anlegen ---------------------------------------------------------

func TestTermine743_AnlegenUeberWerkzeug(t *testing.T) {
	e := newEnv671(t, true)
	s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/termine",
		`{"titel":"Kundentermin Müller","datum":"2026-10-08","von":"14:00","bis":"15:00","ort":"Büro","beschreibung":"Angebot"}`)
	if s != http.StatusOK {
		t.Fatalf("termin_anlegen: %d %s", s, b)
	}
	m := json671(t, b)
	id, _ := m["terminId"].(string)
	if id == "" || m["link"] != basis671+"/#board=b1&termin="+id {
		t.Fatalf("Antwort ohne terminId/Link: %s", b)
	}
	if !strings.Contains(m["ergebnis"].(string), "2026-10-08 14:00–15:00") {
		t.Errorf("ergebnis nennt die Zeit nicht: %s", b)
	}
	termin := terminMitID743(t, e.stand(t, "b1"), id)
	if termin == nil {
		t.Fatalf("Termin nicht im Board: %s", e.stand(t, "b1"))
	}
	for k, soll := range map[string]any{"titel": "Kundentermin Müller", "datum": "2026-10-08", "von": "14:00", "bis": "15:00",
		"ort": "Büro", "beschreibung": "Angebot", "erstelltVon": "agent:open-webui für anna@firma.de"} {
		if termin[k] != soll {
			t.Errorf("%s = %v, erwartet %v", k, termin[k], soll)
		}
	}
	if _, ok := termin["erstellt"].(string); !ok {
		t.Errorf("erstellt fehlt: %v", termin)
	}
	// Kein Kartentyp: keine neue Karte.
	if strings.Count(e.stand(t, "b1"), `"displayId"`) != strings.Count(board671("b1", "Marketing"), `"displayId"`) {
		t.Error("termin_anlegen hat eine Karte angelegt")
	}

	// Wiederholung mit demselben Inhalt: derselbe Termin, kein Doppel.
	s, b = e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/termine",
		`{"titel":"Kundentermin Müller","datum":"2026-10-08","von":"14:00","bis":"15:00","ort":"Büro","beschreibung":"Angebot"}`)
	if s != http.StatusOK || json671(t, b)["wiederholt"] != true || json671(t, b)["terminId"] != id {
		t.Fatalf("Wiederholung: %d %s", s, b)
	}
	if n := len(termine743(t, e.stand(t, "b1"))); n != 1 {
		t.Fatalf("%d Termine nach Wiederholung, erwartet 1", n)
	}
}

func TestTermine743_AnlegenPrueftEingabenUndRechte(t *testing.T) {
	e := newEnv671(t, true)
	vorher := e.stand(t, "b1")
	for name, body := range map[string]string{
		"ohne titel":       `{"datum":"2026-10-08"}`,
		"ohne datum":       `{"titel":"T"}`,
		"datum kaputt":     `{"titel":"T","datum":"2026-02-30"}`,
		"datum mit zeit":   `{"titel":"T","datum":"2026-10-08T14:00"}`,
		"von kaputt":       `{"titel":"T","datum":"2026-10-08","von":"25:00"}`,
		"bis vor von":      `{"titel":"T","datum":"2026-10-08","von":"15:00","bis":"14:00"}`,
		"bisDatum vorher":  `{"titel":"T","datum":"2026-10-08","bisDatum":"2026-10-07"}`,
		"unbekanntes feld": `{"titel":"T","datum":"2026-10-08","personen":["x"]}`,
		"erstelltVon":      `{"titel":"T","datum":"2026-10-08","erstelltVon":"Chef"}`,
		"titel umbruch":    `{"titel":"a\nb","datum":"2026-10-08"}`,
	} {
		if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/termine", body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %s, erwartet 400", name, s, b)
		}
	}
	// Leserecht reicht nicht (cora: viewer), fremdes Board nicht (b3), Leser-Token nicht.
	if s, b := e.rufe(t, e.token, "cora@extern.de", "POST", "/werkzeuge/boards/b1/termine", `{"titel":"T","datum":"2026-10-08"}`); s != http.StatusForbidden {
		t.Errorf("viewer: %d %s", s, b)
	}
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b3/termine", `{"titel":"T","datum":"2026-10-08"}`); s != http.StatusForbidden {
		t.Errorf("fremdes Board: %d %s", s, b)
	}
	if s, b := e.rufe(t, e.leser, "", "POST", "/werkzeuge/boards/b1/termine", `{"titel":"T","datum":"2026-10-08"}`); s != http.StatusForbidden {
		t.Errorf("Leser-Token: %d %s", s, b)
	}
	if s, b := e.rufe(t, e.vorschlg, "", "POST", "/werkzeuge/boards/b1/termine", `{"titel":"T","datum":"2026-10-08"}`); s != http.StatusForbidden {
		t.Errorf("Vorschlags-Token: %d %s", s, b)
	}
	if e.stand(t, "b1") != vorher {
		t.Fatal("abgelehnte Aufrufe haben das Board verändert")
	}
	// Projekt-Token (editor auf b1): Herkunft agent:projekt, ohne Person.
	s, b := e.rufe(t, e.projekt, "", "POST", "/werkzeuge/boards/b1/termine", `{"titel":"Messe","datum":"2026-10-20","bisDatum":"2026-10-22"}`)
	if s != http.StatusOK {
		t.Fatalf("Projekt-Token: %d %s", s, b)
	}
	tm := terminMitID743(t, e.stand(t, "b1"), json671(t, b)["terminId"].(string))
	if tm["erstelltVon"] != "agent:projekt" || tm["bisDatum"] != "2026-10-22" {
		t.Errorf("Projekt-Token-Termin: %v", tm)
	}
}

// --- termine_lesen ----------------------------------------------------------

func TestTermine743_LesenUeberAlleBoardsMitLeserecht(t *testing.T) {
	e := newEnv671(t, true)
	for _, a := range [][3]string{
		{"b1", `{"titel":"Marketing-Termin","datum":"2026-10-08","von":"09:00"}`, "anna@firma.de"},
		{"b2", `{"titel":"Technik-Termin","datum":"2026-10-08"}`, "anna@firma.de"},
		{"b2", `{"titel":"Später","datum":"2026-12-24"}`, "anna@firma.de"},
		// Nur Ende, kein Beginn: sortiert nach dem Ende wie im Browser (K-6).
		{"b1", `{"titel":"Nur Ende","datum":"2026-10-08","bis":"08:00"}`, "anna@firma.de"},
	} {
		if s, b := e.rufe(t, e.token, a[2], "POST", "/werkzeuge/boards/"+a[0]+"/termine", a[1]); s != http.StatusOK {
			t.Fatalf("anlegen %s: %d %s", a[0], s, b)
		}
	}
	// Ein Termin auf b3, auf das Anna keinen Zugriff hat — direkt in den Stand.
	ctx := context.Background()
	b3, err := e.boards.Get(ctx, "b3")
	if err != nil {
		t.Fatal(err)
	}
	stand := strings.Replace(string(b3.StateJSON), `"nextDisplayId":10,`, `"nextDisplayId":10,"termine":[{"id":"geheim","titel":"Geheimtermin","datum":"2026-10-08"}],`, 1)
	if _, err := e.boards.Update(ctx, "b3", b3.Title, b3.Version, []byte(stand), b3.UpdatedAt); err != nil {
		t.Fatal(err)
	}

	s, b := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/termine?ab=2026-10-01&bis=2026-10-31", "")
	if s != http.StatusOK {
		t.Fatalf("termine_lesen: %d %s", s, b)
	}
	if strings.Contains(string(b), "Geheimtermin") {
		t.Fatalf("Termin eines Boards ohne Leserecht sichtbar: %s", b)
	}
	var antw struct {
		Sicherheitshinweis string           `json:"sicherheitshinweis"`
		Termine            []map[string]any `json:"termine"`
		Anzahl             int              `json:"anzahl"`
	}
	if err := json.Unmarshal(b, &antw); err != nil {
		t.Fatal(err)
	}
	if antw.Sicherheitshinweis == "" || antw.Anzahl != 3 || len(antw.Termine) != 3 {
		t.Fatalf("erwartet 3 Termine im Oktober: %s", b)
	}
	// Ganztags zuerst, dann nach Uhrzeit — „nur Ende“ nach dem Ende (K-6,
	// gleiche Reihenfolge wie uhrzeitAmTag im Browser).
	if antw.Termine[0]["titel"] != "Technik-Termin" || antw.Termine[1]["titel"] != "Nur Ende" || antw.Termine[2]["titel"] != "Marketing-Termin" {
		t.Errorf("Reihenfolge: %s", b)
	}
	if antw.Termine[2]["board"] != "Marketing" || antw.Termine[2]["von"] != "09:00" {
		t.Errorf("Felder: %v", antw.Termine[2])
	}
	// Cora (viewer auf b1) liest die Termine von b1, aber nicht die von b2.
	s, b = e.rufe(t, e.token, "cora@extern.de", "GET", "/werkzeuge/termine?ab=2026-10-01&bis=2026-12-31", "")
	if s != http.StatusOK || !strings.Contains(string(b), "Marketing-Termin") || strings.Contains(string(b), "Technik-Termin") {
		t.Errorf("cora: %d %s", s, b)
	}
	// Projekt-Token: nur das eigene Board.
	s, b = e.rufe(t, e.leser, "", "GET", "/werkzeuge/termine?ab=2026-10-01&bis=2026-12-31", "")
	if s != http.StatusOK || !strings.Contains(string(b), "Marketing-Termin") || strings.Contains(string(b), "Später") {
		t.Errorf("Leser-Token: %d %s", s, b)
	}
	// Vorschlags-Token liest nichts.
	if s, b := e.rufe(t, e.vorschlg, "", "GET", "/werkzeuge/termine", ""); s != http.StatusForbidden {
		t.Errorf("Vorschlags-Token: %d %s", s, b)
	}
	// Ohne JWT kein Zugang.
	if s, _ := e.rufe(t, e.token, "", "GET", "/werkzeuge/termine", ""); s != http.StatusUnauthorized {
		t.Errorf("ohne JWT: %d", s)
	}
	// Eingaben.
	for _, q := range []string{"?ab=morgen", "?ab=2026-10-10&bis=2026-10-01", "?ab=2026-01-01&bis=2027-06-01"} {
		if s, b := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/termine"+q, ""); s != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, s, b)
		}
	}
}

// --- Rohe Ops: Herkunft, Form, Rechte ---------------------------------------

func TestTermine743_OpsHerkunftUndForm(t *testing.T) {
	e := newEnv608(t)
	// Mensch (Browser): erstelltVon kommt wie gesendet an.
	s, b := e.anfrage(t, e.mensch, "POST", "/boards/b1/ops", "anna@firma.de",
		`{"ops":[{"type":"termin.upsert","payload":{"termin":{"id":"t1","titel":"Telefonat","datum":"2026-10-09","erstellt":"2026-10-07","erstelltVon":"Anna"}}}]}`)
	if s != http.StatusOK {
		t.Fatalf("Mensch termin.upsert: %d %s", s, b)
	}
	roh := func() string {
		x, err := e.boards.Get(context.Background(), "b1")
		if err != nil {
			t.Fatal(err)
		}
		return string(x.StateJSON)
	}
	if tm := terminMitID743(t, roh(), "t1"); tm == nil || tm["erstelltVon"] != "Anna" {
		t.Fatalf("Termin nach Mensch-Op: %v", tm)
	}
	// Ungültige Form → 422 mit Grund, nichts geschrieben.
	vorher := roh()
	s, b = e.anfrage(t, e.mensch, "POST", "/boards/b1/ops", "anna@firma.de",
		`{"ops":[{"type":"termin.upsert","payload":{"termin":{"id":"t2","titel":"X","datum":"2026-02-30"}}}]}`)
	if s != http.StatusUnprocessableEntity || !strings.Contains(string(b), "termin-datum-ungueltig") {
		t.Fatalf("kaputtes Datum: %d %s", s, b)
	}
	// board.field auf termine wird abgelehnt (die ganze Liste ginge verloren).
	s, b = e.anfrage(t, e.mensch, "POST", "/boards/b1/ops", "anna@firma.de",
		`{"ops":[{"type":"board.field","payload":{"field":"termine","value":[]}}]}`)
	if s != http.StatusUnprocessableEntity {
		t.Fatalf("board.field termine: %d %s", s, b)
	}
	if roh() != vorher {
		t.Fatal("abgelehnte Ops haben das Board verändert")
	}

	// Agent (Personen-Token): erstelltVon wird gestempelt, eine Fälschung —
	// auch in anderer Schreibweise — fällt weg.
	tok := e.tokenFuer(t, "claude", "anna@firma.de")
	s, b = e.anfrage(t, e.agent, "POST", "/boards/b1/ops", tok,
		`{"ops":[{"type":"termin.upsert","payload":{"termin":{"id":"t3","titel":"Vom Agenten","datum":"2026-10-10","erstelltVon":"Chef"}}}]}`)
	if s != http.StatusOK {
		t.Fatalf("Agent termin.upsert: %d %s", s, b)
	}
	if tm := terminMitID743(t, roh(), "t3"); tm == nil || tm["erstelltVon"] != "agent:claude für anna@firma.de" {
		t.Fatalf("Herkunft des Agenten-Termins: %v", tm)
	}
	s, b = e.anfrage(t, e.agent, "POST", "/boards/b1/ops", tok,
		`{"ops":[{"type":"termin.upsert","payload":{"termin":{"id":"t4","titel":"X","datum":"2026-10-10","erstelltVon":"a","ERSTELLTVON":"b"}}}]}`)
	if s != http.StatusBadRequest {
		t.Fatalf("Schreibweisen-Doppel: %d %s", s, b)
	}
	// Agent ändert einen Termin des Menschen: Herkunft bleibt die des Menschen.
	s, b = e.anfrage(t, e.agent, "POST", "/boards/b1/ops", tok,
		`{"ops":[{"type":"termin.upsert","payload":{"termin":{"id":"t1","titel":"Telefonat verschoben","datum":"2026-10-11"}}}]}`)
	if s != http.StatusOK {
		t.Fatalf("Agent ändert: %d %s", s, b)
	}
	if tm := terminMitID743(t, roh(), "t1"); tm["erstelltVon"] != "Anna" || tm["erstellt"] != "2026-10-07" || tm["datum"] != "2026-10-11" {
		t.Fatalf("Herkunft nach Änderung durch Agent: %v", tm)
	}
	// Löschen mit Schreibrecht.
	s, b = e.anfrage(t, e.agent, "POST", "/boards/b1/ops", tok, `{"ops":[{"type":"termin.delete","payload":{"terminId":"t1"}}]}`)
	if s != http.StatusOK || terminMitID743(t, roh(), "t1") != nil {
		t.Fatalf("termin.delete: %d %s", s, b)
	}
}

// --- Pflicht-Nachweis Server-Speichern: PUT lässt termine stehen -----------

func TestTermine743_PutBewahrtTermine(t *testing.T) {
	e := newEnv608(t)
	resp, err := http.DefaultClient.Do(func() *http.Request {
		r, _ := http.NewRequest("GET", e.mensch.URL+"/boards/b1", nil)
		r.Header.Set("Remote-Email", "anna@firma.de")
		r.Header.Set("Remote-User", "anna@firma.de")
		return r
	}())
	if err != nil {
		t.Fatal(err)
	}
	etag := resp.Header.Get("ETag")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	body := `{"version":"2","board":{"id":"b1","title":"B","nextDisplayId":2,
	  "columnDefs":[{"id":"col-ready","title":"Ready"}],
	  "termine":[{"id":"t1","titel":"Über PUT","datum":"2026-10-08","von":"10:00","farbe":"rot"}],
	  "sprints":[{"id":"s1","title":"S","kind":"sprint","cards":[{"id":"k1","displayId":1,"title":"K","columnId":"col-ready","comments":[]}]}]}}`
	if s, b := e.anfrage(t, e.mensch, "PUT", "/boards/b1", "anna@firma.de", body, "If-Match", etag); s != http.StatusOK {
		t.Fatalf("PUT: %d %s", s, b)
	}
	s, b := e.anfrage(t, e.mensch, "GET", "/boards/b1", "anna@firma.de", "")
	if s != http.StatusOK {
		t.Fatalf("GET: %d %s", s, b)
	}
	tm := terminMitID743(t, string(b), "t1")
	if tm == nil || tm["titel"] != "Über PUT" || tm["farbe"] != "rot" {
		t.Fatalf("Termin nach PUT/GET: %s", b)
	}
	// Und eine Karten-Op danach lässt ihn ebenfalls stehen.
	if s, b := e.anfrage(t, e.mensch, "POST", "/boards/b1/ops", "anna@firma.de",
		`{"ops":[{"type":"card.field","payload":{"cardId":"k1","field":"title","value":"Neu"}}]}`); s != http.StatusOK {
		t.Fatalf("card.field: %d %s", s, b)
	}
	_, b = e.anfrage(t, e.mensch, "GET", "/boards/b1", "anna@firma.de", "")
	if terminMitID743(t, string(b), "t1") == nil {
		t.Fatalf("Termin nach Karten-Op weg: %s", b)
	}
}

// --- Pflicht-Nachweis Board-Restore -------------------------------------------

func TestTermine743_RestoreBewahrtTermine(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	ctx := context.Background()
	state := `{"version":"2","board":{"id":"b1","title":"Test","sprints":[],
	  "termine":[{"id":"t1","titel":"Gesichert","datum":"2026-10-08","bis":"11:00"}]}}`
	if _, err := env.boards.Insert(ctx, "b1", "Test", "2", []byte(state)); err != nil {
		t.Fatal(err)
	}
	if err := env.acl.Set(ctx, "b1", "owner@x.de", "owner"); err != nil {
		t.Fatal(err)
	}
	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("backup/now: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	fn, _ := body["filename"].(string)

	// Stand danach ohne Termine.
	b, err := env.boards.Get(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.boards.Update(ctx, "b1", b.Title, b.Version, []byte(`{"version":"2","board":{"id":"b1","title":"Test","sprints":[]}}`), b.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	resp = env.uploadRestore(t, "owner@x.de", filepath.Join(env.bkpDir, "b1", fn), "overwrite", "b1")
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("restore: %d %s", resp.StatusCode, raw)
	}
	resp.Body.Close()
	b, err = env.boards.Get(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if tm := terminMitID743(t, string(b.StateJSON), "t1"); tm == nil || tm["titel"] != "Gesichert" || tm["bis"] != "11:00" {
		t.Fatalf("Termin nach Restore: %s", b.StateJSON)
	}
}

// --- Abo-Feed und /me/termine -------------------------------------------------

const stand743a = `{"version":"2","board":{"id":"a","title":"A","sprints":[
	{"id":"sp1","title":"S","kind":"sprint","cards":[
		{"id":"c1","displayId":1,"title":"Frist","columnId":"col-ready","dueDate":"2026-10-08"}]}],
	"termine":[
		{"id":"t1","titel":"Kundentermin","datum":"2026-10-08","von":"14:00","bis":"15:30","beschreibung":"Angebot, Preise; Termin","ort":"Büro"},
		{"id":"t2","titel":"Messe","datum":"2026-10-20","bisDatum":"2026-10-22"},
		{"id":"","titel":"ohne id","datum":"2026-10-08"},
		{"id":"t3","titel":"kaputtes Datum","datum":"2026-02-30"}]}}`

const stand743b = `{"version":"2","board":{"id":"b","title":"B","sprints":[],
	"termine":[{"id":"tb","titel":"Fremder Termin","datum":"2026-10-08"}]}}`

func TestTermine743_FeedEnthaeltTermine(t *testing.T) {
	env := newICalEnv(t, true)
	defer env.close()
	env.seedUser(t, "u1", "andi@example.com")
	ctx := context.Background()
	for id, st := range map[string]string{"a": stand743a, "b": stand743b} {
		if _, err := env.boards.Insert(ctx, id, strings.ToUpper(id), "2", []byte(st)); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.acl.Set(ctx, "a", "andi@example.com", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := env.acl.Set(ctx, "b", "other@example.com", "owner"); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []map[string]any{{"board_id": "a"}, {}} {
		ics := env.feed(t, env.feedToken(t, "andi@example.com", scope))
		// Karte und Termine im selben Abo, Termine mit UID <termin>@<board>.aboard.
		eventVon(t, ics, "c1")
		ev := eventVon(t, ics, "t1")
		for _, muss := range []string{"UID:t1@a.aboard", "DTSTART:20261008T120000Z", "DTEND:20261008T133000Z",
			"SUMMARY:Kundentermin", `DESCRIPTION:Angebot\, Preise\; Termin`, "URL:http", "LOCATION:Büro"} {
			if !strings.Contains(ev, muss) {
				t.Errorf("%v: %q fehlt in\n%s", scope, muss, ev)
			}
		}
		if !strings.Contains(strings.ReplaceAll(ev, "\r\n ", ""), "/#board=a&termin=t1") {
			t.Errorf("Deep-Link fehlt:\n%s", ev)
		}
		messe := eventVon(t, ics, "t2")
		if !strings.Contains(messe, "DTSTART;VALUE=DATE:20261020\r\nDTEND;VALUE=DATE:20261023") {
			t.Errorf("mehrtägiger Termin:\n%s", messe)
		}
		for _, nein := range []string{"ohne id", "kaputtes Datum", "Fremder Termin"} {
			if strings.Contains(ics, nein) {
				t.Errorf("%v: %q im Feed:\n%s", scope, nein, ics)
			}
		}
	}
}

func TestTermine743_MeTermine(t *testing.T) {
	env := newCalEnv(t)
	defer env.closer()
	env.seedCalBoard(t, "a", "andi@example.com", stand743a)
	env.seedCalBoard(t, "b", "other@example.com", stand743b)
	resp, out := env.get(t, "/me/termine", "andi@example.com")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if len(out) != 2 || out[0]["id"] != "t1" || out[1]["id"] != "t2" || out[0]["board_id"] != "a" || out[0]["von"] != "14:00" {
		t.Fatalf("/me/termine: %v", out)
	}
	_, out = env.get(t, "/me/termine?dueDateRange=2026-10-21~2026-10-31", "andi@example.com")
	if len(out) != 1 || out[0]["id"] != "t2" {
		t.Fatalf("Bereich (mehrtägig überlappend): %v", out)
	}
	if resp, _ := env.get(t, "/me/termine?dueDateRange=kaputt", "andi@example.com"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("kaputter Bereich: %d", resp.StatusCode)
	}
}

// Kritiker 07.10., K-5: Ohne `ab` beginnt termine_lesen mit „heute“ in der
// Instanz-Zeitzone (Standard Europe/Berlin), nicht in UTC. 22:30 UTC am 08.10.
// ist in Berlin schon der 09.10.
func TestTermine743_LesenHeuteInInstanzZone(t *testing.T) {
	e := newEnv671(t, true)
	for _, a := range []string{`{"titel":"Gestern","datum":"2026-10-08"}`, `{"titel":"Heute","datum":"2026-10-09"}`} {
		if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/termine", a); s != http.StatusOK {
			t.Fatalf("anlegen: %d %s", s, b)
		}
	}
	zurueck := api.SetzeOpsNowFuerTest(func() time.Time { return time.Date(2026, 10, 8, 22, 30, 0, 0, time.UTC) })
	defer zurueck()
	s, b := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/termine", "")
	if s != http.StatusOK || !strings.Contains(string(b), `"Heute"`) || strings.Contains(string(b), `"Gestern"`) {
		t.Fatalf("heute in Ortszeit: %d %s", s, b)
	}

	// Verify 07.10. b, V-3: Eine im Admin gesetzte Zone wirkt. 12:00 UTC am
	// 08.10. ist in Berlin noch der 08., in Auckland (UTC+13) schon der 09.
	api.SetzeOpsNowFuerTest(func() time.Time { return time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC) })
	s, b = e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/termine", "")
	if s != http.StatusOK || !strings.Contains(string(b), `"Gestern"`) {
		t.Fatalf("Standardzone Berlin: %d %s", s, b)
	}
	if err := e.cfg.Set(context.Background(), "kalender.zeitzone", "Pacific/Auckland", false, "test"); err != nil {
		t.Fatal(err)
	}
	s, b = e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/termine", "")
	if s != http.StatusOK || !strings.Contains(string(b), `"Heute"`) || strings.Contains(string(b), `"Gestern"`) {
		t.Fatalf("konfigurierte Zone Auckland: %d %s", s, b)
	}
}
