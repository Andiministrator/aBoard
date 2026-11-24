package llm

// context.go — Karten-zu-Prompt-Serialisierung (#299, ADR-0095 §2).
//
// Konvertiert eine aBoard-Karte (raw map[string]any aus state_json) in
// deterministisches Markdown. Reihenfolge fest: description → comments →
// links → agentContext → activity. Auswahl per includeFields-Allowlist.
//
// Determinismus ist Pflicht: gleiche Eingabe → gleicher Output (kein
// Time-Now, kein Map-Iteration-Random-Order). Erleichtert Bit-stabile Tests
// und reproduzierbare LLM-Calls.

import (
	"fmt"
	"sort"
	"strings"
)

// AllowedFields ist die Allowlist erlaubter includeFields-Werte (ADR-0095 §2).
// Andere Werte werden im BuildPrompt ignoriert.
var AllowedFields = map[string]bool{
	"description":  true,
	"comments":     true,
	"links":        true,
	"agentContext": true,
	"activity":     true,
}

// RefSummary bündelt die Minimal-Information über eine verlinkte Karte
// (ADR-0095 §2: Titel + 200-Zeichen-Snippet, nicht volle Karte).
type RefSummary struct {
	DisplayID int // optional; 0 → weglassen, nur Titel
	Title     string
	Snippet   string // ggf. gekürzt; siehe TruncateSnippet
}

// MaxRefSnippetLen — siehe ADR-0095 §2. 200 Zeichen reicht für „Worum geht's"
// ohne den Token-Budget zu sprengen.
const MaxRefSnippetLen = 200

// TruncateSnippet kürzt einen Beschreibungs-Text auf MaxRefSnippetLen
// Codepoints (rune-safe). Vor allem für Refs-Listen genutzt.
func TruncateSnippet(s string) string {
	runes := []rune(s)
	if len(runes) <= MaxRefSnippetLen {
		return s
	}
	return string(runes[:MaxRefSnippetLen-1]) + "…"
}

// PromptOptions steuert Zusätze, die nicht jeder Send braucht (#428).
type PromptOptions struct {
	// MitKartenIDs hängt an jede Zeile der Verweis-Sektion die interne id der
	// Zielkarte. Nur nötig, wenn das Lese-Werkzeug fetch-related-card aktiv
	// ist (ADR-0120): Dessen Argument IST diese id, und ohne sie im Prompt
	// kann das Modell das Werkzeug gar nicht bedienen — es kennt die id
	// nicht und kann sie auch nicht raten.
	//
	// Standardmäßig aus, damit der Prompt jedes gewöhnlichen Sends
	// unverändert bleibt: Die ids kosten Tokens und helfen dort niemandem.
	MitKartenIDs bool
}

// BuildPrompt baut den User-Prompt für den LLM-Send (#299, ADR-0095 §2).
// Ohne Optionen — der Bestandspfad.
//
// card ist die Karten-Map aus state_json (siehe AddCommentToCard für das
// Schema — id/title/displayId/description/comments/links/agentContext/activity).
// includeFields ist die User-Auswahl (Frontend-Checkboxen); unbekannte Werte
// werden ignoriert. refsByID enthält Summaries der Zielkarten (für 'links'-
// Kontext) — der Aufrufer (Endpoint) hat sie vorab aus dem Board geladen.
//
// Reihenfolge der Sektionen ist fest (ADR-0095 §2):
//
//	description → comments → links → agentContext → activity.
//
// Felder ohne Inhalt werden komplett weggelassen (kein leerer Header).
func BuildPrompt(card map[string]any, includeFields []string, refsByID map[string]RefSummary) string {
	return BuildPromptOpts(card, includeFields, refsByID, PromptOptions{})
}

// BuildPromptOpts ist BuildPrompt mit Zusätzen. Siehe PromptOptions.
func BuildPromptOpts(card map[string]any, includeFields []string, refsByID map[string]RefSummary, opts PromptOptions) string {
	selected := normalizeFields(includeFields)

	var sb strings.Builder

	// Header: # Karte #<displayId>: <title>
	header := buildHeader(card)
	if header != "" {
		sb.WriteString(header)
		sb.WriteString("\n\n")
	}

	if selected["description"] {
		if desc := asString(card["description"]); strings.TrimSpace(desc) != "" {
			sb.WriteString("## Beschreibung\n\n")
			sb.WriteString(strings.TrimRight(desc, "\n"))
			sb.WriteString("\n\n")
		}
	}

	if selected["comments"] {
		if section := buildCommentsSection(card); section != "" {
			sb.WriteString(section)
		}
	}

	if selected["links"] {
		if section := buildLinksSection(card, refsByID, opts.MitKartenIDs); section != "" {
			sb.WriteString(section)
		}
	}

	if selected["agentContext"] {
		if ag := asString(card["agentContext"]); strings.TrimSpace(ag) != "" {
			sb.WriteString("## Agent-Kontext\n\n")
			sb.WriteString(strings.TrimRight(ag, "\n"))
			sb.WriteString("\n\n")
		}
	}

	if selected["activity"] {
		if section := buildActivitySection(card); section != "" {
			sb.WriteString(section)
		}
	}

	return strings.TrimRight(sb.String(), "\n") + "\n"
}

// normalizeFields filtert auf AllowedFields-Werte. Stabile Output-Ordnung
// kommt aus dem festen Reihenfolge-Code in BuildPrompt; hier reicht ein Set.
func normalizeFields(in []string) map[string]bool {
	out := map[string]bool{}
	for _, f := range in {
		if AllowedFields[f] {
			out[f] = true
		}
	}
	return out
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// asFloat liest sowohl float64 (json.Unmarshal default) als auch int (aus
// programmatischen Tests) ab.
func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

func buildHeader(card map[string]any) string {
	title := strings.TrimSpace(asString(card["title"]))
	if title == "" {
		title = "(ohne Titel)"
	}
	if id, ok := asFloat(card["displayId"]); ok && id > 0 {
		return fmt.Sprintf("# Karte #%d: %s", int(id), title)
	}
	return "# Karte: " + title
}

func buildCommentsSection(card map[string]any) string {
	arr, ok := card["comments"].([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Kommentare\n\n")
	for _, c := range arr {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		date := asString(cm["created"])
		author := asString(cm["author"])
		text := strings.TrimSpace(asString(cm["text"]))
		if text == "" {
			continue
		}
		// Format: - [<date> <author>]: <text>
		sb.WriteString("- [")
		if date != "" {
			sb.WriteString(date)
		}
		if author != "" {
			if date != "" {
				sb.WriteString(" ")
			}
			sb.WriteString(author)
		}
		sb.WriteString("]: ")
		// Mehrzeilen-Kommentare einrücken, damit der Listen-Bullet korrekt parst.
		text = strings.ReplaceAll(text, "\n", "\n  ")
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

func buildLinksSection(card map[string]any, refsByID map[string]RefSummary, mitIDs bool) string {
	arr, ok := card["links"].([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	// Stabile Output-Reihenfolge: nach (type, targetId) sortieren.
	type linkRow struct {
		Type     string
		TargetID string
	}
	var rows []linkRow
	for _, l := range arr {
		lm, ok := l.(map[string]any)
		if !ok {
			continue
		}
		t := asString(lm["type"])
		id := asString(lm["targetId"])
		if t == "" || id == "" {
			continue
		}
		rows = append(rows, linkRow{Type: t, TargetID: id})
	}
	if len(rows) == 0 {
		return ""
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Type != rows[j].Type {
			return rows[i].Type < rows[j].Type
		}
		return rows[i].TargetID < rows[j].TargetID
	})

	var sb strings.Builder
	sb.WriteString("## Verweise\n\n")
	for _, row := range rows {
		ref := refsByID[row.TargetID]
		title := strings.TrimSpace(ref.Title)
		if title == "" {
			title = "(unbekannte Karte)"
		}
		sb.WriteString("- ")
		sb.WriteString(row.Type)
		sb.WriteString(": ")
		if ref.DisplayID > 0 {
			sb.WriteString(fmt.Sprintf("#%d ", ref.DisplayID))
		}
		sb.WriteString("\"")
		sb.WriteString(title)
		sb.WriteString("\"")
		if snippet := strings.TrimSpace(ref.Snippet); snippet != "" {
			sb.WriteString(" — ")
			sb.WriteString(TruncateSnippet(snippet))
		}
		// Die interne id nur bei aktivem Lese-Werkzeug (ADR-0120). Sie steht
		// am Zeilenende in Klammern, damit die Zeile für einen Menschen
		// weiterhin vorne lesbar ist.
		if mitIDs {
			sb.WriteString(" (id: ")
			sb.WriteString(row.TargetID)
			sb.WriteString(")")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

func buildActivitySection(card map[string]any) string {
	arr, ok := card["activity"].([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Aktivität\n\n")
	for _, a := range arr {
		am, ok := a.(map[string]any)
		if !ok {
			continue
		}
		ts := asString(am["ts"])
		author := asString(am["actor"])
		action := asString(am["action"])
		from := asString(am["from"])
		to := asString(am["to"])
		if action == "" {
			continue
		}
		sb.WriteString("- [")
		if ts != "" {
			sb.WriteString(ts)
		}
		if author != "" {
			if ts != "" {
				sb.WriteString(" ")
			}
			sb.WriteString(author)
		}
		sb.WriteString("] ")
		sb.WriteString(action)
		if from != "" || to != "" {
			sb.WriteString(": ")
			sb.WriteString(from)
			sb.WriteString(" → ")
			sb.WriteString(to)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	return sb.String()
}
