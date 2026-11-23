/* @vitest-environment happy-dom */
/**
 * Karte #1142 / F-3017: Gemerkte Karten-Purges.
 * (1) Scheitert der Sofort-Purge erst, nachdem der Server-Stand schon angekommen ist,
 *     wird das Nachholen EINMAL angestoßen (Vollabruf ohne If-None-Match, sonst 304 und
 *     `restEingang` liefe nicht) — kein Polling, keine Schleife.
 * (2) Wird ein gemerkter Eintrag beim Nachholen verworfen (Karte steht im Server-Stand,
 *     409, 403, übrige 4xx), gibt es einen Hinweis; Erfolg und „nochmal“ bleiben still.
 * Nur Server-Build.
 */
import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { Sitzung } from '../src/sitzung-abgelaufen.js';
import { EventBus } from '../src/eventbus.js';
import { I18n } from '../src/i18n.js';
import { __testing__, anhangPurgeNachholen } from '../src/storage-plugin.js';
import { describeServer } from './helpers/build-flags.js';

const antwort = (status, body = {}, etag = null) => ({
  ok: status >= 200 && status < 300, status,
  headers: { get: h => (h === 'ETag' ? etag : null) },
  json: () => Promise.resolve(body), text: () => Promise.resolve(''),
});
const brett = () => ({ version: '2', board: { id: 'b1', title: 'B', nextDisplayId: 1, columnDefs: [{ id: 'col-backlog' }], sprints: [] } });
const stand = (karten = []) => ({ version: '2', board: { id: 'b1', sprints: [{ id: 's', cards: karten.map(k => ({ id: k })) }] } });

describeServer('Gemerkte Purges: Anstoß und Verwerf-Hinweis (F-3017, #1142)', () => {
  let hinweise, plugin;
  const auf = t => hinweise.push(t);
  beforeEach(async () => {
    I18n.setLang('de'); hinweise = []; EventBus.on('hinweis', auf);
    Sitzung.__zuruecksetzen(); Sitzung.setzeOIDC(true);
    await anhangPurgeNachholen(stand(['c1', 'c2', 'c3'])); /* Liste früherer Tests leeren */
    hinweise.length = 0;
  });
  afterEach(() => {
    plugin?.stopPolling(); plugin = null;
    EventBus.off('hinweis', auf); Sitzung.__zuruecksetzen();
    vi.unstubAllGlobals(); vi.restoreAllMocks();
  });

  /* Plugin mit gesetzter ETag und aktivem Callback; `tabelle`: Status je URL-Teil für DELETE. */
  let haengendePosts = null;
  async function aufbau(tabelle = {}) {
    const calls = [];
    vi.stubGlobal('fetch', vi.fn((url, opt = {}) => {
      calls.push({ url: String(url), methode: opt.method || 'GET', headers: opt.headers || {} });
      if (opt.method === 'DELETE') {
        const st = Object.entries(tabelle).find(([t]) => String(url).includes(t))?.[1] ?? 200;
        /* [status, body] für Antworten mit Inhalt (F-3029: 200 geschuetztGeblieben) */
        return Promise.resolve(Array.isArray(st) ? antwort(st[0], st[1]) : antwort(st));
      }
      if (opt.method === 'POST' && haengendePosts) {
        return new Promise(r => haengendePosts.push(() => r(antwort(500))));
      }
      /* GET mit If-None-Match → 304 wie der Server bei unveränderter ETag */
      if (opt.headers?.['If-None-Match']) return Promise.resolve(antwort(304));
      return Promise.resolve(antwort(200, brett(), '"e1"'));
    }));
    plugin = __testing__.createRESTPlugin();
    await plugin.switchBoard('b1');
    const cb = vi.fn(st => plugin.bezugUebernehmen(st));
    plugin.startPolling(cb);
    calls.length = 0;
    return { calls, cb, be: plugin.attachments() };
  }
  const gets = calls => calls.filter(c => c.methode === 'GET');

  /* Kritiker S116 F-3029: auch ein später Rest (200 geschuetztGeblieben) stößt einmal an. */
  it('F-3029: später Rest stößt einen Vollabruf ohne If-None-Match an', async () => {
    const { calls, cb, be } = await aufbau({ '/c1': [200, { geschuetztGeblieben: 1 }] });
    expect(await be.purgeCardAttachments('c1')).toBe(true);
    await vi.waitFor(() => expect(cb).toHaveBeenCalledTimes(1));
    expect(gets(calls)).toHaveLength(1);
    expect(gets(calls)[0].headers['If-None-Match']).toBeUndefined();
  });

  /* Kritiker S116 F-3031: der Merker wird nach dem Abruf zurückgesetzt. */
  it('F-3031: zwei späte Fehlschläge nacheinander → je ein eigener Vollabruf', async () => {
    const { calls, cb, be } = await aufbau({ '/c1': 500, '/c2': 500 });
    await be.purgeCardAttachments('c1');
    await vi.waitFor(() => expect(cb).toHaveBeenCalledTimes(1));
    await be.purgeCardAttachments('c2');
    await vi.waitFor(() => expect(cb).toHaveBeenCalledTimes(2));
    expect(gets(calls)).toHaveLength(2);
  });

  /* Kritiker S116 F-3031: läuft gerade ein Speichern, wartet der Anstoß und holt danach. */
  it('F-3031: Anstoß während eines laufenden Speicherns holt nach dessen Ende', async () => {
    haengendePosts = [];
    try {
      const { calls, cb, be } = await aufbau({ '/c1': 500 });
      const st = brett(); st.board.sprints = [{ id: 's', title: 'S', cards: [{ id: 'neu', title: 'x', columnId: 'col-backlog' }] }];
      const speichern = plugin.save(st);
      await vi.waitFor(() => expect(haengendePosts.length).toBeGreaterThan(0));
      await be.purgeCardAttachments('c1');
      await new Promise(r => setTimeout(r, 20));
      expect(gets(calls).filter(g => !g.headers['If-None-Match'])).toHaveLength(0);
      haengendePosts.splice(0).forEach(f => f());
      await speichern;
      await vi.waitFor(() => expect(cb).toHaveBeenCalled());
      expect(gets(calls).filter(g => !g.headers['If-None-Match'])).toHaveLength(1);
    } finally { haengendePosts = null; }
  });

  it('später Fehlschlag: genau ein Vollabruf ohne If-None-Match, Callback läuft', async () => {
    const { calls, cb, be } = await aufbau({ '/c1': 500 });
    expect(await be.purgeCardAttachments('c1')).toBe(false);
    await vi.waitFor(() => expect(cb).toHaveBeenCalledTimes(1));
    expect(gets(calls)).toHaveLength(1);
    expect(gets(calls)[0].headers['If-None-Match']).toBeUndefined();
  });

  it('drei gleichzeitige späte Fehlschläge: genau ein Vollabruf ohne If-None-Match', async () => {
    const { calls, cb, be } = await aufbau({ '/c1': 500, '/c2': 500, '/c3': 500 });
    const e = await Promise.all(['c1', 'c2', 'c3'].map(c => be.purgeCardAttachments(c)));
    expect(e).toEqual([false, false, false]);
    await vi.waitFor(() => expect(cb).toHaveBeenCalledTimes(1));
    await new Promise(r => setTimeout(r, 10));
    expect(gets(calls)).toHaveLength(1);
    expect(gets(calls)[0].headers['If-None-Match']).toBeUndefined();
  });

  it('Vergleich: ein normaler Abruf mit gesetzter ETag liefe in 304 (Anstoß ohne ETag nötig)', async () => {
    const { calls, cb } = await aufbau();
    await plugin.holeJetzt();
    expect(gets(calls)[0].headers['If-None-Match']).toBe('"e1"');
    expect(cb).not.toHaveBeenCalled();
  });

  it('kein Anstoß bei Erfolg, 403 und 409', async () => {
    const { calls, be } = await aufbau({ '/c2': 403, '/c3': 409 });
    expect(await be.purgeCardAttachments('c1')).toBe(true);
    expect(await be.purgeCardAttachments('c2')).toBe('verboten');
    expect(await be.purgeCardAttachments('c3')).toBe('verworfen');
    await new Promise(r => setTimeout(r, 10));
    expect(gets(calls)).toHaveLength(0);
  });

  it('ohne aktiven Callback (Polling aus) passiert beim Anstoß nichts', async () => {
    const { calls, be } = await aufbau({ '/c1': 500 });
    plugin.stopPolling();
    await be.purgeCardAttachments('c1');
    await new Promise(r => setTimeout(r, 10));
    expect(gets(calls)).toHaveLength(0);
  });

  it('Nachholen verwirft: Karte steht im Server-Stand → ein Hinweis', async () => {
    const { be } = await aufbau({ '/c1': 500 });
    await be.purgeCardAttachments('c1'); hinweise.length = 0;
    await anhangPurgeNachholen(stand(['c1']));
    expect(hinweise).toHaveLength(1);
    expect(hinweise[0]).toMatch(/nicht weiter versucht/);
  });

  it('Nachholen verwirft: 409, 403 und 400 je ein Hinweis; mehrere → eine Sammelmeldung mit Anzahl', async () => {
    for (const st of [409, 403, 400]) {
      const { be } = await aufbau({ '/c1': 500 });
      await be.purgeCardAttachments('c1'); hinweise.length = 0;
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(st))));
      await anhangPurgeNachholen(stand());
      expect(hinweise, String(st)).toHaveLength(1);
      plugin.stopPolling();
    }
    hinweise.length = 0;
    const { be } = await aufbau({ '/c1': 500, '/c2': 500 });
    await be.purgeCardAttachments('c1'); await be.purgeCardAttachments('c2'); hinweise.length = 0;
    await anhangPurgeNachholen(stand(['c1', 'c2']));
    expect(hinweise).toHaveLength(1);
    expect(hinweise[0]).toContain('2 gelöschten Karten');
  });

  it('Nachholen gelingt oder scheitert vorübergehend: kein Hinweis', async () => {
    const { be } = await aufbau({ '/c1': 500, '/c2': 500 });
    await be.purgeCardAttachments('c1'); hinweise.length = 0;
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(503))));
    await anhangPurgeNachholen(stand());
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(200))));
    await anhangPurgeNachholen(stand());
    expect(hinweise).toEqual([]);
  });

  it('Texte de und en vorhanden, Einzahl und Mehrzahl', () => {
    for (const lang of ['de', 'en']) {
      I18n.setLang(lang);
      expect(Sitzung.text('anhangPurgeVerworfen')).toBeTruthy();
      expect(Sitzung.text('anhangPurgeVerworfenN')).toContain('{n}');
    }
  });

  it('Wächter: restEingang in main.js stößt das Nachholen weiter mit dem Server-Stand an', () => {
    const q = readFileSync(join(__dirname, '../src/main.js'), 'utf8');
    expect(q).toMatch(/anhangPurgeNachholen\?\.\(validated\)/);
  });
});
