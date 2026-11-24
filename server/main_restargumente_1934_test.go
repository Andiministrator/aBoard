package main

// F-1934 (gemeldet vom Partnerprojekt als F-907): Go `flag` hört am ersten
// Positionsargument auf. Optionen dahinter blieben ungelesen, und der Befehl
// lief mit den Voreinstellungen — bei `restore --force <archiv> --dry-run`
// heisst das: echtes Zurückspielen statt Probe.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/backup"
	"github.com/Andiministrator/aBoard/server/storage"
)

func boardTitel(t *testing.T, db string) string {
	t.Helper()
	h, err := storage.Open(context.Background(), storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var titel string
	if err := h.QueryRow(`SELECT title FROM boards WHERE id = 'b1'`).Scan(&titel); err != nil {
		t.Fatal(err)
	}
	return titel
}

func setzeBoardTitel(t *testing.T, db, titel string) {
	t.Helper()
	h, err := storage.Open(context.Background(), storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := h.Exec(`UPDATE boards SET title = ? WHERE id = 'b1'`, titel); err != nil {
		t.Fatal(err)
	}
}

// Das trennende Szenario: Ein Archiv mit Titel „Alt“, die laufende DB trägt
// „Neu“. Würde `--dry-run` hinter dem Pfad still ignoriert, stünde danach
// wieder „Alt“ in der DB.
func TestRestore_F1934_OptionenHinterDemPfadSpielenNichtsZurueck(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "db", "aboard.db")
	anhaenge := filepath.Join(dir, "attachments")
	if err := os.MkdirAll(anhaenge, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewBoardRepo(h).Insert(ctx, "b1", "Alt", "2", []byte(`{"version":"2","board":{"id":"b1"}}`)); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	archiv, err := backup.Run(ctx, backup.Options{
		DBPath: db, AttachmentsDir: anhaenge, Out: filepath.Join(dir, "sicherung.tar.gz"), AboardVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	setzeBoardTitel(t, db, "Neu")

	for _, args := range [][]string{
		{"--db", db, "--attachments", anhaenge, "--force", archiv, "--dry-run"},
		{"--db", db, "--attachments", anhaenge, archiv, "--dry-run"},
		{"--db", db, "--attachments", anhaenge, "--force", archiv, "zweites-archiv.tar.gz"},
	} {
		err := runRestore(args)
		if err == nil {
			t.Fatalf("%v: angenommen, erwartet Ablehnung", args)
		}
		if !strings.Contains(err.Error(), "unerwartete Argumente") {
			t.Fatalf("%v: falscher Fehler: %v", args, err)
		}
		if got := boardTitel(t, db); got != "Neu" {
			t.Fatalf("%v: DB zurückgespielt (Titel %q), obwohl abgelehnt", args, got)
		}
	}

	// Gegenrichtung: Optionen VOR dem Pfad wirken weiter — die Probe lässt die
	// DB stehen, `--force` spielt zurück. Ohne das wäre der Test auch mit einem
	// restore grün, der gar nichts mehr tut.
	if err := runRestore([]string{"--db", db, "--attachments", anhaenge, "--dry-run", archiv}); err != nil {
		t.Fatalf("--dry-run vor dem Pfad: %v", err)
	}
	if got := boardTitel(t, db); got != "Neu" {
		t.Fatalf("--dry-run hat zurückgespielt (Titel %q)", got)
	}
	if err := runRestore([]string{"--db", db, "--attachments", anhaenge, "--force", archiv}); err != nil {
		t.Fatalf("--force vor dem Pfad: %v", err)
	}
	if got := boardTitel(t, db); got != "Alt" {
		t.Fatalf("--force vor dem Pfad hat nicht zurückgespielt (Titel %q)", got)
	}
}

// Derselbe Mechanismus bei agent-token: `--role viewer` hinter einem
// Tippfehler blieb ungelesen, angelegt wurde ein editor-Token.
func TestAgentToken_F1934_OptionHinterTippfehlerLegtNichtsAn(t *testing.T) {
	db := filepath.Join(t.TempDir(), "aboard.db")
	ctx := context.Background()
	h, err := storage.Open(ctx, storage.Config{DBPath: db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewBoardRepo(h).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1"}}`)); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	var out bytes.Buffer
	err = runAgentTokenMit([]string{"create", "--db", db, "--name", "probe", "--board", "b1", "tippfehler", "--role", "viewer"}, &out)
	if err == nil || !strings.Contains(err.Error(), "unerwartete Argumente") {
		t.Fatalf("angenommen oder falscher Fehler: %v (%s)", err, out.String())
	}
	out.Reset()
	if err := runAgentTokenMit([]string{"list", "--db", db}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "probe") {
		t.Fatalf("Token trotz Ablehnung angelegt:\n%s", out.String())
	}
}

func TestSubcommands_F1934_RestargumenteAbgelehnt(t *testing.T) {
	dir := t.TempDir()
	if err := runBackup([]string{"--out", dir, "uebrig", "--keep-daily", "3"}); err == nil || !strings.Contains(err.Error(), "unerwartete Argumente") {
		t.Fatalf("backup: %v", err)
	}
	if err := runHealthcheck([]string{"uebrig", "--timeout", "1s"}); err == nil || !strings.Contains(err.Error(), "unerwartete Argumente") {
		t.Fatalf("healthcheck: %v", err)
	}
	if err := runImportMitArgs(filepath.Join(dir, "x.db"), []string{"a.json", "b.json"}); err == nil || !strings.Contains(err.Error(), "genau eine Datei") {
		t.Fatalf("import: %v", err)
	}
}

// Kritiker K-1: Ein Tippfehler im Unterbefehl startete still den Server —
// aus einem Sicherungsskript heraus ein zweiter Server auf derselben DB.
func TestUnterbefehl_F1934_TippfehlerAbgelehnt(t *testing.T) {
	for _, wort := range []string{"backp", "restor", "Restore", "agent-tokn", "helthcheck", "imprt", "acls", "hlp"} {
		if err := pruefeUnbekanntenUnterbefehl(wort); err == nil {
			t.Errorf("%q: angenommen, erwartet Tippfehler-Ablehnung", wort)
		}
	}
	// Gegenrichtung: andere Wörter und Flags starten wie bisher den Server.
	for _, wort := range []string{"serve", "server", "run", "start", "ab", "-v", "--irgendwas"} {
		if err := pruefeUnbekanntenUnterbefehl(wort); err != nil {
			t.Errorf("%q: abgelehnt, erwartet Serverstart: %v", wort, err)
		}
	}
}

// Nebenbefund des Kritikers: `agent-token <unbekannt>` legte erst die DB an.
func TestAgentToken_F1934_UnbekannterUnterbefehlLegtKeineDBAn(t *testing.T) {
	db := filepath.Join(t.TempDir(), "neu.db")
	var out bytes.Buffer
	if err := runAgentTokenMit([]string{"crate", "--db", db}, &out); err == nil {
		t.Fatal("unbekannter Unterbefehl angenommen")
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatalf("DB trotz unbekanntem Unterbefehl angelegt (Stat: %v)", err)
	}
}

// Kritiker Sprint 88 (F-C): `agent-token --help` verweist auf die Hilfe je
// Unterbefehl, statt „unbekannter Unterbefehl "--help"“ zu melden — und legt
// dabei keine DB an.
func TestAgentToken_HilfeOhneUnterbefehl(t *testing.T) {
	db := filepath.Join(t.TempDir(), "neu.db")
	t.Setenv("ABOARD_DB", db)
	for _, h := range []string{"--help", "-h", "help"} {
		var out bytes.Buffer
		if err := runAgentTokenMit([]string{h}, &out); err != nil || !strings.Contains(out.String(), "<unterbefehl> --help") {
			t.Fatalf("%s: err=%v out=%q", h, err, out.String())
		}
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatalf("Hilfe hat eine DB angelegt (Stat: %v)", err)
	}
}

// Kritiker K-3: acl meldet Restargumente wie die übrigen Unterbefehle.
func TestACL_F1934_RestargumenteEinheitlich(t *testing.T) {
	var out bytes.Buffer
	err := runACLMit(context.Background(), []string{"orphans", "uebrig", "--db", filepath.Join(t.TempDir(), "x.db")}, &out)
	if err == nil || !strings.Contains(err.Error(), "Optionen gehören vor die Positionsargumente") {
		t.Fatalf("acl: %v", err)
	}
}
