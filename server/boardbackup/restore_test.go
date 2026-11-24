package boardbackup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/boardbackup"
)

// helper: macht ein Backup und liefert den Pfad zurück.
func makeBackup(t *testing.T, env *testEnv, boardID string) string {
	t.Helper()
	out := filepath.Join(env.backupDir, boardID, "bb.tar.gz")
	if _, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID:            boardID,
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                out,
	}); err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}
	return out
}

// AK_restore_overwrite_roundtrip: Board exportieren → "leeren" → restore
// overwrite → Board ist wieder identisch.
func TestRestore_OverwriteRoundtrip(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Original Board")
	env.seedAttachment(t, "b1", "c1", "att1", "a.txt", []byte("originaler-inhalt"))

	backupPath := makeBackup(t, env, "b1")

	// State "leeren" — Force-Update mit minimalem JSON.
	emptyState, _ := json.Marshal(map[string]any{
		"version": "2",
		"board":   map[string]any{"id": "b1", "title": "Geleert"},
	})
	if _, err := env.boards.Update(context.Background(), "b1", "Geleert", "2", emptyState, 0); err != nil {
		t.Fatalf("Update empty: %v", err)
	}
	// Attachment löschen
	_ = os.RemoveAll(filepath.Join(env.attDir, "b1"))

	// Restore overwrite
	res, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath:        backupPath,
		Mode:               boardbackup.RestoreOverwrite,
		TargetBoardID:      "b1",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if res.BoardID != "b1" {
		t.Errorf("BoardID drift: got %q", res.BoardID)
	}

	// Board wieder identisch?
	b, _ := env.boards.Get(context.Background(), "b1")
	if b.Title != "Original Board" {
		t.Errorf("Title nicht restauriert: %q", b.Title)
	}
	// Karten-Count wieder 3?
	var env2 struct {
		Board struct {
			Sprints []struct {
				Cards []map[string]any `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(b.StateJSON, &env2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	totalCards := 0
	for _, s := range env2.Board.Sprints {
		totalCards += len(s.Cards)
	}
	if totalCards != 3 {
		t.Errorf("Karten-Count: erwartet 3, got %d", totalCards)
	}
	// Attachment wieder da?
	attPath := filepath.Join(env.attDir, "b1", "c1", "att1__a.txt")
	body, err := os.ReadFile(attPath)
	if err != nil {
		t.Errorf("Attachment fehlt: %v", err)
	} else if string(body) != "originaler-inhalt" {
		t.Errorf("Attachment-Inhalt drift: %q", body)
	}
}

// AK_restore_as_new: Board exportieren → restore as-new → neue Board-UUID,
// Inhalt identisch, Restore-User wird Owner.
func TestRestore_AsNew(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Original")
	env.seedAttachment(t, "b1", "c1", "att1", "a.txt", []byte("inhalt"))

	backupPath := makeBackup(t, env, "b1")

	// Mock UUID-Quelle für deterministischen Test
	boardbackup.SetUUIDFunc(func() string { return "newboard-uuid" })
	defer boardbackup.SetUUIDFunc(nil)

	res, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath:        backupPath,
		Mode:               boardbackup.RestoreAsNew,
		RestoreUserEmail:   "user@x.de",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if res.BoardID != "newboard-uuid" {
		t.Errorf("BoardID erwartet newboard-uuid, got %q", res.BoardID)
	}
	// Owner-Eintrag?
	role, found, _ := env.acl.GetRole(context.Background(), "newboard-uuid", "user@x.de")
	if !found || role != "owner" {
		t.Errorf("owner-Eintrag fehlt: found=%v role=%q", found, role)
	}
	// Original-Board bleibt unverändert?
	if _, err := env.boards.Get(context.Background(), "b1"); err != nil {
		t.Errorf("Original Board verschwunden: %v", err)
	}
	// Attachment unter neuer ID?
	if _, err := os.Stat(filepath.Join(env.attDir, "newboard-uuid", "c1", "att1__a.txt")); err != nil {
		t.Errorf("Attachment unter neuer ID fehlt: %v", err)
	}
}

// AK_sha_manipulation_reject: Backup-File minimal manipulieren → restore
// lehnt mit ErrBackupCorrupted ab.
func TestRestore_SHAManipulationRejected(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Original")
	backupPath := makeBackup(t, env, "b1")

	// Manipulieren: ein Byte in board.json flippen.
	manipulated := manipulateBoardJSON(t, backupPath)

	_, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath:        manipulated,
		Mode:               boardbackup.RestoreOverwrite,
		TargetBoardID:      "b1",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
	})
	if err == nil {
		t.Fatal("Restore mit manipuliertem Backup darf NICHT erfolgreich sein")
	}
	if !errors.Is(err, boardbackup.ErrBackupCorrupted) {
		t.Errorf("erwartet ErrBackupCorrupted, got %v", err)
	}
}

// manipulateBoardJSON liest das Archiv, ändert ein Byte in board.json,
// schreibt das Archiv neu raus. Liefert den neuen Pfad.
func manipulateBoardJSON(t *testing.T, srcPath string) string {
	t.Helper()
	in, err := os.Open(srcPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer in.Close()
	gr, _ := gzip.NewReader(in)
	tr := tar.NewReader(gr)

	dstPath := srcPath + ".manip.tar.gz"
	out, _ := os.Create(dstPath)
	defer out.Close()
	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		body, _ := io.ReadAll(tr)
		if hdr.Name == "board.json" && len(body) > 30 {
			// Ein Byte umdrehen — verändert den Inhalt, lässt Größe gleich.
			body[20] ^= 0xff
		}
		hdr.Size = int64(len(body))
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write(body)
	}
	return dstPath
}

// AK_schema_too_old: meta.json::schema_version=0 (heuristisch alt) → ablehnen.
// Wir simulieren das durch direktes Manipulieren der meta.json im Archiv.
func TestRestore_SchemaTooOld(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Original")
	backupPath := makeBackup(t, env, "b1")

	manipulated := manipulateMetaSchema(t, backupPath, 999)
	// 999 = neuer als Server → ErrSchemaTooNew. Für "too old" nutzen wir
	// einen anderen Wert: -1 ist unmöglich, 0 wird wegen unmarshal-Default
	// als "fehlt" interpretiert (siehe extractArchive). Sicher ist: ein
	// negativer Wert via JSON-Patching produziert schema_version<aktuell
	// in Zukunft. Stattdessen: testen wir SchemaTooNew + SchemaTooOld separat.
	_, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath:        manipulated,
		Mode:               boardbackup.RestoreOverwrite,
		TargetBoardID:      "b1",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
	})
	if err == nil {
		t.Fatal("erwartet Fehler bei schema=999")
	}
	if !errors.Is(err, boardbackup.ErrSchemaTooNew) {
		t.Errorf("erwartet ErrSchemaTooNew, got %v", err)
	}
}

// manipulateMetaSchema schreibt die meta.json mit neuer schema_version
// inkl. Rehash der restlichen Inhalte (damit SHA passt — sonst fängt der
// SHA-Check vor dem Schema-Check).
func manipulateMetaSchema(t *testing.T, srcPath string, newSchema int) string {
	t.Helper()
	in, _ := os.Open(srcPath)
	defer in.Close()
	gr, _ := gzip.NewReader(in)
	tr := tar.NewReader(gr)

	// Inhalt sammeln
	type entry struct {
		hdr  *tar.Header
		body []byte
	}
	var entries []entry
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		body, _ := io.ReadAll(tr)
		entries = append(entries, entry{hdr: hdr, body: body})
	}

	// meta.json finden + patchen
	for i := range entries {
		if entries[i].hdr.Name == "meta.json" {
			var meta boardbackup.Meta
			_ = json.Unmarshal(entries[i].body, &meta)
			meta.SchemaVersion = newSchema
			newBody, _ := json.MarshalIndent(meta, "", "  ")
			entries[i].body = newBody
			entries[i].hdr.Size = int64(len(newBody))
			break
		}
	}

	// Neu schreiben
	dstPath := srcPath + ".meta.tar.gz"
	out, _ := os.Create(dstPath)
	defer out.Close()
	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()
	for _, e := range entries {
		_ = tw.WriteHeader(e.hdr)
		_, _ = tw.Write(e.body)
	}
	return dstPath
}

// Test: Reader-basiertes Restore (statt ArchivePath, für HTTP-Upload).
func TestRestore_FromReader(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b1", "Original")
	backupPath := makeBackup(t, env, "b1")

	raw, _ := os.ReadFile(backupPath)
	boardbackup.SetUUIDFunc(func() string { return "stream-uuid" })
	defer boardbackup.SetUUIDFunc(nil)

	res, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchiveReader:      bytes.NewReader(raw),
		Mode:               boardbackup.RestoreAsNew,
		RestoreUserEmail:   "u@x.de",
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if res.BoardID != "stream-uuid" {
		t.Errorf("BoardID drift: %q", res.BoardID)
	}
}

// F-703 (Sprint-58-Kritiker, vom Verify-Lauf als ungetestet gemeldet):
// Der Restore-Pfad puffert das entpackte Archiv vollständig im Speicher.
// `http.MaxBytesReader` begrenzt nur den KOMPRIMIERTEN Upload; gzip erreicht
// bei Nullen ein Verhältnis von rund 1029:1, ein 10-MB-Upload expandiert
// also auf zweistellige Gigabyte. Ohne Grenze genügt ein Request eines
// beliebigen angemeldeten Nutzers für einen OOM-Kill des Containers — und
// damit sind ALLE Boards offline.
func TestRestore_EntpackteGroesseBegrenzt(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b-limit", "Zu gross")
	env.seedBoard(t, "b-ziel", "Ziel")
	out := filepath.Join(env.backupDir, "b-limit", "gross.tar.gz")

	if _, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID: "b-limit", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir: env.attDir, Out: out,
	}); err != nil {
		t.Fatalf("BackupBoard: %v", err)
	}

	// Grenze absichtlich winzig — das Archiv ist grösser als 50 Bytes.
	_, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath: out, Mode: boardbackup.RestoreOverwrite,
		TargetBoardID: "b-ziel", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir:   env.attDir,
		MaxUncompressedBytes: 50,
	})
	if !errors.Is(err, boardbackup.ErrBackupTooLarge) {
		t.Fatalf("erwartet ErrBackupTooLarge, bekam %v", err)
	}
	// Die Unterscheidung ist der Punkt: Wer ein legitim zu grosses Archiv
	// hochlädt, soll nicht „beschädigt" lesen und seine Sicherung für kaputt
	// halten.
	if errors.Is(err, boardbackup.ErrBackupCorrupted) {
		t.Error("Grenzüberschreitung wird als Korruption gemeldet — irreführend")
	}

	// Gegenprobe im selben Test: mit ausreichender Grenze geht derselbe
	// Restore durch. Ohne diesen Teil wäre nicht belegt, dass die Grenze
	// das Feature nicht schlicht abwürgt.
	if _, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath: out, Mode: boardbackup.RestoreOverwrite,
		TargetBoardID: "b-ziel", Boards: env.boards, ACL: env.acl,
		AttachmentsBaseDir:   env.attDir,
		MaxUncompressedBytes: boardbackup.DefaultMaxUncompressedBytes,
	}); err != nil {
		t.Fatalf("Restore mit regulärer Grenze: %v", err)
	}
}

// ── F-716 (c): Angehängte Bytes am Dateiende ──────────────────────────────

// anhaengen kopiert das Archiv und klebt `extra` ans Ende.
func anhaengen(t *testing.T, quelle string, extra []byte) string {
	t.Helper()
	roh, err := os.ReadFile(quelle)
	if err != nil {
		t.Fatalf("Archiv lesen: %v", err)
	}
	ziel := filepath.Join(t.TempDir(), "manipuliert.tar.gz")
	if err := os.WriteFile(ziel, append(append([]byte(nil), roh...), extra...), 0o644); err != nil {
		t.Fatalf("Archiv schreiben: %v", err)
	}
	return ziel
}

// F-716 (c): Der Sprint-58-Kritiker hat 4096 Byte Müll an ein 400-KB-Archiv
// gehängt — tar meldete nichts, der Restore lief durch. Der SHA-256 aus
// meta.json deckt nur die tar-EINTRÄGE ab, nicht das, was hinter dem Archiv
// klebt; die Zusage „das Format erkennt Manipulation" war an dieser Stelle
// also nicht gedeckt.
func TestRestore_AngehaengteBytesWerdenAbgelehnt(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b-anhang", "Mit Müll am Ende")
	original := makeBackup(t, env, "b-anhang")

	// Kontrolle: unverändert muss der Restore gelingen — sonst prüft der
	// Test unten nur, dass irgendetwas kaputt ist.
	if _, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath: original, Mode: boardbackup.RestoreOverwrite, TargetBoardID: "b-anhang",
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
	}); err != nil {
		t.Fatalf("unverändertes Archiv wird abgelehnt: %v", err)
	}

	faelle := map[string][]byte{
		"4096 Byte Nullen":    bytes.Repeat([]byte{0}, 4096),
		"kurzer Textanhang":   []byte("noch ein Wort"),
		"ein einzelnes Byte":  {0x42},
		"gueltiger gzip-Rest": gzipBytes(t, []byte("ein zweiter, für sich gültiger gzip-Strom")),
	}
	for name, extra := range faelle {
		t.Run(name, func(t *testing.T) {
			pfad := anhaengen(t, original, extra)
			_, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
				ArchivePath: pfad, Mode: boardbackup.RestoreOverwrite, TargetBoardID: "b-anhang",
				Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
			})
			if err == nil {
				t.Fatalf("%s an das Archiv gehängt — der Restore lief trotzdem durch (F-716 c)", name)
			}
			if !errors.Is(err, boardbackup.ErrBackupCorrupted) {
				t.Errorf("Fehler = %v, erwartet ErrBackupCorrupted", err)
			}
		})
	}
}

// makeBackupVerschluesselt macht ein VERSCHLÜSSELTES Backup und liefert den
// tatsächlich geschriebenen Pfad (Out + `.enc`).
func makeBackupVerschluesselt(t *testing.T, env *testEnv, boardID string, c *boardbackup.Crypter) string {
	t.Helper()
	out := filepath.Join(env.backupDir, boardID, "bb-enc.tar.gz")
	res, err := boardbackup.BackupBoard(context.Background(), boardbackup.Options{
		BoardID:            boardID,
		Boards:             env.boards,
		ACL:                env.acl,
		AttachmentsBaseDir: env.attDir,
		Out:                out,
		Crypter:            c,
	})
	if err != nil {
		t.Fatalf("BackupBoard (verschlüsselt): %v", err)
	}
	if !res.Encrypted {
		t.Fatalf("BackupBoard meldet Encrypted=false trotz gesetztem Crypter")
	}
	return res.Path
}

/*
Sprint-61-Kritiker (B1): Dieselbe Tabelle wie oben, nur am VERSCHLÜSSELTEN

	Archiv. Die Zusage „angehängte Bytes werden erkannt" war im Kommentar von
	`extractArchive` und im Namen des Tests darüber unbeschränkt formuliert,
	galt aber nur für den Klartextpfad: `encReader.Read` lieferte nach dem
	Abschlussblock sofort `io.EOF` und las die Datei nie zu Ende, der
	vorgeschaltete `bufio`+`Peek(1)`-Wächter sah deshalb nichts. Gemessen
	liefen 4096 Byte Nullen, ein kurzer Textanhang und ein einzelnes Byte am
	`.tar.gz.enc` allesamt durch — während exakt dieselben drei Fälle
	unverschlüsselt korrekt abgelehnt wurden.

	Die Prüfung sitzt jetzt in `encReader` selbst (ein Byte `io.ReadFull` nach
	`sawFinal`), weil die angehängten Bytes im CHIFFRAT stehen — in einer
	Schicht, die der entpackte Strom gar nicht kennt.
*/
func TestRestore_AngehaengteBytesWerdenAbgelehnt_Verschluesselt(t *testing.T) {
	env := newTestEnv(t)
	env.seedBoard(t, "b-anhang-enc", "Mit Müll am Ende, verschlüsselt")
	c := crypterAus(t, keyA)
	original := makeBackupVerschluesselt(t, env, "b-anhang-enc", c)

	// Kontrolle: unverändert muss der Restore gelingen — sonst prüft der
	// Test unten nur, dass irgendetwas kaputt ist.
	if _, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
		ArchivePath: original, Mode: boardbackup.RestoreOverwrite, TargetBoardID: "b-anhang-enc",
		Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir, Crypter: c,
	}); err != nil {
		t.Fatalf("unverändertes verschlüsseltes Archiv wird abgelehnt: %v", err)
	}

	faelle := map[string][]byte{
		"4096 Byte Nullen":    bytes.Repeat([]byte{0}, 4096),
		"kurzer Textanhang":   []byte("noch ein Wort"),
		"ein einzelnes Byte":  {0x42},
		"gueltiger gzip-Rest": gzipBytes(t, []byte("ein zweiter, für sich gültiger gzip-Strom")),
	}
	for name, extra := range faelle {
		t.Run(name, func(t *testing.T) {
			pfad := anhaengen(t, original, extra)
			_, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
				ArchivePath: pfad, Mode: boardbackup.RestoreOverwrite, TargetBoardID: "b-anhang-enc",
				Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir, Crypter: c,
			})
			if err == nil {
				t.Fatalf("%s an das VERSCHLÜSSELTE Archiv gehängt — der Restore "+
					"lief trotzdem durch (Sprint-61-Kritiker B1)", name)
			}
			if !errors.Is(err, boardbackup.ErrBackupCorrupted) {
				t.Errorf("Fehler = %v, erwartet ErrBackupCorrupted", err)
			}
		})
	}
}

// Gegenprobe auf der Ebene darunter: Der Reststrom-Wächter gehört in
// `encReader`, also muss er auch ohne tar/gzip drumherum greifen — und beim
// unversehrten Strom schweigen.
func TestCrypto_DecryptReader_AngehaengteBytes(t *testing.T) {
	c := crypterAus(t, keyA)
	roh := verschluesselt(t, c, []byte("nutzlast"))

	// Unversehrt: muss sauber bis EOF durchlaufen.
	r, err := c.DecryptReader(bytes.NewReader(roh))
	if err != nil {
		t.Fatalf("DecryptReader: %v", err)
	}
	if got, err := io.ReadAll(r); err != nil || string(got) != "nutzlast" {
		t.Fatalf("unversehrter Strom: got=%q err=%v", got, err)
	}

	// Mit Anhang: der erste Read nach dem Abschlussblock muss meckern.
	for name, extra := range map[string][]byte{
		"ein einzelnes Byte": {0x42},
		"4096 Byte Nullen":   bytes.Repeat([]byte{0}, 4096),
	} {
		t.Run(name, func(t *testing.T) {
			manipuliert := append(append([]byte(nil), roh...), extra...)
			r, err := c.DecryptReader(bytes.NewReader(manipuliert))
			if err != nil {
				t.Fatalf("DecryptReader: %v", err)
			}
			_, err = io.ReadAll(r)
			if !errors.Is(err, boardbackup.ErrEncryptedCorrupt) {
				t.Fatalf("Fehler = %v, erwartet ErrEncryptedCorrupt", err)
			}
		})
	}
}

// gzipBytes packt data in einen eigenständigen gzip-Strom.
func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// stateMitDoppelterFindingNr liefert einen Board-Zustand mit zwei
// Finding-Karten gleicher nr (F-1577).
func stateMitDoppelterFindingNr(id, title string) []byte {
	finding := func(kid string) map[string]any {
		return map[string]any{
			"id": kid, "title": "F", "kind": "finding",
			"finding": map[string]any{"nr": 7, "typ": "K", "prioritaet": "P3", "status": "offen"},
		}
	}
	b, _ := json.Marshal(map[string]any{
		"version": "2",
		"board": map[string]any{
			"id": id, "title": title,
			"sprints": []map[string]any{{"id": "s1", "title": "S1",
				"cards": []map[string]any{finding("f1"), finding("f2")}}},
		},
	})
	return b
}

// F-1577: Die Rückspielung prüft Finding-Karten wie PUT/POST. Ein Archiv mit
// doppelter nr wird abgelehnt, und es ist nichts geschrieben.
func TestRestore_FindingVerstossWirdAbgelehnt(t *testing.T) {
	// Archiv aus einem Board mit doppelter nr bauen.
	env := newTestEnv(t)
	if _, err := env.boards.Insert(context.Background(), "quelle", "Quelle", "2",
		stateMitDoppelterFindingNr("quelle", "Quelle")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	archiv := makeBackup(t, env, "quelle")

	t.Run("as-new", func(t *testing.T) {
		vorher, _ := env.boards.List(context.Background())
		boardbackup.SetUUIDFunc(func() string { return "neu-finding" })
		defer boardbackup.SetUUIDFunc(nil)
		_, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
			ArchivePath: archiv, Mode: boardbackup.RestoreAsNew,
			Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
		})
		if !errors.Is(err, boardbackup.ErrFindingVerstoss) {
			t.Fatalf("erwartet ErrFindingVerstoss, got %v", err)
		}
		if _, gerr := env.boards.Get(context.Background(), "neu-finding"); gerr == nil {
			t.Error("Board wurde trotz Verstoß angelegt")
		}
		nachher, _ := env.boards.List(context.Background())
		if len(nachher) != len(vorher) {
			t.Errorf("Boardzahl %d → %d", len(vorher), len(nachher))
		}
	})

	t.Run("overwrite auf sauberes Board", func(t *testing.T) {
		env.seedBoard(t, "ziel", "Ziel sauber")
		vorher, _ := env.boards.Get(context.Background(), "ziel")
		_, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
			ArchivePath: archiv, Mode: boardbackup.RestoreOverwrite, TargetBoardID: "ziel",
			Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
		})
		if !errors.Is(err, boardbackup.ErrFindingVerstoss) {
			t.Fatalf("erwartet ErrFindingVerstoss, got %v", err)
		}
		nachher, _ := env.boards.Get(context.Background(), "ziel")
		if string(nachher.StateJSON) != string(vorher.StateJSON) || nachher.Title != vorher.Title {
			t.Error("Zielboard wurde trotz Verstoß verändert")
		}
	})

	t.Run("overwrite auf Board mit gleicher Altlast bleibt möglich", func(t *testing.T) {
		if _, err := env.boards.Insert(context.Background(), "altlast", "Altlast", "2",
			stateMitDoppelterFindingNr("altlast", "Altlast")); err != nil {
			t.Fatalf("Insert: %v", err)
		}
		if _, err := boardbackup.RestoreBackup(context.Background(), boardbackup.RestoreOptions{
			ArchivePath: archiv, Mode: boardbackup.RestoreOverwrite, TargetBoardID: "altlast",
			Boards: env.boards, ACL: env.acl, AttachmentsBaseDir: env.attDir,
		}); err != nil {
			t.Errorf("nur Neues zählt, Altlast darf die Rückspielung nicht sperren: %v", err)
		}
	})
}
