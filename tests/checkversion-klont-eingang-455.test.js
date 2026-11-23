/* @vitest-environment happy-dom */

/**
 * F-842 (Karte #455, Sprint 62): `Storage.checkVersion` mutierte seinen
 * Eingabe-Zustand in place — ohne dass Signatur oder Kommentar das sagten.
 *
 * Der Schaden ist nicht theoretisch: An genau dieser Eigenschaft ist F-814
 * monatelang unentdeckt geblieben. Der Diff-Bezug des Projektors und der
 * Stand, den die Migration bearbeitete, waren dasselbe Objekt — die
 * Sprint-Auto-Aktivierung wirkte im Speicher und erreichte die Platte nie.
 *
 * Die Tests prüfen BEIDE Richtungen: der Eingang bleibt unangetastet, UND
 * die Migration greift trotzdem auf dem Rückgabewert. Nur zusammen sagen
 * sie etwas — eine Funktion, die gar nichts mehr migriert, erfüllte die
 * erste Zusicherung ebenfalls.
 */

import { describe, it, expect } from 'vitest'
import { Storage } from '../src/storage.js'

/* Ein v2-Board, dem `columnDefs` fehlt: `migrateDefaultColumns` füllt es.
   Bewusst diese Migration als Sonde — sie schreibt auf dem BOARD-Objekt,
   nicht auf einer Karte, und ist damit auch dann sichtbar, wenn ein Board
   gar keine Karten hat. */
function boardOhneColumnDefs() {
  return {
    version: '2',
    board: {
      id: 'test-board',
      title: 'Testboard',
      sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] }],
    },
  }
}

describe('Storage.checkVersion lässt den Eingabe-Zustand unangetastet (F-842)', () => {
  it('der übergebene Zustand ist nach dem Aufruf byte-identisch zu vorher', () => {
    const eingang = boardOhneColumnDefs()
    const vorher = JSON.stringify(eingang)

    Storage.checkVersion(eingang)

    expect(JSON.stringify(eingang)).toBe(vorher)
  })

  it('die Migration greift trotzdem — auf dem Rückgabewert', () => {
    /* Gegenstück zur Zusicherung oben: ohne diesen Test wäre auch eine
       `checkVersion`, die einfach nichts mehr tut, „korrekt". */
    const eingang = boardOhneColumnDefs()

    const ergebnis = Storage.checkVersion(eingang)

    expect(Array.isArray(ergebnis.board.columnDefs)).toBe(true)
    expect(ergebnis.board.columnDefs.length).toBeGreaterThan(0)
    /* Und der Eingang hat sie eben NICHT bekommen. */
    expect(eingang.board.columnDefs).toBeUndefined()
  })

  it('Rückgabewert und Eingang sind verschiedene Objekte — auch in der Tiefe', () => {
    /* Ein flacher Klon würde die erste Zusicherung teilweise erfüllen und
       trotzdem verschachtelte Migrationen (Karten, Sprints) durchschlagen
       lassen. Deshalb bis auf die Sprint-Ebene geprüft. */
    const eingang = boardOhneColumnDefs()

    const ergebnis = Storage.checkVersion(eingang)

    expect(ergebnis).not.toBe(eingang)
    expect(ergebnis.board).not.toBe(eingang.board)
    expect(ergebnis.board.sprints).not.toBe(eingang.board.sprints)
    expect(ergebnis.board.sprints[0]).not.toBe(eingang.board.sprints[0])
  })

  it('null und undefined bleiben unverändert durchgereicht', () => {
    expect(Storage.checkVersion(null)).toBe(null)
    expect(Storage.checkVersion(undefined)).toBe(undefined)
  })
})
