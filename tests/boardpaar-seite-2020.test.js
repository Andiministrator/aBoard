/* F-2020: Meldet /me `boardpaar_fehler`, merkt sich boardpaarSeite() kein
   „aus“ — der nächste Aufruf fragt /me neu und sieht die echte Seite. */
import { it, expect, vi, beforeEach } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

const antworten = []
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: () => Promise.resolve(antworten.shift()) }) },
}))

import { boardpaarSeite, vergissBoardpaarSeite } from '../src/boardpaar.js'

describeServer('F-2020 boardpaarSeite bei Lesefehler am Server', () => {
  beforeEach(() => { antworten.length = 0; vergissBoardpaarSeite() })

  it('merkt sich nach boardpaar_fehler kein „aus“', async () => {
    antworten.push({ name: 'a', boardpaar_fehler: true }, { name: 'a', boardpaar: 'innen' })
    expect(await boardpaarSeite()).toBe('aus')
    expect(await boardpaarSeite()).toBe('innen')
  })

  it('merkt sich eine gelesene Seite weiterhin', async () => {
    antworten.push({ name: 'a', boardpaar: 'aussen' }, { name: 'a', boardpaar_fehler: true })
    expect(await boardpaarSeite()).toBe('aussen')
    expect(await boardpaarSeite()).toBe('aussen')
    /* Sprint-90-Kritik F-G: die zweite Antwort (Fehler) wurde nie abgeholt —
       eine gelesene Seite fragt /me nicht erneut. */
    expect(antworten).toHaveLength(1)
  })
})
