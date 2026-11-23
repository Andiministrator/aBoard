/**
 * Unit-Tests für Sprint-Meta-Sonderkarten (ADR-0036)
 *
 * Geprüft werden:
 *  - createSprintMetaCard() — korrekte Felder, kind + metaType
 *  - migrateSprintMeta() — Meta-Karten für bestehende Sprints nachrüsten
 *  - Velocity-Ausschluss — Meta-Karten zählen nicht in computeSprintMetrics
 *  - Migration: idempotent (zweimal laufen erzeugt keine Duplikate)
 *  - Migration: Sprint-Status-Erkennung (abgeschlossen vs. laufend)
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { Model }    from '../src/model.js'
import { Storage, istMetaKarteLeer, metaZielSpalte } from '../src/storage.js'
import { Metrics }  from '../src/metrics.js'
import { Renderer } from '../src/renderer.js'
import { purgeLinksTo, addLink } from '../src/links.js'

// ---------------------------------------------------------------------------
// Hilfsfunktionen
// ---------------------------------------------------------------------------

function makeBoard(sprints = []) {
  return {
    id:            'board-1',
    title:         'Test-Board',
    nextDisplayId: 1,
    columnDefs:    [],
    sprints,
  }
}

function makeSprint(id, cards = []) {
  return { id, title: `Sprint ${id}`, cards }
}

function makeCard(overrides = {}) {
  return {
    id:          `card-${Math.random().toString(36).slice(2)}`,
    displayId:   1,
    title:       'Test-Karte',
    description: '',
    status:      'offen',
    priority:    'mittel',
    labels:      [],
    milestone:   null,
    session:     null,
    comments:    [],
    qa:          false,
    critical:    false,
    blocked:     false,
    owner:       '',
    complexity:  '',
    effort:      null,
    agentContext: '',
    links:       [],
    attachments: [],
    dueDate:     null,
    startDate:   null,
    archived:    false,
    activity:    [],
    columnId:    'col-backlog',
    created:     '2026-01-01',
    updated:     '2026-01-01',
    ...overrides,
  }
}

function makeState(sprints) {
  return { version: '2', board: makeBoard(sprints) }
}

// ---------------------------------------------------------------------------
// createSprintMetaCard
// ---------------------------------------------------------------------------

describe('Model.createSprintMetaCard()', () => {
  it('erzeugt eine Start-Karte mit korrekten Pflichtfeldern', () => {
    const card = Model.createSprintMetaCard('start', 42, 'col-erledigt', true)
    expect(card.kind).toBe('sprint-meta')
    expect(card.metaType).toBe('start')
    expect(card.title).toBe('📋 Sprint-Start')
    expect(card.displayId).toBe(42)
    expect(card.columnId).toBe('col-erledigt')
    expect(card.qa).toBe(true)
    expect(card.effort).toBeNull()
    expect(card.complexity).toBe('')
    expect(card.owner).toBe('')
    expect(Array.isArray(card.links)).toBe(true)
    expect(Array.isArray(card.attachments)).toBe(true)
    expect(card.archived).toBe(false)
  })

  it('erzeugt eine End-Karte mit korrekten Pflichtfeldern', () => {
    const card = Model.createSprintMetaCard('end', 99, 'col-backlog', false)
    expect(card.kind).toBe('sprint-meta')
    expect(card.metaType).toBe('end')
    expect(card.title).toBe('🏁 Sprint-Abschluss')
    expect(card.displayId).toBe(99)
    expect(card.columnId).toBe('col-backlog')
    expect(card.qa).toBe(false)
  })

  it('hat eine eindeutige id (UUID)', () => {
    const a = Model.createSprintMetaCard('start', 1, 'col-erledigt', true)
    const b = Model.createSprintMetaCard('start', 2, 'col-erledigt', true)
    expect(a.id).not.toBe(b.id)
  })

  it('hat keine links, keine attachments', () => {
    const card = Model.createSprintMetaCard('end', 1, 'col-backlog', false)
    expect(card.links).toEqual([])
    expect(card.attachments).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// Migration: migrateSprintMeta via Storage.checkVersion
// ---------------------------------------------------------------------------

describe('migrateSprintMeta()', () => {
  /* Sprint-60-Kritiker: `archived:true` kann eine Meta-Karte auch ohne den
     UI-Pfad erreichen — aus einem importierten Board oder aus einer
     `card.field`-Op (die Blockliste in ops.js kennt nur id/displayId).
     Der Renderer filtert archivierte Karten OHNE Meta-Ausnahme aus der
     Spalte, und der Archiv-Button ist bei Meta-Karten versteckt: Die
     Sprint-Abschluss-Karte samt Recap wäre unsichtbar. Deshalb heilt der
     Load-Pass das Flag, statt es nur zu melden (ADR-0036). */
  it('setzt archived:true auf einer Meta-Karte beim Laden zurück (Selbstheilung)', () => {
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-erledigt' }),
      { id: 'ende', kind: 'sprint-meta', metaType: 'end', columnId: 'col-erledigt',
        title: '🏁 Sprint-Abschluss', archived: true, comments: [], activity: [], links: [] },
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const meta = result.board.sprints
      .find(s => s.id === 'sprint-1').cards.find(c => c.id === 'ende')
    expect(meta).toBeDefined()
    expect(meta.archived).toBe(false)
  })

  it('lässt archived:true auf einer NORMALEN Karte unangetastet (Gegenprobe: der Pass trifft nur Meta-Karten)', () => {
    const sprint = makeSprint('sprint-1', [
      makeCard({ id: 'normal', columnId: 'col-erledigt', archived: true }),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const karte = result.board.sprints
      .find(s => s.id === 'sprint-1').cards.find(c => c.id === 'normal')
    expect(karte.archived).toBe(true)
  })

  it('fügt Start- und End-Karte für Sprint ohne Meta-Karten ein', () => {
    const sprint = makeSprint('sprint-1', [makeCard({ columnId: 'col-backlog' })])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const metaCards = s.cards.filter(c => c.kind === 'sprint-meta')
    expect(metaCards.length).toBe(2)
    const startCard = metaCards.find(c => c.metaType === 'start')
    const endCard   = metaCards.find(c => c.metaType === 'end')
    expect(startCard).toBeDefined()
    expect(endCard).toBeDefined()
  })

  it('Start-Karte steht am Anfang des cards-Arrays', () => {
    const sprint = makeSprint('sprint-1', [makeCard()])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    expect(s.cards[0].kind).toBe('sprint-meta')
    expect(s.cards[0].metaType).toBe('start')
  })

  it('End-Karte steht am Ende des cards-Arrays', () => {
    const sprint = makeSprint('sprint-1', [makeCard()])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const last = s.cards[s.cards.length - 1]
    expect(last.kind).toBe('sprint-meta')
    expect(last.metaType).toBe('end')
  })

  it('ist idempotent — zweimaliges Ausführen erzeugt keine Duplikate', () => {
    const sprint = makeSprint('sprint-1', [makeCard()])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const once  = Storage.checkVersion(state)
    const twice = Storage.checkVersion(JSON.parse(JSON.stringify(once)))
    const s = twice.board.sprints.find(s => s.id === 'sprint-1')
    const metaCards = s.cards.filter(c => c.kind === 'sprint-meta')
    expect(metaCards.length).toBe(2)
  })

  it('Backlog-Sprint bekommt keine Meta-Karten', () => {
    const state = makeState([{ id: '__backlog__', title: 'Backlog', cards: [makeCard()] }])
    const result = Storage.checkVersion(state)
    const backlog = result.board.sprints.find(s => s.id === '__backlog__')
    const metaCards = backlog.cards.filter(c => c.kind === 'sprint-meta')
    expect(metaCards.length).toBe(0)
  })

  it('End-Karte landet in col-backlog bei laufendem Sprint', () => {
    /* Sprint hat normale Karte in col-backlog (nicht abgeschlossen) */
    const sprint = makeSprint('sprint-1', [makeCard({ columnId: 'col-backlog', qa: false })])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const endCard = s.cards.find(c => c.kind === 'sprint-meta' && c.metaType === 'end')
    expect(endCard.columnId).toBe('col-backlog')
    expect(endCard.qa).toBe(false)
  })

  it('Start-Karte landet initial in col-backlog/qa:false (ADR-0042)', () => {
    const sprint = makeSprint('sprint-1', [makeCard({ columnId: 'col-backlog' })])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const startCard = s.cards.find(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    expect(startCard.columnId).toBe('col-backlog')
    expect(startCard.qa).toBe(false)
  })

  it('End-Karte landet in col-erledigt bei abgeschlossenem Sprint', () => {
    /* Sprint hat alle normalen Karten in col-erledigt + qa:true */
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-erledigt', qa: true }),
      makeCard({ columnId: 'col-erledigt', qa: true }),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const endCard = s.cards.find(c => c.kind === 'sprint-meta' && c.metaType === 'end')
    expect(endCard.columnId).toBe('col-erledigt')
    expect(endCard.qa).toBe(true)
  })

  it('displayId wird korrekt inkrementiert', () => {
    const sprint = makeSprint('sprint-1', [])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    state.board.nextDisplayId = 10
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const metaCards = s.cards.filter(c => c.kind === 'sprint-meta')
    const ids = metaCards.map(c => c.displayId)
    /* Beide IDs ≥ 10, keine Duplikate */
    expect(ids[0]).toBeGreaterThanOrEqual(10)
    expect(ids[1]).toBeGreaterThan(ids[0])
    expect(result.board.nextDisplayId).toBe(12)
  })
})

// ---------------------------------------------------------------------------
// F-1087 (Karte #537): Doubletten-Zusammenführung bei zwei Start-/End-Karten
// ---------------------------------------------------------------------------

describe('migrateSprintMeta() — Doubletten-Zusammenführung (F-1087)', () => {
  /* Baut eine Meta-Karte direkt über Model.createSprintMetaCard, damit die
     Karte exakt dieselbe Feldliste trägt wie eine real angelegte — inkl.
     comments/activity/description, die istMetaKarteLeer prüft. */
  function metaKarte(metaType, displayId, overrides = {}) {
    return { ...Model.createSprintMetaCard(metaType, displayId, 'col-backlog', false), ...overrides }
  }

  it('zwei LEERE Start-Karten werden zu einer zusammengeführt', () => {
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-ready' }),
      metaKarte('start', 2),
      metaKarte('start', 3),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const startKarten = s.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    expect(startKarten.length).toBe(1)
    /* beide Kandidaten waren leer → die mit der kleineren displayId gewinnt */
    expect(startKarten[0].displayId).toBe(2)
  })

  it('eine LEERE + eine Start-Karte mit Beschreibung: die mit Beschreibung bleibt (F-364-Prinzip)', () => {
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-ready' }),
      metaKarte('start', 2, { description: 'Übergabe-Notiz für den nächsten Chat.' }),
      metaKarte('start', 3),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const startKarten = s.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    expect(startKarten.length).toBe(1)
    expect(startKarten[0].description).toBe('Übergabe-Notiz für den nächsten Chat.')
  })

  it('die mit Beschreibung gewinnt auch dann, wenn ihre displayId die GRÖSSERE ist', () => {
    /* Gegenprobe zur vorigen: Inhalt schlägt displayId, nicht umgekehrt. */
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-ready' }),
      metaKarte('start', 2),
      metaKarte('start', 3, { description: 'Diese hier bleibt.' }),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const startKarten = s.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    expect(startKarten.length).toBe(1)
    expect(startKarten[0].displayId).toBe(3)
  })

  it('zwei Start-Karten MIT Inhalt: beide bleiben stehen, plus Warnung (F-364: nie Inhalt löschen)', () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-ready' }),
      metaKarte('start', 2, { description: 'Notiz A' }),
      metaKarte('start', 3, { description: 'Notiz B' }),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const startKarten = s.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    expect(startKarten.length).toBe(2)
    expect(startKarten.map(c => c.description).sort()).toEqual(['Notiz A', 'Notiz B'])
    expect(warnSpy).toHaveBeenCalled()
    expect(warnSpy.mock.calls.some(args => args.join(' ').includes('F-1087'))).toBe(true)
    warnSpy.mockRestore()
  })

  it('End-Karten werden unabhängig von Start-Karten zusammengeführt (beide Typen gleichzeitig doppelt)', () => {
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-erledigt', qa: true }),
      metaKarte('start', 2, { columnId: 'col-erledigt', qa: true }),
      metaKarte('start', 3, { columnId: 'col-erledigt', qa: true }),
      metaKarte('end', 4),
      metaKarte('end', 5, { description: 'Recap-Text' }),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const startKarten = s.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    const endKarten   = s.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'end')
    expect(startKarten.length).toBe(1)
    expect(endKarten.length).toBe(1)
    expect(endKarten[0].description).toBe('Recap-Text')
  })

  it('ist idempotent — ein zweiter Lauf nach der Zusammenführung ändert nichts mehr', () => {
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-ready' }),
      metaKarte('start', 2),
      metaKarte('start', 3),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const once  = Storage.checkVersion(state)
    const twice = Storage.checkVersion(JSON.parse(JSON.stringify(once)))
    const s1 = once.board.sprints.find(s => s.id === 'sprint-1')
    const s2 = twice.board.sprints.find(s => s.id === 'sprint-1')
    const startIds1 = s1.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'start').map(c => c.id)
    const startIds2 = s2.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'start').map(c => c.id)
    expect(startIds2).toEqual(startIds1)
  })

  it('die übrig bleibende Start-Karte wird bei aktivem Sprint weiterhin korrekt aktiviert (ADR-0047)', () => {
    /* Beachtet ADR-0047: Der Merge darf die Auto-Aktivierung nicht umgehen —
       die überlebende Karte muss denselben Aktivierungs-Regeln unterliegen
       wie eine einzelne Start-Karte. */
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-ready' }),
      metaKarte('start', 2),
      metaKarte('start', 3),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const startKarte = s.cards.find(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    expect(startKarte.columnId).toBe('col-erledigt')
    expect(startKarte.qa).toBe(true)
  })

  // -------------------------------------------------------------------------
  // F-1095 (Kritiker A, K2): Aktivierung/Abschluss schlägt displayId
  // -------------------------------------------------------------------------

  it('F-1095: aktivierte Start-Karte (col-erledigt) gewinnt gegen eine leere im Backlog, AUCH bei grösserer displayId', () => {
    /* Beide Kandidaten sind "leer" im Sinn von istMetaKarteLeer: der
       sprint-activate-Eintrag stammt von actor:'system' und zählt dort nicht
       als Inhalt. Vor F-1095 hätte deshalb die kleinere displayId (2, nie
       aktiviert) gewonnen und die aktivierte Karte (3) gelöscht — der Sprint
       hätte danach als nicht gestartet gegolten. */
    const aktiviert = metaKarte('start', 3, {
      columnId: 'col-erledigt',
      qa: true,
      activity: [{ id: 'a1', actor: 'system', type: 'sprint-activate', ts: '2026-01-01', description: '' }],
    })
    const nieAktiviert = metaKarte('start', 2)
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-ready' }),
      nieAktiviert,
      aktiviert,
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const startKarten = s.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    expect(startKarten.length).toBe(1)
    expect(startKarten[0].displayId).toBe(3)
    expect(startKarten[0].columnId).toBe('col-erledigt')
    /* Der Sprint gilt als aktiv: die überlebende Karte ist die aktivierte. */
    expect(startKarten[0].qa).toBe(true)
  })

  it('F-1095: geschlossene End-Karte (col-erledigt) gewinnt gegen eine leere im Backlog, AUCH bei grösserer displayId', () => {
    const geschlossen = metaKarte('end', 3, { columnId: 'col-erledigt', qa: true })
    const nieGeschlossen = metaKarte('end', 2)
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-erledigt', qa: true }),
      nieGeschlossen,
      geschlossen,
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const endKarten = s.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'end')
    expect(endKarten.length).toBe(1)
    expect(endKarten[0].displayId).toBe(3)
    expect(endKarten[0].columnId).toBe('col-erledigt')
    expect(endKarten[0].qa).toBe(true)
  })

  it('F-1095-Gegenprobe: sind BEIDE Doubletten nicht in col-erledigt, entscheidet weiterhin die kleinere displayId', () => {
    /* Zeigt, dass die neue Regel nur EINGREIFT, wenn col-erledigt betroffen
       ist — der alte Tie-Breaker bleibt für den Normalfall (zwei nie
       aktivierte Karten im Backlog) unverändert wirksam. */
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-ready' }),
      metaKarte('start', 3),
      metaKarte('start', 2),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const startKarten = s.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    expect(startKarten.length).toBe(1)
    expect(startKarten[0].displayId).toBe(2)
  })
})

// ---------------------------------------------------------------------------
// Velocity-Ausschluss
// ---------------------------------------------------------------------------

describe('Metrics.computeSprintMetrics() — Meta-Karten-Ausschluss', () => {
  it('Meta-Karten zählen nicht im cardCount', () => {
    const cards = [
      makeCard({ columnId: 'col-backlog', kind: 'sprint-meta', metaType: 'start' }),
      makeCard({ columnId: 'col-backlog' }),
      makeCard({ columnId: 'col-backlog', kind: 'sprint-meta', metaType: 'end' }),
    ]
    const m = Metrics.computeSprintMetrics({ cards })
    expect(m.cardCount).toBe(1) /* Nur die normale Karte */
  })

  it('Meta-Karten zählen nicht im doneCount', () => {
    const cards = [
      makeCard({ columnId: 'col-erledigt', qa: true, kind: 'sprint-meta', metaType: 'start' }),
      makeCard({ columnId: 'col-erledigt', qa: true }),
      makeCard({ columnId: 'col-erledigt', qa: true, kind: 'sprint-meta', metaType: 'end' }),
    ]
    const m = Metrics.computeSprintMetrics({ cards })
    expect(m.doneCount).toBe(1) /* Nur die normale erledigte Karte */
  })

  it('Meta-Karten mit effort=null verfälschen Velocity nicht (expliziter kind-Check)', () => {
    /* Sicherheitstest: auch wenn jemand effort setzt, Meta-Karten bleiben ausgeschlossen */
    const cards = [
      makeCard({ columnId: 'col-erledigt', qa: true, effort: 5, kind: 'sprint-meta', metaType: 'start' }),
      makeCard({ columnId: 'col-erledigt', qa: true, effort: 3 }),
    ]
    const m = Metrics.computeSprintMetrics({ cards })
    expect(m.totalEffort).toBe(3) /* Nur die normale Karte */
    expect(m.doneEffort).toBe(3)
  })
})

// ---------------------------------------------------------------------------
// ADR-0047: Auto-Aktivierung der Sprint-Start-Karte
// ---------------------------------------------------------------------------

describe('ADR-0047: Auto-Aktivierung der Sprint-Start-Karte', () => {
  function findStart(state, sprintId) {
    const s = state.board.sprints.find(s => s.id === sprintId)
    return s.cards.find(c => c.kind === 'sprint-meta' && c.metaType === 'start')
  }

  it('aktiviert die Start-Karte, sobald eine Karte in col-ready steht', () => {
    const sprint = makeSprint('sprint-1', [makeCard({ columnId: 'col-ready' })])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const startCard = findStart(result, 'sprint-1')
    expect(startCard.columnId).toBe('col-erledigt')
    expect(startCard.qa).toBe(true)
  })

  it('aktiviert die Start-Karte, sobald eine Karte in col-in-arbeit steht', () => {
    const sprint = makeSprint('sprint-1', [makeCard({ columnId: 'col-in-arbeit' })])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const startCard = findStart(result, 'sprint-1')
    expect(startCard.columnId).toBe('col-erledigt')
    expect(startCard.qa).toBe(true)
  })

  it('aktiviert NICHT, wenn alle Nicht-Meta-Karten in col-backlog stehen', () => {
    const sprint = makeSprint('sprint-1', [
      makeCard({ columnId: 'col-backlog' }),
      makeCard({ columnId: 'col-backlog' }),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const startCard = findStart(result, 'sprint-1')
    expect(startCard.columnId).toBe('col-backlog')
    expect(startCard.qa).toBe(false)
  })

  it('schreibt einen sprint-activate-Activity-Eintrag mit actor="system"', () => {
    const sprint = makeSprint('sprint-1', [makeCard({ columnId: 'col-ready' })])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const startCard = findStart(result, 'sprint-1')
    const activate = startCard.activity.find(e => e.type === 'sprint-activate')
    expect(activate).toBeDefined()
    expect(activate.actor).toBe('system')
    expect(activate.note).toMatch(/ADR-0047/)
  })

  it('ist idempotent — zweiter Lauf schreibt keinen weiteren sprint-activate-Eintrag', () => {
    const sprint = makeSprint('sprint-1', [makeCard({ columnId: 'col-ready' })])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const once   = Storage.checkVersion(state)
    const twice  = Storage.checkVersion(JSON.parse(JSON.stringify(once)))
    const startCard = findStart(twice, 'sprint-1')
    const activates = startCard.activity.filter(e => e.type === 'sprint-activate')
    expect(activates.length).toBe(1)
  })

  it('aktiviert NICHT für Lanes (kind === "lane")', () => {
    const sprint = { id: 'lane-1', title: 'Lane', kind: 'lane', cards: [makeCard({ columnId: 'col-ready' })] }
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const lane = result.board.sprints.find(s => s.id === 'lane-1')
    /* Lane bekommt erst gar keine Meta-Karten — und damit auch keine Aktivierung */
    const metaCards = lane.cards.filter(c => c.kind === 'sprint-meta')
    expect(metaCards.length).toBe(0)
  })

  it('beim Sprint-Frisch-Anlegen mit aktiven Karten wird Start sofort aktiviert', () => {
    /* Szenario: Sprint ohne Meta-Karten, aber bereits Karten in col-ready.
       migrateSprintMeta legt die Start-Karte an (col-backlog) und aktiviert sie
       direkt im selben Lauf. */
    const sprint = makeSprint('sprint-1', [makeCard({ columnId: 'col-ready' })])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const startCard = findStart(result, 'sprint-1')
    expect(startCard).toBeDefined()
    expect(startCard.columnId).toBe('col-erledigt')
    expect(startCard.qa).toBe(true)
  })

  it('Start-Karte, die User manuell zurück in col-backlog gesetzt hat, wird wieder aktiviert', () => {
    /* Defense-in-depth: Wenn jemand die Start-Karte versehentlich zurück in
       col-backlog setzt und der Sprint ist weiter aktiv, soll die Migration
       das beim nächsten Load reparieren. */
    const sprint = makeSprint('sprint-1', [
      { ...makeCard({ columnId: 'col-backlog', qa: false, kind: 'sprint-meta', metaType: 'start' }), displayId: 1, title: '📋 Sprint-Start' },
      { ...makeCard({ columnId: 'col-backlog', qa: false, kind: 'sprint-meta', metaType: 'end' }),   displayId: 2, title: '🏁 Sprint-Abschluss' },
      makeCard({ columnId: 'col-erledigt', qa: true }),
    ])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const startCard = findStart(result, 'sprint-1')
    expect(startCard.columnId).toBe('col-erledigt')
    expect(startCard.qa).toBe(true)
  })

  it('F-224: Manueller Reset auf col-backlog erzeugt KEINEN zweiten sprint-activate-Eintrag', () => {
    /* User aktiviert Sprint → Karte in col-erledigt + 1× activate.
       User verschiebt Start-Karte manuell zurück in col-backlog (ADR-0042 erlaubt das).
       Nächster Board-Load: autoActivateSprintStart muss erneut moven, aber KEINEN
       zweiten Activity-Eintrag schreiben — der erste Eintrag bleibt die kanonische Aktivierung. */
    const sprint = makeSprint('sprint-1', [makeCard({ columnId: 'col-ready' })])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const once   = Storage.checkVersion(state)
    /* Simuliere User-Reset zurück in col-backlog */
    const startCardAfterFirst = once.board.sprints
      .find(s => s.id === 'sprint-1').cards
      .find(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    startCardAfterFirst.columnId = 'col-backlog'
    startCardAfterFirst.qa = false
    const twice  = Storage.checkVersion(JSON.parse(JSON.stringify(once)))
    const startCard = twice.board.sprints
      .find(s => s.id === 'sprint-1').cards
      .find(c => c.kind === 'sprint-meta' && c.metaType === 'start')
    /* Wieder aktiviert (Move-Effekt bleibt) */
    expect(startCard.columnId).toBe('col-erledigt')
    expect(startCard.qa).toBe(true)
    /* ABER: nur ein activate-Eintrag */
    const activates = startCard.activity.filter(e => e.type === 'sprint-activate')
    expect(activates.length).toBe(1)
  })

  it('F-225: autoActivateSprintStart setzt card.updated', () => {
    const sprint = makeSprint('sprint-1', [makeCard({ columnId: 'col-ready' })])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const startCard = findStart(result, 'sprint-1')
    /* ISO-Datum YYYY-MM-DD */
    expect(startCard.updated).toMatch(/^\d{4}-\d{2}-\d{2}/)
  })
})

// ---------------------------------------------------------------------------
// Karte #177: Sprint-Backlog-Sortierung — Meta-Karten an die Ränder
// ---------------------------------------------------------------------------

describe('Renderer.sortMetaCardsBacklog (Karte #177)', () => {
  const start  = { metaType: 'start', kind: 'sprint-meta', title: '📋 Start' }
  const end    = { metaType: 'end',   kind: 'sprint-meta', title: '🏁 Ende' }
  const a = { title: 'A' }, b = { title: 'B' }, c = { title: 'C' }

  it('Start oben, Ende unten, normale Karten in Mitte (relative Reihenfolge erhalten)', () => {
    const sorted = Renderer.sortMetaCardsBacklog([a, end, b, start, c])
    expect(sorted.map(x => x.title)).toEqual(['📋 Start', 'A', 'B', 'C', '🏁 Ende'])
  })

  it('nur Start vorhanden → Start oben, normale unverändert', () => {
    const sorted = Renderer.sortMetaCardsBacklog([a, b, start])
    expect(sorted.map(x => x.title)).toEqual(['📋 Start', 'A', 'B'])
  })

  it('nur End vorhanden → End unten, normale unverändert', () => {
    const sorted = Renderer.sortMetaCardsBacklog([a, end, b])
    expect(sorted.map(x => x.title)).toEqual(['A', 'B', '🏁 Ende'])
  })

  it('keine Meta-Karten → unverändert', () => {
    const sorted = Renderer.sortMetaCardsBacklog([a, b, c])
    expect(sorted.map(x => x.title)).toEqual(['A', 'B', 'C'])
  })

  it('leeres Array → leeres Array', () => {
    expect(Renderer.sortMetaCardsBacklog([])).toEqual([])
  })
})

// ---------------------------------------------------------------------------
// Karte #330 / Bug 2: Cleanup-Pass — Meta-Karten in unerlaubten Swimlanes
// ---------------------------------------------------------------------------

describe('migrateSprintMeta() — Cleanup-Pass (Karte #330)', () => {
  /** Hilfsfunktion: erzeugt eine schlanke Meta-Karte mit optionalen Kommentaren */
  function makeMetaCard(metaType, overrides = {}) {
    return {
      id:        `meta-${Math.random().toString(36).slice(2)}`,
      displayId: 1,
      title:     metaType === 'start' ? '📋 Sprint-Start' : '🏁 Sprint-Abschluss',
      kind:      'sprint-meta',
      metaType,
      columnId:  'col-backlog',
      qa:        false,
      comments:  [],
      ...overrides,
    }
  }

  it('entfernt leere Meta-Karte aus __backlog__-Swimlane', () => {
    const metaInBacklog = makeMetaCard('start')
    const state = makeState([
      { id: '__backlog__', title: 'Backlog', cards: [makeCard(), metaInBacklog] },
    ])
    const result = Storage.checkVersion(state)
    const backlog = result.board.sprints.find(s => s.id === '__backlog__')
    const metaCards = backlog.cards.filter(c => c.kind === 'sprint-meta')
    expect(metaCards.length).toBe(0)
    /* Normale Karte bleibt erhalten */
    expect(backlog.cards.filter(c => c.kind !== 'sprint-meta').length).toBe(1)
  })

  it('entfernt leere Meta-Karte aus kind:lane-Swimlane', () => {
    const metaInLane = makeMetaCard('end')
    const lane = { id: 'lane-1', title: 'Themen-Lane', kind: 'lane', cards: [makeCard(), metaInLane] }
    const state = makeState([lane, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const lane1 = result.board.sprints.find(s => s.id === 'lane-1')
    const metaCards = lane1.cards.filter(c => c.kind === 'sprint-meta')
    expect(metaCards.length).toBe(0)
  })

  it('behält Meta-Karte mit Kommentaren in __backlog__ (Datenschutz)', () => {
    const metaWithComment = makeMetaCard('start', {
      comments: [{ id: 'c1', author: 'Andi', text: 'Übergabe-Notiz' }],
    })
    const state = makeState([
      { id: '__backlog__', title: 'Backlog', cards: [metaWithComment] },
    ])
    const result = Storage.checkVersion(state)
    const backlog = result.board.sprints.find(s => s.id === '__backlog__')
    const metaCards = backlog.cards.filter(c => c.kind === 'sprint-meta')
    expect(metaCards.length).toBe(1)
    expect(metaCards[0].comments.length).toBe(1)
  })

  it('behält Meta-Karte mit Kommentaren in kind:lane-Swimlane (Datenschutz)', () => {
    const metaWithComment = makeMetaCard('end', {
      comments: [{ id: 'c2', author: 'Claude', text: 'Sprint-Recap' }],
    })
    const lane = { id: 'lane-1', title: 'Lane', kind: 'lane', cards: [metaWithComment] }
    const state = makeState([lane, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const lane1 = result.board.sprints.find(s => s.id === 'lane-1')
    const metaCards = lane1.cards.filter(c => c.kind === 'sprint-meta')
    expect(metaCards.length).toBe(1)
  })

  it('berührt keine Meta-Karten in kind:sprint-Swimlanes', () => {
    const sprint = makeSprint('sprint-1', [makeCard()])
    const state  = makeState([sprint, { id: '__backlog__', title: 'Backlog', cards: [] }])
    const result = Storage.checkVersion(state)
    const s = result.board.sprints.find(s => s.id === 'sprint-1')
    const metaCards = s.cards.filter(c => c.kind === 'sprint-meta')
    /* Beide Meta-Karten (Start + End) müssen in kind:sprint bleiben */
    expect(metaCards.length).toBe(2)
  })

  it('ist idempotent — zweimaliger Cleanup ändert nichts', () => {
    const metaInBacklog = makeMetaCard('start')
    const state = makeState([
      { id: '__backlog__', title: 'Backlog', cards: [makeCard(), metaInBacklog] },
    ])
    const once  = Storage.checkVersion(state)
    const twice = Storage.checkVersion(JSON.parse(JSON.stringify(once)))
    const backlog = twice.board.sprints.find(s => s.id === '__backlog__')
    const metaCards = backlog.cards.filter(c => c.kind === 'sprint-meta')
    expect(metaCards.length).toBe(0)
  })
})

// ---------------------------------------------------------------------------
// F-364 + F-153 (Karte #373) — zwei Löschpfade ohne Guard
// ---------------------------------------------------------------------------

describe('F-364 — "Meta-Karte ohne Kommentare ist leer" war zu eng', () => {
  function meta(overrides = {}) {
    return {
      id: 'meta-x', displayId: 1, title: '📋 Sprint-Start',
      kind: 'sprint-meta', metaType: 'start', columnId: 'col-backlog',
      qa: false, comments: [], description: '', activity: [],
      ...overrides,
    }
  }

  it('istMetaKarteLeer: leer ist nur, was weder Beschreibung noch Kommentare noch Verlauf hat', () => {
    expect(istMetaKarteLeer(meta())).toBe(true)
    expect(istMetaKarteLeer(meta({ description: '   ' }))).toBe(true)   /* Whitespace zählt nicht */
    expect(istMetaKarteLeer(meta({ description: 'Übergabe an den nächsten Chat' }))).toBe(false)
    expect(istMetaKarteLeer(meta({ comments: [{ id: 'c', text: 'x' }] }))).toBe(false)
    expect(istMetaKarteLeer(meta({ activity: [{ type: 'sprint-activate' }] }))).toBe(false)
  })

  it('istMetaKarteLeer toleriert fehlende Felder (Alt-Daten ohne activity/description)', () => {
    expect(istMetaKarteLeer({ kind: 'sprint-meta' })).toBe(true)
    expect(istMetaKarteLeer(undefined)).toBe(true)
  })

  it('Meta-Karte mit NUR description überlebt die Kind-Umstellung sprint → lane', () => {
    const notiz = meta({ description: 'Nächster Chat: erst #371, dann #372.' })
    const lane  = { id: 'lane-1', title: 'Lane', kind: 'lane', cards: [notiz] }
    const state = makeState([lane, { id: '__backlog__', title: 'Backlog', cards: [] }])

    const result = Storage.checkVersion(state)

    const uebrig = result.board.sprints.find(s => s.id === 'lane-1').cards
      .filter(c => c.kind === 'sprint-meta')
    expect(uebrig.length).toBe(1)
    expect(uebrig[0].description).toContain('Nächster Chat')
  })

  it('Meta-Karte mit NUTZER-Verlaufseintrag überlebt den Cleanup-Pass', () => {
    const bearbeitet = meta({ activity: [{ action: 'field-updated', actor: 'Andi' }] })
    const state = makeState([{ id: '__backlog__', title: 'Backlog', cards: [bearbeitet] }])

    const result = Storage.checkVersion(state)

    expect(result.board.sprints.find(s => s.id === '__backlog__').cards
      .filter(c => c.kind === 'sprint-meta').length).toBe(1)
  })

  /* Sprint-51-Kritiker (K-D): System-Einträge sind Buchhaltung, kein Inhalt.
     Die erste Fassung zählte sie mit — dadurch immunisierte ein einziges
     `col:delete`-Rehoming (das selbst einen system-Eintrag schreibt) eine leere
     Meta-Karte dauerhaft gegen den #330-Cleanup. Zwei Fixes desselben Commits
     hoben sich damit gegenseitig auf. */
  it('System-Verlaufseinträge schützen NICHT vor dem Cleanup', () => {
    const nurSystem = meta({ activity: [
      { type: 'sprint-activate', actor: 'system' },
      { action: 'column-moved', actor: 'system', from: 'Erledigt', to: 'Backlog' },
    ] })
    const state = makeState([{ id: '__backlog__', title: 'Backlog', cards: [nurSystem] }])

    const result = Storage.checkVersion(state)

    expect(result.board.sprints.find(s => s.id === '__backlog__').cards
      .filter(c => c.kind === 'sprint-meta').length).toBe(0)
  })

  it('istMetaKarteLeer unterscheidet System- von Nutzer-Verlauf', () => {
    expect(istMetaKarteLeer(meta({ activity: [{ actor: 'system' }] }))).toBe(true)
    expect(istMetaKarteLeer(meta({ activity: [{ actor: 'Andi' }] }))).toBe(false)
    expect(istMetaKarteLeer(meta({ activity: [{ actor: 'system' }, { actor: 'Andi' }] }))).toBe(false)
  })

  it('wirklich leere Meta-Karte wird weiterhin aufgeräumt (#330 bleibt wirksam)', () => {
    const state = makeState([{ id: '__backlog__', title: 'Backlog', cards: [meta()] }])

    const result = Storage.checkVersion(state)

    expect(result.board.sprints.find(s => s.id === '__backlog__').cards
      .filter(c => c.kind === 'sprint-meta').length).toBe(0)
  })
})

/* Referenz-Spiegel der Spalten-Handler aus src/main.js (nicht exportiert —
   Konvention wie tests/sync-badge-noop-guard.test.js). Reihenfolge und
   Filterlogik VERBATIM, nur Links.purgeLinksTo ist ein Zähler. */
function colClear(board, colId, purge) {
  board.sprints.forEach(sprint => {
    const trifft = c => c.columnId === colId && c.kind !== 'sprint-meta'
    sprint.cards.filter(trifft).forEach(c => purge(c.id))
    sprint.cards = sprint.cards.filter(c => !trifft(c))
  })
}

/* Sprint-51-Kritiker (D-A): Der Spiegel liess den kompletten activity-Block weg,
   den derselbe Commit in main.js eingebaut hat — der F-475-Fix war dadurch
   ungetestet, obwohl F-478 „verbatim" behauptete. Jetzt vollstaendig, inklusive
   metaZielSpalte (K-C), Titel-Aufloesung und Zufalls-Suffix. */
function colDelete(board, colId, purge) {
  const alteSpaltenTitel = new Map(board.columnDefs.map(c => [c.id, c.title]))
  board.columnDefs = board.columnDefs.filter(c => c.id !== colId)
  const titelVon = id => board.columnDefs.find(c => c.id === id)?.title ?? id
  const geloeschterTitel = alteSpaltenTitel.get(colId) ?? colId
  board.sprints.forEach(sprint => {
    sprint.cards.forEach(c => {
      if (c.columnId !== colId || c.kind !== 'sprint-meta') return
      const ziel = metaZielSpalte(board.columnDefs, c.columnId)
      if (!ziel) return
      c.columnId = ziel
      if (!Array.isArray(c.activity)) c.activity = []
      const jetzt = new Date().toISOString()
      const rand  = Math.random().toString(36).slice(2, 8)
      c.activity.push({
        id:        `${jetzt}-col-delete-rehome-${rand}`,
        actor:     'system',
        timestamp: jetzt,
        action:    'column-moved',
        from:      geloeschterTitel,
        to:        titelVon(ziel),
      })
      c.updated = jetzt.slice(0, 10)
    })
    const trifft = c => c.columnId === colId && c.kind !== 'sprint-meta'
    sprint.cards.filter(trifft).forEach(c => purge(c.id))
    sprint.cards = sprint.cards.filter(c => !trifft(c))
  })
}

describe('F-153 — Spalten-Aktionen nehmen keine Meta-Karten mehr mit (main.js-Spiegel)', () => {
  let board, purged

  beforeEach(() => {
    purged = []
    board = {
      id: 'b', title: 'B', nextDisplayId: 9,
      columnDefs: [
        { id: 'col-backlog', title: 'Backlog' },
        { id: 'col-erledigt', title: 'Erledigt' },
      ],
      sprints: [{
        id: 'sprint-1', title: 'Sprint 1', kind: 'sprint',
        cards: [
          { id: 'start', kind: 'sprint-meta', metaType: 'start', columnId: 'col-erledigt',
            description: 'Übergabe-Notizen für den nächsten Chat', comments: [], activity: [] },
          { id: 'normal', columnId: 'col-erledigt', comments: [] },
        ],
      }],
    }
  })

  it('„Spalte leeren" löscht die normale Karte, nicht die Sprint-Start-Karte', () => {
    colClear(board, 'col-erledigt', id => purged.push(id))

    const karten = board.sprints[0].cards
    expect(karten.map(c => c.id)).toEqual(['start'])
    expect(karten[0].description).toContain('Übergabe-Notizen')
    expect(purged).toEqual(['normal'])          /* Meta-Karte auch nicht "entlinkt" */
  })

  it('„Spalte löschen" hängt die Meta-Karte um, statt sie zu löschen', () => {
    colDelete(board, 'col-erledigt', id => purged.push(id))

    const karten = board.sprints[0].cards
    expect(karten.map(c => c.id)).toEqual(['start'])
    expect(karten[0].columnId).toBe('col-backlog')   /* erste verbleibende Spalte */
    expect(purged).toEqual(['normal'])
  })

  it('„Spalte löschen" ohne verbleibende Spalte lässt die Meta-Karte unangetastet', () => {
    board.columnDefs = [{ id: 'col-erledigt', title: 'Erledigt' }]

    colDelete(board, 'col-erledigt', id => purged.push(id))

    const karten = board.sprints[0].cards
    expect(karten.map(c => c.id)).toEqual(['start'])
    expect(karten[0].columnId).toBe('col-erledigt')
  })

  it('normale Karten anderer Spalten bleiben unberührt', () => {
    board.sprints[0].cards.push({ id: 'andere', columnId: 'col-backlog', comments: [] })

    colClear(board, 'col-erledigt', id => purged.push(id))

    expect(board.sprints[0].cards.map(c => c.id).sort()).toEqual(['andere', 'start'])
  })
})

// ---------------------------------------------------------------------------
// F-126 (Karte #527) — col:delete-Pfad mit dangling-links-Entstehung
// ---------------------------------------------------------------------------

/* F-126: Test-Lücke — der `colDelete`-Spiegel oben (F-153) wurde bislang
   ausschliesslich mit einem ZÄHLER als `purge`-Callback aufgerufen
   (`id => purged.push(id)`), nie mit dem echten `Links.purgeLinksTo`. Damit
   war völlig ungetestet, ob eine Karte AUSSERHALB der gelöschten Spalte nach
   dem Löschen einen dangling `blocked-by`/`related`/…-Verweis auf die
   mitgelöschte Karte behält — genau der Fehler, den F-122 beheben musste
   (Rückverweise wurden dort nicht geputzt, bevor die Karten verschwanden).
   Ein Test, der `purge` durch die echte Funktion ersetzt, hätte F-122 vor
   dem Release gefangen. */
describe('F-126 — col:delete räumt Rückverweise auf mitgelöschte Karten auf (hätte F-122 gefangen)', () => {
  function boardMitVerweisen() {
    return {
      id: 'b', title: 'B', nextDisplayId: 9,
      columnDefs: [
        { id: 'col-backlog',  title: 'Backlog' },
        { id: 'col-erledigt', title: 'Erledigt' },
      ],
      sprints: [{
        id: 'sprint-1', title: 'Sprint 1', kind: 'sprint',
        cards: [
          { id: 'aussen',  columnId: 'col-backlog',  comments: [], links: [] },
          { id: 'aussen2', columnId: 'col-backlog',  comments: [], links: [] },
          { id: 'innen',   columnId: 'col-erledigt', comments: [], links: [] },
          { id: 'innen2',  columnId: 'col-erledigt', comments: [], links: [] },
        ],
      }],
    }
  }

  it('Karte AUSSERHALB der gelöschten Spalte verliert ihren Verweis auf eine mitgelöschte Karte', () => {
    const board = boardMitVerweisen()
    addLink(board, 'aussen', 'innen', 'blocks')
    expect(board.sprints[0].cards.find(c => c.id === 'aussen').links).toHaveLength(1)

    colDelete(board, 'col-erledigt', cardId => purgeLinksTo(board, cardId))

    const aussen = board.sprints[0].cards.find(c => c.id === 'aussen')
    /* Ohne den F-122-Fix bliebe hier ein dangling blocked-by-Verweis auf eine
       Karte, die es nicht mehr gibt — genau das soll dieser Test fangen. */
    expect(aussen.links).toEqual([])
    expect(board.sprints[0].cards.find(c => c.id === 'innen')).toBeUndefined()
  })

  it('mehrere gleichzeitig gelöschte Karten räumen jede für sich ihre Rückverweise auf', () => {
    const board = boardMitVerweisen()
    addLink(board, 'aussen',  'innen',  'blocks')
    addLink(board, 'aussen2', 'innen2', 'depends-on')

    colDelete(board, 'col-erledigt', cardId => purgeLinksTo(board, cardId))

    expect(board.sprints[0].cards.find(c => c.id === 'aussen').links).toEqual([])
    expect(board.sprints[0].cards.find(c => c.id === 'aussen2').links).toEqual([])
    expect(board.sprints[0].cards.map(c => c.id).sort()).toEqual(['aussen', 'aussen2'])
  })

  it('Verweise zwischen zwei Karten, die BEIDE mitgelöscht werden, hinterlassen nichts Dangling-es auf Restkarten', () => {
    const board = boardMitVerweisen()
    addLink(board, 'innen', 'innen2', 'related')
    addLink(board, 'aussen', 'innen', 'blocks')

    colDelete(board, 'col-erledigt', cardId => purgeLinksTo(board, cardId))

    expect(board.sprints[0].cards.find(c => c.id === 'aussen').links).toEqual([])
  })
})

/* F-126/Drift-Wächter: Der Spiegel oben prüft eine KOPIE der col:delete-Logik.
   Wer `Links.purgeLinksTo` im echten Handler entfernt oder vor die
   Meta-Karten-Umhängung schiebt, bekommt vom Spiegel allein kein rotes Licht
   (Konvention wie tests/mass-delete-guard.test.js "Drift-Wächter"). Dieser
   Block liest src/main.js als TEXT und prüft Anwesenheit + Reihenfolge des
   F-122-Fixes im echten Handler, ohne dass main.js importierbar sein müsste
   (main.js-Handler sind nicht exportiert — Bootstrap mit Seiteneffekten). */
describe('F-126 — Drift-Wächter: col:delete ruft Links.purgeLinksTo wirklich im echten main.js auf', () => {
  const quelle = readFileSync(join(process.cwd(), 'src', 'main.js'), 'utf8')

  /* Handler-Rumpf ausschneiden: von `EventBus.on('col:delete', ...)` bis zum
     nächsten `EventBus.on(` auf Grundeinrückung — dieselbe Technik wie beim
     showRecoveryBanner-Drift-Wächter in tests/mass-delete-guard.test.js. */
  const rumpf = (() => {
    const start = quelle.indexOf(`EventBus.on('col:delete',`)
    expect(start).toBeGreaterThan(-1)
    const ende = quelle.indexOf(`\nEventBus.on(`, start + 1)
    return quelle.slice(start, ende === -1 ? quelle.length : ende)
  })()

  it('ruft Links.purgeLinksTo für jede mitgelöschte Karte auf', () => {
    expect(rumpf).toContain('Links.purgeLinksTo(')
  })

  it('räumt die Rückverweise VOR dem eigentlichen Entfernen der Karten auf (F-122-Reihenfolge)', () => {
    const purgeAufruf  = rumpf.indexOf('Links.purgeLinksTo(')
    const kartenFilter = rumpf.indexOf('sprint.cards = sprint.cards.filter(')
    expect(purgeAufruf).toBeGreaterThan(-1)
    expect(kartenFilter).toBeGreaterThan(-1)
    /* Rückverweise putzen, BEVOR die Karten verschwinden — sonst zeigt der
       Verweis kurzzeitig auf eine schon entfernte Karte (Kommentar „F-122:
       Rückverweise putzen bevor Karten gelöscht werden" im Original). */
    expect(purgeAufruf).toBeLessThan(kartenFilter)
  })
})

// ---------------------------------------------------------------------------
// Sprint-51-Kritiker K3 — Meta-Karte darf nicht in eine gelöschte Spalte wandern
// ---------------------------------------------------------------------------

describe('K3 — Meta-Karten überleben das Löschen ihrer Spalte sichtbar', () => {
  function boardMitSpalten(spalten, karten) {
    return {
      version: '2',
      board: {
        id: 'b', title: 'B', nextDisplayId: 50,
        columnDefs: spalten.map(id => ({ id, title: id })),
        sprints: [
          { id: 'sprint-1', title: 'Sprint 1', kind: 'sprint', cards: karten },
          { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
        ],
      },
    }
  }

  function startKarte(columnId, extra = {}) {
    return {
      id: 'start', displayId: 1, kind: 'sprint-meta', metaType: 'start',
      title: '📋 Sprint-Start', columnId, qa: false, comments: [], activity: [],
      description: 'Übergabe-Notizen für den nächsten Chat', ...extra,
    }
  }

  it('aktiviert NICHT in eine col-erledigt, die es nicht gibt (sonst unsichtbar)', () => {
    /* Nutzer hat die Spalte „Erledigt" gelöscht; die Start-Karte wurde dabei
       nach col-backlog umgehängt (F-153-Rehoming). */
    const state = boardMitSpalten(['col-backlog', 'col-ready'], [
      startKarte('col-backlog'),
      { id: 'normal', displayId: 2, columnId: 'col-ready', comments: [], activity: [] },
      { id: 'ende', displayId: 3, kind: 'sprint-meta', metaType: 'end',
        title: '🏁 Sprint-Abschluss', columnId: 'col-backlog', qa: false, comments: [], activity: [] },
    ])

    const result = Storage.checkVersion(state)

    const start = result.board.sprints[0].cards.find(c => c.id === 'start')
    const spaltenIds = result.board.columnDefs.map(c => c.id)
    expect(spaltenIds).toContain(start.columnId)      /* der Kern: sichtbar geblieben */
    expect(start.columnId).toBe('col-backlog')
    expect(start.description).toContain('Übergabe-Notizen')
  })

  it('aktiviert weiterhin normal, wenn col-erledigt existiert', () => {
    const state = boardMitSpalten(['col-backlog', 'col-ready', 'col-erledigt'], [
      startKarte('col-backlog'),
      { id: 'normal', displayId: 2, columnId: 'col-ready', comments: [], activity: [] },
      { id: 'ende', displayId: 3, kind: 'sprint-meta', metaType: 'end',
        title: '🏁 Sprint-Abschluss', columnId: 'col-backlog', qa: false, comments: [], activity: [] },
    ])

    const result = Storage.checkVersion(state)

    const start = result.board.sprints[0].cards.find(c => c.id === 'start')
    expect(start.columnId).toBe('col-erledigt')
    expect(start.qa).toBe(true)
  })

  it('Selbstheilung: bereits verwaiste Meta-Karte wird in eine vorhandene Spalte zurückgeholt', () => {
    /* Board, das den Fehler schon erlitten hat: columnId zeigt ins Leere. */
    const state = boardMitSpalten(['col-backlog', 'col-ready'], [
      startKarte('col-erledigt'),          /* Spalte existiert nicht (mehr) */
      { id: 'normal', displayId: 2, columnId: 'col-ready', comments: [], activity: [] },
      { id: 'ende', displayId: 3, kind: 'sprint-meta', metaType: 'end',
        title: '🏁 Sprint-Abschluss', columnId: 'col-backlog', qa: false, comments: [], activity: [] },
    ])

    const result = Storage.checkVersion(state)

    const start = result.board.sprints[0].cards.find(c => c.id === 'start')
    expect(result.board.columnDefs.map(c => c.id)).toContain(start.columnId)
    expect(start.description).toContain('Übergabe-Notizen')   /* Inhalt intakt */
  })

  it('normale Karten mit ungültiger columnId bleiben unangetastet (bewusste Abgrenzung)', () => {
    const state = boardMitSpalten(['col-backlog', 'col-ready'], [
      startKarte('col-backlog'),
      { id: 'normal', displayId: 2, columnId: 'col-weg', comments: [], activity: [] },
      { id: 'ende', displayId: 3, kind: 'sprint-meta', metaType: 'end',
        title: '🏁 Sprint-Abschluss', columnId: 'col-backlog', qa: false, comments: [], activity: [] },
    ])

    const result = Storage.checkVersion(state)

    expect(result.board.sprints[0].cards.find(c => c.id === 'normal').columnId).toBe('col-weg')
  })
})

// ---------------------------------------------------------------------------
// Sprint-51-Kritiker K-C / D-A — Rehoming in eine SEMANTISCH passende Spalte
// ---------------------------------------------------------------------------

describe('K-C — metaZielSpalte hält die Sprint-Mechanik am Leben', () => {
  const defs = ids => ids.map(id => ({ id, title: id }))

  it('Erledigt gelöscht → Meta-Karte nach col-backlog (nicht in die erste Spalte)', () => {
    expect(metaZielSpalte(defs(['col-backlog', 'col-ready']), 'col-erledigt')).toBe('col-backlog')
  })

  it('Backlog gelöscht → Meta-Karte nach col-erledigt, NICHT nach col-ready', () => {
    /* Der eigentliche Befund: columnDefs[0] wäre hier 'col-ready' gewesen —
       damit hätte autoActivateSprintStart (verlangt col-backlog) nie wieder
       greifen können, und col-backlog kommt nie zurück (neue Spalten bekommen
       Zufalls-IDs). Der Sprint wäre dauerhaft nicht mehr aktivierbar. */
    expect(metaZielSpalte(defs(['col-ready', 'col-erledigt']), 'col-backlog')).toBe('col-erledigt')
  })

  it('keine der beiden Meta-Spalten übrig → erste verbleibende als Notnagel', () => {
    expect(metaZielSpalte(defs(['col-ready', 'col-test']), 'col-backlog')).toBe('col-ready')
  })

  it('gar keine Spalte übrig → null (Karte bleibt stehen)', () => {
    expect(metaZielSpalte([], 'col-backlog')).toBe(null)
  })

  it('„Spalte löschen" schreibt einen konventionskonformen Verlaufseintrag (F-475/D-F)', () => {
    const board = {
      id: 'b', title: 'B', nextDisplayId: 9,
      columnDefs: [{ id: 'col-backlog', title: 'Backlog' }, { id: 'col-erledigt', title: 'Erledigt' }],
      sprints: [{
        id: 'sprint-1', title: 'Sprint 1', kind: 'sprint',
        cards: [{ id: 'start', kind: 'sprint-meta', metaType: 'start', columnId: 'col-erledigt',
                  description: 'Notizen', comments: [], activity: [] }],
      }],
    }

    colDelete(board, 'col-erledigt', () => {})

    const karte = board.sprints[0].cards[0]
    expect(karte.columnId).toBe('col-backlog')
    expect(karte.activity.length).toBe(1)
    const e = karte.activity[0]
    /* D-F: gleiches Schema wie alle anderen Spalten-Bewegungen — sonst zeigt
       die Verlaufs-Anzeige deutschen Freitext statt eines übersetzten Labels. */
    expect(e.action).toBe('column-moved')
    expect(e.actor).toBe('system')
    expect(e.from).toBe('Erledigt')        /* Titel, nicht die rohe ID */
    expect(e.to).toBe('Backlog')
    expect(e.id).toMatch(/-col-delete-rehome-[a-z0-9]{1,6}$/)   /* Zufalls-Suffix gegen Kollision */
    expect(karte.updated).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })

  it('zwei Meta-Karten im selben Löschvorgang bekommen verschiedene Verlaufs-IDs (F-226-Muster)', () => {
    const board = {
      id: 'b', title: 'B', nextDisplayId: 9,
      columnDefs: [{ id: 'col-backlog', title: 'Backlog' }, { id: 'col-erledigt', title: 'Erledigt' }],
      sprints: [{
        id: 'sprint-1', title: 'Sprint 1', kind: 'sprint',
        cards: [
          { id: 'start', kind: 'sprint-meta', metaType: 'start', columnId: 'col-erledigt', comments: [], activity: [] },
          { id: 'ende',  kind: 'sprint-meta', metaType: 'end',   columnId: 'col-erledigt', comments: [], activity: [] },
        ],
      }],
    }

    colDelete(board, 'col-erledigt', () => {})

    const ids = board.sprints[0].cards.map(c => c.activity[0].id)
    expect(new Set(ids).size).toBe(2)
  })
})
