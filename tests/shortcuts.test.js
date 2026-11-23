/* @vitest-environment happy-dom */
/**
 * Unit-Tests: shortcuts.js — Tastatur-Shortcut-System (ADR-0035)
 *
 * Geprüft werden:
 *  - setContext / getContext
 *  - Kontext-Wechsel sperrt globale Shortcuts
 *  - Eingabe-Guard (Input/Textarea/ContentEditable)
 *  - IME-Composition-Guard
 *  - j/k-Navigation: Card-Highlight-Klasse
 *  - Esc demarkiert aktive Karte
 *  - Help-Overlay öffnen/schließen
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { Shortcuts } from '../src/shortcuts.js'

/* ── Hilfsfunktionen ─────────────────────────────────────────────── */

function fireKey(key, target = document, opts = {}) {
  const e = new KeyboardEvent('keydown', {
    key,
    bubbles:    true,
    cancelable: true,
    isComposing: opts.isComposing ?? false,
    ctrlKey:    opts.ctrlKey ?? false,
    metaKey:    opts.metaKey ?? false,
    altKey:     opts.altKey  ?? false,
    ...opts,
  })
  Object.defineProperty(e, 'target', { value: target, writable: false })
  document.dispatchEvent(e)
  return e
}

/* Karten-DOM in document.body bauen */
function buildCards(n = 3) {
  document.body.innerHTML = ''
  for (let i = 0; i < n; i++) {
    const card = document.createElement('div')
    card.className = 'card'
    card.dataset.id = String(i + 1)
    /* offsetParent braucht ein sichtbares Elternelement — happy-dom ist hier
       begrenzt, also prüfen wir offsetParent nicht direkt. Wir mock-en die
       getVisibleCards-Logik indirekt durch das Vorhandensein der Elemente. */
    document.body.appendChild(card)
  }
  return [...document.querySelectorAll('.card')]
}

/* ── Setup / Teardown ────────────────────────────────────────────── */

beforeEach(() => {
  /* Shortcut-System initialisieren (einmalig; init() ist idempotent) */
  Shortcuts.init()
  /* Kontext zurücksetzen */
  Shortcuts.setContext('board')
  /* Help-Overlay sicherheitshalber schließen */
  Shortcuts.closeHelp()
  /* DOM leeren */
  document.body.innerHTML = ''
})

afterEach(() => {
  Shortcuts.closeHelp()
  document.body.innerHTML = ''
})

/* ═══════════════════════════════════════════════════════════════════
   1) Kontext-API
   ═══════════════════════════════════════════════════════════════════ */

describe('Shortcuts — Kontext-API', () => {
  it('Standard-Kontext ist "board"', () => {
    expect(Shortcuts.getContext()).toBe('board')
  })

  it('setContext("modal") wechselt den Kontext', () => {
    Shortcuts.setContext('modal')
    expect(Shortcuts.getContext()).toBe('modal')
  })

  it('setContext("board") wechselt zurück', () => {
    Shortcuts.setContext('modal')
    Shortcuts.setContext('board')
    expect(Shortcuts.getContext()).toBe('board')
  })
})

/* ═══════════════════════════════════════════════════════════════════
   2) Help-Overlay
   ═══════════════════════════════════════════════════════════════════ */

describe('Shortcuts — Help-Overlay', () => {
  it('? öffnet das Help-Overlay (DOM-Element vorhanden)', () => {
    fireKey('?')
    expect(document.querySelector('.shortcuts-overlay')).toBeTruthy()
  })

  it('closeHelp() entfernt das Overlay aus dem DOM', () => {
    fireKey('?')
    Shortcuts.closeHelp()
    expect(document.querySelector('.shortcuts-overlay')).toBeNull()
  })

  it('openHelp() erneut aufrufen schließt das Overlay (Toggle)', () => {
    /* Erstes Öffnen via Shortcut */
    fireKey('?')
    expect(document.querySelector('.shortcuts-overlay')).toBeTruthy()
    /* Zweites Öffnen via direktem API-Call (Toggle) */
    Shortcuts.openHelp()
    expect(document.querySelector('.shortcuts-overlay')).toBeNull()
  })

  it('Esc schließt das Overlay wenn es offen ist', () => {
    fireKey('?')
    /* Esc im Overlay selbst */
    const overlayEl = document.querySelector('.shortcuts-overlay')
    if (overlayEl) {
      const escEvt = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
      overlayEl.dispatchEvent(escEvt)
    }
    expect(document.querySelector('.shortcuts-overlay')).toBeNull()
  })
})

/* ═══════════════════════════════════════════════════════════════════
   3) Modal-Kontext sperrt Shortcuts
   ═══════════════════════════════════════════════════════════════════ */

describe('Shortcuts — Modal-Kontext', () => {
  it('? öffnet kein Overlay im modal-Kontext', () => {
    Shortcuts.setContext('modal')
    fireKey('?')
    expect(document.querySelector('.shortcuts-overlay')).toBeNull()
  })

  it('n feuert nicht im modal-Kontext (kein "Hinzufügen"-Klick)', () => {
    Shortcuts.setContext('modal')
    const addBtn = document.createElement('button')
    addBtn.className = 'btn-add-card'
    const clicked = vi.fn()
    addBtn.addEventListener('click', clicked)
    document.body.appendChild(addBtn)

    fireKey('n')
    expect(clicked).not.toHaveBeenCalled()
  })
})

/* ═══════════════════════════════════════════════════════════════════
   4) Eingabe-Guard
   ═══════════════════════════════════════════════════════════════════ */

describe('Shortcuts — Eingabe-Guard', () => {
  it('? feuert nicht wenn ein Input fokussiert ist', () => {
    const input = document.createElement('input')
    input.tagName  /* read-only, bereits gesetzt */
    document.body.appendChild(input)

    /* Simuliere: target ist das Input-Element */
    const e = new KeyboardEvent('keydown', { key: '?', bubbles: true, cancelable: true })
    Object.defineProperty(e, 'target', { value: input, writable: false })
    document.dispatchEvent(e)

    expect(document.querySelector('.shortcuts-overlay')).toBeNull()
  })

  it('? feuert nicht bei isComposing === true', () => {
    fireKey('?', document, { isComposing: true })
    expect(document.querySelector('.shortcuts-overlay')).toBeNull()
  })

  it('? feuert nicht bei Ctrl+?', () => {
    fireKey('?', document, { ctrlKey: true })
    expect(document.querySelector('.shortcuts-overlay')).toBeNull()
  })
})

/* ═══════════════════════════════════════════════════════════════════
   5) Karten-Navigation j/k
   ═══════════════════════════════════════════════════════════════════ */

describe('Shortcuts — j/k Navigation', () => {
  it('j ohne Karten im DOM macht nichts (kein Fehler)', () => {
    expect(() => fireKey('j')).not.toThrow()
  })

  it('onBoardRender() ohne aktive Karte macht nichts (kein Fehler)', () => {
    expect(() => Shortcuts.onBoardRender()).not.toThrow()
  })
})

/* ═══════════════════════════════════════════════════════════════════
   6) Esc-Verhalten
   ═══════════════════════════════════════════════════════════════════ */

describe('Shortcuts — Esc', () => {
  it('Esc wirft keinen Fehler wenn kein Zustand vorhanden', () => {
    expect(() => fireKey('Escape')).not.toThrow()
  })
})

/* ═══════════════════════════════════════════════════════════════════
   7) F-157 — Sprint-Meta-Karten in der j/k-Navigation
   ═══════════════════════════════════════════════════════════════════ */

/* F-157 fragte, ob `getVisibleCards()` Sprint-Meta-Karten herausfiltern muss.
   Entscheidung (Sprint 61, ADR-0035 äusserte sich nicht): NEIN — j/k führt zu
   `Enter` = Karte öffnen, und Öffnen ist bei Meta-Karten ausdrücklich erlaubt
   (Beschreibung + Kommentare sind editierbar; dort liegen die Übergabe-Notizen).
   Ein Filter hier machte genau die Karte tastaturunerreichbar, die per Tastatur
   gebraucht wird. Ctrl+A schliesst sie dagegen aus — weil die darauf folgenden
   Massen-Aktionen für Meta-Karten nach ADR-0036 verboten sind. Diese Suite
   nagelt beide Seiten fest, damit die Asymmetrie nicht als Fehler „korrigiert"
   wird. */

import { BulkSelect } from '../src/bulk-select.js'

/* happy-dom kennt kein Layout: `offsetParent` ist überall null, und
   getVisibleCards() würde alles wegfiltern. Die Eigenschaft wird deshalb
   explizit gesetzt — sie ist hier der Stellvertreter für „steht sichtbar in
   einer aufgeklappten Spalte". */
function buildCardsMitMeta() {
  document.body.innerHTML = ''
  const spec = [
    { id: '1', meta: false },
    { id: 'meta-start', meta: true },
    { id: '2', meta: false },
  ]
  for (const { id, meta } of spec) {
    const card = document.createElement('div')
    card.className = meta ? 'card card--sprint-meta' : 'card'
    card.dataset.id = id
    document.body.appendChild(card)
    Object.defineProperty(card, 'offsetParent', { value: document.body, configurable: true })
  }
  return [...document.querySelectorAll('.card')]
}

describe('Shortcuts — F-157: Meta-Karten bleiben in j/k, fehlen aber bei Ctrl+A', () => {
  /* `activeCardId` ist Modul-Zustand und überlebt das Leeren des DOM — ohne
     das Esc hier startet der zweite Test mit der Markierung des ersten. */
  beforeEach(() => { BulkSelect.clearAll(); fireKey('Escape') })
  afterEach(()  => { BulkSelect.clearAll() })

  it('j erreicht auch die Sprint-Meta-Karte', () => {
    const [erste, meta] = buildCardsMitMeta()
    fireKey('j')
    expect(erste.classList.contains('card--keyboard-active')).toBe(true)
    fireKey('j')
    expect(meta.classList.contains('card--keyboard-active')).toBe(true)
  })

  it('k erreicht sie ebenfalls (Rückwärts-Umlauf)', () => {
    const cards = buildCardsMitMeta()
    fireKey('k')                       /* ohne aktive Karte: letzte */
    expect(cards[2].classList.contains('card--keyboard-active')).toBe(true)
    fireKey('k')
    expect(cards[1].classList.contains('card--keyboard-active')).toBe(true)
  })

  it('Ctrl+A selektiert nur die Nicht-Meta-Karten', () => {
    buildCardsMitMeta()
    fireKey('a', document, { ctrlKey: true })
    expect(BulkSelect.count()).toBe(2)
  })
})

describe('Sitzungs-QA Sprint 85 — Enter auf Knöpfen und Escape im Hilfe-Overlay', () => {
  beforeEach(() => { BulkSelect.clearAll(); fireKey('Escape') })

  it('Enter auf einem Knopf öffnet nicht die per j markierte Karte', () => {
    const [erste] = buildCardsMitMeta()
    let geklickt = 0
    erste.addEventListener('click', () => { geklickt++ })
    fireKey('j')
    expect(erste.classList.contains('card--keyboard-active')).toBe(true)
    /* role="button": Sprint-Titel, Logo, Swimlane-Griff, Chat-Kopf (F-1828) */
    for (const html of ['<button>x</button>', '<a href="#">x</a>', '<div role="menuitem" tabindex="0">x</div>', '<div role="button" tabindex="0">x</div>']) {
      const wrap = document.createElement('div'); wrap.innerHTML = html; document.body.appendChild(wrap)
      const ziel = wrap.firstChild
      const ev = fireKey('Enter', ziel)
      expect(ev.defaultPrevented).toBe(false)
    }
    expect(geklickt).toBe(0)
    /* Gegenrichtung: Enter ohne Knopf öffnet die Karte weiter */
    fireKey('Enter', document.body)
    expect(geklickt).toBe(1)
  })

  it('Hilfe-Overlay, Fokus auf body: Escape schließt es', () => {
    fireKey('?')
    expect(document.querySelector('.shortcuts-overlay')).not.toBe(null)
    document.activeElement?.blur?.()
    fireKey('Escape', document.body)
    expect(document.querySelector('.shortcuts-overlay')).toBe(null)
  })
})
