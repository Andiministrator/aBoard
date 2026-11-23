/* @vitest-environment happy-dom */

/**
 * Tests für den State-Diff → Ops-Übersetzer — ADR-0067 Stufe 1 (#228).
 *
 * Kern-Invariante (Browser-Korrektheit): für jede Mutation gilt
 *   fold(prev, diffToOps(prev, next))  ≡  next
 * (modulo projektor-vergebener displayId neuer Karten — der Projektor ist
 * dafür autoritativ, base.nextDisplayId == der vom App optimistisch genutzte
 * Wert, daher konvergent). Hält das, ist garantiert: was der User optimistisch
 * sieht, materialisiert die Projektion deckungsgleich — F-283-Wurzel
 * („Browser-Memory ≠ Disk") existiert strukturell nicht mehr.
 */

import { describe, it, expect } from 'vitest';
import { diffToOps } from '../src/diff.js';
import { fold } from '../src/ops.js';

function snap() {
  return {
    version: '2',
    board: {
      id: 'b', title: 'Board', nextDisplayId: 10,
      columnDefs: [
        { id: 'col-backlog', title: 'Backlog', color: null },
        { id: 'col-ready', title: 'Ready', color: null },
        { id: 'col-erledigt', title: 'Erledigt', color: null },
      ],
      sprints: [
        { id: 's1', title: 'Sprint 1', kind: 'sprint', cards: [
          { id: 'c1', displayId: 1, title: 'Eins', columnId: 'col-ready', qa: false, comments: [], activity: [] },
          { id: 'c2', displayId: 2, title: 'Zwei', columnId: 'col-ready', qa: false, comments: [], activity: [] },
        ] },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [
          { id: 'c3', displayId: 3, title: 'Drei', columnId: 'col-backlog', qa: false, comments: [], activity: [] },
        ] },
      ],
      awaitingUser: null, templates: [], background: null,
    },
  };
}

const clone = (o) => JSON.parse(JSON.stringify(o));

/* Kanonisierung: Die einzig SEMANTISCH bedeutsame Karten-Reihenfolge ist
   die INNERHALB einer (Sprint, Spalte) — der Renderer (und main.js
   card:moved, targetIndex „relativ zu Karten gleicher columnId") gruppiert
   per columnId; die spaltenübergreifende Array-Interleaving-Reihenfolge im
   sprint.cards-Array ist ein Implementierungsartefakt (auch in der Alt-App
   nicht kanonisch). Wir vergleichen daher Stände kanonisiert: jede
   sprint.cards-Liste wird stabil nach columnId-Block umsortiert, ohne die
   Reihenfolge innerhalb einer Spalte zu verändern. So testen wir die
   korrekte Invariante statt eines belanglosen Array-Index. */
function canonical(state) {
  const s = clone(state);
  for (const sprint of s.board.sprints || []) {
    const byCol = new Map();
    for (const c of sprint.cards) {
      if (!byCol.has(c.columnId)) byCol.set(c.columnId, []);
      byCol.get(c.columnId).push(c);
    }
    sprint.cards = [...byCol.keys()].sort().flatMap(k => byCol.get(k));
  }
  return s;
}

/* Wendet den Diff an und prüft die Round-Trip-Invariante (kanonisiert). */
function roundTrip(prev, mutate) {
  const next = clone(prev);
  mutate(next.board);
  const ops = diffToOps(prev, next, 'tester');
  const proj = fold(prev, ops);
  return { next, ops, proj };
}

describe('diffToOps — Round-Trip-Invariante fold(prev, diff(prev,next)) ≡ next', () => {
  it('keine Änderung → keine Ops, Projektion == prev', () => {
    const prev = snap();
    const ops = diffToOps(prev, clone(prev), 'tester');
    expect(ops).toEqual([]);
    expect(fold(prev, ops)).toEqual(prev);
  });

  it('Karten-Feld geändert (qa toggeln) → feingranulare card.field (ADR-0074)', () => {
    const prev = snap();
    const { next, ops, proj } = roundTrip(prev, b => {
      b.sprints[0].cards[0].qa = true;
    });
    expect(ops.some(o => o.type === 'card.upsert')).toBe(false);
    expect(ops.filter(o => o.type === 'card.field')).toEqual([
      expect.objectContaining({ type: 'card.field',
        payload: { cardId: 'c1', field: 'qa', value: true } }),
    ]);
    expect(canonical(proj)).toEqual(canonical(next));
  });

  it('Karte zwischen Spalten verschoben (col-ready → col-erledigt)', () => {
    const prev = snap();
    const { next, ops, proj } = roundTrip(prev, b => {
      b.sprints[0].cards[0].columnId = 'col-erledigt';
    });
    expect(ops.some(o => o.type === 'card.move')).toBe(true);
    expect(canonical(proj)).toEqual(canonical(next));
  });

  it('Karte zwischen Sprints verschoben (s1 → __backlog__)', () => {
    const prev = snap();
    const { next, proj } = roundTrip(prev, b => {
      const [card] = b.sprints[0].cards.splice(0, 1);
      card.columnId = 'col-backlog';
      b.sprints[1].cards.push(card);
    });
    expect(canonical(proj)).toEqual(canonical(next));
  });

  it('Karte gelöscht', () => {
    const prev = snap();
    const { next, ops, proj } = roundTrip(prev, b => {
      b.sprints[0].cards.splice(1, 1); /* c2 weg */
    });
    expect(ops.some(o => o.type === 'card.delete')).toBe(true);
    expect(canonical(proj)).toEqual(canonical(next));
  });

  it('neue Karte: Projektor vergibt displayId == optimistisch genutzter Wert (konvergent)', () => {
    const prev = snap();
    const { next, ops, proj } = roundTrip(prev, b => {
      /* App-Verhalten: Model.assignDisplayId nutzt+inkrementiert nextDisplayId */
      const did = b.nextDisplayId;       /* 10 */
      b.nextDisplayId += 1;              /* → 11 */
      b.sprints[0].cards.push({ id: 'cNEW', displayId: did, title: 'Neu', columnId: 'col-ready', qa: false, comments: [], activity: [] });
    });
    expect(ops.some(o => o.type === 'card.upsert')).toBe(true);
    /* nextDisplayId steht NICHT in der board.field-Allowlist → kein Op dafür;
       der Projektor zählt selbst hoch. Round-Trip muss trotzdem exakt sein. */
    expect(canonical(proj)).toEqual(canonical(next));
    const created = proj.board.sprints[0].cards.find(c => c.id === 'cNEW');
    expect(created.displayId).toBe(10);
    expect(proj.board.nextDisplayId).toBe(11);
  });

  it('Board-Titel + columnDefs geändert', () => {
    const prev = snap();
    const { next, ops, proj } = roundTrip(prev, b => {
      b.title = 'Neuer Titel';
      b.columnDefs[1].color = '#abc';
    });
    expect(ops.filter(o => o.type === 'board.field').length).toBe(2);
    expect(canonical(proj)).toEqual(canonical(next));
  });

  it('Sprint umbenannt + neuer Sprint + Umordnung', () => {
    const prev = snap();
    const { next, proj } = roundTrip(prev, b => {
      b.sprints[0].title = 'Sprint 1 — neu';
      b.sprints.splice(1, 0, { id: 's2', title: 'Sprint 2', kind: 'sprint', cards: [] });
      /* Reihenfolge: s1, s2, __backlog__ → s2, s1, __backlog__ */
      const s = b.sprints.splice(0, 1)[0];
      b.sprints.splice(1, 0, s);
    });
    expect(canonical(proj)).toEqual(canonical(next));
  });

  it('Sprint gelöscht — Nicht-Meta-Karten landen im Backlog (App tut das vor)', () => {
    const prev = snap();
    const { next, proj } = roundTrip(prev, b => {
      /* App-Semantik (main.js sprint:delete): Karten in Backlog, Sprint weg */
      const dead = b.sprints[0];
      b.sprints[1].cards.push(...dead.cards.map(c => ({ ...c, columnId: 'col-backlog' })));
      b.sprints = b.sprints.filter(s => s.id !== 's1');
    });
    expect(canonical(proj)).toEqual(canonical(next));
  });

  it('Kommentar angehängt → comment.add statt Volltext (ADR-0074 Teil B)', () => {
    const prev = snap();
    const { next, ops, proj } = roundTrip(prev, b => {
      b.sprints[0].cards[0].comments.push({ id: 'k1', author: 'Claude', text: 'Hi', created: '2026-05-17' });
    });
    expect(ops.some(o => o.type === 'card.upsert')).toBe(false);
    expect(ops.filter(o => o.type === 'comment.add')).toEqual([
      expect.objectContaining({ type: 'comment.add', payload: {
        cardId: 'c1', comment: { id: 'k1', author: 'Claude', text: 'Hi', created: '2026-05-17' } } }),
    ]);
    expect(canonical(proj)).toEqual(canonical(next));
  });

  it('NEBENLÄUFIG: zwei Akteure ändern VERSCHIEDENE Felder derselben Karte → beide überleben (ADR-0074)', () => {
    const prev = snap();
    /* Akteur A: title; Akteur B (unabhängig von prev gedifft): qa */
    const a = clone(prev); a.board.sprints[0].cards[0].title = 'A-Titel';
    const b = clone(prev); b.board.sprints[0].cards[0].qa = true;
    const opsA = diffToOps(prev, a, 'A');
    const opsB = diffToOps(prev, b, 'B');
    /* Projektor faltet beide Op-Ströme (ts/seq/id-Totalordnung). */
    const proj = fold(prev, [...opsA, ...opsB]);
    const c1 = proj.board.sprints[0].cards[0];
    expect(c1.title).toBe('A-Titel');   /* Feld-LWW: A überlebt */
    expect(c1.qa).toBe(true);           /* Feld-LWW: B überlebt — kein Clobber */
  });

  it('FALLBACK: Schlüssel entfernt → Volltext-card.upsert, Invariante hält', () => {
    const prev = snap();
    const { next, ops, proj } = roundTrip(prev, b => {
      delete b.sprints[0].cards[0].activity;          /* Schlüsselmenge weicht ab */
      b.sprints[0].cards[0].title = 'Neu';
    });
    expect(ops.some(o => o.type === 'card.upsert')).toBe(true);
    expect(ops.some(o => o.type === 'card.field')).toBe(false);
    expect(canonical(proj)).toEqual(canonical(next));
  });

  it('kombinierte Mutation: Feld + Move + Delete + Neu gleichzeitig', () => {
    const prev = snap();
    const { next, proj } = roundTrip(prev, b => {
      b.sprints[0].cards[0].qa = true;                       /* Feld */
      b.sprints[0].cards[0].columnId = 'col-erledigt';       /* Move */
      b.sprints[1].cards.splice(0, 1);                        /* c3 löschen */
      const did = b.nextDisplayId; b.nextDisplayId += 1;
      b.sprints[0].cards.push({ id: 'cX', displayId: did, title: 'X', columnId: 'col-backlog', qa: false, comments: [], activity: [] });
    });
    expect(canonical(proj)).toEqual(canonical(next));
  });
});

/* ════════════════════════════════════════════════════════════════════════
   ADR-0079 — F-311 (Mehrfacheintritt in dieselbe (Sprint,Spalte)) +
   F-307 (Phantom-card.move). Akzeptanz-Gate: Brute-Force-Round-Trip +
   deterministisches Minimal-Gegenbeispiel. displayId ist projektor-
   autoritativ → für den Vergleich ausgeklammert.
   ════════════════════════════════════════════════════════════════════════ */

/* Vergleichssicht: pro Sprint je Spalte die geordnete Karten-id-Folge —
   genau die einzig semantisch bedeutsame Reihenfolge (Renderer gruppiert
   per columnId). board.nextDisplayId / displayId bewusst ignoriert. */
function colView(state) {
  const out = {};
  for (const s of [...(state.board.sprints || [])].sort((a, b) => a.id < b.id ? -1 : 1)) {
    const cols = {};
    for (const c of s.cards || []) (cols[c.columnId] ||= []).push(c.id);
    /* Spalten-Keys sortiert: die spaltenübergreifende Array-Interleaving-
       Reihenfolge ist ein Implementierungsartefakt (vgl. canonical() oben,
       Renderer gruppiert per columnId). Nur die Reihenfolge INNERHALB einer
       Spalte ist semantisch — genau die prüft F-311. */
    out[s.id] = Object.fromEntries(Object.keys(cols).sort().map(k => [k, cols[k]]));
  }
  return out;
}
const card = (id, columnId) => ({ id, displayId: null, title: id, columnId, qa: false, comments: [], activity: [] });

describe('ADR-0079 F-311 — deterministisches Minimal-Gegenbeispiel', () => {
  it('≥2 Eintritte in dieselbe (Sprint,Spalte): new + Cross-Col + Cross-Sprint', () => {
    /* Finding-Repro: prev s2/col-c=[card3]; __backlog__: card4@col-a, card7@col-c.
       Ziel s2/col-c = [card4, new8, card3, card7]. */
    const prev = {
      version: '2',
      board: {
        id: 'b', title: 'B', nextDisplayId: 8,
        columnDefs: [{ id: 'col-a' }, { id: 'col-c' }],
        sprints: [
          { id: 's2', title: 'S2', kind: 'sprint', cards: [card('card3', 'col-c')] },
          { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [
            card('card4', 'col-a'), card('card7', 'col-c'),
          ] },
        ],
        awaitingUser: null, templates: [], background: null,
      },
    };
    const next = clone(prev);
    next.board.sprints[1].cards = [];                 /* card4, card7 raus aus __backlog__ */
    next.board.sprints[0].cards = [
      { ...card('card4', 'col-c') },
      { ...card('new8', 'col-c') },
      { ...card('card3', 'col-c') },
      { ...card('card7', 'col-c') },
    ];
    const ops = diffToOps(prev, next, 'tester');
    const proj = fold(prev, ops);
    expect(colView(proj)).toEqual(colView(next));
    expect(colView(proj).s2['col-c']).toEqual(['card4', 'new8', 'card3', 'card7']);
  });
});

describe('ADR-0079 F-307 — kein Phantom-card.move für Geschwister', () => {
  it('eine Karte wechselt Spalte → KEIN card.move für die bleibende Geschwisterkarte', () => {
    const prev = snap();                              /* s1: c1,c2 @col-ready */
    const next = clone(prev);
    next.board.sprints[0].cards = [
      { ...next.board.sprints[0].cards.find(c => c.id === 'c2') },
      { ...next.board.sprints[0].cards.find(c => c.id === 'c1'), columnId: 'col-erledigt' },
    ];
    const ops = diffToOps(prev, next, 'tester');
    const moves = ops.filter(o => o.type === 'card.move').map(o => o.payload.cardId);
    expect(moves).toEqual(['c1']);                    /* nur c1, NICHT c2 */
    expect(colView(fold(prev, ops))).toEqual(colView(next));
  });
});

describe('ADR-0079 — Brute-Force-Round-Trip (Mehrfacheintritt-Klasse)', () => {
  /* deterministischer PRNG → reproduzierbare Fehlerfälle (kein Math.random) */
  function mulberry32(a) {
    return function () {
      a |= 0; a = (a + 0x6D2B79F5) | 0;
      let t = Math.imul(a ^ (a >>> 15), 1 | a);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }
  const SPR = ['s1', 's2', '__backlog__'];
  const COL = ['col-a', 'col-b', 'col-c'];

  function genPrev(rnd) {
    const sprints = SPR.map(id => ({
      id, title: id, kind: id === '__backlog__' ? 'backlog' : 'sprint', cards: [],
    }));
    const n = 5 + Math.floor(rnd() * 6);              /* 5–10 Karten */
    for (let i = 0; i < n; i++) {
      const s = sprints[Math.floor(rnd() * 3)];
      s.cards.push(card('c' + i, COL[Math.floor(rnd() * 3)]));
    }
    return { version: '2', board: {
      id: 'b', title: 'B', nextDisplayId: n + 1,
      columnDefs: COL.map(id => ({ id })), sprints,
      awaitingUser: null, templates: [], background: null,
    } };
  }

  /* Ziel: MEHRERE Karten (bestehende + neue, quer über Sprints/Spalten) in
     EINE (Sprint,Spalte) in zufälliger Reihenfolge zusammenführen — exakt
     die F-311-Klasse — plus etwas allgemeines Rauschen. */
  function genNext(prev, rnd, iter) {
    const next = clone(prev);

    /* F-316-Blindfleck schließen: in ~45 % der Iterationen reine
       Intra-Spalten-Umsortierung der VERBLEIBENDEN ko-residenten Karten
       (keine neuen/gelöschten/Cross-Änderungen) — genau die Klasse, die
       der frühere Dedup brach (Block-Rotation → Drift). */
    if (rnd() < 0.45) {
      const s = next.board.sprints[Math.floor(rnd() * 3)];
      const byCol = {};
      for (const c of s.cards) (byCol[c.columnId] ||= []).push(c);
      const cols = Object.keys(byCol).filter(k => byCol[k].length >= 2);
      if (cols.length) {
        const col = cols[Math.floor(rnd() * cols.length)];
        const arr = byCol[col];
        for (let i = arr.length - 1; i > 0; i--) {        /* Fisher-Yates */
          const j = Math.floor(rnd() * (i + 1)); [arr[i], arr[j]] = [arr[j], arr[i]];
        }
        let p = 0;
        s.cards = s.cards.map(c => c.columnId === col ? arr[p++] : c);
        return next;
      }
    }

    const all = [];
    for (const s of next.board.sprints) for (const c of s.cards) all.push(c.id);
    const tgtS = next.board.sprints[Math.floor(rnd() * 3)];
    const tgtC = COL[Math.floor(rnd() * 3)];
    /* k bestehende Karten einsammeln (aus beliebigen Sprints/Spalten) */
    const k = Math.min(all.length, 2 + Math.floor(rnd() * 3));
    const pick = new Set();
    while (pick.size < k && pick.size < all.length) pick.add(all[Math.floor(rnd() * all.length)]);
    for (const s of next.board.sprints) s.cards = s.cards.filter(c => !pick.has(c.id));
    /* neue Karten */
    const nNew = Math.floor(rnd() * 3);
    const movers = [...pick].map(id => ({ ...card(id, tgtC) }));
    for (let i = 0; i < nNew; i++) {
      const id = 'n' + iter + '_' + i;
      movers.push({ ...card(id, tgtC) });
      next.board.nextDisplayId += 1;
    }
    /* zufällige Zielreihenfolge im Zielspalten-Block */
    for (let i = movers.length - 1; i > 0; i--) {
      const j = Math.floor(rnd() * (i + 1)); [movers[i], movers[j]] = [movers[j], movers[i]];
    }
    /* an zufälliger Position relativ zu evtl. schon vorhandenen tgtC-Karten */
    const insAt = Math.floor(rnd() * (tgtS.cards.filter(c => c.columnId === tgtC).length + 1));
    let seen = 0, pos = tgtS.cards.length;
    for (let i = 0; i < tgtS.cards.length; i++) {
      if (tgtS.cards[i].columnId === tgtC) { if (seen === insAt) { pos = i; break; } seen++; }
    }
    tgtS.cards.splice(pos, 0, ...movers);
    return next;
  }

  it('1500 zufällige Mehrfacheintritts-Szenarien: fold(prev,diff) ≡ next', () => {
    for (let iter = 0; iter < 1500; iter++) {
      const rnd = mulberry32(0x9e3779b9 ^ iter);
      const prev = genPrev(rnd);
      const next = genNext(prev, rnd, iter);
      const ops  = diffToOps(prev, next, 'bf');
      const proj = fold(prev, ops);
      const got = colView(proj), want = colView(next);
      if (JSON.stringify(got) !== JSON.stringify(want)) {
        throw new Error(`F-311 Round-Trip-Bruch iter=${iter}\nwant=${JSON.stringify(want)}\n got=${JSON.stringify(got)}`);
      }
      /* keine Karte verloren/dupliziert */
      const ids = s => Object.values(colView(s)).flatMap(c => Object.values(c).flat()).sort();
      expect(ids(proj)).toEqual(ids(next));
    }
  });

  /* F-316 (Sprint-36-Kritiker K-1): deterministisches Minimal-Gegenbeispiel —
     reine Block-Rotation EINER Spalte, keine neuen/gelöschten/Cross-Karten.
     Brach mit dem alten „ko-resident-Vorgänger"-Dedup ([c4,c5,c6,c3]). */
  it('F-316: reine Mehrkarten-Intra-Spalten-Rotation round-trippt', () => {
    const mk = (order) => ({
      version: '2', board: {
        id: 'b', title: 'B', nextDisplayId: 9, columnDefs: [{ id: 'col-c' }],
        sprints: [
          { id: 's1', title: 'S1', kind: 'sprint', cards: order.map(x => card(x, 'col-c')) },
          { id: '__backlog__', title: 'BL', kind: 'backlog', cards: [] },
        ], awaitingUser: null, templates: [], background: null,
      },
    });
    const prev = mk(['c3', 'c4', 'c5', 'c6']);
    const next = mk(['c5', 'c6', 'c3', 'c4']);
    const proj = fold(prev, diffToOps(prev, next, 't'));
    expect(colView(proj).s1['col-c']).toEqual(['c5', 'c6', 'c3', 'c4']);
  });

  /* F-316: ERSCHÖPFEND alle Permutationen einer Spalte für n=2..5
     (2+6+24+120 = 152 Fälle) — beweist die Intra-Spalten-Klasse
     vollständig, nicht nur stichprobenartig. */
  it('F-316: alle Spalten-Permutationen n=2..5 round-trippen (erschöpfend)', () => {
    const mk = (order) => ({
      version: '2', board: {
        id: 'b', title: 'B', nextDisplayId: 99, columnDefs: [{ id: 'col-c' }],
        sprints: [
          { id: 's1', title: 'S1', kind: 'sprint', cards: order.map(x => card(x, 'col-c')) },
          { id: '__backlog__', title: 'BL', kind: 'backlog', cards: [] },
        ], awaitingUser: null, templates: [], background: null,
      },
    });
    const perms = (a) => a.length <= 1 ? [a]
      : a.flatMap((x, i) => perms([...a.slice(0, i), ...a.slice(i + 1)]).map(p => [x, ...p]));
    let cases = 0;
    for (let n = 2; n <= 5; n++) {
      const base = Array.from({ length: n }, (_, i) => 'c' + i);
      for (const P of perms(base)) {
        const proj = fold(mk(base), diffToOps(mk(base), mk(P), 't'));
        expect(colView(proj).s1['col-c']).toEqual(P);
        cases++;
      }
    }
    expect(cases).toBe(2 + 6 + 24 + 120);
  });
});
