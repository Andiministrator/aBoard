/* @vitest-environment happy-dom */

/**
 * Tests für Karte #124 / ADR-0046 — Swimlane-Kind-Flag (Sprint vs. Lane vs. Backlog).
 *
 * Geprüft werden:
 *  - migrateLaneKind: fehlendes `kind` → 'backlog' für __backlog__, sonst 'sprint'
 *  - Idempotenz: zweimaliges Laden ändert nichts
 *  - Defensive Typ-Normalisierung: ungültige `kind`-Werte werden korrigiert
 *  - migrateSprintMeta: für `kind === 'lane'` werden KEINE Meta-Karten angelegt
 *  - Metrics.computeSprintMetrics: Lanes liefern leere/neutrale Metriken
 *  - Renderer: Sprint-Header einer Lane hat KEIN Velocity-Widget + KEINE Progress-Linie
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { Storage } from '../src/storage.js'
import { Metrics } from '../src/metrics.js'

/* ── Renderer-Mocks (analog renderer.dom.test.js) ────────────────── */
vi.mock('../src/sync.js', () => ({
  Sync: { getDirectoryName: vi.fn(() => null), listKnownBoards: vi.fn(async () => []) },
}))
vi.mock('../src/dragdrop.js', () => ({
  DragDrop: { takePendingFlip: vi.fn(() => null), init: vi.fn() },
}))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Filter }   from '../src/filter.js'
import { State }    from '../src/state.js'
import { Renderer } from '../src/renderer.js'

/* ── Hilfsfunktionen ─────────────────────────────────────────────── */

function makeCard(overrides = {}) {
  return {
    id:          `c-${Math.random().toString(36).slice(2)}`,
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
    archived:    false,
    columnId:    'col-backlog',
    created:     '2026-01-01',
    updated:     '2026-01-01',
    ...overrides,
  }
}

function makeState(sprints) {
  return {
    version: '2',
    board: {
      id: 'board-1',
      title: 'Test-Board',
      nextDisplayId: 1,
      columnDefs: [],
      sprints,
    },
  }
}

/* ─────────────────────────────────────────────────────────────────
   1) migrateLaneKind — Defaults + Idempotenz
   ───────────────────────────────────────────────────────────────── */

describe('Storage.checkVersion → migrateLaneKind (ADR-0046)', () => {
  it('Backlog-Swimlane ohne kind bekommt kind: "backlog"', () => {
    const state = makeState([{ id: '__backlog__', title: 'Backlog', cards: [] }])
    const out = Storage.checkVersion(state)
    const backlog = out.board.sprints.find(s => s.id === '__backlog__')
    expect(backlog.kind).toBe('backlog')
  })

  it('Nicht-Backlog-Swimlane ohne kind bekommt kind: "sprint"', () => {
    const state = makeState([
      { id: 'sprint-1', title: 'Sprint 1', cards: [makeCard()] },
      { id: '__backlog__', title: 'Backlog', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    const s = out.board.sprints.find(s => s.id === 'sprint-1')
    expect(s.kind).toBe('sprint')
  })

  it('ist idempotent — zweimaliges Laden ändert kind nicht', () => {
    const state = makeState([
      { id: 'sprint-1', title: 'Sprint 1', cards: [makeCard()] },
      { id: '__backlog__', title: 'Backlog', cards: [] },
    ])
    const once  = Storage.checkVersion(state)
    const twice = Storage.checkVersion(JSON.parse(JSON.stringify(once)))
    expect(twice.board.sprints.find(s => s.id === 'sprint-1').kind).toBe('sprint')
    expect(twice.board.sprints.find(s => s.id === '__backlog__').kind).toBe('backlog')
  })

  it('Backlog-Swimlane wird ans Ende sortiert (immer unterste Swimlane)', () => {
    const state = makeState([
      { id: '__backlog__', title: 'Backlog', cards: [] },
      { id: 'sprint-1', title: 'Sprint 1', cards: [makeCard()] },
    ])
    const out = Storage.checkVersion(state)
    expect(out.board.sprints[out.board.sprints.length - 1].id).toBe('__backlog__')
    // idempotent: erneut laden lässt den Backlog letzter
    const twice = Storage.checkVersion(JSON.parse(JSON.stringify(out)))
    expect(twice.board.sprints[twice.board.sprints.length - 1].id).toBe('__backlog__')
    expect(twice.board.sprints.length).toBe(2)
  })

  it('bestehender Wert "lane" bleibt erhalten', () => {
    const state = makeState([
      { id: 'lane-1', title: 'Person Anna', kind: 'lane', cards: [] },
      { id: '__backlog__', title: 'Backlog', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    expect(out.board.sprints.find(s => s.id === 'lane-1').kind).toBe('lane')
  })

  it('bestehender Wert "service" bleibt erhalten (ADR-0092)', () => {
    const state = makeState([
      { id: 'service', title: 'Service', kind: 'service', cards: [] },
      { id: '__backlog__', title: 'Backlog', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    expect(out.board.sprints.find(s => s.id === 'service').kind).toBe('service')
  })

  it('Service-Swimlane bekommt keine Sprint-Meta-Karten (ADR-0092)', () => {
    const state = makeState([
      { id: 'service', title: 'Service', kind: 'service', cards: [makeCard()] },
      { id: '__backlog__', title: 'Backlog', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    const service = out.board.sprints.find(s => s.id === 'service')
    const meta = service.cards.filter(c => c.kind === 'sprint-meta')
    expect(meta.length).toBe(0)
  })

  it('ungültiger kind-Wert wird normalisiert (Nicht-Backlog → "sprint")', () => {
    const state = makeState([
      { id: 'sprint-1', title: 'X', kind: 'foobar', cards: [] },
      { id: '__backlog__', title: 'Backlog', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    expect(out.board.sprints.find(s => s.id === 'sprint-1').kind).toBe('sprint')
  })

  /* Karte #332: UUID-ID-Swimlane mit kind: 'backlog' bleibt 'backlog' —
     der frühere Flip auf 'sprint' war der Bug (erzeugte Sprint-Meta-Karten). */
  it('UUID-ID-Swimlane mit kind "backlog" bleibt "backlog" (Karte #332 Bug-Fix)', () => {
    const uuid = 'f47ac10b-58cc-4372-a567-0e02b2c3d479'
    const state = makeState([
      { id: uuid, title: 'Backlog (UUID)', kind: 'backlog', cards: [] },
      { id: '__backlog__', title: 'Sentinel-Backlog', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    expect(out.board.sprints.find(s => s.id === uuid).kind).toBe('backlog')
  })

  it('__backlog__ mit kind "sprint" wird auf "backlog" korrigiert', () => {
    const state = makeState([{ id: '__backlog__', title: 'Backlog', kind: 'sprint', cards: [] }])
    const out = Storage.checkVersion(state)
    expect(out.board.sprints.find(s => s.id === '__backlog__').kind).toBe('backlog')
  })

  it('numerischer kind-Wert (kein String) wird normalisiert', () => {
    const state = makeState([
      { id: 'sprint-1', title: 'X', kind: 42, cards: [] },
      { id: '__backlog__', title: 'Backlog', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    expect(out.board.sprints.find(s => s.id === 'sprint-1').kind).toBe('sprint')
  })
})

/* ─────────────────────────────────────────────────────────────────
   Karte #332 — UUID-Backlog-Bug-Fix-Tests
   AK 1: UUID-ID + kind:'backlog' → bleibt 'backlog'
   AK 2: __backlog__ + kind:'sprint' → wird auf 'backlog' korrigiert (Regression)
   AK 3: vollständiger Migrations-Lauf → keine sprint-meta in UUID-Backlog
   ───────────────────────────────────────────────────────────────── */

describe('migrateLaneKind + migrateSprintMeta — UUID-Backlog-Bug (Karte #332)', () => {
  /* AK 1: UUID-ID-Backlog-Swimlane behält kind:'backlog' */
  it('AK1 — UUID-Backlog-Swimlane: kind "backlog" bleibt erhalten (kein Flip auf "sprint")', () => {
    const uuid = 'a1b2c3d4-e5f6-7890-abcd-ef1234567890'
    const state = makeState([
      { id: uuid, title: 'Mein Backlog', kind: 'backlog', cards: [] },
      { id: '__backlog__', title: 'Sentinel', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    const lane = out.board.sprints.find(s => s.id === uuid)
    expect(lane.kind).toBe('backlog')
  })

  /* AK 2: __backlog__ mit kind:'sprint' (versehentlich) → wird auf 'backlog' korrigiert
     (Regression-Test — diese erste Korrekttur bleibt erhalten) */
  it('AK2 — __backlog__ mit kind "sprint" wird auf "backlog" korrigiert (Regression)', () => {
    const state = makeState([
      { id: 'sprint-42', title: 'Sprint 42', kind: 'sprint', cards: [makeCard()] },
      { id: '__backlog__', title: 'Backlog', kind: 'sprint', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    expect(out.board.sprints.find(s => s.id === '__backlog__').kind).toBe('backlog')
    /* Sprint bleibt sprint */
    expect(out.board.sprints.find(s => s.id === 'sprint-42').kind).toBe('sprint')
  })

  /* AK 3: vollständiger Migrations-Lauf (migrateLaneKind + migrateSprintMeta) →
     keine sprint-meta-Karten in der UUID-Backlog-Swimlane */
  it('AK3 — UUID-Backlog: nach vollständigem checkVersion-Lauf keine sprint-meta-Karten', () => {
    const uuid = 'deadbeef-0000-1111-2222-333344445555'
    const normalCard = makeCard({ columnId: 'col-backlog' })
    const state = makeState([
      { id: 'sprint-1', title: 'Sprint 1', kind: 'sprint', cards: [makeCard()] },
      { id: uuid, title: 'UUID-Backlog', kind: 'backlog', cards: [normalCard] },
      { id: '__backlog__', title: 'Sentinel-Backlog', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    const uuidLane = out.board.sprints.find(s => s.id === uuid)
    expect(uuidLane.kind).toBe('backlog')
    /* Keine sprint-meta-Karten dürfen in der UUID-Backlog-Swimlane entstehen */
    const metaCards = uuidLane.cards.filter(c => c.kind === 'sprint-meta')
    expect(metaCards.length).toBe(0)
    /* Der echte Sprint bekommt weiterhin Start + End */
    const sprint1 = out.board.sprints.find(s => s.id === 'sprint-1')
    const sprintMeta = sprint1.cards.filter(c => c.kind === 'sprint-meta')
    expect(sprintMeta.length).toBe(2)
  })
})

/* ─────────────────────────────────────────────────────────────────
   2) migrateSprintMeta-Guard — keine Meta-Karten für Lanes
   ───────────────────────────────────────────────────────────────── */

describe('migrateSprintMeta-Guard (ADR-0046)', () => {
  it('Lane bekommt KEINE Sprint-Meta-Karten', () => {
    const state = makeState([
      { id: 'lane-1', title: 'Person Anna', kind: 'lane', cards: [makeCard()] },
      { id: '__backlog__', title: 'Backlog', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    const lane = out.board.sprints.find(s => s.id === 'lane-1')
    const meta = lane.cards.filter(c => c.kind === 'sprint-meta')
    expect(meta.length).toBe(0)
  })

  it('Sprint (default-kind) bekommt weiterhin Start + End', () => {
    const state = makeState([
      { id: 'sprint-1', title: 'Sprint 1', cards: [makeCard()] },
      { id: '__backlog__', title: 'Backlog', cards: [] },
    ])
    const out = Storage.checkVersion(state)
    const s = out.board.sprints.find(s => s.id === 'sprint-1')
    const meta = s.cards.filter(c => c.kind === 'sprint-meta')
    expect(meta.length).toBe(2)
    expect(meta.some(c => c.metaType === 'start')).toBe(true)
    expect(meta.some(c => c.metaType === 'end')).toBe(true)
  })
})

/* ─────────────────────────────────────────────────────────────────
   3) computeSprintMetrics skippt Lanes
   ───────────────────────────────────────────────────────────────── */

describe('Metrics.computeSprintMetrics — Lane-Skip (ADR-0046)', () => {
  it('Lane liefert neutrale Metriken (cardCount = 0, hasEffortData = false)', () => {
    const lane = {
      id: 'lane-1', title: 'Anna', kind: 'lane',
      cards: [
        makeCard({ effort: 4, qa: true, columnId: 'col-erledigt', updated: '2026-02-02' }),
        makeCard({ effort: 2 }),
      ],
    }
    const m = Metrics.computeSprintMetrics(lane)
    expect(m.cardCount).toBe(0)
    expect(m.doneCount).toBe(0)
    expect(m.totalEffort).toBe(0)
    expect(m.doneEffort).toBe(0)
    expect(m.hasEffortData).toBe(false)
    expect(m.timelinePoints).toEqual([])
  })

  it('Sprint liefert weiterhin echte Metriken', () => {
    const sprint = {
      id: 'sprint-1', title: 'S1', kind: 'sprint',
      cards: [
        makeCard({ effort: 4, qa: true, columnId: 'col-erledigt', updated: '2026-02-02' }),
        makeCard({ effort: 2 }),
      ],
    }
    const m = Metrics.computeSprintMetrics(sprint)
    expect(m.cardCount).toBe(2)
    expect(m.doneCount).toBe(1)
    expect(m.totalEffort).toBe(6)
    expect(m.doneEffort).toBe(4)
    expect(m.hasEffortData).toBe(true)
  })
})

/* ─────────────────────────────────────────────────────────────────
   4) Renderer — Lane-Header ohne Velocity + ohne Progress-Linie
   ───────────────────────────────────────────────────────────────── */

let _laneTestCounter = 0
function nextColId() { return `col-lt-${++_laneTestCounter}` }

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

describe('Renderer — Lane-Header ohne Sprint-Mechanik (ADR-0046)', () => {
  it('Lane hat KEIN .sprint-velocity-Widget', () => {
    const cid = nextColId()
    const board = {
      id: 'board-lane', title: 'B',
      columnDefs: [{ id: cid, title: 'Todo' }],
      sprints: [
        { id: 's-lane-1', title: 'Person Anna', kind: 'lane',
          cards: [makeCard({ columnId: cid, effort: 4 })] },
        { id: 's-lane-2', title: 'Dummy', kind: 'sprint', cards: [] },
      ],
    }
    const container = setupBoardDOM(board)
    /* Erste Swimlane = Lane → kein Velocity-Widget */
    const lanes = container.querySelectorAll('.swimlane')
    expect(lanes.length).toBe(2)
    const laneHeader = lanes[0].querySelector('.sprint-header')
    expect(laneHeader).toBeTruthy()
    expect(laneHeader.querySelector('.sprint-velocity')).toBeNull()
    /* Zweite Swimlane = Sprint → Velocity vorhanden */
    expect(lanes[1].querySelector('.sprint-velocity')).toBeTruthy()
  })

  it('Lane hat KEINE Progress-Linie', () => {
    const cid = nextColId()
    const board = {
      id: 'board-lane-2', title: 'B',
      columnDefs: [{ id: cid, title: 'Todo' }],
      sprints: [
        { id: 's-lane-3', title: 'Thema X', kind: 'lane', cards: [] },
        { id: 's-sprint-4', title: 'Sprint', kind: 'sprint', cards: [] },
      ],
    }
    const container = setupBoardDOM(board)
    const lanes = container.querySelectorAll('.swimlane')
    /* Lane (Index 0) hat keine Progress-Linie, Sprint (Index 1) hat eine */
    expect(lanes[0].querySelector('.sprint-progress-line')).toBeNull()
    expect(lanes[1].querySelector('.sprint-progress-line')).toBeTruthy()
  })

  it('Lane-Header trägt CSS-Hook-Klasse .sprint-header--lane', () => {
    const cid = nextColId()
    const board = {
      id: 'board-lane-3', title: 'B',
      columnDefs: [{ id: cid, title: 'Todo' }],
      sprints: [
        { id: 's-lane-5', title: 'Lane', kind: 'lane', cards: [] },
        { id: 's-sprint-6', title: 'Sprint', kind: 'sprint', cards: [] },
      ],
    }
    const container = setupBoardDOM(board)
    const lanes = container.querySelectorAll('.swimlane')
    expect(lanes[0].querySelector('.sprint-header--lane')).toBeTruthy()
    expect(lanes[1].querySelector('.sprint-header--lane')).toBeNull()
  })
})

/* ─────────────────────────────────────────────────────────────────
   4b) F-216 — der Bestaetigungstext beim Wechsel sprint→lane muss zu dem
   passen, was `migrateSprintMeta` danach tatsaechlich tut.

   Der Text sagte pauschal „Meta-Karten bleiben als Historie". Das gilt aber
   nur fuer Meta-Karten MIT Inhalt; leere entfernt der Cleanup-Pass beim
   naechsten Board-Load, und beim Zurueckschalten entstehen neue mit neuer
   displayId. Fuer einen frisch angelegten Sprint versprach der Dialog also
   das Gegenteil dessen, was passiert.

   Diese Tests greifen den Text ab, den `confirm()` bekommt — genau das ist
   die Zusage an den Nutzer.
   ───────────────────────────────────────────────────────────────── */
import { I18n } from '../src/i18n.js'

describe('Renderer — Bestaetigung beim Wechsel sprint→lane (F-216)', () => {
  /** Rendert ein Board mit EINER Sprint-Swimlane, klickt den Kind-Umschalter
   *  und gibt den Text zurueck, den confirm() gesehen hat. Der Klick wird
   *  ueber die Rueckgabe `false` abgebrochen — getestet wird die Zusage,
   *  nicht die Mutation. */
  function textDesUmschaltDialogs(metaKarten) {
    const cid = nextColId()
    const board = {
      id: 'board-f216', title: 'B',
      columnDefs: [{ id: cid, title: 'Todo' }],
      sprints: [
        { id: 's-f216', title: 'Sprint 99', kind: 'sprint', cards: metaKarten },
        /* Zweite Swimlane noetig: der Renderer zeichnet den Sprint-Header
           (und damit den Kind-Umschalter) erst ab zwei Swimlanes — bei einer
           einzigen gibt es nichts zu unterscheiden. Ohne sie liefe der Test
           gegen `null` und waere aus dem falschen Grund rot. */
        { id: 's-f216-b', title: 'Zweite', kind: 'sprint', cards: [] },
      ],
    }
    const container = setupBoardDOM(board)
    const lanes = container.querySelectorAll('.swimlane')
    expect(lanes.length).toBe(2)
    const btn = lanes[0].querySelector('.btn-sprint-kind')
    expect(btn).toBeTruthy()
    expect(btn.dataset.kind).toBe('sprint')

    const origConfirm = window.confirm
    let gesehen = null
    window.confirm = (msg) => { gesehen = msg; return false }
    try {
      btn.click()
    } finally {
      window.confirm = origConfirm
    }
    return gesehen
  }

  function metaKarte(metaType, overrides = {}) {
    return {
      id: `meta-${metaType}-${Math.random().toString(36).slice(2)}`,
      displayId: 1, title: metaType === 'start' ? '📋 Sprint-Start' : '🏁 Sprint-Abschluss',
      description: '', labels: [], owner: '', qa: false, critical: false, blocked: false,
      columnId: 'col-backlog', kind: 'sprint-meta', metaType,
      comments: [], activity: [],
      ...overrides,
    }
  }

  it('leere Meta-Karten: der Dialog sagt, dass sie entfernt werden (nicht dass sie bleiben)', () => {
    const text = textDesUmschaltDialogs([metaKarte('start'), metaKarte('end')])
    expect(text).toBe(I18n.t('swimlane.toggle.confirm-to-lane-leer'))
    /* Die entscheidende Eigenschaft, unabhaengig vom genauen Wortlaut:
       der Text darf nicht das Gegenteil dessen behaupten, was passiert. */
    expect(text).not.toBe(I18n.t('swimlane.toggle.confirm-to-lane'))
  })

  it('Meta-Karte mit Recap im Beschreibungstext: der Dialog sagt, dass sie bleibt', () => {
    const text = textDesUmschaltDialogs([
      metaKarte('start'),
      metaKarte('end', { description: 'Recap: drei Karten erledigt, zwei verschoben.' }),
    ])
    expect(text).toBe(I18n.t('swimlane.toggle.confirm-to-lane'))
  })

  it('Meta-Karte mit Kommentar zaehlt ebenfalls als Inhalt', () => {
    const text = textDesUmschaltDialogs([
      metaKarte('start', { comments: [{ id: 'k1', author: 'Andi', text: 'Uebergabe', created: '2026-08-19' }] }),
      metaKarte('end'),
    ])
    expect(text).toBe(I18n.t('swimlane.toggle.confirm-to-lane'))
  })

  it('nur System-Eintraege im Verlauf zaehlen NICHT als Inhalt (F-364-Praezisierung)', () => {
    /* `istMetaKarteLeer` ignoriert `actor: "system"` bewusst — sonst waere eine
       einmal aktivierte Start-Karte dauerhaft unaufraeumbar. Der Dialog muss
       derselben Regel folgen, sonst sagt er wieder etwas anderes als der
       Cleanup-Pass tut. */
    const text = textDesUmschaltDialogs([
      metaKarte('start', { activity: [{ actor: 'system', type: 'sprint-activate', ts: '2026-08-19' }] }),
      metaKarte('end'),
    ])
    expect(text).toBe(I18n.t('swimlane.toggle.confirm-to-lane-leer'))
  })
})

/* ─────────────────────────────────────────────────────────────────
   5) sprint:set-kind-EventBus-Handler (ADR-0046, F-212)

   Der Handler in src/main.js ist nicht exportiert.
   Wir testen die Logik über eine lokale Referenz-Implementierung —
   identisch zur Produktionslogik (analog zu recurrence.test.js).
   ───────────────────────────────────────────────────────────────── */

import { Model } from '../src/model.js'

/**
 * Referenz-Implementierung des sprint:set-kind-Handlers aus main.js.
 * Ändert den übergebenen Board-State in-place.
 */
function applySetKind(board, sprintId, kind) {
  if (sprintId === '__backlog__') return;
  if (kind !== 'sprint' && kind !== 'lane') return;
  const sprint = board.sprints.find(s => s.id === sprintId);
  if (!sprint || sprint.kind === kind) return;
  sprint.kind = kind;
  /* Bei lane → sprint: Meta-Karten direkt nachrüsten */
  if (kind === 'sprint') {
    const hasStart = sprint.cards.some(c => c.kind === 'sprint-meta' && c.metaType === 'start');
    const hasEnd   = sprint.cards.some(c => c.kind === 'sprint-meta' && c.metaType === 'end');
    if (!hasStart) {
      const startId = Model.assignDisplayId(board);
      sprint.cards.unshift(Model.createSprintMetaCard('start', startId, 'col-backlog', false));
    }
    if (!hasEnd) {
      const endId = Model.assignDisplayId(board);
      sprint.cards.push(Model.createSprintMetaCard('end', endId, 'col-backlog', false));
    }
  }
}

describe('sprint:set-kind-Handler (ADR-0046, F-212)', () => {
  it('lane → sprint: Meta-Karten (start + end) werden sofort angelegt', () => {
    const board = {
      id: 'b-1', title: 'T', nextDisplayId: 10,
      columnDefs: [],
      sprints: [
        { id: 'lane-1', title: 'Person Anna', kind: 'lane', cards: [makeCard()] },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    applySetKind(board, 'lane-1', 'sprint');
    const sprint = board.sprints.find(s => s.id === 'lane-1');
    expect(sprint.kind).toBe('sprint');
    const metaCards = sprint.cards.filter(c => c.kind === 'sprint-meta');
    expect(metaCards.length).toBe(2);
    expect(metaCards.some(c => c.metaType === 'start')).toBe(true);
    expect(metaCards.some(c => c.metaType === 'end')).toBe(true);
  });

  it('lane → sprint: Start-Karte steht am Anfang, End-Karte am Ende', () => {
    const board = {
      id: 'b-2', title: 'T', nextDisplayId: 1,
      columnDefs: [],
      sprints: [
        { id: 'lane-2', title: 'Thema X', kind: 'lane', cards: [makeCard()] },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    applySetKind(board, 'lane-2', 'sprint');
    const sprint = board.sprints.find(s => s.id === 'lane-2');
    expect(sprint.cards[0].kind).toBe('sprint-meta');
    expect(sprint.cards[0].metaType).toBe('start');
    expect(sprint.cards[sprint.cards.length - 1].kind).toBe('sprint-meta');
    expect(sprint.cards[sprint.cards.length - 1].metaType).toBe('end');
  });

  it('sprint → lane: kind wird auf "lane" gesetzt', () => {
    const board = {
      id: 'b-3', title: 'T', nextDisplayId: 1,
      columnDefs: [],
      sprints: [
        { id: 'sprint-1', title: 'Sprint 1', kind: 'sprint', cards: [makeCard()] },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    applySetKind(board, 'sprint-1', 'lane');
    const sprint = board.sprints.find(s => s.id === 'sprint-1');
    expect(sprint.kind).toBe('lane');
  });

  it('sprint → lane: vorhandene Meta-Karten bleiben als Historie erhalten', () => {
    const board = {
      id: 'b-4', title: 'T', nextDisplayId: 5,
      columnDefs: [],
      sprints: [
        {
          id: 'sprint-2', title: 'Sprint 2', kind: 'sprint',
          cards: [
            Model.createSprintMetaCard('start', 1, 'col-erledigt', true),
            makeCard(),
            Model.createSprintMetaCard('end', 2, 'col-erledigt', true),
          ],
        },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    applySetKind(board, 'sprint-2', 'lane');
    const sprint = board.sprints.find(s => s.id === 'sprint-2');
    expect(sprint.kind).toBe('lane');
    /* Bestehende Meta-Karten bleiben erhalten — kein destruktives Löschen */
    const metaCards = sprint.cards.filter(c => c.kind === 'sprint-meta');
    expect(metaCards.length).toBe(2);
  });

  it('__backlog__ wird nie verändert', () => {
    const board = {
      id: 'b-5', title: 'T', nextDisplayId: 1,
      columnDefs: [],
      sprints: [
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    applySetKind(board, '__backlog__', 'sprint');
    const backlog = board.sprints.find(s => s.id === '__backlog__');
    expect(backlog.kind).toBe('backlog');
  });

  it('ungültiger kind-Wert wird ignoriert', () => {
    const board = {
      id: 'b-6', title: 'T', nextDisplayId: 1,
      columnDefs: [],
      sprints: [
        { id: 'sprint-3', title: 'Sprint 3', kind: 'sprint', cards: [] },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    applySetKind(board, 'sprint-3', 'foobar');
    const sprint = board.sprints.find(s => s.id === 'sprint-3');
    expect(sprint.kind).toBe('sprint');
  });

  it('lane → sprint: keine doppelten Meta-Karten wenn Start bereits vorhanden', () => {
    const existingStart = Model.createSprintMetaCard('start', 99, 'col-erledigt', true);
    const board = {
      id: 'b-7', title: 'T', nextDisplayId: 100,
      columnDefs: [],
      sprints: [
        { id: 'lane-3', title: 'Lane', kind: 'lane', cards: [existingStart, makeCard()] },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    applySetKind(board, 'lane-3', 'sprint');
    const sprint = board.sprints.find(s => s.id === 'lane-3');
    const startCards = sprint.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === 'start');
    expect(startCards.length).toBe(1); /* kein Duplikat */
  });
})

/* ─────────────────────────────────────────────────────────────────
   #326: Swimlane-DnD — Drag-Handle-Sichtbarkeit pro kind
   ───────────────────────────────────────────────────────────────── */

describe('Renderer — Swimlane-Drag-Handle (#326)', () => {
  it('kind:sprint zeigt .sprint-drag-handle', () => {
    const cid = nextColId();
    const board = {
      id: 'b-dnd-1', title: 'B',
      columnDefs: [{ id: cid, title: 'Todo' }],
      sprints: [
        { id: 's-sprint-326', title: 'S', kind: 'sprint', cards: [] },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    const container = setupBoardDOM(board);
    const sprintLane = container.querySelector('.swimlane[data-sprint-id="s-sprint-326"]');
    expect(sprintLane).toBeTruthy();
    expect(sprintLane.querySelector('.sprint-drag-handle')).toBeTruthy();
  });

  it('kind:lane zeigt .sprint-drag-handle', () => {
    const cid = nextColId();
    const board = {
      id: 'b-dnd-2', title: 'B',
      columnDefs: [{ id: cid, title: 'Todo' }],
      sprints: [
        { id: 's-lane-326', title: 'L', kind: 'lane', cards: [] },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    const container = setupBoardDOM(board);
    const laneEl = container.querySelector('.swimlane[data-sprint-id="s-lane-326"]');
    expect(laneEl.querySelector('.sprint-drag-handle')).toBeTruthy();
  });

  it('__backlog__ hat KEIN .sprint-drag-handle (Backlog ist fixiert)', () => {
    const cid = nextColId();
    const board = {
      id: 'b-dnd-3', title: 'B',
      columnDefs: [{ id: cid, title: 'Todo' }],
      sprints: [
        { id: 's-sprint-blg', title: 'S', kind: 'sprint', cards: [] },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    const container = setupBoardDOM(board);
    /* Backlog wird in der Flat-Ansicht nicht als eigene .swimlane gerendert,
       sondern in Swimlane-Modus existiert es nicht als separate Lane (Renderer-
       Pfad: Sprints sind in Swimlane-Modus, Backlog bleibt im einzigen .swimlane). */
    const backlog = container.querySelector('.swimlane[data-sprint-id="__backlog__"]');
    if (backlog) expect(backlog.querySelector('.sprint-drag-handle')).toBeNull();
    /* Hauptpunkt: der nicht-Backlog-Sprint hat den Handle. */
    expect(container.querySelector('.swimlane[data-sprint-id="s-sprint-blg"] .sprint-drag-handle'))
      .toBeTruthy();
  });

  it('kind:service hat KEIN .sprint-drag-handle (Server-Wahrheit, ADR-0092)', () => {
    const cid = nextColId();
    const board = {
      id: 'b-dnd-4', title: 'B',
      columnDefs: [{ id: cid, title: 'Todo' }],
      sprints: [
        { id: 's-service-326', title: 'Service', kind: 'service', cards: [] },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] },
      ],
    };
    const container = setupBoardDOM(board);
    const serviceLane = container.querySelector('.swimlane[data-sprint-id="s-service-326"]');
    expect(serviceLane).toBeTruthy();
    expect(serviceLane.querySelector('.sprint-drag-handle')).toBeNull();
  });
})
