/* @vitest-environment happy-dom */

/**
 * Karte #836 (ADR-0141 Nachtrag): Admin-Reiter „Board-Paar“ — Spalte
 * „Bearbeitbar“ in der Partnerliste außen. Nur Kopien haben den Schalter
 * (Stufe-1-Zeilen „—“); Umschalten ruft den Admin-Weg, ein Fehlschlag stellt
 * das Häkchen zurück; auf der Seite innen ist er gesperrt.
 *
 * Server-only (`describeServer`): `AdminDialog` gibt es nur im Server-Build.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

let stand;
let scrollSpy;
const setze = vi.fn(async () => ({ ok: true, body: { ok: true } }));

vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listConfig: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    setConfig: vi.fn(async () => ({ ok: true, body: { ok: true } })),
    getBoardpaar: vi.fn(async () => ({ ok: true, body: JSON.parse(JSON.stringify(stand)) })),
    listAllBoards: vi.fn(async () => ({ ok: true, body: { boards: [
      { id: 'b2', title: 'Kopie Frei' }, { id: 'b3', title: 'Stufe Eins' }, { id: 'b5', title: 'Kopie Zu' }] } })),
    koppleBoardpaar: vi.fn(async () => ({ ok: true, body: {} })),
    entkoppleBoardpaar: vi.fn(async () => ({ ok: true, body: {} })),
    loescheBoardpaarKopie: vi.fn(async () => ({ ok: true, body: {} })),
    setBoardpaarBearbeitbar: (...a) => setze(...a),
    createBoardpaarZugang: vi.fn(async () => ({ ok: true, body: {} })),
    revokeBoardpaarZugang: vi.fn(async () => ({ ok: true, body: { ok: true } })),
  },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({ boardpaar: stand.seite }), listKnownBoards: async () => [] }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { I18n } from '../src/i18n.js';
import { vergissBoardpaarSeite } from '../src/boardpaar.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
async function ruhig() { for (let i = 0; i < 6; i++) await ruhe(); }
async function reiter(id) {
  document.querySelector(`.admin-tab[data-section="${id}"]`).click();
  await ruhig();
}
const schalter = () => [...document.querySelectorAll('.admin-boardpaar-bearbeitbar')];
const zeile = (text) => [...document.querySelectorAll('tr')].find(tr => tr.textContent.includes(text));

beforeEach(() => {
  I18n.setLang('de');
  stand = {
    seite: 'aussen',
    innen: [],
    aussen: [
      { board_id: 'b2', innen_link_basis: 'https://i.example/#board=x', seit: '2026-10-10T08:00:00Z', kopie: true, bearbeitbar: true },
      { board_id: 'b3', innen_link_basis: 'https://i.example/#board=y', seit: '2026-10-10T08:00:00Z', kopie: false, bearbeitbar: false },
      { board_id: 'b5', innen_link_basis: 'https://i.example/#board=z', seit: '2026-10-10T08:00:00Z', kopie: true, bearbeitbar: false },
    ],
    kopien_entkoppelt: [],
    zugaenge: [],
  };
  setze.mockClear();
  scrollSpy = vi.spyOn(Element.prototype, 'scrollIntoView').mockImplementation(() => {});
  vergissBoardpaarSeite();
});
afterEach(() => { AdminDialog?.close(); vi.unstubAllGlobals(); scrollSpy.mockRestore(); });

describeServer('#836 Schalter „bearbeitbar“ im Admin-Reiter', () => {
  it('zeigt den Wert je Kopie, Stufe 1 ohne Schalter', async () => {
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    expect(document.body.textContent).toContain(I18n.t('admin.boardpaar.col-bearbeitbar'));
    const s = schalter();
    expect(s.map(c => c.getAttribute('aria-label'))).toEqual([
      I18n.t('admin.boardpaar.aria-bearbeitbar', 'Kopie Frei (b2)'),
      I18n.t('admin.boardpaar.aria-bearbeitbar', 'Kopie Zu (b5)'),
    ]);
    expect(s.map(c => c.checked)).toEqual([true, false]);
    expect(s.every(c => !c.disabled)).toBe(true);
    expect(zeile('Stufe Eins (b3)').querySelector('.admin-boardpaar-bearbeitbar')).toBeNull();
    expect(document.body.textContent).toContain(I18n.t('admin.boardpaar.bearbeitbar-hint'));
  });

  it('Umschalten ruft den Admin-Weg je Board', async () => {
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const zu = schalter()[1];
    zu.checked = true;
    zu.dispatchEvent(new Event('change'));
    await ruhig();
    expect(setze).toHaveBeenCalledWith('b5', true);
    expect(zu.checked).toBe(true);
    expect(zeile('Kopie Zu (b5)').textContent).toContain(I18n.t('admin.msg-saved'));
  });

  it('Fehlschlag stellt das Häkchen zurück und nennt den Grund', async () => {
    setze.mockImplementationOnce(async () => ({ ok: false, status: 409, body: { error: 'keine Kopie' } }));
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const frei = schalter()[0];
    frei.checked = false;
    frei.dispatchEvent(new Event('change'));
    await ruhig();
    expect(setze).toHaveBeenCalledWith('b2', false);
    expect(frei.checked).toBe(true);
    expect(zeile('Kopie Frei (b2)').textContent).toContain('keine Kopie');
  });

  it('auf der Seite innen ist der Schalter gesperrt', async () => {
    stand.seite = 'innen';
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    expect(schalter().length).toBe(2);
    expect(schalter().every(c => c.disabled)).toBe(true);
  });
});
