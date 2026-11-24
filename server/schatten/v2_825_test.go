package schatten

// Karte #825 (ADR-0141 §4): Hinweg v2 — Bau innen, Prüfung außen, Struktur
// in den Board-Zustand der Kopie.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const v2Stand = `{"version":"2","board":{"id":"innen","title":"Innen",
 "identities":[{"label":"Gerd Muster"}],
 "columnDefs":[{"id":"col-backlog","title":"Backlog"},{"id":"col-ready","title":"Ready"},{"id":"spalte-review","title":"Review\u0007 "}],
 "sprints":[
  {"id":"sprint-1","title":"Sprint 1","kind":"sprint","cards":[
    {"id":"k1","displayId":1,"title":"Angebot Müller","columnId":"col-ready","priority":"hoch","owner":"Gerd Muster"},
    {"id":"k2","displayId":2,"title":"Geheim Kunde","columnId":"spalte-review","confidential":true},
    {"id":"meta","kind":"sprint-meta","metaType":"start","title":"📋","columnId":"col-backlog"},
    {"id":"k3","displayId":3,"title":"Ohne Spalte","columnId":"gibt-es-nicht"},
    {"id":"k4","displayId":4,"title":"Archiviert","columnId":"col-ready","archived":true}]},
  {"id":"alt","title":"Alt","kind":"lane","archived":true,"cards":[{"id":"k5","displayId":5,"title":"In alter Lane","columnId":"col-ready"}]},
  {"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[{"id":"k6","displayId":6,"title":"Idee","columnId":"col-backlog"}]}],
 "termine":[{"id":"t1","titel":"Jour fixe","datum":"2026-10-24","von":"10:00"},
            {"id":"t2","titel":"Geheimtermin","datum":"2026-10-25","confidential":true}]}}`

func TestBoardpaar825_BaueV2(t *testing.T) {
	erg, err := BaueV2([]byte(v2Stand))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(erg.Struktur.Lanes); got != 3 {
		t.Fatalf("Lanes: %d, erwartet 3 (auch die archivierte)", got)
	}
	if l := erg.Struktur.Lanes[1]; l.ID != "alt" || !l.Archived || l.Kind != "lane" {
		t.Errorf("archivierte Lane: %+v", l)
	}
	if s := erg.Struktur.Spalten[2]; s.ID != "spalte-review" || s.Titel != "Review" {
		t.Errorf("Spalte mit Steuerzeichen: %+v", s)
	}
	je := map[string]EintragV2{}
	for _, e := range erg.Schatten {
		je[e.ID] = e
	}
	if _, ok := je["meta"]; ok {
		t.Error("Sprint-Meta-Karte geht hinaus (VF-8)")
	}
	for _, weg := range []string{"k3", "k4", "k5"} {
		if _, ok := je[weg]; ok {
			t.Errorf("%s geht hinaus", weg)
		}
	}
	if erg.OhneOrt != 1 || erg.OhneSpalte != 1 || erg.OhneLane != 0 {
		t.Errorf("OhneOrt: %d/%d/%d, erwartet 1 (k3 ohne Spalte)", erg.OhneOrt, erg.OhneSpalte, erg.OhneLane)
	}
	k1 := je["k1"]
	if k1.Vertraulich || *k1.Titel != "Angebot Müller" || *k1.Lane != "sprint-1" || *k1.Spalte != "col-ready" ||
		*k1.Prioritaet != "hoch" || len(k1.Bearbeiter) != 1 || k1.Ursprung != nil {
		t.Errorf("k1: %+v", k1)
	}
	// Beschluss 3: vertraulich MIT Titel, in Lane und Spalte.
	k2 := je["k2"]
	if !k2.Vertraulich || k2.Titel == nil || *k2.Titel != "Geheim Kunde" || *k2.Spalte != "spalte-review" {
		t.Errorf("k2: %+v", k2)
	}
	if t2 := je["t2"]; !t2.Vertraulich || t2.Titel == nil || *t2.Titel != "Geheimtermin" || t2.Lane != nil {
		t.Errorf("t2: %+v", t2)
	}
	// Wire-Format: Termine ohne lane/spalte, ursprung immer da.
	b, _ := json.Marshal(je["t1"])
	if strings.Contains(string(b), `"lane"`) || !strings.Contains(string(b), `"ursprung":null`) {
		t.Errorf("Termin im Draht: %s", b)
	}
}

func TestBoardpaar825_BaueV2_GanzOderFehler(t *testing.T) {
	for name, stand := range map[string]string{
		"Lane doppelt":   `{"board":{"sprints":[{"id":"a","cards":[]},{"id":"a","cards":[]},{"id":"__backlog__","cards":[]}]}}`,
		"Spalte doppelt": `{"board":{"columnDefs":[{"id":"c"},{"id":"c"}],"sprints":[{"id":"__backlog__","cards":[]}]}}`,
		"ohne Backlog":   `{"board":{"sprints":[{"id":"a","cards":[]}]}}`,
		"kein Objekt":    `[]`,
	} {
		if _, err := BaueV2([]byte(stand)); !errors.Is(err, ErrStand) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

var (
	kA = strings.Repeat("A", 43)
	kB = strings.Repeat("B", 43)
	kC = strings.Repeat("C", 43)
	kL = strings.Repeat("L", 43)
	kS = strings.Repeat("S", 43)
)

func v2Body(mod func(m map[string]any)) []byte {
	m := map[string]any{
		"version":      "v1.54.0",
		"rueckweg_bis": nil,
		"spiegel":      []any{},
		"struktur": map[string]any{
			"lanes": []any{
				map[string]any{"id": kL, "titel": "Sprint 1", "kind": "sprint", "archived": false},
				map[string]any{"id": "__backlog__", "titel": "Backlog", "kind": "backlog", "archived": false},
			},
			"spalten": []any{
				map[string]any{"id": "col-ready", "titel": "Ready"},
				map[string]any{"id": kS, "titel": "Review"},
			},
		},
		"schatten": []any{
			map[string]any{"schattenId": kA, "art": "karte", "nr": 1, "titel": "Angebot Müller", "vertraulich": false,
				"lane": kL, "spalte": "col-ready", "prioritaet": "hoch", "startDatum": nil, "startZeit": nil,
				"endeDatum": nil, "endeZeit": nil, "bearbeiter": []any{"Gerd Muster"}, "vonAgent": false, "ursprung": nil},
			map[string]any{"schattenId": kB, "art": "karte", "titel": "Geheim Kunde", "vertraulich": true,
				"lane": "__backlog__", "spalte": kS, "prioritaet": "", "startDatum": nil, "startZeit": nil,
				"endeDatum": nil, "endeZeit": nil, "bearbeiter": []any{}, "vonAgent": false, "ursprung": nil},
			map[string]any{"schattenId": kC, "art": "termin", "titel": "Jour fixe", "vertraulich": false,
				"startDatum": "2026-10-24", "startZeit": "10:00", "endeDatum": nil, "endeZeit": nil,
				"bearbeiter": []any{}, "vonAgent": false, "ursprung": nil},
		},
	}
	if mod != nil {
		mod(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func TestBoardpaar825_LiesEingangV2(t *testing.T) {
	k, err := LiesEingangV2(v2Body(nil), "v1.54.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Schatten) != 3 || len(k.Struktur.Lanes) != 2 {
		t.Fatalf("%+v", k)
	}
	b := k.Schatten[1]
	if !b.Vertraulich || *b.Titel != "Geheim Kunde" || *b.Lane != "__backlog__" || *b.SpalteID != kS || *b.Spalte != "Review" {
		t.Errorf("vertraulicher Schatten: %+v", b)
	}
	if k.Schatten[2].Lane != nil || k.Schatten[2].SpalteID != nil {
		t.Error("Termin mit Ort")
	}

	schatten0 := func(m map[string]any) map[string]any { return m["schatten"].([]any)[0].(map[string]any) }
	lane0 := func(m map[string]any) map[string]any {
		return m["struktur"].(map[string]any)["lanes"].([]any)[0].(map[string]any)
	}
	fälle := map[string]func(m map[string]any){
		"spiegel nicht leer":      func(m map[string]any) { m["spiegel"] = []any{map[string]any{}} },
		"rueckweg_bis fehlt":      func(m map[string]any) { delete(m, "rueckweg_bis") },
		"rueckweg_bis negativ":    func(m map[string]any) { m["rueckweg_bis"] = -1 },
		"fremder Hüllenschlüssel": func(m map[string]any) { m["extra"] = 1 },
		"Lane unbekannt":          func(m map[string]any) { schatten0(m)["lane"] = kC },
		"Spalte unbekannt":        func(m map[string]any) { schatten0(m)["spalte"] = "col-erledigt" },
		"Spalte als Name":         func(m map[string]any) { schatten0(m)["spalte"] = "Ready" },
		// Ab 2c ist ursprung eine Außen-ID; abgelehnt wird nur eine ungültige.
		"ursprung ungültig":   func(m map[string]any) { schatten0(m)["ursprung"] = "a b" },
		"ursprung fehlt":      func(m map[string]any) { delete(schatten0(m), "ursprung") },
		"titel null, offen":   func(m map[string]any) { schatten0(m)["titel"] = nil },
		"Titel verpackt":      func(m map[string]any) { schatten0(m)["titel"] = "www.example.org" },
		"innere ID als Lane":  func(m map[string]any) { lane0(m)["id"] = "sprint-1" },
		"Lane ohne kind":      func(m map[string]any) { delete(lane0(m), "kind") },
		"kind backlog falsch": func(m map[string]any) { lane0(m)["kind"] = "backlog" },
		"Lanetitel mit Rand":  func(m map[string]any) { lane0(m)["titel"] = " x" },
		"Backlog fehlt": func(m map[string]any) {
			s := m["struktur"].(map[string]any)
			s["lanes"] = s["lanes"].([]any)[:1]
		},
	}
	for name, mod := range fälle {
		_, err := LiesEingangV2(v2Body(mod), "v1.54.0")
		var f *EingangFehler
		if !errors.As(err, &f) || f.Status != 422 {
			t.Errorf("%s: %v, erwartet 422", name, err)
		}
	}
	if _, err := LiesEingangV2(v2Body(func(m map[string]any) { m["version"] = "v2.0.0" }), "v1.54.0"); err == nil ||
		err.(*EingangFehler).Status != 409 {
		t.Errorf("Hauptversion: %v", err)
	}
	if _, err := LiesEingangV2(v2Body(func(m map[string]any) { m["rueckweg_bis"] = 17 }), "dev"); err != nil {
		t.Errorf("rueckweg_bis 17: %v", err)
	}
}

// Fixpunkt: Was BaueV2 liefert und das Skript nur in den Kennungen ersetzt,
// nimmt LiesEingangV2 an.
func TestBoardpaar825_Fixpunkt(t *testing.T) {
	erg, err := BaueV2([]byte(v2Stand))
	if err != nil {
		t.Fatal(err)
	}
	kennung := func(innen string) string {
		if innen == BacklogLane || IstStandardSpalte(innen) {
			return innen
		}
		return (innen + strings.Repeat("x", 43))[:43]
	}
	for i := range erg.Struktur.Lanes {
		erg.Struktur.Lanes[i].ID = kennung(erg.Struktur.Lanes[i].ID)
	}
	for i := range erg.Struktur.Spalten {
		erg.Struktur.Spalten[i].ID = kennung(erg.Struktur.Spalten[i].ID)
	}
	var schatten []map[string]any
	for _, e := range erg.Schatten {
		b, _ := json.Marshal(e)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		m["schattenId"] = kennung(e.ID)
		delete(m, "id")
		if e.Lane != nil {
			m["lane"], m["spalte"] = kennung(*e.Lane), kennung(*e.Spalte)
		}
		schatten = append(schatten, m)
	}
	body, _ := json.Marshal(map[string]any{"version": "dev", "struktur": erg.Struktur, "schatten": schatten,
		"spiegel": []any{}, "rueckweg_bis": nil})
	if _, err := LiesEingangV2(body, "dev"); err != nil {
		t.Fatalf("Fixpunkt verletzt: %v", err)
	}
}

func TestBoardpaar825_StrukturAnwenden(t *testing.T) {
	leer := `{"version":"2","board":{"id":"k","title":"K","nextDisplayId":1,"columnDefs":[` +
		`{"id":"col-backlog","title":"Backlog","color":null},{"id":"col-ready","title":"Ready","color":"#E9C85D"},` +
		`{"id":"col-in-arbeit","title":"In Arbeit","color":"#ea580c"},{"id":"col-erledigt","title":"Erledigt","color":"#63D2B0"}],` +
		`"sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[]}]}}`
	s := Struktur{
		Lanes:   []StrukturLane{{ID: kL, Titel: "Sprint 1", Kind: "sprint"}, {ID: "__backlog__", Titel: "Ideen", Kind: "backlog"}},
		Spalten: []StrukturSpalte{{ID: "col-ready", Titel: "Bereit"}, {ID: kS, Titel: "Review"}},
	}
	neu, err := StrukturAnwenden([]byte(leer), s)
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Board struct {
			Sprints    []map[string]any `json:"sprints"`
			ColumnDefs []map[string]any `json:"columnDefs"`
			Title      string           `json:"title"`
		} `json:"board"`
	}
	_ = json.Unmarshal(neu, &st)
	if len(st.Board.Sprints) != 2 || st.Board.Sprints[0]["id"] != AussenLaneID(kL) ||
		st.Board.Sprints[0]["spiegel"].(map[string]any)["id"] != kL || st.Board.Sprints[1]["title"] != "Ideen" {
		t.Errorf("Lanes: %+v", st.Board.Sprints)
	}
	if len(st.Board.ColumnDefs) != 2 || st.Board.ColumnDefs[0]["id"] != "col-ready" || st.Board.ColumnDefs[0]["color"] != "#E9C85D" ||
		st.Board.ColumnDefs[1]["id"] != AussenSpalteID(kS) || st.Board.ColumnDefs[1]["spiegel"].(map[string]any)["id"] != kS {
		t.Errorf("Spalten: %+v", st.Board.ColumnDefs)
	}
	if st.Board.Title != "K" {
		t.Error("Titel verändert")
	}
	// Zweiter Lauf mit derselben Struktur: byte-gleich (kein Schreiben, PK-11).
	nochmal, err := StrukturAnwenden(neu, s)
	if err != nil || string(nochmal) != string(neu) {
		t.Errorf("zweiter Lauf nicht byte-gleich: %v", err)
	}
	// Karten außen bleiben: Eine Lane mit Karte, die aus der Struktur fällt,
	// bleibt (ohne Marke); eine Spalte mit Karte ebenso. Leeres fällt weg.
	mitKarte := strings.Replace(string(neu), `"cards":[]`, `"cards":[{"id":"x","columnId":"`+AussenSpalteID(kS)+`"}]`, 1)
	s2 := Struktur{Lanes: []StrukturLane{{ID: "__backlog__", Titel: "Backlog", Kind: "backlog"}},
		Spalten: []StrukturSpalte{{ID: "col-ready", Titel: "Ready"}}}
	danach, err := StrukturAnwenden([]byte(mitKarte), s2)
	if err != nil {
		t.Fatal(err)
	}
	st.Board.Sprints, st.Board.ColumnDefs = nil, nil // frisch lesen: Unmarshal mischt sonst in alte Maps
	_ = json.Unmarshal(danach, &st)
	// Kritik #826 K-3: die Lane ohne Marke steht VOR dem Backlog (Backlog zuletzt).
	if len(st.Board.Sprints) != 2 || st.Board.Sprints[0]["id"] != AussenLaneID(kL) || st.Board.Sprints[0]["spiegel"] != nil ||
		st.Board.Sprints[1]["id"] != "__backlog__" {
		t.Errorf("Lane mit Karte: %+v", st.Board.Sprints)
	}
	if len(st.Board.ColumnDefs) != 2 || st.Board.ColumnDefs[1]["id"] != AussenSpalteID(kS) || st.Board.ColumnDefs[1]["spiegel"] != nil {
		t.Errorf("Spalte mit Karte: %+v", st.Board.ColumnDefs)
	}
	if !strings.Contains(string(danach), `"id":"x"`) {
		t.Error("Karte verloren")
	}
}

// Kritik A-1: Ohne columnDefs gelten die vier Standardspalten, wie im Browser.
func TestBoardpaar825_BaueV2_OhneSpalten(t *testing.T) {
	stand := `{"board":{"sprints":[{"id":"__backlog__","kind":"backlog","cards":[{"id":"k1","displayId":1,"title":"A","columnId":"col-ready"}]}]}}`
	erg, err := BaueV2([]byte(stand))
	if err != nil {
		t.Fatal(err)
	}
	if len(erg.Struktur.Spalten) != 4 || erg.Struktur.Spalten[2].ID != "col-in-arbeit" || erg.OhneOrt != 0 || len(erg.Schatten) != 1 {
		t.Errorf("%+v", erg)
	}
}

// Kritik A-7: Doppelte IDs außen — Fehler statt Karten einer Lane zu verlieren.
func TestBoardpaar825_StrukturAnwenden_DoppelteIDs(t *testing.T) {
	s := Struktur{Lanes: []StrukturLane{{ID: "__backlog__", Titel: "B", Kind: "backlog"}}, Spalten: []StrukturSpalte{{ID: "col-ready", Titel: "R"}}}
	// Verify V-3: zwei Einträge ohne ID sind keine Dublette.
	if _, err := StrukturAnwenden([]byte(`{"board":{"id":"k","title":"K","sprints":[{"cards":[]},{"cards":[]}]}}`), s); err != nil {
		t.Errorf("ohne ID: %v", err)
	}
	for name, st := range map[string]string{
		"Lane":   `{"board":{"id":"k","title":"K","sprints":[{"id":"x","cards":[{"id":"a"}]},{"id":"x","cards":[{"id":"b"}]}]}}`,
		"Spalte": `{"board":{"id":"k","title":"K","columnDefs":[{"id":"c"},{"id":"c"}],"sprints":[]}}`,
	} {
		if _, err := StrukturAnwenden([]byte(st), s); !errors.Is(err, ErrStand) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBoardpaar825_AbgeleiteteIDs(t *testing.T) {
	// Fester Namensraum: dieselbe Kennung ergibt immer dieselbe ID (das Skript
	// rechnet sie nach). Erwartungswert unabhängig berechnet:
	// python3 -c "import uuid; print(uuid.uuid5(uuid.UUID('17afd522-ad42-40fd-ad04-1406d6de81d2'), 'A'*43))"
	if got := AbgeleiteteID(strings.Repeat("A", 43)); got != uuidV5A43 {
		t.Errorf("AbgeleiteteID: %s, erwartet %s", got, uuidV5A43)
	}
	if AussenLaneID("__backlog__") != "__backlog__" || AussenSpalteID("col-erledigt") != "col-erledigt" {
		t.Error("Klartext-IDs")
	}
}

const uuidV5A43 = "4311993f-f017-5a24-bb62-3e0f4c0d2e3e"

// Kritik Sprint 93 K-4: Eine Lane mit kind backlog und eigener ID (innen
// gültig, Karte #332) geht als gewöhnliche Lane hinaus, mit ihren Karten.
func TestBoardpaar93_BaueV2_BacklogArtMitEigenerID(t *testing.T) {
	stand := `{"board":{"sprints":[{"id":"zweiter-backlog","title":"Ablage","kind":"backlog","cards":[{"id":"k1","displayId":1,"title":"A","columnId":"col-ready"}]},` +
		`{"id":"__backlog__","kind":"backlog","cards":[]}]}}`
	erg, err := BaueV2([]byte(stand))
	if err != nil {
		t.Fatal(err)
	}
	if len(erg.Struktur.Lanes) != 2 || erg.Struktur.Lanes[0].Kind != "lane" || erg.OhneLane != 0 || len(erg.Schatten) != 1 ||
		*erg.Schatten[0].Lane != "zweiter-backlog" {
		t.Errorf("%+v", erg)
	}
}

// Kritik Sprint 93 B-1: Jede gespiegelte Lane trägt archived (true oder
// false) — auch der Backlog der frischen Kopie, der das Feld nicht hatte.
func TestBoardpaar93_StrukturAnwenden_ArchivedImmer(t *testing.T) {
	leer := `{"version":"2","board":{"id":"k","columnDefs":[{"id":"col-ready","title":"Ready"}],` +
		`"sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[]}]}}`
	s := Struktur{Lanes: []StrukturLane{{ID: kL, Titel: "S", Kind: "sprint"}, {ID: "__backlog__", Titel: "B", Kind: "backlog"}},
		Spalten: []StrukturSpalte{{ID: "col-ready", Titel: "Ready"}}}
	neu, err := StrukturAnwenden([]byte(leer), s)
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Board struct {
			Sprints []map[string]any `json:"sprints"`
		} `json:"board"`
	}
	_ = json.Unmarshal(neu, &st)
	for _, l := range st.Board.Sprints {
		if a, ok := l["archived"].(bool); !ok || a {
			t.Errorf("Lane %v: archived = %#v, erwartet false", l["id"], l["archived"])
		}
	}
}

// F-2143: Zwei Lanes ohne ID mit Karten außen — beide bleiben, keine wird
// über vergeben[""] mit der anderen zusammengelegt.
func TestBoardpaar842_StrukturAnwenden_LanesOhneID(t *testing.T) {
	s := Struktur{Lanes: []StrukturLane{{ID: "__backlog__", Titel: "B", Kind: "backlog"}}, Spalten: []StrukturSpalte{{ID: "col-ready", Titel: "R"}}}
	stand := `{"board":{"id":"k","title":"K","sprints":[{"title":"Eins","cards":[{"id":"a","columnId":"col-ready"}]},` +
		`{"title":"Zwei","cards":[{"id":"b","columnId":"col-ready"}]}]}}`
	neu, err := StrukturAnwenden([]byte(stand), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{`"id":"a"`, `"id":"b"`} {
		if !strings.Contains(string(neu), id) {
			t.Errorf("Karte %s verloren: %s", id, neu)
		}
	}
}
