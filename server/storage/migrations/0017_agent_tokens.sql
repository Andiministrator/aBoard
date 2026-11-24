-- aBoard server schema, version 17 — Agenten-Tokens (ADR-0129 §3, Karte #553).
--
-- Ein Agent ist KEIN Nutzer (K-1): keine users-Zeile, kein ACL-Eintrag, kein
-- Bootstrap. Board und Rolle stehen hier am Token selbst. Gespeichert wird nur
-- der SHA-256-Hash des Tokens (Vorbild ical_subscriptions); der Klartext wird
-- bei der Anlage einmal angezeigt.
--
-- Widerruf setzt revoked_at statt zu löschen: Der Audit-Eintrag verweist auf
-- den Namen, und ein widerrufenes Token soll nachvollziehbar bleiben. Ein Name
-- ist nur unter den AKTIVEN Tokens eindeutig (partieller Index) — nach einem
-- Widerruf darf derselbe Agent ein neues Token bekommen.
--
-- Nummer 0016 gehört der Protokoll-Erweiterung von activities (Karte #554).

CREATE TABLE agent_tokens (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  board_id    TEXT NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
  role        TEXT NOT NULL CHECK (role IN ('editor', 'viewer')),
  token_hash  TEXT NOT NULL UNIQUE,
  created_at  TEXT NOT NULL,
  created_by  TEXT NOT NULL DEFAULT '',
  revoked_at  TEXT
);

CREATE UNIQUE INDEX agent_tokens_name_aktiv ON agent_tokens(name) WHERE revoked_at IS NULL;
