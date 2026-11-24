package api_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// attachTestEnv bündelt Server + AttachmentStore in einem TempDir.
type attachTestEnv struct {
	srv       *httptest.Server
	attachDir string
}

func (e *attachTestEnv) close() { e.srv.Close() }

func newAttachTestEnv(t *testing.T) *attachTestEnv {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAttachmentStore(dir)
	attachAPI := api.NewAttachmentsAPI(store, 1*1024*1024) // 1 MB Testlimit
	mux := http.NewServeMux()
	attachAPI.Register(mux)
	return &attachTestEnv{srv: httptest.NewServer(mux), attachDir: dir}
}

// buildUpload baut eine multipart-Request für den Upload-Endpoint.
func buildUpload(t *testing.T, env *attachTestEnv, boardId, cardId, attId, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("attachmentId", attId)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = fw.Write(content)
	_ = mw.Close()
	url := env.srv.URL + "/boards/" + boardId + "/attachments/" + cardId
	req, err := http.NewRequestWithContext(t.Context(), "POST", url, &buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// ── Upload ────────────────────────────────────────────────────────────────────

func TestAttachments_Upload_Erfolg(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	content := []byte("fake png data")
	req := buildUpload(t, env, "board-1", "card-1", "att-uuid-1", "foto.png", content)
	resp := mustDo(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status: want 201, got %d", resp.StatusCode)
	}
	body := string(mustReadAll(t, resp.Body))
	if !strings.Contains(body, `"att-uuid-1"`) {
		t.Errorf("response soll attachmentId enthalten, got: %s", body)
	}
}

// TestAttachments_Upload_BelegteId409 (F-2081): ein zweiter Upload unter einer
// vorhandenen attachmentId wird abgelehnt, der erste Inhalt bleibt.
func TestAttachments_Upload_BelegteId409(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	resp := mustDo(t, buildUpload(t, env, "board-1", "card-1", "att-uuid-1", "a.txt", []byte("ORIGINAL")))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("erster Upload: want 201, got %d", resp.StatusCode)
	}
	resp = mustDo(t, buildUpload(t, env, "board-1", "card-1", "att-uuid-1", "a.txt", []byte("ANDERS")))
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("zweiter Upload: want 409, got %d", resp.StatusCode)
	}
	req, _ := http.NewRequestWithContext(t.Context(), "GET", env.srv.URL+"/boards/board-1/attachments/card-1/att-uuid-1", nil)
	resp = mustDo(t, req)
	defer resp.Body.Close()
	if got := string(mustReadAll(t, resp.Body)); got != "ORIGINAL" {
		t.Errorf("Download nach abgelehntem Upload = %q, erwartet ORIGINAL", got)
	}
}

func TestAttachments_Upload_SVG_geblockt(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	svgContent := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	req := buildUpload(t, env, "board-1", "card-1", "att-svg", "bild.svg", svgContent)
	resp := mustDo(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("svg-upload soll 415 liefern, got %d", resp.StatusCode)
	}
}

func TestAttachments_Upload_Pfad_Traversal_boardId(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	/* Path-Traversal im boardId wird via validAttachmentID geblockt — der ServeMux
	   matched die Route nicht (Go 1.22 pattern-routing: {boardId} stoppt bei /),
	   daher 404 vom Mux oder 400 vom Handler. */
	url := env.srv.URL + "/boards/%2E%2E%2Fetc/attachments/card-1"
	req, _ := http.NewRequestWithContext(t.Context(), "POST", url, nil)
	resp := mustDo(t, req)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Errorf("traversal-boardId soll NICHT 201 liefern, got %d", resp.StatusCode)
	}
}

func TestAttachments_Upload_ZuGross(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	// 2 MB > 1 MB Testlimit
	content := make([]byte, 2*1024*1024)
	req := buildUpload(t, env, "board-1", "card-1", "att-big", "grosse.bin", content)
	resp := mustDo(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("zu-grosse-datei soll 413 liefern, got %d", resp.StatusCode)
	}
}

// ── Download ──────────────────────────────────────────────────────────────────

func TestAttachments_Download_Erfolg(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	// Erst hochladen
	content := []byte("JPEG-Fake\xFF\xD8\xFF")
	req := buildUpload(t, env, "board-1", "card-2", "att-dl", "bild.jpg", content)
	resp := mustDo(t, req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload vorbedingung fehlgeschlagen: %d", resp.StatusCode)
	}

	// Download
	dlReq, _ := http.NewRequestWithContext(t.Context(), "GET",
		env.srv.URL+"/boards/board-1/attachments/card-2/att-dl", nil)
	dlResp := mustDo(t, dlReq)
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		t.Fatalf("download status: want 200, got %d", dlResp.StatusCode)
	}
	got := mustReadAll(t, dlResp.Body)
	if !bytes.Equal(got, content) {
		t.Errorf("downloaded content mismatch")
	}
	if !strings.Contains(dlResp.Header.Get("Content-Disposition"), "bild.jpg") {
		t.Errorf("Content-Disposition soll Dateinamen enthalten")
	}
}

func TestAttachments_Download_NichtGefunden(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	req, _ := http.NewRequestWithContext(t.Context(), "GET",
		env.srv.URL+"/boards/board-x/attachments/card-x/att-x", nil)
	resp := mustDo(t, req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("fehlendes attachment soll 404 liefern, got %d", resp.StatusCode)
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestAttachments_DeleteOne_Erfolg(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	// Upload
	req := buildUpload(t, env, "board-1", "card-3", "att-del", "x.txt", []byte("data"))
	resp := mustDo(t, req)
	resp.Body.Close()

	// Delete
	delReq, _ := http.NewRequestWithContext(t.Context(), "DELETE",
		env.srv.URL+"/boards/board-1/attachments/card-3/att-del", nil)
	delResp := mustDo(t, delReq)
	defer delResp.Body.Close()

	if delResp.StatusCode != http.StatusNoContent {
		t.Errorf("delete soll 204 liefern, got %d", delResp.StatusCode)
	}

	// Verify: Download soll 404 liefern
	dlReq, _ := http.NewRequestWithContext(t.Context(), "GET",
		env.srv.URL+"/boards/board-1/attachments/card-3/att-del", nil)
	dlResp := mustDo(t, dlReq)
	dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusNotFound {
		t.Errorf("nach delete soll 404 folgen, got %d", dlResp.StatusCode)
	}
}

func TestAttachments_DeleteCard_EntferntAlle(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	// Zwei Uploads
	for _, id := range []string{"a1", "a2"} {
		req := buildUpload(t, env, "board-2", "card-purge", id, id+".txt", []byte("x"))
		resp := mustDo(t, req)
		resp.Body.Close()
	}

	// Purge
	pReq, _ := http.NewRequestWithContext(t.Context(), "DELETE",
		env.srv.URL+"/boards/board-2/attachments/card-purge", nil)
	pResp := mustDo(t, pReq)
	pResp.Body.Close()
	if pResp.StatusCode != http.StatusNoContent {
		t.Errorf("purge soll 204 liefern, got %d", pResp.StatusCode)
	}

	// Verify: card-Verzeichnis soll nicht mehr existieren
	dir := filepath.Join(env.attachDir, "board-2", "card-purge")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("card-Verzeichnis soll nach purge nicht mehr existieren")
	}
}

// ── Security-Header ───────────────────────────────────────────────────────────

func TestAttachments_SecurityHeaders(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	// Upload-Route hat WithSecurityHeaders (kein WithDefaults)
	req := buildUpload(t, env, "board-h", "card-h", "att-h", "x.txt", []byte("data"))
	resp := mustDo(t, req)
	resp.Body.Close()

	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options fehlt auf Upload-Route")
	}
}
