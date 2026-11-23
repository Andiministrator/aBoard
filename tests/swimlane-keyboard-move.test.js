/* @vitest-environment happy-dom */

/**
 * F-388 / Karte #344: Das Swimlane-Drag-Handle war ein Knopf, der nichts tat.
 *
 * `role="button"` + `aria-label` + `tabindex="0"` waren gesetzt, ein
 * Keydown-Handler nicht. Wer mit der Tastatur navigiert, landete darauf, der
 * Screenreader meldete „Schaltfläche", und keine Taste löste etwas aus —
 * exakt das F-337-Muster aus Sprint 39 (Chat-Tail-Footer).
 *
 * Geprüft wird hier nicht nur „Taste bewegt die Swimlane", sondern auch, was
 * den Unterschied zwischen funktionierend und BEDIENBAR macht:
 *   - der Fokus bleibt nach dem Re-Render auf dem Handle (sonst ist nach
 *     einem Tastendruck Schluss)
 *   - an den Rändern gibt es eine Meldung statt Stille (ein stummer
 *     Tastendruck ist nicht von einem kaputten Knopf zu unterscheiden)
 *   - `__backlog__` bekommt gar kein Handle
 *   - andere Tasten lösen kein preventDefault aus (sonst wäre das
 *     Seiten-Scrollen global blockiert)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

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
vi.mock('../src/dragdrop.js', () => ({
  DragDrop: {
    takePendingFlip: vi.fn(() => null),
    init: vi.fn(),
    snapshotSwimlanesForKeyboardMove: vi.fn(),
  },
}))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Filter }   from '../src/filter.js'
import { State }    from '../src/state.js'
import { Renderer } from '../src/renderer.js'
import { EventBus } from '../src/eventbus.js'
import { DragDrop } from '../src/dragdrop.js'

// ── Hilfsmittel ──────────────────────────────────────────────────────────

function lane(id, title, kind = 'sprint') {
  return { id, title, kind, cards: [] }
}

/* Board mit drei verschiebbaren Lanes plus Backlog (Model-Invariante: Backlog
   steht in board.sprints am Ende). */
function board() {
  return {
    version: '2',
    board: {
      id: 'b1', title: 'T', nextDisplayId: 1,
      columnDefs: [{ id: 'col-ready', title: 'Ready' }],
      sprints: [
        lane('s-a', 'Lane A'),
        lane('s-b', 'Lane B'),
        lane('s-c', 'Lane C'),
        lane('__backlog__', 'Backlog', 'backlog'),
      ],
    },
  }
}

function domVorbereiten() {
  document.body.innerHTML = ''
  const header = document.createElement('div'); header.id = 'board-header'
  const cols = document.createElement('div'); cols.id = 'board-columns'
  document.body.appendChild(header); document.body.appendChild(cols)
}

function rendern() {
  Renderer.renderBoard(State.get())
}

function handleVon(sprintId) {
  return document.querySelector(`.swimlane[data-sprint-id="${sprintId}"] .sprint-drag-handle`)
}

function laneReihenfolge() {
  return [...document.querySelectorAll('.swimlane')]
    .map(l => l.dataset.sprintId)
    .filter(id => id && id !== '__backlog__')
}

function taste(el, key) {
  const ev = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true })
  el.dispatchEvent(ev)
  return ev
}

function meldung() {
  return document.getElementById('swimlane-move-live')?.textContent || ''
}

/* Der Reorder-Handler sitzt in main.js, das hier nicht geladen wird — also
   dieselbe Mutation nachbauen und danach neu rendern, wie die App es tut.
   Die Mutation ist Zeile für Zeile die aus `main.js` (`swimlane:moved`).

   WICHTIG, beim ersten Anlauf hereingefallen: `EventBus.on` gibt KEINE
   Abmeldefunktion zurück (die API ist `on`/`off`/`emit`). Wer sich auf einen
   Rückgabewert verlässt, registriert pro Test einen weiteren Handler — dann
   verschiebt der dritte Test dreimal statt einmal, und der Fokus-Test fällt
   um, weil der zweite Render `_fokusSwimlaneHandle` schon verbraucht
   vorfindet. Deshalb hier eine benannte Funktion und explizites `off`. */
function reorderHandler({ sprintId, targetIndex }) {
  const sprints = State.get().board.sprints
  const idx = sprints.findIndex(s => s.id === sprintId)
  if (idx === -1) return
  const [sp] = sprints.splice(idx, 1)
  sprints.splice(targetIndex, 0, sp)
  rendern()
}

beforeEach(() => {
  domVorbereiten()
  Filter.clear()
  State.set(board())
  EventBus.on('swimlane:moved', reorderHandler)
  rendern()
})

afterEach(() => {
  EventBus.off('swimlane:moved', reorderHandler)
  document.body.innerHTML = ''
  Filter.clear()
  vi.clearAllMocks()
})

describe('Swimlane per Tastatur verschieben (F-388, #344)', () => {
  it('ArrowDown verschiebt die Swimlane eine Position nach unten', () => {
    expect(laneReihenfolge()).toEqual(['s-a', 's-b', 's-c'])
    taste(handleVon('s-a'), 'ArrowDown')
    expect(laneReihenfolge()).toEqual(['s-b', 's-a', 's-c'])
  })

  it('ArrowUp verschiebt die Swimlane eine Position nach oben', () => {
    taste(handleVon('s-c'), 'ArrowUp')
    expect(laneReihenfolge()).toEqual(['s-a', 's-c', 's-b'])
  })

  it('der Fokus bleibt nach dem Verschieben auf dem Handle', () => {
    /* Ohne das wäre nach EINEM Tastendruck Schluss: der Re-Render ersetzt den
       DOM-Knoten, der Fokus fiele auf <body>, und der Nutzer müsste sich
       zurück-tabben. Das ist der Unterschied zwischen „Taste funktioniert"
       und „bedienbar". */
    const h = handleVon('s-a')
    h.focus()
    taste(h, 'ArrowDown')

    const neu = handleVon('s-a')
    expect(neu).not.toBe(h)                       // wirklich ein neuer Knoten
    expect(document.activeElement).toBe(neu)
  })

  it('zwei Tastendrücke hintereinander wirken beide', () => {
    /* Der eigentliche Beweis für Bedienbarkeit. */
    const h = handleVon('s-a')
    h.focus()
    taste(document.activeElement, 'ArrowDown')
    taste(document.activeElement, 'ArrowDown')
    expect(laneReihenfolge()).toEqual(['s-b', 's-c', 's-a'])
  })

  it('am oberen Rand: keine Bewegung, aber eine Meldung', () => {
    taste(handleVon('s-a'), 'ArrowUp')
    expect(laneReihenfolge()).toEqual(['s-a', 's-b', 's-c'])
    expect(meldung()).toContain('Lane A')
    expect(meldung()).not.toBe('')
  })

  it('am unteren Rand: keine Bewegung, aber eine Meldung', () => {
    taste(handleVon('s-c'), 'ArrowDown')
    expect(laneReihenfolge()).toEqual(['s-a', 's-b', 's-c'])
    expect(meldung()).toContain('Lane C')
  })

  it('die Meldung nennt Titel, alte und neue Position', () => {
    taste(handleVon('s-a'), 'ArrowDown')
    const m = meldung()
    expect(m).toContain('Lane A')
    expect(m).toContain('1')      // von Position 1 …
    expect(m).toContain('2')      // … nach Position 2
  })

  it('die Live-Region ist polite und atomar', () => {
    /* assertive würde den Screenreader mitten im Satz unterbrechen. */
    taste(handleVon('s-a'), 'ArrowDown')
    const live = document.getElementById('swimlane-move-live')
    expect(live.getAttribute('aria-live')).toBe('polite')
    expect(live.getAttribute('aria-atomic')).toBe('true')
  })

  it('das Handle verweist per aria-describedby auf einen existierenden Hinweis', () => {
    /* Ein describedby auf eine fehlende ID liest der Screenreader still als
       „keine Beschreibung" — der Hinweis wäre weg, ohne dass es auffällt. */
    const h = handleVon('s-a')
    const id = h.getAttribute('aria-describedby')
    expect(id).toBeTruthy()
    const hint = document.getElementById(id)
    expect(hint).toBeTruthy()
    expect(hint.textContent.trim().length).toBeGreaterThan(0)
  })

  it('andere Tasten werden nicht abgefangen (Seiten-Scrollen bleibt möglich)', () => {
    const ev = taste(handleVon('s-a'), 'PageDown')
    expect(ev.defaultPrevented).toBe(false)
    /* Die Pfeiltasten dagegen schon — sonst scrollte die Seite mit. */
    const ev2 = taste(handleVon('s-a'), 'ArrowDown')
    expect(ev2.defaultPrevented).toBe(true)
  })

  it('die Backlog-Swimlane hat kein Drag-Handle', () => {
    expect(handleVon('__backlog__')).toBeNull()
  })

  it('der FLIP-Snapshot wird angefordert, damit die Umsortierung nicht springt', () => {
    taste(handleVon('s-a'), 'ArrowDown')
    expect(DragDrop.snapshotSwimlanesForKeyboardMove).toHaveBeenCalled()
  })
})
