/* @vitest-environment happy-dom */

/**
 * Karte #604 / ADR-0130 §2 — card.fieldIf: eine Feldänderung mit erwartetem
 * Vorwert. Ändern zwei Schreiber dasselbe Freitextfeld, gewann bis Sprint 76
 * still, wer zuletzt ankam (Feld-LWW, ADR-0074). Jetzt wirkt die zweite
 * Änderung nicht, und der Fold meldet `feld-geaendert` mit dem aktuellen Wert.
 */

import { describe, it, expect } from 'vitest';
import { diffToOps } from '../src/diff.js';
import { fold, makeOp, validateOp, inspectOpPayload, OP_TYPES, regelVerwerfung } from '../src/ops.js';
import { bereiteOpsVor, bedingungsloseFeldOps, BEDINGTE_FELDER } from '../src/rest-ops.js';

const brett = (karte = {}) => ({
  version: '2',
  board: {
    id: 'b', title: 'B', nextDisplayId: 5,
    columnDefs: [{ id: 'col-backlog', title: 'Backlog', color: null }],
    sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [
      { id: 'c1', displayId: 1, title: 'Titel', description: 'alt', columnId: 'col-backlog',
        qa: false, labels: ['a'], comments: [], activity: [], ...karte },
    ] }],
  },
});
const klon = o => JSON.parse(JSON.stringify(o));
const k1 = s => s.board.sprints[0].cards[0];
const ifOp = (field, value, expect, extra = {}) => makeOp('card.fieldIf', { cardId: 'c1', field, value, expect, ...extra }, 'x');

describe('Fold von card.fieldIf', () => {
  it('passt der Vorwert, wirkt die Op wie card.field', () => {
    const erg = fold(brett(), [ifOp('description', 'neu', 'alt')]);
    expect(k1(erg).description).toBe('neu');
  });

  it('weicht der Vorwert ab: nichts geändert, Protokoll feld-geaendert mit aktuellem Wert', () => {
    const protokoll = [];
    const vorher = brett({ description: 'inzwischen von B' });
    const op = ifOp('description', 'von A', 'alt');
    const erg = fold(vorher, [op], protokoll);
    expect(erg).toEqual(vorher);
    expect(protokoll).toEqual([{ art: 'feld-geaendert', op: op.id, typ: 'card.fieldIf',
      cardId: 'c1', feld: 'description', aktuell: 'inzwischen von B' }]);
  });

  it('Tiefenvergleich: Schlüsselreihenfolge egal, Listen in Reihenfolge', () => {
    const vorher = brett({ meta: { a: 1, b: 2 } });
    expect(k1(fold(vorher, [ifOp('meta', 'x', { b: 2, a: 1 })])).meta).toBe('x');
    const liste = brett({ labels: ['a', 'b'] });
    expect(k1(fold(liste, [ifOp('labels', ['c'], ['b', 'a'])])).labels).toEqual(['a', 'b']);
  });

  /* Sprint-77-Kritiker: bis dahin galt „ein fehlendes Feld ist nicht null“ —
     über JSON lässt sich `undefined` aber nicht erwarten, eine Altkarte ohne
     das Feld war für Agenten dauerhaft unerreichbar, und die Meldung trug kein
     `aktuell` (undefined fällt beim Serialisieren weg). Der Browser betrifft
     das nicht: diff.js erzeugt fieldIf nur, wenn das Feld im Bezug einen Wert
     hat. */
  it('`expect: null` erwartet null — ein fehlendes Feld gilt als null', () => {
    expect(k1(fold(brett({ dueDate: null }), [ifOp('dueDate', '2026-10-01', null)])).dueDate).toBe('2026-10-01');
    expect(k1(fold(brett(), [ifOp('dueDate', '2026-10-01', null)])).dueDate).toBe('2026-10-01');   /* Feld fehlt */
    const protokoll = [];
    fold(brett(), [ifOp('dueDate', '2026-10-01', 'irgendwas')], protokoll);
    expect(protokoll).toEqual([expect.objectContaining({ art: 'feld-geaendert', aktuell: null })]);
    expect(JSON.parse(JSON.stringify(protokoll[0]))).toHaveProperty('aktuell', null);
  });

  it('ohne `expect`: wirkungslos, Grund erwartung-fehlt', () => {
    const op = makeOp('card.fieldIf', { cardId: 'c1', field: 'title', value: 'neu' }, 'x');
    const protokoll = [];
    expect(k1(fold(brett(), [op], protokoll)).title).toBe('Titel');
    expect(protokoll[0]).toMatchObject({ art: 'wirkungslos', grund: 'erwartung-fehlt' });
  });

  it('dieselben Feldregeln wie card.field: gesperrtes Feld, Listenfeld ohne Liste', () => {
    expect(regelVerwerfung(brett().board, ifOp('id', 'x', 'c1'))?.grund).toBe('card-feld-gesperrt');
    expect(regelVerwerfung(brett().board, ifOp('labels', 'x', ['a']))?.grund).toBe('card-array-feld-kein-array');
  });

  it('fehlende Karte: no-op (Ziel prüft der Server gesondert)', () => {
    const op = makeOp('card.fieldIf', { cardId: 'fehlt', field: 'title', value: 'x', expect: 'y' }, 'x');
    expect(fold(brett(), [op])).toEqual(brett());
  });

  it('ist ein bekannter Op-Typ; expect ist bekannt, aber nicht Pflicht (null muss gehen)', () => {
    expect(OP_TYPES).toContain('card.fieldIf');
    expect(validateOp(ifOp('title', 'x', null))).toBe(true);
    expect(inspectOpPayload(ifOp('title', 'x', null))).toEqual({ missing: [], unknown: [] });
  });
});

describe('NEBENLÄUFIG: zwei Schreiber, dasselbe Freitextfeld (#604 DoD)', () => {
  const mitTs = (op, ts) => ({ ...op, ts });
  const FRUEH = '2026-09-30T10:00:00.000Z', SPAET = '2026-09-30T10:00:01.000Z';

  it('B schreibt zuerst, A kommt mit veraltetem Vorwert — A wird abgelehnt, B bleibt', () => {
    const basis = brett();
    const a = klon(basis); k1(a).description = 'von A';
    const opsA = diffToOps(basis, a, 'A', { bedingteFelder: BEDINGTE_FELDER });
    expect(opsA.map(o => o.type)).toEqual(['card.fieldIf']);
    const opB = makeOp('card.field', { cardId: 'c1', field: 'description', value: 'von B' }, 'B');
    const protokoll = [];
    const erg = fold(basis, [mitTs(opB, FRUEH), ...opsA.map(o => mitTs(o, SPAET))], protokoll);
    expect(k1(erg).description).toBe('von B');
    expect(protokoll).toEqual([expect.objectContaining({ art: 'feld-geaendert', aktuell: 'von B' })]);
  });

  it('Gegenrichtung: A zuerst — A wirkt, das spätere bedingungslose B gewinnt wie bisher', () => {
    const basis = brett();
    const a = klon(basis); k1(a).description = 'von A';
    const opsA = diffToOps(basis, a, 'A', { bedingteFelder: BEDINGTE_FELDER });
    const opB = makeOp('card.field', { cardId: 'c1', field: 'description', value: 'von B' }, 'B');
    const protokoll = [];
    const erg = fold(basis, [...opsA.map(o => mitTs(o, FRUEH)), mitTs(opB, SPAET)], protokoll);
    expect(k1(erg).description).toBe('von B');
    expect(protokoll).toEqual([]);
  });

  it('verschiedene Felder derselben Karte kollidieren weiter nicht (ADR-0074)', () => {
    const basis = brett();
    const a = klon(basis); k1(a).description = 'von A';
    const opsA = diffToOps(basis, a, 'A', { bedingteFelder: BEDINGTE_FELDER });
    const opB = makeOp('card.field', { cardId: 'c1', field: 'title', value: 'Titel von B' }, 'B');
    const erg = fold(basis, [mitTs(opB, FRUEH), ...opsA.map(o => mitTs(o, SPAET))]);
    expect(k1(erg)).toMatchObject({ description: 'von A', title: 'Titel von B' });
  });
});

describe('diffToOps mit bedingteFelder', () => {
  it('nur die genannten Felder gehen bedingt, mit dem Wert aus prev als expect', () => {
    const prev = brett();
    const next = klon(prev); k1(next).title = 'Neu'; k1(next).qa = true;
    const ops = diffToOps(prev, next, 'x', { bedingteFelder: ['title'] });
    expect(ops.map(o => [o.type, o.payload.field])).toEqual([['card.fieldIf', 'title'], ['card.field', 'qa']]);
    expect(ops[0].payload).toEqual({ cardId: 'c1', field: 'title', value: 'Neu', expect: 'Titel' });
    expect(fold(prev, ops)).toEqual(next);
  });

  it('neues Feld oder undefined in prev → card.field (expect wäre nicht übertragbar)', () => {
    const prev = brett();
    const next = klon(prev); k1(next).agentContext = 'neu';
    expect(diffToOps(prev, next, 'x', { bedingteFelder: ['agentContext'] }).map(o => o.type)).toEqual(['card.field']);
  });

  it('ohne Option unverändert card.field — der lokale Projektor-Weg bleibt, wie er war', () => {
    const prev = brett();
    const next = klon(prev); k1(next).title = 'Neu';
    expect(diffToOps(prev, next, 'x').map(o => o.type)).toEqual(['card.field']);
  });

  it('bereiteOpsVor (REST) nutzt Titel, Beschreibung, Agent-Kontext', () => {
    expect([...BEDINGTE_FELDER].sort()).toEqual(['agentContext', 'description', 'title']);
    const prev = brett();
    const next = klon(prev); k1(next).description = 'neu';
    expect(bereiteOpsVor(prev, next, '').ops.map(o => o.type)).toEqual(['card.fieldIf']);
  });

  it('bedingungsloseFeldOps: „Meine Fassung übernehmen“ als card.field', () => {
    const ops = bedingungsloseFeldOps([{ cardId: 'c1', feld: 'description', mein: 'meins' }, { cardId: null }], '');
    expect(ops.map(o => [o.type, o.payload])).toEqual([['card.field', { cardId: 'c1', field: 'description', value: 'meins' }]]);
  });
});
