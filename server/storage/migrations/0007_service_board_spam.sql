-- aBoard server schema, version 7 — Service-Board-Spam-Backend (#294, ADR-0092).
--
-- Bringt zwei Tabellen für die Server-side Spam-Blocklist + Drop-Metrik:
--   1. mail_spam_senders: Blocklist mit normalisierter Sender-Email als PK.
--      Wird vom IMAP-Mail-Ingest (#295) beim Polling konsultiert; Sender auf
--      der Liste landen entweder in col-spam (default) oder werden gedroppt
--      (MAIL_SPAM_DROP=true → nur Counter +1, Mail in IMAP-Trash).
--   2. mail_spam_counter: single-row Metrik-Tabelle (id=1) mit Counter für
--      gedroppte Spam-Mails. Single-row-Konvention (CHECK id=1) verhindert
--      versehentliche Mehrfach-Inserts.
--
-- Beide Tabellen sind board-unabhängig (Server-globale Blocklist) — anders
-- als activities/attachments KEIN FK auf boards. Lebenszeit = Server-Instanz.
--
-- ACL/Privacy: Sender-Email ist personenbezogen → nur Admin-Endpoints lesen/
-- mutieren (POST /mail-spam-report ist admin-gated via requireAdmin). Kein
-- öffentlicher GET-Endpoint im MVP.
--
-- Idempotent: läuft nur, wenn user_version < 7 (migrations.go: BEGIN IMMEDIATE
-- + Versions-Gate, F-191). Ein zweiter Lauf ist via Versions-Gate ausgeschlossen.

CREATE TABLE mail_spam_senders (
    sender_email  TEXT PRIMARY KEY,        -- normalisiert (NormalizeEmail)
    added_at      TEXT NOT NULL,           -- ISO-8601
    added_by      TEXT                     -- user_id; NULL bei System-Bootstrap
);

CREATE INDEX idx_mail_spam_senders_added ON mail_spam_senders(added_at);

CREATE TABLE mail_spam_counter (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    dropped_total INTEGER NOT NULL DEFAULT 0,
    updated_at    TEXT NOT NULL
);

INSERT INTO mail_spam_counter (id, dropped_total, updated_at)
VALUES (1, 0, strftime('%Y-%m-%dT%H:%M:%fZ','now'));
