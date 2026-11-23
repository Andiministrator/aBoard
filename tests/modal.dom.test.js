/* @vitest-environment happy-dom */

/**
 * DOM-Tests für modal.js — Karte #92
 * Abdeckung: Tab-System, Maximize-Key-Logik (F-108), aria-Verkettung (F-109),
 * Agent-Tab-Sichtbarkeit, Comment-Badge (Karte #95)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

/*
 * Hinweis (Stand Sprint 61, F-756): modal.js nutzte für den Einblend-Effekt
 * (overlay.classList.add('modal-overlay--open')) ein requestAnimationFrame.
 * Der Rückstand daraus — ein rAF, das nach resetModal() auf ein zerstörtes
 * Overlay zugriff — ist mit F-756 verschwunden: die Klasse wird jetzt synchron
 * nach einem erzwungenen Reflow gesetzt (Muster aus modal-shell.js/F-754),
 * weil der Initialfokus sonst garantiert VOR dem rAF lief und ins Leere ging.
 * vi.useFakeTimers() bleibt trotzdem stehen — andere Pfade dieser Datei
 * (Debounce, Toasts) hängen daran.
 */

// ── Mocks für externe Abhängigkeiten ─────────────────────────────────────

vi.mock('../src/swipe.js', () => ({
  addSwipeToDismiss: vi.fn(),
}))

// Config-Mock: boardMode kontrollierbar über boardModeValue
let boardModeValue = 'standalone'
vi.mock('../src/config.js', () => ({
  Config: {
    get: vi.fn((key) => {
      if (key === 'boardMode') return boardModeValue
      if (key === 'userName') return 'TestUser'
      return ''
    }),
    set: vi.fn(),
  },
}))

import { EventBus }   from '../src/eventbus.js'
import { Modal }      from '../src/modal.js'
import { State }      from '../src/state.js'
import { createModal } from '../src/modal-shell.js'
import { addSwipeToDismiss } from '../src/swipe.js'

// ── Hilfsfunktionen ───────────────────────────────────────────────────────

function makeCard(overrides = {}) {
  return {
    id:           'card-test-001',
    displayId:    42,
    title:        'Testkarte',
    description:  '',
    agentContext: '',
    comments:     [],
    attachments:  [],
    links:        [],
    activity:     [],
    priority:     'mittel',
    labels:       [],
    owner:        '',
    complexity:   '',
    effort:       null,
    qa:           false,
    critical:     false,
    blocked:      false,
    archived:     false,
    columnId:     'col-backlog',
    ...overrides,
  }
}

/** Modal öffnen — gibt das .modal-overlay zurück */
function openModal(card) {
  EventBus.emit('card:edit', card)
  return document.querySelector('.modal-overlay')
}

/** Modal-Singleton-Overlay entfernen und neu aufbauen (zwischen Tests) */
function resetModal() {
  // config:language-changed-Event triggert overlay.remove() + overlay = null im Modal-Modul
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
}

// ── Setup / Teardown ─────────────────────────────────────────────────────

beforeEach(() => {
  boardModeValue = 'standalone'
  vi.useFakeTimers()
  resetModal()
})

afterEach(() => {
  resetModal()
  vi.useRealTimers()
})

// ─────────────────────────────────────────────────────────────────────────
// 1) Tab-System — Struktur
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Tab-System Struktur', () => {
  it('enthält 6 Tab-Buttons: #tab-desc, #tab-comments, #tab-attachments, #tab-agent, #tab-links, #tab-history (ADR-0032, ADR-0033, ADR-0037)', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    expect(overlay.querySelector('#tab-desc')).toBeTruthy()
    expect(overlay.querySelector('#tab-comments')).toBeTruthy()
    expect(overlay.querySelector('#tab-attachments')).toBeTruthy()
    expect(overlay.querySelector('#tab-agent')).toBeTruthy()
    expect(overlay.querySelector('#tab-links')).toBeTruthy()
    expect(overlay.querySelector('#tab-history')).toBeTruthy()
  })

  it('alle Tab-Buttons haben role="tab"', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    const tabs = overlay.querySelectorAll('.tab-btn')
    /* #299/ADR-0095: KI-Tab ist nur im Server-Build vorhanden (IS_SERVER_BUILD-gated). */
    const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
      (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true')
    expect(tabs.length).toBe(IS_SERVER_BUILD ? 8 : 7) /* inkl. Finding-Tab (ADR-0133 §1, nur bei kind:finding sichtbar) */
    tabs.forEach(btn => {
      expect(btn.getAttribute('role')).toBe('tab')
    })
  })

  it('aria-controls von Tab-Buttons zeigt auf existierende Panel-IDs (F-109)', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    const expectedPairs = [
      ['#tab-desc',        '#tab-panel-desc'],
      ['#tab-comments',    '#tab-panel-comments'],
      ['#tab-attachments', '#tab-panel-attachments'],
      ['#tab-agent',       '#tab-panel-agent'],
    ]
    expectedPairs.forEach(([btnSel, panelSel]) => {
      const btn   = overlay.querySelector(btnSel)
      const panel = overlay.querySelector(panelSel)
      expect(btn,   `${btnSel} nicht gefunden`).toBeTruthy()
      expect(panel, `${panelSel} nicht gefunden`).toBeTruthy()
      expect(btn.getAttribute('aria-controls')).toBe(panel.id)
    })
  })

  it('Tab-Panels haben role="tabpanel" und aria-labelledby auf den entsprechenden Tab', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    const panels = overlay.querySelectorAll('.tab-panel')
    panels.forEach(panel => {
      expect(panel.getAttribute('role')).toBe('tabpanel')
      const labelId = panel.getAttribute('aria-labelledby')
      expect(labelId, `Panel ${panel.id} hat kein aria-labelledby`).toBeTruthy()
      expect(
        overlay.querySelector(`#${labelId}`),
        `Element #${labelId} existiert nicht`
      ).toBeTruthy()
    })
  })

  it('Tab-Bar hat role="tablist"', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    const tabBar = overlay.querySelector('.modal-tab-bar')
    expect(tabBar).toBeTruthy()
    expect(tabBar.getAttribute('role')).toBe('tablist')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 2) aria-Verkettung — Modal-Titel (F-109)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — aria-labelledby Verkettung (F-109)', () => {
  it('Modal hat aria-labelledby="modal-title"', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    const modal = overlay.querySelector('.modal')
    expect(modal.getAttribute('aria-labelledby')).toBe('modal-title')
  })

  it('Element mit id="modal-title" existiert im Modal', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    const titleEl = overlay.querySelector('#modal-title')
    expect(titleEl).toBeTruthy()
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 3) Maximize-Key-Logik (F-108)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Maximize-Key-Logik (F-108)', () => {
  afterEach(() => {
    /* F-923/Karte #489: GELECKTER ZUSTAND — der private `maximizedPaneKey`
       (Closure-Variable in src/modal.js) und `document.body.classList`
       ('maximized-pane-active', F-173) sind Modul- bzw. DOM-Zustand, den die
       Tests "Maximize-Button im desc-Tab …", "… im agent-Tab …" und
       "Desc-Maximize und Agent-Maximize sind unabhängig" (oben) setzen, ohne
       am Testende zu maximieren zurückzunehmen oder Modal.close() zu rufen.
       Das äussere `resetModal()` (Datei-weites afterEach, entfernt nur das
       Overlay via `config:language-changed`) kennt `maximizedPaneKey` nicht
       und räumt die Body-Klasse nicht ab — beides bleibt für den nächsten
       Test stehen. Unter sortierter Ausführung tarnt das die zwei
       close()-Tests direkt danach ("close() resettet Maximize-State …"),
       weil deren eigener Modal.close()-Aufruf zufällig sauber macht, bevor
       die beiden F-173-Tests unten ihre Anfangs-Annahme "body ist sauber"
       prüfen. Unter Shuffle stimmt diese Nachbarschaft nicht mehr.
       Reset: NUR Modal.close() kennt `maximizedPaneKey` und kann ihn korrekt
       (inkl. Body-Klasse und Icon-Zustand) auf null zurücksetzen — deshalb
       hier aufrufen, solange das Overlay noch existiert (vor dem äusseren
       resetModal(), das es entfernt: innere afterEach-Hooks laufen vor
       äusseren). */
    if (document.querySelector('.modal-overlay')) Modal.close()
  })

  it('Maximize-Button im desc-Tab setzt Desc-Section auf maximized', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    const maxBtn = overlay.querySelector('#tab-panel-desc .btn-maximize')
    expect(maxBtn, 'Maximize-Button im desc-Panel nicht gefunden').toBeTruthy()
    maxBtn.click()
    const descSection = overlay.querySelector('#tab-panel-desc .modal-desc-section')
    expect(descSection.classList.contains('desc-maximized')).toBe(true)
  })

  it('Maximize-Button im agent-Tab setzt Agent-Section auf maximized (NICHT Desc)', () => {
    boardModeValue = 'project'
    const overlay = openModal(makeCard({ owner: 'Claude Sonnet' }))
    expect(overlay).toBeTruthy()

    // Zum Agent-Tab wechseln
    overlay.querySelector('#tab-agent').click()

    const maxBtnAgent = overlay.querySelector('#tab-panel-agent .btn-maximize')
    expect(maxBtnAgent, 'Maximize-Button im agent-Panel nicht gefunden').toBeTruthy()
    maxBtnAgent.click()

    const agentSection = overlay.querySelector('#tab-panel-agent .modal-desc-section')
    const descSection  = overlay.querySelector('#tab-panel-desc .modal-desc-section')
    expect(agentSection.classList.contains('desc-maximized')).toBe(true)
    expect(descSection.classList.contains('desc-maximized')).toBe(false)
  })

  it('Desc-Maximize und Agent-Maximize sind unabhängig — kein Cross-De-Maximieren', () => {
    boardModeValue = 'project'
    const overlay = openModal(makeCard({ owner: 'Claude Sonnet' }))
    expect(overlay).toBeTruthy()

    // Desc maximieren
    const maxBtnDesc = overlay.querySelector('#tab-panel-desc .btn-maximize')
    maxBtnDesc.click()
    const descSection = overlay.querySelector('#tab-panel-desc .modal-desc-section')
    expect(descSection.classList.contains('desc-maximized')).toBe(true)

    // Agent-Tab öffnen — switchTab() resettet Maximize-State (F-108: korrektes Verhalten)
    overlay.querySelector('#tab-agent').click()

    // Agent maximieren
    const maxBtnAgent = overlay.querySelector('#tab-panel-agent .btn-maximize')
    maxBtnAgent.click()
    const agentSection = overlay.querySelector('#tab-panel-agent .modal-desc-section')
    expect(agentSection.classList.contains('desc-maximized')).toBe(true)

    // Desc-Section: nach Tab-Wechsel zurückgesetzt (F-108: Tab-Wechsel resettet Maximize)
    expect(descSection.classList.contains('desc-maximized')).toBe(false)
  })

  it('close() resettet Maximize-State korrekt für Desc-Pane', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    const maxBtn = overlay.querySelector('#tab-panel-desc .btn-maximize')
    maxBtn.click()
    const descSection = overlay.querySelector('#tab-panel-desc .modal-desc-section')
    expect(descSection.classList.contains('desc-maximized')).toBe(true)
    Modal.close()
    expect(descSection.classList.contains('desc-maximized')).toBe(false)
  })

  it('close() resettet Maximize-State korrekt für Agent-Pane (F-108)', () => {
    boardModeValue = 'project'
    const overlay = openModal(makeCard({ owner: 'Claude Sonnet' }))
    expect(overlay).toBeTruthy()

    overlay.querySelector('#tab-agent').click()
    const maxBtnAgent = overlay.querySelector('#tab-panel-agent .btn-maximize')
    maxBtnAgent.click()

    const agentSection = overlay.querySelector('#tab-panel-agent .modal-desc-section')
    expect(agentSection.classList.contains('desc-maximized')).toBe(true)

    Modal.close()
    expect(agentSection.classList.contains('desc-maximized')).toBe(false)
    const descSection = overlay.querySelector('#tab-panel-desc .modal-desc-section')
    expect(descSection.classList.contains('desc-maximized')).toBe(false)
  })

  it('F-173: setMaximized setzt/entfernt body.maximized-pane-active (Transform-Reset für position:fixed)', () => {
    expect(document.body.classList.contains('maximized-pane-active')).toBe(false)
    const overlay = openModal(makeCard())
    const maxBtn = overlay.querySelector('#tab-panel-desc .btn-maximize')
    maxBtn.click()
    expect(document.body.classList.contains('maximized-pane-active')).toBe(true)
    maxBtn.click()
    expect(document.body.classList.contains('maximized-pane-active')).toBe(false)
  })

  it('F-173: close() während Maximize entfernt body.maximized-pane-active', () => {
    const overlay = openModal(makeCard())
    const maxBtn = overlay.querySelector('#tab-panel-desc .btn-maximize')
    maxBtn.click()
    expect(document.body.classList.contains('maximized-pane-active')).toBe(true)
    Modal.close()
    expect(document.body.classList.contains('maximized-pane-active')).toBe(false)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 4) Agent-Tab-Sichtbarkeit
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Agent-Tab-Sichtbarkeit', () => {
  it('Agent-Tab ist ausgeblendet im standalone-Modus (unabhängig vom Owner)', () => {
    boardModeValue = 'standalone'
    const overlay = openModal(makeCard({ owner: 'Claude Sonnet' }))
    expect(overlay).toBeTruthy()
    const agentTab = overlay.querySelector('#tab-agent')
    expect(agentTab.style.display).toBe('none')
  })

  it('Agent-Tab ist ausgeblendet im project-Modus ohne Claude-Owner', () => {
    boardModeValue = 'project'
    const overlay = openModal(makeCard({ owner: 'TestUser' }))
    expect(overlay).toBeTruthy()
    const agentTab = overlay.querySelector('#tab-agent')
    expect(agentTab.style.display).toBe('none')
  })

  it('Agent-Tab ist sichtbar bei boardMode=project + Claude-Owner', () => {
    boardModeValue = 'project'
    const overlay = openModal(makeCard({ owner: 'Claude Sonnet' }))
    expect(overlay).toBeTruthy()
    const agentTab = overlay.querySelector('#tab-agent')
    expect(agentTab.style.display).not.toBe('none')
  })

  it('Agent-Tab ist sichtbar bei boardMode=project + generischem "Claude"-Owner', () => {
    boardModeValue = 'project'
    const overlay = openModal(makeCard({ owner: 'Claude' }))
    expect(overlay).toBeTruthy()
    const agentTab = overlay.querySelector('#tab-agent')
    expect(agentTab.style.display).not.toBe('none')
  })

  it('Agent-Tab ist sichtbar bei boardMode=project + "Claude Opus"', () => {
    boardModeValue = 'project'
    const overlay = openModal(makeCard({ owner: 'Claude Opus' }))
    expect(overlay).toBeTruthy()
    const agentTab = overlay.querySelector('#tab-agent')
    expect(agentTab.style.display).not.toBe('none')
  })

  it('Agent-Tab ist ausgeblendet bei leerem Owner im project-Modus', () => {
    boardModeValue = 'project'
    const overlay = openModal(makeCard({ owner: '' }))
    expect(overlay).toBeTruthy()
    const agentTab = overlay.querySelector('#tab-agent')
    expect(agentTab.style.display).toBe('none')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 5) Comment-Badge (Karte #95)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Comment-Badge (Karte #95)', () => {
  it('kein Badge bei 0 Kommentaren', () => {
    const overlay = openModal(makeCard({ comments: [] }))
    expect(overlay).toBeTruthy()
    const tabBtn = overlay.querySelector('#tab-comments')
    const badge  = tabBtn.querySelector('.tab-badge')
    expect(badge).toBeNull()
  })

  it('Badge vorhanden bei >= 1 Kommentaren', () => {
    const comments = [{ id: 'c1', author: 'Test', text: 'Hallo', created: '2026-05-13' }]
    const overlay = openModal(makeCard({ comments }))
    expect(overlay).toBeTruthy()
    const tabBtn = overlay.querySelector('#tab-comments')
    const badge  = tabBtn.querySelector('.tab-badge')
    expect(badge).toBeTruthy()
    expect(badge.textContent).toBe('1')
  })

  it('Badge zeigt korrekte Anzahl bei mehreren Kommentaren', () => {
    const comments = [
      { id: 'c1', author: 'A', text: 'Eins', created: '2026-05-13' },
      { id: 'c2', author: 'B', text: 'Zwei', created: '2026-05-13' },
      { id: 'c3', author: 'C', text: 'Drei', created: '2026-05-13' },
    ]
    const overlay = openModal(makeCard({ comments }))
    expect(overlay).toBeTruthy()
    const tabBtn = overlay.querySelector('#tab-comments')
    const badge  = tabBtn.querySelector('.tab-badge')
    expect(badge).toBeTruthy()
    expect(badge.textContent).toBe('3')
  })

  it('Badge hat aria-hidden="true"', () => {
    const comments = [{ id: 'c1', author: 'A', text: 'X', created: '2026-05-13' }]
    const overlay = openModal(makeCard({ comments }))
    expect(overlay).toBeTruthy()
    const tabBtn = overlay.querySelector('#tab-comments')
    const badge  = tabBtn.querySelector('.tab-badge')
    expect(badge.getAttribute('aria-hidden')).toBe('true')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 5a) Comment-Markdown-Rendering (Karte #172)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Comment-Markdown-Rendering (Karte #172)', () => {
  it('Kommentar mit **bold** rendert ein <strong>-Element', () => {
    const comments = [{ id: 'c1', author: 'A', text: 'Das ist **wichtig**.',
                        created: '2026-05-14' }]
    const overlay = openModal(makeCard({ comments }))
    const list = overlay.querySelector('#tab-panel-comments')
    expect(list).toBeTruthy()
    const text = list.querySelector('.comment-text')
    expect(text).toBeTruthy()
    expect(text.querySelector('strong')).toBeTruthy()
    expect(text.querySelector('strong').textContent).toBe('wichtig')
  })

  it('Kommentar mit `code` rendert ein <code>-Element', () => {
    const comments = [{ id: 'c1', author: 'A', text: 'Siehe `foo()`.',
                        created: '2026-05-14' }]
    const overlay = openModal(makeCard({ comments }))
    const text = overlay.querySelector('.comment-text')
    expect(text.querySelector('code')).toBeTruthy()
  })

  it('Plain-Text-Kommentar bleibt sichtbar (kein Markdown nötig)', () => {
    const comments = [{ id: 'c1', author: 'A', text: 'Einfacher Text',
                        created: '2026-05-14' }]
    const overlay = openModal(makeCard({ comments }))
    const text = overlay.querySelector('.comment-text')
    expect(text.textContent).toContain('Einfacher Text')
  })

  it('Leerer Kommentar-Text → .comment-text bleibt leer (kein Crash)', () => {
    const comments = [{ id: 'c1', author: 'A', text: '', created: '2026-05-14' }]
    const overlay = openModal(makeCard({ comments }))
    const text = overlay.querySelector('.comment-text')
    expect(text).toBeTruthy()
    expect(text.children.length).toBe(0)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 5b) Attachment-/Link-Badges (Karte #97)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Attachment-Badge (Karte #97)', () => {
  it('kein Badge bei 0 Anhängen', () => {
    const overlay = openModal(makeCard({ attachments: [] }))
    expect(overlay).toBeTruthy()
    const tabBtn = overlay.querySelector('#tab-attachments')
    expect(tabBtn.querySelector('.tab-badge')).toBeNull()
  })

  it('Badge zeigt korrekte Anzahl bei mehreren Anhängen', () => {
    const attachments = [
      { id: 'a1', filename: 'a.png', mime: 'image/png', size: 100, created: '2026-05-13' },
      { id: 'a2', filename: 'b.pdf', mime: 'application/pdf', size: 200, created: '2026-05-13' },
    ]
    const overlay = openModal(makeCard({ attachments }))
    expect(overlay).toBeTruthy()
    const tabBtn = overlay.querySelector('#tab-attachments')
    const badge  = tabBtn.querySelector('.tab-badge')
    expect(badge).toBeTruthy()
    expect(badge.textContent).toBe('2')
    expect(badge.getAttribute('aria-hidden')).toBe('true')
  })
})

describe('Modal — Link-Badge (Karte #97)', () => {
  it('kein Badge bei 0 Verknüpfungen', () => {
    const overlay = openModal(makeCard({ links: [] }))
    expect(overlay).toBeTruthy()
    const tabBtn = overlay.querySelector('#tab-links')
    expect(tabBtn.querySelector('.tab-badge')).toBeNull()
  })

  it('Badge zeigt korrekte Anzahl bei mehreren Verknüpfungen', () => {
    const links = [
      { targetId: 'card-x', type: 'related',  created: '2026-05-13' },
      { targetId: 'card-y', type: 'blocks',   created: '2026-05-13' },
      { targetId: 'card-z', type: 'depends-on', created: '2026-05-13' },
    ]
    const overlay = openModal(makeCard({ links }))
    expect(overlay).toBeTruthy()
    const tabBtn = overlay.querySelector('#tab-links')
    const badge  = tabBtn.querySelector('.tab-badge')
    expect(badge).toBeTruthy()
    expect(badge.textContent).toBe('3')
    expect(badge.getAttribute('aria-hidden')).toBe('true')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 6) Tab-Switching
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Tab-Switching', () => {
  it('öffnet standardmäßig auf dem desc-Tab', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    const descPanel = overlay.querySelector('#tab-panel-desc')
    expect(descPanel.classList.contains('tab-panel-active')).toBe(true)
    const tabDesc = overlay.querySelector('#tab-desc')
    expect(tabDesc.getAttribute('aria-selected')).toBe('true')
  })

  it('Klick auf Kommentare-Tab versteckt desc-Panel, zeigt comments-Panel', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    overlay.querySelector('#tab-comments').click()
    const descPanel     = overlay.querySelector('#tab-panel-desc')
    const commentsPanel = overlay.querySelector('#tab-panel-comments')
    expect(descPanel.classList.contains('tab-panel-active')).toBe(false)
    expect(commentsPanel.classList.contains('tab-panel-active')).toBe(true)
  })

  it('Tab-Button-Zustand wird korrekt aktualisiert (aria-selected)', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    overlay.querySelector('#tab-comments').click()
    expect(overlay.querySelector('#tab-desc').getAttribute('aria-selected')).toBe('false')
    expect(overlay.querySelector('#tab-comments').getAttribute('aria-selected')).toBe('true')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 7) Datumsfelder im Modal (ADR-0034)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Datumsfelder (ADR-0034)', () => {
  it('Modal enthält #modal-due-date und #modal-start-date im desc-Tab', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    expect(overlay.querySelector('#modal-due-date')).toBeTruthy()
    expect(overlay.querySelector('#modal-start-date')).toBeTruthy()
  })

  /* F-142 (Karte #525): Der Test oben prüft nur EXISTENZ irgendwo im
     Overlay — `overlay.querySelector()` findet das Feld genauso, wenn es
     versehentlich direkt in `.modal-body` statt im desc-Tab-Panel
     (`#tab-panel-desc`) landet, weil `.modal-body` ebenfalls Nachfahre von
     `overlay` ist. Diese Fassung fragt gezielt INNERHALB von
     `#tab-panel-desc` ab und prüft zusätzlich über `closest()` die
     tatsächliche Verortung — ein Verschieben der Datumsfelder aus
     `panelDesc` heraus in `modalBody` (Geschwister-Ebene der Tab-Panels)
     lässt diesen Test rot werden, den alten oben nicht.
     Gegenprobe durchgeführt (Orchestrator-Bericht): Mit `metaRow3` testweise
     an `modalBody` statt `panelDesc` gehängt blieb der alte Test grün, DIESER
     hier wurde rot — beleg dafür, dass die Schärfung greift. */
  it('#modal-due-date/#modal-start-date liegen strukturell IM desc-Tab-Panel, nicht nur irgendwo im Overlay (F-142, Gegenprobe)', () => {
    const overlay = openModal(makeCard())
    const panelDesc   = overlay.querySelector('#tab-panel-desc')
    const dueInput    = overlay.querySelector('#tab-panel-desc #modal-due-date')
    const startInput  = overlay.querySelector('#tab-panel-desc #modal-start-date')
    expect(panelDesc).toBeTruthy()
    expect(dueInput).toBeTruthy()
    expect(startInput).toBeTruthy()
    expect(panelDesc.contains(dueInput)).toBe(true)
    expect(panelDesc.contains(startInput)).toBe(true)
    expect(dueInput.closest('.tab-panel')?.id).toBe('tab-panel-desc')
    expect(startInput.closest('.tab-panel')?.id).toBe('tab-panel-desc')
  })

  it('dueDate wird korrekt in das Eingabefeld geladen', () => {
    const overlay = openModal(makeCard({ dueDate: '2026-06-15' }))
    expect(overlay).toBeTruthy()
    const input = overlay.querySelector('#modal-due-date')
    expect(input.value).toBe('2026-06-15')
  })

  it('startDate wird korrekt in das Eingabefeld geladen', () => {
    const overlay = openModal(makeCard({ startDate: '2026-05-20' }))
    expect(overlay).toBeTruthy()
    const input = overlay.querySelector('#modal-start-date')
    expect(input.value).toBe('2026-05-20')
  })

  it('dueDate null wird als leerer String geladen', () => {
    const overlay = openModal(makeCard({ dueDate: null }))
    expect(overlay).toBeTruthy()
    const input = overlay.querySelector('#modal-due-date')
    expect(input.value).toBe('')
  })

  /* ADR-0138 §1 (Karte #742): Uhrzeit je Datum, nur mit Datum bedienbar.
     Geprüft wird der über card:updated emittierte Stand, nicht das Feld. */
  const speichereMitX = (overlay) => {
    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('.btn-modal-close').click()
    } finally {
      EventBus.off('card:updated', spy)
    }
    return spy
  }

  it('Uhrzeiten werden geladen, ohne Datum ist das Uhrzeitfeld gesperrt', () => {
    const overlay = openModal(makeCard({ dueDate: '2026-10-08', dueTime: '17:00', startDate: null, startTime: null }))
    expect(overlay.querySelector('#tab-panel-desc #modal-due-time').value).toBe('17:00')
    expect(overlay.querySelector('#modal-due-time').disabled).toBe(false)
    expect(overlay.querySelector('#modal-start-time').disabled).toBe(true)
  })

  it('F-1892: Frist- und Start-Uhrzeit tragen unterscheidbare Beschriftungen', () => {
    const overlay = openModal(makeCard({ dueDate: '2026-10-08', startDate: '2026-10-01' }))
    const frist = overlay.querySelector('#modal-due-time').getAttribute('aria-label')
    const start = overlay.querySelector('#modal-start-time').getAttribute('aria-label')
    expect(frist).toBeTruthy()
    expect(start).toBeTruthy()
    expect(frist).not.toBe(start)
  })

  it('Datum eintragen schaltet die Uhrzeit frei; gespeichert wird sie als eigenes Feld', () => {
    const overlay = openModal(makeCard({ dueDate: null }))
    const datum = overlay.querySelector('#modal-due-date')
    datum.value = '2026-10-08'
    datum.dispatchEvent(new Event('input'))
    const zeit = overlay.querySelector('#modal-due-time')
    expect(zeit.disabled).toBe(false)
    zeit.value = '09:15'
    const spy = speichereMitX(overlay)
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0][0]).toMatchObject({ dueDate: '2026-10-08', dueTime: '09:15' })
  })

  it('Datum leeren leert die Uhrzeit mit', () => {
    const overlay = openModal(makeCard({ dueDate: '2026-10-08', dueTime: '17:00' }))
    const datum = overlay.querySelector('#modal-due-date')
    datum.value = ''
    datum.dispatchEvent(new Event('input'))
    expect(overlay.querySelector('#modal-due-time').disabled).toBe(true)
    const spy = speichereMitX(overlay)
    expect(spy.mock.calls[0][0]).toMatchObject({ dueDate: null, dueTime: null })
  })

  it('Karte ohne Uhrzeit-Felder (alter Bestand) gilt nach dem Öffnen nicht als geändert', () => {
    const roh = makeCard({ dueDate: '2026-10-08' })
    delete roh.dueTime
    delete roh.startTime
    const overlay = openModal(roh)
    const spy = speichereMitX(overlay)
    expect(spy).not.toHaveBeenCalled()
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 8) Dirty-Cleanup (F-107)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Dirty-Cleanup (F-107)', () => {
  it('close() setzt modal.style.transform auf "" (Swipe-Dismiss-Cleanup)', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    const modal = overlay.querySelector('.modal')
    // Swipe-Dismiss simulieren: Transform manuell setzen
    modal.style.transform = 'translateY(100%)'
    Modal.close()
    expect(modal.style.transform).toBe('')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 9) Tab-Sichtbarkeit nach Meta-Karten-Open (F-160)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Tab-Sichtbarkeit-Wechsel Meta → normal (F-160)', () => {
  it('Meta-Karte blendet tab-attachments / tab-links / tab-history aus', () => {
    const overlay = openModal(makeCard({ kind: 'sprint-meta' }))
    expect(overlay.querySelector('#tab-attachments').style.display).toBe('none')
    expect(overlay.querySelector('#tab-links').style.display).toBe('none')
    expect(overlay.querySelector('#tab-history').style.display).toBe('none')
  })

  it('normale Karte zeigt tab-attachments / tab-links / tab-history wieder ein', () => {
    openModal(makeCard({ kind: 'sprint-meta' }))
    Modal.close()
    const overlay = openModal(makeCard({ id: 'card-test-002' }))
    expect(overlay.querySelector('#tab-attachments').style.display).not.toBe('none')
    expect(overlay.querySelector('#tab-links').style.display).not.toBe('none')
    expect(overlay.querySelector('#tab-history').style.display).not.toBe('none')
  })

  it('Meta-Karte blendet tab-agent aus, auch wenn boardMode=project', () => {
    boardModeValue = 'project'
    const overlay = openModal(makeCard({ kind: 'sprint-meta', owner: 'Claude' }))
    expect(overlay.querySelector('#tab-agent').style.display).toBe('none')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 10) Attachment-Resolver wird vor renderDescPane gesetzt (Karte #117)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Attachment-Resolver vor Render (Karte #117)', () => {
  it('Re-Open einer Karte mit attachment-Bild: img.src wird gesetzt (Resolver aktiv beim ersten Render)', async () => {
    const uuid = 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'
    // Standalone-Pfad mit att.data → attachmentUrl liefert synchron data:-URL
    const card = makeCard({
      id: 'card-test-117',
      attachments: [{
        id: uuid,
        filename: 'pic.png',
        mime: 'image/png',
        size: 4,
        sha256: 'x',
        data: 'iVBORw0KGgo=',
        addedAt: '2026-05-13',
      }],
      description: `![pic](attachment://${uuid})`,
    })
    // Re-Open-Szenario: andere Karte zuerst öffnen + schließen
    openModal(makeCard({ id: 'card-other' }))
    Modal.close()
    // Karte mit Bild öffnen
    const overlay = openModal(card)
    expect(overlay).toBeTruthy()
    const img = overlay.querySelector('#tab-panel-desc img.md-inline-img')
    expect(img, 'img-Element wurde nicht gerendert').toBeTruthy()
    expect(img.dataset.attachmentId).toBe(uuid)
    // Microtask abwarten — Resolver-Promise muss settlen
    await Promise.resolve()
    await Promise.resolve()
    expect(img.src).toMatch(/^data:image\/png;base64,/)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 11) F-118 — rAF-Callback-Guard (config:language-changed während Open-Animation)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — rAF-Null-Guard (F-118)', () => {
  it('kein Crash wenn config:language-changed während der Open-Animation feuert', () => {
    // Modal öffnen — rAF ist durch vi.useFakeTimers() noch nicht gefeuert
    openModal(makeCard())
    // config:language-changed setzt overlay = null im Modal-Modul
    EventBus.emit('config:language-changed')
    document.body.innerHTML = ''
    // rAF-Callbacks ausführen — darf nicht crashen (F-118: if (!overlay) return)
    expect(() => vi.runAllTimers()).not.toThrow()
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 12) F-119 — switchTab Desc-Section-Selektor konsistent zu close()
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — switchTab Desc-Section-Selektor (F-119)', () => {
  it('switchTab spricht nur die Desc-Section im #tab-panel-desc-Panel an', () => {
    const overlay = openModal(makeCard({ owner: 'Claude', complexity: 'mittel' }))

    const descSection  = overlay.querySelector('#tab-panel-desc .modal-desc-section')
    const agentSection = overlay.querySelector('#tab-panel-agent .modal-desc-section')
    expect(descSection).toBeTruthy()
    expect(agentSection).toBeTruthy()

    // Desc-Pane maximieren
    const maxBtn = overlay.querySelector('#tab-panel-desc .btn-maximize')
    expect(maxBtn).toBeTruthy()
    maxBtn.click()

    // Agent-Tab öffnen — löst switchTab aus
    const agentTabBtn = overlay.querySelector('#tab-agent')
    agentTabBtn.click()

    // Nur die Desc-Section darf de-maximiert werden; Agent-Section ist unberührt
    expect(descSection.classList.contains('desc-maximized')).toBe(false)
    // Selektor-Breite prüfen: querySelectorAll('.modal-desc-section') wäre hier 2 Elemente,
    // aber switchTab darf nur 1 (die desc-interne) treffen.
    // Wir stellen sicher, dass der narrowe Selektor exakt 1 Element findet.
    expect(overlay.querySelectorAll('#tab-panel-desc .modal-desc-section').length).toBe(1)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 13) Karte #292 — Export-Submenü (Drucken / Markdown / E-Mail)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Export-Submenü (#292)', () => {
  it('rendert Export-Button + 3 Submenü-Einträge', () => {
    const overlay = openModal(makeCard())
    const expBtn  = overlay.querySelector('#modal-export-btn')
    expect(expBtn).toBeTruthy()
    expect(overlay.querySelector('#modal-export-print')).toBeTruthy()
    expect(overlay.querySelector('#modal-export-md')).toBeTruthy()
    expect(overlay.querySelector('#modal-export-email')).toBeTruthy()
  })

  it('Submenü ist initial verborgen, Klick auf Export-Button öffnet es', () => {
    const overlay = openModal(makeCard())
    const menu    = overlay.querySelector('.modal-export-menu')
    expect(menu.style.display).toBe('none')
    overlay.querySelector('#modal-export-btn').click()
    expect(menu.style.display).toBe('flex')
  })

  it('Klick außerhalb schließt das Submenü', () => {
    const overlay = openModal(makeCard())
    const menu    = overlay.querySelector('.modal-export-menu')
    overlay.querySelector('#modal-export-btn').click()
    expect(menu.style.display).toBe('flex')
    /* In den Modal-Body klicken — gilt als außerhalb des exportWrap */
    overlay.querySelector('.modal-body').click()
    expect(menu.style.display).toBe('none')
  })

  it('Markdown-Klick triggert downloadCardMarkdown (Blob erzeugt)', async () => {
    const { Export } = await import('../src/export.js')
    const orig = Export.downloadCardMarkdown
    let called = null
    Export.downloadCardMarkdown = (card, board) => { called = { card, board } }
    try {
      const overlay = openModal(makeCard({ title: 'Test292' }))
      overlay.querySelector('#modal-export-btn').click()
      overlay.querySelector('#modal-export-md').click()
      expect(called).not.toBeNull()
      expect(called.card.title).toBe('Test292')
    } finally {
      Export.downloadCardMarkdown = orig
    }
  })
})

/* ── #296: Cross-Board-Transfer-Button im Modal ─────────────────────── */

const isServerBuildCtx = (typeof __SERVER_BUILD__ !== 'undefined')
  && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true')

describe('Modal — Cross-Board-Transfer-Button (#296)', () => {
  it.skipIf(isServerBuildCtx)('Standalone-Build: Transfer-Button nicht im DOM (DCE)', () => {
    const overlay = openModal(makeCard())
    expect(overlay.querySelector('#modal-transfer-btn')).toBeFalsy()
  })

  it.skipIf(!isServerBuildCtx)('Server-Build: Transfer-Button existiert, verborgen bei feature.restStorage=false', () => {
    const overlay = openModal(makeCard())
    const btn = overlay.querySelector('#modal-transfer-btn')
    expect(btn).toBeTruthy()
    /* Default-Profile (solo) hat feature.restStorage=false → verborgen */
    expect(btn.style.display).toBe('none')
  })

  it.skipIf(!isServerBuildCtx)('Server-Build: Transfer-Button verborgen bei sprint-meta-Karte', () => {
    /* Profile liest Config.get('boardMode') — über boardModeValue im Config-Mock auf 'server' setzen. */
    boardModeValue = 'server'
    try {
      const overlay = openModal(makeCard({ kind: 'sprint-meta' }))
      const btn = overlay.querySelector('#modal-transfer-btn')
      expect(btn).toBeTruthy()
      expect(btn.style.display).toBe('none')
    } finally {
      boardModeValue = 'standalone'
    }
  })

  it.skipIf(!isServerBuildCtx)('Server-Build: Transfer-Button sichtbar bei normaler Karte + feature.restStorage=true', () => {
    boardModeValue = 'server'
    try {
      const overlay = openModal(makeCard())
      const btn = overlay.querySelector('#modal-transfer-btn')
      expect(btn).toBeTruthy()
      expect(btn.style.display).toBe('')
    } finally {
      boardModeValue = 'standalone'
    }
  })
})

/* ── F-158 (Karte #433): Meta-Karten-Guard in archiveAndClose ────────── */

describe('Modal — archiveAndClose Meta-Karten-Guard (F-158/ADR-0036)', () => {
  it('Klick auf den (versteckten) Archiv-Button einer Sprint-Meta-Karte emittiert KEIN card:updated', () => {
    /* col-erledigt sorgt dafür, dass der Button ohne Meta-Sonderbehandlung
       sichtbar wäre (archBtn.style.display = inErledigt ? '' : 'none') —
       genau der Fall, in dem der Guard greifen muss. updateMetaCardMode()
       versteckt den Button zwar zusätzlich per CSS, aber .click() feuert
       den Listener in happy-dom unabhängig von style.display — das bildet
       den "programmatischen Aufruf" ab, den F-158 beschreibt. */
    const card = makeCard({ kind: 'sprint-meta', metaType: 'end', columnId: 'col-erledigt', archived: false })
    const overlay = openModal(card)
    const archBtn = overlay.querySelector('#modal-archive-btn')
    expect(archBtn).toBeTruthy()

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      archBtn.click()
      expect(spy).not.toHaveBeenCalled()
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  /* Sprint-60-Kritiker: Der Guard darf NUR das Archivieren blocken, nicht das
     Zurückholen — `archiveAndClose` ist ein Toggle. Eine Meta-Karte kann
     `archived:true` auch ohne diesen Pfad tragen (Import, `card.field`-Op),
     und der Renderer blendet archivierte Karten ohne Meta-Ausnahme aus: eine
     bedingungslose Sperre hätte die Sprint-Abschluss-Karte samt Recap
     unsichtbar UND unerreichbar gemacht. */
  it('Eine bereits archivierte Meta-Karte lässt sich zurückholen — der Guard mauert den Rückweg nicht zu', () => {
    const card = makeCard({ kind: 'sprint-meta', metaType: 'end', columnId: 'col-erledigt', archived: true })
    const overlay = openModal(card)
    const archBtn = overlay.querySelector('#modal-archive-btn')

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      archBtn.click()
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].archived).toBe(false)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('Klick auf den Archiv-Button einer normalen Karte in col-erledigt emittiert weiterhin card:updated mit archived:true (Gegenprobe: Guard trifft nur Meta-Karten)', () => {
    const card = makeCard({ columnId: 'col-erledigt', archived: false })
    const overlay = openModal(card)
    const archBtn = overlay.querySelector('#modal-archive-btn')
    expect(archBtn).toBeTruthy()
    expect(archBtn.style.display).not.toBe('none')

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      archBtn.click()
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].archived).toBe(true)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })
})

/* ── F-830 (Karte #454): Archivieren-Button — Titel-Semantik statt Id-Hartvergleich ── */

describe('Modal — Archivieren-Button erkennt auch eine umbenannte Erledigt-Spalte (F-830)', () => {
  afterEach(() => { State.set(null) })

  it('Button ist sichtbar, obwohl die Erledigt-Spalte eine UUID-id trägt (ADR-0052-Fall)', () => {
    /* Vorher: `card.columnId === 'col-erledigt'` — eine manuell angelegte oder
       umbenannte Erledigt-Spalte (UUID-id, `createColumnDef()`) wurde nie
       erkannt, der Button blieb versteckt. */
    State.set({
      version: '2',
      board: {
        id: 'b1',
        columnDefs: [
          { id: 'col-backlog',   title: 'Backlog' },
          { id: 'col-9f3a-uuid', title: 'Fertig' }, // umbenannte Erledigt-Spalte
        ],
        sprints: [{ id: 's1', cards: [] }],
      },
    })
    const card = makeCard({ columnId: 'col-9f3a-uuid' })
    const overlay = openModal(card)
    const archBtn = overlay.querySelector('#modal-archive-btn')
    expect(archBtn).toBeTruthy()
    expect(archBtn.style.display).not.toBe('none')
  })

  it('Button bleibt versteckt auf einer Nicht-Erledigt-Spalte mit columnDefs im State', () => {
    State.set({
      version: '2',
      board: {
        id: 'b1',
        columnDefs: [
          { id: 'col-backlog',   title: 'Backlog' },
          { id: 'col-9f3a-uuid', title: 'Fertig' },
        ],
        sprints: [{ id: 's1', cards: [] }],
      },
    })
    const card = makeCard({ columnId: 'col-backlog' })
    const overlay = openModal(card)
    const archBtn = overlay.querySelector('#modal-archive-btn')
    expect(archBtn.style.display).toBe('none')
  })
})

/* ── F-146 (Karte #433): Badge-Update konsolidiert IM Render-Pfad ────── */

describe('Modal — Badge-Update-Konsistenz (F-146)', () => {
  it('Kommentar über die UI hinzufügen aktualisiert den Tab-Badge automatisch (renderComments aktualisiert selbst, nicht der Aufrufer)', () => {
    const overlay = openModal(makeCard({ comments: [] }))
    const tabBtn = overlay.querySelector('#tab-comments')
    expect(tabBtn.querySelector('.tab-badge')).toBeNull()

    const input  = overlay.querySelector('#comment-input')
    const addBtn = overlay.querySelector('.comment-form button')
    input.value = 'Neuer Kommentar'
    addBtn.click()

    const badge = tabBtn.querySelector('.tab-badge')
    expect(badge).toBeTruthy()
    expect(badge.textContent).toBe('1')
  })

  it('Links-Tab-Badge zeigt korrekte Anzahl (renderLinksList aktualisiert Badge am Ende, einheitlich mit renderAttachmentsList/renderComments)', () => {
    const links = [
      { targetId: 'card-x', type: 'related' },
      { targetId: 'card-y', type: 'depends-on' },
    ]
    const overlay = openModal(makeCard({ links }))
    const tabBtn = overlay.querySelector('#tab-links')
    const badge  = tabBtn.querySelector('.tab-badge')
    expect(badge).toBeTruthy()
    expect(badge.textContent).toBe('2')
  })

  /* Sprint-60-Kritiker: Die erste Fassung dieses Tests prüfte bei `links: []`
     auf `.tab-badge === null` — das ist tautologisch. Ohne JEDEN
     updateLinksBadge()-Aufruf gäbe es nie einen Badge, der Test wäre auch mit
     entferntem Fix grün geblieben (Fehlerklasse „Tests, die nicht fehlschlagen
     können"). Was der if/else-Umbau wirklich zusichert, ist der ÜBERGANG:
     Der Badge muss auch dann verschwinden, wenn vorher einer da war — also im
     Leer-Zweig, der vor dem Umbau hinter einem frühen `return` lag. */
  it('Links-Badge verschwindet beim Rendern einer Karte ohne Verweise — der Leer-Zweig aktualisiert ihn ebenfalls', () => {
    const mitLinks = openModal(makeCard({ links: [
      { targetId: 'card-x', type: 'related' },
      { targetId: 'card-y', type: 'depends-on' },
    ] }))
    expect(mitLinks.querySelector('#tab-links .tab-badge').textContent).toBe('2')

    /* Dasselbe Overlay, neue Karte ohne Verweise: der Leer-Zweig muss den
       Badge des Vorgängers abräumen. */
    const ohneLinks = openModal(makeCard({ links: [] }))
    const tabBtn = ohneLinks.querySelector('#tab-links')
    expect(tabBtn.querySelector('.tab-badge')).toBeNull()
    expect(ohneLinks.querySelector('#tab-panel-links .link-empty')).toBeTruthy()
  })
})

/* ── F-121 (Karte #525): Pending-Comment-Pfad des Comment-Badges ──────────
   Die bestehenden Comment-Badge-Tests (Sektion 5 oben) decken nur den
   Fall `pendingComments.length === 0` (Karte via makeCard geöffnet, kein
   Kommentar über die UI eingegeben) ab. Der F-146-Test direkt oberhalb
   deckt den umgekehrten Fall `comments.length === 0, pendingComments === 1`
   ab. Ungetestet blieb die eigentliche ADDITION in updateCommentBadge()
   (`(currentCard?.comments?.length || 0) + pendingComments.length`) — ein
   Tippfehler, der `+` durch `||` oder ein versehentliches Überschreiben
   statt Addieren ersetzt, wäre mit beiden bestehenden Tests weiter grün
   geblieben, weil dort nie BEIDE Quellen gleichzeitig > 0 sind. */
describe('Modal — Comment-Badge: Pending-Comment-Pfad (F-121)', () => {
  it('Badge summiert bestehende UND im Modal neu hinzugefügte (noch nicht gespeicherte) Kommentare', () => {
    const comments = [
      { id: 'c1', author: 'A', text: 'Bestehend eins', created: '2026-05-13' },
      { id: 'c2', author: 'B', text: 'Bestehend zwei', created: '2026-05-13' },
    ]
    const overlay = openModal(makeCard({ comments }))
    const tabBtn = overlay.querySelector('#tab-comments')
    expect(tabBtn.querySelector('.tab-badge').textContent).toBe('2')

    const input  = overlay.querySelector('#comment-input')
    const addBtn = overlay.querySelector('.comment-form button')
    input.value = 'Neu, noch ungespeichert'
    addBtn.click()

    // 2 bestehende + 1 pending = 3 — eine reine Ersetzung (statt Addition)
    // würde hier fälschlich bei 1 stehen bleiben.
    expect(tabBtn.querySelector('.tab-badge').textContent).toBe('3')
  })

  it('Badge zählt MEHRERE pending Kommentare korrekt (nicht nur "vorhanden ja/nein")', () => {
    const overlay = openModal(makeCard({ comments: [] }))
    const tabBtn = overlay.querySelector('#tab-comments')
    const input  = overlay.querySelector('#comment-input')
    const addBtn = overlay.querySelector('.comment-form button')

    input.value = 'Erster pending Kommentar'
    addBtn.click()
    expect(tabBtn.querySelector('.tab-badge').textContent).toBe('1')

    input.value = 'Zweiter pending Kommentar'
    addBtn.click()
    expect(tabBtn.querySelector('.tab-badge').textContent).toBe('2')

    input.value = 'Dritter pending Kommentar'
    addBtn.click()
    expect(tabBtn.querySelector('.tab-badge').textContent).toBe('3')
  })

  it('pending Kommentare überleben einen Tab-Wechsel weg von und zurück zu Kommentare (Badge bleibt korrekt)', () => {
    const comments = [{ id: 'c1', author: 'A', text: 'Bestehend', created: '2026-05-13' }]
    const overlay = openModal(makeCard({ comments }))
    overlay.querySelector('#tab-comments').click()
    const input  = overlay.querySelector('#comment-input')
    const addBtn = overlay.querySelector('.comment-form button')
    input.value = 'Pending vor Tab-Wechsel'
    addBtn.click()

    const tabBtn = overlay.querySelector('#tab-comments')
    expect(tabBtn.querySelector('.tab-badge').textContent).toBe('2')

    // Weg zu desc und zurück — renderComments() wird dabei NICHT erneut
    // aufgerufen (nur switchTab()), der Badge muss trotzdem stehen bleiben.
    overlay.querySelector('#tab-desc').click()
    overlay.querySelector('#tab-comments').click()
    expect(tabBtn.querySelector('.tab-badge').textContent).toBe('2')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 9) Sprint-61-Aufräumblock: F-756, F-770(b), F-113 (die F-125-Tests liegen in tests/links.test.js, nicht hier)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Einblenden ohne Fokus-Wettlauf (F-756)', () => {
  /* Bis Sprint 61 setzte open() die '--open'-Klasse in einem
     requestAnimationFrame, während der Initialfokus (#modal-title) synchron in
     derselben Funktion lief — also garantiert VORHER. Damit traf focus() ein
     Element unter einem noch `visibility:hidden` gesetzten Vorfahren; ein
     solches Element ist per Fokus-Algorithmus nicht fokussierbar, der Aufruf
     war im echten Browser ein stiller No-Op.
     happy-dom kann das nicht zeigen (kein Layout, `visibility` kaskadiert
     nicht) — nachweisbar ist aber die REIHENFOLGE, und die ist die Ursache:
     unter Fake-Timern feuert kein rAF, die Klasse muss trotzdem stehen, bevor
     irgendetwas Asynchrones lief. */
  it("'--open' steht synchron beim Öffnen — kein rAF mehr dazwischen", () => {
    const overlay = openModal(makeCard())
    expect(overlay.classList.contains('modal-overlay--open')).toBe(true)
  })

  it('Initialfokus liegt auf dem Titelfeld, während die Klasse schon steht', () => {
    const overlay = openModal(makeCard())
    expect(overlay.classList.contains('modal-overlay--open')).toBe(true)
    expect(document.activeElement.id).toBe('modal-title')
  })
})

describe('Modal — Kommentar-Badge ohne Kommentarliste (F-770 b)', () => {
  /* Seit Karte #433 lief updateCommentBadge() nur noch innerhalb von
     renderComments, und das steht im Öffnen-Pfad hinter `if (list)`. Fehlte
     `#comments-list`, blieb der Badge der ZUVOR geöffneten Karte stehen. */
  it('Badge der Vorgängerkarte bleibt nicht stehen, wenn #comments-list fehlt', () => {
    const mitKommentaren = openModal(makeCard({ comments: [
      { id: 'c1', author: 'A', text: 'eins',  created: '2026-01-01' },
      { id: 'c2', author: 'A', text: 'zwei',  created: '2026-01-01' },
      { id: 'c3', author: 'A', text: 'drei',  created: '2026-01-01' },
    ] }))
    expect(mitKommentaren.querySelector('#tab-comments .tab-badge').textContent).toBe('3')

    /* Die Liste aus dem Overlay entfernen — der Öffnen-Pfad findet sie nicht
       mehr und übersprang vor dem Fix jede Badge-Aktualisierung. */
    mitKommentaren.querySelector('#comments-list').remove()

    const ohneKommentare = openModal(makeCard({ id: 'card-test-002', comments: [] }))
    expect(ohneKommentare.querySelector('#tab-comments .tab-badge')).toBeNull()
  })
})

describe('Modal — Pfeil-Navigation im Überschriften-Dropdown (F-113)', () => {
  function headingTeile(overlay) {
    const wrap = overlay.querySelector('.toolbar-dropdown-wrap')
    return {
      btn:   wrap.querySelector('.toolbar-btn-dropdown'),
      menu:  wrap.querySelector('.toolbar-dropdown-menu'),
      items: [...wrap.querySelectorAll('.toolbar-dropdown-item')],
    }
  }
  function pfeil(el, key) {
    el.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }))
  }

  it('Pfeil-runter am Auslöser öffnet das Menü und fokussiert den ersten Eintrag', () => {
    const overlay = openModal(makeCard())
    const { btn, menu, items } = headingTeile(overlay)
    pfeil(btn, 'ArrowDown')
    expect(menu.classList.contains('open')).toBe(true)
    expect(document.activeElement).toBe(items[0])
  })

  it('Pfeil-hoch am Auslöser fokussiert den letzten Eintrag', () => {
    const overlay = openModal(makeCard())
    const { btn, items } = headingTeile(overlay)
    pfeil(btn, 'ArrowUp')
    expect(document.activeElement).toBe(items[items.length - 1])
  })

  it('Pfeil-runter im Menü wandert weiter und läuft zyklisch um', () => {
    const overlay = openModal(makeCard())
    const { btn, items } = headingTeile(overlay)
    pfeil(btn, 'ArrowDown')
    pfeil(items[0], 'ArrowDown')
    expect(document.activeElement).toBe(items[1])
    pfeil(items[1], 'ArrowDown')
    expect(document.activeElement).toBe(items[0])
  })

  it('Esc schliesst das Menü und gibt den Fokus an den Auslöser zurück', () => {
    const overlay = openModal(makeCard())
    const { btn, menu, items } = headingTeile(overlay)
    pfeil(btn, 'ArrowDown')
    pfeil(items[0], 'Escape')
    expect(menu.classList.contains('open')).toBe(false)
    expect(document.activeElement).toBe(btn)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 10) ESC schliesst nur den oberen Dialog, nicht das Karten-Modal (F-752)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — ESC im Dialog-Stapel schliesst nicht zusätzlich das Karten-Modal (F-752)', () => {
  /* Der Fehler betrifft konkret PII-Preview (llm-pii.js) und den E-Mail-
     Dialog (email-dialog.js), die beide aus dem geöffneten Karten-Modal
     heraus über modal-shell.js/createModal() geöffnet werden (siehe
     modal.js:2538 für EmailDialog, ADR-0118). Der Test baut hier bewusst
     KEINEN Mock der Shell, sondern importiert das echte createModal() —
     genau das Modul, das beide Dialoge tatsächlich verwenden — und öffnet
     es über dem echten Karten-Modal. Nur so wird der Stapel selbst
     betreten (zwei echte, an document hängende keydown-Listener in
     Registrierungsreihenfolge Karten-Modal zuerst), nicht nur eine der
     beiden Seiten isoliert. */

  function baueGestapeltenDialog() {
    return createModal({
      extraOverlayClass: 'test-stacked-overlay',
      extraPanelClass: 'test-stacked-panel',
      titleId: 'test-stacked-title',
      closeAriaLabel: 'Schließen',
    })
  }

  it('ESC bei offenem Karten-Modal OHNE weiteren Dialog schliesst das Karten-Modal (Baseline)', () => {
    const overlay = openModal(makeCard())
    expect(overlay.classList.contains('modal-overlay--open')).toBe(true)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))

    expect(overlay.classList.contains('modal-overlay--open')).toBe(false)
  })

  it('ESC bei Karten-Modal + gestapeltem Dialog schliesst NUR den Dialog, das Karten-Modal bleibt offen', () => {
    const cardOverlay = openModal(makeCard({ title: 'Gespeicherter Titel' }))
    expect(cardOverlay.classList.contains('modal-overlay--open')).toBe(true)

    /* Der Schaden, den F-752 anrichtet, ist nicht das Zugehen an sich,
       sondern dass close() nicht speichert. Nachgemessen lässt sich der
       Verlust hier NICHT zusichern: close() räumt das Eingabefeld nicht
       auf, der `value` steht nach dem Schliessen unverändert im DOM. Eine
       Zusicherung auf den Feldinhalt wäre deshalb eine Attrappe — sie
       bleibt auch mit zurückgebautem Guard grün (im Rückbau geprüft).
       Der offene Overlay-Zustand unten ist die einzige Zusicherung, die
       den Fix wirklich trägt; der Feldinhalt wird nur gesetzt, damit der
       Testfall dem realen Szenario entspricht. */
    cardOverlay.querySelector('#modal-title').value = 'Ungespeicherte Änderung'

    const dialog = baueGestapeltenDialog()
    dialog.open()
    expect(dialog.overlay.classList.contains('modal-overlay--open')).toBe(true)
    /* Beide Overlays sind jetzt gleichzeitig offen — der Stapel steht. */
    expect(cardOverlay.classList.contains('modal-overlay--open')).toBe(true)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))

    /* Der obere Dialog behandelt ESC selbst und schliesst sich. */
    expect(dialog.overlay.classList.contains('modal-overlay--open')).toBe(false)
    /* Das Karten-Modal darunter bleibt unangetastet — ohne den Guard in
       modal.js#onGlobalKey würde hier close() laufen und die (nicht
       gespeicherte) Titeländerung wäre weg. */
    expect(cardOverlay.classList.contains('modal-overlay--open')).toBe(true)

    /* Aufräumen: verbleibenden document-keydown-Listener der Shell-Instanz
       abbauen, damit er nicht in einen späteren Test dieser Datei hineinwirkt. */
    dialog.close()
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 11) Auto-Save beim Schliessen — Dirty-Guard (Karte #463, F-752-Folgekarte)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — Auto-Save beim Schliessen (Karte #463)', () => {
  /* Team-Entscheid (Kartenbeschreibung #463): Auto-Save statt Rückfrage-
     Dialog. Die vier dort benannten ambigen Schliesswege sind X-Knopf,
     Backdrop-Klick, ESC am Karten-Modal und beforeunload — plus (Bonus,
     nicht in der Kartenbeschreibung genannt, aber dieselbe Lücke) Swipe-to-
     Dismiss auf Mobil. Der Abbrechen-Button bleibt bewusst aussen vor: er
     ist die einzige EXPLIZITE Verwerfen-Aktion im Modal (eigener Test unten).

     Zusicherungen laufen NIE auf den DOM-Feldinhalt — der bleibt nach
     close() unverändert stehen (siehe #440/F-752, Zeile ~1080 dieser Datei:
     eine Zusicherung auf den Feldinhalt bleibt auch OHNE Fix grün und prüft
     damit nichts). Stattdessen wird der über `card:updated` EMITTIERTE, also
     tatsächlich gespeicherte Kartenzustand geprüft — Muster aus
     "archiveAndClose Meta-Karten-Guard" weiter oben in dieser Datei. */

  it('X-Knopf speichert eine ungespeicherte Titeländerung, bevor er schliesst', () => {
    const overlay = openModal(makeCard({ title: 'Alter Titel' }))
    overlay.querySelector('#modal-title').value = 'Neuer Titel per X-Knopf'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('.btn-modal-close').click()
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].title).toBe('Neuer Titel per X-Knopf')
      expect(overlay.classList.contains('modal-overlay--open')).toBe(false)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('Backdrop-Klick (ausserhalb des Panels) speichert eine ungespeicherte Titeländerung', () => {
    const overlay = openModal(makeCard({ title: 'Alter Titel' }))
    overlay.querySelector('#modal-title').value = 'Neuer Titel per Backdrop'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      /* Der Backdrop-Handler prüft `e.target === ov` — ein Klick direkt auf
         das Overlay-Element (nicht auf ein Kind-Element des Panels) bildet
         genau das ab. */
      overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].title).toBe('Neuer Titel per Backdrop')
      expect(overlay.classList.contains('modal-overlay--open')).toBe(false)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('ESC am Karten-Modal selbst (ohne gestapelten Dialog, über onGlobalKey) speichert eine ungespeicherte Titeländerung', () => {
    const overlay = openModal(makeCard({ title: 'Alter Titel' }))
    overlay.querySelector('#modal-title').value = 'Neuer Titel per ESC'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      /* Dispatch auf document (nicht auf das Titelfeld) trifft gezielt den
         globalen onGlobalKey-Listener — die zweite, unabhängige ESC-Stelle
         (titleInput-eigener Handler) hat einen eigenen Test unten. */
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].title).toBe('Neuer Titel per ESC')
      expect(overlay.classList.contains('modal-overlay--open')).toBe(false)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('ESC im fokussierten Titelfeld (eigener, zweiter ESC-Handler) speichert ebenfalls', () => {
    const overlay = openModal(makeCard({ title: 'Alter Titel' }))
    const titleInput = overlay.querySelector('#modal-title')
    titleInput.value = 'Neuer Titel per Titelfeld-ESC'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      titleInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].title).toBe('Neuer Titel per Titelfeld-ESC')
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('beforeunload ruft preventDefault() auf, wenn das offene Modal ungespeicherte Änderungen hat', () => {
    openModal(makeCard({ title: 'Alter Titel' }))
    document.querySelector('#modal-title').value = 'Ungespeichert bei Tab-Schluss'

    const ev = new Event('beforeunload', { cancelable: true })
    const preventSpy = vi.spyOn(ev, 'preventDefault')
    window.dispatchEvent(ev)

    expect(preventSpy).toHaveBeenCalled()
    expect(ev.returnValue).toBe('')
  })

  it('beforeunload warnt NICHT, wenn im offenen Modal nichts geändert wurde', () => {
    openModal(makeCard({ title: 'Unveränderter Titel' }))

    const ev = new Event('beforeunload', { cancelable: true })
    const preventSpy = vi.spyOn(ev, 'preventDefault')
    window.dispatchEvent(ev)

    expect(preventSpy).not.toHaveBeenCalled()
  })

  it('beforeunload warnt NICHT, wenn gar kein Karten-Modal offen ist', () => {
    /* Kein openModal() in diesem Test — currentCard ist null. */
    const ev = new Event('beforeunload', { cancelable: true })
    const preventSpy = vi.spyOn(ev, 'preventDefault')
    window.dispatchEvent(ev)

    expect(preventSpy).not.toHaveBeenCalled()
  })

  it('Schliessen ohne jede Änderung emittiert KEIN card:updated (kein unnötiger Save)', () => {
    const overlay = openModal(makeCard({ title: 'Unveränderter Titel' }))

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('.btn-modal-close').click()
      expect(spy).not.toHaveBeenCalled()
      expect(overlay.classList.contains('modal-overlay--open')).toBe(false)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('Abbrechen-Button bleibt die explizite Ausnahme — verwirft weiterhin, KEIN Auto-Save', () => {
    /* Bewusste Abweichung vom "alles automatisch speichern"-Reflex: Würde
       auch dieser Button speichern, gäbe es im ganzen Modal keinen Weg mehr,
       eine Bearbeitung bewusst zu verwerfen — siehe Bericht. */
    const overlay = openModal(makeCard({ title: 'Alter Titel' }))
    overlay.querySelector('#modal-title').value = 'Sollte verworfen werden'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('.modal-actions-right .btn:not(.btn-primary)').click()
      expect(spy).not.toHaveBeenCalled()
      expect(overlay.classList.contains('modal-overlay--open')).toBe(false)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('Leerer Titel + Backdrop-Klick: kann nicht gespeichert werden (Validierung), blockiert das Schliessen aber nicht', () => {
    /* Kante, die die Team-Entscheidung "Auto-Save" nicht abdeckt: Ein leerer
       Titel darf nicht gespeichert werden (bestehende Validierung in
       saveAndClose). Ohne den currentCard-Rückfallpfad in
       closeWithAutosave() bliebe das Modal hier für Backdrop/X/ESC/Swipe
       wirkungslos stehen — hier wird geprüft, dass es stattdessen (ohne zu
       speichern) schliesst. */
    const overlay = openModal(makeCard({ title: 'Alter Titel' }))
    overlay.querySelector('#modal-title').value = '   '

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(spy).not.toHaveBeenCalled()
      expect(overlay.classList.contains('modal-overlay--open')).toBe(false)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('[Bonus, ausserhalb der 4 in der Karte genannten Wege] Swipe-to-Dismiss ist ebenfalls auf Auto-Save verdrahtet', () => {
    const overlay = openModal(makeCard({ title: 'Alter Titel' }))
    overlay.querySelector('#modal-title').value = 'Neuer Titel per Swipe'

    /* addSwipeToDismiss(modal, callback) ist gemockt (Zeile ~25 dieser
       Datei) — der zweite Callback-Parameter des letzten Aufrufs IST
       closeWithAutosave, direkt aus dem echten Wiring in modal.js. */
    const lastCall = vi.mocked(addSwipeToDismiss).mock.calls.at(-1)
    expect(lastCall).toBeTruthy()
    const dismissCallback = lastCall[1]

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      dismissCallback()
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].title).toBe('Neuer Titel per Swipe')
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  /* Verify-Lauf (Orchestrator, ADR-0115 Phase 2b): isDirty() zählte die zu
     vergleichenden Felder je Kartentyp einzeln auf — im Normal-Zweig fehlte
     `labels`, obwohl computeFormPatch() es liefert und der Template-Zweig es
     bereits verglich. Wer NUR die Labels ändert und über einen der ambigen
     Wege schliesst, verlor die Änderung wortlos — derselbe Defekt wie in der
     Kartenbeschreibung, nur für ein anderes Feld. Dieser Test bildet genau
     das nach: Titel bleibt unangetastet, nur `#modal-labels` ändert sich. */
  it('Nur-Labels-Änderung wird beim Backdrop-Klick gespeichert, nicht verworfen (Verify-Lauf-Befund)', () => {
    const overlay = openModal(makeCard({ title: 'Unveränderter Titel', labels: ['alt'] }))
    overlay.querySelector('#modal-labels').value = 'alt, neu'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].labels).toEqual(['alt', 'neu'])
      expect(overlay.classList.contains('modal-overlay--open')).toBe(false)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 12) Kritiker-Nachlese Sprint 63: das Karten-Modal wandert beim Öffnen ans
//     DOM-Ende — sonst schliesst ESC gar nichts
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — DOM-Position beim Öffnen (Kritiker-Befund Sprint 63)', () => {
  /* Die Stapel-Regel der Modal-Shell (Karte #465) setzt voraus, dass die
     DOM-Reihenfolge die AKTUELLE Öffnungsreihenfolge abbildet. Die Shell stellt
     das seit F-914 selbst her; das Karten-Modal tat es nicht — es hängte sein
     Overlay nur bei der ERSTEN Öffnung an und behielt die Position für immer.

     Der Fall ist über die Tastatur erreichbar: Shell-Dialoge setzen keinen
     Shortcut-Kontext, `j`/`Enter` öffnet also auch bei offenem Sidebar-Dialog
     eine Karte. Stand das Karten-Modal dann hinter dem Shell-Overlay im DOM,
     traten BEIDE Guards zurück — der Stapel-Guard der Shell („ich bin nicht
     oberstes") und der Existenz-Guard des Karten-Modals („ein anderes Overlay
     ist offen"). ESC schloss gar nichts. */

  function baueShellDialog() {
    return createModal({
      extraOverlayClass: 'test-sprint63-shell',
      titleId: 'test-sprint63-title',
      closeAriaLabel: 'Schließen',
    })
  }
  /* Das Karten-Modal ist NUR über diese Zusatzklasse sicher zu greifen — ein
     `querySelector('.modal-overlay')` liefert sonst das erste Overlay im DOM,
     und das ist in diesem Aufbau die Shell. */
  const kartenOverlay = () => document.querySelector('.modal-overlay--blocks-sync')

  it('das zuletzt geöffnete Overlay steht am DOM-Ende, auch wenn die Shell zuerst da war', () => {
    const dlg = baueShellDialog()
    dlg.open()
    dlg.close()                       // Overlay bleibt im DOM (close() entfernt es nicht)

    openModal(makeCard())             // Karten-Modal zum ersten Mal
    dlg.open()                        // Shell erneut — muss jetzt HINTER dem Karten-Modal stehen

    const alle = [...document.querySelectorAll('.modal-overlay')]
    expect(alle.indexOf(dlg.overlay)).toBeGreaterThan(alle.indexOf(kartenOverlay()))

    dlg.close()
  })

  /* DIE SACKGASSE. Reihenfolge wie im Kritiker-Szenario: erst der
     Sidebar-Dialog, DANN die Karte. Das Karten-Modal ist damit das obere
     Overlay — und muss auf ESC schliessen. Vor dem Fix trat der
     Existenz-Guard des Karten-Modals zurück („ein anderes Overlay ist offen")
     und der Stapel-Guard der Shell ebenfalls („ich bin nicht oberstes"):
     ESC bewirkte gar nichts, in beide Richtungen. */
  it('Shell zuerst, Karte danach: ESC schliesst die KARTE — nicht gar nichts', () => {
    const dlg = baueShellDialog()
    dlg.open()

    openModal(makeCard())
    /* NICHT den Rückgabewert von `openModal` nehmen: Der Helfer liefert
       `querySelector('.modal-overlay')`, also das ERSTE Overlay im DOM — in
       diesem Aufbau die Shell. Genau diese Verwechslung hat im Verify-Lauf zu
       Karte #465 schon einmal eine Messung wertlos gemacht. */
    const card = kartenOverlay()
    expect(card.classList.contains('modal-overlay--open')).toBe(true)
    expect(dlg.overlay.classList.contains('modal-overlay--open')).toBe(true)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))

    /* Das Karten-Modal liegt oben (später im DOM, gleicher z-index) → es geht zu. */
    expect(card.classList.contains('modal-overlay--open'),
      'ESC muss das obere Overlay schliessen — vor dem Fix passierte gar nichts').toBe(false)
    /* Der Dialog darunter bleibt offen. */
    expect(dlg.overlay.classList.contains('modal-overlay--open')).toBe(true)

    dlg.close()
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 11b) isDirty() ohne Commit-Nebenwirkung (F-919, Karte #525)
// ─────────────────────────────────────────────────────────────────────────

describe('Modal — isDirty() committet keinen aktiven Typora-Block mehr (F-919)', () => {
  /* Vor dem Fix riefen isDirty() (→ beforeunload UND closeWithAutosave)
     computeFormPatch() OHNE Unterscheidung auf — genau wie ein echtes
     Speichern. Bei einer offenen, noch nicht per blur() abgeschlossenen
     Neu-Block-Textarea (`.typora-new`) ist `commitNewBlock()` aber NICHT
     idempotent: jeder Aufruf hängt `ta.value` erneut an `panes[key].text`
     an. Drei Aufrufe vor dem eigentlichen Speichern — beforeunload →
     closeWithAutosave()s eigener isDirty()-Check → saveAndClose()s
     computeFormPatch() — committeten vor dem Fix DREIMAL denselben Text,
     das Ergebnis war verdreifacht statt einmal übernommen. Diese Gegenprobe
     ist deshalb kein synthetischer Spy-Test, sondern der real emittierte
     Kartenzustand. */
  it('Tab-Schluss-Versuch (beforeunload) gefolgt von echtem Schliessen dupliziert den Agent-Text NICHT', () => {
    boardModeValue = 'project'
    const overlay = openModal(makeCard({ owner: 'Claude', agentContext: '' }))

    // Agent-Tab öffnen und den leeren Platzhalter anklicken → Neu-Block-Editor
    overlay.querySelector('#tab-agent').click()
    const agentPane = overlay.querySelector('#agent-pane')
    const placeholder = agentPane.querySelector('.typora-placeholder')
    expect(placeholder, 'Platzhalter für leeren Agent-Kontext nicht gefunden').toBeTruthy()
    placeholder.click()

    const ta = agentPane.querySelector('.typora-new')
    expect(ta, 'Neu-Block-Textarea nach Platzhalter-Klick nicht gefunden').toBeTruthy()
    ta.value = 'Neuer Agent-Text'
    // BEWUSST kein ta.blur() — der Block bleibt "aktiv in Bearbeitung",
    // genau der Fall, den isDirty() bisher fälschlich committete.

    // Erster isDirty()-Aufruf: simulierter (später abgebrochener) Tab-Schluss.
    const unloadEv = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(unloadEv)

    // Zweiter, tatsächlicher Schliessweg (X-Knopf → closeWithAutosave() →
    // isDirty()-Check + saveAndClose()).
    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('.btn-modal-close').click()
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].agentContext).toBe('Neuer Agent-Text')
    } finally {
      EventBus.off('card:updated', spy)
    }
  })
})

describe('Modal — isDirty ist unempfindlich gegen Schlüsselreihenfolge (Kritiker-Befund Sprint 63)', () => {
  /* `recurrence` ist das einzige Feld, das computeFormPatch() als frisch
     gebautes Objekt liefert. Ein Import mit abweichender Schlüsselreihenfolge
     machte die Karte damit beim blossen Öffnen „dirty" — jedes Schliessen
     schrieb eine Op und einen Aktivitätseintrag, ohne dass etwas geändert
     wurde. Gemessen vor dem Fix: card:updated wurde 1× emittiert. */
  it('eine wiederkehrende Karte wird beim blossen Ansehen nicht gespeichert', () => {
    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    const overlay = openModal(makeCard({
      recurrence: { interval: 'weekly', day: 1, lastSpawned: '2026-08-01' },
    }))
    /* Nichts anfassen — nur schliessen. */
    overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(spy, 'ohne Änderung darf nichts gespeichert werden').not.toHaveBeenCalled()
  })

  /* Die Gegenrichtung: eine ECHTE Änderung an der Wiederholung muss weiterhin
     erkannt werden — sonst hätte der Fix den Dirty-Check stillgelegt. */
  /* Die Gegenrichtung: eine ECHTE Änderung an der Wiederholung muss weiterhin
     erkannt werden — sonst hätte der Fix den Dirty-Check stillgelegt.
     WICHTIG (QA-Befund Sprint 63): NUR die Wiederholung ändern, nicht
     zusätzlich den Titel. Die erste Fassung dieses Tests änderte beides und
     prüfte dann, dass gespeichert wurde — eine Regression, die ausschliesslich
     die Recurrence-Erkennung kaputt macht, wäre daran vorbeigelaufen, weil die
     Titeländerung den Dirty-Check ohnehin auslöst. Ein Test, der aus zwei
     Gründen grün sein kann, sichert keinen davon zu. */
  it('erkennt eine echte Änderung an der Wiederholung weiterhin', () => {
    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    const overlay = openModal(makeCard({ recurrence: { interval: 'weekly', lastSpawned: null } }))
    /* Der Selektor heisst `#modal-recurrence-INTERVAL` (src/modal.js:2364) —
       die erste Fassung griff `#modal-recurrence` und fand `null`. Zusammen
       mit dem `if (sel)` davor hiess das: Der Test hat die Wiederholung nie
       angefasst und war allein wegen der Titeländerung grün. */
    const sel = overlay.querySelector('#modal-recurrence-interval')
    expect(sel, 'ohne das Wiederholungs-Feld prüft dieser Test nichts').toBeTruthy()
    sel.value = 'monthly'
    sel.dispatchEvent(new Event('change', { bubbles: true }))
    /* Titel BEWUSST unangetastet. */
    overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(spy, 'eine geänderte Wiederholung allein muss gespeichert werden').toHaveBeenCalledTimes(1)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 12) F-915 — Archivieren/Spam speichern offene Formular-Änderungen mit
// ─────────────────────────────────────────────────────────────────────────

/* Nebenbefund aus Karte #463: Beide Knöpfe schliessen das Modal (close() am
   Ende ihrer jeweiligen Funktion), bauten ihr card:updated aber bislang NUR
   aus dem State (`base`) — kein Formularfeld floss ein. Orchestrator-
   Entscheid "Weg 1": mitspeichern über dieselbe computeFormPatch()-Basis wie
   saveAndClose()/closeWithAutosave(). Zusicherungen laufen wie in Sektion 11
   NIE auf den DOM-Feldinhalt, sondern auf den über card:updated EMITTIERTEN
   Zustand. */

describe('Modal — archiveAndClose speichert offene Formular-Änderungen mit (F-915, Nebenbefund #463)', () => {
  afterEach(() => { State.set(null) })

  it('Titeländerung im Formular wird beim Klick auf Archivieren mitgespeichert, nicht verworfen', () => {
    const card = makeCard({ title: 'Alter Titel', columnId: 'col-erledigt', archived: false })
    State.set({
      version: '2',
      board: { id: 'b1', columnDefs: [], sprints: [{ id: 's1', cards: [card] }] },
    })
    const overlay = openModal(card)
    overlay.querySelector('#modal-title').value = 'Neuer Titel vor dem Archivieren'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('#modal-archive-btn').click()
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].title).toBe('Neuer Titel vor dem Archivieren')
      expect(spy.mock.calls[0][0].archived).toBe(true)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  /* Gegenprobe (Fehlerklasse 68a): der legitime Fall — keine offene
     Bearbeitung — darf durch den Fix nicht verändert werden. Titel bleibt
     unangetastet, Archivieren funktioniert unverändert korrekt. */
  it('Ohne offene Änderung bleibt der Titel unangetastet, Archivieren funktioniert weiterhin (Gegenprobe)', () => {
    const card = makeCard({ title: 'Unveränderter Titel', columnId: 'col-erledigt', archived: false })
    State.set({
      version: '2',
      board: { id: 'b1', columnDefs: [], sprints: [{ id: 's1', cards: [card] }] },
    })
    const overlay = openModal(card)

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('#modal-archive-btn').click()
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].title).toBe('Unveränderter Titel')
      expect(spy.mock.calls[0][0].archived).toBe(true)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  /* Der Titel-Guard aus demselben Fix — er war zunächst ungetestet, und ein
     Fix ohne Gegenprobe ist die Fehlerklasse, die der Verify-Lauf zu ADR-0115
     ausdrücklich sucht. Drei Zusicherungen in einem Test, weil sie erst
     zusammen die Entscheidung belegen: NICHT abbrechen (wie saveAndClose),
     NICHT alles verwerfen (wie closeWithAutosave), sondern genau den einen
     ungültigen Schlüssel fallen lassen. Wer den Guard entfernt, macht die
     Titel-Zusicherung rot; wer ihn zum Abbruch ausbaut, die beiden anderen. */
  it('leerer Titel blockiert das Archivieren nicht — nur der Titel-Patch wird verworfen, der Rest gespeichert', () => {
    const card = makeCard({ title: 'Titel bleibt', columnId: 'col-erledigt', archived: false })
    State.set({
      version: '2',
      board: { id: 'b1', columnDefs: [], sprints: [{ id: 's1', cards: [card] }] },
    })
    const overlay = openModal(card)
    overlay.querySelector('#modal-title').value = '   '
    /* Ein ZWEITES, echtes Formularfeld ändern — sonst prüft Zusicherung 3
       nichts. Es muss ein Feld sein, das es wirklich gibt: Die erste Fassung
       dieses Tests griff auf `#modal-desc` zu, und dieses Element existiert
       nirgends (die Beschreibung ist ein Typora-Pane, `#desc-pane`). Die
       Zusicherung stand hinter `if (descFeld)` und lief deshalb nie — der
       Test war grün, ohne den Teil zu belegen, für den er geschrieben wurde.
       Gefunden vom Sprint-65-Kritiker ([[F-964]]); das ist exakt die
       Fehlerklasse, die dieser Test bewachen soll, einmal am eigenen Leib. */
    const labelsFeld = overlay.querySelector('#modal-labels')
    expect(labelsFeld, 'ohne Labels-Feld prüft dieser Test seinen dritten Punkt nicht').toBeTruthy()
    labelsFeld.value = 'darf-nicht-verlorengehen'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('#modal-archive-btn').click()
      /* 1. Die Aktion wurde NICHT abgebrochen … */
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].archived).toBe(true)
      /* 2. … der alte Titel steht noch … */
      expect(spy.mock.calls[0][0].title).toBe('Titel bleibt')
      /* 3. … und die andere Formularänderung ist trotzdem mitgekommen. */
      expect(spy.mock.calls[0][0].labels).toContain('darf-nicht-verlorengehen')
    } finally {
      EventBus.off('card:updated', spy)
    }
  })
})

describe('Modal — toggleSpamAndClose speichert offene Formular-Änderungen mit (F-915, Nebenbefund #463)', () => {
  const origFetch = globalThis.fetch

  afterEach(() => {
    State.set(null)
    globalThis.fetch = origFetch
  })

  /* #modal-spam-btn existiert nur im Server-Build (IS_SERVER_BUILD-Gate in
     modal.js, siehe "Cross-Board-Transfer-Button"-Block weiter oben in
     dieser Datei für dasselbe Muster) — deshalb dieselbe isServerBuildCtx-
     Bedingung wie dort. */
  it.skipIf(!isServerBuildCtx)('Titeländerung im Formular wird beim Klick auf "Als Spam markieren" mitgespeichert', async () => {
    const card = makeCard({
      title: 'Alter Titel', columnId: 'col-inbox', senderEmail: 'spammer@example.test',
    })
    State.set({
      version: '2',
      board: { id: 'b1', columnDefs: [], sprints: [{ id: 's1', kind: 'service', cards: [card] }] },
    })
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true })

    const overlay = openModal(card)
    overlay.querySelector('#modal-title').value = 'Neuer Titel vor Spam-Markierung'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('#modal-spam-btn').click()
      // toggleSpamAndClose ist async (ein await auf fetch) — Mikrotasks abwarten.
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].title).toBe('Neuer Titel vor Spam-Markierung')
      expect(spy.mock.calls[0][0].columnId).toBe('col-spam')
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  /* Gegenprobe (Fehlerklasse 68a): der legitime Fall ohne offene Bearbeitung
     bleibt unverändert korrekt. */
  it.skipIf(!isServerBuildCtx)('Ohne offene Änderung bleibt der Titel unangetastet, Spam-Markierung funktioniert weiterhin (Gegenprobe)', async () => {
    const card = makeCard({
      title: 'Unveränderter Titel', columnId: 'col-inbox', senderEmail: 'spammer@example.test',
    })
    State.set({
      version: '2',
      board: { id: 'b1', columnDefs: [], sprints: [{ id: 's1', kind: 'service', cards: [card] }] },
    })
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true })

    const overlay = openModal(card)

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('#modal-spam-btn').click()
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
      expect(spy).toHaveBeenCalledTimes(1)
      expect(spy.mock.calls[0][0].title).toBe('Unveränderter Titel')
      expect(spy.mock.calls[0][0].columnId).toBe('col-spam')
    } finally {
      EventBus.off('card:updated', spy)
    }
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 13) F-918 / Karte #482 — Auto-Save rollt keine fremde Änderung zurück
// ─────────────────────────────────────────────────────────────────────────

/* Kritiker-Befund zum Sprint-63-Abschluss: Seit Karte #463 speichern fünf
   Wege beim Schliessen, drei davon versehentlich (Backdrop, ESC, Swipe).
   Emittiert wurde `{ ...currentCard, ...patch }` — `currentCard` ist der
   Stand BEIM ÖFFNEN, und main.js ersetzt die Karte im State vollständig.
   Trifft zwischen Öffnen und Schliessen eine fremde Änderung ein (zweiter
   Tab, anderer Client, Projektor-Lauf), rollt der Auto-Save sie zurück.

   Zusicherungen laufen wie in den Sektionen 11/12 NIE auf den DOM-Feldinhalt
   (der bleibt nach close() stehen und wäre auch ohne Fix grün — F-440),
   sondern auf den über `card:updated` EMITTIERTEN Zustand.

   Aufbau jedes Tests: Das Modal wird mit einer KOPIE der Karte geöffnet
   (= Schnappschuss), danach steht im State eine fremd geänderte Fassung —
   genau das, was der Sync-Poll bei offenem Modal tut. */

describe('Modal — Auto-Save überschreibt keine fremde Änderung (F-918, Karte #482)', () => {
  afterEach(() => { State.set(null) })

  function setzeState(karte) {
    State.set({
      version: '2',
      board: { id: 'b1', columnDefs: [], sprints: [{ id: 's1', cards: [karte] }] },
    })
  }

  /** Modal auf dem Schnappschuss öffnen, danach den State fremd fortschreiben. */
  function oeffneUndFremdAendern(schnappschuss, fremdeAenderung) {
    setzeState({ ...schnappschuss })
    const overlay = openModal({ ...schnappschuss })
    setzeState({ ...schnappschuss, ...fremdeAenderung })
    return overlay
  }

  it('das Hauptszenario: fremder Spalten-Wechsel, qa und Kommentar überleben einen Backdrop-Klick mit eigener Titeländerung', () => {
    const fremderKommentar = {
      id: 'c-fremd', author: 'B', text: 'Von B waehrenddessen angehaengt',
      created: '2026-08-31',
    }
    const overlay = oeffneUndFremdAendern(
      makeCard({ title: 'Alter Titel', qa: false, columnId: 'col-backlog', comments: [] }),
      { qa: true, columnId: 'col-erledigt', comments: [fremderKommentar] },
    )
    overlay.querySelector('#modal-title').value = 'Titel von A'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      const emittiert = spy.mock.calls[0][0]
      /* Die eigene Änderung ist da … */
      expect(emittiert.title).toBe('Titel von A')
      /* … und die fremden drei sind es auch. */
      expect(emittiert.qa, 'fremdes qa:true darf nicht zurückfallen').toBe(true)
      expect(emittiert.columnId, 'fremder Spalten-Wechsel darf nicht zurückfallen').toBe('col-erledigt')
      expect(emittiert.comments.map(k => k.id)).toEqual(['c-fremd'])
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('ein Feld, das NUR fremd geändert wurde, behält den fremden Wert — auch wenn es im Formular steht', () => {
    /* Der Kern der Regel: nicht „frisch gewinnt immer", sondern „Basis frisch,
       darüber nur die Felder, die der Nutzer angefasst hat". Hier fasst der
       Nutzer NUR den Titel an; owner/priority/labels wurden fremd geändert und
       müssen unangetastet durchkommen, obwohl das Formular für sie noch die
       alten Werte anzeigt. */
    const overlay = oeffneUndFremdAendern(
      makeCard({ title: 'Alter Titel', owner: '', priority: 'mittel', labels: [] }),
      { owner: 'Claude Opus', priority: 'hoch', labels: ['fremd'] },
    )
    overlay.querySelector('#modal-title').value = 'Nur der Titel von A'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      const emittiert = spy.mock.calls[0][0]
      expect(emittiert.title).toBe('Nur der Titel von A')
      expect(emittiert.owner).toBe('Claude Opus')
      expect(emittiert.priority).toBe('hoch')
      expect(emittiert.labels).toEqual(['fremd'])
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('Gegenrichtung — ändern beide DASSELBE Feld, gewinnt der Schliessende (dokumentierter Restfall, kein Zufall)', () => {
    /* Last-Write-Wins auf FELD-Ebene. Das ist die bewusst gewählte Grenze des
       Fixes (siehe Kommentar bei nurTatsaechlichGeaenderte in modal.js): eine
       echte Auflösung bräuchte Feld-Versionen oder einen Konflikt-Dialog.
       Wichtig ist, dass der Verlust auf das eine umkämpfte Feld begrenzt
       bleibt — deshalb prüft dieser Test zusätzlich, dass die fremde Änderung
       am NACHBAR-Feld denselben Vorgang überlebt. */
    const overlay = oeffneUndFremdAendern(
      makeCard({ title: 'Alter Titel', qa: false }),
      { title: 'Titel von B', qa: true },
    )
    overlay.querySelector('#modal-title').value = 'Titel von A'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      const emittiert = spy.mock.calls[0][0]
      expect(emittiert.title, 'Restfall: bei echtem Feld-Konflikt gewinnt der Schliessende').toBe('Titel von A')
      expect(emittiert.qa, 'der Konflikt bleibt auf das umkämpfte Feld begrenzt').toBe(true)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('ein eigener, noch nicht abgeschickter Kommentar wird an die FREMDE Kommentarliste angehängt, nicht an die vom Öffnen', () => {
    const fremderKommentar = {
      id: 'c-fremd', author: 'B', text: 'Von B', created: '2026-08-31',
    }
    const overlay = oeffneUndFremdAendern(
      makeCard({ comments: [] }),
      { comments: [fremderKommentar] },
    )
    overlay.querySelector('#comment-input').value = 'Von A im offenen Modal'
    overlay.querySelector('.comment-form button').click()

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      const texte = spy.mock.calls[0][0].comments.map(k => k.text)
      expect(texte).toContain('Von B')
      expect(texte).toContain('Von A im offenen Modal')
      expect(texte).toHaveLength(2)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  /* Gegenprobe (Fehlerklasse 68a): ohne fremde Änderung darf sich nichts am
     bisherigen Verhalten ändern — sonst repariert der Fix den seltenen Fall
     auf Kosten des Normalfalls. */
  it('Gegenprobe: ohne fremde Änderung verhält sich der Auto-Save unverändert', () => {
    const karte = makeCard({ title: 'Alter Titel', qa: false, columnId: 'col-backlog' })
    setzeState({ ...karte })
    const overlay = openModal({ ...karte })
    overlay.querySelector('#modal-title').value = 'Neuer Titel'
    const qaCb = overlay.querySelector('#modal-qa')
    if (qaCb) qaCb.checked = true

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      const emittiert = spy.mock.calls[0][0]
      expect(emittiert.title).toBe('Neuer Titel')
      expect(emittiert.qa).toBe(true)
      expect(emittiert.columnId).toBe('col-backlog')
      expect(emittiert.id).toBe(karte.id)
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  /* Der Auto-Save läuft auch ohne State-Eintrag (Board noch nicht geladen,
     Karte inzwischen gelöscht) — frischeBasis() fällt dann auf currentCard
     zurück. Ohne diesen Rückfall entstünde aus {...null} eine leere Karte. */
  it('ohne auffindbare Karte im State bleibt der Schnappschuss die Basis (kein leeres card:updated)', () => {
    State.set(null)
    const overlay = openModal(makeCard({ title: 'Alter Titel', columnId: 'col-backlog' }))
    overlay.querySelector('#modal-title').value = 'Neuer Titel ohne State'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      const emittiert = spy.mock.calls[0][0]
      expect(emittiert.id).toBe('card-test-001')
      expect(emittiert.title).toBe('Neuer Titel ohne State')
      expect(emittiert.columnId).toBe('col-backlog')
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('Checkbox-Toggle in der Beschreibung (Sofort-Speicherung, F-41) rollt ebenfalls nichts zurück', () => {
    /* Derselbe Defekt an einem zweiten Pfad: toggleChecklistLine() speichert
       ohne Speichern-Klick und ersetzte die Karte im State ebenfalls durch den
       Öffnungs-Schnappschuss. */
    const overlay = oeffneUndFremdAendern(
      makeCard({ description: '- [ ] Aufgabe', qa: false, columnId: 'col-backlog' }),
      { qa: true, columnId: 'col-erledigt' },
    )
    /* Karte #817: #desc-pane statt #tab-panel-desc — die Flag-Pillen (auch
       Checkboxen) stehen seitdem oben im Details-Tab, vor der Beschreibung. */
    const box = overlay.querySelector('#desc-pane input[type="checkbox"]')
    expect(box, 'ohne gerenderte Checkbox prüft dieser Test nichts').toBeTruthy()

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      box.checked = true
      box.dispatchEvent(new Event('change', { bubbles: true }))
      expect(spy).toHaveBeenCalledTimes(1)
      const emittiert = spy.mock.calls[0][0]
      expect(emittiert.description).toContain('- [x]')
      expect(emittiert.qa).toBe(true)
      expect(emittiert.columnId).toBe('col-erledigt')
    } finally {
      EventBus.off('card:updated', spy)
    }
  })

  it('archiveAndClose rollt ebenfalls nichts zurück — fremdes qa überlebt das Archivieren mit offener Titeländerung', () => {
    const overlay = oeffneUndFremdAendern(
      makeCard({ title: 'Alter Titel', columnId: 'col-erledigt', archived: false, qa: false, owner: '' }),
      { qa: true, owner: 'Claude Opus' },
    )
    overlay.querySelector('#modal-title').value = 'Titel von A vor dem Archivieren'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('#modal-archive-btn').click()
      expect(spy).toHaveBeenCalledTimes(1)
      const emittiert = spy.mock.calls[0][0]
      expect(emittiert.title).toBe('Titel von A vor dem Archivieren')
      expect(emittiert.archived).toBe(true)
      expect(emittiert.qa).toBe(true)
      expect(emittiert.owner).toBe('Claude Opus')
    } finally {
      EventBus.off('card:updated', spy)
    }
  })
})

/* Dritter Schliessweg mit demselben Nebenläufigkeitsproblem — eigener
   describe wegen des fetch-Mocks (Server-Build-only, wie die F-915-Sektion
   darüber). Anders als bei saveAndClose() war hier schon vor F-918 der
   frische Stand die Basis (`found` wird beim KLICK gelesen, nicht beim
   Öffnen); zurückgerollt hat trotzdem der Patch, weil er alle
   Formularfelder mit den Werten vom Öffnen mitbrachte. */
describe('Modal — toggleSpamAndClose rollt keine fremde Änderung zurück (F-918, Karte #482)', () => {
  const origFetch = globalThis.fetch

  afterEach(() => {
    State.set(null)
    globalThis.fetch = origFetch
  })

  function setzeServiceState(karte) {
    State.set({
      version: '2',
      board: { id: 'b1', columnDefs: [], sprints: [{ id: 's1', kind: 'service', cards: [karte] }] },
    })
  }

  it.skipIf(!isServerBuildCtx)('fremdes qa und fremder Owner überleben die Spam-Markierung mit offener Titeländerung', async () => {
    const schnappschuss = makeCard({
      title: 'Alter Titel', columnId: 'col-inbox', senderEmail: 'spammer@example.test',
      qa: false, owner: '',
    })
    setzeServiceState({ ...schnappschuss })
    globalThis.fetch = vi.fn().mockResolvedValue({ ok: true })

    const overlay = openModal({ ...schnappschuss })
    /* Fremde Änderung nach dem Öffnen — genau das, was der Sync-Poll tut. */
    setzeServiceState({ ...schnappschuss, qa: true, owner: 'Claude Opus' })
    overlay.querySelector('#modal-title').value = 'Titel von A vor Spam-Markierung'

    const spy = vi.fn()
    EventBus.on('card:updated', spy)
    try {
      overlay.querySelector('#modal-spam-btn').click()
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
      expect(spy).toHaveBeenCalledTimes(1)
      const emittiert = spy.mock.calls[0][0]
      expect(emittiert.title).toBe('Titel von A vor Spam-Markierung')
      expect(emittiert.columnId).toBe('col-spam')
      expect(emittiert.qa).toBe(true)
      expect(emittiert.owner).toBe('Claude Opus')
    } finally {
      EventBus.off('card:updated', spy)
    }
  })
})

/* ── Karte #639 (F-1264): „bearbeitet“-Vermerk am Kommentar ───────────── */

describe('Modal — Kommentar zeigt Bearbeitungsvermerk (#639)', () => {
  const k = (id, extra = {}) => ({ id, author: 'agent:bert', text: 'Text', created: '2026-10-03', ...extra })
  it('mit edited: Vermerk "bearbeitet" mit Zeit im title; ohne edited: keiner', () => {
    const overlay = openModal(makeCard({ comments: [k('c1', { edited: '2026-10-03T12:00:00.000Z' }), k('c2')] }))
    const items = overlay.querySelectorAll('.comment-item')
    expect(items.length).toBe(2)
    const v = items[0].querySelector('.comment-edited')
    expect(v).toBeTruthy()
    expect(v.textContent).toMatch(/bearbeitet|edited/)
    expect(v.title).toBe('2026-10-03T12:00:00.000Z')
    expect(items[1].querySelector('.comment-edited')).toBeNull()
  })
})
