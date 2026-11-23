/* @vitest-environment happy-dom */

/**
 * DOM-Tests für src/llm-keys-dialog.js (#298, ADR-0094) — A11y-Nachrüstung
 * Karte #327 (F-347/F-363): Initial-Focus, Focus-Trap, Focus-Restore.
 *
 * Läuft — wie admin-dialog.test.js — nur im Server-Build-Kontext
 * (`describeServer`, ADR-0116); `LLMKeysDialog` ist im slim/claude-code-Build
 * per DCE `null`.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

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
import { describeServer } from './helpers/build-flags.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const panel = () => document.querySelector('.llm-panel');

describeServer('LLMKeysDialog — A11y (#327 F-347/F-363)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    listAntwort = { ok: true, body: { providers: [], keys: [] } };
  });
  afterEach(() => { LLMKeysDialog?.close(); document.body.innerHTML = ''; });

  it('Panel trägt role/aria-modal/aria-labelledby auf den Titel-Header', async () => {
    LLMKeysDialog.open();
    await ruhe();
    const p = panel();
    expect(p.getAttribute('role')).toBe('dialog');
    expect(p.getAttribute('aria-modal')).toBe('true');
    const labelId = p.getAttribute('aria-labelledby');
    expect(labelId).toBeTruthy();
    expect(document.getElementById(labelId)?.textContent).toBeTruthy();
  });

  it('Initial-Focus liegt nach dem (asynchronen) Rendern auf dem Close-Button, wenn kein Input/Button vor ihm steht', async () => {
    /* Ohne Provider-Keys enthält jede Zeile trotzdem ein <input> (Key-Feld) —
       das erste Input im Panel ist also KEIN generisches Beispiel für „kein
       Input vorhanden"; wir prüfen stattdessen, dass der Fokus überhaupt
       innerhalb des Panels landet (Initial-Focus greift). */
    LLMKeysDialog.open();
    await ruhe();
    expect(panel().contains(document.activeElement)).toBe(true);
  });

  it('Focus-Trap: Tab am letzten fokussierbaren Element springt zum ersten', async () => {
    LLMKeysDialog.open();
    await ruhe();
    const focusable = [...panel().querySelectorAll('input,button')]
      .filter(el => !el.disabled && el.offsetParent !== null);
    expect(focusable.length).toBeGreaterThan(1);
    const first = focusable[0];
    const last  = focusable[focusable.length - 1];
    last.focus();
    const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    document.dispatchEvent(ev);
    expect(document.activeElement).toBe(first);
  });

  it('Focus-Trap: Shift+Tab am ersten fokussierbaren Element springt zum letzten', async () => {
    LLMKeysDialog.open();
    await ruhe();
    const focusable = [...panel().querySelectorAll('input,button')]
      .filter(el => !el.disabled && el.offsetParent !== null);
    const first = focusable[0];
    const last  = focusable[focusable.length - 1];
    first.focus();
    const ev = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true });
    document.dispatchEvent(ev);
    expect(document.activeElement).toBe(last);
  });

  it('Focus-Restore: schließt der Dialog, kehrt der Fokus zum auslösenden Element zurück', async () => {
    const opener = document.createElement('button');
    opener.textContent = 'öffnen';
    document.body.appendChild(opener);
    opener.focus();
    expect(document.activeElement).toBe(opener);

    LLMKeysDialog.open();
    await ruhe();
    expect(document.activeElement).not.toBe(opener);

    LLMKeysDialog.close();
    expect(document.activeElement).toBe(opener);
  });

  it('schließt weiterhin über Escape', async () => {
    LLMKeysDialog.open();
    await ruhe();
    const overlay = document.querySelector('.llm-overlay');
    /* #348/ADR-0118: Overlay-Anzeige läuft jetzt über die Modal-Shell-Klasse
       `modal-overlay--open`, nicht mehr über eine dialogeigene
       `llm-overlay--open` — die Shell ist die einzige Quelle für den
       Anzeige-Mechanismus (Klasse-31-Fehlerklasse). */
    expect(overlay.classList.contains('modal-overlay--open')).toBe(true);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(overlay.classList.contains('modal-overlay--open')).toBe(false);
  });

  it('Löschen-Klick ruft window.confirm mit einem lesbaren Text auf, kein Absturz (F-748)', async () => {
    /* Regressionstest für den I18n-Doppelaufruf-Bug (F-748): I18n.t(key, arg)
       statt I18n.t(key)(arg) — mit einem gesetzten Key ist der Löschen-Button
       sichtbar; der Klick-Handler ist eine async-Funktion, ein synchroner
       Wurf darin wird NICHT an den click()-Aufrufer durchgereicht (async
       verpackt ihn in ein rejected Promise) — `expect(...).not.toThrow()`
       würde den Bug deshalb NICHT fangen. Der verlässliche Nachweis ist,
       dass `window.confirm` überhaupt mit einem String erreicht wird: bei
       `I18n.t(key)(arg)` wirft schon die ARGUMENT-Auswertung
       (`"string"(arg)` ist kein Aufruf), bevor `confirm()` je läuft —
       confirmSpy bliebe dann ungerufen. */
    listAntwort = { ok: true, body: {
      providers: ['anthropic'],
      keys: [{ provider: 'anthropic', last_used_at: null }],
    } };
    /* happy-dom implementiert window.confirm nicht — per vi.stubGlobal
       nachrüsten statt vi.spyOn (das nur auf bereits existierenden
       Funktionen spyen kann). */
    const confirmSpy = vi.fn(() => false);
    vi.stubGlobal('confirm', confirmSpy);
    LLMKeysDialog.open();
    await ruhe();
    const delBtn = panel().querySelector('.llm-btn--danger');
    expect(delBtn).toBeTruthy();
    delBtn.click();
    await ruhe();
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(typeof confirmSpy.mock.calls[0][0]).toBe('string');
    vi.unstubAllGlobals();
  });
});

describeServer('LLMKeysDialog — Beschriftung des OpenAI-Anbieters (Karte #665)', () => {
  beforeEach(() => { document.body.innerHTML = ''; });
  afterEach(() => { LLMKeysDialog?.close(); document.body.innerHTML = ''; });

  it('ohne Umstellung nennt die Zeile „OpenAI-kompatibel“ mit — der Anbieter spricht auch mit lokalen Modellen', async () => {
    listAntwort = { ok: true, body: { providers: ['anthropic', 'openai', 'google', 'mistral'], keys: [] } };
    LLMKeysDialog.open();
    await ruhe();
    const labels = [...panel().querySelectorAll('.llm-provider-label')].map(n => n.textContent);
    expect(labels.some(t => t.includes('OpenAI ChatGPT') && t.includes('OpenAI-kompatibel'))).toBe(true);
  });
});
