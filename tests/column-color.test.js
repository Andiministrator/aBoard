/**
 * Tests für Spaltenfarbe (Karte #122)
 * Abdeckung:
 * - Migration: columnDef ohne color-Feld → null nach checkVersion (idempotent)
 * - Migration: bestehende gültige color bleibt erhalten
 * - Migration: ungültiger Typ (nicht String, nicht null) → null normalisiert
 * - State: col:color-Event setzt Farbe korrekt im State
 * - Reset: color: null entfernt den Tint
 */

import { describe, it, expect } from 'vitest'
import { Storage } from '../src/storage.js'
import { State }   from '../src/state.js'
import { EventBus } from '../src/eventbus.js'

/* ── Hilfsfunktionen ──────────────────────────────────────────────────────── */

/**
 * Minimales v2-Board mit konfigurierbaren columnDefs.
 * Verwendung analog zu anderen Storage-Tests (keine DOM-Abhängigkeiten).
 */
function makeV2State(columnDefs = []) {
  return {
    version: '2',
    board: {
      id:            'board-color-test',
      title:         'Farb-Test-Board',
      nextDisplayId: 1,
      columnDefs,
      sprints: [
        {
          id:    '__backlog__',
          title: 'Backlog',
          cards: [],
        },
      ],
    },
  }
}

/* ── Migration ────────────────────────────────────────────────────────────── */

describe('Spaltenfarbe — Migration (migrateColumnColor)', () => {
  it('columnDef ohne color-Feld erhält color: null nach checkVersion', () => {
    const state = makeV2State([{ id: 'col-todo', title: 'Todo' }])
    // color-Feld ist nicht vorhanden
    expect('color' in state.board.columnDefs[0]).toBe(false)

    const result = Storage.checkVersion(state)
    expect(result).not.toBeNull()
    expect(result.board.columnDefs[0].color).toBeNull()
  })

  it('Migration ist idempotent — checkVersion zweimal aufrufen ändert null nicht', () => {
    const state = makeV2State([{ id: 'col-todo', title: 'Todo' }])

    const result1 = Storage.checkVersion(state)
    expect(result1.board.columnDefs[0].color).toBeNull()

    // Zweiter Durchlauf: color ist bereits null — kein Überschreiben
    const result2 = Storage.checkVersion(result1)
    expect(result2.board.columnDefs[0].color).toBeNull()
  })

  it('bestehende gültige color (HEX-String) bleibt nach Migration erhalten', () => {
    const state = makeV2State([{ id: 'col-todo', title: 'Todo', color: '#7c3aed' }])

    const result = Storage.checkVersion(state)
    expect(result.board.columnDefs[0].color).toBe('#7c3aed')
  })

  it('Migration ist idempotent bei vorhandener Farbe — zweiter Aufruf ändert Wert nicht', () => {
    const state = makeV2State([{ id: 'col-todo', title: 'Todo', color: '#2563eb' }])

    const result1 = Storage.checkVersion(state)
    expect(result1.board.columnDefs[0].color).toBe('#2563eb')

    const result2 = Storage.checkVersion(result1)
    expect(result2.board.columnDefs[0].color).toBe('#2563eb')
  })

  it('ungültiger Typ (Zahl) wird auf null normalisiert', () => {
    const state = makeV2State([{ id: 'col-todo', title: 'Todo', color: 42 }])

    const result = Storage.checkVersion(state)
    expect(result.board.columnDefs[0].color).toBeNull()
  })

  it('ungültiger Typ (Boolean) wird auf null normalisiert', () => {
    const state = makeV2State([{ id: 'col-todo', title: 'Todo', color: true }])

    const result = Storage.checkVersion(state)
    expect(result.board.columnDefs[0].color).toBeNull()
  })

  it('ungültiger Typ (Objekt) wird auf null normalisiert', () => {
    const state = makeV2State([{ id: 'col-todo', title: 'Todo', color: { hex: '#abc' } }])

    const result = Storage.checkVersion(state)
    expect(result.board.columnDefs[0].color).toBeNull()
  })

  it('mehrere columnDefs werden alle migriert', () => {
    const state = makeV2State([
      { id: 'col-todo',     title: 'Todo' },
      { id: 'col-in-progress', title: 'In Arbeit', color: '#16a34a' },
      { id: 'col-done',    title: 'Erledigt' },
    ])

    const result = Storage.checkVersion(state)
    expect(result.board.columnDefs[0].color).toBeNull()
    expect(result.board.columnDefs[1].color).toBe('#16a34a')
    expect(result.board.columnDefs[2].color).toBeNull()
  })

  it('columnDefs-loses Board wird nicht abgebrochen (Guard)', () => {
    const state = {
      version: '2',
      board: {
        id:            'board-no-cols',
        title:         'Ohne Spalten',
        nextDisplayId: 1,
        sprints: [{ id: '__backlog__', title: 'Backlog', cards: [] }],
        /* columnDefs fehlt absichtlich */
      },
    }
    // checkVersion darf keinen Fehler werfen — das Board-Objekt hat kein columnDefs
    expect(() => Storage.checkVersion(state)).not.toThrow()
  })
})

/* ── State / Persistenz ──────────────────────────────────────────────────── */

describe('Spaltenfarbe — State und col:color-Event', () => {
  it('State.set propagiert eine neue Farbe im Board', () => {
    const board = {
      id:            'board-state-color',
      title:         'State-Farb-Test',
      nextDisplayId: 1,
      columnDefs:    [{ id: 'col-a', title: 'A', color: null }],
      sprints:       [{ id: '__backlog__', title: 'Backlog', cards: [] }],
    }
    State.set({ board })

    // Farbe im State setzen (analog zu EventBus-Handler in main.js)
    const state = State.get()
    const col = state.board.columnDefs.find(c => c.id === 'col-a')
    col.color = '#dc2626'
    State.set({ ...state })

    const updated = State.get()
    expect(updated.board.columnDefs[0].color).toBe('#dc2626')
    State.markClean()
  })

  it('Reset: color: null entfernt die Farbe aus dem State', () => {
    const board = {
      id:            'board-state-reset',
      title:         'Reset-Test',
      nextDisplayId: 1,
      columnDefs:    [{ id: 'col-b', title: 'B', color: '#ea580c' }],
      sprints:       [{ id: '__backlog__', title: 'Backlog', cards: [] }],
    }
    State.set({ board })

    const state = State.get()
    const col = state.board.columnDefs.find(c => c.id === 'col-b')
    // Reset
    col.color = null
    State.set({ ...state })

    const updated = State.get()
    expect(updated.board.columnDefs[0].color).toBeNull()
    State.markClean()
  })

  it('EventBus überträgt col:color korrekt an Listener', () => {
    let received = null
    function handler(payload) { received = payload }
    EventBus.on('col:color', handler)

    EventBus.emit('col:color', { colId: 'col-x', color: '#0891b2' })

    expect(received).toEqual({ colId: 'col-x', color: '#0891b2' })
    EventBus.off('col:color', handler)
  })

  it('EventBus col:color mit null-Farbe (Reset) korrekt', () => {
    let received = null
    function handler(payload) { received = payload }
    EventBus.on('col:color', handler)

    EventBus.emit('col:color', { colId: 'col-y', color: null })

    expect(received).toEqual({ colId: 'col-y', color: null })
    EventBus.off('col:color', handler)
  })
})

/* ── Defensive: Validierung ungültiger HEX-Eingabe ───────────────────────── */
/*
  Entscheidung: Ungültige HEX-Strings (z. B. "nicht-hex", "red") werden
  **roh gespeichert** — nicht verworfen. Begründung:
  - <input type="color"> liefert immer gültige #rrggbb-Werte (Browser-Garantie).
  - Freie Texteingabe ist nicht vorgesehen (nur color-Input-Nativ).
  - Ein defensives Regexp würde die PALETTE einschränken (z.B. oklch, hsl).
  - Roh-Speichern ist reversibel — der User kann über Reset zurücksetzen.
  Der folgende Test dokumentiert dieses Verhalten.
*/

describe('Spaltenfarbe — Defensive Eingabe-Behandlung', () => {
  it('nicht-HEX-String wird roh gespeichert (kein Verwerfen durch Migration)', () => {
    const state = makeV2State([{ id: 'col-todo', title: 'Todo', color: 'nicht-hex' }])

    const result = Storage.checkVersion(state)
    // "nicht-hex" ist ein gültiger String — migrateColumnColor verwirft nur nicht-Strings
    // Entscheidung: roh speichern, nicht validieren (Browser verhindert invalide Eingaben)
    expect(typeof result.board.columnDefs[0].color).toBe('string')
    expect(result.board.columnDefs[0].color).toBe('nicht-hex')
  })
})
