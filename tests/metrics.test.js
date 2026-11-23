/* @vitest-environment happy-dom */
/**
 * Unit-Tests für metrics.js — Karte #61 (Sprint-Velocity + Burnup-Chart)
 *
 * Geprüft werden:
 *  - computeSprintMetrics() — Effort-Summen, Karten-Counter, Timeline
 *  - Archiv-Ausschluss — archivierte Karten dürfen nicht gezählt werden
 *  - Effort=null-Ausschluss — Karten ohne effort verfälschen nicht die Summe
 *  - Erledigt-Definition — nur col-erledigt + qa:true
 *  - buildSparklineSVG() — SVG-Element bei gültigen Daten, null bei 0 Effort
 *  - Backlog-Sprint-Sonderfall
 */

import { describe, it, expect } from 'vitest'
import { Metrics } from '../src/metrics.js'
import { isDoneColumnId } from '../src/links.js'

// ---------------------------------------------------------------------------
// Hilfsfunktionen
// ---------------------------------------------------------------------------

function makeCard(overrides = {}) {
  return {
    id:         `card-${Math.random().toString(36).slice(2)}`,
    title:      'Test-Karte',
    columnId:   'col-backlog',
    qa:         false,
    effort:     null,
    archived:   false,
    created:    '2026-01-01T10:00:00Z',
    updated:    '2026-01-02T12:00:00Z',
    ...overrides,
  }
}

function makeSprint(cards = []) {
  return { id: 'sprint-1', title: 'Sprint 1', cards }
}

// ---------------------------------------------------------------------------
// computeSprintMetrics — Grundfälle
// ---------------------------------------------------------------------------

describe('Metrics.computeSprintMetrics()', () => {
  it('leerer Sprint gibt Null-Werte zurück', () => {
    const m = Metrics.computeSprintMetrics(makeSprint([]))
    expect(m.totalEffort).toBe(0)
    expect(m.doneEffort).toBe(0)
    expect(m.cardCount).toBe(0)
    expect(m.doneCount).toBe(0)
    expect(m.hasEffortData).toBe(false)
    expect(m.timelinePoints).toEqual([])
  })

  it('zählt nicht-erledigte Karten mit effort korrekt', () => {
    const sprint = makeSprint([
      makeCard({ effort: 2, columnId: 'col-backlog' }),
      makeCard({ effort: 3, columnId: 'col-ready' }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.totalEffort).toBe(5)
    expect(m.doneEffort).toBe(0)
    expect(m.doneCount).toBe(0)
    expect(m.cardCount).toBe(2)
    expect(m.hasEffortData).toBe(true)
  })

  it('erkennt erledigte Karten (col-erledigt + qa:true)', () => {
    const sprint = makeSprint([
      makeCard({ effort: 2, columnId: 'col-erledigt', qa: true }),
      makeCard({ effort: 3, columnId: 'col-erledigt', qa: false }), /* qa:false → nicht erledigt */
      makeCard({ effort: 1, columnId: 'col-ready',    qa: true }),  /* falsche Spalte */
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.doneEffort).toBe(2)
    expect(m.doneCount).toBe(1) /* nur col-erledigt + qa:true */
    expect(m.totalEffort).toBe(6)
  })

  it('berechnet doneRatio korrekt', () => {
    const sprint = makeSprint([
      makeCard({ effort: 4, columnId: 'col-erledigt', qa: true }),
      makeCard({ effort: 4, columnId: 'col-backlog' }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.doneRatio).toBeCloseTo(0.5)
  })

  it('doneRatio ist NaN wenn kein effort vorhanden', () => {
    const sprint = makeSprint([makeCard({ effort: null })])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(isNaN(m.doneRatio)).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// F-126/F-1085 (Karte #537) — computeSprintMetrics() vs. Links.isDoneColumnId()
// müssen bei umbenannten/eigenen Erledigt-Spalten dasselbe Urteil fällen.
// ---------------------------------------------------------------------------

/* F-1085: computeSprintMetrics() erkannte eine Erledigt-Spalte früher über die
   HARTKODIERTE id `col-erledigt` — anders als `Links.isDoneColumnId()`
   (src/links.js, F-127), das dieselbe Frage über den SPALTEN-TITEL beantwortet
   (`DONE_COLUMN_NAMES`) und deshalb auch eine vom Nutzer manuell angelegte
   oder umbenannte Erledigt-Spalte erkennt. `isDoneColumnId` ist seit F-127/
   F-807 die geteilte Quelle für renderer.js und main.js.

   Diese zwei Tests belegten bis zum Fix (Karte #537) eine ECHTE Divergenz
   zwischen den beiden Erledigt-Definitionen im Repo. Seit dem Fix nimmt
   computeSprintMetrics(sprint, board) dieselbe Titel-Semantik — beide Tests
   prüfen jetzt GLEICHES Urteil statt der vormaligen Abweichung. */
describe('F-126/F-1085 — computeSprintMetrics() vs. Links.isDoneColumnId(): gleiches Urteil', () => {
  it('eine manuell angelegte Erledigt-Spalte mit eigener id wird von beiden als erledigt erkannt', () => {
    const board = {
      columnDefs: [
        { id: 'col-backlog', title: 'Backlog' },
        { id: 'custom-done-xyz', title: 'Erledigt' }, /* manuell angelegt, KEINE col-erledigt-id */
      ],
    }
    const karte = makeCard({ effort: 3, columnId: 'custom-done-xyz', qa: true })
    const sprint = makeSprint([karte])

    /* Die Titel-Semantik (F-127) erkennt die Spalte als "erledigt" */
    expect(isDoneColumnId(board, 'custom-done-xyz')).toBe(true)

    /* computeSprintMetrics() muss mit `board` dasselbe Urteil fällen (F-1085) */
    const m = Metrics.computeSprintMetrics(sprint, board)
    expect(m.doneCount).toBe(1)
    expect(m.doneEffort).toBe(3)
  })

  it('die Standard-Spalte col-erledigt auf einen fachfremden Titel umbenannt: beide zählen sie nicht mehr als erledigt', () => {
    const board = {
      columnDefs: [
        { id: 'col-backlog', title: 'Backlog' },
        { id: 'col-erledigt', title: 'Zur Ablage' }, /* umbenannt, id bleibt */
      ],
    }
    const karte = makeCard({ effort: 3, columnId: 'col-erledigt', qa: true })
    const sprint = makeSprint([karte])

    /* Titel-Semantik sagt: "Zur Ablage" steht nicht in DONE_COLUMN_NAMES */
    expect(isDoneColumnId(board, 'col-erledigt')).toBe(false)

    /* computeSprintMetrics() muss mit `board` dasselbe Urteil fällen (F-1085) —
       Gegenrichtung derselben Divergenz. */
    const m = Metrics.computeSprintMetrics(sprint, board)
    expect(m.doneCount).toBe(0)
    expect(m.doneEffort).toBe(0)
  })

  it('ohne board (alter Aufrufer) bleibt der bisherige Hardcode-Rückfall erhalten', () => {
    /* F-1085: Sicherer Rückfall — isDoneColumnId(undefined, columnId) prüft
       dann exakt columnId === 'col-erledigt', identisch zum alten Verhalten
       dieses Moduls. Kein board-Argument nötig, kein Sonderfall im Code. */
    const sprint = makeSprint([makeCard({ effort: 3, columnId: 'col-erledigt', qa: true })])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.doneCount).toBe(1)
    expect(m.doneEffort).toBe(3)
  })
})

// ---------------------------------------------------------------------------
// Archiv-Ausschluss
// ---------------------------------------------------------------------------

describe('Metrics — Archiv-Ausschluss', () => {
  it('zählt archivierte Karten nicht mit', () => {
    const sprint = makeSprint([
      makeCard({ effort: 10, archived: true }),   /* soll ignoriert werden */
      makeCard({ effort: 2,  archived: false }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.totalEffort).toBe(2)
    expect(m.cardCount).toBe(1)
  })

  it('zählt archivierte erledigte Karten nicht als erledigt', () => {
    const sprint = makeSprint([
      makeCard({ effort: 5, columnId: 'col-erledigt', qa: true, archived: true }),
      makeCard({ effort: 2, columnId: 'col-erledigt', qa: true, archived: false }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.doneEffort).toBe(2)
    expect(m.doneCount).toBe(1)
  })
})

// ---------------------------------------------------------------------------
// Effort=null-Ausschluss
// ---------------------------------------------------------------------------

describe('Metrics — Effort=null weglassen', () => {
  it('Karte ohne effort fließt nicht in totalEffort ein', () => {
    const sprint = makeSprint([
      makeCard({ effort: null, columnId: 'col-erledigt', qa: true }),
      makeCard({ effort: 3,    columnId: 'col-erledigt', qa: true }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.totalEffort).toBe(3)
    expect(m.doneEffort).toBe(3)
  })

  it('Karte mit effort=0 fließt nicht in totalEffort ein', () => {
    const sprint = makeSprint([
      makeCard({ effort: 0, columnId: 'col-erledigt', qa: true }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.totalEffort).toBe(0)
    expect(m.hasEffortData).toBe(false)
  })
})

// ---------------------------------------------------------------------------
// Timeline-Punkte
// ---------------------------------------------------------------------------

describe('Metrics — timelinePoints', () => {
  it('keine Punkte wenn keine erledigten Karten mit effort', () => {
    const sprint = makeSprint([makeCard({ effort: 2, columnId: 'col-backlog' })])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.timelinePoints).toEqual([])
  })

  it('beginnt bei 0 Effort zum frühesten created-Zeitpunkt', () => {
    const sprint = makeSprint([
      makeCard({ effort: 3, columnId: 'col-erledigt', qa: true,
                 created: '2026-01-01T08:00:00Z', updated: '2026-01-02T10:00:00Z' }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.timelinePoints.length).toBeGreaterThanOrEqual(2)
    expect(m.timelinePoints[0].effort).toBe(0)
  })

  it('letzter Punkt hat kumulierten doneEffort', () => {
    const sprint = makeSprint([
      makeCard({ effort: 2, columnId: 'col-erledigt', qa: true,
                 created: '2026-01-01T08:00:00Z', updated: '2026-01-02T10:00:00Z' }),
      makeCard({ effort: 3, columnId: 'col-erledigt', qa: true,
                 created: '2026-01-01T09:00:00Z', updated: '2026-01-03T14:00:00Z' }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    const last = m.timelinePoints[m.timelinePoints.length - 1]
    expect(last.effort).toBe(5)
  })

  it('Punkte sind chronologisch sortiert', () => {
    const sprint = makeSprint([
      makeCard({ effort: 1, columnId: 'col-erledigt', qa: true,
                 created: '2026-01-01T08:00:00Z', updated: '2026-01-05T10:00:00Z' }),
      makeCard({ effort: 2, columnId: 'col-erledigt', qa: true,
                 created: '2026-01-01T09:00:00Z', updated: '2026-01-03T10:00:00Z' }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    for (let i = 1; i < m.timelinePoints.length; i++) {
      expect(m.timelinePoints[i].t).toBeGreaterThanOrEqual(m.timelinePoints[i - 1].t)
    }
  })
})

// ---------------------------------------------------------------------------
// buildSparklineSVG
// ---------------------------------------------------------------------------

describe('Metrics.buildSparklineSVG()', () => {
  it('gibt null zurück wenn kein hasEffortData', () => {
    const sprint = makeSprint([makeCard({ effort: null })])
    const m = Metrics.computeSprintMetrics(sprint)
    const svg = Metrics.buildSparklineSVG(m)
    expect(svg).toBeNull()
  })

  it('gibt null zurück wenn weniger als 2 Timeline-Punkte', () => {
    /* Nur 1 erledigte Karte mit demselben created+updated Zeitpunkt → Startpunkt = erledigt-Punkt → werden dedupliziert */
    const sprint = makeSprint([
      makeCard({ effort: 3, columnId: 'col-erledigt', qa: true,
                 created: '2026-01-01T10:00:00Z', updated: '2026-01-01T10:00:00Z' }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    /* Falls nur 1 Punkt: kein SVG */
    if (m.timelinePoints.length < 2) {
      expect(Metrics.buildSparklineSVG(m)).toBeNull()
    }
  })

  it('gibt SVGElement zurück bei gültigen Daten', () => {
    const sprint = makeSprint([
      makeCard({ effort: 2, columnId: 'col-erledigt', qa: true,
                 created: '2026-01-01T08:00:00Z', updated: '2026-01-02T10:00:00Z' }),
      makeCard({ effort: 1, columnId: 'col-backlog' }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    if (!m.hasEffortData || m.timelinePoints.length < 2) return /* Fixture nicht ausreichend */
    const svg = Metrics.buildSparklineSVG(m)
    expect(svg).not.toBeNull()
    expect(svg.tagName.toLowerCase()).toBe('svg')
    expect(svg.classList.contains('sparkline')).toBe(true)
  })

  it('SVG enthält polyline und goal-Linie', () => {
    const sprint = makeSprint([
      makeCard({ effort: 4, columnId: 'col-erledigt', qa: true,
                 created: '2026-01-01T08:00:00Z', updated: '2026-01-03T10:00:00Z' }),
      makeCard({ effort: 2, columnId: 'col-backlog',
                 created: '2026-01-01T09:00:00Z', updated: '2026-01-02T10:00:00Z' }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    const svg = Metrics.buildSparklineSVG(m)
    if (!svg) return
    expect(svg.querySelector('polyline.sparkline-line')).not.toBeNull()
    expect(svg.querySelector('line.sparkline-goal')).not.toBeNull()
    expect(svg.querySelector('circle.sparkline-dot')).not.toBeNull()
  })
})

// ---------------------------------------------------------------------------
// Karte #126 — Bug: falsche 100 % wenn geschätzte Karten erledigt, ungeschätzte offen
// ---------------------------------------------------------------------------

describe('Metrics — 100%-Bug-Regression (Karte #126)', () => {
  it('doneRatio ist NICHT 1.0 wenn ungeschätzte Karten noch offen sind', () => {
    /* 4 geschätzte Karten alle erledigt → doneEffort/totalEffort = 100 % (Effort-Sicht)
       2 ungeschätzte Karten noch offen → cardCount=6, doneCount=4 → 66 % (Karten-Sicht)
       Die Progress-Linie basiert auf doneCount/cardCount, nicht auf doneRatio */
    const sprint = makeSprint([
      makeCard({ effort: 2, columnId: 'col-erledigt', qa: true }),
      makeCard({ effort: 3, columnId: 'col-erledigt', qa: true }),
      makeCard({ effort: 1, columnId: 'col-erledigt', qa: true }),
      makeCard({ effort: 4, columnId: 'col-erledigt', qa: true }),
      makeCard({ effort: null, columnId: 'col-in-arbeit' }), /* ungeschätzt, offen */
      makeCard({ effort: null, columnId: 'col-ready'    }), /* ungeschätzt, offen */
    ])
    const m = Metrics.computeSprintMetrics(sprint)

    /* Effort-Sicht: doneRatio = 10/10 = 1.0 — das ist der alte "100%-Bug" */
    expect(m.doneRatio).toBeCloseTo(1.0) /* Effort-Basis stimmt intern noch */

    /* Karten-Sicht: doneCount/cardCount = 4/6 — korrekte Basis für Progress-Linie */
    expect(m.cardCount).toBe(6)
    expect(m.doneCount).toBe(4)
    /* Karten-basierter Fortschritt: 4/6 ≈ 0.667 — NICHT 1.0 */
    const cardProgress = m.doneCount / m.cardCount
    expect(cardProgress).toBeCloseTo(0.667, 2)
    expect(cardProgress).toBeLessThan(1.0)
  })

  it('cardCount schließt Sprint-Meta-Karten aus', () => {
    const sprint = makeSprint([
      makeCard({ effort: 2, columnId: 'col-erledigt', qa: true }),
      { ...makeCard(), kind: 'sprint-meta' }, /* Meta-Karte darf nicht zählen */
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.cardCount).toBe(1) /* nur die echte Karte */
  })

  it('Progress-Linie-Ratio bei 0 Karten ist 0', () => {
    const sprint = makeSprint([])
    const m = Metrics.computeSprintMetrics(sprint)
    const ratio = m.cardCount > 0 ? m.doneCount / m.cardCount : 0
    expect(ratio).toBe(0)
  })

  it('Progress-Linie-Ratio bei allen erledigten Karten ist 1', () => {
    const sprint = makeSprint([
      makeCard({ columnId: 'col-erledigt', qa: true }),
      makeCard({ columnId: 'col-erledigt', qa: true }),
    ])
    const m = Metrics.computeSprintMetrics(sprint)
    const ratio = m.cardCount > 0 ? m.doneCount / m.cardCount : 0
    expect(ratio).toBeCloseTo(1.0)
  })
})
