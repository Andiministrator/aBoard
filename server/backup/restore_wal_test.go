package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// legeAlteDBMitWALAn baut am Restore-Ziel den Zustand nach einem harten
// Abbruch des Servers nach (F-1859): eine Datenbank, daneben eine -wal-Datei
// mit Frames, die noch nicht in die Hauptdatei zurückgeschrieben sind.
//
// Ein harter Abbruch lässt sich im Test nicht auslösen, ohne einen Prozess
// zu töten. Deshalb: WAL-Datei bei offener Verbindung kopieren, Verbindung
// schließen (das schreibt die WAL zurück und löscht sie), dann die Kopie
// wieder neben eine ältere Hauptdatei legen.
func legeAlteDBMitWALAn(t *testing.T, destDB string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(destDB), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", destDB)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, s := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA wal_autocheckpoint = 0",
		`CREATE TABLE boards (id TEXT PRIMARY KEY, title TEXT NOT NULL, version TEXT NOT NULL,
			state_json TEXT NOT NULL, updated_at INTEGER NOT NULL, created_at INTEGER NOT NULL)`,
		"PRAGMA wal_checkpoint(TRUNCATE)",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	// Hauptdatei sichern, BEVOR die Alt-Zeile in die WAL geht.
	haupt, err := os.ReadFile(destDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO boards VALUES ('alt-board', 'ALT aus der WAL', '2', '{}', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(destDB + "-wal")
	if err != nil {
		t.Fatalf("WAL bei offener Verbindung lesen: %v", err)
	}
	if len(wal) == 0 {
		t.Fatal("WAL ist leer — Testaufbau trägt nicht")
	}
	db.Close()
	if err := os.WriteFile(destDB, haupt, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destDB+"-wal", wal, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreAlteWALWirdNichtAngewendet — F-1859. Liegt neben dem Ziel eine
// WAL aus dem alten Bestand, darf sie nach dem Restore nicht auf die
// zurückgespielte Datenbank treffen.
//
// Gemessen am 2026-10-08 (Widerlegungsbedingung des Findings): Ohne den Fix
// wendet SQLite die fremde WAL an — die zurückgespielte Datenbank ist danach
// kaputt oder trägt Zeilen, die nicht im Archiv standen. Der WAL-Teil des
// Befunds ist also NICHT gegenstandslos.
func TestRestoreAlteWALWirdNichtAngewendet(t *testing.T) {
	quelle := createTestDB(t)
	archiv := filepath.Join(t.TempDir(), "wal.tar.gz")
	if _, err := Run(context.Background(), Options{
		DBPath: quelle, AttachmentsDir: t.TempDir(), Out: archiv,
	}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	ziel := t.TempDir()
	destDB := filepath.Join(ziel, "db", "aboard.db")
	legeAlteDBMitWALAn(t, destDB)

	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath: archiv, DBPath: destDB,
		AttachmentsDir: filepath.Join(ziel, "attachments"), Force: true,
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	db, err := sql.Open("sqlite", destDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ok string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&ok); err != nil || ok != "ok" {
		t.Fatalf("integrity_check = %q, err = %v — alte WAL wurde angewendet", ok, err)
	}
	var titel []string
	rows, err := db.Query("SELECT title FROM boards ORDER BY id")
	if err != nil {
		t.Fatalf("boards lesen: %v — alte WAL wurde angewendet", err)
	}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		titel = append(titel, s)
	}
	rows.Close()
	if strings.Join(titel, "|") != "Test-Board" {
		t.Fatalf("boards = %v, erwartet nur [Test-Board] aus dem Archiv", titel)
	}

	// Die alte WAL ist nicht gelöscht, sondern beiseitegelegt.
	treffer, _ := filepath.Glob(destDB + "-wal.vor-restore-*")
	if len(treffer) != 1 {
		t.Fatalf("beiseitegelegte WAL: %v, erwartet genau eine", treffer)
	}
}

// TestRestoreMeldetFremdeAnhaenge — F-1859, zweiter Teil. Anhänge, die nicht
// im Archiv stehen, bleiben liegen (nichts wird gelöscht), aber Restore
// nennt sie.
func TestRestoreMeldetFremdeAnhaenge(t *testing.T) {
	quelle := createTestDB(t)
	archiv := filepath.Join(t.TempDir(), "anh.tar.gz")
	if _, err := Run(context.Background(), Options{
		DBPath: quelle, AttachmentsDir: createTestAttachments(t), Out: archiv,
	}); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	ziel := t.TempDir()
	attach := filepath.Join(ziel, "attachments")
	fremd := filepath.Join(attach, "board-x", "card-y", "alt__rest.txt")
	if err := os.MkdirAll(filepath.Dir(fremd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fremd, []byte("alt"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gemeldet []string
	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath: archiv, DBPath: filepath.Join(ziel, "db", "aboard.db"),
		AttachmentsDir: attach, Force: true,
		FremdeAnhaenge: func(rel []string) { gemeldet = rel },
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if strings.Join(gemeldet, "|") != "board-x/card-y/alt__rest.txt" {
		t.Fatalf("gemeldet = %v, erwartet genau den fremden Anhang", gemeldet)
	}
	if _, err := os.Stat(fremd); err != nil {
		t.Fatalf("fremder Anhang darf nicht gelöscht werden: %v", err)
	}
}

// schreibeArchiv baut ein .tar.gz aus (Name, Inhalt)-Paaren in dieser
// Reihenfolge — für Archive, die Run() nie erzeugen würde.
func schreibeArchiv(t *testing.T, pfad string, eintraege [][2]string) {
	t.Helper()
	f, err := os.Create(pfad)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range eintraege {
		if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
}

// Kritiker S-3: Scheitert das Archiv VOR dem DB-Eintrag, bleibt die alte WAL
// an ihrem Platz — sonst läge die unversehrte alte DB ohne ihre jüngsten
// Transaktionen da.
func TestRestoreFehlerVorDBLaesstWALStehen(t *testing.T) {
	ziel := t.TempDir()
	destDB := filepath.Join(ziel, "db", "aboard.db")
	legeAlteDBMitWALAn(t, destDB)
	archiv := filepath.Join(t.TempDir(), "kaputt.tar.gz")
	schreibeArchiv(t, archiv, [][2]string{
		{"meta.json", `{"schema_version":1}`},
		{"attachments/../boese", "x"}, // wird abgelehnt (F-236) — vor db/aboard.db
		{"db/aboard.db", "egal"},
	})
	err := Restore(context.Background(), RestoreOptions{
		ArchivePath: archiv, DBPath: destDB, AttachmentsDir: filepath.Join(ziel, "attachments"), Force: true,
	})
	if err == nil {
		t.Fatal("Restore mit unsicherem Pfad muss scheitern")
	}
	if _, err := os.Stat(destDB + "-wal"); err != nil {
		t.Fatalf("alte WAL muss an ihrem Platz bleiben: %v", err)
	}
	if treffer, _ := filepath.Glob(destDB + "-wal.vor-restore-*"); len(treffer) != 0 {
		t.Fatalf("WAL wurde trotz Abbruch beiseitegelegt: %v", treffer)
	}
}

// Kritiker S-4: Ein Attachments-Verzeichnis, das selbst ein Symlink ist,
// meldet keine Scheinfunde („.“).
func TestRestoreSymlinkWurzelKeineFremdenAnhaenge(t *testing.T) {
	quelle := createTestDB(t)
	archiv := filepath.Join(t.TempDir(), "sym.tar.gz")
	if _, err := Run(context.Background(), Options{
		DBPath: quelle, AttachmentsDir: createTestAttachments(t), Out: archiv,
	}); err != nil {
		t.Fatal(err)
	}
	ziel := t.TempDir()
	echt := filepath.Join(ziel, "echt")
	if err := os.MkdirAll(echt, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ziel, "attachments")
	if err := os.Symlink(echt, link); err != nil {
		t.Fatal(err)
	}
	var gemeldet []string
	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath: archiv, DBPath: filepath.Join(ziel, "db", "aboard.db"), AttachmentsDir: link, Force: true,
		FremdeAnhaenge: func(rel []string) { gemeldet = rel },
	}); err != nil {
		t.Fatal(err)
	}
	if len(gemeldet) != 0 {
		t.Fatalf("Scheinfunde bei Symlink-Wurzel: %v", gemeldet)
	}
}

// Verify W-2: Ist die Ziel-DB nicht beschreibbar, bleibt die alte WAL an
// ihrem Platz (vorher war sie schon beiseitegelegt, wenn os.Create scheiterte).
func TestRestoreNichtBeschreibbareDBLaesstWALStehen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("als root greifen Dateirechte nicht")
	}
	quelle := createTestDB(t)
	archiv := filepath.Join(t.TempDir(), "w2.tar.gz")
	if _, err := Run(context.Background(), Options{DBPath: quelle, AttachmentsDir: t.TempDir(), Out: archiv}); err != nil {
		t.Fatal(err)
	}
	ziel := t.TempDir()
	destDB := filepath.Join(ziel, "db", "aboard.db")
	legeAlteDBMitWALAn(t, destDB)
	if err := os.Chmod(destDB, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(destDB, 0o644) })
	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath: archiv, DBPath: destDB, AttachmentsDir: filepath.Join(ziel, "attachments"), Force: true,
	}); err == nil {
		t.Fatal("Restore auf nicht beschreibbare DB muss scheitern")
	}
	if _, err := os.Stat(destDB + "-wal"); err != nil {
		t.Fatalf("alte WAL muss an ihrem Platz bleiben: %v", err)
	}
}

// TestRestoreDBKopierfehler — F-1888 (Sprint-86-Kritiker K-B2). Bricht das
// Archiv mitten in db/aboard.db ab, bleibt die bisherige Datenbank samt WAL
// unverändert; keine Zwischendatei bleibt liegen.
func TestRestoreDBKopierfehler(t *testing.T) {
	ziel := t.TempDir()
	destDB := filepath.Join(ziel, "db", "aboard.db")
	legeAlteDBMitWALAn(t, destDB)
	vorher, err := os.ReadFile(destDB)
	if err != nil {
		t.Fatal(err)
	}
	archiv := filepath.Join(t.TempDir(), "abgeschnitten.tar.gz")
	schreibeAbgeschnittenesArchiv(t, archiv)

	err = Restore(context.Background(), RestoreOptions{
		ArchivePath: archiv, DBPath: destDB, AttachmentsDir: filepath.Join(ziel, "attachments"), Force: true,
	})
	if err == nil {
		t.Fatal("Restore mit abgeschnittenem Archiv muss scheitern")
	}
	nachher, _ := os.ReadFile(destDB)
	if string(nachher) != string(vorher) {
		t.Fatalf("bisherige Datenbank verändert (%d → %d Bytes): %v", len(vorher), len(nachher), err)
	}
	if _, serr := os.Stat(destDB + "-wal"); serr != nil {
		t.Fatalf("alte WAL muss an ihrem Platz bleiben: %v", serr)
	}
	if treffer, _ := filepath.Glob(destDB + ".restore-*"); len(treffer) != 0 {
		t.Fatalf("Zwischendatei liegengeblieben: %v", treffer)
	}
}

// Verify V-2 zu F-1888: Gab es vorher keine Datenbank, bleibt nach einem
// abgeschnittenen Archiv auch keine leere zurück.
func TestRestoreDBKopierfehlerOhneAlteDB(t *testing.T) {
	ziel := t.TempDir()
	destDB := filepath.Join(ziel, "db", "aboard.db")
	if err := os.MkdirAll(filepath.Dir(destDB), 0o755); err != nil {
		t.Fatal(err)
	}
	archiv := filepath.Join(t.TempDir(), "abgeschnitten.tar.gz")
	schreibeAbgeschnittenesArchiv(t, archiv)
	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath: archiv, DBPath: destDB, AttachmentsDir: filepath.Join(ziel, "attachments"), Force: true,
	}); err == nil {
		t.Fatal("Restore mit abgeschnittenem Archiv muss scheitern")
	}
	if _, err := os.Stat(destDB); !os.IsNotExist(err) {
		t.Fatalf("leere Datenbank liegengeblieben: %v", err)
	}
}

// schreibeAbgeschnittenesArchiv: meta.json plus ein db/aboard.db-Eintrag, der
// mitten im Inhalt endet (Kopfgröße 64 KiB, geschrieben 100 Bytes).
func schreibeAbgeschnittenesArchiv(t *testing.T, archiv string) {
	t.Helper()
	datei, err := os.Create(archiv)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(datei)
	tw := tar.NewWriter(gz)
	meta := []byte(`{"schema_version":1}`)
	if err := tw.WriteHeader(&tar.Header{Name: "meta.json", Mode: 0o644, Size: int64(len(meta)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(meta); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "db/aboard.db", Mode: 0o644, Size: 1 << 16, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(strings.Repeat("x", 100))); err != nil {
		t.Fatal(err)
	}
	// Absichtlich KEIN tw.Close(): das Archiv endet mitten im Eintrag.
	if err := tw.Flush(); err != nil && !strings.Contains(err.Error(), "missed writing") {
		t.Fatal(err)
	}
	_ = gz.Close()
	_ = datei.Close()
}

// F-1900 (Verify-2 W-4 der Sprint-86-End-QA): Drei der vier frühen
// Fehlerstellen in extractEntry waren ungetestet — Zwischendatei anlegen,
// Zwischendatei syncen, WAL/SHM beiseitelegen. Jede muss eine neu angelegte
// leere DB wieder entfernen (Verify V-2), die Zwischendatei wegräumen und
// eine bestehende DB samt WAL unverändert lassen. Die Fehler kommen aus
// Nähten im eigenen Code, nicht von außen.

var errNaht1900 = errors.New("F-1900 Testfehler")

type fruehFehler1900 struct {
	name   string
	setzen func() (zurueck func())
}

func fruehFehler() []fruehFehler1900 {
	return []fruehFehler1900{
		{"CreateTemp (F-1900)", func() func() {
			alt := erzeugeZwischendatei
			erzeugeZwischendatei = func(string, string) (*os.File, error) { return nil, errNaht1900 }
			return func() { erzeugeZwischendatei = alt }
		}},
		{"Sync (F-1900)", func() func() {
			alt := synceZwischendatei
			synceZwischendatei = func(*os.File) error { return errNaht1900 }
			return func() { synceZwischendatei = alt }
		}},
		{"Beiseitelegen (F-1900)", func() func() {
			alt := beilagenBeiseite
			beilagenBeiseite = func(string) error { return errNaht1900 }
			return func() { beilagenBeiseite = alt }
		}},
	}
}

func restoreMitFehler1900(t *testing.T, destDB string) error {
	t.Helper()
	archiv := filepath.Join(t.TempDir(), "gut.tar.gz")
	schreibeArchiv(t, archiv, [][2]string{{"meta.json", `{"schema_version":1}`}, {"db/aboard.db", "neuer Inhalt"}})
	return Restore(context.Background(), RestoreOptions{
		ArchivePath: archiv, DBPath: destDB, AttachmentsDir: filepath.Join(filepath.Dir(filepath.Dir(destDB)), "attachments"), Force: true,
	})
}

// Ohne bisherige DB: nach dem Fehler keine leere DB, keine Zwischendatei.
func TestRestoreFruehFehlerOhneAlteDB_F1900(t *testing.T) {
	for _, f := range fruehFehler() {
		t.Run(f.name, func(t *testing.T) {
			destDB := filepath.Join(t.TempDir(), "db", "aboard.db")
			if err := os.MkdirAll(filepath.Dir(destDB), 0o755); err != nil {
				t.Fatal(err)
			}
			zurueck := f.setzen()
			err := restoreMitFehler1900(t, destDB)
			zurueck()
			if !errors.Is(err, errNaht1900) && (err == nil || !strings.Contains(err.Error(), errNaht1900.Error())) {
				t.Fatalf("Fehler nicht aus der Naht: %v", err)
			}
			if _, serr := os.Stat(destDB); !os.IsNotExist(serr) {
				t.Errorf("leere Datenbank liegengeblieben: %v", serr)
			}
			if treffer, _ := filepath.Glob(destDB + ".restore-*"); len(treffer) != 0 {
				t.Errorf("Zwischendatei liegengeblieben: %v", treffer)
			}
		})
	}
}

// Mit bisheriger DB samt WAL: beide unverändert, keine Zwischendatei.
func TestRestoreFruehFehlerMitAlterDB_F1900(t *testing.T) {
	for _, f := range fruehFehler() {
		t.Run(f.name, func(t *testing.T) {
			destDB := filepath.Join(t.TempDir(), "db", "aboard.db")
			legeAlteDBMitWALAn(t, destDB)
			vorher, err := os.ReadFile(destDB)
			if err != nil {
				t.Fatal(err)
			}
			zurueck := f.setzen()
			err = restoreMitFehler1900(t, destDB)
			zurueck()
			if err == nil || !strings.Contains(err.Error(), errNaht1900.Error()) {
				t.Fatalf("Fehler nicht aus der Naht: %v", err)
			}
			if nachher, _ := os.ReadFile(destDB); string(nachher) != string(vorher) {
				t.Errorf("bisherige Datenbank verändert (%d → %d Bytes)", len(vorher), len(nachher))
			}
			if _, serr := os.Stat(destDB + "-wal"); serr != nil {
				t.Errorf("alte WAL muss an ihrem Platz bleiben: %v", serr)
			}
			if treffer, _ := filepath.Glob(destDB + ".restore-*"); len(treffer) != 0 {
				t.Errorf("Zwischendatei liegengeblieben: %v", treffer)
			}
		})
	}
}
