/* @vitest-environment happy-dom */

/**
 * F-164 / Karte #386: ObjectURL-Leak und Bild-Vertauschung beim
 * Board-Hintergrund (`applyBoardBackground` in src/renderer.js).
 *
 * ## Was F-238 bereits abdeckte — und was nicht
 *
 * Der Finding-Text von F-164 nennt das KARTEN-Cover. Dort ist der Fall seit
 * F-238 abgesichert: der Callback vergleicht seine Render-Generation, prüft
 * `cardEl.isConnected` und gibt die eben erzeugte blob:-URL frei, wenn er zu
 * spät kommt. Geprüft und bestätigt (Karte #386, erster DoD-Punkt).
 *
 * Ungeschützt blieb derselbe Mechanismus eine Ebene höher: der
 * BOARD-HINTERGRUND. `boardAttachmentObjectUrl` löst ebenfalls asynchron auf,
 * hatte aber keine Generations-Marke. Damit entschied die Reihenfolge der
 * ANTWORTEN, welches Bild am Ende steht — nicht die der Aufrufe.
 *
 * Der Schaden war doppelt: Der überholte Callback sah `_bgObjUrl` mit der
 * frischen URL des Gewinners belegt, hielt sie für die alte und gab sie frei
 * — das gerade angezeigte Bild bricht sichtbar weg — und setzte danach sein
 * eigenes, veraltetes. Seine eigene URL wurde im umgekehrten Fall nie
 * freigegeben; das ist der Leak, den F-164 beschreibt.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// ── Mocks ────────────────────────────────────────────────────────────────

vi.mock('../src/sync.js', () => ({
  Sync: { getDirectoryName: vi.fn(() => null), listKnownBoards: vi.fn(async () => []) },
}))
vi.mock('../src/storage.js', () => ({ Storage: { listKnownFiles: vi.fn(async () => []) } }))
vi.mock('../src/dragdrop.js', () => ({ DragDrop: { takePendingFlip: vi.fn(() => null), init: vi.fn() } }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

/* Steuerbare Auflösung: jeder Aufruf liefert ein Promise, das der Test
   selbst auflöst — nur so lässt sich die umgekehrte Reihenfolge erzwingen. */
const offeneAufrufe = []
vi.mock('../src/attachments.js', () => ({
  attachmentUrl:      vi.fn(async () => null),
  boardAttachmentObjectUrl: vi.fn(() => new Promise(res => { offeneAufrufe.push(res) })),
  computeBoardBytes:  vi.fn(() => 0),
  humanSize:          vi.fn(() => '–'),
  /* F-1107: `src/renderer.js` prueft SVG bei Board-Hintergruenden seit der
     Nacharbeit ueber istSvgMime() statt eines exakten String-Vergleichs.
     F-1114d (Verify-Lauf 2, V2-6): Platzhalter hier — die ECHTE
     Implementierung wird unten per `mockImplementation()` nachgereicht,
     Muster aus tests/attachments-komfort.test.js. */
  istSvgMime:         vi.fn(),
}))

/* Seit Karte #450 holt der Renderer sein Attachment-Backend über das
   Storage-Plugin statt direkt aus `attachments.js` (F-798). Ohne diesen Mock
   hinge der Test am `boardMode`: Im server-Kontext liefert `getActivePlugin()`
   das REST-Backend mit eigenen Implementierungen, der `attachments.js`-Mock
   oben griffe dort nicht — isoliert grün, in der vollen Kette rot.
   Begründung ausführlich im Helfer. */
vi.mock('../src/storage-plugin.js', () =>
  import('./helpers/attachment-backend-mock.js').then(m => m.storagePluginMock()))

import { Filter }   from '../src/filter.js'
import { State }    from '../src/state.js'
import { Renderer } from '../src/renderer.js'
import * as Attachments from '../src/attachments.js'

/* F-1114d: die ECHTE `istSvgMime` nachreichen (Begründung siehe Mock-Factory
   oben, Muster aus tests/attachments-komfort.test.js). */
const { istSvgMime: echtesIstSvgMime } = await vi.importActual('../src/attachments.js')
Attachments.istSvgMime.mockImplementation(echtesIstSvgMime)

// ── Hilfsmittel ──────────────────────────────────────────────────────────

/* happy-dom kennt createObjectURL/revokeObjectURL nicht zuverlässig —
   defensiv sicherstellen (Muster aus tests/cover.test.js). */
if (typeof URL.revokeObjectURL !== 'function') URL.revokeObjectURL = () => {}
if (typeof URL.createObjectURL !== 'function') URL.createObjectURL = () => 'blob:stub'

/* Kein Netz aus Tests (Sprint-54-Nacharbeit): Im Server-Frontend-Kontext
   (`IS_SERVER_BUILD=true`) holen Module beim Rendern server-seitige Nebendaten
   per `fetch` mit relativer URL. `serverUrl` ist leer, und happy-dom löst
   relative URLs gegen seinen Default-Ursprung `localhost:3000` auf — diese
   Datei allein erzeugte dadurch 50 echte Verbindungsversuche. Läuft auf der
   Maschine etwas auf Port 3000, gingen echte Requests dorthin. */
const _origFetch = globalThis.fetch

let freigegeben = []

function bild(id) {
  return { id, filename: `${id}.png`, mime: 'image/png', size: 10, created: '2026-08-18' }
}

function boardMit(attId, attachments) {
  return {
    id: 'board-bg', title: 'BG-Test',
    columnDefs: [{ id: 'col-a', title: 'A' }],
    sprints: [{ id: 's1', title: 'S', cards: [] }],
    attachments,
    background: attId ? { attachmentId: attId } : null,
  }
}

function domVorbereiten() {
  const app = document.createElement('div'); app.id = 'app'
  const header = document.createElement('div'); header.id = 'board-header'
  const cols = document.createElement('div'); cols.id = 'board-columns'
  app.appendChild(header); app.appendChild(cols)
  document.body.appendChild(app)
  return app
}

/* Microtasks abarbeiten lassen, damit die .then-Ketten durchlaufen. */
const abwarten = () => new Promise(res => setTimeout(res, 0))

beforeEach(() => {
  globalThis.fetch = vi.fn(async () => new Response('{}', {
    status: 200, headers: { 'Content-Type': 'application/json' },
  }))
  document.body.innerHTML = ''
  offeneAufrufe.length = 0
  freigegeben = []
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(u => { freigegeben.push(u) })
  Filter.clear()
  State.markClean()
})

afterEach(() => {
  /* F-923/Karte #489: GELECKTER ZUSTAND — `_bgObjUrl`, `_bgAttId` und
     `_bgGeneration` in `applyBoardBackground` (src/renderer.js) sind
     Modul-Zustand des Renderers und überleben zwischen den Tests dieser
     Datei (kein Reset durch happy-dom, kein Export zum Zurücksetzen von
     aussen). Bisher haben nur zwei der sechs Tests das über eine eigene
     "Leer-Render"-Präambel an ihrem eigenen Anfang abgefangen (Kommentare
     bei den Tests "die überholte URL wird freigegeben" und "data:-URLs
     werden nicht zur Freigabe angemeldet") — die anderen vier verliessen
     sich stillschweigend auf die Datei-Reihenfolge: ihr Vorgänger im
     Datei-Layout hinterliess zufällig einen kompatiblen Zustand. Unter
     `--sequence.shuffle` stimmt diese Annahme nicht mehr — der Kurzschluss
     in `applyBoardBackground` (`if (att && _bgAttId === att.id && _bgObjUrl)
     return;`) überspringt dann die erwartete Anfrage komplett, oder eine
     vom Vortest übrig gebliebene `blob:`-URL wird beim nächsten Render
     legitim (aber vom Test ungeplant) freigegeben.
     Reset: derselbe "Leer-Render"-Weg, den die zwei Tests oben schon
     benutzen, jetzt zentral nach JEDEM Test statt an einzelnen Stellen
     unvollständig dupliziert. Synchron, weil ein Hintergrund ohne
     Attachment-Id sofort (ohne await auf `boardAttachmentObjectUrl`) in den
     Abschalt-Zweig läuft. */
  Renderer.renderBoard({ board: boardMit(null, []) })
  globalThis.fetch = _origFetch
  document.body.innerHTML = ''
  Filter.clear()
  vi.restoreAllMocks()
  vi.clearAllMocks()
})

describe('Board-Hintergrund — Antworten in umgekehrter Reihenfolge (F-164, #386)', () => {
  it('der zuletzt ANGEFORDERTE Hintergrund gewinnt, nicht der zuletzt geantwortete', async () => {
    const app = domVorbereiten()
    const a = bild('att-a'), b = bild('att-b')

    Renderer.renderBoard({ board: boardMit('att-a', [a, b]) })   // Aufruf 1
    Renderer.renderBoard({ board: boardMit('att-b', [a, b]) })   // Aufruf 2
    expect(offeneAufrufe).toHaveLength(2)

    /* Der ZWEITE antwortet zuerst, der erste überholt ihn danach. */
    offeneAufrufe[1]('blob:B')
    await abwarten()
    offeneAufrufe[0]('blob:A')
    await abwarten()

    expect(app.style.getPropertyValue('--board-bg-image')).toBe('url(blob:B)')
    expect(app.classList.contains('board--has-bg')).toBe(true)
  })

  it('die überholte URL wird freigegeben — und die angezeigte NICHT', async () => {
    /* Beide Hälften zählen: ohne Freigabe von blob:A ist es der Leak aus
       F-164; mit Freigabe von blob:B bricht das sichtbare Bild weg. */
    const app = domVorbereiten()
    const a = bild('att-a'), b = bild('att-b')

    /* Ausgangszustand herstellen — dasselbe Muster wie im data:-Test weiter
       unten: `_bgObjUrl` und (seit Karte #439) `_bgAttId` sind Modul-Zustand
       des Renderers und überleben zwischen den Tests dieser Datei. Der
       Vortest hinterlässt 'att-b' als angezeigtes Bild; ohne diesen Reset
       greift der Kurzschluss gegen erneutes Decodieren und der zweite
       Render erzeugt gar keine Anfrage. Das ist kein Fehler des
       Kurzschlusses, sondern eine Annahme dieses Tests, die vorher nur
       zufällig stimmte. */
    Renderer.renderBoard({ board: boardMit(null, [a, b]) })
    await abwarten()
    freigegeben = []
    offeneAufrufe.length = 0

    Renderer.renderBoard({ board: boardMit('att-a', [a, b]) })
    Renderer.renderBoard({ board: boardMit('att-b', [a, b]) })
    offeneAufrufe[1]('blob:B')
    await abwarten()
    offeneAufrufe[0]('blob:A')
    await abwarten()

    expect(freigegeben).toContain('blob:A')
    expect(freigegeben).not.toContain('blob:B')
    expect(app.style.getPropertyValue('--board-bg-image')).toBe('url(blob:B)')
  })

  it('Hintergrund entfernt, während eine Anfrage läuft: das Bild kommt nicht zurück', async () => {
    /* Der Abschalt-Zweig steigt früh aus und rührt laufende Anfragen nicht
       an. Ohne Generations-Marke setzte der späte Callback den Hintergrund
       danach wieder — sichtbar für den Nutzer als "geht nicht weg". */
    const app = domVorbereiten()
    const a = bild('att-a')

    Renderer.renderBoard({ board: boardMit('att-a', [a]) })
    Renderer.renderBoard({ board: boardMit(null, [a]) })         // Hintergrund weg
    offeneAufrufe[0]('blob:A')
    await abwarten()

    expect(app.classList.contains('board--has-bg')).toBe(false)
    expect(app.style.getPropertyValue('--board-bg-image')).toBe('')
    expect(freigegeben).toContain('blob:A')                       // kein Leak
  })

  it('Normalfall ohne Wettlauf: Bild wird gesetzt, nichts freigegeben', async () => {
    const app = domVorbereiten()
    const a = bild('att-a')

    Renderer.renderBoard({ board: boardMit('att-a', [a]) })
    offeneAufrufe[0]('blob:A')
    await abwarten()

    expect(app.style.getPropertyValue('--board-bg-image')).toBe('url(blob:A)')
    expect(app.classList.contains('board--has-bg')).toBe(true)
    expect(freigegeben).toEqual([])
  })

  /* Karte #439: Der Kurzschluss gegen erneutes Decodieren darf den
     F-164-Schutz nicht aushebeln. Die erste Fassung kehrte VOR der
     Generationsmarke zurück — dieser Test hält fest, warum das falsch war. */
  it('ein Kurzschluss auf dasselbe Bild entwertet trotzdem laufende Anfragen', async () => {
    const app = domVorbereiten()
    const a = bild('att-a'), b = bild('att-b')

    /* B steht sichtbar. */
    Renderer.renderBoard({ board: boardMit('att-b', [a, b]) })
    await abwarten()
    offeneAufrufe[0]('blob:B')
    await abwarten()
    expect(app.style.getPropertyValue('--board-bg-image')).toBe('url(blob:B)')
    offeneAufrufe.length = 0
    freigegeben = []

    /* Jemand wählt A — die Anfrage läuft. */
    Renderer.renderBoard({ board: boardMit('att-a', [a, b]) })
    expect(offeneAufrufe).toHaveLength(1)

    /* … und wählt vor der Antwort wieder B. Hier greift der Kurzschluss:
       keine neue Anfrage, weil B ja schon steht. */
    Renderer.renderBoard({ board: boardMit('att-b', [a, b]) })
    expect(offeneAufrufe).toHaveLength(1)

    /* Jetzt antwortet A. Es MUSS verworfen werden — sonst überschreibt eine
       Anfrage, die der Nutzer längst widerrufen hat, das sichtbare Bild. */
    offeneAufrufe[0]('blob:A')
    await abwarten()

    expect(app.style.getPropertyValue('--board-bg-image'),
      'die widerrufene Anfrage hat das angezeigte Bild überschrieben').toBe('url(blob:B)')
    expect(freigegeben, 'die verworfene URL wurde nicht freigegeben').toContain('blob:A')
  })

  it('data:-URLs werden nicht zur Freigabe angemeldet', async () => {
    /* Nur blob: braucht revokeObjectURL. Ein data:-Hintergrund würde sonst
       in die Freigabeliste wandern und dort ewig stehen bleiben.

       ACHTUNG, beim ersten Anlauf hereingefallen: `_bgObjUrl` ist
       Modul-Zustand des Renderers und überlebt zwischen den Tests dieser
       Datei. Ohne den Leer-Render unten schleppt dieser Test die blob:-URL
       des Nachbartests mit und misst dessen Aufräumen statt sein eigenes.
       Der Leer-Render setzt den Modul-Zustand zurück; erst danach beginnt
       die eigentliche Messung. */
    const app = domVorbereiten()
    const a = bild('att-a')

    Renderer.renderBoard({ board: boardMit(null, [a]) })
    await abwarten()
    freigegeben = []
    offeneAufrufe.length = 0

    Renderer.renderBoard({ board: boardMit('att-a', [a]) })
    Renderer.renderBoard({ board: boardMit('att-a', [a]) })
    offeneAufrufe[1]('data:image/png;base64,xxx')
    await abwarten()
    offeneAufrufe[0]('data:image/png;base64,yyy')
    await abwarten()

    expect(freigegeben).toEqual([])
    expect(app.style.getPropertyValue('--board-bg-image')).toBe('url(data:image/png;base64,xxx)')
  })
})
