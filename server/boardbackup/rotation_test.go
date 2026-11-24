package boardbackup_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// makeBackupFiles legt count Test-Files an, EINEN PRO TAG, ältestes zuerst.
//
// Der Abstand ist bewusst ein Tag und nicht — wie bis Sprint 58 — eine
// Stunde: `Rotate` hält seit dem Sprint-58-Kritiker (K1) ein Fenster in
// TAGEN, so wie ADR-0098 §7 es immer schon zugesagt hat. Stundenweise
// gestaffelte Dateien liegen alle innerhalb desselben Tages und werden
// korrekt nie gelöscht — ein Test darauf hätte nur noch bewiesen, dass die
// alte, defekte Zählweise erhalten bleibt.
func makeBackupFiles(t *testing.T, dir, boardID string, count int) []string {
	t.Helper()
	return makeBackupFilesAbstand(t, dir, boardID, count, 24*time.Hour)
}

// makeBackupFilesAbstand ist die Variante mit wählbarem Abstand — gebraucht
// für den Fall „mehrere Sicherungen innerhalb einer Minute".
func makeBackupFilesAbstand(t *testing.T, dir, boardID string, count int, abstand time.Duration) []string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	base := time.Now().Add(-time.Duration(count) * abstand)
	paths := make([]string, count)
	for i := 0; i < count; i++ {
		ts := base.Add(time.Duration(i) * abstand).UTC()
		name := "boardbackup-" + boardID + "-" + ts.Format("20060102-150405") + "-aabbccdd.tar.gz"
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte{0}, 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", p, err)
		}
		// mtime explizit setzen, sonst sind alle Files quasi gleich-alt.
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
		paths[i] = p
	}
	return paths
}

// 10 tägliche Sicherungen, Fenster 7 Tage → die drei ältesten fallen weg.
func TestRotation_KeepsN(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "b1")
	paths := makeBackupFiles(t, dir, "b1", 10)

	deleted, err := boardbackup.Rotate(base, "b1", 7)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if deleted != 3 {
		t.Errorf("erwartet 3 deleted, got %d", deleted)
	}

	// Die 3 ältesten Files (Index 0,1,2) sind älter als 7 Tage → weg.
	for i, p := range paths {
		_, err := os.Stat(p)
		if i < 3 {
			if !os.IsNotExist(err) {
				t.Errorf("File %d sollte gelöscht sein: %v", i, err)
			}
		} else {
			if err != nil {
				t.Errorf("File %d sollte bleiben: %v", i, err)
			}
		}
	}
}

// AK_rotation_other_board_untouched: Files in Sub-Dir eines anderen
// Boards bleiben unangetastet.
func TestRotation_OtherBoardUntouched(t *testing.T) {
	base := t.TempDir()
	makeBackupFiles(t, filepath.Join(base, "b1"), "b1", 10)
	makeBackupFiles(t, filepath.Join(base, "b2"), "b2", 5)

	if _, err := boardbackup.Rotate(base, "b1", 3); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// b2 muss komplett unangetastet sein.
	entries, _ := os.ReadDir(filepath.Join(base, "b2"))
	if len(entries) != 5 {
		t.Errorf("b2: erwartet 5 Files, got %d", len(entries))
	}

	// b1: 3 Files
	entries1, _ := os.ReadDir(filepath.Join(base, "b1"))
	if len(entries1) != 3 {
		t.Errorf("b1: erwartet 3 Files, got %d", len(entries1))
	}
}

// Test ListBackups: liefert sortiert nach mtime desc.
func TestRotation_ListBackups(t *testing.T) {
	base := t.TempDir()
	makeBackupFiles(t, filepath.Join(base, "b1"), "b1", 5)

	files, err := boardbackup.ListBackups(base, "b1")
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(files) != 5 {
		t.Fatalf("erwartet 5, got %d", len(files))
	}
	// Sortierung: mtime desc → die jüngsten zuerst (mtime-String compare).
	mtimes := make([]string, len(files))
	for i, f := range files {
		mtimes[i] = f.ModTime
	}
	if !sort.SliceIsSorted(files, func(i, j int) bool { return files[i].ModTime > files[j].ModTime }) {
		t.Errorf("Sortierung mtime desc verletzt: %v", mtimes)
	}
}

// Test: andere Files (z. B. ein README) im Verzeichnis bleiben unangetastet.
func TestRotation_IgnoresUnrelatedFiles(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "b1")
	makeBackupFiles(t, dir, "b1", 3)
	// Ein anderes File reinwerfen
	other := filepath.Join(dir, "README.txt")
	_ = os.WriteFile(other, []byte("hi"), 0o644)

	if _, err := boardbackup.Rotate(base, "b1", 1); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	if _, err := os.Stat(other); err != nil {
		t.Errorf("Unrelated file wurde gelöscht: %v", err)
	}
}

// keep=0 → Rotation greift nicht.
func TestRotation_KeepZeroNoop(t *testing.T) {
	base := t.TempDir()
	makeBackupFiles(t, filepath.Join(base, "b1"), "b1", 5)
	deleted, err := boardbackup.Rotate(base, "b1", 0)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if deleted != 0 {
		t.Errorf("keep=0 sollte 0 löschen, got %d", deleted)
	}
}

// K1 (Sprint-58-Kritiker) — der Fall, den Karte #335 erst erreichbar gemacht
// hat: Der Board-Eigentümer bekam mit der neuen Backup-Sektion den Knopf
// „Jetzt sichern", und das Rate-Limit erlaubt zehn davon pro Stunde.
//
// Unter der alten Zählweise („behalte die 7 jüngsten DATEIEN") löschten
// sieben Klicks die gesamte Woche und ersetzten sie durch sieben fast
// identische Snapshots derselben Minute — das Wiederherstellungsfenster fiel
// von sieben Tagen auf eine Minute, ohne Warnung und ohne Log-Zeile.
//
// Dieser Test wäre mit dem alten Code rot: dort blieben nach der Rotation
// genau die sieben manuellen übrig.
func TestRotation_ManuelleSicherungenLoeschenDieWocheNicht(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "b1")

	// Sieben nächtliche Sicherungen, einen Tag auseinander.
	naechtlich := makeBackupFiles(t, dir, "b1", 7)
	// Sieben manuelle Klicks, alle innerhalb weniger Minuten (= heute).
	// WICHTIG: dieselbe Board-ID "b1" — mit einer anderen fielen die Dateien
	// aus dem Präfix-Filter von `istBackupDatei` und würden gar nicht
	// mitgezählt. Der Test wäre dann auch mit der alten, defekten Zählweise
	// grün (beim ersten Anlauf genau so passiert und von der Gegenprobe
	// aufgedeckt). Die Namen kollidieren nicht: der Zeitstempel im Dateinamen
	// hat Sekundenauflösung, der Abstand ist eine Minute.
	makeBackupFilesAbstand(t, dir, "b1", 7, time.Minute)

	vorher, _ := os.ReadDir(dir)
	if len(vorher) != 14 {
		t.Fatalf("Aufbau: erwartet 14 Dateien, gefunden %d", len(vorher))
	}

	// Fenster 8 Tage, nicht 7: die älteste der sieben nächtlichen Sicherungen
	// liegt sonst exakt auf der Fenstergrenze und fällt je nach Millisekunde
	// heraus. Geprüft werden soll die ZÄHLWEISE, nicht das Verhalten am
	// Randpunkt — ein Test, der an einer Millisekunde hängt, ist ein
	// Zufallsgenerator. Unter der alten Zählweise blieben bei 8 ohnehin nur
	// die acht jüngsten Dateien: sieben manuelle plus eine nächtliche.
	if _, err := boardbackup.Rotate(base, "b1", 8); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Die nächtlichen sind alle jünger als acht Tage → alle müssen bleiben.
	for i, p := range naechtlich {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("nächtliche Sicherung %d wurde gelöscht: %v", i, err)
		}
	}
}

// K2 (Sprint-58-Kritiker): Ein abgebrochener Lauf hinterlässt ein
// `…-pending.tar.gz`. Es darf dem Board-Eigentümer NICHT als gültige
// Sicherung angeboten werden — sonst hält er ein Trümmerstück für seine
// Rettung und merkt es erst beim Wiederherstellen.
func TestRotation_UnfertigesTaucthNichtInDerListeAuf(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "b1")
	makeBackupFiles(t, dir, "b1", 2)

	// Trümmerstück eines abgebrochenen Laufs danebenlegen.
	pending := filepath.Join(dir, "boardbackup-b1-20260826-020000-pending.tar.gz")
	if err := os.WriteFile(pending, []byte{0}, 0o644); err != nil {
		t.Fatal(err)
	}

	liste, err := boardbackup.ListBackups(base, "b1")
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(liste) != 2 {
		t.Fatalf("Liste hat %d Einträge, erwartet 2 — das unfertige ist mitgezählt", len(liste))
	}
	for _, e := range liste {
		if strings.Contains(e.Filename, "-pending") {
			t.Errorf("unfertige Sicherung in der Liste: %s", e.Filename)
		}
	}
}

// Und die Kehrseite: ein ALTES Trümmerstück darf keine gute Sicherung aus dem
// Mindestbestand verdrängen. Unter der alten Zählweise tat es genau das.
func TestRotation_AltesUnfertigesVerdraengtKeineGuteSicherung(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "b1")
	gute := makeBackupFiles(t, dir, "b1", 2) // vor 2 bzw. 1 Tag

	// Ein Trümmerstück von vor 30 Tagen — älter als jedes Fenster.
	alt := filepath.Join(dir, "boardbackup-b1-20260101-020000-pending.tar.gz")
	if err := os.WriteFile(alt, []byte{0}, 0o644); err != nil {
		t.Fatal(err)
	}
	tsAlt := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(alt, tsAlt, tsAlt); err != nil {
		t.Fatal(err)
	}

	if _, err := boardbackup.Rotate(base, "b1", 7); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := os.Stat(alt); !os.IsNotExist(err) {
		t.Errorf("altes Trümmerstück wurde nicht weggeräumt: %v", err)
	}
	for i, p := range gute {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("gute Sicherung %d wurde gelöscht: %v", i, err)
		}
	}
}

// TestRotation_WartetAufGestellteUhr (Session-Kritiker Sprint 74, F-1201):
// Mit nicht gestellter Uhr rotiert nichts — sonst löschte eine vorgehende Uhr,
// was noch im Fenster liegt. Beide Aufrufer (Nachtlauf, „Jetzt sichern")
// gehen durch Rotate, deshalb sitzt die Prüfung dort.
// GEGENPROBE: Ohne uhr.Erlaubt in Rotate löscht der erste Aufruf drei — rot.
func TestRotation_WartetAufGestellteUhr(t *testing.T) {
	base := t.TempDir()
	makeBackupFiles(t, filepath.Join(base, "b1"), "b1", 10)
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })

	uhr.Gestellt = func() (bool, error) { return false, nil }
	if n, err := boardbackup.Rotate(base, "b1", 7); err != nil || n != 0 {
		t.Fatalf("nicht gestellte Uhr: %d gelöscht (err %v), erwartet 0", n, err)
	}
	uhr.Gestellt = func() (bool, error) { return true, nil }
	if n, err := boardbackup.Rotate(base, "b1", 7); err != nil || n != 3 {
		t.Fatalf("gestellte Uhr: %d gelöscht (err %v), erwartet 3", n, err)
	}
}
