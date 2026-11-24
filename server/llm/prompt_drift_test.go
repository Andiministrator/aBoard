package llm

import (
	"encoding/json"
	"os"
	"testing"
)

// F-400 (Karte #488): Drift-Wächter für den doppelt gebauten User-Prompt.
//
// Gegenstück zu tests/prompt-drift.test.js. Beide laufen über DIESELBE
// Eingabe und vergleichen gegen DENSELBEN erwarteten Text. Driftet eine
// Seite, fällt genau ein Test — und der sagt, welche.
//
// Warum ein eingecheckter Snapshot und nicht „beide gegeneinander": Ein
// Vergleich der beiden Implementierungen bliebe grün, wenn sich beide
// zusammen ändern. Mit dem Snapshot ist auch das eine sichtbare Diff-Zeile,
// und eine Änderung am Prompt-Format ist eine Änderung am Produkt.
const (
	promptFixture  = "../../tests/fixtures/prompt-drift/karte.json"
	promptSnapshot = "../../tests/fixtures/prompt-drift/erwartet.txt"
)

type promptFixtureDatei struct {
	IncludeFields []string       `json:"includeFields"`
	Card          map[string]any `json:"card"`
	RefsByID      map[string]struct {
		DisplayID int    `json:"displayId"`
		Title     string `json:"title"`
		Snippet   string `json:"snippet"`
	} `json:"refsByID"`
}

func ladePromptFixture(t *testing.T) (promptFixtureDatei, map[string]RefSummary) {
	t.Helper()
	roh, err := os.ReadFile(promptFixture)
	if err != nil {
		t.Fatalf("Fixture nicht lesbar: %v", err)
	}
	var fx promptFixtureDatei
	if err := json.Unmarshal(roh, &fx); err != nil {
		t.Fatalf("Fixture kein gültiges JSON: %v", err)
	}
	refs := map[string]RefSummary{}
	for k, v := range fx.RefsByID {
		refs[k] = RefSummary{DisplayID: v.DisplayID, Title: v.Title, Snippet: v.Snippet}
	}
	return fx, refs
}

func TestBuildPromptEntsprichtGeteiltemSnapshot(t *testing.T) {
	fx, refs := ladePromptFixture(t)
	erwartet, err := os.ReadFile(promptSnapshot)
	if err != nil {
		t.Fatalf("Snapshot nicht lesbar: %v", err)
	}
	ist := BuildPrompt(fx.Card, fx.IncludeFields, refs)
	if ist != string(erwartet) {
		t.Errorf("BuildPrompt weicht vom geteilten Snapshot ab.\n"+
			"Entweder ist die Go-Seite gedriftet, oder das Format hat sich bewusst "+
			"geändert — dann gehört der Snapshot im selben Commit nachgezogen, "+
			"und src/llm-pii.js buildPrompt mit.\n\n--- erwartet ---\n%s\n--- ist ---\n%s",
			string(erwartet), ist)
	}
}

// Die Fixture darf nicht schrumpfen, ohne dass es auffällt: Ein Feld, das sie
// nicht setzt, wird von keiner der beiden Seiten gebaut — beide Tests blieben
// grün, während genau dort der nächste Drift entsteht.
func TestPromptFixtureDecktAlleErlaubtenFelderAb(t *testing.T) {
	fx, _ := ladePromptFixture(t)
	gesetzt := map[string]bool{}
	for _, f := range fx.IncludeFields {
		gesetzt[f] = true
	}
	for feld := range AllowedFields {
		if !gesetzt[feld] {
			t.Errorf("AllowedFields kennt %q, die Fixture setzt es nicht — "+
				"der Drift-Wächter ist für dieses Feld blind.", feld)
		}
		if fx.Card[feld] == nil {
			t.Errorf("Die Fixture hat keinen Inhalt für %q.", feld)
		}
	}
}
