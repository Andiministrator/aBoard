package storage_test

// Sprint-Abschluss 110, Nacharbeit 4 (Karte #1103): F-2932, F-2933.
// Testdaten nur example.org.

import (
	"context"
	"errors"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// entsperrtOhneAnmeldedaten: aktives Konto, Betreiber löst die letzte Bindung,
// Sperren, Entsperren → invited, users-Zeile vorhanden.
func entsperrtOhneAnmeldedaten(t *testing.T, adr, sub string) (*storage.EinladungRepo, *storage.KontoRepo, string) {
	t.Helper()
	ctx := context.Background()
	db, r, _ := einladungDB(t)
	k := storage.NewKontoRepo(db)
	b := storage.NewAnbieterBindungRepo(db, nutzerAnleger(db))
	id := bestaetigtesGoogleKonto(t, r, k, nutzerAnleger(db), adr, sub)
	if _, err := b.LoesenOhnePruefung(ctx, adr, "google"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Sperren(ctx, id, 10); err != nil {
		t.Fatal(err)
	}
	if _, neu, err := k.Entsperren(ctx, id, 11); err != nil || neu != storage.KontoEingeladen {
		t.Fatalf("Entsperren: %q %v", neu, err)
	}
	return r, k, id
}

// F-2932: Entsperren ohne Anmeldedaten legt einen Rücksetz-Link an: Widerrufen
// ist bei users-Zeile gesperrt (Person bliebe sonst nie wieder einladbar),
// „Neuer Link“ geht.
func TestF2932_EntsperrenOhneAnmeldedatenWiderrufenGesperrt(t *testing.T) {
	ctx := context.Background()
	r, _, id := entsperrtOhneAnmeldedaten(t, "otto@example.org", "s-otto")
	z := uebersichtZeile(t, r, id)
	if !z.NeuerLink || z.Widerrufbar || !z.HatZugang {
		t.Fatalf("Übersicht: neuerLink=%v widerrufbar=%v hatZugang=%v", z.NeuerLink, z.Widerrufbar, z.HatZugang)
	}
	if _, err := r.Zurueckziehen(ctx, id); !errors.Is(err, storage.ErrEinladungPersonHatZugang) {
		t.Errorf("Widerrufen: %v, erwartet ErrEinladungPersonHatZugang", err)
	}
	if _, err := r.Zuruecksetzen(ctx, id); err != nil {
		t.Errorf("Neuer Link: %v", err)
	}
}

// F-2933: Nach dem Entsperren ist die Vorab-Liste für das Konto ausgesetzt
// (wie nach Zurücksetzen): keine Anmeldung ohne Bestätigung.
func TestF2933_EntsperrenOhneAnmeldedatenSetztListeAus(t *testing.T) {
	ctx := context.Background()
	_, k, _ := entsperrtOhneAnmeldedaten(t, "paul@example.org", "s-paul")
	if ja, err := k.VorabListeAusgesetzt(ctx, "paul@example.org"); err != nil || !ja {
		t.Errorf("Liste ausgesetzt: %v %v", ja, err)
	}
}
