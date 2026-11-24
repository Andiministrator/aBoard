package storage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

// ADR-0132 — Board-Freigabe für eine E-Mail-Domain.

func domainTestRepo(t *testing.T, boards ...string) (*AclRepo, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range boards {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO boards (id, title, version, state_json, updated_at, created_at)
			 VALUES (?, 't', '1', '{}', 0, 0)`, id); err != nil {
			t.Fatalf("insert board: %v", err)
		}
	}
	return NewAclRepo(db), ctx
}

func TestErlaubteACLDomains_Normalisierung(t *testing.T) {
	t.Setenv(ACLDomainsEnv, " @Firma.de , x.org,,firma.de, kaputt, a@b.de, .lead.de ")
	got := ErlaubteACLDomains()
	want := []string{"firma.de", "x.org"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ErlaubteACLDomains = %v, erwartet %v", got, want)
	}
	t.Setenv(ACLDomainsEnv, "")
	if got := ErlaubteACLDomains(); len(got) != 0 {
		t.Fatalf("leere Variable: %v, erwartet keine Domain", got)
	}
}

func TestAclDomain_SetNurErlaubtUndNieOwner(t *testing.T) {
	r, ctx := domainTestRepo(t, "b1")

	// Ohne Betreiber-Liste: kein Domain-Eintrag (Gegenprobe andere Domain).
	t.Setenv(ACLDomainsEnv, "")
	if err := r.Set(ctx, "b1", "@firma.de", RoleViewer); !errors.Is(err, ErrDomainNichtErlaubt) {
		t.Fatalf("Set ohne Liste: err=%v, erwartet ErrDomainNichtErlaubt", err)
	}

	t.Setenv(ACLDomainsEnv, "firma.de")
	if err := r.Set(ctx, "b1", "@andere.de", RoleViewer); !errors.Is(err, ErrDomainNichtErlaubt) {
		t.Fatalf("Set fremde Domain: err=%v", err)
	}
	// Keine Subdomain-Treffer.
	if err := r.Set(ctx, "b1", "@sub.firma.de", RoleViewer); !errors.Is(err, ErrDomainNichtErlaubt) {
		t.Fatalf("Set Subdomain: err=%v", err)
	}
	if err := r.Set(ctx, "b1", "@firma.de", RoleOwner); !errors.Is(err, ErrDomainRolle) {
		t.Fatalf("Set owner für Domain: err=%v, erwartet ErrDomainRolle", err)
	}
	if err := r.Set(ctx, "b1", " @Firma.DE ", RoleEditor); err != nil {
		t.Fatalf("Set erlaubte Domain: %v", err)
	}
	if role, found, err := r.GetRole(ctx, "b1", "@firma.de"); err != nil || !found || role != RoleEditor {
		t.Fatalf("Domain-Schlüssel exakt: role=%q found=%v err=%v", role, found, err)
	}
}

func TestAclDomain_WirksameRolle(t *testing.T) {
	r, ctx := domainTestRepo(t, "b1", "b2", "b3")
	t.Setenv(ACLDomainsEnv, "firma.de")

	mustSet := func(board, email, rolle string) {
		t.Helper()
		if err := r.Set(ctx, board, email, rolle); err != nil {
			t.Fatalf("Set %s %s: %v", board, email, err)
		}
	}
	mustSet("b1", "chef@firma.de", RoleOwner)
	mustSet("b1", "@firma.de", RoleEditor)
	mustSet("b1", "eng@firma.de", RoleViewer) // enger als die Domain
	mustSet("b2", "@firma.de", RoleViewer)
	mustSet("b3", "neu@firma.de", RoleEditor)

	pruefe := func(board, email, wantRole string, wantFound bool) {
		t.Helper()
		role, found, err := r.GetRole(ctx, board, email)
		if err != nil || found != wantFound || role != wantRole {
			t.Fatalf("GetRole(%s,%s) = %q,%v,%v; erwartet %q,%v", board, email, role, found, err, wantRole, wantFound)
		}
	}
	pruefe("b1", "neu@firma.de", RoleEditor, true) // nur über die Domain
	pruefe("b1", "NEU@Firma.de", RoleEditor, true) // F-260-Normalisierung gilt auch hier
	pruefe("b1", "eng@firma.de", RoleViewer, true) // eigener Eintrag geht vor, auch nach unten
	pruefe("b1", "chef@firma.de", RoleOwner, true) // eigener Eintrag geht vor
	pruefe("b1", "x@andere.de", "", false)         // andere Domain
	pruefe("b1", "x@sub.firma.de", "", false)      // keine Subdomain
	pruefe("b2", "neu@firma.de", RoleViewer, true)

	got, err := r.ListBoardsWithRoleForUser(ctx, "neu@firma.de")
	if err != nil {
		t.Fatalf("ListBoardsWithRoleForUser: %v", err)
	}
	want := []BoardRoleEntry{{"b1", RoleEditor}, {"b2", RoleViewer}, {"b3", RoleEditor}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListBoardsWithRoleForUser = %+v, erwartet %+v", got, want)
	}
	ids, err := r.ListBoardsForUser(ctx, "eng@firma.de")
	if err != nil || !reflect.DeepEqual(ids, []string{"b1", "b2"}) {
		t.Fatalf("ListBoardsForUser(eng) = %v, %v", ids, err)
	}
	mitRolle, _ := r.ListBoardsWithRoleForUser(ctx, "eng@firma.de")
	if mitRolle[0].Role != RoleViewer {
		t.Fatalf("eigener viewer-Eintrag muss die Domain-Rolle schlagen, bekam %q", mitRolle[0].Role)
	}

	// Betreiber nimmt die Domain heraus → sofort wirkungslos, Zeilen bleiben.
	t.Setenv(ACLDomainsEnv, "")
	pruefe("b1", "neu@firma.de", "", false)
	pruefe("b1", "eng@firma.de", RoleViewer, true)
	ids, _ = r.ListBoardsForUser(ctx, "neu@firma.de")
	if !reflect.DeepEqual(ids, []string{"b3"}) {
		t.Fatalf("nach Abschalten: %v, erwartet nur [b3]", ids)
	}
	list, _ := r.ListByBoard(ctx, "b1")
	if len(list) != 3 || list[0].UserEmail != "@firma.de" {
		t.Fatalf("Domain-Zeile muss beim Abschalten erhalten bleiben: %+v", list)
	}
	// Entfernen bleibt möglich, auch wenn die Domain nicht mehr erlaubt ist.
	if err := r.Delete(ctx, "b1", "@firma.de"); err != nil {
		t.Fatalf("Delete Domain-Eintrag: %v", err)
	}
}

func TestAclDomain_KeinePersonImVerzeichnis(t *testing.T) {
	r, ctx := domainTestRepo(t, "b1")
	t.Setenv(ACLDomainsEnv, "firma.de")
	if err := r.Set(ctx, "b1", "@firma.de", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := r.Set(ctx, "b1", "a_b@firma.de", RoleOwner); err != nil {
		t.Fatal(err)
	}
	subs, err := r.ListKnownSubjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Email != "a_b@firma.de" {
		t.Fatalf("ListKnownSubjects = %+v, erwartet nur a_b@firma.de", subs)
	}
}

// ADR-0132 §6: Agenten-Zugänge bleiben an einen persönlichen Eintrag gebunden.
// Eine Domain-Freigabe als editor reicht weder zum Anlegen noch erscheint das
// Board in der Selbstbedienungs-Liste; ein bestehendes Token einer Person
// verliert seine Rolle, sobald nur noch die Domain trägt.
func TestAclDomain_AgentenBrauchenPersoenlichenEintrag(t *testing.T) {
	ctx, repo, acl, _ := personenDB(t)
	t.Setenv(ACLDomainsEnv, "firma.de")
	if err := acl.Set(ctx, "b2", "@firma.de", RoleEditor); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE boards SET agent_selbstbedienung = 1 WHERE id = 'b2'`); err != nil {
		t.Fatal(err)
	}
	// Wirksam ist anna auf b2 editor (über die Domain) …
	if role, found, _ := acl.GetRole(ctx, "b2", "anna@firma.de"); !found || role != RoleEditor {
		t.Fatalf("Vorbedingung: anna auf b2 = %q %v", role, found)
	}
	// … einen Agenten bekommt sie dort trotzdem nicht.
	if _, _, err := repo.Anlegen(ctx, AgentTokenAnlage{Name: "anna-tg", BoardID: "b2", Role: "editor", UserID: "u-anna", Selbstbedienung: true}); !errors.Is(err, ErrAgentKeinZugang) {
		t.Fatalf("Anlegen nur über Domain: err=%v, erwartet ErrAgentKeinZugang", err)
	}
	liste, err := repo.SelbstbedienungsBoardsFuer(ctx, "anna@firma.de")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range liste {
		if b.ID == "b2" {
			t.Fatalf("b2 darf nicht in der Selbstbedienungs-Liste stehen: %+v", liste)
		}
	}
	// F-1512: Prüfung je Anfrage. Ein Token, das anna mit persönlichem
	// editor-Eintrag auf b1 bekam, gilt nicht weiter, wenn der persönliche
	// Eintrag fällt und nur noch die Domain-Freigabe trägt.
	if err := acl.Set(ctx, "b1", "@firma.de", RoleEditor); err != nil {
		t.Fatal(err)
	}
	klartext, tok := mitPerson(t, ctx, repo, "anna-b1", "u-anna", AgentRolleEditor)
	if _, err := repo.Lookup(ctx, klartext); err != nil {
		t.Fatalf("Vorbedingung: Token mit persönlichem Eintrag gilt: %v", err)
	}
	if err := acl.Delete(ctx, "b1", "anna@firma.de"); err != nil {
		t.Fatal(err)
	}
	if role, found, _ := acl.GetRole(ctx, "b1", "anna@firma.de"); !found || role != RoleEditor {
		t.Fatalf("Vorbedingung: anna bleibt über die Domain editor, bekam %q %v", role, found)
	}
	if _, err := repo.Lookup(ctx, klartext); !errors.Is(err, ErrAgentTokenUnbekannt) {
		t.Fatalf("Lookup nur über Domain: err=%v, erwartet ErrAgentTokenUnbekannt", err)
	}
	if aktiv, err := repo.IstAktiv(ctx, tok.ID); aktiv || err != nil {
		t.Fatalf("IstAktiv nur über Domain: %v %v", aktiv, err)
	}
}

// F-1511: Der Erstkontakt legt für einen Domain-Schlüssel kein Board an —
// sonst stünde ("@x.de", owner) in der ACL.
func TestBootstrap_DomainSchluesselKeinePerson(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewBootstrapRepo(db).BootstrapNewUser(ctx, "u1", "b1", " @Firma.de", "x"); err == nil {
		t.Fatal("BootstrapNewUser mit @firma.de: erwartet Fehler")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM acl`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("acl-Zeilen nach abgewiesenem Bootstrap: %d %v", n, err)
	}
}

// F-1514: Das Service-Board wird nie per Domain freigegeben.
func TestAclDomain_ServiceBoardAusgenommen(t *testing.T) {
	r, ctx := domainTestRepo(t, ServiceBoardID)
	t.Setenv(ACLDomainsEnv, "firma.de")
	if err := r.Set(ctx, ServiceBoardID, "@firma.de", RoleViewer); !errors.Is(err, ErrDomainServiceBoard) {
		t.Fatalf("Set @firma.de am Service-Board: err=%v, erwartet ErrDomainServiceBoard", err)
	}
	if err := r.Set(ctx, ServiceBoardID, "a@firma.de", RoleViewer); err != nil {
		t.Fatalf("persönlicher Eintrag am Service-Board bleibt möglich: %v", err)
	}
}
