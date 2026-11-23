/* @vitest-environment happy-dom */

/**
 * F-290: Drift-Schutz für die Board-Level-Feldlisten.
 *
 * `src/diff.js` BOARD_FIELDS (Browser-Persist: State-Diff → board.field-Ops)
 * und `src/ops.js` BOARD_FIELD_ALLOWLIST (Fold: erlaubte board.field-Felder)
 * sind zwei hartkodierte Listen. Bekommt das Board-Modell künftig ein neues
 * diffbares Top-Level-Feld, ohne dass BEIDE Listen nachgezogen werden, verliert
 * `diffToOps` Änderungen daran lautlos (kein Op erzeugt) bzw. der Fold lehnt sie
 * ab. Dieser Test bindet beide Listen an die kanonischen `Model.createBoard`-
 * Keys und schlägt fehl, sobald eine Liste oder das Modell driftet.
 */

import { describe, it, expect } from 'vitest';
import { BOARD_FIELDS } from '../src/diff.js';
import { BOARD_FIELD_ALLOWLIST } from '../src/ops.js';
import { Model } from '../src/model.js';

/* Top-Level-Board-Keys, die bewusst NICHT per board.field diffbar/mutierbar
   sind:
   - id            Board-Identität (nie diffen)
   - nextDisplayId  projektor-verwaltet (ADR-0067 §1)
   - sprints        via card.* / swimlane.* Ops, nicht board.field */
const NON_DIFFABLE = new Set(['id', 'nextDisplayId', 'sprints', 'awaitingUser', 'termine']);
/* `termine` (Karte #743, ADR-0138 §4): je Termin termin.upsert/termin.delete,
   nie board.field (die ganze Liste je Op verlöre gleichzeitige Termine). Steht
   nicht in Model.createBoard — die Liste entsteht mit dem ersten Termin. */
/* `awaitingUser` (Karte #609, ADR-0130 §7): je Person eine board.awaiting-Op
   statt einer board.field-Op — der Diff behandelt es gesondert (diff.js), und
   board.field lehnt es ab. Mengengleichheit BOARD_FIELDS ↔ BOARD_FIELD_ALLOWLIST
   bleibt davon unberührt. */

/* In BOARD_FIELDS, aber NICHT in Model.createBoard: additiv per Migration
   ergänzte Board-Felder. `identities` kommt aus migrateIdentities
   (src/storage.js, Karte #123) — legitimes diffbares Feld, nur nicht im
   Frisch-Board-Skelett. Neue Migrations-Felder hier bewusst eintragen. */
const MIGRATION_ADDED = new Set(['identities']);

describe('F-290 Board-Feldlisten-Drift', () => {
  const modelKeys = Object.keys(Model.createBoard('Drift-Test'));

  it('BOARD_FIELDS (diff.js) und BOARD_FIELD_ALLOWLIST (ops.js) sind mengengleich', () => {
    expect([...BOARD_FIELDS].sort()).toEqual([...BOARD_FIELD_ALLOWLIST].sort());
  });

  it('jeder Model.createBoard-Key ist diffbar ODER bewusst ausgeschlossen', () => {
    const diffable = new Set(BOARD_FIELDS);
    const fehlend = modelKeys.filter(k => !diffable.has(k) && !NON_DIFFABLE.has(k));
    expect(fehlend, `Neue Board-Top-Level-Felder ohne Diff-Abdeckung: ${fehlend.join(', ')} — `
      + 'BOARD_FIELDS (diff.js) UND BOARD_FIELD_ALLOWLIST (ops.js) ergänzen, sonst '
      + 'verliert der Browser-Persist Änderungen daran lautlos (F-290).').toEqual([]);
  });

  it('jeder BOARD_FIELDS-Eintrag ist ein echtes Board-Feld (kein toter/vertippter Eintrag)', () => {
    const known = new Set([...modelKeys, ...MIGRATION_ADDED]);
    const fremd = BOARD_FIELDS.filter(f => !known.has(f));
    expect(fremd, `BOARD_FIELDS enthält Felder, die weder Model.createBoard noch eine `
      + `bekannte Migration kennt: ${fremd.join(', ')} — Tippfehler/Altlast, oder `
      + `MIGRATION_ADDED ergänzen, falls neu per Migration eingeführt.`).toEqual([]);
  });

  it('NON_DIFFABLE-Keys sind nicht versehentlich diffbar', () => {
    const leaked = [...NON_DIFFABLE].filter(k => BOARD_FIELDS.includes(k));
    expect(leaked, `Projektor-verwaltete/Identitäts-Keys dürfen nicht per board.field `
      + `gehen: ${leaked.join(', ')}`).toEqual([]);
  });
});
