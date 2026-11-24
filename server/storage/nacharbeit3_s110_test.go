package storage_test

// Sprint-Abschluss 110, Nacharbeit 3 (Karte #1103): F-2930, F-2931.
// Testdaten nur example.org.

import (
	"context"
	"errors"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// F-2930: Aktives Konto (nie zurückgesetzt), Betreiber löst die letzte Bindung,
// Sperren, Entsperren → invited mit users-Zeile (seit F-2932 mit Rücksetz-Link).
// Dort muss „Neuer Link“ gehen (Übersicht und Zurücksetzen).
func TestF2930_CLILoesenSperrenEntsperrenNeuerLink(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	b := storage.NewAnbieterBindungRepo(db, nutzerAnleger(db))
	id := bestaetigtesGoogleKonto(t, r, k, nutzerAnleger(db), "karl@example.org", "s-karl")
	if _, err := b.LoesenOhnePruefung(ctx, "karl@example.org", "google"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Sperren(ctx, id, 10); err != nil {
		t.Fatal(err)
	}
	if _, neu, err := k.Entsperren(ctx, id, 11); err != nil || neu != storage.KontoEingeladen {
		t.Fatalf("Entsperren: %q %v", neu, err)
	}
	z := uebersichtZeile(t, r, id)
	if !z.NeuerLink || !z.HatZugang || z.Widerrufbar {
		t.Fatalf("Übersicht: neuerLink=%v hatZugang=%v widerrufbar=%v", z.NeuerLink, z.HatZugang, z.Widerrufbar)
	}
	nl, err := r.Zuruecksetzen(ctx, id)
	if err != nil {
		t.Fatalf("Zurücksetzen: %v", err)
	}
	if nl.Token == "" {
		t.Errorf("kein Link ausgegeben")
	}
	if z := uebersichtZeile(t, r, id); !z.NeuerLink || z.Widerrufbar {
		t.Errorf("danach: neuerLink=%v widerrufbar=%v", z.NeuerLink, z.Widerrufbar)
	}
	// Frische Einladung ohne users-Zeile und ohne Reset: weiter 409.
	e, err := r.Einladen(ctx, "frisch@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Zuruecksetzen(ctx, e.KontoID); !errors.Is(err, storage.ErrKontoZustand) {
		t.Errorf("frische Einladung: %v", err)
	}
}

// F-2931: Zurückgesetztes Konto ohne users-Zeile ist widerrufbar (Löschen
// verwaist nichts); mit users-Zeile bleibt 409; Übersicht.Widerrufbar folgt.
func TestF2931_ResetOhneUsersZeileWiderrufbar(t *testing.T) {
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	e, err := r.Einladen(ctx, "lea@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einloesen(ctx, storage.EinladungTokenHash(e.Token), storage.Einloesung{Anbieter: "google", Iss: nIss, Sub: "s-lea"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Zuruecksetzen(ctx, e.KontoID); err != nil {
		t.Fatal(err)
	}
	if z := uebersichtZeile(t, r, e.KontoID); !z.NeuerLink || !z.Widerrufbar || z.HatZugang {
		t.Fatalf("Übersicht: neuerLink=%v widerrufbar=%v hatZugang=%v", z.NeuerLink, z.Widerrufbar, z.HatZugang)
	}
	if _, err := r.Zurueckziehen(ctx, e.KontoID); err != nil {
		t.Fatalf("Widerrufen ohne users-Zeile: %v", err)
	}
	if _, err := k.Zustand(ctx, "lea@example.org"); err == nil {
		if z, _ := k.Zustand(ctx, "lea@example.org"); z != "" {
			t.Errorf("Konto steht noch: %q", z)
		}
	}
	// Mit users-Zeile und Rücksetz-Link: weiter 409, Konto bleibt.
	id := bestaetigtesGoogleKonto(t, r, k, nutzerAnleger(db), "max@example.org", "s-max")
	if _, err := r.Zuruecksetzen(ctx, id); err != nil {
		t.Fatal(err)
	}
	if z := uebersichtZeile(t, r, id); z.Widerrufbar || !z.NeuerLink {
		t.Errorf("mit users-Zeile: widerrufbar=%v neuerLink=%v", z.Widerrufbar, z.NeuerLink)
	}
	if _, err := r.Zurueckziehen(ctx, id); !errors.Is(err, storage.ErrEinladungPersonHatZugang) {
		t.Errorf("Widerrufen mit users-Zeile: %v", err)
	}
}
