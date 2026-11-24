package llm

// vision_test.go — Bild-Anhänge: Capability, Grenzen, Serialisierung
// (#429, ADR-0121).

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSupportsVision(t *testing.T) {
	faelle := []struct {
		provider, model string
		want            bool
	}{
		{"anthropic", "claude-opus-4-7", true},
		{"anthropic", "claude-haiku-4-5", true},
		{"openai", "gpt-4o", true},
		{"openai", "o1", true},
		// Der Grund, warum die Prüfung modell- und nicht anbieterweit ist:
		// Ein Anbieter kann beides führen.
		{"openai", "o1-mini", false},
		{"google", "gemini-2.0-flash", true},
		// Mistrals Vision-Modell (Pixtral) steht nicht in der Liste — alle
		// drei geführten Modelle können keine Bilder.
		{"mistral", "mistral-large-latest", false},
		{"mistral", "mistral-small-latest", false},
		// Unbekanntes → false. Sichere Richtung: benennbare Ablehnung statt
		// Kosten für eine Anfrage, die der Anbieter zurückweist.
		{"anthropic", "claude-gibt-es-nicht", false},
		{"gibt-es-nicht", "gpt-4o", false},
		{"", "", false},
	}
	for _, f := range faelle {
		if got := SupportsVision(f.provider, f.model); got != f.want {
			t.Errorf("SupportsVision(%q, %q) = %v, erwartet %v", f.provider, f.model, got, f.want)
		}
	}
}

// Jedes Modell, das Vision kann, muss auch Preise haben — sonst zeigt die
// Oberfläche „Preise unbekannt" für genau die Modelle, bei denen Bilder die
// Kosten treiben.
func TestVisionModelleHabenPreise(t *testing.T) {
	for provider, tabelle := range AllPricing() {
		for modell, eintrag := range tabelle {
			if eintrag.Vision && eintrag.InputPerMillionUSD == 0 {
				t.Errorf("%s/%s kann Bilder, hat aber keinen Input-Preis", provider, modell)
			}
		}
	}
}

func TestPruefeBild(t *testing.T) {
	faelle := []struct {
		name     string
		mime     string
		groesse  int64
		wantHint string
	}{
		{name: "png ok", mime: "image/png", groesse: 1000},
		{name: "jpeg ok", mime: "image/jpeg", groesse: 1000},
		{name: "Grossschreibung ok", mime: "IMAGE/PNG", groesse: 1000},
		{name: "mit charset ok", mime: "image/png; charset=binary", groesse: 1000},
		{name: "PDF abgelehnt", mime: "application/pdf", groesse: 1000, wantHint: "kann nicht mitgeschickt werden"},
		{name: "SVG abgelehnt", mime: "image/svg+xml", groesse: 1000, wantHint: "kann nicht mitgeschickt werden"},
		{name: "leerer MIME abgelehnt", mime: "", groesse: 1000, wantHint: "ohne MIME-Typ"},
		{name: "nur Whitespace abgelehnt", mime: "   ", groesse: 1000, wantHint: "ohne MIME-Typ"},
		{name: "zu gross", mime: "image/png", groesse: MaxImageBytes + 1, wantHint: "Grenze fuer den Versand"},
		{name: "genau an der Grenze ok", mime: "image/png", groesse: MaxImageBytes},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			got := PruefeBild(f.mime, f.groesse)
			if f.wantHint == "" {
				if got != "" {
					t.Fatalf("unerwartete Ablehnung: %q", got)
				}
				return
			}
			if !strings.Contains(got, f.wantHint) {
				t.Fatalf("Grund = %q, erwartet Teilstring %q", got, f.wantHint)
			}
		})
	}
}

// Die Ablehnung wegen Grösse muss beide Zahlen nennen: Ein Anhang darf
// legitim 50 MB gross sein und trotzdem nicht mitgeschickt werden — ohne den
// Unterschied klingt es wie ein Upload-Fehler.
func TestZuGrossNenntDieGrenze(t *testing.T) {
	got := PruefeBild("image/png", MaxImageBytes*2)
	if !strings.Contains(got, "hochladen darf man mehr") {
		t.Fatalf("Meldung unterscheidet Upload- und Versandgrenze nicht: %q", got)
	}
}

func TestPruefeBildMenge(t *testing.T) {
	if got := PruefeBildMenge(MaxImagesPerSend, 100); got != "" {
		t.Fatalf("Anzahl an der Grenze abgelehnt: %q", got)
	}
	if got := PruefeBildMenge(MaxImagesPerSend+1, 100); !strings.Contains(got, "hoechstens") {
		t.Fatalf("zu viele Anhaenge nicht abgelehnt: %q", got)
	}
	if got := PruefeBildMenge(1, MaxImageBytesTotal+1); !strings.Contains(got, "zusammen") {
		t.Fatalf("Gesamtgroesse nicht geprueft: %q", got)
	}
	// Die Gesamtgrenze muss unter „alle Bilder in Maximalgrösse" liegen —
	// sonst prüft sie nichts, was die Einzelgrenze nicht schon prüft.
	if MaxImageBytesTotal >= MaxImageBytes*MaxImagesPerSend {
		t.Fatalf("MaxImageBytesTotal (%d) ist wirkungslos neben %d x %d",
			MaxImageBytesTotal, MaxImagesPerSend, MaxImageBytes)
	}
}

// ─── Provider-Serialisierung ────────────────────────────────────────────

func bildAnhang() []ImageAttachment {
	return []ImageAttachment{{ID: "a1", Filename: "screenshot.png", MIME: "image/png", Data: []byte{1, 2, 3}}}
}

// b64 der Testbytes — ausgeschrieben, damit der Erwartungswert nicht von
// derselben Funktion stammt, die geprüft wird.
const testBildB64 = "AQID"

func TestBildDatenSindKorrektKodiert(t *testing.T) {
	if got := base64.StdEncoding.EncodeToString([]byte{1, 2, 3}); got != testBildB64 {
		t.Fatalf("Testkonstante stimmt nicht: %q", got)
	}
}

func TestAnthropicBildImUserTurn(t *testing.T) {
	got := mustJSON(t, anthropicMessages(SendRequest{UserPrompt: "Was ist das?", Images: bildAnhang()}))
	want := `[{"role":"user","content":[` +
		`{"source":{"data":"AQID","media_type":"image/png","type":"base64"},"type":"image"},` +
		`{"text":"Was ist das?","type":"text"}]}]`
	if got != want {
		t.Fatalf("Anthropic-Bild:\n got: %s\nwant: %s", got, want)
	}
}

func TestOpenAIBildImUserTurn(t *testing.T) {
	got := mustJSON(t, openaiMessages(SendRequest{UserPrompt: "Was ist das?", Images: bildAnhang()}))
	want := `[{"role":"user","content":[` +
		`{"text":"Was ist das?","type":"text"},` +
		`{"image_url":{"url":"data:image/png;base64,AQID"},"type":"image_url"}]}]`
	if got != want {
		t.Fatalf("OpenAI-Bild:\n got: %s\nwant: %s", got, want)
	}
}

func TestGoogleBildImUserTurn(t *testing.T) {
	got := mustJSON(t, googleContents(SendRequest{UserPrompt: "Was ist das?", Images: bildAnhang()}))
	want := `[{"role":"user","parts":[` +
		`{"inline_data":{"mime_type":"image/png","data":"AQID"}},` +
		`{"text":"Was ist das?"}]}]`
	if got != want {
		t.Fatalf("Google-Bild:\n got: %s\nwant: %s", got, want)
	}
}

// Ohne Bilder bleibt der Draht, was er war — die Zusicherung aus #428 gilt
// nach #429 unverändert. Ein eigener Testfall, weil beide Karten dieselben
// Message-Bauer angefasst haben.
func TestBestandsdrahtAuchNachBildern(t *testing.T) {
	sr := SendRequest{System: "Sys", UserPrompt: "Frage"}
	if got := mustJSON(t, anthropicMessages(sr)); got != `[{"role":"user","content":"Frage"}]` {
		t.Fatalf("anthropic: %s", got)
	}
	if got := mustJSON(t, openaiMessages(sr)); got != `[{"role":"system","content":"Sys"},{"role":"user","content":"Frage"}]` {
		t.Fatalf("openai: %s", got)
	}
	if got := mustJSON(t, googleContents(sr)); got != `[{"role":"user","parts":[{"text":"Frage"}]}]` {
		t.Fatalf("google: %s", got)
	}
}

// Im Fetch-Loop wandert der User-Turn in die History — die Bilder müssen
// mitkommen, sonst sieht das Modell sie ab Runde 2 nicht mehr.
func TestBilderUeberlebenDenWegInDieHistory(t *testing.T) {
	sr := SendRequest{
		History: []HistoryMessage{{Role: "user", Content: "Frage", Images: bildAnhang()}},
	}
	for name, got := range map[string]string{
		"anthropic": mustJSON(t, anthropicMessages(sr)),
		"openai":    mustJSON(t, openaiMessages(sr)),
		"google":    mustJSON(t, googleContents(sr)),
	} {
		if !strings.Contains(got, testBildB64) {
			t.Errorf("%s: Bild fehlt in der History-Nachricht: %s", name, got)
		}
	}
}
