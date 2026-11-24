package api_test

// Karte #798 (ADR-0141 Nachtrag „#798 nach 2c“): Herkunftsmarke
// angelegtVonAgent. Gesetzt nur, wenn ein Agent auf der Seite innen eine
// Karte NEU anlegt — gleich ob vertraulich oder mit `vertraulich: false`.
// Sie fällt nie weg (Herkunft), ist nicht per Nutzlast setzbar oder
// entfernbar und entsteht weder bei Menschen noch auf der Seite „aus“.

import (
	"net/http"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
)

func (e *env789) herkunft798(t *testing.T, titel string) any {
	t.Helper()
	v, ok := e.karteVon(t, "b1", titel)["angelegtVonAgent"]
	if !ok {
		return "<fehlt>"
	}
	return v
}

func TestHerkunft798_Innen(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	karten := "/werkzeuge/boards/b1/karten"

	// Werkzeug: offen und vertraulich angelegt — beide tragen die Marke.
	e.anlegen797(t, x.projekt, "", karten, `{"titel":"H798 offen","vertraulich":false}`)
	e.anlegen797(t, x.projekt, "", karten, `{"titel":"H798 vertraulich"}`)
	e.anlegen797(t, x.token, "anna@firma.de", karten, `{"titel":"H798 Gerd offen","vertraulich":false}`)
	// Roh: Marke false in der Nutzlast zählt nicht.
	if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops",
		`{"ops":[`+neueKarte789("h-roh", "H798 roh", `,"angelegtVonAgent":false`)+`]}`); s != http.StatusOK {
		t.Fatalf("roh: %d %s", s, b)
	}
	// Mensch legt an, mit erfundener Marke → keine.
	if s, b := e.mensch789(t, "POST", "/boards/b1/ops",
		`{"ops":[`+neueKarte789("h-m", "H798 Mensch", `,"angelegtVonAgent":true`)+`]}`); s != http.StatusOK {
		t.Fatalf("Mensch: %d %s", s, b)
	}
	for titel, soll := range map[string]any{
		"H798 offen": true, "H798 vertraulich": true, "H798 Gerd offen": true, "H798 roh": true, "H798 Mensch": "<fehlt>",
	} {
		if v := e.herkunft798(t, titel); v != soll {
			t.Errorf("%s: angelegtVonAgent = %v, erwartet %v", titel, v, soll)
		}
	}

	// Mensch ändert Titel, schickt die ganze Karte ohne Marke: kein 422, Marke bleibt.
	k := e.karteVon(t, "b1", "H798 offen")
	id, _ := k["id"].(string)
	delete(k, "angelegtVonAgent")
	k["title"] = "H798 offen umbenannt"
	if s, b := e.mensch789(t, "POST", "/boards/b1/ops", `{"ops":[{"type":"card.upsert","payload":{"card":`+jsonText(k)+`}}]}`); s != http.StatusOK {
		t.Fatalf("Mensch-Upsert ohne Marke: %d %s", s, b)
	}
	if v := e.herkunft798(t, "H798 offen umbenannt"); v != true {
		t.Errorf("nach Mensch-Upsert: %v", v)
	}
	// Weder Mensch noch Agent nehmen sie per card.field weg, in keiner Schreibweise.
	for _, feld := range []string{"angelegtVonAgent", "ANGELEGTVONAGENT"} {
		e.mensch789(t, "POST", "/boards/b1/ops", `{"ops":[{"type":"card.field","payload":{"cardId":"`+id+`","field":"`+feld+`","value":false}}]}`)
		x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops", `{"ops":[{"type":"card.field","payload":{"cardId":"`+id+`","field":"`+feld+`","value":false}}]}`)
	}
	if v := e.herkunft798(t, "H798 offen umbenannt"); v != true {
		t.Errorf("nach card.field false: %v", v)
	}
	// Ein Mensch kann sie an einer fremden Karte nicht setzen.
	e.mensch789(t, "POST", "/boards/b1/ops", `{"ops":[{"type":"card.field","payload":{"cardId":"h-m","field":"angelegtVonAgent","value":true}}]}`)
	if v := e.herkunft798(t, "H798 Mensch"); v != "<fehlt>" {
		t.Errorf("Mensch setzt Marke: %v", v)
	}
}

// Seite „aus“: kein Board-Paar, keine Marke (dort gibt es kein „nach außen“).
func TestHerkunft798_Aus(t *testing.T) {
	e := newEnv789(t, false)
	x := e.neu
	e.anlegen797(t, x.projekt, "", "/werkzeuge/boards/b1/karten", `{"titel":"H798 aus"}`)
	if v := e.herkunft798(t, "H798 aus"); v != "<fehlt>" {
		t.Errorf("Seite aus: angelegtVonAgent = %v", v)
	}
}

// Kritik-Testlücke: auch ein Agent-Ganz-Upsert ohne Marke behält sie (kein 422).
func TestHerkunft798_AgentUpsertOhneMarke(t *testing.T) {
	e := newEnv789(t, false)
	e.seite(t, api.BoardpaarSeiteInnen)
	x := e.neu
	e.anlegen797(t, x.projekt, "", "/werkzeuge/boards/b1/karten", `{"titel":"H798 A","vertraulich":false}`)
	k := e.karteVon(t, "b1", "H798 A")
	delete(k, "angelegtVonAgent")
	k["description"] = "vom Agenten"
	if s, b := x.rufe(t, x.projekt, "", "POST", "/boards/b1/ops", `{"ops":[{"type":"card.upsert","payload":{"card":`+jsonText(k)+`}}]}`); s != http.StatusOK {
		t.Fatalf("Agent-Upsert ohne Marke: %d %s", s, b)
	}
	if v := e.herkunft798(t, "H798 A"); v != true {
		t.Errorf("nach Agent-Upsert: %v", v)
	}
}
