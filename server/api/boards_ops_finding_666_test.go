package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// Karte #666 (ADR-0133 §1): Die Quittung von POST /boards/{id}/ops nennt je
// Op, die eine Karte NEU angelegt hat, die vergebene displayId und bei
// Findings die nr. Ops ohne Neuanlage tragen beides nicht.
func TestOps_QuittungNenntDisplayIdUndFindingNr(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	code, out := postOps(t, env, "b1", `[
	 {"type":"card.field","payload":{"cardId":"k1","field":"qa","value":true}},
	 {"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"n1","title":"Neu"}}},
	 {"type":"card.upsert","payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"f1","title":"Befund",
	   "kind":"finding","finding":{"typ":"K","prioritaet":"P1","status":"offen","beleg":"x","nr":77}}}},
	 {"type":"card.upsert","payload":{"card":{"id":"f2","title":"Befund 2",
	   "kind":"finding","finding":{"typ":"S","prioritaet":"P0","status":"sofort"}}}},
	 {"type":"card.move","payload":{"cardId":"k2","toSprintId":"__backlog__","toColId":"col-backlog"}}
	]`)
	if code != http.StatusOK {
		t.Fatalf("Status %d: %v", code, out)
	}
	q, _ := out["ops"].([]any)
	if len(q) != 5 {
		t.Fatalf("Quittung: %v", out)
	}
	feld := func(i int, k string) any { return q[i].(map[string]any)[k] }
	for _, i := range []int{0, 4} {
		for _, k := range []string{"displayId", "nr", "cardId"} {
			if v, da := q[i].(map[string]any)[k]; da {
				t.Errorf("Op %d (keine Neuanlage) trägt %s=%v", i, k, v)
			}
		}
	}
	// nextDisplayId war 3: n1 → 3, f1 → 4, f2 → 5. Findings 1 und 2 (die
	// mitgeschickte nr 77 zählt nicht).
	if feld(1, "displayId") != float64(3) || feld(1, "cardId") != "n1" {
		t.Errorf("n1: %v", q[1])
	}
	if _, da := q[1].(map[string]any)["nr"]; da {
		t.Errorf("normale Karte trägt nr: %v", q[1])
	}
	if feld(2, "displayId") != float64(4) || feld(2, "nr") != float64(1) {
		t.Errorf("f1: %v", q[2])
	}
	if feld(3, "displayId") != float64(5) || feld(3, "nr") != float64(2) || feld(3, "cardId") != "f2" {
		t.Errorf("f2: %v", q[3])
	}
}

// Ein Wert ausserhalb des Wortschatzes macht die Op ungültig: 422 mit
// Protokoll, das das Feld nennt; nichts geschrieben (Fold-Regel, kein
// zweiter Aufschrieb in Go).
func TestOps_FindingUngueltigerWert422(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	vorher := leseOpsBoard(t, env)
	code, out := postOps(t, env, "b1", `[
	 {"type":"card.upsert","payload":{"card":{"id":"f1","title":"Befund",
	   "kind":"finding","finding":{"typ":"K","prioritaet":"hoch","status":"offen"}}}}
	]`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("Status %d, erwartet 422: %v", code, out)
	}
	p, _ := out["protokoll"].([]any)
	if len(p) != 1 {
		t.Fatalf("Protokoll: %v", out)
	}
	e := p[0].(map[string]any)
	if e["grund"] != "finding-wert-ungueltig" || e["feld"] != "finding.prioritaet" || e["index"] != float64(0) {
		t.Errorf("Eintrag: %v", e)
	}
	if txt, _ := e["text"].(string); !strings.Contains(txt, "P0, P1, P2, P3") {
		t.Errorf("Text nennt den Wortschatz nicht: %q", txt)
	}
	if nachher := leseOpsBoard(t, env); nachher.UpdatedAt != vorher.UpdatedAt {
		t.Error("Board trotz 422 geschrieben")
	}
}

// Umwidmen einer bestehenden Karte wird abgelehnt (422 finding-umwidmung).
func TestOps_FindingUmwidmen422(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	code, out := postOps(t, env, "b1", `[{"type":"card.field","payload":{"cardId":"k1","field":"kind","value":"finding"}}]`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("Status %d, erwartet 422: %v", code, out)
	}
	if e := out["protokoll"].([]any)[0].(map[string]any); e["grund"] != "finding-umwidmung" {
		t.Errorf("Eintrag: %v", e)
	}
}

// Wiederholung derselben Ops (gleiche Client-id): Quittung „wiederholt“,
// keine neue Nummer.
func TestOps_FindingWiederholtVergibtKeineNeueNr(t *testing.T) {
	env := newOpsTestEnv(t, true)
	legeOpsBoardAn(t, env)
	batch := `[{"id":"op-f1","type":"card.upsert","payload":{"card":{"id":"f1","title":"Befund",
	   "kind":"finding","finding":{"typ":"K","prioritaet":"P1","status":"offen"}}}}]`
	if code, out := postOps(t, env, "b1", batch); code != http.StatusOK {
		t.Fatalf("erster Lauf: %d %v", code, out)
	}
	code, out := postOps(t, env, "b1", batch)
	if code != http.StatusOK {
		t.Fatalf("Wiederholung: %d %v", code, out)
	}
	q := out["ops"].([]any)[0].(map[string]any)
	if q["wiederholt"] != true {
		t.Errorf("Quittung: %v", q)
	}
	if _, da := q["nr"]; da {
		t.Errorf("Wiederholung nennt eine nr: %v", q)
	}
	code, out = postOps(t, env, "b1", `[{"type":"card.upsert","payload":{"card":{"id":"f2","title":"B2",
	   "kind":"finding","finding":{"typ":"D","prioritaet":"P3","status":"backlog"}}}}]`)
	if code != http.StatusOK || out["ops"].([]any)[0].(map[string]any)["nr"] != float64(2) {
		t.Errorf("zweites Finding: %d %v", code, out)
	}
}
