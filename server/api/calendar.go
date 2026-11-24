package api

// calendar.go — Cross-Board-Kalender-Endpoint (Karte #308 Etappe 5, ADR-0103).
//
//	GET /me/cards?dueDateRange=<from>~<to>
//
// Liefert alle terminierten Karten (mit dueDate) über ALLE Boards, auf die der
// anfragende User Lese-Zugriff hat — die Datenquelle des Cross-Board-Modus der
// Kalender-Ansicht. Read-only: parst boards.state_json opak (gleiches Muster wie
// /projects/:id/cards, ADR-0102) und respektiert die Board-ACL.
//
// Die Route liegt außerhalb von /boards/* — der ACL-Guard greift hier nicht;
// die ACL-Filterung übernimmt der Handler selbst (ListBoardsForUser). Im
// Legacy-Bypass (kein Forward-Auth) werden alle Boards einbezogen (Single-User
// sieht alles), analog boards.list.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Andiministrator/aBoard/server/calendar"
	"github.com/Andiministrator/aBoard/server/storage"
)

// CalendarAPI bündelt die für die Kalender-Endpoints nötigen Repos.
type CalendarAPI struct {
	boards *storage.BoardRepo
	acl    *storage.AclRepo
	users  *storage.UserRepo
}

func NewCalendarAPI(boards *storage.BoardRepo, acl *storage.AclRepo, users *storage.UserRepo) *CalendarAPI {
	return &CalendarAPI{boards: boards, acl: acl, users: users}
}

func (a *CalendarAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /me/cards", WithDefaults(http.HandlerFunc(a.meCards)))
	// Karte #743 (ADR-0138 §4): Termine ohne Karte über alle Boards (termine.go).
	mux.Handle("GET /me/termine", WithDefaults(http.HandlerFunc(a.meTermine)))
}

// calendarCard ist das Wire-Format einer terminierten Karte für die
// Kalender-Ansicht. Bewusst schlank — keine Kommentare/Activity/agentContext.
type calendarCard struct {
	BoardID    string `json:"board_id"`
	BoardTitle string `json:"board_title"`
	CardID     string `json:"card_id"`
	DisplayID  int    `json:"display_id"`
	Title      string `json:"title"`
	ColumnID   string `json:"column_id"`
	DueDate    string `json:"dueDate"`
	StartDate  string `json:"startDate,omitempty"`
	// ADR-0138 §1: Uhrzeiten "HH:MM", nur wenn gültig und das Datum gesetzt ist.
	DueTime   string   `json:"dueTime,omitempty"`
	StartTime string   `json:"startTime,omitempty"`
	Owner     string   `json:"owner,omitempty"`
	Assignees []string `json:"assignees,omitempty"`
	ProjectID string   `json:"project_id,omitempty"`
	QA        bool     `json:"qa,omitempty"`
	Critical  bool     `json:"critical,omitempty"`
	Blocked   bool     `json:"blocked,omitempty"`
	// Server-intern für den iCal-Export (Etappe 6) — bewusst NICHT in der
	// /me/cards-Antwort (json:"-"), um die Cross-Board-Payload schlank zu halten.
	Description string `json:"-"`
	// Karte #748: Agenten-Marke für den Feed „für außen“ — nicht in /me/cards.
	FristVonAgent bool `json:"-"`
}

func (a *CalendarAPI) meCards(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	from, to, ok := parseDueDateRange(r.URL.Query().Get("dueDateRange"))
	if !ok {
		writeError(w, http.StatusBadRequest, "dueDateRange must be <from>~<to> (YYYY-MM-DD)")
		return
	}

	// ACL-Filter analog /projects/:id/cards: bei Forward-Auth-Identität nur
	// Boards mit ACL-Eintrag, sonst (Legacy) alle Boards.
	var permitted map[string]struct{}
	if id := IdentityFromContext(ctx); id != nil && a.acl != nil {
		email := storage.NormalizeEmail(id.Email)
		if email == "" {
			writeError(w, http.StatusForbidden, "forbidden: no identity email")
			return
		}
		ids, err := a.acl.ListBoardsForUser(ctx, email)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "acl lookup failed", err)
			return
		}
		permitted = make(map[string]struct{}, len(ids))
		for _, bid := range ids {
			permitted[bid] = struct{}{}
		}
	}

	out, err := collectCalendarCards(ctx, a.boards, permitted, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "boards list failed", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// collectCalendarCards iteriert die erlaubten Boards, parst state_json read-only
// und sammelt alle terminierten Nicht-Meta-/Nicht-Archiv-Karten, deren Termin
// in [from,to] fällt. permitted == nil → alle Boards (Legacy-Bypass). from/to
// == "" → keine Range-Schranke (alle Karten mit dueDate). Wird von /me/cards
// und vom iCal-Export (Etappe 6) geteilt.
func collectCalendarCards(ctx context.Context, boards *storage.BoardRepo, permitted map[string]struct{}, from, to string) ([]calendarCard, error) {
	summaries, err := boards.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]calendarCard, 0)
	for _, s := range summaries {
		if permitted != nil {
			if _, okBoard := permitted[s.ID]; !okBoard {
				continue
			}
		}
		b, err := boards.Get(ctx, s.ID)
		if err != nil {
			continue
		}
		var state boardStateLite
		if err := json.Unmarshal(b.StateJSON, &state); err != nil {
			continue
		}
		for _, sp := range state.Board.Sprints {
			for _, c := range sp.Cards {
				if c.Kind == "sprint-meta" || c.Archived {
					continue
				}
				due := derefStr(c.DueDate)
				if due == "" {
					continue // ohne dueDate nicht im Kalender (ADR-0103)
				}
				start := derefStr(c.StartDate)
				if !cardInRange(start, due, from, to) {
					continue
				}
				dueTime := derefStr(c.DueTime)
				if !calendar.IstUhrzeit(dueTime) {
					dueTime = ""
				}
				// startTime gehört zu startDate: ohne startDate beginnt das
				// Event am dueDate, eine verwaiste startTime griffe dort.
				startTime := derefStr(c.StartTime)
				if start == "" || !calendar.IstUhrzeit(startTime) {
					startTime = ""
				}
				proj := ""
				if c.ProjectID != nil {
					proj = *c.ProjectID
				}
				out = append(out, calendarCard{
					BoardID:       b.ID,
					BoardTitle:    b.Title,
					CardID:        c.ID,
					DisplayID:     c.DisplayID,
					Title:         c.Title,
					ColumnID:      c.ColumnID,
					DueDate:       due,
					StartDate:     start,
					DueTime:       dueTime,
					StartTime:     startTime,
					Owner:         c.Owner,
					Assignees:     c.Assignees,
					ProjectID:     proj,
					QA:            c.QA,
					Critical:      c.Critical,
					Blocked:       c.Blocked,
					Description:   c.Description,
					FristVonAgent: c.FristVonAgent,
				})
			}
		}
	}
	return out, nil
}

// parseDueDateRange parst "<from>~<to>" (beide YYYY-MM-DD). Leerer Input ist
// gültig (keine Schranke → "", ""). Bei gesetztem Input müssen beide Enden
// valide ISO-Datumsstrings sein, sonst ok == false.
func parseDueDateRange(raw string) (from, to string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", true
	}
	parts := strings.SplitN(raw, "~", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	from = strings.TrimSpace(parts[0])
	to = strings.TrimSpace(parts[1])
	if !isISODate(from) || !isISODate(to) {
		return "", "", false
	}
	return from, to, true
}

// cardInRange entscheidet, ob eine Karte mit Termin [start..due] (start kann
// leer sein → Punkt-Termin am due) den Filter [from..to] überlappt. Leere
// Filter-Enden = keine Schranke. Vergleich rein lexikografisch (ISO-Datum
// YYYY-MM-DD ist lexikografisch == chronologisch sortierbar).
func cardInRange(start, due, from, to string) bool {
	lo := start
	if lo == "" {
		lo = due
	}
	hi := due
	if hi < lo { // defensiv: vertauschte Range
		lo, hi = hi, lo
	}
	if from != "" && hi < from {
		return false
	}
	if to != "" && lo > to {
		return false
	}
	return true
}

// isISODate prüft das Format YYYY-MM-DD (oberflächlich, ohne Kalender-Validität —
// genügt als Filter-Guard; die Frontend-Date-Helpers normalisieren feiner).
func isISODate(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, ch := range s {
		if i == 4 || i == 7 {
			continue
		}
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}
