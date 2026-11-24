package boardbackup

import (
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// F-1517: Domain-Freigaben (ADR-0132) sind keine Nutzer und zählen im
// Manifest nicht mit.
func TestPersonenZahl_OhneDomainEintraege(t *testing.T) {
	got := personenZahl([]storage.ACLEntry{
		{UserEmail: "a@firma.de"},
		{UserEmail: "@firma.de"},
		{UserEmail: "b@firma.de"},
	})
	if got != 2 {
		t.Fatalf("personenZahl = %d, erwartet 2", got)
	}
}
