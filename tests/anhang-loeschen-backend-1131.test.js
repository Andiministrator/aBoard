/* @vitest-environment happy-dom */
/**
 * Karte #1131 / F-2978: `deleteAttachment` und `purgeCardAttachments` des REST-Plugins
 * liefern ihr Ergebnis: true = Datei am Server weg (200/204, 404 = war schon weg),
 * false = Server hat nicht gelöscht (401, 5xx, Netzfehler). Vorher: kein Rückgabewert.
 * Nur Server-Build.
 */
import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { Sitzung } from '../src/sitzung-abgelaufen.js';
import { __testing__ } from '../src/storage-plugin.js';
import { describeServer } from './helpers/build-flags.js';

const antwort = (status, body = {}) => ({
  ok: status >= 200 && status < 300, status,
  headers: { get: () => null },
  json: () => Promise.resolve(body), text: () => Promise.resolve(''),
});
const brett = () => ({ version: '2', board: { id: 'b1', title: 'B', nextDisplayId: 1, columnDefs: [{ id: 'col-backlog' }], sprints: [] } });

describeServer('Anhang löschen liefert das Ergebnis (F-2978, #1131)', () => {
  beforeEach(() => { Sitzung.__zuruecksetzen(); Sitzung.setzeOIDC(true); });
  afterEach(() => { Sitzung.__zuruecksetzen(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

  async function backend() {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(200, brett()))));
    const p = __testing__.createRESTPlugin();
    await p.switchBoard('b1');
    return p.attachments();
  }
  const liefert = st => vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(st))));
  const netzfehler = () => vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('netz'))));

  for (const [name, aufruf] of [
    ['deleteAttachment', be => be.deleteAttachment({ id: 'c1' }, { id: 'a1' })],
    ['purgeCardAttachments', be => be.purgeCardAttachments('c1')],
  ]) {
    it(`${name}: 200 und 404 → true`, async () => {
      const be = await backend();
      liefert(200); expect(await aufruf(be)).toBe(true);
      liefert(404); expect(await aufruf(be)).toBe(true);
    });
    it(`${name}: 401 → false (und das Band erscheint)`, async () => {
      const be = await backend();
      liefert(401);
      expect(await aufruf(be)).toBe(false);
      expect(Sitzung.abgelaufen()).toBe(true);
    });
    it(`${name}: 500 und Netzfehler → false`, async () => {
      const be = await backend();
      liefert(500); expect(await aufruf(be)).toBe(false);
      netzfehler(); expect(await aufruf(be)).toBe(false);
    });
  }
});
