/* @vitest-environment happy-dom */

/**
 * Karte #375, F-139 — `unwrap()` in `src/storage.js` war nicht idempotent.
 *
 * Seit F-104 (Sprint 17) wird ein Save-File-Handle als Wrapper
 * `{ handle, title }` in IndexedDB persistiert. Ein Doppel-Wrap
 * `{ handle: { handle: rawHandle, title: 'Alt' }, title: 'Neu' }` — z. B.
 * wenn ein bereits gewrapptes Objekt versehentlich erneut an
 * `storeFileHandle()` gereicht wird — lieferte vor dem Fix als `.handle`
 * selbst wieder einen Wrapper statt eines echten FileSystemHandle. Jeder
 * folgende `queryPermission()`-Aufruf (`openByBoardId`) warf dann, weil
 * der Wrapper diese Methode nicht hat; `openByBoardId` fängt das intern ab
 * und liefert lautlos `null` — ein bekanntes Board ließ sich nicht mehr
 * öffnen, ohne dass ein Fehler sichtbar wurde.
 *
 * `unwrap` ist nicht exportiert (Modul-intern) → Test über die öffentliche
 * Storage-API (`listKnownFiles`, `openByBoardId`), analog zu
 * tests/sync-handle-revert.test.js: idb.js wird mit einer In-Memory-Map
 * gemockt, damit ein fehlender Rückschreib-/Lesepfad nicht mit „IndexedDB
 * tut in happy-dom sowieso nichts" verwechselt werden kann.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';

const store = new Map();
vi.mock('../src/idb.js', () => ({
  idbPut:    async (k, v) => { store.set(k, v); },
  idbGet:    async (k)    => (store.has(k) ? store.get(k) : null),
  idbDelete: async (k)    => { store.delete(k); },
  /* Spiegelt die Präfix-Filterlogik des echten idb.js (siehe src/idb.js
     idbListWithPrefix) — für listKnownFiles() unverzichtbar. */
  idbListWithPrefix: async (prefix) => {
    const result = [];
    for (const [key, value] of store) {
      if (key.startsWith(prefix)) result.push({ id: key.slice(prefix.length), value });
    }
    return result;
  },
}));

const { Storage } = await import('../src/storage.js');

/* Der Save-File-Key, unter dem storage.js ablegt (KEY_PREFIX = 'saveFile-',
   ADR-0028) — modul-intern, hier bewusst gespiegelt wie 'projectDir@/' in
   tests/sync-handle-revert.test.js. */
function keyFor(boardId) { return `saveFile-${boardId}`; }

function fakeFileHandle(name, perm = 'granted') {
  return {
    name, kind: 'file',
    async queryPermission()   { return perm; },
    async requestPermission() { return perm; },
    async getFile() {
      return {
        name,
        text: async () => JSON.stringify({
          version: '2',
          board: { id: 'b1', title: 'Board-Titel', columnDefs: [], sprints: [] },
        }),
      };
    },
  };
}

beforeEach(() => { store.clear(); });

describe('F-139 — unwrap() erkennt Mehrfach-Wraps', () => {
  it('einfacher Wrap (Normalfall F-104): Name + Titel korrekt', async () => {
    const raw = fakeFileHandle('einfach.html');
    store.set(keyFor('b1'), { handle: raw, title: 'Mein Board' });

    const list = await Storage.listKnownFiles();
    expect(list).toEqual([{ id: 'b1', name: 'einfach.html', title: 'Mein Board' }]);
  });

  it('Legacy-Eintrag ohne Wrapper (raw FileSystemFileHandle, kind==="file")', async () => {
    const raw = fakeFileHandle('legacy.html');
    store.set(keyFor('b1'), raw);

    const list = await Storage.listKnownFiles();
    expect(list).toEqual([{ id: 'b1', name: 'legacy.html', title: null }]);
  });

  it('Doppel-Wrap: listKnownFiles zieht Name vom echten Handle, nicht vom inneren Wrapper', async () => {
    const raw = fakeFileHandle('doppelt-gewrappt.html');
    store.set(keyFor('b1'), { handle: { handle: raw, title: 'Alt' }, title: 'Neu' });

    const list = await Storage.listKnownFiles();
    /* Vor dem Fix: handle?.name war undefined (handle = innerer Wrapper),
       Fallback auf id.slice(0,8) → 'b1'. Mit dem Fix zeigt der Name auf
       den echten Handle, und der äußerste (jüngste) Titel gewinnt. */
    expect(list).toEqual([{ id: 'b1', name: 'doppelt-gewrappt.html', title: 'Neu' }]);
  });

  it('Doppel-Wrap: openByBoardId gelingt weiterhin — queryPermission wirft nicht mehr', async () => {
    const raw = fakeFileHandle('doppelt.html');
    store.set(keyFor('b1'), { handle: { handle: raw, title: 'Alt' }, title: 'Neu' });

    const state = await Storage.openByBoardId('b1');

    /* Vor dem Fix: handle.queryPermission war keine Funktion (handle = innerer
       Wrapper) → TypeError, von openByBoardId lautlos zu `null` geschluckt. */
    expect(state).not.toBeNull();
    expect(state.board.id).toBe('b1');
  });

  it('dreifacher Wrap: auch mehrfach verschachtelt bleibt der echte Handle auffindbar', async () => {
    const raw = fakeFileHandle('dreifach.html');
    store.set(keyFor('b1'), { handle: { handle: { handle: raw, title: 'ganz-innen' }, title: 'mitte' }, title: 'aussen' });

    const list = await Storage.listKnownFiles();
    expect(list).toEqual([{ id: 'b1', name: 'dreifach.html', title: 'aussen' }]);
  });
});

/* ── Sprint-51-Kritiker: K6, V2 ─────────────────────────────────────── */

describe('K6/V2 — Härtung gegen korrupte Wrapper', () => {
  it('K6: zyklischer Eintrag friert nicht ein, sondern liefert kein Handle', async () => {
    const zyklus = { title: 'Zyklus' };
    zyklus.handle = zyklus;                       /* Structured Clone erlaubt das */
    store.set(keyFor('b1'), zyklus);

    /* Ohne Tiefenbegrenzung liefe die Schleife endlos — der Tab würde einfrieren. */
    const list = await Storage.listKnownFiles();

    expect(list).toEqual([{ id: 'b1', name: 'b1', title: 'Zyklus' }]);
  });

  it('V2: Eintrag mit title, aber ohne handle behält seinen Titel', async () => {
    store.set(keyFor('b1'), { title: 'Nur ein Titel' });

    const list = await Storage.listKnownFiles();

    /* Die erste Fassung der Schleife verlor den Titel hier — die alte,
       nicht-idempotente Implementierung hatte ihn noch geliefert. */
    expect(list).toEqual([{ id: 'b1', name: 'b1', title: 'Nur ein Titel' }]);
  });

  it('zu tief verschachtelt: bricht ab, statt zu hängen', async () => {
    let wert = fakeFileHandle('ganz-tief.html');
    for (let i = 0; i < 12; i++) wert = { handle: wert, title: `ebene-${i}` };
    store.set(keyFor('b1'), wert);

    const list = await Storage.listKnownFiles();

    /* Kein Handle mehr auffindbar (Grenze 8), aber definiert und schnell. */
    expect(list[0].id).toBe('b1');
    expect(list[0].title).toBe('ebene-11');
  });
});
