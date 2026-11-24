package llm_test

// tools_stream_test.go — Tool-Use-Streaming-Tests pro Provider
// (#300 Etappe 4, ADR-0096 §3). Pro Provider ein Happy-Path: ein
// fake-SSE-Server emittet einen Tool-Call, der Provider-Parser muss
// daraus einen ToolCallChunk machen, der via onChunk an den Handler
// gereicht wird.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
)

// ── Anthropic ───────────────────────────────────────────────────────────────

func TestAnthropicSendStream_ToolUse_ParsesAndEmits(t *testing.T) {
	// Anthropic streamt tool_use: content_block_start (type:tool_use)
	// → mehrere input_json_delta → content_block_stop.
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-7\",\"usage\":{\"input_tokens\":10}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_01\",\"name\":\"set-field\",\"input\":{}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"field\\\":\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"qa\\\",\\\"value\\\":true}\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Body-Check: Tools sollten mitgesendet werden, wenn EnableMutations=true
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"tools":`) {
			t.Errorf("Anthropic-Body enthält keine tools[]: %s", body)
		}
		if !strings.Contains(string(body), `"name":"set-field"`) {
			t.Errorf("Anthropic-Body listet set-field nicht: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, e := range events {
			_, _ = io.WriteString(w, e)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	p := &llm.AnthropicProvider{Client: srv.Client(), UA: "aboard-test/0", Endpoint: srv.URL}
	var toolCalls []*llm.ToolCallChunk
	_, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "claude-opus-4-7", UserPrompt: "set qa=true", EnableMutations: true},
		func(c llm.StreamChunk) error {
			if c.ToolCall != nil {
				toolCalls = append(toolCalls, c.ToolCall)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("SendStream err: %v", err)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("got %d tool-calls, want 1", len(toolCalls))
	}
	tc := toolCalls[0]
	if tc.ID != "toolu_01" {
		t.Errorf("ID=%q, want toolu_01", tc.ID)
	}
	if tc.Name != "set-field" {
		t.Errorf("Name=%q, want set-field", tc.Name)
	}
	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("Args not valid JSON: %v (raw=%q)", err, tc.Args)
	}
	if args["field"] != "qa" || args["value"] != true {
		t.Errorf("Args=%v, want field=qa value=true", args)
	}
}

func TestAnthropicSendStream_NoToolsWhenDisabled(t *testing.T) {
	// EnableMutations=false → kein tools-Feld im Request-Body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"tools":`) {
			t.Errorf("tools-Feld trotz EnableMutations=false: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: message_stop\ndata: {}\n\n")
	}))
	t.Cleanup(srv.Close)
	p := &llm.AnthropicProvider{Client: srv.Client(), UA: "aboard-test/0", Endpoint: srv.URL}
	_, _ = p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(c llm.StreamChunk) error { return nil })
}

// ── OpenAI / Mistral (gleicher Pfad) ────────────────────────────────────────

func TestOpenAISendStream_ToolCalls_AccumulatesArgsDeltas(t *testing.T) {
	// OpenAI streamt tool_calls als index-basierte Deltas. Erste Delta trägt
	// id+name, nachfolgende nur arguments-Stringfragmente.
	frames := []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"add-comment","arguments":""}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"text\""}}]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"hi\"}"}}]}}]}` + "\n\n",
		`data: {"choices":[{"finish_reason":"tool_calls"}]}` + "\n\n",
		`data: {"usage":{"prompt_tokens":11,"completion_tokens":3}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"tools":`) {
			t.Errorf("Body ohne tools: %s", body)
		}
		if !strings.Contains(string(body), `"tool_choice":"auto"`) {
			t.Errorf("Body ohne tool_choice=auto: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	p := &llm.OpenAIProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}
	var toolCalls []*llm.ToolCallChunk
	_, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "gpt-4o", UserPrompt: "comment", EnableMutations: true},
		func(c llm.StreamChunk) error {
			if c.ToolCall != nil {
				toolCalls = append(toolCalls, c.ToolCall)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("SendStream err: %v", err)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("got %d tool-calls, want 1", len(toolCalls))
	}
	tc := toolCalls[0]
	if tc.ID != "call_abc" {
		t.Errorf("ID=%q, want call_abc", tc.ID)
	}
	if tc.Name != "add-comment" {
		t.Errorf("Name=%q, want add-comment", tc.Name)
	}
	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("Args not valid JSON: %v (raw=%q)", err, tc.Args)
	}
	if args["text"] != "hi" {
		t.Errorf("Args=%v, want text=hi", args)
	}
}

func TestOpenAISendStream_MultipleParallelToolCalls(t *testing.T) {
	// Zwei tool_calls parallel (Index 0 + 1). Reihenfolge im Emit muss der
	// First-Seen-Reihenfolge entsprechen (Map-Iteration-Determinismus).
	frames := []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c0","type":"function","function":{"name":"set-field","arguments":"{\"field\":\"qa\",\"value\":true}"}},{"index":1,"id":"c1","type":"function","function":{"name":"add-label","arguments":"{\"label\":\"bug\"}"}}]}}]}` + "\n\n",
		`data: {"choices":[{"finish_reason":"tool_calls"}]}` + "\n\n",
		"data: [DONE]\n\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	p := &llm.OpenAIProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}
	var toolCalls []*llm.ToolCallChunk
	_, err := p.SendStream(context.Background(), "sk-test",
		llm.SendRequest{Model: "gpt-4o", UserPrompt: "x", EnableMutations: true},
		func(c llm.StreamChunk) error {
			if c.ToolCall != nil {
				toolCalls = append(toolCalls, c.ToolCall)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("SendStream err: %v", err)
	}
	if len(toolCalls) != 2 {
		t.Fatalf("got %d tool-calls, want 2", len(toolCalls))
	}
	// First-Seen-Order: c0 vor c1.
	if toolCalls[0].ID != "c0" || toolCalls[1].ID != "c1" {
		t.Errorf("Order=[%s,%s], want [c0,c1]", toolCalls[0].ID, toolCalls[1].ID)
	}
}

// ── Google ──────────────────────────────────────────────────────────────────

func TestGoogleSendStream_FunctionCall_Parsed(t *testing.T) {
	// Google streamt functionCall als komplette Struktur (kein args-Delta).
	frames := []string{
		`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"move","args":{"toColumnId":"col-erledigt"}}}]}}]}` + "\n\n",
		`data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2}}` + "\n\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"function_declarations":`) {
			t.Errorf("Body ohne function_declarations: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			if fl != nil {
				fl.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	p := &llm.GoogleProvider{Client: srv.Client(), UA: "aboard-test/0", SendEndpoint: srv.URL}
	var toolCalls []*llm.ToolCallChunk
	_, err := p.SendStream(context.Background(), "key",
		llm.SendRequest{Model: "gemini-1.5-pro", UserPrompt: "move it", EnableMutations: true},
		func(c llm.StreamChunk) error {
			if c.ToolCall != nil {
				toolCalls = append(toolCalls, c.ToolCall)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("SendStream err: %v", err)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("got %d tool-calls, want 1", len(toolCalls))
	}
	tc := toolCalls[0]
	if tc.Name != "move" {
		t.Errorf("Name=%q, want move", tc.Name)
	}
	if !strings.HasPrefix(tc.ID, "gcall_move_") {
		t.Errorf("ID=%q, want gcall_move_* prefix", tc.ID)
	}
	var args map[string]any
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		t.Fatalf("Args not valid JSON: %v (raw=%q)", err, tc.Args)
	}
	if args["toColumnId"] != "col-erledigt" {
		t.Errorf("Args=%v, want toColumnId=col-erledigt", args)
	}
}
