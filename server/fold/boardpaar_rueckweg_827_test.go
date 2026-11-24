package fold

// Karte #827 (ADR-0141 §2, §5.3): die Brücke für den Rückweg des Board-Paars.
//   - Die vier bedingten Ops melden ein fehlendes Ziel als ziel-fehlt (in
//     ops.js sind sie dann ein stilles No-op — der Rückweg muss
//     „verworfen/karte-weg“ von „angewendet“ unterscheiden können).
//   - Die Option boardpaarAnlage lässt boardpaarUrsprung an einer NEU
//     angelegten Karte bzw. einem neuen Termin stehen — sonst nie.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const brett827 = `{"version":"2","board":{"id":"b","title":"B","nextDisplayId":3,
"columnDefs":[{"id":"col-backlog","title":"Backlog"}],
"sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[
 {"id":"c1","displayId":1,"title":"T","description":"- [ ] a","columnId":"col-backlog","comments":[{"id":"k1","author":"extern: A","text":"x"}]}]}],
"termine":[{"id":"t1","titel":"Treffen","datum":"2026-10-20"}]}}`

func op827(typ string, payload string) string {
	return `{"id":"o1","type":"` + typ + `","actor":"boardpaar: A","schemaVersion":1,"ts":"2026-10-13T12:00:00.000Z","seq":0,"payload":` + payload + `}`
}

func zielFehlt827(t *testing.T, opJSON string) []map[string]any {
	t.Helper()
	m := neuerTestMotor(t, Optionen{})
	erg, err := m.FalteMitZielen(context.Background(), brett827, "["+opJSON+"]", "")
	if err != nil {
		t.Fatal(err)
	}
	var p []map[string]any
	if err := json.Unmarshal([]byte(erg.Protokoll), &p); err != nil {
		t.Fatal(err)
	}
	var aus []map[string]any
	for _, e := range p {
		if e["art"] == "ziel-fehlt" {
			aus = append(aus, e)
		}
	}
	return aus
}

func TestRueckweg827_ZielFehlt(t *testing.T) {
	faelle := []struct {
		name, typ, payload string
		schluessel, wert   string
	}{
		{"moveIf Karte", "card.moveIf", `{"cardId":"weg","toSprintId":"__backlog__","toColId":"col-backlog","expectSprintId":"__backlog__","expectColId":"col-backlog"}`, "cardId", "weg"},
		{"moveIf Lane", "card.moveIf", `{"cardId":"c1","toSprintId":"lane-weg","toColId":"col-backlog","expectSprintId":"__backlog__","expectColId":"col-backlog"}`, "sprintId", "lane-weg"},
		{"checkItemIf", "card.checkItemIf", `{"cardId":"weg","text":"a","vorkommen":1,"erledigt":true,"expect":false}`, "cardId", "weg"},
		{"editIf Karte", "comment.editIf", `{"cardId":"weg","commentId":"k1","text":"y","expect":"x"}`, "cardId", "weg"},
		{"editIf Kommentar", "comment.editIf", `{"cardId":"c1","commentId":"k-weg","text":"y","expect":"x"}`, "commentId", "k-weg"},
		{"termin.fieldIf", "termin.fieldIf", `{"terminId":"t-weg","field":"ort","value":"x","expect":null}`, "terminId", "t-weg"},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			z := zielFehlt827(t, op827(f.typ, f.payload))
			if len(z) != 1 || z[0][f.schluessel] != f.wert {
				t.Fatalf("ziel-fehlt erwartet mit %s=%s, bekam %v", f.schluessel, f.wert, z)
			}
		})
	}
	// Gegenrichtung: vorhandene Ziele melden nichts.
	for _, o := range []string{
		op827("card.checkItemIf", `{"cardId":"c1","text":"a","vorkommen":1,"erledigt":true,"expect":false}`),
		op827("comment.editIf", `{"cardId":"c1","commentId":"k1","text":"y","expect":"x"}`),
		op827("termin.fieldIf", `{"terminId":"t1","field":"ort","value":"x","expect":null}`),
	} {
		if z := zielFehlt827(t, o); len(z) != 0 {
			t.Fatalf("kein ziel-fehlt erwartet, bekam %v", z)
		}
	}
}

func TestRueckweg827_UrsprungNurMitOptionUndNurNeu(t *testing.T) {
	m := neuerTestMotor(t, Optionen{})
	neueKarte := op827("card.upsert", `{"sprintId":"__backlog__","card":{"id":"c9","title":"Neu","columnId":"col-backlog","boardpaarUrsprung":"aussen-1"}}`)
	neuerTermin := strings.Replace(op827("termin.upsert", `{"termin":{"id":"t9","titel":"N","datum":"2026-10-21","boardpaarUrsprung":"aussen-2"}}`), `"seq":0`, `"seq":1`, 1)
	bestand := strings.Replace(op827("card.upsert", `{"card":{"id":"c1","title":"T2","description":"- [ ] a","columnId":"col-backlog","comments":[],"boardpaarUrsprung":"fremd"}}`), `"seq":0`, `"seq":2`, 1)
	bestand = strings.Replace(bestand, `"id":"o1"`, `"id":"o3"`, 1)
	neuerTermin = strings.Replace(neuerTermin, `"id":"o1"`, `"id":"o2"`, 1)
	ops := "[" + neueKarte + "," + neuerTermin + "," + bestand + "]"

	lies := func(opt string) (map[string]any, map[string]any, map[string]any) {
		erg, err := m.FalteMitZielen(context.Background(), brett827, ops, opt)
		if err != nil {
			t.Fatal(err)
		}
		var b struct {
			Board struct {
				Sprints []struct{ Cards []map[string]any }
				Termine []map[string]any
			}
		}
		if err := json.Unmarshal([]byte(erg.Projektion), &b); err != nil {
			t.Fatal(err)
		}
		var c1, c9, t9 map[string]any
		for _, c := range b.Board.Sprints[0].Cards {
			switch c["id"] {
			case "c1":
				c1 = c
			case "c9":
				c9 = c
			}
		}
		for _, tt := range b.Board.Termine {
			if tt["id"] == "t9" {
				t9 = tt
			}
		}
		return c1, c9, t9
	}
	c1, c9, t9 := lies(`{"boardpaarAnlage":true}`)
	if c9["boardpaarUrsprung"] != "aussen-1" || t9["boardpaarUrsprung"] != "aussen-2" {
		t.Fatalf("Ursprung an Neuanlagen erwartet: Karte %v, Termin %v", c9["boardpaarUrsprung"], t9["boardpaarUrsprung"])
	}
	if _, da := c1["boardpaarUrsprung"]; da {
		t.Fatalf("auf Bestand gewinnt der Bestand (ohne Ursprung), bekam %v", c1["boardpaarUrsprung"])
	}
	c1, c9, t9 = lies("")
	for name, o := range map[string]map[string]any{"c1": c1, "c9": c9, "t9": t9} {
		if _, da := o["boardpaarUrsprung"]; da {
			t.Fatalf("%s: ohne Option kein Ursprung, bekam %v", name, o["boardpaarUrsprung"])
		}
	}
	// Ungültige Kennung bleibt draußen.
	erg, err := m.FalteMitZielen(context.Background(), brett827,
		"["+op827("card.upsert", `{"card":{"id":"c8","title":"N","columnId":"col-backlog","boardpaarUrsprung":"a b"}}`)+"]", `{"boardpaarAnlage":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(erg.Projektion, "boardpaarUrsprung") {
		t.Fatal("ungültige Kennung darf nicht als Ursprung stehen")
	}
}

// Die neuen Fold-Zweige laufen auch unter goja (gleiches Bündel wie im
// Browser; Fixtures aus bun gibt es für sie nicht — tests/boardpaar-rueckweg-ops-827.test.js
// prüft dieselben Fälle unter vitest).
func TestRueckweg827_GojaFaltetCheckItemIf(t *testing.T) {
	m := neuerTestMotor(t, Optionen{})
	erg, err := m.FalteMitZielen(context.Background(), brett827,
		"["+op827("card.checkItemIf", `{"cardId":"c1","text":"a","vorkommen":1,"erledigt":true,"expect":false}`)+"]", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(erg.Projektion, `"description":"- [x] a"`) || strings.TrimSpace(erg.Protokoll) != "[]" {
		t.Fatalf("Projektion %s, Protokoll %s", erg.Projektion, erg.Protokoll)
	}
	erg, err = m.FalteMitZielen(context.Background(), brett827,
		"["+op827("card.checkItemIf", `{"cardId":"c1","text":"a","vorkommen":1,"erledigt":true,"expect":true}`)+"]", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(erg.Protokoll, `"feld-geaendert"`) {
		t.Fatalf("feld-geaendert erwartet: %s", erg.Protokoll)
	}
}

// Karte #827 (E3b): Die Regelprüfung von termin.fieldIf baute ihren
// Probe-Termin mit clone() — ohne Klon (Server) die Identität. Sie setzte den
// Wert am echten Termin, applyOp sah danach „steht schon so“, und der
// Konflikt fiel still weg. Beide Modi müssen den Konflikt melden.
func TestRueckweg827_TerminFieldIfKonfliktOhneKlon(t *testing.T) {
	st := `{"version":"2","board":{"id":"b","title":"B","sprints":[{"id":"__backlog__","cards":[]}],` +
		`"termine":[{"id":"t1","titel":"Abnahme","datum":"2026-10-20"}]}}`
	op := "[" + op827("termin.fieldIf", `{"terminId":"t1","field":"titel","value":"Neu","expect":"Alt"}`) + "]"
	for _, mitKlon := range []bool{false, true} {
		m := neuerTestMotor(t, Optionen{MitKlon: mitKlon})
		erg, err := m.FalteMitZielen(context.Background(), st, op, `{"akteurArt":"mensch"}`)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(erg.Protokoll, `"feld-geaendert"`) || !strings.Contains(erg.Projektion, `"titel":"Abnahme"`) {
			t.Fatalf("mitKlon=%v: Konflikt erwartet, Termin unverändert — Protokoll %s, Projektion %s", mitKlon, erg.Protokoll, erg.Projektion)
		}
	}
}
