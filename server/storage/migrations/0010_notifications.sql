-- aBoard server schema, version 10 — Notifications (#305, ADR-0107).
--
-- Drei Tabellen für den Notification-Subsystem:
--
--   notification_subscriptions  — pro (user_id, event_type, channel) ein
--                                 enabled-Flag. Default-Subscriptions (was beim
--                                 ersten Login an ist) sind Application-Code,
--                                 nicht Schema.
--
--   notification_config         — pro (user_id, channel) eine kanal-spezifische
--                                 JSON-Konfiguration. Für Gotify enthält das
--                                 Server-URL und API-Token (Token-Teil
--                                 verschlüsselt via secretcrypto.Box, identisch
--                                 zu llm_api_keys + server_config-Secrets;
--                                 dieselbe ABOARD_SECRET_KEY-Master-Box).
--                                 E-Mail braucht keinen Eintrag — die Adresse
--                                 stammt aus users.email (Forward-Auth-Identität).
--
--   notification_log            — append-only Audit/Throttle-Log. Wird vom
--                                 Worker bei jedem Versuch (sent/failed/
--                                 coalesced/throttled) befüllt. Dient als
--                                 Quelle für Rate-Limit-Zählung (30/h/Kanal)
--                                 und die 18:00-Tagesübersicht (gequeued = im
--                                 Log mit status='throttled' seit letzter
--                                 Digest-Sendung).
--
-- Anti-Loop, Coalescence-Window, Throttling-Limits: Konstanten im Code
-- (server/notifications/config.go), nicht im Schema — leichter zu tunen.

CREATE TABLE notification_subscriptions (
    user_id      TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    channel      TEXT NOT NULL CHECK (channel IN ('email', 'gotify')),
    enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    updated_at   TEXT NOT NULL,
    PRIMARY KEY (user_id, event_type, channel)
);

CREATE INDEX idx_notif_subs_event ON notification_subscriptions(event_type, channel, enabled);

CREATE TABLE notification_config (
    user_id          TEXT NOT NULL,
    channel          TEXT NOT NULL CHECK (channel IN ('email', 'gotify')),
    config_encrypted TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    PRIMARY KEY (user_id, channel)
);

-- channel-CHECK auch hier (analog notification_subscriptions); Cleanup-Job
-- ist Folge-Karte (vergleichbar mail_imap_processed-30-Tage-Eviction). Bis
-- dahin: append-only — bei großem Wachstum manuelles DELETE WHERE ts < ...
-- über die Admin-DB-Konsole.
CREATE TABLE notification_log (
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

CREATE INDEX idx_notif_log_user_ts ON notification_log(user_id, ts);
CREATE INDEX idx_notif_log_throttled ON notification_log(user_id, status, digested_at);
CREATE INDEX idx_notif_log_coalesce ON notification_log(user_id, event_type, channel, card_id, ts);
