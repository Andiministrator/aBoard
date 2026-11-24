package api

// F-1621: Die Prüfung am Ergebnis greift auch ohne das Zusammenführen — sie
// ist das Verbindliche, das Zusammenführen nur Komfort.

import "testing"

func standMitVerlauf(karteID, activity string) []byte {
	return []byte(`{"board":{"sprints":[{"cards":[{"id":"` + karteID + `","comments":[],"activity":` + activity + `}]}]}}`)
}

func TestPruefeVerlauf_AmErgebnis(t *testing.T) {
	nachweis := `{"id":"v1","action":"comment-deleted-by-owner","author":"o@x.de","from":"a@x.de","to":"Text"}`
	eigen := `{"id":"v2","action":"owner-changed","author":"A"}`
	owner := kommentarSchreiber{email: "o@x.de", darfFremde: true}
	agent := kommentarSchreiber{agentAkteur: "agent:claude"}
	for _, f := range []struct {
		name         string
		vorher, nach []byte
		s            kommentarSchreiber
		erlaubt      map[string]bool
		erwartet     string
	}{
		{"Nachweis entfernt (Owner)", standMitVerlauf("k", `[`+nachweis+`]`), standMitVerlauf("k", `[]`), owner, nil, "verlauf-geaendert"},
		{"Nachweis entfernt (Agent)", standMitVerlauf("k", `[`+nachweis+`]`), standMitVerlauf("k", `[`+eigen+`]`), agent, nil, "verlauf-geaendert"},
		{"Reihenfolge egal", standMitVerlauf("k", `[`+nachweis+`,`+eigen+`]`), standMitVerlauf("k", `[`+eigen+`,`+nachweis+`]`), owner, nil, ""},
		{"Anhängen", standMitVerlauf("k", `[`+nachweis+`]`), standMitVerlauf("k", `[`+nachweis+`,`+eigen+`]`), owner, nil, ""},
		{"attachments:[] ergänzt (F-1629)", standMitVerlauf("k", `[`+nachweis+`]`),
			standMitVerlauf("k", `[{"id":"v1","action":"comment-deleted-by-owner","author":"o@x.de","from":"a@x.de","to":"Text","meta":{}}]`), owner, nil, ""},
		{"Fälschung", standMitVerlauf("k", `[]`), standMitVerlauf("k", `[{"action":"llm-sent"}]`), owner, nil, "verlauf-gefaelscht"},
		{"Fälschung an neuer Karte", standMitVerlauf("k", `[]`),
			[]byte(`{"board":{"sprints":[{"cards":[{"id":"k","activity":[]},{"id":"n","activity":[{"action":"vorschlag"}]}]}]}}`), agent, nil, "verlauf-gefaelscht"},
		{"Vorschlags-Zugang darf vorschlag", standMitVerlauf("k", `[]`),
			[]byte(`{"board":{"sprints":[{"cards":[{"id":"k","activity":[]},{"id":"n","activity":[{"action":"vorschlag"}]}]}]}}`), agent, map[string]bool{"vorschlag": true}, ""},
		{"Karte gelöscht", standMitVerlauf("k", `[`+nachweis+`]`), []byte(`{"board":{"sprints":[{"cards":[]}]}}`), owner, nil, ""},
		{"kaputt danach", standMitVerlauf("k", `[]`), standMitVerlauf("k", `{"x":1}`), owner, nil, "stand-unlesbar"},
		{"kaputt vorher, lesbar danach", standMitVerlauf("k", `"x"`), standMitVerlauf("k", `[`+eigen+`]`), agent, nil, ""},
		{"Legacy ohne Identität", standMitVerlauf("k", `[`+nachweis+`]`), standMitVerlauf("k", `[]`), kommentarSchreiber{}, nil, ""},
	} {
		alt, ok := leseKommentarBestand(f.vorher)
		v, _ := vergleicheMitBestand(alt, ok, f.nach, f.s, "b", f.erlaubt)
		got := ""
		if len(v) > 0 {
			got = v[0].art
		}
		if got != f.erwartet {
			t.Errorf("%s: %v, erwartet %q", f.name, v, f.erwartet)
		}
	}
}
