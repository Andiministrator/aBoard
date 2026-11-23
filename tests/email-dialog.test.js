/* @vitest-environment happy-dom */

/**
 * DOM-Tests für src/email-dialog.js (#293, ADR-0091) — A11y-Nachrüstung
 * Karte #327 (F-347/F-363): Initial-Focus, Focus-Trap, Focus-Restore.
 *
 * Läuft nur im Server-Build-Kontext (`describeServer`, ADR-0116);
 * `EmailDialog` ist im slim/claude-code-Build per DCE `null`.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

import { EmailDialog } from '../src/email-dialog.js';
import { describeServer } from './helpers/build-flags.js';

const panel = () => document.querySelector('.email-panel');
const card  = { id: 'card-1', displayId: 7, title: 'Testkarte', description: 'Beschreibung' };
const sendFn = vi.fn(async () => ({ ok: true, status: 200, body: {} }));

describeServer('EmailDialog — A11y (#327 F-347/F-363)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    sendFn.mockClear();
  });
  afterEach(() => { EmailDialog?.close(); document.body.innerHTML = ''; });

  it('Panel trägt role/aria-modal/aria-labelledby auf den Titel-Header', () => {
    EmailDialog.open(card, null, sendFn);
    const p = panel();
    expect(p.getAttribute('role')).toBe('dialog');
    expect(p.getAttribute('aria-modal')).toBe('true');
    const labelId = p.getAttribute('aria-labelledby');
    expect(labelId).toBeTruthy();
    expect(document.getElementById(labelId)?.textContent).toBeTruthy();
  });

  it('Initial-Focus liegt auf dem ersten Eingabefeld (An-Feld)', () => {
    EmailDialog.open(card, null, sendFn);
    const toInput = panel().querySelector('.email-input');
    expect(document.activeElement).toBe(toInput);
  });

  it('Focus-Trap: Tab am letzten fokussierbaren Element springt zum ersten', () => {
    EmailDialog.open(card, null, sendFn);
    const focusable = [...panel().querySelectorAll('input,textarea,button')]
      .filter(el => !el.disabled && el.offsetParent !== null);
    expect(focusable.length).toBeGreaterThan(1);
    const first = focusable[0];
    const last  = focusable[focusable.length - 1];
    last.focus();
    const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true });
    document.dispatchEvent(ev);
    expect(document.activeElement).toBe(first);
  });

  it('Focus-Trap: Shift+Tab am ersten fokussierbaren Element springt zum letzten', () => {
    EmailDialog.open(card, null, sendFn);
    const focusable = [...panel().querySelectorAll('input,textarea,button')]
      .filter(el => !el.disabled && el.offsetParent !== null);
    const first = focusable[0];
    const last  = focusable[focusable.length - 1];
    first.focus();
    const ev = new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true });
    document.dispatchEvent(ev);
    expect(document.activeElement).toBe(last);
  });

  it('Focus-Restore: schließt der Dialog, kehrt der Fokus zum auslösenden Element zurück', () => {
    const opener = document.createElement('button');
    opener.textContent = 'öffnen';
    document.body.appendChild(opener);
    opener.focus();

    EmailDialog.open(card, null, sendFn);
    expect(document.activeElement).not.toBe(opener);

    EmailDialog.close();
    expect(document.activeElement).toBe(opener);
  });

  it('schließt weiterhin über Escape', () => {
    EmailDialog.open(card, null, sendFn);
    const overlay = document.querySelector('.email-overlay');
    /* #348/ADR-0118: Anzeige läuft jetzt über die Modal-Shell-Klasse
       `modal-overlay--open`, nicht mehr über eine dialogeigene
       `email-overlay--open`. */
    expect(overlay.classList.contains('modal-overlay--open')).toBe(true);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(overlay.classList.contains('modal-overlay--open')).toBe(false);
  });
});
