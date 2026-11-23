/* @vitest-environment happy-dom */

/**
 * F-749 — Regression durch #348: main.js#onExternalUpdate/#showSyncBadge
 * hielten externe Board-Updates zurück, solange IRGENDEIN migrierter
 * Settings-Dialog offen war — nicht nur das Karten-Modal.
 *
 * Wurzel: Beide Stellen nutzten `.modal-overlay[aria-hidden="false"]`. Bis
 * #348 hatten die sieben Settings-Dialoge (notif-overlay, cp-overlay, ...)
 * eigene Overlay-Klassen OHNE `.modal-overlay` als Basisklasse — der
 * Selektor traf nur das Karten-Modal (src/modal.js) und den
 * Einstellungen-Dialog (src/configdialog.js), die die Basisklasse schon
 * vorher trugen (am Parent-Commit 3d96506 nachgemessen). Seit #348 teilen
 * sich ALLE Settings-Dialoge über modal-shell.js dieselbe Basisklasse
 * `.modal-overlay` — der Selektor fing sie seither versehentlich mit ein:
 * Notifications-Dialog offen + Kollege ändert das Board → Update bleibt
 * hängen, bis der Nutzer den Notifications-Dialog schließt.
 *
 * Fix: eigene Marker-Klasse `modal-overlay--blocks-sync` NUR an den zwei
 * Dialogen, die das Zurückhalten tatsächlich auslösen sollen (Karten-Modal
 * + Einstellungen). main.js selektiert seitdem darüber.
 *
 * WICHTIG (Spiegel-Test-Falle, siehe tests/sync-badge-noop-guard.test.js):
 * Dieser Test baut den Selektor NICHT nach und injiziert `modalOpen` nicht
 * als Parameter — er wendet den ECHTEN Selektor-String aus main.js auf DOM
 * an, das von den ECHTEN Produktions-Baufunktionen erzeugt wird
 * (Modal.open() via EventBus 'card:edit' aus src/modal.js, createModal()
 * aus src/modal-shell.js — exakt das Muster, das jeder migrierte
 * Settings-Dialog nutzt). main.js selbst ist NICHT importierbar (Side-
 * Effekt-Bootstrap, siehe Konvention in tests/swimlane-kind.test.js:316 /
 * sync-badge-noop-guard.test.js) — der Selektor-STRING ist deshalb wörtlich
 * aus src/main.js kopiert, aber die DOM-Seite ist real.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/swipe.js', () => ({
  addSwipeToDismiss: vi.fn(),
}))

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

import { EventBus } from '../src/eventbus.js'
import { Modal }    from '../src/modal.js'
import { createModal } from '../src/modal-shell.js'

/* Wörtlich aus src/main.js (onExternalUpdate + showSyncBadge, F-749-Fix). */
const SYNC_BLOCK_SELECTOR = '.modal-overlay--blocks-sync[aria-hidden="false"]'
/* Der ALTE, zu weite Selektor — als Regressions-Beleg mitgeführt. */
const OLD_BASE_CLASS_SELECTOR = '.modal-overlay[aria-hidden="false"]'

function makeCard(overrides = {}) {
  return {
    id: 'card-sel-1', displayId: 1, title: 'T', description: '', agentContext: '',
    comments: [], attachments: [], links: [], activity: [], priority: 'mittel',
    labels: [], owner: '', complexity: '', effort: null, qa: false,
    critical: false, blocked: false, archived: false, columnId: 'col-backlog',
    ...overrides,
  }
}

/** Modal-Singleton-Overlay entfernen und neu aufbauen (zwischen Tests) —
 * wie tests/modal.dom.test.js#resetModal. */
function resetModal() {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
}

beforeEach(() => {
  boardModeValue = 'standalone'
  vi.useFakeTimers() // Modal.open() nutzt rAF für die Einblend-Klasse
  resetModal()
})

afterEach(() => {
  resetModal()
  vi.useRealTimers()
})

describe('F-749 — echter Sync-Block-Selektor nach #348', () => {
  it('kein Dialog offen → Selektor trifft nicht', () => {
    expect(document.querySelector(SYNC_BLOCK_SELECTOR)).toBeNull()
  })

  it('Karten-Modal offen (echtes Modal.open() via card:edit) → Selektor trifft', () => {
    EventBus.emit('card:edit', makeCard())
    const overlay = document.querySelector('.modal-overlay')
    expect(overlay).toBeTruthy()
    expect(overlay.getAttribute('aria-hidden')).toBe('false')
    expect(document.querySelector(SYNC_BLOCK_SELECTOR)).toBe(overlay)
  })

  it('Settings-Dialog offen (createModal(), Muster jedes der 7 migrierten Dialoge) → Selektor trifft NICHT', () => {
    const shell = createModal({
      extraOverlayClass: 'notif-overlay',
      extraPanelClass:   'notif-panel',
      titleId:           'notif-title',
      closeAriaLabel:    'Schließen',
      variant:           'settings',
    })
    document.body.appendChild(shell.overlay)
    shell.open()
    expect(shell.overlay.getAttribute('aria-hidden')).toBe('false')

    /* Der Fix trifft NICHT: */
    expect(document.querySelector(SYNC_BLOCK_SELECTOR)).toBeNull()

    /* Regressions-Beleg: der alte, zu weite Basisklassen-Selektor HÄTTE
       hier fälschlich getroffen — das war genau der F-749-Bug. */
    expect(document.querySelector(OLD_BASE_CLASS_SELECTOR)).toBe(shell.overlay)
  })

  it('Karten-Modal UND Settings-Dialog gleichzeitig offen → Selektor trifft nur das Karten-Modal', () => {
    EventBus.emit('card:edit', makeCard())
    const cardOverlay = document.querySelector('.modal-overlay--blocks-sync')

    const shell = createModal({
      extraOverlayClass: 'admin-overlay',
      extraPanelClass:   'admin-panel',
      titleId:           'admin-title',
      closeAriaLabel:    'Schließen',
      variant:           'settings',
    })
    document.body.appendChild(shell.overlay)
    shell.open()

    expect(document.querySelector(SYNC_BLOCK_SELECTOR)).toBe(cardOverlay)
  })
})
