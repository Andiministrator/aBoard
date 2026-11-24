package boardbackup_test

// Karte #780 (ADR-0140 §1, Katalog „Backup je Board“): Die Board-Sicherung
// enthält keine Schatten, und das Zurückspielen — überschreibend wie als
// neues Board — lässt die Schatten unberührt.

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/schatten/schattentest"
	"github.com/Andiministrator/aBoard/server/storage"
)

func schattenEnv780(t *testing.T) (*testEnv, *sql.DB) {
	t.Helper()
	tmp := t.TempDir()
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(tmp, "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	env := &testEnv{boards: storage.NewBoardRepo(db), acl: storage.NewAclRepo(db),
		attDir: filepath.Join(tmp, "attachments"), backupDir: filepath.Join(tmp, "backups")}
	env.seedBoard(t, "b1", "Original Board")
	schattentest.Bestuecke(t, db, "b1")
	schattentest.MussBestueckt(t, db, "b1")
	return env, db
}

func TestSchatten780_SicherungOhneSchatten(t *testing.T) {
	env, _ := schattenEnv780(t)
	pfad := makeBackup(t, env, "b1")
	roh, err := os.ReadFile(pfad)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(roh))
	if err != nil {
		t.Fatal(err)
	}
	inhalt, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(inhalt, []byte("Original Board")) {
		t.Fatal("Sicherung enthält das Board nicht — Prüfung wäre leer")
	}
	for _, verboten := range []string{schattentest.Marker, "innen.example", "schatten_id", "schattenId"} {
		if bytes.Contains(inhalt, []byte(verboten)) {
			t.Errorf("Sicherung enthält %q", verboten)
		}
	}
}

func TestSchatten780_RestoreUeberschreibt(t *testing.T) {
	env, db := schattenEnv780(t)
	pfad := makeBackup(t, env, "b1")
	leer, _ := json.Marshal(map[string]any{"version": "2", "board": map[string]any{"id": "b1", "title": "Geleert"}})
	// Direkt per SQL, nicht über boards.Update: Gemessen wird nur der
	// Restore. Ein Vorbereitungsschritt über denselben Schreibweg verdeckte
	// sonst dessen Wirkung, weil die Momentaufnahme erst danach entsteht
	// (Gegenprobe vom 10.10.2026).
	if _, err := db.Exec(`UPDATE boards SET title = 'Geleert', state_json = ? WHERE id = 'b1'`, string(leer)); err != nil {
		t.Fatal(err)
	}
	schattentest.MussBestueckt(t, db, "b1")
	vorher := schattentest.Momentaufnahme(t, db)

	if _, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreOverwrite, TargetBoardID: "b1",
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if b, _ := env.boards.Get(context.Background(), "b1"); b.Title != "Original Board" {
		t.Fatalf("Restore hat nicht geschrieben: %q", b.Title)
	}
	schattentest.Unveraendert(t, db, vorher, "Restore überschreibend")
}

func TestSchatten780_RestoreAlsNeu(t *testing.T) {
	env, db := schattenEnv780(t)
	pfad := makeBackup(t, env, "b1")
	vorher := schattentest.Momentaufnahme(t, db)

	boardbackup.SetUUIDFunc(func() string { return "neu-780" })
	defer boardbackup.SetUUIDFunc(nil)
	if _, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath: pfad, Mode: boardbackup.RestoreAsNew, RestoreUserEmail: "anna@example.com",
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if _, err := env.boards.Get(context.Background(), "neu-780"); err != nil {
		t.Fatalf("Restore hat kein neues Board angelegt: %v", err)
	}
	schattentest.Unveraendert(t, db, vorher, "Restore als neues Board")
}
