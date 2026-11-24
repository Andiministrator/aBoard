package storage

// clients_projects.go — Client- und Project-Entities (ADR-0102, Karte #307).
//
// Server-weite Entities (NICHT pro Board), Hierarchie Client → Projekt → Karte.
// Karten-Zuordnung lebt im Karten-JSON (boards.state_json bleibt opak, ADR-0044).
// Soft-Delete via active=0; Hard-Delete entfernt die Zeile und setzt
// projects.client_id auf NULL (FK ON DELETE SET NULL). Karten-project_id wird
// von dieser Schicht NICHT verändert — Karten-Cleanup ist lazy (Frontend
// ignoriert unbekannte project_id) bzw. Folgekarte für Service-Job.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Client ist ein server-weit verwalteter Auftraggeber/Kunde.
type Client struct {
	ID          string
	Name        string
	Description string
	Contact     string
	Active      bool
	CreatedAt   string
	UpdatedAt   string
}

// Project ist ein server-weites Projekt; optional einem Client zugeordnet.
type Project struct {
	ID          string
	Name        string
	Description string
	ClientID    sql.NullString // NULL = freistehendes Projekt ohne Client
	Active      bool
	CreatedAt   string
	UpdatedAt   string
}

// ErrClientNotFound / ErrProjectNotFound signalisieren fehlende Rows.
var (
	ErrClientNotFound  = errors.New("storage: client nicht gefunden")
	ErrProjectNotFound = errors.New("storage: project nicht gefunden")
)

// ListOptions steuert das Filtern bei List-Aufrufen.
type ListOptions struct {
	IncludeInactive bool // false (default) → nur active=1
}

// ── ClientRepo ────────────────────────────────────────────────────────

// ClientRepo bündelt clients-Zugriffe.
type ClientRepo struct {
	db *sql.DB
}

func NewClientRepo(db *sql.DB) *ClientRepo {
	return &ClientRepo{db: db}
}

// Create legt einen neuen Client an. id muss vom Aufrufer als UUID erzeugt
// werden; created/updated_at werden gesetzt.
func (r *ClientRepo) Create(ctx context.Context, id, name, description, contact string) (*Client, error) {
	if id == "" || name == "" {
		return nil, fmt.Errorf("clients.Create: id und name dürfen nicht leer sein")
	}
	now := nowISO()
	const q = `INSERT INTO clients (id, name, description, contact, active, created_at, updated_at)
	           VALUES (?, ?, ?, ?, 1, ?, ?)`
	if _, err := r.db.ExecContext(ctx, q, id, name, description, contact, now, now); err != nil {
		return nil, fmt.Errorf("clients.Create: %w", err)
	}
	return r.GetByID(ctx, id)
}

// GetByID holt einen Client oder gibt ErrClientNotFound zurück.
func (r *ClientRepo) GetByID(ctx context.Context, id string) (*Client, error) {
	const q = `SELECT id, name, description, contact, active, created_at, updated_at
	             FROM clients WHERE id = ?`
	c := &Client{}
	var act int
	var desc, contact sql.NullString
	err := r.db.QueryRowContext(ctx, q, id).Scan(&c.ID, &c.Name, &desc, &contact, &act, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrClientNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("clients.GetByID: %w", err)
	}
	c.Description = desc.String
	c.Contact = contact.String
	c.Active = act == 1
	return c, nil
}

// List liefert alle Clients (optional inklusive inaktive). Sortiert nach name.
func (r *ClientRepo) List(ctx context.Context, opts ListOptions) ([]*Client, error) {
	q := `SELECT id, name, description, contact, active, created_at, updated_at FROM clients`
	if !opts.IncludeInactive {
		q += ` WHERE active = 1`
	}
	q += ` ORDER BY name`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("clients.List: %w", err)
	}
	defer rows.Close()
	var out []*Client
	for rows.Next() {
		c := &Client{}
		var act int
		var desc, contact sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &desc, &contact, &act, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("clients.List scan: %w", err)
		}
		c.Description = desc.String
		c.Contact = contact.String
		c.Active = act == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

// Update ändert Felder eines bestehenden Clients. Leere Felder bedeuten
// „nicht ändern" — Aufrufer nullt explizit via leerem String, wenn gewünscht
// (Description/Contact erlauben Leerstring); name darf nicht zu leer werden.
// active wird über separates SetActive gesetzt.
func (r *ClientRepo) Update(ctx context.Context, id, name, description, contact string) (*Client, error) {
	if id == "" {
		return nil, fmt.Errorf("clients.Update: id leer")
	}
	if name == "" {
		return nil, fmt.Errorf("clients.Update: name darf nicht leer sein")
	}
	const q = `UPDATE clients SET name = ?, description = ?, contact = ?, updated_at = ?
	            WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, name, description, contact, nowISO(), id)
	if err != nil {
		return nil, fmt.Errorf("clients.Update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrClientNotFound
	}
	return r.GetByID(ctx, id)
}

// SetActive setzt das active-Flag (Soft-Delete=false, Reaktivierung=true).
func (r *ClientRepo) SetActive(ctx context.Context, id string, active bool) error {
	v := 0
	if active {
		v = 1
	}
	const q = `UPDATE clients SET active = ?, updated_at = ? WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, v, nowISO(), id)
	if err != nil {
		return fmt.Errorf("clients.SetActive: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClientNotFound
	}
	return nil
}

// HardDelete entfernt einen Client. Projekte mit dieser client_id behalten ihre
// Zeile, client_id wird auf NULL gesetzt (FK ON DELETE SET NULL).
func (r *ClientRepo) HardDelete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM clients WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("clients.HardDelete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrClientNotFound
	}
	return nil
}

// ── ProjectRepo ───────────────────────────────────────────────────────

// ProjectRepo bündelt projects-Zugriffe.
type ProjectRepo struct {
	db *sql.DB
}

func NewProjectRepo(db *sql.DB) *ProjectRepo {
	return &ProjectRepo{db: db}
}

// Create legt ein neues Projekt an. clientID leer = NULL (freistehend).
func (r *ProjectRepo) Create(ctx context.Context, id, name, description, clientID string) (*Project, error) {
	if id == "" || name == "" {
		return nil, fmt.Errorf("projects.Create: id und name dürfen nicht leer sein")
	}
	now := nowISO()
	var cid any
	if clientID == "" {
		cid = nil
	} else {
		cid = clientID
	}
	const q = `INSERT INTO projects (id, name, description, client_id, active, created_at, updated_at)
	           VALUES (?, ?, ?, ?, 1, ?, ?)`
	if _, err := r.db.ExecContext(ctx, q, id, name, description, cid, now, now); err != nil {
		return nil, fmt.Errorf("projects.Create: %w", err)
	}
	return r.GetByID(ctx, id)
}

// GetByID holt ein Projekt oder gibt ErrProjectNotFound zurück.
func (r *ProjectRepo) GetByID(ctx context.Context, id string) (*Project, error) {
	const q = `SELECT id, name, description, client_id, active, created_at, updated_at
	             FROM projects WHERE id = ?`
	p := &Project{}
	var act int
	var desc sql.NullString
	err := r.db.QueryRowContext(ctx, q, id).Scan(&p.ID, &p.Name, &desc, &p.ClientID, &act, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProjectNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("projects.GetByID: %w", err)
	}
	p.Description = desc.String
	p.Active = act == 1
	return p, nil
}

// List liefert Projekte, optional gefiltert nach client_id (leer = alle) und
// active-Filter. Sortiert nach name.
func (r *ProjectRepo) List(ctx context.Context, opts ListOptions, clientIDFilter string) ([]*Project, error) {
	var (
		conds []string
		args  []any
	)
	if !opts.IncludeInactive {
		conds = append(conds, "active = 1")
	}
	if clientIDFilter != "" {
		conds = append(conds, "client_id = ?")
		args = append(args, clientIDFilter)
	}
	q := `SELECT id, name, description, client_id, active, created_at, updated_at FROM projects`
	if len(conds) > 0 {
		q += " WHERE " + joinSQLConds(conds)
	}
	q += " ORDER BY name"
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("projects.List: %w", err)
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		p := &Project{}
		var act int
		var desc sql.NullString
		if err := rows.Scan(&p.ID, &p.Name, &desc, &p.ClientID, &act, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("projects.List scan: %w", err)
		}
		p.Description = desc.String
		p.Active = act == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// Update ändert Felder eines Projekts. clientID leer = NULL setzen.
func (r *ProjectRepo) Update(ctx context.Context, id, name, description, clientID string) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("projects.Update: id leer")
	}
	if name == "" {
		return nil, fmt.Errorf("projects.Update: name darf nicht leer sein")
	}
	var cid any
	if clientID == "" {
		cid = nil
	} else {
		cid = clientID
	}
	const q = `UPDATE projects SET name = ?, description = ?, client_id = ?, updated_at = ?
	            WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, name, description, cid, nowISO(), id)
	if err != nil {
		return nil, fmt.Errorf("projects.Update: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrProjectNotFound
	}
	return r.GetByID(ctx, id)
}

// SetActive setzt das active-Flag.
func (r *ProjectRepo) SetActive(ctx context.Context, id string, active bool) error {
	v := 0
	if active {
		v = 1
	}
	const q = `UPDATE projects SET active = ?, updated_at = ? WHERE id = ?`
	res, err := r.db.ExecContext(ctx, q, v, nowISO(), id)
	if err != nil {
		return fmt.Errorf("projects.SetActive: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// HardDelete entfernt ein Projekt.
func (r *ProjectRepo) HardDelete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("projects.HardDelete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// joinSQLConds joint Condition-Strings mit " AND ".
func joinSQLConds(conds []string) string {
	out := ""
	for i, c := range conds {
		if i > 0 {
			out += " AND "
		}
		out += c
	}
	return out
}
