/* @vitest-environment happy-dom */
/**
 * Unit-Tests für Workflow-Effekte der Karten-Verweise (ADR-0032, Karte #54)
 *
 * Geprüft werden:
 *  - isLinkBlocked() — computed linkBlocked auf Basis offener blocked-by-Ziele
 *  - openBlockers()  — Liste offener Blocker-Karten
 *  - checkMoveWarning() — Verschiebe-Warnung-Logik (pure Funktion, leicht testbar)
 *  - detectUnlockedCards() — Auto-Entsperren-Erkennung
 *  - Sprint-Löschen → purgeLinksTo wird für alle Sprint-Karten aufgerufen
 *    (Kein-Backlog-Pfad — nur bei wirklichem Löschen)
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  addLink,
  purgeLinksTo,
  isLinkBlocked,
  openBlockers,
  openDependencies,
  checkMoveWarning,
  detectUnlockedCards,
} from '../src/links.js'
import { Model } from '../src/model.js'

/* ─────────────────────────────────────────────────────────────────
   Hilfsfunktionen
   ───────────────────────────────────────────────────────────────── */

function makeBoard() {
  return {
    id:            'board-wf-test',
    title:         'Workflow-Test',
    nextDisplayId: 1,
    columnDefs: [
      { id: 'col-backlog',    title: 'Backlog' },
      { id: 'col-ready',      title: 'Ready' },
      { id: 'col-in-arbeit',  title: 'In Arbeit' },
      { id: 'col-erledigt',   title: 'Erledigt' },
    ],
    sprints: [],
  }
}

function makeCard(title, columnId = 'col-backlog', displayId = null) {
  const c = Model.createCard(title, columnId, displayId)
  return c
}

function makeSprint(id, title, cards = []) {
  return { id, title, cards }
}

/* ─────────────────────────────────────────────────────────────────
   isLinkBlocked — computed Badge-Logik
   ───────────────────────────────────────────────────────────────── */

describe('isLinkBlocked()', () => {
  let board, cardA, cardB, cardC

  beforeEach(() => {
    board = makeBoard()
    cardA = makeCard('A', 'col-backlog', 1)
    cardB = makeCard('B', 'col-backlog', 2)   /* Blocker, noch offen */
    cardC = makeCard('C', 'col-erledigt', 3)  /* Blocker, aber erledigt */
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB, cardC])]
  })

  it('gibt true zurück wenn mindestens ein blocked-by-Ziel nicht in col-erledigt', () => {
    addLink(board, cardA.id, cardB.id, 'blocked-by')
    expect(isLinkBlocked(board, cardA)).toBe(true)
  })

  it('gibt false zurück wenn das blocked-by-Ziel in col-erledigt liegt', () => {
    addLink(board, cardA.id, cardC.id, 'blocked-by')
    expect(isLinkBlocked(board, cardA)).toBe(false)
  })

  it('gibt false zurück wenn alle blocked-by-Ziele erledigt sind', () => {
    addLink(board, cardA.id, cardC.id, 'blocked-by')
    expect(isLinkBlocked(board, cardA)).toBe(false)
  })

  it('gibt false zurück wenn keine Links vorhanden sind', () => {
    expect(isLinkBlocked(board, cardA)).toBe(false)
  })

  it('gibt false zurück bei blocks-Link (nur blocked-by ist relevant)', () => {
    addLink(board, cardA.id, cardB.id, 'blocks') /* A blockiert B, nicht umgekehrt */
    expect(isLinkBlocked(board, cardA)).toBe(false)
  })

  it('gibt false zurück bei related-Link (nicht workflow-relevant)', () => {
    addLink(board, cardA.id, cardB.id, 'related')
    expect(isLinkBlocked(board, cardA)).toBe(false)
  })

  it('gibt true zurück wenn mindestens EIN Blocker offen (gemischt erledigt/offen)', () => {
    addLink(board, cardA.id, cardB.id, 'blocked-by') /* B offen */
    addLink(board, cardA.id, cardC.id, 'blocked-by') /* C erledigt */
    expect(isLinkBlocked(board, cardA)).toBe(true)
  })

  it('ignoriert verwaiste Verweise (Ziel existiert nicht)', () => {
    /* Link auf nicht-existente Karte direkt setzen */
    cardA.links = [{ targetId: 'nonexistent-uuid', type: 'blocked-by', created: '2026-05-13' }]
    expect(isLinkBlocked(board, cardA)).toBe(false)
  })
})

/* ─────────────────────────────────────────────────────────────────
   openBlockers — Liste offener Blocker
   ───────────────────────────────────────────────────────────────── */

describe('openBlockers()', () => {
  it('gibt Karten zurück die als blocked-by-Ziel nicht erledigt sind', () => {
    const board = makeBoard()
    const cardA = makeCard('A', 'col-backlog', 1)
    const cardB = makeCard('B', 'col-backlog', 2) /* offen */
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB])]
    addLink(board, cardA.id, cardB.id, 'blocked-by')
    const blockers = openBlockers(board, cardA)
    expect(blockers.length).toBe(1)
    expect(blockers[0].id).toBe(cardB.id)
  })

  it('gibt leeres Array zurück wenn keine Links', () => {
    const board = makeBoard()
    const cardA = makeCard('A', 'col-backlog', 1)
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA])]
    expect(openBlockers(board, cardA)).toEqual([])
  })

  it('filtert erledigte Blocker heraus', () => {
    const board = makeBoard()
    const cardA = makeCard('A', 'col-backlog', 1)
    const cardB = makeCard('B', 'col-erledigt', 2)
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB])]
    addLink(board, cardA.id, cardB.id, 'blocked-by')
    expect(openBlockers(board, cardA)).toEqual([])
  })
})

/* ─────────────────────────────────────────────────────────────────
   checkMoveWarning — Verschiebe-Warnung-Logik
   ───────────────────────────────────────────────────────────────── */

describe('checkMoveWarning()', () => {
  let board, cardA, cardB, cardC

  beforeEach(() => {
    board = makeBoard()
    cardA = makeCard('A', 'col-backlog', 1)    /* zu verschiebende Karte */
    cardB = makeCard('B', 'col-backlog', 2)    /* offener Blocker */
    cardC = makeCard('C', 'col-erledigt', 3)   /* erledigter Blocker */
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB, cardC])]
  })

  /* blocked-by: warnt bei col-in-arbeit UND col-erledigt */

  it('blocked-by → warnt bei Verschiebung nach col-in-arbeit', () => {
    addLink(board, cardA.id, cardB.id, 'blocked-by')
    const result = checkMoveWarning(board, cardA, 'col-in-arbeit')
    expect(result.warn).toBe(true)
    expect(result.type).toBe('blocked-by')
    expect(result.blockers.length).toBe(1)
  })

  it('blocked-by → warnt bei Verschiebung nach col-erledigt', () => {
    addLink(board, cardA.id, cardB.id, 'blocked-by')
    const result = checkMoveWarning(board, cardA, 'col-erledigt')
    expect(result.warn).toBe(true)
    expect(result.type).toBe('blocked-by')
  })

  it('blocked-by → KEINE Warnung bei Verschiebung nach col-ready', () => {
    addLink(board, cardA.id, cardB.id, 'blocked-by')
    const result = checkMoveWarning(board, cardA, 'col-ready')
    expect(result.warn).toBe(false)
  })

  it('blocked-by → KEINE Warnung wenn alle Blocker erledigt', () => {
    addLink(board, cardA.id, cardC.id, 'blocked-by') /* C ist erledigt */
    const result = checkMoveWarning(board, cardA, 'col-in-arbeit')
    expect(result.warn).toBe(false)
  })

  /* depends-on: warnt NUR bei col-erledigt, nicht bei col-in-arbeit */

  it('depends-on → KEINE Warnung bei Verschiebung nach col-in-arbeit', () => {
    addLink(board, cardA.id, cardB.id, 'depends-on')
    const result = checkMoveWarning(board, cardA, 'col-in-arbeit')
    expect(result.warn).toBe(false)
  })

  it('depends-on → warnt bei Verschiebung nach col-erledigt', () => {
    addLink(board, cardA.id, cardB.id, 'depends-on')
    const result = checkMoveWarning(board, cardA, 'col-erledigt')
    expect(result.warn).toBe(true)
    expect(result.type).toBe('depends-on')
  })

  it('depends-on → KEINE Warnung wenn Abhängigkeit erledigt', () => {
    addLink(board, cardA.id, cardC.id, 'depends-on') /* C ist erledigt */
    const result = checkMoveWarning(board, cardA, 'col-erledigt')
    expect(result.warn).toBe(false)
  })

  /* Kein Link → keine Warnung */

  it('keine Links → keine Warnung', () => {
    const result = checkMoveWarning(board, cardA, 'col-in-arbeit')
    expect(result.warn).toBe(false)
  })

  it('related-Links warnen nie', () => {
    addLink(board, cardA.id, cardB.id, 'related')
    expect(checkMoveWarning(board, cardA, 'col-in-arbeit').warn).toBe(false)
    expect(checkMoveWarning(board, cardA, 'col-erledigt').warn).toBe(false)
  })

  it('blocks-Links warnen nie (A blockiert B, nicht A wartet auf B)', () => {
    addLink(board, cardA.id, cardB.id, 'blocks')
    expect(checkMoveWarning(board, cardA, 'col-in-arbeit').warn).toBe(false)
    expect(checkMoveWarning(board, cardA, 'col-erledigt').warn).toBe(false)
  })

  it('gibt die offenen Blocker als Array in result.blockers zurück', () => {
    addLink(board, cardA.id, cardB.id, 'blocked-by')
    const result = checkMoveWarning(board, cardA, 'col-erledigt')
    expect(Array.isArray(result.blockers)).toBe(true)
    expect(result.blockers[0].id).toBe(cardB.id)
  })
})

/* ─────────────────────────────────────────────────────────────────
   detectUnlockedCards — Auto-Entsperren-Erkennung
   ───────────────────────────────────────────────────────────────── */

describe('detectUnlockedCards()', () => {
  let board, cardA, cardB, cardC, cardD

  beforeEach(() => {
    board = makeBoard()
    cardA = makeCard('A', 'col-backlog', 1)    /* wird erledigt */
    cardB = makeCard('B', 'col-backlog', 2)    /* hat blocked-by A → wird entsperrt */
    cardC = makeCard('C', 'col-backlog', 3)    /* hat blocked-by A UND D → nicht entsperrt */
    cardD = makeCard('D', 'col-backlog', 4)    /* anderer Blocker von C */
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB, cardC, cardD])]
  })

  it('erkennt Karte, die NUR von der gerade erledigten Karte blockiert war', () => {
    addLink(board, cardB.id, cardA.id, 'blocked-by')
    const unlocked = detectUnlockedCards(board, cardA.id)
    expect(unlocked.length).toBe(1)
    expect(unlocked[0].id).toBe(cardB.id)
  })

  it('erkennt KEINE Karte, die noch weitere offene Blocker hat', () => {
    addLink(board, cardC.id, cardA.id, 'blocked-by') /* A erledigt */
    addLink(board, cardC.id, cardD.id, 'blocked-by') /* D noch offen */
    const unlocked = detectUnlockedCards(board, cardA.id)
    /* C hat noch D als Blocker → nicht entsperrt */
    expect(unlocked.find(c => c.id === cardC.id)).toBeUndefined()
  })

  it('gibt leeres Array zurück wenn keine Karte auf die erledigte zeigt', () => {
    const unlocked = detectUnlockedCards(board, cardA.id)
    expect(unlocked).toEqual([])
  })

  it('gibt leeres Array zurück für ungültige Board-Struktur', () => {
    expect(detectUnlockedCards(null, 'some-id')).toEqual([])
    expect(detectUnlockedCards({}, 'some-id')).toEqual([])
  })

  it('ignoriert verwaiste Verweise beim Zählen der restlichen Blocker', () => {
    addLink(board, cardB.id, cardA.id, 'blocked-by') /* A wird erledigt */
    /* verwaister Link direkt setzen — wird ignoriert */
    cardB.links.push({ targetId: 'nonexistent-uuid', type: 'blocked-by', created: '2026-05-13' })
    /* verwaiste Links zählen nicht → B gilt als entsperrt */
    const unlocked = detectUnlockedCards(board, cardA.id)
    expect(unlocked.find(c => c.id === cardB.id)).toBeTruthy()
  })

  it('erkennt mehrere entsperrte Karten gleichzeitig', () => {
    const cardE = makeCard('E', 'col-backlog', 5)
    board.sprints[0].cards.push(cardE)
    addLink(board, cardB.id, cardA.id, 'blocked-by')
    addLink(board, cardE.id, cardA.id, 'blocked-by')
    const unlocked = detectUnlockedCards(board, cardA.id)
    expect(unlocked.length).toBe(2)
  })

  it('erkennt keine Karte mit nur related- oder depends-on-Links', () => {
    addLink(board, cardB.id, cardA.id, 'related')
    addLink(board, cardC.id, cardA.id, 'depends-on')
    const unlocked = detectUnlockedCards(board, cardA.id)
    expect(unlocked.length).toBe(0)
  })
})

/* ─────────────────────────────────────────────────────────────────
   Sprint-Löschen → purgeLinksTo-Cleanup (Kein-Backlog-Pfad)
   ───────────────────────────────────────────────────────────────── */

describe('purgeLinksTo() bei Sprint-Löschen (Kein-Backlog-Pfad)', () => {
  it('entfernt Rückverweise auf alle Karten des gelöschten Sprints', () => {
    const board = makeBoard()
    const cardA = makeCard('A', 'col-backlog', 1) /* in Sprint s1 */
    const cardB = makeCard('B', 'col-backlog', 2) /* in Sprint s1 */
    const cardC = makeCard('C', 'col-backlog', 3) /* in Sprint s2 (bleibt) */
    board.sprints = [
      makeSprint('s1', 'Sprint 1', [cardA, cardB]),
      makeSprint('s2', 'Sprint 2', [cardC]),
    ]

    /* C verweist auf A und B in Sprint s1 */
    addLink(board, cardC.id, cardA.id, 'depends-on')
    addLink(board, cardC.id, cardB.id, 'blocked-by')
    expect(cardC.links.length).toBe(2)

    /* Sprint s1 wird gelöscht — Kein-Backlog-Pfad simulieren:
       purgeLinksTo für jede Karte des Sprints aufrufen */
    const sprint1 = board.sprints.find(s => s.id === 's1')
    for (const card of sprint1.cards) {
      purgeLinksTo(board, card.id)
    }
    board.sprints = board.sprints.filter(s => s.id !== 's1')

    /* C darf keine Verweise mehr auf A oder B haben */
    expect(cardC.links.length).toBe(0)
  })

  it('lässt unbetroffene Links intakt (Verweis auf andere Karten bleibt)', () => {
    const board = makeBoard()
    const cardA = makeCard('A', 'col-backlog', 1) /* in Sprint s1, wird gelöscht */
    const cardB = makeCard('B', 'col-backlog', 2) /* in Sprint s2, bleibt */
    const cardC = makeCard('C', 'col-backlog', 3) /* in Sprint s2, bleibt */
    board.sprints = [
      makeSprint('s1', 'Sprint 1', [cardA]),
      makeSprint('s2', 'Sprint 2', [cardB, cardC]),
    ]

    addLink(board, cardB.id, cardA.id, 'blocks')  /* B blockiert A → wird purged */
    addLink(board, cardB.id, cardC.id, 'related') /* B → C bleibt */

    purgeLinksTo(board, cardA.id)

    /* B→C link bleibt erhalten */
    expect(cardB.links.some(l => l.targetId === cardC.id && l.type === 'related')).toBe(true)
    /* B→A link ist weg */
    expect(cardB.links.some(l => l.targetId === cardA.id)).toBe(false)
  })
})

/* ─────────────────────────────────────────────────────────────────
   Manuelles blocked-Flag bleibt unberührt (Regression-Test)
   ───────────────────────────────────────────────────────────────── */

describe('manuelles blocked-Flag — kein Auto-Mutate durch linkBlocked', () => {
  it('isLinkBlocked verändert card.blocked NICHT', () => {
    const board = makeBoard()
    const cardA = makeCard('A', 'col-backlog', 1)
    const cardB = makeCard('B', 'col-backlog', 2)
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB])]
    cardA.blocked = true /* manuell gesetzt */

    addLink(board, cardA.id, cardB.id, 'blocked-by')
    isLinkBlocked(board, cardA) /* Aufruf darf blocked nicht verändern */

    expect(cardA.blocked).toBe(true) /* bleibt unverändert */
  })

  it('detectUnlockedCards verändert card.blocked NICHT', () => {
    const board = makeBoard()
    const cardA = makeCard('A', 'col-erledigt', 1)
    const cardB = makeCard('B', 'col-backlog', 2)
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB])]
    cardB.blocked = true /* manuell gesetzt */

    addLink(board, cardB.id, cardA.id, 'blocked-by')
    detectUnlockedCards(board, cardA.id) /* Aufruf darf blocked nicht verändern */

    expect(cardB.blocked).toBe(true) /* kein Auto-Mutate */
  })
})

/* ─────────────────────────────────────────────────────────────────
   F-127 — Erledigt-Erkennung per Titel-Semantik statt fester id
   ───────────────────────────────────────────────────────────────── */

/* Board mit einer manuell angelegten Erledigt-Spalte: UUID-artige id, Titel
   „Erledigt". Genau der Fall, den der id-Vergleich `=== 'col-erledigt'` nicht
   erkannt hat — der Blocker wurde dort nie freigegeben. */
function makeBoardMitUmbenannterErledigtSpalte() {
  return {
    id:            'board-f127',
    title:         'F-127',
    nextDisplayId: 1,
    columnDefs: [
      { id: 'col-backlog',   title: 'Backlog' },
      { id: 'col-in-arbeit', title: 'In Arbeit' },
      { id: 'col-9f3a-uuid', title: 'Erledigt' },
    ],
    sprints: [],
  }
}

describe('F-127 — Erledigt-Spalte per Titel, nicht per id', () => {
  it('isLinkBlocked(): Blocker in manuell angelegter „Erledigt"-Spalte gilt als erledigt', () => {
    const board = makeBoardMitUmbenannterErledigtSpalte()
    const cardA = makeCard('A', 'col-backlog', 1)
    const cardB = makeCard('B', 'col-9f3a-uuid', 2)
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB])]
    addLink(board, cardA.id, cardB.id, 'blocked-by')

    expect(isLinkBlocked(board, cardA)).toBe(false)
    expect(openBlockers(board, cardA)).toEqual([])
  })

  it('isLinkBlocked(): Spalte mit id col-erledigt, aber Titel „Wartet" gilt NICHT als erledigt', () => {
    const board = makeBoard()
    board.columnDefs.find(c => c.id === 'col-erledigt').title = 'Wartet'
    const cardA = makeCard('A', 'col-backlog', 1)
    const cardB = makeCard('B', 'col-erledigt', 2)
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB])]
    addLink(board, cardA.id, cardB.id, 'blocked-by')

    expect(isLinkBlocked(board, cardA)).toBe(true)
  })

  it('checkMoveWarning(): Ziel = manuell angelegte Erledigt-Spalte warnt bei offenem depends-on', () => {
    const board = makeBoardMitUmbenannterErledigtSpalte()
    const cardA = makeCard('A', 'col-backlog', 1)
    const cardB = makeCard('B', 'col-backlog', 2)
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB])]
    addLink(board, cardA.id, cardB.id, 'depends-on')

    const res = checkMoveWarning(board, cardA, 'col-9f3a-uuid')
    expect(res.warn).toBe(true)
    expect(res.type).toBe('depends-on')
  })

  it('detectUnlockedCards(): zweiter Blocker in umbenannter Erledigt-Spalte entsperrt', () => {
    const board = makeBoardMitUmbenannterErledigtSpalte()
    const cardA = makeCard('A', 'col-backlog', 1)   /* wird blockiert */
    const cardB = makeCard('B', 'col-9f3a-uuid', 2) /* gerade erledigt */
    const cardC = makeCard('C', 'col-9f3a-uuid', 3) /* weiterer Blocker, ebenfalls erledigt */
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB, cardC])]
    addLink(board, cardA.id, cardB.id, 'blocked-by')
    addLink(board, cardA.id, cardC.id, 'blocked-by')

    const unlocked = detectUnlockedCards(board, cardB.id)
    expect(unlocked.map(c => c.id)).toEqual([cardA.id])
  })

  it('Fallback: Board ohne columnDefs fällt auf den id-Vergleich zurück', () => {
    /* Sonst gälte dort gar keine Spalte mehr als erledigt und jeder Blocker
       bliebe für immer stehen — schlimmer als der behobene Fehler. */
    const board = { id: 'b', title: 'ohne defs', sprints: [] }
    const cardA = makeCard('A', 'col-backlog', 1)
    const cardB = makeCard('B', 'col-erledigt', 2)
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB])]
    addLink(board, cardA.id, cardB.id, 'blocked-by')

    expect(isLinkBlocked(board, cardA)).toBe(false)
  })
})
