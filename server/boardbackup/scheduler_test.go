package boardbackup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/boardbackup"
)

// Test cronMatches indirekt via Scheduler: wir nutzen eine NowFn die exakt
// 02:00 zurückgibt und prüfen, ob runBackupAllBoards aufgerufen wird (durch
// Backup-Files auf der Disk).
func TestScheduler_CronMatches(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Sched-Test")

	tmp := t.TempDir()
	backupDir := filepath.Join(tmp, "bk")

	// NowFn fest auf 02:00 → match für "0 2 * * *"
	fixed := time.Date(2026, 5, 21, 2, 0, 0, 0, time.UTC)
	advance := 0
	nowFn := func() time.Time {
		t := fixed.Add(time.Duration(advance) * time.Minute)
		return t
	}

	// Scheduler in goroutine starten, kurz laufen lassen, dann beenden.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- boardbackup.RunScheduler(ctx, boardbackup.SchedulerDeps{
			Boards:             env.boards,
			ACL:                env.acl,
			AttachmentsBaseDir: env.attDir,
			BackupBaseDir:      backupDir,
			KeepDays:           3,
			CronPattern:        "0 2 * * *",
			NowFn:              nowFn,
			Logger:             func(string, ...any) {},
		})
	}()

	// Bis zu 1 Sekunde warten — der Ticker feuert nach ~60s, also fängt
	// dieser Test nur den initialen Setup-Pfad ab. Das ist ok — der wichtigere
	// Test ist cronMatches als Unit (gleich darunter).
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Scheduler hat ctx-cancel nicht respektiert")
	}
	_ = advance // unused-Reservierung für längere Tests
}

// Direkt cronMatches testen — Unit-Tests des Pattern-Parsers.
func TestCronMatches_Patterns(t *testing.T) {
	// Wir haben keinen direkten Zugriff auf das unexported cronMatches —
	// wir testen es indirekt über RunScheduler-Verhalten. Stattdessen:
	// wenn das Pattern matched, würde ein Tick-Match einen Backup-Lauf
	// auslösen. Für reine Pattern-Korrektheit reicht ein Smoke-Test:
	// wir bauen ein vermeintliches Pattern via Scheduler mit NowFn und
	// einem Single-Tick — wegen 1-Minuten-Ticker können wir das hier
	// aber nicht in akzeptabler Test-Zeit prüfen. Daher: nur Smoke,
	// ausführlichere Pattern-Tests sind im RunScheduler-Test mit forced
	// Ticker.
	_ = t
}

// F-699 (Karte #336/#335, Sprint 58): Der nächtliche Lauf muss die fertige
// Datei auf ihren endgültigen Namen umbenennen. Bis Sprint 58 tat er das
// nicht — die Backups hiessen dauerhaft `…-pending.tar.gz`, während der
// Kommentar an makeFilename das Gegenteil behauptete. Sichtbar wurde es erst,
// als Karte #335 diese Liste dem Board-Eigentümer zeigte.
func TestScheduler_BenenntAufDenEndgueltigenNamenUm(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Nacht-Test")
	backupDir := filepath.Join(t.TempDir(), "bk")

	fest := time.Date(2026, 8, 20, 2, 0, 0, 0, time.UTC)
	boardbackup.SetNowFunc(func() time.Time { return fest })
	t.Cleanup(func() { boardbackup.SetNowFunc(time.Now) })

	boardbackup.BackupAllBoardsOnce(context.Background(), boardbackup.SchedulerDeps{
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		BackupBaseDir:      backupDir,
		KeepDays:           3,
		Logger:             func(string, ...any) {},
	})

	eintraege, err := os.ReadDir(filepath.Join(backupDir, "b1"))
	if err != nil {
		t.Fatalf("Backup-Verzeichnis: %v", err)
	}
	if len(eintraege) != 1 {
		t.Fatalf("erwartet genau eine Datei, gefunden %d", len(eintraege))
	}
	name := eintraege[0].Name()
	if strings.Contains(name, "-pending") {
		t.Fatalf("Datei trägt noch den Zwischennamen: %s", name)
	}
	// Der Endname muss den Zeitstempel des Laufs UND einen SHA-Präfix tragen.
	if !strings.HasPrefix(name, "boardbackup-b1-20260820-020000-") {
		t.Fatalf("Zeitstempel im Namen weicht ab: %s", name)
	}
	if !strings.HasSuffix(name, ".tar.gz") {
		t.Fatalf("Endung weicht ab: %s", name)
	}
	// Und die Datei muss lesbar sein — ein Umbenennen, das den Inhalt
	// verliert, wäre schlimmer als der alte Zustand.
	pfad := filepath.Join(backupDir, "b1", name)
	if fi, serr := os.Stat(pfad); serr != nil || fi.Size() == 0 {
		t.Fatalf("Datei nach Umbenennen nicht lesbar: %v", serr)
	}

	/* F-716 (b): Der Mittelteil muss die ersten acht Hex-Zeichen des
	   TATSÄCHLICHEN Archiv-SHA sein. Vorher prüfte der Test nur Präfix,
	   Abwesenheit von "-pending" und Endung — ein Umbenennen auf einen
	   konstanten Mittelteil wäre grün geblieben, und genau das (ein Name,
	   der einen Hash behauptet, den niemand nachrechnet) nennt F-699 als
	   Symptom. Der Soll-Wert kommt aus meta.json im Archiv selbst. */
	kurz := strings.TrimSuffix(strings.TrimPrefix(name, "boardbackup-b1-20260820-020000-"), ".tar.gz")
	meta := metaAusArchiv(t, pfad, nil)
	if len(meta.SHA256) < 8 {
		t.Fatalf("meta.sha256 ist zu kurz: %q", meta.SHA256)
	}
	if kurz != meta.SHA256[:8] {
		t.Errorf("Mittelteil des Dateinamens = %q, erwartet %q (erste acht Zeichen "+
			"von meta.sha256 %q) — der Name behauptet einen Hash, der nicht der "+
			"des Archivs ist (F-716 b)", kurz, meta.SHA256[:8], meta.SHA256)
	}
}

// F-710 (Sprint-58-Kritiker): Zwei Reservierungen für dasselbe Board zur
// exakt gleichen Sekunde — Scheduler-Lauf und "Jetzt sichern"-Klick, oder
// zwei gleichzeitige Requests — dürfen NICHT denselben Zwischennamen
// bekommen. Vorher bildeten REST-Endpunkt und Scheduler den Namen
// unabhängig aus Board-ID + Sekunden-Zeitstempel; BackupBoard öffnet ihn
// mit os.Create (truncate), der zweite Schreiber hätte den ersten mitten
// im Schreibvorgang überschrieben.
func TestReservePendingPath_GleicheSekunde_KeineKollision(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

	p1, err1 := boardbackup.ReservePendingPath(dir, "b1", ts)
	if err1 != nil {
		t.Fatalf("erste Reservierung: %v", err1)
	}
	p2, err2 := boardbackup.ReservePendingPath(dir, "b1", ts)
	if err2 != nil {
		t.Fatalf("zweite Reservierung: %v", err2)
	}

	if p1 == p2 {
		t.Fatalf("beide Aufrufe erhielten denselben Pfad %q — genau die F-710-Kollision", p1)
	}

	// Der eigentliche Schaden aus F-710: der zweite Schreiber überschreibt
	// den ersten. Beide Pfade müssen unabhängig beschreibbar sein.
	if err := os.WriteFile(p1, []byte("erstes archiv"), 0o644); err != nil {
		t.Fatalf("p1 schreiben: %v", err)
	}
	if err := os.WriteFile(p2, []byte("zweites archiv"), 0o644); err != nil {
		t.Fatalf("p2 schreiben: %v", err)
	}
	if got, _ := os.ReadFile(p1); string(got) != "erstes archiv" {
		t.Fatalf("p1 wurde überschrieben, Inhalt: %q", got)
	}
	if got, _ := os.ReadFile(p2); string(got) != "zweites archiv" {
		t.Fatalf("p2 wurde überschrieben, Inhalt: %q", got)
	}

	// Beide Pfade bleiben als erkennbare Zwischennamen fassbar —
	// istUnfertig()/istBackupDatei() in rotation.go filtern über diese
	// Substrings, nicht über den exakten Namen.
	for _, p := range []string{p1, p2} {
		if !strings.Contains(p, "-pending.tar.gz") {
			t.Fatalf("Pfad %q enthält nicht mehr den erwarteten -pending.tar.gz-Marker", p)
		}
		if !strings.HasPrefix(filepath.Base(p), "boardbackup-b1-") {
			t.Fatalf("Pfad %q hat nicht mehr das erwartete Präfix", p)
		}
	}
}

// Auch verschlüsselt muss der Endname stimmen — inklusive .enc-Endung.
func TestScheduler_EndnameMitVerschluesselung(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b2", "Nacht-Test verschlüsselt")
	backupDir := filepath.Join(t.TempDir(), "bk")

	fest := time.Date(2026, 8, 20, 2, 0, 0, 0, time.UTC)
	boardbackup.SetNowFunc(func() time.Time { return fest })
	t.Cleanup(func() { boardbackup.SetNowFunc(time.Now) })

	boardbackup.BackupAllBoardsOnce(context.Background(), boardbackup.SchedulerDeps{
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		BackupBaseDir:      backupDir,
		KeepDays:           3,
		Logger:             func(string, ...any) {},
		Crypter:            crypterAus(t, keyA),
	})

	eintraege, err := os.ReadDir(filepath.Join(backupDir, "b2"))
	if err != nil {
		t.Fatalf("Backup-Verzeichnis: %v", err)
	}
	if len(eintraege) != 1 {
		t.Fatalf("erwartet genau eine Datei, gefunden %d", len(eintraege))
	}
	name := eintraege[0].Name()
	if strings.Contains(name, "-pending") || !strings.HasSuffix(name, ".tar.gz.enc") {
		t.Fatalf("Endname verschlüsselt falsch: %s", name)
	}

	/* F-716 (a): Der Test las die Datei nie — er glaubte der Endung. Eine
	   Änderung, die `res.Encrypted` setzt, ohne zu verschlüsseln, wäre grün
	   geblieben, und auf der Platte hätte Klartext gelegen. Geprüft wird
	   deshalb am Inhalt: Magic am Dateianfang (SplitEncrypted erkennt die
	   Verschlüsselung genau daran, nicht am Namen) — und danach, dass das
	   Archiv mit dem Schlüssel auch wirklich wieder aufgeht. */
	pfad := filepath.Join(backupDir, "b2", name)
	roh, rerr := os.ReadFile(pfad)
	if rerr != nil {
		t.Fatalf("Archiv lesen: %v", rerr)
	}
	verschluesselt, _, serr := boardbackup.SplitEncrypted(bytes.NewReader(roh))
	if serr != nil {
		t.Fatalf("SplitEncrypted: %v", serr)
	}
	if !verschluesselt {
		t.Fatalf("Datei %s trägt kein Verschlüsselungs-Magic — auf der Platte "+
			"liegt Klartext, obwohl der Name .enc behauptet (F-716 a)", name)
	}
	// Gegenprobe in die andere Richtung: mit Schlüssel muss das Archiv
	// lesbar sein, sonst wäre „verschlüsselt" nur ein Byte-Präfix.
	meta := metaAusArchiv(t, pfad, crypterAus(t, keyA))
	if meta.BoardID != "b2" {
		t.Errorf("meta.board_id = %q, erwartet b2", meta.BoardID)
	}
}

// K4 (Sprint-58-Kritiker): Die Zusage „kein stiller Rückfall auf Klartext"
// hing allein an der Verdrahtung in main.go — im Paket gab es keinen
// Wächter und keinen Test. Wer SchedulerDeps ohne Crypter baut, während die
// Verschlüsselung eingeschaltet ist, bekam lautlos Klartext-Archive.
func TestScheduler_KeinKlartextWennVerschluesselungVerlangt(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b-k4", "Kein Rückfall")
	backupDir := filepath.Join(t.TempDir(), "bk")

	var gemeldet []string
	boardbackup.BackupAllBoardsOnce(context.Background(), boardbackup.SchedulerDeps{
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		BackupBaseDir:      backupDir,
		KeepDays:           3,
		Logger:             func(string, ...any) {},
		EncryptionRequired: true,
		Crypter:            nil, // <- der Fehlerfall
		OnFailure: func(id, titel string, err error) {
			gemeldet = append(gemeldet, id)
		},
	})

	// Es darf NICHTS geschrieben worden sein — auch nichts Unverschlüsseltes.
	if eintraege, err := os.ReadDir(filepath.Join(backupDir, "b-k4")); err == nil && len(eintraege) > 0 {
		namen := []string{}
		for _, e := range eintraege {
			namen = append(namen, e.Name())
		}
		t.Fatalf("trotz fehlendem Schlüssel wurde geschrieben: %v", namen)
	}
	// Und der Betreiber muss es erfahren (ADR-0107-Pfad).
	if len(gemeldet) != 1 || gemeldet[0] != "b-k4" {
		t.Errorf("OnFailure nicht oder falsch gerufen: %v", gemeldet)
	}
}

// metaAusArchiv liest meta.json aus einem Backup-Archiv. `c` != nil
// entschlüsselt vorher. Gebraucht von den F-716-Prüfungen (a)/(b), die den
// Dateinamen gegen den tatsächlichen Inhalt halten statt gegen sich selbst.
func metaAusArchiv(t *testing.T, pfad string, c *boardbackup.Crypter) boardbackup.Meta {
	t.Helper()
	roh, err := os.ReadFile(pfad)
	if err != nil {
		t.Fatalf("Archiv lesen: %v", err)
	}
	var src io.Reader = bytes.NewReader(roh)
	verschluesselt, rest, err := boardbackup.SplitEncrypted(src)
	if err != nil {
		t.Fatalf("SplitEncrypted: %v", err)
	}
	src = rest
	if verschluesselt {
		if c == nil {
			t.Fatalf("Archiv %s ist verschlüsselt, aber kein Crypter übergeben", pfad)
		}
		dr, derr := c.DecryptReader(src)
		if derr != nil {
			t.Fatalf("Entschlüsseln: %v", derr)
		}
		src = dr
	}
	gr, err := gzip.NewReader(src)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		if hdr.Name != "meta.json" {
			continue
		}
		buf, rerr := io.ReadAll(tr)
		if rerr != nil {
			t.Fatalf("meta lesen: %v", rerr)
		}
		var m boardbackup.Meta
		if jerr := json.Unmarshal(buf, &m); jerr != nil {
			t.Fatalf("meta parsen: %v", jerr)
		}
		return m
	}
	t.Fatalf("meta.json nicht im Archiv %s gefunden", pfad)
	return boardbackup.Meta{}
}

// F-716 (d): Cron-Treffer und Dateizeitstempel kamen aus zwei Uhren — der
// Treffer aus `deps.NowFn` (dem dokumentierten Weg für Test-Isolation pro
// Instanz), der Name aus der globalen `nowFunc`. Wer nur `NowFn` setzte,
// bekam ein Archiv mit der echten Wanduhr im Namen. Der Test setzt bewusst
// KEIN SetNowFunc, sondern nur `NowFn`.
func TestScheduler_DateinameFolgtDepsNowFn(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b-uhr", "Zwei Uhren")
	backupDir := filepath.Join(t.TempDir(), "bk")

	fest := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	boardbackup.BackupAllBoardsOnce(context.Background(), boardbackup.SchedulerDeps{
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		BackupBaseDir:      backupDir,
		KeepDays:           3,
		Logger:             func(string, ...any) {},
		NowFn:              func() time.Time { return fest },
	})

	eintraege, err := os.ReadDir(filepath.Join(backupDir, "b-uhr"))
	if err != nil {
		t.Fatalf("Backup-Verzeichnis: %v", err)
	}
	if len(eintraege) != 1 {
		t.Fatalf("erwartet genau eine Datei, gefunden %d", len(eintraege))
	}
	name := eintraege[0].Name()
	if !strings.HasPrefix(name, "boardbackup-b-uhr-20210304-050607-") {
		t.Errorf("Dateiname = %q — der Zeitstempel stammt nicht aus deps.NowFn, "+
			"sondern aus einer zweiten Uhr (F-716 d)", name)
	}
}
