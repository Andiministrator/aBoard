/**
 * Karte #825 (ADR-0141 §4, §7): Marken der externen Kopie im Fold und beim
 * Laden.
 *
 *  - `board.field columnDefs` ersetzt die Liste; die Marke `spiegel` einer
 *    Spalte kommt aus dem Bestand, nie aus der Payload.
 *  - `swimlane.upsert` mergt nur Meta-Felder — die Marke einer Lane bleibt, eine
 *    neue Lane bekommt keine aus der Payload.
 *  - `migrateSprintMeta` legt in gespiegelten Lanes keine Meta-Karten an (VF-8).
 *
 * Reine Funktionen, alle vier Kontexte (der Fold läuft im Browser, im
 * Projektor und am Server, ADR-0116).
 *
 * Gegenproben (von Hand, Bericht #825): `spaltenMarkenBewahren` auskommentiert
 * → „Payload setzt keine Marke“ und „Marke bleibt“ rot; die Zeile
 * `if (sprint.spiegel) return;` in migrateSprintMeta entfernt → „keine
 * Meta-Karten“ rot.
 */

import { describe, it, expect } from 'vitest'
import { fold, makeOp } from '../src/ops.js'
import { Storage } from '../src/storage.js'

const K = 'S'.repeat(43)

function basis() {
  return {
    version: '2',
    board: {
      id: 'kopie', title: 'Kopie', nextDisplayId: 1,
      columnDefs: [
        { id: 'col-ready', title: 'Ready', color: null, spiegel: { id: 'col-ready' } },
        { id: 'aussen-review', title: 'Review', color: null, spiegel: { id: K } },
        { id: 'eigene', title: 'Eigene', color: null },
      ],
      sprints: [
        { id: 'aussen-lane', title: 'Sprint 1', kind: 'sprint', cards: [], spiegel: { id: 'L'.repeat(43) } },
        { id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [], spiegel: { id: '__backlog__' } },
      ],
    },
  }
}

describe('#825 Marken an Spalten (board.field columnDefs)', () => {
  it('Marke bleibt, auch wenn die Payload sie weglässt; Titel ändert sich', () => {
    const neu = [
      { id: 'col-ready', title: 'Bereit', color: null },
      { id: 'aussen-review', title: 'Review 2', color: '#fff' },
      { id: 'eigene', title: 'Eigene', color: null },
    ]
    const st = fold(basis(), [makeOp('board.field', { field: 'columnDefs', value: neu }, 'test')])
    const defs = st.board.columnDefs
    expect(defs[0]).toEqual({ id: 'col-ready', title: 'Bereit', color: null, spiegel: { id: 'col-ready' } })
    expect(defs[1].spiegel).toEqual({ id: K })
    expect(defs[1].title).toBe('Review 2')
  })

  it('Payload setzt keine Marke und ändert keine: Bestand gewinnt', () => {
    const neu = [
      { id: 'col-ready', title: 'Ready', color: null, spiegel: { id: 'gefaelscht' } },
      { id: 'eigene', title: 'Eigene', color: null, spiegel: { id: K } },
      { id: 'ganz-neu', title: 'Neu', color: null, spiegel: { id: K } },
    ]
    const st = fold(basis(), [makeOp('board.field', { field: 'columnDefs', value: neu }, 'test')])
    const defs = st.board.columnDefs
    expect(defs[0].spiegel).toEqual({ id: 'col-ready' })
    expect('spiegel' in defs[1]).toBe(false)
    expect('spiegel' in defs[2]).toBe(false)
  })

  it('andere Board-Felder bleiben unberührt (title)', () => {
    const st = fold(basis(), [makeOp('board.field', { field: 'title', value: 'Neu' }, 'test')])
    expect(st.board.title).toBe('Neu')
    expect(st.board.columnDefs[1].spiegel).toEqual({ id: K })
  })
})

describe('#825 Marken an Lanes (swimlane.upsert)', () => {
  it('Upsert auf eine gespiegelte Lane ändert Titel, die Marke bleibt', () => {
    const op = makeOp('swimlane.upsert', { sprint: { id: 'aussen-lane', title: 'Umbenannt', spiegel: { id: 'x' } } }, 'test')
    const st = fold(basis(), [op])
    const lane = st.board.sprints.find(s => s.id === 'aussen-lane')
    expect(lane.title).toBe('Umbenannt')
    expect(lane.spiegel).toEqual({ id: 'L'.repeat(43) })
  })

  it('eine neue Lane bekommt keine Marke aus der Payload', () => {
    const op = makeOp('swimlane.upsert', { sprint: { id: 'neu', title: 'Neu', kind: 'lane', spiegel: { id: K } } }, 'test')
    const st = fold(basis(), [op])
    const lane = st.board.sprints.find(s => s.id === 'neu')
    expect(lane).toBeTruthy()
    expect('spiegel' in lane).toBe(false)
  })
})

describe('#825 migrateSprintMeta auf gespiegelten Lanes (VF-8)', () => {
  it('keine Meta-Karten in einer gespiegelten Sprint-Lane, wohl aber in einer eigenen', () => {
    const st = basis()
    st.board.sprints.splice(1, 0, { id: 'eigene-lane', title: 'Eigener Sprint', kind: 'sprint', cards: [] })
    const erg = Storage.checkVersion(st)
    const gespiegelt = erg.board.sprints.find(s => s.id === 'aussen-lane')
    const eigene = erg.board.sprints.find(s => s.id === 'eigene-lane')
    expect(gespiegelt.cards.filter(c => c.kind === 'sprint-meta')).toHaveLength(0)
    expect(eigene.cards.filter(c => c.kind === 'sprint-meta')).toHaveLength(2)
  })
})
