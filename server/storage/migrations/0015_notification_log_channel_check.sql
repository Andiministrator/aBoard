-- aBoard server schema, version 15 — channel-CHECK auf notification_log
-- nachrüsten (F-379, Karte #411).
--
-- Hintergrund: Migration 0010 legte notification_log ohne CHECK auf `channel`
-- an; noch am selben Tag (Sprint 42, Commit d5edf55) wurde der CHECK
-- nachgepflegt — ABER direkt in der Datei 0010_notifications.sql editiert.
-- Migrationen laufen einmalig pro Version (RunMigrations vergleicht gegen
-- PRAGMA user_version); jede DB, die 0010 VOR diesem Fix bereits angewendet
-- hatte, bekommt den CHECK nie automatisch nachgereicht — der Fix in der
-- alten Datei wirkt nur auf DBs, die 0010 noch nicht gelaufen sind. Laut
-- Tag-Historie (v1.28.0 wurde erst nach dem Fix-Commit geschnitten) ist keine
-- ausgelieferte Version je mit der Lücke migriert worden; diese Migration ist
-- trotzdem die korrekte, nicht von der Deploy-Historie abhängige Absicherung
-- für jede DB, die 0010 zwischenzeitlich lokal (z. B. während der Entwicklung
-- desselben Sprints) angewendet haben könnte.
--
-- SQLite kann CHECK-Constraints nicht per ALTER TABLE nachrüsten (wie schon
-- 0014_ical_subscriptions_fk.sql) — deshalb der übliche Weg: neue Tabelle mit
-- dem CHECK, Daten kopieren, alte Tabelle weg, umbenennen, Indizes neu.
--
-- BESTANDSZEILEN, DIE DEN NEUEN CHECK VERLETZEN WÜRDEN: Der Worker
-- (server/notifications/worker.go) schreibt `channel` ausschliesslich über
-- `sender.Channel()`, das nur "email" oder "gotify" liefert (EmailSender/
-- GotifySender) — nach Code-Lage kann keine Zeile mit einem anderen
-- channel-Wert existieren. Die WHERE-Klausel filtert trotzdem defensiv (wie
-- 0014 es für Waisen tut): eine Zeile mit fremdem channel-Wert wäre ein
-- Zeichen von Datenkorruption oder manueller DB-Manipulation, kein regulärer
-- Anwendungsfall. Definiertes Verhalten: eine solche Zeile wird beim Kopieren
-- stillschweigend ausgefiltert (nicht kopiert) statt die Migration mit einem
-- CHECK-Constraint-Fehler abzubrechen und den Serverstart zu blockieren — ein
-- Log-/Throttle-Eintrag ist verzichtbares Bestandsmaterial, kein Datensatz,
-- dessen Verlust ein Nutzer bemerken würde (anders als die Bearer-Secrets in
-- 0014).

CREATE TABLE notification_log_neu (
    id              TEXT PRIMARY KEY,
    user_id         TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    channel         TEXT NOT NULL CHECK (channel IN ('email', 'gotify')),
    card_id         TEXT,
    board_id        TEXT,
    summary         TEXT,
    ts              TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('sent', 'failed', 'coalesced', 'throttled')),
    retry_count     INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    digested_at     TEXT
);

INSERT INTO notification_log_neu
     (id, user_id, event_type, channel, card_id, board_id, summary, ts, status, retry_count, error, digested_at)
SELECT id, user_id, event_type, channel, card_id, board_id, summary, ts, status, retry_count, error, digested_at
  FROM notification_log
 WHERE channel IN ('email', 'gotify')
   AND status IN ('sent', 'failed', 'coalesced', 'throttled');

DROP TABLE notification_log;

ALTER TABLE notification_log_neu RENAME TO notification_log;

-- Indizes verschwanden mit der alten Tabelle — hier wieder anlegen (gleiche
-- Namen/Spalten wie in 0010).
CREATE INDEX idx_notif_log_user_ts ON notification_log(user_id, ts);
CREATE INDEX idx_notif_log_throttled ON notification_log(user_id, status, digested_at);
CREATE INDEX idx_notif_log_coalesce ON notification_log(user_id, event_type, channel, card_id, ts);
