/* @vitest-environment happy-dom */

/**
 * Karte #686 (ADR-0134 §3.4–§3.7) — „Extern bearbeiten“, Teil S3:
 *   1. „Freigeben und senden“ schickt den Hash des gespeicherten Stands, erst
 *      nach Bestätigung des Verantwortungssatzes.
 *   2. Gesendete Aufträge stehen mit Zustand im Dialog, der Knopf zeigt
 *      „wartet auf externe Antwort“.
 *   3. Vier-Augen: Eine zweite Person sieht den Auszug nur lesend und gibt ihn
 *      mit seinem Hash frei.
 *   4. Antwortkommentar „extern“: Kennzeichnung, „weicht vom Original ab“,
 *      aufklappbarer Auszug — alles als Text, nie als HTML.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));

import { SchleuseAuszug } from '../src/schleuse-auszug.js';
import { I18n } from '../src/i18n.js';

const ruhe = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0)); };

const KARTE = { id: 'k1', title: 'Titel', description: 'Text', comments: [] };
const SHA = 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789';
const AUFTRAG = {
  id: 'a1', board_id: 'b1', card_id: 'k1', freigabe_id: 'f1', anbieter: 'anthropic', modell: 'claude-x',
  zustand: 'entwurf', text: 'Hallo [PERSON_1]', sha256: SHA,
  ersetzungen: [{ platzhalter: '[PERSON_1]', art: 'PERSON', quelle: 'fest', anzahl: 1 }],
  teile: { beschreibung: true, kommentare: [], aufgabe: false }, freigegeben: false, vier_augen: false,
  veraenderte_platzhalter: [], erstellt_am: '2026-10-05T10:00:00.000Z', geaendert_am: '2026-10-05T10:00:00.000Z',
};

describeServer('Extern bearbeiten: Freigabe, Versand, Antwort (Karte #686)', () => {
  let fetchSpy, liste, freigabe, zweit, beleg;

  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    liste = { status: 200, body: { auftraege: [AUFTRAG], zur_freigabe: [], gesperrt: false, darf_erstellen: true, lokales_modell: true, grenze_zeichen: 20000 } };
    freigabe = { status: 200, body: { auftrag: { ...AUFTRAG, zustand: 'freigegeben', freigegeben: true } } };
    zweit = { status: 200, body: { auftrag: { id: 'z1' } } };
    beleg = { status: 200, body: { beleg: { absender: 'anna@x', freigegeben_von: 'anna@x', zweit_freigegeben_von: 'bert@x',
      gesendet_am: '2026-10-05T10:05:00.000Z', sha256: SHA, auszug: 'Auszug [PERSON_1] <b>fett</b>', abweichend: false,
      kommentar_id: 'c9', kommentar_vorhanden: true,
      veraenderte_platzhalter: ['[Person_2]'], input_tokens: 11, output_tokens: 7, kosten_usd: 0.0123 } } };
    SchleuseAuszug.vergissSchalter();
    fetchSpy = vi.fn(async (pfad, opt = {}) => {
      const m = opt.method || 'GET';
      let a = { status: 404, body: { error: 'x' } };
      if (pfad === '/me') a = { status: 200, body: { schleuse: 'an' } };
      else if (pfad === '/users/me/schleuse-schluessel') a = { status: 200, body: {
        anbieter: [{ id: 'f1', anbieter: 'anthropic', modelle: ['claude-x'], nutzbar: true }], schluessel: [{ freigabe_id: 'f1' }] } };
      else if (pfad === '/boards/b1/cards/k1/schleuse-auftraege' && m === 'GET') a = liste;
      else if (pfad === '/boards/b1/cards/k1/schleuse-auftraege/a1/freigabe' && m === 'POST') a = freigabe;
      else if (pfad === '/boards/b1/cards/k1/schleuse-auftraege/z1/zweitfreigabe' && m === 'POST') a = zweit;
      else if (pfad === '/boards/b1/cards/k1/schleuse-belege/a1' && m === 'GET') a = beleg;
      return { ok: a.status < 300, status: a.status, json: async () => a.body };
    });
    vi.stubGlobal('fetch', fetchSpy);
  });
  afterEach(() => { SchleuseAuszug?.close(); vi.unstubAllGlobals(); vi.restoreAllMocks(); document.body.innerHTML = ''; });

  const aufrufe = (endung) => fetchSpy.mock.calls.filter(([p, o]) => p.endsWith(endung) && o?.method === 'POST');

  it('Freigeben: erst nach Bestätigung, mit dem Hash des gespeicherten Stands', async () => {
    const confirm = vi.fn(() => false);
    vi.stubGlobal('confirm', confirm);
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    document.querySelector('.sla-entwurf .sla-btn').click(); // Öffnen
    await ruhe();
    expect(document.querySelector('.sla-verantwortung').textContent).toContain('verantwortest du');
    const senden = document.querySelector('.sla-senden');
    senden.click();
    await ruhe();
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(aufrufe('/freigabe')).toHaveLength(0);
    confirm.mockImplementation(() => true);
    senden.click();
    await ruhe();
    const [[, opt]] = aufrufe('/freigabe');
    expect(JSON.parse(opt.body)).toEqual({ sha256: SHA });
    expect(document.querySelector('.sla-meldung').textContent).toContain('im Hintergrund');
  });

  it('Gesendete Aufträge stehen mit Zustand da; der Knopf zeigt „wartet auf externe Antwort“', async () => {
    liste.body.auftraege = [
      { ...AUFTRAG, id: 'g1', zustand: 'gesendet', gesendet_am: '2026-10-05T10:05:00.000Z' },
      { ...AUFTRAG, id: 'g2', zustand: 'fehlgeschlagen', fehler_grund: 'unterbrochen' },
      { ...AUFTRAG, id: 'g3', zustand: 'beantwortet', antwort_text: 'Antwort <script>x</script>', veraenderte_platzhalter: ['[PERSON_9]'] },
    ];
    const b = document.createElement('button');
    document.body.appendChild(b);
    await SchleuseAuszug.aktualisiereKnopf(b, KARTE, 'b1');
    expect(b.textContent).toContain('wartet auf externe Antwort');
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    const zeilen = [...document.querySelectorAll('.sla-auftrag')].map(z => z.textContent);
    expect(zeilen[0]).toContain('wartet auf externe Antwort');
    expect(zeilen[1]).toContain('Server wurde während des Versands beendet');
    document.querySelector('.sla-auftrag[data-id="g3"] .sla-btn').click();
    await ruhe();
    expect(document.querySelector('.sla-antwort').textContent).toContain('<script>x</script>');
    expect(document.querySelector('.sla-antwort script')).toBeNull();
    expect(document.querySelector('.sla-veraendert').textContent).toContain('[PERSON_9]');
  });

  it('Vier-Augen: lesende Durchsicht und zweite Freigabe mit dem gezeigten Hash', async () => {
    vi.stubGlobal('confirm', vi.fn(() => true));
    liste.body.auftraege = [];
    liste.body.zur_freigabe = [{ id: 'z1', antragsteller: 'anna@x', anbieter: 'anthropic', modell: 'claude-x',
      zustand: 'freigegeben', text: 'Bitte [PERSON_1]', sha256: SHA, ersetzungen: [], vier_augen: true }];
    SchleuseAuszug.open('b1', KARTE);
    await ruhe();
    expect(document.querySelector('.sla-zweit').textContent).toContain('anna@x');
    document.querySelector('.sla-zweit .sla-btn').click();
    await ruhe();
    expect(document.querySelector('.sla-text')).toBeNull(); // nicht bearbeitbar
    expect(document.querySelector('.sla-vorschau').textContent).toBe('Bitte [PERSON_1]');
    document.querySelector('.sla-zweit-freigeben').click();
    await ruhe();
    const [[, opt]] = aufrufe('/zweitfreigabe');
    expect(JSON.parse(opt.body)).toEqual({ sha256: SHA });
  });

  it('Antwortkommentar „extern“: Kennzeichnung, Abweichung, Auszug als Text', async () => {
    const kommentar = { id: 'c9', kind: 'extern', authorEmail: 'anna@x', text: 'Antwort',
      schleuse: { auftragId: 'a1', anbieter: 'anthropic', modell: 'claude-x', gesendetAm: '2026-10-05T10:05:00.000Z' } };
    let box = SchleuseAuszug.externBeleg(kommentar, 'b1', 'k1');
    document.body.appendChild(box);
    await ruhe();
    expect(box.querySelector('.sla-extern-marke').textContent).toContain('anthropic / claude-x');
    expect(box.querySelector('.sla-extern-marke').textContent).toContain('anna@x');
    expect(box.querySelector('.sla-extern-abweichend').hidden).toBe(true);
    expect(box.querySelector('.sla-extern-unbelegt').hidden).toBe(true);
    box.querySelector('.sla-extern-knopf').click();
    await ruhe();
    const inhalt = box.querySelector('.sla-extern-beleg');
    expect(inhalt.hidden).toBe(false);
    expect(inhalt.textContent).toContain('anna@x + bert@x');
    expect(inhalt.textContent).toContain('[Person_2]');
    expect(inhalt.querySelector('b')).toBeNull();
    expect(inhalt.textContent).toContain('<b>fett</b>');

    // Kommentar verändert → neuer Abruf, der Server meldet die Abweichung.
    beleg.body.beleg.abweichend = true;
    box = SchleuseAuszug.externBeleg({ ...kommentar, text: 'Gefälscht' }, 'b1', 'k1');
    document.body.appendChild(box);
    await ruhe();
    expect(box.querySelector('.sla-extern-abweichend').hidden).toBe(false);

    // Kritiker KS3-1: Beleg gehört zu einem anderen Kommentar (Kopie,
    // eingeschleust) → sichtbar „nicht verifiziert“.
    beleg.body.beleg = { ...beleg.body.beleg, abweichend: false, kommentar_vorhanden: true, kommentar_id: 'anders' };
    box = SchleuseAuszug.externBeleg({ ...kommentar, text: 'Dritter Text' }, 'b1', 'k1');
    document.body.appendChild(box);
    await ruhe();
    expect(box.querySelector('.sla-extern-unbelegt').hidden).toBe(false);

    // Ohne Beleg auf dem Server: Hinweis statt Inhalt.
    beleg = { status: 404, body: { grund: 'auftrag-unbekannt' } };
    box = SchleuseAuszug.externBeleg({ ...kommentar, text: 'Noch anders' }, 'b1', 'k1');
    document.body.appendChild(box);
    box.querySelector('.sla-extern-knopf').click();
    await ruhe();
    expect(box.querySelector('.sla-extern-beleg').textContent).toContain('keinen Beleg');
    expect(box.querySelector('.sla-extern-unbelegt').hidden).toBe(false);
  });

  it('Nachfrage baut das Formular nicht neu, solange darin etwas getippt ist (KS3-6)', async () => {
    vi.useFakeTimers();
    try {
      liste.body.auftraege = [{ ...AUFTRAG, id: 'g1', zustand: 'gesendet', gesendet_am: '2026-10-05T10:05:00.000Z' }];
      SchleuseAuszug.open('b1', KARTE);
      await vi.advanceTimersByTimeAsync(10);
      const feld = document.querySelector('.sla-aufgabe');
      feld.value = 'Halbe Aufgabe';
      const vorher = fetchSpy.mock.calls.length;
      await vi.advanceTimersByTimeAsync(4100);
      expect(document.querySelector('.sla-aufgabe')).toBe(feld);
      expect(feld.value).toBe('Halbe Aufgabe');
      feld.value = '';
      await vi.advanceTimersByTimeAsync(4100);
      expect(fetchSpy.mock.calls.length).toBeGreaterThan(vorher); // ohne Eingabe wird nachgefragt
    } finally {
      vi.useRealTimers();
    }
  });

  it('Ein fehlgeschlagener Beleg wird nicht gemerkt, sondern nach 5 s erneut geholt (VS3-1)', async () => {
    vi.useFakeTimers();
    try {
      const kommentar = { id: 'c9', kind: 'extern', authorEmail: 'anna@x', text: 'Ganz neuer Text',
        schleuse: { auftragId: 'a1', anbieter: 'anthropic', modell: 'claude-x' } };
      const echt = beleg;
      beleg = { status: 503, body: {} };
      const box = SchleuseAuszug.externBeleg(kommentar, 'b1', 'k1');
      document.body.appendChild(box);
      await vi.advanceTimersByTimeAsync(10);
      expect(box.querySelector('.sla-extern-unbelegt').hidden).toBe(false);
      beleg = echt;
      await vi.advanceTimersByTimeAsync(5100);
      expect(box.querySelector('.sla-extern-unbelegt').hidden).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it('Solange der Beleg-Abruf hängt, steht „wird geprüft“, nie eine stille Bestätigung (SK-3)', async () => {
    fetchSpy.mockImplementation(() => new Promise(() => {}));
    const box = SchleuseAuszug.externBeleg({ id: 'c9', kind: 'extern', text: 'Hängt', schleuse: { auftragId: 'a1' } }, 'b1', 'k1');
    document.body.appendChild(box);
    await ruhe();
    const marke = box.querySelector('.sla-extern-unbelegt');
    expect(marke.hidden).toBe(false);
    expect(marke.textContent).toContain('wird geprüft');
  });
});
