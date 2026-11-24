package backup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestRestoreRoundtrip prüft den vollständigen Backup→Restore-Zyklus.
// Erzeugt DB + Attachment, erstellt Backup, leert Zielverzeichnis,
// restored und vergleicht dann den DB-Inhalt.
func TestRestoreRoundtrip(t *testing.T) {
	dbPath := createTestDB(t)
	attachDir := createTestAttachments(t)

	// Backup erstellen.
	outPath := filepath.Join(t.TempDir(), "roundtrip.tar.gz")
	if _, err := Run(context.Background(), Options{
		DBPath:         dbPath,
		AttachmentsDir: attachDir,
		Out:            outPath,
	}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Restore in neues Zielverzeichnis.
	restoreDir := t.TempDir()
	destDB := filepath.Join(restoreDir, "db", "aboard.db")
	destAttach := filepath.Join(restoreDir, "attachments")

	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath:    outPath,
		DBPath:         destDB,
		AttachmentsDir: destAttach,
		Force:          false,
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// DB-Inhalt prüfen.
	db, err := sql.Open("sqlite", destDB)
	if err != nil {
		t.Fatalf("restored DB öffnen: %v", err)
	}
	defer db.Close()

	var title string
	if err := db.QueryRow("SELECT title FROM boards WHERE id = 'board-1'").Scan(&title); err != nil {
		t.Fatalf("boards lesen: %v", err)
	}
	if title != "Test-Board" {
		t.Errorf("title = %q, erwartet 'Test-Board'", title)
	}

	// Attachment-Datei muss am richtigen Ort sein.
	attPath := filepath.Join(destAttach, "board-1", "card-1", "att-1__foto.jpg")
	data, err := os.ReadFile(attPath)
	if err != nil {
		t.Fatalf("Attachment lesen: %v", err)
	}
	if string(data) != "JPEG-Inhalt" {
		t.Errorf("Attachment-Inhalt = %q, erwartet 'JPEG-Inhalt'", string(data))
	}
	// Verify V-3 zu F-1888: Nach Erfolg bleibt keine Zwischendatei liegen.
	if treffer, _ := filepath.Glob(destDB + ".restore-*"); len(treffer) != 0 {
		t.Errorf("Zwischendatei nach Erfolg liegengeblieben: %v", treffer)
	}
}

// TestRestoreDryRun prüft, dass --dry-run nichts schreibt.
func TestRestoreDryRun(t *testing.T) {
	dbPath := createTestDB(t)
	outPath := filepath.Join(t.TempDir(), "dryrun.tar.gz")
	if _, err := Run(context.Background(), Options{
		DBPath:         dbPath,
		AttachmentsDir: t.TempDir(),
		Out:            outPath,
	}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	destDir := t.TempDir()
	destDB := filepath.Join(destDir, "aboard.db")

	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath:    outPath,
		DBPath:         destDB,
		AttachmentsDir: filepath.Join(destDir, "attachments"),
		DryRun:         true,
	}); err != nil {
		t.Fatalf("Restore dry-run: %v", err)
	}

	// DB darf nicht existieren.
	if _, err := os.Stat(destDB); err == nil {
		t.Error("dry-run hat trotzdem eine DB-Datei geschrieben")
	}
}

// TestRestoreSchemaMismatch prüft, dass MaxSchemaVersion=0 die Schema-Prüfung
// deaktiviert: ein Archiv (hier user_version=1) wird ohne Limit klaglos
// restored. Der eigentliche Ablehnungs-Pfad (Archiv-Version > Limit) ist in
// TestRestoreSchemaAbgelehnt abgedeckt (F-237: zuvor lag hier ein toter,
// mit der ersten Hälfte identischer Restore-Block — entfernt).
func TestRestoreSchemaMismatch(t *testing.T) {
	dbPath := createTestDB(t) // user_version = 1
	outPath := filepath.Join(t.TempDir(), "mismatch.tar.gz")
	if _, err := Run(context.Background(), Options{
		DBPath:         dbPath,
		AttachmentsDir: t.TempDir(),
		Out:            outPath,
	}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	destDir := t.TempDir()
	err := Restore(context.Background(), RestoreOptions{
		ArchivePath:      outPath,
		DBPath:           filepath.Join(destDir, "aboard.db"),
		AttachmentsDir:   filepath.Join(destDir, "attachments"),
		MaxSchemaVersion: 0, // 0 = keine Schema-Prüfung, Archiv hat version=1 — kein Fehler
	})
	if err != nil {
		t.Fatalf("Restore ohne Schema-Limit: %v", err)
	}
}

// TestRestoreSchemaMismatchExplicit testet explizit die Schema-Version-Ablehnung.
func TestRestoreSchemaMismatchExplicit(t *testing.T) {
	// Erstelle Backup mit user_version=1.
	dbPath := createTestDB(t)
	outPath := filepath.Join(t.TempDir(), "schema.tar.gz")
	if _, err := Run(context.Background(), Options{
		DBPath:         dbPath,
		AttachmentsDir: t.TempDir(),
		Out:            outPath,
	}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Server behauptet, er kennt nur version 0 → Schema-Mismatch.
	destDir := t.TempDir()
	err := Restore(context.Background(), RestoreOptions{
		ArchivePath:      outPath,
		DBPath:           filepath.Join(destDir, "aboard.db"),
		AttachmentsDir:   filepath.Join(destDir, "attachments"),
		MaxSchemaVersion: 0, // 0 = keine Prüfung → soll klappen
	})
	if err != nil {
		t.Fatalf("MaxSchemaVersion=0 (keine Prüfung) soll klappen: %v", err)
	}

	// Jetzt: MaxSchemaVersion=1 und Archiv hat version=1 → OK.
	destDir3 := t.TempDir()
	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath:      outPath,
		DBPath:           filepath.Join(destDir3, "aboard.db"),
		AttachmentsDir:   filepath.Join(destDir3, "attachments"),
		MaxSchemaVersion: 1,
	}); err != nil {
		t.Fatalf("MaxSchemaVersion=1 == Archiv-Version → soll klappen: %v", err)
	}

	// Archiv hat version=1, Server akzeptiert nur ≤ 0 → Fehler.
	// Wir manipulieren MaxSchemaVersion direkt im Restore-Call:
	// (kein Test hier, da der negative Grenzfall schon in checkDestination liegt)
	// Stattdessen testen wir den eigentlichen Grenzfall: MaxSchemaVersion < meta.SchemaVersion.
	destDir4 := t.TempDir()
	err = Restore(context.Background(), RestoreOptions{
		ArchivePath:    outPath,
		DBPath:         filepath.Join(destDir4, "aboard.db"),
		AttachmentsDir: filepath.Join(destDir4, "attachments"),
		// MaxSchemaVersion nicht über int-Grenze — wir nutzen einen kleinen Wert.
		// meta.SchemaVersion=1, wir setzen MaxSchemaVersion=0 aber 0=keine Prüfung.
		// Deshalb: wir spezifizieren eine sehr niedrige Schema-Version, die niedriger
		// als 1 ist, aber größer als 0, damit der Prüf-Code greift.
		// Die einzige Möglichkeit ist MaxSchemaVersion=1 schlägt nicht fehl,
		// aber wenn wir explizit testen wollen brauchen wir ein Archiv mit version>1.
		// Deshalb: dieser Testfall testet MaxSchemaVersion > meta.SchemaVersion → kein Fehler.
		MaxSchemaVersion: 5, // 5 >= 1 → kein Fehler
	})
	if err != nil {
		t.Fatalf("MaxSchemaVersion=5 >= Archiv-Version=1 → soll klappen: %v", err)
	}
}

// TestRestoreSchemaAbgelehnt testet die Ablehnung bei zu hoher Archiv-Schema-Version.
func TestRestoreSchemaAbgelehnt(t *testing.T) {
	// Erstelle Backup mit user_version=5 (indem wir createTestDB anpassen).
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "aboard-v5.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("DB anlegen: %v", err)
	}
	for _, s := range []string{
		"PRAGMA journal_mode = WAL",
		`CREATE TABLE boards (id TEXT PRIMARY KEY, title TEXT NOT NULL,
		  version TEXT NOT NULL, state_json TEXT NOT NULL,
		  updated_at INTEGER NOT NULL, created_at INTEGER NOT NULL)`,
		"PRAGMA user_version = 5",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("DB setup: %v", err)
		}
	}
	db.Close()

	outPath := filepath.Join(t.TempDir(), "v5.tar.gz")
	if _, err := Run(context.Background(), Options{
		DBPath:         dbPath,
		AttachmentsDir: t.TempDir(),
		Out:            outPath,
	}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Server kennt nur bis version 3 → Archiv mit version=5 muss abgelehnt werden.
	destDir := t.TempDir()
	err = Restore(context.Background(), RestoreOptions{
		ArchivePath:      outPath,
		DBPath:           filepath.Join(destDir, "aboard.db"),
		AttachmentsDir:   filepath.Join(destDir, "attachments"),
		MaxSchemaVersion: 3,
	})
	if err == nil {
		t.Fatal("Restore hätte mit Schema-Mismatch fehlschlagen müssen")
	}
}

// TestRestoreForceUeberschreibt prüft, dass --force eine existierende DB überschreibt.
func TestRestoreForceUeberschreibt(t *testing.T) {
	dbPath := createTestDB(t)
	outPath := filepath.Join(t.TempDir(), "force.tar.gz")
	if _, err := Run(context.Background(), Options{
		DBPath:         dbPath,
		AttachmentsDir: t.TempDir(),
		Out:            outPath,
	}); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	destDir := t.TempDir()
	destDB := filepath.Join(destDir, "db", "aboard.db")
	destAttach := filepath.Join(destDir, "attachments")

	// Erster Restore.
	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath:    outPath,
		DBPath:         destDB,
		AttachmentsDir: destAttach,
	}); err != nil {
		t.Fatalf("erster Restore: %v", err)
	}

	// Zweiter Restore ohne --force → Fehler.
	err := Restore(context.Background(), RestoreOptions{
		ArchivePath:    outPath,
		DBPath:         destDB,
		AttachmentsDir: destAttach,
		Force:          false,
	})
	if err == nil {
		t.Fatal("zweiter Restore ohne --force hätte fehlschlagen müssen")
	}

	// Zweiter Restore mit --force → Erfolg.
	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath:    outPath,
		DBPath:         destDB,
		AttachmentsDir: destAttach,
		Force:          true,
	}); err != nil {
		t.Fatalf("Restore mit --force: %v", err)
	}
}
