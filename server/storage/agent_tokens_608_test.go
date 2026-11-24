package storage

// Karte #608 (ADR-0130 §6): Agenten-Token je (Person, Board).

import (
	"context"
	"errors"
	"testing"
	"time"
)

// personenDB: Board b1 mit Anna (editor), Viewer Vera, Owner Olga; Board b2 ohne Anna.
func personenDB(t *testing.T) (context.Context, *AgentTokenRepo, *AclRepo, *UserRepo) {
	t.Helper()
	ctx, repo, boards, _ := openAgentTestDB(t)
	if _, err := boards.Insert(ctx, "b2", "Zwei", "2", []byte(`{"version":"2","board":{"id":"b2","title":"Zwei"}}`)); err != nil {
		t.Fatal(err)
	}
	users := NewUserRepo(repo.db)
	acl := NewAclRepo(repo.db)
	for _, u := range [][3]string{{"u-anna", "anna@firma.de", "editor"}, {"u-vera", "vera@firma.de", "viewer"}, {"u-olga", "olga@firma.de", "owner"}} {
		if _, err := repo.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, 'N', '2026-01-01', 0)`, u[0], u[1]); err != nil {
			t.Fatal(err)
		}
		if err := acl.Set(ctx, "b1", u[1], u[2]); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, repo, acl, users
}

func mitPerson(t *testing.T, ctx context.Context, repo *AgentTokenRepo, name, user, rolle string) (string, *AgentToken) {
	t.Helper()
	k, tok, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: name, BoardID: "b1", Role: rolle, CreatedBy: "admin", UserID: user})
	if err != nil {
		t.Fatalf("Anlegen %s: %v", name, err)
	}
	return k, tok
}

func TestAgentToken608_RolleSinktMitACL(t *testing.T) {
	ctx, repo, acl, _ := personenDB(t)
	klartext, tok := mitPerson(t, ctx, repo, "annas-claude", "u-anna", AgentRolleEditor)
	got, err := repo.Lookup(ctx, klartext)
	if err != nil || got.WirksameRolle != RoleEditor || got.UserEmail != "anna@firma.de" {
		t.Fatalf("Lookup: %+v %v", got, err)
	}
	if err := acl.Set(ctx, "b1", "anna@firma.de", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.Lookup(ctx, klartext); err != nil || got.WirksameRolle != RoleViewer || got.Role != RoleEditor {
		t.Fatalf("nach Herabstufung: %+v %v", got, err)
	}
	if err := acl.Set(ctx, "b1", "anna@firma.de", RoleOwner); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Lookup(ctx, klartext); got.WirksameRolle != RoleEditor {
		t.Fatalf("Owner-Person: Agent höchstens editor, war %q", got.WirksameRolle)
	}
	if aktiv, _ := repo.IstAktiv(ctx, tok.ID); !aktiv {
		t.Fatal("IstAktiv")
	}
}

// K-6: Nur von DIESEM Board entfernt (der Nutzer bleibt) → kein Zugriff.
func TestAgentToken608_PersonVomBoardEntfernt(t *testing.T) {
	ctx, repo, acl, users := personenDB(t)
	klartext, tok := mitPerson(t, ctx, repo, "annas-claude", "u-anna", AgentRolleEditor)
	if err := acl.Delete(ctx, "b1", "anna@firma.de"); err != nil {
		t.Fatal(err)
	}
	if _, err := users.GetByEmail(ctx, "anna@firma.de"); err != nil {
		t.Fatalf("Nutzer muss bleiben: %v", err)
	}
	if _, err := repo.Lookup(ctx, klartext); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatalf("Lookup nach Entfernen: %v", err)
	}
	if aktiv, err := repo.IstAktiv(ctx, tok.ID); aktiv || err != nil {
		t.Fatalf("IstAktiv nach Entfernen: %v %v", aktiv, err)
	}
	// Wieder aufgenommen → wieder gültig (das Token ist nicht widerrufen).
	if err := acl.Set(ctx, "b1", "anna@firma.de", RoleEditor); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Lookup(ctx, klartext); err != nil {
		t.Fatalf("nach Wiederaufnahme: %v", err)
	}
}

func TestAgentToken608_PersonGeloescht(t *testing.T) {
	ctx, repo, _, users := personenDB(t)
	if _, err := repo.db.Exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-admin', 'admin@firma.de', 'A', '2026-01-01', 1)`); err != nil {
		t.Fatal(err)
	}
	klartext, tok := mitPerson(t, ctx, repo, "annas-claude", "u-anna", AgentRolleEditor)
	projekt, _, err := repo.Create(ctx, "projekt", "b1", AgentRolleEditor, "cli")
	if err != nil {
		t.Fatal(err)
	}
	erg, err := users.Loeschen(ctx, LoeschAnweisung{UserID: "u-anna", AdminID: "u-admin", AdminEmail: "admin@firma.de"})
	if err != nil {
		t.Fatal(err)
	}
	if len(erg.AgentTokens) != 1 || erg.AgentTokens[0] != tok.ID {
		t.Fatalf("widerrufene Tokens: %v", erg.AgentTokens)
	}
	if _, err := repo.Lookup(ctx, klartext); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatalf("Token der gelöschten Person: %v", err)
	}
	var widerrufen string
	if err := repo.db.QueryRow(`SELECT COALESCE(revoked_at, '') FROM agent_tokens WHERE id = ?`, tok.ID).Scan(&widerrufen); err != nil || widerrufen == "" {
		t.Fatalf("nicht widerrufen: %q %v", widerrufen, err)
	}
	if _, err := repo.Lookup(ctx, projekt); err != nil {
		t.Fatalf("Projekt-Token ohne Person muss bleiben: %v", err)
	}
}

func TestAgentToken608_ProjektTokenUnabhaengigVonACL(t *testing.T) {
	ctx, repo, _, _ := personenDB(t)
	klartext, _, err := repo.Create(ctx, "team-container", "b2", AgentRolleEditor, "cli")
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Lookup(ctx, klartext)
	if err != nil || got.WirksameRolle != RoleEditor || got.UserID != "" || got.UserEmail != "" {
		t.Fatalf("Projekt-Token: %+v %v", got, err)
	}
}

func TestAgentToken608_AnlageRegeln(t *testing.T) {
	ctx, repo, _, _ := personenDB(t)
	// Viewer bekommt keinen Agenten, eine Person ohne Rolle auf b2 auch nicht.
	if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "vera", BoardID: "b1", Role: "viewer", UserID: "u-vera"}); !errors.Is(err, ErrAgentKeinZugang) {
		t.Fatalf("Viewer: %v", err)
	}
	if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "anna2", BoardID: "b2", Role: "editor", UserID: "u-anna"}); !errors.Is(err, ErrAgentKeinZugang) {
		t.Fatalf("fremdes Board: %v", err)
	}
	if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "x", BoardID: "b1", Role: "editor", UserID: "u-gibtsnicht"}); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unbekannte Person: %v", err)
	}
	// Selbstbedienung: ab Werk aus, nach dem Einschalten erlaubt.
	selbst := AgentTokenAnlage{Name: "annas-claude", BoardID: "b1", Role: "editor", UserID: "u-anna", Selbstbedienung: true}
	if _, _, err := repo.Anlegen(ctx, selbst); !errors.Is(err, ErrAgentSelbstbedienungAus) {
		t.Fatalf("Selbstbedienung aus: %v", err)
	}
	if an, _ := repo.Selbstbedienung(ctx, "b1"); an {
		t.Fatal("ab Werk an")
	}
	if err := repo.SetzeSelbstbedienung(ctx, "b1", true, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Anlegen(ctx, selbst); err != nil {
		t.Fatalf("Selbstbedienung an: %v", err)
	}
	if bs, _ := repo.SelbstbedienungsBoardsFuer(ctx, "anna@firma.de"); len(bs) != 1 || bs[0].ID != "b1" {
		t.Fatalf("Boards für Anna: %v", bs)
	}
	if bs, _ := repo.SelbstbedienungsBoardsFuer(ctx, "vera@firma.de"); len(bs) != 0 {
		t.Fatalf("Viewer darf keine: %v", bs)
	}
	if err := repo.SetzeSelbstbedienung(ctx, "gibtsnicht", true, "admin"); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("Schalter für fehlendes Board: %v", err)
	}
}

func TestAgentToken608_AblaufUndFormat(t *testing.T) {
	ctx, repo, _, _ := personenDB(t)
	for _, tage := range []int{-1, AgentTokenGueltigMax + 1} {
		if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "x", BoardID: "b1", Role: "editor", GueltigTage: tage}); !errors.Is(err, ErrAgentEingabe) {
			t.Errorf("GueltigTage %d: %v", tage, err)
		}
	}
	klartext, tok, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "kurz", BoardID: "b1", Role: "editor", GueltigTage: 1})
	if err != nil {
		t.Fatal(err)
	}
	if AgentTokenKennung(klartext) != tok.Kennung || len(tok.Kennung) != 10 {
		t.Fatalf("Kennung %q / %q", AgentTokenKennung(klartext), tok.Kennung)
	}
	if _, err := time.Parse(agentAblaufFormat, tok.ExpiresAt); err != nil {
		t.Fatalf("Ablauf %q: %v", tok.ExpiresAt, err)
	}
	_, std, err := repo.Create(ctx, "standard", "b1", "editor", "cli")
	if err != nil {
		t.Fatal(err)
	}
	ende, err := time.Parse(agentAblaufFormat, std.ExpiresAt)
	if soll := timeNow().UTC().Add(90 * 24 * time.Hour); err != nil || ende.Before(soll.Add(-time.Minute)) || ende.After(soll.Add(time.Minute)) {
		t.Fatalf("Voreinstellung 90 Tage: %q %v", std.ExpiresAt, err)
	}
	// Uhr zwei Tage vor → abgelaufen.
	alt := timeNow
	timeNow = func() time.Time { return alt().Add(48 * time.Hour) }
	t.Cleanup(func() { timeNow = alt })
	if _, err := repo.Lookup(ctx, klartext); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatalf("abgelaufen: %v", err)
	}
	if aktiv, _ := repo.IstAktiv(ctx, tok.ID); aktiv {
		t.Fatal("IstAktiv nach Ablauf")
	}
}

// Altformat abt_<geheimnis> ohne Ablauf (vor Migration 0023 angelegt) gilt weiter.
func TestAgentToken608_AltformatGiltWeiter(t *testing.T) {
	ctx, repo, _, _ := personenDB(t)
	zufall, _ := newToken()
	alt := AgentTokenPraefix + zufall
	if _, err := repo.db.Exec(`INSERT INTO agent_tokens (id, name, board_id, role, token_hash, created_at, created_by) VALUES ('t-alt', 'alt', 'b1', 'editor', ?, '2026-01-01', 'cli')`, hashToken(alt)); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Lookup(ctx, alt)
	if err != nil || got.ExpiresAt != "" || got.Kennung != "" {
		t.Fatalf("Altformat: %+v %v", got, err)
	}
	if AgentTokenKennung(alt) != "" {
		t.Fatal("Altformat darf keine Kennung haben")
	}
}

func TestAgentToken608_Widerrufe(t *testing.T) {
	ctx, repo, _, _ := personenDB(t)
	k1, t1 := mitPerson(t, ctx, repo, "anna-eins", "u-anna", AgentRolleEditor)
	k2, t2 := mitPerson(t, ctx, repo, "anna-zwei", "u-anna", AgentRolleViewer)
	_, t3 := mitPerson(t, ctx, repo, "olgas", "u-olga", AgentRolleEditor)
	// Fremdes Token über die Selbstbedienung: unbekannt.
	if _, err := repo.RevokeID(ctx, t3.ID, "u-anna", "u-anna"); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatalf("fremd widerrufen: %v", err)
	}
	if name, err := repo.RevokeID(ctx, t1.ID, "u-anna", "u-anna"); err != nil || name != "anna-eins" {
		t.Fatalf("eigenes widerrufen: %q %v", name, err)
	}
	if _, err := repo.Lookup(ctx, k1); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatal("widerrufenes gilt noch")
	}
	ids, err := repo.RevokeFuerPerson(ctx, "u-anna", "admin")
	if err != nil || len(ids) != 1 || ids[0] != t2.ID {
		t.Fatalf("RevokeFuerPerson: %v %v", ids, err)
	}
	if _, err := repo.Lookup(ctx, k2); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatal("nach RevokeFuerPerson gilt es noch")
	}
	if aktiv, _ := repo.IstAktiv(ctx, t3.ID); !aktiv {
		t.Fatal("Olgas Token darf nicht betroffen sein")
	}
	l, _ := repo.ListFuerPerson(ctx, "u-anna")
	if len(l) != 2 || l[0].UserEmail != "anna@firma.de" {
		t.Fatalf("ListFuerPerson: %+v", l)
	}
}

// Session-Kritiker Sprint 78, K-1: Ein abgelaufenes Token belegte seinen Namen.
func TestAgentToken608_AbgelaufenerNameIstFrei(t *testing.T) {
	ctx, repo, _, _ := personenDB(t)
	alt, tok, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "rotiert", BoardID: "b1", Role: "editor", GueltigTage: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Solange es gilt: Name vergeben.
	if _, _, err := repo.Create(ctx, "rotiert", "b1", "editor", "cli"); !errors.Is(err, ErrAgentNameVergeben) {
		t.Fatalf("gültiges Token: %v", err)
	}
	vorher := timeNow
	timeNow = func() time.Time { return vorher().Add(48 * time.Hour) }
	t.Cleanup(func() { timeNow = vorher })
	neu, _, err := repo.Create(ctx, "rotiert", "b1", "editor", "cli")
	if err != nil {
		t.Fatalf("Nachfolger nach Ablauf: %v", err)
	}
	if _, err := repo.Lookup(ctx, neu); err != nil {
		t.Fatalf("Nachfolger gilt nicht: %v", err)
	}
	if _, err := repo.Lookup(ctx, alt); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatal("altes gilt wieder")
	}
	var widerrufen string
	if err := repo.db.QueryRow(`SELECT COALESCE(revoked_at, '') FROM agent_tokens WHERE id = ?`, tok.ID).Scan(&widerrufen); err != nil || widerrufen == "" {
		t.Fatalf("abgelaufenes nicht widerrufen: %q %v", widerrufen, err)
	}
	var n int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM server_config_audit WHERE action = 'agent-token-revoke-expired' AND key = 'agent-token:rotiert'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("Audit: %d %v", n, err)
	}
}

// TestAgentToken_NameBleibtAnPersonGebunden (F-1381, Kritiker Sprint 78):
// Ein Agentenname geht nie an eine andere Person über — auch nicht nach
// Widerruf oder Ablauf. Sonst erbte die zweite Person den Akteur
// `agent:<name>` und damit Urheberschaft (comment.edit) und Übernahmen der
// ersten. Gemessener Ausgangsfall: Anna widerruft „claude“, Vera legt
// „claude“ an, ihr Agent ändert Annas „für Anna“ gestempelten Kommentar.
func TestAgentToken_NameBleibtAnPersonGebunden(t *testing.T) {
	ctx, repo, acl, _ := personenDB(t)
	if err := acl.Set(ctx, "b1", "vera@firma.de", "editor"); err != nil {
		t.Fatal(err)
	}
	_, tok := mitPerson(t, ctx, repo, "claude", "u-anna", AgentRolleEditor)
	if _, err := repo.RevokeID(ctx, tok.ID, "", "u-anna"); err != nil {
		t.Fatal(err)
	}
	// Andere Person: abgelehnt, auch per Selbstbedienung-freiem Admin-Weg.
	if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "claude", BoardID: "b1", Role: "editor", CreatedBy: "admin", UserID: "u-vera"}); !errors.Is(err, ErrAgentNameVergeben) {
		t.Fatalf("fremde Person bekam den Namen: %v", err)
	}
	// Projekt-Token (ohne Person) zählt als eigene Person: ebenfalls abgelehnt.
	if _, _, err := repo.Create(ctx, "claude", "b1", "editor", "cli"); !errors.Is(err, ErrAgentNameVergeben) {
		t.Fatalf("Projekt-Token bekam den Personennamen: %v", err)
	}
	// Dieselbe Person darf ihren Namen wiederverwenden.
	mitPerson(t, ctx, repo, "claude", "u-anna", AgentRolleEditor)

	// Gegenrichtung: ein Projekt-Name geht nicht an eine Person.
	_, ptok, err := repo.Create(ctx, "bot", "b1", "editor", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RevokeID(ctx, ptok.ID, "", "cli"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "bot", BoardID: "b1", Role: "editor", CreatedBy: "admin", UserID: "u-anna"}); !errors.Is(err, ErrAgentNameVergeben) {
		t.Fatalf("Person bekam den Projekt-Namen: %v", err)
	}

	// Ein abgelehnter Versuch verändert nichts: ein abgelaufenes fremdes
	// Token bleibt unwiderrufen (die Prüfung läuft vor dem Ablauf-Widerruf).
	_, alt, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "kurz", BoardID: "b1", Role: "editor", CreatedBy: "admin", UserID: "u-anna", GueltigTage: 1})
	if err != nil {
		t.Fatal(err)
	}
	vorher := timeNow
	timeNow = func() time.Time { return vorher().Add(48 * time.Hour) }
	t.Cleanup(func() { timeNow = vorher })
	if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "kurz", BoardID: "b1", Role: "editor", CreatedBy: "admin", UserID: "u-vera"}); !errors.Is(err, ErrAgentNameVergeben) {
		t.Fatalf("abgelaufener fremder Name: %v", err)
	}
	var widerrufen string
	if err := repo.db.QueryRow(`SELECT COALESCE(revoked_at, '') FROM agent_tokens WHERE id = ?`, alt.ID).Scan(&widerrufen); err != nil || widerrufen != "" {
		t.Fatalf("abgelehnter Versuch hat widerrufen: %q %v", widerrufen, err)
	}
}
