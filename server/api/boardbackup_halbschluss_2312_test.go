package api_test

// F-2312 (Befund des Partnerprojekts, 19.10.): POST /backup/restore lieferte 403 „kein
// Zugriff auf das Quell-Board“ trotz Admin, wenn der Client nach dem Senden
// seine Schreibseite schloss (nc). Go beendet dann den Anfragekontext; die
// Rechteprüfung las mit abgebrochenem Kontext und wertete den Fehler als
// „kein Recht“. Dazu: ein echter Lesefehler der Rechteprüfung ist 503.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// archiv2312: Backup von Board b1 (Eigentümer owner@x.de) und Admin admin@x.de.
func archiv2312(t *testing.T, env *bbTestEnv) string {
	t.Helper()
	env.seedBoard(t, "b1", "Quelle", "owner@x.de")
	env.seedUser(t, "admin@x.de", true)
	resp := env.do(t, "POST", "/boards/b1/backup/now", "owner@x.de", nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("backup/now: %d", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	fn, _ := body["filename"].(string)
	return filepath.Join(env.bkpDir, "b1", fn)
}

// Admin (nicht Eigentümer), as-new, Anfrage über rohes TCP; nach dem Body
// wird die Schreibseite geschlossen wie bei nc. Erwartet 200.
func TestRestore2312_HalbschlussNachDemSenden(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	pfad := archiv2312(t, env)
	roh, err := os.ReadFile(pfad)
	if err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	_ = mw.WriteField("mode", "as-new")
	fw, _ := mw.CreateFormFile("file", "backup.tar.gz")
	_, _ = fw.Write(roh)
	_ = mw.Close()

	req, err := http.NewRequest(http.MethodPost, env.srv.URL+"/backup/restore", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Remote-Email", "admin@x.de")
	req.Header.Set("Remote-User", "admin@x.de")
	req.Close = true

	conn, err := net.Dial("tcp", strings.TrimPrefix(env.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("Restore mit Halbschluss: %d %s, erwartet 200", resp.StatusCode, b)
	}
}

// Die Rechteprüfung kann nicht lesen (Tabelle users weg): 503, nicht 403 —
// und es entsteht kein Board.
func TestRestore2312_LesefehlerIst503(t *testing.T) {
	env := newBBTestEnv(t)
	defer env.close()
	pfad := archiv2312(t, env)
	if _, err := env.db.ExecContext(context.Background(), `ALTER TABLE users RENAME TO users_weg_2312`); err != nil {
		t.Fatal(err)
	}
	resp := env.uploadRestore(t, "admin@x.de", pfad, "as-new", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("Lesefehler: %d %s, erwartet 503", resp.StatusCode, b)
	}
	boards, _ := env.boards.List(context.Background())
	if len(boards) != 1 {
		t.Fatalf("trotz 503 Boards angelegt: %d", len(boards))
	}
}
