package api_test

// Sprint-Abschluss 110, Nacharbeit (Karte #1102): Admin-API zu F-2921/F-2922.
// Testdaten nur example.org.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// F-2921: Widerrufen eines zurückgesetzten Kontos ist 409 (Person hat eine
// users-Zeile); das Konto bleibt, „Neuer Link“ (Zurücksetzen) geht.
func TestF2921_WiderrufenZurueckgesetztIst409(t *testing.T) {
	e, _, _ := kontenEnv(t, false)
	id := eingeloest(t, e, "anna@example.org")
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "bestaetigen"); c != http.StatusOK {
		t.Fatal(c)
	}
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "zuruecksetzen"); c != http.StatusOK {
		t.Fatal(c)
	}
	resp := e.do(t, http.MethodDelete, "/admin/einladungen/"+id, "admin@example.org", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("Widerrufen: %d, erwartet 409", resp.StatusCode)
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM konten WHERE id = ?`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("Konto nach 409: %d %v", n, err)
	}
	if c, out := kontoAktion(t, e, "admin@example.org", id, "zuruecksetzen"); c != http.StatusOK || out["link"] == "" {
		t.Errorf("Neuer Link: %d %v", c, out)
	}
}

// F-2922: Bestätigen mit Bindungs-Konflikt ist 409 mit lesbarer Meldung, nie 500.
func TestF2922_BestaetigenKonfliktIst409(t *testing.T) {
	e, _, _ := kontenEnv(t, false)
	id := eingeloest(t, e, "anna@example.org")
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "bestaetigen"); c != http.StatusOK {
		t.Fatal(c)
	}
	// Zurücksetzen, erneut einlösen, dann bindet ein anderes Anbieter-Konto die Person.
	r := storage.NewEinladungRepo(e.db)
	neu, err := r.Zuruecksetzen(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einloesen(context.Background(), storage.EinladungTokenHash(neu.Token),
		storage.Einloesung{Anbieter: "heim", Iss: "https://id.example.org", Sub: "sub-neu"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, user_id, angelegt)
		SELECT 'https://id.example.org', 'sub-fremd', 'heim', NULL, id, 1 FROM users WHERE email = 'anna@example.org'`); err != nil {
		t.Fatal(err)
	}
	if c, out := kontoAktion(t, e, "admin@example.org", id, "bestaetigen"); c != http.StatusConflict {
		t.Fatalf("Bestätigen: %d %v, erwartet 409", c, out)
	}
}

// F-2927: Die 409-Meldung beim Bestätigen rät nicht zu „ablehnen“ (das scheitert
// dort immer, die Person hat eine users-Zeile), sondern zu Bindung lösen oder Zurücksetzen.
func TestF2927_BestaetigenKonfliktMeldungOhneAblehnen(t *testing.T) {
	e, _, _ := kontenEnv(t, false)
	id := eingeloest(t, e, "anna@example.org")
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "bestaetigen"); c != http.StatusOK {
		t.Fatal(c)
	}
	r := storage.NewEinladungRepo(e.db)
	neu, err := r.Zuruecksetzen(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Einloesen(context.Background(), storage.EinladungTokenHash(neu.Token),
		storage.Einloesung{Anbieter: "heim", Iss: "https://id.example.org", Sub: "sub-neu"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, user_id, angelegt)
		SELECT 'https://id.example.org', 'sub-fremd', 'heim', NULL, id, 1 FROM users WHERE email = 'anna@example.org'`); err != nil {
		t.Fatal(err)
	}
	c, out := kontoAktion(t, e, "admin@example.org", id, "bestaetigen")
	msg, _ := out["error"].(string)
	if c != http.StatusConflict || msg == "" {
		t.Fatalf("Bestätigen: %d %v", c, out)
	}
	if strings.Contains(strings.ToLower(msg), "reject") || strings.Contains(strings.ToLower(msg), "ablehn") {
		t.Errorf("Meldung rät zum Ablehnen: %q", msg)
	}
	if !strings.Contains(msg, "bindung-loesen") || !strings.Contains(msg, "reset") {
		t.Errorf("Meldung nennt die Wege nicht: %q", msg)
	}
	// Gegenprobe der Behauptung: Ablehnen scheitert dort tatsächlich.
	if c, _ := kontoAktion(t, e, "admin@example.org", id, "ablehnen"); c != http.StatusConflict {
		t.Errorf("Ablehnen: %d, erwartet 409", c)
	}
}

// F-2926: Eine frische Einladung einer Person mit users-Zeile (Vorab-Liste)
// lässt sich über die Admin-API widerrufen (204); die users-Zeile bleibt.
func TestF2926_WiderrufenFrischeEinladungMitUsersZeile(t *testing.T) {
	e, _, _ := kontenEnv(t, false)
	r := storage.NewEinladungRepo(e.db)
	n, err := r.Einladen(context.Background(), "gast@example.org")
	if err != nil {
		t.Fatal(err)
	}
	b := storage.NewBootstrapRepo(e.db)
	if _, err := b.BootstrapNewUser(context.Background(), "u-gast", "b-gast", "gast@example.org", ""); err != nil {
		t.Fatal(err)
	}
	resp := e.do(t, http.MethodDelete, "/admin/einladungen/"+n.KontoID, "admin@example.org", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Widerrufen: %d, erwartet 204", resp.StatusCode)
	}
	var u int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = 'gast@example.org'`).Scan(&u); err != nil || u != 1 {
		t.Errorf("users-Zeile: %d %v", u, err)
	}
}
