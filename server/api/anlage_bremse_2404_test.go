package api

import "testing"

// F-2404: neueTermine liest je Eintrag — ein Eintrag mit nicht-String-id kippt die übrigen nicht,
// ein unlesbares „vorher“ zählt nicht jeden Termin als neu.
func TestKritik2404_NeueTermineJeEintrag(t *testing.T) {
	for name, c := range map[string]struct {
		vor, nach string
		soll      int
	}{
		"fremder Eintrag id=5": {`{"board":{"termine":[{"id":5},{"id":"a"}]}}`, `{"board":{"termine":[{"id":5},{"id":"a"},{"id":"b"}]}}`, 1},
		"Eintrag kein Objekt":  {`{"board":{"termine":["x",{"id":"a"}]}}`, `{"board":{"termine":["x",{"id":"a"},{"id":"b"}]}}`, 1},
		"sauber":               {`{"board":{"termine":[{"id":"a"}]}}`, `{"board":{"termine":[{"id":"a"},{"id":"b"},{"id":"c"}]}}`, 2},
		"vorher ohne Liste":    {`{"board":{}}`, `{"board":{"termine":[{"id":"a"}]}}`, 1},
		"vorher unlesbar":      {`{"board":{"termine":"kaputt"}}`, `{"board":{"termine":[{"id":"a"},{"id":"b"}]}}`, 0},
	} {
		if n := neueTermine([]byte(c.vor), []byte(c.nach)); n != c.soll {
			t.Errorf("%s: %d neue Termine, erwartet %d", name, n, c.soll)
		}
	}
}
