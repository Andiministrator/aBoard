package storage

// Karte #754 (ADR-0139 §5, E-1): Personen-Token für mehrere Boards —
// Migration 0031, Anlage nur mit persönlichem Eintrag ≥ editor (die
// Domain-Freigabe zählt nicht), Rolle je Board und je Aufruf, feste Liste.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func neu754(t *testing.T) (*AgentTokenRepo, func(q string, args ...any)) {
	t.Helper()
	t.Setenv(ACLDomainsEnv, "firma.de")
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, id := range []string{"b1", "b2", "b3", "b4", ServiceBoardID} {
		if _, err := NewBoardRepo(db).Insert(ctx, id, "Board "+id, "2", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1', 'anna@firma.de', 'A', '2026-01-01', 0)`)
	exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u2', 'bert@firma.de', 'B', '2026-01-01', 0)`)
	acl := NewAclRepo(db)
	for _, e := range [][3]string{
		{"b1", "anna@firma.de", "editor"},
		{"b2", "anna@firma.de", "owner"},
		{"b3", "anna@firma.de", "viewer"},
		{"b4", "@firma.de", "editor"}, // nur Domain-Freigabe
		{ServiceBoardID, "anna@firma.de", "owner"},
		{"b1", "@firma.de", "editor"},
	} {
		if err := acl.Set(ctx, e[0], e[1], e[2]); err != nil {
			t.Fatal(err)
		}
	}
	return NewAgentTokenRepo(db), exec
}

func TestAgent754_AnlageUndRegeln(t *testing.T) {
	repo, exec := neu754(t)
	ctx := context.Background()

	klar, tok, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "anna-alle", UserID: "u1", Role: AgentRolleEditor,
		CreatedBy: "cli", BoardIDs: []string{"b2", "b1", "b1"}})
	if err != nil {
		t.Fatalf("Anlegen: %v", err)
	}
	if !tok.Mehrere || tok.BoardID != "" || !reflect.DeepEqual(tok.Boards, []string{"b1", "b2"}) {
		t.Fatalf("Token: %+v", tok)
	}
	got, err := repo.Lookup(ctx, klar)
	if err != nil || !got.Mehrere || got.WirksameRolle != "" || got.UserEmail != "anna@firma.de" {
		t.Fatalf("Lookup: %+v %v", got, err)
	}

	// Ablehnungen — jede ein eigener Grund.
	faelle := []struct {
		name string
		a    AgentTokenAnlage
		soll error
	}{
		{"ohne Person", AgentTokenAnlage{Name: "x1", Role: AgentRolleEditor, BoardIDs: []string{"b1"}}, ErrAgentEingabe},
		{"mit einzelnem Board", AgentTokenAnlage{Name: "x2", UserID: "u1", BoardID: "b1", Role: AgentRolleEditor, BoardIDs: []string{"b2"}}, ErrAgentEingabe},
		{"delegierend", AgentTokenAnlage{Name: "x3", UserID: "u1", Role: AgentRolleEditor, Delegierend: true, BoardIDs: []string{"b1"}}, ErrAgentEingabe},
		{"vorschlag", AgentTokenAnlage{Name: "x4", UserID: "u1", Role: AgentRolleVorschlag, BoardIDs: []string{"b1"}}, ErrAgentEingabe},
		{"liste und alle", AgentTokenAnlage{Name: "x5", UserID: "u1", Role: AgentRolleEditor, BoardIDs: []string{"b1"}, AlleMeineBoards: true}, ErrAgentEingabe},
		{"leere id", AgentTokenAnlage{Name: "x6", UserID: "u1", Role: AgentRolleEditor, BoardIDs: []string{" "}}, ErrAgentEingabe},
		{"nur viewer", AgentTokenAnlage{Name: "x7", UserID: "u1", Role: AgentRolleViewer, BoardIDs: []string{"b3"}}, ErrAgentKeinZugang},
		{"nur Domain-Freigabe", AgentTokenAnlage{Name: "x8", UserID: "u1", Role: AgentRolleEditor, BoardIDs: []string{"b4"}}, ErrAgentKeinZugang},
		{"Service-Board", AgentTokenAnlage{Name: "x9", UserID: "u1", Role: AgentRolleEditor, BoardIDs: []string{ServiceBoardID}}, ErrAgentKeinZugang},
		{"unbekanntes Board", AgentTokenAnlage{Name: "x10", UserID: "u1", Role: AgentRolleEditor, BoardIDs: []string{"b1", "gibts-nicht"}}, ErrAgentKeinZugang},
		{"Person nur mit Domain", AgentTokenAnlage{Name: "x11", UserID: "u2", Role: AgentRolleEditor, AlleMeineBoards: true}, ErrAgentKeinZugang},
		{"Selbstbedienung aus", AgentTokenAnlage{Name: "x12", UserID: "u1", Role: AgentRolleEditor, BoardIDs: []string{"b1"}, Selbstbedienung: true}, ErrAgentKeinZugang},
	}
	for _, f := range faelle {
		if _, _, err := repo.Anlegen(ctx, f.a); !errors.Is(err, f.soll) {
			t.Errorf("%s: %v, erwartet %v", f.name, err, f.soll)
		}
	}

	// „Alle meine Boards“ = Stand jetzt: b1 (editor), b2 (owner) — nicht b3
	// (viewer), nicht b4 (Domain), nicht das Service-Board.
	_, alle, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "anna-jetzt", UserID: "u1", Role: AgentRolleViewer, CreatedBy: "cli", AlleMeineBoards: true})
	if err != nil || !reflect.DeepEqual(alle.Boards, []string{"b1", "b2"}) {
		t.Fatalf("alle: %+v %v", alle, err)
	}
	// Selbstbedienung: nur Boards mit eingeschaltetem Schalter.
	if err := repo.SetzeSelbstbedienung(ctx, "b2", true, "admin"); err != nil {
		t.Fatal(err)
	}
	_, selbst, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "anna-selbst", UserID: "u1", Role: AgentRolleEditor, CreatedBy: "u1",
		AlleMeineBoards: true, Selbstbedienung: true})
	if err != nil || !reflect.DeepEqual(selbst.Boards, []string{"b2"}) {
		t.Fatalf("selbst: %+v %v", selbst, err)
	}
	// Später freigegeben: kommt nicht dazu.
	exec(`UPDATE acl SET role = 'editor' WHERE board_id = 'b3' AND user_email = 'anna@firma.de'`)
	if r, _ := repo.RolleAufBoard(ctx, alle.ID, "b3"); r != "" {
		t.Errorf("b3 nach der Anlage freigegeben: Rolle %q, erwartet keine", r)
	}

	// Der CHECK hält die Mischformen auch ohne den Code fern.
	for _, q := range []string{
		`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, user_id, mehrere) VALUES ('c1', 'c1', 'b1', 'editor', 'h1', 'x', 'u1', 1)`,
		`INSERT INTO agent_tokens (id, name, role, token_hash, created_at, mehrere) VALUES ('c2', 'c2', 'editor', 'h2', 'x', 1)`,
		`INSERT INTO agent_tokens (id, name, role, token_hash, created_at, delegierend, mehrere) VALUES ('c3', 'c3', 'editor', 'h3', 'x', 1, 1)`,
		`INSERT INTO agent_tokens (id, name, role, token_hash, created_at, user_id, mehrere) VALUES ('c4', 'c4', 'vorschlag', 'h4', 'x', 'u1', 1)`,
	} {
		if _, err := repo.db.Exec(q); err == nil {
			t.Errorf("CHECK ließ durch: %s", q)
		}
	}
}

func TestAgent754_RolleJeBoardUndAufruf(t *testing.T) {
	repo, exec := neu754(t)
	ctx := context.Background()
	klar, tok, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "anna-zwei", UserID: "u1", Role: AgentRolleEditor, CreatedBy: "cli", BoardIDs: []string{"b1", "b2"}})
	if err != nil {
		t.Fatal(err)
	}
	// Ein zweites Token derselben Person nur für b1: die Liste von anna-zwei
	// gilt für es nicht.
	_, nurB1, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "anna-b1", UserID: "u1", Role: AgentRolleEditor, CreatedBy: "cli", BoardIDs: []string{"b1"}})
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := repo.RolleAufBoard(ctx, nurB1.ID, "b2"); r != "" {
		t.Errorf("anna-b1 auf b2 (Liste eines anderen Tokens): %q", r)
	}
	if m, _ := repo.BoardRollen(ctx, nurB1.ID); !reflect.DeepEqual(m, map[string]string{"b1": "editor"}) {
		t.Errorf("BoardRollen anna-b1: %v", m)
	}
	rolle := func(b string) string {
		t.Helper()
		r, err := repo.RolleAufBoard(ctx, tok.ID, b)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if rolle("b1") != RoleEditor || rolle("b2") != RoleEditor {
		t.Fatalf("b1=%q b2=%q, erwartet editor (owner gekappt)", rolle("b1"), rolle("b2"))
	}
	for _, b := range []string{"b3", "b4", ServiceBoardID, "", "gibts-nicht"} {
		if r := rolle(b); r != "" {
			t.Errorf("Board %q nicht in der Liste: Rolle %q", b, r)
		}
	}
	if m, err := repo.BoardRollen(ctx, tok.ID); err != nil || !reflect.DeepEqual(m, map[string]string{"b1": "editor", "b2": "editor"}) {
		t.Fatalf("BoardRollen: %v %v", m, err)
	}

	// Rolle herabgesetzt → Minimum; entzogen → kein Zugriff auf DIESES Board,
	// das Token gilt weiter für das andere.
	exec(`UPDATE acl SET role = 'viewer' WHERE board_id = 'b2' AND user_email = 'anna@firma.de'`)
	if rolle("b2") != RoleViewer {
		t.Errorf("b2 nach Herabstufung: %q, erwartet viewer", rolle("b2"))
	}
	exec(`DELETE FROM acl WHERE board_id = 'b1' AND user_email = 'anna@firma.de'`)
	if rolle("b1") != "" {
		t.Errorf("b1 nach Entzug: %q — die Domain-Freigabe auf b1 darf nicht zählen", rolle("b1"))
	}
	if aktiv, _ := repo.IstAktiv(ctx, tok.ID); !aktiv {
		t.Errorf("Token nach Entzug auf einem Board inaktiv — es gilt weiter für b2")
	}
	if m, _ := repo.BoardRollen(ctx, tok.ID); !reflect.DeepEqual(m, map[string]string{"b2": "viewer"}) {
		t.Errorf("BoardRollen nach Entzug: %v", m)
	}

	// Board gelöscht → aus der Liste, mit Audit; Liste zeigt es nicht mehr.
	exec(`DELETE FROM boards WHERE id = 'b2'`)
	var n int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM server_config_audit WHERE action = 'agent-token-board-removed' AND key = 'agent-token:anna-zwei'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("Audit agent-token-board-removed: %d %v", n, err)
	}
	l, err := repo.ListFuerPerson(ctx, "u1")
	if err != nil || len(l) != 2 {
		t.Fatalf("ListFuerPerson: %+v %v", l, err)
	}
	for _, x := range l {
		if !x.Mehrere || !reflect.DeepEqual(x.Boards, []string{"b1"}) {
			t.Errorf("ListFuerPerson %s: %+v", x.Name, x)
		}
	}

	// Widerruf und Ablauf: keine Rolle mehr, Lookup scheitert.
	if _, err := repo.RevokeID(ctx, tok.ID, "", "cli"); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b1', 'anna@firma.de', 'editor', '2026-01-01')`)
	if rolle("b1") != "" {
		t.Errorf("widerrufen: Rolle %q", rolle("b1"))
	}
	if _, err := repo.Lookup(ctx, klar); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Errorf("Lookup widerrufen: %v", err)
	}
	_, tok2, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "anna-kurz", UserID: "u1", Role: AgentRolleEditor, CreatedBy: "cli", BoardIDs: []string{"b1"}, GueltigTage: 1})
	if err != nil {
		t.Fatal(err)
	}
	alt := timeNow
	timeNow = func() time.Time { return alt().Add(48 * time.Hour) }
	defer func() { timeNow = alt }()
	if r, _ := repo.RolleAufBoard(ctx, tok2.ID, "b1"); r != "" {
		t.Errorf("abgelaufen: Rolle %q", r)
	}

	// Person gelöscht → Token unbekannt (Widerruf in derselben Transaktion
	// ist Sache der Nutzer-Löschung; hier nur: ohne Person gilt es nicht).
	timeNow = alt
	exec(`DELETE FROM users WHERE id = 'u1'`)
	if aktiv, _ := repo.IstAktiv(ctx, tok2.ID); aktiv {
		t.Errorf("Person gelöscht, Token weiter aktiv")
	}
}

// Kritiker #754, K-1: Die Service-Board-Sperre gilt bei jedem Aufruf, nicht
// nur bei der Anlage. Eine Zeile, die an der Anlage vorbei in die Liste kam
// (Import, Wiederherstellung, anderer Code), wirkt trotzdem nicht.
func TestAgent754_ServiceBoardJeAufrufGesperrt(t *testing.T) {
	repo, exec := neu754(t)
	ctx := context.Background()
	_, tok, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "anna-svc", UserID: "u1", Role: AgentRolleEditor, CreatedBy: "cli", BoardIDs: []string{"b1"}})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO agent_token_boards (token_id, board_id) VALUES (?, ?)`, tok.ID, ServiceBoardID)
	if r, err := repo.RolleAufBoard(ctx, tok.ID, ServiceBoardID); err != nil || r != "" {
		t.Errorf("RolleAufBoard(service) = %q %v, erwartet keine", r, err)
	}
	if m, err := repo.BoardRollen(ctx, tok.ID); err != nil || !reflect.DeepEqual(m, map[string]string{"b1": "editor"}) {
		t.Errorf("BoardRollen = %v %v, erwartet nur b1", m, err)
	}
}

// Kritiker #754, K-6: Ein aktives Token aus der Zeit vor 0031 gilt nach der
// Migration weiter (gewöhnliche Form, mehrere = 0).
func TestMigration0031_BestandBleibt(t *testing.T) {
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
		if m.version > 30 {
			continue
		}
		if _, err := raw.ExecContext(ctx, m.sql); err != nil {
			t.Fatalf("Vorab-Migration %s: %v", m.name, err)
		}
	}
	klar := AgentTokenPraefix + "abcdefghij_" + strings.Repeat("x", 43)
	for _, q := range []string{
		"PRAGMA user_version = 30",
		`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('b1', 'B', '2', '{}', 1, 1)`,
		`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u1', 'anna@firma.de', 'A', '2026-01-01', 0)`,
		`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b1', 'anna@firma.de', 'editor', '2026-01-01')`,
		`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, created_by, user_id, expires_at, kennung, delegierend)
		 VALUES ('t1', 'alt', 'b1', 'editor', '` + hashToken(klar) + `', '2026-01-01', 'cli', 'u1', '2099-01-01T00:00:00Z', 'abcdefghij', 0)`,
	} {
		if _, err := raw.ExecContext(ctx, q); err != nil {
			t.Fatalf("Bestand %q: %v", q, err)
		}
	}
	_ = raw.Close()
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open (migriert 0031): %v", err)
	}
	defer db.Close()
	got, err := NewAgentTokenRepo(db).Lookup(ctx, klar)
	if err != nil || got.ID != "t1" || got.Mehrere || got.BoardID != "b1" || got.WirksameRolle != RoleEditor {
		t.Fatalf("Lookup nach 0031: %+v %v", got, err)
	}
}
