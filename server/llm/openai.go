package llm

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// defaultOpenAIBaseURL ist die Basis, wenn weder BaseURL noch die
// Test-Overrides (Endpoint/SendEndpoint) gesetzt sind.
const defaultOpenAIBaseURL = "https://api.openai.com/v1"

// OpenAIProvider — Test gegen /v1/models (GET), Send gegen /v1/chat/completions
// (POST). Bearer-Token-Schema.
type OpenAIProvider struct {
	Client *http.Client
	UA     string

	// BaseURL (Karte #570, ADR-0129 §5) macht den Anbieter auf ein
	// OpenAI-kompatibles Gegenüber umstellbar, z. B. ein lokales vLLM ohne
	// Internet-Zugang. Leer = defaultOpenAIBaseURL. Form: Basis INKLUSIVE
	// Versionspfad (z. B. "http://llm.example:8000/v1") — die Endpunkte
	// hängen "/models" bzw. "/chat/completions" an. Ein abschliessender
	// Slash wird toleriert (ValidateOpenAIBaseURL entfernt ihn bereits, aber
	// wer BaseURL direkt setzt — z. B. ein Test — muss sich nicht daran
	// halten).
	BaseURL string
	// Endpoint überschreibt /v1/models (TestKey) VOLLSTÄNDIG — hat Vorrang
	// vor BaseURL. Test-Stubs nutzen das.
	Endpoint string
	// SendEndpoint überschreibt /v1/chat/completions (Send) VOLLSTÄNDIG —
	// hat Vorrang vor BaseURL. Tests setzen das separat, damit der
	// TestKey-Stub nicht versehentlich auch Send sieht.
	SendEndpoint string
}

func (p *OpenAIProvider) Name() string { return "openai" }

// baseURL liefert die wirksame Basis ohne abschliessenden Slash.
func (p *OpenAIProvider) baseURL() string {
	if p.BaseURL != "" {
		return strings.TrimSuffix(p.BaseURL, "/")
	}
	return defaultOpenAIBaseURL
}

func (p *OpenAIProvider) endpointModels() string {
	if p.Endpoint != "" {
		return p.Endpoint
	}
	return p.baseURL() + "/models"
}

func (p *OpenAIProvider) endpointChat() string {
	if p.SendEndpoint != "" {
		return p.SendEndpoint
	}
	return p.baseURL() + "/chat/completions"
}

func (p *OpenAIProvider) TestKey(ctx context.Context, key string) error {
	// F-1126: der geteilte Client hat kein Client.Timeout mehr (siehe
	// defaultClient()-Doku in providers.go) — TestKey bekommt seine kurze
	// Gesamtfrist deshalb hier selbst, über den Context.
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpointModels(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", p.UA)

	res, err := p.Client.Do(req)
	if err != nil {
		return wrapNetErr(err)
	}
	defer res.Body.Close()
	return classifyHTTPStatus(res.StatusCode)
}

// openaiSendBody — POST /v1/chat/completions Request-Body. System wird als
// erste Message mit role=system geliefert (anders als bei Anthropic).
// Mistral nutzt denselben Schema — dort wiederverwendet via separater Datei.
type openaiSendBody struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens,omitempty"`
	Messages  []openaiSendMsg `json:"messages"`
	// User (Karte #664) — nur im Firmenmodus und nur beim Anbieter „openai“
	// gesetzt; omitempty hält den Body sonst bit-identisch.
	User string `json:"user,omitempty"`
}
type openaiSendMsg struct {
	Role string `json:"role"`
	// Content ist entweder ein String (Normalfall) oder ein Block-Array
	// (Nachricht mit Bildern, ADR-0121). Als String serialisiert es
	// bit-identisch zum Stand vor #429.
	Content any `json:"content"`
	// Werkzeug-Runden (ADR-0120 §5). Beide Felder sind omitempty — eine
	// Nachricht ohne Werkzeuge serialisiert bit-identisch zum Stand vor #428.
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// openaiToolCall — ein Werkzeug-Aufruf in einer assistant-Nachricht, so wie
// ihn OpenAI/Mistral zurückgespiegelt haben wollen (arguments ist ein
// JSON-STRING, nicht ein Objekt — das ist keine Nachlässigkeit, sondern das
// Schema der API).
type openaiToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openaiToolCallFunc `json:"function"`
}
type openaiToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// openaiMessages baut die messages[]-Sequenz: optionales system zuerst,
// dann History (älteste zuerst), dann der aktuelle User-Turn.
//
// Werkzeug-Ergebnisse werden bei OpenAI/Mistral NICHT als Blöcke in eine
// user-Nachricht gepackt (das ist die Anthropic-Form), sondern als je eigene
// Nachricht mit role="tool" und tool_call_id. Eine History-Nachricht mit N
// Ergebnissen erzeugt hier also N Nachrichten.
//
// Leerer UserPrompt hängt keine Nachricht an — siehe anthropicMessages.
func openaiMessages(sr SendRequest) []openaiSendMsg {
	out := make([]openaiSendMsg, 0, len(sr.History)+2)
	if sr.System != "" {
		out = append(out, openaiSendMsg{Role: "system", Content: sr.System})
	}
	for _, h := range sr.History {
		if len(h.ToolResults) > 0 {
			for _, tr := range h.ToolResults {
				out = append(out, openaiSendMsg{
					Role:       "tool",
					Content:    tr.Content,
					ToolCallID: tr.ID,
				})
			}
			continue
		}
		if h.Role == "user" && len(h.Images) > 0 {
			out = append(out, openaiUserMsg(h.Content, h.Images))
			continue
		}
		msg := openaiSendMsg{Role: h.Role, Content: h.Content}
		for _, tu := range h.ToolUses {
			args := "{}"
			if len(tu.Args) > 0 {
				args = string(tu.Args)
			}
			msg.ToolCalls = append(msg.ToolCalls, openaiToolCall{
				ID:       tu.ID,
				Type:     "function",
				Function: openaiToolCallFunc{Name: tu.Name, Arguments: args},
			})
		}
		out = append(out, msg)
	}
	if sr.UserPrompt != "" || len(sr.Images) > 0 {
		out = append(out, openaiUserMsg(sr.UserPrompt, sr.Images))
	}
	return out
}

// openaiUserMsg baut den User-Turn. Ohne Bilder bleibt Content ein String —
// derselbe Draht wie vor #429.
//
// Anders als Anthropic verlangt OpenAI eine vollständige data:-URL statt
// getrennter media_type/data-Felder; die Kodierung passiert deshalb hier und
// nicht im Aufrufer.
func openaiUserMsg(prompt string, images []ImageAttachment) openaiSendMsg {
	if len(images) == 0 {
		return openaiSendMsg{Role: "user", Content: prompt}
	}
	blocks := make([]map[string]any, 0, 1+len(images))
	if prompt != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": prompt})
	}
	for _, img := range images {
		blocks = append(blocks, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	return openaiSendMsg{Role: "user", Content: blocks}
}

// openaiSendResponse — relevante Felder der /v1/chat/completions-Antwort.
type openaiSendResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// openaiUserFeld liefert den Wert fürs OpenAI-`user`-Feld (Karte #664,
// ADR-0133 §2): nur für den Anbieter „openai“ — Mistral teilt sich diesen
// Body-Aufbau, bekommt die E-Mail aber nie, auch wenn ein Aufrufer sie setzt.
func openaiUserFeld(sr SendRequest, providerName string) string {
	if providerName != "openai" {
		return ""
	}
	return sr.User
}

// sendOpenAICompatible bedient OpenAI- und Mistral-Schemas (identisch).
// providerName landet in Fehler-Wrappern als Diagnose-Hilfe.
func sendOpenAICompatible(ctx context.Context, client *http.Client, ua, url, key string, sr SendRequest, providerName string) (SendResult, error) {
	messages := openaiMessages(sr)
	maxTok := sr.MaxTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	payload := openaiSendBody{Model: sr.Model, MaxTokens: maxTok, Messages: messages, User: openaiUserFeld(sr, providerName)}
	buf, err := json.Marshal(payload)
	if err != nil {
		return SendResult{}, fmt.Errorf("%s: marshal request: %w", providerName, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(buf)))
	if err != nil {
		return SendResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", ua)

	res, err := client.Do(req)
	if err != nil {
		return SendResult{}, wrapNetErr(err)
	}
	defer res.Body.Close()
	if err := classifyHTTPResponse(res, key); err != nil {
		return SendResult{}, err
	}
	var parsed openaiSendResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return SendResult{}, fmt.Errorf("%s: parse response: %w", providerName, err)
	}
	text := ""
	finish := "stop"
	if len(parsed.Choices) > 0 {
		text = parsed.Choices[0].Message.Content
		if parsed.Choices[0].FinishReason != "" {
			finish = parsed.Choices[0].FinishReason
		}
	}
	return SendResult{
		Text:         text,
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
		Model:        parsed.Model,
		FinishReason: finish,
	}, nil
}

func (p *OpenAIProvider) Send(ctx context.Context, key string, sr SendRequest) (SendResult, error) {
	return sendOpenAICompatible(ctx, p.Client, p.UA, p.endpointChat(), key, sr, "openai")
}

// SendStream — OpenAI Chat-Completions-Streaming (ADR-0096 §1, Etappe 2).
// Mistral nutzt denselben Body + denselben Parser (streamOpenAICompatible).
func (p *OpenAIProvider) SendStream(ctx context.Context, key string, sr SendRequest, onChunk func(StreamChunk) error) (SendResult, error) {
	return streamOpenAICompatible(ctx, p.Client, p.UA, p.endpointChat(), key, sr, "openai", onChunk)
}

func (p *OpenAIProvider) Pricing(model string) (float64, float64) {
	if p.BaseURL != "" {
		return 0, 0 // K-B1: eigenes Gegenüber, OpenAI-Preise gelten nicht
	}
	return lookupPricing(openaiPricing, model)
}

// openaiStreamBody — POST /v1/chat/completions mit "stream": true.
// "stream_options.include_usage": true sorgt dafür, dass der finale Chunk
// vor [DONE] ein "usage"-Objekt enthält — ohne das wären Token-Counts in der
// Streaming-Variante nicht verfügbar (gilt für OpenAI + Mistral).
//
// Etappe 4 (ADR-0096 §3): Tools mit EnableMutations=true. tool_choice="auto"
// = das LLM entscheidet, ob/wann ein Tool genutzt wird (Default-Verhalten;
// explizit setzen für Transparenz).
type openaiStreamBody struct {
	Model         string            `json:"model"`
	MaxTokens     int               `json:"max_tokens,omitempty"`
	Messages      []openaiSendMsg   `json:"messages"`
	Stream        bool              `json:"stream"`
	StreamOptions *openaiStreamOpts `json:"stream_options,omitempty"`
	Tools         []OpenAIToolSpec  `json:"tools,omitempty"`
	ToolChoice    string            `json:"tool_choice,omitempty"`
	User          string            `json:"user,omitempty"` // Karte #664, siehe openaiSendBody.User
}
type openaiStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

// openaiStreamChunk — relevante Felder eines /v1/chat/completions-Stream-
// Frames. delta.content trägt den inkrementellen Text-Token; delta.tool_calls
// das inkrementelle Tool-Use-Schema (Index-basiert, Args werden gestreamt).
// finish_reason kommt im letzten choice-Frame; usage liegt im finalen Frame
// mit "stream_options.include_usage": true (choices kann dann leer sein).
type openaiStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string                `json:"content"`
			ToolCalls []openaiToolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Model string `json:"model"`
}

// openaiToolCallDelta — ein einzelner tool_calls-Delta-Frame. Der erste
// Delta pro Index trägt id+type+function.name; nachfolgende Deltas nur
// function.arguments-Strings, die clientseitig konkateniert werden.
type openaiToolCallDelta struct {
	Index    int                      `json:"index"`
	ID       string                   `json:"id"`
	Type     string                   `json:"type"`
	Function *openaiToolCallFuncDelta `json:"function,omitempty"`
}
type openaiToolCallFuncDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// istFehlerWert: Ein `error`-Feld macht den Frame nur zum Fehler, wenn es etwas
// trägt — nicht leer (`""`), `{}`, `[]`, `null`, `false` (V2-2).
func istFehlerWert(roh json.RawMessage) bool {
	var s string
	if json.Unmarshal(roh, &s) == nil {
		return strings.TrimSpace(s) != ""
	}
	var o map[string]json.RawMessage
	if json.Unmarshal(roh, &o) == nil {
		return len(o) > 0
	}
	var l []json.RawMessage
	if json.Unmarshal(roh, &l) == nil {
		return len(l) > 0
	}
	return false
}

// streamOpenAICompatible bedient OpenAI + Mistral (identisches SSE-Schema).
// providerName landet in Fehler-Wrappern als Diagnose-Hilfe.
func streamOpenAICompatible(ctx context.Context, client *http.Client, ua, url, key string, sr SendRequest, providerName string, onChunk func(StreamChunk) error) (SendResult, error) {
	messages := openaiMessages(sr)
	maxTok := sr.MaxTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	payload := openaiStreamBody{
		Model:         sr.Model,
		MaxTokens:     maxTok,
		Messages:      messages,
		Stream:        true,
		StreamOptions: &openaiStreamOpts{IncludeUsage: true},
		User:          openaiUserFeld(sr, providerName),
	}
	// Karte #665: ABOARD_LLM_OPENAI_WERKZEUGE=aus — ein lokales Modell ohne
	// Werkzeug-Unterstützung bekommt weder tools noch tool_choice.
	if tools := SelectTools(sr.EnableMutations, sr.EnableFetch); len(tools) > 0 && !(providerName == "openai" && OpenAIWerkzeugeAus()) {
		payload.Tools = BuildOpenAITools(tools)
		payload.ToolChoice = "auto"
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return SendResult{}, fmt.Errorf("%s: marshal stream request: %w", providerName, err)
	}
	// F-1126 Nachtrag: client hat kein Gesamt-Timeout mehr (siehe
	// defaultClient()-Doku in providers.go) — readCtx bricht stattdessen ab,
	// wenn touch() (siehe Scan-Schleife unten) länger als streamIdleTimeout
	// nicht aufgerufen wird (tote, aber offene Verbindung). cancelGuard
	// räumt die Watchdog-Goroutine in jedem Fall auf.
	// F-1140: bis zum ersten touch() (= erste SSE-Zeile) gilt die längere
	// streamErstbyteTimeout statt streamIdleTimeout — eine lange Denkphase
	// vor dem ersten Token ist keine tote Verbindung.
	readCtx, touch, cancelGuard := streamIdleGuard(ctx, streamErstbyteTimeout, streamIdleTimeout)
	defer cancelGuard()
	req, err := http.NewRequestWithContext(readCtx, http.MethodPost, url, strings.NewReader(string(buf)))
	if err != nil {
		return SendResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/event-stream")

	res, err := client.Do(req)
	if err != nil {
		return SendResult{}, wrapNetErr(err)
	}
	defer res.Body.Close()
	if err := classifyHTTPResponse(res, key); err != nil {
		return SendResult{}, err
	}

	result := SendResult{Model: sr.Model, FinishReason: "stop"}
	var fullText strings.Builder

	// Tool-Call-Akkumulator (Etappe 4, ADR-0096 §3). OpenAI/Mistral streamen
	// tool_calls inkrementell pro Index: erster Delta trägt id+name,
	// nachfolgende Deltas nur arguments-Stringfragmente. Bei FinishReason
	// "tool_calls" (oder Stream-Ende) emittieren wir die akkumulierten Blöcke.
	type pendingTool struct {
		id, name string
		args     strings.Builder
	}
	toolBlocks := map[int]*pendingTool{}
	toolOrder := []int{} // Reihenfolge für deterministisches Emit am Ende

	emitTools := func() error {
		for _, idx := range toolOrder {
			pt, ok := toolBlocks[idx]
			if !ok {
				continue
			}
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
		toolBlocks = map[int]*pendingTool{}
		toolOrder = nil
		return nil
	}

	// SSE-Parser: nur "data: <json>"-Zeilen sind relevant; "data: [DONE]"
	// beendet den Stream. event:-Header schickt OpenAI nicht (anders als
	// Anthropic) — wir routen anhand des Payloads selbst.
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// K2-7: Ein Stream gilt nur als vollständig, wenn `[DONE]` kam oder ein Chunk ein
	// finish_reason trug. Sonst (Verbindung vom Gegenüber abgeschnitten, Modell-Prozess
	// gestorben) wäre die halbe Antwort ein stiller Erfolg.
	sahDone, sahEnde := false, false

	for scanner.Scan() {
		// Jede empfangene Zeile zählt als Lebenszeichen — hält den
		// Idle-Guard zurück (F-1126 Nachtrag).
		touch()
		if err := ctx.Err(); err != nil {
			return result, wrapNetErr(err)
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue // Kommentare, Leerzeilen, Multi-Line-Frames (nicht von OpenAI verwendet)
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "[DONE]" {
			sahDone = true
			continue
		}
		if data == "" {
			continue
		}
		// K2-7: Fehler-Frame mitten im Stream (`data: {"error":{…}}`, vLLM/OpenAI bei
		// Abbruch nach dem Start): als Fehler melden statt als leere Antwort.
		var fehlerFrame struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &fehlerFrame) == nil && istFehlerWert(fehlerFrame.Error) {
			meldung := schwaerzeSchluessel(anbieterMeldungAusBody([]byte(data)), key)
			return result, &ProviderFehler{Art: ErrProviderRejected, Meldung: meldung}
		}
		var chunk openaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // tolerant: unbekanntes Frame nicht crashen
		}
		if chunk.Model != "" {
			result.Model = chunk.Model
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				fullText.WriteString(ch.Delta.Content)
				if err := onChunk(StreamChunk{Text: ch.Delta.Content}); err != nil {
					return result, err
				}
			}
			// Tool-Call-Deltas akkumulieren (Etappe 4).
			for _, tcd := range ch.Delta.ToolCalls {
				pt, ok := toolBlocks[tcd.Index]
				if !ok {
					pt = &pendingTool{}
					toolBlocks[tcd.Index] = pt
					toolOrder = append(toolOrder, tcd.Index)
				}
				if tcd.ID != "" {
					pt.id = tcd.ID
				}
				if tcd.Function != nil {
					if tcd.Function.Name != "" {
						pt.name = tcd.Function.Name
					}
					if tcd.Function.Arguments != "" {
						pt.args.WriteString(tcd.Function.Arguments)
					}
				}
			}
			if ch.FinishReason != "" {
				result.FinishReason = ch.FinishReason
				sahEnde = true
			}
		}
		if chunk.Usage != nil {
			result.InputTokens = chunk.Usage.PromptTokens
			result.OutputTokens = chunk.Usage.CompletionTokens
		}
	}
	if err := scanner.Err(); err != nil {
		return result, wrapNetErr(err)
	}
	if !sahDone && !sahEnde {
		return result, wrapNetErr(errors.New("Stream endete ohne [DONE] und ohne finish_reason (abgebrochen)"))
	}
	// Akkumulierte Tool-Calls am Stream-Ende emittieren (auch wenn das LLM
	// FinishReason "tool_calls" gemeldet hat — wir warten, bis der Stream
	// vollständig durch ist, damit die Args komplett sind).
	if err := emitTools(); err != nil {
		return result, err
	}

	result.Text = fullText.String()
	return result, nil
}
