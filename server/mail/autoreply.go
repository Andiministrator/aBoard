package mail

// autoreply.go — Mail-Auto-Reply nach erfolgreichem No-Match-Ingest (#297).
//
// SendAutoReply sendet eine optionale Bestätigungs-Mail an den ursprünglichen
// Absender, wenn eine neue Karte im Service-Board angelegt wurde.
//
// Anti-Loop-Schichten (Reihenfolge der Checks in SendAutoReply):
//  1. cfg.Enabled=false → opt-in (Privacy-Default OFF).
//  2. isNewCard=false → nur bei No-Match (Lifetime-Cap-Bolt #1).
//  3. Eigenversand-Check → keine Reply an die eigene Absender-Adresse.
//  4. DB-Lifetime-Cap → (sender, card_id) werden genau einmal gesendet.
//  5. 1/h-Rate-Limit → schützt vor Burst bei defekten Mail-Loops.
//
// RFC-3834-Header (Auto-Submitted: auto-replied + X-Loop: aboard) auf
// ausgehenden Mails → loopHeaderSkip in processor.go erkennt sie und
// droppt etwaige Bounces / Out-of-Office-Replies.

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

//go:embed templates/auto-reply.md.tmpl
var defaultTemplateFS embed.FS

// AutoReplyConfig bündelt die mail_auto_reply.*-Keys aus dem ConfigStore.
type AutoReplyConfig struct {
	Enabled  bool
	Template string // leer → Default-Template aus templates/auto-reply.md.tmpl
	From     string // leer → smtp.from
	ReplyTo  string // leer → cfg.From
}

// AutoReplyDeps bündelt Abhängigkeiten für SendAutoReply.
type AutoReplyDeps struct {
	SMTP    Config
	AutoCfg AutoReplyConfig
	LogRepo *storage.AutoReplyLogRepo
	Logger  func(string, ...any)
}

// templateData wird in das Template eingespeist.
type templateData struct {
	TicketId  int
	CardTitle string
	BoardName string
}

// -- In-memory-Rate-Limit (1/h pro Sender) -----------------------------------

var (
	rateMu      sync.Mutex
	rateMap     = map[string]time.Time{}
	rateCleanup = time.Now()
)

// rateAllow gibt true zurück, wenn der Sender in der letzten Stunde noch
// keine Auto-Reply erhalten hat. Schreibt gleichzeitig den neuen Zeitstempel.
// Cleanup der Map erfolgt periodisch (alle 10 Minuten) beim Aufruf.
func rateAllow(sender string) bool {
	rateMu.Lock()
	defer rateMu.Unlock()
	now := time.Now()
	// Periodischer Cleanup, damit die Map nicht unbegrenzt wächst.
	if now.Sub(rateCleanup) > 10*time.Minute {
		for k, ts := range rateMap {
			if now.Sub(ts) > time.Hour {
				delete(rateMap, k)
			}
		}
		rateCleanup = now
	}
	if ts, exists := rateMap[sender]; exists && now.Sub(ts) < time.Hour {
		return false
	}
	rateMap[sender] = now
	return true
}

// isOwnFromAddr prüft, ob senderEmail == fromAddr (normalisiert).
// Verhindert Eigenversand-Loops.
func isOwnFromAddr(senderEmail, fromAddr string) bool {
	return strings.EqualFold(
		strings.TrimSpace(senderEmail),
		strings.TrimSpace(fromAddr),
	)
}

// Regex zum Erkennen vorhandener [aBoard #N]-Tags im Subject.
var tagRe = regexp.MustCompile(`\[aBoard\s+#\d+\]`)

// Regex zum Normalisieren mehrfacher Re:-Präfixe.
var rePrefixRe = regexp.MustCompile(`(?i)^(re:\s*)+`)

// buildSubject baut den Antwort-Subject aus dem Original-Subject.
//
//   - Leer → "[aBoard #<id>] (No subject)"
//   - Enthält bereits [aBoard #N] → Tag NICHT nochmal anhängen, aber Re: normalisieren.
//   - Sonst → "[aBoard #<id>] Re: <normalisierter-kern>"
func buildSubject(originalSubject string, displayID int) string {
	tag := fmt.Sprintf("[aBoard #%d]", displayID)
	orig := strings.TrimSpace(originalSubject)
	if orig == "" {
		return tag + " (No subject)"
	}
	// Schon ein Tag drin → nur Re:-Normalisierung, kein doppelter Tag.
	if tagRe.MatchString(orig) {
		// Wenn schon Re: vor dem Tag → nichts zu tun, zurückgeben.
		// Wenn kein Re: → Re: voranstellen.
		if rePrefixRe.MatchString(orig) {
			// Re: normalisieren: alles "Re:" am Anfang durch ein einzelnes ersetzen.
			rest := strings.TrimSpace(rePrefixRe.ReplaceAllString(orig, ""))
			return "Re: " + rest
		}
		return orig
	}
	// Kern: Re:-Präfixe entfernen, dann tag + "Re: <kern>".
	kern := strings.TrimSpace(rePrefixRe.ReplaceAllString(orig, ""))
	return tag + " Re: " + kern
}

// renderTemplate rendert das Template mit den gegebenen Daten. Wenn cfg.Template
// nicht leer ist, wird es als Go-text/template-String interpretiert; sonst wird
// das eingebettete Default-Template verwendet.
func renderTemplate(tmplSrc string, data templateData) (string, error) {
	if strings.TrimSpace(tmplSrc) == "" {
		raw, err := defaultTemplateFS.ReadFile("templates/auto-reply.md.tmpl")
		if err != nil {
			return "", fmt.Errorf("autoreply: default-template lesen: %w", err)
		}
		tmplSrc = string(raw)
	}
	t, err := template.New("autoreply").Parse(tmplSrc)
	if err != nil {
		return "", fmt.Errorf("autoreply: template parsen: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("autoreply: template rendern: %w", err)
	}
	return buf.String(), nil
}

// SendAutoReply sendet eine Auto-Reply-Mail an parsed.From, wenn alle
// Pflicht-Checks bestanden werden. Fehler werden nur geloggt — der Ingest-Flow
// darf nicht unterbrochen werden.
//
// ctx: Request-/Worker-Context (für DB-Ops).
// deps: SMTP + AutoReplyConfig + LogRepo + Logger.
// parsed: die bereits verarbeitete Inbound-Mail.
// cardID: die intern vergebene UUID der neu angelegten Karte.
// displayID: die menschenlesbare Ticket-Nummer (#N).
// cardTitle: der Karten-Titel (aus parsed.Subject).
// boardName: der Board-Titel (für das Template).
// isNewCard: true = No-Match-Pfad (neue Karte), false = Match-Pfad (Kommentar).
func SendAutoReply(
	ctx context.Context,
	deps AutoReplyDeps,
	parsed *ParsedMail,
	cardID string,
	displayID int,
	cardTitle string,
	boardName string,
	isNewCard bool,
) error {
	log := deps.Logger
	if log == nil {
		log = func(string, ...any) {}
	}

	// Hardstop 1: opt-in.
	if !deps.AutoCfg.Enabled {
		return nil
	}
	// Hardstop 2: nur bei No-Match (neue Karte).
	if !isNewCard {
		return nil
	}
	// Hardstop 3: leerer oder kaputtes From-Feld → kein Versand möglich.
	sender := strings.TrimSpace(parsed.From)
	if sender == "" {
		log("autoreply: From-Adresse leer — kein Versand")
		return nil
	}
	// Hardstop 4: Eigenversand-Loop.
	fromAddr := deps.AutoCfg.From
	if fromAddr == "" {
		fromAddr = deps.SMTP.From
	}
	if isOwnFromAddr(sender, fromAddr) {
		log("autoreply: Eigenversand-Loop verhindert (From=%s)", sender)
		return nil
	}
	// Hardstop 5: Lifetime-Cap (DB).
	if deps.LogRepo != nil {
		inserted, err := deps.LogRepo.TryRecord(ctx, sender, cardID)
		if err != nil {
			log("autoreply: db-lifetime-cap-fehler: %v — kein Versand", err)
			return nil
		}
		if !inserted {
			log("autoreply: bereits gesendet für (sender=%s, card=%s) — skip", sender, cardID)
			return nil
		}
	}
	// Hardstop 6: 1/h-Rate-Limit (in-memory).
	if !rateAllow(sender) {
		log("autoreply: rate-limit 1/h für %s — skip", sender)
		return nil
	}

	// Template rendern.
	data := templateData{
		TicketId:  displayID,
		CardTitle: cardTitle,
		BoardName: boardName,
	}
	bodyText, err := renderTemplate(deps.AutoCfg.Template, data)
	if err != nil {
		log("autoreply: template-render-fehler: %v — kein Versand", err)
		return nil
	}
	bodyHTML := RenderHTML(bodyText)

	// Subject bauen.
	subject := buildSubject(parsed.Subject, displayID)

	// Zusätzliche Header (Anti-Loop-RFC-3834 + Tracking).
	extraHeaders := map[string]string{
		"Auto-Submitted": "auto-replied",
		"X-Loop":         "aboard",
	}
	if parsed.MessageID != "" {
		extraHeaders["In-Reply-To"] = parsed.MessageID
		extraHeaders["References"] = parsed.MessageID
	}

	// Reply-To setzen.
	replyTo := deps.AutoCfg.ReplyTo
	if replyTo == "" {
		replyTo = fromAddr
	}
	if replyTo != "" && replyTo != fromAddr {
		extraHeaders["Reply-To"] = replyTo
	}

	// SMTP-Config anpassen: From überschreiben falls gesetzt.
	smtpCfg := deps.SMTP
	if deps.AutoCfg.From != "" {
		smtpCfg.From = deps.AutoCfg.From
	}

	msg := Message{
		To:       []string{sender},
		Subject:  subject,
		BodyText: bodyText,
		BodyHTML: bodyHTML,
		Headers:  extraHeaders,
	}

	// F-810: `SendMail` setzte intern `context.Background()` — der bereits
	// vorhandene Ingest-Context (Parameter `ctx`, bis hierher nur für die
	// DB-Ops benutzt) endete also vor dem SMTP-Versand. Ein Shutdown oder ein
	// abgebrochener Request beendete den Sendeversuch damit nicht.
	if err := SendMailContext(ctx, smtpCfg, msg); err != nil {
		log("autoreply: smtp-fehler für %s: %v", sender, err)
		return nil // Fehler loggen, nicht weiterleiten — Ingest darf nicht abbrechen
	}
	log("autoreply: gesendet an %s für Karte #%d (%s)", sender, displayID, cardID)
	return nil
}

// RenderAutoReplyPreview rendert Subject und Body mit Demo-Daten und gibt beide
// zurück (Karte #333 / AK13 aus #297). Kein SMTP-Versand, keine Seiteneffekte.
//
// WICHTIG — originalSubject ist NICHT der Kartentitel (Sprint-53-Kritiker):
// Der Echtbetrieb ruft `buildSubject(parsed.Subject, displayID)` mit dem
// ROH-Betreff der eingegangenen Mail. Der Kartentitel ist davon abgeleitet
// (`SanitizeTitle`: Whitespace gefaltet, Zeilenumbrüche zu Leerzeichen, leer →
// "(kein Betreff)", auf 200 Zeichen gekürzt). Die erste Fassung dieser
// Funktion nahm den Kartentitel als Subject-Quelle und behauptete im
// Kommentar, das sei „derselbe Pfad wie im Echtbetrieb" — dieselbe Funktion,
// aber ein anderes Argument. Vom Kritiker gemessen: 4 von 7 Betreff-Formen
// wichen ab, am deutlichsten der leere Betreff (echt „[aBoard #42] (No
// subject)" gegen Vorschau „[aBoard #42] Re: (kein Betreff)").
//
// Deshalb nimmt die Funktion beides getrennt entgegen. Wer nur einen
// Validitäts-Check will, ruft weiterhin TestAutoReplyTemplate.
func RenderAutoReplyPreview(cfg AutoReplyConfig, ticketID int, originalSubject, cardTitle, boardName string) (subject, body string, err error) {
	data := templateData{
		TicketId:  ticketID,
		CardTitle: cardTitle,
		BoardName: boardName,
	}
	body, err = renderTemplate(cfg.Template, data)
	if err != nil {
		return "", "", err
	}
	return buildSubject(originalSubject, ticketID), body, nil
}

// TestAutoReplyTemplate prüft, ob das Template valide und vollständig
// renderbar ist — ohne SMTP-Versand und ohne Subject-Aufbau.
//
// Seit Karte #333 ein dünner Wrapper um RenderAutoReplyPreview. Der
// Sprint-53-Kritiker hat zu Recht angemerkt, dass die Funktion nach dem Umbau
// keinen Aufrufer mehr hatte — ein „Vertrag", den niemand schliesst. Sie wird
// jetzt vom Testpaket benutzt (autoreply_test.go) und bleibt damit die
// schlanke Antwort auf die Frage „rendert das Template überhaupt?".
func TestAutoReplyTemplate(cfg AutoReplyConfig, ticketID int, cardTitle, boardName string) error {
	_, _, err := RenderAutoReplyPreview(cfg, ticketID, cardTitle, cardTitle, boardName)
	return err
}

// LoadAutoReplyConfig liest mail_auto_reply.*-Keys aus dem ConfigStore.
func LoadAutoReplyConfig(ctx context.Context, cfg *storage.ConfigRepo) (AutoReplyConfig, error) {
	c := AutoReplyConfig{}
	get := func(key string) string {
		v, err := cfg.Get(ctx, key)
		if err != nil {
			return ""
		}
		return v.Value
	}
	if v := get("mail_auto_reply.enabled"); v == "true" {
		c.Enabled = true
	}
	c.Template = get("mail_auto_reply.template")
	c.From = get("mail_auto_reply.from")
	c.ReplyTo = get("mail_auto_reply.reply_to")
	return c, nil
}
