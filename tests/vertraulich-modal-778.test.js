/* @vitest-environment happy-dom */
/* Karte #778 (ADR-0140 §10): Schalter „vertraulich“ im Karten-Modal, am echten
   Modal ausgeführt (Verify-2, Sonnet). Geschickt wird `confidential` nur, wenn der
   Schalter bedient wurde; ein Bestand `null` gilt als an (Fall h); eine verspätete
   Antwort auf die Seitenfrage trifft nur die Karte, für die sie gestellt wurde (W2-1). */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

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

const card = (o = {}) => ({
  id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: [], owner: '',
  complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'col-backlog', assignees: [], ...o,
})
const board = (cards) => ({ id: 'b1', title: 'B', identities: [], columnDefs: [{ id: 'col-backlog', title: 'Backlog' }],
  sprints: [{ id: 's1', title: 'S', cards }] })
const warte = () => new Promise(r => setTimeout(r, 0))
let updates
beforeEach(() => { seiteResolver = 'sofort'; seiteWert = 'innen'; updates = []; EventBus.emit('config:language-changed'); document.body.innerHTML = ''
  EventBus.on('card:updated', c => updates.push(c)) })
afterEach(() => { EventBus.emit('config:language-changed'); document.body.innerHTML = '' })

function open(c, cards) { State.set({ board: board(cards ?? [c]) }); EventBus.emit('card:edit', c) }
const esc = () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
const cb = () => document.querySelector('#modal-confidential')

describeServer('#778 Schalter „vertraulich“ im Modal', () => {
  it('Bestand null: angezeigt als an, Titel ändern bewahrt null', async () => {
    const c = card({ confidential: null }); open(c); await warte()
    expect(cb().checked).toBe(true); expect(cb().hidden).toBe(false)
    document.querySelector('#modal-title').value = 'Neu'
    esc(); await warte()
    expect(updates.length).toBe(1)
    expect(updates[0].title).toBe('Neu'); expect(updates[0].confidential).toBe(null)
  })
  it('Bestand null: Abschalten schickt false', async () => {
    const c = card({ confidential: null }); open(c); await warte()
    cb().checked = false; esc(); await warte()
    expect(updates[0]?.confidential).toBe(false)
  })
  it('Nur Schalter anklicken ist dirty und schickt true', async () => {
    const c = card(); open(c); await warte()
    expect(cb().hidden).toBe(false)
    cb().checked = true; esc(); await warte()
    expect(updates[0]?.confidential).toBe(true)
  })
  it('Nicht bedient: Schlüssel fehlt', async () => {
    const c = card(); open(c); await warte()
    document.querySelector('#modal-title').value = 'X'; esc(); await warte()
    expect('confidential' in updates[0]).toBe(false)
  })
  it('Seite aus: Schalter versteckt, nichts gesendet', async () => {
    seiteWert = 'aus'
    const c = card(); open(c); await warte()
    expect(cb().hidden).toBe(true)
    cb().checked = true
    document.querySelector('#modal-title').value = 'X'; esc(); await warte()
    expect('confidential' in updates[0]).toBe(false)
  })
  it('verspätete Antwort von Karte A versteckt den Schalter der vertraulichen Karte B', async () => {
    seiteResolver = null
    const a = card({ id: 'a' }); open(a, [a]); 
    const erste = seiteResolver
    Modal.close(); await warte()
    seiteResolver = 'sofort'; seiteWert = 'aus'
    const b = card({ id: 'b', confidential: true }); open(b, [a, b]); await warte()
    expect(cb().hidden).toBe(false)
    erste('aus'); await warte()
    expect(cb().hidden, 'B ist vertraulich und muss abschaltbar bleiben').toBe(false)
  })
})
