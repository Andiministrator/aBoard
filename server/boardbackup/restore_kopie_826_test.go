package boardbackup_test

// Karte #826 (ADR-0141 §7, Sitzungs-QA QA-6 d): Eine externe Kopie des
// Board-Paars wird nie per Restore überschrieben — als neues Board
// zurückspielen geht, und das neue Board trägt keine Spiegel-Marken.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestRestore826_KopieNichtUeberschreiben(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	const kopie = "0e5d8a2c-1111-5222-8333-44445555f826"
	if _, err := storage.NewBoardpaarRepo(env.db).KopieAnlegen(ctx, kopie, "Kopie", "https://i.example/#board=b", "skript:t", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.ExecContext(ctx, `UPDATE boards SET state_json = ? WHERE id = ?`,
		`{"version":"2","board":{"id":"`+kopie+`","title":"Kopie","sprints":[{"id":"__backlog__","kind":"backlog","cards":[],"spiegel":{"id":"__backlog__"}}]}}`, kopie); err != nil {
		t.Fatal(err)
	}
	pfad := makeBackup(t, env, kopie)
	_, err := boardbackup.RestoreBackup(ctx, boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreOverwrite, TargetBoardID: kopie,
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	})
	if !errors.Is(err, boardbackup.ErrRestoreAufKopie) {
		t.Fatalf("Überschreiben der Kopie: %v", err)
	}
	res, err := boardbackup.RestoreBackup(ctx, boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreAsNew,
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	})
	if err != nil {
		t.Fatalf("als neues Board: %v", err)
	}
	b, _ := env.boards.Get(ctx, res.BoardID)
	if strings.Contains(string(b.StateJSON), `"spiegel"`) {
		t.Errorf("neues Board trägt Spiegel-Marken: %s", b.StateJSON)
	}
}
