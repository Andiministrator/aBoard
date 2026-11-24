package boardbackup_test

// restore_n1_test.go — Nacharbeit N1 (Sprint 82): Notfall-Schalter für die
// Finding-Prüfung (KA-7) und die Schleusen-Sperre bei der Rückspielung
// (F-1583, ADR-0134 §2).

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/boardbackup"
)

const standMitFindingVerstoss = `{"version":"2","board":{"id":"%ID%","title":"Altlast","sprints":[{"id":"s","cards":[` +
	`{"id":"a","kind":"finding","finding":{"nr":1,"typ":"K","prioritaet":"P2","status":"offen"}},` +
	`{"id":"b","kind":"finding","finding":{"nr":1,"typ":"K","prioritaet":"P2","status":"offen"}}]}]}}`

func seedAltlast(t *testing.T, env *testEnv, id string) string {
	t.Helper()
	state := []byte(replaceID(standMitFindingVerstoss, id))
	if _, err := env.boards.Insert(context.Background(), id, "Altlast", "2", state); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return makeBackup(t, env, id)
}

func replaceID(s, id string) string {
	out := ""
	for i := 0; i < len(s); {
		if i+4 <= len(s) && s[i:i+4] == "%ID%" {
			out += id
			i += 4
			continue
		}
		out += string(s[i])
		i++
	}
	return out
}

// KA-7: Ein Archiv mit Finding-Verstoß lässt sich als neues Board standardmäßig
// nicht zurückspielen — mit dem Notfall-Schalter doch, mit Warnung.
func TestRestoreAsNew_FindingVerstossNotfallSchalter(t *testing.T) {
	env := newTestEnv(t)
	pfad := seedAltlast(t, env, "alt1")
	opts := boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreAsNew,
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	}
	if _, err := boardbackup.RestoreBackup(context.Background(), opts); !errors.Is(err, boardbackup.ErrFindingVerstoss) {
		t.Fatalf("Standard: err=%v, erwartet ErrFindingVerstoss", err)
	}
	opts.FindingsPruefungAus = true
	res, err := boardbackup.RestoreBackup(context.Background(), opts)
	if err != nil {
		t.Fatalf("mit Notfall-Schalter: %v", err)
	}
	if len(res.FindingWarnungen) == 0 {
		t.Error("keine Warnung zurückgegeben")
	}
	if _, err := env.boards.Get(context.Background(), res.BoardID); err != nil {
		t.Errorf("Board nicht angelegt: %v", err)
	}
}

// KA-7: dasselbe beim Überschreiben eines sauberen Boards.
func TestRestoreOverwrite_FindingVerstossNotfallSchalter(t *testing.T) {
	env := newTestEnv(t)
	pfad := seedAltlast(t, env, "alt2")
	sauber := []byte(`{"version":"2","board":{"id":"alt2","title":"Sauber","sprints":[{"id":"s","cards":[]}]}}`)
	if _, err := env.boards.Update(context.Background(), "alt2", "Sauber", "2", sauber, 0); err != nil {
		t.Fatal(err)
	}
	opts := boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreOverwrite, TargetBoardID: "alt2",
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	}
	if _, err := boardbackup.RestoreBackup(context.Background(), opts); !errors.Is(err, boardbackup.ErrFindingVerstoss) {
		t.Fatalf("Standard: err=%v, erwartet ErrFindingVerstoss", err)
	}
	opts.FindingsPruefungAus = true
	res, err := boardbackup.RestoreBackup(context.Background(), opts)
	if err != nil || len(res.FindingWarnungen) == 0 {
		t.Fatalf("mit Notfall-Schalter: err=%v warnungen=%v", err, res)
	}
}

// F-1583: Die Board-Sperre der Ausgangsschleuse überlebt die Rückspielung als
// neues Board — auch wenn das Quell-Board nicht mehr existiert.
func TestRestoreAsNew_UebernimmtSchleusenSperre(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.seedBoard(t, "b1", "Gesperrt")
	if err := env.boards.SetzeSchleuseSperre(ctx, "b1", true, "owner@x.de"); err != nil {
		t.Fatal(err)
	}
	pfad := makeBackup(t, env, "b1")

	boardbackup.SetUUIDFunc(func() string { return "neu-gesperrt" })
	defer boardbackup.SetUUIDFunc(nil)
	res, err := boardbackup.RestoreBackup(ctx, boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreAsNew, RestoreUserEmail: "u@x.de",
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if g, err := env.boards.SchleuseGesperrt(ctx, res.BoardID); err != nil || !g {
		t.Fatalf("neues Board: gesperrt=%v err=%v, erwartet true", g, err)
	}

	// Ein nicht gesperrtes Board bleibt ungesperrt.
	env.seedBoard(t, "b2", "Frei")
	pfad2 := filepath.Join(env.backupDir, "b2", "bb.tar.gz")
	if _, err := boardbackup.BackupBoard(ctx, boardbackup.Options{BoardID: "b2", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir, Out: pfad2}); err != nil {
		t.Fatal(err)
	}
	boardbackup.SetUUIDFunc(func() string { return "neu-frei" })
	res2, err := boardbackup.RestoreBackup(ctx, boardbackup.RestoreOptions{
		ArchivePath: pfad2, Mode: boardbackup.RestoreAsNew,
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := env.boards.SchleuseGesperrt(ctx, res2.BoardID); g {
		t.Error("ungesperrtes Board wurde gesperrt")
	}
}

// F-1583: Die Rückspielung in dasselbe Board behält die Sperre.
func TestRestoreOverwrite_BehaeltSchleusenSperre(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.seedBoard(t, "b1", "Gesperrt")
	if err := env.boards.SetzeSchleuseSperre(ctx, "b1", true, "owner@x.de"); err != nil {
		t.Fatal(err)
	}
	pfad := makeBackup(t, env, "b1")
	if _, err := boardbackup.RestoreBackup(ctx, boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreOverwrite, TargetBoardID: "b1",
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	}); err != nil {
		t.Fatal(err)
	}
	if g, err := env.boards.SchleuseGesperrt(ctx, "b1"); err != nil || !g {
		t.Fatalf("gesperrt=%v err=%v, erwartet true", g, err)
	}
}

// Verify VO-3: Jede der beiden Quellen der Sperre trägt für sich.
// (a) meta.json allein: Das Quell-Board ist gelöscht, die Live-Abfrage findet nichts.
func TestRestoreAsNew_SperreAusMetaOhneQuellBoard(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.seedBoard(t, "b1", "Gesperrt")
	if err := env.boards.SetzeSchleuseSperre(ctx, "b1", true, "owner@x.de"); err != nil {
		t.Fatal(err)
	}
	pfad := makeBackup(t, env, "b1")
	if err := env.boards.Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	boardbackup.SetUUIDFunc(func() string { return "neu-aus-meta" })
	defer boardbackup.SetUUIDFunc(nil)
	res, err := boardbackup.RestoreBackup(ctx, boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreAsNew,
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if g, err := env.boards.SchleuseGesperrt(ctx, res.BoardID); err != nil || !g {
		t.Fatalf("gesperrt=%v err=%v, erwartet true (Quelle meta.json)", g, err)
	}
}

// (b) Quell-Board allein: Das Archiv trägt keine Sperre (vor der Sperre
// gezogen — derselbe Fall wie ein bearbeitetes meta.json), das Quell-Board ist
// heute gesperrt.
func TestRestoreAsNew_SperreAusQuellBoardTrotzArchivOhne(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.seedBoard(t, "b1", "Später gesperrt")
	pfad := makeBackup(t, env, "b1")
	if err := env.boards.SetzeSchleuseSperre(ctx, "b1", true, "owner@x.de"); err != nil {
		t.Fatal(err)
	}
	boardbackup.SetUUIDFunc(func() string { return "neu-aus-live" })
	defer boardbackup.SetUUIDFunc(nil)
	res, err := boardbackup.RestoreBackup(ctx, boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreAsNew,
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if g, err := env.boards.SchleuseGesperrt(ctx, res.BoardID); err != nil || !g {
		t.Fatalf("gesperrt=%v err=%v, erwartet true (Quelle Quell-Board)", g, err)
	}
}
