/* @vitest-environment happy-dom */

/**
 * Karte #667 / ADR-0133 §1 — Findings: Oberfläche (Abzeichen, Tab, Filter, Anlegen).
 * Datenmodell und Wortschatz: src/finding.js (Karte #666). Findings sind ein Kartentyp
 * des Board-Modells, die Oberfläche hängt an keinem Build-Flag — läuft in allen Kontexten.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn((k) => (k === 'userName' ? 'T' : (k === 'boardMode' ? 'standalone' : ''))), set: vi.fn() },
}))

import { EventBus }  from '../src/eventbus.js'
import '../src/modal.js'
import { Filter }    from '../src/filter.js'
import { State }     from '../src/state.js'
import { Renderer }  from '../src/renderer.js'
import { I18n }      from '../src/i18n.js'
import { FINDING_TYPEN, FINDING_STATUS } from '../src/finding.js'

const karte = (id, extra = {}) => ({
  id, displayId: 1, title: 'K ' + id, description: '', agentContext: '', comments: [], attachments: [],
  links: [], activity: [], priority: 'mittel', labels: [], owner: '', complexity: '', effort: null,
  qa: false, critical: false, blocked: false, archived: false, columnId: 'col-x', ...extra,
})
const finding = (id, f = {}) => karte(id, {
  kind: 'finding', finding: { nr: 7, typ: 'K', prioritaet: 'P1', status: 'offen', ...f },
})

function rendern(cards) {
  document.body.innerHTML = '<div id="board-header"></div><div id="board-columns"></div>'
  const board = { id: 'b', title: 'B', columnDefs: [{ id: 'col-x', title: 'X' }],
    sprints: [{ id: 's1', title: 'S1', cards }] }
  Renderer.renderBoard({ board })
  return document.body
}

beforeEach(() => { document.body.innerHTML = ''; Filter.clear(); State.markClean(); EventBus.emit('config:language-changed') })
afterEach(() => { EventBus.emit('config:language-changed'); document.body.innerHTML = ''; Filter.clear() })

describe('Findings-Oberfläche — Abzeichen und Filter', () => {
  it('Finding-Karte trägt Abzeichen „F-7 P1“, normale Karte keins', () => {
    const d = rendern([finding('a'), karte('b')])
    const badges = d.querySelectorAll('.card-finding-id')
    expect(badges.length).toBe(1)
    expect(badges[0].textContent).toBe('F-7 P1')
  })

  it('Filter-Auswahl Typ/Priorität/Status erscheint nur auf Boards mit Findings', () => {
    expect(rendern([karte('b')]).querySelectorAll('.header-filter select').length).toBe(0)
    document.body.innerHTML = ''
    expect(rendern([finding('a')]).querySelectorAll('.header-filter select').length).toBe(3)
  })

  it('Filter.matches: Finding-Filter trifft nur passende Findings, nie normale Karten', () => {
    Filter.set({ finding: ['typ', 'K'] })
    expect(Filter.isActive()).toBe(true)
    expect(Filter.matches(finding('a', { typ: 'K' }))).toBe(true)
    expect(Filter.matches(finding('a', { typ: 'S' }))).toBe(false)
    expect(Filter.matches(karte('n'))).toBe(false)
    Filter.set({ finding: ['status', 'behoben'] })
    expect(Filter.matches(finding('a', { typ: 'K', status: 'offen' }))).toBe(false)
    Filter.set({ finding: ['typ', ''] }); Filter.set({ finding: ['status', ''] })
    expect(Filter.isActive()).toBe(false)
    expect(Filter.matches(karte('n'))).toBe(true)
  })

  it('Filter.clear() räumt auch den Finding-Filter', () => {
    Filter.set({ finding: ['prioritaet', 'P0'] }); Filter.clear()
    expect(Filter.isActive()).toBe(false)
  })

  it('„Finding“-Knopf im Anlege-Formular emittiert card:quick-add mit finding:true; „Hinzufügen“ mit false', () => {
    const d = rendern([])
    const spy = vi.fn(); EventBus.on('card:quick-add', spy)
    try {
      for (const [sel, soll] of [['.btn-new-finding', true], ['.card-add-actions .btn-primary', false]]) {
        d.querySelector('.btn-add-card').click()
        d.querySelector('.card-add-input').value = 'Neu'
        d.querySelector(sel).click()
        expect(spy.mock.calls.at(-1)[0].finding).toBe(soll)
        rendern([])
      }
    } finally { EventBus.off('card:quick-add', spy) }
  })
})

describe('Findings-Oberfläche — Modal-Tab', () => {
  const oeffnen = (c) => { EventBus.emit('card:edit', c); return document.querySelector('.modal-overlay') }

  it('Tab nur bei Finding sichtbar; Auswahllisten tragen genau den Wortschatz aus finding.js', () => {
    const o = oeffnen(finding('a'))
    expect(o.querySelector('#tab-finding').style.display).toBe('')
    expect([...o.querySelectorAll('#modal-finding-typ option')].map(x => x.value)).toEqual([...FINDING_TYPEN])
    expect([...o.querySelectorAll('#modal-finding-status option')].map(x => x.value)).toEqual([...FINDING_STATUS])
    expect(o.querySelector('#modal-finding-prioritaet').value).toBe('P1')
    EventBus.emit('config:language-changed')
    expect(oeffnen(karte('n')).querySelector('#tab-finding').style.display).toBe('none')
  })

  it('Speichern: geänderte Felder gehen als card.finding hinaus, nr bleibt; unverändert = nicht dirty', () => {
    const o = oeffnen(finding('a'))
    const spy0 = vi.fn(); EventBus.on('card:updated', spy0)
    o.dispatchEvent(new MouseEvent('click', { bubbles: true })) /* Backdrop: Auto-Save nur bei Änderung */
    EventBus.off('card:updated', spy0)
    expect(spy0).not.toHaveBeenCalled()
    const o2 = oeffnen(finding('a'))
    o2.querySelector('#modal-finding-status').value = 'behoben'
    o2.querySelector('#modal-finding-lehre').value = 'Immer messen'
    const spy = vi.fn(); EventBus.on('card:updated', spy)
    try {
      [...o2.querySelectorAll('button')].find(b => b.textContent === I18n.t('btn.save-card')).click()
      const f = spy.mock.calls[0][0].finding
      expect(f).toMatchObject({ nr: 7, typ: 'K', prioritaet: 'P1', status: 'behoben', lehre: 'Immer messen' })
    } finally { EventBus.off('card:updated', spy) }
  })
})
