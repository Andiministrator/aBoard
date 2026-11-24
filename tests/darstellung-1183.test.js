/* @vitest-environment happy-dom */

/**
 * Karte #1183 / ADR-0153: Darstellung je Gerät — Ecken (round/square), Schrift (system/serif/mono),
 * Akzentfarbe (teal/blue/green/violet/graphite). Werte in Config (global wie theme), als
 * data-corners/-font/-accent an <html>; das CSS greift nur bei Nicht-Standardwerten.
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
vi.mock('../src/storage.js', () => ({ Storage: { listKnownFiles: vi.fn(async () => []) } }))
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ listKnownBoards: vi.fn(async () => []) }) },
}))
vi.mock('../src/attachments.js', () => ({
  uploadBoardFile:       vi.fn(async () => ({ id: 'att1' })),
  boardAttachmentUrl:    vi.fn(async () => null),
  deleteBoardAttachment: vi.fn(async () => {}),
  checkUpload:           vi.fn(() => ({ ok: true })),
}))
vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Config, GLOBAL_KEYS, DARSTELLUNG } from '../src/config.js'
import { SETTINGS_CLASSIFICATION } from '../src/settings-scope.js'
import { State }        from '../src/state.js'
import { EventBus }     from '../src/eventbus.js'
import { ConfigDialog } from '../src/configdialog.js' // registriert config:open-user
import { I18n }         from '../src/i18n.js'

const css = readFileSync(join(process.cwd(), 'src/style.css'), 'utf8')
const html = document.documentElement

beforeEach(() => {
  document.body.innerHTML = ''
  State.set({ board: { id: 'b1', title: 'Test-Board', identities: [] } })
  Config.set('boardMode', 'solo')
  Config.set('corners', 'round'); Config.set('font', 'system'); Config.set('accent', 'teal')
})

describe('Karte #1183 — Einstellungen', () => {
  it('Voreinstellung eckig (Andi 23.11.), sonst das bisherige Aussehen, global je Gerät, und steht als data-* an <html>', () => {
    expect(DARSTELLUNG).toEqual(['corners', 'font', 'accent'])
    expect([Config.DEFAULTS.corners, Config.DEFAULTS.font, Config.DEFAULTS.accent]).toEqual(['square', 'system', 'teal'])
    for (const k of DARSTELLUNG) {
      expect(GLOBAL_KEYS.has(k)).toBe(true)
      expect(SETTINGS_CLASSIFICATION[k]).toEqual({ persistScope: 'global', serverScope: 'user' })
    }
    Config.set('corners', 'round')
    expect(html.getAttribute('data-corners')).toBe('round')
    expect(html.getAttribute('data-font')).toBe('system')
  })

  it('der Benutzer-Dialog bietet die Werte an, zeigt sie als Vorschau und nimmt sie bei Abbrechen zurück', () => {
    EventBus.emit('config:open-user')
    const sel = k => document.querySelector(`.config-overlay #cfg-${k}`)
    expect([...sel('corners').options].map(o => o.value)).toEqual(['square', 'round'])
    expect([...sel('font').options].map(o => o.value)).toEqual(['system', 'serif', 'mono'])
    expect([...sel('accent').options].map(o => o.value)).toEqual(['teal', 'blue', 'green', 'violet', 'graphite'])
    sel('accent').value = 'violet'
    sel('accent').dispatchEvent(new Event('change'))
    expect(html.getAttribute('data-accent')).toBe('violet')
    const abbrechen = [...document.querySelectorAll('.config-overlay button.btn')].find(b => b.textContent === I18n.t('btn.cancel'))
    abbrechen.click()
    expect(html.getAttribute('data-accent')).toBe('teal')
    expect(Config.get('accent')).toBe('teal')
  })
})

/* WCAG-Kontrast, wie in ADR-0153 §4 */
function lum(hex) {
  const v = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255)
    .map(x => x <= 0.03928 ? x / 12.92 : ((x + 0.055) / 1.055) ** 2.4)
  return 0.2126 * v[0] + 0.7152 * v[1] + 0.0722 * v[2]
}
const kontrast = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05) }
const wert = (block, name) => block.match(new RegExp(`${name}:\\s*(#[0-9a-fA-F]{6})`))[1]

describe('Karte #1183 — CSS', () => {
  const hell = css.slice(css.indexOf(':root,'), css.indexOf('[data-theme="dark"] {'))
  const dunkel = css.slice(css.indexOf('[data-theme="dark"] {'), css.indexOf('@media (prefers-color-scheme: dark)'))
  const flaechen = b => ['--surface', '--surface-2', '--bg'].map(n => wert(b, n))

  for (const farbe of ['blue', 'green', 'violet', 'graphite']) {
    it(`Akzent „${farbe}“ hält AA ≥ 5:1 in Hell und Dunkel, Systemdunkel gleich Dunkel`, () => {
      const l = css.match(new RegExp(`\\n\\[data-accent="${farbe}"\\]\\s*\\{([^}]*)\\}`))[1]
      const d = css.match(new RegExp(`\\n\\[data-theme="dark"\\]\\[data-accent="${farbe}"\\]\\s*\\{([^}]*)\\}`))[1]
      const s = css.match(new RegExp(`:root:not\\(\\[data-theme="light"\\]\\)\\[data-accent="${farbe}"\\]\\s*\\{([^}]*)\\}`))[1]
      expect(s.trim()).toBe(d.trim())
      for (const [block, basis] of [[l, hell], [d, dunkel]]) {
        const a = wert(block, '--accent')
        for (const f of [...flaechen(basis), wert(block, '--accent-dim')]) expect(kontrast(a, f)).toBeGreaterThanOrEqual(5)
        expect(kontrast(wert(block, '--accent-btn'), '#ffffff')).toBeGreaterThanOrEqual(5)
      }
    })
  }

  it('jedes Element mit border-radius 50 % steht in der Kreis-Ausnahme für „eckig“', () => {
    const ausnahme = [
      ...css.match(/\[data-corners="square"\] :is\(([^)]*)\) \{ border-radius: 50% !important/)[1].split(','),
      /* Pille bleibt Pille (Sitzungs-Kritik 23.11. A B-2) */
      ...[...css.matchAll(/\[data-corners="square"\] (\.[\w-]+) \{ border-radius: 999px !important/g)].map(m => m[1]),
    ].map(s => s.trim()).sort()
    const kreise = [...css.matchAll(/(^|\})\s*([^{}]+)\{[^}]*border-radius:\s*(50%|999px);/g)]
      .map(m => m[2].replace(/\/\*[\s\S]*?\*\//g, '').trim()).sort()
    expect(ausnahme).toEqual(kreise)
    /* Server-CSS (stripServerCss) bleibt per eigenem !important rund, nicht über die Liste */
    expect(css).toMatch(/\.board-header-avatar \{[^}]*border-radius: 50% !important/)
  })

  it('Kaskade: „eckig“ nimmt Rundungen weg, Kreise bleiben; Akzent greift erst mit gesetztem Wert', () => {
    const st = document.createElement('style'); st.textContent = css; document.head.appendChild(st)
    /* happy-dom hält den berechneten Stil eines Elements fest; je Zustand frische Elemente */
    const frisch = () => {
      document.body.innerHTML = '<div class="card">x</div><span class="identity-avatar">A</span>'
      const s = sel => getComputedStyle(document.querySelector(sel))
      return { card: s('.card'), ava: s('.identity-avatar'), root: getComputedStyle(html) }
    }
    html.setAttribute('data-corners', 'round'); html.setAttribute('data-accent', 'teal')
    let z = frisch()
    expect(z.card.borderTopLeftRadius).not.toMatch(/^0(px)?$/)
    const tealAkzent = z.root.getPropertyValue('--accent').trim()
    html.setAttribute('data-corners', 'square'); html.setAttribute('data-accent', 'blue')
    z = frisch()
    expect(z.card.borderTopLeftRadius).toMatch(/^0(px)?$/)
    expect(z.ava.borderTopLeftRadius).toBe('50%')
    expect(tealAkzent.toLowerCase()).toBe('#0b5d73')
    expect(z.root.getPropertyValue('--accent').trim().toLowerCase()).toBe('#205fb6')
    st.remove()
  })
})
