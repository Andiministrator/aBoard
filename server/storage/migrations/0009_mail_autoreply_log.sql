-- aBoard server schema, version 9 — Mail-Auto-Reply-Log (#297).
--
-- mail_autoreply_log: Lifetime-Cap-Tabelle für Auto-Reply-Mails.
-- Pro (sender_email, card_id)-Paar wird eine Auto-Reply genau einmal gesendet
-- — UNIQUE-Constraint + rowsAffected-Check in autoreply.go.
--
-- Index auf card_id für Debug-Queries ("welche Replies gingen für Karte X raus?").
--
-- Idempotent: läuft nur, wenn user_version < 9 (migrations.go: BEGIN IMMEDIATE
-- + Versions-Gate, F-191).

CREATE TABLE mail_autoreply_log (
    sender_email TEXT NOT NULL,
    card_id      TEXT NOT NULL,
    sent_at      TIMESTAMP NOT NULL,
    UNIQUE(sender_email, card_id)
);

CREATE INDEX idx_mail_autoreply_log_card ON mail_autoreply_log(card_id);
