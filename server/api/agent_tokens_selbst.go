package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Selbstbedienung für Agenten-Zugänge (ADR-0130 §6 W2, Karte #608).
//
// Eine Person legt sich für ein Board, auf dem sie mindestens editor ist,
// selbst ein Token an — sofern der Admin die Selbstbedienung für dieses Board
// eingeschaltet hat (ab Werk aus). Das Token gehört der Person: Der Agent
// handelt „für“ sie, seine Rolle ist nie höher als ihre, und er verliert den
// Zugang, sobald sie ihn verliert. Widerrufen kann die Person nur eigene
// Tokens; der Admin sieht und widerruft alle (/admin/agent-tokens).
//
// Nur auf dem Menschen-Weg: Die Positivliste des Agenten-Zugangs kennt
// /users nicht — ein Agent legt sich keinen weiteren Agenten an.
//
//	GET    /users/me/agent-tokens        eigene Tokens + Boards mit Selbstbedienung
//	POST   /users/me/agent-tokens        {board, name, role, gueltig_tage, anbieter, beleg} → Klartext EINMAL
//	DELETE /users/me/agent-tokens/{id}   eigenes Token widerrufen

type agentTokenSelbst struct {
	repo    *storage.AgentTokenRepo
	users   *storage.UserRepo
	zugang  *AgentZugang
	limiter *rateLimiter
	// cfg: Seite des Board-Paars — auf der Seite außen sind Anbieter und
	// Beleg Pflicht (Karte #799).
	cfg *storage.ConfigRepo
}

// RegisterAgentTokensSelbst hängt die Routen an den Mux.
func RegisterAgentTokensSelbst(mux *http.ServeMux, repo *storage.AgentTokenRepo, users *storage.UserRepo, zugang *AgentZugang, cfg *storage.ConfigRepo) {
	h := &agentTokenSelbst{repo: repo, users: users, zugang: zugang, limiter: newRateLimiter(20, time.Hour), cfg: cfg}
	mux.Handle("GET /users/me/agent-tokens", h.schutz(h.liste, false))
	mux.Handle("POST /users/me/agent-tokens", h.schutz(h.anlegen, true))
	mux.Handle("DELETE /users/me/agent-tokens/{id}", h.schutz(h.widerrufen, true))
}

// schutz: Menschen-Identität mit users-Zeile (wie /users/me/llm-keys), ein
// Agent kommt hier nie an (Positivliste), wird aber zur Sicherheit auch hier
// abgewiesen.
func (h *agentTokenSelbst) schutz(fn func(w http.ResponseWriter, r *http.Request, u *storage.User), mutation bool) http.Handler {
	return WithDefaults(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if AgentFromContext(r.Context()) != nil {
			writeError(w, http.StatusForbidden, "forbidden for agent")
			return
		}
		id := IdentityFromContext(r.Context())
		if id == nil || storage.NormalizeEmail(id.Email) == "" {
			writeError(w, http.StatusForbidden, "forbidden: agent access requires authentication")
			return
		}
		u, err := h.users.GetByEmail(r.Context(), id.Email)
		if errors.Is(err, storage.ErrUserNotFound) {
			writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "user lookup failed", err)
			return
		}
		if mutation && !h.limiter.allow(u.ID) {
			writeError(w, http.StatusTooManyRequests, "rate limit: 20 changes per hour per user")
			return
		}
		fn(w, r, u)
	}))
}

func (h *agentTokenSelbst) liste(w http.ResponseWriter, r *http.Request, u *storage.User) {
	tokens, err := h.repo.ListFuerPerson(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	boards, err := h.repo.SelbstbedienungsBoardsFuer(r.Context(), u.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tokens": tokens, "boards": boards,
		"gueltig_standard": storage.AgentTokenGueltigStandard, "gueltig_max": storage.AgentTokenGueltigMax,
	})
}

func (h *agentTokenSelbst) anlegen(w http.ResponseWriter, r *http.Request, u *storage.User) {
	// 16 KiB: Platz für die Board-Liste eines Tokens über mehrere Boards
	// (höchstens 200 IDs, Karte #754, Kritiker K-5).
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var body struct {
		Board       string `json:"board"`
		Name        string `json:"name"`
		Role        string `json:"role"`
		GueltigTage int    `json:"gueltig_tage"`
		// Karte #754 (ADR-0139 §5): mehrere Boards statt board — nur Boards
		// mit persönlicher Rolle ≥ editor und eingeschaltetem Schalter.
		Boards     []string `json:"boards"`
		AlleBoards bool     `json:"alle_boards"`
		// Karte #799 (ADR-0140 §8): Pflicht auf der Seite außen.
		Anbieter string `json:"anbieter"`
		Beleg    string `json:"beleg"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	anbieter, beleg, grund, status := PruefeAgentHerkunft(r.Context(), h.cfg, body.Anbieter, body.Beleg)
	if grund != "" {
		writeError(w, status, grund)
		return
	}
	if body.Role == "" {
		body.Role = storage.AgentRolleEditor
	}
	klartext, t, err := h.repo.Anlegen(r.Context(), storage.AgentTokenAnlage{
		Name: body.Name, BoardID: body.Board, Role: body.Role, CreatedBy: u.ID,
		UserID: u.ID, GueltigTage: body.GueltigTage, Selbstbedienung: true,
		BoardIDs: body.Boards, AlleMeineBoards: body.AlleBoards, Anbieter: anbieter, Beleg: beleg,
	})
	switch {
	case errors.Is(err, storage.ErrAgentEingabe):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, storage.ErrBoardNotFound), errors.Is(err, storage.ErrAgentKeinZugang):
		// Kein Unterschied nach aussen: Wer das Board nicht (als editor)
		// sieht, erfährt nicht, dass es existiert.
		if len(body.Boards) > 0 || body.AlleBoards {
			writeError(w, http.StatusForbidden, "no editor role or self-service off on at least one of these boards")
			return
		}
		writeError(w, http.StatusForbidden, "no editor role on this board")
		return
	case errors.Is(err, storage.ErrAgentSelbstbedienungAus):
		writeError(w, http.StatusForbidden, "self-service for agent access is off on this board (ask an admin)")
		return
	case errors.Is(err, storage.ErrAgentNameVergeben):
		// Karte #638 (F-1335): eine Meldung für alle Gründe — eigenes aktives
		// Token, fremde Person, Projekt-Token. Ein Hinweis auf „eine andere
		// Person“ bestätigte dem Fragenden, dass es den Namen bei jemandem
		// gibt. Dass der Name nicht geht, lässt sich bei serverweit
		// eindeutigen Namen nicht verbergen (ADR-0130 §6, Nachtrag Sprint 80).
		writeError(w, http.StatusConflict, "agent name not available, choose another")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "create failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":   klartext,
		"agent":   t,
		"hinweis": "Das Token wird nur jetzt angezeigt.",
	})
}

func (h *agentTokenSelbst) widerrufen(w http.ResponseWriter, r *http.Request, u *storage.User) {
	id := r.PathValue("id")
	name, err := h.repo.RevokeID(r.Context(), id, u.ID, u.ID)
	if errors.Is(err, storage.ErrAgentTokenUnbekannt) {
		writeError(w, http.StatusNotFound, "no active token with this id")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "revoke failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"widerrufen": name, "verbindungen_geschlossen": h.zugang.schliesseAlleIDs([]string{id})})
}
