/* @vitest-environment happy-dom */

/**
 * Karte #618 / F-1257 — card.release mit erwartetem Halter.
 *
 * „Übernahme aufheben“ im Browser schickte `card.release {erzwingen:true}`
 * ohne Bedingung. Während das Modal offen ist, übernimmt der Browser keinen
 * Server-Stand; die Anzeige kann veraltet sein. Folge: Hatte inzwischen eine
 * andere Sitzung übernommen, hob der Mensch deren frische Übernahme auf, und
 * der Grund-Kommentar nannte den alten Halter.
 *
 * Jetzt trägt die Op den Halter, den das Modal zeigte (`expect`), und wirkt
 * nur, wenn genau er die Karte noch hält. Sonst `halter-geaendert` mit dem
 * aktuellen Halter (Server: 422, der Kommentar im Batch wirkt nicht).
 */

import { describe, it, expect } from 'vitest';
import {
  fold, makeOp, inspectOpPayload, regelVerwerfung, UEBERNAHME_ARTEN, uebernahmeMeldung, halterWieErwartet,
} from '../src/ops.js';

const T0 = '2026-10-01T10:00:00.000Z';
const plus = (ts, sek) => new Date(Date.parse(ts) + sek * 1000).toISOString();

const brett = () => ({
  version: '2',
  board: {
    id: 'b', title: 'B', nextDisplayId: 5,
    columnDefs: [
      { id: 'col-ready', title: 'Ready', color: null },
      { id: 'col-erledigt', title: 'Erledigt', color: null },
    ],
    sprints: [{ id: 's1', title: 'S', kind: 'sprint', cards: [
      { id: 'c1', displayId: 1, title: 'Titel', columnId: 'col-ready', comments: [] },
    ] }],
  },
});
const k1 = s => s.board.sprints[0].cards[0];
const op = (type, payload, actor, ts, seq = 0) => ({ ...makeOp(type, payload, actor), ts, seq });
const claim = (actor, ts, extra = {}) => op('card.claim', { cardId: 'c1', ...extra }, actor, ts);
const release = (actor, ts, extra = {}) => op('card.release', { cardId: 'c1', ...extra }, actor, ts);
/* Was das Modal aus dem angezeigten claim baut (src/modal.js). */
const gesehen = c => ({ by: c.by ?? null, session: c.session ?? null, since: c.since ?? null });

describe('card.release mit expect — Fold', () => {
  it('erwarteter Halter hält noch: aufgehoben (mit erzwingen, wie der Browser schickt)', () => {
    const vorher = fold(brett(), [claim('A', T0, { session: 's' })]);
    const protokoll = [];
    const erg = fold(vorher, [release('Andi', plus(T0, 5), { erzwingen: true, expect: gesehen(k1(vorher).claim) })], protokoll);
    expect('claim' in k1(erg)).toBe(false);
    expect(protokoll).toEqual([]);
  });

  it('Erneuerung durch denselben Halter ändert nichts an der Bedingung (since bleibt)', () => {
    const vorher = fold(brett(), [claim('A', T0, { session: 's' })]);
    const angezeigt = gesehen(k1(vorher).claim);
    const erneuert = fold(vorher, [claim('A', plus(T0, 60), { session: 's' })]);
    const erg = fold(erneuert, [release('Andi', plus(T0, 90), { erzwingen: true, expect: angezeigt })]);
    expect('claim' in k1(erg)).toBe(false);
  });

  it('F-1257: inzwischen hält ein anderer — nichts geändert, halter-geaendert nennt den aktuellen', () => {
    /* Modal zeigt A (Lease abgelaufen), B übernimmt ordnungsgemäß, dann klickt der Mensch. */
    const mitA = fold(brett(), [claim('A', T0, { session: 'a' })]);
    const angezeigt = gesehen(k1(mitA).claim);
    const mitB = fold(mitA, [claim('B', plus(T0, 9000), { session: 'b' })]);
    const protokoll = [];
    const erg = fold(mitB, [release('Andi', plus(T0, 9001), { erzwingen: true, expect: angezeigt })], protokoll);
    expect(erg).toEqual(mitB);
    expect(protokoll).toHaveLength(1);
    expect(protokoll[0]).toMatchObject({
      art: 'halter-geaendert', typ: 'card.release', cardId: 'c1',
      halter: 'B', session: 'b', since: plus(T0, 9000), until: k1(mitB).claim.until,
    });
  });

  it('derselbe Halter hat freigegeben und neu übernommen — neue Übernahme, nicht aufgehoben', () => {
    const erste = fold(brett(), [claim('A', T0, { session: 's' })]);
    const angezeigt = gesehen(k1(erste).claim);
    const zweite = fold(erste, [release('A', plus(T0, 10), { session: 's' }), claim('A', plus(T0, 20), { session: 's' })]);
    const protokoll = [];
    expect(fold(zweite, [release('Andi', plus(T0, 30), { erzwingen: true, expect: angezeigt })], protokoll)).toEqual(zweite);
    expect(protokoll.map(e => e.art)).toEqual(['halter-geaendert']);
  });

  it('Übernahme ist schon weg: keine Wirkung, halter-geaendert mit halter null (kein irreführender Kommentar)', () => {
    const vorher = fold(brett(), [claim('A', T0, { session: 's' })]);
    const angezeigt = gesehen(k1(vorher).claim);
    const frei = fold(vorher, [release('A', plus(T0, 5), { session: 's' })]);
    const protokoll = [];
    expect(fold(frei, [release('Andi', plus(T0, 6), { erzwingen: true, expect: angezeigt })], protokoll)).toEqual(frei);
    expect(protokoll).toHaveLength(1);
    expect(protokoll[0]).toMatchObject({ art: 'halter-geaendert', halter: null, session: null });
  });

  it('ohne expect bleibt alles wie in Sprint 77 (Wiederholung ohne Übernahme: no-op ohne Meldung)', () => {
    const protokoll = [];
    expect(fold(brett(), [release('A', T0)], protokoll)).toEqual(brett());
    expect(protokoll).toEqual([]);
    const vorher = fold(brett(), [claim('A', T0)]);
    expect('claim' in k1(fold(vorher, [release('Andi', plus(T0, 5), { erzwingen: true })]))).toBe(false);
  });

  it('expect ersetzt das Halter-Recht nicht: passender Halter, aber weder Halter noch erzwingen → nicht-halter', () => {
    const vorher = fold(brett(), [claim('A', T0, { session: 's' })]);
    const protokoll = [];
    expect(fold(vorher, [release('B', plus(T0, 5), { expect: gesehen(k1(vorher).claim) })], protokoll)).toEqual(vorher);
    expect(protokoll.map(e => e.art)).toEqual(['nicht-halter']);
  });

  it('ein Batch [release mit falschem expect, comment.add] — der Fold meldet genau die release-Op', () => {
    const mitB = fold(brett(), [claim('B', T0, { session: 'b' })]);
    const r = release('Andi', plus(T0, 5), { erzwingen: true, expect: { by: 'A', session: 'a', since: T0 } });
    const c = op('comment.add', { cardId: 'c1', comment: { id: 'k', author: 'Andi', text: 'Grund', created: '2026-10-01' } }, 'Andi', plus(T0, 5), 1);
    const protokoll = [];
    fold(mitB, [r, c], protokoll);
    /* Der Server lehnt den ganzen Batch ab, sobald das Protokoll nicht leer ist (boards_ops.go). */
    expect(protokoll.map(e => [e.art, e.op])).toEqual([['halter-geaendert', r.id]]);
  });
});

describe('halterWieErwartet', () => {
  it('vergleicht by, session, since; fehlend = null', () => {
    const c = { by: 'A', session: null, since: T0, until: plus(T0, 60) };
    expect(halterWieErwartet(c, { by: 'A', session: null, since: T0 })).toBe(true);
    expect(halterWieErwartet(c, { by: 'A', since: T0 })).toBe(true);
    expect(halterWieErwartet(c, { by: 'A', session: 'x', since: T0 })).toBe(false);
    expect(halterWieErwartet(c, { by: 'A', session: null, since: plus(T0, 1) })).toBe(false);
    expect(halterWieErwartet(undefined, { by: 'A' })).toBe(false);
  });
});

describe('Form, Vertrag, Meldung', () => {
  it('expect ist ein bekannter Schlüssel von card.release', () => {
    expect(inspectOpPayload(release('A', T0, { erzwingen: true, expect: { by: 'A', session: null, since: T0 } })))
      .toEqual({ missing: [], unknown: [] });
  });

  it('kaputtes expect ist wirkungslos mit Grund expect-ungueltig', () => {
    const b = brett().board;
    for (const kaputt of ['A', 42, ['A'], { by: 1 }, { since: {} }]) {
      expect(regelVerwerfung(b, release('A', T0, { expect: kaputt }))?.grund, JSON.stringify(kaputt)).toBe('expect-ungueltig');
    }
    expect(regelVerwerfung(b, release('A', T0, { expect: null }))).toBeNull();
    expect(regelVerwerfung(b, release('A', T0, { expect: { by: 'A', session: null, since: T0 } }))).toBeNull();
  });

  it('halter-geaendert ist eine Übernahme-Art mit eigenem Text — mit und ohne aktuellen Halter', () => {
    expect(UEBERNAHME_ARTEN).toContain('halter-geaendert');
    const mit = uebernahmeMeldung({ art: 'halter-geaendert', op: 'o1', typ: 'card.release', cardId: 'c1', halter: 'B', session: 'b', until: T0 });
    expect(mit).toMatch(/c1 hält inzwischen B \(Sitzung b\).*nichts aufgehoben/);
    const ohne = uebernahmeMeldung({ art: 'halter-geaendert', op: 'o1', typ: 'card.release', cardId: 'c1', halter: null });
    expect(ohne).toContain('nicht mehr übernommen');
    expect(ohne).not.toContain('Systemuhr');
  });
});

describe('halterEinwand — Vorprüfung des lokalen Wegs', () => {
  it('null, solange der erwartete Halter hält oder die Karte fehlt; sonst der aktuelle Halter', async () => {
    const { halterEinwand } = await import('../src/ops.js');
    const mitA = fold(brett(), [claim('A', T0, { session: 'a' })]);
    const angezeigt = gesehen(k1(mitA).claim);
    expect(halterEinwand(mitA, 'c1', angezeigt)).toBeNull();
    expect(halterEinwand(mitA, 'fehlt', angezeigt)).toBeNull();
    const mitB = fold(mitA, [claim('B', plus(T0, 9000), { session: 'b' })]);
    expect(halterEinwand(mitB, 'c1', angezeigt)).toEqual({ halterAktuell: k1(mitB).claim });
    const frei = fold(mitB, [release('B', plus(T0, 9001), { session: 'b' })]);
    expect(halterEinwand(frei, 'c1', angezeigt)).toEqual({ halterAktuell: null });
  });
});
