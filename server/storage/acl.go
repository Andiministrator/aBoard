package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Rollen laut ADR-0059 (in Karte #79 festgelegt). Die SQLite-CHECK-Constraint
// auf acl.role spiegelt diese Menge — die Go-Validierung ist Defense-in-depth
// und liefert einen sprechenden Fehler vor dem DB-Roundtrip.
const (
	RoleOwner  = "owner"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// ErrInvalidRole signalisiert eine Rolle außerhalb {owner,editor,viewer}.
var ErrInvalidRole = errors.New("storage: ungültige ACL-Rolle")

// ValidRole prüft, ob role eine bekannte Rolle ist.
func ValidRole(role string) bool {
	return role == RoleOwner || role == RoleEditor || role == RoleViewer
}

// ACLEntry ist ein Eintrag der acl-Tabelle. user_email ist Email-basiert —
// ein Eintrag kann für eine noch nie eingeloggte Email existieren
// (Invite-only, ADR-0059 §1).
type ACLEntry struct {
	BoardID   string
	UserEmail string
	Role      string
	Created   string // ISO-8601
}

// AclRepo bündelt die Zugriffe auf die acl-Tabelle.
type AclRepo struct {
	db *sql.DB
}

func NewAclRepo(db *sql.DB) *AclRepo {
	return &AclRepo{db: db}
}

// ListByBoard liefert alle ACL-Einträge eines Boards, sortiert nach Email.
// Für den Share-Dialog (#199).
func (r *AclRepo) ListByBoard(ctx context.Context, boardID string) ([]ACLEntry, error) {
	const q = `SELECT board_id, user_email, role, created
	            FROM acl WHERE board_id = ? ORDER BY user_email`
	rows, err := r.db.QueryContext(ctx, q, boardID)
	if err != nil {
		return nil, fmt.Errorf("acl.ListByBoard: %w", err)
	}
	defer rows.Close()
	var out []ACLEntry
	for rows.Next() {
		var e ACLEntry
		if err := rows.Scan(&e.BoardID, &e.UserEmail, &e.Role, &e.Created); err != nil {
			return nil, fmt.Errorf("acl.ListByBoard scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acl.ListByBoard rows: %w", err)
	}
	return out, nil
}

// GetRole liefert die Rolle einer Email auf einem Board. found=false, wenn
// kein ACL-Eintrag existiert (→ Aufrufer #198 entscheidet 403 vs. Bypass).
//
// F-260: email wird zentral normalisiert (NormalizeEmail) — der Lookup muss
// dieselbe Form treffen, in der Set() schreibt, sonst Casing-Mismatch
// Authelia↔ACL (Owner-Lockout).
//
// ADR-0132: Das ist die WIRKSAME Rolle. Hat die Email keinen eigenen Eintrag,
// zählt der Eintrag ihrer Domain ("@<domain>"), solange die Domain in
// ABOARD_ACL_DOMAINS steht. Der eigene Eintrag geht immer vor — auch wenn er
// schwächer ist. Für einen Domain-Schlüssel selbst ("@x.de") wird exakt
// gelesen.
func (r *AclRepo) GetRole(ctx context.Context, boardID, email string) (role string, found bool, err error) {
	email = NormalizeEmail(email)
	domain := domainSubjektFuer(email)
	const q = `SELECT user_email, role FROM acl WHERE board_id = ? AND user_email IN (?, ?)`
	rows, err := r.db.QueryContext(ctx, q, boardID, email, domain)
	if err != nil {
		return "", false, fmt.Errorf("acl.GetRole: %w", err)
	}
	defer rows.Close()
	var domainRolle string
	for rows.Next() {
		var subjekt, rolle string
		if err := rows.Scan(&subjekt, &rolle); err != nil {
			return "", false, fmt.Errorf("acl.GetRole scan: %w", err)
		}
		if subjekt == email {
			return rolle, true, nil
		}
		domainRolle = rolle
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("acl.GetRole rows: %w", err)
	}
	if domainRolle != "" {
		return domainRolle, true, nil
	}
	return "", false, nil
}

// boardsMitWirksamerRolle liest eigene und (falls erlaubt) Domain-Einträge
// einer Email und verschmilzt sie je Board — eigener Eintrag vor Domain
// (ADR-0132 §5). Sortiert nach board_id wie bisher.
func (r *AclRepo) boardsMitWirksamerRolle(ctx context.Context, email, wo string) ([]BoardRoleEntry, error) {
	email = NormalizeEmail(email) // F-260
	domain := domainSubjektFuer(email)
	const q = `SELECT board_id, user_email, role FROM acl WHERE user_email IN (?, ?) ORDER BY board_id`
	rows, err := r.db.QueryContext(ctx, q, email, domain)
	if err != nil {
		return nil, fmt.Errorf("acl.%s: %w", wo, err)
	}
	defer rows.Close()
	var out []BoardRoleEntry
	index := map[string]int{}
	for rows.Next() {
		var boardID, subjekt, rolle string
		if err := rows.Scan(&boardID, &subjekt, &rolle); err != nil {
			return nil, fmt.Errorf("acl.%s scan: %w", wo, err)
		}
		if i, ok := index[boardID]; ok {
			if subjekt == email {
				out[i].Role = rolle
			}
			continue
		}
		index[boardID] = len(out)
		out = append(out, BoardRoleEntry{BoardID: boardID, Role: rolle})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acl.%s rows: %w", wo, err)
	}
	return out, nil
}

// ListBoardsForUser liefert die Board-IDs, auf denen die Email eine wirksame
// Rolle hat (eigener Eintrag oder erlaubte Domain, ADR-0132).
// Basis für die ACL-gefilterte Board-Liste (#198 GET /boards, #201 UI).
func (r *AclRepo) ListBoardsForUser(ctx context.Context, email string) ([]string, error) {
	eintraege, err := r.boardsMitWirksamerRolle(ctx, email, "ListBoardsForUser")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range eintraege {
		out = append(out, e.BoardID)
	}
	return out, nil
}

// BoardRoleEntry ist ein Board-ID + Rolle-Paar für die Karte-#201-UI.
type BoardRoleEntry struct {
	BoardID string
	Role    string
}

// ListBoardsWithRoleForUser liefert Board-ID + wirksame Rolle für alle
// Boards, auf denen die Email eine Rolle hat (eigener Eintrag oder erlaubte
// Domain, ADR-0132). Für die getrennte Darstellung in der Sidebar (owner vs.
// editor/viewer, ADR-0059, Karte #201).
func (r *AclRepo) ListBoardsWithRoleForUser(ctx context.Context, email string) ([]BoardRoleEntry, error) {
	return r.boardsMitWirksamerRolle(ctx, email, "ListBoardsWithRoleForUser")
}

// Set legt einen ACL-Eintrag an oder ändert die Rolle (Upsert). Nur der
// owner eines Boards darf das aufrufen — der Guard sitzt im Handler (#199),
// nicht hier (Repo ist autorisierungs-agnostisch, analog BoardRepo).
func (r *AclRepo) Set(ctx context.Context, boardID, email, role string) error {
	// F-260: zentrale Normalisierung VOR der Leer-Prüfung — eine reine
	// Whitespace-Email ("   ") gilt damit korrekt als leer, und der
	// gespeicherte Wert ist exakt die Form, die GetRole/ListBoardsForUser
	// beim Lookup wieder erzeugen (kein Casing-Mismatch).
	email = NormalizeEmail(email)
	if boardID == "" || email == "" {
		return fmt.Errorf("acl.Set: board_id/user_email dürfen nicht leer sein")
	}
	if !ValidRole(role) {
		return fmt.Errorf("acl.Set: %w: %q", ErrInvalidRole, role)
	}
	// ADR-0132 §3/§4: Domain-Eintrag nur für erlaubte Domains, nie owner.
	if err := PruefeACLSubjektFuerBoard(boardID, email, role); err != nil {
		return fmt.Errorf("acl.Set: %w", err)
	}
	const q = `
		INSERT INTO acl (board_id, user_email, role, created)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(board_id, user_email) DO UPDATE SET role = excluded.role`
	if _, err := r.db.ExecContext(ctx, q, boardID, email, role, nowISO()); err != nil {
		return fmt.Errorf("acl.Set: %w", err)
	}
	return nil
}

// KnownSubject ist ein Eintrag im bekannten-Nutzer-Verzeichnis (ADR-0066 §3).
// Status "aktiv" = users-Row vorhanden (hat sich mind. einmal eingeloggt);
// "ausstehend" = Email steht nur in acl (vorab-geteilt, nie eingeloggt).
type KnownSubject struct {
	Email  string
	Name   string // leer wenn nur in acl, nie in users
	Status string // "aktiv" | "ausstehend"
}

// ListKnownSubjects liefert die bekannte Nutzermenge = users ∪ DISTINCT acl.user_email,
// sortiert nach Email. Emails liegen bereits normalisiert vor (F-260) —
// der UNION-Join über die rohe Spalte ist korrekt.
//
// SQL-Strategie: ein LEFT-JOIN von acl-Distinct auf users liefert in einem
// Durchgang beide Fälle:
//   - acl-Email hat users-Row → aktiv (name aus users)
//   - acl-Email hat KEINE users-Row → ausstehend (name leer)
//
// Plus alle users, die KEINE acl-Zeile haben (können durch manuelles DB-
// Löschen von acl-Einträgen entstehen oder über direkte users-Insert):
//
//	UNION
//	SELECT email, name, 'aktiv' FROM users
//	WHERE email NOT IN (SELECT DISTINCT user_email FROM acl)
//
// Repo ist autorisierungs-agnostisch (analog BoardRepo/AclRepo).
//
// ADR-0132 §6: Domain-Einträge ("@<domain>") sind keine Personen und fehlen
// hier. substr statt LIKE, weil LIKE "_" und "%" als Muster läse.
func (r *AclRepo) ListKnownSubjects(ctx context.Context) ([]KnownSubject, error) {
	const q = `
		SELECT u_email, u_name, status FROM (
			SELECT
				a.user_email                   AS u_email,
				COALESCE(u.name, '')           AS u_name,
				CASE WHEN u.email IS NOT NULL THEN 'aktiv' ELSE 'ausstehend' END AS status
			FROM (SELECT DISTINCT user_email FROM acl WHERE substr(user_email, 1, 1) <> '@') a
			LEFT JOIN users u ON u.email = a.user_email
			UNION
			SELECT u2.email, u2.name, 'aktiv'
			FROM users u2
			WHERE u2.email NOT IN (SELECT DISTINCT user_email FROM acl)
		)
		ORDER BY u_email`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("acl.ListKnownSubjects: %w", err)
	}
	defer rows.Close()
	var out []KnownSubject
	for rows.Next() {
		var s KnownSubject
		if err := rows.Scan(&s.Email, &s.Name, &s.Status); err != nil {
			return nil, fmt.Errorf("acl.ListKnownSubjects scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acl.ListKnownSubjects rows: %w", err)
	}
	return out, nil
}

// Delete entfernt einen ACL-Eintrag. Idempotent: fehlender Eintrag ist kein
// Fehler (kein ErrNotFound — der Zielzustand "kein Zugriff" ist erreicht).
func (r *AclRepo) Delete(ctx context.Context, boardID, email string) error {
	email = NormalizeEmail(email) // F-260: muss dieselbe Form wie Set() treffen
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM acl WHERE board_id = ? AND user_email = ?`, boardID, email); err != nil {
		return fmt.Errorf("acl.Delete: %w", err)
	}
	return nil
}
