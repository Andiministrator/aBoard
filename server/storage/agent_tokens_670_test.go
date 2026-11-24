package storage

// Karte #670 (ADR-0133 §6): Token-Rolle „vorschlag“ — nur als Projekt-Token,
// Migration 0025 (CHECK auf role erweitert, Mischformen ausgeschlossen).

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestAgent670_VorschlagNurAlsProjektToken(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"b1", ServiceBoardID} {
		if _, err := NewBoardRepo(db).Insert(ctx, id, "B", "2", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1', 'anna@firma.de', 'A', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b1', 'anna@firma.de', 'owner', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	repo := NewAgentTokenRepo(db)

	klar, tok, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "recherche", BoardID: "b1", Role: AgentRolleVorschlag, CreatedBy: "cli"})
	if err != nil {
		t.Fatalf("Anlegen vorschlag: %v", err)
	}
	got, err := repo.Lookup(ctx, klar)
	if err != nil || got.WirksameRolle != AgentRolleVorschlag || got.BoardID != "b1" || got.UserID != "" {
		t.Fatalf("Lookup: %+v %v", got, err)
	}
	if ok, err := repo.IstAktiv(ctx, tok.ID); err != nil || !ok {
		t.Fatalf("IstAktiv: %v %v", ok, err)
	}

	// Mit Person (auch als Owner des Boards), delegierend oder in Selbstbedienung:
	// abgelehnt, bevor etwas angelegt wird.
	for name, a := range map[string]AgentTokenAnlage{
		"mit-person":  {Name: "v1", BoardID: "b1", Role: AgentRolleVorschlag, UserID: "u1"},
		"delegierend": {Name: "v2", Role: AgentRolleVorschlag, Delegierend: true},
		"selbst":      {Name: "v3", BoardID: "b1", Role: AgentRolleVorschlag, UserID: "u1", Selbstbedienung: true},
		"service":     {Name: "v4", BoardID: ServiceBoardID, Role: AgentRolleVorschlag},
	} {
		if _, _, err := repo.Anlegen(ctx, a); !errors.Is(err, ErrAgentEingabe) {
			t.Errorf("%s: err = %v, erwartet ErrAgentEingabe", name, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_tokens`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("Tokens nach Ablehnungen: %d %v", n, err)
	}
	// Der CHECK hält dasselbe in der Tabelle fest, auch an Anlegen vorbei.
	for name, q := range map[string]string{
		"vorschlag-mit-person": `INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, user_id) VALUES ('x1', 'x1', 'b1', 'vorschlag', 'h1', 'n', 'u1')`,
		"vorschlag-delegiert":  `INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, delegierend) VALUES ('x2', 'x2', NULL, 'vorschlag', 'h2', 'n', 1)`,
		"unbekannte-rolle":     `INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at) VALUES ('x3', 'x3', 'b1', 'owner', 'h3', 'n')`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s: CHECK hat nicht gegriffen", name)
		}
	}
	if _, err := db.Exec(`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at) VALUES ('x4', 'x4', 'b1', 'vorschlag', 'h4', 'n')`); err != nil {
		t.Fatalf("Projekt-Token vorschlag direkt: %v", err)
	}
}

// Migration 0025 baut agent_tokens wie 0024 neu: Bestand (auch delegierende
// Tokens), Indizes, Kaskade und der Audit-Trigger aus 0018 bleiben.
func TestMigration0025_BestandUndTriggerBleiben(t *testing.T) {
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
		if m.version > 24 {
			continue
		}
		if _, err := raw.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("Vorab-Migration %s: %v", m.name, err)
		}
	}
	if _, err := raw.ExecContext(ctx, "PRAGMA user_version = 24"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('b1', 'B', '2', '{}', 1, 1)`,
		`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, created_by, user_id, expires_at, kennung, delegierend)
		 VALUES ('t1', 'alt', 'b1', 'viewer', 'hash-alt', '2026-01-01', 'cli', NULL, '2099-01-01T00:00:00Z', 'abcdefghij', 0)`,
		`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, created_by, delegierend)
		 VALUES ('t2', 'owui', NULL, 'editor', 'hash-owui', '2026-01-01', 'cli', 1)`,
	} {
		if _, err := raw.ExecContext(ctx, q); err != nil {
			t.Fatalf("Bestand: %v", err)
		}
	}
	_ = raw.Close()

	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open (migriert 0025): %v", err)
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
	if err := db.QueryRowContext(ctx, `SELECT delegierend FROM agent_tokens WHERE id = 't2' AND board_id IS NULL`).Scan(&deleg); err != nil || deleg != 1 {
		t.Fatalf("delegierendes Token nach Migration: %d %v", deleg, err)
	}
	var indizes int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'agent_tokens'
		AND name IN ('agent_tokens_name_aktiv', 'agent_tokens_kennung', 'agent_tokens_user')`).Scan(&indizes); err != nil || indizes != 3 {
		t.Fatalf("Indizes: %d %v", indizes, err)
	}
	if _, err := db.Exec(`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at) VALUES ('t3', 'alt', 'b1', 'editor', 'h3', 'n')`); err == nil {
		t.Fatal("Unique-Index agent_tokens_name_aktiv fehlt")
	}
	if _, err := db.Exec(`DELETE FROM boards WHERE id = 'b1'`); err != nil {
		t.Fatal(err)
	}
	var rest, audit int
	_ = db.QueryRow(`SELECT COUNT(*) FROM agent_tokens WHERE id = 't1'`).Scan(&rest)
	_ = db.QueryRow(`SELECT COUNT(*) FROM server_config_audit WHERE action = 'agent-token-revoke-board-deleted' AND key = 'agent-token:alt'`).Scan(&audit)
	if rest != 0 || audit != 1 {
		t.Fatalf("Kaskade/Trigger: rest=%d audit=%d", rest, audit)
	}
}
