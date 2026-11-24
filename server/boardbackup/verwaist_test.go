package boardbackup_test

// verwaist_test.go — Backups gelöschter Boards abräumen (F-1125, Karte #545).
//
// Prüfbefehl aus F-1125: Board anlegen, sichern, löschen, Scheduler mit Uhr
// und Frist laufen lassen → Verzeichnis weg. Widerlegt wäre der Fix, wenn
// das Verzeichnis nach Löschung und Ablauf der Frist noch Backups enthält.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/uhr"
)

// laufUm führt einen Sicherungslauf mit der Scheduler-Uhr auf `um` aus und
// liefert die Log-Zeilen.
func laufUm(t *testing.T, env *testEnv, um time.Time) []string {
	t.Helper()
	var log []string
	boardbackup.BackupAllBoardsOnce(context.Background(), boardbackup.SchedulerDeps{
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		BackupBaseDir:      env.backupDir,
		KeepDays:           3,
		NowFn:              func() time.Time { return um },
		Logger: func(f string, a ...any) {
			log = append(log, f)
		},
	})
	return log
}

// loescheWieAPI tut, was DELETE /boards/{id} tut: Board aus der DB entfernen
// und den Grabstein setzen (main.go verdrahtet MarkiereGeloescht als
// NachDelete-Schritt der BoardsAPI).
func loescheWieAPI(t *testing.T, env *testEnv, id string, um time.Time) {
	t.Helper()
	if err := env.boards.Delete(context.Background(), id); err != nil {
		t.Fatalf("Delete %s: %v", id, err)
	}
	if err := boardbackup.MarkiereGeloescht(env.backupDir, id, um); err != nil {
		t.Fatalf("MarkiereGeloescht %s: %v", id, err)
	}
}

func existiert(pfad string) bool {
	_, err := os.Lstat(pfad)
	return err == nil
}

func backupsIn(t *testing.T, dir string) int {
	t.Helper()
	eintraege, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range eintraege {
		if strings.HasPrefix(e.Name(), "boardbackup-") {
			n++
		}
	}
	return n
}

// TestVerwaist_GeloeschtesBoard_NachFristWeg — der Prüfbefehl aus F-1125,
// einschliesslich des Fensters dazwischen, in dem eine Wiederherstellung
// noch möglich sein muss.
func TestVerwaist_GeloeschtesBoard_NachFristWeg(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "weg", "Wird gelöscht")
	env.seedBoard(t, "bleibt", "Bleibt")
	t0 := time.Now()

	laufUm(t, env, t0)
	wegDir := filepath.Join(env.backupDir, "weg")
	if backupsIn(t, wegDir) != 1 {
		t.Fatalf("Vorbedingung: erwartet 1 Backup von „weg“")
	}

	loescheWieAPI(t, env, "weg", t0.Add(1*time.Hour))

	// Erster Lauf nach dem Löschen: nichts entfernen.
	laufUm(t, env, t0.Add(1*time.Hour))
	if backupsIn(t, wegDir) != 1 {
		t.Fatalf("Backup schon beim ersten Lauf entfernt — kein Fenster für eine Wiederherstellung")
	}
	if !existiert(filepath.Join(wegDir, ".geloescht-seit")) {
		t.Fatalf("kein Grabstein vorhanden")
	}

	// Innerhalb der Frist (3 Tage ab Löschen): alles bleibt.
	laufUm(t, env, t0.Add(1*time.Hour+2*24*time.Hour))
	if backupsIn(t, wegDir) != 1 {
		t.Fatalf("Backup innerhalb der Frist entfernt")
	}

	// Nach der Frist: Verzeichnis weg. Seit F-1135 zählen auch die Läufe —
	// der erste nach dem Löschen setzt nur den Anker, danach braucht es
	// KeepDays (3) Nachtläufe im Abstand von mindestens 20 h.
	laufUm(t, env, t0.Add(1*time.Hour+3*24*time.Hour))
	laufUm(t, env, t0.Add(1*time.Hour+4*24*time.Hour))
	if existiert(wegDir) {
		t.Fatalf("Verzeichnis des gelöschten Boards besteht nach Ablauf der Frist noch (F-1125)")
	}
	// Das bestehende Board ist unberührt und wird weiter gesichert.
	if n := backupsIn(t, filepath.Join(env.backupDir, "bleibt")); n < 1 {
		t.Fatalf("Backups des bestehenden Boards fehlen: %d", n)
	}
	if existiert(filepath.Join(env.backupDir, "bleibt", ".geloescht-seit")) {
		t.Fatalf("bestehendes Board trägt einen Grabstein")
	}
}

// TestVerwaist_FrischeBackupsHaltenAuf — auch mit abgelaufener Markierung
// wird nichts entfernt, solange eine Backup-Datei jünger als die Frist ist.
func TestVerwaist_FrischeBackupsHaltenAuf(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "x", "X")
	t0 := time.Now()
	laufUm(t, env, t0)
	loescheWieAPI(t, env, "x", t0)
	dir := filepath.Join(env.backupDir, "x")
	// Markierung künstlich altern lassen, Backup bleibt frisch (mtime = jetzt).
	alt := t0.Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, ".geloescht-seit"), alt, alt); err != nil {
		t.Fatal(err)
	}
	laufUm(t, env, t0.Add(time.Hour))
	if backupsIn(t, dir) != 1 {
		t.Fatalf("frisches Backup trotz Frist entfernt")
	}
}

// TestVerwaist_FremdesVerzeichnisUnberuehrt — ein Verzeichnis ohne eigene
// Backup-Dateien (Backup-Root z. B. versehentlich auf /data gestellt) wird
// weder markiert noch angefasst, egal wie alt es ist.
func TestVerwaist_FremdesVerzeichnisUnberuehrt(t *testing.T) {
	env := newTestEnv(t)
	t0 := time.Now()
	fremd := filepath.Join(env.backupDir, "db")
	if err := os.MkdirAll(fremd, 0o755); err != nil {
		t.Fatal(err)
	}
	datei := filepath.Join(fremd, "aboard.db")
	if err := os.WriteFile(datei, []byte("wichtig"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Auch eine Backup-Datei eines ANDEREN Boards zählt nicht.
	fremdesBackup := filepath.Join(fremd, "boardbackup-anderes-20200101-000000-abc.tar.gz")
	if err := os.WriteFile(fremdesBackup, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	alt := t0.Add(-100 * 24 * time.Hour)
	for _, p := range []string{datei, fremdesBackup} {
		if err := os.Chtimes(p, alt, alt); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		laufUm(t, env, t0.Add(time.Duration(i)*10*24*time.Hour))
	}
	if !existiert(datei) || !existiert(fremdesBackup) {
		t.Fatalf("fremde Datei wurde gelöscht")
	}
	if existiert(filepath.Join(fremd, ".geloescht-seit")) {
		t.Fatalf("fremdes Verzeichnis wurde markiert")
	}
}

// TestVerwaist_FremdeDateiNebenBackups — Backups gehen, die fremde Datei
// und damit das Verzeichnis bleiben.
func TestVerwaist_FremdeDateiNebenBackups(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "y", "Y")
	t0 := time.Now()
	laufUm(t, env, t0)
	dir := filepath.Join(env.backupDir, "y")
	notiz := filepath.Join(dir, "notiz.txt")
	if err := os.WriteFile(notiz, []byte("vom Admin"), 0o644); err != nil {
		t.Fatal(err)
	}
	loescheWieAPI(t, env, "y", t0)
	// Seit F-1135 zählen auch die Läufe (KeepDays = 3), nicht nur die Zeit.
	for _, tag := range []int{1, 2, 3, 4} { // Anker, dann drei gezählte Läufe
		laufUm(t, env, t0.Add(time.Duration(tag)*24*time.Hour))
	}
	if backupsIn(t, dir) != 0 {
		t.Fatalf("Backups nach Ablauf der Frist noch da")
	}
	if !existiert(notiz) {
		t.Fatalf("fremde Datei gelöscht")
	}
}

// TestVerwaist_BoardKehrtZurueck — wird das Board mit derselben ID wieder
// angelegt (Wiederherstellung), verschwindet die Markierung, und seine
// Backups bleiben.
func TestVerwaist_BoardKehrtZurueck(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "z", "Z")
	t0 := time.Now()
	laufUm(t, env, t0)
	loescheWieAPI(t, env, "z", t0)
	dir := filepath.Join(env.backupDir, "z")
	if !existiert(filepath.Join(dir, ".geloescht-seit")) {
		t.Fatalf("Vorbedingung: Grabstein fehlt")
	}
	env.seedBoard(t, "z", "Z zurück")
	laufUm(t, env, t0.Add(4*24*time.Hour))
	if existiert(filepath.Join(dir, ".geloescht-seit")) {
		t.Fatalf("Grabstein nach Rückkehr des Boards nicht entfernt")
	}
	if backupsIn(t, dir) < 1 {
		t.Fatalf("Backups des zurückgekehrten Boards entfernt")
	}
}

// TestVerwaist_UngueltigeNamenUebergangen — Verzeichnisnamen, die keine
// Board-ID sein können, und Symlinks werden nie angefasst.
func TestVerwaist_UngueltigeNamenUebergangen(t *testing.T) {
	env := newTestEnv(t)
	t0 := time.Now()
	alt := t0.Add(-100 * 24 * time.Hour)
	for _, name := range []string{".versteckt", "mit.punkt"} {
		dir := filepath.Join(env.backupDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "boardbackup-"+name+"-20200101-000000-abc.tar.gz")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, alt, alt); err != nil {
			t.Fatal(err)
		}
	}
	// Symlink auf ein Verzeichnis mit passendem Backup-Namen.
	ziel := filepath.Join(t.TempDir(), "ziel")
	if err := os.MkdirAll(ziel, 0o755); err != nil {
		t.Fatal(err)
	}
	zielDatei := filepath.Join(ziel, "boardbackup-link-20200101-000000-abc.tar.gz")
	if err := os.WriteFile(zielDatei, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(zielDatei, alt, alt); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ziel, filepath.Join(env.backupDir, "link")); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		laufUm(t, env, t0.Add(time.Duration(i)*10*24*time.Hour))
	}
	for _, name := range []string{".versteckt", "mit.punkt"} {
		if backupsIn(t, filepath.Join(env.backupDir, name)) != 1 {
			t.Errorf("Verzeichnis %q angefasst", name)
		}
	}
	if !existiert(zielDatei) || existiert(filepath.Join(ziel, ".geloescht-seit")) {
		t.Errorf("Symlink-Ziel angefasst")
	}
}

// TestVerwaist_FristAbLoeschung_NichtAbLetzterSicherung — war die letzte
// Sicherung beim Löschen schon älter als die Frist, darf das Backup trotzdem
// nicht im nächsten Lauf verschwinden: Die Frist zählt ab dem Grabstein.
func TestVerwaist_FristAbLoeschung_NichtAbLetzterSicherung(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "alt", "Alt")
	t0 := time.Now()
	laufUm(t, env, t0)
	dir := filepath.Join(env.backupDir, "alt")
	eintraege, err := os.ReadDir(dir)
	if err != nil || len(eintraege) != 1 {
		t.Fatalf("Vorbedingung: ein Backup erwartet (%v)", err)
	}
	lange := t0.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, eintraege[0].Name()), lange, lange); err != nil {
		t.Fatal(err)
	}
	loescheWieAPI(t, env, "alt", t0)
	laufUm(t, env, t0)
	laufUm(t, env, t0.Add(2*24*time.Hour))
	if backupsIn(t, dir) != 1 {
		t.Fatalf("altes Backup innerhalb der Frist ab Löschen entfernt")
	}
	laufUm(t, env, t0.Add(3*24*time.Hour)) // F-1135: dritter gezählter Lauf
	laufUm(t, env, t0.Add(4*24*time.Hour))
	if existiert(dir) {
		t.Fatalf("nach Ablauf der Frist ab Löschen noch vorhanden")
	}
}

// TestVerwaist_LeereDB_LoeschtNichts — Prüfbefehl aus K-A1 (Kritiker
// Sprint 70): Nach DB-Verlust oder dem Zurückspielen eines älteren
// Server-Backups fehlen Boards in der Datenbank, deren Board-Backups die
// einzige Kopie sein können. Ohne Grabstein darf der Lauf sie NIE löschen,
// egal wie viel Zeit vergeht.
func TestVerwaist_LeereDB_LoeschtNichts(t *testing.T) {
	alt := newTestEnv(t)
	for _, id := range []string{"b1", "b2", "b3"} {
		alt.seedBoard(t, id, id)
	}
	t0 := time.Now()
	laufUm(t, alt, t0)

	// Frische, leere DB auf demselben Backup-Root.
	neu := newTestEnv(t)
	neu.backupDir = alt.backupDir
	var log []string
	for _, um := range []time.Time{t0, t0.Add(4 * 24 * time.Hour), t0.Add(30 * 24 * time.Hour)} {
		log = append(log, laufUm(t, neu, um)...)
	}
	for _, id := range []string{"b1", "b2", "b3"} {
		if n := backupsIn(t, filepath.Join(alt.backupDir, id)); n != 1 {
			t.Errorf("Board %s: %d Backups statt 1 — ohne Grabstein gelöscht (K-A1)", id, n)
		}
		if existiert(filepath.Join(alt.backupDir, id, ".geloescht-seit")) {
			t.Errorf("Board %s: Grabstein ohne Löschung angelegt", id)
		}
	}
	gemeldet := false
	for _, z := range log {
		if strings.Contains(z, "keinen Grabstein") {
			gemeldet = true
		}
	}
	if !gemeldet {
		t.Errorf("Verzeichnisse ohne Board und ohne Grabstein wurden nicht gemeldet")
	}
}

// TestMarkiereGeloescht_LegtNichtsAn — ohne Backup-Verzeichnis entsteht
// nichts; ein Symlink wird nicht markiert; ein zweites Löschen verschiebt die
// Frist nicht.
func TestMarkiereGeloescht_LegtNichtsAn(t *testing.T) {
	root := t.TempDir()
	if err := boardbackup.MarkiereGeloescht(root, "gibtsnicht", time.Now()); err != nil {
		t.Fatal(err)
	}
	if existiert(filepath.Join(root, "gibtsnicht")) {
		t.Fatalf("Verzeichnis für ein Board ohne Backups angelegt")
	}
	ziel := t.TempDir()
	if err := os.Symlink(ziel, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := boardbackup.MarkiereGeloescht(root, "link", time.Now()); err != nil {
		t.Fatal(err)
	}
	if existiert(filepath.Join(ziel, ".geloescht-seit")) {
		t.Fatalf("Grabstein durch einen Symlink geschrieben")
	}
	if err := os.MkdirAll(filepath.Join(root, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	erst := time.Now().Add(-48 * time.Hour)
	if err := boardbackup.MarkiereGeloescht(root, "b", erst); err != nil {
		t.Fatal(err)
	}
	if err := boardbackup.MarkiereGeloescht(root, "b", time.Now()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "b", ".geloescht-seit"))
	if err != nil || fi.ModTime().After(erst.Add(time.Minute)) {
		t.Fatalf("zweites Löschen hat die Frist verschoben (%v, %v)", fi.ModTime(), err)
	}
	for _, schlecht := range []string{"", "../x", ".versteckt", "a/b"} {
		if err := boardbackup.MarkiereGeloescht(root, schlecht, time.Now()); err != nil {
			t.Fatalf("ungültige ID %q: %v", schlecht, err)
		}
	}
}

// grabsteinAlt bereitet ein gelöschtes Board „alt“ vor, dessen Backup und
// Grabstein 30 Tage vor t0 liegen — so sieht es nach einem Uhrsprung aus:
// Der Grabstein wurde mit einer nachgehenden Uhr gesetzt (F-1135).
func grabsteinAlt(t *testing.T, env *testEnv, t0 time.Time) string {
	t.Helper()
	env.seedBoard(t, "alt", "Alt")
	laufUm(t, env, t0)
	dir := filepath.Join(env.backupDir, "alt")
	eintraege, err := os.ReadDir(dir)
	if err != nil || len(eintraege) != 1 {
		t.Fatalf("Vorbedingung: ein Backup erwartet (%v)", err)
	}
	lange := t0.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, eintraege[0].Name()), lange, lange); err != nil {
		t.Fatal(err)
	}
	loescheWieAPI(t, env, "alt", lange)
	return dir
}

// TestVerwaist_Uhrsprung_ZaehltLaeufe — der Prüfbefehl aus F-1135: Grabstein
// bei t0-30d, ein Lauf bei t0 → nichts gelöscht. Der erste Lauf setzt den
// Anker; erst nach KeepDays (3) gezählten Läufen danach geht das Verzeichnis.
//
// GEGENPROBE (Karte #581): Ohne `laeufe < deps.KeepDays` in der Bedingung
// verschwindet das Verzeichnis schon beim ersten Lauf — der Test wird rot.
func TestVerwaist_Uhrsprung_ZaehltLaeufe(t *testing.T) {
	env := newTestEnv(t)
	t0 := time.Now()
	dir := grabsteinAlt(t, env, t0)

	laufUm(t, env, t0.Add(time.Hour))
	if backupsIn(t, dir) != 1 {
		t.Fatal("Backup beim ersten Lauf nach dem Uhrsprung entfernt (F-1135)")
	}
	laufUm(t, env, t0.Add(time.Hour+24*time.Hour))
	if backupsIn(t, dir) != 1 {
		t.Fatal("Backup nach Anker und einem gezählten Lauf entfernt, KeepDays ist 3")
	}
	laufUm(t, env, t0.Add(time.Hour+48*time.Hour))
	if backupsIn(t, dir) != 1 {
		t.Fatal("Backup nach Anker und zwei gezählten Läufen entfernt, KeepDays ist 3")
	}
	laufUm(t, env, t0.Add(time.Hour+72*time.Hour))
	if existiert(dir) {
		t.Fatal("nach drei gezählten Läufen und abgelaufener Frist noch vorhanden")
	}
}

// TestVerwaist_Uhrsprung_LaeufeAmSelbenTagZaehlenEinmal — mehrere Läufe
// innerhalb von 20 h (Neustarts, manuelle Läufe) zählen als einer.
//
// GEGENPROBE (Karte #581): Ohne die Abstandsprüfung in zaehleLauf reichen
// drei Läufe innerhalb einer Stunde, der Test wird rot.
func TestVerwaist_Uhrsprung_LaeufeAmSelbenTagZaehlenEinmal(t *testing.T) {
	env := newTestEnv(t)
	t0 := time.Now()
	dir := grabsteinAlt(t, env, t0)
	for i := 0; i < 5; i++ {
		laufUm(t, env, t0.Add(time.Duration(i)*10*time.Minute))
	}
	if backupsIn(t, dir) != 1 {
		t.Fatal("fünf Läufe in einer Stunde haben die Frist verkürzt")
	}
}

// TestVerwaist_Uhrsprung_VorgehendeUhrBeschleunigtNicht — ein Lauf mit
// vorgehender Uhr (t0+10d) zählt einmal; danach blockiert sein „zuletzt“ das
// Zählen, bis die echte Zeit es eingeholt hat.
func TestVerwaist_Uhrsprung_VorgehendeUhrBeschleunigtNicht(t *testing.T) {
	env := newTestEnv(t)
	t0 := time.Now()
	dir := grabsteinAlt(t, env, t0)
	laufUm(t, env, t0.Add(10*24*time.Hour)) // Uhr geht 10 Tage vor
	gemeldet := false
	for tag := 1; tag <= 5; tag++ { // danach richtig gestellt
		for _, zeile := range laufUm(t, env, t0.Add(time.Duration(tag)*24*time.Hour)) {
			if strings.Contains(zeile, "vorgehender Uhr") {
				gemeldet = true
			}
		}
	}
	if backupsIn(t, dir) != 1 {
		t.Fatal("vorgehende Uhr hat die gezählten Läufe beschleunigt")
	}
	// Kritiker Sprint 74: Das Stillstehen wird gemeldet, nicht verschwiegen.
	// GEGENPROBE: Ohne die Log-Zeile für b.ZuletztVoraus wird das rot.
	if !gemeldet {
		t.Fatal("Grabstein mit „zuletzt“ in der Zukunft wurde nicht im Log gemeldet")
	}
}

// TestVerwaist_AlterGrabsteinZaehltAbNull — ein Grabstein aus der Zeit vor
// F-1135 (nur Zeile 1) gilt als „0 Läufe“: Der erste Lauf setzt nur den
// Anker und wartet dann die volle Zahl ab; die mtime (Zeitpunkt des
// Löschens) bleibt beim Fortschreiben erhalten.
func TestVerwaist_AlterGrabsteinZaehltAbNull(t *testing.T) {
	env := newTestEnv(t)
	t0 := time.Now()
	dir := grabsteinAlt(t, env, t0)
	marker := filepath.Join(dir, ".geloescht-seit")
	vorher, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	inhalt, _ := os.ReadFile(marker)
	if strings.Contains(string(inhalt), "laeufe=") {
		t.Fatalf("Vorbedingung: Grabstein ohne Zähler erwartet, ist %q", inhalt)
	}
	laufUm(t, env, t0.Add(time.Hour))
	nachher, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !nachher.ModTime().Equal(vorher.ModTime()) {
		t.Errorf("mtime des Grabsteins verändert: %v → %v", vorher.ModTime(), nachher.ModTime())
	}
	inhalt, _ = os.ReadFile(marker)
	if !strings.Contains(string(inhalt), "laeufe=0\n") || !strings.Contains(string(inhalt), "zuletzt=") {
		t.Errorf("Grabstein nach einem Lauf: %q, erwartet laeufe=0 und einen Anker zuletzt=", inhalt)
	}
	if existiert(marker + ".neu") {
		t.Error("Zwischendatei liegengeblieben")
	}
}

// TestVerwaist_Uhrsprung_ErsterLaufZaehltNicht — Prüfbefehl des Kritikers
// Sprint 74: keep_days = 1, Grabstein mit nachgehender Uhr gesetzt (t0-30d),
// ein Lauf bei t0. Zählte der erste Lauf mit, wäre das Board eine Minute
// nach dem Löschen abgeräumt. Er setzt nur den Anker; erst ein Lauf
// mindestens 20 h später räumt ab.
//
// GEGENPROBE: Zählt zaehleLauf beim leeren „zuletzt“ sofort mit (laeufe++),
// wird der Test rot.
func TestVerwaist_Uhrsprung_ErsterLaufZaehltNicht(t *testing.T) {
	env := newTestEnv(t)
	t0 := time.Now()
	env.seedBoard(t, "eins", "Eins")
	laufMitFrist := func(um time.Time) {
		boardbackup.BackupAllBoardsOnce(context.Background(), boardbackup.SchedulerDeps{
			Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
			BackupBaseDir: env.backupDir, KeepDays: 1,
			NowFn: func() time.Time { return um }, Logger: func(string, ...any) {},
		})
	}
	laufMitFrist(t0)
	dir := filepath.Join(env.backupDir, "eins")
	eintraege, err := os.ReadDir(dir)
	if err != nil || len(eintraege) != 1 {
		t.Fatalf("Vorbedingung: ein Backup erwartet (%v)", err)
	}
	lange := t0.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, eintraege[0].Name()), lange, lange); err != nil {
		t.Fatal(err)
	}
	loescheWieAPI(t, env, "eins", lange)

	laufMitFrist(t0.Add(time.Minute))
	if backupsIn(t, dir) != 1 {
		t.Fatal("keep_days = 1: Backup schon beim ersten Lauf nach dem Löschen entfernt")
	}
	laufMitFrist(t0.Add(21 * time.Hour))
	if existiert(dir) {
		t.Fatal("nach Anker und einem gezählten Lauf (keep_days = 1) noch vorhanden")
	}
}

// TestVerwaist_UhrNichtGestellt_RuehrtNichtsAn (F-1192): Mit nicht gestellter
// Uhr wird weder gezählt noch gelöscht — auch der Anker eines Grabsteins
// trüge sonst die falsche Zeit.
// GEGENPROBE: Ohne uhr.Erlaubt in raeumeVerwaisteAb bekommt der Grabstein
// einen Anker (zuletzt=) — rot.
func TestVerwaist_UhrNichtGestellt_RuehrtNichtsAn(t *testing.T) {
	env := newTestEnv(t)
	t0 := time.Now()
	dir := grabsteinAlt(t, env, t0)
	alt := uhr.Gestellt
	t.Cleanup(func() { uhr.Gestellt = alt })
	uhr.Gestellt = func() (bool, error) { return false, nil }
	for tag := 1; tag <= 5; tag++ {
		laufUm(t, env, t0.Add(time.Duration(tag)*24*time.Hour))
	}
	if backupsIn(t, dir) != 1 {
		t.Fatal("Backup mit nicht gestellter Uhr entfernt")
	}
	if inhalt, _ := os.ReadFile(filepath.Join(dir, ".geloescht-seit")); strings.Contains(string(inhalt), "zuletzt=") {
		t.Fatalf("Grabstein mit nicht gestellter Uhr fortgeschrieben: %q", inhalt)
	}
}
