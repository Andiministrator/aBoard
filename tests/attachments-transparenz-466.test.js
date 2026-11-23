/**
 * Karte #466 — Bild-Anhänge verlieren ihre Transparenz (Rest aus F-821).
 *
 * DER FEHLER, DEN DIESE DATEI FESTNAGELT: `verkleinereBild` schrieb jedes
 * verkleinerte Bild als `image/jpeg` zurück. JPEG kennt keinen Alphakanal —
 * ein freigestelltes Logo, eine Grafik mit transparentem Rand oder ein
 * Screenshot mit Fensterschatten verlor seine Transparenz still und nicht
 * umkehrbar, denn das Original wird nicht aufbewahrt.
 *
 * WAS HIER NICHT GEPRÜFT WERDEN KANN — und wie damit umgegangen wird:
 * happy-dom bringt kein Bild-Decoding mit. Sondiert (Sprint 63): `getContext`
 * liefert zwar ein Objekt, dessen `drawImage` und `getImageData` sind aber
 * beide `null`, `createImageBitmap` und `ImageData` fehlen ganz. Aus einer
 * echten PNG-Datei lassen sich hier also keine Pixel gewinnen.
 *
 * Die Karte verlangt ausdrücklich, dass ein Test die ALPHA-WERTE liest und
 * nicht nur `att.mime` vergleicht — ein reiner MIME-Test wäre auch mit dem
 * alten Code grün, sobald jemand nur die Endung ändert. Der Ausweg ist die
 * Zweiteilung des Fixes:
 *
 *   1. `hatTransparentePixel(daten)` ist rein und bekommt hier ECHTE
 *      RGBA-Quadrupel — kein Mock, keine Stellvertreterfrage.
 *   2. Die Formatwahl wird über einen Canvas-Ersatz geprüft, dessen
 *      `getImageData` echte Pixeldaten liefert. Zugesichert wird, mit welchem
 *      Typ `toBlob` gerufen wird — die Entscheidung selbst, nicht ihr Etikett.
 *
 * Was damit ungeprüft bleibt: dass ein realer Browser aus einem realen PNG
 * dieselben Alpha-Werte auf den Canvas legt. Das ist Browser-Verhalten und in
 * keiner happy-dom-Umgebung messbar.
 */

/* @vitest-environment happy-dom */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { verkleinereBild, hatTransparentePixel } from '../src/attachments.js';

/* Ein Canvas-Ersatz, der Pixel liefern kann. `pixel` sind RGBA-Quadrupel;
   fehlen sie, verhält sich der Ersatz wie eine Umgebung ohne `getImageData`
   (genau die, die happy-dom bietet). */
function canvasMitPixeln(pixel, blobTyp = null, blobGroesse = 50_000) {
  const gerufen = { toBlobTyp: null, getImageData: 0, drawImage: 0 };
  const ctx = {
    drawImage: () => { gerufen.drawImage++; },
  };
  if (pixel) {
    ctx.getImageData = () => {
      gerufen.getImageData++;
      return { data: Uint8ClampedArray.from(pixel) };
    };
  }
  const cv = {
    width: 0, height: 0,
    getContext: () => ctx,
    toBlob: (cb, typ) => {
      gerufen.toBlobTyp = typ;
      cb(new Blob([new Uint8Array(blobGroesse)], { type: blobTyp ?? typ }));
    },
  };
  return { cv, gerufen };
}

function setzeCanvas(cv) {
  const alterCreate = document.createElement.bind(document);
  vi.spyOn(document, 'createElement').mockImplementation(
    tag => (tag === 'canvas' ? cv : alterCreate(tag)));
}

/* Gross genug, dass wirklich verkleinert wird — sonst misst der Test den
   Ausstieg „schon klein genug" statt den Umwandlungspfad. */
function grossesBild(typ, name, groesse = 4_000_000) {
  return new File([new Uint8Array(groesse)], name, { type: typ });
}

afterEach(() => {
  vi.restoreAllMocks();
  delete globalThis.createImageBitmap;
});

describe('hatTransparentePixel — die Regel an echten Alpha-Werten', () => {
  it('findet ein einzelnes halbtransparentes Pixel', () => {
    /* Drei deckende Pixel, dann eines mit a=128. */
    const daten = [10, 20, 30, 255, 40, 50, 60, 255, 70, 80, 90, 255, 1, 2, 3, 128];
    expect(hatTransparentePixel(daten)).toBe(true);
  });

  it('findet ein vollständig durchsichtiges Pixel (a=0)', () => {
    expect(hatTransparentePixel([255, 255, 255, 255, 0, 0, 0, 0])).toBe(true);
  });

  it('meldet bei durchgehend deckenden Pixeln false', () => {
    const daten = [];
    for (let i = 0; i < 64; i++) daten.push(i, i, i, 255);
    expect(hatTransparentePixel(daten)).toBe(false);
  });

  /* a=254 ist sichtbar deckend, aber JPEG kann es trotzdem nicht abbilden.
     Die Grenze ist „< 255", nicht „nahe 0" — sonst verlöre ein Bild mit
     ganz leichter Transparenz sie weiterhin still. */
  it('zählt auch a=254 als transparent', () => {
    expect(hatTransparentePixel([0, 0, 0, 254])).toBe(true);
  });

  it('kommt mit leeren und ungültigen Eingaben klar', () => {
    expect(hatTransparentePixel([])).toBe(false);
    expect(hatTransparentePixel(null)).toBe(false);
    expect(hatTransparentePixel(undefined)).toBe(false);
    expect(hatTransparentePixel({})).toBe(false);
  });

  it('arbeitet auch auf einem echten Uint8ClampedArray', () => {
    expect(hatTransparentePixel(Uint8ClampedArray.from([1, 2, 3, 200]))).toBe(true);
    expect(hatTransparentePixel(Uint8ClampedArray.from([1, 2, 3, 255]))).toBe(false);
  });
});

describe('verkleinereBild: das Ausgabeformat folgt den Pixeln (Karte #466)', () => {
  /* DER KERNTEST. Ein PNG mit einem durchsichtigen Pixel darf nicht als JPEG
     zurückkommen. Zugesichert wird der Typ, mit dem `toBlob` GERUFEN wird —
     ein Test auf `klein.type` allein wäre schwächer, weil der Mock den Typ
     seines Blobs frei wählen könnte. */
  it('ein PNG mit transparentem Pixel wird NICHT zu JPEG', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const { cv, gerufen } = canvasMitPixeln([255, 255, 255, 255, 0, 0, 0, 0]);
    setzeCanvas(cv);

    const klein = await verkleinereBild(grossesBild('image/png', 'logo.png'));

    expect(gerufen.getImageData, 'die Pixel müssen gelesen worden sein').toBe(1);
    expect(gerufen.toBlobTyp, 'JPEG kann kein Alpha tragen').not.toBe('image/jpeg');
    expect(gerufen.toBlobTyp).toBe('image/webp');
    expect(klein.type).toBe('image/webp');
    expect(klein.name, 'der Name muss zum Inhalt passen').toBe('logo.webp');
  });

  /* Die Gegenrichtung, und der Grund, warum die Regel an den Pixeln hängt und
     nicht am Format: Ein opakes PNG (Screenshot ohne transparenten Rand) hat
     nichts zu erhalten und geht weiter den JPEG-Weg — sonst wäre bei einem
     freigestellten Foto das 7,4-fache an Bytes fällig (gemessen, Karte #466).
     Zusammen mit dem Test darüber ist diese Zusicherung mit einer statischen
     Formatregel NICHT erfüllbar. */
  it('ein opakes PNG geht weiterhin den JPEG-Weg', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const deckend = [];
    for (let i = 0; i < 32; i++) deckend.push(9, 9, 9, 255);
    const { cv, gerufen } = canvasMitPixeln(deckend);
    setzeCanvas(cv);

    const klein = await verkleinereBild(grossesBild('image/png', 'screenshot.png'));

    expect(gerufen.getImageData).toBe(1);
    expect(gerufen.toBlobTyp).toBe('image/jpeg');
    expect(klein.type).toBe('image/jpeg');
    expect(klein.name).toBe('screenshot.jpg');
  });

  /* JPEG kann von vornherein kein Alpha tragen — dort wird gar nicht erst
     gemessen. Der Test hält fest, dass der Vorfilter greift (sonst kostet
     jedes Foto einen unnötigen `getImageData`-Durchlauf über Millionen
     Pixel). */
  it('bei einer JPEG-Quelle wird gar nicht erst gemessen', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const { cv, gerufen } = canvasMitPixeln([0, 0, 0, 0]);
    setzeCanvas(cv);

    const klein = await verkleinereBild(grossesBild('image/jpeg', 'foto.jpg'));

    expect(gerufen.getImageData, 'JPEG kann kein Alpha haben — nichts zu messen').toBe(0);
    expect(gerufen.toBlobTyp).toBe('image/jpeg');
    expect(klein.name).toBe('foto.jpg');
  });

  /* Kann die Umgebung die Pixel nicht lesen, wird Transparenz ANGENOMMEN.
     Der Preis ist im schlechtesten Fall eine grössere Datei; die
     Gegenrichtung wäre ein stiller, nicht umkehrbarer Datenverlust. Genau
     diese Umgebung ist happy-dom (getImageData === null). */
  it('ohne getImageData wird Transparenz angenommen, nicht ausgeschlossen', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const { cv, gerufen } = canvasMitPixeln(null);   // kein getImageData
    setzeCanvas(cv);

    await verkleinereBild(grossesBild('image/png', 'unbekannt.png'));

    expect(gerufen.toBlobTyp).toBe('image/webp');
  });

  it('wirft getImageData, wird ebenfalls Transparenz angenommen', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const { cv } = canvasMitPixeln([0, 0, 0, 255]);
    cv.getContext = () => ({
      drawImage() {},
      getImageData() { throw new Error('SecurityError: tainted canvas'); },
    });
    let bestellt = null;
    cv.toBlob = (cb, typ) => { bestellt = typ; cb(new Blob([new Uint8Array(1000)], { type: typ })); };
    setzeCanvas(cv);

    await verkleinereBild(grossesBild('image/png', 'fremd.png'));

    expect(bestellt).toBe('image/webp');
  });

  /* Ein Browser darf ein nicht unterstütztes Zielformat still durch PNG
     ersetzen (HTML-Spezifikation; ältere Safari-Versionen können WebP nicht
     kodieren). PNG trägt Alpha ebenfalls — das Ergebnis ist also gültig und
     wird übernommen, mit passender Endung. */
  it('ersetzt der Browser WebP still durch PNG, wird das Ergebnis übernommen', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const { cv } = canvasMitPixeln([0, 0, 0, 0], 'image/png');
    setzeCanvas(cv);

    const klein = await verkleinereBild(grossesBild('image/png', 'logo.png'));

    expect(klein.type).toBe('image/png');
    expect(klein.name).toBe('logo.png');
  });

  /* Das Sicherheitsnetz: Kommt für ein alphafähiges Ziel ein JPEG zurück
     (spezifikationswidrig, aber nicht ausschliessbar), ist die Transparenz
     weg. Dann lieber das grosse Original als das kaputte Ergebnis — die
     Zusage „Alpha geht nie verloren" darf nicht davon abhängen, welche
     Formate der Browser kodieren kann. */
  it('liefert der Browser für ein Alpha-Ziel ein JPEG, bleibt das Original erhalten', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const { cv } = canvasMitPixeln([0, 0, 0, 0], 'image/jpeg');
    setzeCanvas(cv);
    const original = grossesBild('image/png', 'logo.png');

    expect(await verkleinereBild(original)).toBe(original);
  });

  /* Die bestehende Regel aus Karte #439 gilt unverändert weiter, auch auf dem
     neuen Pfad: Wird das Ergebnis nicht kleiner, ist die Umwandlung eine
     Verschlechterung. */
  it('behält das Original, wenn das WebP nicht kleiner wird', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 4000, height: 3000, close() {} }));
    const { cv } = canvasMitPixeln([0, 0, 0, 0], null, 200_000);
    setzeCanvas(cv);
    const original = grossesBild('image/png', 'schon-klein.png', 100_000);

    expect(await verkleinereBild(original)).toBe(original);
  });

  /* Der Avatar-Pfad (`maxKante: 512`) läuft durch dieselbe Funktion — die
     Formatfrage betrifft ihn genauso. Ohne diesen Test wäre eine Fassung
     grün, die den Transparenz-Erhalt nur für die Standardkante umsetzt. */
  it('gilt auch für den Avatar-Pfad mit abweichender Zielkante', async () => {
    globalThis.createImageBitmap = vi.fn(async () => ({ width: 2000, height: 2000, close() {} }));
    const { cv, gerufen } = canvasMitPixeln([0, 0, 0, 0]);
    setzeCanvas(cv);

    await verkleinereBild(grossesBild('image/png', 'avatar.png'), { maxKante: 512 });

    expect(cv.width).toBe(512);
    expect(gerufen.toBlobTyp).toBe('image/webp');
  });
});
