-- aBoard server schema, version 35 — Board-Paar intern/extern, Seite außen:
-- Schatten interner Karten und Termine (ADR-0140 §1, §3, Karte #779).
--
-- Schatten liegen NICHT im Board-Zustand (boards.state_json), sondern hier.
-- Kein Board-Schreibweg erreicht diese Tabelle, und kein Leser des Blobs
-- (Kalender, Export, Werkzeuge, Benachrichtigungen) sieht sie; Schatten
-- erscheinen nur dort, wo ein Leser sie ausdrücklich holt (ADR-0140 §1).
--
-- Geschrieben wird ausschließlich vom Schatten-Eingang
-- (PUT /agent/schatten/{board}, Token abe_), und zwar als ganze Menge je
-- Board in einer Transaktion. Beide Tabellen hängen per Fremdschlüssel an
-- boardpaar_aussen: Entkoppeln oder Löschen des Boards löscht die Schatten
-- und den Abgleich-Stand mit.
--
-- Die Tabelle trägt keinen Link und keine interne Kennung: schatten_id ist
-- die HMAC aus dem Push-Skript (Vertrag Push-Skript, Ziffer 9.12), nicht umkehrbar.

CREATE TABLE schatten (
  board_id     TEXT NOT NULL REFERENCES boardpaar_aussen(aussen_board_id) ON DELETE CASCADE,
  schatten_id  TEXT NOT NULL,
  art          TEXT NOT NULL CHECK (art IN ('karte', 'termin')),
  nr           INTEGER,
  titel        TEXT,
  vertraulich  INTEGER NOT NULL CHECK (vertraulich IN (0, 1)),
  spalte       TEXT,
  prioritaet   TEXT,
  start_datum  TEXT,
  start_zeit   TEXT,
  ende_datum   TEXT,
  ende_zeit    TEXT,
  bearbeiter   TEXT NOT NULL,
  von_agent    INTEGER NOT NULL CHECK (von_agent IN (0, 1)),
  geaendert_at TEXT NOT NULL,
  PRIMARY KEY (board_id, schatten_id)
);

-- Aktualität je Board: letzter_abgleich bei jedem gültigen Lauf,
-- letzte_aenderung nur bei inhaltlicher Änderung (ADR-0140 §3).
CREATE TABLE schatten_abgleich (
  board_id         TEXT PRIMARY KEY REFERENCES boardpaar_aussen(aussen_board_id) ON DELETE CASCADE,
  letzter_abgleich TEXT NOT NULL,
  letzte_aenderung TEXT,
  anzahl           INTEGER NOT NULL
);
