/* @vitest-environment happy-dom */

/**
 * F-3106 (Karte #1179): `openDB` (src/idb.js) wartete ohne Frist auf `indexedDB.open`. Antwortet der
 * Browser nie, hingen alle Handle-Zugriffe — und mit ihnen der Start. Jetzt scheitert das Öffnen nach
 * `IDB_FRIST_MS`, der Cache wird geleert und der nächste Aufruf versucht es neu.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

let anfragen;
function stummesOpen() {
  const req = {};
  anfragen.push(req);
  return req; /* onsuccess/onerror feuern nie — außer der Test löst sie aus */
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.resetModules();
  anfragen = [];
  vi.stubGlobal('indexedDB', { open: vi.fn(stummesOpen) });
});
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

describe('F-3106 — Frist für IndexedDB', () => {
  it('idbGet liefert nach der Frist null statt zu hängen, und der nächste Aufruf öffnet neu', async () => {
    const { idbGet, IDB_FRIST_MS } = await import('../src/idb.js');
    let ergebnis = 'offen';
    idbGet('k').then(v => { ergebnis = v; });
    await vi.advanceTimersByTimeAsync(IDB_FRIST_MS - 1);
    expect(ergebnis).toBe('offen');
    await vi.advanceTimersByTimeAsync(1);
    expect(ergebnis).toBe(null); /* ohne `streng` wie bisher: jeder Fehler → null */
    expect(indexedDB.open).toHaveBeenCalledTimes(1);

    idbGet('k');
    expect(indexedDB.open).toHaveBeenCalledTimes(2); /* Cache geleert, kein toter Promise */
  });

  it('streng: die Frist wird als Fehler mit idbFrist gemeldet, nicht als „fehlt“', async () => {
    const { idbGet, IDB_FRIST_MS } = await import('../src/idb.js');
    const lauf = idbGet('k', { streng: true });
    const fehler = lauf.then(() => null, e => e);
    await vi.advanceTimersByTimeAsync(IDB_FRIST_MS);
    expect((await fehler)?.idbFrist).toBe(true);
  });

  it('ein Fehler von indexedDB.open nach der Frist ändert nichts mehr und leert keinen neueren Cache', async () => {
    const { idbGet, IDB_FRIST_MS } = await import('../src/idb.js');
    idbGet('k');
    await vi.advanceTimersByTimeAsync(IDB_FRIST_MS);
    idbGet('k'); /* zweiter Versuch: neue Anfrage */
    expect(anfragen.length).toBe(2);
    anfragen[0].error = new Error('spät'); anfragen[0].onerror();
    idbGet('k'); /* der zweite Versuch steht noch im Cache → keine dritte Anfrage */
    expect(anfragen.length).toBe(2);
  });

  it('kommt die Verbindung erst nach der Frist, wird sie geschlossen', async () => {
    const { idbGet, IDB_FRIST_MS } = await import('../src/idb.js');
    idbGet('k');
    await vi.advanceTimersByTimeAsync(IDB_FRIST_MS);
    const db = { close: vi.fn() };
    anfragen[0].onsuccess({ target: { result: db } });
    expect(db.close).toHaveBeenCalledTimes(1);
  });

  it('Gegenprobe: antwortet der Browser rechtzeitig, läuft das Lesen wie bisher', async () => {
    const { idbGet } = await import('../src/idb.js');
    const lauf = idbGet('k');
    await Promise.resolve();
    const db = {
      transaction: () => ({ objectStore: () => ({ get: () => {
        const r = { result: 'wert' };
        queueMicrotask(() => r.onsuccess());
        return r;
      } }) }),
    };
    anfragen[0].onsuccess({ target: { result: db } });
    expect(await lauf).toBe('wert');
  });
});
