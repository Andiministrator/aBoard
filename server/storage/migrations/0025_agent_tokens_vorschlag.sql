-- aBoard server schema, version 25 — Token-Rolle „vorschlag“ für die
-- Recherche-Instanz (ADR-0133 §6, Karte #670).
--
-- Ein Vorschlags-Token darf auf seinem Board nur neue Karten anlegen; der
-- Server lenkt sie in die Swimlane „Vorschläge“ (api/boards_ops_vorschlag.go).
-- Kein Lesen, kein Ändern, kein Löschen — die Instanz liest fremde Webseiten.
--
-- Der CHECK auf role aus 0017/0024 kennt nur editor und viewer. SQLite kann
-- einen CHECK nicht nachträglich ändern, deshalb wird die Tabelle wie in 0024
-- neu aufgebaut („neue Tabelle, kopieren, alte weg, umbenennen“). Nichts
-- verweist per Fremdschlüssel AUF agent_tokens; der Trigger aus 0018 wird
-- vorher ausdrücklich entfernt und danach unverändert neu angelegt, ebenso die
-- drei Indizes aus 0017 und 0023.
--
-- Zweiter CHECK: Die Rolle vorschlag gibt es nur als Projekt-Token — ohne
-- Person (eine Person hätte ein Leserecht, das Token soll keins haben, und
-- wirksameRolle kennt kein Minimum mit „vorschlag“) und nie delegierend (die
-- Rolle der Person aus dem JWT würde sie überschreiben). Damit kann auch ein
-- Schreibfehler im Code keine dieser Mischformen erzeugen.

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
  CHECK ((delegierend = 0 AND board_id IS NOT NULL)
      OR (delegierend = 1 AND board_id IS NULL AND user_id IS NULL)),
  CHECK (role <> 'vorschlag' OR (delegierend = 0 AND user_id IS NULL))
);

INSERT INTO agent_tokens_neu (id, name, board_id, role, token_hash, created_at, created_by, revoked_at,
                              user_id, expires_at, kennung, delegierend)
SELECT id, name, board_id, role, token_hash, created_at, created_by, revoked_at,
       user_id, expires_at, kennung, delegierend
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
