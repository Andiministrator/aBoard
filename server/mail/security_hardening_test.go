package mail

// security_hardening_test.go — Regressionstests für die [S]-Findings aus dem
// Sprint-41-Kritiker, abgeräumt in Sprint 49 (Karte #364):
//
//	F-360  Extension-Blocker per Filename-Suffix umgehbar
//	F-361  Mail-Body → Karten-Text ohne Längen-Limit
//	F-362  StripHTML entkodet Entities NACH dem Tag-Strip (Re-Injection)
//
// Jeder Test enthält mindestens einen Fall, der VOR dem Fix durchgegangen wäre —
// sonst bewacht er nichts.

import (
	"strings"
	"testing"
)

// ── F-360: Extension-Bypass ──────────────────────────────────────────────

func TestF360_ExtensionBypassVarianten(t *testing.T) {
	// Jeder dieser Namen umging vorher `strings.HasSuffix(lower, ".exe")`.
	blockiert := []struct {
		name  string
		grund string
	}{
		{"evil.exe", "Basisfall — ging schon vorher"},
		{"evil.EXE", "Grossschreibung"},
		{"evil.exe ", "trailing Whitespace (Windows ignoriert ihn)"},
		{"evil.exe.", "trailing Punkt"},
		{"evil.exe   ", "mehrfacher Whitespace"},
		{"evil.exe...", "mehrfache Punkte"},
		{"rechnung.pdf.exe", "getarnte Doppelendung — echte Endung ist .exe"},
		{"evil.lnk", "Verknüpfung — heute der häufigste Vektor (F-442)"},
		{"evil.hta", "HTML-Application (F-442)"},
		{"evil.iso", "Container zur MotW-Umgehung (F-442)"},
		{"evil.wsf", "Windows Script File (F-442)"},
		{"evil.scf", "Shell Command File (F-442)"},
		{"evil.exe/", "Name endet auf Trenner — Normalisat leer (F-443)"},
		{"evil.exe::$DATA", "NTFS-Alternate-Data-Stream (F-443)"},
		{"evil.exe\u00a0.", "NBSP zwischen Endung und Punkt (F-443)"},
		{"evil.exe\u2060", "Word Joiner (F-443)"},
		{"evil.exe\u00ad", "Soft Hyphen (F-443)"},
		{"evil.exe\u200b", "Zero-Width-Space als Tarnung"},
		{"../../evil.exe", "Pfad-Anteil (Unix)"},
		{`C:\temp\evil.exe`, "Pfad-Anteil (Windows)"},
		{" evil.bat ", "beidseitiger Whitespace, andere Endung"},
		{"script.PS1.", "PowerShell mit Punkt und Grossschreibung"},
	}
	for _, tc := range blockiert {
		t.Run(tc.name, func(t *testing.T) {
			if !isBlocked(ParsedAttachment{Filename: tc.name}) {
				t.Errorf("»%s« wurde NICHT blockiert (%s)", tc.name, tc.grund)
			}
		})
	}
}

func TestF360_HarmlosesBleibtErlaubt(t *testing.T) {
	// Gegenprobe: die Verschärfung darf keine legitimen Anhänge fressen.
	erlaubt := []string{
		"rechnung.pdf",
		"foto.jpg",
		"Angebot 2026.docx",
		"archiv.zip",
		"daten.csv",
		"notiz.txt",
		"praesentation.pptx",
		"executive-summary.pdf", // enthält "exe" als Substring, aber nicht als Endung
		"exeter-bericht.pdf",    // beginnt mit "exe"
		// F-444: Domain-Anteile im Namen sind in Ticket-Mails sehr verbreitet.
		// Die frühere Segment-Prüfung verwarf genau diese, weil `.com` zugleich
		// ausführbare Endung und häufigste TLD ist.
		"Angebot-mustermann.com.pdf",
		"www.kunde.com.png",
		"logs-mail.example.com.txt",
		"protokoll.com.csv",
		// … und Build-Artefakte mit `.js.` in der Mitte
		"app.js.map",
		"bundle.js.LICENSE.txt",
		"vendor.js.gz",
		"v1.0.exe.log", // .exe in der Mitte, echte Endung .log
	}
	for _, name := range erlaubt {
		t.Run(name, func(t *testing.T) {
			if isBlocked(ParsedAttachment{Filename: name}) {
				t.Errorf("»%s« wurde faelschlich blockiert", name)
			}
		})
	}
}

func TestF360_MagicByteSniffing(t *testing.T) {
	// Zweite Stufe: harmloser Name, ausführbarer Inhalt.
	faelle := []struct {
		name string
		data []byte
	}{
		{"PE/DOS (.exe umbenannt)", pePlausibel()},
		{"ELF (Linux-Binary)", []byte{0x7f, 'E', 'L', 'F', 0x02}},
		{"Shebang (Shell-Skript)", []byte("#!/bin/sh\nrm -rf /")},
		{"Mach-O 64", []byte{0xfe, 0xed, 0xfa, 0xcf, 0x00}},
		// Bewusst konservativ: Eine Datei, die mit "#!" + absolutem Pfad beginnt,
		// gilt als Skript — auch wenn es sich um Dokumentation ÜBER Shebangs
		// handelt. Der umgekehrte Fehler (ausführbares Skript durchlassen) wiegt
		// schwerer als ein verworfener Text, der zufällig so anfängt.
		{"Doku, die mit einem Shebang beginnt", []byte("#!/bin/bash im Text erklaert: die erste Zeile")},
	}
	for _, tc := range faelle {
		t.Run(tc.name, func(t *testing.T) {
			a := ParsedAttachment{Filename: "rechnung.pdf", Data: tc.data}
			if !isBlocked(a) {
				t.Errorf("%s mit harmlosem Namen wurde nicht erkannt", tc.name)
			}
		})
	}

	// Gegenprobe: echte PDF-/PNG-Signaturen bleiben erlaubt.
	ok := []struct {
		name string
		data []byte
	}{
		{"PDF", []byte("%PDF-1.7\n")},
		{"PNG", []byte{0x89, 'P', 'N', 'G', 0x0d}},
		{"Plain-Text", []byte("Sehr geehrte Damen und Herren,")},
		// F-445: Zwei-Byte-Marker allein reichen nicht — sonst verliert eine CSV
		// mit Kfz-Kennzeichen oder ein Text über Shell-Skripte seinen Anhang.
		{"CSV mit MZ-Kennzeichen", []byte("MZ-AB 123;Mainz;2026-08-01;Lieferung\n")},
		{"Text der mit MZ beginnt", []byte("MZ Mainz Quartalsbericht — Seite 1")},
	}
	for _, tc := range ok {
		t.Run("erlaubt/"+tc.name, func(t *testing.T) {
			if isBlocked(ParsedAttachment{Filename: "anhang.pdf", Data: tc.data}) {
				t.Errorf("%s wurde faelschlich blockiert", tc.name)
			}
		})
	}
}

func TestF360_FilterBlockedMIMEEndToEnd(t *testing.T) {
	atts := []ParsedAttachment{
		{Filename: "rechnung.pdf", Data: []byte("%PDF-1.7")},
		// F-444: Ein echtes ZIP mit irreführendem Namen bleibt erhalten — es IST
		// ein Archiv und wird beim Öffnen nicht ausgeführt. Die frühere
		// Segment-Prüfung verwarf es und dafür auch "firma.com.pdf".
		{Filename: "evil.exe.zip", Data: []byte("PK\x03\x04")},
		// Getarnte Binary: harmloser Name, plausibler PE-Header -> Magic-Byte-Stufe.
		{Filename: "getarnt.pdf", Data: pePlausibel()},
		// Ehrliche Endung -> Endungs-Stufe.
		{Filename: "setup.msi", Data: []byte("beliebig")},
	}
	kept, dropped := FilterBlockedMIME(atts)

	keptNamen := map[string]bool{}
	for _, k := range kept {
		keptNamen[k.Filename] = true
	}
	if !keptNamen["rechnung.pdf"] || !keptNamen["evil.exe.zip"] || len(kept) != 2 {
		t.Fatalf("kept = %+v, erwartet rechnung.pdf + evil.exe.zip", kept)
	}
	if len(dropped) != 2 {
		t.Fatalf("dropped = %d, erwartet 2 (getarnt.pdf, setup.msi)", len(dropped))
	}
	for _, d := range dropped {
		if d.Data != nil {
			t.Errorf("Data von %q haette genullt werden muessen", d.Filename)
		}
	}
}

// pePlausibel baut einen minimalen, gültigen DOS/PE-Kopf: "MZ", bei 0x3C der
// Offset auf den PE-Header, dort "PE\x00\x00" (F-445).
func pePlausibel() []byte {
	b := make([]byte, 0x80)
	b[0], b[1] = 'M', 'Z'
	b[0x3c] = 0x40
	copy(b[0x40:], []byte{'P', 'E', 0x00, 0x00})
	return b
}

// ── F-361: Längen-Limit ──────────────────────────────────────────────────

func TestF361_LangerTextWirdGekuerzt(t *testing.T) {
	lang := strings.Repeat("A", MaxCardTextBytes+50_000)
	got := truncateCardText(lang)

	if len(got) >= len(lang) {
		t.Fatalf("Text wurde nicht gekuerzt: %d B", len(got))
	}
	if !strings.Contains(got, "gekürzt") {
		t.Error("Hinweis auf die Kuerzung fehlt — stilles Abschneiden ist genau das Problem")
	}
	// Der Nutztext-Anteil darf das Limit nicht überschreiten (Hinweis kommt oben drauf).
	if idx := strings.Index(got, "\n\n---\n"); idx < 0 || idx > MaxCardTextBytes {
		t.Errorf("Nutztext-Anteil = %d B, Limit %d B", idx, MaxCardTextBytes)
	}
}

func TestF361_KurzerTextUnveraendert(t *testing.T) {
	kurz := "Sehr geehrte Damen und Herren,\n\nanbei die Rechnung.\n\nMfG"
	if got := truncateCardText(kurz); got != kurz {
		t.Errorf("kurzer Text wurde veraendert:\n%q", got)
	}
	// Genau auf der Grenze: noch unverändert.
	grenze := strings.Repeat("B", MaxCardTextBytes)
	if got := truncateCardText(grenze); got != grenze {
		t.Errorf("Text exakt auf der Grenze wurde gekuerzt (%d B)", len(got))
	}
}

func TestF361_SchnittAnUTF8Grenze(t *testing.T) {
	// Mehrbyte-Zeichen exakt über die Grenze schieben: der Schnitt darf keine
	// halbe UTF-8-Sequenz hinterlassen (sonst bricht das JSON-Encoding —
	// gleiche Klasse wie F-422 im iCal-Export).
	fuell := strings.Repeat("x", MaxCardTextBytes-1)
	got := truncateCardText(fuell + "über den Rand hinaus")

	if !strings.HasPrefix(got, fuell) {
		t.Fatal("Praefix wurde beschaedigt")
	}
	nutz := got
	if i := strings.Index(got, "\n\n---\n"); i >= 0 {
		nutz = got[:i]
	}
	if !utf8Valid(nutz) {
		t.Error("gekuerzter Text ist kein gueltiges UTF-8 — Multibyte-Zeichen zerschnitten")
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// ── F-362: HTML-Entity-Re-Injection ──────────────────────────────────────

func TestF362_EncodierteTagsUeberlebenNicht(t *testing.T) {
	faelle := []struct {
		name string
		in   string
	}{
		{"einfach encodiert", `<p>Hallo &lt;script&gt;alert(1)&lt;/script&gt;</p>`},
		{"doppelt encodiert", `<p>&amp;lt;script&amp;gt;alert(1)&amp;lt;/script&amp;gt;</p>`},
		{"encodiertes img mit onerror", `<div>&lt;img src=x onerror=alert(1)&gt;</div>`},
		{"gemischt", `<b>echt</b> und &lt;script&gt;boese&lt;/script&gt;`},
	}
	for _, tc := range faelle {
		t.Run(tc.name, func(t *testing.T) {
			got := StripHTML(tc.in)
			if strings.Contains(got, "<script") || strings.Contains(got, "<img") {
				t.Errorf("Tag wurde re-injiziert:\n  in:  %s\n  out: %s", tc.in, got)
			}
			if strings.Contains(got, "onerror=") {
				t.Errorf("Event-Handler ueberlebte:\n  out: %s", got)
			}
		})
	}
}

func TestF362_NormalerTextBleibtLesbar(t *testing.T) {
	in := `<p>Hallo Welt</p><p>Zeile &amp; mehr — 5 &lt; 10</p>`
	got := StripHTML(in)

	if !strings.Contains(got, "Hallo Welt") {
		t.Errorf("Text ging verloren: %q", got)
	}
	if !strings.Contains(got, "&") {
		t.Errorf("&amp; wurde nicht dekodiert: %q", got)
	}
	// "5 &lt; 10" wird zu "5 < 10" dekodiert; da kein schliessendes ">" folgt,
	// bleibt es als Text erhalten.
	if !strings.Contains(got, "5 < 10") {
		t.Errorf("dekodiertes < ohne Tag-Kontext ging verloren: %q", got)
	}
}

// F-441 (Sprint-49-Kritiker): Der erste F-362-Fix zerstörte legitimen Text.
// `<[^>]+>` frass alles zwischen einem dekodierten `&lt;` und dem naechsten `&gt;` —
// und in HTML-Mail ist `&lt;` die EINZIGE Repraesentation eines literalen `<`.
// Diese Faelle sind die Belegtabelle aus dem Kritiker-Bericht.
func TestF441_VergleichsoperatorenUeberleben(t *testing.T) {
	faelle := []struct {
		in   string
		will string
	}{
		{`<p>Preis &lt; 100 EUR und Menge &gt; 5 Stueck</p>`, "Preis < 100 EUR und Menge > 5 Stueck"},
		{`<p>Mathe: 3 &lt; 5, aber 7 &gt; 2. Ende.</p>`, "Mathe: 3 < 5, aber 7 > 2. Ende."},
		{`<p>if (a &lt; b &amp;&amp; c &gt; d) { tuwas(); }</p>`, "if (a < b && c > d) { tuwas(); }"},
		{`<p>Lieferung nur wenn Menge &gt; 50 und Preis &lt; 200 EUR</p>`, "Lieferung nur wenn Menge > 50 und Preis < 200 EUR"},
	}
	for _, tc := range faelle {
		t.Run(tc.will, func(t *testing.T) {
			if got := StripHTML(tc.in); got != tc.will {
				t.Errorf("Inhalt verloren:\n  in:       %s\n  erwartet: %q\n  bekommen: %q", tc.in, tc.will, got)
			}
		})
	}
}

func TestF362_Idempotent(t *testing.T) {
	// Zweimal strippen darf nichts weiter verändern — sonst wäre die
	// Fixpunkt-Schleife nicht konvergiert.
	in := `<p>Text &lt;b&gt;fett&lt;/b&gt; und &amp;amp; Zeichen</p>`
	einmal := StripHTML(in)
	if zweimal := StripHTML(einmal); zweimal != einmal {
		t.Errorf("nicht idempotent:\n  1x: %q\n  2x: %q", einmal, zweimal)
	}
}
