package storage_test

// Karte #1075: 2FA-Speicher im KontoRepo. Testdaten nur example.org.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestKontoRepo1075_TOTP(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-anna', 'anna@example.org', 'active', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	r := storage.NewKontoRepo(db)
	h := func(c string) string { return strings.Repeat(c, 64) }

	if g, _, err := r.TOTPGeheimnis(ctx, "k-anna"); err != nil || g != "" {
		t.Fatalf("vorher: %q %v", g, err)
	}
	if ok, _ := r.TOTPSchrittVerbrauchen(ctx, "k-anna", 5); ok {
		t.Fatal("Schritt ohne Geheimnis angenommen")
	}
	if err := r.TOTPEinrichten(ctx, "k-anna", "versiegelt-1", []string{h("a"), h("b")}, 7); err != nil {
		t.Fatal(err)
	}
	if k, _ := r.FuerAnmeldung(ctx, "anna@example.org"); !k.HatTOTP {
		t.Fatal("HatTOTP fehlt")
	}
	for _, f := range []struct {
		schritt int64
		ok      bool
	}{{100, true}, {100, false}, {99, false}, {101, true}} {
		if ok, err := r.TOTPSchrittVerbrauchen(ctx, "k-anna", f.schritt); ok != f.ok || err != nil {
			t.Errorf("Schritt %d: %v %v", f.schritt, ok, err)
		}
	}
	if g, letzter, _ := r.TOTPGeheimnis(ctx, "k-anna"); g != "versiegelt-1" || letzter != 101 {
		t.Fatalf("%q %d", g, letzter)
	}
	if ok, _ := r.CodeVerbrauchen(ctx, "k-anna", h("a"), 9); !ok {
		t.Fatal("Code a")
	}
	if ok, _ := r.CodeVerbrauchen(ctx, "k-anna", h("a"), 9); ok {
		t.Fatal("Code a zweimal")
	}
	if ok, _ := r.CodeVerbrauchen(ctx, "k-anna", h("c"), 9); ok {
		t.Fatal("unbekannter Code")
	}
	if ok, _ := r.CodeVerbrauchen(ctx, "k-anna", "", 9); ok {
		t.Fatal("leerer Hash")
	}
	if n, _ := r.OffeneCodes(ctx, "k-anna"); n != 1 {
		t.Fatalf("offen %d", n)
	}
	// Neu einrichten ersetzt Geheimnis und alle Codes; der Schritt beginnt bei 0.
	if err := r.TOTPEinrichten(ctx, "k-anna", "versiegelt-2", []string{h("d")}, 8); err != nil {
		t.Fatal(err)
	}
	if g, letzter, _ := r.TOTPGeheimnis(ctx, "k-anna"); g != "versiegelt-2" || letzter != 0 {
		t.Fatalf("neu: %q %d", g, letzter)
	}
	if ok, _ := r.CodeVerbrauchen(ctx, "k-anna", h("b"), 9); ok {
		t.Fatal("alter Code gilt nach Neueinrichtung")
	}
	if n, _ := r.OffeneCodes(ctx, "k-anna"); n != 1 {
		t.Fatalf("offen nach neu %d", n)
	}
	// Gleichzeitig derselbe Schritt: genau einer gewinnt.
	var wg sync.WaitGroup
	var mu sync.Mutex
	gewonnen := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := r.TOTPSchrittVerbrauchen(ctx, "k-anna", 500); ok && err == nil {
				mu.Lock()
				gewonnen++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if gewonnen != 1 {
		t.Fatalf("%d Gewinner", gewonnen)
	}
	if err := r.TOTPEinrichten(ctx, "k-niemand", "x", []string{h("e")}, 1); !errors.Is(err, storage.ErrKontoUnbekannt) {
		t.Errorf("unbekanntes Konto: %v", err)
	}
	if err := r.TOTPEinrichten(ctx, "k-anna", "", []string{h("e")}, 1); err == nil {
		t.Error("leeres Geheimnis angenommen")
	}
}

// F-2891: Passwort setzen und 2FA neu einrichten beenden alle Sitzungen der
// Person, fremde bleiben.
func TestKontoRepo1092_SitzungenEnden(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-anna', 'anna@example.org', 'active', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	s := storage.NewSitzungRepo(db)
	r := storage.NewKontoRepo(db)
	zaehle := func(email string) int {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM sitzungen WHERE email = ?`, email).Scan(&n)
		return n
	}
	for _, schritt := range []func() error{
		func() error { return r.SetzePasswortHash(ctx, "k-anna", "$argon2id$x", 2) },
		func() error {
			return r.TOTPEinrichten(ctx, "k-anna", "versiegelt", []string{strings.Repeat("a", 64)}, 3)
		},
	} {
		for _, e := range []string{"anna@example.org", "anna@example.org", "ben@example.org"} {
			if _, _, err := s.Anlegen(ctx, e, ""); err != nil {
				t.Fatal(err)
			}
		}
		if err := schritt(); err != nil {
			t.Fatal(err)
		}
		if zaehle("anna@example.org") != 0 || zaehle("ben@example.org") == 0 {
			t.Fatalf("anna %d, ben %d", zaehle("anna@example.org"), zaehle("ben@example.org"))
		}
	}
}
