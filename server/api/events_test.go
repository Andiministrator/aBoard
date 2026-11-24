package api_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/storage"
)

// ── EventBroker — Subscribe / Publish / Unsubscribe ─────────────────────

func TestEventBroker_PublishToSingleSubscriber(t *testing.T) {
	broker := api.NewEventBroker()
	ch, unsubscribe := broker.Subscribe("b1")
	defer unsubscribe()

	if got := broker.SubscriberCount("b1"); got != 1 {
		t.Fatalf("SubscriberCount = %d, erwartet 1", got)
	}

	delivered := broker.Publish("b1", api.Event{Type: "board:updated", Data: `{"x":1}`, ID: "100"})
	if delivered != 1 {
		t.Errorf("Publish lieferte an %d Subscriber, erwartet 1", delivered)
	}

	select {
	case ev := <-ch:
		if ev.Type != "board:updated" || ev.Data != `{"x":1}` || ev.ID != "100" {
			t.Errorf("Event-Inhalt unerwartet: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("Timeout — kein Event empfangen")
	}
}

func TestEventBroker_PublishToMultipleSubscribers(t *testing.T) {
	broker := api.NewEventBroker()
	ch1, u1 := broker.Subscribe("b1")
	ch2, u2 := broker.Subscribe("b1")
	defer u1()
	defer u2()

	if got := broker.SubscriberCount("b1"); got != 2 {
		t.Fatalf("SubscriberCount = %d, erwartet 2", got)
	}

	delivered := broker.Publish("b1", api.Event{Type: "board:updated", Data: "hi"})
	if delivered != 2 {
		t.Errorf("Publish lieferte an %d Subscriber, erwartet 2", delivered)
	}

	for i, ch := range []<-chan api.Event{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != "board:updated" {
				t.Errorf("Subscriber %d: Type = %q", i+1, ev.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("Subscriber %d: Timeout", i+1)
		}
	}
}

func TestEventBroker_PublishOnlyToBoardSubscribers(t *testing.T) {
	broker := api.NewEventBroker()
	chB1, u1 := broker.Subscribe("b1")
	chB2, u2 := broker.Subscribe("b2")
	defer u1()
	defer u2()

	broker.Publish("b1", api.Event{Type: "board:updated", Data: "1"})

	select {
	case ev := <-chB1:
		if ev.Data != "1" {
			t.Errorf("b1: Data = %q", ev.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("b1: Timeout")
	}

	// b2 darf KEIN Event bekommen
	select {
	case ev := <-chB2:
		t.Errorf("b2 empfing Event obwohl nicht zugehörig: %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// erwartet — kein Event
	}
}

func TestEventBroker_UnsubscribeRemovesAndClosesChannel(t *testing.T) {
	broker := api.NewEventBroker()
	ch, unsubscribe := broker.Subscribe("b1")

	unsubscribe()

	if got := broker.SubscriberCount("b1"); got != 0 {
		t.Errorf("SubscriberCount nach Unsubscribe = %d, erwartet 0", got)
	}

	// Channel muss geschlossen sein
	select {
	case _, ok := <-ch:
		if ok {
			t.Errorf("Channel nach Unsubscribe nicht geschlossen")
		}
	case <-time.After(time.Second):
		t.Fatal("Channel-Read nach Unsubscribe blockiert")
	}

	// Publish nach Unsubscribe darf nicht panicen
	delivered := broker.Publish("b1", api.Event{Type: "board:updated", Data: "x"})
	if delivered != 0 {
		t.Errorf("Publish lieferte nach Unsubscribe an %d, erwartet 0", delivered)
	}
}

func TestEventBroker_UnsubscribeIdempotent(t *testing.T) {
	broker := api.NewEventBroker()
	_, unsubscribe := broker.Subscribe("b1")
	unsubscribe()
	// Zweiter Aufruf darf nicht panicen (double-close-Schutz)
	unsubscribe()
	unsubscribe()
}

// TestEventBroker_PublishNonBlockingOnFullSubscriber sichert ab, dass ein
// langsamer Subscriber den Publisher nicht blockiert (siehe events.go
// Publish-Doc). Wir füllen den Buffer und prüfen dass ein zusätzliches
// Publish nicht hängt.
func TestEventBroker_PublishNonBlockingOnFullSubscriber(t *testing.T) {
	broker := api.NewEventBroker()
	_, unsubscribe := broker.Subscribe("b1")
	defer unsubscribe()

	// Buffer ist 8 (subscriberBuffer); 100 Publishes müssen schnell durch.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			broker.Publish("b1", api.Event{Type: "board:updated", Data: "x"})
		}
		close(done)
	}()

	select {
	case <-done:
		// erwartet — Publish blockiert nicht
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blockiert auf vollem Subscriber-Channel")
	}
}

// TestEventBroker_ConcurrentSubscribeUnsubscribe sichert die Mutex-Schutz-
// Annahme ab — 100 Goroutines subscriben/unsubscriben parallel, der Broker
// darf nicht panicen oder Datenrennen produzieren (`-race`-Builds erkennen
// Verstöße automatisch).
func TestEventBroker_ConcurrentSubscribeUnsubscribe(t *testing.T) {
	broker := api.NewEventBroker()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, unsubscribe := broker.Subscribe("b1")
			broker.Publish("b1", api.Event{Type: "board:updated", Data: "x"})
			unsubscribe()
		}()
	}
	wg.Wait()
	if got := broker.SubscriberCount("b1"); got != 0 {
		t.Errorf("SubscriberCount nach Test = %d, erwartet 0", got)
	}
}

// ── SSE-Endpoint /boards/{id}/events ────────────────────────────────────

// eventsTestEnv bündelt Server + DB + Broker.
type eventsTestEnv struct {
	srv    *httptest.Server
	db     *sql.DB
	broker *api.EventBroker
}

func (e *eventsTestEnv) close() {
	e.srv.Close()
	_ = e.db.Close()
}

func newEventsTestEnv(t *testing.T) *eventsTestEnv {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	repo := storage.NewBoardRepo(db)
	broker := api.NewEventBroker()
	mux := http.NewServeMux()
	api.NewBoardsAPI(repo).WithBroker(broker).Register(mux)
	api.NewEventsAPI(broker, repo).Register(mux)
	return &eventsTestEnv{
		srv:    httptest.NewServer(mux),
		db:     db,
		broker: broker,
	}
}

func TestEvents_NotFound(t *testing.T) {
	env := newEventsTestEnv(t)
	defer env.close()
	resp, err := http.Get(env.srv.URL + "/boards/ghost/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("Status = %d, erwartet 404", resp.StatusCode)
	}
}

func TestEvents_HeadersAndInitialFrame(t *testing.T) {
	env := newEventsTestEnv(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Hi"), nil)).Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", env.srv.URL+"/boards/b1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, erwartet text/event-stream", got)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-cache") {
		t.Errorf("Cache-Control = %q", got)
	}

	br := bufio.NewReader(resp.Body)
	line, err := readLineWithTimeout(br, 2*time.Second)
	if err != nil {
		t.Fatalf("Initial-Frame lesen: %v", err)
	}
	if !strings.HasPrefix(line, ":") {
		t.Errorf("Initial-Frame ist kein Comment: %q", line)
	}
}

// TestEvents_BroadcastFromMutation deckt den Hauptpfad ab: SSE-Connect,
// danach PUT auf das Board → Subscriber sieht board:updated-Frame.
func TestEvents_BroadcastFromMutation(t *testing.T) {
	env := newEventsTestEnv(t)
	defer env.close()
	createResp := mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "Vor"), nil))
	createETag := createResp.Header.Get("ETag")
	createResp.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", env.srv.URL+"/boards/b1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	// Initial-Comment konsumieren
	if _, err := readLineWithTimeout(br, 2*time.Second); err != nil {
		t.Fatalf("init-frame: %v", err)
	}
	if _, err := readLineWithTimeout(br, 2*time.Second); err != nil { // Leerzeile
		t.Fatalf("init-frame leer: %v", err)
	}

	// Warten bis der Subscribe in der Server-Goroutine durch ist (Race
	// zwischen Header-Send und broker.Subscribe).
	if !waitFor(func() bool { return env.broker.SubscriberCount("b1") == 1 }, time.Second) {
		t.Fatalf("Subscriber wurde nicht registriert")
	}

	// PUT auslösen
	putResp := mustDo(t, mustReq(t, "PUT", env.srv.URL+"/boards/b1", sampleBody("b1", "Nach"),
		http.Header{"If-Match": []string{createETag}}))
	putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT Status = %d", putResp.StatusCode)
	}

	// SSE-Frame einlesen
	frame, err := readSSEFrame(br, 2*time.Second)
	if err != nil {
		t.Fatalf("SSE-Frame lesen: %v", err)
	}
	if frame.event != "board:updated" {
		t.Errorf("Frame.event = %q, erwartet board:updated", frame.event)
	}
	if frame.id == "" {
		t.Errorf("Frame.id leer, erwartet updated_at")
	}

	var payload struct {
		BoardID   string `json:"board_id"`
		UpdatedAt int64  `json:"updated_at"`
		ETag      string `json:"etag"`
		Action    string `json:"action"`
	}
	if err := json.Unmarshal([]byte(frame.data), &payload); err != nil {
		t.Fatalf("Frame.data JSON: %v — raw=%q", err, frame.data)
	}
	if payload.BoardID != "b1" {
		t.Errorf("payload.board_id = %q", payload.BoardID)
	}
	if payload.Action != "update" {
		t.Errorf("payload.action = %q, erwartet update", payload.Action)
	}
	if payload.ETag == "" {
		t.Errorf("payload.etag leer")
	}
}

func TestEvents_BroadcastOnDelete(t *testing.T) {
	env := newEventsTestEnv(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "X"), nil)).Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", env.srv.URL+"/boards/b1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	_, _ = readLineWithTimeout(br, 2*time.Second) // : connected
	_, _ = readLineWithTimeout(br, 2*time.Second) // \n

	if !waitFor(func() bool { return env.broker.SubscriberCount("b1") == 1 }, time.Second) {
		t.Fatal("Subscriber nicht registriert")
	}

	delResp := mustDo(t, mustReq(t, "DELETE", env.srv.URL+"/boards/b1", nil, nil))
	delResp.Body.Close()

	frame, err := readSSEFrame(br, 2*time.Second)
	if err != nil {
		t.Fatalf("delete-frame: %v", err)
	}
	if !strings.Contains(frame.data, `"action":"delete"`) {
		t.Errorf("delete-frame.action fehlt: %q", frame.data)
	}
}

// TestEvents_DisconnectCleansUpSubscriber prüft den Cleanup-Pfad: Client
// schließt die Connection → Server muss den Subscriber entfernen + die
// Goroutine beenden, sonst leaken wir.
func TestEvents_DisconnectCleansUpSubscriber(t *testing.T) {
	env := newEventsTestEnv(t)
	defer env.close()
	mustDo(t, mustReq(t, "POST", env.srv.URL+"/boards", sampleBody("b1", "X"), nil)).Body.Close()

	beforeGoroutines := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", env.srv.URL+"/boards/b1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	br := bufio.NewReader(resp.Body)
	_, _ = readLineWithTimeout(br, 2*time.Second)

	if !waitFor(func() bool { return env.broker.SubscriberCount("b1") == 1 }, time.Second) {
		t.Fatal("Subscriber nicht registriert")
	}

	// Client beendet
	cancel()
	resp.Body.Close()

	// Server muss den Subscriber loswerden
	if !waitFor(func() bool { return env.broker.SubscriberCount("b1") == 0 }, 2*time.Second) {
		t.Errorf("Subscriber-Cleanup nach Disconnect: count = %d, erwartet 0",
			env.broker.SubscriberCount("b1"))
	}

	// Goroutine-Leak-Sniff: nach 200 ms muss die Anzahl wieder ~stabil sein.
	// Toleranz ±2 — httptest hat eigene Idle-Goroutinen.
	time.Sleep(200 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > beforeGoroutines+3 {
		t.Errorf("Goroutine-Leak vermutet: vorher=%d, nachher=%d", beforeGoroutines, after)
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────

// readLineWithTimeout liest eine Zeile mit Timeout — bufio.Reader hat selbst
// keinen Timeout, also packen wir das in eine Goroutine.
func readLineWithTimeout(br *bufio.Reader, d time.Duration) (string, error) {
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

// sseFrame ist ein einzelner SSE-Event-Frame (id/event/data + Leerzeile).
type sseFrame struct {
	id    string
	event string
	data  string
}

// readSSEFrame liest einen kompletten SSE-Frame bis zur Leerzeile. Comment-
// Zeilen (`:`) und Heartbeats werden übersprungen — wir wollen den nächsten
// echten Event-Frame.
func readSSEFrame(br *bufio.Reader, d time.Duration) (sseFrame, error) {
	frame := sseFrame{}
	deadline := time.Now().Add(d)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return frame, fmt.Errorf("readSSEFrame: timeout")
		}
		line, err := readLineWithTimeout(br, remaining)
		if err != nil {
			return frame, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// Frame-Ende — nur returnen wenn wir was Echtes haben
			if frame.event != "" || frame.data != "" || frame.id != "" {
				return frame, nil
			}
			continue // sonst weitersuchen (Leerzeilen zwischen Comments)
		}
		if strings.HasPrefix(line, ":") {
			// Comment / Heartbeat — überspringen
			continue
		}
		switch {
		case strings.HasPrefix(line, "id:"):
			frame.id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			frame.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			frame.data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
}

// waitFor pollt cond bis es true ist oder das Timeout greift.
func waitFor(cond func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
