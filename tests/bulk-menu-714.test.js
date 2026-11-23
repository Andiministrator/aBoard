/* @vitest-environment happy-dom */

/**
 * Bulk-Menüs: Escape bei Fokus außerhalb des Menüs (F-1785) und Bulk-Auswahl
 * beim Boardwechsel (F-1786), Karte #714. Fokus auf `body` ist der Normalfall
 * in Safari/Firefox auf macOS und nach einem Klick auf eine Überschrift im
 * Menü (Fehlerklasse 149) — die Tests prüfen genau diesen Fall.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { EventBus }   from '../src/eventbus.js'
import { State }      from '../src/state.js'
import { Renderer }   from '../src/renderer.js'
import { I18n }       from '../src/i18n.js'
import { BulkSelect } from '../src/bulk-select.js'
import { Shortcuts }  from '../src/shortcuts.js'

const karte = (o = {}) => ({ id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: ['x'], owner: '', complexity: '', effort: null,
  qa: false, critical: false, blocked: false, archived: false, columnId: 'col-a', assignees: [], ...o })
const board = (id, col) => ({ id, title: id, identities: [], columnDefs: [{ id: col, title: 'Spalte-' + id }],
  sprints: [{ id: 's1', title: 'S', cards: [karte({ columnId: col })] }] })
const warte = () => new Promise(r => setTimeout(r, 0))
const dropdown = key => [...document.querySelectorAll('#bulk-action-bar .bulk-dropdown')]
  .find(d => d.querySelector('button')?.textContent === I18n.t(key))
const offen = m => m.classList.contains('bulk-dropdown-menu--open')
const esc = () => {
  const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
  document.body.dispatchEvent(ev)
  return ev
}

/* Shortcuts.init() hängt den globalen Handler modulglobal ans document; nur einmal
   initialisieren, damit kein Test von der Reihenfolge abhängt (F-1784). */
let init = false
beforeEach(async () => {
  [...document.body.children].forEach(c => { if (!c.id?.startsWith('bulk-')) c.remove() })
  document.body.insertAdjacentHTML('afterbegin', '<div id="board-header"></div><div id="board-columns"></div>')
  if (!init) { Shortcuts.init(); init = true }
  const b = board('b1', 'col-a')
  State.set({ board: b })
  Renderer.renderBoard({ board: b })
  BulkSelect.clearAll()
  BulkSelect.toggle('c1')
})
afterEach(() => {
  /* Offene Menüs des Tests schließen (ihr Capture-Handler hängt sonst am document). */
  document.querySelectorAll('#bulk-action-bar .bulk-dropdown-menu--open')
    .forEach(m => m.previousElementSibling.click())
  BulkSelect.clearAll()
})

async function oeffne(key) {
  const dd = dropdown(key), btn = dd.querySelector('button'), m = dd.querySelector('.bulk-dropdown-menu')
  btn.click(); await warte()
  expect(offen(m)).toBe(true)
  return { btn, m }
}

describe('F-1785 — Escape bei offenem Bulk-Menü, Fokus auf body', () => {
  for (const key of ['bulk.move', 'bulk.owner', 'bulk.label-add', 'bulk.label-remove']) {
    it(`${key}: Escape schließt das Menü, die Auswahl bleibt, der Auslöser hat den Fokus`, async () => {
      const { btn, m } = await oeffne(key)
      /* Klick auf eine Überschrift im Menü: pointerdown innen schließt nicht, der Fokus fällt auf body. */
      m.dispatchEvent(new Event('pointerdown', { bubbles: true }))
      document.activeElement?.blur?.()
      expect(document.activeElement).toBe(document.body)
      const ev = esc()
      expect(offen(m)).toBe(false)
      expect(BulkSelect.count()).toBe(1)
      expect(ev.defaultPrevented).toBe(true)
      expect(document.activeElement).toBe(btn)
    })
  }

  it('Menü geschlossen: Escape hebt die Auswahl weiter auf (ADR-0035), der Capture-Handler ist abgemeldet', async () => {
    const { btn } = await oeffne('bulk.move')
    btn.click() /* schließen */
    esc()
    expect(BulkSelect.count()).toBe(0)
  })
})

describe('Sprint-85-Kritiker — ein Dialog über dem offenen Menü bekommt sein Escape', () => {
  it('Hilfe-Overlay (?) über offenem Bulk-Menü, Fokus auf body: Escape schließt das Overlay', async () => {
    const { m } = await oeffne('bulk.move')
    document.activeElement?.blur?.()
    document.body.dispatchEvent(new KeyboardEvent('keydown', { key: '?', bubbles: true, cancelable: true }))
    const ov = document.querySelector('.shortcuts-overlay')
    expect(ov).not.toBe(null)
    ov.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(document.querySelector('.shortcuts-overlay')).toBe(null)
    /* Das Menü bleibt: das nächste Escape gehört wieder ihm, die Auswahl bleibt stehen. */
    expect(offen(m)).toBe(true)
    esc()
    expect(offen(m)).toBe(false)
    expect(BulkSelect.count()).toBe(1)
  })

  it('Fokus in einem Feld außerhalb des Menüs (Suche): Escape gehört dem Feld, nicht dem Menü', async () => {
    const { m } = await oeffne('bulk.move')
    /* Szenario des Kritikers: Fokus stand auf body (Safari/Firefox), „/“ setzt ihn ins
       Suchfeld — kein focusout aus dem Menü, es bleibt offen. */
    document.activeElement?.blur?.()
    const feld = document.createElement('input'); document.body.appendChild(feld); feld.focus()
    expect(offen(m)).toBe(true)
    let feldSah = 0
    feld.addEventListener('keydown', e => { if (e.key === 'Escape') feldSah++ })
    const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    feld.dispatchEvent(ev)
    expect(feldSah).toBe(1)
    expect(document.activeElement).toBe(feld) /* der Fokus wird nicht zum Auslöser gezogen */
    feld.remove()
    void m
  })

  it('Fokus auf einem Knopf außerhalb: Escape schließt das Menü weiter', async () => {
    const { m } = await oeffne('bulk.move')
    const knopf = document.createElement('button'); document.body.appendChild(knopf); knopf.focus()
    esc()
    expect(offen(m)).toBe(false)
    expect(BulkSelect.count()).toBe(1)
    knopf.remove()
  })

  it('ein geschlossener Dialog (aria-hidden) nimmt dem Menü das Escape nicht weg', async () => {
    const zu = document.createElement('div')
    zu.setAttribute('aria-hidden', 'true')
    zu.innerHTML = '<div role="dialog" aria-modal="true"></div>'
    document.body.appendChild(zu)
    const { m } = await oeffne('bulk.move')
    document.activeElement?.blur?.()
    esc()
    expect(offen(m)).toBe(false)
    expect(BulkSelect.count()).toBe(1)
    zu.remove()
  })
})

describe('F-1786 — Boardwechsel bei aktiver Auswahl und offenem Menü', () => {
  it('board:loaded leert die Auswahl, versteckt die Leiste und schließt das Menü', async () => {
    const { m } = await oeffne('bulk.move')
    const bar = document.getElementById('bulk-action-bar')
    expect(bar.classList.contains('bulk-action-bar--hidden')).toBe(false)
    EventBus.emit('board:loaded') /* sendet main.js nach jedem Boardwechsel */
    expect(BulkSelect.count()).toBe(0)
    expect(bar.classList.contains('bulk-action-bar--hidden')).toBe(true)
    expect(document.getElementById('bulk-action-count').textContent).toBe('')
    expect(offen(m)).toBe(false)
    expect(m.hasAttribute('inert')).toBe(true)
  })

  it('Auswahl auf null (Escape bei geschlossenem Menü) schließt auch ein offenes Nachbar-Menü nicht fälschlich offen', async () => {
    const { m } = await oeffne('bulk.owner')
    BulkSelect.clearAll()
    expect(offen(m)).toBe(false)
  })
})
