package wissen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"
)

// schreibe legt eine Datei samt Ordnern an.
func schreibe(t *testing.T, wurzel, rel, inhalt string) {
	t.Helper()
	p := filepath.Join(wurzel, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(inhalt), 0o644); err != nil {
		t.Fatal(err)
	}
}

func oeffneTest(t *testing.T, dir string) *Quelle {
	t.Helper()
	q, err := Oeffne(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Schliesse() })
	return q
}

func pfade(idx *Index) []string {
	out := make([]string, 0, len(idx.Seiten))
	for _, s := range idx.Seiten {
		out = append(out, s.Pfad)
	}
	return out
}

// Bestand wie bei einem Team-Betrieb: Themenordner mit Leerzeichen und &, Herkunftspräfix,
// dazu alles, was NIE erscheinen darf.
func bestand(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	schreibe(t, d, "Datenschutz & Consent/projekt-b__crawling__cookies.md", "# Cookies und Consent\n\nText über Consent-Banner.\n")
	schreibe(t, d, "aboard/start.md", "Ohne Überschrift\n")
	schreibe(t, d, "README.md", "# Übersicht\n")
	schreibe(t, d, "notiz.txt", "kein markdown")
	schreibe(t, d, ".git/config", "[remote]\nurl = https://token:geheim@forge/x.git\n") // abzug:testwert
	schreibe(t, d, ".git/HEAD.md", "# versteckt\n")
	schreibe(t, d, ".versteckt.md", "# versteckt\n")
	schreibe(t, d, "ordner/.verborgen/x.md", "# versteckt\n")
	// Symlinks — in den Ordner hinein, also für os.Root „erlaubt“, für uns nicht.
	if err := os.Symlink(".git/config", filepath.Join(d, "x.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(d, "gitlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("aboard", filepath.Join(d, "spiegel")); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestIndex_NurRegulaereMarkdownOhnePunktUndSymlink(t *testing.T) {
	q := oeffneTest(t, bestand(t))
	idx, err := q.Index()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(pfade(idx), "|")
	want := "Datenschutz & Consent/projekt-b__crawling__cookies.md|README.md|aboard/start.md"
	if got != want {
		t.Fatalf("Index\n got %s\nwant %s", got, want)
	}
	titel := map[string]string{}
	for _, s := range idx.Seiten {
		titel[s.Pfad] = s.Titel
	}
	if titel["README.md"] != "Übersicht" || titel["aboard/start.md"] != "start" ||
		titel["Datenschutz & Consent/projekt-b__crawling__cookies.md"] != "Cookies und Consent" {
		t.Fatalf("Titel: %v", titel)
	}
}

func TestSeite_GeheimesUndAusbruchNieAbrufbar(t *testing.T) {
	q := oeffneTest(t, bestand(t))
	for _, p := range []string{
		"x.md",             // Symlink auf .git/config
		".git/config",      // Punktordner
		".git/HEAD.md",     // Punktordner, .md
		".versteckt.md",    // Punktdatei
		"gitlink/config",   // durch Ordner-Symlink
		"spiegel/start.md", // Ordner-Symlink innerhalb
		"ordner/.verborgen/x.md",
		"../etc/passwd",
		"/etc/passwd",
		"aboard/../.git/config",
		"notiz.txt",
		"",
		"aboard",
	} {
		b, _, err := q.Seite(p)
		if !errors.Is(err, ErrNichtGefunden) {
			t.Errorf("Seite(%q): err=%v, %d Bytes — erwartet ErrNichtGefunden", p, err, len(b))
		}
	}
	b, meta, err := q.Seite("aboard/start.md")
	if err != nil || string(b) != "Ohne Überschrift\n" || meta.Pfad != "aboard/start.md" {
		t.Fatalf("reguläre Seite: %q %v %v", b, meta, err)
	}
}

func TestSeite_NFDAufPlatteNFCInAnfrage(t *testing.T) {
	d := t.TempDir()
	nfd := norm.NFD.String("Überblick/Größen.md")
	schreibe(t, d, nfd, "# Größen\n")
	q := oeffneTest(t, d)
	idx, err := q.Index()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Seiten) != 1 || idx.Seiten[0].Pfad != norm.NFC.String("Überblick/Größen.md") {
		t.Fatalf("Index-Pfad nicht NFC: %q", pfade(idx))
	}
	for _, anfrage := range []string{"Überblick/Größen.md", nfd} {
		if _, _, err := q.Seite(anfrage); err != nil {
			t.Errorf("Seite(%q): %v", anfrage, err)
		}
	}
}

func TestSeite_NachIndexInSymlinkGetauscht(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "# A\n")
	schreibe(t, d, "ordner/b.md", "# B\n")
	schreibe(t, d, ".git/config", "geheim")
	schreibe(t, d, ".git/b.md", "geheim")
	q := oeffneTest(t, d)
	if _, err := q.Index(); err != nil {
		t.Fatal(err)
	}
	// Datei → Symlink
	if err := os.Remove(filepath.Join(d, "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git/config", filepath.Join(d, "a.md")); err != nil {
		t.Fatal(err)
	}
	if b, _, err := q.Seite("a.md"); err == nil || strings.Contains(string(b), "geheim") {
		t.Fatalf("getauschte Datei ausgeliefert: %q %v", b, err)
	}
	// Ordner → Symlink
	if err := os.RemoveAll(filepath.Join(d, "ordner")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(d, "ordner")); err != nil {
		t.Fatal(err)
	}
	if b, _, err := q.Seite("ordner/b.md"); err == nil || strings.Contains(string(b), "geheim") {
		t.Fatalf("getauschter Ordner ausgeliefert: %q %v", b, err)
	}
}

func TestSeite_ZuGross(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "gross.md", strings.Repeat("x", MaxSeitenBytes+1))
	schreibe(t, d, "genau.md", strings.Repeat("x", MaxSeitenBytes))
	q := oeffneTest(t, d)
	if _, _, err := q.Seite("gross.md"); !errors.Is(err, ErrZuGross) {
		t.Fatalf("gross.md: %v", err)
	}
	if b, _, err := q.Seite("genau.md"); err != nil || len(b) != MaxSeitenBytes {
		t.Fatalf("genau.md: %d %v", len(b), err)
	}
}

func TestIndex_Tiefe(t *testing.T) {
	d := t.TempDir()
	tief := strings.Repeat("o/", MaxTiefe) + "x.md"     // Datei in Ordnertiefe MaxTiefe
	zuTief := strings.Repeat("o/", MaxTiefe+1) + "y.md" // eine Ebene mehr
	schreibe(t, d, tief, "x")
	schreibe(t, d, zuTief, "y")
	q := oeffneTest(t, d)
	idx, err := q.Index()
	if err != nil {
		t.Fatal(err)
	}
	if got := pfade(idx); len(got) != 1 || got[0] != tief {
		t.Fatalf("Tiefe: %v", got)
	}
}

func TestIndex_StandSteuertNeuaufbau(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "a")
	schreibe(t, d, StandDatei, `{"commit":"c0db7e3","datum":"2026-10-08T10:00:00Z","fremd":"x"}`)
	q := oeffneTest(t, d)
	idx, err := q.Index()
	if err != nil {
		t.Fatal(err)
	}
	if idx.Stand == nil || idx.Stand.Commit != "c0db7e3" || idx.Stand.Datum != "2026-10-08T10:00:00Z" {
		t.Fatalf("Stand: %+v", idx.Stand)
	}
	schreibe(t, d, "b.md", "b")
	if idx, _ = q.Index(); len(idx.Seiten) != 1 {
		t.Fatalf("ohne Stand-Änderung neu gebaut: %v", pfade(idx))
	}
	schreibe(t, d, StandDatei, `{"commit":"d1e2f30"}`)
	if idx, _ = q.Index(); len(idx.Seiten) != 2 || idx.Stand.Commit != "d1e2f30" {
		t.Fatalf("nach Stand-Änderung: %v %+v", pfade(idx), idx.Stand)
	}
}

func TestIndex_OhneStandNachFrist(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "a")
	q := oeffneTest(t, d)
	jetzt := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	q.jetzt = func() time.Time { return jetzt }
	if _, err := q.Index(); err != nil {
		t.Fatal(err)
	}
	schreibe(t, d, "b.md", "b")
	jetzt = jetzt.Add(OhneStandGueltig - time.Second)
	if idx, _ := q.Index(); len(idx.Seiten) != 1 {
		t.Fatalf("vor Ablauf neu gebaut: %v", pfade(idx))
	}
	jetzt = jetzt.Add(2 * time.Second)
	if idx, _ := q.Index(); len(idx.Seiten) != 2 {
		t.Fatalf("nach Ablauf nicht neu gebaut: %v", pfade(idx))
	}
}

func TestStand_SymlinkUndUnsinnIgnoriert(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "a")
	schreibe(t, d, ".git/stand.json", `{"commit":"geheim"}`)
	if err := os.Symlink(".git/stand.json", filepath.Join(d, StandDatei)); err != nil {
		t.Fatal(err)
	}
	idx, err := oeffneTest(t, d).Index()
	if err != nil || idx.Stand != nil {
		t.Fatalf("Symlink-Stand gelesen: %+v %v", idx.Stand, err)
	}
	if s := parseStand([]byte(`{"commit":"a\u0007b"}`)); s != nil {
		t.Fatalf("Steuerzeichen im Stand übernommen: %+v", s)
	}
	if s := parseStand([]byte(`{"commit":"` + strings.Repeat("a", 65) + `"}`)); s != nil {
		t.Fatalf("überlanger Commit übernommen: %+v", s)
	}
}

func TestSuche(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "# Anfang\n\nzweite Zeile\nHier steht CONSENT im Text.\n")
	schreibe(t, d, "b.md", norm.NFD.String("# Größe\n\nDie Größe zählt.\n"))
	schreibe(t, d, "consent/c.md", "nichts\n")
	schreibe(t, d, "regex.md", "Muster a.*b steht hier wörtlich.\n")
	schreibe(t, d, ".git/geheim.md", "consent geheim\n")
	q := oeffneTest(t, d)

	erg, err := q.Suche(context.Background(), "Consent")
	if err != nil {
		t.Fatal(err)
	}
	if len(erg.Treffer) != 2 || erg.Treffer[0].Pfad != "a.md" || erg.Treffer[0].Zeile != 4 ||
		erg.Treffer[0].Schnipsel != "Hier steht CONSENT im Text." || erg.Treffer[1].Pfad != "consent/c.md" || erg.Treffer[1].Zeile != 0 {
		t.Fatalf("Consent: %+v", erg.Treffer)
	}
	if erg, _ = q.Suche(context.Background(), "größe"); len(erg.Treffer) != 1 || erg.Treffer[0].Pfad != "b.md" {
		t.Fatalf("NFD-Inhalt mit NFC-Anfrage: %+v", erg.Treffer)
	}
	if erg, _ = q.Suche(context.Background(), "a.*b"); len(erg.Treffer) != 1 || erg.Treffer[0].Pfad != "regex.md" {
		t.Fatalf("Regex-Zeichen nicht wörtlich: %+v", erg.Treffer)
	}
	for _, schlecht := range []string{"", " a ", strings.Repeat("x", MaxAnfrage+1), "\xff\xfe"} {
		if _, err := q.Suche(context.Background(), schlecht); !errors.Is(err, ErrAnfrage) {
			t.Errorf("Suche(%q): %v", schlecht, err)
		}
	}
	if _, err := q.Suche(context.Background(), strings.Repeat("ä", MaxAnfrage)); err != nil {
		t.Errorf("Grenze in Zeichen, nicht Bytes: %v", err)
	}
}

func TestSuche_TrefferGrenze(t *testing.T) {
	d := t.TempDir()
	for i := 0; i < MaxTreffer+5; i++ {
		schreibe(t, d, fmt.Sprintf("s%03d.md", i), "wort\n")
	}
	erg, err := oeffneTest(t, d).Suche(context.Background(), "wort")
	if err != nil {
		t.Fatal(err)
	}
	if len(erg.Treffer) != MaxTreffer || !erg.Gekappt {
		t.Fatalf("%d Treffer, gekappt=%v", len(erg.Treffer), erg.Gekappt)
	}
}

func TestSchnipsel_FensterAnZeichengrenzen(t *testing.T) {
	zeile := strings.Repeat("ä", 200) + "ZIEL" + strings.Repeat("ö", 200)
	_, s := fundstelle(zeile, "ziel")
	if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || !strings.Contains(s, "ZIEL") {
		t.Fatalf("Schnipsel: %q", s)
	}
	if n := len([]rune(s)); n > schnipselRunen+2 {
		t.Fatalf("Schnipsel zu lang: %d Zeichen", n)
	}
}

func TestPruefeDir(t *testing.T) {
	ok := map[string]string{
		"/data/wissen":         "/data/wissen",
		"/data/wissen/":        "/data/wissen",
		"/data/a//b":           "/data/a/b",
		" /data/team wissen/ ": "/data/team wissen",
	}
	for in, want := range ok {
		if got, err := PruefeDir(in, ErlaubterPraefix); err != nil || got != want {
			t.Errorf("PruefeDir(%q) = %q, %v — erwartet %q", in, got, err, want)
		}
	}
	for _, schlecht := range []string{"", "/", "/data", "/data/", "/data/.", "/datax/w", "/data/../etc",
		"/data/w/../../etc", "data/wissen", "/etc/wissen"} {
		if got, err := PruefeDir(schlecht, ErlaubterPraefix); err == nil {
			t.Errorf("PruefeDir(%q) = %q ohne Fehler", schlecht, got)
		}
	}
}

func TestPruefeBearbeitenURL(t *testing.T) {
	if got, err := PruefeBearbeitenURL(" https://forge.example/Team/team-wissen/_edit/main/{pfad} "); err != nil ||
		got != "https://forge.example/Team/team-wissen/_edit/main/{pfad}" {
		t.Fatalf("gültig: %q %v", got, err)
	}
	for _, schlecht := range []string{
		"http://forge.example/{pfad}",
		"https://forge.example/edit",
		"https://forge.example/{pfad}/{pfad}",
		"https://user:pw@forge.example/{pfad}",
		"https://forge.example/{pfad}#x",
		"javascript:alert(1)//{pfad}",
		"https:///{pfad}",
		"https://forge.example/a b/{pfad}",
	} {
		if _, err := PruefeBearbeitenURL(schlecht); err == nil {
			t.Errorf("PruefeBearbeitenURL(%q) ohne Fehler", schlecht)
		}
	}
}

func TestBearbeitenLink_KodiertJedenTeil(t *testing.T) {
	got := BearbeitenLink("https://f.example/edit?datei={pfad}&ref=main", "Datenschutz & Consent/a+b=c;d #?.md")
	want := "https://f.example/edit?datei=Datenschutz%20%26%20Consent/a%2Bb%3Dc%3Bd%20%23%3F.md&ref=main"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
	if BearbeitenLink("", "a.md") != "" {
		t.Fatal("leere Vorlage ergibt Link")
	}
}

func TestPruefeWert(t *testing.T) {
	if _, err := PruefeWert("wissen.unbekannt", "x", ErlaubterPraefix); err == nil {
		t.Error("unbekannter Schlüssel angenommen")
	}
	if _, err := PruefeWert(KeyEnabled, "ja", ErlaubterPraefix); err == nil {
		t.Error("enabled=ja angenommen")
	}
	if v, err := PruefeWert(KeyDir, "", ErlaubterPraefix); err != nil || v != "" {
		t.Error("leeres dir muss speicherbar sein (Feld leeren)")
	}
	if _, err := PruefeWert(KeySichtbar, "jeder", ErlaubterPraefix); err == nil {
		t.Error("sichtbar=jeder angenommen")
	}
	if _, err := PruefeWert(KeyName, "a\u0000b", ErlaubterPraefix); err == nil {
		t.Error("Steuerzeichen im Namen angenommen")
	}
}

// Die tragende Prüfung gegen einen eingetauschten Link ist der Vergleich der
// geöffneten Datei mit der Lstat-Angabe (leseRegulaer). Die Lstat-Prüfung in
// leseSeite ist ein Vorfilter davor; dieser Test trennt beide.
func TestLeseRegulaer_AndereDateiAlsGeprueft(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "a.md", "a")
	schreibe(t, d, "b.md", "b")
	q := oeffneTest(t, d)
	fremd, err := os.Lstat(filepath.Join(d, "b.md"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.leseRegulaer("a.md", fremd, 10); !errors.Is(err, ErrVeraendert) {
		t.Fatalf("andere Datei gelesen: %v", err)
	}
}

// Kritiker #739 K-2: {pfad} im Host liesse einen Ordnernamen das Ziel wählen.
func TestPruefeBearbeitenURL_PfadNichtImHost(t *testing.T) {
	for _, schlecht := range []string{
		"https://{pfad}.forge.example/",
		"https://forge.example{pfad}",
		"https://forge.{pfad}/x",
		"https://{pfad}@forge.example/x",
	} {
		if _, err := PruefeBearbeitenURL(schlecht); err == nil {
			t.Errorf("PruefeBearbeitenURL(%q) ohne Fehler", schlecht)
		}
	}
	for _, gut := range []string{"https://forge.example/{pfad}", "https://forge.example/edit?datei={pfad}"} {
		if _, err := PruefeBearbeitenURL(gut); err != nil {
			t.Errorf("PruefeBearbeitenURL(%q): %v", gut, err)
		}
	}
}

// Kritiker #739 K-3: ein ausgetauschter Ordner wird erkannt.
func TestQuelle_AktuellNachAustausch(t *testing.T) {
	basis := t.TempDir()
	d := filepath.Join(basis, "w")
	schreibe(t, d, "a.md", "alt")
	q := oeffneTest(t, d)
	if !q.Aktuell() {
		t.Fatal("frisch geöffnet, aber nicht aktuell")
	}
	if err := os.Rename(d, filepath.Join(basis, "w-alt")); err != nil {
		t.Fatal(err)
	}
	schreibe(t, d, "a.md", "neu")
	if q.Aktuell() {
		t.Fatal("Austausch nicht erkannt")
	}
}

func TestIndex_GekapptBeiDateiGrenze(t *testing.T) {
	d := t.TempDir()
	for i := 0; i < MaxDateien+1; i++ {
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("s%05d.md", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := oeffneTest(t, d).Index()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Seiten) != MaxDateien || !idx.Gekappt {
		t.Fatalf("%d Seiten, gekappt=%v", len(idx.Seiten), idx.Gekappt)
	}
}

func TestSuche_UeberspringtZuGrosseSeite(t *testing.T) {
	d := t.TempDir()
	schreibe(t, d, "gross.md", strings.Repeat("x", MaxSeitenBytes)+"wort")
	schreibe(t, d, "klein.md", "wort")
	erg, err := oeffneTest(t, d).Suche(context.Background(), "wort")
	if err != nil {
		t.Fatal(err)
	}
	if len(erg.Treffer) != 1 || erg.Treffer[0].Pfad != "klein.md" {
		t.Fatalf("%+v", erg.Treffer)
	}
}

// Verify #739 V-5: die Grenze über ALLE Einträge (nicht nur .md) greift.
func TestIndex_GekapptBeiEintragsGrenze(t *testing.T) {
	alt := eintraegeGrenze
	eintraegeGrenze = 10
	t.Cleanup(func() { eintraegeGrenze = alt })
	d := t.TempDir()
	for i := 0; i < 20; i++ {
		schreibe(t, d, fmt.Sprintf("bild%02d.png", i), "x")
	}
	schreibe(t, d, "zz.md", "x")
	idx, err := oeffneTest(t, d).Index()
	if err != nil {
		t.Fatal(err)
	}
	if !idx.Gekappt || len(idx.Seiten) != 0 {
		t.Fatalf("gekappt=%v seiten=%v", idx.Gekappt, pfade(idx))
	}
}

func TestPruefeSichtbar_Gruppe(t *testing.T) {
	for ein, soll := range map[string]string{"gruppe:wissen": "gruppe:wissen", " gruppe: Team A ": "gruppe:Team A"} {
		if ist, err := PruefeSichtbar(ein); err != nil || ist != soll {
			t.Errorf("PruefeSichtbar(%q) = %q, %v; erwartet %q", ein, ist, err, soll)
		}
	}
	for _, ein := range []string{"gruppe:", "gruppe:  ", "gruppe:a,b", "gruppe:a\nb", "gruppen:x", "Gruppe:x"} {
		if _, err := PruefeSichtbar(ein); err == nil {
			t.Errorf("PruefeSichtbar(%q) muss scheitern", ein)
		}
	}
	if g := (Config{Sichtbar: "gruppe:wissen"}).Gruppe(); g != "wissen" {
		t.Errorf("Gruppe() = %q", g)
	}
	if g := (Config{Sichtbar: SichtbarAdmins}).Gruppe(); g != "" {
		t.Errorf("Gruppe() bei admins = %q", g)
	}
}

// F-1887 (Sprint-86-Kritiker K-B1): wissen.dir darf keinen Server-Datenordner
// treffen — weder gleich noch darin noch darüber.
func TestPruefeDirServerVerzeichnisse(t *testing.T) {
	gesperrt := []string{"/data/db", "/data/attachments", "", "/data/backups/"}
	for _, f := range []struct {
		dir string
		ok  bool
	}{
		{"/data/attachments", false},
		{"/data/attachments/b1", false},
		{"/data/backups", false},
		{"/data/db/", false},
		{"/data/wissen", true},
		{"/data/attachments-alt", true}, // nur Namenspräfix, kein Unterordner
		{"/data/wissen/db", true},
	} {
		err := PruefeKeinServerOrdner(f.dir, gesperrt)
		if (err == nil) != f.ok {
			t.Errorf("%s: %v (erwartet ok=%v)", f.dir, err, f.ok)
		}
	}
	// Ein Wissensordner, der einen Server-Ordner enthält, ist ebenso gesperrt.
	if PruefeKeinServerOrdner("/srv/x", []string{"/srv/x/attachments"}) == nil {
		t.Error("Elternordner eines Server-Ordners erlaubt")
	}
}

// Verify V-1 zu F-1887: Ein relativer Server-Ordner und ein Symlink auf den
// Anhang-Ordner umgehen die Sperre nicht.
func TestPruefeDirServerVerzeichnisseAufgeloest(t *testing.T) {
	basis := t.TempDir()
	anhaenge := filepath.Join(basis, "attachments")
	if err := os.MkdirAll(anhaenge, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(basis, "wissen")
	if err := os.Symlink(anhaenge, link); err != nil {
		t.Fatal(err)
	}
	if PruefeKeinServerOrdner(link, []string{anhaenge}) == nil {
		t.Error("Symlink auf den Anhang-Ordner erlaubt")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, anhaenge)
	if err != nil {
		t.Skip("kein relativer Pfad bildbar")
	}
	if PruefeKeinServerOrdner(anhaenge, []string{rel}) == nil {
		t.Errorf("relativer Server-Ordner %q nicht erkannt", rel)
	}
	if err := PruefeKeinServerOrdner(filepath.Join(basis, "eigen"), []string{rel}); err != nil {
		t.Errorf("eigener Ordner abgelehnt: %v", err)
	}
}
