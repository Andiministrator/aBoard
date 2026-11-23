/* @vitest-environment happy-dom */

/**
 * Karte #783 (ADR-0140 §8): Band „Vorschläge des internen Agenten“ auf dem
 * externen Board — nur Seite außen und Rolle editor (403 → kein Band, keine
 * Uhr), Texte des Agenten nur als Text, Bestätigen schickt den gezeigten
 * Hash, Ablehnen per DELETE, Fehler sichtbar.
 *
 * Server-only (`describeServer`); der Gegenfall ohne Server-Build läuft in den
 * anderen Kontexten (`describeOhneServer`).
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer, describeOhneServer } from './helpers/build-flags.js';

let seite = 'aussen';
vi.mock('../src/boardpaar.js', async (orig) => ({ ...(await orig()),
  boardpaarSeite: () => Promise.resolve(seite),
  vergissBoardpaarSeite: () => {},
}));

const { VorschlaegeAussen, feldName, zeitKurz } = await import('../src/vorschlaege-aussen.js');
const { State } = await import('../src/state.js');
const { I18n } = await import('../src/i18n.js');

const ruhe = () => new Promise(r => setTimeout(r, 0));
async function ruhig() { for (let i = 0; i < 8; i++) await ruhe(); }

const BOESE = '<img src=x onerror="window.__xss=1">**fett**';
const V1 = {
  id: 'v1', art: 'kommentar', artText: 'Kommentar', agent: 'agent:gerd', fuer: 'Anna', hash: 'h1',
  angelegt: '2026-10-11T10:00:00Z', laeuftAb: '2026-10-12T10:00:00Z',
  karte: { displayId: 5, titel: 'Im Sprint' }, karteFehlt: false,
  felder: [{ schluessel: 'text', name: 'Kommentar', wert: BOESE }],
};
const V2 = {
  id: 'v2', art: 'karte.loeschen', agent: 'agent:gerd', fuer: '', hash: 'h2', laeuftAb: '2026-10-12T10:00:00Z',
  karte: null, karteFehlt: true, felder: [{ schluessel: 'wirkung-loeschen', name: 'Wirkung', wert: 'Die Karte wird gelöscht.' }],
};
const V3 = { ...V1, id: 'v3', hash: 'h3', zugangAktiv: false };

let abrufe;
let liste;
let listeStatus;
let aktionStatus;

function json(status, body) {
  return { status, ok: status >= 200 && status < 300, json: async () => body };
}

beforeEach(() => {
  I18n.setLang('de');
  seite = 'aussen';
  abrufe = [];
  liste = [V1, V2];
  listeStatus = 200;
  aktionStatus = 200;
  delete window.__xss;
  document.body.innerHTML = '<div id="app"><header id="board-header"></header><main id="board-columns"></main></div>';
  vi.stubGlobal('fetch', vi.fn(async (url, init) => {
    abrufe.push([String(url), init ?? {}]);
    if ((init?.method ?? 'GET') === 'GET') return json(listeStatus, { vorschlaege: liste });
    if (aktionStatus !== 200) return json(aktionStatus, { error: 'Der Vorschlag wurde nicht ausgeführt.', details: { error: 'Karte geändert' } });
    liste = liste.filter(v => !String(url).includes('/' + v.id));
    return json(200, { ergebnis: 'ok' });
  }));
});

afterEach(() => {
  VorschlaegeAussen._resetForTest();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const bereich = () => document.getElementById('vorschlaege-aussen');
const eintraege = () => [...document.querySelectorAll('#vorschlaege-aussen .vsl-eintrag')];
const listenAbrufe = () => abrufe.filter(([, i]) => (i.method ?? 'GET') === 'GET');

function setzeBoard(id = 'ext1') {
  State.set({ version: '2', board: { id, title: 'Extern', sprints: [] } });
}

describeServer('#783 Band „Vorschläge“', () => {
  it('zeigt Vorschläge unter dem Kopf, Texte des Agenten nur als Text', async () => {
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    expect(listenAbrufe()[0][0]).toBe('/boards/ext1/vorschlaege');
    expect(bereich()).not.toBeNull();
    expect(bereich().previousElementSibling.id).toBe('board-header');
    const e = eintraege();
    expect(e).toHaveLength(2);
    expect(e[0].textContent).toContain('agent:gerd');
    expect(e[0].textContent).toContain('laut Agent gefragt für Anna');
    expect(e[0].textContent).toContain('Karte #5 „Im Sprint“');
    const dd = e[0].querySelector('.vsl-felder dd');
    expect(dd.textContent).toBe(BOESE);           /* wörtlich, kein Markdown */
    expect(dd.querySelector('img')).toBeNull();    /* kein HTML */
    expect(window.__xss).toBeUndefined();
    /* Feste Wirkungstexte übersetzt; fehlende Karte sperrt das Bestätigen. */
    expect(e[1].textContent).toContain('mit Kommentaren und Anhängen gelöscht');
    expect(e[1].textContent).toContain('Die Karte gibt es nicht mehr');
    expect(e[1].querySelector('.vsl-ja').disabled).toBe(true);
    expect(e[1].querySelector('.vsl-nein').disabled).toBe(false);
  });

  it('widerrufener Zugang: Bestätigen gesperrt, mit Hinweis (K-A9)', async () => {
    liste = [V3];
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    const e = eintraege()[0];
    expect(e.textContent).toContain('Zugang des Agenten ist widerrufen');
    expect(e.querySelector('.vsl-ja').disabled).toBe(true);
    expect(e.querySelector('.vsl-nein').disabled).toBe(false);
  });

  it('Bestätigen schickt den gezeigten Hash per POST, danach neu geladen', async () => {
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    eintraege()[0].querySelector('.vsl-ja').click();
    await ruhig();
    const post = abrufe.find(([, i]) => i.method === 'POST');
    expect(post[0]).toBe('/boards/ext1/vorschlaege/v1/bestaetigen');
    expect(JSON.parse(post[1].body)).toEqual({ hash: 'h1' });
    expect(listenAbrufe()).toHaveLength(2);
    expect(eintraege()).toHaveLength(1);
    expect(bereich().querySelector('.vsl-meldung').textContent).toBe('Vorschlag ausgeführt.');
  });

  it('Ablehnen per DELETE; Fehler des Servers wird angezeigt, der Vorschlag bleibt', async () => {
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    aktionStatus = 409;
    eintraege()[0].querySelector('.vsl-nein').click();
    await ruhig();
    const del = abrufe.find(([, i]) => i.method === 'DELETE');
    expect(del[0]).toBe('/boards/ext1/vorschlaege/v1');
    expect(bereich().querySelector('.vsl-meldung').textContent).toContain('Nicht ausgeführt');
    expect(bereich().querySelector('.vsl-meldung').textContent).toContain('Karte geändert');
    expect(eintraege()).toHaveLength(2);
  });

  it('403 (nicht editor): kein Band und keine Minuten-Uhr', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    listeStatus = 403;
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    expect(bereich()).toBeNull();
    vi.advanceTimersByTime(5 * 60_000);
    await ruhig();
    expect(listenAbrufe()).toHaveLength(1);
  });

  it('Seite innen/aus: kein Abruf', async () => {
    for (const s of ['innen', 'aus']) {
      seite = s;
      VorschlaegeAussen.init();
      setzeBoard('b-' + s);
      await ruhig();
      expect(listenAbrufe()).toHaveLength(0);
      expect(bereich()).toBeNull();
      VorschlaegeAussen._resetForTest();
    }
  });

  it('leere Liste: kein Band; Board-Wechsel räumt das Band ab', async () => {
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    expect(bereich()).not.toBeNull();
    liste = [];
    setzeBoard('ext2');
    await ruhig();
    expect(listenAbrufe().at(-1)[0]).toBe('/boards/ext2/vorschlaege');
    expect(bereich()).toBeNull();
  });

  it('Fokus und Scrollstand bleiben beim Minuten-Abruf ohne Änderung und nach dem Neubau (K-B2)', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    const nein = eintraege()[1].querySelector('.vsl-nein');
    nein.focus();
    const live = bereich().querySelector('[role="status"]');
    vi.advanceTimersByTime(60_000);
    await ruhig();
    expect(listenAbrufe()).toHaveLength(2);
    expect(eintraege()[1].querySelector('.vsl-nein')).toBe(nein); /* kein Neubau */
    expect(document.activeElement).toBe(nein);
    /* Liste ändert sich (v1 weg): Neubau, Fokus wandert auf denselben Knopf von v2. */
    liste = [V2];
    vi.advanceTimersByTime(60_000);
    await ruhig();
    expect(eintraege()).toHaveLength(1);
    expect(document.activeElement.classList.contains('vsl-nein')).toBe(true);
    expect(document.activeElement.dataset.vid).toBe('v2');
    expect(bereich().querySelector('[role="status"]')).toBe(live); /* Live-Region bleibt dasselbe Element */
  });

  it('Doppelklick wirkt einmal; während der Aktion sind beide Knöpfe gesperrt', async () => {
    let freigeben;
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    globalThis.fetch.mockImplementationOnce(async (url, init) => {
      abrufe.push([String(url), init ?? {}]);
      await new Promise(r => { freigeben = r; });
      return json(200, {});
    });
    const ja = eintraege()[0].querySelector('.vsl-ja');
    ja.click();
    ja.click();
    await ruhe();
    expect(abrufe.filter(([, i]) => i.method === 'POST')).toHaveLength(1);
    expect(eintraege()[0].querySelector('.vsl-nein').disabled).toBe(true);
    freigeben();
    await ruhig();
    expect(abrufe.filter(([, i]) => i.method === 'POST')).toHaveLength(1);
  });

  it('Board-Wechsel während der Aktion: Meldung wandert nicht mit (K-B3)', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    let freigeben;
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    globalThis.fetch.mockImplementationOnce(async (url, init) => {
      abrufe.push([String(url), init ?? {}]);
      await new Promise(r => { freigeben = r; });
      return json(200, {});
    });
    eintraege()[0].querySelector('.vsl-ja').click();
    await ruhe();
    setzeBoard('ext2');
    await ruhig();
    /* Auf ext2 läuft gleich eine eigene Aktion — die alte darf sie nicht entsperren. */
    let freigeben2;
    globalThis.fetch.mockImplementationOnce(async (url, init) => {
      abrufe.push([String(url), init ?? {}]);
      await new Promise(r => { freigeben2 = r; });
      return json(200, {});
    });
    eintraege()[1].querySelector('.vsl-nein').click();
    await ruhe();
    freigeben();
    await ruhig();
    vi.advanceTimersByTime(60_000); /* Minuten-Uhr: während einer Aktion kein Abruf, kein Entsperren */
    await ruhig();
    expect(bereich().querySelector('[role="status"]').textContent).toBe('');
    expect(eintraege()[0].querySelector('.vsl-ja').disabled).toBe(true); /* ext2-Aktion läuft noch */
    eintraege()[0].querySelector('.vsl-ja').click(); /* gesperrt: kein zweiter Aufruf */
    await ruhe();
    expect(abrufe.filter(([, i]) => i.method === 'POST')).toHaveLength(1);
    freigeben2();
    await ruhig();
    expect(bereich().querySelector('[role="status"]').textContent).toBe('Vorschlag abgelehnt und gelöscht.');
  });

  it('eine ältere Antwort überschreibt die jüngere nicht (K-B3)', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    let freigeben;
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    /* Minuten-Abruf hängt und liefert dann noch den alten Stand mit v1. */
    globalThis.fetch.mockImplementationOnce(async (url, init) => {
      abrufe.push([String(url), init ?? {}]);
      await new Promise(r => { freigeben = r; });
      return json(200, { vorschlaege: [V1, V2] });
    });
    vi.advanceTimersByTime(60_000);
    await ruhe();
    eintraege()[0].querySelector('.vsl-ja').click(); /* bestätigt v1, jüngerer Abruf liefert [V2] */
    await ruhig();
    freigeben();
    await ruhig();
    expect(eintraege().map(e => e.querySelector('.vsl-ja').dataset.vid)).toEqual(['v2']);
    expect(bereich().querySelector('[role="status"]').textContent).toBe('Vorschlag ausgeführt.');
  });

  it('Netzfehler beim Abruf: alter Stand bleibt mit Hinweis „womöglich nicht aktuell“', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    globalThis.fetch.mockImplementationOnce(async () => { throw new TypeError('offline'); });
    vi.advanceTimersByTime(60_000);
    await ruhig();
    expect(eintraege()).toHaveLength(2);
    expect(bereich().querySelector('[role="status"]').textContent).toContain('nicht aktuell');
  });

  it('erster Abruf scheitert: Band mit Hinweis statt nichts (Sprint-91-Kritiker F-E)', async () => {
    listeStatus = 500;
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    expect(bereich()).not.toBeNull();
    expect(bereich().querySelector('[role="status"]').textContent).toContain('nicht aktuell');
    expect(eintraege()).toHaveLength(0);
  });

  it('Knöpfe nennen Art und Karte für Screenreader (Sprint-91-Kritiker F-D)', async () => {
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    const e = eintraege();
    expect(e[0].querySelector('.vsl-ja').getAttribute('aria-label')).toBe('Bestätigen: Kommentar, Karte #5 „Im Sprint“');
    expect(e[0].querySelector('.vsl-nein').getAttribute('aria-label')).toContain('Ablehnen: Kommentar');
    expect(e[1].querySelector('.vsl-ja').getAttribute('aria-label')).not.toBe(e[0].querySelector('.vsl-ja').getAttribute('aria-label'));
  });

  it('EN: Fehler übersetzt nach Status, ohne den deutschen Servertext (K-B4); ID kodiert', async () => {
    I18n.setLang('en');
    liste = [{ ...V1, id: 'a/b c' }];
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    aktionStatus = 410;
    eintraege()[0].querySelector('.vsl-ja').click();
    await ruhig();
    expect(abrufe.find(([, i]) => i.method === 'POST')[0]).toBe('/boards/ext1/vorschlaege/a%2Fb%20c/bestaetigen');
    const text = bereich().querySelector('[role="status"]').textContent;
    expect(text).toContain('expired');
    expect(text).not.toContain('Vorschlag wurde nicht');
    expect(document.querySelectorAll('#vsl-styles')).toHaveLength(1);
  });

  it('404 (nicht außen) beim ersten Abruf: kein Band, keine Uhr', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    listeStatus = 404;
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    vi.advanceTimersByTime(3 * 60_000);
    await ruhig();
    expect(listenAbrufe()).toHaveLength(1);
    expect(bereich()).toBeNull();
  });

  it('Fokus nach Bestätigen bleibt im Band (Verify V-8)', async () => {
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    eintraege()[0].querySelector('.vsl-ja').focus();
    eintraege()[0].querySelector('.vsl-ja').click();
    /* Echte Browser nehmen einem gesperrten Knopf den Fokus; happy-dom tut das nicht
       (auch blur() wirkt dort nicht) — nachgebildet mit einem Fokus außerhalb des Bands,
       sonst prüfte der Test nichts. */
    const draussen = document.createElement('input');
    document.body.appendChild(draussen);
    draussen.focus();
    expect(bereich().contains(document.activeElement)).toBe(false);
    await ruhig();
    expect(bereich().contains(document.activeElement)).toBe(true);
  });

  it('umbenannte Spalte im Feldwert wird neu gezeichnet (Verify V-9)', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    const mitSpalte = { ...V1, id: 'v4', hash: 'h4', art: 'karte.anlegen', karte: null, felder: [{ schluessel: 'spalte', name: 'Spalte', wert: 'Ready' }] };
    liste = [mitSpalte];
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    expect(eintraege()[0].textContent).toContain('Ready');
    liste = [{ ...mitSpalte, felder: [{ schluessel: 'spalte', name: 'Spalte', wert: 'Bereit' }] }];
    vi.advanceTimersByTime(60_000);
    await ruhig();
    expect(eintraege()[0].textContent).toContain('Bereit');
  });

  it('die Minuten-Uhr wartet auf den Abruf nach einer Aktion, die Meldung bleibt (Verify V-10)', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    let freigeben;
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    eintraege()[0].querySelector('.vsl-ja').click();
    /* Der Abruf NACH der Aktion hängt. */
    globalThis.fetch.mockImplementationOnce(async (url, init) => {
      abrufe.push([String(url), init ?? {}]);
      await new Promise(r => { freigeben = r; });
      return json(200, { vorschlaege: liste });
    });
    await ruhig();
    const vorher = listenAbrufe().length;
    vi.advanceTimersByTime(60_000);
    await ruhig();
    expect(listenAbrufe()).toHaveLength(vorher); /* kein Minuten-Abruf dazwischen */
    freigeben();
    await ruhig();
    expect(bereich().querySelector('[role="status"]').textContent).toBe('Vorschlag ausgeführt.');
  });

  it('Feldnamen übersetzt (EN), karte.text mit „Neu“, unbekannte mit Servernamen', () => {
    I18n.setLang('en');
    expect(feldName('kommentar', { schluessel: 'text', name: 'Kommentar' })).toBe('Comment');
    expect(feldName('karte.text', { schluessel: 'titel', name: 'Neuer Titel' })).toBe('New title');
    expect(feldName('kommentar', { schluessel: 'unbekannt', name: 'X' })).toBe('X');
    expect(zeitKurz('kaputt')).toBe('');
  });
});

describeOhneServer('#783 ohne Server-Build', () => {
  it('init macht nichts: kein Abruf, kein Band', async () => {
    VorschlaegeAussen.init();
    setzeBoard();
    await ruhig();
    expect(listenAbrufe()).toHaveLength(0);
    expect(bereich()).toBeNull();
  });
});
