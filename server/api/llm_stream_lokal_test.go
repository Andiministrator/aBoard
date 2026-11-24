package api_test

// Karte #665 (ADR-0133 §3): Fehlerart im Stream und Lebenszeichen vor dem
// ersten Wort, am echten Handler.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

func TestLLMStream_Lebenszeichen_VorErstemWort(t *testing.T) {
	defer api.SetzeLebenszeichenFuerTest(25 * time.Millisecond)()
	env := newLLMStreamEnv(t)
	env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
	env.grant(t, "a@x.de", storage.RoleEditor)
	env.provider.vorPause = 200 * time.Millisecond

	resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
		map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	roh := sb.String()
	iToken := strings.Index(roh, "event: token")
	iPing := strings.Index(roh, ": lebenszeichen")
	if iPing < 0 {
		t.Fatalf("kein Lebenszeichen im Stream: %q", roh)
	}
	if iToken < 0 || iPing > iToken {
		t.Errorf("Lebenszeichen muss VOR dem ersten Wort stehen (ping@%d token@%d)", iPing, iToken)
	}
	ev := parseSSEResponse(t, strings.NewReader(roh))
	var done bool
	for _, e := range ev {
		if e.Event == "done" {
			done = true
		}
	}
	if !done {
		t.Errorf("Kommentarzeilen dürfen den Ablauf nicht stören: kein done-Event in %+v", ev)
	}
}

func TestLLMStream_Fehlerart_AusAnbieterAntwort(t *testing.T) {
	for _, c := range []struct {
		err  error
		kind string
	}{
		{&llm.ProviderFehler{Art: llm.ErrContextTooLong, Status: 400, Meldung: "maximum context length is 8192"}, "context-too-long"},
		{&llm.ProviderFehler{Art: llm.ErrToolsUnsupported, Status: 400, Meldung: "tool choice requires flag"}, "tools-unsupported"},
		{&llm.ProviderFehler{Art: llm.ErrProviderRejected, Status: 404, Meldung: "model does not exist"}, "provider-rejected"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			env := newLLMStreamEnv(t)
			env.seedUserWithKey(t, "u-a", "a@x.de", "sk-ant")
			env.grant(t, "a@x.de", storage.RoleEditor)
			env.provider.streamErr = fmt.Errorf("stream: %w", c.err)

			resp := env.do(t, http.MethodPost, streamPath(env.boardID, env.cardID), "a@x.de",
				map[string]any{"provider": "anthropic", "model": "claude-opus-4-7"})
			events := parseSSEResponse(t, resp.Body)
			resp.Body.Close()
			var got string
			for _, e := range events {
				if e.Event == "error" {
					got = e.Data
				}
			}
			if !strings.Contains(got, `"kind":"`+c.kind+`"`) {
				t.Errorf("error-Event %q, erwartet kind %s", got, c.kind)
			}
			if strings.Contains(got, "unreachable") {
				t.Errorf("darf nicht unreachable heißen: %q", got)
			}
			if !strings.Contains(got, strings.Fields(c.err.(*llm.ProviderFehler).Meldung)[0]) {
				t.Errorf("Meldung des Anbieters fehlt: %q", got)
			}
		})
	}
}
