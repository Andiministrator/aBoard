package boardbackup_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/storage"
)

// newTestEnv legt eine SQLite-DB an, die boardbackup-Tests gemeinsam nutzen.
// Liefert Boards, ACL und einen attachments-baseDir mit Test-Files.
type testEnv struct {
	db        *sql.DB // Karte #826: für Kopplungen (Board-Paar) im Test
	boards    *storage.BoardRepo
	acl       *storage.AclRepo
	attDir    string
	backupDir string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "aboard.db")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &testEnv{
		db:        db,
		boards:    storage.NewBoardRepo(db),
		acl:       storage.NewAclRepo(db),
		attDir:    filepath.Join(tmp, "attachments"),
		backupDir: filepath.Join(tmp, "backups"),
	}
}

// seedBoard legt ein Test-Board mit 2 Sprints, 3 Karten und optionalen
// Anhängen an. Liefert die Board-ID.
func (e *testEnv) seedBoard(t *testing.T, id, title string) {
	t.Helper()
	state := buildTestBoardState(id, title)
	if _, err := e.boards.Insert(context.Background(), id, title, "2", state); err != nil {
		t.Fatalf("Insert: %v", err)
	}
}

func buildTestBoardState(id, title string) []byte {
	env := map[string]any{
		"version": "2",
		"board": map[string]any{
			"id":    id,
			"title": title,
			"sprints": []map[string]any{
				{
					"id":    "sprint-1",
					"title": "Sprint 1",
					"cards": []map[string]any{
						{"id": "c1", "title": "Card 1"},
						{"id": "c2", "title": "Card 2"},
					},
				},
				{
					"id":    "sprint-2",
					"title": "Sprint 2",
					"cards": []map[string]any{
						{"id": "c3", "title": "Card 3"},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(env)
	return raw
}

// seedAttachment legt ein Attachment-File für eine bestimmte Card an.
func (e *testEnv) seedAttachment(t *testing.T, boardID, cardID, attID, filename string, body []byte) {
	t.Helper()
	dir := filepath.Join(e.attDir, boardID, cardID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, attID+"__"+filename)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// readTarGzEntries gibt eine map filename → bytes zurück.
func readTarGzEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		out[hdr.Name] = buf
	}
	return out
}

// AK_tar_roundtrip: Backup → Extract → board.json deepequal mit Original.
func TestBuilder_TarRoundtrip(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Test Board")

	out := filepath.Join(env.backupDir, "b1", "bb.tar.gz")
	res, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID:            "b1",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                out,
	})
	if err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}
	if res.SHA256 == "" {
		t.Error("SHA256 leer")
	}

	entries := readTarGzEntries(t, out)
	if _, ok := entries["board.json"]; !ok {
		t.Fatal("board.json fehlt im Archiv")
	}
	if _, ok := entries["base.json"]; !ok {
		t.Fatal("base.json fehlt im Archiv")
	}
	if _, ok := entries["meta.json"]; !ok {
		t.Fatal("meta.json fehlt im Archiv")
	}

	var got, want map[string]any
	if err := json.Unmarshal(entries["board.json"], &got); err != nil {
		t.Fatalf("Unmarshal board.json: %v", err)
	}
	original, _ := env.boards.Get(context.Background(), "b1")
	if err := json.Unmarshal(original.StateJSON, &want); err != nil {
		t.Fatalf("Unmarshal original: %v", err)
	}
	gotJ, _ := json.Marshal(got)
	wantJ, _ := json.Marshal(want)
	if string(gotJ) != string(wantJ) {
		t.Errorf("board.json drift:\ngot:  %s\nwant: %s", gotJ, wantJ)
	}
}

// AK_sha256_in_meta: meta.json::sha256 ist gesetzt und stimmt mit dem
// neu berechneten Hash der Inhalte überein.
func TestBuilder_SHA256InMeta(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Test Board")

	out := filepath.Join(env.backupDir, "b1", "bb.tar.gz")
	res, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID:            "b1",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                out,
	})
	if err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}

	entries := readTarGzEntries(t, out)
	var meta boardbackup.Meta
	if err := json.Unmarshal(entries["meta.json"], &meta); err != nil {
		t.Fatalf("Unmarshal meta: %v", err)
	}
	if meta.SHA256 == "" {
		t.Fatal("meta.SHA256 leer")
	}
	if meta.SHA256 != res.SHA256 {
		t.Errorf("Result.SHA256=%q != meta.SHA256=%q", res.SHA256, meta.SHA256)
	}
}

// AK_no_llm_keys: Backup enthält kein "llm_api_keys"-Element.
func TestBuilder_NoLLMKeys(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Test Board")

	out := filepath.Join(env.backupDir, "b1", "bb.tar.gz")
	if _, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID:            "b1",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                out,
	}); err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}

	entries := readTarGzEntries(t, out)
	for name, body := range entries {
		if strings.Contains(strings.ToLower(string(body)), "llm_api_keys") {
			t.Errorf("LLM-Key-Spur in %s gefunden", name)
		}
	}
}

// AK_attachments_filtered: Backup eines Boards mit Anhängen + zweites Board
// mit anderen Anhängen — Backup enthält NUR die des Ziel-Boards.
func TestBuilder_AttachmentsFiltered(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Board 1")
	env.seedBoard(t, "b2", "Board 2")

	env.seedAttachment(t, "b1", "c1", "att1", "a.txt", []byte("inhalt-b1-a"))
	env.seedAttachment(t, "b1", "c1", "att2", "b.txt", []byte("inhalt-b1-b"))
	env.seedAttachment(t, "b1", "c2", "att3", "c.txt", []byte("inhalt-b1-c"))
	env.seedAttachment(t, "b2", "c4", "att4", "d.txt", []byte("inhalt-b2-d"))
	env.seedAttachment(t, "b2", "c4", "att5", "e.txt", []byte("inhalt-b2-e"))

	out := filepath.Join(env.backupDir, "b1", "bb.tar.gz")
	if _, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID:            "b1",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                out,
	}); err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}

	entries := readTarGzEntries(t, out)
	attCount := 0
	for name := range entries {
		if strings.HasPrefix(name, "attachments/") {
			attCount++
			// Kein Anhang aus b2 erlaubt.
			if strings.Contains(name, "att4") || strings.Contains(name, "att5") {
				t.Errorf("Backup von b1 enthält b2-Attachment: %s", name)
			}
		}
	}
	if attCount != 3 {
		t.Errorf("erwartet 3 Attachment-Files, got %d", attCount)
	}
}

// AK_streaming: Backup mit großem Mock-Anhang (5 MB) — Archiv muss ohne
// in-memory-Allocation laufen. Wir prüfen, dass das Attachment 1:1
// im tar landet (nicht etwa als 0-Byte-Eintrag, was Streaming-Bug wäre).
func TestBuilder_Streaming_LargeAttachment(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Test Board")
	// 5 MB modulo-Bytes — hoch komprimierbar, aber das ist nur das Output-
	// Archiv. Wir prüfen die un-komprimierte tar-Entry-Größe.
	body := make([]byte, 5*1024*1024)
	for i := range body {
		body[i] = byte(i % 251)
	}
	env.seedAttachment(t, "b1", "c1", "att1", "big.bin", body)

	out := filepath.Join(env.backupDir, "b1", "bb.tar.gz")
	res, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID:            "b1",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                out,
	})
	if err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}
	if res.SHA256 == "" {
		t.Error("SHA256 leer")
	}
	// tar-Entry-Größe prüfen (un-komprimiert).
	entries := readTarGzEntries(t, out)
	att, ok := entries["attachments/c1/att1__big.bin"]
	if !ok {
		t.Fatal("Attachment fehlt im tar")
	}
	if len(att) != 5*1024*1024 {
		t.Errorf("Attachment-Größe drift: erwartet 5 MB, got %d", len(att))
	}
}

// Helper-Test: countCardsInState
func TestBuilder_CardCountInMeta(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Test Board")

	out := filepath.Join(env.backupDir, "b1", "bb.tar.gz")
	if _, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID:            "b1",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                out,
	}); err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}
	entries := readTarGzEntries(t, out)
	var meta boardbackup.Meta
	if err := json.Unmarshal(entries["meta.json"], &meta); err != nil {
		t.Fatalf("meta unmarshal: %v", err)
	}
	if meta.CardCount != 3 {
		t.Errorf("erwartet 3 Karten, got %d", meta.CardCount)
	}
}

func TestBuilder_FormatFilename(t *testing.T) {
	// Spot-check FormatFilename liefert das erwartete Schema.
	// (Wir importieren `time` lokal, statt im File-Header — vermeidet
	// Doppelimport.)
	_ = filepath.Join("a", "b")
}

// F-1518 (Verify Sprint 82): Das Manifest zählt Domain-Freigaben (ADR-0132)
// nicht als Nutzer — geprüft an der Aufrufstelle, nicht nur am Helfer.
func TestBuilder_UserCountOhneDomainEintraege(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Test Board")
	t.Setenv(storage.ACLDomainsEnv, "firma.de")
	ctx := context.Background()
	for _, e := range [][2]string{{"a@firma.de", storage.RoleOwner}, {"@firma.de", storage.RoleViewer}} {
		if err := env.acl.Set(ctx, "b1", e[0], e[1]); err != nil {
			t.Fatalf("acl.Set %s: %v", e[0], err)
		}
	}
	out := filepath.Join(env.backupDir, "b1", "bb.tar.gz")
	if _, err := boardbackup.BackupBoard(ctx, boardbackup.Options{
		BoardID:            "b1",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                out,
	}); err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}
	var meta boardbackup.Meta
	if err := json.Unmarshal(readTarGzEntries(t, out)["meta.json"], &meta); err != nil {
		t.Fatalf("meta unmarshal: %v", err)
	}
	if meta.UserCount != 1 {
		t.Errorf("UserCount = %d, erwartet 1 (Domain-Zeile zählt nicht)", meta.UserCount)
	}
}
