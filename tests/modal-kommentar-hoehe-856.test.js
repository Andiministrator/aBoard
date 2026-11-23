/* @vitest-environment happy-dom */

/**
 * Karte #856 — Kommentar-Tab nutzt die volle Modal-Höhe.
 * Geprüft wird das Verhalten (Klasse am Modal, Autogrow des Eingabefelds,
 * Scrollen ans Listenende); CSS-Höhen sind in happy-dom nicht messbar.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn((k) => (k === 'userName' ? 'TestUser' : (k === 'boardMode' ? 'standalone' : ''))), set: vi.fn() },
}))

import { EventBus } from '../src/eventbus.js'
import { I18n }     from '../src/i18n.js'
import { Modal }    from '../src/modal.js'

void Modal

const karte = (o = {}) => ({
  id: 'c-856', displayId: 856, title: 'Karte', description: '', agentContext: '',
  comments: [], attachments: [], links: [], activity: [], priority: 'mittel', labels: [],
  owner: '', complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'col-backlog', ...o,
})

function oeffne(c = karte()) {
  EventBus.emit('card:edit', c)
  return document.querySelector('.modal-overlay')
}
const tab = (o, k) => o.querySelector(`.tab-btn[data-tab="${k}"]`).click()
const modal = o => o.querySelector('.modal')

beforeEach(() => {
  vi.useFakeTimers()
  try { localStorage.clear() } catch {}
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
})
afterEach(() => { EventBus.emit('config:language-changed'); document.body.innerHTML = ''; vi.useRealTimers() })

describe('#856 — Modal-Klasse beim Kommentar-Tab', () => {
  it('Klasse nur bei aktivem Kommentar-Tab', () => {
    const o = oeffne()
    expect(modal(o).classList.contains('modal--kommentare')).toBe(false)
    tab(o, 'comments')
    expect(modal(o).classList.contains('modal--kommentare')).toBe(true)
    tab(o, 'links')
    expect(modal(o).classList.contains('modal--kommentare')).toBe(false)
    tab(o, 'comments')
    tab(o, 'desc')
    expect(modal(o).classList.contains('modal--kommentare')).toBe(false)
  })

  it('Schliessen und Wiederöffnen entfernt die Klasse', () => {
    const o = oeffne()
    tab(o, 'comments')
    o.querySelector('.btn-modal-close').click()
    expect(modal(o).classList.contains('modal--kommentare')).toBe(false)
    const o2 = oeffne()
    expect(modal(o2).classList.contains('modal--kommentare')).toBe(false)
  })

  it('Wechsel in den Kommentar-Tab scrollt ans Listenende', () => {
    const o = oeffne(karte({ comments: [{ id: 'k1', author: 'A', text: 'x', created: '2026-10-21' }] }))
    const liste = o.querySelector('#comments-list')
    Object.defineProperty(liste, 'scrollHeight', { value: 777, configurable: true })
    liste.scrollTop = 0
    tab(o, 'comments')
    expect(liste.scrollTop).toBe(777)
  })
})

describe('#856 — Autogrow des Kommentar-Eingabefelds', () => {
  it('Höhe folgt scrollHeight, gedeckelt auf 40 % der Viewport-Höhe', () => {
    const o = oeffne()
    tab(o, 'comments')
    const ta = o.querySelector('#comment-input')
    window.innerHeight = 1000
    Object.defineProperty(ta, 'scrollHeight', { value: 150, configurable: true })
    ta.value = 'a\nb\nc'
    ta.dispatchEvent(new Event('input'))
    expect(ta.style.height).toBe('150px')
    Object.defineProperty(ta, 'scrollHeight', { value: 900, configurable: true })
    ta.dispatchEvent(new Event('input'))
    expect(ta.style.height).toBe('400px')
    expect(ta.style.overflowY).not.toBe('hidden')
  })

  it('nach dem Absenden zurück auf Ausgangshöhe', () => {
    const o = oeffne()
    tab(o, 'comments')
    const ta = o.querySelector('#comment-input')
    Object.defineProperty(ta, 'scrollHeight', { value: 150, configurable: true })
    ta.value = 'Text'
    ta.dispatchEvent(new Event('input'))
    expect(ta.style.height).toBe('150px')
    o.querySelector('.comment-form .btn-primary').click()
    expect(ta.value).toBe('')
    expect(ta.style.height).toBe('')
  })
})

describe('#863 (A-2) — Autogrow bei verstecktem Panel und verkleinertem Fenster', () => {
  it('Entwurf bei verstecktem Kommentar-Panel wird nicht gemessen; der Tab-Wechsel misst neu', () => {
    const o = oeffne()
    const ta = o.querySelector('#comment-input')
    window.innerHeight = 1000
    Object.defineProperty(ta, 'scrollHeight', { value: 0, configurable: true }) // verstecktes Panel misst 0
    ta.value = 'a\nb\nc'
    ta.dispatchEvent(new Event('input'))
    expect(ta.style.height).toBe('') // nicht auf 0/34 px festgeschrieben
    Object.defineProperty(ta, 'scrollHeight', { value: 150, configurable: true })
    tab(o, 'comments')
    expect(ta.style.height).toBe('150px')
  })

  it('JS setzt nie Inline-overflow „hidden“ (CSS-auto regiert, Verify V-4)', () => {
    const o = oeffne()
    tab(o, 'comments')
    const ta = o.querySelector('#comment-input')
    window.innerHeight = 1000
    Object.defineProperty(ta, 'scrollHeight', { value: 150, configurable: true })
    ta.value = 'x'
    ta.dispatchEvent(new Event('input'))
    expect(ta.style.overflowY).not.toBe('hidden')
    Object.defineProperty(ta, 'scrollHeight', { value: 500, configurable: true })
    ta.dispatchEvent(new Event('input'))
    expect(ta.style.overflowY).not.toBe('hidden')
  })

  it('CSS-Grundwert von .comment-input ist overflow-y:auto (JS-hidden darf bei kleinerem Fenster nicht allein stehen)', () => {
    const css = readFileSync(join(process.cwd(), 'src', 'style.css'), 'utf8')
    const block = css.match(/\n\.comment-input \{([^}]*)\}/)[1]
    expect(block).toMatch(/overflow-y:\s*auto/)
    expect(block).not.toMatch(/overflow-y:\s*hidden/)
  })
})

describe('#863 (W-1) — Autogrow rechnet den Rahmen ein (border-box)', () => {
  it('Höhe = scrollHeight + (offsetHeight − clientHeight), sonst Scrollbalken trotz Platz', () => {
    const o = oeffne()
    tab(o, 'comments')
    const ta = o.querySelector('#comment-input')
    window.innerHeight = 1000
    Object.defineProperty(ta, 'scrollHeight', { value: 100, configurable: true })
    Object.defineProperty(ta, 'offsetHeight', { value: 52, configurable: true })
    Object.defineProperty(ta, 'clientHeight', { value: 50, configurable: true })
    ta.value = 'a\nb'
    ta.dispatchEvent(new Event('input'))
    expect(ta.style.height).toBe('102px')
  })
})
