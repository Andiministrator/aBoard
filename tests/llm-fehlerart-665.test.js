/* llm-fehlerart-665.test.js — Karte #665 (ADR-0133 §3): Fehlerarten aus der
 * 4xx-Antwort des Anbieters kommen mit eigenem Text und der gekürzten
 * Meldung beim Nutzer an — vor dem Stream (HTTP 400) und im Stream. */

import { describe, it, expect, beforeEach } from 'vitest';

import { LLMTab } from '../src/llm-tab.js';
import { I18n }   from '../src/i18n.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');
const describeOnServer = IS_SERVER_BUILD ? describe : describe.skip;

const FAELLE = [
  ['context-too-long',  /zu lang/],
  ['tools-unsupported', /Werkzeuge/],
  ['provider-rejected', /lehnt/],
];

describeOnServer('LLM-Tab — Fehlerarten des Anbieters (#665)', () => {
  beforeEach(() => { I18n.setLang('de'); });

  it('_errorText: HTTP 400 mit kind zeigt Text und Meldung', () => {
    for (const [kind, re] of FAELLE) {
      const t = LLMTab._errorText(400, { kind, message: 'maximum context length is 8192' });
      expect(t).toMatch(re);
      expect(t).toContain('maximum context length is 8192');
      expect(t).not.toBe(I18n.t('llm.error-bad-request'));
    }
  });

  it('_streamErrorText: event:error mit kind zeigt Text und Meldung', () => {
    for (const [kind, re] of FAELLE) {
      const t = LLMTab._streamErrorText({ kind, message: 'Modell x unbekannt' });
      expect(t).toMatch(re);
      expect(t).toContain('Modell x unbekannt');
      expect(t).not.toBe(I18n.t('llm.error-server'));
    }
  });

  it('ohne Meldung kein "undefined"; unbekanntes kind bleibt generisch', () => {
    expect(LLMTab._errorText(400, { kind: 'context-too-long' })).not.toMatch(/undefined/);
    expect(LLMTab._streamErrorText({ kind: 'provider-rejected' })).not.toMatch(/undefined/);
    expect(LLMTab._errorText(400, { kind: 'anderes' })).toBe(I18n.t('llm.error-bad-request'));
  });

  it('Texte existieren in DE und EN', () => {
    for (const lang of ['de', 'en']) {
      I18n.setLang(lang);
      for (const [kind] of FAELLE) {
        expect(I18n.t('llm.error-' + kind, 'm')).not.toBe('llm.error-' + kind);
      }
    }
    I18n.setLang('de');
  });
});
