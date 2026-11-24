package api

// llm_conversations.go — CRUD-Endpoints für Multi-Turn-LLM-Conversations
// (#300, ADR-0096 §2 Etappe 2).
//
// Routen (alle unter /boards/{b}/cards/{c}/conversations, damit ACLGuard greift):
//
//   POST   /boards/{b}/cards/{c}/conversations
//          Body: { provider, model }
//          Response: 201 Created mit Conversation-Header (id, createdAt, ...)
//          ACL: editor+ am Board (ACLGuard Sub-Resource-POST).
//
//   GET    /boards/{b}/cards/{c}/conversations[?includeArchived=true]
//          Response: Liste aktiver Conversations (LIMIT 10, neueste zuerst).
//          ACL: viewer+ am Board.
//
//   GET    /boards/{b}/cards/{c}/conversations/{convId}/turns
//          Response: Turns einer Conversation in chronologischer Reihenfolge.
//          ACL: viewer+ am Board; Server prüft zusätzlich, dass die
//          Conversation auf dieser Karte liegt (404 sonst).
//
//   POST   /boards/{b}/cards/{c}/conversations/{convId}/archive
//          Response: 204 No Content.
//          ACL: editor+ am Board + caller-email == created_by (ADR-0096 §2).
//
//   DELETE /boards/{b}/cards/{c}/conversations/{convId}
//          Response: 204 No Content. Turns werden via FK CASCADE entfernt.
//          ACL: editor+ am Board (ACLGuard) + caller-email == created_by.
//
// Firmenmodus (ABOARD_LLM_SERVERSCHLUESSEL_FILE, ADR-0133 Nachtrag 2 A5):
// Verläufe sind NUR für ihren Ersteller sichtbar — ohne eigenen Schalter und
// ohne Admin-Ausnahme. Die Liste enthält nur eigene Verläufe; Turns,
// Archivieren und Löschen eines fremden Verlaufs antworten 404, nicht 403,
// damit nicht einmal die Existenz eines fremden Verlaufs erkennbar ist.
// Dieselbe Regel gilt beim Fortsetzen über llm-send-stream (llm_stream.go).
// Ohne Firmenmodus gilt ADR-0096 unverändert: Lesen viewer+.

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// LLMConversationsAPI bündelt die Abhängigkeiten für die Conversation-CRUD.
// boards wird aktuell nicht direkt benötigt — wenn wir später Karten-
// existenz prüfen wollen, ist die Referenz schon da.
type LLMConversationsAPI struct {
	convs *storage.LLMConversationRepo
	users *storage.UserRepo
	// Karte #674: nil = kein Firmenmodus (Lesen viewer+, ADR-0096).
	firmenmodus *llm.Firmenmodus
}

func NewLLMConversationsAPI(convs *storage.LLMConversationRepo, users *storage.UserRepo) *LLMConversationsAPI {
	return &LLMConversationsAPI{convs: convs, users: users}
}

// WithFirmenmodus trägt den Firmenmodus ein (nil = aus). Derselbe Wert wie
// bei LLMSendAPI/LLMKeysAPI — keine zweite Erkennung (Karte #674).
func (a *LLMConversationsAPI) WithFirmenmodus(f *llm.Firmenmodus) *LLMConversationsAPI {
	a.firmenmodus = f
	return a
}

// nurErstellerSichtbar meldet, ob c für den Caller im Firmenmodus unsichtbar
// ist (fremder Verlauf). Ohne Firmenmodus immer false.
func (a *LLMConversationsAPI) nurErstellerSichtbar(email string, c storage.LLMConversation) bool {
	return a.firmenmodus.Aktiv() && storage.NormalizeEmail(c.CreatedBy) != email
}

func (a *LLMConversationsAPI) Register(mux *http.ServeMux) {
	mux.Handle("POST /boards/{boardId}/cards/{cardId}/conversations",
		WithDefaults(http.HandlerFunc(a.handleCreate)))
	mux.Handle("GET /boards/{boardId}/cards/{cardId}/conversations",
		WithDefaults(http.HandlerFunc(a.handleList)))
	mux.Handle("GET /boards/{boardId}/cards/{cardId}/conversations/{convId}/turns",
		WithDefaults(http.HandlerFunc(a.handleListTurns)))
	mux.Handle("POST /boards/{boardId}/cards/{cardId}/conversations/{convId}/archive",
		WithDefaults(http.HandlerFunc(a.handleArchive)))
	mux.Handle("DELETE /boards/{boardId}/cards/{cardId}/conversations/{convId}",
		WithDefaults(http.HandlerFunc(a.handleDelete)))
}

// callerEmail extrahiert die normalisierte Caller-Email aus dem Identity-
// Context. Bei fehlender Identity 403.
func callerEmail(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := IdentityFromContext(r.Context())
	if id == nil {
		writeSendError(w, http.StatusForbidden, "internal", "forbidden: authentication required")
		return "", false
	}
	email := storage.NormalizeEmail(id.Email)
	if email == "" {
		writeSendError(w, http.StatusForbidden, "internal", "forbidden: no identity email")
		return "", false
	}
	return email, true
}

type createConvRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (a *LLMConversationsAPI) handleCreate(w http.ResponseWriter, r *http.Request) {
	email, ok := callerEmail(w, r)
	if !ok {
		return
	}
	boardID := r.PathValue("boardId")
	cardID := r.PathValue("cardId")
	if boardID == "" || cardID == "" {
		writeSendError(w, http.StatusBadRequest, "internal", "board/card id required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxLLMSendRequestBytes)
	var req createConvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSendError(w, http.StatusBadRequest, "internal", "invalid json body")
		return
	}
	if req.Provider == "" || req.Model == "" {
		writeSendError(w, http.StatusBadRequest, "internal", "provider/model required")
		return
	}
	c, err := a.convs.CreateConversation(r.Context(), storage.CreateConversationParams{
		ID:        uuid.NewString(),
		BoardID:   boardID,
		CardID:    cardID,
		Provider:  req.Provider,
		Model:     req.Model,
		CreatedBy: email,
	})
	if err != nil {
		writeSendError(w, http.StatusInternalServerError, "internal", "create conversation failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *LLMConversationsAPI) handleList(w http.ResponseWriter, r *http.Request) {
	// ACLGuard hat viewer+ bereits geprüft.
	boardID := r.PathValue("boardId")
	cardID := r.PathValue("cardId")
	if boardID == "" || cardID == "" {
		writeSendError(w, http.StatusBadRequest, "internal", "board/card id required")
		return
	}
	includeArchived := r.URL.Query().Get("includeArchived") == "true"
	var (
		list []storage.LLMConversation
		err  error
	)
	if a.firmenmodus.Aktiv() {
		// Firmenmodus (A5): nur eigene Verläufe — gefiltert in der Abfrage,
		// nicht hier (LIMIT 10, siehe ListConversationsForCardBy).
		email, ok := callerEmail(w, r)
		if !ok {
			return
		}
		list, err = a.convs.ListConversationsForCardBy(r.Context(), boardID, cardID, email, includeArchived)
	} else {
		list, err = a.convs.ListConversationsForCard(r.Context(), boardID, cardID, includeArchived)
	}
	if err != nil {
		writeSendError(w, http.StatusInternalServerError, "internal", "list conversations failed", err)
		return
	}
	if list == nil {
		list = []storage.LLMConversation{}
	}
	writeJSON(w, http.StatusOK, list)
}

// loadConvAndCheckPlacement lädt eine Conversation und prüft, dass sie auf
// (boardID, cardID) liegt. 404 bei nicht-existent oder anderer Karte.
// Verhindert IDOR via cross-card convId.
//
// Im Firmenmodus zusätzlich 404, wenn email nicht der Ersteller ist (A5) —
// derselbe Wortlaut wie „gibt es nicht“, damit eine fremde convId nichts
// verrät. Ohne Firmenmodus wird email hier nicht gebraucht.
func (a *LLMConversationsAPI) loadConvAndCheckPlacement(w http.ResponseWriter, r *http.Request, convID, boardID, cardID, email string) (storage.LLMConversation, bool) {
	c, err := a.convs.GetConversation(r.Context(), convID)
	if errors.Is(err, storage.ErrLLMConversationNotFound) {
		writeSendError(w, http.StatusNotFound, "internal", "conversation not found")
		return storage.LLMConversation{}, false
	}
	if err != nil {
		writeSendError(w, http.StatusInternalServerError, "internal", "load conversation failed", err)
		return storage.LLMConversation{}, false
	}
	if c.BoardID != boardID || c.CardID != cardID {
		// Conversation gehört zu einer anderen Karte/Board — als 404 maskieren,
		// um keine Existenz-Information zu leaken.
		writeSendError(w, http.StatusNotFound, "internal", "conversation not found")
		return storage.LLMConversation{}, false
	}
	if a.nurErstellerSichtbar(email, c) {
		writeSendError(w, http.StatusNotFound, "internal", "conversation not found")
		return storage.LLMConversation{}, false
	}
	return c, true
}

func (a *LLMConversationsAPI) handleListTurns(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("boardId")
	cardID := r.PathValue("cardId")
	convID := r.PathValue("convId")
	if boardID == "" || cardID == "" || convID == "" {
		writeSendError(w, http.StatusBadRequest, "internal", "ids required")
		return
	}
	// Ohne Firmenmodus reicht viewer+ (ACLGuard) — die E-Mail wird nur im
	// Firmenmodus gebraucht und nur dort verlangt, damit sich am bisherigen
	// Verhalten nichts ändert.
	var email string
	if a.firmenmodus.Aktiv() {
		var ok bool
		if email, ok = callerEmail(w, r); !ok {
			return
		}
	}
	if _, ok := a.loadConvAndCheckPlacement(w, r, convID, boardID, cardID, email); !ok {
		return
	}
	turns, err := a.convs.LoadTurns(r.Context(), convID)
	if err != nil {
		writeSendError(w, http.StatusInternalServerError, "internal", "load turns failed", err)
		return
	}
	if turns == nil {
		turns = []storage.LLMTurn{}
	}
	writeJSON(w, http.StatusOK, turns)
}

// requireCreator prüft, dass der Caller die Conversation erstellt hat.
// 403 sonst (analog created_by-only-Schreibrecht aus ADR-0096 §2).
func (a *LLMConversationsAPI) requireCreator(w http.ResponseWriter, callerEmail string, c storage.LLMConversation) bool {
	if storage.NormalizeEmail(c.CreatedBy) != callerEmail {
		writeSendError(w, http.StatusForbidden, "internal", "forbidden: only the conversation creator can modify it")
		return false
	}
	return true
}

func (a *LLMConversationsAPI) handleArchive(w http.ResponseWriter, r *http.Request) {
	email, ok := callerEmail(w, r)
	if !ok {
		return
	}
	boardID := r.PathValue("boardId")
	cardID := r.PathValue("cardId")
	convID := r.PathValue("convId")
	if boardID == "" || cardID == "" || convID == "" {
		writeSendError(w, http.StatusBadRequest, "internal", "ids required")
		return
	}
	c, ok := a.loadConvAndCheckPlacement(w, r, convID, boardID, cardID, email)
	if !ok {
		return
	}
	if !a.requireCreator(w, email, c) {
		return
	}
	if err := a.convs.ArchiveConversation(r.Context(), convID); err != nil {
		if errors.Is(err, storage.ErrLLMConversationNotFound) {
			writeSendError(w, http.StatusNotFound, "internal", "conversation not found")
			return
		}
		writeSendError(w, http.StatusInternalServerError, "internal", "archive failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *LLMConversationsAPI) handleDelete(w http.ResponseWriter, r *http.Request) {
	email, ok := callerEmail(w, r)
	if !ok {
		return
	}
	boardID := r.PathValue("boardId")
	cardID := r.PathValue("cardId")
	convID := r.PathValue("convId")
	if boardID == "" || cardID == "" || convID == "" {
		writeSendError(w, http.StatusBadRequest, "internal", "ids required")
		return
	}
	c, ok := a.loadConvAndCheckPlacement(w, r, convID, boardID, cardID, email)
	if !ok {
		return
	}
	if !a.requireCreator(w, email, c) {
		return
	}
	if err := a.convs.DeleteConversation(r.Context(), convID); err != nil {
		if errors.Is(err, storage.ErrLLMConversationNotFound) {
			writeSendError(w, http.StatusNotFound, "internal", "conversation not found")
			return
		}
		writeSendError(w, http.StatusInternalServerError, "internal", "delete failed", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
