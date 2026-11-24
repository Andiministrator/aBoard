package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestUserRepo_UpsertAndGet(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := NewUserRepo(db)

	// Unbekannte Email → ErrUserNotFound.
	if _, err := r.GetByEmail(ctx, "ghost@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("GetByEmail unbekannt: erwartet ErrUserNotFound, bekam %v", err)
	}

	// Erstanlage.
	u, err := r.Upsert(ctx, "id-1", "a@example.com", "Anna")
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if u.ID != "id-1" || u.Email != "a@example.com" || u.Name != "Anna" {
		t.Fatalf("Upsert-Ergebnis unerwartet: %+v", u)
	}
	if u.Created == "" {
		t.Fatalf("created nicht gesetzt")
	}

	// Zweiter Upsert mit anderer id + geändertem Namen: id bleibt stabil
	// (Konflikt auf email), Name wird aktualisiert.
	u2, err := r.Upsert(ctx, "id-2-anders", "a@example.com", "Anna Neu")
	if err != nil {
		t.Fatalf("zweiter Upsert: %v", err)
	}
	if u2.ID != "id-1" {
		t.Fatalf("id nicht stabil: %q (erwartet id-1)", u2.ID)
	}
	if u2.Name != "Anna Neu" {
		t.Fatalf("Name nicht aktualisiert: %q", u2.Name)
	}

	got, err := r.GetByEmail(ctx, "a@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if got.ID != "id-1" || got.Name != "Anna Neu" {
		t.Fatalf("GetByEmail unerwartet: %+v", got)
	}
}

// TestUserRepo_ListMitBoardZahlen prüft Sortierung + Board-Zahlen (Karte
// #575). Die Einzel-Owner-Definition selbst (kein anderer owner-Eintrag auf
// demselben Board) ist bereits über user_loeschung_test.go abgedeckt — hier
// geht es um die Aggregation über ALLE Nutzer und die Reihenfolge.
func TestUserRepo_ListMitBoardZahlen(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := NewUserRepo(db)

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-admin','admin@example.com','A','2026-01-01',1)`)
	mustExec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna','anna@example.com','Anna','2026-01-02',0)`)
	mustExec(`INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-carla','carla@example.com','Carla','2026-01-03',0)`)
	mustExec(`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('b-anna-eigen','Anna eigen','1','{}',1,1)`)
	mustExec(`INSERT INTO boards (id, title, version, state_json, updated_at, created_at) VALUES ('b-geteilt','Geteilt','1','{}',1,1)`)
	mustExec(`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b-anna-eigen','anna@example.com','owner','2026-01-01')`)
	mustExec(`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b-geteilt','anna@example.com','owner','2026-01-01')`)
	mustExec(`INSERT INTO acl (board_id, user_email, role, created) VALUES ('b-geteilt','carla@example.com','owner','2026-01-01')`)

	list, err := r.ListMitBoardZahlen(ctx)
	if err != nil {
		t.Fatalf("ListMitBoardZahlen: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("erwartet 3 Nutzer, bekam %d", len(list))
	}
	// Sortiert nach Email: admin, anna, carla.
	if list[0].Email != "admin@example.com" || list[1].Email != "anna@example.com" || list[2].Email != "carla@example.com" {
		t.Fatalf("Sortierung unerwartet: %+v", list)
	}
	admin, anna, carla := list[0], list[1], list[2]
	if !admin.IsAdmin || admin.BoardsOwner != 0 || admin.BoardsEinzelOwner != 0 {
		t.Fatalf("admin unerwartet: %+v", admin)
	}
	// Anna ist Owner von 2 Boards, aber nur bei b-anna-eigen die einzige.
	if anna.BoardsOwner != 2 || anna.BoardsEinzelOwner != 1 {
		t.Fatalf("anna unerwartet: %+v (erwartet owner=2 einzel=1)", anna)
	}
	// Carla ist auf b-geteilt Miteigentümerin, also nirgends einzige Owner.
	if carla.BoardsOwner != 1 || carla.BoardsEinzelOwner != 0 {
		t.Fatalf("carla unerwartet: %+v (erwartet owner=1 einzel=0)", carla)
	}
}

func TestUserRepo_UpsertLeer(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	r := NewUserRepo(db)

	if _, err := r.Upsert(ctx, "", "x@example.com", "X"); err == nil {
		t.Fatalf("Upsert mit leerer id: Fehler erwartet")
	}
	if _, err := r.Upsert(ctx, "id", "", "X"); err == nil {
		t.Fatalf("Upsert mit leerer email: Fehler erwartet")
	}
}
