package api

// Karte #779 (Vorprüfung P-14): je Board höchstens ein Lauf gleichzeitig.
// Ein zweiter PUT bekommt 429, statt sich hinter der Schreibsperre zu
// stapeln; nach dem Lauf ist das Board wieder frei.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestSchattenEingang779_EinLaufJeBoard(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := storage.NewBoardRepo(db).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1","sprints":[]}}`)); err != nil {
		t.Fatal(err)
	}
	cfg := storage.NewConfigRepo(db, nil, func() string { return "x" })
	if err := cfg.Set(ctx, KonfigBoardpaarSeite, BoardpaarSeiteAussen, false, "test"); err != nil {
		t.Fatal(err)
	}
	paar := storage.NewBoardpaarRepo(db)
	if _, err := paar.AussenKoppeln(ctx, "b1", "https://innen.example/#board=i", "test"); err != nil {
		t.Fatal(err)
	}
	abe, _, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "push", "test")
	if err != nil {
		t.Fatal(err)
	}
	a := NewSchattenEingangAPI(cfg, paar, storage.NewSchattenRepo(db), nil)
	mux := http.NewServeMux()
	a.Register(mux)
	anfrage := func() int {
		req := httptest.NewRequest("PUT", "/schatten/b1", strings.NewReader(`{"version":"1.0.0","schatten":[]}`))
		req.Header.Set("Authorization", "Bearer "+abe)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	// Abgelehnte Läufe (422, 413) geben die Sperre wieder frei (K-7).
	for _, body := range []string{`{`, `{"version":"1.0.0","schatten":[],"x":"` + strings.Repeat("a", 32<<20) + `"}`} {
		req := httptest.NewRequest("PUT", "/schatten/b1", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+abe)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 422 && rec.Code != 413 {
			t.Fatalf("Ablehnung: %d", rec.Code)
		}
		if s := anfrage(); s != http.StatusOK {
			t.Errorf("nach Ablehnung %d: %d — Sperre nicht freigegeben", rec.Code, s)
		}
	}
	a.laeuft.Store("b1", struct{}{})
	if s := anfrage(); s != http.StatusTooManyRequests {
		t.Errorf("zweiter Lauf: %d, erwartet 429", s)
	}
	a.laeuft.Delete("b1")
	if s := anfrage(); s != http.StatusOK {
		t.Errorf("nach Freigabe: %d", s)
	}
	if s := anfrage(); s != http.StatusOK {
		t.Errorf("Sperre nach dem Lauf nicht freigegeben: %d", s)
	}
}

// eingangUmgebung779 baut Datenbank, gekoppeltes Board und Token für die
// Tests im Paket.
func eingangUmgebung779(t *testing.T) (*SchattenEingangAPI, *http.ServeMux, string, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := storage.NewBoardRepo(db).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1","sprints":[]}}`)); err != nil {
		t.Fatal(err)
	}
	cfg := storage.NewConfigRepo(db, nil, func() string { return "x" })
	if err := cfg.Set(ctx, KonfigBoardpaarSeite, BoardpaarSeiteAussen, false, "test"); err != nil {
		t.Fatal(err)
	}
	paar := storage.NewBoardpaarRepo(db)
	if _, err := paar.AussenKoppeln(ctx, "b1", "https://innen.example/#board=i", "test"); err != nil {
		t.Fatal(err)
	}
	abe, _, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "push", "test")
	if err != nil {
		t.Fatal(err)
	}
	a := NewSchattenEingangAPI(cfg, paar, storage.NewSchattenRepo(db), nil)
	mux := http.NewServeMux()
	a.Register(mux)
	return a, mux, abe, db
}

// K-3 (Kritiker #779): Die Frist des Eingangs gilt auch für die Verbindung.
// Ein Server mit kurzer ReadTimeout/WriteTimeout (wie die Listener, dort
// 30 s) bricht einen langsamen Upload sonst ab — hier 300 ms gegen einen
// Upload über ~900 ms.
func TestSchattenEingang779_VerbindungsFrist(t *testing.T) {
	_, mux, abe, _ := eingangUmgebung779(t)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ReadTimeout = 300 * time.Millisecond
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	pr, pw := io.Pipe()
	go func() {
		teile := []string{`{"version":"1.0.0",`, `"schatten":`, `[]}`}
		for _, teil := range teile {
			time.Sleep(300 * time.Millisecond)
			_, _ = pw.Write([]byte(teil))
		}
		_ = pw.Close()
	}()
	req, _ := http.NewRequest("PUT", srv.URL+"/schatten/b1", pr)
	req.Header.Set("Authorization", "Bearer "+abe)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("langsamer Upload abgebrochen: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status %d", resp.StatusCode)
	}
}

// Verify #779 V-1: Die Frist begrenzt den Upload tatsächlich — ein Upload,
// der länger dauert als die (hier verkürzte) Frist, endet, auch wenn der
// Server selbst keine Lesefrist hat.
func TestSchattenEingang779_FristBegrenztUpload(t *testing.T) {
	_, mux, abe, _ := eingangUmgebung779(t)
	alt := schattenEingangFrist
	schattenEingangFrist = 200 * time.Millisecond
	t.Cleanup(func() { schattenEingangFrist = alt })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	pr, pw := io.Pipe()
	go func() {
		for _, teil := range []string{`{"version":"1.0.0",`, `"schatten":`, `[]}`} {
			time.Sleep(250 * time.Millisecond)
			if _, err := pw.Write([]byte(teil)); err != nil {
				return
			}
		}
		_ = pw.Close()
	}()
	req, _ := http.NewRequest("PUT", srv.URL+"/schatten/b1", pr)
	req.Header.Set("Authorization", "Bearer "+abe)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Auch ein Abbruch der Verbindung ist die Lesefrist bei der Arbeit —
		// ohne Lesefrist läuft der Upload durch und es kommt 503, nie ein
		// Fehler (Gegenprobe). Andere Fehler zählen nicht (Sitzungs-QA C-2).
		if s := err.Error(); !errors.Is(err, io.EOF) && !strings.Contains(s, "reset") && !strings.Contains(s, "broken pipe") && !strings.Contains(s, "EOF") {
			t.Errorf("unerwarteter Fehler: %v", err)
		}
	} else {
		defer resp.Body.Close()
		// Genau 400 vom Lesen — ein 503 hieße, der Upload lief durch und erst
		// die Datenbank-Frist schlug zu (Gegenprobe ohne Lesefrist).
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Status %d, erwartet 400 (Upload über die Frist abgebrochen)", resp.StatusCode)
		}
	}
	_ = pr.CloseWithError(io.ErrClosedPipe)
}

// K-7/K-8: Zuordnung der Fehler aus Ersetze — entkoppelt während des Laufs
// 404, belegt oder Frist um 503, sonst 500.
func TestSchattenEingang779_ErsetzeFehler(t *testing.T) {
	faelle := []struct {
		err  error
		want int
	}{
		{storage.ErrBoardpaarNichtGekoppelt, 404},
		{fmt.Errorf("x: %w", storage.ErrDBBelegt), 503},
		{context.DeadlineExceeded, 503},
		{errors.New("kaputt"), 500},
	}
	for _, f := range faelle {
		if s, _ := ersetzeFehler(f.err); s != f.want {
			t.Errorf("%v: %d, erwartet %d", f.err, s, f.want)
		}
	}
}
