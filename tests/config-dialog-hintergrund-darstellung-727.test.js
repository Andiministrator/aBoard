/* @vitest-environment happy-dom */

/** Karte #727 — Einstellung „Darstellung" im Board-Dialog: speichert, überlebt den Bildwechsel. */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/sync.js', () => ({
  Sync: { getDirectoryName: vi.fn(() => null), isActive: vi.fn(() => false), isPermissionNeeded: vi.fn(() => false) },
}))
vi.mock('../src/storage.js', () => ({ Storage: { listKnownFiles: vi.fn(async () => []) } }))
vi.mock('../src/storage-plugin.js', async (importOriginal) => {
  const actual = await importOriginal()
  return { ...actual, StoragePlugin: { getActivePlugin: () => ({ listKnownBoards: vi.fn(async () => []) }) } }
})
vi.mock('../src/attachments.js', () => ({
  uploadBoardFile:       vi.fn(async () => ({ id: 'neuAtt' })),
  boardAttachmentUrl:    vi.fn(async () => 'blob:bg'),
  deleteBoardAttachment: vi.fn(async () => true),
  checkUpload:           vi.fn(() => ({ ok: true })),
  verkleinereBild:       vi.fn(async (f) => f),
}))
vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Config }   from '../src/config.js'
import { State }    from '../src/state.js'
import { EventBus } from '../src/eventbus.js'
import { ConfigDialog } from '../src/configdialog.js' // eslint-disable-line no-unused-vars

const panel = () => document.querySelector('[aria-labelledby="config-dialog-title-board"]')
const flush = async () => { await new Promise(r => setTimeout(r, 0)); await new Promise(r => setTimeout(r, 0)) }

function setzeBoard(background) {
  State.set({ board: { id: 'b1', title: 'T', identities: [],
    attachments: [{ id: 'bgAtt', name: 'bg.jpg', mime: 'image/jpeg' }], background } })
}

beforeEach(() => {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
  globalThis.alert = vi.fn()
  Config.set('boardMode', 'solo')
})
afterEach(() => { EventBus.emit('config:language-changed'); vi.restoreAllMocks(); vi.clearAllMocks() })

describe('Darstellung im Board-Dialog (#727)', () => {
  it('Altbestand zeigt „getoent"; Auswahl wird in board.background gespeichert und gemeldet', async () => {
    setzeBoard({ attachmentId: 'bgAtt' })
    const gemeldet = vi.fn(); EventBus.on('board:background-changed', gemeldet)
    EventBus.emit('config:open-board'); await flush()
    const sel = panel().querySelector('#cfg-bg-darstellung')
    expect(sel.value).toBe('getoent')
    expect(sel.closest('#cfg-bg-dar-row').style.display).not.toBe('none')
    sel.value = 'abgedunkelt'; sel.dispatchEvent(new Event('change'))
    expect(State.get().board.background).toEqual({ attachmentId: 'bgAtt', darstellung: 'abgedunkelt' })
    expect(gemeldet).toHaveBeenCalled()
  })

  it('gespeicherter Wert wird beim Öffnen vorausgewählt', async () => {
    setzeBoard({ attachmentId: 'bgAtt', darstellung: 'voll' })
    EventBus.emit('config:open-board'); await flush()
    expect(panel().querySelector('#cfg-bg-darstellung').value).toBe('voll')
  })

  it('ohne Hintergrundbild ist die Auswahl ausgeblendet', async () => {
    setzeBoard(null)
    EventBus.emit('config:open-board'); await flush()
    expect(panel().querySelector('#cfg-bg-dar-row').style.display).toBe('none')
  })

  it('Bildwechsel behält die Darstellung', async () => {
    setzeBoard({ attachmentId: 'bgAtt', darstellung: 'voll' })
    let inp = null
    const orig = document.createElement.bind(document)
    vi.spyOn(document, 'createElement').mockImplementation((t, o) => {
      const e = orig(t, o); if (t === 'input') { inp = e; e.click = () => {} } return e
    })
    EventBus.emit('config:open-board'); await flush()
    const pick = [...panel().querySelectorAll('button')].find(b => b.textContent.includes('Bild') || b.textContent.includes('image'))
    pick.click()
    expect(inp, 'Datei-Input wurde nicht angelegt').toBeTruthy()
    await inp.onchange({ target: { files: [{ type: 'image/png', name: 'x.png', size: 1 }] } })
    await flush()
    expect(State.get().board.background).toEqual({ attachmentId: 'neuAtt', darstellung: 'voll' })
  })
})
