package llm

// context_limits.go — Provider-/Modell-spezifische Kontext-Grenzen für das
// Multi-Turn-Budget-Gate (#300, ADR-0096 §2 Etappe 2).
//
// Die Werte sind konservativ; sie spiegeln die offiziellen Provider-Angaben
// zum maximalen Eingabe-Kontext (Tokens). Das Gate prüft gegen 80 % davon —
// Hard-Cap ist Provider-Sache, wir wollen Token-Spirale früh stoppen, bevor
// Provider-Fehler entsteht.
//
// Unbekanntes Modell → 0 = „kein Gate". Das verhindert, dass neue Modelle
// (frisch released, noch nicht hier eingetragen) versehentlich gebrickt
// werden — Provider antwortet im Zweifel mit eigenem Fehler.

import "strings"

// ContextLimit liefert das maximale Input-Token-Limit für (provider, model).
// 0 = unbekannt → kein Gate.
//
// Quelle:
//   - Anthropic claude-3.x/4.x: 200000 (https://docs.anthropic.com/en/docs/models)
//   - OpenAI gpt-4o:            128000
//   - OpenAI o1/o3:             200000
//   - Google gemini-1.5/2.x:    1000000
//   - Mistral large/medium:     32000
func ContextLimit(provider, model string) int {
	m := strings.ToLower(model)
	switch strings.ToLower(provider) {
	case "anthropic":
		if strings.HasPrefix(m, "claude-") {
			return 200000
		}
	case "openai":
		if OpenAIKompatibel() {
			// K-B1: eigenes Gegenüber, Grenze unbekannt → kein Gate —
			// es sei denn, der Betreiber nennt sie (Karte #665,
			// ABOARD_LLM_OPENAI_KONTEXT); dann gilt sie für jedes Modell.
			return int(openAIKontext.Load())
		}
		if strings.HasPrefix(m, "gpt-4o") || strings.HasPrefix(m, "gpt-4-turbo") {
			return 128000
		}
		if strings.HasPrefix(m, "gpt-4") {
			return 8192
		}
		if strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") {
			return 200000
		}
	case "google":
		if strings.HasPrefix(m, "gemini-1.5") || strings.HasPrefix(m, "gemini-2") {
			return 1000000
		}
		if strings.HasPrefix(m, "gemini-") {
			return 32768
		}
	case "mistral":
		if strings.Contains(m, "large") {
			return 32000
		}
		if strings.Contains(m, "medium") || strings.Contains(m, "small") {
			return 32000
		}
	}
	return 0
}

// EstimateTokens — grobe heuristische Token-Schätzung für einen Text-Blob.
// 4 Bytes/Token ist die übliche Faustregel für deutsche/englische Plain-Text-
// Prompts; Code/JSON/Asiatische Sprachen weichen ab. Für das Budget-Gate
// (frühe Warnung statt harte Genauigkeit) reicht das.
func EstimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// EstimatedImageTokensPerImage — grobe, konservative Pauschale je gesendetem
// Bild fürs Kontext-Budget (F-1089, Nebenbefund zu #429/ADR-0121). Beide
// Budget-Gates (Vor-dem-Turn und das Loop-Gate aus F-872) zählten bisher nur
// Text — ein mitgeschicktes Bild ging in keine der beiden Rechnungen ein.
//
// Anthropic rechnet Bild-Kosten in "visual tokens" zu je 28×28-Pixel-Patches
// (⌈Breite/28⌉ × ⌈Höhe/28⌉) und deckelt das Ergebnis je nach Modell-Tier:
// Standard-Modelle bei 1568 visual tokens (lange Kante ≤ 1568 px), Modelle ab
// Claude 4.7 bei 4784 (lange Kante ≤ 2576 px). Quelle:
// https://platform.claude.com/docs/en/build-with-claude/vision
// (Abschnitt "Resolution and token cost", geprüft 2026-09-28).
//
// aBoards Bild-Grenzen sind bewusst anbieter-/modellübergreifend einheitlich
// (ADR-0121 §3: "eine gemeinsame Grenze ist hier besser als vier") — dieselbe
// Überlegung gilt für die Schätzung. Der Server dekodiert die Bilddatei hier
// nicht (kein Zugriff auf die tatsächlichen Pixelmasse ohne zusätzlichen
// Aufwand je Format), ein Bild kann also irgendwo zwischen ein paar Dutzend
// und 4784 Tokens kosten. Die konservative, einfache Wahl ist der
// dokumentierte Höchstwert der grösseren Stufe: Für ein Gate, das ein
// gerissenes Kontext-Limit verhindern soll, ist ein zu früher Abbruch der
// harmlosere Fehler als ein zu später.
const EstimatedImageTokensPerImage = 4784

// EstimateImageTokens — Bild-Anteil der Budget-Schätzung (F-1089). anzahl ist
// die Zahl der mit dem Turn gesendeten Bilder.
func EstimateImageTokens(anzahl int) int {
	if anzahl <= 0 {
		return 0
	}
	return anzahl * EstimatedImageTokensPerImage
}
