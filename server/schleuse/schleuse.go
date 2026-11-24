// Package schleuse trägt Konfiguration und Prüfregeln der Ausgangsschleuse
// (ADR-0134, Karte #684 — Teil S1).
//
// Die Ausgangsschleuse ist ein eigener, enger Weg, auf dem eine Person einen
// pseudonymisierten, durchgesehenen Auszug einer Karte an ein externes Modell
// schickt. Sie ist vom Board-Chat getrennt: Sie liest den Chat-Schlüsselspeicher
// llm_api_keys nie (ADR-0133 §2, Firmenmodus).
//
// Umgebung:
//
//	ABOARD_SCHLEUSE                    an | aus (Voreinstellung). Bei „aus“
//	                                   registriert der Server keine Route und
//	                                   die Oberfläche zeigt nichts davon.
//	ABOARD_SCHLEUSE_AUFBEWAHRUNG_TAGE  Tage, die eine Zuordnungstabelle nach der
//	                                   Antwort aufbewahrt wird (Voreinstellung
//	                                   30; Löschung: ZuordnungenAufraeumen, S3).
//	                                   Nie gesendete Entwürfe (S2) gehen
//	                                   dieselbe Frist nach ihrer letzten
//	                                   Änderung samt Zuordnung weg.
//	ABOARD_SCHLEUSE_AUSZUG_ZEICHEN     Größengrenze je Auszug in Zeichen
//	                                   (Voreinstellung 20000, Karte #685).
//	ABOARD_SCHLEUSE_PSEUDO_MODELL      Modell des Firmenmodus für die zweite
//	                                   Pseudonymisierungsstufe; leer = das
//	                                   erste aus ABOARD_LLM_OPENAI_MODELLE.
//
// Ob ein freigegebener Anbieter NUTZBAR ist, entscheidet Beurteile: aktiv,
// Vertragsform bestätigt, mindestens ein Modell — und sein Host muss nach der
// geltenden Ausgangs-Politik erreichbar sein (zweiter Riegel, ADR-0134 §1).
// Das wird bei jedem Lesen gegen ausgang.Aktiv() berechnet, nicht gespeichert:
// Die Politik kann sich zwischen zwei Starts ändern, die Datenbank nicht mit.
package schleuse

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/Andiministrator/aBoard/server/ausgang"
)

const (
	// SchalterEnvVar schaltet die Schleuse ein (ADR-0134 §1).
	SchalterEnvVar = "ABOARD_SCHLEUSE"
	// AufbewahrungEnvVar nennt die Aufbewahrung der Zuordnungstabelle in Tagen.
	AufbewahrungEnvVar = "ABOARD_SCHLEUSE_AUFBEWAHRUNG_TAGE"
	// AufbewahrungStandardTage ist die Vorgabe aus ADR-0134 §4.
	AufbewahrungStandardTage = 30
	// aufbewahrungHoechstTage begrenzt Tippfehler (36500 statt 365).
	aufbewahrungHoechstTage = 3650
	// AuszugZeichenEnvVar nennt die Größengrenze je Auszug (Zeichen).
	AuszugZeichenEnvVar = "ABOARD_SCHLEUSE_AUSZUG_ZEICHEN"
	// AuszugZeichenStandard: 20000 Zeichen sind rund zehn Seiten Text. Das ist
	// genug für Beschreibung, eine Handvoll Kommentare und die Aufgabe, und es
	// ist die Menge, die ein Mensch in der Durchsicht noch wirklich liest —
	// die Durchsicht ist die eigentliche Kontrolle (ADR-0134 §3.3). Zugleich
	// passt es mit Antwort in das Kontextfenster eines kleinen lokalen Modells
	// (rund 5000–7000 Token Text; die Fundliste kommt dazu).
	AuszugZeichenStandard = 20000
	auszugZeichenMin      = 500
	auszugZeichenMax      = 200000
	// PseudoModellEnvVar wählt das Modell der zweiten Stufe.
	PseudoModellEnvVar = "ABOARD_SCHLEUSE_PSEUDO_MODELL"
	// AuftraegeEnvVar: höchstens so viele GESENDETE Aufträge je Person und
	// Stunde (ADR-0134 §1, Vorgabe 10; Karte #686).
	AuftraegeEnvVar = "ABOARD_SCHLEUSE_AUFTRAEGE_PRO_STUNDE"
	// AuftraegeStandard ist die Vorgabe, auftraegeHoechst die Obergrenze des
	// Werts.
	AuftraegeStandard = 10
	auftraegeHoechst  = 1000
)

// Anbieter-Arten der Freigabeliste (ADR-0134 §2). Dieselbe Liste steht als
// CHECK in Migration 0026.
const (
	AnbieterOpenAIKompatibel = "openai-kompatibel"
	AnbieterAnthropic        = "anthropic"
	AnbieterMistral          = "mistral"
	AnbieterGoogle           = "google"
)

// AnbieterArten in der Reihenfolge der Oberfläche.
var AnbieterArten = []string{AnbieterOpenAIKompatibel, AnbieterAnthropic, AnbieterMistral, AnbieterGoogle}

// standardBasis: die öffentliche API-Adresse je Art. openai-kompatibel hat
// keine — dort MUSS der Admin die Adresse angeben.
var standardBasis = map[string]string{
	AnbieterAnthropic: "https://api.anthropic.com",
	AnbieterMistral:   "https://api.mistral.ai/v1",
	AnbieterGoogle:    "https://generativelanguage.googleapis.com",
}

// Grenzen der Modellliste: genug für jeden realen Anbieter, klein genug, dass
// ein eingefügter Textblock nicht als Modellliste durchgeht.
const (
	ModelleHoechstzahl   = 50
	modellnameHoechstLen = 200
)

var modellnameMuster = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]*$`)

// gueltigerModellname: Muster plus Segmentprüfung (KC-5). Der Name landet in
// S3 womöglich im Pfad einer Anfrage (…/models/{name}:generateContent) — „..“,
// „.“ und leere Segmente würden dort den Pfad verschieben.
func gueltigerModellname(m string) bool {
	if len(m) > modellnameHoechstLen || !modellnameMuster.MatchString(m) {
		return false
	}
	for _, seg := range strings.Split(m, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// Konfig ist der gelesene Stand der beiden Variablen.
type Konfig struct {
	An               bool
	AufbewahrungTage int
	// AuszugZeichen ist die Größengrenze je Auszug; 0 = Voreinstellung
	// (GrenzeZeichen liefert den wirksamen Wert).
	AuszugZeichen int
	// PseudoModell ist der Rohwert von ABOARD_SCHLEUSE_PSEUDO_MODELL; geprüft
	// wird er gegen die Modelle des Firmenmodus (WaehlePseudoModell).
	PseudoModell string
	// AuftraegeProStunde: gesendete Aufträge je Person und Stunde; 0 =
	// Voreinstellung (GrenzeAuftraege liefert den wirksamen Wert).
	AuftraegeProStunde int
}

// GrenzeAuftraege liefert die wirksame Grenze der gesendeten Aufträge je
// Person und Stunde.
func (k Konfig) GrenzeAuftraege() int {
	if k.AuftraegeProStunde <= 0 {
		return AuftraegeStandard
	}
	return k.AuftraegeProStunde
}

// GrenzeZeichen liefert die wirksame Größengrenze je Auszug.
func (k Konfig) GrenzeZeichen() int {
	if k.AuszugZeichen <= 0 {
		return AuszugZeichenStandard
	}
	return k.AuszugZeichen
}

// WaehlePseudoModell bestimmt das Modell der zweiten Pseudonymisierungsstufe:
// roh, wenn gesetzt — dann muss es in den Modellen des Firmenmodus stehen
// (sonst Fehler, der Start bricht ab) —, sonst das erste erlaubte Modell.
// Ohne Firmenmodus-Modelle "" (kein lokales Modell, Auszüge brechen ab).
func WaehlePseudoModell(roh string, erlaubt []string) (string, error) {
	roh = strings.TrimSpace(roh)
	if roh == "" {
		if len(erlaubt) == 0 {
			return "", nil
		}
		return erlaubt[0], nil
	}
	for _, m := range erlaubt {
		if m == roh {
			return roh, nil
		}
	}
	return "", fmt.Errorf("%s=%q: steht nicht in ABOARD_LLM_OPENAI_MODELLE (Firmenmodus) — die zweite Stufe läuft nur über ein freigegebenes lokales Modell", PseudoModellEnvVar, roh)
}

// AusUmgebung liest ABOARD_SCHLEUSE und ABOARD_SCHLEUSE_AUFBEWAHRUNG_TAGE. Ein
// unbekannter Wert ist ein Fehler: Der Server startet dann nicht, statt im
// Zweifel einen Weg nach draussen zu öffnen (Muster ausgang.AusUmgebung).
func AusUmgebung() (Konfig, error) {
	k := Konfig{AufbewahrungTage: AufbewahrungStandardTage}
	switch wert := strings.ToLower(strings.TrimSpace(os.Getenv(SchalterEnvVar))); wert {
	case "", "aus":
	case "an":
		k.An = true
	default:
		return Konfig{}, fmt.Errorf("%s=%q: erlaubt sind \"an\" und \"aus\"", SchalterEnvVar, wert)
	}
	if roh := strings.TrimSpace(os.Getenv(AufbewahrungEnvVar)); roh != "" {
		n, err := strconv.Atoi(roh)
		if err != nil || n < 1 || n > aufbewahrungHoechstTage {
			return Konfig{}, fmt.Errorf("%s=%q: erwartet eine ganze Zahl von 1 bis %d", AufbewahrungEnvVar, roh, aufbewahrungHoechstTage)
		}
		k.AufbewahrungTage = n
	}
	if roh := strings.TrimSpace(os.Getenv(AuszugZeichenEnvVar)); roh != "" {
		n, err := strconv.Atoi(roh)
		if err != nil || n < auszugZeichenMin || n > auszugZeichenMax {
			return Konfig{}, fmt.Errorf("%s=%q: erwartet eine ganze Zahl von %d bis %d", AuszugZeichenEnvVar, roh, auszugZeichenMin, auszugZeichenMax)
		}
		k.AuszugZeichen = n
	}
	k.PseudoModell = strings.TrimSpace(os.Getenv(PseudoModellEnvVar))
	if roh := strings.TrimSpace(os.Getenv(AuftraegeEnvVar)); roh != "" {
		n, err := strconv.Atoi(roh)
		if err != nil || n < 1 || n > auftraegeHoechst {
			return Konfig{}, fmt.Errorf("%s=%q: erwartet eine ganze Zahl von 1 bis %d", AuftraegeEnvVar, roh, auftraegeHoechst)
		}
		k.AuftraegeProStunde = n
	}
	return k, nil
}

// Beschreibung für das Start-Log.
func (k Konfig) Beschreibung() string {
	if !k.An {
		return "Ausgangsschleuse: aus (ABOARD_SCHLEUSE nicht \"an\") — keine Routen, keine Oberfläche"
	}
	return fmt.Sprintf("Ausgangsschleuse: AN — Zuordnungen werden %d Tage nach der Antwort gelöscht, höchstens %d gesendete Aufträge je Person und Stunde",
		k.AufbewahrungTage, k.GrenzeAuftraege())
}

// GueltigeArt meldet, ob art eine bekannte Anbieter-Art ist.
func GueltigeArt(art string) bool {
	for _, a := range AnbieterArten {
		if a == art {
			return true
		}
	}
	return false
}

// PruefeBasis prüft die Basis-URL eines Eintrags und liefert sie in EINER
// Schreibweise (Normalform): Host klein, ohne Punkt am Ende, ohne Port 443,
// Pfad ohne abschliessende Schrägstriche. Nur so greift der UNIQUE-Index
// (anbieter, basis_url) — sonst wären „https://X.example/v1/“ und
// „https://x.example:443/v1“ zwei Einträge für dasselbe Gegenüber (KC-4).
// Leer heisst „öffentliche Adresse der Art“ — ausser bei openai-kompatibel,
// dort ist sie Pflicht.
//
// Nur https: Über die Schleuse geht der Schlüssel der Person und ein Auszug
// aus Board-Inhalten an einen fremden Rechner. Keine Zugangsdaten, keine
// Query, kein Fragment, keine ./..-Segmente, keine leeren Segmente („//“).
//
// Nur EXTERNE Ziele (KC-1): Die Schleuse ist ausschliesslich der Weg zu
// externen Anbietern; lokale Modelle laufen über den Firmenmodus (ADR-0133).
// Ein interner Host wäre die Grundlage für SSRF — der Server schickte den
// Schlüssel der Person und Board-Inhalte an einen Rechner im eigenen Netz
// (Metadaten-Dienst 169.254.169.254, Admin-Oberflächen auf localhost …).
// Abgelehnt werden deshalb Literal-Adressen aus internen Bereichen, jede
// Zahlform eines Hosts und Namen, die nur intern auflösen (pruefeHost).
//
// ⚠️ Was das NICHT leistet — leistet der Versand-Client (versand.go, S3): Ein öffentlicher NAME
// kann zur Laufzeit auf eine interne Adresse auflösen (DNS-Rebinding) oder
// per Redirect dorthin weiterleiten. Beim Verbinden muss deshalb
//   - die AUFGELÖSTE Adresse gegen dieselben Bereiche geprüft werden
//     (net.Dialer.Control bzw. eigener DialContext, nach der Auflösung,
//     für jede Verbindung — nicht einmal vorab),
//   - der Client keinen Redirects folgen (CheckRedirect →
//     http.ErrUseLastResponse); ein 3xx ist ein Fehler des Auftrags.
//
// Beides tut VersandTransport (versand.go); InternesZiel ist dafür exportiert.
func PruefeBasis(art, roh string) (string, error) {
	roh = strings.TrimSpace(roh)
	if roh == "" {
		if s, ok := standardBasis[art]; ok {
			return s, nil
		}
		return "", fmt.Errorf("basis_url: für %s Pflicht", art)
	}
	if strings.IndexFunc(roh, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("basis_url: enthält Leerraum")
	}
	u, err := url.Parse(roh)
	if err != nil {
		return "", fmt.Errorf("basis_url: keine gültige URL")
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("basis_url: nur https erlaubt")
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("basis_url: kein Host")
	}
	if u.User != nil {
		return "", fmt.Errorf("basis_url: keine Zugangsdaten in der URL erlaubt")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(roh, "#") {
		return "", fmt.Errorf("basis_url: keine Query und kein Fragment erlaubt")
	}
	port := u.Port()
	if port != "" {
		n, perr := strconv.Atoi(port)
		if perr != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("basis_url: Port muss zwischen 1 und 65535 liegen")
		}
		// Als Zahl, nicht als Text (Verify VS-3): „:0443“ ist derselbe Port
		// wie „:443“ und fällt ebenso weg; „:08443“ wird „:8443“.
		port = strconv.Itoa(n)
		if n == 443 {
			port = "" // Standardport fällt weg (Normalform)
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("basis_url: leerer Port")
	}
	host, err := pruefeHost(u.Hostname())
	if err != nil {
		return "", err
	}
	// Pfad: geprüft auf der dekodierten UND der kodierten Form — „%2e%2e“
	// ist dekodiert „..“, „%2F%2F“ kodiert kein „//“, dekodiert aber schon.
	pfad := strings.TrimRight(u.EscapedPath(), "/")
	for _, p := range []string{strings.TrimRight(u.Path, "/"), pfad} {
		if strings.Contains(p, "//") {
			return "", fmt.Errorf("basis_url: keine leeren Segmente („//“) im Pfad")
		}
		for _, seg := range strings.Split(p, "/") {
			if seg == "." || seg == ".." {
				return "", fmt.Errorf("basis_url: keine ./..-Segmente im Pfad")
			}
		}
	}
	pfad = normalisiereEscapes(pfad)
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6-Literal (öffentlich, sonst schon abgelehnt)
	}
	if port != "" {
		host += ":" + port
	}
	return "https://" + host + pfad, nil
}

// normalisiereEscapes bringt Prozent-Kodierungen im Pfad in eine Form
// (RFC 3986 §6.2.2, Verify VS-3): Hex-Ziffern groß („%2f“ → „%2F“), und
// kodierte nicht reservierte Zeichen (Buchstaben, Ziffern, „-._~“) werden
// dekodiert („%7E“ → „~“). Sonst greift UNIQUE(anbieter, basis_url) bei zwei
// Schreibweisen desselben Pfads nicht.
func normalisiereEscapes(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && i+2 < len(p) && istHex(p[i+1]) && istHex(p[i+2]) {
			c, _ := strconv.ParseUint(p[i+1:i+3], 16, 8)
			if r := byte(c); r < 0x80 && (r == '-' || r == '.' || r == '_' || r == '~' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
				b.WriteByte(r)
			} else {
				b.WriteString(strings.ToUpper(p[i : i+3]))
			}
			i += 2
			continue
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

func istHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// zahlLabel: ein Host-Teil, den Browser und manche Resolver als Zahl lesen —
// dezimal, oktal (führende 0) oder hex (0x…).
var zahlLabel = regexp.MustCompile(`^(0[xX][0-9a-fA-F]*|[0-9]+)$`)

// hostLabel: ASCII-Label nach RFC 1123 (Buchstaben, Ziffern, Bindestrich).
// Internationale Namen trägt der Admin in Punycode ein (xn--…).
var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// interneEndungen: Namen, die nie öffentlich auflösen (RFC 6761, RFC 6762,
// RFC 8375, ICANN-Reservierung .internal).
// „.localdomain“ ist keine Reservierung, aber die Linux-Vorgabe in /etc/hosts
// („localhost.localdomain“ → 127.0.0.1) und nie ein öffentlicher Anbieter
// (Verify VS-2).
var interneEndungen = []string{".localhost", ".local", ".internal", ".home.arpa", ".localdomain"}

// pruefeHost lehnt interne Ziele ab und liefert den Host in Normalform
// (klein, ohne Punkt am Ende). Die IPv6-Klammern hat url.Hostname schon
// entfernt.
func pruefeHost(roh string) (string, error) {
	host := strings.ToLower(roh)
	if strings.HasSuffix(host, ".") {
		host = strings.TrimSuffix(host, ".") // „api.example.com.“ ist derselbe Name
	}
	if host == "" || strings.HasSuffix(host, ".") {
		return "", fmt.Errorf("basis_url: kein gültiger Host")
	}
	if adr, err := netip.ParseAddr(host); err == nil {
		if InternesZiel(adr) {
			return "", fmt.Errorf("basis_url: interne Adresse %s nicht erlaubt — die Schleuse geht nur zu externen Anbietern; lokale Modelle laufen über den Firmenmodus", host)
		}
		return adr.Unmap().String(), nil
	}
	labels := strings.Split(host, ".")
	// Endet der Host auf einen Zahl-Teil, ist er eine (Kurz-)Form einer
	// IPv4-Adresse — „2130706433“, „0x7f.1“, „127.1“, „0177.0.0.1“. Was
	// netip nicht als Adresse liest, ist deshalb noch kein Name: Resolver
	// und Browser lesen es als 127.0.0.1. Abgelehnt, ohne zu deuten.
	if zahlLabel.MatchString(labels[len(labels)-1]) {
		return "", fmt.Errorf("basis_url: Host %q ist eine Zahlform einer IP-Adresse — nur Namen oder öffentliche Adressen in Normalform", roh)
	}
	for _, l := range labels {
		if !hostLabel.MatchString(l) {
			return "", fmt.Errorf("basis_url: Host %q ist kein gültiger Name", roh)
		}
	}
	if len(labels) < 2 {
		return "", fmt.Errorf("basis_url: Host %q ohne Punkt — nur öffentliche Namen erlaubt", roh)
	}
	if host == "localhost" {
		return "", fmt.Errorf("basis_url: localhost nicht erlaubt")
	}
	for _, e := range interneEndungen {
		if strings.HasSuffix(host, e) {
			return "", fmt.Errorf("basis_url: Host %q löst nur intern auf (%s) — nicht erlaubt", roh, e)
		}
	}
	return host, nil
}

// cgnat ist der Bereich für Carrier-Grade-NAT (RFC 6598) — von netip nicht
// als privat geführt, aber nie ein öffentliches Ziel.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// dieseNetz ist 0.0.0.0/8 („dieses Netz“): Linux verbindet 0.0.0.x mit dem
// eigenen Rechner.
var dieseNetz = netip.MustParsePrefix("0.0.0.0/8")

// nichtOeffentlich: weitere Bereiche, die kein externer Anbieter belegt
// (IANA Special-Purpose Registry, Verify VS-2). Die IPv6-Präfixe mit
// eingebetteter IPv4-Adresse (IPv4-kompatibel ::/96, SIIT, NAT64, 6to4,
// Teredo) werden GANZ abgelehnt statt entpackt: Ein Anbieter-Endpunkt liegt
// dort nie, und ein Entpacken müsste jede Kodierung kennen — eine vergessene
// führt am Ende doch auf 127.0.0.1.
var nichtOeffentlich = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"192.0.0.0/24",    // IETF-Protokollzuweisungen
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // Benchmark
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserviert, darin 255.255.255.255
		"::/96",           // IPv4-kompatibel (veraltet), etwa ::127.0.0.1
		"::ffff:0:0:0/96", // SIIT
		"64:ff9b::/96",    // NAT64
		"64:ff9b:1::/48",  // NAT64 lokal
		"2002::/16",       // 6to4
		"2001::/32",       // Teredo
		"2001:db8::/32",   // Dokumentation
		"100::/64",        // Discard
		"fec0::/10",       // Site-Local (veraltet)
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// InternesZiel meldet, ob eine Adresse in einem Bereich liegt, den die
// Schleuse nie ansteuert: Loopback, Link-Local (darin der Metadaten-Dienst
// 169.254.169.254), privat (RFC 1918, ULA fc00::/7), Unspecified, CGNAT,
// 0.0.0.0/8, Multicast und die Sonderbereiche aus nichtOeffentlich. IPv4 in
// IPv6 (::ffff:127.0.0.1) zählt wie IPv4. S3 ruft dieselbe Prüfung auf die
// AUFGELÖSTE Adresse beim Verbinden.
func InternesZiel(adr netip.Addr) bool {
	adr = adr.Unmap()
	if adr.IsLoopback() || adr.IsLinkLocalUnicast() || adr.IsLinkLocalMulticast() ||
		adr.IsInterfaceLocalMulticast() || adr.IsMulticast() || adr.IsPrivate() ||
		adr.IsUnspecified() || cgnat.Contains(adr) || dieseNetz.Contains(adr) {
		return true
	}
	for _, p := range nichtOeffentlich {
		if p.Contains(adr) {
			return true
		}
	}
	return false
}

// NormalformOderRoh liefert die Normalform einer gespeicherten Basis-URL —
// für Vergleiche mit Einträgen, die vor der Normalisierung angelegt wurden.
// Lässt sie sich nicht prüfen (etwa ein inzwischen abgelehnter interner
// Host), kommt sie unverändert zurück.
func NormalformOderRoh(basis string) string {
	if n, err := PruefeBasis(AnbieterOpenAIKompatibel, basis); err == nil {
		return n
	}
	return basis
}

// PruefeModelle bereinigt die Modellliste: getrimmt, leere und doppelte
// Einträge fallen weg, Reihenfolge bleibt. Ein Name mit Leerraum oder
// Steuerzeichen ist ein Fehler, ebenso mehr als ModelleHoechstzahl Einträge.
func PruefeModelle(roh []string) ([]string, error) {
	out := []string{}
	gesehen := map[string]bool{}
	for _, m := range roh {
		m = strings.TrimSpace(m)
		if m == "" || gesehen[m] {
			continue
		}
		if !gueltigerModellname(m) {
			return nil, fmt.Errorf("modelle: %q ist kein gültiger Modellname", m)
		}
		gesehen[m] = true
		out = append(out, m)
	}
	if len(out) > ModelleHoechstzahl {
		return nil, fmt.Errorf("modelle: höchstens %d Einträge", ModelleHoechstzahl)
	}
	return out, nil
}

// Host liefert den Host (mit Port, falls angegeben) einer geprüften Basis-URL —
// so, wie er in ABOARD_AUSGAENGE_ERLAUBT stehen muss.
func Host(basis string) string {
	u, err := url.Parse(basis)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// Gründe, aus denen ein Eintrag nicht nutzbar ist. Die Oberfläche übersetzt
// sie; die Reihenfolge in Beurteilung.Gruende folgt dieser Liste.
const (
	GrundInaktiv         = "inaktiv"
	GrundVertragsform    = "vertragsform-fehlt"
	GrundKeineModelle    = "keine-modelle"
	GrundAusgangGesperrt = "ausgang-gesperrt"
	// GrundBasisIntern: Die gespeicherte Basis-URL besteht die heutige
	// Prüfung nicht mehr — ein Eintrag von vor KC-1 mit internem Ziel
	// (Verify VS-6). Nie nutzbar, auch wenn der Host in der Ausgangsliste steht.
	GrundBasisIntern = "basis-intern"
)

// Beurteilung ist das Ergebnis von Beurteile.
type Beurteilung struct {
	// Freigegeben: aktiv und Vertragsform bestätigt — die Entscheidung des Admins.
	Freigegeben bool
	// Nutzbar: freigegeben, mit Modellen, und der Host ist erreichbar.
	Nutzbar bool
	Gruende []string
}

// Beurteile entscheidet über einen Eintrag gegen die übergebene Ausgangs-
// Politik (nil = ausgang.Aktiv()).
func Beurteile(aktiv, vertragsform bool, modelle []string, basis string, pol *ausgang.Politik) Beurteilung {
	if pol == nil {
		pol = ausgang.Aktiv()
	}
	var b Beurteilung
	if !aktiv {
		b.Gruende = append(b.Gruende, GrundInaktiv)
	}
	if !vertragsform {
		b.Gruende = append(b.Gruende, GrundVertragsform)
	}
	b.Freigegeben = aktiv && vertragsform
	if len(modelle) == 0 {
		b.Gruende = append(b.Gruende, GrundKeineModelle)
	}
	if _, err := PruefeBasis(AnbieterOpenAIKompatibel, basis); err != nil {
		b.Gruende = append(b.Gruende, GrundBasisIntern)
	}
	u, err := url.Parse(basis)
	if err != nil || u.Host == "" || pol.PruefeURL(u.Host, u.Scheme) != nil {
		b.Gruende = append(b.Gruende, GrundAusgangGesperrt)
	}
	b.Nutzbar = len(b.Gruende) == 0
	if b.Gruende == nil {
		b.Gruende = []string{}
	}
	return b
}
