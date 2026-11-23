/* @vitest-environment happy-dom */
/**
 * Karten #1059 (F-2830, F-2858) und #1061 (F-2851), ADR-0148 §5.
 *
 * F-2830: „Sitzung abgelaufen“ greift auch auf den Nebenpfaden (Admin-Client,
 *   Clients/Projekte, LLM-Stream, Anhang-Upload) und auch vor dem ersten /me.
 * F-2858: das Band sagt, dass der neue Anmelde-Tab danach zu schließen ist.
 * F-2851: Verhaltenstest für die Wechsel-Sperre (`Sitzung.sperrtWechsel`, von
 *   main.js `sitzungSperrtWechsel` aufgerufen) statt Quelltext-Wächter.
 *
 * Nur Server-Build (Sitzung/Clients sind sonst null).
 */
import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { Sitzung } from '../src/sitzung-abgelaufen.js';
import { AdminClient } from '../src/admin-client.js';
import { ClientsProjectsClient } from '../src/clients-projects-client.js';
import { LLMStream } from '../src/llm-stream.js';
import { __testing__ } from '../src/storage-plugin.js';
import { State } from '../src/state.js';
import { EventBus } from '../src/eventbus.js';
import { I18n } from '../src/i18n.js';
import { describeServer } from './helpers/build-flags.js';

const antwort = (status, body = {}) => ({
  ok: status >= 200 && status < 300, status,
  headers: { get: () => null },
  json: () => Promise.resolve(body),
  text: () => Promise.resolve(JSON.stringify(body)),
});
const band = () => document.querySelector('.sitzung-abgelaufen-banner');
const brett = () => ({ version: '2', board: { id: 'b1', title: 'B', nextDisplayId: 1, columnDefs: [{ id: 'col-backlog' }], sprints: [] } });
const SITZUNG401 = { error: 'unauthorized: session required' };
const FORWARD401 = { error: 'unauthorized: forward-auth identity required' };
const tick = () => new Promise(r => setTimeout(r, 0));

describeServer('Sitzung abgelaufen auf den Nebenpfaden (#1059)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    Sitzung.__zuruecksetzen();
  });
  afterEach(() => {
    Sitzung.__zuruecksetzen();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  describe('Betriebsart bekannt (OIDC)', () => {
    beforeEach(() => { Sitzung.setzeOIDC(true); });

    it('Admin-Client: 401 zeigt das Band (GET und PUT)', async () => {
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(401, SITZUNG401))));
      const res = await AdminClient.listConfig();
      expect(res.status).toBe(401);
      expect(Sitzung.abgelaufen()).toBe(true);
      expect(band()).toBeTruthy();
      Sitzung.__zuruecksetzen(); Sitzung.setzeOIDC(true);
      await AdminClient.setConfig('k', 'v', false);
      expect(band()).toBeTruthy();
    });

    it('Clients/Projekte: 401 zeigt das Band (GET, POST, DELETE-Weg)', async () => {
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(401, SITZUNG401))));
      await ClientsProjectsClient.listClients();
      expect(band()).toBeTruthy();
      expect(Sitzung.abgelaufen()).toBe(true);
    });

    it('LLM-Stream: 401 vor dem Stream zeigt das Band und wirft weiter StreamPreError', async () => {
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(401, SITZUNG401))));
      await expect(LLMStream.streamLLM({ url: '/llm/x', body: {} })).rejects.toBeInstanceOf(LLMStream.StreamPreError);
      expect(band()).toBeTruthy();
    });

    it('Anhang-Upload (Karte und Board): 401 zeigt das Band, Upload wirft weiter', async () => {
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(401, SITZUNG401))));
      const plugin = __testing__.createRESTPlugin();
      await plugin.switchBoard('b1').catch(() => {});
      const be = plugin.attachments();
      const datei = new File(['x'], 'a.txt', { type: 'text/plain' });
      Sitzung.__zuruecksetzen(); Sitzung.setzeOIDC(true);
      /* ohne Board-ID wirft der Upload vor dem Fetch — Board-ID über switchBoard-Antwort */
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(200, brett()))));
      await plugin.switchBoard('b1');
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(401, SITZUNG401))));
      await expect(be.uploadFile(datei, { id: 'c1' })).rejects.toThrow(/401/);
      expect(band()).toBeTruthy();
      Sitzung.__zuruecksetzen(); Sitzung.setzeOIDC(true);
      await expect(be.uploadBoardFile(datei)).rejects.toThrow(/401/);
      expect(band()).toBeTruthy();
    });

    it('Erfolg und andere Fehler lösen das Band nicht aus', async () => {
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(500))));
      await AdminClient.listConfig();
      expect(band()).toBeNull();
      expect(Sitzung.abgelaufen()).toBe(false);
    });
  });

  describe('Forward-Auth/Standalone-Betrieb: kein Band', () => {
    it('Betriebsart bekannt und nicht OIDC: 401 im Admin-Client bleibt Einzelfehler', async () => {
      Sitzung.setzeOIDC(false);
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(401, FORWARD401))));
      const res = await AdminClient.listConfig();
      expect(res.status).toBe(401);
      expect(band()).toBeNull();
    });
  });

  describe('vor dem ersten /me (Betriebsart unbekannt)', () => {
    it('401 zuerst: /me klärt OIDC, das Band kommt nachträglich', async () => {
      const fetchMock = vi.fn(url => Promise.resolve(
        String(url).endsWith('/me') ? antwort(200, { anmeldung: 'oidc' }) : antwort(401, SITZUNG401)));
      vi.stubGlobal('fetch', fetchMock);
      Sitzung.setzeBetriebsartAbruf(async () => {
        const r = await fetch('/me'); return { status: r.status, body: await r.json() };
      });
      expect(Sitzung.betriebsartBekannt()).toBe(false);
      await AdminClient.listConfig();
      await tick(); await tick();
      expect(Sitzung.istOIDC()).toBe(true);
      expect(Sitzung.abgelaufen()).toBe(true);
      expect(band()).toBeTruthy();
    });

    it('401 zuerst, /me selbst 401 mit „session required“: Band', async () => {
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(401, SITZUNG401))));
      Sitzung.setzeBetriebsartAbruf(async () => {
        const r = await fetch('/me'); return { status: r.status, body: await r.json() };
      });
      await AdminClient.listConfig();
      await tick(); await tick();
      expect(band()).toBeTruthy();
    });

    it('401 zuerst, Forward-Auth (/me ohne anmeldung): kein Band', async () => {
      vi.stubGlobal('fetch', vi.fn(url => Promise.resolve(
        String(url).endsWith('/me') ? antwort(200, { name: 'A' }) : antwort(401, FORWARD401))));
      Sitzung.setzeBetriebsartAbruf(async () => {
        const r = await fetch('/me'); return { status: r.status, body: await r.json() };
      });
      await AdminClient.listConfig();
      await tick(); await tick();
      expect(Sitzung.betriebsartBekannt()).toBe(true);
      expect(band()).toBeNull();
    });

    it('401 zuerst, /me 401 „forward-auth identity required“: kein Band', async () => {
      vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(401, FORWARD401))));
      Sitzung.setzeBetriebsartAbruf(async () => {
        const r = await fetch('/me'); return { status: r.status, body: await r.json() };
      });
      await AdminClient.listConfig();
      await tick(); await tick();
      expect(band()).toBeNull();
    });

    it('der Abruf läuft nur einmal, auch bei mehreren 401', async () => {
      const abruf = vi.fn(async () => ({ status: 200, body: {} }));
      Sitzung.setzeBetriebsartAbruf(abruf);
      Sitzung.meldeAntwort(401); Sitzung.meldeAntwort(401); Sitzung.meldeAntwort(401);
      await tick(); await tick();
      expect(abruf).toHaveBeenCalledTimes(1);
    });

    it('adoptOrCreate des REST-Plugins stößt die Klärung beim Boot an', async () => {
      const fetchMock = vi.fn(url => Promise.resolve(
        String(url).endsWith('/me') ? antwort(200, { anmeldung: 'oidc' }) : antwort(200, [])));
      vi.stubGlobal('fetch', fetchMock);
      const plugin = __testing__.createRESTPlugin();
      plugin.adoptOrCreate(brett()).catch(() => {});
      await tick(); await tick();
      expect(fetchMock.mock.calls.some(c => String(c[0]).endsWith('/me'))).toBe(true);
      expect(Sitzung.istOIDC()).toBe(true);
    });
  });

  describe('Band: Anmelde-Tab (F-2858)', () => {
    it('nennt, dass der neue Tab nach der Anmeldung zu schließen ist (de und en), Ziel /anmeldung', () => {
      Sitzung.setzeOIDC(true);
      Sitzung.meldeAntwort(401);
      expect(band().textContent).toContain('Tab schließen');
      const open = vi.fn();
      vi.stubGlobal('open', open);
      window.open = open;
      band().querySelector('button.btn-primary').click();
      expect(open).toHaveBeenCalledWith('/anmeldung', '_blank');
      Sitzung.__zuruecksetzen(); Sitzung.setzeOIDC(true);
      I18n.setLang('en');
      Sitzung.meldeAntwort(401);
      expect(band().textContent).toMatch(/close the new tab/);
      I18n.setLang('de');
    });
  });
});

/* #1061 / F-2851: Verhalten der Wechsel-Sperre, nicht der Quelltext. */
describeServer('Wechsel-Sperre bei abgelaufener Sitzung (sitzungSperrtWechsel, F-2851)', () => {
  let hinweise;
  const hinweisHandler = t => hinweise.push(t);
  function ablaufen(probeStatus) {
    Sitzung.setzeOIDC(true);
    Sitzung.setzeProbe(async () => probeStatus);
    Sitzung.meldeAntwort(401);
    expect(Sitzung.abgelaufen()).toBe(true);
  }
  const dirty = () => { State.set(brett()); expect(State.isDirty()).toBe(true); };

  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    Sitzung.__zuruecksetzen();
    hinweise = [];
    EventBus.on('hinweis', hinweisHandler);
  });
  afterEach(() => {
    EventBus.off?.('hinweis', hinweisHandler);
    Sitzung.__zuruecksetzen();
    State.markClean();
  });

  it('abgelaufen + ungesendet + weiter 401: Wechsel gesperrt, nichts gesendet, Hinweis', async () => {
    dirty(); ablaufen(401);
    const flush = vi.fn();
    expect(await Sitzung.sperrtWechsel(flush)).toBe(true);
    expect(flush).not.toHaveBeenCalled();
    expect(State.isDirty()).toBe(true);
    expect(hinweise).toContain(Sitzung.text('wechselGesperrt'));
  });

  it('abgelaufen, Anmeldung wieder gültig, Senden gelingt: Wechsel läuft', async () => {
    dirty(); ablaufen(200);
    const flush = vi.fn(async () => { State.markClean(); });
    expect(await Sitzung.sperrtWechsel(flush)).toBe(false);
    expect(flush).toHaveBeenCalledTimes(1);
    expect(Sitzung.abgelaufen()).toBe(false);
  });

  it('Anmeldung wieder gültig, Senden scheitert aus anderem Grund: gesperrt mit eigenem Hinweis', async () => {
    dirty(); ablaufen(200);
    const flush = vi.fn(async () => { /* bleibt dirty */ });
    expect(await Sitzung.sperrtWechsel(flush)).toBe(true);
    expect(hinweise).toContain(Sitzung.text('nochUngesendet'));
  });

  it('gültige Sitzung: Wechsel läuft ohne Flush', async () => {
    Sitzung.setzeOIDC(true); dirty();
    const flush = vi.fn();
    expect(await Sitzung.sperrtWechsel(flush)).toBe(false);
    expect(flush).not.toHaveBeenCalled();
  });

  it('abgelaufen, aber nichts ungesendet: Wechsel läuft', async () => {
    State.markClean(); ablaufen(401);
    expect(await Sitzung.sperrtWechsel(vi.fn())).toBe(false);
  });
});
