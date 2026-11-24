-- aBoard server schema, version 27 — Ausgangsschleuse, Aufträge (S2)
-- (ADR-0134 §3.1–§3.4, §4; Karte #685).
--
-- schleuse_auftraege: der Auftragsdatensatz AUSSERHALB des Board-JSON. Je
-- Zeile ein Auszug einer Karte, den eine Person für ein externes Modell
-- vorbereitet.
--
--   - text ist der PSEUDONYMISIERTE, normalisierte Auszug — so, wie die
--     Person ihn in der Durchsicht sieht und S3 ihn sendet. Den Klartext
--     speichert die Schleuse nirgends; er entsteht beim Erstellen aus dem
--     gespeicherten Board-Stand und wird nach der Pseudonymisierung verworfen.
--   - text_sha256 ist der SHA-256 über genau diese Bytes (ADR-0134 §3.4).
--   - zuordnung_verschluesselt: Platzhalter → Original als JSON, verschlüsselt
--     mit secretcrypto.Box (wie schleuse_schluessel). Geht nie an den Browser.
--     NULL, sobald die Aufbewahrungsfrist abgelaufen ist (S3) — danach ist der
--     Datensatz nicht mehr re-identifizierbar (ADR-0134 §4).
--   - ersetzungen: Platzhalter samt Art und Quelle (fest/modell) OHNE
--     Originale, für die Markierung in der Durchsicht.
--   - zustand: S2 schreibt nur 'entwurf'. Die CHECK-Liste nennt schon den
--     ganzen Ablauf aus ADR-0134 §3.5, damit S3 ohne Tabellenumbau und ohne
--     Migration der Werte weiterzählen kann.
--   - freigabe_sha256/freigegeben_von/freigegeben_am: für S3. Jede Änderung
--     am Text leert sie (Code und CHECK unten: im Zustand 'entwurf' stehen sie
--     immer leer).
--   - freigabe_id: ohne Fremdschlüssel. Ein Auftrag, der in S3 gesendet ist,
--     ist ein Protokoll — er darf nicht verschwinden, weil der Admin den
--     Anbieter von der Liste nimmt. anbieter und modell stehen deshalb
--     als Abschrift dabei.
--   - user_id/user_email: die Person. Wie bei schleuse_schluessel ohne
--     Fremdschlüssel auf users; die Löschung der Person räumt die Zeilen ab
--     (storage/user_loeschung.go).

CREATE TABLE schleuse_auftraege (
    id                        TEXT PRIMARY KEY,
    board_id                  TEXT NOT NULL,
    card_id                   TEXT NOT NULL,
    user_id                   TEXT NOT NULL,
    user_email                TEXT NOT NULL,
    freigabe_id               TEXT NOT NULL,
    anbieter                  TEXT NOT NULL,
    modell                    TEXT NOT NULL,
    zustand                   TEXT NOT NULL DEFAULT 'entwurf'
        CHECK (zustand IN ('entwurf', 'freigegeben', 'gesendet', 'beantwortet', 'fehlgeschlagen')),
    teile                     TEXT NOT NULL DEFAULT '{}',
    text                      TEXT NOT NULL,
    text_sha256               TEXT NOT NULL,
    ersetzungen               TEXT NOT NULL DEFAULT '[]',
    zuordnung_verschluesselt  TEXT,
    freigabe_sha256           TEXT,
    freigegeben_von           TEXT,
    freigegeben_am            TEXT,
    erstellt_am               TEXT NOT NULL,
    geaendert_am              TEXT NOT NULL,
    CHECK (zustand <> 'entwurf'
        OR (freigabe_sha256 IS NULL AND freigegeben_von IS NULL AND freigegeben_am IS NULL))
);

CREATE INDEX idx_schleuse_auftraege_person ON schleuse_auftraege(user_id);
CREATE INDEX idx_schleuse_auftraege_karte ON schleuse_auftraege(board_id, card_id);
CREATE INDEX idx_schleuse_auftraege_zustand ON schleuse_auftraege(zustand, geaendert_am);
