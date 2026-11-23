/* @vitest-environment happy-dom */

/**
 * Tests für den Dokument-Viewer (Karte #274)
 *
 * Abdeckung:
 *  - readDocFile: gibt null zurück wenn kein FSA-Handle vorhanden
 *  - readDocFile: gibt null zurück wenn Datei nicht gefunden
 *  - readDocFile: gibt Dateiinhalt zurück wenn verfügbar
 *  - openDocViewer: erzeugt Overlay-DOM und öffnet ihn
 *  - openDocViewer: rendert Markdown via bestehenden Renderer (kein innerHTML)
 *  - openDocViewer: zeigt Fallback wenn Datei nicht gefunden
 *  - closeDocViewer: entfernt open-Klasse
 *  - buildDocEntriesSection: erzeugt Abschnitt mit README-Button
 *
 * Hinweis: IS_PROJECTOR_BUILD=true in beiden Test-Kontexten (vitest.config.js /
 * vitest.config.server.js, ADR-0072 Test-Invariante) — der Code ist immer aktiv.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

/* Mock: Sync.getDirectoryHandle — im Test steuerbar */
vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName:   vi.fn(() => null),
    getDirectoryHandle: vi.fn(() => null),
  },
}))

vi.mock('../src/storage.js', () => ({
  Storage: {
    listKnownFiles: vi.fn(async () => []),
  },
}))

vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      listKnownBoards: vi.fn(async () => []),
      aclList: vi.fn(async () => []),
    }),
  },
}))

import { Sync }              from '../src/sync.js'
import { __testingDocs__ }  from '../src/sidebar.js'
import { describeProjektor } from './helpers/build-flags.js'

/* ADR-0116: `__testingDocs__` ist im Nicht-Projektor-Build `null` (der
   Doc-Viewer wird dort per DCE entfernt). Ohne `?? {}` stirbt schon das
   Modul-Laden und die Datei fällt als Suite-Fehler aus, statt sauber zu
   skippen. */
const { readDocFile, openDocViewer, closeDocViewer, buildDocEntriesSection } = __testingDocs__ ?? {}

beforeEach(() => {
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

afterEach(() => {
  closeDocViewer?.()
})

/* ── readDocFile ────────────────────────────────────────────────────── */

describeProjektor('readDocFile — kein Handle', () => {
  it('gibt null zurück wenn getDirectoryHandle() null liefert', async () => {
    Sync.getDirectoryHandle.mockReturnValue(null)
    const result = await readDocFile('README.md')
    expect(result).toBeNull()
  })
})

describeProjektor('readDocFile — Handle vorhanden', () => {
  it('gibt null zurück wenn getFileHandle wirft (Datei nicht vorhanden)', async () => {
    const mockHandle = {
      getFileHandle: vi.fn().mockRejectedValue(new Error('NotFoundError')),
    }
    Sync.getDirectoryHandle.mockReturnValue(mockHandle)
    const result = await readDocFile('README.md')
    expect(result).toBeNull()
  })

  it('gibt den Dateiinhalt zurück wenn die Datei lesbar ist', async () => {
    const inhalt = '# Test\n\nHallo Welt'
    const mockHandle = {
      getFileHandle: vi.fn().mockResolvedValue({
        getFile: vi.fn().mockResolvedValue({
          text: vi.fn().mockResolvedValue(inhalt),
        }),
      }),
    }
    Sync.getDirectoryHandle.mockReturnValue(mockHandle)
    const result = await readDocFile('README.md')
    expect(result).toBe(inhalt)
  })
})

/* ── openDocViewer ──────────────────────────────────────────────────── */

describeProjektor('openDocViewer — Fallback (Datei nicht gefunden)', () => {
  it('erzeugt das Overlay-DOM und zeigt Fallback-Meldung', async () => {
    Sync.getDirectoryHandle.mockReturnValue(null) /* readDocFile → null */
    await openDocViewer({ titleKey: 'doc.readme', filename: 'README.md' })
    const overlay = document.querySelector('.doc-viewer-overlay')
    expect(overlay).toBeTruthy()
    expect(overlay.getAttribute('aria-hidden')).toBe('false')
    /* Fallback-Meldung sichtbar */
    const fallback = document.querySelector('.doc-viewer-empty')
    expect(fallback).toBeTruthy()
  })
})

describeProjektor('openDocViewer — Markdown-Rendering', () => {
  it('rendert Dateiinhalt als .md-content (bestehender Renderer, kein innerHTML)', async () => {
    const inhalt = '# Überschrift\n\nAbsatz'
    const mockHandle = {
      getFileHandle: vi.fn().mockResolvedValue({
        getFile: vi.fn().mockResolvedValue({
          text: vi.fn().mockResolvedValue(inhalt),
        }),
      }),
    }
    Sync.getDirectoryHandle.mockReturnValue(mockHandle)
    await openDocViewer({ titleKey: 'doc.readme', filename: 'README.md' })
    const body = document.querySelector('.doc-viewer-body')
    expect(body).toBeTruthy()
    /* Markdown.render erzeugt .md-content */
    const mdContent = body.querySelector('.md-content')
    expect(mdContent).toBeTruthy()
    /* Kein innerHTML-Direktzugriff — Inhalt durch DOM-Elemente repräsentiert */
    const h1 = mdContent.querySelector('h1')
    expect(h1).toBeTruthy()
    expect(h1.textContent).toBe('Überschrift')
  })

  it('Titel im Overlay-Header entspricht dem i18n-Key', async () => {
    Sync.getDirectoryHandle.mockReturnValue(null)
    await openDocViewer({ titleKey: 'doc.readme', filename: 'README.md' })
    const title = document.querySelector('.doc-viewer-title')
    expect(title).toBeTruthy()
    /* 'doc.readme' → 'README' (DE) oder 'README' (EN) */
    expect(title.textContent).toBe('README')
  })
})

/* ── closeDocViewer ─────────────────────────────────────────────────── */

describeProjektor('closeDocViewer', () => {
  it('entfernt --open-Klasse vom Overlay', async () => {
    Sync.getDirectoryHandle.mockReturnValue(null)
    await openDocViewer({ titleKey: 'doc.readme', filename: 'README.md' })
    const overlay = document.querySelector('.doc-viewer-overlay')
    expect(overlay.classList.contains('doc-viewer-overlay--open')).toBe(true)
    closeDocViewer()
    expect(overlay.classList.contains('doc-viewer-overlay--open')).toBe(false)
    expect(overlay.getAttribute('aria-hidden')).toBe('true')
  })
})

/* ── buildDocEntriesSection ─────────────────────────────────────────── */

describeProjektor('buildDocEntriesSection', () => {
  it('erzeugt eine Sektion mit mindestens einem Button-Eintrag', () => {
    const section = buildDocEntriesSection()
    expect(section.classList.contains('app-sidebar-section')).toBe(true)
    const rows = section.querySelectorAll('.app-sidebar-row')
    expect(rows.length).toBeGreaterThanOrEqual(1)
  })

  it('README-Button-Label entspricht dem i18n-Key "doc.readme"', () => {
    const section = buildDocEntriesSection()
    const label = section.querySelector('.app-sidebar-row-label')
    expect(label).toBeTruthy()
    expect(label.textContent).toBe('README')
  })
})

/* F-3109 (Karte #1186): Der Leser zeigte rohes HTML als Text — auch die Abzugs-Marken
   (`<!-- abzug:… -->`, ADR-0152) samt Inhalt der `abzug:nur`-Blöcke. */
describe('F-3109 — HTML-Kommentare im Dokumentationsleser', () => {
  it('blendet Marken und nur-Blöcke aus, lässt Code-Zäune und den übrigen Text stehen', async () => {
    const { ohneHtmlKommentare } = await import('../src/sidebar.js')
    const text = [
      'Vorher',
      '<!-- abzug:aus -->',
      'Intern',
      '<!-- abzug:ein -->',
      '<!-- abzug:nur',
      'Nur öffentlich',
      '-->',
      'Mitten <!-- still --> im Satz',
      '```',
      '<!-- im Code bleibt -->',
      '```',
      'Nachher',
    ].join('\n')
    expect(ohneHtmlKommentare(text)).toBe([
      'Vorher', 'Intern', 'Mitten  im Satz', '```', '<!-- im Code bleibt -->', '```', 'Nachher',
    ].join('\n'))
  })
})
