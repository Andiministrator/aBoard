package llm

// context_test.go — Karten-zu-Prompt-Serialisierung (#299, ADR-0095 §2).
//
// Tests prüfen Determinismus, Sektions-Reihenfolge, Allowlist-Filterung
// und Leer-Sektion-Eliminierung.

import (
	"strings"
	"testing"
)

func sampleCard() map[string]any {
	return map[string]any{
		"id":           "uuid-1",
		"displayId":    float64(42),
		"title":        "Beispiel-Karte",
		"description":  "Beschreibungs-Markdown.\nMit Mehrzeile.",
		"agentContext": "## Relevante Dateien\n- foo.go:1-10",
		"comments": []any{
			map[string]any{"id": "c1", "author": "Andi", "created": "2026-05-23", "text": "Erster Kommentar."},
			map[string]any{"id": "c2", "author": "Claude", "created": "2026-05-23", "text": "Zweiter\nmit Newline."},
		},
		"links": []any{
			map[string]any{"type": "blocks", "targetId": "uuid-ref-1"},
			map[string]any{"type": "related", "targetId": "uuid-ref-2"},
		},
		"activity": []any{
			map[string]any{"ts": "2026-05-23T10:00:00Z", "actor": "Andi", "action": "owner-changed", "from": "", "to": "Claude"},
			map[string]any{"ts": "2026-05-23T11:00:00Z", "actor": "Claude", "action": "qa-set", "from": "false", "to": "true"},
		},
	}
}

func sampleRefs() map[string]RefSummary {
	return map[string]RefSummary{
		"uuid-ref-1": {DisplayID: 100, Title: "Verlinkte Karte 1", Snippet: "Snippet-Text"},
		"uuid-ref-2": {DisplayID: 200, Title: "Verlinkte Karte 2", Snippet: ""},
	}
}

func TestBuildPrompt_AllFields(t *testing.T) {
	out := BuildPrompt(sampleCard(), []string{"description", "comments", "links", "agentContext", "activity"}, sampleRefs())

	// Header
	if !strings.Contains(out, "# Karte #42: Beispiel-Karte") {
		t.Errorf("Header fehlt: %q", out)
	}
	// Sektionen vorhanden
	for _, header := range []string{"## Beschreibung", "## Kommentare", "## Verweise", "## Agent-Kontext", "## Aktivität"} {
		if !strings.Contains(out, header) {
			t.Errorf("Sektion %q fehlt", header)
		}
	}
	// Reihenfolge (description vor comments vor links vor agentContext vor activity)
	idxDesc := strings.Index(out, "## Beschreibung")
	idxComm := strings.Index(out, "## Kommentare")
	idxLinks := strings.Index(out, "## Verweise")
	idxAgent := strings.Index(out, "## Agent-Kontext")
	idxAct := strings.Index(out, "## Aktivität")
	if !(idxDesc < idxComm && idxComm < idxLinks && idxLinks < idxAgent && idxAgent < idxAct) {
		t.Errorf("Sektion-Reihenfolge falsch: desc=%d comm=%d links=%d agent=%d act=%d", idxDesc, idxComm, idxLinks, idxAgent, idxAct)
	}
	// Comments: Mehrzeilen-Einrückung
	if !strings.Contains(out, "- [2026-05-23 Claude]: Zweiter\n  mit Newline.") {
		t.Errorf("Mehrzeilen-Comment-Einrückung fehlt: %q", out)
	}
	// Links: Display-ID + Titel
	if !strings.Contains(out, "blocks: #100 \"Verlinkte Karte 1\" — Snippet-Text") {
		t.Errorf("Link-Zeile falsch: %q", out)
	}
}

func TestBuildPrompt_Determinism(t *testing.T) {
	// Gleiche Eingabe → gleicher Output (kein Map-Iteration-Random).
	card := sampleCard()
	refs := sampleRefs()
	a := BuildPrompt(card, []string{"description", "comments", "links"}, refs)
	b := BuildPrompt(card, []string{"description", "comments", "links"}, refs)
	if a != b {
		t.Errorf("Output nicht deterministisch:\nA=%q\nB=%q", a, b)
	}
}

func TestBuildPrompt_Allowlist_IgnoresUnknown(t *testing.T) {
	out := BuildPrompt(sampleCard(), []string{"description", "bogus", "comments"}, sampleRefs())
	if strings.Contains(out, "bogus") {
		t.Errorf("Unbekanntes Feld 'bogus' im Output: %q", out)
	}
	if !strings.Contains(out, "## Beschreibung") || !strings.Contains(out, "## Kommentare") {
		t.Errorf("Erlaubte Felder fehlen: %q", out)
	}
}

func TestBuildPrompt_EmptySection_Omitted(t *testing.T) {
	card := map[string]any{
		"title":       "X",
		"description": "",
		"comments":    []any{},
	}
	out := BuildPrompt(card, []string{"description", "comments"}, nil)
	if strings.Contains(out, "## Beschreibung") {
		t.Errorf("Leere Beschreibung soll nicht erscheinen: %q", out)
	}
	if strings.Contains(out, "## Kommentare") {
		t.Errorf("Leere Kommentare sollen nicht erscheinen: %q", out)
	}
}

func TestBuildPrompt_NoFields_OnlyHeader(t *testing.T) {
	out := BuildPrompt(map[string]any{"displayId": float64(7), "title": "Nur Header"}, nil, nil)
	if !strings.Contains(out, "# Karte #7: Nur Header") {
		t.Errorf("Header fehlt: %q", out)
	}
	// Keine Sektion-Header
	for _, header := range []string{"## Beschreibung", "## Kommentare", "## Verweise", "## Agent-Kontext", "## Aktivität"} {
		if strings.Contains(out, header) {
			t.Errorf("Sektion %q ohne includeFields: %q", header, out)
		}
	}
}

func TestBuildPrompt_LinksStableOrder(t *testing.T) {
	// Mehrere Links in random Reihenfolge → sortiert nach (type, targetId).
	card := map[string]any{
		"title": "x",
		"links": []any{
			map[string]any{"type": "related", "targetId": "z"},
			map[string]any{"type": "blocks", "targetId": "a"},
			map[string]any{"type": "blocks", "targetId": "b"},
			map[string]any{"type": "related", "targetId": "y"},
		},
	}
	refs := map[string]RefSummary{
		"a": {DisplayID: 1, Title: "A"},
		"b": {DisplayID: 2, Title: "B"},
		"y": {DisplayID: 3, Title: "Y"},
		"z": {DisplayID: 4, Title: "Z"},
	}
	out := BuildPrompt(card, []string{"links"}, refs)
	idxBlocksA := strings.Index(out, "blocks: #1")
	idxBlocksB := strings.Index(out, "blocks: #2")
	idxRelY := strings.Index(out, "related: #3")
	idxRelZ := strings.Index(out, "related: #4")
	if !(idxBlocksA < idxBlocksB && idxBlocksB < idxRelY && idxRelY < idxRelZ) {
		t.Errorf("Link-Reihenfolge falsch: A=%d B=%d Y=%d Z=%d", idxBlocksA, idxBlocksB, idxRelY, idxRelZ)
	}
}

func TestTruncateSnippet_RuneSafe(t *testing.T) {
	// UTF-8: deutsche Umlaute zählen als 1 Rune, nicht 2 Bytes.
	s := strings.Repeat("ä", 250) // 250 runes, 500 bytes
	got := TruncateSnippet(s)
	gotRunes := []rune(got)
	if len(gotRunes) > MaxRefSnippetLen {
		t.Errorf("TruncateSnippet überschreitet MaxRefSnippetLen: %d runes", len(gotRunes))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("Truncated string sollte mit … enden: %q", got[len(got)-3:])
	}
}

func TestTruncateSnippet_ShortStringUnchanged(t *testing.T) {
	s := "Kurzer Text"
	if got := TruncateSnippet(s); got != s {
		t.Errorf("Kurzer String soll unverändert sein, got %q", got)
	}
}
