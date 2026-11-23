/* @vitest-environment happy-dom */
/**
 * Nacharbeit Karten #684/#702 (Kritiker-Befunde KO-2, KO-4b, KO-12): Schleusen-
 * Board-Sperre in der Freigabe-Sektion des Dialogs, Wache gegen veraltete
 * Antworten in `aktualisieren()`, neutraler Leerzustand, Partition, A11y.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

let boards = [{ id: 'b1', name: 'T', title: 'T', role: 'owner' }]
let listBoardsImpl = null
let aclAntwort = []
const aclListMock = vi.fn(async () => aclAntwort)

vi.mock('../src/sync.js', () => ({
  Sync: { getDirectoryName: vi.fn(() => null), getDirectoryHandle: vi.fn(() => null),
    isActive: vi.fn(() => false), isPermissionNeeded: vi.fn(() => false) },
}))
vi.mock('../src/storage.js', () => ({ Storage: { listKnownFiles: vi.fn(async () => []) } }))
vi.mock('../src/storage-plugin.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    StoragePlugin: {
      getActivePlugin: () => ({
        listKnownBoards: vi.fn(async () => (listBoardsImpl ? listBoardsImpl() : boards)),
        aclList: aclListMock,
        aclSet: vi.fn(async () => ({ ok: true, status: 200 })),
        knownUsers: vi.fn(async () => ({ ok: true, users: [{ email: 'b@x.de', name: 'B' }], domains: [] })),
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
import { ConfigDialog } from '../src/configdialog.js' // eslint-disable-line no-unused-vars -- registriert die EventBus-Handler
import { ShareSection } from '../src/share-section.js'
import { SchleuseSchluesselDialog } from '../src/schleuse-schluessel.js'

const flush = async () => { for (let i = 0; i < 6; i++) await new Promise(r => setTimeout(r, 0)) }
const sektion = () => document.querySelector('.config-share-section')

let me, sperre
beforeEach(() => {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
  boards = [{ id: 'b1', name: 'T', title: 'T', role: 'owner' }]
  listBoardsImpl = null
  aclAntwort = [{ email: 'a@x.de', role: 'owner' }]
  aclListMock.mockClear()
  State.set({ board: { id: 'b1', title: 'T', identities: [], attachments: [] } })
  Config.set('boardMode', 'solo')
  me = { status: 200, body: { email: 'a@x.de', schleuse: 'an' } }
  sperre = { status: 200, body: { gesperrt: false, darf_aendern: true } }
  SchleuseSchluesselDialog?.vergissSchalter()
  vi.stubGlobal('fetch', vi.fn(async (p) => {
    const r = String(p) === '/me' ? me : (String(p).endsWith('/schleuse-sperre') ? sperre : { status: 404, body: null })
    return { ok: r.status >= 200 && r.status < 300, status: r.status, json: async () => r.body }
  }))
})
afterEach(() => { EventBus.emit('config:language-changed'); vi.restoreAllMocks(); vi.unstubAllGlobals() })

const serverModus = () => vi.spyOn(Profile, 'has').mockImplementation((cap) => cap === 'feature.restStorage')

describeServer('KO-2 — Schleusen-Board-Sperre in der Freigabe-Sektion', () => {
  it('/me meldet schleuse:an und die Sperre ist änderbar → Schalter steht IN .config-share-section', async () => {
    serverModus()
    EventBus.emit('config:open-board')
    await flush()
    const cb = sektion().querySelector('.sls-sperre input[type=checkbox]')
    expect(cb).not.toBeNull()
    expect(cb.closest('.config-share-section')).toBe(sektion())
  })
  it('Schleuse aus → kein Schalter in der Sektion', async () => {
    me = { status: 200, body: { email: 'a@x.de' } }
    serverModus()
    EventBus.emit('config:open-board')
    await flush()
    expect(sektion()).not.toBeNull()
    expect(sektion().querySelector('.sls-sperre')).toBeNull()
  })
  it('auch für Editor/Viewer (Rollenzeile statt Formular): Sperre wird ergänzt', async () => {
    boards = [{ id: 'b1', name: 'T', title: 'T', role: 'editor' }]
    sperre = { status: 200, body: { gesperrt: true, darf_aendern: false } }
    serverModus()
    EventBus.emit('config:open-board')
    await flush()
    expect(sektion().querySelector('.sls-sperre').textContent).toContain(I18n.t('schleuse.sperre-gesperrt'))
  })
})

describeServer('KO-4b — aktualisieren(): nur der jüngste Lauf gilt', () => {
  it('zwei Läufe mit verzögertem listKnownBoards, Rollenwechsel → Ergebnis des jüngeren', async () => {
    const s = ShareSection.build('b1')
    document.body.appendChild(s.el)
    const tore = []
    listBoardsImpl = () => new Promise(r => tore.push(r))
    const lauf1 = s.aktualisieren() /* wird später „viewer“ melden */
    const lauf2 = s.aktualisieren() /* meldet „owner“ */
    await flush()
    expect(tore.length).toBe(2)
    tore[1]([{ id: 'b1', role: 'owner' }])
    await flush()
    expect(s.el.querySelector('.share-form')).not.toBeNull()
    tore[0]([{ id: 'b1', role: 'viewer' }]) /* verspätet, veraltet */
    await Promise.all([lauf1, lauf2]); await flush()
    expect(s.el.querySelector('.share-form')).not.toBeNull()
    expect(s.el.querySelector('.share-rolle')).toBeNull()
    expect(s.el.querySelectorAll('.share-inhalt').length).toBe(1)
  })
})

describeServer('KO-12 — Leerzustand, Partition, A11y', () => {
  it('(a) Rolle unbekannt + leere Liste → neutraler Satz statt „Noch niemand hat Zugriff“', async () => {
    boards = [{ id: 'b1', name: 'T', title: 'T' }] /* Legacy: keine Rolle */
    aclAntwort = []
    const s = ShareSection.build('b1')
    document.body.appendChild(s.el)
    await s.aktualisieren(); await flush()
    const t = s.el.querySelector('.share-list').textContent
    expect(t).toBe(I18n.t('share.empty-unsicher'))
    expect(t).not.toContain('Noch niemand hat Zugriff')
    expect(I18n.t('share.empty-unsicher')).toMatch(/nicht Owner|not an owner/)
  })
  it('VO-6: auch der Owner (Rolle bekannt) sieht bei leerer Liste den neutralen Satz', async () => {
    aclAntwort = []
    const s = ShareSection.build('b1')
    document.body.appendChild(s.el)
    await s.aktualisieren(); await flush()
    const t = s.el.querySelector('.share-list').textContent
    expect(t).toBe(I18n.t('share.empty-unsicher'))
    expect(t).not.toContain('Noch niemand hat Zugriff')
  })
  it('VO-7: aria-busy bleibt true, bis auch die ACL-Liste geladen ist (nicht nur die Rolle)', async () => {
    let frei
    aclListMock.mockImplementationOnce(() => new Promise(r => { frei = r }))
    const s = ShareSection.build('b1')
    document.body.appendChild(s.el)
    s.aktualisieren()
    await flush()
    expect(s.el.querySelector('.share-form')).not.toBeNull() /* Rolle da */
    expect(s.el.querySelector('.share-body').getAttribute('aria-busy')).toBe('true')
    frei([{ email: 'a@x.de', role: 'owner' }]); await flush()
    expect(s.el.querySelector('.share-body').getAttribute('aria-busy')).toBe('false')
  })
  it('(b) Benutzer-Einstellungen öffnen löst KEINEN Freigabe-Abruf aus; Board-Einstellungen schon', async () => {
    serverModus()
    EventBus.emit('config:open-board')
    await flush()
    expect(aclListMock).toHaveBeenCalled()
    EventBus.emit('config:close')
    aclListMock.mockClear()
    EventBus.emit('config:open-user')
    await flush()
    expect(aclListMock).not.toHaveBeenCalled()
  })
  it('(c) Inhaltscontainer ist Live-Region, aria-busy während des Ladens, Überschrift mit role/level', async () => {
    const s = ShareSection.build('b1')
    document.body.appendChild(s.el)
    const body = s.el.querySelector('.share-body')
    expect(body.getAttribute('aria-live')).toBe('polite')
    const lauf = s.aktualisieren()
    expect(body.getAttribute('aria-busy')).toBe('true')
    await lauf; await flush()
    expect(body.getAttribute('aria-busy')).toBe('false')
    const h = s.el.querySelector('#cfg-share-heading')
    expect(h.getAttribute('role')).toBe('heading')
    expect(h.getAttribute('aria-level')).toBe('3')
  })
  it('(c) Sofort-Hinweis: sichtbar für Owner und bei unbekannter Rolle, versteckt für Editor/Viewer', async () => {
    const s = ShareSection.build('b1')
    document.body.appendChild(s.el)
    await s.aktualisieren(); await flush()
    expect(s.el.querySelector('.share-sofort').hidden).toBe(false)
    boards = [{ id: 'b1', role: 'viewer' }]
    await s.aktualisieren(); await flush()
    expect(s.el.querySelector('.share-sofort').hidden).toBe(true)
    boards = [{ id: 'b1' }]
    await s.aktualisieren(); await flush()
    expect(s.el.querySelector('.share-sofort').hidden).toBe(false)
  })
})
