package llm_test

import (
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
)

// K-B3 (Session-Kritiker Sprint 72): Die Fehlermeldung landet über
// log.Fatalf im Log — Zugangsdaten, Query und Fragment dürfen darin nicht
// auftauchen.
func TestValidateOpenAIBaseURL_SchwaerztGeheimnisse(t *testing.T) {
	for _, raw := range []string{
		"http://user:geheim@llm.example:8000/v1", // abzug:testwert
		"http://llm.example:8000/v1?api_key=geheim",
		"http://llm.example:8000/v1#geheim",
		"ftp://user:geheim@llm.example/v1", // abzug:testwert
	} {
		_, err := llm.ValidateOpenAIBaseURL(raw)
		if err == nil {
			t.Errorf("%s: erwartet Fehler", raw)
			continue
		}
		if strings.Contains(err.Error(), "geheim") {
			t.Errorf("Fehlermeldung enthält das Geheimnis: %v", err)
		}
	}
}

// K-B6: kaputte Basen, die vorher durchgingen.
func TestValidateOpenAIBaseURL_Streng(t *testing.T) {
	for _, raw := range []string{
		"http://llm.example:/v1",      // leerer Port
		"http://llm.example:99999/v1", // Port ausserhalb
		"http://llm.example:0/v1",     // Port 0
		"http://llm example:8000/v1",  // geschütztes Leerzeichen
		"http://llm.example:8000/v1/../../admin",
		"http://llm.example:8000/./v1",
	} {
		if _, err := llm.ValidateOpenAIBaseURL(raw); err == nil {
			t.Errorf("%q: erwartet Fehler", raw)
		}
	}
	for _, raw := range []string{"http://llm.example:8000/v1", "https://[::1]:8443/v1/", "http://llm.example/v1"} {
		if _, err := llm.ValidateOpenAIBaseURL(raw); err != nil {
			t.Errorf("%q: unerwarteter Fehler %v", raw, err)
		}
	}
}

func TestOpenAIBasisOhnePfad(t *testing.T) {
	if !llm.OpenAIBasisOhnePfad("http://llm.example:8000") || !llm.OpenAIBasisOhnePfad("http://llm.example:8000/") {
		t.Error("ohne Pfad nicht erkannt")
	}
	if llm.OpenAIBasisOhnePfad("http://llm.example:8000/v1") {
		t.Error("/v1 fälschlich als leer erkannt")
	}
}

func TestParseOpenAIModelle(t *testing.T) {
	m, err := llm.ParseOpenAIModelle(" llama-3.1-70b , ,qwen2.5 ")
	if err != nil || len(m) != 2 || m[0] != "llama-3.1-70b" || m[1] != "qwen2.5" {
		t.Fatalf("m=%v err=%v", m, err)
	}
	if m, err := llm.ParseOpenAIModelle(""); err != nil || len(m) != 0 {
		t.Fatalf("leer: m=%v err=%v", m, err)
	}
	if _, err := llm.ParseOpenAIModelle("zwei worte"); err == nil {
		t.Error("Leerraum im Namen muss abgelehnt werden")
	}
}

// K-B1: Bei eigener Basis gelten OpenAI-Preise, -Kontextgrenzen und
// -Bildverständnis nicht — auch nicht für einen Modellnamen wie gpt-4o.
func TestOpenAIKompatibel_KeineOpenAITabellen(t *testing.T) {
	defer llm.SetzeOpenAIKompatibel(llm.SetzeOpenAIKompatibel(false))

	if llm.ContextLimit("openai", "gpt-4o") == 0 || !llm.SupportsVision("openai", "gpt-4o") {
		t.Fatal("Vorbedingung: ohne Umstellung kennt der Server gpt-4o")
	}
	llm.SetzeOpenAIKompatibel(true)
	if n := llm.ContextLimit("openai", "gpt-4o"); n != 0 {
		t.Errorf("ContextLimit = %d, erwartet 0 (unbekannt)", n)
	}
	if llm.SupportsVision("openai", "gpt-4o") {
		t.Error("SupportsVision muss false sein")
	}
	if len(llm.AllPricing()["openai"]) != 0 {
		t.Error("AllPricing liefert für openai noch OpenAI-Preise")
	}
	if in, out := (&llm.OpenAIProvider{BaseURL: "http://x/v1"}).Pricing("gpt-4o"); in != 0 || out != 0 {
		t.Errorf("Pricing = %v/%v, erwartet 0/0", in, out)
	}
	if llm.ContextLimit("anthropic", "claude-opus-4-7") == 0 {
		t.Error("andere Anbieter dürfen nicht betroffen sein")
	}
}

func TestRegisterDefaults_SetztKompatibel(t *testing.T) {
	defer llm.SetzeOpenAIKompatibel(llm.SetzeOpenAIKompatibel(false))
	llm.RegisterDefaults(llm.NewRegistry(), "t", "http://llm.example:8000/v1")
	if !llm.OpenAIKompatibel() {
		t.Error("mit Basis nicht gesetzt")
	}
	llm.RegisterDefaults(llm.NewRegistry(), "t", "")
	if llm.OpenAIKompatibel() {
		t.Error("ohne Basis nicht zurückgesetzt")
	}
}
