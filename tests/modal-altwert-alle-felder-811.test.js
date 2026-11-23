/* @vitest-environment happy-dom */

/**
 * Karte #811 / F-1964 + F-1967 — Altwert-Prinzip für ALLE Modal-Felder.
 *
 * F-1964 (Fehlerklasse 194, generisch): Öffnen und Schliessen des Karten-Modals
 * ohne Eingabe darf bei keinem Feld einen anderen Wert zurückschreiben. Die
 * Schlüssel kommen aus `PATCH_FIELD_DEFAULTS` in src/modal.js (aus dem Quelltext
 * gelesen) — ein neues Feld dort ohne Fremdwert in FREMD_WERTE bricht den Test,
 * statt still ungeprüft zu bleiben.
 *
 * F-1967: ein fremder Aufwand steht als sichtbarer Hinweis am Feld,
 * verbunden per aria-describedby, und verschwindet für die nächste Karte.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { readFileSync } from 'node:fs'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn((k) => (k === 'userName' ? 'TestUser' : (k === 'boardMode' ? 'standalone' : ''))), set: vi.fn() },
}))

/* W-2: Kandidaten-Nachladen steuerbar; ohne `steuer.aktiv` unverändert durchgereicht. */
const steuer = vi.hoisted(() => ({ aktiv: false, sofort: [], spaeter: [], warte: null }))
vi.mock('../src/bearbeiter-kandidaten.js', async (orig) => {
  const echt = await orig()
  return {
    ...echt,
    kandidatenSofort: (...a) => steuer.aktiv ? [...steuer.sofort] : echt.kandidatenSofort(...a),
    ladeKandidaten: (...a) => steuer.aktiv ? new Promise(r => { steuer.warte = () => r([...steuer.spaeter]) }) : echt.ladeKandidaten(...a),
  }
})

import { EventBus } from '../src/eventbus.js'
import { State }    from '../src/state.js'
import { Modal }    from '../src/modal.js'
import { Profile }  from '../src/profile.js'
import { AdminClient } from '../src/admin-client.js'
import { ClientsProjectsClient } from '../src/clients-projects-client.js'
import { describeServer } from './helpers/build-flags.js'

void Modal

const quelle = readFileSync('src/modal.js', 'utf8')
const block  = quelle.match(/const PATCH_FIELD_DEFAULTS = \{([\s\S]*?)\n  \};/)[1]
/* Auch Schlüssel mit Ziffern, $ oder in Anführungszeichen (M-6). */
const SCHLUESSEL = [...block.matchAll(/(?:^|[,{\s])([A-Za-z_$][\w$]*|'[^']+'|"[^"]+"):\s/g)].map(m => m[1].replace(/^['"]|['"]$/g, ''))

const karte = (o = {}) => ({
  id: 'c-811', displayId: 811, title: 'Altkarte', description: '', agentContext: '',
  comments: [], attachments: [], links: [], activity: [], priority: 'mittel', labels: [],
  owner: '', complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'col-backlog', ...o,
})

/* Je Feld: Karten-Teil mit einem Fremdwert (Formular zeigt ihn anders oder leer). */
const FREMD_WERTE = {
  title:        { title: '  Mit Leerraum  ' },
  description:  { description: '  Text  \n\n' },
  agentContext: { agentContext: '  Kontext  ' },
  comments:     { comments: [{ id: 'k1', author: 'A', text: 'Hallo', created: '2026-10-01' }] },
  owner:        { owner: 5 },
  priority:     { priority: 'P1' },
  complexity:   { complexity: 'XL' },
  effort:       { effort: '30 min' },
  dueDate:      { dueDate: '11.10.2026' },
  startDate:    { startDate: '2026-13-45' },
  dueTime:      { dueDate: '2026-10-11', dueTime: '9:30' },
  startTime:    { startDate: '2026-10-11', startTime: '25:99' },
  recurrence:   { recurrence: { interval: 'weekly', day: '3', lastSpawned: '2026-10-01' } },
  project_id:   { project_id: 'p-1' },
  labels:       { labels: ['a,b', 5, '', ' x ', null] },
  assignees:    { assignees: ['Claude', 'Claude', 7] },
  qa:           { qa: 'true' },
  critical:     { critical: 'ja' },
  blocked:      { blocked: 1 },
}

/* Nutzereingabe je Feld (nur wo das Formular einen einfachen Zugriff bietet:
   description/agentContext liegen in Typora-Blöcken, comments in pendingComments,
   project_id ist nur im Server-Build ein Formularfeld). */
const EINGABE = {
  title:      { tu: o => { o.querySelector('#modal-title').value = 'Neu' },           pruefe: g => expect(g.title).toBe('Neu') },
  owner:      { tu: o => { o.querySelector('#modal-owner').value = 'Claude' },        pruefe: g => expect(g.owner).toBe('Claude') },
  priority:   { tu: o => { o.querySelector('#modal-priority').value = 'hoch' },       pruefe: g => expect(g.priority).toBe('hoch') },
  complexity: { tu: o => { o.querySelector('#modal-complexity').value = 'hoch' },     pruefe: g => expect(g.complexity).toBe('hoch') },
  effort:     { tu: o => { const e = o.querySelector('#modal-effort'); e.value = '2'; e.dispatchEvent(new Event('input', { bubbles: true })) }, pruefe: g => expect(g.effort).toBe(2) },
  dueDate:    { tu: o => { o.querySelector('#modal-due-date').value = '2026-12-24' }, pruefe: g => expect(g.dueDate).toBe('2026-12-24') },
  startDate:  { tu: o => { o.querySelector('#modal-start-date').value = '2026-12-01' }, pruefe: g => expect(g.startDate).toBe('2026-12-01') },
  dueTime:    { tu: o => { o.querySelector('#modal-due-time').value = '10:15' },      pruefe: g => expect(g.dueTime).toBe('10:15') },
  startTime:  { tu: o => { o.querySelector('#modal-start-time').value = '08:00' },    pruefe: g => expect(g.startTime).toBe('08:00') },
  recurrence: { tu: o => { const s = o.querySelector('#modal-recurrence-interval'); s.value = 'daily'; s.dispatchEvent(new Event('change', { bubbles: true })) }, pruefe: g => expect(g.recurrence?.interval).toBe('daily') },
  labels:     { tu: o => { o.querySelector('#modal-labels').value = 'x, y' },         pruefe: g => expect(g.labels).toEqual(['x', 'y']) },
  assignees:  { tu: o => { for (const opt of o.querySelector('#modal-assignees').options) opt.selected = opt.value === 'Claude Opus' }, pruefe: g => expect(g.assignees).toEqual(['Claude Opus']) },
  qa:         { tu: o => { o.querySelector('#modal-qa').checked = false },            pruefe: g => expect(g.qa).toBe(false) },
  critical:   { tu: o => { o.querySelector('#modal-critical').checked = false },      pruefe: g => expect(g.critical).toBe(false) },
  blocked:    { tu: o => { o.querySelector('#modal-blocked').checked = false },       pruefe: g => expect(g.blocked).toBe(false) },
}
const OHNE_EINGABE = ['description', 'agentContext', 'comments', 'project_id']

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

describe('F-1964 — Schlüsselliste und Abdeckung', () => {
  it('PATCH_FIELD_DEFAULTS wurde aus dem Quelltext gelesen und jedes Feld hat einen Fremdwert', () => {
    expect(SCHLUESSEL.length).toBeGreaterThanOrEqual(19)
    expect(SCHLUESSEL.filter(k => !(k in FREMD_WERTE))).toEqual([])
  })

  it('jedes Feld mit einfachem Formularzugriff hat eine Nutzereingabe', () => {
    expect(SCHLUESSEL.filter(k => !(k in EINGABE) && !OHNE_EINGABE.includes(k))).toEqual([])
  })
})

describe('F-1964 — Öffnen und Schliessen ohne Eingabe schreibt nichts', () => {
  it.each(Object.keys(FREMD_WERTE))('%s mit Fremdwert: kein card:updated', (key) => {
    const spy = schliesseMitX(oeffne(karte(FREMD_WERTE[key])))
    expect(spy).not.toHaveBeenCalled()
  })

  it('alle Fremdwerte zugleich: kein card:updated', () => {
    const spy = schliesseMitX(oeffne(karte(Object.assign({}, ...Object.values(FREMD_WERTE)))))
    expect(spy).not.toHaveBeenCalled()
  })

  it('Titel ändern lässt alle anderen Fremdwerte unverändert (Rohwert bleibt)', () => {
    const fremd = Object.assign({}, ...Object.values(FREMD_WERTE))
    const overlay = oeffne(karte(fremd))
    overlay.querySelector('#modal-title').value = 'Neu'
    const spy = schliesseMitX(overlay)
    expect(spy).toHaveBeenCalledTimes(1)
    const g = spy.mock.calls[0][0]
    expect(g.title).toBe('Neu')
    for (const k of Object.keys(fremd)) {
      if (k === 'title') continue
      expect(g[k], k).toEqual(fremd[k])
    }
  })
})

describe('F-1964 — was der Nutzer selbst eingibt, gewinnt weiterhin', () => {
  it.each(Object.keys(EINGABE))('%s: Eingabe wird gespeichert', (key) => {
    const overlay = oeffne(karte(FREMD_WERTE[key]))
    EINGABE[key].tu(overlay)
    const spy = schliesseMitX(overlay)
    expect(spy).toHaveBeenCalledTimes(1)
    EINGABE[key].pruefe(spy.mock.calls[0][0])
  })
})

describe('F-1967 — Hinweis am Aufwandsfeld', () => {
  const hinweis = o => o.querySelector('#modal-effort-hint')

  it('fremder Aufwand: sichtbarer Hinweis, per aria-describedby verbunden', () => {
    const o = oeffne(karte({ effort: '30 min' }))
    const eff = o.querySelector('#modal-effort')
    const h = hinweis(o)
    expect(h.hidden).toBe(false)
    expect(h.textContent).toContain('30 min')
    expect(h.textContent).toMatch(/bleibt erhalten|kept/)
    expect(eff.getAttribute('aria-describedby')).toBe(h.id)
  })

  it('Zahl oder leer: kein Hinweis, kein aria-describedby', () => {
    for (const effort of [4, null]) {
      const o = oeffne(karte({ effort }))
      expect(hinweis(o).hidden).toBe(true)
      expect(o.querySelector('#modal-effort').hasAttribute('aria-describedby')).toBe(false)
      schliesseMitX(o)
    }
  })

  it('Overlay wiederverwendet: nächste Karte ohne Fremdwert setzt den Hinweis zurück', () => {
    let o = oeffne(karte({ effort: '30 min' }))
    schliesseMitX(o)
    o = oeffne(karte({ id: 'c-2', displayId: 2, effort: 3 }))
    expect(hinweis(o).hidden).toBe(true)
    expect(hinweis(o).textContent).toBe('')
    expect(o.querySelector('#modal-effort').hasAttribute('aria-describedby')).toBe(false)
  })

  it('Tippen ersetzt den Altwert: Hinweis verschwindet', () => {
    const o = oeffne(karte({ effort: '30 min' }))
    const eff = o.querySelector('#modal-effort')
    eff.value = '5'
    eff.dispatchEvent(new Event('input', { bubbles: true }))
    expect(hinweis(o).hidden).toBe(true)
    expect(eff.hasAttribute('aria-describedby')).toBe(false)
  })
})

const aendere = (el, wert) => { el.value = wert; el.dispatchEvent(new Event('input', { bubbles: true })); el.dispatchEvent(new Event('change', { bubbles: true })) }

describe('M-3 — Datum leeren leert auch eine fremde Uhrzeit (ADR-0138 §1)', () => {
  it.each([['dueDate', 'dueTime', '#modal-due-date'], ['startDate', 'startTime', '#modal-start-date']])('%s leeren: %s wird null', (d, t, sel) => {
    const o = oeffne(karte({ [d]: '2026-10-11', [t]: '9:30' }))
    aendere(o.querySelector(sel), '')
    const g = schliesseMitX(o).mock.calls[0][0]
    expect(g[d]).toBe(null)
    expect(g[t]).toBe(null)
  })
})

describe('M-4 — bewusste Eingabe, die der Normalform gleicht, wird gespeichert', () => {
  it('A: Fremddatum durch Eingabe und Leeren entfernen', () => {
    const o = oeffne(karte({ dueDate: '11.10.2026' }))
    aendere(o.querySelector('#modal-due-date'), '2026-12-24')
    aendere(o.querySelector('#modal-due-date'), '')
    const spy = schliesseMitX(o)
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy.mock.calls[0][0].dueDate).toBe(null)
  })
  it('C: Leerraum im Titel bewusst entfernen', () => {
    const o = oeffne(karte({ title: '  Mit Leerraum  ' }))
    aendere(o.querySelector('#modal-title'), 'Mit Leerraum')
    const spy = schliesseMitX(o)
    expect(spy.mock.calls[0][0].title).toBe('Mit Leerraum')
  })
  it('D: Label "a,b" zu "a, b" (zwei Labels gemeint)', () => {
    const o = oeffne(karte({ labels: ['a,b'] }))
    aendere(o.querySelector('#modal-labels'), 'a, b')
    expect(schliesseMitX(o).mock.calls[0][0].labels).toEqual(['a', 'b'])
  })
  it('Gegenrichtung: unberührt bleibt unberührt, auch nach dem Wechsel zur nächsten Karte', () => {
    let o = oeffne(karte({ title: ' X ' }))
    aendere(o.querySelector('#modal-title'), 'Y')
    schliesseMitX(o)
    o = oeffne(karte({ id: 'c-2', displayId: 2, title: '  Z  ' }))
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })
})

describe('Q-3 — berührt heißt: Formularwert weicht vom angezeigten Wert ab', () => {
  it('B1: Labels angetippt und zurückgesetzt schreiben nichts', () => {
    const o = oeffne(karte({ labels: ['a,b'] }))
    const l = o.querySelector('#modal-labels'); aendere(l, 'a,bx'); aendere(l, 'a,b')
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })
  it('B2: Titel angetippt und zurückgesetzt schreiben nichts', () => {
    const o = oeffne(karte({ title: '  T  ' }))
    const t = o.querySelector('#modal-title'); aendere(t, '  Tx  '); aendere(t, '  T  ')
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })
})

describe('M-5 — Assignees in anderer Reihenfolge als die Auswahlliste', () => {
  it('Öffnen und Schliessen schreibt nichts', () => {
    expect(schliesseMitX(oeffne(karte({ assignees: ['Claude Opus', 'Claude'] })))).not.toHaveBeenCalled()
  })
})

describe('Kleinigkeit — leerer Aufwand-Text', () => {
  it('effort "" zeigt keinen Hinweis', () => {
    const o = oeffne(karte({ effort: '' }))
    expect(o.querySelector('#modal-effort-hint').hidden).toBe(true)
    expect(o.querySelector('#modal-effort').hasAttribute('aria-describedby')).toBe(false)
  })
})

describeServer('M-2 — project_id im Server-Build', () => {
  const warte = () => vi.advanceTimersByTimeAsync(0)
  function mocke(projekte, aufgeschoben) {
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    const fertig = {
      cfg:  { ok: true, body: { entries: [{ key: 'clients-projects.projects_enabled', value: 'true' }] } },
      proj: { ok: true, body: projekte },
      cli:  { ok: true, body: [] },
    }
    const lade = (wert) => aufgeschoben ? new Promise(r => { aufgeschoben.push(() => r(wert)) }) : Promise.resolve(wert)
    vi.spyOn(AdminClient, 'listConfig').mockImplementation(() => lade(fertig.cfg))
    vi.spyOn(ClientsProjectsClient, 'listProjects').mockImplementation(() => lade(fertig.proj))
    vi.spyOn(ClientsProjectsClient, 'listClients').mockImplementation(() => lade(fertig.cli))
  }
  afterEach(() => { vi.restoreAllMocks() })

  it('project_id unter den aktiven Projekten fehlt: Öffnen und Schliessen schreibt nichts', async () => {
    mocke([{ id: 'p-2', name: 'Zwei', active: true }])
    const o = oeffne(karte({ project_id: 'p-1' }))
    await warte()
    expect(o.querySelector('#modal-project').value).toBe('p-1')
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })

  it('Befüllung noch nicht fertig: Schliessen schreibt nichts', async () => {
    mocke([{ id: 'p-2', name: 'Zwei', active: true }], [])
    const o = oeffne(karte({ project_id: 'p-2' }))
    await warte()
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })

  it('wiederverwendetes Overlay: nicht der Wert der vorigen Karte', async () => {
    mocke([{ id: 'p-2', name: 'Zwei', active: true }])
    let o = oeffne(karte({ project_id: 'p-2' }))
    await warte()
    schliesseMitX(o)
    vi.restoreAllMocks()
    mocke([{ id: 'p-2', name: 'Zwei', active: true }], [])
    o = oeffne(karte({ id: 'c-2', displayId: 2, project_id: 'p-9' }))
    await warte()
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })

  it('wer „Kein Projekt“ wählt, entfernt die Zuordnung', async () => {
    mocke([{ id: 'p-2', name: 'Zwei', active: true }])
    const o = oeffne(karte({ project_id: 'p-2' }))
    await warte()
    aendere(o.querySelector('#modal-project'), '')
    expect(schliesseMitX(o).mock.calls[0][0].project_id).toBe(null)
  })

  /* Q-1/Q-2: späte Antworten früherer Öffnungen. Je Öffnung warten drei Aufrufe. */
  const spaet = []
  const mockeWartend = () => {
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    const antw = {
      cfg:  { ok: true, body: { entries: [{ key: 'clients-projects.projects_enabled', value: 'true' }] } },
      proj: { ok: true, body: [{ id: 'p-1', name: 'Eins' }, { id: 'p-2', name: 'Zwei' }] },
      cli:  { ok: true, body: [] },
    }
    const lade = w => new Promise(r => { spaet.push(() => r(w)) })
    vi.spyOn(AdminClient, 'listConfig').mockImplementation(() => lade(antw.cfg))
    vi.spyOn(ClientsProjectsClient, 'listProjects').mockImplementation(() => lade(antw.proj))
    vi.spyOn(ClientsProjectsClient, 'listClients').mockImplementation(() => lade(antw.cli))
  }

  it('Q-2/R1: Karte A antwortet nach Karte B — B schreibt nicht das A-Projekt', async () => {
    spaet.length = 0; mockeWartend()
    let o = oeffne(karte({ id: 'A', project_id: 'p-1' }))
    const a = spaet.splice(0)
    schliesseMitX(o)
    o = oeffne(karte({ id: 'B', project_id: 'p-2' }))
    const b = spaet.splice(0)
    b.forEach(r => r()); await warte()
    a.forEach(r => r()); await warte()
    expect(o.querySelector('#modal-project').value).toBe('p-2')
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })

  it('Q-1/R2: dieselbe Karte neu geöffnet (anderes Projekt) — alte Antwort kommt später', async () => {
    spaet.length = 0; mockeWartend()
    let o = oeffne(karte({ id: 'A', project_id: 'p-1' }))
    const a1 = spaet.splice(0)
    schliesseMitX(o)
    o = oeffne(karte({ id: 'A', project_id: 'p-2' }))
    const a2 = spaet.splice(0)
    a2.forEach(r => r()); await warte()
    a1.forEach(r => r()); await warte()
    expect(o.querySelector('#modal-project').value).toBe('p-2')
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })

  it('Q-1/R2 (Reihenfolge andersherum): alte Antwort zuerst, frische danach', async () => {
    spaet.length = 0; mockeWartend()
    let o = oeffne(karte({ id: 'A', project_id: 'p-1' }))
    const a1 = spaet.splice(0)
    schliesseMitX(o)
    o = oeffne(karte({ id: 'A', project_id: 'p-2' }))
    const a2 = spaet.splice(0)
    a1.forEach(r => r()); await warte()
    expect(o.querySelector('#modal-project').dataset.fertig).toBeUndefined()
    a2.forEach(r => r()); await warte()
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })

  it('Q-7: inaktives Projekt erscheint mit Namen und „(inaktiv)“, unbekanntes mit ID', async () => {
    mocke([{ id: 'p-1', name: 'Altprojekt', active: false }, { id: 'p-2', name: 'Zwei', active: true }])
    let o = oeffne(karte({ project_id: 'p-1' }))
    await warte()
    const opt = [...o.querySelector('#modal-project').options].find(x => x.value === 'p-1')
    expect(opt.textContent).toMatch(/^Altprojekt \((inaktiv|inactive)\)$/)
    expect(opt.textContent).not.toContain('eigene Skala')
    schliesseMitX(o)
    o = oeffne(karte({ id: 'c-2', displayId: 2, project_id: 'p-9' }))
    await warte()
    const opt2 = [...o.querySelector('#modal-project').options].find(x => x.value === 'p-9')
    expect(opt2.textContent).toMatch(/^p-9 \((unbekanntes Projekt|unknown project)\)$/)
  })
})

describeServer('Verify Sprint 91 — W-1/W-2/W-3', () => {
  const warteMk = async () => { for (let i = 0; i < 10; i++) await Promise.resolve() }
  const setzeState = k => State.set({ version: '2', board: { id: 'b1', columnDefs: [], sprints: [{ id: 's1', cards: [k] }] } })
  let antworten = []
  function mockeServer() {
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    antworten = []
    const lade = w => new Promise(r => { antworten.push(() => r(w)) })
    vi.spyOn(AdminClient, 'listConfig').mockImplementation(() => lade({ ok: true, body: { entries: [{ key: 'clients-projects.projects_enabled', value: 'true' }] } }))
    vi.spyOn(ClientsProjectsClient, 'listProjects').mockImplementation(() => lade({ ok: true, body: [{ id: 'p-1', name: 'Eins' }, { id: 'p-2', name: 'Zwei' }] }))
    vi.spyOn(ClientsProjectsClient, 'listClients').mockImplementation(() => lade({ ok: true, body: [] }))
  }
  const toggle = (sel, wert) => {
    const opt = [...sel.options].find(o => o.value === wert)
    opt.selected = !opt.selected; sel.dispatchEvent(new Event('change', { bubbles: true }))
    opt.selected = !opt.selected; sel.dispatchEvent(new Event('change', { bubbles: true }))
  }
  afterEach(() => { steuer.aktiv = false; steuer.warte = null; State.set(null); vi.restoreAllMocks() })

  it('W-1: Tab-Wechsel auf Verknüpfungen vor der Projektantwort — Projektliste wird trotzdem befüllt', async () => {
    mockeServer()
    const k = karte({ project_id: 'p-1' }); setzeState({ ...k })
    const o = oeffne({ ...k })
    o.querySelector('.tab-btn[data-tab="links"]').click()
    antworten.splice(0).forEach(r => r()); await warteMk()
    const p = o.querySelector('#modal-project')
    expect(p.dataset.fertig).toBe('1')
    expect(p.options.length).toBeGreaterThan(1)
  })

  it('W-2: Umordnung beim Nachladen + An-/Abwählen ohne Nettoänderung schreibt nichts und rollt Fremdänderung nicht zurück', async () => {
    mockeServer(); steuer.aktiv = true; steuer.sofort = ['Bob']; steuer.spaeter = ['Anna', 'Bob']
    const k = karte({ assignees: ['Bob', 'Anna'] }); setzeState({ ...k })
    const o = oeffne({ ...k }); steuer.warte(); await warteMk()
    const sel = o.querySelector('#modal-assignees')
    setzeState({ ...k, assignees: ['Bob', 'Anna', 'Zoe'] })
    toggle(sel, 'Bob')
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })

  it('W-3: Profilwechsel Server → ohne restStorage — Projektauswahl der vorigen Karte wird nicht geschrieben', async () => {
    mockeServer()
    let o = oeffne(karte({ id: 'A', project_id: 'p-1' }))
    antworten.splice(0).forEach(r => r()); await warteMk()
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
    Profile.has.mockImplementation(() => false)
    o = oeffne(karte({ id: 'B', project_id: null }))
    expect(schliesseMitX(o)).not.toHaveBeenCalled()
  })
})
