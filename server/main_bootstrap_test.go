package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Kritiker S-11: meldeBootstrapStand — Admin nachholen (F-1855) und Hinweis
// auf Boards ohne Owner nur im Mehrnutzer-Betrieb (F-1854).
func TestMeldeBootstrapStand(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Setenv(storage.BootstrapOwnerEnv, "")
	if _, err := storage.NewBootstrapRepo(db).BootstrapNewUser(ctx, "u1", "b1", "a@x.de", "A"); err != nil {
		t.Fatal(err)
	}
	// a@x.de ist ohne Variable erste Person → Admin; zurücksetzen, um das
	// Nachholen zu prüfen.
	if _, err := db.Exec(`UPDATE users SET is_admin = 0`); err != nil {
		t.Fatal(err)
	}

	// Ein Board ohne Owner gibt es schon VOR dem Einzelbetrieb-Aufruf — sonst
	// trennt dessen Prüfung nicht (Verify W-1: b1 hat über den Bootstrap einen
	// Owner, ohne „alt“ wäre „kein Hinweis“ immer wahr).
	if _, err := storage.NewBoardRepo(db).Insert(ctx, "alt", "alt", "2", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	var aus bytes.Buffer
	log.SetOutput(&aus)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// Einzelbetrieb: kein Hinweis, obwohl „alt“ keinen Owner hat.
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "")
	t.Setenv(storage.BootstrapOwnerEnv, "a@x.de")
	meldeBootstrapStand(ctx, db)
	u, _ := storage.NewUserRepo(db).GetByEmail(ctx, "a@x.de")
	if !u.IsAdmin {
		t.Fatal("meldeBootstrapStand muss den Admin nachholen")
	}
	if strings.Contains(aus.String(), "ohne Owner") {
		t.Fatalf("Einzelbetrieb darf keinen Owner-Hinweis loggen:\n%s", aus.String())
	}

	// Mehrnutzer-Betrieb mit einem Board ohne Owner → Hinweis.
	aus.Reset()
	t.Setenv("ABOARD_TRUST_FORWARD_AUTH", "1")
	meldeBootstrapStand(ctx, db)
	if !strings.Contains(aus.String(), "1 Board(s) ohne Owner") {
		t.Fatalf("Mehrnutzer-Betrieb: Hinweis fehlt:\n%s", aus.String())
	}

	// Verify W-4: gesetzt, ungültig, kein Admin → FEHLER-Zeile beim Start.
	if _, err := db.Exec(`UPDATE users SET is_admin = 0`); err != nil {
		t.Fatal(err)
	}
	aus.Reset()
	t.Setenv(storage.BootstrapOwnerEnv, "tippfehler")
	meldeBootstrapStand(ctx, db)
	if !strings.Contains(aus.String(), "keine Personenadresse und es gibt keinen Admin") {
		t.Fatalf("ungültige Variable ohne Admin: Fehlerzeile fehlt:\n%s", aus.String())
	}
}
