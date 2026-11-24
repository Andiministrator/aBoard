package api_test

// Karte #863 (B-9): Eine ungültige boardId wird beim Upload mit aktiver Spiegel-Sperre
// ausdrücklich mit 400 abgelehnt, ohne dass die Sperrprüfung mit ihr aufgerufen wird
// und ohne dass etwas auf die Platte kommt. (Wächter: schon vor der Berichtigung wies
// uploadNach die ID später ab — der Test hält fest, dass das so bleibt.)

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

type uploadRechtAufruf863 struct{ aufrufe int }

func (u *uploadRechtAufruf863) UploadGesperrt(_ context.Context, _, _ string) (bool, error) {
	u.aufrufe++
	return false, nil
}

func TestUpload863_UngueltigeBoardIdFailClosed(t *testing.T) {
	dir := t.TempDir()
	fake := &uploadRechtAufruf863{}
	mux := http.NewServeMux()
	api.NewAttachmentsAPI(storage.NewAttachmentStore(dir), 1<<20).WithSpiegelUploadRecht(fake).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("attachmentId", "att1")
	fw, _ := mw.CreateFormFile("file", "beleg.txt")
	_, _ = fw.Write([]byte("inhalt"))
	_ = mw.Close()
	req, err := http.NewRequestWithContext(t.Context(), "POST", srv.URL+"/boards/a%20b/attachments/karte", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("ungültige boardId: %d, erwartet 400", resp.StatusCode)
	}
	if fake.aufrufe != 0 {
		t.Errorf("Sperrprüfung wurde mit ungültiger boardId %d-mal aufgerufen", fake.aufrufe)
	}
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Errorf("auf der Platte liegt etwas: %d Einträge", len(es))
	}
}
