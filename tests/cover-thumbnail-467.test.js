/**
 * Karte #467 — Cover-Bilder laden in voller Grösse, es gibt kein Thumbnail.
 *
 * ANDIS BEOBACHTUNG (2026-08-29): „Wenn eine Karte ein Bild als Cover-Bild
 * hat, dauert es ganz schön lang, bis das angezeigt wird." Gemessen: bis zu
 * 1920 px gespeicherte Kante auf rund 264 px Anzeigebreite (im FSA-Volume
 * sogar das unveränderte Original), dazu ein asynchroner Nachladeweg — im
 * Server-Modus ein HTTP-Request pro Cover.
 *
 * DIE ZUSICHERUNG, AUF DIE ES ANKOMMT: Liegt ein Thumbnail am Anhang, zeichnet
 * der Renderer das Cover SYNCHRON und holt das Original gar nicht erst. Ein
 * Test, der nur prüft, dass irgendwann ein Hintergrundbild gesetzt ist, wäre
 * auch ohne den Fix grün — der asynchrone Weg tut das ja seit Karte #173.
 * Geprüft wird deshalb beides: dass das Bild ohne jedes `await` dasteht, UND
 * dass das Backend nicht gefragt wurde.
 */

/* @vitest-environment happy-dom */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { erzeugeThumbnail, thumbnailUrl, THUMB_MAX_KANTE, reicheCoverThumbnailNach, computeBoardBytes, entferneThumbnail } from '../src/attachments.js';

describe('thumbnailUrl — die synchrone Quelle für den Renderer', () => {
  it('baut eine data-URL aus thumb + thumbMime', () => {
    const url = thumbnailUrl({ thumb: 'AAAB', thumbMime: 'image/webp' });
    expect(url).toBe('data:image/webp;base64,AAAB');
  });

  it('fällt auf image/jpeg zurück, wenn thumbMime fehlt', () => {
    expect(thumbnailUrl({ thumb: 'AAAB' })).toBe('data:image/jpeg;base64,AAAB');
  });

  /* Seit dem Sprint-63-Kritiker gilt eine POSITIVLISTE statt blosser
     Neutralisierung: `safeMime` prüft nur die FORM eines MIME-Typs, nicht
     seinen Inhalt — `image/svg+xml` wäre anstandslos durchgegangen, obwohl
     SVG seit F-128 von jeder Bildverarbeitung ausgeschlossen ist. Ein
     unbekannter Typ liefert jetzt `null`, und der Renderer nimmt den
     asynchronen Weg. */
  it('lehnt einen manipulierten MIME-Typ ab, statt ihn nur zu neutralisieren', () => {
    expect(thumbnailUrl({ thumb: 'AAAB', thumbMime: 'image/png") ; background: url("evil' })).toBeNull();
  });

  it('lehnt SVG ab — dieselbe Regel wie für att.mime seit F-128', () => {
    expect(thumbnailUrl({ thumb: 'AAAB', thumbMime: 'image/svg+xml' })).toBeNull();
  });

  it('lehnt eine Nutzlast ab, die kein base64 sein kann', () => {
    /* Ein abgeschnittener oder von Hand editierter Wert ergibt sonst ein Bild,
       das nie lädt — und `.card--has-cover` macht die Karte dann unlesbar
       (weisse Schrift auf hellem Grund, CSS kennt kein `onerror`). */
    expect(thumbnailUrl({ thumb: 'nicht base64!', thumbMime: 'image/webp' })).toBeNull();
    expect(thumbnailUrl({ thumb: 'AAA"B', thumbMime: 'image/webp' })).toBeNull();
  });

  it('escapt Anführungszeichen selbst, statt sich auf den Aufrufer zu verlassen', () => {
    /* Das Escaping sass beim Renderer. Ein zweiter Konsument der exportierten
       Funktion hätte die Lücke geerbt, ohne es zu merken. */
    expect(thumbnailUrl({ thumb: 'AAAB', thumbMime: 'image/webp' })).not.toContain('"');
  });

  it('liefert null, wenn kein Thumbnail vorliegt — der Aufrufer nimmt dann das Original', () => {
    expect(thumbnailUrl({})).toBeNull();
    expect(thumbnailUrl({ thumb: '' })).toBeNull();
    expect(thumbnailUrl(null)).toBeNull();
    expect(thumbnailUrl({ thumb: 123 })).toBeNull();
  });
});

describe('erzeugeThumbnail', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    delete globalThis.createImageBitmap;
  });

  function stelleCanvas(blobGroesse = 20_000, typ = 'image/jpeg') {
    const cv = {
      width: 0, height: 0,
      getContext: () => ({
        drawImage() {},
        getImageData: () => ({ data: Uint8ClampedArray.from([1, 1, 1, 255]) }),
      }),
      toBlob: (cb, t) => cb(new Blob([new Uint8Array(blobGroesse)], { type: typ ?? t })),
    };
    const alterCreate = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation(
      tag => (tag === 'canvas' ? cv : alterCreate(tag)));
    return cv;
  }

  it('verkleinert auf die Thumbnail-Kante, nicht auf die Hintergrund-Kante', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const cv = stelleCanvas();

    const mini = await erzeugeThumbnail(new File([new Uint8Array(900_000)], 'cover.jpg', { type: 'image/jpeg' }));

    expect(cv.width, 'die Zielkante ist THUMB_MAX_KANTE, nicht BG_MAX_KANTE').toBe(THUMB_MAX_KANTE);
    expect(mini).not.toBeNull();
    expect(typeof mini.thumb).toBe('string');
    expect(mini.thumb.length).toBeGreaterThan(0);
    expect(mini.thumbMime).toBe('image/jpeg');
  });

  /* Der Rückgabewert `null` ist kein Fehler, sondern die Ansage „nimm das
     Original". Ohne diese Fälle würde ein „Thumbnail" gespeichert, das eine
     Kopie des Originals ist — reiner State-Zuwachs ohne Gegenwert. */
  it('liefert null für Nicht-Bilder', async () => {
    expect(await erzeugeThumbnail(new File([new Uint8Array(10)], 'x.pdf', { type: 'application/pdf' }))).toBeNull();
  });

  it('liefert null, wenn gar kein Blob da ist', async () => {
    expect(await erzeugeThumbnail(null)).toBeNull();
    expect(await erzeugeThumbnail(undefined)).toBeNull();
  });

  it('liefert null für GIF — dort ist jede Datei potenziell animiert', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    stelleCanvas();
    expect(await erzeugeThumbnail(new File([new Uint8Array(500_000)], 'a.gif', { type: 'image/gif' }))).toBeNull();
  });

  /* WebP stand bis zum Sprint-63-Kritiker pauschal in ANIMIERBARE_FORMATE.
     Das nahm dem Thumbnail genau die Bilder weg, die Karte #466 kurz zuvor
     nach WebP gewandelt hatte: ein freigestelltes PNG wird beim Anhängen zu
     WebP, und beim Cover-Setzen stieg `verkleinereBild` dann sofort aus.
     Seither entscheidet der `ANIM`-Chunk, nicht der MIME-Typ. */
  it('erzeugt ein Thumbnail aus einem STATISCHEN WebP (Kritiker-Befund Sprint 63)', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    stelleCanvas();
    /* RIFF-Kopf ohne ANIM-Chunk = Standbild. */
    const bytes = new Uint8Array(500_000);
    bytes.set([0x52, 0x49, 0x46, 0x46], 0);            // 'RIFF'
    bytes.set([0x57, 0x45, 0x42, 0x50], 8);            // 'WEBP'
    const mini = await erzeugeThumbnail(new File([bytes], 'logo.webp', { type: 'image/webp' }));
    expect(mini, 'ein statisches WebP MUSS ein Thumbnail bekommen').not.toBeNull();
  });

  it('lässt ein ANIMIERTES WebP unangetastet — der ANIM-Chunk entscheidet', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    stelleCanvas();
    const bytes = new Uint8Array(500_000);
    bytes.set([0x52, 0x49, 0x46, 0x46], 0);            // 'RIFF'
    bytes.set([0x57, 0x45, 0x42, 0x50], 8);            // 'WEBP'
    bytes.set([0x41, 0x4E, 0x49, 0x4D], 30);           // 'ANIM'
    expect(await erzeugeThumbnail(new File([bytes], 'bewegt.webp', { type: 'image/webp' }))).toBeNull();
  });

  it('liefert null, wenn das Bild bereits kleiner als die Zielkante ist', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 300, height: 200, close() {} }));
    stelleCanvas();
    expect(await erzeugeThumbnail(new File([new Uint8Array(5_000)], 'klein.jpg', { type: 'image/jpeg' }))).toBeNull();
  });

  it('liefert null statt zu werfen, wenn die Bild-API fehlt', async () => {
    delete globalThis.createImageBitmap;
    expect(await erzeugeThumbnail(new File([new Uint8Array(900_000)], 'cover.jpg', { type: 'image/jpeg' }))).toBeNull();
  });

  /* Transparenz-Erhalt aus Karte #466 gilt auch für das Thumbnail — sonst
     bekäme ein freigestelltes Cover einen schwarzen Rand, obwohl das
     Original ihn nicht hat. */
  it('erhält die Transparenz eines PNG-Covers (Karte #466 gilt auch hier)', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const cv = {
      width: 0, height: 0,
      getContext: () => ({
        drawImage() {},
        getImageData: () => ({ data: Uint8ClampedArray.from([0, 0, 0, 0]) }),   // durchsichtig
      }),
      toBlob: (cb, t) => cb(new Blob([new Uint8Array(20_000)], { type: t })),
    };
    const alterCreate = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation(
      tag => (tag === 'canvas' ? cv : alterCreate(tag)));

    const mini = await erzeugeThumbnail(new File([new Uint8Array(900_000)], 'logo.png', { type: 'image/png' }));

    expect(mini.thumbMime).toBe('image/webp');
  });
});

describe('reicheCoverThumbnailNach — der Weg vom Cover-Klick zum gespeicherten Thumbnail', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    delete globalThis.createImageBitmap;
  });

  /* Canvas + Bild-API so stellen, dass ein Thumbnail wirklich entsteht. */
  function bildApiStellen() {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const cv = {
      width: 0, height: 0,
      getContext: () => ({
        drawImage() {},
        getImageData: () => ({ data: Uint8ClampedArray.from([1, 1, 1, 255]) }),
      }),
      toBlob: (cb, t) => cb(new Blob([new Uint8Array(20_000)], { type: t })),
    };
    const alterCreate = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation(
      tag => (tag === 'canvas' ? cv : alterCreate(tag)));
  }

  function karteMitCover(overrides = {}) {
    return {
      id: 'k1',
      coverAttachmentId: 'a1',
      attachments: [{ id: 'a1', mime: 'image/jpeg', filename: 'cover.jpg' }],
      ...overrides,
    };
  }

  /* `'blob' in opts` statt `blob ?? default`: Ein absichtlich auf `null`
     gesetzter Blob ist der interessante Fall (Backend liefert nichts), und
     `null ?? default` hätte genau ihn durch die Standard-Datei ersetzt — der
     Test wäre grün geworden, ohne den Zweig je zu betreten. Beim ersten
     Anlauf genau so passiert. */
  function deps(karte, opts = {}) {
    const { mutationsZiel } = opts;
    return {
      findeKarte: vi.fn(() => karte),
      leseBlob:   vi.fn(async () => ('blob' in opts)
        ? opts.blob
        : new File([new Uint8Array(900_000)], 'cover.jpg', { type: 'image/jpeg' })),
      mutiere:    vi.fn(fn => fn(mutationsZiel === undefined ? karte : mutationsZiel)),
    };
  }

  it('schreibt thumb und thumbMime an den Cover-Anhang', async () => {
    bildApiStellen();
    const karte = karteMitCover();
    const d = deps(karte);

    expect(await reicheCoverThumbnailNach('k1', 'a1', d)).toBe(true);
    expect(typeof karte.attachments[0].thumb).toBe('string');
    expect(karte.attachments[0].thumb.length).toBeGreaterThan(0);
    expect(karte.attachments[0].thumbMime).toBe('image/jpeg');
  });

  it('rührt einen bereits vorhandenen thumb nicht an und liest das Original gar nicht erst', async () => {
    bildApiStellen();
    const karte = karteMitCover();
    karte.attachments[0].thumb = 'SCHON-DA';
    const d = deps(karte);

    expect(await reicheCoverThumbnailNach('k1', 'a1', d)).toBe(false);
    expect(d.leseBlob, 'ohne Not kein Lesevorgang').not.toHaveBeenCalled();
    expect(karte.attachments[0].thumb).toBe('SCHON-DA');
  });

  /* DER RACE. Zwischen `leseBlob` und `mutiere` liegt ein await; in dieser
     Zeit kann sich das Board geändert haben. Ein Thumbnail an ein Bild zu
     hängen, das gar nicht mehr das Cover ist, wäre ein sichtbar falsches
     Vorschaubild. Ohne die Neuprüfung im Mutations-Block ist dieser Test rot. */
  it('verwirft das Ergebnis, wenn inzwischen ein ANDERES Cover gesetzt wurde', async () => {
    bildApiStellen();
    const karte = karteMitCover();
    /* Der Zustand, den `mutiere` sieht: dieselbe Karte, aber mit anderem Cover. */
    const spaeter = {
      id: 'k1',
      coverAttachmentId: 'a2',
      attachments: [{ id: 'a1', mime: 'image/jpeg' }, { id: 'a2', mime: 'image/jpeg' }],
    };
    const d = deps(karte, { mutationsZiel: spaeter });

    expect(await reicheCoverThumbnailNach('k1', 'a1', d)).toBe(false);
    expect(spaeter.attachments[0].thumb, 'kein Thumbnail an einem Bild, das nicht mehr Cover ist').toBeUndefined();
  });

  it('verwirft das Ergebnis, wenn die Karte inzwischen weg ist', async () => {
    bildApiStellen();
    const karte = karteMitCover();
    const d = deps(karte, { mutationsZiel: null });

    expect(await reicheCoverThumbnailNach('k1', 'a1', d)).toBe(false);
  });

  it('verwirft das Ergebnis, wenn der Anhang inzwischen gelöscht wurde', async () => {
    bildApiStellen();
    const karte = karteMitCover();
    const d = deps(karte, { mutationsZiel: { id: 'k1', coverAttachmentId: 'a1', attachments: [] } });

    expect(await reicheCoverThumbnailNach('k1', 'a1', d)).toBe(false);
  });

  it('endet still, wenn das Backend keinen Blob liefert', async () => {
    bildApiStellen();
    const karte = karteMitCover();
    const d = deps(karte, { blob: null });

    expect(await reicheCoverThumbnailNach('k1', 'a1', d)).toBe(false);
    expect(karte.attachments[0].thumb).toBeUndefined();
  });

  /* Ein Board ohne Thumbnail funktioniert vollständig — ein Lesefehler darf
     deshalb nicht bis zum Aufrufer durchschlagen. */
  it('endet still, wenn das Lesen wirft', async () => {
    bildApiStellen();
    const karte = karteMitCover();
    const d = { findeKarte: () => karte, leseBlob: async () => { throw new Error('404'); }, mutiere: vi.fn() };

    await expect(reicheCoverThumbnailNach('k1', 'a1', d)).resolves.toBe(false);
    expect(d.mutiere).not.toHaveBeenCalled();
  });

  it('kommt mit einer unbekannten Karte klar', async () => {
    const d = { findeKarte: () => null, leseBlob: vi.fn(), mutiere: vi.fn() };
    expect(await reicheCoverThumbnailNach('weg', 'a1', d)).toBe(false);
    expect(d.leseBlob).not.toHaveBeenCalled();
  });
});

describe('computeBoardBytes zählt das Thumbnail mit (Abhängigkeits-Check Sprint-63-Abschluss)', () => {
  /* Das Thumbnail liegt als base64 im Board-State und reist bei jedem PUT
     mit. Zählte die Grössenprüfung es nicht, sähe sie einen Teil des Bodys
     nicht — derselbe Defekt wie F-835, nur an der Nachbarstelle. */
  it('ein Anhang OHNE data, aber MIT thumb wiegt mehr als null', () => {
    const board = { sprints: [{ cards: [{ attachments: [{ id: 'a1', thumb: 'A'.repeat(4000) }] }] }] };
    expect(computeBoardBytes(board)).toBeGreaterThan(2900);
  });

  /* Der Server-Modus ist der scharfe Fall: `data` liegt dort NICHT im State
     (Multipart seit Karte #450), das Thumbnail aber schon. */
  it('zählt data und thumb zusammen, nicht nur eines von beiden', () => {
    const nurData  = { sprints: [{ cards: [{ attachments: [{ id: 'a', data: 'A'.repeat(4000) }] }] }] };
    const beides   = { sprints: [{ cards: [{ attachments: [{ id: 'a', data: 'A'.repeat(4000), thumb: 'B'.repeat(4000) }] }] }] };
    expect(computeBoardBytes(beides)).toBeGreaterThan(computeBoardBytes(nurData));
  });

  it('gilt auch für Board-Anhänge (Hintergrundbild)', () => {
    const board = { sprints: [], attachments: [{ id: 'bg', thumb: 'A'.repeat(4000) }] };
    expect(computeBoardBytes(board)).toBeGreaterThan(2900);
  });

  it('kommt mit Anhängen ohne beide Felder klar', () => {
    const board = { sprints: [{ cards: [{ attachments: [{ id: 'a' }, null] }] }], attachments: [] };
    expect(computeBoardBytes(board)).toBe(0);
  });
});

describe('Grössendeckel beim Nachreichen (Kritiker-Befund Sprint 63)', () => {
  afterEach(() => { vi.restoreAllMocks(); delete globalThis.createImageBitmap; });

  function bildApi() {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const cv = {
      width: 0, height: 0,
      getContext: () => ({ drawImage() {}, getImageData: () => ({ data: Uint8ClampedArray.from([1, 1, 1, 255]) }) }),
      toBlob: (cb, t) => cb(new Blob([new Uint8Array(20_000)], { type: t })),
    };
    const alterCreate = document.createElement.bind(document);
    vi.spyOn(document, 'createElement').mockImplementation(tag => (tag === 'canvas' ? cv : alterCreate(tag)));
  }
  const karte = () => ({ id: 'k1', coverAttachmentId: 'a1', attachments: [{ id: 'a1', mime: 'image/jpeg' }] });
  const basisDeps = (k) => ({
    findeKarte: () => k,
    leseBlob: async () => new File([new Uint8Array(900_000)], 'c.jpg', { type: 'image/jpeg' }),
    mutiere: (fn) => fn(k),
  });

  /* Dieser Weg schreibt rund 29 KB base64 in den Board-State, ohne dass der
     Nutzer eine Datei hochlädt — `checkUpload` läuft nur auf dem Upload-Pfad
     und sieht ihn nie. Ohne Deckel wächst ein Board allein dadurch, dass
     jemand bei vorhandenen Anhängen das Cover durchprobiert. */
  it('schreibt NICHT, wenn die harte Grenze das Thumbnail nicht mehr aufnimmt', async () => {
    bildApi();
    const k = karte();
    expect(await reicheCoverThumbnailNach('k1', 'a1', { ...basisDeps(k), passtNochRein: () => false })).toBe(false);
    expect(k.attachments[0].thumb).toBeUndefined();
  });

  it('schreibt, wenn die Grenze es zulässt', async () => {
    bildApi();
    const k = karte();
    expect(await reicheCoverThumbnailNach('k1', 'a1', { ...basisDeps(k), passtNochRein: () => true })).toBe(true);
    expect(typeof k.attachments[0].thumb).toBe('string');
  });

  /* Der Deckel bekommt die tatsächliche Nutzlast zu sehen — sonst könnte er
     nur raten, wie gross das Thumbnail wird. */
  it('reicht dem Deckel die base64-Nutzlast durch, nicht nur ein Signal', async () => {
    bildApi();
    const k = karte();
    let gesehen = null;
    await reicheCoverThumbnailNach('k1', 'a1', { ...basisDeps(k), passtNochRein: (b) => { gesehen = b; return true; } });
    expect(typeof gesehen).toBe('string');
    expect(gesehen.length).toBeGreaterThan(0);
    expect(gesehen).toBe(k.attachments[0].thumb);
  });

  /* Fehlt der Deckel ganz (Aufrufer ohne diese Fähigkeit), darf das Nachreichen
     nicht stillschweigend ausfallen. */
  it('schreibt auch ohne passtNochRein — der Deckel ist optional', async () => {
    bildApi();
    const k = karte();
    expect(await reicheCoverThumbnailNach('k1', 'a1', basisDeps(k))).toBe(true);
  });
});

describe('entferneThumbnail — verwaiste Thumbnails aufräumen (Kritiker-Befund Sprint 63)', () => {
  it('entfernt thumb und thumbMime vom abgewählten Anhang', () => {
    const karte = { attachments: [{ id: 'a1', thumb: 'X', thumbMime: 'image/webp' }] };
    expect(entferneThumbnail(karte, 'a1')).toBe(true);
    expect('thumb' in karte.attachments[0]).toBe(false);
    expect('thumbMime' in karte.attachments[0]).toBe(false);
  });

  /* Der Anhang selbst bleibt unangetastet — nur seine Vorschau geht. */
  it('lässt den Anhang im Übrigen unverändert', () => {
    const karte = { attachments: [{ id: 'a1', mime: 'image/png', data: 'DATA', thumb: 'X' }] };
    entferneThumbnail(karte, 'a1');
    expect(karte.attachments[0].data).toBe('DATA');
    expect(karte.attachments[0].mime).toBe('image/png');
  });

  it('meldet false, wenn es nichts zu entfernen gibt', () => {
    expect(entferneThumbnail({ attachments: [{ id: 'a1' }] }, 'a1')).toBe(false);
    expect(entferneThumbnail({ attachments: [] }, 'a1')).toBe(false);
    expect(entferneThumbnail(null, 'a1')).toBe(false);
    expect(entferneThumbnail({ attachments: [{ id: 'a1', thumb: 'X' }] }, null)).toBe(false);
  });

  it('fasst nur den genannten Anhang an', () => {
    const karte = { attachments: [{ id: 'a1', thumb: 'X' }, { id: 'a2', thumb: 'Y' }] };
    entferneThumbnail(karte, 'a1');
    expect(karte.attachments[1].thumb).toBe('Y');
  });
});
