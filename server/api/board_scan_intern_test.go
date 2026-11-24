package api

// F-1628: durchlaufeKarten ersetzt das verschachtelte json.Unmarshal in
// leseKommentarBestand. Es muss dieselben Karten mit denselben Rohbytes
// liefern und genau dann „nicht lesbar“ melden, wenn der alte Leser es tat.
// Der alte Leser steht hier als Referenz.

import (
	"bytes"
	"encoding/json"
	"testing"
)

// referenzKarten: der Strukturteil von leseKommentarBestand vor F-1628.
func referenzKarten(state []byte) ([]map[string]json.RawMessage, bool) {
	var wurzel map[string]json.RawMessage
	if json.Unmarshal(state, &wurzel) != nil || wurzel == nil {
		return nil, false
	}
	var board map[string]json.RawMessage
	if json.Unmarshal(wurzel["board"], &board) != nil || board == nil {
		return nil, false
	}
	var sprints []json.RawMessage
	if roh, da := board["sprints"]; da && string(roh) != "null" {
		if json.Unmarshal(roh, &sprints) != nil {
			return nil, false
		}
	}
	var aus []map[string]json.RawMessage
	for _, spRoh := range sprints {
		var sp map[string]json.RawMessage
		if json.Unmarshal(spRoh, &sp) != nil || sp == nil {
			return nil, false
		}
		var karten []json.RawMessage
		if roh, da := sp["cards"]; da && string(roh) != "null" {
			if json.Unmarshal(roh, &karten) != nil {
				return nil, false
			}
		}
		for _, kRoh := range karten {
			var karte map[string]json.RawMessage
			if json.Unmarshal(kRoh, &karte) != nil || karte == nil {
				return nil, false
			}
			aus = append(aus, karte)
		}
	}
	return aus, true
}

func vergleicheMitReferenz(t *testing.T, state []byte) {
	t.Helper()
	ref, refOK := referenzKarten(state)
	var neu []map[string][]byte
	neuOK := durchlaufeKarten(state, func(k map[string][]byte) { neu = append(neu, k) })
	if refOK != neuOK {
		t.Fatalf("lesbar: Referenz %v, Scanner %v — %q", refOK, neuOK, state)
	}
	if !refOK {
		return
	}
	if len(ref) != len(neu) {
		t.Fatalf("Karten: Referenz %d, Scanner %d — %q", len(ref), len(neu), state)
	}
	for i := range ref {
		if len(ref[i]) != len(neu[i]) {
			t.Fatalf("Karte %d: Schlüssel %v gegen %v — %q", i, ref[i], neu[i], state)
		}
		for k, v := range ref[i] {
			if w, da := neu[i][k]; !da || !bytes.Equal(v, w) {
				t.Fatalf("Karte %d, Schlüssel %q: %q gegen %q — %q", i, k, v, w, state)
			}
		}
	}
}

var scanFaelle = []string{
	`{"board":{"sprints":[{"cards":[{"id":"k1","comments":[{"id":"c"}],"activity":[]}]}]}}`,
	` {"version":"2" , "board" : { "sprints" : [ { "cards" : [ ] } , {"id":"s2"} ] } } `,
	`{"board":{}}`,
	`{"board":{"sprints":null}}`,
	`{"board":{"sprints":[{"cards":null}]}}`,
	`{"board":null}`,
	`{}`,
	`[]`,
	`{"board":{"sprints":{}}}`,
	`{"board":{"sprints":[null]}}`,
	`{"board":{"sprints":[{"cards":[null]}]}}`,
	`{"board":{"sprints":[{"cards":[1]}]}}`,
	`{"board":{"sprints":[{"cards":"x"}]}}`,
	`{"board":{"sprints":[]}} x`,
	`{"board":{"sprints":[]}}{}`,
	// Doppelte Schlüssel: der letzte gilt, ein früherer muss nur gültig sein.
	`{"board":5,"board":{"sprints":[{"cards":[{"id":"a"}]}]}}`,
	`{"board":{"sprints":[{"cards":[{"id":"a"}]}]},"board":5}`,
	`{"board":{"sprints":[null],"sprints":[]}}`,
	`{"board":{"sprints":[{"cards":[null],"cards":[{"id":"b"}]}]}}`,
	`{"board":{"sprints":[{"cards":[{"id":"a","id":"b","comments":1,"comments":[]}]}]}}`,
	// Escapes in Schlüsseln werden aufgelöst — wie JSON.parse.
	`{"board":{"sprints":[{"cards":[{"id":"k","comments":[]}]}]}}`,
	// Schreibweise zählt: "Board" ist nicht "board".
	`{"Board":{"sprints":[]}}`,
	`{"board":{"Sprints":[{"cards":[{"id":"x"}]}]}}`,
	// Ungültiges JSON in übersprungenen Werten.
	`{"x":01,"board":{}}`,
	`{"x":1.,"board":{}}`,
	`{"x":-,"board":{}}`,
	`{"x":1e,"board":{}}`,
	`{"x":"\q","board":{}}`,
	`{"x":"\u12G4","board":{}}`,
	`{"x":tru,"board":{}}`,
	`{"x":[1,],"board":{}}`,
	`{"x":{"a":1,},"board":{}}`,
	`{"board":{},}`,
	"{\"x\":\"a\tb\",\"board\":{}}",
	`{"x":-0.5e+10,"y":[true,false,null,"\"\\\/\b\f\n\r\tä"],"board":{}}`,
	`{"board":{"sprints":[{"cards":[{"id":"k","t":"<&>"}]}]}}`,
	``,
	`   `,
	`{"board":{"sprints":[{"cards":[{"id":"k"}]`,
}

func TestDurchlaufeKarten_WieReferenz(t *testing.T) {
	for _, f := range scanFaelle {
		vergleicheMitReferenz(t, []byte(f))
	}
}

func TestDurchlaufeKarten_TiefeBegrenzt(t *testing.T) {
	tief := bytes.Repeat([]byte("["), scanTiefeMax+10)
	state := append([]byte(`{"x":`), tief...)
	if durchlaufeKarten(state, func(map[string][]byte) {}) {
		t.Fatal("unendlich tiefes Dokument als lesbar gemeldet")
	}
}

func FuzzDurchlaufeKarten(f *testing.F) {
	for _, fall := range scanFaelle {
		f.Add([]byte(fall))
	}
	f.Fuzz(func(t *testing.T, state []byte) {
		vergleicheMitReferenz(t, state)
	})
}
