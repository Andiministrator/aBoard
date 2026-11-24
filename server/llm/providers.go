// Package llm — Provider-Strategy für LLM-API-Integration (#298 + #299, ADR-0094/0095).
//
// Zwei Pfade pro Provider:
//   - TestKey(ctx, key) error — Probe-Call zur Validierung eines API-Keys (#298).
//   - Send(ctx, key, req) (SendResult, error) — eigentlicher LLM-Call (#299,
//     ADR-0095). System-Prompt + User-Prompt → Provider-Antwort + Token-Counts.
//
// Strategy-Registry: neue Provider werden per Register(name, impl) im
// Init-Pfad eingehängt. Vier MVP-Provider: anthropic, openai, google,
// mistral. Provider-Namen sind klein/kebab-case und müssen mit dem
// CHECK-Constraint in der llm_api_keys-Tabelle (Migration 0006)
// übereinstimmen.

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Andiministrator/aBoard/server/ausgang"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// SendRequest kapselt einen LLM-Send-Call gegen einen Provider (ADR-0095 §1).
// History ist optional und kam mit ADR-0096 Etappe 2 für Multi-Turn dazu —
// bei leerem Slice bleibt das Verhalten identisch zum klassischen #299-Pfad.
type SendRequest struct {
	Model      string           // Provider-spezifischer Modellname (z. B. "claude-opus-4-7")
	System     string           // System-Prompt (kann leer sein → Default-Provider-System)
	History    []HistoryMessage // vorhergehende Turns (älteste zuerst); ADR-0096 §2 Multi-Turn
	UserPrompt string           // aktueller User-Turn (Karten-Kontext oder Folge-Frage)
	MaxTokens  int              // optional; 0 → Provider-Default (sinnvoll: 4096 für die meisten)
	// EnableMutations (ADR-0096 §3, Etappe 4) — wenn true, schickt der Provider
	// die MutationTools mit; tool_use-Blöcke im Stream werden geparst und als
	// ToolCallChunk an onChunk gegeben. Default false (klassischer Send-Pfad).
	EnableMutations bool
	// EnableFetch (ADR-0120, #428) — wenn true, kommt das Lese-Werkzeug
	// fetch-related-card dazu. Anders als bei EnableMutations beantwortet der
	// Handler dessen Aufrufe selbst und ruft den Provider erneut auf; die
	// Werkzeug-Runden reisen dabei über History.ToolUses/ToolResults.
	EnableFetch bool
	// Images (ADR-0121, #429) — Bilder, die mit dem AKTUELLEN User-Turn
	// reisen. Der Handler hat sie aus dem Attachment-Store geladen und
	// geprüft; die Provider kodieren sie in ihr jeweiliges Schema.
	//
	// Wandert der User-Turn im Fetch-Loop in die History, wandern die Bilder
	// mit (HistoryMessage.Images) — sonst sähe das Modell sie ab der zweiten
	// Runde nicht mehr, mitten im selben Turn.
	Images []ImageAttachment
	// User (Karte #664, ADR-0133 §2) — E-Mail der Person, die den Send
	// auslöst. Nur im Firmenmodus gesetzt; der Anbieter „openai“ schickt sie
	// als OpenAI-`user`-Feld mit — nur fürs Protokoll beim Anbieter, keine
	// Berechtigung. Leer → das Feld fehlt im Body (bit-identisch zu vorher).
	// Die übrigen Anbieter ignorieren es.
	User string
}

// HistoryMessage — eine Nachricht in der Provider-History. Zwei Herkünfte:
// persistierte Turns einer Multi-Turn-Conversation (ADR-0096 §2) und die
// Zwischenrunden des agentischen Fetch-Loops (ADR-0120 §5).
//
// Role ist 'user' oder 'assistant'; Content ist der Text. ToolUses und
// ToolResults sind optional und tragen die Werkzeug-Runden:
//
//   - assistant-Nachricht mit ToolUses:    das Modell hat Werkzeuge gerufen
//   - user-Nachricht mit ToolResults:      unsere Antworten darauf
//
// Sind beide leer, serialisiert jeder Provider die Nachricht BIT-IDENTISCH
// zum Stand vor #428 — das ist die Bedingung dafür, dass der Bestandspfad
// unberührt bleibt, und wird pro Provider getestet.
type HistoryMessage struct {
	Role        string // 'user' | 'assistant'
	Content     string
	ToolUses    []ToolUseBlock    // nur bei Role=='assistant'
	ToolResults []ToolResultBlock // nur bei Role=='user'
	Images      []ImageAttachment // nur bei Role=='user' (ADR-0121)
}

// ToolUseBlock — ein Werkzeug-Aufruf des Modells, so wie er in die nächste
// Anfrage zurückgespiegelt werden muss. Anthropic verlangt das zwingend: auf
// ein stop_reason "tool_use" MUSS die assistant-Nachricht mit demselben
// tool_use-Block und danach ein tool_result mit derselben id folgen, sonst
// lehnt die API die Anfrage ab.
type ToolUseBlock struct {
	ID   string          // Provider-Tool-Call-id
	Name string          // Werkzeug-Name
	Args json.RawMessage // Argumente, wie vom Provider geliefert
}

// ToolResultBlock — unsere Antwort auf einen ToolUseBlock. ID muss der ID des
// Aufrufs entsprechen. IsError markiert Ablehnungen (fehlende Berechtigung,
// Budget erschöpft) — die Provider kennzeichnen das im eigenen Schema, damit
// das Modell die Ablehnung als solche liest und nicht als Karteninhalt.
type ToolResultBlock struct {
	ID      string
	Name    string // für Google nötig (functionResponse ist namensbasiert, nicht id-basiert)
	Content string
	IsError bool
}

// SendResult bündelt die Provider-Antwort + Audit-Daten (ADR-0095 §1, §4, §7).
// InputTokens/OutputTokens sind die TATSÄCHLICHEN Werte aus der Provider-Antwort
// (für das Activity-Log + Real-Cost), nicht die Pre-Send-Heuristik aus §6.
type SendResult struct {
	Text         string // Provider-Antwort (Markdown, vor Sanitierung)
	InputTokens  int
	OutputTokens int
	Model        string // gespiegelt für Audit (= req.Model)
	FinishReason string // "stop" | "length" | provider-spezifisch
}

// ErrInvalidKey signalisiert, dass der Provider den Key abgelehnt hat
// (401/403). NICHT für Netzwerk-/Timeout-Fehler.
var ErrInvalidKey = errors.New("llm: api key invalid")

// ErrProviderUnreachable signalisiert Netzwerk-/Timeout-Fehler oder
// 5xx-Antworten vom Provider. Hier ist KEINE Aussage über die Key-Gültigkeit
// möglich — Caller sollte später erneut testen.
var ErrProviderUnreachable = errors.New("llm: provider unreachable")

// ErrUnknownProvider signalisiert einen nicht-registrierten Provider-Namen.
var ErrUnknownProvider = errors.New("llm: unknown provider")

// ErrStreamNotSupported signalisiert, dass der angefragte Provider keine
// Streaming-Variante implementiert (#300, ADR-0096). Caller (z. B.
// Stream-Handler) sollten den Send-Pfad als Fallback nicht selbst aufrufen,
// sondern strukturiert mit "stream-not-supported" antworten — der Stream-
// Vertrag (SSE) erlaubt kein klassisches Body-Response.
var ErrStreamNotSupported = errors.New("llm: provider does not support streaming")

// StreamChunk ist ein einzelner Token-/Tool-Call-Block aus einem
// SendStream-Lauf (ADR-0096 §1). Text und ToolCall sind orthogonal —
// Anthropic z. B. emittet erst Text-Deltas und kann später einen
// tool_use-Block einschieben. Etappe 1 nutzt nur Text; Tool-Calls
// folgen in Etappe 4.
type StreamChunk struct {
	Text     string         // inkrementeller Text-Token (kann "" sein bei Tool-only-Chunks)
	ToolCall *ToolCallChunk // optional — Etappe 4 (Mutations-Vorschlag)
}

// ToolCallChunk — vom LLM vorgeschlagene Mutation. Schema-Validierung +
// Blocklist greifen erst nach dem Streaming im Handler (ADR-0096 §4 L1/L3).
// Etappe 1 lässt das Feld bewusst ungenutzt — Etappe 4 füllt es.
type ToolCallChunk struct {
	ID   string          // Provider-spezifische Tool-Call-ID (z. B. Anthropic "toolu_…")
	Name string          // interner Mutations-Name (set-field, move, add-comment, ...)
	Args json.RawMessage // unverändert vom Provider; Validierung im Handler
}

// StreamingProvider ist ein optionales Capability-Interface — Provider, die
// Streaming-APIs unterstützen, implementieren es zusätzlich zu Provider.
// Etappe 1 (#300): nur AnthropicProvider. Etappe 2: die übrigen drei.
// Server-Handler nutzt `if streamer, ok := provider.(StreamingProvider); ok {...}`.
type StreamingProvider interface {
	// SendStream führt einen Streaming-Send-Call aus. onChunk wird für jeden
	// Text-/Tool-Call-Delta synchron aufgerufen — Caller (Handler) schreibt
	// dort direkt SSE-Events auf die Connection. Returnwert ist das finale
	// Aggregat (Usage + FinishReason) nach Stream-Ende. Errors-Klassifikation
	// identisch zu Send (ErrInvalidKey/ErrProviderUnreachable). Wenn onChunk
	// einen Fehler liefert (z. B. Client-Abbruch via Context), bricht der
	// Stream ab und gibt den Fehler weiter — kein Body-Cap-Check im Provider
	// (das macht der Handler, der den Gesamt-Body akkumuliert).
	SendStream(ctx context.Context, key string, req SendRequest, onChunk func(StreamChunk) error) (SendResult, error)
}

// Provider ist die Strategie-Schnittstelle für einen LLM-Anbieter.
// Goroutine-sicher: Implementierungen halten höchstens stateless-Client-Konfig
// (HTTP-Client, User-Agent), keinen request-spezifischen State.
type Provider interface {
	// Name liefert den kanonischen Provider-Namen (kebab-case, lowercase).
	Name() string
	// TestKey macht einen minimalen Probe-Call. Erfolgreich (return nil)
	// nur bei 200/2xx; 401/403 → ErrInvalidKey; Netzwerk/Timeout/5xx →
	// ErrProviderUnreachable.
	TestKey(ctx context.Context, key string) error
	// Send führt den eigentlichen LLM-Call aus (#299, ADR-0095).
	// Errors: ErrInvalidKey (401/403), ErrProviderUnreachable (Netzwerk/Timeout/5xx).
	Send(ctx context.Context, key string, req SendRequest) (SendResult, error)
	// Pricing liefert die USD/M-Token-Sätze für ein konkretes Modell.
	// Werte == 0 → Modell unbekannt; Caller zeigt UI-Hinweis "Preise unbekannt".
	Pricing(model string) (inputPerMillionUSD, outputPerMillionUSD float64)
}

// Registry bündelt alle bekannten Provider. Goroutine-sicher.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry legt eine leere Registry an. Der Default-Server registriert
// die 4 MVP-Provider via RegisterDefaults.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register hängt einen Provider unter seinem Namen ein. Überschreibt eine
// vorhandene Registrierung (idempotent für Re-Init-Tests).
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	r.providers[p.Name()] = p
	r.mu.Unlock()
}

// Get liefert den Provider zu name, oder ErrUnknownProvider.
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	p, ok := r.providers[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, name)
	}
	return p, nil
}

// List liefert die Namen aller registrierten Provider in stabiler (alphabetischer)
// Reihenfolge.
func (r *Registry) List() []string {
	r.mu.RLock()
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	r.mu.RUnlock()
	// Stable order erleichtert Frontend-Diffs + Test-Determinismus.
	sortStrings(names)
	return names
}

// RegisterDefaults registriert die 4 MVP-Provider mit Standard-Endpoints.
// userAgent identifiziert den aBoard-Server beim Provider (z. B.
// "aboard-server/v1.27.0") — hilfreich, wenn der Provider Telemetrie pro
// Client-App führt.
//
// openAIBaseURL (Karte #570, ADR-0129 §5) überschreibt die Basis des
// OpenAI-Providers, z. B. für ein lokales vLLM. Leer = Default
// (https://api.openai.com/v1) — Aufrufer geben hier das Ergebnis von
// ValidateOpenAIBaseURL durch, nicht den rohen Env-Wert.
func RegisterDefaults(r *Registry, userAgent string, openAIBaseURL string) {
	client := defaultClient()
	openAIKompatibel.Store(openAIBaseURL != "")
	r.Register(&AnthropicProvider{Client: client, UA: userAgent})
	r.Register(&OpenAIProvider{Client: client, UA: userAgent, BaseURL: openAIBaseURL})
	r.Register(&GoogleProvider{Client: client, UA: userAgent})
	r.Register(&MistralProvider{Client: client, UA: userAgent})
}

// ProbeTimeout ist die GESAMTFRIST eines TestKey-Probe-Calls (#298). Anders
// als früher (F-1126) sitzt sie NICHT mehr als Client.Timeout auf dem
// geteilten HTTP-Client, sondern wird von jeder TestKey-Implementierung
// selbst per context.WithTimeout(ctx, ProbeTimeout) gesetzt — ein Probe-Call
// ist kurz und klein, eine eigene Gesamtfrist dafür ist also unproblematisch
// (anders als bei einem Stream, siehe defaultClient()).
// 10s = großzügig genug für Anthropic-/OpenAI-Cold-Starts, knapp genug um
// Frontend-User nicht endlos warten zu lassen (Wortlaut aus der bisherigen
// defaultClient()-Doku übernommen).
//
// var statt const (F-1143, G5): Tests verkürzen die Frist auf Millisekunden,
// um zu beweisen, dass alle vier Anbieter tatsächlich per
// context.WithTimeout(ctx, ProbeTimeout) abbrechen, statt an einem
// unbegrenzten Eltern-Kontext (wie ihn r.Context() ohne eigene Deadline
// liefert) endlos zu hängen — siehe providers_probetimeout_test.go.
var ProbeTimeout = 10 * time.Second

// AntwortHeaderDeckel begrenzt die Wartezeit bis zu den Antwort-Headern —
// für JEDEN Aufruf über defaultClient, auch für einen, der sonst keine
// eigene Frist setzt. Er ist bewusst ein grosszügiger Deckel, keine
// Betriebsfrist:
//   - Send() ohne Streaming: Anthropic und OpenAI schicken die Header erst,
//     wenn die ganze Antwort erzeugt ist. Bis Sprint 70 stand hier 20 s —
//     damit brach jede Antwort, die länger als 20 s brauchte, mit „timeout
//     awaiting response headers“ ab, und die 2-Minuten-Frist des
//     Send-Handlers (api.llmSendTimeout) griff nie (Kritiker Sprint 70,
//     K-A4). Deshalb muss der Deckel ÜBER dieser Frist liegen; ein Test im
//     api-Paket hält das fest.
//   - SendStream: Der Request hängt am Kontext von streamIdleGuard, der nach
//     streamIdleTimeout Stille abbricht — auch schon vor den Headern.
//   - TestKey: eigene Gesamtfrist ProbeTimeout per context.WithTimeout.
//
// Karte #665 (ADR-0133 §3): Der Deckel liegt jetzt ÜBER streamErstbyteTimeout
// (300 s). Davor stand er bei 3 min und brach ein lokales Modell mit langer
// Denkphase ab, bevor die Erstbyte-Frist überhaupt greifen konnte — ein Stream
// wartet auf die Header, nicht auf das erste Wort. Ein Test hält die
// Reihenfolge fest (providers_fristen_test.go).
const AntwortHeaderDeckel = 330 * time.Second

// defaultClient liefert den EINEN HTTP-Client, den alle Provider für
// TestKey/Send/SendStream verwenden (RegisterDefaults reicht ihn an alle
// vier Provider durch).
//
// F-1126 (#544): Bis hierhin trug dieser Client ein Client.Timeout von 10s.
// Laut net/http-Doku ist Client.Timeout eine GESAMTFRIST, die "includes
// ... reading the response body" — für einen SSE-Stream, der Minuten laufen
// darf, genau der falsche Deckel: jede Antwort, die länger als 10s zum
// vollständigen Lesen brauchte, wurde MITTEN IM BODY abgeschnitten, auch
// wenn der Provider stetig weiter Tokens lieferte. Der Client hat deshalb
// KEIN Client.Timeout mehr. Stattdessen:
//   - Transport.ResponseHeaderTimeout begrenzt nur die Wartezeit bis zum
//     ersten Byte (siehe AntwortHeaderDeckel) — ein Provider, der nicht
//     antwortet, hängt nicht ewig.
//   - Eine tote, aber offen gebliebene Stream-Verbindung (Provider schweigt
//     minutenlang, schließt aber nicht) wird in SendStream über
//     streamIdleGuard abgefangen — NICHT hier, weil das eine Eigenschaft
//     des laufenden Reads ist, kein Client-weiter Wert.
//   - Wer trotzdem eine kurze GESAMTFRIST braucht (TestKey-Probe-Calls),
//     bekommt sie über einen eigenen context.WithTimeout(ctx, ProbeTimeout)
//     in der jeweiligen TestKey-Implementierung.
//
// Send() (#299, der klassische Nicht-Stream-Call) bekommt seine
// Gesamtfrist vom Aufrufer: Der Send-Handler setzt api.llmSendTimeout
// (2 min) per Kontext (#544-Nacharbeit, ea6ae94). Der AntwortHeaderDeckel
// liegt darüber, damit er diese Frist nicht unterläuft (K-A4).
func defaultClient() *http.Client {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		// Klonen statt eines leeren &http.Transport{} — sonst verlieren wir
		// Proxy-Handling aus der Umgebung, Connection-Pooling und
		// TLSHandshakeTimeout, die http.DefaultTransport für uns setzt.
		tr = tr.Clone()
	} else {
		// Defensiv: nur relevant, falls http.DefaultTransport in einem
		// Testkontext durch etwas anderes als *http.Transport ersetzt wurde.
		tr = &http.Transport{}
	}
	tr.ResponseHeaderTimeout = AntwortHeaderDeckel
	// Karte #569: Ausgangs-Schalter vor jeder Anfrage an einen Anbieter.
	return &http.Client{Transport: ausgang.Transport(tr)}
}

// streamIdleTimeout ist NICHT die Gesamtfrist eines Streams (die gibt es
// nach F-1126 bewusst nicht mehr) — sondern die maximale Stille ZWISCHEN
// zwei empfangenen SSE-Zeilen, ab dem ersten empfangenen Byte. Ohne diesen
// Deckel würde eine Verbindung, die der Provider offen lässt, aber nie mehr
// beliefert (kaputter Upstream, hängender Load-Balancer), die
// Handler-Goroutine und die Client-Verbindung für immer blockieren — das
// wäre der neue Leerlauf-Risikofall, den das entfernte Client.Timeout
// (ungewollt) mit-abgedeckt hatte.
// 90s liegt deutlich über jedem beobachteten Provider-Keep-Alive (Anthropic
// "ping"-Events typischerweise alle 10-30s) und schützt trotzdem zuverlässig
// gegen eine tote, aber offen gebliebene Verbindung.
//
// var statt const (F-1140): Tests verkürzen die Frist auf Millisekunden.
var streamIdleTimeout = 90 * time.Second

// streamErstbyteTimeout ist die Frist bis zum ERSTEN empfangenen SSE-Byte
// (F-1140). streamIdleGuard misst Stille ZWISCHEN Ereignissen — sein Timer
// läuft aber schon vor dem Request-Versand, und touch() (siehe Scan-Schleife
// in anthropic.go/openai.go/google.go) wird erst mit der ERSTEN SSE-Zeile
// aufgerufen. Vor F-1140 lief deshalb dieselbe 90s-Frist (streamIdleTimeout)
// auch für die Denkphase vor dem ersten Token — ein Modell, das vor dem
// ersten Token länger als 90s schweigt (z. B. ein lokales vLLM in einer
// langen Denkphase), wurde abgebrochen, obwohl der Provider noch arbeitet
// und die Verbindung nicht tot ist.
// 300s (5 min) ist grosszügig genug für lange Denkphasen, aber immer noch
// eine echte Obergrenze — ein Provider, der auch danach nichts liefert, gilt
// als hängend. Nach dem ersten Byte gilt wieder die kürzere
// streamIdleTimeout wie gewohnt.
//
// var statt const: Tests verkürzen die Frist auf Millisekunden.
var streamErstbyteTimeout = 300 * time.Second

// streamIdleGuard liefert einen von parent abgeleiteten Context, der
// abbricht, wenn touch() länger als idle nicht aufgerufen wird — unabhängig
// von der Gesamtlaufzeit. erstbyte gilt NUR für die Zeit bis zum ersten
// touch()-Aufruf (F-1140); jeder touch() danach setzt die Frist auf idle
// zurück, egal ob es der erste oder ein späterer war. Aufrufer: touch() bei
// jedem empfangenen SSE-Event (auch Keep-Alive/Ping) aufrufen, damit
// reguläre Stille zwischen Tokens den Guard nicht fälschlich auslöst.
// cancel() IMMER per defer aufrufen, auch im Erfolgsfall — sonst leakt die
// Watchdog-Goroutine.
func streamIdleGuard(parent context.Context, erstbyte, idle time.Duration) (ctx context.Context, touch func(), cancel func()) {
	ctx, cancelCtx := context.WithCancel(parent)
	reset := make(chan struct{}, 1)
	done := make(chan struct{})
	var closeOnce sync.Once

	go func() {
		// Erster Timer läuft mit der (in der Regel grosszügigeren)
		// Erstbyte-Frist — jeder darauffolgende reset (= touch()) setzt auf
		// die kürzere idle-Frist zurück (F-1140).
		timer := time.NewTimer(erstbyte)
		defer timer.Stop()
		for {
			select {
			case <-reset:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			case <-timer.C:
				cancelCtx()
				return
			case <-done:
				return
			}
		}
	}()

	touch = func() {
		select {
		case reset <- struct{}{}:
		default:
		}
	}
	cancel = func() {
		closeOnce.Do(func() { close(done) })
		cancelCtx()
	}
	return ctx, touch, cancel
}

// wrapNetErr macht aus einem Netzwerk-/Timeout-Fehler einen
// ErrProviderUnreachable. Aufrufer haben so keine Fall-Diskriminierung
// zwischen DNS-/Connect-/Read-Timeout zu pflegen.
func wrapNetErr(err error) error {
	return fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
}

// classifyHTTPStatus übersetzt eine Response in unsere ErrInvalidKey/
// ErrProviderUnreachable-Klassifikation.
func classifyHTTPStatus(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrInvalidKey
	default:
		return fmt.Errorf("%w: status %d", ErrProviderUnreachable, status)
	}
}

// ErrContextTooLong, ErrToolsUnsupported und ErrProviderRejected sind die
// Fehlerarten aus einer 4xx-Antwort des Anbieters (Karte #665, ADR-0133 §3).
// Bei einem lokalen Modell sind sie die häufigste Störung; früher erschienen
// sie alle als „Anbieter nicht erreichbar“, obwohl der Anbieter geantwortet hat.
var (
	// ErrContextTooLong: Eingabe länger als das Kontextfenster des Modells.
	ErrContextTooLong = errors.New("llm: context too long")
	// ErrToolsUnsupported: das Modell/der Server nimmt keine Werkzeuge an.
	ErrToolsUnsupported = errors.New("llm: tools not supported")
	// ErrProviderRejected: der Anbieter lehnt die Anfrage ab (übrige 4xx).
	ErrProviderRejected = errors.New("llm: provider rejected request")
)

// maxFehlerBodyBytes begrenzt, wie viel einer Fehlerantwort gelesen wird.
const maxFehlerBodyBytes = 4096

// maxAnbieterMeldungRunen kürzt die Meldung des Anbieters, bevor sie bis zum
// Browser läuft.
const maxAnbieterMeldungRunen = 300

// ProviderFehler trägt Art, HTTP-Status und die gekürzte Meldung des
// Anbieters. errors.Is(err, ErrContextTooLong) usw. funktioniert über Unwrap.
type ProviderFehler struct {
	Art     error
	Status  int
	Meldung string
}

func (e *ProviderFehler) Error() string {
	return fmt.Sprintf("%v (status %d): %s", e.Art, e.Status, e.Meldung)
}

func (e *ProviderFehler) Unwrap() error { return e.Art }

// AnbieterMeldung liefert die gekürzte Meldung des Anbieters, wenn err ein
// ProviderFehler ist, sonst "".
func AnbieterMeldung(err error) string {
	var pf *ProviderFehler
	if errors.As(err, &pf) {
		return pf.Meldung
	}
	return ""
}

// classifyHTTPResponse wie classifyHTTPStatus, liest aber bei einem 4xx (außer
// 401/403/408/429) den Body und bestimmt die Fehlerart. Der Body wird auf
// maxFehlerBodyBytes begrenzt gelesen; die Meldung wird gekürzt und von
// Steuerzeichen befreit. 5xx, 408 und 429 bleiben „nicht erreichbar“.
func classifyHTTPResponse(res *http.Response, key string) error {
	st := res.StatusCode
	if st < 400 || st >= 500 || st == http.StatusUnauthorized || st == http.StatusForbidden ||
		st == http.StatusRequestTimeout || st == http.StatusTooManyRequests {
		return classifyHTTPStatus(st)
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, maxFehlerBodyBytes))
	meldung := schwaerzeSchluessel(anbieterMeldungAusBody(raw), key)
	return &ProviderFehler{Art: fehlerArtAusMeldung(st, meldung), Status: st, Meldung: meldung}
}

// bearerRe trifft einen zurückgespiegelten Authorization-Wert.
var bearerRe = regexp.MustCompile(`(?i)bearer\s+\S+`)

// schwaerzeSchluessel entfernt den verwendeten Schlüssel und jeden
// „Bearer …“-Wert aus der Meldung des Anbieters: Manche Gegenüber spiegeln den
// Authorization-Kopf in Fehlerantworten zurück, und die Meldung läuft bis in
// Browser und Log (Firmenmodus: der Serverschlüssel darf nirgends erscheinen).
func schwaerzeSchluessel(meldung, key string) string {
	// Ab 8 Zeichen: ein kürzerer „Schlüssel“ (Test, vLLM ohne --api-key) würde
	// beliebige Wortteile der Meldung zerstören und schützt nichts.
	if key = strings.TrimSpace(key); len(key) >= 8 {
		meldung = strings.ReplaceAll(meldung, key, "[Schlüssel]")
	}
	return bearerRe.ReplaceAllString(meldung, "Bearer [Schlüssel]")
}

// anbieterMeldungAusBody holt die lesbare Meldung aus einem Fehler-Body:
// OpenAI/vLLM {"error":{"message":…}} oder {"error":"…"}, FastAPI/OWUI
// {"detail":"…"} und {"detail":[{"loc":…,"msg":…,"input":…}]} (pydantic), {"message":…};
// ist der Body KEIN JSON, der rohe Text. K3-8: Ist er JSON, aber ohne lesbare Meldung,
// wird NIE der Rohtext verwendet — pydantic-Einträge tragen `input` (die abgelehnte
// Eingabe = womöglich Karteninhalt), und die Meldung landet in Antwort und Log.
func anbieterMeldungAusBody(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if !json.Valid(raw) {
		return kuerzeMeldung(text)
	}
	const ohneMeldung = "Fehlerantwort des Anbieters ohne lesbare Meldung"
	// V2-3: Felder EINZELN lesen — ein Feld mit unerwartetem Typ (Mistral:
	// `message` als Objekt) darf `error`/`detail` nicht mit verwerfen.
	var feld map[string]json.RawMessage
	if json.Unmarshal(raw, &feld) != nil { // gültiges JSON, aber kein Objekt
		return ohneMeldung
	}
	for _, k := range []string{"error", "detail", "message"} {
		if m := meldungAusWert(feld[k], 0); m != "" {
			return kuerzeMeldung(m)
		}
	}
	return ohneMeldung
}

// meldungAusWert liest eine lesbare Meldung aus einem JSON-Wert: Text; Objekt mit
// `message`/`detail`/`error` (auch verschachtelt, Tiefe begrenzt); pydantic-Liste
// von {loc, msg, …} — dort NUR loc und msg, nie `input` (die abgelehnte Eingabe,
// womöglich Karteninhalt). Leer = nichts Lesbares.
func meldungAusWert(roh json.RawMessage, tiefe int) string {
	if len(roh) == 0 || tiefe > 3 {
		return ""
	}
	var s string
	if json.Unmarshal(roh, &s) == nil {
		return strings.TrimSpace(s)
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(roh, &obj) == nil {
		for _, k := range []string{"message", "detail", "error"} {
			if m := meldungAusWert(obj[k], tiefe+1); m != "" {
				return m
			}
		}
		return ""
	}
	var liste []struct {
		Loc []json.RawMessage `json:"loc"`
		Msg string            `json:"msg"`
	}
	if json.Unmarshal(roh, &liste) == nil {
		teile := make([]string, 0, len(liste))
		for _, e := range liste {
			if e.Msg == "" {
				continue
			}
			var pfad []string
			for _, l := range e.Loc {
				var ls string
				if json.Unmarshal(l, &ls) == nil {
					pfad = append(pfad, ls)
				} else {
					pfad = append(pfad, strings.Trim(string(l), `"`)) // Index (Zahl)
				}
			}
			if len(pfad) > 0 {
				teile = append(teile, strings.Join(pfad, ".")+": "+e.Msg)
			} else {
				teile = append(teile, e.Msg)
			}
		}
		return strings.Join(teile, "; ")
	}
	return ""
}

// kuerzeMeldung entfernt Steuerzeichen, fasst Leerraum zusammen und kürzt auf
// maxAnbieterMeldungRunen Zeichen.
func kuerzeMeldung(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxAnbieterMeldungRunen {
		s = string(r[:maxAnbieterMeldungRunen]) + "…"
	}
	return s
}

// fehlerArtAusMeldung ordnet eine 4xx-Antwort einer Fehlerart zu. Die Wörter
// stammen aus den Meldungen von vLLM, OpenAI und Open WebUI; Kontext hat
// Vorrang, weil „maximum context length … tools“ einer langen Eingabe gilt.
func fehlerArtAusMeldung(status int, meldung string) error {
	m := strings.ToLower(meldung)
	if status == http.StatusRequestEntityTooLarge || strings.Contains(m, "context length") ||
		strings.Contains(m, "context_length") || strings.Contains(m, "context window") ||
		strings.Contains(m, "maximum context") || strings.Contains(m, "too many tokens") ||
		strings.Contains(m, "prompt is too long") || strings.Contains(m, "input is too long") ||
		strings.Contains(m, "reduce the length") || strings.Contains(m, "maximum input length") {
		return ErrContextTooLong
	}
	for _, w := range []string{"tool choice", "tool_choice", "tool-choice", "tools", "tool call", "tool_call", "function call"} {
		if strings.Contains(m, w) {
			return ErrToolsUnsupported
		}
	}
	return ErrProviderRejected
}

// sortStrings ist ein lokaler Sort-Wrap, um die sort-Import in der
// Hauptdatei zu vermeiden (klein, häufig benutzt).
func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j-1] > ss[j]; j-- {
			ss[j-1], ss[j] = ss[j], ss[j-1]
		}
	}
}
