-- aBoard server schema, version 5 — Server-Administration Foundation (#306, ADR-0101).
--
-- Bringt drei neue Strukturen:
--   1. server_config: hot-reload-fähige Runtime-Konfiguration (Hybrid Env/DB).
--      Bootstrap-kritische + statisch-geheime Werte (DB-Pfad, Listen-Addr,
--      Master-Encryption-Key) bleiben Env-Var-only — diese Tabelle deckt die
--      runtime-veränderlichen Subsysteme ab (SMTP/IMAP/Backup/Service-Board/
--      Feature-Toggles), die in #293/#294/#295/#297/#303 die Hülle befüllen.
--   2. server_config_audit: append-only Audit-Trail aller Admin-Mutationen.
--      Für is_secret-Keys werden alte/neue Werte als '***' redacted —
--      Tatsache der Mutation ist sichtbar, der Wert nie.
--   3. users.is_admin: rollenbasierte Admin-ACL für /admin/*-Endpoints.
--      DEFAULT FALSE — alle Bestands-User bleiben Nicht-Admins. Der erste
--      Bootstrap-Admin entsteht im Code (storage.BootstrapNewUser, atomar
--      in der BEGIN IMMEDIATE-Transaktion: wenn COUNT(*) der Admins beim
--      Anlegen 0 ist, wird is_admin=TRUE gesetzt).
--
-- Idempotent: läuft nur, wenn user_version < 5 (migrations.go: BEGIN IMMEDIATE
-- + Versions-Gate, F-191). Ein zweiter Lauf ist via Versions-Gate ausgeschlossen.

CREATE TABLE server_config (
    key             TEXT PRIMARY KEY,
    value_plain     TEXT,             -- NULL wenn is_secret
    value_encrypted TEXT,             -- NULL wenn !is_secret; sonst Base64-Secretbox
    is_secret       INTEGER NOT NULL DEFAULT 0 CHECK (is_secret IN (0,1)),
    updated_by      TEXT,             -- user_id; NULL bei Bootstrap-Loader
    updated_at      TEXT NOT NULL,    -- ISO-8601 (RFC3339Nano)
    CHECK ((is_secret = 0 AND value_encrypted IS NULL)
        OR (is_secret = 1 AND value_plain IS NULL))
);

CREATE TABLE server_config_audit (
    id                  TEXT PRIMARY KEY,
    key                 TEXT NOT NULL,
    user_id             TEXT NOT NULL,           -- '' bei System/Bootstrap-Aktionen
    action              TEXT NOT NULL,           -- 'set' | 'delete' | 'reload' | 'admin-grant' | 'admin-revoke'
    old_value_redacted  TEXT,                    -- '***' für is_secret-Keys
    new_value_redacted  TEXT,
    ts                  TEXT NOT NULL            -- ISO-8601
);

CREATE INDEX server_config_audit_ts ON server_config_audit(ts);

-- users.is_admin: rückwärts-kompatibel, alle Bestands-User → FALSE.
ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0,1));
