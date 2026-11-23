// Smoke-Test: verifiziert dass das Test-Setup funktioniert
import { EventBus } from '../src/eventbus.js'

describe('Test-Setup Smoke-Test', () => {
  it('happy-dom DOM-Umgebung ist verfügbar', () => {
    const el = document.createElement('div')
    expect(el).toBeTruthy()
    expect(el.tagName).toBe('DIV')
  })

  it('ESM-Import aus src/ funktioniert: EventBus.emit ist eine Funktion', () => {
    expect(typeof EventBus.emit).toBe('function')
  })
})
