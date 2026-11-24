/* @vitest-environment happy-dom */

/**
 * Karte #627 — sichtbare Rückmeldung für jeden Klick.
 *
 * Anlass: Admin-Dialog → Backup, Feld „Board-ID“ leer (der Platzhalter
 * „default“ sah aus wie ein Wert). „Jetzt sichern“ tat NICHTS (`if (!id)
 * return;`), der Nutzer hielt das Backup für erledigt, es gab keine Datei.
 *
 * Geprüft: der Abbruchfall (Pflichtfeld leer), je ein Erfolgs- und Fehlerfall
 * der Backup-Aktionen, Abbruch einer Rückfrage, und die Rückmeldung im
 * Agenten-Teil (Admin-Reiter + „Meine Agenten-Zugänge“).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));

let adminTokens = [];
let revokePersonAntwort = { ok: true, status: 200, body: {} };
let setSelbstAntwort = { ok: true, status: 200, body: {} };
const revokePersonSpy = vi.fn(async () => revokePersonAntwort);
const setSelbstSpy = vi.fn(async () => setSelbstAntwort);
vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    listConfig: vi.fn(async () => ({ ok: true, status: 200, body: { entries: [], secrets_enabled: true } })),
    reload: vi.fn(async () => ({ ok: true, body: {} })),
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listAgentTokens: vi.fn(async () => ({ ok: true, body: adminTokens })),
    revokeAgentToken: vi.fn(async () => ({ ok: true, body: {} })),
    revokeAgentTokensOfUser: (...a) => revokePersonSpy(...a),
    listAgentSelbstbedienung: vi.fn(async () => ({ ok: true, body: { boards: [{ id: 'b1', title: 'Team', an: false }] } })),
    setAgentSelbstbedienung: (...a) => setSelbstSpy(...a),
  },
}));
vi.mock('../src/clients-projects-client.js', () => ({
  ClientsProjectsClient: { listProjects: vi.fn(async () => ({ ok: true, body: { projects: [] } })) },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({ name: null, email: null }) }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { AgentZugaengeDialog } from '../src/agent-zugaenge-dialog.js';
import { I18n } from '../src/i18n.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const ZUKUNFT = '2099-01-01T00:00:00Z';

async function oeffneSektion(id) {
  AdminDialog.open();
  await ruhe();
  document.querySelector(`.admin-tab[data-section="${id}"]`).click();
  await ruhe(); await ruhe();
  return document.querySelector('.admin-body');
}

describeServer('Backup-Reiter: Rückmeldung bei „Jetzt sichern“ (Karte #627)', () => {
  let fetchSpy;
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
    fetchSpy = vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ files: [] }) }));
    globalThis.fetch = fetchSpy;
  });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; delete globalThis.fetch; delete globalThis.confirm; });

  const knoepfe = b => ({
    laden: b.querySelectorAll('.admin-actions')[0].querySelectorAll('.admin-btn')[0],
    jetzt: b.querySelectorAll('.admin-actions')[0].querySelectorAll('.admin-btn')[1],
    upload: b.querySelectorAll('.admin-actions')[1].querySelector('.admin-btn'),
  });
  const flashZeile = b => [...b.querySelectorAll(':scope > .admin-flash')][0];

  it('„Laden“ mit leerer Board-ID (seit #735 ist „Jetzt sichern“ ohne ID ein Alle-Boards-Lauf): KEIN Request, sichtbarer Hinweis, Feld markiert, Knopf leuchtet rot', async () => {
    const b = await oeffneSektion('backup');
    const { laden } = knoepfe(b);
    laden.click();
    await ruhe();
    expect(fetchSpy).not.toHaveBeenCalled();
    const flash = flashZeile(b);
    expect(flash.textContent).toMatch(/Board-ID fehlt/);
    expect(flash.classList.contains('admin-flash--err')).toBe(true);
    expect(flash.getAttribute('role')).toBe('alert');
    const feld = b.querySelector('input.admin-form-input');
    expect(feld.getAttribute('aria-invalid')).toBe('true');
    expect(laden.classList.contains('admin-btn--err')).toBe(true);
    /* Eintippen räumt die Markierung weg. */
    feld.value = 'x';
    feld.dispatchEvent(new Event('input'));
    expect(feld.getAttribute('aria-invalid')).toBeNull();
  });

  it('„Laden“ ohne Board-ID meldet dasselbe statt still zu bleiben', async () => {
    const b = await oeffneSektion('backup');
    knoepfe(b).laden.click();
    await ruhe();
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(flashZeile(b).textContent).toMatch(/Board-ID fehlt/);
  });

  it('Platzhalter ist kein scheinbarer Wert mehr („default“ irreführte)', async () => {
    const b = await oeffneSektion('backup');
    const ph = b.querySelector('input.admin-form-input').placeholder;
    expect(ph).not.toBe('default');
    expect(ph).toMatch(/alle Boards/);
  });

  it('Erfolg: Meldung grün, Knopf wieder frei und kurz grün', async () => {
    const b = await oeffneSektion('backup');
    b.querySelector('input.admin-form-input').value = 'board-1';
    const { jetzt } = knoepfe(b);
    jetzt.click();
    await ruhe(); await ruhe();
    const post = fetchSpy.mock.calls.find(c => c[1]?.method === 'POST');
    expect(post[0]).toContain('/boards/board-1/backup/now');
    expect(flashZeile(b).classList.contains('admin-flash--ok')).toBe(true);
    expect(flashZeile(b).textContent).toMatch(/Backup erstellt/);
    expect(jetzt.disabled).toBe(false);
    expect(jetzt.classList.contains('admin-btn--ok')).toBe(true);
  });

  it('Serverfehler 500: Meldung rot; 404: eigener Text; Netzfehler: Meldung statt Absturz', async () => {
    const b = await oeffneSektion('backup');
    b.querySelector('input.admin-form-input').value = 'board-1';
    const { jetzt } = knoepfe(b);

    globalThis.fetch = vi.fn(async () => ({ ok: false, status: 500, json: async () => ({}) }));
    jetzt.click(); await ruhe(); await ruhe();
    expect(flashZeile(b).classList.contains('admin-flash--err')).toBe(true);
    expect(jetzt.disabled).toBe(false);

    globalThis.fetch = vi.fn(async () => ({ ok: false, status: 404, json: async () => ({}) }));
    jetzt.click(); await ruhe(); await ruhe();
    expect(flashZeile(b).textContent).toMatch(/nicht gefunden/);

    globalThis.fetch = vi.fn(async () => { throw new Error('netz'); });
    jetzt.click(); await ruhe(); await ruhe();
    expect(flashZeile(b).classList.contains('admin-flash--err')).toBe(true);
    expect(jetzt.disabled).toBe(false);
  });

  it('Wiederherstellen ohne Datei: eigener Hinweis (nicht „Server-Fehler“)', async () => {
    const b = await oeffneSektion('backup');
    knoepfe(b).upload.click();
    await ruhe();
    expect(flashZeile(b).textContent).toMatch(/Keine Backup-Datei/);
    expect(flashZeile(b).textContent).not.toMatch(/Server-Fehler/);
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});

describeServer('Agenten-Reiter: Rückmeldung (Karte #627)', () => {
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
    adminTokens = [
      { id: 't1', name: 'annas-claude', board_id: 'b1', role: 'editor', user_id: 'u-anna', user_email: 'anna@firma.de', kennung: 'aaaaaaaaaa', expires_at: ZUKUNFT },
    ];
    revokePersonAntwort = { ok: true, status: 200, body: {} };
    setSelbstAntwort = { ok: true, status: 200, body: {} };
    revokePersonSpy.mockClear(); setSelbstSpy.mockClear();
    globalThis.confirm = vi.fn(() => true);
  });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; delete globalThis.confirm; });

  it('„alle einer Person widerrufen“ scheitert: Fehler sichtbar (früher stumm)', async () => {
    revokePersonAntwort = { ok: false, status: 500, body: null };
    const b = await oeffneSektion('agents');
    const btn = [...b.querySelectorAll('button')].find(x => x.textContent.includes('anna@firma.de'));
    btn.click();
    await ruhe(); await ruhe();
    expect(revokePersonSpy).toHaveBeenCalled();
    expect(btn.disabled).toBe(false);
    const msg = [...b.querySelectorAll('.admin-row-msg--err')];
    expect(msg.length).toBe(1);
    expect(msg[0].textContent).not.toBe('');
  });

  it('Widerruf abgebrochen (Rückfrage verneint): Hinweis „Abgebrochen“, kein Request', async () => {
    globalThis.confirm = vi.fn(() => false);
    const b = await oeffneSektion('agents');
    [...b.querySelectorAll('button')].find(x => x.textContent.includes('anna@firma.de')).click();
    await ruhe();
    expect(revokePersonSpy).not.toHaveBeenCalled();
    expect(b.querySelector('.admin-row-msg--busy').textContent).toMatch(/Abgebrochen/);
  });

  it('Widerruf gelungen: Bestätigung steht nach dem Neuaufbau der Liste da', async () => {
    const b = await oeffneSektion('agents');
    [...b.querySelectorAll('button')].find(x => x.textContent.includes('anna@firma.de')).click();
    await ruhe(); await ruhe(); await ruhe();
    const ok = document.querySelector('.admin-body .admin-flash--ok');
    expect(ok).toBeTruthy();
    expect(ok.textContent).toContain('anna@firma.de');
  });

  it('Einzel-Widerruf sendet die Token-id, nicht den Namen (Server-Nacharbeit A, F-1434)', async () => {
    const { AdminClient } = await import('../src/admin-client.js');
    AdminClient.revokeAgentToken.mockClear();
    const b = await oeffneSektion('agents');
    [...b.querySelectorAll('.admin-agents-table button')].find(x => x.textContent.includes(I18n.t('admin.agents.btn-revoke'))).click();
    await ruhe(); await ruhe();
    expect(AdminClient.revokeAgentToken).toHaveBeenCalledWith('t1');
  });

  it('Selbstbedienungs-Schalter: Erfolg bestätigt, Fehler setzt zurück und meldet', async () => {
    const b = await oeffneSektion('agents');
    const chk = b.querySelector('.admin-agents-switch input');
    chk.checked = true;
    chk.dispatchEvent(new Event('change'));
    await ruhe();
    expect(b.querySelector('.admin-agents-switch .admin-row-msg--ok')).toBeTruthy();

    setSelbstAntwort = { ok: false, status: 500, body: null };
    chk.checked = false;
    chk.dispatchEvent(new Event('change'));
    await ruhe();
    expect(chk.checked).toBe(true);
    expect(b.querySelector('.admin-agents-switch .admin-row-msg--err')).toBeTruthy();
  });
});

describeServer('Meine Agenten-Zugänge: Rückmeldung (Karte #627)', () => {
  let fetchSpy, liste;
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
    liste = { tokens: [], boards: [{ id: 'b1', title: 'Team', role: 'editor' }], gueltig_standard: 90, gueltig_max: 365 };
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, opt = {}) => {
      if (opt.method === 'POST') {
        liste.tokens = [{ id: 't1', name: 'annas-claude', board_id: 'b1', role: 'editor', kennung: 'kkkkkkkkkk', expires_at: ZUKUNFT }];
        return { ok: true, status: 201, json: async () => ({ token: 'abt_kkkkkkkkkk_GEHEIMGEHEIMGEHEIM' }) }; // abzug:testwert
      }
      return { ok: true, status: 200, json: async () => liste };
    });
    globalThis.confirm = vi.fn(() => true);
  });
  afterEach(() => { AgentZugaengeDialog.close(); fetchSpy.mockRestore(); delete globalThis.confirm; delete navigator.clipboard; });

  it('ungültiger Name: Hinweis rot, Feld markiert, Knopf leuchtet rot, kein Request', async () => {
    AgentZugaengeDialog.open(); await ruhe();
    const panel = document.querySelector('.agz-panel');
    const name = panel.querySelector('.agz-form input:not([type=number])');
    name.value = 'Mit Leerzeichen';
    const btn = panel.querySelector('.agz-btn--primary');
    btn.click(); await ruhe();
    expect(fetchSpy.mock.calls.some(c => c[1]?.method === 'POST')).toBe(false);
    const m = panel.querySelector('.agz-anlage .agz-meldung');
    expect(m.classList.contains('agz-meldung--err')).toBe(true);
    expect(name.getAttribute('aria-invalid')).toBe('true');
    expect(btn.classList.contains('agz-btn--err')).toBe(true);
  });

  it('Serverfehler beim Anlegen: Meldung rot, Knopf wieder frei', async () => {
    fetchSpy.mockImplementation(async (url, opt = {}) => opt.method === 'POST'
      ? { ok: false, status: 409, json: async () => ({}) }
      : { ok: true, status: 200, json: async () => liste });
    AgentZugaengeDialog.open(); await ruhe();
    const panel = document.querySelector('.agz-panel');
    panel.querySelector('.agz-form input:not([type=number])').value = 'annas-claude';
    const btn = panel.querySelector('.agz-btn--primary');
    btn.click(); await ruhe(); await ruhe();
    expect(btn.disabled).toBe(false);
    expect(panel.querySelector('.agz-anlage .agz-meldung').classList.contains('agz-meldung--err')).toBe(true);
  });

  it('Anlegen gelungen: Bestätigung nach dem Neuaufbau sichtbar', async () => {
    AgentZugaengeDialog.open(); await ruhe();
    const panel = document.querySelector('.agz-panel');
    panel.querySelector('.agz-form input:not([type=number])').value = 'annas-claude';
    panel.querySelector('.agz-btn--primary').click();
    await ruhe(); await ruhe(); await ruhe();
    const ok = document.querySelector('.agz-panel .agz-meldung--ok');
    expect(ok).toBeTruthy();
    expect(ok.textContent).toContain('annas-claude');
  });

  it('Kopieren scheitert (Zwischenablage verweigert): sichtbare Meldung statt nur Markierung', async () => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async () => { throw new Error('nein'); } } });
    AgentZugaengeDialog.open(); await ruhe();
    const panel = document.querySelector('.agz-panel');
    panel.querySelector('.agz-form input:not([type=number])').value = 'annas-claude';
    panel.querySelector('.agz-btn--primary').click();
    await ruhe(); await ruhe(); await ruhe();
    const kopieren = [...document.querySelectorAll('.agz-einmal .agz-btn')][0];
    kopieren.click();
    await ruhe();
    const m = document.querySelector('.agz-einmal .agz-meldung');
    expect(m.classList.contains('agz-meldung--err')).toBe(true);
    expect(m.textContent).toMatch(/Kopieren nicht möglich/);
    expect(kopieren.classList.contains('agz-btn--err')).toBe(true);
  });
});

/* F-1450 (Sprint-80-Kritiker K-4): „Laden“ meldete bei 403/404/500 „✓ Liste geladen.“
   in Grün, der Fehler stand am Dialogende, bei 503 der alte Secrets-Text. */
describeServer('Backup-Reiter: „Laden“ mit Serverfehler (F-1450)', () => {
  beforeEach(() => { I18n.setLang('de'); document.body.innerHTML = ''; });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; delete globalThis.fetch; });

  async function laden(antwort) {
    globalThis.fetch = vi.fn(async () => antwort);
    const b = await oeffneSektion('backup');
    b.querySelector('input.admin-form-input').value = 'board-1';
    b.querySelectorAll('.admin-actions')[0].querySelectorAll('.admin-btn')[0].click();
    await ruhe(); await ruhe(); await ruhe();
    const flash = [...b.querySelectorAll(':scope > .admin-flash')][0];
    return { b, flash };
  }

  for (const status of [403, 404, 500]) {
    it(`${status}: Meldungszeile rot (err/alert), kein „Liste geladen“, Fehler im Listenbereich`, async () => {
      const { b, flash } = await laden({ ok: false, status, json: async () => ({}) });
      expect(flash.classList.contains('admin-flash--err')).toBe(true);
      expect(flash.classList.contains('admin-flash--ok')).toBe(false);
      expect(flash.getAttribute('role')).toBe('alert');
      expect(flash.textContent).not.toBe(I18n.t('admin.backup.msg-loaded'));
      const listFehler = b.querySelector('.admin-error');
      expect(listFehler).toBeTruthy();
      /* der Fehler steht im Listenbereich, nicht als eigener Absatz am Dialogende */
      expect(listFehler.parentElement).not.toBe(b);
    });
  }

  it('503 mit Grund: der Server-Grund steht in der Meldung, ABOARD_SECRET_KEY nicht', async () => {
    const { flash } = await laden({ ok: false, status: 503, json: async () => ({ error: 'board-backup ist deaktiviert (backup_board.enabled=false)' }) });
    expect(flash.textContent).toContain('backup_board.enabled=false');
    expect(flash.textContent).not.toContain('ABOARD_SECRET_KEY');
    expect(flash.classList.contains('admin-flash--err')).toBe(true);
  });

  it('Erfolg bleibt Erfolg: 200 → grüne „Liste geladen“-Meldung', async () => {
    const { flash } = await laden({ ok: true, status: 200, json: async () => ({ files: [] }) });
    expect(flash.classList.contains('admin-flash--ok')).toBe(true);
    expect(flash.textContent).toBe(I18n.t('admin.backup.msg-loaded'));
  });
});
