package storage

// Sprint-117-Kritiker: F-3050 (Reste beim Serverstart auflösen, ohne Alter und ohne
// uhr-Gate), F-3051 (Zurückschieben verliert keine Datei und belegt keine id doppelt),
// F-3052 (Obergrenze je Purge-Request).

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/uhr"
)

func uhrNichtGestellt(t *testing.T) {
	t.Helper()
	alt := uhr.Gestellt
	uhr.Gestellt = func() (bool, error) { return false, nil }
	t.Cleanup(func() { uhr.Gestellt = alt })
}

// F-3050: Beim Start werden auch frische Reste aufgelöst, und zwar ohne gestellte Uhr —
// lebende Karte zurück, verwaiste weg, unentscheidbare bleiben.
func TestF3050_StartLoestFrischeResteAuf_OhneUhr(t *testing.T) {
	uhrNichtGestellt(t)

	s, dir := purgeStore(t, "a1")
	legeRestAn(t, s, dir, 0)
	s.raeumeBeiseiteAuf(lageFest(false, nil), false) // Purge-Weg: frisch + Uhr aus → nichts
	if da(s, "a1") {
		t.Fatal("Vorbedingung: der Purge-Weg darf frische Reste nicht anfassen")
	}
	s.RaeumePurgeResteAuf(lageFest(false, nil))
	if !da(s, "a1") {
		t.Fatal("Start: Datei der lebenden Karte nicht zurückgeschoben")
	}
	beiseiteLeer(t, dir)

	s2, dir2 := purgeStore(t, "a1")
	rest := legeRestAn(t, s2, dir2, 0)
	s2.RaeumePurgeResteAuf(lageFest(false, errors.New("stand unlesbar")))
	if _, err := os.Stat(rest); err != nil {
		t.Fatal("Start: Rest bei unlesbarem Stand angefasst")
	}
	s2.RaeumePurgeResteAuf(lageFest(true, nil))
	if _, err := os.Stat(rest); !os.IsNotExist(err) {
		t.Fatal("Start: verwaister Rest blieb liegen")
	}
}

// F-3051: Ist die id inzwischen neu belegt (anderer Dateiname) oder liegt derselbe Name
// schon da, bleibt die alte Datei im Rest; alles andere geht zurück, und zu keiner id
// liegen danach zwei Dateien im Kartenverzeichnis.
func TestF3051_ZurueckschiebenBeiBelegterId_KeinVerlustKeinDoppel(t *testing.T) {
	s, dir := purgeStore(t, "a1", "a2", "a3")
	rest := legeRestAn(t, s, dir, 2*time.Hour)
	// Während die Karte beiseite liegt: Upload mit id a1 (anderer Name) und a2 (gleicher Name).
	if err := s.Save("b1", "k1", "a1", "neu.txt", strings.NewReader("neu")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("b1", "k1", "a2", "a2.txt", strings.NewReader("neu")); err != nil {
		t.Fatal(err)
	}
	ziel := s.cardDir("b1", "k1")
	err := s.schiebeZurueck(filepath.Join(rest, "b1", "k1"), ziel)
	if !errors.Is(err, ErrPurgeRestKollision) {
		t.Fatalf("err=%v, erwartet ErrPurgeRestKollision", err)
	}
	for _, id := range []string{"a1", "a2"} {
		if n := zaehleId(t, ziel, id); n != 1 {
			t.Fatalf("id %s: %d Dateien im Kartenverzeichnis, erwartet 1", id, n)
		}
		if n := zaehleId(t, filepath.Join(rest, "b1", "k1"), id); n != 1 {
			t.Fatalf("id %s: alte Datei nicht im Rest erhalten (%d)", id, n)
		}
	}
	if !da(s, "a3") {
		t.Fatal("a3 (ohne Kollision) nicht zurückgeschoben")
	}
	// Das spätere Aufräumen bei lebender Karte lässt den Rest weiter liegen.
	s.raeumeBeiseiteAuf(lageFest(false, nil), false)
	if zaehleId(t, filepath.Join(rest, "b1", "k1"), "a1") != 1 {
		t.Fatal("Aufräumen hat die alte Datei verloren")
	}
}

// F-3051: ids mit Unterstrich — `a__b` belegt, Datei `a__b__x.txt` im Rest darf nicht
// zurück (sie gehört zu a__b oder zu a; beide Deutungen sind belegt bzw. mehrdeutig).
func TestF3051_IdMitUnterstrich_GiltAlsBelegt(t *testing.T) {
	ziel := t.TempDir()
	if err := os.WriteFile(filepath.Join(ziel, "a__b__neu.txt"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	belegt, err := zurueckBelegt(ziel, "a__b__alt.txt")
	if err != nil || !belegt {
		t.Fatalf("belegt=%v err=%v", belegt, err)
	}
	belegt, err = zurueckBelegt(ziel, "c__alt.txt")
	if err != nil || belegt {
		t.Fatalf("c: belegt=%v err=%v", belegt, err)
	}
	belegt, err = zurueckBelegt(ziel, teilPraefix+"a__b-xyz")
	if err != nil || belegt {
		t.Fatalf("Teil-Datei: belegt=%v err=%v", belegt, err)
	}
}

// F-3052: Ein Purge-Request bewertet höchstens purgeBeiseiteMaxProAufruf Reste; der
// Start bewertet alle.
func TestF3052_ObergrenzeJePurge_StartOhneGrenze(t *testing.T) {
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
	gefragt := 0
	zaehler := func(string, string) (bool, error) { gefragt++; return false, errors.New("unentscheidbar") }
	s.raeumeBeiseiteAuf(zaehler, false)
	if gefragt != purgeBeiseiteMaxProAufruf {
		t.Fatalf("Purge-Weg fragte %d Reste, erwartet %d", gefragt, purgeBeiseiteMaxProAufruf)
	}
	gefragt = 0
	s.RaeumePurgeResteAuf(zaehler)
	if gefragt != n {
		t.Fatalf("Start fragte %d Reste, erwartet %d", gefragt, n)
	}
}

func zaehleId(t *testing.T, dir, id string) int {
	t.Helper()
	e, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range e {
		if strings.HasPrefix(x.Name(), id+"__") {
			n++
		}
	}
	return n
}
