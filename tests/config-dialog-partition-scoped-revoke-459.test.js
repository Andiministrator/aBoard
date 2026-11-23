/* @vitest-environment happy-dom */

/**
 * Karte #459 (F-878) — `closeOverlay(partition)` gibt beide Objekt-URL-Sätze
 * frei — partitionsscharf revoken.
 *
 * Vorher rief `closeOverlay` unconditional sowohl `_revokeBgThumbUrl()`
 * (gehört zur `background`-Sektion, nur `board`-Partition) als auch
 * `_revokeConfigUrls()` (gehört zur `identities`-Sektion, nur `user`-
 * Partition) auf — unabhängig davon, welche Partition gerade geschlossen
 * wurde. Folgenlos war das nur, weil beide Dialoge modal sind und nie
 * gleichzeitig offen erreichbar waren.
 *
 * Dieser Test konstruiert genau den Fall, der in der laufenden App nicht
 * erreichbar ist: BEIDE Partitionen gleichzeitig offen, BEIDE mit einer
 * gesetzten Objekt-URL (Hintergrund-Vorschau bzw. Identitäten-Avatar) —
 * dann wird eine Partition geschlossen und geprüft, dass NUR deren
 * zugehörige URL freigegeben wird, die der anderen Partition aber steht.
 *
 * Aufbau angelehnt an tests/configdialog-modal-shell-441.test.js (dieselben
 * Mocks, dasselbe Öffnen-über-EventBus-Muster).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

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

/* Sowohl configdialog.js (refreshBgPreview) als auch identity.js (buildAvatar)
   lösen den Anhang-Backend über `StoragePlugin.getActivePlugin()?.attachments?.()
   ?? createFsaAttachmentBackend()` auf. Die Plugin-Mock hier liefert bewusst
   KEINE `.attachments()`-Methode → beide Module fallen auf
   `createFsaAttachmentBackend()` zurück, dessen `boardAttachmentUrl` intern an
   `Attachments.boardAttachmentUrl` delegiert (`src/storage-plugin.js:527`) —
   deshalb muss `createFsaAttachmentBackend` hier real (unmocked) exportiert
   werden, nicht nur `StoragePlugin`. */
vi.mock('../src/storage-plugin.js', async (importOriginal) => {
  const actual = await importOriginal()
  return {
    ...actual,
    StoragePlugin: {
      getActivePlugin: () => ({
        listKnownBoards: vi.fn(async () => []),
      }),
    },
  }
})

/* boardAttachmentUrl liefert je nach Anhang eine EIGENE, unterscheidbare
   blob:-URL — nur so lässt sich nachweisen, welche der beiden URLs
   freigegeben wurde und welche nicht. Alle anderen Methoden sind für diesen
   Test irrelevant (Upload/Delete werden nicht ausgelöst). Die Implementierung
   muss direkt in der Factory stehen (vi.mock wird gehoisted — ein Verweis auf
   eine äussere Konstante wäre zum Zeitpunkt des Hoistings noch nicht initialisiert). */
vi.mock('../src/attachments.js', () => ({
  uploadBoardFile:       vi.fn(async () => ({ id: 'att1' })),
  boardAttachmentUrl:    vi.fn(async (att) => {
    if (att.id === 'bgAtt')     return 'blob:board-hintergrund-url'
    if (att.id === 'avatarAtt') return 'blob:user-avatar-url'
    return null
  }),
  deleteBoardAttachment: vi.fn(async () => {}),
  checkUpload:           vi.fn(() => ({ ok: true })),
}))

vi.mock('../src/swipe.js', () => ({
  addSwipeToDismiss: vi.fn(),
}))

vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Config }   from '../src/config.js'
import { State }    from '../src/state.js'
import { EventBus } from '../src/eventbus.js'
import { ConfigDialog } from '../src/configdialog.js' // eslint-disable-line no-unused-vars -- registriert die EventBus-Handler

function panelFor(partition) {
  return document.querySelector(`[aria-labelledby="config-dialog-title-${partition}"]`)
}
function overlayFor(partition) {
  return panelFor(partition)?.closest('.modal-overlay') ?? null
}

/* Mehrere Makrotask-Runden abwarten, bis die verschachtelten Promise-Ketten
   (Promise.resolve(anhangBackend().boardAttachmentUrl(att)).then(...)) in
   refreshBgPreview() UND buildAvatar() durchgelaufen sind. */
async function flush() {
  await new Promise(resolve => setTimeout(resolve, 0))
  await new Promise(resolve => setTimeout(resolve, 0))
}

beforeEach(() => {
  /* Modul-Singleton zurücksetzen — derselbe Weg wie in #441/config-partition.test.js */
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
  State.set({
    board: {
      id: 'b1', title: 'Test-Board',
      identities: [
        { id: 'user-default', kind: 'user', label: 'Andi', color: '#0B5D73', icon: null,
          imageAttachmentId: 'avatarAtt' },
      ],
      attachments: [
        { id: 'bgAtt',     name: 'bg.jpg',     mime: 'image/jpeg' },
        { id: 'avatarAtt', name: 'avatar.jpg', mime: 'image/jpeg' },
      ],
      background: { attachmentId: 'bgAtt' },
    },
  })
  Config.set('boardMode', 'solo')
})

afterEach(() => {
  EventBus.emit('config:language-changed')
  vi.clearAllMocks()
})

describe('F-878/#459 — closeOverlay revoked partitionsscharf', () => {
  it('Schliessen der board-Partition gibt NUR die Hintergrund-URL frei, die Avatar-URL der offenen user-Partition bleibt stehen', async () => {
    EventBus.emit('config:open-user')
    EventBus.emit('config:open-board')
    await flush()

    /* Vorbedingung: beide Blob-URLs sind tatsächlich gesetzt worden — sonst
       prüft die Zusicherung unten nur einen vakuum wahren Fall. */
    const boardThumb = panelFor('board').querySelector('#cfg-bg-thumb')
    expect(boardThumb.style.backgroundImage, 'Hintergrund-Vorschau wurde nicht gesetzt — Vorbedingung nicht erfüllt')
      .toContain('blob:board-hintergrund-url')
    const userAvatarImg = panelFor('user').querySelector('.identity-avatar--image')
    expect(userAvatarImg, 'Avatar-Bild wurde nicht gesetzt — Vorbedingung nicht erfüllt').toBeTruthy()
    expect(userAvatarImg.style.backgroundImage).toContain('blob:user-avatar-url')

    const revokeSpy = vi.spyOn(URL, 'revokeObjectURL')

    /* Nur die board-Partition schliessen (Close-Button im board-Overlay) */
    overlayFor('board').querySelector('.btn-modal-close').click()

    const revoked = revokeSpy.mock.calls.map(c => c[0])
    expect(revoked, 'die Hintergrund-URL wurde beim Schliessen der board-Partition nicht freigegeben')
      .toContain('blob:board-hintergrund-url')
    expect(revoked, 'F-878: die Avatar-URL der NICHT geschlossenen user-Partition wurde faelschlich mitfreigegeben')
      .not.toContain('blob:user-avatar-url')
  })

  it('Schliessen der user-Partition gibt NUR die Avatar-URL frei, die Hintergrund-URL der offenen board-Partition bleibt stehen', async () => {
    EventBus.emit('config:open-board')
    EventBus.emit('config:open-user')
    await flush()

    const boardThumb = panelFor('board').querySelector('#cfg-bg-thumb')
    expect(boardThumb.style.backgroundImage, 'Vorbedingung nicht erfüllt').toContain('blob:board-hintergrund-url')
    const userAvatarImg = panelFor('user').querySelector('.identity-avatar--image')
    expect(userAvatarImg, 'Vorbedingung nicht erfüllt').toBeTruthy()
    expect(userAvatarImg.style.backgroundImage).toContain('blob:user-avatar-url')

    const revokeSpy = vi.spyOn(URL, 'revokeObjectURL')

    /* Nur die user-Partition schliessen */
    overlayFor('user').querySelector('.btn-modal-close').click()

    const revoked = revokeSpy.mock.calls.map(c => c[0])
    expect(revoked, 'die Avatar-URL wurde beim Schliessen der user-Partition nicht freigegeben')
      .toContain('blob:user-avatar-url')
    expect(revoked, 'F-878: die Hintergrund-URL der NICHT geschlossenen board-Partition wurde faelschlich mitfreigegeben')
      .not.toContain('blob:board-hintergrund-url')
  })
})
