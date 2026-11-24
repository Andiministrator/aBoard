package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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

// RestoreOptions bündelt alle Restore-Parameter.
type RestoreOptions struct {
	// ArchivePath ist der Pfad zum .tar.gz-Archiv (Pflichtfeld).
	ArchivePath string
	// DataDir ist das Ziel-Datenverzeichnis. Es wird erwartet, dass darin
	// db/ und attachments/ liegen (oder angelegt werden).
	// Default: /data
	DataDir string
	// DBPath überschreibt den Default-Pfad zur Datenbank (<DataDir>/db/aboard.db).
	// Leer = Default benutzen.
	DBPath string
	// AttachmentsDir überschreibt das Default-Attachments-Verzeichnis (<DataDir>/attachments).
	// Leer = Default benutzen.
	AttachmentsDir string
	// DryRun: Wenn true, wird nichts geschrieben — nur Ausgabe was passieren würde.
	DryRun bool
	// Force: Wenn true, werden existierende DB/Attachments überschrieben.
	// Wenn false und das Ziel nicht leer ist, bricht Restore mit einem Fehler ab.
	Force bool
	// MaxSchemaVersion ist die höchste akzeptierte schema_version im Archiv.
	// 0 = keine Prüfung (nur für Tests). In der CLI wird RunMigrations-Version gesetzt.
	MaxSchemaVersion int
	// FremdeAnhaenge bekommt die Anhänge (relativ zum Attachments-Verzeichnis),
	// die nach dem Restore dort liegen, aber nicht im Archiv standen (F-1859).
	// nil = nur loggen. Gelöscht wird nichts.
	FremdeAnhaenge func(rel []string)
}

// Restore spielt ein Backup-Archiv zurück.
func Restore(ctx context.Context, opts RestoreOptions) error {
	if opts.ArchivePath == "" {
		return fmt.Errorf("restore: ArchivePath ist leer")
	}

	// Ziel-Pfade bestimmen.
	dbPath := opts.DBPath
	if dbPath == "" {
		dataDir := opts.DataDir
		if dataDir == "" {
			dataDir = "/data"
		}
		dbPath = filepath.Join(dataDir, "db", "aboard.db")
	}
	attachDir := opts.AttachmentsDir
	if attachDir == "" {
		dataDir := opts.DataDir
		if dataDir == "" {
			dataDir = "/data"
		}
		attachDir = filepath.Join(dataDir, "attachments")
	}

	// Archiv öffnen und meta.json lesen (erster Pass).
	meta, err := readMetaFromArchive(opts.ArchivePath)
	if err != nil {
		return fmt.Errorf("restore: meta.json lesen: %w", err)
	}

	// Schema-Version validieren.
	if opts.MaxSchemaVersion > 0 && meta.SchemaVersion > opts.MaxSchemaVersion {
		return fmt.Errorf(
			"restore: Archiv-Schema-Version %d ist höher als Server-Version %d — Restore abgebrochen",
			meta.SchemaVersion, opts.MaxSchemaVersion,
		)
	}

	log.Printf("restore: Archiv vom %s (schema_version=%d, aboard=%s)",
		meta.CreatedAt, meta.SchemaVersion, meta.AboardVersion)

	if opts.DryRun {
		return dryRun(opts.ArchivePath, dbPath, attachDir)
	}

	// Ziel-Verzeichnisse prüfen / anlegen.
	if err := checkDestination(dbPath, attachDir, opts.Force); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("restore: DB-Verzeichnis anlegen: %w", err)
	}
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		return fmt.Errorf("restore: Attachments-Verzeichnis anlegen: %w", err)
	}

	// Archiv extrahieren. Die alte WAL/SHM legt der Eintrag db/aboard.db
	// unmittelbar vor dem Schreiben beiseite (F-1859, siehe extractEntry).
	imArchiv, err := extractArchive(opts.ArchivePath, dbPath, attachDir)
	if err != nil {
		return fmt.Errorf("restore: Archiv extrahieren: %w", err)
	}

	// F-1859: Anhänge, die nicht im Archiv standen, bleiben liegen, werden
	// aber genannt — sie gehören zu keiner Karte der zurückgespielten DB.
	fremd, err := fremdeAnhaenge(attachDir, imArchiv)
	if err != nil {
		log.Printf("restore: WARNUNG: Anhänge außerhalb des Archivs nicht prüfbar: %v", err)
	} else if len(fremd) > 0 {
		log.Printf("restore: WARNUNG: %d Anhang/Anhänge in %s stehen nicht im Archiv und bleiben liegen (nichts gelöscht), z. B. %s",
			len(fremd), attachDir, fremd[0])
		if opts.FremdeAnhaenge != nil {
			opts.FremdeAnhaenge(fremd)
		}
	}

	log.Printf("restore: Abgeschlossen — DB: %s, Attachments: %s", dbPath, attachDir)
	return nil
}

// readMetaFromArchive öffnet das Archiv und liest nur meta.json.
func readMetaFromArchive(archivePath string) (*Meta, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("Archiv öffnen: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip lesen: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar lesen: %w", err)
		}
		if hdr.Name != "meta.json" {
			continue
		}
		var meta Meta
		if err := json.NewDecoder(tr).Decode(&meta); err != nil {
			return nil, fmt.Errorf("meta.json dekodieren: %w", err)
		}
		return &meta, nil
	}
	return nil, fmt.Errorf("meta.json nicht im Archiv gefunden")
}

// dryRun gibt aus, was ein echter Restore tun würde.
func dryRun(archivePath, dbPath, attachDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("Archiv öffnen: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip lesen: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	fmt.Printf("[dry-run] Restore von: %s\n", archivePath)
	fmt.Printf("[dry-run] Ziel-DB:           %s\n", dbPath)
	fmt.Printf("[dry-run] Ziel-Attachments:  %s\n", attachDir)
	fmt.Printf("[dry-run] Archiv-Inhalt:\n")

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar lesen: %w", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			fmt.Printf("[dry-run]   Verzeichnis: %s\n", hdr.Name)
		} else {
			fmt.Printf("[dry-run]   Datei: %s (%d Bytes)\n", hdr.Name, hdr.Size)
		}
	}
	return nil
}

// checkDestination prüft ob das Ziel leer ist (oder --force gesetzt).
func checkDestination(dbPath, attachDir string, force bool) error {
	if force {
		return nil
	}
	// DB vorhanden?
	if _, err := os.Stat(dbPath); err == nil {
		return fmt.Errorf(
			"restore: Ziel-DB existiert bereits (%s) — verwende --force zum Überschreiben",
			dbPath,
		)
	}
	// Attachments-Verzeichnis nicht leer?
	entries, err := os.ReadDir(attachDir)
	if err == nil && len(entries) > 0 {
		return fmt.Errorf(
			"restore: Attachments-Verzeichnis ist nicht leer (%s) — verwende --force zum Überschreiben",
			attachDir,
		)
	}
	return nil
}

// Nähte für Tests (F-1900): Die frühen Fehler in extractEntry vor dem
// Kürzen der alten DB lassen sich sonst nur über Platte oder Rechte
// erzwingen. Im Betrieb sind es die echten Funktionen.
var (
	erzeugeZwischendatei = os.CreateTemp
	synceZwischendatei   = func(f *os.File) error { return f.Sync() }
	beilagenBeiseite     = legeSQLiteBeilagenBeiseite
)

// legeSQLiteBeilagenBeiseite benennt <db>-wal und <db>-shm in
// <name>.vor-restore-<zeit> um, falls vorhanden (F-1859).
func legeSQLiteBeilagenBeiseite(dbPath string) error {
	// Nanosekunden: zwei Läufe in derselben Sekunde überschreiben einander
	// die beiseitegelegte Datei nicht (Kritiker S-3).
	stempel := time.Now().UTC().Format("20060102T150405.000000000Z")
	for _, endung := range []string{"-wal", "-shm"} {
		alt := dbPath + endung
		if _, err := os.Lstat(alt); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("restore: %s prüfen: %w", alt, err)
		}
		neu := alt + ".vor-restore-" + stempel
		if err := os.Rename(alt, neu); err != nil {
			return fmt.Errorf("restore: %s beiseitelegen: %w", alt, err)
		}
		log.Printf("restore: %s aus dem alten Bestand beiseitegelegt → %s", alt, neu)
	}
	return nil
}

// fremdeAnhaenge listet Dateien unter attachDir (relativ, mit /), die nicht
// in imArchiv stehen — sortiert.
func fremdeAnhaenge(attachDir string, imArchiv map[string]bool) ([]string, error) {
	// Kritiker S-4: Ist das Verzeichnis selbst ein Symlink, folgt WalkDir der
	// Wurzel nicht und meldete „.“ als fremden Anhang. Deshalb die Wurzel
	// vorher auflösen (Symlinks DARIN bleiben ungefolgt).
	wurzel, err := filepath.EvalSymlinks(attachDir)
	if err != nil {
		return nil, err
	}
	var fremd []string
	err = filepath.WalkDir(wurzel, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(wurzel, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !imArchiv[rel] {
			fremd = append(fremd, rel)
		}
		return nil
	})
	sort.Strings(fremd)
	return fremd, err
}

// extractArchive entpackt das Archiv in die Zielpfade.
// db/aboard.db → dbPath; attachments/… → attachDir/<rest>.
// Rückgabe: die geschriebenen Anhang-Dateien (relativ zu attachDir, mit /).
func extractArchive(archivePath, dbPath, attachDir string) (map[string]bool, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("Archiv öffnen: %w", err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("gzip lesen: %w", err)
	}
	defer gr.Close()

	imArchiv := map[string]bool{}
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar lesen: %w", err)
		}

		if err := extractEntry(tr, hdr, dbPath, attachDir); err != nil {
			return nil, fmt.Errorf("Eintrag %s extrahieren: %w", hdr.Name, err)
		}
		if rel, ok := strings.CutPrefix(hdr.Name, "attachments/"); ok && hdr.Typeflag != tar.TypeDir && rel != "" {
			imArchiv[rel] = true
		}
	}
	return imArchiv, nil
}

// extractEntry verarbeitet einen einzelnen Tar-Entry.
//
// F-236 (Sprint 28): Defense-in-depth gegen Path-Traversal. Symlinks,
// Hard-Links und absolute/Windows-Pfade werden explizit abgelehnt — die
// vorhandene Whitelist (db/aboard.db, attachments/*) dämmt das im Normalfall
// schon ein, aber ein zukünftiger Whitelist-Eintrag oder ein Bug könnte
// sonst durchschlagen.
func extractEntry(tr *tar.Reader, hdr *tar.Header, dbPath, attachDir string) error {
	name := hdr.Name
	// F-236: keine Path-Traversal-Patterns.
	if strings.Contains(name, "..") {
		return fmt.Errorf("unsicherer Tar-Pfad (..): %s", name)
	}
	// F-236: keine absoluten oder Windows-Pfade.
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return fmt.Errorf("unsicherer Tar-Pfad (absolut): %s", name)
	}
	// F-236: nach Cleanup darf sich der normalisierte Pfad nicht verändert
	// haben. Trailing-Slash bei Dir-Einträgen ist erlaubt — wird vor dem
	// Vergleich entfernt.
	cleaned := filepath.ToSlash(filepath.Clean(name))
	stripped := strings.TrimSuffix(name, "/")
	if cleaned != stripped && name != "." {
		return fmt.Errorf("unsicherer Tar-Pfad (clean-Drift): %s", name)
	}
	// F-236: keine Symlinks oder Hard-Links — wir extrahieren nur normale
	// Files und Directories.
	if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
		return fmt.Errorf("unsicherer Tar-Eintrag (symlink/hardlink): %s", name)
	}

	switch {
	case name == "meta.json":
		// meta.json wird nur zur Validierung gelesen, nicht extrahiert.
		return nil

	case name == "db/aboard.db":
		// F-1859: Eine WAL des alten Bestands (harter Abbruch des Servers)
		// würde SQLite beim nächsten Öffnen auf die zurückgespielte Datei
		// anwenden — gemessen: danach steht wieder der ALTE Inhalt da. Deshalb
		// beiseitelegen (umbenennen, nicht löschen) — und zwar ERST HIER, direkt
		// vor dem Schreiben der DB: Scheitert das Archiv vorher (tar-Fehler,
		// unsicherer Pfad), bleibt die alte DB samt WAL unberührt (Kritiker S-3).
		// Verify W-2: Die Zieldatei wird vorher OHNE Kürzen geöffnet — ist sie
		// nicht beschreibbar, scheitert das hier, und die WAL bleibt an ihrem
		// Platz. Gekürzt wird erst nach dem Beiseitelegen.
		//
		// F-1888 (Sprint-86-Kritiker K-B2): Erst vollständig in eine
		// Zwischendatei im selben Ordner — ein abgeschnittenes Archiv oder
		// eine volle Platte scheitert dann, BEVOR die alte DB gekürzt ist.
		// Danach wird am Ort gekürzt und überschrieben (kein Umbenennen: die
		// DB kann eine einzeln eingehängte Datei sein). Scheitert erst dieser
		// zweite Schritt, bleibt die Zwischendatei liegen und die Meldung
		// nennt sie — dann ist sie der einzige vollständige Stand.
		_, statErr := os.Stat(dbPath)
		neuAngelegt := errors.Is(statErr, os.ErrNotExist)
		f, err := os.OpenFile(dbPath, os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			return fmt.Errorf("öffnen %s: %w", dbPath, err)
		}
		defer f.Close()
		// Verify V-2: Gab es vorher keine DB, bleibt nach einem frühen Fehler
		// auch keine leere zurück (der Server legte sonst still eine neue an).
		fruehAufraeumen := func() {
			if neuAngelegt {
				_ = f.Close()
				_ = os.Remove(dbPath)
			}
		}
		zwischen, err := erzeugeZwischendatei(filepath.Dir(dbPath), filepath.Base(dbPath)+".restore-*")
		if err != nil {
			fruehAufraeumen()
			return fmt.Errorf("Zwischendatei neben %s: %w", dbPath, err)
		}
		zwName := zwischen.Name()
		defer zwischen.Close()
		if _, err := io.Copy(zwischen, tr); err != nil {
			_ = zwischen.Close()
			_ = os.Remove(zwName)
			fruehAufraeumen()
			return fmt.Errorf("lesen db/aboard.db aus dem Archiv: %w — die bisherige Datenbank ist unverändert", err)
		}
		if err := synceZwischendatei(zwischen); err != nil {
			_ = zwischen.Close()
			_ = os.Remove(zwName)
			fruehAufraeumen()
			return fmt.Errorf("Zwischendatei %s: %w — die bisherige Datenbank ist unverändert", zwName, err)
		}
		if err := beilagenBeiseite(dbPath); err != nil {
			_ = zwischen.Close()
			_ = os.Remove(zwName)
			fruehAufraeumen()
			return err
		}
		if err := f.Truncate(0); err != nil {
			return fmt.Errorf("kürzen %s: %w (vollständiger Stand liegt in %s)", dbPath, err, zwName)
		}
		if _, err := zwischen.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("%s: %w — %s ist jetzt UNBRAUCHBAR, vollständiger Stand liegt in %s", zwName, err, dbPath, zwName)
		}
		if _, err := io.Copy(f, zwischen); err != nil {
			return fmt.Errorf("schreiben %s: %w — die Datei ist jetzt UNBRAUCHBAR, vollständiger Stand liegt in %s", dbPath, err, zwName)
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("schreiben %s: %w — vollständiger Stand liegt in %s", dbPath, err, zwName)
		}
		_ = zwischen.Close()
		_ = os.Remove(zwName)
		return nil

	case strings.HasPrefix(name, "attachments/"):
		rel := strings.TrimPrefix(name, "attachments/")
		if rel == "" || rel == "/" {
			return nil // Wurzel-Verzeichnis
		}
		dest := filepath.Join(attachDir, filepath.FromSlash(rel))
		if hdr.Typeflag == tar.TypeDir {
			return os.MkdirAll(dest, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return writeFile(tr, dest, hdr.Size)

	default:
		// Unbekannte Einträge werden ignoriert (Vorwärts-Kompatibilität).
		return nil
	}
}

// writeFile schreibt den aktuellen Tar-Entry-Inhalt in eine Datei.
func writeFile(tr *tar.Reader, destPath string, _ int64) error {
	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("erstellen %s: %w", destPath, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, tr); err != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("schreiben %s: %w", destPath, err)
	}
	return nil
}
