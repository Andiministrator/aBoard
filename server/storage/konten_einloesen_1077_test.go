package storage_test

// Karte #1077: Einlösen, Bestätigen, Ablehnen, Sperren, Entsperren,
// Zurücksetzen (ADR-0149 §5.4–§5.6, Nachtrag #1077). Testdaten nur
// example.org.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Andiministrator/aBoard/server/storage"
)

func codeHashes(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%064x", i+1)
	}
	return out
}

func pwEinloesung() storage.Einloesung {
	return storage.Einloesung{PasswortHash: "$argon2id$test", TOTPVersiegelt: "versiegelt", TOTPSchritt: 77, CodeHashes: codeHashes(3)}
}

func zaehle(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func nutzerAnleger(db *sql.DB) func(ctx context.Context, email string) error {
	b := storage.NewBootstrapRepo(db)
	return func(ctx context.Context, email string) error {
		_, err := b.BootstrapNewUser(ctx, uuid.NewString(), uuid.NewString(), email, "")
		return err
	}
}

func TestEinloesen1077_PasswortWegGenauEinmal(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	e, err := r.Einladen(ctx, "anna@example.org")
	if err != nil {
		t.Fatal(err)
	}
	sitz := storage.NewSitzungRepo(db)
	if _, _, err := sitz.Anlegen(ctx, "anna@example.org", ""); err != nil {
		t.Fatal(err)
	}
	h := storage.EinladungTokenHash(e.Token)
	erg, err := r.Einloesen(ctx, h, pwEinloesung())
	if err != nil || erg.KontoID != e.KontoID || erg.Email != "anna@example.org" || erg.Weg != "password" || erg.Zweck != storage.EinladungZweckInvite {
		t.Fatalf("%+v %v", erg, err)
	}
	var zustand, weg string
	var schritt int64
	if err := db.QueryRow(`SELECT zustand, weg, totp_letzter_schritt FROM konten WHERE id = ?`, e.KontoID).Scan(&zustand, &weg, &schritt); err != nil ||
		zustand != storage.KontoWartet || weg != "password" || schritt != 77 {
		t.Fatalf("konto: %s %s %d %v", zustand, weg, schritt, err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM konto_codes WHERE konto_id = ?`, e.KontoID); n != 3 {
		t.Errorf("codes: %d", n)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM sitzungen WHERE email = 'anna@example.org'`); n != 0 {
		t.Errorf("sitzungen nach dem einlösen: %d", n)
	}
	if _, err := r.Einloesen(ctx, h, pwEinloesung()); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Fatalf("zweites Einlösen: %v", err)
	}
	if _, err := r.Pruefen(ctx, e.Token); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Fatalf("Link nach dem Einlösen noch offen: %v", err)
	}
}

func TestEinloesen1077_AbgelaufenUndUnvollstaendig(t *testing.T) {
	ctx := context.Background()
	_, r, u := einladungDB(t)
	e, err := r.Einladen(ctx, "anna@example.org")
	if err != nil {
		t.Fatal(err)
	}
	h := storage.EinladungTokenHash(e.Token)
	for name, d := range map[string]storage.Einloesung{
		"leer":       {},
		"beide":      {PasswortHash: "x", TOTPVersiegelt: "y", CodeHashes: codeHashes(1), Anbieter: "google", Iss: "i", Sub: "s"},
		"ohne codes": {PasswortHash: "x", TOTPVersiegelt: "y"},
		"ohne sub":   {Anbieter: "google", Iss: "i"},
		"password":   {Anbieter: "password", Iss: "i", Sub: "s"},
	} {
		if _, err := r.Einloesen(ctx, h, d); err == nil {
			t.Errorf("%s: angenommen", name)
		}
	}
	u.t = e.Ablauf
	if _, err := r.Einloesen(ctx, h, pwEinloesung()); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Fatalf("abgelaufen: %v", err)
	}
}

func TestEinloesen1077_AnbieterBindetAnKonto(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	e, err := r.Einladen(ctx, "anna@example.org")
	if err != nil {
		t.Fatal(err)
	}
	// Ein anderes Konto hält (iss, sub) schon: kein Einlösen, Link bleibt.
	if _, err := db.Exec(`INSERT INTO konten (id, email, zustand, angelegt, geaendert) VALUES ('k-x', 'x@example.org', 'pending', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, angelegt) VALUES ('https://id.example.org', 'belegt', 'heim', 'k-x', 1)`); err != nil {
		t.Fatal(err)
	}
	h := storage.EinladungTokenHash(e.Token)
	d := storage.Einloesung{Anbieter: "heim", Iss: "https://id.example.org", Sub: "belegt", GemeldeteEmail: "privat@example.org", GemeldeterName: "Anna"}
	if _, err := r.Einloesen(ctx, h, d); !errors.Is(err, storage.ErrBindungBelegt) {
		t.Fatalf("belegtes Anbieter-Konto: %v", err)
	}
	if _, err := r.Pruefen(ctx, e.Token); err != nil {
		t.Fatalf("Link nach abgewiesenem Einlösen verbraucht: %v", err)
	}
	d.Sub = "frei"
	d.GemeldeterName = strings.Repeat("ä", 300)
	if _, err := r.Einloesen(ctx, h, d); err != nil {
		t.Fatal(err)
	}
	var konto sql.NullString
	var user sql.NullString
	if err := db.QueryRow(`SELECT konto_id, user_id FROM anbieter_bindungen WHERE sub = 'frei'`).Scan(&konto, &user); err != nil ||
		konto.String != e.KontoID || user.Valid {
		t.Fatalf("bindung: %v %v %v", konto, user, err)
	}
	var weg, gemeldet, name string
	if err := db.QueryRow(`SELECT weg, gemeldete_email, gemeldeter_name FROM konten WHERE id = ?`, e.KontoID).Scan(&weg, &gemeldet, &name); err != nil ||
		weg != "heim" || gemeldet != "privat@example.org" || len(name) > 200 || !strings.HasPrefix(name, "ä") {
		t.Fatalf("konto: %q %q %d %v", weg, gemeldet, len(name), err)
	}
}

// Nebenläufigkeit §50: Lesen-dann-Schreiben in BEGIN IMMEDIATE. Zehn Runden
// à 16 gleichzeitige Versuche (Startschranke): genau einer löst ein, alle
// anderen bekommen „Link unbekannt“ — kein dritter Ausgang (database is
// locked). Mit DEFERRED-Transaktion rot (Gegenprobe #1077).
func TestEinloesen1077_ParallelGenauEiner(t *testing.T) {
	ctx := context.Background()
	_, r, _ := einladungDB(t)
	for runde := 0; runde < 10; runde++ {
		e, err := r.Einladen(ctx, fmt.Sprintf("parallel%d@example.org", runde))
		if err != nil {
			t.Fatal(err)
		}
		h := storage.EinladungTokenHash(e.Token)
		var wg sync.WaitGroup
		var mu sync.Mutex
		los := make(chan struct{})
		ok, weg, sonst := 0, 0, []error{}
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				d := pwEinloesung()
				if i%2 == 1 {
					d = storage.Einloesung{Anbieter: "heim", Iss: "https://id.example.org", Sub: fmt.Sprint("s", runde, "-", i)}
				}
				<-los
				_, err := r.Einloesen(ctx, h, d)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					ok++
				case errors.Is(err, storage.ErrEinladungUnbekannt):
					weg++
				default:
					sonst = append(sonst, err)
				}
			}(i)
		}
		close(los)
		wg.Wait()
		if ok != 1 || weg != 15 || len(sonst) != 0 {
			t.Fatalf("Runde %d: ok=%d unbekannt=%d sonst=%v", runde, ok, weg, sonst)
		}
	}
}

func eingeloestesKonto(t *testing.T, db *sql.DB, r *storage.EinladungRepo, email string) string {
	t.Helper()
	ctx := context.Background()
	e, err := r.Einladen(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(e.Token),
		storage.Einloesung{Anbieter: "heim", Iss: "https://id.example.org", Sub: "sub-" + email}); err != nil {
		t.Fatal(err)
	}
	return e.KontoID
}

func TestBestaetigen1077_LegtNutzerAnUndBindet(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	id := eingeloestesKonto(t, db, r, "anna@example.org")
	if gibt, err := k.GibtAdmin(ctx); err != nil || gibt {
		t.Fatalf("GibtAdmin auf leerem Server: %v %v", gibt, err)
	}
	email, err := k.Bestaetigen(ctx, id, nutzerAnleger(db), 5)
	if err != nil || email != "anna@example.org" {
		t.Fatalf("%q %v", email, err)
	}
	if z, _ := k.Zustand(ctx, email); z != storage.KontoAktiv {
		t.Errorf("zustand %q", z)
	}
	var uid string
	if err := db.QueryRow(`SELECT b.user_id FROM anbieter_bindungen b JOIN users u ON u.id = b.user_id WHERE b.konto_id = ?`, id).Scan(&uid); err != nil {
		t.Fatalf("bindung ohne user_id: %v", err)
	}
	if gibt, _ := k.GibtAdmin(ctx); !gibt {
		t.Error("erste bestätigte Person ist nicht Admin")
	}
	if _, err := k.Bestaetigen(ctx, id, nutzerAnleger(db), 6); !errors.Is(err, storage.ErrKontoZustand) {
		t.Errorf("zweites Bestätigen: %v", err)
	}
	if _, err := k.Bestaetigen(ctx, "gibt-es-nicht", nutzerAnleger(db), 6); !errors.Is(err, storage.ErrKontoUnbekannt) {
		t.Errorf("unbekannt: %v", err)
	}
}

func TestAblehnen1077(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	// invited → nicht ablehnbar (dafür Zurückziehen)
	e, err := r.Einladen(ctx, "offen@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Ablehnen(ctx, e.KontoID); !errors.Is(err, storage.ErrKontoZustand) {
		t.Errorf("invited: %v", err)
	}
	id := eingeloestesKonto(t, db, r, "anna@example.org")
	if email, err := k.Ablehnen(ctx, id); err != nil || email != "anna@example.org" {
		t.Fatalf("%q %v", email, err)
	}
	for _, tab := range []string{"konten", "einladungen", "anbieter_bindungen"} {
		if n := zaehle(t, db, `SELECT COUNT(*) FROM `+tab+` WHERE `+map[string]string{"konten": "id", "einladungen": "konto_id", "anbieter_bindungen": "konto_id"}[tab]+` = ?`, id); n != 0 {
			t.Errorf("%s nach Ablehnen: %d", tab, n)
		}
	}
	// Dasselbe Anbieter-Konto lässt sich danach neu einladen (Verify N-1).
	if id2 := eingeloestesKonto(t, db, r, "anna@example.org"); id2 == "" {
		t.Fatal("neu einladen")
	}

	// Zurückgesetzte Person mit users-Zeile: nicht ablehnen.
	id3 := eingeloestesKonto(t, db, r, "ben@example.org")
	if _, err := k.Bestaetigen(ctx, id3, nutzerAnleger(db), 1); err != nil {
		t.Fatal(err)
	}
	neu, err := r.Zuruecksetzen(ctx, id3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(neu.Token), pwEinloesung()); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Ablehnen(ctx, id3); !errors.Is(err, storage.ErrKontoHatZugang) {
		t.Errorf("mit users-Zeile: %v", err)
	}
}

func TestSperrenEntsperren1077(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	sitz := storage.NewSitzungRepo(db)

	aktiv := eingeloestesKonto(t, db, r, "anna@example.org")
	if _, err := k.Bestaetigen(ctx, aktiv, nutzerAnleger(db), 1); err != nil {
		t.Fatal(err)
	}
	wartet := eingeloestesKonto(t, db, r, "ben@example.org")
	offen, err := r.Einladen(ctx, "carla@example.org")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct{ id, email, nachher string }{
		// A-2: nie direkt aktiv — der Admin bestätigt nach dem Entsperren.
		{aktiv, "anna@example.org", storage.KontoWartet},
		{wartet, "ben@example.org", storage.KontoWartet},
		{offen.KontoID, "carla@example.org", storage.KontoEingeladen},
	} {
		if _, _, err := sitz.Anlegen(ctx, f.email, ""); err != nil {
			t.Fatal(err)
		}
		if email, err := k.Sperren(ctx, f.id, 2); err != nil || email != f.email {
			t.Fatalf("sperren %s: %q %v", f.email, email, err)
		}
		if z, _ := k.Zustand(ctx, f.email); z != storage.KontoGesperrt {
			t.Errorf("%s: %q", f.email, z)
		}
		if n := zaehle(t, db, `SELECT COUNT(*) FROM sitzungen WHERE email = ?`, f.email); n != 0 {
			t.Errorf("%s: %d Sitzungen nach dem Sperren", f.email, n)
		}
		if _, err := k.Sperren(ctx, f.id, 3); err != nil {
			t.Errorf("zweimal sperren: %v", err)
		}
		if _, neu, err := k.Entsperren(ctx, f.id, 4); err != nil || neu != f.nachher {
			t.Errorf("entsperren %s: %q %v, erwartet %q", f.email, neu, err, f.nachher)
		}
		if _, _, err := k.Entsperren(ctx, f.id, 4); !errors.Is(err, storage.ErrKontoZustand) {
			t.Errorf("entsperren ohne Sperre: %v", err)
		}
	}
	// Zurückgesetzte Person (users-Zeile, ohne Anmeldedaten): entsperrt nach
	// invited, nicht active (Sitzungs-Kritik 03.11. A-2).
	if _, err := k.Bestaetigen(ctx, aktiv, nutzerAnleger(db), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Zuruecksetzen(ctx, aktiv); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Sperren(ctx, aktiv, 6); err != nil {
		t.Fatal(err)
	}
	if _, neu, err := k.Entsperren(ctx, aktiv, 7); err != nil || neu != storage.KontoEingeladen {
		t.Errorf("zurückgesetzt, gesperrt, entsperrt: %q %v, erwartet invited", neu, err)
	}
	if _, err := k.Sperren(ctx, "gibt-es-nicht", 1); !errors.Is(err, storage.ErrKontoUnbekannt) {
		t.Errorf("unbekannt: %v", err)
	}
}

func TestZuruecksetzen1077(t *testing.T) {
	ctx := context.Background()
	db, r, u := einladungDB(t)
	k := storage.NewKontoRepo(db)
	e, err := r.Einladen(ctx, "anna@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Zuruecksetzen(ctx, e.KontoID); !errors.Is(err, storage.ErrKontoZustand) {
		t.Fatalf("invited zurücksetzen: %v", err)
	}
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(e.Token), pwEinloesung()); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Bestaetigen(ctx, e.KontoID, nutzerAnleger(db), 1); err != nil {
		t.Fatal(err)
	}
	// Eine Bindung an users (z. B. aus der Zeit vor der Einladung) geht mit.
	if _, err := db.Exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, user_id, angelegt)
		SELECT 'https://accounts.example.org', 'alt', 'standard', id, 1 FROM users WHERE email = 'anna@example.org'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := storage.NewSitzungRepo(db).Anlegen(ctx, "anna@example.org", ""); err != nil {
		t.Fatal(err)
	}
	u.t = u.t.Add(1)
	neu, err := r.Zuruecksetzen(ctx, e.KontoID)
	if err != nil || neu.Email != "anna@example.org" || len(neu.Token) != 43 {
		t.Fatalf("%+v %v", neu, err)
	}
	var zustand string
	var pw, totp sql.NullString
	if err := db.QueryRow(`SELECT zustand, passwort_hash, totp_geheimnis FROM konten WHERE id = ?`, e.KontoID).Scan(&zustand, &pw, &totp); err != nil ||
		zustand != storage.KontoEingeladen || pw.Valid || totp.Valid {
		t.Fatalf("konto nach dem Zurücksetzen: %s %v %v %v", zustand, pw, totp, err)
	}
	for q, want := range map[string]int{
		`SELECT COUNT(*) FROM konto_codes WHERE konto_id = '` + e.KontoID + `'`:                                            0,
		`SELECT COUNT(*) FROM anbieter_bindungen`:                                                                          0,
		`SELECT COUNT(*) FROM sitzungen WHERE email = 'anna@example.org'`:                                                  0,
		`SELECT COUNT(*) FROM users WHERE email = 'anna@example.org'`:                                                      1,
		`SELECT COUNT(*) FROM einladungen WHERE konto_id = '` + e.KontoID + `' AND zweck = 'reset' AND eingeloest IS NULL`: 1,
	} {
		if n := zaehle(t, db, q); n != want {
			t.Errorf("%s: %d, erwartet %d", q, n, want)
		}
	}
	o, err := r.Pruefen(ctx, neu.Token)
	if err != nil || o.Zweck != storage.EinladungZweckReset || o.Zustand != storage.KontoEingeladen {
		t.Fatalf("neuer Link: %+v %v", o, err)
	}
	// Einlösen des Rücksetz-Links führt wieder nach pending (Bestätigung nötig).
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(neu.Token), pwEinloesung()); err != nil {
		t.Fatal(err)
	}
	if z, _ := k.Zustand(ctx, "anna@example.org"); z != storage.KontoWartet {
		t.Errorf("nach dem Einlösen des Rücksetz-Links: %q", z)
	}
	// Bestätigen klappt auch mit vorhandener users-Zeile.
	if _, err := k.Bestaetigen(ctx, e.KontoID, nutzerAnleger(db), 3); err != nil {
		t.Fatal(err)
	}
}
