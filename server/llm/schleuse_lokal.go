package llm

// schleuse_lokal.go — Aufruf des lokalen Firmenmodus-Modells für die zweite
// Pseudonymisierungsstufe der Ausgangsschleuse (ADR-0134 §1, §3.2; Karte
// #685).
//
// Die Schleuse braucht das lokale Modell NICHT als Chat, sondern als
// einmaligen, nicht gestreamten Aufruf: fest pseudonymisierter Text hinein,
// Fundliste heraus. Dieser Aufruf ist von Registry und Board-Chat getrennt
// (eigener Provider-Wert, derselbe Client mit Ausgangs-Schalter und
// Header-Deckel), damit nichts an der Chat-Konfiguration hängt.
//
// Lokal heisst hier: Firmenmodus aktiv UND eigene Basis-URL
// (ABOARD_LLM_OPENAI_BASE_URL) UND diese zeigt auf ein internes Ziel
// (SchleuseBasisIntern, F-1617). Ohne Basis-URL spräche der Firmenmodus mit
// OpenAI selbst, mit einer öffentlichen Basis-URL mit irgendeinem
// OpenAI-kompatiblen Anbieter — dann ginge der noch nicht durchgesehene
// Text nach draussen. In diesen Fällen gibt es keinen Aufruf (nil), das
// Start-Log nennt den Grund, und jeder Auszug bricht ab (503).
//
// Fehler des Aufrufs tragen nur Fehlerart und HTTP-Status, nie den Text der
// Fehlerantwort (Kritiker KS-10): Ein lokales Modell, das bei 4xx den Body
// zurückspiegelt, schriebe sonst Reste des Auszugs ins Server-Log.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/Andiministrator/aBoard/server/schleuse"
)

// SchleuseMaxAntwortToken begrenzt die Antwort des lokalen Modells. Eine
// Fundliste ist kurz; eine abgeschnittene Liste (finish_reason „length“)
// ist ein Fehler, kein Teilergebnis.
const SchleuseMaxAntwortToken = 4096

// ErrSchleuseAntwortAbgeschnitten: Das lokale Modell hat die Antwort an der
// Token-Grenze abgebrochen — die Fundliste wäre unvollständig.
var ErrSchleuseAntwortAbgeschnitten = errors.New("llm: antwort des lokalen modells abgeschnitten")

// SchleuseModellAufruf liefert den Aufruf des lokalen Modells oder nil, wenn
// keiner möglich ist (kein Firmenmodus, keine eigene Basis-URL, kein Modell
// oder ein Modell, das der Firmenmodus nicht erlaubt). Die Frist setzt der
// Aufrufer über ctx; der Header-Deckel des Clients (AntwortHeaderDeckel)
// liegt darüber.
func SchleuseModellAufruf(f *Firmenmodus, basisURL, ua, modell string) func(ctx context.Context, system, text string) (string, error) {
	basisURL = strings.TrimSpace(basisURL)
	if !f.Aktiv() || basisURL == "" || modell == "" || !f.ModellErlaubt(modell) {
		return nil
	}
	if err := SchleuseBasisIntern(basisURL); err != nil {
		log.Printf("aboard-server: WARNUNG: Ausgangsschleuse: keine zweite Pseudonymisierungsstufe — %v; jeder Auszug bricht ab (503)", err)
		return nil
	}
	// Kritiker KS-4 (Sprint-82-Abschluss, Runde 2): keinem Redirect folgen.
	// Ein 307/308 schickte den noch nicht durchgesehenen Text erneut — an ein
	// Ziel, das SchleuseBasisIntern nie geprüft hat. Wie beim Versand
	// (schleuse.OhneUmleitung) gilt die Antwort mit dem Redirect als Ergebnis;
	// der Provider wertet sie als Fehler. defaultClient liefert je Aufruf
	// einen eigenen Client, der Board-Chat bleibt unberührt.
	client := defaultClient()
	client.CheckRedirect = schleuse.OhneUmleitung
	p := &OpenAIProvider{Client: client, UA: ua, BaseURL: basisURL}
	schluessel := f.Schluessel()
	return func(ctx context.Context, system, text string) (string, error) {
		res, err := p.Send(ctx, schluessel, SendRequest{
			Model:      modell,
			System:     system,
			UserPrompt: text,
			MaxTokens:  SchleuseMaxAntwortToken,
		})
		if err != nil {
			return "", ohneAntworttext(err)
		}
		if res.FinishReason == "length" {
			return "", ErrSchleuseAntwortAbgeschnitten
		}
		return res.Text, nil
	}
}

// interneNamensendungen: Namen, die nur im eigenen Netz auflösen (RFC 6761,
// 6762, 8375, ICANN „.internal“) und die verbreiteten Hausendungen „.intern“
// und „.lan“ — die Betriebsdoku nennt „llm.intern“ als Beispiel.
var interneNamensendungen = []string{".localhost", ".local", ".internal", ".intern", ".lan", ".home.arpa", ".localdomain"}

// SchleuseBasisIntern prüft, dass die Basis-URL des lokalen Modells auf ein
// INTERNES Ziel zeigt (F-1617): eine Adresse aus schleuse.InternesZiel
// (Loopback, privat, Link-Local …), localhost, ein Name ohne Punkt
// (Rechnername im eigenen Netz) oder ein Name mit interner Endung. Alles
// andere ist ein Fehler.
//
// Bewusst OHNE Namensauflösung: Ein Name wie „llm.firma.de“ kann intern
// auflösen, aber das zu prüfen hieße, beim Start ins Netz zu fragen — und
// die Antwort gälte nur für diesen Augenblick. Wer sein lokales Modell unter
// einem solchen Namen betreibt, trägt die Adresse oder einen internen Namen
// ein (Betriebsdoku).
func SchleuseBasisIntern(basisURL string) error {
	u, err := url.Parse(strings.TrimSpace(basisURL))
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("Basis-URL des lokalen Modells nicht lesbar")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if adr, err := netip.ParseAddr(host); err == nil {
		if schleuse.InternesZiel(adr) {
			return nil
		}
		return fmt.Errorf("Basis-URL des lokalen Modells zeigt auf die öffentliche Adresse %s — die zweite Stufe läuft nur gegen ein internes Ziel", host)
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return nil
	}
	for _, e := range interneNamensendungen {
		if strings.HasSuffix(host, e) {
			return nil
		}
	}
	return fmt.Errorf("Basis-URL des lokalen Modells zeigt auf %q, keinen internen Namen (ohne Punkt, localhost, .local, .internal, .intern, .lan, .home.arpa) und keine interne Adresse — die zweite Stufe läuft nur gegen ein internes Ziel", host)
}

// ErrSchleuseModellFehler: ein Fehler des lokalen Modells ohne bekannte Art.
var ErrSchleuseModellFehler = errors.New("llm: lokales modell: fehler")

// SchleuseModellFehler ist ein Fehler des lokalen Modells ohne jeden Text
// der Gegenseite: nur Art (für errors.Is) und HTTP-Status.
type SchleuseModellFehler struct {
	Art    error
	Status int
}

func (e *SchleuseModellFehler) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("%v (HTTP %d)", e.Art, e.Status)
	}
	return e.Art.Error()
}

func (e *SchleuseModellFehler) Unwrap() error { return e.Art }

var statusImText = regexp.MustCompile(`status ([0-9]{3})\b`)

// ohneAntworttext ersetzt einen Fehler des Aufrufs durch Art und Status.
func ohneAntworttext(err error) error {
	var pf *ProviderFehler
	if errors.As(err, &pf) {
		return &SchleuseModellFehler{Art: pf.Art, Status: pf.Status}
	}
	status := 0
	if m := statusImText.FindStringSubmatch(err.Error()); m != nil {
		status, _ = strconv.Atoi(m[1])
	}
	for _, art := range []error{context.DeadlineExceeded, context.Canceled, ErrInvalidKey, ErrProviderUnreachable,
		ErrContextTooLong, ErrToolsUnsupported, ErrProviderRejected} {
		if errors.Is(err, art) {
			return &SchleuseModellFehler{Art: art, Status: status}
		}
	}
	return &SchleuseModellFehler{Art: ErrSchleuseModellFehler, Status: status}
}
