package llm

import (
	"context"
	"net/http"
)

// MistralProvider — Test gegen /v1/models, Send gegen /v1/chat/completions.
// API-Schema ist OpenAI-kompatibel (system als erste Message); Send wird
// daher über sendOpenAICompatible (siehe openai.go) abgewickelt.
type MistralProvider struct {
	Client       *http.Client
	UA           string
	Endpoint     string // /v1/models, TestKey-Stub
	SendEndpoint string // /v1/chat/completions, Send-Stub
}

func (p *MistralProvider) Name() string { return "mistral" }

func (p *MistralProvider) endpointModels() string {
	if p.Endpoint != "" {
		return p.Endpoint
	}
	return "https://api.mistral.ai/v1/models"
}

func (p *MistralProvider) endpointChat() string {
	if p.SendEndpoint != "" {
		return p.SendEndpoint
	}
	return "https://api.mistral.ai/v1/chat/completions"
}

func (p *MistralProvider) TestKey(ctx context.Context, key string) error {
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

func (p *MistralProvider) Send(ctx context.Context, key string, sr SendRequest) (SendResult, error) {
	return sendOpenAICompatible(ctx, p.Client, p.UA, p.endpointChat(), key, sr, "mistral")
}

// SendStream — Mistral chat-completions ist OpenAI-kompatibel, inkl. SSE.
// streamOpenAICompatible wird dafür wiederverwendet (ADR-0096 §1, Etappe 2).
func (p *MistralProvider) SendStream(ctx context.Context, key string, sr SendRequest, onChunk func(StreamChunk) error) (SendResult, error) {
	return streamOpenAICompatible(ctx, p.Client, p.UA, p.endpointChat(), key, sr, "mistral", onChunk)
}

func (p *MistralProvider) Pricing(model string) (float64, float64) {
	return lookupPricing(mistralPricing, model)
}
