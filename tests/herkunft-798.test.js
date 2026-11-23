/* @vitest-environment happy-dom */
/* Karte #798: „Nach innen zurückholen“ im Karten-Modal und Filter „von Agenten
   nach außen“. Die Marke angelegtVonAgent setzt nur der Server (innen); der
   Knopf erscheint mit dem Vertraulich-Schalter, macht die Karte vertraulich und
   speichert über den normalen Weg. Im slim Template gibt es beides nicht. */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer, describeOhneServer } from './helpers/build-flags.js'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn((k) => (k === 'userName' ? 'TestUser' : '')), set: vi.fn() },
}))
let seiteResolver
let seiteWert = 'innen'
vi.mock('../src/boardpaar.js', async (orig) => ({ ...(await orig()),
  boardpaarSeite: () => new Promise(r => { if (seiteResolver === 'sofort') r(seiteWert); else seiteResolver = r }),
}))

import { EventBus } from '../src/eventbus.js'
import { Modal }    from '../src/modal.js'
import { State }    from '../src/state.js'
import { Filter, vonAgentNachAussen, vorigerWerktag } from '../src/filter.js'

const heute = new Date().toISOString().slice(0, 10)
const card = (o = {}) => ({
  id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: [], owner: '',
  complexity: '', effort: null, qa: false, critical: false, blocked: false, created: heute,
  archived: false, columnId: 'col-backlog', assignees: [], ...o,
})
const board = (cards) => ({ id: 'b1', title: 'B', identities: [], columnDefs: [{ id: 'col-backlog', title: 'Backlog' }],
  sprints: [{ id: 's1', title: 'S', cards }] })
const warte = () => new Promise(r => setTimeout(r, 0))
let updates
beforeEach(() => { seiteResolver = 'sofort'; seiteWert = 'innen'; updates = []; EventBus.emit('config:language-changed'); document.body.innerHTML = ''
  EventBus.on('card:updated', c => updates.push(c)); Filter.clear() })
afterEach(() => { Modal.close(); document.body.innerHTML = ''; Filter.clear() })

function open(c) { State.set({ board: board([c]) }); EventBus.emit('card:edit', c) }
const knopf = () => document.querySelector('#modal-zurueckholen')

describeServer('#798 „Nach innen zurückholen“', () => {
  it('Agenten-Karte offen, Seite innen: Knopf sichtbar; Klick schickt confidential:true', async () => {
    open(card({ angelegtVonAgent: true })); await warte()
    expect(knopf().hidden).toBe(false)
    knopf().click(); await warte()
    expect(updates.length).toBe(1)
    expect(updates[0].confidential).toBe(true)
  })
  it('ohne Marke, schon vertraulich oder Seite nicht innen: kein Knopf', async () => {
    open(card()); await warte()
    expect(knopf().hidden).toBe(true)
    Modal.close(); await warte()
    open(card({ angelegtVonAgent: true, confidential: true })); await warte()
    expect(knopf().hidden).toBe(true)
    Modal.close(); await warte()
    seiteWert = 'aus'
    open(card({ angelegtVonAgent: true })); await warte()
    expect(knopf().hidden).toBe(true)
  })
  it('vor der Seitenantwort verborgen (ein Klick ginge sonst still verloren)', async () => {
    seiteResolver = null
    open(card({ angelegtVonAgent: true }))
    expect(knopf().hidden).toBe(true)
    seiteResolver('innen'); await warte()
    expect(knopf().hidden).toBe(false)
  })
})

describeServer('#798 Filter „von Agenten nach außen“', () => {
  const T = (d) => Date.parse(`${d}T12:00:00Z`)
  const mitDatum = (created) => card({ angelegtVonAgent: true, created })
  it('Prädikat: Marke, offen, heute oder voriger Werktag', () => {
    expect(vonAgentNachAussen(card({ angelegtVonAgent: true }))).toBe(true)
    const gestern = new Date(Date.now() - 86400000).toISOString().slice(0, 10)
    expect(vonAgentNachAussen(card({ angelegtVonAgent: true, created: gestern }))).toBe(true)
    expect(vonAgentNachAussen(card({ angelegtVonAgent: true, confidential: true }))).toBe(false)
    expect(vonAgentNachAussen(card({ angelegtVonAgent: true, confidential: null }))).toBe(false)
    expect(vonAgentNachAussen(card({ angelegtVonAgent: 'true' }))).toBe(false)
    expect(vonAgentNachAussen(card())).toBe(false)
  })
  it('F-2407: Wochenende — montags zählt ab Freitag, am Wochenende ab Freitag, sonst voriger Tag', () => {
    /* 2026-10-19 Mo, 10-20 Di, 10-17 Sa, 10-18 So, 10-16 Fr, 10-15 Do, 10-14 Mi */
    expect(vorigerWerktag(T('2026-10-19'))).toBe('2026-10-16')
    expect(vonAgentNachAussen(mitDatum('2026-10-16'), T('2026-10-19'))).toBe(true)
    expect(vonAgentNachAussen(mitDatum('2026-10-15'), T('2026-10-19'))).toBe(false)
    expect(vonAgentNachAussen(mitDatum('2026-10-19'), T('2026-10-20'))).toBe(true)
    expect(vonAgentNachAussen(mitDatum('2026-10-18'), T('2026-10-20'))).toBe(false)
    expect(vonAgentNachAussen(mitDatum('2026-10-16'), T('2026-10-17'))).toBe(true)
    expect(vonAgentNachAussen(mitDatum('2026-10-16'), T('2026-10-18'))).toBe(true)
    expect(vonAgentNachAussen(mitDatum('2026-10-15'), T('2026-10-18'))).toBe(false)
  })
  it('Filter an: nur passende Karten', () => {
    Filter.set({ flagToggle: 'vonAgent' })
    expect(Filter.matches(card({ angelegtVonAgent: true }))).toBe(true)
    expect(Filter.matches(card())).toBe(false)
  })
})

describeOhneServer('#798 im Build ohne Server', () => {
  it('kein Knopf, Prädikat immer false', async () => {
    open(card({ angelegtVonAgent: true })); await warte()
    expect(knopf()).toBe(null)
    expect(vonAgentNachAussen(card({ angelegtVonAgent: true }))).toBe(false)
  })
})
