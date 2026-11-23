/* @vitest-environment happy-dom */
/**
 * Karte #1136 / F-2990: Anhang-Löschhinweise unterscheiden den Status.
 * 403 = keine Berechtigung (Wiederholen hilft nicht), 401/5xx/Netz = wird erneut versucht;
 * beim Mehrfach-Löschen höchstens EINE Sammelmeldung mit Anzahl. Nur Server-Build.
 */
import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { Sitzung } from '../src/sitzung-abgelaufen.js';
import { EventBus } from '../src/eventbus.js';
import { I18n } from '../src/i18n.js';
import { __testing__ } from '../src/storage-plugin.js';
import { describeServer } from './helpers/build-flags.js';

const antwort = (status, body = {}) => ({
  ok: status >= 200 && status < 300, status,
  headers: { get: () => null },
  json: () => Promise.resolve(body), text: () => Promise.resolve(''),
});
const brett = () => ({ version: '2', board: { id: 'b1', title: 'B', nextDisplayId: 1, columnDefs: [{ id: 'col-backlog' }], sprints: [] } });

describeServer('Anhang-Löschhinweise (F-2990, #1136)', () => {
  let hinweise;
  const auf = t => hinweise.push(t);
  beforeEach(() => {
    I18n.setLang('de'); hinweise = []; EventBus.on('hinweis', auf);
    localStorage.clear(); Sitzung.__zuruecksetzen(); Sitzung.setzeOIDC(true);
  });
  afterEach(() => {
    EventBus.off('hinweis', auf); localStorage.clear(); Sitzung.__zuruecksetzen();
    vi.unstubAllGlobals(); vi.restoreAllMocks();
  });

  async function backend(status) {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(200, brett()))));
    const p = __testing__.createRESTPlugin();
    await p.switchBoard('b1');
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(status))));
    return p.attachments();
  }

  it('Backend: 403 liefert verboten, 409/500 liefern false', async () => {
    const be = await backend(403);
    expect(await be.deleteAttachment({ id: 'c1' }, { id: 'a1' })).toBe('verboten');
    expect(await be.purgeCardAttachments('c1')).toBe('verboten');
    const be2 = await backend(409);
    expect(await be2.deleteAttachment({ id: 'c1' }, { id: 'a1' })).toBe(false);
  });

  it('Texte: 403 sagt nicht „später erneut versuchen“, de und en', () => {
    for (const lang of ['de', 'en']) {
      I18n.setLang(lang);
      expect(Sitzung.text('anhangLoeschen403')).not.toMatch(/später|later/i);
      expect(Sitzung.text('anhangPurge403')).not.toMatch(/erneut|retried/i);
      expect(Sitzung.text('anhangPurgeFehler')).toMatch(/automatisch|automatically/);
    }
  });

  it('Karte löschen: 403 → Berechtigungstext, 500 → Wiederholungstext, ok → still', () => {
    Sitzung.anhangPurgeMelden(['verboten']);
    expect(hinweise).toEqual([Sitzung.text('anhangPurge403')]);
    hinweise.length = 0;
    Sitzung.anhangPurgeMelden([false]);
    expect(hinweise).toEqual([Sitzung.text('anhangPurgeFehler')]);
    expect(hinweise[0]).toMatch(/automatisch erneut/);
    hinweise.length = 0;
    Sitzung.anhangPurgeMelden([true, true]);
    expect(hinweise).toEqual([]);
  });

  it('Mehrfach-Löschen: genau eine Sammelmeldung mit Anzahl (Plural), de und en', () => {
    Sitzung.anhangPurgeMelden([false, true, false, false]);
    expect(hinweise).toHaveLength(1);
    expect(hinweise[0]).toContain('3 Karten');
    hinweise.length = 0;
    I18n.setLang('en');
    Sitzung.anhangPurgeMelden(['verboten', 'verboten', true]);
    expect(hinweise).toHaveLength(1);
    expect(hinweise[0]).toContain('2 cards');
  });

  it('F-3014: Textwahl — nur wiederholbar → Wiederholtext, nur 403 → Berechtigung, gemischt/verworfen → neutral', () => {
    Sitzung.anhangPurgeMelden(['verboten', 'verboten']);
    expect(hinweise[0]).toContain('Berechtigung');
    expect(hinweise[0]).not.toContain('teils');
    Sitzung.anhangPurgeMelden([false, 'verboten']);
    expect(hinweise[1]).not.toContain('automatisch');
    expect(hinweise[1]).not.toContain('Berechtigung');
    expect(hinweise[1]).toContain('2 Karten');
    Sitzung.anhangPurgeMelden(['verworfen']);
    expect(hinweise[2]).not.toContain('automatisch');
    Sitzung.anhangPurgeMelden([false]);
    expect(hinweise[3]).toContain('automatisch');
    expect(hinweise).toHaveLength(4);
  });

  it('main.js: Purges werden nur gegen einen Server-Stand nachgeholt, wenn lokal nichts ungesichert ist (F-3009/F-3015)', () => {
    const q = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8');
    const start = q.indexOf('function restEingang(');
    expect(start).toBeGreaterThan(-1);
    const block = q.slice(start, q.indexOf('\n}\n', start));
    const schutz = block.indexOf('if (_aenderungsGen !== _genBezug) {');
    const aufruf = block.indexOf('anhangPurgeNachholen?.(validated)');
    expect(schutz).toBeGreaterThan(-1);
    /* erst NACH dem Rücksprung bei ungesichertem lokalem Stand */
    expect(aufruf).toBeGreaterThan(schutz);
    expect(block.slice(schutz, aufruf)).toMatch(/return;\s*\}/);
    /* kein weiterer Auslöser irgendwo in main.js */
    expect(q.match(/anhangPurgeNachholen\?\.\(/g)).toHaveLength(1);
  });

  it('main.js: bulk:delete sammelt die Purges und meldet einmal (kein .then je Karte)', () => {
    const q = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8');
    const block = q.slice(q.indexOf("EventBus.on('bulk:delete'"));
    const kopf = block.slice(0, block.indexOf('mutateBoard'));
    expect(kopf).toContain('Promise.all(purges)');
    expect(kopf).not.toMatch(/purgeCardAttachments\(cardId\)\.then/);
  });

  it('main.js: Hinweis-Toast zeigt lange Sätze länger als 2,5 s', () => {
    const q = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8');
    expect(q).toMatch(/msg\.length \* 60/);
  });
});
