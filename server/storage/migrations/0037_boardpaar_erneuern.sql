-- aBoard server schema, version 37 — Board-Paar: Zugänge erneuern sich selbst
-- (Karte #796, T-R12, Vertrag zum Board-Paar, 10.10.2026).
--
-- Das Push-Skript des Betreibers tauscht sein Token abs_ bzw. abe_ regelmäßig gegen
-- ein neues, mit dem alten als Ausweis (POST /agent/boardpaar/erneuern). Das
-- alte gilt danach noch eine Übergangszeit (10 min) und ist dann tot.
--
-- Am ALTEN Token:
--   gilt_bis        Ende der Übergangszeit; NULL = nie erneuert.
--   nachfolger_id   das Token, das bei der Erneuerung entstand.
--   nachfolger_enc  der Klartext des Nachfolgers, AES-256-GCM-verschlüsselt mit
--                   einem Schlüssel aus dem Klartext des ALTEN Tokens. Wer in der
--                   Übergangszeit nochmals mit dem alten erneuert, bekommt so
--                   DASSELBE neue Token zurück (Vertrag Punkt 1: ein Abbruch
--                   zwischen Antwort und Speichern sperrt nicht aus). Ohne das
--                   alte Token ist der Wert nicht lesbar; die Datenbank allein
--                   verrät den Nachfolger nicht. Geleert nach der Übergangszeit,
--                   bei der nächsten Erneuerung und beim Widerruf.
-- Am NEUEN Token:
--   erneuert_aus    ID des Vorgängers (NULL = von Hand angelegt).
--   erneuert_von    Quelladresse der Erneuerung (Vertrag Punkt 3: ein
--                   Diebstahl ist außen nachweisbar).
--
-- Der Name eines Zugangs ist nur unter den Tokens ohne Widerruf UND ohne
-- Erneuerung eindeutig — sonst kollidierte der Nachfolger mit dem Vorgänger,
-- der in der Übergangszeit noch gilt. Index deshalb neu.
--
-- Nur neue, nullbare Spalten und ein ersetzter Teilindex: Ein Rückweg auf
-- v1.51.x läuft trotzdem über das Backup von vorher (die Migration ist nicht
-- umkehrbar eingetragen). Ein älterer Server startet auf diesem Schema nicht
-- (F-1237); wer ihn mit dem Notschalter dennoch startet, für den gelten alle
-- Vorgänger wieder — er kennt gilt_bis nicht.

ALTER TABLE boardpaar_tokens ADD COLUMN gilt_bis TEXT;
ALTER TABLE boardpaar_tokens ADD COLUMN nachfolger_id TEXT;
ALTER TABLE boardpaar_tokens ADD COLUMN nachfolger_enc TEXT;
ALTER TABLE boardpaar_tokens ADD COLUMN erneuert_aus TEXT;
ALTER TABLE boardpaar_tokens ADD COLUMN erneuert_von TEXT;

DROP INDEX boardpaar_tokens_name_aktiv;
CREATE UNIQUE INDEX boardpaar_tokens_name_aktiv ON boardpaar_tokens(typ, name)
  WHERE revoked_at IS NULL AND gilt_bis IS NULL;
