/* @vitest-environment happy-dom */

/**
 * DOM-Tests für src/clients-view.js (#307, ADR-0102) — Migration auf die
 * gemeinsame Modal-Shell (#348, ADR-0118).
 *
 * Vorher hatte dieser Dialog KEINEN Frontend-Test und KEINEN Focus-Trap.
 * Diese Suite prüft beides: dass der Dialog tatsächlich `createModal()`
 * BENUTZT (Klasse-32-Fehlerklasse — ein grüner modal-shell.test.js sagt
 * nichts über die Konsumenten aus) und dass F-347/A11y jetzt greift.
 *
 * Läuft nur im Server-Build-Kontext (`describeServer`, ADR-0116);
 * `ClientsView` ist im slim/claude-code-Build per DCE `null`.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

let listClientsAntwort  = { ok: true, body: [] };
let listProjectsAntwort = { ok: true, body: [] };
const deleteClientSpy = vi.fn(async () => ({ ok: true, body: {} }));
const createClientSpy = vi.fn(async () => ({ ok: true, body: {} }));

vi.mock('../src/clients-projects-client.js', () => ({
  ClientsProjectsClient: {
    listClients:  (...a) => listClientsAntwort,
    listProjects: (...a) => listProjectsAntwort,
    createClient: (...a) => createClientSpy(...a),
    deleteClient: (...a) => deleteClientSpy(...a),
  },
}));

import { ClientsView } from '../src/clients-view.js';
import { describeServer } from './helpers/build-flags.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const overlay = () => document.querySelector('.cp-overlay');
const panel   = () => document.querySelector('.cp-panel');

describeServer('ClientsView — Modal-Shell-Migration (#348)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    listClientsAntwort  = { ok: true, body: [{ id: 'c1', name: 'Kunde A', description: '' }] };
    listProjectsAntwort = { ok: true, body: [] };
    deleteClientSpy.mockClear();
    createClientSpy.mockClear();
  });
  afterEach(() => { ClientsView?.close(); document.body.innerHTML = ''; });

  it('öffnet über die Modal-Shell: Basis-Klassen + dialogspezifische Klasse', async () => {
    ClientsView.open();
    await ruhe();
    const ov = overlay();
    expect(ov).toBeTruthy();
    expect(ov.classList.contains('modal-overlay')).toBe(true);
    expect(ov.classList.contains('modal-overlay--open')).toBe(true);
    expect(ov.getAttribute('aria-hidden')).toBe('false');
    const p = panel();
    expect(p.classList.contains('modal')).toBe(true);
    expect(p.getAttribute('role')).toBe('dialog');
    expect(p.getAttribute('aria-modal')).toBe('true');
  });

  it('lädt und zeigt die Client-Liste im .cp-body (aus der Modal-Shell)', async () => {
    ClientsView.open();
    await ruhe();
    const body = panel().querySelector('.modal-body');
    expect(body).toBeTruthy();
    expect(body.textContent).toContain('Kunde A');
  });

  it('schließt über Escape', async () => {
    ClientsView.open();
    await ruhe();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(overlay().getAttribute('aria-hidden')).toBe('true');
  });

  it('schließt über Klick auf den Overlay-Hintergrund', async () => {
    ClientsView.open();
    await ruhe();
    overlay().dispatchEvent(new MouseEvent('click', { bubbles: true }));
    expect(overlay().getAttribute('aria-hidden')).toBe('true');
  });

  it('Focus-Trap: Tab am letzten fokussierbaren Element springt zum ersten (vorher NICHT vorhanden, F-347)', async () => {
    ClientsView.open();
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

    ClientsView.open();
    await ruhe();
    expect(document.activeElement).not.toBe(opener);

    ClientsView.close();
    expect(document.activeElement).toBe(opener);
  });
});
