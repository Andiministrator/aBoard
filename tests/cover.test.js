/* @vitest-environment happy-dom */

/**
 * Tests für Karten-Cover-Bild (Karte #62, umgebaut in Karte #173 zum
 * abgedunkelten Karten-Hintergrund)
 *
 * Abdeckung:
 * - .card--has-cover-Klasse wird gesetzt wenn coverAttachmentId + Bild-Anhang
 * - Keine Cover-Klasse bei null coverAttachmentId
 * - Keine Cover-Klasse bei dangling ID (Anhang existiert nicht)
 * - SVG-Anhänge werden nicht als Cover angeboten (F-128)
 * - Migration: coverAttachmentId wird auf jeder Karte sichergestellt
 * - Migration: dangling IDs werden auf null gesetzt
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// ── Mocks ────────────────────────────────────────────────────────────────

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName: vi.fn(() => null),
    listKnownBoards:  vi.fn(async () => []),
  },
}))

vi.mock('../src/storage.js', () => ({
  Storage: {
    listKnownFiles: vi.fn(async () => []),
  },
}))

vi.mock('../src/dragdrop.js', () => ({
  DragDrop: {
    takePendingFlip: vi.fn(() => null),
    init:            vi.fn(),
  },
}))

vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

/* attachmentUrl wird gemockt — im Test keine echten Blob-URLs nötig */
vi.mock('../src/attachments.js', async () => ({
  attachmentUrl: vi.fn(async (_card, att) => {
    if (att?.data) return `data:${att.mime};base64,${att.data}`;
    return null;
  }),
  computeBoardBytes: vi.fn(() => 0),
  humanSize:         vi.fn(() => '–'),
  /* Karte #467: Der Renderer importiert `thumbnailUrl` direkt aus
     attachments.js (das Thumbnail liegt im Board-State, es braucht kein
     Backend).
     Kritiker-Befund Sprint 63: Hier stand eine NACHGEBAUTE Kurzfassung, die
     die MIME-Prüfung wegliess — die Renderer-Tests wären grün geblieben, wenn
     die Produktion sie verloren hätte. Genau die F-478-Klasse, auf die sich
     dieser Sprint an drei anderen Stellen beruft, um Spiegel-Tests zu
     vermeiden. Jetzt wird die ECHTE Funktion durchgereicht; gemockt bleibt nur
     der Rest des Moduls, den der Renderer sonst mitzöge. */
  thumbnailUrl: (await vi.importActual('../src/attachments.js')).thumbnailUrl,
  /* F-1107 (Nebenbefund, Sprint-69-Nacharbeit): dieselbe Begründung wie bei
     thumbnailUrl oben — die ECHTE SVG-Prüfung, kein Nachbau. */
  istSvgMime: (await vi.importActual('../src/attachments.js')).istSvgMime,
}))

/* Karte #450: Der Renderer geht seither über `StoragePlugin.getActivePlugin()
   .attachments()`; ohne diesen Mock griffe der `attachments.js`-Mock oben im
   server-Kontext nicht (dort liefert das Plugin das REST-Backend). Begründung
   im Helfer. */
vi.mock('../src/storage-plugin.js', () =>
  import('./helpers/attachment-backend-mock.js').then(m => m.storagePluginMock()))

import { Filter }        from '../src/filter.js'
import { State }         from '../src/state.js'
import { Renderer }      from '../src/renderer.js'
import { attachmentUrl } from '../src/attachments.js' /* gemockt — s.o. */

// ── Hilfsfunktionen ──────────────────────────────────────────────────────

let _testCounter = 0
function nextId() { return `t${++_testCounter}` }

function makePngAttachment(overrides = {}) {
  return {
    id:       `att-${nextId()}`,
    filename: 'bild.png',
    mime:     'image/png',
    size:     1024,
    created:  '2026-05-13',
    data:     'abc123', /* Stub-Base64 */
    ...overrides,
  }
}

function makeCard(overrides = {}) {
  return {
    id:               `card-${Math.random().toString(36).slice(2)}`,
    displayId:        1,
    title:            'Test-Karte',
    description:      '',
    labels:           [],
    owner:            '',
    priority:         'mittel',
    complexity:       '',
    effort:           null,
    qa:               false,
    critical:         false,
    blocked:          false,
    columnId:         'col-todo',
    attachments:      [],
    coverAttachmentId: null,
    links:            [],
    ...overrides,
  }
}

function makeBoard(cards = [], colDefs = null) {
  const sid = `sprint-${nextId()}`
  const cid = `col-${nextId()}`
  const usedColDefs = colDefs ?? [{ id: cid, title: 'Backlog' }]
  const adjustedCards = cards.map(c => ({ ...c, columnId: usedColDefs[0]?.id ?? cid }))
  return {
    id:         'board-test',
    title:      'Test-Board',
    columnDefs: usedColDefs,
    sprints:    [{ id: sid, title: 'Sprint 1', cards: adjustedCards }],
  }
}

function setupBoardDOM(board) {
  const header = document.createElement('div')
  header.id = 'board-header'
  const container = document.createElement('div')
  container.id = 'board-columns'
  document.body.appendChild(header)
  document.body.appendChild(container)
  Renderer.renderBoard({ board })
  return container
}

function renderCardViaBoard(card) {
  const board = makeBoard([card])
  return setupBoardDOM(board)
}

// ── Setup / Teardown ─────────────────────────────────────────────────────

beforeEach(() => {
  document.body.innerHTML = ''
  Filter.clear()
  State.markClean()
})

afterEach(() => {
  document.body.innerHTML = ''
  Filter.clear()
  vi.clearAllMocks()
})

// ─────────────────────────────────────────────────────────────────────────
// 1) Cover-Rendering
// ─────────────────────────────────────────────────────────────────────────

describe('Karten-Cover — Rendering (Karte #173: als Hintergrund)', () => {
  it('keine card--has-cover-Klasse wenn coverAttachmentId null', () => {
    const card = makeCard({ coverAttachmentId: null, attachments: [] })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.classList.contains('card--has-cover')).toBe(false)
  })

  it('keine card--has-cover-Klasse bei dangling coverAttachmentId', () => {
    const card = makeCard({
      coverAttachmentId: 'nicht-existent',
      attachments: [],
    })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.classList.contains('card--has-cover')).toBe(false)
  })

  it('card--has-cover-Klasse gesetzt bei gültigem Bild-Anhang', () => {
    const att = makePngAttachment()
    const card = makeCard({
      coverAttachmentId: att.id,
      attachments: [att],
    })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.classList.contains('card--has-cover')).toBe(true)
  })

  it('kein separates .card-cover oder <img class="card-cover-img"> mehr (#173)', () => {
    const att = makePngAttachment()
    const card = makeCard({
      coverAttachmentId: att.id,
      attachments: [att],
    })
    const container = renderCardViaBoard(card)
    expect(container.querySelector('.card-cover')).toBeNull()
    expect(container.querySelector('.card-cover-img')).toBeNull()
  })

  it('SVG-Anhang setzt keine card--has-cover-Klasse (F-128)', () => {
    const svgAtt = makePngAttachment({ mime: 'image/svg+xml', filename: 'bild.svg' })
    const card = makeCard({
      coverAttachmentId: svgAtt.id,
      attachments: [svgAtt],
    })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.classList.contains('card--has-cover')).toBe(false)
  })

  /* F-1107 (Nebenbefund der Server-Nacharbeit zu F-855/#538): der exakte
     String-Vergleich `!== 'image/svg+xml'` liess `image/svg` (ohne `+xml`)
     als normales Cover-Bild durch. */
  it('F-1107: image/svg (ohne +xml) setzt ebenfalls keine card--has-cover-Klasse', () => {
    const svgAtt = makePngAttachment({ mime: 'image/svg', filename: 'bild.svg' })
    const card = makeCard({
      coverAttachmentId: svgAtt.id,
      attachments: [svgAtt],
    })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    /* Rot ohne den Fix: `image/svg` besteht den exakten Vergleich, die Karte
       bekäme card--has-cover. */
    expect(cardEl.classList.contains('card--has-cover')).toBe(false)
  })

  it('Nicht-Bild-Anhang (PDF) setzt keine card--has-cover-Klasse', () => {
    const pdfAtt = makePngAttachment({ mime: 'application/pdf', filename: 'dok.pdf', data: 'pdfdata' })
    const card = makeCard({
      coverAttachmentId: pdfAtt.id,
      attachments: [pdfAtt],
    })
    const container = renderCardViaBoard(card)
    const cardEl = container.querySelector('.card')
    expect(cardEl.classList.contains('card--has-cover')).toBe(false)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 1b) Cover-Async-Race-Absicherung (F-238)
// ─────────────────────────────────────────────────────────────────────────

describe('Karten-Cover — Async-Race-Absicherung (F-238)', () => {
  /* happy-dom kennt revokeObjectURL evtl. nicht — defensiv sicherstellen */
  if (typeof URL.revokeObjectURL !== 'function') URL.revokeObjectURL = () => {}

  it('stale Render-Generation: blob:-URL wird revoked, altes Element bleibt ohne backgroundImage', async () => {
    const att  = makePngAttachment()
    const card = makeCard({ coverAttachmentId: att.id, attachments: [att] })

    let resolveUrl
    attachmentUrl.mockImplementationOnce(() => new Promise(r => { resolveUrl = r }))
    const revokeSpy = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})

    /* Generation N — Cover-Promise bleibt absichtlich pending */
    const board = makeBoard([card])
    setupBoardDOM(board)
    const elN = document.querySelector('.card')

    /* Generation N+1 — Re-Render bumpt _renderGeneration, elN wird abgehängt */
    Renderer.renderBoard({ board })
    expect(elN.isConnected).toBe(false)

    /* Jetzt löst die veraltete Cover-Promise auf */
    resolveUrl('blob:stale-cover')
    await Promise.resolve(); await Promise.resolve()

    /* Leak-Schutz: die nutzlose blob:-URL wurde freigegeben */
    expect(revokeSpy).toHaveBeenCalledWith('blob:stale-cover')
    /* Das abgehängte Element bekam nie ein Hintergrundbild */
    expect(elN.style.backgroundImage).toBe('')

    revokeSpy.mockRestore()
  })

  it('aktuelle Generation: blob:-URL wird als backgroundImage gesetzt', async () => {
    const att  = makePngAttachment()
    const card = makeCard({ coverAttachmentId: att.id, attachments: [att] })

    attachmentUrl.mockResolvedValueOnce('blob:fresh-cover')

    const board = makeBoard([card])
    setupBoardDOM(board)
    await Promise.resolve(); await Promise.resolve()

    const cardEl = document.querySelector('.card')
    expect(cardEl.style.backgroundImage).toContain('blob:fresh-cover')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// 2) Cover-Migration
// ─────────────────────────────────────────────────────────────────────────

describe('Karten-Cover — Schema-Erwartungen (NICHT die Migration)', () => {
  /* Achtung, Abgrenzung (F-165, Karte #376): Dieser Block hiess bis Sprint 52
     „Migration (migrateCover)" und behauptete damit eine Abdeckung, die er nie
     hatte. `../src/storage.js` ist in dieser Datei gemockt — die Migration
     laeuft hier gar nicht. Die Tests unten bauen die Dangling-Pruefung im Test
     NACH; sie waeren auch dann gruen geblieben, wenn `migrateCover` ersatzlos
     entfernt worden waere.

     Was sie tatsaechlich sichern, ist das Karten-Schema (Feld vorhanden,
     Default null) — dafuer bleiben sie stehen. Die echten Migrations-Tests
     stehen in `tests/core.test.js` unter „Storage.checkVersion". */

  it('coverAttachmentId-Feld bleibt null wenn nicht gesetzt', () => {
    const card = makeCard({ coverAttachmentId: null })
    expect(card.coverAttachmentId).toBe(null)
  })

  it('coverAttachmentId ist im Karten-Schema vorhanden', () => {
    const card = makeCard()
    expect('coverAttachmentId' in card).toBe(true)
  })

  it('Karte mit gültigem coverAttachmentId behält diesen', () => {
    const att = makePngAttachment()
    const card = makeCard({
      coverAttachmentId: att.id,
      attachments: [att],
    })
    /* Dangling-Prüfung: Anhang existiert → ID bleibt */
    const attExists = card.attachments.some(a => a.id === card.coverAttachmentId)
    expect(attExists).toBe(true)
  })

  it('Karte mit dangling coverAttachmentId hat keinen passenden Anhang', () => {
    const card = makeCard({
      coverAttachmentId: 'existiert-nicht',
      attachments: [],
    })
    const attExists = Array.isArray(card.attachments) &&
      card.attachments.some(a => a.id === card.coverAttachmentId)
    expect(attExists).toBe(false)
  })
})

// ─────────────────────────────────────────────────────────────────────────
// Karte #467 — das Cover-Thumbnail wird SYNCHRON gezeichnet
// ─────────────────────────────────────────────────────────────────────────

describe('Cover-Thumbnail (Karte #467)', () => {
  /* Die Beobachtung aus dem Betrieb war eine Wartezeit, kein fehlendes Bild. Ein Test, der
     nur zusichert, dass irgendwann ein Hintergrundbild dasteht, wäre deshalb
     auch ohne den Fix grün — der asynchrone Weg tut das seit Karte #173.
     Zugesichert wird hier, dass das Bild OHNE jedes `await` steht UND dass
     das Original gar nicht erst angefordert wurde. */
  it('zeichnet das Cover ohne await und ohne Backend-Aufruf, wenn ein Thumbnail vorliegt', () => {
    attachmentUrl.mockClear()
    const att = makePngAttachment({ thumb: 'QUJD', thumbMime: 'image/webp' })
    const container = renderCardViaBoard(makeCard({
      coverAttachmentId: att.id, attachments: [att],
    }))
    const cardEl = container.querySelector('.card--has-cover')

    expect(cardEl, 'die Cover-Klasse muss stehen').not.toBeNull()
    /* SYNCHRON: kein Tick abgewartet, kein Promise aufgelöst. */
    expect(cardEl.style.backgroundImage).toContain('data:image/webp;base64,QUJD')
    /* Und das Original wurde nicht zusätzlich geholt — genau die Ersparnis,
       um die es geht (im Server-Modus ein HTTP-Request pro Cover). */
    expect(attachmentUrl, 'das Original darf nicht angefordert werden').not.toHaveBeenCalled()
  })

  /* Bestandskarten haben kein Thumbnail. Der bisherige Weg muss vollständig
     erhalten bleiben — ein fehlendes Thumbnail ist ein normaler Zustand und
     darf nicht wie ein kaputtes Bild aussehen. */
  it('nimmt ohne Thumbnail weiterhin den bisherigen asynchronen Weg', async () => {
    attachmentUrl.mockClear()
    const att = makePngAttachment({ data: 'QUJD' })
    const container = renderCardViaBoard(makeCard({
      coverAttachmentId: att.id, attachments: [att],
    }))
    const cardEl = container.querySelector('.card--has-cover')

    expect(cardEl).not.toBeNull()
    /* Vor dem Auflösen ist noch nichts gesetzt — das IST die Verzögerung,
       die das Thumbnail oben beseitigt. */
    expect(cardEl.style.backgroundImage).toBe('')
    expect(attachmentUrl).toHaveBeenCalledTimes(1)

    await Promise.resolve(); await Promise.resolve()
    expect(cardEl.style.backgroundImage).toContain('base64,QUJD')
  })

  /* Ein leeres `thumb`-Feld ist kein Thumbnail. Ohne diesen Test würde eine
     Fassung durchgehen, die auf `'thumb' in att` prüft und dann eine
     kaputte data-URL („data:image/jpeg;base64,") ins CSS schreibt. */
  it('behandelt ein leeres thumb-Feld wie gar keins', async () => {
    attachmentUrl.mockClear()
    const att = makePngAttachment({ thumb: '', data: 'QUJD' })
    const container = renderCardViaBoard(makeCard({
      coverAttachmentId: att.id, attachments: [att],
    }))
    expect(attachmentUrl).toHaveBeenCalledTimes(1)
    await Promise.resolve(); await Promise.resolve()
    expect(container.querySelector('.card--has-cover').style.backgroundImage)
      .toContain('base64,QUJD')
  })
})

// ─────────────────────────────────────────────────────────────────────────
// Kritiker-Nachlese Sprint 63 — Thumbnail-Hygiene im Renderer
// ─────────────────────────────────────────────────────────────────────────

describe('Cover-Thumbnail: kaputte Nutzlast macht die Karte nicht unlesbar', () => {
  /* `.card--has-cover` setzt weisse Schrift und eine Mindesthöhe. Steht die
     Klasse ohne Bild, ist die Karte im hellen Theme unlesbar — CSS-Hintergründe
     haben kein `onerror`. Der asynchrone Pfad fängt das seit der #450-Nacharbeit
     ab; der synchrone muss es vorher erkennen. */
  it('fällt bei unbrauchbarer thumb-Nutzlast auf den asynchronen Weg zurück', async () => {
    attachmentUrl.mockClear()
    const att = makePngAttachment({ thumb: 'kein base64!!', thumbMime: 'image/webp', data: 'QUJD' })
    const container = renderCardViaBoard(makeCard({
      coverAttachmentId: att.id, attachments: [att],
    }))
    /* Nicht das kaputte Thumbnail gezeichnet … */
    expect(container.querySelector('.card--has-cover').style.backgroundImage).toBe('')
    /* … sondern der bisherige Weg genommen. */
    expect(attachmentUrl).toHaveBeenCalledTimes(1)
    await Promise.resolve(); await Promise.resolve()
    expect(container.querySelector('.card--has-cover').style.backgroundImage).toContain('base64,QUJD')
  })

  it('fällt bei einem unzulässigen thumbMime (SVG) ebenfalls zurück', async () => {
    attachmentUrl.mockClear()
    const att = makePngAttachment({ thumb: 'QUJD', thumbMime: 'image/svg+xml', data: 'QUJD' })
    renderCardViaBoard(makeCard({ coverAttachmentId: att.id, attachments: [att] }))
    expect(attachmentUrl).toHaveBeenCalledTimes(1)
  })
})
