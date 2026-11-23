/* @vitest-environment happy-dom */
/**
 * Karte #698 — Owner/Assignees/Bulk-Menü „Owner setzen“ im Server-Modus: Personen des
 * Boards statt fester Claude-Liste. Außerhalb des Server-Modus bleibt alles wie
 * vorher (ADR-0043 Check 1).
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer, describeOhneServer } from './helpers/build-flags.js'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn((k) => (k === 'userName' ? 'TestUser' : '')), set: vi.fn() },
}))

import { EventBus }       from '../src/eventbus.js'
import { Modal }          from '../src/modal.js'
import { State }          from '../src/state.js'
import { Profile }        from '../src/profile.js'
import { StoragePlugin }  from '../src/storage-plugin.js'
import {
  baueKandidaten, personenAusBoard, vergesseKandidatenCache, istKeinePerson,
} from '../src/bearbeiter-kandidaten.js'

const card = (o = {}) => ({
  id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: [], owner: '',
  complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'col-backlog', assignees: [], ...o,
})
const board = (cards = [], identities = []) => ({
  id: 'b1', title: 'B', identities, columnDefs: [{ id: 'col-backlog', title: 'Backlog' }],
  sprints: [{ id: 's1', title: 'S', cards }],
})
function open(c, b) {
  State.set({ board: b })
  EventBus.emit('card:edit', c)
  return document.querySelector('.modal-overlay')
}
const werte = (sel) => [...sel.options].map(o => o.value)
const warte = () => new Promise(r => setTimeout(r, 0))

beforeEach(() => { vergesseKandidatenCache(); EventBus.emit('config:language-changed'); document.body.innerHTML = '' })
afterEach(() => { vi.restoreAllMocks(); EventBus.emit('config:language-changed'); document.body.innerHTML = '' })

function serverModus(plugin) {
  vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
  vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue(plugin)
}

/* Reine Logik — in jedem Build lauffähig. */
describe('bearbeiter-kandidaten — reine Zusammenstellung', () => {
  it('ACL: angemeldete Person zuerst, Personen mit Rolle, Domain-Eintrag nie', () => {
    const l = baueKandidaten({
      board: board(), ich: { name: 'Anna', email: 'anna@firma.de' },
      acl: [{ email: 'anna@firma.de', role: 'owner' }, { email: 'bea@firma.de', role: 'editor' }, { email: '@firma.de', role: 'viewer' }],
    })
    expect(l).toEqual(['Anna', 'bea'])
  })
  it('Identity-Label gewinnt gegenüber dem Teil vor dem @', () => {
    const b = board([], [{ kind: 'user', label: 'Beate', email: 'bea@firma.de' }])
    expect(baueKandidaten({ board: b, ich: { name: 'Anna', email: 'a@f.de' }, acl: [{ email: 'bea@firma.de', role: 'viewer' }] }))
      .toEqual(['Anna', 'Beate'])
  })
  it('Rückfall ohne ACL: Personen vom Board, ohne Claude/User/external', () => {
    const b = board([card({ owner: 'Claude Opus', assignees: ['Carl', 'external:x@y.de', 'User'],
      comments: [{ author: 'Dora' }, { author: 'agent:recherche' }] })], [{ kind: 'user', label: 'Emil' }])
    expect(personenAusBoard(b).sort()).toEqual(['Carl', 'Dora', 'Emil'])
    expect(baueKandidaten({ board: b, ich: { name: 'Anna', email: 'a@f.de' }, acl: [] }).sort())
      .toEqual(['Anna', 'Carl', 'Dora', 'Emil'])
  })
  it('Server-Semantik: Agenten, System, Claude-Modelle fallen raus, „Claude Dupont“ bleibt', () => {
    ['agent:open-webui', 'agent:recherche', 'System', 'system', 'User', '', 'Claude', 'Claude Opus',
      'Claude Opus 5 (1M context)', 'claude-opus', 'Claude 3.5 Sonnet', 'external:x@y.de']
      .forEach(n => expect(istKeinePerson(n), n).toBe(true))
    ;['Claude Dupont', 'Anna', 'Claudette'].forEach(n => expect(istKeinePerson(n), n).toBe(false))
    const b = board([card({ owner: 'Claude Dupont', comments: [{ author: 'agent:open-webui' }, { author: 'agent:recherche' }, { author: 'System' }] })])
    expect(personenAusBoard(b)).toEqual(['Claude Dupont'])
  })
  it('angemeldete Person bleibt auch bei Modellnamen wählbar (bewusster Grenzfall)', () => {
    expect(baueKandidaten({ board: board(), ich: { name: 'Claude Opus', email: 'c@f.de' }, acl: [] })).toEqual(['Claude Opus'])
  })
  it('ohne Anmeldung (Legacy-Bypass): lokale userName-Einstellung', () => {
    expect(baueKandidaten({ board: board(), ich: { name: null, email: null }, acl: [] })).toEqual(['TestUser'])
  })
})

describeServer('Modal — Server-Modus (#698)', () => {
  it('(a) ACL lesbar: Personen + angemeldeter Name, keine Claude-Modelle, kein „User“', async () => {
    serverModus({
      getCurrentUser: async () => ({ name: 'Anna', email: 'anna@firma.de' }),
      aclList: async () => [{ email: 'anna@firma.de', role: 'owner' }, { email: 'bea@firma.de', role: 'viewer' }, { email: '@firma.de', role: 'viewer' }],
    })
    const ov = open(card(), board())
    await warte()
    expect(werte(ov.querySelector('#modal-owner'))).toEqual(['', 'Anna', 'bea'])
    expect(werte(ov.querySelector('#modal-assignees'))).toEqual(['Anna', 'bea'])
  })

  it('(b) ACL nicht lesbar (403 → []) und Netzfehler: Rückfall ohne Ausnahme', async () => {
    const b = board([card({ id: 'x', owner: 'Carl' })])
    serverModus({ getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }), aclList: async () => [] })
    let ov = open(card(), b)
    await warte()
    expect(werte(ov.querySelector('#modal-owner'))).toEqual(['', 'Anna', 'Carl'])
    EventBus.emit('config:language-changed'); document.body.innerHTML = ''; vergesseKandidatenCache()
    vi.restoreAllMocks()
    serverModus({ getCurrentUser: async () => { throw new Error('netz') }, aclList: async () => { throw new Error('netz') } })
    ov = open(card(), b)
    await warte()
    expect(werte(ov.querySelector('#modal-owner'))).toEqual(['', 'TestUser', 'Carl'])
  })

  it('(c) bestehende Werte bleiben als Option erhalten und ausgewählt', async () => {
    serverModus({
      getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }),
      aclList: async () => [{ email: 'a@f.de', role: 'owner' }],
    })
    const ov = open(card({ owner: 'Claude Opus', assignees: ['User', 'external:x@y.de', 'Anna'] }), board())
    await warte()
    const owner = ov.querySelector('#modal-owner')
    expect(owner.value).toBe('Claude Opus')
    expect(werte(owner)).toContain('Claude Opus')
    const ass = ov.querySelector('#modal-assignees')
    expect([...ass.selectedOptions].map(o => o.value).sort()).toEqual(['Anna', 'User', 'external:x@y.de'])
    expect(werte(ass)).not.toContain('Claude Sonnet')
  })

  it('Auswahl des Nutzers überlebt das nachträgliche Einspielen der Liste', async () => {
    let frei
    const gate = new Promise(r => { frei = r })
    serverModus({
      getCurrentUser: async () => { await gate; return { name: 'Anna', email: 'a@f.de' } },
      aclList: async () => [{ email: 'a@f.de', role: 'owner' }, { email: 'bea@f.de', role: 'editor' }],
    })
    const ov = open(card(), board([card({ id: 'y', owner: 'Carl' })]))
    ov.querySelector('#modal-owner').value = 'Carl'
    frei(); await warte(); await warte()
    expect(ov.querySelector('#modal-owner').value).toBe('Carl')
  })
})

describeOhneServer('Modal — ohne Server-Build unverändert (#698 d)', () => {
  it('Claude-Optionen und userName wie bisher, auch wenn Profile restStorage meldet', () => {
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    const ov = open(card(), board())
    expect(werte(ov.querySelector('#modal-owner'))).toEqual(['', 'TestUser', 'Claude', 'Claude Sonnet', 'Claude Opus', 'Claude Haiku', 'Claude Fable'])
    expect(werte(ov.querySelector('#modal-assignees'))).toEqual(['TestUser', 'Claude', 'Claude Sonnet', 'Claude Opus', 'Claude Haiku', 'Claude Fable'])
  })
})

describe('Modal — Server-Build ohne REST-Speicher unverändert (#698 d)', () => {
  it('Claude-Optionen bleiben, wenn feature.restStorage fehlt', () => {
    vi.spyOn(Profile, 'has').mockReturnValue(false)
    const ov = open(card(), board())
    expect(werte(ov.querySelector('#modal-owner'))).toEqual(['', 'TestUser', 'Claude', 'Claude Sonnet', 'Claude Opus', 'Claude Haiku', 'Claude Fable'])
  })
})

/* Karte #699 — Namensliste GET /boards/{id}/mitglieder (jede Rolle, ohne Adressen). */
describe('bearbeiter-kandidaten — Mitglieder-Namen (#699)', () => {
  it('Namensliste geht vor ACL und Board-Rückfall, Doubletten mit der eigenen Person fallen', () => {
    const b = board([card({ owner: 'Altbestand' })])
    const l = baueKandidaten({
      board: b, ich: { name: 'Anna', email: 'anna@firma.de' },
      acl: [{ email: 'zoe@firma.de', role: 'editor' }],
      mitglieder: ['anna', 'Bea Beispiel', 'agent:recherche', 'Claude Opus'],
    })
    expect(l).toEqual(['Anna', 'Bea Beispiel'])
  })
  it('leere Namensliste → Personen aus dem Board (KO-8c), fehlende (null) → ACL, dann Board', () => {
    const b = board([card({ owner: 'Carla' })])
    expect(baueKandidaten({ board: b, ich: { name: 'Anna', email: 'a@f.de' }, acl: [{ email: 'bea@f.de', role: 'viewer' }], mitglieder: [] }))
      .toEqual(['Anna', 'Carla'])
    expect(baueKandidaten({ board: b, ich: { name: 'Anna', email: 'a@f.de' }, acl: [{ email: 'bea@f.de', role: 'viewer' }], mitglieder: null }))
      .toEqual(['Anna', 'bea'])
    expect(baueKandidaten({ board: b, ich: { name: 'Anna', email: 'a@f.de' }, acl: [], mitglieder: null }))
      .toEqual(['Anna', 'Carla'])
  })
})

describeServer('Modal — Server-Modus mit Mitglieder-Endpunkt (#699)', () => {
  it('Editor ohne ACL-Recht sieht alle Personen; die ACL wird gar nicht erst gefragt', async () => {
    const aclList = vi.fn(async () => [])
    serverModus({
      getCurrentUser: async () => ({ name: 'Erik', email: 'erik@f.de' }),
      mitgliederNamen: async () => ['Erik', 'Nie Gesehen', 'Olga'],
      aclList,
    })
    const o = open(card(), board())
    await warte(); await warte()
    expect(werte(o.querySelector('#modal-owner'))).toEqual(['', 'Erik', 'Nie Gesehen', 'Olga'])
    expect(werte(o.querySelector('#modal-assignees'))).toEqual(['Erik', 'Nie Gesehen', 'Olga'])
    expect(aclList).not.toHaveBeenCalled()
  })
  it('älterer Server (Namensliste null) → ACL wie bisher', async () => {
    serverModus({
      getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }),
      mitgliederNamen: async () => null,
      aclList: async () => [{ email: 'a@f.de', role: 'owner' }, { email: 'bea@f.de', role: 'viewer' }],
    })
    const o = open(card(), board())
    await warte(); await warte()
    expect(werte(o.querySelector('#modal-assignees'))).toEqual(['Anna', 'bea'])
  })
})
