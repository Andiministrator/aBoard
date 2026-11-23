/* @vitest-environment happy-dom */

/**
 * Karte #637 — Server-Frontend: Anzeige, Fehlertexte, Board-Liste für Admins.
 *   F-1396  Agenten-Zugänge: Titel lesbar, Boards unterscheidbar, Zustand an/aus
 *   F-1438  Backup-503 nennt den Grund des Servers, nicht pauschal ABOARD_SECRET_KEY
 *   F-1439  Backup-Auswahl aus GET /admin/boards, Rückfall auf listKnownBoards
 *   F-1440  listKnownBoards wirft bei Fehlern (siehe auch tests/storage-plugin.test.js)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));

let alleBoardsAntwort = { ok: false, status: 404, body: null };
let bekannteBoards = [];
let selbstBoards = [];
const alleBoardsSpy = vi.fn(async () => alleBoardsAntwort);

vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    listConfig: vi.fn(async () => ({ ok: true, body: { entries: [], secrets_enabled: true } })),
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listAgentTokens: vi.fn(async () => ({ ok: true, body: [] })),
    listAgentSelbstbedienung: vi.fn(async () => ({ ok: true, body: { boards: selbstBoards } })),
    setAgentSelbstbedienung: vi.fn(async () => ({ ok: true, body: {} })),
    listAllBoards: (...a) => alleBoardsSpy(...a),
  },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      getCurrentUser: async () => ({ name: null, email: null }),
      listKnownBoards: async () => bekannteBoards,
    }),
  },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { AgentZugaengeDialog } from '../src/agent-zugaenge-dialog.js';
import { I18n } from '../src/i18n.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const body = () => document.querySelector('.admin-body');

async function oeffneSektion(id) {
  AdminDialog.open();
  await ruhe();
  document.querySelector(`.admin-tab[data-section="${id}"]`).click();
  await ruhe();
  await ruhe();
}

describeServer('Backup-Reiter: 503-Text aus der Server-Antwort (F-1438)', () => {
  beforeEach(() => { document.body.innerHTML = ''; alleBoardsAntwort = { ok: false, status: 404, body: null }; bekannteBoards = []; });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; delete globalThis.fetch; });

  async function jetztSichern(antwort) {
    globalThis.fetch = vi.fn(async () => antwort);
    await oeffneSektion('backup');
    body().querySelector('input.admin-form-input').value = 'board-1';
    const knoepfe = body().querySelectorAll('.admin-actions')[0].querySelectorAll('.admin-btn');
    knoepfe[1].click();
    await ruhe(); await ruhe();
    return [...body().querySelectorAll('.admin-flash')].map(f => f.textContent).join('');
  }

  it('503 mit anderem Grund: der Grund steht im Text, ABOARD_SECRET_KEY nicht', async () => {
    const text = await jetztSichern({ ok: false, status: 503, json: async () => ({ error: 'board-backup ist deaktiviert (backup_board.enabled=false)' }) });
    expect(text).toContain('backup_board.enabled=false');
    expect(text).not.toContain('ABOARD_SECRET_KEY');
  });

  it('503 ohne lesbaren Grund: neutraler Text, keine Secret-Key-Behauptung', async () => {
    const text = await jetztSichern({ ok: false, status: 503, json: async () => { throw new Error('kein JSON'); } });
    expect(text).toBe(I18n.t('admin.backup.err-503-neutral'));
    expect(text).not.toContain('ABOARD_SECRET_KEY');
  });

  it('503 wegen Secret-Key: der Server-Text (mit ABOARD_SECRET_KEY) wird durchgereicht', async () => {
    const text = await jetztSichern({ ok: false, status: 503, json: async () => ({ error: 'ABOARD_SECRET_KEY ungültig' }) });
    expect(text).toContain('ABOARD_SECRET_KEY ungültig');
  });
});

describeServer('Backup-Auswahl aus /admin/boards (F-1439)', () => {
  beforeEach(() => { document.body.innerHTML = ''; globalThis.fetch = vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ files: [] }) })); alleBoardsSpy.mockClear(); });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; delete globalThis.fetch; bekannteBoards = []; });
  const optionen = () => [...body().querySelectorAll('#admin-backup-board-pick option')].map(o => o.textContent);

  it('listet auch Boards ohne ACL (Seed-Board default), die GET /boards nicht liefert', async () => {
    alleBoardsAntwort = { ok: true, status: 200, body: { boards: [
      { id: 'default', title: 'Seed', updated_at: 5, hat_acl: false, owners: [] },
      { id: 'b-anna', title: 'Annas Board', updated_at: 1, hat_acl: true, owners: ['anna@example.com'] },
    ] } };
    bekannteBoards = [{ id: 'b-anna', name: 'Annas Board' }];
    await oeffneSektion('backup');
    expect(alleBoardsSpy).toHaveBeenCalled();
    expect(optionen().some(t => t.includes('Seed (default)'))).toBe(true);
    expect(optionen().some(t => t.includes('Annas Board'))).toBe(true);
  });

  it('Rückfall auf listKnownBoards, wenn der Endpunkt fehlt (404, älterer Server)', async () => {
    alleBoardsAntwort = { ok: false, status: 404, body: null };
    bekannteBoards = [{ id: 'b-anna', name: 'Annas Board' }];
    await oeffneSektion('backup');
    expect(optionen().some(t => t.includes('Annas Board'))).toBe(true);
    expect(optionen().some(t => t.includes('Seed'))).toBe(false);
  });
});

describeServer('Agenten-Zugänge: Anzeige (F-1396)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    selbstBoards = [
      { id: '0123456789abcdef0123', title: 'Board', an: false },
      { id: 'fedcba9876543210fedc', title: 'Board', an: true },
    ];
  });
  afterEach(() => { AdminDialog?.close(); AgentZugaengeDialog?.close(); document.body.innerHTML = ''; delete globalThis.fetch; });

  it('Boards gleichen Titels sind an der gekürzten ID unterscheidbar, Zustand steht als Text da', async () => {
    await oeffneSektion('agents');
    const zeilen = [...body().querySelectorAll('.admin-agents-switch')];
    expect(zeilen).toHaveLength(2);
    expect(zeilen[0].textContent).toContain('01234567…');
    expect(zeilen[1].textContent).toContain('fedcba98…');
    expect(zeilen[0].querySelector('.admin-agents-state').textContent).toBe(I18n.t('admin.agents.state-off'));
    expect(zeilen[1].querySelector('.admin-agents-state').textContent).toBe(I18n.t('admin.agents.state-on'));
    const chk = zeilen[0].querySelector('input');
    chk.checked = true;
    chk.dispatchEvent(new Event('change'));
    expect(zeilen[0].querySelector('.admin-agents-state').textContent).toBe(I18n.t('admin.agents.state-on'));
  });

  it('Dialog „Meine Agenten-Zugänge“: Kopfzeile hell hinterlegt, Titel dunkel (nicht dunkel auf dunkel)', async () => {
    globalThis.fetch = vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ tokens: [], boards: [], gueltig_standard: 90, gueltig_max: 365 }) }));
    AgentZugaengeDialog.open();
    await ruhe();
    const css = document.getElementById('agz-dialog-style').textContent;
    expect(css).toMatch(/\.agz-panel \.modal-header-row\{[^}]*background:#fff/);
    expect(css).toMatch(/\.agz-panel \.modal-dialog-title\{[^}]*color:#222/);
  });
});

/* F-1443 (Sprint-80-Kritiker V-?): Die Selbstbedienungs-Liste zeigt die Besitzer aus
   GET /admin/boards (`owners[]`); fehlt der Endpunkt, bleibt sie wie bisher. */
describeServer('Agenten-Zugänge: Besitzer in der Selbstbedienungs-Liste (F-1443)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    selbstBoards = [
      { id: '0123456789abcdef0123', title: 'Board', an: false },
      { id: 'fedcba9876543210fedc', title: 'Board', an: true },
    ];
  });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; alleBoardsAntwort = { ok: false, status: 404, body: null }; });

  it('gleichnamige Boards: der Besitzer steht in der Zeile (mehrere Owner kommagetrennt)', async () => {
    alleBoardsAntwort = { ok: true, status: 200, body: { boards: [
      { id: '0123456789abcdef0123', title: 'Board', owners: ['anna@example.com'] },
      { id: 'fedcba9876543210fedc', title: 'Board', owners: ['bob@example.com', 'cy@example.com'] },
    ] } };
    await oeffneSektion('agents');
    const zeilen = [...body().querySelectorAll('.admin-agents-switch')];
    expect(zeilen[0].querySelector('.admin-agents-owner').textContent).toBe(I18n.t('admin.agents.owners', 'anna@example.com'));
    expect(zeilen[1].querySelector('.admin-agents-owner').textContent).toContain('bob@example.com, cy@example.com');
  });

  it('Endpunkt fehlt (404, älterer Server): Liste wie bisher, ohne Besitzer, ohne Fehler', async () => {
    alleBoardsAntwort = { ok: false, status: 404, body: null };
    await oeffneSektion('agents');
    const zeilen = [...body().querySelectorAll('.admin-agents-switch')];
    expect(zeilen).toHaveLength(2);
    expect(body().querySelector('.admin-agents-owner')).toBeNull();
    expect(body().querySelector('.admin-error')).toBeNull();
  });

  it('Board ohne Owner-Eintrag (leeres owners[]): keine leere „Besitzer:“-Hülse', async () => {
    alleBoardsAntwort = { ok: true, status: 200, body: { boards: [
      { id: '0123456789abcdef0123', title: 'Board', owners: [] },
    ] } };
    await oeffneSektion('agents');
    expect(body().querySelector('.admin-agents-owner')).toBeNull();
  });
});

/* F-1463: das sichtbare Label der Board-Auswahl hängt per for/id am select. */
describeServer('Backup-Reiter: Label der Board-Auswahl ist mit dem select verbunden (F-1463)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    alleBoardsAntwort = { ok: true, status: 200, body: { boards: [{ id: 'b1', title: 'Eins', owners: [] }] } };
  });
  afterEach(() => { AdminDialog?.close(); document.body.innerHTML = ''; alleBoardsAntwort = { ok: false, status: 404, body: null }; });

  it('label[for] zeigt auf die id des select; kein doppeltes aria-label', async () => {
    await oeffneSektion('backup');
    const sel = body().querySelector('select.admin-form-input');
    expect(sel.id).toBeTruthy();
    const label = body().querySelector(`label[for="${sel.id}"]`);
    expect(label).toBeTruthy();
    expect(label.textContent).toBe(I18n.t('admin.backup.board-pick'));
    expect(sel.hasAttribute('aria-label')).toBe(false);
  });

  it('auch das Board-ID-Feld hat ein verbundenes Label', async () => {
    await oeffneSektion('backup');
    const input = body().querySelector('input.admin-form-input');
    expect(body().querySelector(`label[for="${input.id}"]`)).toBeTruthy();
  });
});
