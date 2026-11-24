package storage_test

// Sprint-Abschluss 110, Nacharbeit 2 (Karte #1103): F-2925..F-2929.
// Maßstab für „zurückgesetzt“ ist allein der Rücksetz-Link des Kontos (Zweck
// reset, offen oder abgelaufen), nicht die users-Zeile. Testdaten nur example.org.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// zurueckgesetzt bringt eine Person mit users-Zeile und Google-Konto in den
// Zustand invited mit Rücksetz-Link.
func zurueckgesetzt(t *testing.T, r *storage.EinladungRepo, k *storage.KontoRepo, nutzer func(context.Context, string) error, adr string) (string, storage.NeueEinladung) {
	t.Helper()
	id := bestaetigtesGoogleKonto(t, r, k, nutzer, adr, "sub-"+adr)
	neu, err := r.Zuruecksetzen(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return id, neu
}

// F-2925: Das Abräumen löscht den jüngsten Rücksetz-Link eines noch eingeladenen
// Kontos nicht; die Vorab-Liste bleibt ausgesetzt, „Neuer Link“ bleibt angeboten.
func TestF2925_AbraeumenVerschontJuengstenResetLink(t *testing.T) {
	ctx := context.Background()
	db, r, u := einladungDB(t)
	k := storage.NewKontoRepo(db)
	id, _ := zurueckgesetzt(t, r, k, nutzerAnleger(db), "anna@example.org")
	u.t = u.t.Add(2 * 24 * time.Hour)
	if _, err := r.Zuruecksetzen(ctx, id); err != nil { // zweiter Rücksetz-Link, jünger
		t.Fatal(err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM einladungen WHERE konto_id = ? AND zweck = 'reset'`, id); n != 2 {
		t.Fatalf("Rücksetz-Links vorher: %d", n)
	}
	u.t = u.t.Add(60 * 24 * time.Hour) // beide lange abgelaufen, Aufbewahrung vorbei
	if _, err := r.AbgelaufeneAbraeumen(ctx); err != nil {
		t.Fatal(err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM einladungen WHERE konto_id = ? AND zweck = 'reset'`, id); n != 1 {
		t.Fatalf("Rücksetz-Links nach Abräumen: %d, erwartet genau der jüngste", n)
	}
	if ja, err := k.VorabListeAusgesetzt(ctx, "anna@example.org"); err != nil || !ja {
		t.Errorf("Liste nach Abräumen ausgesetzt=%v err=%v", ja, err)
	}
	if z := uebersichtZeile(t, r, id); !z.NeuerLink || !z.LinkAbgelaufen {
		t.Errorf("Übersicht: neuerLink=%v abgelaufen=%v", z.NeuerLink, z.LinkAbgelaufen)
	}
	if _, err := r.Zuruecksetzen(ctx, id); err != nil {
		t.Errorf("Neuer Link nach Abräumen: %v", err)
	}
}

// Nur invited (und blocked) verschont: ein eingelöster Rücksetz-Link eines
// wartenden Kontos und abgelaufene Einladungs-Links gehen wie bisher.
func TestF2925_AbraeumenRaeumtSonstigesWeiterAb(t *testing.T) {
	ctx := context.Background()
	db, r, u := einladungDB(t)
	k := storage.NewKontoRepo(db)
	// wartendes Konto mit eingelöstem Rücksetz-Link
	id, neu := zurueckgesetzt(t, r, k, nutzerAnleger(db), "bert@example.org")
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(neu.Token), storage.Einloesung{Anbieter: "google", Iss: nIss, Sub: "sub-bert-neu"}); err != nil {
		t.Fatal(err)
	}
	// frische Einladung, abgelaufen
	e, err := r.Einladen(ctx, "cora@example.org")
	if err != nil {
		t.Fatal(err)
	}
	u.t = u.t.Add(60 * 24 * time.Hour)
	if _, err := r.AbgelaufeneAbraeumen(ctx); err != nil {
		t.Fatal(err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM einladungen WHERE konto_id = ?`, id); n != 0 {
		t.Errorf("Links des wartenden Kontos nach Abräumen: %d", n)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM einladungen WHERE konto_id = ?`, e.KontoID); n != 0 {
		t.Errorf("abgelaufene Einladung nach Abräumen: %d", n)
	}
	// gesperrtes Konto: Rücksetz-Link bleibt (Entsperren führt nach invited)
	id2, _ := zurueckgesetzt(t, r, k, nutzerAnleger(db), "dora@example.org")
	if _, err := k.Sperren(ctx, id2, u.t.Unix()); err != nil {
		t.Fatal(err)
	}
	u.t = u.t.Add(60 * 24 * time.Hour)
	if _, err := r.AbgelaufeneAbraeumen(ctx); err != nil {
		t.Fatal(err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM einladungen WHERE konto_id = ? AND zweck = 'reset'`, id2); n != 1 {
		t.Errorf("Rücksetz-Link des gesperrten Kontos: %d", n)
	}
}

// F-2926: Eine frische Einladung einer Person mit users-Zeile (Vorab-Liste)
// lässt sich widerrufen; die users-Zeile bleibt. Seit F-2930 wird auch
// „Neuer Link“ angeboten (users-Zeile genügt), beide Knöpfe.
func TestF2926_FrischeEinladungMitUsersZeileWiderrufbar(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	e, err := r.Einladen(ctx, "emil@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := nutzerAnleger(db)(ctx, "emil@example.org"); err != nil { // Anmeldung über die Liste
		t.Fatal(err)
	}
	z := uebersichtZeile(t, r, e.KontoID)
	if !z.HatZugang || !z.NeuerLink || !z.Widerrufbar {
		t.Fatalf("Übersicht: hatZugang=%v neuerLink=%v widerrufbar=%v", z.HatZugang, z.NeuerLink, z.Widerrufbar)
	}
	if _, err := r.Zurueckziehen(ctx, e.KontoID); err != nil {
		t.Fatalf("Widerrufen: %v", err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM users WHERE email = 'emil@example.org'`); n != 1 {
		t.Errorf("users-Zeile nach Widerrufen: %d", n)
	}
	if zst, _ := k.Zustand(ctx, "emil@example.org"); zst != "" {
		t.Errorf("Konto nach Widerrufen: %q", zst)
	}
}

// F-2926/F-2931: Ein zurückgesetztes Konto ohne users-Zeile (wartend
// zurückgesetzt) trägt den Rücksetz-Link: „Neuer Link“ angeboten, Widerrufen
// jetzt ebenfalls möglich (F-2931; es gibt keine users-Zeile, die verwaist).
func TestF2926_ResetKontoOhneUsersZeile(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	e, err := r.Einladen(ctx, "fritz@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(e.Token), storage.Einloesung{Anbieter: "google", Iss: nIss, Sub: "sub-fritz"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Zuruecksetzen(ctx, e.KontoID); err != nil { // pending → invited, reset
		t.Fatal(err)
	}
	if n := zaehle(t, db, `SELECT COUNT(*) FROM users WHERE email = 'fritz@example.org'`); n != 0 {
		t.Fatalf("users-Zeilen: %d", n)
	}
	if z := uebersichtZeile(t, r, e.KontoID); !z.NeuerLink || z.HatZugang {
		t.Errorf("Übersicht: neuerLink=%v hatZugang=%v", z.NeuerLink, z.HatZugang)
	}
	if _, err := r.Zuruecksetzen(ctx, e.KontoID); err != nil {
		t.Errorf("Neuer Link: %v", err)
	}
	if _, err := r.Zurueckziehen(ctx, e.KontoID); err != nil {
		t.Errorf("Widerrufen ohne users-Zeile: %v", err)
	}
}

// F-2928: Löst der Betreiber die letzte Bindung ohne Passwort, wird `weg` leer;
// Entsperren führt das Konto nach invited, nie nach pending.
func TestF2928_WegLeerOhneAnmeldedaten(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	b := storage.NewAnbieterBindungRepo(db, nutzerAnleger(db))
	id := bestaetigtesGoogleKonto(t, r, k, nutzerAnleger(db), "gerda@example.org", "sub-gerda")
	if _, err := b.LoesenOhnePruefung(ctx, "gerda@example.org", "google"); err != nil {
		t.Fatal(err)
	}
	if z := uebersichtZeile(t, r, id); z.Weg != "" {
		t.Fatalf("weg nach Lösen der letzten Bindung: %q", z.Weg)
	}
	if _, err := k.Sperren(ctx, id, 50); err != nil {
		t.Fatal(err)
	}
	if _, neu, err := k.Entsperren(ctx, id, 51); err != nil || neu != storage.KontoEingeladen {
		t.Errorf("Entsperren ohne Anmeldedaten: %q %v, erwartet invited", neu, err)
	}
}

// F-2929: jeder Zweig der Bedingungen für „Neuer Link“ (Uebersicht) und
// Zurücksetzen bei invited.
func TestF2929_NeuerLinkUndZuruecksetzenInvitedZweige(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)

	// Zweig A: invited + Rücksetz-Link → NeuerLink, Zurücksetzen geht.
	idA, _ := zurueckgesetzt(t, r, k, nutzerAnleger(db), "a@example.org")
	if z := uebersichtZeile(t, r, idA); !z.NeuerLink {
		t.Error("A: invited mit Rücksetz-Link muss NeuerLink haben")
	}
	if _, err := r.Zuruecksetzen(ctx, idA); err != nil {
		t.Errorf("A: Zurücksetzen: %v", err)
	}

	// Zweig B: invited, nur users-Zeile, kein Rücksetz-Link → NeuerLink (F-2930),
	// Zurücksetzen geht.
	eB, err := r.Einladen(ctx, "b@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := nutzerAnleger(db)(ctx, "b@example.org"); err != nil {
		t.Fatal(err)
	}
	if z := uebersichtZeile(t, r, eB.KontoID); !z.NeuerLink || !z.HatZugang {
		t.Errorf("B: neuerLink=%v hatZugang=%v", z.NeuerLink, z.HatZugang)
	}
	if _, err := r.Zuruecksetzen(ctx, eB.KontoID); err != nil {
		t.Errorf("B: Zurücksetzen: %v", err)
	}

	// Zweig C: invited, weder users-Zeile noch Rücksetz-Link (frisch).
	eC, err := r.Einladen(ctx, "c@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if z := uebersichtZeile(t, r, eC.KontoID); z.NeuerLink || z.HatZugang {
		t.Errorf("C: neuerLink=%v hatZugang=%v", z.NeuerLink, z.HatZugang)
	}
	if _, err := r.Zuruecksetzen(ctx, eC.KontoID); !errors.Is(err, storage.ErrKontoZustand) {
		t.Errorf("C: Zurücksetzen: %v", err)
	}

	// Zweig D: Rücksetz-Link vorhanden, aber Konto nicht invited (wartend) → kein NeuerLink.
	idD, neuD := zurueckgesetzt(t, r, k, nutzerAnleger(db), "d@example.org")
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(neuD.Token), storage.Einloesung{Anbieter: "google", Iss: nIss, Sub: "sub-d-neu"}); err != nil {
		t.Fatal(err)
	}
	if z := uebersichtZeile(t, r, idD); z.NeuerLink {
		t.Error("D: wartendes Konto darf kein NeuerLink haben")
	}
}
