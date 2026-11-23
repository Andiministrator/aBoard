/**
 * Unit-Tests: Wiederkehrende Karten (ADR-0041)
 * Karte #64
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { Storage } from '../src/storage.js'

// nextSpawnDate/spawnRecurringCards: die ECHTEN Funktionen aus src/recurrence.js
// (F-1103, Sprint 69) — main.js importiert dieselben, keine Kopie mehr hier.
import { nextSpawnDate, spawnRecurringCards } from '../src/recurrence.js'

/* ─────────────────────────────────────────────────────────────────────────────
   Hilfsfunktionen
   ───────────────────────────────────────────────────────────────────────────── */

function makeBoard(cards = []) {
  return {
    version: '2',
    board: {
      id:            'board-recurrence-test',
      title:         'Recurrence-Test',
      nextDisplayId: 1,
      columnDefs: [
        { id: 'col-backlog',   title: 'Backlog' },
        { id: 'col-ready',     title: 'Ready' },
        { id: 'col-in-arbeit', title: 'In Arbeit' },
        { id: 'col-erledigt',  title: 'Erledigt' },
      ],
      templates: [],
      attachments: [],
      background: null,
      awaitingUser: null,
      sprints: [
        {
          id: 's1',
          title: 'Sprint 1',
          cards,
        },
        { id: '__backlog__', title: 'Backlog', cards: [] },
      ],
    },
  }
}

function makeTemplateCard(overrides = {}) {
  return {
    id:            crypto.randomUUID(),
    displayId:     1,
    title:         'Wöchentliches Review',
    description:   '',
    status:        'offen',
    priority:      'mittel',
    labels:        [],
    milestone:     null,
    session:       null,
    comments:      [],
    qa:            false,
    critical:      false,
    blocked:       false,
    owner:         '',
    complexity:    '',
    effort:        null,
    agentContext:  '',
    links:         [],
    attachments:   [],
    coverAttachmentId: null,
    dueDate:       null,
    startDate:     null,
    archived:      false,
    activity:      [],
    columnId:      'col-ready',
    created:       '2026-05-01',
    updated:       '2026-05-01',
    recurrence:    null,
    ...overrides,
  }
}

/* ─────────────────────────────────────────────────────────────────────────────
   Migration: recurrence-Feld
   ───────────────────────────────────────────────────────────────────────────── */

/* Hilfsfunktion: Karte aus gecheckertem State via ID finden */
function findCardInResult(result, cardId) {
  for (const sprint of result.board.sprints) {
    const found = sprint.cards.find(c => c.id === cardId)
    if (found) return found
  }
  return null
}

describe('migrateRecurrence via checkVersion', () => {
  it('fehlend → null', () => {
    const card = makeTemplateCard()
    delete card.recurrence
    const state = makeBoard([card])
    const result = Storage.checkVersion(state)
    const found = findCardInResult(result, card.id)
    expect(found.recurrence).toBeNull()
  })

  it('null bleibt null', () => {
    const card = makeTemplateCard({ recurrence: null })
    const state = makeBoard([card])
    const result = Storage.checkVersion(state)
    const found = findCardInResult(result, card.id)
    expect(found.recurrence).toBeNull()
  })

  it('ungültiges Objekt (fehlendes interval) → null', () => {
    const card = makeTemplateCard({ recurrence: { lastSpawned: '2026-05-12' } })
    const state = makeBoard([card])
    const result = Storage.checkVersion(state)
    const found = findCardInResult(result, card.id)
    expect(found.recurrence).toBeNull()
  })

  it('ungültiges Objekt (falsches interval) → null', () => {
    const card = makeTemplateCard({ recurrence: { interval: 'hourly', lastSpawned: '2026-05-12' } })
    const state = makeBoard([card])
    const result = Storage.checkVersion(state)
    const found = findCardInResult(result, card.id)
    expect(found.recurrence).toBeNull()
  })

  it('ungültiges Objekt (fehlendes lastSpawned) → null', () => {
    const card = makeTemplateCard({ recurrence: { interval: 'daily' } })
    const state = makeBoard([card])
    const result = Storage.checkVersion(state)
    const found = findCardInResult(result, card.id)
    expect(found.recurrence).toBeNull()
  })

  it('gültiges Objekt bleibt erhalten', () => {
    const r = { interval: 'weekly', lastSpawned: '2026-05-06', day: 3 }
    const card = makeTemplateCard({ recurrence: r })
    const state = makeBoard([card])
    const result = Storage.checkVersion(state)
    const found = findCardInResult(result, card.id)
    expect(found.recurrence).toEqual(r)
  })
})

/* ─────────────────────────────────────────────────────────────────────────────
   Spawn-Logik — echte Funktionen aus src/recurrence.js (F-1103, Sprint 69).
   Vorher stand hier eine „vereinfachte Referenz-Implementierung, die
   identisch zur Produktionslogik“ sein sollte — nichts hielt sie das. Den
   Hardcode `columnId === 'col-erledigt'` hatten Kopie und main.js
   gleichermassen (F-1103); die Abschaltung hängt weiter an der id
   (ADR-0041, F-830; F-1117).
   ───────────────────────────────────────────────────────────────────────────── */

/* ─────────────────────────────────────────────────────────────────────────────
   nextSpawnDate — Datumsarithmetik
   ───────────────────────────────────────────────────────────────────────────── */

describe('nextSpawnDate()', () => {
  it('daily: +1 Tag', () => {
    expect(nextSpawnDate('2026-05-12', 'daily')).toBe('2026-05-13')
  })

  it('daily: Monatswechsel', () => {
    expect(nextSpawnDate('2026-05-31', 'daily')).toBe('2026-06-01')
  })

  it('weekly: +7 Tage', () => {
    expect(nextSpawnDate('2026-05-06', 'weekly')).toBe('2026-05-13')
  })

  it('monthly: einfacher Fall', () => {
    expect(nextSpawnDate('2026-04-13', 'monthly')).toBe('2026-05-13')
  })

  it('monthly: Jahreswechsel', () => {
    expect(nextSpawnDate('2025-12-15', 'monthly')).toBe('2026-01-15')
  })

  it('monthly: Monatsende-Edge-Case Jan→Feb (klemmt auf letzten Feb-Tag)', () => {
    /* 2028 ist ein Schaltjahr → 29 Tage */
    expect(nextSpawnDate('2028-01-31', 'monthly')).toBe('2028-02-29')
    /* 2026 kein Schaltjahr → 28 Tage */
    expect(nextSpawnDate('2026-01-31', 'monthly')).toBe('2026-02-28')
  })

  it('monthly: 31. März → 30. April (klemmt)', () => {
    expect(nextSpawnDate('2026-03-31', 'monthly')).toBe('2026-04-30')
  })
})

/* ─────────────────────────────────────────────────────────────────────────────
   spawnRecurringCards — Spawn-Logik
   ───────────────────────────────────────────────────────────────────────────── */

describe('spawnRecurringCards()', () => {
  /* Systemzeit auf 2026-05-13 fixieren */
  beforeEach(() => {
    vi.setSystemTime(new Date('2026-05-13T12:00:00Z'))
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('täglich — lastSpawned=gestern → spawnt heute, aktualisiert lastSpawned', () => {
    const template = makeTemplateCard({
      displayId: 1,
      recurrence: { interval: 'daily', lastSpawned: '2026-05-12' },
    })
    const state = makeBoard([template])
    const board = state.board
    const today = '2026-05-13'

    spawnRecurringCards(board, today)

    const sprint = board.sprints.find(s => s.id === 's1')
    const normalCards = sprint.cards.filter(c => !c.kind)
    expect(normalCards).toHaveLength(2) /* Template + Instanz */

    const instance = normalCards.find(c => c.id !== template.id)
    expect(instance).toBeDefined()
    expect(instance.recurrence).toBeNull()
    expect(instance.dueDate).toBe('2026-05-13')
    expect(instance.columnId).toBe('col-ready')
    expect(instance.title).toBe(template.title)

    /* lastSpawned aktualisiert */
    expect(template.recurrence.lastSpawned).toBe(today)
  })

  it('täglich — lastSpawned=heute → kein Spawn', () => {
    const template = makeTemplateCard({
      displayId: 1,
      recurrence: { interval: 'daily', lastSpawned: '2026-05-13' },
    })
    const state = makeBoard([template])
    const board = state.board
    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    expect(sprint.cards).toHaveLength(1)
  })

  it('täglich — lastSpawned=morgen → kein Spawn (lastSpawned in der Zukunft)', () => {
    const template = makeTemplateCard({
      displayId: 1,
      recurrence: { interval: 'daily', lastSpawned: '2026-05-14' },
    })
    const state = makeBoard([template])
    const board = state.board
    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    expect(sprint.cards).toHaveLength(1)
  })

  it('wöchentlich — 7-Tage-Anchor', () => {
    const template = makeTemplateCard({
      displayId: 1,
      recurrence: { interval: 'weekly', lastSpawned: '2026-05-06' },
    })
    const state = makeBoard([template])
    const board = state.board
    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    expect(sprint.cards).toHaveLength(2)
    const instance = sprint.cards.find(c => c.id !== template.id)
    expect(instance.dueDate).toBe('2026-05-13')
    expect(instance.recurrence).toBeNull()
  })

  it('monatlich — Spawn nach 1 Monat', () => {
    const template = makeTemplateCard({
      displayId: 1,
      recurrence: { interval: 'monthly', lastSpawned: '2026-04-13' },
    })
    const state = makeBoard([template])
    const board = state.board
    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    expect(sprint.cards).toHaveLength(2)
    const instance = sprint.cards.find(c => c.id !== template.id)
    expect(instance.dueDate).toBe('2026-05-13')
  })

  it('ignoriert sprint-meta-Karten', () => {
    const metaCard = {
      ...makeTemplateCard({
        recurrence: { interval: 'daily', lastSpawned: '2026-05-12' },
      }),
      kind: 'sprint-meta',
      metaType: 'start',
    }
    const state = makeBoard([metaCard])
    const board = state.board
    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    /* Kein Spawn — Meta-Karte wird übersprungen */
    expect(sprint.cards).toHaveLength(1)
    /* lastSpawned unverändert */
    expect(metaCard.recurrence.lastSpawned).toBe('2026-05-12')
  })

  it('kein Spawn wenn Template in col-erledigt', () => {
    const template = makeTemplateCard({
      displayId: 1,
      columnId:  'col-erledigt',
      recurrence: { interval: 'daily', lastSpawned: '2026-05-12' },
    })
    const state = makeBoard([template])
    const board = state.board
    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    expect(sprint.cards).toHaveLength(1)
  })

  it('F-1103 (Verify Sprint 69): col-erledigt auf einen fachfremden Titel umbenannt ("Zur Ablage", id bleibt) — KEIN Spawn', () => {
    /* ADR-0041 macht die Abschaltung an der id `col-erledigt` fest. Nur über den
       Titel zu entscheiden (erste Fassung von F-1103) hätte eine vom Nutzer
       abgeschaltete Vorlage nach dem Umbenennen wieder spawnen lassen. Die
       Abschaltung bleibt deshalb an der id (F-1117). */
    const template = makeTemplateCard({
      displayId: 1,
      columnId:  'col-erledigt',
      recurrence: { interval: 'daily', lastSpawned: '2026-05-12' },
    })
    const state = makeBoard([template])
    const board = state.board
    board.columnDefs.find(c => c.id === 'col-erledigt').title = 'Zur Ablage'
    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    expect(sprint.cards).toHaveLength(1) /* nur das Template */
  })

  it('F-1117 (ADR-0041, F-830): eine EIGENE Spalte mit Titel "Erledigt" und fremder id schaltet die Vorlage NICHT ab', () => {
    const template = makeTemplateCard({
      displayId: 1,
      columnId:  'col-custom-done',
      recurrence: { interval: 'daily', lastSpawned: '2026-05-12' },
    })
    const state = makeBoard([template])
    const board = state.board
    board.columnDefs.push({ id: 'col-custom-done', title: 'Erledigt' })
    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    /* Die Abschaltung hängt an der id `col-erledigt` (ADR-0041, F-830),
       nicht am Titel: eine Titel-Erkennung stoppte sonst auch Vorlagen in
       fremden Spalten, die zufällig „Fertig“ oder „Done“ heissen. */
    expect(sprint.cards).toHaveLength(2)
  })

  it('Spawn-Instanz landet in col-backlog wenn col-ready fehlt', () => {
    const template = makeTemplateCard({
      displayId: 1,
      recurrence: { interval: 'daily', lastSpawned: '2026-05-12' },
    })
    const state = makeBoard([template])
    /* col-ready aus columnDefs entfernen */
    state.board.columnDefs = state.board.columnDefs.filter(c => c.id !== 'col-ready')
    const board = state.board
    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    const instance = sprint.cards.find(c => c.id !== template.id)
    expect(instance.columnId).toBe('col-backlog')
  })

  it('Spawn-Instanz übernimmt title, description, labels, owner vom Template', () => {
    const template = makeTemplateCard({
      displayId:   1,
      title:       'Weekly Review',
      description: 'Wöchentliche Retrospektive',
      labels:      ['weekly', 'review'],
      owner:       'Andi',
      complexity:  'einfach',
      effort:      0.5,
      recurrence:  { interval: 'daily', lastSpawned: '2026-05-12' },
    })
    const state = makeBoard([template])
    const board = state.board
    spawnRecurringCards(board, '2026-05-13')

    const sprint = board.sprints.find(s => s.id === 's1')
    const instance = sprint.cards.find(c => c.id !== template.id)
    expect(instance.title).toBe('Weekly Review')
    expect(instance.description).toBe('Wöchentliche Retrospektive')
    expect(instance.labels).toEqual(['weekly', 'review'])
    expect(instance.owner).toBe('Andi')
    expect(instance.complexity).toBe('einfach')
    expect(instance.effort).toBe(0.5)
  })

  it('Backlog-Sprint wird nicht bespaunt', () => {
    const template = makeTemplateCard({
      displayId: 1,
      recurrence: { interval: 'daily', lastSpawned: '2026-05-12' },
    })
    /* Template in __backlog__ legen */
    const state = makeBoard([])
    state.board.sprints.find(s => s.id === '__backlog__').cards.push(template)
    const board = state.board
    const before = board.sprints.find(s => s.id === '__backlog__').cards.length

    spawnRecurringCards(board, '2026-05-13')

    const after = board.sprints.find(s => s.id === '__backlog__').cards.length
    expect(after).toBe(before) /* kein Spawn */
  })
})
