/* @vitest-environment happy-dom */
/**
 * Unit-Tests für die Karten-Verweise (ADR-0032)
 *
 * Geprüft werden:
 *  - links.js — Symmetrie beim Add/Remove, Selbst-Invers (related), Idempotenz,
 *    Selbst-Verweis-Verbot, purgeLinksTo
 *  - export.js — Verweise-Zeile im KI-Kontext, Sprint-Suffix bei Inter-Sprint-Refs,
 *    verwaiste Verweise werden ignoriert
 *  - modal.js — Tab „Verknüpfungen" ist immer sichtbar, hat role=tab + aria-controls,
 *    Empty-Hinweis bei links=[]
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  addLink, removeLink, purgeLinksTo, inverseType, isValidType,
  LINK_TYPES, groupLinksByType,
} from '../src/links.js'
import { Model } from '../src/model.js'
import { Export } from '../src/export.js'

/* ─────────────────────────────────────────────────────────────────
   Hilfsfunktionen
   ───────────────────────────────────────────────────────────────── */

function makeBoard() {
  return {
    id:            'board-test',
    title:         'Test-Board',
    nextDisplayId: 1,
    columnDefs:    [
      { id: 'col-backlog', title: 'Backlog' },
      { id: 'col-ready',   title: 'Ready'   },
      { id: 'col-done',    title: 'Erledigt' },
    ],
    sprints: [],
  }
}

function makeCard(title, columnId = 'col-backlog') {
  const c = Model.createCard(title, columnId, null)
  c.displayId = Model.assignDisplayId({ nextDisplayId: 100 })
  return c
}

function makeSprint(id, title, cards = []) {
  return { id, title, cards }
}

/* ─────────────────────────────────────────────────────────────────
   Model — links-Feld
   ───────────────────────────────────────────────────────────────── */

describe('Model — links-Feld (ADR-0032)', () => {
  it('createCard() liefert links als leeres Array', () => {
    const card = Model.createCard('Neue Karte')
    expect(Array.isArray(card.links)).toBe(true)
    expect(card.links.length).toBe(0)
  })
})

/* ─────────────────────────────────────────────────────────────────
   links.js — Hilfsfunktionen
   ───────────────────────────────────────────────────────────────── */

describe('inverseType()', () => {
  it('blocks ↔ blocked-by', () => {
    expect(inverseType('blocks')).toBe('blocked-by')
    expect(inverseType('blocked-by')).toBe('blocks')
  })

  it('depends-on ↔ required-for', () => {
    expect(inverseType('depends-on')).toBe('required-for')
    expect(inverseType('required-for')).toBe('depends-on')
  })

  it('related ist selbst-invers', () => {
    expect(inverseType('related')).toBe('related')
  })

  it('liefert null für unbekannten Typ', () => {
    expect(inverseType('foo')).toBeNull()
  })
})

describe('isValidType()', () => {
  it('akzeptiert alle fünf LINK_TYPES', () => {
    LINK_TYPES.forEach(t => expect(isValidType(t)).toBe(true))
  })

  it('lehnt unbekannte Typen ab', () => {
    expect(isValidType('foo')).toBe(false)
    expect(isValidType('')).toBe(false)
    expect(isValidType(null)).toBe(false)
  })
})

/* ─────────────────────────────────────────────────────────────────
   addLink — Symmetrie
   ───────────────────────────────────────────────────────────────── */

describe('addLink() — Symmetrie-Pflicht', () => {
  let board, cardA, cardB, cardC

  beforeEach(() => {
    board = makeBoard()
    cardA = makeCard('A')
    cardB = makeCard('B')
    cardC = makeCard('C')
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB, cardC])]
  })

  it('blocks → fügt blocked-by auf Zielkarte hinzu', () => {
    const changed = addLink(board, cardA.id, cardB.id, 'blocks')
    expect(changed).toBe(true)
    expect(cardA.links).toContainEqual(expect.objectContaining({
      targetId: cardB.id, type: 'blocks',
    }))
    expect(cardB.links).toContainEqual(expect.objectContaining({
      targetId: cardA.id, type: 'blocked-by',
    }))
  })

  it('depends-on → fügt required-for auf Zielkarte hinzu', () => {
    addLink(board, cardA.id, cardB.id, 'depends-on')
    expect(cardA.links).toContainEqual(expect.objectContaining({
      targetId: cardB.id, type: 'depends-on',
    }))
    expect(cardB.links).toContainEqual(expect.objectContaining({
      targetId: cardA.id, type: 'required-for',
    }))
  })

  it('related → fügt related (selbst-invers) auf Zielkarte hinzu', () => {
    addLink(board, cardA.id, cardB.id, 'related')
    expect(cardA.links).toContainEqual(expect.objectContaining({
      targetId: cardB.id, type: 'related',
    }))
    expect(cardB.links).toContainEqual(expect.objectContaining({
      targetId: cardA.id, type: 'related',
    }))
  })

  it('verbietet Selbst-Verweis (source==target)', () => {
    const changed = addLink(board, cardA.id, cardA.id, 'related')
    expect(changed).toBe(false)
    expect(cardA.links.length).toBe(0)
  })

  it('lehnt unbekannten Typ ab', () => {
    const changed = addLink(board, cardA.id, cardB.id, 'foo')
    expect(changed).toBe(false)
    expect(cardA.links.length).toBe(0)
    expect(cardB.links.length).toBe(0)
  })

  it('idempotent: zweiter Add desselben Links ändert nichts', () => {
    addLink(board, cardA.id, cardB.id, 'blocks')
    const changed = addLink(board, cardA.id, cardB.id, 'blocks')
    expect(changed).toBe(false)
    expect(cardA.links.filter(l => l.targetId === cardB.id && l.type === 'blocks').length).toBe(1)
    expect(cardB.links.filter(l => l.targetId === cardA.id && l.type === 'blocked-by').length).toBe(1)
  })

  it('lehnt Verweis auf nicht existente Karte ab', () => {
    const changed = addLink(board, cardA.id, 'nonexistent-id', 'blocks')
    expect(changed).toBe(false)
    expect(cardA.links.length).toBe(0)
  })

  it('Verweis-Eintrag enthält targetId, type und created (ISO-Datum)', () => {
    addLink(board, cardA.id, cardB.id, 'blocks')
    const link = cardA.links[0]
    expect(link.targetId).toBe(cardB.id)
    expect(link.type).toBe('blocks')
    expect(link.created).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })
})

/* ─────────────────────────────────────────────────────────────────
   removeLink — Symmetrie
   ───────────────────────────────────────────────────────────────── */

describe('removeLink() — Symmetrie-Pflicht', () => {
  let board, cardA, cardB

  beforeEach(() => {
    board = makeBoard()
    cardA = makeCard('A')
    cardB = makeCard('B')
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB])]
  })

  it('entfernt Eintrag auf Quell- UND Zielkarte (blocks/blocked-by)', () => {
    addLink(board, cardA.id, cardB.id, 'blocks')
    const changed = removeLink(board, cardA.id, cardB.id, 'blocks')
    expect(changed).toBe(true)
    expect(cardA.links.length).toBe(0)
    expect(cardB.links.length).toBe(0)
  })

  it('entfernt related symmetrisch (selbst-invers)', () => {
    addLink(board, cardA.id, cardB.id, 'related')
    removeLink(board, cardA.id, cardB.id, 'related')
    expect(cardA.links.length).toBe(0)
    expect(cardB.links.length).toBe(0)
  })

  it('idempotent: zweiter Remove ändert nichts', () => {
    addLink(board, cardA.id, cardB.id, 'depends-on')
    removeLink(board, cardA.id, cardB.id, 'depends-on')
    const changed = removeLink(board, cardA.id, cardB.id, 'depends-on')
    expect(changed).toBe(false)
  })

  it('entfernt nur den genannten Typ, andere bleiben bestehen', () => {
    addLink(board, cardA.id, cardB.id, 'blocks')
    addLink(board, cardA.id, cardB.id, 'related')
    removeLink(board, cardA.id, cardB.id, 'blocks')
    expect(cardA.links.length).toBe(1)
    expect(cardA.links[0].type).toBe('related')
    expect(cardB.links.length).toBe(1)
    expect(cardB.links[0].type).toBe('related')
  })
})

/* ─────────────────────────────────────────────────────────────────
   purgeLinksTo — Aufräumen beim Karten-Löschen
   ───────────────────────────────────────────────────────────────── */

describe('purgeLinksTo() — Karten-Löschung räumt Rückverweise', () => {
  it('entfernt alle Rückverweise auf eine gelöschte Karte', () => {
    const board = makeBoard()
    const cardA = makeCard('A')
    const cardB = makeCard('B')
    const cardC = makeCard('C')
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB, cardC])]

    addLink(board, cardA.id, cardB.id, 'blocks')
    addLink(board, cardC.id, cardB.id, 'depends-on')
    /* B hat jetzt zwei Rückverweise (blocked-by A, required-for C) */
    expect(cardB.links.length).toBe(2)

    /* Karte B wird gelöscht → A und C müssen ihre Verweise verlieren */
    purgeLinksTo(board, cardB.id)
    expect(cardA.links.length).toBe(0)
    expect(cardC.links.length).toBe(0)
    /* B selbst bleibt unangetastet (wird vom Löscher danach entfernt) */
    expect(cardB.links.length).toBe(2)
  })

  it('lässt unbeteiligte Verweise unverändert', () => {
    const board = makeBoard()
    const cardA = makeCard('A')
    const cardB = makeCard('B')
    const cardC = makeCard('C')
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB, cardC])]

    addLink(board, cardA.id, cardC.id, 'related')
    addLink(board, cardA.id, cardB.id, 'blocks')

    purgeLinksTo(board, cardB.id)
    /* A → C bleibt */
    expect(cardA.links).toContainEqual(expect.objectContaining({
      targetId: cardC.id, type: 'related',
    }))
    expect(cardC.links).toContainEqual(expect.objectContaining({
      targetId: cardA.id, type: 'related',
    }))
  })

  it('F-126: eine Karte mit ZWEI verschiedenen Link-Typen zur selben gelöschten Karte verliert BEIDE Einträge', () => {
    /* Test-Lücke (F-126, Karte #527): Zwei Karten dürfen gleichzeitig über
       mehrere Typen verknüpft sein, z.B. "blockiert" UND "related" — addLink()
       prüft Eindeutigkeit nur über das Paar (targetId, type), nicht über
       targetId allein. purgeLinksTo() filtert dagegen ausschliesslich nach
       targetId — muss also BEIDE Einträge entfernen, nicht nur den zuerst
       gefundenen. */
    const board = makeBoard()
    const cardA = makeCard('A')
    const cardB = makeCard('B')
    board.sprints = [makeSprint('s1', 'Sprint 1', [cardA, cardB])]

    addLink(board, cardA.id, cardB.id, 'blocks')
    addLink(board, cardA.id, cardB.id, 'related')
    expect(cardA.links).toHaveLength(2)
    expect(cardB.links).toHaveLength(2)   /* blocked-by + related */

    purgeLinksTo(board, cardB.id)

    expect(cardA.links).toEqual([])
  })
})

/* ─────────────────────────────────────────────────────────────────
   groupLinksByType
   ───────────────────────────────────────────────────────────────── */

describe('groupLinksByType()', () => {
  it('liefert für jeden LINK_TYPES eine Gruppe (leer wenn kein Eintrag)', () => {
    const card = { links: [] }
    const groups = groupLinksByType(card)
    LINK_TYPES.forEach(t => {
      expect(Array.isArray(groups[t])).toBe(true)
      expect(groups[t].length).toBe(0)
    })
  })

  it('gruppiert Einträge nach type', () => {
    const card = {
      links: [
        { targetId: 't1', type: 'blocks',     created: '2026-05-13' },
        { targetId: 't2', type: 'blocks',     created: '2026-05-13' },
        { targetId: 't3', type: 'related',    created: '2026-05-13' },
      ],
    }
    const groups = groupLinksByType(card)
    expect(groups['blocks'].length).toBe(2)
    expect(groups['related'].length).toBe(1)
    expect(groups['depends-on'].length).toBe(0)
  })
})

/* ─────────────────────────────────────────────────────────────────
   Export — Verweise-Zeile im KI-Kontext
   ───────────────────────────────────────────────────────────────── */

describe('buildKIKontext — Verweise-Zeile (ADR-0032)', () => {
  function captureKIKontext(state) {
    let captured = null
    const origBlob = globalThis.Blob
    globalThis.Blob = class { constructor(parts) { captured = parts.join('') } }
    const origURL = globalThis.URL
    globalThis.URL = { createObjectURL: () => 'blob:mock', revokeObjectURL: () => {} }
    const origCreate = document.createElement.bind(document)
    document.createElement = (tag) => tag === 'a'
      ? { href: '', download: '', click: () => {} }
      : origCreate(tag)
    Export.toKIKontextDownload(state)
    globalThis.Blob = origBlob
    globalThis.URL = origURL
    document.createElement = origCreate
    return captured ?? ''
  }

  function makeState(sprints) {
    return {
      version: '2',
      board: {
        title: 'Test',
        columnDefs: [
          { id: 'col-backlog', title: 'Backlog' },
          { id: 'col-ready',   title: 'Ready'   },
        ],
        sprints,
      },
    }
  }

  it('schreibt "Verweise:"-Zeile für Karte mit Links', () => {
    const cardA = makeCard('A')
    const cardB = makeCard('B')
    cardA.displayId = 1; cardB.displayId = 42
    cardA.links = [{ targetId: cardB.id, type: 'blocked-by', created: '2026-05-13' }]
    const state = makeState([makeSprint('s1', 'Sprint 1', [cardA, cardB])])
    const output = captureKIKontext(state)
    expect(output).toContain('Verweise: blockiert von #42')
  })

  it('gibt KEINE Verweise-Zeile aus wenn links leer', () => {
    const cardA = makeCard('A')
    cardA.displayId = 7; cardA.links = []
    const state = makeState([makeSprint('s1', 'Sprint 1', [cardA])])
    const output = captureKIKontext(state)
    expect(output).not.toContain('Verweise:')
  })

  it('Inter-Sprint-Verweis bekommt Sprint-Suffix', () => {
    const cardA = makeCard('A')
    const cardB = makeCard('B')
    cardA.displayId = 1; cardB.displayId = 38
    cardA.links = [{ targetId: cardB.id, type: 'depends-on', created: '2026-05-13' }]
    const state = makeState([
      makeSprint('s1', 'Sprint 1', [cardA]),
      makeSprint('s2', 'Sprint 2', [cardB]),
    ])
    const output = captureKIKontext(state)
    /* Suffix erwartet, weil B in einem anderen Sprint als A liegt */
    expect(output).toMatch(/Verweise: hängt ab von #38 \(Sprint 2\)/)
  })

  it('Verweise auf Karten im selben Sprint OHNE Sprint-Suffix', () => {
    const cardA = makeCard('A')
    const cardB = makeCard('B')
    cardA.displayId = 1; cardB.displayId = 2
    cardA.links = [{ targetId: cardB.id, type: 'related', created: '2026-05-13' }]
    const state = makeState([makeSprint('s1', 'Sprint 1', [cardA, cardB])])
    const output = captureKIKontext(state)
    expect(output).toContain('Verweise: verwandt mit #2')
    expect(output).not.toContain('#2 (Sprint')
  })

  it('verwaiste Verweise (Ziel existiert nicht) werden weggelassen', () => {
    const cardA = makeCard('A')
    cardA.displayId = 1
    cardA.links = [{ targetId: 'nonexistent-uuid', type: 'blocks', created: '2026-05-13' }]
    const state = makeState([makeSprint('s1', 'Sprint 1', [cardA])])
    const output = captureKIKontext(state)
    /* Karte hat einen Verweis — der ist verwaist → Zeile entfällt komplett */
    expect(output).not.toContain('Verweise:')
  })

  it('mehrere Verweise einer Karte werden komma-getrennt aufgelistet, Reihenfolge nach Typ', () => {
    const cardA = makeCard('A')
    const cardB = makeCard('B'); cardB.displayId = 2
    const cardC = makeCard('C'); cardC.displayId = 3
    cardA.displayId = 1
    cardA.links = [
      { targetId: cardC.id, type: 'related', created: '2026-05-13' },
      { targetId: cardB.id, type: 'blocks',  created: '2026-05-13' },
    ]
    const state = makeState([makeSprint('s1', 'Sprint 1', [cardA, cardB, cardC])])
    const output = captureKIKontext(state)
    /* blocks kommt vor related laut Typen-Reihenfolge */
    expect(output).toMatch(/Verweise: blockiert #2, verwandt mit #3/)
  })
})

/* ─────────────────────────────────────────────────────────────────
   Modal — Tab „Verknüpfungen" DOM-Struktur
   ───────────────────────────────────────────────────────────────── */

/* Mock für Config + Swipe — analog zu modal.dom.test.js */
let boardModeValue = 'standalone'
vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
vi.mock('../src/config.js', () => ({
  Config: {
    get: vi.fn((key) => {
      if (key === 'boardMode') return boardModeValue
      if (key === 'userName')  return 'TestUser'
      return ''
    }),
    set: vi.fn(),
  },
}))

import { EventBus } from '../src/eventbus.js'
import { Modal }    from '../src/modal.js'
import { State }    from '../src/state.js'

function openModalWith(card, boardCards = []) {
  /* State setzen, damit findCardById() im Modal funktioniert */
  State.set({
    version: '2',
    board: {
      id:            'b-test',
      title:         'Test',
      nextDisplayId: 100,
      columnDefs:    [{ id: 'col-backlog', title: 'Backlog' }],
      sprints: [{ id: 's1', title: 'Sprint 1', cards: [card, ...boardCards] }],
    },
  })
  EventBus.emit('card:edit', card)
  return document.querySelector('.modal-overlay')
}

function resetModal() {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
}

describe('Modal — Tab „Verknüpfungen"', () => {
  beforeEach(() => {
    boardModeValue = 'standalone'
    vi.useFakeTimers()
    resetModal()
  })
  afterEach(() => {
    resetModal()
    vi.useRealTimers()
    State.markClean()
  })

  function makeUiCard(overrides = {}) {
    return {
      id:           'card-ui',
      displayId:    99,
      title:        'UI-Test',
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
      ...overrides,
    }
  }

  it('Tab-Bar enthält vier Tabs (desc, comments, agent, links)', () => {
    const overlay = openModalWith(makeUiCard())
    expect(overlay).toBeTruthy()
    expect(overlay.querySelector('#tab-desc')).toBeTruthy()
    expect(overlay.querySelector('#tab-comments')).toBeTruthy()
    expect(overlay.querySelector('#tab-agent')).toBeTruthy()
    expect(overlay.querySelector('#tab-links')).toBeTruthy()
  })

  it('Tab „Verknüpfungen" ist IMMER sichtbar (auch standalone ohne Claude-Owner)', () => {
    const overlay = openModalWith(makeUiCard({ owner: 'TestUser' }))
    const tabLinks = overlay.querySelector('#tab-links')
    /* Kein style.display = 'none' wie beim Agent-Tab */
    expect(tabLinks.style.display).not.toBe('none')
  })

  it('Panel hat role=tabpanel und aria-labelledby=tab-links', () => {
    const overlay = openModalWith(makeUiCard())
    const panel = overlay.querySelector('#tab-panel-links')
    expect(panel).toBeTruthy()
    expect(panel.getAttribute('role')).toBe('tabpanel')
    expect(panel.getAttribute('aria-labelledby')).toBe('tab-links')
  })

  it('aria-controls zeigt auf das richtige Panel', () => {
    const overlay = openModalWith(makeUiCard())
    const btn = overlay.querySelector('#tab-links')
    expect(btn.getAttribute('aria-controls')).toBe('tab-panel-links')
  })

  it('Empty-Zustand: leere Karte zeigt „link.empty"-Hinweis', () => {
    const overlay = openModalWith(makeUiCard({ links: [] }))
    overlay.querySelector('#tab-links').click()
    const list = overlay.querySelector('#links-list')
    expect(list).toBeTruthy()
    /* Empty-Hinweis als <p class="link-empty"> */
    expect(list.querySelector('.link-empty')).toBeTruthy()
  })

  it('Karte mit Verweis zeigt Eintrag mit Typ-Icon, Hash-Link und Remove-Button', () => {
    const otherCard = {
      id: 'card-other', displayId: 42, title: 'Andere Karte',
      columnId: 'col-backlog', links: [], comments: [],
    }
    const card = makeUiCard({
      links: [{ targetId: 'card-other', type: 'blocks', created: '2026-05-13' }],
    })
    const overlay = openModalWith(card, [otherCard])
    overlay.querySelector('#tab-links').click()
    const entry = overlay.querySelector('.link-entry')
    expect(entry).toBeTruthy()
    /* Anker auf #card-42 */
    const a = entry.querySelector('a.link-target')
    expect(a).toBeTruthy()
    expect(a.getAttribute('href')).toBe('#card-42')
    /* Remove-Button */
    expect(entry.querySelector('.link-remove-btn')).toBeTruthy()
  })
})

/* ─────────────────────────────────────────────────────────────────
   F-125 — Link-Picker filtert typ-bewusst
   ───────────────────────────────────────────────────────────────── */

describe('Modal — Link-Picker (F-125)', () => {
  beforeEach(() => {
    boardModeValue = 'standalone'
    vi.useFakeTimers()
    resetModal()
  })
  afterEach(() => {
    resetModal()
    vi.useRealTimers()
    State.markClean()
  })

  function pickerCard(overrides = {}) {
    return {
      id: 'card-quelle', displayId: 1, title: 'Quelle',
      description: '', agentContext: '', comments: [], attachments: [],
      links: [], activity: [], priority: 'mittel', labels: [], owner: '',
      complexity: '', effort: null, qa: false, critical: false, blocked: false,
      archived: false, columnId: 'col-backlog', ...overrides,
    }
  }
  const zielKarte = {
    id: 'card-ziel', displayId: 2, title: 'Ziel',
    columnId: 'col-backlog', links: [], comments: [],
  }

  /** Picker öffnen und die Titel der angebotenen Zeilen liefern */
  function pickerZeilen(overlay) {
    return [...overlay.querySelectorAll('.link-picker-row')].map(r => r.textContent)
  }

  it('bereits mit „related" verlinkte Karte bleibt für einen ANDEREN Typ wählbar', () => {
    /* Vorher schloss der Picker jede Karte aus, die irgendwie verlinkt war —
       ein zweiter Verweistyp auf dieselbe Karte war über die Oberfläche
       unmöglich, obwohl addLink() ihn zulässt (hasLink prüft targetId UND
       type). Man musste den bestehenden Verweis erst löschen. */
    const card = pickerCard({
      links: [{ targetId: 'card-ziel', type: 'related', created: '2026-08-28' }],
    })
    const overlay = openModalWith(card, [zielKarte])
    overlay.querySelector('#tab-links').click()
    overlay.querySelector('#link-add-btn').click()

    const typeSel = overlay.querySelector('.link-picker-type-select')
    typeSel.value = 'blocks'
    typeSel.dispatchEvent(new Event('change'))

    expect(pickerZeilen(overlay).join(' ')).toContain('Ziel')
  })

  it('für den bereits vergebenen Typ bleibt dieselbe Karte ausgeschlossen', () => {
    const card = pickerCard({
      links: [{ targetId: 'card-ziel', type: 'related', created: '2026-08-28' }],
    })
    const overlay = openModalWith(card, [zielKarte])
    overlay.querySelector('#tab-links').click()
    overlay.querySelector('#link-add-btn').click()

    const typeSel = overlay.querySelector('.link-picker-type-select')
    typeSel.value = 'related'
    typeSel.dispatchEvent(new Event('change'))

    expect(pickerZeilen(overlay).join(' ')).not.toContain('Ziel')
    expect(overlay.querySelector('.link-picker-empty')).toBeTruthy()
  })

  it('die Quellkarte selbst wird weiterhin nie angeboten', () => {
    const overlay = openModalWith(pickerCard(), [zielKarte])
    overlay.querySelector('#tab-links').click()
    overlay.querySelector('#link-add-btn').click()

    const zeilen = pickerZeilen(overlay).join(' ')
    expect(zeilen).toContain('Ziel')
    expect(zeilen).not.toContain('Quelle')
  })
})
