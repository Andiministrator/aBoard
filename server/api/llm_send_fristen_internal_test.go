package api

import (
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
)

// TestLLMSend_HeaderDeckelUeberSendFrist — Kritiker Sprint 70, K-A4: Der
// geteilte LLM-Client begrenzt die Wartezeit bis zu den Antwort-Headern.
// Anbieter schicken die Header ohne Streaming erst, wenn die ganze Antwort
// fertig ist. Liegt dieser Deckel unter llmSendTimeout, greift die
// Send-Frist nie; bis Sprint 70 waren es 20 s gegen 2 min.
func TestLLMSend_HeaderDeckelUeberSendFrist(t *testing.T) {
	if llm.AntwortHeaderDeckel <= llmSendTimeout {
		t.Fatalf("AntwortHeaderDeckel %v ≤ llmSendTimeout %v — Send() bricht an den Headern ab, bevor seine Frist greift",
			llm.AntwortHeaderDeckel, llmSendTimeout)
	}
}
