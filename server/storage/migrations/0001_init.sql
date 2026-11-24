-- aBoard server schema, version 1.
-- Quelle: ADR-0044 §"SQLite-Schema (Phase 1)".
-- Karten/Sprints/Kommentare bleiben opak in boards.state_json — keine relationale
-- Zerlegung in Phase 1 (Begründung in ADR-0044).

CREATE TABLE boards (
  id          TEXT PRIMARY KEY,
  title       TEXT NOT NULL,
  version     TEXT NOT NULL,
  state_json  TEXT NOT NULL,
  updated_at  INTEGER NOT NULL,
  created_at  INTEGER NOT NULL
);

CREATE TABLE attachments (
  id            TEXT PRIMARY KEY,
  board_id      TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  card_id       TEXT NOT NULL,
  filename      TEXT NOT NULL,
  mime          TEXT NOT NULL,
  size          INTEGER NOT NULL,
  storage_path  TEXT NOT NULL,
  created_at    INTEGER NOT NULL
);

CREATE INDEX idx_attachments_card ON attachments(board_id, card_id);
