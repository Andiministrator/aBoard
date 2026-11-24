package api_test

// boardbackup_test.go — REST-Smoke-Tests für die pro-Board-Backup-Endpunkte
// (#303, ADR-0098).

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/boardbackup"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/storage"
)

type bbTestEnv struct {
	srv    *httptest.Server
	db     *sql.DB
	boards *storage.BoardRepo
	acl    *storage.AclRepo
	users  *storage.UserRepo
	cfg    *storage.ConfigRepo
	api    *api.BoardBackupAPI
	attDir string
	bkpDir string
}

func newBBTestEnv(t *testing.T) *bbTestEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	tmp := t.TempDir()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(tmp, "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	boards := storage.NewBoardRepo(db)
	acl := storage.NewAclRepo(db)
	users := storage.NewUserRepo(db)
	// Audit-IDs müssen eindeutig sein (UNIQUE auf server_config_audit.id).
	// Vorher stand hier eine Konstante — das trug genau so lange, wie ein
	// Test nur EINEN Config-Wert setzte; der zweite Set lief in einen
	// Constraint-Fehler, der wie ein Produktivfehler aussieht.
	auditN := 0
	cfg := storage.NewConfigRepo(db, nil, func() string {
		auditN++
		return fmt.Sprintf("audit-id-%d", auditN)
	})

	attDir := filepath.Join(tmp, "attachments")
	bkpDir := filepath.Join(tmp, "backups")
	_ = os.MkdirAll(attDir, 0o755)
	_ = os.MkdirAll(bkpDir, 0o755)

	// backup_board.dir auf Test-Verzeichnis setzen
	_ = cfg.Set(ctx, "backup_board.dir", bkpDir, false, "")

	bbAPI := api.NewBoardBackupAPI(boards, acl, users, nil, cfg, attDir, "test")

	mux := http.NewServeMux()
	bbAPI.Register(mux)

	var h http.Handler = mux
	h = api.WithForwardAuthIfEnabled(h)

	return &bbTestEnv{
		srv:    httptest.NewServer(h),
		db:     db,
		boards: boards,
		acl:    acl,
		users:  users,
		cfg:    cfg,
		api:    bbAPI,
		attDir: attDir,
		bkpDir: bkpDir,
	}
}

// setSecretKey hinterlegt den Master-Key an der laufenden API (#336).
func (e *bbTestEnv) setSecretKey(t *testing.T, key string) {
	t.Helper()
	e.api.SetSecretKey(key)
}

func (e *bbTestEnv) close() {
	e.srv.Close()
	_ = e.db.Close()
}

// seedBoard legt ein Board + Owner-ACL für gegebene Email an.
func (e *bbTestEnv) seedBoard(t *testing.T, id, title, ownerEmail string) {
	t.Helper()
	state := buildTestBoardStateAPI(id, title)
	if _, err := e.boards.Insert(context.Background(), id, title, "2", state); err != nil {
		t.Fatalf("Insert board: %v", err)
	}
	if ownerEmail != "" {
		if err := e.acl.Set(context.Background(), id, ownerEmail, storage.RoleOwner); err != nil {
			t.Fatalf("Set ACL: %v", err)
		}
	}
}

func buildTestBoardStateAPI(id, title string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"version": "2",
		"board":   map[string]any{"id": id, "title": title, "sprints": []any{}},
	})
	return raw
}

// seedUser legt einen User-Row (optional Admin) an.
func (e *bbTestEnv) seedUser(t *testing.T, email string, admin bool) {
	t.Helper()
	flag := 0
	if admin {
		flag = 1
	}
	if _, err := e.db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, ?)`,
		"u-"+email, storage.NormalizeEmail(email), "N", "2026-01-01", flag); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

// do schickt einen Request mit optionalen Remote-Headern.
func (e *bbTestEnv) do(t *testing.T, method, path, email string, body io.Reader, ct string) *http.Response {
	t.Helper()
	r, err := http.NewRequest(method, e.srv.URL+path, body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if email != "" {
		r.Header.Set("Remote-Email", email)
		r.Header.Set("Remote-User", email)
	}
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// REST: GET files — owner OK, non-owner 403, unknown-board 404.
func TestBackupAPI_ListFiles_ACL(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")

	// 200 für owner
	resp := env.do(t, "GET", "/boards/b1/backup/files", "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("owner sollte 200 bekommen, got %d", resp.StatusCode)
	}

	// 403 für anderen User
	resp = env.do(t, "GET", "/boards/b1/backup/files", "intruder@x.de", nil, "")
	if resp.StatusCode != 403 {
		t.Errorf("non-owner sollte 403 bekommen, got %d", resp.StatusCode)
	}

	// 404 für unbekanntes Board
	resp = env.do(t, "GET", "/boards/unknown/backup/files", "owner@x.de", nil, "")
	if resp.StatusCode != 404 {
		t.Errorf("unknown board sollte 404 bekommen, got %d", resp.StatusCode)
	}
}

// REST: POST /now — owner OK, danach 1 File in Liste.
func TestBackupAPI_RunNow_Roundtrip(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")

	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("backup/now: status=%d body=%s", resp.StatusCode, body)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fn, _ := body["filename"].(string)
	if !strings.HasPrefix(fn, "boardbackup-b1-") {
		t.Errorf("filename unexpected: %q", fn)
	}

	// List zeigt das File
	resp = env.do(t, "GET", "/boards/b1/backup/files", "owner@x.de", nil, "")
	var list struct {
		Files []boardbackup.BackupInfo `json:"files"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	if len(list.Files) != 1 {
		t.Errorf("erwartet 1 File, got %d", len(list.Files))
	}
}

// REST: Rate-Limit — 11. Aufruf in 1h → 429.
func TestBackupAPI_RunNow_RateLimit(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")

	for i := 0; i < 10; i++ {
		resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("Aufruf %d sollte 200 sein, got %d (%s)", i, resp.StatusCode, body)
		}
	}
	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != 429 {
		t.Errorf("11. Aufruf sollte 429 sein, got %d", resp.StatusCode)
	}
}

// REST: Admin (nicht Owner) darf overwrite-restore aufrufen.
func TestBackupAPI_AdminCanOverwriteRestore(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")
	env.seedUser(t, "admin@x.de", true)

	// 1. Backup als Owner erzeugen
	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("backup/now: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fn, _ := body["filename"].(string)
	backupPath := filepath.Join(env.bkpDir, "b1", fn)

	// 2. Restore als Admin — multipart upload
	resp = env.uploadRestore(t, "admin@x.de", backupPath, "overwrite", "b1")
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("Admin Restore sollte 200 sein, got %d (%s)", resp.StatusCode, body)
	}
}

// REST: Non-Owner-Non-Admin darf NICHT overwrite-restore (403).
func TestBackupAPI_NonOwnerNoRestore(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")

	// Backup
	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fn, _ := body["filename"].(string)
	backupPath := filepath.Join(env.bkpDir, "b1", fn)

	resp = env.uploadRestore(t, "stranger@x.de", backupPath, "overwrite", "b1")
	if resp.StatusCode != 403 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("non-owner Restore sollte 403 sein, got %d (%s)", resp.StatusCode, body)
	}
}

// REST: Download liefert tar.gz mit korrektem Content-Type.
func TestBackupAPI_DownloadOK(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")

	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fn, _ := body["filename"].(string)

	resp = env.do(t, "GET", "/boards/b1/backup/download/"+fn, "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Errorf("Download sollte 200 sein, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type drift: %q", ct)
	}
}

// REST: Download Path-Traversal blockiert.
func TestBackupAPI_Download_PathTraversal(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")

	resp := env.do(t, "GET", "/boards/b1/backup/download/..%2F..%2Fetc%2Fpasswd", "owner@x.de", nil, "")
	if resp.StatusCode != 400 && resp.StatusCode != 404 {
		// Go's net/http resolved path resolution kann das auch als anderes
		// Path produzieren — als Defense-in-depth genügt es, dass kein 200
		// herauskommt.
		t.Errorf("Path-Traversal sollte abgelehnt sein, got %d", resp.StatusCode)
	}
}

// REST: Delete file.
func TestBackupAPI_DeleteFile(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")

	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fn, _ := body["filename"].(string)

	resp = env.do(t, "DELETE", "/boards/b1/backup/files/"+fn, "owner@x.de", nil, "")
	if resp.StatusCode != 204 {
		t.Errorf("Delete sollte 204 sein, got %d", resp.StatusCode)
	}

	// File ist weg
	if _, err := os.Stat(filepath.Join(env.bkpDir, "b1", fn)); !os.IsNotExist(err) {
		t.Errorf("File sollte gelöscht sein: %v", err)
	}
}

// uploadRestore baut multipart-Form mit file+mode+target_board_id.
func (e *bbTestEnv) uploadRestore(t *testing.T, email, archivePath, mode, targetID string) *http.Response {
	t.Helper()
	raw, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	_ = mw.WriteField("mode", mode)
	if targetID != "" {
		_ = mw.WriteField("target_board_id", targetID)
	}
	fw, _ := mw.CreateFormFile("file", "backup.tar.gz")
	_, _ = fw.Write(raw)
	_ = mw.Close()

	return e.do(t, "POST", "/backup/restore", email, buf, mw.FormDataContentType())
}

// REST: Upload-Limit überschritten → 413.
func TestBackupAPI_UploadTooLarge(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")
	// Limit auf 1 MB
	_ = env.cfg.Set(context.Background(), "backup_board.max_upload_mb", "1", false, "")

	// 5 MB-Payload — sicher über dem 1MB-Limit, auch nach dem +1KB-Headroom.
	junk := bytes.Repeat([]byte{0xa}, 5*1024*1024)
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	_ = mw.WriteField("mode", "overwrite")
	_ = mw.WriteField("target_board_id", "b1")
	fw, _ := mw.CreateFormFile("file", "big.tar.gz")
	_, _ = fw.Write(junk)
	_ = mw.Close()

	resp := env.do(t, "POST", "/backup/restore", "owner@x.de", buf, mw.FormDataContentType())
	// Erwartet entweder 413 (Limit greift sauber via MaxBytesReader) ODER
	// eine andere 4xx-/5xx-Antwort, die kein „Restore erfolgreich" ist —
	// solange kein 200 herauskommt, ist die Defense-in-depth gewahrt.
	// In der Praxis greift bei Content-Length-tragenden Multipart-Requests
	// die 413; bei chunked transfer kann es ein 500 mit Stream-Cutoff sein.
	if resp.StatusCode == 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("Upload zu groß: Restore darf NICHT erfolgreich sein, got 200 (%s)", body)
	}
	if resp.StatusCode != 413 && resp.StatusCode != 400 && resp.StatusCode != 500 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("erwartet 413/400/500, got %d (%s)", resp.StatusCode, body)
	}
}

// Helper: silence unused import warning when fmt not used in other tests.
var _ = fmt.Sprintf

/* ── #336: Encryption-at-Rest über die REST-Schicht ────────────────── */

// Ein verschlüsseltes Backup entsteht nur, wenn der Master-Key gesetzt IST
// und die Config es verlangt. Der Endpunkt muss die `.enc`-Datei danach
// listen, ausliefern und löschen können — ein Dateiname-Filter, der die neue
// Endung nicht kennt, macht das Archiv unerreichbar, ohne einen Fehler zu
// melden.
func TestBackupAPI_Verschluesselt_ListeDownloadDelete(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")
	ctx := context.Background()
	if err := env.cfg.Set(ctx, "backup_board.encryption_enabled", "true", false, ""); err != nil {
		t.Fatalf("cfg: %v", err)
	}
	env.setSecretKey(t, "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")

	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("backup/now: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fn, _ := body["filename"].(string)
	if !strings.HasSuffix(fn, ".tar.gz.enc") {
		t.Fatalf("Dateiname %q trägt keine .enc-Endung", fn)
	}

	// Liste: die Datei muss auftauchen UND als verschlüsselt markiert sein.
	resp = env.do(t, "GET", "/boards/b1/backup/files", "owner@x.de", nil, "")
	var liste struct {
		Files []struct {
			Filename  string `json:"filename"`
			Encrypted bool   `json:"encrypted"`
		} `json:"files"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&liste)
	if len(liste.Files) != 1 {
		t.Fatalf("Liste hat %d Einträge, erwartet 1 — verschlüsselte Backups sind unsichtbar", len(liste.Files))
	}
	if !liste.Files[0].Encrypted {
		t.Error("Listeneintrag ist nicht als verschlüsselt markiert")
	}

	// Download: 200, nicht 400 („invalid filename").
	if resp := env.do(t, "GET", "/boards/b1/backup/download/"+fn, "owner@x.de", nil, ""); resp.StatusCode != 200 {
		t.Errorf("Download eines .enc-Archivs: %d, erwartet 200", resp.StatusCode)
	}
	// Delete: 204/200, nicht 400.
	if resp := env.do(t, "DELETE", "/boards/b1/backup/files/"+fn, "owner@x.de", nil, ""); resp.StatusCode >= 400 {
		t.Errorf("Löschen eines .enc-Archivs: %d", resp.StatusCode)
	}
}

// Verschlüsselung eingeschaltet, aber kein Schlüssel: der Endpunkt muss
// ABLEHNEN statt ersatzweise ein unverschlüsseltes Backup zu schreiben.
func TestBackupAPI_VerschluesselungOhneSchluessel_LehntAb(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")
	if err := env.cfg.Set(context.Background(), "backup_board.encryption_enabled", "true", false, ""); err != nil {
		t.Fatalf("cfg: %v", err)
	}
	// Kein SetSecretKey — der Schlüssel fehlt.

	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("erwartet 503, bekam %d", resp.StatusCode)
	}
	// Und es darf KEINE Datei entstanden sein — weder mit noch ohne .enc.
	eintraege, _ := os.ReadDir(filepath.Join(env.bkpDir, "b1"))
	if len(eintraege) != 0 {
		namen := []string{}
		for _, e := range eintraege {
			namen = append(namen, e.Name())
		}
		t.Fatalf("trotz Ablehnung liegen Dateien im Backup-Verzeichnis: %v", namen)
	}
}

// S1 (Sprint-58-Kritiker): Der as-new-Modus verlangte KEINERLEI ACL. Nach
// #336 machte ihn das zum Entschlüsselungs-Orakel: Wer irgendwie an ein
// fremdes `.tar.gz.enc` kam, lud es hoch, der Server entschlüsselte es mit
// dem Master-Key und legte ein neues Board an — mit dem Hochladenden als
// Eigentümer. Genau der Schutz, für den #336 gebaut wurde, wäre über einen
// REST-Aufruf zu umgehen gewesen.
func TestBackupAPI_AsNew_FremdesArchivAbgelehnt(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	// Board von "chef", Backup davon. "fremd" hat damit nichts zu tun.
	env.seedBoard(t, "geheim", "Geheimes Board", "chef@x.de")
	env.seedUser(t, "chef@x.de", false)
	env.seedUser(t, "fremd@x.de", false)

	resp := env.do(t, "POST", "/boards/geheim/backup/now", "chef@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("backup/now: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fn, _ := body["filename"].(string)
	archivPfad := filepath.Join(env.bkpDir, "geheim", fn)

	// 1. Der Fremde versucht, es als neues Board einzuspielen → 403.
	resp = env.uploadRestore(t, "fremd@x.de", archivPfad, "as-new", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("as-new durch Fremden: Status %d, erwartet 403", resp.StatusCode)
	}
	// Und es darf KEIN Board entstanden sein.
	boards, _ := env.boards.List(context.Background())
	for _, b := range boards {
		if strings.Contains(b.Title, "Geheimes Board") && b.ID != "geheim" {
			t.Fatalf("trotz 403 wurde ein Board angelegt: %s / %s", b.ID, b.Title)
		}
	}

	// 2. Der Eigentümer selbst darf es weiterhin — sonst hätte der Fix das
	//    Feature erschlagen statt es abzusichern.
	resp = env.uploadRestore(t, "chef@x.de", archivPfad, "as-new", "")
	if resp.StatusCode != 200 {
		t.Fatalf("as-new durch Eigentümer: Status %d, erwartet 200", resp.StatusCode)
	}
}

/* ── Karte #437 / F-709: Schlüsselwechsel über die REST-Schicht ────── */

// Die Betreiber-Situation, um die es in der Karte geht, in einem Testfall:
// Ein Backup entsteht unter Schlüssel A. Danach wird ABOARD_SECRET_KEY auf B
// rotiert. Vorher gab es KEINEN Zustand, in dem sich dieses Archiv noch
// einspielen liess — der Server hätte mit A starten müssen, und dann wären
// die aktuellen Config-Secrets und LLM-Keys unlesbar gewesen.
//
// Warum dieser Test zusätzlich zu denen in boardbackup/: Dort ist geprüft,
// dass der Crypter mit einem Vorgängerschlüssel umgehen kann. Hier ist
// geprüft, dass der REST-Handler ihn überhaupt heranzieht — die Verdrahtung
// SetPreviousSecretKey → NewCrypterMitVorgaengern. Ein Test der Funktion ist
// kein Test ihres Aufrufers.
func TestBackupAPI_RestoreNachSchluesselwechsel(t *testing.T) {
	const keyAlt = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	const keyNeu = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"

	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")
	env.seedBoard(t, "b-ziel", "Ziel", "owner@x.de")
	if err := env.cfg.Set(context.Background(), "backup_board.encryption_enabled", "true", false, ""); err != nil {
		t.Fatalf("cfg: %v", err)
	}

	// 1. Backup unter dem ALTEN Schlüssel.
	env.setSecretKey(t, keyAlt)
	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("backup/now: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	archiv := filepath.Join(env.bkpDir, "b1", body["filename"].(string))

	// 2. Rotation: nur noch der neue Schlüssel ist konfiguriert.
	env.setSecretKey(t, keyNeu)
	resp = env.uploadRestore(t, "owner@x.de", archiv, "overwrite", "b-ziel")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Restore ohne Vorgängerschlüssel: erwartet 400, bekam %d", resp.StatusCode)
	}
	meldung := leseFehlertext(t, resp)
	if !strings.Contains(meldung, "anderen Schlüssel") {
		t.Fatalf("die Antwort sagt nicht, dass es am Schlüssel liegt — genau die "+
			"Fehldiagnose aus F-709: %q", meldung)
	}

	// 3. Vorgängerschlüssel hinterlegt → dasselbe Archiv geht wieder auf.
	env.api.SetPreviousSecretKey(keyAlt)
	resp = env.uploadRestore(t, "owner@x.de", archiv, "overwrite", "b-ziel")
	if resp.StatusCode != 200 {
		t.Fatalf("Restore mit ABOARD_SECRET_KEY_PREVIOUS: erwartet 200, bekam %d (%s)",
			resp.StatusCode, leseFehlertext(t, resp))
	}
}

// Der Vorgängerschlüssel darf NUR lesen. Schriebe der Server damit, wäre die
// Rotation folgenlos — das alte Schlüsselmaterial bliebe unbegrenzt im
// Einsatz, ohne dass es jemandem auffiele.
func TestBackupAPI_VorgaengerSchluesselSchreibtNicht(t *testing.T) {
	const keyAlt = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	const keyNeu = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"

	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")
	if err := env.cfg.Set(context.Background(), "backup_board.encryption_enabled", "true", false, ""); err != nil {
		t.Fatalf("cfg: %v", err)
	}
	env.setSecretKey(t, keyNeu)
	env.api.SetPreviousSecretKey(keyAlt)

	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("backup/now: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	roh, err := os.ReadFile(filepath.Join(env.bkpDir, "b1", body["filename"].(string)))
	if err != nil {
		t.Fatalf("Archiv lesen: %v", err)
	}

	// Der Abdruck im Kopf muss der des AKTUELLEN Schlüssels sein.
	sub, err := secretcrypto.DeriveKey(keyNeu, "aboard-backup-fingerprint-v1")
	if err != nil {
		t.Fatal(err)
	}
	fpNeu, err := secretcrypto.Fingerprint(sub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roh[8:8+len(fpNeu)], fpNeu) {
		t.Fatalf("das frische Archiv trägt nicht den Abdruck des aktuellen Schlüssels "+
			"(Kopf %x, erwartet %x)", roh[8:8+len(fpNeu)], fpNeu)
	}
}

// leseFehlertext holt das `error`-Feld aus einer Fehlerantwort.
func leseFehlertext(t *testing.T, resp *http.Response) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	return e.Error
}

// Sprint-62-Kritiker K2: Ein unbrauchbarer ABOARD_SECRET_KEY_PREVIOUS darf den
// Restore nicht stilllegen.
//
// Die erste Fassung von Karte #437 reichte beide Schlüssel zusammen an
// NewCrypterMitVorgaengern und machte aus jedem Fehler ein
// "503 ABOARD_SECRET_KEY ungültig". Ein Betreiber, der den Platzhalter aus dem
// Compose-Beispiel nicht ersetzt hat, konnte damit GAR NICHTS mehr einspielen —
// auch kein unverschlüsseltes Archiv — und die Meldung schickte ihn zur
// falschen Variablen. Der Rettungsweg war schlechter als vorher.
func TestBackupAPI_UnbrauchbarerVorgaengerLegtRestoreNichtLahm(t *testing.T) {
	const keyGut = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")
	env.seedBoard(t, "b-ziel", "Ziel", "owner@x.de")
	if err := env.cfg.Set(context.Background(), "backup_board.encryption_enabled", "true", false, ""); err != nil {
		t.Fatalf("cfg: %v", err)
	}
	env.setSecretKey(t, keyGut)

	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("backup/now: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	archiv := filepath.Join(env.bkpDir, "b1", body["filename"].(string))

	// Der Platzhalter-Fall: gesetzt, aber Unsinn.
	env.api.SetPreviousSecretKey("<alter-schluessel-hier>")

	resp = env.uploadRestore(t, "owner@x.de", archiv, "overwrite", "b-ziel")
	if resp.StatusCode == http.StatusServiceUnavailable {
		t.Fatalf("ein kaputter Vorgängerschlüssel legt den ganzen Restore still (503: %q) — "+
			"auch für Archive, die mit dem AKTUELLEN Schlüssel lesbar sind",
			leseFehlertext(t, resp))
	}
	if resp.StatusCode != 200 {
		t.Fatalf("Restore mit gültigem aktuellem Schlüssel: erwartet 200, bekam %d (%s)",
			resp.StatusCode, leseFehlertext(t, resp))
	}
}

// Die Gegenrichtung: Ein kaputter AKTUELLER Schlüssel bleibt ein harter Fehler.
// Ohne ihn kann der Server gar nichts entschlüsseln — hier ist 503 richtig.
func TestBackupAPI_UnbrauchbarerAktuellerSchluesselBleibtHart(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	env.seedBoard(t, "b1", "Test", "owner@x.de")
	env.seedBoard(t, "b-ziel", "Ziel", "owner@x.de")

	// Erst ein gültiges (unverschlüsseltes) Archiv erzeugen …
	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	if resp.StatusCode != 200 {
		t.Fatalf("backup/now: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	archiv := filepath.Join(env.bkpDir, "b1", body["filename"].(string))

	// … dann den aktuellen Schlüssel kaputt machen.
	env.setSecretKey(t, "keinhex")

	resp = env.uploadRestore(t, "owner@x.de", archiv, "overwrite", "b-ziel")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("kaputter ABOARD_SECRET_KEY: erwartet 503, bekam %d", resp.StatusCode)
	}
}
