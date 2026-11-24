package storage

// Karte #1069 (ADR-0149 §4, §7): Migration 0046 und der Weg durch
// AnbieterBindungRepo. Testdaten nur mit example-Adressen.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const googleIss = "https://accounts.google.com"

// schema45 legt eine Datenbank im Stand von Migration 0045 an (Form wie auf
// pm vor dem Update) und liefert sie offen zurück.
func schema45(t *testing.T) *sql.DB {
	t.Helper()
	return schemaBis(t, 45)
}

// schemaBis legt eine Datenbank im Stand von Migration bis (einschließlich)
// an. Migrationstests setzen damit genau den Stand vor ihrer Migration auf,
// statt alle Migrationen laufen zu lassen und die Version zurückzudrehen —
// das scheitert, sobald eine spätere Migration nicht wiederholbar ist
// (Karte #1197: 0048 legt eine Spalte an).
func schemaBis(t *testing.T, bis int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsnFuer(filepath.Join(t.TempDir(), "a.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	alle, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range alle {
		if m.version > bis {
			break
		}
		if _, err := db.Exec(m.sql); err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
	}
	mustExec(t, db, fmt.Sprintf("PRAGMA user_version = %d", bis))
	return db
}

// Gegenprobe auf der pm-Struktur: Die Bindung einer Person mit users-Zeile
// bleibt erhalten (Anbieter standard, user_id), die Schreibweise der Adresse
// spielt keine Rolle, eine Bindung ohne users-Zeile entfällt, und
// oidc_bindungen ist weg. Neue Tabellen sind leer: Bestandspersonen bekommen
// kein Konto (ADR-0149 §5.6).
func TestMigration0046_UebernimmtBindungen(t *testing.T) {
	ctx := context.Background()
	db := schema45(t)
	mustExec(t, db, `INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-anna', 'Anna@Example.com', 'Anna', '2026-01-01', 1)`)
	mustExec(t, db, `INSERT INTO users (id, email, name, created, is_admin) VALUES ('u-bert', 'bert@example.com', 'Bert', '2026-01-01', 0)`)
	mustExec(t, db, `INSERT INTO oidc_bindungen (email, iss, sub, angelegt) VALUES ('anna@example.com', ?, '42', 100)`, googleIss)
	mustExec(t, db, `INSERT INTO oidc_bindungen (email, iss, sub, angelegt) VALUES ('waise@example.com', ?, '77', 200)`, googleIss)
	mustExec(t, db, `INSERT INTO sitzungen (id_hash, email, angelegt, zuletzt, ablauf) VALUES (?, 'anna@example.com', 1, 1, 9999999999)`, strings.Repeat("a", 64))

	if err := RunMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v < 46 {
		t.Fatalf("user_version %d (%v)", v, err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'oidc_bindungen'`); n != 0 {
		t.Errorf("oidc_bindungen besteht noch")
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen WHERE iss = ? AND sub = '42' AND anbieter = 'standard' AND user_id = 'u-anna' AND konto_id IS NULL AND angelegt = 100`, googleIss); n != 1 {
		t.Errorf("Bindung von Anna nicht übernommen")
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen`); n != 1 {
		t.Errorf("anbieter_bindungen: %d Zeilen, erwartet 1 (die Waise entfällt)", n)
	}
	for _, tab := range []string{"konten", "einladungen", "konto_codes"} {
		if n := zaehle(t, db, `SELECT COUNT(*) FROM `+tab); n != 0 {
			t.Errorf("%s: %d Zeilen nach der Migration, erwartet 0", tab, n)
		}
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM sitzungen`); n != 1 {
		t.Errorf("Sitzungen angefasst")
	}

	// Der Rückruf-Weg erkennt Anna nach der Migration wieder und weist ein
	// anderes Google-Konto für ihre Adresse ab — wie vor dem Update.
	r := NewAnbieterBindungRepo(db, nil)
	if err := r.Pruefen(ctx, "anna@example.com", googleIss, "42"); err != nil {
		t.Errorf("Anna nach der Migration: %v", err)
	}
	if err := r.Pruefen(ctx, "anna@example.com", googleIss, "43"); !errors.Is(err, ErrBindungAbweichend) {
		t.Errorf("anderes Konto für Anna: %v, erwartet ErrBindungAbweichend", err)
	}
}

// Die CHECK-Regeln der neuen Tabellen.
func TestMigration0046_Schranken(t *testing.T) {
	db := loeschDB(t)
	mustExec(t, db, `INSERT INTO users (id, email, name, created) VALUES ('u1', 'anna@example.com', '', '2026-01-01')`)
	mustExec(t, db, `INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k1', 'carla@example.com', 'invited', 1, 1)`)
	falsch := map[string]string{
		"Bindung ohne Person":     `INSERT INTO anbieter_bindungen (iss, sub, anbieter, angelegt) VALUES ('i', 's', 'standard', 1)`,
		"Kürzel password":         `INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('i', 's', 'password', 'u1', 1)`,
		"Kürzel groß":             `INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('i', 's', 'Google', 'u1', 1)`,
		"Kürzel mit Schrägstrich": `INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('i', 's', 'a/b', 'u1', 1)`,
		"leeres sub":              `INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('i', '', 'standard', 'u1', 1)`,
		"fremde user_id":          `INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('i', 's', 'standard', 'u-fehlt', 1)`,
		"Konto mit Domain":        `INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k2', '@example.com', 'invited', 1, 1)`,
		"Konto groß geschrieben":  `INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k2', 'Bert@example.com', 'invited', 1, 1)`,
		"Konto doppelt":           `INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k2', 'carla@example.com', 'invited', 1, 1)`,
		"Zustand unbekannt":       `INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k2', 'bert@example.com', 'aktiv', 1, 1)`,
		"Einladung kurzer Hash":   `INSERT INTO einladungen (token_hash, konto_id, zweck, angelegt, ablauf) VALUES ('abc', 'k1', 'invite', 1, 2)`,
		"Einladung Zweck":         `INSERT INTO einladungen (token_hash, konto_id, zweck, angelegt, ablauf) VALUES ('` + strings.Repeat("e", 64) + `', 'k1', 'login', 1, 2)`,
		"Code kurzer Hash":        `INSERT INTO konto_codes (konto_id, code_hash, angelegt) VALUES ('k1', 'abc', 1)`,
	}
	for name, q := range falsch {
		if _, err := db.Exec(q); err == nil {
			t.Errorf("%s: angenommen, erwartet Ablehnung", name)
		}
	}
	// Je Person und Anbieter höchstens eine Bindung; zwei Anbieter gehen.
	mustExec(t, db, `INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('i1', 's1', 'google', 'u1', 1)`)
	mustExec(t, db, `INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('i2', 's2', 'authelia', 'u1', 1)`)
	if _, err := db.Exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES ('i1', 's9', 'google', 'u1', 1)`); err == nil {
		t.Errorf("zweite google-Bindung derselben Person angenommen")
	}
	mustExec(t, db, `INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, angelegt) VALUES ('i3', 's3', 'google', 'k1', 1)`)
	if _, err := db.Exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, angelegt) VALUES ('i3', 's4', 'google', 'k1', 1)`); err == nil {
		t.Errorf("zweite google-Bindung desselben Kontos angenommen")
	}
}

// bindungsRepo: frische Datenbank, NutzerAnlegen über BootstrapNewUser mit
// festen IDs je Adresse.
func bindungsRepo(t *testing.T) (*sql.DB, *AnbieterBindungRepo) {
	t.Helper()
	db := loeschDB(t)
	b := NewBootstrapRepo(db)
	var mu sync.Mutex
	n := 0
	return db, NewAnbieterBindungRepo(db, func(ctx context.Context, email string) error {
		mu.Lock()
		n++
		id := fmt.Sprintf("u-%d", n)
		mu.Unlock()
		_, err := b.BootstrapNewUser(ctx, id, "b-"+id, email, "")
		return err
	})
}

func TestAnbieterBindung_ErsterLoginBindetUndLegtNutzerAn(t *testing.T) {
	ctx := context.Background()
	db, r := bindungsRepo(t)
	if err := r.Pruefen(ctx, " Anna@Example.com", googleIss, "42"); err != nil {
		t.Fatal(err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM users WHERE email = 'anna@example.com'`); n != 1 {
		t.Fatalf("users-Zeile nicht angelegt")
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM acl WHERE user_email = 'anna@example.com' AND role = 'owner'`); n != 1 {
		t.Errorf("persönliches Board nicht angelegt (BootstrapNewUser)")
	}
	b, err := r.NachKonto(ctx, googleIss, "42")
	if err != nil || b.Anbieter != AnbieterStandard || b.Email != "anna@example.com" || b.UserID == "" || b.KontoID != "" {
		t.Fatalf("NachKonto: %+v, %v", b, err)
	}
	if l, err := r.FuerNutzer(ctx, b.UserID); err != nil || len(l) != 1 || l[0].Sub != "42" {
		t.Errorf("FuerNutzer: %+v, %v", l, err)
	}
	// Zweiter Login mit demselben Konto: ok, nichts Neues.
	if err := r.Pruefen(ctx, "anna@example.com", googleIss, "42"); err != nil {
		t.Errorf("zweiter Login: %v", err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM users`); n != 1 {
		t.Errorf("users: %d, erwartet 1", n)
	}
	if _, err := r.NachKonto(ctx, googleIss, "nie"); !errors.Is(err, ErrBindungUnbekannt) {
		t.Errorf("unbekanntes Konto: %v", err)
	}
}

func TestAnbieterBindung_Abweichungen(t *testing.T) {
	ctx := context.Background()
	db, r := bindungsRepo(t)
	if err := r.Pruefen(ctx, "anna@example.com", googleIss, "42"); err != nil {
		t.Fatal(err)
	}
	// Dieselbe Adresse, anderes Konto (auch bei einem anderen Anbieter: keine
	// Verknüpfung per Adressgleichheit, Kritik W4).
	if err := r.Pruefen(ctx, "anna@example.com", googleIss, "43"); !errors.Is(err, ErrBindungAbweichend) {
		t.Errorf("anderes sub: %v", err)
	}
	if err := r.PruefenFuer(ctx, "authelia", "anna@example.com", "https://auth.example", "anna"); !errors.Is(err, ErrBindungAbweichend) {
		t.Errorf("zweiter Anbieter per Adresse: %v", err)
	}
	// Dasselbe Konto meldet eine andere Adresse.
	if err := r.Pruefen(ctx, "bert@example.com", googleIss, "42"); !errors.Is(err, ErrBindungAbweichend) {
		t.Errorf("gebundenes Konto mit fremder Adresse: %v", err)
	}
	// Eine Bindung nur an ein unbestätigtes Konto lässt (noch) niemanden ein.
	mustExec(t, db, `INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k1', 'carla@example.com', 'pending', 1, 1)`)
	mustExec(t, db, `INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, angelegt) VALUES (?, '50', 'standard', 'k1', 1)`, googleIss)
	if err := r.Pruefen(ctx, "carla@example.com", googleIss, "50"); !errors.Is(err, ErrBindungAbweichend) {
		t.Errorf("Bindung an unbestätigtes Konto: %v", err)
	}
	if b, err := r.NachKonto(ctx, googleIss, "50"); err != nil || b.Email != "carla@example.com" || b.KontoID != "k1" || b.UserID != "" {
		t.Errorf("NachKonto für Konto-Bindung: %+v, %v", b, err)
	}
	// Ungültige Angaben schreiben nichts.
	vorher := zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen`)
	for _, a := range [][4]string{{"standard", "", googleIss, "1"}, {"standard", "@example.com", googleIss, "1"}, {"standard", "x@example.com", "", "1"}, {"standard", "x@example.com", googleIss, " "}, {"", "x@example.com", googleIss, "1"}} {
		if err := r.PruefenFuer(ctx, a[0], a[1], a[2], a[3]); err == nil {
			t.Errorf("ungültige Angaben %q angenommen", a)
		}
	}
	if zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen`) != vorher || zaehle(t, db, `SELECT COUNT(*) FROM users`) != 1 {
		t.Errorf("ungültige Angaben haben geschrieben")
	}
}

func TestAnbieterBindung_OhneNutzerAnlegen(t *testing.T) {
	db := loeschDB(t)
	r := NewAnbieterBindungRepo(db, nil)
	if err := r.Pruefen(context.Background(), "anna@example.com", googleIss, "42"); err == nil || errors.Is(err, ErrBindungAbweichend) {
		t.Fatalf("ohne users-Zeile und NutzerAnlegen: %v, erwartet Fehler", err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen`); n != 0 {
		t.Errorf("Bindung trotz Fehler angelegt")
	}
}

// Nach `nutzer umziehen` erkennt die Bindung die Person unter der neuen
// Adresse (sie trägt keine Adresse mehr, sondern users.id).
func TestAnbieterBindung_FolgtDemUmzug(t *testing.T) {
	ctx := context.Background()
	db, r := bindungsRepo(t)
	if err := r.Pruefen(ctx, "anna@example.com", googleIss, "42"); err != nil {
		t.Fatal(err)
	}
	if _, err := NutzerUmziehen(ctx, db, "anna@example.com", "anna.neu@example.net", false); err != nil {
		t.Fatal(err)
	}
	if err := r.Pruefen(ctx, "anna.neu@example.net", googleIss, "42"); err != nil {
		t.Errorf("neue Adresse nach Umzug: %v", err)
	}
	if err := r.Pruefen(ctx, "anna@example.com", googleIss, "42"); !errors.Is(err, ErrBindungAbweichend) {
		t.Errorf("alte Adresse nach Umzug: %v", err)
	}
}

// Zwei gleichzeitige erste Logins derselben Adresse mit verschiedenen
// Konten: genau eines wird gebunden (wie unter ADR-0148).
func TestAnbieterBindung_GleichzeitigGenauEine(t *testing.T) {
	ctx := context.Background()
	for runde := 0; runde < 5; runde++ {
		db, r := bindungsRepo(t)
		const n = 6
		var wg sync.WaitGroup
		fehler := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				fehler[i] = r.Pruefen(ctx, "anna@example.com", googleIss, fmt.Sprintf("sub-%d", i))
			}(i)
		}
		wg.Wait()
		ok := 0
		for i, err := range fehler {
			switch {
			case err == nil:
				ok++
			case !errors.Is(err, ErrBindungAbweichend):
				t.Fatalf("Runde %d, Login %d: %v", runde, i, err)
			}
		}
		if ok != 1 || zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen`) != 1 || zaehle(t, db, `SELECT COUNT(*) FROM users`) != 1 {
			t.Fatalf("Runde %d: %d erfolgreich, erwartet genau 1", runde, ok)
		}
	}
}

func TestKontoRepo_Zustand(t *testing.T) {
	ctx := context.Background()
	db := loeschDB(t)
	mustExec(t, db, `INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k1', 'carla@example.com', 'active', 1, 1)`)
	r := NewKontoRepo(db)
	for adr, want := range map[string]string{" Carla@Example.com ": KontoAktiv, "bert@example.com": "", "@example.com": "", "": ""} {
		if got, err := r.Zustand(ctx, adr); err != nil || got != want {
			t.Errorf("Zustand(%q) = %q, %v; erwartet %q", adr, got, err, want)
		}
	}
}

// Sitzungs-Kritik 02.11. K1-2: Gibt es zwei users-Zeilen, die sich nur in der
// Schreibweise unterscheiden, bindet die Migration an die älteste — dieselbe
// Wahl wie AnbieterBindungRepo.nutzerID.
func TestMigration0046_DoppelteUsersAeltesteGewinnt(t *testing.T) {
	db := schema45(t)
	mustExec(t, db, `INSERT INTO users (id, email, name, created) VALUES ('u-b', 'Anna@Example.com', '', '2026-02-01')`)
	mustExec(t, db, `INSERT INTO users (id, email, name, created) VALUES ('u-a', 'anna@example.com', '', '2026-01-01')`)
	mustExec(t, db, `INSERT INTO oidc_bindungen (email, iss, sub, angelegt) VALUES ('anna@example.com', ?, '42', 1)`, googleIss)
	if err := RunMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow(`SELECT user_id FROM anbieter_bindungen WHERE sub = '42'`).Scan(&id); err != nil || id != "u-a" {
		t.Fatalf("Bindung an %q (%v), erwartet die älteste Zeile u-a", id, err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen`); n != 1 {
		t.Fatalf("%d Bindungen, erwartet 1", n)
	}
}

// Sitzungs-Kritik 02.11. K2-3: Dasselbe Anbieter-Konto meldet sich gleichzeitig
// unter zwei Adressen an. Deterministisch nachgestellt: Zwischen der ersten
// Prüfung (Paar unbekannt) und dem INSERT bindet ein anderer Login das Paar an
// Anna. Bert darf dann nicht durchkommen — die Prüfung nach dem INSERT muss
// greifen (b.UserID != userID).
func TestAnbieterBindung_RennenGleichesKontoAndereAdresse(t *testing.T) {
	ctx := context.Background()
	db := loeschDB(t)
	mustExec(t, db, `INSERT INTO users (id, email, name, created) VALUES ('u-anna', 'anna@example.com', '', '2026-01-01')`)
	b := NewBootstrapRepo(db)
	r := NewAnbieterBindungRepo(db, func(ctx context.Context, email string) error {
		// der „andere Login“ gewinnt genau hier
		mustExec(t, db, `INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt) VALUES (?, '42', 'standard', 'u-anna', 1)`, googleIss)
		_, err := b.BootstrapNewUser(ctx, "u-bert", "b-bert", email, "")
		return err
	})
	if err := r.Pruefen(ctx, "bert@example.com", googleIss, "42"); !errors.Is(err, ErrBindungAbweichend) {
		t.Fatalf("Bert mit Annas Anbieter-Konto: %v, erwartet ErrBindungAbweichend", err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM anbieter_bindungen WHERE user_id = 'u-anna' AND sub = '42'`); n != 1 {
		t.Fatalf("Annas Bindung verändert")
	}
}
