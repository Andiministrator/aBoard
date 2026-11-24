package llm

// history_tools_test.go — Serialisierung der Werkzeug-Runden pro Provider
// (#428, ADR-0120 §5).
//
// Die wichtigste Zusicherung steht ganz oben: Eine History OHNE Werkzeug-
// Felder muss byte-identisch serialisieren wie vor #428. Ohne diesen Test
// wäre jede Änderung an den Message-Structs ein blinder Eingriff in den
// Bestandspfad — und der läuft bei jedem einzelnen Send.

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// Der Bestandsdraht. Die erwarteten Strings sind bewusst ausgeschrieben und
// nicht aus dem Code abgeleitet — ein Erwartungswert, den dieselbe Funktion
// erzeugt, prüft nichts.
func TestBestandsdrahtUnveraendert(t *testing.T) {
	sr := SendRequest{
		System:     "Sys",
		UserPrompt: "Frage",
		History: []HistoryMessage{
			{Role: "user", Content: "alt-user"},
			{Role: "assistant", Content: "alt-assistant"},
		},
	}

	t.Run("anthropic", func(t *testing.T) {
		want := `[{"role":"user","content":"alt-user"},` +
			`{"role":"assistant","content":"alt-assistant"},` +
			`{"role":"user","content":"Frage"}]`
		if got := mustJSON(t, anthropicMessages(sr)); got != want {
			t.Fatalf("Draht geaendert:\n got: %s\nwant: %s", got, want)
		}
	})

	t.Run("openai", func(t *testing.T) {
		want := `[{"role":"system","content":"Sys"},` +
			`{"role":"user","content":"alt-user"},` +
			`{"role":"assistant","content":"alt-assistant"},` +
			`{"role":"user","content":"Frage"}]`
		if got := mustJSON(t, openaiMessages(sr)); got != want {
			t.Fatalf("Draht geaendert:\n got: %s\nwant: %s", got, want)
		}
	})

	t.Run("google", func(t *testing.T) {
		want := `[{"role":"user","parts":[{"text":"alt-user"}]},` +
			`{"role":"model","parts":[{"text":"alt-assistant"}]},` +
			`{"role":"user","parts":[{"text":"Frage"}]}]`
		if got := mustJSON(t, googleContents(sr)); got != want {
			t.Fatalf("Draht geaendert:\n got: %s\nwant: %s", got, want)
		}
	})
}

// werkzeugRunde baut die History, die der Handler nach einem beantworteten
// Abruf aufbaut: User-Prompt, Assistant-Zug mit Aufruf, unsere Antwort.
func werkzeugRunde() SendRequest {
	return SendRequest{
		UserPrompt: "", // ab Runde 2 leer — die tool_results SIND die Anfrage
		History: []HistoryMessage{
			{Role: "user", Content: "Frage"},
			{Role: "assistant", Content: "Ich sehe nach.", ToolUses: []ToolUseBlock{
				{ID: "tu-1", Name: FetchRelatedCardTool, Args: json.RawMessage(`{"cardId":"c2"}`)},
			}},
			{Role: "user", ToolResults: []ToolResultBlock{
				{ID: "tu-1", Name: FetchRelatedCardTool, Content: "# Karte #7: Ziel"},
			}},
		},
	}
}

func TestAnthropicWerkzeugRunde(t *testing.T) {
	got := mustJSON(t, anthropicMessages(werkzeugRunde()))
	want := `[{"role":"user","content":"Frage"},` +
		`{"role":"assistant","content":[{"text":"Ich sehe nach.","type":"text"},` +
		`{"id":"tu-1","input":{"cardId":"c2"},"name":"fetch-related-card","type":"tool_use"}]},` +
		`{"role":"user","content":[{"content":"# Karte #7: Ziel","tool_use_id":"tu-1","type":"tool_result"}]}]`
	if got != want {
		t.Fatalf("Anthropic-Werkzeugrunde:\n got: %s\nwant: %s", got, want)
	}
	// Leerer UserPrompt hängt keine Nachricht an — sonst stünde nach den
	// tool_results eine leere user-Nachricht, und Anthropic lehnt das ab.
	if strings.Count(got, `"role":"user"`) != 2 {
		t.Fatalf("unerwartete Zahl an user-Nachrichten: %s", got)
	}
}

func TestAnthropicFehlerErgebnisIstMarkiert(t *testing.T) {
	sr := SendRequest{History: []HistoryMessage{
		{Role: "user", ToolResults: []ToolResultBlock{
			{ID: "tu-1", Name: FetchRelatedCardTool, Content: "kein Zugriff", IsError: true},
		}},
	}}
	got := mustJSON(t, anthropicMessages(sr))
	if !strings.Contains(got, `"is_error":true`) {
		t.Fatalf("Ablehnung ist nicht als Fehler markiert — das Modell liest sie sonst als Karteninhalt: %s", got)
	}
}

func TestOpenAIWerkzeugRunde(t *testing.T) {
	got := mustJSON(t, openaiMessages(werkzeugRunde()))
	want := `[{"role":"user","content":"Frage"},` +
		`{"role":"assistant","content":"Ich sehe nach.","tool_calls":[` +
		`{"id":"tu-1","type":"function","function":{"name":"fetch-related-card","arguments":"{\"cardId\":\"c2\"}"}}]},` +
		`{"role":"tool","content":"# Karte #7: Ziel","tool_call_id":"tu-1"}]`
	if got != want {
		t.Fatalf("OpenAI-Werkzeugrunde:\n got: %s\nwant: %s", got, want)
	}
}

// Zwei Ergebnisse in einer History-Nachricht werden bei OpenAI zu ZWEI
// Nachrichten — das ist der Formatunterschied zu Anthropic, und wenn er
// verloren geht, lehnt die API die Anfrage ab.
func TestOpenAIMehrereErgebnisseWerdenMehrereNachrichten(t *testing.T) {
	sr := SendRequest{History: []HistoryMessage{
		{Role: "user", ToolResults: []ToolResultBlock{
			{ID: "a", Name: FetchRelatedCardTool, Content: "eins"},
			{ID: "b", Name: FetchRelatedCardTool, Content: "zwei"},
		}},
	}}
	msgs := openaiMessages(sr)
	if len(msgs) != 2 {
		t.Fatalf("erwartet 2 tool-Nachrichten, bekam %d: %s", len(msgs), mustJSON(t, msgs))
	}
	if msgs[0].ToolCallID != "a" || msgs[1].ToolCallID != "b" {
		t.Fatalf("tool_call_id falsch zugeordnet: %s", mustJSON(t, msgs))
	}
}

func TestGoogleWerkzeugRunde(t *testing.T) {
	got := mustJSON(t, googleContents(werkzeugRunde()))
	want := `[{"role":"user","parts":[{"text":"Frage"}]},` +
		`{"role":"model","parts":[{"text":"Ich sehe nach."},` +
		`{"functionCall":{"name":"fetch-related-card","args":{"cardId":"c2"}}}]},` +
		`{"role":"user","parts":[{"functionResponse":{"name":"fetch-related-card","response":{"content":"# Karte #7: Ziel"}}}]}]`
	if got != want {
		t.Fatalf("Google-Werkzeugrunde:\n got: %s\nwant: %s", got, want)
	}
}

func TestGoogleFehlerErgebnisIstMarkiert(t *testing.T) {
	sr := SendRequest{History: []HistoryMessage{
		{Role: "user", ToolResults: []ToolResultBlock{
			{ID: "tu-1", Name: FetchRelatedCardTool, Content: "kein Zugriff", IsError: true},
		}},
	}}
	got := mustJSON(t, googleContents(sr))
	if !strings.Contains(got, `"error":"kein Zugriff"`) {
		t.Fatalf("Ablehnung nicht als error-Schluessel: %s", got)
	}
	if strings.Contains(got, `"content":"kein Zugriff"`) {
		t.Fatalf("Ablehnung zusaetzlich als content — das Modell liest sie als Karteninhalt: %s", got)
	}
}
