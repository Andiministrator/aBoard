/* @vitest-environment happy-dom */

/**
 * Karte #676 / F-1547 — eine Fassung der Finding-Nummernvergabe.
 *
 * `nextFindingNr` liegt in `src/finding.js` (rein, ohne Importe) und wird vom
 * Fold (`src/ops.js`) und vom Anlegen im Browser (`src/main.js`,
 * `card:quick-add`) gemeinsam genutzt. Vorher stand in main.js eine zweite
 * Fassung, weil `ops.js` im slim Template fehlt — zwei Fassungen, die
 * auseinanderlaufen können (die main.js-Fassung nahm z. B. einen Zähler 5.5
 * hin). Die Datei hängt an keinem Build-Flag und läuft in allen Kontexten.
 */

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { fold, makeOp } from '../src/ops.js';
import { nextFindingNr, FINDING_KIND } from '../src/finding.js';

const quelle = rel => readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8');

const fkarte = (id, nr) => ({
  id, displayId: 2, title: 'Befund', columnId: 'col-backlog', kind: FINDING_KIND,
  finding: { nr, typ: 'K', prioritaet: 'P1', status: 'offen' },
});
const brett = (karten, extra = {}) => ({
  id: 'b', title: 'B', nextDisplayId: 10,
  columnDefs: [{ id: 'col-backlog', title: 'Backlog', color: null }],
  sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: karten }],
  ...extra,
});

/* Fälle: [Name, Karten, Board-Zusatz, erwartete Nummer] */
const FAELLE = [
  ['leeres Board', [], {}, 1],
  ['Zähler fehlt, Bestand bis 8', [fkarte('a', 8), fkarte('b', 2)], {}, 9],
  ['Zähler über Bestand (Lücke durch Löschen bleibt)', [fkarte('a', 3)], { nextFindingNr: 5 }, 5],
  ['Zähler hinkt hinterher', [fkarte('a', 7)], { nextFindingNr: 3 }, 8],
  ['Zähler keine ganze Zahl', [fkarte('a', 4)], { nextFindingNr: 5.5 }, 5],
  ['Zähler als Text', [fkarte('a', 4)], { nextFindingNr: '9' }, 5],
  ['Lane ohne cards-Feld', [], { sprints: [{ id: '__backlog__', title: 'B', kind: 'backlog' }] }, 1],
];

describe('nextFindingNr (src/finding.js)', () => {
  it.each(FAELLE)('%s', (_name, karten, extra, erwartet) => {
    const board = brett(karten, extra);
    expect(nextFindingNr(board)).toBe(erwartet);
    expect(board.nextFindingNr).toBe(erwartet + 1);
    expect(nextFindingNr(board)).toBe(erwartet + 1);
  });

  it.each(FAELLE)('Fold vergibt dieselbe Nummer: %s', (_name, karten, extra, erwartet) => {
    const neu = fkarte('neu', undefined);
    delete neu.finding.nr;
    const erg = fold({ version: '2', board: brett(karten, extra) },
      [makeOp('card.upsert', { card: neu }, 't')]);
    const karte = erg.board.sprints[0].cards.find(c => c.id === 'neu');
    expect(karte.finding.nr).toBe(erwartet);
    expect(erg.board.nextFindingNr).toBe(erwartet + 1);
  });

  it('finding.js bleibt rein: keine Importe', () => {
    expect(quelle('../src/finding.js')).not.toMatch(/^\s*import\s/m);
  });

  it('Fold und Browser-Anlegen nutzen dieselbe Fassung — keine zweite in ops.js oder main.js', () => {
    const ops = quelle('../src/ops.js');
    const main = quelle('../src/main.js');
    expect(ops).toMatch(/import\s*\{[^}]*\bnextFindingNr\b[^}]*\}\s*from\s*'\.\/finding\.js'/);
    expect(main).toMatch(/import\s*\{[^}]*\bnextFindingNr\b[^}]*\}\s*from\s*'\.\/finding\.js'/);
    expect(ops).not.toMatch(/function\s+nextFindingNr\b/);
    expect(main).not.toMatch(/function\s+nextFindingNr\b/);
    /* Der Zähler wird nur in finding.js weitergezählt. */
    expect(ops).not.toMatch(/nextFindingNr\s*(\+\+|\+=|=[^=])/);
    expect(main).not.toMatch(/nextFindingNr\s*(\+\+|\+=|=[^=])/);
  });
});
