package llm

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestValidateMutation_Allowlist(t *testing.T) {
	cases := []struct {
		name string
		args string
		ok   bool
		why  string // bei !ok: Substring im Reason
	}{
		{"set-field", `{"field":"qa","value":true}`, true, ""},
		{"set-field", `{"field":"critical","value":true}`, true, ""},
		{"set-field", `{"field":"complexity","value":"hoch"}`, true, ""},
		{"move", `{"toColumnId":"col-erledigt"}`, true, ""},
		{"add-comment", `{"text":"hallo"}`, true, ""},
		{"extend-description", `{"appendMarkdown":"## foo"}`, true, ""},
		{"add-label", `{"label":"bug"}`, true, ""},

		// L1 Blocklist
		{"card.delete", `{}`, false, "Blocklist"},
		{"card.transfer", `{"toBoardId":"x"}`, false, "Blocklist"},
		{"sprint.delete", `{}`, false, "Blocklist"},
		{"board.field", `{"field":"title"}`, false, "Blocklist"},
		{"attachment.delete", `{}`, false, "Blocklist"},

		// Unbekanntes Tool
		{"hallucinated-tool", `{}`, false, "unbekannter Tool-Name"},

		// Required fehlt
		{"set-field", `{"field":"qa"}`, false, "Pflicht-Argument \"value\" fehlt"},
		{"set-field", `{"value":true}`, false, "Pflicht-Argument \"field\" fehlt"},
		{"move", `{}`, false, "Pflicht-Argument \"toColumnId\" fehlt"},
		{"add-comment", `{}`, false, "Pflicht-Argument \"text\" fehlt"},

		// L3 Sanity
		{"set-field", `{"field":"id","value":"foo"}`, false, "Allowlist"},
		{"set-field", `{"field":"displayId","value":1}`, false, "Allowlist"},
		{"set-field", `{"field":"owner","value":"x"}`, false, "Allowlist"},
		{"set-field", `{"field":"","value":1}`, false, "nicht-leerer String"},
		{"move", `{"toColumnId":""}`, false, "nicht-leerer String"},
		{"add-comment", `{"text":""}`, false, "nicht-leerer String"},
		{"add-label", `{"label":""}`, false, "nicht-leerer String"},
		{"extend-description", `{"appendMarkdown":""}`, false, "nicht-leerer String"},

		// Args nicht parsebar
		{"set-field", `not json`, false, "kein JSON-Objekt"},
		{"set-field", ``, false, "leere Args"},
	}
	for _, c := range cases {
		t.Run(c.name+"_"+c.args, func(t *testing.T) {
			v := ValidateMutation(c.name, "tc1", json.RawMessage(c.args), nil)
			if v.Allowed != c.ok {
				t.Fatalf("Allowed=%v, want %v (Reason=%q)", v.Allowed, c.ok, v.Reason)
			}
			if !c.ok && c.why != "" && !strings.Contains(v.Reason, c.why) {
				t.Fatalf("Reason=%q, want substring %q", v.Reason, c.why)
			}
			if c.ok && len(v.Args) == 0 {
				t.Fatalf("Allowed=true but Args is empty")
			}
		})
	}
}

func TestValidateMutation_ExtendDescription_SizeLimit(t *testing.T) {
	big := strings.Repeat("x", MaxExtendDescriptionBytes+1)
	body, _ := json.Marshal(map[string]any{"appendMarkdown": big})
	v := ValidateMutation("extend-description", "tc1", body, nil)
	if !v.Blocked {
		t.Fatalf("want blocked, got allowed (size=%d)", len(big))
	}
	if !strings.Contains(v.Reason, "Limit") {
		t.Fatalf("Reason=%q, want 'Limit'", v.Reason)
	}

	// Exakt am Limit → ok
	okBody, _ := json.Marshal(map[string]any{"appendMarkdown": strings.Repeat("x", MaxExtendDescriptionBytes)})
	v2 := ValidateMutation("extend-description", "tc1", okBody, nil)
	if !v2.Allowed {
		t.Fatalf("at-limit should be allowed, got blocked: %s", v2.Reason)
	}
}

func TestBuildAnthropicTools_Shape(t *testing.T) {
	tools := BuildAnthropicTools(MutationTools)
	if len(tools) != len(MutationTools) {
		t.Fatalf("got %d tools, want %d", len(tools), len(MutationTools))
	}
	names := map[string]bool{}
	for _, t := range tools {
		names[t.Name] = true
		if t.Description == "" {
			continue
		}
		if t.InputSchema["type"] != "object" {
			continue
		}
	}
	for _, want := range []string{"set-field", "move", "add-comment", "extend-description", "add-label"} {
		if !names[want] {
			t.Errorf("missing tool: %s", want)
		}
	}
	// Blocklist darf nicht in der Liste auftauchen.
	for blocked := range MutationActionBlocklist {
		if names[blocked] {
			t.Errorf("blocked tool %q ist in Anthropic-Tool-Liste", blocked)
		}
	}
}

func TestBuildOpenAITools_Shape(t *testing.T) {
	tools := BuildOpenAITools(MutationTools)
	if len(tools) != len(MutationTools) {
		t.Fatalf("got %d tools, want %d", len(tools), len(MutationTools))
	}
	for _, tool := range tools {
		if tool.Type != "function" {
			t.Errorf("tool.Type=%q, want function", tool.Type)
		}
		if tool.Function.Name == "" {
			t.Errorf("function.name leer")
		}
		if tool.Function.Parameters["type"] != "object" {
			t.Errorf("function.parameters.type=%v, want object", tool.Function.Parameters["type"])
		}
	}
}

func TestBuildGoogleTools_Shape(t *testing.T) {
	tools := BuildGoogleTools(MutationTools)
	// Google bündelt alle Declarations in EINEM tools-Block.
	if len(tools) != 1 {
		t.Fatalf("got %d Google-Tool-Blocks, want 1", len(tools))
	}
	if len(tools[0].FunctionDeclarations) != len(MutationTools) {
		t.Fatalf("got %d declarations, want %d", len(tools[0].FunctionDeclarations), len(MutationTools))
	}
	for _, decl := range tools[0].FunctionDeclarations {
		if decl.Name == "" {
			t.Errorf("declaration.Name leer")
		}
		if decl.Parameters["type"] != "object" {
			t.Errorf("declaration.parameters.type=%v, want object", decl.Parameters["type"])
		}
	}
}

func TestBuildMistralTools_OpenAICompatible(t *testing.T) {
	mistral := BuildMistralTools(MutationTools)
	openai := BuildOpenAITools(MutationTools)
	if len(mistral) != len(openai) {
		t.Fatalf("mistral=%d, openai=%d", len(mistral), len(openai))
	}
	for i := range mistral {
		if mistral[i].Function.Name != openai[i].Function.Name {
			t.Errorf("name mismatch at %d", i)
		}
	}
}

func TestValidateMutation_ArgsAreReMarshaled(t *testing.T) {
	// Inkonsistente Whitespace + Trailing-Spaces im JSON → Re-Marshal liefert
	// kanonisches Format (für Test-Determinismus + Client-Confirm-Round-Trip).
	in := `{ "field"  : "qa" ,  "value" : true }`
	v := ValidateMutation("set-field", "tc1", json.RawMessage(in), nil)
	if !v.Allowed {
		t.Fatalf("expected allowed, got %s", v.Reason)
	}
	out := string(v.Args)
	if !strings.Contains(out, `"field":"qa"`) || !strings.Contains(out, `"value":true`) {
		t.Fatalf("args re-marshal kaputt: %q", out)
	}
	// Kein Whitespace mehr (json.Marshal compact)
	if strings.Contains(out, " ") {
		t.Fatalf("expected compact json, got %q", out)
	}
}

// F-410 (ADR-0096 §3 Schritt 4 / §4 L3): move muss die Ziel-Spalte gegen die
// gültigen board.columnDefs prüfen, sonst kann das LLM die Karte in eine
// Phantom-Spalte schieben und sie aus allen Ansichten verschwinden lassen.
func TestValidateMutation_Move_ColumnValidation(t *testing.T) {
	valid := map[string]bool{"col-ready": true, "col-erledigt": true}

	// Bekannte Spalte → erlaubt
	v := ValidateMutation("move", "tc1", json.RawMessage(`{"toColumnId":"col-erledigt"}`), valid)
	if !v.Allowed {
		t.Fatalf("bekannte Spalte sollte erlaubt sein, blocked: %s", v.Reason)
	}

	// Unbekannte/Phantom-Spalte → blockiert
	v2 := ValidateMutation("move", "tc1", json.RawMessage(`{"toColumnId":"col-phantom"}`), valid)
	if !v2.Blocked {
		t.Fatalf("Phantom-Spalte muss blockiert werden, got allowed")
	}
	if !strings.Contains(v2.Reason, "existiert nicht") {
		t.Fatalf("Reason=%q, want 'existiert nicht'", v2.Reason)
	}

	// Backward-Compat: nil/leere Spaltenliste → Check übersprungen (Client-Guard führt)
	v3 := ValidateMutation("move", "tc1", json.RawMessage(`{"toColumnId":"col-phantom"}`), nil)
	if !v3.Allowed {
		t.Fatalf("nil-Spaltenliste sollte Check überspringen (allowed), got blocked: %s", v3.Reason)
	}
}

// ── F-412: Wert-Validierung bei set-field ────────────────────────────────

// TestValidateMutation_SetFieldWert deckt F-412 ab: Bis Sprint 55 prüfte die
// L3-Sanity nur den FELDNAMEN gegen SetFieldAllowedFields, nicht den WERT.
// Ein Modell, das den richtigen Feldnamen mit einem unsinnigen Wert kombiniert
// — `qa: "ja"`, `effort: "ein halber Tag"`, `complexity: "sehr hoch"` —, kam
// durch, und das Ergebnis stand danach im Board: die UI zeigt es an, die
// Velocity-Berechnung stolpert darüber, und niemand weiss mehr, ob es ein
// Mensch oder ein Modell war.
//
// GEGENGEPROBT (Karte #354, Sprint 55): Ohne den Aufruf von
// pruefeSetFieldWert in ValidateMutation werden alle mit ok=false markierten
// Fälle dieser Tabelle grün — der Test wird rot.
func TestValidateMutation_SetFieldWert(t *testing.T) {
	cases := []struct {
		args string
		ok   bool
		why  string
	}{
		// Boolean-Felder
		{`{"field":"qa","value":true}`, true, ""},
		{`{"field":"qa","value":false}`, true, ""},
		{`{"field":"qa","value":"ja"}`, false, "erwartet true oder false, bekam String"},
		{`{"field":"critical","value":1}`, false, "erwartet true oder false, bekam Zahl"},
		{`{"field":"blocked","value":null}`, false, "erwartet true oder false, bekam null"},

		// effort: Zahl >= 0, null loescht die Schaetzung
		{`{"field":"effort","value":4}`, true, ""},
		{`{"field":"effort","value":0.5}`, true, ""},
		{`{"field":"effort","value":0}`, true, ""},
		{`{"field":"effort","value":null}`, true, ""},
		{`{"field":"effort","value":"ein halber Tag"}`, false, "erwartet eine Zahl"},
		{`{"field":"effort","value":-1}`, false, "erwartet eine Zahl >= 0"},
		{`{"field":"effort","value":true}`, false, "erwartet eine Zahl"},

		// Datumsfelder: ISO YYYY-MM-DD, null/"" loeschen
		{`{"field":"dueDate","value":"2026-08-19"}`, true, ""},
		{`{"field":"startDate","value":"2026-01-01"}`, true, ""},
		{`{"field":"dueDate","value":null}`, true, ""},
		{`{"field":"dueDate","value":""}`, true, ""},
		{`{"field":"dueDate","value":"19.08.2026"}`, false, "erwartet ein ISO-Datum"},
		{`{"field":"dueDate","value":"morgen"}`, false, "erwartet ein ISO-Datum"},
		{`{"field":"dueDate","value":42}`, false, "erwartet ein ISO-Datum"},
		// Formal richtig, kalendarisch unmoeglich — deshalb time.Parse und
		// nicht bloss ein Regex.
		{`{"field":"dueDate","value":"2026-02-31"}`, false, "erwartet ein ISO-Datum"},
		{`{"field":"dueDate","value":"2026-13-01"}`, false, "erwartet ein ISO-Datum"},
		// ADR-0138 §1
		{`{"field":"dueTime","value":"09:30"}`, true, ""},
		{`{"field":"startTime","value":"23:59"}`, true, ""},
		{`{"field":"dueTime","value":null}`, true, ""},
		{`{"field":"startTime","value":""}`, true, ""},
		{`{"field":"dueTime","value":"9:30"}`, false, "Uhrzeit HH:MM"},
		{`{"field":"dueTime","value":"24:00"}`, false, "Uhrzeit HH:MM"},
		{`{"field":"startTime","value":"10:60"}`, false, "Uhrzeit HH:MM"},
		{`{"field":"dueTime","value":930}`, false, "Uhrzeit HH:MM"},

		// complexity: Enum aus src/modal.js:2114
		{`{"field":"complexity","value":"einfach"}`, true, ""},
		{`{"field":"complexity","value":"mittel"}`, true, ""},
		{`{"field":"complexity","value":"hoch"}`, true, ""},
		{`{"field":"complexity","value":""}`, true, ""},
		{`{"field":"complexity","value":"sehr hoch"}`, false, "erwartet einen der Werte"},
		{`{"field":"complexity","value":"high"}`, false, "erwartet einen der Werte"},
		{`{"field":"complexity","value":3}`, false, "erwartet einen String"},

		// priority: Enum aus src/modal.js:2079, plus "" als Bestandswert
		{`{"field":"priority","value":"niedrig"}`, true, ""},
		{`{"field":"priority","value":"hoch"}`, true, ""},
		{`{"field":"priority","value":""}`, true, ""},
		{`{"field":"priority","value":"dringend"}`, false, "erwartet einen der Werte"},

		// status: NUR Typpruefung, bewusst keine Werteliste (kein
		// Bedienelement, keine belastbare Aufzaehlung — siehe setFieldEnums).
		{`{"field":"status","value":"offen"}`, true, ""},
		{`{"field":"status","value":"erledigt"}`, true, ""},
		{`{"field":"status","value":"was-auch-immer"}`, true, ""},
		{`{"field":"status","value":true}`, false, "erwartet einen String"},
	}
	for _, c := range cases {
		t.Run(c.args, func(t *testing.T) {
			v := ValidateMutation("set-field", "tc1", json.RawMessage(c.args), nil)
			if v.Allowed != c.ok {
				t.Fatalf("Allowed=%v, want %v (Reason=%q)", v.Allowed, c.ok, v.Reason)
			}
			if !c.ok && !strings.Contains(v.Reason, c.why) {
				t.Fatalf("Reason=%q, want substring %q", v.Reason, c.why)
			}
		})
	}
}

// TestPruefeSetFieldWert_MeldungNenntDenTyp: Die Fehlermeldung geht an das
// Modell zurück. „bekam String" ist eine Auskunft, mit der es den nächsten
// Versuch korrigieren kann — „ungültiger Wert" nicht. Der Test hält fest, dass
// der vorgefundene Typ auch wirklich in der Meldung steht.
func TestPruefeSetFieldWert_MeldungNenntDenTyp(t *testing.T) {
	cases := []struct {
		wert    any
		typName string
	}{
		{nil, "null"},
		{"text", "String"},
		{float64(3), "Zahl"},
		{[]any{1, 2}, "Liste"},
		{map[string]any{"a": 1}, "Objekt"},
	}
	for _, c := range cases {
		t.Run(c.typName, func(t *testing.T) {
			grund := pruefeSetFieldWert("qa", c.wert)
			if grund == "" {
				t.Fatalf("Wert %v (%s) wurde für qa akzeptiert", c.wert, c.typName)
			}
			if !strings.Contains(grund, c.typName) {
				t.Fatalf("Meldung %q nennt den vorgefundenen Typ %q nicht", grund, c.typName)
			}
		})
	}
}

// TestSetFieldEnums_DeckenDieAllowlistNichtVollstaendigAb hält die bewusste
// Lücke fest, damit sie nicht als Versehen repariert wird: `status` steht in
// SetFieldAllowedFields, hat aber absichtlich KEINEN Enum-Eintrag — das Feld
// hat kein Bedienelement in der UI und damit keine belastbare Werteliste
// (Bestand: "", "offen", "erledigt"). Eine hier erfundene Aufzählung würde
// entweder legitime Altwerte blockieren oder falsche durchlassen.
//
// Der Test wird rot, sobald jemand `status` ein Enum gibt — und zwingt damit
// zu der Frage, ob das Feld inzwischen eine Semantik hat.
func TestSetFieldEnums_StatusBewusstOhneEnum(t *testing.T) {
	if _, hat := setFieldEnums["status"]; hat {
		t.Fatal("status hat jetzt ein Enum — wenn das Feld eine Semantik bekommen hat, " +
			"ist das richtig: dann diesen Test entfernen und den Kommentar in " +
			"setFieldEnums nachziehen. Wenn nicht, ist es geraten.")
	}
	// Gegenstück: die Felder, für die es eine belegte Liste GIBT, müssen sie
	// auch behalten — sonst fällt die Prüfung still auf reine Typprüfung zurück.
	for _, f := range []string{"complexity", "priority"} {
		if len(setFieldEnums[f]) == 0 {
			t.Errorf("%s hat kein Enum mehr — die Werteliste steht in src/modal.js", f)
		}
	}
}

// TestSetFieldAllowlist_JedesFeldWirdGeprueft schliesst F-613: `pruefeSetFieldWert`
// deckt heute alle neun Felder aus `SetFieldAllowedFields` ab, faellt aber fuer
// alles Unbekannte auf `return ""` durch — also auf „Wert in Ordnung".
//
// Fehlschlag ohne diesen Test: Jemand ergaenzt die Allowlist um `"labels"` oder
// `"owner"`. Alle Tests bleiben gruen, F-412 ist fuer das neue Feld lautlos
// wieder offen, und ein Modell kann `set-field(labels, {"a":1})` durchreichen.
//
// Die Sonde ist bewusst ein OBJEKT: Kein Feld der Allowlist erwartet eines,
// also muss jedes es ablehnen. Ein Test mit feldspezifischen Sonden waere
// genauer, wuerde aber genau die Pflege verlangen, die hier vergessen wird.
func TestSetFieldAllowlist_JedesFeldWirdGeprueft(t *testing.T) {
	sonde := map[string]any{"unerwartet": true}
	for _, feld := range SetFieldAllowedFields {
		t.Run(feld, func(t *testing.T) {
			if grund := pruefeSetFieldWert(feld, sonde); grund == "" {
				t.Fatalf("pruefeSetFieldWert(%q, <Objekt>) akzeptiert den Wert — das Feld "+
					"steht in SetFieldAllowedFields, wird aber von keinem case behandelt. "+
					"F-412 ist damit fuer %q offen.", feld, feld)
			}
		})
	}
}

// TestSetFieldEnums_ParitaetMitFrontend belegt, was der Kommentar in
// src/llm-mutations.js behauptet: Die Enum-Listen auf beiden Seiten sind
// deckungsgleich (F-612).
//
// Bis zur Sprint-55-Nacharbeit gab es diese Pruefung nicht — beide Seiten
// verpflichteten sich in Kommentaren gegenseitig darauf, und `SET_FIELD_ENUMS`
// war im Frontend nicht einmal exportiert. Fehlschlag ohne den Test: Jemand
// ergaenzt `priority: "kritisch"` auf einer Seite. Beide Suiten bleiben gruen,
// der Server laesst den Wert durch, der Client verwirft ihn beim Apply-Klick.
// Genau die Drift, die F-414 fuer die Feldnamen schon einmal produziert hat.
//
// PRO FELD geprueft, nicht ueber den ganzen Block: Die erste Fassung suchte
// jeden Wert irgendwo in SET_FIELD_ENUMS. Damit blieb sie gruen, als 'hoch' aus
// `priority` entfernt wurde — der Wert steht ja auch bei `complexity`. Ein
// Test, der die Zuordnung nicht mitprueft, prueft bei ueberlappenden
// Wertelisten nichts.
//
// Gelesen wird die JS-Datei als Text — ein Go-Test kann kein JS ausfuehren.
// Das ist grob, faengt aber den Fall, um den es geht: eine geaenderte Liste.
func TestSetFieldEnums_ParitaetMitFrontend(t *testing.T) {
	roh, err := os.ReadFile("../../src/llm-mutations.js")
	if err != nil {
		t.Fatalf("src/llm-mutations.js lesen: %v", err)
	}
	quelle := string(roh)

	i := strings.Index(quelle, "SET_FIELD_ENUMS = {")
	if i < 0 {
		t.Fatal("SET_FIELD_ENUMS nicht in src/llm-mutations.js gefunden — " +
			"wurde die Konstante umbenannt? Dann diesen Test nachziehen.")
	}
	rest := quelle[i:]
	j := strings.Index(rest, "};")
	if j < 0 {
		t.Fatal("Ende von SET_FIELD_ENUMS nicht gefunden")
	}
	block := rest[:j]

	feldZeile := func(feld string) (string, bool) {
		for _, z := range strings.Split(block, "\n") {
			if strings.HasPrefix(strings.TrimSpace(z), feld+":") {
				return z, true
			}
		}
		return "", false
	}

	werteAusZeile := func(zeile string) []string {
		var out []string
		teile := strings.Split(zeile, "'")
		for k := 1; k < len(teile); k += 2 { // ungerade Indizes liegen zwischen den Quotes
			out = append(out, teile[k])
		}
		return out
	}

	for feld, werte := range setFieldEnums {
		zeile, ok := feldZeile(feld)
		if !ok {
			t.Errorf("Feld %q hat serverseitig ein Enum, fehlt aber in SET_FIELD_ENUMS "+
				"im Frontend", feld)
			continue
		}
		imFrontend := werteAusZeile(zeile)
		if len(imFrontend) != len(werte) {
			t.Errorf("Feld %q: Server hat %d Werte %v, Frontend %d %v",
				feld, len(werte), werte, len(imFrontend), imFrontend)
		}
		for _, w := range werte {
			gefunden := false
			for _, f := range imFrontend {
				if f == w {
					gefunden = true
					break
				}
			}
			if !gefunden {
				t.Errorf("Wert %q fehlt im Frontend-Enum von %q (dort: %v) — Server und "+
					"Browser bewerten denselben Vorschlag unterschiedlich", w, feld, imFrontend)
			}
		}
	}

	// Die bewusste status-Luecke muss auf beiden Seiten gleich aussehen.
	for _, feld := range []string{"complexity", "priority", "status"} {
		_, imFrontend := feldZeile(feld)
		_, imServer := setFieldEnums[feld]
		if imFrontend != imServer {
			t.Errorf("Feld %q: Frontend-Enum=%v, Server-Enum=%v — die bewusste "+
				"status-Luecke (kein Bedienelement, keine belastbare Werteliste) "+
				"muss auf BEIDEN Seiten gleich aussehen", feld, imFrontend, imServer)
		}
	}
}
