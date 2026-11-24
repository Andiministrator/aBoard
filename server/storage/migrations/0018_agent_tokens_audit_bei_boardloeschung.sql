-- aBoard server schema, version 18 — Audit, wenn ein Agenten-Token mit
-- seinem Board verschwindet (Kritiker Sprint 71, K-A6).
--
-- agent_tokens.board_id hängt per ON DELETE CASCADE am Board (0017). Das
-- Löschen eines Boards widerruft damit faktisch alle seine Tokens — ohne
-- Eintrag in server_config_audit, obwohl K-13 (ADR-0129 §3) Anlage und
-- Widerruf protokolliert. Ein Trigger holt das nach: Er feuert auch bei
-- Löschungen durch eine Fremdschlüssel-Aktion (SQLite führt sie als
-- gewöhnliches DELETE aus). Nur AKTIVE Tokens erzeugen einen Eintrag; ein
-- bereits widerrufenes ist schon protokolliert.
--
-- VERTRAG (Verify Sprint 71, V-3): Der Trigger kennt die Ursache eines
-- DELETE nicht und beschriftet jeden Eintrag als Board-Löschung. Das stimmt,
-- solange es keinen anderen DELETE-Pfad auf agent_tokens gibt (Widerruf ist
-- ein UPDATE, siehe storage/agent_tokens.go). Wer einen einführt, passt die
-- Beschriftung an — sonst steht im Audit eine falsche Ursache.
--
-- user_id bleibt leer: Der Trigger kennt den Menschen nicht, der das Board
-- gelöscht hat. Wer es war, steht nicht im Server-Audit (F-259: Board-Löschung
-- wird nicht protokolliert) — das Board steht im neuen Wert.

CREATE TRIGGER agent_tokens_mit_board_geloescht
AFTER DELETE ON agent_tokens
WHEN OLD.revoked_at IS NULL
BEGIN
  INSERT INTO server_config_audit (id, key, user_id, action, old_value_redacted, new_value_redacted, ts)
  VALUES (lower(hex(randomblob(16))), 'agent-token:' || OLD.name, '', 'agent-token-revoke-board-deleted',
          NULL, 'board=' || OLD.board_id || ';role=' || OLD.role, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
END;
