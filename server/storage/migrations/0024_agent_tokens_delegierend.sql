-- aBoard server schema, version 24 — delegierendes Dienst-Token für Open WebUI
-- (ADR-0133 §4, Karte #669).
--
-- agent_tokens.delegierend = 1: Das Token gehört keinem Board und keiner
-- Person. Es allein berechtigt zu nichts; jede Anfrage damit muss ein gültig
-- signiertes Nutzer-JWT von Open WebUI tragen, und die wirksame Rolle ist die
-- ACL-Rolle dieser Person auf dem Board aus dem Pfad (inkl. Domain-Freigabe),
-- gekappt auf die Token-Rolle (höchstens editor). Geprüft in
-- server/api/agent_owui.go.
--
-- Dafür muss board_id NULL sein dürfen. SQLite kann ein NOT NULL nicht
-- nachträglich entfernen, deshalb wird die Tabelle neu aufgebaut (Muster
-- „neue Tabelle, kopieren, alte weg, umbenennen“). Nichts verweist per
-- Fremdschlüssel AUF agent_tokens, das implizite DELETE beim DROP trifft also
-- keine andere Tabelle und löst laut SQLite-Doku keinen Trigger aus. Der
-- Trigger aus 0018 wird trotzdem vorher ausdrücklich entfernt und danach
-- unverändert neu angelegt, ebenso die drei Indizes aus 0017 und 0023.
--
-- Der CHECK hält die beiden Formen auseinander: ein gewöhnliches Token hat
-- immer ein Board; ein delegierendes hat weder Board noch Person. Damit kann
-- kein Schreibfehler im Code ein boardloses Projekt-Token erzeugen, das die
-- Middleware dann als „gilt überall“ missverstünde.

DROP TRIGGER agent_tokens_mit_board_geloescht;

CREATE TABLE agent_tokens_neu (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  board_id    TEXT REFERENCES boards(id) ON DELETE CASCADE,
  role        TEXT NOT NULL CHECK (role IN ('editor', 'viewer')),
  token_hash  TEXT NOT NULL UNIQUE,
  created_at  TEXT NOT NULL,
  created_by  TEXT NOT NULL DEFAULT '',
  revoked_at  TEXT,
  user_id     TEXT,
  expires_at  TEXT,
  kennung     TEXT,
  delegierend INTEGER NOT NULL DEFAULT 0 CHECK (delegierend IN (0, 1)),
  CHECK ((delegierend = 0 AND board_id IS NOT NULL)
      OR (delegierend = 1 AND board_id IS NULL AND user_id IS NULL))
);

INSERT INTO agent_tokens_neu (id, name, board_id, role, token_hash, created_at, created_by, revoked_at,
                              user_id, expires_at, kennung, delegierend)
SELECT id, name, board_id, role, token_hash, created_at, created_by, revoked_at,
       user_id, expires_at, kennung, 0
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
