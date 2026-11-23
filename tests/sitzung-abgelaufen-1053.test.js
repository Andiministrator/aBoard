/* @vitest-environment happy-dom */
/**
 * Karte #1053 (ADR-0148 §5, Teil von #816): abgelaufene Sitzung im OIDC-Betrieb.
 *
 * Abnahme: simuliertes 401 während ausstehender Ops — die Ops bleiben, der
 * Hinweis erscheint, das Senden pausiert, nach einer nicht-401-Antwort wird
 * weiter gesendet, nie ein Neuladen. Im Forward-Auth-Betrieb (/me ohne
 * `anmeldung`) bleibt alles wie zuvor.
 *
 * Läuft nur in den Kontexten mit REST-Plugin (server, server-frontend).
 */
import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { __testing__ } from '../src/storage-plugin.js';
import { Sitzung } from '../src/sitzung-abgelaufen.js';
import { State } from '../src/state.js';
import { EventBus } from '../src/eventbus.js';
import { I18n } from '../src/i18n.js';
import { describeServer } from './helpers/build-flags.js';
import { readFileSync } from 'node:fs';

const brett = () => ({
  version: '2',
  board: {
    id: 'b1', title: 'B', nextDisplayId: 3,
    columnDefs: [{ id: 'col-backlog' }, { id: 'col-ready' }],
    sprints: [{ id: 's1', title: 'S', kind: 'sprint', cards: [
      { id: 'c1', displayId: 1, title: 'eins', description: '', columnId: 'col-backlog', comments: [] },
    ] }],
  },
});
const klon = x => JSON.parse(JSON.stringify(x));

function antwort(status, { etag = null, body = {} } = {}) {
  return {
    ok: status >= 200 && status < 300, status,
    headers: { get: h => (h === 'ETag' ? etag : null) },
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  };
}

/* Router nach Methode + Pfad. `me`, `ops`, `board` sind veränderbare Antworten. */
function router() {
  const r = { aufrufe: [], me: antwort(200, { body: { name: 'A', email: 'a@x', anmeldung: 'oidc' } }),
    ops: antwort(200), board: antwort(304), ende: antwort(204) };
  r.fetchMock = vi.fn((url, init = {}) => {
    const methode = init.method || 'GET';
    r.aufrufe.push({ methode, url, body: init.body ? JSON.parse(init.body) : undefined });
    if (url.endsWith('/me')) return Promise.resolve(r.me);
    if (url.endsWith('/anmeldung/ende')) return Promise.resolve(r.ende);
    if (url.endsWith('/ops')) return Promise.resolve(r.ops);
    return Promise.resolve(r.board);
  });
  r.opsPosts = () => r.aufrufe.filter(a => a.methode === 'POST' && a.url.endsWith('/ops'));
  return r;
}

describeServer('Sitzung abgelaufen im OIDC-Betrieb (Karte #1053)', () => {
  let plugin, r, reload;

  async function geladen() {
    r.board = antwort(200, { etag: '"e0"', body: brett() });
    const sw = await plugin.switchBoard('b1');
    expect(sw.ok).toBe(true);
    State.set(klon(sw.state));
    r.board = antwort(304);
    plugin.startPolling(vi.fn());   /* holeJetzt() braucht den Poll-Rückruf */
    r.aufrufe.length = 0;
  }
  function aendere(titel) {
    const neu = klon(State.get());
    neu.board.sprints[0].cards[0].title = titel;
    State.set(neu);
    return State.get();
  }

  beforeEach(async () => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    Sitzung.__zuruecksetzen();
    plugin = __testing__.createRESTPlugin();
    r = router();
    vi.stubGlobal('fetch', r.fetchMock);
    reload = vi.fn();
    try { vi.spyOn(window.location, 'reload').mockImplementation(reload); } catch { /* happy-dom: sonst Reload-Prüfung über Bannertext */ }
  });

  afterEach(() => {
    plugin.stopPolling();
    Sitzung.__zuruecksetzen();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  async function oidcBekannt() {
    const me = await plugin.getCurrentUser();
    expect(me.anmeldung).toBe('oidc');
    expect(Sitzung.istOIDC()).toBe(true);
  }

  it('401 beim Senden: Ops bleiben, Hinweis erscheint, kein Neuladen', async () => {
    await oidcBekannt();
    await geladen();
    r.ops = antwort(401, { body: { error: 'unauthorized: session required' } });
    const res = await plugin.save(aendere('neu'));
    expect(res.ok).toBe(false);
    expect(res.sitzungAbgelaufen).toBe(true);
    expect(res.erneutNachMs).toBeUndefined();            /* kein Wiederholungs-Takt */
    expect(State.isDirty()).toBe(true);                  /* nichts als gesichert markiert */
    const banner = document.querySelector('.sitzung-abgelaufen-banner');
    expect(banner).toBeTruthy();
    expect(banner.textContent).toContain('Sitzung abgelaufen — neu anmelden');
    expect(reload).not.toHaveBeenCalled();
    /* kein PUT-Rückfall, der die Ops ersetzen würde */
    expect(r.aufrufe.some(a => a.methode === 'PUT')).toBe(false);
  });

  it('pausiert: weitere Speicherversuche senden nichts (kein 401-Sturm)', async () => {
    await oidcBekannt();
    await geladen();
    r.ops = antwort(401);
    await plugin.save(aendere('neu'));
    expect(r.opsPosts()).toHaveLength(1);
    for (let i = 0; i < 5; i++) {
      const res = await plugin.autoPersist(aendere('neu' + i));
      expect(res.sitzungAbgelaufen).toBe(true);
    }
    expect(r.opsPosts()).toHaveLength(1);
    expect(document.querySelectorAll('.sitzung-abgelaufen-banner')).toHaveLength(1);
  });

  it('nach erfolgreicher Anmeldung (Poll nicht mehr 401): Hinweis weg, Ereignis, dieselben Ops gehen hinaus', async () => {
    await oidcBekannt();
    await geladen();
    r.ops = antwort(401);
    await plugin.save(aendere('neu'));
    const erster = r.opsPosts()[0].body.ops;
    const wiederDa = vi.fn();
    EventBus.on('sitzung:wieder-da', wiederDa);
    /* Anmeldung im anderen Tab ist durch: der nächste Poll bekommt 304 statt 401 */
    r.ops = antwort(200);
    await plugin.holeJetzt();
    expect(Sitzung.abgelaufen()).toBe(false);
    expect(document.querySelector('.sitzung-abgelaufen-banner')).toBeFalsy();
    expect(wiederDa).toHaveBeenCalledTimes(1);
    const res = await plugin.save(State.get());
    expect(res.ok).toBe(true);
    expect(r.opsPosts()).toHaveLength(2);
    const inhalt = ops => ops.map(o => ({ type: o.type, payload: o.payload }));
    expect(inhalt(r.opsPosts()[1].body.ops)).toEqual(inhalt(erster));    /* nichts verloren, nichts doppelt (Op-ids werden neu vergeben) */
    expect(reload).not.toHaveBeenCalled();
  });

  it('hole() mit 401 pausiert ebenfalls, auch ohne vorheriges Speichern', async () => {
    await oidcBekannt();
    await geladen();
    r.board = antwort(401);
    await plugin.holeJetzt();
    expect(Sitzung.abgelaufen()).toBe(true);
    expect(document.querySelector('.sitzung-abgelaufen-banner')).toBeTruthy();
  });

  it('„Neu anmelden“ öffnet /anmeldung im neuen Tab, aber erst auf Klick', async () => {
    await oidcBekannt();
    await geladen();
    const offen = vi.spyOn(window, 'open').mockImplementation(() => null);
    r.ops = antwort(401);
    await plugin.save(aendere('neu'));
    expect(offen).not.toHaveBeenCalled();
    [...document.querySelectorAll('.sitzung-abgelaufen-banner button')]
      .find(b => b.textContent === 'Neu anmelden').click();
    expect(offen).toHaveBeenCalledWith('/anmeldung', '_blank');
  });

  it('„Weiter senden“: bei 401 bleibt der Hinweis, bei 200 verschwindet er', async () => {
    await oidcBekannt();
    await geladen();
    r.ops = antwort(401);
    await plugin.save(aendere('neu'));
    const knopf = () => [...document.querySelectorAll('.sitzung-abgelaufen-banner button')]
      .find(b => b.textContent === 'Weiter senden');
    r.me = antwort(401);
    knopf().click();
    await vi.waitFor(() => expect(r.aufrufe.filter(a => a.url.endsWith('/me')).length).toBeGreaterThan(0));
    await Promise.resolve();
    expect(Sitzung.abgelaufen()).toBe(true);
    r.me = antwort(200, { body: { anmeldung: 'oidc' } });
    knopf().click();
    await vi.waitFor(() => expect(Sitzung.abgelaufen()).toBe(false));
    expect(document.querySelector('.sitzung-abgelaufen-banner')).toBeFalsy();
  });

  it('Forward-Auth-Betrieb (/me ohne anmeldung): 401 bleibt wie zuvor, kein Hinweis, keine Pause', async () => {
    r.me = antwort(200, { body: { name: 'A', email: 'a@x' } });
    const me = await plugin.getCurrentUser();
    expect(me.anmeldung).toBeUndefined();
    expect(Sitzung.istOIDC()).toBe(false);
    await geladen();
    r.ops = antwort(401);
    const res = await plugin.save(aendere('neu'));
    expect(res.sitzungAbgelaufen).toBeUndefined();
    expect(Sitzung.abgelaufen()).toBe(false);
    expect(document.querySelector('.sitzung-abgelaufen-banner')).toBeFalsy();
    /* der Rückfall auf den PUT des bisherigen Ablaufs findet weiter statt */
    expect(r.aufrufe.some(a => a.methode === 'PUT')).toBe(true);
  });

  it('Abmelden mit ungesendeten Änderungen: Rückfrage; bei „Nein“ keine Anfrage', async () => {
    await oidcBekannt();
    await geladen();
    aendere('ungesendet');
    expect(State.isDirty()).toBe(true);
    const frage = (window.confirm = vi.fn(() => false));
    expect(await Sitzung.abmelden()).toBe(false);
    expect(frage).toHaveBeenCalledTimes(1);
    expect(r.aufrufe.some(a => a.url.endsWith('/anmeldung/ende'))).toBe(false);
  });

  it('Abmelden ohne ungesendete Änderungen: POST /anmeldung/ende, keine Rückfrage', async () => {
    await oidcBekannt();
    await geladen();
    State.markClean();
    const frage = (window.confirm = vi.fn(() => false));
    const weg = vi.fn();
    /* Sitzungs-Kritik B (F-2868): Der Spy muss greifen — sonst prüft die
       Zielzeile unten nichts. Kein try/catch mehr. */
    const spion = vi.spyOn(window.location, 'assign').mockImplementation(weg);
    await Sitzung.abmelden();
    expect(frage).not.toHaveBeenCalled();
    const ende = r.aufrufe.find(a => a.url.endsWith('/anmeldung/ende'));
    expect(ende?.methode).toBe('POST');
    /* K2 F-B: Ziel ist die Seite „Abgemeldet“, nicht '/' (dort meldete der
       Anbieter still wieder an). */
    expect(weg).toHaveBeenCalledTimes(1);
    expect(weg).toHaveBeenCalledWith('/anmeldung/abgemeldet');
    spion.mockRestore();
  });

  it('Abmelden führt auf /anmeldung/abgemeldet, nie auf / (Quelltext-Wächter, K2 F-B)', async () => {
    const fs = await import('node:fs');
    const roh = fs.readFileSync('src/sitzung-abgelaufen.js', 'utf8');
    expect(roh).toContain("ABMELDUNG_ZIEL = '/anmeldung/abgemeldet'");
    expect(roh).toContain('location.assign(ABMELDUNG_ZIEL)');
    expect(roh).not.toMatch(/location\.assign\('\/'\)/);
  });

  it('Sidebar: „Abmelden“ nur, wenn /me den OIDC-Betrieb meldet', async () => {
    const { Sidebar } = await import('../src/sidebar.js');
    const { Profile } = await import('../src/profile.js');
    const { StoragePlugin } = await import('../src/storage-plugin.js');
    const spy = vi.spyOn(Profile, 'has').mockImplementation(cap => cap === 'feature.restStorage');
    const sp = vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue({
      getCurrentUser: () => plugin.getCurrentUser(),
      listKnownBoards: async () => [], aclList: async () => [],
    });
    const knoepfe = () => [...document.querySelectorAll('.app-sidebar-row')].map(b => b.textContent);
    try {
      await Sidebar.open();
      expect(knoepfe().some(t => t.includes('Abmelden'))).toBe(true);
      Sidebar.close();
      r.me = antwort(200, { body: { name: 'A', email: 'a@x' } });
      await Sidebar.open();
      expect(knoepfe().some(t => t.includes('Abmelden'))).toBe(false);
    } finally { Sidebar.close(); spy.mockRestore(); sp.mockRestore(); }
  });

  /* Kritik B-3: Nur eine Erfolgsantwort beendet die Pause; 502/503 eines
     Proxys nicht (sonst flackert das Band). */
  it('Pause endet nur mit 2xx/304, nicht mit 502/503', () => {
    Sitzung.setzeOIDC(true);
    expect(Sitzung.meldeAntwort(401)).toBe(true);
    expect(Sitzung.abgelaufen()).toBe(true);
    for (const st of [502, 503, 500, 404]) {
      Sitzung.meldeAntwort(st);
      expect(Sitzung.abgelaufen()).toBe(true);
    }
    const wieder = vi.fn();
    EventBus.on('sitzung:wieder-da', wieder);
    try {
      Sitzung.meldeAntwort(304);
      expect(Sitzung.abgelaufen()).toBe(false);
      expect(wieder).toHaveBeenCalledTimes(1);
      expect(document.querySelector('.sitzung-abgelaufen-banner')).toBeNull();
    } finally { EventBus.off?.('sitzung:wieder-da', wieder); }
  });
});

/* Kritik B-4: Wächter am Quelltext für Verdrahtungen, die ein Unit-Test des
   Plugins nicht sieht (main.js wird hier nicht geladen). */
describeServer('Verdrahtung #1053 im Quelltext (Kritik B-1/B-2/B-4)', () => {
  const quelle = f => readFileSync(new URL('../src/' + f, import.meta.url), 'utf8');
  it('main.js nimmt nach neuer Anmeldung das Senden wieder auf', () => {
    /* #1135: Handler seit F-2988 mehrzeilig (holt auch gescheiterte Purges nach) — kurzer Block statt einer Zeile. */
    expect(quelle('main.js')).toMatch(/EventBus\.on\('sitzung:wieder-da',[\s\S]{0,160}?debouncedPersist\(State\.get\(\)\)/);
  });
  it('Board-Wechsel und neues Board prüfen die abgelaufene Sitzung (B-1)', () => {
    const m = quelle('main.js');
    const n = (m.match(/if \(await sitzungSperrtWechsel\(\)\) return;/g) || []).length;
    expect(n).toBe(2);
  });
  it('postBoard meldet 401 an die Sitzung (B-2)', () => {
    const sp = quelle('storage-plugin.js');
    const rumpf = sp.slice(sp.indexOf('async function postBoard('), sp.indexOf('async function putBoard('));
    expect(rumpf).toContain('Sitzung?.meldeAntwort(res.status)');
  });
  it('kein Neuladen der Seite im Sitzungs-Modul', () => {
    const ohneKommentare = quelle('sitzung-abgelaufen.js').replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/.*$/gm, '');
    expect(ohneKommentare).not.toMatch(/location\.reload\s*\(/);
  });
});
