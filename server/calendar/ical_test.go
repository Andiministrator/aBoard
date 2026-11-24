package calendar

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func fixedNow() time.Time {
	return time.Date(2026, 6, 5, 9, 30, 0, 0, time.UTC)
}

func TestBuildICS_Wrapper(t *testing.T) {
	out := BuildICS("aBoard — alle Boards", fixedNow(), nil)
	for _, want := range []string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//aBoard//Kalender//DE",
		"CALSCALE:GREGORIAN", "X-WR-CALNAME:aBoard — alle Boards", "END:VCALENDAR",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("VCALENDAR fehlt %q\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "END:VCALENDAR\r\n") {
		t.Errorf("kein CRLF-Abschluss")
	}
}

func TestBuildICS_SingleDayDTENDExclusive(t *testing.T) {
	out := BuildICS("c", fixedNow(), []Event{
		{UID: "u1", Start: "2026-06-15", Summary: "Termin"},
	})
	if !strings.Contains(out, "DTSTART;VALUE=DATE:20260615") {
		t.Errorf("DTSTART falsch:\n%s", out)
	}
	// Eintages-Ganztags-Event: DTEND ist der Folgetag (RFC 5545 exklusiv).
	if !strings.Contains(out, "DTEND;VALUE=DATE:20260616") {
		t.Errorf("DTEND muss Start+1 sein:\n%s", out)
	}
	if !strings.Contains(out, "DTSTAMP:20260605T093000Z") {
		t.Errorf("DTSTAMP falsch:\n%s", out)
	}
}

func TestBuildICS_RangeDTENDExclusive(t *testing.T) {
	out := BuildICS("c", fixedNow(), []Event{
		{UID: "u2", Start: "2026-06-28", End: "2026-07-03", Summary: "Range"},
	})
	if !strings.Contains(out, "DTSTART;VALUE=DATE:20260628") {
		t.Errorf("Range-DTSTART falsch:\n%s", out)
	}
	// End ist inklusiv 03.07 → DTEND exklusiv 04.07.
	if !strings.Contains(out, "DTEND;VALUE=DATE:20260704") {
		t.Errorf("Range-DTEND muss End+1 sein:\n%s", out)
	}
}

func TestBuildICS_TextEscaping(t *testing.T) {
	out := BuildICS("c", fixedNow(), []Event{
		{UID: "u3", Start: "2026-06-15", Summary: "A, B; C\\D", Description: "Zeile1\nZeile2"},
	})
	if !strings.Contains(out, `SUMMARY:A\, B\; C\\D`) {
		t.Errorf("Sonderzeichen nicht escaped:\n%s", out)
	}
	if !strings.Contains(out, `DESCRIPTION:Zeile1\nZeile2`) {
		t.Errorf("Newline nicht escaped:\n%s", out)
	}
}

func TestBuildICS_LineFolding(t *testing.T) {
	long := strings.Repeat("X", 200)
	out := BuildICS("c", fixedNow(), []Event{
		{UID: "u4", Start: "2026-06-15", Summary: long},
	})
	// Jede physische Zeile ≤ 75 Oktette (RFC 5545 §3.1).
	for _, line := range strings.Split(out, "\r\n") {
		if len(line) > 75 {
			t.Errorf("Zeile > 75 Oktette (%d): %q", len(line), line)
		}
	}
	// Fortsetzungszeilen beginnen mit Space.
	if !strings.Contains(out, "\r\n ") {
		t.Errorf("keine Fortsetzungszeile (Space-Prefix) gefunden")
	}
}

func TestBuildICS_FoldingDoesNotBreakUTF8(t *testing.T) {
	// Umlaute/Emoji exakt rund um die 75-Oktett-Grenze — die Faltung darf
	// keine Multi-Oktett-Sequenz zerschneiden (F-422: invalider ICS-Output).
	for _, pad := range []int{60, 68, 70, 72, 73, 74, 80} {
		summary := strings.Repeat("ä", pad) + "🗓ü"
		out := BuildICS("c", fixedNow(), []Event{
			{UID: "u", Start: "2026-06-15", Summary: summary},
		})
		if !utf8.ValidString(out) {
			t.Errorf("pad=%d: ICS enthält invalide UTF-8-Sequenz (Faltung zerschnitt eine Rune)", pad)
		}
		for _, line := range strings.Split(out, "\r\n") {
			if len(line) > 75 {
				t.Errorf("pad=%d: Zeile > 75 Oktette (%d)", pad, len(line))
			}
		}
	}
}

func TestBuildICS_SkipInvalidStart(t *testing.T) {
	out := BuildICS("c", fixedNow(), []Event{
		{UID: "bad", Start: "nicht-iso", Summary: "X"},
		{UID: "ok", Start: "2026-06-15", Summary: "Y"},
	})
	if strings.Contains(out, "UID:bad") {
		t.Errorf("VEVENT mit ungültigem Start sollte übersprungen werden")
	}
	if !strings.Contains(out, "UID:ok") {
		t.Errorf("gültiges VEVENT fehlt")
	}
}

// F-1889 (Sprint-86-Kritiker K-B3): Steuerzeichen außer HTAB verschwinden,
// Zeilenumbrüche werden weiter zu \n.
func TestEscapeTextSteuerzeichen(t *testing.T) {
	ein := "a\x00b\x0bc\x1fd\x7fe\tf\ng;h"
	if got, want := escapeText(ein), "abcde\tf\\ng\\;h"; got != want {
		t.Errorf("escapeText(%q) = %q, erwartet %q", ein, got, want)
	}
}
