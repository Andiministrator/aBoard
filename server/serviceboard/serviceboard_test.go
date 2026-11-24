package serviceboard

import (
	"context"
	"fmt"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func openTestDB(t *testing.T) (context.Context, *storage.BoardRepo, *storage.AclRepo, *storage.ConfigRepo) {
	t.Helper()
	ctx := context.Background()
	dbPath := t.TempDir() + "/svc.db"
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	var n int
	idFn := func() string { n++; return fmt.Sprintf("test-id-%d", n) }
	return ctx, storage.NewBoardRepo(db), storage.NewAclRepo(db),
		storage.NewConfigRepo(db, nil, idFn)
}

func TestSeed_CreatesBoard_WhenEnabled(t *testing.T) {
	ctx, boards, acl, _ := openTestDB(t)
	logCalls := []string{}
	log := func(f string, a ...any) { logCalls = append(logCalls, f) }

	cfg := Config{Enabled: true, Title: "Service"}
	if err := Seed(ctx, boards, acl, cfg, log); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	b, err := boards.Get(ctx, storage.ServiceBoardID)
	if err != nil {
		t.Fatalf("Get nach Seed: %v", err)
	}
	if b.Title != "Service" {
		t.Errorf("Title = %q, erwartet Service", b.Title)
	}

	// ACL: mail-worker als editor.
	role, found, err := acl.GetRole(ctx, storage.ServiceBoardID, storage.MailWorkerEmail)
	if err != nil || !found {
		t.Fatalf("ACL mail-worker fehlt (found=%v err=%v)", found, err)
	}
	if role != storage.RoleEditor {
		t.Errorf("ACL mail-worker role = %q, erwartet editor", role)
	}
}

func TestSeed_Idempotent_PreservesExistingBoard(t *testing.T) {
	ctx, boards, acl, _ := openTestDB(t)
	noop := func(string, ...any) {}

	cfg := Config{Enabled: true, Title: "Original"}
	if err := Seed(ctx, boards, acl, cfg, noop); err != nil {
		t.Fatalf("Seed 1: %v", err)
	}
	// Manuelle Änderung am Titel via Update — simuliert User-Edit.
	b1, _ := boards.Get(ctx, storage.ServiceBoardID)
	if _, err := boards.Update(ctx, b1.ID, "User-Renamed", b1.Version, b1.StateJSON, 0); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Zweiter Seed mit anderem Title — darf NICHT überschreiben.
	if err := Seed(ctx, boards, acl, Config{Enabled: true, Title: "Other"}, noop); err != nil {
		t.Fatalf("Seed 2: %v", err)
	}
	b2, _ := boards.Get(ctx, storage.ServiceBoardID)
	if b2.Title != "User-Renamed" {
		t.Errorf("Title nach zweitem Seed = %q, erwartet User-Renamed (User-Edit erhalten)", b2.Title)
	}
}

func TestSeed_Disabled_DoesNothing(t *testing.T) {
	ctx, boards, acl, _ := openTestDB(t)
	logged := false
	log := func(f string, a ...any) { logged = true }

	cfg := Config{Enabled: false, Title: "Service"}
	if err := Seed(ctx, boards, acl, cfg, log); err != nil {
		t.Fatalf("Seed disabled: %v", err)
	}
	if _, err := boards.Get(ctx, storage.ServiceBoardID); err == nil {
		t.Errorf("Board angelegt trotz Enabled=false")
	}
	if !logged {
		t.Errorf("Disabled-Log fehlt")
	}
}

func TestLoadConfig_DefaultsWhenStoreEmpty(t *testing.T) {
	ctx, _, _, cfgRepo := openTestDB(t)
	c := LoadConfig(ctx, cfgRepo)
	if !c.Enabled {
		t.Errorf("Default Enabled=false")
	}
	if c.Title != DefaultTitle {
		t.Errorf("Default Title=%q, erwartet %q", c.Title, DefaultTitle)
	}
	if c.SpamDrop {
		t.Errorf("Default SpamDrop=true")
	}
}

func TestLoadConfig_ReadsStoreOverrides(t *testing.T) {
	ctx, _, _, cfgRepo := openTestDB(t)
	if err := cfgRepo.Set(ctx, KeyEnabled, "false", false, ""); err != nil {
		t.Fatalf("Set Enabled: %v", err)
	}
	if err := cfgRepo.Set(ctx, KeyTitle, "Tickets", false, ""); err != nil {
		t.Fatalf("Set Title: %v", err)
	}
	if err := cfgRepo.Set(ctx, KeySpamDrop, "true", false, ""); err != nil {
		t.Fatalf("Set SpamDrop: %v", err)
	}
	c := LoadConfig(ctx, cfgRepo)
	if c.Enabled {
		t.Errorf("Enabled = true, erwartet false")
	}
	if c.Title != "Tickets" {
		t.Errorf("Title = %q, erwartet Tickets", c.Title)
	}
	if !c.SpamDrop {
		t.Errorf("SpamDrop = false, erwartet true")
	}
}

// F-1862: Ein ungültiger ABOARD_BOOTSTRAP_OWNER landet nicht als Owner im ACL
// des Service-Boards — dieselbe Prüfung wie beim Default-Board.
func TestSeed_UngueltigerBootstrapOwnerKeinACLEintrag(t *testing.T) {
	for _, wert := range []string{"tippfehler", "@firma.de"} {
		t.Run(wert, func(t *testing.T) {
			t.Setenv(storage.BootstrapOwnerEnv, wert)
			ctx, boards, acl, _ := openTestDB(t)
			if err := Seed(ctx, boards, acl, Config{Enabled: true, Title: "Service"}, func(string, ...any) {}); err != nil {
				t.Fatalf("Seed: %v", err)
			}
			eintraege, err := acl.ListByBoard(ctx, storage.ServiceBoardID)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range eintraege {
				if e.Role == storage.RoleOwner {
					t.Fatalf("Owner-Eintrag %q trotz ungültiger Variable", e.UserEmail)
				}
			}
		})
	}
	t.Run("gültig", func(t *testing.T) {
		t.Setenv(storage.BootstrapOwnerEnv, "Chefin@Firma.de")
		ctx, boards, acl, _ := openTestDB(t)
		if err := Seed(ctx, boards, acl, Config{Enabled: true, Title: "Service"}, func(string, ...any) {}); err != nil {
			t.Fatalf("Seed: %v", err)
		}
		if role, found, _ := acl.GetRole(ctx, storage.ServiceBoardID, "chefin@firma.de"); !found || role != storage.RoleOwner {
			t.Fatalf("gültige Adresse muss Owner werden (found=%v role=%q)", found, role)
		}
	})
}
