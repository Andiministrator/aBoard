package api_test

// llm_stream_budget_test.go — Kontext-Budget im agentischen Loop (F-872,
// F-1091).
//
// Das Budget-Gate lief bis Sprint 69 nur EINMAL vor dem Turn. Die Inhalte,
// die das Lese-Werkzeug nachlädt, entstehen aber erst im Loop — ein Turn mit
// grossen verknüpften Karten konnte das Kontext-Limit überschreiten, ohne
// dass das Gate es je sah.
//
// F-1091 hat das Verhalten bei Überschreitung geändert: Der Turn wird nicht
// mehr verworfen (er ist zu dem Zeitpunkt schon bezahlt), sondern bekommt
// eine letzte Runde, in der die Abrufe mit einer Auskunft abgelehnt werden.
// Abgebrochen wird nur noch, wenn selbst diese Runde nicht mehr passt. Basis
// der Rechnung sind die GEMELDETEN Input-Tokens der gelaufenen Runde, nicht
// die Summe über alle Turns der Conversation.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Andiministrator/aBoard/server/api"
	"github.com/Andiministrator/aBoard/server/llm"
	"github.com/Andiministrator/aBoard/server/storage"
)

// budgetBoardState baut ein Board, in dem c1 mit c2 und c4 verknüpft ist;
// beide tragen eine Beschreibung der gegebenen Länge.
func budgetBoardState(t *testing.T, laenge int) []byte {
	t.Helper()
	gross := strings.Repeat("x", laenge)
	state := map[string]any{
		"version": "2",
		"board": map[string]any{
			"id": "b1", "title": "T",
			"sprints": []any{map[string]any{
				"id": "s1", "title": "S",
				"cards": []any{
					map[string]any{"id": "c1", "displayId": 1, "title": "Quelle", "description": "Quelltext",
						"comments": []any{}, "activity": []any{},
						"links": []any{
							map[string]any{"type": "related", "targetId": "c2"},
							map[string]any{"type": "related", "targetId": "c4"},
						}},
					map[string]any{"id": "c2", "displayId": 2, "title": "Gross A", "description": gross,
						"comments": []any{}, "activity": []any{},
						"links": []any{map[string]any{"type": "related", "targetId": "c1"}}},
					map[string]any{"id": "c4", "displayId": 4, "title": "Gross B", "description": gross,
						"comments": []any{}, "activity": []any{},
						"links": []any{map[string]any{"type": "related", "targetId": "c1"}}},
				},
			}},
		},
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func (e *fetchStreamEnv) ersetzeBoard(t *testing.T, state []byte) {
	t.Helper()
	ctx := context.Background()
	b, err := e.boards.Get(ctx, e.boardID)
	if err != nil {
		t.Fatalf("get board: %v", err)
	}
	if _, err := e.boards.Update(ctx, b.ID, b.Title, b.Version, state, b.UpdatedAt); err != nil {
		t.Fatalf("update board: %v", err)
	}
}

// bytesFuerTokens rechnet einen Token-Anteil des Budgets in Zeichen um
// (EstimateTokens: 4 Bytes/Token). Abgeleitet aus dem Limit, damit der Test
// eine Änderung an Limit oder Anteil überlebt, ohne still grün zu werden.
func bytesFuerTokens(anteil float64) int {
	limit := llm.ContextLimit("anthropic", "claude-opus-4-7")
	return int(float64(limit)*api.ContextBudgetFraction*anteil) * 4
}

func kommentareAnC1(t *testing.T, env *fetchStreamEnv) int {
	t.Helper()
	b, err := env.boards.Get(context.Background(), env.boardID)
	if err != nil {
		t.Fatalf("get board: %v", err)
	}
	var st map[string]any
	_ = json.Unmarshal(b.StateJSON, &st)
	karten := st["board"].(map[string]any)["sprints"].([]any)[0].(map[string]any)["cards"].([]any)
	for _, k := range karten {
		km := k.(map[string]any)
		if km["id"] == "c1" {
			c, _ := km["comments"].([]any)
			return len(c)
		}
	}
	t.Fatal("c1 fehlt")
	return 0
}

// budgetSchwelle — 80 % des Limits in Tokens, die Grenze des Gates.
func budgetSchwelle() int {
	return int(float64(llm.ContextLimit("anthropic", "claude-opus-4-7")) * api.ContextBudgetFraction)
}

// letzteWerkzeugAntwort liefert die Werkzeug-Antworten, die der Server mit
// der i-ten Anfrage an den Anbieter geschickt hat (letzte History-Nachricht).
func letzteWerkzeugAntwort(t *testing.T, env *fetchStreamEnv, i int) []llm.ToolResultBlock {
	t.Helper()
	req := env.provider.anfrage(i)
	if len(req.History) == 0 {
		t.Fatalf("Anfrage %d ohne History", i)
	}
	return req.History[len(req.History)-1].ToolResults
}

// F-1091: Eine einzige nachgeladene Karte sprengt das Budget. Vor F-1091
// verwarf der Server den (schon bezahlten) Turn mit context-budget. Jetzt
// bekommt das Modell eine Schlussrunde, in der der Abruf mit einer Auskunft
// abgelehnt wird, und die Antwort landet als Kommentar an der Karte.
func TestFetchLoop_BudgetUeberschrittenErzwingtSchlussrundeOhneInhalt(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{text: "Ich sehe nach. ", toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}},
		{text: "Antwort ohne die grosse Karte."},
	})
	// 120 % des Budgets in einer Karte.
	env.ersetzeBoard(t, budgetBoardState(t, bytesFuerTokens(1.2)))

	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	if got := env.provider.anzahlAufrufe(); got != 2 {
		t.Fatalf("%d Provider-Aufrufe, erwartet 2 (Abrufrunde + Schlussrunde ohne Inhalt)", got)
	}
	antworten := letzteWerkzeugAntwort(t, env, 1)
	if len(antworten) != 1 || !antworten[0].IsError ||
		!strings.Contains(antworten[0].Content, "Kontext-Budget erreicht") {
		t.Fatalf("Schlussrunde traegt keine Budget-Ablehnung: %+v", antworten)
	}
	if strings.Contains(antworten[0].Content, "xxxxxxxx") {
		t.Fatal("der Karteninhalt ging trotz Budget an den Anbieter")
	}
	if f := ereignisse(evs, "error"); len(f) != 0 {
		t.Fatalf("unerwarteter Fehler: %+v", f)
	}
	f := ereignisse(evs, "fetch")
	if len(f) != 1 || f[0]["ok"] != false {
		t.Fatalf("fetch-Ereignis muss die Ablehnung melden, nicht „geladen“: %+v", f)
	}
	if _, hatLabel := f[0]["label"]; hatLabel {
		t.Fatalf("abgelehnter Abruf traegt ein Label: %+v", f[0])
	}
	d := ereignisse(evs, "done")
	if len(d) != 1 {
		t.Fatalf("kein done: %+v", evs)
	}
	if _, geladen := d[0]["fetchedCards"]; geladen {
		t.Fatalf("done meldet eine nachgeladene Karte: %+v", d[0])
	}
	if n, _ := d[0]["fetchesRejected"].(float64); n != 1 {
		t.Fatalf("fetchesRejected = %v, erwartet 1", d[0]["fetchesRejected"])
	}
	if n := kommentareAnC1(t, env); n != 1 {
		t.Fatalf("%d Kommentar(e), erwartet 1 — die Antwort der Schlussrunde gehoert an die Karte", n)
	}
}

// F-1091, Gegenrichtung: Passt selbst die Schlussrunde nicht mehr (die
// gemessene Anfrage liegt schon über der Grenze), bleibt der Abbruch mit
// context-budget — ohne weiteren Anbieter-Aufruf und ohne Kommentar.
func TestFetchLoop_BudgetSelbstOhneInhalteZuGrossBrichtAb(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{text: "Ich sehe nach. ", toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")},
			eingabe: budgetSchwelle() + 1000},
		{text: "Diese Runde darf nie gesendet werden."},
	})
	env.ersetzeBoard(t, budgetBoardState(t, 100))

	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	if got := env.provider.anzahlAufrufe(); got != 1 {
		t.Fatalf("%d Provider-Aufrufe, erwartet 1", got)
	}
	fehler := ereignisse(evs, "error")
	if len(fehler) != 1 || fehler[0]["kind"] != "context-budget" {
		t.Fatalf("erwartet ein error-Ereignis kind=context-budget: %+v", fehler)
	}
	if msg, _ := fehler[0]["message"].(string); !strings.Contains(msg, "stopped before the next round") {
		t.Fatalf("Meldung benennt den Abbruch nicht: %q", msg)
	}
	if d := ereignisse(evs, "done"); len(d) != 0 {
		t.Fatalf("done trotz Abbruch: %+v", d)
	}
	if n := kommentareAnC1(t, env); n != 0 {
		t.Fatalf("%d Kommentar(e) trotz Abbruch", n)
	}
}

// Die Prüfung läuft vor JEDER weiteren Runde, nicht nur nach der ersten:
// Zwei Karten zu je 60 % passen einzeln, zusammen nicht. Runde 2 bekommt die
// erste Karte; Runde 2 meldet (realistisch) eine Anfrage in dieser Grösse,
// also passt die zweite Karte nicht mehr und Runde 3 ist die Schlussrunde.
//
// Vor F-1091 endete dieser Turn nach Runde 2 mit context-budget.
func TestFetchLoop_BudgetWirdJedeRundeFortgeschrieben(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}},
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-2", "c4")},
			eingabe: int(float64(budgetSchwelle())*0.6) + 500},
		{text: "Schlussantwort."},
	})
	env.ersetzeBoard(t, budgetBoardState(t, bytesFuerTokens(0.6)))

	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	if got := env.provider.anzahlAufrufe(); got != 3 {
		t.Fatalf("%d Provider-Aufrufe, erwartet 3 (erste Karte passt, die zweite nicht mehr)", got)
	}
	f := ereignisse(evs, "fetch")
	if len(f) != 2 || f[0]["ok"] != true || f[1]["ok"] != false {
		t.Fatalf("erwartet: erster Abruf geliefert, zweiter abgelehnt: %+v", f)
	}
	if a := letzteWerkzeugAntwort(t, env, 1); len(a) != 1 || a[0].IsError {
		t.Fatalf("Runde 2 muss die erste Karte bekommen: %+v", a)
	}
	if a := letzteWerkzeugAntwort(t, env, 2); len(a) != 1 || !a[0].IsError {
		t.Fatalf("Runde 3 muss die Budget-Ablehnung tragen: %+v", a)
	}
	if fehler := ereignisse(evs, "error"); len(fehler) != 0 {
		t.Fatalf("unerwarteter Fehler: %+v", fehler)
	}
	if d := ereignisse(evs, "done"); len(d) != 1 {
		t.Fatalf("kein done: %+v", evs)
	}
}

// Die Schlussrunde ist die LETZTE: Will das Modell darin weiter nachladen,
// antwortet der Server nicht mehr und persistiert, was da ist.
func TestFetchLoop_NachDerSchlussrundeKeineWeitereRunde(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}},
		{text: "Teilantwort. ", toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-2", "c4")}},
		{text: "Diese Runde darf nie gesendet werden."},
	})
	env.ersetzeBoard(t, budgetBoardState(t, bytesFuerTokens(1.2)))

	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	if got := env.provider.anzahlAufrufe(); got != 2 {
		t.Fatalf("%d Provider-Aufrufe, erwartet 2", got)
	}
	d := ereignisse(evs, "done")
	if len(d) != 1 {
		t.Fatalf("kein done: %+v", evs)
	}
	if n, _ := d[0]["fetchesRejected"].(float64); n != 2 {
		t.Fatalf("fetchesRejected = %v, erwartet 2", d[0]["fetchesRejected"])
	}
	if n := kommentareAnC1(t, env); n != 1 {
		t.Fatalf("%d Kommentar(e), erwartet 1", n)
	}
}

// Gegenrichtung: Karten unter dem Budget laufen durch wie bisher — der Fix
// darf den normalen Loop nicht abwürgen.
func TestFetchLoop_UnterDemBudgetLaeuftDurch(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}},
		{text: "Antwort."},
	})
	env.ersetzeBoard(t, budgetBoardState(t, bytesFuerTokens(0.5)))

	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	if got := env.provider.anzahlAufrufe(); got != 2 {
		t.Fatalf("%d Provider-Aufrufe, erwartet 2", got)
	}
	if f := ereignisse(evs, "error"); len(f) != 0 {
		t.Fatalf("unerwarteter Fehler: %+v", f)
	}
	if d := ereignisse(evs, "done"); len(d) != 1 {
		t.Fatalf("kein done: %+v", evs)
	}
}

// seedLangeConversation legt eine Conversation mit `turns` assistant-Turns
// zu je `jeTurn` gemeldeten Input-Tokens an — so sah die bis F-1108 addierte
// Summe (sumIn) nach einem längeren Gespräch aus, obwohl der tatsächliche
// Kontext klein ist.
func seedLangeConversation(t *testing.T, env *fetchStreamEnv, turns, jeTurn int) string {
	t.Helper()
	ctx := context.Background()
	convs := storage.NewLLMConversationRepo(env.db)
	conv, err := convs.CreateConversation(ctx, storage.CreateConversationParams{
		ID: "conv-lang", BoardID: env.boardID, CardID: env.cardID,
		Provider: "anthropic", Model: "claude-opus-4-7", CreatedBy: env.email,
	})
	if err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	for i := 0; i < turns; i++ {
		if _, err := convs.AppendTurn(ctx, storage.AppendTurnParams{
			ID: fmt.Sprintf("t-%d", i), ConversationID: conv.ID, Role: "assistant",
			Content: "kurz", InputTokens: ptrInt(jeTurn), OutputTokens: ptrInt(0),
			TS: fmt.Sprintf("2026-09-28T10:00:%02dZ", i),
		}); err != nil {
			t.Fatalf("seed turn: %v", err)
		}
	}
	return conv.ID
}

// DAS Szenario aus F-1091: fünf Turns à 30k auf einem 200k-Modell → die bis
// F-1108 addierte Summe (sumIn) 150k, der tatsächliche Kontext ist viel kleiner (hier meldet der Anbieter
// 40k). Eine Karte à rund 12k wird nachgeladen. Vor dem Fix rechnete das
// Loop-Gate 150k + 12k > 160k und verwarf den Turn nach der bezahlten Runde.
func TestFetchLoop_LangeConversationMitKleinerEchterBasisLaeuftDurch(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}, eingabe: 40000},
		{text: "Antwort mit der Karte."},
	})
	env.ersetzeBoard(t, budgetBoardState(t, 12000*4))
	convID := seedLangeConversation(t, env, 5, 30000)

	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true, "conversationId": convID}))

	if f := ereignisse(evs, "error"); len(f) != 0 {
		t.Fatalf("Turn wurde verworfen: %+v", f)
	}
	if got := env.provider.anzahlAufrufe(); got != 2 {
		t.Fatalf("%d Provider-Aufrufe, erwartet 2", got)
	}
	if a := letzteWerkzeugAntwort(t, env, 1); len(a) != 1 || a[0].IsError {
		t.Fatalf("die Karte muss geliefert werden: %+v", a)
	}
	if d := ereignisse(evs, "done"); len(d) != 1 {
		t.Fatalf("kein done: %+v", evs)
	}
}

// Gleiches Szenario, aber der Anbieter meldet KEINE Token-Zahlen. Dann gilt
// die Schätzung — und auch die darf die Summe der gemeldeten Zahlen (bis
// F-1108 „sumIn“) nicht enthalten, sondern schätzt die History aus ihrem Text.
func TestFetchLoop_OhneMessungSchaetztOhneSumIn(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{
		{toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}, ohneMessung: true},
		{text: "Antwort mit der Karte.", ohneMessung: true},
	})
	env.ersetzeBoard(t, budgetBoardState(t, 12000*4))
	convID := seedLangeConversation(t, env, 5, 30000)

	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true, "conversationId": convID}))

	if f := ereignisse(evs, "error"); len(f) != 0 {
		t.Fatalf("Turn wurde verworfen: %+v", f)
	}
	if a := letzteWerkzeugAntwort(t, env, 1); len(a) != 1 || a[0].IsError {
		t.Fatalf("die Karte muss geliefert werden: %+v", a)
	}
}

// F-1091, zweiter Teil: Der Assistant-Zug einer Runde (Text + Aufruf-
// Argumente) steckt in den gemeldeten OutputTokens. Vorher kam die Schätzung
// desselben Texts noch einmal obendrauf. Hier ist der Text lang (T Tokens)
// und die Karte so gross, dass sie mit einfacher Zählung passt, mit doppelter
// nicht.
func TestFetchLoop_AssistantTextWirdNichtDoppeltGezaehlt(t *testing.T) {
	const textTokens = 20000
	text := strings.Repeat("t", textTokens*4)
	env := newFetchStreamEnv(t, []runde{
		{text: text, toolCalls: []llm.ToolCallChunk{fetchAufruf("tu-1", "c2")}, ausgabe: textTokens + 10},
		{text: "Antwort."},
	})
	env.ersetzeBoard(t, budgetBoardState(t, (budgetSchwelle()-textTokens-3000)*4))

	evs := env.sende(t, standardBody(map[string]any{"enableFetch": true}))

	if got := env.provider.anzahlAufrufe(); got != 2 {
		t.Fatalf("%d Provider-Aufrufe, erwartet 2", got)
	}
	if a := letzteWerkzeugAntwort(t, env, 1); len(a) != 1 || a[0].IsError {
		t.Fatalf("die Karte passt bei einfacher Zaehlung und muss geliefert werden: %+v", a)
	}
	if f := ereignisse(evs, "fetch"); len(f) != 1 || f[0]["ok"] != true {
		t.Fatalf("fetch-Ereignis: %+v", f)
	}
}

// Gate vor dem Turn (F-1108, Karte #583): Die gemeldeten input_tokens früherer
// Turns sind keine Basis mehr, sondern die History, die mitgeht. Sechs Turns
// à 30k gemeldet = 180k > 160k-Schwelle; ihr Text ist aber winzig. Vor dem Fix
// antwortete der Endpunkt 400 context-budget, obwohl die Anfrage klein war.
//
// Der Prüfbefehl im Finding nannte fünf Turns à 30k — das sind 150k, unter
// der Schwelle; der alte Code nahm den Turn damit ebenfalls an, der Befehl
// trennte also nicht (gemessen, siehe Commit). Deshalb sechs.
//
// GEGENPROBE (Karte #583): Mit `projected := summeGemeldet + neuerAnteil`
// (Summe der input_tokens) wird der Test rot.
func TestBudgetGate_LangeConversationMitKleinerHistoryWirdAngenommen(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{{text: "Antwort."}})
	convID := seedLangeConversation(t, env, 6, 30000)

	resp, body := env.sendeRoh(t, standardBody(map[string]any{"conversationId": convID}))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Status %d (%v), erwartet 200 — gemeldete Token-Zahlen früherer Turns zählen nicht mehr", resp.StatusCode, body)
	}
	if got := env.provider.anzahlAufrufe(); got != 1 {
		t.Fatalf("%d Provider-Aufrufe, erwartet 1", got)
	}
}

// Die Gegenrichtung: Ist die History selbst zu groß, lehnt das Gate weiter
// ab — ohne Anbieter-Aufruf.
func TestBudgetGate_GrosseHistoryWirdAbgelehnt(t *testing.T) {
	env := newFetchStreamEnv(t, []runde{{text: "sollte nie kommen"}})
	ctx := context.Background()
	convs := storage.NewLLMConversationRepo(env.db)
	conv, err := convs.CreateConversation(ctx, storage.CreateConversationParams{
		ID: "conv-gross", BoardID: env.boardID, CardID: env.cardID,
		Provider: "anthropic", Model: "claude-opus-4-7", CreatedBy: env.email,
	})
	if err != nil {
		t.Fatalf("seed conv: %v", err)
	}
	if err := convs.AppendTurnPair(ctx,
		storage.AppendTurnParams{ID: "u", ConversationID: conv.ID, Role: "user",
			Content: strings.Repeat("u", budgetSchwelle()*4/2), TS: "2026-09-28T10:00:00Z"},
		storage.AppendTurnParams{ID: "a", ConversationID: conv.ID, Role: "assistant",
			Content: strings.Repeat("a", budgetSchwelle()*4/2+400), TS: "2026-09-28T10:00:01Z",
			InputTokens: ptrInt(1), OutputTokens: ptrInt(1)},
	); err != nil {
		t.Fatalf("seed turns: %v", err)
	}

	resp, body := env.sendeRoh(t, standardBody(map[string]any{"conversationId": conv.ID}))
	if resp.StatusCode != http.StatusBadRequest || body["kind"] != "context-budget" {
		t.Fatalf("Status %d %v, erwartet 400 context-budget", resp.StatusCode, body)
	}
	if got := env.provider.anzahlAufrufe(); got != 0 {
		t.Fatalf("%d Provider-Aufrufe trotz Budget-Gate, erwartet 0", got)
	}
}
