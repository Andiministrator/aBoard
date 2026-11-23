/* @vitest-environment happy-dom */

/**
 * Nachzügler aus Karte #451 — Findings, die beim Cluster-Zuschnitt zwischen
 * zwei Dateibereiche fielen und deshalb vom Orchestrator nachgezogen wurden.
 *
 * Abdeckung:
 *  - F-137  `safeFilename` ist exportiert und damit überhaupt prüfbar
 *  - F-144  Titel aus reinem Whitespace erzeugt kein leeres Label
 *  - F-807  keine zweite Kopie der Erledigt-Namensliste, kein id-Vergleich im Move-Gate
 *  - F-808  der Einstellungsdialog ist beim Initialfokus schon sichtbar
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeOhneServer } from './helpers/build-flags.js'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName:   vi.fn(() => null),
    getDirectoryHandle: vi.fn(() => null),
    isActive:           vi.fn(() => false),
    isPermissionNeeded: vi.fn(() => false),
  },
}))

vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      listKnownBoards: vi.fn(async () => []),
      aclList:         vi.fn(async () => []),
    }),
  },
}))

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

/* Wird je Test umgesetzt — deshalb über eine Indirektion. */
const bekannteDateien = vi.fn(async () => [])
vi.mock('../src/storage.js', () => ({
  Storage: { listKnownFiles: (...a) => bekannteDateien(...a) },
}))

import { safeFilename }   from '../src/attachments.js'
import { isDoneColumn }   from '../src/links.js'
import { Sidebar }        from '../src/sidebar.js'
import { ConfigDialog }   from '../src/configdialog.js'
import { Config }         from '../src/config.js'
import { State }          from '../src/state.js'
import { EventBus }       from '../src/eventbus.js'

/* Quell-Korpus-Zugriff. Bewusst ueber process.cwd() statt import.meta.url:
   vitest laeuft mit dem Repo-Root als cwd, waehrend Vite import.meta.url auf
   einen root-relativen Pfad umschreibt (`/src/...`) — readFileSync suchte dann
   im Dateisystem-Wurzelverzeichnis. */
const SRC = join(process.cwd(), 'src')
/* Kommentare strippen, BEVOR geprueft wird (Muster aus
   tests/board-background-serverfall.test.js). Ohne das trifft ein Waechter auch
   die Prosa, die seine eigene Behebung dokumentiert — gemessen: ein Kommentar,
   der `DONE_COL_NAMES` erwaehnt, machte diese Datei rot (Kritiker-Befund). */
const quelle = (rel) => readFileSync(join(SRC, rel), 'utf8')
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/^\s*\/\/.*$/gm, '')

beforeEach(() => {
  document.body.innerHTML = ''
  bekannteDateien.mockResolvedValue([])
  State.set({
    board: {
      id: 'b1', title: 'Test-Board', sprints: [], columnDefs: [],
      identities: [{ id: 'user-default', kind: 'user', label: 'Andi', color: '#0B5D73', icon: null }],
    },
  })
  Config.set('boardMode', 'solo')
})

describe('F-137 — safeFilename ist prüfbar', () => {
  it('ist exportiert', () => {
    expect(typeof safeFilename).toBe('function')
  })

  it('neutralisiert Pfadtrenner und Null-Bytes', () => {
    const sicher = safeFilename('be' + String.fromCharCode(0) + 'richt/2026\\q3.pdf')
    expect(sicher).not.toContain('/')
    expect(sicher).not.toContain('\\')
    expect(sicher).not.toContain(String.fromCharCode(0))
  })

  it('entschaerft Windows-reservierte Namen', () => {
    expect(safeFilename('NUL')).not.toBe('NUL')
    expect(safeFilename('com1.txt')).not.toBe('com1.txt')
  })

  it('liefert für einen unbrauchbaren Namen keinen leeren String', () => {
    /* Ein leerer Dateiname erzeugte sonst einen Anhang-Schlüssel, der auf
       `<id>__` endet — im FSA-Volume nicht wiederfindbar. */
    expect(safeFilename('').length).toBeGreaterThan(0)
    expect(safeFilename('///').length).toBeGreaterThan(0)
  })
})

/* F-144 (Whitespace-Titel im Board-Wechsler) und F-833 (aktives Board-Label) prüft seit
   Karte #704 tests/board-menu-704.test.js — der Wechsler wohnt im Menü am Board-Titel. */

describeOhneServer('F-833 — Whitespace-Titel in der Templates-Liste (Board-Dialog)', () => {
  /* ConfigDialog ist ein Modul-Singleton (`buildOverlay` läuft genau einmal,
     Kommentar in configdialog.js) — ohne Reset bliebe der Overlay-Knoten nach
     `document.body.innerHTML = ''` (nächstes beforeEach) im JS zwar referenziert,
     aber vom DOM abgehängt, und ein späterer Test in derselben Datei fände
     `.modal-overlay` nicht mehr. `config:language-changed` ist der vom Modul
     selbst vorgesehene Reset-Weg (baut Overlay + Referenzen komplett neu auf). */
  afterEach(() => { EventBus.emit('config:language-changed') })

  it('Template-Titel aus reinem Whitespace zeigt „(ohne Titel)" statt eines leeren Buttons', () => {
    State.set({
      board: {
        id: 'b1', title: 'Test-Board', sprints: [], columnDefs: [],
        templates: [{ id: 'tpl-1', title: '   ', isTemplate: true }],
        identities: [{ id: 'user-default', kind: 'user', label: 'Andi', color: '#0B5D73', icon: null }],
      },
    })
    ConfigDialog.open()
    const titleBtn = document.querySelector('.config-template-title')
    expect(titleBtn, 'Template-Zeile wurde nicht gerendert').toBeTruthy()
    expect(titleBtn.textContent.trim()).toBe('(ohne Titel)')
  })
})

describe('F-807 — nur eine Quelle für die Erledigt-Erkennung', () => {
  it('main.js hält keine eigene Namensliste mehr', () => {
    const src = quelle('main.js')
    expect(src).not.toMatch(/DONE_COL_NAMES/)
    expect(src).not.toMatch(/'erledigt',\s*'done'/)
  })

  it('das Auto-Entsperr-Gate vergleicht nicht mehr gegen die feste Spalten-id', () => {
    const src = quelle('main.js')
    /* Auf die ANWEISUNG pruefen, nicht auf die Zeichenfolge: der Kommentar an
       der Fundstelle zitiert den alten Ausdruck, um die Aenderung zu begruenden
       — ein Wortlaut-Waechter waere an genau dieser Dokumentation gescheitert. */
    expect(src).not.toMatch(/if\s*\(\s*toColId === 'col-erledigt'\s*\)/)
    /* F-834 (Karte #454): `isDoneColumn(toDefVorher)` — ohne Rückweg bei
       fehlendem columnDef — ist durch `Links.isDoneColumnId(stateBefore?.board,
       toColId)` ersetzt. Eigener Test dafür: siehe Describe-Block „F-834". */
    expect(src).toMatch(/Links\.isDoneColumnId\(/)
  })

  it('isDoneColumn erkennt eine umbenannte Spalte am Titel, nicht an der id', () => {
    expect(isDoneColumn({ id: 'col-eigen-7', title: 'Fertig' })).toBe(true)
    expect(isDoneColumn({ id: 'col-erledigt', title: 'Wartet auf Freigabe' })).toBe(false)
    /* Robust gegen fehlenden Titel — der alte Ausdruck in main.js hätte hier geworfen. */
    expect(isDoneColumn({ id: 'col-x' })).toBe(false)
    expect(isDoneColumn(undefined)).toBe(false)
  })
})

describe('F-834 — Move-Gate in main.js nutzt den Rückweg-sicheren Helfer', () => {
  it('main.js ruft Links.isDoneColumnId statt isDoneColumn(toDefVorher) ohne Fallback auf', () => {
    /* Anweisung statt Wortlaut: der Kommentar an der Fundstelle erwähnt beide
       Ausdrücke, um die Änderung zu begründen (wie F-807 oben) — die Prüfung
       gilt deshalb dem Funktionsaufruf, nicht der blossen Zeichenfolge
       "isDoneColumn". */
    const src = quelle('main.js')
    expect(src).toMatch(/Links\.isDoneColumnId\(stateBefore\?\.board,\s*toColId\)/)
    expect(src).not.toMatch(/isDoneColumn\(toDefVorher\)/)
  })
})

describe('F-808 — Einstellungsdialog ist beim Initialfokus sichtbar', () => {
  it('setzt --open synchron, nicht erst im nächsten Frame', () => {
    ConfigDialog.open()
    const ov = document.querySelector('.modal-overlay')
    expect(ov, 'Overlay wurde nicht gemountet').toBeTruthy()
    /* Kein await, kein rAF-Tick: die Klasse muss unmittelbar nach open() stehen,
       weil der Fokus-Aufruf in derselben synchronen Folge läuft. */
    expect(ov.classList.contains('modal-overlay--open')).toBe(true)
  })

  /* Karte #441 (ADR-0118): Der Bau-Code, den dieser Wächter ursprünglich
     direkt in configdialog.js prüfte (`void ov.offsetHeight` vor dem
     `--open`-Klassenwechsel), ist mit der Migration auf die Modal-Shell nach
     `src/modal-shell.js#open()` gewandert — dort steht dieselbe Zeile
     (`void overlay.offsetHeight`) samt derselben Begründung (F-754/F-756/
     F-808), jetzt einmal für alle acht Dialoge statt hier einzeln. Der
     Wächter bleibt sinnvoll, prüft aber jetzt DELEGATION statt eigenen
     Bau-Codes: configdialog.js darf kein eigenes `requestAnimationFrame`
     fürs Einblenden mehr haben (unverändert wahr) und muss dafür sichtbar
     `createModal` aus der Shell verwenden. */
  it('der Quelltext plant das Einblenden nicht mehr per requestAnimationFrame — Sichtbarkeit läuft über die Modal-Shell', () => {
    const src = quelle('configdialog.js')
    expect(src).not.toMatch(/requestAnimationFrame\(/)
    expect(src).toMatch(/from ['"]\.\/modal-shell\.js['"]/)
    expect(src).toMatch(/createModal\(/)
  })
})
