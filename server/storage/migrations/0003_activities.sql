-- aBoard server schema, version 3 — Server-seitiges Activity-Log (#200).
-- Quelle: Karte #200 (Phase-2 Server Activity-Log, analog ADR-0037 §58).
--
-- Persistenz als relationale Tabelle (nicht JSON-Spalte): analog boards/users/acl
-- — ermöglicht effizientes Trimming per DELETE+LIMIT ohne JSON-Parsing,
-- Querybarkeit nach User/Board/Aktion und atomare Cap-on-Insert-Logik.
--
-- Im No-Auth-Modus (ABOARD_TRUST_FORWARD_AUTH=AUS) bleibt actor leer ('')
-- — kein Pflichtfeld, kein Crash (Legacy-Bypass, ADR-0059 §2).

CREATE TABLE activities (
  id         TEXT PRIMARY KEY,    -- UUID (server-generiert)
  board_id   TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  actor      TEXT NOT NULL DEFAULT '',  -- Email aus Forward-Auth oder '' (Legacy-Bypass)
  action     TEXT NOT NULL,       -- "board-created" | "board-updated" | "board-deleted"
                                  -- | "acl-set" | "acl-deleted"
  field      TEXT NOT NULL DEFAULT '',  -- betroffenes Feld bei field-updates (leer sonst)
  from_val   TEXT NOT NULL DEFAULT '',  -- Vorher-Wert als String (leer wenn nicht anwendbar)
  to_val     TEXT NOT NULL DEFAULT '',  -- Nachher-Wert als String (leer wenn nicht anwendbar)
  note       TEXT NOT NULL DEFAULT '',  -- freier Ergänzungstext (z. B. board-title bei create)
  ts         TEXT NOT NULL        -- ISO-8601-Zeitstempel (server-seitig gesetzt)
);

-- Für board-spezifische Abfragen (Verlauf-Anzeige) und Trimming:
-- idx_activities_board wird beim Cap-on-Insert für COUNT(*) + DELETE ORDER BY ts genutzt.
CREATE INDEX idx_activities_board ON activities(board_id, ts DESC);

-- Für user-spezifische Audit-Abfragen (Phase 2 multi-user).
CREATE INDEX idx_activities_actor ON activities(actor);
