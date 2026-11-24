-- aBoard server schema, version 23 — Agenten-Tokens je (Person, Board)
-- (ADR-0130 §6, Karte #608).
--
-- agent_tokens.user_id: Gehört ein Token einer Person, gilt es nur, solange
-- die Person das Board sieht. Die wirksame Rolle ist das Minimum aus
-- Token-Rolle und aktueller ACL-Rolle, je Request ermittelt; keine ACL-Rolle
-- heisst kein Zugriff. NULL = Projekt-Token ohne Person (Team-Container,
-- ADR-0129 §3) — die bleiben, wie sie sind. Bewusst OHNE Fremdschlüssel mit
-- ON DELETE: ein SET NULL machte aus dem Token einer gelöschten Person still
-- ein Projekt-Token. Das Löschen einer Person widerruft ihre Tokens in
-- derselben Transaktion (storage/user_loeschung.go); ein Token, dessen
-- Person fehlt, gilt ohnehin nicht (Lookup verlangt die users-Zeile).
--
-- expires_at: Ablaufzeit (Voreinstellung 90 Tage, höchstens 365). Feste
-- Breite `YYYY-MM-DDTHH:MM:SSZ`, damit der Textvergleich stimmt. NULL =
-- unbefristet: nur Tokens, die vor dieser Migration angelegt wurden.
--
-- kennung: der nicht geheime Teil neuer Tokens (abt_<kennung>_<geheimnis>).
-- Die Fehlversuch-Bremse zählt je Kennung, damit ein falsches Token von
-- Projekt A nicht Projekt B hinter derselben Adresse sperrt; Listen zeigen
-- sie, damit ein Mensch ein Token wiedererkennt, ohne es zu sehen. NULL =
-- Altformat abt_<geheimnis>, das weiter gilt.
--
-- boards.agent_selbstbedienung: Admin-Schalter je Board. Ab Werk aus —
-- ohne ihn legt sich niemand selbst einen Agenten-Zugang an.
--
-- activities.on_behalf_of / session: Für wen ein Agent gehandelt hat und in
-- welcher Sitzung. Die Pseudonymisierung beim Löschen einer Person erfasst
-- on_behalf_of mit.

ALTER TABLE agent_tokens ADD COLUMN user_id TEXT;
ALTER TABLE agent_tokens ADD COLUMN expires_at TEXT;
ALTER TABLE agent_tokens ADD COLUMN kennung TEXT;
CREATE UNIQUE INDEX agent_tokens_kennung ON agent_tokens(kennung) WHERE kennung IS NOT NULL;
CREATE INDEX agent_tokens_user ON agent_tokens(user_id) WHERE user_id IS NOT NULL;

ALTER TABLE boards ADD COLUMN agent_selbstbedienung INTEGER NOT NULL DEFAULT 0;

ALTER TABLE activities ADD COLUMN on_behalf_of TEXT NOT NULL DEFAULT '';
ALTER TABLE activities ADD COLUMN session TEXT NOT NULL DEFAULT '';
