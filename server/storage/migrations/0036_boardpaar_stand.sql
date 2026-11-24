-- aBoard server schema, version 36 — Board-Paar: letzter Abruf und letzter
-- Fehlgrund je gekoppeltem Board (Karte #805, F-2019, Sprint-89-Kritiker KS-4).
--
-- Bis v1.51.0 sah der Admin nicht, wenn der Abgleich eines Boards hing: innen
-- antwortete der Ausgang mit 500 (z. B. über der Betriebsgrenze), außen lehnte
-- der Eingang mit 4xx ab — beides stand nur im Log, und das Board blieb
-- unbemerkt auf dem alten Stand. Jetzt merkt sich jede Seite je Kopplung den
-- Zeitpunkt des letzten Abrufs bzw. Eingangs und den Grund des letzten
-- Fehlschlags (NULL nach einem erfolgreichen Lauf). Admin-Reiter und
-- `aboard-server boardpaar liste` zeigen beides.
--
-- Nur neue, nullbare Spalten: Ein Rückweg auf v1.51.0 läuft trotzdem über das
-- Backup von vorher (die Migration ist nicht umkehrbar eingetragen).

ALTER TABLE boardpaar_innen ADD COLUMN letzte_abfrage TEXT;
ALTER TABLE boardpaar_innen ADD COLUMN letzter_fehlgrund TEXT;

ALTER TABLE boardpaar_aussen ADD COLUMN letzter_eingang TEXT;
ALTER TABLE boardpaar_aussen ADD COLUMN letzter_fehlgrund TEXT;
