/* @vitest-environment happy-dom */

/**
 * Karte #774 / F-1939 — Schätzwerte aus fremden Skalen im Karten-Modal.
 *
 * Sieben Konsumenten-Boards führen `complexity` als S/M/L/XL (und anderes),
 * fünf `effort` als Text (gemessen 2026-10-09). Das Auswahlfeld kennt nur
 * ''/einfach/mittel/hoch, das Aufwandsfeld ist `type="number"` — beide zeigen
 * einen fremden Wert als leer. Weil isDirty() und der Speichern-Patch das
 * Formular gegen die Karte vergleichen, galt die Karte damit schon beim
 * Öffnen als geändert: Öffnen und Schliessen löschte die Schätzung.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn((k) => (k === 'userName' ? 'TestUser' : (k === 'boardMode' ? 'standalone' : ''))), set: vi.fn() },
}))

import { EventBus } from '../src/eventbus.js'
import { Modal }    from '../src/modal.js'

void Modal

const karte = (o = {}) => ({
  id: 'c-774', displayId: 774, title: 'Altkarte', description: '', agentContext: '',
  comments: [], attachments: [], links: [], activity: [], priority: 'mittel', labels: [],
  owner: '', complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'col-backlog', ...o,
})

function oeffne(c) {
  EventBus.emit('card:edit', c)
  return document.querySelector('.modal-overlay')
}

function schliesseMitX(overlay) {
  const spy = vi.fn()
  EventBus.on('card:updated', spy)
  try { overlay.querySelector('.btn-modal-close').click() } finally { EventBus.off('card:updated', spy) }
  return spy
}

beforeEach(() => { vi.useFakeTimers(); EventBus.emit('config:language-changed'); document.body.innerHTML = '' })
afterEach(() => { EventBus.emit('config:language-changed'); document.body.innerHTML = ''; vi.useRealTimers() })

describe('Karte #774 — fremde Schätzskalen überleben das Modal', () => {
  it('Öffnen und Schliessen ohne Änderung speichert nichts', () => {
    const spy = schliesseMitX(oeffne(karte({ complexity: 'M', effort: 'M' })))
    expect(spy).not.toHaveBeenCalled()
  })

  it('nur den Titel ändern: complexity und effort bleiben unverändert', () => {
    const overlay = oeffne(karte({ complexity: 'XL', effort: '30 min' }))
    overlay.querySelector('#modal-title').value = 'Neuer Titel'
    const spy = schliesseMitX(overlay)
    expect(spy).toHaveBeenCalledTimes(1)
    const gespeichert = spy.mock.calls[0][0]
    expect(gespeichert.title).toBe('Neuer Titel')
    expect(gespeichert.complexity).toBe('XL')
    expect(gespeichert.effort).toBe('30 min')
  })

  it('der fremde Wert ist im Auswahlfeld sichtbar, das Aufwandsfeld nennt ihn', () => {
    const overlay = oeffne(karte({ complexity: 'M', effort: 'M' }))
    const sel = overlay.querySelector('#modal-complexity')
    expect(sel.selectedOptions[0].textContent).toContain('M')
    expect(overlay.querySelector('#modal-effort').placeholder).toContain('M')
  })

  it('die Zusatz-Option verschwindet für die nächste Karte', () => {
    let overlay = oeffne(karte({ complexity: 'M' }))
    schliesseMitX(overlay)
    overlay = oeffne(karte({ id: 'c-2', displayId: 2, complexity: '' }))
    const werte = [...overlay.querySelector('#modal-complexity').options].map(o => o.value)
    expect(werte).toEqual(['', 'einfach', 'mittel', 'hoch'])
  })

  it('wer eine Zahl einträgt oder eine Stufe wählt, überschreibt den fremden Wert bewusst', () => {
    const overlay = oeffne(karte({ complexity: 'M', effort: 'M' }))
    overlay.querySelector('#modal-complexity').value = 'mittel'
    const eff = overlay.querySelector('#modal-effort')
    eff.value = '2'
    eff.dispatchEvent(new Event('input', { bubbles: true }))
    const spy = schliesseMitX(overlay)
    expect(spy.mock.calls[0][0].complexity).toBe('mittel')
    expect(spy.mock.calls[0][0].effort).toBe(2)
  })

  /* Kritiker M-1: Zahlen (linux, framework), 0 und Wahrheitswerte sind ebenfalls
     fremde Werte — ein <select> liefert nur Text, `|| ''` machte aus 0 einen
     leeren Wert. Typ muss erhalten bleiben, nicht nur der Anschein. */
  it.each([[2], [1], [0], [true], [['a']], ['klein']])('complexity %j überlebt Öffnen und Schliessen mit Typ', (wert) => {
    const spy = schliesseMitX(oeffne(karte({ complexity: wert })))
    expect(spy).not.toHaveBeenCalled()
  })

  it('Titel ändern bei complexity 2: gespeichert wird die Zahl 2, nicht "2"', () => {
    const overlay = oeffne(karte({ complexity: 2 }))
    overlay.querySelector('#modal-title').value = 'Neu'
    const spy = schliesseMitX(overlay)
    expect(spy.mock.calls[0][0].complexity).toBe(2)
  })

  /* Kritiker M-2: Nach dem ersten Tippen gilt der Altwert als ersetzt — der
     Platzhalter darf dann nicht mehr „bleibt erhalten“ versprechen. */
  it('Platzhalter fällt zurück, sobald jemand im Aufwandsfeld tippt', () => {
    const overlay = oeffne(karte({ effort: '30 min' }))
    const eff = overlay.querySelector('#modal-effort')
    expect(eff.placeholder).toContain('30 min')
    eff.value = '5'
    eff.dispatchEvent(new Event('input', { bubbles: true }))
    expect(eff.placeholder).not.toContain('30 min')
  })

  it('Gegenrichtung: gewöhnliche Werte verhalten sich wie bisher', () => {
    const overlay = oeffne(karte({ complexity: 'hoch', effort: 4 }))
    expect(schliesseMitX(overlay)).not.toHaveBeenCalled()
    const o2 = oeffne(karte({ complexity: 'hoch', effort: 4 }))
    const eff = o2.querySelector('#modal-effort')
    eff.value = ''
    eff.dispatchEvent(new Event('input', { bubbles: true }))
    const spy = schliesseMitX(o2)
    expect(spy.mock.calls[0][0].effort).toBe(null)
  })
})

/* Verify V-2 (F-1947): derselbe Fehler an den Nachbarfeldern. Gemessen 09.10.:
   priority „normal“/„P1“/„high“ in fünf Boards (auch aBoard selbst). */
describe('Verify V-2 — Priorität und Wiederholung mit fremden Werten', () => {
  it.each([['normal'], ['P1'], ['high'], [2]])('priority %j überlebt Öffnen und Schliessen', (wert) => {
    expect(schliesseMitX(oeffne(karte({ priority: wert })))).not.toHaveBeenCalled()
  })

  it('priority P1 bleibt bei Titeländerung P1, eine bewusste Wahl überschreibt', () => {
    let overlay = oeffne(karte({ priority: 'P1' }))
    overlay.querySelector('#modal-title').value = 'Neu'
    expect(schliesseMitX(overlay).mock.calls[0][0].priority).toBe('P1')
    overlay = oeffne(karte({ priority: 'P1' }))
    overlay.querySelector('#modal-priority').value = 'hoch'
    expect(schliesseMitX(overlay).mock.calls[0][0].priority).toBe('hoch')
  })

  it('Wiederholung mit unbekanntem Intervall bleibt unverändert erhalten', () => {
    const r = { interval: 'yearly', lastSpawned: '2026-01-01', day: 3 }
    expect(schliesseMitX(oeffne(karte({ recurrence: r })))).not.toHaveBeenCalled()
    const overlay = oeffne(karte({ recurrence: r }))
    overlay.querySelector('#modal-title').value = 'Neu'
    expect(schliesseMitX(overlay).mock.calls[0][0].recurrence).toEqual(r)
  })

  /* Kritiker Sprint 88 (F-1963): Bis dahin schrieb schon das Ansehen einer Karte
     mit leerer Priorität „mittel“ (82 Karten im eigenen Board) und 0/false gingen
     verloren. Leer zeigt weiter „mittel“, gilt aber als unverändert. */
  it.each([[''], [0], [false]])('priority %j: Öffnen und Schliessen speichert nichts', (wert) => {
    expect(schliesseMitX(oeffne(karte({ priority: wert })))).not.toHaveBeenCalled()
  })

  it('leere Priorität: Titel ändern lässt sie leer, angezeigt wird „mittel“, erst „hoch“ überschreibt', () => {
    let overlay = oeffne(karte({ priority: '' }))
    expect(overlay.querySelector('#modal-priority').value).toBe('mittel')
    overlay.querySelector('#modal-title').value = 'Neu'
    expect(schliesseMitX(overlay).mock.calls[0][0].priority).toBe('')
    overlay = oeffne(karte({ priority: '' }))
    overlay.querySelector('#modal-priority').value = 'hoch'
    expect(schliesseMitX(overlay).mock.calls[0][0].priority).toBe('hoch')
    expect(schliesseMitX(oeffne(karte({ priority: 'hoch' })))).not.toHaveBeenCalled()
  })

  /* Kritiker Sprint 88 (F-1964): eine bekannte Wiederholung wurde beim Schliessen
     neu gebaut — ohne lastSpawned wurde es „gestern“ (aktiviert die ruhende
     Wiederholung, recurrence.js überspringt sie ohne), day "1" wurde 1,
     Zusatzschlüssel fielen weg. */
  it.each([
    [{ interval: 'weekly', day: 1 }],
    [{ interval: 'weekly', day: '1', lastSpawned: '2026-01-01' }],
    [{ interval: 'monthly', day: 15, lastSpawned: '2026-01-01', bis: '2026-12-31' }],
    [{ interval: 'daily', lastSpawned: '2026-01-01' }],
  ])('Wiederholung %j überlebt Öffnen und Schliessen', (r) => {
    expect(schliesseMitX(oeffne(karte({ recurrence: r })))).not.toHaveBeenCalled()
    const overlay = oeffne(karte({ recurrence: r }))
    overlay.querySelector('#modal-title').value = 'Neu'
    expect(schliesseMitX(overlay).mock.calls[0][0].recurrence).toEqual(r)
  })

  it('Gegenrichtung: anderer Tag oder frisch aktiviert baut die Wiederholung neu', () => {
    let overlay = oeffne(karte({ recurrence: { interval: 'weekly', day: 1, lastSpawned: '2026-01-01' } }))
    overlay.querySelector('#modal-recurrence-day').value = '3'
    expect(schliesseMitX(overlay).mock.calls[0][0].recurrence).toEqual({ interval: 'weekly', day: 3, lastSpawned: '2026-01-01' })
    overlay = oeffne(karte({ recurrence: null }))
    overlay.querySelector('#modal-recurrence-interval').value = 'daily'
    const r = schliesseMitX(overlay).mock.calls[0][0].recurrence
    expect(r.interval).toBe('daily')
    expect(r.lastSpawned).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})
