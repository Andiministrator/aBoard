package api

import "time"

// SetzeLebenszeichenFuerTest verkürzt den Abstand der SSE-Lebenszeichen
// (Karte #665). Rückgabe stellt zurück.
func SetzeLebenszeichenFuerTest(d time.Duration) func() {
	alt := llmStreamLebenszeichen
	llmStreamLebenszeichen = d
	return func() { llmStreamLebenszeichen = alt }
}
