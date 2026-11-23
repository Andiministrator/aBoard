/* @vitest-environment happy-dom */

/**
 * Tests für das awaitingUser-Feature (Karte #115):
 * - Migration (checkVersion)
 * - Renderer: Banner erscheint / fehlt je nach Flag
 * - Button-Klick clear-t Flag + Tab-Titel
 * - relativeTime-Format (Stichproben)
 *
 * Seit Karte #609 (ADR-0130 §7) ist awaitingUser eine Abbildung je Person:
 * `{ <Person>: { question, since, askedBy } }`. Der Banner zeigt nur den
 * Eintrag der eigenen Person (Config userName, sonst erste Nutzer-Identität,
 * sonst „User"); „Ich habe geantwortet" entfernt nur diesen Schlüssel.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// ── Mocks für renderer.js-Abhängigkeiten ───────────────────────────────────

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName: vi.fn(() => null),
    listKnownBoards:  vi.fn(async () => []),
  },
}))

vi.mock('../src/storage.js', () => {
  // Wir brauchen die echte Storage-Implementierung nur für die Migrations-Tests,
  // daher importieren wir sie dort direkt. Für Renderer-Tests reicht der Stub.
  return {
    Storage: {
      listKnownFiles: vi.fn(async () => []),
    },
  }
})

vi.mock('../src/dragdrop.js', () => ({
  DragDrop: {
    takePendingFlip: vi.fn(() => null),
    init:            vi.fn(),
  },
}))

vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Filter }   from '../src/filter.js'
import { State }    from '../src/state.js'
import { EventBus } from '../src/eventbus.js'
import { Renderer } from '../src/renderer.js'
import { I18n }     from '../src/i18n.js'
import { Config }   from '../src/config.js'

// ── Storage direkt (ohne Mock, für Migrations-Tests) ───────────────────────
// Wir importieren Storage getrennt in einem eigenen describe-Block
// mit vi.importActual.

// ── Hilfsfunktionen ──────────────────────────────────────────────────────────

let _colIdx = 0
function uniqueColId() { return `col-aw-${++_colIdx}` }

/* Session-QA Sprint 78: `identities` — ein lokales Board mit höchstens einer
   Nutzer-Identität gilt als Einzelplatz (jeder Eintrag ist „meiner“). Tests,
   die zwei Personen unterscheiden, geben deshalb zwei Identitäten mit. */
const ZWEI_PERSONEN = [{ kind: 'user', label: 'Bea' }, { kind: 'user', label: 'Andi' }]
function makeBoard(awaitingUser = null, identities = undefined) {
  const cid = uniqueColId()
  const sid = `sprint-aw-${_colIdx}`
  return {
    id:          'board-aw-test',
    title:       'awaitingUser-Test-Board',
    columnDefs:  [{ id: cid, title: 'Backlog' }],
    sprints:     [{ id: sid, title: 'Sprint 1', cards: [] }],
    awaitingUser,
    ...(identities ? { identities } : {}),
  }
}

function setupDOM() {
  const header = document.createElement('div')
  header.id = 'board-header'
  const columns = document.createElement('div')
  columns.id = 'board-columns'
  document.body.appendChild(header)
  document.body.appendChild(columns)
}

function renderWith(awaitingUser, identities) {
  setupDOM()
  Renderer.renderBoard({ board: makeBoard(awaitingUser, identities) })
}

/* Ein Eintrag für „mich“: userName „User“ (im beforeEach gesetzt). Seit der
   Session-QA Sprint 78 gibt es am Server keinen Rückfall mehr auf die erste
   Identität — der eigene Name muss passen; lokal wäre auch ohne ihn jeder
   Eintrag eines Einzelplatz-Boards meiner. So gilt der Test in allen Kontexten. */
const fuerMich = (eintrag) => ({ User: eintrag })

// ── Setup / Teardown ─────────────────────────────────────────────────────────

beforeEach(() => {
  document.body.innerHTML = ''
  document.title = 'aBoard'
  Filter.clear()
  State.markClean()
  Config.set('userName', 'User')
})

afterEach(() => {
  document.body.innerHTML = ''
  document.title = 'aBoard'
  Filter.clear()
  vi.clearAllMocks()
})

// ─────────────────────────────────────────────────────────────────────────────
// 1) Migration — checkVersion
// ─────────────────────────────────────────────────────────────────────────────

describe('Migration — board.awaitingUser', async () => {
  // Echte Storage-Implementierung ohne Mock
  const { Storage } = await vi.importActual('../src/storage.js')

  function minimalV2(awaitingUser) {
    const state = {
      version: '2',
      board: {
        id:            'board-mig',
        title:         'Mig-Board',
        nextDisplayId: 1,
        columnDefs:    [],
        sprints: [{ id: '__backlog__', title: 'Backlog', cards: [] }],
      },
    }
    if (awaitingUser !== 'OMIT') state.board.awaitingUser = awaitingUser
    return state
  }

  it('fehlendes awaitingUser-Feld → null', () => {
    const result = Storage.checkVersion(minimalV2('OMIT'))
    expect(result.board.awaitingUser).toBeNull()
  })

  it('null bleibt null', () => {
    const result = Storage.checkVersion(minimalV2(null))
    expect(result.board.awaitingUser).toBeNull()
  })

  it('alte Einzelform wird zum Eintrag der Standardperson (ADR-0130 §7)', () => {
    const au = { question: 'Test-Frage?', since: '2026-05-13T10:00:00.000Z', askedBy: 'Claude Sonnet 4.6' }
    const result = Storage.checkVersion(minimalV2(au))
    expect(result.board.awaitingUser).toEqual({ User: au })
  })

  it('alte Einzelform → Eintrag der ersten Nutzer-Identität (der bisherige Board-Owner)', () => {
    const au = { question: 'Test-Frage?', since: '2026-05-13T10:00:00.000Z', askedBy: 'Claude' }
    const state = minimalV2(au)
    state.board.identities = [
      { id: 'i1', kind: 'claude-opus', label: 'Claude Opus' },
      { id: 'i2', kind: 'user', label: 'Andi', email: '' },
    ]
    const result = Storage.checkVersion(state)
    expect(result.board.awaitingUser).toEqual({ Andi: au })
  })

  it('Abbildung bleibt erhalten, ungültige Einträge fallen weg, leere Abbildung → null', () => {
    const gut = { question: 'F?', since: '2026-05-13T10:00:00.000Z', askedBy: 'Claude' }
    const r1 = Storage.checkVersion(minimalV2({ Andi: gut, Bea: { question: '', since: 's', askedBy: 'x' } }))
    expect(r1.board.awaitingUser).toEqual({ Andi: gut })
    const r2 = Storage.checkVersion(minimalV2({ Bea: { question: '' } }))
    expect(r2.board.awaitingUser).toBeNull()
  })

  it('Migration ist idempotent', () => {
    const au = { question: 'Test-Frage?', since: '2026-05-13T10:00:00.000Z', askedBy: 'Claude' }
    const einmal = Storage.checkVersion(minimalV2(au))
    const zweimal = Storage.checkVersion(JSON.parse(JSON.stringify(einmal)))
    expect(zweimal.board.awaitingUser).toEqual(einmal.board.awaitingUser)
  })

  it('Prototyp-Schlüssel als Personenname werden verworfen', () => {
    const gut = { question: 'F?', since: '2026-05-13T10:00:00.000Z', askedBy: 'Claude' }
    const roh = JSON.parse('{"__proto__": ' + JSON.stringify(gut) + ', "Andi": ' + JSON.stringify(gut) + '}')
    const result = Storage.checkVersion(minimalV2(roh))
    expect(Object.keys(result.board.awaitingUser)).toEqual(['Andi'])
  })

  it('ungültiges Objekt (leerer question-String) → null', () => {
    const au = { question: '', since: '2026-05-13T10:00:00.000Z', askedBy: 'Claude' }
    const result = Storage.checkVersion(minimalV2(au))
    expect(result.board.awaitingUser).toBeNull()
  })

  it('ungültiges Objekt (fehlende since) → null', () => {
    const au = { question: 'Irgendwas?', askedBy: 'Claude' }
    const result = Storage.checkVersion(minimalV2(au))
    expect(result.board.awaitingUser).toBeNull()
  })

  it('Nicht-Objekt (String statt Objekt) → null', () => {
    const result = Storage.checkVersion(minimalV2('ungültig'))
    expect(result.board.awaitingUser).toBeNull()
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 2) Renderer — Banner erscheint / fehlt
// ─────────────────────────────────────────────────────────────────────────────

describe('Renderer — awaitingUser-Banner', () => {
  it('kein Banner wenn awaitingUser null', () => {
    renderWith(null)
    expect(document.getElementById('awaiting-user-banner')).toBeNull()
  })

  it('Banner erscheint wenn awaitingUser gesetzt', () => {
    renderWith(fuerMich({
      question: 'Soll ich A oder B verwenden?',
      since:    new Date().toISOString(),
      askedBy:  'Claude Sonnet 4.6',
    }))
    const banner = document.getElementById('awaiting-user-banner')
    expect(banner).not.toBeNull()
    /* F-2876: kein role=status am frisch erzeugten Banner; Ansage über die Live-Region */
    expect(banner.getAttribute('role')).toBeNull()
    const region = document.body.querySelector(':scope > .visually-hidden[role="status"]')
    expect(region.textContent.length).toBeGreaterThan(0)
  })

  it('Banner enthält Frage-Snippet', () => {
    renderWith(fuerMich({
      question: 'Soll ich Feature X einbauen?',
      since:    new Date().toISOString(),
      askedBy:  'Claude Sonnet 4.6',
    }))
    const banner = document.getElementById('awaiting-user-banner')
    expect(banner.textContent).toContain('Feature X')
  })

  it('Banner enthält „Ich habe geantwortet"-Button', () => {
    renderWith(fuerMich({
      question: 'Soll Variante A oder B?',
      since:    new Date().toISOString(),
      askedBy:  'Claude',
    }))
    const btn = document.querySelector('.awaiting-banner-btn')
    expect(btn).not.toBeNull()
    expect(btn.textContent).toBe(I18n.t('awaiting.cta'))
  })

  it('Tab-Titel erhält ⚠️-Prefix wenn awaitingUser gesetzt', () => {
    document.title = 'aBoard'
    renderWith(fuerMich({
      question: 'Frage?',
      since:    new Date().toISOString(),
      askedBy:  'Claude',
    }))
    expect(document.title).toMatch(/^⚠️/)
  })

  it('Tab-Titel enthält keinen ⚠️-Prefix wenn awaitingUser null', () => {
    document.title = 'aBoard'
    renderWith(null)
    expect(document.title).not.toMatch(/^⚠️/)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 3) Button-Klick clear-t Flag und Tab-Titel
// ─────────────────────────────────────────────────────────────────────────────

describe('Renderer — Banner-Button clear-t Flag', () => {
  it('Button-Klick setzt awaitingUser = null und emittiert state:changed', () => {
    const au = fuerMich({
      question: 'Antwort erbeten?',
      since:    new Date().toISOString(),
      askedBy:  'Claude',
    })
    const board = makeBoard(au)
    setupDOM()
    const state = { board }
    State.set(state)
    Renderer.renderBoard(state)

    const spy = vi.fn()
    EventBus.on('state:changed', spy)

    const btn = document.querySelector('.awaiting-banner-btn')
    expect(btn).not.toBeNull()
    btn.click()

    expect(state.board.awaitingUser).toBeNull()
    expect(spy).toHaveBeenCalled()

    EventBus.off('state:changed', spy)
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 4) relativeTime — Stichproben (via Renderer-Modul-interne Funktion)
//    Da relativeTime nicht exportiert wird, testen wir es indirekt
//    über den Banner-Text.
// ─────────────────────────────────────────────────────────────────────────────

describe('relativeTime — Stichproben via Banner', () => {
  it('gerade eben (< 2 Minuten)', () => {
    renderWith(fuerMich({
      question: 'Frage?',
      since:    new Date().toISOString(), // jetzt
      askedBy:  'Claude',
    }))
    const banner = document.getElementById('awaiting-user-banner')
    expect(banner.textContent).toContain('gerade eben')
  })

  it('vor N min (5 Minuten)', () => {
    const since = new Date(Date.now() - 5 * 60_000).toISOString()
    renderWith(fuerMich({ question: 'Frage?', since, askedBy: 'Claude' }))
    const banner = document.getElementById('awaiting-user-banner')
    expect(banner.textContent).toContain('vor 5 min')
  })

  it('vor N Tagen (2 Tage)', () => {
    const since = new Date(Date.now() - 2 * 24 * 60 * 60_000).toISOString()
    renderWith(fuerMich({ question: 'Frage?', since, askedBy: 'Claude' }))
    const banner = document.getElementById('awaiting-user-banner')
    expect(banner.textContent).toContain('vor 2 Tag')
  })
})

// ─────────────────────────────────────────────────────────────────────────────
// 5) Je Person (Karte #609, ADR-0130 §7)
// ─────────────────────────────────────────────────────────────────────────────

describe('Renderer — awaitingUser je Person (ADR-0130 §7)', async () => {
  const { Config } = await import('../src/config.js')
  const eintrag = (frage) => ({ question: frage, since: new Date().toISOString(), askedBy: 'Claude' })

  afterEach(() => { Config.set('userName', '') })

  it('Eintrag NUR einer anderen Person → kein Banner, kein Titel-Prefix', () => {
    document.title = 'aBoard'
    renderWith({ Bea: eintrag('Frage an Bea?') }, ZWEI_PERSONEN)
    expect(document.getElementById('awaiting-user-banner')).toBeNull()
    expect(document.title).not.toMatch(/^⚠️/)
  })

  it('zwei Einträge → der Banner zeigt nur den eigenen (userName)', () => {
    Config.set('userName', 'Andi')
    renderWith({ Bea: eintrag('Frage an Bea?'), Andi: eintrag('Frage an Andi?') }, ZWEI_PERSONEN)
    const text = document.getElementById('awaiting-user-banner').textContent
    expect(text).toContain('Frage an Andi?')
    expect(text).not.toContain('Frage an Bea?')
  })

  it('die Zuordnung ignoriert Gross-/Kleinschreibung und Randleerraum', () => {
    Config.set('userName', '  andi ')
    renderWith({ Andi: eintrag('Frage an Andi?') })
    expect(document.getElementById('awaiting-user-banner')).not.toBeNull()
  })

  it('„Ich habe geantwortet" entfernt NUR den eigenen Schlüssel', () => {
    Config.set('userName', 'Andi')
    const bea = eintrag('Frage an Bea?')
    const board = makeBoard({ Bea: bea, Andi: eintrag('Frage an Andi?') }, ZWEI_PERSONEN)
    setupDOM()
    const state = { board }
    State.set(state)
    Renderer.renderBoard(state)
    document.querySelector('.awaiting-banner-btn').click()
    expect(state.board.awaitingUser).toEqual({ Bea: bea })
  })

  it('der letzte Eintrag weg → awaitingUser wieder null (nicht {})', () => {
    Config.set('userName', 'Andi')
    const board = makeBoard({ Andi: eintrag('Frage?') })
    setupDOM()
    const state = { board }
    State.set(state)
    Renderer.renderBoard(state)
    document.querySelector('.awaiting-banner-btn').click()
    expect(state.board.awaitingUser).toBeNull()
  })
})

// F-1336 (Sprint-81-Kritiker A-1): mehrere Chats derselben Person — der Banner
// zeigt die JÜNGSTE Frage, nennt die Zahl der übrigen und frischt beides auf.
describe('Renderer — mehrere Chats derselben Person (F-1336)', () => {
  const K1 = 'Andi · s1', K2 = 'Andi · s2'
  const e = (frage, since, session) => ({ question: frage, since, askedBy: 'Claude', person: 'Andi', session })
  afterEach(() => { Config.set('userName', '') })

  it('zwei eigene Sitzungs-Einträge → jüngste Frage plus „+1 weiterer Chat“', () => {
    Config.set('userName', 'Andi')
    renderWith({
      [K1]: e('Ältere Frage?', '2026-10-03T10:00:00.000Z', 's1'),
      [K2]: e('Jüngere Frage?', '2026-10-03T11:00:00.000Z', 's2'),
      Bea: { question: 'Frage an Bea?', since: '2026-10-03T12:00:00.000Z', askedBy: 'Claude' },
    }, ZWEI_PERSONEN)
    const text = document.getElementById('awaiting-user-banner').textContent
    expect(text).toContain('Jüngere Frage?')
    expect(text).not.toContain('Ältere Frage?')
    expect(text).not.toContain('Frage an Bea?')
    expect(document.querySelector('.awaiting-banner-weitere').textContent).toContain('+1')
  })

  it('der angezeigte Chat antwortet → der Banner zeigt die Frage des anderen (kein alter Text)', () => {
    Config.set('userName', 'Andi')
    setupDOM()
    const zwei = { [K1]: e('Frage aus Chat 1?', '2026-10-03T10:00:00.000Z', 's1'), [K2]: e('Frage aus Chat 2?', '2026-10-03T11:00:00.000Z', 's2') }
    Renderer.renderBoard({ board: makeBoard(zwei, ZWEI_PERSONEN) })
    expect(document.getElementById('awaiting-user-banner').textContent).toContain('Frage aus Chat 2?')
    Renderer.renderBoard({ board: makeBoard({ [K1]: zwei[K1] }, ZWEI_PERSONEN) })
    const text = document.getElementById('awaiting-user-banner').textContent
    expect(text).toContain('Frage aus Chat 1?')
    expect(text).not.toContain('Frage aus Chat 2?')
    expect(document.querySelector('.awaiting-banner-weitere').textContent).toBe('')
  })

  it('„Ich habe geantwortet“ löscht alle eigenen Sitzungs-Einträge, fremde bleiben', () => {
    Config.set('userName', 'Andi')
    const bea = { question: 'B?', since: '2026-10-03T12:00:00.000Z', askedBy: 'Claude' }
    const state = { board: makeBoard({ [K1]: e('a?', '2026-10-03T10:00:00.000Z', 's1'), [K2]: e('b?', '2026-10-03T11:00:00.000Z', 's2'), Bea: bea }, ZWEI_PERSONEN) }
    setupDOM(); State.set(state); Renderer.renderBoard(state)
    document.querySelector('.awaiting-banner-btn').click()
    expect(state.board.awaitingUser).toEqual({ Bea: bea })
  })
})

// Verify Sprint 78 (V-1): Am Server zählt die Anmelde-Identität — userName ist
// dort nicht gesetzt und nicht einstellbar. Nur im Server-Build (DCE sonst).
import { IS_SERVER_BUILD } from './helpers/build-flags.js'
import { Profile } from '../src/profile.js'
import { StoragePlugin } from '../src/storage-plugin.js'
describe.skipIf(!IS_SERVER_BUILD)('Renderer — Server-Modus: Anmelde-Identität als Person (V-1)', () => {
  const eintrag = (frage) => ({ question: frage, since: new Date().toISOString(), askedBy: 'Claude' })
  afterEach(() => { vi.restoreAllMocks() })

  it('ohne userName: der Eintrag der angemeldeten Person erscheint, der fremde nicht', async () => {
    Config.set('userName', '')
    /* W-1: höchstens ein Identitäts-Versuch je Minute — frühere Tests dieser
       Datei können ihn schon verbraucht haben (Modulzustand). */
    const jetzt = Date.now() + 120_000
    vi.spyOn(Date, 'now').mockReturnValue(jetzt)
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    const holt = vi.fn(async () => ({ name: 'Anna', email: 'anna@firma.de' }))
    vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue({ getCurrentUser: holt })
    const board = makeBoard({ Bea: eintrag('Frage an Bea?'), 'anna@firma.de': eintrag('Frage an Anna?') },
      [{ kind: 'user', label: 'User' }])
    setupDOM()
    State.set({ board })
    Renderer.renderBoard({ board })
    await new Promise(r => setTimeout(r, 0))
    const banner = document.getElementById('awaiting-user-banner')
    expect(banner?.textContent).toContain('Frage an Anna?')
    expect(banner?.textContent).not.toContain('Frage an Bea?')
    /* W-3: auch der Anmelde-NAME zählt (Eintrag unter „Anna“). */
    const board2 = makeBoard({ Anna: eintrag('Frage unter dem Namen?') }, [{ kind: 'user', label: 'User' }, { kind: 'user', label: 'Bea' }])
    document.body.innerHTML = ''
    setupDOM()
    State.set({ board: board2 })
    Renderer.renderBoard({ board: board2 })
    expect(document.getElementById('awaiting-user-banner')?.textContent).toContain('Frage unter dem Namen?')
  })
})
