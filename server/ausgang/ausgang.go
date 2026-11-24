// Package ausgang ist der Schalter für alle ausgehenden Verbindungen des
// Servers (ADR-0129 §5 „Ausgänge“, Karte #569).
//
// Umgebung:
//
//	ABOARD_AUSGAENGE          an (Voreinstellung) | aus
//	ABOARD_AUSGAENGE_ERLAUBT  Komma-Liste von Zielen, die auch bei „aus“ erreichbar
//	                          bleiben: `host` oder `host:port`, z. B.
//	                          `llm.example:8000,push.example`
//	ABOARD_GOTIFY_INTERN_ERLAUBT  Komma-Liste von `host` oder `host:port` (ohne Port = 443), zu denen der
//	                          Gotify-Versand auch dann verbindet, wenn sie intern liegen
//	                          (ADR-0147, F-2689). Unabhängig vom Schalter oben.
//
// Bei „aus“ baut der Server keine Verbindung auf, deren Ziel nicht auf der
// Liste steht: LLM-Anbieter, Gotify (auch der Testversand), SMTP (Versand und
// Admin-Test), IMAP (Mail-Ingest). Die Prüfung liegt VOR dem Verbindungsaufbau
// und liefert ErrGesperrt mit dem Ziel im Text, statt in einen Timeout zu
// laufen.
//
// Bei HTTP wird das Ziel der Anfrage geprüft, nicht der Rechner, zu dem
// gewählt wird. Läuft der Ausgang über einen Proxy (HTTPS_PROXY), steht auf
// der Liste also das eigentliche Ziel (`api.openai.com`), nicht der Proxy.
//
// Warum eine Liste statt „lokale Adressen sind erlaubt“: Ob ein Ziel lokal ist,
// entscheidet DNS, und ein Ausgang, der an einer Namensauflösung hängt, ist
// genau die Art Lücke, die dieser Schalter schliessen soll. Die Liste nennt
// jedes Ziel beim Namen; was nicht draufsteht, geht nicht hinaus.
package ausgang

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

// ErrGesperrt meldet eine Verbindung, die der Schalter verhindert hat.
var ErrGesperrt = errors.New("ausgehende Verbindung abgeschaltet")

// Politik ist der Zustand des Schalters. Der Nullwert erlaubt alles.
type Politik struct {
	aus     bool
	erlaubt map[string]bool // "host" oder "host:port", klein geschrieben
	// gotifyIntern: Hostnamen, die der Gotify-Versand auch intern erreichen
	// darf (ADR-0147). Wörtlicher Vergleich, kein DNS.
	gotifyIntern map[string]bool
	// gotifyInternHosts: dieselben Einträge nur nach Host (jeder Port), damit
	// die Eintragsprüfung einen freigegebenen Host mit fremdem Port erkennt.
	gotifyInternHosts map[string]bool
}

// GotifyInternEnvVar nennt die Admin-Liste interner Gotify-Hosts (ADR-0147).
const GotifyInternEnvVar = "ABOARD_GOTIFY_INTERN_ERLAUBT"

// NormHost bringt einen Host in die Vergleichsform der Intern-Liste: klein,
// ohne Klammern (IPv6) und ohne Punkt am Ende (`gotify.intern.` ist derselbe
// Name wie `gotify.intern`).
func NormHost(h string) string {
	return strings.TrimRight(strings.ToLower(strings.Trim(strings.TrimSpace(h), "[]")), ".")
}

// ParseInternEintrag zerlegt einen Eintrag der Intern-Liste in Host und Port.
// Erlaubt sind `host`, `host:port`, `[v6]`, `[v6]:port` und ein nacktes
// IPv6-Literal (`::1`). Ohne Port gilt 443 (Gotify läuft über https).
// Host in der Vergleichsform von NormHost. Nicht-ASCII, URLs, Platzhalter
// und ein ungültiger Port sind Fehler (ADR-0147).
func ParseInternEintrag(e string) (host, port string, err error) {
	e = strings.TrimSpace(e)
	fehler := func(grund string) (string, string, error) {
		return "", "", fmt.Errorf("%s: %q %s", GotifyInternEnvVar, e, grund)
	}
	if e == "" {
		return fehler("ist leer")
	}
	for _, r := range e {
		if r > 127 {
			return fehler("enthält Nicht-ASCII-Zeichen")
		}
	}
	if strings.ContainsAny(e, "/ *@?#\\") {
		return fehler("ist kein host oder host:port (keine URL, keine Platzhalter)")
	}
	port = "443"
	switch {
	case strings.HasPrefix(e, "["):
		if strings.HasSuffix(e, "]") {
			host = e
		} else if h, pt, serr := net.SplitHostPort(e); serr == nil {
			host, port = h, pt
		} else {
			return fehler("ist kein gültiges [v6]:port")
		}
	case strings.Count(e, ":") == 0:
		host = e
	case strings.Count(e, ":") == 1:
		h, pt, serr := net.SplitHostPort(e)
		if serr != nil {
			return fehler("ist kein gültiges host:port")
		}
		host, port = h, pt
	default: // nacktes IPv6-Literal
		if _, perr := netip.ParseAddr(e); perr != nil {
			return fehler("ist kein gültiges IPv6-Literal (mit Port: [v6]:port)")
		}
		host = e
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
		return fehler("hat einen ungültigen Port")
	}
	host = NormHost(host)
	if host == "" {
		return fehler("hat keinen Host")
	}
	return host, port, nil
}

func internSchluessel(host, port string) string {
	return net.JoinHostPort(NormHost(host), port)
}

// MitGotifyIntern setzt die Einträge (host oder host:port, Syntax von
// ParseInternEintrag), die der Gotify-Versand auch intern erreichen darf, und
// liefert p zurück. Ungültige Einträge werden übersprungen; AusUmgebung
// lehnt sie vorher mit Fehler ab.
func (p *Politik) MitGotifyIntern(eintraege ...string) *Politik {
	p.gotifyIntern = map[string]bool{}
	p.gotifyInternHosts = map[string]bool{}
	for _, e := range eintraege {
		if h, pt, err := ParseInternEintrag(e); err == nil {
			p.gotifyIntern[internSchluessel(h, pt)] = true
			p.gotifyInternHosts[h] = true
		}
	}
	return p
}

// GotifyInternErlaubt meldet, ob host UND port wörtlich auf der Liste
// ABOARD_GOTIFY_INTERN_ERLAUBT stehen (port "" gilt als 443).
func (p *Politik) GotifyInternErlaubt(host, port string) bool {
	if p == nil {
		return false
	}
	if port == "" {
		port = "443"
	}
	return p.gotifyIntern[internSchluessel(host, port)]
}

var aktiv atomic.Pointer[Politik]

func init() { aktiv.Store(&Politik{}) }

// Setze macht p zur geltenden Politik und liefert die vorherige zurück (für
// Tests: `defer ausgang.Setze(ausgang.Setze(p))`).
func Setze(p *Politik) *Politik {
	if p == nil {
		p = &Politik{}
	}
	return aktiv.Swap(p)
}

// Aktiv liefert die geltende Politik.
func Aktiv() *Politik { return aktiv.Load() }

// Neu baut eine Politik. `aus=false` erlaubt alles, die Liste ist dann egal.
func Neu(aus bool, erlaubt ...string) *Politik {
	p := &Politik{aus: aus, erlaubt: map[string]bool{}}
	for _, e := range erlaubt {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			p.erlaubt[e] = true
		}
	}
	return p
}

// AusUmgebung liest ABOARD_AUSGAENGE und ABOARD_AUSGAENGE_ERLAUBT. Ein
// unbekannter Wert ist ein Fehler: Der Server startet dann nicht, statt im
// Zweifel Verbindungen zuzulassen.
func AusUmgebung() (*Politik, error) {
	wert := strings.ToLower(strings.TrimSpace(os.Getenv("ABOARD_AUSGAENGE")))
	var aus bool
	switch wert {
	case "", "an":
	case "aus":
		aus = true
	default:
		return nil, fmt.Errorf("ABOARD_AUSGAENGE=%q: erlaubt sind \"an\" und \"aus\"", wert)
	}
	var liste []string
	for _, e := range strings.Split(os.Getenv("ABOARD_AUSGAENGE_ERLAUBT"), ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.ContainsAny(e, "/ *") {
			return nil, fmt.Errorf("ABOARD_AUSGAENGE_ERLAUBT: %q ist kein host oder host:port (keine URL, keine Platzhalter)", e)
		}
		liste = append(liste, e)
	}
	var intern []string
	for _, e := range strings.Split(os.Getenv(GotifyInternEnvVar), ",") {
		if strings.TrimSpace(e) == "" {
			continue
		}
		if _, _, err := ParseInternEintrag(e); err != nil {
			return nil, err
		}
		intern = append(intern, e)
	}
	return Neu(aus, liste...).MitGotifyIntern(intern...), nil
}

// Aus meldet, ob der Schalter auf „aus“ steht (nur die Liste ist erreichbar).
func (p *Politik) Aus() bool { return p != nil && p.aus }

// Beschreibung für das Start-Log.
func (p *Politik) Beschreibung() string {
	if !p.aus {
		return "Ausgänge: an (ABOARD_AUSGAENGE nicht \"aus\")" + p.gotifyBeschreibung()
	}
	if len(p.erlaubt) == 0 {
		return "Ausgänge: AUS — keine ausgehende Verbindung (LLM, Gotify, SMTP, IMAP)" + p.gotifyBeschreibung()
	}
	ziele := make([]string, 0, len(p.erlaubt))
	for z := range p.erlaubt {
		ziele = append(ziele, z)
	}
	sort.Strings(ziele)
	return "Ausgänge: AUS — erlaubt nur: " + strings.Join(ziele, ", ") + p.gotifyBeschreibung()
}

func (p *Politik) gotifyBeschreibung() string {
	if len(p.gotifyIntern) == 0 {
		return ""
	}
	hosts := make([]string, 0, len(p.gotifyIntern))
	for h := range p.gotifyIntern {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	return "; Gotify intern erlaubt: " + strings.Join(hosts, ", ")
}

// Pruefe entscheidet über ein Ziel `host:port` (oder nur `host`).
func (p *Politik) Pruefe(ziel string) error {
	if !p.aus {
		return nil
	}
	ziel = strings.ToLower(ziel)
	host := ziel
	if h, _, err := net.SplitHostPort(ziel); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if p.erlaubt[ziel] || p.erlaubt[host] {
		return nil
	}
	return fmt.Errorf("%w: %s (ABOARD_AUSGAENGE=aus; freigeben über ABOARD_AUSGAENGE_ERLAUBT)", ErrGesperrt, ziel)
}

// PruefeURL prüft das Ziel einer HTTP-Anfrage; ohne Port gilt der des Schemas.
func (p *Politik) PruefeURL(host, schema string) error {
	if !p.aus {
		return nil
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		port := "443"
		if schema == "http" {
			port = "80"
		}
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	return p.Pruefe(host)
}

// Transport hängt die Prüfung vor einen RoundTripper. Geprüft wird bei jeder
// Anfrage gegen die DANN geltende Politik.
func Transport(basis http.RoundTripper) http.RoundTripper {
	if basis == nil {
		basis = http.DefaultTransport
	}
	return rundreise{basis: basis}
}

type rundreise struct{ basis http.RoundTripper }

// Basis liefert den umhüllten Transport — für Wächter-Tests, die dessen
// Einstellungen prüfen (z. B. ResponseHeaderTimeout, F-1126).
func (r rundreise) Basis() http.RoundTripper { return r.basis }

// Basis packt einen Transport aus, den Transport() umhüllt hat; andere
// kommen unverändert zurück.
func Basis(rt http.RoundTripper) http.RoundTripper {
	if r, ok := rt.(rundreise); ok {
		return r.basis
	}
	return rt
}

func (r rundreise) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := Aktiv().PruefeURL(req.URL.Host, req.URL.Scheme); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	return r.basis.RoundTrip(req)
}

// Dialer ist der Teil von net.Dialer, den SMTP/IMAP brauchen.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Waehle prüft das Ziel und wählt erst dann. Für Wege ohne HTTP (SMTP, IMAP).
func Waehle(ctx context.Context, d Dialer, network, address string) (net.Conn, error) {
	if err := Aktiv().Pruefe(address); err != nil {
		return nil, err
	}
	return d.DialContext(ctx, network, address)
}

// GotifyInternHost meldet, ob host mit irgendeinem Port auf der Liste steht.
// Die Eintragsprüfung lehnt dann jeden anderen Port ab (ADR-0147).
func (p *Politik) GotifyInternHost(host string) bool {
	return p != nil && p.gotifyInternHosts[NormHost(host)]
}
