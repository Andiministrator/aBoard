package schleuse

// auszug_nacharbeit_test.go — Nacharbeit zu den Kritiker-Befunden über S2
// (Karte #685): KS-1 Flexion, KS-2 IBAN/Leerzeichen, KS-3 Claude als
// Vorname, KS-4 Adressen/Telefon/Bindestrich/(at), KS-8 Zuordnung, KS-11
// U+200C/U+200D, F-1616 NFC.

import (
	"context"
	"strings"
	"testing"
)

func nacharbeitLauf(t *testing.T, text string, namen ...string) (string, map[string]string) {
	t.Helper()
	n, err := Normalisiere(text)
	if err != nil {
		t.Fatal(err)
	}
	l := NeueNamensliste()
	for _, name := range namen {
		l.Person(name)
	}
	l.Email("@firma.de")
	p := NeuerPseudonymisierer(n)
	p.FesteStufe(l.Eintraege())
	return p.Text(), p.Zuordnung()
}

// KS-1: Gebeugte Namen fallen nicht mehr durch; der Anhang bleibt hinter
// dem Platzhalter, längere Wörter mit dem Namen als Anfang bleiben stehen.
func TestFesteStufe_GebeugteNamen(t *testing.T) {
	for in, want := range map[string]string{
		"Annas Auto gehört Anna.": "[PERSON_1]s Auto gehört [PERSON_1].",
		"Müllers Haus":            "[PERSON_1]s Haus",
		"Annas’ Rad":              "[PERSON_1]s’ Rad",
		"Anna's Rad":              "[PERSON_1]'s Rad",
		"Hans’ Rad, Hans' Rad":    "[PERSON_2]’ Rad, [PERSON_2]' Rad",
		"bei Müllern":             "bei [PERSON_1]n",
		"Annabelle und Hansen":    "Annabelle und Hansen",
	} {
		got, _ := nacharbeitLauf(t, in, "Anna Müller", "Hans Meier")
		// Hans Meier bekommt [PERSON_2] nur, wenn Anna Müller vorher
		// vergeben wurde — für die Fälle ohne Anna ist es [PERSON_1].
		if !strings.Contains(in, "Anna") && !strings.Contains(in, "Müller") {
			want = strings.ReplaceAll(want, "[PERSON_2]", "[PERSON_1]")
		}
		if got != want {
			t.Errorf("%q → %q, erwartet %q", in, got, want)
		}
	}
}

// KS-2: IBAN in Kleinschreibung und mit geschützten Leerzeichen; alle
// Schreibweisen derselben Nummer → ein Platzhalter. Kleinschreibung ohne
// genug Ziffern bleibt Text.
func TestFesteStufe_IBANVarianten(t *testing.T) {
	in := "a de89 3704 0044 0532 0130 00 b DE89\u00A03704\u00A00044\u00A00532\u00A00130\u00A000 c DE89\u202F3704\u20070044 0532 0130 00 d DE89370400440532013000 e Raum ab12 ganz weit"
	got, _ := nacharbeitLauf(t, in)
	if want := "a [IBAN_1] b [IBAN_1] c [IBAN_1] d [IBAN_1] e Raum ab12 ganz weit"; got != want {
		t.Errorf("IBAN:\n%q\n%q", got, want)
	}
}

// KS-3: Nur Claude-Modellkennungen fallen aus der Namensliste, nicht
// Menschen mit Vornamen Claude.
func TestNamensliste_ClaudeAlsVorname(t *testing.T) {
	l := NeueNamensliste()
	for _, n := range []string{"Claude", "Claude Opus", "Claude Sonnet 4.5", "Claude Opus 5 (1M context)", "claude-opus",
		"Claude 3.5 Sonnet", "Claude Haiku", "agent:recherche", "User", "system",
		"Claude Dupont", "User Meier", "System Berg"} {
		l.Person(n)
	}
	var namen []string
	for _, e := range l.Eintraege() {
		namen = append(namen, e.Text)
	}
	if got := strings.Join(namen, "|"); got != "Claude Dupont|User Meier|System Berg" {
		t.Errorf("Namensliste: %s", got)
	}
	got, _ := nacharbeitLauf(t, "Claude Dupont und Dupont, Claude Opus half.", "Claude Dupont", "Claude Opus")
	// F-1624: Modellkennungen sind vor der Namensstufe geschützt — „Claude
	// Opus“ bleibt, sonst ergäbe die Re-Identifizierung „Claude Dupont Opus“.
	// Der Vorname allein wird weiter ersetzt.
	if got != "[PERSON_1] und [PERSON_1], Claude Opus half." {
		t.Errorf("Text: %q", got)
	}
	got, _ = nacharbeitLauf(t, "Claude sagt, claude-opus-4 und Claude 3.5 Sonnet halfen; Mail claude-opus@firma.de.", "Claude Dupont")
	if got != "[PERSON_1] sagt, claude-opus-4 und Claude 3.5 Sonnet halfen; Mail [EMAIL_1]." {
		t.Errorf("Text: %q", got)
	}
}

// KS-4: Adressen ohne Schema, Telefon mit Punkten, Bindestrich-Namen,
// verschleierte E-Mail. Dateinamen, Pfade, Daten und Versionen bleiben.
func TestFesteStufe_AdressenTelefonBindestrich(t *testing.T) {
	in := strings.Join([]string{
		"siehe example.com/pfad und wiki.example/anna, kunde.example.org:8443 und portal.de.",
		"Datei aboard.html, src/main.go, Version 1.2.3, am 01.02.2026 um 14.30, Kap. 3.",
		"Tel 0170.1234567 und (0170) 7654321.",
		"Hans Peter Schmidt, Hans-Peter Schmidt, HansPeter Schmidt und Dr. Anna Müller-Lüdenscheidt.",
		"Mail anna.mueller (at) firma.de oder anna [at] firma [punkt] de, Firma firma.de.",
	}, "\n")
	got, z := nacharbeitLauf(t, in, "Hans-Peter Schmidt", "Anna Müller")
	for _, weg := range []string{"example.com", "wiki.example", "kunde.example", "portal.de", "1234567", "7654321",
		"Hans", "Peter", "Schmidt", "Anna", "Müller", "Lüdenscheidt", "anna", "mueller"} {
		if strings.Contains(got, weg) {
			t.Errorf("%q steht noch im Text:\n%s", weg, got)
		}
	}
	for _, bleibt := range []string{"aboard.html", "src/main.go", "Version 1.2.3", "am 01.02.2026 um 14.30", "Kap. 3", "Firma [FIRMA_1]."} {
		if !strings.Contains(got, bleibt) {
			t.Errorf("%q fehlt (zu viel ersetzt):\n%s", bleibt, got)
		}
	}
	if strings.Contains(got, "([TELEFON") {
		t.Errorf("Klammer vor der Vorwahl bleibt stehen:\n%s", got)
	}
	if strings.Count(got, "[TELEFON_") != 2 || strings.Count(got, "[EMAIL_") != 2 || strings.Count(got, "[URL_") != 4 {
		t.Errorf("Zählung Telefon/E-Mail/URL:\n%s", got)
	}
	// Alle drei Schreibweisen von Hans-Peter Schmidt → ein Platzhalter.
	ph := ""
	for k, v := range z {
		if v == "Hans-Peter Schmidt" {
			ph = k
		}
	}
	if ph == "" || strings.Count(got, ph) != 3 {
		t.Errorf("Hans-Peter Schmidt nicht einheitlich (%q):\n%s", ph, got)
	}
}

// KS-11 und F-1616: U+200C/U+200D bleiben im Text (Emoji, Schriften),
// U+200B, BOM, Richtungszeichen, weiches Trennzeichen und Tag-Zeichen
// fallen weg; Leerzeichen-Varianten werden normale Leerzeichen; NFC.
func TestNormalisiere_Nacharbeit(t *testing.T) {
	roh := "👨\u200D👩 می\u200Cخواهم\u00A0A\u202FB\u2007C\u2028D\u00ADE\u2060F\U000E0041G\u200BH\u202EI\uFEFFJ Mu\u0308ller"
	got, err := Normalisiere(roh)
	if err != nil {
		t.Fatal(err)
	}
	want := "👨\u200D👩 می\u200Cخواهم A B C\nDEFGHIJ Müller"
	if got != want {
		t.Fatalf("Normalisiere:\n%q\n%q", got, want)
	}
	if nochmal, _ := Normalisiere(got); nochmal != got {
		t.Errorf("nicht idempotent: %q", nochmal)
	}
	// Zerlegtes ü (F-1616) und ein Verbinder mitten im Namen (KS-11)
	// entgehen der festen Stufe nicht.
	text, _ := nacharbeitLauf(t, "Mu\u0308ller und An\u200Dna Mül\u200Cler", "Anna Müller")
	if text != "[PERSON_1] und [PERSON_1]" {
		t.Errorf("feste Stufe: %q", text)
	}
}

// KS-1 auch für die Modellstufe: Das Modell meldet „Huber“, im Text steht
// „Hubers“ und „Hu<U+200D>ber“.
func TestModellStufe_FlexionUndVerbinder(t *testing.T) {
	text, _ := Normalisiere("Hubers Angebot, Hu\u200Dber selbst.")
	p := NeuerPseudonymisierer(text)
	erg, err := p.ModellStufe(context.Background(), func(context.Context, string, string) (string, error) {
		return `{"funde":[{"text":"Huber","art":"PERSON"}]}`, nil
	})
	if err != nil || erg.Ersetzt != 1 {
		t.Fatalf("%+v %v", erg, err)
	}
	if got := p.Text(); got != "[PERSON_1]s Angebot, [PERSON_1] selbst." {
		t.Errorf("Modellstufe: %q", got)
	}
}

// KS-8: Die Zuordnung hält EIN Original je Platzhalter — den Namen, wie er
// im Board steht. Platzhalter zurück durch Originale ergibt deshalb NICHT
// zwingend den Ausgangstext; gleich bleibt, wer gemeint ist.
func TestZuordnung_KanonischNichtWoertlich(t *testing.T) {
	got, z := nacharbeitLauf(t, "MÜLLER, Müller, anna müller, Hans Peter Schmidt", "Anna Müller", "Hans-Peter Schmidt")
	if got != "[PERSON_2], [PERSON_2], [PERSON_2], [PERSON_1]" {
		t.Fatalf("Text: %q", got)
	}
	if r := reidentifiziere(got, z); r != "Anna Müller, Anna Müller, Anna Müller, Hans-Peter Schmidt" {
		t.Errorf("Re-Identifizierung: %q", r)
	}
}

// F-1631: IP-Adressen und host:port ohne Endung gehen nicht hinaus. F-1625:
// jede Endung aus Buchstaben ist eine Adresse, auch ohne Pfad. F-1630:
// Dateinamen (auch mit Zeilennummer), Satzanfänge ohne Leerzeichen und
// Abkürzungen bleiben; Uhrzeiten und Versionen ebenso.
func TestFesteStufe_IPHostPortEndungen(t *testing.T) {
	weg := []string{
		"Server 10.0.0.5 antwortet",
		"siehe 192.168.1.20:8080/admin.",
		"Dienst auf server01:8080 und db-intern:5432/x",
		"Adresse 2001:db8:85a3::8a2e:370:7334 und fe80::1ff:fe23:4567:890a",
		"siehe kunde.xyz",
		"Kunde KUNDE.XYZ",
	}
	for _, in := range weg {
		got, _ := nacharbeitLauf(t, in)
		for _, w := range []string{"10.0.0.5", "192.168", "server01", "db-intern", "2001:db8", "fe80", "kunde.xyz", "KUNDE.XYZ"} {
			if strings.Contains(got, w) {
				t.Errorf("%q steht noch in %q", w, got)
			}
		}
		if !strings.Contains(got, "[URL_1]") {
			t.Errorf("kein Platzhalter in %q", got)
		}
	}
	bleibt := []string{
		"Version 1.2.3.4.5 und v1.2.3",
		"um 10:30 und 10:30:00 Uhr",
		"Fehler in app.js:10 und main.py:42, siehe aboard.html",
		"std::vector und Foo::Bar",
		"Das war gut.Es geht weiter. Ja.Nein/Vielleicht",
		"Dr.med Berg, evtl.noch, bzw.die",
		"Kap. 3, Seite 12",
	}
	for _, in := range bleibt {
		got, _ := nacharbeitLauf(t, in)
		if got != in {
			t.Errorf("zu viel ersetzt:\n%q\n%q", in, got)
		}
	}
}

// Kritiker KB-1: Dateiendungen, die zugleich Domain-Endung sind, gelten ohne
// Zeilennummer als Adresse.
func TestFesteStufe_EndungAuchDomain(t *testing.T) {
	for _, in := range []string{"siehe kunde.rs", "Skript von acme.sh", "Wiki firma.md", "api.acme.py"} {
		if got, _ := nacharbeitLauf(t, in); !strings.Contains(got, "[URL_1]") {
			t.Errorf("nicht ersetzt: %q", got)
		}
	}
	if in := "Fehler in main.py:42 und app.js:10"; func() string { g, _ := nacharbeitLauf(t, in); return g }() != in {
		t.Errorf("Datei mit Zeilennummer ersetzt")
	}
}

// F-1637: verschleierte IPv4, IPv6-Zone und Klammerform mit Port gehen
// nicht hinaus.
func TestFesteStufe_IPVerschleiertUndZone(t *testing.T) {
	for in, rest := range map[string]string{
		"Server 10[.]0[.]0[.]5 und 10 (dot) 0 (dot) 0 (dot) 6": "Server [URL_1] und [URL_2]",
		"Link fe80::1ff:fe23:4567:890a%eth0 aktiv":             "Link [URL_1] aktiv",
		"siehe [2001:db8::1]:443.":                             "siehe [URL_1].",
	} {
		if got, _ := nacharbeitLauf(t, in); got != rest {
			t.Errorf("%q → %q, erwartet %q", in, got, rest)
		}
	}
}

// Verify VB-2/VB-3: Programm-Bezeichner werden bewusst ersetzt (F-1637,
// Abwägung im hostFinder-Kommentar); ein Satzpunkt hinter einer IPv6-Zone
// bleibt beim Satz.
func TestFesteStufe_BezeichnerUndZonenende(t *testing.T) {
	for in, want := range map[string]string{
		"siehe req.body und console.error": "siehe [URL_1] und [URL_2]",
		"Link fe80::1%eth0. Danach":        "Link [URL_1]. Danach",
		"Link fe80::1%eth0-":               "Link [URL_1]-",
	} {
		if got, _ := nacharbeitLauf(t, in); got != want {
			t.Errorf("%q → %q, erwartet %q", in, got, want)
		}
	}
}
