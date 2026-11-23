/* @vitest-environment happy-dom */

/**
 * Gezielte Fehlerpfade für localStorage (F-1762).
 *
 * Seit dem globalen Stub (tests/helpers/localstorage-setup.js, F-1746) läuft jeder
 * Test mit heilem localStorage; die frühere stille Absicherung der try/catch-Zweige
 * ist damit weg (Fehlerklasse 147). Hier wird der Ausfall absichtlich gebaut:
 *   - `fehlt`:  localStorage === undefined (Node ohne --localstorage-file)
 *   - `wirft`:  alle Methoden werfen (SecurityError wie im Safari-Privatmodus)
 *   - `wirft beim Schreiben`: Lesen geht, Schreiben wirft (QuotaExceededError)
 * Gruppen: Config (src/config.js), Autosave/Storage (src/storage.js),
 * Board-Menü „zuletzt benutzt“ (src/board-menu.js).
 * Dazu (F-1774, nur Server-Kontext): KI-Tab `_piiModus` (Senden) und Schlüsseldialog
 * (Auswahl lesen/schreiben von `aboard.pii.mode`).
 */

import { describe, it, expect, afterEach, vi } from 'vitest'

vi.mock('../src/sync.js', () => ({ Sync: { getDirectoryName: vi.fn(() => null) } }))
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ listKnownBoards: async () => [] }) },
}))
vi.mock('../src/llm-keys-client.js', () => ({
  LLMKeysClient: {
    list: vi.fn(async () => ({ ok: true, body: { providers: [], keys: [{ provider: 'anthropic' }] } })),
    set: vi.fn(), delete: vi.fn(), test: vi.fn(),
  },
}))
vi.mock('../src/profile.js', () => ({ Profile: { has: vi.fn(() => false) } }))

import { Config }    from '../src/config.js'
import { Storage }   from '../src/storage.js'
import { BoardMenu } from '../src/board-menu.js'
import { State }     from '../src/state.js'
import { LLMTab }    from '../src/llm-tab.js'
import { LLMKeysDialog } from '../src/llm-keys-dialog.js'
import { describeServer } from './helpers/build-flags.js'

const setze = (wert) => Object.defineProperty(globalThis, 'localStorage',
  { value: wert, writable: true, configurable: true })
const werfer = () => { throw new DOMException('gesperrt', 'SecurityError') }
const wirft = () => ({
  getItem: werfer, setItem: werfer, removeItem: werfer, clear: werfer, key: werfer,
  get length() { throw new DOMException('gesperrt', 'SecurityError') },
})
const wirftNurSchreiben = () => {
  const m = new Map()
  return {
    getItem: k => (m.has(k) ? m.get(k) : null),
    setItem: () => { throw new DOMException('voll', 'QuotaExceededError') },
    removeItem: k => { m.delete(k) }, clear: () => m.clear(),
    key: i => [...m.keys()][i] ?? null, get length() { return m.size },
  }
}
const FAELLE = [['fehlt', () => undefined], ['wirft', wirft], ['wirft beim Schreiben', wirftNurSchreiben]]

afterEach(() => { document.body.innerHTML = '' })

describe.each(FAELLE)('Config bei localStorage %s', (_n, bauen) => {
  it('load() wirft nicht und liefert Defaults', () => {
    setze(bauen())
    let c
    expect(() => { c = Config.load() }).not.toThrow()
    expect(c).toBeTruthy()
    expect(Config.get('theme')).toBeDefined()
  })

  it('set() wirft nicht und der Wert gilt im Speicher weiter', () => {
    setze(bauen())
    expect(() => Config.set('userName', 'Fehlerpfad')).not.toThrow()
    expect(Config.get('userName')).toBe('Fehlerpfad')
  })
})

describe.each(FAELLE)('Storage bei localStorage %s', (_n, bauen) => {
  it('autoSave() wirft nicht', () => {
    setze(bauen())
    expect(() => Storage.autoSave({ board: { id: 'b-1' } })).not.toThrow()
  })

  it('load() wirft nicht und liefert „kein Stand“ (embedded leer)', () => {
    setze(bauen())
    let r
    expect(() => { r = Storage.load() }).not.toThrow()
    expect(r.fromLocalStorage).toBe(false)
    expect(r.state).toBeNull()
  })

  it('discardAutosave() wirft nicht', () => {
    setze(bauen())
    expect(() => Storage.discardAutosave('b-1')).not.toThrow()
  })
})

describe.each(FAELLE)('Board-Menü „zuletzt benutzt“ bei localStorage %s', (_n, bauen) => {
  it('build() wirft nicht und der Titel steht im Kopf', () => {
    setze(bauen())
    State.set({ board: { id: 'b-x', title: 'Mein Board' } })
    let wrap
    expect(() => { wrap = BoardMenu.build({ id: 'b-x', title: 'Mein Board' }, { onRename() {} }) }).not.toThrow()
    document.body.appendChild(wrap)
    expect(document.querySelector('#board-title').textContent).toContain('Mein Board')
  })
})

describe('Gegenstück: heiler localStorage schreibt wirklich (Wächter gegen stille Schutzzweige)', () => {
  it('Config.set persistiert', () => {
    Config.load()
    Config.set('userName', 'Heil')
    const blobs = Array.from({ length: localStorage.length }, (_, i) => localStorage.getItem(localStorage.key(i)))
    expect(blobs.some(b => b.includes('Heil'))).toBe(true)
  })

  it('Storage.autoSave schreibt, discardAutosave räumt', () => {
    Storage.autoSave({ board: { id: 'b-heil', version: 99 } })
    expect(localStorage.length).toBe(1)
    Storage.discardAutosave('b-heil')
    expect(localStorage.length).toBe(0)
  })

  it('Board-Menü merkt das Board in aboard-recent:*', () => {
    State.set({ board: { id: 'b-merk', title: 'M' } })
    BoardMenu.build({ id: 'b-merk', title: 'M' }, { onRename() {} })
    const key = Array.from({ length: localStorage.length }, (_, i) => localStorage.key(i))
      .find(k => k.startsWith('aboard-recent:'))
    expect(JSON.parse(localStorage.getItem(key))).toEqual(['b-merk'])
  })
})

const ruhe = () => new Promise(r => setTimeout(r, 0))

describeServer('KI-Tab und Schlüsseldialog: PII-Modus bei kaputtem localStorage (F-1774)', () => {
  const mitPii = () => ({ id: 'c-1', displayId: 1, title: 'T', description: 'Kontakt: alice@example.com',
    comments: [], links: [], agentContext: '', activity: [], attachments: [] })

  describe.each(FAELLE.slice(1))('localStorage %s', (_n, bauen) => {
    it('Senden wirft nicht und fällt auf „immer prüfen“ zurück (PII-Modal öffnet)', async () => {
      let n = 0
      globalThis.fetch = vi.fn(async () => ({ ok: true, status: 200, json: async () => (n++ === 0
        ? { providers: [], keys: [{ provider: 'anthropic' }] }
        : { anthropic: { 'claude-opus-4-7': { InputPerMillionUSD: 15, OutputPerMillionUSD: 75 } } }) }))
      LLMTab._resetCachesForTest()
      const panel = LLMTab.build()
      document.body.appendChild(panel)
      await LLMTab.render(mitPii(), 'board-1')
      await ruhe()
      setze(bauen())
      panel.querySelector('#llm-send-btn').click()
      await new Promise(r => setTimeout(r, 5))
      expect(document.querySelector('.pii-modal')).toBeTruthy()
    })

    it('Schlüsseldialog baut auf, zeigt „immer prüfen“; Umschalten wirft nicht', async () => {
      setze(bauen())
      LLMKeysDialog.open()
      await ruhe(); await ruhe()
      const radios = [...document.querySelectorAll('input[name="aboard-pii-mode"]')]
      expect(radios.find(r => r.checked)?.value).toBe('always')
      const nie = radios.find(r => r.value === 'never')
      nie.checked = true
      expect(() => nie.dispatchEvent(new Event('change'))).not.toThrow()
    })
  })

  it('Dialog: „fehlt“ (undefined) fällt ebenfalls auf „immer prüfen“ zurück', async () => {
    setze(undefined)
    LLMKeysDialog.open()
    await ruhe(); await ruhe()
    expect([...document.querySelectorAll('input[name="aboard-pii-mode"]')].find(r => r.checked)?.value).toBe('always')
  })
})
