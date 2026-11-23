/* @vitest-environment happy-dom */

/**
 * Karte #727 — Darstellung des Board-Hintergrundbilds:
 * `getoent` (Standard/Altbestand), `voll`, `abgedunkelt`.
 * Gespeichert in `board.background.darstellung`; der Renderer setzt daraus die
 * Klassen `board--bg-voll` / `board--bg-dunkel` auf #app.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/sync.js', () => ({
  Sync: { getDirectoryName: vi.fn(() => null), listKnownBoards: vi.fn(async () => []),
    isActive: vi.fn(() => false), isPermissionNeeded: vi.fn(() => false) },
}))
vi.mock('../src/dragdrop.js', () => ({ DragDrop: { takePendingFlip: vi.fn(() => null), init: vi.fn() } }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))
vi.mock('../src/attachments.js', () => ({
  attachmentUrl: vi.fn(async () => null),
  boardAttachmentObjectUrl: vi.fn(async (att) => att?.data ? `blob:mock/${att.id}` : null),
  boardAttachmentUrl: vi.fn(async () => null),
  computeBoardBytes: vi.fn(() => 0),
  humanSize: vi.fn(() => '–'),
  istSvgMime: vi.fn(),
}))
vi.mock('../src/storage-plugin.js', () =>
  import('./helpers/attachment-backend-mock.js').then(m => m.storagePluginMock()))

import { Storage }  from '../src/storage.js'
import { State }    from '../src/state.js'
import { Filter }   from '../src/filter.js'
import { Renderer } from '../src/renderer.js'
import * as Attachments from '../src/attachments.js'

const { istSvgMime: echt } = await vi.importActual('../src/attachments.js')
Attachments.istSvgMime.mockImplementation(echt)

const att = { id: 'att-1', filename: 'bg.png', mime: 'image/png', size: 1, created: '2026-10-06', data: 'abc' }
const warte = () => new Promise(r => setTimeout(r, 10))

function board(background) {
  return {
    id: 'b727', title: 'T', columnDefs: [{ id: 'col-a', title: 'A' }],
    sprints: [{ id: 's1', title: 'S', cards: [] }], attachments: [att], background,
  }
}
function dom() {
  const app = document.createElement('div'); app.id = 'app'
  const h = document.createElement('div'); h.id = 'board-header'
  const c = document.createElement('div'); c.id = 'board-columns'
  app.append(h, c); document.body.appendChild(app)
  return app
}

beforeEach(() => { document.body.innerHTML = ''; Filter.clear() })
afterEach(async () => {
  /* Modulzustand des Renderers zurücksetzen (Leer-Render, siehe board-background-race) */
  Renderer.renderBoard({ board: board(null) })
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

describe('Renderer: Klassen je Darstellung (#727)', () => {
  it.each([
    [{ attachmentId: 'att-1' }, false, false],                                   // Altbestand = getönt
    [{ attachmentId: 'att-1', darstellung: 'getoent' }, false, false],
    [{ attachmentId: 'att-1', darstellung: 'quatsch' }, false, false],           // unbekannt = getönt
    [{ attachmentId: 'att-1', darstellung: 'voll' }, true, false],
    [{ attachmentId: 'att-1', darstellung: 'abgedunkelt' }, false, true],
  ])('%j → voll=%s dunkel=%s', async (bg, voll, dunkel) => {
    const app = dom()
    State.set({ board: board(bg) })
    Renderer.renderBoard({ board: board(bg) })
    await warte()
    expect(app.classList.contains('board--has-bg')).toBe(true)
    expect(app.classList.contains('board--bg-voll')).toBe(voll)
    expect(app.classList.contains('board--bg-dunkel')).toBe(dunkel)
  })

  it('Moduswechsel bei UNVERÄNDERTEM Bild wirkt (Frühausstieg darf ihn nicht verschlucken)', async () => {
    const app = dom()
    Renderer.renderBoard({ board: board({ attachmentId: 'att-1' }) })
    await warte()
    expect(app.classList.contains('board--bg-voll')).toBe(false)
    Renderer.renderBoard({ board: board({ attachmentId: 'att-1', darstellung: 'voll' }) })
    expect(app.classList.contains('board--bg-voll')).toBe(true)
    Renderer.renderBoard({ board: board({ attachmentId: 'att-1', darstellung: 'abgedunkelt' }) })
    expect(app.classList.contains('board--bg-voll')).toBe(false)
    expect(app.classList.contains('board--bg-dunkel')).toBe(true)
    Renderer.renderBoard({ board: board({ attachmentId: 'att-1' }) })
    expect(app.classList.contains('board--bg-dunkel')).toBe(false)
  })

  it('ohne Hintergrund trägt #app keine Darstellungs-Klasse', async () => {
    const app = dom()
    Renderer.renderBoard({ board: board({ attachmentId: 'att-1', darstellung: 'voll' }) })
    await warte()
    Renderer.renderBoard({ board: board(null) })
    expect(app.classList.contains('board--bg-voll')).toBe(false)
    expect(app.classList.contains('board--has-bg')).toBe(false)
  })
})

describe('Normalisierung (#727)', () => {
  function roh(bg) { return { version: '2', board: board(bg) } }
  it('unbekannter Wert → getoent; gültige Werte und Altbestand bleiben unangetastet', () => {
    dom() /* afterEach rendert leer und braucht das Gerüst */
    const n = (bg) => Storage.checkVersion(roh(bg)).board.background
    expect(n({ attachmentId: 'att-1', darstellung: 'quatsch' }).darstellung).toBe('getoent')
    expect(n({ attachmentId: 'att-1', darstellung: 'voll' }).darstellung).toBe('voll')
    expect(n({ attachmentId: 'att-1', darstellung: 'abgedunkelt' }).darstellung).toBe('abgedunkelt')
    expect(n({ attachmentId: 'att-1' })).toEqual({ attachmentId: 'att-1' })
  })
})
