-- aBoard server schema, version 14 — FK-CASCADE für ical_subscriptions
-- (F-425, Karte #355; Nachzügler aus dem Sprint-46-Kritiker zu #308).
--
-- Migration 0013 legte die Tabelle ohne Fremdschlüssel an — anders als `acl`
-- und `activities`, die beide `ON DELETE CASCADE` führen. Folge: Wird ein
-- User oder ein Board gelöscht, bleiben dessen Abo-Zeilen als Waisen liegen.
-- Der daraus entstandene 500-statt-404-Folgefehler ist über F-419 bereits
-- behoben; offen war die Daten-Hygiene — und die zählt hier besonders, weil
-- jede Zeile ein Bearer-Secret trägt (`token_enc`). Ein Geheimnis, das zu
-- einem gelöschten Konto gehört und trotzdem in der Datenbank stehen bleibt,
-- ist genau das, was beim nächsten Blick niemand mehr zuordnen kann.
--
-- SQLite kann Fremdschlüssel nicht per ALTER TABLE nachrüsten. Deshalb der
-- übliche Weg: neue Tabelle, Daten kopieren, alte weg, umbenennen.
--
-- WICHTIG beim Kopieren: Die FK-Prüfung der NEUEN Tabelle greift beim INSERT.
-- Liegen bereits Waisen vor — und genau davon geht dieses Finding aus —,
-- würde ein blindes `INSERT ... SELECT *` die Migration zum Scheitern bringen
-- und damit den Serverstart. Die WHERE-Klausel filtert sie deshalb aus; das
-- ist zugleich das einmalige Aufräumen des Altbestands.
--
-- Die Abo-URLs der ausgefilterten Zeilen sind damit endgültig ungültig. Das
-- ist beabsichtigt: Ihr Besitzer existiert nicht mehr, bzw. das Board, auf das
-- sie zeigen. Ein Feed, der auf ein gelöschtes Board zeigt, liefert seit
-- F-419 ohnehin 404.

CREATE TABLE ical_subscriptions_neu (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL,
    board_id     TEXT,
    token_hash   TEXT NOT NULL UNIQUE,
    token_enc    TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT,
    FOREIGN KEY (user_id)  REFERENCES users(id)  ON DELETE CASCADE,
    FOREIGN KEY (board_id) REFERENCES boards(id) ON DELETE CASCADE
);

INSERT INTO ical_subscriptions_neu
     (id, user_id, board_id, token_hash, token_enc, created_at, last_used_at)
SELECT id, user_id, board_id, token_hash, token_enc, created_at, last_used_at
  FROM ical_subscriptions
 WHERE user_id IN (SELECT id FROM users)
   AND (board_id IS NULL OR board_id IN (SELECT id FROM boards));

DROP TABLE ical_subscriptions;

ALTER TABLE ical_subscriptions_neu RENAME TO ical_subscriptions;

-- Der Index verschwand mit der alten Tabelle und wird hier wieder angelegt
-- (gleicher Name, gleiche Spalten wie in 0013).
CREATE INDEX idx_ical_subs_user ON ical_subscriptions(user_id, created_at);
