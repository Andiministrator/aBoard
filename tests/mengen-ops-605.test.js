/* @vitest-environment happy-dom */

/**
 * Karte #605 / ADR-0130 §3 — Mengen-Ops für `assignees`, `labels`, `links`.
 *
 * Bis Sprint 76 gingen diese Listen als GANZE Liste per `card.field` hinaus.
 * Tragen sich zwei gleichzeitig ein, bleibt nur einer — Feld-LWW auf einer
 * Liste. Jetzt: `card.listAdd`/`card.listRemove` je Element, vertauschbar.
 * Der NEBENLÄUFIG-Test fällt mit der alten diff.js rot (Gegenprobe beim Bau).
 */

import { describe, it, expect } from 'vitest';
import { diffToOps } from '../src/diff.js';
import { fold, makeOp, regelVerwerfung, OP_TYPES, MENGEN_FELDER } from '../src/ops.js';

const brett = (karte = {}) => ({
  version: '2',
  board: {
    id: 'b', title: 'B', nextDisplayId: 5,
    columnDefs: [{ id: 'col-backlog', title: 'Backlog', color: null }],
    sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [
      { id: 'c1', displayId: 1, title: 'T', columnId: 'col-backlog', assignees: [], labels: ['alt'],
        links: [], comments: [], activity: [], ...karte },
    ] }],
  },
});
const klon = o => JSON.parse(JSON.stringify(o));
const k1 = s => s.board.sprints[0].cards[0];
const op = (typ, field, item) => makeOp(typ, { cardId: 'c1', field, item }, 'x');
const mitTs = (o, ts) => ({ ...o, ts });

describe('Fold von card.listAdd / card.listRemove', () => {
  it('hängt ein neues Element ans Ende, ein vorhandenes wird nicht doppelt', () => {
    const erg = fold(brett(), [op('card.listAdd', 'labels', 'neu'), op('card.listAdd', 'labels', 'alt')]);
    expect(k1(erg).labels).toEqual(['alt', 'neu']);
  });

  it('entfernt ein Element; ein fehlendes zu entfernen ist wirkungslos, kein Fehler', () => {
    const protokoll = [];
    const erg = fold(brett(), [op('card.listRemove', 'labels', 'alt'), op('card.listRemove', 'labels', 'nie')], protokoll);
    expect(k1(erg).labels).toEqual([]);
    expect(protokoll).toEqual([]);
  });

  it('links: Gleichheit über den Inhalt, Schlüsselreihenfolge egal', () => {
    const vorher = brett({ links: [{ type: 'related', targetId: 'c9' }] });
    const erg = fold(vorher, [op('card.listRemove', 'links', { targetId: 'c9', type: 'related' })]);
    expect(k1(erg).links).toEqual([]);
  });

  it('fehlende Liste wird angelegt', () => {
    const vorher = brett(); delete k1(vorher).assignees;
    expect(k1(fold(vorher, [op('card.listAdd', 'assignees', 'andi')])).assignees).toEqual(['andi']);
  });

  it('nur assignees, labels, links — sonst wirkungslos mit Grund', () => {
    expect([...MENGEN_FELDER].sort()).toEqual(['assignees', 'labels', 'links']);
    expect(regelVerwerfung(brett().board, op('card.listAdd', 'comments', { id: 'k' }))?.grund).toBe('kein-mengen-feld');
    expect(regelVerwerfung(brett().board, op('card.listAdd', 'links', 'c9'))?.grund).toBe('link-element-ungueltig');
    expect(OP_TYPES).toEqual(expect.arrayContaining(['card.listAdd', 'card.listRemove']));
  });
});

describe('diffToOps für Mengen-Listen', () => {
  it('hinzugefügt und entfernt → listRemove, listAdd; Round-Trip hält', () => {
    const prev = brett({ labels: ['a', 'b'] });
    const next = klon(prev); k1(next).labels = ['b', 'c'];
    const ops = diffToOps(prev, next, 'x');
    expect(ops.map(o => [o.type, o.payload.item])).toEqual([['card.listRemove', 'a'], ['card.listAdd', 'c']]);
    expect(fold(prev, ops)).toEqual(next);
  });

  it('umsortiert → wie bisher card.field mit der ganzen Liste (Reihenfolge bleibt erreichbar)', () => {
    const prev = brett({ labels: ['a', 'b'] });
    const next = klon(prev); k1(next).labels = ['b', 'a'];
    const ops = diffToOps(prev, next, 'x');
    expect(ops.map(o => o.type)).toEqual(['card.field']);
    expect(fold(prev, ops)).toEqual(next);
  });

  it('neues Element nicht am Ende → card.field (listAdd kann nur anhängen)', () => {
    const prev = brett({ labels: ['a', 'b'] });
    const next = klon(prev); k1(next).labels = ['a', 'x', 'b'];
    expect(diffToOps(prev, next, 'x').map(o => o.type)).toEqual(['card.field']);
  });

  it('Doppelte in einer Liste → card.field', () => {
    const prev = brett({ labels: ['a', 'a'] });
    const next = klon(prev); k1(next).labels = ['a', 'a', 'b'];
    expect(diffToOps(prev, next, 'x').map(o => o.type)).toEqual(['card.field']);
  });

  it('links mit Symmetrie: beide Karten je ein listAdd', () => {
    const prev = brett();
    prev.board.sprints[0].cards.push({ id: 'c2', displayId: 2, title: 'Z', columnId: 'col-backlog', links: [], labels: [], assignees: [], comments: [], activity: [] });
    const next = klon(prev);
    next.board.sprints[0].cards[0].links = [{ type: 'blocks', targetId: 'c2' }];
    next.board.sprints[0].cards[1].links = [{ type: 'blocked-by', targetId: 'c1' }];
    const ops = diffToOps(prev, next, 'x');
    expect(ops.map(o => [o.type, o.payload.cardId])).toEqual([['card.listAdd', 'c1'], ['card.listAdd', 'c2']]);
    expect(fold(prev, ops)).toEqual(next);
  });
});

describe('NEBENLÄUFIG: zwei tragen sich gleichzeitig ein (#605 DoD)', () => {
  const FRUEH = '2026-09-30T10:00:00.000Z', SPAET = '2026-09-30T10:00:01.000Z';
  it('A fügt „andi“, B „berta“ als Beteiligte hinzu → beide da, in beiden Reihenfolgen', () => {
    const basis = brett();
    const a = klon(basis); k1(a).assignees = ['andi'];
    const b = klon(basis); k1(b).assignees = ['berta'];
    const opsA = diffToOps(basis, a, 'A'), opsB = diffToOps(basis, b, 'B');
    for (const [tA, tB] of [[FRUEH, SPAET], [SPAET, FRUEH]]) {
      const erg = fold(basis, [...opsA.map(o => mitTs(o, tA)), ...opsB.map(o => mitTs(o, tB))]);
      expect([...k1(erg).assignees].sort()).toEqual(['andi', 'berta']);
    }
  });

  it('A entfernt „alt“, B fügt „neu“ hinzu → „alt“ weg, „neu“ da', () => {
    const basis = brett();
    const a = klon(basis); k1(a).labels = [];
    const b = klon(basis); k1(b).labels = ['alt', 'neu'];
    const erg = fold(basis, [...diffToOps(basis, a, 'A').map(o => mitTs(o, SPAET)),
                             ...diffToOps(basis, b, 'B').map(o => mitTs(o, FRUEH))]);
    expect(k1(erg).labels).toEqual(['neu']);
  });
});

function mulberry32(a) {
  return () => {
    a |= 0; a = a + 0x6D2B79F5 | 0;
    let t = Math.imul(a ^ a >>> 15, 1 | a);
    t = t + Math.imul(t ^ t >>> 7, 61 | t) ^ t;
    return ((t ^ t >>> 14) >>> 0) / 4294967296;
  };
}

describe('Zufallsprüfung: 2000 Listen-Szenarien', () => {
  it('fold(prev, diffToOps(prev, next)) ≡ next', () => {
    const pool = ['a', 'b', 'c', 'd', 'e', 'f'];
    for (let iter = 0; iter < 2000; iter++) {
      const rnd = mulberry32(0x605 ^ (iter * 2654435761));
      const zieh = () => pool.filter(() => rnd() < 0.4).sort(() => rnd() - 0.5);
      const prev = brett({ labels: zieh(), assignees: zieh() });
      const next = klon(prev);
      k1(next).labels = zieh();
      k1(next).assignees = rnd() < 0.5 ? zieh() : k1(next).assignees;
      const ops = diffToOps(prev, next, 'bf');
      if (JSON.stringify(fold(prev, ops)) !== JSON.stringify(next)) {
        throw new Error(`Round-Trip-Bruch iter=${iter}\nops=${JSON.stringify(ops.map(o => [o.type, o.payload]))}`);
      }
    }
  });
});
