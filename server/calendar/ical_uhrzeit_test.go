package calendar

// Uhrzeiten im Feed (ADR-0138 §1–§3, Karte #742): die Tabelle aus §3 und die
// Randfälle aus §2 (Sommerzeit-Lücke, doppelte Stunde, Ende vor Beginn,
// Zeitzonenwechsel), je mit einem Test.

import (
	"strings"
	"testing"
	"time"
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("Zeitzonendaten fehlen: %v", err)
	}
	return loc
}

// vevent liefert die DTSTART-/DTEND-Zeilen des einzigen Events.
func vevent(t *testing.T, out string) (dtstart, dtend string) {
	t.Helper()
	for _, z := range strings.Split(out, "\r\n") {
		if strings.HasPrefix(z, "DTSTART") {
			dtstart = z
		}
		if strings.HasPrefix(z, "DTEND") {
			dtend = z
		}
	}
	return
}

func TestFeedUhrzeit_Tabelle(t *testing.T) {
	loc := berlin(t)
	faelle := []struct {
		name      string
		ev        Event
		wantStart string
		wantEnd   string // "" = keine DTEND-Zeile
	}{
		{"ohne Uhrzeit bleibt ganztags",
			Event{Start: "2026-10-08"},
			"DTSTART;VALUE=DATE:20261008", "DTEND;VALUE=DATE:20261009"},
		{"ein Tag, von–bis, Sommerzeit (UTC+2)",
			Event{Start: "2026-10-08", End: "2026-10-08", StartTime: "10:00", EndTime: "11:30"},
			"DTSTART:20261008T080000Z", "DTEND:20261008T093000Z"},
		{"ein Tag, von–bis, Winterzeit (UTC+1)",
			Event{Start: "2026-12-08", StartTime: "10:00", EndTime: "11:00"},
			"DTSTART:20261208T090000Z", "DTEND:20261208T100000Z"},
		{"ein Tag, nur Frist-Uhrzeit → Zeitpunkt",
			Event{Start: "2026-10-08", End: "2026-10-08", EndTime: "17:00"},
			"DTSTART:20261008T150000Z", ""},
		{"ein Tag, nur Beginn → Zeitpunkt",
			Event{Start: "2026-10-08", StartTime: "09:15"},
			"DTSTART:20261008T071500Z", ""},
		{"mehrere Tage, beide Uhrzeiten → über die Tage",
			Event{Start: "2026-10-08", End: "2026-10-10", StartTime: "09:00", EndTime: "12:00"},
			"DTSTART:20261008T070000Z", "DTEND:20261010T100000Z"},
		{"mehrere Tage, nur eine Uhrzeit → ganztags",
			Event{Start: "2026-10-08", End: "2026-10-10", EndTime: "12:00"},
			"DTSTART;VALUE=DATE:20261008", "DTEND;VALUE=DATE:20261011"},
		{"ungültige Uhrzeit zählt als keine",
			Event{Start: "2026-10-08", StartTime: "25:00", EndTime: "9:00"},
			"DTSTART;VALUE=DATE:20261008", "DTEND;VALUE=DATE:20261009"},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			out := BuildICSInZone("c", fixedNow(), loc, []Event{f.ev})
			s, e := vevent(t, out)
			if s != f.wantStart || e != f.wantEnd {
				t.Fatalf("DTSTART/DTEND = %q / %q, erwartet %q / %q\n%s", s, e, f.wantStart, f.wantEnd, out)
			}
			// RFC 5545: nie DATE und DATE-TIME in einem Event mischen.
			if e != "" && strings.Contains(s, "VALUE=DATE") != strings.Contains(e, "VALUE=DATE") {
				t.Fatalf("Wertetypen gemischt: %q / %q", s, e)
			}
		})
	}
}

// Ende vor Beginn am selben Tag: nur der Beginn geht als Zeitpunkt hinaus.
// Gleich lang (Ende == Beginn) ebenso — RFC 5545 verlangt DTEND > DTSTART.
func TestFeedUhrzeit_EndeVorBeginn(t *testing.T) {
	loc := berlin(t)
	for _, ende := range []string{"09:00", "10:00"} {
		out := BuildICSInZone("c", fixedNow(), loc, []Event{{Start: "2026-10-08", StartTime: "10:00", EndTime: ende}})
		s, e := vevent(t, out)
		if s != "DTSTART:20261008T080000Z" || e != "" {
			t.Fatalf("Ende %s: DTSTART/DTEND = %q / %q, erwartet nur den Beginn", ende, s, e)
		}
	}
}

// Sommerzeit-Lücke: 02:30 am 29.03.2026 gibt es in Berlin nicht. Gerechnet
// wird vorwärts auf die gültige Zeit — 03:30 MESZ = 01:30 UTC.
func TestFeedUhrzeit_SommerzeitLuecke(t *testing.T) {
	loc := berlin(t)
	got := Ortszeit(2026, time.March, 29, 2, 30, loc).UTC()
	if want := time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Lücke: %v, erwartet %v", got, want)
	}
	if w := got.In(loc); w.Hour() != 3 || w.Minute() != 30 {
		t.Fatalf("Lücke: Wanduhr %v, erwartet 03:30", w)
	}
}

// Doppelte Stunde: 02:30 am 25.10.2026 gibt es zweimal — es gilt die erste,
// noch in Sommerzeit: 00:30 UTC.
func TestFeedUhrzeit_DoppelteStunde(t *testing.T) {
	loc := berlin(t)
	got := Ortszeit(2026, time.October, 25, 2, 30, loc).UTC()
	if want := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("doppelte Stunde: %v, erwartet %v (die erste)", got, want)
	}
}

// Normale Zeiten an den Umstellungstagen bleiben exakt.
func TestFeedUhrzeit_Umstellungstage_normaleZeiten(t *testing.T) {
	loc := berlin(t)
	for _, f := range []struct {
		m       time.Month
		d, h    int
		wantUTC int
	}{
		{time.March, 29, 1, 0}, {time.March, 29, 4, 2},
		{time.October, 25, 1, 23}, {time.October, 25, 4, 3},
	} {
		got := Ortszeit(2026, f.m, f.d, f.h, 0, loc).UTC()
		if got.Hour() != f.wantUTC {
			t.Errorf("%02d.%02d. %02d:00 → %v, erwartet %02d:00 UTC", f.d, f.m, f.h, got, f.wantUTC)
		}
		if w := got.In(loc); w.Hour() != f.h {
			t.Errorf("%02d.%02d. %02d:00: Rücklauf ergibt %v", f.d, f.m, f.h, w)
		}
	}
}

// Zeitzonenwechsel: Dieselbe Karte ergibt in einer anderen Instanz-Zone einen
// anderen UTC-Zeitpunkt — gewollt, Uhrzeiten sind Ortszeit (ADR-0138 §2).
func TestFeedUhrzeit_Zeitzonenwechsel(t *testing.T) {
	berl := berlin(t)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("Zeitzonendaten fehlen: %v", err)
	}
	ev := []Event{{Start: "2026-10-08", StartTime: "10:00", EndTime: "11:00"}}
	s1, _ := vevent(t, BuildICSInZone("c", fixedNow(), berl, ev))
	s2, _ := vevent(t, BuildICSInZone("c", fixedNow(), ny, ev))
	s3, _ := vevent(t, BuildICS("c", fixedNow(), ev))
	if s1 != "DTSTART:20261008T080000Z" || s2 != "DTSTART:20261008T140000Z" || s3 != "DTSTART:20261008T100000Z" {
		t.Fatalf("Berlin %q, New York %q, UTC %q", s1, s2, s3)
	}
}

func TestIstUhrzeit(t *testing.T) {
	for _, s := range []string{"00:00", "09:30", "19:59", "23:59"} {
		if !IstUhrzeit(s) {
			t.Errorf("%q abgelehnt", s)
		}
	}
	for _, s := range []string{"", "9:30", "24:00", "23:60", "12:5", "12-30", "1230 ", "ab:cd", "2026-10-08T10:00"} {
		if IstUhrzeit(s) {
			t.Errorf("%q angenommen", s)
		}
	}
}

// Kritik zu #782 (K-5): Der URL-Wert wird nicht als TEXT escaped; Steuerzeichen
// (CR/LF) fallen weg, damit keine Eigenschaft eingeschleust wird.
func TestBuildICS_URLUnescapedOhneSteuerzeichen(t *testing.T) {
	ics := BuildICS("x", time.Unix(0, 0), []Event{{UID: "u", Start: "2026-10-20", Summary: "s",
		URL: "https://h/a,b;c#board=1\r\nX-EVIL:1"}})
	if !strings.Contains(ics, "URL:https://h/a,b;c#board=1X-EVIL:1\r\n") {
		t.Errorf("URL-Zeile:\n%s", ics)
	}
	for _, z := range strings.Split(ics, "\r\n") {
		if strings.HasPrefix(z, "X-EVIL") {
			t.Errorf("eingeschleuste Eigenschaft: %q", z)
		}
	}
}
