package storage_test

// Sprint-Abschluss 110, Nacharbeit (Karte #1102): F-2921..F-2924.
// Testdaten nur example.org.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

const nIss = "https://idp.example.org"

// bestaetigtesGoogleKonto legt eine Person mit eingelöstem und bestätigtem
// Google-Konto an (users-Zeile, Bindung, Konto active).
func bestaetigtesGoogleKonto(t *testing.T, r *storage.EinladungRepo, k *storage.KontoRepo, nutzer func(context.Context, string) error, adr, sub string) string {
	t.Helper()
	ctx := context.Background()
	e, err := r.Einladen(ctx, adr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(e.Token), storage.Einloesung{Anbieter: "google", Iss: nIss, Sub: sub}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Bestaetigen(ctx, e.KontoID, nutzer, 1); err != nil {
		t.Fatal(err)
	}
	return e.KontoID
}

func uebersichtZeile(t *testing.T, r *storage.EinladungRepo, id string) storage.KontoUebersicht {
	t.Helper()
	l, err := r.Uebersicht(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, z := range l {
		if z.ID == id {
			return z
		}
	}
	t.Fatalf("konto %s nicht in der Übersicht", id)
	return storage.KontoUebersicht{}
}

// F-2921: Ein zurückgesetztes Konto mit abgelaufenem Link bekommt über
// Zurücksetzen einen neuen Link; Widerrufen löscht es nicht (die Person bliebe
// sonst ohne Konto, mit users-Zeile, ausgesperrt).
func TestF2921_AbgelaufenerRuecksetzLinkNeuerLink(t *testing.T) {
	ctx := context.Background()
	db, r, u := einladungDB(t)
	k := storage.NewKontoRepo(db)
	id := bestaetigtesGoogleKonto(t, r, k, nutzerAnleger(db), "bert@example.org", "sub-bert")
	alt, err := r.Zuruecksetzen(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	u.t = u.t.Add(8 * 24 * time.Hour) // Link abgelaufen
	z := uebersichtZeile(t, r, id)
	if !z.LinkAbgelaufen || !z.NeuerLink || !z.HatZugang {
		t.Fatalf("Übersicht: abgelaufen=%v neuerLink=%v hatZugang=%v", z.LinkAbgelaufen, z.NeuerLink, z.HatZugang)
	}
	// Widerrufen darf das Konto nicht löschen.
	if _, err := r.Zurueckziehen(ctx, id); !errors.Is(err, storage.ErrEinladungPersonHatZugang) {
		t.Fatalf("Zurückziehen: %v, erwartet ErrEinladungPersonHatZugang", err)
	}
	if zst, _ := k.Zustand(ctx, "bert@example.org"); zst != storage.KontoEingeladen {
		t.Fatalf("Konto nach Zurückziehen: %q", zst)
	}
	// Neuer Link über Zurücksetzen.
	neu, err := r.Zuruecksetzen(ctx, id)
	if err != nil {
		t.Fatalf("Zurücksetzen bei invited mit users-Zeile: %v", err)
	}
	if _, err := r.Pruefen(ctx, alt.Token); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Errorf("alter Link gilt noch: %v", err)
	}
	if o, err := r.Pruefen(ctx, neu.Token); err != nil || o.Zweck != storage.EinladungZweckReset {
		t.Fatalf("neuer Link: %v %+v", err, o)
	}
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(neu.Token), storage.Einloesung{Anbieter: "google", Iss: nIss, Sub: "sub-bert-neu"}); err != nil {
		t.Fatalf("Einlösen des neuen Links: %v", err)
	}
}

// Eine frische Einladung bleibt wie bisher: Zurücksetzen 409, Widerrufen geht.
func TestF2921_FrischeEinladungUnveraendert(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	e, err := r.Einladen(ctx, "cora@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if z := uebersichtZeile(t, r, e.KontoID); z.NeuerLink || z.HatZugang {
		t.Errorf("frische Einladung: neuerLink=%v hatZugang=%v", z.NeuerLink, z.HatZugang)
	}
	if _, err := r.Zuruecksetzen(ctx, e.KontoID); !errors.Is(err, storage.ErrKontoZustand) {
		t.Errorf("Zurücksetzen frische Einladung: %v", err)
	}
	if _, err := r.Zurueckziehen(ctx, e.KontoID); err != nil {
		t.Errorf("Zurückziehen frische Einladung: %v", err)
	}
	if zst, _ := k.Zustand(ctx, "cora@example.org"); zst != "" {
		t.Errorf("Konto nach Zurückziehen: %q", zst)
	}
}

// F-2922: Nach Zurücksetzen gilt die Vorab-Liste für das Konto nicht (Tor
// fragt das hier ab); eine frische Einladung bleibt gedeckt. Der Konflikt beim
// Bestätigen ist ein eigener Fehler, kein 500.
func TestF2922_VorabListeAusgesetztUndBestaetigenKonflikt(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	id := bestaetigtesGoogleKonto(t, r, k, nutzerAnleger(db), "anna@example.org", "sub-anna")
	if ja, err := k.VorabListeAusgesetzt(ctx, "anna@example.org"); err != nil || ja {
		t.Fatalf("aktives Konto: ausgesetzt=%v err=%v", ja, err)
	}
	neu, err := r.Zuruecksetzen(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if ja, err := k.VorabListeAusgesetzt(ctx, "anna@example.org"); err != nil || !ja {
		t.Fatalf("zurückgesetzt (invited, reset): ausgesetzt=%v err=%v", ja, err)
	}
	// Frische Einladung einer anderen Person: Liste gilt.
	if _, err := r.Einladen(ctx, "dora@example.org"); err != nil {
		t.Fatal(err)
	}
	if ja, _ := k.VorabListeAusgesetzt(ctx, "dora@example.org"); ja {
		t.Errorf("frische Einladung: Liste darf nicht ausgesetzt sein")
	}
	if ja, _ := k.VorabListeAusgesetzt(ctx, "niemand@example.org"); ja {
		t.Errorf("ohne Konto: Liste darf nicht ausgesetzt sein")
	}
	// Anna löst ein; wartend → ausgesetzt.
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(neu.Token), storage.Einloesung{Anbieter: "google", Iss: nIss, Sub: "sub-anna-neu"}); err != nil {
		t.Fatal(err)
	}
	if ja, _ := k.VorabListeAusgesetzt(ctx, "anna@example.org"); !ja {
		t.Errorf("pending: Liste muss ausgesetzt sein")
	}
	// Rennen: Zwischen Einlösen und Bestätigen bindet die Person doch ein
	// anderes Google-Konto (alter Tor-Cache o. Ä.) → Bestätigen scheitert am
	// Index; das ist ein eigener Fehler und das Konto bleibt pending.
	b := storage.NewAnbieterBindungRepo(db, nutzerAnleger(db))
	if err := b.PruefenFuer(ctx, "google", "anna@example.org", nIss, "sub-fremd"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Bestaetigen(ctx, id, nutzerAnleger(db), 2); !errors.Is(err, storage.ErrBindungPersonBelegt) {
		t.Fatalf("Bestätigen: %v, erwartet ErrBindungPersonBelegt", err)
	}
	if zst, _ := k.Zustand(ctx, "anna@example.org"); zst != storage.KontoWartet {
		t.Errorf("Konto nach Konflikt: %q", zst)
	}
}

// F-2923: `weg` folgt den Anmeldewegen — Passwort anlegen, Anbieter lösen.
func TestF2923_WegFolgtAnmeldewegen(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	id := bestaetigtesGoogleKonto(t, r, k, nutzerAnleger(db), "emil@example.org", "sub-emil")
	if z := uebersichtZeile(t, r, id); z.Weg != "google" {
		t.Fatalf("Ausgang: weg=%q", z.Weg)
	}
	if _, err := k.PasswortAnlegen(ctx, "emil@example.org", "neue-id", "$argon2id$test", "versiegelt", codeHashes(2), 5, 10); err != nil {
		t.Fatal(err)
	}
	z := uebersichtZeile(t, r, id)
	if z.Weg != "password" || !z.HatPasswort || !z.HatZweiFaktor {
		t.Errorf("nach Passwort: weg=%q pw=%v 2fa=%v", z.Weg, z.HatPasswort, z.HatZweiFaktor)
	}
	// Entsperren entscheidet weiter richtig: Konto hat Anmeldedaten → pending.
	if _, err := k.Sperren(ctx, id, 20); err != nil {
		t.Fatal(err)
	}
	if _, neuZ, err := k.Entsperren(ctx, id, 21); err != nil || neuZ != storage.KontoWartet {
		t.Errorf("Entsperren: %q %v", neuZ, err)
	}

	// Zweiter Anbieter, dann den ersten lösen: weg zeigt auf den verbliebenen.
	b := storage.NewAnbieterBindungRepo(db, nutzerAnleger(db))
	id2 := bestaetigtesGoogleKonto(t, r, k, nutzerAnleger(db), "fritz@example.org", "sub-fritz")
	if _, err := b.Verknuepfen(ctx, "fritz@example.org", "authelia", nIss, "sub-fritz-a"); err != nil {
		t.Fatal(err)
	}
	if err := b.Loesen(ctx, "fritz@example.org", "google"); err != nil {
		t.Fatal(err)
	}
	if z := uebersichtZeile(t, r, id2); z.Weg != "authelia" {
		t.Errorf("nach Lösen von google: weg=%q, erwartet authelia", z.Weg)
	}
	// Altbestand: weg google, Passwort da, einzige Bindung betreiberseitig gelöst → password.
	id3 := bestaetigtesGoogleKonto(t, r, k, nutzerAnleger(db), "gerda@example.org", "sub-gerda")
	if _, err := db.ExecContext(ctx, `UPDATE konten SET passwort_hash = 'h', totp_geheimnis = 't' WHERE id = ?`, id3); err != nil {
		t.Fatal(err)
	}
	if _, err := b.LoesenOhnePruefung(ctx, "gerda@example.org", "google"); err != nil {
		t.Fatal(err)
	}
	if z := uebersichtZeile(t, r, id3); z.Weg != "password" {
		t.Errorf("nach Lösen der letzten Bindung: weg=%q, erwartet password", z.Weg)
	}
}

// F-2924: Sperren lässt offene Links verfallen; Entsperren macht sie nicht
// wieder gültig.
func TestF2924_SperrenLaesstLinksVerfallen(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	e, err := r.Einladen(ctx, "hans@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Sperren(ctx, e.KontoID, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pruefen(ctx, e.Token); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Fatalf("Link nach Sperren: %v", err)
	}
	if _, _, err := k.Entsperren(ctx, e.KontoID, 101); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(e.Token), pwEinloesung()); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Fatalf("alter Link nach Entsperren einlösbar: %v", err)
	}
	// Erneut einladen gibt einen gültigen Link.
	neu, err := r.Einladen(ctx, "hans@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pruefen(ctx, neu.Token); err != nil {
		t.Errorf("neuer Link: %v", err)
	}

	// Zurückgesetzte Person: nach Sperren/Entsperren hilft „Neuer Link“.
	id := bestaetigtesGoogleKonto(t, r, k, nutzerAnleger(db), "ida@example.org", "sub-ida")
	rl, err := r.Zuruecksetzen(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Sperren(ctx, id, 200); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.Entsperren(ctx, id, 201); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Pruefen(ctx, rl.Token); !errors.Is(err, storage.ErrEinladungUnbekannt) {
		t.Errorf("Rücksetz-Link nach Sperren/Entsperren gültig: %v", err)
	}
	if z := uebersichtZeile(t, r, id); !z.NeuerLink {
		t.Errorf("nach Entsperren: Neuer Link muss angeboten werden")
	}
	if _, err := r.Zuruecksetzen(ctx, id); err != nil {
		t.Errorf("Neuer Link nach Entsperren: %v", err)
	}
}
