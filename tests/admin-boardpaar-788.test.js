/* @vitest-environment happy-dom */

/**
 * Karte #788 (ADR-0140 §4): Admin-Reiter „Board-Paar“ — Seite der Instanz,
 * Partnerlisten innen und außen, Zugänge abs_/abe_. Der Klartext eines neuen
 * Zugangs steht GENAU EINMAL im Dialog (wie #748), nach dem Umschalten der
 * Seite fragt dieser Browser /me neu.
 *
 * Server-only (`describeServer`): `AdminDialog` gibt es nur im Server-Build.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

let stand;
let scrollSpy;
let anlegen = null; /* Promise-Steuerung: Antwort erst auf Zuruf */
const createSpy = vi.fn(() => new Promise((res) => { anlegen = res; }));
const setConfig = vi.fn(async (key, value) => { stand.seite = value; return { ok: true, body: { ok: true } }; });
const kopple = vi.fn(async () => ({ ok: true, body: {} }));
const entkopple = vi.fn(async () => ({ ok: true, body: { ok: true } }));
const meAufrufe = vi.fn(async () => ({ boardpaar: stand.seite === 'aus' ? undefined : stand.seite }));

vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listConfig: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    setConfig: (...a) => setConfig(...a),
    getBoardpaar: vi.fn(async () => ({ ok: true, body: JSON.parse(JSON.stringify(stand)) })),
    listAllBoards: vi.fn(async () => ({ ok: true, body: { boards: [
      { id: 'b1', title: 'Intern A' }, { id: 'b2', title: 'Extern B' }, { id: 'service', title: 'Service' }] } })),
    koppleBoardpaar: (...a) => kopple(...a),
    entkoppleBoardpaar: (...a) => entkopple(...a),
    createBoardpaarZugang: (...a) => createSpy(...a),
    revokeBoardpaarZugang: vi.fn(async () => ({ ok: true, body: { ok: true } })),
  },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: (...a) => meAufrufe(...a), listKnownBoards: async () => [] }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { I18n } from '../src/i18n.js';
import { boardpaarSeite, vergissBoardpaarSeite } from '../src/boardpaar.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
async function ruhig() { for (let i = 0; i < 4; i++) await ruhe(); }
async function reiter(id) {
  document.querySelector(`.admin-tab[data-section="${id}"]`).click();
  await ruhig();
}
const body = () => document.querySelector('.admin-body');
const tokenFelder = () => [...document.querySelectorAll('.admin-agents-token')].map(f => f.value);
const formulare = () => [...document.querySelectorAll('.admin-agents-new')];

beforeEach(() => {
  I18n.setLang('de');
  stand = {
    seite: 'aus',
    innen: [{ board_id: 'b1', seit: '2026-10-10T08:00:00Z' }],
    aussen: [],
    zugaenge: [],
  };
  anlegen = null;
  [createSpy, setConfig, kopple, entkopple, meAufrufe].forEach(f => f.mockClear());
  /* #820: happy-dom scrollt nicht — mitschneiden, welches Element sichtbar gemacht wird */
  scrollSpy = vi.spyOn(Element.prototype, 'scrollIntoView').mockImplementation(() => {});
  vergissBoardpaarSeite();
});
afterEach(() => { AdminDialog?.close(); vi.unstubAllGlobals(); scrollSpy.mockRestore(); });

describeServer('#788 Admin-Reiter Board-Paar', () => {
  it('zeigt Seite, Partnerliste mit Board-Titel und den Hinweis bei falscher Seite', async () => {
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    /* Verify F5: der UTC-Hinweis steht auch ohne einen Zugang. */
    expect(stand.zugaenge).toHaveLength(0);
    expect(body().textContent).toContain('Alle Zeiten in UTC.');
    expect(document.querySelector('#admin-boardpaar-seite').value).toBe('aus');
    expect(body().textContent).toContain('Intern A (b1)');
    /* Seite „aus“ → beide Listen tragen den Hinweis. */
    expect(body().textContent.match(/Die Liste wirkt erst, wenn die Seite passt/g)).toHaveLength(2);
    /* Service-Board steht nicht zur Auswahl, gekoppeltes b1 innen auch nicht. */
    const [innenForm] = formulare();
    const optionen = [...innenForm.querySelectorAll('option')].map(o => o.value);
    expect(optionen).toEqual(['', 'b2']);
  });

  it('Seite speichern: setConfig mit dem Wert, danach fragt dieser Browser /me neu', async () => {
    expect(await boardpaarSeite()).toBe('aus');
    expect(meAufrufe).toHaveBeenCalledTimes(1);
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const sel = document.querySelector('#admin-boardpaar-seite');
    sel.value = 'innen';
    document.querySelector('.admin-actions button').click();
    await ruhig();
    expect(setConfig).toHaveBeenCalledWith('boardpaar.seite', 'innen', false);
    expect(await boardpaarSeite()).toBe('innen');
    expect(meAufrufe).toHaveBeenCalledTimes(2);
    expect(document.querySelector('#admin-boardpaar-seite').value).toBe('innen');
  });

  it('F-2019: die Partnerliste zeigt den letzten Abgleich und einen Fehlgrund rot', async () => {
    stand.seite = 'innen';
    stand.innen = [
      { board_id: 'b1', seit: '2026-10-10T08:00:00Z', letzte_abfrage: '2026-10-11T09:15:00Z',
        letzter_fehlgrund: 'zu viele Einträge: 5001, Grenze 5000 — erledigte Karten archivieren' },
      { board_id: 'b2', seit: '2026-10-10T08:00:00Z', letzte_abfrage: '2026-10-11T09:20:00Z' },
      /* Sprint-90-Kritik F-G: auch „noch nicht abgerufen“ (innen). */
      { board_id: 'b3', seit: '2026-10-10T08:00:00Z' },
    ];
    stand.aussen = [{ board_id: 'service', innen_link_basis: 'https://i.example/#board=x', seit: '2026-10-10T08:00:00Z' }];
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const fehler = [...body().querySelectorAll('.admin-row-msg--err')].map(e => e.textContent);
    expect(fehler).toEqual(['Fehler am 2026-10-11 09:15: zu viele Einträge: 5001, Grenze 5000 — erledigte Karten archivieren']);
    expect(body().textContent).toContain('2026-10-11 09:20, in Ordnung');
    expect(body().textContent).toContain('noch nichts empfangen');
    expect(body().textContent).toContain('noch nicht abgerufen');
  });

  it('F-2021: ein 409 beim Umschalten nennt das Entkoppeln, die Seite bleibt', async () => {
    stand.seite = 'innen';
    setConfig.mockResolvedValueOnce({ ok: false, status: 409, body: { error: 'boardpaar: seite nicht wechselbar' } });
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    document.querySelector('#admin-boardpaar-seite').value = 'aus';
    document.querySelector('.admin-actions button').click();
    await ruhig();
    expect(document.querySelector('.admin-flash').textContent).toContain('zuerst unten entkoppeln');
    expect(meAufrufe).not.toHaveBeenCalled();
  });

  it('außen koppeln schickt Board und Adresse; ein 400 zeigt den Grund des Servers', async () => {
    kopple.mockResolvedValueOnce({ ok: false, status: 400, body: { error: 'adresse des internen boards ungültig: nur https' } });
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const aussen = formulare()[1];
    aussen.querySelector('select').value = 'b2';
    aussen.querySelector('input').value = ' http://i.example/#board=x ';
    aussen.querySelector('button').click();
    await ruhig();
    expect(kopple).toHaveBeenCalledWith('aussen', 'b2', 'http://i.example/#board=x');
    expect(aussen.textContent).toContain('nur https');
  });

  it('ohne Board-Wahl wird nichts geschickt', async () => {
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    formulare()[0].querySelector('button').click();
    await ruhig();
    expect(kopple).not.toHaveBeenCalled();
  });

  it('Entkoppeln fragt nach; Abbrechen schickt nichts', async () => {
    const frage = vi.fn().mockReturnValueOnce(false).mockReturnValueOnce(true);
    vi.stubGlobal('confirm', frage);
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const knopf = () => [...body().querySelectorAll('button')].find(b => b.textContent === 'Entkoppeln');
    knopf().click();
    await ruhig();
    expect(entkopple).not.toHaveBeenCalled();
    knopf().click();
    await ruhig();
    expect(entkopple).toHaveBeenCalledWith('innen', 'b1');
    expect(frage.mock.calls[1][0]).toContain('zuerst außen entkoppeln');
  });

  it('#796: erneuerter Zugang zeigt Übergangszeit, Nachfolger wann und von wo; abgelöste ohne Knopf', async () => {
    stand.seite = 'innen';
    stand.zugaenge = [
      { id: 'z2', typ: 'abs', name: 'push', created_at: '2026-10-11T09:00:00.1Z', created_by: 'erneuert',
        erneuert_aus: 'z1', erneuert_von: '192.0.2.7' },
      { id: 'z1', typ: 'abs', name: 'push', created_at: '2026-10-04T09:00:00Z', gilt_bis: '2026-10-11T09:10:00.100000000Z' },
      { id: 'z0', typ: 'abe', name: 'alt', created_at: '2026-10-01T09:00:00Z', revoked_at: '2026-10-02T09:00:00Z' },
      { id: 'zx', typ: 'abs', name: 'push', created_at: '2026-09-27T09:00:00Z', gilt_bis: '2026-10-04T09:10:00.000000000Z' },
    ];
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-10-11T09:05:00Z'));
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    vi.useRealTimers();
    const zeilen = [...document.querySelectorAll('.admin-audit-table tbody tr')].filter(tr => /push|alt/.test(tr.textContent));
    expect(zeilen).toHaveLength(4);
    expect(zeilen[0].textContent).toContain('2026-10-11 09:00 von 192.0.2.7');
    expect(zeilen[0].textContent).toContain('aktiv');
    expect(zeilen[1].textContent).toContain('erneuert, gilt bis 2026-10-11 09:10 (UTC)');
    /* Notaus: auch das Token in der Übergangszeit hat einen Widerruf-Knopf. */
    /* F-E/D-1: abgelaufener Vorgänger → „abgelöst“, ohne Widerruf-Knopf.
       Seit #848 tragen abgeschlossene Zeilen (widerrufen, abgelöst) stattdessen
       „Entfernen“. */
    expect(zeilen[3].textContent).toContain('abgelöst (erneuert)');
    expect(zeilen.map(z => [...z.querySelectorAll('button')].map(b => b.textContent).join('|')))
      .toEqual(['Widerrufen', 'Widerrufen', 'Entfernen', 'Entfernen']);
    expect(body().textContent).toContain('Alle Zeiten in UTC.');
  });

  it('neuer Zugang: Klartext genau einmal; Dialog zu während des Anlegens → verfällt', async () => {
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const zug = formulare()[2];
    zug.querySelector('input').value = 'team-schatten';
    zug.querySelector('button').click();
    await ruhe();
    expect(createSpy).toHaveBeenCalledWith('abs', 'team-schatten', false) /* #825: drittes Argument darf_anlegen */;
    stand.zugaenge = [{ id: 'z1', typ: 'abs', name: 'team-schatten', created_at: '2026-10-10T08:00:00Z' }];
    anlegen({ ok: true, body: { token: 'abs_geheim', zugang: { id: 'z1' } } }); // abzug:testwert
    await ruhig();
    expect(tokenFelder()).toEqual(['abs_geheim']);
    /* #820: Token direkt unter der Überschrift „Zugänge für das Skript“, sichtbar gescrollt */
    const box = document.querySelector('.admin-agents-once');
    const titel = box.previousElementSibling;
    expect(titel.tagName).toBe('H3');
    expect(titel.textContent).toBe(I18n.t('admin.boardpaar.zugaenge-title'));
    expect(scrollSpy).toHaveBeenCalledTimes(1);
    expect(scrollSpy.mock.contexts[0]).toBe(box);
    await reiter('status');
    await reiter('boardpaar');
    expect(tokenFelder()).toEqual([]);
    expect(body().textContent).toContain('team-schatten');

    formulare()[2].querySelector('input').value = 'zweiter';
    formulare()[2].querySelector('button').click();
    await ruhe();
    AdminDialog.close();
    anlegen({ ok: true, body: { token: 'abs_verfallen', zugang: { id: 'z2' } } }); // abzug:testwert
    await ruhig();
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    expect(tokenFelder()).toEqual([]);
  });

  it('#834: Typ „Sicherungen lesen (abb_)“ steht zur Wahl, nicht vorgewählt, ohne Anlege-Recht', async () => {
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const zug = formulare()[2];
    const typ = zug.querySelector('select');
    const abb = [...typ.querySelectorAll('option')].find(o => o.value === 'abb');
    expect(abb?.textContent).toBe(I18n.t('admin.boardpaar.typ-abb'));
    expect(abb.textContent).toContain('Sicherungen lesen (abb_)');
    expect(typ.value).toBe('abs');
    typ.value = 'abb';
    typ.dispatchEvent(new Event('change'));
    expect(zug.querySelector('.admin-checkbox-label').hidden).toBe(true);
    zug.querySelector('input:not([type="checkbox"])').value = 'team-sicherung';
    zug.querySelector('button').click();
    await ruhe();
    expect(createSpy).toHaveBeenCalledWith('abb', 'team-sicherung', false);
    anlegen({ ok: false, status: 500, body: {} });
    await ruhig();
  });
});

/* Karte #827 E4 (ADR-0141 §7, Plan D1/D9): Schalter „Bearbeiten außen
   erlauben“ im Reiter, nur auf der Seite außen. */
describeServer('#827 Admin-Reiter Board-Paar: Rückweg-Schalter', () => {
  it('zeigt den Stand, schaltet ein und fragt /me neu', async () => {
    stand.seite = 'aussen';
    stand.rueckweg = 'aus';
    setConfig.mockImplementationOnce(async (key, value) => { stand.rueckweg = value; return { ok: true, body: { ok: true } }; });
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const cb = document.querySelector('#admin-boardpaar-rueckweg');
    expect(cb).not.toBeNull();
    expect(cb.checked).toBe(false);
    const vorher = meAufrufe.mock.calls.length;
    cb.checked = true;
    cb.dispatchEvent(new Event('change', { bubbles: true }));
    await ruhig();
    expect(setConfig).toHaveBeenCalledWith('boardpaar.rueckweg', 'an', false);
    expect(stand.seite).toBe('aussen');
    expect(meAufrufe.mock.calls.length).toBe(vorher + 1);
    expect(cb.checked).toBe(true);
    expect(cb.disabled).toBe(false);
  });

  it('bei Fehler springt der Schalter zurück und nennt den Grund', async () => {
    stand.seite = 'aussen';
    stand.rueckweg = 'an';
    setConfig.mockResolvedValueOnce({ ok: false, status: 400, body: { error: 'boardpaar.rueckweg: kaputt' } });
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    const cb = document.querySelector('#admin-boardpaar-rueckweg');
    expect(cb.checked).toBe(true);
    cb.checked = false;
    cb.dispatchEvent(new Event('change', { bubbles: true }));
    await ruhig();
    expect(setConfig).toHaveBeenCalledWith('boardpaar.rueckweg', 'aus', false);
    expect(cb.checked).toBe(true);
    expect(body().textContent).toContain('boardpaar.rueckweg: kaputt');
  });

  it('innen gibt es den Schalter nicht', async () => {
    stand.seite = 'innen';
    AdminDialog.open();
    await ruhe();
    await reiter('boardpaar');
    expect(document.querySelector('#admin-boardpaar-rueckweg')).toBeNull();
  });
});
