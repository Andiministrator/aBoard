package llm

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// AnthropicProvider — Test + Send gegen die Messages-API
// (https://api.anthropic.com/v1/messages).
//
// TestKey: POST /v1/messages mit max_tokens=1 ist der günstigste
// Authentifizierungs-Probe (~0,0001 USD pro Call). 200 = Key gültig; 401 = ungültig.
// Send: derselbe Endpoint mit dem eigentlichen System+User-Prompt (#299, ADR-0095).
type AnthropicProvider struct {
	Client *http.Client
	UA     string

	// Endpoint erlaubt Test-Stubs zu überschreiben (httptest.URL).
	Endpoint string
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// endpointMessages liefert die /v1/messages-URL für TestKey + Send.
// Test-Stubs überschreiben das via p.Endpoint.
func (p *AnthropicProvider) endpointMessages() string {
	if p.Endpoint != "" {
		return p.Endpoint
	}
	return "https://api.anthropic.com/v1/messages"
}

// applyAnthropicAuth setzt die Header, die beide Pfade (TestKey + Send) brauchen.
func (p *AnthropicProvider) applyAnthropicAuth(req *http.Request, key string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", key)
	req.Header.Set("User-Agent", p.UA)
}

func (p *AnthropicProvider) TestKey(ctx context.Context, key string) error {
	// F-1126: der geteilte Client hat kein Client.Timeout mehr (siehe
	// defaultClient()-Doku in providers.go) — TestKey bekommt seine kurze
	// Gesamtfrist deshalb hier selbst, über den Context.
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	// Minimaler Probe-Body. Modell ist Haiku (günstigstes), max_tokens=1
	// damit der Call so wenig wie möglich kostet (~0,0001 USD).
	body := strings.NewReader(`{"model":"claude-haiku-5-5","max_tokens":1,"messages":[{"role":"user","content":"."}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpointMessages(), body)
	if err != nil {
		return err
	}
	p.applyAnthropicAuth(req, key)

	res, err := p.Client.Do(req)
	if err != nil {
		return wrapNetErr(err)
	}
	defer res.Body.Close()
	return classifyHTTPStatus(res.StatusCode)
}

// anthropicSendBody — POST /v1/messages Request-Body. System ist Top-Level
// (nicht in messages), das ist Anthropic-spezifisch (vs. OpenAI/Mistral).
type anthropicSendBody struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicSendMsg `json:"messages"`
}
type anthropicSendMsg struct {
	Role string `json:"role"`
	// Content ist entweder ein String (Normalfall) oder ein Block-Array
	// (Werkzeug-Runden, ADR-0120 §5). Als String serialisiert es
	// bit-identisch zum Stand vor #428.
	Content any `json:"content"`
}

// anthropicMessages baut die messages[]-Sequenz: History älteste zuerst,
// dann der aktuelle User-Turn ans Ende. Anthropic akzeptiert sowohl
// "user"- als auch "assistant"-Rollen in messages — system ist separat.
//
// Leerer UserPrompt hängt KEINE Nachricht an. Das ist der Fall der
// Werkzeug-Runden (ADR-0120 §5): dort trägt die letzte History-Nachricht die
// tool_results und ist damit die eigentliche Anfrage. Vorher war ein leerer
// UserPrompt ohnehin kein sinnvoller Aufruf — Anthropic lehnt leere
// Nachrichten ab.
func anthropicMessages(sr SendRequest) []anthropicSendMsg {
	out := make([]anthropicSendMsg, 0, len(sr.History)+1)
	for _, h := range sr.History {
		out = append(out, anthropicHistoryMsg(h))
	}
	if sr.UserPrompt != "" || len(sr.Images) > 0 {
		out = append(out, anthropicHistoryMsg(HistoryMessage{
			Role: "user", Content: sr.UserPrompt, Images: sr.Images,
		}))
	}
	return out
}

// anthropicHistoryMsg übersetzt eine History-Nachricht. Ohne Werkzeug-Blöcke
// bleibt Content ein String — derselbe Draht wie vor #428.
func anthropicHistoryMsg(h HistoryMessage) anthropicSendMsg {
	if len(h.ToolUses) == 0 && len(h.ToolResults) == 0 && len(h.Images) == 0 {
		return anthropicSendMsg{Role: h.Role, Content: h.Content}
	}
	blocks := make([]map[string]any, 0, 1+len(h.ToolUses)+len(h.ToolResults)+len(h.Images))
	// Bilder VOR dem Text: Anthropic empfiehlt das ausdrücklich — das Modell
	// soll erst sehen, worüber der Text spricht.
	for _, img := range h.Images {
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MIME,
				"data":       base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	// tool_result-Blöcke müssen bei Anthropic am ANFANG der user-Nachricht
	// stehen; Text danach. Für die assistant-Seite ist die Reihenfolge
	// Text → tool_use.
	for _, tr := range h.ToolResults {
		b := map[string]any{
			"type":        "tool_result",
			"tool_use_id": tr.ID,
			"content":     tr.Content,
		}
		if tr.IsError {
			b["is_error"] = true
		}
		blocks = append(blocks, b)
	}
	if strings.TrimSpace(h.Content) != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": h.Content})
	}
	for _, tu := range h.ToolUses {
		input := json.RawMessage("{}")
		if len(tu.Args) > 0 {
			input = tu.Args
		}
		blocks = append(blocks, map[string]any{
			"type":  "tool_use",
			"id":    tu.ID,
			"name":  tu.Name,
			"input": input,
		})
	}
	return anthropicSendMsg{Role: h.Role, Content: blocks}
}

// anthropicSendResponse — relevante Felder der /v1/messages-Antwort.
// "content" ist ein Array von Blöcken — wir extrahieren das erste Text-Element
// (im MVP kein Tool-Use/Multi-Block; Anthropic gibt für klassische Send-Calls
// einen einzigen text-Block zurück).
type anthropicSendResponse struct {
	StopReason string `json:"stop_reason"`
	Model      string `json:"model"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (p *AnthropicProvider) Send(ctx context.Context, key string, sr SendRequest) (SendResult, error) {
	maxTok := sr.MaxTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	payload := anthropicSendBody{
		Model:     sr.Model,
		System:    sr.System,
		MaxTokens: maxTok,
		Messages:  anthropicMessages(sr),
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return SendResult{}, fmt.Errorf("anthropic: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpointMessages(), strings.NewReader(string(buf)))
	if err != nil {
		return SendResult{}, err
	}
	p.applyAnthropicAuth(req, key)

	res, err := p.Client.Do(req)
	if err != nil {
		return SendResult{}, wrapNetErr(err)
	}
	defer res.Body.Close()
	if err := classifyHTTPStatus(res.StatusCode); err != nil {
		return SendResult{}, err
	}
	var parsed anthropicSendResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return SendResult{}, fmt.Errorf("anthropic: parse response: %w", err)
	}
	var text strings.Builder
	for _, block := range parsed.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return SendResult{
		Text:         text.String(),
		InputTokens:  parsed.Usage.InputTokens,
		OutputTokens: parsed.Usage.OutputTokens,
		Model:        parsed.Model,
		FinishReason: parsed.StopReason,
	}, nil
}

func (p *AnthropicProvider) Pricing(model string) (float64, float64) {
	return lookupPricing(anthropicPricing, model)
}

// PricingBei berücksichtigt die Staffelpreise nach Prompt-Länge (F-3102).
func (p *AnthropicProvider) PricingBei(model string, promptTokens int) (float64, float64) {
	return lookupPricingBei(anthropicPricing, model, promptTokens)
}

// anthropicStreamBody — POST /v1/messages mit "stream": true (#300, ADR-0096).
// Felder identisch zu anthropicSendBody plus Stream-Flag. Tools werden mit
// EnableMutations=true mitgesendet (Etappe 4, ADR-0096 §3).
type anthropicStreamBody struct {
	Model     string              `json:"model"`
	System    string              `json:"system,omitempty"`
	MaxTokens int                 `json:"max_tokens"`
	Messages  []anthropicSendMsg  `json:"messages"`
	Stream    bool                `json:"stream"`
	Tools     []AnthropicToolSpec `json:"tools,omitempty"`
}

// SendStream — Anthropic Messages-Streaming (ADR-0096 §1, Etappe 1).
//
// Stream-Event-Schema (relevant für uns):
//   - message_start:        usage.input_tokens (Output noch 0)
//   - content_block_delta:  delta.text → Token-Chunk (für Etappe 1 nur Text)
//   - message_delta:        delta.stop_reason + usage.output_tokens (final)
//   - message_stop:         Stream-Ende
//   - ping:                 Keep-Alive, ignorieren
//   - error:                Provider-Fehler im Stream → ErrProviderUnreachable
//
// Tool-Use-Blöcke (content_block_start mit type="tool_use" + input_json_delta)
// werden in Etappe 4 ergänzt; Etappe 1 ignoriert sie defensiv (kein Crash,
// kein Token an Client).
func (p *AnthropicProvider) SendStream(ctx context.Context, key string, sr SendRequest, onChunk func(StreamChunk) error) (SendResult, error) {
	maxTok := sr.MaxTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	payload := anthropicStreamBody{
		Model:     sr.Model,
		System:    sr.System,
		MaxTokens: maxTok,
		Messages:  anthropicMessages(sr),
		Stream:    true,
	}
	if tools := SelectTools(sr.EnableMutations, sr.EnableFetch); len(tools) > 0 {
		payload.Tools = BuildAnthropicTools(tools)
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return SendResult{}, fmt.Errorf("anthropic: marshal stream request: %w", err)
	}
	// F-1126 Nachtrag: der Client hat kein Gesamt-Timeout mehr — readCtx
	// bricht stattdessen ab, wenn touch() (siehe Scan-Schleife unten) länger
	// als streamIdleTimeout nicht aufgerufen wird (tote, aber offene
	// Verbindung). cancelGuard räumt die Watchdog-Goroutine in jedem Fall auf.
	// F-1140: bis zum ersten touch() (= erste SSE-Zeile) gilt die längere
	// streamErstbyteTimeout statt streamIdleTimeout — eine lange Denkphase
	// vor dem ersten Token ist keine tote Verbindung.
	readCtx, touch, cancelGuard := streamIdleGuard(ctx, streamErstbyteTimeout, streamIdleTimeout)
	defer cancelGuard()
	req, err := http.NewRequestWithContext(readCtx, http.MethodPost, p.endpointMessages(), strings.NewReader(string(buf)))
	if err != nil {
		return SendResult{}, err
	}
	p.applyAnthropicAuth(req, key)
	req.Header.Set("Accept", "text/event-stream")

	res, err := p.Client.Do(req)
	if err != nil {
		return SendResult{}, wrapNetErr(err)
	}
	defer res.Body.Close()
	if err := classifyHTTPStatus(res.StatusCode); err != nil {
		return SendResult{}, err
	}

	result := SendResult{Model: sr.Model}
	var (
		fullText strings.Builder
	)

	// SSE-Parser: bufio.Scanner mit ResizableBuffer, weil ein einzelnes
	// delta.text-Payload nicht > 1 MB werden sollte, aber wir geben dem
	// Scanner 1 MB Spielraum (Default ist 64 KB → zu knapp für lange Antworten).
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		curEvent string
		curData  strings.Builder
	)

	// Tool-Use-Block-Akkumulator (Etappe 4, ADR-0096 §3). Anthropic streamt
	// einen tool_use als content_block_start → mehrere input_json_delta →
	// content_block_stop, alle mit demselben `index`. Wir halten pro Index
	// einen Eintrag und emittieren bei content_block_stop.
	type pendingTool struct {
		id, name string
		args     strings.Builder // partial_json chunks
	}
	toolBlocks := map[int]*pendingTool{}

	dispatch := func() error {
		if curEvent == "" && curData.Len() == 0 {
			return nil
		}
		data := curData.String()
		curData.Reset()
		// Wir routen alles über den Body-Parser; curEvent ist eine Hilfe,
		// aber Anthropic schickt den Typ auch im JSON-`type`-Feld.
		switch curEvent {
		case "", "ping", "message_stop":
			// Keep-Alive / Boundary-Events ohne Payload-Verwertung.
			return nil
		case "error":
			// Strukturierter Provider-Fehler — Stream brach beim Anbieter ab.
			return fmt.Errorf("%w: anthropic stream error: %s", ErrProviderUnreachable, data)
		case "message_start":
			var ms struct {
				Message struct {
					Usage struct {
						InputTokens int `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(data), &ms); err == nil {
				result.InputTokens = ms.Message.Usage.InputTokens
			}
			return nil
		case "content_block_start":
			var cbs struct {
				Index        int `json:"index"`
				ContentBlock struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"content_block"`
			}
			if err := json.Unmarshal([]byte(data), &cbs); err != nil {
				return nil
			}
			if cbs.ContentBlock.Type == "tool_use" {
				toolBlocks[cbs.Index] = &pendingTool{
					id:   cbs.ContentBlock.ID,
					name: cbs.ContentBlock.Name,
				}
			}
			return nil
		case "content_block_stop":
			var cbs struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal([]byte(data), &cbs); err != nil {
				return nil
			}
			if pt, ok := toolBlocks[cbs.Index]; ok {
				delete(toolBlocks, cbs.Index)
				argsRaw := pt.args.String()
				if argsRaw == "" {
					argsRaw = "{}"
				}
				if err := onChunk(StreamChunk{ToolCall: &ToolCallChunk{
					ID:   pt.id,
					Name: pt.name,
					Args: json.RawMessage(argsRaw),
				}}); err != nil {
					return err
				}
			}
			return nil
		case "content_block_delta":
			var d struct {
				Index int `json:"index"`
				Delta struct {
					Type        string `json:"type"`
					Text        string `json:"text"`
					PartialJSON string `json:"partial_json"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &d); err != nil {
				return nil // tolerant: unbekanntes Delta nicht crashen
			}
			if d.Delta.Type == "text_delta" && d.Delta.Text != "" {
				fullText.WriteString(d.Delta.Text)
				if err := onChunk(StreamChunk{Text: d.Delta.Text}); err != nil {
					return err // Client-Cancel oder Body-Cap → Stream abbrechen
				}
			}
			if d.Delta.Type == "input_json_delta" {
				if pt, ok := toolBlocks[d.Index]; ok {
					pt.args.WriteString(d.Delta.PartialJSON)
				}
			}
			return nil
		case "message_delta":
			var md struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &md); err == nil {
				if md.Delta.StopReason != "" {
					result.FinishReason = md.Delta.StopReason
				}
				result.OutputTokens = md.Usage.OutputTokens
			}
			return nil
		}
		return nil
	}

	for scanner.Scan() {
		// Jede empfangene Zeile zählt als Lebenszeichen — hält den
		// Idle-Guard zurück (F-1126 Nachtrag).
		touch()
		// Context-Cancel zwischen Events → schnell raus. Prüft den ÄUSSEREN
		// ctx (Client-Abbruch), nicht readCtx — readCtx kann zusätzlich vom
		// Idle-Guard abgebrochen worden sein; beide Fälle landen ohnehin
		// über scanner.Err() im wrapNetErr-Pfad unten, diese Prüfung ist nur
		// der schnelle Ausstieg für den expliziten Client-Cancel-Fall.
		if err := ctx.Err(); err != nil {
			return result, wrapNetErr(err)
		}
		line := scanner.Text()
		if line == "" {
			// Event-Boundary → dispatch.
			if err := dispatch(); err != nil {
				return result, err
			}
			curEvent = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			curEvent = strings.TrimSpace(line[len("event:"):])
			continue
		}
		if strings.HasPrefix(line, "data:") {
			d := strings.TrimSpace(line[len("data:"):])
			if curData.Len() > 0 {
				curData.WriteByte('\n')
			}
			curData.WriteString(d)
			continue
		}
		// Unbekannte Zeilen (z. B. ":"-Kommentare oder "id:") ignorieren.
	}
	// Letztes Event ohne Trailing-Leerzeile (kommt bei manchen Implementierungen).
	if err := dispatch(); err != nil {
		return result, err
	}
	if err := scanner.Err(); err != nil {
		return result, wrapNetErr(err)
	}

	result.Text = fullText.String()
	return result, nil
}
