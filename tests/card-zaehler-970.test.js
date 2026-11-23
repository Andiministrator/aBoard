/* @vitest-environment happy-dom */

/* Karte #970: Zähler für Kommentare (💬 n), Anhänge (📎 n) und Beschreibung (≡)
   im Footer normaler Karten. 0 bzw. leere Beschreibung bleibt unsichtbar. */

import { describe, it, expect, beforeEach, vi } from 'vitest'

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
vi.mock('../src/storage.js', () => ({ Storage: { listKnownFiles: vi.fn(async () => []) } }))
vi.mock('../src/dragdrop.js', () => ({ DragDrop: { takePendingFlip: vi.fn(() => null), init: vi.fn() } }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Filter }   from '../src/filter.js'
import { Renderer } from '../src/renderer.js'
import { I18n }     from '../src/i18n.js'

let n = 0
function rendere(karte) {
  document.body.innerHTML = '<div id="board-header"></div><div id="board-columns"></div>'
  const colId = `col-z${++n}`
  const board = {
    id: 'board-z', title: 'B', columnDefs: [{ id: colId, title: 'Backlog' }],
    sprints: [{ id: `sp-z${n}`, title: 'Sprint', cards: [{
      id: `k-z${n}`, displayId: 1, title: 'K', description: '', labels: [], owner: '',
      priority: 'mittel', complexity: '', effort: null, qa: false, critical: false,
      blocked: false, columnId: colId, ...karte,
    }] }],
  }
  Renderer.renderBoard({ board })
  return [...document.querySelectorAll('.card .card-count-badge')]
}

beforeEach(() => { Filter.clear(); I18n.setLang('de') })

describe('#970 Zähler auf der Kartenvorderseite', () => {
  it('ohne Kommentare, Anhänge und Beschreibung: kein Zähler, kein Footer', () => {
    expect(rendere({})).toHaveLength(0)
    expect(document.querySelector('.card .card-footer')).toBeNull()
  })

  it('leere Listen und eine Beschreibung nur aus Leerraum zählen nicht', () => {
    expect(rendere({ comments: [], attachments: [], description: '  \n ' })).toHaveLength(0)
  })

  it('zeigt 💬 n, 📎 n und ≡ in dieser Reihenfolge, mit Klartext als title und aria-label', () => {
    const z = rendere({
      comments: [{ id: 'c1' }, { id: 'c2' }, { id: 'c3' }],
      attachments: [{ id: 'a1' }],
      description: 'Text',
    })
    expect(z.map(b => b.textContent)).toEqual(['💬 3', '📎 1', '≡'])
    expect(z.map(b => b.title)).toEqual([
      `${I18n.t('tab.comments')}: 3`,
      `${I18n.t('card.attachments')}: 1`,
      I18n.t('card.export.section.description'),
    ])
    for (const b of z) expect(b.getAttribute('aria-label')).toBe(b.title)
    /* F-2675: role=img, sonst sagt der Screenreader das Symbol statt des Namens */
    for (const b of z) expect(b.getAttribute('role')).toBe('img')
    /* F-2678: Klartext ohne Emoji aus dem Tab-Schlüssel */
    expect(z[1].title).not.toContain('📎')
  })

  it('#971: Labels auf der Karte tragen die Labelfarbe', () => {
    rendere({ labels: ['ux'] })
    const l = document.querySelector('.card .card-labels .label')
    expect(l.classList.contains('lc')).toBe(true)
    expect(l.style.getPropertyValue('--h')).not.toBe('')
  })

  it('#972: Priorität als Symbol mit Klartext in title und aria-label', () => {
    rendere({ priority: 'hoch' })
    const b = document.querySelector('.card .card-prio-badge--high')
    expect(b.textContent).toBe('↑')
    expect(b.title).toBe(`${I18n.t('card.priority')}: ${I18n.t('card.prio.high')}`)
    expect(b.getAttribute('aria-label')).toBe(b.title)
    expect(b.getAttribute('role')).toBe('img')
    rendere({ priority: 'niedrig' })
    expect(document.querySelector('.card .card-prio-badge--low').textContent).toBe('↓')
    rendere({ priority: 'mittel' })
    expect(document.querySelector('.card .card-prio-badge')).toBeNull()
  })

  it('#972: Komplexität als ●○○ / ●●○ / ●●●, Klartext in title und aria-label', () => {
    const stufen = { einfach: ['●○○', 'card.complexity.simple'], mittel: ['●●○', 'card.complexity.medium'], hoch: ['●●●', 'card.complexity.high'] }
    for (const [cx, [sym, key]] of Object.entries(stufen)) {
      rendere({ complexity: cx })
      const b = document.querySelector('.card .card-complexity-badge')
      expect(b.textContent).toBe(sym)
      expect(b.title).toBe(`${I18n.t('card.complexity')}: ${I18n.t(key)}`)
      expect(b.getAttribute('aria-label')).toBe(b.title)
      expect(b.getAttribute('role')).toBe('img')
    }
    rendere({ complexity: 'xyz' })
    expect(document.querySelector('.card .card-complexity-badge').textContent).toBe('xyz')
  })

  it('ein einzelner Zähler reicht für den Footer', () => {
    const z = rendere({ attachments: [{ id: 'a1' }, { id: 'a2' }] })
    expect(z.map(b => b.textContent)).toEqual(['📎 2'])
    expect(document.querySelector('.card .card-footer')).not.toBeNull()
  })
})
