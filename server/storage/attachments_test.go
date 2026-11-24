package storage

// attachments_test.go — Tests für die zweiphasige Anhang-Übernahme beim
// Cross-Board-Transfer (#296 Etappe 1b, gehärtet durch F-390).

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/uhr"
)

// seedCardAttachments legt ein Karten-Verzeichnis mit den gegebenen Dateien an.
func seedCardAttachments(t *testing.T, baseDir, boardId, cardId string, names ...string) string {
	t.Helper()
	dir := filepath.Join(baseDir, boardId, cardId)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("inhalt-"+name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestAttachments_StageCommit_HappyPath(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	srcDir := seedCardAttachments(t, baseDir, "src-board", "card-uuid",
		"att1__rechnung.pdf", "att2__bild.png")

	staged, err := store.StageCardAttachments("src-board", "dst-board", "card-uuid")
	if err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	if staged == nil {
		t.Fatal("Handle ist nil, obwohl die Karte Anhänge hat")
	}
	if err := staged.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	dstDir := filepath.Join(baseDir, "dst-board", "card-uuid")
	for _, name := range []string{"att1__rechnung.pdf", "att2__bild.png"} {
		got, err := os.ReadFile(filepath.Join(dstDir, name))
		if err != nil {
			t.Errorf("dst-Datei fehlt: %s — %v", name, err)
			continue
		}
		if string(got) != "inhalt-"+name {
			t.Errorf("dst-Inhalt geändert für %s: %q", name, string(got))
		}
	}

	// src bleibt zunächst stehen — Saga lässt den Caller cleanup'en
	if _, err := os.Stat(filepath.Join(srcDir, "att1__rechnung.pdf")); err != nil {
		t.Errorf("src-Datei nach Commit verschwunden (sollte nicht): %v", err)
	}
	// Staging-Wurzel ist leer — kein Rest.
	if entries, err := os.ReadDir(filepath.Join(baseDir, transferStagingRoot)); err == nil && len(entries) != 0 {
		t.Errorf("Staging-Wurzel nach Commit nicht leer: %d Einträge", len(entries))
	}
}

func TestAttachments_Stage_NoSrcDir_NoOp(t *testing.T) {
	store := NewAttachmentStore(t.TempDir())
	staged, err := store.StageCardAttachments("kein-board", "dst", "kein-card")
	if err != nil {
		t.Errorf("erwartet nil-Fehler bei fehlendem src, erhalten %v", err)
	}
	if staged != nil {
		t.Errorf("erwartet nil-Handle bei fehlendem src, erhalten %+v", staged)
	}
	// nil-Handle muss bedienbar bleiben — der Aufrufer soll nicht prüfen müssen.
	if err := staged.Commit(); err != nil {
		t.Errorf("Commit auf nil-Handle: %v", err)
	}
	staged.Abort()
}

func TestAttachments_Stage_DstCollision(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	// Beide Pfade existieren — echte UUID-Kollision (keine Absturz-Waise).
	seedCardAttachments(t, baseDir, "src", "c", "a__x.txt")
	seedCardAttachments(t, baseDir, "dst", "c")

	_, err := store.StageCardAttachments("src", "dst", "c")
	if err == nil {
		t.Fatal("erwartet Fehler bei dst-Kollision, erhalten nil")
	}
	if !strings.Contains(err.Error(), "existiert bereits") {
		t.Errorf("Fehler nennt nicht die Kollision: %v", err)
	}
}

// ── F-390: Absturz zwischen Saga §1 und §2 ───────────────────────────

// TestAttachments_Stage_AbsturzVorCommit_KeineWaiseAmZielpfad ist der Kern von
// F-390. Vor dem Fix kopierte Saga §1 direkt an den Ziel-Pfad; ein Absturz vor
// der DB-Tx liess dort ein Verzeichnis zurück, an dem JEDER folgende Versuch
// mit „existiert bereits" (→ HTTP 500) hängenblieb. Der Nutzer sass fest, bis
// jemand von Hand `rm -r` machte.
//
// Beide Zusagen werden hier geprüft:
//  1. Nach §1 existiert am Ziel-Pfad noch nichts.
//  2. Ein zweiter Anlauf nach dem „Absturz" läuft durch.
func TestAttachments_Stage_AbsturzVorCommit_KeineWaiseAmZielpfad(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src", "card-uuid", "att1__x.pdf")
	dstDir := filepath.Join(baseDir, "dst", "card-uuid")

	// Saga §1 — danach „stürzt der Server ab": kein Commit, kein Abort.
	if _, err := store.StageCardAttachments("src", "dst", "card-uuid"); err != nil {
		t.Fatalf("StageCardAttachments (1. Anlauf): %v", err)
	}
	if _, err := os.Stat(dstDir); !os.IsNotExist(err) {
		t.Fatalf("Ziel-Pfad %s existiert schon nach Saga §1 — genau die Waise aus F-390 (err=%v)",
			dstDir, err)
	}

	// Neustart: der nächste Transfer darf nicht an der Leiche hängenbleiben.
	staged2, err := store.StageCardAttachments("src", "dst", "card-uuid")
	if err != nil {
		t.Fatalf("2. Anlauf nach Absturz blockiert: %v", err)
	}
	if err := staged2.Commit(); err != nil {
		t.Fatalf("Commit (2. Anlauf): %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dstDir, "att1__x.pdf")); err != nil {
		t.Errorf("dst-Datei nach 2. Anlauf fehlt: %v", err)
	} else if string(got) != "inhalt-att1__x.pdf" {
		t.Errorf("dst-Inhalt: %q", string(got))
	}
}

func TestAttachments_Abort_LaesstNichtsZurueck(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src", "c", "a__x.txt")

	staged, err := store.StageCardAttachments("src", "dst", "c")
	if err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	staged.Abort()
	staged.Abort() // idempotent

	if entries, err := os.ReadDir(filepath.Join(baseDir, transferStagingRoot)); err == nil && len(entries) != 0 {
		t.Errorf("Staging-Wurzel nach Abort nicht leer: %d Einträge", len(entries))
	}
	if _, err := os.Stat(filepath.Join(baseDir, "dst", "c")); !os.IsNotExist(err) {
		t.Errorf("Ziel-Pfad nach Abort vorhanden: %v", err)
	}
	// Nach Abort darf Commit nichts mehr anlegen.
	if err := staged.Commit(); err != nil {
		t.Errorf("Commit nach Abort: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "dst", "c")); !os.IsNotExist(err) {
		t.Errorf("Commit nach Abort hat den Ziel-Pfad angelegt")
	}
}

// TestAttachments_Stage_RaeumtAlteStagingResteAuf: die zweite Hälfte von F-390.
// Ohne diesen Durchlauf wäre die Sackgasse zwar weg, die Reste abgestürzter
// Transfers würden sich aber unbegrenzt ansammeln.
func TestAttachments_Stage_RaeumtAlteStagingResteAuf(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	seedCardAttachments(t, baseDir, "src", "c", "a__x.txt")

	stagingRoot := filepath.Join(baseDir, transferStagingRoot)
	alt := filepath.Join(stagingRoot, "absturz-von-vorgestern")
	if err := os.MkdirAll(alt, 0o755); err != nil {
		t.Fatalf("mkdir alt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(alt, "a__rest.pdf"), []byte("rest"), 0o644); err != nil {
		t.Fatalf("write alt: %v", err)
	}
	frisch := filepath.Join(stagingRoot, "laeuft-gerade")
	if err := os.MkdirAll(frisch, 0o755); err != nil {
		t.Fatalf("mkdir frisch: %v", err)
	}
	vorgestern := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(alt, vorgestern, vorgestern); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, err := store.StageCardAttachments("src", "dst", "c"); err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}

	if _, err := os.Stat(alt); !os.IsNotExist(err) {
		t.Errorf("alter Staging-Rest nicht aufgeräumt: %v", err)
	}
	if _, err := os.Stat(frisch); err != nil {
		t.Errorf("frisches Staging-Verzeichnis wurde mitgenommen: %v", err)
	}
}

// ── F-200 / Karte #376: DeleteBoard + Pfadkomponenten-Validierung ─────

func TestAttachments_DeleteBoard_EntferntAllesDesBoards(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)

	for _, card := range []string{"card-1", "card-2"} {
		dir := filepath.Join(baseDir, "board-weg", card)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "a__f.png"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// Nachbar-Board im selben Volume — darf nicht mitgenommen werden.
	nachbar := filepath.Join(baseDir, "board-bleibt", "card-9")
	if err := os.MkdirAll(nachbar, 0o755); err != nil {
		t.Fatalf("mkdir nachbar: %v", err)
	}

	if err := store.DeleteBoard("board-weg"); err != nil {
		t.Fatalf("DeleteBoard: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "board-weg")); !os.IsNotExist(err) {
		t.Errorf("Board-Verzeichnis existiert noch: %v", err)
	}
	if _, err := os.Stat(nachbar); err != nil {
		t.Errorf("Nachbar-Board wurde mitgenommen: %v", err)
	}
}

func TestAttachments_DeleteBoard_Idempotent(t *testing.T) {
	store := NewAttachmentStore(t.TempDir())
	if err := store.DeleteBoard("gibt-es-nicht"); err != nil {
		t.Fatalf("erster Lauf: %v", err)
	}
	if err := store.DeleteBoard("gibt-es-nicht"); err != nil {
		t.Fatalf("zweiter Lauf: %v", err)
	}
}

// TestAttachments_DeleteBoard_LehntUnsicherePfadeAb ist der eigentliche
// Wächter: DeleteBoard löscht rekursiv. Käme hier ".." durch, träfe das
// RemoveAll das Elternverzeichnis des gesamten Anhang-Volumes.
func TestAttachments_DeleteBoard_LehntUnsicherePfadeAb(t *testing.T) {
	baseDir := t.TempDir()
	// Ein Geschwister-Verzeichnis NEBEN baseDir — das wäre bei ".." das Opfer.
	opfer := filepath.Join(baseDir, "wichtig")
	if err := os.MkdirAll(opfer, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := NewAttachmentStore(baseDir)

	for _, id := range []string{"", ".", "..", "../..", "a/b", `a\b`, ".versteckt", "mit\x00null"} {
		if err := store.DeleteBoard(id); !errors.Is(err, ErrUnsafePathComponent) {
			t.Errorf("DeleteBoard(%q) = %v, erwartet ErrUnsafePathComponent", id, err)
		}
	}
	if _, err := os.Stat(opfer); err != nil {
		t.Errorf("baseDir-Inhalt wurde beschädigt: %v", err)
	}
	if _, err := os.Stat(baseDir); err != nil {
		t.Errorf("baseDir selbst wurde entfernt: %v", err)
	}
}

func TestAttachments_UnsicherePfade_AlleMethoden(t *testing.T) {
	store := NewAttachmentStore(t.TempDir())
	// Die Zusage „boardId, cardId und attId müssen valide Pfadkomponenten
	// sein" stand seit jeher im Kommentar von Save — geprüft wurde sie nie.
	if err := store.Save("..", "c", "a", "f.png", strings.NewReader("x")); !errors.Is(err, ErrUnsafePathComponent) {
		t.Errorf("Save: %v", err)
	}
	if _, _, err := store.Open("b", "..", "a"); !errors.Is(err, ErrUnsafePathComponent) {
		t.Errorf("Open: %v", err)
	}
	if err := store.Delete("b", "c", ".."); !errors.Is(err, ErrUnsafePathComponent) {
		t.Errorf("Delete: %v", err)
	}
	if err := store.DeleteCard("..", "c"); !errors.Is(err, ErrUnsafePathComponent) {
		t.Errorf("DeleteCard: %v", err)
	}
	if _, err := store.StageCardAttachments("b", "..", "c"); !errors.Is(err, ErrUnsafePathComponent) {
		t.Errorf("StageCardAttachments: %v", err)
	}
}

func TestAttachments_ValidePfade_WeiterhinAkzeptiert(t *testing.T) {
	// Gegenprobe zur Validierung: die real vorkommenden IDs (UUIDs, "default")
	// dürfen NICHT abgelehnt werden — sonst wäre der Fix eine Regression.
	store := NewAttachmentStore(t.TempDir())
	for _, id := range []string{"default", "b1", "550e8400-e29b-41d4-a716-446655440000"} {
		if err := store.Save(id, "card-"+id, "att-1", "f.png", strings.NewReader("x")); err != nil {
			t.Errorf("Save(%q) = %v, erwartet nil", id, err)
		}
		if err := store.DeleteBoard(id); err != nil {
			t.Errorf("DeleteBoard(%q) = %v, erwartet nil", id, err)
		}
	}
}

// TestAttachments_FilenameNeutralisiert deckt den letzten unvalidierten
// Parameter ab (Sprint-52-Kritiker, S4). Anders als bei den IDs wird nicht
// abgelehnt, sondern neutralisiert — ein Dateiname ist Anzeigetext.
func TestAttachments_FilenameNeutralisiert(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)

	faelle := []struct{ eingabe, erwartetImNamen string }{
		{"../../etc/passwd", "....etcpasswd"},
		{`..\..\windows\system32`, "....windowssystem32"},
		{"harmlos.png", "harmlos.png"},
		{"/", "datei"},
		{"..", "datei"},
	}
	for i, f := range faelle {
		// Je Fall eine eigene id: seit F-2081 ist eine belegte id ein Fehler.
		attId := fmt.Sprintf("att-x%d", i)
		if err := store.Save("b1", "c1", attId, f.eingabe, strings.NewReader("x")); err != nil {
			t.Fatalf("Save(%q): %v", f.eingabe, err)
		}
		// Die Datei muss IM Kartenverzeichnis liegen, nirgends sonst.
		eintraege, err := os.ReadDir(filepath.Join(baseDir, "b1", "c1"))
		if err != nil {
			t.Fatalf("readdir: %v", err)
		}
		gefunden := false
		for _, e := range eintraege {
			if e.Name() == attId+"__"+f.erwartetImNamen {
				gefunden = true
			}
			if e.IsDir() {
				t.Errorf("Save(%q) hat ein Unterverzeichnis erzeugt: %s", f.eingabe, e.Name())
			}
		}
		if !gefunden {
			var namen []string
			for _, e := range eintraege {
				namen = append(namen, e.Name())
			}
			t.Errorf("Save(%q): erwartet att-x__%s, gefunden %v", f.eingabe, f.erwartetImNamen, namen)
		}
		_ = os.RemoveAll(filepath.Join(baseDir, "b1", "c1"))
	}
	// Nichts darf ausserhalb von baseDir/b1 gelandet sein.
	if _, err := os.Stat(filepath.Join(baseDir, "etc")); !os.IsNotExist(err) {
		t.Errorf("Pfad-Escape: %s existiert", filepath.Join(baseDir, "etc"))
	}
}

// fehlerLeser liefert erst Daten, dann einen Fehler — ein abgebrochener Upload.
type fehlerLeser struct{ gelesen bool }

func (f *fehlerLeser) Read(p []byte) (int, error) {
	if !f.gelesen {
		f.gelesen = true
		return copy(p, "halb"), nil
	}
	return 0, errors.New("verbindung abgebrochen")
}

// TestAttachments_Save_BelegteIdAbgelehnt deckt F-2081 in allen drei Fällen:
// gleicher Name (stiller Austausch), anderer Name (zweite Datei als Leiche)
// und abgebrochene zweite Übertragung (löschte vorher das Original).
func TestAttachments_Save_BelegteIdAbgelehnt(t *testing.T) {
	faelle := []struct {
		name      string
		dateiname string
		leser     func() io.Reader
	}{
		{"gleicher Name", "a.txt", func() io.Reader { return strings.NewReader("ANDERS") }},
		{"anderer Name", "b.txt", func() io.Reader { return strings.NewReader("ANDERS") }},
		{"Abbruch beim zweiten Upload", "a.txt", func() io.Reader { return &fehlerLeser{} }},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			baseDir := t.TempDir()
			store := NewAttachmentStore(baseDir)
			if err := store.Save("b1", "c1", "att-1", "a.txt", strings.NewReader("ORIGINAL")); err != nil {
				t.Fatalf("erster Save: %v", err)
			}
			err := store.Save("b1", "c1", "att-1", f.dateiname, f.leser())
			if !errors.Is(err, ErrAttachmentExists) {
				t.Fatalf("zweiter Save = %v, erwartet ErrAttachmentExists", err)
			}
			eintraege, err := os.ReadDir(filepath.Join(baseDir, "b1", "c1"))
			if err != nil {
				t.Fatalf("readdir: %v", err)
			}
			if len(eintraege) != 1 || eintraege[0].Name() != "att-1__a.txt" {
				t.Fatalf("Verzeichnis nach zweitem Save: %v, erwartet nur att-1__a.txt", eintraege)
			}
			rc, name, err := store.Open("b1", "c1", "att-1")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer rc.Close()
			inhalt, _ := io.ReadAll(rc)
			if name != "a.txt" || string(inhalt) != "ORIGINAL" {
				t.Errorf("Open = %q/%q, erwartet a.txt/ORIGINAL", name, inhalt)
			}
		})
	}
}

// TestAttachments_Save_AbbruchRaeumtEigeneDateiAuf: Bricht ein ERSTER Upload
// ab, bleibt nichts liegen, und die id ist danach wieder frei.
func TestAttachments_Save_AbbruchRaeumtEigeneDateiAuf(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	if err := store.Save("b1", "c1", "att-1", "a.txt", &fehlerLeser{}); err == nil {
		t.Fatal("Save mit abbrechendem Leser lieferte nil")
	}
	if err := store.Save("b1", "c1", "att-1", "a.txt", strings.NewReader("neu")); err != nil {
		t.Fatalf("Save nach Abbruch: %v", err)
	}
}

// TestAttachments_Save_GleichzeitigEineDatei (F-2081, Kritik K-1): Viele
// gleichzeitige Uploads mit derselben id und verschiedenen Namen — genau einer
// gelingt, genau eine Datei liegt danach im Verzeichnis.
func TestAttachments_Save_GleichzeitigEineDatei(t *testing.T) {
	for runde := 0; runde < 20; runde++ {
		baseDir := t.TempDir()
		store := NewAttachmentStore(baseDir)
		if err := os.MkdirAll(filepath.Join(baseDir, "b1", "c1"), 0o755); err != nil {
			t.Fatal(err)
		}
		const n = 16
		start := make(chan struct{})
		ergebnis := make(chan error, n)
		for i := 0; i < n; i++ {
			go func(i int) {
				<-start
				ergebnis <- store.Save("b1", "c1", "att-1", fmt.Sprintf("d%d.txt", i), strings.NewReader("x"))
			}(i)
		}
		close(start)
		ok := 0
		for i := 0; i < n; i++ {
			if err := <-ergebnis; err == nil {
				ok++
			} else if !errors.Is(err, ErrAttachmentExists) {
				t.Fatalf("unerwarteter Fehler: %v", err)
			}
		}
		eintraege, _ := os.ReadDir(filepath.Join(baseDir, "b1", "c1"))
		if ok != 1 || len(eintraege) != 1 {
			t.Fatalf("Runde %d: %d Erfolge, %d Dateien — erwartet je 1", runde, ok, len(eintraege))
		}
	}
}

// abbruchLeser liefert ein paar Bytes und beendet dann die Goroutine mit
// runtime.Goexit — wie ein Prozess, der mitten im Kopieren abbricht: Deferred
// Aufräumer laufen, der Fehlerzweig von Save nicht.
type abbruchLeser struct{ gelesen bool }

func (a *abbruchLeser) Read(p []byte) (int, error) {
	if !a.gelesen {
		a.gelesen = true
		return copy(p, "TEIL"), nil
	}
	runtime.Goexit()
	return 0, nil
}

// Sprint-92-Kritiker KS-7: Bricht ein Upload mitten im Kopieren ab, darf die
// id nicht dauerhaft belegt bleiben — ein Wiederholungs-Upload muss gelingen.
func TestSave_AbbruchBelegtIdNicht(t *testing.T) {
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	fertig := make(chan struct{})
	go func() {
		defer close(fertig)
		_ = store.Save("b1", "c1", "att-1", "a.txt", &abbruchLeser{})
	}()
	<-fertig
	if err := store.Save("b1", "c1", "att-1", "a.txt", strings.NewReader("GANZ")); err != nil {
		t.Fatalf("Wiederholungs-Upload nach Abbruch = %v, erwartet Erfolg", err)
	}
	rc, name, err := store.Open("b1", "c1", "att-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	inhalt, _ := io.ReadAll(rc)
	if name != "a.txt" || string(inhalt) != "GANZ" {
		t.Fatalf("Open = %q/%q, erwartet a.txt/GANZ", name, inhalt)
	}
}

// Verify V-6: Alte Teil-Dateien (harter Abbruch, kein defer) räumt der nächste
// Upload ins selbe Kartenverzeichnis weg; frische bleiben (laufender Upload).
// StageCard nimmt Teil-Dateien nicht mit.
func TestSave_RaeumtAlteTeileUndTransferIgnoriertSie(t *testing.T) {
	t.Setenv(uhr.PruefungEnvVar, "aus") // Uhr der Testumgebung zählt hier nicht
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	dir := filepath.Join(baseDir, "b1", "c1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	alt := filepath.Join(dir, teilPraefix+"att-0-alt")
	frisch := filepath.Join(dir, teilPraefix+"att-0-frisch")
	for _, p := range []string{alt, frisch} {
		if err := os.WriteFile(p, []byte("TEIL"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vorher := time.Now().Add(-2 * teilMaxAlter)
	if err := os.Chtimes(alt, vorher, vorher); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("b1", "c1", "att-1", "a.txt", strings.NewReader("GANZ")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(alt); !os.IsNotExist(err) {
		t.Fatalf("alte Teil-Datei liegt noch: %v", err)
	}
	if _, err := os.Stat(frisch); err != nil {
		t.Fatalf("frische Teil-Datei wurde entfernt: %v", err)
	}
	staged, err := store.StageCardAttachments("b1", "b2", "c1")
	if err != nil {
		t.Fatalf("StageCardAttachments: %v", err)
	}
	eintraege, err := os.ReadDir(staged.stageDir)
	if err != nil {
		t.Fatalf("readdir stage: %v", err)
	}
	for _, e := range eintraege {
		if strings.HasPrefix(e.Name(), teilPraefix) {
			t.Fatalf("Teil-Datei im Transfer: %s", e.Name())
		}
	}
}

// Verify 2 W-1: Mit ungestellter Uhr räumt Save keine Teil-Datei weg.
func TestSave_RaeumtTeileNurMitGestellterUhr(t *testing.T) {
	t.Setenv(uhr.PruefungEnvVar, "an")
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })
	uhr.Gestellt = func() (bool, error) { return false, nil }
	baseDir := t.TempDir()
	store := NewAttachmentStore(baseDir)
	dir := filepath.Join(baseDir, "b1", "c1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	teil := filepath.Join(dir, teilPraefix+"att-0-alt")
	if err := os.WriteFile(teil, []byte("TEIL"), 0o644); err != nil {
		t.Fatal(err)
	}
	vorher := time.Now().Add(-2 * teilMaxAlter)
	_ = os.Chtimes(teil, vorher, vorher)
	if err := store.Save("b1", "c1", "att-1", "a.txt", strings.NewReader("GANZ")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(teil); err != nil {
		t.Fatalf("Teil-Datei bei ungestellter Uhr entfernt: %v", err)
	}
}
