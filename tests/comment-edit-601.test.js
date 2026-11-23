/* @vitest-environment happy-dom */

/**
 * Karte #601 / ADR-0130 §4 — Kommentar bearbeiten oder löschen, ohne die
 * ganze Karte zu ersetzen.
 *
 * Bis Sprint 76 fiel `diffToOps` für jede Kommentarliste, die nicht nur
 * angehängt hatte, auf einen Voll-`card.upsert` zurück. Der ersetzt die Karte
 * komplett — ein gleichzeitig (von einem Agenten oder zweiten Nutzer)
 * angehängter Kommentar derselben Karte war danach weg, ohne Meldung
 * (ADR-0129 §4 „Bekannte Grenze“). Jetzt: `comment.delete` je entfallenem,
 * `comment.edit` je geändertem, `comment.add` je neuem Kommentar.
 *
 * Der Kerntest ist „NEBENLÄUFIG“ unten: Er fällt mit der alten diff.js rot
 * (Gegenprobe im Commit dokumentiert), mit der neuen grün.
 */

import { describe, it, expect } from 'vitest';
import { diffToOps } from '../src/diff.js';
import { fold, makeOp, validateOp, inspectOpPayload, OP_TYPES } from '../src/ops.js';

const k = (id, text, extra = {}) => ({ id, author: 'andi', text, created: '2026-09-30', ...extra });

function brett(comments) {
  return {
    version: '2',
    board: {
      id: 'b', title: 'B', nextDisplayId: 5,
      columnDefs: [{ id: 'col-backlog', title: 'Backlog', color: null }],
      sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [
        { id: 'c1', displayId: 1, title: 'Eins', columnId: 'col-backlog', qa: false, comments, activity: [] },
      ] }],
    },
  };
}
const clone = (o) => JSON.parse(JSON.stringify(o));
const kommentare = (snap) => snap.board.sprints[0].cards[0].comments;

describe('diffToOps: Kommentare je Eintrag statt Volltext (ADR-0130 §4)', () => {
  it('bearbeiteter Kommentar → genau ein comment.edit, kein card.upsert', () => {
    const prev = brett([k('k1', 'alt'), k('k2', 'bleibt')]);
    const next = clone(prev); kommentare(next)[0].text = 'neu';
    const ops = diffToOps(prev, next, 'andi');
    expect(ops.map(o => o.type)).toEqual(['comment.edit']);
    expect(ops[0].payload).toEqual({ cardId: 'c1', commentId: 'k1', text: 'neu' });
    expect(fold(prev, ops)).toEqual(next);
  });

  it('gelöschter Kommentar → comment.delete', () => {
    const prev = brett([k('k1', 'eins'), k('k2', 'zwei'), k('k3', 'drei')]);
    const next = clone(prev); kommentare(next).splice(1, 1);
    const ops = diffToOps(prev, next, 'andi');
    expect(ops.map(o => o.type)).toEqual(['comment.delete']);
    expect(ops[0].payload).toEqual({ cardId: 'c1', commentId: 'k2' });
    expect(fold(prev, ops)).toEqual(next);
  });

  it('löschen, bearbeiten und anhängen zugleich → delete, edit, add; Round-Trip hält', () => {
    const prev = brett([k('k1', 'eins'), k('k2', 'zwei'), k('k3', 'drei')]);
    const next = clone(prev);
    kommentare(next).splice(0, 1);
    kommentare(next)[1].text = 'drei!';
    kommentare(next).push(k('k4', 'vier'));
    const ops = diffToOps(prev, next, 'andi');
    expect(ops.map(o => o.type)).toEqual(['comment.delete', 'comment.edit', 'comment.add']);
    expect(fold(prev, ops)).toEqual(next);
  });

  it('`edited` wird nur mitgesendet, wenn der Schreiber es gesetzt hat', () => {
    const prev = brett([k('k1', 'alt')]);
    const next = clone(prev);
    kommentare(next)[0].text = 'neu';
    kommentare(next)[0].edited = '2026-09-30T12:00:00.000Z';
    const ops = diffToOps(prev, next, 'andi');
    expect(ops[0].payload.edited).toBe('2026-09-30T12:00:00.000Z');
    expect(fold(prev, ops)).toEqual(next);
  });

  /* Gegenrichtung: wo der Volltext BLEIBEN muss (§20 — eine Gegenprobe je Grund). */
  it('umsortierte Kommentare → Volltext (keine Op ordnet um)', () => {
    const prev = brett([k('k1', 'eins'), k('k2', 'zwei')]);
    const next = clone(prev); kommentare(next).reverse();
    const ops = diffToOps(prev, next, 'andi');
    expect(ops.map(o => o.type)).toEqual(['card.upsert']);
    expect(fold(prev, ops)).toEqual(next);
  });

  it('Kommentar ohne id → Volltext (nicht adressierbar)', () => {
    const prev = brett([{ author: 'a', text: 'alt' }]);
    const next = clone(prev); kommentare(next)[0].text = 'neu';
    expect(diffToOps(prev, next, 'andi').map(o => o.type)).toEqual(['card.upsert']);
  });

  it('anderes Feld als text/edited geändert (author) → Volltext', () => {
    const prev = brett([k('k1', 'alt')]);
    const next = clone(prev); kommentare(next)[0].author = 'jemand';
    expect(diffToOps(prev, next, 'andi').map(o => o.type)).toEqual(['card.upsert']);
  });

  it('Kommentarfeld entfernt → Volltext (keine Op löscht ein Feld)', () => {
    const prev = brett([k('k1', 'alt', { edited: 'x' })]);
    const next = clone(prev); delete kommentare(next)[0].edited;
    const ops = diffToOps(prev, next, 'andi');
    expect(ops.map(o => o.type)).toEqual(['card.upsert']);
    expect(fold(prev, ops)).toEqual(next);
  });

  it('reines Anhängen bleibt beim alten Weg (auch mit Alt-Kommentaren ohne id)', () => {
    const prev = brett([{ author: 'a', text: 'alt' }]);
    const next = clone(prev); kommentare(next).push(k('k9', 'neu'));
    expect(diffToOps(prev, next, 'andi').map(o => o.type)).toEqual(['comment.add']);
  });
});

describe('NEBENLÄUFIG: Bearbeiten und fremdes Anhängen an derselben Karte (#601 DoD)', () => {
  /* fold ordnet nach ts→seq→id, NICHT nach Array-Reihenfolge. Die beiden
     Reihenfolgen müssen deshalb über die ts hergestellt werden — mit bloßem
     Umstellen im Array lief die Agenten-Op immer zuletzt, und der Test war
     auch mit der alten diff.js grün (Gegenprobe beim Bau, §12b). */
  const mitTs = (op, ts) => ({ ...op, ts });
  const FRUEH = '2026-09-30T10:00:00.000Z', SPAET = '2026-09-30T10:00:01.000Z';

  it('Nutzer bearbeitet k1, Agent hängt k2 an → beide erhalten, in beiden Reihenfolgen', () => {
    const prev = brett([k('k1', 'alt')]);
    const nutzer = clone(prev); kommentare(nutzer)[0].text = 'neu';
    const opsNutzer = diffToOps(prev, nutzer, 'andi');
    const opAgent = makeOp('comment.add', { cardId: 'c1', comment: k('k2', 'vom Agenten', { author: 'agent:x' }) }, 'agent:x');

    for (const [tsNutzer, tsAgent] of [[FRUEH, SPAET], [SPAET, FRUEH]]) {
      const reihe = [...opsNutzer.map(o => mitTs(o, tsNutzer)), mitTs(opAgent, tsAgent)];
      const ks = kommentare(fold(prev, reihe));
      expect(ks.map(c => c.id).sort()).toEqual(['k1', 'k2']);
      expect(ks.find(c => c.id === 'k1').text).toBe('neu');
    }
  });

  it('Nutzer löscht k1, Agent hängt k2 an → k2 bleibt', () => {
    const prev = brett([k('k1', 'weg')]);
    const nutzer = clone(prev); kommentare(nutzer).length = 0;
    const opsNutzer = diffToOps(prev, nutzer, 'andi');
    const opAgent = mitTs(makeOp('comment.add', { cardId: 'c1', comment: k('k2', 'vom Agenten') }, 'agent:x'), FRUEH);
    const ks = kommentare(fold(prev, [opAgent, ...opsNutzer.map(o => mitTs(o, SPAET))]));
    expect(ks.map(c => c.id)).toEqual(['k2']);
  });
});

describe('Fold und Vertrag von comment.edit', () => {
  it('ist ein bekannter Op-Typ mit Pflichtfeldern cardId, commentId, text', () => {
    expect(OP_TYPES).toContain('comment.edit');
    const ohneText = makeOp('comment.edit', { cardId: 'c1', commentId: 'k1' }, 'x');
    expect(inspectOpPayload(ohneText).missing).toEqual(['text']);
    const voll = makeOp('comment.edit', { cardId: 'c1', commentId: 'k1', text: 't' }, 'x');
    expect(validateOp(voll)).toBe(true);
  });

  it('ändert nur text (und edited) — author, created und andere Kommentare bleiben', () => {
    const prev = brett([k('k1', 'alt', { author: 'agent:x' }), k('k2', 'anders')]);
    const erg = fold(prev, [makeOp('comment.edit', { cardId: 'c1', commentId: 'k1', text: 'neu' }, 'y')]);
    expect(kommentare(erg)).toEqual([k('k1', 'neu', { author: 'agent:x' }), k('k2', 'anders')]);
  });

  it('fehlender Kommentar oder fehlende Karte → no-op', () => {
    const prev = brett([k('k1', 'alt')]);
    const ops = [
      makeOp('comment.edit', { cardId: 'c1', commentId: 'gibtsnicht', text: 'x' }, 'y'),
      makeOp('comment.edit', { cardId: 'c-fehlt', commentId: 'k1', text: 'x' }, 'y'),
    ];
    expect(fold(prev, ops)).toEqual(prev);
  });

  it('text, der keine Zeichenkette ist → no-op', () => {
    const prev = brett([k('k1', 'alt')]);
    expect(fold(prev, [makeOp('comment.edit', { cardId: 'c1', commentId: 'k1', text: 42 }, 'y')])).toEqual(prev);
  });
});

/* Zufallsprüfung wie in diff-card-upsert-huckepack-464.test.js: Round-Trip
   über gemischte Kommentar-Änderungen, und Volltext nur aus einem der drei
   zulässigen Gründe (umsortiert, Feld entfernt, anderes Feld geändert). */
function mulberry32(a) {
  return () => {
    a |= 0; a = a + 0x6D2B79F5 | 0;
    let t = Math.imul(a ^ a >>> 15, 1 | a);
    t = t + Math.imul(t ^ t >>> 7, 61 | t) ^ t;
    return ((t ^ t >>> 14) >>> 0) / 4294967296;
  };
}

describe('Zufallsprüfung: 2000 Kommentar-Szenarien', () => {
  it('fold(prev, diffToOps(prev, next)) ≡ next, Volltext nur mit Grund', () => {
    for (let iter = 0; iter < 2000; iter++) {
      const rnd = mulberry32(0x601 ^ (iter * 2654435761));
      const n = Math.floor(rnd() * 5);
      const prevK = Array.from({ length: n }, (_, i) => k('k' + i, 't' + i));
      const prev = brett(prevK);
      const next = clone(prev);
      let ks = kommentare(next);
      let umsortiert = false, feldWeg = false, fremdFeld = false;
      ks = ks.filter(() => rnd() > 0.25);
      for (const c of ks) {
        const r = rnd();
        if (r < 0.3) c.text = c.text + '*';
        else if (r < 0.35) { c.author = 'jemand'; fremdFeld = true; }
        else if (r < 0.4) { delete c.created; feldWeg = true; }
        else if (r < 0.45) c.edited = 'e' + iter;
      }
      if (ks.length > 1 && rnd() < 0.15) { ks.reverse(); umsortiert = true; }
      const neu = Math.floor(rnd() * 3);
      for (let i = 0; i < neu; i++) ks.push(k('n' + iter + '_' + i, 'neu'));
      next.board.sprints[0].cards[0].comments = ks;

      const ops = diffToOps(prev, next, 'bf');
      const proj = fold(prev, ops);
      if (JSON.stringify(proj) !== JSON.stringify(next)) {
        throw new Error(`Round-Trip-Bruch iter=${iter}\nops=${JSON.stringify(ops.map(o => [o.type, o.payload]))}`);
      }
      if (ops.some(o => o.type === 'card.upsert') && !(umsortiert || feldWeg || fremdFeld)) {
        throw new Error(`Volltext ohne Grund iter=${iter}\nprev=${JSON.stringify(prevK)}\nnext=${JSON.stringify(ks)}`);
      }
    }
  });
});

describe('comment.edit lässt den Antwortkommentar „extern“ unverändert (Karte #686, ADR-0134 §3.6)', () => {
  it('Fold ignoriert comment.edit auf kind extern, andere Kommentare bleiben bearbeitbar', () => {
    const prev = brett([k('k1', 'Antwort', { kind: 'extern', authorEmail: 'anna@x.de', schleuse: { auftragId: 'a1' } }), k('k2', 'alt')]);
    const ops = [
      makeOp('comment.edit', { cardId: 'c1', commentId: 'k1', text: 'gefälscht' }, 'anna@x.de'),
      makeOp('comment.edit', { cardId: 'c1', commentId: 'k2', text: 'neu' }, 'anna@x.de'),
    ];
    const erg = fold(prev, ops);
    expect(kommentare(erg)[0].text).toBe('Antwort');
    expect(kommentare(erg)[0].kind).toBe('extern');
    expect(kommentare(erg)[1].text).toBe('neu');
  });
});
