-- aBoard server schema, version 38 — Board-Paar Stufe 2: Rolle „intern“ und
-- offene Vorschläge (ADR-0140 §8, Karte #783, Vertrag Push-Skript, Ziffer 3a/9.6).
--
-- 1. Token-Rolle „intern“ für den internen Agenten (Open-WebUI-Assistent) auf dem externen
--    Server. Er liest das externe Board über fünf feste Wege, ändert sofort
--    nur Spalte und Priorität einer bestehenden externen Karte und legt alles
--    andere als Vorschlag ab, den ein Mensch bestätigt (api/vorschlaege.go).
--    Der CHECK auf role aus 0031 kennt die Rolle nicht. SQLite kann einen
--    CHECK nicht nachträglich ändern, deshalb wird agent_tokens wie in
--    0024/0025/0031 neu aufgebaut („neue Tabelle, kopieren, alte weg,
--    umbenennen“). Beide Trigger, die agent_tokens nennen, werden vorher
--    entfernt und danach unverändert neu angelegt — auch der auf
--    agent_token_boards, dessen Rumpf agent_tokens liest: Zwischen DROP und
--    RENAME gäbe es die Tabelle sonst nicht, und die Umbenennung prüft alle
--    Trigger. Die drei Indizes aus 0017/0023 ebenso.
--
--    Zweiter CHECK: „intern“ gibt es nur als Projekt-Token für genau ein
--    Board — ohne Person, nie delegierend, nie für mehrere Boards (ADR-0140
--    §8). Damit kann auch ein Schreibfehler im Code keine Mischform erzeugen.
--
-- 2. Tabelle vorschlaege: offene Vorschläge eines intern-Tokens. Sie liegen
--    mit vollem Text auf dem externen Server (Vertrag Push-Skript, W-1 (a)), sind über keinen
--    Agentenweg lesbar und werden beim Bestätigen, Ablehnen und Ablauf (höchstens
--    24 h) HART gelöscht (Vertrag Push-Skript, Ziffer 9.6 (b), (c)). Übrig bleibt nur ein Eintrag im
--    Protokoll des Boards, ohne Nutzlast.
--
--    nutzlast ist der Body des Werkzeugs, das die Bestätigung ausführt
--    (karte_anlegen, karte_aendern, kommentieren, …), so wie der Server ihn
--    beim Annehmen geprüft und normalisiert hat; hash ist SHA-256 über Art,
--    Ziel-Karte, Nutzlast und „für wen“ — der Mensch bestätigt mit dem Hash,
--    den er gesehen hat. karte_id: die Ziel-Karte bei Arten, die eine
--    bestehende Karte betreffen (sonst NULL). token_name steht dabei, weil
--    der Vorschlag beim Bestätigen unter genau diesem Namen ausgeführt wird
--    (agent:<name>) und ein Widerruf ihn danach nicht mehr erklären könnte.
--
--    Kein Fremdschlüssel auf agent_tokens: Ein künftiger Neuaufbau von
--    agent_tokens (DROP TABLE) löschte sonst bei aktivem foreign_keys alle
--    offenen Vorschläge mit (dieselbe Überlegung wie agent_token_boards, 0031).
--    Ob das Token noch gilt, prüft das Bestätigen selbst.

DROP TRIGGER agent_tokens_mit_board_geloescht;
DROP TRIGGER agent_token_boards_board_geloescht;

CREATE TABLE agent_tokens_neu (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  board_id    TEXT REFERENCES boards(id) ON DELETE CASCADE,
  role        TEXT NOT NULL CHECK (role IN ('editor', 'viewer', 'vorschlag', 'intern')),
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
  CHECK (role <> 'vorschlag' OR (delegierend = 0 AND mehrere = 0 AND user_id IS NULL)),
  CHECK (role <> 'intern' OR (delegierend = 0 AND mehrere = 0 AND user_id IS NULL AND board_id IS NOT NULL))
);

INSERT INTO agent_tokens_neu (id, name, board_id, role, token_hash, created_at, created_by, revoked_at,
                              user_id, expires_at, kennung, delegierend, mehrere)
SELECT id, name, board_id, role, token_hash, created_at, created_by, revoked_at,
       user_id, expires_at, kennung, delegierend, mehrere
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

CREATE TRIGGER agent_token_boards_board_geloescht
AFTER DELETE ON agent_token_boards
WHEN EXISTS (SELECT 1 FROM agent_tokens t WHERE t.id = OLD.token_id AND t.revoked_at IS NULL)
BEGIN
  INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
  SELECT lower(hex(randomblob(16))), 'agent-token:' || t.name, '', 'agent-token-board-removed',
         NULL, 'board=' || OLD.board_id, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
    FROM agent_tokens t WHERE t.id = OLD.token_id;
END;

CREATE TABLE vorschlaege (
  id          TEXT PRIMARY KEY,
  board_id    TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  token_id    TEXT NOT NULL,
  token_name  TEXT NOT NULL,
  art         TEXT NOT NULL CHECK (art IN ('karte.anlegen', 'karte.text', 'karte.bearbeiter', 'kommentar',
                                           'karte.loeschen', 'karte.archivieren')),
  karte_id    TEXT,
  nutzlast    TEXT NOT NULL,
  hash        TEXT NOT NULL,
  fuer        TEXT NOT NULL DEFAULT '',
  angelegt    TEXT NOT NULL,
  laeuft_ab   TEXT NOT NULL
);

CREATE INDEX vorschlaege_board ON vorschlaege(board_id, angelegt);
CREATE INDEX vorschlaege_token ON vorschlaege(token_id);
CREATE INDEX vorschlaege_ablauf ON vorschlaege(laeuft_ab);
