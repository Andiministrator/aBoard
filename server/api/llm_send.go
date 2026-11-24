package api

// llm_send.go — POST /boards/{b}/cards/{c}/llm-send + GET /llm/pricing
// (#299, ADR-0095 Etappe 2b).
//
// ACL: editor+ am Board (via ACLGuard, POST auf Sub-Resource = editor+).
// Identity → User-Lookup → LLMKeyRepo.Get(user, provider) → Provider.Send →
// AddLLMReply (Kommentar + Activity-Entry mit meta) → LLMKeyRepo.MarkUsed.
//
// Pending-Lock (in-memory, ADR-0095 §5) verhindert parallele Sends auf
// derselbe Karte. Stale-Cleanup nach 90 s. Konflikt → HTTP 423 Locked.
//
// Antwort-Sanitierung: NICHT serverseitig — LLM-Antwort bleibt als
// Markdown-Roh-Text im Kommentar-Body (state_json). Browser sanitiert beim
// Rendern via src/markdown.js (Pfad analog Mail-Ingest-Kommentare, kein
// dediziertes ADR-0089-Backend-Port nötig). Schutz gegen Provider-Runaway:
// Body-Cap auf MaxBodyBytes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// MaxLLMReplyBodyBytes — Cap für die LLM-Antwort, die als Kommentar
// persistiert wird. Schutz gegen Provider-Runaway oder Prompt-Injection-
// Eskalation. 256 KB sind ~64k Tokens bei 4-Byte-Heuristik — deutlich
// über realistischen MaxTokens-Defaults (4096).
const MaxLLMReplyBodyBytes = 256 * 1024

// MaxLLMSendRequestBytes — Cap für den Request-Body (System-Prompt +
// includeFields). 64 KB ist großzügig für einen System-Prompt.
const MaxLLMSendRequestBytes = 64 * 1024

// SendDefaultMaxTokens — Provider-default für MaxTokens, falls Client
// keinen Wert mitschickt. 4096 ist für die vier MVP-Provider ein
// vernünftiger Mittelweg.
const SendDefaultMaxTokens = 4096

// LLMSendAPI bündelt Abhängigkeiten für POST /boards/.../llm-send +
// GET /llm/pricing.
type LLMSendAPI struct {
	boards *storage.BoardRepo
	users  *storage.UserRepo
	keys   *storage.LLMKeyRepo
	convs  *storage.LLMConversationRepo // optional — Multi-Turn-Stream (#300/ADR-0096 §2)
	acl    *storage.AclRepo             // optional — Standort-ACL für fetch-related-card (#428/ADR-0120 §1)
	// optional — Anhänge als Bilder im Prompt (#429/ADR-0121). Ohne Store
	// wird eine Anhang-Auswahl benannt abgelehnt, nicht still übergangen.
	attachments attachmentOpener
	registry    *llm.Registry
	locks       *llm.LockManager
	// Karte #664 (ADR-0133 §2): nil = kein Firmenmodus (heutiges Verhalten).
	// Siehe llm_firmenmodus.go.
	firmenmodus *llm.Firmenmodus
	pii         llm.PIIVorgabe
	// Karte #674 (ADR-0133 Nachtrag 2 A6): Obergrenze für Chat-Anfragen je
	// Person. Immer gesetzt (Vorgabe), wirkt aber nur im Firmenmodus —
	// siehe llm_obergrenze.go.
	anfragen *rateLimiter
}

// NewLLMSendAPI initialisiert die API. Wenn keys == nil (kein Master-Key
// konfiguriert), antworten Send-Routen mit 503 — analog LLMKeysAPI.
// convs ist optional: nil → Multi-Turn-Stream-Pfad antwortet mit 503,
// klassische Single-Turn-Sends laufen unverändert.
func NewLLMSendAPI(boards *storage.BoardRepo, users *storage.UserRepo, keys *storage.LLMKeyRepo, registry *llm.Registry, locks *llm.LockManager) *LLMSendAPI {
	if locks == nil {
		locks = llm.NewLockManager(0)
	}
	return &LLMSendAPI{
		boards:   boards,
		users:    users,
		keys:     keys,
		registry: registry,
		locks:    locks,
		anfragen: newRateLimiter(DefaultLLMAnfragenProStunde, LLMAnfragenFenster),
	}
}

// WithConversations rüstet eine bestehende LLMSendAPI um das
// Conversation-Repo nach. Wird vom main.go-Wiring nach beiden Konstruktoren
// aufgerufen, damit die Stream-Multi-Turn-Logik aktiviert ist. Bei nil bleibt
// der Multi-Turn-Pfad deaktiviert (klassischer Single-Turn-Stream bleibt
// funktional).
func (a *LLMSendAPI) WithConversations(convs *storage.LLMConversationRepo) *LLMSendAPI {
	a.convs = convs
	return a
}

// WithACL rüstet das ACL-Repo nach. Es wird ausschliesslich für die
// Standort-Prüfung des Lese-Werkzeugs fetch-related-card gebraucht
// (ADR-0120 §1a) — die Routen-ACL macht weiterhin der ACLGuard.
//
// Ohne dieses Repo wird das Werkzeug gar nicht erst angeboten
// (fetchVerfuegbar): Ein Nachladen ohne Standort-Prüfung wäre genau die
// Lücke, die die Prüfung schliesst. Fail-closed statt „geht schon".
func (a *LLMSendAPI) WithACL(acl *storage.AclRepo) *LLMSendAPI {
	a.acl = acl
	return a
}

// Register hängt die Routen an. /boards/* liegt hinter dem ACLGuard
// (editor+ für POST auf Sub-Resource). /llm/pricing ist authentifiziert,
// aber ohne Board-ACL — Pricing-Tabelle ist nicht sensibel.
func (a *LLMSendAPI) Register(mux *http.ServeMux) {
	mux.Handle("POST /boards/{boardId}/cards/{cardId}/llm-send",
		WithDefaults(http.HandlerFunc(a.handleSend)))
	mux.Handle("POST /boards/{boardId}/cards/{cardId}/llm-discard",
		WithDefaults(http.HandlerFunc(a.handleDiscard)))
	mux.Handle("GET /boards/{boardId}/cards/{cardId}/llm-pending",
		WithDefaults(http.HandlerFunc(a.handlePending)))
	mux.Handle("GET /llm/pricing",
		WithDefaults(http.HandlerFunc(a.handlePricing)))
	// #300, ADR-0096 Etappe 1: SSE-Streaming-Endpoint.
	a.registerStream(mux)
}

type llmSendRequest struct {
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	SystemPrompt  string   `json:"systemPrompt"`
	IncludeFields []string `json:"includeFields"`
	MaxTokens     int      `json:"maxTokens"` // 0 → SendDefaultMaxTokens
	// UserPromptOverride: optional. Wenn nicht-leer, ersetzt es den
	// server-gebauten BuildPrompt. Genutzt vom PII-Preview-Modal (#301,
	// ADR-0097) — der Client redactet PII clientseitig und sendet den
	// gefilterten User-Prompt direkt; der Server überspringt BuildPrompt.
	// IncludeFields wird in diesem Fall nur für den Audit-Vermerk
	// (ContextNote) genutzt.
	UserPromptOverride string `json:"userPromptOverride,omitempty"`
	// PiiFilterActive — wie im Stream-Request (llm_stream.go). Hier nur für
	// die Betreiber-Vorgabe ABOARD_LLM_PII=pflicht gebraucht (Karte #664):
	// ein Client, der kein aktives Filtern meldet, wird dann abgewiesen.
	PiiFilterActive bool `json:"piiFilterActive,omitempty"`
}

// llmSendErrorResponse ist die strukturierte Fehler-Form (ADR-0095 §1).
// kind ∈ {"locked","invalid-key","timeout","unreachable","provider","internal"}.
type llmSendErrorResponse struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	// Holder ist optional, nur bei kind="locked" gesetzt.
	Holder *llmLockedHolder `json:"holder,omitempty"`
}

// llmLockedHolder beschreibt den Halter einer Kartensperre. Person, Anbieter
// und Modell fehlen im Firmenmodus für alle ausser dem Halter selbst
// (F-1553, siehe sperrHalter) — dann bleibt nur „belegt seit".
type llmLockedHolder struct {
	User     string `json:"user,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Since    string `json:"since"`
}

// sperrHalter baut die Halter-Angabe für 423 und GET llm-pending. user ist
// die Kennung, die der jeweilige Weg ausliefert (E-Mail bzw. User-ID);
// istSelbst sagt, ob der Aufrufer die Sperre selbst hält.
//
// F-1553 (ADR-0133 Nachtrag 4): Im Firmenmodus sind Verläufe nur für ihren
// Ersteller lesbar (Nachtrag 2 A5) — und schon die Befragung selbst ist ein
// Personenbezug. Die Kartensperre (ADR-0095 §5) braucht aber nur „belegt";
// wer fragt und mit welchem Modell, erfährt deshalb nur die fragende Person.
// Ohne Firmenmodus bleibt alles sichtbar wie bisher.
func (a *LLMSendAPI) sperrHalter(holder llm.LockHolder, user string, istSelbst bool) *llmLockedHolder {
	h := &llmLockedHolder{Since: holder.Since.UTC().Format(time.RFC3339)}
	if a.firmenmodus.Aktiv() && !istSelbst {
		return h
	}
	h.User = user
	h.Provider = holder.Provider
	h.Model = holder.Model
	return h
}

// F-252-Nachzug (Sprint-57-Kritiker): Diese Familie hatte dieselbe Luecke wie
// writeError vor Karte #411 — nur andersherum. Sie schrieb den Rohfehler in
// die CLIENT-Antwort (inklusive Dateipfaden und SQLite-Meldungen) und ins Log
// gar nichts. Beides ist jetzt getauscht: generische Meldung nach aussen, der
// echte Fehler ins Log, redigiert wie bei writeError.
func writeSendError(w http.ResponseWriter, status int, kind, msg string, err ...error) {
	if len(err) > 0 && err[0] != nil {
		log.Printf("aboard-server: %d %s/%s: %s", status, kind, msg, redactForLog(err[0]))
	}
	writeJSON(w, status, llmSendErrorResponse{Kind: kind, Message: msg})
}

func (a *LLMSendAPI) handleSend(w http.ResponseWriter, r *http.Request) {
	// Im Firmenmodus braucht der Send kein Schlüssel-Repo (Serverschlüssel).
	if a.keys == nil && !a.firmenmodus.Aktiv() {
		writeSendError(w, http.StatusServiceUnavailable, "internal", "llm-send disabled: configure ABOARD_SECRET_KEY")
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
	var req llmSendRequest
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
	// Karte #664: Modell-Allowlist und PII-Pflicht des Betreibers.
	if !a.pruefeBetreiberVorgaben(w, req.Model, req.PiiFilterActive) {
		return
	}

	provider, err := a.registry.Get(req.Provider)
	if err != nil {
		writeSendError(w, http.StatusBadRequest, "internal", "unknown provider")
		return
	}

	// Karten-Map + Refs laden (für BuildPrompt).
	cardCtx, err := loadCardForLLM(r, a.boards, boardID, cardID)
	if err != nil {
		if errors.Is(err, errCardNotFound) {
			writeSendError(w, http.StatusNotFound, "internal", "card not found")
			return
		}
		writeSendError(w, http.StatusInternalServerError, "internal", "load card failed")
		return
	}

	// API-Key des Senders — im Firmenmodus der Serverschlüssel (Karte #664).
	apiKey, ok := a.sendeSchluessel(w, r, u.ID, req.Provider)
	if !ok {
		return
	}

	// Lock acquire VOR Provider-Call (verhindert parallele Sends). Schlüssel
	// ist Board + Karte (K2-3): Karten-IDs wiederholen sich nach einem Import.
	sperre := llm.SperrSchluessel(boardID, cardID)
	lauf, acquired, holder := a.locks.Acquire(sperre, u.ID, req.Provider, req.Model)
	if !acquired {
		writeJSON(w, http.StatusLocked, llmSendErrorResponse{
			Kind:    "locked",
			Message: "card is currently being processed by another send",
			Holder:  a.sperrHalter(holder, holder.User, holder.User == u.ID),
		})
		return
	}
	// F-1538: Freigabe nur mit der eigenen Lauf-Kennung — ein früherer Lauf
	// desselben Nutzers kann so nicht die Sperre dieses Laufs löschen. Die
	// Sperre wird für die ganze Laufzeit des Aufrufs aufgefrischt; stopHalten
	// läuft (LIFO) vor Release.
	defer a.locks.Release(sperre, lauf)
	stopHalten := a.locks.Halten(sperre, lauf)
	defer stopHalten()

	// Karte #674 (A6): Obergrenze je Person — direkt vor dem Anbieter-Aufruf,
	// damit nur Anfragen zählen, die ihn erreichen. Die Sperre gibt das defer
	// oben wieder frei.
	if !a.pruefeAnfragenObergrenze(w, u.ID) {
		return
	}

	// Prompt bauen (deterministische Karten-Serialisierung).
	// Falls der Client einen userPromptOverride mitschickt (PII-Preview-Modal,
	// #301/ADR-0097), nehmen wir den statt BuildPrompt — der Client hat dann
	// die Inhalte clientseitig redactet, was tatsächlich rausgeht == was der
	// User im Modal gesehen hat.
	userPrompt := req.UserPromptOverride
	if userPrompt == "" {
		userPrompt = llm.BuildPrompt(cardCtx.Card, req.IncludeFields, cardCtx.RefsByID)
	}

	// Provider-Call — mit eigener Gesamtfrist (#544-Nacharbeit). Seit F-1126
	// hat der geteilte LLM-Client kein Client.Timeout mehr; ohne diese Frist
	// hinge ein Anbieter, der die Header schickt und dann schweigt, so lange
	// wie der Request lebt. Die Schreibfrist des http.Servers (30 s, F-197)
	// wird für DIESE Antwort passend verlängert — sonst stürbe jede Antwort,
	// die länger als 30 s braucht, beim Schreiben, obwohl das Modell fertig ist.
	sendCtx, cancelSend := context.WithTimeout(r.Context(), llmSendTimeout)
	defer cancelSend()
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(llmSendTimeout + llmSendSchreibReserve)); err != nil {
		log.Printf("llm-send: SetWriteDeadline nicht möglich (%v) — es gilt die Server-Frist", err)
	}
	result, err := provider.Send(sendCtx, apiKey, llm.SendRequest{
		Model:      req.Model,
		System:     req.SystemPrompt,
		UserPrompt: userPrompt,
		MaxTokens:  req.MaxTokens,
		User:       a.anbieterNutzerFeld(u.Email),
	})
	if err != nil {
		switch {
		case errors.Is(err, llm.ErrInvalidKey):
			writeSendError(w, http.StatusUnprocessableEntity, "invalid-key", "provider rejected api key")
		case anbieterFehlerArt(err) != "":
			// Karte #665: Fehlerart aus der 4xx-Antwort des Anbieters. 400 mit
			// kind — 502 hieße im Frontend „nicht erreichbar“.
			writeSendError(w, http.StatusBadRequest, anbieterFehlerArt(err), anbieterFehlerMeldung(err), err)
		case errors.Is(err, llm.ErrProviderUnreachable):
			writeSendError(w, http.StatusBadGateway, "unreachable", "provider unreachable or timeout")
		default:
			writeSendError(w, http.StatusBadGateway, "provider", redactString(err.Error()), err)
		}
		return
	}

	// Cap auf MaxLLMReplyBodyBytes — Provider-Runaway-Schutz.
	body := result.Text
	if len(body) > MaxLLMReplyBodyBytes {
		body = body[:MaxLLMReplyBodyBytes] + "\n\n[…Antwort gekürzt]"
	}

	// Pricing-Lookup für Cost-Estimate.
	inputPM, outputPM := llm.PricingBei(provider, req.Model, result.InputTokens)
	cost := (float64(result.InputTokens)/1_000_000)*inputPM + (float64(result.OutputTokens)/1_000_000)*outputPM

	// Karte #674 (ADR-0133 Nachtrag 2 A5): im Firmenmodus kein Kommentar und
	// kein Aktivitätseintrag — die Antwort steht nur in dieser Antwort.
	var commentID string
	if a.antwortAlsKommentar() {
		// Kommentar + Activity persistieren.
		commentID = uuid.NewString()
		now := time.Now().UTC().Format(time.RFC3339)
		contextNote := buildContextNote(req.IncludeFields)
		// #301/ADR-0097 — bei aktivem Client-Override (PII-Filter) ist
		// IncludeFields keine zuverlässige Aussage mehr über das, was
		// tatsächlich an den Provider ging. Audit-Note explizit markieren,
		// damit Compliance-Aussagen ehrlich bleiben (Kritiker-Finding S-P1).
		if req.UserPromptOverride != "" {
			contextNote = fmt.Sprintf("Client-Override (PII-Filter aktiv, %d B); ursprüngliche Auswahl: %s",
				len(req.UserPromptOverride), contextNote)
		}
		authorLabel := fmt.Sprintf("🤖 %s/%s (im Auftrag von %s)", req.Provider, req.Model, u.Email)
		if err := llm.AddLLMReply(r.Context(), a.boards, llm.LLMReplyParams{
			BoardID:         boardID,
			CardID:          cardID,
			CommentID:       commentID,
			Author:          authorLabel,
			Body:            body,
			Created:         now,
			ActivityTS:      now,
			ActivityActor:   u.Email,
			Provider:        req.Provider,
			Model:           req.Model,
			InputTokens:     result.InputTokens,
			OutputTokens:    result.OutputTokens,
			CostEstimateUSD: cost,
			ContextNote:     contextNote,
		}); err != nil {
			writeSendError(w, http.StatusInternalServerError, "internal", "persist reply failed", err)
			return
		}
	}

	// Best-effort: last_used_at aktualisieren (nicht im Firmenmodus — dort
	// wurde kein Nutzerschlüssel benutzt).
	a.markiereSchluesselBenutzt(r.Context(), u.ID, req.Provider)

	antwort := map[string]any{
		"ok":              true,
		"replyText":       body, // Etappe 3: identisch zum persistierten Kommentar-Body, gecappt.
		"inputTokens":     result.InputTokens,
		"outputTokens":    result.OutputTokens,
		"costEstimateUSD": cost,
		"finishReason":    result.FinishReason,
		// K2-4: Der Lauf sagt selbst, ob er im Firmenmodus lief. Die Oberfläche
		// richtet Protokoll und Knöpfe danach, nicht nach ihrer Schlüssel-Liste
		// (die kann zwischendurch verworfen oder leer geladen sein).
		"firmenmodus": a.firmenmodus.Aktiv(),
	}
	if commentID != "" {
		antwort["commentId"] = commentID
	}
	writeJSON(w, http.StatusOK, antwort)
}

type llmDiscardRequest struct {
	CommentID string `json:"commentId"`
	// Provider/Model werden vom Frontend mitgeschickt, damit der Discard-
	// Activity-Eintrag dieselbe to-Spalte trägt wie der Original-llm-sent-
	// Eintrag (Audit-Symmetrie). Wenn das Frontend sie nicht hat, kann der
	// Server-Endpoint sie aus dem Activity-Log rekonstruieren — im MVP
	// verlangen wir sie aber explizit (saubere Trennung).
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (a *LLMSendAPI) handleDiscard(w http.ResponseWriter, r *http.Request) {
	// Sender-Identität (für Activity-Actor).
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
	boardID := r.PathValue("boardId")
	cardID := r.PathValue("cardId")
	if boardID == "" || cardID == "" {
		writeSendError(w, http.StatusBadRequest, "internal", "board/card id required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxLLMSendRequestBytes)
	var req llmDiscardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSendError(w, http.StatusBadRequest, "internal", "invalid json body")
		return
	}
	if req.CommentID == "" {
		writeSendError(w, http.StatusBadRequest, "internal", "commentId required")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := llm.DiscardLLMReply(r.Context(), a.boards, llm.DiscardLLMReplyParams{
		BoardID:       boardID,
		CardID:        cardID,
		CommentID:     req.CommentID,
		ActivityTS:    now,
		ActivityActor: email,
		Provider:      req.Provider,
		Model:         req.Model,
	}); err != nil {
		if errors.Is(err, llm.ErrLLMCommentNotFound) {
			writeSendError(w, http.StatusNotFound, "internal", "comment not found on card")
			return
		}
		writeSendError(w, http.StatusInternalServerError, "internal", "discard failed", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePending — Multi-User-Lock-Status für eine Karte (ADR-0095 §5,
// Etappe 4). Liefert den aktuellen Lock-Holder oder null. Email-Mapping
// erfolgt hier (LockHolder speichert User-ID; das Frontend bekommt Email).
// isSelf ist true, wenn der Caller selbst den Lock hält — Frontend
// unterdrückt dann den „wird verarbeitet von"-Banner für eigene Sends.
// Im Firmenmodus erfahren alle anderen nur „belegt seit" (F-1553,
// sperrHalter).
func (a *LLMSendAPI) handlePending(w http.ResponseWriter, r *http.Request) {
	// Auth/ACL läuft über ACLGuard (viewer+ für GET-Sub-Resource).
	boardID := r.PathValue("boardId")
	cardID := r.PathValue("cardId")
	if boardID == "" || cardID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"holder": nil})
		return
	}
	// K2-3: Die ACL des Guards gilt dem Board im Pfad, nicht der Karte. Ohne
	// diese Prüfung erführe ein Leser irgendeines Boards mit einer bekannten
	// Karten-UUID E-Mail, Modell und Start einer fremden Anfrage. Die Karte
	// muss also in genau diesem Board liegen — sonst 404 wie bei llm-send.
	if _, err := loadCardForLLM(r, a.boards, boardID, cardID); err != nil {
		if errors.Is(err, errCardNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]any{"holder": nil})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"holder": nil})
		return
	}
	holder, ok := a.locks.Holder(llm.SperrSchluessel(boardID, cardID))
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"holder": nil, "isSelf": false})
		return
	}

	// Self-Check über Caller-Identity.
	isSelf := false
	if id := IdentityFromContext(r.Context()); id != nil {
		callerEmail := storage.NormalizeEmail(id.Email)
		if callerEmail != "" && a.users != nil {
			if cu, err := a.users.GetByEmail(r.Context(), callerEmail); err == nil && cu != nil {
				isSelf = cu.ID == holder.User
			}
		}
	}

	// User-ID → Email mappen für UI-Anzeige — nur, wenn die Person überhaupt
	// genannt wird (F-1553: im Firmenmodus nur der fragenden Person selbst).
	holderEmail := ""
	if (isSelf || !a.firmenmodus.Aktiv()) && a.users != nil {
		if u, err := a.users.GetByID(r.Context(), holder.User); err == nil && u != nil {
			holderEmail = u.Email
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"holder": a.sperrHalter(holder, holderEmail, isSelf),
		"isSelf": isSelf,
	})
}

func (a *LLMSendAPI) handlePricing(w http.ResponseWriter, r *http.Request) {
	// Keine ACL-Sensibilität: Preise sind öffentlich publizierte Tarife.
	// Authentifizierung reicht (Forward-Auth-Bypass für ungestützte Setups
	// greift wie überall sonst).
	writeJSON(w, http.StatusOK, pricingAntwort())
}

// llmBildGrenzen — die Bild-Grenzen aus server/llm/vision.go für das
// Frontend (F-858). Vertrag mit src/llm-tab.js: Feld `limits` mit genau
// diesen drei Schlüsseln. Das Frontend führte die Werte vorher als eigene
// Literale (MAX_BILD_BYTES, MAX_BILDER) — dieselbe Klasse „zweite Liste an
// anderer Stelle veraltet still", die ADR-0121 §4 für die Vision-Fähigkeit
// ausdrücklich vermeidet; die Gesamtgrenze fehlte dort ganz.
type llmBildGrenzen struct {
	MaxImageBytes      int64 `json:"maxImageBytes"`
	MaxImages          int   `json:"maxImages"`
	MaxImageBytesTotal int64 `json:"maxImageBytesTotal"`
}

// pricingAntwort baut die Antwort von GET /llm/pricing: die Preistabellen je
// Anbieter (unverändert auf oberster Ebene, wie seit ADR-0095) plus das Feld
// `limits`. Kein Anbieter heisst „limits"; die Leser im Frontend greifen
// ausschliesslich über den Anbieternamen zu.
func pricingAntwort() map[string]any {
	out := map[string]any{}
	for name, tabelle := range llm.AllPricing() {
		out[name] = tabelle
	}
	out["limits"] = llmBildGrenzen{
		MaxImageBytes:      llm.MaxImageBytes,
		MaxImages:          llm.MaxImagesPerSend,
		MaxImageBytesTotal: llm.MaxImageBytesTotal,
	}
	return out
}

// llmCardContext bündelt, was ein LLM-Send aus dem Board-State braucht.
//
// Der Karten-Index (CardsByID) ist der SNAPSHOT des Turns: Der agentische
// Fetch-Loop (ADR-0120) löst alle Nachlade-Anfragen gegen genau diesen
// Stand auf, nicht gegen jeweils frisch gelesene Boards. Sonst könnte sich
// die Verweis-Lage zwischen zwei Fetches desselben Turns ändern, und die
// One-Hop-Zusicherung gälte je Anfrage gegen ein anderes Board.
type llmCardContext struct {
	Card      map[string]any
	RefsByID  map[string]llm.RefSummary
	ValidCols map[string]bool
	CardsByID map[string]map[string]any
}

// loadCardForLLM lädt die Karten-Map + Refs-Map aus dem Board-State.
// Refs werden aus card.links.targetId aufgelöst — nur Karten desselben
// Boards (lokale Verweise; Cross-Board-Refs siehe #303).
func loadCardForLLM(r *http.Request, boards *storage.BoardRepo, boardID, cardID string) (llmCardContext, error) {
	b, err := boards.Get(r.Context(), boardID)
	if err != nil {
		return llmCardContext{}, fmt.Errorf("get board: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(b.StateJSON, &root); err != nil {
		return llmCardContext{}, fmt.Errorf("parse state_json: %w", err)
	}
	boardObj, _ := root["board"].(map[string]any)
	sprints, _ := boardObj["sprints"].([]any)

	// Gültige Spalten-IDs für die move-Ziel-Prüfung (F-410 / ADR-0096 §3 Schritt 4).
	validCols := map[string]bool{}
	if cds, ok := boardObj["columnDefs"].([]any); ok {
		for _, cd := range cds {
			if cm, ok := cd.(map[string]any); ok {
				if id, _ := cm["id"].(string); id != "" {
					validCols[id] = true
				}
			}
		}
	}

	// Zwei Indizes, und der Unterschied ist die eigentliche Aussage (F-859).
	//
	// `alleCards` findet die Karte, um die es geht. Sie darf archiviert sein:
	// Der Nutzer hat sie ausdrücklich geöffnet, und ihm den KI-Tab an einer
	// archivierten Karte zu verweigern wäre eine Regression ohne Anlass.
	//
	// `cardsByID` ist der Nachlade-Snapshot und die Grundlage der Refs — und
	// hier gilt das Gegenteil: Was der Nutzer im Board weggeräumt hat, geht
	// nicht unaufgefordert an ein fremdes Modell. Archiviert ist eine Karte
	// dabei auch dann, wenn ihre SWIMLANE archiviert ist (ADR-0117): Das
	// Swimlane-Flag kaskadiert bewusst nicht auf die Karten, verbirgt aber
	// die ganze Lane — für „hat der Nutzer das weggeräumt?" zählen beide.
	// Dieselbe Regel lebt der Board-Kontext für Agenten schon
	// (framework/hooks/aboard-context.sh blendet archivierte Swimlanes aus);
	// hier war sie die Ausnahme.
	//
	// Eine archivierte, verknüpfte Karte fällt damit in den „nicht
	// gefunden"-Pfad von resolveFetch — der bewusst dieselbe Auskunft gibt
	// wie „nicht verknüpft", weil die Unterscheidung ein Orakel über den
	// Kartenbestand wäre. Kein zusätzliches Leck, keine neue Ablehnungsart.
	alleCards := map[string]map[string]any{}
	cardsByID := map[string]map[string]any{}
	for _, sp := range sprints {
		spMap, _ := sp.(map[string]any)
		laneArchiviert, _ := spMap["archived"].(bool)
		cards, _ := spMap["cards"].([]any)
		for _, c := range cards {
			cm, _ := c.(map[string]any)
			id, _ := cm["id"].(string)
			if id == "" {
				continue
			}
			alleCards[id] = cm
			kartenArchiv, _ := cm["archived"].(bool)
			if laneArchiviert || kartenArchiv {
				continue
			}
			cardsByID[id] = cm
		}
	}

	card, ok := alleCards[cardID]
	if !ok {
		return llmCardContext{}, errCardNotFound
	}

	// Refs sammeln: nur card.links.targetIds, die im Board existieren.
	refs := map[string]llm.RefSummary{}
	if links, ok := card["links"].([]any); ok {
		for _, l := range links {
			lm, _ := l.(map[string]any)
			targetID, _ := lm["targetId"].(string)
			if targetID == "" {
				continue
			}
			target, ok := cardsByID[targetID]
			if !ok {
				continue // Dangling ignorieren (Karten-Konsistenz-Check ist /projekt-start-Job)
			}
			title, _ := target["title"].(string)
			desc, _ := target["description"].(string)
			displayID := 0
			if v, ok := target["displayId"].(float64); ok {
				displayID = int(v)
			}
			refs[targetID] = llm.RefSummary{
				DisplayID: displayID,
				Title:     title,
				Snippet:   llm.TruncateSnippet(desc),
			}
		}
	}
	return llmCardContext{
		Card:      card,
		RefsByID:  refs,
		ValidCols: validCols,
		CardsByID: cardsByID,
	}, nil
}

// buildContextNote erzeugt aus der Feld-Auswahl eine menschenlesbare
// Zusammenfassung für den Activity-Entry (z. B. "Beschreibung + Kommentare").
func buildContextNote(fields []string) string {
	labels := map[string]string{
		"description":  "Beschreibung",
		"comments":     "Kommentare",
		"links":        "Verweise",
		"agentContext": "Agent-Kontext",
		"activity":     "Aktivität",
	}
	out := ""
	for _, f := range fields {
		if l, ok := labels[f]; ok {
			if out != "" {
				out += " + "
			}
			out += l
		}
	}
	return out
}

// llmSendTimeout ist die Gesamtfrist eines nicht gestreamten LLM-Aufrufs
// (#544-Nacharbeit). Großzügig, weil eine vollständige Antwort mit großem
// max_tokens Minuten brauchen kann; begrenzt, weil der Client dafür seit
// F-1126 keine eigene Gesamtfrist mehr hat. Wer längere Antworten braucht,
// nimmt den Stream-Endpunkt.
const llmSendTimeout = 2 * time.Minute

// llmSendSchreibReserve — so viel länger als llmSendTimeout darf das
// Schreiben der Antwort dauern (Fehler- oder Ergebnis-JSON nach Fristende).
const llmSendSchreibReserve = 15 * time.Second
