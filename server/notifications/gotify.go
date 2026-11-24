package notifications

// gotify.go — Gotify-HTTP-POST-Sender.
//
// Pro Send wird das Token + URL aus dem (verschlüsselten) ConfigStore
// gelesen — keine Caches. Gotify-API:
//
//   POST <url>/message?token=<app-token>
//   Body: { "title": "...", "message": "...", "priority": 5 }

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Andiministrator/aBoard/server/ausgang"
	"github.com/Andiministrator/aBoard/server/schleuse"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"

	"github.com/Andiministrator/aBoard/server/storage"
)

// GotifyConfigSource liefert die verschlüsselte Config (URL+Token) eines
// Users. Injizierbar für Tests.
type GotifyConfigSource interface {
	GetGotify(ctx context.Context, userID string) (*storage.GotifyConfig, error)
}

// HTTPClient ist das schmale Interface, das der Sender nutzt (typischerweise
// *http.Client). Test-Stub kann ihn ersetzen.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// GotifySender implementiert Sender für den Gotify-Channel.
type GotifySender struct {
	cfgSrc GotifyConfigSource
	client HTTPClient      // nil = pro Ziel ein Versand-Client (clientFuer)
	basis  *http.Transport // nur Tests: Basis-Transport (Proxy) für clientFuer; nil = Standard
}

// NewGotifySender legt einen Sender an. client nil = pro Versand ein Client
// über schleuse.VersandTransport (F-2668); ein übergebener Client (Tests)
// wird unverändert benutzt.
func NewGotifySender(cfgSrc GotifyConfigSource, client HTTPClient) *GotifySender {
	return &GotifySender{cfgSrc: cfgSrc, client: client}
}

// ErrGotifyNichtErreichbar ist alles, was der Nutzer über einen
// gescheiterten Verbindungsaufbau erfährt (F-2668): Der Fehlertext der
// Gegenstelle würde ein internes Netz abtastbar machen.
var ErrGotifyNichtErreichbar = errors.New("nicht erreichbar")

// gotifyFehler trägt einen festen, nutzertauglichen Text; die Ursache bleibt
// für errors.Is erreichbar, steht aber nie in Error().
type gotifyFehler struct {
	text string
	err  error
}

func (e *gotifyFehler) Error() string { return e.text }
func (e *gotifyFehler) Unwrap() error { return e.err }

// clientFuer liefert den Client für einen Versand an ziel. Ohne injizierten
// Client: Timeout, Ausgangs-Schalter (Karte #569) und VersandTransport — der
// prüft die aufgelöste Adresse (kein internes Ziel), folgt keinem Redirect
// und erlaubt bei Proxy genau dessen Adresse (F-2668, wie
// llm/schleuse_versand.go).
func (g *GotifySender) clientFuer(ziel string) (HTTPClient, error) {
	if g.client != nil {
		return g.client, nil
	}
	var opts []schleuse.VersandOption
	if zu, perr := url.Parse(ziel); perr == nil && ausgang.Aktiv().GotifyInternErlaubt(zu.Hostname(), urlPort(zu)) {
		// ADR-0147: Der Admin hat genau diesen Host wörtlich freigegeben.
		opts = append(opts, schleuse.ErlaubeInternenHost(zu.Hostname(), urlPort(zu)))
	}
	rt, err := schleuse.VersandTransport(ziel, g.basis, SendTimeout, opts...)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout:       SendTimeout,
		Transport:     ausgang.Transport(rt),
		CheckRedirect: schleuse.OhneUmleitung,
	}, nil
}

// senden schickt den POST. Fehler tragen nur „nicht erreichbar“ bzw. den
// Statuscode; Verbindungsfehler und Umleitungen gehen ins Server-Log, der
// Body der Gegenstelle wird nie gelesen.
func (g *GotifySender) senden(req *http.Request, ziel string) error {
	c, err := g.clientFuer(ziel)
	if err != nil {
		log.Printf("gotify: client: %v", err)
		return &gotifyFehler{text: ErrGotifyNichtErreichbar.Error(), err: ErrGotifyNichtErreichbar}
	}
	resp, err := c.Do(req)
	if err != nil {
		// G-4 (Sitzungs-Kritik 29.10.): `%v` auf den *url.Error schriebe die
		// ganze URL samt Query ins Log (ein `?token=…` stuende im Klartext).
		// Geloggt werden nur Methode, Host und die innere Ursache.
		var ue *url.Error
		if errors.As(err, &ue) {
			log.Printf("gotify: %s %s: %v", ue.Op, req.URL.Host, ue.Err)
		} else {
			log.Printf("gotify: %s: %v", req.URL.Host, err)
		}
		if errors.Is(err, ausgang.ErrGesperrt) {
			return &gotifyFehler{text: "ausgehende Verbindung abgeschaltet", err: ausgang.ErrGesperrt}
		}
		return &gotifyFehler{text: ErrGotifyNichtErreichbar.Error(), err: ErrGotifyNichtErreichbar}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// Channel implementiert Sender.
func (g *GotifySender) Channel() string { return ChannelGotify }

// SendOne implementiert Sender. Lädt Config des Empfängers, baut POST,
// schickt.
func (g *GotifySender) SendOne(ctx context.Context, recipientUserID, recipientEmail, recipientName string, ev Event) error {
	cfg, err := g.cfgSrc.GetGotify(ctx, recipientUserID)
	if err != nil {
		return fmt.Errorf("gotify: load config: %w", err)
	}
	if cfg == nil {
		return errors.New("gotify: no config")
	}
	if err := ValidateGotifyURL(cfg.URL); err != nil {
		return fmt.Errorf("gotify: invalid url: %w", err)
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return errors.New("gotify: empty token")
	}
	title, message := buildGotifyPayload(ev)
	priority := GotifyPriority(ev.Type)
	body, err := json.Marshal(map[string]interface{}{
		"title":    title,
		"message":  message,
		"priority": priority,
	})
	if err != nil {
		return fmt.Errorf("gotify: marshal: %w", err)
	}
	// Token via X-Gotify-Key-Header, NICHT via URL-Query (sonst leakt das
	// Geheimnis in Webserver-Access-Logs + in fehlerwrap-Logs).
	// Gotify unterstützt beides; Header ist die sichere Variante.
	postURL := strings.TrimRight(cfg.URL, "/") + "/message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("gotify: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gotify-Key", cfg.Token)
	req.Header.Set("User-Agent", "aboard-notification/1")
	if err := g.senden(req, cfg.URL); err != nil {
		return fmt.Errorf("gotify: %w", err)
	}
	return nil
}

// buildGotifyPayload — kompakte Title + Message-Strings für Gotify.
func buildGotifyPayload(ev Event) (title, message string) {
	label := HumanLabel(ev.Type)
	snippet := truncate(strings.TrimSpace(ev.Snippet), GotifySnippetMax)
	switch ev.Type {
	case EventBackupFailed:
		title = "Backup fehlgeschlagen"
		message = ev.BoardName
		return title, strings.TrimSpace(message + " — " + snippet)
	case EventBoardAwaiting:
		title = "Antwort gefragt"
		return title, fmt.Sprintf("%s — %s", ev.BoardName, snippet)
	case EventSubsystemOff:
		// Eigener Fall (Sprint-59-Kritiker [K] P2): Der Vorgabezweig baut
		// die Nachricht aus `ev.CardTitle` — das ist hier leer, die Meldung
		// begann deshalb mit einem führenden „— ". Und 80 Zeichen sind für
		// den Grund zu wenig; auf Gotify zählt die Handlungsaufforderung,
		// die Einzelheiten stehen in der Mail und im Log.
		title = "Subsystem AUS"
		return title, truncate(strings.TrimSpace(ev.Snippet), GotifySnippetMax)
	default:
		if ev.CardDisplayID > 0 {
			title = fmt.Sprintf("%s #%d", label, ev.CardDisplayID)
		} else {
			title = label
		}
		message = ev.CardTitle
		if snippet != "" {
			message = strings.TrimSpace(message + " — " + snippet)
		}
		return title, message
	}
}

// ErrGotifyHostUngueltig: Der Host enthält Nicht-ASCII-Zeichen (Vollbreiten-
// ziffern, ideographischer Punkt): Resolver und Proxy könnten sie als
// Adresse deuten, die Prüfung unten nicht.
var ErrGotifyHostUngueltig = errors.New("url host must be plain ASCII")

// urlPort liefert den effektiven Port einer https-URL (ohne Angabe 443).
func urlPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	return "443"
}

// ErrGotifyInternesZiel: Die URL zeigt auf ein IP-Literal im internen Bereich
// oder auf localhost, und der Host steht nicht auf ABOARD_GOTIFY_INTERN_ERLAUBT.
var ErrGotifyInternesZiel = errors.New("url must not point to an internal address (only the admin can allow it)")

// zahlenHost erkennt Schreibweisen, die ein Resolver als IPv4-Adresse deutet,
// obwohl netip sie nicht liest (2130706433, 0x7f.1, 017700000001).
var zahlenHost = regexp.MustCompile(`^(0x[0-9a-f]*|[0-9]+)(\.(0x[0-9a-f]*|[0-9]+))*$`)

// ValidateGotifyURL prüft, dass die URL mit https:// beginnt und einen
// Host hat (Karte #305 §Gotify-URL-Validierung). Dazu (F-2689 G-3,
// ADR-0147): IP-Literale in internen Bereichen, localhost und Zahlenhosts
// werden abgelehnt, außer der Host steht wörtlich auf
// ABOARD_GOTIFY_INTERN_ERLAUBT. Der Text nennt weder Adresse noch Netz.
func ValidateGotifyURL(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("url empty")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("url must be https://")
	}
	if u.Host == "" {
		return errors.New("url must have a host")
	}
	host := ausgang.NormHost(u.Hostname())
	if host == "" {
		return errors.New("url must have a host")
	}
	for _, r := range host {
		if r > 127 {
			return ErrGotifyHostUngueltig
		}
	}
	if ausgang.Aktiv().GotifyInternErlaubt(host, urlPort(u)) {
		return nil
	}
	if ausgang.Aktiv().GotifyInternHost(host) {
		// Host ist freigegeben, aber nicht mit diesem Port (A-1).
		return ErrGotifyInternesZiel
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return ErrGotifyInternesZiel
	}
	if adr, perr := netip.ParseAddr(host); perr == nil {
		if schleuse.InternesZiel(adr.WithZone("")) {
			return ErrGotifyInternesZiel
		}
	} else if zahlenHost.MatchString(host) {
		return ErrGotifyInternesZiel
	}
	return nil
}

// TestConnection probiert einen synthetischen Send-Call (Title "aBoard
// Test"), ohne ein Event zu loggen. Wird vom Test-Endpoint aufgerufen.
func (g *GotifySender) TestConnection(ctx context.Context, cfg storage.GotifyConfig) error {
	if err := ValidateGotifyURL(cfg.URL); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]interface{}{
		"title":    "aBoard-Test",
		"message":  "Verbindungstest erfolgreich.",
		"priority": 3,
	})
	postURL := strings.TrimRight(cfg.URL, "/") + "/message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gotify-Key", cfg.Token)
	return g.senden(req, cfg.URL)
}
