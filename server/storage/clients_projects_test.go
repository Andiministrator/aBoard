package storage_test

// clients_projects_test.go — Tests für ClientRepo + ProjectRepo (#307, ADR-0102).
//
// Deckt ab: Create/Get/List/Update/SetActive/HardDelete für beide Entities,
// Soft-Delete-Default-Filter, Cascade SET NULL bei Client-Lösch, UNIQUE-
// Constraint auf clients.name und projects.(name, client_id), NULL-client_id-
// Special-Case (mehrere globale Projekte mit gleichem Namen sind möglich).

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func openTestDB(t *testing.T) *storage.ClientRepo {
	t.Helper()
	// helper not actually used — see openRepos below
	return nil
}

func openRepos(t *testing.T) (*storage.ClientRepo, *storage.ProjectRepo, func()) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(dir, "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	cleanup := func() { _ = db.Close() }
	return storage.NewClientRepo(db), storage.NewProjectRepo(db), cleanup
}

// ── ClientRepo ────────────────────────────────────────────────────────

func TestClient_CreateAndGet(t *testing.T) {
	clients, _, done := openRepos(t)
	defer done()
	ctx := context.Background()

	c, err := clients.Create(ctx, "c1", "Acme GmbH", "Beschreibung", "kontakt@acme.de")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID != "c1" || c.Name != "Acme GmbH" {
		t.Errorf("c=%+v", c)
	}
	if !c.Active {
		t.Errorf("Active sollte true sein nach Create")
	}
	if c.CreatedAt == "" || c.UpdatedAt == "" {
		t.Errorf("Timestamps fehlen: %+v", c)
	}

	got, err := clients.GetByID(ctx, "c1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Acme GmbH" || got.Contact != "kontakt@acme.de" {
		t.Errorf("get=%+v", got)
	}
}

func TestClient_CreateLeereIDOderName_Fehler(t *testing.T) {
	clients, _, done := openRepos(t)
	defer done()
	ctx := context.Background()

	if _, err := clients.Create(ctx, "", "Name", "", ""); err == nil {
		t.Error("Create mit leerer ID = nil err, erwartet Fehler")
	}
	if _, err := clients.Create(ctx, "id", "", "", ""); err == nil {
		t.Error("Create mit leerem Name = nil err, erwartet Fehler")
	}
}

func TestClient_GetUnbekannt_ErrClientNotFound(t *testing.T) {
	clients, _, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, err := clients.GetByID(ctx, "ghost")
	if err != storage.ErrClientNotFound {
		t.Errorf("err=%v, erwartet ErrClientNotFound", err)
	}
}

func TestClient_NameUnique(t *testing.T) {
	clients, _, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, _ = clients.Create(ctx, "c1", "Acme", "", "")
	if _, err := clients.Create(ctx, "c2", "Acme", "", ""); err == nil {
		t.Error("erwartet UNIQUE-Violation bei doppeltem Namen")
	}
}

func TestClient_ListNurAktivStandard(t *testing.T) {
	clients, _, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, _ = clients.Create(ctx, "c1", "Active GmbH", "", "")
	_, _ = clients.Create(ctx, "c2", "Inactive GmbH", "", "")
	_ = clients.SetActive(ctx, "c2", false)

	out, err := clients.List(ctx, storage.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 || out[0].ID != "c1" {
		t.Errorf("List default = %d Einträge, erwartet 1 (nur active); out=%+v", len(out), out)
	}

	out2, _ := clients.List(ctx, storage.ListOptions{IncludeInactive: true})
	if len(out2) != 2 {
		t.Errorf("List include-inactive = %d, erwartet 2", len(out2))
	}
}

func TestClient_UpdateAendertNameUndKontakt(t *testing.T) {
	clients, _, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, _ = clients.Create(ctx, "c1", "Alt", "altdesc", "alt@x.de")

	got, err := clients.Update(ctx, "c1", "Neu", "neudesc", "neu@x.de")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Name != "Neu" || got.Contact != "neu@x.de" {
		t.Errorf("update=%+v", got)
	}
}

func TestClient_UpdateUnbekannt_ErrClientNotFound(t *testing.T) {
	clients, _, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, err := clients.Update(ctx, "ghost", "X", "", "")
	if err != storage.ErrClientNotFound {
		t.Errorf("err=%v, erwartet ErrClientNotFound", err)
	}
}

func TestClient_HardDeleteSetzteClientIdAufNULL(t *testing.T) {
	clients, projects, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, _ = clients.Create(ctx, "c1", "Acme", "", "")
	_, _ = projects.Create(ctx, "p1", "Migration", "", "c1")

	if err := clients.HardDelete(ctx, "c1"); err != nil {
		t.Fatalf("HardDelete: %v", err)
	}

	p, err := projects.GetByID(ctx, "p1")
	if err != nil {
		t.Fatalf("Projekt nach Client-Lösch nicht mehr da: %v", err)
	}
	if p.ClientID.Valid {
		t.Errorf("project.client_id = %v, erwartet NULL (FK ON DELETE SET NULL)", p.ClientID)
	}
}

// ── ProjectRepo ───────────────────────────────────────────────────────

func TestProject_CreateOhneClient(t *testing.T) {
	_, projects, done := openRepos(t)
	defer done()
	ctx := context.Background()

	p, err := projects.Create(ctx, "p1", "Solo-Projekt", "", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ClientID.Valid {
		t.Errorf("ClientID sollte NULL sein; got %+v", p.ClientID)
	}
	if !p.Active {
		t.Errorf("Active sollte true sein")
	}
}

func TestProject_CreateMitClient(t *testing.T) {
	clients, projects, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, _ = clients.Create(ctx, "c1", "Acme", "", "")

	p, err := projects.Create(ctx, "p1", "Migration", "Schema-Migration", "c1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !p.ClientID.Valid || p.ClientID.String != "c1" {
		t.Errorf("ClientID=%+v, erwartet c1", p.ClientID)
	}
}

func TestProject_UniqueNamePerClient(t *testing.T) {
	clients, projects, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, _ = clients.Create(ctx, "c1", "Acme", "", "")
	_, _ = clients.Create(ctx, "c2", "Beta GmbH", "", "")

	if _, err := projects.Create(ctx, "p1", "Migration", "", "c1"); err != nil {
		t.Fatalf("Create p1: %v", err)
	}
	// Anderer Client, gleicher Name → erlaubt.
	if _, err := projects.Create(ctx, "p2", "Migration", "", "c2"); err != nil {
		t.Errorf("Create p2 (anderer Client) = err %v, erwartet ok", err)
	}
	// Gleicher Client, gleicher Name → UNIQUE-Violation.
	if _, err := projects.Create(ctx, "p3", "Migration", "", "c1"); err == nil {
		t.Error("erwartet UNIQUE-Violation bei (name,client_id)-Doppelung")
	}
}

func TestProject_NullClientId_MehrereNamensgleichErlaubt(t *testing.T) {
	// SQLite-Quirk: UNIQUE(name, client_id) lässt mehrere Rows mit NULL-
	// client_id zu, weil NULL != NULL. Im MVP akzeptiert (siehe ADR-0102).
	_, projects, done := openRepos(t)
	defer done()
	ctx := context.Background()
	if _, err := projects.Create(ctx, "p1", "Internes Projekt", "", ""); err != nil {
		t.Fatalf("Create p1: %v", err)
	}
	if _, err := projects.Create(ctx, "p2", "Internes Projekt", "", ""); err != nil {
		t.Errorf("zwei NULL-client_id-Projekte mit gleichem Namen sollten gehen (SQLite-NULL-Quirk); err=%v", err)
	}
}

func TestProject_ListFilterClientId(t *testing.T) {
	clients, projects, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, _ = clients.Create(ctx, "c1", "Acme", "", "")
	_, _ = clients.Create(ctx, "c2", "Beta GmbH", "", "")
	_, _ = projects.Create(ctx, "p1", "A1", "", "c1")
	_, _ = projects.Create(ctx, "p2", "A2", "", "c1")
	_, _ = projects.Create(ctx, "p3", "B1", "", "c2")

	out, err := projects.List(ctx, storage.ListOptions{}, "c1")
	if err != nil {
		t.Fatalf("List c1: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len=%d, erwartet 2 für c1", len(out))
	}
	for _, p := range out {
		if p.ClientID.String != "c1" {
			t.Errorf("Filter-Lecks: %+v hat client_id=%v", p, p.ClientID)
		}
	}
}

func TestProject_UpdateAendertClientId(t *testing.T) {
	clients, projects, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, _ = clients.Create(ctx, "c1", "Acme", "", "")
	_, _ = clients.Create(ctx, "c2", "Beta GmbH", "", "")
	_, _ = projects.Create(ctx, "p1", "Wandelt", "", "c1")

	got, err := projects.Update(ctx, "p1", "Wandelt", "neue Beschr.", "c2")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.ClientID.String != "c2" {
		t.Errorf("Update client_id=%+v, erwartet c2", got.ClientID)
	}

	// Update auf "" → NULL setzen.
	got2, err := projects.Update(ctx, "p1", "Wandelt", "", "")
	if err != nil {
		t.Fatalf("Update auf NULL: %v", err)
	}
	if got2.ClientID.Valid {
		t.Errorf("Update mit clientID='' sollte ClientID auf NULL setzen; got %+v", got2.ClientID)
	}
}

func TestProject_SetActiveSoftDelete(t *testing.T) {
	_, projects, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, _ = projects.Create(ctx, "p1", "X", "", "")

	if err := projects.SetActive(ctx, "p1", false); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	out, _ := projects.List(ctx, storage.ListOptions{}, "")
	if len(out) != 0 {
		t.Errorf("Default-List sollte p1 nicht zeigen; got %d", len(out))
	}
	out2, _ := projects.List(ctx, storage.ListOptions{IncludeInactive: true}, "")
	if len(out2) != 1 {
		t.Errorf("Include-inactive-List len=%d, erwartet 1", len(out2))
	}
}

func TestProject_HardDelete(t *testing.T) {
	_, projects, done := openRepos(t)
	defer done()
	ctx := context.Background()
	_, _ = projects.Create(ctx, "p1", "X", "", "")
	if err := projects.HardDelete(ctx, "p1"); err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	if _, err := projects.GetByID(ctx, "p1"); err != storage.ErrProjectNotFound {
		t.Errorf("Get nach HardDelete = %v, erwartet ErrProjectNotFound", err)
	}
}

func TestProject_HardDeleteUnbekannt_ErrProjectNotFound(t *testing.T) {
	_, projects, done := openRepos(t)
	defer done()
	ctx := context.Background()
	if err := projects.HardDelete(ctx, "ghost"); err != storage.ErrProjectNotFound {
		t.Errorf("err=%v, erwartet ErrProjectNotFound", err)
	}
}
