/* @vitest-environment happy-dom */

/**
 * Tests für die App-Sidebar (Karte #176, #207)
 *
 * Abdeckung:
 *  - init mountet Backdrop + Sidebar in document.body (idempotent)
 *  - open() setzt --open-Klassen + aria-hidden=false, schließt Backdrop fade
 *  - close() entfernt Klassen + setzt aria-hidden=true
 *  - Backdrop-Klick schließt
 *  - Esc-Taste schließt
 *  - EventBus 'sidebar:open' / 'sidebar:close' triggern open/close
 *  - File-Sektion enthält Öffnen + Export-Buttons
 *  - Settings-Sektion enthält Settings-Button
 *  Karte #207 — Server-Modus-Filterung:
 *  - Im Server-Modus wird die File-Sektion ausgeblendet
 *  - Im Solo-/Standalone-Modus bleibt die File-Sektion erhalten (ADR-0059-Invariante)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName:   vi.fn(() => null),
    getDirectoryHandle: vi.fn(() => null), /* Karte #274: kein FSA-Handle im Test → kein Doc-Abschnitt */
  },
}))

vi.mock('../src/storage.js', () => ({
  Storage: {
    listKnownFiles: vi.fn(async () => []),
  },
}))

/* F-1441: steuerbar, damit der Fehlerfall (F-1440: listKnownBoards wirft) testbar ist. */
let listKnownBoardsImpl = async () => []
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      listKnownBoards: (...a) => listKnownBoardsImpl(...a),
      aclList: vi.fn(async () => []),
    }),
  },
}))

import { EventBus } from '../src/eventbus.js'
import { State }    from '../src/state.js'
import { Config }   from '../src/config.js'
import { Sidebar }  from '../src/sidebar.js'

/* F-272 / ADR-0064: Im Server-Build-Kontext (test:server) ist boardMode immer 'server'.
   Config.set('boardMode', 'solo') im beforeEach clampt auf 'server' — die Sidebar zeigt
   Server-Modus-Verhalten (keine File-Sektion). Tests die Solo-/claude-code-File-Sektionen
   als ADR-0059-Invariante prüfen, laufen nur im Standalone-Kontext (test:standalone). */
const isServerBuildCtx = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true')

beforeEach(() => {
  document.body.innerHTML = ''
  /* Sidebar wird in body gemountet — Test-Reset */
  State.set({ board: { id: 'b1', title: 'Test-Board' } })
  /* Default: Solo-Modus (kein Server) — ADR-0059-Invariante */
  Config.set('boardMode', 'solo')
})

afterEach(() => {
  Sidebar.close()
  vi.clearAllMocks()
})

describe('Sidebar — Init + Mount', () => {
  it('init() mountet Backdrop und Sidebar in document.body', () => {
    Sidebar.init()
    expect(document.querySelector('.app-sidebar-backdrop')).toBeTruthy()
    expect(document.querySelector('.app-sidebar')).toBeTruthy()
  })

  it('init() ist idempotent — zweiter Aufruf erzeugt kein Duplikat', () => {
    Sidebar.init()
    Sidebar.init()
    expect(document.querySelectorAll('.app-sidebar').length).toBe(1)
    expect(document.querySelectorAll('.app-sidebar-backdrop').length).toBe(1)
  })

  it('Sidebar startet im geschlossenen Zustand (aria-hidden="true")', () => {
    Sidebar.init()
    const sb = document.querySelector('.app-sidebar')
    expect(sb.getAttribute('aria-hidden')).toBe('true')
    expect(sb.classList.contains('app-sidebar--open')).toBe(false)
  })
})

describe('Sidebar — Open / Close', () => {
  it('open() setzt aria-hidden=false und (im nächsten Frame) --open-Klasse', async () => {
    await Sidebar.open()
    const sb = document.querySelector('.app-sidebar')
    expect(sb.getAttribute('aria-hidden')).toBe('false')
    /* Spring-Animation setzt die Klasse via requestAnimationFrame */
    await new Promise(r => requestAnimationFrame(r))
    expect(sb.classList.contains('app-sidebar--open')).toBe(true)
  })

  it('open() öffnet auch den Backdrop', async () => {
    await Sidebar.open()
    const bd = document.querySelector('.app-sidebar-backdrop')
    expect(bd.classList.contains('app-sidebar-backdrop--open')).toBe(true)
  })

  it('close() entfernt --open-Klasse und setzt aria-hidden=true', async () => {
    await Sidebar.open()
    Sidebar.close()
    const sb = document.querySelector('.app-sidebar')
    expect(sb.classList.contains('app-sidebar--open')).toBe(false)
    expect(sb.getAttribute('aria-hidden')).toBe('true')
  })

  it('Backdrop-Klick schließt die Sidebar', async () => {
    await Sidebar.open()
    const bd = document.querySelector('.app-sidebar-backdrop')
    bd.click()
    const sb = document.querySelector('.app-sidebar')
    expect(sb.classList.contains('app-sidebar--open')).toBe(false)
  })

  it('Esc-Taste schließt die Sidebar', async () => {
    await Sidebar.open()
    const esc = new KeyboardEvent('keydown', { key: 'Escape' })
    document.dispatchEvent(esc)
    const sb = document.querySelector('.app-sidebar')
    expect(sb.classList.contains('app-sidebar--open')).toBe(false)
  })
})

describe('Sidebar — EventBus-Integration', () => {
  it('EventBus.emit("sidebar:open") öffnet', async () => {
    Sidebar.init()
    EventBus.emit('sidebar:open')
    /* open() ist async — kurz warten. Server-Build hat zusätzliche async
       Lookups (#306-Admin-Check, #307-Toggle-Check via fetch), daher mehr
       Microtasks als ein einzelner setTimeout(0) abdeckt. */
    await new Promise(r => setTimeout(r, 50))
    const sb = document.querySelector('.app-sidebar')
    expect(sb.getAttribute('aria-hidden')).toBe('false')
  })

  it('EventBus.emit("sidebar:close") schließt', async () => {
    await Sidebar.open()
    EventBus.emit('sidebar:close')
    const sb = document.querySelector('.app-sidebar')
    expect(sb.classList.contains('app-sidebar--open')).toBe(false)
  })
})

describe('Sidebar — Inhalt', () => {
  it('hat einen Schließen-Button im Header', async () => {
    await Sidebar.open()
    const close = document.querySelector('.app-sidebar .btn-modal-close')
    expect(close).toBeTruthy()
  })

  /* F-272 / #298: Im Server-Build-Test-Kontext clampt boardMode auf 'server'
     → File-Section verschwindet (ADR-0063), Share + LLM-Keys-Section
     (#298/ADR-0094, server-mode + IS_SERVER_BUILD) erscheinen zusätzlich.
     Der Solo-Sektions-Count gilt strikt nur im Standalone-Kontext. */
  it.skipIf(isServerBuildCtx)('hat drei Sektionen mit Aktions-Rows (File/Ansichten/Settings) im Solo-Modus', async () => {
    await Sidebar.open()
    const sections = document.querySelectorAll('.app-sidebar-section')
    /* #308/ADR-0103: Kalender-Section ("Ansichten") ist nicht gegated → +1 in allen Modi. */
    expect(sections.length).toBe(3)
    /* Mindestens drei Action-Rows: Open + Export + Settings. */
    const rows = document.querySelectorAll('.app-sidebar-row')
    expect(rows.length).toBeGreaterThanOrEqual(3)
  })

  it('Klick auf den Benutzer-Einstellungen-Button triggert config:open-user + schließt Sidebar (ADR-0065)', async () => {
    await Sidebar.open()
    let userConfigOpened = false
    EventBus.on('config:open-user', () => { userConfigOpened = true })
    /* ADR-0065: Settings-Sektion hat jetzt zwei Buttons (Benutzer + Board).
       Erster Button = Benutzer-Einstellungen (config:open-user). */
    const sections = [...document.querySelectorAll('.app-sidebar-section')]
    const settingsSection = sections[sections.length - 1]
    const buttons = settingsSection.querySelectorAll('.app-sidebar-row')
    expect(buttons.length).toBe(2) /* Zwei Menüpunkte */
    buttons[0].click() /* Erster = Benutzer-Einstellungen */
    expect(userConfigOpened).toBe(true)
    const sb = document.querySelector('.app-sidebar')
    expect(sb.classList.contains('app-sidebar--open')).toBe(false)
  })

  it('Klick auf den Board-Einstellungen-Button triggert config:open-board + schließt Sidebar (ADR-0065)', async () => {
    await Sidebar.open()
    let boardConfigOpened = false
    EventBus.on('config:open-board', () => { boardConfigOpened = true })
    /* ADR-0065: Settings-Sektion — zweiter Button = Board-Einstellungen (config:open-board). */
    const sections = [...document.querySelectorAll('.app-sidebar-section')]
    const settingsSection = sections[sections.length - 1]
    const buttons = settingsSection.querySelectorAll('.app-sidebar-row')
    buttons[1].click() /* Zweiter = Board-Einstellungen */
    expect(boardConfigOpened).toBe(true)
    const sb = document.querySelector('.app-sidebar')
    expect(sb.classList.contains('app-sidebar--open')).toBe(false)
  })
})

/* ── Karte #347: Build-Version im Sidebar-Fuss ─────────────────────────── */

describe('Sidebar — Versions-Fussnote (#347)', () => {
  it('zeigt die Build-Version in allen Modi als letztes Element', async () => {
    await Sidebar.open()
    const zeile = document.querySelector('.app-sidebar .app-sidebar-version')
    expect(zeile, 'Versions-Zeile fehlt').toBeTruthy()
    expect(zeile.textContent.trim()).not.toBe('')
    /* Fussnote, nicht Menüpunkt: muss NACH der Einstellungen-Sektion kommen. */
    const sidebar = document.querySelector('.app-sidebar')
    expect(sidebar.lastElementChild).toBe(zeile)
  })

  it('trägt den vollen Versionsstring als title (Diagnose per Hover)', async () => {
    await Sidebar.open()
    const zeile = document.querySelector('.app-sidebar .app-sidebar-version')
    expect(zeile.title).toContain(zeile.textContent.trim())
  })

  it('ist keine Aktions-Row — kein Klick-Handler, keine Row-Klasse', async () => {
    /* Sonst zählt sie in den Sektions-/Row-Tests mit und wandert versehentlich
       in die Tastatur-Navigation. */
    await Sidebar.open()
    const zeile = document.querySelector('.app-sidebar .app-sidebar-version')
    expect(zeile.classList.contains('app-sidebar-row')).toBe(false)
    expect(zeile.closest('.app-sidebar-section')).toBeNull()
    expect(zeile.tagName).toBe('DIV')
  })

  it('wird bei erneutem Öffnen nicht doppelt angehängt', async () => {
    await Sidebar.open()
    Sidebar.close()
    await Sidebar.open()
    expect(document.querySelectorAll('.app-sidebar-version').length).toBe(1)
  })
})

/* ── Karte #207: Server-Modus-Filterung (ADR-0059-Invariante) ───────────── */

describe('Sidebar — Server-Modus-Filterung (Karte #207)', () => {
  /* Standalone-only (F-272): Im Server-Kontext ist boardMode='server' (via clamping im
     beforeEach), die Sidebar zeigt Server-Modus-Verhalten ohne File-Sektion. */
  it.skipIf(isServerBuildCtx)('Solo-Modus: File-Sektion ist sichtbar (ADR-0059-Invariante)', async () => {
    /* Solo ist der Default — kein Überschreiben nötig */
    await Sidebar.open()
    /* Im Solo-Modus: File + Ansichten (Kalender #308) + Settings = 3 Sektionen (Boards-Sektion seit #704 im Titel-Menü) */
    const sections = document.querySelectorAll('.app-sidebar-section')
    expect(sections.length).toBe(3)
    /* File-Sektion enthält den Öffnen-Button */
    const allButtons = [...document.querySelectorAll('.app-sidebar-row')]
    const openBtn = allButtons.find(b => b.textContent.includes('Öffnen') || b.textContent.includes('Open'))
    expect(openBtn).toBeTruthy()
  })

  /* Standalone-only (F-272): Config.set('boardMode','claude-code') clampt im Server-Kontext
     auf 'server' — keine claude-code-Sidebar-Invariante testbar (F-272). */
  it.skipIf(isServerBuildCtx)('Claude-Code-Modus: File-Sektion ist sichtbar (ADR-0059-Invariante)', async () => {
    Config.set('boardMode', 'claude-code')
    await Sidebar.open()
    /* Im Claude-Code-Modus: File + Ansichten (Kalender #308) + Settings = 3 (kein restStorage) */
    const sections = document.querySelectorAll('.app-sidebar-section')
    expect(sections.length).toBe(3)
    /* Öffnen-Button muss vorhanden sein */
    const allButtons = [...document.querySelectorAll('.app-sidebar-row')]
    const openBtn = allButtons.find(b => b.textContent.includes('Öffnen') || b.textContent.includes('Open'))
    expect(openBtn).toBeTruthy()
  })

  it('Server-Modus (via Profile-Spy): File-Sektion wird ausgeblendet', async () => {
    /* Da Config.set('boardMode', 'server') im Standalone-Test-Build nicht möglich ist
       (F-186 → fällt auf solo zurück), wird Profile.has direkt via vi.spyOn überschrieben. */
    const { Profile } = await import('../src/profile.js')
    const spy = vi.spyOn(Profile, 'has').mockImplementation((cap, override) => {
      if (cap === 'feature.restStorage') return true
      /* Andere Capabilities originalgetreu behalten */
      return override
        ? ['feature.polling', 'feature.agentContext', 'feature.ownerClaude', 'feature.complexityEstimate'].includes(cap)
        : false
    })
    try {
      await Sidebar.open()
      /* Kein Öffnen-Button im Server-Modus — File-Sektion ist ausgeblendet */
      const allButtons = [...document.querySelectorAll('.app-sidebar-row')]
      const openBtn = allButtons.find(b => b.textContent.includes('Öffnen') || b.textContent.includes('Open'))
      expect(openBtn).toBeFalsy()
    } finally {
      spy.mockRestore()
    }
  })

  it('Server-Modus (via Profile-Spy): Settings-Sektion bleibt erhalten', async () => {
    const { Profile } = await import('../src/profile.js')
    const spy = vi.spyOn(Profile, 'has').mockImplementation((cap) => {
      if (cap === 'feature.restStorage') return true
      return false
    })
    try {
      await Sidebar.open()
      /* Settings-Button (für den Config-Dialog) muss vorhanden bleiben */
      const allButtons = [...document.querySelectorAll('.app-sidebar-row')]
      const settingsBtn = allButtons.find(b =>
        b.textContent.includes('Einstellungen') || b.textContent.includes('Settings'))
      expect(settingsBtn).toBeTruthy()
    } finally {
      spy.mockRestore()
    }
  })
})

/* F-1441/#624: Der Eintrag „Alle Boards anzeigen…“ lebt seit Karte #704 im Board-Menü am
   Titel (tests/board-menu-704.test.js); die Sidebar führt keine Boards-Sektion mehr. */
