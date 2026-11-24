package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestAclRepo_SetGetListDelete(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES ('b1', 't', '1', '{}', 0, 0)`); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	r := NewAclRepo(db)

	// Kein Eintrag → found=false, kein Fehler.
	if _, found, err := r.GetRole(ctx, "b1", "a@example.com"); err != nil || found {
		t.Fatalf("GetRole ohne Eintrag: found=%v err=%v", found, err)
	}

	if err := r.Set(ctx, "b1", "a@example.com", RoleOwner); err != nil {
		t.Fatalf("Set owner: %v", err)
	}
	if err := r.Set(ctx, "b1", "b@example.com", RoleViewer); err != nil {
		t.Fatalf("Set viewer: %v", err)
	}

	role, found, err := r.GetRole(ctx, "b1", "a@example.com")
	if err != nil || !found || role != RoleOwner {
		t.Fatalf("GetRole: role=%q found=%v err=%v", role, found, err)
	}

	// Upsert: gleiche (board,email) → Rolle ändert sich, kein Duplikat.
	if err := r.Set(ctx, "b1", "b@example.com", RoleEditor); err != nil {
		t.Fatalf("Set upsert: %v", err)
	}
	list, err := r.ListByBoard(ctx, "b1")
	if err != nil {
		t.Fatalf("ListByBoard: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListByBoard len = %d, erwartet 2 (Upsert darf nicht duplizieren)", len(list))
	}
	// Sortiert nach email: a@ vor b@.
	if list[0].UserEmail != "a@example.com" || list[1].Role != RoleEditor {
		t.Fatalf("ListByBoard unerwartet: %+v", list)
	}

	boards, err := r.ListBoardsForUser(ctx, "b@example.com")
	if err != nil {
		t.Fatalf("ListBoardsForUser: %v", err)
	}
	if len(boards) != 1 || boards[0] != "b1" {
		t.Fatalf("ListBoardsForUser = %v, erwartet [b1]", boards)
	}

	// Delete + Idempotenz.
	if err := r.Delete(ctx, "b1", "b@example.com"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, _ := r.GetRole(ctx, "b1", "b@example.com"); found {
		t.Fatalf("Eintrag nach Delete noch da")
	}
	if err := r.Delete(ctx, "b1", "b@example.com"); err != nil {
		t.Fatalf("Delete idempotent: %v", err)
	}
}

func TestAclRepo_RollenValidierung(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES ('b1', 't', '1', '{}', 0, 0)`); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	r := NewAclRepo(db)

	// Go-Validierung (Defense-in-depth, vor DB-Roundtrip).
	if err := r.Set(ctx, "b1", "a@example.com", "admin"); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("Set ungültige Rolle: erwartet ErrInvalidRole, bekam %v", err)
	}
	for _, ok := range []string{RoleOwner, RoleEditor, RoleViewer} {
		if !ValidRole(ok) {
			t.Fatalf("ValidRole(%q) = false", ok)
		}
	}
	if ValidRole("admin") {
		t.Fatalf("ValidRole(admin) = true")
	}

	// DB-CHECK-Constraint (greift auch bei direktem Insert unter Umgehung des Repos).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO acl (board_id, user_email, role, created)
		 VALUES ('b1', 'x@example.com', 'admin', '2026-05-16')`); err == nil {
		t.Fatalf("CHECK-Constraint auf acl.role greift nicht (direkter Insert 'admin' akzeptiert)")
	}
}

// FK acl.board_id → boards(id) ON DELETE CASCADE: Board-Löschung räumt die
// ACL-Einträge mit (ADR-0059 §3). foreign_keys ist via DSN-Pragma aktiv.
func TestAcl_CascadeOnBoardDelete(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES ('b1', 't', '1', '{}', 0, 0)`); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	r := NewAclRepo(db)
	if err := r.Set(ctx, "b1", "a@example.com", RoleOwner); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM boards WHERE id = 'b1'"); err != nil {
		t.Fatalf("delete board: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM acl WHERE board_id = 'b1'").Scan(&n); err != nil {
		t.Fatalf("count acl: %v", err)
	}
	if n != 0 {
		t.Fatalf("acl-Einträge nach Board-Delete übrig: %d (Cascade greift nicht)", n)
	}
}
