/* @vitest-environment happy-dom */

/**
 * Karte #682 / F-1553 — Im Firmenmodus liefert der Server für eine fremde
 * KI-Anfrage nur „belegt seit“ (ohne user/provider/model). Die Texte für
 * Sperr-Banner und 423 dürfen dann kein „undefined“ zeigen.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { I18n } from '../src/i18n.js'
import { describeServer } from './helpers/build-flags.js'

afterEach(() => { I18n.setLang('de') })

describeServer('F-1553: Sperrtexte ohne Person und Modell', () => {
  for (const lang of ['de', 'en']) {
    it(`${lang}: Banner und 423 ohne model zeigen kein undefined`, () => {
      I18n.setLang(lang)
      const nurBelegt = { since: '2026-10-04T18:00:00Z' }
      for (const key of ['llm.pending.banner', 'llm.error-locked']) {
        const text = I18n.t(key, nurBelegt)
        expect(text).not.toMatch(/undefined/)
        expect(text.length).toBeGreaterThan(10)
      }
    })

    it(`${lang}: mit Person und Modell bleibt der volle Text`, () => {
      I18n.setLang(lang)
      const voll = { user: 'a@x.de', provider: 'openai', model: 'gemma' }
      expect(I18n.t('llm.pending.banner', voll)).toContain('openai/gemma')
      expect(I18n.t('llm.error-locked', voll)).toContain('a@x.de')
    })
  }
})
