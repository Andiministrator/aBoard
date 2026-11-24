package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// createTestDB legt eine minimale SQLite-Datenbank mit WAL an,
// führt eine einfache Migration aus und gibt den Pfad zurück.
// Der Aufrufer räumt die Datei auf.
func createTestDB(t *testing.T) string {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "aboard-test-*.db")
	if err != nil {
		t.Fatalf("test-DB temporäre Datei: %v", err)
	}
	tmp.Close()
	path := tmp.Name()

	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("test-DB öffnen: %v", err)
	}
	defer db.Close()

	stmts := []string{
		"PRAGMA journal_mode = WAL",
		`CREATE TABLE boards (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			version TEXT NOT NULL,
			state_json TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES ('board-1', 'Test-Board', '2', '{"board":{"id":"board-1"}}', 1000, 1000)`,
		"PRAGMA user_version = 1",
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("test-DB setup %q: %v", s, err)
		}
	}
	return path
}

// createTestAttachments legt eine Attachment-Struktur mit einer Datei an.
func createTestAttachments(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	subDir := filepath.Join(dir, "board-1", "card-1")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("attachment-dir anlegen: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "att-1__foto.jpg"), []byte("JPEG-Inhalt"), 0o644); err != nil {
		t.Fatalf("attachment schreiben: %v", err)
	}
	return dir
}

// archiveContents liest alle Datei-Namen aus einem .tar.gz-Archiv.
func archiveContents(t *testing.T, archivePath string) []string {
	t.Helper()
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("Archiv öffnen: %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip lesen: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar lesen: %v", err)
		}
		names = append(names, hdr.Name)
	}
	return names
}

// TestBackupErzeugtArchiv prüft, dass ein Archiv mit den erwarteten Einträgen entsteht.
func TestBackupErzeugtArchiv(t *testing.T) {
	dbPath := createTestDB(t)
	attachDir := createTestAttachments(t)
	outPath := filepath.Join(t.TempDir(), "backup.tar.gz")

	_, err := Run(context.Background(), Options{
		DBPath:         dbPath,
		AttachmentsDir: attachDir,
		Out:            outPath,
		AboardVersion:  "v0.27.0",
	})
	if err != nil {
		t.Fatalf("Backup fehlgeschlagen: %v", err)
	}

	names := archiveContents(t, outPath)
	want := map[string]bool{
		"meta.json":    false,
		"db/aboard.db": false,
	}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("Archiv: %q fehlt", name)
		}
	}

	// Attachment-Datei muss im Archiv sein.
	found := false
	for _, n := range names {
		if n == "attachments/board-1/card-1/att-1__foto.jpg" {
			found = true
		}
	}
	if !found {
		t.Errorf("Attachment nicht im Archiv gefunden; Archiv-Inhalt: %v", names)
	}
}

// TestBackupMetaJSON prüft den Inhalt der meta.json im Archiv.
func TestBackupMetaJSON(t *testing.T) {
	dbPath := createTestDB(t)
	outPath := filepath.Join(t.TempDir(), "backup.tar.gz")

	_, err := Run(context.Background(), Options{
		DBPath:         dbPath,
		AttachmentsDir: t.TempDir(),
		Out:            outPath,
		AboardVersion:  "v0.27.0",
	})
	if err != nil {
		t.Fatalf("Backup fehlgeschlagen: %v", err)
	}

	// meta.json aus Archiv lesen.
	f, _ := os.Open(outPath)
	defer f.Close()
	gr, _ := gzip.NewReader(f)
	defer gr.Close()
	tr := tar.NewReader(gr)
	var meta Meta
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if hdr.Name == "meta.json" {
			if err := json.NewDecoder(tr).Decode(&meta); err != nil {
				t.Fatalf("meta.json dekodieren: %v", err)
			}
			break
		}
	}

	if meta.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, erwartet 1", meta.SchemaVersion)
	}
	if meta.AboardVersion != "v0.27.0" {
		t.Errorf("aboard_version = %q, erwartet v0.27.0", meta.AboardVersion)
	}
	if meta.CreatedAt == "" {
		t.Error("created_at ist leer")
	}
}

// TestBackupRollingVerzeichnis prüft, dass im Rolling-Modus eine dated Datei angelegt wird.
func TestBackupRollingVerzeichnis(t *testing.T) {
	dbPath := createTestDB(t)
	outDir := t.TempDir()

	archivePath, err := Run(context.Background(), Options{
		DBPath:         dbPath,
		AttachmentsDir: t.TempDir(),
		Out:            outDir,
	})
	if err != nil {
		t.Fatalf("Backup fehlgeschlagen: %v", err)
	}

	expectedName := "aboard-" + time.Now().UTC().Format("2006-01-02") + ".tar.gz"
	expectedPath := filepath.Join(outDir, expectedName)
	if archivePath != expectedPath {
		t.Errorf("Archiv-Pfad = %q, erwartet %q", archivePath, expectedPath)
	}
	if _, err := os.Stat(archivePath); err != nil {
		t.Errorf("Archiv existiert nicht: %v", err)
	}
}

// TestRollingRetentionKeepDaily prüft die tägliche Retention-Logik.
func TestRollingRetentionKeepDaily(t *testing.T) {
	outDir := t.TempDir()

	// Drei Backups erzeugen mit unterschiedlichen Daten.
	days := []string{"2026-05-12", "2026-05-13", "2026-05-14"}
	for _, d := range days {
		name := "aboard-" + d + ".tar.gz"
		if err := os.WriteFile(filepath.Join(outDir, name), []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Retention: behalte nur 2 jüngste.
	if err := applyRetention(outDir, 2, 0); err != nil {
		t.Fatalf("applyRetention: %v", err)
	}

	entries, _ := os.ReadDir(outDir)
	var remaining []string
	for _, e := range entries {
		remaining = append(remaining, e.Name())
	}
	sort.Strings(remaining)

	// Ältester (2026-05-12) muss weg sein.
	if len(remaining) != 2 {
		t.Errorf("verbleibende Archive: %v, erwartet 2", remaining)
	}
	for _, r := range remaining {
		if r == "aboard-2026-05-12.tar.gz" {
			t.Errorf("ältestes Archiv wurde nicht gelöscht")
		}
	}
}

// TestRollingRetentionKeepWeekly prüft die wöchentliche Retention-Logik.
func TestRollingRetentionKeepWeekly(t *testing.T) {
	outDir := t.TempDir()

	// Sechs tägliche Archive anlegen: 2026-05-08 (Freitag) bis 2026-05-13 (Mittwoch).
	// 2026-05-11 ist ein Montag.
	days := []string{
		"2026-05-08", // Freitag
		"2026-05-09", // Samstag
		"2026-05-10", // Sonntag
		"2026-05-11", // Montag ← Wochen-Checkpoint
		"2026-05-12", // Dienstag
		"2026-05-13", // Mittwoch
	}
	for _, d := range days {
		name := "aboard-" + d + ".tar.gz"
		if err := os.WriteFile(filepath.Join(outDir, name), []byte("dummy"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// keepDaily=2 → 2026-05-12 + 2026-05-13 behalten
	// keepWeekly=1 → 2026-05-11 (Montag) zusätzlich behalten
	// Rest löschen (2026-05-08, 2026-05-09, 2026-05-10)
	if err := applyRetention(outDir, 2, 1); err != nil {
		t.Fatalf("applyRetention: %v", err)
	}

	entries, _ := os.ReadDir(outDir)
	var remaining []string
	for _, e := range entries {
		remaining = append(remaining, e.Name())
	}
	sort.Strings(remaining)

	expected := []string{
		"aboard-2026-05-11.tar.gz",
		"aboard-2026-05-12.tar.gz",
		"aboard-2026-05-13.tar.gz",
	}
	if len(remaining) != len(expected) {
		t.Fatalf("verbleibende Archive: %v, erwartet: %v", remaining, expected)
	}
	for i, r := range remaining {
		if r != expected[i] {
			t.Errorf("Archive[%d] = %q, erwartet %q", i, r, expected[i])
		}
	}
}
