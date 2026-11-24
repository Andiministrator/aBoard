-- aBoard server schema, version 43 — Zugang „Sicherungen lesen“ (ADR-0142,
-- Karte #834).
--
-- boardpaar_tokens kennt einen vierten Typ `abb` (Präfix abb_): Ein Skript
-- holt damit die fertigen, verschlüsselten Board-Sicherungen ab
-- (GET /agent/sicherungen, GET /agent/sicherungen/{board}/{datei}). Der CHECK
-- auf typ aus 0040 kennt ihn nicht; SQLite ändert keinen CHECK nachträglich,
-- deshalb wird die Tabelle neu aufgebaut wie in 0040 („neue Tabelle,
-- kopieren, alte weg, umbenennen“). Mitgenommen werden alle Spalten aus 0033,
-- 0037 und 0040 samt dem Teilindex aus 0037. 0041 und 0042 fassen die Tabelle
-- nicht an. Kein Trigger, kein Fremdschlüssel und keine andere Tabelle nennt
-- boardpaar_tokens.
--
-- Das Recht darf_anlegen bleibt beim Typ abe (zweiter CHECK unverändert).
--
-- Ein Rückweg läuft über das Backup von vorher; ein älterer Server startet auf
-- diesem Schema nicht (F-1237).

CREATE TABLE boardpaar_tokens_neu (
  id             TEXT PRIMARY KEY,
  typ            TEXT NOT NULL CHECK (typ IN ('abs', 'abe', 'abr', 'abb')),
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
                                  gilt_bis, nachfolger_id, nachfolger_enc, erneuert_aus, erneuert_von, darf_anlegen)
SELECT id, typ, name, token_hash, created_at, created_by, last_used_at, revoked_at,
       gilt_bis, nachfolger_id, nachfolger_enc, erneuert_aus, erneuert_von, darf_anlegen
  FROM boardpaar_tokens;

DROP TABLE boardpaar_tokens;
ALTER TABLE boardpaar_tokens_neu RENAME TO boardpaar_tokens;

CREATE UNIQUE INDEX boardpaar_tokens_name_aktiv ON boardpaar_tokens(typ, name)
  WHERE revoked_at IS NULL AND gilt_bis IS NULL;
