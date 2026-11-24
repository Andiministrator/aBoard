// Package backup implementiert die Backup- und Restore-Funktionalität für
// aboard-server (ADR-0044, Phase 2, Karte #80).
//
// Backup-Archiv-Format (.tar.gz):
//
//	meta.json             — Metadaten: schema_version, created_at, aboard_version
//	db/aboard.db          — konsistente SQLite-Kopie via VACUUM INTO (kein raw File-Copy)
//	attachments/…         — Attachments-Verzeichnis, strukturidentisch zum Volume
//
// Rolling-Retention: wenn --out ein Verzeichnis ist, wird
// <verzeichnis>/aboard-YYYY-MM-DD.tar.gz geschrieben und alte Archivdateien
// nach --keep-daily / --keep-weekly bereinigt.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Meta wird als meta.json ins Archiv eingebettet. schema_version entspricht
// dem PRAGMA user_version der gesicherten Datenbank.
type Meta struct {
	SchemaVersion int    `json:"schema_version"`
	CreatedAt     string `json:"created_at"`     // RFC3339 UTC
	AboardVersion string `json:"aboard_version"` // leer wenn nicht ermittelbar
}

// Options bündelt alle Backup-Parameter.
type Options struct {
	// DBPath ist der Pfad zur laufenden SQLite-Datei (Quelldatenbank).
	DBPath string
	// AttachmentsDir ist das Attachments-Volume-Verzeichnis.
	AttachmentsDir string
	// Out ist das Ziel: entweder eine .tar.gz-Datei oder ein Verzeichnis
	// (Rolling-Modus). Pflichtfeld.
	Out string
	// AboardVersion wird in meta.json eingetragen; leer = unbekannt.
	AboardVersion string
	// KeepDaily: Rolling-Retention — behalte die N jüngsten täglichen Archive.
	// 0 = kein Retention-Lauf.
	KeepDaily int
	// KeepWeekly: Rolling-Retention — behalte zusätzlich M wöchentliche Archive.
	// 0 = kein Wochen-Retention.
	KeepWeekly int
}

// Run führt das Backup durch. Gibt den Pfad des erzeugten Archivs zurück.
func Run(ctx context.Context, opts Options) (string, error) {
	if opts.DBPath == "" {
		return "", fmt.Errorf("backup: DBPath ist leer")
	}
	if opts.Out == "" {
		return "", fmt.Errorf("backup: Out ist leer")
	}

	// Ziel-Pfad bestimmen: Verzeichnis → Rolling-Modus; Datei → direkter Pfad.
	outPath, err := resolveOutPath(opts.Out)
	if err != nil {
		return "", err
	}

	// Temporäre SQLite-Kopie via VACUUM INTO (konsistentes Online-Backup).
	tmpDB, err := vacuumIntoCopy(ctx, opts.DBPath)
	if err != nil {
		return "", fmt.Errorf("backup: VACUUM INTO fehlgeschlagen: %w", err)
	}
	defer os.Remove(tmpDB)

	// Schema-Version aus der Kopie lesen.
	schemaVersion, err := readUserVersion(tmpDB)
	if err != nil {
		return "", fmt.Errorf("backup: user_version lesen: %w", err)
	}

	meta := Meta{
		SchemaVersion: schemaVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
		AboardVersion: opts.AboardVersion,
	}

	if err := writeTarGz(outPath, tmpDB, opts.AttachmentsDir, meta); err != nil {
		_ = os.Remove(outPath)
		return "", fmt.Errorf("backup: Archiv schreiben: %w", err)
	}

	log.Printf("backup: Archiv erstellt → %s (schema_version=%d)", outPath, schemaVersion)

	// Rolling-Retention nur wenn Out ein Verzeichnis war.
	if isDir(opts.Out) && (opts.KeepDaily > 0 || opts.KeepWeekly > 0) {
		if err := applyRetention(opts.Out, opts.KeepDaily, opts.KeepWeekly); err != nil {
			// Retention-Fehler ist nicht fatal — Backup selbst ist fertig.
			log.Printf("backup: Retention-Warnung: %v", err)
		}
	}

	return outPath, nil
}

// resolveOutPath gibt den konkreten Archiv-Pfad zurück.
// Ist out ein existierendes Verzeichnis, wird aboard-YYYY-MM-DD.tar.gz darin erzeugt.
func resolveOutPath(out string) (string, error) {
	if isDir(out) {
		name := "aboard-" + time.Now().UTC().Format("2006-01-02") + ".tar.gz"
		return filepath.Join(out, name), nil
	}
	// Datei-Pfad: Eltern-Verzeichnis anlegen falls nötig.
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", fmt.Errorf("backup: Zielverzeichnis anlegen: %w", err)
	}
	return out, nil
}

// vacuumIntoCopy erstellt via "VACUUM INTO" eine konsistente, kompakte SQLite-
// Kopie in einer temporären Datei und gibt deren Pfad zurück. Der Aufrufer ist
// für os.Remove zuständig.
//
// F-234 (Sprint 28): Vor dem ro-VACUUM-INTO wird auf einer separaten rw-
// Verbindung ein wal_checkpoint(TRUNCATE) ausgeführt. Ohne diesen Schritt
// leben uncommittete Writes im -wal-Sidecar — der Snapshot ist konsistent,
// aber „letzte Sekunden vor Backup" könnten fehlen. Der Checkpoint zwingt
// alle WAL-Frames in die Haupt-DB, bevor wir sie kopieren.
func vacuumIntoCopy(ctx context.Context, srcPath string) (string, error) {
	tmp, err := os.CreateTemp("", "aboard-backup-*.db")
	if err != nil {
		return "", fmt.Errorf("tempfile anlegen: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	// Datei muss leer sein bevor VACUUM INTO schreibt.
	os.Remove(tmpPath)

	// F-234: WAL-Checkpoint vor dem Backup. Best-effort — bei Fehler
	// (z.B. read-only-Filesystem) loggen wir, brechen aber nicht ab.
	checkpointDSN := srcPath + "?_pragma=busy_timeout(10000)"
	if cpDB, cpErr := sql.Open("sqlite", checkpointDSN); cpErr == nil {
		_, _ = cpDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
		cpDB.Close()
	}

	dsn := srcPath + "?_pragma=busy_timeout(10000)&mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", fmt.Errorf("DB öffnen: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", tmpPath); err != nil {
		return "", fmt.Errorf("VACUUM INTO: %w", err)
	}
	return tmpPath, nil
}

// readUserVersion liest PRAGMA user_version aus einer SQLite-Datei.
func readUserVersion(dbPath string) (int, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// writeTarGz erzeugt das .tar.gz-Archiv mit meta.json, DB-Kopie und Attachments.
func writeTarGz(outPath, dbCopy, attachmentsDir string, meta Meta) error {
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("Archiv-Datei erstellen: %w", err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// meta.json
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("meta.json kodieren: %w", err)
	}
	if err := addBytes(tw, "meta.json", metaJSON); err != nil {
		return err
	}

	// db/aboard.db
	if err := addFile(tw, "db/aboard.db", dbCopy); err != nil {
		return err
	}

	// attachments/… (Verzeichnis kann fehlen wenn noch keine Attachments existieren)
	if _, err := os.Stat(attachmentsDir); err == nil {
		if err := addDir(tw, "attachments", attachmentsDir); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("tar schließen: %w", err)
	}
	return gz.Close()
}

// addBytes schreibt einen Byte-Slice als Tar-Entry.
func addBytes(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(data)),
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("tar write %s: %w", name, err)
	}
	return nil
}

// addFile fügt eine einzelne Datei als Tar-Entry ein.
func addFile(tw *tar.Writer, tarName, filePath string) error {
	fi, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", filePath, err)
	}
	hdr := &tar.Header{
		Name:    tarName,
		Mode:    0o644,
		Size:    fi.Size(),
		ModTime: fi.ModTime().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header %s: %w", tarName, err)
	}
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("öffnen %s: %w", filePath, err)
	}
	defer f.Close()
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("kopieren %s: %w", filePath, err)
	}
	return nil
}

// addDir fügt ein Verzeichnis rekursiv als Tar-Einträge ein.
// tarBase ist der Pfad-Prefix im Archiv (z. B. "attachments").
func addDir(tw *tar.Writer, tarBase, srcDir string) error {
	return filepath.Walk(srcDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		tarPath := filepath.Join(tarBase, rel)
		// Trennzeichen im Tar-Format ist immer "/" (POSIX).
		tarPath = strings.ReplaceAll(tarPath, string(filepath.Separator), "/")

		if fi.IsDir() {
			hdr := &tar.Header{
				Typeflag: tar.TypeDir,
				Name:     tarPath + "/",
				Mode:     0o755,
				ModTime:  fi.ModTime().UTC(),
			}
			return tw.WriteHeader(hdr)
		}
		hdr := &tar.Header{
			Name:    tarPath,
			Mode:    0o644,
			Size:    fi.Size(),
			ModTime: fi.ModTime().UTC(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

// isDir gibt true zurück, wenn path ein existierendes Verzeichnis ist.
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// applyRetention bereinigt alte aboard-YYYY-MM-DD.tar.gz-Dateien im outDir
// nach den angegebenen Retention-Regeln.
//
// Logik:
//   - Alle Dateien die dem Muster "aboard-YYYY-MM-DD.tar.gz" entsprechen, nach
//     Datum absteigend sortieren.
//   - Die ersten keepDaily Dateien behalten (jüngste Tage).
//   - Von den restlichen: wöchentlich (Montag) keepWeekly Dateien behalten.
//   - Alle anderen löschen.
func applyRetention(outDir string, keepDaily, keepWeekly int) error {
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return fmt.Errorf("retention: Verzeichnis lesen: %w", err)
	}

	type archiveEntry struct {
		name string
		date time.Time
	}
	var archives []archiveEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "aboard-") || !strings.HasSuffix(name, ".tar.gz") {
			continue
		}
		// aboard-YYYY-MM-DD.tar.gz → Datum extrahieren
		dateStr := strings.TrimSuffix(strings.TrimPrefix(name, "aboard-"), ".tar.gz")
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue // unbekanntes Format → ignorieren
		}
		archives = append(archives, archiveEntry{name: name, date: t})
	}

	// Absteigend nach Datum sortieren (jüngste zuerst).
	sort.Slice(archives, func(i, j int) bool {
		return archives[i].date.After(archives[j].date)
	})

	keep := make(map[string]bool)

	// Tägliche Retention: die ersten keepDaily behalten.
	for i, a := range archives {
		if i < keepDaily {
			keep[a.name] = true
		}
	}

	// Wöchentliche Retention: aus dem Rest keepWeekly Montage-Einträge behalten.
	if keepWeekly > 0 {
		weeklyCount := 0
		for _, a := range archives {
			if keep[a.name] {
				continue
			}
			if a.date.Weekday() == time.Monday {
				keep[a.name] = true
				weeklyCount++
				if weeklyCount >= keepWeekly {
					break
				}
			}
		}
	}

	// Nicht-behaltene Dateien löschen.
	for _, a := range archives {
		if keep[a.name] {
			continue
		}
		path := filepath.Join(outDir, a.name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("retention: löschen %s: %w", a.name, err)
		}
		log.Printf("backup: Retention — gelöscht: %s", a.name)
	}

	return nil
}
