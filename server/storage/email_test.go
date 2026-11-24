package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// TestNormalizeEmail_Idempotent_UndKanonisierung — F-260: die zentrale
// Normalisierung trimmt Whitespace und lowercased. Idempotent.
func TestNormalizeEmail_Idempotent_UndKanonisierung(t *testing.T) {
	cases := map[string]string{
		"  Owner@X.de ":      "owner@x.de",
		"OWNER@X.DE":         "owner@x.de",
		"owner@x.de":         "owner@x.de",
		"\tMixed@Case.COM\n": "mixed@case.com",
		"   ":                "",
		"":                   "",
	}
	for in, want := range cases {
		got := NormalizeEmail(in)
		if got != want {
			t.Errorf("NormalizeEmail(%q) = %q, erwartet %q", in, got, want)
		}
		// Idempotenz: zweite Anwendung ändert nichts.
		if again := NormalizeEmail(got); again != got {
			t.Errorf("NormalizeEmail nicht idempotent: %q → %q", got, again)
		}
	}
}

// TestAclRepo_F260_CaseInsensitiveLookup — der Kern-Exploit aus F-260:
// ACL-Eintrag `owner@x.de`, Lookup mit `  Owner@X.de ` MUSS treffen.
func TestAclRepo_F260_CaseInsensitiveLookup(t *testing.T) {
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

	// Schreiben in kanonischer Form.
	if err := r.Set(ctx, "b1", "owner@x.de", RoleOwner); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Lookup mit gemischtem Casing + Whitespace → MUSS treffen (F-260).
	role, found, err := r.GetRole(ctx, "b1", "  Owner@X.de ")
	if err != nil || !found || role != RoleOwner {
		t.Fatalf("GetRole(' Owner@X.de ') = role=%q found=%v err=%v, erwartet owner/true (F-260 Owner-Lockout)", role, found, err)
	}

	// ListBoardsForUser ebenfalls case-insensitiv.
	boards, err := r.ListBoardsForUser(ctx, "OWNER@X.DE")
	if err != nil || len(boards) != 1 || boards[0] != "b1" {
		t.Fatalf("ListBoardsForUser(OWNER@X.DE) = %v err=%v, erwartet [b1]", boards, err)
	}

	// Schreibseite: Set mit ungetrimmter/Upper-Email speichert normalisiert
	// (kein Duplikat-Eintrag, Upsert trifft denselben Key).
	if err := r.Set(ctx, "b1", "  OWNER@x.de  ", RoleEditor); err != nil {
		t.Fatalf("Set ungetrimmt: %v", err)
	}
	list, err := r.ListByBoard(ctx, "b1")
	if err != nil {
		t.Fatalf("ListByBoard: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListByBoard len=%d, erwartet 1 (normalisierter Upsert, kein Duplikat)", len(list))
	}
	if list[0].UserEmail != "owner@x.de" || list[0].Role != RoleEditor {
		t.Fatalf("Eintrag = %+v, erwartet {owner@x.de, editor} (normalisiert gespeichert)", list[0])
	}

	// Delete ebenfalls normalisiert.
	if err := r.Delete(ctx, "b1", "Owner@X.DE"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, _ := r.GetRole(ctx, "b1", "owner@x.de"); found {
		t.Fatalf("Eintrag nach Delete(Owner@X.DE) noch da (F-260 Delete-Mismatch)")
	}

	// Set mit reiner Whitespace-Email → leer → Fehler (keine kaputte Row).
	if err := r.Set(ctx, "b1", "   ", RoleOwner); err == nil {
		t.Fatalf("Set('   ') = nil, erwartet Fehler (leere Email nach Normalisierung)")
	}
}

// TestMigration0004_BackfillNormalisiertBestand — F-260: bestehende Rows mit
// gemischtem Casing/Whitespace (vor v1.20.1 geschrieben) werden durch
// Migration 0004 in die kanonische Form gebracht. Simuliert einen
// Bestands-Stand auf Schema-Version 3, schreibt nicht-normalisierte Rows
// direkt per SQL (am Repo vorbei) und lässt 0004 nachziehen.
func TestMigration0004_BackfillNormalisiertBestand(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := Open(ctx, Config{DBPath: dbPath}) // bringt DB auf Version 4
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx,
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
		 VALUES ('b1', 't', '1', '{}', 0, 0)`); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	// Nicht-normalisierte Bestands-Rows direkt einfügen (Repo würde normalisieren).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO acl (board_id, user_email, role, created)
		 VALUES ('b1', '  Owner@X.de ', 'owner', '2026-01-01')`); err != nil {
		t.Fatalf("insert acl: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, email, name, created)
		 VALUES ('u1', '  User@Example.COM ', 'N', '2026-01-01')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	// Migration 0004 direkt aus dem embed-FS ausführen (nicht via
	// RunMigrations, weil das Folge-Migrations doppelt-applien würde —
	// CREATE TABLE in 0005 ist nicht idempotent). Testet die 0004-SQL-
	// Semantik isoliert.
	migration4SQL, err := migrationFiles.ReadFile("migrations/0004_normalize_emails.sql")
	if err != nil {
		t.Fatalf("read 0004 sql: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(migration4SQL)); err != nil {
		t.Fatalf("apply 0004 sql: %v", err)
	}

	var aclEmail, userEmail string
	if err := db.QueryRowContext(ctx, "SELECT user_email FROM acl WHERE board_id='b1'").Scan(&aclEmail); err != nil {
		t.Fatalf("scan acl: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT email FROM users WHERE id='u1'").Scan(&userEmail); err != nil {
		t.Fatalf("scan user: %v", err)
	}
	if aclEmail != "owner@x.de" {
		t.Errorf("acl.user_email nach Backfill = %q, erwartet owner@x.de", aclEmail)
	}
	if userEmail != "user@example.com" {
		t.Errorf("users.email nach Backfill = %q, erwartet user@example.com", userEmail)
	}

	// Versions-Pragma wird hier nicht geprüft — wir haben 0004 direkt aus
	// dem embed-FS ausgeführt, ohne den RunMigrations-Versions-Pfad
	// anzustoßen. Die DB-Version bleibt damit auf dem Stand des initialen
	// Open() (= latest).
}

// TestUserRepo_F260_CaseInsensitive — Upsert/GetByEmail normalisieren.
func TestUserRepo_F260_CaseInsensitive(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := NewUserRepo(db)

	if _, err := r.Upsert(ctx, "id-1", "  User@Example.COM ", "Name"); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	u, err := r.GetByEmail(ctx, "user@example.com")
	if err != nil {
		t.Fatalf("GetByEmail(normalisiert): %v", err)
	}
	if u.Email != "user@example.com" {
		t.Errorf("gespeicherte Email = %q, erwartet user@example.com", u.Email)
	}
	// Zweiter Upsert mit anderem Casing → kein Duplikat, stabile id.
	u2, err := r.Upsert(ctx, "id-2", "USER@EXAMPLE.COM", "Neu")
	if err != nil {
		t.Fatalf("Upsert #2: %v", err)
	}
	if u2.ID != "id-1" {
		t.Errorf("id nach Re-Upsert = %q, erwartet id-1 (stabil, kein Duplikat)", u2.ID)
	}
}
