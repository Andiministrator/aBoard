/* @vitest-environment happy-dom */

/**
 * DOM-Tests für src/notifications-dialog.js (#305, ADR-0107) — Migration auf
 * die gemeinsame Modal-Shell (#348, ADR-0118).
 *
 * Vorher hatte dieser Dialog KEINEN Frontend-Test und KEINEN Focus-Trap.
 *
 * Läuft nur im Server-Build-Kontext (`describeServer`, ADR-0116).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

let typesAntwort  = { ok: true, body: { event_types: [{ type: 'card.moved', label: 'Karte verschoben', default_email: false, default_gotify: false }], delivery_enabled: true } };
let subsAntwort   = { ok: true, body: { subscriptions: [] } };
let gotifyAntwort = { ok: true, body: { is_set: false } };

vi.mock('../src/notifications-client.js', () => ({
  NotificationsClient: {
    eventTypes: () => typesAntwort,
    listSubs:   () => subsAntwort,
    getConfig:  () => gotifyAntwort,
    setSub:     vi.fn(async () => ({ ok: true, body: {} })),
    setConfig:  vi.fn(async () => ({ ok: true, body: {} })),
    deleteConfig: vi.fn(async () => ({ ok: true, body: {} })),
    test:       vi.fn(async () => ({ ok: true, body: {} })),
  },
}));

import { NotificationsDialog } from '../src/notifications-dialog.js';
import { describeServer } from './helpers/build-flags.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const overlay = () => document.querySelector('.notif-overlay');
const panel   = () => document.querySelector('.notif-panel');

describeServer('NotificationsDialog — Modal-Shell-Migration (#348)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    typesAntwort  = { ok: true, body: { event_types: [{ type: 'card.moved', label: 'Karte verschoben', default_email: false, default_gotify: false }], delivery_enabled: true } };
    subsAntwort   = { ok: true, body: { subscriptions: [] } };
    gotifyAntwort = { ok: true, body: { is_set: false } };
  });
  afterEach(() => { NotificationsDialog?.close(); document.body.innerHTML = ''; });

  it('öffnet über die Modal-Shell: Basis-Klassen + dialogspezifische Klasse', async () => {
    NotificationsDialog.open();
    await ruhe();
    const ov = overlay();
    expect(ov.classList.contains('modal-overlay')).toBe(true);
    expect(ov.classList.contains('modal-overlay--open')).toBe(true);
    expect(ov.getAttribute('aria-hidden')).toBe('false');
    const p = panel();
    expect(p.classList.contains('modal')).toBe(true);
    expect(p.getAttribute('role')).toBe('dialog');
    expect(p.getAttribute('aria-modal')).toBe('true');
  });

  it('rendert die Event-Tabelle im Modal-Body', async () => {
    NotificationsDialog.open();
    await ruhe();
    const body = panel().querySelector('.modal-body');
    expect(body.querySelector('.notif-table')).toBeTruthy();
    expect(body.textContent).toContain('Karte verschoben');
  });

  it('zeigt den Delivery-off-Hinweis, wenn der Versand serverseitig aus ist', async () => {
    typesAntwort = { ...typesAntwort, body: { ...typesAntwort.body, delivery_enabled: false } };
    NotificationsDialog.open();
    await ruhe();
    expect(panel().querySelector('.notif-warn')).toBeTruthy();
  });

  it('Focus-Trap greift (vorher nicht vorhanden, F-347)', async () => {
    NotificationsDialog.open();
    await ruhe();
    const focusable = [...panel().querySelectorAll('input,button')]
      .filter(el => !el.disabled && el.offsetParent !== null);
    expect(focusable.length).toBeGreaterThan(1);
    const first = focusable[0];
    const last  = focusable[focusable.length - 1];
    last.focus();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(first);
  });

  it('Focus-Restore: schließt der Dialog, kehrt der Fokus zum auslösenden Element zurück', async () => {
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();

    NotificationsDialog.open();
    await ruhe();
    expect(document.activeElement).not.toBe(opener);

    NotificationsDialog.close();
    expect(document.activeElement).toBe(opener);
  });

  it('schließt über Escape', async () => {
    NotificationsDialog.open();
    await ruhe();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(overlay().getAttribute('aria-hidden')).toBe('true');
  });
});
