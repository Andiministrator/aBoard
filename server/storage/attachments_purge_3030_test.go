package storage

// F-3030 (#1149): LoescheKarteBestaetigt schiebt das Kartenverzeichnis beiseite, prüft
// noch einmal und löscht erst dann — oder schiebt zurück.
// F-3038/F-3039 (Sitzungs-Kritik 11.11.): Alte Reste in .purge-beiseite werden nie
// blind gelöscht — das Aufräumen fragt die Kartenlage; das Alter steht im Namen.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func purgeStore(t *testing.T, ids ...string) (*AttachmentStore, string) {
	t.Helper()
	dir := t.TempDir()
	s := NewAttachmentStore(dir)
	for _, id := range ids {
		if err := s.Save("b1", "k1", id, id+".txt", strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	return s, dir
}

func da(s *AttachmentStore, id string) bool {
	f, _, err := s.Open("b1", "k1", id)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func beiseite(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	e, _ := os.ReadDir(filepath.Join(dir, purgeBeiseiteRoot))
	return e
}

func beiseiteLeer(t *testing.T, dir string) {
	t.Helper()
	if e := beiseite(t, dir); len(e) != 0 {
		t.Fatalf("Reste in %s: %d", purgeBeiseiteRoot, len(e))
	}
}

// legeRestAn: Kartenverzeichnis b1/k1 als Rest mit gegebenem Alter in .purge-beiseite
// (Aufbau `<unix>__<zufall>/<board>/<karte>`), Rückgabe: Pfad der Hülle.
func legeRestAn(t *testing.T, s *AttachmentStore, dir string, alter time.Duration) string {
	t.Helper()
	return legeRestFuer(t, s, dir, "b1", "k1", alter)
}

func legeRestFuer(t *testing.T, s *AttachmentStore, dir, board, karte string, alter time.Duration) string {
	t.Helper()
	huelle := filepath.Join(dir, purgeBeiseiteRoot, strconv.FormatInt(time.Now().Add(-alter).Unix(), 10)+"__abcdef012345")
	if err := os.MkdirAll(filepath.Join(huelle, board), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.cardDir(board, karte), filepath.Join(huelle, board, karte)); err != nil {
		t.Fatal(err)
	}
	return huelle
}

func lageFest(verwaist bool, err error) KartenLageFn {
	return func(string, string) (bool, error) { return verwaist, err }
}

func TestF3030_VerwaistBestaetigt_Geloescht(t *testing.T) {
	s, dir := purgeStore(t, "a1", "a2")
	ok, err := s.LoescheKarteBestaetigt("b1", "k1", func() (bool, error) { return true, nil }, nil)
	if err != nil || !ok || da(s, "a1") || da(s, "a2") {
		t.Fatalf("ok=%v err=%v a1=%v", ok, err, da(s, "a1"))
	}
	beiseiteLeer(t, dir)
}

// Der F-3030-Fall: Zwischen erster und zweiter Prüfung holt ein Schreiben die Karte
// zurück — die Dateien müssen bleiben.
func TestF3030_KarteWiederDa_Zurueckgeschoben(t *testing.T) {
	s, dir := purgeStore(t, "a1", "a2")
	gesehen := true
	ok, err := s.LoescheKarteBestaetigt("b1", "k1", func() (bool, error) {
		gesehen = da(s, "a1") // während der Prüfung ist das Verzeichnis beiseite
		return false, nil
	}, nil)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if gesehen {
		t.Fatal("Verzeichnis war während der zweiten Prüfung nicht beiseite")
	}
	if !da(s, "a1") || !da(s, "a2") {
		t.Fatal("Dateien der wiederhergestellten Karte fehlen")
	}
	beiseiteLeer(t, dir)
}

func TestF3030_PruefFehler_Zurueckgeschoben(t *testing.T) {
	s, _ := purgeStore(t, "a1")
	kaputt := errors.New("stand unlesbar")
	ok, err := s.LoescheKarteBestaetigt("b1", "k1", func() (bool, error) { return false, kaputt }, nil)
	if !errors.Is(err, kaputt) || ok || !da(s, "a1") {
		t.Fatalf("ok=%v err=%v a1=%v", ok, err, da(s, "a1"))
	}
}

// Kommt während der Prüfung ein Upload in ein neues Verzeichnis, werden die alten
// Dateien einzeln zurückgeschoben, die neue bleibt.
func TestF3030_UploadZwischendurch_Zusammengefuehrt(t *testing.T) {
	s, dir := purgeStore(t, "a1")
	ok, err := s.LoescheKarteBestaetigt("b1", "k1", func() (bool, error) {
		if err := s.Save("b1", "k1", "neu", "neu.txt", strings.NewReader("y")); err != nil {
			t.Fatal(err)
		}
		return false, nil
	}, nil)
	if err != nil || ok || !da(s, "a1") || !da(s, "neu") {
		t.Fatalf("ok=%v err=%v a1=%v neu=%v", ok, err, da(s, "a1"), da(s, "neu"))
	}
	beiseiteLeer(t, dir)
}

func TestF3030_NichtsDa_Idempotent(t *testing.T) {
	s, _ := purgeStore(t)
	gerufen := false
	ok, err := s.LoescheKarteBestaetigt("b1", "k1", func() (bool, error) { gerufen = true; return true, nil }, nil)
	if err != nil || !ok || gerufen {
		t.Fatalf("ok=%v err=%v gerufen=%v", ok, err, gerufen)
	}
	if _, err := s.LoescheKarteBestaetigt("b1", "__board__", nil, nil); !errors.Is(err, ErrReservierteAblage) {
		t.Fatalf("Board-Ablage: %v", err)
	}
}

// F-3039: Ein laufender Eintrag (Name mit frischer Zeit, Verzeichnis mit alter mtime)
// überlebt das Aufräumen eines gleichzeitigen Purges — auch wenn dessen Lage
// „verwaist“ sagen würde.
func TestF3039_LaufenderEintrag_UeberlebtFremdesAufraeumen(t *testing.T) {
	s, dir := purgeStore(t, "a1")
	alt := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(s.cardDir("b1", "k1"), alt, alt); err != nil {
		t.Fatal(err)
	}
	ok, err := s.LoescheKarteBestaetigt("b1", "k1", func() (bool, error) {
		s.raeumeBeiseiteAuf(lageFest(true, nil), false) // ein anderer Purge räumt gerade auf
		return false, nil
	}, nil)
	if err != nil || ok || !da(s, "a1") {
		t.Fatalf("ok=%v err=%v a1=%v", ok, err, da(s, "a1"))
	}
	beiseiteLeer(t, dir)
}

// F-3038: Ein alter Rest einer LEBENDEN Karte (Absturz während der zweiten Prüfung)
// wird zurückgeschoben, nicht gelöscht.
func TestF3038_RestLebenderKarte_Zurueckgeschoben(t *testing.T) {
	s, dir := purgeStore(t, "a1")
	legeRestAn(t, s, dir, 2*time.Hour)
	if da(s, "a1") {
		t.Fatal("Vorbedingung: Datei sollte beiseite liegen")
	}
	s.raeumeBeiseiteAuf(lageFest(false, nil), false)
	if !da(s, "a1") {
		t.Fatal("Datei der lebenden Karte nicht zurückgeschoben")
	}
	beiseiteLeer(t, dir)
}

// F-3038: Rest einer verwaisten Karte wird gelöscht; bei Fehler der Lage, jungem
// Rest oder ohne Lage bleibt er liegen.
func TestF3038_AufraeumenNurMitBestaetigung(t *testing.T) {
	s, dir := purgeStore(t, "a1")
	rest := legeRestAn(t, s, dir, 2*time.Hour)
	s.raeumeBeiseiteAuf(nil, false)
	s.raeumeBeiseiteAuf(lageFest(false, errors.New("stand unlesbar")), false)
	if _, err := os.Stat(rest); err != nil {
		t.Fatal("Rest bei Fehler/ohne Lage angefasst")
	}
	s.raeumeBeiseiteAuf(lageFest(true, nil), false)
	if _, err := os.Stat(rest); !os.IsNotExist(err) {
		t.Fatal("verwaister Rest blieb liegen")
	}

	s2, dir2 := purgeStore(t, "a1")
	jung := legeRestAn(t, s2, dir2, 10*time.Minute)
	s2.raeumeBeiseiteAuf(lageFest(true, nil), false)
	if _, err := os.Stat(jung); err != nil {
		t.Fatal("junger Rest gelöscht")
	}
	if err := os.WriteFile(filepath.Join(dir2, purgeBeiseiteRoot, "kaputt"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	s2.raeumeBeiseiteAuf(lageFest(true, nil), false)
	if _, err := os.Stat(filepath.Join(dir2, purgeBeiseiteRoot, "kaputt")); err != nil {
		t.Fatal("Eintrag mit unlesbarem Namen gelöscht")
	}
}

// F-3042: Scheitert das Zurückschieben, kommt der Fehler zurück und der Rest bleibt —
// das spätere Aufräumen schiebt ihn bei lebender Karte zurück (F-3038).
func TestF3042_ZurueckschiebenScheitert_RestBleibtUndWirdSpaeterGerettet(t *testing.T) {
	s, dir := purgeStore(t, "a1")
	boardDir := filepath.Join(dir, "b1")
	ok, err := s.LoescheKarteBestaetigt("b1", "k1", func() (bool, error) {
		if err := os.Chmod(boardDir, 0o500); err != nil { // eigenes Testverzeichnis
			t.Fatal(err)
		}
		return false, nil
	}, nil)
	if err := os.Chmod(boardDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err == nil || ok {
		t.Fatalf("ok=%v err=%v, erwartet Fehler beim Zurückschieben", ok, err)
	}
	reste := beiseite(t, dir)
	if len(reste) != 1 {
		t.Fatalf("Rest fehlt: %d", len(reste))
	}
	// Den Rest altern lassen (Name), dann aufräumen mit lebender Karte.
	teile := strings.SplitN(reste[0].Name(), "__", 2)
	altName := strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10) + "__" + teile[1]
	root := filepath.Join(dir, purgeBeiseiteRoot)
	if err := os.Rename(filepath.Join(root, reste[0].Name()), filepath.Join(root, altName)); err != nil {
		t.Fatal(err)
	}
	s.raeumeBeiseiteAuf(lageFest(false, nil), false)
	if !da(s, "a1") {
		t.Fatal("Datei nach dem Aufräumen nicht zurück")
	}
	beiseiteLeer(t, dir)
}

// F-3042: Zwei gleichzeitige Purges derselben Karte — beide melden Erfolg, nichts bleibt liegen.
func TestF3042_ParallelePurgesDerselbenKarte(t *testing.T) {
	s, dir := purgeStore(t, "a1", "a2")
	var wg sync.WaitGroup
	fehler := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.LoescheKarteBestaetigt("b1", "k1", func() (bool, error) { return true, nil }, lageFest(true, nil))
			if err != nil || !ok {
				fehler <- errors.New("ok=false oder Fehler")
			}
		}()
	}
	wg.Wait()
	close(fehler)
	for e := range fehler {
		t.Fatal(e)
	}
	if da(s, "a1") || da(s, "a2") {
		t.Fatal("Dateien noch da")
	}
	beiseiteLeer(t, dir)
}

// F-3043/F-3046: IDs mit `_` (auch `__`) und lange IDs (128 Zeichen, safeIDRe) werden
// richtig zugeordnet — Board und Karte sind Verzeichnisnamen, nichts wird kodiert.
func TestF3043_F3046_IDsMitUnterstrichUndLang(t *testing.T) {
	lang := strings.Repeat("x", 128)
	for _, f := range [][2]string{{"a_", "k"}, {"a", "_k"}, {"a", "k_"}, {"a__b", "k"}, {"b1", "k__1"}, {lang, lang}} {
		dir := t.TempDir()
		s := NewAttachmentStore(dir)
		if err := s.Save(f[0], f[1], "x1", "x.txt", strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
		// Hauptweg mit langen IDs: kein ENAMETOOLONG, zurückgeschoben bei lebender Karte.
		ok, err := s.LoescheKarteBestaetigt(f[0], f[1], func() (bool, error) { return false, nil }, nil)
		if err != nil || ok {
			t.Fatalf("%v: ok=%v err=%v", f[0][:1], ok, err)
		}
		legeRestFuer(t, s, dir, f[0], f[1], 2*time.Hour)
		var gefragt [][2]string
		s.raeumeBeiseiteAuf(func(b, k string) (bool, error) { gefragt = append(gefragt, [2]string{b, k}); return false, nil }, false)
		if len(gefragt) != 1 || gefragt[0] != f {
			t.Fatalf("%v: gefragt %v", f[0][:1], gefragt)
		}
		if g, _, err := s.Open(f[0], f[1], "x1"); err != nil {
			t.Fatalf("%v: nicht zurückgeschoben: %v", f[0][:1], err)
		} else {
			_ = g.Close()
		}
		beiseiteLeer(t, dir)
	}
}

// F-3047/F-3044: Fremde Formate bleiben unangetastet und die Lage wird nicht gefragt —
// Rohnamen früherer Fassungen (auch aus reinen Hex-Zeichen), Hüllen mit ungültiger ID
// oder mehreren Einträgen.
func TestF3047_FremdeFormateBleibenLiegen(t *testing.T) {
	s, dir := purgeStore(t)
	root := filepath.Join(dir, purgeBeiseiteRoot)
	alt := strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10)
	for _, p := range []string{
		alt + "__ab12__cd34__abcdef012345",                         // Rohformat aus bb12e069, Hex-Zeichen
		alt + "__b1__k1__r",                                        // Rohformat
		alt + "__aaaaaaaaaaaa/.x/k1",                               // ungültige Board-ID
		alt + "__bbbbbbbbbbbb/b1/k1", alt + "__bbbbbbbbbbbb/b2/k1", // zwei Boards
		alt + "__cccccccccccc/b1/a.b",                              // ungültige Karten-ID (F-3049)
		alt + "__dddddddddddd/b1/k1", alt + "__dddddddddddd/b1/k2", // zwei Karten (F-3049)
	} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	gerufen := false
	// Inhalt in jede Karten-Hülle, damit „leer“ (F-3048) sie nicht entfernt.
	for _, p := range []string{alt + "__aaaaaaaaaaaa/.x/k1", alt + "__bbbbbbbbbbbb/b1/k1", alt + "__cccccccccccc/b1/a.b", alt + "__dddddddddddd/b1/k1"} {
		if err := os.WriteFile(filepath.Join(root, p, "datei"), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	s.raeumeBeiseiteAuf(func(string, string) (bool, error) { gerufen = true; return true, nil }, false)
	if gerufen || len(beiseite(t, dir)) != 6 {
		t.Fatalf("gerufen=%v, Reste=%d", gerufen, len(beiseite(t, dir)))
	}
}

// F-3048: Leere Hüllen (auch mit leerem Board-Verzeichnis) verschwinden nach Alter,
// junge bleiben; Hüllen mit Inhalt fasst das nie an.
func TestF3048_LeereHuellenWerdenAufgeraeumt(t *testing.T) {
	s, dir := purgeStore(t)
	root := filepath.Join(dir, purgeBeiseiteRoot)
	alt := strconv.FormatInt(time.Now().Add(-2*time.Hour).Unix(), 10)
	jung := strconv.FormatInt(time.Now().Unix(), 10)
	for _, p := range []string{alt + "__aaaaaaaaaaaa", alt + "__bbbbbbbbbbbb/b1", jung + "__cccccccccccc/b1"} {
		if err := os.MkdirAll(filepath.Join(root, p), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	s.raeumeBeiseiteAuf(func(string, string) (bool, error) { return true, nil }, false)
	rest := beiseite(t, dir)
	if len(rest) != 1 || !strings.HasPrefix(rest[0].Name(), jung) {
		t.Fatalf("übrig: %d", len(rest))
	}
}
