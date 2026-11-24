package api_test

// Karte #799 (ADR-0140 §8, Vertrag Push-Skript §11): Anbieter und Beleg des
// Trainingsausschlusses. Auf der Seite außen Pflicht auf JEDEM Weg, der ein
// Agenten-Token anlegt (Admin-API, Selbstbedienung, mit Person, für mehrere
// Boards); innen/aus optional; Länge und Steuerzeichen werden abgelehnt;
// die Token-Liste zeigt beide Felder. Die CLI steht in
// main_agent_herkunft_799_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

func setzeSeite799(t *testing.T, e *env608, seite string) {
	t.Helper()
	n := 0
	cfg := storage.NewConfigRepo(e.db, nil, func() string { n++; return fmt.Sprintf("audit799-%s-%d", seite, n) })
	if err := cfg.Set(context.Background(), api.KonfigBoardpaarSeite, seite, false, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestHerkunft799_AdminApi(t *testing.T) {
	e := newEnv608(t)
	admin := func(body string) (int, string) {
		s, b := e.anfrage(t, e.mensch, "POST", "/admin/agent-tokens", "admin@firma.de", body)
		return s, string(b)
	}
	// aus (Standard): ohne Angaben angelegt.
	if s, b := admin(`{"name":"ohne-aus","board":"b1","role":"editor"}`); s != http.StatusCreated {
		t.Fatalf("aus ohne Angaben: %d %s", s, b)
	}
	setzeSeite799(t, e, api.BoardpaarSeiteInnen)
	if s, b := admin(`{"name":"ohne-innen","board":"b1","role":"editor"}`); s != http.StatusCreated {
		t.Fatalf("innen ohne Angaben: %d %s", s, b)
	}

	setzeSeite799(t, e, api.BoardpaarSeiteAussen)
	for name, body := range map[string]string{
		"beides fehlt":       `{"name":"x1","board":"b1","role":"editor"}`,
		"beleg fehlt":        `{"name":"x2","board":"b1","role":"editor","anbieter":"Anthropic"}`,
		"anbieter fehlt":     `{"name":"x3","board":"b1","role":"editor","beleg":"knowledge/vertrag.md"}`,
		"nur Leerraum":       `{"name":"x4","board":"b1","role":"editor","anbieter":"  ","beleg":"\t "}`,
		"anbieter zu lang":   `{"name":"x5","board":"b1","role":"editor","anbieter":"` + strings.Repeat("a", 81) + `","beleg":"b"}`,
		"beleg zu lang":      `{"name":"x6","board":"b1","role":"editor","anbieter":"A","beleg":"` + strings.Repeat("b", 301) + `"}`,
		"steuerzeichen":      `{"name":"x7","board":"b1","role":"editor","anbieter":"A\u0007","beleg":"b"}`,
		"umbruch im beleg":   `{"name":"x8","board":"b1","role":"editor","anbieter":"A","beleg":"b\nc"}`,
		"mit person":         `{"name":"x9","board":"b1","role":"editor","user":"anna@firma.de"}`,
		"mehrere boards":     `{"name":"x10","role":"editor","user":"anna@firma.de","alle_boards":true}`,
		"rolle vorschlag":    `{"name":"x11","board":"b1","role":"vorschlag"}`,
		"rolle intern":       `{"name":"x12","board":"b1","role":"intern"}`,
		"liste mehrerer ids": `{"name":"x13","role":"viewer","user":"anna@firma.de","boards":["b1"]}`,
	} {
		s, b := admin(body)
		if s != http.StatusBadRequest {
			t.Errorf("%s: %d %s (400 erwartet)", name, s, b)
			continue
		}
		if strings.HasPrefix(name, "beides") || strings.HasPrefix(name, "mit person") || strings.HasPrefix(name, "mehrere") {
			if !strings.Contains(b, "anbieter") || !strings.Contains(b, "beleg") {
				t.Errorf("%s: Meldung nennt nicht beide Felder: %s", name, b)
			}
		}
	}
	// Mit Angaben (getrimmt): angelegt, in der Liste sichtbar.
	s, b := admin(`{"name":"mit-angaben","board":"b1","role":"editor","anbieter":"  Beispiel-Anbieter, Geschäftstarif ","beleg":" vertraege/vertrag.md "}`)
	if s != http.StatusCreated {
		t.Fatalf("mit Angaben: %d %s", s, b)
	}
	if s, b := admin(`{"name":"person-mit","board":"b1","role":"editor","user":"anna@firma.de","anbieter":"A","beleg":"B"}`); s != http.StatusCreated {
		t.Fatalf("mit Person: %d %s", s, b)
	}
	if s, b := admin(`{"name":"mehrere-mit","role":"editor","user":"anna@firma.de","alle_boards":true,"anbieter":"A","beleg":"B"}`); s != http.StatusCreated {
		t.Fatalf("mehrere Boards: %d %s", s, b)
	}
	s, b2 := e.anfrage(t, e.mensch, "GET", "/admin/agent-tokens", "admin@firma.de", "")
	var liste []storage.AgentToken
	if s != http.StatusOK || json.Unmarshal(b2, &liste) != nil {
		t.Fatalf("Liste: %d %s", s, b2)
	}
	je := map[string]storage.AgentToken{}
	for _, tk := range liste {
		je[tk.Name] = tk
	}
	if m := je["mit-angaben"]; m.Anbieter != "Beispiel-Anbieter, Geschäftstarif" || m.Beleg != "vertraege/vertrag.md" {
		t.Errorf("Liste (getrimmt erwartet): %+v", m)
	}
	// Bestandstoken aus der Zeit vor außen: leer, aber nicht gesperrt.
	if a := je["ohne-aus"]; a.Anbieter != "" || a.Beleg != "" || a.RevokedAt != "" {
		t.Errorf("Bestandstoken: %+v", a)
	}
	// Abgelehnte Versuche haben nichts angelegt.
	if _, da := je["x1"]; da {
		t.Error("abgelehnter Versuch hat ein Token angelegt")
	}
}

func TestHerkunft799_Selbstbedienung(t *testing.T) {
	e := newEnv608(t)
	if s, b := e.anfrage(t, e.mensch, "PUT", "/admin/boards/b1/agent-selbstbedienung", "admin@firma.de", `{"an":true}`); s != http.StatusOK {
		t.Fatalf("Schalter: %d %s", s, b)
	}
	anna := func(body string) (int, string) {
		s, b := e.anfrage(t, e.mensch, "POST", "/users/me/agent-tokens", "anna@firma.de", body)
		return s, string(b)
	}
	if s, b := anna(`{"board":"b1","name":"s-aus"}`); s != http.StatusCreated {
		t.Fatalf("aus: %d %s", s, b)
	}
	setzeSeite799(t, e, api.BoardpaarSeiteInnen)
	if s, b := anna(`{"board":"b1","name":"s-innen"}`); s != http.StatusCreated {
		t.Fatalf("innen: %d %s", s, b)
	}
	setzeSeite799(t, e, api.BoardpaarSeiteAussen)
	for name, body := range map[string]string{
		"fehlt":         `{"board":"b1","name":"s1"}`,
		"nur anbieter":  `{"board":"b1","name":"s2","anbieter":"A"}`,
		"zu lang":       `{"board":"b1","name":"s3","anbieter":"` + strings.Repeat("ä", 81) + `","beleg":"b"}`,
		"steuerzeichen": `{"board":"b1","name":"s4","anbieter":"A","beleg":"b\u0000"}`,
		"mehrere":       `{"alle_boards":true,"name":"s5"}`,
	} {
		if s, b := anna(body); s != http.StatusBadRequest {
			t.Errorf("%s: %d %s (400 erwartet)", name, s, b)
		} else if name == "fehlt" && (!strings.Contains(b, "anbieter") || !strings.Contains(b, "beleg")) {
			t.Errorf("Meldung nennt nicht beide Felder: %s", b)
		}
	}
	// 80 Zeichen (Umlaute zählen als Zeichen, nicht als Bytes) sind erlaubt.
	if s, b := anna(`{"board":"b1","name":"s6","anbieter":"` + strings.Repeat("ä", 80) + `","beleg":"` + strings.Repeat("b", 300) + `"}`); s != http.StatusCreated {
		t.Fatalf("Grenzwerte: %d %s", s, b)
	}
	s, b := e.anfrage(t, e.mensch, "GET", "/users/me/agent-tokens", "anna@firma.de", "")
	if s != http.StatusOK || !strings.Contains(string(b), `"anbieter":"`+strings.Repeat("ä", 80)+`"`) {
		t.Fatalf("Liste: %d %s", s, b)
	}
}
