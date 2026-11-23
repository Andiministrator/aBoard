/* @vitest-environment happy-dom */

/**
 * Karte #825 (ADR-0141 §8): Schatten auf einer externen KOPIE stehen in ihrer
 * Lane und Spalte, nicht im Band. Eigene Klasse `.schatten-karte`, nie
 * `.card` — Zähler, Ziehen, Modal, Mehrfachauswahl, j/k und FLIP zählen
 * DOM-Knoten `.card` und sehen sie damit nicht (die fünf Stellen aus ADR-0141
 * „Kontext/Anzeige“). Das Band fängt nur Schatten auf, deren Lane oder Spalte
 * es außen nicht gibt.
 *
 * Server-only (`describeServer`); ohne Server-Build liefert inSpalte nichts.
 *
 * Gegenproben (von Hand, Bericht #825): Klasse `card` an die Schatten-Karte →
 * „Zähler = Anzahl .card“ rot; Lane-Vergleich über `sprint.id` statt Marke →
 * „richtige Spalte“ rot; Band zeigt alle statt ohnePlatz → „Band nur Auffang“ rot.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer, describeOhneServer } from './helpers/build-flags.js'

vi.mock('../src/boardpaar.js', async (orig) => ({ ...(await orig()),
  boardpaarSeite: () => Promise.resolve('aussen'),
  vergissBoardpaarSeite: () => {},
}))
vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName: vi.fn(() => null), listKnownBoards: vi.fn(async () => []), getInboxOpCount: vi.fn(() => 0),
    requestCompaction: vi.fn(async () => true), getChatTail: vi.fn(() => []), setChatTailListener: vi.fn(() => {}),
    setChatTailEnabled: vi.fn(async () => {}), readChatTail: vi.fn(async () => {}),
  },
}))
vi.mock('../src/dragdrop.js', () => ({ DragDrop: { takePendingFlip: vi.fn(() => null), init: vi.fn() } }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

const { SchattenAussen, inSpalte, ohnePlatz } = await import('../src/schatten-aussen.js')
const { State } = await import('../src/state.js')
const { Search } = await import('../src/search.js')
const { I18n } = await import('../src/i18n.js')
const { Renderer } = await import('../src/renderer.js')

const ruhe = () => new Promise(r => setTimeout(r, 0))
async function ruhig() { for (let i = 0; i < 6; i++) await ruhe() }

const LANE = 'L'.repeat(43)
const REVIEW = 'R'.repeat(43)
const FREMD = 'F'.repeat(43)

const SCHATTEN = [
  { schatten_id: 'S2', art: 'karte', nummer: 'i-12', titel: 'Geheim Kunde', titel_platzhalter: false, lane: LANE, spalte_id: REVIEW,
    prioritaet: 'hoch', bearbeiter: ['Gerd Muster'], von_agent: false, link: 'https://innen.example/#board=b&card=12' },
  { schatten_id: 'S1', art: 'karte', nummer: 'i-3', titel: 'Angebot Müller', titel_platzhalter: false, lane: LANE, spalte_id: REVIEW,
    bearbeiter: [], von_agent: false },
  { schatten_id: 'S3', art: 'karte', nummer: 'i-7', titel: 'Idee', titel_platzhalter: false, lane: '__backlog__', spalte_id: 'col-backlog',
    bearbeiter: [], von_agent: false },
  { schatten_id: 'S4', art: 'karte', nummer: 'i-9', titel: 'Ohne Platz', titel_platzhalter: false, lane: FREMD, spalte_id: 'col-backlog',
    bearbeiter: [], von_agent: false },
  { schatten_id: 'S5', art: 'termin', titel: 'Jour fixe', start_datum: '2026-10-24', bearbeiter: [], von_agent: false },
]

function board() {
  return {
    id: 'kopie-1', title: 'Kopie',
    columnDefs: [
      { id: 'col-backlog', title: 'Backlog', spiegel: { id: 'col-backlog' } },
      { id: 'aussen-review', title: 'Review', spiegel: { id: REVIEW } },
    ],
    sprints: [
      { id: 'aussen-lane', title: 'Sprint 1', kind: 'sprint', spiegel: { id: LANE },
        cards: [{ id: 'echt', displayId: 1, title: 'Echte Karte', columnId: 'aussen-review', labels: [] }] },
      { id: '__backlog__', title: 'Backlog', kind: 'backlog', spiegel: { id: '__backlog__' }, cards: [] },
    ],
  }
}

let kopie = true
beforeEach(() => {
  I18n.setLang('de')
  kopie = true
  document.body.innerHTML = '<div id="app"><header id="board-header"></header><div id="board-columns"></div></div>'
  vi.stubGlobal('EventSource', class { addEventListener() {} close() {} })
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    if (String(url).includes('/schatten')) {
      return { status: 200, ok: true, json: async () => ({ letzter_abgleich: new Date().toISOString(), schatten: SCHATTEN, kopie }) }
    }
    return { status: 200, ok: true, type: 'opaque' }
  }))
  Search.clear()
})

afterEach(() => {
  SchattenAussen._resetForTest()
  vi.unstubAllGlobals()
})

describeServer('#825 Schatten in Lane und Spalte (rein)', () => {
  it('inSpalte findet über die Marke, sortiert nach Nummer; Standardspalten und Backlog über die Klartext-ID', () => {
    const b = board()
    const review = inSpalte(b.sprints[0], b.columnDefs[1], SCHATTEN, '')
    expect(review.map(e => e.nummer)).toEqual(['i-3', 'i-12'])
    expect(inSpalte(b.sprints[1], b.columnDefs[0], SCHATTEN, '').map(e => e.nummer)).toEqual(['i-7'])
    /* Ohne Marke zählt die id: eine Lane „aussen-lane“ ohne Marke trifft LANE nicht. */
    const ohneMarke = { ...b.sprints[0], spiegel: undefined }
    expect(inSpalte(ohneMarke, b.columnDefs[1], SCHATTEN, '')).toHaveLength(0)
    /* Suche filtert wie im Band. */
    expect(inSpalte(b.sprints[0], b.columnDefs[1], SCHATTEN, 'geheim').map(e => e.nummer)).toEqual(['i-12'])
  })

  it('ohnePlatz: nur Karten, deren Lane oder Spalte es nicht gibt; Termine nie', () => {
    expect(ohnePlatz(SCHATTEN, board()).map(e => e.schatten_id)).toEqual(['S4'])
  })
})

describeServer('#825 Externe Kopie im Browser', () => {
  async function lade() {
    State.set({ version: '2', board: board() })
    SchattenAussen.init()
    await ruhig()
  }

  it('Band nur als Auffang: auf einer Kopie zeigt es nur, was keinen Platz hat', async () => {
    await lade()
    expect(SchattenAussen.kopie()).toBe(true)
    const zeilen = [...document.querySelectorAll('#schatten-aussen .sch-zeile')]
    expect(zeilen.map(z => z.dataset.schatten)).toEqual(['S4'])
  })

  it('ohne Kopie (Stufe 1) bleibt das Band wie bisher — alle Schatten', async () => {
    kopie = false
    await lade()
    expect(SchattenAussen.kopie()).toBe(false)
    expect(document.querySelectorAll('#schatten-aussen .sch-zeile')).toHaveLength(5)
  })

  it('Renderer: Schatten-Karten am Ende der richtigen Spalte, nie .card; Zähler = Anzahl .card', async () => {
    await lade()
    Renderer.renderBoard(State.get())
    const spalte = document.querySelector('.column[data-id="aussen-review"][data-sprint-id="aussen-lane"]')
    expect(spalte).not.toBeNull()
    const karten = [...spalte.querySelectorAll('.column-cards > *')]
    expect(karten.map(k => k.classList.contains('schatten-karte') ? k.dataset.schatten : 'card'))
      .toEqual(['card', 'S1', 'S2'])
    const schatten = spalte.querySelectorAll('.schatten-karte')
    expect([...schatten].every(s => !s.classList.contains('card') && s.getAttribute('draggable') === 'false')).toBe(true)
    expect(spalte.querySelector('.column-count').textContent).toBe(String(spalte.querySelectorAll('.card').length))
    /* Vertraulicher Titel sichtbar (Beschluss 3), mit Nummer, Priorität und Bearbeiter. */
    expect(schatten[1].textContent).toContain('Geheim Kunde')
    expect(schatten[1].textContent).toContain('i-12')
    expect(schatten[1].textContent).toContain('hoch · Gerd Muster')
    /* Backlog-Lane, Standardspalte. */
    const backlog = document.querySelector('.column[data-id="col-backlog"][data-sprint-id="__backlog__"]')
    expect([...backlog.querySelectorAll('.schatten-karte')].map(s => s.dataset.schatten)).toEqual(['S3'])
  })

  it('Kritik Sprint 93 K-1: Suche blendet gespiegelte Lanes nicht aus — Schatten-Treffer bleiben sichtbar', async () => {
    await lade()
    Search.setQuery('geheim'); await new Promise(r => setTimeout(r, 200))
    Renderer.renderBoard(State.get())
    /* Die echte Karte passt nicht, die Lane bleibt trotzdem (Marke), und der
       Schatten-Treffer steht in seiner Spalte. */
    const spalte = document.querySelector('.column[data-id="aussen-review"][data-sprint-id="aussen-lane"]')
    expect(spalte).not.toBeNull()
    expect([...spalte.querySelectorAll('.schatten-karte')].map(s => s.dataset.schatten)).toEqual(['S2'])
    /* D-2: keine Sprint-Fortschrittslinie auf der Kopie. */
    const lane = document.querySelector('.swimlane[data-sprint-id="aussen-lane"]')
    expect(lane.querySelector('.sprint-progress-line, .sprint-progress')).toBeNull()
  })

  it('Suche ohne Marke: eine Lane ohne Treffer fällt weiter weg (Karte #156)', async () => {
    State.set({ version: '2', board: { ...board(), sprints: board().sprints.map(l => ({ ...l, spiegel: undefined })) } })
    SchattenAussen.init()
    await ruhig()
    Search.setQuery('geheim'); await new Promise(r => setTimeout(r, 200))
    Renderer.renderBoard(State.get())
    expect(document.querySelector('.swimlane[data-sprint-id="aussen-lane"]')).toBeNull()
  })

  it('Klick öffnet kein Modal, sondern den Hinweis „Interne Karte“', async () => {
    await lade()
    Renderer.renderBoard(State.get())
    const s = document.querySelector('.schatten-karte[data-schatten="S2"]')
    s.click()
    await ruhig()
    expect(document.querySelector('.sch-hinweis')).not.toBeNull()
    expect(document.querySelector('.modal-overlay, .modal')).toBeNull()
    document.querySelectorAll('.sch-hinweis').forEach(n => n.remove())
  })
})

describeOhneServer('#825 ohne Server-Build', () => {
  it('inSpalte liefert nichts', () => {
    const b = board()
    expect(inSpalte(b.sprints[0], b.columnDefs[1], SCHATTEN, '')).toEqual([])
  })
})
