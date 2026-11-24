-- aBoard server schema, version 46 — eigene Konten, Einladungen und
-- Anbieter-Bindungen (ADR-0149 §4, §5.2, §7; Karte #1069). Die Migration
-- trägt beide Sprints (109: mehrere Anbieter, 110: Passwort, 2FA, Einladung);
-- Sprint 109 liest und schreibt nur anbieter_bindungen und fragt konten ab.
--
-- konten: Konten im Werden und eigene Anmeldedaten. `users` bleibt „Person
-- hat Zugang“ (ADR-0149 §5.2, Kritik B3): Eine Einladung legt nur eine Zeile
-- hier an, erst die Bestätigung ruft BootstrapNewUser auf.
--   email: die eingeladene Adresse, normalisiert (storage.NormalizeEmail),
--     zugleich der Benutzername (§2). Nie ein Domain-Schlüssel.
--   zustand: invited → pending → active, dazu blocked (§5).
--   weg: der beim Einlösen gewählte Anmeldeweg ('' solange eingeladen,
--     'password' oder das Kürzel eines Anbieters).
--   gemeldete_email, gemeldeter_name: was der Anbieter beim Einlösen gemeldet
--     hat — nur zur Prüfung durch den Admin vor der Bestätigung (§5.5).
--   passwort_hash: argon2id im PHC-Format (Sprint 110), sonst NULL.
--   totp_geheimnis: mit internal/secretcrypto versiegelt (Sprint 110), sonst
--     NULL. totp_letzter_schritt: zuletzt angenommener Zeitschritt, damit ein
--     Code nur einmal gilt (§3).
--   angelegt, geaendert: Unix-Sekunden (UTC).
-- Bestehende Personen bekommen KEINE Zeile (§5.6): Ihr Zugang hängt weiter
-- an der Vorab-Liste.
--
-- konto_codes: Wiederherstellungscodes, nur als SHA-256 (hex), einmal nutzbar
-- (§3). `verbraucht` = Unix-Sekunden der Nutzung, NULL solange offen.
--
-- einladungen: Einladungs- und Rücksetz-Links (§5.3, §5.5). Gespeichert wird
-- nur der SHA-256 (hex) des 256-Bit-Tokens. `zweck` ist 'invite' oder
-- 'reset'; `ablauf` ist Anlage + 7 Tage; `eingeloest` ist NULL, bis der Link
-- benutzt wurde.
--
-- anbieter_bindungen: (iss, sub) eines OIDC-Kontos → Person (§4, Kritik W4).
-- Ersetzt oidc_bindungen (email als Primärschlüssel, ein Paar je Adresse).
-- Die Bindung bestimmt die Person, nicht die gemeldete Adresse.
--   anbieter: Kürzel aus ABOARD_OIDC_PROVIDERS ([a-z0-9-], nicht 'password');
--     'standard' für den Anbieter aus ABOARD_OIDC_ISSUER (pm).
--   konto_id → konten, user_id → users; beide nullbar, mindestens eines
--     gesetzt. Zwischen Einlösen und Bestätigung trägt die Bindung nur
--     konto_id; die Bestätigung setzt user_id (Sitzungs-Kritik 02.11. K-5).
--   Die Teil-Indizes unten erlauben je Person und Anbieter höchstens eine
--   Bindung. Der erste Login bindet nur, wenn die Person noch gar keine hat
--   (AnbieterBindungRepo.PruefenFuer, keine Verknüpfung per Adresse); weitere
--   Bindungen entstehen nur über Einladung oder Konto-Einstellungen (ADR-0149 §4).
--   Keine Adresse in dieser Tabelle: `nutzer umziehen` muss sie nicht
--   anfassen, die Bindung folgt users.id.
--
-- Übernahme: Jede Zeile aus oidc_bindungen wird Anbieter 'standard' mit der
-- user_id der gleichlautenden users-Zeile (bei mehreren Zeilen, die sich nur in
-- der Schreibweise unterscheiden, die älteste — wie AnbieterBindungRepo.nutzerID,
-- Sitzungs-Kritik 02.11. K1-2). Eine Bindung ohne users-Zeile
-- (Login abgeschlossen, aber nie ein Board-Aufruf) entfällt; die Adresse
-- bindet beim nächsten Login neu, Zulassung wie bisher über die Vorab-Liste.
-- Trüge dasselbe (iss, sub) mehrere Adressen, gewinnt die älteste Bindung.
--
-- Kein Down; Rückweg über das Backup von vorher (F-1237). Eine Vorversion
-- startet auch mit ABOARD_SCHEMA_NEUER_ZULASSEN=1 im OIDC-Betrieb nicht
-- sinnvoll, weil oidc_bindungen fehlt — Rückweg nur über die Sicherung.

CREATE TABLE konten (
  id                   TEXT PRIMARY KEY CHECK (id <> ''),
  email                TEXT NOT NULL UNIQUE CHECK (email <> '' AND substr(email, 1, 1) <> '@' AND email = lower(trim(email))),
  zustand              TEXT NOT NULL CHECK (zustand IN ('invited', 'pending', 'active', 'blocked')),
  weg                  TEXT NOT NULL DEFAULT '' CHECK (weg NOT GLOB '*[^a-z0-9-]*'),
  gemeldete_email      TEXT NOT NULL DEFAULT '',
  gemeldeter_name      TEXT NOT NULL DEFAULT '',
  passwort_hash        TEXT,
  totp_geheimnis       TEXT,
  totp_letzter_schritt INTEGER NOT NULL DEFAULT 0,
  angelegt             INTEGER NOT NULL,
  geaendert            INTEGER NOT NULL
);

CREATE TABLE konto_codes (
  konto_id   TEXT NOT NULL REFERENCES konten(id) ON DELETE CASCADE,
  code_hash  TEXT NOT NULL CHECK (length(code_hash) = 64),
  angelegt   INTEGER NOT NULL,
  verbraucht INTEGER,
  PRIMARY KEY (konto_id, code_hash)
);

CREATE TABLE einladungen (
  token_hash TEXT PRIMARY KEY CHECK (length(token_hash) = 64),
  konto_id   TEXT NOT NULL REFERENCES konten(id) ON DELETE CASCADE,
  zweck      TEXT NOT NULL CHECK (zweck IN ('invite', 'reset')),
  angelegt   INTEGER NOT NULL,
  ablauf     INTEGER NOT NULL,
  eingeloest INTEGER
);

CREATE INDEX einladungen_konto ON einladungen (konto_id);
CREATE INDEX einladungen_ablauf ON einladungen (ablauf);

CREATE TABLE anbieter_bindungen (
  iss      TEXT NOT NULL CHECK (iss <> ''),
  sub      TEXT NOT NULL CHECK (sub <> ''),
  anbieter TEXT NOT NULL CHECK (anbieter <> '' AND anbieter <> 'password' AND anbieter NOT GLOB '*[^a-z0-9-]*'),
  konto_id TEXT REFERENCES konten(id) ON DELETE CASCADE,
  user_id  TEXT REFERENCES users(id) ON DELETE CASCADE,
  angelegt INTEGER NOT NULL,
  PRIMARY KEY (iss, sub),
  CHECK (konto_id IS NOT NULL OR user_id IS NOT NULL)
);

CREATE UNIQUE INDEX anbieter_bindungen_person ON anbieter_bindungen (user_id, anbieter) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX anbieter_bindungen_konto ON anbieter_bindungen (konto_id, anbieter) WHERE konto_id IS NOT NULL;

INSERT INTO anbieter_bindungen (iss, sub, anbieter, konto_id, user_id, angelegt)
SELECT b.iss, b.sub, 'standard', NULL, u.id, b.angelegt
  FROM oidc_bindungen b
  JOIN users u ON u.id = (SELECT x.id FROM users x WHERE lower(x.email) = lower(b.email) ORDER BY x.created, x.id LIMIT 1)
 WHERE true
 ORDER BY b.angelegt, b.email
ON CONFLICT DO NOTHING;

DROP TABLE oidc_bindungen;
