/* @vitest-environment happy-dom */

/**
 * DOM-Tests für src/projects-view.js (#307, ADR-0102) — Migration auf die
 * gemeinsame Modal-Shell (#348, ADR-0118).
 *
 * Vorher hatte dieser Dialog KEINEN Frontend-Test und KEINEN Focus-Trap.
 * Zusätzlich prüft diese Suite, dass das Content-CSS jetzt über
 * `ensureCpContentStyles()` aus clients-view.js kommt (der frühere
 * "Fallback-Stub"-Doppel-Inject ist entfernt) — nur EIN <style
 * data-aboard="cp-content"> landet im Kopf, egal welcher der beiden Dialoge
 * zuerst öffnet.
 *
 * Läuft nur im Server-Build-Kontext (`describeServer`, ADR-0116).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

let listProjectsAntwort = { ok: true, body: [{ id: 'p1', name: 'Projekt A', description: '', client_id: '' }] };
let listClientsAntwort  = { ok: true, body: [] };
const createProjectSpy = vi.fn(async () => ({ ok: true, body: {} }));
const deleteProjectSpy = vi.fn(async () => ({ ok: true, body: {} }));

vi.mock('../src/clients-projects-client.js', () => ({
  ClientsProjectsClient: {
    listProjects: (...a) => listProjectsAntwort,
    listClients:  (...a) => listClientsAntwort,
    createProject: (...a) => createProjectSpy(...a),
    deleteProject: (...a) => deleteProjectSpy(...a),
    listCardsForProject: async () => ({ ok: true, body: [] }),
  },
}));

import { ProjectsView } from '../src/projects-view.js';
import { describeServer } from './helpers/build-flags.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const overlay = () => document.querySelector('.cp-overlay');
const panel   = () => document.querySelector('.cp-panel');

describeServer('ProjectsView — Modal-Shell-Migration (#348)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    listProjectsAntwort = { ok: true, body: [{ id: 'p1', name: 'Projekt A', description: '', client_id: '' }] };
    listClientsAntwort  = { ok: true, body: [] };
    createProjectSpy.mockClear();
    deleteProjectSpy.mockClear();
  });
  afterEach(() => { ProjectsView?.close(); document.body.innerHTML = ''; });

  it('öffnet über die Modal-Shell: Basis-Klassen + dialogspezifische Klasse', async () => {
    ProjectsView.open();
    await ruhe();
    const ov = overlay();
    expect(ov.classList.contains('modal-overlay')).toBe(true);
    expect(ov.classList.contains('modal-overlay--open')).toBe(true);
    const p = panel();
    expect(p.classList.contains('modal')).toBe(true);
    expect(p.getAttribute('role')).toBe('dialog');
  });

  it('lädt und zeigt die Projekt-Liste im Modal-Body', async () => {
    ProjectsView.open();
    await ruhe();
    const body = panel().querySelector('.modal-body');
    expect(body.textContent).toContain('Projekt A');
  });

  it('injiziert das cp-content-CSS genau EINMAL, auch bei mehrfachem Öffnen', async () => {
    ProjectsView.open();
    await ruhe();
    expect(document.querySelectorAll('style[data-aboard="cp-content"]').length).toBe(1);
    ProjectsView.close();
    ProjectsView.open();
    await ruhe();
    expect(document.querySelectorAll('style[data-aboard="cp-content"]').length).toBe(1);
  });

  it('Focus-Trap greift (vorher nicht vorhanden, F-347)', async () => {
    ProjectsView.open();
    await ruhe();
    const focusable = [...panel().querySelectorAll('input,button,select')]
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

    ProjectsView.open();
    await ruhe();
    expect(document.activeElement).not.toBe(opener);

    ProjectsView.close();
    expect(document.activeElement).toBe(opener);
  });
});
