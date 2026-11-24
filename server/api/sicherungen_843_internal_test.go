package api

// Karte #843: Sicherungen härten (F-2298, F-2305, F-2306).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	enc843 = "boardbackup-b1-20261019-010203-abcdef12.tar.gz.enc"
	b2_843 = "boardbackup-b2-20261019-010203-abcdef22.tar.gz.enc"
)

type umgebung843 struct {
	mux    *http.ServeMux
	boards *storage.BoardRepo
	dir    string
	abb    string
}

func neueUmgebung843(t *testing.T, boardIDs ...string) *umgebung843 {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	n := 0
	cfg := storage.NewConfigRepo(db, nil, func() string { n++; return fmt.Sprintf("a-%d", n) })
	dir := filepath.Join(t.TempDir(), "sicherungen")
	if err := cfg.Set(ctx, "backup_board.dir", dir, false, "t"); err != nil {
		t.Fatal(err)
	}
	boards := storage.NewBoardRepo(db)
	for _, id := range boardIDs {
		if _, err := boards.Insert(ctx, id, "B "+id, "2", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	paar := storage.NewBoardpaarRepo(db)
	abb, _, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypSicherungen, "abholung", "t")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewBoardBackupAPI(boards, nil, nil, nil, cfg, t.TempDir(), "test").Register(mux)
	NewSicherungenAPI(boards, cfg, paar).Register(mux)
	return &umgebung843{mux: mux, boards: boards, dir: dir, abb: abb}
}

func (u *umgebung843) datei(t *testing.T, board, name string, inhalt []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(u.dir, board), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(u.dir, board, name)
	if err := os.WriteFile(p, inhalt, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// F-2298: Menschenweg liefert/löscht nur Dateien mit dem Präfix des Boards.
func TestSicherungen843_MenschenwegPraefix(t *testing.T) {
	u := neueUmgebung843(t, "b1", "b2")
	fremd := u.datei(t, "b1", b2_843, []byte("fremd")) // Datei von b2 im Ordner von b1
	eigen := u.datei(t, "b1", enc843, []byte("eigen"))

	w := httptest.NewRecorder()
	u.mux.ServeHTTP(w, httptest.NewRequest("GET", "/boards/b1/backup/download/"+b2_843, nil))
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "fremd") {
		t.Errorf("download fremdes Präfix: %d %q, erwartet 400", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	u.mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/boards/b1/backup/files/"+b2_843, nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("delete fremdes Präfix: %d, erwartet 400", w.Code)
	}
	if _, err := os.Stat(fremd); err != nil {
		t.Errorf("fremde Datei wurde gelöscht: %v", err)
	}
	// Eigene Datei bleibt erreichbar.
	w = httptest.NewRecorder()
	u.mux.ServeHTTP(w, httptest.NewRequest("GET", "/boards/b1/backup/download/"+enc843, nil))
	if w.Code != http.StatusOK || w.Body.String() != "eigen" {
		t.Errorf("download eigene Datei: %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	u.mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/boards/b1/backup/files/"+enc843, nil))
	if w.Code != http.StatusNoContent {
		t.Errorf("delete eigene Datei: %d", w.Code)
	}
	if _, err := os.Stat(eigen); !os.IsNotExist(err) {
		t.Errorf("eigene Datei nicht gelöscht: %v", err)
	}
}

type liste843 struct {
	Boards []struct {
		BoardID     string `json:"board_id"`
		BoardStatus string `json:"board_status"`
		Fehler      string `json:"fehler"`
		Files       []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	} `json:"boards"`
}

func (u *umgebung843) liste(t *testing.T) (int, string, liste843) {
	t.Helper()
	req := httptest.NewRequest("GET", "/sicherungen", nil)
	req.Header.Set("Authorization", "Bearer "+u.abb)
	w := httptest.NewRecorder()
	u.mux.ServeHTTP(w, req)
	var l liste843
	_ = json.Unmarshal(w.Body.Bytes(), &l)
	return w.Code, w.Body.String(), l
}

// F-2305: Ein unlesbarer Board-Ordner macht nicht die ganze Liste zum 500;
// er meldet seinen Fehler (ohne Pfad), die übrigen Boards kommen.
func TestSicherungen843_FehlerJeBoard(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("als root sind Dateirechte wirkungslos")
	}
	u := neueUmgebung843(t, "b1", "b2")
	u.datei(t, "b1", enc843, []byte("x"))
	u.datei(t, "b2", b2_843, []byte("y"))
	u.datei(t, "bo", "boardbackup-bo-20261019-010203-abcdef33.tar.gz.enc", []byte("z")) // ohne Board, später unlesbar
	for _, id := range []string{"b1", "bo"} {
		p := filepath.Join(u.dir, id)
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
	}
	st, body, l := u.liste(t)
	if st != http.StatusOK {
		t.Fatalf("Liste: %d %s, erwartet 200", st, body)
	}
	if strings.Contains(body, u.dir) || strings.Contains(body, "permission") {
		t.Errorf("Liste verrät Pfad oder Systemfehler: %s", body)
	}
	got := map[string]string{}
	for _, e := range l.Boards {
		got[e.BoardID] = fmt.Sprintf("%s|%s|%d", e.BoardStatus, e.Fehler, len(e.Files))
	}
	soll := map[string]string{
		"b1": "aktiv|ordner nicht lesbar|0",
		"b2": "aktiv||1",
		"bo": "ohne-board|ordner nicht lesbar|0",
	}
	if fmt.Sprint(got) != fmt.Sprint(soll) {
		t.Errorf("Liste:\n %v\nerwartet\n %v", got, soll)
	}
}

// F-2305: Ordner ungültiger Namen und Dateien in der Wurzel erscheinen nicht.
func TestSicherungen843_OrdnerFilter(t *testing.T) {
	u := neueUmgebung843(t, "b1")
	u.datei(t, "b1", enc843, []byte("x"))
	u.datei(t, ".versteckt", "boardbackup-.versteckt-20261019-010203-abcdef12.tar.gz.enc", []byte("x"))
	u.datei(t, "mit.punkt", "boardbackup-mit.punkt-20261019-010203-abcdef12.tar.gz.enc", []byte("x"))
	if err := os.WriteFile(filepath.Join(u.dir, "datei"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, body, l := u.liste(t)
	if st != http.StatusOK || len(l.Boards) != 1 || l.Boards[0].BoardID != "b1" {
		t.Errorf("Liste: %d %s, erwartet nur b1", st, body)
	}
}

// langsam843: verzögert jedes Write und reicht per Unwrap die Frist durch.
type langsam843 struct{ http.ResponseWriter }

func (l *langsam843) Write(p []byte) (int, error) {
	time.Sleep(60 * time.Millisecond)
	return l.ResponseWriter.Write(p)
}
func (l *langsam843) Unwrap() http.ResponseWriter { return l.ResponseWriter }

// F-2306: Gegen einen echten Server mit kurzer WriteTimeout (200 ms): Das
// Archiv wird länger als diese Frist ausgeliefert und kommt vollständig an —
// nur wenn der Handler die Frist an der echten Verbindung verlängert.
func TestSicherungen843_FristAmEchtenServer(t *testing.T) {
	u := neueUmgebung843(t, "b1")
	inhalt := make([]byte, 8*32*1024) // acht 32-KiB-Schreibvorgänge à 60 ms ≈ 480 ms
	for i := range inhalt {
		inhalt[i] = byte(i)
	}
	u.datei(t, "b1", enc843, inhalt)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mux.ServeHTTP(&langsam843{w}, r)
	}))
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	for _, weg := range []struct{ name, pfad, token string }{
		{"Menschenweg", "/boards/b1/backup/download/" + enc843, ""},
		{"Token-Weg", "/sicherungen/b1/" + enc843, u.abb},
	} {
		req, _ := http.NewRequest("GET", srv.URL+weg.pfad, nil)
		if weg.token != "" {
			req.Header.Set("Authorization", "Bearer "+weg.token)
		}
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", weg.name, err)
		}
		got, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if dauer := time.Since(start); dauer < 300*time.Millisecond {
			t.Errorf("%s: Auslieferung dauerte nur %s, der Test prüft die Frist nicht", weg.name, dauer)
		}
		if err != nil || len(got) != len(inhalt) {
			t.Errorf("%s: %d von %d Bytes, Fehler %v", weg.name, len(got), len(inhalt), err)
		}
	}
}

// F-2325: Ein Board mit Datenbankzeile, dessen Kennung nicht auf sicherungBoardRe
// passt, steht mit `fehler` in der Liste statt still mit `files: []`. Ein
// gewöhnliches leeres Board bleibt ohne `fehler` (F-2322).
func TestSicherungen2325_KennungUngueltig(t *testing.T) {
	u := neueUmgebung843(t, "b1", "kein gueltiger.name")
	st, body, l := u.liste(t)
	if st != http.StatusOK {
		t.Fatalf("Liste: %d %s", st, body)
	}
	got := map[string]string{}
	for _, e := range l.Boards {
		got[e.BoardID] = fmt.Sprintf("%s|%s|%d", e.BoardStatus, e.Fehler, len(e.Files))
	}
	soll := map[string]string{
		"b1":                  "aktiv||0",
		"kein gueltiger.name": "aktiv|kennung ungueltig|0",
	}
	if fmt.Sprint(got) != fmt.Sprint(soll) {
		t.Errorf("Liste:\n %v\nerwartet\n %v", got, soll)
	}
	if !strings.Contains(body, `"files":[]`) {
		t.Errorf("files muss ein leeres Array sein: %s", body)
	}
}
