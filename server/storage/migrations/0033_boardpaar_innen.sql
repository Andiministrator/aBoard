-- aBoard server schema, version 33 — Board-Paar intern/extern, Seite innen
-- (ADR-0140 §2, §4, Karte #778).
--
-- 1. Partnerliste innen (`boardpaar_innen`): die internen Boards, deren
--    Schatten der Ausgang liefern darf. Ein Board, das hier nicht steht,
--    antwortet am Ausgang 404 — ohne Unterschied zu „gibt es nicht“.
--    Löschen des Boards löscht die Freigabe mit. Pflege (koppeln,
--    entkoppeln, CLI, Admin-Dialog) folgt mit Karte #788.
--
-- 2. Zugänge des Board-Paars (`boardpaar_tokens`): instanzweit, keine
--    Person, kein Board. Typ `abs` = Schatten-Ausgang innen (Präfix abs_),
--    Typ `abe` = Schatten-Eingang außen (Präfix abe_, Karte #779). Je Zweck
--    ein eigener Typ (ADR-0138 §6), deshalb nicht `kalender_ausgang_tokens`.
--    Gespeichert wird nur der SHA-256-Hash; der Klartext wird bei der Anlage
--    einmal angezeigt. Widerruf setzt revoked_at.

CREATE TABLE boardpaar_innen (
  board_id TEXT PRIMARY KEY REFERENCES boards(id) ON DELETE CASCADE,
  seit     TEXT NOT NULL,
  von      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE boardpaar_tokens (
  id           TEXT PRIMARY KEY,
  typ          TEXT NOT NULL CHECK (typ IN ('abs', 'abe')),
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  created_at   TEXT NOT NULL,
  created_by   TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  revoked_at   TEXT
);

CREATE UNIQUE INDEX boardpaar_tokens_name_aktiv ON boardpaar_tokens(typ, name) WHERE revoked_at IS NULL;
