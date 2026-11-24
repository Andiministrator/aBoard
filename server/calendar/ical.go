// Package calendar baut iCalendar-Feeds (RFC 5545) aus terminierten Karten
// (#308 Etappe 6, ADR-0103 §iCal-Export). Bewusst api-frei (neutrale Event-
// Struct) → isoliert unit-testbar, kein Import-Zyklus mit server/api.
package calendar

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Event ist eine Karte für den Feed. Start/End sind ISO-Kalenderdaten
// (YYYY-MM-DD, zeitzonenlos — aBoard-Doktrin ADR-0034). End ist der INKLUSIVE
// letzte Tag; der Builder rechnet die in RFC 5545 geforderte exklusive DTEND
// selbst (End+1). End == "" → Eintages-Termin am Start.
//
// StartTime/EndTime (ADR-0138 §1/§3) sind optionale Uhrzeiten "HH:MM" in der
// Ortszeit der Instanz, StartTime zum Tag Start, EndTime zum Tag End. Welche
// Kombination zu welchem DTSTART/DTEND führt, steht an zeitpunkte().
type Event struct {
	UID       string
	Start     string
	End       string
	StartTime string
	EndTime   string
	Summary   string
	// Location: Ort (Termine ohne Karte, ADR-0138 §4). Leer → keine Zeile.
	Location    string
	Description string
	URL         string
	// Agent: Ein Agent hat den Eintrag zuletzt geändert (Karte: Frist oder
	// Uhrzeit, Termin: jeder Upsert). Ergibt `X-ABOARD-AGENT:TRUE`; bei
	// Menschen fehlt die Zeile ganz (Zusage ans Partnerprojekt, ADR-0138 „Umsetzung“).
	// Derzeit ohne Setzer: der Feed „für außen“ (#748) ist mit #795
	// entfallen, und auch #782 setzt es nicht (Schatten tragen die Marke als
	// vonAgent, der Feed gibt sie nicht weiter). Vorgehalten, falls die
	// Anzeige außen (#781) die Agenten-Marke am Kalendereintrag braucht.
	Agent bool
	// Art ergibt `X-ABOARD-ART:<art>`. Gesetzt nur vom Feed der Seite außen
	// (api/ical.go, Karte #782, ADR-0140 §6): card und termin für eigene
	// Einträge, schatten-card und schatten-termin für Schatten. Auf aus und
	// innen leer — dort fehlt die Zeile.
	Art string
}

// BuildICS serialisiert einen kompletten VCALENDAR-Body. calName landet in
// X-WR-CALNAME (Anzeigename im Kalender-Client). now liefert das DTSTAMP
// (Erzeugungszeitpunkt) — als Parameter für deterministische Tests.
// Zeilen werden CRLF-getrennt und nach RFC 5545 §3.1 auf 75 Oktette gefaltet.
// Uhrzeiten gelten hier als UTC; der Feed nimmt BuildICSInZone.
func BuildICS(calName string, now time.Time, events []Event) string {
	return BuildICSInZone(calName, now, time.UTC, events)
}

// BuildICSInZone wie BuildICS, Uhrzeiten der Events gelten aber als Ortszeit in
// loc und gehen als UTC-DATE-TIME (…Z) in den Feed (ADR-0138 §2) — so braucht
// der Feed keine VTIMEZONE-Komponente. loc == nil → UTC.
func BuildICSInZone(calName string, now time.Time, loc *time.Location, events []Event) string {
	if loc == nil {
		loc = time.UTC
	}
	var b strings.Builder
	writeLine(&b, "BEGIN:VCALENDAR")
	writeLine(&b, "VERSION:2.0")
	writeLine(&b, "PRODID:-//aBoard//Kalender//DE")
	writeLine(&b, "CALSCALE:GREGORIAN")
	writeLine(&b, "METHOD:PUBLISH")
	if calName != "" {
		writeLine(&b, "X-WR-CALNAME:"+escapeText(calName))
	}
	stamp := now.UTC().Format("20060102T150405Z")
	for _, ev := range events {
		dtStart := compact(ev.Start)
		if dtStart == "" {
			continue // ohne gültiges Startdatum kein VEVENT
		}
		endDay := ev.End
		if endDay == "" {
			endDay = ev.Start
		}
		dtEnd := compact(plusOneDay(endDay))
		if dtEnd == "" {
			dtEnd = compact(plusOneDay(ev.Start))
		}
		writeLine(&b, "BEGIN:VEVENT")
		writeLine(&b, "UID:"+escapeText(ev.UID))
		writeLine(&b, "DTSTAMP:"+stamp)
		if zs, ze, ok := zeitpunkte(ev, endDay, loc); ok {
			writeLine(&b, "DTSTART:"+zs)
			if ze != "" {
				writeLine(&b, "DTEND:"+ze)
			}
		} else {
			writeLine(&b, "DTSTART;VALUE=DATE:"+dtStart)
			writeLine(&b, "DTEND;VALUE=DATE:"+dtEnd)
		}
		writeLine(&b, "SUMMARY:"+escapeText(ev.Summary))
		if ev.Location != "" {
			writeLine(&b, "LOCATION:"+escapeText(ev.Location))
		}
		if ev.Description != "" {
			writeLine(&b, "DESCRIPTION:"+escapeText(ev.Description))
		}
		if ev.URL != "" {
			writeLine(&b, "URL:"+uriWert(ev.URL))
		}
		if ev.Agent {
			writeLine(&b, "X-ABOARD-AGENT:TRUE")
		}
		if ev.Art != "" {
			writeLine(&b, "X-ABOARD-ART:"+escapeText(ev.Art))
		}
		writeLine(&b, "END:VEVENT")
	}
	writeLine(&b, "END:VCALENDAR")
	return b.String()
}

// zeitpunkte entscheidet nach ADR-0138 §3, ob ein Event mit Uhrzeit geht, und
// liefert dann DTSTART und (optional) DTEND als UTC-DATE-TIME. ok == false →
// ganztags (VALUE=DATE). RFC 5545 verlangt für DTSTART und DTEND denselben
// Wertetyp, gemischt wird deshalb nie:
//
//   - keine Uhrzeit                         → ganztags
//   - ein Tag, beide Uhrzeiten, Ende > Beginn → von–bis
//   - ein Tag, nur eine Uhrzeit             → Zeitpunkt (nur DTSTART, §3.6.1)
//   - ein Tag, Ende <= Beginn               → nur der Beginn als Zeitpunkt
//   - mehrere Tage, beide Uhrzeiten         → von–bis über die Tage
//   - mehrere Tage, nur eine Uhrzeit        → ganztags
func zeitpunkte(ev Event, endDay string, loc *time.Location) (start, end string, ok bool) {
	st := gueltigeUhrzeit(ev.StartTime)
	et := gueltigeUhrzeit(ev.EndTime)
	if st == "" && et == "" {
		return "", "", false
	}
	if endDay == ev.Start {
		if st != "" && et != "" && et > st {
			a, okA := utcZeitpunkt(ev.Start, st, loc)
			e, okE := utcZeitpunkt(ev.Start, et, loc)
			return a, e, okA && okE
		}
		t := st
		if t == "" {
			t = et
		}
		a, okA := utcZeitpunkt(ev.Start, t, loc)
		return a, "", okA
	}
	if st == "" || et == "" {
		return "", "", false
	}
	a, okA := utcZeitpunkt(ev.Start, st, loc)
	e, okE := utcZeitpunkt(endDay, et, loc)
	if !okA || !okE || e <= a {
		return "", "", false
	}
	return a, e, true
}

// gueltigeUhrzeit liefert s, wenn es die Form "HH:MM" (24 h) hat, sonst "".
func gueltigeUhrzeit(s string) string {
	if !IstUhrzeit(s) {
		return ""
	}
	return s
}

// IstUhrzeit prüft die Form einer Karten-Uhrzeit (ADR-0138 §1): "HH:MM",
// 24 Stunden, zweistellig. Gegenstück zu istUhrzeit in
// src/calendar/date-helpers.js.
func IstUhrzeit(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	for _, i := range []int{0, 1, 3, 4} {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return (s[0] < '2' || (s[0] == '2' && s[1] <= '3')) && s[3] <= '5'
}

// utcZeitpunkt rechnet Datum und Uhrzeit in Ortszeit loc nach UTC und
// formatiert als RFC-5545-DATE-TIME (…Z).
func utcZeitpunkt(iso, hhmm string, loc *time.Location) (string, bool) {
	d, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return "", false
	}
	z, err := time.Parse("15:04", hhmm)
	if err != nil {
		return "", false
	}
	return Ortszeit(d.Year(), d.Month(), d.Day(), z.Hour(), z.Minute(), loc).UTC().Format("20060102T150405Z"), true
}

// Ortszeit bildet eine Wanduhrzeit in loc auf einen Zeitpunkt ab — eindeutig
// auch an den Umstellungstagen (ADR-0138 §2), was time.Date nicht zusagt („does
// not guarantee which“):
//
//   - doppelte Stunde (Herbst): die ERSTE, also noch in Sommerzeit;
//   - Lücke (Frühjahr, 02:30 gibt es nicht): mit dem Versatz VOR der Umstellung
//     gerechnet — das landet vorwärts auf der gültigen Zeit (02:30 MEZ =
//     03:30 MESZ).
//
// Dazu werden die beiden Versätze probiert, die rund um den Tag gelten, und der
// früheste Zeitpunkt genommen, dessen Wanduhrzeit stimmt.
func Ortszeit(y int, m time.Month, d, h, mi int, loc *time.Location) time.Time {
	naiv := time.Date(y, m, d, h, mi, 0, 0, time.UTC)
	_, vorher := naiv.Add(-24 * time.Hour).In(loc).Zone()
	_, nachher := naiv.Add(24 * time.Hour).In(loc).Zone()
	var best time.Time
	for _, off := range []int{vorher, nachher} {
		t := naiv.Add(-time.Duration(off) * time.Second)
		w := t.In(loc)
		if w.Year() != y || w.Month() != m || w.Day() != d || w.Hour() != h || w.Minute() != mi {
			continue
		}
		if best.IsZero() || t.Before(best) {
			best = t
		}
	}
	if best.IsZero() {
		best = naiv.Add(-time.Duration(vorher) * time.Second)
	}
	return best
}

// writeLine fügt eine gefaltete, CRLF-terminierte Content-Line an.
func writeLine(b *strings.Builder, line string) {
	b.WriteString(foldLine(line))
	b.WriteString("\r\n")
}

// foldLine bricht Content-Lines > 75 Oktette nach RFC 5545 §3.1 um
// (Fortsetzungszeile beginnt mit einem Space). RUNE-bewusst: die Faltgrenze
// fällt NIE mitten in eine Multi-Oktett-UTF-8-Sequenz (RFC 5545 §3.1 fordert
// das explizit) — ein deutscher Umlaut/Emoji genau an der 75-Oktett-Grenze
// würde sonst eine invalide Zeile erzeugen, die strikte Parser (Apple/Outlook)
// verwerfen.
func foldLine(s string) string {
	if len(s) <= 75 {
		return s
	}
	var out strings.Builder
	limit := 75 // erste Zeile: 75 Oktette; Folgezeilen: 74 (führender Space zählt)
	octets := 0
	for _, r := range s {
		rl := utf8.RuneLen(r)
		if octets+rl > limit {
			out.WriteString("\r\n ")
			octets = 0
			limit = 74
		}
		out.WriteRune(r)
		octets += rl
	}
	return out.String()
}

// uriWert: Ein URI-Wert wird nach RFC 5545 §3.3.13 nicht escaped (ein „\,“
// änderte die Adresse). Steuerzeichen, auch CR/LF, werden entfernt, damit
// kein Wert eine Zeile beendet oder eine neue Eigenschaft einschleust.
func uriWert(s string) string {
	return strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return -1
		}
		return c
	}, s)
}

// escapeText escaped \\ ; , und Zeilenumbrüche nach RFC 5545 §3.3.11.
func escapeText(s string) string {
	r := strings.NewReplacer(
		"\\", "\\\\",
		";", "\\;",
		",", "\\,",
		"\r\n", "\\n",
		"\n", "\\n",
		"\r", "\\n",
	)
	// F-1889 (Sprint-86-Kritiker K-B3): RFC 5545 §3.3.11 erlaubt in TEXT
	// außer HTAB keine Steuerzeichen; strikte Parser verwerfen sonst den
	// ganzen Feed. CR/LF bleiben hier stehen, der Replacer macht \n daraus.
	s = strings.Map(func(c rune) rune {
		if c != '\t' && c != '\n' && c != '\r' && (c < 0x20 || c == 0x7f) {
			return -1
		}
		return c
	}, s)
	return r.Replace(s)
}

// compact wandelt "2026-06-15" → "20260615". Liefert "" bei ungültigem Format.
func compact(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return ""
	}
	return t.Format("20060102")
}

// plusOneDay addiert einen Tag auf ein ISO-Datum (für die exklusive DTEND).
// Leerer/ungültiger Input → "".
func plusOneDay(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return ""
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

// FormatCalName baut einen sprechenden Kalendernamen.
func FormatCalName(scope string) string {
	return fmt.Sprintf("aBoard — %s", scope)
}
