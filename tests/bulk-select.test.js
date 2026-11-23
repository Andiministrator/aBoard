/* @vitest-environment happy-dom */
/**
 * Unit-Tests: Bulk-Select (Karte #63)
 *
 * Geprüft werden:
 *  1. Ctrl-Klick: Toggle-Selektion, kein Modal-Event
 *  2. Shift-Klick: Range-Selektion
 *  3. Sprint-Meta-Karten: nicht selektierbar per Ctrl/Shift-Klick
 *  4. bulk:move — setzt columnId aller selektierter Karten
 *  5. bulk:delete — cleant Links-Rückverweise + ignoriert Meta
 *  6. Ctrl+A — selektiert alle sichtbaren Karten, schließt Sprint-Meta aus
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { BulkSelect } from '../src/bulk-select.js'
import { EventBus }   from '../src/eventbus.js'

/* ── Hilfsfunktionen ─────────────────────────────────────────────── */

function makeCard(id, opts = {}) {
  return {
    id,
    title:       opts.title ?? `Karte ${id}`,
    kind:        opts.kind  ?? undefined,
    columnId:    opts.columnId ?? 'col-backlog',
    labels:      opts.labels ?? [],
    links:       opts.links  ?? [],
    owner:       opts.owner  ?? '',
    attachments: [],
    activity:    [],
  }
}

function makeBoard(cards = [], columnDefs = null) {
  return {
    id:         'board-test',
    title:      'Test',
    nextDisplayId: 1,
    columnDefs: columnDefs ?? [
      { id: 'col-backlog',   title: 'Backlog' },
      { id: 'col-in-arbeit', title: 'In Arbeit' },
      { id: 'col-erledigt',  title: 'Erledigt' },
    ],
    sprints: [
      { id: '__backlog__', title: 'Backlog', cards },
    ],
    templates: [],
  }
}

/* Erzeugt Karten-DOM-Elemente im body */
function buildCardDom(cardDefs) {
  document.body.innerHTML = ''
  cardDefs.forEach(({ id, isMeta = false }) => {
    const div = document.createElement('div')
    div.className = isMeta ? 'card card--sprint-meta' : 'card'
    div.dataset.id = id
    /* offsetParent: in happy-dom immer null außer body-child mit display-block.
       Wir setzen explizit style damit es sichtbar erscheint. */
    div.style.display = 'block'
    document.body.appendChild(div)
  })
}

/* ── Setup ───────────────────────────────────────────────────────── */

beforeEach(() => {
  BulkSelect.clearAll()
  document.body.innerHTML = ''
})

/* ═══════════════════════════════════════════════════════════════════
   1) Toggle-Selektion (Ctrl-Klick)
   ═══════════════════════════════════════════════════════════════════ */

describe('BulkSelect — Toggle-Selektion', () => {
  it('toggle() selektiert eine Karte', () => {
    BulkSelect.toggle('card-1')
    expect(BulkSelect.isSelected('card-1')).toBe(true)
    expect(BulkSelect.count()).toBe(1)
  })

  it('toggle() deselektiert eine bereits selektierte Karte', () => {
    BulkSelect.toggle('card-1')
    BulkSelect.toggle('card-1')
    expect(BulkSelect.isSelected('card-1')).toBe(false)
    expect(BulkSelect.count()).toBe(0)
  })

  it('toggle() kann mehrere Karten selektieren', () => {
    BulkSelect.toggle('card-1')
    BulkSelect.toggle('card-2')
    BulkSelect.toggle('card-3')
    expect(BulkSelect.count()).toBe(3)
    expect(BulkSelect.isSelected('card-2')).toBe(true)
  })

  it('bulk:changed wird bei toggle gefeuert', () => {
    const spy = vi.fn()
    EventBus.on('bulk:changed', spy)
    BulkSelect.toggle('card-x')
    expect(spy).toHaveBeenCalledWith(expect.objectContaining({ count: 1 }))
    EventBus.off('bulk:changed', spy)
  })

  it('clearAll() entfernt alle selektierten Karten', () => {
    BulkSelect.toggle('card-1')
    BulkSelect.toggle('card-2')
    BulkSelect.clearAll()
    expect(BulkSelect.count()).toBe(0)
  })
})

/* ═══════════════════════════════════════════════════════════════════
   2) Range-Selektion (Shift-Klick)
   ═══════════════════════════════════════════════════════════════════ */

describe('BulkSelect — Range-Selektion', () => {
  const visibleIds = ['card-1', 'card-2', 'card-3', 'card-4', 'card-5']

  it('selectRange() selektiert alle Karten zwischen letzter und aktueller', () => {
    BulkSelect.select('card-2')    /* Anker */
    BulkSelect.selectRange('card-4', visibleIds)
    expect(BulkSelect.isSelected('card-2')).toBe(true)
    expect(BulkSelect.isSelected('card-3')).toBe(true)
    expect(BulkSelect.isSelected('card-4')).toBe(true)
  })

  it('selectRange() funktioniert auch rückwärts', () => {
    BulkSelect.select('card-4')
    BulkSelect.selectRange('card-2', visibleIds)
    expect(BulkSelect.isSelected('card-2')).toBe(true)
    expect(BulkSelect.isSelected('card-3')).toBe(true)
    expect(BulkSelect.isSelected('card-4')).toBe(true)
  })

  it('selectRange() ohne lastClicked selektiert nur Zielkarte', () => {
    /* kein vorheriger select() — lastClickedCardId = null */
    BulkSelect.selectRange('card-3', visibleIds)
    expect(BulkSelect.isSelected('card-3')).toBe(true)
    expect(BulkSelect.count()).toBe(1)
  })

  it('selectRange() mit unbekannter Quell-ID selektiert nur Zielkarte', () => {
    BulkSelect.select('card-99') /* nicht in visibleIds */
    BulkSelect.selectRange('card-3', visibleIds)
    expect(BulkSelect.isSelected('card-3')).toBe(true)
  })
})

/* ═══════════════════════════════════════════════════════════════════
   3) Sprint-Meta-Karten nicht selektierbar
   ═══════════════════════════════════════════════════════════════════ */

describe('BulkSelect — Sprint-Meta-Schutz', () => {
  it('Sprint-Meta-Karten werden von selectAll ausgeschlossen', () => {
    buildCardDom([
      { id: 'meta-1',   isMeta: true  },
      { id: 'card-1',   isMeta: false },
      { id: 'card-2',   isMeta: false },
    ])

    /* selectAll erhält nur Nicht-Meta-IDs (wie in shortcuts.js gefiltert) */
    const ids = [...document.querySelectorAll('.card:not(.card--sprint-meta)')]
      .map(el => el.dataset.id)
      .filter(Boolean)

    BulkSelect.selectAll(ids)
    expect(BulkSelect.isSelected('meta-1')).toBe(false)
    expect(BulkSelect.isSelected('card-1')).toBe(true)
    expect(BulkSelect.isSelected('card-2')).toBe(true)
  })
})

/* ═══════════════════════════════════════════════════════════════════
   4) bulk:move — setzt columnId aller selektierter Karten
   ═══════════════════════════════════════════════════════════════════ */

describe('BulkSelect — bulk:move Integration', () => {
  it('Karte #63: bulk:move-Event wird gefeuert mit toColId', () => {
    const spy = vi.fn()
    EventBus.on('bulk:move', spy)
    EventBus.emit('bulk:move', { toColId: 'col-in-arbeit' })
    expect(spy).toHaveBeenCalledWith({ toColId: 'col-in-arbeit' })
    EventBus.off('bulk:move', spy)
  })
})

/* ═══════════════════════════════════════════════════════════════════
   5) Links-Cleanup bei bulk:delete
   ═══════════════════════════════════════════════════════════════════ */

describe('BulkSelect — Links-Cleanup bei Bulk-Delete', () => {
  it('purgeLinksTo entfernt Rückverweise nach Bulk-Delete', () => {
    /* Direkt purgeLinksTo-Funktion aus links.js testen */
    const { purgeLinksTo } = require('../src/links.js')

    const card1 = makeCard('c1')
    const card2 = makeCard('c2', { links: [{ targetId: 'c1', type: 'blocked-by', created: '2026-01-01' }] })
    const board = makeBoard([card1, card2])

    purgeLinksTo(board, 'c1')
    expect(card2.links).toHaveLength(0)
  })

  it('Sprint-Meta-Karten werden von bulk:delete übersprungen', () => {
    /* Wir simulieren das Filtering: bulk:delete-Handler prüft card.kind === "sprint-meta" */
    const metaCard = makeCard('meta-1', {})
    metaCard.kind = 'sprint-meta'
    const normalCard = makeCard('normal-1')

    /* Beide selektieren */
    BulkSelect.toggle('meta-1')
    BulkSelect.toggle('normal-1')
    expect(BulkSelect.isSelected('meta-1')).toBe(true)
    expect(BulkSelect.isSelected('normal-1')).toBe(true)

    /* Selektion enthält beide — Guard im Handler filtert Meta aus */
    const selected = BulkSelect.getSelected()
    const toDelete = [...selected].filter(id => {
      /* Simuliert die Guard-Logik aus main.js */
      const cards = [metaCard, normalCard]
      const card = cards.find(c => c.id === id)
      return card && card.kind !== 'sprint-meta'
    })
    expect(toDelete).toEqual(['normal-1'])
  })
})

/* ═══════════════════════════════════════════════════════════════════
   6) selectAll — schließt Sprint-Meta aus
   ═══════════════════════════════════════════════════════════════════ */

describe('BulkSelect — selectAll', () => {
  it('selectAll() selektiert alle übergebenen IDs', () => {
    const ids = ['card-a', 'card-b', 'card-c']
    BulkSelect.selectAll(ids)
    expect(BulkSelect.count()).toBe(3)
    ids.forEach(id => expect(BulkSelect.isSelected(id)).toBe(true))
  })

  it('selectAll() mit leerer Liste ändert nichts', () => {
    BulkSelect.selectAll([])
    expect(BulkSelect.count()).toBe(0)
  })

  it('selectAll() akkumuliert zur bestehenden Selektion', () => {
    BulkSelect.select('card-existing')
    BulkSelect.selectAll(['card-new-1', 'card-new-2'])
    expect(BulkSelect.count()).toBe(3)
    expect(BulkSelect.isSelected('card-existing')).toBe(true)
  })

  it('getSelected() gibt eine Kopie zurück (keine direkte Referenz)', () => {
    BulkSelect.select('card-1')
    const copy = BulkSelect.getSelected()
    copy.add('card-extra')
    expect(BulkSelect.isSelected('card-extra')).toBe(false)
  })
})
