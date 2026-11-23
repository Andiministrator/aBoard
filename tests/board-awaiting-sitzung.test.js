/* F-1336 (Karte #645, ADR-0130 §7 Nachtrag): Warte-Einträge JE SITZUNG.

   Zwei Chats derselben Person teilten sich bis Sprint 80 einen Eintrag: die
   zweite Frage wurde nicht gesetzt, ein Prompt in einer Sitzung löschte die
   Frage der anderen. Mit `session` in der Op liegt jeder Eintrag unter
   `<Person> · <Sitzung>` und nennt `person`/`session` selbst; Banner und
   Server ordnen über `eintrag.person` zu. Ohne `session` bleibt alles wie
   bisher — dafür stehen die Bestandstests in board-awaiting-609.test.js. */
import { describe, it, expect } from 'vitest';
import { fold, regelVerwerfung } from '../src/ops.js';
import { diffToOps } from '../src/diff.js';
import {
  awaitingAlsAbbildung, eigeneAwaitingSchluessel, awaitingSchluesselFuer,
  awaitingSitzungsSchluessel, awaitingPersonVon, istAwaitingEintrag, AWAITING_SITZUNGEN_MAX,
} from '../src/awaiting.js';

const E = (frage, wer = 'Claude Opus 5.5') => ({ question: frage, since: '2026-10-03T10:00:00.000Z', askedBy: wer });

function basis(awaitingUser = null, identities = []) {
  return {
    version: '2',
    board: {
      id: 'b1', title: 'B', nextDisplayId: 1, columnDefs: [], identities,
      sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] }],
      templates: [], background: null, attachments: [], awaitingUser,
    },
  };
}

let zaehler = 0;
function op(payload, ts) {
  zaehler += 1;
  return { id: `s${String(zaehler).padStart(4, '0')}`, ts, seq: 0, actor: 'test', schemaVersion: 1, type: 'board.awaiting', payload };
}
const K1 = awaitingSitzungsSchluessel('Andi', 'sitzung-1');
const K2 = awaitingSitzungsSchluessel('Andi', 'sitzung-2');
const S = (frage, session) => ({ ...E(frage), person: 'Andi', session });

describe('Fold: zwei Sitzungen derselben Person', () => {
  it('KERNTEST: beide Fragen stehen nebeneinander', () => {
    const proj = fold(basis(), [
      op({ person: 'Andi', session: 'sitzung-1', value: E('erste?') }, '2026-10-03T10:00:00.000Z'),
      op({ person: 'Andi', session: 'sitzung-2', value: E('zweite?') }, '2026-10-03T10:00:00.001Z'),
    ]);
    expect(proj.board.awaitingUser).toEqual({ [K1]: S('erste?', 'sitzung-1'), [K2]: S('zweite?', 'sitzung-2') });
  });

  it('KERNTEST: Löschen mit Sitzung trifft nur die eigene Sitzung', () => {
    const proj = fold(basis({ [K1]: S('erste?', 'sitzung-1'), [K2]: S('zweite?', 'sitzung-2') }), [
      op({ person: 'Andi', session: 'sitzung-1', value: null }, '2026-10-03T10:00:01.000Z'),
    ]);
    expect(proj.board.awaitingUser).toEqual({ [K2]: S('zweite?', 'sitzung-2') });
  });

  it('ein Eintrag ohne Sitzung (alter Hook, Variante A) bleibt daneben unberührt', () => {
    const proj = fold(basis({ Andi: E('manuell?') }), [
      op({ person: 'Andi', session: 'sitzung-1', value: E('erste?') }, '2026-10-03T10:00:00.000Z'),
    ]);
    expect(proj.board.awaitingUser).toEqual({ Andi: E('manuell?'), [K1]: S('erste?', 'sitzung-1') });
  });

  it('ohne session verhält sich die Op wie bisher (Schlüssel = Person, kein Stempel)', () => {
    const proj = fold(basis(), [op({ person: 'Andi', value: E('alt?') }, '2026-10-03T10:00:00.000Z')]);
    expect(proj.board.awaitingUser).toEqual({ Andi: E('alt?') });
  });
});

describe('Obergrenze und Kollision (Sprint-81-Kritiker A-3, A-4)', () => {
  it('A-3: höchstens AWAITING_SITZUNGEN_MAX Sitzungs-Einträge je Person — die ältesten fallen weg', () => {
    const ops = Array.from({ length: AWAITING_SITZUNGEN_MAX + 2 }, (_, i) =>
      op({ person: 'Andi', session: `s${String(i).padStart(2, '0')}`, value: { ...E(`F${i}?`), since: `2026-10-03T10:00:${String(i).padStart(2, '0')}.000Z` } },
        `2026-10-03T10:00:${String(i).padStart(2, '0')}.000Z`));
    const au = fold(basis({ Bea: E('fremd?'), Andi: E('ohne Sitzung?') }), ops).board.awaitingUser;
    const sitzungen = Object.keys(au).filter(k => au[k].session !== undefined);
    expect(sitzungen).toHaveLength(AWAITING_SITZUNGEN_MAX);
    expect(au[awaitingSitzungsSchluessel('Andi', 's00')]).toBeUndefined();
    expect(au[awaitingSitzungsSchluessel('Andi', 's01')]).toBeUndefined();
    expect(au[awaitingSitzungsSchluessel('Andi', 's11')]).toBeDefined();
    /* Einträge ohne Sitzung und anderer Personen zählen nicht mit. */
    expect(au.Bea).toEqual(E('fremd?'));
    expect(au.Andi).toEqual(E('ohne Sitzung?'));
  });

  it('V-5: ein neuer Eintrag mit älterem since (abweichende Uhr) bleibt trotz Obergrenze stehen', () => {
    const ops = Array.from({ length: AWAITING_SITZUNGEN_MAX }, (_, i) =>
      op({ person: 'Andi', session: `n${i}`, value: { ...E('neu?'), since: `2026-10-03T12:00:${String(i).padStart(2, '0')}.000Z` } }, `2026-10-03T12:00:${String(i).padStart(2, '0')}.000Z`));
    ops.push(op({ person: 'Andi', session: 'alt', value: { ...E('alte Uhr?'), since: '2026-10-03T08:00:00.000Z' } }, '2026-10-03T12:01:00.000Z'));
    const au = fold(basis(), ops).board.awaitingUser;
    expect(au[awaitingSitzungsSchluessel('Andi', 'alt')]?.question).toBe('alte Uhr?');
    expect(Object.keys(au)).toHaveLength(AWAITING_SITZUNGEN_MAX);
  });

  it('A-4: ein Personenname mit dem Sitzungs-Trenner wird verworfen (keine Kollision)', () => {
    const r = regelVerwerfung(basis(), op({ person: 'Andi · s1', value: E('x?') }, '2026-10-03T10:00:00.000Z'));
    expect(r?.grund).toBe('awaiting-person-ungueltig');
    const proj = fold(basis({ [K1]: S('echt?', 'sitzung-1') }), [op({ person: 'Andi · sitzung-1', value: null }, '2026-10-03T10:00:01.000Z')]);
    expect(proj.board.awaitingUser).toEqual({ [K1]: S('echt?', 'sitzung-1') });
  });
});

describe('Formprüfung', () => {
  it('ungültige session → Verwerfung awaiting-sitzung-ungueltig', () => {
    const r = regelVerwerfung(basis(), op({ person: 'Andi', session: 'mit leerzeichen', value: E('x?') }, '2026-10-03T10:00:00.000Z'));
    expect(r?.grund).toBe('awaiting-sitzung-ungueltig');
  });

  it('person/session im Eintrag selbst → abgelehnt (der Fold stempelt sie)', () => {
    const r = regelVerwerfung(basis(), op({ person: 'Andi', value: { ...E('x?'), person: 'Bea', session: 's' } }, '2026-10-03T10:00:00.000Z'));
    expect(r?.grund).toBe('awaiting-wert-ungueltig');
  });

  it('Gegenprobe: gültige session wird nicht verworfen', () => {
    expect(regelVerwerfung(basis(), op({ person: 'Andi', session: 'abc-123:x', value: E('x?') }, '2026-10-03T10:00:00.000Z'))).toBeNull();
  });
});

describe('Normalisierung: Schlüssel und Eintrag müssen zusammenpassen', () => {
  it('ein Sitzungs-Eintrag unter fremdem Schlüssel fällt weg', () => {
    const v = { Bea: S('untergeschoben?', 'sitzung-1'), [K1]: S('echt?', 'sitzung-1') };
    expect(Object.keys(awaitingAlsAbbildung(v, {}))).toEqual([K1]);
  });

  it('person ohne session (oder umgekehrt) ist kein gültiger Eintrag', () => {
    expect(istAwaitingEintrag({ ...E('x?'), person: 'Andi' })).toBe(false);
    expect(istAwaitingEintrag({ ...E('x?'), session: 's1' })).toBe(false);
    expect(istAwaitingEintrag(S('x?', 's1'))).toBe(true);
  });

  it('Prototyp-Schlüssel bleiben abgewehrt', () => {
    const v = JSON.parse('{"__proto__": {"question":"x","since":"t","askedBy":""}}');
    expect(awaitingAlsAbbildung(v, {})).toEqual({});
  });
});

describe('Zuordnung über die Person des Eintrags', () => {
  const zwei = [{ kind: 'user', label: 'Andi' }, { kind: 'user', label: 'Bea' }];
  const board = (au) => ({ awaitingUser: au, identities: zwei });

  it('eigeneAwaitingSchluessel liefert alle Sitzungs-Einträge der Person — nicht die anderer', () => {
    const b = board({ [K1]: S('a?', 'sitzung-1'), [K2]: S('b?', 'sitzung-2'), Bea: E('c?') });
    expect(eigeneAwaitingSchluessel(b, 'andi')).toEqual([K1, K2]);
    expect(eigeneAwaitingSchluessel(b, 'Bea')).toEqual(['Bea']);
  });

  it('am Server ebenso (Person = E-Mail, Gross-/Kleinschreibung egal)', () => {
    const k = awaitingSitzungsSchluessel('anna@firma.de', 's1');
    const b = board({ [k]: { ...E('a?'), person: 'anna@firma.de', session: 's1' } });
    expect(eigeneAwaitingSchluessel(b, ['Anna@Firma.DE'], { server: true })).toEqual([k]);
    expect(eigeneAwaitingSchluessel(b, ['bea@firma.de'], { server: true })).toEqual([]);
  });

  it('awaitingSchluesselFuer und awaitingPersonVon lesen die Person aus dem Eintrag', () => {
    const v = { [K1]: S('a?', 'sitzung-1') };
    expect(awaitingSchluesselFuer(v, 'Andi')).toBe(K1);
    expect(awaitingPersonVon(K1, v[K1])).toBe('Andi');
    expect(awaitingPersonVon('Bea', E('x?'))).toBe('Bea');
  });
});

describe('diffToOps: „Ich habe geantwortet“ löscht Sitzungs-Einträge mit { person, session }', () => {
  it('ein entfernter Sitzungs-Eintrag wird zu board.awaiting { person, session, value: null }', () => {
    const prev = basis({ [K1]: S('a?', 'sitzung-1'), Bea: E('b?') });
    const next = basis({ Bea: E('b?') });
    const ops = diffToOps(prev, next, 'Andi').filter(o => o.type === 'board.awaiting');
    expect(ops.map(o => o.payload)).toEqual([{ person: 'Andi', session: 'sitzung-1', value: null }]);
  });

  it('Rundlauf: diff → fold ergibt den Zielzustand', () => {
    const prev = basis({ [K1]: S('a?', 'sitzung-1'), [K2]: S('b?', 'sitzung-2') });
    const next = basis({ [K2]: S('b?', 'sitzung-2') });
    const ops = diffToOps(prev, next, 'Andi');
    expect(fold(prev, ops).board.awaitingUser).toEqual(next.board.awaitingUser);
  });
});
