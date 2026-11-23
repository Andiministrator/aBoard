/* @vitest-environment happy-dom */

/**
 * F-3110 (Karte #1190): Sprint-Meta-Karten tragen ihren Titel fest auf Deutsch (Daten). Angezeigt wird er
 * in der Oberflächensprache — nur solange er der vorgegebene ist; gespeichert wird nichts davon.
 * Mocks wie tests/a11y-symbol-labels.test.js.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// ── Mocks für Abhängigkeiten (renderer.js + modal.js) ─────────────────────

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName: vi.fn(() => null),
    listKnownBoards:  vi.fn(async () => []),
    getInboxOpCount:  vi.fn(() => 0),
    requestCompaction: vi.fn(async () => true),
    getChatTail:         vi.fn(() => []),
    setChatTailListener: vi.fn(() => {}),
    setChatTailEnabled:  vi.fn(async () => {}),
    readChatTail:        vi.fn(async () => {}),
  },
}))

vi.mock('../src/storage.js', () => ({
  Storage: { listKnownFiles: vi.fn(async () => []) },
  istMetaKarteLeer: vi.fn(() => false),
}))

vi.mock('../src/dragdrop.js', () => ({
  DragDrop: { takePendingFlip: vi.fn(() => null), init: vi.fn() },
}))

vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

let boardModeValue = 'standalone'
vi.mock('../src/config.js', () => ({
  Config: {
    get: vi.fn((key) => {
      if (key === 'boardMode') return boardModeValue
      if (key === 'userName') return 'TestUser'
      return ''
    }),
    set: vi.fn(),
  },
}))

import { EventBus } from '../src/eventbus.js'
import { State }    from '../src/state.js'
import { Renderer } from '../src/renderer.js'
import { I18n }     from '../src/i18n.js'
import { Modal }    from '../src/modal.js'

import { Model } from '../src/model.js'

function zeichne(karten) {
  document.body.innerHTML = '<div id="board-header"></div><div id="board-columns"></div>'
  const board = {
    id: 'b-1190', title: 'Meta', columnDefs: [{ id: 'col-backlog', title: 'Backlog' }, { id: 'col-erledigt', title: 'Erledigt' }],
    sprints: [
      { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      { id: 's-1', title: 'Sprint 1', kind: 'sprint', cards: karten },
    ],
  }
  State.set({ board })
  Renderer.renderBoard({ board })
  return [...document.querySelectorAll('.card--sprint-meta')]
}

beforeEach(() => { I18n.setLang('en') })
afterEach(() => { I18n.setLang('de'); document.body.innerHTML = '' })

describe('F-3110 — Meta-Karten-Titel', () => {
  const start = () => Model.createSprintMetaCard('start', 1, 'col-backlog', false)
  const ende  = () => Model.createSprintMetaCard('end', 2, 'col-backlog', false)

  it('die Daten behalten den deutschen Vorgabetitel', () => {
    expect(start().title).toBe('📋 Sprint-Start')
    expect(ende().title).toBe('🏁 Sprint-Abschluss')
  })

  it('englische Oberfläche zeigt englische Titel (Text und aria-label), deutsche die deutschen', () => {
    let el = zeichne([start(), ende()])
    expect(el.map(e => e.querySelector('.card-title-text').textContent)).toEqual(['📋 Sprint start', '🏁 Sprint end'])
    expect(el.map(e => e.getAttribute('aria-label'))).toEqual(['📋 Sprint start', '🏁 Sprint end'])
    I18n.setLang('de')
    el = zeichne([start(), ende()])
    expect(el.map(e => e.querySelector('.card-title-text').textContent)).toEqual(['📋 Sprint-Start', '🏁 Sprint-Abschluss'])
  })

  it('ein abweichender Titel bleibt, wie er ist', () => {
    const el = zeichne([{ ...start(), title: 'Eigener Start' }])
    expect(el[0].querySelector('.card-title-text').textContent).toBe('Eigener Start')
  })

  it('das Karten-Fenster zeigt den übersetzten Titel', () => {
    const karte = start()
    zeichne([karte])
    EventBus.emit('card:edit', karte)
    expect(document.querySelector('#modal-title')?.value).toBe('📋 Sprint start')
    expect(karte.title).toBe('📋 Sprint-Start')
  })
})
