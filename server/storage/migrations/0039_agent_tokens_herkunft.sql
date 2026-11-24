-- aBoard server schema, version 39 — Agenten-Tokens: Anbieter und Beleg für den
-- Trainingsausschluss (ADR-0140 §8, Karte #799, Vertrag Push-Skript, Ziffer 11).
--
-- Vorgabe: Inhalte des externen Boards dürfen nicht zum Training verwendet
-- werden. Auf der Seite außen (boardpaar.seite = aussen) verlangen Anlage-Wege
-- in API und CLI deshalb zu jedem Agenten-Token zwei Angaben:
--   anbieter  wer den Agenten betreibt (z. B. „Beispiel-Anbieter, Geschäftstarif“)
--   beleg     Fundstelle des vertraglichen Trainingsausschlusses (z. B. ein
--             Pfad im eigenen Vertragsordner)
-- Innen und ohne Board-Paar sind beide optional.
--
-- GRENZE: Die Spalten DOKUMENTIEREN, sie prüfen nichts. Ein gültiges Token in
-- einem Privatkonto-Werkzeug erkennt der Server nicht. Bestandstokens bleiben
-- gültig, auch ohne Angaben — die Token-Liste markiert sie nur.
--
-- Zwei neue TEXT-Spalten mit DEFAULT '' genügen: kein Umbau der Tabelle, also
-- auch kein CHECK-, Trigger- oder Index-Austausch wie in 0031/0038. Die Länge
-- (80/300 Zeichen) und die Steuerzeichen prüft der Anlage-Weg, nicht die
-- Tabelle. ACHTUNG für künftige Umbauten (Muster 0025/0031/0038 mit
-- ausgeschriebener Spaltenliste): Ein Umbau von agent_tokens muss anbieter und
-- beleg in die Spaltenliste übernehmen, sonst gehen sie still verloren — die
-- Migrationen werden nicht gegen eine Prüfsumme verglichen, nichts fängt es ab
-- außer dem Rundwegtest TestAgentHerkunft799_MigrationUndRundweg. Ein Rückweg
-- läuft über das Backup von vorher; ein älterer Server startet auf diesem
-- Schema nicht (F-1237).

ALTER TABLE agent_tokens ADD COLUMN anbieter TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_tokens ADD COLUMN beleg TEXT NOT NULL DEFAULT '';
