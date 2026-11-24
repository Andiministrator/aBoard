-- aBoard server schema, version 48 — „wartet auf dich“ je Board in der
-- Boardliste (ADR-0156, Karte #1197).
--
-- `boards.awaiting_json` ist eine abgeleitete Kopie von
-- `state_json.board.awaitingUser` (nur wenn das ein JSON-Objekt ist, sonst
-- NULL). GET /boards liest sie, statt je Abruf jeden Stand zu parsen
-- (gemessen 471 ms statt 37 ms bei 20 Boards à 6,5 MB).
--
-- Gepflegt von Triggern, nicht von Go: state_json wird an vielen Stellen
-- geschrieben, und jeder vergessene Weg ließe die Spalte still veralten.
-- state_json liegt als BLOB vor; deshalb CAST AS TEXT, und nur bei gültigem
-- JSON — ein unlesbarer Stand ergibt NULL und bricht das Schreiben nie ab.
--
-- Wer `boards` später neu aufbaut, legt beide Trigger wieder an
-- (Test awaiting_spalte_1197_test.go prüft, dass sie existieren).

ALTER TABLE boards ADD COLUMN awaiting_json TEXT;

CREATE TRIGGER boards_awaiting_nach_insert AFTER INSERT ON boards
BEGIN
  UPDATE boards SET awaiting_json = (
    SELECT CASE WHEN json_valid(s) AND json_type(s, '$.board.awaitingUser') = 'object'
                THEN json_extract(s, '$.board.awaitingUser') END
      FROM (SELECT CAST(NEW.state_json AS TEXT) AS s)
  ) WHERE id = NEW.id;
END;

CREATE TRIGGER boards_awaiting_nach_update AFTER UPDATE OF state_json ON boards
BEGIN
  UPDATE boards SET awaiting_json = (
    SELECT CASE WHEN json_valid(s) AND json_type(s, '$.board.awaitingUser') = 'object'
                THEN json_extract(s, '$.board.awaitingUser') END
      FROM (SELECT CAST(NEW.state_json AS TEXT) AS s)
  ) WHERE id = NEW.id;
END;

UPDATE boards SET awaiting_json = (
  SELECT CASE WHEN json_valid(s) AND json_type(s, '$.board.awaitingUser') = 'object'
              THEN json_extract(s, '$.board.awaitingUser') END
    FROM (SELECT CAST(boards.state_json AS TEXT) AS s)
);
