/* @vitest-environment happy-dom */

/**
 * ADR-0076 — Beschreibungs-Pane: expliziter „Bearbeiten"-Button statt
 * Klick-auf-Block (revidiert ADR-0029 NUR für den desc-Pane).
 *
 * #267 (Andi-Report 2026-05-19): Der Toggle ist ein Modus-SCHALTER und
 * lebt in der `.desc-toolbar` (innerhalb `.modal-desc-section`), NICHT
 * mehr IN `#desc-pane` (dem WYSIWYG-Editor). Im Lese-Modus trägt die
 * Section NICHT `.desc-section-editing` (CSS blendet dann die
 * Format-Buttons aus); im Edit-Modus trägt sie sie.
 *
 * Sichert:
 *  - Toggle liegt außerhalb #desc-pane (in .desc-toolbar) — der #267-Fix
 *  - desc öffnet im Lese-Modus: `.desc-pane--readonly`, Toggle „Bearbeiten",
 *    Block-Klick aktiviert KEINEN Inline-Editor
 *  - Toggle → Edit: Block-Klick aktiviert Textarea, Toggle „Fertig",
 *    Section `.desc-section-editing`
 *  - Toggle zurück → Lese-Modus
 *  - agent-Pane unverändert (ADR-0029): KEIN Toggle
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))

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
import { I18n }     from '../src/i18n.js'
import '../src/modal.js'

function makeCard(overrides = {}) {
  return {
    id: 'card-076', displayId: 76, title: 'T',
    description: 'Erster Absatz.\n\nZweiter Absatz.',
    agentContext: '', comments: [], attachments: [], links: [], activity: [],
    priority: 'mittel', labels: [], owner: '', complexity: '', effort: null,
    qa: false, critical: false, blocked: false, archived: false,
    columnId: 'col-backlog', ...overrides,
  }
}
const openModal = (card) => { EventBus.emit('card:edit', card); return document.querySelector('.modal-overlay') }
function resetModal() { EventBus.emit('config:language-changed'); document.body.innerHTML = '' }

/* #267: Toggle lebt in der desc-Section-Toolbar, NICHT in #desc-pane. */
const descSection = (ov) => ov.querySelector('#tab-panel-desc .modal-desc-section')
const descToggle  = (ov) => descSection(ov).querySelector('.desc-toolbar > .desc-edit-toggle')

beforeEach(() => { boardModeValue = 'standalone'; vi.useFakeTimers(); resetModal() })
afterEach(() => { resetModal(); vi.useRealTimers() })

describe('ADR-0076 / #267 — desc-Pane Bearbeiten-Toggle', () => {
  it('#267: Toggle liegt in .desc-toolbar, NICHT in #desc-pane (außerhalb des Editors)', () => {
    const ov = openModal(makeCard())
    const pane = ov.querySelector('#desc-pane')
    expect(pane.querySelector('.desc-edit-toggle')).toBeNull()      /* nicht im Editor */
    expect(descToggle(ov)).toBeTruthy()                              /* sondern in der Toolbar */
  })

  it('desc öffnet im Lese-Modus: Block-Klick aktiviert KEINEN Editor', () => {
    const ov = openModal(makeCard())
    const pane = ov.querySelector('#desc-pane')
    expect(pane.classList.contains('desc-pane--readonly')).toBe(true)
    expect(descSection(ov).classList.contains('desc-section-editing')).toBe(false)

    const toggle = descToggle(ov)
    expect(toggle).toBeTruthy()
    expect(toggle.textContent).toBe(I18n.t('md.edit-toggle'))

    const block = pane.querySelector('.typora-block')
    expect(block).toBeTruthy()
    block.click()
    vi.runAllTimers()
    expect(pane.querySelector('.typora-textarea')).toBeNull()
  })

  it('Toggle → Edit: Block-Klick aktiviert die Textarea (ADR-0029)', () => {
    const ov = openModal(makeCard())
    const pane = ov.querySelector('#desc-pane')

    descToggle(ov).click()
    vi.runAllTimers()

    expect(pane.classList.contains('desc-pane--readonly')).toBe(false)
    expect(descSection(ov).classList.contains('desc-section-editing')).toBe(true)
    expect(descToggle(ov).textContent).toBe(I18n.t('md.edit-done'))

    pane.querySelector('.typora-block').click()
    vi.runAllTimers()
    expect(pane.querySelector('.typora-editing .typora-textarea')).toBeTruthy()
  })

  it('Toggle zurück → Lese-Modus, kein Editor', () => {
    const ov = openModal(makeCard())
    const pane = ov.querySelector('#desc-pane')
    descToggle(ov).click(); vi.runAllTimers()
    /* zurück auf „Fertig" klicken */
    descToggle(ov).click(); vi.runAllTimers()
    expect(pane.classList.contains('desc-pane--readonly')).toBe(true)
    expect(descSection(ov).classList.contains('desc-section-editing')).toBe(false)
    expect(descToggle(ov).textContent).toBe(I18n.t('md.edit-toggle'))
    expect(pane.querySelector('.typora-textarea')).toBeNull()
  })

  it('Checklisten-Häkchen bleiben im Lese-Modus interaktiv (keine .typora-textarea nötig)', () => {
    const ov = openModal(makeCard({ description: '- [ ] Aufgabe' }))
    const pane = ov.querySelector('#desc-pane')
    expect(pane.classList.contains('desc-pane--readonly')).toBe(true)
    const box = pane.querySelector('input[type="checkbox"]')
    expect(box).toBeTruthy()
    /* Toggle wirkt ohne Edit-Modus, ohne Textarea zu öffnen */
    box.click()
    vi.runAllTimers()
    expect(pane.querySelector('.typora-textarea')).toBeNull()
  })

  it('K-2: Neu-Block tippen + „Fertig" → Text NICHT verloren (mousedown-preventDefault-Falle)', () => {
    const ov = openModal(makeCard({ description: '' })) /* leer → Placeholder */
    const pane = ov.querySelector('#desc-pane')
    /* in Edit-Modus */
    descToggle(ov).click(); vi.runAllTimers()
    /* Placeholder klicken → Neu-Block-Textarea */
    pane.querySelector('.typora-placeholder').click(); vi.runAllTimers()
    const ta = pane.querySelector('.typora-new')
    expect(ta).toBeTruthy()
    ta.value = 'Frisch getippter Absatz'
    /* „Fertig" klicken OHNE vorher zu blurren — früher Datenverlust */
    descToggle(ov).click(); vi.runAllTimers()
    /* zurück im Lese-Modus, Text als gerenderter Block vorhanden */
    expect(pane.classList.contains('desc-pane--readonly')).toBe(true)
    expect(pane.textContent).toContain('Frisch getippter Absatz')
    expect(pane.querySelector('.typora-textarea')).toBeNull()
  })

  it('agent-Pane unverändert (ADR-0029): KEIN Toggle, Klick aktiviert direkt', () => {
    boardModeValue = 'project'
    const ov = openModal(makeCard({ owner: 'Claude Sonnet', agentContext: 'Agent-Notiz.' }))
    ov.querySelector('#tab-agent').click()
    vi.runAllTimers()

    const apane = ov.querySelector('#agent-pane')
    expect(apane).toBeTruthy()
    expect(ov.querySelector('#tab-panel-agent .desc-edit-toggle')).toBeNull()
    expect(apane.classList.contains('desc-pane--readonly')).toBe(false)

    apane.querySelector('.typora-block').click()
    vi.runAllTimers()
    expect(apane.querySelector('.typora-editing .typora-textarea')).toBeTruthy()
  })
})

/* Karte #450, Kritiker-Befund zu F-113: Der Tastatur-Weg in die Toolbar hat
   KEINE Wirkung gehabt.

   Jeder Toolbar-Knopf verhindert per `mousedown → preventDefault` den
   Fokus-Diebstahl — für die Maus. Der Tastatur-Weg (normaler Tab-Umlauf wie
   auch die mit F-113 ergänzte Pfeil-Navigation) hat diesen Schutz nicht: Der
   Fokuswechsel blurt das Textarea, der blur-Handler committet den Block und
   rendert den Pane neu, `.typora-editing` verschwindet — und
   `applyToActiveTextarea` steigt wortlos aus.

   Die vier F-113-Tests in modal.dom.test.js prüfen nur `activeElement` und die
   `.open`-Klasse; sie können den Defekt per Konstruktion nicht sehen. Dieser
   Test prüft die WIRKUNG und wäre ohne den Fix im blur-Handler rot. */
describe('Karte #450 — Toolbar per Tastatur wirkt auf den bearbeiteten Block', () => {
  function inBearbeitung(ov) {
    descToggle(ov).click(); vi.runAllTimers()
    ov.querySelector('#desc-pane .typora-block').click(); vi.runAllTimers()
    return ov.querySelector('#desc-pane .typora-editing .typora-textarea')
  }

  it('Pfeil-Navigation ins Überschriften-Menü setzt tatsächlich eine Überschrift', () => {
    const ov = openModal(makeCard())
    const ta = inBearbeitung(ov)
    expect(ta, 'Bearbeitungsmodus nicht hergestellt').toBeTruthy()
    ta.focus()
    ta.setSelectionRange(0, 0)

    const wrap  = ov.querySelector('#tab-panel-desc .toolbar-dropdown-wrap')
    const btn   = wrap.querySelector('.toolbar-btn-dropdown')
    const items = [...wrap.querySelectorAll('.toolbar-dropdown-item')]

    /* Genau der Weg aus F-113: Pfeil-runter am Auslöser → Fokus auf H2. */
    btn.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, cancelable: true }))
    expect(document.activeElement, 'F-113-Navigation hat den Fokus nicht bewegt').toBe(items[0])

    /* Der Block muss die Bearbeitung überlebt haben — sonst greift die Aktion ins Leere. */
    expect(ov.querySelector('#desc-pane .typora-editing .typora-textarea'),
      'der Fokuswechsel in die Toolbar hat den Block committet — die Aktion trifft nichts mehr')
      .toBeTruthy()

    items[0].click()
    vi.runAllTimers()

    const danach = ov.querySelector('#desc-pane .typora-editing .typora-textarea')
    expect(danach, 'nach der Aktion ist kein Bearbeitungsfeld mehr da').toBeTruthy()
    expect(danach.value, 'die Überschrift wurde nicht gesetzt').toMatch(/^##\s/)
  })

  it('ein Klick ausserhalb der Toolbar committet den Block weiterhin', () => {
    /* Gegenrichtung: Der neue Schutz darf den Normalfall nicht aushebeln —
       sonst bliebe der Editor offen, wo er sich schliessen soll. */
    const ov = openModal(makeCard())
    const ta = inBearbeitung(ov)
    ta.focus()
    ta.value = 'Geändert.'
    ta.dispatchEvent(new Event('blur'))
    vi.runAllTimers()
    expect(ov.querySelector('#desc-pane .typora-editing')).toBeNull()
  })
})
