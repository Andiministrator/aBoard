/* @vitest-environment happy-dom */

/**
 * Karte #703 — KI-Anbieter wandern aus der Sidebar in die Benutzer-Einstellungen.
 *
 * Geprüft wird:
 *  - Sektion „KI-Anbieter“ steht im Dialog Benutzer-Einstellungen (nur Server-Modus),
 *    nicht in den Board-Einstellungen
 *  - Knopf „KI-Anbieter verwalten…“ öffnet den LLMKeysDialog
 *  - Firmenmodus: nur „Vom Betreiber bereitgestellt: …“, Knopf nur noch für die PII-Wahl
 *  - Die Sidebar hat keine KI-Anbieter-Zeile mehr; der Hinweistext verweist nicht mehr auf sie
 *  - Ausserhalb des Server-Builds existiert die Sektion nicht (DCE)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer, IS_SERVER_BUILD } from './helpers/build-flags.js'

let listAntwort = { ok: true, status: 200, body: { providers: [], keys: [] } }
const listMock = vi.fn(async () => listAntwort)
const oeffneMock = vi.fn()

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName:    vi.fn(() => null),
    getDirectoryHandle:  vi.fn(() => null),
    isActive:            vi.fn(() => false),
    isPermissionNeeded:  vi.fn(() => false),
  },
}))
vi.mock('../src/storage.js', () => ({ Storage: { listKnownFiles: vi.fn(async () => []) } }))
vi.mock('../src/storage-plugin.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    StoragePlugin: {
      getActivePlugin: () => ({
        listKnownBoards: vi.fn(async () => []),
        getCurrentUser: vi.fn(async () => ({ is_admin: false })),
      }),
    },
  }
})
vi.mock('../src/llm-keys-client.js', () => ({
  LLMKeysClient: { list: (...a) => listMock(...a) },
}))
vi.mock('../src/llm-keys-dialog.js', () => ({
  LLMKeysDialog: { open: (...a) => oeffneMock(...a), close: vi.fn() },
}))
vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Config }   from '../src/config.js'
import { State }    from '../src/state.js'
import { EventBus } from '../src/eventbus.js'
import { Profile }  from '../src/profile.js'
import { I18n }     from '../src/i18n.js'
import { Sidebar }  from '../src/sidebar.js'
import { ConfigDialog } from '../src/configdialog.js' // eslint-disable-line no-unused-vars -- registriert die EventBus-Handler
import { LLMSection } from '../src/llm-section.js'

const flush = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0)) }
const sektion = () => document.querySelector('.config-llm-section')

beforeEach(() => {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
  listAntwort = { ok: true, status: 200, body: { providers: [], keys: [] } }
  listMock.mockClear()
  oeffneMock.mockClear()
  State.set({ board: { id: 'b1', title: 'Test', identities: [], attachments: [] } })
  Config.set('boardMode', 'solo')
})
afterEach(() => {
  EventBus.emit('config:language-changed')
  vi.restoreAllMocks()
})

function serverModus() {
  return vi.spyOn(Profile, 'has').mockImplementation((cap) => cap === 'feature.restStorage')
}

describeServer('#703 — Sektion „KI-Anbieter“ in den Benutzer-Einstellungen (Server-Build)', () => {
  it('steht im Dialog Benutzer-Einstellungen, nicht in den Board-Einstellungen', async () => {
    serverModus()
    EventBus.emit('config:open-user')
    await flush()
    expect(sektion()).not.toBeNull()
    expect(sektion().querySelector('.config-separator').textContent).toBe(I18n.t('konto.section-title'))
    document.body.innerHTML = ''
    EventBus.emit('config:language-changed')
    EventBus.emit('config:open-board')
    await flush()
    expect(sektion()).toBeNull()
  })

  it('Knopf „KI-Anbieter verwalten…“ öffnet den LLMKeysDialog', async () => {
    serverModus()
    EventBus.emit('config:open-user')
    await flush()
    const knopf = sektion().querySelector('button')
    expect(knopf.textContent).toBe(I18n.t('llm.section-verwalten'))
    knopf.click()
    expect(oeffneMock).toHaveBeenCalledTimes(1)
  })

  it('Firmenmodus: nur „Vom Betreiber bereitgestellt“, Knopf führt zur PII-Wahl', async () => {
    listAntwort = { ok: true, status: 200, body: { providers: [], keys: [],
      firmenmodus: { ziel: 'llm.firma.example', anbieter: ['openai'], modelle: ['qwen3-32b', 'm2'] } } }
    serverModus()
    EventBus.emit('config:open-user')
    await flush()
    const text = sektion().textContent
    expect(text).toContain('llm.firma.example')
    expect(text).toContain('qwen3-32b, m2')
    expect(sektion().querySelector('.llm-section-text')).toBeNull()
    const knopf = sektion().querySelector('button')
    expect(knopf.textContent).toBe(I18n.t('llm.section-pii'))
    expect(text).not.toContain(I18n.t('llm.section-verwalten'))
  })

  it('F-1734: fokussierter Knopf bleibt beim Firmenmodus-Wechsel derselbe Knoten und behält den Fokus', async () => {
    let fertig
    listMock.mockImplementationOnce(() => new Promise(r => { fertig = r }))
    serverModus()
    EventBus.emit('config:open-user')
    await flush()
    const knopf = sektion().querySelector('button')
    knopf.focus()
    expect(document.activeElement).toBe(knopf)
    fertig({ ok: true, status: 200, body: { firmenmodus: { ziel: 'llm.firma.example', modelle: ['m1'] } } })
    await flush()
    expect(sektion().querySelector('button')).toBe(knopf)
    expect(document.activeElement).toBe(knopf)
    expect(knopf.textContent).toBe(I18n.t('llm.section-pii'))
    expect(sektion().textContent).toContain('llm.firma.example')
  })

  it('Antwort-Fehler: Normalfall bleibt bedienbar (Dialog meldet den Fehler selbst)', async () => {
    listAntwort = { ok: false, status: 503, body: null }
    serverModus()
    EventBus.emit('config:open-user')
    await flush()
    expect(sektion().querySelector('button').textContent).toBe(I18n.t('llm.section-verwalten'))
  })

  it('ohne Server-Modus (feature.restStorage aus) keine Sektion', async () => {
    vi.spyOn(Profile, 'has').mockImplementation(() => false)
    EventBus.emit('config:open-user')
    await flush()
    expect(sektion()).toBeNull()
  })

  it('Sidebar: keine KI-Anbieter-Zeile mehr, auch keine Sektion „Konto“ (Karte #706)', async () => {
    serverModus()
    await Sidebar.open({ id: 'b1', title: 'Test' })
    await flush()
    const sb = document.querySelector('.app-sidebar')
    expect(sb.textContent).not.toContain('AI providers')
    expect(sb.textContent).not.toContain('KI-Anbieter')
    expect(sb.textContent).not.toContain(I18n.t('notif.menu'))
    expect(sb.textContent).not.toContain(I18n.t('agz.menu'))
  })

  it('Hinweis ohne Key verweist nicht mehr auf die Sidebar', () => {
    expect(I18n.t('llm.provider.no-keys')).not.toMatch(/Sidebar|sidebar/)
  })
})

describe('#703 — ausserhalb des Server-Builds', () => {
  it.skipIf(IS_SERVER_BUILD)('keine KI-Anbieter-Sektion und kein Modulinhalt (DCE)', async () => {
    vi.spyOn(Profile, 'has').mockImplementation((cap) => cap === 'feature.restStorage')
    EventBus.emit('config:open-user')
    await flush()
    expect(sektion()).toBeNull()
    expect(LLMSection).toBeNull()
  })
})
