/* @vitest-environment happy-dom */

/**
 * F-3106 (Karte #1179): Antwortet Chromium auf `queryPermission` eines gespeicherten Ordner-Handles nie,
 * blieb der Start still auf einer leeren Seite stehen (`main.js` wartet vor dem ersten Rendern auf
 * `Sync.init` → `tryRestoreHandle`). Jetzt gilt nach `FREIGABE_FRIST_MS` „Freigabe nötig“, und
 * `isFreigabeHaengend()` sagt dem Start, dass er „Ordner neu wählen“ anbieten soll.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

/* Der gespeicherte Handle dieses Deployments; `'haengt'` = Lese-Transaktion antwortet nie,
   `'frist'` = openDB-Frist abgelaufen (idb.js wirft im strengen Modus), `'kaputt'` = anderer IDB-Fehler. */
let gespeichert = null;
const geschrieben = [];
vi.mock('../src/idb.js', () => ({
  idbPut:    async (k, v) => { geschrieben.push(['put', v]); },
  idbGet:    async (k, opt) => {
    if (gespeichert === 'haengt') return new Promise(() => {});
    if (gespeichert === 'frist') { if (opt?.streng) throw Object.assign(new Error('Frist'), { idbFrist: true }); return null; }
    if (gespeichert === 'kaputt') { if (opt?.streng) return null; return null; }
    return gespeichert;
  },
  idbDelete: async () => { geschrieben.push(['delete']); },
  idbListWithPrefix: async () => [],
}));

const { Sync, FREIGABE_FRIST_MS, LADE_FRIST_MS } = await import('../src/sync.js');

function handle(antwort) {
  return {
    name: 'projekt', kind: 'directory',
    queryPermission: vi.fn(() => (antwort === 'nie' ? new Promise(() => {}) : Promise.resolve(antwort))),
    requestPermission: vi.fn(async () => 'granted'),
  };
}
beforeEach(() => {
  vi.useFakeTimers();
  gespeichert = null; geschrieben.length = 0;
  Sync.__testing__._reset();
});
afterEach(() => { vi.useRealTimers(); });

describe('F-3106 — Frist beim Wiederherstellen des Ordner-Handles', () => {
  it('queryPermission antwortet nie → nach der Frist „Freigabe nötig“ mit Hängt-Kennzeichen, nicht aktiv', async () => {
    const h = handle('nie');
    gespeichert = h;
    const lauf = Sync.tryRestoreHandle();
    let fertig = false; lauf.then(() => { fertig = true; });
    await vi.advanceTimersByTimeAsync(FREIGABE_FRIST_MS - 1);
    expect(fertig).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(await lauf).toBe(true);
    expect(h.queryPermission).toHaveBeenCalledTimes(1);
    expect(Sync.isPermissionNeeded()).toBe(true);
    expect(Sync.isFreigabeHaengend()).toBe(true);
    expect(Sync.isActive()).toBe(false);
  });

  it('Gegenprobe: eine Antwort vor der Frist bleibt wie bisher (granted → aktiv, prompt → ohne Hängt-Kennzeichen)', async () => {
    for (const [antwort, aktiv] of [['granted', true], ['prompt', false]]) {
      Sync.__testing__._reset();
      const h = handle(antwort);
      gespeichert = h;
      expect(await Sync.tryRestoreHandle()).toBe(true);
      expect(Sync.isActive()).toBe(aktiv);
      expect(Sync.isFreigabeHaengend()).toBe(false);
      }
  });

  /* Verify B1: „IndexedDB antwortet nicht“ ist nicht „kein Ordner gespeichert“ — sonst still ein leeres Board. */
  it('IndexedDB-Lesen hängt → nach der Ladefrist „Ordner neu wählen“, kein Handle, nicht aktiv', async () => {
    gespeichert = 'haengt';
    const lauf = Sync.tryRestoreHandle();
    let fertig = false; lauf.then(() => { fertig = true; });
    await vi.advanceTimersByTimeAsync(LADE_FRIST_MS - 1);
    expect(fertig).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(await lauf).toBe(true);
    expect(Sync.isFreigabeHaengend()).toBe(true);
    expect(Sync.isPermissionNeeded()).toBe(true);
    expect(Sync.isActive()).toBe(false);
    expect(Sync.getDirectoryName()).toBe(null);
    expect(await Sync.requestPermission()).toBe(false);
  });

  it('openDB-Frist (idb.js wirft im strengen Modus) → ebenso „Ordner neu wählen“', async () => {
    gespeichert = 'frist';
    expect(await Sync.tryRestoreHandle()).toBe(true);
    expect(Sync.isFreigabeHaengend()).toBe(true);
  });

  it('Gegenprobe: nichts gespeichert → wie bisher „kein Handle“ ohne Kennzeichen', async () => {
    for (const fall of [null, 'kaputt']) {
      Sync.__testing__._reset();
      gespeichert = fall;
      expect(await Sync.tryRestoreHandle()).toBe(false);
      expect(Sync.isFreigabeHaengend()).toBe(false);
      expect(Sync.isPermissionNeeded()).toBe(false);
    }
  });

  it('Ordner neu wählen nimmt das Kennzeichen, der Revert stellt es wieder her; clearActiveHandle löscht es', async () => {
    const alt = handle('nie');
    gespeichert = alt;
    const lauf = Sync.tryRestoreHandle();
    await vi.advanceTimersByTimeAsync(FREIGABE_FRIST_MS);
    await lauf;
    const neu = handle('granted'); neu.name = 'neu';
    window.showDirectoryPicker = async () => neu;
    try {
      expect(await Sync.pickDirectory()).toBe('neu');
      expect(Sync.isFreigabeHaengend()).toBe(false);
      expect(Sync.isActive()).toBe(true);
      await Sync.revertPickedDirectory();
      expect(Sync.isFreigabeHaengend()).toBe(true);
      expect(Sync.isActive()).toBe(false);
      Sync.clearActiveHandle();
      expect(Sync.isFreigabeHaengend()).toBe(false);
    } finally { delete window.showDirectoryPicker; }
  });

  it('Verify B3: hängt das Lesen für den Schnappschuss, lässt der Revert den IndexedDB-Eintrag in Ruhe', async () => {
    gespeichert = 'haengt';
    const neu = handle('granted'); neu.name = 'neu';
    window.showDirectoryPicker = async () => neu;
    try {
      const pick = Sync.pickDirectory();
      await vi.advanceTimersByTimeAsync(FREIGABE_FRIST_MS);
      expect(await pick).toBe('neu');
      geschrieben.length = 0;
      await Sync.revertPickedDirectory();
      expect(geschrieben).toEqual([]); /* weder überschrieben noch gelöscht */
    } finally { delete window.showDirectoryPicker; }
  });

  it('ein erfolgreiches Erlauben nimmt das Hängt-Kennzeichen zurück', async () => {
    const h = handle('nie');
    gespeichert = h;
    const lauf = Sync.tryRestoreHandle();
    await vi.advanceTimersByTimeAsync(FREIGABE_FRIST_MS);
    await lauf;
    expect(Sync.isFreigabeHaengend()).toBe(true);
    expect(await Sync.requestPermission()).toBe(true);
    expect(Sync.isFreigabeHaengend()).toBe(false);
  });

  it('der Start zeigt bei hängender Freigabe „Ordner neu wählen“ statt „Erlauben“', () => {
    const main = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8');
    expect(main).toMatch(/showPermissionBanner\(Sync\.isFreigabeHaengend\?\.\(\)\)/);
    const banner = main.slice(main.indexOf('function showPermissionBanner('), main.indexOf('function showPermissionBanner(') + 1600);
    expect(banner).toMatch(/haengt \? 'sync\.pick-again' : 'sync\.grant-permission'/);
    expect(banner).toMatch(/haengt \? 'sync:pick-directory' : 'sync:request-permission'/);
    /* Verify B2: das Banner bleibt bis zur echten Verbindung stehen */
    /* Sitzungs-Kritik 23.11. A B-1: auch „Erlauben“ entfernt das Banner nicht beim Klick */
    const klick = banner.slice(banner.indexOf("btn.addEventListener('click'"), banner.indexOf('const weg'));
    expect(klick).not.toMatch(/banner\.remove\(\)/);
    expect(banner).toMatch(/EventBus\.on\('config:project-changed', weg\)/);
  });
});
