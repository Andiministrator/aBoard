-- aBoard server schema, version 40 — Board-Paar Stufe 2a: externe Kopie
-- anlegen, Rückweg-Zugang, Schatten in Lane und Spalte (ADR-0141 §3, §5.3,
-- Karte #825).
--
-- 1. boardpaar_tokens kennt einen dritten Typ `abr` (Board-Paar-Rückweg,
--    Präfix abr_, nur innen): Das Skript meldet damit die Namen seiner Ziele,
--    quittiert Aufträge und wendet ab Schritt 2c Rückweg-Einträge an. Der
--    CHECK auf typ aus 0033 kennt ihn nicht; SQLite ändert keinen CHECK
--    nachträglich, deshalb wird die Tabelle neu aufgebaut („neue Tabelle,
--    kopieren, alte weg, umbenennen“, Muster 0038). Mitgenommen werden alle
--    Spalten aus 0033 und 0037 samt dem Teilindex aus 0037. Die Selbstverweise
--    nachfolger_id/erneuert_aus sind schlichte TEXT-Spalten ohne
--    Fremdschlüssel; sie überstehen das Kopieren unverändert. Kein Trigger
--    und keine andere Tabelle nennt boardpaar_tokens.
--
--    Neue Spalte darf_anlegen (ADR-0141 §3, VF-6): Nur ein abe_-Zugang mit
--    diesem Recht legt außen Kopien an (POST /agent/boardpaar/boards).
--    Standard 0 — bestehende abe_-Zugänge aus Stufe 1 bekommen das Recht
--    NICHT von selbst. Der zweite CHECK hält das Recht beim Typ abe; ein
--    Schreibfehler im Code kann kein abs_ mit Anlege-Recht erzeugen. Beim
--    Erneuern übernimmt der Nachfolger das Recht (storage/boardpaar_erneuern.go,
--    W2-4) — nicht hier.
--
-- 2. boardpaar_aussen.kopie (W2-1): 1 nur für Boards, die über
--    POST /agent/boardpaar/boards als Kopie entstanden sind. Hinweg v2 und
--    (ab 2b) die Spiegel-Schranke gelten nur dort; der alte Schatten-Eingang
--    (PUT /agent/schatten) nur für kopie = 0. Die Stufe-1-Boards in derselben
--    Partnerliste behalten 0 und laufen unverändert weiter.
--
-- 3. boardpaar_ziele: die Namen der externen Server, die das Skript innen
--    meldet (PUT /agent/boardpaar/ziele, abr_). NUR Namen, keine Adressen —
--    der innere Server kennt das externe Board nicht (ADR-0140 §7).
--
-- 4. boardpaar_auftraege: „Externe Kopie dieses Boards auf Ziel X anlegen“,
--    angelegt von einem Admin innen, abgeholt vom Skript (abs_), quittiert mit
--    abr_. Je Board und Ziel höchstens ein offener Auftrag (Teilindex).
--    innen_link_basis ist die Adresse für den Menschen-Link „im Firmennetz
--    öffnen“; sie geht mit dem Auftrag ans Skript und von dort nach außen
--    (dort Pflicht, Migration 0034). Löschen des Boards löscht seine Aufträge.
--
-- 5. schatten.lane / schatten.spalte_id (ADR-0141 §4, §8): Lane und Spalte
--    eines Schattens als Kennung des Skripts (bzw. Klartext-ID der
--    Standardspalten und des Backlogs). Nur der Eingang v2 setzt sie; Zeilen
--    aus Stufe 1 tragen NULL und erscheinen weiter im Band.
--    schatten.fassung: 1 = Stufe 1 (PUT /agent/schatten), 2 = Hinweg v2. Nur
--    Zeilen der Fassung 2 zeigen außen den Titel eines VERTRAULICHEN
--    Schattens (ADR-0141 Beschluss 3); für Fassung 1 bleibt „vertraulich ⇒
--    Interne Karte“, auch wenn eine Zeile am Eingang vorbei einen Titel trüge.
--
-- Ein Rückweg läuft über das Backup von vorher; ein älterer Server startet auf
-- diesem Schema nicht (F-1237).

CREATE TABLE boardpaar_tokens_neu (
  id             TEXT PRIMARY KEY,
  typ            TEXT NOT NULL CHECK (typ IN ('abs', 'abe', 'abr')),
  name           TEXT NOT NULL,
  token_hash     TEXT NOT NULL UNIQUE,
  created_at     TEXT NOT NULL,
  created_by     TEXT NOT NULL DEFAULT '',
  last_used_at   TEXT,
  revoked_at     TEXT,
  gilt_bis       TEXT,
  nachfolger_id  TEXT,
  nachfolger_enc TEXT,
  erneuert_aus   TEXT,
  erneuert_von   TEXT,
  darf_anlegen   INTEGER NOT NULL DEFAULT 0 CHECK (darf_anlegen IN (0, 1)),
  CHECK (darf_anlegen = 0 OR typ = 'abe')
);

INSERT INTO boardpaar_tokens_neu (id, typ, name, token_hash, created_at, created_by, last_used_at, revoked_at,
                                  gilt_bis, nachfolger_id, nachfolger_enc, erneuert_aus, erneuert_von)
SELECT id, typ, name, token_hash, created_at, created_by, last_used_at, revoked_at,
       gilt_bis, nachfolger_id, nachfolger_enc, erneuert_aus, erneuert_von
  FROM boardpaar_tokens;

DROP TABLE boardpaar_tokens;
ALTER TABLE boardpaar_tokens_neu RENAME TO boardpaar_tokens;

CREATE UNIQUE INDEX boardpaar_tokens_name_aktiv ON boardpaar_tokens(typ, name)
  WHERE revoked_at IS NULL AND gilt_bis IS NULL;

ALTER TABLE boardpaar_aussen ADD COLUMN kopie INTEGER NOT NULL DEFAULT 0 CHECK (kopie IN (0, 1));

CREATE TABLE boardpaar_ziele (
  name        TEXT PRIMARY KEY,
  gemeldet_at TEXT NOT NULL
);

CREATE TABLE boardpaar_auftraege (
  id               TEXT PRIMARY KEY,
  board_id         TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  ziel             TEXT NOT NULL,
  innen_link_basis TEXT NOT NULL,
  status           TEXT NOT NULL CHECK (status IN ('offen', 'erledigt')),
  angelegt_at      TEXT NOT NULL,
  von              TEXT NOT NULL DEFAULT '',
  erledigt_at      TEXT
);

CREATE UNIQUE INDEX boardpaar_auftraege_offen ON boardpaar_auftraege(board_id, ziel) WHERE status = 'offen';

ALTER TABLE schatten ADD COLUMN lane TEXT;
ALTER TABLE schatten ADD COLUMN spalte_id TEXT;
ALTER TABLE schatten ADD COLUMN fassung INTEGER NOT NULL DEFAULT 1 CHECK (fassung IN (1, 2));
