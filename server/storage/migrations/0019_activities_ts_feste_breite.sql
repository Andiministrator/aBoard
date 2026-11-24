-- aBoard server schema, version 19 — Activity-Log: Zeitstempel mit fester
-- Breite und Indizes, die Löschen und Lesen tragen (F-1157, Karte #582).
--
-- Bis hierher schrieb der Server ts als RFC3339Nano. Das lässt
-- Null-Nachkommastellen weg ("…12:00:00Z" neben "…12:00:00.5Z"), als
-- Zeichenkette sortiert der spätere Wert dann VOR dem früheren. Deshalb
-- verglichen Frist-Löschung und Lese-Endpunkt nur substr(ts, 1, 19) — und
-- auf einen Ausdruck greift kein Index: Jeder Record/RecordOps prüfte unter
-- der Schreibsperre jeden Eintrag des Boards, jeder Lesezugriff sortierte
-- alle Einträge (TEMP B-TREE).
--
-- Seit dieser Migration schreibt der Server "YYYY-MM-DDTHH:MM:SS.mmmZ"
-- (activityTSFormat in activities.go, dieselbe Form wie SQLites
-- strftime('%Y-%m-%dT%H:%M:%fZ')). Der Bestand wird hier auf dieselbe Form
-- gebracht: Nachkommastellen auf drei Ziffern aufgefüllt bzw. abgeschnitten
-- (abschneiden, nicht runden — sonst rutschte ein Wert in die nächste
-- Sekunde). Die Reihenfolge innerhalb derselben Millisekunde bleibt über
-- rowid erhalten, die UPDATE nicht ändert.
--
-- Nur Werte in genau der Form, die nowISO() erzeugt hat, werden umgeschrieben:
-- 19 Zeichen Datum/Uhrzeit, optional "." mit 1–9 Ziffern, dann "Z". Alles
-- andere bleibt unverändert stehen (es gibt dafür keinen bekannten Schreiber;
-- ein Wert, der mit "YYYY-MM-DDTHH:MM:SS" beginnt, wird von der Frist trotzdem
-- richtig behandelt, siehe trim()).
--
-- Die Indizes wechseln von (…, ts DESC) auf (…, ts): SQLite hängt rowid
-- aufsteigend an jeden Index-Eintrag. Rückwärts gelesen liefert (board_id, ts)
-- damit genau `ORDER BY ts DESC, rowid DESC`, (board_id, ts DESC) dagegen
-- nicht — dort blieb ein Sortierschritt für rowid.
--
-- Kein Rückweg (K-14, ADR-0129 §5): Das Migrationssystem kennt kein Down.
-- Vor dem Update ein Server-Backup ziehen. Die umgeschriebenen Zeitstempel
-- sind für alle Leser gültiges ISO-8601.

UPDATE activities
   SET ts = substr(ts, 1, 19) || '.' ||
            substr(CASE WHEN substr(ts, 20, 1) = '.' THEN substr(ts, 21, length(ts) - 21) ELSE '' END || '000', 1, 3) ||
            'Z'
 WHERE ts GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]*Z'
   AND (length(ts) = 20
        OR (substr(ts, 20, 1) = '.'
            AND length(ts) BETWEEN 22 AND 30
            AND substr(ts, 21, length(ts) - 21) NOT GLOB '*[^0-9]*'));

DROP INDEX idx_activities_board;
DROP INDEX idx_activities_card;
CREATE INDEX idx_activities_board ON activities(board_id, ts);
CREATE INDEX idx_activities_card ON activities(board_id, card_id, ts);
