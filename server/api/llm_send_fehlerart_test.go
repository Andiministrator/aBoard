package api_test

// Karte #665 (ADR-0133 §3): Fehlerart aus der 4xx-Antwort des Anbieters am
// nicht streamenden Endpunkt — 400 mit kind (502 hieße im Frontend „nicht
// erreichbar“) und der gekürzten, geschwärzten Meldung.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestLLMSend_Fehlerart_AusAnbieterAntwort(t *testing.T) {
	for _, c := range []struct {
		art  error
		kind string
	}{
		{llm.ErrContextTooLong, "context-too-long"},
		{llm.ErrToolsUnsupported, "tools-unsupported"},
		{llm.ErrProviderRejected, "provider-rejected"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			env := newLLMSendEnv(t)
			env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
			env.grant(t, "a@x.de", storage.RoleEditor)
			env.provider.sendErr = fmt.Errorf("send: %w", &llm.ProviderFehler{
				Art: c.art, Status: 400, Meldung: "maximum context length is 8192 (mail a@x.de)"})

			resp := env.do(t, http.MethodPost, sendPath(env.boardID, env.cardID), "a@x.de",
				map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("Status %d, erwartet 400", resp.StatusCode)
			}
			var body struct {
				Kind    string `json:"kind"`
				Message string `json:"message"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Kind != c.kind {
				t.Errorf("kind %q, erwartet %q", body.Kind, c.kind)
			}
			if !strings.Contains(body.Message, "maximum context length is 8192") {
				t.Errorf("Meldung des Anbieters fehlt: %q", body.Message)
			}
			if strings.Contains(body.Message, "a@x.de") {
				t.Errorf("E-Mail nicht geschwärzt: %q", body.Message)
			}
		})
	}
}
