/* @vitest-environment happy-dom */

/**
 * Karte #681 — Oberflächen-Findings, die in jedem Build gelten:
 *   F-1555  Verlaufseintrag `vorschlag` ist übersetzt (DE und EN)
 *   F-1548  Vorlagen-Dropdown bietet „Finding“ an; dieselbe card:quick-add-Spur
 *           wie der Finding-Knopf der Spalten-Fußzeile
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn((k) => (k === 'userName' ? 'T' : (k === 'boardMode' ? 'standalone' : ''))), set: vi.fn() },
}))

import { EventBus } from '../src/eventbus.js'
import '../src/modal.js'
import { State }    from '../src/state.js'
import { Renderer } from '../src/renderer.js'
import { I18n }     from '../src/i18n.js'

const karte = (id, extra = {}) => ({
  id, displayId: 1, title: 'K ' + id, description: '', agentContext: '', comments: [], attachments: [],
  links: [], activity: [], priority: 'mittel', labels: [], owner: '', complexity: '', effort: null,
  qa: false, critical: false, blocked: false, archived: false, columnId: 'col-x', ...extra,
})

beforeEach(() => { document.body.innerHTML = ''; EventBus.emit('config:language-changed') })
afterEach(() => { I18n.setLang('de'); EventBus.emit('config:language-changed'); document.body.innerHTML = '' })

describe('F-1555: Verlaufseintrag eines Vorschlags', () => {
  const eintrag = { id: 'e1', author: 'agent:recherche', action: 'vorschlag',
    note: 'angelegt über den Vorschlags-Zugang „recherche“', timestamp: '2026-10-04T10:00:00.000Z' }
  const label = () => {
    EventBus.emit('card:edit', karte('a', { activity: [eintrag] }))
    return document.querySelector('.modal-overlay #history-list .activity-label').textContent
  }

  it('DE: übersetztes Label statt Rohtext „vorschlag: …“', () => {
    I18n.setLang('de')
    const t = label()
    expect(t).toBe(I18n.t('history.action.vorschlag') + ' ' + eintrag.note)
    expect(t.startsWith('Vorschlag:')).toBe(true)
  })

  it('EN: englisches Label, und es unterscheidet sich vom deutschen', () => {
    I18n.setLang('en')
    const en = label()
    expect(en.startsWith('Proposal:')).toBe(true)
    I18n.setLang('de')
    expect(I18n.t('history.action.vorschlag')).not.toBe('Proposal:')
  })
})

describe('F-1548: Finding aus dem Vorlagen-Dropdown', () => {
  function rendern() {
    document.body.innerHTML = '<div id="board-header"></div><div id="board-columns"></div>'
    const board = { id: 'b', title: 'B', columnDefs: [{ id: 'col-x', title: 'X' }],
      templates: [{ id: 't1', title: 'Vorlage Eins', description: 'D', labels: [], owner: '', complexity: '', effort: null }],
      sprints: [{ id: 's1', title: 'S1', cards: [] }] }
    State.set({ version: 1, board })
    Renderer.renderBoard({ board })
    return document.body
  }

  it('Dropdown hat „Finding“; Auswahl → Formular nur mit Finding-Knopf, Enter legt ein Finding an', () => {
    const d = rendern()
    d.querySelector('.btn-add-card').click()
    const item = d.querySelector('.template-dropdown-finding')
    expect(item).toBeTruthy()
    const spy = vi.fn(); EventBus.on('card:quick-add', spy)
    try {
      item.click()
      expect(d.querySelector('.card-add-actions .btn-primary')).toBeNull()
      const inp = d.querySelector('.card-add-input')
      inp.value = 'Neu'
      inp.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0]).toMatchObject({ sprintId: 's1', colId: 'col-x', title: 'Neu', finding: true })
    } finally { EventBus.off('card:quick-add', spy) }
  })

  it('Eintrag „Leer“ bleibt eine normale Karte (finding:false) mit Hinzufügen-Knopf', () => {
    const d = rendern()
    d.querySelector('.btn-add-card').click()
    d.querySelector('.template-dropdown-item').click()   /* erster Eintrag = Leer */
    const spy = vi.fn(); EventBus.on('card:quick-add', spy)
    try {
      d.querySelector('.card-add-input').value = 'Neu'
      d.querySelector('.card-add-actions .btn-primary').click()
      expect(spy.mock.calls[0][0].finding).toBe(false)
    } finally { EventBus.off('card:quick-add', spy) }
  })
})
