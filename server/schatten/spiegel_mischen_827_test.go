package schatten

// Karte #827 (ADR-0141 §2, §4; Etappe E2): Merge-Eingang außen. Geprüft wird
// mit echten Journal-Einträgen (JournalEintraege) als Schutz:
//   - Schutz hält: eine Bearbeitung außen, dann ein Eingang mit dem alten
//     Stand von innen → die Außensicht bleibt, wie der Mensch sie hinterließ;
//   - Rundreise: innen angewendet und bestätigt, dann der Eingang mit dem
//     neuen Stand → dieselbe Außensicht („Eingang → Rückweg → Eingang“);
//   - ohne Schutz gilt innen, Begleitfelder bleiben;
//   - wartende Spiegel: bleiben, werden über ursprung markiert, fallen nur in
//     den beiden Fällen aus §2 weg;
//   - Kommentare und Plätze unmarkierter Karten.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

var (
	kA827 = strings.Repeat("A", 43)
	kB827 = strings.Repeat("B", 43)
	kN827 = strings.Repeat("N", 43)
	kT827 = strings.Repeat("T", 43)
)

func karte827(kennung, titel, spalte string, pos int) SpiegelEingang {
	return SpiegelEingang{SpiegelID: kennung, Karte: &SpiegelKarte{Art: ArtKarte, Lane: BacklogLane, Spalte: spalte,
		Position: pos, Title: titel, Labels: []string{"rot"}, Assignees: []string{}, Comments: []SpiegelKommentar{}}}
}

func termin827(kennung, titel string) SpiegelEingang {
	return SpiegelEingang{SpiegelID: kennung, Termin: &SpiegelTermin{Art: ArtTermin, Titel: titel, Datum: "2026-11-01",
		Personen: []string{}}}
}

func menge827(spiegel ...SpiegelEingang) *Kopie {
	return &Kopie{Struktur: Struktur{Lanes: []StrukturLane{{ID: BacklogLane, Titel: "Backlog", Kind: "backlog"}},
		Spalten: []StrukturSpalte{{ID: "col-backlog", Titel: "Backlog"}, {ID: "col-ready", Titel: "Ready"}}},
		Schatten: []Eingang{}, Spiegel: spiegel}
}

func anwenden827(t *testing.T, state []byte, k *Kopie, schutz *Schutz) ([]byte, *SpiegelAbgleich) {
	t.Helper()
	neu, ab, err := KopieAnwendenMit(state, k, schutz)
	if err != nil {
		t.Fatalf("KopieAnwendenMit: %v", err)
	}
	return neu, ab
}

// bearbeite827 ändert den Stand wie ein Mensch außen; fn bekommt das board.
func bearbeite827(t *testing.T, state []byte, fn func(board map[string]any)) []byte {
	t.Helper()
	var st map[string]any
	if err := json.Unmarshal(state, &st); err != nil {
		t.Fatal(err)
	}
	fn(st["board"].(map[string]any))
	neu, _ := json.Marshal(st)
	return neu
}

func karten827(board map[string]any) []any {
	return board["sprints"].([]any)[0].(map[string]any)["cards"].([]any)
}

func karteMit827(board map[string]any, titel string) map[string]any {
	for _, k := range karten827(board) {
		if km := k.(map[string]any); km["title"] == titel {
			return km
		}
	}
	return nil
}

// schutz827: der Schutz, den der Speicher aus dem Journal dieses Schritts
// bauen würde (alle Einträge unbestätigt), samt neuer wartender.
func schutz827(t *testing.T, vorher, nachher []byte, wartend map[string]WartendInfo) *Schutz {
	t.Helper()
	w := map[string]bool{}
	for id := range wartend {
		w[id] = true
	}
	eintraege, neuWartend, ok := JournalEintraege(vorher, nachher, w)
	if !ok {
		t.Fatal("JournalEintraege: unlesbar")
	}
	s := &Schutz{Wartend: map[string]WartendInfo{}}
	for id, info := range wartend {
		s.Wartend[id] = info
	}
	for id, objekt := range neuWartend {
		s.Wartend[id] = WartendInfo{Objekt: objekt, SeqAngelegt: 7}
	}
	for _, e := range eintraege {
		s.MerkeOffen(e.Objekt, e.Kennung, e.Wartend, e.Art, e.Stelle)
	}
	if len(eintraege) == 0 {
		t.Fatal("Bearbeitung erzeugt keinen Journal-Eintrag — der Test prüfte nichts")
	}
	return s
}

func gleich827(t *testing.T, wo string, a, b []byte) {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		t.Fatalf("%s: kein JSON", wo)
	}
	if !reflect.DeepEqual(x, y) {
		t.Errorf("%s: Außensicht verschieden\n ist:  %s\n soll: %s", wo, a, b)
	}
}

func TestMischen827_SchutzUndRundreise(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0), karte827(kB827, "B", "col-backlog", 1), termin827(kT827, "Termin"))
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)

	faelle := []struct {
		name string
		// außen: was der Mensch tut (dazu immer updated/activity, wie der Browser).
		aussen func(b map[string]any)
		// innen: derselbe Stand von innen nach Anwenden und Bestätigen.
		innen func(k *Kopie)
	}{
		{"Titel", func(b map[string]any) { karteMit827(b, "A")["title"] = "A außen" },
			func(k *Kopie) { k.Spiegel[0].Karte.Title = "A außen" }},
		{"Häkchen", func(b map[string]any) {
			karteMit827(b, "A")["description"] = "- [x] eins"
		}, nil},
		{"Label dazu", func(b map[string]any) { karteMit827(b, "A")["labels"] = []any{"rot", "blau"} },
			func(k *Kopie) { k.Spiegel[0].Karte.Labels = []string{"rot", "blau"} }},
		{"Label weg", func(b map[string]any) { karteMit827(b, "A")["labels"] = []any{} },
			func(k *Kopie) { k.Spiegel[0].Karte.Labels = []string{} }},
		{"Spalte", func(b map[string]any) { karteMit827(b, "A")["columnId"] = "col-ready" },
			func(k *Kopie) { k.Spiegel[0].Karte.Spalte = "col-ready" }},
		{"Reihenfolge", func(b map[string]any) {
			l := b["sprints"].([]any)[0].(map[string]any)
			c := l["cards"].([]any)
			l["cards"] = []any{c[1], c[0]}
		}, func(k *Kopie) { k.Spiegel[0].Karte.Position, k.Spiegel[1].Karte.Position = 1, 0 }},
		{"Termin", func(b map[string]any) {
			b["termine"].([]any)[0].(map[string]any)["ort"] = "Saal 2"
		}, func(k *Kopie) { o := "Saal 2"; k.Spiegel[2].Termin.Ort = &o }},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			s1 := bearbeite827(t, s0, func(b map[string]any) {
				f.aussen(b)
				a := karteMit827(b, "A")
				if a == nil {
					a = karteMit827(b, "A außen")
				}
				a["updated"] = "2026-10-13T20:00:00.000Z"
				a["activity"] = []any{map[string]any{"id": "x1", "type": "field"}}
			})
			if f.name == "Häkchen" {
				// Die Checkliste braucht einen Punkt im Ausgangsstand.
				k := menge827(karte827(kA827, "A", "col-backlog", 0), karte827(kB827, "B", "col-backlog", 1), termin827(kT827, "Termin"))
				k.Spiegel[0].Karte.Description = "- [ ] eins"
				basis, _ := anwenden827(t, leereKopie826(), k, nil)
				s1 = bearbeite827(t, basis, func(b map[string]any) { f.aussen(b) })
				schutz := schutz827(t, basis, s1, nil)
				nach, ab := anwenden827(t, s1, k, schutz)
				gleich827(t, "Schutz (Häkchen)", nach, s1)
				if ab.Geaendert != 0 {
					t.Errorf("Häkchen: geaendert=%d", ab.Geaendert)
				}
				return
			}
			schutz := schutz827(t, s0, s1, nil)

			// 1. Schutz hält: alter Stand von innen, Außensicht bleibt.
			nach, ab := anwenden827(t, s1, k0, schutz)
			gleich827(t, "Schutz", nach, s1)
			if ab.Geaendert != 0 || ab.Neu != 0 || ab.Entfernt != 0 {
				t.Errorf("Schutz: Abgleich %+v", ab)
			}

			// 2. Rundreise: innen angewendet und bestätigt → dieselbe Außensicht.
			k1 := menge827(karte827(kA827, "A", "col-backlog", 0), karte827(kB827, "B", "col-backlog", 1), termin827(kT827, "Termin"))
			f.innen(k1)
			nach, _ = anwenden827(t, s1, k1, &Schutz{})
			gleich827(t, "Rundreise", nach, s1)
		})
	}
}

// Ohne offenen Eintrag gilt innen — die Begleitfelder des Bestands bleiben.
func TestMischen827_OhneSchutzGiltInnen(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0))
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)
	s1 := bearbeite827(t, s0, func(b map[string]any) {
		a := karteMit827(b, "A")
		a["title"] = "außen, schon bestätigt"
		a["updated"] = "2026-10-13T20:00:00.000Z"
		a["claim"] = map[string]any{"session": "s"}
	})
	nach, ab := anwenden827(t, s1, k0, &Schutz{})
	var st map[string]any
	_ = json.Unmarshal(nach, &st)
	a := karteMit827(st["board"].(map[string]any), "A")
	if a == nil || a["updated"] != "2026-10-13T20:00:00.000Z" || a["claim"] == nil {
		t.Fatalf("innen nicht durchgesetzt oder Begleitfelder verloren: %v", a)
	}
	if ab.Geaendert != 1 {
		t.Errorf("geaendert=%d", ab.Geaendert)
	}
}

func TestMischen827_Kommentare(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0))
	k0.Spiegel[0].Karte.Comments = []SpiegelKommentar{{ID: "c0", Author: "Innen", Text: "von innen", Created: "2026-10-01", Herkunft: HerkunftInnen}}
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)
	s1 := bearbeite827(t, s0, func(b map[string]any) {
		a := karteMit827(b, "A")
		a["comments"] = append(a["comments"].([]any), map[string]any{"id": "c1", "author": "Gerd", "text": "von außen",
			"created": "2026-10-13", "authorEmail": "gerd@team.example"})
	})
	schutz := schutz827(t, s0, s1, nil)
	nach, _ := anwenden827(t, s1, k0, schutz)
	gleich827(t, "Kommentar offen", nach, s1)

	// Innen angekommen (Autor „extern: Gerd“, Herkunft außen), bestätigt: der
	// Kommentar kommt von innen, behält aber seine authorEmail.
	k1 := menge827(karte827(kA827, "A", "col-backlog", 0))
	k1.Spiegel[0].Karte.Comments = []SpiegelKommentar{k0.Spiegel[0].Karte.Comments[0],
		{ID: "c1", Author: "extern: Gerd", Text: "von außen", Created: "2026-10-13", Herkunft: HerkunftAussen}}
	nach, _ = anwenden827(t, s1, k1, &Schutz{})
	var st map[string]any
	_ = json.Unmarshal(nach, &st)
	cs := karteMit827(st["board"].(map[string]any), "A")["comments"].([]any)
	c1 := cs[1].(map[string]any)
	if len(cs) != 2 || c1["author"] != "extern: Gerd" || c1["authorEmail"] != "gerd@team.example" || c1["herkunft"] != HerkunftAussen {
		t.Fatalf("Kommentare nach Rundreise: %v", cs)
	}

	// Ohne offenen Eintrag und nicht innen: Der Kommentar fällt weg (innen
	// gelöscht oder nie angekommen).
	nach, _ = anwenden827(t, s1, k0, &Schutz{})
	_ = json.Unmarshal(nach, &st)
	if cs := karteMit827(st["board"].(map[string]any), "A")["comments"].([]any); len(cs) != 1 {
		t.Fatalf("Kommentar ohne Eintrag blieb: %v", cs)
	}
}

// Eine außen angelegte Karte: wartet, wird über ursprung markiert, oder
// fällt in den beiden Fällen aus §2 weg.
func TestMischen827_Wartend(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0), karte827(kB827, "B", "col-backlog", 1))
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)
	s1 := bearbeite827(t, s0, func(b map[string]any) {
		l := b["sprints"].([]any)[0].(map[string]any)
		c := l["cards"].([]any)
		// zwischen A und B, wie der Browser sie anlegt
		l["cards"] = []any{c[0], map[string]any{"id": "w1", "displayId": 9, "title": "Neu außen", "columnId": "col-backlog",
			"updated": "2026-10-13T20:00:00.000Z"}, c[1]}
	})
	schutz := schutz827(t, s0, s1, nil)
	if _, ok := schutz.Wartend["w1"]; !ok {
		t.Fatalf("w1 nicht wartend: %+v", schutz.Wartend)
	}

	// 1. Innen noch nicht angekommen: bleibt an seinem Platz.
	nach, ab := anwenden827(t, s1, k0, schutz)
	gleich827(t, "wartet", nach, s1)
	if len(ab.WartendErledigt) != 0 {
		t.Errorf("erledigt: %v", ab.WartendErledigt)
	}

	// 2. Innen angelegt (Hinweg mit ursprung), angelegt bestätigt: die
	// Karte bekommt die Marke, behält ID und Nummer, kein Duplikat.
	u := "w1"
	neuK := karte827(kN827, "Neu außen", "col-backlog", 1)
	neuK.Karte.Ursprung = &u
	neuK.Karte.Labels = []string{}
	k1 := menge827(karte827(kA827, "A", "col-backlog", 0), neuK, karte827(kB827, "B", "col-backlog", 2))
	bestaetigt := &Schutz{Wartend: map[string]WartendInfo{"w1": {Objekt: ArtKarte, SeqAngelegt: 7}}}
	nach, ab = anwenden827(t, s1, k1, bestaetigt)
	var st map[string]any
	_ = json.Unmarshal(nach, &st)
	cs := karten827(st["board"].(map[string]any))
	if len(cs) != 3 {
		t.Fatalf("Karten: %v", cs)
	}
	w := cs[1].(map[string]any)
	if w["id"] != "w1" || w["displayId"] != float64(9) || w["spiegel"].(map[string]any)["id"] != kN827 || w["updated"] == nil {
		t.Fatalf("markiert: %v", w)
	}
	if len(ab.WartendErledigt) != 1 || ab.WartendErledigt[0] != "w1" || ab.Neu != 0 {
		t.Errorf("Abgleich: %+v", ab)
	}

	// 3. ursprung an einem Schatten: innen vertraulich geworden — weg.
	k2 := menge827(karte827(kA827, "A", "col-backlog", 0), karte827(kB827, "B", "col-backlog", 1))
	k2.Schatten = []Eingang{{SchattenID: kN827, Art: ArtKarte, Ursprung: &u}}
	nach, ab = anwenden827(t, s1, k2, bestaetigt)
	if strings.Contains(string(nach), `"w1"`) || len(ab.WartendErledigt) != 1 || ab.Entfernt != 1 {
		t.Fatalf("Schatten-ursprung: %s %+v", nach, ab)
	}

	// 4. Nirgends: weg nur, wenn angelegt bis rueckweg_bis bestätigt war.
	for _, f := range []struct {
		bis  *int64
		weg  bool
		name string
	}{{nil, false, "ohne rueckweg_bis"}, {ptr827(6), false, "seq dahinter"}, {ptr827(7), true, "seq erreicht"}} {
		s := &Schutz{Wartend: bestaetigt.Wartend, RueckwegBis: f.bis}
		nach, ab := anwenden827(t, s1, k0, s)
		if weg := !strings.Contains(string(nach), `"w1"`); weg != f.weg || (len(ab.WartendErledigt) == 1) != f.weg {
			t.Errorf("%s: weg=%v erledigt=%v", f.name, weg, ab.WartendErledigt)
		}
	}
}

func ptr827(n int64) *int64 { return &n }

// Unmarkierte Karten behalten ihren Platz zwischen Spiegeln, auch wenn innen
// die Spiegel umsortiert.
func TestMischen827_PlatzUnmarkierter(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0), karte827(kB827, "B", "col-backlog", 1))
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)
	s1 := bearbeite827(t, s0, func(b map[string]any) {
		l := b["sprints"].([]any)[0].(map[string]any)
		c := l["cards"].([]any)
		l["cards"] = []any{map[string]any{"id": "alt0", "title": "Vorne"}, c[0], map[string]any{"id": "alt1", "title": "Mitte"}, c[1]}
	})
	k1 := menge827(karte827(kA827, "A", "col-backlog", 1), karte827(kB827, "B", "col-backlog", 0))
	nach, _ := anwenden827(t, s1, k1, nil)
	var st map[string]any
	_ = json.Unmarshal(nach, &st)
	var titel []string
	for _, k := range karten827(st["board"].(map[string]any)) {
		titel = append(titel, k.(map[string]any)["title"].(string))
	}
	if strings.Join(titel, ",") != "Vorne,B,A,Mitte" {
		t.Fatalf("Reihenfolge: %v (Mitte steht hinter ihrem Vorgänger A)", titel)
	}
	// Ein zweiter Lauf ändert nichts.
	nochmal, ab := anwenden827(t, nach, k1, nil)
	if string(nochmal) != string(nach) || ab.Geaendert != 0 {
		t.Errorf("zweiter Lauf nicht stabil: %+v", ab)
	}
}

// vereine827: zwei Schutz-Stände zusammen (offene Stellen und wartende), wie
// der Speicher sie nach zwei Schreibvorgängen außen lesen würde.
func vereine827(a, b *Schutz) *Schutz {
	for k, m := range b.Offen {
		for st := range m {
			if a.Offen == nil {
				a.Offen = map[string]map[string]bool{}
			}
			if a.Offen[k] == nil {
				a.Offen[k] = map[string]bool{}
			}
			a.Offen[k][st] = true
		}
	}
	if a.Wartend == nil {
		a.Wartend = map[string]WartendInfo{}
	}
	for id, w := range b.Wartend {
		a.Wartend[id] = w
	}
	return a
}

func titelFolge827(board map[string]any) string {
	var titel []string
	for _, k := range karten827(board) {
		titel = append(titel, k.(map[string]any)["title"].(string))
	}
	return strings.Join(titel, ",")
}

func terminMit827(board map[string]any, id string) map[string]any {
	ts, _ := board["termine"].([]any)
	for _, t := range ts {
		if tm := t.(map[string]any); tm["id"] == id {
			return tm
		}
	}
	return nil
}

func boardAus827(t *testing.T, state []byte) map[string]any {
	t.Helper()
	var st map[string]any
	if err := json.Unmarshal(state, &st); err != nil {
		t.Fatal(err)
	}
	return st["board"].(map[string]any)
}

// F-2220: Außen angelegt und bearbeitet (Journal-Einträge mit wartend = 1),
// innen angekommen: Der erste Eingang setzt die Marke und löscht die Zeile in
// boardpaar_wartend. Kommt ein zweiter Eingang, bevor das Skript die Einträge
// bestätigt hat, bleiben Feld und Platz trotzdem geschützt (§4) — für Karte
// und Termin.
func TestMischen827_WartendNachMarke(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0), karte827(kB827, "B", "col-backlog", 1))
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)
	s1 := bearbeite827(t, s0, func(b map[string]any) {
		l := b["sprints"].([]any)[0].(map[string]any)
		c := l["cards"].([]any)
		l["cards"] = []any{c[0], map[string]any{"id": "w1", "displayId": 9, "title": "Neu", "columnId": "col-backlog"}, c[1]}
		ts, _ := b["termine"].([]any)
		b["termine"] = append(ts, map[string]any{"id": "wt", "titel": "T neu", "datum": "2026-11-02", "personen": []any{}})
	})
	s2 := bearbeite827(t, s1, func(b map[string]any) {
		karteMit827(b, "Neu")["title"] = "Neu außen"
		terminMit827(b, "wt")["ort"] = "Saal 9"
	})
	w := map[string]WartendInfo{"w1": {Objekt: ArtKarte, SeqAngelegt: 7}, "wt": {Objekt: ArtTermin, SeqAngelegt: 8}}
	schutz := vereine827(schutz827(t, s0, s1, nil), schutz827(t, s1, s2, w))
	for _, soll := range []struct{ objekt, id, stelle string }{
		{ArtKarte, "w1", JArtAngelegt + "|"}, {ArtKarte, "w1", JArtFeld + "|title"},
		{ArtTermin, "wt", JArtAngelegt + "|"}, {ArtTermin, "wt", JArtTerminFeld + "|ort"},
	} {
		if !schutz.Offen[OffenSchluessel(soll.objekt, soll.id, true)][soll.stelle] {
			t.Fatalf("Journal ohne %v: %v", soll, schutz.Offen)
		}
	}

	// Innen angekommen, aber noch ohne die Änderungen außen: alter Titel, kein
	// Ort, und der Hinweg legt die Karte an eine andere Stelle.
	uK, uT := "w1", "wt"
	nk := karte827(kN827, "Neu", "col-ready", 0)
	nk.Karte.Ursprung, nk.Karte.Labels = &uK, []string{}
	nt := termin827(kT827, "T neu")
	nt.Termin.Datum, nt.Termin.Ursprung = "2026-11-02", &uT
	k1 := menge827(karte827(kA827, "A", "col-backlog", 0), nk, karte827(kB827, "B", "col-backlog", 1), nt)

	pruefe := func(wo string, state []byte) {
		t.Helper()
		b := boardAus827(t, state)
		if f := titelFolge827(b); f != "A,Neu außen,B" {
			t.Errorf("%s: Karten %q, erwartet A,Neu außen,B (Titel und Platz geschützt)", wo, f)
		}
		if k := karteMit827(b, "Neu außen"); k == nil || k["id"] != "w1" || k["columnId"] != "col-backlog" ||
			marke827(k) != kN827 {
			t.Errorf("%s: Karte w1 %v", wo, k)
		}
		if tm := terminMit827(b, "wt"); tm == nil || tm["ort"] != "Saal 9" || marke827(tm) != kT827 {
			t.Errorf("%s: Termin wt %v", wo, tm)
		}
	}
	// 1. Eingang: Zeile steht noch, die Marke wird gesetzt.
	n1, ab := anwenden827(t, s2, k1, schutz)
	pruefe("1. Eingang", n1)
	if strings.Join(ab.WartendErledigt, ",") != "w1,wt" {
		t.Fatalf("WartendErledigt: %v", ab.WartendErledigt)
	}
	// 2. Eingang: Zeile gelöscht, Einträge mit wartend = 1 noch offen.
	schutz2 := &Schutz{Offen: schutz.Offen}
	n2, ab2 := anwenden827(t, n1, k1, schutz2)
	pruefe("2. Eingang", n2)
	if string(n2) != string(n1) || ab2.Geaendert != 0 {
		t.Errorf("2. Eingang änderte etwas: %+v", ab2)
	}
	// Bestätigt: Jetzt gilt innen.
	n3, _ := anwenden827(t, n2, k1, &Schutz{})
	if b := boardAus827(t, n3); karteMit827(b, "Neu") == nil || terminMit827(b, "wt")["ort"] != nil {
		t.Errorf("nach Bestätigung gilt innen nicht: %s", n3)
	}
}

// F-2220, Widerlegungsbedingung: Ein Spiegel, der innen entstanden ist, trägt
// außen die abgeleitete ID. Eine Bearbeitung außen erfasst das Journal unter
// seiner Kennung mit wartend = 0 — nie mit wartend = 1 unter seiner ID. Der
// zusätzliche Schlüssel (ID am Bestand, wartend) schützt dort also nichts;
// Einträge mit wartend = 1 unter anderen IDs oder unter der Kennung ebenso
// wenig.
func TestMischen827_WartendNachMarkeInnenEntstanden(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0), termin827(kT827, "Termin"))
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)
	s1 := bearbeite827(t, s0, func(b map[string]any) {
		karteMit827(b, "A")["title"] = "A außen"
		b["termine"].([]any)[0].(map[string]any)["ort"] = "Saal 2"
	})
	schutz := schutz827(t, s0, s1, nil)
	for k := range schutz.Offen {
		if strings.HasSuffix(k, "\x001") {
			t.Fatalf("Bearbeitung eines Spiegels ergab einen Eintrag mit wartend = 1: %q", k)
		}
	}
	// Fremde Einträge mit wartend = 1: unter der Kennung und unter einer
	// anderen Außen-ID. Sie dürfen A und den Termin nicht schützen.
	fremd := &Schutz{}
	for _, kennung := range []string{kA827, kT827, "w1"} {
		fremd.MerkeOffen(ArtKarte, kennung, true, JArtFeld, "title")
		fremd.MerkeOffen(ArtKarte, kennung, true, JArtAngelegt, "")
		fremd.MerkeOffen(ArtTermin, kennung, true, JArtTerminFeld, "ort")
	}
	nach, _ := anwenden827(t, s1, k0, fremd)
	b := boardAus827(t, nach)
	if karteMit827(b, "A") == nil || b["termine"].([]any)[0].(map[string]any)["ort"] != nil {
		t.Fatalf("fremde wartend-Einträge schützten einen innen entstandenen Spiegel: %s", nach)
	}
	// Gegenstück: Der eigene Eintrag (Kennung, wartend = 0) schützt.
	nach, _ = anwenden827(t, s1, k0, schutz)
	gleich827(t, "eigener Eintrag", nach, s1)
}

// F-2221 K-2: Ein außen bearbeiteter Kommentar, der nicht von innen stammt,
// behält Text und edited, solange kommentar.edit offen ist.
func TestMischen827_KommentarEditGeschuetzt(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0))
	k0.Spiegel[0].Karte.Comments = []SpiegelKommentar{{ID: "c1", Author: "extern: Gerd", Text: "alt", Created: "2026-10-01",
		Herkunft: HerkunftAussen}}
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)
	s1 := bearbeite827(t, s0, func(b map[string]any) {
		c := karteMit827(b, "A")["comments"].([]any)[0].(map[string]any)
		c["text"], c["edited"] = "neu außen", "2026-10-13T20:00:00.000Z"
	})
	schutz := schutz827(t, s0, s1, nil)
	if !schutz.Offen[OffenSchluessel(ArtKarte, kA827, false)][JArtKommentarEdit+"|kommentar:c1"] {
		t.Fatalf("kein kommentar.edit: %v", schutz.Offen)
	}
	nach, ab := anwenden827(t, s1, k0, schutz)
	gleich827(t, "kommentar.edit offen", nach, s1)
	if ab.Geaendert != 0 {
		t.Errorf("geaendert=%d", ab.Geaendert)
	}
	// Bestätigt: Text von innen, ohne edited.
	nach, _ = anwenden827(t, s1, k0, &Schutz{})
	c := karteMit827(boardAus827(t, nach), "A")["comments"].([]any)[0].(map[string]any)
	if c["text"] != "alt" || c["edited"] != nil {
		t.Fatalf("ohne Eintrag gilt innen nicht: %v", c)
	}
}

// F-2221 K-3: confidential bleibt an Karte und Termin nur bei offenem
// Eintrag vertraulich.
func TestMischen827_Vertraulich(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0), termin827(kT827, "Termin"))
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)
	s1 := bearbeite827(t, s0, func(b map[string]any) {
		karteMit827(b, "A")["confidential"] = true
		b["termine"].([]any)[0].(map[string]any)["confidential"] = true
	})
	schutz := schutz827(t, s0, s1, nil)
	for _, objekt := range []string{ArtKarte, ArtTermin} {
		kennung := kA827
		if objekt == ArtTermin {
			kennung = kT827
		}
		if !schutz.Offen[OffenSchluessel(objekt, kennung, false)][JArtVertraulich+"|confidential"] {
			t.Fatalf("%s: kein Eintrag vertraulich: %v", objekt, schutz.Offen)
		}
	}
	nach, _ := anwenden827(t, s1, k0, schutz)
	gleich827(t, "vertraulich offen", nach, s1)

	nach, _ = anwenden827(t, s1, k0, &Schutz{})
	b := boardAus827(t, nach)
	if _, da := karteMit827(b, "A")["confidential"]; da {
		t.Errorf("Karte: confidential ohne Eintrag geblieben")
	}
	if _, da := b["termine"].([]any)[0].(map[string]any)["confidential"]; da {
		t.Errorf("Termin: confidential ohne Eintrag geblieben")
	}
}

// F-2221 K-4: Ein außen angelegter Termin wartet, wird über ursprung
// markiert (ID bleibt) oder fällt in den beiden Fällen aus §2 weg.
func TestMischen827_WartenderTermin(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0))
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)
	s1 := bearbeite827(t, s0, func(b map[string]any) {
		ts, _ := b["termine"].([]any)
		b["termine"] = append(ts, map[string]any{"id": "wt", "titel": "T außen", "datum": "2026-11-02", "personen": []any{}})
	})
	schutz := schutz827(t, s0, s1, nil)
	if w, ok := schutz.Wartend["wt"]; !ok || w.Objekt != ArtTermin {
		t.Fatalf("wt nicht wartend: %+v", schutz.Wartend)
	}
	// 1. Innen noch nicht angekommen: bleibt.
	nach, ab := anwenden827(t, s1, k0, schutz)
	gleich827(t, "wartet", nach, s1)
	if len(ab.WartendErledigt) != 0 {
		t.Errorf("erledigt: %v", ab.WartendErledigt)
	}

	// 2. Innen angelegt (ursprung), angelegt bestätigt: Marke, ID bleibt.
	u := "wt"
	nt := termin827(kT827, "T außen")
	nt.Termin.Datum, nt.Termin.Ursprung = "2026-11-02", &u
	k1 := menge827(karte827(kA827, "A", "col-backlog", 0), nt)
	bestaetigt := &Schutz{Wartend: map[string]WartendInfo{"wt": {Objekt: ArtTermin, SeqAngelegt: 7}}}
	nach, ab = anwenden827(t, s1, k1, bestaetigt)
	ts := boardAus827(t, nach)["termine"].([]any)
	if len(ts) != 1 {
		t.Fatalf("Termine: %v (Duplikat?)", ts)
	}
	if tm := ts[0].(map[string]any); tm["id"] != "wt" || marke827(tm) != kT827 {
		t.Fatalf("markiert: %v", tm)
	}
	if strings.Join(ab.WartendErledigt, ",") != "wt" || ab.Neu != 0 {
		t.Errorf("Abgleich: %+v", ab)
	}

	// 3. ursprung an einem Schatten: weg.
	k2 := menge827(karte827(kA827, "A", "col-backlog", 0))
	k2.Schatten = []Eingang{{SchattenID: kT827, Art: ArtTermin, Ursprung: &u}}
	nach, ab = anwenden827(t, s1, k2, bestaetigt)
	if strings.Contains(string(nach), `"wt"`) || strings.Join(ab.WartendErledigt, ",") != "wt" || ab.Entfernt != 1 {
		t.Fatalf("Schatten-ursprung: %s %+v", nach, ab)
	}

	// 4. Nirgends: weg nur, wenn angelegt bis rueckweg_bis bestätigt war.
	for _, f := range []struct {
		bis  *int64
		weg  bool
		name string
	}{{nil, false, "ohne rueckweg_bis"}, {ptr827(6), false, "seq dahinter"}, {ptr827(7), true, "seq erreicht"}} {
		s := &Schutz{Wartend: bestaetigt.Wartend, RueckwegBis: f.bis}
		nach, ab := anwenden827(t, s1, k0, s)
		weg := !strings.Contains(string(nach), `"wt"`)
		if weg != f.weg || (strings.Join(ab.WartendErledigt, ",") == "wt") != f.weg || (ab.Entfernt == 1) != f.weg {
			t.Errorf("%s: weg=%v erledigt=%v entfernt=%d", f.name, weg, ab.WartendErledigt, ab.Entfernt)
		}
	}
}

// F-2221 K-5: Ein offenes angelegt schützt den Platz. Die Karte wird im
// selben Eingang übernommen, der Hinweg will sie in eine andere Spalte an den
// Anfang — sie bleibt, wo der Mensch sie angelegt hat.
func TestMischen827_PlatzAngelegt(t *testing.T) {
	k0 := menge827(karte827(kA827, "A", "col-backlog", 0), karte827(kB827, "B", "col-backlog", 1))
	s0, _ := anwenden827(t, leereKopie826(), k0, nil)
	s1 := bearbeite827(t, s0, func(b map[string]any) {
		l := b["sprints"].([]any)[0].(map[string]any)
		c := l["cards"].([]any)
		l["cards"] = []any{c[0], map[string]any{"id": "w1", "displayId": 9, "title": "Neu", "columnId": "col-backlog"}, c[1]}
	})
	schutz := schutz827(t, s0, s1, nil)
	u := "w1"
	nk := karte827(kN827, "Neu", "col-ready", 0)
	nk.Karte.Ursprung, nk.Karte.Labels = &u, []string{}
	k1 := menge827(karte827(kA827, "A", "col-backlog", 1), nk, karte827(kB827, "B", "col-backlog", 2))

	nach, ab := anwenden827(t, s1, k1, schutz)
	b := boardAus827(t, nach)
	if f := titelFolge827(b); f != "A,Neu,B" {
		t.Fatalf("Platz nicht geschützt: %q", f)
	}
	if k := karteMit827(b, "Neu"); k["columnId"] != "col-backlog" || k["spiegel"] == nil {
		t.Fatalf("Karte: %v", k)
	}
	if strings.Join(ab.WartendErledigt, ",") != "w1" {
		t.Errorf("erledigt: %v", ab.WartendErledigt)
	}
	// Gegenstück: angelegt bestätigt → der Hinweg platziert.
	nach, _ = anwenden827(t, s1, k1, &Schutz{Wartend: schutz.Wartend})
	b = boardAus827(t, nach)
	if f := titelFolge827(b); f != "Neu,A,B" || karteMit827(b, "Neu")["columnId"] != "col-ready" {
		t.Fatalf("ohne Eintrag: %q %v", f, karteMit827(b, "Neu"))
	}
}

// marke827: spiegel.id eines Objekts, "" ohne Marke (kein Panic in Gegenproben).
func marke827(o map[string]any) string {
	m, _ := o["spiegel"].(map[string]any)
	id, _ := m["id"].(string)
	return id
}
