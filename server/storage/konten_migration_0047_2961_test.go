package storage

import (
	"context"
	"testing"
)

// F-2961: Migration 0047 trennt den Zusatz " (unbestätigt)" von der Adresse
// und setzt das Flag; reine Adressen bleiben unberührt.
func TestMigration0047_ZusatzWirdFlag(t *testing.T) {
	// Zustand vor 0047: Schema bis 0046, Altwerte einsetzen.
	db := schemaBis(t, 46)
	ins := func(id, mail, gem string) {
		mustExec(t, db, `INSERT INTO konten (id, email, zustand, weg, gemeldete_email, angelegt, geaendert) VALUES (?, ?, 'pending', 'google', ?, 1, 1)`, id, mail, gem)
	}
	ins("k1", "a@example.org", "a@example.org (unbestätigt)")
	ins("k2", "b@example.org", "b@example.org")
	ins("k3", "c@example.org", " (unbestätigt)")
	if err := RunMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	check := func(id, want string, flag int) {
		t.Helper()
		var g string
		var f int
		if err := db.QueryRow(`SELECT gemeldete_email, gemeldete_email_unbestaetigt FROM konten WHERE id = ?`, id).Scan(&g, &f); err != nil {
			t.Fatal(err)
		}
		if g != want || f != flag {
			t.Errorf("%s: %q/%d, erwartet %q/%d", id, g, f, want, flag)
		}
	}
	check("k1", "a@example.org", 1)
	check("k2", "b@example.org", 0)
	check("k3", " (unbestätigt)", 0)
}
