/* @vitest-environment happy-dom */

/* llm-firmenmodus-dialog-664.test.js — Karte #664 (ADR-0133 §2):
 * Schlüsseldialog im Firmenmodus und mit PII-Vorgabe des Betreibers.
 *
 *   - Firmenmodus: „vom Betreiber eingerichtet“ und das Ziel, KEINE Eingabe
 *     für eigene Schlüssel.
 *   - PII-Vorgabe: die Auswahl ist gesperrt und zeigt die Vorgabe; die eigene
 *     Wahl in localStorage bleibt unangetastet.
 *   - Gegenproben ohne die neuen Felder: Dialog wie vorher.
 *
 * Nur im Server-Build-Kontext (LLMKeysDialog ist sonst per DCE null). */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';

let listAntwort = { ok: true, body: { providers: [], keys: [] } };

vi.mock('../src/llm-keys-client.js', () => ({
  LLMKeysClient: {
    list:   vi.fn(async () => listAntwort),
    set:    vi.fn(async () => ({ ok: true, body: {} })),
    delete: vi.fn(async () => ({ ok: true, body: {} })),
    test:   vi.fn(async () => ({ ok: true, body: {} })),
  },
}));

vi.mock('../src/llm-tab.js', () => ({
  LLMTab: { invalidateKeysCache: vi.fn(), hasAnyKey: vi.fn(async () => false) },
}));

import { LLMKeysDialog } from '../src/llm-keys-dialog.js';
import { I18n } from '../src/i18n.js';
import { describeServer } from './helpers/build-flags.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const panel = () => document.querySelector('.llm-panel');
const radios = () => [...panel().querySelectorAll('input[name="aboard-pii-mode"]')];
const gewaehlt = () => radios().find(r => r.checked)?.value;

async function oeffne(body) {
  listAntwort = { ok: true, body };
  LLMKeysDialog.open();
  await ruhe();
  await ruhe();
}

describeServer('LLMKeysDialog — Firmenmodus und PII-Vorgabe (#664)', () => {
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
  });
  afterEach(() => { LLMKeysDialog?.close(); document.body.innerHTML = ''; });

  it('Firmenmodus: „vom Betreiber eingerichtet“ + Ziel, keine Schlüssel-Eingabe', async () => {
    await oeffne({ providers: [], keys: [], firmenmodus: { ziel: 'llm.example:8000', anbieter: ['openai'], modelle: ['qwen3-30b'] }, pii: 'aus' });
    const text = panel().textContent;
    expect(text).toContain(I18n.t('llm.firmenmodus.titel'));
    expect(text).toContain('llm.example:8000');
    expect(panel().querySelector('.llm-key-input')).toBeNull();
    expect(panel().querySelector('.llm-provider-row')).toBeNull();
  });

  it('Gegenprobe: ohne Firmenmodus gibt es die Zeilen mit Schlüssel-Eingabe', async () => {
    await oeffne({ providers: [], keys: [] });
    expect(panel().querySelectorAll('.llm-key-input').length).toBe(4);
    expect(panel().textContent).not.toContain(I18n.t('llm.firmenmodus.titel'));
  });

  it('PII „pflicht“: Auswahl gesperrt auf „Immer prüfen“, eigene Wahl bleibt gespeichert', async () => {
    localStorage.setItem('aboard.pii.mode', 'never');
    await oeffne({ providers: [], keys: [], firmenmodus: { ziel: 'api.openai.com', anbieter: ['openai'], modelle: ['m'] }, pii: 'pflicht' });
    expect(gewaehlt()).toBe('always');
    expect(radios().every(r => r.disabled)).toBe(true);
    expect(panel().textContent).toContain(I18n.t('llm.pii.vorgabe-pflicht'));
    /* Auch ein erzwungenes change-Ereignis schreibt nichts. */
    const nie = radios().find(r => r.value === 'never');
    nie.checked = true;
    nie.dispatchEvent(new Event('change'));
    expect(localStorage.getItem('aboard.pii.mode')).toBe('never');
  });

  it('PII „aus“ (auch ohne Firmenmodus): Auswahl gesperrt auf „Nie prüfen“', async () => {
    localStorage.setItem('aboard.pii.mode', 'always');
    await oeffne({ providers: [], keys: [], pii: 'aus' });
    expect(gewaehlt()).toBe('never');
    expect(radios().every(r => r.disabled)).toBe(true);
    expect(panel().textContent).toContain(I18n.t('llm.pii.vorgabe-aus'));
  });

  it('Gegenprobe: ohne Vorgabe ist die Auswahl frei und speichert', async () => {
    localStorage.setItem('aboard.pii.mode', 'custom');
    await oeffne({ providers: [], keys: [] });
    expect(gewaehlt()).toBe('custom');
    expect(radios().some(r => r.disabled)).toBe(false);
    const nie = radios().find(r => r.value === 'never');
    nie.checked = true;
    nie.dispatchEvent(new Event('change'));
    expect(localStorage.getItem('aboard.pii.mode')).toBe('never');
  });
});
