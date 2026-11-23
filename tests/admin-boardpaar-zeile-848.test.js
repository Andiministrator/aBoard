/* @vitest-environment happy-dom */

/**
 * Karte #848: abgeschlossene Board-Paar-Zugänge aus der Tabelle entfernen.
 * Nur widerrufene und abgelöste Zeilen (Übergangszeit vorbei) tragen
 * „Entfernen“; geltende und erneuerte in der Übergangszeit nicht. Ein Knopf
 * „Abgeschlossene entfernen (N)“ räumt alle ab. Ein 409 des Servers (gilt
 * noch) und ein 503 (Uhr nicht gestellt) stehen lesbar an der Zeile.
 *
 * Server-only (`describeServer`): `AdminDialog` gibt es nur im Server-Build.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

let stand;
const entferne = vi.fn(async () => ({ ok: true, status: 200, body: { ok: true } }));
const raeume = vi.fn(async () => ({ ok: true, status: 200, body: { entfernt: 2, uebersprungen: 0 } }));
const lade = vi.fn(async () => ({ ok: true, body: JSON.parse(JSON.stringify(stand)) }));

vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listConfig: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    getBoardpaar: (...a) => lade(...a),
    listAllBoards: vi.fn(async () => ({ ok: true, body: { boards: [] } })),
    revokeBoardpaarZugang: vi.fn(async () => ({ ok: true, body: { ok: true } })),
    entferneBoardpaarZugang: (...a) => entferne(...a),
    raeumeBoardpaarZugaengeAuf: (...a) => raeume(...a),
  },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({}), listKnownBoards: async () => [] }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { I18n } from '../src/i18n.js';
import { vergissBoardpaarSeite } from '../src/boardpaar.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
async function ruhig() { for (let i = 0; i < 4; i++) await ruhe(); }
async function oeffne() {
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(new Date('2026-10-11T09:05:00Z'));
  AdminDialog.open();
  await ruhe();
  document.querySelector('.admin-tab[data-section="boardpaar"]').click();
  await ruhig();
  vi.useRealTimers();
}
const zeilen = () => [...document.querySelectorAll('.admin-audit-table tbody tr')];
const knopf = (tr, text) => [...tr.querySelectorAll('button')].find(b => b.textContent === text);
const aufraeumKnopf = () => [...document.querySelectorAll('.admin-body button')].find(b => b.textContent.startsWith('Abgeschlossene entfernen'));

beforeEach(() => {
  I18n.setLang('de');
  stand = {
    seite: 'innen', innen: [], aussen: [],
    zugaenge: [
      { id: 'z2', typ: 'abs', name: 'push', created_at: '2026-10-11T09:00:00.1Z', erneuert_aus: 'z1', erneuert_von: '192.0.2.7' },
      { id: 'z1', typ: 'abs', name: 'push', created_at: '2026-10-04T09:00:00Z', gilt_bis: '2026-10-11T09:10:00.100000000Z' },
      { id: 'z0', typ: 'abe', name: 'alt', created_at: '2026-10-01T09:00:00Z', revoked_at: '2026-10-02T09:00:00Z' },
      { id: 'zx', typ: 'abs', name: 'push', created_at: '2026-09-27T09:00:00Z', gilt_bis: '2026-10-04T09:10:00.000000000Z' },
    ],
  };
  [entferne, raeume, lade].forEach(f => f.mockClear());
  vi.stubGlobal('confirm', vi.fn(() => true));
  vergissBoardpaarSeite();
});
afterEach(() => { AdminDialog?.close(); vi.unstubAllGlobals(); });

describeServer('#848 abgeschlossene Zugänge entfernen', () => {
  it('nur widerrufene und abgelöste Zeilen tragen „Entfernen“; der Sammelknopf zählt sie', async () => {
    await oeffne();
    const z = zeilen();
    expect(z).toHaveLength(4);
    expect(z.map(tr => !!knopf(tr, 'Entfernen'))).toEqual([false, false, true, true]);
    expect(aufraeumKnopf()?.textContent).toBe('Abgeschlossene entfernen (2)');
  });

  it('Entfernen fragt nach, schickt die ID und lädt neu', async () => {
    await oeffne();
    const vorher = lade.mock.calls.length;
    knopf(zeilen()[2], 'Entfernen').click();
    await ruhig();
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('abe_ alt'));
    expect(entferne).toHaveBeenCalledWith('z0');
    expect(lade.mock.calls.length).toBeGreaterThan(vorher);
  });

  it('Abbrechen schickt nichts', async () => {
    confirm.mockReturnValue(false);
    await oeffne();
    knopf(zeilen()[3], 'Entfernen').click();
    await ruhig();
    expect(entferne).not.toHaveBeenCalled();
  });

  it('409 (gilt noch) und 503 (Uhr) stehen lesbar an der Zeile', async () => {
    await oeffne();
    entferne.mockResolvedValueOnce({ ok: false, status: 409, body: {} });
    knopf(zeilen()[3], 'Entfernen').click();
    await ruhig();
    expect(zeilen()[3].textContent).toContain(I18n.t('admin.boardpaar.entfernen-gilt-noch'));
    entferne.mockResolvedValueOnce({ ok: false, status: 503, body: {} });
    knopf(zeilen()[2], 'Entfernen').click();
    await ruhig();
    expect(zeilen()[2].textContent).toContain(I18n.t('admin.boardpaar.entfernen-uhr'));
  });

  it('Sammelknopf räumt ab; Übersprungenes steht nach dem Neuaufbau als Hinweis da', async () => {
    await oeffne();
    raeume.mockResolvedValueOnce({ ok: true, status: 200, body: { entfernt: 1, uebersprungen: 1 } });
    aufraeumKnopf().click();
    await ruhig();
    expect(raeume).toHaveBeenCalledTimes(1);
    expect(document.querySelector('.admin-body').textContent).toContain('1 entfernt, 1 übersprungen');
  });

  it('F-2324: unlesbares gilt_bis gilt nicht als abgelöst — Widerrufen statt Entfernen', async () => {
    stand.zugaenge = [{ id: 'zk', typ: 'abs', name: 'kaputt', created_at: '2026-10-01T09:00:00Z', gilt_bis: 'kein-datum' }];
    await oeffne();
    const [tr] = zeilen();
    expect(knopf(tr, 'Entfernen')).toBeUndefined();
    expect(knopf(tr, 'Widerrufen')).toBeTruthy();
    expect(aufraeumKnopf()).toBeUndefined();
  });

  it('F-2329: Fehler beim Aufräumen lädt neu und nennt den Fehler', async () => {
    await oeffne();
    const vorher = lade.mock.calls.length;
    raeume.mockResolvedValueOnce({ ok: false, status: 500, body: {} });
    aufraeumKnopf().click();
    await ruhig();
    expect(lade.mock.calls.length).toBeGreaterThan(vorher);
    expect(document.querySelector('.admin-aufraeum-hinweis')?.textContent).toBe(I18n.t('admin.error-server'));
  });

  it('Verify A-1: auch vor einem Ladefehler genannt', async () => {
    await oeffne();
    raeume.mockResolvedValueOnce({ ok: false, status: 500, body: {} });
    lade.mockResolvedValueOnce({ ok: false, status: 503, body: {} });
    aufraeumKnopf().click();
    await ruhig();
    expect(document.querySelector('.admin-aufraeum-hinweis')?.textContent).toBe(I18n.t('admin.error-server'));
  });

  it('Verify A-1: Antwort nach Reiterwechsel oder Schließen hinterlässt keinen Hinweis', async () => {
    for (const weg of ['reiter', 'schliessen']) {
      await oeffne();
      let antwort;
      raeume.mockImplementationOnce(() => new Promise((res) => { antwort = res; }));
      aufraeumKnopf().click();
      await ruhe();
      if (weg === 'reiter') document.querySelector('.admin-tab[data-section="status"]').click();
      else AdminDialog.close();
      antwort({ ok: false, status: 500, body: {} });
      await ruhig();
      if (weg === 'schliessen') { AdminDialog.open(); await ruhe(); }
      document.querySelector('.admin-tab[data-section="boardpaar"]').click();
      await ruhig();
      expect(document.querySelector('.admin-aufraeum-hinweis'), weg).toBeNull();
      AdminDialog.close();
    }
  });

  it('Verify A-3: gilt_bis ohne RFC-3339-Form gilt nicht als abgelöst', async () => {
    stand.zugaenge = [{ id: 'zd', typ: 'abs', name: 'datum', created_at: '2026-10-01T09:00:00Z', gilt_bis: '2026-10-01 12:00' }];
    await oeffne();
    const [tr] = zeilen();
    expect(knopf(tr, 'Entfernen')).toBeUndefined();
    expect(knopf(tr, 'Widerrufen')).toBeTruthy();
  });

  it('ohne abgeschlossene Zeilen gibt es keinen Sammelknopf', async () => {
    stand.zugaenge = stand.zugaenge.slice(0, 2);
    await oeffne();
    expect(aufraeumKnopf()).toBeUndefined();
  });
});
