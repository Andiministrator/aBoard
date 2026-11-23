/* @vitest-environment happy-dom */

/**
 * Bulk-Menüs, Karte #715: Pfeiltasten (F-1773), ARIA-Zustände und Rollen (F-1790),
 * Owner-Menü mit konsistenten Einträgen unabhängig vom Fokus (F-1787).
 * Alle vier Menüs (bulk.move, bulk.owner, bulk.label-add, bulk.label-remove), auch
 * im geschlossenen Zustand (Klasse 141).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'
import { State }      from '../src/state.js'
import { Profile }    from '../src/profile.js'
import { Renderer }   from '../src/renderer.js'
import { I18n }       from '../src/i18n.js'
import { BulkSelect } from '../src/bulk-select.js'
import { StoragePlugin } from '../src/storage-plugin.js'
import { vergesseKandidatenCache } from '../src/bearbeiter-kandidaten.js'

const KEYS = ['bulk.move', 'bulk.owner', 'bulk.label-add', 'bulk.label-remove']
const karte = (o = {}) => ({ id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '',
  comments: [{ id: 'k', author: 'Ehemalig', text: 'x', created: '2026-01-01' }],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: ['x', 'y'], owner: 'Zora', complexity: '',
  effort: null, qa: false, critical: false, blocked: false, archived: false, columnId: 'col-a', assignees: [], ...o })
const board = () => ({ id: 'b1', title: 'B', identities: [],
  columnDefs: [{ id: 'col-a', title: 'A' }, { id: 'col-b', title: 'B' }],
  sprints: [{ id: 's1', title: 'S', cards: [karte()] }] })
const warte = () => new Promise(r => setTimeout(r, 0))
const dropdown = key => [...document.querySelectorAll('#bulk-action-bar .bulk-dropdown')]
  .find(d => d.querySelector('button')?.textContent === I18n.t(key))
const teile = key => { const d = dropdown(key); return { btn: d.querySelector('button'), m: d.querySelector('.bulk-dropdown-menu'), d } }
const offen = m => m.classList.contains('bulk-dropdown-menu--open')
const taste = (ziel, key) => {
  const ev = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
  ziel.dispatchEvent(ev); return ev
}
const punkte = m => [...m.querySelectorAll('input,button')]

beforeEach(() => {
  vergesseKandidatenCache()
  ;[...document.body.children].forEach(c => { if (!c.id?.startsWith('bulk-')) c.remove() })
  document.body.insertAdjacentHTML('afterbegin', '<div id="board-header"></div><div id="board-columns"></div>')
  const b = board(); State.set({ board: b }); Renderer.renderBoard({ board: b })
  BulkSelect.clearAll(); BulkSelect.toggle('c1')
})
afterEach(() => {
  document.querySelectorAll('#bulk-action-bar .bulk-dropdown-menu--open').forEach(m => m.previousElementSibling.click())
  BulkSelect.clearAll()
  vi.restoreAllMocks()
})

describe('F-1790 — ARIA-Zustände und Rollen (alle vier Menüs)', () => {
  it.each(KEYS)('%s: geschlossen aria-expanded=false, haspopup und Rolle gesetzt', key => {
    const { btn, m } = teile(key)
    /* Menü mit Eingabefeld: Disclosure-Muster, kein aria-haspopup (Sprint-85-Kritiker) */
    expect(btn.getAttribute('aria-haspopup')).toBe(key === 'bulk.label-add' ? null : 'menu')
    expect(btn.getAttribute('aria-expanded')).toBe('false')
    expect(m.getAttribute('role')).toBeTruthy()
    expect(m.getAttribute('aria-label')).toBe(I18n.t(key))
  })

  it('Menü mit Eingabefeld ist kein role=menu, die anderen schon', () => {
    expect(teile('bulk.label-add').m.getAttribute('role')).not.toBe('menu')
    /* role=group verträgt kein aria-haspopup="dialog" (Wert muss zur Popup-Rolle passen) */
    expect(teile('bulk.label-add').btn.hasAttribute('aria-haspopup')).toBe(false)
    for (const k of ['bulk.move', 'bulk.owner', 'bulk.label-remove']) {
      expect(teile(k).m.getAttribute('role')).toBe('menu')
      expect(teile(k).btn.getAttribute('aria-haspopup')).toBe('menu')
    }
  })

  it.each(['bulk.move', 'bulk.label-remove'])('%s: jedes Kind im role=menu ist menuitem oder presentation (Sitzungs-QA Sprint 85)', async key => {
    const { btn, m } = teile(key)
    btn.click(); await warte()
    const rollen = [...m.children].map(k => k.getAttribute('role'))
    expect(rollen.length).toBeGreaterThan(0)
    rollen.forEach(r => expect(['menuitem', 'presentation']).toContain(r))
    if (key === 'bulk.move') expect(rollen.filter(r => r === 'presentation').length).toBeGreaterThan(0)
  })

  it.each(['bulk.move', 'bulk.owner', 'bulk.label-remove'])('%s: Einträge tragen role=menuitem', async key => {
    const { btn, m } = teile(key)
    btn.click(); await warte()
    const bs = [...m.querySelectorAll('button')]
    expect(bs.length).toBeGreaterThan(0)
    bs.forEach(b => expect(b.getAttribute('role')).toBe('menuitem'))
  })

  describe.each(KEYS)('%s: aria-expanded folgt jedem Schließweg', key => {
    const oeffnen = async () => { const t = teile(key); t.btn.click(); await warte()
      expect(t.btn.getAttribute('aria-expanded')).toBe('true'); return t }
    it('Umschalt-Klick', async () => {
      const { btn } = await oeffnen(); btn.click()
      expect(btn.getAttribute('aria-expanded')).toBe('false')
    })
    it('Escape', async () => {
      const { btn } = await oeffnen()
      document.body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      expect(btn.getAttribute('aria-expanded')).toBe('false')
    })
    it('Aussenklick', async () => {
      const { btn } = await oeffnen()
      document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
      expect(btn.getAttribute('aria-expanded')).toBe('false')
    })
    it('Tab-Verlassen', async () => {
      const { btn, d } = await oeffnen()
      const draussen = document.createElement('button'); document.body.appendChild(draussen)
      punkte(d)[1]?.focus?.(); btn.focus()
      btn.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: draussen }))
      expect(btn.getAttribute('aria-expanded')).toBe('false')
    })
    it('Leerung der Auswahl', async () => {
      const { btn } = await oeffnen()
      BulkSelect.clearAll()
      expect(btn.getAttribute('aria-expanded')).toBe('false')
    })
  })
})

describe('F-1773 — Pfeiltasten (alle vier Menüs)', () => {
  it.each(KEYS)('%s: Pfeil runter am geschlossenen Auslöser öffnet und fokussiert den ersten Punkt', async key => {
    const { btn, m } = teile(key)
    btn.focus()
    const ev = taste(btn, 'ArrowDown'); await warte()
    expect(ev.defaultPrevented).toBe(true)
    expect(offen(m)).toBe(true)
    await new Promise(r => setTimeout(r, 30)) /* requestAnimationFrame des Label-Felds */
    const p = punkte(m)
    if (!p.length) return /* label-remove ohne Labels hat keinen Punkt */
    expect(document.activeElement).toBe(p[0])
  })

  it.each(['bulk.move', 'bulk.owner', 'bulk.label-add', 'bulk.label-remove'])('%s: runter/hoch mit Umlauf', async key => {
    const { btn, m } = teile(key)
    btn.focus(); taste(btn, 'ArrowDown'); await new Promise(r => setTimeout(r, 30))
    const p = punkte(m)
    expect(p.length).toBeGreaterThan(1)
    p[0].focus()
    taste(p[0], 'ArrowDown'); expect(document.activeElement).toBe(p[1])
    taste(p[1], 'ArrowUp');   expect(document.activeElement).toBe(p[0])
    taste(p[0], 'ArrowUp');   expect(document.activeElement).toBe(p[p.length - 1])
    taste(p[p.length - 1], 'ArrowDown'); expect(document.activeElement).toBe(p[0])
  })

  it('Pfeil hoch am geschlossenen Auslöser tut nichts; Pfeil im geschlossenen Menü öffnet nicht', () => {
    const { btn, m } = teile('bulk.move')
    const ev = taste(btn, 'ArrowUp')
    expect(ev.defaultPrevented).toBe(false)
    expect(offen(m)).toBe(false)
  })

  it('Label-Feld ist ein Punkt der Navigation; Pfeil im Feld wandert zum ersten Label', async () => {
    const { btn, m } = teile('bulk.label-add')
    btn.click(); await new Promise(r => setTimeout(r, 30))
    const inp = m.querySelector('input')
    expect(document.activeElement).toBe(inp)
    taste(inp, 'ArrowDown')
    expect(document.activeElement).toBe(m.querySelector('button'))
  })
})

describeServer('F-1787 — Owner-Menü: gleiche Einträge unabhängig vom Fokus', () => {
  async function lauf(fokusImMenue) {
    vergesseKandidatenCache()
    let frei; const gate = new Promise(r => { frei = r })
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue({
      getCurrentUser: async () => { await gate; return { name: 'Anna', email: 'a@f.de' } },
      mitgliederNamen: async () => ['Anna', 'Mia'],
    })
    const b = board(); State.set({ board: b }); Renderer.renderBoard({ board: b })
    BulkSelect.clearAll(); BulkSelect.toggle('c1')
    const { btn, m } = teile('bulk.owner')
    btn.click(); await warte()
    const vor = [...m.querySelectorAll('.bulk-dropdown-item')].map(e => e.textContent)
    let f = null
    if (fokusImMenue) { f = [...m.querySelectorAll('button')].find(e => e.textContent === 'Zora'); f.focus() }
    frei(); for (let i = 0; i < 6; i++) await warte()
    const nach = [...m.querySelectorAll('.bulk-dropdown-item')].map(e => e.textContent)
    const fokusNach = document.activeElement
    btn.click()
    return { vor, nach, f, fokusNach, m }
  }

  it('gleiche Einträge bei Fokus außerhalb und im Menü; ehemaliger Autor fällt in beiden Fällen heraus', async () => {
    const a = await lauf(false)
    const b = await lauf(true)
    expect(a.vor).toContain('Ehemalig')
    expect(a.nach).not.toContain('Ehemalig')
    expect(b.nach.slice().sort()).toEqual(a.nach.slice().sort())
  })

  it('der fokussierte Eintrag bleibt derselbe Knoten und behält den Fokus', async () => {
    const r = await lauf(true)
    expect(r.f.isConnected).toBe(true)
    expect(r.fokusNach).toBe(r.f)
  })

  it('Fokus auf einem Eintrag, der entfällt: Fokus geht auf den Auslöser, Enter leert keinen Owner', async () => {
    vergesseKandidatenCache()
    let frei; const gate = new Promise(r => { frei = r })
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue({
      getCurrentUser: async () => { await gate; return { name: 'Anna', email: 'a@f.de' } },
      mitgliederNamen: async () => ['Anna', 'Mia'],
    })
    const b = board(); State.set({ board: b }); Renderer.renderBoard({ board: b })
    BulkSelect.clearAll(); BulkSelect.toggle('c1')
    const { btn, m } = teile('bulk.owner')
    btn.click(); await warte()
    const weg = [...m.querySelectorAll('button')].find(e => e.textContent === 'Ehemalig'); weg.focus()
    frei(); for (let i = 0; i < 6; i++) await warte()
    expect([...m.querySelectorAll('button')].map(e => e.textContent)).not.toContain('Ehemalig')
    /* Sprint-85-Kritiker: Der erste Eintrag ist „kein Owner“ — ein Sprung dorthin
       ließ das für den Autor gemeinte Enter den Owner aller Karten leeren. */
    expect(document.activeElement).toBe(btn)
    expect(offen(m)).toBe(true)
    btn.click()
  })
})
