package storage

// Karte #799: Migration 0039 (anbieter, beleg an agent_tokens) und der Weg
// durch Anlegen/List.

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// Ein Token aus Schema 38 bleibt gültig und hat danach leere Angaben; ein neues
// Token trägt seine Angaben bis in die Liste.
func TestAgentHerkunft799_MigrationUndRundweg(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", dsnFuer(filepath.Join(t.TempDir(), "a.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	alle, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range alle {
		if m.version > 38 {
			break
		}
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 38"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBoardRepo(db).Insert(ctx, "b1", "B", "2", []byte(`{"version":"2","board":{"id":"b1","title":"B"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_tokens (id,name,board_id,role,token_hash,created_at,created_by) VALUES ('t1','alt','b1','editor','h1','2026-01-01','andi')`); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo := NewAgentTokenRepo(db)
	if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "neu", BoardID: "b1", Role: AgentRolleEditor, CreatedBy: "cli",
		Anbieter: "Beispiel-Anbieter", Beleg: "vertraege/vertrag.md"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "leer", BoardID: "b1", Role: AgentRolleEditor, CreatedBy: "cli"}); err != nil {
		t.Fatal(err)
	}
	// Kritiker K-6: Anbieter und Beleg stehen im Anlage-Audit (das die Token-Zeile
	// überlebt, wenn das Board gelöscht wird); ohne Angaben keine leeren Schlüssel.
	audit := func(name string) string {
		t.Helper()
		var v string
		if err := db.QueryRowContext(ctx, `SELECT new_value_redacted FROM server_config_audit WHERE key = ? AND action = 'agent-token-create'`, "agent-token:"+name).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := audit("neu"); !strings.HasSuffix(v, ";anbieter=Beispiel-Anbieter;beleg=vertraege/vertrag.md") {
		t.Errorf("Audit mit Angaben: %q", v)
	}
	if v := audit("leer"); strings.Contains(v, "anbieter") || strings.Contains(v, "beleg") {
		t.Errorf("Audit ohne Angaben: %q", v)
	}
	liste, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	je := map[string]AgentToken{}
	for _, tk := range liste {
		je[tk.Name] = tk
	}
	if a := je["alt"]; a.Anbieter != "" || a.Beleg != "" || a.RevokedAt != "" {
		t.Errorf("Bestandstoken: %+v", a)
	}
	if n := je["neu"]; n.Anbieter != "Beispiel-Anbieter" || n.Beleg != "vertraege/vertrag.md" {
		t.Errorf("neues Token: %+v", n)
	}
}
