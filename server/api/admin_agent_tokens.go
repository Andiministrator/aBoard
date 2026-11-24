package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Admin-Endpunkte für Agenten-Tokens (ADR-0129 §3, Karte #553). Dieselben
// Schutzhüllen wie die übrige Admin-API: nur Admins, Mutationen mit
// Rate-Limit. Ohne Identität (Legacy-Bypass) gibt es keinen Admin → 403;
// dort bleibt die CLI `aboard-server agent-token`.
//
// Die Routen liegen auf dem gemeinsamen Mux, sind vom Agenten-Listener aus
// aber durch die Positivliste gesperrt (agentDarf kennt /admin nicht).
func (a *AdminAPI) RegisterAgentTokens(mux *http.ServeMux, repo *storage.AgentTokenRepo, zugang *AgentZugang) {
	h := &agentTokenAdmin{repo: repo, zugang: zugang, users: a.users, cfg: a.cfg}
	a.agentZugang = zugang
	mux.Handle("GET /admin/agent-tokens", a.protect(h.liste))
	mux.Handle("POST /admin/agent-tokens", a.protectMutate(h.anlegen))
	mux.Handle("DELETE /admin/agent-tokens/{idOderName}", a.protectMutate(h.widerrufen))
	// Karte #608 (ADR-0130 §6): Team-Austrittsweg und Admin-Schalter je Board.
	mux.Handle("POST /admin/users/{id}/agent-tokens/revoke", a.protectMutate(h.widerrufenFuerPerson))
	mux.Handle("GET /admin/agent-selbstbedienung", a.protect(h.selbstbedienungListe))
	mux.Handle("PUT /admin/boards/{id}/agent-selbstbedienung", a.protectMutate(h.selbstbedienungSetzen))
}

type agentTokenAdmin struct {
	repo   *storage.AgentTokenRepo
	zugang *AgentZugang
	users  *storage.UserRepo
	// cfg: Seite des Board-Paars — die Rolle intern gibt es nur außen (#783).
	cfg *storage.ConfigRepo
}

// schliesse beendet die offenen Verbindungen der genannten Tokens.
func (z *AgentZugang) schliesseAlleIDs(ids []string) int {
	if z == nil {
		return 0
	}
	n := 0
	for _, id := range ids {
		n += z.SchliesseAlle(id)
	}
	return n
}

func (h *agentTokenAdmin) liste(w http.ResponseWriter, r *http.Request) {
	l, err := h.repo.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// anlegen liefert den Klartext genau einmal in der Antwort.
func (h *agentTokenAdmin) anlegen(w http.ResponseWriter, r *http.Request) {
	// 16 KiB: Platz für die Board-Liste eines Tokens über mehrere Boards
	// (höchstens 200 IDs, Karte #754, Kritiker K-5).
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var body struct {
		Name  string `json:"name"`
		Board string `json:"board"`
		Role  string `json:"role"`
		// Karte #608: optional die Person (E-Mail), für die der Agent handelt,
		// und die Gültigkeit in Tagen (0 = 90, höchstens 365).
		User        string `json:"user"`
		GueltigTage int    `json:"gueltig_tage"`
		// Karte #754 (ADR-0139 §5): Token einer Person für mehrere Boards —
		// eine feste Liste oder „alle meine Boards“ (Stand jetzt). Statt board.
		Boards     []string `json:"boards"`
		AlleBoards bool     `json:"alle_boards"`
		// Karte #799 (ADR-0140 §8): wer den Agenten betreibt und wo der
		// Trainingsausschluss belegt ist — Pflicht auf der Seite außen.
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
	anlage := storage.AgentTokenAnlage{Name: body.Name, BoardID: body.Board, Role: body.Role,
		CreatedBy: adminUserIDFromContext(r.Context()), GueltigTage: body.GueltigTage,
		BoardIDs: body.Boards, AlleMeineBoards: body.AlleBoards, Anbieter: anbieter, Beleg: beleg}
	if strings.TrimSpace(body.User) != "" {
		u, err := h.users.GetByEmail(r.Context(), body.User)
		if errors.Is(err, storage.ErrUserNotFound) {
			writeError(w, http.StatusBadRequest, "user not found")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "user lookup failed", err)
			return
		}
		anlage.UserID = u.ID
	}
	if body.Role == storage.AgentRolleIntern {
		if grund, status := InternRolleAnlegbar(r.Context(), h.cfg); grund != "" {
			writeError(w, status, grund)
			return
		}
	}
	klartext, t, err := h.repo.Anlegen(r.Context(), anlage)
	switch {
	case errors.Is(err, storage.ErrAgentKeinZugang):
		if len(anlage.BoardIDs) > 0 || anlage.AlleMeineBoards {
			writeError(w, http.StatusBadRequest, "user has no personal editor role on at least one of these boards (or the list is empty)")
			return
		}
		writeError(w, http.StatusBadRequest, "user has no editor role on this board")
		return
	case errors.Is(err, storage.ErrAgentEingabe):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, storage.ErrBoardNotFound):
		writeError(w, http.StatusBadRequest, "board not found")
		return
	case errors.Is(err, storage.ErrAgentNameVergeben):
		// Admins sehen alle Tokens (GET /admin/agent-tokens) — der genaue
		// Grund verrät ihnen nichts Neues und spart die Suche (Karte #638).
		writeError(w, http.StatusConflict, "agent name not available (an active token uses it, or it belongs to another person or a project token; see GET /admin/agent-tokens)")
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

// widerrufen schliesst zusätzlich alle offenen Verbindungen des Tokens (K-4).
//
// Der Pfadteil ist die Token-id (bevorzugt, F-1434) oder — rückwärtskompatibel
// für Admin-Dialog und Skripte — der Agentenname. Zuerst gilt die id; passt
// keine aktive id, wird als Name gelesen. Tragen mehrere aktive Tokens den
// Namen, antwortet der Endpunkt 409 statt ein beliebiges zu widerrufen.
func (h *agentTokenAdmin) widerrufen(w http.ResponseWriter, r *http.Request) {
	ziel := r.PathValue("idOderName")
	by := adminUserIDFromContext(r.Context())
	name, err := h.repo.RevokeID(r.Context(), ziel, "", by)
	id := ziel
	if errors.Is(err, storage.ErrAgentTokenUnbekannt) {
		name = ziel
		id, err = h.repo.Revoke(r.Context(), ziel, by)
	}
	if errors.Is(err, storage.ErrAgentNameMehrdeutig) {
		writeError(w, http.StatusConflict, "several active tokens use this name; revoke by token id (GET /admin/agent-tokens)")
		return
	}
	if errors.Is(err, storage.ErrAgentTokenUnbekannt) {
		writeError(w, http.StatusNotFound, "no active token for this agent")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "revoke failed", err)
		return
	}
	geschlossen := 0
	if h.zugang != nil {
		geschlossen = h.zugang.SchliesseAlle(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"widerrufen": name, "verbindungen_geschlossen": geschlossen})
}

// widerrufenFuerPerson widerruft alle aktiven Tokens einer Person — der
// Austrittsweg im Team-Betrieb, auch ohne den Nutzer zu löschen (ADR-0130 §6).
func (h *agentTokenAdmin) widerrufenFuerPerson(w http.ResponseWriter, r *http.Request) {
	ids, err := h.repo.RevokeFuerPerson(r.Context(), r.PathValue("id"), adminUserIDFromContext(r.Context()))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "revoke failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"widerrufen": len(ids), "verbindungen_geschlossen": h.zugang.schliesseAlleIDs(ids),
	})
}

func (h *agentTokenAdmin) selbstbedienungListe(w http.ResponseWriter, r *http.Request) {
	l, err := h.repo.SelbstbedienungsUebersicht(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"boards": l})
}

// selbstbedienungSetzen: {"an": true|false}. Bestehende Tokens bleiben — der
// Schalter regelt nur, ob Personen sich selbst neue anlegen dürfen.
func (h *agentTokenAdmin) selbstbedienungSetzen(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body struct {
		An *bool `json:"an"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.An == nil {
		writeError(w, http.StatusBadRequest, `body must be {"an": true|false}`)
		return
	}
	err := h.repo.SetzeSelbstbedienung(r.Context(), r.PathValue("id"), *body.An, adminUserIDFromContext(r.Context()))
	if errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"board": r.PathValue("id"), "an": *body.An})
}
