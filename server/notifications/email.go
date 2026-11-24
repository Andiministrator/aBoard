package notifications

// email.go — E-Mail-Sender für den Notification-Subsystem.
//
// Nutzt server/mail.SendMailContext (#293-SMTP-Lib). Konfig kommt pro Send frisch
// aus dem ConfigStore (Reload-frei, keine SMTP-Client-Cache).

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	mailpkg "github.com/Andiministrator/aBoard/server/mail"
)

// SMTPConfigSource lädt die aktuelle SMTP-Config aus dem ConfigStore
// (oder Test-Stub). Injizierbar für Tests.
type SMTPConfigSource interface {
	LoadSMTP(ctx context.Context) (mailpkg.Config, error)
}

// EmailSender sendet Notification-Mails. Implementiert Sender.
type EmailSender struct {
	cfgSrc  SMTPConfigSource
	baseURL string // Deep-Link-Basis (z. B. "https://aboard.example/")
	// sendFunc ist injizierbar für Tests.
	//
	// F-810: Die Signatur trug bis Sprint 61 KEINEN Context. Der Worker baute
	// in `sendWithRetry` ein `context.WithTimeout(ctx, SendTimeout)` und
	// übergab es an `SendOne` — hier endete es dann, weil `mailpkg.SendMail`
	// intern `context.Background()` setzt. Der `SendTimeout` band den
	// E-Mail-Kanal also überhaupt nicht, obwohl die Paket-Doku des Workers
	// ausdrücklich mit ihm rechnet.
	sendFunc func(context.Context, mailpkg.Config, mailpkg.Message) error
}

// NewEmailSender legt einen Sender an. baseURL ist die Basis für Deep-Links
// — leer = kein Link in Mails. sendFunc nil = mailpkg.SendMailContext.
func NewEmailSender(cfgSrc SMTPConfigSource, baseURL string, sendFunc func(context.Context, mailpkg.Config, mailpkg.Message) error) *EmailSender {
	if sendFunc == nil {
		sendFunc = mailpkg.SendMailContext
	}
	return &EmailSender{cfgSrc: cfgSrc, baseURL: baseURL, sendFunc: sendFunc}
}

// Channel implementiert Sender.
func (s *EmailSender) Channel() string { return ChannelEmail }

// SendOne implementiert Sender. Lädt SMTP-Config, rendert Body, sendet.
func (s *EmailSender) SendOne(ctx context.Context, recipientUserID, recipientEmail, recipientName string, ev Event) error {
	if recipientEmail == "" {
		return errors.New("email: empty recipient address")
	}
	cfg, err := s.cfgSrc.LoadSMTP(ctx)
	if err != nil {
		return fmt.Errorf("email: load smtp cfg: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("email: smtp not configured: %w", err)
	}
	subject := buildEmailSubject(ev)
	text, htmlBody := buildEmailBody(ev, recipientName, s.baseURL)
	return s.sendFunc(ctx, cfg, mailpkg.Message{
		To:       []string{recipientEmail},
		Subject:  subject,
		BodyText: text,
		BodyHTML: htmlBody,
		Headers: map[string]string{
			"Auto-Submitted":              "auto-generated",
			"X-aBoard-Notification-Type":  ev.Type,
			"X-aBoard-Notification-Card":  ev.CardID,
			"X-aBoard-Notification-Board": ev.BoardID,
		},
	})
}

// buildEmailSubject — kanonisches Subject-Format aus Karte #305 §E-Mail-Kanal.
func buildEmailSubject(ev Event) string {
	label := HumanLabel(ev.Type)
	switch ev.Type {
	case EventBackupFailed:
		return fmt.Sprintf("[aBoard] %s: %s", label, ev.BoardName)
	case EventBoardAwaiting:
		return fmt.Sprintf("[aBoard] %s — %s", label, ev.BoardName)
	default:
		if ev.CardDisplayID > 0 {
			return fmt.Sprintf("[aBoard] %s: #%d — %s", label, ev.CardDisplayID, ev.CardTitle)
		}
		return fmt.Sprintf("[aBoard] %s", label)
	}
}

// buildEmailBody rendert Plain-Text + HTML parallel. Bewusst minimal —
// kein voller Karten-Inhalt (Karte #305 §Sicherheit).
func buildEmailBody(ev Event, recipientName, baseURL string) (text, htmlOut string) {
	greeting := "Hallo"
	if recipientName != "" {
		greeting = "Hallo " + recipientName
	}
	snippet := truncate(strings.TrimSpace(ev.Snippet), EmailSnippetMax)
	actor := ev.ActorEmail
	if actor == "" {
		actor = ev.ActorUserID
	}
	deepLink := buildDeepLink(baseURL, ev)

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s,\n\n", greeting)
	switch ev.Type {
	case EventCardAssigned:
		fmt.Fprintf(&sb, "Du wurdest einer Karte zugewiesen: #%d — %s\n", ev.CardDisplayID, ev.CardTitle)
	case EventCardCommented:
		fmt.Fprintf(&sb, "Neuer Kommentar auf Karte #%d — %s\n", ev.CardDisplayID, ev.CardTitle)
	case EventCardMentioned:
		fmt.Fprintf(&sb, "Du wurdest in einem Kommentar erwähnt: #%d — %s\n", ev.CardDisplayID, ev.CardTitle)
	case EventCardMoved:
		fmt.Fprintf(&sb, "Karte #%d — %s wurde verschoben.\n", ev.CardDisplayID, ev.CardTitle)
	case EventCardBlocked:
		fmt.Fprintf(&sb, "Karte #%d — %s wurde als blockiert markiert.\n", ev.CardDisplayID, ev.CardTitle)
	case EventBoardAwaiting:
		fmt.Fprintf(&sb, "Auf dem Board „%s\" wartet eine Frage auf deine Antwort.\n", ev.BoardName)
	case EventServiceNewCard:
		fmt.Fprintf(&sb, "Neue Karte im Service-Board: #%d — %s\n", ev.CardDisplayID, ev.CardTitle)
	case EventSprintClosed:
		fmt.Fprintf(&sb, "Sprint wurde abgeschlossen: %s\n", ev.BoardName)
	case EventBackupFailed:
		fmt.Fprintf(&sb, "Das automatische Backup für Board „%s\" ist fehlgeschlagen.\n", ev.BoardName)
	case EventSubsystemOff:
		// Eigener Fall statt `default` (Sprint-59-Kritiker [K] P2): Der
		// Vorgabetext lautet „… — Details siehe Board." Für dieses Ereignis
		// gibt es kein Board — der Operator würde auf eine Stelle verwiesen,
		// an der nichts steht. Und der Grund ist hier nicht ein Auszug,
		// sondern DIE Nachricht; er stand vorher unter „Auszug:" und wurde
		// auf EmailSnippetMax gekürzt, wodurch ausgerechnet der Satz
		// wegfiel, für den das Ereignis existiert („es werden KEINE
		// Sicherungen mehr erstellt"). Deshalb steht er hier ungekürzt.
		fmt.Fprintf(&sb, "Ein Subsystem läuft NICHT, weil es fehlkonfiguriert ist:\n\n%s\n",
			strings.TrimSpace(ev.Snippet))
		fmt.Fprintf(&sb, "\nDieser Zustand behebt sich nicht von selbst und wiederholt sich bei jedem Serverstart.\n")
	default:
		fmt.Fprintf(&sb, "%s — Details siehe Board.\n", HumanLabel(ev.Type))
	}
	if actor != "" && ev.Type != EventBackupFailed {
		fmt.Fprintf(&sb, "Auslöser: %s\n", actor)
	}
	// Bei EventSubsystemOff steht der Grund oben bereits vollständig — ein
	// zweites Mal gekürzt als „Auszug" wäre Rauschen.
	if snippet != "" && ev.Type != EventSubsystemOff {
		fmt.Fprintf(&sb, "\nAuszug:\n%s\n", snippet)
	}
	if deepLink != "" {
		fmt.Fprintf(&sb, "\nLink: %s\n", deepLink)
	}
	fmt.Fprintf(&sb, "\n—\naBoard — automatische Benachrichtigung. Einstellungen → 🔔 Benachrichtigungen, falls du dies nicht mehr erhalten willst.\n")
	text = sb.String()
	htmlOut = textToBasicHTML(text, deepLink)
	return text, htmlOut
}

// truncate kappt s auf max Zeichen mit Ellipsis (per Rune, kein Byte-Cut).
func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// textToBasicHTML wandelt den Plain-Text in einfaches HTML (für den HTML-
// Part der Multipart-Mail). Bewusst minimal — keine HTML-Mail-Magie.
func textToBasicHTML(text, deepLink string) string {
	escaped := html.EscapeString(text)
	escaped = strings.ReplaceAll(escaped, "\n", "<br>")
	body := "<div style=\"font-family:sans-serif;font-size:14px;line-height:1.4;\">" + escaped + "</div>"
	_ = deepLink // wird im Plain-Text-Part bereits ausgegeben — HTML-Part dupliziert ihn dort nicht zusätzlich
	return body
}

// buildDeepLink konstruiert den Karten-/Board-Link aus baseURL + Event.
// Leer wenn keine Basis konfiguriert ist.
//
// F-377: IDs werden escaped. Der Link ist ein Fragment im `key=value&key=value`-
// Format — eine ID, die selbst `&`, `=` oder `#` enthält, hängt sonst weitere
// Parameter an oder schneidet das Fragment ab und führt den Empfänger auf ein
// anderes Board/eine andere Karte. In der Praxis setzt das Frontend UUIDs, aber
// Events können auch aus server-internen Triggern und (perspektivisch) aus
// Client-Triggern stammen — der Escape kostet nichts und macht die Annahme
// „hier kommen immer UUIDs an" überflüssig.
//
// QueryEscape (nicht PathEscape): die IDs stehen als Werte in einer
// Parameter-Kette, dort müssen `&` und `=` kodiert werden — genau das tut
// QueryEscape, PathEscape lässt beide durch.
func buildDeepLink(baseURL string, ev Event) string {
	if baseURL == "" {
		return ""
	}
	base := strings.TrimRight(baseURL, "/")
	if ev.CardID != "" {
		return fmt.Sprintf("%s/#board=%s&card=%s",
			base, url.QueryEscape(ev.BoardID), url.QueryEscape(ev.CardID))
	}
	if ev.BoardID != "" {
		return fmt.Sprintf("%s/#board=%s", base, url.QueryEscape(ev.BoardID))
	}
	return base
}

// SendDigest sendet eine Tagesübersichts-Mail (18:00-Cron) — listet alle
// gedrosselten Events seit der letzten Digest-Sendung, ohne Detail-Snippets.
// Wird vom Digest-Worker aufgerufen.
func (s *EmailSender) SendDigest(ctx context.Context, recipientEmail, recipientName string, entries []digestItem, asOf time.Time) error {
	if recipientEmail == "" {
		return errors.New("email: empty recipient address (digest)")
	}
	cfg, err := s.cfgSrc.LoadSMTP(ctx)
	if err != nil {
		return fmt.Errorf("email: load smtp cfg (digest): %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("email: smtp not configured (digest): %w", err)
	}
	subject := fmt.Sprintf("[aBoard] Tagesübersicht — %d gedrosselte Ereignisse", len(entries))
	text := buildDigestText(recipientName, entries, asOf)
	return s.sendFunc(ctx, cfg, mailpkg.Message{
		To:       []string{recipientEmail},
		Subject:  subject,
		BodyText: text,
		BodyHTML: textToBasicHTML(text, ""),
		Headers: map[string]string{
			"Auto-Submitted":             "auto-generated",
			"X-aBoard-Notification-Type": "digest",
		},
	})
}

// digestItem ist die kompakte Form pro Eintrag im Digest. Wird vom
// Digest-Worker befüllt (digest.go).
type digestItem struct {
	EventType string
	Summary   string
	TS        time.Time
}

// buildDigestText rendert die Tagesübersicht. Eine Zeile pro Eintrag —
// kein Detail-Snippet (Spec §Tagesübersicht: „listet alle gedrosselten
// Events ohne Detail").
func buildDigestText(recipientName string, items []digestItem, asOf time.Time) string {
	greeting := "Hallo"
	if recipientName != "" {
		greeting = "Hallo " + recipientName
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s,\n\nin den letzten 24 Stunden wurden %d Notification(s) gedrosselt (30/h-Limit überschritten).\n\n",
		greeting, len(items))
	for _, it := range items {
		fmt.Fprintf(&sb, "  [%s] %s — %s\n", it.TS.Local().Format("15:04"), HumanLabel(it.EventType), it.Summary)
	}
	fmt.Fprintf(&sb, "\nStand: %s\n", asOf.Local().Format("2006-01-02 15:04 MST"))
	fmt.Fprintf(&sb, "\n—\naBoard — automatische Tagesübersicht. Einstellungen → 🔔 Benachrichtigungen.\n")
	return sb.String()
}
