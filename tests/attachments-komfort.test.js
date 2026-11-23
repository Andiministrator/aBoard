/* @vitest-environment happy-dom */

/**
 * Tests für Attachment-Komfort (Karte #50, ADR-0033):
 * A) Markdown-Resolver: attachment://<id> → <img data-attachment-id>
 * B) Bild-vs-Link-Unterscheidung im Inline-Renderer
 * C) DnD-Overlay-Verhalten (DOM-Tests)
 * D) Paste-Verhalten (DOM-Tests)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

/* ─── Mocks für Modal-Abhängigkeiten (müssen vor Importen stehen) ────── */

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))

let boardModeValue = 'standalone'
vi.mock('../src/config.js', () => ({
  Config: {
    get: vi.fn((key) => {
      if (key === 'boardMode') return boardModeValue
      if (key === 'userName') return 'TestUser'
      return ''
    }),
    set: vi.fn(),
  },
}))

/* uploadFile als spyable Mock — gibt stabiles Attachment-Objekt zurück */
vi.mock('../src/attachments.js', () => ({
  /* Karte #450: Der Upload-Pfad verkleinert Bilder, bevor er prueft. Der Mock
     reicht die Datei unveraendert durch — hier geht es um Drop und Paste,
     nicht um die Verkleinerung selbst (die prueft
     tests/attachments-upload-race.test.js). Fehlt der Eintrag, stirbt der
     ganze Pfad an `Attachments.verkleinereBild is not a function`. */
  verkleinereBild:      vi.fn(async (file) => file),
  checkUpload:          vi.fn(() => ({ ok: true })),
  uploadFile:           vi.fn(async (file) => ({
    id:       '99999999-8888-7777-6666-555555555555',
    filename: file.name || 'datei',
    mime:     file.type || 'application/octet-stream',
    size:     file.size || 0,
    created:  '2026-05-13',
  })),
  readAttachment:       vi.fn(async () => null),
  attachmentUrl:        vi.fn(async () => null),
  downloadAttachment:   vi.fn(async () => true),
  deleteAttachment:     vi.fn(async () => {}),
  purgeCardAttachments: vi.fn(),
  boardSizeLabel:       vi.fn(() => ''),
  humanSize:            vi.fn(() => '0 B'),
  computeBoardBytes:    vi.fn(() => 0),
  /* Karte #450: `updateBoardSizeInfo` rechnet die Anzeige seit dem
     Kritiker-Lauf mit dem base64-Aufschlag, damit sie zur Grenze passt —
     ohne diesen Export stirbt der Modal-Pfad beim Oeffnen. */
  BASE64_FAKTOR:        4 / 3,
  /* F-1088/F-1102: renderAttachmentsList() rendert bei JEDEM Modal-Open
     (ADR-0033, nicht erst beim Tab-Klick) — ohne diesen Export stirbt jeder
     Test hier, der eine Karte mit Anhang öffnet, an `normalisiereAnhang is
     not a function`. Platzhalter hier (`vi.fn()`), die ECHTE Implementierung
     wird unten per `mockImplementation()` nachgereicht — siehe Begründung
     dort: ein ASYNC-Factory-`vi.importActual()` direkt an dieser Stelle
     lieferte in genau diesem Testfile die REALE (ungemockte) `attachments.js`
     an `src/storage-plugin.js` aus, nicht an dieses Testfile — mit realer
     `Config`-Abfrage und daraus resultierendem „Datei zu groß"-Fehlschlag in
     JEDEM Upload-Test (F-1102, Nacharbeit). */
  normalisiereAnhang:   vi.fn(),
  /* F-1107 (Nebenbefund, Sprint-69-Nacharbeit): `src/modal.js` prüft SVG seit
     diesem Fix über `istSvgMime()` statt eines exakten String-Vergleichs —
     ohne diesen Export stirbt jede Vorschau/jeder Cover-Button-Aufbau eines
     Bild-Anhangs hier an `istSvgMime is not a function`. Platzhalter wie bei
     `normalisiereAnhang` oben, echte Implementierung unten nachgereicht. */
  istSvgMime:           vi.fn(),
}))

/* Importe nach vi.mock() */
import { Markdown }    from '../src/markdown.js'
import { EventBus }    from '../src/eventbus.js'
import { Modal }       from '../src/modal.js'
import * as Attachments from '../src/attachments.js'

/* F-1102: `normalisiereAnhang` bekommt die ECHTE Implementierung erst HIER,
   NACHDEM alle Module (inkl. `src/storage-plugin.js`, das `attachments.js`
   ebenfalls importiert) ihre Bindung an das oben gemockte Modul-Objekt fest
   aufgelöst haben. `vi.importActual()` lädt die reale `attachments.js`
   dann als EIGENSTÄNDIGE, separate Modul-Instanz — das berührt die oben
   bereits etablierten Bindungen nicht mehr, nur noch die Implementierung
   dieses einen `vi.fn()`-Platzhalters (von allen Aufrufern geteilt, da
   dieselbe Funktionsreferenz). Ein Mock-Nachbau (`{id:''}` statt `null` für
   `''`) hätte genau den F-1102-Absturz verdeckt — deshalb die echte Funktion,
   kein Nachbau, aber eben zeitlich verschoben statt inline in der Factory. */
const { normalisiereAnhang: echtesNormalisiereAnhang, istSvgMime: echtesIstSvgMime } =
  await vi.importActual('../src/attachments.js')
Attachments.normalisiereAnhang.mockImplementation(echtesNormalisiereAnhang)
/* F-1107: dieselbe Begründung wie oben bei normalisiereAnhang — die ECHTE
   Prüfung, nachgereicht statt inline in der Factory. */
Attachments.istSvgMime.mockImplementation(echtesIstSvgMime)

/* ═══════════════════════════════════════════════════════════════════════
   A) Markdown-Resolver
══════════════════════════════════════════════════════════════════════ */

describe('Markdown.setAttachmentResolver', () => {
  afterEach(() => Markdown.setAttachmentResolver(null))

  it('setAttachmentResolver(fn) / (null) wird ohne Fehler akzeptiert', () => {
    expect(() => Markdown.setAttachmentResolver(() => Promise.resolve(null))).not.toThrow()
    expect(() => Markdown.setAttachmentResolver(null)).not.toThrow()
  })

  it('![katze](attachment://<uuid>) erzeugt <img> mit data-attachment-id', () => {
    const uuid = 'a1b2c3d4-e5f6-7890-abcd-ef1234567890'
    Markdown.setAttachmentResolver(() => Promise.resolve('blob:mock'))
    const container = Markdown.render(`![katze](attachment://${uuid})`)
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img.dataset.attachmentId).toBe(uuid)
  })

  it('![alt](https://example.com/cat.png) wird NICHT als Attachment behandelt', () => {
    const resolver = vi.fn(() => Promise.resolve('blob:mock'))
    Markdown.setAttachmentResolver(resolver)
    const container = Markdown.render('![katze](https://example.com/cat.png)')
    /* Seit ADR-0122 lädt ein fremder Host nicht von selbst — erst der Klick
       macht daraus ein <img>. Die Aussage dieses Tests ist davon unberührt:
       Es ist und bleibt kein Anhang. */
    container.querySelector('a.md-ext-img').click()
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img.dataset.attachmentId).toBeUndefined()
    expect(resolver).not.toHaveBeenCalled()
  })

  it('UUID-Validierung: ungültige ID ruft Resolver NICHT auf', () => {
    const resolver = vi.fn(() => Promise.resolve('blob:mock'))
    Markdown.setAttachmentResolver(resolver)
    const container = Markdown.render('![bild](attachment://keine-gueltige-uuid)')
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    /* Kein Resolver-Aufruf wegen ungültiger UUID */
    expect(resolver).not.toHaveBeenCalled()
  })

  it('Resolver wird mit korrekter UUID aufgerufen', async () => {
    const uuid = '11111111-2222-3333-4444-555555555555'
    const resolver = vi.fn(() => Promise.resolve('data:image/png;base64,abc'))
    Markdown.setAttachmentResolver(resolver)
    Markdown.render(`![bild](attachment://${uuid})`)
    /* Microtask-Queue leeren — Promise.resolve() ist sofort resolved */
    await Promise.resolve()
    expect(resolver).toHaveBeenCalledWith(uuid)
  })

  it('img.src wird nach Resolver-Promise gesetzt', async () => {
    const uuid = 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'
    Markdown.setAttachmentResolver(() => Promise.resolve('data:image/png;base64,TEST'))
    const container = Markdown.render(`![bild](attachment://${uuid})`)
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    /* Zwei Microtask-Ebenen: Promise.resolve().then(url => img.src = url) */
    await Promise.resolve()
    await Promise.resolve()
    expect(img.src).toMatch(/TEST/)
  })

  it('kein Resolver gesetzt → kein Fehler, img ohne src', () => {
    Markdown.setAttachmentResolver(null)
    const uuid = 'ffffffff-eeee-dddd-cccc-bbbbbbbbbbbb'
    expect(() => Markdown.render(`![bild](attachment://${uuid})`)).not.toThrow()
    const container = Markdown.render(`![bild](attachment://${uuid})`)
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    /* Kein src gesetzt — happy-dom liefert leeren String */
    expect(img.getAttribute('src')).toBeNull()
  })
})

/* ═══════════════════════════════════════════════════════════════════════
   B) Bild-vs-Link-Unterscheidung
══════════════════════════════════════════════════════════════════════ */

describe('Inline: Bild ![alt](src) vs. Link [text](url)', () => {
  afterEach(() => Markdown.setAttachmentResolver(null))

  it('[text](url) bleibt <a>, kein <img>', () => {
    const container = Markdown.render('[Beispiel](https://example.com)')
    expect(container.querySelector('a')).not.toBeNull()
    expect(container.querySelector('img')).toBeNull()
  })

  it('![alt](https://...) wird <img>, kein <a>', () => {
    const container = Markdown.render('![logo](https://example.com/logo.png)')
    /* ADR-0122: externes Ziel → erst Platzhalter, nach dem Klick das Bild.
       Der Platzhalter selbst IST seit dem Sprint-61-Kritiker ein `<a>` —
       damit er die Serialisierung in den Druck-/HTML-Export überlebt, wo
       Event-Listener verlorengehen. Er trägt deshalb die Klasse
       `md-ext-img`; die Aussage dieses Tests bezieht sich auf das, was aus
       dem Bild-Markdown am Ende WIRD. */
    expect(container.querySelector('a.md-ext-img')).not.toBeNull()
    container.querySelector('a.md-ext-img').click()
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img.alt).toBe('logo')
    expect(container.querySelector('a'), 'nach dem Laden darf kein Link mehr stehen').toBeNull()
  })

  it('![alt](attachment://<uuid>) wird <img> mit data-attachment-id', () => {
    const uuid = 'cccccccc-dddd-eeee-ffff-000000000000'
    const container = Markdown.render(`![screenshot](attachment://${uuid})`)
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img.alt).toBe('screenshot')
    expect(img.dataset.attachmentId).toBe(uuid)
    expect(container.querySelector('a')).toBeNull()
  })

  it('Gemischt: Link und Bild in einer Zeile', () => {
    const uuid = '12345678-1234-1234-1234-123456789012'
    const container = Markdown.render(`[link](https://a.com) und ![bild](attachment://${uuid})`)
    expect(container.querySelector('a')).not.toBeNull()
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img.alt).toBe('bild')
  })

  it('Leerer alt-Text ist erlaubt', () => {
    const container = Markdown.render('![](https://example.com/img.png)')
    container.querySelector('a.md-ext-img').click()  /* ADR-0122 */
    const img = container.querySelector('img')
    expect(img).not.toBeNull()
    expect(img.alt).toBe('')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
   C+D) DOM-Tests: DnD + Paste (via Modal)
══════════════════════════════════════════════════════════════════════ */

function makeCard(overrides = {}) {
  return {
    id:           'card-dnd-001',
    displayId:    7,
    title:        'DnD-Karte',
    description:  '',
    agentContext: '',
    comments:     [],
    priority:     'mittel',
    labels:       [],
    owner:        '',
    complexity:   '',
    effort:       null,
    qa:           false,
    critical:     false,
    blocked:      false,
    links:        [],
    attachments:  [],
    ...overrides,
  }
}

function openModal(card) {
  EventBus.emit('card:edit', card)
  return document.querySelector('.modal-overlay')
}

function resetModal() {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
}

beforeEach(() => {
  boardModeValue = 'standalone'
  vi.useFakeTimers()
  resetModal()
  Attachments.uploadFile.mockClear()
})

afterEach(() => {
  resetModal()
  Markdown.setAttachmentResolver(null)
  vi.useRealTimers()
})

/* Hilfsfunktion: DragEvent mit gesetztem dataTransfer (happy-dom-kompatibel).
   happy-dom übernimmt dataTransfer nicht aus dem EventInit-Objekt —
   wir setzen es mit Object.defineProperty. */
function makeDragEvent(type, types = ['Files'], files = []) {
  const dt = {
    types,
    files,
    items:      files.map(f => ({ kind: 'file', type: f.type, getAsFile: () => f })),
    dropEffect: '',
  }
  const ev = new Event(type, { bubbles: true, cancelable: true })
  Object.defineProperty(ev, 'dataTransfer', { value: dt, writable: false })
  return ev
}

describe('DnD-Overlay — dragenter/dragover/dragleave/drop', () => {
  it('dragenter mit "Files" in types → .attachment-drop-overlay wird erstellt und angezeigt', () => {
    const ov    = openModal(makeCard())
    const modal = ov.querySelector('.modal')
    expect(modal).not.toBeNull()

    modal.dispatchEvent(makeDragEvent('dragenter', ['Files']))

    const dropOverlay = modal.querySelector('.attachment-drop-overlay')
    expect(dropOverlay).not.toBeNull()
    expect(dropOverlay.style.display).not.toBe('none')
  })

  it('F-2876: Overlay ist aria-hidden ohne role=status; Ansage über die Live-Region, nur einmal je Drag', () => {
    const ov    = openModal(makeCard())
    const modal = ov.querySelector('.modal')
    modal.dispatchEvent(makeDragEvent('dragenter', ['Files']))
    const dropOverlay = modal.querySelector('.attachment-drop-overlay')
    expect(dropOverlay.getAttribute('role')).toBeNull()
    expect(dropOverlay.getAttribute('aria-hidden')).toBe('true')
    const regionen = () => [...document.body.querySelectorAll(':scope > .visually-hidden[role="status"]')]
    expect(regionen().length).toBe(1)
    const text = regionen()[0].textContent
    expect(text.trim()).toBe(dropOverlay.textContent)
    modal.dispatchEvent(makeDragEvent('dragenter', ['Files']))
    expect(regionen()[0].textContent).toBe(text)
  })

  it('dragenter OHNE "Files" → kein Overlay erscheint', () => {
    const ov    = openModal(makeCard())
    const modal = ov.querySelector('.modal')

    modal.dispatchEvent(makeDragEvent('dragenter', ['text/plain']))

    expect(modal.querySelector('.attachment-drop-overlay')).toBeNull()
  })

  it('dragleave nach dragenter → Overlay wird versteckt', () => {
    const ov    = openModal(makeCard())
    const modal = ov.querySelector('.modal')

    modal.dispatchEvent(makeDragEvent('dragenter', ['Files']))
    modal.dispatchEvent(makeDragEvent('dragleave', ['Files']))

    const dropOverlay = modal.querySelector('.attachment-drop-overlay')
    if (dropOverlay) {
      expect(dropOverlay.style.display).toBe('none')
    }
  })

  it('drop ruft uploadFile für jede Datei auf', async () => {
    const ov    = openModal(makeCard())
    const modal = ov.querySelector('.modal')

    const file1 = new File([new Uint8Array(10)], 'a.png', { type: 'image/png' })
    const file2 = new File([new Uint8Array(20)], 'b.png', { type: 'image/png' })

    modal.dispatchEvent(makeDragEvent('drop', ['Files'], [file1, file2]))
    /* Async-Aufrufe abwarten */
    await vi.runAllTimersAsync()

    expect(Attachments.uploadFile).toHaveBeenCalledTimes(2)
  })

  it('drop versteckt das Overlay', async () => {
    const ov    = openModal(makeCard())
    const modal = ov.querySelector('.modal')

    modal.dispatchEvent(makeDragEvent('dragenter', ['Files']))
    modal.dispatchEvent(makeDragEvent('drop',      ['Files']))
    await vi.runAllTimersAsync()

    const dropOverlay = modal.querySelector('.attachment-drop-overlay')
    if (dropOverlay) {
      expect(dropOverlay.style.display).toBe('none')
    }
  })
})

describe('Paste-Verhalten', () => {
  it('Paste mit Image-File ohne aktive Textarea → kein Upload (Fallback)', async () => {
    /* Kein Edit-Mode aktiv (Textarea nicht fokussiert) */
    const ov    = openModal(makeCard())
    const modal = ov.querySelector('.modal')

    const file = new File([new Uint8Array(50)], 'bild.png', { type: 'image/png' })
    const clipData = {
      items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }],
    }
    const ev = new ClipboardEvent('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(ev, 'clipboardData', { value: clipData })

    expect(() => modal.dispatchEvent(ev)).not.toThrow()
    await vi.runAllTimersAsync()

    /* Kein Edit-Mode → kein Upload */
    expect(Attachments.uploadFile).not.toHaveBeenCalled()
  })

  it('Paste mit nur Text (kein Bild) → kein Upload, kein Fehler', async () => {
    const ov    = openModal(makeCard())
    const modal = ov.querySelector('.modal')

    /* Textarea aktivieren */
    const placeholder = ov.querySelector('.typora-placeholder')
    if (placeholder) placeholder.click()
    vi.runAllTimers()

    const clipData = {
      items: [{ kind: 'string', type: 'text/plain' }],
    }
    const ev = new ClipboardEvent('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(ev, 'clipboardData', { value: clipData })

    expect(() => modal.dispatchEvent(ev)).not.toThrow()
    await vi.runAllTimersAsync()

    expect(Attachments.uploadFile).not.toHaveBeenCalled()
  })

  it('Paste mit Image-File bei aktiver Textarea → uploadFile wird aufgerufen', async () => {
    const ov    = openModal(makeCard())
    const modal = ov.querySelector('.modal')

    /* ADR-0076: desc öffnet im Lese-Modus — erst per „Bearbeiten"-Toggle in
       den Edit-Modus, dann ist der Placeholder-Klick-Editor aktiv. */
    const editToggle = ov.querySelector('.desc-edit-toggle')
    expect(editToggle, 'Erwartet: ADR-0076 Bearbeiten-Toggle im desc-Pane').toBeTruthy()
    editToggle.click()
    vi.runAllTimers()

    /* F-135: Textarea zwingend aktivieren — kein Silent-Skip mehr.
       Bei leerer Description ist ein .typora-placeholder vorhanden → Klick öffnet neuen Block. */
    const placeholder = ov.querySelector('.typora-placeholder')
    expect(placeholder, 'Erwartet: typora-placeholder bei leerer Description').toBeTruthy()
    placeholder.click()
    vi.runAllTimers()

    const ta = ov.querySelector('.typora-new, .typora-editing .typora-textarea')
    expect(ta, 'Erwartet: Textarea nach placeholder.click()').toBeTruthy()

    const file = new File([new Uint8Array(100)], 'screenshot.png', { type: 'image/png' })
    const clipData = {
      items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }],
    }
    const ev = new ClipboardEvent('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(ev, 'clipboardData', { value: clipData })

    modal.dispatchEvent(ev)
    await vi.runAllTimersAsync()

    expect(Attachments.uploadFile).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'screenshot.png' }),
      expect.objectContaining({ id: 'card-dnd-001' })
    )
    /* F-829: der eingefügte Verweis zeigt auf das Attachment, das uploadFile
       tatsächlich zurückgegeben hat (fixe Mock-ID) — nicht auf irgendeinen
       Eintrag, der zufällig zuletzt in einer Liste stand. */
    expect(ta.value).toContain('attachment://99999999-8888-7777-6666-555555555555')
  })

  it('F-829: bereits vorhandenes Attachment bleibt unangetastet — Referenz zeigt auf das NEUE', async () => {
    /* Regressionstest zum Kritiker-Befund: Vorher griff der Paste-Pfad nach
       `await triggerUpload(file)` ungeprüft "das letzte Attachment der
       Liste". Card hat hier bereits einen unbeteiligten Alt-Anhang — die
       Referenz darf trotzdem nur auf das frisch erzeugte Attachment zeigen. */
    const altAnhang = {
      id:       'aaaaaaaa-1111-2222-3333-444444444444',
      filename: 'alt-unbeteiligt.png',
      mime:     'image/png',
      size:     10,
      created:  '2026-05-01',
    }
    const ov    = openModal(makeCard({ attachments: [altAnhang] }))
    const modal = ov.querySelector('.modal')

    const editToggle = ov.querySelector('.desc-edit-toggle')
    editToggle.click()
    vi.runAllTimers()
    const placeholder = ov.querySelector('.typora-placeholder')
    placeholder.click()
    vi.runAllTimers()
    const ta = ov.querySelector('.typora-new, .typora-editing .typora-textarea')
    expect(ta).toBeTruthy()

    const file = new File([new Uint8Array(100)], 'neu.png', { type: 'image/png' })
    const clipData = { items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }] }
    const ev = new ClipboardEvent('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(ev, 'clipboardData', { value: clipData })

    modal.dispatchEvent(ev)
    await vi.runAllTimersAsync()

    expect(ta.value).toContain('attachment://99999999-8888-7777-6666-555555555555')
    expect(ta.value).not.toContain(altAnhang.id)
    expect(ta.value).not.toContain('alt-unbeteiligt.png')
  })

  it('F-829: checkUpload lehnt ab → kein Verweis auf ein fremdes Attachment wird eingefügt', async () => {
    /* Kern des Findings: Lehnt checkUpload ab (Pro-Datei-/Hartlimit), legt
       uploadOne KEIN Attachment an. Der Paste-Pfad darf dann nichts in die
       Textarea schreiben — insbesondere nicht den Verweis auf einen bereits
       vorhandenen, unbeteiligten Anhang. */
    globalThis.alert = vi.fn() /* alert() würde in happy-dom sonst werfen */
    const altAnhang = {
      id:       'bbbbbbbb-1111-2222-3333-444444444444',
      filename: 'unbeteiligt.png',
      mime:     'image/png',
      size:     10,
      created:  '2026-05-01',
    }
    const ov    = openModal(makeCard({ attachments: [altAnhang] }))
    const modal = ov.querySelector('.modal')

    const editToggle = ov.querySelector('.desc-edit-toggle')
    editToggle.click()
    vi.runAllTimers()
    const placeholder = ov.querySelector('.typora-placeholder')
    placeholder.click()
    vi.runAllTimers()
    const ta = ov.querySelector('.typora-new, .typora-editing .typora-textarea')
    expect(ta).toBeTruthy()

    Attachments.checkUpload.mockReturnValueOnce({ ok: false, reason: 'zu groß' })

    const file = new File([new Uint8Array(100)], 'zu-gross.png', { type: 'image/png' })
    const clipData = { items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }] }
    const ev = new ClipboardEvent('paste', { bubbles: true, cancelable: true })
    Object.defineProperty(ev, 'clipboardData', { value: clipData })

    modal.dispatchEvent(ev)
    await vi.runAllTimersAsync()

    expect(Attachments.uploadFile).not.toHaveBeenCalled()
    expect(ta.value).not.toContain('attachment://')
    expect(ta.value).not.toContain(altAnhang.id)
    expect(ta.value).not.toContain('unbeteiligt.png')
  })

  it('Paste ohne clipboardData → kein Fehler', async () => {
    const ov    = openModal(makeCard())
    const modal = ov.querySelector('.modal')

    /* Textarea aktivieren */
    const placeholder = ov.querySelector('.typora-placeholder')
    if (placeholder) placeholder.click()
    vi.runAllTimers()

    const ev = new ClipboardEvent('paste', { bubbles: true, cancelable: true })
    /* clipboardData absichtlich nicht setzen */

    expect(() => modal.dispatchEvent(ev)).not.toThrow()
  })
})

/* ═══════════════════════════════════════════════════════════════════════
   E) F-128 — Sicherheit: SVG-Attachments NICHT als img-Thumbnail rendern
══════════════════════════════════════════════════════════════════════ */

describe('Anhang-Preview — SVG-Sicherheit (F-128 / ADR-0033)', () => {
  it('image/svg+xml landet im Icon-Pfad, NICHT als <img>', async () => {
    const svgCard = makeCard({
      attachments: [{
        id:       'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee',
        filename: 'evil.svg',
        mime:     'image/svg+xml',
        size:     128,
        created:  '2026-05-13',
      }],
    })
    const ov = openModal(svgCard)
    /* Anhänge-Tab öffnen, damit renderAttachmentsList läuft */
    ov.querySelector('#tab-attachments').click()
    await vi.runAllTimersAsync()

    const entry = ov.querySelector('.attachment-entry')
    expect(entry).toBeTruthy()
    /* Kein <img.attachment-thumb> für SVG — Icon-Pfad ist svg-Outline */
    expect(entry.querySelector('img.attachment-thumb')).toBeNull()
    /* Stattdessen ein inline-SVG aus mimeIconFor */
    expect(entry.querySelector('.attachment-preview svg')).toBeTruthy()
  })

  it('image/png landet als <img>-Thumbnail (Gegenbeispiel)', async () => {
    const pngCard = makeCard({
      attachments: [{
        id:       'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee',
        filename: 'screenshot.png',
        mime:     'image/png',
        size:     1024,
        created:  '2026-05-13',
      }],
    })
    const ov = openModal(pngCard)
    ov.querySelector('#tab-attachments').click()
    await vi.runAllTimersAsync()

    const entry = ov.querySelector('.attachment-entry')
    expect(entry).toBeTruthy()
    expect(entry.querySelector('img.attachment-thumb')).toBeTruthy()
  })

  /* F-1107 (Nebenbefund der Server-Nacharbeit zu F-855/#538): Der exakte
     String-Vergleich `att.mime !== 'image/svg+xml'` liess Bestandsanhänge mit
     `image/svg` (ohne `+xml`) als normales Bild durch — Vorschau UND
     Cover-Button wurden angeboten, obwohl es sich um SVG handelt. */
  it('F-1107: image/svg (ohne +xml) landet ebenfalls im Icon-Pfad, kein Cover-Button', async () => {
    const svgCard = makeCard({
      attachments: [{
        id:       'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee',
        filename: 'evil.svg',
        mime:     'image/svg',
        size:     128,
        created:  '2026-05-13',
      }],
    })
    const ov = openModal(svgCard)
    ov.querySelector('#tab-attachments').click()
    await vi.runAllTimersAsync()

    const entry = ov.querySelector('.attachment-entry')
    expect(entry).toBeTruthy()
    /* Rot ohne den Fix: `image/svg` besteht den exakten `!== 'image/svg+xml'`-
       Vergleich und würde als <img> gerendert. */
    expect(entry.querySelector('img.attachment-thumb')).toBeNull()
    expect(entry.querySelector('.attachment-preview svg')).toBeTruthy()
    expect(entry.querySelector('.attachment-cover-btn')).toBeNull()
  })
})

/* ═══════════════════════════════════════════════════════════════════════
   F) F-1088 — Mail-Ingest-Altbestand als reiner id-String im Anhänge-Tab
══════════════════════════════════════════════════════════════════════ */

describe('Anhänge-Tab — Mail-Ingest-Altbestand ohne Objekt-Form (F-1088)', () => {
  it('zeigt die id als Name statt „–", Download-Button ist da', async () => {
    const card = makeCard({ attachments: ['legacy-att-1'] })
    const ov = openModal(card)
    ov.querySelector('#tab-attachments').click()
    await vi.runAllTimersAsync()

    const entry = ov.querySelector('.attachment-entry')
    expect(entry).toBeTruthy()
    expect(entry.dataset.attachmentId).toBe('legacy-att-1')
    expect(entry.querySelector('.attachment-name').textContent).toBe('legacy-att-1')
    expect(entry.querySelector('.attachment-name').textContent).not.toBe('–')
    expect(entry.querySelector('.attachment-download-btn')).toBeTruthy()
  })

  it('Download ruft das Backend mit der normalisierten id auf', async () => {
    const card = makeCard({ attachments: ['legacy-att-1'] })
    const ov = openModal(card)
    ov.querySelector('#tab-attachments').click()
    await vi.runAllTimersAsync()

    ov.querySelector('.attachment-download-btn').click()
    await vi.runAllTimersAsync()

    expect(Attachments.downloadAttachment).toHaveBeenCalled()
    const [, attArg] = Attachments.downloadAttachment.mock.calls.at(-1)
    expect(attArg).toEqual({ id: 'legacy-att-1' })
  })

  it('mischt Altbestand (String) mit Neubestand (Objekt) ohne Fehler', async () => {
    const card = makeCard({ attachments: [
      'legacy-att-1',
      { id: 'neu-1', filename: 'bild.png', mime: 'image/png', size: 100, created: '2026-09-01' },
    ] })
    const ov = openModal(card)
    expect(() => { ov.querySelector('#tab-attachments').click() }).not.toThrow()
    await vi.runAllTimersAsync()

    const entries = ov.querySelectorAll('.attachment-entry')
    expect(entries.length).toBe(2)
    const ids = [...entries].map(e => e.dataset.attachmentId)
    expect(ids.sort()).toEqual(['legacy-att-1', 'neu-1'])
  })
})

/* ═══════════════════════════════════════════════════════════════════════
   G) F-1102 — leerer String in card.attachments bricht die Liste nicht ab
══════════════════════════════════════════════════════════════════════ */

describe('Anhänge-Tab — leerer String zwischen gültigen Einträgen (F-1102)', () => {
  it('überspringt den leeren Eintrag, rendert die übrigen zwei ohne Absturz', async () => {
    const card = makeCard({ attachments: [
      '',
      'abc',
      { id: 'x', filename: 'datei.png', mime: 'image/png', size: 10, created: '2026-09-01' },
    ] })
    const ov = openModal(card)
    expect(() => { ov.querySelector('#tab-attachments').click() }).not.toThrow()
    await vi.runAllTimersAsync()

    const entries = ov.querySelectorAll('.attachment-entry')
    expect(entries.length).toBe(2)
    const ids = [...entries].map(e => e.dataset.attachmentId)
    expect(ids.sort()).toEqual(['abc', 'x'])
  })
})
