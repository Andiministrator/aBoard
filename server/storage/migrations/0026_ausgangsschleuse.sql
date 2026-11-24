-- aBoard server schema, version 26 — Ausgangsschleuse, Grundlage S1
-- (ADR-0134 §1, §2, §4; Karte #684).
--
-- 1. schleuse_freigaben: die Admin-Freigabeliste. Je Eintrag ein Anbieter
--    samt Basis-URL, den erlaubten Modellen (JSON-Array), der Bestätigung der
--    Vertragsform (geschäftliches API-Konto mit AVV, ohne Training — mit
--    Zeitpunkt und Admin), dem Vier-Augen-Schalter (Vorgabe aus) und „aktiv“.
--    Ob ein Eintrag NUTZBAR ist, steht nicht in der Tabelle: Das hängt auch an
--    ABOARD_AUSGAENGE/ABOARD_AUSGAENGE_ERLAUBT und wird bei jedem Lesen gegen
--    die geltende Politik berechnet (server/schleuse).
--
-- 2. schleuse_schluessel: je (Person, Freigabe) ein Schlüssel, verschlüsselt
--    mit secretcrypto.Box wie llm_api_keys — aber eine EIGENE Tabelle. Die
--    Schleuse liest llm_api_keys nie und der Board-Chat liest diese Tabelle
--    nie (ADR-0133 §2, Firmenmodus). Wird eine Freigabe gelöscht, gehen ihre
--    Schlüssel mit (Fremdschlüssel; der Löschpfad im Code räumt zusätzlich
--    ausdrücklich ab). Die Person zu löschen räumt ihre Zeilen ab
--    (storage/user_loeschung.go). Wie bei llm_api_keys gibt es keinen
--    Fremdschlüssel auf users (Bestand-Schema).
--
-- 3. boards.schleuse_gesperrt: die Board-Sperre. Vorbild ist
--    boards.agent_selbstbedienung (0023) — eine serverseitige Einstellung je
--    Board als Spalte, wer sie gestellt hat, steht im server_config_audit.
--    Vorgabe 0 = offen (ADR-0134 §2: offen, sobald der Server-Schalter an ist).

CREATE TABLE schleuse_freigaben (
    id                           TEXT PRIMARY KEY,
    anbieter                     TEXT NOT NULL CHECK (anbieter IN ('openai-kompatibel', 'anthropic', 'mistral', 'google')),
    bezeichnung                  TEXT NOT NULL DEFAULT '',
    basis_url                    TEXT NOT NULL,
    modelle                      TEXT NOT NULL DEFAULT '[]',
    vertragsform_bestaetigt      INTEGER NOT NULL DEFAULT 0 CHECK (vertragsform_bestaetigt IN (0, 1)),
    vertragsform_bestaetigt_am   TEXT,
    vertragsform_bestaetigt_von  TEXT,
    vier_augen                   INTEGER NOT NULL DEFAULT 0 CHECK (vier_augen IN (0, 1)),
    aktiv                        INTEGER NOT NULL DEFAULT 1 CHECK (aktiv IN (0, 1)),
    erstellt_am                  TEXT NOT NULL,
    erstellt_von                 TEXT NOT NULL DEFAULT '',
    geaendert_am                 TEXT NOT NULL,
    geaendert_von                TEXT NOT NULL DEFAULT '',
    -- Bestätigt heisst: mit Zeitpunkt und Admin. Ein Eintrag „bestätigt, aber
    -- von niemandem“ kann auch ein Schreibfehler im Code nicht erzeugen.
    CHECK (vertragsform_bestaetigt = 0
        OR (vertragsform_bestaetigt_am IS NOT NULL AND vertragsform_bestaetigt_von IS NOT NULL)),
    UNIQUE (anbieter, basis_url)
);

CREATE TABLE schleuse_schluessel (
    user_id                        TEXT NOT NULL,
    freigabe_id                    TEXT NOT NULL REFERENCES schleuse_freigaben(id) ON DELETE CASCADE,
    key_encrypted                  TEXT NOT NULL,
    geschaeftskonto_bestaetigt_am  TEXT NOT NULL,
    erstellt_am                    TEXT NOT NULL,
    PRIMARY KEY (user_id, freigabe_id)
);

CREATE INDEX idx_schleuse_schluessel_freigabe ON schleuse_schluessel(freigabe_id);

ALTER TABLE boards ADD COLUMN schleuse_gesperrt INTEGER NOT NULL DEFAULT 0;
