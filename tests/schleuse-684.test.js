/* @vitest-environment happy-dom */

/**
 * Karte #684 (ADR-0134 §1, §2) — Oberfläche der Ausgangsschleuse, Grundlage S1:
 *   1. Admin-Dialog: Reiter „Ausgangsschleuse“ nur bei `schleuse: "an"`,
 *      Statuszeile, Freigabeliste mit Gründen, Anlegen.
 *   2. „Meine Schlüssel“: Menüpunkt nur bei `schleuse: "an"` aus /me,
 *      Speichern nur mit Bestätigung „geschäftliches Konto“, der Schlüssel
 *      steht danach nirgends mehr im DOM.
 *   3. Board-Sperre: bei 404 (Schleuse aus) nichts, Owner schaltet.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));

let statusBody = {};
let freigaben = [];
const createSpy = vi.fn(async () => ({ ok: true, status: 201, body: {} }));
vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: statusBody })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listSchleuseFreigaben: vi.fn(async () => ({ ok: true, body: {
      freigaben, anbieter_arten: ['openai-kompatibel', 'anthropic', 'mistral', 'google'], ausgaenge: 'aus' } })),
    createSchleuseFreigabe: (...a) => createSpy(...a),
    updateSchleuseFreigabe: vi.fn(async () => ({ ok: true, body: {} })),
    deleteSchleuseFreigabe: vi.fn(async () => ({ ok: true, body: {} })),
  },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({ name: null, email: null }) }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { SchleuseSchluesselDialog } from '../src/schleuse-schluessel.js';
import { I18n } from '../src/i18n.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const tab = () => document.querySelector('.admin-tab[data-section="schleuse"]');

describeServer('Admin-Dialog: Ausgangsschleuse (Karte #684)', () => {
  beforeEach(() => { document.body.innerHTML = ''; I18n.setLang('de'); createSpy.mockClear(); });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; });

  it('bei „aus“: Statuszeile, aber kein Reiter', async () => {
    statusBody = { version: 'x', schleuse: 'aus' };
    AdminDialog.open();
    await ruhe();
    expect(document.querySelector('.admin-body').textContent).toContain('aus (ABOARD_SCHLEUSE)');
    expect(tab()).toBeNull();
  });

  it('ohne Feld (älterer Server): weder Zeile noch Reiter', async () => {
    statusBody = { version: 'x' };
    AdminDialog.open();
    await ruhe();
    expect(document.querySelector('.admin-body').textContent).not.toContain('Ausgangsschleuse');
    expect(tab()).toBeNull();
  });

  it('bei „an“: Zahlen im Status, Reiter mit Gründen, Anlegen schickt das Formular', async () => {
    statusBody = { version: 'x', schleuse: 'an', schleuse_anbieter_freigegeben: 2,
      schleuse_anbieter_nutzbar: 1, schleuse_aufbewahrung_tage: 30 };
    freigaben = [
      { id: 'f1', anbieter: 'anthropic', bezeichnung: '', host: 'api.anthropic.com', modelle: ['claude-x'],
        vertragsform_bestaetigt: true, vier_augen: false, aktiv: true, nutzbar: true, gruende: [] },
      { id: 'f2', anbieter: 'openai-kompatibel', bezeichnung: 'Intern', host: 'llm.example.net', modelle: ['m'],
        vertragsform_bestaetigt: true, vier_augen: true, aktiv: true, nutzbar: false, gruende: ['ausgang-gesperrt'] },
    ];
    AdminDialog.open();
    await ruhe();
    const text = document.querySelector('.admin-body').textContent;
    expect(text).toContain('2 Anbieter freigegeben, davon 1 nutzbar');
    expect(text).toContain('30 Tage');
    expect(tab()).toBeTruthy();
    tab().click();
    await ruhe();
    const zeilen = document.querySelectorAll('.admin-schleuse-table tbody tr');
    expect(zeilen.length).toBe(2);
    expect(zeilen[1].textContent).toContain('Host nicht in ABOARD_AUSGAENGE_ERLAUBT');
    expect(document.querySelector('.admin-body').textContent).toContain('ABOARD_AUSGAENGE=aus');

    document.getElementById('admin-schleuse-col-anbieter').value = 'mistral';
    document.getElementById('admin-schleuse-modelle').value = 'a, b ,';
    document.getElementById('admin-schleuse-vertragsform').checked = true;
    document.querySelector('.admin-schleuse-form .admin-btn--primary').click();
    await ruhe();
    expect(createSpy).toHaveBeenCalledWith({ anbieter: 'mistral', bezeichnung: '', basis_url: '',
      modelle: ['a', 'b'], vertragsform_bestaetigt: true, vier_augen: false, aktiv: true });
  });
});

describeServer('Meine Schlüssel und Board-Sperre (Karte #684)', () => {
  const GEHEIM = 'sk-GEHEIM-123';
  let fetchSpy;
  let me;
  let sperre;
  let liste;
  let schluesselPut;

  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    me = { status: 200, body: { email: 'a@x', schleuse: 'an' } };
    sperre = { status: 200, body: { gesperrt: false, darf_aendern: true } };
    liste = { anbieter: [{ id: 'f1', anbieter: 'anthropic', bezeichnung: '', host: 'api.anthropic.com',
      basis_url: 'https://api.anthropic.com', modelle: ['claude-x'], nutzbar: true, gruende: [] }], schluessel: [] };
    schluesselPut = { status: 200, body: { ok: true } };
    SchleuseSchluesselDialog.vergissSchalter();
    fetchSpy = vi.fn(async (pfad, opt = {}) => {
      const m = opt.method || 'GET';
      let a = { status: 200, body: { ok: true } };
      if (pfad === '/me') a = me;
      else if (pfad.endsWith('/schleuse-sperre') && m === 'GET') a = sperre;
      else if (pfad === '/users/me/schleuse-schluessel') a = { status: 200, body: liste };
      else if (pfad.startsWith('/users/me/schleuse-schluessel/') && m === 'PUT') a = schluesselPut;
      return { ok: a.status < 300, status: a.status, json: async () => a.body };
    });
    vi.stubGlobal('fetch', fetchSpy);
  });
  afterEach(() => { SchleuseSchluesselDialog?.close(); vi.unstubAllGlobals(); document.body.innerHTML = ''; });

  const sektion = () => { const s = document.createElement('div'); document.body.appendChild(s); return s; };
  const makeRow = (label, onClick) => {
    const b = document.createElement('button'); b.textContent = label; b.addEventListener('click', onClick); return b;
  };

  it('Menüpunkt nur bei schleuse: "an"', async () => {
    const s1 = sektion();
    await SchleuseSchluesselDialog.ergaenzeMenue(s1, makeRow);
    expect(s1.querySelector('.sls-menue')).toBeTruthy();
    me = { status: 200, body: { email: 'a@x' } };
    SchleuseSchluesselDialog.vergissSchalter();
    const s2 = sektion();
    await SchleuseSchluesselDialog.ergaenzeMenue(s2, makeRow);
    expect(s2.querySelector('.sls-menue')).toBeNull();
  });

  it('Speichern verlangt die Bestätigung, schickt sie mit und lässt den Schlüssel nicht im DOM', async () => {
    SchleuseSchluesselDialog.open();
    await ruhe();
    const feld = document.querySelector('.sls-key');
    feld.value = GEHEIM;
    const knopf = document.querySelector('.sls-btn--primary');
    knopf.click();
    await ruhe();
    expect(fetchSpy.mock.calls.some(([, o]) => o?.method === 'PUT')).toBe(false);
    expect(document.querySelector('.sls-anbieter .sls-meldung').textContent).toContain('geschäftliches Konto');

    document.querySelector('.sls-bestaetigt').checked = true;
    knopf.click();
    await ruhe();
    const put = fetchSpy.mock.calls.find(([, o]) => o?.method === 'PUT');
    expect(put[0]).toBe('/users/me/schleuse-schluessel/f1');
    // KC-2: der Stand der Freigabe, den der Dialog angezeigt hat, geht mit.
    expect(JSON.parse(put[1].body)).toEqual({ key: GEHEIM, geschaeftskonto_bestaetigt: true,
      anbieter: 'anthropic', basis_url: 'https://api.anthropic.com' });
    await ruhe();
    expect([...document.querySelectorAll('input')].some(i => String(i.value).includes('GEHEIM'))).toBe(false);
    expect(document.body.innerHTML).not.toContain('GEHEIM');
  });

  it('KC-2: 409 (Freigabe geändert) → Dialog lädt neu und meldet es', async () => {
    SchleuseSchluesselDialog.open();
    await ruhe();
    schluesselPut = { status: 409, body: { error: 'freigabe hat sich geändert' } };
    const vorher = fetchSpy.mock.calls.filter(([p]) => p === '/users/me/schleuse-schluessel').length;
    document.querySelector('.sls-key').value = GEHEIM;
    document.querySelector('.sls-bestaetigt').checked = true;
    document.querySelector('.sls-btn--primary').click();
    await ruhe(); await ruhe();
    const nachher = fetchSpy.mock.calls.filter(([p]) => p === '/users/me/schleuse-schluessel').length;
    expect(nachher).toBe(vorher + 1);
    expect(document.querySelector('.sls-anbieter .sls-meldung').textContent).toContain('geändert');
    expect(document.body.innerHTML).not.toContain('GEHEIM');
  });

  it('KC-8: /me ohne schleuse → kein Aufruf der Board-Sperre; /me nur einmal', async () => {
    me = { status: 200, body: { email: 'a@x' } };
    const s1 = sektion();
    await SchleuseSchluesselDialog.ergaenzeBoardSperre(s1, 'b1');
    await SchleuseSchluesselDialog.ergaenzeBoardSperre(sektion(), 'b2');
    await SchleuseSchluesselDialog.ergaenzeMenue(sektion(), makeRow);
    expect(fetchSpy.mock.calls.some(([p]) => String(p).endsWith('/schleuse-sperre'))).toBe(false);
    expect(fetchSpy.mock.calls.filter(([p]) => p === '/me').length).toBe(1);
    expect(s1.children.length).toBe(0);
  });

  it('KC-8: /me scheitert → nicht gemerkt, der nächste Aufbau fragt erneut', async () => {
    me = { status: 500, body: null };
    await SchleuseSchluesselDialog.ergaenzeBoardSperre(sektion(), 'b1');
    me = { status: 200, body: { email: 'a@x', schleuse: 'an' } };
    const s2 = sektion();
    await SchleuseSchluesselDialog.ergaenzeBoardSperre(s2, 'b1');
    expect(fetchSpy.mock.calls.filter(([p]) => p === '/me').length).toBe(2);
    expect(s2.querySelector('input[type=checkbox]')).toBeTruthy();
  });

  it('Board-Sperre: 404 → nichts; Owner schaltet per PUT; Nicht-Owner sieht nur „gesperrt“', async () => {
    sperre = { status: 404, body: { error: 'not found' } };
    const s1 = sektion();
    await SchleuseSchluesselDialog.ergaenzeBoardSperre(s1, 'b1');
    expect(s1.children.length).toBe(0);

    sperre = { status: 200, body: { gesperrt: false, darf_aendern: true } };
    const s2 = sektion();
    await SchleuseSchluesselDialog.ergaenzeBoardSperre(s2, 'b1');
    const cb = s2.querySelector('input[type=checkbox]');
    cb.checked = true;
    cb.dispatchEvent(new Event('change'));
    await ruhe();
    const put = fetchSpy.mock.calls.find(([p, o]) => p === '/boards/b1/schleuse-sperre' && o?.method === 'PUT');
    expect(JSON.parse(put[1].body)).toEqual({ gesperrt: true });

    sperre = { status: 200, body: { gesperrt: true, darf_aendern: false } };
    const s3 = sektion();
    await SchleuseSchluesselDialog.ergaenzeBoardSperre(s3, 'b1');
    expect(s3.querySelector('input')).toBeNull();
    expect(s3.textContent).toContain('gesperrt');
  });
});
