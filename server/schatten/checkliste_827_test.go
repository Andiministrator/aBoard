package schatten

// Karte #827: Go-Seite der gemeinsamen Checklisten-Vektoren (JS-Seite:
// tests/boardpaar-rueckweg-ops-827.test.js) und die Erkennung „nur Häkchen“.

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCheckliste827_Vektoren(t *testing.T) {
	roh, err := os.ReadFile("testdata/checkliste-827.json")
	if err != nil {
		t.Fatal(err)
	}
	var vektoren []struct {
		Name         string            `json:"name"`
		Beschreibung string            `json:"beschreibung"`
		Punkte       []ChecklistePunkt `json:"punkte"`
	}
	if err := json.Unmarshal(roh, &vektoren); err != nil {
		t.Fatal(err)
	}
	if len(vektoren) < 10 {
		t.Fatalf("nur %d Vektoren", len(vektoren))
	}
	for _, v := range vektoren {
		if got := ChecklistePunkte(v.Beschreibung); !reflect.DeepEqual(got, v.Punkte) {
			t.Errorf("%s: %+v, erwartet %+v", v.Name, got, v.Punkte)
		}
	}
	for _, v := range vektoren {
		if v.Name == "CRLF" && !strings.Contains(v.Beschreibung, "\r") {
			t.Fatal("CRLF-Vektor ohne echtes CR")
		}
	}
}

func TestCheckliste827_NurHaekchen(t *testing.T) {
	alt := "Vorspann\n- [ ] a\n- [x] b\n- [ ] a"
	g, ok := NurHaekchenGeaendert(alt, "Vorspann\n- [ ] a\n- [ ] b\n- [x] a")
	if !ok || len(g) != 2 || g[0].Text != "b" || g[0].Erledigt || g[1].Text != "a" || g[1].Vorkommen != 2 || !g[1].Erledigt {
		t.Fatalf("zwei Häkchen erwartet: %+v %v", g, ok)
	}
	for name, neu := range map[string]string{
		"gleich":          alt,
		"Text geändert":   "Vorspann!\n- [x] a\n- [x] b\n- [ ] a",
		"Punkt umbenannt": "Vorspann\n- [x] a2\n- [x] b\n- [ ] a",
		"Punkt dazu":      alt + "\n- [ ] c",
		"nur X statt x":   "Vorspann\n- [ ] a\n- [X] b\n- [ ] a",
		"Zeile dazu":      alt + "\nNachsatz",
	} {
		if _, ok := NurHaekchenGeaendert(alt, neu); ok {
			t.Errorf("%s: darf nicht als „nur Häkchen“ gelten", name)
		}
	}
}
