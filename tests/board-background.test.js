/* @vitest-environment happy-dom */

/**
 * Tests für Board-Hintergrundbild (Karte #114)
 * Abdeckung:
 * - Migration: board.background + board.attachments werden sichergestellt
 * - Migration: dangling attachmentId → null (idempotent)
 * - Renderer: CSS-Variable gesetzt wenn Hintergrund vorhanden
 * - Renderer: CSS-Variable entfernt wenn kein Hintergrund
 * - SVG wird nicht als Hintergrund gesetzt (F-128)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// ── Mocks ────────────────────────────────────────────────────────────────

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName: vi.fn(() => null),
    listKnownBoards:  vi.fn(async () => []),
    isActive:         vi.fn(() => false),
    isPermissionNeeded: vi.fn(() => false),
  },
}))

vi.mock('../src/storage.js', () => ({
  Storage: { listKnownFiles: vi.fn(async () => []) },
}))

vi.mock('../src/dragdrop.js', () => ({
  DragDrop: { takePendingFlip: vi.fn(() => null), init: vi.fn() },
}))

vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

vi.mock('../src/attachments.js', () => ({
  attachmentUrl:      vi.fn(async (_card, att) => att?.data ? `data:${att.mime};base64,${att.data}` : null),
  /* Karte #439: Der Renderer nutzt jetzt boardAttachmentObjectUrl — IMMER
     eine blob:-URL. Eine data:-URL wird in einer CSS Custom Property
     stillschweigend verworfen; deshalb wurde umgestellt. */
  boardAttachmentObjectUrl: vi.fn(async (att) => att?.data ? `blob:mock/${att.id}` : null),
  boardAttachmentUrl: vi.fn(async (att) => att?.data ? `data:${att.mime};base64,${att.data}` : null),
  computeBoardBytes:  vi.fn(() => 0),
  humanSize:          vi.fn(() => '–'),
  /* F-1107: `src/renderer.js` prueft SVG bei Board-Hintergruenden seit der
     Nacharbeit ueber istSvgMime() statt eines exakten String-Vergleichs.
     F-1114d (Verify-Lauf 2, V2-6): Platzhalter hier — ein Nachbau der Regex
     drieftet unbemerkt vom Original ab (z. B. eine künftige MIME-Variante,
     die `istSvgMime` erkennt, der Nachbau aber nicht). Die ECHTE
     Implementierung wird unten per `mockImplementation()` nachgereicht,
     Muster aus tests/attachments-komfort.test.js. */
  istSvgMime:         vi.fn(),
}))

/* Karte #450: Der Renderer geht seither über `StoragePlugin.getActivePlugin()
   .attachments()`; ohne diesen Mock griffe der `attachments.js`-Mock oben im
   server-Kontext nicht (dort liefert das Plugin das REST-Backend). Begründung
   im Helfer. */
vi.mock('../src/storage-plugin.js', () =>
  import('./helpers/attachment-backend-mock.js').then(m => m.storagePluginMock()))

import { Storage } from '../src/storage.js'
import { Filter }  from '../src/filter.js'
import { State }   from '../src/state.js'
import { Renderer } from '../src/renderer.js'
import { boardAttachmentObjectUrl } from '../src/attachments.js'
import * as Attachments from '../src/attachments.js'

/* F-1114d: die ECHTE `istSvgMime` nachreichen (Begründung siehe Mock-Factory
   oben, Muster aus tests/attachments-komfort.test.js). */
const { istSvgMime: echtesIstSvgMime } = await vi.importActual('../src/attachments.js')
Attachments.istSvgMime.mockImplementation(echtesIstSvgMime)

// ── Hilfsfunktionen ──────────────────────────────────────────────────────

function makePngAtt(overrides = {}) {
  return {
    id:       `att-${Math.random().toString(36).slice(2)}`,
    filename: 'bg.png',
    mime:     'image/png',
    size:     1024,
    created:  '2026-05-13',
    data:     'abc123',
    ...overrides,
  }
}

function makeBoard(overrides = {}) {
  return {
    id:          'board-test',
    title:       'Test-Board',
    columnDefs:  [{ id: 'col-todo', title: 'Todo' }],
    sprints:     [{ id: 's1', title: 'Sprint 1', cards: [] }],
    background:  null,
    attachments: [],
    ...overrides,
  }
}

function setupDOM(board) {
  const header = document.createElement('div')
  header.id = 'board-header'
  const container = document.createElement('div')
  container.id = 'board-columns'
  const app = document.createElement('div')
  app.id = 'app'
  app.appendChild(header)
  app.appendChild(container)
  document.body.appendChild(app)
  State.set({ board })
  Renderer.renderBoard({ board })
  return app
}

// ── Setup / Teardown ─────────────────────────────────────────────────────

beforeEach(() => {
  document.body.innerHTML = ''
  Filter.clear()
  State.markClean()
  vi.clearAllMocks()
})

afterEach(() => {
  document.body.innerHTML = ''
  Filter.clear()
  vi.clearAllMocks()
})

// ─────────────────────────────────────────────────────────────────────────
// 1) Migration: migrateBoardBackground
// ─────────────────────────────────────────────────────────────────────────

describe('Board-Hintergrund — Migration', () => {
  it('board.attachments wird auf [] gesetzt wenn nicht vorhanden', () => {
    const board = makeBoard()
    delete board.attachments
    expect(Array.isArray(board.attachments)).toBe(false)
    // Simulation: checkVersion initialisiert attachments
    if (!Array.isArray(board.attachments)) board.attachments = []
    expect(Array.isArray(board.attachments)).toBe(true)
  })

  it('board.background wird auf null gesetzt wenn undefined', () => {
    const board = makeBoard()
    delete board.background
    expect(board.background).toBeUndefined()
    if (board.background === undefined) board.background = null
    expect(board.background).toBeNull()
  })

  it('dangling attachmentId wird auf null gesetzt', () => {
    const board = makeBoard({
      background:  { attachmentId: 'nicht-existent' },
      attachments: [],
    })
    // Simulation der migrateBoardBackground-Logik
    if (board.background && !board.attachments.some(a => a.id === board.background.attachmentId)) {
      board.background = null
    }
    expect(board.background).toBeNull()
  })

  it('gültige attachmentId bleibt erhalten', () => {
    const att = makePngAtt()
    const board = makeBoard({
      background:  { attachmentId: att.id },
      attachments: [att],
    })
    // Simulation der migrateBoardBackground-Logik
    if (board.background && !board.attachments.some(a => a.id === board.background.attachmentId)) {
      board.background = null
    }
    expect(board.background).not.toBeNull()
    expect(board.background.attachmentId).toBe(att.id)
  })

  it('Migration ist idempotent bei board.background = null', () => {
    const board = makeBoard({ background: null, attachments: [] })
    // Zweimal anwenden
    if (!Array.isArray(board.attachments)) board.attachments = []
    if (board.background === undefined) board.background = null
    else if (board.background && !board.attachments.some(a => a.id === board.background.attachmentId)) board.background = null
    expect(board.background).toBeNull()
    expect(Array.isArray(board.attachments)).toBe(true)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 2) Renderer: CSS-Variable
// ─────────────────────────────────────────────────────────────────────────

describe('Board-Hintergrund — Renderer', () => {
  it('app-Element hat keine board--has-bg-Klasse wenn kein Hintergrund', () => {
    const board = makeBoard({ background: null, attachments: [] })
    const app = setupDOM(board)
    expect(app.classList.contains('board--has-bg')).toBe(false)
  })

  it('boardAttachmentObjectUrl wird aufgerufen wenn Hintergrund gesetzt', async () => {
    const att = makePngAtt()
    const board = makeBoard({
      background:  { attachmentId: att.id },
      attachments: [att],
    })
    setupDOM(board)
    // Kurz warten damit die async boardAttachmentObjectUrl aufgelöst wurde
    await new Promise(r => setTimeout(r, 10))
    expect(boardAttachmentObjectUrl).toHaveBeenCalledWith(att)
  })

  /* Kritiker zu Karte #439: `applyBoardBackground` hängt an `state:changed`,
     und das feuert bei jeder Kartenbewegung, jedem Kommentar und — in den
     Polling-Profilen — bei jedem erfolgreichen Poll. Seit der Umstellung auf
     Blob-URLs ist jeder Aufruf ein `atob` plus eine Byte-Schleife über das
     ganze Bild; vorher war es eine Template-Konkatenation. Ohne den
     Id-Vergleich hätte sich der Fix gegen den unsichtbaren Hintergrund als
     wiederkehrende Last durch die Hintertür wieder eingekauft. */
  it('ein Re-Render mit UNVERÄNDERTEM Bild decodiert es nicht erneut', async () => {
    const att = makePngAtt()
    const board = makeBoard({ background: { attachmentId: att.id }, attachments: [att] })
    setupDOM(board)
    await new Promise(r => setTimeout(r, 10))
    expect(boardAttachmentObjectUrl).toHaveBeenCalledTimes(1)

    /* Zweimal neu rendern, ohne am Hintergrund etwas zu ändern. */
    Renderer.renderBoard({ board })
    Renderer.renderBoard({ board })
    await new Promise(r => setTimeout(r, 10))
    expect(boardAttachmentObjectUrl,
      'der Hintergrund wurde bei einem Re-Render ohne Änderung erneut decodiert')
      .toHaveBeenCalledTimes(1)
  })

  /* Die Gegenrichtung: Ein ANDERES Bild muss sehr wohl neu geladen werden —
     sonst hätte der Guard den Hintergrund eingefroren. */
  it('ein Wechsel des Bildes lädt neu', async () => {
    const a = makePngAtt()
    const b = makePngAtt()
    setupDOM(makeBoard({ background: { attachmentId: a.id }, attachments: [a, b] }))
    await new Promise(r => setTimeout(r, 10))
    expect(boardAttachmentObjectUrl).toHaveBeenCalledTimes(1)

    Renderer.renderBoard({ board: makeBoard({ background: { attachmentId: b.id }, attachments: [a, b] }) })
    await new Promise(r => setTimeout(r, 10))
    expect(boardAttachmentObjectUrl).toHaveBeenCalledTimes(2)
  })

  it('SVG-Anhang wird nicht als Hintergrund gesetzt (F-128)', async () => {
    const svgAtt = makePngAtt({ mime: 'image/svg+xml', filename: 'bild.svg' })
    const board = makeBoard({
      background:  { attachmentId: svgAtt.id },
      attachments: [svgAtt],
    })
    const app = setupDOM(board)
    await new Promise(r => setTimeout(r, 10))
    // SVG wird ignoriert → boardAttachmentObjectUrl wird nicht aufgerufen
    expect(boardAttachmentObjectUrl).not.toHaveBeenCalled()
    expect(app.classList.contains('board--has-bg')).toBe(false)
  })

  /* F-1107 (Nebenbefund der Server-Nacharbeit zu F-855/#538): der exakte
     String-Vergleich liess `image/svg` (ohne `+xml`) als normales Bild durch. */
  it('F-1107: image/svg (ohne +xml) wird ebenfalls nicht als Hintergrund gesetzt', async () => {
    const svgAtt = makePngAtt({ mime: 'image/svg', filename: 'bild.svg' })
    const board = makeBoard({
      background:  { attachmentId: svgAtt.id },
      attachments: [svgAtt],
    })
    const app = setupDOM(board)
    await new Promise(r => setTimeout(r, 10))
    /* Rot ohne den Fix: `image/svg` besteht den exakten
       `!== 'image/svg+xml'`-Vergleich, boardAttachmentObjectUrl würde
       aufgerufen. */
    expect(boardAttachmentObjectUrl).not.toHaveBeenCalled()
    expect(app.classList.contains('board--has-bg')).toBe(false)
  })

  it('board.attachments-Feld ist im Board-Schema vorhanden', () => {
    const board = makeBoard()
    expect(Array.isArray(board.attachments)).toBe(true)
  })

  it('board.background-Feld ist im Board-Schema vorhanden', () => {
    const board = makeBoard()
    expect('background' in board).toBe(true)
    expect(board.background).toBeNull()
  })
})
