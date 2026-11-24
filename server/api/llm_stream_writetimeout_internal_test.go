package api

// llm_stream_writetimeout_internal_test.go — F-1143 (G2, Sprint 71): belegt
// und verifiziert, dass der LLM-Stream (/boards/{b}/cards/{c}/llm-send-stream)
// nicht mehr an der Server.WriteTimeout-Grenze (main.go: 30s) hängen bleibt.
//
// F-1127 (#544, Sprint 70) hatte den rollierenden Schreibfrist-Mechanismus
// (rollendeFrist in events.go) auf llm_stream.go ausgeweitet — sse.frist wird
// vor JEDEM SSE-Write per verlaengern() neu gesetzt (siehe
// sseWriter.writeEvent in llm_stream.go). Ein Test dafür fehlte bisher:
// events_writetimeout_internal_test.go deckt nur den /events-Endpoint ab,
// nicht den davon unabhängigen zweiten Aufrufer desselben Mechanismus.
//
// Package `api` statt `api_test` (wie llm_stream_test.go), weil wir
// srv.Config.WriteTimeout direkt setzen müssen (httptest.NewUnstartedServer)
// — von aussen (package api_test) ist das genauso möglich, aber der
// gemeinsam genutzte streamSchreibfrist-Mechanismus lebt ohnehin in package
// api; eigener, minimaler Testaufbau statt des externen newLLMStreamEnv-
// Helpers (package api_test, hier nicht importierbar).
//
// Mechanik wie in events_writetimeout_internal_test.go: main.go's 30s
// WriteTimeout wird hier auf 300ms verkürzt; ein Mock-Provider liefert 5
// Text-Chunks über insgesamt ~750ms verteilt — deutlich mehr als das
// halbierte WriteTimeout. Ohne den F-1127-Fix bricht Go's http.Server die
// Connection spätestens bei ~300ms ab.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// langsamerStreamProvider liefert seine Chunks mit fester Verzögerung
// zwischen jedem einzelnen — implementiert Provider UND StreamingProvider
// minimal (nur was handleSendStream tatsächlich aufruft).
type langsamerStreamProvider struct {
	name   string
	chunks []string
	delay  time.Duration
}

func (p *langsamerStreamProvider) Name() string                          { return p.name }
func (p *langsamerStreamProvider) TestKey(context.Context, string) error { return nil }
func (p *langsamerStreamProvider) Pricing(string) (float64, float64)     { return 0, 0 }
func (p *langsamerStreamProvider) Send(_ context.Context, _ string, _ llm.SendRequest) (llm.SendResult, error) {
	return llm.SendResult{}, nil
}
func (p *langsamerStreamProvider) SendStream(ctx context.Context, _ string, _ llm.SendRequest, onChunk func(llm.StreamChunk) error) (llm.SendResult, error) {
	var sb strings.Builder
	for _, c := range p.chunks {
		if err := ctx.Err(); err != nil {
			return llm.SendResult{}, err
		}
		time.Sleep(p.delay)
		if err := onChunk(llm.StreamChunk{Text: c}); err != nil {
			return llm.SendResult{}, err
		}
		sb.WriteString(c)
	}
	return llm.SendResult{Text: sb.String(), FinishReason: "end_turn"}, nil
}

func TestLLMStream_SurvivesBeyondServerWriteTimeout(t *testing.T) {
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()

	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	boards := storage.NewBoardRepo(db)
	users := storage.NewUserRepo(db)

	state := `{"version":"2","board":{"id":"b1","title":"Test","sprints":[{"id":"s1","title":"S","cards":[{"id":"c1","displayId":1,"title":"Karte","description":"Hi","comments":[],"activity":[],"links":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "Test", "2", []byte(state)); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, ?, ?, 0)`,
		"u1", storage.NormalizeEmail("andi@example.com"), "N", "2026-01-01"); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, err := secretcrypto.New(hex.EncodeToString(k))
	if err != nil {
		t.Fatalf("secretcrypto.New: %v", err)
	}
	keys := storage.NewLLMKeyRepo(db, box)
	if err := keys.Set(ctx, "u1", "anthropic", "sk-test"); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	registry := llm.NewRegistry()
	mp := &langsamerStreamProvider{
		name:   "anthropic",
		chunks: []string{"a", "b", "c", "d", "e"},
		delay:  150 * time.Millisecond, // 5 × 150ms = 750ms Gesamtlaufzeit
	}
	registry.Register(mp)

	locks := llm.NewLockManager(0)
	sendAPI := NewLLMSendAPI(boards, users, keys, registry, locks)
	mux := http.NewServeMux()
	sendAPI.Register(mux)

	var handler http.Handler = mux
	handler = WithForwardAuthIfEnabled(handler)

	srv := httptest.NewUnstartedServer(handler)
	// Steht stellvertretend für main.go's WriteTimeout=30s (server/main.go),
	// auf 300ms verkürzt — deutlich unter den 750ms Gesamtlaufzeit des Mock-
	// Providers. Siehe events_writetimeout_internal_test.go für dieselbe
	// Übertragbarkeits-Begründung.
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	body := `{"provider":"anthropic","model":"claude-x","includeFields":["description"]}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/boards/b1/cards/c1/llm-send-stream", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Remote-Email", "andi@example.com")
	req.Header.Set("Remote-User", "andi@example.com")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST llm-send-stream: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status = %d, erwartet 200", resp.StatusCode)
	}

	br := bufio.NewReader(resp.Body)
	tokenEvents := 0
	sawDone := false
	// Über die WriteTimeout-Schwelle (300ms) hinaus lesen — insgesamt bis zu
	// 2s, mehr als das Doppelte der 750ms Gesamtlaufzeit. Ohne den F-1127-Fix
	// bricht die Verbindung spätestens bei ~300ms ab (Go schliesst sie
	// serverseitig, der Client-Read liefert dann einen Fehler statt
	// weiterer Zeilen).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !sawDone {
		line, err := readLineDeadline(br, 800*time.Millisecond)
		if err != nil {
			t.Fatalf("Stream brach nach %d token-Events ab (WriteTimeout=%v, sawDone=%v): %v",
				tokenEvents, srv.Config.WriteTimeout, sawDone, err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(trimmed, "event: token"):
			tokenEvents++
		case strings.HasPrefix(trimmed, "event: done"):
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("kein event:done innerhalb von 2s gelesen (tokenEvents=%d) — Stream wahrscheinlich hängen geblieben statt korrekt abzuschliessen", tokenEvents)
	}
	if tokenEvents != len(mp.chunks) {
		t.Errorf("tokenEvents = %d, erwartet %d — Stream unvollständig angekommen trotz event:done", tokenEvents, len(mp.chunks))
	}
}
