package fold

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/dop251/goja"
)

func neuerTestMotor(t *testing.T, opt Optionen) *Motor {
	t.Helper()
	m, err := Neu(opt)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestBuendelLaedt: das eingebettete Bündel lädt und setzt alle Exporte,
// die der Server und die Konformitätstests brauchen.
func TestBuendelLaedt(t *testing.T) {
	l, err := neuerLaeufer()
	if err != nil {
		t.Fatal(err)
	}
	ops := l.rt.Get("AboardOps")
	if ops == nil || goja.IsUndefined(ops) {
		t.Fatal("AboardOps fehlt — setzt eintrag.js das Global noch?")
	}
	for _, name := range []string{"fold", "validateOp", "tsSchluessel", "inspectOpPayload", "regelVerwerfung", "makeOp"} {
		if _, ok := goja.AssertFunction(ops.ToObject(l.rt).Get(name)); !ok {
			t.Errorf("Export %s fehlt oder ist keine Funktion", name)
		}
	}
}

// ── Konformität gegen Fixtures (ADR-0129 §4 Nachtrag, Punkt 4) ──────────

type fixture struct {
	Fn               string `json:"fn"`
	Datei            string `json:"datei"`
	Test             string `json:"test"`
	Args             string `json:"args"`
	ErwartetErgebnis string `json:"erwartetErgebnis"`
	ErwartetArgsNach string `json:"erwartetArgsNach"`
}

func ladeFixtures(t *testing.T) []fixture {
	t.Helper()
	d, err := os.Open("testdata/fixtures.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	gz, err := gzip.NewReader(d)
	if err != nil {
		t.Fatal(err)
	}
	var fx []fixture
	if err := json.NewDecoder(gz).Decode(&fx); err != nil {
		t.Fatal(err)
	}
	// Untergrenze statt Gleichheit: neue Op-Tests dürfen Fixtures hinzufügen.
	// Fällt die Zahl, ist beim Neuerzeugen etwas verloren gegangen.
	if len(fx) < 866 {
		t.Fatalf("nur %d Fixtures — erwartet mindestens 866 (Stand Spike #546)", len(fx))
	}
	return fx
}

// TestKonformitaetFixtures: jeder mitgeschnittene Aufruf aus tests/ops.test.js
// und tests/orphan-ops.test.js liefert in goja byte-identisch dasselbe wie
// unter bun — Rückgabe UND Argumente danach (Protokoll, Reinheit).
func TestKonformitaetFixtures(t *testing.T) {
	fx := ladeFixtures(t)
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	abweichend := 0
	for i, x := range fx {
		erg, nach, err := m.rufe(context.Background(), x.Fn, x.Args, false)
		if err != nil || erg != x.ErwartetErgebnis || nach != x.ErwartetArgsNach {
			abweichend++
			if abweichend <= 5 {
				t.Errorf("Fixture %d (%s / %s / %s) weicht ab: err=%v\n  goja: %.300s\n  bun:  %.300s",
					i, x.Datei, x.Test, x.Fn, err, erg, x.ErwartetErgebnis)
			}
		}
	}
	t.Logf("Fixtures byte-identisch: %d / %d", len(fx)-abweichend, len(fx))
}

// TestKonformitaetOhneKlon sichert die Server-Voreinstellung ab: Ohne Klon
// muss fold dieselbe Projektion und dasselbe Protokoll liefern. Verglichen
// wird NICHT der Snapshot danach — ohne Klon mutiert fold den frisch
// geparsten Snapshot absichtlich; der Server verwirft ihn ohnehin.
func TestKonformitaetOhneKlon(t *testing.T) {
	fx := ladeFixtures(t)
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	n, abweichend := 0, 0
	for i, x := range fx {
		if x.Fn != "fold" {
			continue
		}
		n++
		erg, nach, err := m.rufe(context.Background(), x.Fn, x.Args, true)
		var a, b []json.RawMessage
		_ = json.Unmarshal([]byte(nach), &a)
		_ = json.Unmarshal([]byte(x.ErwartetArgsNach), &b)
		protokollGleich := len(a) == len(b) && (len(a) < 3 || string(a[2]) == string(b[2]))
		if err != nil || erg != x.ErwartetErgebnis || !protokollGleich {
			abweichend++
			if abweichend <= 5 {
				t.Errorf("Fixture %d (%s / %s) weicht ohne Klon ab: err=%v protokollGleich=%v", i, x.Datei, x.Test, err, protokollGleich)
			}
		}
	}
	if n < 800 {
		t.Fatalf("nur %d fold-Fixtures — erwartet mindestens 800", n)
	}
	t.Logf("ohne Klon: %d / %d fold-Fixtures gleich", n-abweichend, n)
}

// TestFalteBeideModiGleich: die Server-Form (Falte) liefert mit und ohne
// Klon dieselbe Projektion und dasselbe Protokoll, auf einem Batch mit
// allen gängigen Op-Typen.
func TestFalteBeideModiGleich(t *testing.T) {
	snap := synthBoard("m", 200)
	ops := batch("modi", kartenIDs(snap), 50, 0)
	ohne := neuerTestMotor(t, Optionen{})
	mit := neuerTestMotor(t, Optionen{MitKlon: true})
	a, err := ohne.Falte(context.Background(), snap, ops, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := mit.Falte(context.Background(), snap, ops, "")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("Ergebnis ohne Klon ≠ mit Klon")
	}
	if a.Projektion == snap {
		t.Fatal("Projektion unverändert — der Batch hat nichts bewirkt")
	}
	var protokoll []any
	if err := json.Unmarshal([]byte(a.Protokoll), &protokoll); err != nil {
		t.Fatalf("Protokoll ist kein JSON-Array: %v", err)
	}
}

// TestWeicheOhneKlonWirkt: Die Gleichheitstests oben blieben auch grün, wenn
// die Weiche gar nichts täte. Beleg, dass sie greift: ohne Klon ändert fold
// den übergebenen Snapshot (er IST die Projektion), mit Klon bleibt er
// unberührt.
func TestWeicheOhneKlonWirkt(t *testing.T) {
	snap := synthBoard("k", 20)
	args := "[" + snap + "," + batch("k", kartenIDs(snap), 10, 0) + ",[]]"
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	for _, ohneKlon := range []bool{false, true} {
		_, nach, err := m.rufe(context.Background(), "fold", args, ohneKlon)
		if err != nil {
			t.Fatal(err)
		}
		var a []json.RawMessage
		if err := json.Unmarshal([]byte(nach), &a); err != nil {
			t.Fatal(err)
		}
		veraendert := string(a[0]) != snap
		if veraendert != ohneKlon {
			t.Errorf("ohneKlon=%v: Snapshot verändert=%v", ohneKlon, veraendert)
		}
	}
	// Die Weiche ist auf die Dauer EINES Aufrufs begrenzt: danach ist
	// structuredClone wieder so wie vorher (in goja: nicht vorhanden).
	_ = m.mitLaeufer(context.Background(), func(l *laeufer) error {
		if v := l.rt.Get("structuredClone"); v != nil && !goja.IsUndefined(v) {
			t.Error("structuredClone nach dem Aufruf noch gesetzt")
		}
		return nil
	})
}

// TestFalteReichtOptionenDurch: das vierte Argument erreicht fold(). Belegt
// über feldMassstab — mit Massstab meldet das Protokoll die Felder, die einer
// neu angelegten Karte fehlen; ohne Massstab nicht.
func TestFalteReichtOptionenDurch(t *testing.T) {
	snap := synthBoard("o", 5)
	ops := `[{"id":"op1","ts":"2026-09-28T12:00:00.000Z","actor":"A","schemaVersion":1,"type":"card.upsert",
	  "payload":{"sprintId":"s1","columnId":"col-ready","card":{"id":"neu","title":"x"}}}]`
	m := neuerTestMotor(t, Optionen{})
	ohne, err := m.Falte(context.Background(), snap, ops, "")
	if err != nil {
		t.Fatal(err)
	}
	mit, err := m.Falte(context.Background(), snap, ops, `{"feldMassstab":["id","title","owner","labels"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if ohne.Protokoll == mit.Protokoll {
		t.Fatalf("Optionen wirkungslos — Protokoll in beiden Fällen %s", ohne.Protokoll)
	}
}

// ── Zeitgrenze (ADR-0129 §4 Nachtrag, Punkt 6) ──────────────────────────

// TestZeitgrenzeBrichtAb: eine Endlosschleife kommt als ErrZeitgrenze
// zurück, und der NÄCHSTE Aufruf auf demselben (einzigen) Pool-Platz läuft
// normal — die abgebrochene Runtime wurde ersetzt.
func TestZeitgrenzeBrichtAb(t *testing.T) {
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1, Zeitgrenze: 50 * time.Millisecond})
	t0 := time.Now()
	err := m.mitLaeufer(context.Background(), func(l *laeufer) error {
		_, err := l.rt.RunString("for(;;){}")
		return err
	})
	if !errors.Is(err, ErrZeitgrenze) {
		t.Fatalf("erwartet ErrZeitgrenze, bekommen %v", err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("Abbruch erst nach %v", d)
	}
	snap := synthBoard("z", 10)
	if _, err := m.Falte(context.Background(), snap, batch("z", kartenIDs(snap), 5, 0), ""); err != nil {
		t.Fatalf("Folgeaufruf nach Abbruch scheitert: %v", err)
	}
}

// TestSpaeteUhrVergiftetNichtDenNaechstenAufruf: Die Uhr feuert, NACHDEM fn
// fertig ist (hier: Go-seitige Arbeit ohne JS). Das Interrupt-Flag der
// Runtime ist dann gesetzt, ohne dass ein Aufruf es verbraucht hat. Würde die
// Runtime zurückgelegt, bräche der nächste, völlig normale Aufruf sofort mit
// ErrZeitgrenze ab. Gegenprobe: in mitLaeufer den Ersatz bei `gefeuert`
// weglassen → dieser Test wird rot.
func TestSpaeteUhrVergiftetNichtDenNaechstenAufruf(t *testing.T) {
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1, Zeitgrenze: 20 * time.Millisecond})
	err := m.mitLaeufer(context.Background(), func(l *laeufer) error {
		time.Sleep(60 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("erwartet nil (fn war fertig), bekommen %v", err)
	}
	snap := synthBoard("u", 10)
	if _, err := m.Falte(context.Background(), snap, batch("u", kartenIDs(snap), 5, 0), ""); err != nil {
		t.Fatalf("Aufruf nach später Uhr scheitert: %v", err)
	}
}

// TestJSFehlerErsetztRuntime: Eine Exception mitten in der Brücke (hier:
// kaputtes JSON) wird als Fehler gemeldet, und die Runtime danach ersetzt.
// Belegt über ein Global, das die alte Runtime trägt und die neue nicht.
func TestJSFehlerErsetztRuntime(t *testing.T) {
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	_ = m.mitLaeufer(context.Background(), func(l *laeufer) error {
		return l.rt.Set("__markiert", true)
	})
	if _, err := m.Falte(context.Background(), "{kein json", "[]", ""); err == nil {
		t.Fatal("kaputter Snapshot ohne Fehler durchgegangen")
	}
	_ = m.mitLaeufer(context.Background(), func(l *laeufer) error {
		if v := l.rt.Get("__markiert"); v != nil && !goja.IsUndefined(v) {
			t.Error("Runtime nach JS-Fehler zurückgelegt statt ersetzt")
		}
		return nil
	})
}

// TestWartenRespektiertKontext: Ist der Pool belegt, kehrt ein Aufruf mit
// abgelaufenem Kontext zurück, statt zu warten.
func TestWartenRespektiertKontext(t *testing.T) {
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	belegt := <-m.frei
	defer func() { m.frei <- belegt }()
	ctx, abbrechen := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer abbrechen()
	_, err := m.Falte(ctx, synthBoard("w", 1), "[]", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("erwartet DeadlineExceeded, bekommen %v", err)
	}
}

// ── Nebenläufigkeit (K-6) ────────────────────────────────────────────────

// TestNebenlaeufig: 8 Goroutinen auf 4 Boards, Pool kleiner als die Zahl der
// Goroutinen. Unter `go test -race` ohne Befund; jedes Board gleicht
// bit-genau einem sequenziellen Nachfalten in der beobachteten Reihenfolge,
// displayIds bleiben eindeutig. Die Sperre je Board steht für BEGIN
// IMMEDIATE (ADR-0129 §4, K-5).
func TestNebenlaeufig(t *testing.T) {
	const nBoards, nGoroutinen, nBatches, nOps, nKarten = 4, 8, 5, 50, 100
	m := neuerTestMotor(t, Optionen{PoolGroesse: 2})
	type zustand struct {
		mu    sync.Mutex
		blob  string
		folge []string
	}
	start := make([]string, nBoards)
	boards := make([]*zustand, nBoards)
	for i := range boards {
		start[i] = synthBoard(fmt.Sprintf("b%d", i), nKarten)
		boards[i] = &zustand{blob: start[i]}
	}
	batches := map[string]string{}
	for g := 0; g < nGoroutinen; g++ {
		ids := kartenIDs(start[g%nBoards])
		for k := 0; k < nBatches; k++ {
			tag := fmt.Sprintf("g%d-b%d", g, k)
			batches[tag] = batch(tag, ids, nOps, (g*nBatches+k)*nOps)
		}
	}
	var wg sync.WaitGroup
	fehler := make(chan error, nGoroutinen)
	for g := 0; g < nGoroutinen; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			z := boards[g%nBoards]
			for k := 0; k < nBatches; k++ {
				tag := fmt.Sprintf("g%d-b%d", g, k)
				z.mu.Lock()
				erg, err := m.Falte(context.Background(), z.blob, batches[tag], "")
				if err == nil {
					z.blob = erg.Projektion
					z.folge = append(z.folge, tag)
				}
				z.mu.Unlock()
				if err != nil {
					fehler <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(fehler)
	for err := range fehler {
		t.Fatal(err)
	}
	ref := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	for i, z := range boards {
		blob := start[i]
		for _, tag := range z.folge {
			erg, err := ref.Falte(context.Background(), blob, batches[tag], "")
			if err != nil {
				t.Fatal(err)
			}
			blob = erg.Projektion
		}
		if blob != z.blob {
			t.Errorf("Board %d: nebenläufiges Ergebnis ≠ sequenzielles Nachfalten", i)
		}
		if len(z.folge) != 2*nBatches {
			t.Errorf("Board %d: %d Batches angewandt, erwartet %d", i, len(z.folge), 2*nBatches)
		}
		pruefeDisplayIDs(t, i, blob)
	}
}

func pruefeDisplayIDs(t *testing.T, board int, blob string) {
	t.Helper()
	var s struct {
		Board struct {
			Sprints []struct {
				Cards []struct {
					ID        string `json:"id"`
					DisplayID int    `json:"displayId"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal([]byte(blob), &s); err != nil {
		t.Fatal(err)
	}
	gesehen := map[int]string{}
	for _, sp := range s.Board.Sprints {
		for _, c := range sp.Cards {
			if alt, ok := gesehen[c.DisplayID]; ok {
				t.Errorf("Board %d: displayId %d doppelt (%s, %s)", board, c.DisplayID, alt, c.ID)
			}
			gesehen[c.DisplayID] = c.ID
		}
	}
}

// ── Laufzeit ─────────────────────────────────────────────────────────────

// TestLaufzeitGrenze: 50 Ops auf 500 Karten, Median aus 15 Läufen unter
// 200 ms (ADR-0129 §4). Geprüft auf dem synthetischen Board; das echte
// aBoard-Board (Repo-Wurzel) wird nur protokolliert, weil es mit jedem
// Sprint wächst. Unter -race nur protokolliert.
func TestLaufzeitGrenze(t *testing.T) {
	faelle := []struct {
		name   string
		snap   string
		pruefe bool
	}{{"synthetisch-500", synthBoard("m", 500), true}}
	if raw, err := os.ReadFile("../../aboard-sync.base.json"); err == nil {
		faelle = append(faelle, struct {
			name   string
			snap   string
			pruefe bool
		}{"echt-aboard", string(raw), false})
	}
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	// Gemessen werden beide Wege: Falte (ein fold über den Batch) und
	// FalteMitZielen (Op für Op, der Weg des Servers seit F-1146).
	wege := []struct {
		name string
		fn   func(context.Context, string, string, string) (Ergebnis, error)
	}{{"Falte", m.Falte}, {"FalteMitZielen", m.FalteMitZielen}}
	for _, f := range faelle {
		ops := serverBatch(t, batch("zeit", kartenIDs(f.snap), 50, 0))
		for _, weg := range wege {
			dauern := make([]time.Duration, 0, 15)
			for i := 0; i < 15; i++ {
				t0 := time.Now()
				if _, err := weg.fn(context.Background(), f.snap, ops, ""); err != nil {
					t.Fatal(err)
				}
				dauern = append(dauern, time.Since(t0))
			}
			sort.Slice(dauern, func(i, j int) bool { return dauern[i] < dauern[j] })
			median := dauern[len(dauern)/2]
			t.Logf("%s/%s: %d Karten, %d B; Median %v, Max %v", f.name, weg.name, len(kartenIDs(f.snap)), len(f.snap), median, dauern[len(dauern)-1])
			if f.pruefe && !raceAktiv && median >= 200*time.Millisecond {
				t.Errorf("%s/%s: Median %v ≥ 200 ms", f.name, weg.name, median)
			}
		}
	}
}

func BenchmarkFalte50OpsAuf500Karten(b *testing.B) {
	snap := synthBoard("m", 500)
	ops := batch("bench", kartenIDs(snap), 50, 0)
	m, err := Neu(Optionen{PoolGroesse: 1})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Falte(context.Background(), snap, ops, ""); err != nil {
			b.Fatal(err)
		}
	}
}

// ── Vorprüfung (Karte #552) ──────────────────────────────────────────────

func TestSchemaVersionAusBuendel(t *testing.T) {
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	if m.SchemaVersion() != 1 {
		t.Fatalf("SchemaVersion = %d, erwartet 1 (OP_SCHEMA_VERSION in src/ops.js)", m.SchemaVersion())
	}
}

// TestPruefe: je Op ein Ergebnis in derselben Reihenfolge; fehlendes
// Pflichtfeld macht ungültig, ein unbekannter Schlüssel nicht — er wird mit
// dem gemeinten Namen gemeldet (F-451).
func TestPruefe(t *testing.T) {
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	ops := `[
	 {"id":"a","ts":"2026-09-28T12:00:00.000Z","schemaVersion":1,"type":"card.field","payload":{"cardId":"k","field":"qa","value":true}},
	 {"id":"b","ts":"2026-09-28T12:00:00.000Z","schemaVersion":1,"type":"card.move","payload":{"cardId":"k","toSprintId":"s1","toColumnId":"col-ready"}},
	 {"id":"c","ts":"2026-09-28T12:00:00.000Z","schemaVersion":1,"type":"card.field","payload":{"field":"qa"}},
	 {"id":"d","ts":"2026-09-28T12:00:00.000Z","schemaVersion":1,"type":"gibt.es.nicht","payload":{}}
	]`
	p, err := m.Pruefe(context.Background(), ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 4 {
		t.Fatalf("%d Ergebnisse, erwartet 4", len(p))
	}
	if !p[0].Gueltig || len(p[0].Unbekannt) != 0 {
		t.Errorf("Op 0: %+v", p[0])
	}
	if !p[1].Gueltig || len(p[1].Unbekannt) != 1 || p[1].Unbekannt[0].Key != "toColumnId" ||
		p[1].Unbekannt[0].Meant == nil || *p[1].Unbekannt[0].Meant != "toColId" {
		t.Errorf("Op 1: erwartet gültig mit Vorschlag toColId, bekommen %+v", p[1])
	}
	if p[2].Gueltig || len(p[2].Fehlend) != 1 || p[2].Fehlend[0] != "cardId" {
		t.Errorf("Op 2: erwartet ungültig, fehlend cardId, bekommen %+v", p[2])
	}
	if p[3].Gueltig {
		t.Errorf("Op 3: unbekannter Typ als gültig gemeldet")
	}
	if _, err := m.Pruefe(context.Background(), `{"kein":"array"}`); err == nil {
		t.Error("Nicht-Array ohne Fehler angenommen")
	}
}

// ── Ziel-Prüfung (F-1146, Karte #558) ────────────────────────────────────

// serverBatch stempelt einen batch() so, wie der Server es tut: ein
// gemeinsamer ts, seq = Index. Nur so ist die Array-Reihenfolge die
// Faltordnung, die FalteMitZielen verlangt.
func serverBatch(t *testing.T, ops string) string {
	t.Helper()
	var liste []map[string]any
	if err := json.Unmarshal([]byte(ops), &liste); err != nil {
		t.Fatal(err)
	}
	for i, op := range liste {
		op["ts"] = "2026-09-28T12:00:00.000Z"
		op["seq"] = i
	}
	raw, _ := json.Marshal(liste)
	return string(raw)
}

// TestFalteMitZielenGleichFalte: Op für Op zu falten liefert dieselbe
// Projektion wie EIN fold() über den Batch, in beiden Klon-Modi; das
// Protokoll ist das von Falte plus die ziel-fehlt-Einträge. Der Batch
// enthält card.delete mit späteren Ops auf dieselbe Karte, erzeugt also
// ziel-fehlt-Einträge — sonst belegte der Vergleich den Protokollteil nicht.
func TestFalteMitZielenGleichFalte(t *testing.T) {
	snap := synthBoard("z", 31) // 31 statt 30: dann trifft (i*7)%31 gelöschte Karten wieder
	ops := serverBatch(t, batch("ziel", kartenIDs(snap), 120, 0))
	for _, mitKlon := range []bool{false, true} {
		m := neuerTestMotor(t, Optionen{MitKlon: mitKlon})
		alt, err := m.Falte(context.Background(), snap, ops, "")
		if err != nil {
			t.Fatal(err)
		}
		neu, err := m.FalteMitZielen(context.Background(), snap, ops, "")
		if err != nil {
			t.Fatal(err)
		}
		if neu.Projektion != alt.Projektion {
			t.Fatalf("mitKlon=%v: Projektion weicht von Falte ab", mitKlon)
		}
		var pAlt, pNeu []map[string]any
		_ = json.Unmarshal([]byte(alt.Protokoll), &pAlt)
		_ = json.Unmarshal([]byte(neu.Protokoll), &pNeu)
		var ohneZiel []map[string]any
		ziele := 0
		for _, e := range pNeu {
			if e["art"] == "ziel-fehlt" {
				ziele++
				continue
			}
			ohneZiel = append(ohneZiel, e)
		}
		a, _ := json.Marshal(pAlt)
		b, _ := json.Marshal(ohneZiel)
		if len(pAlt) == 0 && len(ohneZiel) == 0 {
			a, b = []byte("[]"), []byte("[]")
		}
		if string(a) != string(b) {
			t.Errorf("mitKlon=%v: übriges Protokoll weicht ab:\n%s\n%s", mitKlon, a, b)
		}
		if ziele == 0 {
			t.Errorf("mitKlon=%v: keine ziel-fehlt-Einträge — der Batch prüft den Fall nicht", mitKlon)
		}
	}
}

// TestFalteMitZielenVerlangtFaltordnung: Ops mit verschiedenen ts könnte
// fold() anders ordnen als das Array — dann wäre die Prüfung je Op gegen den
// falschen Stand gelaufen. Abweisen statt still anders falten.
func TestFalteMitZielenVerlangtFaltordnung(t *testing.T) {
	snap := synthBoard("f", 5)
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	if _, err := m.FalteMitZielen(context.Background(), snap, batch("f", kartenIDs(snap), 3, 0), ""); err == nil {
		t.Fatal("Ops mit verschiedenen ts angenommen")
	}
}

// TestFalteMitZielenOhneBacklogUndOhneSprints (F-1152, Karte #561): Zwei
// Randlagen, in denen fold() eine neue Karte still verwirft und die Brücke
// bisher nichts meldete.
//   - Service-Board ohne __backlog__, Neuanlage ohne sprintId: fold() fände
//     kein Ziel, verwürfe die Karte und verbrauchte eine displayId.
//   - Board ohne sprints-Array: die Zielprüfung wurde übersprungen.
//
// Und die Gegenrichtung: mit __backlog__ bleibt dieselbe Op ohne Meldung.
func TestFalteMitZielenOhneBacklogUndOhneSprints(t *testing.T) {
	m := neuerTestMotor(t, Optionen{PoolGroesse: 1})
	op := `[{"id":"o1","ts":"2026-09-29T00:00:00.000Z","seq":0,"actor":"t","schemaVersion":1,"type":"card.upsert","payload":{"card":{"id":"n1","title":"x"}}}]`
	faelle := []struct {
		name, snap string
		ziel       bool
	}{
		{"Service-Board ohne __backlog__", `{"version":"2","board":{"id":"b","title":"B","nextDisplayId":1,"sprints":[{"id":"__service__","kind":"service","cards":[]}]}}`, true},
		{"Board ohne sprints", `{"version":"2","board":{"id":"b","title":"B"}}`, true},
		{"mit __backlog__ (Gegenrichtung)", `{"version":"2","board":{"id":"b","title":"B","nextDisplayId":1,"sprints":[{"id":"__backlog__","kind":"backlog","cards":[]}]}}`, false},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			erg, err := m.FalteMitZielen(context.Background(), f.snap, op, "")
			if err != nil {
				t.Fatal(err)
			}
			var p []map[string]any
			_ = json.Unmarshal([]byte(erg.Protokoll), &p)
			gefunden := false
			for _, e := range p {
				if e["art"] == "ziel-fehlt" && e["cardId"] == "n1" {
					gefunden = true
				}
			}
			if gefunden != f.ziel {
				t.Errorf("ziel-fehlt für n1 = %v, erwartet %v; Protokoll %s", gefunden, f.ziel, erg.Protokoll)
			}
		})
	}
}
