/* @vitest-environment happy-dom */

/**
 * Karte #831 (ADR-0141 Nachtrag, F-2232/F-2229): Admin-Reiter „Board-Paar“ —
 * Notweg „Kopie außen löschen“ (nur außen, nur Kopien, auch außen
 * entkoppelte) mit Tipp-Bestätigung; eigener Bestätigungstext beim
 * Entkoppeln einer Kopie.
 *
 * Server-only (`describeServer`): `AdminDialog` gibt es nur im Server-Build.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

let stand;
let scrollSpy;
const loesche = vi.fn(async (id) => {
  stand.aussen = stand.aussen.filter(e => e.board_id !== id);
  stand.kopien_entkoppelt = stand.kopien_entkoppelt.filter(e => e.board_id !== id);
  return { ok: true, body: { ok: true, offene_aenderungen: loescheOffene } };
});
let loescheOffene = 0;
const entkopple = vi.fn(async () => ({ ok: true, body: { ok: true } }));

vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listConfig: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    setConfig: vi.fn(async () => ({ ok: true, body: { ok: true } })),
    getBoardpaar: vi.fn(async () => ({ ok: true, body: JSON.parse(JSON.stringify(stand)) })),
    listAllBoards: vi.fn(async () => ({ ok: true, body: { boards: [
      { id: 'b2', title: 'Extern B' }, { id: 'b3', title: 'Stufe Eins' }, { id: 'b4', title: 'Alt' }] } })),
    koppleBoardpaar: vi.fn(async () => ({ ok: true, body: {} })),
    entkoppleBoardpaar: (...a) => entkopple(...a),
    loescheBoardpaarKopie: (...a) => loesche(...a),
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
const body = () => document.querySelector('.admin-body');
const loeschKnoepfe = () => [...document.querySelectorAll('.admin-boardpaar-kopie-loeschen')];
const eingabe = (feld, wert) => { feld.value = wert; feld.dispatchEvent(new Event('input')); };

beforeEach(() => {
  I18n.setLang('de');
  stand = {
    seite: 'aussen',
    innen: [],
    aussen: [
      { board_id: 'b2', innen_link_basis: 'https://i.example/#board=x', seit: '2026-10-10T08:00:00Z', kopie: true },
      { board_id: 'b3', innen_link_basis: 'https://i.example/#board=y', seit: '2026-10-10T08:00:00Z', kopie: false },
    ],
    kopien_entkoppelt: [{ board_id: 'b4', titel: 'Alt' }],
    zugaenge: [],
  };
  [loesche, entkopple].forEach(f => f.mockClear());
  loescheOffene = 0;
  scrollSpy = vi.spyOn(Element.prototype, 'scrollIntoView').mockImplementation(() => {});
  vergissBoardpaarSeite();
});
afterEach(() => { AdminDialog?.close(); vi.unstubAllGlobals(); scrollSpy.mockRestore(); });

describeServer('#831 Notweg „Kopie außen löschen“ im Admin-Reiter', () => {
  it('Knopf nur außen, nur bei Kopien — gekoppelt und außen entkoppelt, nicht bei Stufe 1', async () => {
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const k = loeschKnoepfe();
    expect(k.map(b => b.getAttribute('aria-label'))).toEqual([
      I18n.t('admin.boardpaar.aria-kopie-loeschen', 'Extern B (b2)'),
      I18n.t('admin.boardpaar.aria-kopie-loeschen', 'Alt (b4)'),
    ]);
    expect(body().textContent).toContain(I18n.t('admin.boardpaar.entkoppelt-title'));
    /* Stufe-1-Zeile b3 hat keinen Knopf. */
    const zeileB3 = [...document.querySelectorAll('tr')].find(tr => tr.textContent.includes('Stufe Eins (b3)'));
    expect(zeileB3.querySelector('.admin-boardpaar-kopie-loeschen')).toBeNull();
  });

  it('auf der Seite innen gibt es den Knopf nicht', async () => {
    stand.seite = 'innen';
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    expect(loeschKnoepfe()).toHaveLength(0);
    expect(body().textContent).not.toContain(I18n.t('admin.boardpaar.entkoppelt-title'));
  });

  it('erst der exakt eingegebene Name gibt den Knopf frei; dann wird gelöscht und gemeldet', async () => {
    const nativ = vi.fn(() => true);
    vi.stubGlobal('confirm', nativ);
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    loeschKnoepfe()[0].click();
    const panel = document.querySelector('.admin-boardpaar-kopie-confirm');
    expect(panel).not.toBeNull();
    const feld = panel.querySelector('input');
    const [ok] = panel.querySelectorAll('button');
    expect(ok.disabled).toBe(true);
    ok.click();
    await ruhig();
    expect(loesche).not.toHaveBeenCalled();
    eingabe(feld, 'Extern');
    expect(ok.disabled).toBe(true);
    eingabe(feld, 'Extern B');
    expect(ok.disabled).toBe(false);
    ok.click();
    await ruhig();
    expect(loesche).toHaveBeenCalledWith('b2');
    expect(nativ).not.toHaveBeenCalled(); /* kein natives confirm() */
    expect(body().textContent).toContain(I18n.t('admin.boardpaar.kopie-geloescht', 'Extern B (b2)'));
    expect(loeschKnoepfe()).toHaveLength(1);
  });

  /* F-2240: Die Zahl aus der Liste steht VOR dem Löschen im Dialog, die
     Zahl aus der Antwort in der Erfolgsmeldung. */
  it('F-2240: Dialog warnt vor offenen Änderungen (gekoppelt und entkoppelt), Erfolgsmeldung nennt die verworfene Zahl', async () => {
    stand.aussen[0].offene_aenderungen = 3;
    stand.kopien_entkoppelt[0].offene_aenderungen = 1;
    loescheOffene = 3;
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    loeschKnoepfe()[1].click(); /* entkoppelt, eine offene */
    expect(document.querySelector('.admin-boardpaar-kopie-offene').textContent)
      .toBe(I18n.t('admin.boardpaar.kopie-loeschen-offene', 1));
    document.querySelector('.admin-boardpaar-kopie-confirm button[class*="admin-btn"]:last-child').click(); /* Abbrechen */
    loeschKnoepfe()[0].click();
    const panel = document.querySelector('.admin-boardpaar-kopie-confirm');
    expect(panel.querySelector('.admin-boardpaar-kopie-offene').textContent)
      .toBe(I18n.t('admin.boardpaar.kopie-loeschen-offene', 3));
    expect(I18n.t('admin.boardpaar.kopie-loeschen-offene', 3)).toContain('3');
    eingabe(panel.querySelector('input'), 'Extern B');
    panel.querySelector('button').click();
    await ruhig();
    expect(body().textContent).toContain(I18n.t('admin.boardpaar.kopie-geloescht-offene', 3));
  });

  it('F-2240: ohne offene Änderungen keine Zusatzwarnung', async () => {
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    loeschKnoepfe()[0].click();
    expect(document.querySelector('.admin-boardpaar-kopie-offene')).toBeNull();
  });

  it('Abbrechen löscht nichts und gibt den Knopf zurück', async () => {
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const knopf = loeschKnoepfe()[1];
    knopf.click();
    const panel = document.querySelector('.admin-boardpaar-kopie-confirm');
    eingabe(panel.querySelector('input'), 'Alt');
    panel.querySelectorAll('button')[1].click();
    await ruhig();
    expect(loesche).not.toHaveBeenCalled();
    expect(document.querySelector('.admin-boardpaar-kopie-confirm')).toBeNull();
    expect(knopf.disabled).toBe(false);
  });

  it('409 vom Server: Meldung „keine Kopie“, nichts neu gezeichnet', async () => {
    loesche.mockImplementationOnce(async () => ({ ok: false, status: 409, body: { error: 'x' } }));
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    loeschKnoepfe()[0].click();
    const panel = document.querySelector('.admin-boardpaar-kopie-confirm');
    eingabe(panel.querySelector('input'), 'Extern B');
    panel.querySelector('button').click();
    await ruhig();
    expect(body().textContent).toContain(I18n.t('admin.boardpaar.err-keine-kopie'));
  });

  it('F-2229: Entkoppeln einer Kopie warnt und verweist auf den Notweg, Stufe 1 behält den alten Text', async () => {
    const texte = [];
    vi.stubGlobal('confirm', vi.fn((t) => { texte.push(t); return false; }));
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const entkoppeln = [...document.querySelectorAll('button')].filter(b => b.textContent === I18n.t('admin.boardpaar.btn-entkoppeln'));
    entkoppeln[0].click(); /* b2, Kopie */
    entkoppeln[1].click(); /* b3, Stufe 1 */
    expect(texte[0]).toBe(I18n.t('admin.boardpaar.confirm-aussen-kopie', 'Extern B (b2)'));
    expect(texte[0]).toContain('Kopie außen löschen');
    expect(texte[0]).toContain('vollem Inhalt');
    expect(texte[1]).toBe(I18n.t('admin.boardpaar.confirm-aussen', 'Stufe Eins (b3)'));
    expect(entkopple).not.toHaveBeenCalled();
  });

  it('Kritik K-2: Titel mit Rand-Leerzeichen und anderer Unicode-Form ist bestätigbar, die Board-ID auch', async () => {
    /* Zerlegtes „é“ (e + U+0301) mit Rand-Leerzeichen; die Eingabe ist NFC.
       Per fromCharCode gebaut, damit kein Werkzeug die Form vorher angleicht. */
    const zerlegt = 'Cafe' + String.fromCharCode(0x301);
    const nfc = 'Caf' + String.fromCharCode(0xe9);
    expect(zerlegt).not.toBe(nfc);
    stand.kopien_entkoppelt = [{ board_id: 'b4', titel: '  ' + zerlegt + '  ' }];
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    loeschKnoepfe()[1].click();
    const panel = document.querySelector('.admin-boardpaar-kopie-confirm');
    const feld = panel.querySelector('input');
    const [ok] = panel.querySelectorAll('button');
    for (const [wert, frei] of [['', false], ['   ', false], ['Cafe', false], [nfc, true], ['  ' + nfc + ' ', true],
      [zerlegt, true], ['b4', true], [' b4 ', true], ['b2', false]]) {
      eingabe(feld, wert);
      expect([wert, ok.disabled]).toEqual([wert, !frei]);
    }
    eingabe(feld, nfc);
    ok.click();
    await ruhig();
    expect(loesche).toHaveBeenCalledWith('b4');
  });

  /* Sprint-94-Kritik F-2: Gelingt das Löschen, scheitert aber das Neuladen der
     Liste, muss der Admin trotzdem sehen, dass die unumkehrbare Aktion lief —
     die Meldung steht VOR dem Fehler und bekommt den Fokus. */
  it('Löschen gelingt, Neuladen scheitert: Erfolgsmeldung vor dem Fehler, mit Fokus', async () => {
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    loeschKnoepfe()[0].click();
    const panel = document.querySelector('.admin-boardpaar-kopie-confirm');
    eingabe(panel.querySelector('input'), 'Extern B');
    const { AdminClient } = await import('../src/admin-client.js');
    AdminClient.getBoardpaar.mockImplementationOnce(async () => ({ ok: false, status: 503, body: {} }));
    panel.querySelectorAll('button')[0].click();
    await ruhig();
    expect(loesche).toHaveBeenCalledWith('b2');
    const flash = body().querySelector('.admin-flash');
    expect(flash?.textContent).toContain(I18n.t('admin.boardpaar.kopie-geloescht', 'Extern B (b2)'));
    expect(body().querySelector('.admin-error')).not.toBeNull();
    expect(flash.compareDocumentPosition(body().querySelector('.admin-error')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(document.activeElement).toBe(flash);
    /* Die Meldung erscheint nicht ein zweites Mal beim nächsten Neuzeichnen. */
    await reiter('status');
    await reiter('boardpaar');
    expect(body().textContent).not.toContain(I18n.t('admin.boardpaar.kopie-geloescht', 'Extern B (b2)'));
  });
});
