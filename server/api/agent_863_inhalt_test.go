package api_test

// Karte #863 (A-5): Pflicht-Titel und Pflicht-Text der Agenten-Werkzeuge brauchen — wie bei Vorschlägen
// (F-2120) — mindestens ein Zeichen aus L, N, P oder S; ein Kombinationszeichen allein ist kein Inhalt.
// Das Zeichen entsteht per rune(), nicht als Escape im Quelltext.

import (
	"net/http"
	"strings"
	"testing"
)

func TestAgent863_PflichtTextBrauchtInhaltszeichen(t *testing.T) {
	e := newEnv671(t, true)
	akzent := string(rune(0x300))
	for name, c := range map[string]struct{ pfad, feld string }{
		"karte anlegen: titel": {"/werkzeuge/boards/b1/karten", "titel"},
		"kommentieren: text":   {"/werkzeuge/boards/b1/karten/5/kommentare", "text"},
	} {
		s, b := e.rufe(t, e.token, "anna@firma.de", "POST", c.pfad, `{"`+c.feld+`":"`+akzent+`"}`)
		if s != http.StatusBadRequest || !strings.Contains(string(b), "braucht mindestens") {
			t.Errorf("%s: nur Kombinationszeichen: %d %s", name, s, b)
		}
	}
	// Gegenprobe: ein normaler Titel mit Kombinationszeichen geht weiter.
	if s, b := e.rufe(t, e.token, "anna@firma.de", "POST", "/werkzeuge/boards/b1/karten", `{"titel":"Cafe`+akzent+` planen"}`); s != http.StatusOK {
		t.Errorf("normaler Titel abgelehnt: %d %s", s, b)
	}
}

// Verify V-7: ein unveränderter Bestandstitel ohne Inhaltszeichen (Altbestand) blockiert keine Änderung;
// ein neuer Titel ohne Inhaltszeichen bleibt abgelehnt.
func TestAgent863_VorlageUnveraenderterTitelOhneInhalt(t *testing.T) {
	e := newEnv671(t, true)
	owner, _ := e.personOwner757(t)
	e.zuruecksetzen755(t)
	akzent := string(rune(0x300))
	st := e.stand(t, "b2")
	vorlagen := `"templates":[{"id":"v1","isTemplate":true,"title":"` + akzent + `"}],`
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b2'`, strings.Replace(st, `"nextDisplayId":10,`, `"nextDisplayId":10,`+vorlagen, 1)); err != nil {
		t.Fatal(err)
	}
	s, b := e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"vorlagen":[{"id":"v1","titel":"`+akzent+`","labels":["x"]}]}`)
	if s != http.StatusOK {
		t.Errorf("unveränderter Altbestands-Titel blockiert: %d %s", s, b)
	}
	s, b = e.rufe(t, owner, "", "POST", "/werkzeuge/boards/b2/aendern", `{"vorlagen":[{"id":"v1","titel":"`+akzent+akzent+`"}]}`)
	if s != http.StatusBadRequest || !strings.Contains(string(b), "braucht mindestens") {
		t.Errorf("geänderter Titel ohne Inhalt: %d %s", s, b)
	}
}

func TestAgent863_SwimlaneUnveraenderterTitelOhneInhalt(t *testing.T) {
	e := newEnv671(t, true)
	e.zuruecksetzen755(t)
	akzent := string(rune(0x300))
	st := e.stand(t, "b1")
	if !strings.Contains(st, `"title":"Sprint 1"`) {
		t.Fatal("Ausgangsstand ohne Sprint 1")
	}
	if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b1'`, strings.Replace(st, `"title":"Sprint 1"`, `"title":"`+akzent+`"`, 1)); err != nil {
		t.Fatal(err)
	}
	if s, m := e.schreibe755(t, "/swimlanes/s1", `{"titel":"`+akzent+`","archiviert":true}`); s != http.StatusOK {
		t.Errorf("unveränderter Altbestands-Titel blockiert: %d %v", s, m)
	}
	if s, m := e.schreibe755(t, "/swimlanes/s1", `{"titel":"`+akzent+akzent+`"}`); s != http.StatusBadRequest {
		t.Errorf("geänderter Titel ohne Inhalt: %d %v", s, m)
	}
}
