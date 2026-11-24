/* @vitest-environment happy-dom */

/**
 * Karte #460 / [[F-871]] — ein fehlgeschlagenes Löschen darf den Eintrag
 * nicht mitnehmen.
 *
 * DER DEFEKT: `deleteBoardAttachment` verschluckte jeden Fehler (leerer
 * `catch`, `res.ok` nie geprüft), und alle vier Aufrufer in
 * `src/configdialog.js` entfernten den Eintrag danach BEDINGUNGSLOS aus
 * `board.attachments`. Schlug das Löschen fehl, blieb die Datei
 * unreferenziert unter `<board>/__board__/` liegen — ohne Aufräumweg: Die
 * Purge-Route ist für die Board-Ablage seit Karte #443 bewusst gesperrt,
 * `DeleteBoard` greift erst beim Löschen des ganzen Boards.
 *
 * DIE ENTSCHEIDUNG (Maintainer, 2026-08-29): keine neue Purge-Route, sondern der
 * Fehler wird sichtbar. Eintrag bleibt stehen, Nutzer bekommt eine Meldung.
 *
 * WAS HIER GEPRÜFT WIRD — in zwei Ebenen, weil der Defekt aus zwei Hälften
 * bestand und eine Hälfte allein nichts nützt:
 *   1. Der Vertrag der Backends: melden sie einen Fehlschlag überhaupt?
 *      Ein Aufrufer, der auswertet, wo niemand meldet, wertet `undefined` aus.
 *   2. Die Auswertung in `entferneBoardAnhang`: bleibt der Eintrag stehen?
 *
 * WAS HIER NICHT GEPRÜFT WIRD: die vier Aufrufstellen selbst — sie sind
 * modul-private Closures in `configdialog.js` (dieselbe Lage wie in
 * `board-anhang-reihenfolge-863.test.js`, dort ist die Begründung
 * ausführlich). Dass sie `entferneBoardAnhang` benutzen statt am Backend
 * vorbeizugreifen, sichert der Struktur-Wächter dort zu; was die Funktion
 * dann tut, steht hier.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { entferneBoardAnhang } from '../src/configdialog.js';
import { __testing__ } from '../src/storage-plugin.js';

const anhang = () => ({ id: 'att-1', filename: 'bg.jpg', mime: 'image/jpeg' });
const boardMit = (...atts) => ({ attachments: atts });

describe('F-871 — entferneBoardAnhang wertet das Ergebnis aus', () => {
  let alertSpy, alertVorher;

  /* `globalThis.alert` direkt setzen statt `vi.spyOn`: happy-dom bringt die
     Funktion nicht als eigene Property mit — dasselbe Muster wie in
     `attachments-komfort.test.js` und `attachments-upload-race.test.js`. */
  beforeEach(() => { alertVorher = globalThis.alert; globalThis.alert = alertSpy = vi.fn(); });
  afterEach(() => { globalThis.alert = alertVorher; });

  it('Löschen geglückt → Eintrag ist weg, keine Meldung', async () => {
    const att = anhang();
    const board = boardMit(att, { id: 'att-2' });
    const backend = { deleteBoardAttachment: vi.fn().mockResolvedValue(true) };

    expect(await entferneBoardAnhang(board, att, backend)).toBe(true);
    expect(board.attachments.map(a => a.id)).toEqual(['att-2']);
    expect(alertSpy).not.toHaveBeenCalled();
  });

  it('Löschen fehlgeschlagen → Eintrag BLEIBT und der Nutzer erfährt es', async () => {
    const att = anhang();
    const board = boardMit(att);
    const backend = { deleteBoardAttachment: vi.fn().mockResolvedValue(false) };

    expect(await entferneBoardAnhang(board, att, backend)).toBe(false);
    expect(board.attachments.map(a => a.id),
      'der Eintrag wurde trotz Fehlschlag entfernt — die Datei liegt jetzt '
      + 'unreferenziert in der Board-Ablage, genau F-871').toEqual(['att-1']);
    expect(alertSpy, 'der Fehlschlag lief stumm durch').toHaveBeenCalledTimes(1);
  });

  /* Bewusste Asymmetrie, siehe Kommentar an der Funktion: NUR ein explizites
     `false` gilt als Fehlschlag. Ein Backend, das (noch) nichts zurückgibt,
     verhält sich wie vor dieser Karte. Andersherum bliebe bei jedem
     geglückten Löschen ein Eintrag ohne Datei stehen — ein kaputtes Bild ist
     schlimmer als eine verwaiste Datei. */
  it('Backend ohne Rückgabewert → verhält sich wie bisher (Eintrag weg)', async () => {
    const att = anhang();
    const board = boardMit(att);
    const backend = { deleteBoardAttachment: vi.fn().mockResolvedValue(undefined) };

    expect(await entferneBoardAnhang(board, att, backend)).toBe(true);
    expect(board.attachments).toEqual([]);
  });

  it('kein Anhang übergeben → nichts zu tun, kein Backend-Aufruf', async () => {
    const backend = { deleteBoardAttachment: vi.fn() };
    expect(await entferneBoardAnhang(boardMit(), null, backend)).toBe(true);
    expect(backend.deleteBoardAttachment).not.toHaveBeenCalled();
  });

  it('board.attachments fehlt → kein Absturz', async () => {
    const att = anhang();
    const board = {};
    const backend = { deleteBoardAttachment: vi.fn().mockResolvedValue(true) };
    expect(await entferneBoardAnhang(board, att, backend)).toBe(true);
    expect(board.attachments).toEqual([]);
  });
});

/* ── Ebene 1: Melden die Backends einen Fehlschlag überhaupt? ───────────── */

const hatRESTPlugin = typeof __testing__.createRESTPlugin === 'function';

describe.skipIf(!hatRESTPlugin)('F-871 — das REST-Backend meldet den Fehlschlag', () => {
  let backend, fetchSpy;

  beforeEach(async () => {
    const plugin = __testing__.createRESTPlugin();
    await plugin.preload('board-1').catch(() => {});
    backend = plugin.attachments();
  });
  afterEach(() => { fetchSpy?.mockRestore(); });

  it.each([
    ['204 — gelöscht', { ok: true, status: 204 }, true],
    ['404 — war schon weg, der Endpunkt ist idempotent', { ok: false, status: 404 }, true],
    ['500 — Serverfehler', { ok: false, status: 500 }, false],
    ['403 — keine Berechtigung', { ok: false, status: 403 }, false],
  ])('%s → %s', async (_name, antwort, erwartet) => {
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(antwort);
    expect(await backend.deleteBoardAttachment({ id: 'att-1' })).toBe(erwartet);
  });

  it('Netzabbruch → false, nicht stillschweigend erfolgreich', async () => {
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockRejectedValue(new TypeError('network'));
    expect(await backend.deleteBoardAttachment({ id: 'att-1' })).toBe(false);
  });

  it('Bestandsdaten im State → true ohne Server-Kontakt', async () => {
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: false, status: 500 });
    expect(await backend.deleteBoardAttachment({ id: 'att-alt', data: 'iVBORw0KGgo=' })).toBe(true);
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});
