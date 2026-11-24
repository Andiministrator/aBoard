package storage_test

// Karte #1076: Einladungs-Links (ADR-0149 §5.3, §7). Testdaten nur example.org.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

type einladungUhr struct{ t time.Time }

func (u *einladungUhr) jetzt() time.Time { return u.t }

func einladungDB(t *testing.T) (*sql.DB, *storage.EinladungRepo, *einladungUhr) {
	t.Helper()
	t.Setenv(uhr.PruefungEnvVar, "aus")
	db, err := storage.Open(context.Background(), storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	u := &einladungUhr{t: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	return db, storage.NewEinladungRepo(db).MitUhr(u.jetzt), u
}

func TestEinladungsAdresse1076(t *testing.T) {
	for e, want := range map[string]string{
		" Anna@Example.org ": "anna@example.org",
		"a.b+c@example.org":  "a.b+c@example.org",
	} {
		if got, err := storage.EinladungsAdresse(e); err != nil || got != want {
			t.Errorf("%q: %q %v", e, got, err)
		}
	}
	for _, e := range []string{"", "anna", "@example.org", "anna@", "a@b@example.org", "Anna <anna@example.org>",
		"anna@example.org, ben@example.org", "änna@example.org", "anna@example.org\r\nBcc: x@example.org", "anna @example.org"} {
		if _, err := storage.EinladungsAdresse(e); !errors.Is(err, storage.ErrEinladungAdresse) {
			t.Errorf("%q angenommen", e)
		}
	}
}

func TestEinladung1076_EinladenPruefenErneut(t *testing.T) {
	ctx := context.Background()
	db, r, u := einladungDB(t)
	e, err := r.Einladen(ctx, "Anna@Example.org")
	if err != nil || e.Email != "anna@example.org" || len(e.Token) != 43 || e.Erneut || !e.Ablauf.Equal(u.t.Add(storage.EinladungGueltig)) {
		t.Fatalf("%+v %v", e, err)
	}
	// Gespeichert ist nur der Hash, nie der Klartext; keine users-Zeile.
	var hash string
	if err := db.QueryRow(`SELECT token_hash FROM einladungen WHERE konto_id = ?`, e.KontoID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256([]byte(e.Token))
	if hash != hex.EncodeToString(s[:]) || hash == e.Token {
		t.Fatal("kein Hash gespeichert")
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Fatal("users-Zeile durch Einladung")
	}
	if z, _ := storage.NewKontoRepo(db).Zustand(ctx, "anna@example.org"); z != storage.KontoEingeladen {
		t.Fatalf("Zustand %q", z)
	}
	o, err := r.Pruefen(ctx, e.Token)
	if err != nil || o.KontoID != e.KontoID || o.Email != "anna@example.org" || o.Zweck != storage.EinladungZweckInvite || o.Zustand != storage.KontoEingeladen {
		t.Fatalf("Pruefen: %+v %v", o, err)
	}
	for _, falsch := range []string{"", e.Token[:42], e.Token + "A", hash} {
		if _, err := r.Pruefen(ctx, falsch); !errors.Is(err, storage.ErrEinladungUnbekannt) {
			t.Errorf("%q angenommen", falsch)
		}
	}
	// Erneut einladen: neuer Link, der alte gilt nicht mehr, dasselbe Konto.
	u.t = u.t.Add(time.Hour)
	e2, err := r.Einladen(ctx, "anna@example.org")
	if err != nil || !e2.Erneut || e2.KontoID != e.KontoID || e2.Token == e.Token {
		t.Fatalf("erneut: %+v %v", e2, err)
	}
	if _, err := r.Pruefen(ctx, e.Token); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Fatal("alter Link gilt nach erneuter Einladung")
	}
	if _, err := r.Pruefen(ctx, e2.Token); err != nil {
		t.Fatalf("neuer Link: %v", err)
	}
	// Nach 7 Tagen abgelaufen.
	u.t = u.t.Add(storage.EinladungGueltig)
	if _, err := r.Pruefen(ctx, e2.Token); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Fatal("abgelaufener Link gilt")
	}
	l, err := r.Uebersicht(ctx)
	if err != nil || len(l) != 1 || !l[0].LinkAbgelaufen || l[0].Zustand != storage.KontoEingeladen || l[0].HatPasswort {
		t.Fatalf("Liste: %+v %v", l, err)
	}
	// Ein benutzter Link gilt nicht (eingeloest setzt #1077; hier direkt).
	e3, _ := r.Einladen(ctx, "anna@example.org")
	if _, err := db.Exec(`UPDATE einladungen SET eingeloest = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pruefen(ctx, e3.Token); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Fatal("benutzter Link gilt")
	}
}

func TestEinladung1076_Grenzen(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	if _, err := db.Exec(`INSERT INTO users (id, email, name, created) VALUES ('u-ben', 'Ben@example.org', 'Ben', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einladen(ctx, "ben@example.org"); !errors.Is(err, storage.ErrEinladungHatZugang) {
		t.Fatalf("vorhandene Person: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-dora', 'dora@example.org', 'pending', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einladen(ctx, "dora@example.org"); !errors.Is(err, storage.ErrEinladungKontoWeiter) {
		t.Fatalf("eingelöstes Konto: %v", err)
	}
	if _, err := r.Einladen(ctx, "@example.org"); !errors.Is(err, storage.ErrEinladungAdresse) {
		t.Fatalf("Domain: %v", err)
	}
	// Zurückziehen: nur invited; Links gehen mit.
	e, _ := r.Einladen(ctx, "emil@example.org")
	if email, err := r.Zurueckziehen(ctx, e.KontoID); err != nil || email != "emil@example.org" {
		t.Fatalf("zurückziehen: %q %v", email, err)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM einladungen WHERE konto_id = ?`, e.KontoID).Scan(&n)
	if n != 0 {
		t.Fatal("Links blieben stehen")
	}
	if _, err := r.Pruefen(ctx, e.Token); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Fatal("Link nach Zurückziehen gültig")
	}
	if _, err := r.Zurueckziehen(ctx, e.KontoID); !errors.Is(err, storage.ErrKontoUnbekannt) {
		t.Fatalf("zweimal: %v", err)
	}
	if _, err := r.Zurueckziehen(ctx, "k-dora"); !errors.Is(err, storage.ErrEinladungKontoWeiter) {
		t.Fatalf("pending: %v", err)
	}
}

func TestEinladung1076_Abraeumen(t *testing.T) {
	ctx := context.Background()
	db, r, u := einladungDB(t)
	alt, _ := r.Einladen(ctx, "anna@example.org")
	if _, err := db.Exec(`INSERT INTO konto_codes (konto_id, code_hash, angelegt, verbraucht) VALUES
		(?, ?, 1, ?), (?, ?, 1, NULL)`, alt.KontoID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", u.t.Unix(),
		alt.KontoID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err != nil {
		t.Fatal(err)
	}
	// Innerhalb der Aufbewahrung bleibt alles stehen.
	u.t = u.t.Add(storage.EinladungGueltig + time.Hour)
	if n, err := r.AbgelaufeneAbraeumen(ctx); n != 0 || err != nil {
		t.Fatalf("zu früh: %d %v", n, err)
	}
	u.t = u.t.Add(storage.EinladungAufbewahrung)
	if n, err := r.AbgelaufeneAbraeumen(ctx); n != 2 || err != nil {
		t.Fatalf("abräumen: %d %v", n, err)
	}
	var codes, konten int
	_ = db.QueryRow(`SELECT COUNT(*) FROM konto_codes`).Scan(&codes)
	_ = db.QueryRow(`SELECT COUNT(*) FROM konten`).Scan(&konten)
	if codes != 1 || konten != 1 {
		t.Fatalf("offener Code oder Konto mitgelöscht: codes %d, konten %d", codes, konten)
	}
}

// F-2892: Gleichzeitiges Einladen derselben neuen Adresse endet nie mit
// einem Fehler; genau ein Konto entsteht.
func TestEinladung1092_Gleichzeitig(t *testing.T) {
	// Verify N3: zehn Runden à 16 Goroutinen — mit DEFERRED-Transaktion war
	// schon ein Drittel der Einzelläufe rot; so trennt der Test sicher.
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	for runde := 0; runde < 10; runde++ {
		email := fmt.Sprintf("gleich%d@example.org", runde)
		var wg sync.WaitGroup
		fehler := make(chan error, 16)
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := r.Einladen(ctx, email); err != nil {
					fehler <- err
				}
			}()
		}
		wg.Wait()
		close(fehler)
		for err := range fehler {
			t.Fatalf("Runde %d: %v", runde, err)
		}
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM konten WHERE email = ?`, email).Scan(&n)
		if n != 1 {
			t.Fatalf("Runde %d: %d Konten", runde, n)
		}
	}
}
