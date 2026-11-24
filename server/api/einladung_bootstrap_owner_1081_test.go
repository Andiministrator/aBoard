package api

// Karte #1081, Prüfer B: Regressionstest zum Befund „Bestätigung ohne Admin
// trotz ABOARD_BOOTSTRAP_OWNER“ (gegen aafb5ac0 rot). Testdaten nur example.org/example.com.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// einloesenPasswort löst token über den Passwort-Weg ganz ein.
func (x *einloeseTest) einloesenPasswort(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	g, siegel := x.schritt1(t, token)
	return x.post(t, token, url.Values{"weg": {"password"}, "schritt": {"code"}, "einrichtung": {siegel},
		"code": {totpCode(g, TOTPSchritt(x.jetzt))}})
}

// ---------------------------------------------------------------------------
// Server ohne Admin, ABOARD_BOOTSTRAP_OWNER zeigt auf eine andere Person.
//
// ADR-0149 §5.7: „Die eigene Einladung des ersten Admins gilt als bestätigt.“
// Nachtrag #1076: „Gibt es noch keinen Admin, ist die eingelöste Einladung
// bestätigt, und BootstrapNewUser macht die Person zum Erstadmin.“ Mit
// gesetztem ABOARD_BOOTSTRAP_OWNER macht BootstrapNewUser aber nur DIESE Person
// zum Admin (F-1855). Jede andere Einladung, die vorher eingelöst wird, ist
// dann ohne Bestätigung aktiv, und es bleibt „kein Admin“ — der nächste
// weitergeleitete Link wird ebenso still aktiv.
func TestPrueferB1081_BootstrapOwnerAndererKeinAutoBestaetigen(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	t.Setenv("ABOARD_BOOTSTRAP_OWNER", "chef@example.org")
	ben := x.einladen(t, "ben@example.org")
	if rec := x.einloesenPasswort(t, ben); rec.Code != http.StatusOK {
		t.Fatalf("Einlösen: %d %s", rec.Code, rec.Body.String())
	}
	var admins int
	if err := x.db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if z := x.zustand(t, "ben@example.org"); z != storage.KontoWartet {
		t.Errorf("Ben (nicht der Bootstrap-Owner) ist nach dem Einlösen %q, erwartet pending — "+
			"bestätigt ohne Admin, obwohl er nicht Erstadmin wird (Admins: %d)", z, admins)
	}
	if ok, _ := x.tor.Erlaubt(context.Background(), "ben@example.org"); ok {
		t.Errorf("Tor lässt Ben ohne Bestätigung herein")
	}
	// Der Owner selbst wird weiter ohne Bestätigung Admin.
	chef := x.einladen(t, "chef@example.org")
	if rec := x.einloesenPasswort(t, chef); rec.Code != http.StatusOK {
		t.Fatalf("Einlösen Chef: %d", rec.Code)
	}
	if z := x.zustand(t, "chef@example.org"); z != storage.KontoAktiv {
		t.Errorf("Bootstrap-Owner nach dem Einlösen %q, erwartet active", z)
	}
}

// Ungültiger ABOARD_BOOTSTRAP_OWNER (Tippfehler): Niemand wird je Admin — dann
// darf auch nie eine Einladung ohne Bestätigung aktiv werden.
func TestPrueferB1081_BootstrapOwnerUngueltigKeinAutoBestaetigen(t *testing.T) {
	x := neuerEinloeseTest(t, "")
	t.Setenv("ABOARD_BOOTSTRAP_OWNER", "chef-ohne-at.example.org")
	for _, e := range []string{"ben@example.org", "carla@example.org"} {
		if rec := x.einloesenPasswort(t, x.einladen(t, e)); rec.Code != http.StatusOK {
			t.Fatalf("Einlösen %s: %d", e, rec.Code)
		}
		if z := x.zustand(t, e); z != storage.KontoWartet {
			t.Errorf("%s nach dem Einlösen %q, erwartet pending (es gibt keinen Admin und wird keinen geben)", e, z)
		}
	}
}
