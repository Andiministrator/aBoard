-- aBoard server schema, version 28 — Ausgangsschleuse, Freigabe und Versand (S3)
-- (ADR-0134 §3.4–§3.7, §4; Karte #686).
--
-- Neue Spalten am Auftragsdatensatz (schleuse_auftraege, Migration 0027):
--
--   - vier_augen: Abschrift des Vier-Augen-Schalters der Freigabe zum
--     Zeitpunkt der ersten Freigabe. Ändert der Admin den Schalter danach,
--     gilt für diesen Auftrag weiter, was beim Freigeben galt.
--   - freigegeben_von (seit 0027): E-Mail der Person, die freigegeben hat —
--     bei Vier-Augen die antragstellende. zweit_freigegeben_von/_am: die
--     zweite Person (mindestens editor am Board, nicht die antragstellende).
--     E-Mail statt user_id, weil das Protokoll „wer“ nennt; die Löschung
--     einer Person ersetzt sie durch ihr Pseudonym (user_loeschung.go).
--   - gesendet_am: Zeitpunkt, an dem der Server den Auftrag auf „gesendet“
--     gesetzt hat — UNMITTELBAR vor dem Verbindungsaufbau. Grundlage der
--     Grenze ABOARD_SCHLEUSE_AUFTRAEGE_PRO_STUNDE.
--   - abgeschlossen_am: „beantwortet“ oder „fehlgeschlagen“. Ab hier läuft
--     die Frist der Zuordnung (ADR-0134 §4).
--   - antwort_text/antwort_sha256: die Antwort des Anbieters, PSEUDONYMISIERT,
--     so wie sie ankam (Beleg, ADR-0134 §3.6).
--   - kommentar_id/kommentar_sha256: der Antwortkommentar „extern“ an der
--     Karte und der SHA-256 seines re-identifizierten Texts beim Schreiben.
--     Weicht der Kommentar im Board später davon ab, zeigt die Oberfläche
--     „weicht vom Original ab“.
--   - veraenderte_platzhalter: JSON-Array der Platzhalter, die das Modell
--     verändert oder erfunden hat — sie bleiben im Kommentar stehen.
--   - fehler_grund: maschinenlesbarer Grund (nie Antworttext des Anbieters).
--   - input_tokens/output_tokens/kosten_usd/finish_reason: Protokoll.

ALTER TABLE schleuse_auftraege ADD COLUMN vier_augen INTEGER NOT NULL DEFAULT 0 CHECK (vier_augen IN (0, 1));
ALTER TABLE schleuse_auftraege ADD COLUMN zweit_freigegeben_von TEXT;
ALTER TABLE schleuse_auftraege ADD COLUMN zweit_freigegeben_am TEXT;
ALTER TABLE schleuse_auftraege ADD COLUMN gesendet_am TEXT;
ALTER TABLE schleuse_auftraege ADD COLUMN abgeschlossen_am TEXT;
ALTER TABLE schleuse_auftraege ADD COLUMN antwort_text TEXT;
ALTER TABLE schleuse_auftraege ADD COLUMN antwort_sha256 TEXT;
ALTER TABLE schleuse_auftraege ADD COLUMN kommentar_id TEXT;
ALTER TABLE schleuse_auftraege ADD COLUMN kommentar_sha256 TEXT;
ALTER TABLE schleuse_auftraege ADD COLUMN veraenderte_platzhalter TEXT NOT NULL DEFAULT '[]';
ALTER TABLE schleuse_auftraege ADD COLUMN fehler_grund TEXT;
ALTER TABLE schleuse_auftraege ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE schleuse_auftraege ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE schleuse_auftraege ADD COLUMN kosten_usd REAL NOT NULL DEFAULT 0;
ALTER TABLE schleuse_auftraege ADD COLUMN finish_reason TEXT;

CREATE INDEX idx_schleuse_auftraege_gesendet ON schleuse_auftraege(user_id, gesendet_am);
