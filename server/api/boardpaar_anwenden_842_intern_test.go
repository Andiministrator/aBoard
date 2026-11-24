package api

// Karte #842 (F-2293): verschoben von außen in eine innen archivierte oder
// Service-Lane — der Hinweg trägt deren Karten nicht hinaus, außen sähe die
// Karte gelöscht aus. Verworfen (lane-unbekannt), außen bleibt sie am alten Ort.

import (
	"encoding/json"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
)

func TestRueckweg842_VerschobenInArchivierteLaneVerworfen(t *testing.T) {
	verschoben := &anwendenEintrag{OpID: "v", Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtVerschoben, Stelle: "ort",
		Vorher:  json.RawMessage(`{"lane":"s1","spalte":"col-ready","nach":null}`),
		Nachher: json.RawMessage(`{"lane":"alt","spalte":"col-ready","nach":null}`)}
	for name, art := range map[string]string{"archiviert": `"archived":true`, "service": `"kind":"service"`} {
		b := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","cards":[{"id":"k1","columnId":"col-ready"}]},
		 {"id":"alt",`+art+`,"cards":[]},{"id":"__backlog__","cards":[]}]}}`)
		plan, sofort := verschoben.baue(b)
		if sofort == nil || sofort.ergebnis != rwVerworfen || sofort.grund != rwGrundLaneUnbekannt {
			t.Fatalf("%s: plan %v, sofort %+v — erwartet verworfen/lane-unbekannt", name, plan, sofort)
		}
	}
	// Gegenprobe: in eine gewöhnliche Lane geht es durch.
	offen := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","cards":[{"id":"k1","columnId":"col-ready"}]},
	 {"id":"alt","cards":[]},{"id":"__backlog__","cards":[]}]}}`)
	plan, sofort := verschoben.baue(offen)
	if sofort != nil || plan.op["payload"].(map[string]any)["toSprintId"] != "alt" {
		t.Fatalf("offene Ziel-Lane: %+v %v", sofort, plan)
	}
	if g := plan.bindung(offen); g != "" {
		t.Fatalf("Bindung unverändert: %q", g)
	}
	// Unter der Sperre archiviert: neu planen statt in die archivierte Lane.
	unterSperre := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","cards":[{"id":"k1","columnId":"col-ready"}]},
	 {"id":"alt","archived":true,"cards":[]},{"id":"__backlog__","cards":[]}]}}`)
	if g := plan.bindung(unterSperre); g != rwGrundStandGeaendert {
		t.Fatalf("Ziel-Lane unter der Sperre archiviert: %q", g)
	}
}

// Dasselbe für die Spalte: eine Spalte, die der Hinweg nicht spiegelt
// (ungültige Kennung), ist kein Ziel — verworfen mit spalte-unbekannt.
func TestRueckweg842_VerschobenInNichtGespiegelteSpalteVerworfen(t *testing.T) {
	verschoben := &anwendenEintrag{OpID: "v", Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtVerschoben, Stelle: "ort",
		Vorher:  json.RawMessage(`{"lane":"s1","spalte":"col-ready","nach":null}`),
		Nachher: json.RawMessage(`{"lane":"s1","spalte":"col intern","nach":null}`)}
	b := rwBoard827(t, `{"board":{"columnDefs":[{"id":"col-ready","title":"Ready"},{"id":"col intern","title":"Intern"}],
	 "sprints":[{"id":"s1","cards":[{"id":"k1","columnId":"col-ready"}]},{"id":"__backlog__","cards":[]}]}}`)
	plan, sofort := verschoben.baue(b)
	if sofort == nil || sofort.ergebnis != rwVerworfen || sofort.grund != rwGrundSpalteUnbekannt {
		t.Fatalf("plan %v, sofort %+v — erwartet verworfen/spalte-unbekannt", plan, sofort)
	}
}
