package boardbackup_test

// restore_1685_test.go — die Archiv-Rückspielung bringt eine seit dem Sichern
// gelöschte Finding-Karte mit ihrer URSPRÜNGLICHEN Nummer zurück (F-1685):
// derselbe Befund, kein neuer; der Zähler wird trotzdem nicht abgesenkt.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Andiministrator/aBoard/server/boardbackup"
)

func TestRestoreOverwrite_F1685_FindingBehaeltNummer(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	mit := []byte(`{"version":"2","board":{"id":"f1","title":"F","nextFindingNr":8,"sprints":[{"id":"s","cards":[` +
		`{"id":"k7","kind":"finding","finding":{"nr":7,"typ":"K","prioritaet":"P2","status":"offen"}}]}]}}`)
	if _, err := env.boards.Insert(ctx, "f1", "F", "2", mit); err != nil {
		t.Fatal(err)
	}
	pfad := makeBackup(t, env, "f1")
	// Seither: F-7 gelöscht, Zähler auf 9 gewachsen.
	ohne := []byte(`{"version":"2","board":{"id":"f1","title":"F","nextFindingNr":9,"sprints":[{"id":"s","cards":[]}]}}`)
	if _, err := env.boards.Update(ctx, "f1", "F", "2", ohne, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := boardbackup.RestoreBackup(ctx, boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreOverwrite, TargetBoardID: "f1",
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	b, err := env.boards.Get(ctx, "f1")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Board struct {
			Next    int `json:"nextFindingNr"`
			Sprints []struct {
				Cards []struct {
					Finding struct {
						Nr int `json:"nr"`
					} `json:"finding"`
				} `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(b.StateJSON, &s); err != nil {
		t.Fatal(err)
	}
	if got := s.Board.Sprints[0].Cards[0].Finding.Nr; got != 7 {
		t.Errorf("zurückgespielte Karte: F-%d, erwartet F-7 (ursprüngliche Nummer)", got)
	}
	if s.Board.Next != 9 {
		t.Errorf("nextFindingNr = %d, erwartet 9 (nicht abgesenkt)", s.Board.Next)
	}
}
