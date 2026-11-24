package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// User ist die persistente Identität eines Nutzers. Bei ECHTEM Erstkontakt
// (authentifizierter Request einer noch unbekannten Email) provisioniert
// `BootstrapNewUser` atomar users-Row + persönliches Board + owner-ACL
// (Open-to-authenticated, ADR-0066 — ersetzt das ältere Invite-only-Modell
// aus ADR-0059 §1). Im No-Auth-Modus bleibt die Tabelle leer.
type User struct {
	ID      string
	Email   string
	Name    string
	Created string // ISO-8601
	IsAdmin bool   // #306/ADR-0101: Admin-ACL für /admin/*-Endpoints
}

// ErrUserNotFound signalisiert, dass zu einer Email kein User-Row existiert.
var ErrUserNotFound = errors.New("storage: user nicht gefunden")

// UserRepo bündelt die Zugriffe auf die users-Tabelle.
type UserRepo struct {
	db *sql.DB
}

func NewUserRepo(db *sql.DB) *UserRepo {
	return &UserRepo{db: db}
}

// timeNow ist die paketinterne Uhr, auf der nowISO UND jede Cutoff-Berechnung
// (z. B. EvictOlderThan) aufsetzen — eine Variable statt zwei, damit Tests nur
// EINE Stelle fixieren müssen und "jetzt" für Insert und Evict konsistent
// bleibt (F-686c: EvictOlderThan nutzte bislang `time.Now()` direkt und war
// dadurch nicht testbar fixierbar, obwohl der Digest-Scheduler mit `deps.Now`
// bereits eine injizierbare Uhr kennt).
var timeNow = time.Now

// nowISO liefert den aktuellen Zeitstempel als ISO-8601 (UTC) mit
// Nanosekunden-Genauigkeit. Als Variable, damit Tests ihn fixieren können.
// Nanosekunden-Genauigkeit (RFC3339Nano) ist notwendig für Activity-Log-
// Sortierung: mehrere Einträge pro Sekunde brauchen sub-sekunden-genaue TS
// (z. B. board-created + board-updated im selben Test-Run in < 1 s).
var nowISO = func() string {
	return timeNow().UTC().Format(time.RFC3339Nano)
}

// GetByEmail holt den User zu einer Email oder gibt ErrUserNotFound zurück.
func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*User, error) {
	email = NormalizeEmail(email) // F-260: konsistent zu Upsert/ACL
	const q = `SELECT id, email, name, created, is_admin FROM users WHERE email = ?`
	u := &User{}
	var adm int
	err := r.db.QueryRowContext(ctx, q, email).Scan(&u.ID, &u.Email, &u.Name, &u.Created, &adm)
	if err == nil {
		u.IsAdmin = adm == 1
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("users.GetByEmail: %w", err)
	}
	return u, nil
}

// GetByID holt den User zu einer UUID oder gibt ErrUserNotFound zurück.
// Wird vom Notification-Worker und vom Test-Send-Endpoint genutzt — die
// Empfänger-Auflösung läuft über User-IDs, nicht Emails (Anti-Loop und
// Subscriptions sind UserID-keyed).
func (r *UserRepo) GetByID(ctx context.Context, userID string) (*User, error) {
	const q = `SELECT id, email, name, created, is_admin FROM users WHERE id = ?`
	u := &User{}
	var adm int
	err := r.db.QueryRowContext(ctx, q, userID).Scan(&u.ID, &u.Email, &u.Name, &u.Created, &adm)
	if err == nil {
		u.IsAdmin = adm == 1
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("users.GetByID: %w", err)
	}
	return u, nil
}

// Upsert materialisiert einen User-Row. Die ID ist stabil: existiert die
// Email bereits, bleibt deren id erhalten und nur der Name wird aktualisiert
// (z. B. wenn der Reverse-Proxy einen geänderten Anzeigenamen liefert).
// Neue Rows bekommen die übergebene id und einen created-Zeitstempel.
//
// Aufrufer (#198): bei erstem authentifiziertem Request einer Email, die
// auf einer ACL steht. id wird vom Aufrufer als UUID erzeugt.
func (r *UserRepo) Upsert(ctx context.Context, id, email, name string) (*User, error) {
	email = NormalizeEmail(email) // F-260: Materialisierung in kanonischer Form
	if id == "" || email == "" {
		return nil, fmt.Errorf("users.Upsert: id/email dürfen nicht leer sein")
	}
	const q = `
		INSERT INTO users (id, email, name, created)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET name = excluded.name`
	if _, err := r.db.ExecContext(ctx, q, id, email, name, nowISO()); err != nil {
		return nil, fmt.Errorf("users.Upsert: %w", err)
	}
	// Zurücklesen — liefert bei Konflikt die bestehende (stabile) id.
	return r.GetByEmail(ctx, email)
}

// SetName setzt den Anzeigenamen eines Nutzers (per ID). Karte #577: der
// ACLGuard zieht damit users.name nach, wenn der Proxy einen anderen
// Anzeigenamen liefert als gespeichert. Die Bedingung `name <> ?` hält den
// Aufruf idempotent und schreibt nichts, wenn nichts zu ändern ist.
func (r *UserRepo) SetName(ctx context.Context, userID, name string) error {
	if _, err := r.db.ExecContext(ctx,
		`UPDATE users SET name = ? WHERE id = ? AND name <> ?`, name, userID, name,
	); err != nil {
		return fmt.Errorf("users.SetName: %w", err)
	}
	return nil
}

// ListAdmins liefert alle User mit is_admin=1. Wird vom Notification-
// Subsystem (#305) für system.backup-failed-Empfänger und vom Admin-Tab
// genutzt.
func (r *UserRepo) ListAdmins(ctx context.Context) ([]*User, error) {
	const q = `SELECT id, email, name, created, is_admin FROM users WHERE is_admin = 1 ORDER BY email`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("users.ListAdmins: %w", err)
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u := &User{}
		var adm int
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Created, &adm); err != nil {
			return nil, fmt.Errorf("users.ListAdmins scan: %w", err)
		}
		u.IsAdmin = adm == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserListEntry ist ein Eintrag der Admin-Nutzerliste (GET /admin/users,
// Karte #575, Folge zu #571).
type UserListEntry struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"is_admin"`
	Created string `json:"created"`
	// BoardsOwner zählt Boards, auf denen der Nutzer die Rolle owner hat.
	BoardsOwner int `json:"boards_owner"`
	// BoardsEinzelOwner zählt darunter die, deren EINZIGER Owner er ist —
	// dieselbe Definition wie einzigerOwnerBoards in user_loeschung.go (kein
	// anderer owner-Eintrag auf demselben Board). Zweimal formuliert statt
	// geteilt, weil hier über ALLE Nutzer auf einmal gezählt wird und dort
	// nur für einen — beide Subqueries müssen bei einer künftigen Änderung
	// der Definition zusammen angefasst werden.
	BoardsEinzelOwner int `json:"boards_einzel_owner"`
}

// ListMitBoardZahlen liefert alle Nutzer sortiert nach Email, samt
// Board-Owner-Zahlen für den Nutzer-Tab im Admin-Dialog (Karte #575).
func (r *UserRepo) ListMitBoardZahlen(ctx context.Context) ([]*UserListEntry, error) {
	const q = `
		SELECT u.id, u.email, u.name, u.is_admin, u.created,
		  (SELECT COUNT(*) FROM acl a WHERE a.user_email = u.email AND a.role = 'owner') AS boards_owner,
		  (SELECT COUNT(*) FROM acl a WHERE a.user_email = u.email AND a.role = 'owner'
		     AND NOT EXISTS (SELECT 1 FROM acl o WHERE o.board_id = a.board_id AND o.role = 'owner' AND o.user_email <> a.user_email)
		  ) AS boards_einzel_owner
		FROM users u
		ORDER BY u.email`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("users.ListMitBoardZahlen: %w", err)
	}
	defer rows.Close()
	out := []*UserListEntry{}
	for rows.Next() {
		e := &UserListEntry{}
		var adm int
		if err := rows.Scan(&e.ID, &e.Email, &e.Name, &adm, &e.Created, &e.BoardsOwner, &e.BoardsEinzelOwner); err != nil {
			return nil, fmt.Errorf("users.ListMitBoardZahlen scan: %w", err)
		}
		e.IsAdmin = adm == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// BoardUebersicht ist ein Eintrag der Admin-Board-Liste (F-1439).
type BoardUebersicht struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	UpdatedAt int64    `json:"updated_at"`
	HatACL    bool     `json:"hat_acl"`
	Owners    []string `json:"owners"`
}

// ListAlleBoards liefert ALLE Boards (auch ohne ACL-Eintrag, z. B. das
// Seed-Board `default`), jüngst geändert zuerst, samt Owner-E-Mails.
//
// Seitenweise (F-1442): limit/offset als LIMIT/OFFSET; limit <= 0 heisst
// Standard (StandardBoardSeite), mehr als MaxBoardSeite wird gekappt. Zweiter
// Rückgabewert: Gesamtzahl aller Boards (unabhängig von der Seite).
func (r *UserRepo) ListAlleBoards(ctx context.Context, limit, offset int) ([]*BoardUebersicht, int, error) {
	if limit <= 0 {
		limit = StandardBoardSeite
	}
	if limit > MaxBoardSeite {
		limit = MaxBoardSeite
	}
	if offset < 0 {
		offset = 0
	}
	var gesamt int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM boards`).Scan(&gesamt); err != nil {
		return nil, 0, fmt.Errorf("users.ListAlleBoards count: %w", err)
	}
	const q = `
		SELECT b.id, b.title, b.updated_at,
		  EXISTS(SELECT 1 FROM acl a WHERE a.board_id = b.id),
		  COALESCE((SELECT group_concat(a.user_email, ',') FROM acl a WHERE a.board_id = b.id AND a.role = 'owner'), '')
		FROM boards b
		ORDER BY b.updated_at DESC, b.id
		LIMIT ? OFFSET ?`
	rows, err := r.db.QueryContext(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("users.ListAlleBoards: %w", err)
	}
	defer rows.Close()
	out := []*BoardUebersicht{}
	for rows.Next() {
		e := &BoardUebersicht{Owners: []string{}}
		var hat int
		var owners string
		if err := rows.Scan(&e.ID, &e.Title, &e.UpdatedAt, &hat, &owners); err != nil {
			return nil, 0, fmt.Errorf("users.ListAlleBoards scan: %w", err)
		}
		e.HatACL = hat == 1
		if owners != "" {
			e.Owners = strings.Split(owners, ",")
			sort.Strings(e.Owners)
		}
		out = append(out, e)
	}
	return out, gesamt, rows.Err()
}

// Seitengrössen von ListAlleBoards (F-1442).
const (
	StandardBoardSeite = 500
	MaxBoardSeite      = 1000
)

// CountAdmins zählt User mit is_admin=1. Dient als Self-Lockout-Schutz
// (#306/ADR-0101 §2): der letzte Admin darf sich selbst nicht entziehen.
func (r *UserRepo) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("users.CountAdmins: %w", err)
	}
	return n, nil
}

// SetAdminFlag setzt is_admin für einen Benutzer (per ID). Aufrufer ist für
// den Self-Lockout-Schutz verantwortlich (CountAdmins zuvor prüfen, in einer
// Transaktion mit dem UPDATE — siehe BootstrapRepo / Admin-Handler).
func (r *UserRepo) SetAdminFlag(ctx context.Context, userID string, isAdmin bool) error {
	flag := 0
	if isAdmin {
		flag = 1
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE users SET is_admin = ? WHERE id = ?`, flag, userID,
	); err != nil {
		return fmt.Errorf("users.SetAdminFlag: %w", err)
	}
	return nil
}

// ErrLastAdminLockout signalisiert dass die Mutation den letzten Admin
// entzogen hätte — Self-Lockout-Schutz griff (#306/ADR-0101 §2,
// Sprint-40-Kritiker-Fix [K]P1).
var ErrLastAdminLockout = errors.New("storage: last admin lockout would occur")

// SetAdminFlagSelfSafe entzieht ODER vergibt is_admin atomar mit dem
// Self-Lockout-Check. Beide Operationen laufen in EINER BEGIN IMMEDIATE-Tx
// (F-191-Pattern): COUNT(*) der Admins + UPDATE sind serialisiert, kein
// Race zwischen zwei gleichzeitig entziehenden Admins.
//
// Wenn der UPDATE den letzten Admin entzöge (Count vor dem Update == 1 und
// targetWasAdmin == true UND isAdmin == false) → ErrLastAdminLockout, Tx
// wird gerollt.
func (r *UserRepo) SetAdminFlagSelfSafe(ctx context.Context, userID string, isAdmin bool) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("users.SetAdminFlagSelfSafe: conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("users.SetAdminFlagSelfSafe: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Aktuellen is_admin-Stand des Targets + Count innerhalb der Tx lesen.
	var targetCurrent int
	err = conn.QueryRowContext(ctx, `SELECT is_admin FROM users WHERE id = ?`, userID).Scan(&targetCurrent)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("users.SetAdminFlagSelfSafe: %w", ErrUserNotFound)
	}
	if err != nil {
		return fmt.Errorf("users.SetAdminFlagSelfSafe: target lookup: %w", err)
	}

	if !isAdmin && targetCurrent == 1 {
		// Wir entziehen einem aktuellen Admin: prüfen ob noch andere übrig
		// bleiben. Count != 1 → safe; == 1 → last-admin-lockout.
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&count); err != nil {
			return fmt.Errorf("users.SetAdminFlagSelfSafe: count: %w", err)
		}
		if count <= 1 {
			return ErrLastAdminLockout
		}
	}

	flag := 0
	if isAdmin {
		flag = 1
	}
	if _, err := conn.ExecContext(ctx, `UPDATE users SET is_admin = ? WHERE id = ?`, flag, userID); err != nil {
		return fmt.Errorf("users.SetAdminFlagSelfSafe: update: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("users.SetAdminFlagSelfSafe: commit: %w", err)
	}
	committed = true
	return nil
}
