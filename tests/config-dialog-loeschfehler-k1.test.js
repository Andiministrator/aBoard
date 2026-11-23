/* @vitest-environment happy-dom */

/**
 * Sprint-62-Kritiker, K1 und V3 — zwei Lücken in der Nacharbeit zu Karte #460
 * bzw. #459, beide über die normale UI erreichbar.
 *
 * **K1 — der Entfernen-Pfad des Hintergrundbilds.** Karte #460 hat
 * `deleteBoardAttachment` beigebracht, einen Fehlschlag zu melden, und
 * `entferneBoardAnhang` wertet ihn aus. `_setBg` verwarf das Ergebnis aber
 * bewusst: „der neue Hintergrund wird trotzdem gesetzt — der Upload ist
 * geglückt". Diese Begründung trägt beim ERSETZEN. Beim ENTFERNEN gibt es
 * keinen Upload, und `board.background = null` lief trotzdem.
 *
 * Der Schaden ist schlimmer als der ursprüngliche Befund F-871: Ohne
 * `board.background` blendet `refreshBgPreview` den Entfernen-Knopf aus und
 * `_setBg` ist über `if (board?.background)` nicht mehr erreichbar — der
 * Nutzer hat keinen zweiten Versuch, während Eintrag UND Datei liegenbleiben.
 *
 * **V3 — Sprach- und Projektwechsel gaben keine Objekt-URLs frei.** Seit
 * Karte #459 ist `closeOverlay` der einzige Ort, an dem revoked wird, und er
 * steigt bei `if (!modal) return;` aus. Die beiden EventBus-Handler setzen
 * `modalUser`/`modalBoard` selbst auf `null` — danach findet `closeOverlay`
 * nichts mehr vor, und jede Vorschau-URL bleibt für die Lebensdauer der Seite
 * hängen.
 *
 * Aufbau und Mocks übernommen aus
 * `tests/config-dialog-partition-scoped-revoke-459.test.js`.
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

/* `deleteBoardAttachment` ist hier die Stellschraube: Der Test setzt pro Fall,
   ob das Löschen glückt (`true`) oder scheitert (`false`). Alles andere
   entspricht der #459-Vorlage. */
const loeschErgebnis = { wert: true }
vi.mock('../src/attachments.js', () => ({
  uploadBoardFile:       vi.fn(async () => ({ id: 'neuAtt' })),
  boardAttachmentUrl:    vi.fn(async (att) => {
    if (att.id === 'bgAtt')     return 'blob:board-hintergrund-url'
    if (att.id === 'avatarAtt') return 'blob:user-avatar-url'
    return null
  }),
  deleteBoardAttachment: vi.fn(async () => loeschErgebnis.wert),
  checkUpload:           vi.fn(() => ({ ok: true })),
  verkleinereBild:       vi.fn(async (f) => f),
}))

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Config }   from '../src/config.js'
import { State }    from '../src/state.js'
import { EventBus } from '../src/eventbus.js'
import { ConfigDialog } from '../src/configdialog.js' // eslint-disable-line no-unused-vars -- registriert die EventBus-Handler

function panelFor(partition) {
  return document.querySelector(`[aria-labelledby="config-dialog-title-${partition}"]`)
}

async function flush() {
  await new Promise(resolve => setTimeout(resolve, 0))
  await new Promise(resolve => setTimeout(resolve, 0))
}

let alertVorher

beforeEach(() => {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
  loeschErgebnis.wert = true
  alertVorher = globalThis.alert
  globalThis.alert = vi.fn() /* alert() würde in happy-dom sonst werfen */
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
  globalThis.alert = alertVorher
  vi.clearAllMocks()
})

describe('K1 — Hintergrund entfernen bei fehlgeschlagenem Löschen', () => {
  it('Löschen scheitert → board.background BLEIBT, der Nutzer behält seinen zweiten Versuch', async () => {
    loeschErgebnis.wert = false
    EventBus.emit('config:open-board')
    await flush()

    const rmBtn = panelFor('board').querySelector('#cfg-bg-remove')
    expect(rmBtn, 'Entfernen-Knopf nicht gefunden — Vorbedingung nicht erfüllt').toBeTruthy()
    expect(rmBtn.style.display, 'der Knopf ist ausgeblendet, obwohl ein Hintergrund gesetzt ist')
      .not.toBe('none')

    rmBtn.click()
    await flush()

    const board = State.get().board
    expect(board.background,
      'board.background wurde trotz fehlgeschlagenem Löschen geleert — der Anhang ist damit '
      + 'unerreichbar (kein Entfernen-Knopf mehr, _setBg nicht mehr aufrufbar), waehrend Eintrag '
      + 'UND Datei liegenbleiben. Genau der Zustand ohne Rueckweg, den F-871 beheben sollte.')
      .toEqual({ attachmentId: 'bgAtt' })
    expect(board.attachments.map(a => a.id),
      'der Eintrag wurde trotz Fehlschlag entfernt').toContain('bgAtt')
    expect(globalThis.alert, 'der Fehlschlag lief stumm durch').toHaveBeenCalled()
  })

  it('Löschen glückt → board.background wird geleert und der Eintrag verschwindet', async () => {
    /* Die Gegenrichtung. Ohne sie wuerde ein Fix, der das Entfernen GENERELL
       verhindert, den Test oben ebenfalls bestehen. */
    loeschErgebnis.wert = true
    EventBus.emit('config:open-board')
    await flush()

    panelFor('board').querySelector('#cfg-bg-remove').click()
    await flush()

    const board = State.get().board
    expect(board.background, 'der Hintergrund wurde bei geglücktem Löschen nicht entfernt').toBeNull()
    expect(board.attachments.map(a => a.id)).not.toContain('bgAtt')
    expect(globalThis.alert, 'es wurde gemeldet, obwohl nichts schiefging').not.toHaveBeenCalled()
  })
})

describe('V3 — Sprach- und Projektwechsel geben die Objekt-URLs frei', () => {
  it('config:language-changed revoked beide Partitionen, bevor es die Instanzen verwirft', async () => {
    EventBus.emit('config:open-user')
    EventBus.emit('config:open-board')
    await flush()

    /* Vorbedingung: beide URLs wurden wirklich vergeben — sonst ist die
       Zusicherung unten vakuum wahr. */
    expect(panelFor('board').querySelector('#cfg-bg-thumb').style.backgroundImage)
      .toContain('blob:board-hintergrund-url')
    expect(panelFor('user').querySelector('.identity-avatar--image')).toBeTruthy()

    const revokeSpy = vi.spyOn(URL, 'revokeObjectURL')
    EventBus.emit('config:language-changed')

    const revoked = revokeSpy.mock.calls.map(c => c[0])
    expect(revoked, 'die Hintergrund-URL leckt beim Sprachwechsel')
      .toContain('blob:board-hintergrund-url')
    expect(revoked, 'die Avatar-URL leckt beim Sprachwechsel')
      .toContain('blob:user-avatar-url')
    revokeSpy.mockRestore()
  })

  it('config:project-changed revoked die board-Partition — und nur die', async () => {
    EventBus.emit('config:open-user')
    EventBus.emit('config:open-board')
    await flush()

    const revokeSpy = vi.spyOn(URL, 'revokeObjectURL')
    EventBus.emit('config:project-changed')

    const revoked = revokeSpy.mock.calls.map(c => c[0])
    expect(revoked, 'die Hintergrund-URL leckt beim Projektwechsel')
      .toContain('blob:board-hintergrund-url')
    expect(revoked,
      'die user-Partition bleibt offen, ihre URL darf NICHT freigegeben werden — '
      + 'sonst bricht das noch sichtbare Avatarbild ab (dieselbe Partitionsschärfe wie F-878)')
      .not.toContain('blob:user-avatar-url')
    revokeSpy.mockRestore()
  })
})
