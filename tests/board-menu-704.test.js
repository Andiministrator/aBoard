/* @vitest-environment happy-dom */

/**
 * Board-Titel mit Umschalt-Menü und Umbenennen-Stift (Karte #704).
 *
 * Löst die Tests der früheren Sidebar-Sektion „Boards“ ab (user-avatar.test.js
 * Karte #201, nachzuegler-451.test.js F-144/F-833, sidebar.test.js F-1441).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describeServer } from './helpers/build-flags.js'

vi.mock('../src/sync.js', () => ({
  Sync: { getDirectoryName: vi.fn(() => null) },
}))

const bekannteDateien = vi.fn(async () => [])
vi.mock('../src/storage.js', () => ({
  Storage: { listKnownFiles: (...a) => bekannteDateien(...a) },
}))

let listeImpl = async () => []
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ listKnownBoards: (...a) => listeImpl(...a) }) },
}))

let serverModus = false
vi.mock('../src/profile.js', () => ({
  Profile: { has: vi.fn(f => serverModus && f === 'feature.restStorage') },
}))

import { EventBus }  from '../src/eventbus.js'
import { State }     from '../src/state.js'
import { BoardMenu } from '../src/board-menu.js'
import { I18n }      from '../src/i18n.js'

const BOARD = { id: 'b-aktiv', title: 'Aktives Board' }
const tick = () => new Promise(r => setTimeout(r, 0))
let onRename

function bauen(board = BOARD) {
  State.set({ board })
  State.markClean()
  const wrap = BoardMenu.build(board, { onRename })
  document.body.appendChild(wrap)
  return wrap
}
const titel = () => document.querySelector('#board-title')
const menu  = () => document.querySelector('.board-menu')
const punkte = () => [...document.querySelectorAll('.board-menu-item')]
const text = p => p.firstChild.textContent
const labels = () => punkte().map(text)
/* localStorage kommt aus dem zentralen Setup (tests/helpers/localstorage-setup.js, F-1746). */
const speicherKeys = () => Array.from({ length: localStorage.length }, (_, i) => localStorage.key(i))
async function oeffnen() { titel().click(); await tick(); await tick() }

/* Modulzustand (Rollen-Cache, Negativ-Cache) altert nur mit der Uhr: jeder Test
   bekommt eine eigene Stunde, damit er keine Cache-Reste des Vorgängers erbt. */
let uhr = Date.UTC(2026, 9, 5)
beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(uhr += 3600e3)
  /* F-1760: erst den Modus des Vorgängertests zurücknehmen — der Reset-Aufbau darf keinen Serverabruf
     auslösen (ein scheiternder Abruf setzte sonst den Negativ-Cache für die neuen Server-Tests). */
  serverModus = false
  /* Modulzustand „offenes Menü“ des Vorgängertests verwerfen (anderes Board → kein Wiederöffnen). */
  document.body.appendChild(BoardMenu.build({ id: '__reset__', title: '' }, { onRename() {} }))
  /* Der Reset-Aufbau schreibt selbst in die „zuletzt benutzt“-Liste — vor dem Test leeren. */
  localStorage.clear()
  document.body.innerHTML = ''
  listeImpl = async () => []
  bekannteDateien.mockResolvedValue([])
  onRename = vi.fn()
})
afterEach(() => { vi.useRealTimers(); EventBus.emit('config:language-changed') })

describe('Board-Titel — Knopf + A11y', () => {
  it('Titel ist ein Knopf mit aria-haspopup/aria-expanded; Menü geschlossen, inert und leer', () => {
    bauen()
    expect(titel().tagName).toBe('BUTTON')
    expect(titel().textContent).toBe('Aktives Board')
    expect(titel().getAttribute('aria-haspopup')).toBe('menu')
    expect(titel().getAttribute('aria-expanded')).toBe('false')
    expect(menu().hasAttribute('inert')).toBe(true)
    expect(menu().hidden).toBe(true)
    expect(menu().children.length).toBe(0)
  })

  it('Dirty-Stern bleibt am Titel', () => {
    State.set({ board: BOARD }) /* set() macht dirty */
    document.body.appendChild(BoardMenu.build(BOARD, { onRename }))
    expect(titel().textContent).toBe('* Aktives Board')
  })

  it('Klick öffnet (aria-expanded, aktuelles Board markiert), zweiter Klick schließt und leert', async () => {
    bauen()
    await oeffnen()
    expect(titel().getAttribute('aria-expanded')).toBe('true')
    expect(menu().getAttribute('role')).toBe('menu')
    expect(menu().hasAttribute('inert')).toBe(false)
    expect(document.querySelector('.board-menu-item--current').getAttribute('aria-current')).toBe('true')
    titel().click()
    expect(titel().getAttribute('aria-expanded')).toBe('false')
    expect(menu().hasAttribute('inert')).toBe(true)
    expect(menu().children.length).toBe(0)
  })
})

describe('Board-Menü — lokale Quellen', () => {
  it('Standalone-Dateien: Wechsel sendet standalone:switch-to-file, Menü schließt', async () => {
    bekannteDateien.mockResolvedValue([{ id: 'f2', title: 'Zweites', name: 'zwei.json' }])
    bauen()
    await oeffnen()
    expect(labels()).toContain('Zweites')
    const spy = vi.fn()
    EventBus.on('standalone:switch-to-file', spy)
    punkte().find(p => p.textContent.includes('Zweites')).click()
    expect(spy).toHaveBeenCalledWith({ boardId: 'f2' })
    expect(titel().getAttribute('aria-expanded')).toBe('false')
  })

  it('F-144: Whitespace-Titel → Dateiname als Label, kein Untertitel', async () => {
    bekannteDateien.mockResolvedValue([{ id: 'f2', title: '   ', name: 'projekt-b.json' }])
    bauen()
    await oeffnen()
    const p = punkte().find(x => x.textContent.includes('projekt-b.json'))
    expect(p).toBeTruthy()
    expect(text(p)).toBe('projekt-b.json')
    expect(p.querySelector('small')).toBeNull()
  })

  it('F-833: Whitespace-Titel des aktiven Boards → Fallback auf die Board-id', async () => {
    bauen({ id: 'board-xyz', title: '   ' })
    await oeffnen()
    expect(text(document.querySelector('.board-menu-item--current'))).toBe('board-xyz')
  })

  it('Claude-Code-Projekte: Wechsel sendet sync:switch-to-board; Datei, die auch als Projekt gelistet wird, zählt nur einmal', async () => {
    listeImpl = async () => [{ id: 'p1', name: 'Projekt 1' }, { id: 'f2', name: 'Doppelt' }]
    bekannteDateien.mockResolvedValue([{ id: 'f2', title: 'Doppelt', name: 'd.json' }])
    bauen()
    await oeffnen()
    expect(labels().filter(l => l === 'Doppelt').length).toBe(1)
    const spy = vi.fn()
    EventBus.on('sync:switch-to-board', spy)
    punkte().find(p => p.textContent.includes('Projekt 1')).click()
    expect(spy).toHaveBeenCalledWith({ boardId: 'p1' })
  })

  it('F-1731: lokal höchstens 5 Boards, „Alle Boards anzeigen…“ klappt die volle Liste im Menü auf', async () => {
    bekannteDateien.mockResolvedValue(Array.from({ length: 8 }, (_, i) => ({ id: `f${i}`, title: `Datei ${i}`, name: `d${i}.json` })))
    bauen()
    await oeffnen()
    expect(labels().filter(l => l.startsWith('Datei')).length).toBe(5)
    const alle = punkte().find(p => /Alle Boards|Show all boards/.test(p.textContent))
    expect(alle).toBeTruthy()
    alle.click()
    expect(labels().filter(l => l.startsWith('Datei')).length).toBe(8)
    expect(punkte().some(p => /Alle Boards|Show all boards/.test(p.textContent))).toBe(false)
    expect(document.activeElement).toBe(punkte()[6]) /* erster neu sichtbarer Eintrag */
  })

  it('F-1731: die Auswahl folgt „zuletzt benutzt“ (localStorage), nicht der Quellreihenfolge', async () => {
    bekannteDateien.mockResolvedValue(Array.from({ length: 8 }, (_, i) => ({ id: `f${i}`, title: `Datei ${i}`, name: `d${i}.json` })))
    for (const id of ['f7', 'f6', 'f5', 'f4', 'f3', 'gibt-es-nicht', 'f2']) bauen({ id, title: id }) /* f2 zuletzt */
    document.body.innerHTML = ''
    bauen()
    await oeffnen()
    /* jüngste zuerst: f2, f3, f4, f5, f6 (f7 fällt raus; unbekannte ID fällt still raus; das aktive steht oben) */
    expect(labels().filter(l => l.startsWith('Datei'))).toEqual(['Datei 2', 'Datei 3', 'Datei 4', 'Datei 5', 'Datei 6'])
  })

  it('F-1731: Fortschreiben beim Anzeigen eines Boards — auch ohne Menü; Liste je Modus getrennt, ohne localStorage gilt die Quellreihenfolge', async () => {
    bauen({ id: 'x1', title: 'X1' })
    bauen({ id: 'x2', title: 'X2' })
    const lokal = speicherKeys().filter(k => k.startsWith('aboard-recent:'))
    expect(lokal.length).toBe(1)
    expect(lokal[0]).not.toBe('aboard-recent:srv')
    expect(JSON.parse(localStorage.getItem(lokal[0]))).toEqual(['x2', 'x1'])
    /* localStorage gesperrt: kein Fehler, Reihenfolge der Quelle */
    const heil = globalThis.localStorage
    globalThis.localStorage = { getItem() { throw new Error('gesperrt') }, setItem() { throw new Error('gesperrt') } }
    bekannteDateien.mockResolvedValue([{ id: 'a', title: 'A', name: 'a.json' }, { id: 'b', title: 'B', name: 'b.json' }])
    document.body.innerHTML = ''
    bauen()
    await oeffnen()
    expect(labels().filter(l => l === 'A' || l === 'B')).toEqual(['A', 'B'])
    globalThis.localStorage = heil
  })

  it('wenige Boards: kein „Alle Boards“-Eintrag; ohne weitere Boards der Leerhinweis', async () => {
    bauen()
    await oeffnen()
    expect(punkte().length).toBe(1)
    expect(menu().textContent).toMatch(/Keine weiteren Boards|No other boards/)
  })
})

describe('Board-Menü — Tastatur und Fokus', () => {
  beforeEach(() => {
    bekannteDateien.mockResolvedValue([{ id: 'f2', title: 'Zwei', name: 'a.json' }, { id: 'f3', title: 'Drei', name: 'b.json' }])
  })
  const key = (k, ziel = document.activeElement) => ziel.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true }))

  it('Öffnen setzt den Fokus auf den ersten Eintrag; Pfeile laufen im Kreis', async () => {
    bauen()
    await oeffnen()
    expect(document.activeElement).toBe(punkte()[0])
    key('ArrowDown'); expect(document.activeElement).toBe(punkte()[1])
    key('ArrowUp');   key('ArrowUp'); expect(document.activeElement).toBe(punkte().at(-1))
    key('ArrowDown'); expect(document.activeElement).toBe(punkte()[0])
  })

  it('Pfeil-runter am Titel öffnet das Menü', async () => {
    bauen()
    titel().focus()
    key('ArrowDown', titel())
    await tick(); await tick()
    expect(titel().getAttribute('aria-expanded')).toBe('true')
  })

  it('Esc schließt und gibt den Fokus an den Titel zurück', async () => {
    bauen()
    await oeffnen()
    key('Escape')
    expect(titel().getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(titel())
  })

  it('Sprint-85-Kritiker: Hilfe-Overlay über offenem Menü — Escape schließt das Overlay, nicht das Menü', async () => {
    bauen()
    await oeffnen()
    const ov = document.createElement('div')
    ov.setAttribute('role', 'dialog'); ov.setAttribute('aria-modal', 'true')
    const knopf = document.createElement('button'); ov.appendChild(knopf)
    let zu = 0
    ov.addEventListener('keydown', e => { if (e.key === 'Escape') { zu++; ov.remove() } })
    document.body.appendChild(ov); knopf.focus()
    knopf.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(zu).toBe(1)
    expect(titel().getAttribute('aria-expanded')).toBe('true')
  })

  it('Sitzungs-QA Sprint 85: Fokus in einem Feld außerhalb (Suche) — Escape gehört dem Feld', async () => {
    bauen()
    await oeffnen()
    const feld = document.createElement('input'); document.body.appendChild(feld); feld.focus()
    const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    feld.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(false)
    expect(document.activeElement).toBe(feld)
    expect(titel().getAttribute('aria-expanded')).toBe('true')
    feld.remove()
  })

  it('Sprint-85-Kritiker: abgehängtes offenes Menü verschluckt kein Escape und meldet sich ab', async () => {
    const wrap = bauen()
    await oeffnen()
    wrap.remove()
    const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    document.body.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(false)
    const ev2 = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    document.body.dispatchEvent(ev2)
    expect(ev2.defaultPrevented).toBe(false)
  })

  it('F-1785: Esc schließt auch, wenn der Fokus auf body steht (Klick auf eine Überschrift)', async () => {
    bauen()
    await oeffnen()
    document.activeElement?.blur?.()
    expect(document.activeElement).toBe(document.body)
    const ev = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    document.body.dispatchEvent(ev)
    expect(titel().getAttribute('aria-expanded')).toBe('false')
    expect(ev.defaultPrevented).toBe(true)
    /* Geschlossen: der Capture-Handler ist abgemeldet, Escape bleibt den anderen Handlern. */
    const ev2 = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true })
    document.body.dispatchEvent(ev2)
    expect(ev2.defaultPrevented).toBe(false)
  })
})

describe('Board-Menü — Aussenklick und spätes Nachladen (F-1729/F-1730)', () => {
  const pd = ziel => ziel.dispatchEvent(new Event('pointerdown', { bubbles: true }))

  it('Aussenklick schließt, ein Klick INS Menü (auf eine Überschrift) lässt es offen', async () => {
    bekannteDateien.mockResolvedValue([{ id: 'f2', title: 'Zwei', name: 'a.json' }])
    bauen()
    await oeffnen()
    pd(document.querySelector('.board-menu-head'))
    expect(titel().getAttribute('aria-expanded')).toBe('true')
    pd(document.body)
    expect(titel().getAttribute('aria-expanded')).toBe('false')
  })

  it('Der Aussenklick-Listener hängt nur, solange das Menü offen ist', async () => {
    const add = vi.spyOn(document, 'addEventListener')
    const rem = vi.spyOn(document, 'removeEventListener')
    bauen()
    await oeffnen()
    titel().click()
    const n = (spy) => spy.mock.calls.filter(c => c[0] === 'pointerdown').length
    expect(n(add)).toBe(1)
    expect(n(rem)).toBeGreaterThanOrEqual(1)
    add.mockRestore(); rem.mockRestore()
  })

  it('Ein spät fertiger Ladelauf baut ein inzwischen geschlossenes bzw. neu geöffnetes Menü nicht neu', async () => {
    let frei1
    bekannteDateien.mockImplementationOnce(() => new Promise(r => { frei1 = () => r([{ id: 'alt', title: 'Alt-Lauf', name: 'a.json' }]) }))
    bekannteDateien.mockResolvedValue([{ id: 'neu', title: 'Neu-Lauf', name: 'n.json' }])
    bauen()
    titel().click()        /* Lauf 1 hängt */
    await tick()
    titel().click()        /* schließen */
    titel().click()        /* Lauf 2 */
    await tick(); await tick()
    expect(labels()).toContain('Neu-Lauf')
    document.activeElement?.blur?.()
    frei1(); await tick(); await tick()
    expect(labels()).not.toContain('Alt-Lauf')
    expect(labels()).toContain('Neu-Lauf')
  })
})

describe('Board-Menü — Neuaufbau des Kopfes (F-1732)', () => {
  const key = (k, ziel = document.activeElement) => ziel.dispatchEvent(new KeyboardEvent('keydown', { key: k, bubbles: true }))
  const neuBauen = (board = BOARD) => { document.body.innerHTML = ''; return bauen(board) }
  beforeEach(() => {
    bekannteDateien.mockResolvedValue([{ id: 'f2', title: 'Zwei', name: 'a.json' }, { id: 'f3', title: 'Drei', name: 'b.json' }])
  })

  it('ein offenes Menü übersteht einen Render mit unverändertem Board — offen, Fokus auf demselben Eintrag', async () => {
    bauen()
    await oeffnen()
    key('ArrowDown'); key('ArrowDown') /* Fokus auf Eintrag 2 (per Tastatur, wie im Betrieb) */
    expect(document.activeElement).toBe(punkte()[2])
    neuBauen()
    await tick(); await tick()
    expect(titel().getAttribute('aria-expanded')).toBe('true')
    expect(menu().hidden).toBe(false)
    expect(document.activeElement).toBe(punkte()[2])
  })

  it('lokal aufgeklappte volle Liste („Alle Boards anzeigen…“) bleibt nach dem Render aufgeklappt', async () => {
    bekannteDateien.mockResolvedValue(Array.from({ length: 8 }, (_, i) => ({ id: `f${i}`, title: `Datei ${i}`, name: `d${i}.json` })))
    bauen()
    await oeffnen()
    punkte().find(p => /Alle Boards|Show all boards/.test(p.textContent)).click()
    expect(labels().filter(l => l.startsWith('Datei')).length).toBe(8)
    neuBauen()
    await tick(); await tick()
    expect(labels().filter(l => l.startsWith('Datei')).length).toBe(8)
  })

  it('F-1738: stand der Fokus vor dem Neuaufbau außerhalb des Menüs, bleibt er dort (Menü bleibt offen)', async () => {
    document.body.insertAdjacentHTML('beforeend', '<input id="aussen">')
    bauen()
    await oeffnen()
    document.querySelector('#aussen').focus() /* z. B. Shortcut „n“ / „/“ bei offenem Menü */
    neuBauen()
    document.body.insertAdjacentHTML('beforeend', '<input id="aussen">')
    document.querySelector('#aussen').focus()
    await tick(); await tick()
    expect(titel().getAttribute('aria-expanded')).toBe('true')
    expect(menu().hidden).toBe(false)
    expect(menu().contains(document.activeElement)).toBe(false)
    expect(document.activeElement.id).toBe('aussen')
  })

  it('F-1739: Titel von außen geändert — „Aktuelles Board“ zeigt nach dem Neuaufbau den neuen Namen', async () => {
    bauen()
    await oeffnen()
    neuBauen({ id: BOARD.id, title: 'Umbenannt' })
    await tick(); await tick()
    expect(document.querySelector('.board-menu-item--current').firstChild.textContent).toBe('Umbenannt')
  })

  it('F-1739: die Liste lädt beim Neuaufbau neu und ersetzt sich, wenn der Fokus nicht im Menü steht', async () => {
    document.body.insertAdjacentHTML('beforeend', '<input id="aussen">')
    bauen()
    await oeffnen()
    expect(labels()).toContain('Zwei')
    bekannteDateien.mockResolvedValue([{ id: 'f9', title: 'Neun', name: 'n.json' }])
    document.querySelector('#aussen').focus()
    neuBauen()
    await tick(); await tick(); await tick()
    expect(labels()).toContain('Neun')
    expect(labels()).not.toContain('Zwei')
  })

  it('F-1739: ein gerade bedientes Menü (Fokus im Menü) wird nicht durch die frische Liste umgebaut', async () => {
    bauen()
    await oeffnen()
    key('ArrowDown')
    bekannteDateien.mockResolvedValue([{ id: 'f9', title: 'Neun', name: 'n.json' }])
    neuBauen()
    await tick(); await tick(); await tick()
    expect(labels()).toContain('Zwei')
    expect(labels()).not.toContain('Neun')
  })

  it('F-1741: „Alle Boards anzeigen…“ lässt die 5 sichtbaren in Reihenfolge stehen, der Rest folgt; Fokus auf dem ersten neuen (gemischte Gruppen)', async () => {
    /* zuletzt benutzt: f0, p0, f1, p1, f2, p2, f3 — Projekte (Gruppe 0) und Dateien (Gruppe 1) abwechselnd */
    const f = i => ({ id: `f${i}`, title: `Datei ${i}`, name: `d${i}.json` })
    const pr = i => ({ id: `p${i}`, name: `Projekt ${i}` })
    bekannteDateien.mockResolvedValue([0, 1, 2, 3].map(f))
    listeImpl = async () => [0, 1, 2].map(pr)
    bauen()
    localStorage.setItem('aboard-recent:' + location.pathname, JSON.stringify(['f0', 'p0', 'f1', 'p1', 'f2', 'p2', 'f3']))
    await oeffnen()
    const vorher = labels()
    expect(vorher.slice(1, 6)).toEqual(['Projekt 0', 'Projekt 1', 'Datei 0', 'Datei 1', 'Datei 2'])
    punkte().find(p => /Alle Boards|Show all boards/.test(p.textContent)).click()
    expect(labels().slice(0, 6)).toEqual(vorher.slice(0, 6))
    expect(labels().slice(6)).toEqual(['Projekt 2', 'Datei 3'])
    expect(document.activeElement).toBe(punkte()[6])
    expect(text(document.activeElement)).toBe('Projekt 2')
  })

  it('geschlossenes Menü bleibt nach dem Render zu; Board-Wechsel schließt', async () => {
    bauen()
    await oeffnen()
    titel().click()
    neuBauen()
    await tick()
    expect(titel().getAttribute('aria-expanded')).toBe('false')
    await oeffnen()
    neuBauen({ id: 'anderes', title: 'Anderes' })
    await tick(); await tick()
    expect(titel().getAttribute('aria-expanded')).toBe('false')
  })

  it('es bleibt genau ein Aussenklick-Listener: nach Neuaufbau wird der alte entfernt, ein Aussenklick schließt', async () => {
    const add = vi.spyOn(document, 'addEventListener')
    const rem = vi.spyOn(document, 'removeEventListener')
    bauen()
    await oeffnen()
    neuBauen()
    await tick(); await tick()
    const n = spy => spy.mock.calls.filter(c => c[0] === 'pointerdown').length
    expect(n(add) - n(rem)).toBe(1)
    document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }))
    expect(titel().getAttribute('aria-expanded')).toBe('false')
    expect(n(add) - n(rem)).toBe(0)
    add.mockRestore(); rem.mockRestore()
  })

  it('Klick auf den Stift schließt ein offenes Menü, bevor das Umbenennen startet', async () => {
    const wrap = bauen()
    await oeffnen()
    wrap.querySelector('.board-title-edit').click()
    expect(titel().getAttribute('aria-expanded')).toBe('false')
    expect(onRename).toHaveBeenCalledWith(wrap)
    neuBauen()
    await tick(); await tick()
    expect(titel().getAttribute('aria-expanded')).toBe('false')
  })
  it('Neuaufbau, bevor das erste Laden zurück ist (F-1747) → Menü sichtbar, kein Fehler, keine unbehandelte Rejection', async () => {
    const fehler = []; const h = e => fehler.push(e); process.on('unhandledRejection', h)
    let frei; bekannteDateien.mockImplementation(() => new Promise(r => { frei = r }))
    bauen()
    titel().click(); await tick()           /* Lauf 1 wartet noch auf das erste Laden */
    neuBauen()
    await tick(); await tick()
    frei([{ id: 'f2', title: 'Zwei', name: 'a.json' }]); await tick(); await tick() /* frei = Auflöser des zweiten Laufs */
    process.off('unhandledRejection', h)
    expect(fehler).toEqual([])
    expect(menu().hidden).toBe(false)
    expect(titel().getAttribute('aria-expanded')).toBe('true')
    expect(labels()).toContain('Zwei')
  })

  it('lokal aufgeklappt: jeder Eintrag steht unter der Überschrift seiner Gruppe (F-1749)', async () => {
    const f = i => ({ id: `f${i}`, title: `Datei ${i}`, name: `d${i}.json` })
    bekannteDateien.mockResolvedValue([0, 1, 2, 3].map(f))
    listeImpl = async () => [0, 1, 2].map(i => ({ id: `p${i}`, name: `Projekt ${i}` }))
    localStorage.setItem('aboard-recent:' + location.pathname, JSON.stringify(['f0', 'p0', 'f1', 'p1', 'f2', 'p2', 'f3']))
    bauen()
    await oeffnen()
    punkte().find(p => /Alle Boards|Show all boards/.test(p.textContent)).click()
    const gruppe = {}; let kopf = null
    for (const n of menu().children) {
      if (n.classList.contains('board-menu-head')) kopf = n.textContent
      else gruppe[n.firstChild.textContent] = kopf
    }
    const lokal = /Lokal|Local/, cc = /Claude/
    for (const i of [0, 1, 2, 3]) expect(gruppe[`Datei ${i}`]).toMatch(lokal)
    for (const i of [0, 1, 2]) expect(gruppe[`Projekt ${i}`]).toMatch(cc)
  })

  it('Schließen meldet den focusin-Listener ab (F-1750a)', async () => {
    const add = vi.spyOn(document, 'addEventListener')
    const rem = vi.spyOn(document, 'removeEventListener')
    bauen()
    await oeffnen()
    const n = spy => spy.mock.calls.filter(c => c[0] === 'focusin').length
    expect(n(add) - n(rem)).toBe(1)
    titel().click()
    expect(n(add) - n(rem)).toBe(0)
    add.mockRestore(); rem.mockRestore()
  })

  it('zwei Neuaufbauten ohne Tick dazwischen behalten den Menüfokus (F-1750b)', async () => {
    bauen()
    await oeffnen()
    punkte()[1].focus()
    neuBauen(); neuBauen()
    await tick(); await tick()
    expect(menu().hidden).toBe(false)
    expect(menu().contains(document.activeElement)).toBe(true)
  })

  it('Schließen vor Rückkehr des Neuabrufs → Menü bleibt zu (F-1750c)', async () => {
    bauen()
    await oeffnen()
    let frei; bekannteDateien.mockImplementation(() => new Promise(r => { frei = r }))
    neuBauen()
    await tick()
    titel().click()
    expect(menu().hidden).toBe(true)
    frei([{ id: 'f9', title: 'Neun', name: 'n.json' }]); await tick(); await tick()
    expect(menu().hidden).toBe(true)
    expect(titel().getAttribute('aria-expanded')).toBe('false')
  })

  it('Schließen + Wiederöffnen bei hängendem Neuabruf: die alte Liste überschreibt die neue nicht (F-1752, mein === lauf)', async () => {
    const offene = []
    bekannteDateien.mockResolvedValue([{ id: 'f1', title: 'Start', name: 's.json' }])
    bauen(); await oeffnen()
    bekannteDateien.mockImplementation(() => new Promise(r => offene.push(r)))
    neuBauen(); await tick()                 /* Neuabruf offene[0] hängt */
    titel().click()                          /* schließen */
    titel().click(); await tick()            /* wieder öffnen: eigenes Laden offene[1] */
    offene[1]([{ id: 'n', title: 'Neu', name: 'n.json' }]); await tick(); await tick()
    expect(labels()).toContain('Neu')
    const aussen = document.createElement('input'); document.body.appendChild(aussen)
    aussen.focus()                           /* Fokus draußen, Menü bleibt offen */
    offene[0]([{ id: 'a', title: 'Alt', name: 'a.json' }]); await tick(); await tick()
    expect(labels()).toContain('Neu')
    expect(labels()).not.toContain('Alt')
  })

  it('Neuaufbau während des ersten Ladens fokussiert nach dem Laden wie das Erstöffnen (F-1753)', async () => {
    let frei; bekannteDateien.mockImplementation(() => new Promise(r => { frei = r }))
    bauen(); titel().focus(); titel().click(); await tick()
    neuBauen(); await tick()
    frei([{ id: 'f2', title: 'Zwei', name: 'z.json' }]); await tick(); await tick()
    expect(menu().hidden).toBe(false)
    expect(menu().contains(document.activeElement)).toBe(true)
  })
})

describe('Board-Menü — A11y (F-1735)', () => {
  it('Überschriften mit role="presentation" (im Menü nur presentation/group/separator/menuitem erlaubt), Stift mit eigenem Namen, Zeichen ▾/✓ ohne zugänglichen Namen', async () => {
    bekannteDateien.mockResolvedValue([{ id: 'f2', title: 'Zwei', name: 'a.json' }])
    const wrap = bauen()
    await oeffnen()
    const kinder = [...menu().children]
    expect(kinder.length).toBeGreaterThan(0)
    for (const k of kinder) expect(k.getAttribute('role')).toMatch(/^(presentation|menuitem)$/)
    expect(menu().querySelector('.board-menu-head').getAttribute('role')).toBe('presentation')
    const stift = wrap.querySelector('.board-title-edit')
    expect(stift.getAttribute('aria-label')).toBe(I18n.t('board.rename'))
    expect(stift.getAttribute('aria-label')).not.toBe(I18n.t('col.rename-tip'))
    expect(stift.title).toBe(I18n.t('col.rename-tip'))
    /* Zierzeichen: CSS-Inhalt mit leerem Alternativtext (happy-dom kennt keine ::after) */
    const css = readFileSync(resolve(process.cwd(), 'src/style.css'), 'utf8')
    expect(css).toMatch(/#board-title::after \{[^}]*content: ' ▾' \/ '';/)
    expect(css).toMatch(/\.board-menu-item--current::before \{[^}]*content: '✓ ' \/ '';/)
  })
})

describe('Umbenennen-Stift', () => {
  it('lokal: Stift sichtbar, Klick ruft onRename mit dem Titelblock', () => {
    const wrap = bauen()
    const stift = wrap.querySelector('.board-title-edit')
    expect(stift.hidden).toBe(false)
    expect(stift.getAttribute('aria-label')).toBeTruthy()
    stift.click()
    expect(onRename).toHaveBeenCalledWith(wrap)
  })

  it('der Titelklick selbst benennt NICHT mehr um', () => {
    bauen()
    titel().click()
    expect(onRename).not.toHaveBeenCalled()
  })
})

describeServer('Board-Menü — Server (Rollen, Übersicht, neues Board)', () => {
  beforeEach(() => {
    serverModus = true
  })

  const boards = [
    { id: 'b-aktiv', name: 'Aktives Board', role: 'owner', updatedAt: 1 },
    { id: 'o1', name: 'Eigenes', role: 'owner', updatedAt: 50 },
    { id: 's1', name: 'Geteilt', role: 'viewer', updatedAt: 60 },
    { id: 's2', name: 'Editierbar', role: 'editor', updatedAt: 40 },
  ]

  it('Gruppen Eigene / Mit mir geteilt mit Rolle; aktives Board nicht doppelt', async () => {
    listeImpl = async () => boards
    bauen()
    await oeffnen()
    const heads = [...document.querySelectorAll('.board-menu-head')].map(h => h.textContent)
    expect(heads.some(t => /Eigene|My boards/.test(t))).toBe(true)
    expect(heads.some(t => /Mit mir|Shared with me/.test(t))).toBe(true)
    expect(labels().filter(l => l === 'Aktives Board').length).toBe(1)
    expect(punkte().find(p => p.textContent.includes('Geteilt')).textContent).toMatch(/Betrachter|Viewer/)
  })

  it('ohne Rollen (Legacy-Bypass) eine flache Server-Boards-Gruppe', async () => {
    listeImpl = async () => [{ id: 'x', name: 'Legacy' }]
    bauen()
    await oeffnen()
    const heads = [...document.querySelectorAll('.board-menu-head')].map(h => h.textContent)
    expect(heads.some(t => /Server/.test(t))).toBe(true)
    expect(heads.some(t => /Eigene|My boards|Mit mir|Shared/.test(t))).toBe(false)
  })

  it('die 5 zuletzt geänderten Boards (updatedAt absteigend)', async () => {
    listeImpl = async () => [
      { id: 'b-aktiv', name: 'Aktives Board', role: 'owner', updatedAt: 999 },
      ...Array.from({ length: 7 }, (_, i) => ({ id: `x${i}`, name: `Board ${i}`, role: 'owner', updatedAt: i + 1 })),
    ]
    bauen()
    await oeffnen()
    const sichtbar = labels().filter(l => l.startsWith('Board '))
    expect(sichtbar.sort()).toEqual(['Board 2', 'Board 3', 'Board 4', 'Board 5', 'Board 6'])
  })

  it('F-1731: Server — die 5 zuletzt BENUTZTEN Boards (localStorage), Gruppen bleiben, höchstens 5 insgesamt', async () => {
    listeImpl = async () => [
      { id: 'b-aktiv', name: 'Aktives Board', role: 'owner', updatedAt: 999 },
      ...Array.from({ length: 8 }, (_, i) => ({ id: `x${i}`, name: `Board ${i}`, role: i % 2 ? 'editor' : 'owner', updatedAt: 100 - i })),
    ]
    for (const id of ['x7', 'x6', 'x5', 'x4', 'x3', 'x2']) bauen({ id, title: id }) /* x2 zuletzt */
    document.body.innerHTML = ''
    bauen()
    await oeffnen()
    const sichtbar = labels().filter(l => l.startsWith('Board '))
    expect(sichtbar.length).toBe(5)
    expect([...sichtbar].sort()).toEqual(['Board 2', 'Board 3', 'Board 4', 'Board 5', 'Board 6'])
    const keys = speicherKeys().filter(k => k.startsWith('aboard-recent:'))
    expect(keys).toEqual(['aboard-recent:srv'])
  })

  /* F-1789: eigene Board-Id — `_rollen` ist Modulzustand, `darfUmbenennen` prüft keine TTL; mit 'n1'
     erbte der Test den Eintrag von F-1755 und der Stift war schon sichtbar. */
  it('F-1733: schlägt der Rollenabruf fehl, fragt der nächste Render erst nach der TTL wieder', async () => {
    const abruf = vi.fn(async () => { throw new Error('HTTP 500') })
    listeImpl = abruf
    bauen({ id: 'f1733', title: 'F1733' })
    await tick(); await tick()
    expect(abruf).toHaveBeenCalledTimes(1)
    document.body.innerHTML = ''
    const wrap = bauen({ id: 'f1733', title: 'F1733' })
    await tick(); await tick()
    expect(abruf).toHaveBeenCalledTimes(1) /* Negativ-Cache */
    expect(wrap.querySelector('.board-title-edit').hidden).toBe(true)
    vi.setSystemTime(Date.now() + 61000)
    document.body.innerHTML = ''
    bauen({ id: 'f1733', title: 'F1733' })
    await tick(); await tick()
    expect(abruf).toHaveBeenCalledTimes(2)
  })

  it('„Alle Boards anzeigen…“ sendet boards-view:open, auch wenn die Liste nicht ladbar ist (F-1440)', async () => {
    listeImpl = async () => { throw new Error('HTTP 500') }
    bauen()
    await oeffnen()
    const spy = vi.fn()
    EventBus.on('boards-view:open', spy)
    punkte().find(p => /Alle Boards|Show all boards/.test(p.textContent)).click()
    expect(spy).toHaveBeenCalledTimes(1)
  })

  it('„Neues Board“ fragt den Titel und sendet server:new-board', async () => {
    bauen()
    await oeffnen()
    const altPrompt = window.prompt
    window.prompt = vi.fn(() => ' Neu ')
    const spy = vi.fn()
    EventBus.on('server:new-board', spy)
    punkte().find(p => /Neues Server-Board|New server board/.test(p.textContent)).click()
    expect(spy).toHaveBeenCalledWith({ title: 'Neu' })
    window.prompt = altPrompt
  })

  it('Stift: Viewer sieht ihn nicht, Editor/Owner schon; vor der Rollenauskunft versteckt', async () => {
    listeImpl = async () => [{ id: 'v1', name: 'V', role: 'viewer' }, { id: 'e1', name: 'E', role: 'editor' }, { id: 'o1', name: 'O', role: 'owner' }]
    const wrapV = bauen({ id: 'v1', title: 'V' })
    expect(wrapV.querySelector('.board-title-edit').hidden).toBe(true)
    await tick(); await tick()
    expect(wrapV.querySelector('.board-title-edit').hidden).toBe(true)
    document.body.innerHTML = ''
    const wrapE = bauen({ id: 'e1', title: 'E' })
    await tick(); await tick()
    expect(wrapE.querySelector('.board-title-edit').hidden).toBe(false)
    document.body.innerHTML = ''
    const wrapO = bauen({ id: 'o1', title: 'O' })
    expect(wrapO.querySelector('.board-title-edit').hidden).toBe(false)
  })

  it('Fehlschlag des Hintergrund-Neuabrufs lässt die sichtbare Liste stehen und löst im Negativ-Cache keinen weiteren Abruf aus (F-1748)', async () => {
    listeImpl = async () => [{ id: 'b-aktiv', name: 'Aktives Board', role: 'owner' }, { id: 'x', name: 'Fremd', role: 'editor' }]
    bauen()
    await oeffnen()
    const vorher = menu().textContent
    expect(labels()).toContain('Fremd')
    const aussen = document.createElement('input'); document.body.appendChild(aussen)
    const abrufe = vi.fn(async () => { throw new Error('offline') })
    listeImpl = abrufe
    aussen.focus() /* Fokus draußen: nur dann baut der Neuabruf das Menü um */
    document.body.innerHTML = ''; document.body.appendChild(aussen)
    bauen(); aussen.focus()
    await tick(); await tick(); await tick()
    expect(abrufe).toHaveBeenCalledTimes(1)
    expect(menu().textContent).toBe(vorher)
    expect(labels()).toContain('Fremd')
    /* Negativ-Cache (60 s): ein weiterer Neuaufbau ruft nicht erneut ab */
    document.body.innerHTML = ''; document.body.appendChild(aussen)
    bauen(); aussen.focus()
    await tick(); await tick(); await tick()
    expect(abrufe).toHaveBeenCalledTimes(1)
    expect(menu().textContent).toBe(vorher)
  })
  it('F-1751: ein einzelner Listen-Fehlschlag sperrt den Rollenabruf neuer Boards nicht (eigener Negativ-Cache)', async () => {
    let n = 0
    listeImpl = async () => { n++; return [{ id: 'b-aktiv', name: 'Aktives Board', role: 'owner' }] }
    bauen(); await tick(); await tick()
    listeImpl = async () => { n++; throw new Error('kurz weg') }
    await oeffnen()                          /* nur die Liste scheitert */
    listeImpl = async () => { n++; return [{ id: 'neu', name: 'Neu', role: 'owner' }] }
    const vor = n
    document.body.innerHTML = ''
    const wrap = bauen({ id: 'neu', title: 'Neu' })
    await tick(); await tick()
    expect(n - vor).toBe(1)
    expect(wrap.querySelector('.board-title-edit').hidden).toBe(false)
  })

  it('F-1755: Rollenabruf scheitert → Menü-Laden gelingt → der nächste Neuaufbau fragt die Rollen wieder ab', async () => {
    let n = 0
    listeImpl = async () => { n++; throw new Error('HTTP 500') }
    bauen({ id: 'n1', title: 'N1' }); await tick(); await tick()    /* Rollenabruf scheitert */
    listeImpl = async () => { n++; return [{ id: 'b0', name: 'B0', role: 'owner' }] }
    await oeffnen()                          /* Menü-Laden gelingt → Negativ-Cache zurückgesetzt */
    const vor = n
    document.body.innerHTML = ''
    bauen({ id: 'n2', title: 'N2' }); await tick(); await tick()   /* n2 unbekannt */
    expect(n - vor).toBe(1)
  })

  it('F-1765: Listenabruf scheitert → Erstöffnen gelingt → der Neuaufbau ruft die Liste im Hintergrund wieder ab', async () => {
    let n = 0
    listeImpl = async () => { n++; throw new Error('kurz weg') }
    bauen(); await oeffnen()                 /* Liste scheitert → Negativ-Cache der Liste gesetzt */
    titel().click()                          /* schließen */
    listeImpl = async () => { n++; return [{ id: 'b-aktiv', name: 'Aktives Board', role: 'owner' }, { id: 'x', name: 'Fremd', role: 'editor' }] }
    await oeffnen()                          /* Erstöffnen gelingt (innerhalb der TTL) → Negativ-Cache zurückgesetzt */
    expect(labels()).toContain('Fremd')
    const aussen = document.createElement('input'); document.body.appendChild(aussen)
    const vor = n
    document.body.innerHTML = ''; document.body.appendChild(aussen)
    bauen(); aussen.focus()                  /* Neuaufbau bei offenem Menü (Fokus draußen) */
    await tick(); await tick(); await tick()
    expect(n - vor).toBe(1)                  /* Hintergrund-Neuabruf nicht unterdrückt */
  })

  it('F-1754: scheitert die Liste, steht ein Fehlertext statt „Keine weiteren Boards bekannt“', async () => {
    listeImpl = async () => { throw new Error('offline') }
    bauen(); await oeffnen()
    const kopfText = [...menu().querySelectorAll('.board-menu-head')].map(h => h.textContent).join('|')
    expect(kopfText).toMatch(/nicht ladbar|could not be loaded/)
    expect(kopfText).not.toMatch(/Keine weiteren|No other boards/)
  })
})
