package api

// Kritiker Sprint 82 (K1-1, K1-2, K1-3) über die Agenten-Zugänge für Open
// WebUI (ADR-0133 §4/§5): interne Prüfungen mit gestellter Uhr.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// K1-1: Die Positivliste lässt für ein delegierendes Token nur die Werkzeuge zu.
func TestAgentK1_DelegiertNurWerkzeuge(t *testing.T) {
	p := &AgentPrincipal{Name: "open-webui", BoardID: "b1", Role: storage.AgentRolleEditor, OnBehalfOf: "anna@firma.de",
		Delegiert: true, tokenRolle: storage.AgentRolleEditor}
	for _, f := range [][2]string{
		{"POST", "/boards/b1/ops"}, {"GET", "/boards/b1"}, {"HEAD", "/boards/b1"}, {"GET", "/boards/b1/events"},
		{"GET", "/boards/b1/ich"}, {"POST", "/boards/b1/attachments/k1"}, {"GET", "/boards/b1/attachments/k1/a1"},
		{"DELETE", "/boards/b1/attachments/k1/a1"}, {"POST", "/boards/b1/board-attachments"},
		{"GET", "/boards/b1/board-attachments/a1"}, {"DELETE", "/boards/b1/board-attachments/a1"},
	} {
		if agentDarf(p, httptest.NewRequest(f[0], f[1], nil)) {
			t.Errorf("delegiert %s %s erlaubt", f[0], f[1])
		}
	}
	for _, f := range [][2]string{{"GET", "/werkzeuge/boards/b1"}, {"POST", "/werkzeuge/boards/b1/karten"}} {
		if !agentDarf(p, httptest.NewRequest(f[0], f[1], nil)) {
			t.Errorf("delegiert %s %s abgelehnt", f[0], f[1])
		}
	}
	// Gegenstück: dasselbe als Personen-Token (nicht delegierend) bleibt erlaubt.
	q := *p
	q.Delegiert = false
	if !agentDarf(&q, httptest.NewRequest("POST", "/boards/b1/ops", nil)) {
		t.Error("Personen-Token: ops abgelehnt")
	}
}

// Die Sperre der Ops, deren Erlaubnis am Akteur hängt, bleibt als zweite
// Hürde bestehen — auch wenn ein delegierendes Token postOps heute nur über
// die Werkzeuge erreicht.
func TestAgentK1_AkteurOpsGesperrt(t *testing.T) {
	deleg := &AgentPrincipal{Name: "open-webui", BoardID: "b1", Role: storage.AgentRolleEditor, OnBehalfOf: "bert@firma.de", Delegiert: true}
	person := &AgentPrincipal{Name: "annas-agent", BoardID: "b1", Role: storage.AgentRolleEditor, OnBehalfOf: "anna@firma.de"}
	for typ, payload := range map[string]string{
		"comment.edit": `{"cardId":"k1","commentId":"c1","text":"x"}`,
		"card.claim":   `{"cardId":"k1","session":"s1"}`,
		"card.release": `{"cardId":"k1"}`,
	} {
		if _, grund := agentPayload(typ, json.RawMessage(payload), deleg); !strings.Contains(grund, "delegating token") {
			t.Errorf("delegiert %s: grund %q", typ, grund)
		}
		if _, grund := agentPayload(typ, json.RawMessage(payload), person); grund != "" {
			t.Errorf("Personen-Token %s abgelehnt: %q", typ, grund)
		}
	}
}

// K1-2: personenAdresse nimmt nur eine schlichte Adresse.
func TestAgentK1_PersonenAdresse(t *testing.T) {
	for e, soll := range map[string]bool{
		"anna@firma.de":            true,
		"a.b+c@sub.firma.de":       true,
		"a@b@firma.de":             false, // zwei @ — die Domain-Freigabe läse firma.de
		`"x" <x@firma.de>`:         false, // Anzeigename
		"x <x@firma.de>":           false,
		"<x@firma.de>":             false,
		"x@firma.de (chef)":        false, // Kommentar
		"(chef)x@firma.de":         false,
		`"a b"@firma.de`:           false, // Quoting
		`"a@b"@firma.de`:           false,
		"anna @firma.de":           false,
		"anna@ firma.de":           false,
		"anna@firma.de ":           false, // geschütztes Leerzeichen
		"anna\t@firma.de":          false,
		"anna@firma.de\n":          false,
		"anna@firma.de,bert@x.de":  false,
		"anna@firma.de;bert@x.de":  false,
		"@firma.de":                false,
		"anna@":                    false,
		"anna":                     false,
		"":                         false,
		"anna@.firma.de":           false,
		"anna@firma.de.":           false,
		"anna@[127.0.0.1]":         false,
		"anna\\@firma.de@firma.de": false,
		"anna@firma.de\u0000":      false,
		"anna@firma.de​":           false, // nullbreites Leerzeichen: kein IsSpace, aber kein gültiges Zeichen
		"anna@fir\u0007ma.de":      false,
		"anna.@firma.de":           false,
		"anna..b@firma.de":         false,
		".anna@firma.de":           false,
		"anna@firma..de":           false,
		"anna@-firma.de":           true, // RFC-5322-Atom; DNS-Prüfung ist nicht Sache dieser Funktion
	} {
		if ist := personenAdresse(e); ist != soll {
			t.Errorf("personenAdresse(%q) = %v, erwartet %v", e, ist, soll)
		}
	}
	// Über das JWT: Die Prüfung normalisiert (klein, getrimmt) und lehnt dann ab.
	jetzt := time.Unix(1_800_000_000, 0).UTC()
	for _, email := range []string{"a@b@firma.de", `"X" <x@firma.de>`, "X <x@firma.de>", "anna @firma.de"} {
		tok := owuiTestJWT(t, owuiTestGeheimnis, map[string]any{"alg": "HS256", "typ": "JWT"},
			map[string]any{"iss": "open-webui", "email": email, "iat": jetzt.Unix(), "exp": jetzt.Unix() + 300})
		if _, _, err := pruefeOWUIJWT(tok, owuiTestGeheimnis, jetzt); err == nil {
			t.Errorf("JWT mit email %q angenommen", email)
		}
	}
}

// K1-3: Der Ablauf folgt nicht blind exp — höchstens iat + 300 s.
func TestAgentK1_JWTAblaufGekappt(t *testing.T) {
	jetzt := time.Unix(1_800_000_000, 0).UTC()
	kopf := map[string]any{"alg": "HS256", "typ": "JWT"}
	for name, f := range map[string]struct {
		iat, exp int64
		soll     time.Time
	}{
		"exp weit":   {jetzt.Unix() - 10, jetzt.Unix() + 3600, jetzt.Add(290 * time.Second)},
		"exp knapp":  {jetzt.Unix() - 10, jetzt.Unix() + 100, jetzt.Add(100 * time.Second)},
		"exp gleich": {jetzt.Unix(), jetzt.Unix() + 300, jetzt.Add(300 * time.Second)},
	} {
		tok := owuiTestJWT(t, owuiTestGeheimnis, kopf, map[string]any{"iss": "open-webui", "email": "anna@firma.de", "iat": f.iat, "exp": f.exp})
		_, ablauf, err := pruefeOWUIJWT(tok, owuiTestGeheimnis, jetzt)
		if err != nil || !ablauf.Equal(f.soll) {
			t.Errorf("%s: ablauf %v, erwartet %v (err %v)", name, ablauf, f.soll, err)
		}
	}
}
