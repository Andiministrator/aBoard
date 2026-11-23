/* @vitest-environment happy-dom */

/**
 * ADR-0087 / F-318: deployment-gescopter Autosave-Puffer.
 *
 * Auf `file://` teilen sich alle `aboard.html` einer Maschine localStorage
 * (Opaque-Origin). Vor dem Fix adoptierte `Storage.load()` den ERSTEN
 * `aboard-autosave-*`-Schlüssel — auch den eines fremden Deployments
 * (F-318-Bleed). Der Fix scopt den Schlüssel per `location.pathname` (wie
 * ADR-0048 für die Config). F-NEU-1 (Sprint-38-Kritiker): ungescopte
 * Legacy-Schlüssel werden NICHT hart verworfen, sondern einmalig in den
 * scoped Slot MIGRIERT (Solo-Datenverlust-Schutz) und erst danach entfernt.
 *
 * localStorage ist in vitest 4 + happy-dom v20 + Node 26 nicht global —
 * In-Memory-Mock mit length/key() (von storage.js genutzt). pathname gestubbt.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { Storage } from '../src/storage.js'

const LEGACY = 'aboard-autosave-'
const PATH   = '/home/nutzer/projects/test/aboard.html'

function createLocalStorageMock() {
  const store = {}
  return {
    getItem:    (k)    => (k in store ? store[k] : null),
    setItem:    (k, v) => { store[k] = String(v) },
    removeItem: (k)    => { delete store[k] },
    clear:      ()     => { Object.keys(store).forEach(k => delete store[k]) },
    key:        (i)    => Object.keys(store)[i] ?? null,
    get length() { return Object.keys(store).length },
    _keys:      ()     => Object.keys(store),
  }
}

let ls

beforeEach(() => {
  ls = createLocalStorageMock()
  Object.defineProperty(globalThis, 'localStorage', {
    value: ls, writable: true, configurable: true,
  })
  Object.defineProperty(globalThis, 'location', {
    value: { pathname: PATH }, writable: true, configurable: true,
  })
  document.body.innerHTML = '' /* kein #app-data → load() scannt localStorage */
})

afterEach(() => {
  Object.defineProperty(globalThis, 'localStorage', {
    value: undefined, writable: true, configurable: true,
  })
})

function minimalState(id) {
  return { version: '2', board: { id, title: 'B', columnDefs: [], sprints: [] } }
}

describe('ADR-0087 — Autosave deployment-scoped (F-318)', () => {
  it('autoSave schreibt unter einen pathname-gescopten Schlüssel, NICHT ungescopt', () => {
    Storage.autoSave(minimalState('board-1'))
    const keys = ls._keys()
    expect(keys.length).toBe(1)
    expect(keys[0]).toBe(`aboard-autosave@${PATH}-board-1`)
    expect(keys[0].startsWith(LEGACY + 'board-1')).toBe(false)
  })

  /* F-NEU-1 (Sprint-38-Kritiker, [K] P1 Regression): im Solo-Modus ist der
     Autosave-Puffer die EINZIGE Persistenz. Der Legacy-Key darf beim Upgrade
     NICHT hart verworfen werden — sein Inhalt muss in den scoped Slot
     migriert werden, sonst Datenverlust für Bestands-Solo-Nutzer. */
  it('F-NEU-1: Bestands-Solo-Puffer überlebt das Upgrade (Legacy → scoped migriert, kein Datenverlust)', () => {
    ls.setItem(LEGACY + 'bestand', JSON.stringify(minimalState('bestand')))
    const { state, fromLocalStorage } = Storage.load()
    /* Board NICHT verloren */
    expect(state?.board?.id).toBe('bestand')
    expect(fromLocalStorage).toBe(true)
    /* Inhalt liegt jetzt im scoped Slot … */
    expect(ls.getItem(`aboard-autosave@${PATH}-bestand`)).not.toBeNull()
    /* … und die ungescopte Bleed-Quelle ist erst NACH der Migration weg */
    expect(ls.getItem(LEGACY + 'bestand')).toBeNull()
  })

  it('migriert NICHT über einen bereits vorhandenen scoped Slot (scoped ist autoritativ), entfernt aber den Legacy-Key', () => {
    ls.setItem(`aboard-autosave@${PATH}-dup`, JSON.stringify(minimalState('scoped-neu')))
    ls.setItem(LEGACY + 'dup', JSON.stringify(minimalState('legacy-alt')))
    const { state } = Storage.load()
    expect(state?.board?.id).toBe('scoped-neu')          /* scoped gewinnt */
    expect(ls.getItem(LEGACY + 'dup')).toBeNull()        /* Legacy trotzdem weg */
  })

  it('Round-Trip: autoSave dann load() liefert dasselbe Board über den gescopten Schlüssel', () => {
    Storage.autoSave(minimalState('rt-board'))
    const { state, fromLocalStorage } = Storage.load()
    expect(fromLocalStorage).toBe(true)
    expect(state?.board?.id).toBe('rt-board')
  })

  it('discardAutosave entfernt gescopten UND Legacy-Schlüssel derselben boardId', () => {
    Storage.autoSave(minimalState('x'))
    ls.setItem(LEGACY + 'x', '{}')
    Storage.discardAutosave('x')
    expect(ls._keys().filter(k => k.includes('-x') || k.endsWith('x'))).toEqual([])
  })

  it('gescopte Schlüssel anderer Deployments bleiben unangetastet (kein Über-Purge)', () => {
    const otherScoped = 'aboard-autosave@/other/path-other'
    ls.setItem(otherScoped, JSON.stringify(minimalState('other')))
    Storage.load()
    expect(ls.getItem(otherScoped)).not.toBeNull()
  })
})
