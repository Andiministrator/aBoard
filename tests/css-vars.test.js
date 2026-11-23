/* @vitest-environment node */

/**
 * F-436 / Sprint-48-Kritiker: Jede `var(--x)`-Referenz muss eine Definition haben.
 *
 * Hintergrund: Die per JS injizierten Overlay-Styles (`_ensureXStyles()`-Muster,
 * #286) referenzierten sieben CSS-Variablen, die in `src/style.css` gar nicht
 * existieren (`--bg-panel`, `--bg-input`, `--fg`, `--fg-muted`, …). Weil überall
 * ein `var(--x, #fallback)` stand, griff stumm der hartcodierte Dark-Wert — die
 * Overlays folgten dem Light-Theme nicht, und kein Test hat es gemerkt.
 *
 * Der Fallback ist genau das Problem: er macht jeden Tippfehler im Variablennamen
 * unsichtbar. Dieser Test schliesst die Lücke strukturell, statt sie beim nächsten
 * Mal wieder von Hand zu finden.
 */

import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

/* Definition = Variable in Deklarationsposition (direkt nach `{` oder `;`), Kommentare
   vorher entfernt (K-2, Sprint 85): sonst gilt ein Kommentar wie „Einzige Ausnahme
   `--admin-err:` …“ als Definition und verdeckt eine fehlende echte. */
function definitionen(quelle) {
  const ohneKommentar = quelle.replace(/\/\*[\s\S]*?\*\//g, '')
  return [...ohneKommentar.matchAll(/[{;]\s*(--[a-z0-9-]+)\s*:/g)].map(m => m[1])
}

const SRC = new URL('../src/', import.meta.url).pathname
const css = readFileSync(join(SRC, 'style.css'), 'utf8')

/* Definitionen: `--name:` — sowohl in :root/[data-theme] als auch lokal in Regeln. */
const defined = new Set(definitionen(css))

/* Zur Laufzeit per element.style.setProperty() gesetzte Variablen. Die stehen
   bewusst nicht in style.css und sind daher legitime Ausnahmen. */
const runtimeSet = new Set(
  [...readdirSync(SRC)
    .filter(f => f.endsWith('.js'))
    .map(f => readFileSync(join(SRC, f), 'utf8'))
    .join('\n')
    .matchAll(/setProperty\(\s*['"`](--[a-z0-9-]+)/g)].map(m => m[1]),
)

/* Bewusst ohne Definition, mit Fallback als einziger Quelle. Jeder Eintrag hier
   ist eine Entscheidung, kein Versehen — beim Hinzufügen bitte begründen. */
const ERLAUBTE_AUSNAHMEN = new Map([
  ['--danger', 'style.css hartcodiert #dc2626 in .btn-danger; keine Variable vorhanden (F-436)'],
])

function referenzen(quelle, datei) {
  return [...quelle.matchAll(/var\(\s*(--[a-z0-9-]+)/g)].map(m => ({ name: m[1], datei }))
}

/* Dateilokale Definition (#728): Injiziertes CSS darf eine Variable in DERSELBEN
   Datei definieren, die es referenziert (z. B. --admin-err in admin-dialog.js).
   Bewusst NICHT global über alle JS-Dateien: sonst deckte eine Definition in
   Datei A einen Tippfehler in Datei B. */
function dateilokalDefiniert(quelle) {
  return new Set(definitionen(quelle))
}

describe('Hilfsfunktion dateilokalDefiniert (Selbsttest)', () => {
  it('K-2: Kommentar-Text mit „--x:“ ist keine Definition', () => {
    const q = '/* Einzige Ausnahme `--x:` hier */ .a { color: var(--x); }'
    expect(dateilokalDefiniert(q).has('--x')).toBe(false)
    expect(dateilokalDefiniert('.a { --x: 1px; }').has('--x')).toBe(true)
    expect(dateilokalDefiniert('.a { color: red; --x: 1px; }').has('--x')).toBe(true)
    expect(dateilokalDefiniert(':root {\n  --x: 1px;\n}').has('--x')).toBe(true)
  })

  it('definiert nur für die eigene Datei', () => {
    const a = '.x { --nur-in-a: red; }'
    const b = '.y { color: var(--nur-in-a); }'
    expect(dateilokalDefiniert(a).has('--nur-in-a')).toBe(true)
    expect(dateilokalDefiniert(b).has('--nur-in-a')).toBe(false)
  })
})

describe('CSS-Variablen: jede Referenz hat eine Definition (F-436)', () => {
  const dateien = ['style.css', ...readdirSync(SRC).filter(f => f.endsWith('.js'))]
  const lokal = new Map(dateien.map(f => [f, dateilokalDefiniert(readFileSync(join(SRC, f), 'utf8'))]))
  const alle = dateien.flatMap(f => referenzen(readFileSync(join(SRC, f), 'utf8'), f))

  it('kennt überhaupt Referenzen (Selbsttest gegen kaputte Regex)', () => {
    expect(alle.length).toBeGreaterThan(100)
    expect(defined.size).toBeGreaterThan(10)
  })

  it('referenziert keine undefinierte CSS-Variable', () => {
    const unbekannt = alle.filter(r =>
      !defined.has(r.name) && !lokal.get(r.datei).has(r.name) && !runtimeSet.has(r.name) && !ERLAUBTE_AUSNAHMEN.has(r.name))

    /* Aussagekräftige Fehlermeldung: Name + Fundstelle, nicht nur "false !== true" */
    const bericht = [...new Set(unbekannt.map(r => `${r.name}  (${r.datei})`))].sort()
    expect(bericht, `Undefinierte CSS-Variablen — der var()-Fallback verdeckt sie stumm.
Entweder in style.css definieren, auf eine existierende Variable umstellen,
oder (mit Begründung) in ERLAUBTE_AUSNAHMEN eintragen:\n  ${bericht.join('\n  ')}`).toEqual([])
  })

  it('hält die Ausnahmeliste aktuell (kein toter Eintrag)', () => {
    const referenziert = new Set(alle.map(r => r.name))
    const tot = [...ERLAUBTE_AUSNAHMEN.keys()].filter(n => !referenziert.has(n))
    expect(tot, `Ausnahme eingetragen, aber nirgends mehr referenziert: ${tot.join(', ')}`).toEqual([])
  })
})
