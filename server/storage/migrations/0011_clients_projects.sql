-- 0011_clients_projects.sql — Client-/Projekt-Tracking (ADR-0102, Karte #307).
--
-- Server-weite Entities: ein Kunde kann Karten in mehreren Boards haben, daher
-- bewusst NICHT pro Board. Karten-Zuordnung (project_id) lebt im Karten-JSON
-- (ADR-0044 — boards.state_json bleibt opak; KEIN cards-Table existiert).
--
-- Soft-Delete via active=0; Hard-Delete erfordert ON DELETE SET NULL für die
-- projects.client_id-Beziehung (Projekte überleben Client-Lösch, verlieren nur
-- die Zuordnung). Karten-Projekt-Cleanup beim project-Hard-Delete passiert
-- lazy (Frontend ignoriert unbekannte project_id) oder eager via Service-Job
-- (Folgekarte). Hier kein DB-Cascade — Karten-JSON wird vom Server NICHT
-- relational verwaltet.

CREATE TABLE clients (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  description TEXT,
  contact     TEXT,
  active      INTEGER NOT NULL DEFAULT 1,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE projects (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT,
  client_id   TEXT REFERENCES clients(id) ON DELETE SET NULL,
  active      INTEGER NOT NULL DEFAULT 1,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  UNIQUE(name, client_id)
);

CREATE INDEX projects_client_id ON projects(client_id);
CREATE INDEX projects_active   ON projects(active);
CREATE INDEX clients_active    ON clients(active);
