package llm

// schleuse_versand.go — Versand eines freigegebenen Auszugs an einen externen
// Anbieter der Ausgangsschleuse (ADR-0134 §3.4–§3.6; Karte #686).
//
// Getrennt vom Board-Chat (ADR-0133 §2): kein Registry-Eintrag, kein
// Nutzerschlüssel aus llm_api_keys — der Schlüssel kommt aus
// schleuse_schluessel, der Anbieter samt Basis-URL aus der Admin-Freigabe.
// Je Versand ein eigener Client (schleuse.VersandTransport): aufgelöste
// Adresse geprüft, keine Redirects, Ausgangs-Schalter davor.
//
// Gesendet wird GENAU der Text, den der Aufrufer übergibt — der Aufrufer
// reicht den gespeicherten, freigegebenen Auszug durch (Hash-Vertrag
// schleuse.Normalisiere/Hash). Ein System-Text sagt dem Modell, die
// Platzhalter stehen zu lassen; er ist fest und enthält nichts vom Board.
//
// Fehler tragen nur Art und Status, nie den Text einer Fehlerantwort
// (ohneAntworttext, Kritiker KS-10): Ein Anbieter, der den Auszug in seiner
// Fehlermeldung zurückspiegelt, schriebe ihn sonst ins Server-Log.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Andiministrator/aBoard/server/ausgang"
	"github.com/Andiministrator/aBoard/server/schleuse"
)

// SchleuseVersandMaxToken begrenzt die Antwort des externen Modells.
const SchleuseVersandMaxToken = 8192

// SchleuseAntwortHoechstZeichen: längere Antworten werden gekürzt, bevor sie
// Kommentar werden (Schutz gegen einen ausufernden Anbieter).
const SchleuseAntwortHoechstZeichen = 100000

// SchleuseVersandSystem ist der feste System-Text jedes Versands.
const SchleuseVersandSystem = "Du bearbeitest einen Auszug aus einer Projektkarte. Namen, Firmen, Adressen und " +
	"Kennungen darin sind durch Platzhalter in eckigen Klammern ersetzt, etwa [PERSON_1] oder [FIRMA_2]. " +
	"Übernimm Platzhalter in deiner Antwort unverändert und genau in dieser Schreibweise; erfinde keine neuen " +
	"und versuche nicht, sie aufzulösen. Antworte in der Sprache des Auszugs, als Markdown."

// ErrSchleuseAnbieterUnbekannt: Die Anbieter-Art der Freigabe kennt der
// Versand nicht.
var ErrSchleuseAnbieterUnbekannt = errors.New("llm: schleuse: anbieter-art unbekannt")

// ErrSchleuseKeineBilder: Der Anbieter-Art fehlt der Bildversand — ein Auftrag
// mit freigegebenen Bildern geht dann gar nicht hinaus (Karte #687).
var ErrSchleuseKeineBilder = errors.New("llm: schleuse: anbieter-art kann keine bilder senden")

// SchleuseBilderMoeglich meldet, ob der Versand an diese Anbieter-Art Bilder
// mitschicken kann (Karte #687, ADR-0134 §5). Mistral nicht: Dessen Provider
// liest SendRequest.Images nicht — die Bilder fielen still weg, obwohl der
// Hash des Auftrags sie als gesendet ausweist.
//
// Ob das gewählte MODELL Bilder versteht, prüft die Schleuse bewusst nicht
// gegen die Preistabelle (SupportsVision): Die kennt nur wenige, zum Teil
// veraltete Namen und im Firmenmodus gar keine OpenAI-Modelle. Kann das
// Modell keine Bilder, lehnt der Anbieter die Anfrage ab — der Auftrag endet
// als „abgelehnt“, und hinausgegangen ist nur, was freigegeben war.
func SchleuseBilderMoeglich(anbieter string) bool {
	switch anbieter {
	case schleuse.AnbieterOpenAIKompatibel, schleuse.AnbieterAnthropic, schleuse.AnbieterGoogle:
		return true
	}
	return false
}

// SchleuseVersand beschreibt einen Versand.
type SchleuseVersand struct {
	Anbieter   string // schleuse.Anbieter* (Art der Freigabe)
	BasisURL   string // Normalform aus der Freigabe
	Modell     string
	Schluessel string
	Text       string // der freigegebene Auszug, Byte für Byte
	// Bilder: die freigegebenen Bilder, bereinigt und gegen ihren Hash
	// geprüft (Karte #687). Der Aufrufer reicht sie durch, wie er den Text
	// durchreicht.
	Bilder []ImageAttachment
	UA     string
	// Transport ersetzt den Versand-Transport (nur Tests: httptest-Server
	// liegen auf 127.0.0.1, den der echte Transport ablehnt). nil = echt.
	Transport http.RoundTripper
}

// SchleuseAntwort ist das Ergebnis eines Versands.
type SchleuseAntwort struct {
	Text         string
	Gekuerzt     bool
	InputTokens  int
	OutputTokens int
	FinishReason string
	KostenUSD    float64
}

// schleuseProvider baut den Anbieter für eine Freigabe.
func schleuseProvider(v SchleuseVersand, client *http.Client) (Provider, error) {
	basis := strings.TrimRight(v.BasisURL, "/")
	switch v.Anbieter {
	case schleuse.AnbieterOpenAIKompatibel:
		return &OpenAIProvider{Client: client, UA: v.UA, BaseURL: basis}, nil
	case schleuse.AnbieterMistral:
		return &MistralProvider{Client: client, UA: v.UA, SendEndpoint: basis + "/chat/completions"}, nil
	case schleuse.AnbieterAnthropic:
		return &AnthropicProvider{Client: client, UA: v.UA, Endpoint: basis + "/v1/messages"}, nil
	case schleuse.AnbieterGoogle:
		return &GoogleProvider{Client: client, UA: v.UA, SendEndpoint: basis + "/v1beta/models/" + v.Modell + ":generateContent"}, nil
	}
	return nil, ErrSchleuseAnbieterUnbekannt
}

// SchleuseVersenden sendet den Auszug und liefert die Antwort. Die Frist
// setzt der Aufrufer über ctx. Fehler: schleuse.ErrInterneAdresse,
// schleuse.ErrUmleitung, ErrSchleuseAnbieterUnbekannt oder ein
// *SchleuseModellFehler (Art + Status, kein Text).
func SchleuseVersenden(ctx context.Context, v SchleuseVersand) (SchleuseAntwort, error) {
	var rt *schleuse.VersandRT
	transport := v.Transport
	if transport == nil {
		var err error
		rt, err = schleuse.VersandTransport(v.BasisURL, nil, AntwortHeaderDeckel)
		if err != nil {
			return SchleuseAntwort{}, err
		}
		transport = rt
	}
	if len(v.Bilder) > 0 && !SchleuseBilderMoeglich(v.Anbieter) {
		return SchleuseAntwort{}, ErrSchleuseKeineBilder
	}
	client := &http.Client{Transport: ausgang.Transport(transport), CheckRedirect: schleuse.OhneUmleitung}
	p, err := schleuseProvider(v, client)
	if err != nil {
		return SchleuseAntwort{}, err
	}
	res, err := p.Send(ctx, v.Schluessel, SendRequest{
		Model:      v.Modell,
		System:     SchleuseVersandSystem,
		UserPrompt: v.Text,
		Images:     v.Bilder,
		MaxTokens:  SchleuseVersandMaxToken,
	})
	if err != nil {
		if rt != nil {
			if vs := rt.Verstoss(); vs != nil {
				return SchleuseAntwort{}, vs
			}
		}
		if errors.Is(err, schleuse.ErrUmleitung) {
			return SchleuseAntwort{}, schleuse.ErrUmleitung
		}
		return SchleuseAntwort{}, ohneAntworttext(err)
	}
	a := SchleuseAntwort{Text: res.Text, InputTokens: res.InputTokens, OutputTokens: res.OutputTokens, FinishReason: res.FinishReason}
	if r := []rune(a.Text); len(r) > SchleuseAntwortHoechstZeichen {
		a.Text, a.Gekuerzt = string(r[:SchleuseAntwortHoechstZeichen]), true
	}
	ein, aus := PricingBei(p, v.Modell, a.InputTokens)
	a.KostenUSD = float64(a.InputTokens)*ein/1e6 + float64(a.OutputTokens)*aus/1e6
	return a, nil
}

// SchleuseFehlerGrund übersetzt einen Versandfehler in einen
// maschinenlesbaren Grund für Auftrag, Verlauf und Oberfläche.
func SchleuseFehlerGrund(err error) string {
	var mf *SchleuseModellFehler
	switch {
	case errors.Is(err, schleuse.ErrInterneAdresse):
		return "interne-adresse"
	case errors.Is(err, schleuse.ErrUmleitung):
		return "umleitung"
	case errors.Is(err, ErrSchleuseAnbieterUnbekannt):
		return "anbieter-unbekannt"
	case errors.Is(err, ErrSchleuseKeineBilder):
		return "keine-bilder"
	case errors.Is(err, context.DeadlineExceeded):
		return "zeitueberschreitung"
	case errors.Is(err, context.Canceled):
		return "abgebrochen"
	case errors.Is(err, ErrInvalidKey):
		return "schluessel-abgelehnt"
	case errors.Is(err, ErrContextTooLong):
		return "zu-lang"
	case errors.Is(err, ErrProviderRejected):
		return "abgelehnt"
	case errors.Is(err, ausgang.ErrGesperrt):
		return "ausgang-gesperrt"
	case errors.As(err, &mf) && mf.Status >= 400 && mf.Status < 500 && mf.Status != 408 && mf.Status != 429:
		// Nicht jeder Anbieter liest 4xx genauer (Anthropic meldet sie als
		// „nicht erreichbar“ samt Status) — der Status entscheidet.
		return "abgelehnt"
	case errors.Is(err, ErrProviderUnreachable):
		return "nicht-erreichbar"
	case errors.As(err, &mf):
		return "anbieter-fehler"
	}
	return "fehler"
}

// SchleuseVersandFrist ist die Gesamtfrist eines Versands — die
// Erstbyte-Frist aus ADR-0133 §3 (300 s); der Header-Deckel des Clients
// (AntwortHeaderDeckel, 330 s) liegt darüber. var: Tests verkürzen sie.
var SchleuseVersandFrist = 300 * time.Second

// SchleuseFehlerText: Art und Status für das Log — nie Antworttext.
func SchleuseFehlerText(err error) string {
	var mf *SchleuseModellFehler
	if errors.As(err, &mf) {
		return fmt.Sprintf("%v (status %d)", mf.Art, mf.Status)
	}
	return SchleuseFehlerGrund(err)
}
