package storage

// Karte #669 (ADR-0133 §4): delegierendes Dienst-Token für Open WebUI —
// Migration 0024 (board_id nullbar, Spalte delegierend), Anlage, Lookup,
// Kappung der Rolle.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestAgent669_DelegierendAnlegenUndLookup(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewAgentTokenRepo(db)

	klar, tok, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "open-webui", Role: AgentRolleEditor, CreatedBy: "cli", Delegierend: true})
	if err != nil {
		t.Fatalf("Anlegen delegierend: %v", err)
	}
	if !tok.Delegierend || tok.BoardID != "" {
		t.Fatalf("Anlage: %+v", tok)
	}
	got, err := repo.Lookup(ctx, klar)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !got.Delegierend || got.BoardID != "" || got.UserEmail != "" {
		t.Fatalf("Lookup: %+v", got)
	}
	// Keine wirksame Rolle ohne Person und Board — sonst hielte jemand die
	// Token-Rolle für eine Erlaubnis.
	if got.WirksameRolle != "" {
		t.Fatalf("WirksameRolle = %q, erwartet leer", got.WirksameRolle)
	}
	if ok, err := repo.IstAktiv(ctx, tok.ID); err != nil || !ok {
		t.Fatalf("IstAktiv: %v %v", ok, err)
	}
	l, err := repo.List(ctx)
	if err != nil || len(l) != 1 || !l[0].Delegierend {
		t.Fatalf("List: %+v %v", l, err)
	}
	var audit string
	if err := db.QueryRowContext(ctx, `SELECT new_value_redacted FROM server_config_audit WHERE key = 'agent-token:open-webui' AND action = 'agent-token-create'`).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if audit[:12] != "delegierend;" {
		t.Fatalf("Audit = %q", audit)
	}
}

func TestAgent669_DelegierendOhneBoardUndPerson(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewBoardRepo(db).Insert(ctx, "b1", "B", "2", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1', 'anna@firma.de', 'A', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	repo := NewAgentTokenRepo(db)
	for name, a := range map[string]AgentTokenAnlage{
		"mit-board":  {Name: "d1", BoardID: "b1", Role: "editor", Delegierend: true},
		"mit-person": {Name: "d2", UserID: "u1", Role: "editor", Delegierend: true},
		"selbst":     {Name: "d3", Role: "editor", Delegierend: true, Selbstbedienung: true},
	} {
		if _, _, err := repo.Anlegen(ctx, a); !errors.Is(err, ErrAgentEingabe) {
			t.Errorf("%s: err = %v, erwartet ErrAgentEingabe", name, err)
		}
	}
	// Ein gewöhnliches Token ohne Board bleibt verboten.
	if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "p1", Role: "editor"}); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("Projekt-Token ohne Board: %v", err)
	}
	// Der CHECK der Tabelle hält die Formen auseinander, auch an Create vorbei.
	for name, q := range map[string]string{
		"boardlos-nicht-delegierend": `INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, delegierend) VALUES ('x1', 'x1', NULL, 'editor', 'h1', 'n', 0)`,
		"delegierend-mit-board":      `INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, delegierend) VALUES ('x2', 'x2', 'b1', 'editor', 'h2', 'n', 1)`,
		"delegierend-mit-person":     `INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, delegierend, user_id) VALUES ('x3', 'x3', NULL, 'editor', 'h3', 'n', 1, 'u1')`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s: CHECK hat nicht gegriffen", name)
		}
	}
}

// Migration 0024 baut agent_tokens neu: Bestand, Indizes, Fremdschlüssel
// (Kaskade beim Board-Löschen) und der Audit-Trigger aus 0018 müssen danach
// genau so wirken wie vorher.
func TestMigration0024_BestandUndTriggerBleiben(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	raw, err := sql.Open("sqlite", dsnFuer(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := journalModusAufWAL(ctx, raw); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version > 23 {
			continue
		}
		if _, err := raw.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("Vorab-Migration %s: %v", m.name, err)
		}
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA user_version = 23"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('b1', 'B', '2', '{}', 1, 1)`,
		`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, created_by, user_id, expires_at, kennung)
		 VALUES ('t1', 'alt', 'b1', 'viewer', 'hash-alt', '2026-01-01', 'cli', NULL, '2099-01-01T00:00:00Z', 'abcdefghij')`,
	} {
		if _, err := raw.ExecContext(ctx, q); err != nil {
			t.Fatalf("Bestand: %v", err)
		}
	}
	_ = raw.Close()

	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open (migriert 0024): %v", err)
	}
	defer db.Close()
	var name, board, role, kennung string
	var deleg int
	if err := db.QueryRowContext(ctx, `SELECT name, board_id, role, kennung, delegierend FROM agent_tokens WHERE id = 't1'`).
		Scan(&name, &board, &role, &kennung, &deleg); err != nil {
		t.Fatal(err)
	}
	if name != "alt" || board != "b1" || role != "viewer" || kennung != "abcdefghij" || deleg != 0 {
		t.Fatalf("Bestand nach Migration: %s %s %s %s %d", name, board, role, kennung, deleg)
	}
	var indizes int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'agent_tokens'
		AND name IN ('agent_tokens_name_aktiv', 'agent_tokens_kennung', 'agent_tokens_user')`).Scan(&indizes); err != nil || indizes != 3 {
		t.Fatalf("Indizes: %d %v", indizes, err)
	}
	// Name eines aktiven Tokens bleibt eindeutig.
	if _, err := db.Exec(`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at) VALUES ('t2', 'alt', 'b1', 'editor', 'h2', 'n')`); err == nil {
		t.Fatal("Unique-Index agent_tokens_name_aktiv fehlt")
	}
	// Board löschen: Kaskade + Audit-Trigger aus 0018.
	if _, err := db.Exec(`DELETE FROM boards WHERE id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	var rest, audit int
	_ = db.QueryRow(`SELECT COUNT(*) FROM agent_tokens`).Scan(&rest)
	_ = db.QueryRow(`SELECT COUNT(*) FROM server_config_audit WHERE action = 'agent-token-revoke-board-deleted' AND key = 'agent-token:alt'`).Scan(&audit)
	if rest != 0 || audit != 1 {
		t.Fatalf("Kaskade/Trigger: rest=%d audit=%d", rest, audit)
	}
}

func TestAgent669_KappeAgentRolle(t *testing.T) {
	for _, f := range []struct{ token, acl, soll string }{
		{"editor", "owner", "editor"},
		{"editor", "editor", "editor"},
		{"editor", "viewer", "viewer"},
		{"viewer", "owner", "viewer"},
		{"editor", "", ""},
		{"editor", "gast", ""},
		{"", "owner", ""},
	} {
		if ist := KappeAgentRolle(f.token, f.acl); ist != f.soll {
			t.Errorf("KappeAgentRolle(%q, %q) = %q, soll %q", f.token, f.acl, ist, f.soll)
		}
	}
}
