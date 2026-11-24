package boardbackup_test

// K-7 / K-5 (Sprint 85, Kritiker #735): Panik im Alle-Boards-Lauf beendet den
// Prozess nicht, gibt die Sperre frei und landet als Fehler im Status;
// ein abgebrochener Kontext startet keine Boards mehr.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/boardbackup"
)

func TestRunAllBoardsReserviert_PanikWirdAbgefangen(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Panik")
	if !boardbackup.ReserviereAll() {
		t.Fatal("Sperre sollte frei sein")
	}
	deps := boardbackup.SchedulerDeps{
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
		BackupBaseDir: filepath.Join(t.TempDir(), "bk"), KeepDays: 3,
		// Eingespritzte Panik: der Logger kippt nach dem ersten erfolgreichen Board.
		Logger: func(f string, _ ...any) {
			if strings.HasPrefix(f, "board-backup: board=%s file=") {
				panic("eingespritzt")
			}
		},
	}
	erg := boardbackup.RunAllBoardsReserviert(context.Background(), deps) // würde ohne recover den Test-Prozess beenden
	if erg.Code != "panic" || erg.Meldung == "" {
		t.Errorf("Ergebnis soll Code panic tragen, got %+v", erg)
	}
	laeuft, last := boardbackup.AllStatus()
	if laeuft {
		t.Error("Sperre muss nach Panik frei sein")
	}
	if last == nil || last.Code != "panic" {
		t.Errorf("Status soll Fehler zeigen, got %+v", last)
	}
	if !boardbackup.ReserviereAll() {
		t.Fatal("Sperre nach Panik nicht frei")
	}
	boardbackup.RunAllBoardsReserviert(context.Background(), boardbackup.SchedulerDeps{
		Boards: env.boards, ACL: env.acl, BackupBaseDir: t.TempDir(), KeepDays: 3, Logger: func(string, ...any) {},
	})
}

func TestRunAllBoardsReserviert_AbgebrochenerKontextStartetNichts(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Abbruch")
	if !boardbackup.ReserviereAll() {
		t.Fatal("Sperre sollte frei sein")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bk := filepath.Join(t.TempDir(), "bk")
	erg := boardbackup.RunAllBoardsReserviert(ctx, boardbackup.SchedulerDeps{
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
		BackupBaseDir: bk, KeepDays: 3, Logger: func(string, ...any) {},
	})
	if erg.Code != "abort" || erg.OK != 0 {
		t.Errorf("erwartet Code abort ohne Sicherung, got %+v", erg)
	}
	if laeuft, _ := boardbackup.AllStatus(); laeuft {
		t.Error("Sperre muss frei sein")
	}
}

// F-1824 (Verify Sprint 85): Abbruch MITTEN im Lauf — nach dem ersten Board
// wird der Kontext abgebrochen (Herunterfahren oder Reload der Sektion
// backup_board). Das zweite Board startet nicht mehr, das Ergebnis trägt
// Code abort, und die Logzeile nennt die übersprungenen Boards.
func TestRunAllBoardsReserviert_AbbruchMittenImLauf(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Eins")
	env.seedBoard(t, "b2", "Zwei")
	if !boardbackup.ReserviereAll() {
		t.Fatal("Sperre sollte frei sein")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var abbruchZeile string
	erg := boardbackup.RunAllBoardsReserviert(ctx, boardbackup.SchedulerDeps{
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
		BackupBaseDir: filepath.Join(t.TempDir(), "bk"), KeepDays: 3,
		Logger: func(f string, args ...any) {
			if strings.HasPrefix(f, "board-backup: board=%s file=") {
				cancel() // nach dem ersten erfolgreichen Board
			}
			if strings.Contains(f, "Lauf abgebrochen") {
				abbruchZeile = fmt.Sprintf(f, args...)
			}
		},
	})
	if erg.Code != "abort" || erg.OK != 1 || erg.Gesamt != 2 {
		t.Errorf("erwartet abort nach genau einem Board von zwei, got %+v", erg)
	}
	if !strings.Contains(abbruchZeile, "neu konfiguriert") || !strings.Contains(abbruchZeile, "1 nicht gesichert") {
		t.Errorf("Logzeile soll beide Auslöser und die Zahl der übersprungenen Boards nennen, got %q", abbruchZeile)
	}
	if laeuft, _ := boardbackup.AllStatus(); laeuft {
		t.Error("Sperre muss frei sein")
	}
}
