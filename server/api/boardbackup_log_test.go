package api_test

// Karte #726: Jede Rückspielung hinterlässt eine Logzeile — die geglückte und
// die vom Restore selbst abgelehnte. Bis v1.48.0 war ein Restore im Nachhinein
// nicht belegbar: Das Containerlog eines Team-Betriebs der Nacht vom 06.10. enthielt für zwei
// Rückspielungen keine einzige Zeile.

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/boardbackup"
)

func logMitschneiden(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	altOut, altFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(altOut); log.SetFlags(altFlags) })
	return &buf
}

func TestBackupAPI_RestoreSchreibtLogzeile(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")

	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("backup/now: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fn, _ := body["filename"].(string)
	backupPath := filepath.Join(env.bkpDir, "b1", fn)

	buf := logMitschneiden(t)
	resp = env.uploadRestore(t, "owner@x.de", backupPath, "overwrite", "b1")
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("Restore sollte 200 sein, got %d (%s)", resp.StatusCode, b)
	}
	got := buf.String()
	if !strings.Contains(got, `restore ok (mode="overwrite", board="b1"`) {
		t.Errorf("Logzeile für geglückten Restore fehlt, Log:\n%s", got)
	}
	if strings.Contains(got, "owner@x.de") {
		t.Errorf("Adresse gehört nicht ins Log:\n%s", got)
	}
}

func TestBackupAPI_AbgelehnterRestoreSchreibtLogzeile(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")

	kaputt := filepath.Join(t.TempDir(), "kaputt.tar.gz")
	if err := os.WriteFile(kaputt, []byte("kein gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := logMitschneiden(t)
	resp := env.uploadRestore(t, "owner@x.de", kaputt, "overwrite", "b1")
	// #732 (F-1816): Nicht-gzip ist ein beschädigtes Archiv → 400 mit dem
	// Text von ErrBackupCorrupted, nicht 500 „restore failed“.
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Status %d, erwartet 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), boardbackup.ErrBackupCorrupted.Error()) {
		t.Errorf("Antworttext nennt ErrBackupCorrupted nicht: %s", body)
	}
	if strings.Contains(string(body), "restore failed") || strings.Contains(string(body), kaputt) {
		t.Errorf("Antwort darf weder „restore failed“ noch den Pfad enthalten: %s", body)
	}
	got := buf.String()
	if !strings.Contains(got, `restore abgelehnt (mode="overwrite", ziel="b1")`) {
		t.Errorf("Logzeile für abgelehnten Restore fehlt, Log:\n%s", got)
	}
	if strings.Contains(got, "owner@x.de") {
		t.Errorf("Adresse gehört nicht ins Log:\n%s", got)
	}
}

// Kritiker A-1: `mode` ist ein roher Formularwert. Ein Zeilenumbruch darin
// darf keine zweite, gefälschte Logzeile erzeugen.
func TestBackupAPI_RestoreLogzeileNichtFaelschbar(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")

	kaputt := filepath.Join(t.TempDir(), "kaputt.tar.gz")
	if err := os.WriteFile(kaputt, []byte("kein gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	buf := logMitschneiden(t)
	log.SetFlags(0)
	gefaelscht := "as-new\naboard-server: restore ok (mode=overwrite, board=\"b1\", finding_warnungen=0)"
	resp := env.uploadRestore(t, "owner@x.de", kaputt, gefaelscht, "")
	if resp.StatusCode == 200 {
		t.Fatalf("kaputtes Archiv darf nicht angenommen werden")
	}
	for _, z := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.HasPrefix(z, "aboard-server: restore ok") {
			t.Errorf("eingeschleuste Logzeile: %q\nganzes Log:\n%s", z, buf.String())
		}
	}
	if !strings.Contains(buf.String(), `restore abgelehnt (mode="as-new\n`) {
		t.Errorf("Modus nicht gequotet geloggt:\n%s", buf.String())
	}
}
