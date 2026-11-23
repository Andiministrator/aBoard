/* @vitest-environment happy-dom */

/**
 * Karte #760 (F-1856) — Admin-Dialog: Speichern im Reiter „Mail-Eingang“ und
 * „Auto-Reply“ lädt den IMAP-Arbeitsprozess neu (POST /admin/config/reload,
 * Hook „imap“) und meldet das Ergebnis der Sektion „imap“ — nicht nur HTTP 200
 * (Muster F-1462). Der Worker liest SMTP-, Service-Board- und Auto-Reply-Config
 * nur beim Start.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

let reloadAntwort = { ok: true, status: 200, body: { results: { imap: 'ok' } } };
const setConfigSpy = vi.fn(async () => ({ ok: true, status: 200, body: { ok: true } }));
let serviceAntwort = { ok: true, status: 200, body: { results: { 'service-board': 'ok' } } };
const reloadSpy = vi.fn(async sektion => (sektion === 'service-board' ? serviceAntwort : reloadAntwort));

vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    listConfig: vi.fn(async () => ({ ok: true, status: 200, body: { entries: [] } })),
    setConfig: (...a) => setConfigSpy(...a),
    reload: (...a) => reloadSpy(...a),
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
  },
}));
vi.mock('../src/clients-projects-client.js', () => ({
  ClientsProjectsClient: { listProjects: vi.fn(async () => ({ ok: true, body: { projects: [] } })) },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({ name: null, email: null }) }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { I18n } from '../src/i18n.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));

async function oeffne(sektion) {
  AdminDialog.open();
  await ruhe();
  document.querySelector(`.admin-tab[data-section="${sektion}"]`).click();
  await ruhe(); await ruhe();
  return document.querySelector('.admin-body');
}
const speichern = async b => {
  b.querySelector('.admin-actions .btn-primary').click();
  await ruhe(); await ruhe(); await ruhe();
  return b.querySelector('.admin-flash');
};

describeServer('Mail-Eingang/Auto-Reply: Speichern lädt den Worker neu (Karte #760, F-1856)', () => {
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
    reloadAntwort = { ok: true, status: 200, body: { results: { imap: 'ok' } } };
    serviceAntwort = { ok: true, status: 200, body: { results: { 'service-board': 'ok' } } };
    setConfigSpy.mockClear(); reloadSpy.mockClear();
  });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; });

  it.each(['imap', 'autoreply', 'smtp', 'service'])('%s: Speichern → setConfig, genau ein Reload, Erfolgsmeldung', async sektion => {
    const b = await oeffne(sektion);
    const f = await speichern(b);
    expect(setConfigSpy).toHaveBeenCalled();
    /* V-5: der Service-Board-Reiter ruft zusätzlich den Re-Seed seiner Sektion */
    expect(reloadSpy).toHaveBeenCalledTimes(sektion === 'service' ? 2 : 1);
    if (sektion === 'service') expect(reloadSpy).toHaveBeenCalledWith('service-board');
    /* O-3: nur die Sektion „imap“ — kein Reload aller Hooks (Backup-Lauf!) */
    expect(reloadSpy).toHaveBeenCalledWith('imap');
    expect(f.classList.contains('admin-flash--ok')).toBe(true);
    expect(f.textContent).toMatch(/Mail-Eingang neu gestartet/);
  });

  /* O-5: drei Zustände — ok / aus / Fehler mit dem Text des Servers */
  it.each(['imap', 'autoreply', 'smtp', 'service'])('%s: Reload-Ergebnis „imap“ mit Fehler → rote Meldung mit dem Grund des Servers', async sektion => {
    reloadAntwort = { ok: true, status: 200, body: { results: { imap: 'error: IMAP-Config ungültig, Worker läuft nicht: imap.host fehlt' } } };
    const b = await oeffne(sektion);
    const f = await speichern(b);
    expect(f.classList.contains('admin-flash--err')).toBe(true);
    expect(f.textContent).toMatch(/imap\.host fehlt/);
    expect(f.textContent).not.toMatch(/Neustart des Servers/);
  });

  it.each(['imap', 'autoreply', 'smtp', 'service'])('%s: Mail-Eingang aus → grüne Meldung „ausgeschaltet“, nicht „neu gestartet“', async sektion => {
    reloadAntwort = { ok: true, status: 200, body: { results: { imap: 'disabled' } } };
    const b = await oeffne(sektion);
    const f = await speichern(b);
    expect(f.classList.contains('admin-flash--ok')).toBe(true);
    expect(f.textContent).toMatch(/ausgeschaltet/);
    expect(f.textContent).not.toMatch(/neu gestartet/);
  });

  it('Reload-Antwort ohne Sektion „imap“ (HTTP 200 allein) zählt nicht als Erfolg', async () => {
    reloadAntwort = { ok: true, status: 200, body: { results: {} } };
    const b = await oeffne('imap');
    const f = await speichern(b);
    expect(f.classList.contains('admin-flash--err')).toBe(true);
  });

  it('Speicherfehler: kein Reload', async () => {
    setConfigSpy.mockImplementationOnce(async () => ({ ok: false, status: 500, body: {} }));
    const b = await oeffne('imap');
    const f = await speichern(b);
    expect(reloadSpy).not.toHaveBeenCalled();
    expect(f.classList.contains('admin-flash--err')).toBe(true);
  });

  /* V-5: Service-Board-Reiter — Re-Seed der eigenen Sektion zuerst; scheitert er, kein Mail-Worker-Neustart. */
  it('service: Re-Seed-Fehler → rote Meldung, kein Reload von imap', async () => {
    serviceAntwort = { ok: true, status: 200, body: { results: { 'service-board': 'error: seed kaputt' } } };
    const b = await oeffne('service');
    const f = await speichern(b);
    expect(f.classList.contains('admin-flash--err')).toBe(true);
    expect(f.textContent).toMatch(/seed kaputt/);
    expect(reloadSpy).not.toHaveBeenCalledWith('imap');
  });

  it('service: Reihenfolge erst service-board, dann imap', async () => {
    const b = await oeffne('service');
    await speichern(b);
    expect(reloadSpy.mock.calls.map(c => c[0])).toEqual(['service-board', 'imap']);
  });
});
