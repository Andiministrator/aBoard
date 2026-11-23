/* @vitest-environment happy-dom */

/**
 * Karte #375, F-138 — `src/idb.js` cached `dbPromise` dauerhaft.
 *
 * Der Browser kann eine offene IndexedDB-Verbindung jederzeit von sich aus
 * schließen — bei Quota-Eviction (`onclose`) oder wenn ein anderer Tab /
 * eine andere `aboard.html`-Instanz ein Schema-Upgrade verlangt
 * (`onversionchange`). Vor diesem Fix hat NUR der `onerror`-Zweig von
 * `openDB()` den Modul-Cache zurückgesetzt; `onclose`/`onversionchange`
 * liefen ins Leere. Der Cache zeigte danach dauerhaft auf eine tote
 * Connection — jeder weitere `idbPut`/`idbGet`/`idbDelete` schlug (still,
 * idb.js schluckt Fehler) fehl, bis der Tab neu geladen wurde.
 *
 * happy-dom bringt kein echtes IndexedDB mit → `indexedDB.open()` wird
 * durch ein minimales Fake mit steuerbaren onsuccess/onclose/onversion-
 * change-Callbacks ersetzt. `vi.resetModules()` + dynamischer Re-Import
 * pro Test isoliert den modul-internen `dbPromise`-Zustand von idb.js
 * (der sonst über Tests hinweg gecacht bliebe).
 */

import { describe, it, expect, afterEach, vi } from 'vitest';

/* Baut ein Fake von `indexedDB`, das jeden `.open()`-Aufruf protokolliert
   und eine neue Fake-DB liefert (asynchron, wie das echte IDB-Event-Modell).
   Jede Fake-DB unterstützt genug von `transaction().objectStore()`, um
   put/get/delete aus idb.js zu bedienen — die Werte selbst sind für diesen
   Test irrelevant, es geht nur darum, WIE OFT eine neue Verbindung geöffnet
   wird. */
function makeFakeIndexedDB() {
  const dbs = [];

  function makeFakeDB() {
    const db = {
      onclose: null,
      onversionchange: null,
      close: vi.fn(),
      transaction() {
        return {
          objectStore() {
            return {
              put() {
                const req = {};
                Promise.resolve().then(() => req.onsuccess?.({ target: req }));
                return req;
              },
              get() {
                const req = { result: undefined };
                Promise.resolve().then(() => req.onsuccess?.({ target: req }));
                return req;
              },
              delete() {
                const req = {};
                Promise.resolve().then(() => req.onsuccess?.({ target: req }));
                return req;
              },
            };
          },
        };
      },
    };
    return db;
  }

  return {
    fake: {
      open() {
        const req = { onupgradeneeded: null, onsuccess: null, onerror: null };
        const db = makeFakeDB();
        dbs.push(db);
        Promise.resolve().then(() => req.onsuccess?.({ target: { result: db } }));
        return req;
      },
    },
    dbs,
  };
}

describe('F-138 — dbPromise-Cache reagiert auf onclose/onversionchange', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.resetModules();
  });

  it('onclose: nächster Zugriff öffnet eine neue Verbindung statt eine tote weiterzuverwenden', async () => {
    const { fake, dbs } = makeFakeIndexedDB();
    vi.stubGlobal('indexedDB', fake);
    const { idbPut, idbGet } = await import('../src/idb.js');

    await idbPut('k1', 'v1');
    expect(dbs.length).toBe(1);

    /* Der Browser schließt die Verbindung selbst — z. B. Quota-Eviction. */
    dbs[0].onclose();

    await idbGet('k1');
    expect(dbs.length).toBe(2); /* F-138-Fix: Cache war invalidiert → Reopen */
  });

  it('onversionchange: schließt die alte Verbindung UND invalidiert den Cache', async () => {
    const { fake, dbs } = makeFakeIndexedDB();
    vi.stubGlobal('indexedDB', fake);
    const { idbPut } = await import('../src/idb.js');

    await idbPut('k1', 'v1');
    expect(dbs.length).toBe(1);

    /* Ein anderer Tab/eine andere aboard.html-Instanz verlangt ein Upgrade. */
    dbs[0].onversionchange();
    expect(dbs[0].close).toHaveBeenCalled();

    await idbPut('k2', 'v2');
    expect(dbs.length).toBe(2); /* F-138-Fix: Cache war invalidiert → Reopen */
  });

  it('Kontrolle: ohne Event bleibt eine einzige Verbindung über mehrere Calls hinweg gecacht', async () => {
    const { fake, dbs } = makeFakeIndexedDB();
    vi.stubGlobal('indexedDB', fake);
    const { idbPut, idbGet, idbDelete } = await import('../src/idb.js');

    await idbPut('k1', 'v1');
    await idbGet('k1');
    await idbDelete('k1');

    expect(dbs.length).toBe(1); /* Caching-Verhalten für den Normalfall bleibt erhalten */
  });
});
