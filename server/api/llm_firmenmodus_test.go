package api_test

// llm_firmenmodus_test.go — Karte #664 (ADR-0133 §2 + Nachtrag 2026-10-04
// abends): Firmenmodus in den LLM-Handlern.
//
// Gegen einen ECHTEN OpenAIProvider, der auf einen httptest-Stub zeigt — so
// sieht der Test, was tatsächlich beim Anbieter ankommt: welcher Schlüssel
// im Authorization-Kopf steht, ob das `user`-Feld im Body ist, welche
// Werkzeuge mitgehen.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/internal/secretcrypto"
	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

const (
	firmenSchluessel = "test-schluessel-firma-664"
	privatSchluessel = "test-schluessel-privat-664"
	firmenModell     = "qwen3-30b"
)

type anbieterAufruf struct {
	auth string
	body map[string]any
}

type firmenEnv struct {
	srv     *httptest.Server
	db      *sql.DB
	keys    *storage.LLMKeyRepo
	stub    *httptest.Server
	mu      sync.Mutex
	aufrufe []anbieterAufruf
	// stubFehler != 0 → der Stub antwortet mit diesem Status und echot den
	// Authorization-Kopf im Fehlertext (prüft, dass nichts davon nach aussen
	// oder ins Log gelangt).
	stubFehler int
}

func (e *firmenEnv) letzterAufruf(t *testing.T) anbieterAufruf {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.aufrufe) == 0 {
		t.Fatal("Anbieter wurde nicht aufgerufen")
	}
	return e.aufrufe[len(e.aufrufe)-1]
}

func (e *firmenEnv) anzahlAufrufe() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.aufrufe)
}

// newFirmenEnv baut Send-/Stream-/Schlüssel-API mit fm (nil = kein
// Firmenmodus) und pii. mitKeysRepo=false spielt „kein ABOARD_SECRET_KEY“.
func newFirmenEnv(t *testing.T, fm *llm.Firmenmodus, pii llm.PIIVorgabe, mitKeysRepo bool) *firmenEnv {
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

	env := &firmenEnv{db: db}
	var keys *storage.LLMKeyRepo
	if mitKeysRepo {
		k := make([]byte, secretcrypto.KeySize)
		_, _ = rand.Read(k)
		box, _ := secretcrypto.New(hex.EncodeToString(k))
		keys = storage.NewLLMKeyRepo(db, box)
	}
	env.keys = keys

	// Karte c1 verweist auf c2 — damit hat das Lese-Werkzeug ein Ziel.
	state := `{"version":"2","board":{"id":"b1","title":"T","sprints":[{"id":"s1","title":"S","cards":[` +
		`{"id":"c1","displayId":1,"title":"Karte","description":"Hi","comments":[],"activity":[],"links":[{"type":"related","targetId":"c2"}]},` +
		`{"id":"c2","displayId":2,"title":"Nachbar","description":"x","comments":[],"activity":[],"links":[{"type":"related","targetId":"c1"}]}]}]}}`
	if _, err := boards.Insert(ctx, "b1", "T", "2", []byte(state)); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-a','a@x.de','N','2026-01-01',0)`); err != nil {
		t.Fatal(err)
	}
	if err := acl.Set(ctx, "b1", "a@x.de", storage.RoleEditor); err != nil {
		t.Fatal(err)
	}
	if keys != nil {
		// Ein früher gespeicherter Nutzerschlüssel — im Firmenmodus darf er
		// weder benutzt noch gelöscht werden.
		if err := keys.Set(ctx, "u-a", "openai", privatSchluessel); err != nil {
			t.Fatal(err)
		}
	}

	env.stub = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		env.mu.Lock()
		env.aufrufe = append(env.aufrufe, anbieterAufruf{auth: r.Header.Get("Authorization"), body: body})
		env.mu.Unlock()
		if env.stubFehler != 0 {
			w.WriteHeader(env.stubFehler)
			_, _ = io.WriteString(w, `{"error":{"message":"abgelehnt: `+r.Header.Get("Authorization")+`"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}
		if s, _ := body["stream"].(bool); s {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Antwort\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Antwort"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	}))
	t.Cleanup(env.stub.Close)

	registry := llm.NewRegistry()
	registry.Register(&llm.OpenAIProvider{Client: env.stub.Client(), UA: "test/1",
		Endpoint: env.stub.URL + "/models", SendEndpoint: env.stub.URL + "/chat/completions"})

	sendAPI := api.NewLLMSendAPI(boards, users, keys, registry, llm.NewLockManager(0)).
		WithConversations(storage.NewLLMConversationRepo(db)).
		WithACL(acl).
		WithFirmenmodus(fm, pii)
	keysAPI := api.NewLLMKeysAPI(keys, users, registry).
		WithOpenAIKompatibel("http://llm.example:8000/v1", []string{firmenModell}).
		WithFirmenmodus(fm, pii)

	mux := http.NewServeMux()
	sendAPI.Register(mux)
	keysAPI.Register(mux)
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

func neuerFirmenmodus(t *testing.T) *llm.Firmenmodus {
	t.Helper()
	fm, err := llm.NeuerFirmenmodus(firmenSchluessel, []string{firmenModell})
	if err != nil {
		t.Fatal(err)
	}
	return fm
}

// rufe schickt eine Anfrage als a@x.de und liefert Status und vollständigen
// Antwortkörper (bei SSE: alle Events als Text).
func (e *firmenEnv) rufe(t *testing.T, method, path string, body any) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	r, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	r.Header.Set("Remote-Email", "a@x.de")
	r.Header.Set("Remote-User", "a@x.de")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

const (
	pfadStream = "/boards/b1/cards/c1/llm-send-stream"
	pfadSend   = "/boards/b1/cards/c1/llm-send"
)

// sendWege — Stream und klassischer Send müssen sich gleich verhalten.
var sendWege = []struct{ name, pfad string }{{"stream", pfadStream}, {"send", pfadSend}}

// Kern von ADR-0133 §2: Serverschlüssel statt Nutzerschlüssel, E-Mail als
// `user`-Feld. Der gespeicherte Nutzerschlüssel bleibt in der Datenbank.
func TestFirmenmodus_ServerschluesselUndUserFeld(t *testing.T) {
	for _, weg := range sendWege {
		t.Run(weg.name, func(t *testing.T) {
			env := newFirmenEnv(t, neuerFirmenmodus(t), llm.PIIAus, true)
			status, body := env.rufe(t, http.MethodPost, weg.pfad, map[string]any{"provider": "openai", "model": firmenModell})
			if status != http.StatusOK || strings.Contains(body, "event: error") {
				t.Fatalf("Status %d: %s", status, body)
			}
			a := env.letzterAufruf(t)
			if a.auth != "Bearer "+firmenSchluessel {
				t.Errorf("Authorization = %q, erwartet den Serverschlüssel", a.auth)
			}
			if a.body["user"] != "a@x.de" {
				t.Errorf("user-Feld = %v, erwartet a@x.de", a.body["user"])
			}
			if got, err := env.keys.Get(context.Background(), "u-a", "openai"); err != nil || got != privatSchluessel {
				t.Errorf("gespeicherter Nutzerschlüssel verändert/gelöscht: err=%v", err)
			}
		})
	}
}

// Gegenprobe zum Kern: Ohne Firmenmodus geht der Nutzerschlüssel raus und
// es gibt KEIN `user`-Feld — exakt wie vor #664.
func TestOhneFirmenmodus_NutzerschluesselOhneUserFeld(t *testing.T) {
	for _, weg := range sendWege {
		t.Run(weg.name, func(t *testing.T) {
			env := newFirmenEnv(t, nil, "", true)
			status, body := env.rufe(t, http.MethodPost, weg.pfad, map[string]any{"provider": "openai", "model": "irgendein-modell"})
			if status != http.StatusOK {
				t.Fatalf("Status %d: %s", status, body)
			}
			a := env.letzterAufruf(t)
			if a.auth != "Bearer "+privatSchluessel {
				t.Errorf("Authorization = %q, erwartet den Nutzerschlüssel", a.auth)
			}
			if _, da := a.body["user"]; da {
				t.Errorf("user-Feld ohne Firmenmodus im Body: %v", a.body["user"])
			}
		})
	}
}

// Im Firmenmodus braucht der Send kein Schlüssel-Repo (kein ABOARD_SECRET_KEY).
func TestFirmenmodus_OhneSchluesselRepo(t *testing.T) {
	for _, weg := range sendWege {
		t.Run(weg.name, func(t *testing.T) {
			env := newFirmenEnv(t, neuerFirmenmodus(t), llm.PIIAus, false)
			status, body := env.rufe(t, http.MethodPost, weg.pfad, map[string]any{"provider": "openai", "model": firmenModell})
			if status != http.StatusOK || strings.Contains(body, "event: error") {
				t.Fatalf("Status %d: %s", status, body)
			}
			if a := env.letzterAufruf(t); a.auth != "Bearer "+firmenSchluessel {
				t.Errorf("Authorization = %q", a.auth)
			}
		})
	}
	// Gegenprobe: ohne Firmenmodus bleibt es bei 503.
	env := newFirmenEnv(t, nil, "", false)
	if status, _ := env.rufe(t, http.MethodPost, pfadStream, map[string]any{"provider": "openai", "model": "m"}); status != http.StatusServiceUnavailable {
		t.Errorf("ohne Firmenmodus und ohne Repo: Status %d, erwartet 503", status)
	}
}

// Nachtrag A1/A2: Modell nicht in ABOARD_LLM_OPENAI_MODELLE → 400, der
// Anbieter wird nicht gefragt.
func TestFirmenmodus_ModellNichtInAllowlist(t *testing.T) {
	for _, weg := range sendWege {
		t.Run(weg.name, func(t *testing.T) {
			env := newFirmenEnv(t, neuerFirmenmodus(t), llm.PIIAus, true)
			status, body := env.rufe(t, http.MethodPost, weg.pfad, map[string]any{"provider": "openai", "model": "gpt-4o"})
			if status != http.StatusBadRequest || !strings.Contains(body, api.KindModellNichtErlaubt) {
				t.Fatalf("Status %d, Body %s — erwartet 400 %s", status, body, api.KindModellNichtErlaubt)
			}
			if n := env.anzahlAufrufe(); n != 0 {
				t.Errorf("Anbieter %d× aufgerufen trotz abgelehntem Modell", n)
			}
		})
	}
}

// ABOARD_LLM_PII=pflicht: ein Client, der keinen aktiven Filter meldet, wird
// abgewiesen; mit Meldung geht der Send durch.
func TestPIIPflicht_ClientOhneFilterAbgewiesen(t *testing.T) {
	for _, weg := range sendWege {
		t.Run(weg.name, func(t *testing.T) {
			env := newFirmenEnv(t, neuerFirmenmodus(t), llm.PIIPflicht, true)
			status, body := env.rufe(t, http.MethodPost, weg.pfad, map[string]any{"provider": "openai", "model": firmenModell})
			if status != http.StatusBadRequest || !strings.Contains(body, api.KindPIIPflicht) {
				t.Fatalf("ohne Filter: Status %d, Body %s — erwartet 400 %s", status, body, api.KindPIIPflicht)
			}
			if n := env.anzahlAufrufe(); n != 0 {
				t.Errorf("Anbieter %d× aufgerufen", n)
			}
			status, body = env.rufe(t, http.MethodPost, weg.pfad, map[string]any{"provider": "openai", "model": firmenModell, "piiFilterActive": true})
			if status != http.StatusOK {
				t.Fatalf("mit Filter: Status %d: %s", status, body)
			}
		})
	}
}

// werkzeugNamen liest die Werkzeugnamen aus dem OpenAI-Body.
func werkzeugNamen(body map[string]any) []string {
	var namen []string
	tools, _ := body["tools"].([]any)
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		fn, _ := m["function"].(map[string]any)
		if n, ok := fn["name"].(string); ok {
			namen = append(namen, n)
		}
	}
	return namen
}

// Die Server-Vorgabe ist maßgeblich fürs Nachladen (llm_fetch.go
// fetchVerfuegbar): „aus“ lässt es zu, obwohl der Client einen aktiven
// Filter meldet; „pflicht“ sperrt es; „nutzer“ folgt dem Client. (Dass
// „pflicht“ einen Client ohne Filter-Meldung abweist, prüft
// TestPIIPflicht_ClientOhneFilterAbgewiesen.)
func TestPIIVorgabe_MassgeblichFuersNachladen(t *testing.T) {
	faelle := []struct {
		name      string
		pii       llm.PIIVorgabe
		fm        bool
		erwartet  bool
		clientPII bool
	}{
		{"aus-client-meldet-aktiv", llm.PIIAus, true, true, true},
		{"nutzer-client-meldet-aktiv", llm.PIINutzer, false, false, true},
		{"nutzer-client-meldet-inaktiv", llm.PIINutzer, false, true, false},
		{"pflicht-client-meldet-aktiv", llm.PIIPflicht, true, false, true},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			var fm *llm.Firmenmodus
			modell := "irgendein-modell"
			if f.fm {
				fm = neuerFirmenmodus(t)
				modell = firmenModell
			}
			env := newFirmenEnv(t, fm, f.pii, true)
			status, body := env.rufe(t, http.MethodPost, pfadStream, map[string]any{
				"provider": "openai", "model": modell, "enableFetch": true, "piiFilterActive": f.clientPII,
			})
			if status != http.StatusOK {
				t.Fatalf("Status %d: %s", status, body)
			}
			mitFetch := strings.Contains(strings.Join(werkzeugNamen(env.letzterAufruf(t).body), ","), llm.FetchRelatedCardTool)
			if mitFetch != f.erwartet {
				t.Errorf("Lese-Werkzeug mitgeschickt = %v, erwartet %v", mitFetch, f.erwartet)
			}
		})
	}
}

// GET /users/me/llm-keys meldet den Firmenmodus — und keine gespeicherten
// Nutzerschlüssel, weil die nicht benutzt werden.
func TestFirmenmodus_SchluesselListeMeldetFirmenmodus(t *testing.T) {
	env := newFirmenEnv(t, neuerFirmenmodus(t), llm.PIIAus, true)
	status, body := env.rufe(t, http.MethodGet, "/users/me/llm-keys", nil)
	if status != http.StatusOK {
		t.Fatalf("Status %d: %s", status, body)
	}
	var got struct {
		Providers   []string        `json:"providers"`
		Keys        []any           `json:"keys"`
		Firmenmodus *map[string]any `json:"firmenmodus"`
		PII         string          `json:"pii"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 0 || len(got.Keys) != 0 {
		t.Errorf("gespeicherte Schlüssel gemeldet: %s", body)
	}
	if got.Firmenmodus == nil {
		t.Fatalf("firmenmodus fehlt: %s", body)
	}
	fmw := *got.Firmenmodus
	if fmw["ziel"] != "llm.example:8000" {
		t.Errorf("ziel = %v", fmw["ziel"])
	}
	if a, _ := fmw["anbieter"].([]any); len(a) != 1 || a[0] != "openai" {
		t.Errorf("anbieter = %v", fmw["anbieter"])
	}
	if m, _ := fmw["modelle"].([]any); len(m) != 1 || m[0] != firmenModell {
		t.Errorf("modelle = %v", fmw["modelle"])
	}
	if got.PII != "aus" {
		t.Errorf("pii = %q, erwartet aus", got.PII)
	}
	if strings.Contains(body, firmenSchluessel) || strings.Contains(body, privatSchluessel) {
		t.Errorf("Antwort enthält einen Schlüssel: %s", body)
	}
	// Ohne Schlüssel-Repo antwortet die Liste im Firmenmodus trotzdem.
	env2 := newFirmenEnv(t, neuerFirmenmodus(t), llm.PIIPflicht, false)
	if status, body := env2.rufe(t, http.MethodGet, "/users/me/llm-keys", nil); status != http.StatusOK || !strings.Contains(body, `"pii":"pflicht"`) {
		t.Errorf("ohne Repo: Status %d, Body %s", status, body)
	}
}

// Gegenprobe: Ohne die neuen Variablen ist die Antwort exakt die alte — kein
// firmenmodus-, kein pii-Schlüssel, die gespeicherten Schlüssel stehen drin.
func TestOhneFirmenmodus_SchluesselListeUnveraendert(t *testing.T) {
	for _, pii := range []llm.PIIVorgabe{"", llm.PIINutzer} {
		env := newFirmenEnv(t, nil, pii, true)
		status, body := env.rufe(t, http.MethodGet, "/users/me/llm-keys", nil)
		if status != http.StatusOK {
			t.Fatalf("Status %d: %s", status, body)
		}
		var got map[string]any
		_ = json.Unmarshal([]byte(body), &got)
		for _, k := range []string{"firmenmodus", "pii"} {
			if _, da := got[k]; da {
				t.Errorf("pii=%q: Schlüssel %q in der Antwort: %s", pii, k, body)
			}
		}
		if p, _ := got["providers"].([]any); len(p) != 1 || p[0] != "openai" {
			t.Errorf("providers = %v, erwartet [openai]", got["providers"])
		}
	}
	// pflicht ohne Firmenmodus wird gemeldet.
	env := newFirmenEnv(t, nil, llm.PIIPflicht, true)
	if _, body := env.rufe(t, http.MethodGet, "/users/me/llm-keys", nil); !strings.Contains(body, `"pii":"pflicht"`) || strings.Contains(body, "firmenmodus") {
		t.Errorf("pflicht ohne Firmenmodus: %s", body)
	}
}

// Speichern und Testen eigener Schlüssel: 409 vom-betreiber-eingerichtet,
// kein Anbieter-Aufruf, der gespeicherte Schlüssel bleibt. Löschen des
// eigenen gespeicherten Schlüssels bleibt möglich.
func TestFirmenmodus_EigeneSchluesselAbgelehnt(t *testing.T) {
	env := newFirmenEnv(t, neuerFirmenmodus(t), llm.PIIAus, true)
	status, body := env.rufe(t, http.MethodPost, "/users/me/llm-keys/openai", map[string]any{"key": "test-schluessel-neu-664"})
	if status != http.StatusConflict || !strings.Contains(body, api.KindVomBetreiber) {
		t.Fatalf("Speichern: Status %d, Body %s — erwartet 409 %s", status, body, api.KindVomBetreiber)
	}
	status, body = env.rufe(t, http.MethodPost, "/users/me/llm-keys/openai/test", map[string]any{})
	if status != http.StatusConflict || !strings.Contains(body, api.KindVomBetreiber) {
		t.Fatalf("Testen: Status %d, Body %s", status, body)
	}
	if n := env.anzahlAufrufe(); n != 0 {
		t.Errorf("Anbieter %d× aufgerufen — ein privater Schlüssel ginge ans Firmen-Gegenüber", n)
	}
	if got, err := env.keys.Get(context.Background(), "u-a", "openai"); err != nil || got != privatSchluessel {
		t.Errorf("gespeicherter Schlüssel verändert: err=%v", err)
	}
	if status, body := env.rufe(t, http.MethodDelete, "/users/me/llm-keys/openai", nil); status != http.StatusOK {
		t.Errorf("Löschen: Status %d, Body %s", status, body)
	}
	// Gegenprobe: ohne Firmenmodus speichert derselbe Aufruf.
	env2 := newFirmenEnv(t, nil, "", true)
	if status, body := env2.rufe(t, http.MethodPost, "/users/me/llm-keys/openai", map[string]any{"key": "test-schluessel-neu-664"}); status != http.StatusOK {
		t.Errorf("ohne Firmenmodus: Status %d, Body %s", status, body)
	}
}

// Der Serverschlüssel erscheint weder in einer Antwort noch im Log — auch
// dann nicht, wenn der Anbieter ihn in seiner Fehlermeldung zurückspiegelt.
func TestFirmenmodus_SchluesselNieInLogOderAntwort(t *testing.T) {
	var logPuffer bytes.Buffer
	var logMu sync.Mutex
	alt := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logPuffer.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(alt) })

	var antworten []string
	for _, stubStatus := range []int{0, http.StatusUnauthorized, http.StatusBadRequest, http.StatusInternalServerError} {
		for _, weg := range sendWege {
			env := newFirmenEnv(t, neuerFirmenmodus(t), llm.PIIAus, true)
			env.stubFehler = stubStatus
			_, body := env.rufe(t, http.MethodPost, weg.pfad, map[string]any{"provider": "openai", "model": firmenModell})
			antworten = append(antworten, body)
			_, body = env.rufe(t, http.MethodPost, weg.pfad, map[string]any{"provider": "openai", "model": "nicht-erlaubt"})
			antworten = append(antworten, body)
			_, body = env.rufe(t, http.MethodGet, "/users/me/llm-keys", nil)
			antworten = append(antworten, body)
			_, body = env.rufe(t, http.MethodPost, "/users/me/llm-keys/openai/test", map[string]any{})
			antworten = append(antworten, body)
		}
	}
	for _, a := range antworten {
		if strings.Contains(a, firmenSchluessel) {
			t.Errorf("Antwort enthält den Serverschlüssel: %s", a)
		}
	}
	logMu.Lock()
	defer logMu.Unlock()
	if strings.Contains(logPuffer.String(), firmenSchluessel) {
		t.Errorf("Log enthält den Serverschlüssel:\n%s", logPuffer.String())
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
