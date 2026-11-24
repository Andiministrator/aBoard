package api_test

// llm_nacharbeit_k2_test.go — Nacharbeit zum Kritiker-Lauf K2, Sprint 82
// (Karten #662, #664, #674; ADR-0133 §2 und Nachtrag 2):
//
//   - K2-1: Der Stream-Pfad frischt die Kartensperre über die ganze Laufzeit
//     auf (bis dahin nur für llm-send getestet).
//   - K2-3: Kartensperre und llm-pending gelten Board + Karte, nicht der
//     Karten-ID allein (Import behält Karten-IDs).
//   - K2-4: Send und Stream melden im Ergebnis, ob der Lauf im Firmenmodus lief.
//   - K2-9: Eine gesperrte Karte (423) zählt nicht gegen die Obergrenze.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// K2-1: wie TestLLMSend_F1538_SperreHaeltUeberTTL, aber für llm-send-stream.
// TTL 60 ms (echte Uhr), der Anbieter schweigt 250 ms vor dem ersten Wort
// (> 4 × TTL). Währenddessen versucht ein zweiter Lauf, die Sperre zu
// bekommen — er muss scheitern.
func TestLLMStream_K21_SperreHaeltUeberTTL(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)

	locks := llm.NewLockManager(60 * time.Millisecond)
	registry := llm.NewRegistry()
	registry.Register(env.provider)
	env.provider.vorPause = 250 * time.Millisecond
	mux := http.NewServeMux()
	api.NewLLMSendAPI(env.boards, env.users, env.keys, registry, locks).
		WithConversations(env.convs).Register(mux)
	var h http.Handler = mux
	h = api.NewACLGuard(env.acl, env.users, storage.NewBootstrapRepo(env.db)).Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	env.srv = srv

	var zweiterBekamSperre atomic.Bool
	var geprueft atomic.Bool
	go func() {
		time.Sleep(200 * time.Millisecond) // mitten in der Denkphase, > 3 × TTL
		_, ok, _ := locks.Acquire(llm.SperrSchluessel(env.boardID, env.cardID), "u-b", "anthropic", "claude-opus-4-7")
		zweiterBekamSperre.Store(ok)
		geprueft.Store(true)
	}()
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "event: done") {
		t.Fatalf("Stream: Status %d, Körper %s", resp.StatusCode, raw)
	}
	if !geprueft.Load() {
		t.Fatal("Prüfung lief nicht während des Streams — Zeitfenster zu knapp")
	}
	if zweiterBekamSperre.Load() {
		t.Fatal("zweiter Lauf bekam die Sperre, während der Stream noch lief")
	}
	if _, ok := locks.Holder(llm.SperrSchluessel(env.boardID, env.cardID)); ok {
		t.Error("Sperre nach Ende des Streams nicht freigegeben")
	}
}

// zweitesBoard legt ein Board mit derselben Karten-ID c1 an (Import-Fall) und
// gibt email darauf die Rolle rolle.
func (e *llmStreamEnv) zweitesBoard(t *testing.T, boardID, cardID, email, rolle string) {
	t.Helper()
	state := `{"version":"2","board":{"id":"` + boardID + `","title":"Kopie","sprints":[{"id":"s1","title":"S","cards":[` +
		`{"id":"` + cardID + `","displayId":42,"title":"Karte","description":"Hi","comments":[],"activity":[],"links":[]}]}]}}`
	if _, err := e.boards.Insert(context.Background(), boardID, "Kopie", "2", []byte(state)); err != nil {
		t.Fatalf("seed board %s: %v", boardID, err)
	}
	if err := e.acl.Set(context.Background(), boardID, email, rolle); err != nil {
		t.Fatalf("seed acl: %v", err)
	}
}

// K2-3: Ein Lauf auf b1/c1 sperrt NICHT die gleichnamige Karte c1 im Board
// b2 (Import behält Karten-IDs). Und llm-pending in b2 sieht die Sperre in b1
// nicht.
func TestLLMSperre_K23_BoarduebergreifendGetrennt(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	env.zweitesBoard(t, "b2", "c1", "a@x.de", storage.RoleEditor)

	if _, ok, _ := env.locks.Acquire(llm.SperrSchluessel("b1", "c1"), "u-other", "openai", "gpt-4o"); !ok {
		t.Fatal("Sperre auf b1/c1 nicht bekommen")
	}
	resp := env.do(t, http.MethodPost, streamPath("b2", "c1"), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "event: done") {
		t.Fatalf("Stream auf b2/c1 trotz Sperre nur auf b1/c1: Status %d, %s", resp.StatusCode, raw)
	}
	// Gegenrichtung: Auf b1/c1 greift die Sperre weiter.
	resp = env.do(t, http.MethodPost, streamPath("b1", "c1"), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusLocked {
		t.Fatalf("Stream auf b1/c1: Status %d, erwartet 423", resp.StatusCode)
	}
	// llm-pending in b2 meldet keinen Halter.
	resp = env.do(t, http.MethodGet, "/boards/b2/cards/c1/llm-pending", "a@x.de", nil)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"holder":null`) {
		t.Errorf("llm-pending b2/c1: %d %s — erwartet holder null", resp.StatusCode, raw)
	}
}

// K2-3: llm-pending prüft, ob die Karte im Board des Pfads liegt. Ein Leser
// von b3 (ohne Karte c1) erfährt über /boards/b3/cards/c1/llm-pending nichts
// über eine Anfrage an c1 in b1.
func TestLLMPending_K23_KarteNichtImBoard404(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.seedUserWithKey(t, "u-c", "c@x.de", "")
	env.grant(t, "a@x.de", storage.RoleEditor)
	env.zweitesBoard(t, "b3", "c-anders", "c@x.de", storage.RoleViewer)

	if _, ok, _ := env.locks.Acquire(llm.SperrSchluessel("b1", "c1"), "u-a", "anthropic", "claude-opus-4-7"); !ok {
		t.Fatal("Sperre auf b1/c1 nicht bekommen")
	}
	resp := env.do(t, http.MethodGet, "/boards/b3/cards/c1/llm-pending", "c@x.de", nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("llm-pending mit fremder Karte: Status %d (%s), erwartet 404", resp.StatusCode, raw)
	}
	for _, leck := range []string{"a@x.de", "claude-opus-4-7", "u-a"} {
		if strings.Contains(string(raw), leck) {
			t.Errorf("Antwort nennt %q: %s", leck, raw)
		}
	}
	// Gegenprobe: Im eigenen Board sieht der Halter-Abruf die Sperre.
	resp = env.do(t, http.MethodGet, "/boards/b1/cards/c1/llm-pending", "a@x.de", nil)
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "claude-opus-4-7") {
		t.Errorf("llm-pending b1/c1: %d %s — erwartet den Halter", resp.StatusCode, raw)
	}
}

// K2-4: Send und Stream sagen im Ergebnis, ob der Lauf im Firmenmodus lief —
// die Oberfläche richtet Protokoll und Knöpfe danach, nicht nach ihrem
// Schlüssel-Cache.
func TestLLM_K24_FirmenmodusImErgebnis(t *testing.T) {
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
				if status != http.StatusOK || strings.Contains(body, "event: error") {
					t.Fatalf("Send: %d %s", status, body)
				}
				json1 := body
				if weg.name == "stream" {
					i := strings.Index(body, "event: done\ndata: ")
					if i < 0 {
						t.Fatalf("kein done-Ereignis: %s", body)
					}
					json1 = strings.SplitN(body[i+len("event: done\ndata: "):], "\n", 2)[0]
				}
				var erg map[string]any
				if err := json.Unmarshal([]byte(json1), &erg); err != nil {
					t.Fatalf("Ergebnis kein JSON: %v (%s)", err, json1)
				}
				if v, ok := erg["firmenmodus"].(bool); !ok || v != fall.firma {
					t.Errorf("firmenmodus = %#v, erwartet %v", erg["firmenmodus"], fall.firma)
				}
			})
		}
	}
}

// K2-9: Eine gesperrte Karte (423) zählt nicht gegen die Obergrenze — die
// Sperre wird vor der Zählung geprüft. Grenze 1: drei abgewiesene Versuche,
// danach geht die erste echte Anfrage durch.
func TestFirmenmodus674_K29_GesperrteKarteZaehltNicht(t *testing.T) {
	for _, weg := range []struct{ name, pfad string }{{"stream", pfad674Stream}, {"send", pfad674Send}} {
		t.Run(weg.name, func(t *testing.T) {
			env := newEnv674(t, neuerFirmenmodus(t), 1)
			sperre := llm.SperrSchluessel("b1", "c1")
			lauf, ok, _ := env.locks.Acquire(sperre, "u-b", "openai", firmenModell)
			if !ok {
				t.Fatal("Sperre nicht bekommen")
			}
			req := map[string]any{"provider": "openai", "model": firmenModell}
			for i := 0; i < 3; i++ {
				if status, body, _ := env.rufe(t, "a@x.de", http.MethodPost, weg.pfad, req); status != http.StatusLocked {
					t.Fatalf("Versuch %d auf gesperrte Karte: %d %s, erwartet 423", i+1, status, body)
				}
			}
			env.locks.Release(sperre, lauf)
			if status, body, _ := env.rufe(t, "a@x.de", http.MethodPost, weg.pfad, req); status != http.StatusOK || strings.Contains(body, "event: error") {
				t.Errorf("erste echte Anfrage nach drei 423: %d %s — 423 hat gezählt", status, body)
			}
			if n := env.anzahlAufrufe(); n != 1 {
				t.Errorf("Anbieter %d× gefragt, erwartet 1", n)
			}
		})
	}
}

// Zusatz Sprint 82 (Befund aus Strang N3): Der Antwortkopf des Streams geht
// direkt nach WriteHeader hinaus, nicht erst mit dem ersten Wort. Der Anbieter
// schweigt hier 600 ms; der Client muss den Kopf (Status 200,
// text/event-stream) deutlich früher haben.
func TestLLMStream_KopfSofortGeflusht(t *testing.T) {
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	env.provider.vorPause = 600 * time.Millisecond

	start := time.Now()
	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	kopfNach := time.Since(start)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Status %d, Content-Type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if kopfNach >= 400*time.Millisecond {
		t.Errorf("Antwortkopf erst nach %v — erwartet vor dem ersten Wort (600 ms)", kopfNach)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "event: done") {
		t.Errorf("Stream unvollständig: %s", raw)
	}
}
