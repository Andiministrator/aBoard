package api_test

// Karte #778 (ADR-0140 §10): „vertraulich“ setzt jeder, abschalten darf nur
// ein Mensch. Die Testfälle (a), (b), (d), (e), (g), (i) aus §10, am echten
// Ops-Weg (Umgebung aus kalender_aussen_748_test.go).

import (
	"net/http"
	"strings"
	"testing"
)

func TestVertraulichSperre778(t *testing.T) {
	e := newEnv748(t)
	assistent := e.agentToken(t)
	anna := "anna@firma.de"
	opsStatus := func(srv string, wer, ops string) (int, string) {
		t.Helper()
		if srv == "agent" {
			return e.anfrage(t, e.agent, "POST", "/boards/b1/ops", wer, `{"ops":[`+ops+`]}`)
		}
		return e.anfrage(t, e.mensch, "POST", "/boards/b1/ops", wer, `{"ops":[`+ops+`]}`)
	}
	vertraulich := func(id string) any {
		t.Helper()
		k := e.karte(t, id)
		if k == nil {
			t.Fatalf("Karte %s fehlt", id)
		}
		v, ok := k["confidential"]
		if !ok {
			return "<fehlt>"
		}
		return v
	}

	// Ein Agent darf einschalten.
	e.ops(t, e.agent, assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"confidential","value":true}}`)
	if v := vertraulich("k1"); v != true {
		t.Fatalf("Agent schaltet ein: %v", v)
	}
	// (a) Agent schaltet per card.field ab → 422, Wert bleibt.
	if s, b := opsStatus("agent", assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"confidential","value":false}}`); s != http.StatusUnprocessableEntity || !strings.Contains(b, "vertraulich-abgeschaltet") {
		t.Fatalf("(a) card.field false: %d %s", s, b)
	}
	// … ebenso per card.fieldIf.
	if s, _ := opsStatus("agent", assistent, `{"type":"card.fieldIf","payload":{"cardId":"k1","field":"confidential","value":false,"expect":true}}`); s != http.StatusUnprocessableEntity {
		t.Fatalf("(a) card.fieldIf false: %d", s)
	}
	// (b) Ein Agent kann eine bestehende Karte nicht ganz ersetzen (comments
	// gehören nicht in sein card.upsert, ohne sie meldet der Fold eine
	// Entkernung) — der Ganzersatz-Fall ohne das Feld ist deshalb ein
	// Menschen-Weg und in tests/vertraulich-778.test.js geprüft. Hier nur:
	// auch dieser Versuch schreibt nichts.
	k := e.karte(t, "k1")
	delete(k, "confidential")
	delete(k, "comments")
	delete(k, "activity")
	if s, b := opsStatus("agent", assistent, `{"type":"card.upsert","payload":{"card":`+jsonText(k)+`}}`); s != http.StatusUnprocessableEntity || !strings.Contains(b, "entkernung") {
		t.Fatalf("(b) Agenten-Ganzersatz: %d %s", s, b)
	}
	if v := vertraulich("k1"); v != true {
		t.Fatalf("(b) nach Versuch: %v", v)
	}
	// (i) Agent setzt "" bzw. null → kein 422 (beides gilt als vertraulich).
	for _, wert := range []string{`""`, `null`, `true`} {
		if s, b := opsStatus("agent", assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"confidential","value":`+wert+`}}`); s != http.StatusOK {
			t.Fatalf("(i) card.field %s: %d %s", wert, s, b)
		}
	}
	// Ein Batch mit einer harmlosen Op und dem Abschalten scheitert ganz.
	if s, _ := opsStatus("agent", assistent, `{"type":"card.field","payload":{"cardId":"k2","field":"title","value":"harmlos"}},`+
		`{"type":"card.field","payload":{"cardId":"k1","field":"confidential","value":false}}`); s != http.StatusUnprocessableEntity {
		t.Fatalf("Batch: %d", s)
	}
	if e.karte(t, "k2")["title"] == "harmlos" {
		t.Fatal("Batch teilweise geschrieben")
	}
	// Batch „true → false → true“: am Ende vertraulich, kein 422.
	if s, b := opsStatus("agent", assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"confidential","value":false}},`+
		`{"type":"card.field","payload":{"cardId":"k1","field":"confidential","value":true}}`); s != http.StatusOK {
		t.Fatalf("Batch aus/an: %d %s", s, b)
	}
	// (h) Ein Mensch setzt null bzw. "": gespeichert, wie es kommt — gilt als vertraulich.
	e.ops(t, e.mensch, anna, `{"type":"card.field","payload":{"cardId":"k1","field":"confidential","value":null}}`)
	if v := vertraulich("k1"); v != nil {
		t.Fatalf("(h) null: %v", v)
	}
	if s, _ := opsStatus("agent", assistent, `{"type":"card.field","payload":{"cardId":"k1","field":"confidential","value":false}}`); s != http.StatusUnprocessableEntity {
		t.Fatalf("(h) null gilt als vertraulich, Agent darf nicht abschalten: %d", s)
	}
	// (g) Ein Mensch schaltet per card.field ab.
	e.ops(t, e.mensch, anna, `{"type":"card.field","payload":{"cardId":"k1","field":"confidential","value":false}}`)
	if v := vertraulich("k1"); v != false {
		t.Fatalf("(g) Mensch schaltet ab: %v", v)
	}

	// Termine: (d) ohne Feld bleibt, (e) mit false → 422.
	e.ops(t, e.mensch, anna, `{"type":"termin.upsert","payload":{"termin":{"id":"t9","titel":"Notar","datum":"2026-10-20","confidential":true}}}`)
	if s, b := opsStatus("agent", assistent, `{"type":"termin.upsert","payload":{"termin":{"id":"t9","titel":"Notar 2","datum":"2026-10-20"}}}`); s != http.StatusOK {
		t.Fatalf("(d) termin ohne Feld: %d %s", s, b)
	}
	if !strings.Contains(e.stand(t), `"confidential":true`) {
		t.Fatalf("(d) Termin nicht mehr vertraulich: %s", e.stand(t))
	}
	if s, b := opsStatus("agent", assistent, `{"type":"termin.upsert","payload":{"termin":{"id":"t9","titel":"Notar","datum":"2026-10-20","confidential":false}}}`); s != http.StatusUnprocessableEntity || !strings.Contains(b, `"terminId":"t9"`) {
		t.Fatalf("(e) termin mit false: %d %s", s, b)
	}
	// Agent löscht den vertraulichen Termin: erlaubt (X-6).
	e.ops(t, e.agent, assistent, `{"type":"termin.delete","payload":{"terminId":"t9"}}`)

	// Kritiker K-1: Vererbung von der Karte. k2 vertraulich, Termin t8 ohne
	// eigenen Schalter hängt an k2.
	e.ops(t, e.mensch, anna, `{"type":"card.field","payload":{"cardId":"k2","field":"confidential","value":true}}`)
	e.ops(t, e.mensch, anna, `{"type":"termin.upsert","payload":{"termin":{"id":"t8","titel":"Kündigung","datum":"2026-10-21","karteId":"k2"}}}`)
	// Agent löst karteId → 422.
	if s, b := opsStatus("agent", assistent, `{"type":"termin.upsert","payload":{"termin":{"id":"t8","titel":"Kündigung","datum":"2026-10-21"}}}`); s != http.StatusUnprocessableEntity || !strings.Contains(b, `"terminId":"t8"`) {
		t.Fatalf("K-1 karteId lösen: %d %s", s, b)
	}
	// Agent schaltet die Karte ab → 422 (Karte), der Termin hängt mit dran.
	if s, _ := opsStatus("agent", assistent, `{"type":"card.field","payload":{"cardId":"k2","field":"confidential","value":false}}`); s != http.StatusUnprocessableEntity {
		t.Fatalf("K-1 Karte abschalten: %d", s)
	}
	// Agent löscht die Karte → erlaubt; der Termin bleibt wirksam vertraulich
	// (Karte fehlt), ein späteres Lösen von karteId bleibt gesperrt.
	e.ops(t, e.agent, assistent, `{"type":"card.delete","payload":{"cardId":"k2"}}`)
	if s, _ := opsStatus("agent", assistent, `{"type":"termin.upsert","payload":{"termin":{"id":"t8","titel":"Kündigung","datum":"2026-10-21"}}}`); s != http.StatusUnprocessableEntity {
		t.Fatalf("K-1 nach Kartenlöschung: %d", s)
	}
	// Ein Mensch darf die Verbindung lösen.
	e.ops(t, e.mensch, anna, `{"type":"termin.upsert","payload":{"termin":{"id":"t8","titel":"Kündigung","datum":"2026-10-21"}}}`)
}
