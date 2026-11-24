package schatten

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func baueDatei(t *testing.T, pfad string) *Ergebnis {
	t.Helper()
	roh, err := os.ReadFile(pfad)
	if err != nil {
		t.Fatal(err)
	}
	erg, err := Baue(roh)
	if err != nil {
		t.Fatalf("Baue: %v", err)
	}
	return erg
}

// Der Feldsatz ist EINE Liste: Eintrag trägt genau diese JSON-Schlüssel.
func TestFelderGleichStruct(t *testing.T) {
	typ := reflect.TypeOf(Eintrag{})
	var tags []string
	for i := 0; i < typ.NumField(); i++ {
		tags = append(tags, strings.Split(typ.Field(i).Tag.Get("json"), ",")[0])
	}
	if !reflect.DeepEqual(tags, Felder) {
		t.Fatalf("JSON-Schlüssel von Eintrag %v ≠ Felder %v", tags, Felder)
	}
}

// Vertrag Push-Skript, T1: Karte mit Beschreibung, Kommentar, Anhang, Label, Verlauf → die
// Ausgabe enthält nur die Feldliste, Abgleich gegen die erwartete Datei.
func TestT1NurFeldliste(t *testing.T) {
	erg := baueDatei(t, "../testdata/schatten/t1-board.json")
	ist, _ := json.Marshal(erg.Eintraege)
	soll, err := os.ReadFile("../testdata/schatten/t1-erwartet.json")
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(ist, &a)
	if err := json.Unmarshal(soll, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("Ausgabe weicht ab:\nist:  %s\nsoll: %s", ist, bytes.TrimSpace(soll))
	}
	for _, geheim := range []string{"GEHEIM", "@", "external:", "Kündigung", "archiviert", "Sprint-Start", "b-intern"} {
		if strings.Contains(string(ist), geheim) {
			t.Errorf("Ausgabe enthält %q: %s", geheim, ist)
		}
	}
	if erg.OhneIdentitaeten || len(erg.NichtBaubar) != 0 {
		t.Errorf("unerwartet: %+v", erg)
	}
}

// Schalter „vertraulich“: Der Titel erscheint nirgends in der Ausgabe;
// Gegenprobe ohne Schalter.
func TestVertraulichTitelNirgends(t *testing.T) {
	stand := func(conf string) []byte {
		feld := ""
		if conf != "" {
			feld = `,"confidential":` + conf
		}
		return []byte(`{"board":{"sprints":[{"id":"s","cards":[{"id":"k","displayId":1,"title":"Fusion mit Beispiel AG"` + feld + `}]}],
			"termine":[{"id":"t","titel":"Notartermin Fusion","datum":"2026-10-01","karteId":"k"}]}}`)
	}
	for _, conf := range []string{"true", "null", `""`, "0", `"false"`, "{}"} {
		erg, err := Baue(stand(conf))
		if err != nil {
			t.Fatal(err)
		}
		aus, _ := json.Marshal(erg.Eintraege)
		if strings.Contains(string(aus), "Fusion") {
			t.Errorf("confidential=%s: Titel steht in der Ausgabe: %s", conf, aus)
		}
		if len(erg.Eintraege) != 2 || !erg.Eintraege[0].Vertraulich || !erg.Eintraege[1].Vertraulich {
			t.Errorf("confidential=%s: %s", conf, aus)
		}
	}
	for _, conf := range []string{"", "false"} {
		erg, _ := Baue(stand(conf))
		aus, _ := json.Marshal(erg.Eintraege)
		if strings.Count(string(aus), "Fusion") != 2 {
			t.Errorf("Gegenprobe confidential=%q: %s", conf, aus)
		}
	}
}

func TestVertraulichPraedikat(t *testing.T) {
	faelle := map[string]bool{`{}`: false, `{"confidential":false}`: false, `{"confidential": false }`: false,
		`{"confidential":true}`: true, `{"confidential":null}`: true, `{"confidential":""}`: true,
		`{"confidential":0}`: true, `{"confidential":"false"}`: true, `{"confidential":[]}`: true}
	for roh, soll := range faelle {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(roh), &m); err != nil {
			t.Fatal(err)
		}
		if ist := Vertraulich(m); ist != soll {
			t.Errorf("%s: %v, erwartet %v", roh, ist, soll)
		}
		var o map[string]any
		_ = json.Unmarshal([]byte(roh), &o)
		if ist := VertraulichObjekt(o); ist != soll {
			t.Errorf("VertraulichObjekt %s: %v, erwartet %v", roh, ist, soll)
		}
	}
}

func TestTitelBehandlung(t *testing.T) {
	faelle := map[string]string{
		"Angebot Müller prüfen":                    "Angebot Müller prüfen",
		"  Leerzeichen  ":                          "Leerzeichen",
		"Mail an erika@example.org":                Ersatztext,
		"siehe https://example.org":                Ersatztext,
		"siehe www.example.org":                    Ersatztext,
		"WWW.EXAMPLE.ORG":                          Ersatztext,
		"ww​w.example.org":                         Ersatztext,
		"Konto 1234567890123":                      Ersatztext, // 13 Ziffern
		"Konto 123456789012":                       "Konto 123456789012",
		"abcdefghijklmnopqrstuvwxyz":               Ersatztext, // 26 ohne Leerzeichen
		"Prüfsumme deadbeefdeadbeef0":              Ersatztext, // 17 Hex
		"Kyrillisch Привет weg":                    "Kyrillisch weg",
		"Привет":                                   Ersatztext,
		"Ｆｕｌｌｗｉｄｔｈ":                                "Fullwidth",
		"Termin <script>":                          "Termin script",
		"Fragezeichen? Ja! (gut) + 50 € – ok & so": "Fragezeichen? Ja! (gut) + 50 – ok & so",
		"Größe ß Àçé":                              "Größe ß Àçé",
		"Mal × geteilt ÷":                          "Mal geteilt",
		"Nur@nach Filter":                          Ersatztext,
	}
	for roh, soll := range faelle {
		if ist := Titel(roh); ist != soll {
			t.Errorf("Titel(%q) = %q, erwartet %q", roh, ist, soll)
		}
	}
	lang := strings.Repeat("Wort ", 30)
	if ist := Titel(lang); len([]rune(ist)) > TitelMaxZeichen || !strings.HasPrefix(ist, "Wort Wort") {
		t.Errorf("Kürzung: %q", ist)
	}
}

func TestBearbeiterFilter(t *testing.T) {
	st := []byte(`{"board":{"identities":[{"label":"Erika Muster","kind":"user","email":"g@x.de"},{"label":"a@b.de","kind":"user"},
		{"label":"external:x","kind":"user"},{"label":"Team Süd","kind":"team"}],
		"sprints":[{"id":"s","cards":[{"id":"k","title":"x","owner":"Claude","assignees":["ERIKA MUSTER","claude sonnet","Sonnet 4.7","a@b.de","external:x","Unbekannt","Team Süd","Erika Muster"]}]}]}}`)
	erg, err := Baue(st)
	if err != nil {
		t.Fatal(err)
	}
	soll := []string{"Erika Muster", "Claude Sonnet", "Team Süd"}
	if ist := erg.Eintraege[0].Bearbeiter; !reflect.DeepEqual(ist, soll) {
		t.Errorf("Bearbeiter %v, erwartet %v", ist, soll)
	}
	// Ohne identities: nur Claude-Modelle, und der Fall wird gemeldet.
	erg, _ = Baue([]byte(`{"board":{"sprints":[{"id":"s","cards":[{"id":"k","title":"x","owner":"Erika","assignees":["Claude Haiku"]}]}]}}`))
	if !erg.OhneIdentitaeten || !reflect.DeepEqual(erg.Eintraege[0].Bearbeiter, []string{"Claude Haiku"}) {
		t.Errorf("ohne identities: %+v %v", erg.OhneIdentitaeten, erg.Eintraege[0].Bearbeiter)
	}
}

// Sitzungs-Kritik 20.11. F-A: „Claude Fable“ ohne identities bleibt Bearbeiter.
func TestBearbeiterFilterFable(t *testing.T) {
	erg, _ := Baue([]byte(`{"board":{"sprints":[{"id":"s","cards":[{"id":"k","title":"x","owner":"Claude Fable"}]}]}}`))
	if !reflect.DeepEqual(erg.Eintraege[0].Bearbeiter, []string{"Claude Fable"}) {
		t.Errorf("Fable verworfen: %v", erg.Eintraege[0].Bearbeiter)
	}
}

// Parität: claudeLabels = Labels aus CLAUDE_DEFAULTS in src/identity.js.
func TestClaudeLabelsWieIdentityJS(t *testing.T) {
	roh, err := os.ReadFile("../../src/identity.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(roh)
	a := strings.Index(s, "export const CLAUDE_DEFAULTS")
	if a < 0 {
		t.Fatal("CLAUDE_DEFAULTS nicht gefunden")
	}
	ende := strings.Index(s[a:], "};")
	if ende < 0 {
		t.Fatal("Ende von CLAUDE_DEFAULTS („};“) nicht gefunden")
	}
	block := s[a : a+ende]
	var js []string
	for _, m := range regexp.MustCompile(`label: '([^']+)'`).FindAllStringSubmatch(block, -1) {
		js = append(js, m[1])
	}
	gs := append([]string(nil), claudeLabels...)
	sort.Strings(js)
	sort.Strings(gs)
	if !reflect.DeepEqual(js, gs) {
		t.Errorf("claudeLabels %v ≠ CLAUDE_DEFAULTS %v", gs, js)
	}
}

func TestSpalteUndPrioritaet(t *testing.T) {
	st := []byte(`{"board":{"columnDefs":[{"id":"c1","title":"Warten auf www.x.de"},{"id":"c2","title":"` +
		strings.Repeat("Lang ", 12) + `"}],"sprints":[{"id":"s","cards":[
		{"id":"a","title":"a","columnId":"c1","priority":"HOCH"},
		{"id":"b","title":"b","columnId":"c2","priority":"niedrig"},
		{"id":"c","title":"c","columnId":"fehlt","priority":"Geheimtext"}]}]}}`)
	erg, err := Baue(st)
	if err != nil {
		t.Fatal(err)
	}
	e := erg.Eintraege
	if *e[0].Spalte != "" || *e[0].Prioritaet != "" {
		t.Errorf("Muster in Spalte bzw. fremde Priorität: %q %q", *e[0].Spalte, *e[0].Prioritaet)
	}
	if len([]rune(*e[1].Spalte)) > SpalteMaxZeichen || *e[1].Prioritaet != "niedrig" {
		t.Errorf("Spalte zu lang oder Priorität verloren: %q %q", *e[1].Spalte, *e[1].Prioritaet)
	}
	if *e[2].Spalte != "" || *e[2].Prioritaet != "" {
		t.Errorf("unbekannte Spalte/Priorität: %q %q", *e[2].Spalte, *e[2].Prioritaet)
	}
}

// Eine kaputte Karte legt nicht alles still (K-6): Sie geht vertraulich nur
// mit Nummer, die anderen normal.
func TestNichtBaubareKarte(t *testing.T) {
	st := []byte(`{"board":{"sprints":[{"id":"s","cards":[{"id":"kaputt","displayId":7,"title":{"x":"Geheim"},"dueDate":"2026-10-01","owner":"Claude Opus"},
		{"id":"gut","displayId":8,"title":"Gut"}, {"displayId":9,"title":"ohne id"}]}],
		"termine":[{"id":"tk","titel":42,"datum":"2026-10-01"},{"titel":"ohne id","datum":"2026-10-01"},{"id":"td","titel":"kein Datum","datum":"morgen"}]}}`)
	erg, err := Baue(st)
	if err != nil {
		t.Fatal(err)
	}
	aus, _ := json.Marshal(erg.Eintraege)
	if len(erg.Eintraege) != 3 || !reflect.DeepEqual(erg.NichtBaubar, []string{"kaputt", "tk"}) || erg.OhneKennung != 2 {
		t.Fatalf("%s %+v", aus, erg)
	}
	k := erg.Eintraege[0]
	if !k.Vertraulich || k.Titel != nil || k.Nr == nil || *k.Nr != 7 || k.EndeDatum != nil || len(k.Bearbeiter) != 0 {
		t.Errorf("kaputte Karte: %s", aus)
	}
	if strings.Contains(string(aus), "Geheim") || strings.Contains(string(aus), "kein Datum") {
		t.Errorf("Ausgabe: %s", aus)
	}
}

// Ganz oder Fehler: ein unlesbarer Stand ist ErrStand, nie eine Teilliste.
func TestUnlesbarerStand(t *testing.T) {
	for _, st := range []string{``, `[]`, `{}`, `{"board":null}`, `{"board":{}}`, `{"board":{"sprints":{}}}`,
		`{"board":{"sprints":[{"id":"s","cards":{}}]}}`, `{"board":{"sprints":[],"termine":{}}}`} {
		if _, err := Baue([]byte(st)); !errors.Is(err, ErrStand) {
			t.Errorf("%s: %v, erwartet ErrStand", st, err)
		}
	}
	erg, err := Baue([]byte(`{"board":{"sprints":[]}}`))
	if err != nil || erg.Eintraege == nil || len(erg.Eintraege) != 0 {
		t.Errorf("leeres Board: %v %+v", err, erg)
	}
}

// Service-Lanes gehen nie hinaus, auch ihre Termine nicht.
func TestServiceLane(t *testing.T) {
	erg, _ := Baue([]byte(`{"board":{"sprints":[{"id":"__service__","kind":"service","cards":[{"id":"m","title":"Betreff von außen"}]}],
		"termine":[{"id":"t","titel":"x","datum":"2026-10-01","karteId":"m"}]}}`))
	if len(erg.Eintraege) != 0 {
		t.Errorf("Service-Lane: %+v", erg.Eintraege)
	}
}

// Kritiker #778, K-1: Ein Termin erbt „vertraulich“ von seiner Karte — auch,
// wenn die Karte fehlt (gelöscht, übertragen). Gegenprobe: ohne karteId.
func TestTerminVererbungFailClosed(t *testing.T) {
	erg, err := Baue([]byte(`{"board":{"sprints":[{"id":"s","cards":[]}],"termine":[
		{"id":"t1","titel":"Kündigung Müller","datum":"2026-10-01","karteId":"weg"},
		{"id":"t2","titel":"Messe","datum":"2026-10-01"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	aus, _ := json.Marshal(erg.Eintraege)
	if strings.Contains(string(aus), "Kündigung") || !erg.Eintraege[0].Vertraulich {
		t.Errorf("Termin zu fehlender Karte: %s", aus)
	}
	if erg.Eintraege[1].Vertraulich || erg.Eintraege[1].Titel == nil {
		t.Errorf("Gegenprobe: %s", aus)
	}
}

// Kritiker #778, K-5/K-6: doppelte Kennung → einmal, vertraulich; ungültige
// Kennung → geht nicht hinaus.
func TestKennungenDoppeltUndUngueltig(t *testing.T) {
	erg, err := Baue([]byte(`{"board":{"sprints":[{"id":"s","cards":[
		{"id":"k","displayId":1,"title":"Erste"},{"id":"k","displayId":2,"title":"Zweite","confidential":true},
		{"id":"k mit leerzeichen","title":"x"},{"id":"` + strings.Repeat("a", 65) + `","title":"y"}]}],
		"termine":[{"id":"t/1","titel":"z","datum":"2026-10-01"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	aus, _ := json.Marshal(erg.Eintraege)
	if len(erg.Eintraege) != 1 || !erg.Eintraege[0].Vertraulich || erg.Eintraege[0].Titel != nil {
		t.Fatalf("doppelte Kennung: %s", aus)
	}
	if !reflect.DeepEqual(erg.NichtBaubar, []string{"k"}) || erg.OhneKennung != 3 {
		t.Fatalf("Zählung: %+v", erg)
	}
}

// Kritiker #778, K-2: Bearbeiter halten die Grenzen des Eingangs ein
// (höchstens 10, je höchstens 60 Zeichen) und laufen durch die Titelregeln.
func TestBearbeiterGrenzen(t *testing.T) {
	var ids, namen []string
	for i := 0; i < 12; i++ {
		n := "Person " + string(rune('A'+i))
		ids = append(ids, `{"label":"`+n+`","kind":"user"}`)
		namen = append(namen, `"`+n+`"`)
	}
	lang := strings.Repeat("Langer Name ", 10)
	ids = append(ids, `{"label":"http://evil.example/x"}`, `{"label":"erika＠x.de"}`, `{"label":"`+lang+`"}`)
	st := `{"board":{"identities":[` + strings.Join(ids, ",") + `],"sprints":[{"id":"s","cards":[
		{"id":"a","title":"a","assignees":[` + strings.Join(namen, ",") + `]},
		{"id":"b","title":"b","assignees":["http://evil.example/x","erika＠x.de","` + lang + `"]}]}]}}`
	erg, err := Baue([]byte(st))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(erg.Eintraege[0].Bearbeiter); n != BearbeiterMax {
		t.Errorf("Anzahl: %d", n)
	}
	b := erg.Eintraege[1].Bearbeiter
	if len(b) != 1 || len([]rune(b[0])) > BearbeiterMaxZeichen || !strings.HasPrefix(b[0], "Langer Name") {
		t.Errorf("Labels: %q", b)
	}
}
