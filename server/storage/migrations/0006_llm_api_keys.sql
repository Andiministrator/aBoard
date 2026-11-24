-- aBoard server schema, version 6 — LLM-API-Keys (#298, ADR-0094).
--
-- Speichert pro (user_id, provider) genau einen verschlüsselten API-Key
-- für die LLM-Integration (#299/#300). Verschlüsselung läuft über
-- secretcrypto.Box mit ABOARD_SECRET_KEY — derselbe Master-Key wie für
-- server_config-Secrets (#306). Kein zweiter Key, kein zweites
-- Crypto-Modul.
--
-- Schlüssel-Klartext wird NIE persistiert; auch nicht in einem
-- value_plain-Pfad (anders als server_config) — ein LLM-Key ist immer
-- geheim. Frontend-GET liefert ausschließlich die Liste der Provider mit
-- hinterlegtem Key, nicht den Key selbst.
--
-- last_used_at wird beim TestKey-Erfolg und beim Send (#299) aktualisiert
-- — null bis zum ersten Test/Send. ON DELETE CASCADE würden wir gerne,
-- aber users-Tabelle hat keinen FK-Constraint (Bestand-Schema); statt
-- dessen wird der Delete-Pfad im Code geräumt (Folge-Karte falls je nötig
-- — heute hat niemand User-Löschen-UI).

CREATE TABLE llm_api_keys (
    user_id        TEXT NOT NULL,
    provider       TEXT NOT NULL CHECK (provider IN ('anthropic','openai','google','mistral')),
    key_encrypted  TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    last_used_at   TEXT,
    PRIMARY KEY (user_id, provider)
);

CREATE INDEX idx_llm_api_keys_user ON llm_api_keys(user_id);
