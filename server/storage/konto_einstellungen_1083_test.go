package storage

// Karte #1083: Speicher der Konto-Einstellungen. Testdaten nur example.org.

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func neueEinstellungenDB(t *testing.T) (*AnbieterBindungRepo, *KontoRepo, *SitzungRepo, func(q string, a ...any)) {
	t.Helper()
	db, err := Open(context.Background(), Config{DBPath: filepath.Join(t.TempDir(), "k.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	exec := func(q string, a ...any) {
		t.Helper()
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, e := range []string{"anna", "ben"} {
		exec(`INSERT INTO users (id, email, name, created, is_admin) VALUES (?, ?, '', '2026-01-01T00:00:00Z', 0)`, "u-"+e, e+"@example.org")
	}
	return NewAnbieterBindungRepo(db, nil), NewKontoRepo(db), NewSitzungRepo(db), exec
}

// Zwei Personen verknüpfen gleichzeitig dasselbe Anbieter-Konto: genau eine
// gewinnt, die andere bekommt ErrBindungBelegt.
func TestKontoEinstellungen1083_VerknuepfenGleichzeitig(t *testing.T) {
	b, _, _, _ := neueEinstellungenDB(t)
	var wg sync.WaitGroup
	fehler := make([]error, 2)
	for i, e := range []string{"anna@example.org", "ben@example.org"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, fehler[i] = b.Verknuepfen(context.Background(), e, "google", "https://iss.example.org", "sub-1")
		}()
	}
	wg.Wait()
	ok, belegt := 0, 0
	for _, e := range fehler {
		switch {
		case e == nil:
			ok++
		case errors.Is(e, ErrBindungBelegt):
			belegt++
		default:
			t.Errorf("unerwarteter Fehler: %v", e)
		}
	}
	if ok != 1 || belegt != 1 {
		t.Fatalf("ok=%d belegt=%d, erwartet je 1", ok, belegt)
	}
}

func TestKontoEinstellungen1083_LoesenRegeln(t *testing.T) {
	b, _, _, exec := neueEinstellungenDB(t)
	ctx := context.Background()
	if _, err := b.Verknuepfen(ctx, "anna@example.org", "google", "https://iss.example.org", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := b.Loesen(ctx, "anna@example.org", "google"); !errors.Is(err, ErrLetzterWeg) {
		t.Fatalf("letzter Weg ohne Passwort: %v", err)
	}
	if err := b.Loesen(ctx, "anna@example.org", "authelia"); !errors.Is(err, ErrBindungFehlt) {
		t.Fatalf("nicht verknüpft: %v", err)
	}
	if err := b.Loesen(ctx, "gibt-es-nicht@example.org", "google"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unbekannte Person: %v", err)
	}
	// Ein aktives Konto mit Passwort macht das Lösen frei; ein gesperrtes nicht.
	exec(`INSERT INTO konten (id, email, zustand, weg, passwort_hash, angelegt, geaendert) VALUES ('k1', 'anna@example.org', 'blocked', 'password', 'h', 1, 1)`)
	if err := b.Loesen(ctx, "anna@example.org", "google"); !errors.Is(err, ErrLetzterWeg) {
		t.Fatalf("gesperrtes Konto zählt nicht als Weg: %v", err)
	}
	exec(`UPDATE konten SET zustand = 'active' WHERE id = 'k1'`)
	if err := b.Loesen(ctx, "anna@example.org", "google"); err != nil {
		t.Fatalf("mit Passwort: %v", err)
	}
}

func TestKontoEinstellungen1083_Sitzungen(t *testing.T) {
	_, k, s, exec := neueEinstellungenDB(t)
	ctx := context.Background()
	exec(`INSERT INTO konten (id, email, zustand, weg, passwort_hash, totp_geheimnis, angelegt, geaendert) VALUES ('k1', 'anna@example.org', 'active', 'password', 'h', 'g', 1, 1)`)
	lauf, _, _ := s.Anlegen(ctx, "anna@example.org", "")
	andere, _, _ := s.Anlegen(ctx, "anna@example.org", "")
	ben, _, _ := s.Anlegen(ctx, "ben@example.org", "")
	if err := k.PasswortAendern(ctx, "k1", "neu", lauf, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pruefen(ctx, lauf); err != nil {
		t.Errorf("laufende Sitzung beendet: %v", err)
	}
	if _, err := s.Pruefen(ctx, andere); err == nil {
		t.Error("andere Sitzung gilt noch")
	}
	if _, err := s.Pruefen(ctx, ben); err != nil {
		t.Errorf("fremde Sitzung beendet: %v", err)
	}
	// Gesperrtes oder unbekanntes Konto: nichts passiert.
	exec(`UPDATE konten SET zustand = 'blocked' WHERE id = 'k1'`)
	if err := k.PasswortAendern(ctx, "k1", "x", lauf, 6); !errors.Is(err, ErrKontoUnbekannt) {
		t.Errorf("gesperrtes Konto: %v", err)
	}
	if err := k.TOTPNeu(ctx, "k1", "v", []string{string(make([]byte, 64))}, 3, lauf, 6); !errors.Is(err, ErrKontoUnbekannt) {
		t.Errorf("TOTPNeu gesperrt: %v", err)
	}
	if err := k.CodesErsetzen(ctx, "k1", []string{"x"}, 6); !errors.Is(err, ErrKontoUnbekannt) {
		t.Errorf("CodesErsetzen gesperrt: %v", err)
	}
}

// Ersetzen, Lösen und das Lösen durch den Betreiber beenden alle Sitzungen der
// Person in derselben Transaktion (Sicherheitsprüfung #1083); ein bloßes
// Verknüpfen lässt sie bestehen.
func TestKontoEinstellungen1083_WegeBeendenSitzungen(t *testing.T) {
	b, _, s, exec := neueEinstellungenDB(t)
	ctx := context.Background()
	exec(`INSERT INTO konten (id, email, zustand, weg, passwort_hash, angelegt, geaendert) VALUES ('k1', 'anna@example.org', 'active', 'password', 'h', 1, 1)`)
	ist := func(id string) bool { _, err := s.Pruefen(ctx, id); return err == nil }
	neu := func() string { id, _, _ := s.Anlegen(ctx, "anna@example.org", ""); return id }

	a := neu()
	if ersetzt, err := b.Verknuepfen(ctx, "anna@example.org", "google", "https://iss.example.org", "s1"); err != nil || ersetzt || !ist(a) {
		t.Fatalf("erstes Verknüpfen: ersetzt=%v err=%v sitzung=%v", ersetzt, err, ist(a))
	}
	if ersetzt, err := b.Verknuepfen(ctx, "anna@example.org", "google", "https://iss.example.org", "s2"); err != nil || !ersetzt || ist(a) {
		t.Fatalf("Ersetzen: ersetzt=%v err=%v sitzung=%v", ersetzt, err, ist(a))
	}
	a = neu()
	if err := b.Loesen(ctx, "anna@example.org", "google"); err != nil || ist(a) {
		t.Fatalf("Lösen: %v sitzung=%v", err, ist(a))
	}
	if _, err := b.Verknuepfen(ctx, "anna@example.org", "google", "https://iss.example.org", "s3"); err != nil {
		t.Fatal(err)
	}
	a = neu()
	uid, err := b.LoesenOhnePruefung(ctx, "anna@example.org", "google")
	if err != nil || uid != "u-anna" || ist(a) {
		t.Fatalf("Betreiber-Lösen: uid=%q err=%v sitzung=%v", uid, err, ist(a))
	}
	// Fehlschlag lässt Sitzungen in Ruhe.
	a = neu()
	if _, err := b.LoesenOhnePruefung(ctx, "anna@example.org", "google"); !errors.Is(err, ErrBindungFehlt) || !ist(a) {
		t.Fatalf("Fehlschlag: %v sitzung=%v", err, ist(a))
	}
}
