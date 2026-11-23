/* @vitest-environment happy-dom */

/**
 * Karte #443 — der Transportweg von BOARD-Anhängen im Server-Modus (F-739).
 *
 * Karten-Anhänge nehmen seit Karte #450 den REST-Weg. Board-Anhänge —
 * Hintergrundbild und Avatare — konnten das nicht: `/boards/{id}/attachments/{cardId}`
 * verlangt eine Karten-id, und ein Hintergrundbild hat keine. Sie reisten
 * deshalb weiter als base64 IM Board-State und liefen gegen `MaxBodyBytes`.
 * Diese Karte hat den fehlenden Endpunkt nachgeliefert.
 *
 * Geprüft werden die drei Dinge, an denen der Umbau scheitern konnte:
 *   1. Der Transport geht wirklich über den neuen Endpunkt (kein `data` mehr).
 *   2. ALLE Richtungen wandern mit — Lesen, Objekt-URL und Löschen. Der halbe
 *      Umbau war F-844: neue Anhänge unsichtbar, weil nur die Schreibrichtung
 *      umgestellt war.
 *   3. Bestandsdaten (`att.data`) bleiben lesbar. Sie liegen auf dem Server
 *      unter keiner id — wer diesen Zweig entfernt, verliert sie.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { __testing__ } from '../src/storage-plugin.js';

const hatRESTPlugin = typeof __testing__.createRESTPlugin === 'function';

describe.skipIf(!hatRESTPlugin)('Karte #443 — Board-Anhänge nehmen den REST-Weg', () => {
  let plugin, backend, fetchSpy;

  beforeEach(async () => {
    plugin = __testing__.createRESTPlugin();
    await plugin.preload('board-1').catch(() => {});
    backend = plugin.attachments();
  });
  afterEach(() => { fetchSpy?.mockRestore(); });

  it('uploadBoardFile schickt Multipart an /board-attachments und liefert KEIN data', async () => {
    const antwort = { id: 'att-bg', filename: 'bg.jpg', mime: 'image/jpeg', size: 3_000_000, created: '2026-08-29' };
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true, status: 201, json: async () => antwort,
    });

    const file = new File(['x'.repeat(64)], 'bg.jpg', { type: 'image/jpeg' });
    const att = await backend.uploadBoardFile(file);

    expect(att.data, 'der Board-Anhang trägt base64-Daten — dann liegt er im Board-State '
      + 'und reist bei jedem PUT mit, genau der Defekt aus F-739').toBeUndefined();
    expect(att.id).toBe('att-bg');

    const [url, opts] = fetchSpy.mock.calls.at(-1);
    expect(String(url)).toContain('/boards/board-1/board-attachments');
    expect(String(url), 'die Karten-Route wurde benutzt — sie verlangt eine Karten-id')
      .not.toContain('/attachments/');
    expect(opts.method).toBe('POST');
    expect(opts.body).toBeInstanceOf(FormData);
  });

  it('das Backend sagt selbst, dass Board-Anhänge nicht mehr im State liegen', () => {
    expect(backend.boardAnhangImBoardState()).toBe(false);
  });

  /* Die Grenze wandert mit dem Transportweg (F-845). Stünde hier weiter die
     Board-Grössenprüfung, bekäme der Nutzer die 2-MB-Absage für eine Datei,
     die der Server mit 50 MB annimmt — der Fehler, den Karte #450 an der
     Nachbarstelle schon einmal hatte. */
  it('checkBoardUpload prüft gegen das Endpunkt-Limit, nicht mehr gegen den State', () => {
    const board = { attachments: [] };
    const gross = new File([new Uint8Array(5 * 1024 * 1024)], 'foto.jpg', { type: 'image/jpeg' });
    expect(backend.checkBoardUpload(gross, board).ok,
      '5 MB abgelehnt — dann gilt noch die alte State-Grenze').toBe(true);
  });

  /* Eigene Frage, eigener Schalter: Ein Avatar wird mit 40 px Kante gezeigt.
     Über REST lädt jeder Betrachter das Original — die Verkleinerung darf
     also NICHT mit dem Transportweg abgeschaltet werden. */
  it('Anzeigebilder werden weiterhin verkleinert, obwohl sie nicht im State liegen', () => {
    expect(backend.boardBildVerkleinern()).toBe(true);
    expect(backend.boardAnhangImBoardState()).toBe(false);
  });

  it('boardAttachmentUrl holt den Anhang vom Endpunkt, wenn er kein data trägt', async () => {
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true, status: 200, blob: async () => new Blob(['bild']),
    });
    const url = await backend.boardAttachmentUrl({ id: 'att-bg', mime: 'image/jpeg' });
    expect(url).toMatch(/^blob:/);
    expect(String(fetchSpy.mock.calls.at(-1)[0])).toContain('/boards/board-1/board-attachments/att-bg');
  });

  it('boardAttachmentObjectUrl holt ebenfalls vom Endpunkt — sonst bleibt der Hintergrund leer', async () => {
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true, status: 200, blob: async () => new Blob(['bild']),
    });
    const url = await backend.boardAttachmentObjectUrl({ id: 'att-bg', mime: 'image/jpeg' });
    expect(url).toMatch(/^blob:/);
    expect(String(fetchSpy.mock.calls.at(-1)[0])).toContain('/board-attachments/att-bg');
  });

  it('deleteBoardAttachment löscht am Endpunkt', async () => {
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: true, status: 204 });
    await backend.deleteBoardAttachment({ id: 'att-bg' });
    const [url, opts] = fetchSpy.mock.calls.at(-1);
    expect(String(url)).toContain('/boards/board-1/board-attachments/att-bg');
    expect(opts.method).toBe('DELETE');
  });
});

describe.skipIf(!hatRESTPlugin)('Karte #443 — Bestandsdaten bleiben lesbar', () => {
  /* Alles, was vor diesem Umbau als Hintergrund oder Avatar gesetzt wurde,
     liegt als base64 im State und auf dem Server unter KEINER id. Jeder
     Leseweg muss `att.data` zuerst prüfen; ein Endpunkt-Aufruf dafür liefert
     404 und das Bild verschwindet stumm. */
  const bestand = {
    id: 'att-alt',
    mime: 'image/png',
    data: 'iVBORw0KGgo=',
  };
  let plugin, backend, fetchSpy;

  beforeEach(async () => {
    plugin = __testing__.createRESTPlugin();
    await plugin.preload('board-1').catch(() => {});
    backend = plugin.attachments();
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 404 });
  });
  afterEach(() => { fetchSpy?.mockRestore(); });

  it('boardAttachmentUrl liefert die data:-URL, ohne den Server zu fragen', async () => {
    const url = await backend.boardAttachmentUrl(bestand);
    expect(url).toContain('base64,iVBORw0KGgo=');
    expect(fetchSpy, 'der Server wurde gefragt — der Bestands-Anhang liegt dort nicht')
      .not.toHaveBeenCalled();
  });

  it('deleteBoardAttachment fasst den Server nicht an — es gibt dort nichts zu löschen', async () => {
    await backend.deleteBoardAttachment(bestand);
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});
