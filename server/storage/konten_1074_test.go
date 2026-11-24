package storage_test

// Karte #1074: KontoRepo.FuerAnmeldung und SetzePasswortHash. Testdaten nur
// example.org.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestKontoRepo1074_FuerAnmeldung(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES
		('k-anna', 'anna@example.org', 'active', 1, 1),
		('k-ben', 'ben@example.org', 'pending', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	r := storage.NewKontoRepo(db)

	k, err := r.FuerAnmeldung(ctx, " Anna@Example.org ")
	if err != nil || k.ID != "k-anna" || k.Email != "anna@example.org" || k.Zustand != storage.KontoAktiv || k.PasswortHash != "" || k.HatTOTP {
		t.Fatalf("ohne Passwort: %+v %v", k, err)
	}
	if err := r.SetzePasswortHash(ctx, "k-anna", "$argon2id$x", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE konten SET totp_geheimnis = 'versiegelt' WHERE id = 'k-anna'`); err != nil {
		t.Fatal(err)
	}
	k, err = r.FuerAnmeldung(ctx, "anna@example.org")
	if err != nil || k.PasswortHash != "$argon2id$x" || !k.HatTOTP {
		t.Fatalf("mit Passwort: %+v %v", k, err)
	}
	var geaendert int64
	if err := db.QueryRow(`SELECT geaendert FROM konten WHERE id = 'k-anna'`).Scan(&geaendert); err != nil || geaendert != 5 {
		t.Fatalf("geaendert %d %v", geaendert, err)
	}

	for _, email := range []string{"niemand@example.org", "", "@example.org"} {
		if _, err := r.FuerAnmeldung(ctx, email); !errors.Is(err, storage.ErrKontoUnbekannt) {
			t.Errorf("%q: %v", email, err)
		}
	}
	if err := r.SetzePasswortHash(ctx, "k-niemand", "$argon2id$x", 5); !errors.Is(err, storage.ErrKontoUnbekannt) {
		t.Errorf("unbekanntes Konto: %v", err)
	}
	if err := r.SetzePasswortHash(ctx, "k-ben", "", 5); err == nil {
		t.Error("leerer Hash angenommen")
	}
}
