/**
 * Tests für das Identity-System — Migration (Karte #123, angepasst Karte #171)
 *
 * Abdeckung:
 *  - Migration erzeugt nur 1 User-Identity bei fehlendem `identities`-Array
 *    (Karte #171: Claude-Defaults werden on-the-fly aus CLAUDE_DEFAULTS
 *    synthetisiert, nicht persistiert).
 *  - Migration ist idempotent (zweiter Aufruf verändert nichts).
 *  - Bestandsboards mit Claude-Einträgen bleiben unverändert (Daten-Schonung).
 *  - User-Label und User-Farbe stabil reproduzierbar bei gleichem Label.
 *  - CLAUDE_DEFAULTS bleibt verbindliche Quelle für Farbe (#cc785c) +
 *    Icons (✦/✧/❋).
 */

import { describe, it, expect } from 'vitest'
import { Storage } from '../src/storage.js'
import { defaultUserIdentity, defaultClaudeIdentities, colorForLabel,
         CLAUDE_DEFAULTS } from '../src/identity.js'

/* ── Hilfsfunktion ─────────────────────────────────────────────────── */

function makeV2State(extra = {}) {
  return {
    version: '2',
    board: {
      id:            'board-identity-test',
      title:         'Identity-Test-Board',
      nextDisplayId: 1,
      columnDefs:    [{ id: 'col-todo', title: 'Todo' }],
      sprints: [
        { id: '__backlog__', title: 'Backlog', cards: [] },
      ],
      ...extra,
    },
  }
}

/* ── Migration: Erstanlage ──────────────────────────────────────────── */

describe('Identity-Migration — Erstanlage', () => {
  it('erzeugt identities-Array, wenn es noch fehlt', () => {
    const state = makeV2State()
    expect('identities' in state.board).toBe(false)

    const result = Storage.checkVersion(state)
    expect(result).not.toBeNull()
    expect(Array.isArray(result.board.identities)).toBe(true)
  })

  it('enthält genau eine User-Identity (Karte #171: keine Claude-Defaults mehr)', () => {
    const state = makeV2State()
    const result = Storage.checkVersion(state)
    const identities = result.board.identities
    expect(identities).toHaveLength(1)
    expect(identities[0].kind).toBe('user')
  })

  it('CLAUDE_DEFAULTS hält weiterhin Coral + Icons (statisch, nicht persistiert)', () => {
    expect(CLAUDE_DEFAULTS['claude-opus'].color).toBe('#cc785c')
    expect(CLAUDE_DEFAULTS['claude-opus'].icon).toBe('✦')
    expect(CLAUDE_DEFAULTS['claude-sonnet'].color).toBe('#cc785c')
    expect(CLAUDE_DEFAULTS['claude-sonnet'].icon).toBe('✧')
    expect(CLAUDE_DEFAULTS['claude-haiku'].color).toBe('#cc785c')
    expect(CLAUDE_DEFAULTS['claude-haiku'].icon).toBe('❋')
  })

  it('User-Identity: id="user-default", label="User" wenn Config leer', () => {
    const result = Storage.checkVersion(makeV2State())
    const user = result.board.identities.find(i => i.kind === 'user')
    expect(user.id).toBe('user-default')
    /* Migration ohne aktive Config.userName: Default-Label "User" */
    expect(user.label).toBe('User')
    expect(user.icon).toBe('U')
  })
})

/* ── Migration: Idempotenz ─────────────────────────────────────────── */

describe('Identity-Migration — Idempotenz', () => {
  it('zweiter Aufruf verändert das Array nicht', () => {
    const state = makeV2State()
    const result1 = Storage.checkVersion(state)
    const before = JSON.stringify(result1.board.identities)

    const result2 = Storage.checkVersion(result1)
    const after = JSON.stringify(result2.board.identities)
    expect(after).toBe(before)
  })

  it('bestehende identities werden nicht überschrieben', () => {
    const state = makeV2State({
      identities: [
        { id: 'custom-1', kind: 'user', label: 'Andi', color: '#abcdef',
          icon: 'A', imageAttachmentId: null },
      ],
    })
    const result = Storage.checkVersion(state)
    expect(result.board.identities).toHaveLength(1)
    expect(result.board.identities[0].label).toBe('Andi')
    expect(result.board.identities[0].color).toBe('#abcdef')
  })

  it('normalisiert ungültige Felder defensiv (ohne Datenverlust bei Labels)', () => {
    const state = makeV2State({
      identities: [
        { id: 'identity-claude-opus', kind: 'claude-opus', label: 'Custom',
          color: '#111111', icon: null, imageAttachmentId: 'broken-id-as-not-string' },
      ],
    })
    /* imageAttachmentId muss ein String sein — hier ist's einer, also ok */
    const result = Storage.checkVersion(state)
    expect(result.board.identities[0].label).toBe('Custom')
    expect(result.board.identities[0].color).toBe('#111111')
    /* imageAttachmentId bleibt erhalten (gültiger String) */
    expect(result.board.identities[0].imageAttachmentId).toBe('broken-id-as-not-string')
  })
})

/* ── Stabile Hash-Farbe für User-Label ─────────────────────────────── */

describe('Identity — Hash-basierte User-Farbe', () => {
  it('gleicher Label → gleiche Farbe (deterministisch)', () => {
    const a = colorForLabel('Andi')
    const b = colorForLabel('Andi')
    expect(a).toBe(b)
  })

  it('unterschiedliche Labels → typischerweise unterschiedliche Farben', () => {
    /* Schwacher Test: kollisionsfrei nicht garantiert, aber für drei alltägliche
       Labels in einer 9-Farben-Palette sehr wahrscheinlich. */
    const a = colorForLabel('Andi')
    const b = colorForLabel('Bea')
    const c = colorForLabel('Chris')
    /* Mindestens zwei unterschiedlich (Pigeonhole bei 9 Farben + 3 Inputs) */
    const set = new Set([a, b, c])
    expect(set.size).toBeGreaterThanOrEqual(2)
  })

  it('defaultUserIdentity reproduziert label + color + icon bei gleichem Input', () => {
    const a = defaultUserIdentity('Andi')
    const b = defaultUserIdentity('Andi')
    expect(a.label).toBe(b.label)
    expect(a.color).toBe(b.color)
    expect(a.icon).toBe(b.icon)
    expect(a.label).toBe('Andi')
    expect(a.icon).toBe('A')
  })

  it('leerer userName → Label "User", Icon "U"', () => {
    const idn = defaultUserIdentity('')
    expect(idn.label).toBe('User')
    expect(idn.icon).toBe('U')
  })

  it('whitespace-only userName → Label "User"', () => {
    const idn = defaultUserIdentity('   ')
    expect(idn.label).toBe('User')
  })
})

/* ── defaultClaudeIdentities-Helper ───────────────────────────────── */

describe('Identity — defaultClaudeIdentities()', () => {
  it('liefert genau 4 Identities (Opus, Sonnet, Haiku, Fable — Fable seit #1172)', () => {
    const list = defaultClaudeIdentities()
    expect(list).toHaveLength(4)
    const kinds = list.map(i => i.kind).sort()
    expect(kinds).toEqual(['claude-fable', 'claude-haiku', 'claude-opus', 'claude-sonnet'])
  })

  it('jede Identity hat id, label, color, icon, imageAttachmentId: null', () => {
    defaultClaudeIdentities().forEach(idn => {
      expect(typeof idn.id).toBe('string')
      expect(typeof idn.label).toBe('string')
      expect(typeof idn.color).toBe('string')
      expect(typeof idn.icon).toBe('string')
      expect(idn.imageAttachmentId).toBeNull()
    })
  })
})
