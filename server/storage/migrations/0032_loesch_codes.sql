-- aBoard server schema, version 32 — Bestätigungscodes für das Löschen über
-- die Agenten-Werkzeuge (ADR-0139 E-2, Karte #757).
--
-- 1. loesch_code_geheimnis: genau eine Zeile mit dem Server-Geheimnis, über
--    das der Bestätigungscode als HMAC gebildet wird. Erzeugt beim ersten
--    Bedarf (INSERT OR IGNORE, danach lesen — so gewinnt bei mehreren
--    Instanzen dieselbe Zeile). In der Datenbank statt im Speicher, damit ein
--    Code einen Neustart übersteht und auf jeder Instanz gilt.
--
-- 2. loesch_codes_verbraucht: der Zufallswert jedes angenommenen Codes bis zu
--    seinem Ablauf. Ein Code wirkt nur einmal — sonst wirkte er erneut, wenn
--    ein Objekt mit derselben id und demselben Inhalt innerhalb der Frist
--    wieder angelegt wird (Rückgängig, Restore, card.upsert mit alter id).
--    Abgelaufene Zeilen räumt jede Annahme ab; nach dem Ablauf lehnt die
--    HMAC-Prüfung den Code ohnehin ab.

CREATE TABLE loesch_code_geheimnis (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  geheimnis  BLOB NOT NULL CHECK (length(geheimnis) >= 32),
  created_at TEXT NOT NULL
);

CREATE TABLE loesch_codes_verbraucht (
  zufall      TEXT PRIMARY KEY,
  ablauf_unix INTEGER NOT NULL
);

CREATE INDEX idx_loesch_codes_ablauf ON loesch_codes_verbraucht(ablauf_unix);
