-- aBoard server schema, version 41 — Board-Paar Stufe 2c: Journal außen und
-- wartende Spiegel (ADR-0141 §5.1, §5.2, Karte #827).
--
-- 1. boardpaar_journal: je geänderter Stelle einer Spiegelkarte, eines
--    Spiegeltermins oder eines wartenden Spiegels auf einer externen Kopie ein
--    Eintrag, geschrieben in derselben Transaktion wie das Board (Ops-Weg).
--    Das Skript holt die Einträge ab (GET /agent/boardpaar/aenderungen, setzt
--    abgeholt_at) und bestätigt sie (POST …/bestaetigt, setzt bestaetigt_at).
--    seq ist die einzige Ordnung — AUTOINCREMENT, damit eine gelöschte
--    höchste seq nicht wiederkehrt (der Cursor des Skripts darf nie auf eine
--    zweite Bedeutung derselben Zahl treffen). kennung ist die spiegel.id
--    bzw. bei wartenden Spiegeln die Außen-ID (wartend = 1). vorher_json und
--    nachher_json tragen Werte in der Form des Hinwegs (NULL = kein Wert).
--    person/akteur_art: wer außen geschrieben hat. Verschmolzen werden nur
--    Einträge, die noch nicht abgeholt sind (§5.1, VF-3).
--
-- 2. boardpaar_wartend: außen angelegte Karten und Termine, deren Marke
--    `spiegel` noch aussteht (VF-1). Der Eingang löscht die Zeile, sobald er
--    die Marke setzt; seq_angelegt ist die seq des Eintrags `angelegt` (für
--    das Aufräumen mit rueckweg_bis, §2).
--
-- Beide Tabellen hängen am Board und verschwinden mit ihm. Kein Down; Rückweg
-- nur über das Backup von vorher (F-1237).

CREATE TABLE boardpaar_journal (
  seq           INTEGER PRIMARY KEY AUTOINCREMENT,
  board_id      TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  objekt        TEXT NOT NULL CHECK (objekt IN ('karte', 'termin')),
  kennung       TEXT NOT NULL,
  wartend       INTEGER NOT NULL DEFAULT 0 CHECK (wartend IN (0, 1)),
  art           TEXT NOT NULL,
  stelle        TEXT NOT NULL DEFAULT '',
  vorher_json   TEXT,
  nachher_json  TEXT,
  person        TEXT NOT NULL DEFAULT '',
  akteur_art    TEXT NOT NULL DEFAULT '',
  zeit          TEXT NOT NULL,
  abgeholt_at   TEXT,
  bestaetigt_at TEXT
);

CREATE INDEX boardpaar_journal_offen ON boardpaar_journal(board_id, bestaetigt_at, seq);
CREATE INDEX boardpaar_journal_stelle ON boardpaar_journal(board_id, kennung, stelle) WHERE bestaetigt_at IS NULL;

CREATE TABLE boardpaar_wartend (
  board_id     TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  aussen_id    TEXT NOT NULL,
  objekt       TEXT NOT NULL CHECK (objekt IN ('karte', 'termin')),
  seq_angelegt INTEGER NOT NULL,
  PRIMARY KEY (board_id, aussen_id)
);
