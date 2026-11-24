// tools.go — Mutations-Tool-Schema + Provider-Bridge (#300 Etappe 4, ADR-0096 §3+§4).
//
// Internes Schema: 5 erlaubte Mutationen, definiert als Strategy-Slice. Übersetzt
// pro Provider in deren native Tool-Use-/Function-Calling-API. Validate-Pfad
// greift NACH dem Stream auf jedem vom Provider gelieferten Tool-Call:
//
//   L1 Blocklist:     Karten-/Board-/Sprint-/Attachment-Delete & Transfer → niemals
//   L2 Cap:           max 5 Vorschläge pro Antwort (Caller-Verantwortung im Handler)
//   L3 Sanity:        set-field-Allowlist, extend-description ≤ 10 KB, etc.
//
// Apply-Pfad (Op-Bridge) ist Etappe 5 — diese Datei kennt keine Op-Inbox.

package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Andiministrator/aBoard/server/calendar"
)

// MaxMutationProposalsPerResponse (ADR-0096 §4 L2) — Cap der Vorschläge pro
// LLM-Antwort. Mehr → der Handler beendet den Stream mit too-many-mutations.
const MaxMutationProposalsPerResponse = 5

// MaxExtendDescriptionBytes (ADR-0096 §4 L3) — Größenlimit für
// extend-description-Appends. Verhindert Memory-Bloat durch LLM-Halluzination.
const MaxExtendDescriptionBytes = 10 * 1024 // 10 KB

// MutationTool — interne Beschreibung eines erlaubten LLM-Tools (ADR-0096 §3).
// Die Felder mappen direkt auf die Bridge-Funktionen unten (Anthropic/OpenAI/
// Google/Mistral). Description steht im LLM-Prompt — knapp, eindeutig, deutsch.
type MutationTool struct {
	Name        string
	Description string
	Schema      ToolSchema // JSON-Schema-äquivalent (type + properties + required)
}

// ToolSchema — minimaler JSON-Schema-Untermenge, die alle 4 Provider verstehen.
// "object" mit Properties + Required. Provider-Bridges serialisieren das in
// das jeweilige native Format (siehe Build*Tools).
type ToolSchema struct {
	Type       string                   // immer "object"
	Properties map[string]ToolParamSpec // pro Parameter
	Required   []string                 // Pflichtfelder
}

// ToolParamSpec — Typ + optionale Enum-Werte + Beschreibung eines Parameters.
type ToolParamSpec struct {
	Type        string // "string" | "boolean" | "number" | "any"
	Description string
	Enum        []string // optional
}

// SetFieldAllowedFields (ADR-0096 §4 L3) — Karten-Felder, die set-field
// setzen darf. id/displayId/owner/assignees/sprintId/columnId sind
// ausgeschlossen — Owner-/Move-Mutationen laufen über eigene Tools (oder
// gar nicht). Die Liste spiegelt CARD_FIELD_BLOCKLIST aus src/ops.js
// invers (set-field ist die WHITELIST, ops.js die BLACKLIST).
var SetFieldAllowedFields = []string{
	"qa", "critical", "blocked", "complexity", "effort",
	"dueDate", "startDate", "dueTime", "startTime", "priority", "status",
}

// MutationActionBlocklist (ADR-0096 §4 L1) — Tool-Namen, die selbst wenn ein
// Provider sie liefert NIEMALS exposed werden. Praxis: wir senden diese Tools
// ohnehin nicht an den Provider, aber Defense-in-depth deckt Fälle ab, wo das
// LLM trotzdem einen tool_use-Block mit einem dieser Namen halluziniert.
var MutationActionBlocklist = map[string]bool{
	"card.delete":       true,
	"card.transfer":     true,
	"sprint.delete":     true,
	"board.field":       true,
	"attachment.delete": true,
}

// MutationTools — die kanonische Liste der erlaubten LLM-Mutationen.
// Beim Streaming-Send wird sie pro Provider übersetzt (Build*Tools).
var MutationTools = []MutationTool{
	{
		Name:        "set-field",
		Description: "Setze ein einfaches Feld auf der Karte (qa, critical, blocked, complexity, effort, dueDate, startDate, dueTime, startTime, priority, status).",
		Schema: ToolSchema{
			Type: "object",
			Properties: map[string]ToolParamSpec{
				"field": {Type: "string", Description: "Feldname.", Enum: SetFieldAllowedFields},
				"value": {Type: "any", Description: "Neuer Wert. Boolean für qa/critical/blocked, String für complexity (einfach/mittel/hoch), Number für effort, ISO-Datum für dueDate/startDate, Uhrzeit HH:MM (24 h, Ortszeit) für dueTime/startTime — wirkt nur, wenn das zugehörige Datum gesetzt ist."},
			},
			Required: []string{"field", "value"},
		},
	},
	{
		Name:        "move",
		Description: "Verschiebe die Karte in eine andere Spalte (z. B. col-erledigt, col-in-arbeit).",
		Schema: ToolSchema{
			Type: "object",
			Properties: map[string]ToolParamSpec{
				"toColumnId": {Type: "string", Description: "Ziel-Spalten-ID."},
			},
			Required: []string{"toColumnId"},
		},
	},
	{
		Name:        "add-comment",
		Description: "Hänge einen Kommentar an die Karte an.",
		Schema: ToolSchema{
			Type: "object",
			Properties: map[string]ToolParamSpec{
				"text": {Type: "string", Description: "Kommentar-Text (Markdown)."},
			},
			Required: []string{"text"},
		},
	},
	{
		Name:        "extend-description",
		Description: "Hänge Markdown an die Karten-Beschreibung an (kein Überschreiben).",
		Schema: ToolSchema{
			Type: "object",
			Properties: map[string]ToolParamSpec{
				"appendMarkdown": {Type: "string", Description: "Markdown-Text, der unten an description angehängt wird (max 10 KB)."},
			},
			Required: []string{"appendMarkdown"},
		},
	},
	{
		Name:        "add-label",
		Description: "Füge der Karte ein Label hinzu.",
		Schema: ToolSchema{
			Type: "object",
			Properties: map[string]ToolParamSpec{
				"label": {Type: "string", Description: "Label-Name."},
			},
			Required: []string{"label"},
		},
	},
}

// Validation-Ergebnisse für Tool-Calls. Genau einer von Allowed/Blocked ist
// belegt — Allowed enthält die geparsten Args, Blocked den Reason.
type MutationValidation struct {
	Name    string
	ID      string // Provider-Tool-Call-ID (durchgereicht für Confirm-Round-Trip)
	Allowed bool
	Blocked bool
	Reason  string          // bei Blocked: menschenlesbarer Grund
	Args    json.RawMessage // bei Allowed: validierte Args (JSON-Form)
}

// ErrMutationCapExceeded — Stream-Handler übersetzt das in
// `event: error data: {"kind":"too-many-mutations","limit":5}`.
var ErrMutationCapExceeded = errors.New("llm: too many mutation proposals (cap 5)")

// ValidateMutation prüft einen vom Provider gelieferten Tool-Call gegen
// Blocklist + Schema (ADR-0096 §4 L1+L3). Cap (L2) ist Caller-Pflicht (siehe
// MaxMutationProposalsPerResponse).
//
// validColumns ist die Menge gültiger Spalten-IDs (aus board.columnDefs) für
// die move-Ziel-Prüfung (F-410 / ADR-0096 §3 Schritt 4). Leer/nil → der
// Spalten-Check wird übersprungen (Board-Kontext unbekannt); der Client-Guard
// in src/llm-mutations.js bleibt die harte Schranke vor dem Apply.
//
// Returnwert ist immer non-nil; bei verworfenen Vorschlägen ist Blocked=true
// gesetzt — der Caller schickt das als „blocked: true"-Event an den Client
// (fail-loud, ADR-0096 §4).
func ValidateMutation(name, id string, argsRaw json.RawMessage, validColumns map[string]bool) MutationValidation {
	v := MutationValidation{Name: name, ID: id}

	// L1: Blocklist — Defense-in-depth, falls das LLM einen verbotenen
	// Action-Namen halluziniert (wir senden diese Tools nicht).
	if MutationActionBlocklist[name] {
		v.Blocked = true
		v.Reason = fmt.Sprintf("action %q ist via LLM nicht erlaubt (Blocklist)", name)
		return v
	}

	// Tool muss in der erlaubten Liste sein.
	var tool *MutationTool
	for i := range MutationTools {
		if MutationTools[i].Name == name {
			tool = &MutationTools[i]
			break
		}
	}
	if tool == nil {
		v.Blocked = true
		v.Reason = fmt.Sprintf("unbekannter Tool-Name %q", name)
		return v
	}

	// Args müssen sich als JSON-Objekt parsen lassen.
	var args map[string]any
	if len(argsRaw) == 0 {
		v.Blocked = true
		v.Reason = "leere Args"
		return v
	}
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		v.Blocked = true
		v.Reason = fmt.Sprintf("Args sind kein JSON-Objekt: %v", err)
		return v
	}

	// Required-Check.
	for _, req := range tool.Schema.Required {
		if _, ok := args[req]; !ok {
			v.Blocked = true
			v.Reason = fmt.Sprintf("Pflicht-Argument %q fehlt", req)
			return v
		}
	}

	// L3: Per-Tool Argument-Sanity.
	switch name {
	case "set-field":
		field, _ := args["field"].(string)
		if field == "" {
			v.Blocked = true
			v.Reason = "set-field: field-Argument muss ein nicht-leerer String sein"
			return v
		}
		ok := false
		for _, allowed := range SetFieldAllowedFields {
			if allowed == field {
				ok = true
				break
			}
		}
		if !ok {
			v.Blocked = true
			v.Reason = fmt.Sprintf("set-field: Feld %q ist nicht in der Allowlist (erlaubt: %v)", field, SetFieldAllowedFields)
			return v
		}
		// F-412: Der FELDNAME war bisher geprüft, der WERT nicht. Ein Modell,
		// das `set-field(complexity, "sehr hoch")` oder `set-field(qa, "ja")`
		// vorschlägt, kam damit durch — und schrieb einen Wert ins Board, den
		// weder die UI noch die Velocity-Berechnung deuten kann. Die Allowlist
		// beantwortet „welches Feld", nicht „welcher Wert".
		if grund := pruefeSetFieldWert(field, args["value"]); grund != "" {
			v.Blocked = true
			v.Reason = "set-field: " + grund
			return v
		}
	case "move":
		col, _ := args["toColumnId"].(string)
		if col == "" {
			v.Blocked = true
			v.Reason = "move: toColumnId muss ein nicht-leerer String sein"
			return v
		}
		// F-410 (ADR-0096 §3 Schritt 4 / §4 L3): Ziel-Spalte muss existieren —
		// sonst verschwindet die Karte aus allen gerenderten Spalten.
		if len(validColumns) > 0 && !validColumns[col] {
			v.Blocked = true
			v.Reason = fmt.Sprintf("move: Ziel-Spalte %q existiert nicht", col)
			return v
		}
	case "add-comment":
		text, _ := args["text"].(string)
		if text == "" {
			v.Blocked = true
			v.Reason = "add-comment: text muss ein nicht-leerer String sein"
			return v
		}
	case "extend-description":
		md, _ := args["appendMarkdown"].(string)
		if md == "" {
			v.Blocked = true
			v.Reason = "extend-description: appendMarkdown muss ein nicht-leerer String sein"
			return v
		}
		if len(md) > MaxExtendDescriptionBytes {
			v.Blocked = true
			v.Reason = fmt.Sprintf("extend-description: appendMarkdown %d B > Limit %d B", len(md), MaxExtendDescriptionBytes)
			return v
		}
	case "add-label":
		label, _ := args["label"].(string)
		if label == "" {
			v.Blocked = true
			v.Reason = "add-label: label muss ein nicht-leerer String sein"
			return v
		}
	}

	// Args neu marshallen, damit der Client das kanonisierte Format bekommt
	// (gleiche Schlüssel-Reihenfolge, keine Whitespace-Drift). Fallback:
	// Original-Bytes, wenn das Re-Marshal fehlschlägt (sollte nie passieren).
	cleaned, err := json.Marshal(args)
	if err != nil {
		cleaned = argsRaw
	}
	v.Allowed = true
	v.Args = cleaned
	return v
}

// ── Provider-Bridges ────────────────────────────────────────────────────────

// AnthropicToolSpec — Form, in der Anthropic Tools entgegennimmt
// (https://docs.anthropic.com/en/docs/build-with-claude/tool-use):
// `tools: [{name, description, input_schema}]`.
type AnthropicToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// BuildAnthropicTools übersetzt eine Werkzeug-Liste in Anthropic-Schema. Der
// `input_schema` ist JSON-Schema (type:object + properties + required).
//
// Die Liste kommt seit #428 von SelectTools (Mutations- und/oder Lese-
// Werkzeuge) statt fest aus MutationTools — welche Werkzeuge ein Send
// mitschickt, entscheidet der Aufrufer, nicht die Bridge.
func BuildAnthropicTools(tools []MutationTool) []AnthropicToolSpec {
	out := make([]AnthropicToolSpec, 0, len(tools))
	for _, t := range tools {
		out = append(out, AnthropicToolSpec{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: jsonSchemaFromToolSchema(t.Schema),
		})
	}
	return out
}

// OpenAIToolSpec — OpenAI-Form (OpenAI/Mistral identisch):
// `tools: [{type:"function", function:{name, description, parameters}}]`.
type OpenAIToolSpec struct {
	Type     string             `json:"type"`
	Function OpenAIToolFunction `json:"function"`
}
type OpenAIToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// BuildOpenAITools — auch von Mistral verwendet (gleicher Vertrag).
func BuildOpenAITools(tools []MutationTool) []OpenAIToolSpec {
	out := make([]OpenAIToolSpec, 0, len(tools))
	for _, t := range tools {
		out = append(out, OpenAIToolSpec{
			Type: "function",
			Function: OpenAIToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  jsonSchemaFromToolSchema(t.Schema),
			},
		})
	}
	return out
}

// BuildMistralTools — Mistral-Tool-Calling ist OpenAI-kompatibel.
func BuildMistralTools(tools []MutationTool) []OpenAIToolSpec { return BuildOpenAITools(tools) }

// GoogleToolSpec — Google Function Calling:
// `tools: [{function_declarations: [{name, description, parameters}]}]`.
type GoogleToolSpec struct {
	FunctionDeclarations []GoogleFunctionDecl `json:"function_declarations"`
}
type GoogleFunctionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// BuildGoogleTools — Google packt alle Declarations in EINEN tools-Block
// (anders als die anderen 3 Provider, die ein Array von Tools verwenden).
func BuildGoogleTools(tools []MutationTool) []GoogleToolSpec {
	decls := make([]GoogleFunctionDecl, 0, len(tools))
	for _, t := range tools {
		decls = append(decls, GoogleFunctionDecl{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  jsonSchemaFromToolSchema(t.Schema),
		})
	}
	return []GoogleToolSpec{{FunctionDeclarations: decls}}
}

// jsonSchemaFromToolSchema — gemeinsame JSON-Schema-Serialisierung für alle
// vier Provider-Bridges. Übersetzt unseren ToolSchema in ein
// JSON-Schema-Object. Unknown types (z. B. "any") werden weggelassen — Provider
// akzeptieren leere type-Felder, der LLM rät dann den Typ aus der Description.
func jsonSchemaFromToolSchema(s ToolSchema) map[string]any {
	props := make(map[string]any, len(s.Properties))
	for k, p := range s.Properties {
		spec := map[string]any{}
		if p.Type != "" && p.Type != "any" {
			spec["type"] = p.Type
		}
		if p.Description != "" {
			spec["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			spec["enum"] = p.Enum
		}
		props[k] = spec
	}
	out := map[string]any{
		"type":       s.Type,
		"properties": props,
	}
	if len(s.Required) > 0 {
		out["required"] = s.Required
	}
	return out
}

// setFieldEnums nennt die zulässigen Werte für die Felder, die eine feste
// Auswahl haben. Die Listen sind NICHT erfunden, sondern aus den <option>-
// Elementen in `src/modal.js` abgelesen: complexity bei :2114-2115, priority
// bei :2079. Der Leerstring steht bei complexity für „unbekannt" und ist dort
// eine echte Auswahl; bei priority ist er kein UI-Wert, kommt aber im Bestand
// vor — ihn abzulehnen hiesse, einer Karte den Rückweg zu verbauen.
//
// `status` fehlt hier ABSICHTLICH. Das Feld existiert im Modell
// (`src/model.js`: Default `'offen'`), hat aber **kein Bedienelement** — es
// gibt keine Stelle, an der ein Mensch es setzt, und damit keine belastbare
// Werteliste. Im Bestand stehen `”`, `'offen'` und `'erledigt'`. Eine hier
// erfundene Aufzählung würde entweder legitime Altwerte blockieren oder
// falsche durchlassen; beides ist schlechter als die reine Typprüfung, die
// pruefeSetFieldWert für status vornimmt. Wer dem Feld eine Semantik gibt,
// trägt die Liste hier nach.
//
// Wer hier etwas ergänzt, muss es auch im Frontend (`src/llm-mutations.js`)
// tun — die Listen sind bewusst gespiegelt, weil Server- und Browser-Pfad
// unabhängig voneinander erreichbar sind.
var setFieldEnums = map[string][]string{
	"complexity": {"", "einfach", "mittel", "hoch"},
	"priority":   {"", "niedrig", "mittel", "hoch"},
}

// pruefeSetFieldWert validiert den WERT einer set-field-Mutation gegen den Typ,
// den das Feld tatsächlich trägt (F-412). Rückgabe "" heisst: in Ordnung.
//
// Warum das eine eigene Prüfung braucht: Die Allowlist in SetFieldAllowedFields
// beantwortet nur „darf dieses Feld überhaupt gesetzt werden". Ein Modell, das
// den richtigen Feldnamen mit einem unsinnigen Wert kombiniert — `qa: "ja"`,
// `effort: "ein halber Tag"`, `complexity: "sehr hoch"` —, kam bis F-412 durch.
// Das Ergebnis steht danach im Board und ist von dort schwer wieder
// herauszubekommen: die UI zeigt es an, die Velocity-Berechnung stolpert
// darüber, und niemand weiss mehr, ob es ein Mensch oder ein Modell war.
//
// JSON-Zahlen kommen aus encoding/json immer als float64 an — deshalb wird
// hier auf float64 geprüft und nicht auf int.
func pruefeSetFieldWert(field string, wert any) string {
	switch field {
	case "qa", "critical", "blocked":
		if _, ok := wert.(bool); !ok {
			return fmt.Sprintf("%s erwartet true oder false, bekam %s", field, typName(wert))
		}
	case "effort":
		// null ist gültig und bedeutet „nicht geschätzt" — das ist ein
		// legitimer Wunsch („die Schätzung stimmt nicht, nimm sie raus").
		if wert == nil {
			return ""
		}
		f, ok := wert.(float64)
		if !ok {
			return fmt.Sprintf("effort erwartet eine Zahl (Stunden) oder null, bekam %s", typName(wert))
		}
		if f < 0 {
			return fmt.Sprintf("effort erwartet eine Zahl >= 0, bekam %v", f)
		}
	case "dueDate", "startDate":
		// null loescht das Datum — gültig.
		if wert == nil {
			return ""
		}
		str, ok := wert.(string)
		if !ok {
			return fmt.Sprintf("%s erwartet ein ISO-Datum YYYY-MM-DD oder null, bekam %s", field, typName(wert))
		}
		if str == "" {
			return "" // Leerstring ist die zweite Schreibweise für „kein Datum"
		}
		// Strikt: time.Parse mit "2006-01-02" lehnt sowohl das Format als auch
		// unmögliche Daten (2026-02-31) ab. Ein Regex täte nur das Erste.
		if _, err := time.Parse("2006-01-02", str); err != nil {
			return fmt.Sprintf("%s erwartet ein ISO-Datum YYYY-MM-DD, bekam %q", field, str)
		}
	case "dueTime", "startTime":
		// ADR-0138 §1: "HH:MM" (24 h); null oder "" löscht die Uhrzeit.
		if wert == nil {
			return ""
		}
		str, ok := wert.(string)
		if !ok {
			return fmt.Sprintf("%s erwartet eine Uhrzeit HH:MM (24 h) oder null, bekam %s", field, typName(wert))
		}
		if str != "" && !calendar.IstUhrzeit(str) {
			return fmt.Sprintf("%s erwartet eine Uhrzeit HH:MM (24 h) oder null, bekam %q", field, str)
		}
	case "complexity", "priority", "status":
		str, ok := wert.(string)
		if !ok {
			return fmt.Sprintf("%s erwartet einen String, bekam %s", field, typName(wert))
		}
		erlaubt, hatEnum := setFieldEnums[field]
		if !hatEnum {
			// status: nur Typprüfung, keine Werteliste — siehe setFieldEnums.
			return ""
		}
		for _, e := range erlaubt {
			if e == str {
				return ""
			}
		}
		return fmt.Sprintf("%s erwartet einen der Werte %v, bekam %q", field, erlaubt, str)
	}
	return ""
}

// typName benennt den vorgefundenen JSON-Typ in der Fehlermeldung. Die
// Meldung geht an das Modell zurück; „bekam String" ist eine Auskunft, mit der
// es den nächsten Versuch korrigieren kann — „ungültiger Wert" nicht.
func typName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "Boolean"
	case float64:
		return "Zahl"
	case string:
		return "String"
	case []any:
		return "Liste"
	case map[string]any:
		return "Objekt"
	}
	return "unbekannten Typ"
}
