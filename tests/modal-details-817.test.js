/* @vitest-environment happy-dom */

/**
 * Karte #817 — Details-Tab übersichtlicher: Chips für Labels und Bearbeiter,
 * Flags als Pillen, Termine einklappbar, Beschreibung ohne Pflicht-Umweg über
 * den Bearbeiten-Knopf (ADR-0076 §Nachtrag 2026-10-12).
 *
 * 🛑 Kern: Öffnen und Schliessen ohne Eingabe ändert NICHTS — auch nicht mit
 * Fremdwerten (F-1939/F-1945/F-1947/F-1964). Die Chip-Felder sind nur eine
 * Ansicht über den unsichtbaren Datenträgern #modal-labels und
 * #modal-assignees; wer die Ansicht zeichnet, darf die Datenträger nicht
 * anfassen.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn((k) => (k === 'userName' ? 'TestUser' : (k === 'boardMode' ? 'standalone' : ''))), set: vi.fn() },
}))

import { EventBus } from '../src/eventbus.js'
import { I18n }     from '../src/i18n.js'
import { Modal }    from '../src/modal.js'

void Modal

const karte = (o = {}) => ({
  id: 'c-817', displayId: 817, title: 'Karte', description: '', agentContext: '',
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

const labelChips    = o => [...o.querySelectorAll('#modal-label-chips .chip')].map(c => c.firstChild.textContent)
const assigneeChips = o => [...o.querySelectorAll('#modal-assignee-chips .chip')].map(c => c.firstChild.textContent)
const labelEingabe  = o => o.querySelector('#modal-label-chips input')
const plusAuswahl   = o => o.querySelector('#modal-assignee-chips select.chip-add')

beforeEach(() => {
  vi.useFakeTimers()
  try { localStorage.clear() } catch {}
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
})
afterEach(() => { EventBus.emit('config:language-changed'); document.body.innerHTML = ''; vi.useRealTimers() })

describe('#817 — Öffnen und Schliessen ändert nichts (Chip-Felder)', () => {
  it.each([
    ['Labels normal',        { labels: ['bug', 'ui'] }],
    ['Labels mit Komma',     { labels: ['a,b', 'c'] }],
    ['Labels Nicht-Text',    { labels: [5, null, '', ' x '] }],
    ['Labels kein Array',    { labels: 'bug' }],
    ['Assignees normal',     { assignees: ['Claude Opus', 'TestUser'] }],
    ['Assignees doppelt',    { assignees: ['Claude', 'Claude', 7] }],
    ['Assignees extern',     { assignees: ['external:a@b.de'] }],
    ['Owner fremd',          { owner: 'Anna', assignees: ['Anna'] }],
    ['Flags als Fremdwert',  { qa: 'true', critical: 'ja', blocked: 1 }],
    ['Termine gesetzt',      { dueDate: '2026-10-14', dueTime: '09:00', recurrence: { interval: 'weekly', day: 3, lastSpawned: '2026-10-01' } }],
  ])('%s: kein card:updated', (_n, teil) => {
    const o = oeffne(karte(teil))
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })

  it('Chips zeigen die Labels und Beteiligten der Karte', () => {
    const o = oeffne(karte({ labels: ['bug', 'ui'], assignees: ['Claude Opus'] }))
    expect(labelChips(o)).toEqual(['bug', 'ui'])
    expect(assigneeChips(o)).toEqual(['Claude Opus'])
    /* „+“ bietet nur, was noch nicht gewählt ist. */
    const angeboten = [...plusAuswahl(o).options].map(x => x.value).filter(Boolean)
    expect(angeboten).not.toContain('Claude Opus')
    expect(angeboten).toContain('Claude Haiku')
  })

  it('Fremd-Labels: Label-Eingabe betreten und leer verlassen schreibt nichts', () => {
    const o = oeffne(karte({ labels: ['a,b', 5] }))
    const e = labelEingabe(o)
    e.focus()
    e.dispatchEvent(new Event('blur'))
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })

  it('Fremd-Labels: Chips zeichnen ändert den Datenträger nicht', () => {
    const o = oeffne(karte({ labels: ['a,b', 5] }))
    expect(o.querySelector('#modal-labels').value).toBe('a,b, 5')
    expect(o.querySelector('#modal-labels').dataset.beruehrt).toBeUndefined()
  })
})

describe('#817 — Bedienung der Chips speichert', () => {
  it('Label per Enter hinzufügen', () => {
    const o = oeffne(karte({ labels: ['bug'] }))
    const e = labelEingabe(o)
    e.value = 'neu'
    e.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    expect(labelChips(o)).toEqual(['bug', 'neu'])
    expect(schliesseMitX(o).mock.calls[0][0].labels).toEqual(['bug', 'neu'])
  })

  it('Label per Verlassen des Feldes übernehmen (kein Tipp-Verlust)', () => {
    const o = oeffne(karte())
    const e = labelEingabe(o)
    e.value = 'beim-blur'
    e.dispatchEvent(new Event('blur'))
    expect(schliesseMitX(o).mock.calls[0][0].labels).toEqual(['beim-blur'])
  })

  it('Label per × entfernen', () => {
    const o = oeffne(karte({ labels: ['bug', 'ui'] }))
    o.querySelector('#modal-label-chips .chip .chip-x').click()
    expect(labelChips(o)).toEqual(['ui'])
    expect(schliesseMitX(o).mock.calls[0][0].labels).toEqual(['ui'])
  })

  it('#971: Label-Chips tragen die Labelfarbe, Beteiligten-Chips nicht', () => {
    const o = oeffne(karte({ labels: ['bug'], assignees: ['TestUser'] }))
    const l = o.querySelector('#modal-label-chips .chip')
    expect(l.classList.contains('lc')).toBe(true)
    expect(l.style.getPropertyValue('--h')).not.toBe('')
    expect(o.querySelector('#modal-assignee-chips .chip').classList.contains('lc')).toBe(false)
  })

  it('Beteiligten per „+“ hinzufügen und per × entfernen', () => {
    const o = oeffne(karte({ assignees: ['TestUser'] }))
    const plus = plusAuswahl(o)
    plus.value = 'Claude Sonnet'
    plus.dispatchEvent(new Event('change', { bubbles: true }))
    expect(assigneeChips(o)).toEqual(['TestUser', 'Claude Sonnet'])
    o.querySelector('#modal-assignee-chips .chip .chip-x').click()
    expect(assigneeChips(o)).toEqual(['Claude Sonnet'])
    expect(schliesseMitX(o).mock.calls[0][0].assignees).toEqual(['Claude Sonnet'])
  })

  it('Owner bleibt ein eigenes Feld (ADR-0043): Beteiligte ändern lässt owner stehen', () => {
    const o = oeffne(karte({ owner: 'Claude Opus', assignees: [] }))
    const plus = plusAuswahl(o)
    plus.value = 'Claude Haiku'
    plus.dispatchEvent(new Event('change', { bubbles: true }))
    const g = schliesseMitX(o).mock.calls[0][0]
    expect(g.owner).toBe('Claude Opus')
    expect(g.assignees).toEqual(['Claude Haiku'])
  })
})

describe('#817 — Termine einklappbar', () => {
  const termine = o => o.querySelector('#modal-termine')

  it('nichts gesetzt: eingeklappt, Felder bleiben im DOM', () => {
    const o = oeffne(karte())
    expect(termine(o).open).toBe(false)
    expect(termine(o).querySelector('#modal-due-date')).toBeTruthy()
    expect(termine(o).querySelector('#modal-recurrence-interval')).toBeTruthy()
  })

  it('gesetzt: aufgeklappt, Zusammenfassung im Kopf', () => {
    const o = oeffne(karte({ dueDate: '2026-10-14', dueTime: '09:00', recurrence: { interval: 'weekly', day: 3 } }))
    expect(termine(o).open).toBe(true)
    const kopf = termine(o).querySelector('summary').textContent
    expect(kopf).toContain('09:00')
    expect(kopf).toContain(I18n.t('recurrence.weekly'))
  })

  it('zugeklappt gemerkt: gesetzte Termine bleiben zu, Kopf nennt sie trotzdem', () => {
    let o = oeffne(karte({ dueDate: '2026-10-14' }))
    termine(o).querySelector('summary').click()
    schliesseMitX(o)
    o = oeffne(karte({ id: 'c-2', displayId: 2, startDate: '2026-10-20' }))
    expect(termine(o).open).toBe(false)
    expect(termine(o).querySelector('summary').textContent).toContain(I18n.t('card.start-date'))
  })

  it('Template-Karte: Termine-Sektion verborgen; Sprint-Meta-Karte ebenso', () => {
    let o = oeffne(karte({ isTemplate: true }))
    expect(termine(o).style.display).toBe('none')
    schliesseMitX(o)
    o = oeffne(karte({ id: 'c-3', kind: 'sprint-meta', metaType: 'start' }))
    expect(termine(o).style.display).toBe('none')
    schliesseMitX(o)
    o = oeffne(karte({ id: 'c-4' }))
    expect(termine(o).style.display).toBe('')
  })
})

describe('#817 — Flags oben im Details-Tab', () => {
  it('Flag-Pillen stehen vor der Beschreibung', () => {
    const o = oeffne(karte())
    const panel = o.querySelector('#tab-panel-desc')
    const flags = panel.querySelector('.flag-row')
    const desc  = panel.querySelector('.modal-desc-section')
    expect(flags.compareDocumentPosition(desc) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})

describe('#817 / ADR-0076 Nachtrag — Beschreibung ohne Umweg über den Knopf', () => {
  it('leere Beschreibung: ein Klick auf den Platzhalter öffnet den Schreibmodus', () => {
    const o = oeffne(karte({ description: '' }))
    const pane = o.querySelector('#desc-pane')
    expect(pane.classList.contains('desc-pane--readonly')).toBe(true)
    pane.querySelector('.typora-placeholder').click()
    vi.runAllTimers()
    expect(pane.classList.contains('desc-pane--readonly')).toBe(false)
    const ta = pane.querySelector('.typora-new')
    expect(ta).toBeTruthy()
    ta.value = 'Neu geschrieben'
    /* Rückweg über „Fertig“ committet (K-2). */
    o.querySelector('.desc-edit-toggle').click()
    vi.runAllTimers()
    expect(schliesseMitX(o).mock.calls[0][0].description).toContain('Neu geschrieben')
  })

  it('mit Text: Einfachklick öffnet nichts, Doppelklick öffnet genau diesen Absatz', () => {
    const o = oeffne(karte({ description: 'Erster.\n\nZweiter.' }))
    const pane = o.querySelector('#desc-pane')
    const zweiter = pane.querySelectorAll('.typora-block')[1]
    zweiter.click()
    vi.runAllTimers()
    expect(pane.querySelector('.typora-textarea')).toBeNull()
    zweiter.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    vi.runAllTimers()
    const ta = pane.querySelector('.typora-editing .typora-textarea')
    expect(ta).toBeTruthy()
    expect(ta.value).toBe('Zweiter.')
  })

  it('Doppelklick auf ein Checklisten-Häkchen öffnet keinen Editor', () => {
    const o = oeffne(karte({ description: '- [ ] Aufgabe' }))
    const pane = o.querySelector('#desc-pane')
    pane.querySelector('input[type="checkbox"]').dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    vi.runAllTimers()
    expect(pane.querySelector('.typora-textarea')).toBeNull()
  })

  it('Öffnen und Schliessen nach blossem Doppelklick-Öffnen ändert nichts', () => {
    const o = oeffne(karte({ description: 'Nur lesen.' }))
    const pane = o.querySelector('#desc-pane')
    pane.querySelector('.typora-block').dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    vi.runAllTimers()
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })
})

/* ── Nacharbeit #817 (Kritiker K-1 bis K-5, Sichtprüfung O-1) ─────────── */

const knopf = (o, primaer) => [...o.querySelectorAll('.modal-actions .btn')].find(b => b.textContent === I18n.t(primaer ? 'btn.save-card' : 'btn.cancel'))
function mitSpy(fn) {
  const spy = vi.fn()
  EventBus.on('card:updated', spy)
  try { fn() } finally { EventBus.off('card:updated', spy) }
  return spy
}

describe('#817 Nacharbeit — halb getipptes Label geht nicht verloren (K-1)', () => {
  it('ESC mit halb getipptem Label speichert es', () => {
    const o = oeffne(karte({ labels: ['bug'] }))
    labelEingabe(o).value = 'halb'
    const spy = mitSpy(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })))
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0][0].labels).toEqual(['bug', 'halb'])
  })

  it('X mit halb getipptem Label speichert es', () => {
    const o = oeffne(karte())
    labelEingabe(o).value = 'a, b'
    expect(schliesseMitX(o).mock.calls[0][0].labels).toEqual(['a', 'b'])
  })

  it('Speichern-Knopf nimmt das halb getippte Label mit', () => {
    const o = oeffne(karte({ labels: ['bug'] }))
    labelEingabe(o).value = 'neu'
    const spy = mitSpy(() => knopf(o, true).click())
    expect(spy.mock.calls[0][0].labels).toEqual(['bug', 'neu'])
  })

  it('nur Leerzeichen im Eingabefeld: Schliessen ändert nichts', () => {
    const o = oeffne(karte({ labels: ['a,b', 5] }))
    labelEingabe(o).value = '   '
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })
})

describe('#817 Nacharbeit — kein Resttext auf der nächsten Karte (K-2)', () => {
  it('Text im Feld, Abbrechen ohne Blur, zweite Karte öffnen, Blur: zweite Karte bleibt unverändert', () => {
    let o = oeffne(karte({ id: 'c-a', displayId: 1 }))
    labelEingabe(o).value = 'rest'
    expect(mitSpy(() => knopf(o, false).click())).not.toHaveBeenCalled()
    o = oeffne(karte({ id: 'c-b', displayId: 2, labels: ['x'] }))
    expect(labelEingabe(o).value).toBe('')
    labelEingabe(o).dispatchEvent(new Event('blur'))
    expect(labelChips(o)).toEqual(['x'])
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })

  /* Verify V-1: Die beiden Leerungen verdecken sich gegenseitig — je ein Test pro Stelle. */
  it('close() leert das Feld selbst (ohne nachfolgendes open)', () => {
    const o = oeffne(karte({ id: 'c-a', displayId: 1 }))
    labelEingabe(o).value = 'rest'
    knopf(o, false).click()
    expect(labelEingabe(o).value).toBe('')
  })

  it('open() leert das Feld selbst (ohne vorheriges close)', () => {
    const o = oeffne(karte({ id: 'c-a', displayId: 1 }))
    labelEingabe(o).value = 'rest'
    const o2 = oeffne(karte({ id: 'c-b', displayId: 2 }))
    expect(labelEingabe(o2).value).toBe('')
  })
})

describe('#817 Nacharbeit — Fokus nach „ד (K-3)', () => {
  it('Label-Chip entfernen: Fokus auf den Nachbar-Chip, beim letzten aufs Eingabefeld', () => {
    const o = oeffne(karte({ labels: ['bug', 'ui'] }))
    o.querySelector('#modal-label-chips .chip-x').click()
    expect(document.activeElement).toBe(o.querySelector('#modal-label-chips .chip-x'))
    expect(document.activeElement.parentElement.firstChild.textContent).toBe('ui')
    document.activeElement.click()
    expect(document.activeElement).toBe(labelEingabe(o))
  })

  it('Beteiligten-Chip entfernen: Fokus aufs „+“', () => {
    const o = oeffne(karte({ assignees: ['TestUser'] }))
    o.querySelector('#modal-assignee-chips .chip-x').click()
    expect(document.activeElement).toBe(plusAuswahl(o))
  })
})

describe('#817 Nacharbeit — Beschriftung, Doppelklick, Tastatur (K-4/K-5)', () => {
  it('Labels-Feld hat ein echtes <label for> auf das Eingabefeld', () => {
    const o = oeffne(karte())
    const e = labelEingabe(o)
    expect(e.id).toBeTruthy()
    expect(o.querySelector(`label[for="${e.id}"]`)?.textContent).toBe(I18n.t('card.labels'))
  })

  it('Doppelklick auf ein Element IN einem Link öffnet keinen Editor', () => {
    const o = oeffne(karte({ description: 'Siehe [Doku](https://example.org).' }))
    const pane = o.querySelector('#desc-pane')
    const innen = document.createElement('strong')
    pane.querySelector('a').appendChild(innen)
    innen.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    vi.runAllTimers()
    expect(pane.querySelector('.typora-textarea')).toBeNull()
  })

  /* Space/Enter lösen in Browsern nativ einen click aus (Checkbox, <summary>);
     happy-dom bildet diese Aktivierung nicht nach. Geprüft wird deshalb, was
     unser Code dafür liefern muss: fokussierbares Element im Tab-Umlauf, das
     auf click reagiert. */
  it('Flag-Pille: Checkbox fokussierbar, Aktivierung (click) speichert', () => {
    const o = oeffne(karte())
    const cb = o.querySelector('#modal-qa')
    expect(cb.hidden).toBe(false)
    expect(cb.tabIndex).toBeGreaterThanOrEqual(0)
    expect(o.querySelector('label[for="modal-qa"]')).toBeTruthy()
    cb.focus()
    expect(document.activeElement).toBe(cb)
    cb.click()
    expect(schliesseMitX(o).mock.calls[0][0].qa).toBe(true)
  })

  it('Termine-Kopf: <summary> ist erstes Kind von <details>, Aktivierung klappt um', () => {
    const o = oeffne(karte())
    const t = o.querySelector('#modal-termine')
    expect(t.firstElementChild.tagName).toBe('SUMMARY')
    t.firstElementChild.click()
    expect(t.open).toBe(true)
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })
})

describe('#817 Nacharbeit — leere Beschreibung zeigt im Schreibmodus nur das Eingabefeld (O-1)', () => {
  it('nach Klick auf den Platzhalter ist er weg, die Textarea da', () => {
    const o = oeffne(karte({ description: '' }))
    const pane = o.querySelector('#desc-pane')
    pane.querySelector('.typora-placeholder').click()
    vi.runAllTimers()
    expect(pane.querySelectorAll('.typora-placeholder').length).toBe(0)
    expect(pane.querySelectorAll('.typora-new').length).toBe(1)
  })
})
