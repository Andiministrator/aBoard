package fold

// Verify #789, V-1: W-4 (anlageVertraulich) muss linear bleiben. Die erste
// Fassung von K-1 suchte nach jeder Op jede Batch-Neuanlage einzeln
// (karteVon): Ops × Neuanlagen × Karten — 200 Neuanlagen auf 3000 Karten
// rissen die Fold-Zeitgrenze von 2 s, der Batch ging verloren. Jetzt ein
// Durchlauf je Op über die Menge der Neuanlagen.
//
// Gemessen wird RELATIV zum selben Batch ohne die Option (gleiche Maschine,
// gleiche Last), mit großzügiger Zeitgrenze des Motors, damit eine langsame
// Maschine nicht am absoluten Wert scheitert. Messwerte stehen in ADR-0140
// „Umsetzung #789“.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func neueKarteOp789(i int, id string) map[string]any {
	var c map[string]any
	_ = json.Unmarshal([]byte(fmt.Sprintf(`{"id":%q,"title":"N %s","columnId":"col-backlog","description":"","labels":[],"assignees":[],"links":[],"attachments":[]}`, id, id)), &c)
	return map[string]any{"id": fmt.Sprintf("op%d", i), "type": "card.upsert", "actor": "x", "schemaVersion": 1,
		"ts": "2026-09-28T12:00:00.000Z", "seq": i, "payload": map[string]any{"sprintId": "__backlog__", "columnId": "col-backlog", "card": c}}
}

func TestW4Menge789_Linear(t *testing.T) {
	if testing.Short() {
		t.Skip("Zeitmessung")
	}
	// 400 Neuanlagen sind mehr als MaxOpsJeBatch (200, api/boards_ops.go) —
	// Absicht (Sitzungs-QA C-1): Der alte, quadratische Stand trennt erst bei
	// dieser Größe sicher (gemessen Faktor 8,9 alt gegen 1,6 neu). Der Test
	// braucht deshalb rund 7 s; mit -short wird er übersprungen.
	const karten, neu = 2000, 400
	snap := synthBoard("z", karten)
	var ops []map[string]any
	for i := 0; i < neu; i++ {
		ops = append(ops, neueKarteOp789(i, fmt.Sprintf("neu-%d", i)))
	}
	roh, _ := json.Marshal(ops)
	m := neuerTestMotor(t, Optionen{Zeitgrenze: 60 * time.Second})
	miss := func(opt string) time.Duration {
		t0 := time.Now()
		erg, err := m.FalteMitZielen(context.Background(), snap, string(roh), opt)
		if err != nil {
			t.Fatalf("%s: %v", opt, err)
		}
		if opt != `{"akteurArt":"agent"}` {
			var b struct {
				Board struct {
					Sprints []struct{ Cards []map[string]any }
				}
			}
			_ = json.Unmarshal([]byte(erg.Projektion), &b)
			n := 0
			for _, s := range b.Board.Sprints {
				for _, c := range s.Cards {
					if c["confidential"] == true {
						n++
					}
				}
			}
			if n != neu {
				t.Fatalf("vertraulich: %d von %d Neuanlagen", n, neu)
			}
		}
		return time.Since(t0)
	}
	// Jede Variante dreimal, das Minimum zählt (Ausreißer durch GC/Last).
	best := func(opt string) time.Duration {
		d := miss(opt)
		for k := 0; k < 2; k++ {
			if x := miss(opt); x < d {
				d = x
			}
		}
		return d
	}
	// F-2023: Die CI fährt den Test ohne -short, also unter fremder Last. Erst
	// wenn auch der zweite Durchgang über Faktor 3 liegt, ist es rot — ein
	// quadratischer Stand (gemessen 8,9) bleibt in beiden darüber.
	const versuche = 2
	for versuch := 1; versuch <= versuche; versuch++ {
		ohne := best(`{"akteurArt":"agent"}`)
		mit := best(`{"akteurArt":"agent","anlageVertraulich":true}`)
		t.Logf("versuch=%d karten=%d neu=%d ohne=%v mit=%v faktor=%.2f", versuch, karten, neu, ohne, mit, float64(mit)/float64(ohne))
		if mit <= 3*ohne {
			return
		}
		if versuch == versuche {
			t.Errorf("W-4 macht den Batch %.1f-mal so langsam (ohne %v, mit %v) — erwartet linear, höchstens Faktor 3", float64(mit)/float64(ohne), ohne, mit)
		}
	}
}
