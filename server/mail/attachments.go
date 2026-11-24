package mail

// attachments.go — Anhang-Filterung für #295 (ADR-0093 §7).
//
// Drei Filter-Stufen, in dieser Reihenfolge (jeder reduziert das Set):
//   1. FilterBlockedMIME   — gefährliche Dateitypen verwerfen (.exe, .scr, …)
//   2. FilterInlineTracking — Inline-Bilder (Tracking-Pixel, Social-Icons)
//                              die nirgends im Body referenziert sind und
//                              klein sind, ausfiltern.
//   3. DedupAttachments    — SHA-256-basiertes Dedup pro Mail (verhindert
//                              dass mehrfach eingebettete Bilder N-fach
//                              ans Attachment-Volume landen).
//
// Jeder Filter ist eine reine Funktion: Eingabe-Slice → (kept, dropped).
// Aufrufer (processor.go) entscheidet, wie die dropped-Liste in den
// Karten-Body-Hinweis fließt (composeCommentBody → „Verworfene Anhänge").

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// BlockedExtensions sind ausführbare/skript-fähige Dateitypen, die in Mail-
// Anhängen typische Malware-Verteilungs-Vektoren sind (ADR-0093 §7).
// Listenrückgabe statt Set für deterministische Iteration in Logs/Tests.
// F-442 (Sprint-49-Kritiker): Die ursprüngliche Liste deckte die klassischen
// PE-Endungen ab, aber nicht die heute dominanten Zustellwege. Seit dem
// Office-Makro-Block von 2022 kommt Mail-Malware überwiegend als Verknüpfung
// (.lnk), HTML-Application (.hta), Container (.iso/.img/.vhd) oder
// Skript-Wrapper (.wsf/.jse/.vbe) — keine davon war erfasst, und keine wird
// von der Magic-Byte-Stufe erkannt (LNK und ISO haben eigene Signaturen an
// anderen Offsets). Der Suffix-Trick war also geschlossen, während der direkte
// Weg offen stand.
//
// Bewusst weiterhin NICHT geblockt: Archive (.zip/.7z/.rar) und Dokumente —
// die dürfen in einem Ticket-Ingest legitim ankommen. Wer den Inhalt eines
// Archivs prüfen will, braucht Entpacken + rekursive Prüfung; das ist eine
// eigene Entscheidung (Zip-Bomben, Passwortschutz) und keine Endungsfrage.
var BlockedExtensions = []string{
	/* PE / Windows-Binaries */
	".exe", ".scr", ".com", ".dll", ".pif", ".msi", ".msp", ".mst",
	".appx", ".msix", ".cpl", ".msc",
	/* Skripte und Skript-Wrapper */
	".bat", ".cmd", ".vbs", ".vbe", ".js", ".jse", ".ps1", ".psm1",
	".wsf", ".wsh", ".sh", ".jar", ".jnlp",
	/* Verknüpfungen / Auto-Ausführung — der heute häufigste Vektor */
	".lnk", ".hta", ".scf", ".url", ".inf", ".reg", ".chm",
	".settingcontent-ms",
	/* Container, die als Umgehung von Mark-of-the-Web dienen */
	".iso", ".img", ".vhd", ".vhdx",
}

// BlockedMIMETypes ist eine zusätzliche Liste auf MIME-Type-Ebene — manche
// Mail-Clients geben den Typ statt der Endung an.
var BlockedMIMETypes = []string{
	"application/x-msdownload",
	"application/x-msdos-program",
	"application/x-msi",
	"application/javascript",
	"application/x-bat",
	"application/bat",
	"application/x-sh",
	"application/x-shellscript",
}

// FilterBlockedMIME teilt die Anhangs-Liste in (kept, dropped):
//   - dropped: Filename-Endung in BlockedExtensions ODER ContentType in
//     BlockedMIMETypes (jeweils case-insensitive).
//   - kept: alle anderen.
//
// Data der dropped-Einträge wird auf nil gesetzt — der Caller braucht
// nur Filename + ContentType für den Hinweis.
func FilterBlockedMIME(atts []ParsedAttachment) (kept, dropped []ParsedAttachment) {
	for _, a := range atts {
		if isBlocked(a) {
			a.Data = nil
			dropped = append(dropped, a)
			continue
		}
		kept = append(kept, a)
	}
	return kept, dropped
}

// normalizeFilename bereitet einen Anhangs-Dateinamen für den Endungs-Vergleich
// auf (F-360). Der naive `HasSuffix(strings.ToLower(name), ext)` war auf drei
// Wegen umgehbar:
//
//	"evil.exe "   — trailing Whitespace; Windows ignoriert ihn beim Öffnen
//	"evil.exe."   — trailing Punkt; ebenso
//	"evil.exe\u200b" — unsichtbare Steuer-/Zero-Width-Zeichen
//
// Zusätzlich werden Pfad-Anteile entfernt: ein Anhang „../../evil.exe" oder
// „C:\tmp\evil.exe" soll an seiner echten Endung gemessen werden.
func normalizeFilename(name string) string {
	s := strings.ToLower(name)
	// Pfad-Anteile weg (beide Trenner — Mail-Clients liefern beides).
	if i := strings.LastIndexAny(s, `/\`); i >= 0 {
		s = s[i+1:]
	}
	// NTFS-Alternate-Data-Stream abschneiden: "evil.exe::$DATA", "evil.exe:x.txt".
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	// F-443: Unsichtbares ZUERST entfernen. Vorher lief TrimSpace davor, wodurch
	// ein NBSP (U+00A0) oder Word Joiner (U+2060) zwischen Endung und trailing
	// Punkt den Trim wirkungslos machte. Statt einzelner Codepoints jetzt die
	// ganze Unicode-Kategorie Cf (Format-Zeichen) — plus Steuerzeichen und
	// U+00AD (Soft Hyphen), das Go unter Cf NICHT führt.
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '­' || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	// Trailing Punkte und Whitespace JEDER Art abschneiden ("evil.exe." → "evil.exe").
	return strings.TrimRightFunc(s, func(r rune) bool {
		return r == '.' || unicode.IsSpace(r)
	})
}

// hasBlockedExtension prüft die normalisierte Endung.
//
// **F-444 — die Segment-Prüfung ist wieder entfernt.** Sie sollte Doppelendungen
// wie „rechnung.exe.zip" fangen, kostete aber deutlich mehr als sie einbrachte:
//
//   - Schaden: `.com` ist zugleich ausführbare Endung UND die häufigste TLD.
//     Jeder Anhang mit Domain-Anteil im Namen flog raus — „Angebot-firma.com.pdf",
//     „Screenshot-kunde.com.png", „logs-mail.example.com.txt". In einem
//     Ticket-Ingest verschwinden so reihenweise legitime Kundenanhänge, ohne
//     Rückmeldung an den Absender. Ebenso `.js.` → „app.js.map", „vendor.js.gz".
//   - Nutzen: nahe null. Der gefährliche Fall ist „rechnung.pdf.exe" — den fängt
//     der normale Suffix-Vergleich. „rechnung.exe.zip" IST ein ZIP und wird beim
//     Öffnen nicht ausgeführt; enthält es doch eine getarnte Binary, greift die
//     Magic-Byte-Stufe.
//
// Der Schutz gegen Doppelendungen ruht damit auf `sniffExecutable` (Inhalt statt
// Name) — der robusteren Prüfung, die sich nicht per Dateiname umgehen lässt.
func hasBlockedExtension(filename string) bool {
	norm := normalizeFilename(filename)
	if norm == "" {
		// F-443: Bleibt nach der Normalisierung nichts übrig ("evil.exe/",
		// "evil.exe:", nur Steuerzeichen), ist das keine harmlose Eingabe — der
		// Absender kontrolliert das Feld vollständig. Vorher stand hier `false`
		// und damit ein Bypass.
		return true
	}
	for _, ext := range BlockedExtensions {
		if strings.HasSuffix(norm, ext) {
			return true
		}
	}
	return false
}

func isBlocked(a ParsedAttachment) bool {
	if a.Filename != "" && hasBlockedExtension(a.Filename) {
		return true
	}
	// Zweite Stufe (F-360): Inhalts-Sniffing. Ein umbenanntes „rechnung.pdf",
	// das in Wahrheit eine PE-/ELF-Binary oder ein Shell-Skript ist, hat weder
	// verdächtige Endung noch zwingend einen ehrlichen ContentType.
	if len(a.Data) > 0 && sniffExecutable(a.Data) {
		return true
	}
	if a.ContentType != "" {
		ct := strings.ToLower(strings.TrimSpace(strings.SplitN(a.ContentType, ";", 2)[0]))
		for _, mt := range BlockedMIMETypes {
			if ct == mt {
				return true
			}
		}
	}
	return false
}

// executableMagic sind Datei-Signaturen ausführbarer Formate (F-360, zweite
// Stufe). Bewusst kurz gehalten: nur Formate, die auf einem Zielsystem direkt
// ausgeführt werden können — keine Archive (die dürfen legitim ankommen) und
// keine Dokumente.
//
// Nur Signaturen ab 4 Byte stehen hier. Zwei-Byte-Marker (`MZ`, `#!`) sind zu
// kurz für einen reinen Präfix-Vergleich und werden unten gesondert
// plausibilisiert (F-445).
var executableMagic = [][]byte{
	{0x7f, 'E', 'L', 'F'},    // ELF — Linux-Binaries
	{0xca, 0xfe, 0xba, 0xbe}, // Mach-O Fat / Java-Class
	{0xfe, 0xed, 0xfa, 0xce}, // Mach-O 32
	{0xfe, 0xed, 0xfa, 0xcf}, // Mach-O 64
}

// looksLikePE prüft eine DOS/PE-Datei (.exe, .dll, .scr) über mehr als das
// `MZ`-Präfix (F-445): Der DOS-Header trägt bei Offset 0x3C den Zeiger auf den
// PE-Header, der mit "PE\0\0" beginnt. Beides zusammen ist praktisch nicht
// zufällig zu treffen.
//
// Der reine `MZ`-Vergleich blockierte sonst jede Datei, die zufällig so
// beginnt — eine CSV mit einem Kfz-Kennzeichen ("MZ-AB 123;Mainz") oder ein
// Bericht, der mit "MZ Mainz" anfängt, verlor damit stillschweigend seinen
// Anhang.
func looksLikePE(data []byte) bool {
	if len(data) < 0x40 || data[0] != 'M' || data[1] != 'Z' {
		return false
	}
	off := int(data[0x3c]) | int(data[0x3d])<<8 | int(data[0x3e])<<16 | int(data[0x3f])<<24
	if off < 0 || off+4 > len(data) {
		return false
	}
	return string(data[off:off+4]) == "PE\x00\x00"
}

// looksLikeShebang verlangt nach `#!` einen absoluten Pfad (F-445). Eine
// Dokumentation, die mit "#!/bin/bash im Text erklaert" beginnt, ist kein
// Skript — ein echtes Skript hat dort einen Interpreter-Pfad.
func looksLikeShebang(data []byte) bool {
	if len(data) < 4 || data[0] != '#' || data[1] != '!' {
		return false
	}
	i := 2
	for i < len(data) && (data[i] == ' ' || data[i] == '\t') {
		i++
	}
	return i < len(data) && data[i] == '/'
}

// sniffExecutable erkennt ausführbare Inhalte an ihrer Signatur, unabhängig
// von Dateiname und deklariertem ContentType.
func sniffExecutable(data []byte) bool {
	if looksLikePE(data) || looksLikeShebang(data) {
		return true
	}
	for _, magic := range executableMagic {
		if len(data) >= len(magic) && string(data[:len(magic)]) == string(magic) {
			return true
		}
	}
	return false
}

// InlineTrackingThresholdBytes: Inline-Image-Anhänge ≤ diesem Wert gelten
// als Tracking-Pixel und werden verworfen (ADR-0093 §7).
const InlineTrackingThresholdBytes = 1024

// FilterInlineTracking entfernt Inline-Anhänge (Content-Disposition: inline
// ODER ContentID gesetzt), die KLEIN sind UND deren ContentID nirgends im
// referencingBody erscheint. Das eliminiert Marketing-/Tracking-Pixel
// (typisch 1×1-PNG/GIF, < 1 KB), ohne echte Inline-Bilder zu verlieren.
//
// referencingBody = ParsedMail.Body (Plain-Text nach HTML-Strip). Wenn die
// ContentID dort als `cid:<id>` oder als String erscheint, gilt der Anhang
// als referenziert und bleibt erhalten.
func FilterInlineTracking(atts []ParsedAttachment, referencingBody string) (kept, dropped []ParsedAttachment) {
	for _, a := range atts {
		if !a.Inline && a.ContentID == "" {
			kept = append(kept, a)
			continue
		}
		isSmall := int64(len(a.Data)) <= InlineTrackingThresholdBytes
		referenced := a.ContentID != "" && (strings.Contains(referencingBody, "cid:"+a.ContentID) ||
			strings.Contains(referencingBody, a.ContentID))
		if isSmall && !referenced {
			a.Data = nil
			dropped = append(dropped, a)
			continue
		}
		kept = append(kept, a)
	}
	return kept, dropped
}

// DedupAttachments entfernt identische Anhänge (gleicher SHA-256 + gleicher
// Filename). Verhindert dass z. B. ein Header-Logo, das in mehreren
// Multipart-Teilen referenziert ist, doppelt im Attachment-Volume landet.
// Erste Vorkommen gewinnt, spätere werden in dropped übernommen
// (Hinweis-Format weniger relevant — Caller kann sie verwerfen oder
// in Log werfen).
func DedupAttachments(atts []ParsedAttachment) (kept, dropped []ParsedAttachment) {
	seen := map[string]bool{}
	for _, a := range atts {
		if len(a.Data) == 0 {
			// Anhänge ohne Daten (bereits verworfen / Metadata-only) passieren
			// dedup-frei durch.
			kept = append(kept, a)
			continue
		}
		h := sha256.Sum256(a.Data)
		key := a.Filename + "/" + hex.EncodeToString(h[:])
		if seen[key] {
			dropped = append(dropped, a)
			continue
		}
		seen[key] = true
		kept = append(kept, a)
	}
	return kept, dropped
}
