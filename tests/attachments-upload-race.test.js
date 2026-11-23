/* @vitest-environment happy-dom */

/**
 * F-130 (Karte #376) — Upload-Limit-Race.
 *
 * `triggerUpload` in `src/modal.js` liest die Board-Größe (`checkUpload`) und
 * schreibt sie (`uploadFile`) — dazwischen liegt ein `await`. Der
 * Datei-Dialog feuerte bisher `files.forEach(f => triggerUpload(f))` OHNE
 * `await`: alle Uploads prüften damit gegen denselben Schnappschuss und kamen
 * gemeinsam durch, obwohl ihre Summe das Hartlimit riss.
 *
 * Der Test fährt den ECHTEN DOM-Pfad (Modal öffnen → Datei-Input →
 * `change`-Event), nicht eine nachgebaute Orchestrierung. Gemockt ist nur das
 * Attachments-Modul, und zwar als Größen-Buchhaltung: `checkUpload` lehnt ab,
 * sobald die bereits belegten Bytes plus die neue Datei das Limit reissen,
 * `uploadFile` schreibt die Bytes erst NACH einem Tick fort — exakt das
 * Zeitfenster, um das es geht.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

const LIMIT = 100

/* Board-Zustand, den die Mocks lesen und schreiben. Kein echtes State-Modul —
   die Größenbuchhaltung ist alles, was dieser Test braucht. */
let belegteBytes = 0
let uploadsDurch = []
/* Karte #450: protokolliert die Schrittfolge je Upload (verkleinern → pruefen
   → schreiben), damit die Reihenfolge pruefbar ist und nicht nur das Ergebnis. */
let ablauf = []

/* Die Standard-Implementierung von `uploadFile` als benannte Funktion, damit
   der beforeEach sie wiederherstellen kann (Karte #450).
   WARUM: Zwei Tests weiter unten ersetzen `uploadFile` per
   `mockImplementation` OHNE `Once` — das gilt dauerhaft und damit auch fuer
   alle FOLGENDEN Tests. Deren Ersatz-Implementierungen kennen `ablauf` nicht,
   also wurde weitergeschrieben, aber nicht mehr protokolliert: `belegteBytes`
   stieg, die Schrittfolge blieb unvollstaendig. Ein spaeter hinzugefuegter
   Test sieht dann einen Zustand, den sein eigener Code nicht erklaert.
   Funktionsdeklaration statt const, weil die `vi.mock`-Factory gehoistet wird
   und die Referenz sonst zur Auswertungszeit noch nicht existiert. */
async function standardUpload(file) {
  await Promise.resolve();
  await Promise.resolve();
  belegteBytes += file.size;
  uploadsDurch.push(file.name);
  ablauf.push(`schreiben:${file.name}:${file.size}`);
  return { id: `att-${uploadsDurch.length}`, filename: file.name, mime: 'text/plain', size: file.size };
}

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))

vi.mock('../src/config.js', () => ({
  Config: {
    get: vi.fn((key) => (key === 'boardMode' ? 'standalone' : key === 'userName' ? 'TestUser' : '')),
    set: vi.fn(),
  },
}))

/* Karte #450: Der Upload-Pfad verkleinert nur, wenn die Datei im Board-State
   landet — im `claude-code`-Profil (FSA-Volume) waere es unnoetiger, nicht
   umkehrbarer Verlust. `fsaVolume` steuert diesen Zweig je Test. */
let fsaVolume = false
vi.mock('../src/profile.js', () => ({
  Profile: {
    has: (cap) => (cap === 'feature.fsaVolumeAttachments' ? fsaVolume : false),
    getActive: () => ({ id: fsaVolume ? 'claude-code' : 'solo', capabilities: new Set() }),
    PROFILES: {},
  },
}))

vi.mock('../src/state.js', () => ({
  State: {
    get: vi.fn(() => ({ version: '2', board: { id: 'b', title: 'T', sprints: [], attachments: [] } })),
    set: vi.fn(),
  },
}))

vi.mock('../src/attachments.js', () => ({
  /* Karte #450: Bilder werden VOR der Pruefung verkleinert. Der Mock bildet
     das als Groessen-Halbierung fuer image/* ab und protokolliert, in welcher
     Reihenfolge die Schritte laufen — genau darum geht es beim Test unten. */
  verkleinereBild: vi.fn(async (file) => {
    ablauf.push(`verkleinern:${file.name}:${file.size}`);
    if (!String(file.type || '').startsWith('image/')) return file;
    return { ...file, size: Math.round(file.size / 2), name: file.name.replace(/\.[^.]*$/, '') + '.jpg', type: 'image/jpeg' };
  }),
  checkUpload: vi.fn((file) => {
    ablauf.push(`pruefen:${file.name}:${file.size}`);
    return belegteBytes + file.size > LIMIT ? { ok: false, reason: 'zu gross' } : { ok: true };
  }),
  /* Der Schreibvorgang ist async und schreibt die Bytes erst danach fort —
     genau hier lag das Fenster, in dem ein zweiter Upload noch den alten
     Stand las. */
  uploadFile: vi.fn(standardUpload),
  computeBoardBytes: vi.fn(() => belegteBytes),
  humanSize:         vi.fn(b => `${b} B`),
  attachmentUrl:     vi.fn(async () => null),
  readAttachment:    vi.fn(async () => null),
  deleteAttachment:  vi.fn(async () => {}),
  downloadAttachment: vi.fn(async () => {}),
  safeMime:          vi.fn(m => m),
  boardSizeLabel:    vi.fn(() => ''),
  uploadBoardFile:   vi.fn(async () => null),
  boardAttachmentUrl: vi.fn(async () => null),
  deleteBoardAttachment: vi.fn(async () => {}),
  purgeCardAttachments: vi.fn(async () => {}),
  /* F-1088: renderAttachmentsList() rendert bei JEDEM Modal-Open (ADR-0033) —
     ohne diesen Export stirbt jedes Oeffnen einer Karte mit Anhang. */
  normalisiereAnhang: vi.fn((a) => (typeof a === 'string' ? { id: a } : a)),
  /* F-1107: `src/modal.js` prueft SVG seit der Nacharbeit ueber istSvgMime()
     statt eines exakten String-Vergleichs — ohne diesen Export stirbt jede
     Bild-Vorschau hier (dieser Test laedt ausschliesslich image/*-Dateien).
     F-1114d (Verify-Lauf 2, V2-6): Platzhalter hier — die ECHTE
     Implementierung wird unten per `mockImplementation()` nachgereicht,
     Muster aus tests/attachments-komfort.test.js. */
  istSvgMime: vi.fn(),
}))

import { EventBus } from '../src/eventbus.js'
import { Modal }    from '../src/modal.js'
import * as Attachments from '../src/attachments.js'

/* F-1114d: die ECHTE `istSvgMime` nachreichen (Begründung siehe Mock-Factory
   oben, Muster aus tests/attachments-komfort.test.js). */
const { istSvgMime: echtesIstSvgMime } = await vi.importActual('../src/attachments.js')
Attachments.istSvgMime.mockImplementation(echtesIstSvgMime)

function makeCard(id = 'card-1') {
  return {
    id, displayId: 1, title: 'Testkarte', description: '', agentContext: '',
    comments: [], attachments: [], links: [], activity: [],
    priority: 'mittel', labels: [], owner: '', complexity: '', effort: null,
    qa: false, critical: false, blocked: false, archived: false, columnId: 'col-backlog',
  }
}

function datei(name, size) {
  return { name, size, type: 'text/plain' }
}

function resetModal() {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
}

beforeEach(async () => {
  /* ZUERST die Kette austrocknen, DANN zuruecksetzen (Karte #450).
     `uploadQueue` ist Modul-Zustand von `modal.js` und ueberlebt jeden Test —
     ein Upload, der beim Testende noch in der Warteschlange stand, laeuft im
     NAECHSTEN Test weiter und schreibt dort `belegteBytes` hoch, nachdem der
     Reset schon durch war. Symptom: Tests, die isoliert gruen sind und in der
     Suite rot, mit einer Ablehnung, fuer die es im eigenen Test keinen Grund
     gibt. Aufgefallen beim Ergaenzen der beiden Verkleinerungs-Tests. */
  for (let i = 0; i < 200; i++) await Promise.resolve()
  belegteBytes = 0
  uploadsDurch = []
  ablauf = []
  fsaVolume = false
  /* Dauerhafte mockImplementation aus einem Vorgaenger-Test zuruecknehmen. */
  const { uploadFile } = await import('../src/attachments.js')
  uploadFile.mockImplementation(standardUpload)
  resetModal()
  /* alert() würde in happy-dom werfen bzw. den Lauf anhalten */
  globalThis.alert = vi.fn()
})

afterEach(() => { resetModal() })

/* Die drei Dateien laufen über den Datei-Input auf einmal ein — der Pfad, den
   `files.forEach(...)` ohne await bediente. */
async function ladeUeberDateiDialog(dateien) {
  EventBus.emit('card:edit', makeCard())
  const input = document.querySelector('#attachments-upload')
  expect(input).not.toBeNull()
  Object.defineProperty(input, 'files', { value: dateien, configurable: true })
  input.dispatchEvent(new Event('change'))
  /* Auf die Upload-Kette warten: mehrere Microtask-Runden pro Datei plus
     Reserve. Kein Timer im Spiel — alle Awaits sind Promise-basiert.

     Die Rundenzahl haengt an der Dateizahl UND traegt eine grosszuegige
     Reserve, weil `uploadQueue` Modul-Zustand von `modal.js` ist und jeden
     Test ueberlebt (derselbe Umstand, den der Test „Modal-Wechsel mitten in
     der Warteschlange" ausnutzt). Ein Nachlaeufer aus dem vorherigen Test
     verschiebt damit den Start des naechsten. Mit der festen 40 fielen die
     beiden Karte-#450-Tests aus genau diesem Grund um — isoliert gruen, in
     der Suite rot, was wie ein Fehler im Produktivcode aussieht und keiner
     war. Wer hier kuerzt, baut diese Falle wieder ein. */
  const runden = 60 + 40 * dateien.length
  for (let i = 0; i < runden; i++) await Promise.resolve()
}

describe('F-130 — Upload-Limit bei gleichzeitigen Uploads', () => {
  it('drei Dateien über den Datei-Dialog: das Hartlimit hält', async () => {
    /* 3 × 40 B = 120 B gegen ein Limit von 100 B. Korrekt sind zwei
       Uploads (80 B); der dritte muss abgelehnt werden. Vor dem Fix kamen
       alle drei durch, weil alle drei `belegteBytes === 0` sahen. */
    await ladeUeberDateiDialog([datei('a.txt', 40), datei('b.txt', 40), datei('c.txt', 40)])

    expect(uploadsDurch).toEqual(['a.txt', 'b.txt'])
    expect(belegteBytes).toBe(80)
    expect(belegteBytes).toBeLessThanOrEqual(LIMIT)
  })

  it('Uploads laufen nacheinander, nicht verschachtelt', async () => {
    /* Zusätzlich zur Summe: die Reihenfolge. Zwei Läufe dürfen sich nicht
       überlappen, sonst ist der Check des zweiten wieder wertlos. */
    const { uploadFile } = await import('../src/attachments.js')
    let laufend = 0
    let maxGleichzeitig = 0
    uploadFile.mockImplementation(async (file) => {
      laufend++
      maxGleichzeitig = Math.max(maxGleichzeitig, laufend)
      await Promise.resolve()
      await Promise.resolve()
      belegteBytes += file.size
      uploadsDurch.push(file.name)
      laufend--
      return { id: 'att', filename: file.name, mime: 'text/plain', size: file.size }
    })

    await ladeUeberDateiDialog([datei('a.txt', 10), datei('b.txt', 10), datei('c.txt', 10)])

    expect(maxGleichzeitig).toBe(1)
    expect(uploadsDurch).toEqual(['a.txt', 'b.txt', 'c.txt'])
  })

  it('unterhalb des Limits kommen alle durch (kein Fehlalarm)', async () => {
    await ladeUeberDateiDialog([datei('a.txt', 10), datei('b.txt', 10), datei('c.txt', 10)])
    expect(uploadsDurch).toEqual(['a.txt', 'b.txt', 'c.txt'])
    expect(belegteBytes).toBe(30)
  })

  it('Modal-Wechsel mitten in der Warteschlange: Dateien bleiben an IHRER Karte', async () => {
    /* Sprint-52-Kritiker (K1), Regression der ersten Fassung dieses Fixes:
       `uploadQueue` überlebt als Modul-Zustand jedes Modal-Öffnen. Wurde
       `currentCard` erst zur Ausführungszeit gelesen, landeten die noch
       wartenden Dateien an der Karte, die zufällig gerade offen war. */
    const { uploadFile } = await import('../src/attachments.js')
    let freigabe
    const tor = new Promise(r => { freigabe = r })
    let ersterLauf = true
    uploadFile.mockImplementation(async (file) => {
      if (ersterLauf) { ersterLauf = false; await tor }
      belegteBytes += file.size
      uploadsDurch.push(file.name)
      return { id: `att-${uploadsDurch.length}`, filename: file.name, mime: 'text/plain', size: file.size }
    })

    const ereignisse = []
    const horcher = p => ereignisse.push(p.cardId)
    EventBus.on('card:attachment-add', horcher)

    /* Karte A öffnen, drei Dateien einreihen — der erste Upload hängt. */
    EventBus.emit('card:edit', makeCard('karte-A'))
    const input = document.querySelector('#attachments-upload')
    Object.defineProperty(input, 'files', {
      value: [datei('a.txt', 10), datei('b.txt', 10), datei('c.txt', 10)], configurable: true,
    })
    input.dispatchEvent(new Event('change'))
    await Promise.resolve()

    /* Modal schliessen und Karte B öffnen, WÄHREND die Kette noch wartet. */
    EventBus.emit('config:language-changed')
    EventBus.emit('card:edit', makeCard('karte-B'))

    freigabe()
    for (let i = 0; i < 60; i++) await Promise.resolve()

    EventBus.off('card:attachment-add', horcher)
    expect(uploadsDurch).toEqual(['a.txt', 'b.txt', 'c.txt'])
    /* Alle drei gehören an Karte A — keine an B, und keine verschwindet. */
    expect(ereignisse).toEqual(['karte-A', 'karte-A', 'karte-A'])
  })

  it('Karte #450: ein Bild wird VOR der Limit-Pruefung verkleinert', async () => {
    /* Die Reihenfolge ist der Punkt, nicht das Verkleinern an sich. Liefe die
       Pruefung zuerst, wuerde ein 80-Byte-Bild am Limit von 100 scheitern,
       obwohl es verkleinert nur 40 Byte gross ankommt — der Vorcheck lehnte
       dann etwas ab, das in dieser Groesse nie gespeichert wird.
       Aufbau: 50 belegt + 80 roh = 130 > 100 (waere abgelehnt),
               50 belegt + 40 verkleinert = 90 <= 100 (kommt durch). */
    belegteBytes = 50

    await ladeUeberDateiDialog([{ name: 'foto.png', size: 80, type: 'image/png' }])

    expect(ablauf).toEqual([
      'verkleinern:foto.png:80',
      'pruefen:foto.jpg:40',
      'schreiben:foto.jpg:40',
    ])
    expect(uploadsDurch).toEqual(['foto.jpg'])
  })

  it('Karte #450: im FSA-Volume-Profil wird NICHT verkleinert', async () => {
    /* `claude-code` legt Anhaenge als Dateien neben dem Board ab — dort gibt es
       weder PUT-Body-Grenze noch Quota. Eine Verkleinerung waere reiner,
       nicht umkehrbarer Verlust (JPEG-Konvertierung, PNG verliert Alpha). */
    fsaVolume = true

    await ladeUeberDateiDialog([{ name: 'beleg.png', size: 80, type: 'image/png' }])

    expect(ablauf).toEqual([
      'pruefen:beleg.png:80',
      'schreiben:beleg.png:80',
    ])
    expect(uploadsDurch).toEqual(['beleg.png'])
  })

  it('Karte #450: Nicht-Bilder gehen unveraendert durch die Verkleinerung', async () => {
    /* `verkleinereBild` ist bewusst tolerant — sie gibt alles zurueck, was sie
       nicht sicher rastern kann. Der Aufruf darf deshalb bedingungslos
       erfolgen, ohne dass der Upload-Pfad Dateitypen kennen muss. */
    await ladeUeberDateiDialog([datei('bericht.txt', 10)])

    expect(ablauf).toEqual([
      'verkleinern:bericht.txt:10',
      'pruefen:bericht.txt:10',
      'schreiben:bericht.txt:10',
    ])
  })

  it('ein fehlgeschlagener Upload blockiert die folgenden nicht', async () => {
    /* Die Kette darf nie im rejected-Zustand hängen bleiben — sonst wäre der
       Guard ein Gate ohne Rückweg (knowledge §6): ein einziger Fehler
       legte alle weiteren Uploads still bis zum Modal-Neuaufbau. */
    const { uploadFile } = await import('../src/attachments.js')
    uploadFile.mockImplementationOnce(async () => { throw new Error('Schreibfehler') })

    await ladeUeberDateiDialog([datei('kaputt.txt', 10), datei('gut.txt', 10)])

    expect(uploadsDurch).toEqual(['gut.txt'])
  })
})
