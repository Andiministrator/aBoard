/* @vitest-environment happy-dom */

/**
 * Sprint-85-Kritiker (Nachtrag F-1786): „Datei öffnen“ und „Projektordner
 * verbinden“ wechseln das Board, ohne `board:loaded` zu senden. Die Bulk-Auswahl
 * überlebte deshalb — und bei einer Backup-Kopie desselben Boards (gleiche
 * Karten-IDs) wirkten Aktionen auf nie gewählte Karten.
 *
 * Geprüft wird der echte Quelltext des `board:open`-Handlers aus src/main.js
 * (kein Spiegel): herausgeschnitten und mit eingesetzten Abhängigkeiten
 * ausgeführt — Muster aus tests/kommentar-loeschrecht-banner.test.js. Für die
 * beiden Ladezweige von `sync:pick-directory` (zu viele Abhängigkeiten zum
 * Ausführen) prüft der zweite Test die Reihenfolge im Quelltext.
 */
import { it, expect, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const quelle = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8')
function block(kopf) {
  const start = quelle.indexOf(kopf)
  expect(start).toBeGreaterThan(-1)
  return quelle.slice(start, quelle.indexOf('\n});\n', start) + 5)
}

it('board:open leert die Bulk-Auswahl, bevor das neue Board gesetzt wird', async () => {
  const reihenfolge = []
  let handler
  const EventBus = { on: (n, f) => { if (n === 'board:open') handler = f } }
  const geladen = { version: '2', board: { id: 'b' } }
  const Storage = { open: vi.fn(async () => geladen) }
  const BulkSelect = { clearAll: vi.fn(() => reihenfolge.push('clear')) }
  const State = { set: vi.fn(() => reihenfolge.push('set')) }
  // eslint-disable-next-line no-new-func
  new Function('EventBus', 'Storage', 'State', 'BulkSelect', 'IS_SERVER_BUILD', 'StoragePlugin',
    block("EventBus.on('board:open', () => {"))(EventBus, Storage, State, BulkSelect, false, {})
  handler()
  await new Promise(r => setTimeout(r, 0))
  expect(State.set).toHaveBeenCalledWith(geladen)
  expect(reihenfolge).toEqual(['clear', 'set'])
})

it('sync:pick-directory: jedes State.set eines neuen Boards folgt auf BulkSelect.clearAll', () => {
  const b = block("EventBus.on('sync:pick-directory', async () => {")
  const sets = [...b.matchAll(/^\s*State\.set\(.*$/gm)]
  expect(sets.length).toBe(2)
  for (const s of sets) {
    const davor = b.slice(0, s.index).trimEnd().split('\n').at(-1)
    expect(davor).toContain('BulkSelect.clearAll()')
  }
})
