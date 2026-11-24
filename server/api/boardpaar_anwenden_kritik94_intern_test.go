package api

// Sprint-94-Kritik K-1 (systematischer Fall) und V-1: Rückweg auf einem
// inneren Board ohne columnDefs, und die Deutung einer fehlenden Ziel-Lane.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/schatten"
)

func TestRueckwegKritik94_BoardOhneColumnDefs(t *testing.T) {
	// Der Hinweg nimmt für ein Board ohne columnDefs die vier
	// Standardspalten an (schatten/v2.go) — der Rückweg muss dieselben kennen.
	b := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","title":"S1","cards":[{"id":"k1","columnId":"col-ready"}]},
	 {"id":"__backlog__","title":"Backlog","cards":[]}]}}`)
	anlage := &anwendenEintrag{OpID: "a", Objekt: schatten.ArtKarte, ID: schatten.AbgeleiteteID("u1"), Art: schatten.JArtAngelegt,
		Ursprung: "u1", Nachher: json.RawMessage(`{"lane":"s1","spalte":"col-in-arbeit","nach":null,"title":"N","labels":[],"assignees":[]}`)}
	plan, sofort := anlage.baue(b)
	if sofort != nil {
		t.Fatalf("angelegt in Standardspalte: %+v", sofort)
	}
	p := plan.op["payload"].(map[string]any)
	if p["sprintId"] != "s1" || p["columnId"] != "col-in-arbeit" {
		t.Fatalf("Ort: %v", p)
	}
	if kom := p["card"].(map[string]any)["comments"].([]any); len(kom) != 0 {
		t.Fatalf("kein Ersatzort, also kein Hinweis: %v", kom)
	}
	verschoben := &anwendenEintrag{OpID: "v", Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtVerschoben, Stelle: "ort",
		Vorher:  json.RawMessage(`{"lane":"s1","spalte":"col-ready","nach":null}`),
		Nachher: json.RawMessage(`{"lane":"s1","spalte":"col-erledigt","nach":null}`)}
	if _, sofort := verschoben.baue(b); sofort != nil {
		t.Fatalf("verschoben in Standardspalte: %+v", sofort)
	}
	// Unter der Sperre verschwundene Lane: neu planen, nicht ablegen.
	ohneLane := rwBoard827(t, `{"board":{"sprints":[{"id":"__backlog__","cards":[]}]}}`)
	if g := plan.bindung(ohneLane); g != rwGrundStandGeaendert {
		t.Fatalf("Lane unter der Sperre weg: %q", g)
	}
	plan2, sofort := anlage.baue(ohneLane)
	if sofort != nil || plan2.op["payload"].(map[string]any)["sprintId"] != "__backlog__" {
		t.Fatalf("zweiter Versuch: %+v %v", sofort, plan2)
	}
	// F-2270 (Sitzungs-Kritik N-3): eine archivierte Wunsch-Lane gilt wie eine
	// fehlende — der Hinweg spiegelt sie nicht.
	mitArchiv := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","archived":true,"cards":[]},{"id":"__backlog__","cards":[]}]}}`)
	plan3, sofort := anlage.baue(mitArchiv)
	if sofort != nil || plan3.op["payload"].(map[string]any)["sprintId"] != "__backlog__" {
		t.Fatalf("archivierte Wunsch-Lane: %+v %v", sofort, plan3)
	}
	// Unter der Sperre archiviert: neu planen.
	if g := plan.bindung(mitArchiv); g != rwGrundStandGeaendert {
		t.Fatalf("Lane unter der Sperre archiviert: %q", g)
	}
	// Ohne Backlog-Lane gibt es keinen Ersatzort (der Hinweg baute ohne sie
	// keinen Spiegel, schatten.BaueV2) — 422 (HALT), nie verworfen, auch
	// nicht in eine archivierte Lane (früher Verify F-V5/F-V7).
	for name, stand := range map[string]string{
		"nur archivierte und eine andere": `{"board":{"sprints":[{"id":"alt","archived":true,"cards":[]},{"id":"s2","cards":[]}]}}`,
		"alle archiviert":                 `{"board":{"sprints":[{"id":"a1","archived":true,"cards":[]},{"id":"a2","archived":true,"cards":[]}]}}`,
		"ohne jede Lane":                  `{"board":{"sprints":[]}}`,
	} {
		if _, sofort := anlage.baue(rwBoard827(t, stand)); sofort == nil || sofort.fehler == "" {
			t.Fatalf("%s: %+v, erwartet 422", name, sofort)
		}
	}
}

// F-2271 (Sitzungs-Kritik N-4): Meldet der Fold für ein angelegt ziel-fehlt
// oder klemmung, ist das HALT — ein verworfenes angelegt bestätigte das
// Skript, und der nächste Hinweg entfernte die wartende Karte außen.
func TestRueckwegSitzungskritik94_AngelegtNieVerworfen(t *testing.T) {
	a := &BoardsAPI{}
	for _, objekt := range []string{schatten.ArtKarte, schatten.ArtTermin} {
		e := &anwendenEintrag{OpID: "x", Objekt: objekt, ID: schatten.AbgeleiteteID("u1"), Art: schatten.JArtAngelegt, Ursprung: "u1"}
		for _, prot := range []string{`{"art":"ziel-fehlt","sprintId":"s-weg"}`, `{"art":"ziel-fehlt","cardId":"k"}`,
			`{"art":"ziel-fehlt","terminId":"t"}`, `{"art":"klemmung"}`} {
			erg := a.rwDeute(nil, nil, "b", e, nil, puffer827(422, `{"protokoll":[`+prot+`]}`))
			if erg.status == 200 || erg.body["ergebnis"] == rwVerworfen {
				t.Errorf("%s %s: %d %v, erwartet HALT", objekt, prot, erg.status, erg.body)
			}
		}
	}
	// Gegenprobe: ein verschoben mit klemmung bleibt verworfen/spalte-unbekannt.
	v := &anwendenEintrag{OpID: "y", Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtVerschoben, Stelle: "ort"}
	if erg := a.rwDeute(nil, nil, "b", v, nil, puffer827(422, `{"protokoll":[{"art":"klemmung"}]}`)); erg.body["grund"] != rwGrundSpalteUnbekannt {
		t.Fatalf("verschoben/klemmung: %v", erg.body)
	}
}

func TestRueckwegKritik94_ZielLaneFehltIstLaneUnbekannt(t *testing.T) {
	a := &BoardsAPI{}
	e := &anwendenEintrag{OpID: "x", Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtVerschoben, Stelle: "ort"}
	erg := a.rwDeute(nil, nil, "b", e, nil, puffer827(422, `{"protokoll":[{"art":"ziel-fehlt","sprintId":"s-weg"}]}`))
	if erg.status != 200 || erg.body["ergebnis"] != rwVerworfen || erg.body["grund"] != rwGrundLaneUnbekannt {
		t.Fatalf("%d %v, erwartet verworfen/lane-unbekannt", erg.status, erg.body)
	}
	erg = a.rwDeute(nil, nil, "b", e, nil, puffer827(422, `{"protokoll":[{"art":"ziel-fehlt","cardId":"k1"}]}`))
	if erg.body["grund"] != rwGrundKarteWeg {
		t.Fatalf("Karte fehlt: %v", erg.body)
	}
}

// Sitzungs-QA #835 K-1 und Verify V-1: Ist die Backlog-Lane innen (per Op)
// archiviert oder eine Service-Lane, gibt es keinen Ersatzort — baueSpiegel
// nähme ihre Karten nicht mit (stiller Verlust außen). Das angelegt endet mit
// 422 (HALT), statt endlos denselben Ort zu planen (vorher 503) oder die Karte
// unsichtbar abzulegen. K-4: Hinweis mit Ersatztext für leere Spaltentitel
// und benannter Service-Lane.
func TestRueckwegSitzungsQA835_BacklogNurNutzbarAlsErsatzort(t *testing.T) {
	anlage := &anwendenEintrag{OpID: "a", Objekt: schatten.ArtKarte, ID: schatten.AbgeleiteteID("u1"), Art: schatten.JArtAngelegt,
		Ursprung: "u1", Person: "assistent", Nachher: json.RawMessage(`{"lane":"weg","spalte":"col-ready","nach":null,"title":"N","labels":[],"assignees":[]}`)}
	for name, stand := range map[string]string{
		"Backlog archiviert": `{"board":{"sprints":[{"id":"s1","cards":[]},{"id":"__backlog__","archived":true,"cards":[]}]}}`,
		"Backlog service":    `{"board":{"sprints":[{"id":"s1","cards":[]},{"id":"__backlog__","kind":"service","cards":[]}]}}`,
	} {
		if _, sofort := anlage.baue(rwBoard827(t, stand)); sofort == nil || sofort.fehler == "" {
			t.Fatalf("%s: %+v, erwartet 422 (HALT)", name, sofort)
		}
	}
	// Auch die Wunsch-Lane __backlog__ selbst, archiviert: kein Ablegen dort.
	direkt := *anlage
	direkt.Nachher = json.RawMessage(`{"lane":"__backlog__","spalte":"col-ready","nach":null,"title":"N","labels":[],"assignees":[]}`)
	if _, sofort := direkt.baue(rwBoard827(t, `{"board":{"sprints":[{"id":"__backlog__","archived":true,"cards":[]}]}}`)); sofort == nil || sofort.fehler == "" {
		t.Fatalf("Wunsch-Lane archivierter Backlog: %+v, erwartet 422", sofort)
	}
	b := rwBoard827(t, `{"board":{"columnDefs":[{"id":"col-backlog","title":""}],
	 "sprints":[{"id":"svc","title":"Service","kind":"service","cards":[]},{"id":"__backlog__","title":"","cards":[]}]}}`)
	text := ersatzortText(b, anlage, "svc", "col-backlog", "__backlog__", "col-backlog")
	for _, soll := range []string{"„Service“ (Service-Lane, wird nicht gespiegelt)", "„Spalte ohne Titel“", "„Lane ohne Titel / Spalte ohne Titel“"} {
		if !strings.Contains(text, soll) {
			t.Errorf("Hinweis ohne %q: %s", soll, text)
		}
	}
}

// Sitzungs-QA #835, Verify 2 V-1 (Klasse 295): Eine Anlage von außen landet
// innen entweder an einem Ort, dessen Karten der echte Hinweg (BaueV2Mit)
// wieder hinausgibt, oder sie endet mit 422 (HALT) — nie unsichtbar. Der Test
// gleicht das gemeinsame Prädikat schatten.LaneTraegtKartenHinaus gegen den
// Hinweg ab; driftet eine Seite, wird er rot.
func TestRueckwegSitzungsQA835_AnlageImmerSichtbar(t *testing.T) {
	bl := `{"id":"__backlog__","cards":[]}`
	faelle := map[string]struct{ sprints, cols, lane, spalte string }{
		"Kontrolle":                      {`{"id":"s1","cards":[]},` + bl, "", "s1", "col-ready"},
		"Lane kind unbekannt":            {`{"id":"s1","kind":"foo","cards":[]},` + bl, "", "s1", "col-ready"},
		"Lane kind backlog, eigene ID":   {`{"id":"s1","kind":"backlog","cards":[]},` + bl, "", "s1", "col-ready"},
		"Lane kind lane":                 {`{"id":"s1","kind":"lane","cards":[]},` + bl, "", "s1", "col-ready"},
		"Lane-ID ungültig":               {`{"id":"s 1","cards":[]},` + bl, "", "s 1", "col-ready"},
		"Lane archiviert":                {`{"id":"s1","archived":true,"cards":[]},` + bl, "", "s1", "col-ready"},
		"Lane service":                   {`{"id":"s1","kind":"service","cards":[]},` + bl, "", "s1", "col-ready"},
		"Spalte ungültig":                {`{"id":"s1","cards":[]},` + bl, `[{"id":"col.x","title":"X"},{"id":"col-backlog","title":"B"}]`, "s1", "col.x"},
		"Spalte fehlt, kein col-backlog": {`{"id":"s1","cards":[]},` + bl, `[{"id":"col.x","title":"X"},{"id":"abc","title":"A"}]`, "s1", "weg"},
		"Backlog kind foo":               {`{"id":"s1","cards":[]},{"id":"__backlog__","kind":"foo","cards":[]}`, "", "weg", "col-ready"},
		"Backlog archiviert":             {`{"id":"s1","cards":[]},{"id":"__backlog__","archived":true,"cards":[]}`, "", "weg", "col-ready"},
		"Backlog service":                {`{"id":"s1","cards":[]},{"id":"__backlog__","kind":"service","cards":[]}`, "", "weg", "col-ready"},
	}
	for name, f := range faelle {
		cols := ""
		if f.cols != "" {
			cols = `"columnDefs":` + f.cols + `,`
		}
		stand := `{"version":"2","board":{"id":"b",` + cols + `"sprints":[` + f.sprints + `]}}`
		anlage := &anwendenEintrag{OpID: "a", Objekt: schatten.ArtKarte, ID: schatten.AbgeleiteteID("u1"), Art: schatten.JArtAngelegt,
			Ursprung: "u1", Person: "assistent",
			Nachher: json.RawMessage(`{"lane":"` + f.lane + `","spalte":"` + f.spalte + `","nach":null,"title":"N","labels":[],"assignees":[]}`)}
		b := rwBoard827(t, stand)
		plan, sofort := anlage.baue(b)
		if sofort != nil {
			if sofort.fehler == "" {
				t.Errorf("%s: %+v — weder Ort noch 422", name, sofort)
			}
			continue
		}
		if g := plan.bindung(b); g != "" {
			t.Errorf("%s: Bindung %q", name, g)
			continue
		}
		p := plan.op["payload"].(map[string]any)
		var st map[string]any
		_ = json.Unmarshal([]byte(stand), &st)
		for _, s := range st["board"].(map[string]any)["sprints"].([]any) {
			if sm := s.(map[string]any); sm["id"] == p["sprintId"] {
				sm["cards"] = append(sm["cards"].([]any), p["card"])
			}
		}
		roh, _ := json.Marshal(st)
		erg, err := schatten.BaueV2Mit(roh, true)
		if err != nil {
			t.Errorf("%s: BaueV2Mit: %v", name, err)
			continue
		}
		sichtbar := false
		for _, s := range erg.Spiegel {
			if k, ok := s.(schatten.SpiegelKarte); ok && k.ID == anlage.ID {
				sichtbar = true
			}
		}
		if !sichtbar {
			t.Errorf("%s: angelegt in %v/%v, aber der Hinweg gibt die Karte nicht hinaus", name, p["sprintId"], p["columnId"])
		}
	}
}
