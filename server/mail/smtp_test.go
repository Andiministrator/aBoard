package mail

import (
	"strings"
	"testing"
	"time"
)

// Sprint-40-Kritiker-Fix [K]P2/Sprint 40 + Härtungen: Header-Tests.
// buildMessageBytes ist private, aber Same-Package-Test darf direkt rufen.

func TestBuildMessage_HasMandatoryRFC5322Headers(t *testing.T) {
	cfg := Config{Host: "h", Port: 25, From: "from@x.de", FromName: "aBoard", User: "u", Pass: "p"}
	msg := Message{To: []string{"to@x.de"}, Subject: "Hi", BodyText: "txt", BodyHTML: "<p>html</p>"}
	body, err := buildMessageBytes(cfg, msg)
	if err != nil {
		t.Fatalf("buildMessageBytes: %v", err)
	}
	s := string(body)
	// textproto.MIMEHeader canonicalisiert Header-Namen — "Message-ID" wird
	// als "Message-Id" geschrieben. Empfänger-Mail-Clients sind case-
	// insensitive (RFC-5322), nur Form-Erwartung im Test anpassen.
	for _, want := range []string{"Date:", "Message-Id:", "Mime-Version: 1.0",
		"From:", "To: to@x.de", "Subject:", "multipart/alternative"} {
		if !strings.Contains(s, want) {
			t.Errorf("Pflicht-Header/Marker fehlt: %q\n--Body--\n%s", want, s)
		}
	}
}

func TestBuildMessage_BoundaryIsRandom(t *testing.T) {
	cfg := Config{Host: "h", Port: 25, From: "f@x.de", User: "u", Pass: "p"}
	msg := Message{To: []string{"t@x.de"}, Subject: "X", BodyText: "x", BodyHTML: "x"}
	a, _ := buildMessageBytes(cfg, msg)
	b, _ := buildMessageBytes(cfg, msg)
	if extractBoundary(string(a)) == extractBoundary(string(b)) {
		t.Error("Boundary nicht random — predictable (Multipart-Splitting möglich)")
	}
}

func extractBoundary(s string) string {
	const marker = `boundary="`
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func TestBuildMessage_UTF8SubjectIsRFC2047Encoded(t *testing.T) {
	cfg := Config{Host: "h", Port: 25, From: "f@x.de", User: "u", Pass: "p"}
	msg := Message{To: []string{"t@x.de"}, Subject: "Größe: über 10€", BodyText: "x", BodyHTML: "x"}
	body, _ := buildMessageBytes(cfg, msg)
	s := string(body)
	if strings.Contains(s, "Größe: über 10€") {
		t.Error("Subject ist nicht RFC-2047-kodiert (Klartext-UTF8 im Header)")
	}
	if !strings.Contains(s, "=?utf-8?") {
		t.Error("Subject sollte =?utf-8?q?...?= encoded sein")
	}
}

// AK_headers_in_output: Message mit Headers["X-Loop"]="aboard" →
// buildMessageBytes-Output enthält "X-Loop: aboard\r\n".
func TestBuildMessage_AdditionalHeadersInOutput(t *testing.T) {
	cfg := Config{Host: "h", Port: 25, From: "from@x.de", User: "u", Pass: "p"}
	msg := Message{
		To:       []string{"to@x.de"},
		Subject:  "Test",
		BodyText: "text",
		BodyHTML: "<p>html</p>",
		Headers: map[string]string{
			"X-Loop":         "aboard",
			"Auto-Submitted": "auto-replied",
		},
	}
	body, err := buildMessageBytes(cfg, msg)
	if err != nil {
		t.Fatalf("buildMessageBytes: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "X-Loop: aboard\r\n") {
		t.Errorf("X-Loop-Header fehlt im Output:\n%s", s)
	}
	if !strings.Contains(s, "Auto-Submitted: auto-replied\r\n") {
		t.Errorf("Auto-Submitted-Header fehlt im Output:\n%s", s)
	}
}

// AK_headers_stable_order: sortierte Reihenfolge der zusätzlichen Header.
func TestBuildMessage_AdditionalHeadersStableOrder(t *testing.T) {
	cfg := Config{Host: "h", Port: 25, From: "from@x.de", User: "u", Pass: "p"}
	msg := Message{
		To:       []string{"to@x.de"},
		Subject:  "Stable",
		BodyText: "t",
		BodyHTML: "h",
		Headers: map[string]string{
			"Z-Custom": "zzz",
			"A-Custom": "aaa",
		},
	}
	body, _ := buildMessageBytes(cfg, msg)
	s := string(body)
	posA := strings.Index(s, "A-Custom:")
	posZ := strings.Index(s, "Z-Custom:")
	if posA < 0 || posZ < 0 {
		t.Fatalf("Beide Header müssen im Output sein: A=%d Z=%d", posA, posZ)
	}
	if posA > posZ {
		t.Errorf("Header sollen alphabetisch sortiert sein (A vor Z)")
	}
}

// Validate erzwingt User+Pass — Sprint-40-Kritiker-Fix [S]P1.
func TestConfig_Validate_RequiresAuth(t *testing.T) {
	cases := map[string]Config{
		"no-user": {Host: "h", Port: 25, From: "f@x.de", Pass: "p"},
		"no-pass": {Host: "h", Port: 25, From: "f@x.de", User: "u"},
		"empty":   {Host: "h", Port: 25, From: "f@x.de"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := c.Validate(); err == nil {
				t.Errorf("erwartet Validate-Fehler (Auth-Pflicht), got nil")
			}
		})
	}
	ok := Config{Host: "h", Port: 25, From: "f@x.de", User: "u", Pass: "p"}
	if err := ok.Validate(); err != nil {
		t.Errorf("Vollständige Config: %v", err)
	}
}

// TestNormalisiereTimeout deckt F-832 (a) ab: bis Sprint 61 prüfte der Code
// `== 0`, ein negativer Wert kam also unverändert durch und schaltete beide
// F-718-Absicherungen still aus (der net.Dialer rechnet dann `now.Add(-1s)`,
// jeder Dial scheitert sofort mit „i/o timeout", newTimeoutConn wird No-Op).
func TestNormalisiereTimeout(t *testing.T) {
	const fallback = 15 * time.Second
	faelle := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"null nimmt den Default", 0, fallback},
		{"negativ nimmt den Default (F-832 a)", -1 * time.Second, fallback},
		{"stark negativ ebenso", -99 * time.Hour, fallback},
		{"positiver Wert bleibt", 3 * time.Second, 3 * time.Second},
		{"eine Nanosekunde ist positiv und bleibt", time.Nanosecond, time.Nanosecond},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			if got := normalisiereTimeout(f.in, fallback); got != f.want {
				t.Errorf("normalisiereTimeout(%v) = %v, erwartet %v", f.in, got, f.want)
			}
		})
	}
}
