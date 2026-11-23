/* @vitest-environment happy-dom */

/**
 * Karte #706 — Konto-Zeilen (Benachrichtigungen, Agenten-Zugänge, Schleusen-
 * Schlüssel) wandern aus der Sidebar in die Benutzer-Einstellungen.
 *
 * Geprüft wird (Vorlage: llm-section-703.test.js):
 *  - die Knöpfe stehen im Dialog Benutzer-Einstellungen und öffnen die Dialoge
 *  - der Schleusen-Knopf erscheint nur, wenn GET /me `schleuse: "an"` meldet
 *  - Board-Einstellungen und Nicht-Server-Modus zeigen sie nicht
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

let listAntwort = { ok: true, status: 200, body: { providers: [], keys: [] } }
const listMock = vi.fn(async () => listAntwort)
const oeffneMock = vi.fn()
const notifMock = vi.fn()
const agzMock = vi.fn()

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
vi.mock('../src/notifications-dialog.js', () => ({
  NotificationsDialog: { open: (...a) => notifMock(...a) },
}))
vi.mock('../src/agent-zugaenge-dialog.js', () => ({
  AgentZugaengeDialog: { open: (...a) => agzMock(...a) },
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
import { SchleuseSchluesselDialog } from '../src/schleuse-schluessel.js'
import { ConfigDialog } from '../src/configdialog.js' // eslint-disable-line no-unused-vars -- registriert die EventBus-Handler

const flush = async () => { for (let i = 0; i < 8; i++) await new Promise(r => setTimeout(r, 0)) }
const knoepfe = () => [...document.querySelectorAll('.config-llm-section .llm-konto button')]
const finde = (key) => knoepfe().find(b => b.textContent.includes(I18n.t(key)))

let meAntwort
beforeEach(() => {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
  notifMock.mockClear(); agzMock.mockClear()
  meAntwort = {}
  SchleuseSchluesselDialog?.vergissSchalter?.()
  State.set({ board: { id: 'b1', title: 'Test', identities: [], attachments: [] } })
  Config.set('boardMode', 'solo')
  vi.spyOn(Profile, 'has').mockImplementation((cap) => cap === 'feature.restStorage')
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, json: async () => meAntwort })))
})
afterEach(() => {
  EventBus.emit('config:language-changed')
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describeServer('#706 — Konto-Zeilen in den Benutzer-Einstellungen (Server-Build)', () => {
  it('Benachrichtigungen und Agenten-Zugänge öffnen ihre Dialoge', async () => {
    EventBus.emit('config:open-user')
    await flush()
    finde('notif.menu').click()
    expect(notifMock).toHaveBeenCalledTimes(1)
    finde('agz.menu').click()
    expect(agzMock).toHaveBeenCalledTimes(1)
  })

  it('Schleusen-Knopf nur bei schleuse: "an" — sonst fehlt er', async () => {
    EventBus.emit('config:open-user')
    await flush()
    expect(finde('schleuse.menu')).toBeUndefined()
    document.body.innerHTML = ''
    EventBus.emit('config:language-changed')
    meAntwort = { schleuse: 'an' }
    SchleuseSchluesselDialog.vergissSchalter()
    const offen = vi.spyOn(SchleuseSchluesselDialog, 'open').mockImplementation(() => {})
    EventBus.emit('config:open-user')
    await flush()
    finde('schleuse.menu').click()
    expect(offen).toHaveBeenCalledTimes(1)
  })

  it('Board-Einstellungen zeigen die Konto-Zeilen nicht', async () => {
    EventBus.emit('config:open-board')
    await flush()
    expect(knoepfe()).toHaveLength(0)
  })

  it('ohne Server-Modus keine Konto-Zeilen', async () => {
    vi.spyOn(Profile, 'has').mockImplementation(() => false)
    EventBus.emit('config:open-user')
    await flush()
    expect(knoepfe()).toHaveLength(0)
  })
})
