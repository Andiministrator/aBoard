package boardbackup

// rotation.go — Backup-Rotation pro Board (#303, ADR-0098).
//
// Behält die N jüngsten Backup-Files je Board-Verzeichnis. Ältere Files
// werden gelöscht. Globbing: <baseDir>/<boardId>/boardbackup-<boardId>-*.tar.gz
// bzw. …tar.gz.enc (#336) — siehe istBackupDatei.
//
// Die Sortierung erfolgt nach mtime (neuste zuerst) — robust gegen Clock-
// Skew, weil das OS-mtime gesetzt wird. Filename-basierte Sortierung wäre
// fragil, falls zwei Backups innerhalb derselben Sekunde laufen.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/uhr"
)

// Rotate räumt das Verzeichnis <baseDir>/<boardID>/ auf und liefert die
// Anzahl gelöschter Dateien. keepDays<=0 → kein Eingriff.
//
// BEHALTEN wird, was MINDESTENS EINE der beiden Bedingungen erfüllt:
//   - jünger als keepDays Tage (das ist die Zusage aus ADR-0098 §7:
//     „7 Tage rollend"), ODER
//   - unter den keepDays jüngsten Dateien (Mindestbestand, falls länger
//     nichts gesichert wurde — sonst stünde ein Board nach einer Pause
//     ganz ohne Sicherung da).
//
// WARUM DAS GEÄNDERT WURDE (Sprint-58-Kritiker, K1): Vorher zählte nur die
// zweite Bedingung — „behalte die 7 jüngsten Dateien". Das trug, solange
// ausschliesslich der nächtliche Lauf schrieb. Karte #335 hat den Knopf
// „Jetzt sichern" jedem Board-Eigentümer in die Hand gegeben, und das
// Rate-Limit erlaubt zehn davon pro Stunde: **Sieben Klicks löschten die
// gesamte Woche** und ersetzten sie durch sieben fast identische Snapshots
// derselben Minute. Das Wiederherstellungsfenster fiel von sieben Tagen auf
// eine Minute — ohne Warnung, ohne Bestätigung, ohne Log-Zeile.
//
// Die Konfiguration hiess dabei die ganze Zeit `keep_days` und die ADR sprach
// von Tagen; nur der Code zählte Dateien. Der Name war richtig, die
// Umsetzung nicht.
//
// Preis der Änderung: Bei vielen manuellen Sicherungen liegen jetzt mehr
// Dateien als vorher — das ist der bewusst gewählte Tausch. Platz ist
// nachschaffbar, ein überschriebenes Wiederherstellungsfenster nicht.
//
// UHR (Session-Kritiker Sprint 74, F-1201): Die Rotation rechnet das Fenster
// gegen die Systemuhr. Mit vorgehender Uhr löschte sie alles jenseits des
// Mindestbestands, was in Wahrheit noch im Fenster liegt. Deshalb rotiert
// sie nur mit gestellter Uhr (server/uhr) — hier drin, damit Nachtlauf und
// „Jetzt sichern" beide gedeckt sind. Grenze: Eine Sicherung, die bei falscher
// Uhr geschrieben wurde, trägt eine falsche mtime; der Mindestbestand (die
// keepDays jüngsten) federt das ab, beseitigt es nicht.
func Rotate(baseDir, boardID string, keepDays int) (int, error) {
	if keepDays <= 0 {
		return 0, nil
	}
	if !uhr.Erlaubt("board-backup-rotate", nil) {
		return 0, nil
	}
	dir := filepath.Join(baseDir, boardID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // noch nichts da, nichts zu rotieren
		}
		return 0, fmt.Errorf("rotate: read %s: %w", dir, err)
	}

	type entry struct {
		name  string
		path  string
		mtime int64
	}
	var files []entry
	prefix := "boardbackup-" + boardID + "-"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !istBackupDatei(name, prefix) {
			continue // andere Files unangetastet lassen
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, entry{
			name:  name,
			path:  filepath.Join(dir, name),
			mtime: fi.ModTime().UnixNano(),
		})
	}

	// Absteigend nach mtime (neuste zuerst).
	sort.Slice(files, func(i, j int) bool { return files[i].mtime > files[j].mtime })

	grenze := nowFunc().Add(-time.Duration(keepDays) * 24 * time.Hour).UnixNano()
	deleted := 0
	behalten := 0
	for _, f := range files {
		if !istUnfertig(f.name) {
			if behalten < keepDays {
				behalten++
				continue // Mindestbestand: die keepDays jüngsten bleiben immer
			}
			if f.mtime >= grenze {
				continue // jünger als das Fenster — die eigentliche Zusage
			}
		} else if f.mtime >= grenze {
			// Unfertiges bleibt liegen, solange es frisch ist: es könnte die
			// Sicherung sein, die GERADE geschrieben wird. Es zählt aber
			// nicht zum Mindestbestand.
			continue
		}
		if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
			return deleted, fmt.Errorf("rotate: remove %s: %w", f.name, err)
		}
		deleted++
	}
	return deleted, nil
}

// ListBackups liefert alle Backup-Files eines Boards, sortiert nach mtime
// (neuste zuerst). Wird vom REST-Handler GET /boards/:id/backup/files benutzt.
func ListBackups(baseDir, boardID string) ([]BackupInfo, error) {
	dir := filepath.Join(baseDir, boardID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []BackupInfo{}, nil
		}
		return nil, fmt.Errorf("list: read %s: %w", dir, err)
	}

	prefix := "boardbackup-" + boardID + "-"
	var out []BackupInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !istBackupDatei(name, prefix) || istUnfertig(name) {
			continue /* K2: Trümmerstücke sind keine Sicherungen */
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{
			Filename:  name,
			Size:      fi.Size(),
			ModTime:   fi.ModTime().Format("2006-01-02T15:04:05Z"),
			Encrypted: IsEncryptedName(name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime > out[j].ModTime })
	return out, nil
}

// istBackupDatei entscheidet, ob ein Dateiname zu diesem Board gehört.
//
// #336: Die Endung `.tar.gz.enc` MUSS hier mitgeführt werden. Täte sie es
// nicht, wäre der Effekt doppelt und beides Mal still: verschlüsselte
// Backups würden nie rotiert (die Platte läuft voll, ohne dass jemand einen
// Fehler sieht) und nie gelistet (der Board-Owner sieht ein leeres
// Backup-Register, obwohl Backups laufen). Ein Suffix-Vergleich, der eine
// neue Dateiendung nicht kennt, meldet keinen Fehler — er findet nur nichts.
func istBackupDatei(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	return strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tar.gz"+EncSuffix)
}

// istUnfertig erkennt den Zwischennamen, unter dem eine Sicherung geschrieben
// wird, bevor sie auf ihren endgültigen Namen umbenannt wird.
//
// WARUM (Sprint-58-Kritiker, K2): Der Zwischenname existiert erklärtermassen,
// „damit eine abgebrochene Sicherung nicht wie eine fertige aussieht" — aber
// niemand wertete ihn aus. Bricht der Container mitten im nächtlichen Lauf ab,
// bleibt ein halb geschriebenes `…-pending.tar.gz` liegen; es erschien dem
// Board-Eigentümer in der Liste aus Karte #335 als gültige Sicherung,
// verdrängte beim Rotieren eine gute ältere und schlug beim Wiederherstellen
// mit „beschädigt" fehl — genau dann, wenn er darauf angewiesen ist.
//
// Behandlung: aus der LISTE raus (es ist keine Sicherung), in der ROTATION
// drin (es soll weggeräumt werden), und ohne Anspruch auf den Mindestbestand
// (ein Trümmerstück darf keine gute Sicherung verdrängen).
func istUnfertig(name string) bool {
	return strings.Contains(name, "-pending.tar.gz")
}

// BackupInfo ist die schmale Sicht für die Backup-Liste — bewusst ohne
// SHA-Read (würde ein Archiv-Open pro Eintrag bedeuten, teuer bei vielen).
// Die SHA steht im meta.json und ist beim Download/Restore verifizierbar.
type BackupInfo struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	ModTime  string `json:"mtime"`
	// Encrypted wird aus der Dateiendung abgeleitet (nicht aus dem Inhalt —
	// die Liste öffnet bewusst keine Archive, siehe Kommentar oben). Für die
	// Anzeige genügt das; die inhaltliche Entscheidung trifft der
	// Restore-Pfad über das Magic.
	Encrypted bool `json:"encrypted"`
}
