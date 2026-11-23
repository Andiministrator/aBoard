/* @vitest-environment happy-dom */

/**
 * Karte #826, Opus-Sitzungs-Kritik O-1 (ADR-0141 §7): Das Mehrfachauswahl-Menü
 * „Verschieben“ bietet gespiegelte Lanes einer externen Kopie nicht als Ziel an —
 * der Server wiese eine eigene Karte dort ab.
 *
 * Eigene Datei: Die Leiste hängt der Renderer einmalig an `body`; ein Test, der
 * `body` zurücksetzt, löst sie ab.
 *
 * Karte #827 E4: gilt bei Rückweg aus und an gleich — die Mehrfachauswahl
 * nimmt keine gebundenen Karten, also gibt es nichts, was nach innen ginge;
 * eine eigene Karte in eine gespiegelte Lane legt man einzeln (Ziehen,
 * „Karte hinzufügen“).
 */

import { it, expect, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/boardpaar.js', async (orig) => ({ ...(await orig()), boardpaarSeite: () => Promise.resolve('aussen'), vergissBoardpaarSeite: () => {} }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

const { State } = await import('../src/state.js')
const { I18n } = await import('../src/i18n.js')
const { Renderer } = await import('../src/renderer.js')
const { BulkSelect } = await import('../src/bulk-select.js')
const { _setzeRueckwegForTest } = await import('../src/boardpaar.js')

const karte = (o = {}) => ({ id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: [], owner: '', complexity: '', effort: null,
  qa: false, critical: false, blocked: false, archived: false, columnId: 'aussen-review', assignees: [], ...o })

describeServer('#826 O-1: Bulk-„Verschieben“ auf der externen Kopie', () => {
  it('bietet eigene Lanes an, gespiegelte nicht — bei Rückweg aus und an', () => {
    I18n.setLang('de')
    document.body.innerHTML = '<div id="app"><header id="board-header"></header><div id="board-columns"></div></div>'
    State.set({ version: '2', board: { id: 'kopie-1', title: 'Kopie', identities: [],
      columnDefs: [{ id: 'aussen-review', title: 'Review', spiegel: { id: 'R'.repeat(43) } }, { id: 'eigen-spalte', title: 'Eigene Spalte' }],
      sprints: [
        { id: 'aussen-lane', title: 'Sprint innen', kind: 'sprint', spiegel: { id: 'L'.repeat(43) }, cards: [karte({ id: 'sp', spiegel: { id: 'i1' } })] },
        { id: 'eigen-lane', title: 'Eigene Lane', kind: 'lane', cards: [karte({ id: 'eg', columnId: 'eigen-spalte' })] },
        { id: 'eigen-2', title: 'Zweite eigene Lane', kind: 'lane', cards: [] },
      ] } })
    /* Die Leiste hängt einmalig an body: body nicht zurücksetzen, nur neu zeichnen. */
    for (const an of [false, true]) {
      _setzeRueckwegForTest(an)
      Renderer.renderBoard(State.get())
      BulkSelect.clearAll()
      BulkSelect.toggle('eg')
      const bar = document.getElementById('bulk-action-bar')
      expect(bar).not.toBeNull()
      const knopf = [...bar.querySelectorAll('button')].find(k => k.textContent.includes(I18n.t('bulk.move')))
      knopf.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      const eintraege = [...bar.querySelectorAll('*')].filter(e => e.children.length === 0).map(e => e.textContent)
      expect(eintraege, `an=${an}`).toContain('Zweite eigene Lane')
      expect(eintraege, `an=${an}`).not.toContain('Sprint innen')
      BulkSelect.clearAll()
    }
    _setzeRueckwegForTest(false)
  })

  /* Verify V-2 (Karte #827): Eine eigene Karte, die nach dem Wählen gebunden wird
     (ins Rückweg-Ziel gezogen), zählt der Löschdialog nicht mit, und ihre Labels
     stehen nicht im Menü „Label entfernen“ — die Aktion lässt sie ohnehin aus.
     Ohne Zurücksetzen von body: die Leiste aus dem Test oben bleibt. */
  it('Löschdialog und Label-Menü zählen eine gebunden gewordene Karte nicht', () => {
    _setzeRueckwegForTest(true)
    const board = { id: 'kopie-1', title: 'Kopie', identities: [],
      columnDefs: [{ id: 'aussen-review', title: 'Review', spiegel: { id: 'R'.repeat(43) } }, { id: 'eigen-spalte', title: 'Eigene Spalte' }],
      sprints: [
        { id: 'aussen-lane', title: 'Sprint innen', kind: 'sprint', spiegel: { id: 'L'.repeat(43) }, cards: [] },
        { id: 'eigen-lane', title: 'Eigene Lane', kind: 'lane', cards: [
          karte({ id: 'eg', columnId: 'eigen-spalte', labels: ['nur-gebunden'] }),
          karte({ id: 'eg2', columnId: 'eigen-spalte', labels: ['frei'] }) ] },
      ] }
    State.set({ version: '2', board })
    Renderer.renderBoard(State.get())
    BulkSelect.clearAll()
    BulkSelect.toggle('eg')
    BulkSelect.toggle('eg2')
    /* „eg“ wird ins Rückweg-Ziel gezogen und damit gebunden. */
    const eg = board.sprints[1].cards.shift()
    board.sprints[0].cards.push({ ...eg, columnId: 'aussen-review' })
    State.set({ version: '2', board })
    Renderer.renderBoard(State.get())
    const bar = document.getElementById('bulk-action-bar')
    const knopf = t => [...bar.querySelectorAll('button')].find(k => k.textContent.includes(I18n.t(t)))
    knopf('bulk.label-remove').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const eintraege = [...bar.querySelectorAll('*')].filter(e => e.children.length === 0).map(e => e.textContent)
    expect(eintraege).toContain('frei')
    expect(eintraege).not.toContain('nur-gebunden')
    let frage = null
    vi.stubGlobal('confirm', m => { frage = m; return false })
    knopf('bulk.delete').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    vi.unstubAllGlobals()
    expect(frage).toBe(I18n.t('bulk.delete-confirm', 1))
    BulkSelect.clearAll()
    _setzeRueckwegForTest(false)
  })
})
