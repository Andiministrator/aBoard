package llm

// pricing.go — Statische Provider-Preise pro Modell (#299, ADR-0095 §7).
//
// USD pro 1 Million Tokens, getrennt nach Input und Output (alle vier MVP-
// Provider modellieren so). Werte sind Stand 2026-05-23 und müssen 1×/Quartal
// nachgepflegt werden, sobald Provider die Tarife ändern (Pflege durch den Maintainer).
//
// Der Server liefert die Tabelle via GET /llm/pricing an das Frontend (#299
// Etappe 2c) — Frontend cached pro Session. Heuristik-Schätzung im UI (§6,
// charCount/4) × dieser Sätze ergibt die Pre-Send-Cost-Vorschätzung.
//
// Lookup-Pattern: Provider.Pricing(model) → (input, output) — gibt (0, 0)
// zurück, wenn der Modell-Name unbekannt ist. UI zeigt dann "Preise unbekannt"
// statt eine falsche Zahl.

// PricingEntry bündelt die zwei Sätze für ein Modell — und seit #429
// (ADR-0121) die Bild-Fähigkeit.
//
// Warum Vision AUSGERECHNET hier steht und nicht in einer eigenen Liste: Die
// Modell-Namen stehen schon in dieser Tabelle, sie reist über
// GET /llm/pricing ohnehin ins Frontend, und wer ein Modell ergänzt, sieht
// das Feld direkt daneben. Eine zweite Liste an anderer Stelle veraltet
// still — genau das war die Auflage auf Karte #429 („Provider-Capability
// nicht raten … eine harte Liste im Code veraltet still").
//
// Das Feld teilt die Pflege-Eigenschaft der Preise: Stand 2026-08-28, gehört
// nachgezogen, wenn ein Anbieter seine Modell-Palette ändert.
type PricingEntry struct {
	InputPerMillionUSD  float64
	OutputPerMillionUSD float64
	Vision              bool
	// Staffel ist der Satz für lange Prompts (F-3102, Karte #1176); nil =
	// ein Satz für alle Längen. Zusätzliches JSON-Feld in GET /llm/pricing:
	// ältere Frontends ignorieren es, die beiden Grundsätze bleiben unverändert.
	Staffel *PreisStaffel `json:",omitempty"`
}

// PreisStaffel ist der Satz, der gilt, sobald der Prompt MEHR als
// UeberPromptTokens Tokens hat. Die Prompt-Länge zählt Cache-Tokens mit
// (Quelle: platform.claude.com/docs/en/about-claude/pricing, 2026-11-20).
//
// Grenzfall: „über 100K" heisst strikt grösser — genau 100.000 Tokens kosten
// noch den Grundsatz. Das folgt dem Wortlaut der Preisseite („bis 100K" und
// „über 100K"); die Schätzung ist ohnehin grob (charCount/4).
type PreisStaffel struct {
	UeberPromptTokens   int
	InputPerMillionUSD  float64
	OutputPerMillionUSD float64
}

// Satz liefert (Input, Output) je Million Tokens für einen Prompt dieser Länge.
func (e PricingEntry) Satz(promptTokens int) (float64, float64) {
	if s := e.Staffel; s != nil && promptTokens > s.UeberPromptTokens {
		return s.InputPerMillionUSD, s.OutputPerMillionUSD
	}
	return e.InputPerMillionUSD, e.OutputPerMillionUSD
}

// StaffelProvider ist die optionale Erweiterung von Provider für Modelle mit
// Staffelpreisen. Provider ohne sie (und Test-Mocks) gelten mit PricingBei als
// einsätzig.
type StaffelProvider interface {
	PricingBei(model string, promptTokens int) (float64, float64)
}

// PricingBei ist der Einstieg für die Kostenrechnung: Satz je Million Tokens
// passend zur tatsächlichen Prompt-Länge (F-3102). Fällt auf Pricing zurück.
func PricingBei(p Provider, model string, promptTokens int) (float64, float64) {
	if sp, ok := p.(StaffelProvider); ok {
		return sp.PricingBei(model, promptTokens)
	}
	return p.Pricing(model)
}

// KostenUSD rechnet die Kosten EINER Anfrage (Staffel nach deren Prompt-Tokens).
// Mehrrundige Turns summieren je Runde (F-3102-Nacharbeit).
func KostenUSD(p Provider, model string, inputTokens, outputTokens int) float64 {
	inPM, outPM := PricingBei(p, model, inputTokens)
	return (float64(inputTokens)/1_000_000)*inPM + (float64(outputTokens)/1_000_000)*outPM
}

// lookupPricingBei wie lookupPricing, berücksichtigt aber die Staffel.
func lookupPricingBei(table ProviderPricing, model string, promptTokens int) (float64, float64) {
	if entry, ok := table[model]; ok {
		return entry.Satz(promptTokens)
	}
	return 0, 0
}

// ProviderPricing ist die statische Modell→Preis-Tabelle eines Providers.
type ProviderPricing map[string]PricingEntry

// anthropicPricing — Anthropic Claude (https://platform.claude.com/docs/en/about-claude/pricing,
// abgerufen 2026-11-20). Modell-Namen entsprechen den /v1/messages-API-Identifiern.
//
// Haiku 5.5 hat Staffelpreise nach Prompt-Länge (bis 100.000 Tokens 0,10/0,50 $,
// darüber 0,50/2,50 $) — als Staffel am Eintrag abgebildet (F-3102, Karte #1176);
// Server-Kostenrechnung (PricingBei) und Frontend nehmen darüber den höheren Satz.
//
// Die Vorgänger-IDs (4.x) bleiben als Einträge, weil gespeicherte Unterhaltungen,
// Sperren und Kartenkommentare sie als Modellnamen tragen und ein Preis dort
// weiter angezeigt werden soll; das Frontend bietet sie nicht mehr zur Auswahl an.
var anthropicPricing = ProviderPricing{
	"claude-opus-5-5":   {InputPerMillionUSD: 4.00, OutputPerMillionUSD: 20.00, Vision: true},
	"claude-sonnet-5-5": {InputPerMillionUSD: 2.00, OutputPerMillionUSD: 10.00, Vision: true},
	"claude-haiku-5-5": {InputPerMillionUSD: 0.10, OutputPerMillionUSD: 0.50, Vision: true,
		Staffel: &PreisStaffel{UeberPromptTokens: 100_000, InputPerMillionUSD: 0.50, OutputPerMillionUSD: 2.50}},
	"claude-fable-5-2": {InputPerMillionUSD: 8.00, OutputPerMillionUSD: 40.00, Vision: true},
	// Vorgänger (nur noch für Bestandsdaten; Preise Stand 2026-05-23).
	"claude-opus-4-7":   {InputPerMillionUSD: 15.00, OutputPerMillionUSD: 75.00, Vision: true},
	"claude-sonnet-4-6": {InputPerMillionUSD: 3.00, OutputPerMillionUSD: 15.00, Vision: true},
	"claude-haiku-4-5":  {InputPerMillionUSD: 0.80, OutputPerMillionUSD: 4.00, Vision: true},
}

// openaiPricing — OpenAI ChatGPT (https://openai.com/pricing).
// o1-mini kann als einziges Modell dieser Liste keine Bilder — der Fall ist
// nicht theoretisch, sondern der Grund, warum die Prüfung modell- und nicht
// anbieterweit ist.
var openaiPricing = ProviderPricing{
	"gpt-4o":      {InputPerMillionUSD: 2.50, OutputPerMillionUSD: 10.00, Vision: true},
	"gpt-4o-mini": {InputPerMillionUSD: 0.15, OutputPerMillionUSD: 0.60, Vision: true},
	"o1":          {InputPerMillionUSD: 15.00, OutputPerMillionUSD: 60.00, Vision: true},
	"o1-mini":     {InputPerMillionUSD: 3.00, OutputPerMillionUSD: 12.00},
}

// googlePricing — Google Gemini (https://ai.google.dev/pricing).
var googlePricing = ProviderPricing{
	"gemini-1.5-pro":   {InputPerMillionUSD: 1.25, OutputPerMillionUSD: 5.00, Vision: true},
	"gemini-1.5-flash": {InputPerMillionUSD: 0.075, OutputPerMillionUSD: 0.30, Vision: true},
	"gemini-2.0-flash": {InputPerMillionUSD: 0.10, OutputPerMillionUSD: 0.40, Vision: true},
}

// mistralPricing — Mistral AI (https://mistral.ai/pricing).
// KEINES dieser drei Modelle kann Bilder; Mistrals Vision-Modell (Pixtral)
// steht nicht in der Liste. Ein Anhang-Send an Mistral scheitert deshalb
// sauber, statt Kosten für eine Anfrage zu erzeugen, die der Anbieter ablehnt.
var mistralPricing = ProviderPricing{
	"mistral-large-latest":  {InputPerMillionUSD: 2.00, OutputPerMillionUSD: 6.00},
	"mistral-medium-latest": {InputPerMillionUSD: 0.40, OutputPerMillionUSD: 2.00},
	"mistral-small-latest":  {InputPerMillionUSD: 0.20, OutputPerMillionUSD: 0.60},
}

// modelSupportsVision beantwortet „darf dieses Modell Bilder sehen?".
// Unbekanntes Modell → false. Das ist die sichere Richtung: Ein Send mit
// Bildern scheitert dann mit einer benennbaren Meldung, statt beim Anbieter
// Kosten zu erzeugen und dort abgelehnt zu werden.
func modelSupportsVision(table ProviderPricing, model string) bool {
	entry, ok := table[model]
	return ok && entry.Vision
}

// SupportsVision ist der Einstieg für Aufrufer ausserhalb dieses Pakets
// (Stream-Handler). Unbekannter Provider oder unbekanntes Modell → false.
func SupportsVision(provider, model string) bool {
	table, ok := AllPricing()[provider]
	if !ok {
		return false
	}
	return modelSupportsVision(table, model)
}

// lookupPricing ist der gemeinsame Lookup für alle Provider.
// Unbekannte Modell-Namen → (0, 0). Die concrete-Provider-Implementierungen
// (anthropic.go, openai.go, ...) delegieren ihre Pricing()-Methode hierhin.
func lookupPricing(table ProviderPricing, model string) (float64, float64) {
	if entry, ok := table[model]; ok {
		return entry.InputPerMillionUSD, entry.OutputPerMillionUSD
	}
	return 0, 0
}

// AllPricing bündelt die vier Tabellen pro Provider-Name für den
// GET /llm/pricing-Endpoint. Stabile, sortierte JSON-Ausgabe wird vom
// Handler übernommen.
func AllPricing() map[string]ProviderPricing {
	openai := openaiPricing
	if OpenAIKompatibel() {
		// K-B1: Bei eigener Basis gibt es keine bekannten Preise und kein
		// bekanntes Bildverständnis — auch nicht für Namen wie `gpt-4o`.
		openai = ProviderPricing{}
	}
	return map[string]ProviderPricing{
		"anthropic": anthropicPricing,
		"openai":    openai,
		"google":    googlePricing,
		"mistral":   mistralPricing,
	}
}
