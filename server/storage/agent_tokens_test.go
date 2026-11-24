package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func openAgentTestDB(t *testing.T) (context.Context, *AgentTokenRepo, *BoardRepo, *ConfigRepo) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := NewBoardRepo(db)
	if _, err := boards.Insert(ctx, "b1", "Board", "2", []byte(`{"version":"2","board":{"id":"b1","title":"Board"}}`)); err != nil {
		t.Fatal(err)
	}
	return ctx, NewAgentTokenRepo(db), boards, NewConfigRepo(db, nil, nil)
}

func TestAgentToken_AnlegenUndNachschlagen(t *testing.T) {
	ctx, repo, _, _ := openAgentTestDB(t)
	klartext, tok, err := repo.Create(ctx, "projekt-x", "b1", AgentRolleEditor, "admin-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(klartext, AgentTokenPraefix) || len(klartext) < 40 {
		t.Fatalf("Klartext %q", klartext)
	}
	got, err := repo.Lookup(ctx, klartext)
	if err != nil || got.ID != tok.ID || got.BoardID != "b1" || got.Role != "editor" || got.Name != "projekt-x" {
		t.Fatalf("Lookup: %+v, %v", got, err)
	}
	// Gespeichert ist nur der Hash.
	var gespeichert string
	if err := repo.db.QueryRowContext(ctx, `SELECT token_hash FROM agent_tokens WHERE id = ?`, tok.ID).Scan(&gespeichert); err != nil {
		t.Fatal(err)
	}
	if gespeichert == klartext || strings.Contains(gespeichert, klartext[len(AgentTokenPraefix):]) {
		t.Fatal("Klartext in der Datenbank")
	}
	for _, falsch := range []string{"", "abt_", klartext + "x", strings.TrimPrefix(klartext, AgentTokenPraefix)} {
		if _, err := repo.Lookup(ctx, falsch); !errors.Is(err, ErrAgentTokenUnbekannt) {
			t.Errorf("Lookup(%q) = %v, erwartet ErrAgentTokenUnbekannt", falsch, err)
		}
	}
}

func TestAgentToken_Eingaben(t *testing.T) {
	ctx, repo, _, _ := openAgentTestDB(t)
	for _, name := range []string{"", "Projekt", "a b", "x@y.de", "-start", strings.Repeat("a", 64),
		"3f2b8c1e-0a4d-4e5f-9a7b-1c2d3e4f5a6b"} { /* V-3: Form einer Token-id */
		if _, _, err := repo.Create(ctx, name, "b1", "editor", ""); !errors.Is(err, ErrAgentEingabe) {
			t.Errorf("Name %q: %v", name, err)
		}
	}
	for _, rolle := range []string{"owner", "", "Editor"} {
		if _, _, err := repo.Create(ctx, "ok", "b1", rolle, ""); !errors.Is(err, ErrAgentEingabe) {
			t.Errorf("Rolle %q: %v", rolle, err)
		}
	}
	if _, _, err := repo.Create(ctx, "ok", "gibtsnicht", "editor", ""); !errors.Is(err, ErrBoardNotFound) {
		t.Errorf("fremdes Board: %v", err)
	}
}

// TestAgentToken_WiderrufUndNeuanlage: Ein Name hat höchstens ein aktives
// Token; nach dem Widerruf gilt das alte nicht mehr, ein neues darf entstehen.
func TestAgentToken_WiderrufUndNeuanlage(t *testing.T) {
	ctx, repo, _, _ := openAgentTestDB(t)
	alt, tok, err := repo.Create(ctx, "agent", "b1", "viewer", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Create(ctx, "agent", "b1", "viewer", ""); !errors.Is(err, ErrAgentNameVergeben) {
		t.Fatalf("zweites aktives Token: %v", err)
	}
	id, err := repo.Revoke(ctx, "agent", "admin-1")
	if err != nil || id != tok.ID {
		t.Fatalf("Revoke: %q, %v", id, err)
	}
	if _, err := repo.Lookup(ctx, alt); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatalf("widerrufenes Token gilt noch: %v", err)
	}
	if aktiv, _ := repo.IstAktiv(ctx, tok.ID); aktiv {
		t.Fatal("IstAktiv nach Widerruf")
	}
	if _, err := repo.Revoke(ctx, "agent", ""); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatalf("doppelter Widerruf: %v", err)
	}
	neu, _, err := repo.Create(ctx, "agent", "b1", "editor", "")
	if err != nil {
		t.Fatalf("Neuanlage nach Widerruf: %v", err)
	}
	if _, err := repo.Lookup(ctx, neu); err != nil {
		t.Fatal(err)
	}
	l, _ := repo.List(ctx)
	if len(l) != 2 {
		t.Fatalf("List: %d Einträge, erwartet 2 (widerrufen + aktiv)", len(l))
	}
}

// TestAgentToken_Audit: Anlage und Widerruf stehen in server_config_audit
// (K-13) — mit Board und Rolle, ohne Token.
func TestAgentToken_Audit(t *testing.T) {
	ctx, repo, _, cfg := openAgentTestDB(t)
	klartext, _, err := repo.Create(ctx, "audit-agent", "b1", "editor", "admin-7")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Revoke(ctx, "audit-agent", "admin-8"); err != nil {
		t.Fatal(err)
	}
	eintraege, err := cfg.Audit(ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	gefunden := map[string]bool{}
	for _, e := range eintraege {
		zeile := strings.Join([]string{e.Key, e.UserID, e.Action, e.OldValueRedacted, e.NewValueRedacted}, "|")
		if strings.Contains(zeile, klartext[len(AgentTokenPraefix):]) {
			t.Fatal("Token im Audit")
		}
		if e.Key == "agent-token:audit-agent" {
			gefunden[e.Action+"/"+e.UserID] = true
			if e.Action == "agent-token-create" && !strings.HasPrefix(e.NewValueRedacted, "board=b1;role=editor;expires=") { // seit #608 mit Ablauf
				t.Errorf("Anlage-Audit: %q", e.NewValueRedacted)
			}
		}
	}
	if !gefunden["agent-token-create/admin-7"] || !gefunden["agent-token-revoke/admin-8"] {
		t.Fatalf("Audit unvollständig: %v", gefunden)
	}
}

// TestAgentToken_BoardLoeschenNimmtTokenMit: Ein Token ohne Board wäre ein
// Schlüssel ins Leere — der FK räumt es ab.
func TestAgentToken_BoardLoeschenNimmtTokenMit(t *testing.T) {
	ctx, repo, boards, _ := openAgentTestDB(t)
	klartext, _, err := repo.Create(ctx, "weg", "b1", "editor", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := boards.Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Lookup(ctx, klartext); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatalf("Token überlebt das Board: %v", err)
	}
}

// TestAgentToken_BoardLoeschenSchreibtAudit (Kritiker Sprint 71, K-A6): Das
// Verschwinden per Kaskade ist ein Widerruf und steht im Audit — nur für
// aktive Tokens, ein bereits widerrufenes ist schon protokolliert.
func TestAgentToken_BoardLoeschenSchreibtAudit(t *testing.T) {
	ctx, repo, boards, cfg := openAgentTestDB(t)
	if _, _, err := repo.Create(ctx, "aktiv", "b1", "editor", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Create(ctx, "alt", "b1", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Revoke(ctx, "alt", ""); err != nil {
		t.Fatal(err)
	}
	if err := boards.Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	eintraege, _ := cfg.Audit(ctx, "", 50)
	n := map[string]int{}
	for _, e := range eintraege {
		if e.Action == "agent-token-revoke-board-deleted" {
			n[e.Key]++
			if e.NewValueRedacted != "board=b1;role=editor" {
				t.Errorf("Audit-Wert %q", e.NewValueRedacted)
			}
		}
	}
	if n["agent-token:aktiv"] != 1 || n["agent-token:alt"] != 0 {
		t.Fatalf("Audit bei Board-Löschung: %v", n)
	}
}
