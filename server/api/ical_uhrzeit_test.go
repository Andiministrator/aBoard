package api_test

// Uhrzeiten im Abo-Feed über den echten Handler (ADR-0138 §1–§3, Karte #742):
// Feld-Durchreichung aus dem Board-Zustand, Instanz-Zeitzone aus
// server_config, Wechsel der Zone ohne Neustart, kaputte Zone → UTC.

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

func (e *icalTestEnv) seedBoardMitUhrzeit(t *testing.T, boardID, ownerEmail string) {
	t.Helper()
	state := `{"version":"2","board":{"id":"` + boardID + `","title":"B","sprints":[
		{"id":"sp1","title":"S","kind":"sprint","cards":[
			{"id":"c1","displayId":1,"title":"Kundentermin","columnId":"col-ready","startDate":"2026-10-08","startTime":"10:00","dueDate":"2026-10-08","dueTime":"11:30"},
			{"id":"c2","displayId":2,"title":"Frist","columnId":"col-ready","dueDate":"2026-10-09","dueTime":"17:00"},
			{"id":"c3","displayId":3,"title":"Verwaist","columnId":"col-ready","dueDate":"2026-10-10","startTime":"08:00"},
			{"id":"c4","displayId":4,"title":"Ganztags","columnId":"col-ready","dueDate":"2026-10-11"}
		]}
	]}}`
	if _, err := e.boards.Insert(context.Background(), boardID, "B", "2", []byte(state)); err != nil {
		t.Fatalf("Insert board: %v", err)
	}
	if err := e.acl.Set(context.Background(), boardID, ownerEmail, storage.RoleOwner); err != nil {
		t.Fatalf("acl.Set: %v", err)
	}
}

func (e *icalTestEnv) feed(t *testing.T, token string) string {
	t.Helper()
	resp, err := http.Get(e.srv.URL + "/ical/calendar.ics?token=" + token)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Feed: status=%d", resp.StatusCode)
	}
	return string(body)
}

// eventVon schneidet das VEVENT mit der UID der Karte aus.
func eventVon(t *testing.T, ics, cardID string) string {
	t.Helper()
	i := strings.Index(ics, "UID:"+cardID+"@")
	if i < 0 {
		t.Fatalf("Event %s fehlt:\n%s", cardID, ics)
	}
	j := strings.Index(ics[i:], "END:VEVENT")
	return ics[i : i+j]
}

func TestICalAPI_FeedUhrzeiten(t *testing.T) {
	env := newICalEnv(t, true)
	defer env.close()
	env.seedUser(t, "u1", "andi@example.com")
	env.seedBoardMitUhrzeit(t, "a", "andi@example.com")
	token := env.feedToken(t, "andi@example.com", map[string]any{})

	// Ohne Eintrag gilt Europe/Berlin (Oktober: UTC+2).
	ics := env.feed(t, token)
	for _, f := range []struct{ card, muss, darfNicht string }{
		{"c1", "DTSTART:20261008T080000Z\r\nDTEND:20261008T093000Z", "VALUE=DATE"},
		{"c2", "DTSTART:20261009T150000Z", "DTEND"},
		// startTime ohne startDate darf nicht am dueDate greifen → ganztags.
		{"c3", "DTSTART;VALUE=DATE:20261010", "T060000Z"},
		{"c4", "DTSTART;VALUE=DATE:20261011\r\nDTEND;VALUE=DATE:20261012", ""},
	} {
		ev := eventVon(t, ics, f.card)
		if !strings.Contains(ev, f.muss) {
			t.Errorf("%s: erwartet %q in\n%s", f.card, f.muss, ev)
		}
		if f.darfNicht != "" && strings.Contains(ev, f.darfNicht) {
			t.Errorf("%s: %q darf nicht vorkommen:\n%s", f.card, f.darfNicht, ev)
		}
	}

	// Zone ändern wirkt beim nächsten Abruf, ohne Neustart.
	if err := env.cfg.Set(context.Background(), "kalender.zeitzone", "America/New_York", false, "u1"); err != nil {
		t.Fatalf("cfg.Set: %v", err)
	}
	if ev := eventVon(t, env.feed(t, token), "c1"); !strings.Contains(ev, "DTSTART:20261008T140000Z") {
		t.Errorf("New York: erwartet 14:00 UTC:\n%s", ev)
	}

	// Unbekannte Zone → UTC, der Feed läuft weiter.
	if err := env.cfg.Set(context.Background(), "kalender.zeitzone", "Mars/Olympus", false, "u1"); err != nil {
		t.Fatalf("cfg.Set: %v", err)
	}
	if ev := eventVon(t, env.feed(t, token), "c1"); !strings.Contains(ev, "DTSTART:20261008T100000Z") {
		t.Errorf("kaputte Zone: erwartet UTC 10:00:\n%s", ev)
	}
}
