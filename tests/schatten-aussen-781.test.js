/* @vitest-environment happy-dom */

/**
 * Karte #781 (ADR-0140 §5): Schatten interner Karten auf dem externen Board —
 * Band „Intern“, Klick-Hinweis (C-3), Aktualität, SSE-Abonnent, Suche.
 *
 * Server-only (`describeServer`): das Modul gibt es nur im Server-Build. Der
 * Gegenfall — ohne Server-Build kein Abruf und kein UI — läuft in den anderen
 * Kontexten (`describeOhneServer`).
 *
 * Gegenproben (von Hand gefahren, im Bericht der Karte): Seiten-Prüfung
 * entfernen → „aus/innen“ rot; Hinweis beim Laden statt beim Klick → „kein
 * Probe-Abruf ohne Klick“ rot; Berechtigungs-Prüfung entfernen → „prompt“ rot;
 * Abonnent nicht schließen → „Board-Wechsel“ rot.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer, describeOhneServer } from './helpers/build-flags.js';

let seite = 'aussen';
vi.mock('../src/boardpaar.js', async (orig) => ({ ...(await orig()),
  boardpaarSeite: () => Promise.resolve(seite),
  vergissBoardpaarSeite: () => {},
}));

const { SchattenAussen, standAnzeige, minutenSeit, schattenTrifft, sichererLink, metaText, schattenAlsKalenderEintraege, STAND_WARN_MIN } =
  await import('../src/schatten-aussen.js');
const { State } = await import('../src/state.js');
const { Search } = await import('../src/search.js');
const { I18n } = await import('../src/i18n.js');

const ruhe = () => new Promise(r => setTimeout(r, 0));
async function ruhig() { for (let i = 0; i < 6; i++) await ruhe(); }

const BASIS = 'https://innen.example/#board=innen-ext1';
const vorMin = (n) => new Date(Date.now() - n * 60_000).toISOString();
const EINTRAEGE = [
  { schatten_id: 'S1', art: 'karte', nummer: 'i-42', titel: 'Angebot Müller', spalte: 'In Arbeit', prioritaet: 'hoch',
    ende_datum: '2026-10-22', bearbeiter: ['Gerd Muster'], von_agent: false, link: BASIS + '&card=42' },
  { schatten_id: 'S2', art: 'karte', nummer: 'i-43', titel: 'Interne Karte', titel_platzhalter: true, bearbeiter: [], von_agent: true, link: BASIS + '&card=43' },
  { schatten_id: 'S3', art: 'termin', titel: 'Jour fixe', start_datum: '2026-10-24', start_zeit: '10:00', ende_datum: '2026-10-24',
    ende_zeit: '11:00', bearbeiter: [], von_agent: true, link: BASIS },
];

let abrufe;          /* alle fetch-Aufrufe: [url, init] */
let antwort;         /* was /schatten liefert: () => Response-ähnlich */
let es;              /* erzeugte EventSource-Stubs */

class EsStub {
  constructor(url) { this.url = url; this.geschlossen = false; this.readyState = 1; this.h = {}; es.push(this); }
  addEventListener(typ, fn) { (this.h[typ] ??= []).push(fn); }
  close() { this.geschlossen = true; this.readyState = 2; }
  feuere(typ) { (this.h[typ] ?? []).forEach(fn => fn({})); }
}

function json(status, body) {
  return { status, ok: status >= 200 && status < 300, json: async () => body };
}

function setzeBoard(id = 'ext1') {
  State.set({ version: '2', board: { id, title: 'Extern', sprints: [] } });
}

beforeEach(() => {
  I18n.setLang('de');
  seite = 'aussen';
  abrufe = [];
  es = [];
  antwort = () => json(200, { letzter_abgleich: vorMin(10), schatten: EINTRAEGE });
  document.body.innerHTML = '<div id="app"><header id="board-header"></header><main id="board-columns"></main></div>';
  vi.stubGlobal('EventSource', EsStub);
  vi.stubGlobal('fetch', vi.fn(async (url, init) => {
    abrufe.push([String(url), init]);
    if (String(url).includes('/schatten')) return antwort(url);
    return { status: 200, ok: true, type: 'opaque' }; /* /healthz */
  }));
  Search.clear();
});

afterEach(() => {
  SchattenAussen._resetForTest();
  document.querySelectorAll('.sch-hinweis').forEach(n => n.remove());
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const bereich = () => document.getElementById('schatten-aussen');
const zeilen = () => [...document.querySelectorAll('#schatten-aussen .sch-zeile')];
const schattenAbrufe = () => abrufe.filter(([u]) => u.includes('/schatten'));
const probeAbrufe = () => abrufe.filter(([u]) => u.includes('/healthz'));

describeServer('#781 Band „Intern“ auf dem externen Board', () => {
  it('zeigt die Schatten ausgegraut, nicht ziehbar, mit Nummer, 🤖 und „Interne Karte“', async () => {
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    expect(schattenAbrufe()).toHaveLength(1);
    expect(schattenAbrufe()[0][0]).toBe('/boards/ext1/schatten');
    expect(bereich()).not.toBeNull();
    expect(bereich().previousElementSibling.id).toBe('board-header'); /* über dem Board */
    expect(bereich().hidden).toBe(false);
    const z = zeilen();
    expect(z).toHaveLength(3);
    expect(z.every(b => b.getAttribute('draggable') === 'false' && b.type === 'button')).toBe(true);
    expect(z[0].querySelector('.sch-nr').textContent).toBe('i-42');
    expect(z[0].textContent).toContain('Angebot Müller');
    expect(z[0].textContent).toContain('In Arbeit · hoch · 22.10.');
    expect(z[0].querySelector('.sch-agent')).toBeNull();
    expect(z[1].querySelector('.sch-agent').textContent).toBe('🤖');
    /* F-2029: „geändert“, nicht „angelegt“ (ADR-0140 §2). */
    expect(z[1].querySelector('.sch-agent').title).toBe('Titel zuletzt von einem Agenten geändert');
    /* F-2027: keine Deckkraft unter 1 auf Zeile und Metazeile (WCAG 4,5:1). */
    const css = document.getElementById('sch-aussen-styles').textContent;
    expect(css).not.toMatch(/\.sch-(zeile|meta)\{[^}]*opacity/);
    expect(z[1].querySelector('.sch-titel').textContent).toBe('Interne Karte');
    expect(z[2].textContent).toContain('Termin');
    /* Sprint-90-Kritik F-G: auch die Termin-Variante der Agent-Marke. */
    expect(z[2].querySelector('.sch-agent').title).toBe('Zuletzt von einem Agenten geändert');
    /* Nicht editierbar: kein Modal, kein Eingabefeld, kein Link im Band. */
    expect(document.querySelector('.modal-overlay, .modal')).toBeNull();
    expect(bereich().querySelector('input, textarea, a[href]')).toBeNull();
  });

  it('auf aus und innen: kein Band, kein Abruf, kein Abonnent', async () => {
    for (const s of ['aus', 'innen']) {
      seite = s;
      SchattenAussen._resetForTest();
      SchattenAussen.init();
      setzeBoard('ext-' + s);
      await ruhig();
      expect(bereich(), s).toBeNull();
      expect(schattenAbrufe(), s).toHaveLength(0);
      expect(es, s).toHaveLength(0);
    }
  });

  it('404 (nicht gekoppelt): kein Band, kein Abonnent', async () => {
    antwort = () => json(404, { error: 'not found' });
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    expect(bereich()).toBeNull();
    expect(es).toHaveLength(0);
  });

  it('wird das Board nachträglich entkoppelt (404 beim Nachladen), verschwindet das Band', async () => {
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    expect(zeilen()).toHaveLength(3);
    antwort = () => json(404, { error: 'not found' });
    es[0].feuere('schatten:aktualisiert');
    await ruhig();
    expect(bereich()).toBeNull();
    expect(zeilen()).toHaveLength(0);
  });

  it('V-1: Nach nachträglicher Entkopplung fragt die Uhr nicht weiter — Kanal zu, Uhr aus, Band weg; höchstens ein weiterer Abruf in 30 Minuten', async () => {
    vi.useFakeTimers();
    try {
      SchattenAussen.init();
      setzeBoard();
      await vi.advanceTimersByTimeAsync(10);
      expect(schattenAbrufe()).toHaveLength(1);
      antwort = () => json(404, { error: 'not found' });
      es[0].feuere('schatten:aktualisiert');
      await vi.advanceTimersByTimeAsync(10);
      expect(schattenAbrufe()).toHaveLength(2);
      expect(bereich()).toBeNull();
      expect(es[0].geschlossen).toBe(true);
      await vi.advanceTimersByTimeAsync(30 * 60_000);
      expect(schattenAbrufe().length).toBeLessThanOrEqual(2);
    } finally { vi.useRealTimers(); }
  });

  it('leere Liste: kein Band', async () => {
    antwort = () => json(200, { letzter_abgleich: vorMin(1), schatten: [] });
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    expect(bereich()?.hidden ?? true).toBe(true);
  });

  it('Klick: Hinweis mit dem Wortlaut der Vorgabe und „Trotzdem öffnen“ (Link nach innen); kein Probe-Abruf', async () => {
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    zeilen()[0].click();
    await ruhig();
    const h = document.querySelector('.sch-hinweis');
    expect(h).not.toBeNull();
    expect(h.getAttribute('role')).toBe('alertdialog');
    expect(h.textContent).toContain('Diese Karte ist intern. Bitte ins Firmennetz einwählen (VPN).');
    const a = h.querySelector('a.sch-trotzdem');
    expect(a.textContent).toBe('Trotzdem öffnen');
    expect(a.href).toBe(BASIS + '&card=42');
    expect(a.rel).toBe('noopener noreferrer');
    expect(a.target).toBe('_blank');
    /* C-3: nichts wird beim Laden oder Klicken ohne Berechtigung geprüft. */
    expect(probeAbrufe()).toHaveLength(0);
    /* Esc schließt, Schließen-Knopf auch. */
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(document.querySelector('.sch-hinweis')).toBeNull();
    zeilen()[1].click();
    await ruhig();
    document.querySelector('.sch-hinweis .sch-zu').click();
    expect(document.querySelector('.sch-hinweis')).toBeNull();
  });

  it('V-2: Auf dem Board ohne Kalender greift der Hinweis nicht ins Dokument ein — Tab im Kopf-Eingabefeld und Esc außerhalb erreichen ihre Handler', async () => {
    document.getElementById('board-header').innerHTML = '<input id="kopf-suche">';
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    zeilen()[0].click();
    await ruhig();
    const h = document.querySelector('.sch-hinweis');
    expect(h.closest('.cal-panel')).toBeNull();
    const eingabe = document.getElementById('kopf-suche');
    eingabe.focus();
    const tab = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    eingabe.dispatchEvent(tab);
    expect(tab.defaultPrevented).toBe(false);
    expect(document.activeElement).toBe(eingabe);
    /* Esc mit Fokus außerhalb: erreicht andere Handler (z. B. ein Karten-Modal), Hinweis bleibt unberührt. */
    const andere = vi.fn();
    document.addEventListener('keydown', andere);
    const esc = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
    eingabe.dispatchEvent(esc);
    document.removeEventListener('keydown', andere);
    expect(andere).toHaveBeenCalledTimes(1);
    expect(document.querySelector('.sch-hinweis')).not.toBeNull();
    /* Mit Fokus im Hinweis schließt Esc ihn wie bisher. */
    h.querySelector('.sch-zu').focus();
    h.querySelector('.sch-zu').dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(document.querySelector('.sch-hinweis')).toBeNull();
  });

  it('Klick ohne Menschen-Link (Basis unbekannt): Hinweis ohne „Trotzdem öffnen“', async () => {
    antwort = () => json(200, { letzter_abgleich: vorMin(1), schatten: [{ ...EINTRAEGE[0], link: undefined }] });
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    zeilen()[0].click();
    await ruhig();
    expect(document.querySelector('.sch-hinweis').textContent).toContain('Bitte ins Firmennetz einwählen');
    expect(document.querySelector('.sch-hinweis a')).toBeNull();
  });

  it('nur http(s)-Links werden ein href (javascript: wird verworfen)', () => {
    expect(sichererLink('javascript:alert(1)')).toBe('');
    expect(sichererLink('data:text/html,x')).toBe('');
    expect(sichererLink('')).toBe('');
    expect(sichererLink(undefined)).toBe('');
    expect(sichererLink(BASIS)).toBe(BASIS);
  });

  describe('automatische Prüfung nur bei erteilter Berechtigung', () => {
    const permissions = (query) => Object.defineProperty(navigator, 'permissions', { value: { query }, configurable: true });
    afterEach(() => { try { delete navigator.permissions; } catch { /* eigenschaft der Prototype */ } });

    it('granted + Server erreichbar: öffnet direkt, kein Hinweis; Probe ohne Anmeldedaten gegen /healthz', async () => {
      const query = vi.fn(async () => ({ state: 'granted' }));
      permissions(query);
      const klick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
      SchattenAussen.init();
      setzeBoard();
      await ruhig();
      zeilen()[0].click();
      await ruhig();
      expect(query).toHaveBeenCalledWith({ name: 'local-network' });
      expect(probeAbrufe()).toHaveLength(1);
      expect(probeAbrufe()[0][0]).toBe('https://innen.example/healthz');
      expect(probeAbrufe()[0][1]).toMatchObject({ mode: 'no-cors', credentials: 'omit' });
      expect(klick).toHaveBeenCalledTimes(1);
      expect(document.querySelector('.sch-hinweis')).toBeNull();
    });

    it('granted, Server nicht erreichbar: Hinweis', async () => {
      permissions(async () => ({ state: 'granted' }));
      vi.mocked(fetch).mockImplementation(async (url, init) => {
        abrufe.push([String(url), init]);
        if (String(url).includes('/schatten')) return antwort(url);
        throw new TypeError('Failed to fetch');
      });
      const klick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
      SchattenAussen.init();
      setzeBoard();
      await ruhig();
      zeilen()[0].click();
      await ruhig();
      expect(probeAbrufe()).toHaveLength(1);
      expect(klick).not.toHaveBeenCalled();
      expect(document.querySelector('.sch-hinweis').textContent).toContain('Bitte ins Firmennetz einwählen (VPN)');
    });

    it('prompt oder denied: Hinweis, KEIN Probe-Abruf (er löste sonst die Abfrage aus)', async () => {
      for (const state of ['prompt', 'denied']) {
        permissions(async () => ({ state }));
        SchattenAussen._resetForTest();
        document.querySelectorAll('.sch-hinweis').forEach(n => n.remove());
        SchattenAussen.init();
        setzeBoard('ext-' + state);
        await ruhig();
        zeilen()[0].click();
        await ruhig();
        expect(document.querySelector('.sch-hinweis'), state).not.toBeNull();
        expect(probeAbrufe(), state).toHaveLength(0);
      }
    });

    it('Name nicht unterstützt (TypeError) oder keine Permissions-API: Hinweis, kein Probe-Abruf', async () => {
      permissions(async () => { throw new TypeError('not a valid value for enumeration PermissionName'); });
      SchattenAussen.init();
      setzeBoard();
      await ruhig();
      zeilen()[0].click();
      await ruhig();
      expect(document.querySelector('.sch-hinweis')).not.toBeNull();
      expect(probeAbrufe()).toHaveLength(0);
    });
  });

  it('Aktualität: „vor N Minuten“; unter 30 Minuten ohne Warnung, ab 30 als Warnung, nie abgeglichen als Warnung', async () => {
    expect(STAND_WARN_MIN).toBe(30);
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    let st = bereich().querySelector('.sch-stand');
    expect(st.textContent).toBe('Interne Karten zuletzt abgeglichen: vor 10 Minuten');
    expect(st.classList.contains('sch-stand--warn')).toBe(false);

    for (const [min, warn] of [[29, false], [30, true], [95, true]]) {
      antwort = () => json(200, { letzter_abgleich: vorMin(min), schatten: EINTRAEGE });
      SchattenAussen._resetForTest();
      SchattenAussen.init();
      setzeBoard('ext-' + min);
      await ruhig();
      st = bereich().querySelector('.sch-stand');
      expect(st.classList.contains('sch-stand--warn'), `${min} min`).toBe(warn);
      expect(st.getAttribute('role')).toBeNull(); /* V-4: die Minutenzeile ist keine Live-Region */
    }
    antwort = () => json(200, { letzter_abgleich: null, schatten: EINTRAEGE });
    SchattenAussen._resetForTest();
    SchattenAussen.init();
    setzeBoard('ext-nie');
    await ruhig();
    st = bereich().querySelector('.sch-stand');
    expect(st.textContent).toContain('noch nie abgeglichen');
    expect(st.classList.contains('sch-stand--warn')).toBe(true);
  });

  it('Aktualität: Formulierung in Minuten, Stunden und Tagen; nie negativ', () => {
    const jetzt = Date.parse('2026-10-10T12:00:00Z');
    const t = (min) => standAnzeige(new Date(jetzt - min * 60_000).toISOString(), jetzt).text;
    expect(t(0)).toBe('Interne Karten zuletzt abgeglichen: vor weniger als einer Minute');
    expect(t(1)).toBe('Interne Karten zuletzt abgeglichen: vor 1 Minute');
    expect(t(59)).toBe('Interne Karten zuletzt abgeglichen: vor 59 Minuten');
    expect(t(61)).toBe('Interne Karten zuletzt abgeglichen: vor 1 Stunde');
    expect(t(180)).toBe('Interne Karten zuletzt abgeglichen: vor 3 Stunden');
    expect(t(3000)).toBe('Interne Karten zuletzt abgeglichen: vor 2 Tagen');
    expect(minutenSeit(new Date(jetzt + 5 * 60_000).toISOString(), jetzt)).toBe(0); /* Uhr geht vor */
    expect(minutenSeit('kein Datum', jetzt)).toBeNull();
    expect(standAnzeige('kein Datum', jetzt).warn).toBe(true);
  });

  it('SSE: schatten:aktualisiert holt neu; Board-Wechsel schließt den Abonnenten und verwirft späte Antworten', async () => {
    SchattenAussen.init();
    setzeBoard('ext1');
    await ruhig();
    expect(es).toHaveLength(1);
    expect(es[0].url).toBe('/boards/ext1/events');
    expect(zeilen()).toHaveLength(3);

    antwort = () => json(200, { letzter_abgleich: vorMin(0), schatten: [EINTRAEGE[0]] });
    es[0].feuere('schatten:aktualisiert');
    await ruhig();
    expect(schattenAbrufe()).toHaveLength(2);
    expect(zeilen()).toHaveLength(1);

    /* Späte Antwort des alten Boards darf das neue nicht überschreiben. */
    let freigabe;
    antwort = (url) => (String(url).includes('/boards/ext1/')
      ? new Promise(r => { freigabe = () => r(json(200, { letzter_abgleich: vorMin(0), schatten: EINTRAEGE })); })
      : json(404, {}));
    es[0].feuere('schatten:aktualisiert');
    setzeBoard('ext2');
    await ruhig();
    expect(es[0].geschlossen).toBe(true);
    expect(bereich()).toBeNull();
    freigabe();
    await ruhig();
    expect(bereich()).toBeNull(); /* ext2 ist nicht gekoppelt (404), die späte ext1-Antwort zählt nicht */
    expect(es).toHaveLength(1);
  });

  it('Abruf scheitert nach Erfolg (503): alter Stand bleibt, Warnung „Aktualisierung gerade nicht möglich“', async () => {
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    antwort = () => json(503, { error: 'x' });
    es[0].feuere('schatten:aktualisiert');
    await ruhig();
    expect(zeilen()).toHaveLength(3);
    expect(bereich().textContent).toContain('Aktualisierung gerade nicht möglich');
    antwort = () => json(200, { letzter_abgleich: vorMin(0), schatten: EINTRAEGE });
    es[0].feuere('schatten:aktualisiert');
    await ruhig();
    expect(bereich().textContent).not.toContain('Aktualisierung gerade nicht möglich');
  });

  it('Minuten-Uhr: ohne Ereigniskanal holt sie nach; stop() beendet Uhr und Abonnent', async () => {
    vi.useFakeTimers();
    try {
      SchattenAussen.init();
      setzeBoard();
      await vi.advanceTimersByTimeAsync(10);
      expect(schattenAbrufe()).toHaveLength(1);
      /* Mit offenem Kanal tickt die Uhr zunächst nur (kein Abruf) … */
      await vi.advanceTimersByTimeAsync(60_000);
      expect(schattenAbrufe()).toHaveLength(1);
      /* … holt aber nach 5 Minuten ohne erfolgreichen Abruf neu (K-1). */
      await vi.advanceTimersByTimeAsync(4 * 60_000);
      expect(schattenAbrufe()).toHaveLength(2);
      /* Fehlerzähler: nach 5 Fehlern in Folge gibt der Kanal auf, die Uhr holt nach. */
      for (let i = 0; i < 5; i++) es[0].feuere('error');
      expect(es[0].geschlossen).toBe(true);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(schattenAbrufe()).toHaveLength(3);
      SchattenAussen.stop();
      await vi.advanceTimersByTimeAsync(300_000);
      expect(schattenAbrufe()).toHaveLength(3);
    } finally { vi.useRealTimers(); }
  });

  it('W-1: Entkoppelt, dann wieder gekoppelt (Kalender öffnet → ladeJetzt): Kanal und Uhr laufen wieder, die 30-min-Warnung kommt; kein Doppelstart', async () => {
    vi.useFakeTimers();
    try {
      SchattenAussen.init();
      setzeBoard();
      await vi.advanceTimersByTimeAsync(10);
      const einTimer = vi.getTimerCount();      /* Zustand im Normalbetrieb: Uhr läuft */
      expect(es).toHaveLength(1);
      antwort = () => json(404, { error: 'not found' });
      es[0].feuere('schatten:aktualisiert');
      await vi.advanceTimersByTimeAsync(10);
      expect(vi.getTimerCount()).toBe(einTimer - 1);
      expect(bereich()).toBeNull();
      /* Wieder gekoppelt; der Server meldet einen alten Abgleich (vor 10 min). */
      const fest = vorMin(10);
      antwort = () => json(200, { letzter_abgleich: fest, schatten: EINTRAEGE });
      for (let i = 0; i < 3; i++) await SchattenAussen.ladeJetzt();   /* Kalender mehrmals geöffnet */
      await vi.advanceTimersByTimeAsync(10);
      expect(bereich()).not.toBeNull();
      expect(vi.getTimerCount()).toBe(einTimer);       /* genau eine Uhr */
      expect(es).toHaveLength(2);                      /* genau ein neuer Kanal */
      expect(es[1].geschlossen).toBe(false);
      for (let i = 0; i < 31; i++) await vi.advanceTimersByTimeAsync(60_000);
      expect(bereich().querySelector('.sch-stand').classList.contains('sch-stand--warn')).toBe(true);
      expect(schattenAbrufe().length).toBeGreaterThan(5);
    } finally { vi.useRealTimers(); }
  });

  it('W-1: War schon der erste Abruf 404 und kommt später ok (ladeJetzt), läuft die Uhr — nur einmal', async () => {
    vi.useFakeTimers();
    try {
      antwort = () => json(404, { error: 'not found' });
      SchattenAussen.init();
      setzeBoard();
      await vi.advanceTimersByTimeAsync(10);
      expect(es).toHaveLength(0);
      const keiner = vi.getTimerCount();
      const fest = vorMin(10);
      antwort = () => json(200, { letzter_abgleich: fest, schatten: EINTRAEGE });
      await SchattenAussen.ladeJetzt();
      await SchattenAussen.ladeJetzt();
      await vi.advanceTimersByTimeAsync(10);
      expect(vi.getTimerCount()).toBe(keiner + 1);
      expect(es).toHaveLength(1);
      for (let i = 0; i < 31; i++) await vi.advanceTimersByTimeAsync(60_000);
      expect(bereich().querySelector('.sch-stand').classList.contains('sch-stand--warn')).toBe(true);
    } finally { vi.useRealTimers(); }
  });

  it('W-2: Ein offener Hinweis verschwindet, wenn das Board entkoppelt wird (404)', async () => {
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    zeilen()[0].click();
    await ruhig();
    expect(document.querySelector('.sch-hinweis')).not.toBeNull();
    antwort = () => json(404, { error: 'not found' });
    es[0].feuere('schatten:aktualisiert');
    await ruhig();
    expect(document.querySelector('.sch-hinweis')).toBeNull();
  });

  it('K-1: Im Normalbetrieb meldet der Server nur Änderungen, der Abgleich läuft weiter — nach 31 Minuten keine Warnung, die Uhr holt mehrfach neu', async () => {
    vi.useFakeTimers();
    try {
      /* Der Server setzt letzter_abgleich bei jedem gültigen Lauf auf „jetzt“, sendet aber
         kein schatten:aktualisiert, solange sich nichts geändert hat. */
      antwort = () => json(200, { letzter_abgleich: new Date().toISOString(), schatten: EINTRAEGE });
      SchattenAussen.init();
      setzeBoard();
      await vi.advanceTimersByTimeAsync(10);
      for (let i = 0; i < 31; i++) {
        await vi.advanceTimersByTimeAsync(60_000);
        expect(bereich().querySelector('.sch-stand').classList.contains('sch-stand--warn'), `Minute ${i + 1}`).toBe(false);
      }
      expect(schattenAbrufe().length).toBeGreaterThanOrEqual(6);
      expect(bereich().querySelector('.sch-stand').textContent).toMatch(/vor (weniger als einer Minute|\d Minuten?)$/);
    } finally { vi.useRealTimers(); }
  });

  it('K-2: Steht die EventSource endgültig auf CLOSED (ein einziger error, z. B. 401 beim Neuverbinden), holt die Uhr weiter nach', async () => {
    vi.useFakeTimers();
    try {
      SchattenAussen.init();
      setzeBoard();
      await vi.advanceTimersByTimeAsync(10);
      expect(schattenAbrufe()).toHaveLength(1);
      es[0].readyState = 2;           /* der Browser gibt auf … */
      es[0].feuere('error');          /* … und meldet das genau einmal */
      expect(es[0].geschlossen).toBe(true);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(schattenAbrufe()).toHaveLength(2);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(schattenAbrufe()).toHaveLength(3);
    } finally { vi.useRealTimers(); }
  });

  it('K-2: Ein Kanal, der ohne error auf CLOSED steht, gilt in der Uhr als „kein Kanal“', async () => {
    vi.useFakeTimers();
    try {
      SchattenAussen.init();
      setzeBoard();
      await vi.advanceTimersByTimeAsync(10);
      es[0].readyState = 2;
      await vi.advanceTimersByTimeAsync(60_000);
      expect(schattenAbrufe()).toHaveLength(2);
    } finally { vi.useRealTimers(); }
  });

  it('K-8: Die Minuten-Uhr und ein unveränderter Abruf bauen das Band nicht neu — Fokus, Zeilen und Hinweis-Anker bleiben', async () => {
    vi.useFakeTimers();
    try {
      SchattenAussen.init();
      setzeBoard();
      await vi.advanceTimersByTimeAsync(10);
      const knopf = zeilen()[1];
      knopf.focus();
      expect(document.activeElement).toBe(knopf);
      const st = bereich().querySelector('.sch-stand');
      expect(st.textContent).toContain('vor 10 Minuten');
      await vi.advanceTimersByTimeAsync(60_000);      /* Tick ohne Abruf */
      expect(schattenAbrufe()).toHaveLength(1);
      expect(zeilen()[1]).toBe(knopf);
      expect(document.activeElement).toBe(knopf);
      expect(bereich().querySelector('.sch-stand')).toBe(st);
      expect(st.textContent).toContain('vor 11 Minuten'); /* nur der Text zieht nach */
      await vi.advanceTimersByTimeAsync(5 * 60_000);  /* Nachholen mit gleichem Inhalt */
      expect(schattenAbrufe().length).toBeGreaterThanOrEqual(2);
      expect(zeilen()[1]).toBe(knopf);
      expect(document.activeElement).toBe(knopf);
      /* Der Hinweis-Anker bleibt im DOM. */
      knopf.click();
      await vi.advanceTimersByTimeAsync(10);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(knopf.isConnected).toBe(true);
      /* Geänderte Daten bauen dagegen neu. */
      antwort = () => json(200, { letzter_abgleich: vorMin(0), schatten: [EINTRAEGE[0]] });
      es[0].feuere('schatten:aktualisiert');
      await vi.advanceTimersByTimeAsync(10);
      expect(zeilen()).toHaveLength(1);
    } finally { vi.useRealTimers(); }
  });

  it('V-4: Die Live-Region (role=status) ändert sich nur beim Wechsel ok ⇄ Warnung / bei Fehler — nicht mit der Minutenzeile', async () => {
    vi.useFakeTimers();
    try {
      const fest = vorMin(10);   /* der Server meldet immer denselben (alten) Abgleich */
      antwort = () => json(200, { letzter_abgleich: fest, schatten: EINTRAEGE });
      SchattenAussen.init();
      setzeBoard();
      await vi.advanceTimersByTimeAsync(10);   /* letzter Abgleich vor 10 min */
      const live = bereich().querySelector('[role="status"]');
      expect(live).not.toBeNull();
      expect(bereich().querySelectorAll('[role="status"]')).toHaveLength(1);
      expect(live.classList.contains('sch-stand')).toBe(false);
      let aenderungen = 0;
      new MutationObserver((l) => { aenderungen += l.length; }).observe(live, { childList: true, characterData: true, subtree: true });
      for (let i = 0; i < 15; i++) await vi.advanceTimersByTimeAsync(60_000);   /* 11 … 25 min: Minutenzeile tickt */
      expect(bereich().querySelector('.sch-stand').textContent).toContain('vor 25 Minuten');
      await Promise.resolve();
      expect(aenderungen).toBe(0);
      expect(live.textContent).toBe('');
      for (let i = 0; i < 6; i++) await vi.advanceTimersByTimeAsync(60_000);    /* über 30 min: Wechsel zur Warnung */
      expect(bereich().querySelector('.sch-stand').classList.contains('sch-stand--warn')).toBe(true);
      expect(live.textContent).toBe('Interne Karten: Der Abgleich ist veraltet.');
      expect(bereich().querySelector('[role="status"]')).toBe(live);           /* dieselbe Region, nicht neu gebaut */
      const nach = live.textContent;
      await vi.advanceTimersByTimeAsync(60_000);                                /* weiter in der Warnung: Text bleibt */
      expect(live.textContent).toBe(nach);
      /* Zurück zu „aktuell“ nach neuem Abgleich (geänderte Daten bauen das Band neu, die Region bleibt). */
      antwort = () => json(200, { letzter_abgleich: new Date().toISOString(), schatten: [EINTRAEGE[0]] });
      es[0].feuere('schatten:aktualisiert');
      await vi.advanceTimersByTimeAsync(10);
      expect(bereich().querySelector('[role="status"]')).toBe(live);
      expect(live.textContent).toBe('Interne Karten sind wieder aktuell.');
    } finally { vi.useRealTimers(); }
  });

  it('Suche außen findet Schatten über Titel und Nummer; der Platzhalter ist kein Titel', async () => {
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    Search.setQuery('müller');
    await new Promise(r => setTimeout(r, 200));
    expect(zeilen().map(z => z.querySelector('.sch-nr').textContent)).toEqual(['i-42']);
    Search.setQuery('i-43');
    await new Promise(r => setTimeout(r, 200));
    expect(zeilen().map(z => z.querySelector('.sch-nr').textContent)).toEqual(['i-43']);
    Search.setQuery('interne');
    await new Promise(r => setTimeout(r, 200));
    expect(bereich().hidden).toBe(true);
    Search.clear();
    expect(zeilen()).toHaveLength(3);
    /* rein */
    expect(schattenTrifft({ nummer: 'i-12', titel: 'Interne Karte' }, '#i-12')).toBe(true);
    expect(schattenTrifft({ nummer: 'i-12', titel: 'Interne Karte' }, '12')).toBe(true);
    expect(schattenTrifft({ nummer: 'i-12', titel: 'Interne Karte', titel_platzhalter: true }, 'interne')).toBe(false);
    /* V-5: eine echte Karte mit dem Titel „Interne Karte“ ist per Titel findbar — entschieden am Feld, nicht am Text. */
    expect(schattenTrifft({ nummer: 'i-13', titel: 'Interne Karte', titel_platzhalter: false }, 'interne')).toBe(true);
    expect(schattenTrifft({ titel: 'Jour fixe' }, 'jour')).toBe(true);
  });

  it('aufklappen/zuklappen per Knopf; Zustand im aria-expanded', async () => {
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    const knopf = () => bereich().querySelector('.sch-umschalter');
    expect(knopf().getAttribute('aria-expanded')).toBe('true');
    knopf().click();
    expect(knopf().getAttribute('aria-expanded')).toBe('false');
    expect(bereich().querySelector('.sch-liste').hidden).toBe(true);
  });

  it('Kopfzeile nennt Anzahl; Meta-Text: Termin ohne Spalte, mehrtägig als Spanne', () => {
    expect(metaText({ art: 'termin', start_datum: '2026-10-24', ende_datum: '2026-10-26' })).toBe('Termin · 24.10.–26.10.');
    expect(metaText({ art: 'karte', spalte: 'Ready' })).toBe('Ready');
  });

  it('mobil: einspaltige Liste und Hinweis ohne feste Breite (390 px), nichts läuft über den Rand', () => {
    SchattenAussen.stile();
    const css = document.getElementById('sch-aussen-styles').textContent;
    const mobil = css.slice(css.indexOf('@media (max-width:640px)'));
    expect(mobil).toContain('.sch-liste{flex-direction:column');
    expect(mobil).toContain('.sch-eintrag{flex:none;width:100%}');
    /* Hinweis: links/rechts gebunden statt fester Breite, umbrechender Text. */
    expect(css).toMatch(/\.sch-hinweis\{[^}]*left:16px;right:16px;[^}]*max-width:460px/);
    expect(css).toContain('overflow-wrap:anywhere');
    /* K-4: das Band verdrängt das Board nicht — die Liste scrollt in sich (mobil höher). */
    expect(css).toMatch(/\.sch-liste\{[^}]*max-height:30vh;overflow-y:auto/);
    expect(mobil).toMatch(/\.sch-liste\{[^}]*max-height:40vh/);
    expect(css).not.toMatch(/\.sch-(zeile|eintrag|hinweis)\{[^}]*[^-]width:\d+px/);
  });
});

describeOhneServer('#781 ohne Server-Build', () => {
  it('init macht nichts: kein Abruf, kein Band, kein Abonnent', async () => {
    SchattenAussen.init();
    setzeBoard();
    await ruhig();
    expect(schattenAbrufe()).toHaveLength(0);
    expect(bereich()).toBeNull();
    expect(es).toHaveLength(0);
  });
});

describe('#781 Kalender-Einträge (rein)', () => {
  it('Schatten mit Datum werden Einträge; ohne Datum keiner; Uhrzeit gehört zum jeweiligen Tag', () => {
    const e = schattenAlsKalenderEintraege([
      ...EINTRAEGE,
      { schatten_id: 'S4', art: 'karte', nummer: 'i-44', titel: 'Ohne Datum', bearbeiter: [] },
      { schatten_id: 'S5', art: 'karte', nummer: 'i-45', titel: 'Rückwärts', start_datum: '2026-10-30', ende_datum: '2026-10-28', bearbeiter: [] },
    ]);
    expect(e.map(x => x.card.id)).toEqual(['schatten:S1', 'schatten:S3', 'schatten:S5']);
    expect(e[0].card).toMatchObject({ title: 'i-42 Angebot Müller', startDate: '2026-10-22', dueDate: '2026-10-22', startTime: null, dueTime: null });
    expect(e[1].card).toMatchObject({ title: 'Jour fixe', startDate: '2026-10-24', dueDate: '2026-10-24', startTime: '10:00', dueTime: '11:00' });
    expect(e[2].card).toMatchObject({ startDate: '2026-10-30', dueDate: '2026-10-30' }); /* Ende vor Start → Eintag */
    expect(e[0].card._schatten).toBe(EINTRAEGE[0]);
    expect(schattenAlsKalenderEintraege(null)).toEqual([]);
  });

  it('K-6: Die Nummer steht nur vor einem echten Titel (wie der Feed) — entschieden am Feld titel_platzhalter, nicht am Text', () => {
    const e = schattenAlsKalenderEintraege([
      { ...EINTRAEGE[1], ende_datum: '2026-10-23' },
      /* Ein echter Titel, der zufällig „Interne Karte“ lautet: bekommt die Nummer. */
      { schatten_id: 'S6', art: 'karte', nummer: 'i-46', titel: 'Interne Karte', titel_platzhalter: false, ende_datum: '2026-10-25', bearbeiter: [] },
    ]);
    expect(e.map(x => x.card.title)).toEqual(['Interne Karte', 'i-46 Interne Karte']);
  });
});
