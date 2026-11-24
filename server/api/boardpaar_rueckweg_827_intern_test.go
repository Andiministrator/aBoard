package api

// Karte #827: Kontext-Merkmal und Grund der Rückweg-Sperre (boardpaar_rueckweg.go).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Andiministrator/aBoard/server/fold"
)

func TestRueckweg827_GrundNurOhneMerkmal(t *testing.T) {
	if rueckwegOpGrund("card.moveIf", false) == "" || rueckwegOpGrund("card.moveIf", true) != "" {
		t.Fatal("card.moveIf: ohne Merkmal abgelehnt, mit Merkmal erlaubt")
	}
	if rueckwegOpGrund("card.move", false) != "" {
		t.Fatal("andere Ops unberührt")
	}
	if !boardpaarRueckwegAus(mitBoardpaarRueckweg(context.Background())) || boardpaarRueckwegAus(context.Background()) {
		t.Fatal("Kontext-Merkmal")
	}
}

// Sitzungs-Kritik B-7: Formprüfung (400) der vier Rückweg-Ops.
func TestRueckweg827_Formpruefung(t *testing.T) {
	faelle := []struct {
		typ, payload string
		fehler       bool
	}{
		{"termin.fieldIf", `{"terminId":"t","field":"ort","value":"x"}`, true},
		{"termin.fieldIf", `{"terminId":"t","field":"ort","value":"x","expect":null}`, false},
		{"card.moveIf", `{"cardId":"c","toSprintId":"s","toColId":"","expectSprintId":"s","expectColId":"c"}`, true},
		{"card.moveIf", `{"cardId":"c","toSprintId":"s","toColId":"x","expectSprintId":"s","expectColId":"c"}`, false},
		{"card.checkItemIf", `{"cardId":"c","text":"a","vorkommen":0,"erledigt":true,"expect":false}`, true},
		{"card.checkItemIf", `{"cardId":"c","text":"a","vorkommen":1.5,"erledigt":true,"expect":false}`, true},
		{"card.checkItemIf", `{"cardId":"c","text":"a","vorkommen":1,"erledigt":true}`, true},
		{"card.checkItemIf", `{"cardId":"c","text":"a","vorkommen":1,"erledigt":true,"expect":false}`, false},
		{"comment.editIf", `{"cardId":"c","commentId":"k","text":"x"}`, true},
		{"comment.editIf", `{"cardId":"c","commentId":"","text":"x","expect":"y"}`, true},
		{"comment.editIf", `{"cardId":"c","commentId":"k","text":"x","expect":"y"}`, false},
	}
	for _, f := range faelle {
		if got := payloadFormFehler(f.typ, json.RawMessage(f.payload)); (got != "") != f.fehler {
			t.Errorf("%s %s: Fehler erwartet=%v, bekam %q", f.typ, f.payload, f.fehler, got)
		}
	}
}

// Sitzungs-Kritik B-7: Mit Rückweg-Merkmal kommen die Ops durch stempleOps;
// comment.editIf bekommt den edited-Stempel des Servers, und der Hash hängt
// nicht am mitgeschickten edited (Wiederholung bleibt Wiederholung).
func TestRueckweg827_StempelUndHash(t *testing.T) {
	m, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	a := &BoardsAPI{fold: m}
	op := func(edited string) map[string]json.RawMessage {
		return map[string]json.RawMessage{"id": json.RawMessage(`"bp-1"`), "type": json.RawMessage(`"comment.editIf"`),
			"payload": json.RawMessage(`{"cardId":"c","commentId":"k","text":"x","expect":"y","edited":"` + edited + `"}`)}
	}
	ops1, f1 := a.stempleOps([]map[string]json.RawMessage{op("2020-01-01")}, "boardpaar: A", nil, false, true)
	ops2, f2 := a.stempleOps([]map[string]json.RawMessage{op("2030-12-31")}, "boardpaar: A", nil, false, true)
	if len(f1)+len(f2) != 0 || len(ops1) != 1 || len(ops2) != 1 {
		t.Fatalf("mit Merkmal angenommen erwartet: %v %v", f1, f2)
	}
	var p map[string]any
	_ = json.Unmarshal(ops1[0].Payload, &p)
	if p["edited"] == "2020-01-01" || p["edited"] == nil {
		t.Fatalf("edited stempelt der Server: %v", p["edited"])
	}
	if opHash(ops1[0]) != opHash(ops2[0]) {
		t.Fatal("Hash darf nicht am mitgeschickten edited hängen")
	}
	if _, f := a.stempleOps([]map[string]json.RawMessage{op("x")}, "boardpaar: A", nil, false, false); len(f) != 1 {
		t.Fatal("ohne Merkmal abgelehnt")
	}
}
