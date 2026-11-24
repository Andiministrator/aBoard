package api_test

// Tests für die board-weiten Attachment-Routen (Karte #443, F-739).
//
// Board-Anhänge — Hintergrundbild und Avatare — gehören zu keiner Karte.
// Bis Sprint 61 reisten sie deshalb als base64 im Board-State und liefen gegen
// MaxBodyBytes, während der Server für Karten-Anhänge längst einen eigenen
// Endpunkt mit eigenem Limit hatte. Diese Routen schliessen die Lücke.

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// buildBoardUpload baut eine multipart-Request für den board-weiten Upload.
func buildBoardUpload(t *testing.T, env *attachTestEnv, boardId, attId, filename string, content []byte) *http.Request {
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
	req, err := http.NewRequestWithContext(t.Context(), "POST",
		env.srv.URL+"/boards/"+boardId+"/board-attachments", &buf)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestBoardAttachments_UploadDownloadDelete(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	inhalt := []byte("fake background image data")
	resp := mustDo(t, buildBoardUpload(t, env, "board-1", "att-bg-1", "hintergrund.png", inhalt))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status: want 201, got %d", resp.StatusCode)
	}
	if body := string(mustReadAll(t, resp.Body)); !strings.Contains(body, `"id":"att-bg-1"`) {
		t.Fatalf("Antwort nennt die id nicht: %s", body)
	}

	// Download liefert genau das, was hochgeladen wurde.
	dl := mustDo(t, mustReq(t, "GET", env.srv.URL+"/boards/board-1/board-attachments/att-bg-1", nil, nil))
	defer dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("download status: want 200, got %d", dl.StatusCode)
	}
	if got := mustReadAll(t, dl.Body); !bytes.Equal(got, inhalt) {
		t.Fatalf("Download-Inhalt weicht ab: %q", got)
	}

	// Löschen, danach 404.
	del := mustDo(t, mustReq(t, "DELETE", env.srv.URL+"/boards/board-1/board-attachments/att-bg-1", nil, nil))
	defer del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status: want 204, got %d", del.StatusCode)
	}
	weg := mustDo(t, mustReq(t, "GET", env.srv.URL+"/boards/board-1/board-attachments/att-bg-1", nil, nil))
	defer weg.Body.Close()
	if weg.StatusCode != http.StatusNotFound {
		t.Fatalf("nach dem Löschen: want 404, got %d", weg.StatusCode)
	}
}

// Board-Anhänge liegen im selben Store, aber unter einem reservierten
// Bezeichner — nicht im Verzeichnis irgendeiner Karte.
func TestBoardAttachments_AblageGetrenntVonKarten(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	resp := mustDo(t, buildBoardUpload(t, env, "board-1", "att-bg-2", "bg.png", []byte("x")))
	defer resp.Body.Close()

	eintraege, err := os.ReadDir(filepath.Join(env.attachDir, "board-1"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var namen []string
	for _, e := range eintraege {
		namen = append(namen, e.Name())
	}
	if len(namen) != 1 || namen[0] != "__board__" {
		t.Fatalf("Ablage-Verzeichnisse = %v, erwartet genau [__board__]", namen)
	}
}

// Die Reservierung ist echt: Die KARTEN-Route lehnt den Bezeichner ab. Ohne
// das führten zwei Routen auf dieselbe Ablage, und die Trennung wäre bloss
// eine Absichtserklärung.
func TestBoardAttachments_ReservierterBezeichnerAufKartenRouteAbgelehnt(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	up := mustDo(t, buildUpload(t, env, "board-1", "__board__", "att-x", "x.png", []byte("x")))
	defer up.Body.Close()
	if up.StatusCode != http.StatusBadRequest {
		t.Fatalf("Upload auf /attachments/__board__: want 400, got %d", up.StatusCode)
	}

	// Auch lesend und löschend — sonst käme man an fremd abgelegte Anhänge
	// über die Nachbarroute heran.
	for _, fall := range []struct {
		name string
		req  *http.Request
	}{
		{"download", mustReq(t, "GET", env.srv.URL+"/boards/board-1/attachments/__board__/att-x", nil, nil)},
		{"deleteOne", mustReq(t, "DELETE", env.srv.URL+"/boards/board-1/attachments/__board__/att-x", nil, nil)},
		{"deleteCard", mustReq(t, "DELETE", env.srv.URL+"/boards/board-1/attachments/__board__", nil, nil)},
	} {
		resp := mustDo(t, fall.req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s auf __board__: want 400, got %d", fall.name, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// Dieselben Schranken wie beim Karten-Upload — sie stecken in der gemeinsamen
// Kernfunktion, damit sie nicht an einer der beiden Stellen fehlen können.
func TestBoardAttachments_SVGGesperrt(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	resp := mustDo(t, buildBoardUpload(t, env, "board-1", "att-svg", "logo.svg",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("SVG-Upload: want 415, got %d", resp.StatusCode)
	}
}

func TestBoardAttachments_GroessenlimitGilt(t *testing.T) {
	env := newAttachTestEnv(t) // Testlimit: 1 MB
	defer env.close()

	resp := mustDo(t, buildBoardUpload(t, env, "board-1", "att-gross", "gross.png",
		bytes.Repeat([]byte("A"), 2*1024*1024)))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("Upload über dem Limit: want 413, got %d", resp.StatusCode)
	}
}

func TestBoardAttachments_UngueltigeIDs(t *testing.T) {
	env := newAttachTestEnv(t)
	defer env.close()

	resp := mustDo(t, mustReq(t, "GET", env.srv.URL+"/boards/board-1/board-attachments/..%2Fetc", nil, nil))
	defer resp.Body.Close()
	// Auf 400 prüfen, nicht auf „nicht 200" (Server-Kritiker Sprint 61): Ohne
	// die validAttachmentID-Zeile in downloadAus landet "../etc" im Store,
	// scheitert dort an safePathComponent und ergibt 500 — auch nicht 200,
	// also wäre die schwächere Zusicherung grün geblieben und hätte die
	// Prüfung bewacht, die sie im Namen führt, gerade nicht.
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Pfad-Traversal im attId: want 400, got %d", resp.StatusCode)
	}
}

// Die Reservierung muss auch dort halten, wo sie niemand hingeschrieben hat:
// `handleTransfer` nimmt die Karten-id ebenfalls aus dem Pfad und reicht sie
// an den Store weiter — vor jeder Existenzprüfung. Der Riegel sitzt deshalb
// im Store selbst; diese Tests prüfen ihn dort, wo er wirkt.
func TestBoardAttachments_StoreLehntKartenOperationenAufDerBoardAblageAb(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAttachmentStore(dir)

	// Erst etwas ablegen, damit die Operationen überhaupt etwas zu tun hätten.
	if err := store.Save("board-1", storage.BoardAblageId, "att-1", "bg.png",
		strings.NewReader("bild")); err != nil {
		t.Fatalf("Save in die Board-Ablage muss erlaubt sein: %v", err)
	}

	if _, err := store.StageCardAttachments("board-1", "board-2", storage.BoardAblageId); !errors.Is(err, storage.ErrReservierteAblage) {
		t.Fatalf("StageCardAttachments auf die Board-Ablage: want ErrReservierteAblage, got %v", err)
	}
	if err := store.DeleteCard("board-1", storage.BoardAblageId); !errors.Is(err, storage.ErrReservierteAblage) {
		t.Fatalf("DeleteCard auf die Board-Ablage: want ErrReservierteAblage, got %v", err)
	}

	// Und der Beleg, dass die Ablehnung wirkt statt nur zu melden: Die Datei
	// liegt noch da. Ohne den Riegel hätte DeleteCard sie gelöscht.
	if _, _, err := store.Open("board-1", storage.BoardAblageId, "att-1"); err != nil {
		t.Fatalf("der Board-Anhang ist weg — die Ablehnung kam zu spät: %v", err)
	}
}
