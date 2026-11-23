/* @vitest-environment happy-dom */

/**
 * Karte #826 Teil 2 (ADR-0141 §7/§8, F-2142): Spiegelkarten einer externen
 * Kopie sind im Browser nur lesbar.
 *   - Modal: Lesemodus mit Hinweisband, gesperrte Eingaben, kein Speichern,
 *     Löschen oder Archivieren; die nächste normale Karte ist wieder frei.
 *   - Renderer: Klasse `card--spiegel`, Abzeichen „↪ von innen“ und
 *     „📎 N nur intern“; schlanker Kopf gespiegelter Lanes; kein „Karte
 *     hinzufügen“ in gespiegelten Lanes; gespiegelte Spalten ohne Umbenennen,
 *     Löschen, Leeren.
 *   - DragDrop: Spiegelkarten und gespiegelte Spalten nicht ziehbar, in eine
 *     gespiegelte Lane wird nichts abgelegt (Klassen, deshalb in jedem Build).
 *
 * Modal und Renderer sind server-only (`describeServer`): im Solo-Template
 * gibt es keine Spiegel, die Zweige fallen per DCE heraus.
 *
 * Karte #827 E4: Das ist das Verhalten bei `boardpaar.rueckweg = aus` (Stand
 * 2b, weiterhin der Normalfall bis zum Einschalten). Jeder Test setzt den
 * Schalter deshalb ausdrücklich auf aus; das Gegenstück mit „an“ steht in
 * tests/boardpaar-rueckweg-827.test.js.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/boardpaar.js', async (orig) => ({ ...(await orig()),
  boardpaarSeite: () => Promise.resolve('aussen'),
  vergissBoardpaarSeite: () => {},
}))
vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName: vi.fn(() => null), listKnownBoards: vi.fn(async () => []), getInboxOpCount: vi.fn(() => 0),
    requestCompaction: vi.fn(async () => true), getChatTail: vi.fn(() => []), setChatTailListener: vi.fn(() => {}),
    setChatTailEnabled: vi.fn(async () => {}), readChatTail: vi.fn(async () => {}),
  },
}))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

const { EventBus } = await import('../src/eventbus.js')
const { Modal } = await import('../src/modal.js')
const { State } = await import('../src/state.js')
const { I18n } = await import('../src/i18n.js')
const { Renderer } = await import('../src/renderer.js')
const { DragDrop } = await import('../src/dragdrop.js')
const { _setzeRueckwegForTest } = await import('../src/boardpaar.js')
void Modal

const warte = () => new Promise(r => setTimeout(r, 0))

const karte = (o = {}) => ({
  id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: [], owner: '',
  complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'aussen-review', assignees: [], ...o,
})

const LANE = 'L'.repeat(43)
const REVIEW = 'R'.repeat(43)

function kopieBoard(spiegelKarten = [], eigeneKarten = []) {
  return {
    id: 'kopie-1', title: 'Kopie', identities: [],
    columnDefs: [
      { id: 'aussen-review', title: 'Review', spiegel: { id: REVIEW } },
      { id: 'eigen-spalte', title: 'Eigene Spalte' },
    ],
    sprints: [
      { id: 'aussen-lane', title: 'Sprint innen', kind: 'sprint', spiegel: { id: LANE }, cards: spiegelKarten },
      { id: 'eigen-lane', title: 'Eigene Lane', kind: 'lane', cards: eigeneKarten },
    ],
  }
}

let updates, geloescht
const merkeUpdate = c => updates.push(c)
const merkeLoeschen = id => geloescht.push(id)
beforeEach(() => {
  I18n.setLang('de')
  _setzeRueckwegForTest(false)
  updates = []; geloescht = []
  document.body.innerHTML = '<div id="app"><header id="board-header"></header><div id="board-columns"></div></div>'
  EventBus.on('card:updated', merkeUpdate)
  EventBus.on('card:deleted', merkeLoeschen)
})
afterEach(() => {
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  EventBus.off('card:updated', merkeUpdate)
  EventBus.off('card:deleted', merkeLoeschen)
  document.body.innerHTML = ''
})

function oeffne(c, b) {
  State.set({ version: '2', board: b ?? kopieBoard([c]) })
  EventBus.emit('card:edit', c)
}

describeServer('#826 Modal: Spiegelkarte nur lesend', () => {
  it('zeigt das Hinweisband und sperrt Eingaben und Knöpfe — Tabs, Schliessen, Abbrechen bleiben frei', async () => {
    const c = karte({ spiegel: { id: 'innen-1' }, description: '- [ ] Punkt' })
    oeffne(c); await warte()
    const band = document.querySelector('.modal-body > .modal-nur-lesen-band')
    expect(band).not.toBeNull()
    expect(band.textContent).toBe(I18n.t('card.spiegel.nur-lesen'))
    expect(document.querySelector('#modal-title').disabled).toBe(true)
    /* Auch, was open() erst spät baut (Beschreibung mit Häkchen). */
    expect(document.querySelector('#desc-pane .md-checkbox').disabled).toBe(true)
    expect(document.querySelector('.btn-card-delete').disabled).toBe(true)
    expect(document.querySelector('.btn-archive').disabled).toBe(true)
    expect(document.querySelector('.modal-actions-right .btn-primary').disabled).toBe(true)
    const textfelder = [...document.querySelectorAll('.modal-overlay textarea')]
    expect(textfelder.length).toBeGreaterThan(0)
    expect(textfelder.every(t => t.disabled)).toBe(true)
    expect([...document.querySelectorAll('.tab-btn')].every(b => !b.disabled)).toBe(true)
    expect(document.querySelector('.btn-modal-close').disabled).toBe(false)
    expect(document.querySelector('.btn-modal-cancel').disabled).toBe(false)
  })

  it('Schliessen, Speichern, Löschen und Archivieren schicken nichts', async () => {
    const c = karte({ spiegel: { id: 'innen-1' } })
    oeffne(c); await warte()
    /* Auch wenn ein Feld trotz Sperre geändert wird (Skript, Erweiterung): nichts geht raus. */
    document.querySelector('#modal-title').value = 'Geändert'
    document.querySelector('.btn-card-delete').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    document.querySelector('.btn-archive').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    document.querySelector('.modal-actions-right .btn-primary').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await warte()
    expect(updates).toEqual([])
    expect(geloescht).toEqual([])
  })

  it('auch per Skript entsperrte Knöpfe speichern, löschen und archivieren nicht', async () => {
    vi.stubGlobal('confirm', () => true)
    const c = karte({ spiegel: { id: 'innen-1' } })
    for (const sel of ['.btn-card-delete', '.btn-archive', '.modal-actions-right .btn-primary']) {
      oeffne(c); await warte()
      const titel = document.querySelector('#modal-title')
      titel.disabled = false
      titel.value = 'Geändert'
      titel.dispatchEvent(new Event('input', { bubbles: true }))
      const k = document.querySelector(sel)
      k.disabled = false
      k.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await warte()
      expect(updates, sel).toEqual([])
      expect(geloescht, sel).toEqual([])
      document.querySelector('.btn-modal-close').dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await warte()
    }
    vi.unstubAllGlobals()
  })

  it('Escape nach einer Eingabe schliesst ohne zu speichern', async () => {
    const c = karte({ spiegel: { id: 'innen-1' } })
    oeffne(c); await warte()
    const titel = document.querySelector('#modal-title')
    titel.disabled = false
    titel.value = 'Geändert'
    titel.dispatchEvent(new Event('input', { bubbles: true }))
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await warte()
    expect(updates).toEqual([])
    expect(document.querySelector('.modal-overlay--open')).toBeNull()
  })

  it('Häkchen der Checkliste ändert die Beschreibung nicht', async () => {
    const c = karte({ spiegel: { id: 'innen-1' }, description: '- [ ] Angebot prüfen' })
    oeffne(c); await warte()
    const cb = document.querySelector('#desc-pane .md-checkbox')
    expect(cb).not.toBeNull()
    /* Wie ein echter Klick, und auch dann, wenn ein Skript die Sperre aufhebt. */
    cb.disabled = false
    cb.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    cb.checked = true
    cb.dispatchEvent(new Event('change', { bubbles: true }))
    await warte()
    /* toggleChecklistLine speichert sonst sofort (card:updated, F-41) — hier nicht. */
    expect(updates).toEqual([])
  })

  it('die nächste normale Karte ist wieder frei, ohne Band', async () => {
    const s = karte({ id: 's1', spiegel: { id: 'innen-1' } })
    const n = karte({ id: 'n1', title: 'Eigen', columnId: 'eigen-spalte' })
    const b = kopieBoard([s], [n])
    oeffne(s, b); await warte()
    document.querySelector('.btn-modal-close').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    oeffne(n, b); await warte()
    expect(document.querySelector('.modal-nur-lesen-band')).toBeNull()
    expect(document.querySelector('#modal-title').disabled).toBe(false)
    expect(document.querySelector('[data-nur-lesen]')).toBeNull()
    document.querySelector('#modal-title').value = 'Eigen neu'
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await warte()
    expect(updates.map(u => u.title)).toEqual(['Eigen neu'])
  })

  it('markiert nur, was er selbst sperrt — eine schon bestehende Sperre trägt keine Marke', async () => {
    /* Ohne Datum ist das Uhrzeitfeld gesperrt (ADR-0138 §1), bevor der Lesemodus greift.
       Trüge es die Marke, höbe das Freigeben beim nächsten open() eine fremde Sperre auf. */
    oeffne(karte({ spiegel: { id: 'innen-1' }, dueDate: null })); await warte()
    const zeit = document.querySelector('#modal-due-time')
    expect(zeit.disabled).toBe(true)
    expect(zeit.hasAttribute('data-nur-lesen')).toBe(false)
    expect(document.querySelectorAll('[data-nur-lesen]').length).toBeGreaterThan(0)
  })

  it('Kritik K-1: nach einem Tab-Wechsel neu gebaute Knöpfe sind wieder gesperrt und wirkungslos', async () => {
    const ziel = karte({ id: 'eigen', columnId: 'eigen-spalte' })
    const c = karte({ id: 's1', spiegel: { id: 'innen-1' }, links: [{ type: 'related', targetId: 'eigen' }],
      attachments: [{ id: 'a1', name: 'x.txt', mime: 'text/plain', size: 1 }] })
    const b = kopieBoard([c], [ziel])
    let entfernt = 0
    const zaehle = () => { entfernt++ }
    EventBus.on('card:link-remove', zaehle)
    oeffne(c, b); await warte()
    for (const tab of ['links', 'attachments']) {
      document.querySelector(`.tab-btn[data-tab="${tab}"]`).dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await warte()
    }
    document.querySelector('.tab-btn[data-tab="links"]').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    const x = document.querySelector('.link-remove-btn')
    /* Ziel ist eine eigene Karte: der Knopf existiert, ist aber gesperrt. */
    expect(x).not.toBeNull()
    expect(x.disabled).toBe(true)
    /* Selbst entsperrt fängt die Klick-Sperre ihn ab. */
    x.disabled = false
    x.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    EventBus.off('card:link-remove', zaehle)
    expect(entfernt).toBe(0)
    expect(updates).toEqual([])
  })

  it('Kritik K-1: nach dem Open neu gezeichnete Chips sind wieder gesperrt', async () => {
    oeffne(karte({ spiegel: { id: 'innen-1' }, labels: ['alt'] })); await warte()
    expect([...document.querySelectorAll('#modal-label-chips .chip-x')].every(x => x.disabled)).toBe(true)
    /* Ein Skript hebt die Sperre auf und fügt ein Label hinzu — die Chips werden neu gezeichnet. */
    const neu = document.querySelector('#modal-label-neu')
    neu.disabled = false
    neu.value = 'neu'
    neu.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    const xs = [...document.querySelectorAll('#modal-label-chips .chip-x')]
    expect(xs.length).toBe(2)
    expect(xs.every(x => x.disabled)).toBe(true)
  })

  it('Kritik K-4: eine Datei auf das Modal fallen lassen lädt nichts hoch', async () => {
    let angehaengt = 0
    const zaehle = () => { angehaengt++ }
    EventBus.on('card:attachment-add', zaehle)
    /* Ein Upload-Versuch endet im Test mit einer Meldung (kein Backend) — gezählt
       wird sie als Beleg, dass überhaupt hochgeladen werden sollte. */
    const meldung = vi.fn()
    vi.stubGlobal('alert', meldung)
    oeffne(karte({ spiegel: { id: 'innen-1' } })); await warte()
    const datei = new File(['x'], 'x.txt', { type: 'text/plain' })
    const dt = { types: ['Files'], files: [datei], dropEffect: '' }
    const sende = typ => {
      const ev = new Event(typ, { bubbles: true, cancelable: true })
      Object.defineProperty(ev, 'dataTransfer', { value: dt })
      document.querySelector('.modal').dispatchEvent(ev)
    }
    sende('dragenter')
    /* Kein Ablege-Hinweis auf einer Spiegelkarte. */
    const ablage = document.querySelector('.attachment-drop-overlay')
    expect(ablage === null || ablage.style.display === 'none').toBe(true)
    sende('drop')
    await warte(); await warte()
    EventBus.off('card:attachment-add', zaehle)
    vi.unstubAllGlobals()
    expect(meldung).not.toHaveBeenCalled()
    expect(angehaengt).toBe(0)
    expect(updates).toEqual([])
  })

  it('Kritik K-2: der Verknüpfungs-Picker einer eigenen Karte bietet keine Spiegelkarten an', async () => {
    const s = karte({ id: 's1', title: 'Spiegelkarte', spiegel: { id: 'innen-1' } })
    const e2 = karte({ id: 'e2', title: 'Andere eigene', columnId: 'eigen-spalte' })
    const n = karte({ id: 'n1', title: 'Eigen', columnId: 'eigen-spalte' })
    oeffne(n, kopieBoard([s], [n, e2])); await warte()
    document.querySelector('.tab-btn[data-tab="links"]').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    document.querySelector('#link-add-btn').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    const zeilen = [...document.querySelectorAll('.link-picker-row')].map(r => r.textContent)
    expect(zeilen.some(z => z.includes('Andere eigene'))).toBe(true)
    expect(zeilen.some(z => z.includes('Spiegelkarte'))).toBe(false)
  })

  it('Kritik K-2: ein vorhandener Verweis auf eine Spiegelkarte lässt sich nicht entfernen', async () => {
    const s = karte({ id: 's1', title: 'Spiegelkarte', spiegel: { id: 'innen-1' } })
    const e2 = karte({ id: 'e2', title: 'Andere eigene', columnId: 'eigen-spalte' })
    const n = karte({ id: 'n1', columnId: 'eigen-spalte',
      links: [{ type: 'related', targetId: 's1' }, { type: 'related', targetId: 'e2' }] })
    oeffne(n, kopieBoard([s], [n, e2])); await warte()
    document.querySelector('.tab-btn[data-tab="links"]').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    const eintraege = [...document.querySelectorAll('.link-entry')]
    const mit = t => eintraege.find(e => e.textContent.includes(t))
    expect(mit('Spiegelkarte').querySelector('.link-remove-btn')).toBeNull()
    expect(mit('Andere eigene').querySelector('.link-remove-btn')).not.toBeNull()
  })

  it('Verify V-1: der leere Agent-Kontext öffnet auf einer Spiegelkarte keinen Editor', async () => {
    oeffne(karte({ spiegel: { id: 'innen-1' }, owner: 'Claude', agentContext: '' })); await warte()
    const tab = document.querySelector('.tab-btn[data-tab="agent"]')
    expect(tab.style.display).not.toBe('none')
    tab.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    const ph = document.querySelector('#agent-pane .agent-placeholder')
    expect(ph).not.toBeNull()
    ph.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    expect(document.querySelector('#agent-pane .typora-new')).toBeNull()
  })

  it('Gegenprobe zu V-1: bei einer eigenen Karte öffnet der Platzhalter den Editor', async () => {
    const n = karte({ id: 'n1', owner: 'Claude', agentContext: '', columnId: 'eigen-spalte' })
    oeffne(n, kopieBoard([], [n])); await warte()
    document.querySelector('.tab-btn[data-tab="agent"]').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    document.querySelector('#agent-pane .agent-placeholder').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    expect(document.querySelector('#agent-pane .typora-new')).not.toBeNull()
  })

  it('Kritik K-7: Doppelklick auf die Beschreibung öffnet keinen Editor', async () => {
    oeffne(karte({ spiegel: { id: 'innen-1' }, description: 'Ein Absatz' })); await warte()
    const block = document.querySelector('#desc-pane .typora-block')
    expect(block).not.toBeNull()
    block.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    block.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    expect(document.querySelector('#desc-pane .typora-editing')).toBeNull()
    expect(document.querySelector('#desc-pane textarea')).toBeNull()
  })

  it('Gegenprobe zu K-7: bei einer eigenen Karte öffnet der Doppelklick den Editor', async () => {
    const n = karte({ id: 'n1', description: 'Ein Absatz', columnId: 'eigen-spalte' })
    oeffne(n, kopieBoard([], [n])); await warte()
    document.querySelector('#desc-pane .typora-block').dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    await warte()
    expect(document.querySelector('#desc-pane .typora-editing')).not.toBeNull()
  })

  it('Kritik K-7: der KI-Tab bleibt bei Spiegelkarten aus, bei eigenen Karten an', async () => {
    const { Profile } = await import('../src/profile.js')
    const { LLMTab } = await import('../src/llm-tab.js')
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    vi.spyOn(LLMTab, 'hasAnyKey').mockResolvedValue(true)
    const n = karte({ id: 'n1', columnId: 'eigen-spalte' })
    const s = karte({ id: 's1', spiegel: { id: 'innen-1' } })
    const b = kopieBoard([s], [n])
    oeffne(n, b); await warte(); await warte()
    expect(document.querySelector('#tab-llm').style.display).toBe('')
    document.querySelector('.btn-modal-close').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    oeffne(s, b); await warte(); await warte()
    expect(document.querySelector('#tab-llm').style.display).toBe('none')
    vi.restoreAllMocks()
  })
})

describeServer('#826 Renderer: Spiegel auf der externen Kopie', () => {
  function zeichne(b) {
    State.set({ version: '2', board: b })
    Renderer.renderBoard(State.get())
  }

  it('Spiegelkarte trägt card--spiegel und die Abzeichen „von innen“ und „N nur intern“', () => {
    zeichne(kopieBoard(
      [karte({ id: 'mit', spiegel: { id: 'i1', anhaenge: 2 } }), karte({ id: 'ohne', spiegel: { id: 'i2' } })],
      [karte({ id: 'eigen', columnId: 'eigen-spalte' })],
    ))
    const mit = document.querySelector('.card[data-id="mit"]')
    expect(mit.classList.contains('card--spiegel')).toBe(true)
    /* Karte #827: bei Rückweg aus gebunden und nur lesbar (nicht ziehbar, nicht wählbar). */
    expect(mit.classList.contains('card--gebunden')).toBe(true)
    expect(mit.classList.contains('card--nur-lesen')).toBe(true)
    expect(mit.querySelector('.card-spiegel-badge').textContent).toBe(I18n.t('card.spiegel'))
    expect(mit.querySelector('.card-spiegel-anhaenge').textContent).toBe(I18n.t('card.spiegel.anhaenge', 2))
    const ohne = document.querySelector('.card[data-id="ohne"]')
    expect(ohne.querySelector('.card-spiegel-badge')).not.toBeNull()
    expect(ohne.querySelector('.card-spiegel-anhaenge')).toBeNull()
    const eigen = document.querySelector('.card[data-id="eigen"]')
    expect(eigen.classList.contains('card--spiegel')).toBe(false)
    expect(eigen.classList.contains('card--gebunden')).toBe(false)
    expect(eigen.querySelector('.card-spiegel-badge')).toBeNull()
  })

  it('Kritik K-5: Strg-Klick und Umschalt-Klick wählen keine Spiegelkarte aus', async () => {
    const { BulkSelect } = await import('../src/bulk-select.js')
    BulkSelect.clearAll()
    zeichne(kopieBoard([karte({ id: 'sp', spiegel: { id: 'i1' } })], [karte({ id: 'eg', columnId: 'eigen-spalte' })]))
    document.querySelector('.card[data-id="sp"]').dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true }))
    expect(BulkSelect.count()).toBe(0)
    document.querySelector('.card[data-id="eg"]').dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true }))
    expect([...BulkSelect.getSelected()]).toEqual(['eg'])
    document.querySelector('.card[data-id="sp"]').dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
    expect([...BulkSelect.getSelected()]).toEqual(['eg'])
    BulkSelect.clearAll()
  })

  it('Verify V-2: Strg+A wählt alle sichtbaren eigenen Karten, keine Spiegelkarte', async () => {
    const { BulkSelect } = await import('../src/bulk-select.js')
    const { Shortcuts } = await import('../src/shortcuts.js')
    Shortcuts.init()
    Shortcuts.setContext('board')
    BulkSelect.clearAll()
    /* happy-dom kennt kein Layout: offsetParent wäre immer null, Strg+A fände nichts. */
    const op = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetParent')
    Object.defineProperty(HTMLElement.prototype, 'offsetParent', { configurable: true, get() { return document.body } })
    try {
      zeichne(kopieBoard([karte({ id: 'sp', spiegel: { id: 'i1' } })], [karte({ id: 'eg', columnId: 'eigen-spalte' })]))
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', ctrlKey: true, bubbles: true }))
      expect([...BulkSelect.getSelected()]).toEqual(['eg'])
    } finally {
      if (op) Object.defineProperty(HTMLElement.prototype, 'offsetParent', op)
      else delete HTMLElement.prototype.offsetParent
      BulkSelect.clearAll()
    }
  })

  it('gespiegelte Lane: schlanker Kopf ohne Ziehgriff, Umbenennen, Löschen, Art, Archiv, Velocity', () => {
    zeichne(kopieBoard([karte({ spiegel: { id: 'i1' } })]))
    const kopf = document.querySelector('.swimlane[data-sprint-id="aussen-lane"] .sprint-header')
    expect(kopf.classList.contains('sprint-header--spiegel')).toBe(true)
    for (const sel of ['.sprint-drag-handle', '.btn-sprint-delete', '.btn-sprint-kind', '.btn-sprint-archive', '.sprint-velocity']) {
      expect(kopf.querySelector(sel), sel).toBeNull()
    }
    expect(kopf.querySelector('.sprint-title').textContent).toBe('Sprint innen')
    /* Gegenstück: die eigene Lane behält ihren Ziehgriff. */
    const eigen = document.querySelector('.swimlane[data-sprint-id="eigen-lane"] .sprint-header')
    expect(eigen.querySelector('.sprint-drag-handle')).not.toBeNull()
  })

  it('kein „Karte hinzufügen“ in gespiegelten Lanes, wohl aber in eigenen', () => {
    zeichne(kopieBoard())
    const spiegelSpalten = [...document.querySelectorAll('.swimlane[data-sprint-id="aussen-lane"] .column')]
    expect(spiegelSpalten.length).toBe(2)
    for (const s of spiegelSpalten) {
      expect(s.classList.contains('column--spiegel')).toBe(true)
      expect(s.classList.contains('column--rueckweg-ziel')).toBe(false)
      expect(s.querySelector('.btn-add-card')).toBeNull()
      expect(s.querySelector('.btn-col-clear')).toBeNull()
    }
    const eigene = [...document.querySelectorAll('.swimlane[data-sprint-id="eigen-lane"] .column')]
    for (const s of eigene) expect(s.querySelector('.btn-add-card')).not.toBeNull()
  })

  it('gespiegelte Spalte: kein Umbenennen, Löschen, Leeren, keine Farbe — auch in der eigenen Lane', () => {
    zeichne(kopieBoard([], [karte({ id: 'e', columnId: 'aussen-review' })]))
    const s = document.querySelector('.column[data-id="aussen-review"][data-sprint-id="eigen-lane"]')
    expect(s.classList.contains('column--struktur-spiegel')).toBe(true)
    expect(s.querySelector('.btn-col-delete')).toBeNull()
    expect(s.querySelector('.btn-col-clear')).toBeNull()
    expect(s.querySelector('.column-count').disabled).toBe(true)
    s.querySelector('.column-title').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(s.querySelector('.column-title-input')).toBeNull()
    /* Gegenstück: die eigene Spalte bleibt bedienbar. */
    const eigen = document.querySelector('.column[data-id="eigen-spalte"][data-sprint-id="eigen-lane"]')
    expect(eigen.querySelector('.btn-col-delete')).not.toBeNull()
    eigen.querySelector('.column-title').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(eigen.querySelector('.column-title-input')).not.toBeNull()
  })
})

describe('#826 DragDrop: Spiegel bleiben stehen', () => {
  let bewegt
  let initialisiert = false
  const merke = p => bewegt.push(p)
  beforeEach(() => {
    bewegt = []
    EventBus.on('card:moved', merke)
    EventBus.on('col:moved', merke)
    Element.prototype.setPointerCapture ??= function () {}
    document.body.innerHTML = `
      <div class="column column--spiegel" data-id="a" data-sprint-id="aussen-lane">
        <div class="column-header"><span class="x">A</span></div>
        <div class="card card--spiegel card--gebunden card--nur-lesen" data-id="spiegel"></div>
      </div>
      <div class="column column--struktur-spiegel" data-id="b" data-sprint-id="eigen-lane">
        <div class="column-header"><span class="y">B</span></div>
        <div class="card" data-id="eigen"></div>
      </div>`
    if (!initialisiert) { DragDrop.init(); initialisiert = true }
  })
  afterEach(() => {
    EventBus.off('card:moved', merke)
    EventBus.off('col:moved', merke)
    vi.restoreAllMocks()
  })

  const unten = (sel) => vi.spyOn(document, 'elementFromPoint').mockReturnValue(document.querySelector(sel))
  function ziehe(el, ziel) {
    el.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, button: 0, clientX: 0, clientY: 0, pointerId: 1 }))
    if (ziel) unten(ziel)
    el.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientX: 50, clientY: 50, pointerId: 1 }))
    el.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientX: 50, clientY: 50, pointerId: 1 }))
  }

  it('Spiegelkarte startet keinen Zug (kein Geist)', () => {
    document.querySelector('[data-id="spiegel"]').dispatchEvent(
      new PointerEvent('pointerdown', { bubbles: true, button: 0, pointerId: 1 }))
    expect(document.querySelector('.drag-ghost')).toBeNull()
  })

  it('eigene Karte lässt sich nicht in eine gespiegelte Lane ablegen', () => {
    ziehe(document.querySelector('[data-id="eigen"]'), '.column--spiegel')
    expect(bewegt).toEqual([])
  })

  it('Gegenprobe: eigene Karte in eine eigene Spalte wird verschoben', () => {
    ziehe(document.querySelector('[data-id="eigen"]'), '.column--struktur-spiegel')
    expect(bewegt.length).toBe(1)
  })

  it('gespiegelte Spalte startet keinen Spalten-Zug', () => {
    document.querySelector('.column--struktur-spiegel .x, .column--struktur-spiegel .y').dispatchEvent(
      new PointerEvent('pointerdown', { bubbles: true, button: 0, pointerId: 1 }))
    expect(document.querySelector('.drag-ghost')).toBeNull()
  })
})

describeServer('#826 F-2137: Band rechnet bei reinem Strukturwechsel neu', () => {
  const FREMD = 'F'.repeat(43)
  const SCHATTEN = [
    { schatten_id: 'S4', art: 'karte', nummer: 'i-9', titel: 'Ohne Platz', titel_platzhalter: false,
      lane: FREMD, spalte_id: REVIEW, bearbeiter: [], von_agent: false },
  ]
  let SchattenAussen
  beforeEach(async () => {
    ({ SchattenAussen } = await import('../src/schatten-aussen.js'))
    vi.stubGlobal('EventSource', class { addEventListener() {} close() {} })
    vi.stubGlobal('fetch', vi.fn(async () => ({ status: 200, ok: true,
      json: async () => ({ letzter_abgleich: new Date().toISOString(), schatten: SCHATTEN, kopie: true }) })))
  })
  afterEach(() => { SchattenAussen._resetForTest(); vi.unstubAllGlobals() })
  const ruhig = async () => { for (let i = 0; i < 6; i++) await warte() }
  const zeilen = () => [...document.querySelectorAll('#schatten-aussen:not([hidden]) .sch-zeile')].map(z => z.dataset.schatten)

  it('Lane kommt nach dem Schatten-Abruf hinzu: der Schatten verlässt das Band', async () => {
    State.set({ version: '2', board: kopieBoard() })
    SchattenAussen.init(); await ruhig()
    expect(zeilen()).toEqual(['S4'])
    const b = kopieBoard()
    b.sprints.push({ id: 'aussen-fremd', title: 'Neu', kind: 'lane', spiegel: { id: FREMD }, cards: [] })
    State.set({ version: '2', board: b }); await ruhig()
    expect(zeilen()).toEqual([])
  })

  it('Lane verschwindet: der Schatten erscheint wieder im Band', async () => {
    const b = kopieBoard()
    b.sprints.push({ id: 'aussen-fremd', title: 'Neu', kind: 'lane', spiegel: { id: FREMD }, cards: [] })
    State.set({ version: '2', board: b })
    SchattenAussen.init(); await ruhig()
    expect(zeilen()).toEqual([])
    State.set({ version: '2', board: kopieBoard() }); await ruhig()
    expect(zeilen()).toEqual(['S4'])
  })
})
