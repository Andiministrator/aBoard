package schatten

// Karte #826 (ADR-0141 Schritt 2b): Spiegel im Hinweg (Format 3), Eingang,
// Anwenden außen und die Spiegel-Schranke.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

const innen826 = `{"version":"2","board":{"id":"b","title":"Projekt X","nextDisplayId":9,
 "identities":[{"label":"Gerd"}],
 "columnDefs":[{"id":"col-backlog","title":"Backlog"},{"id":"col-ready","title":"Ready"},{"id":"sp-review","title":"Review"}],
 "sprints":[
  {"id":"s1","title":"Sprint 1","kind":"sprint","cards":[
    {"id":"k1","displayId":1,"title":"Angebot Müller","description":"- [ ] Preis\n- [x] Termin","columnId":"col-ready",
     "priority":"hoch","critical":true,"blocked":false,"labels":["kunde",5],"owner":"Gerd","assignees":["Anna","external:x@y.de"],
     "startDate":"2026-10-01","startTime":"09:00","dueDate":"2026-10-20","dueTime":"bad","archived":false,
     "agentContext":"geheim","links":[{"targetId":"k2","type":"related"}],"qa":true,"effort":3,
     "attachments":[{"id":"a1"},{"id":"a2"}],
     "comments":[{"id":"c1","author":"Andi","authorEmail":"andi@firma.de","text":"intern","created":"2026-10-01","kind":"extern"},
                 {"id":"c2","author":"extern: Tom","text":"von außen","created":"2026-10-02","edited":"2026-10-03"},
                 {"id":"","author":"x","text":"ohne id","created":"x"}]},
    {"id":"k2","displayId":2,"title":"Geheim Kunde","columnId":"sp-review","confidential":true},
    {"id":"meta","kind":"sprint-meta","metaType":"start","title":"Start","columnId":"col-backlog"},
    {"id":"k4","displayId":4,"title":"Alt","columnId":"col-ready","archived":true},
    {"id":"k5","displayId":5,"title":"Ohne Spalte","columnId":"gibt-es-nicht","archived":true}]},
  {"id":"s-arch","title":"Alt","kind":"lane","archived":true,"cards":[{"id":"k6","title":"In archivierter Lane","columnId":"col-ready"}]},
  {"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[
    {"id":"k3","displayId":3,"title":"Idee","columnId":"col-backlog"},
    {"id":"kd","title":"Doppelt","columnId":"col-backlog"},{"id":"kd","title":"Doppelt 2","columnId":"col-backlog"}]}],
 "termine":[
   {"id":"t1","titel":"Messe","datum":"2026-11-02","bisDatum":"2026-11-03","von":"10:00","bis":"x","ort":"Halle","personen":["Gerd"],"karteId":"k1","erstellt":"2026-10-01"},
   {"id":"t2","titel":"Geheimtermin","datum":"2026-11-04","karteId":"k2"},
   {"id":"t3","titel":"Eigen vertraulich","datum":"2026-11-05","confidential":true},
   {"id":"t4","titel":"Zur archivierten","datum":"2026-11-06","karteId":"k4"},
   {"id":"t5","titel":"Ohne Karte","datum":"2026-11-07"}]}}`

// kennung826: wie das Skript (SHA-256 statt HMAC, der Server sieht nur das Format).
func kennung826(innen string) string {
	if innen == BacklogLane || IstStandardSpalte(innen) {
		return innen
	}
	h := sha256.Sum256([]byte("826|" + innen))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func TestSpiegel826_BauInnen(t *testing.T) {
	erg, err := BaueV2Mit([]byte(innen826), true)
	if err != nil {
		t.Fatal(err)
	}
	karten := map[string]SpiegelKarte{}
	termine := map[string]SpiegelTermin{}
	for _, s := range erg.Spiegel {
		switch v := s.(type) {
		case SpiegelKarte:
			karten[v.ID] = v
		case SpiegelTermin:
			termine[v.ID] = v
		}
	}
	// Spiegel: k1, k3, k4 (archiviert); nicht k2 (vertraulich), meta, k5 (ohne Ort),
	// k6 (archivierte Lane), kd (doppelt → vertraulich).
	if len(karten) != 3 || karten["k1"].ID == "" || karten["k3"].ID == "" || !karten["k4"].Archived {
		t.Fatalf("Spiegelkarten: %+v", karten)
	}
	k1 := karten["k1"]
	if k1.Lane != "s1" || k1.Spalte != "col-ready" || k1.Position != 0 || k1.Title != "Angebot Müller" ||
		k1.Description != "- [ ] Preis\n- [x] Termin" || *k1.Priority != "hoch" || !k1.Critical || k1.Blocked ||
		len(k1.Labels) != 1 || k1.Owner != "Gerd" || len(k1.Assignees) != 2 || k1.Anhaenge != 2 ||
		*k1.StartDate != "2026-10-01" || *k1.StartTime != "09:00" || *k1.DueDate != "2026-10-20" || k1.DueTime != nil {
		t.Errorf("k1: %+v", k1)
	}
	if len(k1.Comments) != 2 || k1.Comments[0].Herkunft != HerkunftInnen || k1.Comments[1].Herkunft != HerkunftAussen ||
		*k1.Comments[1].Edited != "2026-10-03" {
		t.Errorf("Kommentare: %+v", k1.Comments)
	}
	roh, _ := json.Marshal(erg.Spiegel)
	for _, verboten := range []string{"andi@firma.de", "authorEmail", "geheim", "agentContext", "links", `"qa"`, "effort", `"kind"`, "erstellt"} {
		if bytes.Contains(roh, []byte(verboten)) {
			t.Errorf("%q geht hinaus: %s", verboten, roh)
		}
	}
	if karten["k4"].Position != 3 {
		t.Errorf("Position k4: %d", karten["k4"].Position)
	}
	// Termine: t1 (Karte k1 ist Spiegel), t5; nicht t2 (Karte vertraulich), t3, t4 (Karte archiviert).
	if len(termine) != 2 || *termine["t1"].Karte != "k1" || *termine["t1"].BisDatum != "2026-11-03" ||
		*termine["t1"].Von != "10:00" || termine["t1"].Bis != nil || *termine["t1"].Ort != "Halle" || termine["t5"].Karte != nil {
		t.Errorf("Spiegeltermine: %+v", termine)
	}
	// Schatten: nur die vertraulichen und nicht baubaren — k2, kd, t2, t3.
	ids := map[string]bool{}
	for _, s := range erg.Schatten {
		ids[s.ID] = true
		if !s.Vertraulich {
			t.Errorf("nicht vertraulicher Schatten im Format 3: %+v", s)
		}
	}
	if len(erg.Schatten) != 4 || !ids["k2"] || !ids["kd"] || !ids["t2"] || !ids["t3"] {
		t.Errorf("Schatten: %v", ids)
	}
	// k5 (archiviert, Spalte fehlt) zählt als ohne Ort.
	if erg.OhneOrt != 1 || erg.OhneSpalte != 1 {
		t.Errorf("ohne Ort: %d/%d", erg.OhneOrt, erg.OhneSpalte)
	}

	// Format 2 (ohne Spiegel) bleibt wie in 2a: alle als Schatten, Spiegel leer.
	alt, _ := BaueV2([]byte(innen826))
	if len(alt.Spiegel) != 0 || len(alt.Schatten) <= len(erg.Schatten) {
		t.Errorf("Format 2: %d Spiegel, %d Schatten", len(alt.Spiegel), len(alt.Schatten))
	}
}

// skript826 bildet aus einem Bau den Rumpf des PUT, wie das Skript ihn schickt.
func skript826(t *testing.T, erg *ErgebnisV2, titel *string) []byte {
	t.Helper()
	s := erg.Struktur
	s.Titel = titel
	for i := range s.Lanes {
		s.Lanes[i].ID = kennung826(s.Lanes[i].ID)
	}
	for i := range s.Spalten {
		s.Spalten[i].ID = kennung826(s.Spalten[i].ID)
	}
	var schattenListe []map[string]any
	for _, e := range erg.Schatten {
		b, _ := json.Marshal(e)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		delete(m, "id")
		m["schattenId"] = kennung826(e.ID)
		if e.Lane != nil {
			m["lane"], m["spalte"] = kennung826(*e.Lane), kennung826(*e.Spalte)
		}
		schattenListe = append(schattenListe, m)
	}
	spiegel := []map[string]any{}
	for _, e := range erg.Spiegel {
		b, _ := json.Marshal(e)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		m["spiegelId"] = kennung826(m["id"].(string))
		delete(m, "id")
		if m["art"] == ArtKarte {
			m["lane"], m["spalte"] = kennung826(m["lane"].(string)), kennung826(m["spalte"].(string))
		} else if k, ok := m["karte"].(string); ok {
			m["karte"] = kennung826(k)
		}
		spiegel = append(spiegel, m)
	}
	if schattenListe == nil {
		schattenListe = []map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{"version": "v1.55.0", "struktur": s, "schatten": schattenListe,
		"spiegel": spiegel, "rueckweg_bis": nil})
	return body
}

func leereKopie826() []byte {
	return []byte(`{"version":"2","board":{"id":"aussen","title":"Projekt X","nextDisplayId":1,"sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[]}]}}`)
}

type aussen826 struct {
	Board struct {
		Title         string                       `json:"title"`
		NextDisplayID int                          `json:"nextDisplayId"`
		Sprints       []map[string]json.RawMessage `json:"sprints"`
		Termine       []map[string]any             `json:"termine"`
	} `json:"board"`
}

func (a aussen826) karten(t *testing.T) map[string]map[string]any {
	t.Helper()
	aus := map[string]map[string]any{}
	for _, l := range a.Board.Sprints {
		var cards []map[string]any
		_ = json.Unmarshal(l["cards"], &cards)
		for _, k := range cards {
			k["_lane"] = zeichenkette(l["id"])
			aus[k["title"].(string)] = k
		}
	}
	return aus
}

func wende826(t *testing.T, state []byte, body []byte) ([]byte, *SpiegelAbgleich, aussen826) {
	t.Helper()
	k, err := LiesEingangV2(body, "v1.55.0")
	if err != nil {
		t.Fatalf("Eingang: %v", err)
	}
	neu, ab, err := KopieAnwenden(state, k)
	if err != nil {
		t.Fatalf("Anwenden: %v", err)
	}
	var a aussen826
	if err := json.Unmarshal(neu, &a); err != nil {
		t.Fatal(err)
	}
	return neu, ab, a
}

func TestSpiegel826_EingangUndAnwenden(t *testing.T) {
	erg, _ := BaueV2Mit([]byte(innen826), true)
	titel := "Projekt X (neu)"
	body := skript826(t, erg, &titel)
	neu, ab, a := wende826(t, leereKopie826(), body)
	if ab.Neu != 5 || ab.Geaendert != 0 || ab.Entfernt != 0 || ab.Anzahl != 5 {
		t.Errorf("Abgleich: %+v", ab)
	}
	if a.Board.Title != "Projekt X (neu)" {
		t.Errorf("Titel (F-2140): %q", a.Board.Title)
	}
	k := a.karten(t)
	k1 := k["Angebot Müller"]
	if k1["id"] != AbgeleiteteID(kennung826("k1")) || k1["_lane"] != AbgeleiteteID(kennung826("s1")) ||
		k1["columnId"] != "col-ready" || k1["spiegel"].(map[string]any)["id"] != kennung826("k1") ||
		k1["spiegel"].(map[string]any)["anhaenge"] != float64(2) {
		t.Errorf("k1 außen: %+v", k1)
	}
	if _, ok := k["Geheim Kunde"]; ok {
		t.Error("vertrauliche Karte steht außen als Karte")
	}
	// displayId aus dem Zähler außen, in Reihenfolge der Menge; Zähler rückt nach.
	nrs := map[float64]bool{}
	for _, x := range k {
		nrs[x["displayId"].(float64)] = true
	}
	if !nrs[1] || !nrs[2] || !nrs[3] || a.Board.NextDisplayID != 4 {
		t.Errorf("displayIds: %v next %d", nrs, a.Board.NextDisplayID)
	}
	// Termin t1 zeigt auf die Außen-ID von k1.
	var t1 map[string]any
	for _, x := range a.Board.Termine {
		if x["titel"] == "Messe" {
			t1 = x
		}
	}
	if t1 == nil || t1["karteId"] != AbgeleiteteID(kennung826("k1")) || t1["ort"] != "Halle" {
		t.Errorf("Termin außen: %+v", a.Board.Termine)
	}

	// Zweiter Lauf mit derselben Menge: byte-gleich, nichts geändert.
	neu2, ab2, _ := wende826(t, neu, body)
	if !bytes.Equal(neu, neu2) || ab2.Neu+ab2.Geaendert+ab2.Entfernt != 0 {
		t.Errorf("zweiter Lauf schreibt: %+v", ab2)
	}

	// Innen: k1 umbenannt, k3 vertraulich, Lane gewechselt für k4.
	innen2 := strings.Replace(innen826, `"title":"Angebot Müller"`, `"title":"Angebot Müller 2"`, 1)
	innen2 = strings.Replace(innen2, `"title":"Idee","columnId":"col-backlog"`, `"title":"Idee","columnId":"col-backlog","confidential":true`, 1)
	erg2, _ := BaueV2Mit([]byte(innen2), true)
	_, ab3, a3 := wende826(t, neu2, skript826(t, erg2, &titel))
	k3 := a3.karten(t)
	if _, ok := k3["Idee"]; ok {
		t.Error("vertraulich gewordene Karte steht noch außen (§9)")
	}
	if x := k3["Angebot Müller 2"]; x == nil || x["displayId"] != k1["displayId"] || x["id"] != k1["id"] {
		t.Errorf("Umbenennen behält ID und Nummer nicht: %+v", x)
	}
	if ab3.Geaendert != 1 || ab3.Entfernt != 1 || ab3.Neu != 0 {
		t.Errorf("Abgleich nach Änderung: %+v", ab3)
	}
}

func TestSpiegel826_UnmarkierteBleiben(t *testing.T) {
	erg, _ := BaueV2Mit([]byte(innen826), true)
	body := skript826(t, erg, nil)
	neu, _, _ := wende826(t, leereKopie826(), body)
	// Außen liegt eine Karte ohne Marke im Backlog (z. B. aus 2a von einem Admin).
	var st map[string]any
	_ = json.Unmarshal(neu, &st)
	b := st["board"].(map[string]any)
	for _, l := range b["sprints"].([]any) {
		lm := l.(map[string]any)
		if lm["id"] == BacklogLane {
			lm["cards"] = append(lm["cards"].([]any), map[string]any{"id": "eigene", "title": "Eigene", "columnId": "col-backlog"})
		}
	}
	mit, _ := json.Marshal(st)
	_, ab, a := wende826(t, mit, body)
	if a.karten(t)["Eigene"] == nil || ab.Entfernt != 0 {
		t.Errorf("Karte ohne Marke entfernt: %+v", ab)
	}
	// Eine leere Menge entfernt alle Spiegel, die eigene bleibt.
	leer, _ := BaueV2Mit([]byte(`{"version":"2","board":{"id":"b","title":"X","sprints":[{"id":"__backlog__","kind":"backlog","cards":[]}]}}`), true)
	_, ab2, a2 := wende826(t, mit, skript826(t, leer, nil))
	if len(a2.karten(t)) != 1 || a2.karten(t)["Eigene"] == nil || ab2.Entfernt != 5 {
		t.Errorf("leere Menge: %v %+v", a2.karten(t), ab2)
	}
}

func TestSpiegel826_LaneFaelltImSelbenLaufWeg(t *testing.T) {
	erg, _ := BaueV2Mit([]byte(innen826), true)
	neu, _, _ := wende826(t, leereKopie826(), skript826(t, erg, nil))
	// Innen fällt Sprint 1 weg; k1 wandert in den Backlog.
	innen2 := `{"version":"2","board":{"id":"b","title":"X","columnDefs":[{"id":"col-backlog","title":"Backlog"},{"id":"col-ready","title":"Ready"}],
	 "sprints":[{"id":"__backlog__","title":"Backlog","kind":"backlog","cards":[{"id":"k1","title":"Angebot Müller","columnId":"col-ready"}]}]}}`
	erg2, _ := BaueV2Mit([]byte(innen2), true)
	body2 := skript826(t, erg2, nil)
	neu2, _, a := wende826(t, neu, body2)
	if len(a.Board.Sprints) != 1 {
		t.Errorf("leer gewordene Lane steht noch: %d Lanes", len(a.Board.Sprints))
	}
	if neu3, _, _ := wende826(t, neu2, body2); !bytes.Equal(neu2, neu3) {
		t.Error("erst der nächste Lauf ist fertig")
	}
}

func TestSpiegel826_EingangLehntAb(t *testing.T) {
	erg, _ := BaueV2Mit([]byte(innen826), true)
	gut := skript826(t, erg, nil)
	ersetze := func(alt, neu string) []byte {
		if !bytes.Contains(gut, []byte(alt)) {
			t.Fatalf("Muster %q fehlt", alt)
		}
		return bytes.Replace(gut, []byte(alt), []byte(neu), 1)
	}
	faelle := map[string][]byte{
		"unbekannter Schlüssel": ersetze(`"anhaenge":2`, `"anhaenge":2,"agentContext":"x"`),
		// Ab 2c ist ursprung eine Außen-ID; abgelehnt wird nur eine ungültige.
		"ursprung ungültig":     ersetze(`"ursprung":null`, `"ursprung":"a b"`),
		"herkunft falsch":       ersetze(`"herkunft":"innen"`, `"herkunft":"irgendwo"`),
		"position negativ":      ersetze(`"position":0`, `"position":-1`),
		"lane fremd":            ersetze(`"lane":"`+kennung826("s1")+`"`, `"lane":"`+kennung826("x")+`"`),
		"Uhrzeit ohne Datum":    ersetze(`"startDate":"2026-10-01"`, `"startDate":null`),
		"Termin vor Datum":      ersetze(`"bisDatum":"2026-11-03"`, `"bisDatum":"2026-10-01"`),
		"karte kein Spiegel":    ersetze(`"karte":"`+kennung826("k1")+`"`, `"karte":"`+kennung826("k2")+`"`),
		"Kennung auch Schatten": ersetze(`"spiegelId":"`+kennung826("k1")+`"`, `"spiegelId":"`+kennung826("k2")+`"`),
		"titel leer Termin":     ersetze(`"titel":"Messe"`, `"titel":" "`),
		"Struktur-Titel":        ersetze(`"struktur":{`, `"struktur":{"titel":" x",`),
	}
	for name, body := range faelle {
		if _, err := LiesEingangV2(body, "v1.55.0"); err == nil {
			t.Errorf("%s: angenommen", name)
		} else if f, ok := err.(*EingangFehler); !ok || f.Status != 422 {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := LiesEingangV2(gut, "v1.55.0"); err != nil {
		t.Errorf("gut: %v", err)
	}
}

func TestSpiegel826_Schranke(t *testing.T) {
	erg, _ := BaueV2Mit([]byte(innen826), true)
	vorher, _, _ := wende826(t, leereKopie826(), skript826(t, erg, nil))
	if v, u := SpiegelVerstoesse(vorher, vorher); len(v) != 0 || u {
		t.Fatalf("unverändert: %v %v", v, u)
	}
	k1 := AbgeleiteteID(kennung826("k1"))
	s1 := AbgeleiteteID(kennung826("s1"))
	aendere := func(f func(b map[string]any)) []byte {
		var st map[string]any
		_ = json.Unmarshal(vorher, &st)
		f(st["board"].(map[string]any))
		out, _ := json.Marshal(st)
		return out
	}
	lane := func(b map[string]any, id string) map[string]any {
		for _, l := range b["sprints"].([]any) {
			if l.(map[string]any)["id"] == id {
				return l.(map[string]any)
			}
		}
		t.Fatalf("Lane %s fehlt", id)
		return nil
	}
	karte := func(b map[string]any, id string) map[string]any {
		for _, l := range b["sprints"].([]any) {
			for _, k := range l.(map[string]any)["cards"].([]any) {
				if k.(map[string]any)["id"] == id {
					return k.(map[string]any)
				}
			}
		}
		t.Fatalf("Karte %s fehlt", id)
		return nil
	}
	faelle := []struct {
		name string
		art  string
		f    func(b map[string]any)
	}{
		{"Titel", "karte", func(b map[string]any) { karte(b, k1)["title"] = "anders" }},
		{"vertraulich ein", "karte", func(b map[string]any) { karte(b, k1)["confidential"] = true }},
		{"Marke weg", "karte", func(b map[string]any) { delete(karte(b, k1), "spiegel") }},
		{"Kommentar dazu", "karte", func(b map[string]any) {
			k := karte(b, k1)
			k["comments"] = append(k["comments"].([]any), map[string]any{"id": "neu", "text": "x"})
		}},
		{"gelöscht", "karte", func(b map[string]any) { lane(b, s1)["cards"] = []any{} }},
		{"neue Karte in gespiegelter Lane", "karte", func(b map[string]any) {
			l := lane(b, s1)
			l["cards"] = append(l["cards"].([]any), map[string]any{"id": "neu", "title": "x", "columnId": "col-ready"})
		}},
		{"umsortiert", "karte", func(b map[string]any) {
			c := lane(b, s1)["cards"].([]any)
			c[0], c[1] = c[1], c[0]
		}},
		{"Lane umbenannt", "lane", func(b map[string]any) { lane(b, s1)["title"] = "anders" }},
		{"Spalte umbenannt", "spalte", func(b map[string]any) {
			b["columnDefs"].([]any)[0].(map[string]any)["title"] = "anders"
		}},
		{"Termin geändert", "termin", func(b map[string]any) { b["termine"].([]any)[0].(map[string]any)["ort"] = "anders" }},
		{"Termin gelöscht", "termin", func(b map[string]any) { b["termine"] = []any{} }},
	}
	for _, f := range faelle {
		v, u := SpiegelVerstoesse(vorher, aendere(f.f))
		if u || len(v) == 0 {
			t.Errorf("%s: kein Verstoß", f.name)
			continue
		}
		gefunden := false
		for _, o := range v {
			gefunden = gefunden || o.Art == f.art
		}
		if !gefunden {
			t.Errorf("%s: %v, erwartet Art %s", f.name, v, f.art)
		}
	}
	// Erlaubt: neue Lane ohne Marke mit eigener Karte, Board-Feld, eigener Termin.
	frei := aendere(func(b map[string]any) {
		b["sprints"] = append(b["sprints"].([]any), map[string]any{"id": "eigen", "title": "Eigen", "kind": "lane",
			"cards": []any{map[string]any{"id": "e1", "title": "x", "columnId": "col-ready"}}})
		b["awaitingUser"] = nil
		b["termine"] = append(b["termine"].([]any), map[string]any{"id": "t-eigen", "titel": "x", "datum": "2026-12-01"})
	})
	if v, u := SpiegelVerstoesse(vorher, frei); len(v) != 0 || u {
		t.Errorf("Freies beanstandet: %v %v", v, u)
	}
	if _, u := SpiegelVerstoesse(vorher, []byte(`{"board":5}`)); !u {
		t.Error("unlesbar nicht gemeldet")
	}
}

func TestSpiegel826_OhneSpiegelMarken(t *testing.T) {
	erg, _ := BaueV2Mit([]byte(innen826), true)
	mit, _, _ := wende826(t, leereKopie826(), skript826(t, erg, nil))
	ohne := OhneSpiegelMarken(mit)
	if bytes.Contains(ohne, []byte(`"spiegel"`)) {
		t.Errorf("Marken bleiben: %s", ohne)
	}
	ohneVorher := []byte(`{"version":"2","board":{"id":"x","title":"X","sprints":[]}}`)
	if !bytes.Equal(OhneSpiegelMarken(ohneVorher), ohneVorher) {
		t.Error("Board ohne Marke verändert")
	}
	if got := OhneSpiegelMarken([]byte("kein json spiegel")); string(got) != "kein json spiegel" {
		t.Error("unlesbarer Zustand verändert")
	}
}

// Die Spiegelkarte außen muss die Lade-Migrationen des Browsers schon tragen
// (sonst schickte jeder Browser beim Laden Ops, die die Schranke ablehnt).
// testdata/spiegel-karte-826.json ist die Probe, die
// tests/boardpaar-spiegel-826.test.js durch die Migrationen schickt. Neu
// schreiben: ABOARD_UPDATE_GOLDEN=1 go test ./schatten -run Golden
func TestSpiegel826_GoldenKarte(t *testing.T) {
	prio, start, zeit, edited := "mittel", "2026-10-01", "09:30", "2026-10-03"
	k := &SpiegelKarte{Art: ArtKarte, Lane: "x", Spalte: "col-ready", Title: "Probe", Description: "- [ ] eins",
		Priority: &prio, Critical: true, Labels: []string{"a"}, Owner: "Gerd", Assignees: []string{"Anna"},
		StartDate: &start, StartTime: &zeit, Archived: false, Anhaenge: 1,
		Comments: []SpiegelKommentar{{ID: "c1", Author: "Andi", Text: "t", Created: "2026-10-01", Edited: &edited, Herkunft: HerkunftInnen}}}
	karte := SpiegelKarteAussen(k, strings.Repeat("A", 43), "11111111-2222-5333-8444-555555555555", 7, "col-ready")
	board := map[string]any{"version": "2", "board": map[string]any{"id": "kopie", "title": "Kopie", "nextDisplayId": 8,
		"columnDefs": []any{map[string]any{"id": "col-ready", "title": "Ready", "color": nil, "spiegel": map[string]any{"id": "col-ready"}}},
		"sprints": []any{map[string]any{"id": "__backlog__", "title": "Backlog", "kind": "backlog", "archived": false,
			"spiegel": map[string]any{"id": "__backlog__"}, "cards": []any{karte}}}}}
	neu, _ := json.MarshalIndent(board, "", "  ")
	neu = append(neu, '\n')
	const pfad = "testdata/spiegel-karte-826.json"
	if os.Getenv("ABOARD_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(pfad, neu, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	alt, err := os.ReadFile(pfad)
	if err != nil {
		t.Fatalf("%v (neu schreiben: ABOARD_UPDATE_GOLDEN=1)", err)
	}
	if !bytes.Equal(alt, neu) {
		t.Errorf("%s weicht ab — Eingang und Probe auseinander (neu schreiben: ABOARD_UPDATE_GOLDEN=1, danach den JS-Test laufen lassen)", pfad)
	}
}

// Kritik #826 K-2: Eine geänderte, markierte Kopie mit gleicher ID VOR dem
// Original fiel der Schranke nicht auf (Maps: letzter Eintrag gewinnt).
func TestSpiegel826_SchrankeDoppelteID(t *testing.T) {
	erg, _ := BaueV2Mit([]byte(innen826), true)
	vorher, _, _ := wende826(t, leereKopie826(), skript826(t, erg, nil))
	var st map[string]any
	_ = json.Unmarshal(vorher, &st)
	b := st["board"].(map[string]any)
	faelle := map[string]func(){
		"karte": func() {
			for _, l := range b["sprints"].([]any) {
				lm := l.(map[string]any)
				cards := lm["cards"].([]any)
				if len(cards) > 0 {
					kopie := map[string]any{}
					for k, v := range cards[0].(map[string]any) {
						kopie[k] = v
					}
					kopie["title"] = "MANIPULIERT"
					lm["cards"] = append([]any{kopie}, cards...)
					return
				}
			}
		},
		"termin": func() {
			t0 := b["termine"].([]any)[0].(map[string]any)
			kopie := map[string]any{}
			for k, v := range t0 {
				kopie[k] = v
			}
			kopie["ort"] = "MANIPULIERT"
			b["termine"] = append([]any{kopie}, b["termine"].([]any)...)
		},
		"spalte": func() {
			d0 := b["columnDefs"].([]any)[0].(map[string]any)
			kopie := map[string]any{}
			for k, v := range d0 {
				kopie[k] = v
			}
			// Unveränderte Kopie am Ende: Reihenfolge und Inhalt der ersten
			// Vorkommen bleiben gleich — nur die Zählung je ID fällt auf (Verify
			// V-1; der Eingang hinge sonst an der doppelten Spalten-ID).
			b["columnDefs"] = append(b["columnDefs"].([]any), kopie)
		},
		"lane": func() {
			l0 := b["sprints"].([]any)[0].(map[string]any)
			kopie := map[string]any{"id": l0["id"], "title": "MANIPULIERT", "kind": l0["kind"], "spiegel": l0["spiegel"], "cards": []any{}}
			b["sprints"] = append([]any{kopie}, b["sprints"].([]any)...)
		},
	}
	for art, f := range faelle {
		_ = json.Unmarshal(vorher, &st)
		b = st["board"].(map[string]any)
		f()
		nachher, _ := json.Marshal(st)
		v, _ := SpiegelVerstoesse(vorher, nachher)
		gefunden := false
		for _, o := range v {
			gefunden = gefunden || o.Art == art
		}
		if !gefunden {
			t.Errorf("%s doppelt: kein Verstoß (%v)", art, v)
		}
	}
}

// Kritik #826 K-3: Der Backlog steht außen zuletzt, auch wenn eine eigene Lane
// mit Karten stehen bleibt — wie migrateBacklogPosition im Browser.
func TestSpiegel826_BacklogZuletzt(t *testing.T) {
	erg, _ := BaueV2Mit([]byte(innen826), true)
	body := skript826(t, erg, nil)
	neu, _, _ := wende826(t, leereKopie826(), body)
	var st map[string]any
	_ = json.Unmarshal(neu, &st)
	b := st["board"].(map[string]any)
	b["sprints"] = append(b["sprints"].([]any), map[string]any{"id": "eigen", "title": "Vorschläge", "kind": "lane",
		"cards": []any{map[string]any{"id": "e1", "title": "x", "columnId": "col-backlog"}}})
	mit, _ := json.Marshal(st)
	neu2, _, a := wende826(t, mit, body)
	letzte := a.Board.Sprints[len(a.Board.Sprints)-1]
	if zeichenkette(letzte["id"]) != BacklogLane {
		t.Errorf("Backlog nicht zuletzt: %s", letzte["id"])
	}
	if neu3, _, _ := wende826(t, neu2, body); !bytes.Equal(neu2, neu3) {
		t.Error("zweiter Lauf schreibt die Reihenfolge erneut")
	}
}
