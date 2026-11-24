/* @vitest-environment happy-dom */

/**
 * Karte #973: Kartendichte „kompakt“ als Benutzer-Einstellung (Vorbild Planka).
 * Der Wert liegt in Config (`density`, global wie `theme`) und steht als
 * `data-density` an <html>; das CSS blendet im Modus „kompakt“ Cover und
 * Zusatzabzeichen aus und verringert den Innenabstand.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName:    vi.fn(() => null),
    isActive:            vi.fn(() => false),
    isPermissionNeeded:  vi.fn(() => false),
  },
}))

vi.mock('../src/storage.js', () => ({
  Storage: { listKnownFiles: vi.fn(async () => []) },
}))

vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      listKnownBoards: vi.fn(async () => []),
    }),
  },
}))

vi.mock('../src/attachments.js', () => ({
  uploadBoardFile:       vi.fn(async () => ({ id: 'att1' })),
  boardAttachmentUrl:    vi.fn(async () => null),
  deleteBoardAttachment: vi.fn(async () => {}),
  checkUpload:           vi.fn(() => ({ ok: true })),
}))

vi.mock('../src/swipe.js', () => ({
  addSwipeToDismiss: vi.fn(),
}))

vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Config }       from '../src/config.js'
import { State }        from '../src/state.js'
import { EventBus }     from '../src/eventbus.js'
import { ConfigDialog } from '../src/configdialog.js'
import { GLOBAL_KEYS }  from '../src/config.js'

beforeEach(() => {
  document.body.innerHTML = ''
  State.set({ board: { id: 'b1', title: 'Test-Board', identities: [] } })
  Config.set('boardMode', 'solo')
  Config.set('density', 'normal')
})

describe('Karte #973 — Kartendichte', () => {
  it('Voreinstellung ist „normal“, global wie das Theme, und steht als data-density an <html>', () => {
    expect(Config.DEFAULTS.density).toBe('normal')
    expect(GLOBAL_KEYS.has('density')).toBe(true)
    expect(document.documentElement.getAttribute('data-density')).toBe('normal')
    Config.set('density', 'kompakt')
    expect(Config.get('density')).toBe('kompakt')
    expect(document.documentElement.getAttribute('data-density')).toBe('kompakt')
  })

  it('der Benutzer-Dialog bietet normal/kompakt an und zeigt die Wahl sofort als Vorschau', () => {
    EventBus.emit('config:open-user')
    const sel = document.querySelector('.config-overlay #cfg-density')
    expect(sel).toBeTruthy()
    expect([...sel.options].map(o => o.value)).toEqual(['normal', 'kompakt'])
    expect(sel.value).toBe('normal')
    sel.value = 'kompakt'
    sel.dispatchEvent(new Event('change'))
    expect(document.documentElement.getAttribute('data-density')).toBe('kompakt')
  })

  it('das CSS blendet im Modus „kompakt“ Cover und Zusatzabzeichen aus, Flags, Datum und Owner bleiben', () => {
    const css = readFileSync(join(process.cwd(), 'src/style.css'), 'utf8')
    /* F-2711: ausgeschriebene Selektoren, kein Nesting — der Block reicht vom Kommentar bis zur nächsten fremden Regel */
    const von = css.indexOf('/* Karte #973: Kartendichte')
    const regeln = css.slice(von, css.indexOf('.card-title { font-size: 13px'))
    expect(regeln).toMatch(/\.card--has-cover \{ background: var\(--surface\) !important/)
    /* Liste der Abzeichen, die bleiben (alles andere in der Fußzeile ist aus) */
    const bleiben = regeln.match(/\.card-footer>:not\(([^)]*)\)/)[1].split(',').map(x => x.trim())
    expect(bleiben.sort()).toEqual(['.card-date-badge', '.card-flag-badge', '.card-link-blocked-badge',
      '.card-owner-badge', '.card-qa-badge'])
    /* F-2707: Den Namen blendet nur aus, wer einen Avatar hat; sonst bliebe eine leere Pille */
    expect(regeln).toMatch(/\.card-owner-badge--avatar \.card-owner-badge-label \{ display: none; \}/)
    /* Wunsch des Maintainers 29.10.: Owner in die Label-Zeile (Fußzeile rechtsbündig daneben), Titel auf zwei Zeilen */
    expect(regeln).toMatch(/\.card \{[^}]*display: flex; flex-wrap: wrap/)
    expect(regeln).toMatch(/\.card-footer \{ margin: 6px 0 0 auto; \}/)
    expect(regeln).toMatch(/-webkit-line-clamp: 2/)
  })
})
