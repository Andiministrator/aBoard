/* @vitest-environment happy-dom */

/**
 * Karte #1004: Sortierung „nach Dringlichkeit“ (Benutzer-Einstellung `sortierung`).
 * Reine Darstellung: die gespeicherte Reihenfolge bleibt, Meta-Karten behalten
 * ihren Platz, die Erledigt-Spalte kennt keine Datumsstufen, und innerhalb einer
 * Spalte wird nicht per Drag umsortiert (Verschieben in eine andere Spalte schon).
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

import { Filter }    from '../src/filter.js'
import { State }     from '../src/state.js'
import { Renderer }  from '../src/renderer.js'
import { EventBus }  from '../src/eventbus.js'
import { Config }    from '../src/config.js'
import { DragDrop }  from '../src/dragdrop.js'
import { heuteOrtszeit } from '../src/dringlichkeit.js'

function tagVerschoben(n) {
  const d = new Date(); d.setDate(d.getDate() + n)
  return heuteOrtszeit(d)
}
const karte = (id, o = {}) => ({
  id, displayId: 1, title: id, description: '', labels: [], owner: '', priority: 'mittel',
  complexity: '', effort: null, qa: false, critical: false, blocked: false, columnId: 'c-offen', ...o,
})

let zaehler = 0
/* Zwei Spalten: „Backlog“ (c-offen) und „Erledigt“ (c-fertig, Titel-Semantik). */
function baue(karten) {
  const n = ++zaehler
  return {
    id: 'b', title: 'T',
    columnDefs: [{ id: `c-offen`, title: 'Backlog' }, { id: `c-fertig`, title: 'Erledigt' }],
    sprints: [{ id: `s${n}`, title: 'Lane', cards: karten }],
  }
}
function zeichne(board) {
  document.body.innerHTML = '<div id="board-header"></div><div id="board-columns"></div>'
  Renderer.renderBoard({ board })
}
const reihenfolge = spalte => [...document.querySelectorAll(`.column[data-id="${spalte}"] .card`)].map(e => e.dataset.id)

beforeEach(() => { Filter.clear(); Config.set('sortierung', 'manuell') })
afterEach(() => { document.body.innerHTML = ''; Config.set('sortierung', 'manuell'); vi.restoreAllMocks() })

const mix = () => [
  karte('rest1'),
  karte('hoch', { priority: 'hoch' }),
  karte('bald', { dueDate: tagVerschoben(2) }),
  karte('rest2'),
  karte('krit', { critical: true }),
  karte('ueberf', { dueDate: tagVerschoben(-1) }),
  karte('hoch2', { priority: 'hoch' }),
]

describe('Karte #1004 — Reihenfolge im DOM', () => {
  it('manuell (Voreinstellung): gespeicherte Reihenfolge', () => {
    expect(Config.get('sortierung')).toBe('manuell')
    zeichne(baue(mix()))
    expect(reihenfolge('c-offen')).toEqual(['rest1', 'hoch', 'bald', 'rest2', 'krit', 'ueberf', 'hoch2'])
  })

  it('dringlichkeit: kritisch, überfällig, bald fällig, hoch, Rest — stabil innerhalb der Stufe', () => {
    Config.set('sortierung', 'dringlichkeit')
    zeichne(baue(mix()))
    expect(reihenfolge('c-offen')).toEqual(['krit', 'ueberf', 'bald', 'hoch', 'hoch2', 'rest1', 'rest2'])
  })

  it('Erledigt-Spalte: Datum zählt nicht, kritisch und hoch schon', () => {
    Config.set('sortierung', 'dringlichkeit')
    const k = [
      karte('f-rest'), karte('f-ueberf', { dueDate: tagVerschoben(-5), columnId: 'c-fertig' }),
      karte('f-hoch', { priority: 'hoch', columnId: 'c-fertig' }),
    ].map(c => ({ ...c, columnId: 'c-fertig' }))
    zeichne(baue(k))
    /* f-ueberf bleibt vor f-hoch NICHT stehen: ohne Datumsstufe ist es „Rest“ */
    expect(reihenfolge('c-fertig')).toEqual(['f-hoch', 'f-rest', 'f-ueberf'])
  })

  it('Sprint-Meta-Karten behalten ihren Platz', () => {
    Config.set('sortierung', 'dringlichkeit')
    const k = [
      karte('meta-start', { kind: 'sprint-meta' }), karte('a'), karte('b', { critical: true }),
      karte('meta-ende', { kind: 'sprint-meta' }),
    ]
    zeichne(baue(k))
    expect(reihenfolge('c-offen')).toEqual(['meta-start', 'b', 'a', 'meta-ende'])
  })

  it('Spaltenkopf trägt den Hinweis nur im Modus dringlichkeit', () => {
    zeichne(baue(mix()))
    expect(document.querySelector('.column-header').title).toBe('')
    expect(document.querySelector('.column-header').classList.contains('column-header--sort')).toBe(false)
    Config.set('sortierung', 'dringlichkeit')
    zeichne(baue(mix()))
    expect(document.querySelector('.column-header').title).not.toBe('')
    expect(document.querySelector('.column-header').classList.contains('column-header--sort')).toBe(true) /* F-2763: sichtbares Symbol */
  })

  it('F-2862: Sortierzustand als Text im Kopf (Screenreader), nur bei dringlichkeit', () => {
    zeichne(baue(mix()))
    expect(document.querySelector('.column-header .visually-hidden')).toBeNull()
    Config.set('sortierung', 'dringlichkeit')
    zeichne(baue(mix()))
    const sr = document.querySelector('.column-header .visually-hidden')
    expect(sr).not.toBeNull()
    expect(sr.textContent).toBe(document.querySelector('.column-header').title)
    expect(sr.textContent).not.toBe('')
  })

  it('F-2862: Live-Region steht vor dem ersten Hinweis im DOM und trägt den Text', async () => {
    /* F-2876: renderer.js lädt ansage.js schon beim Import (die Region wird
       beim Aufräumen der Tests aus dem body entfernt) — hier frisch laden,
       damit „Region steht vor dem ersten Aufruf“ wieder messbar ist. */
    vi.resetModules()
    document.body.innerHTML = ''
    const vorher = document.querySelectorAll('[role="status"]').length
    const { ansagen } = await import('../src/ansage.js')
    const regionen = document.querySelectorAll('[role="status"]')
    expect(regionen.length).toBe(vorher + 1)          /* existiert schon beim Laden, vor jedem Aufruf */
    const region = regionen[regionen.length - 1]
    expect(region.textContent).toBe('')
    ansagen('Gesperrt')
    expect(region.textContent).toBe('Gesperrt')
    await Promise.resolve()                             /* F-2960: neuer Takt — sonst hängen sich Ansagen an */
    ansagen('Noch einmal')
    expect(document.querySelectorAll('[role="status"]').length).toBe(vorher + 1)  /* dasselbe Element */
    expect(region.textContent).toBe('Noch einmal')
    await Promise.resolve()
    ansagen('Noch einmal')                              /* gleicher Text → Inhalt ändert sich trotzdem (N-5) */
    expect(region.textContent).not.toBe('Noch einmal')
    expect(region.textContent.trim()).toBe('Noch einmal')
    await Promise.resolve()
    document.body.innerHTML = ''                        /* body geleert → nächster Hinweis hängt die Region wieder ein (N-6) */
    ansagen('Danach')
    expect(document.querySelector('[role="status"]')).toBe(region)
    expect(region.textContent).toBe('Danach')
    /* F-2960: zwei Ansagen im selben Takt (zwei Banner im selben Render) — beide bleiben stehen */
    await Promise.resolve()
    ansagen('Claude wartet auf deine Antwort')
    ansagen('Board wurde kompaktiert')
    expect(region.textContent).toBe('Claude wartet auf deine Antwort Board wurde kompaktiert')
    await Promise.resolve()
    ansagen('Später')
    expect(region.textContent).toBe('Später')
  })

  it('F-2862: showExportToast sagt über die Live-Region an, das sichtbare Toast ist aria-hidden', async () => {
    const { readFileSync } = await import('node:fs')
    const src = readFileSync('src/main.js', 'utf8')
    const start = src.indexOf('function showExportToast(')
    expect(start).toBeGreaterThan(-1)
    const ende = src.indexOf('\n}\n', start)
    expect(ende).toBeGreaterThan(start)
    const fn = src.slice(start, ende)
    expect(fn).toContain('ansagen(msg)')
    expect(fn).toContain("setAttribute('aria-hidden', 'true')")
    expect(fn).not.toContain("'role', 'status'")
    expect(src).toMatch(/^import \{ ansagen \}\s+from '\.\/ansage\.js'/m)
  })

  it('gespeicherte Reihenfolge und Karten bleiben unverändert', () => {
    Config.set('sortierung', 'dringlichkeit')
    const board = baue(mix())
    const vorher = JSON.stringify(board)
    const idsVorher = board.sprints[0].cards.map(c => c.id)
    zeichne(board)
    expect(board.sprints[0].cards.map(c => c.id)).toEqual(idsVorher)
    expect(JSON.stringify(board)).toBe(vorher)
  })
})

describe('Karte #1004 — Drag-Sperre innerhalb der Spalte', () => {
  let bewegt
  const merke = p => bewegt.push(p)
  let initialisiert = false
  beforeEach(() => {
    bewegt = []
    EventBus.on('card:moved', merke)
    Element.prototype.setPointerCapture ??= function () {}
    if (!initialisiert) { DragDrop.init(); initialisiert = true }
  })
  afterEach(() => EventBus.off('card:moved', merke))

  function ziehe(kartenId, zielEl) {
    const el = document.querySelector(`.card[data-id="${kartenId}"]`)
    el.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, button: 0, clientX: 0, clientY: 0, pointerId: 1 }))
    vi.spyOn(document, 'elementFromPoint').mockReturnValue(zielEl)
    el.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientX: 50, clientY: 50, pointerId: 1 }))
    el.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientX: 50, clientY: 50, pointerId: 1 }))
    vi.restoreAllMocks()
  }

  it('manuell: Umsortieren in derselben Spalte meldet card:moved mit Index', () => {
    zeichne(baue(mix()))
    ziehe('rest1', document.querySelector('.card[data-id="bald"]'))
    expect(bewegt.length).toBe(1)
    expect(bewegt[0].targetIndex).toBe(2)
  })

  it('dringlichkeit: innerhalb derselben Spalte kein card:moved', () => {
    Config.set('sortierung', 'dringlichkeit')
    zeichne(baue(mix()))
    ziehe('rest1', document.querySelector('.card[data-id="bald"]'))
    ziehe('rest1', document.querySelector('.column[data-id="c-offen"]'))
    expect(bewegt).toEqual([])
  })

  it('dringlichkeit: in eine andere Spalte möglich, ans Ende angehängt (kein Index)', () => {
    Config.set('sortierung', 'dringlichkeit')
    const k = [...mix(), karte('f1', { columnId: 'c-fertig' })]
    zeichne(baue(k))
    ziehe('rest1', document.querySelector('.card[data-id="f1"]'))
    expect(bewegt.length).toBe(1)
    expect(bewegt[0].toColId).toBe('c-fertig')
    expect(bewegt[0].targetIndex).toBeNull()
  })

  /* F-2763 */
  it('dringlichkeit: gesperrter Drop in die eigene Spalte meldet einen Hinweis', () => {
    Config.set('sortierung', 'dringlichkeit')
    const hinweise = []
    const h = t => hinweise.push(t)
    EventBus.on('hinweis', h)
    zeichne(baue(mix()))
    ziehe('rest1', document.querySelector('.column[data-id="c-offen"]'))
    EventBus.off('hinweis', h)
    expect(bewegt).toEqual([])
    expect(hinweise.length).toBe(1)
    expect(hinweise[0]).toMatch(/Dringlichkeit|urgency/)
  })

  it('manuell: kein Hinweis beim Drop in die eigene Spalte', () => {
    const hinweise = []
    const h = t => hinweise.push(t)
    EventBus.on('hinweis', h)
    zeichne(baue(mix()))
    ziehe('rest1', document.querySelector('.column[data-id="c-offen"]'))
    EventBus.off('hinweis', h)
    expect(hinweise).toEqual([])
  })

  it('dringlichkeit: eigene Spalte wird beim Ziehen nicht als Drop-Ziel markiert, andere schon', () => {
    Config.set('sortierung', 'dringlichkeit')
    zeichne(baue([...mix(), karte('f1', { columnId: 'c-fertig' })]))
    const el = document.querySelector('.card[data-id="rest1"]')
    el.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, button: 0, clientX: 0, clientY: 0, pointerId: 1 }))
    const marke = zielEl => {
      vi.spyOn(document, 'elementFromPoint').mockReturnValue(zielEl)
      el.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientX: 50, clientY: 50, pointerId: 1 }))
      return document.querySelectorAll('.column-drop-target').length
    }
    expect(marke(document.querySelector('.column[data-id="c-offen"]'))).toBe(0)
    expect(marke(document.querySelector('.column[data-id="c-fertig"]'))).toBe(1)
    el.dispatchEvent(new PointerEvent('pointercancel', { bubbles: true, pointerId: 1 }))
    vi.restoreAllMocks()
  })
})
