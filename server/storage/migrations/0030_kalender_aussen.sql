-- aBoard server schema, version 30 — Kalender-Abo „für außen“ und Zugang
-- des Push-Skripts (ADR-0138 §6, Karte #748).
--
-- 1. Abo „für außen“ (Spalten an ical_subscriptions, Migration 0013/0014):
--
--   - aussen_hash: SHA-256-Hex der Kennung. Gesetzt heißt: dieses Abo ist
--     „für außen“. Die Kennung (128 Bit Zufall) ist der geheime Teil der
--     externen Adresse <Außenstelle>/f/<kennung>.ics, unabhängig vom
--     Feed-Token (aus der Kennung folgt nichts über das Token). Lookup wie
--     beim Token über den Hash.
--   - aussen_enc: die Kennung, verschlüsselt (secretcrypto, wie token_enc) —
--     für die Anzeige im Abo-Panel und die Kennungsliste des Push-Skripts.
--
-- Der Feed eines Abos „für außen“ ist NUR über /ical/aussen/<kennung>.ics
-- erreichbar; das Feed-Token des Abos öffnet den vollen Feed nicht
-- (server/api/ical.go). Widerruf = Zeile löschen wie bisher; damit fällt die
-- Kennung aus der Liste, und das Skript löscht die Datei extern.
--
-- 2. Zugang des Push-Skripts (Token-Typ `kalender-ausgang`): instanzweit,
--    keine Person, kein Board. Er erreicht genau zwei Wege (Kennungsliste und
--    Feed „für außen“), jeder andere antwortet 403 (api.KalenderAusgangSchranke).
--    Gespeichert wird nur der Hash; der Klartext wird bei der Anlage einmal
--    angezeigt. Widerruf setzt revoked_at (nachvollziehbar wie agent_tokens).

ALTER TABLE ical_subscriptions ADD COLUMN aussen_hash TEXT;
ALTER TABLE ical_subscriptions ADD COLUMN aussen_enc TEXT;
CREATE UNIQUE INDEX idx_ical_subs_aussen ON ical_subscriptions(aussen_hash) WHERE aussen_hash IS NOT NULL;

CREATE TABLE kalender_ausgang_tokens (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  created_at   TEXT NOT NULL,
  created_by   TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  revoked_at   TEXT
);

CREATE UNIQUE INDEX kalender_ausgang_name_aktiv ON kalender_ausgang_tokens(name) WHERE revoked_at IS NULL;
