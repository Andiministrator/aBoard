package api_test

// Karte #798: Bremse für Agenten-Anlagen innen mit `vertraulich: false`
// (gehen nach außen) — 20 je Stunde, je Token und Board; Karten und Termine
// in einem Eimer. Vertrauliche Anlagen, die Seite „aus“ und andere Tokens
// bleiben unberührt; die abgelehnte Anlage schreibt nichts.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

func (e *env789) gibtEintrag798(t *testing.T, titel string) bool {
	t.Helper()
	return strings.Contains(e.neu.stand(t, "b1"), `"`+titel+`"`)
}

func (e *env789) anlegenStatus798(t *testing.T, token, email, pfad, body string) (int, string) {
	t.Helper()
	s, b := e.neu.rufe(t, token, email, "POST", pfad, body)
	return s, string(b)
}

func TestAnlageBremse798_Innen(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	karten, termine := "/werkzeuge/boards/b1/karten", "/werkzeuge/boards/b1/termine"

	// 19 offene Karten + 1 offener Termin = 20: alle durch.
	for i := 1; i <= 19; i++ {
		e.anlegen797(t, x.projekt, "", karten, fmt.Sprintf(`{"titel":"B798 offen %d","vertraulich":false}`, i))
	}
	e.anlegen797(t, x.projekt, "", termine, `{"titel":"B798 Termin offen","datum":"2026-10-23","vertraulich":false}`)

	// Die 21. offene Anlage: 429, Retry-After-Text, nichts geschrieben.
	s, b := e.anlegenStatus798(t, x.projekt, "", karten, `{"titel":"B798 zu viel","vertraulich":false}`)
	if s != http.StatusTooManyRequests {
		t.Fatalf("21. Anlage: %d %s", s, b)
	}
	var antwort map[string]any
	_ = json.Unmarshal([]byte(b), &antwort)
	if !strings.Contains(fmt.Sprint(antwort), "nicht vertrauliche") {
		t.Errorf("Text nennt die Grenze nicht: %s", b)
	}
	if e.gibtEintrag798(t, "B798 zu viel") {
		t.Errorf("abgelehnte Anlage steht im Board")
	}
	// Auch ein offener Termin ist jetzt gebremst (ein Eimer).
	if s, b := e.anlegenStatus798(t, x.projekt, "", termine, `{"titel":"B798 Termin zu viel","datum":"2026-10-23","vertraulich":false}`); s != http.StatusTooManyRequests {
		t.Errorf("Termin über der Grenze: %d %s", s, b)
	}
	// Vertraulich anlegen geht weiter.
	e.anlegen797(t, x.projekt, "", karten, `{"titel":"B798 vertraulich"}`)
	// Ein anderes Token hat seinen eigenen Eimer.
	e.anlegen797(t, x.token, "anna@firma.de", karten, `{"titel":"B798 Gerd offen","vertraulich":false}`)
	// Rohe Ops nehmen W-4 nie zurück, sie sind nicht gebremst (und vertraulich).
	if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops", `{"ops":[`+neueKarte789("b-roh", "B798 roh", "")+`]}`); s != http.StatusOK {
		t.Errorf("roh: %d %s", s, b)
	}
	// Ein Mensch ist nie gebremst.
	if s, b := e.mensch789(t, "POST", "/boards/b1/ops", `{"ops":[`+neueKarte789("b-m", "B798 Mensch", "")+`]}`); s != http.StatusOK {
		t.Errorf("Mensch: %d %s", s, b)
	}
	// Bearbeiten bestehender Karten zählt nicht.
	k := e.karteVon(t, "b1", "B798 offen 1")
	if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops", `{"ops":[{"type":"card.field","payload":{"cardId":"`+k["id"].(string)+`","field":"description","value":"x"}}]}`); s != http.StatusOK {
		t.Errorf("Bearbeiten: %d %s", s, b)
	}
}

// Seite „aus“: keine Bremse (nichts geht nach außen).
func TestAnlageBremse798_Aus(t *testing.T) {
	e := newEnv789(t, false)
	x := e.neu
	for i := 1; i <= 22; i++ {
		e.anlegen797(t, x.projekt, "", "/werkzeuge/boards/b1/karten", fmt.Sprintf(`{"titel":"B798 aus %d"}`, i))
	}
}

// Eine abgelehnte Anlage (hier: Wiederholung mit gleichem Inhalt ist keine
// neue Anlage) belastet die Bremse nicht: 20 echte Anlagen gehen danach.
func TestAnlageBremse798_WiederholungZaehltNicht(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	karten := "/werkzeuge/boards/b1/karten"
	for i := 0; i < 5; i++ {
		e.anlegen797(t, x.projekt, "", karten, `{"titel":"B798 dieselbe","vertraulich":false}`)
	}
	for i := 1; i <= 19; i++ {
		e.anlegen797(t, x.projekt, "", karten, fmt.Sprintf(`{"titel":"B798 w %d","vertraulich":false}`, i))
	}
	if s, b := e.anlegenStatus798(t, x.projekt, "", karten, `{"titel":"B798 w 20","vertraulich":false}`); s != http.StatusTooManyRequests {
		t.Errorf("21. echte Anlage: %d %s", s, b)
	}
}

// Kritik Befund 1: Scheitert der Schreibvorgang NACH der Reservierung, kommt
// sie zurück — sonst wäre die Bremse nach Fehlern zu früh voll.
func TestAnlageBremse798_RueckgabeBeiFehler(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	karten := "/werkzeuge/boards/b1/karten"
	zurueck := api.SetzeAnlageBremseNahtFuerTest(func() error { return fmt.Errorf("Probe: Schreibfehler nach Reservierung") })
	for i := 1; i <= 5; i++ {
		if s, b := e.anlegenStatus798(t, x.projekt, "", karten, fmt.Sprintf(`{"titel":"B798 f %d","vertraulich":false}`, i)); s == http.StatusOK {
			t.Fatalf("Anlage %d trotz Fehler-Naht: %s", i, b)
		}
	}
	zurueck()
	if e.gibtEintrag798(t, "B798 f 1") {
		t.Fatalf("gescheiterte Anlage steht im Board")
	}
	for i := 1; i <= 20; i++ {
		e.anlegen797(t, x.projekt, "", karten, fmt.Sprintf(`{"titel":"B798 r %d","vertraulich":false}`, i))
	}
	if s, b := e.anlegenStatus798(t, x.projekt, "", karten, `{"titel":"B798 r 21","vertraulich":false}`); s != http.StatusTooManyRequests {
		t.Errorf("21. Anlage: %d %s", s, b)
	}
}

// Kritik Befund 2: Das Protokoll aus ADR-0048 §11 (b) ist der Aktivitätslog —
// eine offene Werkzeug-Anlage steht dort mit actor_kind agent und dem Token.
func TestAnlageBremse798_Protokoll(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	e.anlegen797(t, x.projekt, "", "/werkzeuge/boards/b1/karten", `{"titel":"B798 Protokoll","vertraulich":false}`)
	id, _ := e.karteVon(t, "b1", "B798 Protokoll")["id"].(string)
	s, b := e.mensch789(t, "GET", "/boards/b1/activities?card="+id, "")
	if s != http.StatusOK {
		t.Fatalf("activities: %d %s", s, b)
	}
	var liste []map[string]any
	if err := json.Unmarshal([]byte(b), &liste); err != nil {
		var huelle map[string][]map[string]any
		if json.Unmarshal([]byte(b), &huelle) != nil {
			t.Fatalf("Antwort unlesbar: %s", b)
		}
		for _, v := range huelle {
			liste = append(liste, v...)
		}
	}
	gefunden := false
	for _, a := range liste {
		akteur, _ := a["actor"].(string)
		if a["actor_kind"] == "agent" && strings.HasPrefix(akteur, "agent:") && a["ts"] != "" {
			gefunden = true
		}
	}
	if !gefunden {
		t.Errorf("kein Aktivitätseintrag mit actor_kind agent und agent:<Token>: %s", b)
	}
}
