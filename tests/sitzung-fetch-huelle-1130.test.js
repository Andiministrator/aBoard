/* @vitest-environment happy-dom */
/**
 * Karte #1130 / F-2977, ADR-0148 §5.
 *
 * Gemeinsame Fetch-Hülle `holen` (src/sitzung-abgelaufen.js): führt `fetch` aus und
 * gibt jede Antwort an `Sitzung.meldeAntwort`. Die zuvor stummen Wege in
 * storage-plugin.js (switchBoard, listKnownBoards, knownUsers, sendCardEmail,
 * transferCard, peekBoardColumns, fetchEtag) und Beispiele aus Client-Modulen
 * melden einen 401 jetzt. switchBoard meldet bei abgelaufener Sitzung nicht mehr
 * das irreführende `no-handle`.
 *
 * Vorbild: tests/sitzung-abgelaufen-restwege-1120.test.js. Nur Server-Build.
 */
import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import * as SA from '../src/sitzung-abgelaufen.js';
import { __testing__ } from '../src/storage-plugin.js';
import { NotificationsClient } from '../src/notifications-client.js';
import { LLMKeysClient } from '../src/llm-keys-client.js';
import { I18n } from '../src/i18n.js';
import { describeServer } from './helpers/build-flags.js';

const { Sitzung, holen } = SA;
const antwort = (status, body = {}) => ({
  ok: status >= 200 && status < 300, status,
  headers: { get: () => null },
  json: () => Promise.resolve(body),
  text: () => Promise.resolve(JSON.stringify(body)),
});
const SITZUNG401 = { error: 'unauthorized: session required' };
const spaeter = wert => new Promise(r => setTimeout(() => r(wert), 20));
const brett = () => ({ version: '2', board: { id: 'b1', title: 'B', nextDisplayId: 1, columnDefs: [{ id: 'col-backlog' }], sprints: [] } });

describeServer('Fetch-Hülle holen: 401 aller Server-Wege geht an meldeAntwort (F-2977, #1130)', () => {
  let meldeSpy;
  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    Sitzung.__zuruecksetzen();
    Sitzung.setzeOIDC(true);
    meldeSpy = vi.spyOn(Sitzung, 'meldeAntwort');
  });
  afterEach(() => {
    Sitzung.__zuruecksetzen();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  async function plugin() {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(200, brett()))));
    const p = __testing__.createRESTPlugin();
    await p.switchBoard('b1');
    meldeSpy.mockClear();
    return p;
  }
  const liefert = st => vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(st, st === 401 ? SITZUNG401 : {}))));
  const meldet401 = () => expect(meldeSpy).toHaveBeenCalledWith(401);

  it('holen: reicht die Antwort unverändert durch und meldet den Status', async () => {
    liefert(401);
    const res = await holen('/x', { method: 'GET' });
    expect(res.status).toBe(401);
    meldet401();
    expect(Sitzung.abgelaufen()).toBe(true);
  });

  it('holen: Netzfehler wirft wie fetch, nichts wird gemeldet', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('netz'))));
    await expect(holen('/x')).rejects.toThrow('netz');
    expect(meldeSpy).not.toHaveBeenCalled();
  });

  it('holen: 200 bei pausierter Sitzung hebt das Band auf', async () => {
    liefert(401); await holen('/x');
    expect(Sitzung.abgelaufen()).toBe(true);
    liefert(200); await holen('/x');
    expect(Sitzung.abgelaufen()).toBe(false);
  });

  it('switchBoard: 401 → sitzungAbgelaufen statt no-handle, Band steht', async () => {
    const p = await plugin();
    liefert(401);
    const r = await p.switchBoard('b2');
    expect(r).toEqual({ ok: false, sitzungAbgelaufen: true });
    meldet401();
  });

  it('switchBoard: 401 bei noch offener Betriebsart → erst klären, dann sitzungAbgelaufen (Kritiker S114 K-6)', async () => {
    const p = await plugin();
    Sitzung.__zuruecksetzen();                      // Betriebsart unbekannt (z.oidc === null)
    Sitzung.setzeBetriebsartAbruf(() => spaeter({ status: 401, body: SITZUNG401 })); // /me braucht wie am Server einen Takt
    liefert(401);
    expect(await p.switchBoard('b2')).toEqual({ ok: false, sitzungAbgelaufen: true });
    expect(Sitzung.abgelaufen()).toBe(true);
  });

  it('switchBoard: 401 im Forward-Auth-Betrieb bleibt no-handle (kein Band, also Meldung nötig)', async () => {
    const p = await plugin();
    Sitzung.__zuruecksetzen();
    Sitzung.setzeBetriebsartAbruf(() => spaeter({ status: 200, body: { anmeldung: 'forward-auth' } }));
    liefert(401);
    expect(await p.switchBoard('b2')).toEqual({ ok: false, error: 'no-handle' });
    expect(Sitzung.abgelaufen()).toBe(false);
  });

  it('switchBoard: 404 bleibt no-handle (Nicht-401 unverändert)', async () => {
    const p = await plugin();
    liefert(404);
    expect(await p.switchBoard('b2')).toEqual({ ok: false, error: 'no-handle' });
  });

  it('listKnownBoards: 401 gemeldet, wirft wie bisher', async () => {
    const p = await plugin();
    liefert(401);
    await expect(p.listKnownBoards()).rejects.toThrow('HTTP 401');
    meldet401();
  });

  it('knownUsers: 401 gemeldet, Ergebnis unverändert', async () => {
    const p = await plugin();
    liefert(401);
    expect(await p.knownUsers()).toEqual({ ok: false, status: 401, users: [], domains: [] });
    meldet401();
  });

  it('sendCardEmail: 401 gemeldet', async () => {
    const p = await plugin();
    liefert(401);
    expect((await p.sendCardEmail('c1', { to: ['a@example.org'] })).status).toBe(401);
    meldet401();
  });

  it('transferCard: 401 gemeldet', async () => {
    const p = await plugin();
    liefert(401);
    expect((await p.transferCard('c1', 'b2', 'col-backlog')).status).toBe(401);
    meldet401();
  });

  it('peekBoardColumns: 401 gemeldet, Ergebnis ok:false', async () => {
    const p = await plugin();
    liefert(401);
    expect(await p.peekBoardColumns('b2')).toEqual({ ok: false });
    meldet401();
  });

  it('Client-Modul notifications-client: 401 gemeldet', async () => {
    liefert(401);
    expect((await NotificationsClient.eventTypes()).status).toBe(401);
    meldet401();
  });

  it('Client-Modul llm-keys-client: 401 gemeldet', async () => {
    liefert(401);
    const k = Object.keys(LLMKeysClient).find(n => /^(list|get)/i.test(n)) || Object.keys(LLMKeysClient)[0];
    await LLMKeysClient[k]();
    meldet401();
  });
});
