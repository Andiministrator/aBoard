package api_test

// Karte #753 (ADR-0139 §3, §6.4/§6.5): MCP-Eingang über die echte Kette
// (Agenten-Middleware → /mcp → SDK → Schritt B → Werkzeug-Handler → postOps).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

const mcpVersion753 = "2026-07-28"

func meta753() string {
	return `"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcpVersion753 + `","io.modelcontextprotocol/clientCapabilities":{}}`
}

// mcpRufe schickt eine JSON-RPC-Anfrage an /mcp. kopf überschreibt oder
// ergänzt die Standard-Kopfzeilen der Fassung 2026-07-28 (Wert "" löscht).
func (e *env671) mcpRufe(t *testing.T, token, email, body string, kopf map[string]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.agent.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	var env struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	_ = json.Unmarshal([]byte(body), &env)
	req.Header.Set("MCP-Protocol-Version", mcpVersion753)
	req.Header.Set("Mcp-Method", env.Method)
	if env.Params.Name != "" {
		req.Header.Set("Mcp-Name", env.Params.Name)
	}
	for k, v := range kopf {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	if email != "" {
		req.Header.Set("X-OpenWebUI-User-Jwt", frisch669(t, email))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m == nil {
		m = map[string]any{"_roh": string(b)}
	}
	return resp.StatusCode, m
}

func (e *env671) mcpWerkzeug(t *testing.T, token, email, name, args string) (int, map[string]any) {
	t.Helper()
	return e.mcpRufe(t, token, email, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`,`+meta753()+`}}`, nil)
}

func ergebnis753(t *testing.T, m map[string]any) (fehler bool, text string, strukt any) {
	t.Helper()
	r, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("kein result: %v", m)
	}
	fehler, _ = r["isError"].(bool)
	if c, _ := r["content"].([]any); len(c) > 0 {
		text, _ = c[0].(map[string]any)["text"].(string)
	}
	return fehler, text, r["structuredContent"]
}

func werkzeugNamen753(t *testing.T, m map[string]any) []string {
	t.Helper()
	r, _ := m["result"].(map[string]any)
	liste, _ := r["tools"].([]any)
	var namen []string
	for _, x := range liste {
		namen = append(namen, x.(map[string]any)["name"].(string))
	}
	sort.Strings(namen)
	return namen
}

func TestAgent753_DiscoverUndListe(t *testing.T) {
	e := newEnv671(t, true)
	// Delegierend ohne JWT: discover und tools/list liefern keine Board-Daten.
	s, m := e.mcpRufe(t, e.token, "", `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+meta753()+`}}`, nil)
	if s != http.StatusOK {
		t.Fatalf("discover: %d %v", s, m)
	}
	r := m["result"].(map[string]any)
	versionen := r["supportedVersions"].([]any)
	if !strings.Contains(strings.Join(func() []string {
		var v []string
		for _, x := range versionen {
			v = append(v, x.(string))
		}
		return v
	}(), ","), "2026-07-28") || r["resultType"] != "complete" {
		t.Fatalf("discover: %v", r)
	}
	s, m = e.mcpRufe(t, e.token, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{`+meta753()+`}}`, nil)
	if s != http.StatusOK {
		t.Fatalf("tools/list: %d %v", s, m)
	}
	r = m["result"].(map[string]any)
	if r["cacheScope"] != "private" || r["resultType"] != "complete" {
		t.Errorf("tools/list: cacheScope %v, resultType %v — erwartet private/complete", r["cacheScope"], r["resultType"])
	}
	if ttl, _ := r["ttlMs"].(float64); ttl <= 0 {
		t.Errorf("ttlMs %v", r["ttlMs"])
	}
	alle := []string{"board_lesen", "boards", "finding_anlegen", "karte_anlegen", "karte_zeigen", "kommentieren", "termin_anlegen", "termine_lesen", "verschieben"}
	if got := werkzeugNamen753(t, m); !reflect.DeepEqual(got, alle) {
		t.Errorf("delegierend: %v", got)
	}
	// Jedes Werkzeug: alle vier Hinweise ausdrücklich, Eingabe-Schema als Objekt.
	for _, x := range r["tools"].([]any) {
		w := x.(map[string]any)
		h, _ := w["annotations"].(map[string]any)
		for _, k := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
			if _, da := h[k]; !da {
				t.Errorf("%s: Hinweis %s fehlt", w["name"], k)
			}
		}
		schema, _ := w["inputSchema"].(map[string]any)
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s: inputSchema %v", w["name"], schema)
		}
		lesend := map[string]bool{"boards": true, "board_lesen": true, "karte_zeigen": true, "termine_lesen": true,
			"karten_suchen": true, "board_verlauf": true, "mitglieder": true, "anhang_lesen": true}[w["name"].(string)]
		if h["readOnlyHint"] != lesend || h["destructiveHint"] != false || h["openWorldHint"] != false {
			t.Errorf("%s: Hinweise %v", w["name"], h)
		}
	}
	// Viewer-Token: nur die lesenden.
	_, m = e.mcpRufe(t, e.leser, "", `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{`+meta753()+`}}`, nil)
	if got := werkzeugNamen753(t, m); !reflect.DeepEqual(got, []string{"anhang_lesen", "board_lesen", "board_verlauf", "boards", "karte_zeigen", "karten_suchen", "mitglieder", "termine_lesen"}) {
		t.Errorf("viewer: %v", got)
	}
}

// Rollen-Matrix aus #752 noch einmal, über /mcp: dieselben Rechte wie über
// OpenAPI (ADR-0139 §6.5). Erlaubt = Ergebnis ohne isError; verboten =
// isError (Schritt B) bzw. 403 schon in Schritt A (Vorschlags-Token).
func TestAgent753_RollenMatrixUeberMCP(t *testing.T) {
	e := newEnv671(t, true)
	ctx := t.Context()
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna', 'anna@firma.de', 'Anna', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	anlegen := func(a storage.AgentTokenAnlage) string {
		k, _, err := e.repo.Anlegen(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	personEditor := anlegen(storage.AgentTokenAnlage{Name: "anna-b1", BoardID: "b1", UserID: "u-anna", Role: storage.AgentRolleEditor, CreatedBy: "cli"})
	personViewer := anlegen(storage.AgentTokenAnlage{Name: "anna-b1-lesen", BoardID: "b1", UserID: "u-anna", Role: storage.AgentRolleViewer, CreatedBy: "cli"})
	personOwner := anlegen(storage.AgentTokenAnlage{Name: "anna-b2", BoardID: "b2", UserID: "u-anna", Role: storage.AgentRolleEditor, CreatedBy: "cli"})
	mehrEditor, mehrViewer := mehrereTokens754(t, e.repo)
	type zelle struct{ token, person, board, name string }
	aufruf := map[string]zelle{
		"projekt-editor/eigen":          {e.projekt, "", "b1", "projekt"},
		"projekt-editor/fremd":          {e.projekt, "", "b3", "projekt"},
		"projekt-viewer/eigen":          {e.leser, "", "b1", "leser"},
		"projekt-viewer/fremd":          {e.leser, "", "b3", "leser"},
		"person-editor/eigen":           {personEditor, "", "b1", "anna-b1"},
		"person-editor/fremd":           {personEditor, "", "b3", "anna-b1"},
		"person-viewer/eigen":           {personViewer, "", "b1", "anna-b1-lesen"},
		"person-owner/eigen":            {personOwner, "", "b2", "anna-b2"},
		"delegiert-editor/eigen":        {e.token, "anna@firma.de", "b1", "open-webui"},
		"delegiert-editor/fremd":        {e.token, "anna@firma.de", "b3", "open-webui"},
		"delegiert-viewer/eigen":        {e.token, "cora@extern.de", "b1", "open-webui"},
		"vorschlag/eigen":               {e.vorschlg, "", "b1", "recherche"},
		"mehrere-editor/liste":          {mehrEditor, "", "b1", "anna-mehrere-0"},
		"mehrere-editor/nicht-in-liste": {mehrEditor, "", "b2", "anna-mehrere-0"},
		"mehrere-editor/fremd":          {mehrEditor, "", "b3", "anna-mehrere-0"},
		"mehrere-viewer/liste":          {mehrViewer, "", "b1", "anna-mehrere-1"},
	}
	stand := e.stand755(t)
	argumente := func(name, board string) string {
		mitBoard := map[string]string{
			"board_lesen":     `{"boardId":"` + board + `"}`,
			"karte_zeigen":    `{"boardId":"` + board + `","karte":"1","format":"json"}`,
			"karte_anlegen":   `{"boardId":"` + board + `","titel":"Aus der MCP-Matrix"}`,
			"kommentieren":    `{"boardId":"` + board + `","karte":"1","text":"MCP-Matrix"}`,
			"verschieben":     `{"boardId":"` + board + `","karte":"1","spalte":"Ready"}`,
			"finding_anlegen": `{"boardId":"` + board + `","titel":"Befund aus der MCP-Matrix","typ":"K","prioritaet":"P2"}`,
			"termin_anlegen":  `{"boardId":"` + board + `","titel":"Termin aus der MCP-Matrix","datum":"2026-10-08"}`,
			// Karte #755
			"karten_suchen": `{"q":"Karte"}`,
			"board_verlauf": `{"boardId":"` + board + `"}`,
			"mitglieder":    `{"boardId":"` + board + `"}`,
			"anhang_lesen":  `{"boardId":"` + board + `","karte":"1"}`,
		}
		if a, ok := mitBoard[name]; ok {
			return a
		}
		return `{}`
	}
	pfade := map[string]string{}
	for _, z := range zusage752 {
		pfade[z.name] = z.pfad
	}
	for _, z := range zusage752 {
		for i, c := range zellen752 {
			a := aufruf[c]
			soll := matrix752[z.name][i]
			args := argumente(z.name, a.board)
			if _, neu := koerper755[z.name]; neu {
				args = mcpArgumente755(t, pfade[z.name], z.name, a.board, a.name, stand)
			}
			e.zuruecksetzen755(t)
			// Karte #757: Lösch-Werkzeuge mit dem Code des Prüf-Werkzeugs
			// (über OpenAPI geholt — er hängt am Zugang, nicht am Eingang);
			// die Prüf-Werkzeuge mit ihren Pfad-Parametern.
			args = e.mitCode757(t, a.token, a.person, pfade[z.name], a.board, a.name, args)
			if strings.HasSuffix(z.name, "_loeschen_pruefen") {
				args = mcpArgumente755(t, pfade[z.name], z.name, a.board, a.name, stand)
			}
			s, m := e.mcpWerkzeug(t, a.token, a.person, z.name, args)
			if c == "vorschlag/eigen" {
				if s != http.StatusForbidden {
					t.Errorf("%s · %s: %d, erwartet 403 schon in Schritt A", z.name, c, s)
				}
				continue
			}
			// Ein „nein“ zählt nur in seiner erwarteten Form (Sitzungs-QA
			// 08.10., V-1): ein Viewer-Token sieht schreibende Werkzeuge
			// nicht → JSON-RPC „unknown tool“; sonst lehnt Schritt B ab →
			// isError mit der Meldung der Werkzeuge. Eine Ratenbremse oder
			// falsche Argumente färbten die Zelle sonst grün.
			if f, ok := m["error"].(map[string]any); ok {
				meldung, _ := f["message"].(string)
				unsichtbar := soll == nein52 && (strings.HasPrefix(c, "projekt-viewer/") || strings.HasPrefix(c, "person-viewer/") || strings.HasPrefix(c, "mehrere-viewer/") ||
					// Nicht für Open WebUI freigegeben: tools/list zeigt es dem delegierenden Token nicht.
					strings.HasPrefix(c, "delegiert-") && !z.delegiert ||
					// Karte #757: Board-Struktur nur mit Person — ein Projekt-Token sieht sie nicht.
					strings.HasPrefix(c, "projekt-") && nurMitPerson757[z.name])
				if !unsichtbar || !strings.Contains(meldung, "unknown tool") {
					t.Errorf("%s · %s: JSON-RPC-Fehler %v (HTTP %d)", z.name, c, f, s)
				}
				continue
			}
			if s != http.StatusOK {
				t.Errorf("%s · %s: HTTP %d %v", z.name, c, s, m)
				continue
			}
			fehler, text, strukt := ergebnis753(t, m)
			switch {
			case soll == ok752 && (fehler || strings.TrimSpace(text) == ""):
				t.Errorf("%s · %s: erwartet Erfolg, isError=%v — %.160s", z.name, c, fehler, text)
			case soll == ok752 && z.methode == "GET" && z.name != "karte_zeigen" && strukt == nil:
				t.Errorf("%s · %s: lesend ohne strukturierte Daten — %.160s", z.name, c, text)
			case soll == nein52 && (!fehler || !strings.Contains(text, "nicht erlaubt")):
				t.Errorf("%s · %s: erwartet Ablehnung durch die Positivliste, isError=%v — %.160s", z.name, c, fehler, text)
			}
		}
	}
}

// Gleiche Wirkung (ADR-0139 §6.5): board_lesen liefert über MCP dieselben
// Daten wie über OpenAPI; ein Kommentar über MCP trägt denselben Autor und
// Akteur wie einer über OpenAPI.
func TestAgent753_GleicheWirkungWieOpenAPI(t *testing.T) {
	e := newEnv671(t, true)
	_, oa := e.rufe(t, e.token, "anna@firma.de", "GET", "/werkzeuge/boards/b1", "")
	_, m := e.mcpWerkzeug(t, e.token, "anna@firma.de", "board_lesen", `{"boardId":"b1"}`)
	fehler, _, strukt := ergebnis753(t, m)
	var erwartet any
	_ = json.Unmarshal(oa, &erwartet)
	if fehler || !reflect.DeepEqual(strukt, erwartet) {
		t.Fatalf("board_lesen über MCP ≠ OpenAPI:\n%v\n%v", strukt, erwartet)
	}
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten/1/kommentare", `{"text":"über OpenAPI"}`); s != http.StatusOK {
		t.Fatalf("OpenAPI kommentieren: %d %s", s, b)
	}
	if _, m := e.mcpWerkzeug(t, e.token, "anna@firma.de", "kommentieren", `{"boardId":"b1","karte":"1","text":"über MCP"}`); func() bool { f, _, _ := ergebnis753(t, m); return f }() {
		t.Fatalf("MCP kommentieren: %v", m)
	}
	var stand struct {
		Board struct {
			Sprints []struct {
				Cards []struct {
					DisplayID int `json:"displayId"`
					Comments  []map[string]any
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	_ = json.Unmarshal([]byte(e.stand(t, "b1")), &stand)
	var kommentare []map[string]any
	for _, l := range stand.Board.Sprints {
		for _, k := range l.Cards {
			if k.DisplayID == 1 {
				kommentare = k.Comments
			}
		}
	}
	if len(kommentare) != 2 {
		t.Fatalf("Kommentare: %v", kommentare)
	}
	// Alles außer id, Text und Zeit muss gleich sein — Autor und Stempel der
	// Person kommen aus postOps, egal über welchen Eingang.
	ohne := func(k map[string]any) map[string]any {
		aus := map[string]any{}
		for f, v := range k {
			if f != "id" && f != "text" && f != "created" {
				aus[f] = v
			}
		}
		return aus
	}
	if a, b := ohne(kommentare[0]), ohne(kommentare[1]); !reflect.DeepEqual(a, b) || a["author"] == nil {
		t.Errorf("Kommentar über OpenAPI %v ≠ über MCP %v", a, b)
	}
}

// Negativtest auf /mcp (ADR-0139 §6.4).
func TestAgent753_Negativ(t *testing.T) {
	e := newEnv671(t, true)
	liste := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + meta753() + `}}`

	// Unbekanntes Werkzeug → Protokollfehler.
	if _, m := e.mcpWerkzeug(t, e.projekt, "", "board_loeschen", `{}`); m["error"] == nil {
		t.Errorf("unbekanntes Werkzeug: %v", m)
	}
	// Falsche Methoden: GET/DELETE → 405, PUT → abgelehnt.
	for _, methode := range []string{"GET", "DELETE", "PUT", "PATCH"} {
		s, _ := e.rufe(t, e.projekt, "", methode, "/mcp", "")
		if (methode == "GET" || methode == "DELETE") && s != http.StatusMethodNotAllowed || s < 400 {
			t.Errorf("%s /mcp: %d", methode, s)
		}
	}
	// Mcp-Name passt nicht zum Inhalt / fehlt → 400 -32020.
	for _, kopf := range []map[string]string{{"Mcp-Name": "boards"}, {"Mcp-Name": ""}, {"Mcp-Method": "tools/list"}} {
		s, m := e.mcpRufe(t, e.projekt, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"board_lesen","arguments":{"boardId":"b1"},`+meta753()+`}}`, kopf)
		f, _ := m["error"].(map[string]any)
		if s != http.StatusBadRequest || f["code"] != float64(-32020) {
			t.Errorf("Kopfzeilen %v: %d %v", kopf, s, m)
		}
	}
	// Fremdes Board → isError, mit der Meldung der Werkzeuge.
	_, m := e.mcpWerkzeug(t, e.projekt, "", "board_lesen", `{"boardId":"b3"}`)
	if f, text, _ := ergebnis753(t, m); !f || !strings.Contains(text, "nicht erlaubt") {
		t.Errorf("fremdes Board: %v %q", f, text)
	}
	// Ein Pfad-Parameter, der den Pfad verlässt, findet kein anderes Ziel.
	_, m = e.mcpWerkzeug(t, e.projekt, "", "board_lesen", `{"boardId":"b1/karten/1"}`)
	if f, _, _ := ergebnis753(t, m); !f {
		t.Errorf("boardId mit Schrägstrich: %v", m)
	}
	// Delegierend ohne JWT: tools/list ja, tools/call nein (Schritt B).
	_, m = e.mcpWerkzeug(t, e.token, "", "board_lesen", `{"boardId":"b1"}`)
	if f, _, _ := ergebnis753(t, m); !f {
		t.Errorf("delegierend ohne JWT: %v", m)
	}
	// Service-Board nie.
	_, m = e.mcpWerkzeug(t, e.token, "anna@firma.de", "board_lesen", `{"boardId":"`+storage.ServiceBoardID+`"}`)
	if f, _, _ := ergebnis753(t, m); !f {
		t.Errorf("Service-Board: %v", m)
	}
	// Vorschlags-Token → 403 vor dem Body.
	if s, _ := e.mcpRufe(t, e.vorschlg, "", liste, nil); s != http.StatusForbidden {
		t.Errorf("Vorschlag: %d", s)
	}
	// Fremder Origin → 403; die eigene Board-Adresse ist erlaubt.
	if s, _ := e.mcpRufe(t, e.projekt, "", liste, map[string]string{"Origin": "https://boese.example"}); s != http.StatusForbidden {
		t.Errorf("fremder Origin: %d", s)
	}
	if s, _ := e.mcpRufe(t, e.projekt, "", liste, map[string]string{"Origin": basis671}); s != http.StatusOK {
		t.Errorf("eigener Origin: %d", s)
	}
	// Unbekannte Version → 400 mit JSON-RPC-Fehler -32022 samt Liste.
	s, m := e.mcpRufe(t, e.projekt, "", liste, map[string]string{"MCP-Protocol-Version": "1900-01-01"})
	f, _ := m["error"].(map[string]any)
	if s != http.StatusBadRequest || f["code"] != float64(-32022) || f["data"] == nil {
		t.Errorf("unbekannte Version: %d %v", s, m)
	}
	// Ohne Token → 401; ohne Agenten-Principal (Mux direkt) → 404.
	resp, err := http.Post(e.agent.URL+"/mcp", "application/json", strings.NewReader(liste))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("ohne Token: %d", resp.StatusCode)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(liste)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("ohne Agenten-Principal: %d", rec.Code)
	}
	// Zu großer Body.
	if s, _ := e.mcpRufe(t, e.projekt, "", `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"x":"`+strings.Repeat("a", 70<<10)+`",`+meta753()+`}}`, nil); s != http.StatusRequestEntityTooLarge {
		t.Errorf("großer Body: %d", s)
	}
}

// Fassung 2025-11-25 (initialize, keine Sitzung).
func TestAgent753_AlteFassung(t *testing.T) {
	e := newEnv671(t, true)
	s, m := e.mcpRufe(t, e.projekt, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`,
		map[string]string{"MCP-Protocol-Version": "", "Mcp-Method": ""})
	r, _ := m["result"].(map[string]any)
	if s != http.StatusOK || r["protocolVersion"] != "2025-11-25" {
		t.Fatalf("initialize: %d %v", s, m)
	}
	alt := map[string]string{"MCP-Protocol-Version": "2025-11-25", "Mcp-Method": ""}
	if s, _ := e.mcpRufe(t, e.projekt, "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, alt); s != http.StatusAccepted {
		t.Errorf("initialized: %d", s)
	}
	_, m = e.mcpRufe(t, e.projekt, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, alt)
	// Karte #757: Ein Projekt-Token hat keine Person — die Werkzeuge der
	// Board-Struktur zeigt die Liste ihm nicht.
	if len(werkzeugNamen753(t, m)) != len(zusage752)-len(nurMitPerson757) {
		t.Errorf("tools/list alt: %v", m)
	}
	_, m = e.mcpRufe(t, e.projekt, "", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"karte_anlegen","arguments":{"boardId":"b1","titel":"Über die alte Fassung"}}}`, alt)
	if f, text, _ := ergebnis753(t, m); f || !strings.Contains(text, "angelegt") {
		t.Errorf("tools/call alt: %v %q", f, text)
	}
	if !strings.Contains(e.stand(t, "b1"), "Über die alte Fassung") {
		t.Error("Karte nicht angelegt")
	}
}

// Sitzungs-QA 08.10. (S-1): Die Listen-Ratenbremse zählt beim delegierenden
// Token je Person (JWT), nicht für alle Personen hinter Open WebUI gemeinsam.
func TestAgent753_ListenBremseJePerson(t *testing.T) {
	e := newEnv671(t, true)
	liste := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + meta753() + `}}`
	gebremst := false
	for i := 0; i < 130; i++ {
		if s, _ := e.mcpRufe(t, e.token, "anna@firma.de", liste, nil); s == http.StatusTooManyRequests {
			gebremst = true
			break
		}
	}
	if !gebremst {
		t.Fatal("anna wurde nie gebremst — Messung trennt nicht")
	}
	if s, m := e.mcpRufe(t, e.token, "cora@extern.de", liste, nil); s != http.StatusOK {
		t.Fatalf("cora nach annas Kontingent: %d %v — die Bremse zählt nicht je Person", s, m)
	}
}

// Sitzungs-QA 08.10. (S-2): JSON-RPC-Batches werden abgelehnt (gäbe es sonst
// an der Listen-Ratenbremse vorbei).
func TestAgent753_KeinBatch(t *testing.T) {
	e := newEnv671(t, true)
	eins := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	s, m := e.mcpRufe(t, e.projekt, "", "["+eins+","+eins+"]", map[string]string{"MCP-Protocol-Version": "", "Mcp-Method": ""})
	f, _ := m["error"].(map[string]any)
	if s != http.StatusBadRequest || f["code"] != float64(-32600) {
		t.Fatalf("Batch: %d %v", s, m)
	}
}

// Sitzungs-QA 08.10. (K-2): Eine Werkzeug-Antwort über der Obergrenze kommt
// als isError an, nicht als leeres Erfolgs-Ergebnis.
func TestAgent753_ZuGrosseAntwort(t *testing.T) {
	e := newEnv671(t, true)
	zurueck := api.SetzeMCPAntwortMaxFuerTest(200)
	defer zurueck()
	_, m := e.mcpWerkzeug(t, e.projekt, "", "board_lesen", `{"boardId":"b1"}`)
	if f, text, _ := ergebnis753(t, m); !f || !strings.Contains(text, "zu groß") {
		t.Fatalf("zu große Antwort: isError=%v %q", f, text)
	}
}

// Verify 08.10. (W-1): Listen mit ungültigem JWT schreiben keine Logzeile —
// der Schlüssel der Listen-Bremse prüft das JWT still.
func TestAgent753_ListeMitUngueltigemJWTOhneLog(t *testing.T) {
	e := newEnv671(t, true)
	var zeilen int
	zurueck := api.SetzeOWUILogFuerTest(func(string, ...any) { zeilen++ })
	defer zurueck()
	liste := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + meta753() + `}}`
	for i := 0; i < 5; i++ {
		if s, m := e.mcpRufe(t, e.token, "", liste, map[string]string{"X-OpenWebUI-User-Jwt": "kaputt.kaputt.kaputt"}); s != http.StatusOK {
			t.Fatalf("tools/list mit ungültigem JWT: %d %v", s, m)
		}
	}
	if zeilen != 0 {
		t.Fatalf("%d Logzeilen für 5 Listen mit ungültigem JWT, erwartet 0", zeilen)
	}
	// Gegenstück: ein tools/call mit ungültigem JWT protokolliert weiter.
	if _, m := e.mcpRufe(t, e.token, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"boards","arguments":{},`+meta753()+`}}`,
		map[string]string{"X-OpenWebUI-User-Jwt": "kaputt.kaputt.kaputt"}); func() bool { f, _, _ := ergebnis753(t, m); return !f }() || zeilen == 0 {
		t.Fatalf("tools/call mit ungültigem JWT: isError erwartet und eine Logzeile (zeilen=%d) %v", zeilen, m)
	}
}
