package llm

import (
	"encoding/json"
	"testing"
)

// Karte #1174: aktuelle Claude-Modelle und Preise (platform.claude.com, 2026-11-20).
func TestAnthropicPricing_AktuelleModelle(t *testing.T) {
	cases := []struct {
		model   string
		in, out float64
	}{
		{"claude-opus-5-5", 4, 20},
		{"claude-sonnet-5-5", 2, 10},
		{"claude-haiku-5-5", 0.10, 0.50},
		{"claude-fable-5-2", 8, 40},
		{"claude-opus-4-7", 15, 75}, // Bestandsdaten bleiben bepreist
	}
	for _, c := range cases {
		in, out := lookupPricing(anthropicPricing, c.model)
		if in != c.in || out != c.out {
			t.Errorf("%s: %v/%v, erwartet %v/%v", c.model, in, out, c.in, c.out)
		}
		if !SupportsVision("anthropic", c.model) {
			t.Errorf("%s: Vision erwartet", c.model)
		}
	}
}

// F-3102 (Karte #1176): Haiku 5.5 hat Staffelpreise nach Prompt-Länge.
// „Über 100K" ist strikt grösser: genau 100.000 Tokens kosten noch den Grundsatz.
func TestAnthropicPricing_HaikuStaffel(t *testing.T) {
	cases := []struct {
		tokens  int
		in, out float64
	}{
		{0, 0.10, 0.50},
		{99_999, 0.10, 0.50},
		{100_000, 0.10, 0.50}, // Grenzfall: nicht „über"
		{100_001, 0.50, 2.50},
		{500_000, 0.50, 2.50},
	}
	for _, c := range cases {
		in, out := lookupPricingBei(anthropicPricing, "claude-haiku-5-5", c.tokens)
		if in != c.in || out != c.out {
			t.Errorf("%d Tokens: %v/%v, erwartet %v/%v", c.tokens, in, out, c.in, c.out)
		}
	}
	// Modell ohne Staffel bleibt einsätzig, unbekanntes Modell (0, 0).
	if in, out := lookupPricingBei(anthropicPricing, "claude-sonnet-5-5", 900_000); in != 2 || out != 10 {
		t.Errorf("Sonnet über 100K: %v/%v", in, out)
	}
	if in, out := lookupPricingBei(anthropicPricing, "gibt-es-nicht", 900_000); in != 0 || out != 0 {
		t.Errorf("unbekannt: %v/%v", in, out)
	}
	// Einstieg für die Kostenrechnung über den Provider.
	var p Provider = &AnthropicProvider{}
	if in, _ := PricingBei(p, "claude-haiku-5-5", 200_000); in != 0.50 {
		t.Errorf("PricingBei über Provider: %v", in)
	}
}

func TestAnthropicPricing_StaffelImJSON(t *testing.T) {
	b, err := json.Marshal(AllPricing()["anthropic"])
	if err != nil {
		t.Fatal(err)
	}
	var tab map[string]map[string]any
	if err := json.Unmarshal(b, &tab); err != nil {
		t.Fatal(err)
	}
	st, ok := tab["claude-haiku-5-5"]["Staffel"].(map[string]any)
	if !ok || st["UeberPromptTokens"] != 100000.0 || st["InputPerMillionUSD"] != 0.5 || st["OutputPerMillionUSD"] != 2.5 {
		t.Errorf("Staffel im JSON: %+v", tab["claude-haiku-5-5"])
	}
	if _, da := tab["claude-sonnet-5-5"]["Staffel"]; da {
		t.Error("Sonnet darf kein Staffel-Feld tragen")
	}
}
