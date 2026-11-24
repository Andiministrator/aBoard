package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// boardsTestEnv bündelt Server + DB für einen Test. defer env.close().
type boardsTestEnv struct {
	srv *httptest.Server
	db  *sql.DB
	// attachDir ist das Wurzelverzeichnis des AttachmentStore dieser Env
	// (F-200, Karte #376) — leer, wenn ohne Store aufgebaut.
	attachDir string
}

func (e *boardsTestEnv) close() {
	e.srv.Close()
	_ = e.db.Close()
}

func newBoardsTestEnv(t *testing.T) *boardsTestEnv {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	mux := http.NewServeMux()
	apiSrv := api.NewBoardsAPI(storage.NewBoardRepo(db))
	apiSrv.Register(mux)
	return &boardsTestEnv{srv: httptest.NewServer(mux), db: db}
}

// newBoardsTestEnvWithAttachments baut die Env zusätzlich mit einem
// AttachmentStore auf einem temporären Verzeichnis (F-200, Karte #376).
// Bewusst als eigener Konstruktor: der bestehende ohne Store bleibt gültig
// und belegt weiter, dass die API ohne Attachment-Volume funktioniert.
func newBoardsTestEnvWithAttachments(t *testing.T) *boardsTestEnv {
	t.Helper()
	ctx := context.Background()
	tmp := t.TempDir()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(tmp, "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	attachDir := filepath.Join(tmp, "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		t.Fatalf("mkdir attachments: %v", err)
	}
	mux := http.NewServeMux()
	api.NewBoardsAPI(storage.NewBoardRepo(db)).
		WithAttachments(storage.NewAttachmentStore(attachDir)).
		Register(mux)
	return &boardsTestEnv{srv: httptest.NewServer(mux), db: db, attachDir: attachDir}
}

// sampleBody erzeugt einen minimal-validen Envelope mit der gegebenen Board-ID
// und einem Test-Marker, der im state_json round-tripped wird.
func sampleBody(id, title string) []byte {
	body := map[string]any{
		"version": "2",
		"board": map[string]any{
			"id":           id,
			"title":        title,
			"columnDefs":   []any{},
			"sprints":      []any{},
			"marker":       "round-trip",
			"awaitingUser": nil,
		},
	}
	raw, _ := json.Marshal(body)
	return raw
}

func mustDo(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func mustReadAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// ── Security-Header (Middleware-Smoke-Test) ───────────────────────────

func TestBoards_SecurityHeaders(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	resp, err := http.Get(env.srv.URL + "/boards")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, erwartet nosniff", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, erwartet no-store", got)
	}
}

// ── GET /boards ───────────────────────────────────────────────────────

func TestBoardsList_Empty(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	resp, err := http.Get(env.srv.URL + "/boards")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", resp.StatusCode)
	}
	var got []any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, erwartet 0", len(got))
	}
}

func TestBoardsList_SortedByUpdatedAtDesc(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	for _, b := range []struct{ id, title string }{
		{"b1", "Erstes"},
		{"b2", "Zweites"},
		{"b3", "Drittes"},
	} {
		resp := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody(b.id, b.title), nil))
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("Create %s: Status %d", b.id, resp.StatusCode)
		}
	}
	resp, err := http.Get(env.srv.URL + "/boards")
	if err != nil {
		t.Fatalf("GET /boards: %v", err)
	}
	defer resp.Body.Close()
	var list []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		UpdatedAt int64  `json:"updated_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, erwartet 3", len(list))
	}
	for i := 0; i+1 < len(list); i++ {
		if list[i].UpdatedAt < list[i+1].UpdatedAt {
			t.Errorf("Sortierung verletzt: [%d]=%d < [%d]=%d",
				i, list[i].UpdatedAt, i+1, list[i+1].UpdatedAt)
		}
	}
}

// ── GET /boards/{id} ──────────────────────────────────────────────────

func TestBoardsGet_NotFound(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	resp, err := http.Get(env.srv.URL + "/boards/does-not-exist")
	if err != nil {
		t.Fatalf("GET /boards/does-not-exist: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Status = %d, erwartet 404", resp.StatusCode)
	}
}

func TestBoardsGet_HappyPath_RoundTripStateJSON(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	create := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Erstes"), nil))
	create.Body.Close()
	resp, err := http.Get(env.srv.URL + "/boards/b1")
	if err != nil {
		t.Fatalf("GET /boards/b1: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", resp.StatusCode)
	}
	if etag := resp.Header.Get("ETag"); etag == "" || !strings.HasPrefix(etag, `"`) {
		t.Errorf("ETag fehlt oder ungeklammert: %q", etag)
	}
	body := mustReadAll(t, resp.Body)
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	boardPart, _ := decoded["board"].(map[string]any)
	if boardPart["marker"] != "round-trip" {
		t.Errorf("state_json round-trip verletzt: marker=%v", boardPart["marker"])
	}
}

// F-254/ADR-0055: Conditional-GET. If-None-Match == aktueller ETag → 304
// ohne Body (ETag-Header bleibt); stale/fehlend → 200 + Body.
func TestBoardsGet_ConditionalGET_304(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("c1", "Cond"), nil)).Body.Close()

	first := mustDo(t, mustReq(t, "GET", env.srv.URL+"/boards/c1", nil, nil))
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("erster GET Status = %d, erwartet 200", first.StatusCode)
	}
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatal("ETag fehlt im ersten GET")
	}

	// 1) If-None-Match == aktueller ETag → 304, kein Body, ETag bleibt
	notMod := mustDo(t, mustReq(t, "GET", env.srv.URL+"/boards/c1", nil,
		http.Header{"If-None-Match": []string{etag}}))
	defer notMod.Body.Close()
	if notMod.StatusCode != http.StatusNotModified {
		t.Fatalf("Status = %d, erwartet 304", notMod.StatusCode)
	}
	if body := mustReadAll(t, notMod.Body); len(body) != 0 {
		t.Errorf("304 darf keinen Body haben, hat %d Bytes", len(body))
	}
	if got := notMod.Header.Get("ETag"); got != etag {
		t.Errorf("ETag bei 304 = %q, erwartet %q (RFC 7232)", got, etag)
	}

	// 2) Stale If-None-Match → 200 + Body
	stale := mustDo(t, mustReq(t, "GET", env.srv.URL+"/boards/c1", nil,
		http.Header{"If-None-Match": []string{`"0"`}}))
	defer stale.Body.Close()
	if stale.StatusCode != http.StatusOK {
		t.Fatalf("stale Status = %d, erwartet 200", stale.StatusCode)
	}
	if len(mustReadAll(t, stale.Body)) == 0 {
		t.Error("200 bei stale If-None-Match muss Body liefern")
	}
}

// ── POST /boards ──────────────────────────────────────────────────────

func TestBoardsCreate_HappyPath(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	resp := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Hallo"), nil))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("Status = %d, erwartet 201", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/boards/b1" {
		t.Errorf("Location = %q, erwartet /boards/b1", loc)
	}
	if resp.Header.Get("ETag") == "" {
		t.Errorf("ETag fehlt")
	}
}

func TestBoardsCreate_MissingFields(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	cases := []struct {
		name string
		body []byte
	}{
		{"missing version", []byte(`{"board":{"id":"b","title":"t"}}`)},
		{"missing board.id", []byte(`{"version":"2","board":{"title":"t"}}`)},
		{"missing board.title", []byte(`{"version":"2","board":{"id":"b"}}`)},
		{"empty body", []byte(``)},
		{"invalid json", []byte(`{garbage`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", tc.body, nil))
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("Status = %d, erwartet 400", resp.StatusCode)
			}
		})
	}
}

func TestBoardsCreate_DuplicateID(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	first := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Erst"), nil))
	first.Body.Close()
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first Status = %d", first.StatusCode)
	}
	second := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Zweit"), nil))
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("Status = %d, erwartet 409", second.StatusCode)
	}
}

func TestBoardsCreate_BodyTooLarge(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	// 12 MB Müll > MaxBodyBytes (10 MB)
	padding := strings.Repeat("x", 12*1024*1024)
	body := []byte(`{"version":"2","board":{"id":"b","title":"` + padding + `"}}`)
	resp := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", body, nil))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("Status = %d, erwartet 413", resp.StatusCode)
	}
}

// ── PUT /boards/{id} ──────────────────────────────────────────────────

func TestBoardsUpdate_NoIfMatch_Required(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Vor"), nil)).Body.Close()
	resp := mustDo(t, mustReq(t, "PUT", env.srv.URL+"/boards/b1", sampleBody("b1", "Nach"), nil))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("Status = %d, erwartet 428", resp.StatusCode)
	}
}

func TestBoardsUpdate_StaleIfMatch_Conflict(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Vor"), nil)).Body.Close()
	// Falscher ETag — vortäuschen, dass ein paralleles Device geschrieben hat
	staleETag := `"1"`
	resp := mustDo(t, mustReq(t, "PUT", env.srv.URL+"/boards/b1", sampleBody("b1", "Nach"),
		http.Header{"If-Match": []string{staleETag}}))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("Status = %d, erwartet 409", resp.StatusCode)
	}
	if resp.Header.Get("ETag") == "" {
		t.Errorf("Server-ETag im 409-Response fehlt")
	}
	// Body sollte den aktuellen Server-State enthalten (round-tripped 'Vor')
	body := mustReadAll(t, resp.Body)
	if !bytes.Contains(body, []byte(`"title":"Vor"`)) {
		t.Errorf("409-Body enthält Server-State nicht: %s", body)
	}
}

func TestBoardsUpdate_FreshIfMatch_Updates(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	createResp := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Vor"), nil))
	defer createResp.Body.Close()
	etag := createResp.Header.Get("ETag")
	if etag == "" {
		t.Fatalf("Create ohne ETag")
	}
	resp := mustDo(t, mustReq(t, "PUT", env.srv.URL+"/boards/b1", sampleBody("b1", "Nach"),
		http.Header{"If-Match": []string{etag}}))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", resp.StatusCode)
	}
	newETag := resp.Header.Get("ETag")
	if newETag == "" || newETag == etag {
		t.Errorf("ETag nicht erneuert: alt=%q neu=%q", etag, newETag)
	}
}

func TestBoardsUpdate_URLBodyMismatch(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Vor"), nil)).Body.Close()
	resp := mustDo(t, mustReq(t, "PUT", env.srv.URL+"/boards/b1",
		sampleBody("OTHER-ID", "Nach"),
		http.Header{"If-Match": []string{`"123"`}}))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Status = %d, erwartet 400", resp.StatusCode)
	}
}

func TestBoardsUpdate_NotFound(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	resp := mustDo(t, mustReq(t, "PUT", env.srv.URL+"/boards/ghost",
		sampleBody("ghost", "x"),
		http.Header{"If-Match": []string{`"123"`}}))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Status = %d, erwartet 404", resp.StatusCode)
	}
}

func TestBoardsUpdate_MalformedIfMatch(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Vor"), nil)).Body.Close()
	cases := []string{`*`, `W/"123"`, `not-a-number`, `"123","456"`}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			resp := mustDo(t, mustReq(t, "PUT", env.srv.URL+"/boards/b1",
				sampleBody("b1", "Nach"),
				http.Header{"If-Match": []string{raw}}))
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("Status = %d für %q, erwartet 400", resp.StatusCode, raw)
			}
		})
	}
}

// ── DELETE /boards/{id} ───────────────────────────────────────────────

func TestBoardsDelete_HappyPath(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Bald weg"), nil)).Body.Close()
	resp := mustDo(t, mustReq(t, "DELETE", env.srv.URL+"/boards/b1", nil, nil))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Status = %d, erwartet 204", resp.StatusCode)
	}
	get, _ := http.Get(env.srv.URL + "/boards/b1")
	get.Body.Close()
	if get.StatusCode != http.StatusNotFound {
		t.Errorf("Nach Delete: GET Status = %d, erwartet 404", get.StatusCode)
	}
}

func TestBoardsDelete_NotFound(t *testing.T) {
	env := newBoardsTestEnv(t)
	defer env.close()
	resp := mustDo(t, mustReq(t, "DELETE", env.srv.URL+"/boards/ghost", nil, nil))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Status = %d, erwartet 404", resp.StatusCode)
	}
}

// F-509 (Karte #416, Sprint 58): `TestBoardsDelete_CascadeAttachments` stand
// hier und belegte die FK-Cascade der `attachments`-TABELLE aus 0001_init.sql
// (F-190-Lehre: foreign_keys=1 muss in DSN stehen). Ein `grep` über
// server/ nach `INSERT INTO attachments` traf ausserhalb von Tests auf
// NICHTS — `AttachmentsAPI.upload` (server/api/attachments.go) ruft nur
// `AttachmentStore.Save`, das ausschliesslich auf das Dateisystem schreibt
// (server/storage/attachments.go, kein DB-Handle im Store). Die Tabelle
// bleibt im Betrieb leer; der gestrichene Test bewies eine Schema-Eigenschaft
// (FK-Cascade greift), aber nichts über einen real erreichbaren Zustand —
// die Klasse „Test ist grün, weil der Code nie läuft". Gestrichen statt
// künstlich am Leben gehalten: eine offen benannte Lücke ist ehrlicher als
// eine behauptete Abdeckung. Für einen Anhang-DB-Schreibpfad bräuchte es
// erst eine Produktentscheidung (ADR-0044 hält den Blob-Zugriff bewusst
// opak) — bis dahin ist der Dateisystem-Pfad die alleinige Wahrheit, siehe
// den Test darunter.
//
// ── F-200 (Karte #376): Anhang-VERZEICHNIS beim Board-Delete ──────────
//
// Die Anhangsdateien liegen ausschliesslich im Dateisystem; für sie gibt es
// keine Cascade, und genau deshalb blieben sie beim Board-Delete liegen.

func TestBoardsDelete_EntferntAnhangVerzeichnis(t *testing.T) {
	env := newBoardsTestEnvWithAttachments(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Mit Dateien"), nil)).Body.Close()

	// Zwei Karten-Verzeichnisse mit je einer Datei anlegen (so wie der
	// AttachmentStore sie schreibt: <boardId>/<cardId>/<attId>__<name>).
	boardDir := filepath.Join(env.attachDir, "b1")
	for _, card := range []string{"card-1", "card-2"} {
		dir := filepath.Join(boardDir, card)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "att-1__datei.png"), []byte("PNG"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// Ein FREMDES Board im selben Volume — das muss unberührt bleiben.
	fremd := filepath.Join(env.attachDir, "b2", "card-9")
	if err := os.MkdirAll(fremd, 0o755); err != nil {
		t.Fatalf("mkdir fremd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fremd, "att-9__fremd.png"), []byte("PNG"), 0o644); err != nil {
		t.Fatalf("write fremd: %v", err)
	}

	resp := mustDo(t, mustReq(t, "DELETE", env.srv.URL+"/boards/b1", nil, nil))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Delete Status = %d, erwartet 204", resp.StatusCode)
	}

	if _, err := os.Stat(boardDir); !os.IsNotExist(err) {
		t.Errorf("Anhang-Verzeichnis %s existiert noch (err=%v) — F-200 nicht behoben", boardDir, err)
	}
	if _, err := os.Stat(fremd); err != nil {
		t.Errorf("fremdes Board-Verzeichnis wurde mitgenommen: %v", err)
	}
}

func TestBoardsDelete_OhneAttachmentStore_Weiterhin204(t *testing.T) {
	// Test-Setups ohne Attachment-Volume (und der Legacy-Betrieb) dürfen
	// durch WithAttachments nicht brechen: a.attach == nil ist erlaubt.
	env := newBoardsTestEnv(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Ohne Volume"), nil)).Body.Close()
	resp := mustDo(t, mustReq(t, "DELETE", env.srv.URL+"/boards/b1", nil, nil))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Status = %d, erwartet 204", resp.StatusCode)
	}
}

func TestBoardsDelete_OhneAnhaenge_KeinFehler(t *testing.T) {
	// Der Normalfall: Board ohne jede Anhangsdatei. os.RemoveAll auf ein
	// nicht existentes Verzeichnis ist kein Fehler — der Delete bleibt 204.
	env := newBoardsTestEnvWithAttachments(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Leer"), nil)).Body.Close()
	resp := mustDo(t, mustReq(t, "DELETE", env.srv.URL+"/boards/b1", nil, nil))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Status = %d, erwartet 204", resp.StatusCode)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────

func mustReq(t *testing.T, method, url string, body []byte, header http.Header) *http.Request {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if header != nil {
		for k, vs := range header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// TestBoardsDelete_RuftNachDelete — K-A1 (Kritiker Sprint 70): Nur ein per
// API gelöschtes Board bekommt einen Grabstein für seine Backups. Der
// Schritt läuft genau nach einem erfolgreichen Delete, nicht bei 404.
func TestBoardsDelete_RuftNachDelete(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	var gerufen []string
	mux := http.NewServeMux()
	api.NewBoardsAPI(storage.NewBoardRepo(db)).
		WithNachDelete(func(_ context.Context, id string) { gerufen = append(gerufen, id) }).
		Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mustDo(t, mustReq(t, "POST", srv.URL+"/boards", sampleBody("b1", "Weg"), nil)).Body.Close()
	resp := mustDo(t, mustReq(t, "DELETE", srv.URL+"/boards/b1", nil, nil))
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Delete Status = %d, erwartet 204", resp.StatusCode)
	}
	resp = mustDo(t, mustReq(t, "DELETE", srv.URL+"/boards/gibtsnicht", nil, nil))
	resp.Body.Close()
	if len(gerufen) != 1 || gerufen[0] != "b1" {
		t.Fatalf("NachDelete gerufen mit %v, erwartet genau [b1]", gerufen)
	}
}
