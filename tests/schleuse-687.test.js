/* @vitest-environment happy-dom */

/**
 * Karte #687 (ADR-0134 §5) — „Extern bearbeiten“, Teil S4: Dateien.
 *   1. Das Formular listet die Anhänge der Karte; der Browser schickt nur
 *      ihre ids, nie Inhalt.
 *   2. Bilder: eigene Freigabe je Bild mit Warnhinweis; eine ungespeicherte
 *      Auswahl sperrt „Freigeben und senden“ wie eine Textänderung; Speichern
 *      schickt die vollständige Liste der freigegebenen Bilder.
 *   3. Die Bestätigung vor dem Senden nennt die Zahl der Bilder.
 *   4. Ein Fehler zu einer Datei nennt ihren Namen.
 *   5. Die zweite Freigabe zeigt die Bilder, die mitgehen.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));

import { SchleuseAuszug } from '../src/schleuse-auszug.js';
import { I18n } from '../src/i18n.js';

const ruhe = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0)); };

const KARTE = {
  id: 'k1', title: 'Titel', description: 'Text', comments: [],
  attachments: [
    { id: 't1', filename: 'notiz.txt', mime: 'text/plain', size: 120 },
    { id: 'b1', filename: 'foto.jpg', mime: 'image/jpeg', size: 4000 },
    'altlast-id',
  ],
};
const SHA = 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789';
const SHA2 = '1111110123456789abcdef0123456789abcdef0123456789abcdef0123456789';
const BILD = { id: 'b1', mime: 'image/jpeg', bytes: 3000, sha256: 'b'.repeat(64), freigegeben: false };
const AUFTRAG = {
  id: 'a1', board_id: 'b1', card_id: 'k1', freigabe_id: 'f1', anbieter: 'anthropic', modell: 'claude-x',
  zustand: 'entwurf', text: '## Datei 1: notiz.txt\n\nHallo [PERSON_1]', sha256: SHA,
  ersetzungen: [{ platzhalter: '[PERSON_1]', art: 'PERSON', quelle: 'fest', anzahl: 1 }],
  teile: { beschreibung: false, kommentare: [], aufgabe: false, dateien: ['t1'] }, freigegeben: false, vier_augen: false,
  bilder: [BILD], veraenderte_platzhalter: [], erstellt_am: '2026-10-05T10:00:00.000Z', geaendert_am: '2026-10-05T10:00:00.000Z',
};

describeServer('Extern bearbeiten: Dateien (Karte #687)', () => {
  let fetchSpy, liste, erstellen, speichern, freigabe;

  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    liste = { status: 200, body: { auftraege: [AUFTRAG], zur_freigabe: [], gesperrt: false, darf_erstellen: true, lokales_modell: true, grenze_zeichen: 20000 } };
    erstellen = { status: 201, body: { auftrag: AUFTRAG, entschaerft: 0 } };
    speichern = { status: 200, body: { auftrag: { ...AUFTRAG, sha256: SHA2, bilder: [{ ...BILD, freigegeben: true }] } } };
    freigabe = { status: 200, body: { auftrag: { ...AUFTRAG, zustand: 'freigegeben', freigegeben: true } } };
    SchleuseAuszug.vergissSchalter();
    fetchSpy = vi.fn(async (pfad, opt = {}) => {
      const m = opt.method || 'GET';
      let a = { status: 404, body: { error: 'x' } };
      if (pfad === '/me') a = { status: 200, body: { schleuse: 'an' } };
      else if (pfad === '/users/me/schleuse-schluessel') a = { status: 200, body: {
        anbieter: [{ id: 'f1', anbieter: 'anthropic', modelle: ['claude-x'], nutzbar: true }], schluessel: [{ freigabe_id: 'f1' }] } };
      else if (pfad === '/boards/b1/cards/k1/schleuse-auftraege' && m === 'GET') a = liste;
      else if (pfad === '/boards/b1/cards/k1/schleuse-auftraege' && m === 'POST') a = erstellen;
      else if (pfad === '/boards/b1/cards/k1/schleuse-auftraege/a1' && m === 'PUT') a = speichern;
      else if (pfad === '/boards/b1/cards/k1/schleuse-auftraege/a1/freigabe' && m === 'POST') a = freigabe;
      return { ok: a.status < 300, status: a.status, json: async () => a.body };
    });
    vi.stubGlobal('fetch', fetchSpy);
  });
  afterEach(() => { SchleuseAuszug?.close(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.body.innerHTML = ''; });

  const aufrufe = (endung, methode) => fetchSpy.mock.calls.filter(([p, o]) => p.endsWith(endung) && o?.method === methode);

  it('Formular listet die Anhänge; geschickt werden nur ids', async () => {
    liste.body.auftraege = [];
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    const boxen = [...document.querySelectorAll('.sla-teil-datei')];
    expect(boxen.map(b => b.dataset.id)).toEqual(['t1', 'b1', 'altlast-id']);
    const text = document.querySelector('.sla-form').textContent;
    expect(text).toContain('notiz.txt');
    expect(text).toContain('wird NICHT pseudonymisiert');
    /* Nur Dateien, keine Beschreibung: zählt als Auswahl. */
    document.querySelector('.sla-teil-beschreibung').checked = false;
    boxen[0].checked = true;
    boxen[1].checked = true;
    [...document.querySelectorAll('.sla-form .sla-btn--primary')].pop().click();
    await ruhe();
    const posts = aufrufe('/schleuse-auftraege', 'POST');
    expect(posts).toHaveLength(1);
    const body = JSON.parse(posts[0][1].body);
    expect(body.dateien).toEqual(['t1', 'b1']);
    expect(posts[0][1].body).not.toContain('notiz.txt');
  });

  it('Bild: Einzelfreigabe sperrt Senden bis zum Speichern, Speichern schickt die Liste', async () => {
    const confirm = vi.fn(() => false);
    vi.stubGlobal('confirm', confirm);
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-entwurf .sla-btn').click(); // Öffnen
    await ruhe();
    expect(document.querySelector('.sla-bild-warnung').textContent).toContain('nicht pseudonymisiert');
    expect(document.querySelector('.sla-bild').textContent).toContain('foto.jpg');
    const senden = document.querySelector('.sla-senden');
    expect(senden.disabled).toBe(false);
    const frei = document.querySelector('.sla-bild-frei');
    expect(frei.checked).toBe(false);
    /* VS4-2: Vorschau vom Server (bereinigte Bytes), Freigabe erst nach dem Laden. */
    const vorschau = document.querySelector('.sla-bild-vorschau');
    expect(vorschau.getAttribute('src')).toBe('/boards/b1/cards/k1/schleuse-auftraege/a1/bilder/b1');
    expect(frei.disabled).toBe(true);
    vorschau.dispatchEvent(new Event('load'));
    expect(frei.disabled).toBe(false);
    frei.checked = true;
    frei.dispatchEvent(new Event('change'));
    expect(senden.disabled).toBe(true);
    expect(document.querySelector('.sla-ungespeichert')).not.toBeNull();
    /* Zurück auf den gespeicherten Stand: wieder sendbar. */
    frei.checked = false;
    frei.dispatchEvent(new Event('change'));
    expect(senden.disabled).toBe(false);
    frei.checked = true;
    frei.dispatchEvent(new Event('change'));

    [...document.querySelectorAll('.sla-knoepfe .sla-btn--primary')][0].click(); // Speichern
    await ruhe();
    const puts = aufrufe('/schleuse-auftraege/a1', 'PUT');
    expect(puts).toHaveLength(1);
    expect(JSON.parse(puts[0][1].body)).toEqual({ text: AUFTRAG.text, basis_sha256: SHA, bilder_freigegeben: ['b1'] });
    expect(senden.disabled).toBe(false);
    expect(document.querySelector('.sla-bild-frei').checked).toBe(true);

    senden.click();
    await ruhe();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(confirm.mock.calls[0][0]).toContain('1 Bild(er)');
    expect(aufrufe('/freigabe', 'POST')).toHaveLength(0);
    confirm.mockImplementation(() => true);
    senden.click();
    await ruhe();
    const posts = aufrufe('/freigabe', 'POST');
    expect(posts).toHaveLength(1);
    expect(JSON.parse(posts[0][1].body)).toEqual({ sha256: SHA2 });
  });

  it('Ohne Bilder im Auftrag schickt Speichern keine Bildliste', async () => {
    liste.body.auftraege = [{ ...AUFTRAG, bilder: [] }];
    speichern.body.auftrag = { ...AUFTRAG, bilder: [] };
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-entwurf .sla-btn').click();
    await ruhe();
    expect(document.querySelector('.sla-bild')).toBeNull();
    const feld = document.querySelector('.sla-text');
    feld.value += ' mehr';
    feld.dispatchEvent(new Event('input'));
    [...document.querySelectorAll('.sla-knoepfe .sla-btn--primary')][0].click();
    await ruhe();
    const body = JSON.parse(aufrufe('/schleuse-auftraege/a1', 'PUT')[0][1].body);
    expect('bilder_freigegeben' in body).toBe(false);
  });

  it('Vorschau lädt nicht (Bild geändert): Hinweis, Freigabe bleibt gesperrt', async () => {
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-entwurf .sla-btn').click();
    await ruhe();
    document.querySelector('.sla-bild-vorschau').dispatchEvent(new Event('error'));
    expect(document.querySelector('.sla-bild-fehlt').textContent).toContain('geändert');
    expect(document.querySelector('.sla-bild-frei').disabled).toBe(true);
  });

  it('Gespeichert freigegebenes Bild ohne Vorschau: abgewählt, Senden gesperrt (SK4-6)', async () => {
    liste.body.auftraege = [{ ...AUFTRAG, bilder: [{ ...BILD, freigegeben: true }] }];
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-entwurf .sla-btn').click();
    await ruhe();
    const senden = document.querySelector('.sla-senden');
    expect(senden.disabled).toBe(false);
    document.querySelector('.sla-bild-vorschau').dispatchEvent(new Event('error'));
    const frei = document.querySelector('.sla-bild-frei');
    expect(frei.checked).toBe(false);
    expect(frei.disabled).toBe(true);
    expect(senden.disabled).toBe(true);
  });

  it('Fehler zu einer Datei nennt ihren Namen', async () => {
    liste.body.auftraege = [];
    erstellen = { status: 422, body: { error: 'x', grund: 'pdf-ohne-text', status: 422, datei_id: 't1' } };
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-teil-datei').checked = true;
    [...document.querySelectorAll('.sla-form .sla-btn--primary')].pop().click();
    await ruhe();
    const meldung = document.querySelector('.sla-form .sla-meldung').textContent;
    expect(meldung).toContain('keinen auslesbaren Text');
    expect(meldung).toContain('notiz.txt');
  });

  it('Zweite Freigabe zeigt die Bilder, die mitgehen', async () => {
    liste.body.auftraege = [];
    liste.body.zur_freigabe = [{ id: 'z1', anbieter: 'anthropic', modell: 'claude-x', zustand: 'freigegeben', text: 'T',
      sha256: SHA, ersetzungen: [], teile: {}, antragsteller: 'anna@x', vier_augen: true,
      bilder: [{ ...BILD, freigegeben: true }, { ...BILD, id: 'b2', freigegeben: false }] }];
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-zweit .sla-btn').click();
    await ruhe();
    const bilder = [...document.querySelectorAll('.sla-bild')];
    expect(bilder.map(b => b.dataset.id)).toEqual(['b1']);
    expect(document.querySelector('.sla-bild-vorschau').getAttribute('src')).toBe('/boards/b1/cards/k1/schleuse-auftraege/z1/bilder/b1');
    expect(document.querySelector('.sla-bild-frei')).toBeNull(); // nur lesend
    expect(document.body.textContent).toContain('1 Bild(er) hinaus');
  });
});
