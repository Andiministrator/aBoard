package schleuse

// versand.go — der HTTP-Client, über den die Schleuse an einen externen
// Anbieter sendet (ADR-0134 §1, Kritiker KC-1; Karte #686).
//
// PruefeBasis lehnt interne Ziele beim EINTRAGEN ab. Das reicht nicht: Ein
// öffentlicher Name kann zur Laufzeit auf eine interne Adresse auflösen
// (DNS-Rebinding), und ein Anbieter kann per Redirect dorthin weiterleiten.
// Der Versand-Client schützt deshalb an zwei Stellen:
//
//   - Verbindung: Jede Verbindung prüft die AUFGELÖSTE Adresse, bevor sie
//     zustande kommt (net.Dialer.Control — läuft nach der Namensauflösung für
//     jede einzelne Adresse, die Go probiert). Eine interne Adresse
//     (InternesZiel, darin NAT64, 6to4, Teredo ganz) bricht ab.
//   - Redirects: Der Client folgt keinem. Jede 3xx-Antwort ist ein Fehler
//     des Auftrags (ErrUmleitung).
//
// Läuft der Ausgang über einen Proxy (HTTPS_PROXY), wählt der Server nur den
// Proxy an — der steht typischerweise im eigenen Netz und ist das Tor, das
// das Partnerprojekt mit seiner Ausgangsliste führt. Dann ist genau diese eine Adresse
// (wie im Proxy-URL geschrieben) ohne Bereichsprüfung erlaubt, jede andere
// direkte Verbindung abgelehnt. Die Namensauflösung des Ziels macht dann der
// Proxy; gegen Rebinding schützt dort seine Liste.
//
// Ausnahme (ADR-0147, F-2689): Mit der Option ErlaubeInternenHost verbindet der
// Dialer zu genau diesem einen, vom Admin wörtlich freigegebenen Host auch
// dann, wenn er intern liegt. Jede andere Verbindung bleibt geprüft, Umleitungen
// bleiben ein Fehler.
//
// Keep-Alive ist aus: Jeder Versand löst neu auf und prüft neu.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"syscall"
	"time"

	"github.com/Andiministrator/aBoard/server/ausgang"
)

var (
	// ErrInterneAdresse: Der Name des Anbieters löst auf eine interne Adresse
	// auf (oder eine direkte Verbindung trotz Proxy).
	ErrInterneAdresse = errors.New("schleuse: ziel löst auf eine interne adresse auf")
	// ErrUmleitung: Der Anbieter hat mit einer Umleitung (3xx) geantwortet.
	ErrUmleitung = errors.New("schleuse: anbieter antwortet mit umleitung")
)

// VerbindungsFrist begrenzt den Verbindungsaufbau (TCP), nicht die Antwort.
const VerbindungsFrist = 30 * time.Second

// pruefeVerbindung: Control-Funktion des Dialers — address ist "ip:port"
// nach der Auflösung.
func pruefeVerbindung(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInterneAdresse, err)
	}
	adr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: keine adresse", ErrInterneAdresse)
	}
	if InternesZiel(adr.WithZone("")) {
		return ErrInterneAdresse
	}
	return nil
}

// proxyAdresse liefert host:port des Proxys, den transport für ziel nimmt,
// oder "".
func proxyAdresse(proxy func(*http.Request) (*url.URL, error), ziel *url.URL) (string, error) {
	if proxy == nil {
		return "", nil
	}
	pu, err := proxy(&http.Request{URL: ziel, Header: http.Header{}})
	if err != nil || pu == nil {
		return "", err
	}
	port := pu.Port()
	if port == "" {
		switch pu.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return net.JoinHostPort(pu.Hostname(), port), nil
}

// VersandRT ist der Transport eines Versands: macht aus jeder 3xx-Antwort
// einen Fehler und merkt sich, ob ein Schutz ausgelöst hat — die
// Provider-Schicht wickelt Netzfehler in eigene Fehlerarten ein, der Grund
// ginge sonst verloren.
type VersandRT struct {
	basis    http.RoundTripper
	mu       sync.Mutex
	verstoss error
}

func (u *VersandRT) merke(err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.verstoss == nil {
		u.verstoss = err
	}
}

// Verstoss liefert ErrInterneAdresse oder ErrUmleitung, wenn einer der
// Schutzmechanismen ausgelöst hat, sonst nil.
func (u *VersandRT) Verstoss() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.verstoss
}

func (u *VersandRT) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := u.basis.RoundTrip(req)
	if err != nil {
		if errors.Is(err, ErrInterneAdresse) {
			u.merke(ErrInterneAdresse)
		}
		return nil, err
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		res.Body.Close()
		u.merke(ErrUmleitung)
		return nil, fmt.Errorf("%w (status %d)", ErrUmleitung, res.StatusCode)
	}
	return res, nil
}

// VersandOption ändert den Transport eines Versands.
type VersandOption func(*versandEinstellung)

type versandEinstellung struct{ internerHost, internerPort string }

// ErlaubeInternenHost nimmt host:port (wörtlich so wie im Ziel, port "" = 443)
// von der Bereichsprüfung des Dialers aus (ADR-0147). Host UND Port müssen
// stimmen. Leerer host = keine Ausnahme.
func ErlaubeInternenHost(host, port string) VersandOption {
	return func(e *versandEinstellung) {
		if port == "" {
			port = "443"
		}
		e.internerHost, e.internerPort = ausgang.NormHost(host), port
	}
}

// VersandTransport baut den Transport für einen Versand an ziel (die
// Basis-URL des Anbieters). basis wird geklont (Proxy aus der Umgebung,
// TLS-Einstellungen); nil = http.DefaultTransport. kopfFrist ist die
// Wartezeit bis zu den Antwort-Headern. Den Ausgangs-Schalter (ausgang.
// Transport) legt der Aufrufer darum.
func VersandTransport(ziel string, basis *http.Transport, kopfFrist time.Duration, opts ...VersandOption) (*VersandRT, error) {
	var einst versandEinstellung
	for _, o := range opts {
		o(&einst)
	}
	zu, err := url.Parse(ziel)
	if err != nil || zu.Hostname() == "" {
		return nil, fmt.Errorf("schleuse: ziel nicht lesbar")
	}
	if basis == nil {
		dt, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("schleuse: kein http.Transport")
		}
		basis = dt
	}
	tr := basis.Clone()
	proxy, err := proxyAdresse(tr.Proxy, zu)
	if err != nil {
		return nil, fmt.Errorf("schleuse: proxy: %w", err)
	}
	geprueft := &net.Dialer{Timeout: VerbindungsFrist, Control: pruefeVerbindung}
	direkt := &net.Dialer{Timeout: VerbindungsFrist}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if proxy != "" {
			if addr != proxy {
				return nil, fmt.Errorf("%w: direkte verbindung trotz proxy", ErrInterneAdresse)
			}
			return direkt.DialContext(ctx, network, addr)
		}
		if einst.internerHost != "" {
			// Der Name wird vor der Auflösung verglichen: addr ist host:port
			// des Ziels, so wie es in der URL steht (ADR-0147, kein DNS-Vertrauen).
			if h, pt, err := net.SplitHostPort(addr); err == nil && ausgang.NormHost(h) == einst.internerHost && pt == einst.internerPort {
				return direkt.DialContext(ctx, network, addr)
			}
		}
		return geprueft.DialContext(ctx, network, addr)
	}
	tr.DialTLSContext = nil
	tr.DisableKeepAlives = true
	tr.ResponseHeaderTimeout = kopfFrist
	return &VersandRT{basis: tr}, nil
}

// OhneUmleitung ist die CheckRedirect-Funktion des Versand-Clients: Er folgt
// keinem Redirect (der Transport meldet ihn ohnehin als Fehler).
func OhneUmleitung(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
