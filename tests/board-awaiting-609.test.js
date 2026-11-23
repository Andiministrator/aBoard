/**
 * Karte #609 (ADR-0130 §7): `awaitingUser` je Person, geschrieben nur über die
 * Op `board.awaiting { person, value }`.
 *
 * Kerntest: Zwei Sitzungen setzen gleichzeitig je ihren Schlüssel — beide
 * Einträge bleiben. Mit dem alten Weg (`board.field` ersetzt die ganze
 * Abbildung) bliebe nur die spätere Frage (Kritiker K-1, dieselbe LWW-Falle wie
 * bei den Listenfeldern in §3). Die Gegenprobe steht im Commit-Text: der Fold
 * wurde einmal so verbogen, dass `board.awaiting` die ganze Abbildung ersetzt —
 * die Kerntests wurden rot.
 */

import { describe, it, expect } from 'vitest';
import {
  fold, validateOp, makeOp, OP_TYPES, OP_PAYLOAD_SPEC, BOARD_FIELD_ALLOWLIST,
  regelVerwerfung, inspectOpPayload,
} from '../src/ops.js';
import { diffToOps } from '../src/diff.js';
import {
  awaitingAlsAbbildung, awaitingPersonGueltig, awaitingSchluesselFuer, standardPerson,
} from '../src/awaiting.js';

const E = (frage, wer = 'Claude Opus 5') => ({ question: frage, since: '2026-09-30T10:00:00.000Z', askedBy: wer });

function basis(awaitingUser = null, extra = {}) {
  return {
    version: '2',
    board: {
      id: 'b1', title: 'B', nextDisplayId: 1, columnDefs: [],
      sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] }],
      templates: [], background: null, attachments: [], awaitingUser, ...extra,
    },
  };
}

let zaehler = 0;
function op(type, payload, ts, actor = 'test') {
  zaehler += 1;
  return { id: `o${String(zaehler).padStart(4, '0')}`, ts, seq: 0, actor, schemaVersion: 1, type, payload };
}
const setze = (person, e, ts) => op('board.awaiting', { person, value: e }, ts);
const loesche = (person, ts) => op('board.awaiting', { person, value: null }, ts);

describe('board.awaiting — Fold', () => {
  it('ist ein Op-Typ und im Payload-Spec mit person als Pflichtfeld', () => {
    expect(OP_TYPES).toContain('board.awaiting');
    expect(OP_PAYLOAD_SPEC['board.awaiting']).toEqual({ required: ['person'], known: ['person', 'value', 'session'] });
  });

  it('KERNTEST: zwei Personen fragen gleichzeitig → beide Einträge bleiben', () => {
    const proj = fold(basis(), [
      setze('Andi', E('Frage an Andi?'), '2026-09-30T10:00:00.000Z'),
      setze('Bea', E('Frage an Bea?'), '2026-09-30T10:00:00.001Z'),
    ]);
    expect(proj.board.awaitingUser).toEqual({ Andi: E('Frage an Andi?'), Bea: E('Frage an Bea?') });
  });

  it('KERNTEST: dieselbe Reihenfolge umgekehrt geliefert → gleiches Ergebnis (Totalordnung)', () => {
    const ops = [
      setze('Andi', E('A?'), '2026-09-30T10:00:00.000Z'),
      setze('Bea', E('B?'), '2026-09-30T10:00:00.001Z'),
    ];
    expect(JSON.stringify(fold(basis(), [...ops].reverse()).board.awaitingUser))
      .toBe(JSON.stringify(fold(basis(), ops).board.awaitingUser));
  });

  it('dieselbe Person: die spätere Frage ersetzt die frühere (LWW je Schlüssel)', () => {
    const proj = fold(basis(), [
      setze('Andi', E('erste?'), '2026-09-30T10:00:00.000Z'),
      setze('Andi', E('zweite?'), '2026-09-30T10:00:01.000Z'),
    ]);
    expect(proj.board.awaitingUser).toEqual({ Andi: E('zweite?') });
  });

  it('value: null löscht nur den eigenen Schlüssel', () => {
    const proj = fold(basis({ Andi: E('A?'), Bea: E('B?') }), [loesche('Andi', '2026-09-30T10:00:00.000Z')]);
    expect(proj.board.awaitingUser).toEqual({ Bea: E('B?') });
  });

  it('der letzte gelöschte Eintrag → null (nicht {}), wie im Board-Skelett', () => {
    const proj = fold(basis({ Andi: E('A?') }), [loesche('Andi', '2026-09-30T10:00:00.000Z')]);
    expect(proj.board.awaitingUser).toBeNull();
  });

  it('Löschen eines nicht vorhandenen Schlüssels ist ein No-op', () => {
    const proj = fold(basis({ Bea: E('B?') }), [loesche('Andi', '2026-09-30T10:00:00.000Z')]);
    expect(proj.board.awaitingUser).toEqual({ Bea: E('B?') });
  });

  it('eine alte Einzelform im Bestand wird zum Eintrag der Standardperson, nicht überschrieben', () => {
    const alt = E('alte Frage?');
    const proj = fold(basis(alt), [setze('Bea', E('B?'), '2026-09-30T10:00:00.000Z')]);
    expect(proj.board.awaitingUser).toEqual({ User: alt, Bea: E('B?') });
  });

  it('Standardperson = erste Nutzer-Identität des Boards', () => {
    const ids = [{ id: 'c', kind: 'claude-opus', label: 'Claude Opus' }, { id: 'u', kind: 'user', label: 'Andi' }];
    const proj = fold(basis(E('alt?'), { identities: ids }), [setze('Bea', E('B?'), '2026-09-30T10:00:00.000Z')]);
    expect(Object.keys(proj.board.awaitingUser).sort()).toEqual(['Andi', 'Bea']);
  });

  it('der Fold verändert seinen Eingang nicht und bleibt rein (zweimal = gleich)', () => {
    const b = basis({ Andi: E('A?') });
    const vorher = JSON.stringify(b);
    const ops = [setze('Bea', E('B?'), '2026-09-30T10:00:00.000Z')];
    const p1 = fold(b, ops);
    expect(JSON.stringify(b)).toBe(vorher);
    expect(JSON.stringify(fold(b, ops))).toBe(JSON.stringify(p1));
  });
});

describe('board.awaiting — Formprüfung (regelVerwerfung)', () => {
  const pruefe = (payload) => regelVerwerfung(basis(), { type: 'board.awaiting', payload });

  it('Prototyp-Schlüssel als Person werden abgelehnt und ändern nichts', () => {
    for (const person of ['__proto__', 'constructor', 'prototype']) {
      expect(pruefe({ person, value: E('x?') })?.grund).toBe('awaiting-person-ungueltig');
      const proj = fold(basis(), [setze(person, E('x?'), '2026-09-30T10:00:00.000Z')]);
      expect(proj.board.awaitingUser).toBeNull();
      expect(({}).question).toBeUndefined();
    }
  });

  it('leere, nicht-Zeichenketten- und überlange Personen werden abgelehnt', () => {
    for (const person of ['', '   ', 5, null, {}, 'x'.repeat(201)]) {
      expect(pruefe({ person, value: E('x?') })?.grund, String(person)).toBe('awaiting-person-ungueltig');
    }
    expect(pruefe({ person: 'x'.repeat(200), value: E('x?') })).toBeNull();
  });

  it('fehlendes value → wirkungslos (kein stilles Löschen)', () => {
    expect(pruefe({ person: 'Andi' })?.grund).toBe('awaiting-wert-fehlt');
    const proj = fold(basis({ Andi: E('A?') }), [op('board.awaiting', { person: 'Andi' }, '2026-09-30T10:00:00.000Z')]);
    expect(proj.board.awaitingUser).toEqual({ Andi: E('A?') });
  });

  it('ungültiges value (kein Objekt, leere Frage, fehlendes since) → wirkungslos', () => {
    for (const value of ['text', 5, [], {}, { question: '', since: 's', askedBy: 'x' }, { question: 'q', askedBy: 'x' }]) {
      expect(pruefe({ person: 'Andi', value })?.grund, JSON.stringify(value)).toBe('awaiting-wert-ungueltig');
    }
  });

  it('das Fold-Protokoll meldet die Verwerfung mit Grund', () => {
    const protokoll = [];
    fold(basis(), [op('board.awaiting', { person: '__proto__', value: E('x?') }, '2026-09-30T10:00:00.000Z')], protokoll);
    expect(protokoll.some(m => m.art === 'wirkungslos' && m.grund === 'awaiting-person-ungueltig')).toBe(true);
  });

  it('validateOp: person ist Pflicht (Poison ohne), value darf fehlen (regelVerwerfung meldet es)', () => {
    const ohnePerson = makeOp('board.awaiting', { value: null }, 'test');
    expect(validateOp(ohnePerson)).toBe(false);
    expect(inspectOpPayload(ohnePerson).missing).toContain('person');
    expect(validateOp(makeOp('board.awaiting', { person: 'Andi', value: null }, 'test'))).toBe(true);
  });

  it('unbekannte Zusatzschlüssel werden gemeldet (Namensdreher: persons statt person)', () => {
    const r = inspectOpPayload(makeOp('board.awaiting', { person: 'Andi', value: null, persons: 'x' }, 'test'));
    expect(r.unknown.length).toBeGreaterThan(0);
  });
});

describe('board.field auf awaitingUser — abgelehnt (ADR-0130 §7)', () => {
  it('awaitingUser ist nicht mehr in BOARD_FIELD_ALLOWLIST', () => {
    expect(BOARD_FIELD_ALLOWLIST.has('awaitingUser')).toBe(false);
  });

  it('ein alter Hook mit board.field überschreibt die Abbildung NICHT und wird gemeldet', () => {
    const bestand = { Andi: E('A?'), Bea: E('B?') };
    const protokoll = [];
    const proj = fold(basis(bestand), [
      op('board.field', { field: 'awaitingUser', value: E('alt?') }, '2026-09-30T10:00:00.000Z'),
    ], protokoll);
    expect(proj.board.awaitingUser).toEqual(bestand);
    const m = protokoll.find(x => x.grund === 'board-feld-nicht-erlaubt');
    expect(m, 'Meldung fehlt').toBeDefined();
    expect(m.text).toContain('board.awaiting');
  });
});

describe('diffToOps — awaitingUser je Person', () => {
  const stand = (au) => ({ board: basis(au).board });

  it('nur der eigene Schlüssel geändert → genau EINE board.awaiting-Op, keine board.field-Op', () => {
    const prev = stand({ Bea: E('B?') });
    const next = stand({ Bea: E('B?'), Andi: E('A?') });
    const ops = diffToOps(prev, next, 'test');
    expect(ops.map(o => o.type)).toEqual(['board.awaiting']);
    expect(ops[0].payload).toEqual({ person: 'Andi', value: E('A?') });
  });

  it('Schlüssel entfernt → value: null; unverändertes → keine Op', () => {
    const ops = diffToOps(stand({ Andi: E('A?'), Bea: E('B?') }), stand({ Bea: E('B?') }), 'test');
    expect(ops.map(o => o.payload)).toEqual([{ person: 'Andi', value: null }]);
    expect(diffToOps(stand({ Andi: E('A?') }), stand({ Andi: E('A?') }), 'test')).toEqual([]);
  });

  it('alte Einzelform in prev, migrierte Abbildung in next → keine Phantom-Op', () => {
    expect(diffToOps(stand(E('alt?')), stand({ User: E('alt?') }), 'test')).toEqual([]);
  });

  it('KERNTEST Browser: zwei Browser diffen gegen denselben Stand, beide Ops gefaltet → beide Einträge', () => {
    const prev = stand(null);
    const opsA = diffToOps(prev, stand({ Andi: E('A?') }), 'A');
    const opsB = diffToOps(prev, stand({ Bea: E('B?') }), 'B');
    const proj = fold(basis(null), [...opsA, ...opsB]);
    expect(proj.board.awaitingUser).toEqual({ Andi: E('A?'), Bea: E('B?') });
  });

  it('KERNTEST Browser: „Ich habe geantwortet" (Löschen) lässt die Frage der anderen stehen', () => {
    const prev = stand({ Andi: E('A?'), Bea: E('B?') });
    const ops = diffToOps(prev, stand({ Bea: E('B?') }), 'A');
    const proj = fold(basis({ Andi: E('A?'), Bea: E('B?neu?') }), ops);
    expect(proj.board.awaitingUser).toEqual({ Bea: E('B?neu?') });
  });

  it('Round-Trip: fold(prev, diff(prev, next)) == next', () => {
    const prev = stand({ Andi: E('A?'), Bea: E('B?') });
    const next = stand({ Bea: E('B2?'), Cem: E('C?') });
    const proj = fold(basis(prev.board.awaitingUser), diffToOps(prev, next, 'test'));
    expect(proj.board.awaitingUser).toEqual(next.board.awaitingUser);
  });
});

describe('awaiting.js — Hilfen', () => {
  it('awaitingPersonGueltig', () => {
    expect(awaitingPersonGueltig('Andi')).toBe(true);
    expect(awaitingPersonGueltig('andi@example.org')).toBe(true);
    expect(awaitingPersonGueltig('')).toBe(false);
    expect(awaitingPersonGueltig('__proto__')).toBe(false);
  });

  it('awaitingAlsAbbildung: eigene __proto__-Schlüssel aus JSON.parse fallen weg', () => {
    const roh = JSON.parse('{"__proto__": {"question":"q","since":"s","askedBy":"x"}, "Andi": {"question":"q","since":"s","askedBy":"x"}}');
    expect(Object.keys(awaitingAlsAbbildung(roh, {}))).toEqual(['Andi']);
  });

  it('awaitingSchluesselFuer: Gross-/Kleinschreibung und Randleerraum egal; alte Einzelform → null', () => {
    expect(awaitingSchluesselFuer({ Andi: E('a?') }, ' andi ')).toBe('Andi');
    expect(awaitingSchluesselFuer({ Andi: E('a?') }, 'Bea')).toBeNull();
    expect(awaitingSchluesselFuer(E('alt?'), 'User')).toBeNull();
    expect(awaitingSchluesselFuer(null, 'Andi')).toBeNull();
  });

  it('standardPerson', () => {
    expect(standardPerson({})).toBe('User');
    expect(standardPerson({ identities: [{ kind: 'user', label: ' Andi ' }] })).toBe('Andi');
  });
});

/* Session-QA Sprint 78 (Kritiker O-2/O-3/O-4): welche Einträge sind „meine“? */
describe('eigeneAwaitingSchluessel', async () => {
  const { eigeneAwaitingSchluessel, istAwaitingEintrag, AWAITING_FRAGE_MAX } = await import('../src/awaiting.js');
  const e = { question: 'A oder B?', since: '2026-10-01T10:00:00.000Z', askedBy: 'Claude' };
  const zwei = [{ kind: 'user', label: 'Bert' }, { kind: 'user', label: 'Andi' }];

  it('Einzelplatz (eine Nutzer-Identität „User“, Browser „Andi“): jeder Eintrag ist meiner', () => {
    const board = { identities: [{ kind: 'user', label: 'User' }], awaitingUser: { User: e } };
    expect(eigeneAwaitingSchluessel(board, 'Andi')).toEqual(['User']);
  });
  it('O-4: nach Umbenennung der einzigen Identität bleibt der alte Eintrag meiner', () => {
    const board = { identities: [{ kind: 'user', label: 'Andi' }], awaitingUser: { User: e } };
    expect(eigeneAwaitingSchluessel(board, '')).toEqual(['User']);
  });
  it('O-3: dieselbe Person in zwei Schreibweisen — beide Schlüssel', () => {
    const board = { identities: zwei, awaitingUser: { Andi: e, andi: e, Bert: e } };
    expect(eigeneAwaitingSchluessel(board, 'ANDI')).toEqual(['Andi', 'andi']);
  });
  it('Server-Modus: nie Einzelplatz — Mitglieder teilen sich oft die eine Identität „User“', () => {
    const board = { identities: [{ kind: 'user', label: 'User' }], awaitingUser: { Bea: e, Andi: e } };
    expect(eigeneAwaitingSchluessel(board, 'Andi', { server: true })).toEqual(['Andi']);
    expect(eigeneAwaitingSchluessel(board, '', { server: true })).toEqual([]);
  });
  it('O-2: mehrere Personen, Browser ohne userName — keiner (nicht die Frage der ersten Identität)', () => {
    const board = { identities: zwei, awaitingUser: { Bert: e } };
    expect(eigeneAwaitingSchluessel(board, '')).toEqual([]);
    expect(eigeneAwaitingSchluessel(board, 'Andi')).toEqual([]);
  });
  it('alte Einzelform und kaputte Werte: keiner', () => {
    expect(eigeneAwaitingSchluessel({ identities: [], awaitingUser: e }, 'Andi')).toEqual([]);
    expect(eigeneAwaitingSchluessel({ identities: [], awaitingUser: null }, 'Andi')).toEqual([]);
  });
  it('O-7: Längengrenzen des Eintrags', () => {
    expect(istAwaitingEintrag({ ...e, question: 'x'.repeat(AWAITING_FRAGE_MAX) })).toBe(true);
    expect(istAwaitingEintrag({ ...e, question: 'x'.repeat(AWAITING_FRAGE_MAX + 1) })).toBe(false);
    expect(istAwaitingEintrag({ ...e, askedBy: 'x'.repeat(201) })).toBe(false);
    expect(istAwaitingEintrag({ ...e, since: 'x'.repeat(201) })).toBe(false);
  });
});
