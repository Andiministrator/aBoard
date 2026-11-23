/* @vitest-environment happy-dom */

/**
 * F-2268 (Sitzungs-Kritik zum Sprint-94-Abschluss N-5): Speichern des
 * Board-Paar-Teams entzieht Entfernten sofort den Zugriff auf alle Kopien.
 * Der Dialog fragt deshalb nach: erster Aufruf ohne Bestätigung → Vorschau
 * (409 bestaetigung_noetig) → confirm mit Liste und Zahl → zweiter Aufruf
 * mit bestaetigt. Abbrechen speichert nichts; „veraltet“ wird gemeldet.
 *
 * Server-only (`describeServer`): `AdminDialog` gibt es nur im Server-Build.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

let stand;
let scrollSpy;
const speichere = vi.fn();

vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listConfig: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    setConfig: vi.fn(async () => ({ ok: true, body: { ok: true } })),
    getBoardpaar: vi.fn(async () => ({ ok: true, body: JSON.parse(JSON.stringify(stand)) })),
    listAllBoards: vi.fn(async () => ({ ok: true, body: { boards: [] } })),
    setBoardpaarTeam: (...a) => speichere(...a),
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
async function reiterBoardpaar() {
  AdminDialog.open();
  await ruhe();
  document.querySelector('.admin-tab[data-section="boardpaar"]').click();
  await ruhig();
}
const feld = () => document.getElementById('admin-boardpaar-team');
function teamKnopf() {
  /* Der Speichern-Knopf direkt hinter dem Team-Feld. */
  const el = feld().closest('.admin-form-row').nextElementSibling;
  return el.querySelector('button');
}
const flashText = () => teamKnopf().closest('.admin-actions').nextElementSibling.textContent;

beforeEach(() => {
  I18n.setLang('de');
  stand = { seite: 'aussen', innen: [], aussen: [], kopien_entkoppelt: [], zugaenge: [],
    team: 'anna@firma.example, bernd@firma.example', rueckweg: 'aus' };
  speichere.mockReset();
  scrollSpy = vi.spyOn(Element.prototype, 'scrollIntoView').mockImplementation(() => {});
  vergissBoardpaarSeite();
});
afterEach(() => { AdminDialog?.close(); vi.unstubAllGlobals(); scrollSpy.mockRestore(); });

describeServer('F-2268 Board-Paar-Team speichern mit Bestätigung', () => {
  it('Entzug: Vorschau, confirm mit Liste und Zahl, dann bestätigt speichern', async () => {
    speichere
      .mockResolvedValueOnce({ ok: false, status: 409, body: { bestaetigung_noetig: true, weg: ['bernd@firma.example'], zeilen: 2 } })
      .mockResolvedValueOnce({ ok: true, status: 200, body: { ok: true, team: 'anna@firma.example', weg: ['bernd@firma.example'], zeilen: 2 } });
    const frage = vi.fn(() => true);
    vi.stubGlobal('confirm', frage);
    await reiterBoardpaar();
    expect(feld().value).toBe('anna@firma.example, bernd@firma.example');
    feld().value = 'anna@firma.example';
    teamKnopf().click();
    await ruhig();
    expect(speichere.mock.calls).toEqual([
      ['anna@firma.example', 'anna@firma.example, bernd@firma.example', false],
      ['anna@firma.example', 'anna@firma.example, bernd@firma.example', true],
    ]);
    expect(frage).toHaveBeenCalledWith(I18n.t('admin.boardpaar.team-confirm-entzug', 'bernd@firma.example', 2));
    expect(flashText()).toContain(I18n.t('admin.boardpaar.team-gespeichert-entzogen', 2));
    /* Danach gilt der gespeicherte Wert als „bisher“. */
    speichere.mockResolvedValueOnce({ ok: true, status: 200, body: { ok: true, team: 'anna@firma.example, carla@firma.example', weg: [], zeilen: 0 } });
    feld().value = 'anna@firma.example, carla@firma.example';
    teamKnopf().click();
    await ruhig();
    expect(speichere.mock.calls[2]).toEqual(['anna@firma.example, carla@firma.example', 'anna@firma.example', false]);
    expect(flashText()).toContain(I18n.t('admin.msg-saved'));
  });

  it('Abbrechen speichert nichts', async () => {
    speichere.mockResolvedValueOnce({ ok: false, status: 409, body: { bestaetigung_noetig: true, weg: ['bernd@firma.example'], zeilen: 1 } });
    vi.stubGlobal('confirm', vi.fn(() => false));
    await reiterBoardpaar();
    feld().value = '';
    teamKnopf().click();
    await ruhig();
    expect(speichere).toHaveBeenCalledTimes(1);
    expect(speichere.mock.calls[0][2]).toBe(false);
    expect(flashText()).toContain(I18n.t('admin.boardpaar.team-abgebrochen'));
  });

  it('Reiter während der Vorschau gewechselt: keine Rückfrage, kein zweiter Aufruf (Sitzungs-QA #835 K-2)', async () => {
    let antworte;
    speichere.mockImplementationOnce(() => new Promise(r => { antworte = r; }));
    const frage = vi.fn(() => true);
    vi.stubGlobal('confirm', frage);
    await reiterBoardpaar();
    feld().value = 'anna@firma.example';
    teamKnopf().click();
    await ruhig();
    document.querySelector('.admin-tab[data-section="status"]').click();
    await ruhig();
    antworte({ ok: false, status: 409, body: { bestaetigung_noetig: true, weg: ['bernd@firma.example'], zeilen: 1 } });
    await ruhig();
    expect(frage).not.toHaveBeenCalled();
    expect(speichere).toHaveBeenCalledTimes(1);
  });

  it('veraltet wird gemeldet, ohne confirm', async () => {
    speichere.mockResolvedValueOnce({ ok: false, status: 409, body: { veraltet: true } });
    const frage = vi.fn(() => true);
    vi.stubGlobal('confirm', frage);
    await reiterBoardpaar();
    teamKnopf().click();
    await ruhig();
    expect(frage).not.toHaveBeenCalled();
    expect(flashText()).toContain(I18n.t('admin.boardpaar.team-veraltet'));
  });
});
