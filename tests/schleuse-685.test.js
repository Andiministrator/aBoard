/* @vitest-environment happy-dom */

/**
 * Karte #685 (ADR-0134 §3.1–§3.4) — „Extern bearbeiten“, Teil S2:
 *   1. Knopf im Karten-Modal nur bei `schleuse: "an"` und nutzbarem eigenem
 *      Anbieter (oder vorhandenem Entwurf, dann mit Markierung).
 *   2. Auszug erstellen: Der Browser schickt nur die AUSWAHL, nie den Text
 *      von Beschreibung oder Kommentaren.
 *   3. Durchsicht: Platzhalter markiert (fest/Modell/neu), Text editierbar,
 *      Speichern mit Ausgangshash, „Freigeben und senden“ nur für den
 *      gespeicherten Stand (seit S3, Karte #686).
 *   4. Antwort mit Lebenszeichen (HTTP 200, Status im Objekt) wird richtig
 *      gelesen — auch ein Fehler.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));

import { SchleuseAuszug, SchleuseAuszugClient } from '../src/schleuse-auszug.js';
import { I18n } from '../src/i18n.js';

const ruhe = async () => { for (let i = 0; i < 4; i++) await new Promise(r => setTimeout(r, 0)); };

const KARTE = {
  id: 'k1', title: 'Titel', description: 'GEHEIME BESCHREIBUNG von Anna',
  comments: [{ id: 'c1', author: 'Bert', text: 'GEHEIMER KOMMENTAR' }, { id: 'c2', author: 'Anna', text: 'zweiter' }],
};

const AUFTRAG = {
  id: 'a1', board_id: 'b1', card_id: 'k1', freigabe_id: 'f1', anbieter: 'anthropic', modell: 'claude-x',
  zustand: 'entwurf', text: 'Hallo [PERSON_1] von [FIRMA_1] und [PERSON_7] <img src=x onerror=alert(1)>',
  sha256: 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789',
  ersetzungen: [{ platzhalter: '[PERSON_1]', art: 'PERSON', quelle: 'fest', anzahl: 1 },
    { platzhalter: '[FIRMA_1]', art: 'FIRMA', quelle: 'modell', anzahl: 1 }],
  teile: { beschreibung: true, kommentare: ['c1'], aufgabe: false }, freigegeben: false,
  erstellt_am: '2026-10-04T10:00:00.000Z', geaendert_am: '2026-10-04T10:00:00.000Z',
};

describeServer('Extern bearbeiten: Auszug und Durchsicht (Karte #685)', () => {
  let fetchSpy, me, schluessel, liste, post, put;

  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    me = { status: 200, body: { email: 'a@x', schleuse: 'an' } };
    schluessel = { status: 200, body: {
      anbieter: [{ id: 'f1', anbieter: 'anthropic', bezeichnung: '', modelle: ['claude-x', 'claude-y'], nutzbar: true }],
      schluessel: [{ freigabe_id: 'f1' }] } };
    liste = { status: 200, body: { auftraege: [], gesperrt: false, darf_erstellen: true, lokales_modell: true, grenze_zeichen: 20000 } };
    post = { status: 200, body: { status: 201, auftrag: AUFTRAG, entschaerft: 0 } };
    put = { status: 200, body: { auftrag: { ...AUFTRAG, text: 'neu [PERSON_1]', sha256: 'ffff0000ffff0000' } } };
    SchleuseAuszug.vergissSchalter();
    fetchSpy = vi.fn(async (pfad, opt = {}) => {
      const m = opt.method || 'GET';
      let a = { status: 404, body: { error: 'x' } };
      if (pfad === '/me') a = me;
      else if (pfad === '/users/me/schleuse-schluessel') a = schluessel;
      else if (pfad === '/boards/b1/cards/k1/schleuse-auftraege' && m === 'GET') a = liste;
      else if (pfad === '/boards/b1/cards/k1/schleuse-auftraege' && m === 'POST') a = post;
      else if (pfad === '/boards/b1/cards/k1/schleuse-auftraege/a1' && m === 'PUT') a = put;
      else if (pfad === '/users/me/schleuse-auftraege/a1' && m === 'DELETE') a = { status: 200, body: { ok: true } };
      return { ok: a.status < 300, status: a.status, json: async () => a.body };
    });
    vi.stubGlobal('fetch', fetchSpy);
  });
  afterEach(() => { SchleuseAuszug?.close(); vi.unstubAllGlobals(); document.body.innerHTML = ''; });

  const knopf = () => { const b = document.createElement('button'); b.style.display = 'none'; document.body.appendChild(b); return b; };

  it('Knopf: bei „aus“ unsichtbar und ohne weitere Aufrufe', async () => {
    me = { status: 200, body: { email: 'a@x' } };
    const b = knopf();
    await SchleuseAuszug.aktualisiereKnopf(b, KARTE, 'b1');
    expect(b.style.display).toBe('none');
    expect(fetchSpy.mock.calls.map(([p]) => p)).toEqual(['/me']);
  });

  it('Knopf: sichtbar mit nutzbarem eigenem Anbieter, unsichtbar ohne Schlüssel', async () => {
    const b = knopf();
    await SchleuseAuszug.aktualisiereKnopf(b, KARTE, 'b1');
    expect(b.style.display).toBe('');
    expect(b.textContent).toBe('Extern bearbeiten');
    schluessel.body.schluessel = [];
    await SchleuseAuszug.aktualisiereKnopf(b, KARTE, 'b1');
    expect(b.style.display).toBe('none');
    // Viewer (darf_erstellen false) ohne Entwurf: unsichtbar.
    schluessel.body.schluessel = [{ freigabe_id: 'f1' }];
    liste.body.darf_erstellen = false;
    await SchleuseAuszug.aktualisiereKnopf(b, KARTE, 'b1');
    expect(b.style.display).toBe('none');
  });

  it('Knopf: vorhandener Entwurf markiert die Karte für die Person', async () => {
    schluessel.body.schluessel = [];
    liste.body.auftraege = [AUFTRAG];
    const b = knopf();
    await SchleuseAuszug.aktualisiereKnopf(b, KARTE, 'b1');
    expect(b.style.display).toBe('');
    expect(b.textContent).toContain('Entwurf vorhanden');
    expect(b.classList.contains('sla-knopf--entwurf')).toBe(true);
  });

  it('Auszug: der Browser schickt nur die Auswahl; Antwort mit Lebenszeichen → Durchsicht mit Markierung', async () => {
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    expect(document.querySelector('.sla-teil-beschreibung').checked).toBe(true);
    const boxen = document.querySelectorAll('.sla-teil-kommentar');
    expect(boxen.length).toBe(2);
    boxen[0].checked = true;
    document.querySelector('.sla-modell').value = 'claude-y';
    document.querySelector('.sla-aufgabe').value = 'Fasse zusammen';
    document.querySelector('.sla-form .sla-btn--primary').click();
    await ruhe();
    const p = fetchSpy.mock.calls.find(([, o]) => o?.method === 'POST');
    const body = JSON.parse(p[1].body);
    /* Karte #687: `dateien` gehört seit S4 zur Auswahl — hier leer (Karte ohne Anhänge). */
    expect(body).toEqual({ freigabe_id: 'f1', modell: 'claude-y', beschreibung: true, kommentare: ['c1'], aufgabe: 'Fasse zusammen', dateien: [] });
    expect(p[1].body).not.toContain('GEHEIM');

    // Durchsicht: bekannte Platzhalter mit Quelle, unbekannter als „neu“.
    const marks = [...document.querySelectorAll('.sla-vorschau .sla-ph')];
    expect(marks.map(m => [m.textContent, m.className])).toEqual([
      ['[PERSON_1]', 'sla-ph sla-ph--fest'], ['[FIRMA_1]', 'sla-ph sla-ph--modell'], ['[PERSON_7]', 'sla-ph sla-ph--neu']]);
    expect(document.querySelector('.sla-vorschau img')).toBeNull();
    expect(document.querySelector('.sla-vorschau').textContent).toContain('<img src=x');
    expect(document.querySelector('.sla-hash').textContent).toContain('abcdef012345…');
    // Seit S3 (Karte #686): freigeben lässt sich der gespeicherte Stand —
    // mit ungespeicherten Änderungen nicht.
    const senden = document.querySelector('.sla-senden');
    expect(senden.disabled).toBe(false);

    // Bearbeiten: Vorschau folgt, Hash als ungespeichert markiert, Speichern
    // schickt den Ausgangshash mit.
    const feld = document.querySelector('.sla-text');
    feld.value = 'neu [PERSON_1] [ORT_3]';
    feld.dispatchEvent(new Event('input'));
    expect([...document.querySelectorAll('.sla-vorschau .sla-ph')].map(m => m.className))
      .toEqual(['sla-ph sla-ph--fest', 'sla-ph sla-ph--neu']);
    expect(document.querySelector('.sla-hash').textContent).toContain('ungespeichert');
    expect(document.querySelector('.sla-senden').disabled).toBe(true);
    document.querySelector('.sla-knoepfe .sla-btn--primary').click();
    await ruhe();
    const u = fetchSpy.mock.calls.find(([, o]) => o?.method === 'PUT');
    expect(JSON.parse(u[1].body)).toEqual({ text: 'neu [PERSON_1] [ORT_3]', basis_sha256: AUFTRAG.sha256 });
    expect(document.querySelector('.sla-hash').textContent).toContain('ffff0000ffff…');
    expect(document.querySelector('.sla-hash').textContent).not.toContain('ungespeichert');
  });

  it('Fehler nach Lebenszeichen (HTTP 200, Status im Objekt) wird als Fehler gemeldet', async () => {
    post = { status: 200, body: { status: 502, grund: 'lokales-modell-unerreichbar', error: 'x' } };
    const roh = await SchleuseAuszugClient.erstellen('b1', 'k1', {});
    expect([roh.ok, roh.status]).toEqual([false, 502]);
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-form .sla-btn--primary').click();
    await ruhe();
    expect(document.querySelector('.sla-vorschau')).toBeNull();
    expect(document.querySelector('.sla-form .sla-meldung').textContent).toContain('nicht erreichbar');
  });

  it('Ohne lokales Modell oder bei gesperrtem Board kein Formular, sondern ein Hinweis', async () => {
    liste.body.lokales_modell = false;
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    expect(document.querySelector('.sla-form')).toBeNull();
    expect(document.querySelector('.sla-hinweis--stark').textContent).toContain('kein lokales Modell');
    SchleuseAuszug.close();
    liste.body.lokales_modell = true;
    liste.body.gesperrt = true;
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    expect(document.querySelector('.sla-form')).toBeNull();
    expect(document.querySelector('.sla-hinweis--stark').textContent).toContain('gesperrt');
  });

  it('Durchsicht nennt die Zahl der Modellfunde und warnt bei 0 (KS-7)', async () => {
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-form .sla-btn--primary').click();
    await ruhe();
    const funde = document.querySelector('.sla-modell-funde');
    expect(funde.dataset.anzahl).toBe('1');
    expect(funde.classList.contains('sla-modell-funde--keine')).toBe(false);
    SchleuseAuszug.close();
    post = { status: 201, body: { auftrag: { ...AUFTRAG, ersetzungen: [AUFTRAG.ersetzungen[0]] }, entschaerft: 0 } };
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-form .sla-btn--primary').click();
    await ruhe();
    const keine = document.querySelector('.sla-modell-funde');
    expect(keine.dataset.anzahl).toBe('0');
    expect(keine.classList.contains('sla-modell-funde--keine')).toBe(true);
    expect(keine.classList.contains('sla-hinweis--stark')).toBe(true);
  });

  it('Entwurf löschen geht über /users/me — auch ohne Bearbeitungsrecht (F-1619)', async () => {
    liste.body.auftraege = [AUFTRAG];
    liste.body.darf_erstellen = false;
    vi.stubGlobal('confirm', () => true);
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-entwurf .sla-btn--danger').click();
    await ruhe();
    const d = fetchSpy.mock.calls.find(([, o]) => o?.method === 'DELETE');
    expect(d[0]).toBe('/users/me/schleuse-auftraege/a1');
  });
});
