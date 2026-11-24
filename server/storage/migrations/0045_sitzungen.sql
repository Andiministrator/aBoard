-- aBoard server schema, version 45 — Sitzungen der eingebauten Anmeldung
-- (ADR-0148 §2, Karte #1051).
--
-- sitzungen: eine Zeile je angemeldetem Browser im OIDC-Betrieb
-- (ABOARD_ANMELDUNG=oidc). Der Cookie `__Host-aboard_sitzung` trägt eine
-- Zufalls-ID mit 256 Bit; gespeichert wird nur ihr SHA-256 (hex). Wer die
-- Datenbank liest, kann damit keine Sitzung übernehmen.
--   angelegt, zuletzt, ablauf: Unix-Sekunden (UTC). `ablauf` ist die harte
--   Grenze (Anlage + 7 Tage); ohne Nutzung endet die Sitzung schon nach 8 h
--   (zuletzt + 8 h). `zuletzt` schreibt der Server höchstens einmal pro Minute.
--   email: normalisiert (storage.NormalizeEmail), nie ein Domain-Schlüssel.
--   name: nur Anzeige (Claim `name`, wie Remote-Name, Karte #577).
-- Ohne OIDC-Betrieb bleibt die Tabelle leer. Die Nutzerlöschung räumt die
-- Zeilen der Person in derselben Transaktion ab (user_loeschung.go).
--
-- oidc_bindungen: das Paar (iss, sub) je Adresse (ADR-0148 §2 „Bindung an
-- sub“). Google nennt `email` veränderlich und nicht eindeutig; die Kennung ist
-- `sub`. Der erste OIDC-Login einer Adresse legt die Zeile an (Karte #816),
-- ein späterer Login derselben Adresse mit anderem Paar wird abgewiesen.
-- `nutzer umziehen` (Karte #1052) nimmt die Zeile mit; die Nutzerlöschung
-- entfernt sie samt den Sitzungen (keine Adresse bleibt zurück).
--
-- Kein Down; Rückweg über das Backup von vorher (F-1237). Eine Vorversion
-- startet mit ABOARD_SCHEMA_NEUER_ZULASSEN=1 und ignoriert beide Tabellen.

CREATE TABLE sitzungen (
  id_hash  TEXT PRIMARY KEY CHECK (length(id_hash) = 64),
  email    TEXT NOT NULL CHECK (email <> '' AND substr(email, 1, 1) <> '@'),
  name     TEXT NOT NULL DEFAULT '',
  angelegt INTEGER NOT NULL,
  zuletzt  INTEGER NOT NULL,
  ablauf   INTEGER NOT NULL
);

CREATE INDEX sitzungen_email ON sitzungen (email);
CREATE INDEX sitzungen_ablauf ON sitzungen (ablauf);

CREATE TABLE oidc_bindungen (
  email    TEXT PRIMARY KEY CHECK (email <> '' AND substr(email, 1, 1) <> '@'),
  iss      TEXT NOT NULL CHECK (iss <> ''),
  sub      TEXT NOT NULL CHECK (sub <> ''),
  angelegt INTEGER NOT NULL
);
