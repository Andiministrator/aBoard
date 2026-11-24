package api

// activities_read.go — Lese-Endpunkt fürs Protokoll je Karte
// (ADR-0129 §5, Karte #554): GET /boards/{id}/activities.
//
// Rolle: die Route liegt unter /boards/{id}/..., der ACLGuard (acl.go)
// erzwingt dafür bereits viewer+ für GET (roleAllows, deeper=true) — hier
// keine eigene Rollenprüfung nötig.

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Andiministrator/aBoard/server/storage"
)

// activitiesListDefaultLimit ist die Voreinstellung für ?limit, wenn der
// Parameter fehlt oder nicht als positive ganze Zahl parst.
const activitiesListDefaultLimit = 100

// activitiesListMaxLimit begrenzt ?limit nach oben — ein Client, der mehr
// anfragt, bekommt die Kappung statt eines Fehlers (analog admin.go
// handleAudit: robust statt strikt bei einem reinen Lese-/Paginierungs-
// Parameter).
const activitiesListMaxLimit = 500

// activityOut ist die JSON-Ausgabeform eines Activity-Eintrags. Kürzere
// Feldnamen als storage.ActivityEntry (from/to statt from_val/to_val) — die
// API ist der öffentliche Vertrag, das Go-Struct ein internes Detail.
type activityOut struct {
	ID        string `json:"id"`
	Actor     string `json:"actor"`
	ActorKind string `json:"actor_kind"`
	// Karte #608: für wen ein Agent handelte und in welcher Sitzung — nur
	// gesetzt, wenn bekannt.
	OnBehalfOf string `json:"on_behalf_of,omitempty"`
	Session    string `json:"session,omitempty"`
	Action     string `json:"action"`
	CardID     string `json:"card_id"`
	OpType     string `json:"op_type"`
	Field      string `json:"field"`
	From       string `json:"from"`
	To         string `json:"to"`
	Note       string `json:"note"`
	TS         string `json:"ts"`
}

// getActivities — GET /boards/{id}/activities[?card=<cardId>][?limit=<n>]
// (Karte #554). Liefert die Activity-Einträge eines Boards, optional auf
// eine Karte eingeschränkt, neueste zuerst. Ohne konfiguriertes
// ActivityRepo (kein WithActivities) antwortet der Endpunkt mit 503 — analog
// WithFold/postOps.
func (a *BoardsAPI) getActivities(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing board id")
		return
	}
	if a.activities == nil {
		writeError(w, http.StatusServiceUnavailable, "activities endpoint not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if _, err := a.repo.Get(ctx, id); errors.Is(err, storage.ErrBoardNotFound) {
		writeError(w, http.StatusNotFound, "board not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "get failed", err)
		return
	}

	cardID := r.URL.Query().Get("card")

	limit := activitiesListDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > activitiesListMaxLimit {
		limit = activitiesListMaxLimit
	}

	// Die ACL-Historie (acl-set/acl-deleted: E-Mail und Rolle) sieht nur,
	// wer auch die ACL-Liste lesen darf (F-261, Kritiker Sprint 71).
	ohneACL, err := a.ohneACLHistorie(ctx, r, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl check failed", err)
		return
	}
	// Karte #783 (Kritik K-A4): Einträge zu Vorschlägen nur für editor und mehr —
	// dieselben, die die Vorschläge selbst sehen.
	ohneVorschlaege, err := a.ohneVorschlagHistorie(ctx, r, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "acl check failed", err)
		return
	}
	entries, err := a.activities.ListGefiltertMit(ctx, id, cardID, limit, ohneACL, ohneVorschlaege)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "activities list failed", err)
		return
	}

	out := make([]activityOut, len(entries))
	for i, e := range entries {
		out[i] = activityOut{
			ID:         e.ID,
			Actor:      e.Actor,
			ActorKind:  e.ActorKind,
			OnBehalfOf: e.OnBehalfOf,
			Session:    e.Session,
			Action:     e.Action,
			CardID:     e.CardID,
			OpType:     e.OpType,
			Field:      e.Field,
			From:       e.FromVal,
			To:         e.ToVal,
			Note:       e.Note,
			TS:         e.TS,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ohneVorschlagHistorie: true für Agenten und für Personen unter editor
// (Karte #783). Legacy-Bypass ohne Identität sieht alles, wie bei der ACL.
func (a *BoardsAPI) ohneVorschlagHistorie(ctx context.Context, r *http.Request, boardID string) (bool, error) {
	if AgentFromContext(r.Context()) != nil {
		return true, nil
	}
	ident := IdentityFromContext(r.Context())
	if ident == nil || a.acl == nil {
		return false, nil
	}
	role, found, err := a.acl.GetRole(ctx, boardID, storage.NormalizeEmail(ident.Email))
	if err != nil {
		return false, err
	}
	return !found || roleRank[role] < roleRank[storage.RoleEditor], nil
}

// ohneACLHistorie: true, wenn der Aufrufer die ACL-Liste nicht lesen darf —
// dieselbe Regel wie GET /boards/{id}/acl (Owner-only, F-261). Agenten
// (ADR-0129 §3) sind nie Owner. Heute erreicht kein Agent diesen Endpunkt
// (Positivliste in agent.go, TestAgentDarf_Positivliste); der
// Zweig bleibt als zweite Linie, falls die Positivliste ihn einmal freigibt
// (Sprint-72-Abschluss, S72-8). Legacy-Bypass ohne Identität und ohne Agent
// (auth-loser Einzelbetrieb, ADR-0059 §2) sieht alles, wie bei der ACL-Liste.
func (a *BoardsAPI) ohneACLHistorie(ctx context.Context, r *http.Request, boardID string) (bool, error) {
	if AgentFromContext(r.Context()) != nil {
		return true, nil
	}
	ident := IdentityFromContext(r.Context())
	if ident == nil || a.acl == nil {
		return false, nil
	}
	role, found, err := a.acl.GetRole(ctx, boardID, storage.NormalizeEmail(ident.Email))
	if err != nil {
		return false, err
	}
	return !found || role != storage.RoleOwner, nil
}
