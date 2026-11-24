package api_test

// Karte #795 (ADR-0140 §11): Die Außenstelle ist zurückgebaut. Was davon
// übrig bleiben darf und was nicht:
//   - Ein Bestand-Abo „für außen“ öffnet den vollen Feed weiterhin NICHT
//     (fail-closed; es war als datensparsamer Feed angelegt) und steht in
//     der Abo-Liste nur noch zum Löschen — ohne Kennung, ohne Adresse.
//   - Neue Abos „für außen“ werden abgewiesen (410), nicht still als volle
//     Abos angelegt.
//   - /ical/aussen/* und /me/ical/aussenstelle gibt es nicht mehr; ein
//     abk_-Token ist am Agenten-Eingang ein unbekanntes Token.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func TestICalBestand795(t *testing.T) {
	e := newEnv748(t)
	ctx := context.Background()
	var annaID string
	if err := e.db.QueryRow(`SELECT id FROM users WHERE email = 'anna@firma.de'`).Scan(&annaID); err != nil {
		t.Fatal(err)
	}
	normal, err := e.tokens.Create(ctx, annaID, nil)
	if err != nil {
		t.Fatal(err)
	}
	bestand, err := e.tokens.Create(ctx, annaID, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Wie ein Abo „für außen“ aus der Zeit der Außenstelle (Migration 0030).
	if _, err := e.db.Exec(`UPDATE ical_subscriptions SET aussen_hash = 'h', aussen_enc = 'e' WHERE id = ?`, bestand.ID); err != nil {
		t.Fatal(err)
	}

	if s, _ := e.anfrage(t, e.mensch, "GET", "/ical/calendar.ics?token="+normal.Token, "", ""); s != http.StatusOK {
		t.Fatalf("normaler Feed: %d", s)
	}
	if s, b := e.anfrage(t, e.mensch, "GET", "/ical/calendar.ics?token="+bestand.Token, "", ""); s != http.StatusNotFound {
		t.Fatalf("Bestand „für außen“ öffnet den vollen Feed: %d %s", s, b)
	}

	s, b := e.anfrage(t, e.mensch, "GET", "/me/ical/subscriptions", "anna@firma.de", "")
	if s != http.StatusOK {
		t.Fatalf("Liste: %d %s", s, b)
	}
	var liste []map[string]any
	if err := json.Unmarshal([]byte(b), &liste); err != nil || len(liste) != 2 {
		t.Fatalf("Liste: %v %s", err, b)
	}
	for _, a := range liste {
		if a["id"] != bestand.ID {
			continue
		}
		if a["aussen"] != true || a["path"] != "" || a["defekt"] != nil {
			t.Errorf("Bestand in der Liste: %v", a)
		}
		for _, weg := range []string{"kennung", "aussen_url"} {
			if _, da := a[weg]; da {
				t.Errorf("Bestand trägt noch %s: %v", weg, a)
			}
		}
	}

	if s, b := e.anfrage(t, e.mensch, "POST", "/me/ical/subscriptions", "anna@firma.de", `{"aussen":true}`); s != http.StatusGone {
		t.Errorf("neues Abo „für außen“: %d %s, erwartet 410", s, b)
	}
	// Sitzungs-QA #748 C-4 (aus dem gelöschten Außen-Test übernommen): ein
	// nicht lesbarer Body legt kein Abo an, schon gar keins mit vollem Feed.
	if s, b := e.anfrage(t, e.mensch, "POST", "/me/ical/subscriptions", "anna@firma.de", `{"aussen":"ja"}`); s != http.StatusBadRequest {
		t.Errorf(`{"aussen":"ja"}: %d %s, erwartet 400`, s, b)
	}
	if n := zaehleAbos795(t, e); n != 2 {
		t.Errorf("nach abgewiesenem Anlegen %d Abos, erwartet 2", n)
	}

	for _, pfad := range []string{"/ical/aussen/", "/ical/aussen/" + strings.Repeat("a", 32) + ".ics", "/me/ical/aussenstelle"} {
		if s, _ := e.anfrage(t, e.mensch, "GET", pfad, "anna@firma.de", ""); s != http.StatusNotFound {
			t.Errorf("GET %s angemeldet: %d, erwartet 404", pfad, s)
		}
		// Ohne Anmeldung kein offener Weg mehr (vorher an der Anmeldung vorbei).
		if s, _ := e.anfrage(t, e.mensch, "GET", pfad, "", ""); s != http.StatusUnauthorized {
			t.Errorf("GET %s ohne Anmeldung: %d, erwartet 401", pfad, s)
		}
	}
	if s, _ := e.anfrage(t, e.agent, "GET", "/ical/aussen/", "abk_"+strings.Repeat("x", 43), ""); s != http.StatusUnauthorized {
		t.Errorf("abk_-Token am Agenten-Eingang: %d, erwartet 401", s)
	}

	if s, _ := e.anfrage(t, e.mensch, "DELETE", "/me/ical/subscriptions/"+bestand.ID, "anna@firma.de", ""); s != http.StatusNoContent {
		t.Fatalf("Bestand löschen: %d", s)
	}
	if n := zaehleAbos795(t, e); n != 1 {
		t.Errorf("nach Löschen %d Abos, erwartet 1", n)
	}
}

func zaehleAbos795(t *testing.T, e *env748) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM ical_subscriptions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Nebenprüfung aus dem gelöschten TestKalender1890 (F-1890): Der volle Feed
// der eigenen Kalender-App zeigt Karten des Service-Boards weiter, wenn die
// Person dort Rechte hat — gefiltert hatte nur der Feed „für außen“.
func TestICalVollerFeedMitServiceBoard795(t *testing.T) {
	e := newEnv748(t)
	ctx := context.Background()
	svc := `{"version":"2","board":{"id":"` + storage.ServiceBoardID + `","title":"Service","nextDisplayId":2,
	  "columnDefs":[{"id":"col-ready","title":"Ready"}],
	  "sprints":[{"id":"s1","title":"S","kind":"service","cards":[
	    {"id":"mail-k","displayId":1,"title":"Betreff einer Kundenmail","columnId":"col-ready","dueDate":"2026-10-21","comments":[]}]}]}}`
	if _, err := e.boards.Get(ctx, storage.ServiceBoardID); err != nil {
		if _, err := e.boards.Insert(ctx, storage.ServiceBoardID, "Service", "2", []byte(svc)); err != nil {
			t.Fatal(err)
		}
	} else if _, err := e.db.Exec(`UPDATE boards SET state_json = ? WHERE id = ?`, svc, storage.ServiceBoardID); err != nil {
		t.Fatal(err)
	}
	if err := e.acl.Set(ctx, storage.ServiceBoardID, "anna@firma.de", "editor"); err != nil {
		t.Fatal(err)
	}
	abo, err := e.tokens.Create(ctx, "u-anna", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, voll := e.anfrage(t, e.mensch, "GET", "/ical/calendar.ics?token="+abo.Token, "", ""); s != 200 || !strings.Contains(voll, "Kundenmail") || !strings.Contains(voll, "UID:k1@b1.aboard") {
		t.Fatalf("voller Feed: %d %s", s, voll)
	}
}
