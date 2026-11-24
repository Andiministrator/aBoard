package storage

// Tests zu den Sitzungen der eingebauten Anmeldung (ADR-0148 §2, Karte #1051).

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/uhr"
)

type testUhr struct{ t time.Time }

func (u *testUhr) jetzt() time.Time { return u.t }

func sitzungRepoTest(t *testing.T) (*SitzungRepo, *testUhr) {
	t.Helper()
	db := loeschDB(t)
	uhr := &testUhr{t: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)}
	return NewSitzungRepo(db).MitUhr(uhr.jetzt), uhr
}

func TestSitzung1051_AnlegenUndPruefen(t *testing.T) {
	r, uhr := sitzungRepoTest(t)
	ctx := context.Background()
	id, ablauf, err := r.Anlegen(ctx, "  Anna@Example.COM ", "Anna")
	if err != nil {
		t.Fatalf("Anlegen: %v", err)
	}
	if len(id) != sitzungIDLaenge {
		t.Fatalf("ID-Länge %d, erwartet %d (256 Bit)", len(id), sitzungIDLaenge)
	}
	if !ablauf.Equal(uhr.t.Add(SitzungHoechstdauer)) {
		t.Fatalf("Ablauf %v, erwartet Anlage + 7 Tage", ablauf)
	}
	s, err := r.Pruefen(ctx, id)
	if err != nil {
		t.Fatalf("Pruefen: %v", err)
	}
	if s.Email != "anna@example.com" || s.Name != "Anna" {
		t.Fatalf("Sitzung %+v: Adresse nicht normalisiert oder Name fehlt", s)
	}
	// Nur der Hash steht in der Tabelle, nie die ID.
	if n := zaehle(t, r.db, `SELECT COUNT(*) FROM sitzungen WHERE id_hash = ?`, hashToken(id)); n != 1 {
		t.Fatalf("Hash-Zeile fehlt (%d)", n)
	}
	if f := sucheReste(t, r.db, id); len(f) != 0 {
		t.Fatalf("Klartext-ID steht in der Datenbank: %+v", f)
	}
	// Jede Anmeldung bekommt eine neue ID (Session-Fixation).
	id2, _, err := r.Anlegen(ctx, "anna@example.com", "Anna")
	if err != nil || id2 == id {
		t.Fatalf("zweite Anlage: id gleich=%v err=%v", id2 == id, err)
	}
}

func TestSitzung1051_UnbekannteUndFalscheIDs(t *testing.T) {
	r, _ := sitzungRepoTest(t)
	ctx := context.Background()
	for _, id := range []string{"", "kurz", strings.Repeat("x", sitzungIDLaenge), strings.Repeat("x", 5000)} {
		if _, err := r.Pruefen(ctx, id); !errors.Is(err, ErrSitzungUnbekannt) {
			t.Errorf("Pruefen(%.10q…): %v, erwartet ErrSitzungUnbekannt", id, err)
		}
	}
}

func TestSitzung1051_UngueltigeAdresseWirdAbgelehnt(t *testing.T) {
	r, _ := sitzungRepoTest(t)
	for _, e := range []string{"", "   ", "@example.com"} {
		if _, _, err := r.Anlegen(context.Background(), e, ""); err == nil {
			t.Errorf("Anlegen(%q) ohne Fehler — Domain-Schlüssel oder leere Adresse dürfen keine Sitzung bekommen", e)
		}
	}
}

func TestSitzung1051_LeerlaufNach8Stunden(t *testing.T) {
	r, uhr := sitzungRepoTest(t)
	ctx := context.Background()
	id, _, _ := r.Anlegen(ctx, "anna@example.com", "")
	uhr.t = uhr.t.Add(SitzungLeerlauf - time.Second)
	if _, err := r.Pruefen(ctx, id); err != nil {
		t.Fatalf("knapp vor dem Leerlauf-Ende abgewiesen: %v", err)
	}
	// Die Nutzung eben hat den Leerlauf neu gestartet.
	uhr.t = uhr.t.Add(SitzungLeerlauf - time.Second)
	if _, err := r.Pruefen(ctx, id); err != nil {
		t.Fatalf("genutzte Sitzung nach erneut knapp 8 h abgewiesen: %v", err)
	}
	uhr.t = uhr.t.Add(SitzungLeerlauf)
	if _, err := r.Pruefen(ctx, id); !errors.Is(err, ErrSitzungUnbekannt) {
		t.Fatalf("nach 8 h ohne Nutzung noch gültig: %v", err)
	}
	if n := zaehle(t, r.db, `SELECT COUNT(*) FROM sitzungen`); n != 0 {
		t.Fatalf("abgelaufene Sitzung nicht gelöscht (%d)", n)
	}
}

func TestSitzung1051_HoechstdauerAuchBeiNutzung(t *testing.T) {
	r, uhr := sitzungRepoTest(t)
	ctx := context.Background()
	id, _, _ := r.Anlegen(ctx, "anna@example.com", "")
	ende := uhr.t.Add(SitzungHoechstdauer)
	for uhr.t.Before(ende.Add(-time.Hour)) {
		uhr.t = uhr.t.Add(time.Hour)
		if _, err := r.Pruefen(ctx, id); err != nil {
			t.Fatalf("stündlich genutzte Sitzung vor 7 Tagen abgewiesen (%v): %v", uhr.t, err)
		}
	}
	uhr.t = ende
	if _, err := r.Pruefen(ctx, id); !errors.Is(err, ErrSitzungUnbekannt) {
		t.Fatalf("Sitzung nach 7 Tagen trotz Nutzung noch gültig: %v", err)
	}
}

func TestSitzung1051_ZuletztHoechstensEinmalProMinute(t *testing.T) {
	r, uhr := sitzungRepoTest(t)
	ctx := context.Background()
	id, _, _ := r.Anlegen(ctx, "anna@example.com", "")
	start := uhr.t.Unix()
	zuletzt := func() int64 {
		var z int64
		if err := r.db.QueryRow(`SELECT zuletzt FROM sitzungen`).Scan(&z); err != nil {
			t.Fatalf("zuletzt: %v", err)
		}
		return z
	}
	uhr.t = uhr.t.Add(59 * time.Second)
	_, _ = r.Pruefen(ctx, id)
	if zuletzt() != start {
		t.Fatalf("zuletzt nach 59 s geschrieben — Polling schriebe sonst bei jeder Anfrage")
	}
	uhr.t = uhr.t.Add(time.Second)
	_, _ = r.Pruefen(ctx, id)
	if zuletzt() != uhr.t.Unix() {
		t.Fatalf("zuletzt nach 60 s nicht geschrieben: %d", zuletzt())
	}
}

func TestSitzung1051_BeendenUndAbraeumen(t *testing.T) {
	r, uhr := sitzungRepoTest(t)
	ctx := context.Background()
	a, _, _ := r.Anlegen(ctx, "anna@example.com", "")
	b, _, _ := r.Anlegen(ctx, "anna@example.com", "")
	c, _, _ := r.Anlegen(ctx, "carla@example.com", "")

	if err := r.Beenden(ctx, a); err != nil {
		t.Fatalf("Beenden: %v", err)
	}
	if _, err := r.Pruefen(ctx, a); !errors.Is(err, ErrSitzungUnbekannt) {
		t.Fatalf("beendete Sitzung noch gültig")
	}
	if err := r.Beenden(ctx, "gibtsnicht"); err != nil {
		t.Fatalf("Beenden einer unbekannten ID ist kein Fehler: %v", err)
	}
	if err := r.Beenden(ctx, b); err != nil {
		t.Fatalf("Beenden (zweite Sitzung derselben Adresse): %v", err)
	}
	if _, err := r.Pruefen(ctx, b); !errors.Is(err, ErrSitzungUnbekannt) {
		t.Fatalf("Sitzung nach Beenden noch gültig")
	}

	// Abräumen: c läuft ab; eine neue Anlage räumt sie mit ab.
	uhr.t = uhr.t.Add(SitzungLeerlauf)
	if _, _, err := r.Anlegen(ctx, "bert@example.com", ""); err != nil {
		t.Fatalf("Anlegen: %v", err)
	}
	if n := zaehle(t, r.db, `SELECT COUNT(*) FROM sitzungen WHERE id_hash = ?`, hashToken(c)); n != 0 {
		t.Fatalf("abgelaufene Sitzung bei der Neuanlage nicht abgeräumt")
	}
	uhr.t = uhr.t.Add(SitzungHoechstdauer)
	if n, err := r.AbgelaufeneAbraeumen(ctx); err != nil || n != 1 {
		t.Fatalf("AbgelaufeneAbraeumen: n=%d err=%v", n, err)
	}
}

// F-1192 (Wissen §38): Mit nicht gestellter Uhr wird eine scheinbar
// abgelaufene Sitzung abgewiesen, aber nicht gelöscht.
func TestSitzung1051_UhrNichtGestelltLoeschtNichts(t *testing.T) {
	r, uhrTest := sitzungRepoTest(t)
	ctx := context.Background()
	id, _, _ := r.Anlegen(ctx, "anna@example.com", "")
	alt := uhr.Gestellt
	uhr.Gestellt = func() (bool, error) { return false, nil }
	t.Cleanup(func() { uhr.Gestellt = alt })

	uhrTest.t = uhrTest.t.Add(SitzungHoechstdauer)
	if _, err := r.Pruefen(ctx, id); !errors.Is(err, ErrSitzungUnbekannt) {
		t.Fatalf("abgelaufene Sitzung bei ungestellter Uhr angenommen: %v", err)
	}
	if n, err := r.AbgelaufeneAbraeumen(ctx); err != nil || n != 0 {
		t.Fatalf("AbgelaufeneAbraeumen bei ungestellter Uhr: n=%d err=%v", n, err)
	}
	if n := zaehle(t, r.db, `SELECT COUNT(*) FROM sitzungen`); n != 1 {
		t.Fatalf("bei ungestellter Uhr gelöscht (übrig %d)", n)
	}
}

// Kritik A-3: Scheitert das Schreiben von „zuletzt“, bleibt die Anfrage gültig.
func TestSitzung1051_BeruehrFehlerIstKeinAbbruch(t *testing.T) {
	r, uhrTest := sitzungRepoTest(t)
	ctx := context.Background()
	id, _, _ := r.Anlegen(ctx, "anna@example.com", "")
	mustExec(t, r.db, `CREATE TRIGGER sperre BEFORE UPDATE ON sitzungen BEGIN SELECT RAISE(FAIL, 'gesperrt'); END`)
	uhrTest.t = uhrTest.t.Add(2 * time.Minute)
	if _, err := r.Pruefen(ctx, id); err != nil {
		t.Fatalf("Berühr-Fehler brach die Prüfung ab: %v", err)
	}
}

// Kritik A-2: Sitzungen nicht mehr zugelassener Adressen gehen beim Start.
func TestSitzung1051_BeendeNichtZugelassene(t *testing.T) {
	r, _ := sitzungRepoTest(t)
	ctx := context.Background()
	a, _, _ := r.Anlegen(ctx, "anna@example.com", "")
	b, _, _ := r.Anlegen(ctx, "bert@example.com", "")
	n, err := r.BeendeNichtZugelassene(ctx, func(_ context.Context, e string) (bool, error) { return e == "bert@example.com", nil })
	if err != nil || n != 1 {
		t.Fatalf("BeendeNichtZugelassene: n=%d err=%v", n, err)
	}
	if _, err := r.Pruefen(ctx, a); !errors.Is(err, ErrSitzungUnbekannt) {
		t.Fatal("Sitzung der gestrichenen Adresse lebt noch")
	}
	if _, err := r.Pruefen(ctx, b); err != nil {
		t.Fatalf("Sitzung der zugelassenen Adresse beendet: %v", err)
	}
	if n, _ := r.BeendeNichtZugelassene(ctx, nil); n != 1 {
		t.Fatalf("ohne Zulassung muss alles gehen (n=%d)", n)
	}
}

// Verify V-11: die Drossel lässt höchstens eine Zeile pro Minute durch.
func TestSitzung1051_Drossel(t *testing.T) {
	var z atomic.Int64
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	if !drossel(&z, t0) || drossel(&z, t0.Add(59*time.Second)) || !drossel(&z, t0.Add(61*time.Second)) {
		t.Fatal("Drossel lässt nicht genau eine Zeile pro Minute durch")
	}
}
