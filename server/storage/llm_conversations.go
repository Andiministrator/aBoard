package storage

// llm_conversations.go — Multi-Turn-LLM-Conversation-Storage (#300, ADR-0096 §2).
//
// Pro Karte 0..N Conversations. Eine Conversation hält N Turns (user/assistant);
// jeder assistant-Turn trägt Token-Counts + cost_estimate (für Budget-Gate +
// Audit). Cross-Card-Conversations sind nicht modelliert (ADR-0096 §2).
//
// ACL wird im Handler-Layer durchgesetzt (board-weit viewer+ Lesen,
// created_by Schreiben). Das Repo gibt zurück, was es findet — der Caller
// gleicht gegen die Karten-ACL ab.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
)

// ErrLLMConversationNotFound signalisiert, dass die angefragte Conversation
// (id) nicht existiert. Wird auch bei „existiert auf anderer Karte" geliefert,
// wenn der Caller eine cardID-Klausel anfügt — das Repo prüft das nicht von
// sich aus.
var ErrLLMConversationNotFound = errors.New("storage: llm conversation nicht gefunden")

// LLMConversation — Conversation-Header (ohne Turns). Felder sind exakt das
// Schema-Mapping. Turns lädt man separat via LoadTurns(id) — vermeidet
// N+1 bei List-Aufrufen.
type LLMConversation struct {
	ID         string  `json:"id"`
	BoardID    string  `json:"boardId"`
	CardID     string  `json:"cardId"`
	Provider   string  `json:"provider"`
	Model      string  `json:"model"`
	CreatedBy  string  `json:"createdBy"`
	CreatedAt  string  `json:"createdAt"`
	ArchivedAt *string `json:"archivedAt,omitempty"`
}

// LLMTurn — ein Turn-Eintrag. InputTokens/OutputTokens/CostEstimate sind nur
// bei assistant-Turns gesetzt; bei user-Turns sind sie null/zero. Wir
// verwenden Pointer/sql.Null, damit das im JSON sauber als omitempty fällt.
type LLMTurn struct {
	ID             string   `json:"id"`
	ConversationID string   `json:"conversationId"`
	TS             string   `json:"ts"`
	Role           string   `json:"role"` // 'user' | 'assistant'
	Content        string   `json:"content"`
	InputTokens    *int     `json:"inputTokens,omitempty"`
	OutputTokens   *int     `json:"outputTokens,omitempty"`
	CostEstimate   *float64 `json:"costEstimate,omitempty"`
}

// LLMConversationRepo verwaltet llm_conversations + llm_turns.
type LLMConversationRepo struct {
	db *sql.DB
}

// NewLLMConversationRepo legt ein neues Repo an.
func NewLLMConversationRepo(db *sql.DB) *LLMConversationRepo {
	return &LLMConversationRepo{db: db}
}

// CreateConversationParams bündelt die Pflichtfelder einer neuen Conversation.
// ID muss vom Caller vergeben werden (UUID), damit Idempotenz-Strategien
// (Replay-Schutz) caller-seitig möglich bleiben.
type CreateConversationParams struct {
	ID        string
	BoardID   string
	CardID    string
	Provider  string
	Model     string
	CreatedBy string
}

// CreateConversation legt eine neue (aktive) Conversation an. created_at wird
// auf nowISO() gesetzt. Bei doppelter ID liefert SQLite einen UNIQUE-Fehler —
// Caller-Verantwortung, eine frische UUID zu reichen.
func (r *LLMConversationRepo) CreateConversation(ctx context.Context, p CreateConversationParams) (LLMConversation, error) {
	if p.ID == "" || p.BoardID == "" || p.CardID == "" || p.Provider == "" || p.Model == "" || p.CreatedBy == "" {
		return LLMConversation{}, fmt.Errorf("llm-conv.create: id/board/card/provider/model/createdBy pflicht")
	}
	ts := nowISO()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO llm_conversations (id, board_id, card_id, provider, model, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.BoardID, p.CardID, p.Provider, p.Model, p.CreatedBy, ts); err != nil {
		return LLMConversation{}, fmt.Errorf("llm-conv.create: %w", err)
	}
	return LLMConversation{
		ID:        p.ID,
		BoardID:   p.BoardID,
		CardID:    p.CardID,
		Provider:  p.Provider,
		Model:     p.Model,
		CreatedBy: p.CreatedBy,
		CreatedAt: ts,
	}, nil
}

// ListConversationsForCard liefert alle aktiven Conversations einer Karte
// (archived_at IS NULL), neueste zuerst, harter LIMIT 10 (ADR-0096 §2 UI-Cap).
//
// includeArchived = true blendet archivierte Conversations ein (für „Archiv
// einblenden"-UI). Die Reihenfolge bleibt created_at DESC.
func (r *LLMConversationRepo) ListConversationsForCard(ctx context.Context, boardID, cardID string, includeArchived bool) ([]LLMConversation, error) {
	return r.listConversations(ctx, boardID, cardID, "", includeArchived)
}

// ListConversationsForCardBy liefert wie ListConversationsForCard, aber nur
// die Conversations von createdBy (Firmenmodus, ADR-0133 Nachtrag 2 A5).
//
// Der Filter steht bewusst in der Abfrage und nicht im Aufrufer: Mit dem
// LIMIT 10 fielen eigene Verläufe sonst still heraus, sobald andere Personen
// zehn jüngere an derselben Karte haben. Leeres createdBy ist ein Fehler —
// es hiesse sonst „alle“, das Gegenteil des Gewollten.
func (r *LLMConversationRepo) ListConversationsForCardBy(ctx context.Context, boardID, cardID, createdBy string, includeArchived bool) ([]LLMConversation, error) {
	createdBy = NormalizeEmail(createdBy)
	if createdBy == "" {
		return nil, fmt.Errorf("llm-conv.list: createdBy pflicht")
	}
	return r.listConversations(ctx, boardID, cardID, createdBy, includeArchived)
}

// listConversations — gemeinsamer Kern; createdBy == "" heisst ohne Filter.
// lower(created_by) wie in user_loeschung.go: created_by wird zwar
// normalisiert geschrieben, ältere Zeilen sind aber nicht garantiert.
func (r *LLMConversationRepo) listConversations(ctx context.Context, boardID, cardID, createdBy string, includeArchived bool) ([]LLMConversation, error) {
	q := `SELECT id, board_id, card_id, provider, model, created_by, created_at, archived_at
	      FROM llm_conversations
	      WHERE board_id = ? AND card_id = ?`
	args := []any{boardID, cardID}
	if createdBy != "" {
		q += ` AND lower(created_by) = ?`
		args = append(args, createdBy)
	}
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	q += ` ORDER BY created_at DESC LIMIT 10`

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("llm-conv.list: %w", err)
	}
	defer rows.Close()
	var out []LLMConversation
	for rows.Next() {
		var c LLMConversation
		var archivedAt sql.NullString
		if err := rows.Scan(&c.ID, &c.BoardID, &c.CardID, &c.Provider, &c.Model, &c.CreatedBy, &c.CreatedAt, &archivedAt); err != nil {
			return nil, fmt.Errorf("llm-conv.list scan: %w", err)
		}
		if archivedAt.Valid {
			s := archivedAt.String
			c.ArchivedAt = &s
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConversation liefert eine Conversation per ID. ErrLLMConversationNotFound
// wenn nicht existent.
func (r *LLMConversationRepo) GetConversation(ctx context.Context, id string) (LLMConversation, error) {
	var c LLMConversation
	var archivedAt sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT id, board_id, card_id, provider, model, created_by, created_at, archived_at
		 FROM llm_conversations WHERE id = ?`, id).
		Scan(&c.ID, &c.BoardID, &c.CardID, &c.Provider, &c.Model, &c.CreatedBy, &c.CreatedAt, &archivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return LLMConversation{}, ErrLLMConversationNotFound
	}
	if err != nil {
		return LLMConversation{}, fmt.Errorf("llm-conv.get: %w", err)
	}
	if archivedAt.Valid {
		s := archivedAt.String
		c.ArchivedAt = &s
	}
	return c, nil
}

// ArchiveConversation setzt archived_at auf nowISO(). Idempotent — re-archive
// aktualisiert den Zeitstempel. Bei nicht existenter ID:
// ErrLLMConversationNotFound.
func (r *LLMConversationRepo) ArchiveConversation(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE llm_conversations SET archived_at = ? WHERE id = ?`, nowISO(), id)
	if err != nil {
		return fmt.Errorf("llm-conv.archive: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("llm-conv.archive rows: %w", err)
	}
	if n == 0 {
		return ErrLLMConversationNotFound
	}
	return nil
}

// DeleteConversation entfernt eine Conversation samt ihrer Turns
// (FK ON DELETE CASCADE). ErrLLMConversationNotFound bei nicht existenter ID.
func (r *LLMConversationRepo) DeleteConversation(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM llm_conversations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("llm-conv.delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("llm-conv.delete rows: %w", err)
	}
	if n == 0 {
		return ErrLLMConversationNotFound
	}
	return nil
}

// AppendTurnParams bündelt einen neuen Turn. Bei role="user" werden
// InputTokens/OutputTokens/CostEstimate ignoriert; bei role="assistant"
// gespeichert (null wenn Pointer nil).
type AppendTurnParams struct {
	ID             string
	ConversationID string
	TS             string // ISO-8601; leer → nowISO()
	Role           string // 'user' | 'assistant'
	Content        string
	InputTokens    *int
	OutputTokens   *int
	CostEstimate   *float64
}

// AppendTurn hängt einen Turn an. Validiert role gegen CHECK-Constraint des
// Schemas (SQLite würde sonst einen unspezifischen CHECK-Fehler werfen).
// FK-Verstoß (unbekannte ConversationID) propagiert als Fehler.
func (r *LLMConversationRepo) AppendTurn(ctx context.Context, p AppendTurnParams) (LLMTurn, error) {
	if p.ID == "" || p.ConversationID == "" || p.Content == "" {
		return LLMTurn{}, fmt.Errorf("llm-conv.turn: id/conversationId/content pflicht")
	}
	if p.Role != "user" && p.Role != "assistant" {
		return LLMTurn{}, fmt.Errorf("llm-conv.turn: role muss 'user' oder 'assistant' sein, war %q", p.Role)
	}
	ts := p.TS
	if ts == "" {
		ts = nowISO()
	}

	var (
		inTok  any = nil
		outTok any = nil
		cost   any = nil
	)
	if p.Role == "assistant" {
		if p.InputTokens != nil {
			inTok = *p.InputTokens
		}
		if p.OutputTokens != nil {
			outTok = *p.OutputTokens
		}
		if p.CostEstimate != nil {
			cost = *p.CostEstimate
		}
	}

	if _, err := r.db.ExecContext(ctx, turnInsertSQL,
		p.ID, p.ConversationID, ts, p.Role, p.Content, inTok, outTok, cost); err != nil {
		return LLMTurn{}, fmt.Errorf("llm-conv.turn: %w", err)
	}
	out := LLMTurn{
		ID:             p.ID,
		ConversationID: p.ConversationID,
		TS:             ts,
		Role:           p.Role,
		Content:        p.Content,
	}
	if p.Role == "assistant" {
		out.InputTokens = p.InputTokens
		out.OutputTokens = p.OutputTokens
		out.CostEstimate = p.CostEstimate
	}
	return out, nil
}

// turnInsertSQL ist die einzige Stelle, an der ein Turn geschrieben wird —
// AppendTurn und AppendTurnPair teilen sie. Zwei getrennte INSERT-Texte wären
// eine zweite Wahrheit, die genau dann auseinanderläuft, wenn jemand eine
// Spalte ergänzt und nur eine der beiden anfasst.
const turnInsertSQL = `INSERT INTO llm_turns (id, conversation_id, ts, role, content, input_tokens, output_tokens, cost_estimate)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

// AppendTurnPair schreibt User- und Assistant-Turn in EINER Transaktion —
// entweder beide oder keiner (F-413).
//
// DAS PROBLEM: `llm_stream.go` rief zweimal AppendTurn hintereinander und
// behandelte einen Fehler, indem es die zurückgegebenen IDs leerte. Das räumt
// nichts auf. Schlug der zweite Aufruf fehl (oder starb der Prozess dazwischen),
// blieb ein **User-Turn ohne Antwort** in der Tabelle stehen.
//
// WARUM DAS MEHR IST ALS EIN LÜCKENHAFTER AUDIT-TRAIL: `LoadTurns` speist die
// Provider-History des nächsten Multi-Turn-Aufrufs. Ein User-Turn ohne
// Assistant-Turn erscheint dem Modell dort als Frage, die es unbeantwortet
// gelassen hat — und manche Anbieter lehnen zwei aufeinanderfolgende
// user-Rollen sogar rundheraus ab. Der Defekt verzerrt also die nächste
// Antwort, nicht nur die Statistik.
//
// Kompensierendes Löschen war die Alternative und wurde verworfen: Der
// Kompensations-DELETE kann selbst fehlschlagen, und dann steht man wieder
// genau hier. Eine Transaktion kann das nicht.
func (r *LLMConversationRepo) AppendTurnPair(ctx context.Context, user, assistant AppendTurnParams) error {
	if user.Role != "user" {
		return fmt.Errorf("llm-conv.turnpair: erster Turn muss role 'user' haben, war %q", user.Role)
	}
	if assistant.Role != "assistant" {
		return fmt.Errorf("llm-conv.turnpair: zweiter Turn muss role 'assistant' haben, war %q", assistant.Role)
	}
	for _, p := range []AppendTurnParams{user, assistant} {
		if p.ID == "" || p.ConversationID == "" {
			return fmt.Errorf("llm-conv.turnpair: id/conversationId pflicht (role %q)", p.Role)
		}
	}
	// Der USER-Turn braucht Inhalt — eine leere Frage gibt es nicht.
	if user.Content == "" {
		return fmt.Errorf("llm-conv.turnpair: content pflicht (role \"user\")")
	}
	// F-610: Der ASSISTANT-Turn darf leer sein, und das ist kein Sonderfall,
	// sondern der Normalfall einer reinen Tool-Call-Antwort. `result.Text` wird
	// ausschliesslich aus `text_delta`-Ereignissen gefüllt; eine Antwort, die
	// nur `tool_use`-Blöcke enthält, liefert keine. Unter ADR-0096 §3/§4 ist
	// genau das eine erwartete Antwortform.
	//
	// Die erste Fassung lehnte auch hier den Leerstring ab — und machte damit
	// aus einem halben Verlust einen ganzen: Vorher blieb bei einem Fehler
	// wenigstens der User-Turn stehen; danach fehlte die komplette Runde. Der
	// nächste Multi-Turn-Aufruf sieht dann weder Frage noch Antwort, obwohl auf
	// der Karte ein Kommentar steht, und das Modell wiederholt seinen Vorschlag.
	//
	// Das Schema erlaubt den Leerstring ausdrücklich (`content TEXT NOT NULL`,
	// keine CHECK-Bedingung) — die Ablehnung war eine Anwendungsentscheidung,
	// und zwar die falsche.
	if user.ConversationID != assistant.ConversationID {
		return fmt.Errorf("llm-conv.turnpair: beide Turns müssen zur selben Conversation gehören")
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("llm-conv.turnpair: begin: %w", err)
	}
	// Rollback nach erfolgreichem Commit ist ein No-op (sql.ErrTxDone) — das
	// defer darf deshalb bedingungslos stehen und deckt auch den Panik-Pfad ab.
	defer func() { _ = tx.Rollback() }()

	for _, p := range []AppendTurnParams{user, assistant} {
		ts := p.TS
		if ts == "" {
			ts = nowISO()
		}
		var inTok, outTok, cost any
		if p.Role == "assistant" {
			if p.InputTokens != nil {
				inTok = *p.InputTokens
			}
			if p.OutputTokens != nil {
				outTok = *p.OutputTokens
			}
			if p.CostEstimate != nil {
				cost = *p.CostEstimate
			}
		}
		if _, err := tx.ExecContext(ctx, turnInsertSQL,
			p.ID, p.ConversationID, ts, p.Role, p.Content, inTok, outTok, cost); err != nil {
			return fmt.Errorf("llm-conv.turnpair (%s): %w", p.Role, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("llm-conv.turnpair: commit: %w", err)
	}
	return nil
}

// LoadTurns lädt alle Turns einer Conversation, älteste zuerst (TS ASC).
// Leeres Slice ist legitim (frische Conversation ohne Turns).
func (r *LLMConversationRepo) LoadTurns(ctx context.Context, conversationID string) ([]LLMTurn, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, conversation_id, ts, role, content, input_tokens, output_tokens, cost_estimate
		 FROM llm_turns WHERE conversation_id = ? ORDER BY ts ASC, id ASC`,
		conversationID)
	if err != nil {
		return nil, fmt.Errorf("llm-conv.turns: %w", err)
	}
	defer rows.Close()
	var out []LLMTurn
	for rows.Next() {
		var t LLMTurn
		var (
			inTok  sql.NullInt64
			outTok sql.NullInt64
			cost   sql.NullFloat64
		)
		if err := rows.Scan(&t.ID, &t.ConversationID, &t.TS, &t.Role, &t.Content, &inTok, &outTok, &cost); err != nil {
			return nil, fmt.Errorf("llm-conv.turns scan: %w", err)
		}
		if inTok.Valid {
			v := int(inTok.Int64)
			t.InputTokens = &v
		}
		if outTok.Valid {
			v := int(outTok.Int64)
			t.OutputTokens = &v
		}
		if cost.Valid {
			v := cost.Float64
			t.CostEstimate = &v
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// LLMRetentionDaysDefault ist die Voreinstellung für die Frist, wenn
// ABOARD_LLM_RETENTION_DAYS fehlt oder ungültig ist (ADR-0129 §5, Karte
// #572). Gleiche Größenordnung wie die Aktivitäten-Frist (365 Tage, Karte
// #554) — ein LLM-Verlauf ist wie das Activity-Log ein Nutzungsprotokoll,
// kein Nachweis-Dokument wie das Audit (siehe AuditRetentionDaysDefault in
// server_config.go, dort länger begründet).
const LLMRetentionDaysDefault = 365

// llmRetentionDaysEnvVar ist der Name der überschreibenden Env-Var.
const llmRetentionDaysEnvVar = "ABOARD_LLM_RETENTION_DAYS"

// llmRetentionWarnungEinmal verhindert Log-Flut analog
// activities.go/retentionWarnungEinmal (F-1148): EvictExpired läuft
// periodisch (server/retention), nicht bei jedem Schreibvorgang — die
// Gefahr einer Flut ist hier kleiner, aber sync.Once kostet nichts und hält
// beide Fristen im selben Muster.
var llmRetentionWarnungEinmal sync.Once

// llmRetentionDays liest die Frist (in Tagen) aus der Umgebung. Fehlender
// oder ungültiger Wert (keine ganze Zahl, <= 0) fällt auf die Voreinstellung
// zurück — bei ungültigem (aber gesetztem) Wert mit einer einmaligen
// Log-Warnung, analog activityRetentionDays().
func llmRetentionDays() int {
	raw := os.Getenv(llmRetentionDaysEnvVar)
	if raw == "" {
		return LLMRetentionDaysDefault
	}
	n, err := strconv.Atoi(raw)
	if err != nil || !fristGueltig(n) {
		llmRetentionWarnungEinmal.Do(func() {
			log.Printf("aboard-server: %s=%q ungültig (erwartet ganze Zahl 1–%d), verwende Voreinstellung %d Tage",
				llmRetentionDaysEnvVar, raw, FristTageMax, LLMRetentionDaysDefault)
		})
		return LLMRetentionDaysDefault
	}
	return n
}

// EvictExpired löscht Conversations — und per FK ON DELETE CASCADE ihre
// Turns —, deren Alter die konfigurierte Frist (llmRetentionDays)
// überschreitet (ADR-0129 §5, Karte #572).
//
// MASSGEBLICH FÜR DAS ALTER IST DER JÜNGSTE TURN, NICHT created_at: Eine
// Conversation, die gestern noch fortgeschrieben wurde, darf nicht wegen
// eines alten created_at verschwinden, während sie im Budget-Gate
// (LoadTurns, ADR-0096 §2) und im UI (LoadTurns/ListConversationsForCard) weiter
// aktiv ist. Nur eine Conversation GANZ OHNE Turns (z. B. wenn der erste
// AppendTurnPair-Aufruf nie stattfand) fällt auf created_at zurück — sonst
// gäbe es für sie gar keinen Alters-Maßstab.
//
// Vergleich über die ersten 19 Zeichen des Zeitstempels (Sekunden-
// Genauigkeit), analog activities.go trim(): nowISO() (RFC3339Nano) lässt
// Null-Nachkommastellen weg, ein voller String-Vergleich sortierte dadurch
// falsch (dort ausführlich hergeleitet). Bewusst OHNE zusätzlichen Index
// (Unterschied zu activities, dort seit F-1157/Migration 0019 mit Index): Eine
// Conversation entsteht nur durch eine bewusste Nutzeraktion im
// LLM-Dialog einer Karte, nicht je Op wie ein activities-Eintrag. Eine harte
// Obergrenze gibt es nicht — LIMIT 10 gilt nur beim Auflisten der aktiven
// Conversations (ADR-0096 §2), archivierte zählen nicht mit; die Tabelle
// bleibt trotzdem um Größenordnungen kleiner als activities. Ein
// periodischer Lauf statt eines Trims je Schreibvorgang (wie bei
// activities.Record): AppendTurn/AppendTurnPair laufen bei jedem
// Chat-Turn — ein Trim bei jedem davon wäre unnötige Last für eine Frist,
// die ohnehin nur tage-granular gemessen wird. Der periodische Aufrufer
// steht in server/retention.
func (r *LLMConversationRepo) EvictExpired(ctx context.Context) (int64, error) {
	cutoff, ok := fristStichtag(llmRetentionDays())
	if !ok {
		return 0, nil
	}
	const delQ = `
		DELETE FROM llm_conversations
		WHERE substr(
			COALESCE(
				(SELECT MAX(t.ts) FROM llm_turns t WHERE t.conversation_id = llm_conversations.id),
				created_at
			), 1, 19) < ?`
	res, err := r.db.ExecContext(ctx, delQ, cutoff)
	if err != nil {
		return 0, fmt.Errorf("llm-conv.evict: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
