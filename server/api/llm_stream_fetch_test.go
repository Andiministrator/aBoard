package api_test

// llm_stream_fetch_test.go — der agentische Loop Ende-zu-Ende (#428,
// ADR-0120). Ergänzt llm_stream_test.go um einen Provider-Mock, der über
// MEHRERE Runden antwortet: Runde 1 ruft das Lese-Werkzeug, Runde 2 schreibt
// die Antwort.
//
// Die Autorisierung selbst wird in llm_fetch_internal_test.go geprüft; hier
// geht es um das, was nur im Zusammenspiel sichtbar wird — dass der Server
// wirklich ein zweites Mal beim Provider anfragt, dass die Werkzeug-Antwort
// dort ankommt, dass die Obergrenze über den ganzen Turn hält und dass der
// Audit-Eintrag an der Karte landet.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// ─── Mehrrunden-Mock ────────────────────────────────────────────────────

// runde beschreibt, was der Provider bei EINEM SendStream-Aufruf liefert.
type runde struct {
	text      string
	toolCalls []llm.ToolCallChunk
	// Gemeldete Token-Zahlen dieser Runde (F-1091). 0 heisst: Vorgabe
	// (10 Input, 5 Output). ohneMessung: der Anbieter meldet gar nichts
	// (InputTokens und OutputTokens 0) — dann muss der Server schätzen.
	eingabe     int
	ausgabe     int
	ohneMessung bool
}

// mehrrundenProvider spielt pro SendStream-Aufruf die nächste Runde ab und
// merkt sich, was ihm dabei übergeben wurde. Läuft der Vorrat aus, liefert er
// nur noch Text — damit ein Loop-Fehler als Endlosschleife auffällt statt als
// Absturz.
type mehrrundenProvider struct {
	mu       sync.Mutex
	runden   []runde
	aufrufe  int
	gesehen  []llm.SendRequest // Kopie jeder Anfrage, in Reihenfolge
	inPM     float64
	outPM    float64
	toolsAn  []bool // EnableFetch pro Aufruf
	restText string
	// Optionale Staffel (F-3102): über staffelAb Prompt-Tokens gelten
	// staffelIn/staffelOut. 0 = keine Staffel (Verhalten wie Pricing).
	staffelAb  int
	staffelIn  float64
	staffelOut float64
}

func (m *mehrrundenProvider) PricingBei(_ string, promptTokens int) (float64, float64) {
	if m.staffelAb > 0 && promptTokens > m.staffelAb {
		return m.staffelIn, m.staffelOut
	}
	return m.inPM, m.outPM
}

func (m *mehrrundenProvider) Name() string                          { return "anthropic" }
func (m *mehrrundenProvider) TestKey(context.Context, string) error { return nil }
func (m *mehrrundenProvider) Pricing(string) (float64, float64)     { return m.inPM, m.outPM }
func (m *mehrrundenProvider) Send(context.Context, string, llm.SendRequest) (llm.SendResult, error) {
	return llm.SendResult{}, nil
}

func (m *mehrrundenProvider) SendStream(ctx context.Context, _ string, req llm.SendRequest, onChunk func(llm.StreamChunk) error) (llm.SendResult, error) {
	m.mu.Lock()
	idx := m.aufrufe
	m.aufrufe++
	m.gesehen = append(m.gesehen, req)
	m.toolsAn = append(m.toolsAn, req.EnableFetch)
	var r runde
	if idx < len(m.runden) {
		r = m.runden[idx]
	} else {
		r = runde{text: m.restText}
	}
	m.mu.Unlock()

	if r.text != "" {
		if err := onChunk(llm.StreamChunk{Text: r.text}); err != nil {
			return llm.SendResult{}, err
		}
	}
	for i := range r.toolCalls {
		tc := r.toolCalls[i]
		if err := onChunk(llm.StreamChunk{ToolCall: &tc}); err != nil {
			return llm.SendResult{}, err
		}
	}
	ein, aus := 10, 5
	if r.eingabe > 0 {
		ein = r.eingabe
	}
	if r.ausgabe > 0 {
		aus = r.ausgabe
	}
	if r.ohneMessung {
		ein, aus = 0, 0
	}
	return llm.SendResult{
		Text: r.text, InputTokens: ein, OutputTokens: aus,
		Model: "claude-opus-4-7", FinishReason: "end_turn",
	}, nil
}

func (m *mehrrundenProvider) anzahlAufrufe() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.aufrufe
}

func (m *mehrrundenProvider) anfrage(i int) llm.SendRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gesehen[i]
}

func fetchAufruf(id, cardID string) llm.ToolCallChunk {
	return llm.ToolCallChunk{
		ID:   id,
		Name: llm.FetchRelatedCardTool,
		Args: json.RawMessage(`{"cardId":"` + cardID + `"}`),
	}
}

// ─── Environment ────────────────────────────────────────────────────────

type fetchStreamEnv struct {
	srv      *httptest.Server
	db       *sql.DB
	boards   *storage.BoardRepo
	provider *mehrrundenProvider
	anhaenge *testAnhangStore
	boardID  string
	cardID   string
	email    string
}

// testAnhangStore stellt den Attachment-Store fuer die Stream-Tests (#429).
// Kein Dateisystem: Es geht um den Weg durch den Handler, nicht um Storage.
type testAnhangStore struct{ dateien map[string][]byte }

func (s *testAnhangStore) Open(_, _, attId string) (io.ReadSeekCloser, string, error) {
	b, ok := s.dateien[attId]
	if !ok {
		return nil, "", errors.New("nicht gefunden")
	}
	return nopSeeker{bytes.NewReader(b)}, attId + ".png", nil
}

// testPNG — kurze Bytefolge (PNG-Signatur + IHDR-Kopf), die http.DetectContentType als image/png
// erkennt. Seit F-1094 bestimmt der Server den Typ jedes Anhangs am Inhalt;
// drei beliebige Bytes gingen nicht mehr als Bild durch.
var testPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

type nopSeeker struct{ *bytes.Reader }

func (nopSeeker) Close() error { return nil }

// Board-State: c1 (Quelle) ↔ c2 (verknüpft), c3 liegt unverknüpft daneben.
const fetchBoardState = `{"version":"2","board":{"id":"b1","title":"T","sprints":[{"id":"s1","title":"S","cards":[
{"id":"c1","displayId":1,"title":"Quelle","description":"Quelltext","comments":[],"activity":[],"links":[{"type":"related","targetId":"c2"}],"attachments":[{"id":"att-1","filename":"bild.png","mime":"image/png","size":3}]},
{"id":"c2","displayId":2,"title":"Nachbar","description":"NACHBARTEXT","comments":[],"activity":[],"links":[{"type":"related","targetId":"c1"}]},
{"id":"c3","displayId":3,"title":"Fremd","description":"GEHEIMTEXT","comments":[],"activity":[],"links":[]}]}]}}`

func newFetchStreamEnv(t *testing.T, runden []runde) *fetchStreamEnv {
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

	if _, err := boards.Insert(ctx, "b1", "T", "2", []byte(fetchBoardState)); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	email := "alice@example.com"
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

	anh := &testAnhangStore{dateien: map[string][]byte{"att-1": testPNG}}
	mp := &mehrrundenProvider{runden: runden, inPM: 1, outPM: 1, restText: "fertig"}
	registry := llm.NewRegistry()
	registry.Register(mp)

	sendAPI := api.NewLLMSendAPI(boards, users, keys, registry, llm.NewLockManager(0)).
		WithConversations(storage.NewLLMConversationRepo(db)).
		WithACL(acl).
		WithAttachments(anh)

	mux := http.NewServeMux()
	sendAPI.Register(mux)
	var h http.Handler = mux
	h = api.NewACLGuard(acl, users, bootstrap).Middleware(h)
	h = api.WithForwardAuthIfEnabled(h)
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); _ = db.Close() })

	return &fetchStreamEnv{srv: srv, db: db, boards: boards, provider: mp, anhaenge: anh,
		boardID: "b1", cardID: "c1", email: email}
}

func (e *fetchStreamEnv) sende(t *testing.T, body map[string]any) []sseEvent {
	t.Helper()
	raw, _ := json.Marshal(body)
	r, _ := http.NewRequest(http.MethodPost, e.srv.URL+streamPath(e.boardID, e.cardID), strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Remote-Email", e.email)
	r.Header.Set("Remote-User", e.email)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d", resp.StatusCode)
	}
	return parseSSEResponse(t, resp.Body)
}

func standardBody(extra map[string]any) map[string]any {
	b := map[string]any{
		"provider":      "anthropic",
		"model":         "claude-opus-4-7",
		"systemPrompt":  "Sys",
		"includeFields": []string{"description"},
	}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func ereignisse(evs []sseEvent, typ string) []map[string]any {
	var out []map[string]any
	for _, e := range evs {
		if e.Event != typ {
			continue
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(e.Data), &m)
		out = append(out, m)
	}
	return out
}

// ─── Tests ──────────────────────────────────────────────────────────────

// Der Kern: Das Modell ruft das Werkzeug, der Server beantwortet es SELBST
// und fragt den Provider ein zweites Mal — mit dem Karteninhalt in der
// History.
func TestFetchLoop_BeantwortetUndFragtErneut(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{text: "Ich sehe nach. ", toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}},
		{text: "Der Nachbar sagt X."},
	})
	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	if got := env.provider.anzahlAufrufe(); got != 2 {
		t.Fatalf("%d Provider-Aufrufe, erwartet 2 (ohne zweiten Aufruf ist es kein agentischer Loop)", got)
	}

	// Der Werkzeug-Aufruf darf NICHT als Mutations-Vorschlag beim Client
	// landen — er ist ein Lesevorgang, den der Server beantwortet.
	if tc := ereignisse(evs, "tool-call"); len(tc) != 0 {
		t.Fatalf("Lese-Werkzeug als Mutations-Vorschlag gestreamt: %+v", tc)
	}

	fetches := ereignisse(evs, "fetch")
	if len(fetches) != 1 {
		t.Fatalf("%d fetch-Ereignisse, erwartet 1: %+v", len(fetches), fetches)
	}
	if fetches[0]["ok"] != true || fetches[0]["label"] != "#2" {
		t.Fatalf("fetch-Ereignis falsch: %+v", fetches[0])
	}

	// Die zweite Anfrage muss den Karteninhalt tragen — sonst hat der Loop
	// zwar zweimal gefragt, aber nichts übergeben.
	zweite := env.provider.anfrage(1)
	if zweite.UserPrompt != "" {
		t.Fatalf("zweite Runde schickt erneut einen UserPrompt (%q) — der steht dann doppelt in der Unterhaltung", zweite.UserPrompt)
	}
	var gefunden bool
	for _, h := range zweite.History {
		for _, tr := range h.ToolResults {
			if strings.Contains(tr.Content, "NACHBARTEXT") {
				gefunden = true
			}
			if tr.IsError {
				t.Fatalf("Ergebnis als Fehler markiert: %+v", tr)
			}
		}
	}
	if !gefunden {
		t.Fatalf("Karteninhalt fehlt in der zweiten Anfrage: %+v", zweite.History)
	}

	// Der Text beider Runden landet im Kommentar.
	done := ereignisse(evs, "done")
	if len(done) != 1 {
		t.Fatalf("kein done-Ereignis: %+v", evs)
	}
	if done[0]["fetchedCards"] == nil {
		t.Fatalf("done nennt die nachgeladenen Karten nicht: %+v", done[0])
	}
	// Token-Zahlen sind über beide Runden summiert (2 × 10 Input).
	if in, _ := done[0]["inputTokens"].(float64); in != 20 {
		t.Fatalf("inputTokens = %v, erwartet 20 (Summe beider Runden)", done[0]["inputTokens"])
	}
}

// Die Sperre im Zusammenspiel: eine unverknüpfte Karte wird abgelehnt, und
// ihr Inhalt taucht nirgends auf — weder in der zweiten Anfrage an den
// Provider noch im SSE-Strom.
func TestFetchLoop_UnverknuepfteKarteWirdAbgelehnt(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c3")}},
		{text: "Ohne die Karte."},
	})
	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	fetches := ereignisse(evs, "fetch")
	if len(fetches) != 1 || fetches[0]["ok"] != false {
		t.Fatalf("erwartet eine abgelehnte Anfrage: %+v", fetches)
	}
	roh, _ := json.Marshal(evs)
	if strings.Contains(string(roh), "GEHEIMTEXT") {
		t.Fatal("Inhalt der unverknuepften Karte ist in den SSE-Strom gelangt")
	}
	zweite := env.provider.anfrage(1)
	for _, h := range zweite.History {
		for _, tr := range h.ToolResults {
			if strings.Contains(tr.Content, "GEHEIMTEXT") {
				t.Fatal("Inhalt der unverknuepften Karte ist an den Provider gegangen")
			}
			if !tr.IsError {
				t.Fatalf("Ablehnung nicht als Fehler markiert: %+v", tr)
			}
		}
	}
}

// Elf Abrufe in einem Turn: der elfte wird abgelehnt. Der Zähler läuft über
// den GESAMTEN Turn — pro Runde zurückgesetzt wäre er wirkungslos.
func TestFetchLoop_ObergrenzeGiltUeberDenGanzenTurn(t *testing.T) {
	var r []runde
	for i := 0; i < llm.MaxFetchesPerTurn+1; i++ {
		r = append(r, runde{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu", "c2")}})
	}
	env := newFetchStreamEnv(t, r)
	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	fetches := ereignisse(evs, "fetch")
	if len(fetches) != llm.MaxFetchesPerTurn+1 {
		t.Fatalf("%d fetch-Ereignisse, erwartet %d", len(fetches), llm.MaxFetchesPerTurn+1)
	}
	for i, f := range fetches[:llm.MaxFetchesPerTurn] {
		if f["ok"] != true {
			t.Fatalf("Abruf %d wurde abgelehnt, obwohl im Budget: %+v", i+1, f)
		}
	}
	letzter := fetches[llm.MaxFetchesPerTurn]
	if letzter["ok"] != false {
		t.Fatalf("Abruf %d wurde beantwortet — die Obergrenze haelt nicht: %+v", llm.MaxFetchesPerTurn+1, letzter)
	}
	if grund, _ := letzter["reason"].(string); !strings.Contains(grund, "Obergrenze") {
		t.Fatalf("Grund benennt die Obergrenze nicht: %q", grund)
	}

	// Und der Punkt, an dem ein frueherer Anlauf falsch lag: Nach dem
	// abgelehnten Abruf muss das Modell NOCH EINMAL drankommen, um mit dem zu
	// antworten, was es hat. Griffe stattdessen die Runden-Obergrenze, endete
	// die Antwort mitten im Satz — und der Grund erreichte das Modell nie.
	if got := env.provider.anzahlAufrufe(); got != llm.MaxFetchesPerTurn+2 {
		t.Fatalf("%d Provider-Aufrufe, erwartet %d — nach der Ablehnung fehlt die Runde, in der das Modell antwortet",
			got, llm.MaxFetchesPerTurn+2)
	}
	letzteAnfrage := env.provider.anfrage(llm.MaxFetchesPerTurn + 1)
	var sahAblehnung bool
	for _, h := range letzteAnfrage.History {
		for _, tr := range h.ToolResults {
			if tr.IsError && strings.Contains(tr.Content, "Obergrenze") {
				sahAblehnung = true
			}
		}
	}
	if !sahAblehnung {
		t.Fatal("die Ablehnung wegen der Obergrenze ist nie beim Modell angekommen")
	}
	done := ereignisse(evs, "done")
	if len(done) != 1 {
		t.Fatalf("kein done-Ereignis — der Turn ist abgebrochen statt beendet worden: %+v", evs)
	}
}

// F-857 (Karte #488): Der Rundendeckel sitzt eine Ebene VOR der Obergrenze.
//
// MaxFetchesPerTurn begrenzt, was beantwortet wird — mit einer Ablehnung, die
// das Modell erreicht. Das ist der Regelweg und bleibt es. Es begrenzt aber
// nicht, was der Handler einsammelt: Ein Modell, das in EINER Runde tausend
// Aufrufe emittiert, erzeugte tausend Ablehnungen, Logzeilen, SSE-Ereignisse
// und ToolResultBlocks in der History. Kein Datenverlust — aber der Logstrom
// ist dann ausgerechnet im Anomaliefall unlesbar.
func TestFetchLoop_RundendeckelBegrenztDasEinsammeln(t *testing.T) {
	// Eine einzige Runde mit weit mehr Aufrufen, als je beantwortet werden
	// können. Ohne Deckel entstünde pro Aufruf ein fetch-Ereignis.
	const emittiert = llm.MaxToolCallsPerRound * 5
	var calls []llm.ToolCallChunk
	for i := 0; i < emittiert; i++ {
		calls = append(calls, fetchAufruf(fmt.Sprintf("tu%d", i), "c2"))
	}
	env := newFetchStreamEnv(t, []runde{{toolCalls: calls}})
	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	fetches := ereignisse(evs, "fetch")
	if len(fetches) > llm.MaxToolCallsPerRound {
		t.Fatalf("%d fetch-Ereignisse aus einer Runde — der Deckel (%d) greift nicht",
			len(fetches), llm.MaxToolCallsPerRound)
	}
	// Die Gegenrichtung: Der Deckel darf die Runde nicht ganz verschlucken.
	// Ein Deckel, der auch die ersten Aufrufe wegwirft, wäre kein Deckel,
	// sondern ein Abschalter — und der Test oben allein bliebe dabei grün.
	if len(fetches) == 0 {
		t.Fatal("kein einziger Abruf beantwortet — der Deckel wirft die ganze Runde weg")
	}
	// Der Turn endet regulär, nicht mit einem Abbruch.
	if len(ereignisse(evs, "done")) != 1 {
		t.Fatalf("kein done-Ereignis — der Turn ist abgebrochen worden: %+v", evs)
	}
	// Und was der Provider in der Folgerunde zu sehen bekommt, ist gepaart:
	// zu jedem tool_use genau ein tool_result. Anthropic lehnt eine Anfrage
	// ab, in der eines fehlt — deshalb dürfen verworfene Aufrufe auch nicht
	// in der History landen. Gezählt wird über die GANZE History, nicht je
	// Nachricht: Der Handler legt erst den Assistant-Zug mit den Aufrufen ab
	// und dann unsere Antworten als eigene Nachricht.
	if env.provider.anzahlAufrufe() >= 2 {
		zweite := env.provider.anfrage(1)
		var uses, results int
		for _, h := range zweite.History {
			uses += len(h.ToolUses)
			results += len(h.ToolResults)
		}
		if uses != results {
			t.Fatalf("%d tool_use gegen %d tool_result in der Folge-Anfrage — ungepaart",
				uses, results)
		}
		if uses > llm.MaxToolCallsPerRound {
			t.Fatalf("%d tool_use in der History — verworfene Aufrufe sind mitgewandert",
				uses)
		}
	}
}

// Ohne enableFetch wird das Werkzeug nicht mitgeschickt — und ein trotzdem
// halluzinierter Aufruf läuft in die Mutations-Validierung und wird dort
// fail-loud abgelehnt, statt still zu wirken.
func TestFetchLoop_OhneOptInKeinWerkzeug(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}},
	})
	evs := env.sende(t, standardBody(map[string]any{"enableMutations": true}))

	if env.provider.anfrage(0).EnableFetch {
		t.Fatal("EnableFetch war gesetzt, obwohl der Client es nicht angefordert hat")
	}
	if len(ereignisse(evs, "fetch")) != 0 {
		t.Fatal("fetch-Ereignis ohne Opt-in")
	}
	tc := ereignisse(evs, "tool-call")
	if len(tc) != 1 || tc[0]["blocked"] != true {
		t.Fatalf("halluzinierter Aufruf nicht fail-loud abgelehnt: %+v", tc)
	}
	if env.provider.anzahlAufrufe() != 1 {
		t.Fatalf("%d Provider-Aufrufe — ohne Opt-in darf es keine zweite Runde geben", env.provider.anzahlAufrufe())
	}
}

// ADR-0120 §4: Mit gesetztem userPromptOverride (PII-Filter aktiv) wird das
// Werkzeug nicht angeboten — die nachgeladene Karte hätte den Browser-Filter
// strukturell nicht passiert.
func TestFetchLoop_MitPIIFilterKeinWerkzeug(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}},
	})
	evs := env.sende(t, standardBody(map[string]any{
		"enableFetch":        true,
		"userPromptOverride": "redigierter Prompt",
	}))
	if env.provider.anfrage(0).EnableFetch {
		t.Fatal("EnableFetch trotz aktivem PII-Filter gesetzt")
	}
	if len(ereignisse(evs, "fetch")) != 0 {
		t.Fatalf("Nachladen trotz aktivem PII-Filter: %+v", evs)
	}
}

// Der Audit-Eintrag landet an der Karte (ADR-0120 §6): ein llm-sent-Eintrag,
// dessen meta die nachgeladenen Karten nennt.
func TestFetchLoop_AuditAmKartenLog(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2"), fetchAufruf("tu-2", "c3")}},
		{text: "Antwort."},
	})
	env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	b, err := env.boards.Get(context.Background(), env.boardID)
	if err != nil {
		t.Fatalf("board lesen: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b.StateJSON, &root); err != nil {
		t.Fatalf("state parsen: %v", err)
	}
	board := root["board"].(map[string]any)
	sprint := board["sprints"].([]any)[0].(map[string]any)
	var quelle map[string]any
	for _, c := range sprint["cards"].([]any) {
		cm := c.(map[string]any)
		if cm["id"] == "c1" {
			quelle = cm
		}
	}
	if quelle == nil {
		t.Fatal("Quellkarte nicht gefunden")
	}
	activity, _ := quelle["activity"].([]any)
	if len(activity) != 1 {
		t.Fatalf("%d Aktivitaets-Eintraege, erwartet genau 1 pro Turn (nicht einen pro Abruf)", len(activity))
	}
	meta := activity[0].(map[string]any)["meta"].(map[string]any)
	geladen, _ := meta["fetchedCards"].([]any)
	if len(geladen) != 1 || geladen[0] != "#2" {
		t.Fatalf("fetchedCards = %+v, erwartet [#2]", meta["fetchedCards"])
	}
	if abgelehnt, _ := meta["fetchesRejected"].(float64); abgelehnt != 1 {
		t.Fatalf("fetchesRejected = %v, erwartet 1 (c3 ist unverknuepft)", meta["fetchesRejected"])
	}
	note, _ := meta["contextNote"].(string)
	if !strings.Contains(note, "#2") {
		t.Fatalf("contextNote nennt die nachgeladene Karte nicht: %q", note)
	}
}

// Ohne Nachladen bleibt der meta-Block frei von Fetch-Feldern — eine Null in
// jedem Eintrag ist das Rauschen, das dazu führt, dass niemand mehr hinsieht.
func TestFetchLoop_OhneNachladenKeineFetchFelder(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{{text: "Nur Text."}})
	evs := env.sende(t, standardBody(nil))

	done := ereignisse(evs, "done")
	if len(done) != 1 {
		t.Fatalf("kein done: %+v", evs)
	}
	if _, da := done[0]["fetchedCards"]; da {
		t.Fatalf("fetchedCards im done, obwohl nichts nachgeladen wurde: %+v", done[0])
	}
	if _, da := done[0]["fetchesRejected"]; da {
		t.Fatalf("fetchesRejected im done, obwohl nichts abgelehnt wurde: %+v", done[0])
	}
}

// ─── Anhänge als Bilder (#429, ADR-0121) ────────────────────────────────

// sendeRoh schickt eine Anfrage und liefert die Antwort ungefiltert — für die
// Fälle, die VOR dem Stream scheitern sollen (dort gibt es keinen SSE-Body,
// sondern eine JSON-Fehlerantwort).
func (e *fetchStreamEnv) sendeRoh(t *testing.T, body map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r, _ := http.NewRequest(http.MethodPost, e.srv.URL+streamPath(e.boardID, e.cardID), strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Remote-Email", e.email)
	r.Header.Set("Remote-User", e.email)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	var parsed map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return resp, parsed
}

// Der Kern von Punkt 1 der Karte: Ein Send an ein Modell ohne Bild-Fähigkeit
// muss sauber scheitern, OHNE Kosten zu erzeugen. „Ohne Kosten" heisst
// nachprüfbar: der Anbieter darf gar nicht erst gerufen werden.
func TestBilder_NichtVisionModellScheitertOhneAnbieterAufruf(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{{text: "sollte nie kommen"}})
	resp, body := env.sendeRoh(t, standardBody(map[string]any{
		"model":         "claude-haiku-4-5", // Provider bleibt anthropic (Mock)
		"attachmentIds": []string{"att-1"},
	}))
	_ = body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Vorbedingung: ein Vision-Modell soll durchgehen, bekam %d", resp.StatusCode)
	}

	env2 := newFetchStreamEnv(t, []runde{{text: "sollte nie kommen"}})
	resp2, body2 := env2.sendeRoh(t, standardBody(map[string]any{
		"model":         "o1-mini", // kann keine Bilder
		"attachmentIds": []string{"att-1"},
	}))
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("Status %d, erwartet 400", resp2.StatusCode)
	}
	if body2["kind"] != "vision-not-supported" {
		t.Fatalf("kind = %v, erwartet vision-not-supported: %+v", body2["kind"], body2)
	}
	if got := env2.provider.anzahlAufrufe(); got != 0 {
		t.Fatalf("%d Anbieter-Aufrufe — ein untauglicher Send darf keine Kosten erzeugen", got)
	}
}

// Ohne ausgewählte Anhänge greift die Capability-Prüfung nicht — sonst wären
// Nicht-Vision-Modelle für gewöhnliche Sends gesperrt.
func TestBilder_OhneAuswahlKeineCapabilityPruefung(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{{text: "Antwort"}})
	resp, _ := env.sendeRoh(t, standardBody(map[string]any{"model": "o1-mini"}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d — ein Send ohne Anhaenge darf an jedem Modell gehen", resp.StatusCode)
	}
}

func TestBilder_GelangenAnDenAnbieter(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{{text: "Ein Diagramm."}})
	env.sende(t, standardBody(map[string]any{"attachmentIds": []string{"att-1"}}))

	if env.provider.anzahlAufrufe() != 1 {
		t.Fatalf("%d Anbieter-Aufrufe", env.provider.anzahlAufrufe())
	}
	req := env.provider.anfrage(0)
	if len(req.Images) != 1 {
		t.Fatalf("%d Bilder im Request, erwartet 1", len(req.Images))
	}
	if req.Images[0].MIME != "image/png" || req.Images[0].Filename != "bild.png" {
		t.Fatalf("Metadaten falsch: %+v", req.Images[0])
	}
	if !bytes.Equal(req.Images[0].Data, testPNG) {
		t.Fatalf("Bytes falsch: %v", req.Images[0].Data)
	}
}

// Ein Anhang, der nicht zu dieser Karte gehört, wird vor dem Stream
// abgelehnt — und auch hier ohne Anbieter-Aufruf.
func TestBilder_FremderAnhangWirdAbgelehnt(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{{text: "x"}})
	resp, body := env.sendeRoh(t, standardBody(map[string]any{
		"attachmentIds": []string{"gibt-es-nicht"},
	}))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Status %d, erwartet 400", resp.StatusCode)
	}
	if body["kind"] != "attachment-rejected" {
		t.Fatalf("kind = %v: %+v", body["kind"], body)
	}
	if env.provider.anzahlAufrufe() != 0 {
		t.Fatal("Anbieter wurde trotz abgelehntem Anhang gerufen")
	}
}

// Bilder und Nachladen zusammen: Wandert der User-Turn im Fetch-Loop in die
// History, müssen die Bilder mitkommen — sonst sieht das Modell sie ab
// Runde 2 nicht mehr, mitten im selben Turn.
func TestBilder_UeberlebenDenFetchLoop(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}},
		{text: "Fertig."},
	})
	env.sende(t, standardBody(map[string]any{
		"enableFetch":   true,
		"attachmentIds": []string{"att-1"},
	}))

	if env.provider.anzahlAufrufe() != 2 {
		t.Fatalf("%d Anbieter-Aufrufe, erwartet 2", env.provider.anzahlAufrufe())
	}
	zweite := env.provider.anfrage(1)
	if len(zweite.Images) != 0 {
		t.Fatalf("Bilder haengen ab Runde 2 noch am Request statt in der History: %+v", zweite.Images)
	}
	var gefunden bool
	for _, h := range zweite.History {
		if len(h.Images) > 0 {
			gefunden = true
			if h.Role != "user" {
				t.Fatalf("Bilder an einer %q-Nachricht", h.Role)
			}
		}
	}
	if !gefunden {
		t.Fatalf("Bilder sind zwischen Runde 1 und 2 verloren gegangen: %+v", zweite.History)
	}
}

// Audit: Die mitgeschickten Anhänge stehen namentlich in der contextNote am
// Aktivitäts-Eintrag — sonst lässt sich später nicht feststellen, welches
// Bild an ein fremdes Modell gegangen ist.
func TestBilder_StehenImAudit(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{{text: "Antwort."}})
	env.sende(t, standardBody(map[string]any{"attachmentIds": []string{"att-1"}}))

	b, err := env.boards.Get(context.Background(), env.boardID)
	if err != nil {
		t.Fatalf("board lesen: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b.StateJSON, &root); err != nil {
		t.Fatalf("state parsen: %v", err)
	}
	sprint := root["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)
	var quelle map[string]any
	for _, c := range sprint["cards"].([]any) {
		if cm := c.(map[string]any); cm["id"] == "c1" {
			quelle = cm
		}
	}
	activity := quelle["activity"].([]any)
	meta := activity[0].(map[string]any)["meta"].(map[string]any)
	note, _ := meta["contextNote"].(string)
	if !strings.Contains(note, "bild.png") {
		t.Fatalf("contextNote nennt den Anhang nicht: %q", note)
	}
}

// Der Rahmen muss auf dem Weg ankommen, den die Daten im Betrieb nehmen —
// nicht nur dort, wo der Unit-Test die Funktion direkt aufruft (F-856,
// Teil 1). Und er gilt nur für den Erfolgsfall: Ablehnungsgründe sind
// serverseitiger Text, kein Fremdmaterial. Wären sie mitgerahmt, träfe die
// Kennzeichnung auf alles zu und sagte nichts mehr.
//
// Die Markerform steht hier absichtlich als Literal und nicht als Konstante
// aus dem llm-Paket: Geprüft wird das Format, das den Provider tatsächlich
// erreicht — ein Test, der die Konstante gegen sich selbst hält, hielte auch
// dann noch, wenn der Rahmen ganz woanders verloren ginge.
func TestFetchLoop_WerkzeugAntwortIstAlsFremddatenGerahmt(t *testing.T) {
	const anfangsMarker = "[[FREMDDATEN "

	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}}, // verknüpft → Erfolg
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-2", "c3")}}, // unverknüpft → Ablehnung
		{text: "Fertig."},
	})
	env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	if got := env.provider.anzahlAufrufe(); got != 3 {
		t.Fatalf("%d Provider-Aufrufe, erwartet 3", got)
	}

	// Runde 2 trägt die erfolgreiche Antwort — gerahmt, mit Inhalt im Block.
	var sahGerahmt bool
	for _, h := range env.provider.anfrage(1).History {
		for _, tr := range h.ToolResults {
			if tr.IsError {
				t.Fatalf("erfolgreicher Abruf als Fehler markiert: %+v", tr)
			}
			// `Contains`, nicht `HasPrefix`: Seit dem Sprint-61-Kritiker steht der
			// Vorspann VOR dem Anfangsmarker — er darf nicht in dem Bereich
			// liegen, den er selbst zu Fremdmaterial erklärt.
			if !strings.Contains(tr.Content, anfangsMarker) {
				t.Fatalf("Werkzeug-Antwort ist ungerahmt beim Provider angekommen:\n%s", tr.Content)
			}
			if !strings.Contains(tr.Content, "NACHBARTEXT") {
				t.Fatalf("Karteninhalt fehlt in der gerahmten Antwort:\n%s", tr.Content)
			}
			sahGerahmt = true
		}
	}
	if !sahGerahmt {
		t.Fatal("in Runde 2 lag gar keine Werkzeug-Antwort — der Test prueft dann nichts")
	}

	// Runde 3 trägt die Ablehnung — ungerahmt.
	var sahAblehnung bool
	for _, h := range env.provider.anfrage(2).History {
		for _, tr := range h.ToolResults {
			if !tr.IsError {
				continue
			}
			if strings.Contains(tr.Content, anfangsMarker) {
				t.Fatalf("Ablehnungsgrund wurde als Fremddaten gerahmt:\n%s", tr.Content)
			}
			sahAblehnung = true
		}
	}
	if !sahAblehnung {
		t.Fatal("in Runde 3 lag keine Ablehnung vor — der Test prueft dann nichts")
	}
}

// F-3102-Nacharbeit (Sitzungs-Kritik 23.11. B1): Die Staffel gilt je Anfrage.
// Zwei Runden à 60.000 Prompt-Tokens liegen einzeln unter der Schwelle von
// 100.000 — die Summe (120.000) darf NICHT den hohen Satz auslösen.
func TestFetchLoop_StaffelpreisJeRundeNichtFuerDieSumme(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{text: "Ich sehe nach. ", toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}, eingabe: 60000, ausgabe: 100},
		{text: "Fertig.", eingabe: 60000, ausgabe: 100},
	})
	env.provider.inPM, env.provider.outPM = 0.10, 0.50
	env.provider.staffelAb, env.provider.staffelIn, env.provider.staffelOut = 100000, 0.50, 2.50
	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	done := ereignisse(evs, "done")
	if len(done) != 1 {
		t.Fatalf("kein done-Ereignis: %+v", evs)
	}
	if in, _ := done[0]["inputTokens"].(float64); in != 120000 {
		t.Fatalf("inputTokens = %v, erwartet 120000", done[0]["inputTokens"])
	}
	je := (60000.0/1_000_000)*0.10 + (100.0/1_000_000)*0.50
	want := 2 * je
	got, _ := done[0]["costEstimateUSD"].(float64)
	if got < want*0.999 || got > want*1.001 {
		t.Fatalf("costEstimateUSD = %v, erwartet %v (Grundsatz je Runde; mit der Summe wären es %v)",
			got, want, (120000.0/1_000_000)*0.50+(200.0/1_000_000)*2.50)
	}
}
