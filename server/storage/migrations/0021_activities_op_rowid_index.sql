-- aBoard server schema, version 21 — die Kappung der Op-Einträge ordnet nach
-- Einfügereihenfolge statt nach Zeitstempel (F-1212, Sprint 75).
--
-- Bis hierher suchte die Kappung (activities.go kappen) die Grenze über
-- (board_id, ts). Eine Zeile, die mit rückwärts gestellter Uhr geschrieben
-- wurde, sortierte dort als älteste und fiel beim nächsten fälligen Lauf,
-- obwohl sie die neueste war — genau der Pi-ohne-RTC-Fall, für den F-1192 die
-- Frist-Löschung an die Uhr gebunden hat. Die Kappung zählt Einträge und
-- hängt nicht an der Uhr; sie ordnet deshalb jetzt nach rowid.
--
-- rowid trägt die Einfügereihenfolge: Eine Tabelle ohne AUTOINCREMENT vergibt
-- MAX(rowid)+1, eine neue Zeile liegt also immer über allen vorhandenen. Dass
-- eine gelöschte rowid wiederkehren kann, ändert daran nichts — sie kehrt nur
-- als neue größte wieder.
--
-- Der Index (board_id) enthält wie jeder SQLite-Index die rowid als letzten
-- Schlüssel; er ist damit nach (board_id, rowid) sortiert und trägt Grenzsuche
-- und Löschen als Covering-Scan. Der Teilindex aus Migration 0020 über
-- (board_id, ts) hat danach keinen Leser mehr und fällt weg: Er kostete bei
-- jeder Op einen Index-Schreibvorgang.
--
-- Kein Rückweg (K-14, ADR-0129 §5): Das Migrationssystem kennt kein Down; der
-- dokumentierte Rückweg ist das Backup vor dem Update (docs/server-betrieb.md).

DROP INDEX IF EXISTS idx_activities_op;
CREATE INDEX idx_activities_op_rowid ON activities(board_id) WHERE action = 'op';
