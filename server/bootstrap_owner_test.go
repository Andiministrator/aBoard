package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// openTestRepos öffnet eine frische Test-DB und liefert Board- + ACL-Repo.
func openTestRepos(t *testing.T) (context.Context, *storage.BoardRepo, *storage.AclRepo) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return ctx, storage.NewBoardRepo(db), storage.NewAclRepo(db)
}

// TestF263_BootstrapOwner_Normalisiert — `  OWNER@x.de ` muss als
// normalisierter owner-ACL-Eintrag (`owner@x.de`) auf `default` landen.
func TestF263_BootstrapOwner_Normalisiert(t *testing.T) {
	t.Setenv("ABOARD_BOOTSTRAP_OWNER", "  OWNER@x.de ")
	ctx, repo, aclRepo := openTestRepos(t)

	if err := seedDefaultBoard(ctx, repo, aclRepo); err != nil {
		t.Fatalf("seedDefaultBoard: %v", err)
	}

	role, found, err := aclRepo.GetRole(ctx, "default", "owner@x.de")
	if err != nil || !found || role != storage.RoleOwner {
		t.Fatalf("Bootstrap-Owner-ACL = role=%q found=%v err=%v, erwartet owner/true (F-263 normalisiert)", role, found, err)
	}
	// Auch der Lookup mit der Original-(unnormalisierten)-Form trifft jetzt.
	if _, found2, _ := aclRepo.GetRole(ctx, "default", "  OWNER@x.de "); !found2 {
		t.Fatalf("Lookup mit unnormalisierter Form trifft nicht (F-260/263)")
	}
}

// TestF263_BootstrapOwner_Kaputt_KeinEintrag — ein Wert ohne '@' darf NIE
// still einen unbrauchbaren ACL-Eintrag erzeugen. Server darf NICHT crashen.
func TestF263_BootstrapOwner_Kaputt_KeinEintrag(t *testing.T) {
	t.Setenv("ABOARD_BOOTSTRAP_OWNER", "kaputt")
	ctx, repo, aclRepo := openTestRepos(t)

	// Kein Crash / kein Fehler aus seedDefaultBoard (Tippfehler ≠ Start-Abbruch).
	if err := seedDefaultBoard(ctx, repo, aclRepo); err != nil {
		t.Fatalf("seedDefaultBoard mit kaputtem Owner = %v, erwartet nil (kein Crash, F-263)", err)
	}
	// Aber: KEIN ACL-Eintrag (weder 'kaputt' noch sonst etwas auf default).
	entries, err := aclRepo.ListByBoard(ctx, "default")
	if err != nil {
		t.Fatalf("ListByBoard: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ACL-Einträge bei kaputtem Bootstrap-Owner = %d, erwartet 0 (kein unbrauchbarer Eintrag)", len(entries))
	}
	// Das Board selbst wurde dennoch angelegt (Bootstrap-Tippfehler blockt nicht).
	if _, gerr := repo.Get(ctx, "default"); gerr != nil {
		t.Fatalf("Default-Board fehlt trotz kaputtem Owner: %v", gerr)
	}
}

// TestF263_BootstrapOwner_NurWhitespace_KeinEintrag.
func TestF263_BootstrapOwner_NurWhitespace_KeinEintrag(t *testing.T) {
	t.Setenv("ABOARD_BOOTSTRAP_OWNER", "    ")
	ctx, repo, aclRepo := openTestRepos(t)

	if err := seedDefaultBoard(ctx, repo, aclRepo); err != nil {
		t.Fatalf("seedDefaultBoard = %v, erwartet nil", err)
	}
	entries, err := aclRepo.ListByBoard(ctx, "default")
	if err != nil {
		t.Fatalf("ListByBoard: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ACL-Einträge bei Whitespace-Owner = %d, erwartet 0", len(entries))
	}
}

// TestF263_BootstrapOwner_Gueltig_GenauEinEintrag — Standardfall: gültige
// Adresse → genau ein owner-Eintrag.
func TestF263_BootstrapOwner_Gueltig_GenauEinEintrag(t *testing.T) {
	t.Setenv("ABOARD_BOOTSTRAP_OWNER", "admin@firma.de")
	ctx, repo, aclRepo := openTestRepos(t)

	if err := seedDefaultBoard(ctx, repo, aclRepo); err != nil {
		t.Fatalf("seedDefaultBoard: %v", err)
	}
	entries, err := aclRepo.ListByBoard(ctx, "default")
	if err != nil {
		t.Fatalf("ListByBoard: %v", err)
	}
	if len(entries) != 1 || entries[0].UserEmail != "admin@firma.de" || entries[0].Role != storage.RoleOwner {
		t.Fatalf("ACL-Einträge = %+v, erwartet genau [admin@firma.de owner]", entries)
	}
}
