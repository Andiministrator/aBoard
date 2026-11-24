package api

// clients_projects.go — REST-Endpunkte für /clients und /projects (Karte #307,
// ADR-0102).
//
// Server-weite Entities. Read-Endpoints sind viewer+ (jeder authentifizierte
// User); Mutationen (POST/PATCH/DELETE) sind admin-only. Toggle-Validierung
// (clients_enabled erfordert projects_enabled) wird in der ConfigRepo via
// PUT /admin/config/clients_enabled erzwungen — separater Doppel-Check hier
// nicht nötig, da Sichtbarkeit der Listen-Endpoints unabhängig vom Toggle ist
// (Toggle steuert UI-Sichtbarkeit, nicht API-Verfügbarkeit).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/google/uuid"
)

// ClientsProjectsAPI bündelt Abhängigkeiten für /clients und /projects.
type ClientsProjectsAPI struct {
	clients  *storage.ClientRepo
	projects *storage.ProjectRepo
	users    *storage.UserRepo
	// boards + acl werden ausschließlich von handleListCardsForProject benötigt
	// (board-übergreifende Karten-Auflistung pro Projekt, ADR-0102 §REST-API).
	// Server bleibt opak gegenüber Karten-Inhalt — die Funktion parst state_json
	// nur lesend (ADR-0044 gewahrt: kein relationaler Index auf JSON-Felder).
	boards  *storage.BoardRepo
	acl     *storage.AclRepo
	limiter *rateLimiter
}

func NewClientsProjectsAPI(clients *storage.ClientRepo, projects *storage.ProjectRepo, users *storage.UserRepo, boards *storage.BoardRepo, acl *storage.AclRepo) *ClientsProjectsAPI {
	return &ClientsProjectsAPI{
		clients:  clients,
		projects: projects,
		users:    users,
		boards:   boards,
		acl:      acl,
		limiter:  newRateLimiter(30, time.Hour), // 30 Mutationen/h pro Admin (analog AdminAPI)
	}
}

// Register hängt alle Routen an den Mux.
func (a *ClientsProjectsAPI) Register(mux *http.ServeMux) {
	mux.Handle("GET /clients", a.auth(a.handleListClients, false))
	mux.Handle("POST /clients", a.auth(a.handleCreateClient, true))
	mux.Handle("PATCH /clients/{id}", a.auth(a.handleUpdateClient, true))
	mux.Handle("DELETE /clients/{id}", a.auth(a.handleDeleteClient, true))
	mux.Handle("GET /clients/{id}/projects", a.auth(a.handleListProjectsForClient, false))

	mux.Handle("GET /projects", a.auth(a.handleListProjects, false))
	mux.Handle("POST /projects", a.auth(a.handleCreateProject, true))
	mux.Handle("PATCH /projects/{id}", a.auth(a.handleUpdateProject, true))
	mux.Handle("DELETE /projects/{id}", a.auth(a.handleDeleteProject, true))
	mux.Handle("GET /projects/{id}/cards", a.auth(a.handleListCardsForProject, false))
}

// auth umhüllt einen Handler. requireAdmin=true → admin-only + Rate-Limit;
// requireAdmin=false → jeder authentifizierte User darf lesen.
func (a *ClientsProjectsAPI) auth(h http.HandlerFunc, requireAdmin bool) http.Handler {
	return WithDefaults(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFromContext(r.Context())
		if id == nil {
			writeError(w, http.StatusForbidden, "forbidden: authentication required")
			return
		}
		email := storage.NormalizeEmail(id.Email)
		if email == "" {
			writeError(w, http.StatusForbidden, "forbidden: no identity email")
			return
		}
		u, err := a.users.GetByEmail(r.Context(), email)
		if errors.Is(err, storage.ErrUserNotFound) {
			writeError(w, http.StatusForbidden, "forbidden: user not provisioned")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "user lookup failed", err)
			return
		}
		if requireAdmin {
			if !u.IsAdmin {
				writeError(w, http.StatusForbidden, "forbidden: admin role required")
				return
			}
			if !a.limiter.allow(u.ID) {
				writeError(w, http.StatusTooManyRequests, "rate limit: 30 mutations per hour per admin")
				return
			}
		}
		h(w, r)
	}))
}

// ── DTOs ──────────────────────────────────────────────────────────────

type clientDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Contact     string `json:"contact,omitempty"`
	Active      bool   `json:"active"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type projectDTO struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	ClientID    *string `json:"client_id"` // nullable
	Active      bool    `json:"active"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func toClientDTO(c *storage.Client) clientDTO {
	return clientDTO{
		ID:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		Contact:     c.Contact,
		Active:      c.Active,
		CreatedAt:   c.CreatedAt,
		UpdatedAt:   c.UpdatedAt,
	}
}

func toProjectDTO(p *storage.Project) projectDTO {
	dto := projectDTO{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Active:      p.Active,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
	if p.ClientID.Valid {
		v := p.ClientID.String
		dto.ClientID = &v
	}
	return dto
}

// includeInactive liest ?include=inactive aus der Query.
func includeInactive(r *http.Request) bool {
	return r.URL.Query().Get("include") == "inactive"
}

// ── Clients ───────────────────────────────────────────────────────────

func (a *ClientsProjectsAPI) handleListClients(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	list, err := a.clients.List(ctx, storage.ListOptions{IncludeInactive: includeInactive(r)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clients list failed", err)
		return
	}
	out := make([]clientDTO, 0, len(list))
	for _, c := range list {
		out = append(out, toClientDTO(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *ClientsProjectsAPI) handleCreateClient(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Contact     string `json:"contact"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	c, err := a.clients.Create(ctx, uuid.NewString(), body.Name, body.Description, body.Contact)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "client name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "client create failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, toClientDTO(c))
}

func (a *ClientsProjectsAPI) handleUpdateClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Contact     string `json:"contact"`
		Active      *bool  `json:"active"` // optional: nur wenn gesetzt → SetActive
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.Active != nil {
		if err := a.clients.SetActive(ctx, id, *body.Active); err != nil {
			if errors.Is(err, storage.ErrClientNotFound) {
				writeError(w, http.StatusNotFound, "client not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "set active failed", err)
			return
		}
	}
	if body.Name != "" {
		c, err := a.clients.Update(ctx, id, body.Name, body.Description, body.Contact)
		if err != nil {
			if errors.Is(err, storage.ErrClientNotFound) {
				writeError(w, http.StatusNotFound, "client not found")
				return
			}
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "client name already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "client update failed", err)
			return
		}
		writeJSON(w, http.StatusOK, toClientDTO(c))
		return
	}
	// Reiner Active-Toggle ohne Name-Update — Get + Return.
	c, err := a.clients.GetByID(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "client not found")
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(c))
}

func (a *ClientsProjectsAPI) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	hard := r.URL.Query().Get("hard") == "true"
	if hard {
		if err := a.clients.HardDelete(ctx, id); err != nil {
			if errors.Is(err, storage.ErrClientNotFound) {
				writeError(w, http.StatusNotFound, "client not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "client delete failed", err)
			return
		}
	} else {
		if err := a.clients.SetActive(ctx, id, false); err != nil {
			if errors.Is(err, storage.ErrClientNotFound) {
				writeError(w, http.StatusNotFound, "client not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "client deactivate failed", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *ClientsProjectsAPI) handleListProjectsForClient(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("id")
	if clientID == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	list, err := a.projects.List(ctx, storage.ListOptions{IncludeInactive: includeInactive(r)}, clientID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "projects list failed", err)
		return
	}
	out := make([]projectDTO, 0, len(list))
	for _, p := range list {
		out = append(out, toProjectDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Projects ──────────────────────────────────────────────────────────

func (a *ClientsProjectsAPI) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	clientFilter := r.URL.Query().Get("client_id")
	list, err := a.projects.List(ctx, storage.ListOptions{IncludeInactive: includeInactive(r)}, clientFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "projects list failed", err)
		return
	}
	out := make([]projectDTO, 0, len(list))
	for _, p := range list {
		out = append(out, toProjectDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *ClientsProjectsAPI) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		ClientID    string `json:"client_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	p, err := a.projects.Create(ctx, uuid.NewString(), body.Name, body.Description, body.ClientID)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "project name already exists for this client")
			return
		}
		writeError(w, http.StatusInternalServerError, "project create failed", err)
		return
	}
	writeJSON(w, http.StatusCreated, toProjectDTO(p))
}

func (a *ClientsProjectsAPI) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		ClientID    string `json:"client_id"` // leer = NULL
		Active      *bool  `json:"active"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if body.Active != nil {
		if err := a.projects.SetActive(ctx, id, *body.Active); err != nil {
			if errors.Is(err, storage.ErrProjectNotFound) {
				writeError(w, http.StatusNotFound, "project not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "set active failed", err)
			return
		}
	}
	if body.Name != "" {
		p, err := a.projects.Update(ctx, id, body.Name, body.Description, body.ClientID)
		if err != nil {
			if errors.Is(err, storage.ErrProjectNotFound) {
				writeError(w, http.StatusNotFound, "project not found")
				return
			}
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "project name already exists for this client")
				return
			}
			writeError(w, http.StatusInternalServerError, "project update failed", err)
			return
		}
		writeJSON(w, http.StatusOK, toProjectDTO(p))
		return
	}
	p, err := a.projects.GetByID(ctx, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, toProjectDTO(p))
}

// cardOnProjectEntry ist die ausgegebene Repräsentation einer Karte, die einem
// Projekt zugeordnet ist — board-übergreifend mit Board- und Sprint-Kontext.
type cardOnProjectEntry struct {
	BoardID     string `json:"board_id"`
	BoardTitle  string `json:"board_title"`
	SprintID    string `json:"sprint_id,omitempty"`
	SprintTitle string `json:"sprint_title,omitempty"`
	CardID      string `json:"card_id"`
	DisplayID   int    `json:"display_id"`
	Title       string `json:"title"`
	ColumnID    string `json:"column_id"`
	Owner       string `json:"owner,omitempty"`
	Archived    bool   `json:"archived,omitempty"`
	QA          bool   `json:"qa,omitempty"`
	Critical    bool   `json:"critical,omitempty"`
	Blocked     bool   `json:"blocked,omitempty"`
}

// Minimal-Schema fürs Parsen von boards.state_json. Server bleibt opak — wir
// lesen nur die Felder, die ADR-0102 §REST-API für /projects/:id/cards braucht.
// Unbekannte Felder werden von json.Unmarshal stillschweigend ignoriert.
type boardCardLite struct {
	ID        string  `json:"id"`
	DisplayID int     `json:"displayId"`
	Title     string  `json:"title"`
	ColumnID  string  `json:"columnId"`
	ProjectID *string `json:"project_id"`
	Owner     string  `json:"owner"`
	Archived  bool    `json:"archived"`
	QA        bool    `json:"qa"`
	Critical  bool    `json:"critical"`
	Blocked   bool    `json:"blocked"`
	// Zusätzlich von /me/cards (Kalender, #308 Etappe 5) genutzt — additive
	// Felder; /projects/:id/cards ignoriert sie. ADR-0103.
	DueDate     *string  `json:"dueDate"`
	StartDate   *string  `json:"startDate"`
	DueTime     *string  `json:"dueTime"`   // ADR-0138 §1
	StartTime   *string  `json:"startTime"` // ADR-0138 §1
	Assignees   []string `json:"assignees"`
	Kind        string   `json:"kind"`
	Description string   `json:"description"`
	// Karte #748: Frist zuletzt von einem Agenten geändert (Fold-Marke,
	// server/fold markeNachher). Nur der Feed „für außen“ liest sie.
	FristVonAgent bool `json:"fristVonAgent"`
}

type boardSprintLite struct {
	ID    string          `json:"id"`
	Title string          `json:"title"`
	Cards []boardCardLite `json:"cards"`
}

type boardStateLite struct {
	Board struct {
		ID      string            `json:"id"`
		Title   string            `json:"title"`
		Sprints []boardSprintLite `json:"sprints"`
	} `json:"board"`
}

// handleListCardsForProject — GET /projects/{id}/cards (viewer+). Iteriert alle
// Boards, die der User per ACL sehen darf, parst boards.state_json read-only
// und sammelt Karten mit project_id == :id. Im Legacy-Bypass (kein Forward-Auth)
// werden alle Boards einbezogen — gleiches Muster wie boards.list.
func (a *ClientsProjectsAPI) handleListCardsForProject(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if _, err := a.projects.GetByID(ctx, projectID); err != nil {
		if errors.Is(err, storage.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "project lookup failed", err)
		return
	}

	// ACL-Filter analog boards.list: bei vorhandener Forward-Auth-Identität
	// nur Boards, auf deren ACL die Email steht. Ohne Identität = Legacy-
	// Verhalten (alle Boards) — der Aufrufer ist via auth-Wrapper bereits
	// als viewer+ authentifiziert.
	var permitted map[string]struct{}
	if id := IdentityFromContext(r.Context()); id != nil && a.acl != nil {
		email := storage.NormalizeEmail(id.Email)
		if email == "" {
			writeJSON(w, http.StatusOK, []cardOnProjectEntry{})
			return
		}
		ids, aerr := a.acl.ListBoardsForUser(ctx, email)
		if aerr != nil {
			writeError(w, http.StatusInternalServerError, "acl lookup failed", aerr)
			return
		}
		permitted = make(map[string]struct{}, len(ids))
		for _, bid := range ids {
			permitted[bid] = struct{}{}
		}
	}

	summaries, err := a.boards.List(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "boards list failed", err)
		return
	}
	out := make([]cardOnProjectEntry, 0)
	for _, s := range summaries {
		if permitted != nil {
			if _, ok := permitted[s.ID]; !ok {
				continue
			}
		}
		b, err := a.boards.Get(ctx, s.ID)
		if err != nil {
			continue
		}
		var state boardStateLite
		if err := json.Unmarshal(b.StateJSON, &state); err != nil {
			continue
		}
		for _, sp := range state.Board.Sprints {
			for _, c := range sp.Cards {
				if c.ProjectID == nil || *c.ProjectID != projectID {
					continue
				}
				out = append(out, cardOnProjectEntry{
					BoardID:     b.ID,
					BoardTitle:  b.Title,
					SprintID:    sp.ID,
					SprintTitle: sp.Title,
					CardID:      c.ID,
					DisplayID:   c.DisplayID,
					Title:       c.Title,
					ColumnID:    c.ColumnID,
					Owner:       c.Owner,
					Archived:    c.Archived,
					QA:          c.QA,
					Critical:    c.Critical,
					Blocked:     c.Blocked,
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *ClientsProjectsAPI) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	hard := r.URL.Query().Get("hard") == "true"
	if hard {
		if err := a.projects.HardDelete(ctx, id); err != nil {
			if errors.Is(err, storage.ErrProjectNotFound) {
				writeError(w, http.StatusNotFound, "project not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "project delete failed", err)
			return
		}
	} else {
		if err := a.projects.SetActive(ctx, id, false); err != nil {
			if errors.Is(err, storage.ErrProjectNotFound) {
				writeError(w, http.StatusNotFound, "project not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "project deactivate failed", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
