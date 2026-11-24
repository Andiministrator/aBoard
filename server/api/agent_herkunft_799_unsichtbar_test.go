package api_test

// Karte #799, Nacharbeit zum Kritiker: unsichtbare Zeichen in Anbieter und
// Beleg (K-1) und eine unlesbare Einstellung boardpaar.seite (K-2) auf der
// Admin-API und der Selbstbedienung. Die CLI steht in
// main_agent_herkunft_799_test.go.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// unsichtbar799: Werte, die TrimSpace nicht entfernt und die keine Steuerzeichen
// der Klassen C0/C1 sind. Als Go-Escape geschrieben, nie als rohes Zeichen.
var unsichtbar799 = map[string]string{
	"ZWSP":                      "\u200b",
	"Braille leer":              "\u2800",
	"Hangul-Füller":             "\u3164",
	"Bidi-Umkehr":               "A\u202eB",
	"nur Unsichtbares+Leerraum": " \u200b ",
}

func koerper799(name, anbieter, beleg string) string {
	b, _ := json.Marshal(map[string]any{"name": name, "board": "b1", "role": "editor", "anbieter": anbieter, "beleg": beleg})
	return string(b)
}

func TestHerkunft799_UnsichtbareZeichenAbgelehnt(t *testing.T) {
	e := newEnv608(t)
	if s, b := e.anfrage(t, e.mensch, "PUT", "/admin/boards/b1/agent-selbstbedienung", "admin@firma.de", `{"an":true}`); s != http.StatusOK {
		t.Fatalf("Schalter: %d %s", s, b)
	}
	for _, seite := range []string{api.BoardpaarSeiteAussen, api.BoardpaarSeiteInnen} {
		setzeSeite799(t, e, seite)
		n := 0
		for fall, v := range unsichtbar799 {
			for feld, paar := range map[string][2]string{"anbieter": {v, "b"}, "beleg": {"A", v}} {
				n++
				name := fmt.Sprintf("u%d-", n)
				for weg, nutzer := range map[string]struct{ pfad, wer string }{
					"admin":  {"/admin/agent-tokens", "admin@firma.de"},
					"selbst": {"/users/me/agent-tokens", "anna@firma.de"},
				} {
					s, b := e.anfrage(t, e.mensch, "POST", nutzer.pfad, nutzer.wer, koerper799(name+weg, paar[0], paar[1]))
					if s != http.StatusBadRequest {
						t.Errorf("%s/%s/%s/%s: %d %s (400 erwartet)", seite, weg, fall, feld, s, b)
					}
				}
			}
		}
	}
	// Nichts angelegt.
	s, b := e.anfrage(t, e.mensch, "GET", "/admin/agent-tokens", "admin@firma.de", "")
	var liste []storage.AgentToken
	if s != http.StatusOK || json.Unmarshal(b, &liste) != nil || len(liste) != 0 {
		t.Errorf("abgelehnte Versuche haben Tokens angelegt: %d %s", s, b)
	}
}

// Kritiker K-2: Ist server_config nicht lesbar, wird die Pflicht nicht still
// erlassen (fail-closed): 503, nichts angelegt.
func TestHerkunft799_SeiteNichtLesbar503(t *testing.T) {
	e := newEnv608(t)
	if s, b := e.anfrage(t, e.mensch, "PUT", "/admin/boards/b1/agent-selbstbedienung", "admin@firma.de", `{"an":true}`); s != http.StatusOK {
		t.Fatalf("Schalter: %d %s", s, b)
	}
	// Gültige Angaben: nur die unlesbare Seite darf ablehnen.
	if _, err := e.db.Exec(`DROP TABLE server_config`); err != nil {
		t.Fatal(err)
	}
	for weg, nutzer := range map[string]struct{ pfad, wer string }{
		"admin":  {"/admin/agent-tokens", "admin@firma.de"},
		"selbst": {"/users/me/agent-tokens", "anna@firma.de"},
	} {
		s, b := e.anfrage(t, e.mensch, "POST", nutzer.pfad, nutzer.wer, koerper799("x-"+weg, "Anthropic", "beleg.md"))
		if s != http.StatusServiceUnavailable {
			t.Errorf("%s bei unlesbarer Seite: %d %s (503 erwartet)", weg, s, b)
		}
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM agent_tokens`).Scan(&n); err != nil || n != 0 {
		t.Errorf("trotz 503 angelegt: n=%d err=%v", n, err)
	}
}
