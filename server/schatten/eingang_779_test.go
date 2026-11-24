package schatten

// Karte #779 (ADR-0140 §3): Prüfung der Schattenmenge am Eingang außen.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"testing"
)

// sid: eine gültige Schatten-Kennung (43 Zeichen base64url) aus einer Zahl.
func sid(n int) string {
	s := fmt.Sprintf("%043d", n)
	return strings.Replace(s, "0", "A", 1)
}

// ausAusgang macht aus Einträgen des Ausgangs eine Eingangs-Hülle, so wie
// das Skript es tut: id → schattenId (HMAC), sonst nichts geändert.
func ausAusgang(t *testing.T, version string, ein []Eintrag) []byte {
	t.Helper()
	var liste []map[string]json.RawMessage
	for i, e := range ein {
		roh, _ := json.Marshal(e)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(roh, &m)
		delete(m, "id")
		m["schattenId"], _ = json.Marshal(sid(i + 1))
		liste = append(liste, m)
	}
	if liste == nil {
		liste = []map[string]json.RawMessage{}
	}
	body, _ := json.Marshal(map[string]any{"version": version, "schatten": liste})
	return body
}

func eingangStatus(err error) int {
	var f *EingangFehler
	if errors.As(err, &f) {
		return f.Status
	}
	if err != nil {
		return -1
	}
	return 200
}

// Rundlauf: Alles, was der Ausgang innen baut, nimmt der Eingang außen an —
// Feld für Feld gleich. Sonst scheiterte ein echter Push am 422.
func TestEingang779_RundlaufAusgang(t *testing.T) {
	staende := [][]byte{}
	if erg := baueDatei(t, "../testdata/schatten/t1-board.json"); len(erg.Eintraege) == 0 {
		t.Fatal("T1 ohne Einträge")
	}
	t1, _ := os.ReadFile("../testdata/schatten/t1-board.json")
	staende = append(staende, t1,
		[]byte(`{"board":{"identities":[{"label":"Erika Muster","kind":"user"},{"label":"Team Süd","kind":"team"}],
		"columnDefs":[{"id":"c1","title":"Warten auf www.x.de"},{"id":"c2","title":"`+strings.Repeat("Lang ", 12)+`"},{"id":"c3","title":"  In  Arbeit "}],
		"sprints":[{"id":"s","cards":[
		{"id":"a","displayId":3,"title":"Привет","columnId":"c1","priority":"HOCH","owner":"erika muster"},
		{"id":"b","title":"`+strings.Repeat("Wort ", 30)+`","columnId":"c2","priority":"niedrig","startDate":"2026-10-01","startTime":"25:00","dueDate":"2026-10-02","dueTime":"09:30"},
		{"id":"c","displayId":5,"title":"Kyrillisch Привет weg","columnId":"c3","assignees":["Team Süd","Claude Opus"],"titelVonAgent":true},
		{"id":"d","displayId":6,"title":{"kaputt":1}},
		{"id":"e","displayId":7,"title":"x","confidential":true}]}],
		"termine":[{"id":"t1","titel":"Abstimmung","datum":"2026-10-03","von":"10:00","bis":"11:00","personen":["Erika Muster"],"vonAgent":true},
		{"id":"t2","titel":7,"datum":"2026-10-03"},{"id":"t3","titel":"Mehrtägig","datum":"2026-10-03","bisDatum":"2026-10-05"},
		{"id":"t4","titel":"an Karte","datum":"2026-10-03","karteId":"e"}]}}`))
	for i, st := range staende {
		erg, err := Baue(st)
		if err != nil {
			t.Fatal(err)
		}
		body := ausAusgang(t, "v1.52.0", erg.Eintraege)
		ein, err := LiesEingang(body, "v1.52.0")
		if err != nil {
			t.Fatalf("Stand %d: Ausgang wird am Eingang abgelehnt: %v\n%s", i, err, body)
		}
		if len(ein) != len(erg.Eintraege) {
			t.Fatalf("Stand %d: %d statt %d", i, len(ein), len(erg.Eintraege))
		}
		for j, e := range erg.Eintraege {
			g := ein[j]
			a, _ := json.Marshal([]any{e.Art, e.Nr, e.Titel, e.Vertraulich, e.Spalte, e.Prioritaet, e.StartDatum, e.StartZeit, e.EndeDatum, e.EndeZeit, e.Bearbeiter, e.VonAgent})
			b, _ := json.Marshal([]any{g.Art, g.Nr, g.Titel, g.Vertraulich, g.Spalte, g.Prioritaet, g.StartDatum, g.StartZeit, g.EndeDatum, g.EndeZeit, g.Bearbeiter, g.VonAgent})
			if string(a) != string(b) {
				t.Errorf("Stand %d Eintrag %d:\nAusgang %s\nEingang %s", i, j, a, b)
			}
		}
	}
}

// Die Prüfungen sind Fixpunkte der Ausgangsregeln: Was Titel() liefert,
// besteht PruefeTitel — auch für zufällige Eingaben.
func TestEingang779_FixpunktZufall(t *testing.T) {
	zeichen := []rune("aZ09 .,:;-–/&()+'!?äöüßÀ×÷€@:/wПри\u200b\u00adＦ\t\n<>#")
	r := rand.New(rand.NewSource(779))
	for i := 0; i < 20000; i++ {
		n := r.Intn(120)
		var sb strings.Builder
		for j := 0; j < n; j++ {
			sb.WriteRune(zeichen[r.Intn(len(zeichen))])
		}
		roh := sb.String()
		if t1 := Titel(roh); !PruefeTitel(t1) {
			t.Fatalf("Titel(%q) = %q besteht PruefeTitel nicht", roh, t1)
		}
		if s, ok := behandle(roh, SpalteMaxZeichen); ok && !PruefeSpalte(s) {
			t.Fatalf("Spalte %q besteht PruefeSpalte nicht", s)
		}
		if s, ok := behandle(roh, BearbeiterMaxZeichen); ok && bearbeiterZulaessig(s) && !PruefeBearbeiter(s) {
			t.Fatalf("Bearbeiter %q besteht PruefeBearbeiter nicht", s)
		}
	}
}

func TestEingang779_Pruefungen(t *testing.T) {
	for s, soll := range map[string]bool{
		"Angebot Müller prüfen": true, Ersatztext: true, "": false, " rand": false, "rand ": false,
		strings.Repeat("abc ", 19) + "abcd": true, strings.Repeat("abc ", 19) + "abcde": false, // 80 bzw. 81 Zeichen
		"Mail an erika@example.org": false, "siehe www.x.de": false, "Konto 1234567890123": false,
		"Привет": false, "Termin <script>": false, "Ｆｕｌｌ": false, "zero\u200bwidth": false, "Tab\tx": false,
	} {
		if PruefeTitel(s) != soll {
			t.Errorf("PruefeTitel(%q) ≠ %v", s, soll)
		}
	}
	for s, soll := range map[string]bool{"": true, "In Arbeit": true, strings.Repeat("x ", 20): false, strings.Repeat("ab ", 13) + "a": true} {
		if PruefeSpalte(s) != soll {
			t.Errorf("PruefeSpalte(%q) ≠ %v", s, soll)
		}
	}
	for s, soll := range map[string]bool{"Erika Muster": true, "": false, "external:erika": false, "g@x.de": false, strings.Repeat("ab ", 20): false} {
		if PruefeBearbeiter(s) != soll {
			t.Errorf("PruefeBearbeiter(%q) ≠ %v", s, soll)
		}
	}
	for s, soll := range map[string]bool{"": true, "niedrig": true, "mittel": true, "hoch": true, "HOCH": false, "kritisch": false} {
		if PruefePrioritaet(s) != soll {
			t.Errorf("PruefePrioritaet(%q) ≠ %v", s, soll)
		}
	}
	for s, soll := range map[string]bool{
		sid(1): true, strings.Repeat("-", 43): true, "0b7c6d1e-1111-4222-8333-944455556666": false,
		strings.Repeat("a", 42): false, strings.Repeat("a", 44): false, strings.Repeat("a", 42) + "=": false, strings.Repeat("a", 42) + "+": false,
	} {
		if GueltigeSchattenID(s) != soll {
			t.Errorf("GueltigeSchattenID(%q) ≠ %v", s, soll)
		}
	}
}

// karte: ein gültiger Karten-Schatten als Map, zum gezielten Verderben.
func karte(n int) map[string]any {
	return map[string]any{
		"schattenId": sid(n), "art": "karte", "nr": n, "titel": "Angebot prüfen", "vertraulich": false,
		"spalte": "In Arbeit", "prioritaet": "hoch", "startDatum": nil, "startZeit": nil,
		"endeDatum": "2026-10-14", "endeZeit": "09:30", "bearbeiter": []string{"Erika Muster"}, "vonAgent": false,
	}
}

func termin(n int) map[string]any {
	return map[string]any{
		"schattenId": sid(n), "art": "termin", "titel": nil, "vertraulich": true,
		"startDatum": "2026-10-15", "startZeit": "10:00", "endeDatum": nil, "endeZeit": nil, "bearbeiter": []string{}, "vonAgent": true,
	}
}

func huelle(schatten ...map[string]any) []byte {
	if schatten == nil {
		schatten = []map[string]any{}
	}
	b, _ := json.Marshal(map[string]any{"version": "1.52.0", "schatten": schatten})
	return b
}

func TestEingang779_Ablehnung(t *testing.T) {
	mit := func(basis map[string]any, k string, v any) map[string]any {
		m := map[string]any{}
		for a, b := range basis {
			m[a] = b
		}
		if v == "<weg>" {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	gut := []struct {
		name string
		body []byte
	}{
		{"leere Liste", huelle()},
		{"Karte und Termin", huelle(karte(1), termin(2))},
		{"Karte ohne nr", huelle(mit(karte(1), "nr", "<weg>"))},
		{"vertrauliche Karte", huelle(mit(mit(karte(1), "vertraulich", true), "titel", nil))},
		{"Ersatztext", huelle(mit(karte(1), "titel", Ersatztext))},
		{"zehn Bearbeiter", huelle(mit(karte(1), "bearbeiter", strings.Split("A B C D E F G H I J", " ")))},
	}
	for _, f := range gut {
		if _, err := LiesEingang(f.body, "v1.52.0"); err != nil {
			t.Errorf("%s: abgelehnt: %v", f.name, err)
		}
	}
	schlecht := []struct {
		name string
		body string
		want int
	}{
		{"kein JSON", `{`, 422},
		{"Liste statt Objekt", `[]`, 422},
		{"Daten hinter der Hülle", string(huelle()) + `{}`, 422},
		{"Hülle mit board", `{"version":"1.52.0","board":"b-intern","schatten":[]}`, 422},
		{"Hülle mit BOARD", `{"version":"1.52.0","BOARD":"x","schatten":[]}`, 422},
		{"Hülle ohne version", `{"schatten":[]}`, 422},
		{"version leer", `{"version":"","schatten":[]}`, 422},
		{"version Zahl", `{"version":1,"schatten":[]}`, 422},
		{"schatten null", `{"version":"1.52.0","schatten":null}`, 422},
		{"schatten fehlt", `{"version":"1.52.0"}`, 422},
		{"schatten doppelt", `{"version":"1.52.0","schatten":[],"schatten":[]}`, 422},
		{"Hauptversion weicht ab", `{"version":"2.0.0","schatten":[]}`, 409},
		{"unbekannter Schlüssel", string(huelle(mit(karte(1), "beschreibung", "x"))), 422},
		{"TITEL statt titel", strings.Replace(string(huelle(karte(1))), `"titel"`, `"TITEL"`, 1), 422},
		{"titel doppelt", strings.Replace(string(huelle(karte(1))), `"titel":"Angebot prüfen"`, `"titel":"Angebot prüfen","titel":"x"`, 1), 422},
		{"id statt schattenId", string(huelle(mit(mit(karte(1), "id", sid(1)), "schattenId", "<weg>"))), 422},
		{"schattenId UUID", string(huelle(mit(karte(1), "schattenId", "0b7c6d1e-1111-4222-8333-944455556666"))), 422},
		{"schattenId doppelt", string(huelle(karte(1), mit(karte(2), "schattenId", sid(1)))), 422},
		{"art fremd", string(huelle(mit(karte(1), "art", "schatten"))), 422},
		{"Termin mit nr", string(huelle(mit(termin(1), "nr", 3))), 422},
		{"Termin mit spalte null", string(huelle(mit(termin(1), "spalte", nil))), 422},
		{"Karte ohne spalte", string(huelle(mit(karte(1), "spalte", "<weg>"))), 422},
		{"Karte ohne endeZeit", string(huelle(mit(karte(1), "endeZeit", "<weg>"))), 422},
		{"nr null", string(huelle(mit(karte(1), "nr", nil))), 422},
		{"nr 0", string(huelle(mit(karte(1), "nr", 0))), 422},
		{"nr 1.5", string(huelle(mit(karte(1), "nr", 1.5))), 422},
		{"nr Text", string(huelle(mit(karte(1), "nr", "3"))), 422},
		{"vertraulich mit titel", string(huelle(mit(karte(1), "vertraulich", true))), 422},
		{"titel null ohne vertraulich", string(huelle(mit(karte(1), "titel", nil))), 422},
		{"vertraulich null", string(huelle(mit(karte(1), "vertraulich", nil))), 422},
		{"vertraulich Text", string(huelle(mit(karte(1), "vertraulich", "false"))), 422},
		{"vonAgent 0", string(huelle(mit(karte(1), "vonAgent", 0))), 422},
		{"titel mit URL", string(huelle(mit(karte(1), "titel", "siehe www.x.de"))), 422},
		{"titel 81 Zeichen", string(huelle(mit(karte(1), "titel", strings.Repeat("abc ", 19)+"abcde"))), 422},
		{"spalte 41 Zeichen", string(huelle(mit(karte(1), "spalte", strings.Repeat("a", 20)+" "+strings.Repeat("b", 20)))), 422},
		{"spalte null", string(huelle(mit(karte(1), "spalte", nil))), 422},
		{"prioritaet fremd", string(huelle(mit(karte(1), "prioritaet", "kritisch"))), 422},
		{"Datum falsch", string(huelle(mit(karte(1), "endeDatum", "14.10.2026"))), 422},
		{"Datum kein Tag", string(huelle(mit(karte(1), "endeDatum", "2026-02-30"))), 422},
		{"Zeit falsch", string(huelle(mit(karte(1), "endeZeit", "9:30"))), 422},
		{"Zeit ohne Datum", string(huelle(mit(karte(1), "startZeit", "10:00"))), 422},
		{"bearbeiter mit @", string(huelle(mit(karte(1), "bearbeiter", []string{"erika@example.org"}))), 422},
		{"bearbeiter external", string(huelle(mit(karte(1), "bearbeiter", []string{"external:x"}))), 422},
		{"elf bearbeiter", string(huelle(mit(karte(1), "bearbeiter", strings.Split("A B C D E F G H I J K", " ")))), 422},
		{"bearbeiter null", string(huelle(mit(karte(1), "bearbeiter", nil))), 422},
		{"bearbeiter Emoji", string(huelle(mit(karte(1), "bearbeiter", []string{"Erika 🙂"}))), 422},
		{"Schatten null", `{"version":"1.52.0","schatten":[null]}`, 422},
	}
	for _, f := range schlecht {
		_, err := LiesEingang([]byte(f.body), "v1.52.0")
		if s := eingangStatus(err); s != f.want {
			t.Errorf("%s: %d (%v), erwartet %d", f.name, s, err, f.want)
		}
	}
}

// Grenze 5000: 5000 gehen, 5001 nicht. Die Meldung nennt nie einen Wert.
func TestEingang779_ZaehlgrenzeUndGrund(t *testing.T) {
	viele := make([]map[string]any, EingangMaxSchatten+1)
	for i := range viele {
		viele[i] = termin(i + 1)
	}
	if _, err := LiesEingang(huelle(viele[:EingangMaxSchatten]...), "1.52.0"); err != nil {
		t.Errorf("5000: %v", err)
	}
	if s := eingangStatus(func() error { _, e := LiesEingang(huelle(viele...), "1.52.0"); return e }()); s != 422 {
		t.Errorf("5001: %d", s)
	}
	k := karte(1)
	k["titel"] = "Geheimer Text mit www.x.de"
	_, err := LiesEingang(huelle(k), "1.52.0")
	if err == nil || strings.Contains(err.Error(), "Geheim") || !strings.Contains(err.Error(), "schatten[0]: titel") {
		t.Errorf("Grund: %v", err)
	}
}

// Version: verglichen wird nur die Hauptversion, und nur, wenn beide Seiten
// SemVer melden (Builds ohne Tag tragen ein Commit-Kürzel oder „dev“).
func TestEingang779_Version(t *testing.T) {
	faelle := []struct {
		sie, ich string
		want     int
	}{
		{"1.52.0", "v1.52.0", 200}, {"v1.53.1", "1.52.0", 200}, {"v2.0.0", "v1.52.0", 409}, {"1.0.0-rc.1", "v2.0.0", 409},
		{"abc1234", "v1.52.0", 200}, {"v2.0.0", "dev", 200}, {"v2.0.0", "abc1234", 200},
		{"1.52.0 ", "v1.52.0", 422}, {"v1.52.0\n", "v1.52.0", 422}, {strings.Repeat("1", 65), "v1.52.0", 422},
	}
	for _, f := range faelle {
		body, _ := json.Marshal(map[string]any{"version": f.sie, "schatten": []any{}})
		_, err := LiesEingang(body, f.ich)
		if s := eingangStatus(err); s != f.want {
			t.Errorf("sie %q, ich %q: %d (%v), erwartet %d", f.sie, f.ich, s, err, f.want)
		}
	}
}

// K-2 (Kritiker #779): Ein Label, das erst nach dem Säubern mit „external:“
// beginnt, geht nicht hinaus — vorher gab der Ausgang „external:Bob“ aus,
// und der Eingang lehnte die ganze Menge ab.
func TestEingang779_BearbeiterNachSaeubernPruefen(t *testing.T) {
	for _, label := range []string{"ex­ternal:Bob", "ext​ernal:Bob", "ｅｘｔｅｒｎａｌ:Bob", "EXTERNAL:Bob", "ext⁠ernal:Bob"} {
		st, _ := json.Marshal(map[string]any{"board": map[string]any{
			"identities": []any{map[string]any{"label": label, "kind": "user"}},
			"sprints":    []any{map[string]any{"id": "s", "cards": []any{map[string]any{"id": "k", "title": "x", "owner": label}}}},
		}})
		erg, err := Baue(st)
		if err != nil {
			t.Fatal(err)
		}
		if b := erg.Eintraege[0].Bearbeiter; len(b) != 0 {
			t.Errorf("Label %q: Bearbeiter %q geht hinaus", label, b)
		}
		if _, err := LiesEingang(ausAusgang(t, "1.0.0", erg.Eintraege), "1.0.0"); err != nil {
			t.Errorf("Label %q: Eingang lehnt ab: %v", label, err)
		}
	}
}

// K-5: Eine Kartennummer über int32 fehlt im Ausgang, statt am Eingang zu
// scheitern.
func TestEingang779_NummerObergrenze(t *testing.T) {
	erg, err := Baue([]byte(`{"board":{"sprints":[{"id":"s","cards":[{"id":"a","displayId":2147483647,"title":"x"},{"id":"b","displayId":2147483648,"title":"y"}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if erg.Eintraege[0].Nr == nil || erg.Eintraege[1].Nr != nil {
		t.Errorf("Nummern: %v %v", erg.Eintraege[0].Nr, erg.Eintraege[1].Nr)
	}
	if _, err := LiesEingang(ausAusgang(t, "1.0.0", erg.Eintraege), "1.0.0"); err != nil {
		t.Errorf("Eingang: %v", err)
	}
}

// Rundlauf-Fuzz über Baue: zufällige Titel, Spalten, Labels (auch mit
// unsichtbaren Zeichen, Ligaturen, Kombinationszeichen, Präfix external:),
// Nummern und Daten — was der Ausgang baut, nimmt der Eingang an.
func TestEingang779_RundlaufFuzz(t *testing.T) {
	zeichen := []rune("aZ09 .,:;-–/&()+'!?äöüßÀ×÷€@wПри​­⁠̈ﬁＦｅｘ\t<>#external")
	r := rand.New(rand.NewSource(7791))
	wort := func(n int) string {
		var sb strings.Builder
		for j := r.Intn(n); j >= 0; j-- {
			sb.WriteRune(zeichen[r.Intn(len(zeichen))])
		}
		if r.Intn(5) == 0 {
			return "ex­ternal:" + sb.String()
		}
		return sb.String()
	}
	for lauf := 0; lauf < 300; lauf++ {
		var ids, cols, cards, termine []any
		labels := []string{}
		for i := 0; i < 5; i++ {
			l := wort(70)
			labels = append(labels, l)
			ids = append(ids, map[string]any{"label": l, "kind": "user"})
			cols = append(cols, map[string]any{"id": fmt.Sprintf("c%d", i), "title": wort(60)})
		}
		for i := 0; i < 20; i++ {
			cards = append(cards, map[string]any{"id": fmt.Sprintf("k%d", i), "displayId": r.Intn(1 << 33), "title": wort(120),
				"columnId": fmt.Sprintf("c%d", r.Intn(6)), "priority": []string{"hoch", "x", ""}[r.Intn(3)],
				"owner": labels[r.Intn(5)], "assignees": []string{labels[r.Intn(5)], wort(10)},
				"dueDate": []string{"2026-10-01", "2026-13-01", ""}[r.Intn(3)], "dueTime": []string{"09:30", "24:00", ""}[r.Intn(3)],
				"confidential": r.Intn(4) == 0})
			termine = append(termine, map[string]any{"id": fmt.Sprintf("t%d", i), "titel": wort(100), "datum": "2026-10-03",
				"von": "10:00", "bis": []string{"11:00", "x"}[r.Intn(2)], "bisDatum": []string{"", "2026-10-01", "2026-10-09"}[r.Intn(3)],
				"personen": []string{labels[r.Intn(5)]}, "karteId": []string{"", "k1", "fehlt"}[r.Intn(3)]})
		}
		st, _ := json.Marshal(map[string]any{"board": map[string]any{"identities": ids, "columnDefs": cols,
			"sprints": []any{map[string]any{"id": "s", "cards": cards}}, "termine": termine}})
		erg, err := Baue(st)
		if err != nil {
			t.Fatal(err)
		}
		body := ausAusgang(t, "1.0.0", erg.Eintraege)
		if _, err := LiesEingang(body, "1.0.0"); err != nil {
			t.Fatalf("Lauf %d: Ausgang am Eingang abgelehnt: %v", lauf, err)
		}
	}
}

// K-1: Die Zählgrenze greift beim Lesen, nicht nach dem Anlegen aller
// Elemente — 32 MB aus „0,“ kosten nicht mehr Gigabytes.
func TestEingang779_ZaehlgrenzeVorAllokation(t *testing.T) {
	body := []byte(`{"version":"1.0.0","schatten":[` + strings.Repeat("0,", 16<<20-20) + `0]}`)
	var vor, nach runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&vor)
	_, err := LiesEingang(body, "1.0.0")
	runtime.ReadMemStats(&nach)
	if s := eingangStatus(err); s != 422 {
		t.Fatalf("Status %d (%v)", s, err)
	}
	if mb := (nach.TotalAlloc - vor.TotalAlloc) >> 20; mb > 400 {
		t.Errorf("%d MB allokiert, erwartet höchstens 400", mb)
	}
}

// K-6: Auch ein doppelter Schlüssel erscheint nur gekürzt in der Meldung —
// ein Schlüsselname kann Inhalt tragen.
func TestEingang779_DoppelterSchluesselGekuerzt(t *testing.T) {
	lang := strings.Repeat("Geheim", 20)
	_, err := LiesEingang([]byte(`{"version":"1.0.0","schatten":[],"`+lang+`":1,"`+lang+`":2}`), "1.0.0")
	if err == nil || strings.Contains(err.Error(), lang) {
		t.Errorf("Meldung: %v", err)
	}
}

// Sitzungs-QA C-1: Die Grenze beim Lesen gilt auch für die Bearbeiter-Liste
// und für die Schlüssel eines Objekts — sonst kosten 31 MB „0,“ bzw. eine
// Schlüsselflut wieder Gigabytes, nur an anderer Stelle (Klasse 214).
func TestEingang779_GrenzeBeimLesenUeberall(t *testing.T) {
	flut := strings.Repeat("0,", 15<<20) + "0"
	schluessel := func(n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			fmt.Fprintf(&sb, `"k%d":0,`, i)
		}
		return sb.String()
	}
	faelle := map[string]string{
		"bearbeiter-Flut":            `{"version":"1.0.0","schatten":[{"schattenId":"` + sid(1) + `","art":"termin","titel":null,"vertraulich":true,"startDatum":null,"startZeit":null,"endeDatum":null,"endeZeit":null,"vonAgent":false,"bearbeiter":[` + flut + `]}]}`,
		"Schlüsselflut im Schatten":  `{"version":"1.0.0","schatten":[{` + schluessel(1<<21) + `"art":"termin"}]}`,
		"Schlüsselflut in der Hülle": `{` + schluessel(1<<21) + `"version":"1.0.0","schatten":[]}`,
	}
	for name, body := range faelle {
		var vor, nach runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&vor)
		_, err := LiesEingang([]byte(body), "1.0.0")
		runtime.ReadMemStats(&nach)
		if s := eingangStatus(err); s != 422 {
			t.Errorf("%s: Status %d (%v)", name, s, err)
		}
		if mb := (nach.TotalAlloc - vor.TotalAlloc) >> 20; mb > 400 {
			t.Errorf("%s: %d MB allokiert, erwartet höchstens 400", name, mb)
		}
	}
}
