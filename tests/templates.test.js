/* @vitest-environment happy-dom */
/* Unit-Tests: Karten-Templates (ADR-0040, Karte #65)
   - Migration: checkVersion ergänzt fehlende board.templates
   - Anwenden: neue Karte mit neuer UUID, übernimmt Skelett-Felder
   - Export-Ausschluss: KI-Kontext enthält keine Template-Karten
   - UI: Template-Dropdown erscheint nur wenn templates.length > 0
*/

import { beforeAll, beforeEach, describe, it, expect, vi } from 'vitest'
import { Storage }  from '../src/storage.js'
import { Model }    from '../src/model.js'
import { Export }   from '../src/export.js'
import { I18n }     from '../src/i18n.js'

beforeAll(() => I18n.setLang('de'))

// ---------------------------------------------------------------------------
// Hilfsfunktionen
// ---------------------------------------------------------------------------

function makeCard(overrides = {}) {
  return {
    id: `card-${Math.random().toString(36).slice(2)}`,
    displayId: 1,
    title: 'Test-Karte',
    description: '',
    priority: 'mittel',
    labels: [],
    owner: '',
    complexity: '',
    effort: null,
    qa: false,
    critical: false,
    blocked: false,
    comments: [],
    columnId: 'col-backlog',
    agentContext: '',
    links: [],
    attachments: [],
    activity: [],
    archived: false,
    dueDate: null,
    startDate: null,
    coverAttachmentId: null,
    ...overrides,
  }
}

function makeSprint(id, title, cards = []) {
  return { id, title, cards }
}

function makeMinimalState(overrides = {}) {
  return {
    version: '2',
    board: {
      id: 'board-test',
      title: 'Test-Board',
      nextDisplayId: 10,
      columnDefs: [
        { id: 'col-backlog',   title: 'Backlog'   },
        { id: 'col-erledigt',  title: 'Erledigt'  },
      ],
      sprints: overrides.sprints ?? [makeSprint('__backlog__', 'Backlog', [])],
      ...(overrides.templates !== undefined ? { templates: overrides.templates } : {}),
      background: null,
      attachments: [],
    },
  }
}

// ---------------------------------------------------------------------------
// Hilfsfunktion: KI-Kontext abfangen (analog export.test.js)
// ---------------------------------------------------------------------------

function buildKIKontext(state) {
  const origBlob    = globalThis.Blob
  const origURL     = globalThis.URL
  let captured = null
  globalThis.Blob = class MockBlob {
    constructor(parts) { captured = parts.join('') }
  }
  globalThis.URL = { createObjectURL: () => 'blob:mock', revokeObjectURL: () => {} }
  const origCreateElement = document.createElement.bind(document)
  const mockA = { href: '', download: '', click: () => {} }
  document.createElement = (tag) => tag === 'a' ? mockA : origCreateElement(tag)

  Export.toKIKontextDownload(state)

  globalThis.Blob = origBlob
  globalThis.URL  = origURL
  document.createElement = origCreateElement
  return captured ?? ''
}

// ---------------------------------------------------------------------------
// Gruppe 1: Migration — board.templates sicherstellen
// ---------------------------------------------------------------------------

describe('Migration — board.templates', () => {
  it('fügt leeres templates-Array hinzu wenn Feld fehlt (Version 2)', () => {
    const raw = makeMinimalState()
    // templates bewusst NICHT im State
    delete raw.board.templates
    const migrated = Storage.checkVersion(raw)
    expect(Array.isArray(migrated.board.templates)).toBe(true)
    expect(migrated.board.templates.length).toBe(0)
  })

  it('lässt bestehendes templates-Array unverändert (idempotent)', () => {
    const tpl = { id: 'tpl-1', isTemplate: true, title: 'Bug', description: '', labels: [], owner: '', complexity: '', effort: null }
    const raw = makeMinimalState({ templates: [tpl] })
    const migrated = Storage.checkVersion(raw)
    expect(migrated.board.templates.length).toBe(1)
    expect(migrated.board.templates[0].id).toBe('tpl-1')
  })

  it('Migration läuft auch für Version 1 (fehlende templates → leeres Array)', () => {
    /* Version-1-State muss eine columns-Struktur haben (migrateV1toV2) */
    const v1state = {
      version: '1',
      board: {
        id: 'board-v1',
        title: 'Alt-Board',
        columns: [{ id: 'col-a', title: 'Backlog', cards: [] }],
      },
    }
    const migrated = Storage.checkVersion(v1state)
    expect(Array.isArray(migrated?.board?.templates)).toBe(true)
  })

  // -------------------------------------------------------------------------
  // F-168: migrateTemplates prüfte bisher nur das Array selbst, nicht die
  // Einträge — ein Template ohne id oder ohne isTemplate:true landete
  // unverändert im Add-Dropdown und brach dort.
  // -------------------------------------------------------------------------

  it('repariert ein Template ohne id — bekommt eine neue, gültige id (F-168)', () => {
    const tplOhneId = { isTemplate: true, title: 'Ohne ID', description: '', labels: [], owner: '', complexity: '', effort: null }
    const raw = makeMinimalState({ templates: [tplOhneId] })
    const migrated = Storage.checkVersion(raw)
    expect(migrated.board.templates.length).toBe(1)
    expect(typeof migrated.board.templates[0].id).toBe('string')
    expect(migrated.board.templates[0].id.length).toBeGreaterThan(0)
    expect(migrated.board.templates[0].isTemplate).toBe(true)
  })

  it('repariert ein Template ohne isTemplate:true — Flag wird auf true gesetzt, id bleibt erhalten (F-168)', () => {
    const tplOhneFlag = { id: 'tpl-ohne-flag', title: 'Kein Flag', description: '', labels: [], owner: '', complexity: '', effort: null }
    const raw = makeMinimalState({ templates: [tplOhneFlag] })
    const migrated = Storage.checkVersion(raw)
    expect(migrated.board.templates.length).toBe(1)
    expect(migrated.board.templates[0].id).toBe('tpl-ohne-flag')
    expect(migrated.board.templates[0].isTemplate).toBe(true)
  })

  it('überspringt einen Nicht-Objekt-Eintrag im templates-Array, behält valide Einträge (F-168)', () => {
    const tplValide = { id: 'tpl-valide', isTemplate: true, title: 'Valide', description: '', labels: [], owner: '', complexity: '', effort: null }
    const raw = makeMinimalState({ templates: [null, tplValide, undefined] })
    const migrated = Storage.checkVersion(raw)
    expect(migrated.board.templates.length).toBe(1)
    expect(migrated.board.templates[0].id).toBe('tpl-valide')
  })
})

// ---------------------------------------------------------------------------
// Gruppe 2: Model.createTemplate — Skelett-Felder korrekt
// ---------------------------------------------------------------------------

describe('Model.createTemplate()', () => {
  it('erstellt ein Template mit isTemplate:true und neuer UUID', () => {
    const tpl = Model.createTemplate({ title: 'Bug-Report', description: 'Schritte…', labels: ['bug'], owner: 'Claude Sonnet', complexity: 'einfach', effort: 1 })
    expect(tpl.isTemplate).toBe(true)
    expect(tpl.id).toBeTruthy()
    expect(tpl.title).toBe('Bug-Report')
    expect(tpl.description).toBe('Schritte…')
    expect(tpl.labels).toEqual(['bug'])
    expect(tpl.owner).toBe('Claude Sonnet')
    expect(tpl.complexity).toBe('einfach')
    expect(tpl.effort).toBe(1)
  })

  it('enthält keine UUID-sensitiven oder Sprint-Felder (comments, attachments, columnId, displayId)', () => {
    const tpl = Model.createTemplate({ title: 'Feature' })
    expect(tpl.comments).toBeUndefined()
    expect(tpl.attachments).toBeUndefined()
    expect(tpl.columnId).toBeUndefined()
    expect(tpl.displayId).toBeUndefined()
    expect(tpl.activity).toBeUndefined()
  })

  it('verwendet bei leerem Input leere Standardwerte', () => {
    const tpl = Model.createTemplate()
    expect(tpl.title).toBe('')
    expect(tpl.description).toBe('')
    expect(tpl.labels).toEqual([])
    expect(tpl.effort).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// Gruppe 3: Anwenden — neue Karte aus Template mit neuer UUID
// ---------------------------------------------------------------------------

describe('Anwenden — Template-Felder auf neue Karte', () => {
  it('übernimmt description, labels, owner, complexity, effort aus Template', () => {
    const template = {
      title: 'Bug-Report',
      description: 'Schritte zur Reproduktion…',
      labels: ['bug', 'dringend'],
      owner: 'Claude Opus',
      complexity: 'hoch',
      effort: 4,
    }

    /* Simuliert das Verhalten aus main.js EventBus.on('card:quick-add') */
    const card = Model.createCard('Mein Ticket', 'col-backlog', 99)
    if (template) {
      card.description = template.description || ''
      card.labels      = Array.isArray(template.labels) ? [...template.labels] : []
      card.owner       = template.owner       || ''
      card.complexity  = template.complexity  || ''
      card.effort      = template.effort      ?? null
    }

    expect(card.title).toBe('Mein Ticket')
    expect(card.description).toBe('Schritte zur Reproduktion…')
    expect(card.labels).toEqual(['bug', 'dringend'])
    expect(card.owner).toBe('Claude Opus')
    expect(card.complexity).toBe('hoch')
    expect(card.effort).toBe(4)
    expect(card.isTemplate).toBeUndefined()
  })

  it('erzeugt eine neue UUID — nicht die Template-UUID', () => {
    const template = Model.createTemplate({ title: 'Spike' })
    const card     = Model.createCard(template.title, 'col-backlog', 1)
    // isTemplate wird beim Anwenden NICHT übernommen
    expect(card.id).not.toBe(template.id)
    expect(card.isTemplate).toBeUndefined()
  })

  it('created-Datum der neuen Karte ist heute', () => {
    const today = new Date().toISOString().slice(0, 10)
    const card  = Model.createCard('Neue Karte', 'col-backlog', 1)
    expect(card.created).toBe(today)
  })
})

// ---------------------------------------------------------------------------
// Gruppe 4: KI-Kontext-Export — Templates ausgeschlossen
// ---------------------------------------------------------------------------

describe('KI-Kontext-Export — Templates ausgeschlossen', () => {
  it('enthält keine Karte mit isTemplate:true', () => {
    const normalCard   = makeCard({ title: 'Normale Karte', columnId: 'col-backlog' })
    const templateCard = makeCard({ title: 'Template-Karte', columnId: 'col-backlog', isTemplate: true })
    const state = {
      version: '2',
      board: {
        title: 'Test-Board',
        columnDefs: [{ id: 'col-backlog', title: 'Backlog' }],
        sprints: [makeSprint('sprint-1', 'Sprint 1', [normalCard, templateCard])],
      },
    }
    const output = buildKIKontext(state)
    expect(output).toContain('Normale Karte')
    expect(output).not.toContain('Template-Karte')
  })

  it('normaler Ablauf bleibt unberührt wenn keine Templates im Sprint', () => {
    const card  = makeCard({ title: 'Echte Karte', columnId: 'col-backlog' })
    const state = {
      version: '2',
      board: {
        title: 'Board',
        columnDefs: [{ id: 'col-backlog', title: 'Backlog' }],
        sprints: [makeSprint('sprint-1', 'Sprint 1', [card])],
      },
    }
    const output = buildKIKontext(state)
    expect(output).toContain('Echte Karte')
  })
})

// ---------------------------------------------------------------------------
// Gruppe 5: createBoard — templates-Feld vorhanden
// ---------------------------------------------------------------------------

describe('Model.createBoard() — templates-Feld', () => {
  it('neues Board hat leeres templates-Array', () => {
    const board = Model.createBoard('Neues Board')
    expect(Array.isArray(board.templates)).toBe(true)
    expect(board.templates.length).toBe(0)
  })
})
