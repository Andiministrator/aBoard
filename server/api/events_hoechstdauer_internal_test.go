package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// TestEventsStream_EndetNachHoechstdauer — Kritiker Sprint 70, K-A2: Ein
// Board-Stream endet nach sseHoechstdauer, damit der Client neu verbindet und
// der ACLGuard die Rechte erneut prüft. Ohne Obergrenze bekäme ein Tab nach
// einem Rechteentzug unbegrenzt weiter board:updated. Die Höchstdauer ist hier
// auf 400 ms verkürzt, die Schreibfrist des Servers auf 200 ms: Der Stream
// muss die Schreibfrist überleben (rollierende Frist, K-A3) und trotzdem an
// der Höchstdauer enden.
func TestEventsStream_EndetNachHoechstdauer(t *testing.T) {
	alt := [3]time.Duration{heartbeatInterval, sseHoechstdauer, streamSchreibfrist}
	heartbeatInterval, sseHoechstdauer, streamSchreibfrist = 50*time.Millisecond, 400*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { heartbeatInterval, sseHoechstdauer, streamSchreibfrist = alt[0], alt[1], alt[2] })

	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := storage.NewBoardRepo(db)
	broker := NewEventBroker()
	mux := http.NewServeMux()
	NewBoardsAPI(repo).WithBroker(broker).Register(mux)
	NewEventsAPI(broker, repo).Register(mux)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.WriteTimeout = 200 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	body := `{"version":"2","board":{"id":"b1","title":"X","columnDefs":[],"sprints":[],"awaitingUser":null}}`
	cr, err := srv.Client().Post(srv.URL+"/boards", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cr.Body.Close()

	start := time.Now()
	resp, err := srv.Client().Get(srv.URL + "/boards/b1/events")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	for {
		_, rerr := br.ReadString('\n')
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) && !strings.Contains(rerr.Error(), "unexpected EOF") {
				t.Fatalf("Stream endete mit Fehler statt sauber: %v", rerr)
			}
			break
		}
		if time.Since(start) > 3*time.Second {
			t.Fatalf("Stream läuft nach 3 s noch — keine Höchstdauer (K-A2)")
		}
	}
	dauer := time.Since(start)
	if dauer < 300*time.Millisecond {
		t.Fatalf("Stream endete nach %v — vor der Höchstdauer, vermutlich an der Server-Schreibfrist (K-A3)", dauer)
	}
}
