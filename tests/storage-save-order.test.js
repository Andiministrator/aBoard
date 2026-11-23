/* @vitest-environment happy-dom */

/**
 * F-476 (Sprint-51-Kritiker, D4) — „erst abreissen, dann prüfen" in
 * `Storage.save()`.
 *
 * Im Zweig „kein gültiger Handle → Datei-Picker" setzte `save()` bis Sprint 61
 * `fileHandle`, `activeBoardId` UND den IndexedDB-Eintrag, BEVOR das erste
 * `createWritable()/write()` überhaupt lief. Schlug das Schreiben fehl, setzte
 * der catch zwar `fileHandle = null`, der IndexedDB-Eintrag blieb aber stehen —
 * das Switch-Popover listete danach ein Board, dessen Datei nie geschrieben
 * wurde (Geistereintrag). Dieselbe Musterklasse wie F-432, nur mit milderer
 * Folge: kein Datenverlust, aber ein Eintrag, der auf nichts zeigt.
 *
 * Der IndexedDB-Zugriff wird — wie in tests/storage-handle-unwrap.test.js und
 * tests/sync-handle-revert.test.js — durch eine In-Memory-Map ersetzt, damit
 * „kein Eintrag" nicht mit „IndexedDB tut in happy-dom sowieso nichts"
 * verwechselt werden kann: der Erfolgsfall unten weist nach, dass derselbe
 * Pfad sehr wohl schreibt.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';

const store = new Map();
vi.mock('../src/idb.js', () => ({
  idbPut:    async (k, v) => { store.set(k, v); },
  idbGet:    async (k)    => (store.has(k) ? store.get(k) : null),
  idbDelete: async (k)    => { store.delete(k); },
  idbListWithPrefix: async (prefix) => {
    const result = [];
    for (const [key, value] of store) {
      if (key.startsWith(prefix)) result.push({ id: key.slice(prefix.length), value });
    }
    return result;
  },
}));

const { Storage } = await import('../src/storage.js');

/* KEY_PREFIX aus src/storage.js (ADR-0028) — modul-intern, hier gespiegelt. */
function keyFor(boardId) { return `saveFile-${boardId}`; }

function makeState() {
  return {
    version: 1,
    board: { id: 'board-f476', title: 'F-476-Board', columnDefs: [], sprints: [] },
  };
}

/* save() klont `document.documentElement` und schreibt in `#app-data` — das
   Element muss existieren, sonst wirft der Klon-Pfad vor dem eigentlichen Test. */
beforeEach(() => {
  store.clear();
  document.body.innerHTML = '<div id="app-data"></div>';
});

describe('F-476 — Storage.save() persistiert den Handle erst nach dem Schreiben', () => {
  it('Schreibfehler im Picker-Zweig hinterlässt KEINEN IndexedDB-Eintrag', async () => {
    const kaputterHandle = {
      name: 'aboard.html', kind: 'file',
      createWritable: async () => { throw new Error('Quota überschritten'); },
    };
    window.showSaveFilePicker = vi.fn(async () => kaputterHandle);

    await expect(Storage.save(makeState())).rejects.toThrow('Quota überschritten');

    /* Vor dem Fix stand hier der Wrapper { handle, title } — das Switch-
       Popover hätte ein nie geschriebenes Board angeboten. */
    expect(store.has(keyFor('board-f476'))).toBe(false);
  });

  it('Erfolgsfall schreibt den Eintrag weiterhin (Gegenprobe zur Zeile darüber)', async () => {
    const geschrieben = [];
    const guterHandle = {
      name: 'aboard.html', kind: 'file',
      createWritable: async () => ({
        write: async (b) => { geschrieben.push(b); },
        close: async () => {},
      }),
    };
    window.showSaveFilePicker = vi.fn(async () => guterHandle);

    const res = await Storage.save(makeState());

    expect(res).toEqual({ ok: true, method: 'fsa' });
    expect(geschrieben.length).toBe(1);
    const eintrag = store.get(keyFor('board-f476'));
    expect(eintrag).toBeTruthy();
    expect(eintrag.title).toBe('F-476-Board');
  });

  it('Abbruch im Picker (AbortError) schreibt ebenfalls nichts', async () => {
    const abbruch = new Error('abgebrochen');
    abbruch.name = 'AbortError';
    window.showSaveFilePicker = vi.fn(async () => { throw abbruch; });

    const res = await Storage.save(makeState());

    expect(res).toEqual({ ok: false });
    expect(store.size).toBe(0);
  });
});
