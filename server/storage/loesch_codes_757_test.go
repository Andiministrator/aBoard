package storage

// Karte #757: Geheimnis und Verbrauchsliste der Bestätigungscodes.

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLoeschCodes757(t *testing.T) {
	ctx := context.Background()
	pfad := filepath.Join(t.TempDir(), "aboard.db")
	db, err := Open(ctx, Config{DBPath: pfad})
	if err != nil {
		t.Fatal(err)
	}
	r := NewLoeschCodeRepo(db)
	g1, err := r.Geheimnis(ctx)
	if err != nil || len(g1) < 32 {
		t.Fatalf("Geheimnis: %v %d", err, len(g1))
	}
	// Eine zweite Instanz (eigenes Repo) liest dasselbe Geheimnis.
	if g2, err := NewLoeschCodeRepo(db).Geheimnis(ctx); err != nil || !bytes.Equal(g1, g2) {
		t.Fatalf("zweite Instanz: %v", err)
	}
	_ = db.Close()
	// Neustart: dasselbe Geheimnis.
	db, err = Open(ctx, Config{DBPath: pfad})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r = NewLoeschCodeRepo(db)
	if g3, err := r.Geheimnis(ctx); err != nil || !bytes.Equal(g1, g3) {
		t.Fatalf("nach Neustart: %v", err)
	}

	ablauf := time.Now().Add(10 * time.Minute)
	if err := r.Verbrauche(ctx, "z1", ablauf); err != nil {
		t.Fatal(err)
	}
	if err := r.Verbrauche(ctx, "z1", ablauf); !errors.Is(err, ErrLoeschCodeVerbraucht) {
		t.Fatalf("zweimal: %v", err)
	}
	if err := r.Freigeben(ctx, "z1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Verbrauche(ctx, "z1", ablauf); err != nil {
		t.Fatalf("nach Freigeben: %v", err)
	}
	// Abgelaufene Einträge räumt die nächste Annahme ab.
	if err := r.Verbrauche(ctx, "alt", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := r.Verbrauche(ctx, "z2", ablauf); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM loesch_codes_verbraucht WHERE zufall = 'alt'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("abgelaufener Eintrag: %d %v", n, err)
	}
}
