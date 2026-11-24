-- aBoard server schema, version 31 — Personen-Token für mehrere Boards
-- (ADR-0139 §5, Entscheid E-1, Karte #754; ändert ADR-0130 §6).
--
-- agent_tokens.mehrere = 1: Das Token gehört einer Person und gilt für eine
-- feste Liste von Boards (Tabelle agent_token_boards). Es hat selbst kein
-- Board (board_id NULL) und erreicht deshalb nur die Werkzeug-Wege
-- (/werkzeuge/…, /openapi.json, /mcp) — die rohen Ops bleiben bei einem Board
-- je Token (ADR-0139 §5). In der Liste stehen nur Boards, auf denen die Person
-- bei der Anlage einen PERSÖNLICHEN ACL-Eintrag mit mindestens editor hatte
-- (ADR-0132 §6, die Domain-Freigabe zählt nicht); geprüft wird bei jedem
-- Aufruf erneut gegen Liste und ACL (api/agent.go, AgentTokenRepo.RolleAufBoard).
--
-- Der CHECK auf die Token-Form muss erweitert werden. SQLite kann einen CHECK
-- nicht nachträglich ändern, deshalb wird die Tabelle wie in 0024/0025 neu
-- aufgebaut („neue Tabelle, kopieren, alte weg, umbenennen“). Der Trigger aus
-- 0018 wird vorher entfernt und danach unverändert neu angelegt, ebenso die
-- drei Indizes aus 0017 und 0023.
--
-- Drei Formen, gegenseitig ausschliessend:
--   gewöhnlich  — delegierend = 0, mehrere = 0, board_id gesetzt
--   delegierend — ohne Board und ohne Person (ADR-0133 §4)
--   mehrere     — ohne Board, MIT Person, nie vorschlag (die Rolle gibt es
--                 nur als Projekt-Token, ADR-0133 §6)
--
-- agent_token_boards hat bewusst KEINEN Fremdschlüssel auf agent_tokens: Jeder
-- künftige Neuaufbau von agent_tokens (DROP TABLE) löschte sonst bei aktivem
-- foreign_keys still alle Board-Listen mit. Tokens werden nie gelöscht, nur
-- widerrufen; ein Token mit mehrere = 1 hat kein board_id und fällt deshalb
-- auch keiner Board-Löschung zum Opfer. Der Fremdschlüssel auf boards löscht
-- beim Löschen eines Boards dessen Zeile aus jeder Liste; der Trigger unten
-- vermerkt das im Audit, solange das Token aktiv ist.

DROP TRIGGER agent_tokens_mit_board_geloescht;

CREATE TABLE agent_tokens_neu (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  board_id    TEXT REFERENCES boards(id) ON DELETE CASCADE,
  role        TEXT NOT NULL CHECK (role IN ('editor', 'viewer', 'vorschlag')),
  token_hash  TEXT NOT NULL UNIQUE,
  created_at  TEXT NOT NULL,
  created_by  TEXT NOT NULL DEFAULT '',
  revoked_at  TEXT,
  user_id     TEXT,
  expires_at  TEXT,
  kennung     TEXT,
  delegierend INTEGER NOT NULL DEFAULT 0 CHECK (delegierend IN (0, 1)),
  mehrere     INTEGER NOT NULL DEFAULT 0 CHECK (mehrere IN (0, 1)),
  CHECK ((delegierend = 0 AND mehrere = 0 AND board_id IS NOT NULL)
      OR (delegierend = 1 AND mehrere = 0 AND board_id IS NULL AND user_id IS NULL)
      OR (delegierend = 0 AND mehrere = 1 AND board_id IS NULL AND user_id IS NOT NULL)),
  CHECK (role <> 'vorschlag' OR (delegierend = 0 AND mehrere = 0 AND user_id IS NULL))
);

INSERT INTO agent_tokens_neu (id, name, board_id, role, token_hash, created_at, created_by, revoked_at,
                              user_id, expires_at, kennung, delegierend, mehrere)
SELECT id, name, board_id, role, token_hash, created_at, created_by, revoked_at,
       user_id, expires_at, kennung, delegierend, 0
  FROM agent_tokens;

DROP TABLE agent_tokens;
ALTER TABLE agent_tokens_neu RENAME TO agent_tokens;

CREATE UNIQUE INDEX agent_tokens_name_aktiv ON agent_tokens(name) WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX agent_tokens_kennung ON agent_tokens(kennung) WHERE kennung IS NOT NULL;
CREATE INDEX agent_tokens_user ON agent_tokens(user_id) WHERE user_id IS NOT NULL;

CREATE TRIGGER agent_tokens_mit_board_geloescht
AFTER DELETE ON agent_tokens
WHEN OLD.revoked_at IS NULL
BEGIN
  INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
  VALUES (lower(hex(randomblob(16))), 'agent-token:' || OLD.name, '', 'agent-token-revoke-board-deleted',
          NULL, 'board=' || OLD.board_id || ';role=' || OLD.role, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;

CREATE TABLE agent_token_boards (
  token_id TEXT NOT NULL,
  board_id TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  PRIMARY KEY (token_id, board_id)
);

CREATE INDEX agent_token_boards_board ON agent_token_boards(board_id);

CREATE TRIGGER agent_token_boards_board_geloescht
AFTER DELETE ON agent_token_boards
WHEN EXISTS (SELECT 1 FROM agent_tokens t WHERE t.id = OLD.token_id AND t.revoked_at IS NULL)
BEGIN
  INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
  SELECT lower(hex(randomblob(16))), 'agent-token:' || t.name, '', 'agent-token-board-removed',
         NULL, 'board=' || OLD.board_id, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
    FROM agent_tokens t WHERE t.id = OLD.token_id;
END;
