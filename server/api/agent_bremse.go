package api

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Andiministrator/aBoard/server/storage"
)

// Fehlversuch-Bremse am Agenten-Zugang (Karte #608, ADR-0130 §6 und
// Nachtrag W3). Bevor der Zugang öffentlich erreichbar wird (#610), begrenzt
// sie abgewiesene Versuche (401): nach MaxFehlversuche innerhalb von
// Fenster antwortet der Zugang für diesen Schlüssel mit 429, bis das Fenster
// abgelaufen ist — auch ein dann richtiges Token.
//
// Schlüssel (Betreiberwunsch): Hinter einer Adresse sitzen im Team-Betrieb mehrere
// Projekt-Agenten (rootless Podman, gemeinsame Quell-IP). Nur je Adresse zu
// zählen, sperrte mit einem falsch konfigurierten Projekt alle anderen. Trägt
// das Token eine BEKANNTE Kennung (abt_<kennung>_…), zählt die Bremse je
// (Adresse, Kennung); sonst je Adresse. Unbekannte Kennungen fallen auf die
// Adresse zurück — sonst umginge man die Bremse mit einer neuen erfundenen
// Kennung je Versuch.
//
// Client-Adresse (vorgeschalteter Proxy): nur `X-Real-IP`, NIE `X-Forwarded-For` (den kann
// der Client anhängen). Und `X-Real-IP` nur, wenn die TCP-Gegenstelle der
// konfigurierte Proxy ist — sonst die TCP-Adresse. Der Proxy steht in
// ABOARD_AGENT_PROXY als Liste aus Netzen (CIDR/IP), DNS-Namen und dem Wort
// `unix` (Unix-Socket-Gegenstelle). DNS-Namen werden regelmässig neu
// aufgelöst: Der Proxy-Container bekommt bei jedem Update eine neue Adresse;
// eine feste IP fiele danach still auf die TCP-Adresse zurück und drosselte
// alle Clients gemeinsam.

const (
	// AgentProxyEnvVar nennt die vertrauenswürdige(n) Gegenstelle(n).
	AgentProxyEnvVar = "ABOARD_AGENT_PROXY"
	// MaxFehlversuche je Schlüssel und Fenster.
	MaxFehlversuche = 10
	// FehlversuchFenster: Zählfenster und zugleich Sperrdauer.
	FehlversuchFenster = 10 * time.Minute
	// maxBremsEintraege begrenzt den Speicher bei vielen Adressen.
	maxBremsEintraege = 10000
	proxyNeuAufloesen = time.Minute
)

type bremsEintrag struct {
	fehler []time.Time
	// letzter: jüngster Fehlversuch — für die Verdrängung bei voller Tabelle.
	letzter time.Time
	// gemeldet: die Sperre dieses Schlüssels steht schon im Log (K-9).
	gemeldet bool
}

// bremsLog meldet eine neu eingetretene Sperre (Session-Kritiker Sprint 78,
// K-9: vorher sah der Betreiber einen Angriff nur im Proxy-Log). Nie ein
// Token, nur Adresse und — nicht geheime — Kennung.
var bremsLog = log.Printf

type fehlversuchBremse struct {
	mu      sync.Mutex
	jetzt   func() time.Time
	eintrag map[string]*bremsEintrag
	proxy   *proxyVertrauen
	// max, fenster, name: Grenze, Zählfenster (= Sperrdauer) und Name im
	// Log. Der Agenten-Zugang nimmt MaxFehlversuche/FehlversuchFenster, die
	// Passwort-Anmeldung eigene Werte (ADR-0149 §3, Karte #1074).
	max     int
	fenster time.Duration
	name    string
}

func neueBremse(proxy *proxyVertrauen) *fehlversuchBremse {
	return neueBremseMit(proxy, MaxFehlversuche, FehlversuchFenster, "Agenten-Zugang")
}

func neueBremseMit(proxy *proxyVertrauen, max int, fenster time.Duration, name string) *fehlversuchBremse {
	return &fehlversuchBremse{jetzt: time.Now, eintrag: map[string]*bremsEintrag{}, proxy: proxy,
		max: max, fenster: fenster, name: name}
}

// frisch entfernt Fehlversuche ausserhalb des Fensters (unter mu).
func (b *fehlversuchBremse) frisch(e *bremsEintrag, jetzt time.Time) {
	grenze := jetzt.Add(-b.fenster)
	i := 0
	for i < len(e.fehler) && !e.fehler[i].After(grenze) {
		i++
	}
	e.fehler = e.fehler[i:]
}

// gesperrt meldet, ob der Schlüssel gesperrt ist, und bis wann ungefähr.
func (b *fehlversuchBremse) gesperrt(schluessel string) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.eintrag[schluessel]
	if e == nil {
		return false, 0
	}
	jetzt := b.jetzt()
	b.frisch(e, jetzt)
	if len(e.fehler) < b.max {
		e.gemeldet = false
		return false, 0
	}
	return true, e.fehler[0].Add(b.fenster).Sub(jetzt)
}

func (b *fehlversuchBremse) fehlschlag(schluessel string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	jetzt := b.jetzt()
	e := b.eintrag[schluessel]
	if e == nil {
		if len(b.eintrag) >= maxBremsEintraege {
			b.aufraeumen(jetzt)
		}
		if len(b.eintrag) >= maxBremsEintraege {
			// Session-Kritiker Sprint 78, K-3: Voll mit frischen Einträgen.
			// Vorher wurde der neue Schlüssel dann gar nicht gezählt — wer die
			// Tabelle mit vielen Adressen füllte, war danach selbst nie mehr
			// gesperrt. Jetzt weicht der Eintrag mit dem ältesten letzten
			// Fehlversuch; der Speicher bleibt begrenzt, der neue wird gezählt.
			b.verdraengeAeltesten()
		}
		e = &bremsEintrag{}
		b.eintrag[schluessel] = e
	}
	b.frisch(e, jetzt)
	e.fehler = append(e.fehler, jetzt)
	e.letzter = jetzt
	if len(e.fehler) >= b.max && !e.gemeldet {
		e.gemeldet = true
		bremsLog("aboard-server: %s: %d abgewiesene Versuche in %s — Schlüssel %s bis zum Fensterende gesperrt (429)",
			b.name, len(e.fehler), b.fenster, schluessel)
	}
}

// verdraengeAeltesten entfernt bei voller Tabelle einen Eintrag (unter mu):
// zuerst den ältesten, der NICHT gesperrt ist; einen gesperrten nur, wenn es
// keinen anderen gibt. Verify Sprint 78, V-3: Ein gesperrter Schlüssel bekommt
// keine Fehlversuche mehr (429 kommt vorher), sein `letzter` altert also — ohne
// diese Bevorzugung verdrängte ein Angreifer mit vielen Adressen genau seine
// eigene Sperre. O(n) bei n ≤ maxBremsEintraege, nur bei voller Tabelle.
func (b *fehlversuchBremse) verdraengeAeltesten() {
	var frei, gesperrt string
	var freiZeit, gesperrtZeit time.Time
	for k, e := range b.eintrag {
		if len(e.fehler) >= b.max {
			if gesperrt == "" || e.letzter.Before(gesperrtZeit) {
				gesperrt, gesperrtZeit = k, e.letzter
			}
			continue
		}
		if frei == "" || e.letzter.Before(freiZeit) {
			frei, freiZeit = k, e.letzter
		}
	}
	if frei != "" {
		delete(b.eintrag, frei)
		return
	}
	delete(b.eintrag, gesperrt)
}

func (b *fehlversuchBremse) aufraeumen(jetzt time.Time) {
	for k, e := range b.eintrag {
		b.frisch(e, jetzt)
		if len(e.fehler) == 0 {
			delete(b.eintrag, k)
		}
	}
}

// proxyVertrauen entscheidet, ob eine Gegenstelle der Proxy ist.
type proxyVertrauen struct {
	netze []*net.IPNet
	namen []string
	unix  bool

	mu         sync.Mutex
	aufloeser  func(ctx context.Context, host string) ([]string, error)
	jetzt      func() time.Time
	aufgeloest map[string]bool
	stand      time.Time
	// loest: eine Neuauflösung läuft (K-8) — andere Requests nehmen solange
	// den letzten Stand, statt auf DNS zu warten.
	loest bool
}

// parseProxy liest ABOARD_AGENT_PROXY. Leer = kein Proxy: X-Real-IP wird nie
// ausgewertet, es zählt die TCP-Adresse.
func parseProxy(wert string) (*proxyVertrauen, error) {
	p := &proxyVertrauen{aufloeser: net.DefaultResolver.LookupHost, jetzt: time.Now}
	for _, teil := range strings.Split(wert, ",") {
		t := strings.TrimSpace(teil)
		switch {
		case t == "":
		case t == "unix":
			p.unix = true
		case strings.Contains(t, "/"):
			_, n, err := net.ParseCIDR(t)
			if err != nil {
				return nil, fmt.Errorf("%s: %q ist kein gültiges Netz: %w", AgentProxyEnvVar, t, err)
			}
			p.netze = append(p.netze, n)
		case net.ParseIP(t) != nil:
			ip := net.ParseIP(t)
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			p.netze = append(p.netze, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		default:
			p.namen = append(p.namen, t)
		}
	}
	return p, nil
}

func (p *proxyVertrauen) aktiv() bool {
	return p != nil && (p.unix || len(p.netze) > 0 || len(p.namen) > 0)
}

// istProxy: remote ist r.RemoteAddr ohne Port ("" bzw. "@" bei Unix-Socket).
func (p *proxyVertrauen) istProxy(ctx context.Context, remote string) bool {
	if p == nil {
		return false
	}
	ip := net.ParseIP(remote)
	if ip == nil {
		return p.unix && (remote == "" || remote == "@")
	}
	for _, n := range p.netze {
		if n.Contains(ip) {
			return true
		}
	}
	if len(p.namen) == 0 {
		return false
	}
	return p.namensAdressen(ctx)[ip.String()]
}

// namensAdressen löst die Proxy-Namen höchstens einmal je proxyNeuAufloesen
// neu auf. Scheitert die Auflösung, bleibt der letzte Stand — ein kurzer
// DNS-Aussetzer soll nicht alle Clients auf eine Adresse zusammenwerfen.
func (p *proxyVertrauen) namensAdressen(ctx context.Context) map[string]bool {
	p.mu.Lock()
	jetzt := p.jetzt()
	if (p.aufgeloest != nil && jetzt.Sub(p.stand) < proxyNeuAufloesen) || p.loest {
		// Session-Kritiker Sprint 78, K-8: Die Auflösung läuft NICHT unter der
		// Sperre — sonst wartete jeder Agenten-Request einmal pro Minute bis zu
		// 2 s je Name auf DNS. Während sie läuft, gilt der letzte Stand.
		stand := p.aufgeloest
		p.mu.Unlock()
		return stand
	}
	p.loest = true
	namen, aufloeser := p.namen, p.aufloeser
	p.mu.Unlock()
	// Verify Sprint 78, V-4: `loest` auch bei einem Panic im Auflöser
	// zurücksetzen — sonst hinge die Auflösung für immer auf „läuft“.
	fertig := false
	defer func() {
		if !fertig {
			p.mu.Lock()
			p.loest = false
			p.mu.Unlock()
		}
	}()
	// V-4: nicht am Request-Kontext hängen — bricht der erste Client ab,
	// scheiterten sonst alle Lookups, und der Proxy gälte eine Minute lang
	// als „kein Proxy“ (alle Clients unter seiner Adresse).
	ctx = context.WithoutCancel(ctx)

	neu := map[string]bool{}
	ok := false
	for _, n := range namen {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		adr, err := aufloeser(c, n)
		cancel()
		if err != nil {
			continue
		}
		ok = true
		for _, a := range adr {
			if ip := net.ParseIP(a); ip != nil {
				neu[ip.String()] = true
			}
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	fertig = true
	if ok || p.aufgeloest == nil {
		p.aufgeloest = neu
	}
	p.stand = jetzt
	p.loest = false
	return p.aufgeloest
}

// clientAdresse: X-Real-IP, wenn die Gegenstelle der Proxy ist und der
// Header eine gültige Adresse trägt; sonst die TCP-Adresse.
func (p *proxyVertrauen) clientAdresse(r *http.Request) string {
	remote := r.RemoteAddr
	if h, _, err := net.SplitHostPort(remote); err == nil {
		remote = h
	}
	if p.aktiv() && p.istProxy(r.Context(), remote) {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
			return adresseZaehlbar(ip)
		}
	}
	if remote == "" {
		return "unix"
	}
	if ip := net.ParseIP(remote); ip != nil {
		return adresseZaehlbar(ip)
	}
	return remote
}

// adresseZaehlbar: IPv4 unverändert, IPv6 auf das /64 zusammengefasst
// (Session-Kritiker Sprint 78, K-3) — ein Anschluss hat typischerweise ein
// ganzes /64 und könnte sonst je Versuch eine neue Adresse nehmen.
func adresseZaehlbar(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// bremsSchluessel: (Adresse, bekannte Kennung) oder nur Adresse.
func (z *AgentZugang) bremsSchluessel(r *http.Request, klartext string) string {
	adresse := z.bremse.proxy.clientAdresse(r)
	k := storage.AgentTokenKennung(klartext)
	if k == "" {
		return "a:" + adresse
	}
	kb, ok := z.tokens.(interface {
		KennungBekannt(ctx context.Context, kennung string) (bool, error)
	})
	if !ok {
		return "a:" + adresse
	}
	if bekannt, err := kb.KennungBekannt(r.Context(), k); err != nil || !bekannt {
		return "a:" + adresse
	}
	return "k:" + adresse + "|" + k
}

// ClientAdresse ist die Adressregel der Bremse für andere Drosseln am
// Agenten-Eingang (F-2030): X-Real-IP nur von der Gegenstelle aus
// ABOARD_AGENT_PROXY, sonst die TCP-Adresse; nie X-Forwarded-For.
func (z *AgentZugang) ClientAdresse(r *http.Request) string {
	var p *proxyVertrauen
	if z != nil && z.bremse != nil {
		p = z.bremse.proxy
	}
	return p.clientAdresse(r)
}

// MitProxy konfiguriert die vertrauenswürdige Gegenstelle für die Bremse
// (Wert von ABOARD_AGENT_PROXY). Ohne Aufruf zählt die TCP-Adresse.
func (z *AgentZugang) MitProxy(wert string) error {
	p, err := parseProxy(wert)
	if err != nil {
		return err
	}
	z.bremse.proxy = p
	return nil
}
