/* @vitest-environment happy-dom */
/**
 * Karte #1135 / F-2988, Nacharbeit Kritiker Sprint 115 (F-3009..F-3011):
 * Scheitert das Aufräumen der Anhänge einer gelöschten Karte vorübergehend
 * (401, 408, 429, 5xx, Netzfehler), merkt sich der Client Board und Karte — nur im
 * Speicher des Tabs — und sendet das DELETE erneut, wenn ein übergebener
 * SERVER-Stand die Karte nicht mehr enthält (F-3015). Enthält er sie noch, wird
 * der Eintrag verworfen; ein Stand eines anderen Boards (oder keiner) lässt ihn
 * liegen. 403 und übrige 4xx werden nicht gemerkt. Nur Server-Build.
 */
import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { Sitzung } from '../src/sitzung-abgelaufen.js';
import { __testing__, anhangPurgeNachholen } from '../src/storage-plugin.js';
import { describeServer } from './helpers/build-flags.js';

const antwort = (status, body = {}) => ({
  ok: status >= 200 && status < 300, status,
  headers: { get: () => null },
  json: () => Promise.resolve(body), text: () => Promise.resolve(''),
});
const brett = () => ({ version: '2', board: { id: 'b1', title: 'B', nextDisplayId: 1, columnDefs: [{ id: 'col-backlog' }], sprints: [] } });
/* Server-Stand von Board `id` mit den Karten `karten` */
const stand = (karten = [], id = 'b1') => ({ version: '2', board: { id, sprints: [{ id: 's', cards: karten.map(k => ({ id: k })) }] } });
const alleWeg = stand();

describeServer('Gescheiterte Karten-Purges werden nachgeholt (F-2988, #1135; F-3009..F-3011)', () => {
  beforeEach(async () => {
    Sitzung.__zuruecksetzen(); Sitzung.setzeOIDC(true);
    /* Liste aus früheren Tests leeren: alle gemerkten Karten als vorhanden melden → verworfen */
    await anhangPurgeNachholen(stand(['c1', 'c2', 'c3', 'c4']));
  });
  afterEach(() => { Sitzung.__zuruecksetzen(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

  async function backend() {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(200, brett()))));
    const p = __testing__.createRESTPlugin();
    await p.switchBoard('b1');
    return p.attachments();
  }
  /* Antwortet je URL mit dem Status aus `tabelle`, sonst 200; zeichnet DELETEs auf. */
  function server(tabelle = {}) {
    const deletes = [];
    vi.stubGlobal('fetch', vi.fn((url, opt) => {
      if (opt?.method === 'DELETE') deletes.push(String(url));
      const st = Object.entries(tabelle).find(([teil]) => String(url).includes(teil))?.[1] ?? 200;
      return st === 'netz' ? Promise.reject(new Error('netz')) : Promise.resolve(antwort(st));
    }));
    return deletes;
  }
  async function gemerkt() {
    const deletes = server();
    await anhangPurgeNachholen(alleWeg);
    return deletes.map(u => u.split('/').pop().split('?')[0]).sort();
  }

  it('401, 500, 429 und Netzfehler werden gemerkt, je Adresse nur einmal', async () => {
    const be = await backend();
    server({ '/c1': 401 }); expect(await be.purgeCardAttachments('c1')).toBe(false);
    Sitzung.__zuruecksetzen(); Sitzung.setzeOIDC(true);
    server({ '/c2': 500 }); await be.purgeCardAttachments('c2');
    server({ '/c3': 429 }); await be.purgeCardAttachments('c3');
    server({ '/c4': 'netz' }); await be.purgeCardAttachments('c4');
    server({ '/c4': 'netz' }); await be.purgeCardAttachments('c4');
    expect(await gemerkt()).toEqual(['c1', 'c2', 'c3', 'c4']);
  });

  it('403 → \'verboten\', 400/409 → \'verworfen\'; beides wird nicht gemerkt (F-3011)', async () => {
    const be = await backend();
    server({ '/c1': 403 }); expect(await be.purgeCardAttachments('c1')).toBe('verboten');
    server({ '/c2': 400 }); expect(await be.purgeCardAttachments('c2')).toBe('verworfen');
    server({ '/c3': 409 }); expect(await be.purgeCardAttachments('c3')).toBe('verworfen');
    expect(await gemerkt()).toEqual([]);
  });

  it('F-3009: steht die Karte im Server-Stand noch, wird nichts gesendet und der Eintrag verworfen', async () => {
    const be = await backend();
    server({ '/c1': 500 }); await be.purgeCardAttachments('c1');
    const deletes = server();
    await anhangPurgeNachholen(stand(['c1']));
    expect(deletes).toEqual([]);
    expect(await gemerkt()).toEqual([]);
  });

  it('Stand eines anderen Boards oder kein Stand: nichts senden, Eintrag behalten', async () => {
    const be = await backend();
    server({ '/c1': 500 }); await be.purgeCardAttachments('c1');
    const deletes = server();
    await anhangPurgeNachholen(stand([], 'b2'));
    await anhangPurgeNachholen();
    expect(deletes).toEqual([]);
    expect(await gemerkt()).toEqual(['c1']);
  });

  it('nach dem Nachholen ist die Liste leer, ein 404 zählt als erledigt', async () => {
    const be = await backend();
    server({ '/c1': 500 }); await be.purgeCardAttachments('c1');
    server({ '/c2': 500 }); await be.purgeCardAttachments('c2');
    const deletes = server({ '/c2': 404 });
    await anhangPurgeNachholen(alleWeg);
    expect(deletes.map(u => u.split('/').pop().split('?')[0]).sort()).toEqual(['c1', 'c2']);
    expect(await gemerkt()).toEqual([]);
  });

  it('beim ersten erneuten vorübergehenden Fehlschlag hört der Lauf auf, die Liste bleibt', async () => {
    const be = await backend();
    server({ '/c1': 500 }); await be.purgeCardAttachments('c1');
    server({ '/c2': 500 }); await be.purgeCardAttachments('c2');
    const deletes = server({ '/c1': 503, '/c2': 503 });
    await anhangPurgeNachholen(alleWeg);
    expect(deletes).toHaveLength(1);
    expect(await gemerkt()).toEqual(['c1', 'c2']);
  });

  it('ein gelungener Purge holt NICHT von selbst nach (nur nach bestätigtem Speichern)', async () => {
    const be = await backend();
    server({ '/c1': 500 }); await be.purgeCardAttachments('c1');
    const deletes = server();
    expect(await be.purgeCardAttachments('c9')).toBe(true);
    await new Promise(r => setTimeout(r, 0));
    expect(deletes.some(u => u.endsWith('/c1'))).toBe(false);
  });

  it('bei abgelaufener Sitzung sendet das Nachholen nichts', async () => {
    const be = await backend();
    server({ '/c1': 401 }); await be.purgeCardAttachments('c1');
    expect(Sitzung.abgelaufen()).toBe(true);
    const deletes = server();
    await anhangPurgeNachholen(alleWeg);
    expect(deletes).toEqual([]);
  });

  it('F-3009/F-3010: nichts landet in localStorage', async () => {
    const vorher = Object.keys(localStorage);
    const be = await backend();
    server({ '/c1': 500 }); await be.purgeCardAttachments('c1');
    expect(Object.keys(localStorage)).toEqual(vorher);
  });

  /* F-3019/F-3020 (#1143): Den Beweis „Karte fehlt am Server“ führt der Server. */
  it('F-3019: das Nachholen sendet nurVerwaist=1, der Sofort-Purge nicht', async () => {
    const be = await backend();
    const sofort = server({ '/c1': 500 }); await be.purgeCardAttachments('c1');
    expect(sofort).toHaveLength(1);
    expect(sofort[0]).not.toContain('nurVerwaist');
    const nach = server();
    await anhangPurgeNachholen(alleWeg);
    expect(nach).toHaveLength(1);
    expect(nach[0].endsWith('/c1?nurVerwaist=1')).toBe(true);
  });

  it('F-3020: 409 vom Server (Karte steht dort noch) verwirft den Eintrag, der Lauf geht weiter', async () => {
    const be = await backend();
    server({ '/c1': 500 }); await be.purgeCardAttachments('c1');
    server({ '/c2': 500 }); await be.purgeCardAttachments('c2');
    /* Client-Stand ohne c1 (veraltet), Server kennt c1 noch → 409 */
    const deletes = server({ '/c1?': 409 });
    await anhangPurgeNachholen(alleWeg);
    expect(deletes.map(u => u.split('/').pop())).toEqual(['c1?nurVerwaist=1', 'c2?nurVerwaist=1']);
    expect(await gemerkt()).toEqual([]);
  });

  /* F-3013 (#1141, ADR-0136 Nachtrag 2026-11-11): Blieben Dateien fremder Kommentare an
     der noch lebenden Karte, wird der Rest gemerkt und nach dem Speichern nachgeholt. */
  function restServer() {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(200, { geschuetztGeblieben: 1 }))));
  }
  it('F-3013: 200 mit geschuetztGeblieben → true, Rest gemerkt, nachgeholt mit nurVerwaist=1', async () => {
    const be = await backend();
    restServer();
    expect(await be.purgeCardAttachments('c1')).toBe(true);
    const deletes = server();
    await anhangPurgeNachholen(alleWeg);
    expect(deletes.map(u => u.split('/').pop())).toEqual(['c1?nurVerwaist=1']);
  });

  it('F-3013: Rest-Eintrag einer lebenden Karte (Stand oder 409) wird still verworfen', async () => {
    const be = await backend();
    const hinweis = vi.spyOn(Sitzung, 'anhangPurgeVerworfen');
    restServer(); await be.purgeCardAttachments('c1');
    restServer(); await be.purgeCardAttachments('c2');
    server({ '/c2?': 409 });
    await anhangPurgeNachholen(stand(['c1']));
    expect(await gemerkt()).toEqual([]);
    expect(hinweis).not.toHaveBeenCalled();
  });
});
