/* Tests für AdminClient.listUsers/deleteUser (Karte #575, Verify-Lauf
   Sprint 72, V-2). Der Admin-Dialog-Test mockt den Client vollständig —
   ohne diese Datei wäre der echte Request (Pfad, Methode, Body) ungetestet. */
import { it, expect, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';
import { AdminClient } from '../src/admin-client.js';

describeServer('AdminClient — Nutzer-Endpunkte (#575)', () => {
  afterEach(() => { vi.unstubAllGlobals(); });

  function fetchStub(status, body) {
    const f = vi.fn(async () => ({ ok: status >= 200 && status < 300, status, json: async () => body }));
    vi.stubGlobal('fetch', f);
    return f;
  }

  it('deleteUser ohne Übertragung sendet keinen Body und kodiert die ID', async () => {
    const f = fetchStub(200, { ok: true, pseudonym: 'geloescht:1' });
    const r = await AdminClient.deleteUser('a/b c', {});
    expect(r.ok).toBe(true);
    const [url, opts] = f.mock.calls[0];
    expect(url).toBe('/admin/users/a%2Fb%20c');
    expect(opts.method).toBe('DELETE');
    expect(opts.body).toBeNull();
  });

  it('deleteUser mit Übertragung sendet {uebertragen}', async () => {
    const f = fetchStub(200, { ok: true });
    await AdminClient.deleteUser('u1', { b1: 'x@y.de' });
    expect(JSON.parse(f.mock.calls[0][1].body)).toEqual({ uebertragen: { b1: 'x@y.de' } });
  });

  it('409 mit boards wird mit Body durchgereicht, nicht geworfen', async () => {
    fetchStub(409, { error: 'sole owner', boards: [{ id: 'b1', title: 'T' }] });
    const r = await AdminClient.deleteUser('u1');
    expect(r).toEqual({ ok: false, status: 409, body: { error: 'sole owner', boards: [{ id: 'b1', title: 'T' }] } });
  });

  it('Netzfehler ergibt status 0 statt Ausnahme', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('offline'); }));
    expect(await AdminClient.deleteUser('u1')).toEqual({ ok: false, status: 0, body: null });
  });

  it('listUsers ruft GET /admin/users', async () => {
    const f = fetchStub(200, { users: [] });
    await AdminClient.listUsers();
    expect(f.mock.calls[0][0]).toBe('/admin/users');
  });
});

/* Server-Nacharbeit A (f7c5a42f), Folgearbeit Frontend: Token-Widerruf per id (F-1434)
   und seitenweise Board-Liste (F-1442). */
describeServer('AdminClient — id-Widerruf und seitenweise Board-Liste', () => {
  afterEach(() => { vi.unstubAllGlobals(); });

  it('revokeAgentToken sendet die übergebene Kennung kodiert (id oder Name)', async () => {
    const f = vi.fn(async () => ({ ok: true, status: 200, json: async () => ({}) }));
    vi.stubGlobal('fetch', f);
    await AdminClient.revokeAgentToken('tok/1 x');
    expect(f.mock.calls[0][0]).toBe('/admin/agent-tokens/tok%2F1%20x');
    expect(f.mock.calls[0][1].method).toBe('DELETE');
  });

  function seiten(map) {
    const f = vi.fn(async (url) => {
      const s = map[url];
      return { ok: !!s, status: s ? 200 : 500, json: async () => s || null };
    });
    vi.stubGlobal('fetch', f);
    return f;
  }

  it('lädt Folgeseiten über next_offset nach und vereint die Boards', async () => {
    const f = seiten({
      '/admin/boards': { boards: [{ id: 'a' }, { id: 'b' }], total: 5, next_offset: 2 },
      '/admin/boards?offset=2': { boards: [{ id: 'c' }, { id: 'd' }], total: 5, next_offset: 4 },
      '/admin/boards?offset=4': { boards: [{ id: 'e' }], total: 5 },
    });
    const r = await AdminClient.listAllBoards();
    expect(r.ok).toBe(true);
    expect(r.body.boards.map(b => b.id)).toEqual(['a', 'b', 'c', 'd', 'e']);
    expect(f).toHaveBeenCalledTimes(3);
  });

  it('V-4: doppelte Boards (verschoben während des Ladens) fallen weg, Fehlbestand gegenüber total → unvollstaendig', async () => {
    seiten({
      '/admin/boards': { boards: [{ id: 'a' }, { id: 'b' }], total: 4, next_offset: 2 },
      '/admin/boards?offset=2': { boards: [{ id: 'b' }, { id: 'c' }], total: 4 },
    });
    const r = await AdminClient.listAllBoards();
    expect(r.body.boards.map(b => b.id)).toEqual(['a', 'b', 'c']);
    expect(r.body.unvollstaendig).toBe(true);
  });

  it('F-1477 (a): next_offset rückläufig ohne total → unvollstaendig true, keine Endlosschleife', async () => {
    const f = seiten({
      '/admin/boards': { boards: [{ id: 'a' }], next_offset: 2 },
      '/admin/boards?offset=2': { boards: [{ id: 'b' }], next_offset: 1 },
    });
    const r = await AdminClient.listAllBoards();
    expect(r.body.boards.map(b => b.id)).toEqual(['a', 'b']);
    expect(r.body.unvollstaendig).toBe(true);
    expect(f).toHaveBeenCalledTimes(2);
  });

  it('F-1477 (b): Boards ohne id werden nicht gegeneinander entdoppelt', async () => {
    seiten({ '/admin/boards': { boards: [{ title: 'x' }, { title: 'y' }, { id: 'a' }, { id: 'a' }] } });
    const r = await AdminClient.listAllBoards();
    expect(r.body.boards.map(b => b.id ?? b.title)).toEqual(['x', 'y', 'a']);
  });

  it('V-4: vollständige Liste → unvollstaendig false', async () => {
    seiten({ '/admin/boards': { boards: [{ id: 'a' }], total: 1 } });
    const r = await AdminClient.listAllBoards();
    expect(r.body.unvollstaendig).toBe(false);
  });

  it('älterer Server ohne next_offset: genau eine Anfrage wie bisher', async () => {
    const f = seiten({ '/admin/boards': { boards: [{ id: 'a' }] } });
    const r = await AdminClient.listAllBoards();
    expect(r.body.boards).toHaveLength(1);
    expect(f).toHaveBeenCalledTimes(1);
  });

  it('Endlosschleifen-Schutz: gleichbleibendes next_offset bricht ab', async () => {
    const f = seiten({
      '/admin/boards': { boards: [{ id: 'a' }], next_offset: 1 },
      '/admin/boards?offset=1': { boards: [{ id: 'b' }], next_offset: 1 },
    });
    const r = await AdminClient.listAllBoards();
    expect(r.body.boards).toHaveLength(2);
    expect(f.mock.calls.length).toBeLessThanOrEqual(3);
  });

  it('Fehler auf einer Folgeseite wird zurückgegeben statt still abzuschneiden', async () => {
    seiten({ '/admin/boards': { boards: [{ id: 'a' }], next_offset: 1 } });
    const r = await AdminClient.listAllBoards();
    expect(r.ok).toBe(false);
  });
});
