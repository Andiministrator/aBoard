-- aBoard server schema, version 34 — Board-Paar intern/extern, Seite außen
-- (ADR-0140 §1, §4, Karte #788).
--
-- Partnerliste außen (`boardpaar_aussen`): die externen Boards, für die der
-- Schatten-Eingang (#779) Schatten annimmt, jeweils mit der Adresse des
-- internen Gegenstücks im Deep-Link-Format von aBoard
-- (`https://<host>/#board=<innen-id>`). Diese Adresse ist die Grundlage des
-- Menschen-Links „im Firmennetz öffnen“ und wird NIE über einen Agentenweg
-- ausgeliefert (ADR-0140 §7).
--
-- Löschen des Boards löscht die Kopplung mit. Die Tabellen `schatten` und
-- `schatten_abgleich` (#779) hängen per Fremdschlüssel an dieser Tabelle;
-- Entkoppeln löscht dann auch die Schatten (ADR-0140 §1).

CREATE TABLE boardpaar_aussen (
  aussen_board_id  TEXT PRIMARY KEY REFERENCES boards(id) ON DELETE CASCADE,
  innen_link_basis TEXT NOT NULL,
  seit             TEXT NOT NULL,
  von              TEXT NOT NULL DEFAULT ''
);
