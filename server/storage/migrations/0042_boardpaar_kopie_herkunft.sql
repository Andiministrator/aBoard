-- aBoard server schema, version 42 — Herkunft externer Kopien (ADR-0141
-- Nachtrag #831, Kritik K-1).
--
-- boardpaar_kopie_herkunft: ein Eintrag je Board, das außen über
-- POST /agent/boardpaar/boards als Kopie ENTSTANDEN ist. Geschrieben nur von
-- KopieAnlegen in derselben Transaktion wie das Board; keine Schnittstelle
-- (REST, Ops, Ganz-PUT, Restore) schreibt hier. Das Entkoppeln außen löscht die
-- Zeile in boardpaar_aussen, die Herkunft bleibt — daran erkennt der Server
-- eine außen entkoppelte Kopie (Notweg „Kopie außen löschen“, Admin-Liste,
-- Wiederaufnahme beim nächsten Auftrag). Bis #831-Nacharbeit entschieden das
-- die Spiegel-Marken im Board-Zustand; die kann jeder Board-Owner per PUT oder
-- Op setzen, die Herkunft nicht.
--
-- Backfill: jedes Board, das heute mit kopie = 1 gekoppelt ist. Kopien, die
-- schon VOR dieser Migration außen entkoppelt wurden, bekommen keine Herkunft
-- (sie sind von einem gewöhnlichen Board mit Marken nicht zu unterscheiden);
-- sie gelten danach als gewöhnliche Boards — ein Owner löscht sie auf dem
-- normalen Weg, ein neuer Auftrag findet die ID belegt.
--
-- Die Zeile verschwindet mit dem Board (Fremdschlüssel). Kein Down; Rückweg
-- nur über das Backup von vorher (F-1237). Eine Vorversion, die man mit
-- ABOARD_SCHEMA_NEUER_ZULASSEN=1 trotzdem startet, ignoriert die Tabelle und
-- entscheidet wieder an den Marken.

CREATE TABLE boardpaar_kopie_herkunft (
  board_id    TEXT PRIMARY KEY REFERENCES boards(id) ON DELETE CASCADE,
  angelegt_at TEXT NOT NULL
);

INSERT INTO boardpaar_kopie_herkunft (board_id, angelegt_at)
SELECT aussen_board_id, seit FROM boardpaar_aussen
 WHERE kopie = 1 AND aussen_board_id IN (SELECT id FROM boards);
