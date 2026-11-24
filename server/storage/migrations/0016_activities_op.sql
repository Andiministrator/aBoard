-- aBoard server schema, version 16 — Protokoll je Karte: card_id, op_type,
-- actor_kind auf activities (ADR-0129 §5, Karte #554).
--
-- Bislang schrieb POST /boards/{id}/ops GENAU EINEN Sammel-Eintrag je Batch
-- ("board-ops", ohne Karten-/Op-Bezug, boards_ops.go). Für ein Protokoll je
-- Karte braucht es die Op-Auflösung: ein Eintrag je Op, mit der betroffenen
-- Karte (card_id) und dem Op-Typ (op_type). actor_kind unterscheidet
-- Mensch/Agent/System — heute (Karte #554) immer 'mensch'; eine Folgekarte
-- befüllt 'agent' für die Agenten-Anmeldung eines Team-Betriebs (ADR-0129 §3).
--
-- SQLite kann CHECK-Constraints nicht per ALTER TABLE nachrüsten (wie schon
-- 0014_ical_subscriptions_fk.sql, 0015_notification_log_channel_check.sql)
-- — actor_kind trägt einen CHECK, deshalb der übliche Weg: neue Tabelle mit
-- den zusätzlichen Spalten, Daten kopieren (Bestandszeilen bekommen die
-- Defaults '', '', 'mensch' — kein Bestand widerspricht dem CHECK, da
-- actor_kind für sie neu ist), alte Tabelle weg, umbenennen, Indizes neu.
--
-- Kein Rückweg (K-14, ADR-0129 §5): Das Migrationssystem kennt kein Down
-- (storage/migrations.go). Vor dem Update auf eine Version mit dieser
-- Migration ein Server-Backup ziehen.

CREATE TABLE activities_neu (
  id         TEXT PRIMARY KEY,
  board_id   TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  actor      TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  field      TEXT NOT NULL DEFAULT '',
  from_val   TEXT NOT NULL DEFAULT '',
  to_val     TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  ts         TEXT NOT NULL,
  card_id    TEXT NOT NULL DEFAULT '',       -- betroffene Karte bei action='op' (sonst '')
  op_type    TEXT NOT NULL DEFAULT '',       -- Op-Typ bei action='op' (z. B. "card.field", sonst '')
  actor_kind TEXT NOT NULL DEFAULT 'mensch' CHECK (actor_kind IN ('mensch', 'agent', 'system'))
);

INSERT INTO activities_neu
     (id, board_id, actor, action, field, from_val, to_val, note, ts, card_id, op_type, actor_kind)
SELECT id, board_id, actor, action, field, from_val, to_val, note, ts, '', '', 'mensch'
  FROM activities
 ORDER BY rowid;
-- ORDER BY rowid (Verify Sprint 71, V-1): Die Sortierung im Lese-Endpunkt
-- nimmt rowid als Einfügereihenfolge innerhalb einer Sekunde (F-1147). Ohne
-- ORDER BY garantiert SQLite die Reihenfolge der kopierten Zeilen formal
-- nicht, auch wenn der Tabellen-Scan sie heute einhält.

DROP TABLE activities;

ALTER TABLE activities_neu RENAME TO activities;

-- Indizes verschwanden mit der alten Tabelle — hier wieder anlegen (gleiche
-- Namen/Spalten wie in 0003) plus der neue Index für den Lese-Endpunkt
-- „Protokoll je Karte" (GET /boards/{id}/activities?card=...).
CREATE INDEX idx_activities_board ON activities(board_id, ts DESC);
CREATE INDEX idx_activities_actor ON activities(actor);
CREATE INDEX idx_activities_card ON activities(board_id, card_id, ts DESC);
