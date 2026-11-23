/* @vitest-environment happy-dom */

/**
 * „Board löschen…“ im Board-Menü (Karte #845, F-2237).
 * Abnahme: (a) Menüpunkt nur für Eigentümer, (b) Knopf erst bei exakt
 * eingetipptem Titel aktiv, (c) Erfolg ruft DELETE und wechselt weg,
 * (d) Fehlerantwort wird angezeigt, das Board bleibt.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

vi.mock('../src/sync.js', () => ({ Sync: { getDirectoryName: vi.fn(() => null) } }))
vi.mock('../src/storage.js', () => ({ Storage: { listKnownFiles: async () => [] } }))

let liste = []
const deleteBoard = vi.fn()
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ listKnownBoards: async () => liste, deleteBoard: (...a) => deleteBoard(...a) }) },
}))
vi.mock('../src/profile.js', () => ({ Profile: { has: vi.fn(f => f === 'feature.restStorage') } }))

import { EventBus } from '../src/eventbus.js'
import { State } from '../src/state.js'
import { BoardMenu } from '../src/board-menu.js'
import { BoardLoeschen } from '../src/board-loeschen.js'

const BOARD = { id: 'b-aktiv', title: 'Aktives Board' }
const tick = () => new Promise(r => setTimeout(r, 0))
let uhr = Date.UTC(2027, 0, 1)

async function menuOeffnen(rolle) {
  liste = [{ id: 'b-aktiv', name: 'Aktives Board', role: rolle, updatedAt: 1 },
           { id: 'b-alt', name: 'Alt', role: 'owner', updatedAt: 5 },
           { id: 'b-neu', name: 'Neu', role: 'editor', updatedAt: 9 }]
  State.set({ board: BOARD }); State.markClean()
  document.body.appendChild(BoardMenu.build(BOARD, { onRename() {} }))
  document.querySelector('#board-title').click()
  await tick(); await tick()
}
const punkt = () => [...document.querySelectorAll('.board-menu-item')].find(b => /löschen|Delete board/i.test(b.textContent))
const dlg = () => document.querySelector('.bl-overlay')
const feld = () => dlg().querySelector('.bl-input')
const ok = () => dlg().querySelector('.bl-ok')
const tippe = (t) => { feld().value = t; feld().dispatchEvent(new Event('input', { bubbles: true })) }

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(uhr += 3600e3)
  /* Modulzustand „offenes Menü“ des Vorgängertests verwerfen (wie board-menu-704.test.js). */
  document.body.appendChild(BoardMenu.build({ id: '__reset__', title: '' }, { onRename() {} }))
  document.body.innerHTML = ''
  document.querySelectorAll('.bl-overlay').forEach(n => n.remove())
  localStorage.clear()
  deleteBoard.mockReset()
})

describeServer('Board löschen — Menüpunkt (#845 a)', () => {
  it('Eigentümer sieht „Board löschen…“', async () => {
    await menuOeffnen('owner')
    expect(punkt()).toBeTruthy()
  })
  it('Editor und Betrachter sehen ihn nicht', async () => {
    await menuOeffnen('editor')
    expect(punkt()).toBeFalsy()
    document.body.innerHTML = ''
    await menuOeffnen('viewer')
    expect(punkt()).toBeFalsy()
  })
  it('Klick öffnet den Dialog, Löschen-Knopf zunächst gesperrt', async () => {
    await menuOeffnen('owner')
    punkt().click()
    expect(dlg().classList.contains('modal-overlay--open')).toBe(true)
    expect(ok().disabled).toBe(true)
    expect(deleteBoard).not.toHaveBeenCalled()
  })
})

describeServer('Board löschen — Dialog (#845 b–d)', () => {
  const haken = { anhalten: vi.fn(async () => {}), fortsetzen: vi.fn(), melde: vi.fn() }
  beforeEach(() => {
    Object.values(haken).forEach(f => f.mockClear())
    BoardLoeschen.konfiguriere(haken)
    liste = [{ id: 'b-aktiv', updatedAt: 1 }, { id: 'b-alt', updatedAt: 5 }, { id: 'b-neu', updatedAt: 9 }]
    State.set({ board: BOARD }); State.markClean()
    BoardLoeschen.open(BOARD)
  })

  it('(b) Knopf erst bei exakt eingetipptem Titel (Rand-Leerzeichen egal), auch die ID zählt', () => {
    expect(ok().disabled).toBe(true)
    tippe('Aktives'); expect(ok().disabled).toBe(true)
    tippe('aktives board'); expect(ok().disabled).toBe(true)
    tippe('  Aktives Board '); expect(ok().disabled).toBe(false)
    tippe('Aktives Boar'); expect(ok().disabled).toBe(true)
    tippe('b-aktiv'); expect(ok().disabled).toBe(false)
  })

  it('(c) Erfolg: haelt an, ruft DELETE, wechselt aufs jüngste andere Board', async () => {
    deleteBoard.mockResolvedValue({ ok: true, status: 204 })
    const wechsel = vi.fn(); EventBus.on('sync:switch-to-board', wechsel)
    tippe('Aktives Board'); ok().click()
    await tick(); await tick(); await tick()
    expect(haken.anhalten).toHaveBeenCalled()
    expect(deleteBoard).toHaveBeenCalledWith('b-aktiv')
    expect(wechsel).toHaveBeenCalledWith({ boardId: 'b-neu' })
    expect(haken.melde).toHaveBeenCalled()
    expect(haken.fortsetzen).not.toHaveBeenCalled()
    expect(dlg().classList.contains('modal-overlay--open')).toBe(false)
  })

  it('(c) kein anderes Board übrig: neues Board statt totem Zustand', async () => {
    liste = [{ id: 'b-aktiv', updatedAt: 1 }]
    deleteBoard.mockResolvedValue({ ok: true, status: 204 })
    const neu = vi.fn(); EventBus.on('server:new-board', neu)
    tippe('Aktives Board'); ok().click()
    await tick(); await tick(); await tick()
    expect(neu).toHaveBeenCalledWith({ title: 'Board' }) /* übersetzter Titel (board.delete.new-title), kein Schlüsselname */
  })

  for (const [status, muster] of [[403, /Eigentümer|owners/i], [409, /Server-Fehler|Server error/i], [404, /Server-Fehler|Server error/i], [500, /Server-Fehler|Server error/i], [0, /nicht erreichbar|unreachable/i]]) {
    it(`(d) Fehler ${status}: Meldung steht im Dialog, Board bleibt, Polling läuft wieder`, async () => {
      deleteBoard.mockResolvedValue({ ok: false, status })
      const wechsel = vi.fn(); EventBus.on('sync:switch-to-board', wechsel)
      tippe('Aktives Board'); ok().click()
      await tick(); await tick()
      expect(deleteBoard).toHaveBeenCalled()
      expect(dlg().querySelector('.bl-msg').textContent).toMatch(muster)
      expect(dlg().classList.contains('modal-overlay--open')).toBe(true)
      expect(haken.fortsetzen).toHaveBeenCalled()
      expect(wechsel).not.toHaveBeenCalledWith({ boardId: 'b-neu' })
      expect(ok().disabled).toBe(false) /* erneut versuchbar */
    })
  }

  /* Kritiker Sprint 97 K-1: 409 mit Zahl verweist auf den Admin-Notweg. */
  it('(d) 409 mit offenen Änderungen: Zahl und Verweis auf „Kopie außen löschen“', async () => {
    deleteBoard.mockResolvedValue({ ok: false, status: 409, offene: 3 })
    tippe('Aktives Board'); ok().click()
    await tick(); await tick()
    const text = dlg().querySelector('.bl-msg').textContent
    expect(text).toMatch(/3 Änderungen|3 changes/)
    expect(text).toMatch(/Kopie außen löschen|Delete copy/)
    expect(dlg().classList.contains('modal-overlay--open')).toBe(true)
  })

  /* Kritiker Sprint 97 K-2: 404 = schon weg → wegwechseln, nicht stehen bleiben. */
  it('(c) 404: Board war schon weg — Browser wechselt weg, Meldung „schon gelöscht“', async () => {
    deleteBoard.mockResolvedValue({ ok: false, status: 404, gone: true })
    const wechsel = vi.fn(); EventBus.on('sync:switch-to-board', wechsel)
    tippe('Aktives Board'); ok().click()
    await tick(); await tick(); await tick()
    expect(wechsel).toHaveBeenCalledWith({ boardId: 'b-neu' })
    expect(haken.melde).toHaveBeenCalledWith(expect.stringMatching(/schon gelöscht|already been deleted/))
    expect(dlg().classList.contains('modal-overlay--open')).toBe(false)
  })

  /* Kritiker Sprint 97 K-2: Esc während DELETE schließt nicht. */
  it('Esc während DELETE läuft: Dialog bleibt offen, Fehler danach sichtbar', async () => {
    let fertig
    deleteBoard.mockReturnValue(new Promise(r => { fertig = r }))
    tippe('Aktives Board'); ok().click()
    await tick()
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    dlg().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(dlg().classList.contains('modal-overlay--open')).toBe(true)
    fertig({ ok: false, status: 500 })
    await tick(); await tick()
    expect(dlg().classList.contains('modal-overlay--open')).toBe(true)
    expect(dlg().querySelector('.bl-msg').textContent).toMatch(/Server-Fehler|Server error/i)
  })
})
