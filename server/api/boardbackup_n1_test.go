package api_test

// boardbackup_n1_test.go — Nacharbeit N1 (Sprint 82), KA-7: Notfall-Schalter
// `findings_pruefung=aus` an POST /backup/restore.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"os"
	"strings"
	"testing"
)

func (e *bbTestEnv) restoreMitFeld(t *testing.T, archivePath string, felder map[string]string) (int, string) {
	t.Helper()
	return e.restoreMitFeldAls(t, "owner@x.de", archivePath, felder)
}

func (e *bbTestEnv) restoreMitFeldAls(t *testing.T, wer, archivePath string, felder map[string]string) (int, string) {
	t.Helper()
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	for k, v := range felder {
		_ = mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", "backup.tar.gz")
	_, _ = fw.Write(raw)
	_ = mw.Close()
	resp := e.do(t, "POST", "/backup/restore", wer, buf, mw.FormDataContentType())
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestBackupAPI_Restore_FindingsPruefungAus(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Altlast", "owner@x.de")
	schlecht := `{"version":"2","board":{"id":"b1","title":"Altlast","sprints":[{"id":"s","cards":[` +
		`{"id":"a","kind":"finding","finding":{"nr":1,"typ":"K","prioritaet":"P2","status":"offen"}},` +
		`{"id":"b","kind":"finding","finding":{"nr":1,"typ":"K","prioritaet":"P2","status":"offen"}}]}]}}`
	if _, err := env.boards.Update(context.Background(), "b1", "Altlast", "2", []byte(schlecht), 0); err != nil {
		t.Fatal(err)
	}
	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fn, _ := body["filename"].(string)
	pfad := env.bkpDir + "/b1/" + fn

	st, antwort := env.restoreMitFeld(t, pfad, map[string]string{"mode": "as-new"})
	if st != 422 || !strings.Contains(antwort, "findings_pruefung=aus") {
		t.Fatalf("Standard: %d %s, erwartet 422 mit Hinweis auf den Notfall-Schalter", st, antwort)
	}
	// F-1597: Der Board-Owner ohne Admin-Recht darf den Schalter nicht setzen.
	st, antwort = env.restoreMitFeld(t, pfad, map[string]string{"mode": "as-new", "findings_pruefung": "aus"})
	if st != 403 || !strings.Contains(antwort, "findings_pruefung") {
		t.Fatalf("Owner mit Schalter: %d %s, erwartet 403", st, antwort)
	}
	env.seedUser(t, "admin@x.de", true)
	st, antwort = env.restoreMitFeldAls(t, "admin@x.de", pfad, map[string]string{"mode": "as-new", "findings_pruefung": "aus"})
	if st != 200 || !strings.Contains(antwort, "finding_warnungen") {
		t.Fatalf("Admin mit Schalter: %d %s, erwartet 200 mit finding_warnungen", st, antwort)
	}
}
