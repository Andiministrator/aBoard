-- aBoard server schema, version 22 — Idempotenz am Ops-Endpunkt (ADR-0130 §5, Karte #607).
--
-- Der Server merkt sich die Client-id jeder angenommenen Op. Kommt derselbe
-- Batch nach einem Verbindungsabbruch noch einmal, quittiert er die bekannten
-- Ops mit 200, ohne sie erneut zu falten. Vorher bekam die Wiederholung eine
-- neue Eingangszeit und gewann damit gegen eine fremde Änderung, die zwischen
-- beiden Versuchen kam (ADR-0129 §4 Punkt 4, „bekannter Rest“).
--
-- ts in fester Breite wie activities (Migration 0019), damit das Aufräumen
-- nach Frist (retention, Uhr-Gate) über den Index läuft. Mit dem Board weg.
--
-- hash: SHA-256 über Typ und Nutzlast der Op (Sitzungs-QA 2026-09-30, K-3).
-- Kommt eine bekannte id mit anderem Inhalt, ist das kein Wiederholen, sondern
-- eine wiederverwendete id — der Server lehnt ab, statt die zweite Op still
-- als „schon angenommen“ zu quittieren. Die Migration war zu dem Zeitpunkt in
-- keinem Release, deshalb hier ergänzt statt in einer 0023.

CREATE TABLE op_ids (
  board_id   TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  client_id  TEXT NOT NULL,
  hash       TEXT NOT NULL DEFAULT '',
  ts         TEXT NOT NULL,
  PRIMARY KEY (board_id, client_id)
);

CREATE INDEX op_ids_ts ON op_ids(ts);
