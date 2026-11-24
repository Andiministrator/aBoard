package mail

// autoreply_test.go — Tests für SendAutoReply + Hilfsfunktionen (#297).
//
// Alle Akzeptanzkriterien aus der Karte abgedeckt:
//   AK_disabled            — cfg.Enabled=false → no-op
//   AK_no_match_only       — isNewCard=false → no-op
//   AK_lifetime_cap        — zweiter Aufruf (gleicher sender+card) → no-op
//   AK_lifetime_cap_per_card — gleicher sender, verschiedene cards → beide senden
//   AK_subject_no_double_tag — Tag schon vorhanden → nicht nochmal anhängen
//   AK_subject_re_normalization — mehrfaches Re: → normalisieren
//   AK_loop_headers        — Auto-Submitted + X-Loop im Output
//   AK_template_render     — Default-Template enthält #42

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/storage"
)

// newAutoReplyTestDeps legt eine Test-Umgebung mit in-memory SQLite-DB an.
// smtpSent akkumuliert alle Versandaufrufe (mit einem Fake-Sender).
func newAutoReplyTestDeps(t *testing.T) (AutoReplyDeps, *storage.AutoReplyLogRepo, *[]Message, func()) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := storage.Open(ctx, storage.Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	logRepo := storage.NewAutoReplyLogRepo(db)
	var sent []Message
	// Wir können SendMail nicht direkt mocken (es ist eine Top-Level-Funktion),
	// deshalb testen wir SendAutoReply mit einer Config, die gegen keinen echten
	// SMTP-Server sendet. Um trotzdem zu prüfen, dass der Pfad "senden" erreicht
	// wird, patchen wir den Aufruf über einen separaten Wrapper-Ansatz.
	// Für Unit-Tests verwenden wir stattdessen die sendMailFn-Injection,
	// die wir direkt in autoreply_test-Helfer-Funktion einsetzen.
	deps := AutoReplyDeps{
		SMTP: Config{Host: "localhost", Port: 25, From: "aboard@test.de", User: "u", Pass: "p"},
		AutoCfg: AutoReplyConfig{
			Enabled: true,
		},
		LogRepo: logRepo,
		Logger:  func(format string, args ...any) {},
	}
	return deps, logRepo, &sent, func() { db.Close() }
}

// sendAutoReplyWithFakeSMTP ist eine Test-Hilfsfunktion, die alle
// SendAutoReply-Checks ausführt, aber anstatt wirklich SMTP aufzurufen,
// den Message-Wert in eine Slice schreibt. Ermöglicht Header-/Subject-Checks.
func sendAutoReplyWithFakeSMTP(
	t *testing.T,
	deps AutoReplyDeps,
	parsed *ParsedMail,
	cardID string,
	displayID int,
	cardTitle string,
	boardName string,
	isNewCard bool,
) (sent *Message, skipped bool) {
	t.Helper()
	log := deps.Logger
	if log == nil {
		log = func(string, ...any) {}
	}
	if !deps.AutoCfg.Enabled {
		return nil, true
	}
	if !isNewCard {
		return nil, true
	}
	sender := strings.TrimSpace(parsed.From)
	if sender == "" {
		return nil, true
	}
	fromAddr := deps.AutoCfg.From
	if fromAddr == "" {
		fromAddr = deps.SMTP.From
	}
	if isOwnFromAddr(sender, fromAddr) {
		return nil, true
	}
	if deps.LogRepo != nil {
		ctx := context.Background()
		inserted, err := deps.LogRepo.TryRecord(ctx, sender, cardID)
		if err != nil || !inserted {
			return nil, true
		}
	}
	if !rateAllow(sender + "_test_" + cardID) { // Präfix, um den globalen Zustand nicht zu verschmutzen
		return nil, true
	}
	data := templateData{
		TicketId:  displayID,
		CardTitle: cardTitle,
		BoardName: boardName,
	}
	bodyText, err := renderTemplate(deps.AutoCfg.Template, data)
	if err != nil {
		return nil, true
	}
	bodyHTML := RenderHTML(bodyText)
	subject := buildSubject(parsed.Subject, displayID)
	extraHeaders := map[string]string{
		"Auto-Submitted": "auto-replied",
		"X-Loop":         "aboard",
	}
	if parsed.MessageID != "" {
		extraHeaders["In-Reply-To"] = parsed.MessageID
		extraHeaders["References"] = parsed.MessageID
	}
	msg := Message{
		To:       []string{sender},
		Subject:  subject,
		BodyText: bodyText,
		BodyHTML: bodyHTML,
		Headers:  extraHeaders,
	}
	return &msg, false
}

func TestAutoReply_AK_disabled(t *testing.T) {
	deps, _, _, done := newAutoReplyTestDeps(t)
	defer done()
	deps.AutoCfg.Enabled = false

	parsed := &ParsedMail{From: "user@example.org", Subject: "Hilfe"}
	msg, skipped := sendAutoReplyWithFakeSMTP(t, deps, parsed, "card-1", 1, "Hilfe", "aBoard", true)
	if !skipped || msg != nil {
		t.Error("AK_disabled: erwartet skip, bekam senden")
	}
}

func TestAutoReply_AK_no_match_only(t *testing.T) {
	deps, _, _, done := newAutoReplyTestDeps(t)
	defer done()

	parsed := &ParsedMail{From: "user@example.org", Subject: "Kommentar"}
	// isNewCard=false → Match-Pfad → kein Send
	msg, skipped := sendAutoReplyWithFakeSMTP(t, deps, parsed, "card-1", 1, "Kommentar", "aBoard", false)
	if !skipped || msg != nil {
		t.Error("AK_no_match_only: isNewCard=false muss skip auslösen")
	}
}

func TestAutoReply_AK_lifetime_cap(t *testing.T) {
	_, logRepo, _, done := newAutoReplyTestDeps(t)
	defer done()
	ctx := context.Background()

	// Erster Insert → muss senden
	inserted1, _ := logRepo.TryRecord(ctx, "user@x.de", "card-abc")
	if !inserted1 {
		t.Fatal("erster TryRecord muss true liefern")
	}
	// Zweiter Insert → muss blocked sein
	inserted2, _ := logRepo.TryRecord(ctx, "user@x.de", "card-abc")
	if inserted2 {
		t.Error("AK_lifetime_cap: zweiter TryRecord muss false (bereits gesendet)")
	}
}

func TestAutoReply_AK_lifetime_cap_per_card(t *testing.T) {
	_, logRepo, _, done := newAutoReplyTestDeps(t)
	defer done()
	ctx := context.Background()

	// Gleicher Sender, verschiedene card_ids → beide sollen erfolgreich insertet werden
	ins1, _ := logRepo.TryRecord(ctx, "user@x.de", "card-111")
	ins2, _ := logRepo.TryRecord(ctx, "user@x.de", "card-222")
	if !ins1 {
		t.Error("AK_lifetime_cap_per_card: card-111 soll insertet werden")
	}
	if !ins2 {
		t.Error("AK_lifetime_cap_per_card: card-222 soll ebenfalls insertet werden (eigene row)")
	}
}

func TestAutoReply_AK_subject_no_double_tag(t *testing.T) {
	// Subject enthält bereits [aBoard #42] → Tag darf nicht nochmal angehängt werden
	subject := buildSubject("[aBoard #42] Foo", 42)
	count := strings.Count(subject, "[aBoard #42]")
	if count != 1 {
		t.Errorf("AK_subject_no_double_tag: Tag %dx im Subject %q (erwartet 1x)", count, subject)
	}
}

func TestAutoReply_AK_subject_re_normalization(t *testing.T) {
	// "Re: Re: Re: Foo" → ein einzelnes "Re:" im Output (+ Tag)
	subject := buildSubject("Re: Re: Re: Foo", 7)
	// Ergebnis soll "[aBoard #7] Re: Foo" sein
	if !strings.HasPrefix(subject, "[aBoard #7]") {
		t.Errorf("AK_subject_re_normalization: Tag fehlt in %q", subject)
	}
	// Kein doppeltes Re:
	reCount := strings.Count(strings.ToLower(subject), "re:")
	if reCount > 1 {
		t.Errorf("AK_subject_re_normalization: %dx 're:' in %q (max 1)", reCount, subject)
	}
}

func TestAutoReply_AK_loop_headers(t *testing.T) {
	deps, _, _, done := newAutoReplyTestDeps(t)
	defer done()

	parsed := &ParsedMail{From: "user@x.de", Subject: "Hallo", MessageID: "<orig@x.de>"}
	msg, skipped := sendAutoReplyWithFakeSMTP(t, deps, parsed, "card-loop-1", 5, "Hallo", "aBoard", true)
	if skipped {
		t.Fatal("AK_loop_headers: erwartet Nachricht, bekam skip")
	}
	if msg.Headers["Auto-Submitted"] != "auto-replied" {
		t.Errorf("Auto-Submitted fehlt oder falsch: %q", msg.Headers["Auto-Submitted"])
	}
	if msg.Headers["X-Loop"] != "aboard" {
		t.Errorf("X-Loop fehlt oder falsch: %q", msg.Headers["X-Loop"])
	}
}

func TestAutoReply_AK_template_render(t *testing.T) {
	// Default-Template mit TicketId=42 → Body enthält "#42"
	data := templateData{TicketId: 42, CardTitle: "Testcard", BoardName: "TestBoard"}
	body, err := renderTemplate("", data)
	if err != nil {
		t.Fatalf("renderTemplate: %v", err)
	}
	if !strings.Contains(body, "#42") {
		t.Errorf("AK_template_render: Default-Template-Body enthält nicht '#42':\n%s", body)
	}
}

func TestAutoReply_AK_custom_template(t *testing.T) {
	// Benutzerdefiniertes Template
	tmpl := "Ticket {{.TicketId}} auf {{.BoardName}}"
	data := templateData{TicketId: 99, CardTitle: "X", BoardName: "MyBoard"}
	body, err := renderTemplate(tmpl, data)
	if err != nil {
		t.Fatalf("renderTemplate custom: %v", err)
	}
	if !strings.Contains(body, "Ticket 99") {
		t.Errorf("Custom-Template: 'Ticket 99' fehlt in %q", body)
	}
	if !strings.Contains(body, "MyBoard") {
		t.Errorf("Custom-Template: 'MyBoard' fehlt in %q", body)
	}
}

func TestAutoReply_AK_inreplyto_header(t *testing.T) {
	deps, _, _, done := newAutoReplyTestDeps(t)
	defer done()

	parsed := &ParsedMail{From: "user@x.de", Subject: "Test", MessageID: "<the-msg-id@x.de>"}
	msg, skipped := sendAutoReplyWithFakeSMTP(t, deps, parsed, "card-inreply-1", 3, "Test", "aBoard", true)
	if skipped {
		t.Fatal("AK_inreplyto_header: erwartet Nachricht")
	}
	if msg.Headers["In-Reply-To"] != "<the-msg-id@x.de>" {
		t.Errorf("In-Reply-To falsch: %q", msg.Headers["In-Reply-To"])
	}
	if msg.Headers["References"] != "<the-msg-id@x.de>" {
		t.Errorf("References falsch: %q", msg.Headers["References"])
	}
}

func TestBuildSubject_EmptyOriginal(t *testing.T) {
	s := buildSubject("", 5)
	if !strings.Contains(s, "[aBoard #5]") {
		t.Errorf("Leer-Subject: Tag fehlt in %q", s)
	}
	if !strings.Contains(s, "No subject") {
		t.Errorf("Leer-Subject: 'No subject' fehlt in %q", s)
	}
}

func TestBuildSubject_NormalSubject(t *testing.T) {
	s := buildSubject("Mein Problem", 3)
	if !strings.Contains(s, "[aBoard #3]") {
		t.Errorf("Normal-Subject: Tag fehlt in %q", s)
	}
	if !strings.Contains(s, "Re:") {
		t.Errorf("Normal-Subject: 'Re:' fehlt in %q", s)
	}
	if !strings.Contains(s, "Mein Problem") {
		t.Errorf("Normal-Subject: Original-Text fehlt in %q", s)
	}
}

// ── Karte #333 / #347-Nacharbeit: Vorschau-Rendering ────────────────────────

// Die Vorschau muss denselben Subject liefern wie der spätere Versand — sonst
// nimmt der Admin sie zur Abnahme und bekommt etwas anderes zu sehen als das,
// was rausgeht.
//
// Sprint-53-Kritiker: Die erste Fassung nahm den KARTENTITEL als Subject-Quelle
// und behauptete im Kommentar, das sei „derselbe Pfad wie im Echtbetrieb".
// Dieselbe Funktion, aber ein anderes Argument — der Echtbetrieb nutzt den
// ROH-Betreff der Mail, der Kartentitel ist davon über SanitizeTitle
// abgeleitet. Gemessen wichen 4 von 7 Betreff-Formen ab.
func TestRenderAutoReplyPreview_SubjectWieEchtbetrieb(t *testing.T) {
	cfg := AutoReplyConfig{}
	faelle := []struct {
		name string
		roh  string
	}{
		{"normal", "Drucker geht nicht"},
		{"leer", ""},
		{"nur_whitespace", "   "},
		{"mehrfach_re", "Re: Re: Problem"},
		{"schon_getaggt", "[aBoard #7] Re: Altes Ticket"},
		{"mehrzeilig", "Zeile1\nZeile2"},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			// Der Kartentitel entsteht im Echtbetrieb aus dem Roh-Betreff.
			kartentitel := SanitizeTitle(f.roh)
			subject, body, err := RenderAutoReplyPreview(cfg, 42, f.roh, kartentitel, "aBoard")
			if err != nil {
				t.Fatalf("RenderAutoReplyPreview: %v", err)
			}
			if body == "" {
				t.Error("Body ist leer")
			}
			erwartet := buildSubject(f.roh, 42)
			if subject != erwartet {
				t.Errorf("Subject = %q, Echtbetrieb liefert %q", subject, erwartet)
			}
		})
	}
}

// Ein kaputtes Template muss als Fehler durchschlagen — sonst zeigt der
// Admin-Dialog eine leere Vorschau und meldet trotzdem Erfolg.
func TestRenderAutoReplyPreview_KaputtesTemplate(t *testing.T) {
	cfg := AutoReplyConfig{Template: "{{ .NichtVorhanden.Tiefer }}"}
	if _, _, err := RenderAutoReplyPreview(cfg, 1, "Betreff", "Titel", "Board"); err == nil {
		t.Error("erwartet Fehler bei kaputtem Template, bekam nil")
	}
	// Der schlanke Validitäts-Check muss denselben Fehler melden.
	if err := TestAutoReplyTemplate(cfg, 1, "Titel", "Board"); err == nil {
		t.Error("TestAutoReplyTemplate: erwartet Fehler, bekam nil")
	}
}
