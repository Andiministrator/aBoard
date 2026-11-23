/**
 * Karte #711 — Bulk-Menüs: F-1729 (Aussenklick-Listener) und F-1730 (spätes
 * Nachladen von „Owner setzen“). Alle vier Menüs, offen und geschlossen.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'
import { EventBus }       from '../src/eventbus.js'
import { State }          from '../src/state.js'
import { Profile }        from '../src/profile.js'
import { Renderer }       from '../src/renderer.js'
import { I18n }           from '../src/i18n.js'
import { BulkSelect }     from '../src/bulk-select.js'
import { Shortcuts }      from '../src/shortcuts.js'
import { StoragePlugin }  from '../src/storage-plugin.js'
import { vergesseKandidatenCache } from '../src/bearbeiter-kandidaten.js'

const card = (o = {}) => ({
  id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: ['x'], owner: '',
  complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'col-backlog', assignees: [], ...o,
})
const board = () => ({
  id: 'b1', title: 'B', identities: [], columnDefs: [{ id: 'col-backlog', title: 'Backlog' }],
  sprints: [{ id: 's1', title: 'S', cards: [card({ owner: 'Zora' })] }],
})
const warte = () => new Promise(r => setTimeout(r, 0))
const raeumeAuf = () => [...document.body.children].forEach(c => { if (!c.id?.startsWith('bulk-')) c.remove() })
const dropdown = (key) => [...document.querySelectorAll('#bulk-action-bar .bulk-dropdown')]
  .find(d => d.querySelector('button')?.textContent === I18n.t(key))
const offen = (m) => m.classList.contains('bulk-dropdown-menu--open')
const klickAussen = () => document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
const klickInnen = (m) => m.dispatchEvent(new Event('pointerdown', { bubbles: true }))

/* F-1784: In der App ist der globale Tastatur-Handler immer aktiv (Escape hebt die Bulk-Auswahl
   auf, ADR-0035). `Shortcuts.init()` ist modulglobal und einmalig — wer es erst in einem späten
   Block aufruft, macht alle früheren Tests reihenfolgeabhängig. Deshalb einmal für die ganze Datei, vor
   jedem Test — so ist die Datei im Shuffle-Lauf reihenfolgeunabhängig (Fehlerklasse 18). */
Shortcuts.init()

beforeEach(() => {
  vergesseKandidatenCache(); EventBus.emit('config:language-changed'); raeumeAuf()
  document.body.insertAdjacentHTML('afterbegin', '<div id="board-header"></div><div id="board-columns"></div>')
})
/* Offene Menüs vor dem Abbau schließen: ihr Escape-Listener hängt am document (Capture) und
   überlebt das Entfernen des DOM — ein Test, der ein Menü offen lässt, verschluckt sonst das
   Escape eines späteren (nur unter Shuffle sichtbar, Seed 5). */
const schliesseOffene = () => document.querySelectorAll('.bulk-dropdown-menu--open')
  .forEach(m => m.previousElementSibling?.click())
afterEach(() => { schliesseOffene(); vi.restoreAllMocks(); EventBus.emit('config:language-changed'); raeumeAuf() })

function aufbauen() {
  const b = board()
  State.set({ board: b })
  Renderer.renderBoard({ board: b })
  BulkSelect.toggle?.('c1')
}

describe('F-1729 — Aussenklick schließt auch nach einem Klick ins Menü (alle vier Menüs)', () => {
  const alle = ['bulk.move', 'bulk.owner', 'bulk.label-add', 'bulk.label-remove']
  for (const key of alle) {
    it(`${key}: Klick ins Menü verbraucht den Listener nicht, Klick daneben schließt`, async () => {
      aufbauen()
      const dd = dropdown(key), btn = dd.querySelector('button'), m = dd.querySelector('.bulk-dropdown-menu')
      if (offen(m)) btn.click()
      btn.click(); await warte()
      expect(offen(m)).toBe(true)
      klickInnen(m); klickInnen(m)
      expect(offen(m)).toBe(true) /* im Menü schließt nichts */
      klickAussen()
      expect(offen(m)).toBe(false)
    })
    it(`${key}: geschlossen bleibt kein Listener hängen (Wiederöffnen, Klick daneben schließt)`, async () => {
      aufbauen()
      const dd = dropdown(key), btn = dd.querySelector('button'), m = dd.querySelector('.bulk-dropdown-menu')
      if (offen(m)) btn.click()
      const spy = vi.spyOn(document, 'removeEventListener')
      btn.click(); await warte(); btn.click() /* auf, zu */
      expect(spy.mock.calls.some(c => c[0] === 'pointerdown')).toBe(true)
      btn.click(); await warte()
      klickAussen()
      expect(offen(m)).toBe(false)
    })
  }
})

describeServer('F-1730 — spätes Nachladen von „Owner setzen“', () => {
  function serverModus(plugin) {
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue(plugin)
  }
  function plugin() {
    let frei
    const gate = new Promise(r => { frei = r })
    return {
      frei,
      p: { getCurrentUser: async () => { await gate; return { name: 'Anna', email: 'a@f.de' } }, mitgliederNamen: async () => ['Anna', 'Mia'] },
    }
  }
  const texte = (m) => [...m.querySelectorAll('.bulk-dropdown-item')].map(e => e.textContent)
  async function start() {
    const { frei, p } = plugin()
    serverModus(p)
    aufbauen()
    const dd = dropdown('bulk.owner'), btn = dd.querySelector('button'), m = dd.querySelector('.bulk-dropdown-menu')
    if (offen(m)) btn.click()
    return { frei, btn, m }
  }

  it('Fokus auf einem Eintrag: die Antwort baut nicht neu auf, ergänzt nur (Fokus bleibt)', async () => {
    const { frei, btn, m } = await start()
    btn.click(); await warte()
    const eintrag = m.querySelectorAll('.bulk-dropdown-item')[1]
    eintrag.focus()
    expect(document.activeElement).toBe(eintrag)
    expect(texte(m)).not.toContain('Mia')
    frei(); for (let i = 0; i < 5; i++) await warte()
    expect(texte(m)).toContain('Mia')
    expect(m.contains(eintrag)).toBe(true) /* derselbe Knoten, nicht neu gebaut */
    expect(document.activeElement).toBe(eintrag)
  })

  it('Fokus nicht im Menü: die Antwort baut sortiert neu auf', async () => {
    const { frei, btn, m } = await start()
    btn.click(); await warte()
    const alt = m.querySelectorAll('.bulk-dropdown-item')[1]
    frei(); for (let i = 0; i < 5; i++) await warte()
    expect(texte(m).filter(t => !t.startsWith('—')).slice(-3)).toEqual(['Anna', 'Mia', 'Zora'])
    expect(m.contains(alt)).toBe(false)
  })

  it('Menü inzwischen geschlossen: die Antwort lässt es unberührt', async () => {
    const { frei, btn, m } = await start()
    btn.click(); await warte()
    const vorher = [...m.children]
    btn.click() /* zu */
    frei(); for (let i = 0; i < 5; i++) await warte()
    expect([...m.children]).toEqual(vorher)
  })

  it('Menü geschlossen und neu geöffnet: nur die Antwort des jüngsten Öffnens zählt', async () => {
    const { frei, btn, m } = await start()
    btn.click(); await warte()
    btn.click(); btn.click(); await warte() /* zu, wieder auf: neuer Lauf */
    const ein = m.querySelectorAll('.bulk-dropdown-item')[1]
    ein.focus()
    frei(); for (let i = 0; i < 5; i++) await warte()
    /* der ältere Lauf darf nicht neu aufbauen; der jüngere ergänzt nur (Fokus im Menü) */
    expect(m.contains(ein)).toBe(true)
    expect(document.activeElement).toBe(ein)
  })
})

describeServer('F-1763/F-1764 — Owner-Menü: Antworten ordnen, Ergänzen vor dem Claude-Kopf', () => {
  const brd = (id, owner) => ({ ...board(), id, title: id, sprints: [{ id: 's1', title: 'S', cards: [card({ owner })] }] })
  const texte = (m) => [...m.querySelectorAll('.bulk-dropdown-item')].map(e => e.textContent)
  const spaeter = () => (async () => { for (let i = 0; i < 6; i++) await warte() })()
  function serverModus(plugin) {
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue(plugin)
  }
  function zeige(b) { State.set({ board: b }); Renderer.renderBoard({ board: b }); BulkSelect.toggle?.('c1') }
  function menue() {
    const dd = dropdown('bulk.owner'), btn = dd.querySelector('button'), m = dd.querySelector('.bulk-dropdown-menu')
    if (offen(m)) btn.click()
    return { btn, m }
  }

  it('F-1776: Ergänzen sortiert in eine vorhandene Personengruppe ein (Mia vor Zora, vor dem Claude-Kopf)', async () => {
    let frei; const gate = new Promise(r => { frei = r })
    serverModus({ getCurrentUser: async () => { await gate; return { name: 'Anna', email: 'a@f.de' } }, mitgliederNamen: async () => ['Mia'] })
    const b = brd('b1', 'Zora'); b.sprints[0].cards.push(card({ id: 'c2', displayId: 2, owner: 'Claude Opus' }))
    zeige(b)
    const { btn, m } = menue()
    btn.click(); await warte()
    m.querySelectorAll('.bulk-dropdown-item')[0].focus()
    expect(texte(m)).toContain('Zora')
    frei(); await spaeter()
    const t = texte(m)
    expect(t.indexOf('Mia')).toBeGreaterThan(-1)
    expect(t.indexOf('Mia')).toBeLessThan(t.indexOf('Zora'))
    expect(t.indexOf('Zora')).toBeLessThan(t.findIndex(x => x.startsWith('── ')))
  })

  it('F-1764: Ergänzen bei Fokus im Menü fügt Personen sortiert vor „── Claude-Modelle“ ein', async () => {
    let frei; const gate = new Promise(r => { frei = r })
    serverModus({ getCurrentUser: async () => { await gate; return { name: 'Anna', email: 'a@f.de' } }, mitgliederNamen: async () => ['Anna', 'Mia'] })
    zeige(brd('b1', 'Claude Opus'))
    const { btn, m } = menue()
    btn.click(); await warte()
    m.querySelectorAll('.bulk-dropdown-item')[0].focus()
    frei(); await spaeter()
    const t = texte(m)
    const kopf = t.findIndex(x => x.startsWith('── '))
    expect(kopf).toBeGreaterThan(-1)
    expect(t.indexOf('Anna')).toBeLessThan(kopf)
    expect(t.indexOf('Mia')).toBeLessThan(kopf)
    expect(t.indexOf('Anna')).toBeLessThan(t.indexOf('Mia')) /* sortiert wie beim vollen Aufbau */
    expect(t.slice(kopf + 1)).toContain('Claude Opus') /* Claude-Gruppe bleibt hinter dem Kopf */
  })

  it('F-1763: zwei Öffnungen am selben Board, die ältere Antwort kommt zuletzt — das jüngere Menü bleibt', async () => {
    /* Je Öffnen ruft der Aufbau getCurrentUser mehrfach; die Tore werden je Lauf gruppiert. */
    const tore = { 1: [], 2: [] }; let nr = 1; let welche = 1
    serverModus({
      getCurrentUser: () => { const mein = nr; return new Promise(r => tore[mein].push(() => { welche = mein; r({ name: 'Anna', email: 'a@f.de' }) })) },
      mitgliederNamen: async () => (welche === 1 ? ['AlteMia'] : ['NeueMia']),
    })
    const oeffne = (lauf) => tore[lauf].forEach(f => f())
    zeige(brd('b1', 'Zora'))
    const { btn, m } = menue()
    btn.click(); await warte() /* Lauf 1 hängt */
    nr = 2
    btn.click(); btn.click(); await warte() /* zu, wieder auf: Lauf 2 */
    oeffne(2); await spaeter() /* jüngere Antwort zuerst */
    expect(texte(m)).toContain('NeueMia')
    vergesseKandidatenCache() /* Cache-Ablauf: die ältere Antwort bringt jetzt andere Namen */
    oeffne(1); await spaeter() /* ältere zuletzt */
    expect(texte(m)).toContain('NeueMia')
    expect(texte(m)).not.toContain('AlteMia')
  })

  it('F-1763: Antwort vom alten Board überschreibt nach dem Boardwechsel das Menü des neuen nicht', async () => {
    const tore = { 1: [], 2: [] }; let nr = 1 /* Tore je Lauf gruppiert (mehrere getCurrentUser je Öffnen) */
    serverModus({
      getCurrentUser: () => { const mein = nr; return new Promise(r => tore[mein].push(() => r({ name: 'Anna', email: 'a@f.de' }))) },
      mitgliederNamen: async (id) => (id === 'A' ? ['AlterKollege'] : ['NeuKollege']),
    })
    zeige(brd('A', 'Zora'))
    const { btn, m } = menue()
    btn.click(); await warte() /* Lauf 1 auf Board A */
    btn.click() /* zu */
    zeige(brd('B', 'Xaver'))
    nr = 2
    btn.click(); await warte() /* Lauf 2 auf Board B */
    tore[2].forEach(f => f()); await spaeter()
    tore[1].forEach(f => f()); await spaeter()
    const t = texte(m)
    expect(t).toContain('Xaver')
    expect(t).not.toContain('Zora')
    expect(t).not.toContain('AlterKollege')
  })

  it('F-1763: Menü bleibt offen, Board wechselt ohne neues Öffnen — die späte Antwort des alten Boards wird verworfen', async () => {
    const tore = []
    serverModus({
      getCurrentUser: () => new Promise(r => tore.push(() => r({ name: 'Anna', email: 'a@f.de' }))),
      mitgliederNamen: async () => ['AlterKollege'],
    })
    zeige(brd('A', 'Zora'))
    const { btn, m } = menue()
    btn.click(); await warte()
    const vorher = texte(m)
    State.set({ board: brd('B', 'Xaver') }) /* Board-Id wechselt, kein Neu-Öffnen */
    tore.forEach(f => f()); await spaeter()
    expect(texte(m)).toEqual(vorher)
    expect(texte(m)).not.toContain('AlterKollege')
  })
})

describe('F-1769 — Escape und Tab-Verlassen schließen (alle vier Menüs)', () => {
  const alle = ['bulk.move', 'bulk.owner', 'bulk.label-add', 'bulk.label-remove']
  const esc = (ziel) => ziel.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
  for (const key of alle) {
    it(`${key}: Escape schließt und gibt dem Auslöser den Fokus`, async () => {
      aufbauen()
      const dd = dropdown(key), btn = dd.querySelector('button'), m = dd.querySelector('.bulk-dropdown-menu')
      if (offen(m)) btn.click()
      btn.click(); await warte()
      expect(offen(m)).toBe(true)
      const fokussierbar = m.querySelector('input, button')
      if (fokussierbar) fokussierbar.focus()
      esc(document.activeElement && m.contains(document.activeElement) ? document.activeElement : btn)
      expect(offen(m)).toBe(false)
      expect(m.hasAttribute('inert')).toBe(true)
      expect(document.activeElement).toBe(btn)
    })
    it(`${key}: Fokus verlässt das Umfeld (Tab) → schließt, Fokus wird nicht zurückgezogen`, async () => {
      aufbauen()
      const dd = dropdown(key), btn = dd.querySelector('button'), m = dd.querySelector('.bulk-dropdown-menu')
      if (offen(m)) btn.click()
      btn.click(); await warte()
      const draussen = document.createElement('button'); document.body.appendChild(draussen);
      (m.querySelector('input, button') || btn).focus()
      const drin = document.activeElement
      /* focusout feuert, solange der Fokus noch im Menü steht: er darf nicht zum Auslöser springen */
      dd.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: draussen }))
      expect(offen(m)).toBe(false)
      expect(document.activeElement).toBe(drin)
      expect(document.activeElement).not.toBe(btn === drin ? null : btn)
      draussen.focus()
      expect(document.activeElement).toBe(draussen)
    })
    it(`${key}: Fokuswechsel innerhalb des Umfelds schließt nicht; geschlossen greift das Menü bei Escape nicht ein`, async () => {
      aufbauen()
      const dd = dropdown(key), btn = dd.querySelector('button'), m = dd.querySelector('.bulk-dropdown-menu')
      if (offen(m)) btn.click()
      btn.click(); await warte()
      dd.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: m }))
      dd.dispatchEvent(new FocusEvent('focusout', { bubbles: true, relatedTarget: null }))
      expect(offen(m)).toBe(true)
      btn.click() /* zu */
      const draussen = document.createElement('button'); document.body.appendChild(draussen); draussen.focus()
      /* Mit dem globalen Handler (App-Wirklichkeit) räumt Escape die Auswahl ab — das ist
         ADR-0035 und gehört dem Handler, nicht dem Menü. Geprüft wird, dass das Menü selbst
         nichts tut: es bleibt zu, der Fokus wird nicht zum Auslöser gezogen. */
      const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
      btn.dispatchEvent(ev)
      expect(offen(m)).toBe(false)
      expect(document.activeElement).toBe(draussen)
      expect(document.activeElement).not.toBe(btn)
    })
  }
  it('bulk.label-add: Escape im Eingabefeld schließt genau einmal, Fokus beim Auslöser', async () => {
    aufbauen()
    const dd = dropdown('bulk.label-add'), btn = dd.querySelector('button'), m = dd.querySelector('.bulk-dropdown-menu')
    if (offen(m)) btn.click()
    btn.click(); await warte(); await warte()
    const inp = m.querySelector('input'); inp.focus()
    const spy = vi.spyOn(btn, 'focus')
    esc(inp)
    expect(offen(m)).toBe(false)
    expect(spy).toHaveBeenCalledTimes(1)
    expect(document.activeElement).toBe(btn)
  })
})

describe('F-1775 — Escape und die Bulk-Auswahl (globaler Handler aktiv)', () => {
  const alle = ['bulk.move', 'bulk.owner', 'bulk.label-add', 'bulk.label-remove']
  const esc = (ziel) => ziel.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
  function start(key) {
    aufbauen()
    if (BulkSelect.count() === 0) BulkSelect.toggle('c1')
    const dd = dropdown(key), btn = dd.querySelector('button'), m = dd.querySelector('.bulk-dropdown-menu')
    if (offen(m)) btn.click()
    return { btn, m }
  }
  for (const key of alle) {
    it(`${key}: Escape im offenen Menü schließt, die Auswahl bleibt`, async () => {
      const { btn, m } = start(key)
      const vor = BulkSelect.count()
      expect(vor).toBe(1)
      btn.click(); await warte(); await warte()
      const f = m.querySelector('input, button'); expect(f).toBeTruthy(); f.focus()
      /* kein stiller Rückfall auf den Auslöser: der Fokus muss wirklich im Menü stehen */
      expect(m.contains(document.activeElement)).toBe(true)
      if (key === 'bulk.label-add') expect(document.activeElement.tagName).toBe('INPUT')
      esc(document.activeElement)
      expect(offen(m)).toBe(false)
      expect(BulkSelect.count()).toBe(vor)
    })
    it(`${key}: Escape bei geschlossenem Menü hebt die Auswahl auf (ADR-0035)`, async () => {
      const { btn } = start(key)
      expect(BulkSelect.count()).toBe(1)
      esc(btn)
      expect(BulkSelect.count()).toBe(0)
    })
  }
})
