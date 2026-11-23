/* @vitest-environment happy-dom */

/**
 * Karte #449 (F-795): Ein Feld geht beim Aktualisieren verloren.
 *
 * Der Vorfall (Konsumenten-Projekt, 2026-08-28): Eine Swimlane war
 * per Op archiviert worden. Ein Reload schrieb kurz darauf eine
 * `swimlane.upsert` mit `archived:false` — obwohl niemand dearchiviert hatte.
 * Der Wert war ein Nebenprodukt: `swimlaneMeta` sendete `archived` mit,
 * sobald das Feld DEFINIERT war, und die Migration definiert es beim Laden
 * auf jeder Swimlane.
 *
 * Diese Tests halten den gemessenen Mechanismus fest — alle drei Fälle sind
 * vor dem Fix real durchgespielt worden, Fall 3 hat den Datenverlust
 * reproduziert.
 *
 * GOTCHA, an dem beim Nachmessen zweimal jemand gescheitert ist:
 * `diffToOps` erwartet `{version, board}`, NICHT das Board direkt — bei
 * falschem Wrapper liefert es stumm `[]`, und die Fehlmessung sieht aus wie
 * eine Bestätigung. Dasselbe gilt für `fold`.
 */

import { describe, it, expect } from 'vitest'
import { diffToOps } from '../src/diff.js'
import { fold } from '../src/ops.js'

const wrap = (sprints) => ({
  version: '2',
  board: { id: 'b', title: 'B', columnDefs: [{ id: 'col-backlog', title: 'Backlog' }], sprints },
})
const lane = (extra) => ({ id: 's1', title: 'Alt', kind: 'lane', cards: [], ...extra })

describe('swimlane.upsert trägt `archived` nur bei echter Änderung (Karte #449, F-795)', () => {
  it('Titeländerung allein schreibt den migrations-gesetzten Default NICHT fest', () => {
    /* Der Kern des Vorfalls: `prev` kennt das Feld nicht (Stand vor der
       ADR-0117-Migration), `next` trägt es auf dem Default — beides
       derselbe Zustand. Die Op darf davon nichts erzählen. */
    const prev = wrap([lane()])
    const next = wrap([lane({ title: 'Neu', archived: false })])

    const ops = diffToOps(prev, next, 'tester')

    expect(ops).toHaveLength(1)
    expect(ops[0].type).toBe('swimlane.upsert')
    expect(ops[0].payload.sprint.title).toBe('Neu')
    expect('archived' in ops[0].payload.sprint).toBe(false)
  })

  it('genau diese Op nimmt eine parallel gefaltete Archivierung nicht mehr zurück', () => {
    /* Fall 3 der Messung — der eigentliche Datenverlust. Die base trägt
       `archived:true` (eine andere Session hat archiviert); die Op oben wird
       darauf gefaltet. Vor dem Fix stand danach `false`. */
    const prev = wrap([lane()])
    const next = wrap([lane({ title: 'Neu', archived: false })])
    const ops = diffToOps(prev, next, 'tester')

    const base = wrap([lane({ archived: true })])
    const proj = fold(base, ops)

    expect(proj.board.sprints[0].archived).toBe(true)
    expect(proj.board.sprints[0].title).toBe('Neu')   /* die gemeinte Änderung wirkt */
  })

  it('eine ECHTE Dearchivierung wird weiterhin übertragen', () => {
    /* Gegenstück: Ohne diesen Test wäre auch ein `swimlaneMeta` korrekt, das
       `archived` nie mehr mitsendet — und Dearchivieren ginge verloren. */
    const prev = wrap([lane({ archived: true })])
    const next = wrap([lane({ archived: false })])

    const ops = diffToOps(prev, next, 'tester')

    expect(ops).toHaveLength(1)
    expect(ops[0].payload.sprint.archived).toBe(false)
    expect(fold(wrap([lane({ archived: true })]), ops).board.sprints[0].archived).toBe(false)
  })

  it('eine echte Archivierung ebenso', () => {
    const prev = wrap([lane()])
    const next = wrap([lane({ archived: true })])

    const ops = diffToOps(prev, next, 'tester')

    expect(ops).toHaveLength(1)
    expect(ops[0].payload.sprint.archived).toBe(true)
    expect(fold(prev, ops).board.sprints[0].archived).toBe(true)
  })

  it('eine NEUE Swimlane trägt ihren Archiv-Zustand mit', () => {
    /* Der `!old`-Zweig bleibt unverändert: hier gibt es keinen Vorzustand,
       gegen den sich „geändert" bestimmen liesse. */
    const prev = wrap([])
    const next = wrap([lane({ archived: true })])

    const ops = diffToOps(prev, next, 'tester')

    expect(ops.some(o => o.type === 'swimlane.upsert' && o.payload.sprint.archived === true)).toBe(true)
  })

  it('der Round-Trip ist für dieses Feld SEMANTISCH, nicht byte-genau — und war es schon vorher', () => {
    /* Diese Zusicherung dokumentiert eine Grenze, keine Eigenschaft. Sie ist
       der Beleg dafür, dass das Invarianz-Argument im alten Kommentar nicht
       trug: Schon ohne jede Änderung ausser dem definierten Default entsteht
       KEINE Op, und der gefaltete Stand ist byte-ungleich zu `next`. Wer den
       Round-Trip hier byte-genau haben will, müsste die Migration rückgängig
       machen, nicht den Diff ändern. */
    const prev = wrap([lane()])
    const next = wrap([lane({ archived: false })])

    const ops = diffToOps(prev, next, 'tester')
    const proj = fold(prev, ops)

    expect(ops).toEqual([])
    expect('archived' in proj.board.sprints[0]).toBe(false)
    expect(proj.board.sprints[0].archived).toBeUndefined()
    /* semantisch identisch — genau die Normalisierung, die diff.js und die
       Migration ohnehin anwenden */
    expect(!!proj.board.sprints[0].archived).toBe(!!next.board.sprints[0].archived)
  })
})

/* ─────────────────────────────────────────────────────────────────────
 * Brute-Force-Round-Trip für Swimlane-Metafelder (Karte #449)
 *
 * ADR-0079 hat diese Absicherung für die KARTEN-Seite etabliert; die
 * Swimlane-Meta-Seite hatte sie nicht. Der Brute-Force-Test in
 * `tests/diff.test.js` vergleicht über `colView` — eine rein
 * karten-orientierte Sicht, die `title`, `kind` und `archived` gar nicht
 * ansieht. Der Generator dort erzeugt auch nie ein `archived`. Ein Bruch in
 * `swimlaneMeta` konnte deshalb an beiden Brute-Force-Läufen vorbeigehen,
 * und genau das ist er auch.
 *
 * Kanonisiert wird `archived` über `!!` — `undefined` und `false` sind
 * derselbe Zustand (Punkt 1 des Kommentars in `src/diff.js`, und die
 * Migration setzt das Feld beim Laden ohnehin). Der Round-Trip ist für
 * dieses Feld semantisch, nicht byte-genau; siehe die Begründung im Test
 * darüber.
 * ───────────────────────────────────────────────────────────────────── */

describe('ADR-0079-Niveau — Brute-Force-Round-Trip über Swimlane-Metafelder (Karte #449)', () => {
  function mulberry32(a) {
    return function () {
      a |= 0; a = (a + 0x6D2B79F5) | 0
      let t = Math.imul(a ^ (a >>> 15), 1 | a)
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296
    }
  }

  /* Kanonische Sicht auf die Metafelder — mit !!-Normalisierung für archived. */
  const metaView = (st) => st.board.sprints.map(s => ({
    id: s.id, title: s.title, kind: s.kind, archived: !!s.archived,
  }))

  const KINDS = ['sprint', 'lane']

  function genPrev(rnd) {
    const n = 1 + Math.floor(rnd() * 4)
    const sprints = []
    for (let i = 0; i < n; i++) {
      const sp = { id: 's' + i, title: 'T' + i, kind: KINDS[Math.floor(rnd() * 2)], cards: [] }
      /* In etwa der Hälfte der Fälle das Feld GAR NICHT setzen — das ist der
         Zustand eines noch nicht migrierten Boards und die Quelle des
         Vorfalls. */
      if (rnd() < 0.5) sp.archived = rnd() < 0.4
      sprints.push(sp)
    }
    sprints.push({ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] })
    return { version: '2', board: { id: 'b', title: 'B', columnDefs: [{ id: 'col-backlog' }], sprints } }
  }

  function genNext(prev, rnd) {
    const next = JSON.parse(JSON.stringify(prev))
    for (const sp of next.board.sprints) {
      if (sp.id === '__backlog__') continue
      if (rnd() < 0.35) sp.title = sp.title + '!'
      if (rnd() < 0.25) sp.kind = KINDS[Math.floor(rnd() * 2)]
      if (rnd() < 0.35) sp.archived = rnd() < 0.5
      /* Und der Fall, um den es geht: die Migration ergänzt das Feld auf dem
         Default, ohne dass jemand etwas geändert hat. */
      if (sp.archived === undefined && rnd() < 0.6) sp.archived = false
    }
    return next
  }

  it('2000 zufällige Meta-Szenarien: fold(prev, diff) ≡ next (archived kanonisiert)', () => {
    for (let iter = 0; iter < 2000; iter++) {
      const rnd = mulberry32(0x5bd1e995 ^ iter)
      const prev = genPrev(rnd)
      const next = genNext(prev, rnd)
      const ops = diffToOps(prev, next, 'bf')
      const proj = fold(prev, ops)

      const got = metaView(proj), want = metaView(next)
      if (JSON.stringify(got) !== JSON.stringify(want)) {
        throw new Error(`Round-Trip-Bruch (Swimlane-Meta) iter=${iter}`
          + `\nprev=${JSON.stringify(metaView(prev))}`
          + `\nwant=${JSON.stringify(want)}`
          + `\n got=${JSON.stringify(got)}`
          + `\n ops=${JSON.stringify(ops.map(o => o.payload))}`)
      }
    }
  })

  it('keine Op behauptet etwas über archived, das sich nicht geändert hat', () => {
    /* Die eigentliche Zusicherung dieser Karte, über den ganzen Zufallsraum:
       Trägt eine Op das Feld, muss der normalisierte Wert sich von prev
       unterscheiden. Ohne diese Prüfung wäre der Round-Trip oben auch von
       der alten, huckepack sendenden Fassung erfüllt. */
    for (let iter = 0; iter < 2000; iter++) {
      const rnd = mulberry32(0xc2b2ae35 ^ iter)
      const prev = genPrev(rnd)
      const next = genNext(prev, rnd)
      const vorher = new Map(prev.board.sprints.map(s => [s.id, !!s.archived]))

      for (const op of diffToOps(prev, next, 'bf')) {
        if (op.type !== 'swimlane.upsert') continue
        const sp = op.payload.sprint
        if (!('archived' in sp)) continue
        if (!vorher.has(sp.id)) continue          /* neue Swimlane: erlaubt */
        expect(sp.archived).not.toBe(vorher.get(sp.id))
      }
    }
  })
})
