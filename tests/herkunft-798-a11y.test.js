/* @vitest-environment happy-dom */
/* F-2409 (Sprint-95-Kritiker KF-4): Der Zähler der Übersicht „🤖 N“ steht im zugänglichen Namen des Knopfs,
   nicht nur im sichtbaren Text. Geprüft am echten Ergebnis von Renderer.renderBoard. */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/sync.js', () => ({
  Sync: { getDirectoryName: vi.fn(() => null), listKnownBoards: vi.fn(async () => []), getInboxOpCount: vi.fn(() => 0),
    requestCompaction: vi.fn(async () => true), getChatTail: vi.fn(() => []), setChatTailListener: vi.fn(() => {}),
    setChatTailEnabled: vi.fn(async () => {}), readChatTail: vi.fn(async () => {}) },
}))
vi.mock('../src/storage.js', () => ({ Storage: { listKnownFiles: vi.fn(async () => []) }, istMetaKarteLeer: vi.fn(() => false) }))
vi.mock('../src/dragdrop.js', () => ({ DragDrop: { takePendingFlip: vi.fn(() => null), init: vi.fn() } }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))
vi.mock('../src/config.js', () => ({ Config: { get: vi.fn((k) => (k === 'userName' ? 'TestUser' : (k === 'boardMode' ? 'standalone' : ''))), set: vi.fn() } }))

import { State }    from '../src/state.js'
import { Filter }   from '../src/filter.js'
import { Renderer } from '../src/renderer.js'
import { I18n }     from '../src/i18n.js'

const heute = new Date().toISOString().slice(0, 10)
const k = (id, o = {}) => ({ id, displayId: Number(id.slice(1)), title: 'K' + id, description: '', labels: [], owner: '', assignees: [],
  priority: 'mittel', complexity: '', effort: null, qa: false, critical: false, blocked: false, archived: false,
  columnId: 'col-backlog', comments: [], attachments: [], links: [], activity: [], created: heute, ...o })

beforeEach(() => { document.body.innerHTML = ''; Filter.clear() })
afterEach(() => { document.body.innerHTML = ''; Filter.clear() })

describeServer('F-2409: Zähler „🤖 N“ im zugänglichen Namen', () => {
  it('aria-label nennt Titel und Zahl; die Flags ⚡/⊘ behalten den reinen Titel', () => {
    const header = document.createElement('div'); header.id = 'board-header'
    const container = document.createElement('div'); container.id = 'board-columns'
    document.body.append(header, container)
    const board = { id: 'b', title: 'B', columnDefs: [{ id: 'col-backlog', title: 'Backlog' }],
      sprints: [{ id: 's1', title: 'S', kind: 'sprint', cards: [k('c1', { angelegtVonAgent: true }), k('c2', { angelegtVonAgent: true }), k('c3')] }] }
    State.set({ board })
    Renderer.renderBoard({ board })
    const btn = [...document.querySelectorAll('.filter-flag')].find(b => b.textContent.startsWith('🤖'))
    expect(btn.textContent).toBe('🤖 2')
    expect(btn.getAttribute('aria-label')).toBe(`${I18n.t('filter.vonAgent')}: 2`)
    const krit = [...document.querySelectorAll('.filter-flag')].find(b => b.textContent === '⚡')
    expect(krit.getAttribute('aria-label')).toBe(I18n.t('filter.critical'))
  })
})
