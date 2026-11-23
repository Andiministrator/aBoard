/* @vitest-environment happy-dom */

/**
 * Nacharbeit zur Sitzungs-QA vom 2026-09-30 (Kritiker über Sprint 77,
 * Befunde K-1, K-2, K-5, K-6; Findings F-1247 ff.).
 */

import { describe, it, expect } from 'vitest';
import { eigeneFassungAlsText, kopiereEigeneFassung } from '../src/feldkonflikt.js';
import { stabil, fold, makeOp, regelVerwerfung } from '../src/ops.js';
import { findFutureKollisionen } from '../src/inbox.js';

describe('K-1: „Ihre behalten“ verliert die eigene Fassung nie', () => {
  it('meldet nur dann Erfolg, wenn das Kopieren sicher gelang', async () => {
    const geschrieben = [];
    expect(await kopiereEigeneFassung('meins', { writeText: async t => { geschrieben.push(t); } })).toBe(true);
    expect(geschrieben).toEqual(['meins']);
  });

  it('ohne Zwischenablage: false (bisher meldete der Banner trotzdem „kopiert“)', async () => {
    expect(await kopiereEigeneFassung('meins', undefined)).toBe(false);
    expect(await kopiereEigeneFassung('meins', {})).toBe(false);
  });

  it('Fehler beim Kopieren: false, keine Ausnahme', async () => {
    expect(await kopiereEigeneFassung('meins', { writeText: async () => { throw new Error('verweigert'); } })).toBe(false);
  });

  it('mehrere Felder als ein Text, Nicht-Text als JSON', () => {
    expect(eigeneFassungAlsText([{ mein: 'a' }, { mein: ['x'] }, {}])).toBe('a\n\n["x"]\n\n""');
  });
});

describe('K-2: stabil unterscheidet NaN von null', () => {
  it('NaN ≠ null, undefined ≠ null, Schlüsselreihenfolge egal', () => {
    expect(stabil(NaN)).not.toBe(stabil(null));
    expect(stabil(undefined)).not.toBe(stabil(null));
    expect(stabil({ a: 1, b: 2 })).toBe(stabil({ b: 2, a: 1 }));
  });
});

describe('K-6: links-Element mit Zusatzschlüsseln', () => {
  const brett = links => ({ version: '2', board: { id: 'b', title: 'B', nextDisplayId: 2,
    columnDefs: [{ id: 'col-backlog' }],
    sprints: [{ id: '__backlog__', title: 'B', kind: 'backlog', cards: [{ id: 'c1', title: 'T', columnId: 'col-backlog', links }] }] } });
  it('wird angenommen und über den ganzen Inhalt verglichen (so steht es jetzt auch in der Meldung)', () => {
    const op = makeOp('card.listAdd', { cardId: 'c1', field: 'links', item: { type: 'related', targetId: 'c2', note: 'x' } }, 'x');
    expect(regelVerwerfung(brett([]).board, op)).toBe(null);
    const erg = fold(brett([{ type: 'related', targetId: 'c2' }]), [op]);
    expect(erg.board.sprints[0].cards[0].links).toHaveLength(2);
  });
});

describe('K-5: Kollisionsregeln für comment.edit und Mengen-Ops (inbox.js)', () => {
  const now = Date.parse('2026-09-30T12:00:00.000Z');
  const zukunft = '2026-09-30T12:05:00.000Z', vergangen = '2026-09-30T11:55:00.000Z';
  const op = (id, ts, type, payload) => ({ id, ts, type, actor: 'x', payload });
  const edit = (id, ts, commentId) => op(id, ts, 'comment.edit', { cardId: 'k1', commentId, text: 't' });
  const kollidiert = (a, b) => findFutureKollisionen([a, b], now).length > 0;

  it('comment.edit gegen edit/delete/add derselben commentId kollidiert', () => {
    expect(kollidiert(edit('a', zukunft, 'c1'), edit('b', vergangen, 'c1'))).toBe(true);
    expect(kollidiert(edit('a', zukunft, 'c1'), op('b', vergangen, 'comment.delete', { cardId: 'k1', commentId: 'c1' }))).toBe(true);
    expect(kollidiert(edit('a', zukunft, 'c1'),
      op('b', vergangen, 'comment.add', { cardId: 'k1', comment: { id: 'c1', text: 't' } }))).toBe(true);
  });

  it('comment.edit verschiedener Kommentare kollidiert nicht; card.field comments mit jedem', () => {
    expect(kollidiert(edit('a', zukunft, 'c1'), edit('b', vergangen, 'c2'))).toBe(false);
    expect(kollidiert(edit('a', zukunft, 'c1'), op('b', vergangen, 'card.field', { cardId: 'k1', field: 'comments', value: [] }))).toBe(true);
  });

  it('Mengen-Ops: add/remove desselben Elements kollidiert, add/add nicht, verschiedene Elemente nicht, card.field auf die Liste schon', () => {
    const m = (id, ts, type, item) => op(id, ts, type, { cardId: 'k1', field: 'labels', item });
    expect(kollidiert(m('a', zukunft, 'card.listAdd', 'x'), m('b', vergangen, 'card.listRemove', 'x'))).toBe(true);
    expect(kollidiert(m('a', zukunft, 'card.listAdd', 'x'), m('b', vergangen, 'card.listAdd', 'x'))).toBe(false);
    expect(kollidiert(m('a', zukunft, 'card.listAdd', 'x'), m('b', vergangen, 'card.listAdd', 'y'))).toBe(false);
    expect(kollidiert(m('a', zukunft, 'card.listAdd', 'x'), op('b', vergangen, 'card.field', { cardId: 'k1', field: 'labels', value: [] }))).toBe(true);
  });
});
