package mail

// mime.go — MIME-Parser + Subject-/Body-Normalisierung für #295 (ADR-0093).
//
// Eingabe: Roh-Mail-Bytes (RFC-5322 + MIME). Ausgabe: ParsedMail mit:
//   - Subject (RFC-2047-decoded, Reply-Präfixe gestrippt)
//   - From (lowercase + getrimmt)
//   - Date (Time, RFC-5322-parsed; fallback Zero-Wert)
//   - MessageID (wörtlich inkl. <>)
//   - Headers (Map für Loop-Detection in §spam.go)
//   - Body (Plain-Text bevorzugt; sonst HTML → Plain via Strip)
//   - Attachments (mit Größenlimit pro Anhang)
//
// Pre-Spam-Filter (#spam.go) konsumiert Subject + Headers; Karten-Schreibe-
// Pfad (cardops.go) konsumiert Body + Attachments + MessageID.

import (
	"errors"
	"fmt"
	"html"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"time"

	gomessage "github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

// ParsedMail bündelt die fürs Routing+Karten-Schreiben relevanten Felder.
type ParsedMail struct {
	Subject         string              // RFC-2047-decoded, Reply-Strip-Subject
	OriginalSubject string              // RFC-2047-decoded, OHNE Reply-Strip (für Audit/Debug)
	From            string              // lowercase Mail-Adresse (Display-Name verworfen, Spoof-Schutz)
	Date            time.Time           // RFC-5322-parsed; IsZero wenn Header fehlt/kaputt
	MessageID       string              // mit <>-Klammern, wörtlich
	Headers         map[string][]string // Header-Map für Loop-Detection (Auto-Submitted, Precedence, X-Loop)
	Body            string              // Markdown-roher Body (Plain bevorzugt, sonst HTML-strip)
	BodyKind        string              // "plain", "html-converted", oder "empty"
	Attachments     []ParsedAttachment
	Size            int64 // Brutto-Größe der Mail-Bytes (vor Parse)
}

// ParsedAttachment beschreibt einen einzelnen Anhang.
type ParsedAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
	ContentID   string // multipart/related-Marker (Inline-Bild-Erkennung)
	Inline      bool   // Content-Disposition: inline
}

// ErrMailTooLarge signalisiert dass die Mail größer als maxBytes ist.
var ErrMailTooLarge = errors.New("mail: mail size exceeds limit")

// ErrParseFailed signalisiert, dass die Mail nicht parsebar ist
// (kaputter MIME-Aufbau, fehlende Header). Caller verschiebt sie in
// imap.error_folder.
var ErrParseFailed = errors.New("mail: parse failed")

// ParseMail parst eine rohe Mail. maxBytes limitiert die Bruttogröße; bei
// Überschreitung wird ErrMailTooLarge zurückgegeben und keine Daten geliefert.
// maxAttachmentBytes limitiert einzelne Anhänge (über-große Anhänge werden mit
// Hinweis-Markierung in Body übernommen, Daten verworfen).
func ParseMail(r io.Reader, maxBytes, maxAttachmentBytes int64) (*ParsedMail, error) {
	// Limit-Reader, um keine GBs zu schlucken
	limited := &io.LimitedReader{R: r, N: maxBytes + 1}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", ErrParseFailed, err)
	}
	size := int64(len(raw))
	if size > maxBytes {
		return nil, ErrMailTooLarge
	}

	entity, err := gomessage.Read(strings.NewReader(string(raw)))
	if err != nil && !gomessage.IsUnknownCharset(err) {
		// UnknownCharset ist tolerant — go-message liefert dann den Body
		// als Bytes ohne Conversion. Andere Fehler sind hart.
		return nil, fmt.Errorf("%w: gomessage.Read: %v", ErrParseFailed, err)
	}

	out := &ParsedMail{
		Size:    size,
		Headers: map[string][]string{},
	}

	// Header-Map befüllen — alle Werte (case-insensitive Lookup via TextProto)
	for fields := entity.Header.Fields(); fields.Next(); {
		k := strings.ToLower(fields.Key())
		v, _ := fields.Text()
		if v == "" {
			v = fields.Value()
		}
		out.Headers[k] = append(out.Headers[k], v)
	}

	// Subject (RFC-2047 via go-message)
	originalSubject := firstHeader(out.Headers, "subject")
	out.OriginalSubject = strings.TrimSpace(originalSubject)
	out.Subject = StripReplyPrefixes(out.OriginalSubject)

	// From — nur die Adresse, kein Display-Name (Spoof-Schutz)
	fromRaw := firstHeader(out.Headers, "from")
	if addr, err := mail.ParseAddress(fromRaw); err == nil {
		out.From = strings.ToLower(strings.TrimSpace(addr.Address))
	} else {
		// Fallback: roher String getrimmt + lowercase (besser als leer für
		// Spam-Filter-Lookup, auch wenn nicht RFC-konform)
		out.From = strings.ToLower(strings.TrimSpace(fromRaw))
	}

	// Date (RFC-5322)
	if dateRaw := firstHeader(out.Headers, "date"); dateRaw != "" {
		if t, err := mail.ParseDate(dateRaw); err == nil {
			out.Date = t
		}
	}

	// Message-ID (wörtlich erhalten)
	out.MessageID = strings.TrimSpace(firstHeader(out.Headers, "message-id"))

	// Body + Attachments via mail.Reader (höher-level multipart-Iteration)
	mr := gomail.NewReader(entity)
	if mr == nil {
		return nil, fmt.Errorf("%w: keine mail.Reader-Initialisierung", ErrParseFailed)
	}
	var plainBody, htmlBody string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: next part: %v", ErrParseFailed, err)
		}
		switch h := part.Header.(type) {
		case *gomail.InlineHeader:
			ct, _, _ := h.ContentType()
			data, err := io.ReadAll(part.Body)
			if err != nil {
				// Tolerant: bei kaputtem Part lieber weiterlesen
				continue
			}
			if strings.EqualFold(ct, "text/plain") && plainBody == "" {
				plainBody = string(data)
			} else if strings.EqualFold(ct, "text/html") && htmlBody == "" {
				htmlBody = string(data)
			}
		case *gomail.AttachmentHeader:
			filename, _ := h.Filename()
			ct, _, _ := h.ContentType()
			contentID, _ := h.Text("Content-ID")
			disp, _, _ := h.ContentDisposition()
			inline := strings.EqualFold(disp, "inline")

			// LimitedReader pro Anhang
			lr := &io.LimitedReader{R: part.Body, N: maxAttachmentBytes + 1}
			data, _ := io.ReadAll(lr)
			if int64(len(data)) > maxAttachmentBytes {
				// Über-großer Anhang — Daten verwerfen, Metadaten behalten
				// für Hinweis im Body (siehe processor.go)
				out.Attachments = append(out.Attachments, ParsedAttachment{
					Filename:    filename,
					ContentType: ct,
					ContentID:   strings.Trim(contentID, "<>"),
					Inline:      inline,
					Data:        nil, // bewusst weggelassen
				})
				continue
			}
			out.Attachments = append(out.Attachments, ParsedAttachment{
				Filename:    filename,
				ContentType: ct,
				ContentID:   strings.Trim(contentID, "<>"),
				Inline:      inline,
				Data:        data,
			})
		}
	}

	// Body-Wahl: Plain bevorzugt; sonst HTML→Plain via Strip
	if plainBody != "" {
		out.Body = TrimQuotedReply(plainBody)
		out.BodyKind = "plain"
	} else if htmlBody != "" {
		out.Body = TrimQuotedReply(StripHTML(htmlBody))
		out.BodyKind = "html-converted"
	} else {
		out.Body = ""
		out.BodyKind = "empty"
	}

	return out, nil
}

// firstHeader gibt den ersten Wert eines Headers (lowercase-Key) zurück.
func firstHeader(h map[string][]string, key string) string {
	if vs, ok := h[strings.ToLower(key)]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// --- Subject-/Body-Normalisierung -----------------------------------------

// replyPrefixRE matcht typische Reply-/Forward-Präfixe — case-insensitive,
// optional gefolgt von Klammer-Indikatoren wie "[2]" o. Ä.
//
// Iterativ angewandt, damit verschachtelte Replies ("Re: Fwd: Re: …")
// vollständig gestrippt werden.
var replyPrefixRE = regexp.MustCompile(`(?i)^\s*(re|fwd|fw|aw|wg|antw|antwort)\s*(\[\d+\])?\s*:\s*`)

// StripReplyPrefixes entfernt iterativ Reply-/Forward-Präfixe vom Subject.
// Gibt das saubere Subject zurück. Stoppt nach 10 Iterationen (Worst-Case-
// Schutz gegen absurd geschachtelte Mails).
func StripReplyPrefixes(subject string) string {
	s := subject
	for i := 0; i < 10; i++ {
		stripped := replyPrefixRE.ReplaceAllString(s, "")
		if stripped == s {
			break
		}
		s = stripped
	}
	return strings.TrimSpace(s)
}

// quoteHeaderRE matcht typische Quote-Header (vor zitierten Vorgänger-Mails).
// "On <date> <addr> wrote:" / "Am <date> schrieb <addr>:" / einfaches
// "Gesendet:" — Heuristik, deckt Outlook/Gmail/Apple/Thunderbird ab.
//
// Reihenfolge in (Am|On|Le)…(wrote|schrieb|écrit) ist flexibel: zwischen
// dem Sprach-Trigger und dem Verb darf beliebiger Text stehen, danach ggf.
// noch Sender-Name + ":". Ende der Zeile ist Pflichtanker.
var quoteHeaderRE = regexp.MustCompile(
	`(?im)^\s*(?:(?:Am|On|Le)\b.*\b(?:wrote|schrieb|écrit)\b[^\n]*:\s*$|` +
		`-{2,}\s*Original (?:Message|Nachricht)\s*-{2,}\s*$|` +
		`Von:\s+\S.*$|` +
		`From:\s+\S.*$)`)

// TrimQuotedReply schneidet ab der ersten erkannten Quote-Header-Linie
// alles ab. Konservativ: triggert nur bei klar erkannten Marker-Linien;
// im Zweifel bleibt der Body unverändert (False-Positive-Schutz).
//
// Zusätzlich: aufeinanderfolgende Leerzeilen am Ende werden gestrippt.
func TrimQuotedReply(body string) string {
	if body == "" {
		return body
	}
	loc := quoteHeaderRE.FindStringIndex(body)
	if loc == nil {
		return strings.TrimRight(body, "\r\n\t ")
	}
	return strings.TrimRight(body[:loc[0]], "\r\n\t ")
}

// --- HTML-Stripping -------------------------------------------------------

// htmlScriptRE entfernt komplette <script>-Blöcke (inkl. Inhalt).
// Go's RE2-Engine unterstützt keine Backreferences — daher zwei getrennte Regex.
var htmlScriptRE = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)

// htmlStyleRE entfernt komplette <style>-Blöcke (inkl. Inhalt).
var htmlStyleRE = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)

// htmlTagRE matcht jedes HTML-Tag.
// htmlTagRE matcht HTML-Tags. Der führende Namens-Anker (`</?[a-zA-Z]`) ist
// nicht kosmetisch, sondern korrektheitsrelevant (F-441, Sprint-49-Kritiker):
//
// Das frühere `<[^>]+>` fraß JEDE Zeichenfolge zwischen einem `<` und dem
// nächsten `>`. Solange das Entity-Decoding als letzter Schritt lief, fiel das
// nicht auf — dekodierte `&lt;`/`&gt;` waren danach reiner Text. Seit StripHTML
// nach jedem Decode-Schritt erneut strippt (F-362), traf es genau die einzige
// Repräsentation, in der Vergleichsoperatoren in HTML-Mail überhaupt vorkommen
// können: `Preis &lt; 100 und Menge &gt; 5` wurde zu `Preis  5`.
//
// Mit dem Anker überlebt „3 < 5, aber 7 > 2" (nach `<` steht kein Buchstabe),
// während `<script>`, `</script>` und `<img src=x onerror=…>` weiterhin fallen.
var htmlTagRE = regexp.MustCompile(`(?s)</?[a-zA-Z][^>]*>`)

// htmlBlockBreakRE matcht Block-Element-Open-/Close-Tags, die wir vor dem
// Tag-Strip durch Zeilenumbruch ersetzen — damit der Plain-Text lesbar bleibt.
var htmlBlockBreakRE = regexp.MustCompile(
	`(?i)</?(p|div|br|hr|h[1-6]|li|tr|table|blockquote|pre|article|section)\b[^>]*>`)

// StripHTML konvertiert HTML zu einem Plain-Text-Approximation. Bewusst
// einfach — kein voller HTML-Parser. Verlustbehaftet (Tabellen, Bilder,
// Listen-Marker), dokumentiert in ADR-0093 §3.
// stripTagsOnce führt einen vollständigen Tag-Entfernungs-Durchlauf aus:
// script/style-Blöcke samt Inhalt, Block-Elemente als Zeilenumbruch, Rest weg.
func stripTagsOnce(s string) string {
	s = htmlScriptRE.ReplaceAllString(s, "")
	s = htmlStyleRE.ReplaceAllString(s, "")
	s = htmlBlockBreakRE.ReplaceAllString(s, "\n")
	return htmlTagRE.ReplaceAllString(s, "")
}

// maxUnescapeRunden begrenzt die Fixpunkt-Iteration in StripHTML. Drei Runden
// decken einfach und doppelt encodierte Payloads ab; mehr ist in echter Mail
// nicht zu erwarten und die Grenze verhindert pathologische Eingaben.
const maxUnescapeRunden = 3

// StripHTML konvertiert HTML zu einem Plain-Text-Approximation. Bewusst
// einfach — kein voller HTML-Parser. Verlustbehaftet (Tabellen, Bilder,
// Listen-Marker), dokumentiert in ADR-0093 §3.
//
// F-362: Die frühere Reihenfolge „erst strippen, dann Entities decodieren"
// war als Defense-in-depth wertlos — ein Body mit `&lt;script&gt;` überlebte
// den Tag-Strip unbeschadet und wurde erst durch das nachgelagerte
// UnescapeString wieder zu echtem `<script>`, das dann in der Karten-
// Description landete. Der Browser-Sanitizer (ADR-0089) fängt das zwar ab,
// aber eine Schicht, die ihre eigene Ausgabe re-injiziert, ist keine Schicht.
//
// Deshalb jetzt Strip und Unescape abwechselnd bis zum Fixpunkt: nach jedem
// Decode-Schritt wird erneut gestrippt, bis Decoding nichts mehr verändert.
// Das erfasst auch doppelt encodierte Payloads (`&amp;lt;script&amp;gt;`).
//
// Trade-off: Text, der nach dem Decodieren wie ein echtes Tag aussieht
// (`a &lt;b&gt; c` → `a <b> c`), verliert diesen Teil — dasselbe Verhalten, das
// nicht-encodierter Text hier schon immer hatte.
//
// **Korrektur F-441:** Diese Begründung war ursprünglich zu weit gefasst und
// hat eine echte Regression verdeckt. In einem HTML-Body kommt ein literales
// `<` NUR als `&lt;` vor — „encodierter Text" ist also nicht der Sonderfall,
// sondern der Normalfall. Erst der Namens-Anker in `htmlTagRE` grenzt „sieht
// aus wie ein Tag" von „ist ein Vergleichsoperator" ab; ohne ihn zerstörte die
// Fixpunkt-Schleife jede Mail mit `&lt;` … `&gt;` im selben Absatz.
func StripHTML(htmlIn string) string {
	if htmlIn == "" {
		return ""
	}
	s := stripTagsOnce(htmlIn)
	for i := 0; i < maxUnescapeRunden; i++ {
		dec := html.UnescapeString(s)
		if dec == s {
			break // vollständig dekodiert und gestrippt
		}
		s = stripTagsOnce(dec)
	}
	// Whitespace normalisieren (mehrfache Leerzeilen → 2 Leerzeilen)
	s = normalizeWhitespace(s)
	return strings.TrimSpace(s)
}

// multiNewlineRE matcht 3+ aufeinanderfolgende Newlines.
var multiNewlineRE = regexp.MustCompile(`\n{3,}`)

// trailingWhitespaceRE matcht Whitespace am Ende einer Zeile.
var trailingWhitespaceRE = regexp.MustCompile(`[ \t]+\n`)

func normalizeWhitespace(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = trailingWhitespaceRE.ReplaceAllString(s, "\n")
	s = multiNewlineRE.ReplaceAllString(s, "\n\n")
	return s
}
