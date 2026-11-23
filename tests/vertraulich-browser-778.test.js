/* @vitest-environment happy-dom */

/**
 * Karte #778 (ADR-0140 §10): Hinweis „vertraulich bewahrt“ im Browser.
 *
 * Der Server meldet im PUT den Header `X-Aboard-Vertraulich-Bewahrt`, wenn
 * er einen Schalter stehen gelassen hat. Er muss VOR dem frühen return bei
 * `X-Aboard-Stand-Geaendert` gelesen werden und bis zu beiden Aufrufern
 * durchgehen: `save` (Rückfall auf den PUT) und `speichereGanz` („Meine
 * Version behalten“) — Vorprüfung #778, P-2.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { __testing__, leseVertraulichBewahrt } from '../src/storage-plugin.js';
import { Config } from '../src/config.js';

const hasRESTPlugin = typeof __testing__.createRESTPlugin === 'function';

describe('#778 leseVertraulichBewahrt', () => {
  it('liest Anzahl und Kennungen, sonst null', () => {
    expect(leseVertraulichBewahrt('2; ids=k1,t1')).toEqual({ anzahl: 2, ids: ['k1', 't1'] });
    expect(leseVertraulichBewahrt('30; ids=')).toEqual({ anzahl: 30, ids: [] });
    for (const w of [null, undefined, '', '0; ids=', 'x; ids=a', '2', '2; ids=a b', '2; ids=<x>']) {
      expect(leseVertraulichBewahrt(w), String(w)).toBe(null);
    }
  });
});

describe.skipIf(!hasRESTPlugin)('#778 REST-Plugin reicht den Hinweis durch', () => {
  let plugin;
  beforeEach(() => {
    Config.set?.('serverUrl', '');
    plugin = __testing__.createRESTPlugin();
  });
  afterEach(() => vi.unstubAllGlobals());

  function fetchMitHeader(header) {
    return vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET';
      if (method === 'GET') {
        return Promise.resolve({ ok: true, status: 200, headers: { get: h => (h === 'ETag' ? '"alt"' : null) },
          json: () => Promise.resolve({ version: '2', board: { id: 'b1', title: 't' } }) });
      }
      if (method === 'PUT') {
        return Promise.resolve({ ok: true, status: 200,
          headers: { get: h => ({ ETag: '"neu"', 'X-Aboard-Stand-Geaendert': '1', 'X-Aboard-Vertraulich-Bewahrt': header })[h] ?? null },
          json: () => Promise.resolve({}) });
      }
      return Promise.resolve({ ok: false, status: 500, headers: { get: () => null }, json: () => Promise.resolve({}) });
    });
  }

  it('speichereGanz („Meine Version behalten“) liefert vertraulichBewahrt', async () => {
    await plugin.preload('b1');
    vi.stubGlobal('fetch', fetchMitHeader('1; ids=k1'));
    const r = await plugin.speichereGanz({ version: '2', board: { id: 'b1', title: 't' } });
    expect(r).toEqual({ ok: true, method: 'server', vertraulichBewahrt: { anzahl: 1, ids: ['k1'] } });
  });

  it('ohne Header bleibt die Antwort wie bisher', async () => {
    await plugin.preload('b1');
    vi.stubGlobal('fetch', fetchMitHeader(null));
    expect(await plugin.speichereGanz({ version: '2', board: { id: 'b1', title: 't' } })).toEqual({ ok: true, method: 'server' });
  });
});
