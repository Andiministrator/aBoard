-- aBoard server schema, version 2 — Phase 2 Multi-User + ACL.
-- Quelle: ADR-0059 §3. Tabellen werden IMMER angelegt (Schema-Konvergenz);
-- im No-Auth-Modus (ABOARD_TRUST_FORWARD_AUTH=AUS, Default) bleiben sie leer
-- und ungenutzt — Legacy-Bypass, kein synthetischer Default-Owner (ADR-0059 §2).

CREATE TABLE users (
  id       TEXT PRIMARY KEY,            -- UUID
  email    TEXT NOT NULL UNIQUE,
  name     TEXT NOT NULL DEFAULT '',
  created  TEXT NOT NULL                -- ISO-8601
);

-- ACL ist Email-basiert (nicht user-id-basiert): ein Share kann für eine
-- noch nie eingeloggte Email gesetzt werden (Invite-only, ADR-0059 §1).
CREATE TABLE acl (
  board_id    TEXT NOT NULL,
  user_email  TEXT NOT NULL,
  role        TEXT NOT NULL CHECK (role IN ('owner','editor','viewer')),
  created     TEXT NOT NULL,
  PRIMARY KEY (board_id, user_email),
  FOREIGN KEY (board_id) REFERENCES boards(id) ON DELETE CASCADE
);

-- Für die ACL-gefilterte Board-Liste (#198/#201): "welche Boards sieht
-- diese Email?" — Lookup über user_email.
CREATE INDEX idx_acl_user ON acl(user_email);
