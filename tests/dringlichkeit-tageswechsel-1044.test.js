/* @vitest-environment happy-dom */

/**
 * Karte #1044 (F-2804): Bei offenem Board über Mitternacht sortiert die
 * Dringlichkeits-Ansicht neu (Timer bzw. visibilitychange), ohne dass ein
 * anderer Render nötig ist.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName: vi.fn(() => null), listKnownBoards: vi.fn(async () => []),
    getInboxOpCount: vi.fn(() => 0), requestCompaction: vi.fn(async () => true),
    getChatTail: vi.fn(() => []), setChatTailListener: vi.fn(() => {}),
    setChatTailEnabled: vi.fn(async () => {}), readChatTail: vi.fn(async () => {}),
  },
}))
vi.mock('../src/storage.js', () => ({ Storage: { listKnownFiles: vi.fn(async () => []) } }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Filter }   from '../src/filter.js'
import { Renderer } from '../src/renderer.js'
import { Config }   from '../src/config.js'

const karte = (id, o = {}) => ({
  id, displayId: 1, title: id, description: '', labels: [], owner: '', priority: 'mittel',
  complexity: '', effort: null, qa: false, critical: false, blocked: false, columnId: 'c-offen', ...o,
})
const reihenfolge = () => [...document.querySelectorAll('.column[data-id="c-offen"] .card')].map(e => e.dataset.id)

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date(2026, 5, 10, 23, 0, 0))
  Filter.clear(); Config.set('sortierung', 'dringlichkeit')
})
afterEach(() => { vi.useRealTimers(); document.body.innerHTML = ''; Config.set('sortierung', 'manuell') })

describe('Karte #1044 — Tageswechsel', () => {
  it('Timer über Mitternacht: „bald fällig“ wird „überfällig“ ohne weiteren Render', () => {
    document.body.innerHTML = '<div id="board-header"></div><div id="board-columns"></div>'
    const board = {
      id: 'b', title: 'T',
      columnDefs: [{ id: 'c-offen', title: 'Backlog' }],
      sprints: [{ id: 's1', title: 'Lane', cards: [
        karte('a-frei'),
        karte('b-heute', { dueDate: '2026-06-10' }),
        karte('c-gestern', { dueDate: '2026-06-09' }),
      ] }],
    }
    Renderer.renderBoard({ board })
    const vorher = reihenfolge()
    vi.setSystemTime(new Date(2026, 5, 11, 0, 0, 0))
    vi.advanceTimersByTime(2 * 3600 * 1000)
    const nachher = reihenfolge()
    expect(nachher).not.toEqual(vorher)
    expect(nachher[0]).toBe('b-heute')
  })
})
