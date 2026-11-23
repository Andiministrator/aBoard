/* @vitest-environment happy-dom */

/**
 * Karte #626 (F-1406) — Admin-Dialog, Reiter „Backup“: Schalter für
 * `backup_board.encryption_enabled`.
 *
 * Anlass: Ein Betreiber musste `PUT /admin/config/backup_board.encryption_enabled`
 * von Hand absetzen; wer es vergaß, bekam still Klartext-Backups. Geprüft wird:
 *   - der Schalter wirkt (setConfig + Reload, Meldung),
 *   - der Warnzustand ist sichtbar, solange die Verschlüsselung aus ist,
 *   - „Schlüssel gesetzt ja/nein“ erscheint, OHNE einen Schlüsselwert zu zeigen,
 *   - ohne Schlüssel ist Einschalten gesperrt (sonst bliebe der Scheduler aus),
 *     Ausschalten bleibt möglich.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

let listConfigAntwort = { ok: true, status: 200, body: { entries: [], secrets_enabled: true } };
let setConfigAntwort = { ok: true, status: 200, body: { ok: true } };
let reloadAntwort = { ok: true, status: 200, body: { results: {} } };
const listConfigSpy = vi.fn(async () => listConfigAntwort);
const setConfigSpy = vi.fn(async () => setConfigAntwort);
const reloadSpy = vi.fn(async () => reloadAntwort);

vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    listConfig: (...a) => listConfigSpy(...a),
    setConfig: (...a) => setConfigSpy(...a),
    reload: (...a) => reloadSpy(...a),
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
  },
}));
vi.mock('../src/clients-projects-client.js', () => ({
  ClientsProjectsClient: {
    listProjects: vi.fn(async () => ({ ok: true, body: { projects: [] } })),
  },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({ name: null, email: null }) }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { I18n } from '../src/i18n.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));

async function oeffneBackup() {
  AdminDialog.open();
  await ruhe();
  document.querySelector('.admin-tab[data-section="backup"]').click();
  await ruhe(); await ruhe();
  return document.querySelector('.admin-body');
}

const eintrag = wert => ({ key: 'backup_board.encryption_enabled', value: wert, is_secret: false });

describeServer('Backup-Reiter: Verschlüsselungsschalter (Karte #626)', () => {
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
    listConfigAntwort = { ok: true, status: 200, body: { entries: [], secrets_enabled: true } };
    setConfigAntwort = { ok: true, status: 200, body: { ok: true } };
    reloadAntwort = { ok: true, status: 200, body: { results: {} } };
    listConfigSpy.mockClear(); setConfigSpy.mockClear(); reloadSpy.mockClear();
  });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; });

  it('Voreinstellung aus: Schalter unmarkiert, Warnung sichtbar, Schlüssel „ja“', async () => {
    const b = await oeffneBackup();
    expect(listConfigSpy).toHaveBeenCalledWith('backup_board');
    const chk = b.querySelector('.admin-enc-box input[type=checkbox]');
    expect(chk).toBeTruthy();
    expect(chk.checked).toBe(false);
    expect(chk.disabled).toBe(false);
    const warn = b.querySelector('.admin-enc-warn');
    expect(warn.textContent).toMatch(/Klartext/);
    expect(b.querySelector('.admin-enc-box').classList.contains('admin-enc-box--warn')).toBe(true);
    expect(b.querySelector('.admin-enc-key').textContent).toMatch(/ja/);
    expect(b.querySelector('.admin-enc-key--missing')).toBeNull();
  });

  it('Einschalten schreibt "true", lädt neu, meldet Erfolg und räumt die Warnung weg', async () => {
    const b = await oeffneBackup();
    const chk = b.querySelector('.admin-enc-box input[type=checkbox]');
    chk.checked = true;
    chk.dispatchEvent(new Event('change'));
    await ruhe(); await ruhe();
    expect(setConfigSpy).toHaveBeenCalledWith('backup_board.encryption_enabled', 'true', false);
    expect(reloadSpy).toHaveBeenCalledTimes(1);
    expect(reloadSpy).toHaveBeenCalledWith('backup_board');   /* V-4: nur diese Sektion */
    const flash = b.querySelector('.admin-enc-box .admin-flash');
    expect(flash.classList.contains('admin-flash--ok')).toBe(true);
    expect(b.querySelector('.admin-enc-warn').textContent).toBe('');
    expect(b.querySelector('.admin-enc-box').classList.contains('admin-enc-box--warn')).toBe(false);
    expect(chk.checked).toBe(true);
  });

  it('bereits eingeschaltet: Schalter markiert, keine Warnung; Ausschalten schreibt "false"', async () => {
    listConfigAntwort = { ok: true, status: 200, body: { entries: [eintrag('true')], secrets_enabled: true } };
    const b = await oeffneBackup();
    const chk = b.querySelector('.admin-enc-box input[type=checkbox]');
    expect(chk.checked).toBe(true);
    expect(b.querySelector('.admin-enc-warn').textContent).toBe('');
    chk.checked = false;
    chk.dispatchEvent(new Event('change'));
    await ruhe(); await ruhe();
    expect(setConfigSpy).toHaveBeenCalledWith('backup_board.encryption_enabled', 'false', false);
    expect(b.querySelector('.admin-enc-warn').textContent).toMatch(/Klartext/);
  });

  it('ohne Schlüssel: Einschalten gesperrt, Schlüssel „nein“ hervorgehoben, Warnung nennt beides', async () => {
    listConfigAntwort = { ok: true, status: 200, body: { entries: [], secrets_enabled: false } };
    const b = await oeffneBackup();
    const chk = b.querySelector('.admin-enc-box input[type=checkbox]');
    expect(chk.disabled).toBe(true);
    expect(b.querySelector('.admin-enc-key--missing')).toBeTruthy();
    expect(b.querySelector('.admin-enc-key').textContent).toMatch(/nein/);
    expect(b.querySelector('.admin-enc-warn').textContent).toMatch(/ABOARD_SECRET_KEY/);
    expect(setConfigSpy).not.toHaveBeenCalled();
  });

  it('an, aber ohne Schlüssel: Warnung „keine Backups“, Schalter bleibt ausschaltbar', async () => {
    listConfigAntwort = { ok: true, status: 200, body: { entries: [eintrag('true')], secrets_enabled: false } };
    const b = await oeffneBackup();
    const chk = b.querySelector('.admin-enc-box input[type=checkbox]');
    expect(chk.checked).toBe(true);
    expect(chk.disabled).toBe(false);
    expect(b.querySelector('.admin-enc-warn').textContent).toMatch(/KEINE Backups/);
  });

  it('Schreibfehler: Schalter springt zurück, Fehler sichtbar, kein Reload', async () => {
    setConfigAntwort = { ok: false, status: 500, body: null };
    const b = await oeffneBackup();
    const chk = b.querySelector('.admin-enc-box input[type=checkbox]');
    chk.checked = true;
    chk.dispatchEvent(new Event('change'));
    await ruhe(); await ruhe();
    expect(chk.checked).toBe(false);
    expect(chk.disabled).toBe(false);
    expect(reloadSpy).not.toHaveBeenCalled();
    const flash = b.querySelector('.admin-enc-box .admin-flash');
    expect(flash.classList.contains('admin-flash--err')).toBe(true);
    expect(flash.getAttribute('role')).toBe('alert');
  });

  it('Reload scheitert: gespeichert, aber Fehlermeldung statt Erfolg', async () => {
    reloadAntwort = { ok: false, status: 500, body: null };
    const b = await oeffneBackup();
    const chk = b.querySelector('.admin-enc-box input[type=checkbox]');
    chk.checked = true;
    chk.dispatchEvent(new Event('change'));
    await ruhe(); await ruhe();
    expect(setConfigSpy).toHaveBeenCalled();
    expect(b.querySelector('.admin-enc-box .admin-flash').classList.contains('admin-flash--err')).toBe(true);
  });

  it('Lesefehler der Config: Fehlerhinweis statt Schalter; der Rest der Sektion bleibt', async () => {
    listConfigAntwort = { ok: false, status: 500, body: null };
    const b = await oeffneBackup();
    expect(b.querySelector('.admin-enc-box')).toBeNull();
    expect(b.querySelector('.admin-enc-host .admin-error')).toBeTruthy();
    expect(b.querySelector('input.admin-form-input')).toBeTruthy();
  });

  it('zeigt nie einen Schlüsselwert — nur die Ja/Nein-Aussage', async () => {
    listConfigAntwort = { ok: true, status: 200, body: {
      entries: [eintrag('false')], secrets_enabled: true, secret_key: 'GEHEIMER-SCHLUESSEL-0123456789',
    } };
    const b = await oeffneBackup();
    expect(b.textContent).not.toContain('GEHEIMER-SCHLUESSEL');
  });
});

/* F-1462 (Sprint-80-Kritiker V-4): HTTP 200 sagt nichts über `backup_board`;
   ausgewertet wird `results.backup_board`. Seit V-4 (Sprint 86) ruft der Schalter
   `reload('backup_board')` — nur diese Sektion, nicht alle Hooks. */
describeServer('Verschlüsselungs-Schalter: Reload-Ergebnis der Sektion backup_board (F-1462)', () => {
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
    listConfigAntwort = { ok: true, status: 200, body: { entries: [], secrets_enabled: true } };
    setConfigAntwort = { ok: true, status: 200, body: { ok: true } };
    setConfigSpy.mockClear(); reloadSpy.mockClear();
  });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; });

  async function schalte(reload) {
    reloadAntwort = reload;
    AdminDialog.open();
    await ruhe();
    document.querySelector('.admin-tab[data-section="backup"]').click();
    await ruhe(); await ruhe();
    const b = document.querySelector('.admin-body');
    const chk = b.querySelector('.admin-enc-box input[type=checkbox]');
    chk.checked = true;
    chk.dispatchEvent(new Event('change'));
    await ruhe(); await ruhe();
    return b.querySelector('.admin-enc-box .admin-flash');
  }

  it('HTTP 200, aber results.backup_board = "error: …": „wirkt erst nach einem Neustart“, rot', async () => {
    const flash = await schalte({ ok: true, status: 200, body: { results: { backup_board: 'error: secret key missing', llm: 'ok' } } });
    expect(flash.classList.contains('admin-flash--err')).toBe(true);
    expect(flash.textContent).toContain('Neustart');
  });

  it('HTTP 200 und backup_board "ok": Erfolgsmeldung', async () => {
    const flash = await schalte({ ok: true, status: 200, body: { results: { backup_board: 'ok' } } });
    expect(flash.classList.contains('admin-flash--ok')).toBe(true);
  });

  it('Fehler einer FREMDEN Sektion ändert die Meldung nicht (nur backup_board zählt)', async () => {
    const flash = await schalte({ ok: true, status: 200, body: { results: { backup_board: 'ok', smtp: 'error: x' } } });
    expect(flash.classList.contains('admin-flash--ok')).toBe(true);
  });
});
