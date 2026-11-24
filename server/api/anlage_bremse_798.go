package api

// Karte #798 (ADR-0141 Nachtrag „#798 nach 2c“, Vertrag Push-Skript §11 (c)):
// Mengenbremse für Agenten-Anlagen, die nach außen gehen. Der interne Assistent legt innen mit
// `vertraulich: false` an; der Hinweg trägt die Karte vollständig zum externen
// Board. Höchstens anlageBremseMax solcher Anlagen (Karten und Termine) je
// Stunde, je Token und Board. Vertrauliche Anlagen zählen nicht — sie gehen
// nur als Schatten hinaus.
//
// Gezählt wird am Ergebnis des Folds (neu angelegte Karten und Termine), nicht
// an den Ops: Wiederholungen und Upserts auf Bestand legen nichts an. Ein
// abgelehnter oder gescheiterter Batch gibt seine Reservierung zurück.

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	anlageBremseMax     = 20
	anlageBremseFenster = time.Hour
)

// errAnlageBremse: Grenze erreicht, nichts geschrieben. warten bis zum neuen
// Fenster (Retry-After). Ein Batch mit mehr offenen Anlagen als der Grenze
// ginge nie durch; erreichbar ist das nicht, denn W-4 nehmen nur die Werkzeuge
// zurück, und die legen je Aufruf genau einen Eintrag an (Kritik Befund 3).
type errAnlageBremse struct{ warten time.Duration }

func (e *errAnlageBremse) Error() string { return "anlage-bremse" }

func istAnlageBremse(err error) (*errAnlageBremse, bool) {
	var b *errAnlageBremse
	return b, errors.As(err, &b)
}

func (a *BoardsAPI) anlageBremseHolen() *internBremse {
	a.anlageBremseInit.Do(func() {
		if a.anlageBremse == nil {
			a.anlageBremse = &internBremse{eimer: map[string]*bucket{}, jetzt: time.Now, max: anlageBremseMax, fenster: anlageBremseFenster}
		}
	})
	return a.anlageBremse
}

// anlageBremseSchluessel: je Token und Board. Der Token-Name (Akteur) ist je
// Board-Instanz eindeutig; delegierende Tokens (der Assistent über Open WebUI) zählen
// unter ihrem Token, nicht je Person.
func anlageBremseSchluessel(akteur, board string) string { return akteur + "\x00" + board }

// neueTermine zählt Termine, deren id im Ergebnis steht, aber nicht im Stand
// davor. Die Karten-Zählung kommt aus dem Fold selbst. Die Termine werden
// je Eintrag gelesen (F-2404): ein Eintrag ohne String-id zählt nicht als
// neu und kippt die übrigen nicht; ist der ganze Stand oder die Liste
// unlesbar, ist die Menge leer — im „vorher“ hieße das „alles neu“, deshalb
// zählt ein unlesbares „vorher“ gar nichts (fail-open nur für diese eine
// Zählung, der Stand scheitert ohnehin an anderer Stelle).
func neueTermine(vorher, nachher []byte) int {
	ids := func(roh []byte) (map[string]bool, bool) {
		var s struct {
			Board struct {
				Termine json.RawMessage `json:"termine"`
			} `json:"board"`
		}
		m := map[string]bool{}
		if json.Unmarshal(roh, &s) != nil {
			return m, false
		}
		var eintraege []json.RawMessage
		if json.Unmarshal(s.Board.Termine, &eintraege) != nil {
			// Fehlende Liste (null/nichts) ist leer und lesbar; alles andere nicht.
			t := strings.TrimSpace(string(s.Board.Termine))
			return m, t == "" || t == "null"
		}
		for _, e := range eintraege {
			var t struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(e, &t) == nil && t.ID != "" {
				m[t.ID] = true
			}
		}
		return m, true
	}
	vor, vorOK := ids(vorher)
	if !vorOK {
		return 0
	}
	n := 0
	nach, _ := ids(nachher)
	for id := range nach {
		if !vor[id] {
			n++
		}
	}
	return n
}
