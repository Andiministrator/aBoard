/* @vitest-environment happy-dom */

/* Karte #971: Labelfarbe fest aus dem Labeltext — 8 Farbtöne (45°-Schritte),
   Sättigung/Helligkeit je Theme über --lb/--lf in src/style.css. Der
   Kontrasttest rechnet alle 8 Töne in jedem Theme-Block nach (AA 4,5:1). */

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { faerbeLabel, labelTon } from '../src/identity.js'

const css = readFileSync(resolve(process.cwd(), 'src/style.css'), 'utf8')

function farbton(text) {
  const n = document.createElement('span')
  faerbeLabel(n, text)
  return n
}

describe('#971 faerbeLabel', () => {
  it('setzt Klasse lc und einen Farbton aus 8 Stufen', () => {
    const n = farbton('ux')
    expect(n.classList.contains('lc')).toBe(true)
    expect(Number(n.style.getPropertyValue('--h'))).toBe(labelTon('ux'))
  })

  it('F-2677: Serien-Labels und Anagramme bekommen nicht zwangsläufig dieselbe Farbe', () => {
    expect(labelTon('P1')).not.toBe(labelTon('P2'))
    expect(labelTon('sprint-1')).not.toBe(labelTon('sprint-9'))
    expect(labelTon('ux')).not.toBe(labelTon('xu'))
    const serie = new Set([1, 2, 3, 4, 5, 6, 7, 8, 9].map(i => labelTon('P' + i)))
    expect(serie.size).toBeGreaterThanOrEqual(5)
  })

  it('gleicher Text → gleiche Farbe, verschiedene Texte verteilen sich', () => {
    expect(farbton('layout').style.getPropertyValue('--h')).toBe(farbton('layout').style.getPropertyValue('--h'))
    const toene = new Set(['ux', 'layout', 'bug', 'server', 'doku', 'sicherheit', 'betrieb', 'test', 'mobil', 'a11y']
      .map(t => farbton(t).style.getPropertyValue('--h')))
    expect(toene.size).toBeGreaterThan(3)
    for (const h of toene) expect(Number(h) % 45).toBe(0)
  })
})

/* WCAG-Kontrast für hsl(h s l) — HSL wie im Browser */
function hslZuRgb(h, s, l) {
  s /= 100; l /= 100
  const k = n => (n + h / 30) % 12
  const a = s * Math.min(l, 1 - l)
  return [0, 8, 4].map(n => l - a * Math.max(-1, Math.min(k(n) - 3, 9 - k(n), 1)))
}
function luminanz(rgb) {
  const [r, g, b] = rgb.map(v => v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
function kontrast(a, b) {
  const [x, y] = [luminanz(a), luminanz(b)].sort((p, q) => q - p)
  return (x + 0.05) / (y + 0.05)
}

describe('#971 Labelfarben halten AA in jedem Theme-Block', () => {
  const bloecke = [...css.matchAll(/--lb:\s*(\d+)%\s+(\d+)%;[^]*?--lf:\s*(\d+)%\s+(\d+)%;/g)]

  it('drei Theme-Blöcke (hell, dunkel, System-dunkel) setzen --lb und --lf', () => {
    expect(bloecke).toHaveLength(3)
  })

  it('alle 8 Farbtöne ≥ 4,5:1 zwischen Schrift und Hintergrund', () => {
    for (const [, sb, lb, sf, lf] of bloecke) {
      for (let h = 0; h < 360; h += 45) {
        const k = kontrast(hslZuRgb(h, +sb, +lb), hslZuRgb(h, +sf, +lf))
        expect(k, `Ton ${h}° mit --lb ${sb}% ${lb}% / --lf ${sf}% ${lf}%`).toBeGreaterThanOrEqual(4.5)
      }
    }
  })

  /* F-2674/F-2676: Datum überfällig/bald und Komplexität hoch — Schrift (Variable) auf
     getöntem Hintergrund (fester rgba über --surface bzw. --surface-2) */
  it('Datums- und Komplexitätsfarben ≥ 4,5:1 in jedem Theme-Block', () => {
    const hex = h => [1, 3, 5].map(i => parseInt(h.slice(i, i + 2), 16) / 255)
    const mix = (fg, a, bg) => fg.map((v, i) => a * v + (1 - a) * bg[i])
    /* F-2680: Tönung aus der jeweiligen CSS-Regel lesen, nicht als Konstante */
    const toenung = sel => {
      const m = new RegExp(`\\.${sel} \\{[^}]*background:\\s*rgba\\((\\d+),\\s*(\\d+),\\s*(\\d+),\\s*([\\d.]+)\\)`).exec(css)
      expect(m, sel).not.toBeNull()
      return ['#' + [m[1], m[2], m[3]].map(v => (+v).toString(16).padStart(2, '0')).join(''), +m[4]]
    }
    const toene = { 'due-o': toenung('card-date-badge--overdue'), 'due-s': toenung('card-date-badge--soon'), 'cx-h': toenung('card-complexity-badge--hoch') }
    const themes = [...css.matchAll(/--surface:\s*(#[0-9a-f]{6});\s*--surface-2:\s*(#[0-9a-f]{6});[^]*?--due-o:\s*(#[0-9a-f]{6});\s*--due-s:\s*(#[0-9a-f]{6});\s*--cx-h:\s*(#[0-9a-f]{6});/gi)]
    expect(themes).toHaveLength(3)
    for (const [, s1, s2, o, s, cx] of themes) {
      const fg = { 'due-o': o, 'due-s': s, 'cx-h': cx }
      for (const [k, [bg, a]] of Object.entries(toene)) for (const surf of [s1, s2]) {
        const kt = kontrast(hex(fg[k]), mix(hex(bg), a, hex(surf)))
        expect(kt, `${k} ${fg[k]} auf ${bg}/${a} über ${surf}`).toBeGreaterThanOrEqual(4.5)
      }
    }
    for (const [sel, v] of [['card-date-badge--overdue', 'due-o'], ['card-date-badge--soon', 'due-s'], ['card-complexity-badge--hoch', 'cx-h']])
      expect(css).toMatch(new RegExp(`\\.${sel} \\{\\s*color: var\\(--${v}\\);`))
  })

  it('.lc steht nach .label (gleiche Spezifität, späterer Block gewinnt)', () => {
    expect(css.indexOf('\n.lc {')).toBeGreaterThan(css.indexOf('\n.label {'))
    expect(css).toMatch(/\.lc \{[^}]*background:\s*hsl\(var\(--h\) var\(--lb\)\)[^}]*color:\s*hsl\(var\(--h\) var\(--lf\)\)/)
  })
})
