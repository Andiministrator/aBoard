/* @vitest-environment happy-dom */
/**
 * Verify-Nacharbeit Karten #698/#702 (Befunde VO-1 … VO-12 des Opus-Verify über
 * 09266749): Bulk-Menüs (Aufbau nur beim Öffnen, geschlossen nicht bedienbar),
 * Wachen im Modal, Owner-Gruppierung, Cache- und Leerzustand-Regeln.
 * Die Bulk-Tests laufen in JEDEM Kontext (auch standalone/slim) — die Menüs
 * gibt es überall.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn((k) => (k === 'userName' ? 'TestUser' : (k === 'boardMode' ? 'standalone' : ''))), set: vi.fn() },
}))

import { EventBus }       from '../src/eventbus.js'
import { Modal }          from '../src/modal.js' // eslint-disable-line no-unused-vars -- registriert die EventBus-Handler
import { State }          from '../src/state.js'
import { Profile }        from '../src/profile.js'
import { Renderer }       from '../src/renderer.js'
import { I18n }           from '../src/i18n.js'
import { BulkSelect }     from '../src/bulk-select.js'
import { StoragePlugin }  from '../src/storage-plugin.js'
import { vergesseKandidatenCache } from '../src/bearbeiter-kandidaten.js'

const card = (o = {}) => ({
  id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: ['x'], owner: '',
  complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'col-backlog', assignees: [], ...o,
})
const board = (cards = [card()]) => ({
  id: 'b1', title: 'B', identities: [], columnDefs: [{ id: 'col-backlog', title: 'Backlog' }],
  sprints: [{ id: 's1', title: 'S', cards }],
})
const werte = (sel) => [...sel.options].map(o => o.value)
const warte = () => new Promise(r => setTimeout(r, 0))
const raf = () => new Promise(r => requestAnimationFrame(() => r()))
const raeumeAuf = () => [...document.body.children].forEach(c => { if (!c.id?.startsWith('bulk-')) c.remove() })
beforeEach(() => { vergesseKandidatenCache(); EventBus.emit('config:language-changed'); raeumeAuf() })
afterEach(() => { vi.restoreAllMocks(); EventBus.emit('config:language-changed'); raeumeAuf() })

/* ── VO-1 / VO-2 — Bulk-Menüs ─────────────────────────────────────── */
describe('VO-1/VO-2 — Bulk-Menüs: Aufbau nur beim Öffnen, geschlossen nicht bedienbar', () => {
  function dropdown(schluessel) {
    return [...document.querySelectorAll('#bulk-action-bar .bulk-dropdown')]
      .find(d => d.querySelector('button')?.textContent === I18n.t(schluessel))
  }
  beforeEach(() => {
    document.body.insertAdjacentHTML('afterbegin', '<div id="board-header"></div><div id="board-columns"></div>')
    const b = board()
    State.set({ board: b })
    Renderer.renderBoard({ board: b })
    BulkSelect.toggle?.('c1')
  })
  const alle = [['bulk.move', 'Verschieben'], ['bulk.label-add', 'Label hinzufügen'], ['bulk.label-remove', 'Label entfernen']]

  for (const [key, name] of alle) {
    it(`„${name}“: nach dem Öffnen befüllt und bedienbar, geschlossen leer-inert (inert)`, async () => {
      const dd = dropdown(key)
      const btn = dd.querySelector('button'), menu = dd.querySelector('.bulk-dropdown-menu')
      expect(menu.hasAttribute('inert')).toBe(true) /* zu Beginn geschlossen */
      btn.click(); await raf(); await raf()
      expect(menu.classList.contains('bulk-dropdown-menu--open')).toBe(true)
      expect(menu.children.length).toBeGreaterThan(0) /* Gegenprobe des Einhänge-Fixes: sonst leer */
      expect(menu.hasAttribute('inert')).toBe(false)
      btn.click(); await raf(); await raf()
      expect(menu.classList.contains('bulk-dropdown-menu--open')).toBe(false)
      expect(menu.hasAttribute('inert')).toBe(true)
    })
  }

  it('Label hinzufügen: Schließen per Button baut nicht neu auf und fokussiert das unsichtbare Feld nicht', async () => {
    const dd = dropdown('bulk.label-add')
    const btn = dd.querySelector('button'), menu = dd.querySelector('.bulk-dropdown-menu')
    btn.click(); await raf(); await raf()
    const eingabeOffen = menu.querySelector('.bulk-dropdown-input')
    expect(eingabeOffen).not.toBeNull()
    btn.click(); await raf(); await raf()
    /* Schließ-Klick darf das Feld weder ersetzen noch fokussieren */
    expect(menu.querySelector('.bulk-dropdown-input')).toBe(eingabeOffen)
    expect(menu.contains(document.activeElement)).toBe(false)
  })

  it('Label hinzufügen: öffnen und sofort wieder schließen → der verspätete Fokus-Rahmen greift nicht', async () => {
    const dd = dropdown('bulk.label-add')
    const btn = dd.querySelector('button'), menu = dd.querySelector('.bulk-dropdown-menu')
    btn.click(); btn.click() /* synchron: der rAF des Öffnens läuft erst danach */
    await raf(); await raf()
    expect(menu.classList.contains('bulk-dropdown-menu--open')).toBe(false)
    expect(menu.contains(document.activeElement)).toBe(false)
  })

  it('Label hinzufügen: beim Öffnen steht der Fokus im Feld', async () => {
    const dd = dropdown('bulk.label-add')
    dd.querySelector('button').click(); await raf(); await raf()
    expect(document.activeElement?.classList.contains('bulk-dropdown-input')).toBe(true)
  })
})

/* ── VO-3 / VO-4 / VO-10 / VO-12 — Modal ──────────────────────────── */
const anna = { getCurrentUser: async () => ({ name: 'Anna', email: 'anna@f.de' }), mitgliederNamen: async () => ['Anna', 'Bea'] }
function serverModus(plugin) {
  vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
  vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue(plugin)
}
function open(c, b) {
  State.set({ board: b })
  EventBus.emit('card:edit', c)
  return document.querySelector('.modal-overlay')
}
const schliesse = () => Modal.close() /* setzt currentCard = null */

describeServer('VO-3 — Wache gen === bearbeiterGen && currentCard.id === card.id: jede Hälfte einzeln', () => {
  it('nur `gen` schützt: A öffnen, schließen, A erneut öffnen, dann kommt die ERSTE Antwort', async () => {
    const tore = []
    serverModus({
      getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }),
      mitgliederNamen: () => new Promise(r => tore.push(r)),
    })
    const b = board()
    open(card({ id: 'A' }), b); await warte()
    schliesse()
    vergesseKandidatenCache()
    const ov = open(card({ id: 'A' }), b); await warte()
    expect(tore.length).toBe(2)
    const vorher = werte(ov.querySelector('#modal-owner'))
    tore[0](['Anna', 'Erste-Antwort']); await warte(); await warte()
    expect(werte(ov.querySelector('#modal-owner'))).toEqual(vorher) /* veraltete Antwort verworfen */
    tore[1](['Anna', 'Zweite']); await warte(); await warte()
    expect(werte(ov.querySelector('#modal-owner'))).toContain('Zweite')
  })
  it('nur `currentCard` schützt: Karte schließen (gen bleibt gleich), dann kommt die Antwort', async () => {
    const tore = []
    serverModus({
      getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }),
      mitgliederNamen: () => new Promise(r => tore.push(r)),
    })
    const ov = open(card({ id: 'A', owner: 'Anna' }), board()); await warte()
    schliesse() /* setzt currentCard = null, zählt gen nicht hoch */
    const vorher = werte(ov.querySelector('#modal-owner'))
    tore[0](['Anna', 'Nur-Spaet']); await warte(); await warte()
    expect(werte(ov.querySelector('#modal-owner'))).toEqual(vorher)
  })
})

describeServer('VO-4 — Nicht-Server-Pfad von open() verwirft späte Server-Antworten', () => {
  it('Server-Öffnung, dann Nicht-Server-Öffnung derselben Karte, dann kommt die späte Antwort', async () => {
    let frei
    const gate = new Promise(r => { frei = r })
    const sp = vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue({
      getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }),
      mitgliederNamen: async () => { await gate; return ['Anna', 'Spaet'] },
    })
    const b = board()
    const c = card({ id: 'A', owner: 'Claude Opus' })
    open(c, b); await warte()
    schliesse()
    sp.mockImplementation(() => false)
    const ov = open(c, b)
    const vorher = werte(ov.querySelector('#modal-owner'))
    expect(vorher).toContain('Claude Haiku')
    frei(); await warte(); await warte()
    expect(werte(ov.querySelector('#modal-owner'))).toEqual(vorher)
    expect(ov.querySelector('#modal-owner').dataset.bearbeiterServer).toBeUndefined()
  })
})

describeServer('VO-10 — Modal gruppiert versionierte Claude-Owner in die Claude-optgroup', () => {
  it('„Claude Opus 5 (1M context)“ steht im optgroup, nicht ungruppiert', async () => {
    serverModus(anna)
    const b = board([card({ id: 'x', owner: 'Claude Opus' })])
    const ov = open(card({ id: 'y', owner: 'Claude Opus 5 (1M context)' }), b)
    await warte(); await warte()
    const sel = ov.querySelector('#modal-owner')
    const optgroup = sel.querySelector('optgroup')
    expect(optgroup).not.toBeNull()
    expect([...optgroup.querySelectorAll('option')].map(o => o.value)).toContain('Claude Opus 5 (1M context)')
    expect([...sel.children].filter(n => n.tagName === 'OPTION').map(o => o.value)).not.toContain('Claude Opus 5 (1M context)')
    expect(sel.value).toBe('Claude Opus 5 (1M context)')
  })
  it('auch ohne Claude-Karten auf dem Board: der Kartenwert steht in der Gruppe', async () => {
    serverModus(anna)
    const ov = open(card({ id: 'y', owner: 'Claude Opus 5' }), board([]))
    await warte(); await warte()
    const optgroup = ov.querySelector('#modal-owner optgroup')
    expect([...optgroup.querySelectorAll('option')].map(o => o.value)).toContain('Claude Opus 5')
  })
})

describe('VO-12 — Nicht-Server-Pfad: Kartenowner außerhalb der festen Liste bleibt Option', () => {
  it('„Anna“ und „Claude Opus 5“ gehen beim Öffnen nicht verloren; Zusatzoption wandert nicht mit', () => {
    vi.spyOn(Profile, 'has').mockImplementation(() => false)
    const b = board([])
    let ov = open(card({ id: 'a', owner: 'Anna' }), b)
    let sel = ov.querySelector('#modal-owner')
    expect(werte(sel)).toContain('Anna')
    expect(sel.value).toBe('Anna')
    schliesse()
    ov = open(card({ id: 'b', owner: 'Claude Opus 5' }), b)
    sel = ov.querySelector('#modal-owner')
    expect(sel.value).toBe('Claude Opus 5')
    expect(werte(sel)).not.toContain('Anna')
    schliesse()
    ov = open(card({ id: 'c', owner: 'Claude Haiku' }), b)
    sel = ov.querySelector('#modal-owner')
    expect(werte(sel).filter(v => v === 'Claude Haiku').length).toBe(1)
    expect(werte(sel)).not.toContain('Claude Opus 5')
  })
})
