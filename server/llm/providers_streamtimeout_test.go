package llm

// providers_streamtimeout_test.go — F-1126 (#544, Sprint 70): belegt und
// verifiziert, dass der von RegisterDefaults geteilte HTTP-Client Streams
// nicht mehr mitten im Body abschneidet.
//
// Hintergrund: Client.Timeout ist laut net/http-Doku eine GESAMTFRIST, die
// "includes ... reading the response body" umfasst. Vor F-1126 trug
// defaultClient() ein Client.Timeout von 10s, und RegisterDefaults reichte
// GENAU DIESEN Client an alle vier Provider für TestKey/Send/SendStream
// durch (providers.go) — jede Stream-Antwort, die länger als 10s zum
// vollständigen Lesen brauchte, wurde abgebrochen, auch wenn der Provider
// stetig weiter Tokens lieferte.
//
// Drei Tests, drei Belegebenen:
//  1. TestSendStream_ClientTotalTimeout_CutsRunningBody — Mechanismus-Beleg:
//     zeigt, dass ein Client mit (aus defaultClient() übernommenem, aber für
//     die Testlaufzeit herunterskaliertem) Client.Timeout>0 einen laufenden
//     Stream tatsächlich abschneidet. Läuft VOR dem Fix rot; nach dem Fix
//     hat defaultClient() kein Timeout mehr (Client.Timeout==0) — dann
//     entfällt der Skalierungs-Ansatzpunkt, der Test überspringt sich
//     selbst mit Verweis auf Test 2.
//  2. TestDefaultClient_NoTotalTimeout — die eigentliche Produktions-
//     Assertion: defaultClient().Timeout muss 0 sein (kein Gesamt-Timeout).
//     Deterministisch, ohne Sleep — vor dem Fix rot (Timeout==10s), nach
//     dem Fix grün.
//  3. TestSendStream_AnthropicOver12Seconds_CompletesFully — die im
//     Karten-Prüfbefehl genannte Widerlegung wörtlich: ein Stub, der ≥12s
//     lang Events schickt, muss vollständig ankommen. Läuft nur ohne
//     `-short` (go test -short überspringt sie) — 13s reale Testlaufzeit
//     sind für den Normallauf von `go test ./...` in Kauf genommen, aber
//     nicht für schnelle Iterationsschleifen.
//
// "mindestens Anthropic, die anderen, wenn mit vertretbarem Aufwand"
// (Aufgabenstellung #544): Test 4 deckt zusätzlich OpenAI ab (moderate
// Dauer, ohne -short-Gate) — OpenAI und Mistral teilen sich
// streamOpenAICompatible, ein Test deckt also beide Codepfade strukturell
// ab. Google hat eine eigene SendStream-Implementierung, die aber dieselbe
// p.Client-Verdrahtung nutzt wie Anthropic/OpenAI (siehe google.go) — ein
// eigener Langlauf-Test dafür wäre bei identischem Fix-Mechanismus
// zusätzlicher CI-Zeitaufwand ohne neuen Erkenntnisgewinn und wurde deshalb
// bewusst weggelassen (vertretbarer-Aufwand-Abwägung).

import (
	"context"
	"fmt"
	"github.com/Andiministrator/aBoard/server/ausgang"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// anthropicSSEEvent baut einen einzelnen Anthropic-SSE-Frame im Format, das
// AnthropicProvider.SendStream erwartet (siehe anthropic_stream_test.go).
func anthropicSSEEvent(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

// TestSendStream_ClientTotalTimeout_CutsRunningBody — Mechanismus-Beleg für
// F-1126: ein Client, dessen Timeout-Feld gesetzt ist, schneidet einen
// länger laufenden Stream mitten im Body ab (Go-Stdlib-Verhalten,
// unabhängig von unserem Code). Wir übernehmen den TATSÄCHLICHEN
// defaultClient()-Wert und skalieren ihn nur für die Testlaufzeit herunter
// (1/33 der echten ProbeTimeout-Größenordnung von 10s) — steht die
// Übertragbarkeit auf 10s belegt Test 2 (TestDefaultClient_NoTotalTimeout)
// strukturell: wenn Timeout>0 ist (wie vor dem Fix), tritt exakt der hier
// gezeigte Effekt auch bei 10s auf, nur langsamer.
func TestSendStream_ClientTotalTimeout_CutsRunningBody(t *testing.T) {
	client := defaultClient()
	if client.Timeout == 0 {
		t.Skip("F-1126 behoben: defaultClient() hat kein Gesamt-Timeout mehr, der hier demonstrierte Abbruch-Mechanismus greift nicht mehr — siehe TestDefaultClient_NoTotalTimeout für den Erfolgsnachweis und TestSendStream_AnthropicOver12Seconds_CompletesFully für den funktionalen Beleg")
	}
	// Herunterskaliert von defaultClient().Timeout (vor dem Fix: 10s) auf
	// 300ms, damit der Test nicht selbst über 10s läuft.
	client.Timeout = 300 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, anthropicSSEEvent("message_start",
			`{"type":"message_start","message":{"usage":{"input_tokens":1}}}`))
		fl.Flush()
		// Drei Text-Deltas über insgesamt ~600ms verteilt — deutlich mehr
		// als das 300ms-Timeout auf client, aber kurz genug fürs CI.
		for i, tok := range []string{"eins", "zwei", "drei"} {
			time.Sleep(200 * time.Millisecond)
			_ = i
			_, _ = io.WriteString(w, anthropicSSEEvent("content_block_delta",
				fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, tok)))
			fl.Flush()
		}
		_, _ = io.WriteString(w, anthropicSSEEvent("message_delta",
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`))
		fl.Flush()
		_, _ = io.WriteString(w, anthropicSSEEvent("message_stop", `{"type":"message_stop"}`))
		fl.Flush()
	}))
	defer srv.Close()

	p := &AnthropicProvider{Client: client, UA: "test/1.0", Endpoint: srv.URL}
	var received strings.Builder
	result, err := p.SendStream(context.Background(), "sk-test",
		SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(c StreamChunk) error {
			received.WriteString(c.Text)
			return nil
		})

	// Der Beleg: der Stream kommt NICHT vollständig an — entweder als
	// expliziter Fehler oder als unvollständiger Text, weil Client.Timeout
	// während des laufenden Body-Lesens abläuft.
	if err == nil && received.String() == "einszweidrei" {
		t.Fatalf("Stream kam trotz Client.Timeout=%v vollständig an (Text=%q, err=%v) — Mechanismus nicht reproduziert, Testaufbau prüfen", client.Timeout, result.Text, err)
	}
	t.Logf("Beleg reproduziert: Client.Timeout=%v hat den Stream abgeschnitten (Text=%q, err=%v)", client.Timeout, received.String(), err)
}

// TestDefaultClient_NoTotalTimeout ist die deterministische, sleep-freie
// Kern-Assertion für F-1126: der Produktions-Client darf keine Gesamtfrist
// mehr tragen (Client.Timeout==0 bedeutet in net/http "kein Timeout"). Vor
// dem Fix ist Timeout==10s (rot); nach dem Fix ist Timeout==0 (grün).
func TestDefaultClient_NoTotalTimeout(t *testing.T) {
	client := defaultClient()
	if client.Timeout != 0 {
		t.Fatalf("defaultClient().Timeout = %v, erwartet 0 (kein Gesamt-Timeout mehr, F-1126) — ein Wert > 0 schneidet jeden Stream ab, der länger als dieser Wert zum vollständigen Lesen braucht, unabhängig davon, ob der Provider weiter Daten liefert", client.Timeout)
	}
	/* Karte #569: vor dem Transport sitzt der Ausgangs-Schalter — ausgepackt
	   wird genau diese eine Hülle, nichts sonst. */
	tr, ok := ausgang.Basis(client.Transport).(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport ist kein *http.Transport (Typ %T) — ResponseHeaderTimeout-Schutz fehlt damit", client.Transport)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Errorf("Transport.ResponseHeaderTimeout = %v, erwartet > 0 — ohne das hängt ein nicht antwortender Provider unbegrenzt (F-1126-Nachtrag)", tr.ResponseHeaderTimeout)
	}
}

// TestSendStream_AnthropicOver12Seconds_CompletesFully ist der im Karten-
// Prüfbefehl wörtlich genannte Widerlegungstest: ein Stub, der ≥12s lang
// Events sendet, muss vollständig ankommen. Bewusst NICHT herunterskaliert
// — das ist der reale Prüfbefehl aus der Aufgabenstellung, keine
// Annäherung. `go test -short` überspringt ihn für schnelle Iteration.
func TestSendStream_AnthropicOver12Seconds_CompletesFully(t *testing.T) {
	if testing.Short() {
		t.Skip("übersprungen unter -short (reale Laufzeit ~13s) — TestDefaultClient_NoTotalTimeout deckt denselben Fix deterministisch ab")
	}
	const chunkCount = 13 // 13 × 1s ≈ 13s > 12s Mindestdauer aus der Aufgabenstellung
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, anthropicSSEEvent("message_start",
			`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`))
		fl.Flush()
		for i := 0; i < chunkCount; i++ {
			time.Sleep(1 * time.Second)
			_, _ = io.WriteString(w, anthropicSSEEvent("content_block_delta",
				fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"tok%d "}}`, i)))
			fl.Flush()
		}
		_, _ = io.WriteString(w, anthropicSSEEvent("message_delta",
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":13}}`))
		fl.Flush()
		_, _ = io.WriteString(w, anthropicSSEEvent("message_stop", `{"type":"message_stop"}`))
		fl.Flush()
	}))
	defer srv.Close()

	// Der PRODUKTIONS-Client, unverändert — kein Override.
	p := &AnthropicProvider{Client: defaultClient(), UA: "test/1.0", Endpoint: srv.URL}

	var chunkCountSeen int
	start := time.Now()
	result, err := p.SendStream(context.Background(), "sk-test",
		SendRequest{Model: "claude-opus-4-7", UserPrompt: "Hi"},
		func(c StreamChunk) error {
			if c.Text != "" {
				chunkCountSeen++
			}
			return nil
		})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("SendStream nach %v abgebrochen (erwartet vollständigen Durchlauf über >12s): %v", elapsed, err)
	}
	if elapsed < 12*time.Second {
		t.Fatalf("Stream lief nur %v — Testaufbau liefert nicht die geforderten ≥12s", elapsed)
	}
	if chunkCountSeen != chunkCount {
		t.Errorf("chunkCountSeen = %d, erwartet %d — Stream unvollständig angekommen", chunkCountSeen, chunkCount)
	}
	if result.OutputTokens != 13 {
		t.Errorf("OutputTokens = %d, erwartet 13 (aus dem finalen message_delta, kommt nur bei vollständigem Stream an)", result.OutputTokens)
	}
}

// TestSendStream_OpenAI_LongerThanScaledProbeTimeout_CompletesFully — Test 4
// laut Datei-Kopfkommentar: OpenAI (und stellvertretend Mistral, dieselbe
// streamOpenAICompatible-Funktion) über eine moderate, aber deutlich über
// jedem plausiblen kurz-skalierten Timeout liegende Dauer (~2s). Ergänzt
// TestSendStream_AnthropicOver12Seconds_CompletesFully um den zweiten
// Codepfad, ohne dessen Laufzeitkosten zu verdoppeln.
func TestSendStream_OpenAI_LongerThanScaledProbeTimeout_CompletesFully(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			time.Sleep(400 * time.Millisecond) // 5 × 400ms = 2s
			_, _ = io.WriteString(w, "data: "+fmt.Sprintf(`{"choices":[{"delta":{"content":"tok%d "}}]}`, i)+"\n\n")
			fl.Flush()
		}
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":5}}`+"\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	p := &OpenAIProvider{Client: defaultClient(), UA: "test/1.0", SendEndpoint: srv.URL}
	var textSeen strings.Builder
	start := time.Now()
	result, err := p.SendStream(context.Background(), "sk-test",
		SendRequest{Model: "gpt-test", UserPrompt: "Hi"},
		func(c StreamChunk) error {
			textSeen.WriteString(c.Text)
			return nil
		})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("SendStream nach %v abgebrochen: %v", elapsed, err)
	}
	if elapsed < 2*time.Second {
		t.Fatalf("Stream lief nur %v — Testaufbau liefert nicht die geplanten ~2s", elapsed)
	}
	if textSeen.Len() == 0 {
		t.Fatal("kein Text empfangen — Stream unvollständig")
	}
	if result.OutputTokens != 5 {
		t.Errorf("OutputTokens = %d, erwartet 5", result.OutputTokens)
	}
}

// TestDefaultClient_HeaderDeckelIstGesetzt — K-A4: Der Client trägt genau den
// AntwortHeaderDeckel (nicht mehr 20 s), und weiterhin keine Gesamtfrist.
func TestDefaultClient_HeaderDeckelIstGesetzt(t *testing.T) {
	c := defaultClient()
	if ausgang.Basis(c.Transport) == c.Transport {
		t.Fatalf("Transport %T trägt den Ausgangs-Schalter nicht (Karte #569)", c.Transport)
	}
	tr, ok := ausgang.Basis(c.Transport).(*http.Transport) // Karte #569: Schalter-Hülle
	if !ok {
		t.Fatalf("Transport ist %T, erwartet *http.Transport", c.Transport)
	}
	if tr.ResponseHeaderTimeout != AntwortHeaderDeckel {
		t.Fatalf("ResponseHeaderTimeout = %v, erwartet %v", tr.ResponseHeaderTimeout, AntwortHeaderDeckel)
	}
	if c.Timeout != 0 {
		t.Fatalf("Client.Timeout = %v, erwartet 0 (F-1126)", c.Timeout)
	}
}
