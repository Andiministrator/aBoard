package api_test

// llm_stream_budget_vision_test.go — Bild-Tokens im Kontext-Budget (F-1089,
// Nebenbefund Karte #528/#538).
//
// Das Vor-dem-Turn-Budget-Gate (#300, ADR-0096 §2) zaehlte bisher nur Text —
// ein mit dem Turn gesendetes Bild ging nicht in die Rechnung ein. Die Tests
// zeigen am Gate vor dem Turn: derselbe Text wird ohne Bild durchgelassen,
// mit Bild abgelehnt — mit Conversation und (seit F-1100) auch ohne.
//
// Das Loop-Gate (F-872/F-1091) rechnet mit den GEMELDETEN Input-Tokens der
// gelaufenen Runde, in denen die Bilder schon stecken; die Pauschale braucht
// es dort nur, wenn der Anbieter nichts meldet.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

type visionBudgetEnv struct {
	srv      *httptest.Server
	db       *sql.DB
	convs    *storage.LLMConversationRepo
	provider *streamingMockProvider
	boardID  string
	cardID   string
	email    string
}

// newVisionBudgetEnv baut eine Karte mit genau einem Bild-Anhang (att-1) UND
// Conversation-Unterstuetzung — die Kombination, die weder newLLMStreamEnv
// (kein WithAttachments) noch newFetchStreamEnv (kein WithConversations)
// allein bietet.
func newVisionBudgetEnv(t *testing.T) *visionBudgetEnv {
	t.Helper()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	boards := storage.NewBoardRepo(db)
	users := storage.NewUserRepo(db)
	acl := storage.NewAclRepo(db)
	bootstrap := storage.NewBootstrapRepo(db)

	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, _ := secretcrypto.New(hex.EncodeToString(k))
	keys := storage.NewLLMKeyRepo(db, box)

	email := "alice@example.com"
	state := `{"version":"2","board":{"id":"b1","title":"T","sprints":[{"id":"s1","title":"S","cards":[` +
		`{"id":"c1","displayId":1,"title":"K","description":"","comments":[],"activity":[],"links":[],` +
		`"attachments":[{"id":"att-1","filename":"bild.png","mime":"image/png","size":3}]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "T", "2", []byte(state)); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?,?,?,?,0)`,
		"u1", storage.NormalizeEmail(email), "A", "2026-01-01"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := acl.Set(ctx, "b1", storage.NormalizeEmail(email), storage.RoleEditor); err != nil {
		t.Fatalf("seed acl: %v", err)
	}
	if err := keys.Set(ctx, "u1", "anthropic", "sk-test"); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	// testAnhangStore + nopSeeker kommen aus llm_stream_fetch_test.go
	// (gleiches Package api_test).
	anh := &testAnhangStore{dateien: map[string][]byte{"att-1": testPNG}}
	provider := &streamingMockProvider{
		name:         "anthropic",
		streamChunks: []string{"ok"},
		streamResult: llm.SendResult{InputTokens: 10, OutputTokens: 5, Model: "claude-opus-4-7", FinishReason: "end_turn"},
		inPM:         1, outPM: 1,
	}
	registry := llm.NewRegistry()
	registry.Register(provider)
	convs := storage.NewLLMConversationRepo(db)

	sendAPI := api.NewLLMSendAPI(boards, users, keys, registry, llm.NewLockManager(0)).
		WithConversations(convs).
		WithACL(acl).
		WithAttachments(anh)

	mux := http.NewServeMux()
	sendAPI.Register(mux)
	var h http.Handler = mux
	h = api.NewACLGuard(acl, users, bootstrap).Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); _ = db.Close() })

	return &visionBudgetEnv{srv: srv, db: db, convs: convs, provider: provider, boardID: "b1", cardID: "c1", email: email}
}

// seedConv legt eine Conversation mit genau einem assistant-Turn an, dessen
// Text die History-Schätzung steuert, gegen die das Vor-dem-Turn-Gate rechnet
// (seit F-1108 der Text der History, nicht mehr gemeldete input_tokens).
func (e *visionBudgetEnv) seedConv(t *testing.T, verlaufTokens int) string {
	t.Helper()
	ctx := context.Background()
	conv, err := e.convs.CreateConversation(ctx, storage.CreateConversationParams{
		ID: "conv-1", BoardID: e.boardID, CardID: e.cardID,
		Provider: "anthropic", Model: "claude-opus-4-7", CreatedBy: e.email,
	})
	if err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	if _, err := e.convs.AppendTurn(ctx, storage.AppendTurnParams{
		ID: "t1", ConversationID: conv.ID, Role: "assistant",
		Content:     strings.Repeat("x", verlaufTokens*4), // EstimateTokens: 4 Zeichen je Token
		InputTokens: ptrInt(1), OutputTokens: ptrInt(0),
	}); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	return conv.ID
}

func (e *visionBudgetEnv) send(t *testing.T, body map[string]any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	r, _ := http.NewRequest(http.MethodPost, e.srv.URL+streamPath(e.boardID, e.cardID), bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Remote-Email", e.email)
	r.Header.Set("Remote-User", e.email)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// Kern des Tests: die History-Schätzung liegt so, dass Text allein knapp UNTER der
// Schwelle bleibt (80 % von ContextLimit), Text + ein Bild aber drueber.
// margin = EstimatedImageTokensPerImage/2 — von der Konstante abgeleitet,
// damit der Test eine spaetere Aenderung an Limit/Anteil/Bild-Pauschale
// uebersteht, statt still gruen zu bleiben. Sie ist gross genug, dass die
// paar hundert Tokens des winzigen Test-Prompts (leere Beschreibung, kein
// Text) darin sicher Platz haben.
func visionBudgetVerlauf(t *testing.T) int {
	t.Helper()
	limit := llm.ContextLimit("anthropic", "claude-opus-4-7")
	if limit == 0 {
		t.Fatal("ContextLimit fuer claude-opus-4-7 liefert 0 — Testannahme verletzt")
	}
	schwelle := int(float64(limit) * api.ContextBudgetFraction)
	return schwelle - llm.EstimatedImageTokensPerImage/2
}

func TestLLMStream_MultiTurn_BildTokenGehtInsBudgetEin(t *testing.T) {
	env := newVisionBudgetEnv(t)
	verlauf := visionBudgetVerlauf(t)
	convID := env.seedConv(t, verlauf)

	resp := env.send(t, map[string]any{
		"provider": "anthropic", "model": "claude-opus-4-7",
		"conversationId": convID, "attachmentIds": []string{"att-1"},
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mit Bild: Status %d, erwartet 400 (context-budget)", resp.StatusCode)
	}
	var errBody struct{ Kind string }
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Kind != "context-budget" {
		t.Errorf("kind: %q, erwartet 'context-budget'", errBody.Kind)
	}
	if calls := env.provider.streamCalls.Load(); calls != 0 {
		t.Errorf("%d Provider-Aufrufe trotz Budget-Gate, erwartet 0", calls)
	}
}

// Gegenprobe zum Fix: dieselbe History, aber OHNE Bild — das Gate darf nicht
// zuschlagen. Ohne die F-1089-Ergaenzung waere dieser Test bereits vorher
// gruen gewesen; der Fix aendert an ihm nichts, er sichert nur, dass die
// Bild-Ergaenzung den bildlosen Fall nicht mit heraufzieht.
func TestLLMStream_MultiTurn_OhneBildBleibtUnterBudget(t *testing.T) {
	env := newVisionBudgetEnv(t)
	verlauf := visionBudgetVerlauf(t)
	convID := env.seedConv(t, verlauf)

	resp := env.send(t, map[string]any{
		"provider": "anthropic", "model": "claude-opus-4-7",
		"conversationId": convID,
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(resp.Body)
		t.Fatalf("ohne Bild: Status %d (%s), erwartet 200", resp.StatusCode, raw.String())
	}
	evs := parseSSEResponse(t, resp.Body)
	if len(ereignisse(evs, "error")) != 0 {
		t.Fatalf("unerwarteter Fehler ohne Bild: %+v", evs)
	}
	if calls := env.provider.streamCalls.Load(); calls != 1 {
		t.Errorf("%d Provider-Aufrufe, erwartet 1", calls)
	}
}

// einzelSendBoard — c1 mit einer Beschreibung von `beschreibungTokens`
// geschätzten Tokens und genau einem Bild-Anhang (att-1).
func einzelSendBoard(t *testing.T, beschreibungTokens int) []byte {
	t.Helper()
	state := map[string]any{
		"version": "2",
		"board": map[string]any{
			"id": "b1", "title": "T",
			"sprints": []any{map[string]any{
				"id": "s1", "title": "S",
				"cards": []any{map[string]any{
					"id": "c1", "displayId": 1, "title": "K",
					"description": strings.Repeat("x", beschreibungTokens*4),
					"comments":    []any{}, "activity": []any{}, "links": []any{},
					"attachments": []any{map[string]any{
						"id": "att-1", "filename": "bild.png", "mime": "image/png", "size": float64(len(testPNG)),
					}},
				}},
			}},
		},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// F-1100: Ein einzelner Send OHNE conversationId durchlief vorher kein
// Budget-Gate — das Gate vor dem Turn gab es nur mit Conversation, das
// Loop-Gate nur bei einer Werkzeug-Runde. Die Karte liegt knapp unter der
// Schwelle; mit einem Bild darüber. Der Anbieter darf nicht gerufen werden.
func TestLLMStream_EinzelSendMitBildDurchlaeuftDasBudgetGate(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{{text: "sollte nie kommen"}})
	env.ersetzeBoard(t, einzelSendBoard(t, visionBudgetVerlauf(t)))

	resp, body := env.sendeRoh(t, standardBody(map[string]any{"attachmentIds": []string{"att-1"}}))

	if resp.StatusCode != http.StatusBadRequest || body["kind"] != "context-budget" {
		t.Fatalf("Status %d, Body %+v — erwartet 400 context-budget", resp.StatusCode, body)
	}
	if n := env.provider.anzahlAufrufe(); n != 0 {
		t.Fatalf("%d Anbieter-Aufrufe trotz Budget-Gate, erwartet 0", n)
	}
}

// Gegenrichtung: dieselbe Karte ohne Bild bleibt unter der Schwelle und geht
// durch — das Gate ohne Conversation darf normale Sends nicht abwürgen.
func TestLLMStream_EinzelSendOhneBildBleibtUnterBudget(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{{text: "Antwort."}})
	env.ersetzeBoard(t, einzelSendBoard(t, visionBudgetVerlauf(t)))

	evs := env.sende(t, standardBody(nil))

	if f := ereignisse(evs, "error"); len(f) != 0 {
		t.Fatalf("unerwarteter Fehler: %+v", f)
	}
	if n := env.provider.anzahlAufrufe(); n != 1 {
		t.Fatalf("%d Anbieter-Aufrufe, erwartet 1", n)
	}
}
