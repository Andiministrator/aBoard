-- aBoard server schema, version 8 — IMAP-Mail-Ingest-State (#295, ADR-0093).
--
-- Bringt zwei Tabellen für den IMAP-Polling-Worker (#295):
--   1. mail_imap_state: pro Folder (default INBOX) die letzte gesehene UID +
--      Last-Poll-Zeitstempel. Erlaubt dem Worker, beim Server-Restart genau
--      dort weiterzumachen, wo er aufgehört hat (kein Re-Pull alter Mails).
--      Bei UIDVALIDITY-Wechsel (Postfach-Re-Mount, Provider-Migration) wird
--      last_uid auf 0 zurückgesetzt — siehe server/mail/state.go.
--   2. mail_imap_processed: Set der Message-IDs bereits verarbeiteter Mails.
--      Schützt gegen Doppelverarbeitung im MOVE-Race (Provider-Akzept ohne
--      Server-Bestätigung → erneuter SEARCH UNSEEN würde die Mail nochmal
--      liefern). Eviction der > 30 Tage alten Einträge beim Worker-Start.
--
-- Beide Tabellen sind board-unabhängig (Server-globaler Worker-State). Kein
-- FK auf boards. Lebenszeit = Server-Instanz; bei Migration/Restore bleiben
-- die Tabellen erhalten (sonst Re-Pull).
--
-- Idempotent: läuft nur, wenn user_version < 8 (migrations.go: BEGIN IMMEDIATE
-- + Versions-Gate, F-191). Ein zweiter Lauf ist via Versions-Gate ausgeschlossen.

CREATE TABLE mail_imap_state (
    folder         TEXT PRIMARY KEY,            -- z. B. "INBOX"
    last_uid       INTEGER NOT NULL DEFAULT 0,  -- höchste verarbeitete UID
    uid_validity   INTEGER NOT NULL DEFAULT 0,  -- IMAP UIDVALIDITY; bei Wechsel → last_uid = 0
    last_poll_at   TEXT                         -- ISO-8601, NULL beim ersten Lauf
);

CREATE TABLE mail_imap_processed (
    message_id     TEXT PRIMARY KEY,            -- RFC-5322 Message-ID (mit <>-Klammern)
    processed_at   TEXT NOT NULL                -- ISO-8601
);

CREATE INDEX idx_mail_imap_processed_at ON mail_imap_processed(processed_at);
