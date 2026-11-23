/* @vitest-environment happy-dom */

/**
 * Karte #450 — der Transportweg von Karten-Anhängen im Server-Modus.
 *
 * Andis Meldung aus dem Produktivbetrieb: Bild an eine Karte anhängen →
 * „Speichern fehlgeschlagen", nach dem Reload ist der Anhang weg;
 * Beschreibungsänderungen gehen. Die Ursache war kein Fehler im
 * Attachment-Code, sondern ein nie benutzter: `createRESTAttachmentBackend`
 * war vollständig implementiert und hatte im GESAMTEN Frontend keinen
 * einzigen Aufrufer (F-798). Alle acht Aufrufstellen importierten direkt aus
 * `attachments.js`, wo der base64-Zweig greift — die Datei landete im
 * Board-State und riss beim nächsten PUT `MaxBodyBytes`.
 *
 * Diese Datei prüft beide Hälften des Fixes:
 *   1. VERHALTEN — das REST-Backend legt nichts im State ab (der Test, den
 *      die Kartenbeschreibung ausdrücklich verlangt).
 *   2. KORPUS — die Aufrufer gehen über das Plugin. Ohne diesen Wächter wäre
 *      ein direkter Import in einem späteren Commit wieder da, ohne dass ein
 *      Verhaltenstest anschlägt: Der Fehler ist ja gerade, dass der richtige
 *      Weg funktioniert und niemand ihn nimmt.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { __testing__ } from '../src/storage-plugin.js';

const hatRESTPlugin = typeof __testing__.createRESTPlugin === 'function';

/* ── 1. Verhalten ─────────────────────────────────────────────────────── */

describe.skipIf(!hatRESTPlugin)('Karte #450 — REST-Backend legt Karten-Anhänge NICHT im Board-State ab', () => {
  let plugin, backend, fetchSpy;

  beforeEach(() => {
    plugin = __testing__.createRESTPlugin();
    backend = plugin.attachments();
  });
  afterEach(() => { fetchSpy?.mockRestore(); });

  it('uploadFile schickt Multipart an den Attachment-Endpunkt und liefert Metadaten OHNE data', async () => {
    /* Der Server antwortet mit den Metadaten der abgelegten Datei — kein
       `data`-Feld, weil die Bytes auf dem Server liegen und nicht im JSON. */
    const antwort = { id: 'att-1', filename: 'foto.jpg', mime: 'image/jpeg', size: 4_000_000, created: '2026-08-28' };
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true, status: 201, json: async () => antwort,
    });

    /* boardId setzen — ohne sie liefert attachPath null und uploadFile wirft. */
    await plugin.preload('board-1').catch(() => {});
    const backendMitBoard = plugin.attachments();

    const file = new File(['x'.repeat(64)], 'foto.jpg', { type: 'image/jpeg' });
    const att = await backendMitBoard.uploadFile(file, { id: 'karte-1' });

    expect(att.data, 'das Attachment trägt base64-Daten — dann liegt es im Board-State '
      + 'und reist bei jedem PUT mit, genau der Defekt aus Karte #450').toBeUndefined();
    expect(att.id).toBe('att-1');

    const [url, opts] = fetchSpy.mock.calls.at(-1);
    expect(String(url)).toContain('/boards/board-1/attachments/karte-1');
    expect(opts.method).toBe('POST');
    expect(opts.body).toBeInstanceOf(FormData);
  });

  it('das Backend sagt selbst, dass Karten-Anhänge nicht im Board-State landen', () => {
    /* Diese Auskunft steuert die verlustbehaftete Verkleinerung im Modal.
       Stünde sie auf true, würde jedes Foto ohne Not nach JPEG konvertiert
       (Alphakanal weg) — für eine Datei, die gar nicht in den State geht. */
    expect(backend.kartenAnhangImBoardState()).toBe(false);
    /* Board-Anhänge seit Karte #443 ebenso wenig: Der Endpunkt
       `/boards/{id}/board-attachments` existiert jetzt. Bis dahin stand hier
       `true`, und das war korrekt — der Wert beschreibt den Transportweg,
       nicht einen Wunsch. */
    expect(backend.boardAnhangImBoardState()).toBe(false);
  });

  it('F-835: computeBoardBytes zählt echt — es gibt sehr wohl base64 im State', () => {
    /* Hier stand `return 0` mit dem Kommentar „keine Base64-Attachments im
       State", während `uploadBoardFile` vierzig Zeilen darüber selbst welche
       anlegt. Wer den Upload auf das Plugin umstellt und das nicht anfasst,
       verliert wortlos jede Grössenprüfung. */
    const board = { sprints: [], attachments: [{ id: 'bg', data: 'A'.repeat(4000) }] };
    expect(backend.computeBoardBytes(board)).toBeGreaterThan(0);
  });

  it('F-835: checkUpload prüft die Datei, statt pauschal {ok:true} zu liefern', () => {
    /* „Server setzt Limit via 413" beschreibt keinen Schutz, sondern seinen
       Ausfall: Der Nutzer sieht „Speichern fehlgeschlagen", nachdem er
       gewartet hat. Die Board-Grössenprüfung entfällt hier zu Recht (die
       Datei geht nicht in den State) — die Pro-Datei-Prüfung nicht. */
    const riesig = { name: 'riesig.bin', size: 999 * 1024 * 1024, type: 'application/octet-stream' };
    const board = { sprints: [], attachments: [] };
    expect(backend.checkUpload(riesig, board).ok).toBe(false);
  });

  it('Bestands-Anhänge aus der Zeit vor dem Umbau bleiben über ALLE drei Lesewege erreichbar', async () => {
    /* Alles, was vor dem Umbau im Server-Modus hochgeladen wurde, liegt als
       base64 im State und auf dem Server unter KEINER id. `readAttachment`
       und `attachmentUrl` hatten den Fallback; `downloadAttachment` nicht —
       es war der einzige Leseweg, der solche Anhänge stumm mit `false`
       quittierte. */
    const att = { id: 'alt-1', mime: 'image/png', filename: 'alt.png', data: 'iVBORw0KGgo=' };
    const url = await backend.attachmentUrl({ id: 'k' }, att);
    expect(url).toContain('base64,');

    fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true, blob: async () => new Blob(['x']),
    });
    const klick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    globalThis.URL.createObjectURL ??= () => 'blob:x';
    globalThis.URL.revokeObjectURL ??= () => {};

    await expect(backend.downloadAttachment({ id: 'k' }, att)).resolves.toBe(true);
    klick.mockRestore();
  });
});

/* ── 2. Korpus ────────────────────────────────────────────────────────── */

/* Kommentare entfernen, bevor gesucht wird (Fehlerklasse 47 / F-825): Sonst
   trifft der Wächter genau die Kommentare, die seine eigene Behebung
   dokumentieren — und meldet einen Verstoss, wo die Erklärung steht. */
function codeOhneKommentare(pfad) {
  return readFileSync(join(process.cwd(), pfad), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '');
}

/* Die Funktionen, bei denen der Transportweg entscheidet. `verkleinereBild`,
   `humanSize`, `safeMime` und `BASE64_FAKTOR` stehen bewusst NICHT hier: das
   sind reine Rechen-/Formatier-Operationen ohne Bezug zum Speicherort. */
const ERLAUBTE_DIREKTIMPORTE = new Set([
  'verkleinereBild', 'humanSize', 'safeMime', 'BASE64_FAKTOR',
  'BG_MAX_KANTE', 'BG_JPEG_QUALITAET', 'ATTACHMENTS_DIR',
  /* Karte #467 — beide transport-frei, aus unterschiedlichen Gründen:
     `thumbnailUrl` rechnet ausschliesslich auf dem Anhang-OBJEKT (base64-Feld
     → data-URL) und fragt keinen Speicherort; das ist dieselbe Kategorie wie
     `safeMime`. `reicheCoverThumbnailNach` fasst Anhänge an, hat aber KEINEN
     eigenen Zugriff: Lesen und Schreiben kommen als Parameter herein, und
     `src/main.js` verdrahtet das Lesen mit `anhangBackend().readAttachment`.
     Der Transportweg bleibt damit beim Plugin — genau das, was dieser
     Wächter sichern soll. Wer die Funktion umbaut und ihr einen direkten
     Speicherzugriff gibt, muss diesen Eintrag wieder entfernen. */
  'thumbnailUrl', 'reicheCoverThumbnailNach', 'entferneThumbnail',
  /* Sprint 69, F-1088 — `normalisiereAnhang` wandelt einen Alt-Eintrag
     (reiner id-String aus dem Mail-Ingest vor Sprint 69) in `{ id }` um und
     fragt keinen Speicherort: reine Formumwandlung wie `safeMime`. */
  'normalisiereAnhang',
  /* Sprint 69, F-1107 (Nebenbefund der Nacharbeit zu F-855/#538) —
     `istSvgMime` ist eine reine String-Prüfung auf dem `mime`-Feld, exakt
     dieselbe Kategorie wie `safeMime`: kein Speicherzugriff, kein Bezug zum
     Transportweg. */
  'istSvgMime',
]);

/* Dateien, die das Modul definieren bzw. es per Vertrag kapseln — sie DÜRFEN
   direkt importieren. Alles andere in `src/` nicht. */
const AUSGENOMMEN = new Set(['attachments.js', 'storage-plugin.js']);

/* Die Dateiliste wird ABGELEITET, nicht aufgezählt (Frontend-Kritiker
   Sprint 61): Die erste Fassung listete `modal.js`, `main.js` und
   `configdialog.js` hart — und übersah damit `renderer.js` und
   `identity.js`, die beide direkt importieren. Der Wächter hätte die
   Cover-Regression im selben Commit gefunden, in dem er gebaut wurde. Eine
   aufgezählte Liste prüft, woran der Autor gedacht hat; eine abgeleitete
   prüft den Bestand. */
function alleQuelldateien(verzeichnis = 'src') {
  const wurzel = join(process.cwd(), verzeichnis);
  const treffer = [];
  for (const eintrag of readdirSync(wurzel, { withFileTypes: true })) {
    const relativ = `${verzeichnis}/${eintrag.name}`;
    if (eintrag.isDirectory()) { treffer.push(...alleQuelldateien(relativ)); continue; }
    if (!eintrag.name.endsWith('.js')) continue;
    if (AUSGENOMMEN.has(eintrag.name)) continue;
    treffer.push(relativ);
  }
  return treffer;
}

/* Geprüft wird die IMPORT-ANWEISUNG, nicht die Aufrufform. Die erste Fassung
   suchte nach `Attachments.uploadFile(` und `uploadFile(` — beides ist
   umgehbar, gemessen: ein Alias-Import (`import { uploadFile as hochladen }`)
   und ein dynamischer Zugriff (`Attachments["uploadFile"]`) rutschten
   durch. Wer nicht importiert, kann auch nicht aufrufen; das ist die
   lückenlose Stelle. */
function importierteNamen(quelltext) {
  const namen = [];
  const re = /import\s+([^;]+?)\s+from\s+['"][^'"]*\/attachments\.js['"]/g;
  for (const m of quelltext.matchAll(re)) {
    const klausel = m[1].trim();
    if (klausel.startsWith('*')) { namen.push('*'); continue; }
    const geschweift = klausel.match(/\{([^}]*)\}/);
    if (!geschweift) { namen.push(klausel); continue; }
    for (const teil of geschweift[1].split(',')) {
      const roh = teil.trim();
      if (roh) namen.push(roh.split(/\s+as\s+/)[0].trim());
    }
  }
  return namen;
}

describe('Karte #450 — kein Aufrufer greift am Storage-Plugin vorbei', () => {
  const dateien = alleQuelldateien();

  it('die Ableitung findet überhaupt Dateien (sonst prüft der Wächter nichts)', () => {
    /* Ein Wächter, der über eine leere Menge iteriert, ist immer grün. */
    expect(dateien.length).toBeGreaterThan(20);
    expect(dateien).toContain('src/modal.js');
    expect(dateien).toContain('src/renderer.js');
    expect(dateien).toContain('src/identity.js');
  });

  for (const datei of dateien) {
    it(`${datei} importiert aus attachments.js nur Transport-freie Helfer`, () => {
      const code = codeOhneKommentare(datei);
      const verboten = importierteNamen(code).filter(n => !ERLAUBTE_DIREKTIMPORTE.has(n));
      expect(verboten, `${datei} importiert ${verboten.join(', ')} direkt aus attachments.js. `
        + `Im Server-Modus geht das am REST-Backend vorbei — Uploads landen als base64 im `
        + `Board-State, und Lesewege finden REST-Anhänge nicht (Karte #450 / F-798). `
        + `Über \`StoragePlugin.getActivePlugin().attachments()\` gehen.`).toEqual([]);
    });
  }

  it('das REST-Attachment-Backend hat überhaupt Aufrufer (die Wurzel von F-798)', () => {
    /* Der eigentliche Befund war nicht „eine Zeile ist falsch", sondern
       „das ganze Backend ist toter Code". Dieser Wächter prüft die
       Gegenrichtung: Irgendwer muss `attachments()` am Plugin aufrufen. */
    const treffer = dateien
      .map(codeOhneKommentare)
      .filter(c => /getActivePlugin\(\)\??\.attachments\??\.\(\)/.test(c));
    expect(treffer.length, 'kein einziger Aufrufer geht über Storage.plugin.attachments() — '
      + 'das REST-Backend wäre wieder toter Code').toBeGreaterThanOrEqual(3);
  });
});
