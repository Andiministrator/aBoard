package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newConvTestRepo(t *testing.T) (*LLMConversationRepo, func()) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "aboard.db")
	db, err := Open(ctx, Config{DBPath: dbPath})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return NewLLMConversationRepo(db), func() { db.Close() }
}

func TestLLMConv_CreateListGet(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()

	c, err := repo.CreateConversation(ctx, CreateConversationParams{
		ID:        "conv-1",
		BoardID:   "b1",
		CardID:    "card-1",
		Provider:  "anthropic",
		Model:     "claude-opus-4-7",
		CreatedBy: "andi@example.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID != "conv-1" || c.BoardID != "b1" || c.CreatedAt == "" {
		t.Errorf("Create-Result unerwartet: %+v", c)
	}
	if c.ArchivedAt != nil {
		t.Errorf("Frisch erzeugte Conversation hat archivedAt = %v, erwartet nil", *c.ArchivedAt)
	}

	got, err := repo.GetConversation(ctx, "conv-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-4-7" {
		t.Errorf("Get unerwartet: %+v", got)
	}

	list, err := repo.ListConversationsForCard(ctx, "b1", "card-1", false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != "conv-1" {
		t.Errorf("List liefert %+v, erwartet [conv-1]", list)
	}
}

func TestLLMConv_CreateValidation(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()

	// Pflichtfeld fehlt
	if _, err := repo.CreateConversation(ctx, CreateConversationParams{
		ID: "", BoardID: "b1", CardID: "c1", Provider: "anthropic", Model: "x", CreatedBy: "a@x",
	}); err == nil {
		t.Errorf("Create ohne ID sollte fehlschlagen")
	}

	// CHECK-Constraint: Provider muss whitelisted sein
	if _, err := repo.CreateConversation(ctx, CreateConversationParams{
		ID: "x", BoardID: "b1", CardID: "c1", Provider: "wat", Model: "m", CreatedBy: "a@x",
	}); err == nil {
		t.Errorf("Create mit Fantasie-Provider sollte CHECK-fehlschlagen")
	}
}

func TestLLMConv_GetNotFound(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()
	_, err := repo.GetConversation(ctx, "nope")
	if !errors.Is(err, ErrLLMConversationNotFound) {
		t.Errorf("GetNotFound: erwarte ErrLLMConversationNotFound, got %v", err)
	}
}

func TestLLMConv_ListExcludesArchived(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()

	_, _ = repo.CreateConversation(ctx, CreateConversationParams{
		ID: "active", BoardID: "b1", CardID: "c1", Provider: "openai", Model: "gpt-4o", CreatedBy: "a@x",
	})
	_, _ = repo.CreateConversation(ctx, CreateConversationParams{
		ID: "to-archive", BoardID: "b1", CardID: "c1", Provider: "openai", Model: "gpt-4o", CreatedBy: "a@x",
	})
	if err := repo.ArchiveConversation(ctx, "to-archive"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	// Default: archivierte ausblenden
	list, _ := repo.ListConversationsForCard(ctx, "b1", "c1", false)
	if len(list) != 1 || list[0].ID != "active" {
		t.Errorf("List(false) = %+v, erwartet nur active", list)
	}

	// includeArchived: alle
	listAll, _ := repo.ListConversationsForCard(ctx, "b1", "c1", true)
	if len(listAll) != 2 {
		t.Errorf("List(true) liefert %d, erwartet 2", len(listAll))
	}

	got, _ := repo.GetConversation(ctx, "to-archive")
	if got.ArchivedAt == nil || *got.ArchivedAt == "" {
		t.Errorf("Archive setzt archivedAt nicht: %+v", got)
	}
}

func TestLLMConv_ListLimit10(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()
	// 12 Conversations anlegen — List soll auf 10 cappen.
	for i := 0; i < 12; i++ {
		_, err := repo.CreateConversation(ctx, CreateConversationParams{
			ID:        "c-" + string(rune('a'+i)),
			BoardID:   "b1",
			CardID:    "c1",
			Provider:  "google",
			Model:     "gemini-1.5-pro",
			CreatedBy: "a@x",
		})
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
	}
	list, _ := repo.ListConversationsForCard(ctx, "b1", "c1", false)
	if len(list) != 10 {
		t.Errorf("LIMIT 10 verletzt: %d Items", len(list))
	}
}

func TestLLMConv_ArchiveNotFound(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	if err := repo.ArchiveConversation(context.Background(), "nope"); !errors.Is(err, ErrLLMConversationNotFound) {
		t.Errorf("Archive(nope): erwarte ErrLLMConversationNotFound, got %v", err)
	}
}

func TestLLMConv_DeleteCascadesTurns(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()
	_, _ = repo.CreateConversation(ctx, CreateConversationParams{
		ID: "conv-cas", BoardID: "b1", CardID: "c1", Provider: "mistral", Model: "mistral-large", CreatedBy: "a@x",
	})
	if _, err := repo.AppendTurn(ctx, AppendTurnParams{
		ID: "t1", ConversationID: "conv-cas", Role: "user", Content: "Hallo",
	}); err != nil {
		t.Fatalf("AppendTurn: %v", err)
	}
	if _, err := repo.AppendTurn(ctx, AppendTurnParams{
		ID: "t2", ConversationID: "conv-cas", Role: "assistant", Content: "Hi",
		InputTokens: intPtr(42), OutputTokens: intPtr(7), CostEstimate: f64Ptr(0.001),
	}); err != nil {
		t.Fatalf("AppendTurn assistant: %v", err)
	}

	turns, err := repo.LoadTurns(ctx, "conv-cas")
	if err != nil {
		t.Fatalf("LoadTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("LoadTurns: %d Turns, erwartet 2", len(turns))
	}
	if turns[0].ID != "t1" || turns[1].ID != "t2" {
		t.Errorf("Turn-Reihenfolge unerwartet: %+v", turns)
	}
	if turns[1].InputTokens == nil || *turns[1].InputTokens != 42 {
		t.Errorf("InputTokens nicht persistiert: %+v", turns[1])
	}
	if turns[0].InputTokens != nil {
		t.Errorf("user-Turn sollte InputTokens=nil haben: %+v", turns[0])
	}

	if err := repo.DeleteConversation(ctx, "conv-cas"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	turnsAfter, _ := repo.LoadTurns(ctx, "conv-cas")
	if len(turnsAfter) != 0 {
		t.Errorf("CASCADE greift nicht — Turns übrig: %+v", turnsAfter)
	}
}

func TestLLMConv_AppendTurnValidation(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()
	_, _ = repo.CreateConversation(ctx, CreateConversationParams{
		ID: "v", BoardID: "b", CardID: "c", Provider: "anthropic", Model: "m", CreatedBy: "a@x",
	})
	// Falsche Rolle
	if _, err := repo.AppendTurn(ctx, AppendTurnParams{
		ID: "t", ConversationID: "v", Role: "system", Content: "x",
	}); err == nil {
		t.Errorf("AppendTurn(role=system) sollte fehlschlagen")
	}
	// Unbekannte ConversationID → FK-Fehler
	if _, err := repo.AppendTurn(ctx, AppendTurnParams{
		ID: "t", ConversationID: "nope", Role: "user", Content: "x",
	}); err == nil {
		t.Errorf("AppendTurn auf unbekannte Conversation sollte FK-fehlschlagen")
	}
}

func intPtr(v int) *int         { return &v }
func f64Ptr(v float64) *float64 { return &v }

// ── F-413: Turn-Paar in einer Transaktion ────────────────────────────────

// TestAppendTurnPair_ScheiternLaesstKeinenWaisenTurn ist der Kern von F-413.
//
// Der Defekt: `llm_stream.go` rief zweimal `AppendTurn` hintereinander. Schlug
// der zweite fehl, leerte die Fehlerbehandlung nur die zurückgegebenen IDs —
// sie räumte NICHTS auf. Ein User-Turn ohne Antwort blieb in der Tabelle
// stehen, und `LoadTurns` speist damit die Provider-History des nächsten
// Multi-Turn-Aufrufs: Das Modell sieht dort eine Frage, die es nicht
// beantwortet hat.
//
// Der Assistant-Turn wird hier zum Scheitern gebracht, indem er die ID des
// User-Turns wiederverwendet — der Primärschlüssel kollidiert. Das ist ein
// echter Datenbankfehler auf dem zweiten Einfügevorgang, kein nachgebauter.
//
// GEGENGEPROBT (Karte #354, Sprint 55): Wird die Transaktion durch zwei
// getrennte `AppendTurn`-Aufrufe ersetzt (die alte Fassung), zählt die Abfrage
// danach 1 statt 0 — der Test wird rot.
func TestAppendTurnPair_ScheiternLaesstKeinenWaisenTurn(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()

	if _, err := repo.CreateConversation(ctx, CreateConversationParams{
		ID: "conv-413", BoardID: "b1", CardID: "card-1",
		Provider: "anthropic", Model: "claude-opus-4-7", CreatedBy: "andi@example.com",
	}); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	err := repo.AppendTurnPair(ctx,
		AppendTurnParams{ID: "turn-x", ConversationID: "conv-413", Role: "user", Content: "Frage"},
		// Gleiche ID → Primärschlüssel-Kollision beim ZWEITEN Insert.
		AppendTurnParams{ID: "turn-x", ConversationID: "conv-413", Role: "assistant", Content: "Antwort"},
	)
	if err == nil {
		t.Fatal("AppendTurnPair meldete Erfolg trotz kollidierender IDs")
	}

	turns, ladeErr := repo.LoadTurns(ctx, "conv-413")
	if ladeErr != nil {
		t.Fatalf("LoadTurns: %v", ladeErr)
	}
	if len(turns) != 0 {
		t.Fatalf("nach gescheitertem Paar liegen %d Turn(s) in der Tabelle, erwartet 0 — "+
			"der User-Turn ist die Waise aus F-413 (%+v)", len(turns), turns)
	}
}

// TestAppendTurnPair_ErfolgSchreibtBeideInReihenfolge: der Normalfall. Ohne
// diesen Test wäre der obige auch mit einer Fassung grün, die schlicht gar
// nichts schreibt.
func TestAppendTurnPair_ErfolgSchreibtBeideInReihenfolge(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()

	if _, err := repo.CreateConversation(ctx, CreateConversationParams{
		ID: "conv-ok", BoardID: "b1", CardID: "card-1",
		Provider: "anthropic", Model: "claude-opus-4-7", CreatedBy: "andi@example.com",
	}); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	in, out := 11, 22
	kosten := 0.42
	if err := repo.AppendTurnPair(ctx,
		AppendTurnParams{ID: "t-u", ConversationID: "conv-ok", TS: "2026-08-19T10:00:00.000000001Z",
			Role: "user", Content: "Frage"},
		AppendTurnParams{ID: "t-a", ConversationID: "conv-ok", TS: "2026-08-19T10:00:00.000000002Z",
			Role: "assistant", Content: "Antwort",
			InputTokens: &in, OutputTokens: &out, CostEstimate: &kosten},
	); err != nil {
		t.Fatalf("AppendTurnPair: %v", err)
	}

	turns, err := repo.LoadTurns(ctx, "conv-ok")
	if err != nil {
		t.Fatalf("LoadTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("erwartet 2 Turns, gefunden %d", len(turns))
	}
	if turns[0].Role != "user" || turns[1].Role != "assistant" {
		t.Fatalf("Reihenfolge: %q, %q — LoadTurns sortiert nach TS, die Rollen müssen "+
			"user-vor-assistant stehen", turns[0].Role, turns[1].Role)
	}
	// Die Zahlenfelder gehören nur an den Assistant-Turn — sonst zählt eine
	// Kostenauswertung sie doppelt.
	if turns[0].InputTokens != nil || turns[0].CostEstimate != nil {
		t.Errorf("User-Turn trägt Token-/Kostenfelder: %+v", turns[0])
	}
	if turns[1].InputTokens == nil || *turns[1].InputTokens != in {
		t.Errorf("Assistant-Turn ohne InputTokens: %+v", turns[1])
	}
	if turns[1].CostEstimate == nil || *turns[1].CostEstimate != kosten {
		t.Errorf("Assistant-Turn ohne CostEstimate: %+v", turns[1])
	}
}

// TestAppendTurnPair_LehntFalscheRollenAb: die Reihenfolge user→assistant ist
// Teil des Vertrags, nicht Konvention. Ein vertauschtes Paar würde in der
// Provider-History eine Antwort vor die Frage stellen.
func TestAppendTurnPair_LehntFalscheRollenAb(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()

	faelle := []struct {
		name            string
		user, assistant AppendTurnParams
	}{
		{"vertauscht",
			AppendTurnParams{ID: "a", ConversationID: "c", Role: "assistant", Content: "x"},
			AppendTurnParams{ID: "b", ConversationID: "c", Role: "user", Content: "y"}},
		{"leerer User-Inhalt",
			AppendTurnParams{ID: "a", ConversationID: "c", Role: "user", Content: ""},
			AppendTurnParams{ID: "b", ConversationID: "c", Role: "assistant", Content: "y"}},
		{"verschiedene Conversations",
			AppendTurnParams{ID: "a", ConversationID: "c1", Role: "user", Content: "x"},
			AppendTurnParams{ID: "b", ConversationID: "c2", Role: "assistant", Content: "y"}},
	}
	for _, f := range faelle {
		t.Run(f.name, func(t *testing.T) {
			if err := repo.AppendTurnPair(ctx, f.user, f.assistant); err == nil {
				t.Fatal("AppendTurnPair akzeptierte ein ungültiges Paar")
			}
		})
	}
}

// TestAppendTurnPair_LeererAssistantTextIstErlaubt hält F-610 fest: Eine reine
// Tool-Call-Antwort hat KEINEN Text. `result.Text` wird ausschliesslich aus
// `text_delta`-Ereignissen gefüllt; eine Antwort nur mit `tool_use`-Blöcken
// liefert keine. Unter ADR-0096 §3/§4 ist das eine erwartete Antwortform, nicht
// eine Ausnahme.
//
// Die erste Fassung von AppendTurnPair lehnte den Leerstring auch für den
// Assistant-Turn ab — und machte damit aus einem halben Verlust einen ganzen:
// vorher blieb bei einem Fehler wenigstens der User-Turn stehen, danach fehlte
// die komplette Runde. Der nächste Multi-Turn-Aufruf sah dann weder Frage noch
// Antwort, obwohl auf der Karte ein Kommentar stand.
//
// GEGENGEPROBT (Sprint-55-Nacharbeit): Mit der alten Bedingung
// (`p.Content == ""` für beide Turns) ist dieser Test rot.
func TestAppendTurnPair_LeererAssistantTextIstErlaubt(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()

	if _, err := repo.CreateConversation(ctx, CreateConversationParams{
		ID: "conv-610", BoardID: "b1", CardID: "card-1",
		Provider: "anthropic", Model: "claude-opus-4-7", CreatedBy: "andi@example.com",
	}); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	if err := repo.AppendTurnPair(ctx,
		AppendTurnParams{ID: "t-u", ConversationID: "conv-610", TS: "2026-08-19T10:00:00.000000001Z",
			Role: "user", Content: "setz die Karte auf qa"},
		AppendTurnParams{ID: "t-a", ConversationID: "conv-610", TS: "2026-08-19T10:00:00.000000002Z",
			Role: "assistant", Content: ""}, // reine Tool-Call-Antwort
	); err != nil {
		t.Fatalf("AppendTurnPair lehnt eine reine Tool-Call-Antwort ab: %v", err)
	}

	turns, err := repo.LoadTurns(ctx, "conv-610")
	if err != nil {
		t.Fatalf("LoadTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("erwartet 2 Turns, gefunden %d — die Runde fehlt in der "+
			"Provider-History, obwohl auf der Karte ein Kommentar steht", len(turns))
	}
	if turns[0].Content != "setz die Karte auf qa" {
		t.Errorf("User-Turn-Inhalt: %q", turns[0].Content)
	}
	if turns[1].Content != "" {
		t.Errorf("Assistant-Turn-Inhalt: %q, erwartet leer", turns[1].Content)
	}
}

// ── ADR-0129 §5/Karte #572: Frist für LLM-Verläufe ───────────────────────

// TestLLMConv_EvictExpired_MassgeblichIstJuengsterTurn belegt die
// zentrale Festlegung: Eine Conversation mit altem created_at, aber einem
// JUNGEN Turn, darf NICHT verschwinden — nur der jüngste Turn zählt für ihr
// Alter. Eine Conversation ganz ohne Turns fällt auf created_at zurück.
//
// Aufbau (Frist 10 Tage, "jetzt" = base):
//   - "alt-ohne-neuen-turn": created_at vor 20 Tagen, letzter Turn vor 20
//     Tagen → muss gelöscht werden (Turns cascaded mit).
//   - "alt-mit-neuem-turn": created_at vor 20 Tagen, letzter Turn vor 2
//     Tagen → muss BLEIBEN (Turn ist der Maßstab, nicht created_at).
//   - "jung-ohne-turns": created_at vor 2 Tagen, keine Turns → muss
//     bleiben (created_at ist hier der einzige Maßstab, und er ist jung).
//
// GEGENPROBE: Ersetzt man in EvictExpired() das COALESCE(MAX(turns.ts),
// created_at) durch ein reines `created_at < ?`, wird dieser Test rot —
// "alt-mit-neuem-turn" würde dann fälschlich gelöscht.
func TestLLMConv_EvictExpired_MassgeblichIstJuengsterTurn(t *testing.T) {
	repo, done := newConvTestRepo(t)
	defer done()
	ctx := context.Background()

	origTimeNow := timeNow
	defer func() { timeNow = origTimeNow }()
	t.Setenv(llmRetentionDaysEnvVar, "10")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	alt := base.AddDate(0, 0, -20)
	jungTurn := base.AddDate(0, 0, -2)
	jungCreated := base.AddDate(0, 0, -2)

	// "alt-ohne-neuen-turn": created_at + letzter Turn beide vor 20 Tagen.
	timeNow = func() time.Time { return alt }
	if _, err := repo.CreateConversation(ctx, CreateConversationParams{
		ID: "alt-ohne-neuen-turn", BoardID: "b1", CardID: "c1",
		Provider: "anthropic", Model: "m", CreatedBy: "a@x",
	}); err != nil {
		t.Fatalf("Create alt-ohne-neuen-turn: %v", err)
	}
	if _, err := repo.AppendTurn(ctx, AppendTurnParams{
		ID: "t-alt", ConversationID: "alt-ohne-neuen-turn", Role: "user", Content: "hallo",
		TS: alt.Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("AppendTurn alt: %v", err)
	}

	// "alt-mit-neuem-turn": created_at vor 20 Tagen, aber gestern noch
	// fortgeschrieben.
	timeNow = func() time.Time { return alt }
	if _, err := repo.CreateConversation(ctx, CreateConversationParams{
		ID: "alt-mit-neuem-turn", BoardID: "b1", CardID: "c1",
		Provider: "anthropic", Model: "m", CreatedBy: "a@x",
	}); err != nil {
		t.Fatalf("Create alt-mit-neuem-turn: %v", err)
	}
	if _, err := repo.AppendTurn(ctx, AppendTurnParams{
		ID: "t-jung", ConversationID: "alt-mit-neuem-turn", Role: "user", Content: "hallo nochmal",
		TS: jungTurn.Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("AppendTurn jung: %v", err)
	}

	// "jung-ohne-turns": frisch angelegt, nie beschrieben.
	timeNow = func() time.Time { return jungCreated }
	if _, err := repo.CreateConversation(ctx, CreateConversationParams{
		ID: "jung-ohne-turns", BoardID: "b1", CardID: "c1",
		Provider: "anthropic", Model: "m", CreatedBy: "a@x",
	}); err != nil {
		t.Fatalf("Create jung-ohne-turns: %v", err)
	}

	// "Jetzt": löst EvictExpired mit Frist 10 Tage aus.
	timeNow = func() time.Time { return base }
	n, err := repo.EvictExpired(ctx)
	if err != nil {
		t.Fatalf("EvictExpired: %v", err)
	}
	if n != 1 {
		t.Errorf("EvictExpired meldet %d gelöschte Conversation(s), erwartet 1", n)
	}

	if _, err := repo.GetConversation(ctx, "alt-ohne-neuen-turn"); !errors.Is(err, ErrLLMConversationNotFound) {
		t.Errorf("alt-ohne-neuen-turn hätte gelöscht sein müssen, err=%v", err)
	}
	if turnsAfter, _ := repo.LoadTurns(ctx, "alt-ohne-neuen-turn"); len(turnsAfter) != 0 {
		t.Errorf("Turns von alt-ohne-neuen-turn nicht mitgelöscht (CASCADE): %+v", turnsAfter)
	}

	if _, err := repo.GetConversation(ctx, "alt-mit-neuem-turn"); err != nil {
		t.Errorf("alt-mit-neuem-turn haette bleiben muessen (juengster Turn ist jung), err=%v", err)
	}
	if _, err := repo.GetConversation(ctx, "jung-ohne-turns"); err != nil {
		t.Errorf("jung-ohne-turns haette bleiben muessen (created_at ist jung), err=%v", err)
	}
}

// TestLLMRetentionDays_WarntNurEinmal (analog
// TestActivityRetentionDays_WarntNurEinmal, F-1148): Ein ungültiger Wert
// fällt auf die Voreinstellung zurück und warnt genau EINMAL je Prozess.
func TestLLMRetentionDays_WarntNurEinmal(t *testing.T) {
	llmRetentionWarnungEinmal = sync.Once{}
	defer func() { llmRetentionWarnungEinmal = sync.Once{} }()

	t.Setenv(llmRetentionDaysEnvVar, "abc")
	for i := 0; i < 3; i++ {
		if got := llmRetentionDays(); got != LLMRetentionDaysDefault {
			t.Errorf("Lauf %d: llmRetentionDays() = %d, erwartet Voreinstellung %d", i, got, LLMRetentionDaysDefault)
		}
	}
}

// TestLLMRetentionDays_UngueltigeWerte deckt die Randfälle (leer, negativ,
// null, keine Zahl) — alle fallen auf die Voreinstellung zurück; nur ein
// gültiger positiver Wert wird übernommen.
func TestLLMRetentionDays_UngueltigeWerte(t *testing.T) {
	llmRetentionWarnungEinmal = sync.Once{}
	defer func() { llmRetentionWarnungEinmal = sync.Once{} }()

	if got := llmRetentionDays(); got != LLMRetentionDaysDefault {
		t.Errorf("ohne Env-Var: %d, erwartet %d", got, LLMRetentionDaysDefault)
	}
	for _, raw := range []string{"0", "-5", "nicht-numerisch"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(llmRetentionDaysEnvVar, raw)
			if got := llmRetentionDays(); got != LLMRetentionDaysDefault {
				t.Errorf("%s: %d, erwartet Voreinstellung %d", raw, got, LLMRetentionDaysDefault)
			}
		})
	}
	t.Setenv(llmRetentionDaysEnvVar, "42")
	if got := llmRetentionDays(); got != 42 {
		t.Errorf("gültiger Wert 42 wird nicht übernommen, got %d", got)
	}
}
