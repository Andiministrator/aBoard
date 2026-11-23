/* @vitest-environment happy-dom */

/**
 * DOM-Tests für src/boards-view.js (#624) — Board-Übersicht mit kopierbarer
 * Board-ID. Läuft nur im Server-Build-Kontext (`describeServer`, ADR-0116).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

let boardsAntwort = [];
let wirft = false;

vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      listKnownBoards: async () => { if (wirft) throw new Error('x'); return boardsAntwort; },
    }),
  },
}));
vi.mock('../src/state.js', () => ({ State: { get: () => ({ board: { id: 'b-aktuell' } }) } }));

import { BoardsView } from '../src/boards-view.js';
import { EventBus } from '../src/eventbus.js';
import { I18n } from '../src/i18n.js';
import { describeServer } from './helpers/build-flags.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const body = () => document.querySelector('.bv-panel .modal-body');

const BOARDS = [
  { id: 'b-eigen', name: 'Mein Board', role: 'owner', updatedAt: 1760000000000 },
  { id: 'b-aktuell', name: 'Offenes Board', role: 'owner', updatedAt: 1750000000000 },
  { id: 'b-fremd', name: 'Geteiltes Board', role: 'viewer', updatedAt: 1770000000000 },
];

describeServer('BoardsView — Board-Übersicht (#624)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    boardsAntwort = BOARDS.map(b => ({ ...b }));
    wirft = false;
  });
  afterEach(() => { BoardsView?.close(); document.body.innerHTML = ''; vi.restoreAllMocks(); });

  it('rendert die Gruppen „Eigene“ und „Mit mir geteilt“ mit Rolle und ID', async () => {
    BoardsView.open();
    await ruhe();
    const gruppen = [...body().querySelectorAll('.bv-group')].map(g => g.textContent);
    expect(gruppen).toEqual([I18n.t('board.switch.own-boards'), I18n.t('board.switch.shared-boards')]);
    const zeilen = body().querySelectorAll('.bv-row');
    expect(zeilen.length).toBe(3);
    const fremd = [...zeilen].find(z => z.textContent.includes('Geteiltes Board'));
    expect(fremd.querySelector('.bv-meta').textContent).toContain(I18n.t('share.role-viewer'));
    expect(fremd.querySelector('input.bv-id').value).toBe('b-fremd');
    const offen = [...zeilen].find(z => z.textContent.includes('Offenes Board'));
    expect(offen.querySelector('.bv-meta').textContent).toContain(I18n.t('boards-view.current'));
  });

  it('ohne Rollen (Legacy-Bypass) eine flache Liste', async () => {
    boardsAntwort = [{ id: 'default', name: 'Standard' }];
    BoardsView.open();
    await ruhe();
    expect([...body().querySelectorAll('.bv-group')].map(g => g.textContent)).toEqual([I18n.t('board.switch.server-boards')]);
  });

  it('Kopieren schreibt die ID in die Zwischenablage und meldet Erfolg', async () => {
    const schreibe = vi.fn(async () => {});
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: schreibe }, configurable: true });
    BoardsView.open();
    await ruhe();
    const zeile = [...body().querySelectorAll('.bv-row')].find(z => z.textContent.includes('Mein Board'));
    zeile.querySelector('button.bv-btn').click();
    await ruhe();
    expect(schreibe).toHaveBeenCalledWith('b-eigen');
    const msg = zeile.querySelector('.bv-msg');
    expect(msg.textContent).toBe(I18n.t('boards-view.copied'));
    expect(msg.getAttribute('role')).toBe('status');
  });

  it('verweigerte Zwischenablage: Fehlermeldung (role=alert), Knopf rot, ID bleibt sichtbar', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: async () => { throw new Error('verweigert'); } }, configurable: true,
    });
    BoardsView.open();
    await ruhe();
    const zeile = [...body().querySelectorAll('.bv-row')].find(z => z.textContent.includes('Mein Board'));
    const btn = zeile.querySelector('button.bv-btn');
    btn.click();
    await ruhe();
    const msg = zeile.querySelector('.bv-msg');
    expect(msg.getAttribute('role')).toBe('alert');
    expect(msg.textContent).toBe(I18n.t('boards-view.copy-failed'));
    expect(btn.classList.contains('bv-btn--err')).toBe(true);
    expect(zeile.querySelector('input.bv-id').value).toBe('b-eigen');
  });

  it('Klick auf den Titel schließt den Dialog und wechselt das Board', async () => {
    const gehoert = vi.fn();
    EventBus.on('sync:switch-to-board', gehoert);
    BoardsView.open();
    await ruhe();
    const zeile = [...body().querySelectorAll('.bv-row')].find(z => z.textContent.includes('Geteiltes Board'));
    zeile.querySelector('button.bv-title').click();
    expect(gehoert).toHaveBeenCalledWith({ boardId: 'b-fremd' });
    expect(document.querySelector('.bv-overlay').classList.contains('modal-overlay--open')).toBe(false);
  });

  it('leere Liste und Ladefehler zeigen einen Text statt einer leeren Fläche', async () => {
    boardsAntwort = [];
    BoardsView.open();
    await ruhe();
    expect(body().textContent).toContain(I18n.t('boards-view.empty'));
    wirft = true;
    BoardsView.close();
    BoardsView.open();
    await ruhe();
    expect(body().querySelector('[role="alert"]').textContent).toBe(I18n.t('boards-view.error'));
  });
});
