package storage_test

// Karte #788 (ADR-0140 §4): Partnerliste außen, Prüfung der Adresse des
// internen Gegenstücks, lesbare Fehler beim Koppeln, Widerruf per Name.

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func boardpaarTestDB(t *testing.T) (*storage.BoardpaarRepo, *storage.BoardRepo) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, storage.Config{DBPath: filepath.Join(t.TempDir(), "aboard.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	boards := storage.NewBoardRepo(db)
	for _, id := range []string{"aussen1", "aussen2", storage.ServiceBoardID} {
		if _, err := boards.Insert(ctx, id, "B", "2", []byte(`{"version":"2","board":{"id":"`+id+`","sprints":[]}}`)); err != nil {
			t.Fatal(err)
		}
	}
	return storage.NewBoardpaarRepo(db), boards
}

func TestBoardpaar788_PruefeInnenLinkBasis(t *testing.T) {
	gut := map[string]string{
		"https://board.example.org/#board=abc":           "https://board.example.org/#board=abc",
		"  https://innen.example#board=abc  ":            "https://innen.example/#board=abc",
		"https://innen.example:8443/pfad/#board=a-b_c.1": "https://innen.example:8443/pfad/#board=a-b_c.1",
		"https://innen.example/#board=0b7c-4f~x":         "https://innen.example/#board=0b7c-4f~x",
		"https://innen.example:1/#board=x":               "https://innen.example:1/#board=x",
	}
	for roh, want := range gut {
		got, err := storage.PruefeInnenLinkBasis(roh)
		if err != nil || got != want {
			t.Errorf("PruefeInnenLinkBasis(%q) = %q, %v; erwartet %q", roh, got, err, want)
		}
	}
	schlecht := []string{
		"",
		"http://innen.example/#board=abc",          // kein https
		"https://innen.example/",                   // kein Fragment
		"https://innen.example/#board=",            // leere ID
		"https://innen.example/#board=a&card=1",    // Karte gehört nicht in die Basis
		"https://innen.example/#card-1",            // falsches Fragment
		"https://innen.example/?x=1#board=abc",     // Abfrage
		"https://nutzer:pw@innen.example/#board=a", // Zugangsdaten
		"https:///#board=abc",                      // kein Host
		"https://innen.example/#board=a%20b",       // Leerzeichen in der ID
		"https://innen.example/#board=a&board=b",   // doppelt
		"javascript:alert(1)//#board=a",            // kein https
		"https://innen.example/#board=a%0Ab",       // Steuerzeichen
		"https://innen.example/#board=a%2Fb",       // Schrägstrich in der ID
		"https://innen.example/#board=x#y",         // zweites # (K-1)
		"https://innen.example:99999/#board=x",     // Port ausserhalb (K-1)
		"https://innen.example:0/#board=x",         // Port 0
	}
	for _, roh := range schlecht {
		if got, err := storage.PruefeInnenLinkBasis(roh); !errors.Is(err, storage.ErrBoardpaarLinkBasis) {
			t.Errorf("PruefeInnenLinkBasis(%q) = %q, %v; erwartet ErrBoardpaarLinkBasis", roh, got, err)
		}
	}
}

func TestBoardpaar788_AussenKoppeln(t *testing.T) {
	ctx := context.Background()
	paar, boards := boardpaarTestDB(t)

	if _, err := paar.AussenKoppeln(ctx, "gibtsnicht", "https://i.example/#board=x", "admin"); !errors.Is(err, storage.ErrBoardpaarBoardUnbekannt) {
		t.Errorf("unbekanntes Board: %v", err)
	}
	if _, err := paar.AussenKoppeln(ctx, storage.ServiceBoardID, "https://i.example/#board=x", "admin"); !errors.Is(err, storage.ErrBoardpaarNichtFreigebbar) {
		t.Errorf("Service-Board: %v", err)
	}
	if _, err := paar.AussenKoppeln(ctx, "aussen1", "http://i.example/#board=x", "admin"); !errors.Is(err, storage.ErrBoardpaarLinkBasis) {
		t.Errorf("schlechte Adresse: %v", err)
	}
	e, err := paar.AussenKoppeln(ctx, "aussen1", "https://i.example#board=x", "admin")
	if err != nil || e.InnenLinkBasis != "https://i.example/#board=x" || e.Von != "admin" || e.Seit == "" {
		t.Fatalf("koppeln: %+v, %v", e, err)
	}
	// Erneut koppeln ändert nur die Adresse; seit und von bleiben.
	e2, err := paar.AussenKoppeln(ctx, "aussen1", "https://i.example/#board=y", "anderer")
	if err != nil || e2.InnenLinkBasis != "https://i.example/#board=y" || e2.Seit != e.Seit || e2.Von != "admin" {
		t.Fatalf("neu koppeln: %+v, %v", e2, err)
	}
	if g, err := paar.AussenGekoppelt(ctx, "aussen1"); err != nil || g == nil {
		t.Fatalf("AussenGekoppelt aussen1: %+v, %v", g, err)
	}
	if g, err := paar.AussenGekoppelt(ctx, "aussen2"); err != nil || g != nil {
		t.Fatalf("AussenGekoppelt aussen2: %+v, %v", g, err)
	}
	if _, err := paar.AussenKoppeln(ctx, "aussen2", "https://i.example/#board=z", "admin"); err != nil {
		t.Fatal(err)
	}
	l, err := paar.AussenListe(ctx)
	if err != nil || len(l) != 2 || l[0].BoardID != "aussen1" || l[1].BoardID != "aussen2" {
		t.Fatalf("Liste: %+v, %v", l, err)
	}
	if err := paar.AussenEntkoppeln(ctx, "aussen1"); err != nil {
		t.Fatal(err)
	}
	if err := paar.AussenEntkoppeln(ctx, "aussen1"); !errors.Is(err, storage.ErrBoardpaarNichtGekoppelt) {
		t.Errorf("zweites Entkoppeln: %v", err)
	}
	// Board löschen löscht die Kopplung mit (Fremdschlüssel, ADR-0140 §1).
	if err := boards.Delete(ctx, "aussen2"); err != nil {
		t.Fatal(err)
	}
	if l, _ := paar.AussenListe(ctx); len(l) != 0 {
		t.Errorf("Kopplung überlebt das Löschen des Boards: %+v", l)
	}
}

func TestBoardpaar788_InnenKoppelnLesbareFehler(t *testing.T) {
	ctx := context.Background()
	paar, _ := boardpaarTestDB(t)
	if err := paar.InnenKoppeln(ctx, "", "admin"); !errors.Is(err, storage.ErrBoardpaarBoardFehlt) {
		t.Errorf("leere Board-ID: %v", err)
	}
	if err := paar.InnenKoppeln(ctx, "gibtsnicht", "admin"); !errors.Is(err, storage.ErrBoardpaarBoardUnbekannt) {
		t.Errorf("unbekanntes Board: %v", err)
	}
	if err := paar.InnenKoppeln(ctx, storage.ServiceBoardID, "admin"); !errors.Is(err, storage.ErrBoardpaarNichtFreigebbar) {
		t.Errorf("Service-Board: %v", err)
	}
	if err := paar.InnenKoppeln(ctx, "aussen1", "admin"); err != nil {
		t.Errorf("gültiges Board: %v", err)
	}
}

func TestBoardpaar788_TokenAktivNachName(t *testing.T) {
	ctx := context.Background()
	paar, _ := boardpaarTestDB(t)
	_, alt, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := paar.TokenWiderrufen(ctx, alt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := paar.TokenAktivNachName(ctx, storage.BoardpaarTypAusgang, "push"); !errors.Is(err, storage.ErrBoardpaarTokenUnbekannt) {
		t.Errorf("widerrufenes Token gefunden: %v", err)
	}
	_, neu, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypAusgang, "push", "cli")
	if err != nil {
		t.Fatal(err)
	}
	// Gleicher Name, anderer Typ: zählt nicht.
	if _, _, err := paar.TokenAnlegen(ctx, storage.BoardpaarTypEingang, "push", "cli"); err != nil {
		t.Fatal(err)
	}
	got, err := paar.TokenAktivNachName(ctx, storage.BoardpaarTypAusgang, " push ")
	if err != nil || got.ID != neu.ID {
		t.Fatalf("TokenAktivNachName: %+v, %v; erwartet %s", got, err, neu.ID)
	}
	if _, err := paar.TokenAktivNachName(ctx, "xyz", "push"); !errors.Is(err, storage.ErrBoardpaarTyp) {
		t.Errorf("unbekannter Typ: %v", err)
	}
}
