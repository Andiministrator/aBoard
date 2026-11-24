package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// F-1854: Alt-Boards nach dem Umstieg auf den Mehrnutzer-Betrieb ausdrücklich
// zuteilen — orphans listet, adopt teilt zu, set setzt einzeln.
func TestRunACL_OrphansAdoptSet(t *testing.T) {
	ctx := context.Background()
	dbPfad := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPfad})
	if err != nil {
		t.Fatal(err)
	}
	boards, acl := storage.NewBoardRepo(db), storage.NewAclRepo(db)
	for _, id := range []string{"alt-1", "alt-2", "neu"} {
		if _, err := boards.Insert(ctx, id, id, "2", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := acl.Set(ctx, "neu", "b@x.de", storage.RoleOwner); err != nil {
		t.Fatal(err)
	}
	db.Close()

	lauf := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runACLMit(ctx, append(args, "--db", dbPfad), &out)
		return out.String(), err
	}
	// flag-Reihenfolge: Unterbefehl zuerst, dann Optionen.
	lauf2 := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runACLMit(ctx, append([]string{args[0], "--db", dbPfad}, args[1:]...), &out)
		return out.String(), err
	}

	aus, err := lauf("orphans")
	if err != nil || !strings.Contains(aus, "alt-1") || !strings.Contains(aus, "alt-2") || strings.Contains(aus, "neu\t") ||
		!strings.Contains(aus, "2 Board(s) ohne Owner") {
		t.Fatalf("orphans: %v\n%s", err, aus)
	}
	if _, err := lauf2("adopt", "--email", "@x.de"); err == nil {
		t.Fatal("adopt mit Domain-Schlüssel muss scheitern")
	}
	if aus, err := lauf2("adopt", "--email", "Andi@X.de"); err != nil || !strings.Contains(aus, "2 Board(s) zugeteilt") {
		t.Fatalf("adopt: %v\n%s", err, aus)
	}
	if aus, _ := lauf("orphans"); !strings.Contains(aus, "0 Board(s) ohne Owner") {
		t.Fatalf("nach adopt noch Boards ohne Owner:\n%s", aus)
	}
	if _, err := lauf2("set", "--board", "alt-1", "--email", "c@x.de", "--role", "chef"); err == nil {
		t.Fatal("set mit unbekannter Rolle muss scheitern")
	}
	if _, err := lauf2("set", "--board", "alt-1", "--email", "c@x.de", "--role", "viewer"); err != nil {
		t.Fatalf("set: %v", err)
	}
	db, _ = storage.Open(ctx, storage.Config{DBPath: dbPfad})
	defer db.Close()
	acl = storage.NewAclRepo(db)
	if r, ok, _ := acl.GetRole(ctx, "alt-1", "andi@x.de"); !ok || r != storage.RoleOwner {
		t.Fatalf("andi@x.de nicht Owner von alt-1: %q %v", r, ok)
	}
	if r, ok, _ := acl.GetRole(ctx, "neu", "andi@x.de"); ok {
		t.Fatalf("adopt darf Boards MIT Owner nicht anfassen (neu: %q)", r)
	}
	if r, ok, _ := acl.GetRole(ctx, "alt-1", "c@x.de"); !ok || r != storage.RoleViewer {
		t.Fatalf("set viewer: %q %v", r, ok)
	}
}

// Kritiker S-8: falscher --db-Pfad legt keine leere Datenbank an.
func TestRunACL_FehlendeDatenbank(t *testing.T) {
	pfad := filepath.Join(t.TempDir(), "gibt-es-nicht.db")
	err := runACLMit(context.Background(), []string{"orphans", "--db", pfad}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "nicht gefunden") {
		t.Fatalf("erwartet „nicht gefunden“, bekam %v", err)
	}
	if _, serr := os.Stat(pfad); serr == nil {
		t.Fatal("acl darf keine neue Datenbank anlegen")
	}
}

// Kritiker S-9: den einzigen Owner nicht herabstufen; mit zweitem Owner geht es.
func TestRunACL_LetzterOwnerBleibt(t *testing.T) {
	ctx := context.Background()
	dbPfad := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPfad})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.NewBoardRepo(db).Insert(ctx, "b", "b", "2", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := storage.NewAclRepo(db).Set(ctx, "b", "a@x.de", storage.RoleOwner); err != nil {
		t.Fatal(err)
	}
	db.Close()
	set := func(email, rolle string) error {
		return runACLMit(ctx, []string{"set", "--db", dbPfad, "--board", "b", "--email", email, "--role", rolle}, &bytes.Buffer{})
	}
	if err := set("a@x.de", storage.RoleViewer); err == nil || !strings.Contains(err.Error(), "einzige Owner") {
		t.Fatalf("Herabstufen des einzigen Owners: %v", err)
	}
	if err := set("b@x.de", storage.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := set("a@x.de", storage.RoleViewer); err != nil {
		t.Fatalf("mit zweitem Owner muss es gehen: %v", err)
	}
}
