-- aBoard server schema, version 20 — Teilindex über die Op-Einträge des
-- Activity-Logs (Kritiker Sprint 74, Karte #582 Nacharbeit).
--
-- Die Obergrenze je Board (ABOARD_ACTIVITY_MAX_PER_BOARD, activities.go
-- kappen) kappte zunächst ALLE Einträge. Damit konnte jeder, der auf einem
-- Board schreiben darf, per Op-Flut die ACL-Historie (acl-set/acl-deleted)
-- und die board-*-Einträge verdrängen — für ACL-Änderungen an Boards ist das
-- Activity-Log die einzige Spur. Seitdem gilt die Grenze nur für
-- action = 'op'; die übrigen Einträge entstehen nur durch Menschen und
-- Admins und unterliegen weiter der Frist.
--
-- Der Teilindex trägt Grenzsuche und Löschen der Kappung: Ohne ihn müsste
-- SQLite für jeden Index-Eintrag über (board_id, ts) die Tabellenzeile lesen,
-- um action zu prüfen. Mit ihm ist die Grenzsuche ein Covering-Scan.
--
-- Kein Rückweg (K-14, ADR-0129 §5): Das Migrationssystem kennt kein Down.
-- Ein Index ändert keine Daten; ihn stehen zu lassen schadet einer älteren
-- Version nicht.

CREATE INDEX idx_activities_op ON activities(board_id, ts) WHERE action = 'op';
