package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// F-1442: Seite, Gesamtzahl und Obergrenze von ListAlleBoards.
func TestListAlleBoards_SeiteGesamtzahlObergrenze(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := MaxBoardSeite + 5
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := tx.Exec(`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES (?, 'T', '1', '{}', ?, 1)`, fmt.Sprintf("b%05d", i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	r := NewUserRepo(db)

	l, gesamt, err := r.ListAlleBoards(ctx, 10, 0)
	if err != nil || len(l) != 10 || gesamt != n {
		t.Fatalf("Seite: %d Einträge, gesamt %d, %v", len(l), gesamt, err)
	}
	if l[0].ID != fmt.Sprintf("b%05d", n-1) {
		t.Fatalf("jüngstes zuerst: %s", l[0].ID)
	}
	// limit 0 → Standard.
	if l, _, _ = r.ListAlleBoards(ctx, 0, 0); len(l) != StandardBoardSeite {
		t.Fatalf("Standard: %d", len(l))
	}
	// Obergrenze: auch ein riesiges limit liefert höchstens MaxBoardSeite.
	if l, _, _ = r.ListAlleBoards(ctx, 1<<30, 0); len(l) != MaxBoardSeite {
		t.Fatalf("Obergrenze: %d, erwartet %d", len(l), MaxBoardSeite)
	}
	// Rest hinter dem Offset.
	if l, _, _ = r.ListAlleBoards(ctx, MaxBoardSeite, MaxBoardSeite); len(l) != 5 {
		t.Fatalf("Rest: %d", len(l))
	}
}
