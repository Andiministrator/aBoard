package schatten

// Karte #827 (ADR-0141 §5.1, Etappe E1a): JournalEintraege — je geänderter
// Stelle ein Eintrag, Werte in Hinweg-Form, Lanes/Spalten als Kennung.

import (
	"encoding/json"
	"strings"
	"testing"
)

// kopie827 baut den Zustand einer Kopie: Lane L (Marke "kl"), Backlog (Marke
// "__backlog__"), unmarkierte Lane U; Spalten col-backlog (Marke), col-ready
// (Marke), eigene Spalte X (ohne Marke). Karten als JSON-Objekte je Lane.
func kopie827(l, backlog, u string, termine string) []byte {
	if termine == "" {
		termine = "[]"
	}
	return []byte(`{"version":"2","board":{"id":"b","title":"B","nextDisplayId":50,
"columnDefs":[{"id":"col-backlog","title":"B","spiegel":{"id":"col-backlog"}},{"id":"col-ready","title":"R","spiegel":{"id":"col-ready"}},{"id":"x","title":"X"}],
"sprints":[{"id":"l-aussen","title":"L","spiegel":{"id":"kl"},"cards":[` + l + `]},
{"id":"u","title":"U","cards":[` + u + `]},
{"id":"__backlog__","title":"Backlog","spiegel":{"id":"__backlog__"},"cards":[` + backlog + `]}],
"termine":` + termine + `}}`)
}

func sk(id, kennung, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"id":"` + id + `","displayId":1,"title":"T","description":"","columnId":"col-backlog","labels":[],"assignees":[],"comments":[],"spiegel":{"id":"` + kennung + `"}` + extra + `}`
}

func journal827(t *testing.T, vorher, nachher []byte, wartend map[string]bool) ([]JournalEintrag, map[string]string) {
	t.Helper()
	e, nw, ok := JournalEintraege(vorher, nachher, wartend)
	if !ok {
		t.Fatal("Stand nicht lesbar")
	}
	return e, nw
}

func einzig827(t *testing.T, e []JournalEintrag) JournalEintrag {
	t.Helper()
	if len(e) != 1 {
		b, _ := json.Marshal(e)
		t.Fatalf("genau ein Eintrag erwartet, bekam %d: %s", len(e), b)
	}
	return e[0]
}

func TestJournal827_FeldUndBegleitfelder(t *testing.T) {
	v := kopie827(sk("a", "KA", `"activity":[]`), "", "", "")
	n := kopie827(sk("a", "KA", `"activity":[{"id":"x"}],"updated":"2026-10-13"`), "", "", "")
	if e, _ := journal827(t, v, n, nil); len(e) != 0 {
		t.Fatalf("activity/updated erzeugen keine Einträge: %v", e)
	}
	n = kopie827(strings.Replace(sk("a", "KA", ""), `"title":"T"`, `"title":"Neu"`, 1), "", "", "")
	e := einzig827(t, func() []JournalEintrag { e, _ := journal827(t, v, n, nil); return e }())
	if e.Art != JArtFeld || e.Stelle != "title" || e.Kennung != "KA" || e.Wartend || string(e.Vorher) != `"T"` || string(e.Nachher) != `"Neu"` {
		t.Fatalf("%+v", e)
	}
	// Fehlendes Feld gilt als null.
	n = kopie827(sk("a", "KA", `"dueDate":"2026-10-20"`), "", "", "")
	e = einzig827(t, func() []JournalEintrag { e, _ := journal827(t, v, n, nil); return e }())
	if e.Stelle != "dueDate" || string(e.Vorher) != "null" {
		t.Fatalf("%+v", e)
	}
}

func TestJournal827_Checkliste(t *testing.T) {
	mit := func(d string) []byte {
		return kopie827(strings.Replace(sk("a", "KA", ""), `"description":""`, `"description":`+string(roh(d)), 1), "", "", "")
	}
	e, _ := journal827(t, mit("x\n- [ ] a\n- [ ] b"), mit("x\n- [x] a\n- [ ] b"), nil)
	p := einzig827(t, e)
	if p.Art != JArtCheckpunkt || p.Stelle != "checkpunkt:1:a" || !strings.Contains(string(p.Nachher), `"erledigt":true`) ||
		!strings.Contains(string(p.Vorher), `"erledigt":false`) {
		t.Fatalf("%+v", p)
	}
	e, _ = journal827(t, mit("x\n- [ ] a"), mit("y\n- [x] a"), nil)
	if f := einzig827(t, e); f.Art != JArtFeld || f.Stelle != "description" {
		t.Fatalf("Häkchen mit Textänderung = ganze Beschreibung: %+v", f)
	}
}

func TestJournal827_Listen(t *testing.T) {
	v := kopie827(sk("a", "KA", `"labels":["x","y"]`), "", "", "")
	n := kopie827(sk("a", "KA", `"labels":["y","z"]`), "", "", "")
	e, _ := journal827(t, v, n, nil)
	if len(e) != 2 || e[0].Art != JArtListeAdd || e[0].Stelle != "labels:z" || e[1].Art != JArtListeRemove || e[1].Stelle != "labels:x" {
		t.Fatalf("%+v", e)
	}
	n = kopie827(sk("a", "KA", `"labels":["y","x"]`), "", "", "")
	if e, _ := journal827(t, v, n, nil); len(e) != 0 {
		t.Fatalf("nur umsortiert: kein Eintrag, bekam %v", e)
	}
}

func TestJournal827_VerschobenUndReihenfolge(t *testing.T) {
	a, b, c := sk("a", "KA", ""), sk("b", "KB", ""), sk("c", "KC", "")
	v := kopie827(a+","+b+","+c, "", "", "")
	// b in den Backlog, Spalte Ready.
	n := kopie827(a+","+c, strings.Replace(b, "col-backlog", "col-ready", 1), "", "")
	e := einzig827(t, func() []JournalEintrag { e, _ := journal827(t, v, n, nil); return e }())
	if e.Art != JArtVerschoben || string(e.Vorher) != `{"lane":"kl","nach":{"kennung":"KA","wartend":false},"spalte":"col-backlog"}` ||
		string(e.Nachher) != `{"lane":"__backlog__","nach":null,"spalte":"col-ready"}` {
		t.Fatalf("%s %s", e.Vorher, e.Nachher)
	}
	// c an den Anfang: genau EIN Eintrag (für c), nicht für a und b.
	n = kopie827(c+","+a+","+b, "", "", "")
	e = einzig827(t, func() []JournalEintrag { e, _ := journal827(t, v, n, nil); return e }())
	if e.Art != JArtReihenfolge || e.Kennung != "KC" || string(e.Nachher) != `{"nach":null}` {
		t.Fatalf("%+v", e)
	}
	// Eine Karte in einer anderen Spalte stört die Reihenfolge nicht.
	x := strings.Replace(sk("x", "KX", ""), "col-backlog", "col-ready", 1)
	v = kopie827(a+","+x+","+b, "", "", "")
	n = kopie827(x+","+a+","+b, "", "", "")
	if e, _ := journal827(t, v, n, nil); len(e) != 0 {
		t.Fatalf("Reihenfolge je Spalte unverändert: %v", e)
	}
}

func TestJournal827_Kommentare(t *testing.T) {
	v := kopie827(sk("a", "KA", `"comments":[{"id":"k1","author":"Anna","text":"alt","created":"c","herkunft":"aussen"}]`), "", "", "")
	n := kopie827(sk("a", "KA", `"comments":[{"id":"k1","author":"Anna","text":"neu","created":"c","herkunft":"aussen"},{"id":"k2","author":"Bert","authorEmail":"b@x.de","text":"hallo","created":"d"}]`), "", "", "")
	e, _ := journal827(t, v, n, nil)
	if len(e) != 2 || e[0].Art != JArtKommentarEdit || e[0].Stelle != "kommentar:k1" || string(e[0].Nachher) != `"neu"` ||
		e[1].Art != JArtKommentarAdd || strings.Contains(string(e[1].Nachher), "b@x.de") {
		t.Fatalf("%+v", e)
	}
}

func TestJournal827_Vertraulich(t *testing.T) {
	v := kopie827(sk("a", "KA", ""), "", "", "")
	n := kopie827(sk("a", "KA", `"confidential":true`), "", "", "")
	if e := einzig827(t, func() []JournalEintrag { e, _ := journal827(t, v, n, nil); return e }()); e.Art != JArtVertraulich {
		t.Fatalf("%+v", e)
	}
}

func TestJournal827_AngelegtUndWartend(t *testing.T) {
	a := sk("a", "KA", "")
	neuK := `{"id":"aussen-1","title":"Neu","description":"","columnId":"col-backlog","labels":["l"],"comments":[]}`
	v := kopie827(a, "", "", "")
	n := kopie827(a+","+neuK, "", "", "")
	e, nw := journal827(t, v, n, nil)
	f := einzig827(t, e)
	if f.Art != JArtAngelegt || f.Kennung != "aussen-1" || !f.Wartend || nw["aussen-1"] != ArtKarte {
		t.Fatalf("%+v %v", f, nw)
	}
	var o map[string]json.RawMessage
	_ = json.Unmarshal(f.Nachher, &o)
	if string(o["lane"]) != `"kl"` || string(o["nach"]) != `{"kennung":"KA","wartend":false}` || string(o["labels"]) != `["l"]` ||
		string(o["vertraulich"]) != "false" || string(o["title"]) != `"Neu"` {
		t.Fatalf("%s", f.Nachher)
	}
	// Danach wartend: eine Änderung trägt wartend:true, Kennung = Außen-ID.
	n2 := kopie827(a+","+strings.Replace(neuK, `"title":"Neu"`, `"title":"Neu2"`, 1), "", "", "")
	e, nw = journal827(t, n, n2, map[string]bool{"aussen-1": true})
	f = einzig827(t, e)
	if f.Art != JArtFeld || !f.Wartend || f.Kennung != "aussen-1" || len(nw) != 0 {
		t.Fatalf("%+v %v", f, nw)
	}
	// In einer unmarkierten Lane: rein außen, kein Eintrag.
	if e, nw := journal827(t, v, kopie827(a, "", neuK, ""), nil); len(e) != 0 || len(nw) != 0 {
		t.Fatalf("unmarkierte Lane: %v %v", e, nw)
	}
	// Aus einer unmarkierten Lane hereingeschoben: angelegt.
	if e, _ := journal827(t, kopie827(a, "", neuK, ""), n, nil); len(e) != 1 || e[0].Art != JArtAngelegt {
		t.Fatalf("hereingeschoben: %v", e)
	}
}

func TestJournal827_AltkarteOhneMarkeBleibtAussen(t *testing.T) {
	alt := `{"id":"alt-1","title":"Alt","description":"","columnId":"col-backlog","comments":[]}`
	a, b := sk("a", "KA", ""), sk("b", "KB", "")
	v := kopie827(alt+","+a, "", "", "")
	n := kopie827(strings.Replace(alt, `"Alt"`, `"Alt2"`, 1)+","+a, "", "", "")
	if e, nw := journal827(t, v, n, nil); len(e) != 0 || len(nw) != 0 {
		t.Fatalf("Altkarte geändert: %v %v", e, nw)
	}
	// Nie Anker: b hinter die Altkarte geschoben ergibt den Anker a bzw. null.
	v = kopie827(a+","+b+","+alt, "", "", "")
	n = kopie827(a+","+alt+","+b, "", "", "")
	if e, _ := journal827(t, v, n, nil); len(e) != 0 {
		t.Fatalf("Reihenfolge der Spiegel unverändert: %v", e)
	}
	n = kopie827(alt+","+b+","+a, "", "", "")
	for _, e := range func() []JournalEintrag { e, _ := journal827(t, v, n, nil); return e }() {
		if strings.Contains(string(e.Nachher), "alt-1") {
			t.Fatalf("Altkarte als Anker: %+v", e)
		}
	}
}

func TestJournal827_Termine(t *testing.T) {
	a := sk("a", "KA", "")
	tSpiegel := `{"id":"t-a","titel":"Treffen","datum":"2026-10-20","spiegel":{"id":"KT"}}`
	tEigen := `{"id":"t-e","titel":"Eigen","datum":"2026-10-21"}`
	v := kopie827(a, "", "", "["+tSpiegel+","+tEigen+"]")
	n := kopie827(a, "", "", "["+strings.Replace(tSpiegel, `"datum"`, `"karteId":"a","datum"`, 1)+","+strings.Replace(tEigen, "Eigen", "Eigen2", 1)+"]")
	f := einzig827(t, func() []JournalEintrag { e, _ := journal827(t, v, n, nil); return e }())
	if f.Objekt != ArtTermin || f.Art != JArtTerminFeld || f.Stelle != "karteId" || string(f.Nachher) != `{"kennung":"KA","wartend":false}` {
		t.Fatalf("%+v", f)
	}
	neuT := `{"id":"t-n","titel":"Neu","datum":"2026-10-22"}`
	e, nw := journal827(t, v, kopie827(a, "", "", "["+tSpiegel+","+tEigen+","+neuT+"]"), nil)
	if f := einzig827(t, e); f.Art != JArtAngelegt || f.Kennung != "t-n" || nw["t-n"] != ArtTermin {
		t.Fatalf("%+v %v", f, nw)
	}
}

func TestJournal827_EntferntUndUnlesbar(t *testing.T) {
	v := kopie827(sk("a", "KA", ""), "", "", "")
	if f := einzig827(t, func() []JournalEintrag { e, _ := journal827(t, v, kopie827("", "", "", ""), nil); return e }()); f.Art != JArtEntfernt {
		t.Fatalf("%+v", f)
	}
	if _, _, ok := JournalEintraege(v, []byte("{"), nil); ok {
		t.Fatal("unlesbar muss ok=false sein")
	}
}

// Sitzungs-Kritik A-2: Positions-Einträge in der Reihenfolge des neuen Stands —
// jeder Anker steht schon an seinem Platz, wenn der Eintrag angewendet wird.
func TestJournal827_AnkerReihenfolgeAbhaengigkeitsgerecht(t *testing.T) {
	a := sk("a", "KA", "")
	x := strings.Replace(sk("x", "KZ", ""), "col-backlog", "col-ready", 1)
	y := strings.Replace(sk("y", "KB", ""), "col-backlog", "col-ready", 1)
	// X und Y liegen in Ready; beide wandern nach Backlog, X hinter A, Y hinter X.
	// Y sortiert nach Kennung vor X (KB < KZ) — trotzdem muss X zuerst kommen.
	v := kopie827(a+","+x+","+y, "", "", "")
	n := kopie827(a+","+strings.Replace(x, "col-ready", "col-backlog", 1)+","+strings.Replace(y, "col-ready", "col-backlog", 1), "", "", "")
	e, _ := journal827(t, v, n, nil)
	if len(e) != 2 || e[0].Kennung != "KZ" || e[1].Kennung != "KB" || !strings.Contains(string(e[1].Nachher), `"kennung":"KZ"`) {
		b, _ := json.Marshal(e)
		t.Fatalf("X vor Y erwartet: %s", b)
	}
	// Bestehende Karte aus einer anderen Spalte hinter eine neue verschoben:
	// angelegt kommt zuerst. (Bleibt eine Karte relativ zu den übrigen
	// Spiegeln an ihrem Platz, braucht sie keinen Eintrag — „n1 nach a“ reicht.)
	neuK := `{"id":"n1","title":"N","description":"","columnId":"col-backlog","comments":[]}`
	b := strings.Replace(sk("b", "KB", ""), "col-backlog", "col-ready", 1)
	v = kopie827(a+","+b, "", "", "")
	n = kopie827(a+","+neuK+","+strings.Replace(b, "col-ready", "col-backlog", 1), "", "", "")
	e, _ = journal827(t, v, n, nil)
	if len(e) != 2 || e[0].Art != JArtAngelegt || e[1].Art != JArtVerschoben || !strings.Contains(string(e[1].Nachher), `"kennung":"n1"`) {
		bb, _ := json.Marshal(e)
		t.Fatalf("angelegt vor der Reihenfolge erwartet: %s", bb)
	}
}

// Sitzungs-Kritik B-5: das Feld wartend auch außerhalb von angelegt/feld.
func TestJournal827_WartendInAllenArten(t *testing.T) {
	a := sk("a", "KA", "")
	w := `{"id":"w1","title":"W","description":"","columnId":"col-backlog","labels":[],"comments":[]}`
	v := kopie827(a+","+w, "", "", "")
	w2 := `{"id":"w1","title":"W","description":"","columnId":"col-ready","labels":["l"],"confidential":true,"comments":[{"id":"k1","author":"A","text":"t","created":"c"}]}`
	n := kopie827(w2+","+a, "", "", "")
	e, _ := journal827(t, v, n, map[string]bool{"w1": true})
	arten := map[string]bool{}
	for _, x := range e {
		if x.Kennung == "w1" {
			if !x.Wartend {
				t.Fatalf("wartend fehlt: %+v", x)
			}
			arten[x.Art] = true
		}
	}
	for _, art := range []string{JArtVerschoben, JArtListeAdd, JArtKommentarAdd, JArtVertraulich} {
		if !arten[art] {
			t.Errorf("Art %s für die wartende Karte fehlt: %v", art, arten)
		}
	}
}
