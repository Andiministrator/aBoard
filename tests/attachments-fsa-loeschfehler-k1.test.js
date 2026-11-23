/* @vitest-environment happy-dom */

/**
 * Sprint-62-Kritiker K1/V1 — die FSA-Hälfte des [[F-871]]-Fixes.
 *
 * **Was der Kritiker gemessen hat:** Baut man `fsaDeleteFile` und
 * `deleteBoardAttachment` in `src/attachments.js` auf den Stand vor dem
 * F-871-Fix zurück, bleiben **alle vier Testkontexte grün**. Gemessen war nur
 * die REST-Hälfte (`tests/board-anhang-loeschfehler-871.test.js`, die
 * ausserdem nur im server-Kontext läuft). Das ausgelieferte
 * Standalone-Distributable benutzt aber genau die ungetestete Hälfte.
 *
 * **Und dort steckte ein echter Restdefekt:** `fsaDeleteFile` meldete Erfolg,
 * sobald `getCardDir` `null` lieferte — mit der Begründung „kein Verzeichnis
 * = war schon weg". `getCardDir` liefert `null` aber auch, wenn gar kein
 * Volume-Handle da ist oder die FSA-Berechtigung entzogen wurde
 * (`NotAllowedError`). Dann blieb die Datei liegen, während der Aufrufer den
 * Eintrag aus `board.attachments` strich — exakt der Zustand, den F-871
 * ausschliessen sollte.
 *
 * Geprüft wird deshalb die Unterscheidung, auf die es ankommt:
 * „nachweislich nicht vorhanden" (Erfolg) gegen „nicht erreichbar"
 * (Fehlschlag).
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'

/* Das FSA-Volume wird über `Sync.getDirectoryHandle()` aufgelöst. */
const volume = { handle: null }
vi.mock('../src/sync.js', () => ({
  Sync: { getDirectoryHandle: () => volume.handle },
}))

/* Der FSA-Zweig hängt an diesem Profil-Merkmal — ohne es liegt der Anhang
   als base64 im Board-State und es gibt gar keine Datei. */
vi.mock('../src/profile.js', () => ({
  Profile: { has: (merkmal) => merkmal === 'feature.fsaVolumeAttachments' },
}))

import { deleteBoardAttachment } from '../src/attachments.js'

/** Fehler mit dem Namen, den die File System Access API wirklich wirft. */
function fsaFehler(name) {
  const e = new Error(name)
  e.name = name
  return e
}

/** Ein Volume, dessen `attachments`-Verzeichnis sich so verhält wie angegeben. */
function volumeMit({ ablage, removeEntry }) {
  const boardDir = {
    removeEntry: removeEntry ?? vi.fn(async () => {}),
  }
  return {
    getDirectoryHandle: async (name) => {
      if (name !== 'attachments') throw fsaFehler('NotFoundError')
      if (ablage instanceof Error) throw ablage
      return {
        getDirectoryHandle: async (id) => {
          if (id !== '__board__') throw fsaFehler('NotFoundError')
          if (ablage === 'fehlt') throw fsaFehler('NotFoundError')
          return boardDir
        },
      }
    },
  }
}

const anhang = { id: 'att-1', filename: 'bg.jpg', mime: 'image/jpeg' }

beforeEach(() => { volume.handle = null })

describe('K1 — fsaDeleteFile trennt „gibt es nicht" von „komme nicht dran"', () => {
  it('Datei gelöscht → true', async () => {
    const removeEntry = vi.fn(async () => {})
    volume.handle = volumeMit({ removeEntry })
    expect(await deleteBoardAttachment(anhang)).toBe(true)
    expect(removeEntry).toHaveBeenCalledOnce()
  })

  it('Datei war schon weg (NotFoundError beim Entfernen) → true, das Ergebnis stimmt ja', async () => {
    volume.handle = volumeMit({ removeEntry: vi.fn(async () => { throw fsaFehler('NotFoundError') }) })
    expect(await deleteBoardAttachment(anhang)).toBe(true)
  })

  it('Ablage-Verzeichnis existiert nicht → true', async () => {
    volume.handle = volumeMit({ ablage: 'fehlt' })
    expect(await deleteBoardAttachment(anhang)).toBe(true)
  })

  it('FSA-Berechtigung entzogen (NotAllowedError) → FALSE, nicht Erfolg', async () => {
    volume.handle = volumeMit({ ablage: fsaFehler('NotAllowedError') })
    expect(await deleteBoardAttachment(anhang),
      'gemeldeter Erfolg bei entzogener Berechtigung — der Aufrufer streicht dann den '
      + 'Eintrag, waehrend die Datei liegenbleibt (genau F-871, nur in der FSA-Haelfte)')
      .toBe(false)
  })

  it('gar kein Volume-Handle → FALSE, wir konnten nicht einmal nachsehen', async () => {
    volume.handle = null
    expect(await deleteBoardAttachment(anhang)).toBe(false)
  })

  it('Löschen scheitert aus einem anderen Grund → false', async () => {
    volume.handle = volumeMit({ removeEntry: vi.fn(async () => { throw fsaFehler('InvalidModificationError') }) })
    expect(await deleteBoardAttachment(anhang)).toBe(false)
  })
})
