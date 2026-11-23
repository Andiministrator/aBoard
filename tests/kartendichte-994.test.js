/* @vitest-environment happy-dom */

/**
 * Karte #994 (F-2709, F-2710, F-2711): Kartendichte „kompakt“ gegen das gerenderte DOM.
 * Die Quelltext-Regexe aus kartendichte-973.test.js prüfen nur den Wortlaut; hier steht die
 * echte Karte im DOM, das ganze src/style.css hängt als <style> im Dokument, und
 * `getComputedStyle` (happy-dom, F-2741: Kaskade samt Spezifität und Reihenfolge geprüft,
 * Gegenprobe mit einer später stehenden, spezifischeren Regel) entscheidet, was verschwindet.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

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

import { State }    from '../src/state.js'
import { Renderer } from '../src/renderer.js'
import { Filter }   from '../src/filter.js'

const css = readFileSync(join(process.cwd(), 'src/style.css'), 'utf8')

/** Alle Regeln des kompakt-Blocks (Kommentar „Karte #973“ bis zur ersten Regel danach). */
const kompaktCss = css.slice(css.indexOf('/* Karte #973: Kartendichte'),
  css.indexOf('.card-title { font-size: 13px'))
const kompaktRegeln = [...kompaktCss.matchAll(/^(\[data-density="kompakt"\][^{]*)\{([^}]*)\}/gms)]
  .map(m => ({ selektor: m[1].trim(), body: m[2] }))

/** Karte mit allen Abzeichen; Owner „Claude Opus“ löst im Renderer einen Avatar auf. */
function vollKarte(extra = {}) {
  return {
    id: 'c-voll', displayId: 7, title: 'Karte mit allem', labels: [], owner: 'Claude Opus',
    priority: 'hoch', complexity: 'mittel', effort: 2, qa: true, critical: true, blocked: false,
    description: '- [x] a\n- [ ] b', comments: [{ id: 'k1', author: 'A', text: 't', created: '2026-10-01' }],
    dueDate: '2026-12-01', recurrence: { interval: 'weekly' }, assignees: ['Andi'],
    links: [{ type: 'related', cardId: 'x' }, { type: 'blocked-by', targetId: 'c-blocker' }],
    columnId: 'col-a', ...extra,
  }
}
/** Offene Karte, die vollKarte() blockiert: der Renderer setzt daraus `card-link-blocked-badge`. */
const blocker = () => ({ id: 'c-blocker', displayId: 8, title: 'Blocker', labels: [], columnId: 'col-a',
  comments: [], links: [] })

function rendere(card, weitere = []) {
  const board = {
    id: 'b', title: 'T', identities: [], columnDefs: [{ id: 'col-a', title: 'Backlog' }],
    sprints: [{ id: 's1', title: 'Sprint 1', cards: [card, ...weitere] }],
  }
  State.set({ board })   /* ohne State.set betritt der Renderer den Avatar-Zweig nie (Klasse 26) */
  const header = document.createElement('div'); header.id = 'board-header'
  const container = document.createElement('div'); container.id = 'board-columns'
  document.body.append(header, container)
  Renderer.renderBoard({ board })
  return container
}

/** F-2741: „ausgeblendet“ = die berechnete Kaskade des echten Stylesheets ergibt display:none. */
function ausgeblendetDurchCss(el) {
  return getComputedStyle(el).display === 'none'
}
const kompakt = () => document.documentElement.setAttribute('data-density', 'kompakt')

beforeEach(() => {
  document.body.innerHTML = ''; Filter.clear(); State.markClean()
  const st = document.createElement('style'); st.id = 'echtes-stylesheet'; st.textContent = css
  document.head.appendChild(st)
})
afterEach(() => {
  document.body.innerHTML = ''; document.documentElement.removeAttribute('data-density')
  document.getElementById('echtes-stylesheet')?.remove()
})

const BLEIBEN = ['card-flag-badge', 'card-date-badge', 'card-qa-badge', 'card-link-blocked-badge', 'card-owner-badge']
const VERSCHWINDEN = ['card-prio-badge', 'card-complexity-badge', 'card-effort-badge', 'card-count-badge',
  'card-link-badge', 'card-recurrence-badge', 'card-checklist-summary', 'card-assignees-cluster']

describe('F-2709 — kompakt gegen das gerenderte DOM', () => {
  it('die Karte trägt alle erwarteten Abzeichen (sonst sind die folgenden Tests leer)', () => {
    const c = rendere(vollKarte(), [blocker()])
    for (const k of [...BLEIBEN, ...VERSCHWINDEN]) {
      expect(c.querySelector(`.card-footer .${k}`), k).toBeTruthy()
    }
    expect(c.querySelector('.card-owner-badge--avatar')).toBeTruthy()
  })

  it('ohne Dichte „kompakt“ ist keines dieser Abzeichen ausgeblendet (die Kaskade trennt wirklich)', () => {
    const c = rendere(vollKarte(), [blocker()])
    for (const k of [...BLEIBEN, ...VERSCHWINDEN]) {
      expect(ausgeblendetDurchCss(c.querySelector(`.card-footer .${k}`)), `${k} normal`).toBe(false)
    }
  })

  it('die kompakt-Regeln lassen genau die Abzeichen der Bleibe-Liste stehen und blenden alle anderen aus', () => {
    kompakt()
    const c = rendere(vollKarte(), [blocker()])
    expect(kompaktRegeln.length).toBeGreaterThan(5)
    for (const k of BLEIBEN) {
      expect(ausgeblendetDurchCss(c.querySelector(`.card-footer .${k}`)), `${k} bleibt`).toBe(false)
    }
    for (const k of VERSCHWINDEN) {
      expect(ausgeblendetDurchCss(c.querySelector(`.card-footer .${k}`)), `${k} weg`).toBe(true)
    }
  })

  it('der Owner-Name (Avatar vorhanden) wird ausgeblendet, das Abzeichen selbst bleibt', () => {
    kompakt()
    const c = rendere(vollKarte())
    expect(c.querySelector('.card-owner-badge--avatar .card-owner-badge-label')).toBeTruthy()
    expect(ausgeblendetDurchCss(c.querySelector('.card-owner-badge-label'))).toBe(true)
    expect(ausgeblendetDurchCss(c.querySelector('.card-owner-badge'))).toBe(false)
  })

  it('jede kompakt-Regel ist ein ausgeschriebener Selektor ohne natives Nesting (F-2711)', () => {
    expect(kompaktCss).not.toMatch(/^\[data-density="kompakt"\]\s*\{/m)
    expect(kompaktCss).not.toMatch(/\{[^}]*(^|[\s;{])&/m)
  })

  it('Cover-Karte in kompakt: der Renderer setzt `card--has-cover` selbst, die kompakt-Regel nimmt die Cover-Höhe zurück', () => {
    const cover = { coverAttachmentId: 'att1', attachments: [{ id: 'att1', mime: 'image/png', thumb: 'AAAA', thumbMime: 'image/jpeg' }] }
    const karteNormal = rendere(vollKarte(cover)).querySelector('.card')
    expect(karteNormal.classList.contains('card--has-cover')).toBe(true)
    expect(getComputedStyle(karteNormal).minHeight).toBe('110px')
    /* happy-dom merkt sich berechnete Werte je Element: für „kompakt“ neu rendern. */
    document.body.innerHTML = ''
    kompakt()
    const karteKompakt = rendere(vollKarte(cover)).querySelector('.card')
    expect(karteKompakt.classList.contains('card--has-cover')).toBe(true)
    expect(['0', '0px']).toContain(getComputedStyle(karteKompakt).minHeight)
  })

  it('Gegenprobe der Kaskade: eine spätere, spezifischere Regel würde ein Abzeichen wieder einblenden', () => {
    kompakt()
    const st = document.createElement('style'); st.id = 'echtes-stylesheet-zusatz'
    st.textContent = '.card .card-footer .card-prio-badge { display: inline-block; }'
    document.head.appendChild(st)
    try {
      const c = rendere(vollKarte())
      expect(ausgeblendetDurchCss(c.querySelector('.card-footer .card-prio-badge'))).toBe(false)
    } finally { st.remove() }
  })
})

describe('F-2710 — Titel-Tooltip nur bei gekürztem Titel', () => {
  function messbar(t, { scrollH, clientH, scrollW = 100, clientW = 100 }) {
    Object.defineProperty(t, 'scrollHeight', { configurable: true, value: scrollH })
    Object.defineProperty(t, 'clientHeight', { configurable: true, value: clientH })
    Object.defineProperty(t, 'scrollWidth',  { configurable: true, value: scrollW })
    Object.defineProperty(t, 'clientWidth',  { configurable: true, value: clientW })
  }
  const darueber = t => t.dispatchEvent(new Event('pointerover', { bubbles: true }))

  it('beim Rendern steht kein title am Titel, auch nicht in „kompakt“', () => {
    document.documentElement.setAttribute('data-density', 'kompakt')
    const t = rendere(vollKarte({ title: 'Ein sehr langer Titel' })).querySelector('.card-title-text')
    expect(t.hasAttribute('title')).toBe(false)
  })

  it('gekürzt (scrollHeight > clientHeight): Darüberfahren setzt den vollen Titel als Tooltip', () => {
    const t = rendere(vollKarte({ title: 'Ein sehr langer Titel' })).querySelector('.card-title-text')
    messbar(t, { scrollH: 60, clientH: 36 })
    darueber(t)
    expect(t.title).toBe('Ein sehr langer Titel')
  })

  it('F-2748: in „kompakt“ hebt die Tastaturmarkierung (j/k) die Kürzung auf, statt auf einen Tooltip zu setzen', () => {
    kompakt()
    const c = rendere(vollKarte({ title: 'Ein sehr langer Titel' }), [blocker()])
    const [markiert, andere] = c.querySelectorAll('.card')
    /* Klasse VOR der ersten Messung: happy-dom invalidiert den berechneten Stil bei einer
       Klassenänderung am Vorfahren nicht (gemessen 30.10.) — deshalb zwei Karten statt vorher/nachher. */
    markiert.classList.add('card--keyboard-active')            /* genau das tut activateCard (shortcuts.js) */
    const ta = andere.querySelector('.card-title-text'), tm = markiert.querySelector('.card-title-text')
    /* happy-dom kennt display:-webkit-box nicht, overflow schon: hidden = gekürzt */
    expect(getComputedStyle(ta).overflow).toBe('hidden')
    expect(getComputedStyle(tm).overflow).toBe('visible')
    expect(getComputedStyle(tm).display).toBe('block')
    expect(tm.hasAttribute('title')).toBe(false)
  })

  it('F-2748: ohne „kompakt“ ändert die Markierung nichts (die Regel gehört zur Dichte)', () => {
    const c = rendere(vollKarte({ title: 'Ein sehr langer Titel' }))
    c.querySelector('.card').classList.add('card--keyboard-active')
    expect(getComputedStyle(c.querySelector('.card-title-text')).overflow).not.toBe('hidden')
    expect(kompaktCss).toContain('.card--keyboard-active .card-title-text')
  })

  it('F-2751: Tab-Fokus (:focus-visible) hebt die Kürzung auf, die Regel nimmt nicht :focus-within (Mausklick)', () => {
    kompakt()
    const c = rendere(vollKarte({ title: 'Ein sehr langer Titel' }), [blocker()])
    const [fokus, andere] = c.querySelectorAll('.card')
    fokus.focus()                                             /* vor der ersten Messung, s. F-2748-Test */
    expect(document.activeElement).toBe(fokus)
    expect(getComputedStyle(andere.querySelector('.card-title-text')).overflow).toBe('hidden')
    expect(getComputedStyle(fokus.querySelector('.card-title-text')).overflow).toBe('visible')
    expect(kompaktRegeln.filter(r => r.selektor.includes(':focus-within'))).toEqual([])
  })

  it('F-2748: Fokus (focusin) setzt keinen Tooltip mehr — der Listener hört nur auf pointerover', () => {
    const c = rendere(vollKarte({ title: 'Ein sehr langer Titel' }))
    const t = c.querySelector('.card-title-text')
    messbar(t, { scrollH: 60, clientH: 36 })
    /* F-2752: auf den Titel selbst feuern — dort fände ein focusin-Listener ihn (trennt wirklich) */
    t.dispatchEvent(new Event('focusin', { bubbles: true }))
    expect(t.hasAttribute('title')).toBe(false)
  })

  it('nicht gekürzt: kein Tooltip, und ein früherer wird wieder entfernt (Dichte gewechselt)', () => {
    const t = rendere(vollKarte({ title: 'Kurz' })).querySelector('.card-title-text')
    messbar(t, { scrollH: 36, clientH: 36 })
    t.title = 'alt'
    darueber(t)
    expect(t.hasAttribute('title')).toBe(false)
  })

  it('der Listener sitzt einmal am Container, auch nach mehrfachem Rendern', () => {
    const c = rendere(vollKarte())
    const board = State.get().board
    Renderer.renderBoard({ board }); Renderer.renderBoard({ board })
    const t = c.querySelector('.card-title-text')
    const spy = vi.spyOn(t, 'removeAttribute')
    messbar(t, { scrollH: 36, clientH: 36 })
    darueber(t)
    expect(spy).toHaveBeenCalledTimes(1)
  })
})
