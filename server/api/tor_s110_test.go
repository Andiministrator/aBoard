package api

// Sprint-Abschluss 110, Nacharbeit (Karte #1102), F-2922: Das Tor lässt eine
// Person der Vorab-Liste nicht herein, solange ihr Konto wartet oder
// zurückgesetzt ist. Testdaten nur example.org.

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
	"github.com/Andiministrator/aBoard/server/uhr"
)

func TestF2922_TorVorabListeGegenKontoZustand(t *testing.T) {
	t.Setenv(uhr.PruefungEnvVar, "aus")
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	er := storage.NewEinladungRepo(db)
	k := storage.NewKontoRepo(db)
	bs := storage.NewBootstrapRepo(db)
	nutzer := func(ctx context.Context, email string) error {
		_, err := bs.BootstrapNewUser(ctx, "u-"+email, "b-"+email, email, "")
		return err
	}
	liste, err := NeueZulassung("anna@example.org, dora@example.org, pia@example.org", "")
	if err != nil {
		t.Fatal(err)
	}
	tor := NeuesTor(liste, k)
	tor.TTL = time.Nanosecond // kein Cache zwischen den Schritten
	erlaubt := func(adr string) bool {
		t.Helper()
		ok, err := tor.Erlaubt(ctx, adr)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	// Anna: eingelöst, bestätigt, dann zurückgesetzt.
	e, err := er.Einladen(ctx, "anna@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := er.Einloesen(ctx, storage.EinladungTokenHash(e.Token), storage.Einloesung{Anbieter: "google", Iss: "https://idp.example.org", Sub: "s-anna"}); err != nil {
		t.Fatal(err)
	}
	if erlaubt("anna@example.org") { // pending: Liste gilt nicht
		t.Errorf("pending auf der Liste darf nicht herein")
	}
	if _, err := k.Bestaetigen(ctx, e.KontoID, nutzer, 1); err != nil {
		t.Fatal(err)
	}
	if !erlaubt("anna@example.org") {
		t.Errorf("aktiv: muss herein")
	}
	if _, err := er.Zuruecksetzen(ctx, e.KontoID); err != nil {
		t.Fatal(err)
	}
	if erlaubt("anna@example.org") {
		t.Errorf("zurückgesetzt (invited, reset) auf der Liste darf nicht herein")
	}

	// Dora: frische Einladung (Zweck invite) bleibt durch die Liste gedeckt.
	if _, err := er.Einladen(ctx, "dora@example.org"); err != nil {
		t.Fatal(err)
	}
	if !erlaubt("dora@example.org") {
		t.Errorf("frische Einladung einer gelisteten Person: Liste muss gelten")
	}
	// Pia: ohne Konto, nur Liste.
	if !erlaubt("pia@example.org") {
		t.Errorf("nur Liste: muss herein")
	}
}
