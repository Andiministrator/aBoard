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

/* F-3112 (Karte #1192): außerhalb von Karte und Karten-Fenster — Verweise und Suche. */
import { addLink } from '../src/links.js'
import { Search }  from '../src/search.js'

describe('F-3112 — Meta-Karten in Verweisen und Suche', () => {
  const start = () => Model.createSprintMetaCard('start', 1, 'col-backlog', false)
  const normal = (links = []) => ({ ...Model.createCard('Normale Karte', 'col-backlog', 3), links })

  it('Model.anzeigeTitel übersetzt nur den unveränderten Vorgabetitel einer Meta-Karte', () => {
    expect(Model.anzeigeTitel(start(), I18n.t)).toBe('📋 Sprint start')
    expect(Model.anzeigeTitel({ ...start(), title: 'Eigen' }, I18n.t)).toBe('Eigen')
    expect(Model.anzeigeTitel({ title: '📋 Sprint-Start', metaType: 'start' }, I18n.t)).toBe('📋 Sprint-Start')
    expect(Model.anzeigeTitel(start())).toBe('📋 Sprint-Start')
  })

  it('addLink lehnt Meta-Karten als Quelle und Ziel ab (ADR-0036)', () => {
    const m = start(), n = normal()
    const board = { sprints: [{ id: 's-1', cards: [m, n] }] }
    expect(addLink(board, n.id, m.id, 'related')).toBe(false)
    expect(addLink(board, m.id, n.id, 'related')).toBe(false)
    expect(n.links).toEqual([])
  })

  it('die Verweis-Auswahl bietet keine Meta-Karten an', () => {
    const m = start(), n = normal(), z = { ...Model.createCard('Ziel', 'col-backlog', 4), links: [] }
    zeichne([m, n, z])
    EventBus.emit('card:edit', n)
    document.querySelector('#link-add-btn').click()
    const zeilen = [...document.querySelectorAll('.link-picker-row')].map(r => r.textContent)
    expect(zeilen.some(t => t.includes('#4 Ziel'))).toBe(true)
    expect(zeilen.some(t => t.includes('📋'))).toBe(false)
  })

  it('ein Alt-Verweis auf eine Meta-Karte zeigt deren übersetzten Titel', () => {
    const m = start()
    const n = normal([{ targetId: m.id, type: 'related', created: '2026-11-23' }])
    zeichne([m, n])
    EventBus.emit('card:edit', n)
    expect(document.querySelector('.link-target')?.textContent).toBe('#1 📋 Sprint start')
  })

  it('die Suche findet eine Meta-Karte über ihren übersetzten Titel', () => {
    vi.useFakeTimers()
    try {
      Search.setQuery('sprint start')
      vi.advanceTimersByTime(200)
      expect(Search.matches(start())).toBe(true)
      expect(Search.matches(normal())).toBe(false)
      expect(Search.matches({ ...Model.createCard('📋 Sprint-Start (Text)', 'col-backlog', 5) })).toBe(false)
    } finally {
      Search.clear()
      vi.useRealTimers()
    }
  })
})
