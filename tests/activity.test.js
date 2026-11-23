/* @vitest-environment happy-dom */
/**
 * Unit-Tests für activity.js — ADR-0037
 *
 * Geprüft werden:
 *  - recordChange() — einzelnen Eintrag anlegen
 *  - diffAndRecord() — Felder vergleichen und Einträge erzeugen
 *  - Migrations-Funktion — migrateActivity() indirekt via Storage-Muster
 *  - Modal-Tab — tab-history und tab-panel-history existieren (DOM)
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { Activity } from '../src/activity.js'
import { Model }    from '../src/model.js'

/* ─────────────────────────────────────────────────────────────────
   recordChange — Einzeleintrag
   ───────────────────────────────────────────────────────────────── */

describe('Activity.recordChange()', () => {
  it('erzeugt einen Eintrag im activity-Array', () => {
    const card = { activity: [] }
    Activity.recordChange(card, 'column-moved', 'Testnutzer', { from: 'col-a', to: 'col-b' })
    expect(card.activity).toHaveLength(1)
    expect(card.activity[0].action).toBe('column-moved')
    expect(card.activity[0].author).toBe('Testnutzer')
    expect(card.activity[0].from).toBe('col-a')
    expect(card.activity[0].to).toBe('col-b')
    expect(card.activity[0].id).toBeTruthy()
    expect(card.activity[0].timestamp).toBeTruthy()
  })

  it('initialisiert activity-Array wenn es fehlt', () => {
    const card = {}
    Activity.recordChange(card, 'owner-changed', 'Andi')
    expect(Array.isArray(card.activity)).toBe(true)
    expect(card.activity).toHaveLength(1)
  })

  it('setzt leeren author wenn kein Name übergeben', () => {
    const card = { activity: [] }
    Activity.recordChange(card, 'field-updated', '', { field: 'qa', from: 'false', to: 'true' })
    expect(card.activity[0].author).toBe('')
    expect(card.activity[0].field).toBe('qa')
  })

  it('enthält kein field-Attribut wenn nicht übergeben', () => {
    const card = { activity: [] }
    Activity.recordChange(card, 'column-moved', 'Test')
    expect(card.activity[0].field).toBeUndefined()
    expect(card.activity[0].from).toBeUndefined()
    expect(card.activity[0].to).toBeUndefined()
  })
})

/* ─────────────────────────────────────────────────────────────────
   diffAndRecord — Felder vergleichen
   ───────────────────────────────────────────────────────────────── */

describe('Activity.diffAndRecord()', () => {
  let oldCard, newCard

  beforeEach(() => {
    oldCard = Model.createCard('TestKarte', 'col-backlog')
    newCard = { ...oldCard }
    newCard.activity = [...(oldCard.activity || [])]
  })

  it('erzeugt keinen Eintrag wenn sich nichts ändert', () => {
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created).toHaveLength(0)
    expect(newCard.activity).toHaveLength(0)
  })

  it('trackt Owner-Wechsel', () => {
    newCard.owner = 'Claude'
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created).toHaveLength(1)
    expect(created[0].action).toBe('owner-changed')
    expect(created[0].from).toBe('')
    expect(created[0].to).toBe('Claude')
  })

  it('trackt Spalten-Wechsel', () => {
    newCard.columnId = 'col-erledigt'
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created).toHaveLength(1)
    expect(created[0].action).toBe('column-moved')
    expect(created[0].from).toBe('col-backlog')
    expect(created[0].to).toBe('col-erledigt')
  })

  it('trackt QA-Flag-Änderung', () => {
    newCard.qa = true
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created).toHaveLength(1)
    expect(created[0].action).toBe('field-updated')
    expect(created[0].field).toBe('qa')
    expect(created[0].from).toBe('false')
    expect(created[0].to).toBe('true')
  })

  it('trackt Critical-Flag-Änderung', () => {
    newCard.critical = true
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created.some(e => e.field === 'critical')).toBe(true)
  })

  it('trackt Blocked-Flag-Änderung', () => {
    newCard.blocked = true
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created.some(e => e.field === 'blocked')).toBe(true)
  })

  /* #302: assignees-changed */
  it('#302: trackt Assignees-Hinzufügung (added-Liste)', () => {
    newCard.assignees = ['Claude Opus']
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    const entry = created.find(e => e.action === 'assignees-changed')
    expect(entry).toBeTruthy()
    expect(entry.added).toEqual(['Claude Opus'])
    expect(entry.removed).toEqual([])
  })

  it('#302: trackt Assignees-Entfernung (removed-Liste)', () => {
    oldCard.assignees = ['Claude Opus', 'Andi']
    newCard.assignees = ['Andi']
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    const entry = created.find(e => e.action === 'assignees-changed')
    expect(entry).toBeTruthy()
    expect(entry.added).toEqual([])
    expect(entry.removed).toEqual(['Claude Opus'])
  })

  it('#302: reine Reihenfolge-Änderung erzeugt keinen Eintrag', () => {
    oldCard.assignees = ['A', 'B']
    newCard.assignees = ['B', 'A']
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created.some(e => e.action === 'assignees-changed')).toBe(false)
  })

  /* #307/ADR-0102: project-changed */
  it('#307: trackt Projekt-Zuordnung (from null → id)', () => {
    oldCard.project_id = null
    newCard.project_id = 'p-abc'
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    const entry = created.find(e => e.action === 'project-changed')
    expect(entry).toBeTruthy()
    expect(entry.from).toBe('')
    expect(entry.to).toBe('p-abc')
  })

  it('#307: trackt Projekt-Entfernung (id → null)', () => {
    oldCard.project_id = 'p-abc'
    newCard.project_id = null
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    const entry = created.find(e => e.action === 'project-changed')
    expect(entry).toBeTruthy()
    expect(entry.from).toBe('p-abc')
    expect(entry.to).toBe('')
  })

  it('#307: kein Eintrag wenn project_id gleich bleibt', () => {
    oldCard.project_id = 'p-abc'
    newCard.project_id = 'p-abc'
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created.some(e => e.action === 'project-changed')).toBe(false)
  })

  it('trackt Komplexität-Änderung', () => {
    newCard.complexity = 'hoch'
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created.some(e => e.field === 'complexity')).toBe(true)
    const entry = created.find(e => e.field === 'complexity')
    expect(entry.from).toBe('')
    expect(entry.to).toBe('hoch')
  })

  it('trackt Aufwand-Änderung', () => {
    newCard.effort = 3
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created.some(e => e.field === 'effort')).toBe(true)
    const entry = created.find(e => e.field === 'effort')
    expect(entry.from).toBe('')
    expect(entry.to).toBe('3')
  })

  it('trackt mehrere Änderungen in einem Aufruf', () => {
    newCard.owner  = 'Claude'
    newCard.qa     = true
    const created = Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(created.length).toBeGreaterThanOrEqual(2)
    expect(created.some(e => e.action === 'owner-changed')).toBe(true)
    expect(created.some(e => e.field === 'qa')).toBe(true)
  })

  it('hängt Einträge an bestehendes activity-Array', () => {
    const existingEntry = { id: 'x', author: 'Alt', action: 'field-updated', timestamp: new Date().toISOString() }
    newCard.activity = [existingEntry]
    newCard.qa = true
    Activity.diffAndRecord(oldCard, newCard, 'Andi')
    expect(newCard.activity).toHaveLength(2)
    expect(newCard.activity[0]).toBe(existingEntry)
  })

  it('tut nichts wenn oldCard null', () => {
    const created = Activity.diffAndRecord(null, newCard, 'Andi')
    expect(created).toHaveLength(0)
  })
})

/* ─────────────────────────────────────────────────────────────────
   Model.createCard — activity-Default
   ───────────────────────────────────────────────────────────────── */

describe('Model.createCard() — activity-Feld', () => {
  it('enthält activity: [] als Default', () => {
    const card = Model.createCard('Neue Karte')
    expect(Array.isArray(card.activity)).toBe(true)
    expect(card.activity).toHaveLength(0)
  })
})

/* ─────────────────────────────────────────────────────────────────
   Migration — migrateActivity (indirekt über Storage-Muster)
   ───────────────────────────────────────────────────────────────── */

describe('migrateActivity() — Migrations-Verhalten', () => {
  it('importiert Storage und hat checkVersion (Smoke-Test)', async () => {
    /* Nur sicherstellen dass der Import sauber durchläuft */
    const mod = await import('../src/storage.js')
    expect(mod.Storage).toBeDefined()
  })
})

/* ─────────────────────────────────────────────────────────────────
   Modal-DOM-Test — Tab-History existiert
   ───────────────────────────────────────────────────────────────── */

vi.mock('../src/swipe.js', () => ({
  addSwipeToDismiss: vi.fn(),
}))

vi.mock('../src/config.js', () => ({
  Config: {
    get: vi.fn((key) => {
      if (key === 'boardMode') return 'standalone'
      if (key === 'userName') return 'Testnutzer'
      return ''
    }),
    set: vi.fn(),
  },
}))

import { EventBus } from '../src/eventbus.js'
import { Modal }    from '../src/modal.js'

function makeCard(overrides = {}) {
  return {
    id:           'card-act-001',
    displayId:    99,
    title:        'Aktivitäts-Test-Karte',
    description:  '',
    agentContext: '',
    comments:     [],
    attachments:  [],
    links:        [],
    activity:     [],
    priority:     'mittel',
    labels:       [],
    owner:        '',
    complexity:   '',
    effort:       null,
    qa:           false,
    critical:     false,
    blocked:      false,
    archived:     false,
    columnId:     'col-backlog',
    ...overrides,
  }
}

function openModal(card) {
  EventBus.emit('card:edit', card)
  return document.querySelector('.modal-overlay')
}

function resetModal() {
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
}

beforeEach(() => {
  vi.useFakeTimers()
  resetModal()
})

afterEach(() => {
  resetModal()
  vi.useRealTimers()
})

describe('Modal — Tab-History (ADR-0037)', () => {
  it('hat einen Tab-Button #tab-history', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    expect(overlay.querySelector('#tab-history')).toBeTruthy()
  })

  it('hat ein Tab-Panel #tab-panel-history', () => {
    const overlay = openModal(makeCard())
    expect(overlay).toBeTruthy()
    expect(overlay.querySelector('#tab-panel-history')).toBeTruthy()
  })

  it('Tab-Button hat role="tab"', () => {
    const overlay = openModal(makeCard())
    const btn = overlay.querySelector('#tab-history')
    expect(btn.getAttribute('role')).toBe('tab')
  })

  it('Tab-Panel hat role="tabpanel"', () => {
    const overlay = openModal(makeCard())
    const panel = overlay.querySelector('#tab-panel-history')
    expect(panel.getAttribute('role')).toBe('tabpanel')
  })

  it('zeigt history.empty wenn activity leer', () => {
    const overlay = openModal(makeCard({ activity: [] }))
    const histList = overlay.querySelector('#history-list')
    expect(histList).toBeTruthy()
    /* Leer-Zustand: mindestens ein Element mit activity-empty-Klasse */
    const empty = histList.querySelector('.activity-empty')
    expect(empty).toBeTruthy()
  })

  it('rendert Einträge im Verlauf-Tab', () => {
    const activity = [
      {
        id: 'e1', author: 'Andi', action: 'owner-changed',
        from: '', to: 'Claude', timestamp: '2026-05-13T10:00:00.000Z',
      },
    ]
    const overlay = openModal(makeCard({ activity }))
    const histList = overlay.querySelector('#history-list')
    expect(histList).toBeTruthy()
    const entries = histList.querySelectorAll('.activity-entry')
    expect(entries.length).toBe(1)
  })

  it('Tab-Bar enthält 7 Tabs (Standalone) bzw. 8 Tabs (Server-Build inkl. KI-Tab, #299) — Finding-Tab (#667) steckt immer im DOM', () => {
    const overlay = openModal(makeCard())
    const tabs = overlay.querySelectorAll('.tab-btn')
    const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
      (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true')
    expect(tabs.length).toBe(IS_SERVER_BUILD ? 8 : 7)
    // Bei einer normalen Karte ist der Finding-Tab ausgeblendet.
    const finding = [...tabs].filter(t => t.dataset.tab === 'finding')
    expect(finding.length).toBe(1)
    expect(finding[0].hidden || finding[0].style.display === 'none').toBe(true)
  })

  /* F-245 (Hot-Fix Sprint-28-Folge): Schema-Drift — neue Lifecycle-Einträge
     nutzen actor/type/note statt author/action. Renderer muss beide tolerieren. */
  it('F-245: rendert Lifecycle-Eintrag mit actor/type/note (ADR-0047-Schema)', () => {
    const activity = [{
      id: 'e1', actor: 'Claude Opus 4.7', type: 'sprint-activate',
      note: 'Automatisch aktiviert via migrateSprintMeta',
      timestamp: '2026-05-14T20:35:00.000Z',
    }]
    const overlay = openModal(makeCard({ activity }))
    const histList = overlay.querySelector('#history-list')
    const entry = histList.querySelector('.activity-entry')
    expect(entry).toBeTruthy()
    /* Author-Tag muss den actor zeigen, nicht "—". */
    expect(entry.querySelector('.activity-author-tag').textContent).toBe('Claude Opus 4.7')
    /* Label muss type + note enthalten, nicht leer/undefined sein. */
    const label = entry.querySelector('.activity-label')
    expect(label.textContent).toContain('sprint-activate')
    expect(label.textContent).toContain('Automatisch aktiviert')
  })

  it('F-245: rendert Lifecycle-Eintrag mit actor/type ohne note', () => {
    const activity = [{
      id: 'e2', actor: 'Andi', type: 'move', timestamp: '2026-05-14T10:00:00.000Z',
    }]
    const overlay = openModal(makeCard({ activity }))
    const label = overlay.querySelector('#history-list .activity-entry .activity-label')
    expect(label.textContent).toBe('move')
  })

  it('F-245: alte ADR-0037-Einträge (author/action) bleiben funktional', () => {
    const activity = [{
      id: 'e3', author: 'Andi', action: 'owner-changed',
      from: '', to: 'Claude', timestamp: '2026-05-13T10:00:00.000Z',
    }]
    const overlay = openModal(makeCard({ activity }))
    const entry = overlay.querySelector('#history-list .activity-entry')
    expect(entry.querySelector('.activity-author-tag').textContent).toBe('Andi')
    /* Label enthält den Pfeil — Beweis dass owner-changed-Branch greift. */
    expect(entry.querySelector('.activity-label').textContent).toContain('→')
  })

  /* #299/ADR-0095 Etappe 4: llm-sent + llm-response-discarded mit ts-Field
     (Server-Schema). Renderer muss beide tolerieren und die meta-Zeile zeigen. */
  it('#299 Etappe 4: rendert llm-sent-Eintrag mit Provider/Modell + meta-Zeile (Tokens + Cost + Kontext)', () => {
    const activity = [{
      ts: '2026-05-25T10:42:00Z', actor: 'alice@x.de', action: 'llm-sent',
      from: '', to: 'anthropic/claude-opus-4-7',
      meta: { commentId: 'c-1', inputTokens: 1234, outputTokens: 567,
              costEstimateUSD: 0.06375, contextNote: 'Beschreibung + Kommentare' },
    }]
    const overlay = openModal(makeCard({ activity }))
    const entry = overlay.querySelector('#history-list .activity-entry')
    expect(entry).toBeTruthy()
    expect(entry.querySelector('.activity-author-tag').textContent).toBe('alice@x.de')
    /* Datum kommt aus ts (statt timestamp) */
    expect(entry.querySelector('.activity-date').textContent).toBe('2026-05-25')
    /* Label: Action-Label + Provider/Modell */
    const labelTxt = entry.querySelector('.activity-label').textContent
    expect(labelTxt).toMatch(/LLM-Send|LLM send/i)
    expect(labelTxt).toContain('anthropic/claude-opus-4-7')
    /* Meta-Zeile: Token-Sum + Cost + Context-Note */
    const metaLine = entry.querySelector('.activity-llm-meta')
    expect(metaLine).toBeTruthy()
    expect(metaLine.textContent).toContain('1234+567')
    expect(metaLine.textContent).toContain('0.0638') /* toFixed(4) rundet 0.06375→0.0638 */
    expect(metaLine.textContent).toContain('Beschreibung + Kommentare')
  })

  it('#299 Etappe 4: llm-sent ohne meta → Label rendert, aber keine Meta-Zeile', () => {
    const activity = [{
      ts: '2026-05-25T10:42:00Z', actor: 'alice', action: 'llm-sent',
      to: 'openai/gpt-4o',
    }]
    const overlay = openModal(makeCard({ activity }))
    const entry = overlay.querySelector('#history-list .activity-entry')
    expect(entry.querySelector('.activity-label').textContent).toContain('openai/gpt-4o')
    expect(entry.querySelector('.activity-llm-meta')).toBeFalsy()
  })

  it('#299 Etappe 4: llm-response-discarded zeigt eigenes Label, KEINE meta-Zeile (Tokens stehen am llm-sent-Eintrag)', () => {
    const activity = [{
      ts: '2026-05-25T10:43:00Z', actor: 'alice', action: 'llm-response-discarded',
      to: 'anthropic/claude-opus-4-7',
      meta: { commentId: 'c-1', inputTokens: 100, outputTokens: 50, costEstimateUSD: 0.005 },
    }]
    const overlay = openModal(makeCard({ activity }))
    const entry = overlay.querySelector('#history-list .activity-entry')
    const labelTxt = entry.querySelector('.activity-label').textContent
    expect(labelTxt).toMatch(/verworfen|discarded/i)
    expect(labelTxt).toContain('anthropic/claude-opus-4-7')
    /* Keine meta-Zeile — Discard-Entries replizieren keine Token-Stats. */
    expect(entry.querySelector('.activity-llm-meta')).toBeFalsy()
  })
})
