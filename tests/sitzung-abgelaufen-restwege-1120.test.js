/* @vitest-environment happy-dom */
/**
 * Karte #1120 / F-2953, ADR-0148 §5.
 *
 * Die restlichen fetch-Wege in storage-plugin.js melden ihre Antwort an
 * `Sitzung.meldeAntwort` (wie die Wege aus #1059): Anhang lesen (Karte und Board),
 * Anhang löschen (Karte und Board), Anhänge purgen, ACL- und Mitglieder-Abrufe,
 * Board löschen. Jeder Weg bekommt einen 401 und muss `meldeAntwort(401)` rufen.
 *
 * Vorbild: tests/sitzung-abgelaufen-nebenpfade-1059.test.js.
 * Nur Server-Build (Sitzung und REST-Plugin sind sonst null).
 */
import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { Sitzung } from '../src/sitzung-abgelaufen.js';
import { __testing__ } from '../src/storage-plugin.js';
import { I18n } from '../src/i18n.js';
import { describeServer } from './helpers/build-flags.js';

const antwort = (status, body = {}) => ({
  ok: status >= 200 && status < 300, status,
  headers: { get: () => null },
  json: () => Promise.resolve(body),
  text: () => Promise.resolve(JSON.stringify(body)),
  blob: () => Promise.resolve(new Blob(['x'])),
});
const SITZUNG401 = { error: 'unauthorized: session required' };
const brett = () => ({ version: '2', board: { id: 'b1', title: 'B', nextDisplayId: 1, columnDefs: [{ id: 'col-backlog' }], sprints: [] } });

describeServer('Sitzung abgelaufen auf den restlichen fetch-Wegen (F-2953, #1120)', () => {
  let meldeSpy;

  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    Sitzung.__zuruecksetzen();
    Sitzung.setzeOIDC(true);
    meldeSpy = vi.spyOn(Sitzung, 'meldeAntwort');
  });
  afterEach(() => {
    Sitzung.__zuruecksetzen();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  /* Plugin mit gesetzter Board-ID (switchBoard braucht eine 200-Antwort). */
  async function plugin() {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(200, brett()))));
    const p = __testing__.createRESTPlugin();
    await p.switchBoard('b1');
    return p;
  }
  const liefert401 = () => vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(401, SITZUNG401))));
  const meldet401 = () => expect(meldeSpy).toHaveBeenCalledWith(401);

  it('fetchBoardBlob (Board-Anhang lesen): 401 wird gemeldet', async () => {
    const be = (await plugin()).attachments();
    liefert401();
    expect(await be.boardAttachmentUrl({ id: 'a1' })).toBeNull();
    meldet401();
  });

  it('fetchBlob (Karten-Anhang lesen): 401 wird gemeldet', async () => {
    const be = (await plugin()).attachments();
    liefert401();
    expect(await be.readAttachment({ id: 'c1' }, { id: 'a1' })).toBeNull();
    meldet401();
  });

  it('deleteAttachment (Karten-Anhang löschen): 401 wird gemeldet', async () => {
    const be = (await plugin()).attachments();
    liefert401();
    await be.deleteAttachment({ id: 'c1' }, { id: 'a1' });
    meldet401();
  });

  it('purgeCardAttachments (alle Anhänge einer Karte): 401 wird gemeldet', async () => {
    const be = (await plugin()).attachments();
    liefert401();
    await be.purgeCardAttachments('c1');
    meldet401();
  });

  it('deleteBoardAttachment (Board-Anhang löschen): 401 wird gemeldet, Ergebnis false', async () => {
    const be = (await plugin()).attachments();
    liefert401();
    expect(await be.deleteBoardAttachment({ id: 'a1' })).toBe(false);
    meldet401();
  });

  it('aclList: 401 wird gemeldet, Ergebnis leer', async () => {
    const p = await plugin();
    liefert401();
    expect(await p.aclList('b1')).toEqual([]);
    meldet401();
  });

  it('mitgliederNamen: 401 wird gemeldet, Ergebnis null', async () => {
    const p = await plugin();
    liefert401();
    expect(await p.mitgliederNamen('b1')).toBeNull();
    meldet401();
  });

  it('aclSet: 401 wird gemeldet', async () => {
    const p = await plugin();
    liefert401();
    expect(await p.aclSet('b1', 'nutzer@example.org', 'editor')).toEqual({ ok: false, status: 401 });
    meldet401();
  });

  it('deleteBoard: 401 wird gemeldet', async () => {
    const p = await plugin();
    liefert401();
    expect((await p.deleteBoard('b1')).status).toBe(401);
    meldet401();
  });

  it('aclDelete: 401 wird gemeldet', async () => {
    const p = await plugin();
    liefert401();
    expect((await p.aclDelete('b1', 'nutzer@example.org')).status).toBe(401);
    meldet401();
  });

  it('Erfolg meldet 200 und zeigt kein Band', async () => {
    const p = await plugin();
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(antwort(200, []))));
    await p.aclList('b1');
    expect(meldeSpy).toHaveBeenCalledWith(200);
    expect(meldeSpy).not.toHaveBeenCalledWith(401);
    expect(Sitzung.abgelaufen()).toBe(false);
  });
});
