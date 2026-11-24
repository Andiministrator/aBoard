-- aBoard server schema, version 13 — iCal-Subscription-Tokens (#308 Etappe 6,
-- ADR-0103 §iCal-Export).
--
-- Pro User 0..N Subscription-Tokens für den iCal-Feed (Apple/Outlook/Google
-- Calendar abonnieren). Ein Token ist ein Bearer-Secret in der Feed-URL
-- (/ical/calendar.ics?token=…). Externe Kalender-Apps können kein Forward-Auth
-- → der Token ist die einzige Auth; deshalb liegt der Feed-Endpoint außerhalb
-- von /boards/* (ACLGuard greift dort nicht) und authentifiziert allein über
-- diese Tabelle.
--
-- Scope:
--   board_id NULL  → Cross-Board: alle Boards mit Lese-Zugriff des Users.
--   board_id gesetzt → genau dieses Board (User muss zur Feed-Zeit Zugriff haben).
--
-- Token-Speicherung (zwei Spalten, ein Klartext):
--   token_hash  — SHA-256-Hex des Klartext-Tokens. UNIQUE + Lookup-Schlüssel
--                 beim eingehenden Feed-Request (kein Decrypt zum Validieren
--                 nötig — der Hash-Treffer IST die Validierung).
--   token_enc   — AES-256-GCM-verschlüsselter Klartext (secretcrypto.Box,
--                 Reuse #298/#306). Nur, damit die Settings-UI die volle
--                 Abo-URL erneut anzeigen kann (encrypted-at-rest, ADR-0103).
--
-- Rotation = Zeile löschen + neue anlegen (alter Token sofort ungültig, weil
-- der Hash-Lookup ins Leere läuft). last_used_at protokolliert den letzten
-- Feed-Abruf (Best-effort, für „verwaiste Tokens aufräumen").

CREATE TABLE ical_subscriptions (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    board_id     TEXT,
    token_hash   TEXT NOT NULL UNIQUE,
    token_enc    TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT
);

CREATE INDEX idx_ical_subs_user ON ical_subscriptions(user_id, created_at);
