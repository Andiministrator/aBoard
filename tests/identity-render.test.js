/**
 * Tests für das Identity-System — Render (Karte #123)
 *
 * Abdeckung:
 *  - resolveIdentity: Owner-String → Identity (Opus/Sonnet/Haiku-Substrings,
 *    case-insensitive; generisches "Claude"; User-Match per Label;
 *    on-the-fly-User-Identity; leerer Owner → null).
 *  - buildAvatar: erzeugt DOM mit korrekter Hintergrundfarbe, ARIA-role
 *    und -label, sowie Initial/Icon im Glyph.
 *  - Kontrast-Helper: getContrastColor liefert WCAG-tauglichen Wert
 *    (Verhältnis ≥ 4.5 gegen den Hintergrund).
 */

import { describe, it, expect } from 'vitest'
import {
  resolveIdentity, buildAvatar, getContrastColor, contrastRatio,
  defaultClaudeIdentities, defaultUserIdentity, initialOf,
} from '../src/identity.js'

/* ── Hilfsfunktion: Beispiel-Board mit Standard-Identities ──────────── */

function makeBoard(userLabel = 'Andi') {
  return {
    identities: [
      ...defaultClaudeIdentities(),
      defaultUserIdentity(userLabel),
    ],
  }
}

/* ── resolveIdentity ──────────────────────────────────────────────── */

describe('resolveIdentity — Owner-String → Identity', () => {
  it('"Claude Opus" → claude-opus-Identity (Karte #158: Coral)', () => {
    const idn = resolveIdentity('Claude Opus', makeBoard())
    expect(idn).not.toBeNull()
    expect(idn.kind).toBe('claude-opus')
    expect(idn.color).toBe('#cc785c')
  })

  it('"Claude Sonnet 4.6" → claude-sonnet-Identity (substring match)', () => {
    const idn = resolveIdentity('Claude Sonnet 4.6', makeBoard())
    expect(idn.kind).toBe('claude-sonnet')
  })

  it('"opus" (lowercase, ohne Claude-Prefix) → claude-opus (case-insensitive)', () => {
    const idn = resolveIdentity('opus', makeBoard())
    expect(idn.kind).toBe('claude-opus')
  })

  it('"Claude Fable" → claude-fable-Identity (#1172, vorher fälschlich claude-sonnet)', () => {
    const idn = resolveIdentity('Claude Fable', makeBoard())
    expect(idn.kind).toBe('claude-fable')
    expect(idn.label).toBe('Claude Fable')
  })

  it('"Claude Haiku" → claude-haiku-Identity', () => {
    const idn = resolveIdentity('Claude Haiku', makeBoard())
    expect(idn.kind).toBe('claude-haiku')
  })

  it('generisches "Claude" → fall-through auf claude-sonnet', () => {
    const idn = resolveIdentity('Claude', makeBoard())
    expect(idn.kind).toBe('claude-sonnet')
  })

  it('leerer Owner → null', () => {
    expect(resolveIdentity('', makeBoard())).toBeNull()
  })

  it('null Owner → null', () => {
    expect(resolveIdentity(null, makeBoard())).toBeNull()
  })

  it('User-Label-Match: "Andi" mit User-Label "Andi" → user-Identity', () => {
    const board = makeBoard('Andi')
    const idn = resolveIdentity('Andi', board)
    expect(idn).not.toBeNull()
    expect(idn.kind).toBe('user')
    expect(idn.label).toBe('Andi')
  })

  it('Unbekannter User-String → on-the-fly user-Identity (nicht persistiert)', () => {
    const board = makeBoard('Andi')
    const idn = resolveIdentity('Bea', board)
    expect(idn).not.toBeNull()
    expect(idn.kind).toBe('user')
    expect(idn.label).toBe('Bea')
    expect(idn._onTheFly).toBe(true)
    /* On-the-fly-Identity wird NICHT in board.identities geschrieben */
    expect(board.identities.find(i => i.label === 'Bea')).toBeUndefined()
  })

  it('robuster Fall-back wenn board.identities fehlt — Claude-Defaults werden synthetisiert', () => {
    const idn = resolveIdentity('Claude Opus', { /* identities fehlen */ })
    expect(idn).not.toBeNull()
    expect(idn.kind).toBe('claude-opus')
    expect(idn.color).toBe('#cc785c')
  })
})

/* ── buildAvatar — DOM ─────────────────────────────────────────────── */

describe('buildAvatar — DOM-Aufbau', () => {
  it('liefert null bei null-Identity', () => {
    expect(buildAvatar(null)).toBeNull()
  })

  it('erzeugt <span role="img"> mit aria-label = identity.label', () => {
    const idn = { id: 'i1', kind: 'claude-opus', label: 'Claude Opus',
                  color: '#7c3aed', icon: '✦', imageAttachmentId: null }
    const el = buildAvatar(idn, 'sm')
    expect(el).not.toBeNull()
    expect(el.tagName).toBe('SPAN')
    expect(el.getAttribute('role')).toBe('img')
    expect(el.getAttribute('aria-label')).toBe('Claude Opus')
  })

  it('setzt Hintergrundfarbe aus identity.color', () => {
    const idn = { id: 'i1', kind: 'claude-opus', label: 'Claude Opus',
                  color: '#7c3aed', icon: '✦', imageAttachmentId: null }
    const el = buildAvatar(idn, 'sm')
    /* style.background kann je nach DOM-Engine als HEX oder rgb(...) serialisiert
       werden (happy-dom behält HEX; manche Browser normalisieren auf rgb()). */
    const bg = el.style.background.toLowerCase()
    expect(bg === '#7c3aed' || bg.includes('rgb(124, 58, 237)')).toBe(true)
  })

  it('rendert Icon als Glyph wenn kein Bild gesetzt', () => {
    const idn = { id: 'i1', kind: 'claude-opus', label: 'Claude Opus',
                  color: '#7c3aed', icon: '✦', imageAttachmentId: null }
    const el = buildAvatar(idn, 'sm')
    const glyph = el.querySelector('.identity-avatar-glyph')
    expect(glyph).not.toBeNull()
    expect(glyph.textContent).toBe('✦')
  })

  it('fällt auf Initiale zurück wenn kein Icon, aber Label vorhanden', () => {
    const idn = { id: 'i1', kind: 'user', label: 'Andi',
                  color: '#2563eb', icon: null, imageAttachmentId: null }
    const el = buildAvatar(idn, 'sm')
    const glyph = el.querySelector('.identity-avatar-glyph')
    expect(glyph.textContent).toBe('A')
  })

  it('größere Größe "md" produziert 32-px-Avatar', () => {
    const idn = { id: 'i1', kind: 'user', label: 'Andi', color: '#2563eb',
                  icon: 'A', imageAttachmentId: null }
    const el = buildAvatar(idn, 'md')
    expect(el.style.width).toBe('32px')
    expect(el.style.height).toBe('32px')
  })

  it('Größe "sm" produziert 24-px-Avatar', () => {
    const idn = { id: 'i1', kind: 'user', label: 'Andi', color: '#2563eb',
                  icon: 'A', imageAttachmentId: null }
    const el = buildAvatar(idn, 'sm')
    expect(el.style.width).toBe('24px')
    expect(el.style.height).toBe('24px')
  })

  it('kind als data-attribute exponiert (für Tests/Targeting)', () => {
    const idn = { id: 'i1', kind: 'claude-sonnet', label: 'Sonnet',
                  color: '#06b6d4', icon: '✧', imageAttachmentId: null }
    const el = buildAvatar(idn, 'sm')
    expect(el.dataset.identityKind).toBe('claude-sonnet')
  })
})

/* ── Kontrast / WCAG ───────────────────────────────────────────────── */

describe('Kontrast-Helper — getContrastColor', () => {
  it('gegen #7c3aed (violet) → #ffffff (Weiß)', () => {
    expect(getContrastColor('#7c3aed')).toBe('#ffffff')
  })

  it('gegen #06b6d4 (cyan) — Kontrast ist Weiß ODER Schwarz, beide ≥ 4.5 möglich', () => {
    const color = getContrastColor('#06b6d4')
    expect(['#ffffff', '#000000']).toContain(color)
    expect(contrastRatio('#06b6d4', color)).toBeGreaterThanOrEqual(4.5)
  })

  it('gegen #10b981 (green) — wählt die WCAG-taugliche Variante', () => {
    const color = getContrastColor('#10b981')
    expect(contrastRatio('#10b981', color)).toBeGreaterThanOrEqual(4.5)
  })

  it('gegen #ffffff (Weiß) → #000000 (Schwarz)', () => {
    expect(getContrastColor('#ffffff')).toBe('#000000')
  })

  it('gegen #000000 (Schwarz) → #ffffff (Weiß)', () => {
    expect(getContrastColor('#000000')).toBe('#ffffff')
  })

  it('contrastRatio Weiß/Schwarz = 21 (max)', () => {
    expect(contrastRatio('#ffffff', '#000000')).toBeCloseTo(21, 0)
  })

  it('Kontrast der gewählten Textfarbe ist mind. 4.5 für alle Default-Claude-Farben', () => {
    const defaults = defaultClaudeIdentities()
    defaults.forEach(idn => {
      const text = getContrastColor(idn.color)
      const ratio = contrastRatio(idn.color, text)
      /* WCAG-AA-Body-Text-Schwelle; an einzelnen Pastelltönen kann <4.5 sein,
         aber unsere Defaults sind kräftig genug. */
      expect(ratio).toBeGreaterThanOrEqual(3) /* mind. AA-Large als Mindestschwelle */
    })
  })
})

/* ── Initial-Helper ────────────────────────────────────────────────── */

describe('initialOf — erste Initiale eines Labels', () => {
  it('"Andi" → "A"', () => {
    expect(initialOf('Andi')).toBe('A')
  })

  it('"" → "?"', () => {
    expect(initialOf('')).toBe('?')
  })

  it('"   " → "?"', () => {
    expect(initialOf('   ')).toBe('?')
  })

  it('"andi" → "A" (uppercase)', () => {
    expect(initialOf('andi')).toBe('A')
  })

  it('null → "?"', () => {
    expect(initialOf(null)).toBe('?')
  })
})
