/* @vitest-environment happy-dom */

/**
 * Karte #441 (ADR-0118 §6) — configdialog.js auf die gemeinsame Modal-Shell
 * (src/modal-shell.js, `createModal()`) umgestellt. Letzter der acht
 * Einstellungsdialoge; anders als bei den sieben zuvor migrierten ist dieses
 * Modul NICHT IS_SERVER_BUILD-gegated.
 *
 * Abdeckung — was der Umbau NEU hinzufügt (Bestandsverhalten bleibt in
 * tests/config-partition.test.js und tests/server-mode-filter.test.js):
 *  (a) Focus-Trap: Tab am Ende des Dialogs läuft zum Anfang um (gab es vorher
 *      nicht — configdialog.js hatte einen eigenen, unvollständigeren
 *      Tab-Handler ohne Shift+Tab-Rücklauf-Test).
 *  (b) ESC schliesst über die Shell (onRequestClose → cancel(partition)).
 *  (c) Beide Partitionen (user/board) lassen sich unabhängig öffnen und
 *      schliessen — zwei Shell-Instanzen, kein gemeinsamer Zustand.
 *  (d) Sichtbarmachen synchron ohne requestAnimationFrame, für BEIDE
 *      Partitionen (die bestehende F-808-Regression in
 *      tests/nachzuegler-451.test.js deckt nur die board-Partition ab).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName:    vi.fn(() => null),
    isActive:            vi.fn(() => false),
    isPermissionNeeded:  vi.fn(() => false),
  },
}))

vi.mock('../src/storage.js', () => ({
  Storage: { listKnownFiles: vi.fn(async () => []) },
}))

vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      listKnownBoards: vi.fn(async () => []),
    }),
  },
}))

vi.mock('../src/attachments.js', () => ({
  uploadBoardFile:       vi.fn(async () => ({ id: 'att1' })),
  boardAttachmentUrl:    vi.fn(async () => null),
  deleteBoardAttachment: vi.fn(async () => {}),
  checkUpload:           vi.fn(() => ({ ok: true })),
}))

vi.mock('../src/swipe.js', () => ({
  addSwipeToDismiss: vi.fn(),
}))

vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Config }       from '../src/config.js'
import { State }        from '../src/state.js'
import { EventBus }     from '../src/eventbus.js'
import { ConfigDialog } from '../src/configdialog.js'

function keydown(target, key, opts = {}) {
  const ev = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...opts })
  target.dispatchEvent(ev)
  return ev
}

/* Panel per aria-labelledby identifizieren (stabil — trägt die Partition im
   Namen, siehe configdialog.js#buildOverlay: titleId = `config-dialog-title-${partition}`). */
function panelFor(partition) {
  return document.querySelector(`[aria-labelledby="config-dialog-title-${partition}"]`)
}
function overlayFor(partition) {
  return panelFor(partition)?.closest('.modal-overlay') ?? null
}

beforeEach(() => {
  /* Reset des Modul-Singletons — derselbe Weg wie in config-partition.test.js
     (der einzige vom Modul selbst vorgesehene): schliesst offene Modals
     (siehe configdialog.js EventBus.on('config:language-changed', ...)) und
     baut beide Overlays beim nächsten Öffnen neu. */
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
  State.set({
    board: {
      id: 'b1', title: 'Test-Board',
      identities: [{ id: 'user-default', kind: 'user', label: 'Andi', color: '#0B5D73', icon: null }],
    },
  })
  Config.set('boardMode', 'solo')
})

afterEach(() => {
  EventBus.emit('config:language-changed')
  vi.clearAllMocks()
})

describe('#441 — Focus-Trap (neu, gab es vorher nicht)', () => {
  it('Tab am letzten fokussierbaren Element läuft zum ersten um', () => {
    EventBus.emit('config:open-user')
    const panel = panelFor('user')
    expect(panel, 'Panel nicht gefunden').toBeTruthy()

    const focusable = [...panel.querySelectorAll('input, select, button')]
      .filter(f => !f.disabled)
    expect(focusable.length, 'kein fokussierbares Element im Dialog').toBeGreaterThan(1)
    const first = focusable[0]
    const last  = focusable[focusable.length - 1]

    last.focus()
    expect(document.activeElement).toBe(last)
    keydown(document, 'Tab')
    expect(document.activeElement, 'Tab am Ende läuft nicht zum Anfang um — kein Focus-Trap')
      .toBe(first)
  })

  it('Shift+Tab am ersten fokussierbaren Element läuft zum letzten um', () => {
    EventBus.emit('config:open-board')
    const panel = panelFor('board')
    const focusable = [...panel.querySelectorAll('input, select, button')]
      .filter(f => !f.disabled)
    expect(focusable.length).toBeGreaterThan(1)
    const first = focusable[0]
    const last  = focusable[focusable.length - 1]

    first.focus()
    expect(document.activeElement).toBe(first)
    keydown(document, 'Tab', { shiftKey: true })
    expect(document.activeElement, 'Shift+Tab am Anfang läuft nicht zum Ende um — kein Focus-Trap')
      .toBe(last)
  })
})

describe('#441 — ESC schliesst über die Shell (onRequestClose)', () => {
  it('ESC auf dem Benutzer-Dialog entfernt die --open-Klasse', () => {
    EventBus.emit('config:open-user')
    const overlay = overlayFor('user')
    expect(overlay.classList.contains('modal-overlay--open')).toBe(true)
    keydown(document, 'Escape')
    expect(overlay.classList.contains('modal-overlay--open')).toBe(false)
    expect(overlay.getAttribute('aria-hidden')).toBe('true')
  })
})

describe('#441 — beide Partitionen öffnen/schliessen unabhängig (zwei Shell-Instanzen)', () => {
  it('Board-Dialog öffnen, während der Benutzer-Dialog schon offen ist — beide bleiben offen', () => {
    EventBus.emit('config:open-user')
    EventBus.emit('config:open-board')

    const userOverlay  = overlayFor('user')
    const boardOverlay = overlayFor('board')
    expect(userOverlay,  'Benutzer-Overlay fehlt').toBeTruthy()
    expect(boardOverlay, 'Board-Overlay fehlt').toBeTruthy()
    expect(userOverlay).not.toBe(boardOverlay)
    expect(userOverlay.classList.contains('modal-overlay--open')).toBe(true)
    expect(boardOverlay.classList.contains('modal-overlay--open')).toBe(true)
  })

  it('Schliessen der Benutzer-Partition (Close-Button) lässt die offene Board-Partition unberührt', () => {
    EventBus.emit('config:open-user')
    EventBus.emit('config:open-board')

    const userOverlay  = overlayFor('user')
    const boardOverlay = overlayFor('board')
    userOverlay.querySelector('.btn-modal-close').click()

    expect(userOverlay.classList.contains('modal-overlay--open'),
      'Benutzer-Partition wurde nicht geschlossen').toBe(false)
    expect(boardOverlay.classList.contains('modal-overlay--open'),
      'Board-Partition wurde durch das Schliessen der Benutzer-Partition mitgeschlossen — die Instanzen sind nicht unabhängig').toBe(true)
  })
})

describe('#441 — Sichtbarmachen synchron, ohne requestAnimationFrame (beide Partitionen)', () => {
  it('Benutzer-Dialog: --open steht sofort nach config:open-user, ohne await', () => {
    EventBus.emit('config:open-user')
    const overlay = overlayFor('user')
    expect(overlay, 'Overlay wurde nicht gemountet').toBeTruthy()
    expect(overlay.classList.contains('modal-overlay--open')).toBe(true)
  })

  it('Board-Dialog: --open steht sofort nach config:open-board, ohne await', () => {
    EventBus.emit('config:open-board')
    const overlay = overlayFor('board')
    expect(overlay, 'Overlay wurde nicht gemountet').toBeTruthy()
    expect(overlay.classList.contains('modal-overlay--open')).toBe(true)
  })
})
