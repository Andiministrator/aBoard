package api

// F-2911 (Sicherheitsprüfung #1081): vier kleine Härtungen der Anmeldeseiten.
// Testdaten nur example.org/example.com.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Teil 1: Alle Anmelde- und Einlöse-Seiten tragen nosniff (sie laufen nicht
// durch WithDefaults).
func TestF2911_Nosniff(t *testing.T) {
	rec := httptest.NewRecorder()
	anmeldeSeiteAnsicht(rec, http.StatusOK, seitenAnsicht{Lang: "de", Titel: "T", Text: "x"})
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("anmeldeSeiteAnsicht: X-Content-Type-Options = %q", got)
	}
	x := neuerEinloeseTest(t, "")
	rec = x.hole(t, EinladungPfadPraefix+"gibt-es-nicht", nil)
	if rec.Code != http.StatusNotFound || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("Einlöse-Seite: %d, nosniff %q", rec.Code, rec.Header().Get("X-Content-Type-Options"))
	}
}

// Teil 2: Weist die Vorprüfung ab, wird nicht belegt — und läuft die Wartezeit
// genau zwischen Vor- und Zweitprüfung ab, darf die Anfrage nicht ohne Belegung
// bis zur Code-Prüfung durchfallen (früher: 303 mit Sitzung). Die Uhr der
// Wartezeit springt nach dem ersten Lesen über das Ende der Wartezeit.
func TestF2911_VorabBelegt(t *testing.T) {
	ct := neuerCodeTest(t)
	c := ct.passwort(t)
	start := ct.uhr.t
	ct.p.adressen.fehlschlag("anna@example.org") // Wartezeit 1 s
	lesen := 0
	ct.p.adressen.jetzt = func() time.Time {
		lesen++
		if lesen == 1 {
			return start // Vorprüfung: noch in der Wartezeit
		}
		return start.Add(30 * time.Second) // danach abgelaufen
	}
	rec := ct.code(c, ct.aktuellerCode(), "192.0.2.1")
	if rec.Code != http.StatusTooManyRequests || hatSitzung(rec) {
		t.Fatalf("abgewiesene Vorprüfung: %d %s — erwartet 429 ohne Sitzung", rec.Code, rec.Body.String())
	}
	if len(ct.sitzungen.angelegt) != 0 {
		t.Fatalf("Sitzung trotz abgewiesener Vorprüfung: %v", ct.sitzungen.angelegt)
	}
	// Die Belegung ist nach der Antwort wieder frei (kein Hängenbleiben).
	if n := len(ct.p.laufend); n != 0 {
		t.Fatalf("Belegung bleibt hängen: %v", ct.p.laufend)
	}
	// Gegenprobe: Ohne Wartezeit geht derselbe Code durch.
	ct.p.adressen.jetzt = func() time.Time { return start.Add(time.Hour) }
	ct.uhr.t = start.Add(time.Hour)
	c = ct.passwort(t)
	if rec := ct.code(c, ct.aktuellerCode(), "192.0.2.1"); rec.Code != http.StatusSeeOther || !hatSitzung(rec) {
		t.Fatalf("Gegenprobe: %d %s", rec.Code, rec.Body.String())
	}
}

// Teil 3 / F-2961: Eine vom Anbieter nicht bestätigte Adresse wird rein
// gespeichert, das Flag gemeldete_email_unbestaetigt trägt den Zustand (und die
// Admin-Liste liefert es als eigenes Feld).
func TestF2911_GemeldetUnbestaetigt(t *testing.T) {
	gemeldet := func(t *testing.T, sub string, claims map[string]any) (string, bool) {
		t.Helper()
		x := neuerEinloeseTest(t, "")
		x.mitAdmin()
		tok := x.einladen(t, "anna@example.org")
		c, q := inviteZustand(t, x.hole(t, EinladungPfadPraefix+tok+"?weg=google", nil))
		claims["sub"] = sub
		if rec := x.melde(t, x.google, "google", c, q, "u1", claims); rec.Code != http.StatusOK {
			t.Fatalf("Rückruf: %d %s", rec.Code, rec.Body.String())
		}
		var g string
		var u bool
		if err := x.db.QueryRow(`SELECT gemeldete_email, gemeldete_email_unbestaetigt FROM konten WHERE email = 'anna@example.org'`).Scan(&g, &u); err != nil {
			t.Fatal(err)
		}
		return g, u
	}
	if g, u := gemeldet(t, "s-a", map[string]any{"email": "anna@example.org", "email_verified": false}); g != "anna@example.org" || !u {
		t.Errorf("unbestätigt, eingeladene Adresse: %q %v", g, u)
	}
	if g, u := gemeldet(t, "s-b", map[string]any{"email": "anna@example.org", "email_verified": "false"}); g != "anna@example.org" || !u {
		t.Errorf("email_verified als Zeichenkette \"false\": %q %v", g, u)
	}
	if g, u := gemeldet(t, "s-c", map[string]any{"email": "anna@example.org", "email_verified": true}); g != "anna@example.org" || u {
		t.Errorf("bestätigt: %q %v", g, u)
	}
	if g, u := gemeldet(t, "s-d", map[string]any{"email": "", "email_verified": false}); g != "" || u {
		t.Errorf("leere Adresse bleibt leer, ohne Flag: %q %v", g, u)
	}
}
