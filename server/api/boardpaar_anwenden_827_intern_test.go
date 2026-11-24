package api

// Karte #827 (E3b): Bausteine des Rückwegs innen, die an der Kette schwer zu
// erreichen sind — unbekannte Protokollart, Bindung an den gelesenen Stand,
// Kommentarrecht des Rückwegs, Typ der Op je Eintrag.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/fold"
	"github.com/Andiministrator/aBoard/server/schatten"
	"github.com/Andiministrator/aBoard/server/storage"
)

func puffer827(status int, body string) *pufferAntwort {
	p := &pufferAntwort{kopf: http.Header{}, status: status}
	p.body.WriteString(body)
	return p
}

func TestRueckweg827_DeuteProtokollarten(t *testing.T) {
	a := &BoardsAPI{}
	e := &anwendenEintrag{OpID: "x", Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtFeld, Stelle: "title"}
	faelle := []struct {
		name     string
		p        *pufferAntwort
		status   int
		ergebnis string
		grund    string
	}{
		{"angewendet", puffer827(200, `{"ops":[{"id":"o"}]}`), 200, rwAngewendet, ""},
		{"wiederholt", puffer827(200, `{"ops":[],"wiederholt":true}`), 200, rwSchonAngewendet, ""},
		{"ziel-fehlt", puffer827(422, `{"protokoll":[{"art":"ziel-fehlt"}]}`), 200, rwVerworfen, rwGrundKarteWeg},
		{"klemmung", puffer827(422, `{"protokoll":[{"art":"klemmung"}]}`), 200, rwVerworfen, rwGrundSpalteUnbekannt},
		{"entfernt", puffer827(422, `{"protokoll":[{"art":"entfernt"}]}`), 200, rwVerworfen, rwGrundLoeschenNichtErlaubt},
		{"wirkungslos", puffer827(422, `{"protokoll":[{"art":"wirkungslos","grund":"x"}]}`), 422, "", ""},
		// VF-11: jede andere Art ist 500 mit der Art — auch neben bekannten.
		{"beansprucht", puffer827(422, `{"protokoll":[{"art":"beansprucht"}]}`), 500, "", ""},
		{"gemischt", puffer827(422, `{"protokoll":[{"art":"ziel-fehlt"},{"art":"entkernung"}]}`), 500, "", ""},
		{"422 ohne Protokoll", puffer827(422, `{"error":"x"}`), 500, "", ""},
		{"op-id wiederverwendet", puffer827(400, `{"error":"op id x `+opIDWiederverwendetText+`"}`), 409, "", ""},
		{"ungültige op", puffer827(400, `{"error":"invalid ops"}`), 422, "", ""},
		{"belegt", puffer827(503, `{"error":"busy"}`), 503, "", ""},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			erg := a.rwDeute(nil, nil, "b", e, nil, f.p)
			if erg.status != f.status {
				t.Fatalf("status %d, erwartet %d (%v)", erg.status, f.status, erg.body)
			}
			if f.status == 200 && (erg.body["ergebnis"] != f.ergebnis || (f.grund != "" && erg.body["grund"] != f.grund)) {
				t.Fatalf("%v, erwartet %s/%s", erg.body, f.ergebnis, f.grund)
			}
			if f.name == "beansprucht" && erg.body["protokollart"] != "beansprucht" {
				t.Fatalf("protokollart fehlt: %v", erg.body)
			}
		})
	}
	// Retry-After wird durchgereicht.
	p := puffer827(429, `{"error":"rate"}`)
	p.kopf.Set("Retry-After", "7")
	if erg := a.rwDeute(nil, nil, "b", e, nil, p); erg.status != 429 || erg.kopf.Get("Retry-After") != "7" {
		t.Fatalf("429: %d %v", erg.status, erg.kopf)
	}
}

const brett827intern = `{"board":{"identities":[{"label":"Andi"}],"columnDefs":[{"id":"col-ready"}],
 "sprints":[{"id":"s1","title":"S1","cards":[{"id":"k1","columnId":"col-ready","comments":[
   {"id":"ci","author":"Andi","authorEmail":"a@x","text":"innen"},{"id":"ca","author":"extern: Max","text":"außen"}]}]},
  {"id":"s2","title":"S2","cards":[]}],
 "termine":[{"id":"t1","titel":"T","datum":"2026-10-20"}]}}`

func rwBoard827(t *testing.T, st string) *rwBoard {
	t.Helper()
	b, err := leseRwBoard([]byte(st))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRueckweg827_BindungAnGelesenenStand(t *testing.T) {
	b := rwBoard827(t, brett827intern)
	// reihenfolge: an Lane und Spalte gebunden.
	e := &anwendenEintrag{OpID: "x", Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtReihenfolge, Stelle: "reihenfolge",
		Nachher: json.RawMessage(`{"nach":null}`)}
	plan, sofort := e.baue(b)
	if sofort != nil {
		t.Fatalf("baue: %+v", sofort)
	}
	if g := plan.bindung(b); g != "" {
		t.Fatalf("unveränderter Stand: %q", g)
	}
	verschoben := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","cards":[]},{"id":"s2","cards":[{"id":"k1","columnId":"col-ready"}]}]}}`)
	if g := plan.bindung(verschoben); g != rwGrundStandGeaendert {
		t.Fatalf("Karte innen verschoben: %q, erwartet stand-geaendert", g)
	}
	// vertraulich am Termin: an den ganzen Termin gebunden.
	e = &anwendenEintrag{OpID: "y", Objekt: schatten.ArtTermin, ID: "t1", Art: schatten.JArtVertraulich, Stelle: "confidential",
		Vorher: json.RawMessage(`false`), Nachher: json.RawMessage(`true`)}
	plan, sofort = e.baue(b)
	if sofort != nil {
		t.Fatalf("baue: %+v", sofort)
	}
	if g := plan.bindung(b); g != "" {
		t.Fatalf("Termin unverändert: %q", g)
	}
	geaendert := rwBoard827(t, `{"board":{"sprints":[],"termine":[{"id":"t1","titel":"Anders","datum":"2026-10-20"}]}}`)
	if g := plan.bindung(geaendert); g != rwGrundStandGeaendert {
		t.Fatalf("Termin innen geändert: %q", g)
	}
	// kommentar.edit nur an Kommentaren von außen.
	for id, soll := range map[string]string{"ca": "", "ci": rwGrundKommentarNichtErlaubt} {
		e = &anwendenEintrag{OpID: "z", Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtKommentarEdit, Stelle: "kommentar:" + id,
			Vorher: json.RawMessage(`"a"`), Nachher: json.RawMessage(`"b"`)}
		plan, sofort = e.baue(b)
		if sofort != nil {
			t.Fatalf("baue: %+v", sofort)
		}
		if g := plan.bindung(b); g != soll {
			t.Fatalf("kommentar %s: %q, erwartet %q", id, g, soll)
		}
	}
}

func TestRueckweg827_KommentarSchreiber(t *testing.T) {
	s := kommentarSchreiber{boardpaar: true}
	if !s.prueft() {
		t.Fatal("Rückweg ohne Prüfung (Legacy-Bypass)")
	}
	faelle := []struct {
		k          kommentarEintrag
		eigen, neu bool
	}{
		{kommentarEintrag{autor: "extern: Max"}, true, true},
		{kommentarEintrag{autor: "extern: Max", autorEmail: "max@x"}, false, false},
		{kommentarEintrag{autor: "Andi", autorEmail: "a@x"}, false, false},
		{kommentarEintrag{autor: "extern: "}, false, false},
		{kommentarEintrag{autor: BoardpaarKonfliktAutor}, false, true},
		{kommentarEintrag{autor: BoardpaarKonfliktAutor, autorEmail: "a@x"}, false, false},
	}
	for _, f := range faelle {
		if s.eigen(f.k) != f.eigen || s.neuOK(f.k) != f.neu {
			t.Errorf("%+v: eigen=%v neu=%v, erwartet %v/%v", f.k, s.eigen(f.k), s.neuOK(f.k), f.eigen, f.neu)
		}
	}
}

// Der Typ, den die Idempotenz-Vorprüfung annimmt, ist der, den baue bildet.
func TestRueckweg827_OpTypJeEintrag(t *testing.T) {
	b := rwBoard827(t, brett827intern)
	ort := `{"lane":"s1","spalte":"col-ready","nach":null}`
	faelle := []*anwendenEintrag{
		{Objekt: "karte", ID: "k1", Art: "feld", Stelle: "title", Vorher: json.RawMessage(`"a"`), Nachher: json.RawMessage(`"b"`)},
		{Objekt: "karte", ID: "k1", Art: "vertraulich", Stelle: "confidential", Nachher: json.RawMessage(`true`)},
		{Objekt: "karte", ID: "k1", Art: "verschoben", Stelle: "ort", Vorher: json.RawMessage(ort), Nachher: json.RawMessage(ort)},
		{Objekt: "karte", ID: "k1", Art: "reihenfolge", Stelle: "reihenfolge", Nachher: json.RawMessage(`{"nach":null}`)},
		{Objekt: "karte", ID: "k1", Art: "liste.add", Stelle: "labels:x", Nachher: json.RawMessage(`"x"`)},
		{Objekt: "karte", ID: "k1", Art: "liste.remove", Stelle: "labels:x", Vorher: json.RawMessage(`"x"`)},
		{Objekt: "karte", ID: "k1", Art: "checkpunkt.feld", Stelle: "checkpunkt:1:a",
			Vorher: json.RawMessage(`{"text":"a","vorkommen":1,"erledigt":false}`), Nachher: json.RawMessage(`{"text":"a","vorkommen":1,"erledigt":true}`)},
		{Objekt: "karte", ID: "k1", Art: "kommentar.add", Stelle: "kommentar:n", Nachher: json.RawMessage(`{"id":"n","author":"M","text":"t"}`)},
		{Objekt: "karte", ID: "k1", Art: "kommentar.edit", Stelle: "kommentar:ca", Vorher: json.RawMessage(`"a"`), Nachher: json.RawMessage(`"b"`)},
		{Objekt: "karte", ID: "neu", Art: "angelegt", Ursprung: "u", Nachher: json.RawMessage(`{"lane":"s1","spalte":"col-ready","nach":null,"title":"N","labels":[],"assignees":[]}`)},
		{Objekt: "termin", ID: "t1", Art: "termin.feld", Stelle: "ort", Vorher: json.RawMessage(`null`), Nachher: json.RawMessage(`"x"`)},
		{Objekt: "termin", ID: "t1", Art: "vertraulich", Stelle: "confidential", Nachher: json.RawMessage(`true`)},
		{Objekt: "termin", ID: "tn", Art: "angelegt", Ursprung: "u", Nachher: json.RawMessage(`{"titel":"N","datum":"2026-10-20"}`)},
	}
	for _, e := range faelle {
		plan, sofort := e.baue(b)
		if sofort != nil {
			t.Fatalf("%s/%s: %+v", e.Objekt, e.Art, sofort)
		}
		if plan.op["type"] != rwOpTyp(e) || rwOpTyp(e) == "" {
			t.Errorf("%s/%s: baue %v, rwOpTyp %q", e.Objekt, e.Art, plan.op["type"], rwOpTyp(e))
		}
	}
}

// Kritik E3b K-3: Die Vertraulich-Prüfung UNTER der Sperre (Rennen zwischen
// Vorprüfung und Sperre) — Karte, Termin, Konflikt-Kommentar.
func TestRueckweg827_BindungVertraulichUnterSperre(t *testing.T) {
	b := rwBoard827(t, brett827intern)
	e := &anwendenEintrag{OpID: "x", Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtFeld, Stelle: "title",
		Vorher: json.RawMessage(`"a"`), Nachher: json.RawMessage(`"b"`)}
	plan, sofort := e.baue(b)
	if sofort != nil {
		t.Fatalf("baue: %+v", sofort)
	}
	vertraulich := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","cards":[{"id":"k1","columnId":"col-ready","confidential":true}]}],
	 "termine":[{"id":"t1","titel":"T","datum":"2026-10-20","karteId":"k1"}]}}`)
	if g := plan.bindung(vertraulich); g != rwGrundVertraulichGeworden {
		t.Fatalf("Karte: %q", g)
	}
	e = &anwendenEintrag{OpID: "y", Objekt: schatten.ArtTermin, ID: "t1", Art: schatten.JArtTerminFeld, Stelle: "ort",
		Vorher: json.RawMessage(`null`), Nachher: json.RawMessage(`"x"`)}
	if plan, sofort = e.baue(b); sofort != nil {
		t.Fatalf("baue: %+v", sofort)
	}
	if g := plan.bindung(b); g != "" {
		t.Fatalf("Termin offen: %q", g)
	}
	if g := plan.bindung(vertraulich); g != rwGrundVertraulichGeworden {
		t.Fatalf("Termin über seine Karte vertraulich: %q", g)
	}
	if g := bindungKonflikt("k1", &anwendenEintrag{Objekt: schatten.ArtKarte})(b); g != "" {
		t.Fatalf("Konflikt-Ziel offen: %q", g)
	}
	if g := bindungKonflikt("k1", &anwendenEintrag{Objekt: schatten.ArtKarte})(vertraulich); g != rwGrundVertraulichGeworden {
		t.Fatalf("Konflikt-Ziel vertraulich: %q", g)
	}
}

// Kritik E3b K-8: höchstens drei Versuche, wenn sich der gelesene Stand unter
// der Sperre als geändert erweist; dann 503.
func TestRueckweg827_VersucheBeiGeaendertemStand(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	repo := storage.NewBoardRepo(db)
	a := NewBoardsAPI(repo).WithFold(m)
	stand := func(spalte string) string {
		return `{"version":"2","board":{"id":"b","title":"B","columnDefs":[{"id":"col-a"},{"id":"col-b"}],` +
			`"sprints":[{"id":"s1","cards":[{"id":"k0","columnId":"` + spalte + `"},{"id":"k1","columnId":"` + spalte + `"}]}]}}`
	}
	if _, err := repo.Insert(ctx, "b", "B", "2", []byte(stand("col-a"))); err != nil {
		t.Fatal(err)
	}
	spalten := []string{"col-b", "col-a"}
	wechsel := func(bis int) func(int) {
		return func(versuch int) {
			if versuch < bis {
				if _, err := db.Exec(`UPDATE boards SET state_json = ? WHERE id = 'b'`, stand(spalten[versuch%2])); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	t.Cleanup(func() { rwNachVersuchNaht = nil })
	eintrag := func(op string) *anwendenEintrag {
		return &anwendenEintrag{OpID: op, Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtReihenfolge, Stelle: "reihenfolge",
			Nachher: json.RawMessage(`{"nach":null}`), Person: "p"}
	}
	r := httptest.NewRequest("POST", "/boardpaar/anwenden/b", nil)
	rwNachVersuchNaht = wechsel(rwVersuche)
	if erg := a.boardpaarAnwenden(ctx, r, "b", eintrag("v-1")); erg.status != http.StatusServiceUnavailable || erg.kopf.Get("Retry-After") == "" {
		t.Fatalf("immer geändert: %d %v", erg.status, erg.body)
	}
	rwNachVersuchNaht = wechsel(1)
	if erg := a.boardpaarAnwenden(ctx, r, "b", eintrag("v-2")); erg.status != 200 || erg.body["ergebnis"] != rwAngewendet {
		t.Fatalf("einmal geändert, dann stabil: %d %v", erg.status, erg.body)
	}
}

// Kritik E3b K-6: Der Rückweg teilt sich EINEN Eimer — wechselnde Personen
// umgehen die Bremse nicht.
func TestRueckweg827_EinEimerFuerDenRueckweg(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	repo := storage.NewBoardRepo(db)
	a := NewBoardsAPI(repo).WithFold(m)
	a.opsLimiter = newRateLimiter(2, time.Minute)
	st := `{"version":"2","board":{"id":"b","title":"B","sprints":[{"id":"s1","cards":[{"id":"k1","title":"T","columnId":"c"}]}]}}`
	if _, err := repo.Insert(ctx, "b", "B", "2", []byte(st)); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/boardpaar/anwenden/b", nil)
	var codes []int
	for i, person := range []string{"a@x", "b@x", "c@x"} {
		e := &anwendenEintrag{OpID: "e-" + person, Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtListeAdd,
			Stelle: "labels:L" + string(rune('0'+i)), Nachher: json.RawMessage(`"L` + string(rune('0'+i)) + `"`), Person: person}
		codes = append(codes, a.boardpaarAnwenden(ctx, r, "b", e).status)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("Statuscodes %v, erwartet 200, 200, 429", codes)
	}
	// Verify E3b V-4: je Board ein Eimer — ein anderes Board läuft weiter.
	if _, err := repo.Insert(ctx, "b2", "B", "2", []byte(strings.Replace(st, `"id":"b"`, `"id":"b2"`, 1))); err != nil {
		t.Fatal(err)
	}
	e := &anwendenEintrag{OpID: "e-b2", Objekt: schatten.ArtKarte, ID: "k1", Art: schatten.JArtListeAdd,
		Stelle: "labels:X", Nachher: json.RawMessage(`"X"`), Person: "a@x"}
	if code := a.boardpaarAnwenden(ctx, r, "b2", e).status; code != 200 {
		t.Fatalf("anderes Board: %d, erwartet 200", code)
	}
}

// Kritik E3b K-1/K-4/K-5: die Vorprüfung in baue — an der Kette fängt die
// Prüfung unter der Sperre (bzw. der Fold) denselben Fall noch einmal ab,
// deshalb hier direkt.
func TestRueckweg827_VorpruefungInBaue(t *testing.T) {
	b := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","cards":[
	  {"id":"k1","columnId":"c","confidential":true},
	  {"id":"k2","columnId":"c","comments":[{"id":"cx","author":"extern: M","text":"Eins"}]}]}],
	 "termine":[{"id":"t1","titel":"T","datum":"2026-10-20","karteId":"k1"},{"id":"t2","titel":"U","datum":"2026-10-21"}]}}`)
	faelle := []struct {
		name  string
		e     *anwendenEintrag
		grund string
	}{
		{"Termin über Karte vertraulich", &anwendenEintrag{Objekt: "termin", ID: "t1", Art: "termin.feld", Stelle: "ort",
			Vorher: json.RawMessage(`null`), Nachher: json.RawMessage(`"x"`)}, rwGrundVertraulichGeworden},
		{"karteId auf vertrauliche Karte", &anwendenEintrag{Objekt: "termin", ID: "t2", Art: "termin.feld", Stelle: "karteId",
			Vorher: json.RawMessage(`null`), Nachher: json.RawMessage(`"k1"`)}, rwGrundVertraulichGeworden},
		{"karteId auf fehlende Karte", &anwendenEintrag{Objekt: "termin", ID: "t2", Art: "termin.feld", Stelle: "karteId",
			Vorher: json.RawMessage(`null`), Nachher: json.RawMessage(`"k-weg"`)}, rwGrundKarteWeg},
		{"Kommentar-ID belegt", &anwendenEintrag{Objekt: "karte", ID: "k2", Art: "kommentar.add", Stelle: "kommentar:cx",
			Nachher: json.RawMessage(`{"id":"cx","author":"M","text":"Zwei"}`)}, rwGrundKommentarIDBelegt},
		{"Kommentar fehlt", &anwendenEintrag{Objekt: "karte", ID: "k2", Art: "kommentar.edit", Stelle: "kommentar:weg",
			Vorher: json.RawMessage(`"a"`), Nachher: json.RawMessage(`"b"`)}, rwGrundKommentarWeg},
		{"Spalte unbekannt (Sitzungs-QA A-3)", &anwendenEintrag{Objekt: "karte", ID: "k2", Art: "verschoben", Stelle: "ort",
			Vorher:  json.RawMessage(`{"lane":"s1","spalte":"c","nach":null}`),
			Nachher: json.RawMessage(`{"lane":"s1","spalte":"gibt-es-nicht","nach":null}`)}, rwGrundSpalteUnbekannt},
	}
	for _, f := range faelle {
		_, sofort := f.e.baue(b)
		if sofort == nil || sofort.ergebnis != rwVerworfen || sofort.grund != f.grund {
			t.Errorf("%s: %+v, erwartet verworfen/%s", f.name, sofort, f.grund)
		}
	}
	// Unter der Sperre: Kommentar-ID inzwischen mit anderem Text belegt.
	e := &anwendenEintrag{Objekt: "karte", ID: "k2", Art: "kommentar.add", Stelle: "kommentar:neu",
		Nachher: json.RawMessage(`{"id":"neu","author":"M","text":"A"}`)}
	plan, sofort := e.baue(b)
	if sofort != nil {
		t.Fatalf("baue: %+v", sofort)
	}
	belegt := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","cards":[{"id":"k2","comments":[{"id":"neu","text":"B"}]}]}]}}`)
	if g := plan.bindung(belegt); g != rwGrundKommentarIDBelegt {
		t.Fatalf("unter der Sperre belegt: %q", g)
	}
}

// Verify E3b V-2/V-3: Bindung des angelegten Termins (Bezug wird unter der
// Sperre vertraulich) und Konflikt-Ziel eines selbst vertraulichen Termins.
func TestRueckweg827_BindungTerminAnlageUndKonflikt(t *testing.T) {
	b := rwBoard827(t, brett827intern)
	e := &anwendenEintrag{Objekt: "termin", ID: "tn", Art: "angelegt", Ursprung: "u",
		Nachher: json.RawMessage(`{"titel":"N","datum":"2026-10-20","karteId":"k1"}`)}
	plan, sofort := e.baue(b)
	if sofort != nil {
		t.Fatalf("baue: %+v", sofort)
	}
	if g := plan.bindung(b); g != "" {
		t.Fatalf("Bezug offen: %q", g)
	}
	vertraulich := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","cards":[{"id":"k1","confidential":true}]}]}}`)
	if g := plan.bindung(vertraulich); g != rwGrundStandGeaendert {
		t.Fatalf("Bezug unter der Sperre vertraulich: %q", g)
	}
	te := &anwendenEintrag{Objekt: schatten.ArtTermin, ID: "t1"}
	terminVertraulich := rwBoard827(t, `{"board":{"sprints":[{"id":"s1","cards":[{"id":"k1"}]}],
	 "termine":[{"id":"t1","titel":"T","datum":"2026-10-20","karteId":"k1","confidential":true}]}}`)
	if g := bindungKonflikt("k1", te)(terminVertraulich); g != rwGrundVertraulichGeworden {
		t.Fatalf("Termin selbst vertraulich: %q", g)
	}
}

// Sitzungs-QA E3b A-1: Wird ein Termin zwischen der gescheiterten Op und dem
// Konflikt-Kommentar vertraulich, ist das Ergebnis verworfen/vertraulich-
// geworden — dasselbe, das eine Wiederholung liefert — und kein Kommentar.
func TestRueckweg827_KonfliktAnInzwischenVertraulichemTermin(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := fold.Neu(fold.Optionen{})
	if err != nil {
		t.Fatal(err)
	}
	repo := storage.NewBoardRepo(db)
	a := NewBoardsAPI(repo).WithFold(m)
	st := `{"version":"2","board":{"id":"b","title":"B","sprints":[{"id":"s1","cards":[{"id":"k1","title":"K","columnId":"c","comments":[]}]}],` +
		`"termine":[{"id":"t1","titel":"T","datum":"2026-10-20","karteId":"k1","confidential":true}]}}`
	if _, err := repo.Insert(ctx, "b", "B", "2", []byte(st)); err != nil {
		t.Fatal(err)
	}
	e := &anwendenEintrag{OpID: "k-1", Objekt: schatten.ArtTermin, ID: "t1", Art: schatten.JArtTerminFeld, Stelle: "titel",
		Vorher: json.RawMessage(`"Alt"`), Nachher: json.RawMessage(`"Neu"`), Person: "p"}
	r := httptest.NewRequest("POST", "/boardpaar/anwenden/b", nil)
	erg := a.rwKonflikt(mitBoardpaarAkteur(mitBoardpaarRueckweg(ctx), "p"), r, "b", e)
	if erg.status != 200 || erg.body["ergebnis"] != rwVerworfen || erg.body["grund"] != rwGrundVertraulichGeworden {
		t.Fatalf("Konflikt an vertraulichem Termin: %d %v", erg.status, erg.body)
	}
	b, _ := repo.Get(ctx, "b")
	if strings.Contains(string(b.StateJSON), "Konflikt") {
		t.Fatal("Konflikt-Kommentar an der Karte eines vertraulichen Termins")
	}
}
