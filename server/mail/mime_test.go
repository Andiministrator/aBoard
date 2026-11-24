package mail

import (
	"strings"
	"testing"
)

func TestStripReplyPrefixes(t *testing.T) {
	cases := []struct {
		in, out string
	}{
		{"Re: Hallo", "Hallo"},
		{"Fwd: Re: Aw: Original", "Original"},
		{"WG: Wichtige Mail", "Wichtige Mail"},
		{"Antw: [aBoard #42] Bug", "[aBoard #42] Bug"},
		{"Re[2]: Verschachtelt", "Verschachtelt"},
		{"  re :   Whitespace  ", "Whitespace"},
		{"Kein Präfix", "Kein Präfix"},
		{"", ""},
	}
	for _, c := range cases {
		got := StripReplyPrefixes(c.in)
		if got != c.out {
			t.Errorf("StripReplyPrefixes(%q) = %q, want %q", c.in, got, c.out)
		}
	}
}

func TestTrimQuotedReply(t *testing.T) {
	cases := []struct {
		name string
		in   string
		out  string
	}{
		{
			name: "outlook-style",
			in:   "Mein Text.\n\nVon: andi@example.org\nGesendet: gestern\nBetreff: Test\n\n> Alter Text",
			out:  "Mein Text.",
		},
		{
			name: "gmail-style",
			in:   "Antwort hier.\n\nOn Mon, 5 May 2026, Andi <andi@x> wrote:\n> Original",
			out:  "Antwort hier.",
		},
		{
			name: "de-am-style",
			in:   "Hallo,\n\nAm 5.5.2026 schrieb andi:\n> Frage?",
			out:  "Hallo,",
		},
		{
			name: "no-quote",
			in:   "Einfache Mail ohne Zitat.",
			out:  "Einfache Mail ohne Zitat.",
		},
		{
			name: "original-message-marker",
			in:   "Mein Kommentar.\n\n----- Original Message -----\nDann das Original.",
			out:  "Mein Kommentar.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TrimQuotedReply(c.in)
			if got != c.out {
				t.Errorf("TrimQuotedReply: got %q, want %q", got, c.out)
			}
		})
	}
}

func TestStripHTML(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		contains []string
		excludes []string
	}{
		{
			name:     "basic",
			in:       "<p>Hallo <b>Welt</b></p>",
			contains: []string{"Hallo", "Welt"},
			excludes: []string{"<p>", "<b>", "</p>"},
		},
		{
			name:     "entities",
			in:       "<p>Tom &amp; Jerry &lt;3</p>",
			contains: []string{"Tom & Jerry <3"},
			excludes: []string{"&amp;", "&lt;"},
		},
		{
			name:     "script-style-killed",
			in:       "<style>body{color:red}</style><script>alert(1)</script><p>Sichtbar</p>",
			contains: []string{"Sichtbar"},
			excludes: []string{"alert", "color:red"},
		},
		{
			name:     "block-newlines",
			in:       "<p>Eins</p><p>Zwei</p>",
			contains: []string{"Eins", "Zwei", "\n"},
			excludes: []string{"<p>"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StripHTML(c.in)
			for _, sub := range c.contains {
				if !strings.Contains(got, sub) {
					t.Errorf("erwartet %q in %q", sub, got)
				}
			}
			for _, sub := range c.excludes {
				if strings.Contains(got, sub) {
					t.Errorf("nicht erwartet %q in %q", sub, got)
				}
			}
		})
	}
}

func TestParseMail_PlainText(t *testing.T) {
	raw := "From: andi@example.org\r\n" +
		"To: support@example.org\r\n" +
		"Subject: Re: Test\r\n" +
		"Date: Wed, 21 May 2026 10:00:00 +0000\r\n" +
		"Message-ID: <abc@example.org>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Mein Body."
	p, err := ParseMail(strings.NewReader(raw), 1024*1024, 1024*1024)
	if err != nil {
		t.Fatalf("ParseMail: %v", err)
	}
	if p.From != "andi@example.org" {
		t.Errorf("From = %q", p.From)
	}
	if p.Subject != "Test" {
		t.Errorf("Subject (Reply-Strip) = %q", p.Subject)
	}
	if p.OriginalSubject != "Re: Test" {
		t.Errorf("OriginalSubject = %q", p.OriginalSubject)
	}
	if p.MessageID != "<abc@example.org>" {
		t.Errorf("MessageID = %q", p.MessageID)
	}
	if !strings.Contains(p.Body, "Mein Body") {
		t.Errorf("Body fehlt: %q", p.Body)
	}
	if p.BodyKind != "plain" {
		t.Errorf("BodyKind = %q", p.BodyKind)
	}
	if p.Date.IsZero() {
		t.Errorf("Date sollte gesetzt sein")
	}
}

func TestParseMail_TooLarge(t *testing.T) {
	raw := "From: a@b.de\r\nSubject: X\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("x", 2000)
	_, err := ParseMail(strings.NewReader(raw), 100, 100) // 100 Byte Limit
	if err != ErrMailTooLarge {
		t.Errorf("erwartet ErrMailTooLarge, bekam %v", err)
	}
}

func TestParseMail_DisplayNameStripped(t *testing.T) {
	raw := `From: "Anna Beispiel" <andi@example.org>` + "\r\n" +
		"Subject: Test\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\nBody"
	p, err := ParseMail(strings.NewReader(raw), 4096, 4096)
	if err != nil {
		t.Fatalf("ParseMail: %v", err)
	}
	if p.From != "andi@example.org" {
		t.Errorf("Display-Name nicht gestrippt: From = %q", p.From)
	}
}

func TestParseMail_MultipartAlternative_PrefersPlain(t *testing.T) {
	raw := "From: a@b.de\r\n" +
		"Subject: Test\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=BND\r\n" +
		"\r\n" +
		"--BND\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"Plain-Body" +
		"\r\n--BND\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p>HTML-Body</p>" +
		"\r\n--BND--\r\n"
	p, err := ParseMail(strings.NewReader(raw), 4096, 4096)
	if err != nil {
		t.Fatalf("ParseMail: %v", err)
	}
	if !strings.Contains(p.Body, "Plain-Body") {
		t.Errorf("erwartet Plain-Body bevorzugt, bekam %q", p.Body)
	}
	if p.BodyKind != "plain" {
		t.Errorf("BodyKind = %q", p.BodyKind)
	}
}

func TestParseMail_HTMLOnly(t *testing.T) {
	raw := "From: a@b.de\r\n" +
		"Subject: Test\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<p>HTML-<b>Only</b></p>"
	p, err := ParseMail(strings.NewReader(raw), 4096, 4096)
	if err != nil {
		t.Fatalf("ParseMail: %v", err)
	}
	if p.BodyKind != "html-converted" {
		t.Errorf("BodyKind = %q, erwartet html-converted", p.BodyKind)
	}
	if !strings.Contains(p.Body, "HTML") || !strings.Contains(p.Body, "Only") {
		t.Errorf("HTML-Body nicht korrekt gestrippt: %q", p.Body)
	}
}
