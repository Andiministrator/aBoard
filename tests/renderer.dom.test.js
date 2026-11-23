/* @vitest-environment happy-dom */

/**
 * DOM-Tests für renderer.js — Karte #92
 * Abdeckung: renderCard (displayId-Badge, Labels, ARIA), Filter-Klasse,
 * Spalten-Toggle (Karte #93), Dirty-Stern im Board-Titel
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeProjektor, IS_PROJECTOR_BUILD, IS_SERVER_BUILD } from './helpers/build-flags.js'

/* ADR-0116: Kompaktierungs-Banner, Activity-Tail- und Chat-Tail-Footer sind
   IS_PROJECTOR_BUILD-gegatet (renderer.js) und fallen im slim- wie im
   server-frontend-Build per DCE weg. Der Renderer erzeugt dort keinen Footer
   und kein Banner — die Tests prüften sonst gegen `null`. */

// ── Mocks für Abhängigkeiten die renderer.js benötigt ────────────────────

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName: vi.fn(() => null),
    listKnownBoards:  vi.fn(async () => []),
    getInboxOpCount:  vi.fn(() => 0),
    requestCompaction: vi.fn(async () => true),
    /* Karte #290: Chat-Tail-Stubs für renderChatTailFooter — der Mock soll
       die Listener-Funktion sofort mit Test-Inhalten triggern können. */
    getChatTail:         vi.fn(() => []),
    setChatTailListener: vi.fn(() => {}),
    setChatTailEnabled:  vi.fn(async () => {}),
    readChatTail:        vi.fn(async () => {}),
  },
}))

vi.mock('../src/storage.js', () => ({
  Storage: {
    listKnownFiles: vi.fn(async () => []),
  },
}))

vi.mock('../src/dragdrop.js', () => ({
  DragDrop: {
    takePendingFlip: vi.fn(() => null),
    init:            vi.fn(),
  },
}))

// Vite ?raw-Import — kein Vite in Vitest
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Filter }   from '../src/filter.js'
import { State }    from '../src/state.js'
import { Renderer } from '../src/renderer.js'
import { Sync }     from '../src/sync.js'
import { EventBus } from '../src/eventbus.js'
import { COMPACT_OP_THRESHOLD } from '../src/inbox.js'
import { Config }   from '../src/config.js'
import { I18n }     from '../src/i18n.js'

// ── Hilfsfunktionen ───────────────────────────────────────────────────────

/** Zähler für eindeutige IDs — verhindert Singleton-State-Kollisionen in collapsedColumns */
let _testCounter = 0
function nextId() { return `t${++_testCounter}` }

function makeCard(overrides = {}) {
  return {
    id:          `card-${Math.random().toString(36).slice(2)}`,
    displayId:   1,
    title:       'Test-Karte',
    description: '',
    labels:      [],
    owner:       '',
    priority:    'mittel',
    complexity:  '',
    effort:      null,
    qa:          false,
    critical:    false,
    blocked:     false,
    columnId:    'col-todo',
    ...overrides,
  }
}

function makeColDef(overrides = {}) {
  return {
    id:    'col-todo',
    title: 'Backlog',
    ...overrides,
  }
}

/**
 * Minimales Board-State-Objekt für renderBoard.
 * Verwendet eindeutige Sprint-ID und Spalten-ID um collapsedColumns-Singleton
 * zwischen Tests nicht zu teilen.
 */
function makeBoard(cards = [], colDefs = null, sprintId = null) {
  const sid = sprintId ?? `sprint-${nextId()}`
  const cid = `col-${nextId()}`
  const usedColDefs = colDefs ?? [{ id: cid, title: 'Backlog' }]
  // Karten an die neue colId anpassen
  const adjustedCards = cards.map(c => ({ ...c, columnId: usedColDefs[0]?.id ?? cid }))
  return {
    id:         'board-test',
    title:      'Test-Board',
    columnDefs: usedColDefs,
    sprints:    [{ id: sid, title: 'Sprint 1', cards: adjustedCards }],
  }
}

/** Hilfs-Setup: DOM-Anker für renderBoard erstellen und Board rendern */
function setupBoardDOM(board) {
  const header = document.createElement('div')
  header.id = 'board-header'
  const container = document.createElement('div')
  container.id = 'board-columns'
  document.body.appendChild(header)
  document.body.appendChild(container)
  Renderer.renderBoard({ board })
  return container
}

/** renderCard indirekt via renderBoard testen */
function renderCardViaBoard(card) {
  const board = makeBoard([card])
  return setupBoardDOM(board)
}

// ── Setup / Teardown ─────────────────────────────────────────────────────

beforeEach(() => {
  document.body.innerHTML = ''
  Filter.clear()
  State.markClean()
})

afterEach(() => {
  document.body.innerHTML = ''
  Filter.clear()
  vi.clearAllMocks()
})

// ─────────────────────────────────────────────────────────────────────────
// 1) renderCard — Grundstruktur
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — renderCard Grundstruktur', () => {
  it('Karte hat tabindex="0"', () => {
    const container = renderCardViaBoard(makeCard())
    const cardEl = container.querySelector('.card')
    expect(cardEl).toBeTruthy()
    expect(cardEl.getAttribute('tabindex')).toBe('0')
  })

  it('Karte hat role="button"', () => {
    const container = renderCardViaBoard(makeCard())
    const cardEl = container.querySelector('.card')
    expect(cardEl.getAttribute('role')).toBe('button')
  })

  it('F-2707: Owner-Abzeichen nennt den Namen im Tooltip; ohne Avatar keine --avatar-Klasse', () => {
    const container = renderCardViaBoard(makeCard({ owner: 'Andi' }))
    const badge = container.querySelector('.card-owner-badge')
    expect(badge.title).toBe('Owner: Andi')
    /* ohne Board im State löst der Renderer keine Identität auf: kein Avatar */
    expect(badge.querySelector('[role="img"]')).toBeNull()
    expect(badge.classList.contains('card-owner-badge--avatar')).toBe(false)
  })

  it('F-2716 (Verify): mit Board im State hat das Abzeichen einen Avatar und die --avatar-Klasse', () => {
    const board = makeBoard([makeCard({ owner: 'Andi' })])
    State.set({ board })
    const container = setupBoardDOM(board)
    const badge = container.querySelector('.card-owner-badge')
    expect(badge.querySelector('[role="img"]')).not.toBeNull()
    expect(badge.classList.contains('card-owner-badge--avatar')).toBe(true)
  })

  it('aria-label entspricht card.title', () => {
    const card = makeCard({ title: 'Meine Testkarte' })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.getAttribute('aria-label')).toBe('Meine Testkarte')
  })

  it('Titel wird als Text in .card-title-text gerendert', () => {
    const card = makeCard({ title: 'Wichtige Karte' })
    const container = renderCardViaBoard(card)
    const titleText = container.querySelector('.card-title-text')
    expect(titleText).toBeTruthy()
    expect(titleText.textContent).toBe('Wichtige Karte')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 2) renderCard — displayId-Badge
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — displayId-Badge', () => {
  it('displayId rendert #N-Badge als Link', () => {
    const card = makeCard({ displayId: 42 })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-display-id')
    expect(badge).toBeTruthy()
    expect(badge.textContent).toBe('#42')
  })

  it('displayId-Badge verlinkt auf #card-N', () => {
    const card = makeCard({ displayId: 77 })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-display-id')
    expect(badge.getAttribute('href')).toBe('#card-77')
  })

  it('kein displayId-Badge wenn displayId null', () => {
    const card = makeCard({ displayId: null })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-display-id')
    expect(badge).toBeNull()
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 3) renderCard — Labels
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Labels', () => {
  it('Labels werden als .label-Spans gerendert', () => {
    const card = makeCard({ labels: ['bug', 'ui', 'sprint-14'] })
    const container = renderCardViaBoard(card)
    const spans = container.querySelectorAll('.card-labels .label')
    expect(spans.length).toBe(3)
    expect(spans[0].textContent).toBe('bug')
    expect(spans[1].textContent).toBe('ui')
    expect(spans[2].textContent).toBe('sprint-14')
  })

  it('kein .card-labels-Element bei leeren Labels', () => {
    const card = makeCard({ labels: [] })
    const container = renderCardViaBoard(card)
    const labelsEl = container.querySelector('.card-labels')
    expect(labelsEl).toBeNull()
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 4) renderCard — Flags (critical, blocked, qa)
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Flag-Badges', () => {
  it('card.critical rendert ⚡-Badge', () => {
    const card = makeCard({ critical: true })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-flag-badge--critical')
    expect(badge).toBeTruthy()
  })

  it('card.blocked rendert ⊘-Badge', () => {
    const card = makeCard({ blocked: true })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-flag-badge--blocked')
    expect(badge).toBeTruthy()
  })

  it('card.qa rendert ✓ QA-Badge', () => {
    const card = makeCard({ qa: true })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-qa-badge')
    expect(badge).toBeTruthy()
    expect(badge.textContent).toContain('QA')
  })

  it('keine Flag-Badges bei allen false', () => {
    const card = makeCard({ critical: false, blocked: false, qa: false })
    const container = renderCardViaBoard(card)
    expect(container.querySelector('.card-flag-badge--critical')).toBeNull()
    expect(container.querySelector('.card-flag-badge--blocked')).toBeNull()
    expect(container.querySelector('.card-qa-badge')).toBeNull()
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 5) Datum-Badges (ADR-0034)
// ─────────────────────────────────────────────────────────────────────────

/* Kalendertage bis iso, lokal (wie diffDays in date-helpers.js) */
function diffTage(iso) {
  const h = new Date(); h.setHours(0, 0, 0, 0)
  return Math.round((new Date(iso + 'T00:00') - h) / 86400000)
}

describe('Renderer — Datum-Badges (ADR-0034)', () => {
  describe('#972 kurzes Datum (feste Uhr, deutsch)', () => {
    /* F-2678: Uhr und Sprache im afterEach zurücksetzen — ein fehlschlagendes expect
       ließ sonst Fake-Timer und 'de' für die Folgetests stehen */
    let sprache
    beforeEach(() => {
      sprache = I18n.getLang()
      vi.useFakeTimers()
      vi.setSystemTime(new Date(2026, 9, 28, 12))
      I18n.setLang('de')
    })
    afterEach(() => {
      vi.useRealTimers()
      I18n.setLang(sprache)
    })

    it('bald fällig: ⏳ statt 📅, ohne Jahr im laufenden Jahr, Klartext als Name', () => {
      const badge = renderCardViaBoard(makeCard({ dueDate: '2026-10-30' })).querySelector('.card-date-badge')
      expect(badge.textContent).toBe('⏳ 30. Okt.')
      expect(badge.classList.contains('card-date-badge--soon')).toBe(true)
      /* F-2674/F-2675: Dringlichkeit nicht nur über Farbe, Name per role=img */
      expect(badge.title).toBe('⏳ 30. Okt. — Fällig am: 2026-10-30 (in 2 Tagen)')
      expect(badge.getAttribute('aria-label')).toBe(badge.title)
      expect(badge.getAttribute('role')).toBe('img')
    })

    /* F-2680: Grenzen der Zeichenwahl — heute und genau 3 Tage ⏳, 4 Tage 📅 */
    it('Grenzen: heute und in 3 Tagen ⏳, in 4 Tagen 📅', () => {
      const zeichen = iso => renderCardViaBoard(makeCard({ dueDate: iso })).querySelector('.card-date-badge').textContent.split(' ')[0]
      expect(zeichen('2026-10-28')).toBe('⏳')
      document.body.innerHTML = ''
      expect(zeichen('2026-10-31')).toBe('⏳')
      document.body.innerHTML = ''
      expect(zeichen('2026-11-01')).toBe('📅')
    })

    it('überfällig: ⚠ im Text, „Überfällig“ im Tooltip', () => {
      const badge = renderCardViaBoard(makeCard({ dueDate: '2026-10-01' })).querySelector('.card-date-badge--overdue')
      expect(badge.textContent).toBe('⚠ 1. Okt.')
      expect(badge.title).toBe('⚠ 1. Okt. — Fällig am: 2026-10-01 (Überfällig)')
    })

    it('später als 3 Tage: neutrales 📅, Restzeit im Tooltip', () => {
      const badge = renderCardViaBoard(makeCard({ dueDate: '2026-11-15' })).querySelector('.card-date-badge')
      expect(badge.textContent).toBe('📅 15. Nov.')
      expect(badge.className).toBe('card-date-badge')
      expect(badge.title).toBe('📅 15. Nov. — Fällig am: 2026-11-15 (in 18 Tagen)')
    })
  })

  it('#972: unlesbares Datum bleibt Rohwert ohne Restzeit', () => {
    const badge = renderCardViaBoard(makeCard({ dueDate: 'kaputt' })).querySelector('.card-date-badge')
    expect(badge.textContent).toBe('📅 kaputt')
    expect(badge.title).toBe(`📅 kaputt — ${I18n.t('card.due-date')}: kaputt`)
  })

  it('kein Datum-Badge bei dueDate null', () => {
    const card = makeCard({ dueDate: null, columnId: 'col-todo' })
    const container = renderCardViaBoard(card)
    expect(container.querySelector('.card-date-badge')).toBeNull()
  })

  it('Datum-Badge erscheint bei gesetztem dueDate in der Zukunft (neutral)', () => {
    const future = new Date()
    future.setDate(future.getDate() + 10)
    const card = makeCard({ dueDate: future.toISOString().slice(0, 10), columnId: 'col-todo' })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-date-badge')
    expect(badge).toBeTruthy()
    // neutral — keine overdue/soon Modifier
    expect(badge.classList.contains('card-date-badge--overdue')).toBe(false)
    expect(badge.classList.contains('card-date-badge--soon')).toBe(false)
  })

  /* ADR-0138 §1: Uhrzeit hinter dem Datum, Überfällig-Regel bleibt tagesgenau. */
  it('Datum-Badge zeigt die Uhrzeit hinter dem Datum', () => {
    const card = makeCard({ dueDate: '2099-12-31', dueTime: '17:00', columnId: 'col-todo' })
    const badge = renderCardViaBoard(card).querySelector('.card-date-badge')
    /* Karte #972: Text kurz mit Uhrzeit und fremdem Jahr, volles Datum im Tooltip */
    expect(badge.textContent).toMatch(/^📅 .*2099.*17:00$/)
    expect(badge.textContent).not.toContain('2099-12-31')
    expect(badge.title).toBe(`${badge.textContent} — ${I18n.t('card.due-date')}: 2099-12-31 17:00 (${I18n.t('date.in-days', diffTage('2099-12-31'))})`)
  })

  it('Uhrzeit ändert die Überfällig-Regel nicht: heute 00:01 ist nicht überfällig', () => {
    const heute = new Date()
    const iso = `${heute.getFullYear()}-${String(heute.getMonth() + 1).padStart(2, '0')}-${String(heute.getDate()).padStart(2, '0')}`
    const card = makeCard({ dueDate: iso, dueTime: '00:01', columnId: 'col-todo' })
    const container = renderCardViaBoard(card)
    expect(container.querySelector('.card-date-badge--overdue')).toBeNull()
    expect(container.querySelector('.card-date-badge--soon')).toBeTruthy()
  })

  it('Datum-Badge hat Klasse --soon wenn dueDate innerhalb 3 Tagen', () => {
    const soon = new Date()
    soon.setDate(soon.getDate() + 2)
    const card = makeCard({ dueDate: soon.toISOString().slice(0, 10), columnId: 'col-todo' })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-date-badge--soon')
    expect(badge).toBeTruthy()
  })

  it('Datum-Badge hat Klasse --overdue wenn dueDate in der Vergangenheit', () => {
    const past = new Date()
    past.setDate(past.getDate() - 5)
    const card = makeCard({ dueDate: past.toISOString().slice(0, 10), columnId: 'col-todo' })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-date-badge--overdue')
    expect(badge).toBeTruthy()
  })

  it('kein Datum-Badge bei col-erledigt — auch wenn überfällig', () => {
    const past = new Date()
    past.setDate(past.getDate() - 5)
    // Muss Board mit col-erledigt als einziger Spalte aufbauen, damit columnId erhalten bleibt
    const colErledigt = { id: 'col-erledigt', title: 'Erledigt' }
    const card = makeCard({ dueDate: past.toISOString().slice(0, 10), columnId: 'col-erledigt' })
    const board = makeBoard([card], [colErledigt])
    const container = setupBoardDOM(board)
    expect(container.querySelector('.card-date-badge--overdue')).toBeNull()
    expect(container.querySelector('.card-date-badge')).toBeNull()
  })

  it('kein Datum-Badge in einer umbenannten/eigenen Erledigt-Spalte mit fremder id (F-141)', () => {
    const past = new Date()
    past.setDate(past.getDate() - 5)
    /* Eigene "Fertig"-Spalte mit UUID-artiger id statt des festen 'col-erledigt' —
       genau der Fall aus F-141: wer die Erledigt-Spalte umbenennt oder eine eigene
       anlegt, bekommt eine id ungleich 'col-erledigt' (createColumnDef()). Die
       Titel-Semantik (isDoneColumn/F-250, DONE_COLUMN_NAMES) muss den Badge
       trotzdem unterdrücken — ein reiner columnId==='col-erledigt'-Vergleich
       (der alte, jetzt entfernte Code) würde hier fälschlich einen Badge zeigen. */
    const colFertig = { id: 'col-custom-done-xyz', title: 'Fertig' }
    const card = makeCard({ dueDate: past.toISOString().slice(0, 10), columnId: 'col-custom-done-xyz' })
    const board = makeBoard([card], [colFertig])
    const container = setupBoardDOM(board)
    expect(container.querySelector('.card-date-badge')).toBeNull()
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 5b) Datum-Badges — DST-sichere Datums-Differenz (F-140)
//
// Die alte Formel `Math.floor((new Date(due) - new Date(today)) / 86400000)`
// berechnete "heute" über `new Date().toISOString().slice(0, 10)` — das ist
// der UTC-Kalendertag, nicht der lokale. Kurz nach lokaler Mitternacht weichen
// UTC-Tag und lokaler Tag um 1 ab; am stärksten sichtbar in der Nacht des
// DST-Wechsels, weil sich dort der UTC-Offset ändert (F-140-Vorgabe:
// Europe/Berlin, letzter Sonntag März/Oktober). Der neue Code rechnet über
// `todayISO()` (lokale Kalenderkomponenten) + `diffDays()` (reine
// UTC-Zivil-Arithmetik, src/calendar/date-helpers.js) und ist damit
// unabhängig vom UTC-Offset korrekt.
//
// process.env.TZ wird hier explizit gesetzt, damit der Test unabhängig von
// der Zeitzone der ausführenden Maschine deterministisch ist — CI läuft
// typischerweise in UTC, wo die alte Formel NICHT sichtbar bricht (dort gilt
// lokal == UTC, der Bug tritt gar nicht auf).
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Datum-Badges: DST-sichere Differenz (F-140)', () => {
  let originalTZ

  beforeEach(() => {
    originalTZ = process.env.TZ
    process.env.TZ = 'Europe/Berlin'
  })

  afterEach(() => {
    vi.useRealTimers()
    if (originalTZ === undefined) delete process.env.TZ
    else process.env.TZ = originalTZ
  })

  it('März-DST-Beginn (2026-03-29, EU): dueDate "heute" bleibt neutral (nicht --soon/--overdue-verschoben)', () => {
    // 2026-03-29, 00:30 Uhr lokal (Berlin) = 2026-03-28T23:30:00Z — Umstellung
    // CET→CEST erfolgt erst um 02:00 Uhr lokal (01:00Z) desselben Tages, der
    // lokale Kalendertag ist bereits der 29. Die alte Formel läse "heute" (UTC)
    // als den 28. und käme auf diffDays = 1 → fälschlich "in 1 Tag" statt "heute".
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-03-28T23:30:00Z'))
    const card = makeCard({ dueDate: '2026-03-29', columnId: 'col-todo' })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-date-badge')
    expect(badge).toBeTruthy()
    expect(badge.title).toContain(I18n.t('date.today')) /* #972: Restzeit im Tooltip */
    expect(badge.classList.contains('card-date-badge--overdue')).toBe(false)
  })

  it('Oktober-DST-Ende (2026-10-25, EU): dueDate "heute" bleibt neutral (nicht --soon/--overdue-verschoben)', () => {
    // 2026-10-26, 00:30 Uhr lokal (Berlin) = 2026-10-25T23:30:00Z — Umstellung
    // CEST→CET erfolgt bereits um 03:00 Uhr lokal (01:00Z) am 25.10.; zum
    // Testzeitpunkt (23:30Z) gilt schon CET, lokaler Kalendertag ist der 26.
    // Die alte Formel läse "heute" (UTC) als den 25. und käme auf diffDays = 1
    // → fälschlich "in 1 Tag" statt "heute".
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-25T23:30:00Z'))
    const card = makeCard({ dueDate: '2026-10-26', columnId: 'col-todo' })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-date-badge')
    expect(badge).toBeTruthy()
    expect(badge.title).toContain(I18n.t('date.today')) /* #972: Restzeit im Tooltip */
    expect(badge.classList.contains('card-date-badge--overdue')).toBe(false)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 6) Filter-Klasse card--filtered (war 5)
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Filter-Klasse card--filtered', () => {
  it('Karte ohne aktiven Filter hat keine card--filtered-Klasse', () => {
    Filter.clear()
    const card = makeCard({ owner: 'Andi' })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.classList.contains('card--filtered')).toBe(false)
  })

  it('Karte mit aktivem Filter ohne Match bekommt card--filtered', () => {
    Filter.set({ owner: 'Claude' })
    const card = makeCard({ owner: 'Andi' })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.classList.contains('card--filtered')).toBe(true)
  })

  it('Karte mit aktivem Filter MIT Match hat keine card--filtered-Klasse', () => {
    Filter.set({ owner: 'Claude' })
    const card = makeCard({ owner: 'Claude' })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.classList.contains('card--filtered')).toBe(false)
  })

  it('Critical-Flag-Filter: Karte ohne critical bekommt card--filtered', () => {
    Filter.set({ flagToggle: 'critical' })
    const card = makeCard({ critical: false })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.classList.contains('card--filtered')).toBe(true)
  })

  it('Critical-Flag-Filter: Karte mit critical hat keine card--filtered', () => {
    Filter.set({ flagToggle: 'critical' })
    const card = makeCard({ critical: true })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.classList.contains('card--filtered')).toBe(false)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 6) Spalten-Toggle — Karte #93
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Spalten-Toggle (Karte #93)', () => {
  it('Toggle-Button (.column-toggle) ist im Spalten-Header vorhanden', () => {
    const container = renderCardViaBoard(makeCard())
    const toggleBtn = container.querySelector('.column-toggle')
    expect(toggleBtn).toBeTruthy()
  })

  it('Toggle-Button zeigt initial ▼ (aufgeklappt)', () => {
    const container = renderCardViaBoard(makeCard())
    const toggleBtn = container.querySelector('.column-toggle')
    expect(toggleBtn.textContent).toBe('▼')
  })

  it('Klick auf Toggle versteckt .column-cards', () => {
    const container = renderCardViaBoard(makeCard())
    const toggleBtn = container.querySelector('.column-toggle')
    const cardsEl   = container.querySelector('.column-cards')
    expect(cardsEl).toBeTruthy()
    toggleBtn.click()
    expect(cardsEl.style.display).toBe('none')
  })

  it('Klick auf Toggle ändert Pfeil-Symbol auf ▶', () => {
    const container = renderCardViaBoard(makeCard())
    const toggleBtn = container.querySelector('.column-toggle')
    toggleBtn.click()
    expect(toggleBtn.textContent).toBe('▶')
  })

  it('Zweiter Klick blendet .column-cards wieder ein', () => {
    const container = renderCardViaBoard(makeCard())
    const toggleBtn = container.querySelector('.column-toggle')
    const cardsEl   = container.querySelector('.column-cards')
    toggleBtn.click()
    expect(cardsEl.style.display).toBe('none')
    toggleBtn.click()
    expect(cardsEl.style.display).toBe('')
  })

  it('Toggle versteckt auch .column-footer beim Zuklappen', () => {
    const container = renderCardViaBoard(makeCard())
    const toggleBtn = container.querySelector('.column-toggle')
    const footer    = container.querySelector('.column-footer')
    expect(footer).toBeTruthy()
    toggleBtn.click()
    expect(footer.style.display).toBe('none')
  })

  it('aria-label wird beim Toggle aktualisiert', () => {
    const container = renderCardViaBoard(makeCard())
    const toggleBtn = container.querySelector('.column-toggle')
    const initialLabel = toggleBtn.getAttribute('aria-label')
    toggleBtn.click()
    const collapsedLabel = toggleBtn.getAttribute('aria-label')
    expect(collapsedLabel).not.toBe(initialLabel)
    toggleBtn.click()
    expect(toggleBtn.getAttribute('aria-label')).toBe(initialLabel)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 7) Board-Titel — Dirty-Stern
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Dirty-Stern im Board-Titel', () => {
  it('Board-Titel ohne dirty hat kein * -Präfix', () => {
    State.markClean()
    const board = makeBoard()
    setupBoardDOM(board)
    const titleEl = document.querySelector('#board-title')
    expect(titleEl).toBeTruthy()
    expect(titleEl.textContent.startsWith('* ')).toBe(false)
    expect(titleEl.textContent).toBe(board.title)
  })

  it('Board-Titel mit dirty=true hat * -Präfix', () => {
    // State.set() setzt _dirty = true
    State.set({ board: makeBoard() })
    const board = makeBoard()
    document.body.innerHTML = ''
    setupBoardDOM(board)
    const titleEl = document.querySelector('#board-title')
    expect(titleEl).toBeTruthy()
    expect(titleEl.textContent.startsWith('* ')).toBe(true)
  })
})

describe('Renderer — Board-Titel: Stift benennt um, Titelklick öffnet das Menü (Karte #704)', () => {
  it('Klick auf den Stift ersetzt den Titelblock durch das Eingabefeld und meldet board:title-change', () => {
    State.markClean()
    const board = makeBoard()
    setupBoardDOM(board)
    const spy = vi.fn()
    EventBus.on('board:title-change', spy)
    document.querySelector('.board-title-edit').click()
    const input = document.querySelector('#board-title-input')
    expect(input).toBeTruthy()
    expect(document.querySelector('.board-title-wrap')).toBeNull()
    input.value = 'Neuer Name'
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(spy).toHaveBeenCalledWith({ title: 'Neuer Name' })
  })

  it('F-1732: ein laufendes Umbenennen übersteht einen Render (Wert, Auswahl, kein Speichern)', () => {
    State.markClean()
    const board = makeBoard()
    setupBoardDOM(board)
    const spy = vi.fn()
    EventBus.on('board:title-change', spy)
    document.querySelector('.board-title-edit').click()
    const alt = document.querySelector('#board-title-input')
    alt.value = 'Halb getippt'
    alt.setSelectionRange(2, 5)
    Renderer.renderBoard({ board })
    const neu = document.querySelector('#board-title-input')
    expect(neu).toBeTruthy()
    expect(neu).not.toBe(alt)
    expect(neu.value).toBe('Halb getippt')
    expect([neu.selectionStart, neu.selectionEnd]).toEqual([2, 5])
    expect(spy).not.toHaveBeenCalled()
  })

  /* main.js nachgestellt: board:title-change rendert über mutateBoard → state:changed → renderBoard
     SYNCHRON neu, noch bevor das Feld entfernt ist (F-1737). */
  describe('Umbenennen mit synchronem Render (F-1737)', () => {
    let board, off
    const key = (i, k) => i.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true }))
    const starten = () => { document.querySelector('.board-title-edit').click(); return document.querySelector('#board-title-input') }
    beforeEach(() => {
      State.markClean()
      board = makeBoard()
      setupBoardDOM(board)
      const h = ({ title }) => { board = { ...board, title }; Renderer.renderBoard({ board }) }
      EventBus.on('board:title-change', h)
      off = () => EventBus.off('board:title-change', h)
    })
    afterEach(() => off())

    it('Enter speichert, das Feld ist zu, der Titel zeigt den neuen Text', () => {
      const i = starten(); i.value = 'Neu'
      key(i, 'Enter')
      expect(board.title).toBe('Neu')
      expect(document.querySelector('#board-title-input')).toBeNull()
      expect(document.querySelector('#board-title').textContent).toContain('Neu')
    })

    it('Esc verwirft: Feld zu, Titel unverändert; ein späteres blur speichert nicht', () => {
      const spy = vi.fn(); EventBus.on('board:title-change', spy)
      const i = starten(); i.value = 'Verworfen'
      key(i, 'Escape')
      expect(document.querySelector('#board-title-input')).toBeNull()
      expect(board.title).toBe('Test-Board')
      i.dispatchEvent(new Event('blur')); i.onblur?.()
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy).toHaveBeenCalledWith({ title: 'Test-Board' })
      EventBus.off('board:title-change', spy)
    })

    it('blur speichert genau einmal und schließt das Feld', () => {
      const spy = vi.fn(); EventBus.on('board:title-change', spy)
      const i = starten(); i.value = 'PerBlur'
      i.onblur()
      expect(board.title).toBe('PerBlur')
      expect(document.querySelector('#board-title-input')).toBeNull()
      expect(spy).toHaveBeenCalledTimes(1)
      EventBus.off('board:title-change', spy)
    })

    it('Enter, dann blur am alten Feld: kein doppeltes Speichern', () => {
      const spy = vi.fn(); EventBus.on('board:title-change', spy)
      const i = starten(); i.value = 'Einmal'
      key(i, 'Enter'); i.onblur?.()
      expect(spy).toHaveBeenCalledTimes(1)
      EventBus.off('board:title-change', spy)
    })

    it('Board-Wechsel verwirft ein offenes Feld, ohne zu speichern; das neue Board erbt den Text nicht', () => {
      const spy = vi.fn(); EventBus.on('board:title-change', spy)
      const i = starten(); i.value = 'Alter Text'
      board = { ...makeBoard(), id: 'board-anders', title: 'Anderes Board' }
      Renderer.renderBoard({ board })
      expect(document.querySelector('#board-title-input')).toBeNull()
      expect(document.querySelector('#board-title').textContent).toContain('Anderes Board')
      i.dispatchEvent(new Event('blur')); i.onblur?.()
      expect(spy).not.toHaveBeenCalled()
      EventBus.off('board:title-change', spy)
    })
  })

  it('F-1740: das Entfernen des alten Feldes beim Render löst blur aus — es darf nicht speichern', () => {
    State.markClean()
    const board = makeBoard()
    setupBoardDOM(board)
    const spy = vi.fn()
    EventBus.on('board:title-change', spy)
    document.querySelector('.board-title-edit').click()
    const alt = document.querySelector('#board-title-input')
    alt.value = 'Halb getippt'
    /* Browser feuern blur beim Entfernen eines fokussierten Feldes; happy-dom nicht — nachgestellt. */
    const orig = Node.prototype.removeChild
    const rc = vi.spyOn(Node.prototype, 'removeChild').mockImplementation(function (k) {
      if (k === alt) alt.onblur?.(new Event('blur'))
      return orig.call(this, k)
    })
    Renderer.renderBoard({ board })
    rc.mockRestore()
    expect(document.querySelector('#board-title-input').value).toBe('Halb getippt')
    expect(spy).not.toHaveBeenCalled()
    EventBus.off('board:title-change', spy)
  })

  it('Klick auf den Titel startet kein Umbenennen', () => {
    State.markClean()
    setupBoardDOM(makeBoard())
    document.querySelector('#board-title').click()
    expect(document.querySelector('#board-title-input')).toBeNull()
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 8) Sprint-Header Polish — Karte #126
// ─────────────────────────────────────────────────────────────────────────

/**
 * Baut ein Board mit zwei Sprints, sodass der Swimlane-Modus aktiv wird
 * und renderSprintHeader + Progress-Linie gerendert werden.
 */
function makeSwimlaneBoard(sprint1Cards = [], sprint2Cards = []) {
  const cid = `col-${nextId()}`
  const s1id = `sprint-${nextId()}`
  const s2id = `sprint-${nextId()}`
  const colDefs = [{ id: cid, title: 'Todo' }]
  return {
    id:         'board-swimlane',
    title:      'Swimlane-Board',
    columnDefs: colDefs,
    sprints: [
      { id: s1id, title: 'Sprint A', cards: sprint1Cards.map(c => ({ ...c, columnId: cid })) },
      { id: s2id, title: 'Sprint B', cards: sprint2Cards.map(c => ({ ...c, columnId: cid })) },
    ],
  }
}

describe('Renderer — Sprint Progress-Linie (Karte #126)', () => {
  it('sprint-progress-line existiert in jeder Swimlane', () => {
    const board = makeSwimlaneBoard()
    const container = setupBoardDOM(board)
    const lines = container.querySelectorAll('.sprint-progress-line')
    expect(lines.length).toBe(2) /* je eine pro Sprint */
  })

  it('sprint-progress-fill hat 0% Breite wenn keine Karten erledigt', () => {
    const board = makeSwimlaneBoard([
      { id: 'c1', displayId: 1, title: 'Karte', description: '', labels: [],
        owner: '', priority: 'mittel', complexity: '', effort: null,
        qa: false, critical: false, blocked: false, archived: false,
        columnId: 'x', created: '2026-01-01', updated: '2026-01-01' }
    ])
    const container = setupBoardDOM(board)
    const fills = container.querySelectorAll('.sprint-progress-fill')
    /* Erste Swimlane: 1 Karte, 0 erledigt → 0% */
    expect(fills[0].style.width).toBe('0.00%')
  })

  it('sprint-progress-fill zeigt karten-basierte Quote, nicht effort-basierte', () => {
    /* Bug-Regression: 4 geschätzte Karten alle erledigt + 2 ungeschätzte offen
       → Effort-Basis wäre 100%, Karten-Basis ist 4/6 ≈ 66.67% */
    const doneCard = (id) => ({
      id, displayId: +id, title: `Karte ${id}`, description: '', labels: [],
      owner: '', priority: 'mittel', complexity: '', effort: 2,
      qa: true, critical: false, blocked: false, archived: false,
      columnId: 'col-erledigt', created: '2026-01-01', updated: '2026-01-02',
    })
    const openCard = (id) => ({
      id, displayId: +id, title: `Karte ${id}`, description: '', labels: [],
      owner: '', priority: 'mittel', complexity: '', effort: null,
      qa: false, critical: false, blocked: false, archived: false,
      columnId: 'col-todo', created: '2026-01-01', updated: '2026-01-01',
    })
    const cid = `col-${nextId()}`
    const board = {
      id: 'board-bug', title: 'Bug-Board',
      columnDefs: [{ id: cid, title: 'Todo' }, { id: 'col-erledigt', title: 'Erledigt' }],
      sprints: [
        {
          id: `sprint-${nextId()}`, title: 'Sprint Bug', cards: [
            { ...doneCard('d1'), columnId: 'col-erledigt' },
            { ...doneCard('d2'), columnId: 'col-erledigt' },
            { ...doneCard('d3'), columnId: 'col-erledigt' },
            { ...doneCard('d4'), columnId: 'col-erledigt' },
            { ...openCard('o1'), columnId: cid },
            { ...openCard('o2'), columnId: cid },
          ],
        },
        { id: `sprint-${nextId()}`, title: 'Sprint Dummy', cards: [] },
      ],
    }
    const container = setupBoardDOM(board)
    const fills = container.querySelectorAll('.sprint-progress-fill')
    /* Erste Swimlane: 4/6 = 66.67% */
    expect(fills[0].style.width).toBe('66.67%')
    /* NICHT 100% */
    expect(fills[0].style.width).not.toBe('100.00%')
  })
})

describe('Renderer — Sprint-Title Truncation (Karte #126)', () => {
  it('.sprint-title ist vorhanden', () => {
    const board = makeSwimlaneBoard()
    const container = setupBoardDOM(board)
    const title = container.querySelector('.sprint-title')
    expect(title).toBeTruthy()
  })

  it('.sprint-title title-Attribut enthält den Sprint-Titel', () => {
    const cid = `col-${nextId()}`
    const board = {
      id: 'board-t', title: 'T',
      columnDefs: [{ id: cid, title: 'Col' }],
      sprints: [
        { id: `sprint-${nextId()}`, title: 'Sehr langer Sprint-Name der gekürzt werden soll', cards: [] },
        { id: `sprint-${nextId()}`, title: 'Sprint Zwei', cards: [] },
      ],
    }
    const container = setupBoardDOM(board)
    const titles = container.querySelectorAll('.sprint-title')
    expect(titles[0].getAttribute('title')).toBe('Sehr langer Sprint-Name der gekürzt werden soll')
  })

  it('.sprint-title hat aria-describedby auf versteckten Rename-Hinweis', () => {
    const board = makeSwimlaneBoard()
    const container = setupBoardDOM(board)
    const title = container.querySelector('.sprint-title')
    const describedBy = title?.getAttribute('aria-describedby')
    expect(describedBy).toBeTruthy()
    const hint = container.querySelector(`#${describedBy}`)
    expect(hint).toBeTruthy()
    expect(hint.classList.contains('visually-hidden')).toBe(true)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// Erledigt-Spalte per Titel-Semantik statt fester id (F-250)
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Erledigt-Spalte .is-done (F-250)', () => {
  it('Titel "Erledigt" mit UUID-id (nicht col-erledigt) bekommt .is-done', () => {
    const board = makeBoard([], [{ id: '5f2a3b1c-uuid', title: 'Erledigt' }])
    const col = setupBoardDOM(board).querySelector('.column')
    expect(col.dataset.id).toBe('5f2a3b1c-uuid')
    expect(col.classList.contains('is-done')).toBe(true)
  })

  it('id="col-erledigt" aber Nicht-Done-Titel bekommt KEINE .is-done (id irrelevant)', () => {
    const board = makeBoard([], [{ id: 'col-erledigt', title: 'Backlog' }])
    const col = setupBoardDOM(board).querySelector('.column')
    expect(col.classList.contains('is-done')).toBe(false)
  })

  it('Done-Titel case-insensitive + getrimmt (" DONE ")', () => {
    const board = makeBoard([], [{ id: 'x-1', title: ' DONE ' }])
    const col = setupBoardDOM(board).querySelector('.column')
    expect(col.classList.contains('is-done')).toBe(true)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// Backlog-Meta-Sortierung per Titel-Semantik statt fester id (F-242)
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Backlog-Meta-Sortierung (F-242)', () => {
  /* Board mit 2 Sprints (≥2 → swimlane-Modus, sonst greift der isFlat-Pfad
     ohne Meta-Sortierung). Erste Swimlane = Test-Sprint, zweite nur Füller. */
  function metaBoard(colDef) {
    Renderer.resetSwimlaneState()
    const colId = colDef.id
    const start  = { id: 'm-start', kind: 'sprint-meta', metaType: 'start', title: '📋 Sprint-Start',     columnId: colId }
    const normal = { ...makeCard({ id: 'n1', displayId: 1, title: 'Normal' }), columnId: colId }
    const end    = { id: 'm-end',   kind: 'sprint-meta', metaType: 'end',   title: '🏁 Sprint-Abschluss', columnId: colId }
    return {
      id: 'board-f242', title: 'F242', columnDefs: [colDef],
      sprints: [
        /* Eingabe-Reihenfolge bewusst verdreht: [normal, end, start] */
        { id: `sp-f242-${nextId()}`, title: 'Sprint A', kind: 'sprint', cards: [normal, end, start] },
        { id: `sp-f242-${nextId()}`, title: 'Sprint B', kind: 'sprint', cards: [] },
      ],
    }
  }

  function cardOrder(container) {
    const col = container.querySelector('.column')
    return [...col.querySelectorAll('.card')].map(c => c.dataset.id)
  }

  it('Titel "Backlog" mit UUID-id (nicht col-backlog) → Meta-Sortierung greift (📋 oben, 🏁 unten)', () => {
    const container = setupBoardDOM(metaBoard({ id: 'a1b2-uuid', title: 'Backlog' }))
    expect(cardOrder(container)).toEqual(['m-start', 'n1', 'm-end'])
  })

  it('id="col-backlog" aber Nicht-Backlog-Titel → KEINE Meta-Sortierung (id irrelevant)', () => {
    const container = setupBoardDOM(metaBoard({ id: 'col-backlog', title: 'Aktiv' }))
    /* unsortiert = Eingabe-Reihenfolge [normal, end, start] bleibt erhalten */
    expect(cardOrder(container)).toEqual(['n1', 'm-end', 'm-start'])
  })

  it('Backlog-Titel case-insensitive + getrimmt (" BACKLOG ")', () => {
    const container = setupBoardDOM(metaBoard({ id: 'x-9', title: ' BACKLOG ' }))
    expect(cardOrder(container)).toEqual(['m-start', 'n1', 'm-end'])
  })
})

// ─────────────────────────────────────────────────────────────────────────
// Sprint-Aktivierungs-Sweep: reduced-motion-Gating (F-240)
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Sprint-Sweep reduced-motion (F-240)', () => {
  /* ≥2 Sprints (swimlane-Modus → renderSprintHeader läuft). Start-Meta-Karte
     mit frischem sprint-activate-Activity-Eintrag (< 4 s) triggert den Sweep. */
  function activatedBoard() {
    Renderer.resetSwimlaneState()
    const start = {
      id: 'm-start', kind: 'sprint-meta', metaType: 'start', title: '📋 Sprint-Start',
      columnId: 'c1', activity: [{ type: 'sprint-activate', timestamp: new Date().toISOString() }],
    }
    return {
      id: 'b-f240', title: 'F240', columnDefs: [{ id: 'c1', title: 'Backlog' }],
      sprints: [
        { id: `sp-f240-${nextId()}`, title: 'Sprint A', kind: 'sprint', cards: [start] },
        { id: `sp-f240-${nextId()}`, title: 'Sprint B', kind: 'sprint', cards: [] },
      ],
    }
  }

  it('ohne reduced-motion: Header bekommt sprint-header--just-activated', () => {
    const container = setupBoardDOM(activatedBoard())
    const header = container.querySelector('.sprint-header')
    expect(header.classList.contains('sprint-header--just-activated')).toBe(true)
  })

  it('mit prefers-reduced-motion: Sweep-Klasse wird gar nicht erst gesetzt', () => {
    const orig = window.matchMedia
    window.matchMedia = () => ({ matches: true })
    try {
      const container = setupBoardDOM(activatedBoard())
      const header = container.querySelector('.sprint-header')
      expect(header.classList.contains('sprint-header--just-activated')).toBe(false)
    } finally {
      window.matchMedia = orig
    }
  })
})

// ─────────────────────────────────────────────────────────────────────────
// Inbox-Kompaktierungs-Banner (ADR-0071 Achse 2D — Karte #230 Task 5)
// ─────────────────────────────────────────────────────────────────────────

describeProjektor('Renderer — Kompaktierungs-Banner (ADR-0071 Achse 2D)', () => {
  it('unter der Schwelle: KEIN Banner', () => {
    Sync.getInboxOpCount.mockReturnValue(COMPACT_OP_THRESHOLD - 1)
    setupBoardDOM(makeBoard([makeCard()]))
    expect(document.getElementById('compaction-banner')).toBe(null)
  })

  it('ab der Schwelle: Banner mit Op-Zahl + Aktions-Button', () => {
    Sync.getInboxOpCount.mockReturnValue(COMPACT_OP_THRESHOLD)
    setupBoardDOM(makeBoard([makeCard()]))
    const banner = document.getElementById('compaction-banner')
    expect(banner).toBeTruthy()
    expect(banner.textContent).toContain(String(COMPACT_OP_THRESHOLD))
    expect(banner.querySelector('button')).toBeTruthy()
    /* F-2876: kein role=status am frisch erzeugten Banner; Ansage über die Live-Region */
    expect(banner.getAttribute('role')).toBeNull()
    const region = document.body.querySelector(':scope > .visually-hidden[role="status"]')
    expect(region.textContent).toContain(String(COMPACT_OP_THRESHOLD))
  })

  it('Button-Klick ruft Sync.requestCompaction (hängt nur Marker-Op an), Button danach disabled', async () => {
    Sync.getInboxOpCount.mockReturnValue(COMPACT_OP_THRESHOLD + 5)
    Sync.requestCompaction.mockResolvedValueOnce(true)
    setupBoardDOM(makeBoard([makeCard()]))
    const btn = document.getElementById('compaction-banner').querySelector('button')
    btn.click()
    await Promise.resolve(); await Promise.resolve()
    expect(Sync.requestCompaction).toHaveBeenCalledTimes(1)
    expect(btn.disabled).toBe(true)
  })

  it('schrumpft die Inbox unter die Schwelle: Banner verschwindet beim Re-Render', () => {
    Sync.getInboxOpCount.mockReturnValue(COMPACT_OP_THRESHOLD + 1)
    const board = makeBoard([makeCard()])
    setupBoardDOM(board)
    expect(document.getElementById('compaction-banner')).toBeTruthy()
    /* Projektor hat kompaktiert → Op-Zahl fällt unter die Schwelle */
    Sync.getInboxOpCount.mockReturnValue(0)
    Renderer.renderBoard({ board })
    expect(document.getElementById('compaction-banner')).toBe(null)
  })
})

/* ── Karte #279: Massen-Archiv-Button im Erledigt-Spalten-Header ─────── */

describe('Renderer — Massen-Archiv (#279)', () => {
  function renderCols(cards, colDef) {
    const board = {
      id: 'b', title: 'T', columnDefs: [colDef],
      sprints: [{ id: `s-${nextId()}`, title: 'S',
        cards: cards.map(c => ({ ...c, columnId: colDef.id })) }],
    }
    return setupBoardDOM(board)
  }

  it('zeigt den Button in einer Erledigt-Spalte mit nicht-archivierten Nicht-Meta-Karten', () => {
    const c = renderCols(
      [makeCard({ qa: true }), makeCard({ qa: true })],
      { id: 'col-erledigt', title: 'Erledigt' },
    )
    expect(c.querySelector('.btn-col-archive-all')).not.toBeNull()
  })

  it('zeigt den Button NICHT in einer Nicht-Erledigt-Spalte', () => {
    const c = renderCols([makeCard()], { id: 'col-todo', title: 'Backlog' })
    expect(c.querySelector('.btn-col-archive-all')).toBeNull()
  })

  it('zeigt den Button NICHT wenn nur Meta-/archivierte Karten vorhanden sind', () => {
    const c = renderCols(
      [makeCard({ kind: 'sprint-meta', metaType: 'end' }),
       makeCard({ archived: true })],
      { id: 'col-erledigt', title: 'Erledigt' },
    )
    expect(c.querySelector('.btn-col-archive-all')).toBeNull()
  })

  it('Klick (bestätigt) emittiert col:archive-done mit colId UND sprintId (F-NEU-2)', () => {
    const origConfirm = window.confirm
    window.confirm = () => true
    const spy = vi.fn()
    EventBus.on('col:archive-done', spy)
    try {
      const c = renderCols([makeCard({ qa: true })],
        { id: 'col-erledigt', title: 'Erledigt' })
      c.querySelector('.btn-col-archive-all').click()
      expect(spy).toHaveBeenCalledTimes(1)
      const arg = spy.mock.calls[0][0]
      expect(arg.colId).toBe('col-erledigt')
      /* F-NEU-2: sprintId muss mitkommen, damit der Handler sprint-scopen
         kann (Confirm-Zahl == Wirkung). */
      expect(typeof arg.sprintId).toBe('string')
      expect(arg.sprintId.length).toBeGreaterThan(0)
    } finally {
      window.confirm = origConfirm
    }
  })
})

/* ── Karte #287 / F-325: Findability — Toggle 👁 vs. Sammel-Aktion 📦 ─── */

describe('Renderer — Archiv-Buttons sind visuell+a11y getrennt (#287/F-325)', () => {
  function renderCols(cards, colDef) {
    const board = {
      id: 'b', title: 'T', columnDefs: [colDef],
      sprints: [{ id: `s-${nextId()}`, title: 'S',
        cards: cards.map(c => ({ ...c, columnId: colDef.id })) }],
    }
    return setupBoardDOM(board)
  }

  it('Erledigt-Spalte mit archivierter + nicht-archivierter Karte: BEIDE Buttons sichtbar', () => {
    const c = renderCols(
      [makeCard({ qa: true }), makeCard({ qa: true, archived: true })],
      { id: 'col-erledigt', title: 'Erledigt' },
    )
    expect(c.querySelector('.btn-col-archive-toggle')).not.toBeNull()
    expect(c.querySelector('.btn-col-archive-all')).not.toBeNull()
  })

  it('Glyphen unterscheiden sich: Toggle = 👁, Sammel-Aktion = 📦 (kein Konflikt)', () => {
    const c = renderCols(
      [makeCard({ qa: true }), makeCard({ qa: true, archived: true })],
      { id: 'col-erledigt', title: 'Erledigt' },
    )
    const toggle = c.querySelector('.btn-col-archive-toggle')
    const bulk = c.querySelector('.btn-col-archive-all')
    expect(toggle.textContent).toContain('👁')
    expect(toggle.textContent).not.toContain('📦')
    expect(bulk.textContent).toContain('📦')
    expect(bulk.textContent).not.toContain('👁')
  })

  it('Aria-Labels unterscheiden sich (Toggle vs. Sammel-Aktion)', () => {
    const c = renderCols(
      [makeCard({ qa: true }), makeCard({ qa: true, archived: true })],
      { id: 'col-erledigt', title: 'Erledigt' },
    )
    const toggleAria = c.querySelector('.btn-col-archive-toggle').getAttribute('aria-label')
    const bulkAria = c.querySelector('.btn-col-archive-all').getAttribute('aria-label')
    expect(toggleAria).toBeTruthy()
    expect(bulkAria).toBeTruthy()
    expect(toggleAria).not.toBe(bulkAria)
    /* DE-/EN-agnostisch — keine String-Identität, nur Trennung. */
  })
})

/* ── Karte #329: Activity-Tail-Tab im Footer (IS_PROJECTOR_BUILD, alle Modi) ─ */

describeProjektor('Renderer — Activity-Tail-Tab im Footer (#329)', () => {
  beforeEach(() => {
    document.getElementById('chat-tail-footer')?.remove()
    /* activityTailEnabled default true — Tab erscheint in allen Modi */
    Config.set('boardMode', 'solo')
    Config.set('activityTailEnabled', true)
    Config.set('chatTailEnabled', false)
  })

  it('Footer erscheint im Solo-Modus wenn activityTailEnabled=true', () => {
    setupBoardDOM(makeBoard([makeCard()]))
    expect(document.getElementById('chat-tail-footer')).toBeTruthy()
  })

  it('Footer bleibt weg wenn beide Tabs deaktiviert sind', () => {
    Config.set('activityTailEnabled', false)
    Config.set('chatTailEnabled', false)
    setupBoardDOM(makeBoard([makeCard()]))
    expect(document.getElementById('chat-tail-footer')).toBeFalsy()
  })

  it('Activity-Tab zeigt Einträge sortiert nach ts absteigend (jüngster zuerst)', () => {
    const card = makeCard({
      activity: [
        { id: '1', author: 'A', action: 'owner-changed', from: '', to: 'X',
          timestamp: '2026-05-01T10:00:00Z' },
        { id: '2', author: 'B', action: 'column-moved', from: 'col-a', to: 'col-b',
          timestamp: '2026-05-19T10:00:00Z' },
        { id: '3', author: 'C', action: 'field-updated', field: 'qa', from: 'false', to: 'true',
          timestamp: '2026-05-10T10:00:00Z' },
      ],
    })
    setupBoardDOM(makeBoard([card]))
    const footer = document.getElementById('chat-tail-footer')
    footer.querySelector('.chat-tail-footer__head').click() /* aufklappen */
    const entries = footer.querySelectorAll('.chat-tail-footer__act-entry')
    expect(entries).toHaveLength(3)
    expect(entries[0].textContent).toContain('B')
    expect(entries[1].textContent).toContain('C')
    expect(entries[2].textContent).toContain('A')
  })

  it('Activity-Tab: Leeres Board → Empty-State', () => {
    setupBoardDOM(makeBoard([]))
    const footer = document.getElementById('chat-tail-footer')
    footer.querySelector('.chat-tail-footer__head').click()
    expect(footer.querySelector('.chat-tail-footer__empty')).toBeTruthy()
    expect(footer.querySelectorAll('.chat-tail-footer__act-entry')).toHaveLength(0)
  })

  it('Activity-Aggregat ist board-weit (Karten in verschiedenen Sprints)', () => {
    const c1 = makeCard({ id: 'a', activity: [
      { id: '1', author: 'A', action: 'owner-changed', from: '', to: 'X',
        timestamp: '2026-05-19T10:00:00Z' },
    ]})
    const c2 = makeCard({ id: 'b', activity: [
      { id: '2', author: 'B', action: 'column-moved', from: 'x', to: 'y',
        timestamp: '2026-05-19T11:00:00Z' },
    ]})
    const board = {
      id: 'b', title: 'T', columnDefs: [{ id: 'c1', title: 'Backlog' }],
      sprints: [
        { id: 's1', title: 'S1', cards: [{ ...c1, columnId: 'c1' }] },
        { id: 's2', title: 'S2', cards: [{ ...c2, columnId: 'c1' }] },
      ],
    }
    setupBoardDOM(board)
    const footer = document.getElementById('chat-tail-footer')
    footer.querySelector('.chat-tail-footer__head').click()
    const entries = footer.querySelectorAll('.chat-tail-footer__act-entry')
    expect(entries).toHaveLength(2)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// Chat-Tail-Footer (Karte #290 + #329) — Tab-System + claude-code-only
// ─────────────────────────────────────────────────────────────────────────

/* Karte #290: Im Server-Build (vitest.config.server.js) ist boardMode
   compile-time auf 'server' fixiert (Config.set('boardMode', ...) ist no-op).
   Die Chat-Tail-Sektion ist per Definition nur in 'claude-code' relevant —
   diese Tests laufen daher nur im Standalone-Vitest-Kontext. */
const isServerBuildCtx = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true')

describe.skipIf(isServerBuildCtx || !IS_PROJECTOR_BUILD)('Renderer — Chat-Tail-Footer (#290)', () => {
  beforeEach(() => {
    /* Footer-Reste aus vorherigen Tests aufräumen (renderChatTailFooter
       benutzt fixiertes id="chat-tail-footer", das beforeEach-DOM-Reset
       deckt das ab, aber explizit ist sicherer). */
    document.getElementById('chat-tail-footer')?.remove()
    /* Default-Modus + beide Features aus, damit keine Tab-Überlagerung entsteht */
    Config.set('boardMode', 'solo')
    Config.set('chatTailEnabled', false)
    Config.set('activityTailEnabled', false)
  })

  afterEach(() => {
    /* F-923/Karte #489: GELECKTER ZUSTAND — `_chatTailListenerBound` (Modul-
       Zustand in src/renderer.js) bleibt `true` stehen, wenn ein Test mit
       chatEnabled=true (boardMode 'claude-code' + chatTailEnabled=true)
       endet, ohne wieder abzuschalten — jeder Test hier ausser dem
       Off-Path-Tests am Anfang tut genau das ("claude-code + chatTailEnabled
       an, …", "Klick auf Head …", "F-330 …"). Das obige `beforeEach` setzt
       nur die Config-Werte zurück, nicht den Bind-Zustand selbst — der
       ändert sich nur, wenn tatsächlich gerendert wird. `renderChatTailFooter`
       bindet den Sync-Listener aber nur EINMAL, solange
       `_chatTailListenerBound` true ist (Zeile ~2298 in src/renderer.js) —
       jeder weitere `Sync.setChatTailListener`-Aufruf wird dann übersprungen.
       Symptom, das das ursprünglich mit einem Reihenfolge-Kommentar
       ("MUSS vor allen anderen … laufen") statt einem Fix umschifft wurde:
       Der Test "Listener-Update mit Einträgen …" registriert seinen Mock
       über `mockImplementation`, bekommt aber nie einen Aufruf, wenn der
       Listener von einem VORGÄNGER-Test unter Shuffle schon gebunden ist —
       `captured` bleibt `null` statt einer Funktion.
       Reset: über denselben Aus-Pfad, den der F-330-Test bereits als
       korrekt verifiziert (chatEnabled=false + Render löst
       `Sync.setChatTailListener(null)` aus und setzt
       `_chatTailListenerBound` zurück auf false) — synchron, kein await
       nötig. */
    Config.set('boardMode', 'solo')
    Config.set('chatTailEnabled', false)
    setupBoardDOM(makeBoard([makeCard()]))
  })

  it('Solo-Modus, nur chatTailEnabled=true → Chat-Tab sichtbar (aber activityTail aus)', () => {
    Config.set('boardMode', 'solo')
    Config.set('chatTailEnabled', true)
    /* chatTail ist claude-code-only → Footer bleibt weg wenn nur chatTail an */
    setupBoardDOM(makeBoard([makeCard()]))
    expect(document.getElementById('chat-tail-footer')).toBeFalsy()
  })

  it('claude-code-Modus + beide Features aus → Footer wird NICHT gerendert', () => {
    Config.set('boardMode', 'claude-code')
    Config.set('chatTailEnabled', false)
    Config.set('activityTailEnabled', false)
    setupBoardDOM(makeBoard([makeCard()]))
    expect(document.getElementById('chat-tail-footer')).toBeFalsy()
  })

  /* Der Renderer bindet den Sync-Listener nur einmal pro Modul-Singleton.
     Früher musste dieser Test deshalb vor allen anderen claude-code+enabled-
     Tests stehen; seit F-923 (3fb40da) setzt das afterEach den Bind-Zustand
     zurück, die Reihenfolge ist frei.
     activityTailEnabled=false damit _footerActiveTab='chat' erzwungen wird. */
  it('Listener-Update mit Einträgen → Body listet sie + latest-Span zeigt jüngste Zeile', () => {
    Config.set('boardMode', 'claude-code')
    Config.set('chatTailEnabled', true)
    Config.set('activityTailEnabled', false)
    let captured = null
    Sync.setChatTailListener.mockImplementation(fn => { captured = fn })
    setupBoardDOM(makeBoard([makeCard()]))
    expect(captured).toBeTypeOf('function')
    captured([
      { ts: '2026-05-20T10:00:00Z', kind: 'user',      snippet: 'erster prompt' },
      { ts: '2026-05-20T10:00:05Z', kind: 'assistant', snippet: 'erste antwort' },
    ])
    const footer = document.getElementById('chat-tail-footer')
    expect(footer.querySelectorAll('.chat-tail-footer__entry')).toHaveLength(2)
    expect(footer.querySelector('.chat-tail-footer__latest').textContent).toContain('erste antwort')
    /* Modul-Singleton-State zurücksetzen, damit folgende Tests neutral starten. */
    captured([])
  })

  it('claude-code + chatTailEnabled an, activityTail aus → Footer rendert mit Empty-State (Chat-Tab aktiv)', () => {
    Config.set('boardMode', 'claude-code')
    Config.set('chatTailEnabled', true)
    Config.set('activityTailEnabled', false)
    setupBoardDOM(makeBoard([makeCard()]))
    const footer = document.getElementById('chat-tail-footer')
    expect(footer).toBeTruthy()
    expect(footer.querySelector('.chat-tail-footer__empty')).toBeTruthy()
    /* Default zugeklappt — latest-Span sichtbar, body display:none via CSS */
    expect(footer.classList.contains('chat-tail-footer--open')).toBe(false)
  })

  it('Klick auf Head klappt Footer auf', () => {
    Config.set('boardMode', 'claude-code')
    Config.set('chatTailEnabled', true)
    Config.set('activityTailEnabled', false)
    setupBoardDOM(makeBoard([makeCard()]))
    const footer = document.getElementById('chat-tail-footer')
    footer.querySelector('.chat-tail-footer__head').click()
    expect(footer.classList.contains('chat-tail-footer--open')).toBe(true)
  })

  it('Wechsel claude-code → solo entfernt den Footer wenn activityTail ebenfalls aus', () => {
    Config.set('boardMode', 'claude-code')
    Config.set('chatTailEnabled', true)
    Config.set('activityTailEnabled', false)
    const board = makeBoard([makeCard()])
    setupBoardDOM(board)
    expect(document.getElementById('chat-tail-footer')).toBeTruthy()
    Config.set('boardMode', 'solo')
    Renderer.renderBoard({ board })
    /* Beide Tabs aus → Footer entfernt */
    expect(document.getElementById('chat-tail-footer')).toBeFalsy()
  })

  it('F-330: Sync-Listener wird beim Wegfallen von chatEnabled gelöst (null) und bei erneutem Einschalten wieder gebunden', () => {
    /* Reihenfolge bewusst: dieser Test schaltet chatEnabled selbst zweimal
       um (an → aus → an) und prüft NUR die relativen Aufrufe innerhalb der
       eigenen drei setupBoardDOM-Aufrufe — unabhängig davon, ob vorherige
       Tests im Datei-Lauf den Listener schon gebunden hatten. */
    Config.set('boardMode', 'claude-code')
    Config.set('chatTailEnabled', true)
    Config.set('activityTailEnabled', false)
    setupBoardDOM(makeBoard([makeCard()]))
    const callsAfterBind = Sync.setChatTailListener.mock.calls.length
    expect(callsAfterBind).toBeGreaterThan(0)
    expect(Sync.setChatTailListener.mock.calls.at(-1)[0]).toBeTypeOf('function')

    // Wechsel weg (Mode-Wechsel) → MUSS den Listener explizit lösen (null),
    // nicht stillschweigend gebunden lassen (das war die F-330-Lücke).
    Config.set('boardMode', 'solo')
    setupBoardDOM(makeBoard([makeCard()]))
    expect(Sync.setChatTailListener.mock.calls.length).toBe(callsAfterBind + 1)
    expect(Sync.setChatTailListener.mock.calls.at(-1)[0]).toBe(null)

    // Wieder aktivieren → MUSS erneut binden (kein Once-Only-Skip mehr).
    Config.set('boardMode', 'claude-code')
    setupBoardDOM(makeBoard([makeCard()]))
    expect(Sync.setChatTailListener.mock.calls.length).toBe(callsAfterBind + 2)
    expect(Sync.setChatTailListener.mock.calls.at(-1)[0]).toBeTypeOf('function')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// Wiederholungs-Badge — aria-label neben title (F-167)
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Wiederholungs-Badge hat aria-label (ADR-0041/F-167)', () => {
  it('🔁-Badge trägt zusätzlich zum title ein gleichlautendes aria-label', () => {
    const card = makeCard({ recurrence: { interval: 'daily', lastSpawned: '2026-05-12' } })
    const container = renderCardViaBoard(card)
    const badge = container.querySelector('.card-recurrence-badge')
    expect(badge).toBeTruthy()
    // Reines Emoji ohne Textalternative ist für Screenreader stumm (F-167) —
    // title allein reicht nicht, aria-label muss zusätzlich gesetzt sein.
    expect(badge.getAttribute('aria-label')).toBeTruthy()
    expect(badge.getAttribute('aria-label')).toBe(badge.title)
    expect(badge.getAttribute('aria-label')).toBe(I18n.t('recurrence.badge-title', I18n.t('recurrence.daily')))
  })
})

// ─────────────────────────────────────────────────────────────────────────
// Sprint-Lösch-Bestätigung zählt Meta-Karten nicht mit (F-154)
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Sprint-Lösch-Bestätigung zählt Meta-Karten nicht mit (F-154)', () => {
  /* Swimlane-Modus (Delete-Button existiert nur ab >1 Sprint) braucht eine
     zweite Swimlane neben der echten Test-Swimlane — sonst greift der
     Flat-Zweig (isFlat = sprints.length <= 1) und es gibt gar keinen
     Sprint-Header mit Lösch-Button. */
  function makeTwoSwimlaneBoard(sprintCards) {
    return {
      id: 'board-f154', title: 'T',
      columnDefs: [
        { id: 'col-backlog',  title: 'Backlog'  },
        { id: 'col-erledigt', title: 'Erledigt' },
      ],
      sprints: [
        { id: '__backlog__', title: 'Backlog',    kind: 'backlog', cards: [] },
        { id: 'sprint-f154', title: 'Sprint F154', kind: 'sprint',  cards: sprintCards },
      ],
    }
  }

  it('Sprint mit ausschließlich Start-/Abschluss-Meta-Karten löst die "leer"-Bestätigung aus (nicht "2 Karten")', () => {
    const metaStart = { id: 'meta-start-f154', kind: 'sprint-meta', metaType: 'start',
      title: '📋 Sprint-Start', columnId: 'col-erledigt', comments: [] }
    const metaEnd   = { id: 'meta-end-f154',   kind: 'sprint-meta', metaType: 'end',
      title: '🏁 Sprint-Abschluss', columnId: 'col-backlog', comments: [] }
    const board = makeTwoSwimlaneBoard([metaStart, metaEnd])
    const container = setupBoardDOM(board)

    const origConfirm = window.confirm
    let capturedMsg = null
    window.confirm = (msg) => { capturedMsg = msg; return false }
    try {
      const deleteBtn = container.querySelector('.btn-sprint-delete')
      expect(deleteBtn).toBeTruthy()
      deleteBtn.click()
      /* Zwei Meta-Karten, null fachliche Karten → muss die LEER-Variante sein,
         nicht `sprint.delete-confirm` mit n=2 ("2 Karte(n) werden verschoben"). */
      expect(capturedMsg).toBe(I18n.t('sprint.delete-confirm-empty', 'Sprint F154'))
      expect(capturedMsg).not.toContain('werden in den Backlog verschoben')
    } finally {
      window.confirm = origConfirm
    }
  })

  it('Sprint mit einer fachlichen Karte neben den Meta-Karten zählt nur die fachliche Karte (n=1, nicht n=3)', () => {
    const metaStart = { id: 'meta-start-f154b', kind: 'sprint-meta', metaType: 'start',
      title: '📋 Sprint-Start', columnId: 'col-erledigt', comments: [] }
    const metaEnd   = { id: 'meta-end-f154b',   kind: 'sprint-meta', metaType: 'end',
      title: '🏁 Sprint-Abschluss', columnId: 'col-backlog', comments: [] }
    const normal    = makeCard({ id: 'normal-f154', columnId: 'col-backlog' })
    const board = makeTwoSwimlaneBoard([metaStart, metaEnd, normal])
    const container = setupBoardDOM(board)

    const origConfirm = window.confirm
    let capturedMsg = null
    window.confirm = (msg) => { capturedMsg = msg; return false }
    try {
      const deleteBtn = container.querySelector('.btn-sprint-delete')
      deleteBtn.click()
      expect(capturedMsg).toBe(I18n.t('sprint.delete-confirm', 'Sprint F154', 1))
    } finally {
      window.confirm = origConfirm
    }
  })
})


/* ─────────────────────────────────────────────────────────────────
   F-214 — Aufraeumen verwaister collapsedColumns-Schluessel.

   Zwei Dinge werden hier geprueft, und das zweite ist das wichtigere:
   dass der Prune-Pass Schluessel FREMDER Boards in Ruhe laesst. Die Config
   speichert nur `sprintId:colId` und gilt ueber Boards hinweg (F-227) — ein
   Schluessel mit unbekannter sprintId ist deshalb nicht beweisbar verwaist,
   und ihn wegzuraeumen waere Verlust fremder Einstellungen statt Hygiene.

   `cleanupCollapsedColumns` selbst hatte bis Sprint 56 ueberhaupt keine
   Abdeckung, obwohl es seit Karte #160 an zwei Loeschpfaden haengt.
   ───────────────────────────────────────────────────────────────── */
describe('collapsedColumns — Aufraeumen (F-214)', () => {
  /** Setzt den Config-Zustand und laedt ihn in das Renderer-Singleton. */
  function setzeKeys(keys) {
    Config.set('collapsedColumns', keys)
    Renderer.resetSwimlaneState()   /* erzwingt Reload aus der Config */
  }
  const gespeicherteKeys = () => Config.get('collapsedColumns')

  it('cleanupCollapsedColumns entfernt passende Schluessel und persistiert', () => {
    setzeKeys(['s1:col-a', 's1:col-b', 's2:col-a'])
    const geaendert = Renderer.cleanupCollapsedColumns(k => k.endsWith(':col-a'))
    expect(geaendert).toBe(true)
    expect(gespeicherteKeys().sort()).toEqual(['s1:col-b'])
  })

  it('cleanupCollapsedColumns meldet false und schreibt nicht, wenn nichts passt', () => {
    setzeKeys(['s1:col-b'])
    const geaendert = Renderer.cleanupCollapsedColumns(() => false)
    expect(geaendert).toBe(false)
    expect(gespeicherteKeys().sort()).toEqual(['s1:col-b'])
  })

  it('pruneCollapsedColumns entfernt einen Schluessel dieses Boards, dessen Spalte es nicht mehr gibt', () => {
    setzeKeys(['s1:col-weg', 's1:col-da'])
    const board = {
      columnDefs: [{ id: 'col-da', title: 'Da' }],
      sprints: [{ id: 's1', title: 'S', cards: [] }],
    }
    expect(Renderer.pruneCollapsedColumns(board)).toBe(true)
    expect(gespeicherteKeys().sort()).toEqual(['s1:col-da'])
  })

  it('pruneCollapsedColumns laesst Schluessel eines FREMDEN Boards unangetastet', () => {
    /* Kernzusage: `fremd:col-weg` sieht wie Muell aus (weder Sprint noch
       Spalte im aktuellen Board), gehoert aber moeglicherweise zu einem
       anderen Board desselben Geraets. Wird er geloescht, verliert der
       Nutzer dort seine eingeklappten Spalten. */
    setzeKeys(['fremd-sprint:col-weg', 's1:col-weg', 's1:col-da'])
    const board = {
      columnDefs: [{ id: 'col-da', title: 'Da' }],
      sprints: [{ id: 's1', title: 'S', cards: [] }],
    }
    expect(Renderer.pruneCollapsedColumns(board)).toBe(true)
    expect(gespeicherteKeys().sort()).toEqual(['fremd-sprint:col-weg', 's1:col-da'])
  })

  it('pruneCollapsedColumns raeumt nichts weg, solange das Board leer/halb geladen ist', () => {
    setzeKeys(['s1:col-weg'])
    expect(Renderer.pruneCollapsedColumns({ columnDefs: [], sprints: [] })).toBe(false)
    expect(Renderer.pruneCollapsedColumns(null)).toBe(false)
    expect(gespeicherteKeys().sort()).toEqual(['s1:col-weg'])
  })

  it('renderBoard ruft den Prune-Pass tatsaechlich auf (Verdrahtung, nicht nur Vorhandensein)', () => {
    /* Ohne diesen Test waere `pruneCollapsedColumns` eine Funktion, die nur
       die eigene Testdatei aufruft — der Config-Eintrag im echten Betrieb
       waechst trotzdem weiter. Genau diese Luecke (Helfer eingefuehrt, aber
       nicht verdrahtet) ist in diesem Projekt schon einmal durchgerutscht. */
    const sid = 'sprint-verdrahtung'
    const cid = 'col-verdrahtung'
    setzeKeys([`${sid}:col-existiert-nicht`, `${sid}:${cid}`])
    const board = {
      id: 'board-test', title: 'Test-Board',
      columnDefs: [{ id: cid, title: 'Backlog' }],
      sprints: [{ id: sid, title: 'Sprint 1', cards: [] }],
    }
    setupBoardDOM(board)
    expect(gespeicherteKeys().sort()).toEqual([`${sid}:${cid}`])
  })

  it('pruneCollapsedColumns entfernt formwidrige Schluessel ohne Trenner', () => {
    setzeKeys(['kaputt', ':col-da', 's1:col-da'])
    const board = {
      columnDefs: [{ id: 'col-da', title: 'Da' }],
      sprints: [{ id: 's1', title: 'S', cards: [] }],
    }
    expect(Renderer.pruneCollapsedColumns(board)).toBe(true)
    expect(gespeicherteKeys().sort()).toEqual(['s1:col-da'])
  })
})

// ─────────────────────────────────────────────────────────────────────────
// F-155 — Spalten-Zähler ist die Anzahl der gerenderten Karten
// ─────────────────────────────────────────────────────────────────────────

/* F-155 meldete, der Zähler schliesse Sprint-Meta-Karten nicht aus („Erledigt:
   2" bei nur einer normalen Karte). Entscheidung Sprint 61: der Zähler bleibt
   die Anzahl der GERENDERTEN Karten. Meta-Karten stehen sichtbar in der Spalte
   (die 📋-Start-Karte liegt im aktiven Sprint in „Erledigt"); eine 1 über zwei
   sichtbaren Karten wäre schlimmer als der gemeldete Fall. Die Stellen, die
   Meta-Karten ausschliessen (Velocity, Archivier-Dialog, archivableCount),
   MESSEN Arbeit — der Spalten-Zähler beschriftet einen Stapel.
   Dieser Test nagelt die Invariante fest, damit die Entscheidung nicht
   stillschweigend gedreht wird. */

describe('Renderer — Spalten-Zähler == gerenderte Karten (F-155)', () => {
  it('Meta-Karte in der Spalte zählt mit — Zähler und .card-Anzahl stimmen überein', () => {
    const metaKarte = makeCard({
      id: 'card-meta', title: '📋 Sprint-Start',
      kind: 'sprint-meta', metaType: 'start', qa: true,
    })
    const board = makeBoard([makeCard({ id: 'card-normal' }), metaKarte])
    const container = setupBoardDOM(board)

    const spalte = container.querySelector('.column')
    const gerendert = spalte.querySelectorAll('.card').length
    expect(gerendert).toBe(2)
    expect(spalte.querySelector('.column-count').textContent).toBe(String(gerendert))
  })

  it('archivierte Karte wird weder gerendert noch gezählt — die Invariante hält auch dort', () => {
    const board = makeBoard([
      makeCard({ id: 'c1', title: 'Sichtbar' }),
      makeCard({ id: 'c2', title: 'Weggeräumt', archived: true }),
    ])
    const container = setupBoardDOM(board)
    const spalte = container.querySelector('.column')

    expect(spalte.querySelectorAll('.card').length).toBe(1)
    expect(spalte.querySelector('.column-count').textContent).toBe('1')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// Karte #606 (ADR-0130 §1): Übernahme-Badge
// ─────────────────────────────────────────────────────────────────────────

describe('Renderer — Übernahme-Badge (Karte #606)', () => {
  const inStunden = h => new Date(Date.now() + h * 3600_000).toISOString()
  const claim = (until) => ({ by: 'agent:projekt-x', onBehalfOf: null, session: 's1', since: inStunden(-1), until })
  const mitClaim = IS_PROJECTOR_BUILD || IS_SERVER_BUILD

  it('aktive Übernahme: 🔒 mit Halter — nur in Projektor- und Server-Builds', () => {
    const container = renderCardViaBoard(makeCard({ claim: claim(inStunden(1)) }))
    const badge = container.querySelector('.card-claim-badge')
    if (!mitClaim) { expect(badge).toBeNull(); return }
    expect(badge.textContent).toContain('agent:projekt-x')
    expect(badge.textContent).toContain('🔒')
  })

  it('abgelaufene Übernahme: „verwaist“ statt Halter', () => {
    const container = renderCardViaBoard(makeCard({ claim: claim(inStunden(-0.5)) }))
    const badge = container.querySelector('.card-claim-badge')
    if (!mitClaim) { expect(badge).toBeNull(); return }
    expect(badge.textContent).not.toContain('🔒')
    expect(badge.title).toContain('agent:projekt-x')
  })

  it('Karte in col-erledigt: keine Anzeige (die Übernahme gilt als erloschen)', () => {
    const board = makeBoard([makeCard({ claim: claim(inStunden(1)) })], [{ id: 'col-erledigt', title: 'Erledigt' }])
    expect(setupBoardDOM(board).querySelector('.card-claim-badge')).toBeNull()
  })

  it('ohne claim: keine Anzeige', () => {
    expect(renderCardViaBoard(makeCard()).querySelector('.card-claim-badge')).toBeNull()
  })
})

