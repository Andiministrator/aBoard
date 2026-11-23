/* @vitest-environment happy-dom */

/**
 * Karte #741: Kachelansicht der Board-Übersicht mit Vorschaubild.
 * 200 → Bild als Hintergrund der Kachel, 204 → Grundfläche, Fehler → Grundfläche.
 * Läuft nur im Server-Build-Kontext (`describeServer`, ADR-0116).
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';

let boardsAntwort = [];
let mitVorschau = true;

vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      listKnownBoards: async () => boardsAntwort,
      ...(mitVorschau ? { boardVorschauUrl: id => `/api/boards/${id}/vorschaubild` } : {}),
    }),
  },
}));
vi.mock('../src/state.js', () => ({ State: { get: () => ({ board: { id: 'b-bild' } }) } }));

import { BoardsView } from '../src/boards-view.js';
import { describeServer } from './helpers/build-flags.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const kachel = name => [...document.querySelectorAll('.bv-panel .bv-row')].find(z => z.textContent.includes(name));

describeServer('BoardsView — Kachelansicht mit Vorschaubild (#741)', () => {
  let abrufe;
  beforeEach(() => {
    document.body.innerHTML = '';
    mitVorschau = true;
    boardsAntwort = [
      { id: 'b-bild', name: 'Mit Bild', role: 'owner', updatedAt: 2 },
      { id: 'b-leer', name: 'Ohne Bild', role: 'owner', updatedAt: 1 },
      { id: 'b-kaputt', name: 'Kaputt', role: 'viewer', updatedAt: 0 },
    ];
    abrufe = [];
    vi.stubGlobal('fetch', vi.fn(async url => {
      abrufe.push(url);
      if (url.includes('b-bild')) return { status: 200, blob: async () => new Blob(['jpeg']) };
      if (url.includes('b-leer')) return { status: 204, blob: async () => new Blob([]) };
      throw new Error('netz weg');
    }));
    vi.spyOn(URL, 'createObjectURL').mockImplementation(() => 'blob:vorschau-1');
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
  });
  afterEach(() => { BoardsView?.close(); document.body.innerHTML = ''; vi.unstubAllGlobals(); vi.restoreAllMocks(); });

  it('jede Kachel hat einen Titel-Knopf mit dem Board-Namen und lädt ihr Vorschaubild', async () => {
    BoardsView.open();
    await ruhe(); await ruhe();
    expect(abrufe.sort()).toEqual([
      '/api/boards/b-bild/vorschaubild', '/api/boards/b-kaputt/vorschaubild', '/api/boards/b-leer/vorschaubild',
    ]);
    expect(kachel('Mit Bild').querySelector('.bv-title').style.backgroundImage).toContain('blob:vorschau-1');
  });

  it('204 und Fehler lassen die Grundfläche stehen', async () => {
    BoardsView.open();
    await ruhe(); await ruhe();
    expect(kachel('Ohne Bild').querySelector('.bv-title').style.backgroundImage).toBe('');
    expect(kachel('Kaputt').querySelector('.bv-title').style.backgroundImage).toBe('');
  });

  it('die geöffnete Kachel ist markiert, die anderen nicht', async () => {
    BoardsView.open();
    await ruhe();
    expect(kachel('Mit Bild').classList.contains('bv-row--current')).toBe(true);
    expect(kachel('Ohne Bild').classList.contains('bv-row--current')).toBe(false);
  });

  it('Schließen gibt die Objekt-URLs frei', async () => {
    BoardsView.open();
    await ruhe(); await ruhe();
    BoardsView.close();
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:vorschau-1');
  });

  it('K-7: der volle Name steht im Tooltip des Titel-Knopfs', async () => {
    BoardsView.open();
    await ruhe();
    expect(kachel('Ohne Bild').querySelector('.bv-title').getAttribute('title')).toBe('Ohne Bild');
  });

  it('K-8: Escape schließt über close() — Objekt-URLs werden sofort frei', async () => {
    BoardsView.open();
    await ruhe(); await ruhe();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(document.querySelector('.bv-overlay').classList.contains('modal-overlay--open')).toBe(false);
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:vorschau-1');
  });

  it('ohne boardVorschauUrl (anderes Plugin) wird nichts abgerufen', async () => {
    mitVorschau = false;
    BoardsView.open();
    await ruhe(); await ruhe();
    expect(abrufe).toEqual([]);
    expect(document.querySelectorAll('.bv-panel .bv-row').length).toBe(3);
  });
});
