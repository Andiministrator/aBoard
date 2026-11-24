package api_test

// Sprint-80-Nacharbeit (F-1461, F-1447, F-1434, F-1442).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
)

// agentToken legt über die Selbstbedienung ein Agenten-Token für Anna an.
func agentTokenFuerAnna(t *testing.T, e *env608, name string) string {
	t.Helper()
	if s, b := e.anfrage(t, e.mensch, "PUT", "/admin/boards/b1/agent-selbstbedienung", "admin@firma.de", `{"an":true}`); s != http.StatusOK {
		t.Fatalf("Schalter: %d %s", s, b)
	}
	s, b := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "anna@firma.de", `{"board":"b1","name":"`+name+`"}`)
	if s != http.StatusCreated {
		t.Fatalf("Token: %d %s", s, b)
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Token == "" {
		t.Fatalf("Token lesen: %v %s", err, b)
	}
	return out.Token
}

func kommentareVonK1(t *testing.T, e *env608) []map[string]any {
	t.Helper()
	s, b := e.anfrage(t, e.mensch, "GET", "/boards/b1", "anna@firma.de", "")
	if s != http.StatusOK {
		t.Fatalf("Board lesen: %d %s", s, b)
	}
	var st struct {
		Board struct {
			Sprints []struct {
				Cards []struct {
					ID       string           `json:"id"`
					Comments []map[string]any `json:"comments"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st.Board.Sprints[0].Cards[0].Comments
}

// F-1461: comment.edit über den Agentenpfad — der Server stempelt `edited`
// (ein mitgeschickter Wert fällt weg), der eigene Kommentar geht, der eines
// Menschen nicht (422 fremder-kommentar).
func TestAgent639_CommentEditStempelEigenUndFremd(t *testing.T) {
	e := newEnv608(t)
	feste := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	defer api.SetzeOpsNowFuerTest(func() time.Time { return feste })()
	token := agentTokenFuerAnna(t, e, "claude")

	// Ein Kommentar von Anna (Mensch), einer vom Agenten.
	mensch := `{"ops":[{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"c-anna","author":"anna@firma.de","text":"von anna","created":"2026-10-03"}}}]}`
	if s, b := e.anfrage(t, e.mensch, "POST", "/boards/b1/ops", "anna@firma.de", mensch); s != http.StatusOK {
		t.Fatalf("Anna kommentiert: %d %s", s, b)
	}
	agent := `{"ops":[{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"c-agent","text":"von agent","created":"2026-10-03"}}}]}`
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", token, agent, "X-Aboard-Session", "s1"); s != http.StatusOK {
		t.Fatalf("Agent kommentiert: %d %s", s, b)
	}

	// Eigener Kommentar: erlaubt, `edited` vom Server — der mitgeschickte
	// Wert (falsche Schreibweise, gefälscht) fällt weg.
	eigen := `{"ops":[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c-agent","text":"neu","Edited":"1999-01-01T00:00:00.000Z"}}]}`
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", token, eigen, "X-Aboard-Session", "s1"); s != http.StatusOK {
		t.Fatalf("Agent bearbeitet eigenen Kommentar: %d %s", s, b)
	}
	var gefunden bool
	for _, c := range kommentareVonK1(t, e) {
		if c["id"] != "c-agent" {
			continue
		}
		gefunden = true
		if c["text"] != "neu" {
			t.Errorf("Text nicht übernommen: %v", c)
		}
		if got, _ := c["edited"].(string); got != feste.Format("2006-01-02T15:04:05.000Z") {
			t.Errorf("edited = %v, erwartet Server-Zeit %s", c["edited"], feste.Format("2006-01-02T15:04:05.000Z"))
		}
	}
	if !gefunden {
		t.Fatal("Agenten-Kommentar fehlt")
	}

	// Fremder (menschlicher) Kommentar: 422, unverändert.
	fremd := `{"ops":[{"type":"comment.edit","payload":{"cardId":"k1","commentId":"c-anna","text":"gekapert"}}]}`
	s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", token, fremd, "X-Aboard-Session", "s1")
	if s != http.StatusUnprocessableEntity || !strings.Contains(string(b), "fremder-kommentar") {
		t.Fatalf("Agent bearbeitet fremden Kommentar: %d %s", s, b)
	}
	for _, c := range kommentareVonK1(t, e) {
		if c["id"] == "c-anna" && (c["text"] != "von anna" || c["edited"] != nil) {
			t.Errorf("fremder Kommentar verändert: %v", c)
		}
	}
}

// F-1447: Wiederholung derselben comment.edit-Op (gleiche Client-id) nach
// verlorener Antwort — auch Wochen später in Uhrzeit, `edited` ist
// Request-Zeit — wird als wiederholt quittiert, nicht mit 400 abgelehnt. Über
// den Agentenpfad, der die Wiederholung real braucht.
func TestAgent639_CommentEditWiederholungMitGleicherID(t *testing.T) {
	e := newEnv608(t)
	t1 := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	jetzt := t1
	defer api.SetzeOpsNowFuerTest(func() time.Time { return jetzt })()
	token := agentTokenFuerAnna(t, e, "claude")

	agent := `{"ops":[{"type":"comment.add","payload":{"cardId":"k1","comment":{"id":"c1","text":"alt","created":"2026-10-03"}}}]}`
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", token, agent, "X-Aboard-Session", "s1"); s != http.StatusOK {
		t.Fatalf("Vorbereitung: %d %s", s, b)
	}
	edit := `{"ops":[{"id":"edit-1","type":"comment.edit","payload":{"cardId":"k1","commentId":"c1","text":"neu"}}]}`
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", token, edit, "X-Aboard-Session", "s1"); s != http.StatusOK {
		t.Fatalf("erster Versuch: %d %s", s, b)
	}
	jetzt = t1.Add(1500 * time.Millisecond)
	s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", token, edit, "X-Aboard-Session", "s1")
	if s != http.StatusOK || !strings.Contains(string(b), `"wiederholt":true`) {
		t.Fatalf("Wiederholung: %d %s, erwartet 200 mit wiederholt", s, b)
	}
	// Dieselbe id mit anderem Text bleibt ein Widerspruch.
	anders := strings.Replace(edit, `"neu"`, `"anders"`, 1)
	if s, b := e.anfrage(t, e.agent, "POST", "/boards/b1/ops", token, anders, "X-Aboard-Session", "s1"); s != http.StatusBadRequest {
		t.Fatalf("gleiche id, anderer Inhalt: %d %s, erwartet 400", s, b)
	}
}

// F-1434: Widerruf per id (bevorzugt); die Namensform läuft weiter. Sind
// mehrere aktive Tokens unter einem Namen (hier durch Entfernen des
// Eindeutigkeits-Index nachgestellt, wie es F-1335 lockern könnte), trifft
// der Namenswiderruf KEINES, sondern 409; die id trifft genau eines.
func TestAdminAgentTokens_WiderrufPerIDUndName(t *testing.T) {
	e := newEnv608(t)
	legeAn := func(name string) string {
		t.Helper()
		s, b := e.anfrage(t, e.mensch, "POST", "/admin/agent-tokens", "admin@firma.de", `{"name":"`+name+`","board":"b1","role":"viewer"}`)
		if s != http.StatusCreated {
			t.Fatalf("anlegen %s: %d %s", name, s, b)
		}
		var out struct {
			Agent struct {
				ID string `json:"id"`
			} `json:"agent"`
		}
		if err := json.Unmarshal(b, &out); err != nil || out.Agent.ID == "" {
			t.Fatalf("id lesen: %v %s", err, b)
		}
		return out.Agent.ID
	}
	idA, idB := legeAn("agent-a"), legeAn("agent-b")

	// Per id.
	if s, b := e.anfrage(t, e.mensch, "DELETE", "/admin/agent-tokens/"+idA, "admin@firma.de", ""); s != http.StatusOK || !strings.Contains(string(b), `"widerrufen":"agent-a"`) {
		t.Fatalf("Widerruf per id: %d %s", s, b)
	}
	if s, _ := e.anfrage(t, e.mensch, "DELETE", "/admin/agent-tokens/"+idA, "admin@firma.de", ""); s != http.StatusNotFound {
		t.Fatalf("zweiter Widerruf per id: %d, erwartet 404", s)
	}
	// Namensform weiter gültig.
	if s, b := e.anfrage(t, e.mensch, "DELETE", "/admin/agent-tokens/agent-b", "admin@firma.de", ""); s != http.StatusOK || !strings.Contains(string(b), `"widerrufen":"agent-b"`) {
		t.Fatalf("Widerruf per Name: %d %s", s, b)
	}
	_ = idB

	// Mehrdeutig: Index weg, zwei aktive „zwilling“.
	if _, err := e.db.ExecContext(context.Background(), `DROP INDEX agent_tokens_name_aktiv`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t-1", "t-2"} {
		if _, err := e.db.ExecContext(context.Background(),
			`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at) VALUES (?, 'zwilling', 'b1', 'viewer', ?, '2026-10-03T00:00:00.000Z')`, id, "h-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if s, b := e.anfrage(t, e.mensch, "DELETE", "/admin/agent-tokens/zwilling", "admin@firma.de", ""); s != http.StatusConflict {
		t.Fatalf("Namenswiderruf bei Mehrdeutigkeit: %d %s, erwartet 409", s, b)
	}
	var aktiv int
	if err := e.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM agent_tokens WHERE name='zwilling' AND revoked_at IS NULL`).Scan(&aktiv); err != nil || aktiv != 2 {
		t.Fatalf("nach 409 aktiv=%d err=%v, erwartet 2 (nichts widerrufen)", aktiv, err)
	}
	if s, b := e.anfrage(t, e.mensch, "DELETE", "/admin/agent-tokens/t-2", "admin@firma.de", ""); s != http.StatusOK {
		t.Fatalf("Widerruf per id bei Zwillingen: %d %s", s, b)
	}
	if err := e.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM agent_tokens WHERE id='t-1' AND revoked_at IS NULL`).Scan(&aktiv); err != nil || aktiv != 1 {
		t.Fatalf("t-1 muss aktiv bleiben: %d %v", aktiv, err)
	}
}
