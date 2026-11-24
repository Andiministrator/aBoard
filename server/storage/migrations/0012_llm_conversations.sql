-- aBoard server schema, version 12 — LLM-Conversations + Turns (#300, ADR-0096 §2).
--
-- Zwei Tabellen für Multi-Turn-LLM-Conversations pro Karte:
--
--   llm_conversations  — pro Karte 0..N Konversationen. Eine Conversation
--                        gehört zu genau einer Karte und einem `created_by`
--                        (User-Email, normalisiert). Cross-Card-Multi-Turn
--                        ist explizit ausgeschlossen — sonst explodieren
--                        die ACL-Fragen (ADR-0096 §2).
--
--   llm_turns          — Turns (user/assistant) einer Conversation, geordnet
--                        nach ts. assistant-Turns tragen Token-Counts +
--                        cost_estimate (REAL USD); user-Turns nicht.
--                        FK zu llm_conversations mit ON DELETE CASCADE —
--                        Conversation löschen räumt die Turns mit.
--
-- ACL (Code, nicht Schema):
--   - Lesen: board-weit viewer+ (jeder, der die Karte sieht, sieht ihre
--     Conversations + Turns).
--   - Schreiben (Turn hinzufügen, archive, delete): nur `created_by` selbst.
--
-- Archivieren ist ein Soft-Delete via `archived_at` (NULL = aktiv). UI-Limit
-- für „aktive Conversations pro Karte" ist 10 (LIMIT 10 ORDER BY created_at
-- DESC WHERE archived_at IS NULL); harter Cap im Schema bewusst nicht —
-- archivierte Conversations bleiben für späteren Recall lesbar.
--
-- Token-Budget-Gate: pro Conversation summiert der Server die
-- input_tokens-Schätzung aller Turns + den aktuellen User-Prompt vor jedem
-- neuen Send. Bei Überschreitung von 80 % des Provider-Context-Limits wirft
-- der Stream-Endpoint 400 mit kind="context-budget" zurück (ADR-0096 §2).

CREATE TABLE llm_conversations (
    id          TEXT PRIMARY KEY,
    board_id    TEXT NOT NULL,
    card_id     TEXT NOT NULL,
    provider    TEXT NOT NULL CHECK (provider IN ('anthropic','openai','google','mistral')),
    model       TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    archived_at TEXT
);

CREATE INDEX idx_llm_conversations_card ON llm_conversations(card_id, created_at);
CREATE INDEX idx_llm_conversations_user ON llm_conversations(created_by, created_at);

CREATE TABLE llm_turns (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES llm_conversations(id) ON DELETE CASCADE,
    ts              TEXT NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content         TEXT NOT NULL,
    input_tokens    INTEGER,
    output_tokens   INTEGER,
    cost_estimate   REAL
);

CREATE INDEX idx_llm_turns_conv ON llm_turns(conversation_id, ts);
