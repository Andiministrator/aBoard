package storage

// Sprint-117-Verify: F-3054 (Rundlauf statt Dauerbelegung des Kontingents, Logzeile
// einmal je Rest), F-3055 (Kollision im Hauptweg → wie „Karte lebt“, kein Fehler),
// F-3056 (trennende Tests für die Sperre in schiebeZurueck und den Teil-Datei-Fall).

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// F-3054: Mehr Reste als das Kontingent, keiner entscheidbar — zwei Purge-Requests
// zusammen bewerten alle, nicht zweimal dieselben 20.
func TestF3054_Rundlauf_AlleResteKommenDran(t *testing.T) {
	dir := t.TempDir()
	s := NewAttachmentStore(dir)
	n := purgeBeiseiteMaxProAufruf + 5
	alt := strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10)
	for i := 0; i < n; i++ {
		huelle := filepath.Join(dir, purgeBeiseiteRoot, alt+"__"+strings.Repeat("0", 10)+strconv.Itoa(10+i))
		if err := os.MkdirAll(filepath.Join(huelle, "b1", "k"+strconv.Itoa(i)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(huelle, "b1", "k"+strconv.Itoa(i), "x__d.txt"), nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	gefragt := map[string]int{}
	lage := func(_, k string) (bool, error) { gefragt[k]++; return false, errors.New("unentscheidbar") }
	s.raeumeBeiseiteAuf(lage, false)
	if len(gefragt) != purgeBeiseiteMaxProAufruf {
		t.Fatalf("erster Request: %d Reste, erwartet %d", len(gefragt), purgeBeiseiteMaxProAufruf)
	}
	s.raeumeBeiseiteAuf(lage, false)
	if len(gefragt) != n {
		t.Fatalf("nach zwei Requests %d verschiedene Reste bewertet, erwartet alle %d", len(gefragt), n)
	}
}

// F-3054: Ein Kollisions-Rest wird bei jedem Aufräumen erneut bewertet, aber nur
// einmal je Prozess gemeldet.
func TestF3054_KollisionNurEinmalGemeldet(t *testing.T) {
	var buf bytes.Buffer
	altOut, altFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(altOut); log.SetFlags(altFlags) })

	s, dir := purgeStore(t, "a1")
	legeRestAn(t, s, dir, 2*time.Hour)
	if err := s.Save("b1", "k1", "a1", "neu.txt", strings.NewReader("neu")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		s.raeumeBeiseiteAuf(lageFest(false, nil), false)
	}
	if n := strings.Count(buf.String(), "F-3051"); n != 1 {
		t.Fatalf("Kollision %d-mal gemeldet, erwartet 1:\n%s", n, buf.String())
	}
}

// F-3055: Holt ein Schreiben die Karte zurück und belegt ein Upload in der Zeit
// dieselbe id, endet der Purge wie „Karte lebt“ (false, nil → 409), nicht mit Fehler;
// die alte Datei bleibt im Rest, die neue im Kartenverzeichnis.
func TestF3055_KollisionImHauptweg_KeinFehler(t *testing.T) {
	s, dir := purgeStore(t, "a1")
	ok, err := s.LoescheKarteBestaetigt("b1", "k1", func() (bool, error) {
		if err := s.Save("b1", "k1", "a1", "neu.txt", strings.NewReader("neu")); err != nil {
			t.Fatal(err)
		}
		return false, nil
	}, nil)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, erwartet false/nil", ok, err)
	}
	if n := zaehleId(t, s.cardDir("b1", "k1"), "a1"); n != 1 {
		t.Fatalf("a1: %d Dateien im Kartenverzeichnis", n)
	}
	reste := beiseite(t, dir)
	if len(reste) != 1 {
		t.Fatalf("alte Datei nicht im Rest: %d Hüllen", len(reste))
	}
	if n := zaehleId(t, filepath.Join(dir, purgeBeiseiteRoot, reste[0].Name(), "b1", "k1"), "a1"); n != 1 {
		t.Fatalf("alte a1 im Rest: %d", n)
	}
}

// F-3056: schiebeZurueck wartet auf s.anlegen — sonst könnte ein Upload zwischen
// Prüfung und Zurückschieben dieselbe id belegen.
func TestF3056_SchiebeZurueck_WartetAufAnlegeSperre(t *testing.T) {
	s, dir := purgeStore(t, "a1")
	rest := legeRestAn(t, s, dir, 2*time.Hour)
	s.anlegen.Lock()
	fertig := make(chan error, 1)
	go func() { fertig <- s.schiebeZurueck(filepath.Join(rest, "b1", "k1"), s.cardDir("b1", "k1")) }()
	select {
	case <-fertig:
		s.anlegen.Unlock()
		t.Fatal("schiebeZurueck lief, obwohl s.anlegen gehalten wurde")
	case <-time.After(100 * time.Millisecond):
	}
	s.anlegen.Unlock()
	if err := <-fertig; err != nil {
		t.Fatal(err)
	}
	if !da(s, "a1") {
		t.Fatal("a1 nicht zurück")
	}
}

// F-3056: Teil-Dateien haben keine id. Liegt im Kartenverzeichnis eine andere
// Teil-Datei mit gleichem Anfang, gilt der Name trotzdem nicht als belegt.
func TestF3056_TeilDatei_KeineIdKollision(t *testing.T) {
	ziel := t.TempDir()
	if err := os.WriteFile(filepath.Join(ziel, teilPraefix+"a__x"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	belegt, err := zurueckBelegt(ziel, teilPraefix+"a__b-xyz")
	if err != nil || belegt {
		t.Fatalf("belegt=%v err=%v", belegt, err)
	}
}

// F-3058: Wird ein Kollisions-Rest aufgelöst (hier: Karte inzwischen gelöscht), fällt
// seine Meldungsmarke weg.
func TestF3058_MeldungsmarkeFaelltBeimAufloesenWeg(t *testing.T) {
	s, dir := purgeStore(t, "a1")
	legeRestAn(t, s, dir, 2*time.Hour)
	if err := s.Save("b1", "k1", "a1", "neu.txt", strings.NewReader("neu")); err != nil {
		t.Fatal(err)
	}
	s.raeumeBeiseiteAuf(lageFest(false, nil), false)
	if len(s.kollisionGemeldet) != 1 {
		t.Fatalf("Vorbedingung: %d Marken, erwartet 1", len(s.kollisionGemeldet))
	}
	s.raeumeBeiseiteAuf(lageFest(true, nil), false)
	beiseiteLeer(t, dir)
	if len(s.kollisionGemeldet) != 0 {
		t.Fatalf("%d Marken nach dem Auflösen, erwartet 0", len(s.kollisionGemeldet))
	}
}

// F-3064 (Sitzungs-Kritik 11.11.): Auch wenn der Kollisions-Rest später zurückgeschoben wird (die neue Datei
// derselben id ist inzwischen weg), fällt seine Meldungsmarke weg.
func TestF3064_MeldungsmarkeFaelltBeimZurueckschiebenWeg(t *testing.T) {
	s, dir := purgeStore(t, "a1")
	legeRestAn(t, s, dir, 2*time.Hour)
	if err := s.Save("b1", "k1", "a1", "neu.txt", strings.NewReader("neu")); err != nil {
		t.Fatal(err)
	}
	s.raeumeBeiseiteAuf(lageFest(false, nil), false)
	if len(s.kollisionGemeldet) != 1 {
		t.Fatalf("Vorbedingung: %d Marken, erwartet 1", len(s.kollisionGemeldet))
	}
	if err := s.Delete("b1", "k1", "a1"); err != nil {
		t.Fatal(err)
	}
	s.raeumeBeiseiteAuf(lageFest(false, nil), false)
	beiseiteLeer(t, dir)
	if !da(s, "a1") {
		t.Fatal("alte a1 nicht zurückgeschoben")
	}
	if len(s.kollisionGemeldet) != 0 {
		t.Fatalf("%d Marken nach dem Zurückschieben, erwartet 0", len(s.kollisionGemeldet))
	}
}
