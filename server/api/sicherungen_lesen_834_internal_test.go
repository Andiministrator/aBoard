package api

// Karte #834 (ADR-0142): Schreibfrist als reine Funktion, Weg-Freigabe der
// Schranke für abb_ und die gesetzte Frist an beiden Download-Wegen.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestSicherungen834_Schreibfrist(t *testing.T) {
	for _, f := range []struct {
		groesse int64
		soll    time.Duration
	}{
		{-1, 60 * time.Second},
		{0, 60 * time.Second},
		{1, 61 * time.Second},
		{256 * 1024, 61 * time.Second},
		{256*1024 + 1, 62 * time.Second},
		{100 << 20, 460 * time.Second},          // 100 MiB = 400 Blöcke
		{1740 * 256 * 1024, 30 * time.Minute},   // genau am Deckel
		{1740*256*1024 + 1, 30 * time.Minute},   // knapp darüber
		{1 << 30, 30 * time.Minute},             // 1 GiB
		{math.MaxInt64, 30 * time.Minute},       // kein Überlauf
		{1739 * 256 * 1024, 1799 * time.Second}, // knapp darunter
	} {
		if got := SicherungSchreibfrist(f.groesse); got != f.soll {
			t.Errorf("SicherungSchreibfrist(%d) = %s, erwartet %s", f.groesse, got, f.soll)
		}
	}
}

func TestSicherungen834_WegFreigabe(t *testing.T) {
	for _, f := range []struct {
		methode, pfad string
		roh           string
		soll          bool
	}{
		{"GET", "/sicherungen", "", true},
		{"HEAD", "/sicherungen", "", true},
		{"GET", "/sicherungen/b1/boardbackup-b1-20261019-010203-abc.tar.gz.enc", "", true},
		{"HEAD", "/sicherungen/b1/datei", "", true},
		{"POST", "/sicherungen", "", false},
		{"PUT", "/sicherungen/b1/datei", "", false},
		{"DELETE", "/sicherungen/b1/datei", "", false},
		{"GET", "/sicherungen/", "", false},
		{"GET", "/sicherungen/b1", "", false},
		{"GET", "/sicherungen/b1/", "", false},
		{"GET", "/sicherungen/b1/a/b", "", false},
		{"GET", "/sicherungen/../datei", "", false},
		{"GET", "/sicherungen/b1/..", "", false},
		{"GET", "/sicherungenx", "", false},
		{"GET", "/sicherungen/b1/datei", "/sicherungen/b1%2Fx/datei", false},
		{"GET", "/boards/b1/backup/files", "", false},
		{"GET", "/boards/b1/backup/download/datei", "", false},
		{"GET", "/schatten/b1", "", false},
		{"GET", "/boardpaar/auftraege", "", false},
		{"POST", "/backup/restore", "", false},
		{"POST", BoardpaarErneuernPfad, "", true},
	} {
		r := httptest.NewRequest(f.methode, "http://x/", nil)
		r.URL.Path, r.URL.RawPath = f.pfad, f.roh
		if got := istBoardpaarWegFuer(storage.BoardpaarTypSicherungen, r); got != f.soll {
			t.Errorf("abb %s %s (roh %q): %v, erwartet %v", f.methode, f.pfad, f.roh, got, f.soll)
		}
	}
	// Die anderen Typen erreichen die Sicherungen nicht.
	for _, typ := range []string{storage.BoardpaarTypAusgang, storage.BoardpaarTypEingang, storage.BoardpaarTypRueckweg} {
		for _, p := range []string{"/sicherungen", "/sicherungen/b1/datei"} {
			r := httptest.NewRequest("GET", "http://x"+p, nil)
			if istBoardpaarWegFuer(typ, r) {
				t.Errorf("%s erreicht GET %s", typ, p)
			}
		}
	}
}

func TestSicherungen834_Abholbar(t *testing.T) {
	for _, f := range []struct {
		board, name string
		soll        bool
	}{
		{"b1", "boardbackup-b1-20261019-010203-abcdef12.tar.gz.enc", true},
		{"b1", "boardbackup-b1-20261019-010203-abcdef12.tar.gz", false},           // unverschlüsselt
		{"b1", "boardbackup-b1-20261019-010203-123456-pending.tar.gz.enc", false}, // unfertig
		{"b1", "boardbackup-b2-20261019-010203-abcdef12.tar.gz.enc", false},       // fremdes Board
		{"b1", "boardbackup-b1-x-20261019-010203-abcdef12.tar.gz.enc", false},     // Präfix eines anderen Boards b1-x
		{"b1-x", "boardbackup-b1-x-20261019-010203-abcdef12.tar.gz.enc", true},
		{"..", "boardbackup-..-20261019-010203-abcdef12.tar.gz.enc", false},
		{"b1", "../boardbackup-b1-20261019-010203-abcdef12.tar.gz.enc", false},
		{"b1", "boardbackup-b1-20261019-010203-abcdef12.tar.gz.enc.tmp", false},
	} {
		if got := sicherungAbholbar(f.board, f.name); got != f.soll {
			t.Errorf("sicherungAbholbar(%q, %q) = %v, erwartet %v", f.board, f.name, got, f.soll)
		}
	}
}

// fristSchreiber: ein ResponseWriter, der die gesetzte Schreibfrist festhält
// (http.ResponseController ruft SetWriteDeadline des Writers).
type fristSchreiber struct {
	*httptest.ResponseRecorder
	frist time.Time
}

func (f *fristSchreiber) SetWriteDeadline(t time.Time) error { f.frist = t; return nil }

// Beide Download-Wege setzen die Frist nach Dateigröße (ADR-0142 §5) — der
// Menschenweg hatte bis #834 keine.
func TestSicherungen834_FristAnBeidenWegen(t *testing.T) {
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
	if _, err := boards.Insert(ctx, "b1", "B", "2", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	name := "boardbackup-b1-20261019-010203-abcdef12.tar.gz.enc"
	groesse := 3*256*1024 + 5 // vier angefangene Blöcke → 64 s
	if err := os.MkdirAll(filepath.Join(dir, "b1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b1", name), make([]byte, groesse), 0o600); err != nil {
		t.Fatal(err)
	}
	paar := storage.NewBoardpaarRepo(db)
	abb, _, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypSicherungen, "abholung", "t")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewBoardBackupAPI(boards, nil, nil, nil, cfg, t.TempDir(), "test").Register(mux)
	NewSicherungenAPI(boards, cfg, paar).Register(mux)

	for _, f := range []struct{ weg, pfad, token string }{
		{"Menschenweg", "/boards/b1/backup/download/" + name, ""},
		{"Token-Weg", "/sicherungen/b1/" + name, abb},
	} {
		req := httptest.NewRequest("GET", "http://x"+f.pfad, nil)
		if f.token != "" {
			req.Header.Set("Authorization", "Bearer "+f.token)
		}
		w := &fristSchreiber{ResponseRecorder: httptest.NewRecorder()}
		vorher := time.Now()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.Len() != groesse {
			t.Fatalf("%s: %d, %d Bytes", f.weg, w.Code, w.Body.Len())
		}
		if w.frist.IsZero() {
			t.Errorf("%s: keine Schreibfrist gesetzt", f.weg)
			continue
		}
		if d := w.frist.Sub(vorher); d < 63*time.Second || d > 65*time.Second {
			t.Errorf("%s: Frist %s, erwartet etwa 64 s", f.weg, d)
		}
	}
}
