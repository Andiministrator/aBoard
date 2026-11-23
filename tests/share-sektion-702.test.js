/* @vitest-environment happy-dom */

/**
 * Karte #702 — Board-Freigabe wandert von der Sidebar in den Dialog
 * Board-Einstellungen (Sektion „Freigabe“, ganz oben, nur Server-Modus).
 *
 * Geprüft wird:
 *  - Sektion steht ganz oben im Board-Dialog, nur im Server-Modus
 *  - Hinweis „Änderungen an der Freigabe wirken sofort.“ ist da
 *  - Owner (und Legacy ohne role) sieht Liste + Formular (ACL-Abruf)
 *  - Editor/Viewer sehen nur ihre Rolle und lösen KEINEN ACL-Abruf aus
 *  - Die Sidebar hat keine Freigabe-Sektion mehr
 *  - Ausserhalb des Server-Builds existiert die Sektion nicht (DCE)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer, IS_SERVER_BUILD } from './helpers/build-flags.js'

let boards = [{ id: 'b1', name: 'Test', title: 'Test', role: 'owner' }]
const aclListMock = vi.fn(async () => [{ email: 'a@x.de', role: 'owner' }])
const knownUsersMock = vi.fn(async () => ({ ok: true, users: [{ email: 'b@x.de', name: 'B' }], domains: [] }))

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
        listKnownBoards: vi.fn(async () => boards),
        aclList: aclListMock,
        aclSet: vi.fn(async () => ({ ok: true, status: 200 })),
        knownUsers: knownUsersMock,
      }),
    },
  }
})
vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Config }   from '../src/config.js'
import { State }    from '../src/state.js'
import { EventBus } from '../src/eventbus.js'
import { Profile }  from '../src/profile.js'
import { I18n }     from '../src/i18n.js'
import { Sidebar }  from '../src/sidebar.js'
import { ConfigDialog } from '../src/configdialog.js' // eslint-disable-line no-unused-vars -- registriert die EventBus-Handler
import { ShareSection } from '../src/share-section.js'

const flush = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0)) }

function sektion() { return document.querySelector('.config-share-section') }

beforeEach(() => {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
  boards = [{ id: 'b1', name: 'Test', title: 'Test', role: 'owner' }]
  aclListMock.mockClear()
  knownUsersMock.mockClear()
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

describeServer('#702 — Sektion „Freigabe“ im Board-Dialog (Server-Build)', () => {
  it('steht ganz oben im Dialog-Body und trägt den Sofort-Hinweis', async () => {
    serverModus()
    EventBus.emit('config:open-board')
    await flush()
    const body = document.querySelector('.config-dialog .modal-body')
    expect(body.firstElementChild).toBe(sektion())
    expect(sektion().querySelector('.config-separator').textContent).toBe(I18n.t('share.section-title'))
    const hinweis = sektion().querySelector('.share-sofort')
    expect(hinweis.textContent).toBe(I18n.t('share.sofort-hinweis'))
    expect(I18n.t('share.sofort-hinweis')).toMatch(/wirken sofort|take effect immediately/)
  })

  it('Owner sieht Liste und Formular (ACL-Abruf findet statt)', async () => {
    serverModus()
    EventBus.emit('config:open-board')
    await flush()
    expect(aclListMock).toHaveBeenCalledWith('b1')
    expect(sektion().querySelector('.share-form')).not.toBeNull()
    expect(sektion().querySelector('.share-acl-email').textContent).toBe('a@x.de')
  })

  it('Legacy ohne role: wie bisher — Liste wird geladen', async () => {
    boards = [{ id: 'b1', name: 'Test', title: 'Test' }]
    serverModus()
    EventBus.emit('config:open-board')
    await flush()
    expect(aclListMock).toHaveBeenCalled()
    expect(sektion().querySelector('.share-form')).not.toBeNull()
  })

  /* Direkt gegen die Sektion: der Backup-Bereich des Dialogs (Karte #335)
     macht für seinen Owner-Check einen eigenen ACL-Abruf, der hier nicht
     mitgezählt werden soll. */
  it.each(['editor', 'viewer'])('%s sieht nur die eigene Rolle und löst keinen ACL-Abruf aus', async (rolle) => {
    boards = [{ id: 'b1', name: 'Test', title: 'Test', role: rolle }]
    const s = ShareSection.build('b1')
    document.body.appendChild(s.el)
    await s.aktualisieren()
    await flush()
    expect(aclListMock).not.toHaveBeenCalled()
    expect(knownUsersMock).not.toHaveBeenCalled()
    expect(s.el.querySelector('.share-form')).toBeNull()
    expect(s.el.querySelector('.share-list')).toBeNull()
    expect(s.el.querySelector('.share-rolle').textContent)
      .toBe(I18n.t('share.nur-rolle', I18n.t(`share.role-${rolle}`)))
    /* KO-12c: „wirkt sofort“ betrifft nur, wer Freigaben ändern kann. */
    expect(s.el.querySelector('.share-sofort').hidden).toBe(true)
  })

  it('Dialog: Editor sieht die Rollenzeile statt des Formulars', async () => {
    boards = [{ id: 'b1', name: 'Test', title: 'Test', role: 'editor' }]
    serverModus()
    EventBus.emit('config:open-board')
    await flush()
    expect(sektion().querySelector('.share-rolle')).not.toBeNull()
    expect(sektion().querySelector('.share-form')).toBeNull()
  })

  it('Owner: build+aktualisieren ruft ACL-Liste und bekannte Nutzer ab', async () => {
    const s = ShareSection.build('b1')
    document.body.appendChild(s.el)
    await s.aktualisieren()
    await flush()
    expect(aclListMock).toHaveBeenCalledWith('b1')
    expect(knownUsersMock).toHaveBeenCalled()
  })

  it('Rollenwechsel zwischen zwei Öffnungen: Sektion wird neu geladen', async () => {
    serverModus()
    EventBus.emit('config:open-board')
    await flush()
    expect(sektion().querySelector('.share-form')).not.toBeNull()
    boards = [{ id: 'b1', name: 'Test', title: 'Test', role: 'viewer' }]
    aclListMock.mockClear()
    EventBus.emit('config:open-board')
    await flush()
    expect(sektion().querySelector('.share-form')).toBeNull()
  })

  it('ohne Server-Modus (feature.restStorage aus) keine Sektion', async () => {
    vi.spyOn(Profile, 'has').mockImplementation(() => false)
    EventBus.emit('config:open-board')
    await flush()
    expect(sektion()).toBeNull()
  })

  it('ShareSection.build ist vorhanden', () => {
    expect(typeof ShareSection.build).toBe('function')
  })
})

describe('#702 — ausserhalb des Server-Builds', () => {
  it.skipIf(IS_SERVER_BUILD)('keine Freigabe-Sektion und kein Modulinhalt (DCE)', async () => {
    vi.spyOn(Profile, 'has').mockImplementation((cap) => cap === 'feature.restStorage')
    EventBus.emit('config:open-board')
    await flush()
    expect(sektion()).toBeNull()
    expect(ShareSection).toBeNull()
  })
})

describe('#702 — Sidebar ohne Freigabe-Sektion', () => {
  it('Sidebar enthält weder Share-Container noch eine Sektion „Board teilen“', async () => {
    serverModus()
    await Sidebar.open({ id: 'b1', title: 'Test' })
    await flush()
    const sb = document.querySelector('.app-sidebar')
    expect(sb.querySelector('.share-container')).toBeNull()
    expect(sb.querySelector('.share-form')).toBeNull()
    expect(sb.textContent).not.toContain('Board teilen')
    expect(sb.textContent).not.toContain('Share board')
    expect(aclListMock).not.toHaveBeenCalled()
  })
})
