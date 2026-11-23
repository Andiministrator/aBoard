/**
 * rest-ops.js — Op-Schicht des Browsers im Server-Modus (Karte #564,
 * ADR-0129 §4, Nachtrag Sprint 72, Punkte 1 und 5).
 *
 * Das Modul hat keine Build-Konstanten und läuft deshalb in allen vier
 * Kontexten gleich. Ob es im Server-Frontend-BÜNDEL ankommt, prüft
 * tests/build-invarianten.test.js (#564) und die Artefakt-Messung im Commit.
 */
import { describe, it, expect } from 'vitest';
import {
  teileInBatches, fuehreBezugNach, bereiteOpsVor, MAX_OPS_JE_BATCH, MAX_BATCH_BYTES,
} from '../src/rest-ops.js';
import { fold } from '../src/ops.js';

const op = (i, extra = '') => ({
  id: `op-${String(i).padStart(4, '0')}`, ts: '2026-09-29T00:00:00.000Z', seq: i, actor: 't',
  schemaVersion: 1, type: 'card.field', payload: { cardId: 'c1', field: 'description', value: `v${i}${extra}` },
});

describe('teileInBatches', () => {
  it('die Grenzen entsprechen dem Server (200 Ops, unter 1 MiB)', () => {
    expect(MAX_OPS_JE_BATCH).toBe(200);
    expect(MAX_BATCH_BYTES).toBeLessThan(1 << 20);
  });

  it('ein kleiner Diff bleibt ein Batch', () => {
    const ops = [op(0), op(1), op(2)];
    const { batches, zuGross } = teileInBatches(ops);
    expect(zuGross).toEqual([]);
    expect(batches).toEqual([ops]);
  });

  it('mehr als 200 Ops: geteilt, Reihenfolge erhalten, keine Op doppelt oder verloren', () => {
    /* Der reale Ausreisser aus der Messung #563 hatte 753 Ops. */
    const ops = Array.from({ length: 753 }, (_, i) => op(i));
    const { batches, zuGross } = teileInBatches(ops);
    expect(zuGross).toEqual([]);
    expect(batches.map(b => b.length)).toEqual([200, 200, 200, 153]);
    expect(batches.flat()).toEqual(ops);
  });

  it('die Byte-Grenze teilt, bevor die Op-Grenze greift', () => {
    const gross = 'x'.repeat(100 * 1024);
    const ops = Array.from({ length: 20 }, (_, i) => op(i, gross));
    const { batches } = teileInBatches(ops);
    expect(batches.length).toBeGreaterThan(1);
    for (const b of batches) {
      const koerper = JSON.stringify({ ops: b });
      expect(new TextEncoder().encode(koerper).length).toBeLessThan(1 << 20);
    }
    expect(batches.flat()).toEqual(ops);
  });

  it('eine einzelne Op über der Grenze: kein Batch, sondern Meldung (PUT-Rückfall)', () => {
    /* Kein Teil-Versand davor: sonst ginge dieselbe Änderung teils per Ops,
       teils per PUT hinaus. */
    const riesig = op(1, 'x'.repeat(MAX_BATCH_BYTES + 1));
    const { batches, zuGross } = teileInBatches([op(0), riesig, op(2)]);
    expect(batches).toEqual([]);
    expect(zuGross).toEqual([riesig]);
  });

  it('an der Grenze: dasselbe Maß wie beim Füllen (Op + Komma), kein Batch über maxBytes', () => {
    /* Session-Kritiker K-3: `> maxBytes` liess eine Op mit genau maxBytes allein
       in einen Batch, der dann mit Trennzeichen über der Grenze lag. */
    const o = op(0);
    const n = new TextEncoder().encode(JSON.stringify(o)).length;
    expect(teileInBatches([o], { maxBytes: n }).zuGross).toEqual([o]);
    expect(teileInBatches([o], { maxBytes: n + 1 }).batches).toEqual([[o]]);
  });

  it('leere oder fehlende Eingabe: keine Batches', () => {
    expect(teileInBatches([])).toEqual({ batches: [], zuGross: [] });
    expect(teileInBatches(undefined)).toEqual({ batches: [], zuGross: [] });
  });
});

const brett = () => ({
  version: '2',
  board: {
    id: 'b', title: 'B', nextDisplayId: 2,
    columnDefs: [{ id: 'col-backlog' }, { id: 'col-ready' }],
    sprints: [{ id: 's1', title: 'S', kind: 'sprint', cards: [
      { id: 'c1', displayId: 1, title: 'alt', description: '', columnId: 'col-backlog', comments: [] },
    ] }],
  },
});

describe('fuehreBezugNach', () => {
  it('entscheidet nach Position im Batch, nicht nach id — wie der Server', () => {
    /* Zwei Ops auf dasselbe Feld mit gleichem ts und gleicher seq: `fold`
       ordnet dann nach id. Die ids sind hier gegen die Position sortiert. Ohne
       Umstempeln gewönne die erste Op (id "z…" sortiert hinter "a…"). */
    const erste  = { ...op(0), id: 'z-erste',  seq: 0, payload: { cardId: 'c1', field: 'title', value: 'erst' } };
    const zweite = { ...op(1), id: 'a-zweite', seq: 0, payload: { cardId: 'c1', field: 'title', value: 'dann' } };
    expect(fold(brett(), [erste, zweite]).board.sprints[0].cards[0].title).toBe('erst');
    expect(fuehreBezugNach(brett(), [erste, zweite]).board.sprints[0].cards[0].title).toBe('dann');
  });

  it('verändert weder den Bezug noch die Ops', () => {
    const bezug = brett();
    const vorher = JSON.stringify(bezug);
    const ops = [op(0)];
    const opsVorher = JSON.stringify(ops);
    fuehreBezugNach(bezug, ops);
    expect(JSON.stringify(bezug)).toBe(vorher);
    expect(JSON.stringify(ops)).toBe(opsVorher);
  });

  it('Batch für Batch nachgeführt ergibt denselben Bezug wie alles auf einmal', () => {
    /* Punkt 5: Nach jedem angenommenen Batch wird der Bezug nachgeführt. Das
       muss mit dem Falten des ganzen Diffs übereinstimmen, sonst rechnet der
       nächste Diff gegen einen falschen Bezug. */
    const bezug = brett();
    const lokal = brett();
    lokal.board.sprints[0].cards[0].title = 'neu';
    lokal.board.sprints[0].cards[0].description = 'Text';
    lokal.board.sprints[0].cards.push({ id: 'c2', title: 'Neue Karte', columnId: 'col-ready', comments: [] });
    const { ops } = bereiteOpsVor(bezug, lokal, 't');
    expect(ops.length).toBeGreaterThan(1);
    const { batches } = teileInBatches(ops, { maxOps: 1 });
    const schrittweise = batches.reduce((b, batch) => fuehreBezugNach(b, batch), bezug);
    expect(schrittweise).toEqual(fuehreBezugNach(bezug, ops));
    const karten = schrittweise.board.sprints[0].cards;
    expect(karten.find(c => c.id === 'c1')).toMatchObject({ title: 'neu', description: 'Text' });
    expect(karten.find(c => c.id === 'c2')).toMatchObject({ title: 'Neue Karte', columnId: 'col-ready' });
  });
});

describe('bereiteOpsVor', () => {
  it('ohne Unterschied: keine Ops, keine Batches', () => {
    expect(bereiteOpsVor(brett(), brett(), 't')).toEqual({ ops: [], batches: [], zuGross: [] });
  });
});
