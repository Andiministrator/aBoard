-- aBoard server schema, version 47 — gemeldete Adresse und ihr Bestätigungs-
-- Zustand getrennt (Finding F-2961, Karte #1125; Herkunft F-2911 Teil 3).
--
-- Bisher hängte der Server an eine vom Anbieter nicht bestätigte Adresse den
-- deutschen Text " (unbestätigt)" in konten.gemeldete_email. Das zeigte die
-- englische Oberfläche auf Deutsch, nannte dem Admin keinen Grund und wanderte
-- in jeden Leser der Adresse. Jetzt steht die Adresse rein in gemeldete_email
-- und der Zustand in gemeldete_email_unbestaetigt (0/1); die Oberfläche
-- übersetzt und nennt den Grund.
--
-- Bestand: Werte mit dem Zusatz am Ende verlieren ihn und bekommen das Flag.
-- Der Zusatz ist 14 Zeichen lang (Leerzeichen + "(unbestätigt)"); substr/length
-- zählen in SQLite Zeichen, nicht Bytes.

ALTER TABLE konten ADD COLUMN gemeldete_email_unbestaetigt INTEGER NOT NULL DEFAULT 0
  CHECK (gemeldete_email_unbestaetigt IN (0, 1));

UPDATE konten
   SET gemeldete_email_unbestaetigt = 1,
       gemeldete_email = substr(gemeldete_email, 1, length(gemeldete_email) - length(' (unbestätigt)'))
 WHERE length(gemeldete_email) > length(' (unbestätigt)')
   AND substr(gemeldete_email, -length(' (unbestätigt)')) = ' (unbestätigt)';
