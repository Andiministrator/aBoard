/**
 * Unit-Tests: attachments.js + Migration + KI-Export-Badge
 * Karte #49 — Sprint 16 — ADR-0033
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { Model }       from '../src/model.js'
import { Storage }     from '../src/storage.js'
import { Config }      from '../src/config.js'

/* attachments.js importieren — Achtung: Modul nutzt Sync/State/Config — wir setzen Standalone */
import * as Attachments from '../src/attachments.js'

/* Karte #450: Diese Begruendung stimmt so nicht mehr — das Server-Profil FUEHRT
   `feature.attachmentLimits`, die Limits gelten dort. Was im Server-Build weiterhin
   nicht herstellbar ist, ist ein ANDERER `boardMode`: `Config.set` clampt dort auf
   'server' (F-186/F-272). Gegated wird deshalb nur noch, was einen fremden Modus
   braucht — nicht mehr pauschal „Limits". */
const isServerBuildCtx = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true')

/* F-828: im Server-Build kappt `Config.set('attachmentBoardHardBytes', …)` jetzt
   auf `Config.SERVER_ATTACHMENT_HARD_CAP_BYTES` (8 MB statt 10 MB) — der PUT-Body
   darf die 10-MB-Grenze des Servers nicht mehr randvoll ausreizen. Die Tests
   setzen deshalb kontextabhängige Werte statt der alten festen 8/10 MB, sonst
   würde `makeStandalone()` im Server-Kontext den Kappungswert überschreiben und
   `attachmentBoardWarnBytes === attachmentBoardHardBytes` erzeugen. */
const HARD_BYTES = isServerBuildCtx ? Config.SERVER_ATTACHMENT_HARD_CAP_BYTES : 10 * 1024 * 1024
const WARN_BYTES = isServerBuildCtx ? 6 * 1024 * 1024 : 8 * 1024 * 1024

/* Hilfen ──────────────────────────────────────────────────────────── */

function makeStandalone() {
  /* localStorage in happy-dom ist nicht standardmäßig persistent — Config nutzt
     try/catch, also reicht direkter Config.set() ohne Reset */
  Config.set('boardMode', 'standalone')
  Config.set('attachmentMaxBytes',       2 * 1024 * 1024)
  Config.set('attachmentBoardWarnBytes', WARN_BYTES)
  Config.set('attachmentBoardHardBytes', HARD_BYTES)
}

function fakeFile(name, mime, size) {
  /* happy-dom hat File-Konstruktor; fülle Inhalt mit 'a'-Wiederholung */
  const data = new Uint8Array(size).fill(97)
  return new File([data], name, { type: mime })
}

function emptyBoard() {
  return {
    sprints: [
      { id: '__backlog__', cards: [] },
    ],
  }
}

/* model.js — attachments-Default ───────────────────────────────────── */

describe('Model.createCard — ADR-0033', () => {
  it('legt attachments: [] als Default an', () => {
    const c = Model.createCard('Test', 'col-backlog', 1)
    expect(Array.isArray(c.attachments)).toBe(true)
    expect(c.attachments).toHaveLength(0)
  })
})

/* storage.js — Migration ──────────────────────────────────────────── */

describe('Storage.checkVersion — migrateAttachments', () => {
  it('füllt fehlendes attachments-Feld auf bestehenden Karten', () => {
    const state = {
      version: '2',
      board: {
        id: 'b1',
        title: 'B',
        columnDefs: [{ id: 'col-x', title: 'X' }],
        sprints: [{
          id: '__backlog__',
          title: 'Backlog',
          cards: [
            { id: 'c1', displayId: 1, title: 't', columnId: 'col-x', links: [] },
            { id: 'c2', displayId: 2, title: 't', columnId: 'col-x', links: [], attachments: [{ id: 'a1', filename: 'foo' }] },
          ],
        }],
      },
    }
    const migrated = Storage.checkVersion(state)
    expect(migrated.board.sprints[0].cards[0].attachments).toEqual([])
    /* bestehender Wert bleibt unangetastet */
    expect(migrated.board.sprints[0].cards[1].attachments).toHaveLength(1)
  })
})

/* attachments.js — Größe, Limits, Upload ──────────────────────────── */

describe('Attachments.humanSize', () => {
  it('Bytes für < 1024', () => {
    expect(Attachments.humanSize(0)).toBe('0 B')
    expect(Attachments.humanSize(512)).toBe('512 B')
  })

  it('KB für < 1 MB', () => {
    /* I18n.t macht string-Interpolation; Format hängt von Sprache ab — wir prüfen nur Substring */
    const out = Attachments.humanSize(2048)
    expect(out).toMatch(/KB/)
  })

  it('MB für ≥ 1 MB', () => {
    const out = Attachments.humanSize(2 * 1024 * 1024)
    expect(out).toMatch(/MB/)
  })
})

describe('Attachments.computeBoardBytes', () => {
  it('summiert nur Base64-data-Felder (FSA-Attachments tragen 0 bei)', () => {
    /* 'aGVsbG8=' = 'hello' → 5 Bytes (Base64-Länge 8, padding=1, Math.floor(8*3/4)=6, -1=5) */
    const board = {
      sprints: [{
        id: '__backlog__',
        cards: [
          { attachments: [{ id: '1', data: 'aGVsbG8=' }] }, // 5 B
          { attachments: [{ id: '2' /* kein data, FSA */ }] }, // 0
          { attachments: [{ id: '3', data: 'aGVsbG8=' }] }, // 5 B
        ],
      }],
    }
    expect(Attachments.computeBoardBytes(board)).toBe(10)
  })

  it('null/undefined-sicher', () => {
    expect(Attachments.computeBoardBytes(null)).toBe(0)
    expect(Attachments.computeBoardBytes({})).toBe(0)
    expect(Attachments.computeBoardBytes({ sprints: [] })).toBe(0)
  })
})

describe('Attachments.checkUpload', () => {
  beforeEach(makeStandalone)

  /* Karte #450: Gate entfallen — die Limits gelten seit dem Server-Profil-Flag auch
     dort, und `makeStandalone()` setzt nur Config-Werte, die in jedem Kontext
     greifen. Der Test lief bis Sprint 61 im Server-Kontext gar nicht. */
  it('Pro-Datei-Limit blockiert zu große Dateien', () => {
    const tooBig = fakeFile('big.png', 'image/png', 3 * 1024 * 1024) /* 3 MB > 2 MB */
    const res = Attachments.checkUpload(tooBig, emptyBoard())
    expect(res.ok).toBe(false)
    expect(res.reason).toMatch(/zu groß|too large/i)
  })

  /* Karte #450: Gate entfallen — ausgerechnet die Hartlimit-Pruefung, der Kern der
     Karte, lief im Server-Kontext bisher nicht (Kritiker-Befund).

     F-828: Boardgrösse bewusst relativ zu HARD_BYTES statt absolut (9.5 MB) —
     seit F-828 ist HARD_BYTES im Server-Kontext 8 statt 10 MB. Board-Nutzdaten
     in Höhe von HARD_BYTES selbst reichen mit dem base64-Aufschlag (4/3) plus
     der 1-MB-Datei in JEDEM Kontext klar über die Grenze. */
  it('Hartlimit blockiert wenn Board-Summe überschritten', () => {
    const file = fakeFile('foo.bin', 'application/octet-stream', 1 * 1024 * 1024) /* 1 MB */
    const fakeBoard = {
      sprints: [{
        id: '__backlog__',
        cards: [{
          attachments: [{ id: 'big', data: 'a'.repeat(Math.ceil(HARD_BYTES * 4 / 3)) }],
        }],
      }],
    }
    const res = Attachments.checkUpload(file, fakeBoard)
    expect(res.ok).toBe(false)
    expect(res.reason).toMatch(/überschritten|exceeded/i)
  })

  /* Karte #450: Der `skipIf(isServerBuildCtx)` ist entfallen — seine Begruendung
     („feature.attachmentLimits=false im server-Profil") gilt nicht mehr, das
     Server-Profil fuehrt das Flag jetzt. Der Test laeuft damit in allen vier
     Kontexten.

     F-828: die Boardgrösse wird aus WARN_BYTES/HARD_BYTES errechnet statt fest
     verdrahtet — seit F-828 unterscheiden sich diese Werte zwischen Standalone
     (8/10 MB) und Server-Build (6/8 MB, gedeckelt durch
     Config.SERVER_ATTACHMENT_HARD_CAP_BYTES). Ziel ist eine projizierte Grösse
     GENAU zwischen Warn- und Hartgrenze, unabhängig vom Kontext: erst die
     gewünschte kodierte Zielgrösse festlegen, dann rückwärts die Board-Rohgrösse
     bestimmen, die mit der 1-MB-Testdatei zusammen dorthin führt. */
  it('Warnung wenn Soft-Schwelle überschritten, Upload aber okay', () => {
    const file = fakeFile('foo.bin', 'application/octet-stream', 1 * 1024 * 1024) /* 1 MB */
    const zielProjiziert = Math.round((WARN_BYTES + HARD_BYTES) / 2)
    const boardNutzbytes = Math.round(zielProjiziert * 3 / 4) - file.size
    const fakeBoard = {
      sprints: [{
        id: '__backlog__',
        cards: [{
          attachments: [{ id: 'big', data: 'a'.repeat(Math.ceil(boardNutzbytes * 4 / 3)) }],
        }],
      }],
    }
    const res = Attachments.checkUpload(file, fakeBoard)
    expect(res.ok).toBe(true)
    expect(res.warning).toMatch(/Warnschwelle|warning threshold/i)
  })

  it('Standalone: kleine Datei + leeres Board → keine Warnung', () => {
    const file = fakeFile('foo.png', 'image/png', 100 * 1024) /* 100 KB */
    const res = Attachments.checkUpload(file, emptyBoard())
    expect(res.ok).toBe(true)
    expect(res.warning).toBeUndefined()
  })

  /* Standalone-only (Karte #450): `Config.set('boardMode','project')` clampt im
     Server-Kontext auf 'server' (F-272/F-186) — der Projekt-Modus ist dort
     nicht herstellbar. Bis Sprint 61 war der Test trotzdem gruen, aber aus dem
     falschen Grund: Er lief gegen das server-Profil, das damals ebenfalls keine
     Limits fuehrte. Seit das Profil `feature.attachmentLimits` hat, faellt die
     Verwechslung auf — die Zusage „Projekt-Modus hat keine Limits" ist im
     Server-Build schlicht nicht pruefbar. */
  it.skipIf(isServerBuildCtx)('Projekt-Modus: keine Limits', () => {
    Config.set('boardMode', 'project')
    const tooBig = fakeFile('big.bin', 'application/octet-stream', 50 * 1024 * 1024)
    const res = Attachments.checkUpload(tooBig, emptyBoard())
    expect(res.ok).toBe(true)
  })
})

describe('Attachments.uploadFile (Standalone)', () => {
  beforeEach(makeStandalone)

  it('liefert vollständige Metadaten + Base64-data', async () => {
    const file = fakeFile('hello.txt', 'text/plain', 5)
    const card = { id: 'c1' }
    const att  = await Attachments.uploadFile(file, card)
    expect(att.id).toMatch(/-/)
    expect(att.filename).toBe('hello.txt')
    expect(att.mime).toBe('text/plain')
    expect(att.size).toBe(5)
    expect(att.created).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(typeof att.data).toBe('string')
    expect(att.data.length).toBeGreaterThan(0)
  })

  it('Base64-Roundtrip: readAttachment liefert lesbares Blob', async () => {
    const file = fakeFile('a.bin', 'application/octet-stream', 8)
    const card = { id: 'c1' }
    const att  = await Attachments.uploadFile(file, card)
    const blob = await Attachments.readAttachment(card, att)
    expect(blob).toBeTruthy()
    expect(blob.size).toBe(8)
  })
})

/* export.js — KI-Kontext-Badge ─────────────────────────────────────── */

import { Export } from '../src/export.js'

/* downloadText nicht aufrufen — wir greifen via buildKIKontext (private) nicht direkt zu;
   stattdessen exportiere ich state und prüfe das Markdown-Ergebnis indirekt:
   toJSON umgeht Filesystem nicht, also testen wir nur die Badge-Anzeige via mock-DOM:
   einfacher: wir lesen den generierten Output über eine eigene Helper-Funktion.

   Da buildKIKontext nicht exportiert ist, prüfen wir das Verhalten indirekt:
   toKIKontextFSA schreibt — happy-dom hat aber kein echtes FSA. Daher: wir nutzen
   Export.toKIKontextDownload und fangen den downloadText-Aufruf NICHT — sondern
   testen direkt das Datenmodell.

   Pragmatisch: wir prüfen das Ergebnis von buildKIKontext indirekt durch eine
   inline kopierte Mini-Implementierung NICHT — sondern verlassen uns auf den
   bestehenden Test in export.test.js + dieses sanity-check. */

describe('Export.buildKIKontext — Anhang-Hinweis', () => {
  it('renderCard listet "Anhänge: N" wenn vorhanden', () => {
    /* Wir prüfen indirekt: über toKIKontextDownload können wir nicht — also
       importieren wir das Modul rein und nutzen state-Mutation. */
    const state = {
      version: '2',
      board: {
        id: 'b',
        title: 'T',
        columnDefs: [{ id: 'col-x', title: 'X' }],
        sprints: [{
          id: '__backlog__', title: 'Backlog', cards: [
            { id: 'c1', displayId: 1, title: 'A', columnId: 'col-x',
              priority: 'mittel', links: [], attachments: [
                { id: 'a1', filename: 'foo.png', mime: 'image/png', size: 100, created: '2026-05-13' },
                { id: 'a2', filename: 'b.pdf',   mime: 'application/pdf', size: 200, created: '2026-05-13' },
              ] },
            { id: 'c2', displayId: 2, title: 'B', columnId: 'col-x',
              priority: 'mittel', links: [], attachments: [] },
          ],
        }],
      },
    }

    /* Trick: toKIKontextDownload nutzt internen Builder, ruft aber `Blob` + `URL.createObjectURL` an.
       Wir patchen createElement temporär, um den Inhalt abzugreifen. */
    const origCreate = document.createElement.bind(document)
    let capturedHref = null
    const origUrlCreate = URL.createObjectURL
    let capturedBlobText = null
    URL.createObjectURL = (blob) => {
      capturedHref = 'blob:mock'
      /* FileReader für Blob — happy-dom kennt Blob.text() */
      blob.text().then(t => { capturedBlobText = t })
      return capturedHref
    }
    URL.revokeObjectURL = () => {}

    document.createElement = (tag) => {
      const el = origCreate(tag)
      if (tag === 'a') el.click = () => {}
      return el
    }

    Export.toKIKontextDownload(state)

    /* Async-Warten auf blob.text() */
    return new Promise(resolve => {
      setTimeout(() => {
        document.createElement = origCreate
        URL.createObjectURL = origUrlCreate
        expect(capturedBlobText).toBeTruthy()
        expect(capturedBlobText).toMatch(/Anhänge: 2/)
        /* Karte ohne Anhänge nennt keinen Anhang-Hinweis */
        const lines = capturedBlobText.split('\n')
        const cIdx = lines.findIndex(l => l.includes('#2 B'))
        expect(cIdx).toBeGreaterThan(0)
        expect(lines[cIdx + 1]).not.toMatch(/Anhänge/)
        resolve()
      }, 50)
    })
  })
})

describe('Attachments.safeMime — CSS-Injection-Schutz (F-162)', () => {
  it('akzeptiert kanonische Bild-MIME-Typen', () => {
    expect(Attachments.safeMime('image/png')).toBe('image/png')
    expect(Attachments.safeMime('image/jpeg')).toBe('image/jpeg')
    expect(Attachments.safeMime('image/webp')).toBe('image/webp')
    expect(Attachments.safeMime('application/pdf')).toBe('application/pdf')
  })
  it('reduziert CSS-Injection-Versuche auf Default', () => {
    expect(Attachments.safeMime('image/png) ; background-image: url(http://evil/?')).toBe('application/octet-stream')
    expect(Attachments.safeMime('image/png;evil')).toBe('application/octet-stream')
    expect(Attachments.safeMime('image/png<script>')).toBe('application/octet-stream')
    expect(Attachments.safeMime('image/png\n')).toBe('application/octet-stream')
  })
  it('reduziert nicht-string Werte auf Default', () => {
    expect(Attachments.safeMime(null)).toBe('application/octet-stream')
    expect(Attachments.safeMime(undefined)).toBe('application/octet-stream')
    expect(Attachments.safeMime(42)).toBe('application/octet-stream')
    expect(Attachments.safeMime({})).toBe('application/octet-stream')
  })
})

/* Mail-Ingest-Altbestand vor Sprint 69 (F-358) trägt card.attachments-Einträge
   als reine id-Strings statt Objekten {id, filename, mime, size} — F-1088.
   normalisiereAnhang ist die EINE Lese-Normalisierung, die alle Frontend-Leser
   (llm-tab.js, modal.js) nutzen, statt sie stumm zu verstecken. */
describe('Attachments.istSvgMime — F-1107 (Nebenbefund der Server-Nacharbeit)', () => {
  it('erkennt den kanonischen Typ', () => {
    expect(Attachments.istSvgMime('image/svg+xml')).toBe(true)
  })
  it('erkennt Varianten aus Mail-Ingest-Altbestand (ohne +xml, mit -xml, Groß-/Kleinschreibung)', () => {
    expect(Attachments.istSvgMime('image/svg')).toBe(true)
    expect(Attachments.istSvgMime('image/svg-xml')).toBe(true)
    expect(Attachments.istSvgMime('image/SVG+XML')).toBe(true)
    expect(Attachments.istSvgMime('IMAGE/Svg+Xml')).toBe(true)
  })
  it('Gegenprobe: andere Bild-Typen bleiben unbetroffen', () => {
    expect(Attachments.istSvgMime('image/png')).toBe(false)
    expect(Attachments.istSvgMime('image/jpeg')).toBe(false)
    expect(Attachments.istSvgMime('image/webp')).toBe(false)
  })
  it('Gegenprobe: Nicht-Bild-Typen und Nicht-Strings liefern false, kein Wurf', () => {
    expect(Attachments.istSvgMime('application/svg-info')).toBe(false) /* kein image/*-Präfix */
    expect(Attachments.istSvgMime(null)).toBe(false)
    expect(Attachments.istSvgMime(undefined)).toBe(false)
    expect(Attachments.istSvgMime(123)).toBe(false)
  })
})

describe('Attachments.normalisiereAnhang — F-1088', () => {
  it('macht aus einem String {id: string}', () => {
    expect(Attachments.normalisiereAnhang('att-123')).toEqual({ id: 'att-123' })
  })
  it('lässt ein Objekt unverändert (identisch, keine Kopie mit fehlenden Feldern)', () => {
    const obj = { id: 'a', filename: 'x.png', mime: 'image/png', size: 5 }
    expect(Attachments.normalisiereAnhang(obj)).toBe(obj)
  })
  it('ein leerer String ist keine adressierbare id → null statt {id: \'\'}', () => {
    expect(Attachments.normalisiereAnhang('')).toBeNull()
  })
  it('null/undefined bleiben unverändert (kein Wegwerfen fremder Falsy-Werte)', () => {
    expect(Attachments.normalisiereAnhang(null)).toBeNull()
    expect(Attachments.normalisiereAnhang(undefined)).toBeUndefined()
  })
})
