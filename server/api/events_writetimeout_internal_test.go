package api

// events_writetimeout_internal_test.go — F-1127 (#544, Sprint 70): belegt
// und verifiziert, dass der SSE-Stream (/boards/{id}/events) nicht mehr an
// der Server.WriteTimeout-Grenze (main.go: 30s) hängen bleibt.
//
// Package `api` statt `api_test`, weil wir heartbeatInterval — bewusst als
// unexportierte var angelegt, "damit Tests sie temporär verkürzen können"
// (siehe events.go) — für die Testlaufzeit verkürzen müssen; das ist von
// außerhalb des Pakets nicht möglich. Baut deshalb einen eigenen,
// minimalen Testaufbau statt den Helfer aus events_test.go (package
// api_test) wiederzuverwenden.
//
// Mechanik: Go's http.Server wendet WriteTimeout als ABSOLUTE Deadline auf
// die Connection an, gesetzt beim Start der Anfrage (net/http-Doku:
// "WriteTimeout is the maximum duration before timing out writes of the
// response"). Für eine einzelne, lange offen gehaltene Streaming-Anfrage
// gibt es kein "neue Anfrage gelesen"-Ereignis, das die Deadline
// zurücksetzt — jeder positive WriteTimeout-Wert schneidet einen Stream,
// der länger offen bleibt, irgendwann mitten im Schreiben ab.
//
// Um die Testlaufzeit klein zu halten, simulieren wir main.go's 30s mit
// 500ms (WriteTimeout) und verkürzen heartbeatInterval auf 100ms, damit
// mehrere Heartbeats innerhalb der Testlaufzeit über die Schwelle hinaus
// gelesen werden. Die Übertragbarkeit auf 30s ist strukturell: Go's
// http.Server-Mechanik unterscheidet nicht zwischen 500ms und 30s — nur
// SetWriteDeadline(time.Time{}) hebt die Deadline für DIESE Connection
// unabhängig von ihrer Höhe auf (siehe Fix in events.go).

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// readLineDeadline liest eine Zeile mit Timeout — bufio.Reader hat selbst
// keinen Timeout. Eigene, dem Paket lokale Kopie des Musters aus
// events_test.go (dort package api_test, hier nicht wiederverwendbar).
func readLineDeadline(br *bufio.Reader, d time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := br.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		return r.line, r.err
	case <-time.After(d):
		return "", fmt.Errorf("readLine: timeout nach %v", d)
	}
}

func TestEventsStream_SurvivesBeyondServerWriteTimeout(t *testing.T) {
	origInterval := heartbeatInterval
	heartbeatInterval = 100 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = origInterval })

	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
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
	// Steht stellvertretend für main.go's WriteTimeout=30s (server/main.go,
	// Z. 1043) — auf 500ms verkürzt, siehe Datei-Kopfkommentar.
	srv.Config.WriteTimeout = 500 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	// Board anlegen, damit /events nicht 404 liefert. Envelope-Form wie
	// sampleBody() in boards_test.go (package api_test, von hier aus package
	// api nicht importierbar) — minimal-valides board+version-Envelope.
	createBody := `{"version":"2","board":{"id":"b1","title":"X","columnDefs":[],"sprints":[],"marker":"wt-test","awaitingUser":null}}`
	createReq, err := http.NewRequest(http.MethodPost, srv.URL+"/boards", strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("NewRequest create: %v", err)
	}
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := srv.Client().Do(createReq)
	if err != nil {
		t.Fatalf("create board: %v", err)
	}
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create board status = %d, erwartet %d", createResp.StatusCode, http.StatusCreated)
	}

	getCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(getCtx, http.MethodGet, srv.URL+"/boards/b1/events", nil)
	if err != nil {
		t.Fatalf("NewRequest events: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d, erwartet 200", resp.StatusCode)
	}

	br := bufio.NewReader(resp.Body)

	// Über die WriteTimeout-Schwelle (500ms) hinaus lesen — insgesamt
	// 1.2s, also mehr als das Doppelte. Ohne den F-1127-Fix bricht die
	// Verbindung spätestens bei ~500ms ab (Go schließt sie serverseitig,
	// der Client-Read liefert dann einen Fehler statt weiterer Zeilen).
	deadline := time.Now().Add(1200 * time.Millisecond)
	commentLines := 0
	for time.Now().Before(deadline) {
		line, err := readLineDeadline(br, 400*time.Millisecond)
		if err != nil {
			t.Fatalf("Stream brach nach %d gelesenen Kommentar-/Heartbeat-Zeilen ab (WriteTimeout=%v): %v",
				commentLines, srv.Config.WriteTimeout, err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trimmed, ":") {
			commentLines++
		}
	}
	// Mind. 2 Heartbeats/Comments in 1.2s bei 100ms-Intervall — grosszügige
	// Toleranz nach unten für langsame CI-Läufer.
	if commentLines < 2 {
		t.Fatalf("nur %d Kommentar-/Heartbeat-Zeilen in 1.2s gelesen — Testaufbau prüfen (heartbeatInterval=%v)", commentLines, heartbeatInterval)
	}
}
