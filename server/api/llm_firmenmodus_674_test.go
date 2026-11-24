package api_test

// llm_firmenmodus_674_test.go — Karte #674 (ADR-0133 Nachtrag 2, A5/A6):
// Im Firmenmodus sind KI-Verläufe nur für ihren Ersteller sichtbar, und
// Chat-Anfragen sind je Person begrenzt. Ohne Firmenmodus bleibt beides wie
// vorher (Gegenproben in jedem Test).

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

type env674 struct {
	srv     *httptest.Server
	db      *sql.DB
	convs   *storage.LLMConversationRepo
	locks   *llm.LockManager // K2-9: Sperre von aussen halten (423-Fall)
	mu      sync.Mutex
	aufrufe int
}

func (e *env674) anzahlAufrufe() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.aufrufe
}

// newEnv674 baut Send-, Stream- und Verlaufs-API mit fm (nil = kein
// Firmenmodus). grenze > 0 setzt WithAnfragenObergrenze. Personen: a und b
// editor (mit eigenem openai-Schlüssel für den Fall ohne Firmenmodus),
// c viewer.
func newEnv674(t *testing.T, fm *llm.Firmenmodus, grenze int) *env674 {
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
	convs := storage.NewLLMConversationRepo(db)
	k := make([]byte, secretcrypto.KeySize)
	_, _ = rand.Read(k)
	box, _ := secretcrypto.New(hex.EncodeToString(k))
	keys := storage.NewLLMKeyRepo(db, box)

	state := `{"version":"2","board":{"id":"b1","title":"T","sprints":[{"id":"s1","title":"S","cards":[` +
		`{"id":"c1","displayId":1,"title":"Karte","description":"Hi","comments":[],"activity":[],"links":[]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "T", "2", []byte(state)); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	for _, p := range []struct{ id, email, rolle string }{
		{"u-a", "a@x.de", storage.RoleEditor},
		{"u-b", "b@x.de", storage.RoleEditor},
		{"u-c", "c@x.de", storage.RoleViewer},
	} {
		if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', 0)`, p.id, p.email); err != nil {
			t.Fatal(err)
		}
		if err := acl.Set(ctx, "b1", p.email, p.rolle); err != nil {
			t.Fatal(err)
		}
		if err := keys.Set(ctx, p.id, "openai", privatSchluessel); err != nil {
			t.Fatal(err)
		}
	}

	env := &env674{db: db, convs: convs, locks: llm.NewLockManager(0)}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		env.mu.Lock()
		env.aufrufe++
		env.mu.Unlock()
		if s, _ := body["stream"].(bool); s {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Antwort\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Antwort"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	t.Cleanup(stub.Close)
	registry := llm.NewRegistry()
	registry.Register(&llm.OpenAIProvider{Client: stub.Client(), UA: "test/1",
		Endpoint: stub.URL + "/models", SendEndpoint: stub.URL + "/chat/completions"})

	sendAPI := api.NewLLMSendAPI(boards, users, keys, registry, env.locks).
		WithConversations(convs).
		WithACL(acl).
		WithFirmenmodus(fm, llm.PIIAus)
	if grenze > 0 {
		sendAPI.WithAnfragenObergrenze(grenze)
	}
	convAPI := api.NewLLMConversationsAPI(convs, users).WithFirmenmodus(fm)

	mux := http.NewServeMux()
	sendAPI.Register(mux)
	convAPI.Register(mux)
	guard := api.NewACLGuard(acl, users, bootstrap)
	var h http.Handler = mux
	h = guard.Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	env.srv = httptest.NewServer(h)
	t.Cleanup(func() {
		env.srv.Close()
		_ = db.Close()
	})
	return env
}

// rufe schickt eine Anfrage als email; liefert Status, Körper, Kopfzeilen.
func (e *env674) rufe(t *testing.T, email, method, path string, body any) (int, string, http.Header) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	r.Header.Set("Remote-Email", email)
	r.Header.Set("Remote-User", email)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

// verlauf legt einen Verlauf von email mit einem Turn-Paar an.
func (e *env674) verlauf(t *testing.T, id, email string) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.convs.CreateConversation(ctx, storage.CreateConversationParams{
		ID: id, BoardID: "b1", CardID: "c1", Provider: "openai", Model: firmenModell, CreatedBy: email,
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.convs.AppendTurnPair(ctx,
		storage.AppendTurnParams{ID: id + "-u", ConversationID: id, TS: "2026-10-04T10:00:00.000000001Z", Role: "user", Content: "Kundendaten von " + email},
		storage.AppendTurnParams{ID: id + "-a", ConversationID: id, TS: "2026-10-04T10:00:00.000000002Z", Role: "assistant", Content: "Antwort an " + email},
	); err != nil {
		t.Fatal(err)
	}
}

func listeIDs(t *testing.T, body string) []string {
	t.Helper()
	var list []storage.LLMConversation
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("Liste unlesbar: %v — %s", err, body)
	}
	ids := make([]string, 0, len(list))
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	return ids
}

const (
	pfad674Liste  = "/boards/b1/cards/c1/conversations"
	pfad674Stream = "/boards/b1/cards/c1/llm-send-stream"
	pfad674Send   = "/boards/b1/cards/c1/llm-send"
)

// A5: Liste, Turns, Archivieren, Löschen und Fortsetzen per Stream — ein
// fremder Verlauf ist im Firmenmodus auf jedem Weg unsichtbar (404), ohne
// Firmenmodus gilt ADR-0096 (lesen viewer+, schreiben nur Ersteller: 403).
func TestFirmenmodus674_VerlaeufeNurFuerErsteller(t *testing.T) {
	for _, fall := range []struct {
		name    string
		firma   bool
		fremd   int // Status für Turns eines fremden Verlaufs
		schreib int // Status für Archivieren/Löschen/Fortsetzen eines fremden
	}{
		{"firmenmodus", true, http.StatusNotFound, http.StatusNotFound},
		{"ohne-firmenmodus", false, http.StatusOK, http.StatusForbidden},
	} {
		t.Run(fall.name, func(t *testing.T) {
			var fm *llm.Firmenmodus
			if fall.firma {
				fm = neuerFirmenmodus(t)
			}
			env := newEnv674(t, fm, 0)
			env.verlauf(t, "conv-a", "a@x.de")
			env.verlauf(t, "conv-b", "b@x.de")

			for _, archiv := range []string{"", "?includeArchived=true"} {
				status, body, _ := env.rufe(t, "b@x.de", http.MethodGet, pfad674Liste+archiv, nil)
				if status != http.StatusOK {
					t.Fatalf("Liste%s als b: %d %s", archiv, status, body)
				}
				ids := strings.Join(listeIDs(t, body), ",")
				hatFremd := strings.Contains(ids, "conv-a")
				if fall.firma && (hatFremd || !strings.Contains(ids, "conv-b")) {
					t.Errorf("Liste%s als b im Firmenmodus = [%s], erwartet nur conv-b", archiv, ids)
				}
				if !fall.firma && (!hatFremd || !strings.Contains(ids, "conv-b")) {
					t.Errorf("Liste%s als b ohne Firmenmodus = [%s], erwartet beide", archiv, ids)
				}
			}
			// Betrachter c hat keinen eigenen Verlauf.
			status, body, _ := env.rufe(t, "c@x.de", http.MethodGet, pfad674Liste, nil)
			if n := len(listeIDs(t, body)); status != http.StatusOK || (fall.firma && n != 0) || (!fall.firma && n != 2) {
				t.Errorf("Liste als Betrachter c: %d, %d Einträge", status, n)
			}

			// Turns: fremd vs. eigen.
			status, body, _ = env.rufe(t, "b@x.de", http.MethodGet, pfad674Liste+"/conv-a/turns", nil)
			if status != fall.fremd {
				t.Errorf("Turns von conv-a als b: %d, erwartet %d — %s", status, fall.fremd, body)
			}
			if fall.firma && strings.Contains(body, "Kundendaten") {
				t.Errorf("fremder Turn-Inhalt in der Antwort: %s", body)
			}
			status, body, _ = env.rufe(t, "a@x.de", http.MethodGet, pfad674Liste+"/conv-a/turns", nil)
			if status != http.StatusOK || !strings.Contains(body, "Kundendaten von a@x.de") {
				t.Errorf("eigene Turns als a: %d %s", status, body)
			}

			// Fortsetzen eines fremden Verlaufs per Stream — der Anbieter darf
			// nicht gefragt werden.
			vorher := env.anzahlAufrufe()
			status, body, _ = env.rufe(t, "b@x.de", http.MethodPost, pfad674Stream, map[string]any{
				"provider": "openai", "model": firmenModell, "conversationId": "conv-a"})
			if status != fall.schreib {
				t.Errorf("Stream mit fremder conversationId: %d, erwartet %d — %s", status, fall.schreib, body)
			}
			if n := env.anzahlAufrufe() - vorher; n != 0 {
				t.Errorf("Anbieter %d× gefragt für fremden Verlauf", n)
			}

			// Archivieren und Löschen fremd → abgewiesen, Verlauf bleibt.
			status, _, _ = env.rufe(t, "b@x.de", http.MethodPost, pfad674Liste+"/conv-a/archive", nil)
			if status != fall.schreib {
				t.Errorf("Archivieren fremd: %d, erwartet %d", status, fall.schreib)
			}
			status, _, _ = env.rufe(t, "b@x.de", http.MethodDelete, pfad674Liste+"/conv-a", nil)
			if status != fall.schreib {
				t.Errorf("Löschen fremd: %d, erwartet %d", status, fall.schreib)
			}
			c, err := env.convs.GetConversation(context.Background(), "conv-a")
			if err != nil || c.ArchivedAt != nil {
				t.Errorf("conv-a nach fremden Versuchen verändert: err=%v archiviert=%v", err, c.ArchivedAt)
			}

			// Der Ersteller darf weiterhin alles — auch fortsetzen.
			status, body, _ = env.rufe(t, "a@x.de", http.MethodPost, pfad674Stream, map[string]any{
				"provider": "openai", "model": firmenModell, "conversationId": "conv-a"})
			if status != http.StatusOK || strings.Contains(body, "event: error") {
				t.Errorf("eigenen Verlauf fortsetzen: %d %s", status, body)
			}
			status, _, _ = env.rufe(t, "a@x.de", http.MethodPost, pfad674Liste+"/conv-a/archive", nil)
			if status != http.StatusNoContent {
				t.Errorf("eigenen Verlauf archivieren: %d", status)
			}
			status, _, _ = env.rufe(t, "a@x.de", http.MethodDelete, pfad674Liste+"/conv-a", nil)
			if status != http.StatusNoContent {
				t.Errorf("eigenen Verlauf löschen: %d", status)
			}
		})
	}
}

// A5 mit LIMIT 10: Der Filter muss in der Abfrage stehen. Hat b zehn
// jüngere Verläufe an derselben Karte, darf a seinen älteren trotzdem sehen.
func TestFirmenmodus674_EigenerVerlaufTrotzZehnFremderJuengerer(t *testing.T) {
	env := newEnv674(t, neuerFirmenmodus(t), 0)
	env.verlauf(t, "conv-a-alt", "a@x.de")
	if _, err := env.db.Exec(`UPDATE llm_conversations SET created_at = '2020-01-01T00:00:00Z' WHERE id = 'conv-a-alt'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		env.verlauf(t, "conv-b-"+strconv.Itoa(i), "b@x.de")
	}
	status, body, _ := env.rufe(t, "a@x.de", http.MethodGet, pfad674Liste, nil)
	if ids := listeIDs(t, body); status != http.StatusOK || len(ids) != 1 || ids[0] != "conv-a-alt" {
		t.Errorf("Liste als a: %d %v, erwartet genau [conv-a-alt]", status, ids)
	}
}

// A6: Obergrenze je Person, für Send und Stream. Die dritte Anfrage
// innerhalb der Stunde bekommt 429 mit Retry-After und Zahlen für die
// Oberfläche; der Anbieter wird nicht gefragt; eine andere Person ist nicht
// betroffen. Ohne Firmenmodus gibt es keine Obergrenze.
func TestFirmenmodus674_ObergrenzeJePerson(t *testing.T) {
	for _, weg := range []struct{ name, pfad string }{{"stream", pfad674Stream}, {"send", pfad674Send}} {
		t.Run(weg.name, func(t *testing.T) {
			env := newEnv674(t, neuerFirmenmodus(t), 2)
			req := map[string]any{"provider": "openai", "model": firmenModell}
			for i := 1; i <= 2; i++ {
				if status, body, _ := env.rufe(t, "a@x.de", http.MethodPost, weg.pfad, req); status != http.StatusOK || strings.Contains(body, "event: error") {
					t.Fatalf("Anfrage %d: %d %s", i, status, body)
				}
			}
			status, body, kopf := env.rufe(t, "a@x.de", http.MethodPost, weg.pfad, req)
			if status != http.StatusTooManyRequests {
				t.Fatalf("dritte Anfrage: %d %s, erwartet 429", status, body)
			}
			var antw struct {
				Kind           string `json:"kind"`
				Grenze         int    `json:"grenze"`
				WartenSekunden int    `json:"wartenSekunden"`
			}
			_ = json.Unmarshal([]byte(body), &antw)
			if antw.Kind != api.KindAnfragenObergrenze || antw.Grenze != 2 || antw.WartenSekunden < 1 || antw.WartenSekunden > 3600 {
				t.Errorf("429-Körper = %+v (%s)", antw, body)
			}
			if ra, err := strconv.Atoi(kopf.Get("Retry-After")); err != nil || ra < 1 || ra > 3600 {
				t.Errorf("Retry-After = %q", kopf.Get("Retry-After"))
			}
			if n := env.anzahlAufrufe(); n != 2 {
				t.Errorf("Anbieter %d× gefragt, erwartet 2", n)
			}
			// Andere Person: eigenes Kontingent.
			if status, body, _ := env.rufe(t, "b@x.de", http.MethodPost, weg.pfad, req); status != http.StatusOK || strings.Contains(body, "event: error") {
				t.Errorf("b nach a's Grenze: %d %s", status, body)
			}
		})
	}
	// Gegenprobe ohne Firmenmodus: Grenze 1 gesetzt, wirkt aber nicht.
	env := newEnv674(t, nil, 1)
	for i := 1; i <= 3; i++ {
		if status, body, _ := env.rufe(t, "a@x.de", http.MethodPost, pfad674Send, map[string]any{"provider": "openai", "model": "m"}); status != http.StatusOK {
			t.Fatalf("ohne Firmenmodus Anfrage %d: %d %s", i, status, body)
		}
	}
}

// Abgewiesene Anfragen (hier: Modell nicht freigegeben) zählen nicht —
// gezählt wird erst direkt vor dem Anbieter.
func TestFirmenmodus674_AbgewieseneZaehlenNicht(t *testing.T) {
	env := newEnv674(t, neuerFirmenmodus(t), 1)
	for i := 0; i < 3; i++ {
		if status, _, _ := env.rufe(t, "a@x.de", http.MethodPost, pfad674Send, map[string]any{"provider": "openai", "model": "gpt-4o"}); status != http.StatusBadRequest {
			t.Fatalf("nicht freigegebenes Modell: %d", status)
		}
	}
	if status, body, _ := env.rufe(t, "a@x.de", http.MethodPost, pfad674Send, map[string]any{"provider": "openai", "model": firmenModell}); status != http.StatusOK {
		t.Errorf("erste echte Anfrage nach Abweisungen: %d %s", status, body)
	}
}

func TestParseLLMAnfragenProStunde(t *testing.T) {
	for _, f := range []struct {
		roh  string
		will int
		ok   bool
	}{
		{"", api.DefaultLLMAnfragenProStunde, true},
		{"  ", api.DefaultLLMAnfragenProStunde, true},
		{"120", 120, true},
		{" 5 ", 5, true},
		{"1", 1, true},
		{"100000", 100000, true},
		{"0", 0, false},
		{"-3", 0, false},
		{"100001", 0, false},
		{"sechzig", 0, false},
		{"60/h", 0, false},
	} {
		n, err := api.ParseLLMAnfragenProStunde(f.roh)
		if (err == nil) != f.ok || (f.ok && n != f.will) {
			t.Errorf("Parse(%q) = %d, %v; erwartet %d, ok=%v", f.roh, n, err, f.will, f.ok)
		}
		if err != nil && !strings.Contains(err.Error(), api.LLMAnfragenEnvVar) {
			t.Errorf("Fehlermeldung nennt die Variable nicht: %v", err)
		}
	}
}

// kartenInhalt liest Kommentare und Aktivität der Karte c1 aus dem Board-Stand.
func (e *env674) kartenInhalt(t *testing.T) (kommentare, aktivitaet []any) {
	t.Helper()
	b, err := storage.NewBoardRepo(e.db).Get(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Board struct {
			Sprints []struct {
				Cards []map[string]any `json:"cards"`
			} `json:"sprints"`
		} `json:"board"`
	}
	if err := json.Unmarshal(b.StateJSON, &state); err != nil {
		t.Fatal(err)
	}
	for _, sp := range state.Board.Sprints {
		for _, c := range sp.Cards {
			if c["id"] == "c1" {
				k, _ := c["comments"].([]any)
				a, _ := c["activity"].([]any)
				return k, a
			}
		}
	}
	t.Fatal("Karte c1 nicht gefunden")
	return nil, nil
}

// A5 vollständig (Nacharbeit #674): Im Firmenmodus schreiben Send und Stream
// die Antwort NICHT als Kartenkommentar und legen keinen Aktivitätseintrag an.
// Die Antwort steht im Stream bzw. in der Antwort und — mit conversationId —
// im privaten Verlauf. Ohne Firmenmodus unverändert (ADR-0095).
func TestFirmenmodus674_AntwortNichtAnDerKarte(t *testing.T) {
	for _, fall := range []struct {
		name  string
		firma bool
	}{{"firmenmodus", true}, {"ohne-firmenmodus", false}} {
		for _, weg := range []struct{ name, pfad string }{{"stream", pfad674Stream}, {"send", pfad674Send}} {
			t.Run(fall.name+"/"+weg.name, func(t *testing.T) {
				var fm *llm.Firmenmodus
				if fall.firma {
					fm = neuerFirmenmodus(t)
				}
				env := newEnv674(t, fm, 0)
				status, body, _ := env.rufe(t, "a@x.de", http.MethodPost, weg.pfad, map[string]any{"provider": "openai", "model": firmenModell})
				if status != http.StatusOK || strings.Contains(body, "event: error") || !strings.Contains(body, "Antwort") {
					t.Fatalf("Send: %d %s", status, body)
				}
				kommentare, aktivitaet := env.kartenInhalt(t)
				hatCommentID := strings.Contains(body, `"commentId"`)
				if fall.firma {
					if len(kommentare) != 0 || len(aktivitaet) != 0 || hatCommentID {
						t.Errorf("Firmenmodus: %d Kommentare, %d Aktivitäten, commentId in Antwort=%v — erwartet nichts an der Karte",
							len(kommentare), len(aktivitaet), hatCommentID)
					}
				} else if len(kommentare) != 1 || len(aktivitaet) != 1 || !hatCommentID {
					t.Errorf("ohne Firmenmodus: %d Kommentare, %d Aktivitäten, commentId=%v — erwartet je einen (ADR-0095)",
						len(kommentare), len(aktivitaet), hatCommentID)
				}
			})
		}
	}
}

// Im Firmenmodus mit Verlauf: Die Antwort landet in den Turns (und nur dort).
func TestFirmenmodus674_AntwortImPrivatenVerlauf(t *testing.T) {
	env := newEnv674(t, neuerFirmenmodus(t), 0)
	if _, err := env.convs.CreateConversation(context.Background(), storage.CreateConversationParams{
		ID: "conv-a", BoardID: "b1", CardID: "c1", Provider: "openai", Model: firmenModell, CreatedBy: "a@x.de",
	}); err != nil {
		t.Fatal(err)
	}
	status, body, _ := env.rufe(t, "a@x.de", http.MethodPost, pfad674Stream, map[string]any{
		"provider": "openai", "model": firmenModell, "conversationId": "conv-a"})
	if status != http.StatusOK || !strings.Contains(body, "assistantTurnId") {
		t.Fatalf("Stream im Verlauf: %d %s", status, body)
	}
	turns, err := env.convs.LoadTurns(context.Background(), "conv-a")
	if err != nil || len(turns) != 2 || turns[1].Content != "Antwort" {
		t.Errorf("Turns = %+v (err %v), erwartet Frage + Antwort", turns, err)
	}
	if k, a := env.kartenInhalt(t); len(k) != 0 || len(a) != 0 {
		t.Errorf("Karte trägt %d Kommentare, %d Aktivitäten", len(k), len(a))
	}
}
