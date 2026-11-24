package api_test

// Karte #754 (ADR-0139 §5): Personen-Token für mehrere Boards — Anlage über
// Selbstbedienung und Admin-Weg, Werkzeug `boards` zeigt nur die Liste,
// rohe Wege bleiben zu.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestAgent754_SelbstbedienungUndAdmin(t *testing.T) {
	e := newEnv608(t)
	// Selbstbedienung aus → auch „alle“ abgelehnt (die Liste wäre leer).
	if s, b := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "anna@firma.de", `{"alle_boards":true,"name":"anna-mehr"}`); s != http.StatusForbidden {
		t.Fatalf("Schalter aus: %d %s", s, b)
	}
	if s, b := e.anfrage(t, e.mensch, "PUT", "/admin/boards/b1/agent-selbstbedienung", "admin@firma.de", `{"an":true}`); s != http.StatusOK {
		t.Fatalf("Schalter: %d %s", s, b)
	}
	s, b := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "anna@firma.de", `{"boards":["b1"],"name":"anna-mehr"}`)
	if s != http.StatusCreated {
		t.Fatalf("anlegen: %d %s", s, b)
	}
	var out struct {
		Token string `json:"token"`
		Agent struct {
			Mehrere bool     `json:"mehrere"`
			Boards  []string `json:"boards"`
			BoardID string   `json:"board_id"`
		} `json:"agent"`
	}
	_ = json.Unmarshal(b, &out)
	if !out.Agent.Mehrere || len(out.Agent.Boards) != 1 || out.Agent.Boards[0] != "b1" || out.Agent.BoardID != "" {
		t.Fatalf("Antwort: %s", b)
	}
	// Ein Board, das es nicht gibt, sieht aus wie eines ohne Rolle.
	if s, b := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "anna@firma.de", `{"boards":["b1","gibts-nicht"],"name":"anna-mehr2"}`); s != http.StatusForbidden {
		t.Fatalf("unbekanntes Board: %d %s", s, b)
	}
	// Liste: Board-Liste ohne Klartext.
	s, b = e.anfrage(t, e.mensch, "GET", "/users/me/agent-tokens", "anna@firma.de", "")
	if s != http.StatusOK || !strings.Contains(string(b), `"mehrere":true`) || !strings.Contains(string(b), `"boards":["b1"]`) {
		t.Fatalf("Liste: %d %s", s, b)
	}
	// Rohe Wege: 403 mit Grund.
	if s, b := e.anfrage(t, e.agent, "GET", "/boards/b1", out.Token, ""); s != http.StatusForbidden || !strings.Contains(string(b), "several boards") {
		t.Fatalf("roher Weg: %d %s", s, b)
	}
	// Admin-Weg mit Person und „alle“.
	s, b = e.anfrage(t, e.mensch, "POST", "/admin/agent-tokens", "admin@firma.de", `{"name":"fuer-anna-alle","role":"viewer","user":"anna@firma.de","alle_boards":true}`)
	if s != http.StatusCreated || !strings.Contains(string(b), `"boards":["b1"]`) {
		t.Fatalf("Admin alle: %d %s", s, b)
	}
	// Admin-Weg ohne Person: 400.
	if s, b := e.anfrage(t, e.mensch, "POST", "/admin/agent-tokens", "admin@firma.de", `{"name":"ohne-person","role":"editor","boards":["b1"]}`); s != http.StatusBadRequest {
		t.Fatalf("ohne Person: %d %s", s, b)
	}
	// Personen-Widerruf erfasst auch Mehr-Board-Tokens.
	if s, b := e.anfrage(t, e.mensch, "POST", "/admin/users/u-anna/agent-tokens/revoke", "admin@firma.de", ""); s != http.StatusOK || !strings.Contains(string(b), `"widerrufen":2`) {
		t.Fatalf("Personen-Widerruf: %d %s", s, b)
	}
}

// Das Werkzeug `boards` zeigt nur die Boards der Liste, auf denen die Person
// noch eine persönliche Rolle hat — nicht b2, obwohl sie dort owner ist.
func TestAgent754_BoardsZeigtNurDieListe(t *testing.T) {
	e := newEnv671(t, true)
	if _, err := e.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna', 'anna@firma.de', 'Anna', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	editor, _ := mehrereTokens754(t, e.repo)
	s, b := e.rufe(t, editor, "", "GET", "/werkzeuge/boards", "")
	var out struct {
		Person string `json:"person"`
		Boards []struct {
			BoardID       string `json:"boardId"`
			Rolle         string `json:"rolle"`
			DarfSchreiben bool   `json:"darfSchreiben"`
		} `json:"boards"`
	}
	if err := json.Unmarshal(b, &out); err != nil || s != http.StatusOK {
		t.Fatalf("%d %s", s, b)
	}
	if out.Person != "anna@firma.de" || len(out.Boards) != 1 || out.Boards[0].BoardID != "b1" || out.Boards[0].Rolle != storage.AgentRolleEditor || !out.Boards[0].DarfSchreiben {
		t.Fatalf("boards: %s", b)
	}
	// Schreiben über das Werkzeug wirkt und trägt den Stempel der Person.
	s, b = e.rufe(t, editor, "", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"Von mehreren Boards"}`)
	if s != http.StatusOK {
		t.Fatalf("karte_anlegen: %d %s", s, b)
	}
	if n := strings.Count(e.stand(t, "b1"), "Von mehreren Boards"); n != 1 {
		t.Fatalf("Karte %d-mal im Stand", n)
	}
}

// Verify #754, V-1: Die Body-Grenze der Anlage trägt eine volle Board-Liste.
// 250 lange IDs (~10 KiB) kommen beim Server an und scheitern fachlich an der
// Grenze von 200 Boards — nicht schon am Lesen („invalid json body“), wie es
// bei 8 KiB der Fall war. Über 16 KiB scheitert das Lesen.
func TestAgent754_BodyGrenzeTraegtBoardListe(t *testing.T) {
	e := newEnv608(t)
	ids := func(n int) string {
		l := make([]string, n)
		for i := range l {
			l[i] = fmt.Sprintf(`"%036d"`, i)
		}
		return strings.Join(l, ",")
	}
	for _, f := range []struct {
		pfad, wer, body, soll string
	}{
		{"/admin/agent-tokens", "admin@firma.de", `{"name":"gross","role":"editor","user":"anna@firma.de","boards":[` + ids(250) + `]}`, "höchstens 200"},
		{"/admin/agent-tokens", "admin@firma.de", `{"name":"gross","role":"editor","user":"anna@firma.de","boards":[` + ids(450) + `]}`, "invalid json body"},
		{"/users/me/agent-tokens", "anna@firma.de", `{"name":"gross","role":"editor","boards":[` + ids(250) + `]}`, "höchstens 200"},
	} {
		s, b := e.anfrage(t, e.mensch, "POST", f.pfad, f.wer, f.body)
		if s != http.StatusBadRequest || !strings.Contains(string(b), f.soll) {
			t.Errorf("%s (%d Bytes): %d %s, erwartet 400 mit %q", f.pfad, len(f.body), s, b, f.soll)
		}
	}
}
