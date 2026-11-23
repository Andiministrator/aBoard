/* @vitest-environment happy-dom */
/**
 * Nacharbeit Karte #698 (Kritiker-Befunde KO-1, KO-4a, KO-5, KO-6, KO-7, KO-8):
 * Claude-Gruppe nur auf Boards, die Claude benutzen; Wachen gegen veraltete
 * asynchrone Antworten; Bulk-Menü „Owner setzen“ im Server-Zweig; Modus-Wechsel
 * ohne Datenverlust; Cache-Regeln.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn((k) => (k === 'userName' ? 'TestUser' : (k === 'boardMode' ? 'standalone' : ''))), set: vi.fn() },
}))

import { EventBus }       from '../src/eventbus.js'
import { Modal }          from '../src/modal.js' // eslint-disable-line no-unused-vars -- registriert die EventBus-Handler
import { State }          from '../src/state.js'
import { Profile }        from '../src/profile.js'
import { Renderer }       from '../src/renderer.js'
import { I18n }           from '../src/i18n.js'
import { StoragePlugin }  from '../src/storage-plugin.js'
import {
  baueKandidaten, boardNutztClaude, claudeGruppe, ladeKandidaten, vergesseKandidatenCache,
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
const werte = (sel) => [...sel.options].map(o => o.value)
const warte = () => new Promise(r => setTimeout(r, 0))
function open(c, b) {
  State.set({ board: b })
  EventBus.emit('card:edit', c)
  return document.querySelector('.modal-overlay')
}
function serverModus(plugin) {
  vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
  vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue(plugin)
}
const claudeNamen = ['Claude', 'Claude Sonnet', 'Claude Opus', 'Claude Haiku', 'Claude Fable']
const anna = { getCurrentUser: async () => ({ name: 'Anna', email: 'anna@f.de' }), mitgliederNamen: async () => ['Anna', 'Bea'] }

/* Die Bulk-Leiste entsteht einmal pro Modul (_bulkBarInitialized) und hängt am
   Body — sie darf das Aufräumen nicht verlieren, sonst fehlt sie den Folgetests. */
const raeumeAuf = () => [...document.body.children].forEach(c => { if (!c.id?.startsWith('bulk-')) c.remove() })
beforeEach(() => { vergesseKandidatenCache(); EventBus.emit('config:language-changed'); raeumeAuf() })
afterEach(() => { vi.restoreAllMocks(); EventBus.emit('config:language-changed'); raeumeAuf() })

describe('KO-1 — boardNutztClaude (reine Funktion)', () => {
  it('Owner oder Assignee mit Claude-Kennung → true', () => {
    expect(boardNutztClaude(board([card({ owner: 'Claude Opus' })]))).toBe(true)
    expect(boardNutztClaude(board([card({ assignees: ['Anna', 'Claude Opus 5 (1M context)'] })]))).toBe(true)
    expect(boardNutztClaude(board([card({ owner: 'claude-opus' })]))).toBe(true)
  })
  it('Personen, Agenten, „Claude Dupont“, leeres Board → false', () => {
    expect(boardNutztClaude(board([card({ owner: 'Anna', assignees: ['agent:open-webui', 'Claudette', 'Claude Dupont'] })]))).toBe(false)
    expect(boardNutztClaude(board())).toBe(false)
    expect(boardNutztClaude(null)).toBe(false)
  })
  it('claudeGruppe liefert die vier Modelle nur bei Nutzung', () => {
    expect(claudeGruppe(board([card({ owner: 'Claude' })]))).toEqual(claudeNamen)
    expect(claudeGruppe(board([card({ owner: 'Anna' })]))).toEqual([])
  })
})

describeServer('KO-1 — Modal: Claude-Gruppe nach den Personen', () => {
  it('Board mit Claude-Karten: optgroup mit vier Modellen NACH den Personen, Agenten nie', async () => {
    serverModus(anna)
    const b = board([card({ id: 'x', owner: 'Claude Opus', assignees: ['agent:open-webui'] })])
    const ov = open(card({ id: 'y' }), b)
    await warte(); await warte()
    for (const id of ['#modal-owner', '#modal-assignees']) {
      const sel = ov.querySelector(id)
      const gruppe = sel.querySelector('optgroup')
      expect(gruppe, id).not.toBeNull()
      expect(gruppe.label).toBe(I18n.t('card.owner.gruppe-claude'))
      expect(werte({ options: gruppe.querySelectorAll('option') })).toEqual(claudeNamen)
      /* Personen stehen vor der Gruppe */
      const alle = werte(sel).filter(v => v !== '')
      expect(alle.slice(0, 2)).toEqual(['Anna', 'Bea'])
      expect(alle.join('|')).not.toContain('agent:')
    }
  })
  it('Board ohne Claude-Karten (z. B. Team-Board): nur Personen, keine Gruppe', async () => {
    serverModus(anna)
    const ov = open(card(), board([card({ id: 'x', owner: 'Bea' })]))
    await warte(); await warte()
    expect(ov.querySelector('#modal-owner optgroup')).toBeNull()
    expect(werte(ov.querySelector('#modal-owner'))).toEqual(['', 'Anna', 'Bea'])
    expect(werte(ov.querySelector('#modal-assignees'))).toEqual(['Anna', 'Bea'])
  })
  it('Karten-Assignee „Claude Opus“ bleibt in der Gruppe ausgewählt', async () => {
    serverModus(anna)
    const b = board([card({ id: 'x', owner: 'Claude' })])
    const ov = open(card({ id: 'y', owner: 'Claude Opus', assignees: ['Claude Haiku', 'Anna'] }), b)
    await warte(); await warte()
    expect(ov.querySelector('#modal-owner').value).toBe('Claude Opus')
    expect([...ov.querySelector('#modal-assignees').selectedOptions].map(o => o.value).sort()).toEqual(['Anna', 'Claude Haiku'])
  })
})

describeServer('KO-5 — Bulk-Menü „Owner setzen“ im Server-Zweig', () => {
  /* Die Leiste entsteht einmal pro Modul (_bulkBarInitialized) — den Body hier
     nicht leeren, sonst fehlt sie in den Folgetests. */
  function ownerMenue() {
    const dds = [...document.querySelectorAll('#bulk-action-bar .bulk-dropdown')]
    return dds.find(d => d.querySelector('button')?.textContent === I18n.t('bulk.owner'))
  }
  const eintraege = (m) => [...m.querySelectorAll('.bulk-dropdown-item')].map(e => e.textContent)
  async function oeffne(b, plugin) {
    serverModus(plugin)
    State.set({ board: b })
    Renderer.renderBoard({ board: b })
    const dd = ownerMenue()
    const m = dd.querySelector('.bulk-dropdown-menu')
    /* Die Leiste überlebt die Tests (einmal pro Modul): ein noch offenes Menü
       eines Vortests erst schließen — der Klick schaltet um, er baut nur beim Öffnen auf. */
    if (m.classList.contains('bulk-dropdown-menu--open')) dd.querySelector('button').click()
    dd.querySelector('button').click()
    await warte(); await warte()
    return m
  }
  beforeEach(() => {
    document.body.insertAdjacentHTML('afterbegin', '<div id="board-header"></div><div id="board-columns"></div>')
  })

  it('Personen statt Claude-Liste; ohne Claude-Karten keine Claude-Gruppe', async () => {
    const b = board([card({ id: 'x', owner: 'Carl' })])
    const m = await oeffne(b, anna)
    const e = eintraege(m)
    expect(e).toEqual(expect.arrayContaining(['Anna', 'Bea', 'Carl']))
    expect(e.some(t => /^Claude/.test(t) || t.includes(I18n.t('card.owner.gruppe-claude')))).toBe(false)
  })
  it('Board mit Claude-Karten: Gruppe mit Unterüberschrift NACH den Personen', async () => {
    const b = board([card({ id: 'x', owner: 'Claude Opus' })])
    const m = await oeffne(b, anna)
    const e = eintraege(m)
    const kopf = e.findIndex(t => t.includes(I18n.t('card.owner.gruppe-claude')))
    expect(kopf).toBeGreaterThan(e.indexOf('Bea'))
    expect(e.slice(kopf + 1)).toEqual(['Claude', 'Claude Fable', 'Claude Haiku', 'Claude Opus', 'Claude Sonnet'])
    expect(e.slice(0, kopf)).not.toContain('Claude Opus')
  })
  it('nachgeladene Personen werden sortiert eingefügt (nicht ans Ende gehängt)', async () => {
    const b = board([card({ id: 'x', owner: 'Zora' })])
    let frei
    const gate = new Promise(r => { frei = r })
    const m = await oeffne(b, {
      getCurrentUser: async () => { await gate; return { name: 'Anna', email: 'a@f.de' } },
      mitgliederNamen: async () => ['Anna', 'Mia'],
    })
    expect(eintraege(m)).not.toContain('Mia')
    frei(); await warte(); await warte(); await warte()
    const personen = eintraege(m).filter(t => !t.startsWith('—') && !t.startsWith('-'))
    expect(personen).toEqual(['Anna', 'Mia', 'Zora'])
  })
})

describeServer('KO-6 — Wechsel Server-Modus → Nicht-Server-Pfad im selben Overlay', () => {
  it('feste Owner-Optionen kommen zurück, card.owner geht nicht verloren', async () => {
    const sp = vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue(anna)
    State.set({ board: board() })
    EventBus.emit('card:edit', card({ id: 'c0' }))
    await warte()
    EventBus.emit('modal:close')
    document.querySelector('.modal-overlay')?.classList.remove('modal-overlay--open')
    sp.mockImplementation(() => false)
    EventBus.emit('card:edit', card({ owner: 'Claude Opus' }))
    const sel = document.querySelector('#modal-owner')
    expect(werte(sel)).toEqual(['', 'TestUser', 'Claude', 'Claude Sonnet', 'Claude Opus', 'Claude Haiku', 'Claude Fable'])
    expect(sel.value).toBe('Claude Opus')
  })
})

describeServer('KO-4a — veraltete Antwort überschreibt die neuere Karte nicht', () => {
  it('Karte A öffnen, dann B, dann A-Promise auflösen → B bleibt unverändert', async () => {
    let frei
    const gate = new Promise(r => { frei = r })
    serverModus({
      getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }),
      mitgliederNamen: async () => { await gate; return ['Anna', 'Nur-Spaet'] },
    })
    const b = board()
    open(card({ id: 'A', owner: 'Anna' }), b)
    /* B: zweite Karte, deren Nachlade-Antwort sofort kommt */
    vergesseKandidatenCache()
    vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue({
      getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }), mitgliederNamen: async () => ['Anna', 'Bea'],
    })
    const ov = open(card({ id: 'B', owner: 'Bea' }), b)
    await warte(); await warte()
    const vorher = werte(ov.querySelector('#modal-owner'))
    expect(vorher).toEqual(['', 'Anna', 'Bea'])
    frei(); await warte(); await warte()
    expect(werte(ov.querySelector('#modal-owner'))).toEqual(vorher)
    expect(ov.querySelector('#modal-owner').value).toBe('Bea')
  })
})

describe('KO-7 — nie die volle Adresse als Name', () => {
  it('Name mit @ (Proxy ohne Anzeigename) → Teil vor dem @', () => {
    expect(baueKandidaten({ board: board(), ich: { name: 'anna@firma.de', email: 'anna@firma.de' }, acl: [], mitglieder: ['Bea'] }))
      .toEqual(['anna', 'Bea'])
  })
  it('nur E-Mail, kein Name → Teil vor dem @', () => {
    expect(baueKandidaten({ board: board(), ich: { name: null, email: 'carl@firma.de' }, acl: [], mitglieder: [] }))
      .toEqual(['carl'])
  })
})

describe('KO-8 — Cache-Regeln von ladeKandidaten', () => {
  const mit = (o) => vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue(o)
  afterEach(() => vi.useRealTimers())

  it('(a) /me-Fehlschlag (Exception) wird nicht gecacht: zwei Aufrufe fragen zweimal die Namensliste', async () => {
    const p = { getCurrentUser: vi.fn(async () => { throw new Error('Netz') }),
      mitgliederNamen: vi.fn(async () => ['Anna', 'Bea']) }
    mit(p)
    const b = board()
    await ladeKandidaten(b); await ladeKandidaten(b)
    expect(p.getCurrentUser).toHaveBeenCalledTimes(2)
    expect(p.mitgliederNamen).toHaveBeenCalledTimes(2)
  })
  it('(a) /me-Fehlschlag (fehler:true vom REST-Plugin) wird nicht gecacht', async () => {
    const p = { getCurrentUser: vi.fn(async () => ({ name: null, email: null, fehler: true })),
      mitgliederNamen: vi.fn(async () => ['Bea']) }
    mit(p)
    await ladeKandidaten(board()); await ladeKandidaten(board())
    expect(p.mitgliederNamen).toHaveBeenCalledTimes(2)
  })
  it('VO-11: Legacy-Bypass (Erfolg ohne Person) wird 60 s unter „ohne Person“ gecacht', async () => {
    vi.useFakeTimers()
    const p = { getCurrentUser: vi.fn(async () => ({ name: null, email: null })),
      mitgliederNamen: vi.fn(async () => ['Bea']) }
    mit(p)
    const b = board()
    await ladeKandidaten(b); await ladeKandidaten(b)
    expect(p.mitgliederNamen).toHaveBeenCalledTimes(1)
    vi.advanceTimersByTime(61_000)
    await ladeKandidaten(b)
    expect(p.mitgliederNamen).toHaveBeenCalledTimes(2)
  })
  it('(b) Personenwechsel verwirft den Cache', async () => {
    let wer = { name: 'Anna', email: 'a@f.de' }
    const p = { getCurrentUser: async () => wer,
      mitgliederNamen: vi.fn(async () => (wer.name === 'Anna' ? ['Anna', 'Bea'] : ['Dora', 'Bea'])) }
    mit(p)
    const b = board()
    expect(await ladeKandidaten(b)).toEqual(['Anna', 'Bea'])
    wer = { name: 'Dora', email: 'd@f.de' }
    expect(await ladeKandidaten(b)).toEqual(['Dora', 'Bea'])
    expect(p.mitgliederNamen).toHaveBeenCalledTimes(2)
  })
  it('gleiche Person, gleiches Board: Namensliste kommt aus dem Cache', async () => {
    const p = { getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }), mitgliederNamen: vi.fn(async () => ['Anna']) }
    mit(p)
    await ladeKandidaten(board()); await ladeKandidaten(board())
    expect(p.mitgliederNamen).toHaveBeenCalledTimes(1)
  })
  it('(c) mitglieder: [] → keine ACL-Abfrage, Personen aus dem Board; null → ACL', async () => {
    const aclList = vi.fn(async () => [{ email: 'zoe@f.de', role: 'editor' }])
    const b = board([card({ owner: 'Carla' })])
    mit({ getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }), mitgliederNamen: async () => [], aclList })
    expect(await ladeKandidaten(b)).toEqual(['Anna', 'Carla'])
    expect(aclList).not.toHaveBeenCalled()
    vergesseKandidatenCache()
    mit({ getCurrentUser: async () => ({ name: 'Anna', email: 'a@f.de' }), mitgliederNamen: async () => null, aclList })
    expect(await ladeKandidaten(b)).toEqual(['Anna', 'zoe'])
    expect(aclList).toHaveBeenCalledTimes(1)
  })
})
