package llm_test

// openai_lokal_test.go — Karte #665 (ADR-0133 §3): Fehlerarten aus dem
// 4xx-Body, Kontextgrenze, Werkzeuge abschaltbar.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/llm"
)

func fehlerServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func TestOpenAI_Fehlerarten_AusDemBody(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
		teil   string // muss in der Meldung stehen
	}{
		{"kontext vLLM", 400, `{"object":"error","message":"This model's maximum context length is 8192 tokens. However, you requested 9000 tokens","type":"BadRequestError","code":400}`, llm.ErrContextTooLong, "maximum context length"},
		{"kontext OpenAI", 400, `{"error":{"message":"Input exceeds context_length_exceeded","type":"invalid_request_error"}}`, llm.ErrContextTooLong, "context_length"},
		{"kontext 413", 413, `Request Entity Too Large`, llm.ErrContextTooLong, "Too Large"},
		{"werkzeuge vLLM", 400, `{"object":"error","message":"\"auto\" tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set","code":400}`, llm.ErrToolsUnsupported, "enable-auto-tool-choice"},
		{"werkzeuge OWUI", 400, `{"detail":"Model does not support tools"}`, llm.ErrToolsUnsupported, "does not support tools"},
		{"abgelehnt 404", 404, `{"error":{"message":"The model board-x does not exist"}}`, llm.ErrProviderRejected, "does not exist"},
		{"abgelehnt Text", 422, "kaputt", llm.ErrProviderRejected, "kaputt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := fehlerServer(c.status, c.body)
			defer srv.Close()
			p := &llm.OpenAIProvider{Client: srv.Client(), UA: "t", SendEndpoint: srv.URL}
			sr := llm.SendRequest{Model: "m", UserPrompt: "hi"}

			_, errStream := p.SendStream(context.Background(), "k", sr, func(llm.StreamChunk) error { return nil })
			_, errSend := p.Send(context.Background(), "k", sr)
			for wo, err := range map[string]error{"SendStream": errStream, "Send": errSend} {
				if !errors.Is(err, c.want) {
					t.Fatalf("%s: err = %v, erwartet %v", wo, err, c.want)
				}
				if errors.Is(err, llm.ErrProviderUnreachable) || errors.Is(err, llm.ErrInvalidKey) {
					t.Errorf("%s: darf weder unreachable noch invalid-key sein: %v", wo, err)
				}
				if m := llm.AnbieterMeldung(err); !strings.Contains(m, c.teil) {
					t.Errorf("%s: Meldung %q enthält %q nicht", wo, m, c.teil)
				}
			}
		})
	}
}

func TestOpenAI_Fehlerarten_BleibenBeiUnreachableUndInvalidKey(t *testing.T) {
	for status, want := range map[int]error{
		401: llm.ErrInvalidKey, 403: llm.ErrInvalidKey,
		429: llm.ErrProviderUnreachable, 500: llm.ErrProviderUnreachable, 503: llm.ErrProviderUnreachable,
	} {
		srv := fehlerServer(status, `{"error":{"message":"context length"}}`)
		p := &llm.OpenAIProvider{Client: srv.Client(), UA: "t", SendEndpoint: srv.URL}
		_, err := p.SendStream(context.Background(), "k", llm.SendRequest{Model: "m", UserPrompt: "hi"}, func(llm.StreamChunk) error { return nil })
		srv.Close()
		if !errors.Is(err, want) {
			t.Errorf("status %d: err = %v, erwartet %v", status, err, want)
		}
		if errors.Is(err, llm.ErrContextTooLong) {
			t.Errorf("status %d darf nicht als Kontext-Fehler gelten", status)
		}
	}
}

func TestOpenAI_Fehlermeldung_GekuerztUndBereinigt(t *testing.T) {
	lang := strings.Repeat("x", 2000)
	srv := fehlerServer(400, `{"error":{"message":"zeile1\nzeile2\u0007 `+lang+`"}}`)
	defer srv.Close()
	p := &llm.OpenAIProvider{Client: srv.Client(), UA: "t", SendEndpoint: srv.URL}
	_, err := p.SendStream(context.Background(), "k", llm.SendRequest{Model: "m", UserPrompt: "hi"}, func(llm.StreamChunk) error { return nil })
	m := llm.AnbieterMeldung(err)
	if n := len([]rune(m)); n > 301 {
		t.Errorf("Meldung hat %d Zeichen, erwartet höchstens 301 (300 + …)", n)
	}
	if !strings.HasPrefix(m, "zeile1 zeile2 ") || strings.ContainsAny(m, "\n\a") {
		t.Errorf("Meldung nicht bereinigt: %.40q", m)
	}
	if !strings.HasSuffix(m, "…") {
		t.Errorf("Kürzung nicht gekennzeichnet")
	}
}

func TestParseOpenAIKontext(t *testing.T) {
	if n, err := llm.ParseOpenAIKontext("", ""); n != 0 || err != nil {
		t.Errorf("leer: %d, %v", n, err)
	}
	if n, err := llm.ParseOpenAIKontext(" 32768 ", "http://x/v1"); n != 32768 || err != nil {
		t.Errorf("gültig: %d, %v", n, err)
	}
	for _, bad := range []string{"abc", "0", "-5", "100", "99999999999"} {
		if _, err := llm.ParseOpenAIKontext(bad, "http://x/v1"); err == nil {
			t.Errorf("%q muss abgelehnt werden", bad)
		}
	}
	if _, err := llm.ParseOpenAIKontext("32768", ""); err == nil {
		t.Error("ohne Basis-URL muss abgelehnt werden")
	}
}

func TestParseOpenAIWerkzeuge(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "an": false, "AUS": true, " aus ": true} {
		if got, err := llm.ParseOpenAIWerkzeuge(raw); err != nil || got != want {
			t.Errorf("%q: %v, %v", raw, got, err)
		}
	}
	if _, err := llm.ParseOpenAIWerkzeuge("nein"); err == nil {
		t.Error("unbekannter Wert muss abgelehnt werden")
	}
}

func TestContextLimit_EigeneBasisMitKontextvariable(t *testing.T) {
	defer llm.SetzeOpenAIKompatibel(llm.SetzeOpenAIKompatibel(true))
	defer llm.SetzeOpenAIKontext(llm.SetzeOpenAIKontext(0))

	if n := llm.ContextLimit("openai", "board-chat"); n != 0 {
		t.Fatalf("ohne Variable: %d, erwartet 0", n)
	}
	llm.SetzeOpenAIKontext(32768)
	if n := llm.ContextLimit("openai", "board-chat"); n != 32768 {
		t.Errorf("mit Variable: %d, erwartet 32768", n)
	}
	if n := llm.ContextLimit("anthropic", "claude-opus-4-7"); n != 200000 {
		t.Errorf("andere Anbieter dürfen nicht betroffen sein: %d", n)
	}
	// Echtes OpenAI (keine eigene Basis): die Variable gilt nicht.
	llm.SetzeOpenAIKompatibel(false)
	if n := llm.ContextLimit("openai", "gpt-4o"); n != 128000 {
		t.Errorf("echtes OpenAI: %d, erwartet 128000", n)
	}
}

func TestOpenAIStream_WerkzeugeAus_LaesstToolsWeg(t *testing.T) {
	defer llm.SetzeOpenAIWerkzeugeAus(llm.SetzeOpenAIWerkzeugeAus(false))
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	p := &llm.OpenAIProvider{Client: srv.Client(), UA: "t", SendEndpoint: srv.URL}
	sr := llm.SendRequest{Model: "m", UserPrompt: "hi", EnableMutations: true, EnableFetch: true}
	run := func() {
		if _, err := p.SendStream(context.Background(), "k", sr, func(llm.StreamChunk) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}

	run() // an
	llm.SetzeOpenAIWerkzeugeAus(true)
	run() // aus
	if !strings.Contains(bodies[0], `"tools"`) || !strings.Contains(bodies[0], `"tool_choice":"auto"`) {
		t.Errorf("Werkzeuge an: tools/tool_choice fehlen im Body")
	}
	if strings.Contains(bodies[1], `"tools"`) || strings.Contains(bodies[1], "tool_choice") {
		t.Errorf("Werkzeuge aus: tools/tool_choice stehen noch im Body: %.200s", bodies[1])
	}
}

// Ein Gegenüber, das den Authorization-Kopf in der Fehlermeldung zurückspiegelt,
// darf den Schlüssel nicht bis in Browser und Log bringen (Firmenmodus, #664).
func TestOpenAI_Fehlermeldung_SchwaerztSchluessel(t *testing.T) {
	srv := fehlerServer(400, `{"error":{"message":"abgelehnt: Bearer geheim-12345 und geheim-12345 nochmal"}}`)
	defer srv.Close()
	p := &llm.OpenAIProvider{Client: srv.Client(), UA: "t", SendEndpoint: srv.URL}
	_, err := p.Send(context.Background(), "geheim-12345", llm.SendRequest{Model: "m", UserPrompt: "hi"})
	if m := llm.AnbieterMeldung(err); strings.Contains(m, "geheim-12345") || strings.Contains(err.Error(), "geheim-12345") {
		t.Errorf("Schlüssel in der Meldung: %q / %q", m, err.Error())
	}
}

// K3-8: FastAPI/pydantic liefert detail als Liste; `input` trägt die abgelehnte Eingabe
// (womöglich Karteninhalt) und darf nie in Meldung/Log landen. Ist der Body JSON, wird
// nie der Rohtext verwendet.
func TestOpenAI_Fehlermeldung_PydanticDetailListe_OhneInput(t *testing.T) {
	cases := []struct {
		name, body, teil string
	}{
		{"detail-liste", `{"detail":[{"type":"missing","loc":["body","messages",0,"content"],"msg":"Field required","input":{"role":"user","text":"GEHEIMER-KARTENINHALT"}}]}`, "body.messages.0.content: Field required"},
		{"unbekanntes JSON", `{"foo":"GEHEIMER-KARTENINHALT"}`, "ohne lesbare Meldung"},
		{"mistral message als Objekt", `{"object":"error","message":{"detail":[{"loc":["body","x"],"msg":"Field required","input":"GEHEIMER-KARTENINHALT"}]},"type":"invalid_request_message_error"}`, "body.x: Field required"},
		{"error-Objekt neben falsch typisiertem message", `{"error":{"message":"modell fehlt"},"message":{"a":1},"code":404}`, "modell fehlt"},
		{"detail neben falsch typisiertem error", `{"error":5,"detail":"Modell kaputt"}`, "Modell kaputt"},
		{"JSON-Array", `["GEHEIMER-KARTENINHALT"]`, "ohne lesbare Meldung"},
		{"detail-liste ohne msg", `{"detail":[{"input":"GEHEIMER-KARTENINHALT"}]}`, "ohne lesbare Meldung"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := fehlerServer(422, c.body)
			defer srv.Close()
			p := &llm.OpenAIProvider{Client: srv.Client(), UA: "t", SendEndpoint: srv.URL}
			_, err := p.SendStream(context.Background(), "k", llm.SendRequest{Model: "m", UserPrompt: "hi"}, func(llm.StreamChunk) error { return nil })
			if !errors.Is(err, llm.ErrProviderRejected) {
				t.Fatalf("err = %v, erwartet provider-rejected", err)
			}
			m := llm.AnbieterMeldung(err)
			if !strings.Contains(m, c.teil) {
				t.Errorf("Meldung %q enthält %q nicht", m, c.teil)
			}
			if strings.Contains(m, "GEHEIM") || strings.Contains(err.Error(), "GEHEIM") {
				t.Errorf("Karteninhalt in Meldung/Fehler: %q / %q", m, err.Error())
			}
		})
	}
}

func sseServer(roh string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, roh)
	}))
}

// K2-7: Fehler-Frame mitten im Stream → Fehler (provider-rejected, geschwärzt);
// Ende ohne [DONE] und ohne finish_reason → abgebrochen; je eines von beiden genügt.
func TestOpenAI_Stream_FehlerFrameUndAbbruch(t *testing.T) {
	wort := `data: {"choices":[{"delta":{"content":"Hallo"}}]}` + "\n\n"
	cases := []struct {
		name, roh string
		fehler    error // nil = Erfolg
		teil      string
	}{
		{"fehler-frame", wort + `data: {"error":{"message":"Bearer abc12345 engine died"}}` + "\n\n", llm.ErrProviderRejected, "engine died"},
		{"fehler-frame als Text", `data: {"error":"model crashed"}` + "\n\n", llm.ErrProviderRejected, "model crashed"},
		{"ende ohne DONE und finish", wort, llm.ErrProviderUnreachable, "abgebrochen"},
		{"nur DONE", wort + "data: [DONE]\n\n", nil, ""},
		{"nur finish_reason", wort + `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n", nil, ""},
		{"error leeres Objekt", `data: {"error":{},"choices":[{"delta":{"content":"x"}}]}` + "\n\ndata: [DONE]\n\n", nil, ""},
		{"error leerer Text", `data: {"error":"","choices":[{"delta":{"content":"x"}}]}` + "\n\ndata: [DONE]\n\n", nil, ""},
		{"error false", `data: {"error":false,"choices":[{"delta":{"content":"x"}}]}` + "\n\ndata: [DONE]\n\n", nil, ""},
		{"error null ist kein Fehler", `data: {"error":null,"choices":[{"delta":{"content":"x"}}]}` + "\n\ndata: [DONE]\n\n", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := sseServer(c.roh)
			defer srv.Close()
			p := &llm.OpenAIProvider{Client: srv.Client(), UA: "t", SendEndpoint: srv.URL}
			_, err := p.SendStream(context.Background(), "abc12345-schluessel", llm.SendRequest{Model: "m", UserPrompt: "hi"}, func(llm.StreamChunk) error { return nil })
			if c.fehler == nil {
				if err != nil {
					t.Fatalf("erwartet Erfolg, err = %v", err)
				}
				return
			}
			if !errors.Is(err, c.fehler) {
				t.Fatalf("err = %v, erwartet %v", err, c.fehler)
			}
			if !strings.Contains(err.Error(), c.teil) && !strings.Contains(llm.AnbieterMeldung(err), c.teil) {
				t.Errorf("Meldung enthält %q nicht: %v", c.teil, err)
			}
			if strings.Contains(err.Error(), "abc12345") {
				t.Errorf("Schlüssel nicht geschwärzt: %v", err)
			}
		})
	}
}
