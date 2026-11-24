package boardbackup_test

// restore_732_test.go — Karte #732 (F-1816): Format-/Lesefehler der
// Archivdatei sind ErrBackupCorrupted (→ 400), echte interne Fehler nicht.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/Andiministrator/aBoard/server/boardbackup"
)

func restoreAusBytes(t *testing.T, env *testEnv, r io.Reader) error {
	t.Helper()
	_, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchiveReader: r, Mode: boardbackup.RestoreAsNew, RestoreUserEmail: "u@x.de",
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	})
	return err
}

func TestRestore732_NichtGzipIstBeschaedigt(t *testing.T) {
	env := newTestEnv(t)
	for name, inhalt := range map[string][]byte{
		"text": []byte("kein gzip"), "leer": {},
	} {
		err := restoreAusBytes(t, env, bytes.NewReader(inhalt))
		if !errors.Is(err, boardbackup.ErrBackupCorrupted) {
			t.Errorf("%s: err=%v, erwartet ErrBackupCorrupted", name, err)
		}
	}
}

func TestRestore732_GzipMitKaputtemTarIstBeschaedigt(t *testing.T) {
	env := newTestEnv(t)
	// gültiges gzip, Inhalt ist kein tar
	err := restoreAusBytes(t, env, bytes.NewReader(gzipBytes(t, bytes.Repeat([]byte("x"), 2048))))
	if !errors.Is(err, boardbackup.ErrBackupCorrupted) {
		t.Fatalf("kaputtes tar: err=%v, erwartet ErrBackupCorrupted", err)
	}
}

func TestRestore732_AbgeschnittenesArchivIstBeschaedigt(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Original")
	raw, _ := os.ReadFile(makeBackup(t, env, "b1"))
	err := restoreAusBytes(t, env, bytes.NewReader(raw[:len(raw)/2]))
	if !errors.Is(err, boardbackup.ErrBackupCorrupted) {
		t.Fatalf("abgeschnitten: err=%v, erwartet ErrBackupCorrupted", err)
	}
}

type kaputteQuelle struct{ err error }

func (k kaputteQuelle) Read([]byte) (int, error) { return 0, k.err }

// Interner Fehler: die QUELLE (Platte/Netz) schlägt fehl — die Datei ist
// nicht „beschädigt“, der Fehler bleibt 500.
func TestRestore732_QuellfehlerIstNichtBeschaedigt(t *testing.T) {
	env := newTestEnv(t)
	boese := errors.New("input/output error")
	err := restoreAusBytes(t, env, kaputteQuelle{boese})
	if err == nil || errors.Is(err, boardbackup.ErrBackupCorrupted) {
		t.Fatalf("Quellfehler am Anfang: err=%v, darf nicht ErrBackupCorrupted sein", err)
	}
	// Quelle bricht mitten im Archiv ab (nach gültigem Anfang).
	env.seedBoard(t, "b1", "Original")
	raw, _ := os.ReadFile(makeBackup(t, env, "b1"))
	r := io.MultiReader(bytes.NewReader(raw[:len(raw)/2]), kaputteQuelle{boese})
	err = restoreAusBytes(t, env, r)
	if err == nil || errors.Is(err, boardbackup.ErrBackupCorrupted) {
		t.Fatalf("Quellfehler mitten im Archiv: err=%v, darf nicht ErrBackupCorrupted sein", err)
	}
	if !errors.Is(err, boese) {
		t.Errorf("Ursprungsfehler ging verloren: %v", err)
	}
}

// Nicht vermischen: verschlüsselter Rahmenfehler bleibt ErrEncryptedCorrupt
// und wird nicht zusätzlich als ErrBackupCorrupted gemeldet (der Handler
// prüft ErrBackupCorrupted zuerst).
func TestRestore732_VerschluesselterRahmenfehlerBleibtEigenerSentinel(t *testing.T) {
	env := newTestEnv(t)
	c := crypterAus(t, keyA)
	env.seedBoard(t, "b1", "Original")
	raw, _ := os.ReadFile(makeBackupVerschluesselt(t, env, "b1", c))
	_, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchiveReader: bytes.NewReader(raw[:len(raw)-40]), Mode: boardbackup.RestoreAsNew,
		RestoreUserEmail: "u@x.de", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir, Crypter: c,
	})
	if !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Fatalf("err=%v, erwartet ErrEncryptedCorrupt", err)
	}
	if errors.Is(err, boardbackup.ErrBackupCorrupted) {
		t.Errorf("verschlüsselter Schaden darf nicht ErrBackupCorrupted sein: %v", err)
	}
}

// K-3 (Sprint 85): Bricht die QUELLE eines VERSCHLÜSSELTEN Archivs mitten im
// Block mit einem I/O-Fehler ab, darf das weder ErrEncryptedCorrupt noch
// ErrBackupCorrupted heißen (→ 500 statt 400); der Ursprungsfehler bleibt.
// Ein abgeschnittenes Archiv (EOF) bleibt ErrEncryptedCorrupt.
func TestRestore732_VerschluesselterQuellfehlerIstKeinSchaden(t *testing.T) {
	env := newTestEnv(t)
	c := crypterAus(t, keyA)
	env.seedBoard(t, "b1", "Original")
	raw, _ := os.ReadFile(makeBackupVerschluesselt(t, env, "b1", c))
	boese := errors.New("input/output error")
	restore := func(r io.Reader) error {
		_, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
			ArchiveReader: r, Mode: boardbackup.RestoreAsNew, RestoreUserEmail: "u@x.de",
			Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir, Crypter: c,
		})
		return err
	}
	// Abbruch an mehreren Stellen: im Kopf, mitten im ersten Block, später.
	for _, schnitt := range []int{3, 10, 40, len(raw) / 2, len(raw) - 40} {
		err := restore(io.MultiReader(bytes.NewReader(raw[:schnitt]), kaputteQuelle{boese}))
		if err == nil || errors.Is(err, boardbackup.ErrEncryptedCorrupt) || errors.Is(err, boardbackup.ErrBackupCorrupted) {
			t.Errorf("Schnitt %d: err=%v, darf weder ErrEncryptedCorrupt noch ErrBackupCorrupted sein", schnitt, err)
			continue
		}
		if !errors.Is(err, boese) {
			t.Errorf("Schnitt %d: Ursprungsfehler ging verloren: %v", schnitt, err)
		}
	}
	// Gegenprobe-Gegenstück: abgeschnitten (EOF, kein I/O-Fehler) bleibt Schaden.
	err := restore(bytes.NewReader(raw[:len(raw)/2]))
	if !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
		t.Errorf("abgeschnitten (EOF): err=%v, erwartet ErrEncryptedCorrupt", err)
	}
}
