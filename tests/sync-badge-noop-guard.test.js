/* @vitest-environment happy-dom */

/**
 * F-310 — Spurious „↺ Änderungen warten…"-Badge beim bloßen Öffnen einer
 * Karte (claude-code projectorPoll, ADR-0067).
 *
 * Wurzel: src/main.js `onExternalUpdate` verzweigte modal-offen vs.
 * -geschlossen BEVOR der F-254-Deep-Equal-No-Op-Guard griff (der saß nur in
 * `applyExternalState`). Fix: Guard single-gesourct als
 * `validateExternalDelta` und an den ANFANG von `onExternalUpdate` gezogen
 * (vor die Modal-Verzweigung).
 *
 * Konvention (wie tests/swimlane-kind.test.js:316 dokumentiert): die
 * main.js-Handler sind NICHT exportiert (side-effekt-Bootstrap) → die
 * Orchestrierung wird hier als Referenz gespiegelt. WICHTIG: die
 * sicherheitskritische Hälfte — Schema-Normalisierung + Deep-Equal — nutzt
 * das ECHTE `Storage.checkVersion` + `State` (genau die vom Card benannte
 * Symmetrie-/Falsch-Positiv-Risikofläche), nur setPending/Badge sind Fakes.
 * Die Platzierung des Guards VOR der Modal-Verzweigung ist zusätzlich per
 * Code-Review + Single-Source-Refactor (validateExternalDelta von BEIDEN
 * Aufrufern genutzt) strukturell garantiert.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { Storage } from '../src/storage.js'
import { State }   from '../src/state.js'

/* Referenz-Spiegel von src/main.js validateExternalDelta — Logik VERBATIM,
   echtes Storage/State. */
function validateExternalDelta(newState) {
  const vorher   = JSON.stringify(newState)
  const validated = Storage.checkVersion(newState)
  if (!validated?.board) return null
  const nachher  = JSON.stringify(validated)
  const cur = State.get()
  if (cur && JSON.stringify(cur) === nachher) return null
  return { state: validated, migriert: nachher !== vorher }
}

/* Referenz-Spiegel von onExternalUpdate NACH dem F-310-Fix: Guard ZUERST,
   dann erst Modal-Verzweigung. */
function onExternalUpdate(newState, { modalOpen, setPending, showBadge, applyExternal }) {
  if (!validateExternalDelta(newState)) return            // F-310: Guard VOR Modal-Check
  if (!modalOpen) { applyExternal(newState); return }
  setPending(newState)
  showBadge()
}

function boardState(extra = {}) {
  /* checkVersion akzeptiert nur version '1'|'2' (String) und normalisiert
     via Migrationskette — beide Aufrufer durchlaufen dieselbe Normalisierung. */
  return { version: '2', board: { id: 'b1', title: 'B', columnDefs: [], sprints: [], nextDisplayId: 1, ...extra } }
}

describe('F-310 — No-Op-Guard vor der Modal-Verzweigung', () => {
  let setPending, showBadge, applyExternal
  beforeEach(() => {
    setPending = vi.fn(); showBadge = vi.fn(); applyExternal = vi.fn()
    State.set(Storage.checkVersion(boardState()))
  })

  it('identischer Poll bei OFFENEM Modal → KEIN setPending / KEIN Badge', () => {
    /* exakt der aktuelle Stand, neu „gepollt" (Projektor faltet dieselbe base) */
    onExternalUpdate(boardState(), { modalOpen: true, setPending, showBadge, applyExternal })
    expect(setPending).not.toHaveBeenCalled()
    expect(showBadge).not.toHaveBeenCalled()
    expect(applyExternal).not.toHaveBeenCalled()
  })

  it('identischer Poll bei GESCHLOSSENEM Modal → kein applyExternalState (No-Op)', () => {
    onExternalUpdate(boardState(), { modalOpen: false, setPending, showBadge, applyExternal })
    expect(applyExternal).not.toHaveBeenCalled()
  })

  it('ECHT abweichender Stand bei offenem Modal → setPending + Badge (nicht unterdrückt)', () => {
    onExternalUpdate(boardState({ title: 'GEÄNDERT' }), { modalOpen: true, setPending, showBadge, applyExternal })
    expect(setPending).toHaveBeenCalledTimes(1)
    expect(showBadge).toHaveBeenCalledTimes(1)
  })

  it('ECHT abweichender Stand bei geschlossenem Modal → applyExternalState', () => {
    onExternalUpdate(boardState({ title: 'GEÄNDERT' }), { modalOpen: false, setPending, showBadge, applyExternal })
    expect(applyExternal).toHaveBeenCalledTimes(1)
    expect(setPending).not.toHaveBeenCalled()
  })

  it('ungültiges Schema → No-Op (kein Badge, kein Apply)', () => {
    onExternalUpdate({ nope: true }, { modalOpen: true, setPending, showBadge, applyExternal })
    expect(setPending).not.toHaveBeenCalled()
    expect(showBadge).not.toHaveBeenCalled()
  })
})

/* ── F-814-Nacharbeit (Kritiker Sprint 61) ──────────────────────────────
   `validateExternalDelta` meldet jetzt zusätzlich, OB die Migration den
   adoptierten Stand verändert hat. `applyExternalState` hängt daran, ob es
   `markClean()` ruft: Bleibt der State dirty, schreibt der Poll-Flush die
   Migration beim nächsten Tick als Ops heraus. Ohne das erreichte sie auf
   einem nur angesehenen Board die Platte nie.

   Diese Datei ist ein SPIEGEL von `src/main.js` — Spiegel bleiben grün,
   wenn der Produktivcode driftet (F-478). Der Korpus-Wächter unten prüft
   deshalb die echte Datei. */
describe('F-814 — Migrations-Erkennung im Adopt-Pfad', () => {
  beforeEach(() => { State.set(Storage.checkVersion(boardState())) })

  it('ein bereits migrierter Stand meldet migriert=false', () => {
    /* Board mit gefüllten columnDefs und ohne Meta-Karten: Die
       Migrationskette findet nichts zu tun. */
    const fertig = Storage.checkVersion(boardState({ id: 'b-anders' }))
    const d = validateExternalDelta(JSON.parse(JSON.stringify(fertig)))
    expect(d).not.toBe(null)
    expect(d.migriert).toBe(false)
  })

  it('ein unmigrierter Stand meldet migriert=true', () => {
    /* `columnDefs: []` füllt `migrateDefaultColumns` beim Laden (F-249/
       ADR-0052) — genau der Fall, der bisher im Speicher wirkte und die
       Platte nie erreichte. */
    const roh = { version: '2', board: { id: 'b-roh', title: 'B', columnDefs: [], sprints: [], nextDisplayId: 1 } }
    const d = validateExternalDelta(roh)
    expect(d).not.toBe(null)
    expect(d.migriert).toBe(true)
  })
})

describe('F-814 — Korpus-Wächter auf src/main.js (der Spiegel oben genügt nicht)', () => {
  const quelle = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '')

  function block(fnName) {
    const start = quelle.indexOf(`function ${fnName}`)
    expect(start, `${fnName} nicht gefunden — umbenannt?`).toBeGreaterThan(-1)
    const rest = quelle.slice(start)
    const ende = rest.search(/\n(?:async )?function /)
    return ende === -1 ? rest : rest.slice(0, ende)
  }

  it('applyExternalState ruft markClean NUR bedingt', () => {
    const b = block('applyExternalState')
    expect(b).toContain('State.markClean()')
    /* Der Punkt ist die Bedingung: ein unbedingtes `markClean()` räumt das
       dirty-Flag ab, das der Poll-Flush braucht — dann steht die Migration
       wieder nur im Speicher. */
    expect(b, 'markClean() steht unbedingt im Adopt-Pfad — die Migration erreicht '
      + 'die Platte dann nur, wenn der Nutzer aus anderem Grund etwas ändert (F-814)')
      .toMatch(/if\s*\(\s*!\s*\w+\.migriert\s*\)\s*\{\s*State\.markClean\(\)/)
  })

  it('validateExternalDelta liefert das Migrations-Flag, nicht nur den Stand', () => {
    const b = block('validateExternalDelta')
    expect(b).toMatch(/migriert:\s*nachher\s*!==\s*vorher/)
  })
})
