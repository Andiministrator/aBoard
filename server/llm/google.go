package llm

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GoogleProvider — Test gegen /v1/models, Send gegen /v1beta/models/:model:generateContent.
// Google verwendet `?key=…` als Query-Parameter, kein Header. Schema unterscheidet
// sich strukturell von OpenAI/Anthropic: System-Prompt wird als separates
// "system_instruction"-Feld geliefert, User-Prompt steht in `contents[].parts[].text`.
type GoogleProvider struct {
	Client       *http.Client
	UA           string
	Endpoint     string // /v1/models, TestKey-Stub
	SendEndpoint string // /v1beta/models/{model}:generateContent, Send-Stub
}

func (p *GoogleProvider) Name() string { return "google" }

func (p *GoogleProvider) endpointModels() string {
	if p.Endpoint != "" {
		return p.Endpoint
	}
	return "https://generativelanguage.googleapis.com/v1/models"
}

// endpointGenerate baut die Send-URL. Im Test-Modus zeigt SendEndpoint auf
// einen Stub; das Modell-Segment ({model}:generateContent) entfällt dann,
// weil der Stub ohnehin alles annimmt.
func (p *GoogleProvider) endpointGenerate(model string) string {
	if p.SendEndpoint != "" {
		return p.SendEndpoint
	}
	return "https://generativelanguage.googleapis.com/v1beta/models/" + model + ":generateContent"
}

// endpointStreamGenerate baut die Streaming-Send-URL.
// `?alt=sse` kommt erst beim Aufbau in SendStream dazu, damit der Test-Stub
// dieselbe SendEndpoint-Override-Logik nutzen kann wie Send.
func (p *GoogleProvider) endpointStreamGenerate(model string) string {
	if p.SendEndpoint != "" {
		return p.SendEndpoint
	}
	return "https://generativelanguage.googleapis.com/v1beta/models/" + model + ":streamGenerateContent"
}

func (p *GoogleProvider) TestKey(ctx context.Context, key string) error {
	// F-1126: der geteilte Client hat kein Client.Timeout mehr (siehe
	// defaultClient()-Doku in providers.go) — TestKey bekommt seine kurze
	// Gesamtfrist deshalb hier selbst, über den Context.
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	u, err := url.Parse(p.endpointModels())
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("key", key)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", p.UA)

	res, err := p.Client.Do(req)
	if err != nil {
		return wrapNetErr(err)
	}
	defer res.Body.Close()
	return classifyHTTPStatus(res.StatusCode)
}

// googleSendBody — Schema der :generateContent-API.
// Etappe 4 (ADR-0096 §3): Tools wird mit EnableMutations=true gefüllt.
type googleSendBody struct {
	SystemInstruction *googlePart      `json:"system_instruction,omitempty"`
	Contents          []googleContent  `json:"contents"`
	GenerationConfig  *googleGenConfig `json:"generationConfig,omitempty"`
	Tools             []GoogleToolSpec `json:"tools,omitempty"`
}
type googleContent struct {
	Role string `json:"role"`
	// Parts ist heterogen: Text-Parts (googlePart), Werkzeug-Aufrufe
	// (googleFunctionCallPart) und Werkzeug-Antworten
	// (googleFunctionResponsePart). Ein Google-Part trägt genau EINE dieser
	// Ausprägungen — deshalb `[]any` und nicht ein Struct mit drei
	// omitempty-Feldern: letzteres hätte `{"text":""}` neben einem
	// functionCall gesetzt, und `text` trägt hier bewusst kein omitempty
	// (der Bestandsdraht soll unverändert bleiben).
	Parts []any `json:"parts"`
}

// googleFunctionCallPart / googleFunctionResponsePart — die Werkzeug-Runden
// im Google-Schema (ADR-0120 §5). Google verknüpft Aufruf und Antwort über
// den NAMEN, nicht über eine id — anders als Anthropic und OpenAI. Deshalb
// trägt ToolResultBlock ein Name-Feld.
// googleInlineDataPart — ein Bild im Google-Schema (ADR-0121). Schreibweise
// snake_case wie system_instruction daneben; Google akzeptiert beide, aber
// zwei Schreibweisen in einer Datei sind eine Einladung zum Vertippen.
type googleInlineDataPart struct {
	InlineData googleInlineData `json:"inline_data"`
}
type googleInlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type googleFunctionCallPart struct {
	FunctionCall googleFunctionCall `json:"functionCall"`
}
type googleFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}
type googleFunctionResponsePart struct {
	FunctionResponse googleFunctionResponse `json:"functionResponse"`
}
type googleFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

// googleContents baut die contents[]-Sequenz. Google nutzt role="model" für
// Assistenten-Turns (nicht "assistant" wie OpenAI/Anthropic) — Mapping
// findet hier statt. History älteste zuerst, dann der aktuelle User-Turn.
//
// Leerer UserPrompt hängt keine Nachricht an — siehe anthropicMessages.
func googleContents(sr SendRequest) []googleContent {
	out := make([]googleContent, 0, len(sr.History)+1)
	for _, h := range sr.History {
		role := h.Role
		if role == "assistant" {
			role = "model"
		}
		out = append(out, googleContent{Role: role, Parts: googleParts(h)})
	}
	if sr.UserPrompt != "" || len(sr.Images) > 0 {
		out = append(out, googleContent{Role: "user", Parts: googleParts(HistoryMessage{
			Role: "user", Content: sr.UserPrompt, Images: sr.Images,
		})})
	}
	return out
}

// googleParts übersetzt eine History-Nachricht in Google-Parts. Ohne
// Werkzeug-Blöcke bleibt es bei genau einem Text-Part — derselbe Draht wie
// vor #428.
func googleParts(h HistoryMessage) []any {
	if len(h.ToolUses) == 0 && len(h.ToolResults) == 0 && len(h.Images) == 0 {
		return []any{googlePart{Text: h.Content}}
	}
	parts := make([]any, 0, 1+len(h.ToolUses)+len(h.ToolResults)+len(h.Images))
	for _, img := range h.Images {
		parts = append(parts, googleInlineDataPart{
			InlineData: googleInlineData{
				MimeType: img.MIME,
				Data:     base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	if strings.TrimSpace(h.Content) != "" {
		parts = append(parts, googlePart{Text: h.Content})
	}
	for _, tu := range h.ToolUses {
		parts = append(parts, googleFunctionCallPart{
			FunctionCall: googleFunctionCall{Name: tu.Name, Args: tu.Args},
		})
	}
	for _, tr := range h.ToolResults {
		// Fehler als eigener Schlüssel, damit das Modell die Ablehnung als
		// solche liest und nicht als Karteninhalt.
		resp := map[string]any{"content": tr.Content}
		if tr.IsError {
			resp = map[string]any{"error": tr.Content}
		}
		parts = append(parts, googleFunctionResponsePart{
			FunctionResponse: googleFunctionResponse{Name: tr.Name, Response: resp},
		})
	}
	return parts
}

type googlePart struct {
	Text string `json:"text"`
}
type googleGenConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
}

// googleSendResponse — relevante Felder der :generateContent-Antwort.
type googleSendResponse struct {
	Candidates []struct {
		FinishReason string `json:"finishReason"`
		Content      struct {
			Parts []googlePart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

func (p *GoogleProvider) Send(ctx context.Context, key string, sr SendRequest) (SendResult, error) {
	maxTok := sr.MaxTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	payload := googleSendBody{
		Contents:         googleContents(sr),
		GenerationConfig: &googleGenConfig{MaxOutputTokens: maxTok},
	}
	if sr.System != "" {
		payload.SystemInstruction = &googlePart{Text: sr.System}
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return SendResult{}, fmt.Errorf("google: marshal request: %w", err)
	}
	u, err := url.Parse(p.endpointGenerate(sr.Model))
	if err != nil {
		return SendResult{}, err
	}
	q := u.Query()
	q.Set("key", key)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(buf)))
	if err != nil {
		return SendResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", p.UA)

	res, err := p.Client.Do(req)
	if err != nil {
		return SendResult{}, wrapNetErr(err)
	}
	defer res.Body.Close()
	if err := classifyHTTPStatus(res.StatusCode); err != nil {
		return SendResult{}, err
	}
	var parsed googleSendResponse
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return SendResult{}, fmt.Errorf("google: parse response: %w", err)
	}
	text := ""
	finish := "stop"
	if len(parsed.Candidates) > 0 {
		cand := parsed.Candidates[0]
		var sb strings.Builder
		for _, part := range cand.Content.Parts {
			sb.WriteString(part.Text)
		}
		text = sb.String()
		if cand.FinishReason != "" {
			finish = cand.FinishReason
		}
	}
	return SendResult{
		Text:         text,
		InputTokens:  parsed.UsageMetadata.PromptTokenCount,
		OutputTokens: parsed.UsageMetadata.CandidatesTokenCount,
		Model:        sr.Model, // Google liefert das Modell nicht zurück → req-Wert spiegeln
		FinishReason: finish,
	}, nil
}

func (p *GoogleProvider) Pricing(model string) (float64, float64) {
	return lookupPricing(googlePricing, model)
}

// SendStream — Google streamGenerateContent (ADR-0096 §1, Etappe 2).
//
// Stream-URL: …/{model}:streamGenerateContent?key=…&alt=sse
//   - `alt=sse` schaltet von JSON-Array-Chunks auf echte SSE-Frames um.
//
// Frame-Schema (Beispiel):
//
//	data: {"candidates":[{"content":{"parts":[{"text":"Hi"}],"role":"model"}}]}
//	data: {"candidates":[{"content":{"parts":[{"text":" world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}
//
// Es gibt keinen [DONE]-Marker — der Stream endet einfach. finishReason +
// usageMetadata liegen im finalen Chunk (Google liefert sie konsistent
// zusammen mit dem letzten Token-Frame).
func (p *GoogleProvider) SendStream(ctx context.Context, key string, sr SendRequest, onChunk func(StreamChunk) error) (SendResult, error) {
	maxTok := sr.MaxTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	payload := googleSendBody{
		Contents:         googleContents(sr),
		GenerationConfig: &googleGenConfig{MaxOutputTokens: maxTok},
	}
	if sr.System != "" {
		payload.SystemInstruction = &googlePart{Text: sr.System}
	}
	if tools := SelectTools(sr.EnableMutations, sr.EnableFetch); len(tools) > 0 {
		payload.Tools = BuildGoogleTools(tools)
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return SendResult{}, fmt.Errorf("google: marshal stream request: %w", err)
	}
	u, err := url.Parse(p.endpointStreamGenerate(sr.Model))
	if err != nil {
		return SendResult{}, err
	}
	q := u.Query()
	q.Set("key", key)
	q.Set("alt", "sse")
	u.RawQuery = q.Encode()
	// F-1126 Nachtrag: p.Client hat kein Gesamt-Timeout mehr (siehe
	// defaultClient()-Doku in providers.go) — readCtx bricht stattdessen ab,
	// wenn touch() (siehe Scan-Schleife unten) länger als streamIdleTimeout
	// nicht aufgerufen wird (tote, aber offene Verbindung). cancelGuard
	// räumt die Watchdog-Goroutine in jedem Fall auf.
	// F-1140: bis zum ersten touch() (= erste SSE-Zeile) gilt die längere
	// streamErstbyteTimeout statt streamIdleTimeout — eine lange Denkphase
	// vor dem ersten Token ist keine tote Verbindung.
	readCtx, touch, cancelGuard := streamIdleGuard(ctx, streamErstbyteTimeout, streamIdleTimeout)
	defer cancelGuard()
	req, err := http.NewRequestWithContext(readCtx, http.MethodPost, u.String(), strings.NewReader(string(buf)))
	if err != nil {
		return SendResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", p.UA)

	res, err := p.Client.Do(req)
	if err != nil {
		return SendResult{}, wrapNetErr(err)
	}
	defer res.Body.Close()
	if err := classifyHTTPStatus(res.StatusCode); err != nil {
		return SendResult{}, err
	}

	result := SendResult{Model: sr.Model, FinishReason: "stop"}
	var fullText strings.Builder

	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		// Jede empfangene Zeile zählt als Lebenszeichen — hält den
		// Idle-Guard zurück (F-1126 Nachtrag).
		touch()
		if err := ctx.Err(); err != nil {
			return result, wrapNetErr(err)
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "" {
			continue
		}
		var chunk struct {
			Candidates []struct {
				FinishReason string `json:"finishReason"`
				Content      struct {
					Parts []struct {
						Text         string `json:"text"`
						FunctionCall *struct {
							Name string          `json:"name"`
							Args json.RawMessage `json:"args"`
						} `json:"functionCall,omitempty"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			UsageMetadata struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, cand := range chunk.Candidates {
			for _, part := range cand.Content.Parts {
				if part.Text != "" {
					fullText.WriteString(part.Text)
					if err := onChunk(StreamChunk{Text: part.Text}); err != nil {
						return result, err
					}
				}
				if part.FunctionCall != nil && part.FunctionCall.Name != "" {
					// Google streamt Function-Calls als komplette Struktur
					// (keine inkrementellen args-Deltas wie OpenAI). Args
					// sind RawMessage — ggf. leeres Objekt bei {}.
					args := part.FunctionCall.Args
					if len(args) == 0 {
						args = json.RawMessage("{}")
					}
					// Google hat keine Tool-Call-ID — wir generieren eine
					// per-Stream-eindeutige aus Index+Name (stabil genug für
					// den Confirm-Round-Trip).
					tcID := fmt.Sprintf("gcall_%s_%d", part.FunctionCall.Name, fullText.Len())
					if err := onChunk(StreamChunk{ToolCall: &ToolCallChunk{
						ID:   tcID,
						Name: part.FunctionCall.Name,
						Args: args,
					}}); err != nil {
						return result, err
					}
				}
			}
			if cand.FinishReason != "" {
				result.FinishReason = cand.FinishReason
			}
		}
		if chunk.UsageMetadata.PromptTokenCount > 0 {
			result.InputTokens = chunk.UsageMetadata.PromptTokenCount
		}
		if chunk.UsageMetadata.CandidatesTokenCount > 0 {
			result.OutputTokens = chunk.UsageMetadata.CandidatesTokenCount
		}
	}
	if err := scanner.Err(); err != nil {
		return result, wrapNetErr(err)
	}

	result.Text = fullText.String()
	return result, nil
}
