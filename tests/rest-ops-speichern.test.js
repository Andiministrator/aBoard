/**
 * REST-Plugin speichert über POST /boards/{id}/ops (Karte #565, ADR-0129 §4,
 * Nachtrag Sprint 72, Punkte 1, 2, 4, 5, 6).
 *
 * Läuft nur in den Kontexten mit REST-Plugin (server, server-frontend); im
 * Standalone- und slim Kontext ist `createRESTPlugin` per DCE null.
 *
 * fetch ist ein Router, der jeden Aufruf mitschreibt. Antworten kommen aus
 * einer Warteschlange je Methode + Pfad-Endung, damit ein Test genau sagt,
 * welcher Weg (GET, POST /ops, PUT) welche Antwort bekommt.
 */
import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { __testing__ } from '../src/storage-plugin.js';
import { State } from '../src/state.js';
import { EventBus } from '../src/eventbus.js';
import { describeServer } from './helpers/build-flags.js';

const brett = () => ({
  version: '2',
  board: {
    id: 'b1', title: 'B', nextDisplayId: 3,
    columnDefs: [{ id: 'col-backlog' }, { id: 'col-ready' }],
    sprints: [{ id: 's1', title: 'S', kind: 'sprint', cards: [
      { id: 'c1', displayId: 1, title: 'eins', description: '', columnId: 'col-backlog', comments: [] },
      { id: 'c2', displayId: 2, title: 'zwei', description: '', columnId: 'col-backlog', comments: [] },
    ] }],
  },
});
const klon = x => JSON.parse(JSON.stringify(x));

function antwort(status, { etag = null, body = {}, header = {} } = {}) {
  return {
    ok: status >= 200 && status < 300, status,
    headers: { get: h => (h === 'ETag' ? etag : (header[h] ?? null)) },
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  };
}

/* Router: `schlange['POST ops']` usw. — ein Eintrag ist eine Antwort, eine
   Funktion (liefert Antwort oder Promise) oder ein Error (wird geworfen). */
function router() {
  const aufrufe = [];
  const schlange = { 'GET board': [], 'POST ops': [], 'PUT board': [] };
  const fetchMock = vi.fn((url, init = {}) => {
    const methode = init.method || 'GET';
    const weg = url.endsWith('/ops') ? 'ops' : 'board';
    const schluessel = `${methode} ${weg}`;
    const body = init.body ? JSON.parse(init.body) : undefined;
    aufrufe.push({ schluessel, url, headers: init.headers || {}, body });
    const q = schlange[schluessel] || [];
    const naechste = q.length > 1 ? q.shift() : q[0];
    if (!naechste) return Promise.reject(new Error(`kein Mock für ${schluessel}`));
    if (naechste instanceof Error) return Promise.reject(naechste);
    return Promise.resolve(typeof naechste === 'function' ? naechste() : naechste);
  });
  return { fetchMock, aufrufe, schlange };
}

describeServer('REST-Plugin — Speichern über Ops (Karte #565)', () => {
  let plugin, r;

  /* Board laden wie im echten Ablauf: switchBoard setzt Bezug und ETag. */
  async function geladen(etag = '"e0"') {
    r.schlange['GET board'] = [antwort(200, { etag, body: brett() })];
    const sw = await plugin.switchBoard('b1');
    expect(sw.ok).toBe(true);
    State.set(klon(sw.state));
    r.aufrufe.length = 0;
  }

  /* Lokale Änderung wie aus der UI: neuer State-Stand, dann speichern. */
  function aendere(fn) {
    const neu = klon(State.get());
    fn(neu.board);
    State.set(neu);
    return State.get();
  }
  const karte = (b, id) => b.sprints[0].cards.find(c => c.id === id);

  beforeEach(() => {
    plugin = __testing__.createRESTPlugin();
    r = router();
    vi.stubGlobal('fetch', r.fetchMock);
  });

  afterEach(() => {
    plugin.stopPolling();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('eine Änderung geht als Ops an POST /boards/{id}/ops, nicht als PUT', async () => {
    await geladen();
    r.schlange['POST ops'] = [antwort(200, { etag: '"p1"' })];
    const res = await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    expect(res.ok).toBe(true);
    const posts = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    expect(posts).toHaveLength(1);
    expect(posts[0].url).toMatch(/\/boards\/b1\/ops$/);
    /* Titel ist Freitext → mit erwartetem Vorwert (Karte #604, ADR-0130 §2). */
    expect(posts[0].body.ops).toEqual([
      expect.objectContaining({ type: 'card.fieldIf', payload: { cardId: 'c1', field: 'title', value: 'neu', expect: 'eins' } }),
    ]);
    expect(r.aufrufe.some(a => a.schluessel === 'PUT board')).toBe(false);
  });

  it('leerer Diff: kein Request', async () => {
    await geladen();
    const res = await plugin.save(State.get());
    expect(res.ok).toBe(true);
    expect(r.aufrufe).toHaveLength(0);
  });

  it('nach dem POST: B := S — das nächste Speichern schickt nur die neue Änderung', async () => {
    await geladen();
    r.schlange['POST ops'] = [antwort(200)];
    await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    await plugin.save(aendere(b => { karte(b, 'c2').title = 'auch neu'; }));
    const posts = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    expect(posts).toHaveLength(2);
    expect(posts[1].body.ops.map(o => o.payload.cardId)).toEqual(['c2']);
  });

  it('die ETag der POST-Antwort wird NICHT übernommen; das GET danach fragt mit der alten', async () => {
    await geladen('"e0"');
    plugin.startPolling(vi.fn());
    r.schlange['POST ops'] = [antwort(200, { etag: '"p1"' })];
    r.schlange['GET board'] = [antwort(304)];
    const res = await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    expect(res.gepostet).toBe(true);                     /* main.js stösst das GET an */
    await plugin.holeJetzt();
    const gets = r.aufrufe.filter(a => a.schluessel === 'GET board');
    expect(gets).toHaveLength(1);
    expect(gets[0].headers['If-None-Match']).toBe('"e0"');
    /* und ein späterer PUT trägt ebenfalls nicht die POST-ETag */
    r.schlange['PUT board'] = [antwort(200, { etag: '"q"' })];
    await plugin.speichereGanz(State.get());
    const put = r.aufrufe.find(a => a.schluessel === 'PUT board');
    expect(put.headers['If-Match']).toBe('"e0"');
  });

  it('das GET nach dem POST liefert den Server-Stand an den Poll-Callback', async () => {
    await geladen('"e0"');
    const cb = vi.fn();
    plugin.startPolling(cb);
    const server = klon(brett());
    server.board.sprints[0].cards[0].title = 'neu';
    server.board.sprints[0].cards[1].title = 'fremd';
    r.schlange['POST ops'] = [antwort(200)];
    r.schlange['GET board'] = [antwort(200, { etag: '"e1"', body: server })];
    await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    await plugin.holeJetzt();
    expect(cb).toHaveBeenCalledWith(server);
  });

  it('#566: ein geholter, aber nicht übernommener Stand lässt die ETag stehen', async () => {
    await geladen('"e0"');
    plugin.startPolling(vi.fn());                          /* main.js lehnt ab: kein bezugUebernehmen */
    r.schlange['GET board'] = [antwort(200, { etag: '"e1"', body: brett() })];
    await plugin.holeJetzt();
    await plugin.holeJetzt();
    const gets = r.aufrufe.filter(a => a.schluessel === 'GET board');
    expect(gets.map(g => g.headers['If-None-Match'])).toEqual(['"e0"', '"e0"']);
  });

  it('#566: übernimmt main.js genau diesen Stand, rückt die ETag nach', async () => {
    await geladen('"e0"');
    plugin.startPolling(st => plugin.bezugUebernehmen(st));
    r.schlange['GET board'] = [antwort(200, { etag: '"e1"', body: brett() })];
    await plugin.holeJetzt();
    await plugin.holeJetzt();
    const gets = r.aufrufe.filter(a => a.schluessel === 'GET board');
    expect(gets.map(g => g.headers['If-None-Match'])).toEqual(['"e0"', '"e1"']);
  });

  it('#566: ein anderes Objekt übernommen (z. B. 409-Stand) rückt die Poll-ETag NICHT nach', async () => {
    await geladen('"e0"');
    plugin.startPolling(() => plugin.bezugUebernehmen(brett()));
    r.schlange['GET board'] = [antwort(200, { etag: '"e1"', body: brett() })];
    await plugin.holeJetzt();
    await plugin.holeJetzt();
    const gets = r.aufrufe.filter(a => a.schluessel === 'GET board');
    expect(gets.map(g => g.headers['If-None-Match'])).toEqual(['"e0"', '"e0"']);
  });

  it('das Speichern holt nicht selbst — das GET entscheidet main.js nach seiner Generation', async () => {
    await geladen();
    plugin.startPolling(vi.fn());
    let loslassen;
    r.schlange['POST ops'] = [() => new Promise(res => { loslassen = () => res(antwort(200)); })];
    const laeuft = plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    await vi.waitFor(() => expect(loslassen).toBeTypeOf('function'));
    aendere(b => { karte(b, 'c2').title = 'getippt'; });   /* Nutzer tippt weiter */
    loslassen();
    await laeuft;
    expect(r.aufrufe.some(a => a.schluessel === 'GET board')).toBe(false);
  });

  it('Save während laufendem Save: höchstens ein POST; der zweite wartet und schickt nur seine Änderung', async () => {
    await geladen();
    let loslassen;
    r.schlange['POST ops'] = [
      () => new Promise(res => { loslassen = () => res(antwort(200)); }),
      antwort(200),
    ];
    const erster = plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    await vi.waitFor(() => expect(loslassen).toBeTypeOf('function'));
    const zweiter = plugin.save(aendere(b => { karte(b, 'c2').title = 'auch neu'; }));
    await new Promise(res => setTimeout(res, 10));
    expect(r.aufrufe.filter(a => a.schluessel === 'POST ops')).toHaveLength(1);
    loslassen();
    await Promise.all([erster, zweiter]);
    const posts = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    expect(posts).toHaveLength(2);
    expect(posts[1].body.ops.map(o => o.payload.cardId)).toEqual(['c2']);
  });

  it('das Flag wird auch nach einem Netzfehler zurückgesetzt (kein Hänger)', async () => {
    await geladen();
    r.schlange['POST ops'] = [new Error('offline'), antwort(200)];
    const erster = await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    expect(erster.ok).toBe(false);
    const zweiter = await plugin.save(State.get());
    expect(zweiter.ok).toBe(true);
  });

  it('Netzabbruch nach dem Senden: Wiederholung mit Wartezeit, derselbe Batch geht erneut', async () => {
    await geladen();
    r.schlange['POST ops'] = [new Error('Antwort verloren'), antwort(200)];
    const erster = await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    expect(erster).toEqual({ ok: false, erneutNachMs: 2000 });
    await plugin.save(State.get());
    const posts = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    expect(posts).toHaveLength(2);
    expect(posts[1].body.ops.map(o => o.payload)).toEqual(posts[0].body.ops.map(o => o.payload));
    expect(r.aufrufe.some(a => a.schluessel === 'PUT board')).toBe(false);
  });

  it('429 mit Retry-After: nichts verworfen, kein PUT, Wartezeit aus dem Header', async () => {
    await geladen();
    r.schlange['POST ops'] = [antwort(429, { header: { 'Retry-After': '7' } })];
    const res = await plugin.autoPersist(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    expect(res).toEqual({ ok: false, erneutNachMs: 7000 });
    expect(r.aufrufe.some(a => a.schluessel === 'PUT board')).toBe(false);
  });

  it('503 ohne Retry-After: Wartezeit verdoppelt sich, gedeckelt', async () => {
    await geladen();
    r.schlange['POST ops'] = [antwort(503)];
    const lokal = aendere(b => { karte(b, 'c1').title = 'neu'; });
    const zeiten = [];
    for (let i = 0; i < 8; i++) zeiten.push((await plugin.save(lokal)).erneutNachMs);
    expect(zeiten).toEqual([2000, 4000, 8000, 16000, 32000, 60000, 60000, 60000]);
  });

  it('400: genau ein Rückfall auf PUT mit If-Match, Fehler sichtbar in der Konsole', async () => {
    await geladen('"e0"');
    const fehler = vi.spyOn(console, 'error').mockImplementation(() => {});
    r.schlange['POST ops'] = [antwort(400, { body: { error: 'invalid ops' } })];
    r.schlange['PUT board'] = [antwort(200, { etag: '"e1"' })];
    const res = await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    expect(res.ok).toBe(true);
    expect(r.aufrufe.filter(a => a.schluessel === 'POST ops')).toHaveLength(1);
    const puts = r.aufrufe.filter(a => a.schluessel === 'PUT board');
    expect(puts).toHaveLength(1);
    expect(puts[0].headers['If-Match']).toBe('"e0"');
    expect(fehler).toHaveBeenCalled();
  });

  it('405 (Endpunkt fehlt) wie 400: PUT-Rückfall; ein 409 dort bleibt Konflikt-Dialog', async () => {
    await geladen('"e0"');
    vi.spyOn(console, 'error').mockImplementation(() => {});
    const server = brett();
    r.schlange['POST ops'] = [antwort(405)];
    r.schlange['PUT board'] = [antwort(409, { etag: '"e9"', body: server })];
    const res = await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    expect(res.ok).toBe(false);
    expect(res.conflict.serverState).toEqual(server);
  });

  it('F-1576: 403 beim Ops-Speichern → Ereignis ops:abgelehnt (Board, Status), Rückfall wie bisher', async () => {
    await geladen('"e0"');
    vi.spyOn(console, 'error').mockImplementation(() => {});
    const gemeldet = [];
    const hoerer = e => gemeldet.push(e);
    EventBus.on('ops:abgelehnt', hoerer);
    try {
      r.schlange['POST ops'] = [antwort(403, { body: { error: 'forbidden' } })];
      r.schlange['PUT board'] = [antwort(403)];
      const res = await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
      expect(res.ok).toBe(false);
      expect(r.aufrufe.filter(a => a.schluessel === 'PUT board')).toHaveLength(1);
      expect(gemeldet).toEqual([{ boardId: 'b1', status: 403,
        ops: [{ type: 'card.fieldIf', cardId: 'c1', commentId: null }] }]);
    } finally { EventBus.off('ops:abgelehnt', hoerer); }
  });

  it('F-1576: 422 mit Protokoll → Ereignis ops:abgelehnt mit den abgelehnten Ops', async () => {
    await geladen();
    const gemeldet = [];
    const hoerer = e => gemeldet.push(e);
    EventBus.on('ops:abgelehnt', hoerer);
    try {
      r.schlange['POST ops'] = [
        antwort(422, { body: { protokoll: [{ art: 'ziel-fehlt', op: 'srv-1', index: 0, cardId: 'c1' }] } }),
        antwort(200),
      ];
      await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; karte(b, 'c2').title = 'auch neu'; }));
      /* nur die vom Server genannte Op (Index 0), nicht der angenommene Rest */
      expect(gemeldet).toEqual([{ boardId: 'b1', status: 422,
        ops: [{ type: 'card.fieldIf', cardId: 'c1', commentId: null }] }]);
    } finally { EventBus.off('ops:abgelehnt', hoerer); }
  });

  /* KA-10 (F-1579): Der Weg „comment.add mit 422 abgelehnt → Kommentar lokal
     entfernt → nächstes Speichern" erzeugt genau EINE Op: comment.delete für
     genau diesen Kommentar, sonst nichts. */
  it('F-1579: abgelehntes comment.add, lokal entfernt → nächster Batch ist nur comment.delete dieses Kommentars', async () => {
    await geladen();
    const kommentar = { id: 'k-neu', author: 'A', text: 'KI-Antwort', created: '2026-10-04' };
    r.schlange['POST ops'] = [
      antwort(422, { body: { protokoll: [{ art: 'ziel-fehlt', op: 'srv-1', index: 0, cardId: 'c1' }] } }),
    ];
    await plugin.save(aendere(b => { karte(b, 'c1').comments.push({ ...kommentar }); }));
    const erster = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    expect(erster).toHaveLength(1);
    expect(erster[0].body.ops.map(o => o.type)).toEqual(['comment.add']);
    /* wie _entferneKommentarLokal (src/llm-tab.js) */
    r.aufrufe.length = 0;
    r.schlange['POST ops'] = [antwort(200)];
    await plugin.save(aendere(b => { karte(b, 'c1').comments = karte(b, 'c1').comments.filter(k => k.id !== 'k-neu'); }));
    const zweiter = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    expect(zweiter).toHaveLength(1);
    expect(zweiter[0].body.ops).toHaveLength(1);
    expect(zweiter[0].body.ops[0].type).toBe('comment.delete');
    expect(zweiter[0].body.ops[0].payload).toEqual({ cardId: 'c1', commentId: 'k-neu' });
    expect(r.aufrufe.some(a => a.schluessel === 'PUT board')).toBe(false);
  });

  it('422 mit Index: abgelehnte Op heraus, Rest genau einmal neu, kein PUT, Meldung mit Karte', async () => {
    await geladen();
    r.schlange['POST ops'] = [
      antwort(422, { body: { protokoll: [{ art: 'ziel-fehlt', op: 'srv-1', index: 0, cardId: 'c1' }] } }),
      antwort(200),
    ];
    const lokal = aendere(b => { karte(b, 'c1').title = 'neu'; karte(b, 'c2').title = 'auch neu'; });
    const res = await plugin.save(lokal);
    const posts = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    expect(posts).toHaveLength(2);
    expect(posts[0].body.ops.map(o => o.payload.cardId)).toEqual(['c1', 'c2']);
    expect(posts[1].body.ops.map(o => o.payload.cardId)).toEqual(['c2']);
    expect(r.aufrufe.some(a => a.schluessel === 'PUT board')).toBe(false);
    expect(res.ok).toBe(true);
    expect(res.abgelehnt).toEqual([{ art: 'ziel-fehlt', cardId: 'c1', displayId: 1, titel: 'neu' }]);
    expect(res.meineFassung).toEqual(lokal);
    /* B := S: die abgelehnte Änderung wird beim nächsten Speichern nicht erneut geschickt */
    r.aufrufe.length = 0;
    await plugin.save(State.get());
    expect(r.aufrufe.filter(a => a.schluessel === 'POST ops')).toHaveLength(0);
  });

  it('#604: 422 feld-geaendert — Meldung trägt Feld, Server-Fassung und eigene Fassung', async () => {
    await geladen();
    r.schlange['POST ops'] = [
      antwort(422, { body: { protokoll: [{ art: 'feld-geaendert', op: 'srv-1', index: 0, cardId: 'c1',
        feld: 'description', aktuell: 'von jemand anderem' }] } }),
    ];
    const res = await plugin.save(aendere(b => { karte(b, 'c1').description = 'meins'; }));
    expect(res.ok).toBe(true);
    expect(res.abgelehnt).toEqual([{ art: 'feld-geaendert', cardId: 'c1', displayId: 1, titel: 'eins',
      feld: 'description', aktuell: 'von jemand anderem', mein: 'meins' }]);
    expect(r.aufrufe.some(a => a.schluessel === 'PUT board')).toBe(false);
  });

  it('#604: „Meine Fassung übernehmen“ sendet genau die Felder als card.field, kein PUT', async () => {
    await geladen();
    r.schlange['POST ops'] = [antwort(200)];
    const res = await plugin.ueberschreibeFelder([{ cardId: 'c1', feld: 'description', mein: 'meins' }]);
    expect(res.ok).toBe(true);
    const posts = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    expect(posts).toHaveLength(1);
    expect(posts[0].body.ops).toEqual([
      expect.objectContaining({ type: 'card.field', payload: { cardId: 'c1', field: 'description', value: 'meins' } }),
    ]);
    expect(r.aufrufe.some(a => a.schluessel === 'PUT board')).toBe(false);
  });

  it('422 auch beim Rest: nichts mehr automatisch, Meldung nennt den Rest', async () => {
    await geladen();
    r.schlange['POST ops'] = [
      antwort(422, { body: { protokoll: [{ art: 'klemmung', op: 'srv-1', index: 0, cardId: 'c1' }] } }),
      antwort(422, { body: { protokoll: [{ art: 'ziel-fehlt', op: 'srv-2', index: 0, cardId: 'c2' }] } }),
    ];
    const res = await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; karte(b, 'c2').title = 'auch neu'; }));
    expect(r.aufrufe.filter(a => a.schluessel === 'POST ops')).toHaveLength(2);
    expect(r.aufrufe.some(a => a.schluessel === 'PUT board')).toBe(false);
    expect(res.abgelehnt.map(a => a.art)).toEqual(['klemmung', 'rest']);
  });

  it('422 ohne Protokoll (Hülle verletzt): wie 400 — PUT-Rückfall', async () => {
    await geladen();
    vi.spyOn(console, 'error').mockImplementation(() => {});
    r.schlange['POST ops'] = [antwort(422, { body: { error: 'resulting board would violate the envelope' } })];
    r.schlange['PUT board'] = [antwort(200)];
    const res = await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    expect(res.ok).toBe(true);
    expect(r.aufrufe.filter(a => a.schluessel === 'PUT board')).toHaveLength(1);
  });

  it('grosser Diff: nacheinander in Batches zu höchstens 200 Ops, danach B := S', async () => {
    await geladen();
    r.schlange['POST ops'] = [antwort(200)];
    const lokal = aendere(b => {
      for (let i = 0; i < 250; i++) {
        b.sprints[0].cards.push({ id: `n${i}`, displayId: 10 + i, title: `n${i}`, description: '', columnId: 'col-ready', comments: [] });
      }
    });
    const res = await plugin.save(lokal);
    expect(res.ok).toBe(true);
    const posts = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    expect(posts.map(p => p.body.ops.length)).toEqual([200, 50]);
    r.aufrufe.length = 0;
    await plugin.save(State.get());
    expect(r.aufrufe).toHaveLength(0);
  });

  it('scheitert ein späterer Batch, bleiben die angenommenen — der nächste Save schickt nur den Rest', async () => {
    await geladen();
    r.schlange['POST ops'] = [antwort(200), antwort(503), antwort(200)];
    aendere(b => {
      for (let i = 0; i < 250; i++) {
        b.sprints[0].cards.push({ id: `n${i}`, displayId: 10 + i, title: `n${i}`, description: '', columnId: 'col-ready', comments: [] });
      }
    });
    const erster = await plugin.save(State.get());
    expect(erster.ok).toBe(false);
    await plugin.save(State.get());
    const posts = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    expect(posts.map(p => p.body.ops.length)).toEqual([200, 50, 50]);
    expect(posts[2].body.ops.map(o => o.payload.card.id)).toEqual(posts[1].body.ops.map(o => o.payload.card.id));
  });

  it('K-A2: nach 422 in einem geteilten Diff zählt nur das Angenommene als gesendet', async () => {
    await geladen();
    r.schlange['POST ops'] = [
      antwort(422, { body: { protokoll: [{ art: 'ziel-fehlt', op: 'srv-0', index: 0, sprintId: 's1' }] } }),
      antwort(200),                 /* Rest des ersten Batches */
      new Error('Netz weg'),        /* zweiter Batch */
      antwort(200),
    ];
    aendere(b => {
      for (let i = 0; i < 250; i++) {
        b.sprints[0].cards.push({ id: `n${i}`, displayId: 10 + i, title: `n${i}`, description: '', columnId: 'col-ready', comments: [] });
      }
    });
    const erster = await plugin.save(State.get());
    expect(erster.ok).toBe(false);
    await plugin.save(State.get());
    const posts = r.aufrufe.filter(a => a.schluessel === 'POST ops');
    const letzter = posts[posts.length - 1].body.ops.map(o => o.payload.card.id);
    /* die abgelehnte Karte ist nicht gesendet — sie gehört wieder in den Diff */
    expect(letzter).toContain('n0');
    /* die angenommenen des ersten Batches nicht noch einmal */
    expect(letzter).not.toContain('n1');
    expect(letzter).toContain('n249');
  });

  it('eine einzelne Op über 900 KiB geht per PUT', async () => {
    await geladen();
    r.schlange['PUT board'] = [antwort(200)];
    await plugin.save(aendere(b => { karte(b, 'c1').description = 'x'.repeat(950 * 1024); }));
    expect(r.aufrufe.filter(a => a.schluessel === 'POST ops')).toHaveLength(0);
    expect(r.aufrufe.filter(a => a.schluessel === 'PUT board')).toHaveLength(1);
  });

  it('ohne Bezug (Boot ohne Server) speichert es per PUT und hat danach einen', async () => {
    await plugin.preload('b1');
    State.set(brett());
    r.schlange['GET board'] = [antwort(200, { etag: '"e0"' })];
    r.schlange['PUT board'] = [antwort(200, { etag: '"e1"' })];
    r.schlange['POST ops'] = [antwort(200)];
    await plugin.save(State.get());
    expect(r.aufrufe.filter(a => a.schluessel === 'PUT board')).toHaveLength(1);
    await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    expect(r.aufrufe.filter(a => a.schluessel === 'POST ops')).toHaveLength(1);
  });

  it('naechstesGanz (JSON-Import): das nächste Speichern ist ein PUT, danach wieder Ops', async () => {
    await geladen();
    r.schlange['PUT board'] = [antwort(200, { etag: '"e1"' })];
    r.schlange['POST ops'] = [antwort(200)];
    plugin.naechstesGanz();
    await plugin.save(aendere(b => { karte(b, 'c1').title = 'importiert'; }));
    expect(r.aufrufe.filter(a => a.schluessel === 'PUT board')).toHaveLength(1);
    expect(r.aufrufe.filter(a => a.schluessel === 'POST ops')).toHaveLength(0);
    await plugin.save(aendere(b => { karte(b, 'c2').title = 'danach'; }));
    expect(r.aufrufe.filter(a => a.schluessel === 'POST ops')).toHaveLength(1);
  });

  it('bezugUebernehmen: nach einer Übernahme rechnet der Diff gegen den neuen Stand', async () => {
    await geladen();
    const server = brett();
    server.board.sprints[0].cards[1].title = 'fremd';
    plugin.bezugUebernehmen(server);
    State.set(klon(server));
    r.schlange['POST ops'] = [antwort(200)];
    await plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    const ops = r.aufrufe.find(a => a.schluessel === 'POST ops').body.ops;
    /* die fremde Änderung an c2 geht nicht als Rücknahme zurück */
    expect(ops.map(o => o.payload.cardId)).toEqual(['c1']);
  });

  it('der Bezug ist eine eigene Kopie: Änderungen am übergebenen Objekt wirken nicht auf ihn', async () => {
    await geladen();
    const server = brett();
    plugin.bezugUebernehmen(server);
    server.board.sprints[0].cards[0].title = 'nachträglich mutiert';
    State.set(klon(server));
    r.schlange['POST ops'] = [antwort(200)];
    await plugin.save(State.get());
    /* hätte das Plugin die Referenz gehalten, wäre der Diff leer */
    expect(r.aufrufe.filter(a => a.schluessel === 'POST ops')).toHaveLength(1);
  });

  it('kein Poll, solange ein Speichern läuft', async () => {
    await geladen();
    vi.useFakeTimers();
    const cb = vi.fn();
    plugin.startPolling(cb);
    let loslassen;
    r.schlange['POST ops'] = [() => new Promise(res => { loslassen = () => res(antwort(200)); })];
    r.schlange['GET board'] = [antwort(200, { etag: '"e1"', body: brett() })];
    const laeuft = plugin.save(aendere(b => { karte(b, 'c1').title = 'neu'; }));
    await vi.advanceTimersByTimeAsync(25_000);                 /* mehrere Poll-Ticks */
    expect(r.aufrufe.filter(a => a.schluessel === 'GET board')).toHaveLength(0);
    loslassen();
    await laeuft;
    vi.useRealTimers();
  });
});
