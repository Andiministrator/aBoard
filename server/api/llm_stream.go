package api

// llm_stream.go — POST /boards/{b}/cards/{c}/llm-send-stream (#300, ADR-0096
// Etappe 1).
//
// SSE-Endpoint analog llm_send.go (#299, ADR-0095), aber Token-Streaming:
// Provider → SSE-Events → Client. Persistierung des finalen Kommentars +
// Activity-Eintrags geschieht NACH Stream-Ende (vermeidet halbe Kommentare
// bei Cancel/Abort).
//
// Etappe 1 Scope:
//   - Provider-Capability-Check (StreamingProvider) — nur Anthropic
//     liefert in Etappe 1, andere geben "stream-not-supported".
//   - SSE-Events: token / done / error. Tool-Use-Events folgen in Etappe 4.
//   - F-398: userPromptOverride wird akzeptiert + im contextNote markiert
//     (Wortlaut identisch zu llm_send.go:255-258, F-392-Muster).
//   - Reply-Body-Cap (MaxLLMReplyBodyBytes) als Defense-in-depth während des
//     Streamings — bei Überschreitung "too-large"-Error + Stream-Ende ohne
//     Persistierung.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// ContextBudgetFraction — Anteil des Provider-Context-Limits, ab dem das
// Budget-Gate (#300, ADR-0096 §2) den nächsten Multi-Turn-Send mit 400
// kind="context-budget" ablehnt. 0.8 = 80 %; lässt Spielraum für Output-
// Tokens (Provider rechnet Input+Output gegen den Context-Window).
const ContextBudgetFraction = 0.8

// llmStreamRequest spiegelt llmSendRequest (llm_send.go:86) bewusst. Etappe 2
// (#300, ADR-0096 §2) ergänzt das optionale `conversationId` für Multi-Turn —
// wenn gesetzt, lädt der Server die Turns der Conversation als History,
// prüft das Token-Budget gegen den Provider-Context-Limit und persistiert
// User- und Assistant-Turn nach Stream-Ende.
type llmStreamRequest struct {
	Provider           string   `json:"provider"`
	Model              string   `json:"model"`
	SystemPrompt       string   `json:"systemPrompt"`
	IncludeFields      []string `json:"includeFields"`
	MaxTokens          int      `json:"maxTokens"` // 0 → SendDefaultMaxTokens
	UserPromptOverride string   `json:"userPromptOverride,omitempty"`
	ConversationID     string   `json:"conversationId,omitempty"`
	// EnableMutations (ADR-0096 §3, Etappe 4) — wenn true, schickt der Server
	// die MutationTools mit (Tool-Use), parsed Tool-Call-Blöcke aus dem
	// Provider-Stream und leitet validierte Vorschläge als event:tool-call
	// an den Client. Default false.
	EnableMutations bool `json:"enableMutations,omitempty"`
	// EnableFetch (ADR-0120, #428) — wenn true, bekommt das Modell das
	// Lese-Werkzeug fetch-related-card. Der Server beantwortet dessen Aufrufe
	// selbst und ruft den Provider erneut auf (agentischer Loop); der Client
	// sieht jeden Abruf als event:fetch. Default false.
	//
	// Der Wunsch allein genügt nicht: fetchVerfuegbar entscheidet
	// (kein ACL-Repo → nein; gesetzter userPromptOverride → nein, ADR-0120 §4).
	EnableFetch bool `json:"enableFetch,omitempty"`
	// AttachmentIDs (ADR-0121, #429) — Anhänge dieser Karte, die als Bilder
	// mitgeschickt werden sollen. Der Nutzer wählt sie im KI-Tab aus; nur
	// die ids reisen, die Bytes lädt der Server aus dem Attachment-Store.
	AttachmentIDs []string `json:"attachmentIds,omitempty"`
	// PiiFilterActive (ADR-0120 §4, korrigiert nach dem Kritiker-Lauf) — der
	// Client meldet, ob der PII-Filter für diesen Send AKTIV war. Nicht, ob
	// er etwas gefunden hat.
	//
	// Warum das ein eigenes Feld braucht: Vorher diente `userPromptOverride`
	// als Stellvertreter. Der entsteht aber nur, wenn der Filter Treffer
	// hatte UND der Nutzer das Modal durchlaufen ist. Bei aktivem Filter
	// ohne Treffer war der Stellvertreter leer — und das Nachladen lief,
	// obwohl der Filter an war. Der Stellvertreter beantwortete „wurde
	// redigiert?", die Zusage lautet aber „ist der Filter aktiv?".
	PiiFilterActive bool `json:"piiFilterActive,omitempty"`
}

// RegisterStream hängt die Stream-Route ein. Wird vom selben LLMSendAPI-Mux
// aufgerufen (siehe LLMSendAPI.Register Erweiterung).
func (a *LLMSendAPI) registerStream(mux *http.ServeMux) {
	mux.Handle("POST /boards/{boardId}/cards/{cardId}/llm-send-stream",
		WithDefaults(http.HandlerFunc(a.handleSendStream)))
}

// sseWriter kapselt das SSE-Write-Protokoll. `event:` und `data:` werden in
// JSON-Form auf die Connection geschrieben, anschließend Flush. Wenn der
// http.Flusher fehlt (z. B. HTTP/2-Pseudo-Stream), wird trotzdem geschrieben —
// die meisten Reverse-Proxies puffern dann bis zum nächsten Chunk.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	frist   *rollendeFrist // K-A3: Schreibfrist je Event, nil = Server-Frist

	// mu serialisiert alle Schreibvorgänge: Seit Karte #665 schreibt neben dem
	// Handler auch die Lebenszeichen-Goroutine (siehe starteLebenszeichen).
	mu        sync.Mutex
	letzteAus time.Time // Zeitpunkt des letzten Schreibens, für das Lebenszeichen
}

// llmStreamLebenszeichen ist der Abstand der SSE-Kommentare, solange der
// Anbieter schweigt (Karte #665, ADR-0133 §3). Wie heartbeatInterval
// (events.go) 25 s — unter den üblichen 30–60 s Leerlauf-Fristen von Proxys.
// var statt const: Tests verkürzen ihn.
var llmStreamLebenszeichen = 25 * time.Second

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	fl, _ := w.(http.Flusher)
	return &sseWriter{w: w, flusher: fl}
}

func (s *sseWriter) writeEvent(event string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sse marshal: %w", err)
	}
	return s.schreibe(fmt.Sprintf("event: %s\ndata: %s\n\n", event, raw))
}

// schreibe schreibt einen fertigen SSE-Block unter dem Schreibschloss.
func (s *sseWriter) schreibe(block string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frist != nil {
		s.frist.verlaengern()
	}
	if _, err := io.WriteString(s.w, block); err != nil {
		return err
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	s.letzteAus = time.Now()
	return nil
}

// starteLebenszeichen schreibt einen SSE-Kommentar (`: …`), sobald seit dem
// letzten Schreiben `abstand` vergangen ist — die größte Stille ist damit
// höchstens `abstand` (K3-1). Browser-
// Parser und Proxys ignorieren Kommentare; sie halten die Verbindung offen,
// solange das Modell vor dem ersten Wort denkt (Karte #665, ADR-0133 §3; bei
// einem Team-Server ein 504 unter Parallellast). Die zurückgegebene Funktion beendet die
// Goroutine und WARTET auf sie — nach dem Aufruf schreibt sie nie mehr.
// Der Kommentar verlängert wie jedes Schreiben die Schreibfrist.
func (s *sseWriter) starteLebenszeichen(abstand time.Duration) (stopp func()) {
	halt := make(chan struct{})
	ende := make(chan struct{})
	go func() {
		defer close(ende)
		// K3-1: Der Timer läuft ab dem LETZTEN Schreiben, nicht im festen Takt — ein
		// Ticker mit Schwelle ließ bis zu knapp 2 × abstand Stille zu (Wort kurz nach dem
		// Tick → nächster Tick noch zu früh → erst der übernächste schreibt).
		// Größte Lücke damit ≈ abstand (plus Zeitgeber-Ungenauigkeit).
		t := time.NewTimer(abstand)
		defer t.Stop()
		for {
			select {
			case <-halt:
				return
			case <-t.C:
				s.mu.Lock()
				rest := abstand - time.Since(s.letzteAus)
				s.mu.Unlock()
				if rest > 0 {
					t.Reset(rest) // zwischenzeitlich geschrieben: ab dort neu zählen
					continue
				}
				if err := s.schreibe(": lebenszeichen\n\n"); err != nil {
					return // Verbindung tot — der Handler bemerkt es beim nächsten Event
				}
				t.Reset(abstand)
			}
		}
	}()
	var einmal sync.Once
	return func() {
		einmal.Do(func() { close(halt) })
		<-ende
	}
}

// writeStreamError schreibt einen strukturierten Fehler als SSE-Event und
// flusht. Wird nach Stream-Start verwendet (vor Stream-Start nutzen wir
// writeSendError für JSON-Antworten — Vertrag mit Frontend §1 in ADR-0096).
// Siehe writeSendError (llm_send.go) zur Begruendung des err-Parameters.
func writeSSEError(s *sseWriter, kind, msg string, err ...error) {
	if len(err) > 0 && err[0] != nil {
		log.Printf("aboard-server: sse %s/%s: %s", kind, msg, redactForLog(err[0]))
	}
	_ = s.writeEvent("error", llmSendErrorResponse{Kind: kind, Message: msg})
}

func (a *LLMSendAPI) handleSendStream(w http.ResponseWriter, r *http.Request) {
	// Im Firmenmodus braucht der Send kein Schlüssel-Repo (Serverschlüssel).
	if a.keys == nil && !a.firmenmodus.Aktiv() {
		writeSendError(w, http.StatusServiceUnavailable, "internal", "llm-send-stream disabled: configure ABOARD_SECRET_KEY")
		return
	}
	id := IdentityFromContext(r.Context())
	if id == nil {
		writeSendError(w, http.StatusForbidden, "internal", "forbidden: authentication required")
		return
	}
	email := storage.NormalizeEmail(id.Email)
	if email == "" {
		writeSendError(w, http.StatusForbidden, "internal", "forbidden: no identity email")
		return
	}
	u, err := a.users.GetByEmail(r.Context(), email)
	if err != nil {
		writeSendError(w, http.StatusForbidden, "internal", "forbidden: user not provisioned")
		return
	}

	boardID := r.PathValue("boardId")
	cardID := r.PathValue("cardId")
	if boardID == "" || cardID == "" {
		writeSendError(w, http.StatusBadRequest, "internal", "board/card id required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxLLMSendRequestBytes)
	var req llmStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSendError(w, http.StatusBadRequest, "internal", "invalid json body")
		return
	}
	if req.Provider == "" || req.Model == "" {
		writeSendError(w, http.StatusBadRequest, "internal", "provider/model required")
		return
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = SendDefaultMaxTokens
	}
	// Karte #664: Modell-Allowlist und PII-Pflicht des Betreibers — vor allem
	// anderen, was Kosten, Sperren oder Anbieter-Verkehr erzeugt.
	if !a.pruefeBetreiberVorgaben(w, req.Model, req.PiiFilterActive) {
		return
	}

	provider, err := a.registry.Get(req.Provider)
	if err != nil {
		writeSendError(w, http.StatusBadRequest, "internal", "unknown provider")
		return
	}
	streamer, ok := provider.(llm.StreamingProvider)
	if !ok {
		// Etappe 2: alle 4 MVP-Provider streamen — dieser Pfad bleibt als
		// Defense-in-depth, falls jemand einen neuen Provider registriert,
		// ohne SendStream zu implementieren.
		writeSendError(w, http.StatusBadRequest, "stream-not-supported", "provider does not support streaming yet")
		return
	}

	cardCtx, err := loadCardForLLM(r, a.boards, boardID, cardID)
	if err != nil {
		if errors.Is(err, errCardNotFound) {
			writeSendError(w, http.StatusNotFound, "internal", "card not found")
			return
		}
		writeSendError(w, http.StatusInternalServerError, "internal", "load card failed")
		return
	}

	// Bilder (ADR-0121): Capability zuerst, dann laden. Beides VOR dem
	// Key-Lookup und vor dem Lock — ein Send, der ohnehin nicht gehen kann,
	// soll weder eine Karte sperren noch beim Anbieter Kosten erzeugen.
	//
	// Die Reihenfolge ist Absicht: Steht „Modell kann keine Bilder" hinter
	// einer Grössenprüfung, verkleinert der Nutzer sein Bild und scheitert
	// danach am eigentlichen Grund.
	var bilder []llm.ImageAttachment
	if len(req.AttachmentIDs) > 0 {
		if !llm.SupportsVision(req.Provider, req.Model) {
			writeSendError(w, http.StatusBadRequest, "vision-not-supported",
				fmt.Sprintf("model %q cannot process images", req.Model))
			return
		}
		var grund string
		bilder, grund = a.ladeBilder(cardCtx.Card, boardID, cardID, email, req.AttachmentIDs)
		if grund != "" {
			writeSendError(w, http.StatusBadRequest, "attachment-rejected", grund)
			return
		}
	}

	// Im Firmenmodus der Serverschlüssel (Karte #664), sonst der des Nutzers.
	apiKey, ok := a.sendeSchluessel(w, r, u.ID, req.Provider)
	if !ok {
		return
	}

	// Lock acquire VOR Stream-Start — verhindert parallele Sends auf
	// derselbe Karte (analog ADR-0095 §5). Bei Konflikt 423 JSON-Antwort
	// (Stream noch nicht gestartet, Vertrag erlaubt JSON-Body). Schlüssel ist
	// Board + Karte (K2-3): Karten-IDs wiederholen sich nach einem Import.
	sperre := llm.SperrSchluessel(boardID, cardID)
	lauf, acquired, holder := a.locks.Acquire(sperre, u.ID, req.Provider, req.Model)
	if !acquired {
		writeJSON(w, http.StatusLocked, llmSendErrorResponse{
			Kind:    "locked",
			Message: "card is currently being processed by another send",
			// F-1553 (#682): im Firmenmodus Person und Modell nur für den Halter selbst.
			Holder: a.sperrHalter(holder, holder.User, holder.User == u.ID),
		})
		return
	}
	// F-1538: Freigabe nur mit der eigenen Lauf-Kennung — ein früherer Lauf
	// desselben Nutzers kann so nicht die Sperre dieses Laufs löschen. Die
	// Sperre wird für die ganze Laufzeit des Streams aufgefrischt; stopHalten
	// läuft (LIFO) vor Release.
	defer a.locks.Release(sperre, lauf)
	stopHalten := a.locks.Halten(sperre, lauf)
	defer stopHalten()

	// Ob das Lese-Werkzeug mitgeschickt wird — nicht der Wunsch des Clients
	// entscheidet das, sondern fetchVerfuegbar (ADR-0120 §4). Das muss VOR
	// dem Prompt-Bau feststehen: Bei aktivem Werkzeug trägt die
	// Verweis-Sektion die Karten-ids, und ohne sie kann das Modell das
	// Werkzeug nicht bedienen (es kennt die id nicht und kann sie nicht
	// raten — der Prompt nannte bis dahin nur `#displayId` und den Titel).
	// Karte #664: Die Betreiber-Vorgabe ABOARD_LLM_PII ist maßgeblich —
	// „pflicht“ gilt als aktiver Filter, „aus“ als inaktiver, nur bei
	// „nutzer“ zählt die Meldung des Clients.
	fetchAktiv := a.fetchVerfuegbar(req.EnableFetch, req.UserPromptOverride, a.pii.Wirksam(req.PiiFilterActive))

	// F-398: userPromptOverride respektieren — Wortlaut + Marker identisch zu
	// llm_send.go:213-215 + 255-258. Sonst PII-Bypass (ADR-0096 §5).
	userPrompt := req.UserPromptOverride
	if userPrompt == "" {
		userPrompt = llm.BuildPromptOpts(cardCtx.Card, req.IncludeFields, cardCtx.RefsByID,
			llm.PromptOptions{MitKartenIDs: fetchAktiv})
	}

	// Multi-Turn-Pfad (ADR-0096 §2): bei vorhandener conversationId History
	// laden + Budget-Gate. Bei fehlender ID läuft alles wie Etappe 1
	// (Single-Turn-Stream).
	var (
		conv    *storage.LLMConversation
		history []llm.HistoryMessage
	)
	// Kontext-Limit und Schätzung des neuen Anteils dieses Turns (Prompt,
	// System-Prompt, Bilder). Limit 0 = unbekanntes Modell → kein Gate.
	//
	// F-1089: `bilder` ist an dieser Stelle bereits geladen (oben, vor dem
	// Key-Lookup) und geht als grobe Pauschale mit ein.
	//
	// F-1100: Wo die Schätzung wirkt, war vorher zu optimistisch beschrieben
	// („auf beide Gates"). Tatsächlich gilt:
	//   - Gate vor dem Turn: seit F-1100 für JEDEN Send mit bekanntem Limit,
	//     auch ohne Conversation. Vorher lief es nur mit conversationId — ein
	//     einzelner Send mit fünf Bildern und langer Karte durchlief gar kein
	//     Gate. Mit Conversation kommt die geschätzte History hinzu
	//     (ADR-0096 §2, Nachtrag F-1108).
	//   - Loop-Gate: nur, wenn der Anbieter für die gelaufene Runde keine
	//     Input-Tokens meldet (F-1091) — dann ist die Schätzung, samt
	//     Bild-Pauschale, seine Basis. Meldet er sie, gilt die Messung; die
	//     Bilder stecken dann schon darin.
	budgetLimit := llm.ContextLimit(req.Provider, req.Model)
	neuerAnteil := llm.EstimateTokens(userPrompt) + llm.EstimateTokens(req.SystemPrompt) +
		llm.EstimateImageTokens(len(bilder))
	// verlauf — geschätzte Grösse der History, die mitgesendet wird; nur mit
	// Conversation, sonst 0.
	var verlauf int
	if req.ConversationID != "" {
		if a.convs == nil {
			writeSendError(w, http.StatusServiceUnavailable, "internal", "conversations subsystem disabled")
			return
		}
		c, err := a.convs.GetConversation(r.Context(), req.ConversationID)
		if errors.Is(err, storage.ErrLLMConversationNotFound) {
			writeSendError(w, http.StatusNotFound, "internal", "conversation not found")
			return
		}
		if err != nil {
			writeSendError(w, http.StatusInternalServerError, "internal", "load conversation failed")
			return
		}
		// Cross-Card-IDOR-Schutz: Conversation muss auf dieser Karte liegen.
		if c.BoardID != boardID || c.CardID != cardID {
			writeSendError(w, http.StatusNotFound, "internal", "conversation not found")
			return
		}
		// Firmenmodus (ADR-0133 Nachtrag 2 A5, Karte #674): Ein fremder
		// Verlauf existiert für den Caller nicht — 404 wie in
		// llm_conversations.go, und zwar VOR der Archiv-Prüfung, die sonst
		// den Zustand eines fremden Verlaufs verriete. Ohne Firmenmodus
		// bleibt es bei der 403 unten (ADR-0096 §2).
		if a.firmenmodus.Aktiv() && storage.NormalizeEmail(c.CreatedBy) != email {
			writeSendError(w, http.StatusNotFound, "internal", "conversation not found")
			return
		}
		// Archivierte Conversations bekommen keine neuen Turns mehr.
		if c.ArchivedAt != nil {
			writeSendError(w, http.StatusBadRequest, "internal", "conversation is archived")
			return
		}
		// Nur created_by darf Turns hinzufügen (ADR-0096 §2).
		if storage.NormalizeEmail(c.CreatedBy) != email {
			writeSendError(w, http.StatusForbidden, "internal", "forbidden: only the conversation creator may send")
			return
		}
		// Provider/Modell-Konsistenz: Conversation ist an einen Provider
		// gebunden — Wechsel wäre Multi-Turn-bruch (unterschiedliche Token-
		// Heuristiken). Re-Use desselben Modells ist Pflicht.
		if c.Provider != req.Provider {
			writeSendError(w, http.StatusBadRequest, "internal", "provider mismatch with conversation")
			return
		}

		turns, err := a.convs.LoadTurns(r.Context(), c.ID)
		if err != nil {
			writeSendError(w, http.StatusInternalServerError, "internal", "load turns failed")
			return
		}
		history = turnsToHistory(turns)

		// Budget-Basis der Conversation (ADR-0096 §2, Nachtrag F-1108): die
		// History, die tatsächlich mitgeht, jede Nachricht einmal geschätzt.
		// Bis F-1108 stand hier die Summe der gemeldeten input_tokens aller
		// Turns — jede davon enthält die History bis zu ihrem Turn schon
		// ganz, zusammen also vielfach. Geprüft wird unten, gemeinsam mit
		// dem Send ohne Conversation.
		verlauf = schaetzeVerlauf(history)
		conv = &c
	}

	// Gate vor dem Turn (ADR-0096 §2; seit F-1100 auch ohne Conversation).
	if budgetLimit > 0 {
		projected := verlauf + neuerAnteil
		if float64(projected) > float64(budgetLimit)*ContextBudgetFraction {
			msg := fmt.Sprintf("prompt with attachments (%d tok) would exceed %.0f%% of model limit (%d tok)",
				projected, ContextBudgetFraction*100, budgetLimit)
			if conv != nil {
				msg = fmt.Sprintf("history (%d tok) + new prompt would exceed %.0f%% of model limit (%d tok)",
					projected, ContextBudgetFraction*100, budgetLimit)
			}
			writeSendError(w, http.StatusBadRequest, "context-budget", msg)
			return
		}
	}

	// Karte #674 (A6): Obergrenze je Person — als letzte Prüfung vor dem
	// Anbieter, damit nur Anfragen zählen, die ihn auch erreichen. Ein
	// Stream mit Nachlade-Runden zählt als EINE Anfrage.
	if !a.pruefeAnfragenObergrenze(w, u.ID) {
		return
	}

	// SSE-Header VOR dem ersten Schreiben setzen (sonst defaultet Go auf
	// text/plain + chunked-Buffering).
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx/Caddy-Hinweis: kein Buffering
	w.WriteHeader(http.StatusOK)

	// F-1127 (#544): main.go setzt WriteTimeout=30s für den ganzen
	// http.Server — eine ABSOLUTE Deadline ab Anfrage-Start. Ein LLM-Stream
	// darf länger laufen; die Frist wird deshalb vor jedem Event um
	// streamSchreibfrist verlängert (K-A3, events.go), statt sie aufzuheben.
	// Der server-weite WriteTimeout bleibt für alle anderen Endpunkte.
	sse := newSSEWriter(w)
	sse.frist = neueRollendeFrist(w, "llm-stream")
	sse.frist.verlaengern()
	sse.letzteAus = time.Now()
	// Antwortkopf sofort hinausschicken (Befund aus Strang N3, Sprint 82):
	// WriteHeader allein puffert ihn bis zum ersten Flush — also bis zum
	// ersten Wort oder Lebenszeichen, schlimmstenfalls 25 s. So lange sähe der
	// Proxy gar keine Antwort und könnte selbst mit 504 abbrechen.
	if sse.flusher != nil {
		sse.flusher.Flush()
	}
	// Karte #665: Lebenszeichen, solange der Anbieter schweigt. Das stopp
	// läuft per defer — der Handler schreibt danach nichts mehr.
	stoppLebenszeichen := sse.starteLebenszeichen(llmStreamLebenszeichen)
	defer stoppLebenszeichen()

	// Body-Akkumulator + Cap-Check (ADR-0096 §1 — Reply-Body-Cap als
	// Defense-in-depth während des Streamings).
	//
	// Alle Zähler laufen über den GESAMTEN Turn, nicht pro Provider-Aufruf:
	// Der agentische Fetch-Loop (ADR-0120) kann mehrere Runden fahren, und ein
	// pro Runde zurückgesetzter Cap wäre keiner.
	var (
		bodyLen     int
		capExceeded bool
		// Tool-Call-Cap (ADR-0096 §4 L2): max 5 Vorschläge pro Antwort.
		mutationCount  int
		mutationCapHit bool
		// Fetch-Buchhaltung (ADR-0120 §2/§6).
		fetchVersuche  int
		fetchGeladen   []string // Labels der tatsächlich gelieferten Karten
		fetchAbgelehnt int
	)

	// Loop-Zustand. loopHistory wächst um die Werkzeug-Runden; loopPrompt ist
	// ab Runde 2 leer, weil dann die letzte History-Nachricht (die
	// tool_results) die eigentliche Anfrage ist — siehe anthropicMessages.
	loopHistory := append([]llm.HistoryMessage{}, history...)
	loopPrompt := userPrompt
	loopBilder := bilder

	// Aggregat über alle Runden. Token-Zahlen werden summiert (ADR-0120
	// §Konsequenzen: ein Turn kann mehrere Provider-Aufrufe kosten).
	var (
		result       llm.SendResult
		antwortText  strings.Builder
		streamFehler error
		rundenTools  []llm.ToolUseBlock // Fetch-Aufrufe der laufenden Runde
		// F-857: in dieser Runde ueber MaxToolCallsPerRound hinaus
		// eingetroffene Lese-Aufrufe. Werden verworfen, ohne dass jeder
		// einzeln eine Meldung erzeugt.
		rundenVerworfen int
		// F-872/F-1091: Budget-Fortschreibung über die Werkzeug-Runden.
		//
		// anfrageSchaetzung ist die GESCHÄTZTE Grösse der Anfrage, die
		// gerade an den Anbieter ging — sie dient nur als Ersatz, wenn der
		// Anbieter für eine Runde keine Input-Tokens meldet. Sie beginnt
		// bei System-Prompt + Prompt + Bildern + dem Text der geladenen
		// History. Seit F-1108 dieselbe Rechnung wie das Gate vor dem Turn;
		// die frühere Summe der input_tokens aller Turns zählte die History
		// vielfach (F-1091) und brach als Basis in langen Conversations einen
		// bereits bezahlten Turn ab.
		anfrageSchaetzung = neuerAnteil + verlauf
		// budgetSchlussrunde: das Budget reichte nicht mehr für die
		// nachgeladenen Inhalte; die laufende Runde ist die letzte, und
		// ihre Werkzeug-Aufrufe werden nicht mehr beantwortet.
		budgetSchlussrunde   bool
		budgetUeberschritten bool
		budgetProjektion     int
	)

	onChunk := func(chunk llm.StreamChunk) error {
		// Client-Cancel via Context-Done → onChunk meldet zurück, Provider
		// bricht ab.
		if err := r.Context().Err(); err != nil {
			return err
		}
		if chunk.Text != "" {
			bodyLen += len(chunk.Text)
			if bodyLen > MaxLLMReplyBodyBytes {
				capExceeded = true
				return fmt.Errorf("reply body cap exceeded (%d > %d)", bodyLen, MaxLLMReplyBodyBytes)
			}
			if err := sse.writeEvent("token", map[string]string{"text": chunk.Text}); err != nil {
				return err
			}
		}
		if chunk.ToolCall == nil {
			return nil
		}
		// Lese-Werkzeug: NICHT an den Client als Vorschlag, sondern
		// serverseitig beantwortet (ADR-0120). Nur wenn wir es auch
		// mitgeschickt haben — sonst fällt der Name unten in die
		// Mutations-Validierung und wird dort fail-loud abgelehnt.
		if fetchAktiv && llm.IsReadTool(chunk.ToolCall.Name) {
			// F-857: Deckel schon beim Einsammeln. Ohne ihn erzeugt ein
			// Modell, das in einer Runde tausend Aufrufe emittiert, tausend
			// Ablehnungen samt Logzeilen und History-Einträgen — beantwortet
			// werden können ohnehin nie mehr als MaxFetchesPerTurn. Die
			// Anzahl der Verworfenen wird gezählt und EINMAL pro Runde
			// gemeldet, nicht pro Block.
			if len(rundenTools) >= llm.MaxToolCallsPerRound {
				rundenVerworfen++
				return nil
			}
			rundenTools = append(rundenTools, llm.ToolUseBlock{
				ID:   chunk.ToolCall.ID,
				Name: chunk.ToolCall.Name,
				Args: chunk.ToolCall.Args,
			})
			return nil
		}
		// Tool-Call-Chunks (ADR-0096 §3+§4 — Etappe 4). EnableMutations=false
		// blockt das implizit, weil Tools nicht mitgeschickt werden — aber
		// Defense-in-depth: prüfen + ignorieren (LLM könnte tool_use auch
		// ohne explizite Tools halluzinieren).
		if !req.EnableMutations {
			return nil
		}
		mutationCount++
		if mutationCount > llm.MaxMutationProposalsPerResponse {
			mutationCapHit = true
			return llm.ErrMutationCapExceeded
		}
		v := llm.ValidateMutation(chunk.ToolCall.Name, chunk.ToolCall.ID, chunk.ToolCall.Args, cardCtx.ValidCols)
		payload := map[string]any{"id": v.ID, "name": v.Name}
		if v.Allowed {
			payload["allowed"] = true
			payload["args"] = json.RawMessage(v.Args)
		} else {
			payload["blocked"] = true
			payload["reason"] = v.Reason
		}
		return sse.writeEvent("tool-call", payload)
	}

	// F-3102-Nacharbeit (Sitzungs-Kritik 23.11. B1): Kosten je Runde mit dem Satz
	// DIESER Runde — jede Runde ist eine eigene Anfrage, die Staffel (Haiku 5.5
	// über 100K Prompt-Tokens) gilt je Anfrage, nicht für die Summe des Turns.
	var kostenUSD float64
	for runde := 0; runde < llm.MaxAgenticRounds; runde++ {
		rundenTools = nil
		rundenVerworfen = 0
		rundenErgebnis, err := streamer.SendStream(r.Context(), apiKey, llm.SendRequest{
			Model:           req.Model,
			System:          req.SystemPrompt,
			History:         loopHistory,
			UserPrompt:      loopPrompt,
			Images:          loopBilder,
			MaxTokens:       req.MaxTokens,
			EnableMutations: req.EnableMutations,
			EnableFetch:     fetchAktiv,
			User:            a.anbieterNutzerFeld(u.Email),
		}, onChunk)

		// Token-Zahlen summieren, auch wenn diese Runde fehlschlug — was der
		// Provider bis dahin gerechnet hat, ist angefallen. FinishReason und
		// Modell kommen von der letzten Runde.
		result.InputTokens += rundenErgebnis.InputTokens
		result.OutputTokens += rundenErgebnis.OutputTokens
		kostenUSD += llm.KostenUSD(provider, req.Model, rundenErgebnis.InputTokens, rundenErgebnis.OutputTokens)
		if rundenErgebnis.Model != "" {
			result.Model = rundenErgebnis.Model
		}
		if rundenErgebnis.FinishReason != "" {
			result.FinishReason = rundenErgebnis.FinishReason
		}
		antwortText.WriteString(rundenErgebnis.Text)

		if err != nil {
			streamFehler = err
			break
		}
		// F-857: eine Zeile pro Runde, nicht eine pro verworfenem Block.
		// Sie steht VOR dem Abbruch bei leerer Runde, weil sonst genau der
		// pathologische Fall unprotokolliert bliebe, fuer den der Deckel da
		// ist — wenn alle Aufrufe jenseits des Deckels lagen, ist
		// rundenTools voll, aber die Zahl dahinter waere unsichtbar.
		if rundenVerworfen > 0 {
			log.Printf("aboard-server: llm-stream: %d Lese-Aufruf(e) ueber dem Rundendeckel (%d) verworfen (Runde %d, Karte %s)",
				rundenVerworfen, llm.MaxToolCallsPerRound, runde+1, cardID)
			fetchAbgelehnt += rundenVerworfen
		}
		if len(rundenTools) == 0 {
			break // das Modell will nichts mehr nachladen — fertig
		}
		if budgetSchlussrunde {
			// F-1091: Diese Runde war die erzwungene letzte, weil das Budget
			// die nachgeladenen Inhalte nicht mehr trug. Will das Modell
			// trotzdem weiter nachladen, antworten wir nicht mehr — wie bei
			// der Runden-Obergrenze direkt darunter. Der bis hier gestreamte
			// Text wird persistiert.
			log.Printf("aboard-server: llm-stream: Kontext-Budget erschoepft, %d weitere Nachlade-Wuensche nach der Schlussrunde verworfen (Karte %s)",
				len(rundenTools), cardID)
			fetchAbgelehnt += len(rundenTools)
			break
		}
		if runde == llm.MaxAgenticRounds-1 {
			// Letzte erlaubte Runde und trotzdem noch Nachlade-Wünsche: Wir
			// antworten nicht mehr darauf. Der bis hier gestreamte Text wird
			// persistiert; ohne diese Zeile bliebe unsichtbar, dass die
			// Antwort abgeschnitten ist.
			log.Printf("aboard-server: llm-stream: Runden-Obergrenze (%d) erreicht, %d offene Nachlade-Wuensche verworfen (Karte %s)",
				llm.MaxAgenticRounds, len(rundenTools), cardID)
			for range rundenTools {
				fetchAbgelehnt++
			}
			break
		}

		// Werkzeug-Antworten bauen. Reihenfolge und Anzahl müssen exakt zu
		// den Aufrufen passen — Anthropic lehnt eine Anfrage ab, in der ein
		// tool_use ohne zugehöriges tool_result bleibt.
		//
		// F-1091: Die Antworten werden erst GEBAUT, dann gegen das Budget
		// geprüft, und erst danach gemeldet (SSE, Log, Buchhaltung). Vorher
		// gingen die fetch-Ereignisse vor der Prüfung hinaus — eine Karte,
		// die das Budget dann nicht mehr trug, stand beim Client als
		// „geladen" da.
		type abruf struct {
			res      llm.ToolResultBlock
			ereignis map[string]any
			label    string // gesetzt, wenn die Karte geliefert wird
		}
		abrufe := make([]abruf, 0, len(rundenTools))
		for _, tu := range rundenTools {
			res := llm.ToolResultBlock{ID: tu.ID, Name: tu.Name, IsError: true}
			ereignis := map[string]any{"id": tu.ID}
			// Die angefragte id gehört in JEDE Meldung — gerade die
			// abgelehnten sind forensisch interessant. Sie erst nach
			// resolveFetch zu setzen liess die Budget- und Parse-Ablehnungen
			// mit `ziel=<nil>` im Log stehen.
			if roh, _ := llm.ParseFetchCardID(tu.Args); roh != "" {
				ereignis["cardId"] = roh
			}
			var label string

			fetchVersuche++
			switch {
			case fetchVersuche > llm.MaxFetchesPerTurn:
				// Gezählt wird JEDE Anfrage, nicht nur die erfolgreiche —
				// sonst könnte ein Modell mit ablehnbaren Anfragen beliebig
				// viele Berechtigungsprüfungen auslösen (ADR-0120 §2).
				res.Content = fmt.Sprintf(
					"Obergrenze von %d Abrufen pro Antwort erreicht. Bitte mit den vorliegenden Angaben antworten.",
					llm.MaxFetchesPerTurn)
			default:
				cardIDRaw, grund := llm.ParseFetchCardID(tu.Args)
				if grund != "" {
					res.Content = grund
					break
				}
				out := a.resolveFetch(r.Context(), email, boardID, cardCtx.Card, cardCtx.CardsByID, cardIDRaw, req.IncludeFields)
				ereignis["cardId"] = out.CardID
				if !out.OK {
					res.Content = out.Reason
					break
				}
				res.IsError = false
				res.Content = out.Content
				label = out.Label()
				ereignis["label"] = label
				ereignis["title"] = out.Title
			}
			abrufe = append(abrufe, abruf{res: res, ereignis: ereignis, label: label})
		}

		// F-872/F-1091: Budget VOR der nächsten Provider-Runde. Das Gate vor
		// dem Turn kennt die nachgeladenen Karteninhalte nicht — sie
		// entstehen erst hier.
		//
		// Basis ist die Grösse der Anfrage, die eben lief: gemessen, wenn der
		// Anbieter Input-Tokens meldet, sonst geschätzt. Dazu kommt der
		// Assistant-Zug dieser Runde (Text + Aufruf-Argumente) — gemessen als
		// OutputTokens, die beides enthalten, sonst geschätzt; NICHT beides,
		// das zählte Text und Argumente doppelt (F-1091) — und die
		// Werkzeug-Antworten.
		if budgetLimit > 0 {
			grenze := float64(budgetLimit) * ContextBudgetFraction
			basis := anfrageSchaetzung
			if rundenErgebnis.InputTokens > 0 {
				basis = rundenErgebnis.InputTokens
			}
			assistant := rundenErgebnis.OutputTokens
			if assistant <= 0 {
				assistant = llm.EstimateTokens(rundenErgebnis.Text)
				for _, tu := range rundenTools {
					assistant += llm.EstimateTokens(string(tu.Args))
				}
			}
			antworten := func() int {
				n := 0
				for _, ab := range abrufe {
					n += llm.EstimateTokens(ab.res.Content)
				}
				return n
			}
			projektion := basis + assistant + antworten()
			if float64(projektion) > grenze {
				// Die nachgeladenen Inhalte passen nicht mehr. Statt den
				// bereits bezahlten Turn zu verwerfen, bekommt das Modell
				// eine letzte Runde OHNE neue Inhalte: Jeder Abruf dieser
				// Runde wird mit einer Auskunft abgelehnt — derselbe Weg wie
				// bei MaxFetchesPerTurn („bitte mit den vorliegenden Angaben
				// antworten"). Die Werkzeug-Definition bleibt dabei in der
				// Anfrage, weil die History jetzt tool_use-Blöcke trägt; eine
				// weitere Runde gibt es aber nicht (budgetSchlussrunde oben).
				for i := range abrufe {
					if abrufe[i].label == "" {
						continue // war ohnehin abgelehnt
					}
					abrufe[i].res.IsError = true
					abrufe[i].res.Content = "Kontext-Budget erreicht: Der Inhalt dieser Karte passt nicht mehr in die Anfrage. " +
						"Bitte mit den vorliegenden Angaben antworten."
					abrufe[i].label = ""
					delete(abrufe[i].ereignis, "label")
					delete(abrufe[i].ereignis, "title")
				}
				ohneInhalte := basis + assistant + antworten()
				log.Printf("aboard-server: llm-stream: Kontext-Budget vor Runde %d ueberschritten (%d tok, Grenze %.0f%% von %d), Schlussrunde ohne Nachladen (%d tok) (Karte %s)",
					runde+2, projektion, ContextBudgetFraction*100, budgetLimit, ohneInhalte, cardID)
				if float64(ohneInhalte) > grenze {
					// Selbst ohne die neuen Inhalte zu gross — dann bleibt
					// nur der Abbruch wie vor F-1091.
					budgetUeberschritten = true
					budgetProjektion = ohneInhalte
					streamFehler = fmt.Errorf("context budget exceeded before round %d (%d tok > %.0f%% of %d tok)",
						runde+2, ohneInhalte, ContextBudgetFraction*100, budgetLimit)
				}
				budgetSchlussrunde = true
				projektion = ohneInhalte
			}
			anfrageSchaetzung = projektion
		}

		// Melden: Buchhaltung, pro Abruf eine Server-Logzeile und ein
		// SSE-Event (ADR-0120 §6). Der Karteninhalt selbst wird NICHT
		// geloggt.
		ergebnisse := make([]llm.ToolResultBlock, 0, len(abrufe))
		for _, ab := range abrufe {
			if ab.res.IsError {
				fetchAbgelehnt++
				ab.ereignis["reason"] = ab.res.Content
			} else {
				fetchGeladen = append(fetchGeladen, ab.label)
			}
			ab.ereignis["ok"] = !ab.res.IsError
			log.Printf("aboard-server: llm-fetch: board=%s karte=%s akteur=%s ziel=%v ok=%t",
				boardID, cardID, email, ab.ereignis["cardId"], !ab.res.IsError)
			if err := sse.writeEvent("fetch", ab.ereignis); err != nil {
				if streamFehler == nil {
					streamFehler = err
				}
				break
			}
			ergebnisse = append(ergebnisse, ab.res)
		}
		if streamFehler != nil {
			break
		}

		// Werkzeug-Runde in die History legen: erst der Assistant-Zug mit den
		// Aufrufen, dann unsere Antworten. Ab jetzt ist der User-Prompt Teil
		// der History, nicht mehr ein eigenes Feld.
		if loopPrompt != "" || len(loopBilder) > 0 {
			// Die Bilder wandern MIT in die History. Ohne das sähe das Modell
			// sie ab der zweiten Runde nicht mehr — mitten im selben Turn,
			// während es gerade über sie schreibt.
			loopHistory = append(loopHistory, llm.HistoryMessage{
				Role: "user", Content: loopPrompt, Images: loopBilder,
			})
			loopPrompt = ""
			loopBilder = nil
		}
		loopHistory = append(loopHistory,
			llm.HistoryMessage{Role: "assistant", Content: rundenErgebnis.Text, ToolUses: rundenTools},
			llm.HistoryMessage{Role: "user", ToolResults: ergebnisse},
		)
	}

	result.Text = antwortText.String()

	if streamFehler != nil {
		// Stream gebrochen — kein Kommentar persistieren (Cancel-Vertrag).
		// Gilt unverändert auch, wenn der Bruch in einer späteren Runde des
		// Fetch-Loops auftrat: eine halbe Antwort ist keine Antwort.
		//
		// Die Kosten sind trotzdem angefallen — seit dem Fetch-Loop
		// womöglich über mehrere erfolgreiche Runden hinweg. Ohne diese
		// Zeile verschwinden sie spurlos: Der Kommentar wird nicht
		// geschrieben, und die SSE-Fehlermeldung führt keine Zahlen.
		if result.InputTokens > 0 || result.OutputTokens > 0 || len(fetchGeladen) > 0 {
			log.Printf("aboard-server: llm-stream abgebrochen, Kosten sind angefallen: "+
				"board=%s karte=%s akteur=%s provider=%s modell=%s in=%d out=%d nachgeladen=%d",
				boardID, cardID, email, req.Provider, req.Model,
				result.InputTokens, result.OutputTokens, len(fetchGeladen))
		}
		switch {
		case budgetUeberschritten:
			// F-872: dieselbe Fehlerart wie das Gate vor dem Turn
			// (kind="context-budget"), hier als SSE-Ereignis, weil der Stream
			// schon läuft. Kein Kommentar — wie bei jedem gebrochenen Turn.
			writeSSEError(sse, "context-budget",
				fmt.Sprintf("conversation plus loaded card contents (%d tok) would exceed %.0f%% of model limit (%d tok); stopped before the next round",
					budgetProjektion, ContextBudgetFraction*100, budgetLimit))
		case mutationCapHit:
			writeSSEError(sse, "too-many-mutations", fmt.Sprintf("more than %d mutation proposals", llm.MaxMutationProposalsPerResponse))
		case capExceeded:
			writeSSEError(sse, "too-large", "reply exceeded server-side cap")
		case errors.Is(streamFehler, llm.ErrInvalidKey):
			writeSSEError(sse, "invalid-key", "provider rejected api key")
		case anbieterFehlerArt(streamFehler) != "":
			// Karte #665: Fehlerart aus der 4xx-Antwort des Anbieters.
			writeSSEError(sse, anbieterFehlerArt(streamFehler), anbieterFehlerMeldung(streamFehler), streamFehler)
		case errors.Is(streamFehler, llm.ErrProviderUnreachable):
			writeSSEError(sse, "unreachable", "provider unreachable or timeout")
		case errors.Is(r.Context().Err(), context.Canceled):
			// Client-Abort — kein Event-Schreiben mehr, Connection ist eh tot.
		default:
			writeSSEError(sse, "provider", redactString(streamFehler.Error()), streamFehler)
		}
		return
	}

	// Persistierung: Kommentar + Activity wie ADR-0095, mit F-398-konformem
	// contextNote-Override-Marker.
	cost := kostenUSD

	// Karte #674 (ADR-0133 Nachtrag 2 A5): Im Firmenmodus wird die Antwort
	// NICHT als Kartenkommentar geschrieben und es entsteht kein
	// llm-sent-Aktivitätseintrag — beides sähen alle am Board (samt
	// Ereignisstrom). Die Antwort lebt dann nur im Stream und, mit
	// conversationId, in den Turns des privaten Verlaufs.
	var commentID string
	if a.antwortAlsKommentar() {
		commentID = uuid.NewString()
		now := time.Now().UTC().Format(time.RFC3339)
		contextNote := buildContextNote(req.IncludeFields)
		if req.UserPromptOverride != "" {
			contextNote = fmt.Sprintf("Client-Override (PII-Filter aktiv, %d B); ursprüngliche Auswahl: %s",
				len(req.UserPromptOverride), contextNote)
		}
		if len(bilder) > 0 {
			namen := make([]string, 0, len(bilder))
			for _, b := range bilder {
				namen = append(namen, b.Filename)
			}
			contextNote = fmt.Sprintf("%s; Anhaenge: %s", contextNote, strings.Join(namen, ", "))
		}
		if len(fetchGeladen) > 0 {
			contextNote = fmt.Sprintf("%s; nachgeladen: %s", contextNote, strings.Join(fetchGeladen, ", "))
		}
		authorLabel := fmt.Sprintf("🤖 %s/%s (im Auftrag von %s)", req.Provider, req.Model, u.Email)

		if err := llm.AddLLMReply(r.Context(), a.boards, llm.LLMReplyParams{
			BoardID:         boardID,
			CardID:          cardID,
			CommentID:       commentID,
			Author:          authorLabel,
			Body:            result.Text,
			Created:         now,
			ActivityTS:      now,
			ActivityActor:   u.Email,
			Provider:        req.Provider,
			Model:           req.Model,
			InputTokens:     result.InputTokens,
			OutputTokens:    result.OutputTokens,
			CostEstimateUSD: cost,
			ContextNote:     contextNote,
			FetchedCards:    fetchGeladen,
			FetchesRejected: fetchAbgelehnt,
			Attachments:     anhangAudit(bilder),
		}); err != nil {
			writeSSEError(sse, "internal", "persist reply failed", err)
			return
		}
	}

	a.markiereSchluesselBenutzt(r.Context(), u.ID, req.Provider)

	// Multi-Turn-Persistenz (ADR-0096 §2): User- + Assistant-Turn ablegen.
	// Best-effort: Fehler werden geloggt-am-Done, der Stream gilt aber als
	// erfolgreich (Kommentar liegt im Karten-State). Im Firmenmodus gibt es
	// keinen Kommentar — ein Fehler hier heisst dann, dass die Antwort nur
	// im Stream angekommen ist. RFC3339Nano für strikt
	// monotone TS-Reihenfolge (LoadTurns ORDER BY ts ASC).
	var userTurnID, assistantTurnID string
	if conv != nil && a.convs != nil {
		userTurnID = uuid.NewString()
		assistantTurnID = uuid.NewString()
		userTS := time.Now().UTC().Format(time.RFC3339Nano)
		assistantTS := time.Now().UTC().Format(time.RFC3339Nano)
		if assistantTS <= userTS {
			// Defense-in-depth: gleicher Nano-Stamp (hochfrequente Mocks)
			// → ein Nanosekunden-Tick erzwingen, damit LoadTurns die
			// Reihenfolge stabil hält.
			assistantTS = time.Now().Add(time.Nanosecond).UTC().Format(time.RFC3339Nano)
		}
		// F-413: EIN Aufruf, EINE Transaktion. Vorher waren es zwei getrennte
		// AppendTurn-Aufrufe, und die Fehlerbehandlung leerte nur die
		// zurückgegebenen IDs — sie räumte nichts auf. Schlug der zweite fehl,
		// blieb ein User-Turn OHNE Antwort in der Tabelle stehen, und
		// LoadTurns speist damit die Provider-History des nächsten Aufrufs:
		// das Modell sieht dort eine Frage, die es nicht beantwortet hat.
		if err := a.convs.AppendTurnPair(r.Context(),
			storage.AppendTurnParams{
				ID:             userTurnID,
				ConversationID: conv.ID,
				TS:             userTS,
				Role:           "user",
				Content:        userPrompt,
			},
			storage.AppendTurnParams{
				ID:             assistantTurnID,
				ConversationID: conv.ID,
				TS:             assistantTS,
				Role:           "assistant",
				Content:        result.Text,
				InputTokens:    &result.InputTokens,
				OutputTokens:   &result.OutputTokens,
				CostEstimate:   &cost,
			},
		); err != nil {
			// Kein Datenverlust — der Kommentar ist persistiert; die
			// Turn-Tabelle ist Sidecar. Aber anders als vorher ist der
			// Zustand jetzt eindeutig: BEIDE Turns fehlen, keiner hängt
			// halb in der History. Und der Fehler wird geloggt, statt
			// stumm in zwei geleerten IDs zu verschwinden.
			log.Printf("aboard-server: llm-stream: Turn-Paar fuer Conversation %s "+
				"nicht persistiert (Audit-Trail luecke, Stream-Erfolg unberuehrt): %v",
				conv.ID, err)
			userTurnID, assistantTurnID = "", ""
		}
	}

	// Done-Event mit allen Audit-/UI-Metadaten (Frontend braucht
	// commentId, um auf den persistierten Kommentar zu verlinken).
	donePayload := map[string]any{
		"inputTokens":     result.InputTokens,
		"outputTokens":    result.OutputTokens,
		"costEstimateUSD": cost,
		"finishReason":    result.FinishReason,
		// K2-4: Der Lauf sagt selbst, ob er im Firmenmodus lief. Die Oberfläche
		// richtet Protokoll und Knöpfe danach, nicht nach ihrer Schlüssel-Liste
		// (die kann zwischendurch verworfen oder leer geladen sein).
		"firmenmodus": a.firmenmodus.Aktiv(),
	}
	// Ohne Kommentar (Firmenmodus) keine commentId — die Oberfläche blendet
	// dann „In Kommentar belassen“/„Verwerfen“ aus.
	if commentID != "" {
		donePayload["commentId"] = commentID
	}
	if len(fetchGeladen) > 0 {
		donePayload["fetchedCards"] = fetchGeladen
	}
	if fetchAbgelehnt > 0 {
		donePayload["fetchesRejected"] = fetchAbgelehnt
	}
	if userTurnID != "" {
		donePayload["userTurnId"] = userTurnID
		donePayload["assistantTurnId"] = assistantTurnID
	}
	_ = sse.writeEvent("done", donePayload)
}

// anhangAudit formt die Audit-Zeilen für die mitgeschickten Anhänge:
// id zuerst, dann Typ und Bytes. Der Dateiname steht bewusst NICHT drin —
// er ist im contextNote für Menschen, hier zählt die eindeutige Zuordnung.
func anhangAudit(bilder []llm.ImageAttachment) []string {
	if len(bilder) == 0 {
		return nil
	}
	out := make([]string, 0, len(bilder))
	for _, b := range bilder {
		out = append(out, fmt.Sprintf("%s (%s, %d B)", b.ID, b.MIME, len(b.Data)))
	}
	return out
}

// schaetzeVerlauf schätzt die Tokens einer geladenen History aus ihrem Text
// (F-1091). Sie ist der Ersatz für eine Messung, wenn der Anbieter keine
// Input-Tokens meldet, und Basis des Gates vor dem Turn (F-1108). Jede
// Nachricht zählt genau einmal.
func schaetzeVerlauf(history []llm.HistoryMessage) int {
	n := 0
	for _, h := range history {
		n += llm.EstimateTokens(h.Content)
	}
	return n
}

// turnsToHistory wandelt persistierte Turns in das Provider-Format. Turns
// kommen in chronologischer Reihenfolge aus dem Repo — die History wird
// genauso übergeben (älteste zuerst, jüngste zuletzt). Provider übersetzen
// die generische role in ihr Schema (Google: assistant→model).
func turnsToHistory(turns []storage.LLMTurn) []llm.HistoryMessage {
	out := make([]llm.HistoryMessage, 0, len(turns))
	for _, t := range turns {
		out = append(out, llm.HistoryMessage{Role: t.Role, Content: t.Content})
	}
	return out
}
