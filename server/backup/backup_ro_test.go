package backup

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// F-1861: Der Begleitcontainer `aboard-backup` der Compose-Datei bindet das
// DB-Volume `:ro` ein, während der Server-Container weiterschreibt.
// Nachgebaut: Ein offener Schreiber hält eine Zeile nur in der WAL (kein
// Checkpoint), der Backup-Lauf sieht das Verzeichnis nur lesbar und läuft in
// einem EIGENEN Prozess — im selben Prozess teilte SQLite die
// Shared-Memory-Abbildung (-shm) des Schreibers, und der Test bewiese nichts
// über einen zweiten Container. Das Archiv muss die jüngste Zeile tragen.
//
// Gemessen am 2026-10-08: beide Varianten grün, also ist der Verdacht
// hinfällig. Gegenprobe: Mit `immutable=1` im Backup-DSN (WAL wird übergangen)
// werden beide rot — der Test trennt.

// legeSchreiberMitWALAn öffnet eine WAL-Datenbank unter <dir>/db/aboard.db und
// hinterlässt eine Zeile, die nur in der WAL steht. Der Schreiber bleibt bis
// zum Testende offen.
func legeSchreiberMitWALAn(t *testing.T, dir string) string {
	t.Helper()
	dbDir := filepath.Join(dir, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "aboard.db")
	schreiber, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	schreiber.SetMaxOpenConns(1)
	t.Cleanup(func() { schreiber.Close() })
	for _, s := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA wal_autocheckpoint = 0",
		`CREATE TABLE boards (id TEXT PRIMARY KEY, title TEXT NOT NULL)`,
		"PRAGMA user_version = 1",
		"PRAGMA wal_checkpoint(TRUNCATE)",
		`INSERT INTO boards VALUES ('frisch', 'nur in der WAL')`,
	} {
		if _, err := schreiber.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return dbPath
}

// hilfsprozessBackup startet den Backup-Lauf als Kindprozess; vorne kann ein
// Wrapper stehen (z. B. unshare für einen echten Nur-lesen-Mount).
func hilfsprozessBackup(t *testing.T, dbPath, archiv string, wrapper ...string) ([]byte, error) {
	t.Helper()
	args := append(append([]string{}, wrapper...), os.Args[0], "-test.run=^TestHilfsprozessBackup$", "-test.count=1")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(),
		"ABOARD_TEST_BACKUP_DB="+dbPath,
		"ABOARD_TEST_BACKUP_ANH="+t.TempDir(),
		"ABOARD_TEST_BACKUP_OUT="+archiv)
	return cmd.CombinedOutput()
}

func pruefeJuengsteZeileImArchiv(t *testing.T, archiv string) {
	t.Helper()
	ziel := t.TempDir()
	zielDB := filepath.Join(ziel, "db", "aboard.db")
	if err := Restore(context.Background(), RestoreOptions{
		ArchivePath: archiv, DBPath: zielDB, AttachmentsDir: filepath.Join(ziel, "attachments"),
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	db, err := sql.Open("sqlite", zielDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var titel string
	if err := db.QueryRow("SELECT title FROM boards WHERE id = 'frisch'").Scan(&titel); err != nil {
		t.Fatalf("jüngste Zeile fehlt im Archiv: %v", err)
	}
}

// Variante 1: nur lesbare Dateirechte (EACCES). Für root wirken sie nicht.
func TestBackupAusNurLesbaremVerzeichnis(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("als root greifen Dateirechte nicht")
	}
	dir := t.TempDir()
	dbPath := legeSchreiberMitWALAn(t, dir)
	dbDir := filepath.Dir(dbPath)

	// Nur lesbar machen — und in jedem Fall zurücksetzen, sonst kann
	// t.TempDir() nicht aufräumen.
	dateien := []string{dbPath, dbPath + "-wal", dbPath + "-shm"}
	t.Cleanup(func() {
		os.Chmod(dbDir, 0o755)
		for _, p := range dateien {
			os.Chmod(p, 0o644)
		}
	})
	for _, p := range dateien {
		if err := os.Chmod(p, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(dbDir, 0o555); err != nil {
		t.Fatal(err)
	}

	archiv := filepath.Join(dir, "ro.tar.gz")
	if aus, err := hilfsprozessBackup(t, dbPath, archiv); err != nil {
		t.Fatalf("Backup aus nur lesbarem Verzeichnis: %v\n%s", err, aus)
	}
	pruefeJuengsteZeileImArchiv(t, archiv)
}

// Variante 2: echter Nur-lesen-Bind-Mount (EROFS) wie bei `:ro` im
// Container — über einen User-Namespace, ohne root. Steht `unshare` nicht zur
// Verfügung oder verbietet das System User-Namespaces: Skip.
func TestBackupAusNurLesenMount(t *testing.T) {
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("unshare nicht vorhanden")
	}
	dir := t.TempDir()
	dbPath := legeSchreiberMitWALAn(t, dir)
	dbDir := filepath.Dir(dbPath)
	if aus, err := exec.Command(unshare, "-rm", "sh", "-c",
		`mount --bind "$1" "$1" && mount -o remount,bind,ro "$1"`, "sh", dbDir).CombinedOutput(); err != nil {
		t.Skipf("Nur-lesen-Mount im User-Namespace nicht möglich: %v %s", err, aus)
	}

	archiv := filepath.Join(dir, "ro-mount.tar.gz")
	// Erst mounten, dann im selben Namespace prüfen, dass wirklich nichts
	// schreibbar ist, dann den Backup-Lauf starten.
	skript := `mount --bind "$1" "$1" && mount -o remount,bind,ro "$1" || exit 90
		if touch "$1/.probe" 2>/dev/null; then exit 91; fi
		shift; exec "$@"`
	aus, err := hilfsprozessBackup(t, dbPath, archiv, unshare, "-rm", "sh", "-c", skript, "sh", dbDir)
	if err != nil {
		t.Fatalf("Backup aus Nur-lesen-Mount: %v\n%s", err, aus)
	}
	pruefeJuengsteZeileImArchiv(t, archiv)
}

// TestHilfsprozessBackup ist kein eigener Test: Er läuft nur als Kindprozess
// der beiden Tests oben (Umgebungsvariablen gesetzt).
func TestHilfsprozessBackup(t *testing.T) {
	db := os.Getenv("ABOARD_TEST_BACKUP_DB")
	if db == "" {
		t.Skip("nur als Hilfsprozess")
	}
	if _, err := Run(context.Background(), Options{
		DBPath: db, AttachmentsDir: os.Getenv("ABOARD_TEST_BACKUP_ANH"),
		Out: os.Getenv("ABOARD_TEST_BACKUP_OUT"),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}
