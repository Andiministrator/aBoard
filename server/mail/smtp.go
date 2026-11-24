package mail

// smtp.go — SMTP-Versand via net/smtp (Stdlib) für #293 (ADR-0091).
//
// Bewusst KEIN externer Mail-Lib-Stack (gopkg.in/mail.v2 etc.) — die
// MIME-multipart-Konstruktion ist ~80 LOC. Hält die Server-Module-
// Dep-Liste minimal.

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/Andiministrator/aBoard/server/ausgang"
	"mime"
	"net"
	"net/smtp"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config bündelt SMTP-Parameter. Werte stammen aus dem ConfigStore
// (#306/ADR-0101: smtp.host/port/user/pass/from/from_name/starttls).
type Config struct {
	Host     string
	Port     int
	User     string
	Pass     string
	From     string
	FromName string
	STARTTLS bool
	Timeout  time.Duration
}

// ErrSMTPNotConfigured signalisiert dass keine SMTP-Konfiguration vorhanden
// ist. Endpunkt antwortet 503.
var ErrSMTPNotConfigured = errors.New("mail: smtp not configured")

// ErrAuthFailed signalisiert dass SMTP-AUTH fehlgeschlagen ist (falsche
// Credentials). Operator-Aufgabe.
var ErrAuthFailed = errors.New("mail: smtp auth failed")

// Validate prüft die Pflichtfelder. Aufrufer-pre-flight, damit der Test-Hook
// klare Fehlermeldungen liefert ("smtp.host fehlt").
//
// Sprint-40-Kritiker-Fix [S]P1: AUTH ist Pflicht (User+Pass nicht-leer) —
// sonst Open-Relay-Risk (ADR-0091 §8). Wer bewusst gegen einen lokal
// vertrauenswürdigen MTA ohne AUTH senden will, kann den Schutz explizit
// per `smtp.allow_no_auth = true` aushebeln (Folge-Karte falls je gebraucht;
// MVP-Konvention: kein Versand ohne AUTH).
func (c Config) Validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("%w: smtp.host fehlt", ErrSMTPNotConfigured)
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("%w: smtp.port ungültig: %d", ErrSMTPNotConfigured, c.Port)
	}
	if strings.TrimSpace(c.From) == "" {
		return fmt.Errorf("%w: smtp.from fehlt", ErrSMTPNotConfigured)
	}
	if strings.TrimSpace(c.User) == "" || strings.TrimSpace(c.Pass) == "" {
		return fmt.Errorf("%w: smtp.user/smtp.pass sind Pflicht (kein Versand ohne AUTH, ADR-0091 §8)", ErrSMTPNotConfigured)
	}
	return nil
}

// Message ist die abstrakte Repräsentation einer zu versendenden Mail.
type Message struct {
	To       []string // RFC-5322 addr-spec; max-Empfänger prüft der Caller (Rate-Limit/Spam-Schutz)
	CC       []string
	Subject  string
	BodyText string            // Plain-Text-Part (Markdown-Source)
	BodyHTML string            // HTML-Part (gerendert)
	Headers  map[string]string // zusätzliche RFC-822-Header (z. B. Auto-Submitted, X-Loop, In-Reply-To)
}

// SendMail verbindet sich gegen cfg.Host:Port, authentifiziert (PLAIN) und
// versendet msg. STARTTLS wird genutzt wenn cfg.STARTTLS=true und der Server
// es anbietet.
//
// Kein Connection-Pool, kein async-Buffer — ein Send = eine SMTP-Session.
// Für die erwartete Last (~10 Mails/min/User, single-process) ausreichend.
//
// Ohne Context — Aufrufer, die einen haben (HTTP-Handler!), nehmen
// SendMailContext. Siehe dort, warum das zählt.
func SendMail(cfg Config, msg Message) error {
	return SendMailContext(context.Background(), cfg, msg)
}

// SendMailContext ist SendMail mit Abbruch-Kanal (F-718).
//
// ZWEI Absicherungen, die vorher fehlten:
//
//  1. `cfg.Timeout` liegt jetzt als Deadline auf JEDER Lese-/Schreib-Operation
//     der offenen Verbindung, nicht mehr nur auf dem Verbindungsaufbau.
//     `net.DialTimeout` deckte allein den TCP-Connect ab; Greeting, EHLO,
//     STARTTLS, AUTH, MAIL, RCPT, DATA und der Body-Write liefen danach ohne
//     jede Frist. Ein SMTP-Server, der nach dem Connect verstummt, hielt den
//     Aufrufer damit unbegrenzt fest — im Notification-Worker (dort begrenzt
//     seit F-380 wenigstens die Lane-Trennung den Schaden auf einen
//     Empfänger) und, deutlich schlimmer, im HTTP-Handler von
//     `api/email.go`, wo ein Request einfach nie zurückkam.
//
//  2. `ctx` beendet den Sendeversuch, wenn der Aufrufer weg ist (Client hat
//     den Request abgebrochen, Server fährt herunter). Umgesetzt durch
//     Schliessen der Verbindung — `net/smtp` kennt keinen Context, jede
//     laufende Operation bricht dann mit einem Verbindungsfehler ab.
//
// Die Deadline ist bewusst eine IDLE-Frist pro I/O-Operation, keine
// Gesamtlaufzeit-Grenze: Ein grosser Anhang darf länger als `cfg.Timeout`
// brauchen, solange die Gegenstelle Fortschritt macht.
// normalisiereTimeout ersetzt einen unbrauchbaren Timeout durch den Default.
//
// F-832 (a), Server-Kritiker Sprint 61: Hier stand `== 0`. Ein NEGATIVER Wert
// kam damit unverändert durch — gemessen mit `Timeout: -1s`: der `net.Dialer`
// rechnet `now.Add(-1s)`, jeder Dial scheitert sofort mit „i/o timeout", und
// `newTimeoutConn` wird zur No-Op. Beide F-718-Absicherungen waren still aus.
// Produktiv heute unerreichbar (`config_loader.go` setzt hart 15 s), aber
// `Config` ist exportiert: jeder künftige Aufrufer kann so einen Wert
// hereinreichen. Als eigene Funktion, damit die Regel testbar ist statt nur
// als Bedingung in zwei Funktionsrümpfen zu stehen.
func normalisiereTimeout(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}

func SendMailContext(ctx context.Context, cfg Config, msg Message) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	cfg.Timeout = normalisiereTimeout(cfg.Timeout, 15*time.Second)

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	// Dial mit Timeout — net/smtp.Dial hat selbst keinen.
	d := net.Dialer{Timeout: cfg.Timeout}
	rawConn, err := ausgang.Waehle(ctx, &d, "tcp", addr) // Karte #569: Ausgangs-Schalter
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", addr, err)
	}
	// F-NEU (Sprint-61-Kritiker): Die Verbindung MUSS unabhängig von `c.Quit()`
	// geschlossen werden. `net/smtp.Quit` ruft `c.Text.Close()` erst NACH
	// `c.cmd(221, "QUIT")` — schlägt das QUIT fehl (und genau das tut es nach
	// einem Idle-Timeout, weil die Verbindung schon nicht mehr antwortet),
	// kehrt Quit vorher zurück und das Socket bleibt offen. Der Watchdog
	// unten deckt nur das Ende von `ctx` ab, nicht den Deadline-Fall.
	// Gemessen: nach `mail: ehlo: … i/o timeout` sah die Gegenstelle zwei
	// Sekunden lang kein EOF; der Notification-Worker probiert es viermal je
	// Empfänger (RetryDelays), also vier offene Sockets pro hängendem
	// Empfänger. Doppel-Close ist bei `*net.TCPConn` gefahrlos (nachgemessen:
	// der zweite Aufruf liefert nur „use of closed network connection").
	defer func() { _ = rawConn.Close() }()
	conn := newTimeoutConn(rawConn, cfg.Timeout)
	stopWatchdog := watchContext(ctx, rawConn)
	defer stopWatchdog()
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail: client init: %w", err)
	}
	defer c.Quit()

	if err := c.Hello(localHelo()); err != nil {
		return fmt.Errorf("mail: ehlo: %w", err)
	}

	if cfg.STARTTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("mail: starttls: %w", err)
			}
		}
	}

	if cfg.User != "" && cfg.Pass != "" {
		auth := smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("%w: %v", ErrAuthFailed, err)
		}
	}

	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("mail: from: %w", err)
	}
	for _, r := range append(append([]string{}, msg.To...), msg.CC...) {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("mail: rcpt %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	body, err := buildMessageBytes(cfg, msg)
	if err != nil {
		_ = w.Close()
		return err
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		return fmt.Errorf("mail: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: close-data: %w", err)
	}
	return nil
}

// timeoutConn legt vor jedem Read/Write eine frische Deadline auf die
// Verbindung (F-718). Damit gilt `cfg.Timeout` als Idle-Frist für JEDE
// einzelne SMTP-Operation statt nur für den Verbindungsaufbau.
//
// Wichtig für STARTTLS: `smtp.Client.StartTLS` legt `tls.Client` um genau
// diese Verbindung — die TLS-Schicht liest und schreibt also weiterhin durch
// diesen Wrapper, die Deadline überlebt den Upgrade.
type timeoutConn struct {
	net.Conn
	timeout time.Duration
}

func newTimeoutConn(c net.Conn, timeout time.Duration) net.Conn {
	if timeout <= 0 {
		return c
	}
	return &timeoutConn{Conn: c, timeout: timeout}
}

func (c *timeoutConn) Read(b []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func (c *timeoutConn) Write(b []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

// watchContext schliesst conn, sobald ctx endet — der einzige Weg, eine
// laufende `net/smtp`-Operation abzubrechen (das Paket kennt keinen Context).
// Der Rückgabewert beendet den Wächter; er MUSS gerufen werden (defer), sonst
// bleibt die Goroutine bis zum Ende eines langlebigen ctx stehen.
func watchContext(ctx context.Context, conn net.Conn) func() {
	if ctx == nil || ctx.Done() == nil {
		return func() {}
	}
	fertig := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-fertig:
		}
	}()
	return func() { close(fertig) }
}

// localHelo liefert einen sicheren EHLO-Namen. Fully-qualified ist ideal,
// hostname-Fallback OK.
func localHelo() string {
	return "aboard.local"
}

// randomToken erzeugt einen 16-Byte-zufälligen hex-String — für Boundary +
// Message-ID. Crypto/rand schlägt nur bei extremen System-Fehlern fehl;
// dann fallback auf Time-basiert (Worst-Case-Lockup-Schutz).
func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// buildMessageBytes konstruiert eine RFC-822-Multipart/alternative-Mail.
// Subject + From bekommen RFC-2047-Encoded-Words für Sonderzeichen.
func buildMessageBytes(cfg Config, msg Message) ([]byte, error) {
	var sb strings.Builder
	headers := textproto.MIMEHeader{}
	from := cfg.From
	if cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", cfg.FromName), cfg.From)
	}
	headers.Set("From", from)
	headers.Set("To", strings.Join(msg.To, ", "))
	if len(msg.CC) > 0 {
		headers.Set("Cc", strings.Join(msg.CC, ", "))
	}
	headers.Set("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	headers.Set("MIME-Version", "1.0")
	// Sprint-40-Kritiker-Fix [K]P2: RFC-5322 Date + Message-ID Pflicht-Header.
	// Ohne Date markieren Gmail/M365 die Mail typischerweise als Spam.
	now := time.Now()
	headers.Set("Date", now.Format(time.RFC1123Z))
	headers.Set("Message-ID", fmt.Sprintf("<%s@aboard.local>", randomToken()))
	// Sprint-40-Kritiker-Fix [K]P2: Boundary aus crypto/rand statt predictable
	// UnixNano — verhindert adversarial Karten-Body, der die Boundary trifft.
	boundary := "aboard-" + randomToken()
	headers.Set("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	for k, vs := range headers {
		for _, v := range vs {
			sb.WriteString(k)
			sb.WriteString(": ")
			sb.WriteString(v)
			sb.WriteString("\r\n")
		}
	}
	// Zusätzliche Header in stabiler (sortierter) Reihenfolge — reproduzierbarer
	// Output hilft Tests, Logs und Debugging.
	if len(msg.Headers) > 0 {
		keys := make([]string, 0, len(msg.Headers))
		for k := range msg.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteString(k)
			sb.WriteString(": ")
			sb.WriteString(msg.Headers[k])
			sb.WriteString("\r\n")
		}
	}
	sb.WriteString("\r\n")

	// Plain-Part
	sb.WriteString("--")
	sb.WriteString(boundary)
	sb.WriteString("\r\nContent-Type: text/plain; charset=\"utf-8\"\r\n")
	sb.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	sb.WriteString(msg.BodyText)
	sb.WriteString("\r\n")

	// HTML-Part
	sb.WriteString("--")
	sb.WriteString(boundary)
	sb.WriteString("\r\nContent-Type: text/html; charset=\"utf-8\"\r\n")
	sb.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	sb.WriteString(msg.BodyHTML)
	sb.WriteString("\r\n")

	sb.WriteString("--")
	sb.WriteString(boundary)
	sb.WriteString("--\r\n")
	return []byte(sb.String()), nil
}

// TestConnection probiert Connect + EHLO + STARTTLS + AUTH gegen den
// konfigurierten Server, OHNE Mail zu versenden. Anti-Spam-Pflicht: häufige
// Test-Klicks dürfen keine Testmails verschicken.
//
// F-718: Dieselbe Deadline-Absicherung wie in SendMailContext. Der Aufrufer
// ist der Admin-Test-Hook und läuft im HTTP-Handler — ein Server, der nach
// dem Connect verstummt, hätte den Request sonst unbegrenzt festgehalten,
// und zwar ausgerechnet in der Funktion, deren Zweck es ist, genau so eine
// Störung zu diagnostizieren.
func TestConnection(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	cfg.Timeout = normalisiereTimeout(cfg.Timeout, 10*time.Second)
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	td := net.Dialer{Timeout: cfg.Timeout}
	rawConn, err := ausgang.Waehle(context.Background(), &td, "tcp", addr) // Karte #569
	if err != nil {
		return fmt.Errorf("mail-test: dial: %w", err)
	}
	// Gleiche Konstruktion wie in SendMailContext, gleiches Leck: `c.Quit()`
	// allein schliesst nach einem Idle-Timeout nicht (siehe dort).
	defer func() { _ = rawConn.Close() }()
	conn := newTimeoutConn(rawConn, cfg.Timeout)
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mail-test: client: %w", err)
	}
	defer c.Quit()
	if err := c.Hello(localHelo()); err != nil {
		return fmt.Errorf("mail-test: ehlo: %w", err)
	}
	if cfg.STARTTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("mail-test: starttls: %w", err)
			}
		}
	}
	if cfg.User != "" && cfg.Pass != "" {
		auth := smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("%w: %v", ErrAuthFailed, err)
		}
	}
	return nil
}
