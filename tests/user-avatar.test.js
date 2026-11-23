/* @vitest-environment happy-dom */
/**
 * Tests für Karte #201: User-Avatar im #board-header + geteilte-Boards in der Sidebar
 *
 * Abdeckung:
 *  - getCurrentUser() im Standalone/Project-Plugin: immer null-Felder
 *  - getCurrentUser() im REST-Plugin: liefert Name/Email vom Server
 *  - getCurrentUser() im REST-Plugin: null-Felder bei Netzwerkfehler
 *  - listKnownBoards() im REST-Plugin: role-Feld wird durchgereicht
 *  - Header-Avatar: Forward-Auth AN → Initialen-Kreis mit title-Tooltip, KEIN Namenstext
 *  - Header-Avatar: Legacy-Bypass (kein User) → kein Avatar im Header
 *  - Sidebar Board-Sektionen: owner vs. editor/viewer (geteilte-Boards-Split bleibt)
 *  - F-223: board:loaded-Event triggert resetSwimlaneState
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

/* Karte #328 / ADR-0064: Header-Avatar in renderer.js ist hinter IS_SERVER_BUILD
   gegated, damit der slim-Build via DCE entfällt. Tests die ein gerendertes
   Avatar-Element erwarten, müssen daher im Nicht-Server-Test-Kontext
   übersprungen werden (Verhalten dort: kein Avatar — was die negativen Tests
   weiterhin korrekt prüfen). */
const isServerBuildCtx = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true')

/* ── Mocks für Renderer- + Sidebar-Tests ────────────────────────── */

vi.mock('../src/sync.js', () => ({
  Sync: { getDirectoryName: vi.fn(() => null) },
}))

vi.mock('../src/storage.js', () => ({
  Storage: { listKnownFiles: vi.fn(async () => []) },
}))

vi.mock('../src/dragdrop.js', () => ({
  DragDrop: {
    takePendingFlip: vi.fn(() => null),
    init:            vi.fn(),
  },
}))

/* Vite ?raw-Import — kein Vite in Vitest */
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

/* Plugin-Mock — wird pro Test konfiguriert */
const _mockPlugin = {
  listKnownBoards: vi.fn(async () => []),
  getCurrentUser:  vi.fn(async () => ({ name: null, email: null })),
}

vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => _mockPlugin },
  __testing__:   {
    createStandalonePlugin: () => ({ getCurrentUser: async () => ({ name: null, email: null }), kind: 'standalone' }),
    createProjectPlugin:    () => ({ getCurrentUser: async () => ({ name: null, email: null }), kind: 'project' }),
    createRESTPlugin:       () => ({ getCurrentUser: async () => ({ name: null, email: null }), kind: 'rest' }),
  },
}))

let _profileServerMode = false
vi.mock('../src/profile.js', () => ({
  Profile: {
    has: vi.fn((feat) => _profileServerMode && feat === 'feature.restStorage'),
  },
}))

import { EventBus } from '../src/eventbus.js'
import { State }    from '../src/state.js'
import { Filter }   from '../src/filter.js'
import { Renderer } from '../src/renderer.js'
import { Sidebar }  from '../src/sidebar.js'
import { Profile }  from '../src/profile.js'

/* ── Zähler für eindeutige Board-IDs (verhindert Singleton-Kollisionen) */
let _testCounter = 0
function nextId() { return `t${++_testCounter}` }

/** Minimales Board für renderBoard() */
function makeMinimalBoard() {
  const sid = `sprint-${nextId()}`
  const cid = `col-${nextId()}`
  return {
    id:         'board-test',
    title:      'Test-Board',
    columnDefs: [{ id: cid, title: 'Backlog' }],
    sprints:    [{ id: sid, title: 'Sprint 1', cards: [] }],
  }
}

/** DOM-Anker für renderBoard aufbauen */
function setupBoardDOM() {
  const header = document.createElement('div')
  header.id = 'board-header'
  const container = document.createElement('div')
  container.id = 'board-columns'
  document.body.appendChild(header)
  document.body.appendChild(container)
}

/** Wartet auf ausstehende Mikrotask-Promises (fire-and-forget async flush) */
function flushAsync() {
  return new Promise(r => setTimeout(r, 0))
}

/* ── getCurrentUser()-Vertrag ─────────────────────────────────── */

describe('Plugin-Vertrag: getCurrentUser()', () => {
  it('Standalone-Plugin: getCurrentUser() liefert { name: null, email: null }', async () => {
    const mockStandalone = { getCurrentUser: async () => ({ name: null, email: null }) }
    const user = await mockStandalone.getCurrentUser()
    expect(user).toEqual({ name: null, email: null })
  })

  it('REST-Plugin getCurrentUser(): liefert User-Daten vom /me-Endpoint', async () => {
    const realFetch = global.fetch
    let calledUrl = ''
    global.fetch = async (url) => {
      calledUrl = url
      return {
        ok:   true,
        json: async () => ({ name: 'Ada Lovelace', email: 'ada@example.com' }),
      }
    }
    async function getCurrentUser() {
      try {
        const res = await fetch('http://localhost:8765/me')
        if (!res.ok) return { name: null, email: null }
        return await res.json()
      } catch { return { name: null, email: null }; }
    }
    const user = await getCurrentUser()
    expect(user).toEqual({ name: 'Ada Lovelace', email: 'ada@example.com' })
    expect(calledUrl).toContain('/me')
    global.fetch = realFetch
  })

  it('REST-Plugin getCurrentUser(): null-Felder bei Netzwerkfehler', async () => {
    const realFetch = global.fetch
    global.fetch = async () => { throw new Error('Netzwerkfehler') }
    async function getCurrentUser() {
      try {
        const res = await fetch('http://localhost:8765/me')
        if (!res.ok) return { name: null, email: null }
        return await res.json()
      } catch { return { name: null, email: null }; }
    }
    const user = await getCurrentUser()
    expect(user).toEqual({ name: null, email: null })
    global.fetch = realFetch
  })
})

/* ── listKnownBoards() mit role-Feld ──────────────────────────── */

describe('REST-Plugin: listKnownBoards() mit Rollen', () => {
  it('role-Feld wird durchgereicht wenn Server es liefert', async () => {
    const mockItems = [
      { id: 'b1', title: 'Mein Board',    updated_at: 1, role: 'owner' },
      { id: 'b2', title: 'Geteiltes',     updated_at: 2, role: 'viewer' },
    ]
    const realFetch = global.fetch
    global.fetch = async () => ({ ok: true, json: async () => mockItems })
    async function listKnownBoards() {
      const res = await fetch('http://localhost:8765/boards')
      if (!res.ok) return []
      const items = await res.json()
      return items.map(b => ({ id: b.id, name: b.title, title: b.title, role: b.role }))
    }
    const boards = await listKnownBoards()
    expect(boards[0]).toMatchObject({ id: 'b1', name: 'Mein Board', role: 'owner' })
    expect(boards[1]).toMatchObject({ id: 'b2', name: 'Geteiltes',  role: 'viewer' })
    global.fetch = realFetch
  })

  it('role=undefined wenn Server kein role liefert (Legacy-Bypass)', async () => {
    const realFetch = global.fetch
    global.fetch = async () => ({
      ok: true,
      json: async () => [{ id: 'b1', title: 'Board', updated_at: 1 }],
    })
    async function listKnownBoards() {
      const res = await fetch('http://localhost:8765/boards')
      if (!res.ok) return []
      const items = await res.json()
      return items.map(b => ({ id: b.id, name: b.title, title: b.title, role: b.role }))
    }
    const boards = await listKnownBoards()
    expect(boards[0].role).toBeUndefined()
    global.fetch = realFetch
  })
})

/* ── Header-Avatar (Karte #201) ────────────────────────────────── */

describe('Header-Avatar im #board-header (Karte #201)', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    Filter.clear()
    State.markClean?.()
    vi.clearAllMocks()
    _mockPlugin.listKnownBoards = vi.fn(async () => [])
    _mockPlugin.getCurrentUser  = vi.fn(async () => ({ name: null, email: null }))
    _profileServerMode = false
    setupBoardDOM()
  })

  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('Legacy-Bypass (getCurrentUser null) → kein Avatar im Header', async () => {
    _profileServerMode = true
    Profile.has.mockImplementation((feat) => feat === 'feature.restStorage')
    _mockPlugin.getCurrentUser = vi.fn(async () => ({ name: null, email: null }))

    Renderer.renderBoard({ board: makeMinimalBoard() })
    await flushAsync()

    const avatar = document.querySelector('.board-header-avatar')
    expect(avatar).toBeNull()
  })

  it.skipIf(!isServerBuildCtx)('Forward-Auth AN: Avatar erscheint im #board-header mit Initialen', async () => {
    _profileServerMode = true
    Profile.has.mockImplementation((feat) => feat === 'feature.restStorage')
    _mockPlugin.getCurrentUser = vi.fn(async () => ({ name: 'Ada Lovelace', email: 'ada@example.com' }))

    Renderer.renderBoard({ board: makeMinimalBoard() })
    await flushAsync()

    const avatar = document.querySelector('#board-header .board-header-avatar')
    expect(avatar).toBeTruthy()
    /* Initiale des Namens */
    expect(avatar.textContent.trim()).toBe('A')
    /* Name als Hover-Tooltip — kein sichtbarer Namens-Text im DOM */
    expect(avatar.title).toContain('Ada Lovelace')
    /* KEIN sichtbarer Name/E-Mail-Text als eigenes DOM-Element */
    expect(document.querySelector('.sidebar-user-name')).toBeNull()
    expect(document.querySelector('.sidebar-user-email')).toBeNull()
  })

  it.skipIf(!isServerBuildCtx)('Header-Avatar zeigt Initiale des Namens (Bob → B)', async () => {
    _profileServerMode = true
    Profile.has.mockImplementation((feat) => feat === 'feature.restStorage')
    _mockPlugin.getCurrentUser = vi.fn(async () => ({ name: 'Bob Müller', email: 'bob@example.com' }))

    Renderer.renderBoard({ board: makeMinimalBoard() })
    await flushAsync()

    const avatar = document.querySelector('.board-header-avatar')
    expect(avatar).toBeTruthy()
    expect(avatar.textContent.trim()).toBe('B')
  })

  it('Standalone-Modus → kein Avatar im Header (kein Server)', async () => {
    _profileServerMode = false
    Profile.has.mockImplementation(() => false)

    Renderer.renderBoard({ board: makeMinimalBoard() })
    await flushAsync()

    const avatar = document.querySelector('.board-header-avatar')
    expect(avatar).toBeNull()
  })

  it.skipIf(!isServerBuildCtx)('title-Attribut enthält den Namen (Hover-Tooltip)', async () => {
    _profileServerMode = true
    Profile.has.mockImplementation((feat) => feat === 'feature.restStorage')
    _mockPlugin.getCurrentUser = vi.fn(async () => ({ name: 'Andi P.', email: 'andi@example.com' }))

    Renderer.renderBoard({ board: makeMinimalBoard() })
    await flushAsync()

    const avatar = document.querySelector('.board-header-avatar')
    expect(avatar?.title).toBe('Andi P.')
  })

  it.skipIf(!isServerBuildCtx)('Kein sichtbarer Namenstext im Header-DOM bei identifiziertem User', async () => {
    _profileServerMode = true
    Profile.has.mockImplementation((feat) => feat === 'feature.restStorage')
    _mockPlugin.getCurrentUser = vi.fn(async () => ({ name: 'Erika Muster', email: 'erika@example.com' }))

    Renderer.renderBoard({ board: makeMinimalBoard() })
    await flushAsync()

    /* Avatar selbst darf nur die Initiale enthalten — kein ausgeschriebener Name */
    const avatar = document.querySelector('.board-header-avatar')
    expect(avatar).toBeTruthy()
    /* Kein DOM-Knoten mit dem vollen Namen als textContent */
    const allText = [...document.querySelectorAll('#board-header *')]
      .map(n => n.childNodes)
      .flat()
      .filter(n => n.nodeType === Node.TEXT_NODE)
      .map(n => n.textContent.trim())
      .filter(Boolean)
    expect(allText.some(t => t === 'Erika Muster')).toBe(false)
    expect(allText.some(t => t === 'erika@example.com')).toBe(false)
  })
})

/* ── Sidebar: Board-Sektionen (Karte #201) ──────────────────────────
   Die Boards-Sektion der Seitenleiste ist mit Karte #704 entfallen; ihre Tests
   (owner vs. geteilt, aktives Board gefiltert) stehen in tests/board-menu-704.test.js.
   Hier bleibt die User-Sektions-Prüfung. */

describe('Sidebar — keine User-Sektion (Karte #201)', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    State.set({ board: { id: 'b-aktiv', title: 'Aktives Board' } })
    vi.clearAllMocks()
    _mockPlugin.listKnownBoards = vi.fn(async () => [])
    _mockPlugin.getCurrentUser  = vi.fn(async () => ({ name: null, email: null }))
    _profileServerMode = true
    Profile.has.mockImplementation((feat) => feat === 'feature.restStorage')
  })

  afterEach(() => {
    Sidebar.close()
  })

  it('Keine sidebar-user-row mehr in der Sidebar (User-Sektion entfernt)', async () => {
    _mockPlugin.getCurrentUser = vi.fn(async () => ({ name: 'Ada Lovelace', email: 'ada@example.com' }))
    await Sidebar.open()
    /* Sidebar-User-Sektion (Avatar+Name+E-Mail) darf nicht mehr existieren */
    expect(document.querySelector('.sidebar-user-row')).toBeNull()
    expect(document.querySelector('.sidebar-user-name')).toBeNull()
    expect(document.querySelector('.sidebar-user-email')).toBeNull()
  })
})

/* ── F-223: board:loaded-Event ──────────────────────────────────── */

describe('F-223: board:loaded-Event (Karte #201)', () => {
  it('board:loaded-Event feuert ohne Fehler', () => {
    let fired = false
    EventBus.on('board:loaded', () => { fired = true })
    EventBus.emit('board:loaded')
    expect(fired).toBe(true)
  })
})
