/* @vitest-environment happy-dom */

/**
 * Karte #754 (ADR-0139 §5) — „Meine Agenten-Zugänge“ für mehrere Boards:
 *   1. Auswahl mehrerer Boards schickt `boards: [ids]`, keine Auswahl → Meldung,
 *      keine Anfrage.
 *   2. „Alle diese Boards“ schickt `alle_boards: true`.
 *   3. Ein einzelnes Board schickt weiter `board` (unverändert).
 *   4. Die Tabelle zeigt einen Mehr-Board-Zugang als „N Boards: Titel, …“.
 *   5. Der Admin-Dialog zeigt die Board-Liste eines solchen Tokens.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));
let adminTokens = [];
vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listAgentTokens: vi.fn(async () => ({ ok: true, body: adminTokens })),
    listAgentSelbstbedienung: vi.fn(async () => ({ ok: true, body: { boards: [
      { id: 'b1', title: 'Team', an: true }, { id: 'b2', title: 'Technik', an: true }] } })),
  },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({ name: null, email: null }) }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { AgentZugaengeDialog } from '../src/agent-zugaenge-dialog.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
const ZUKUNFT = '2099-01-01T00:00:00Z';

describeServer('Agenten-Zugang für mehrere Boards (Karte #754)', () => {
  let fetchSpy, gesendet, liste;

  beforeEach(() => {
    document.body.innerHTML = '';
    gesendet = [];
    liste = { tokens: [], boards: [{ id: 'b1', title: 'Team', role: 'editor' }, { id: 'b2', title: 'Technik', role: 'owner' }],
      gueltig_standard: 90, gueltig_max: 365 };
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, opt = {}) => {
      if (opt.method === 'POST') {
        const body = JSON.parse(opt.body);
        gesendet.push(body);
        const boards = body.alle_boards ? ['b1', 'b2'] : body.boards;
        const t = boards
          ? { id: 't1', name: body.name, board_id: '', mehrere: true, boards, role: 'editor', kennung: 'kkkkkkkkkk', expires_at: ZUKUNFT }
          : { id: 't1', name: body.name, board_id: body.board, role: 'editor', kennung: 'kkkkkkkkkk', expires_at: ZUKUNFT };
        liste.tokens = [t];
        return { ok: true, status: 201, json: async () => ({ token: 'abt_kkkkkkkkkk_X', agent: t }) }; // abzug:testwert
      }
      return { ok: true, status: 200, json: async () => liste };
    });
  });
  afterEach(() => {
    AgentZugaengeDialog.close();
    fetchSpy.mockRestore();
  });

  async function oeffnen() {
    AgentZugaengeDialog.open();
    await ruhe(); await ruhe();
    const sel = document.querySelector('.agz-form select');
    const name = document.querySelector('.agz-form input[maxlength="63"]');
    const btn = document.querySelector('.agz-btn--primary');
    return { sel, name, btn };
  }
  async function waehle(sel, wert) {
    sel.value = wert;
    sel.dispatchEvent(new Event('change'));
  }

  it('bietet Auswahl und „alle“ an; Auswahl schickt die angekreuzten Boards', async () => {
    const { sel, name, btn } = await oeffnen();
    const optionen = [...sel.options].map(o => o.textContent);
    expect(optionen).toHaveLength(4);
    expect(optionen[3]).toContain('2');
    const auswahl = document.querySelector('.agz-auswahl');
    expect(auswahl.hasAttribute('hidden')).toBe(true);
    await waehle(sel, sel.options[2].value);
    expect(auswahl.hasAttribute('hidden')).toBe(false);
    name.value = 'anna-mehr';
    // Nichts angekreuzt → keine Anfrage, Meldung.
    btn.click();
    await ruhe();
    expect(gesendet).toHaveLength(0);
    expect(document.querySelector('.agz-anlage .agz-meldung').textContent).toMatch(/mindestens ein Board|at least one board/);
    auswahl.querySelectorAll('input[type=checkbox]')[1].checked = true;
    btn.click();
    await ruhe(); await ruhe(); await ruhe();
    expect(gesendet).toEqual([{ name: 'anna-mehr', role: 'editor', gueltig_tage: 90, boards: ['b2'] }]);
    // Tabelle: „1 Boards: Technik“ statt einer leeren Board-Zelle.
    const zelle = document.querySelector('.agz-tabelle tbody td:nth-child(2)');
    expect(zelle.textContent).toMatch(/^1 [Bb]oards \(.+\): Technik$/);
  });

  it('„alle diese Boards“ schickt alle_boards, ein einzelnes Board weiter board', async () => {
    let { sel, name, btn } = await oeffnen();
    await waehle(sel, sel.options[3].value);
    name.value = 'anna-alle';
    btn.click();
    await ruhe(); await ruhe(); await ruhe();
    expect(gesendet[0]).toEqual({ name: 'anna-alle', role: 'editor', gueltig_tage: 90, alle_boards: true });
    expect(document.querySelector('.agz-tabelle tbody td:nth-child(2)').textContent).toMatch(/^2 [Bb]oards \(.+\): Team, Technik$/);
    AgentZugaengeDialog.close();
    ({ sel, name, btn } = await oeffnen());
    await waehle(sel, 'b1');
    name.value = 'anna-eins';
    btn.click();
    await ruhe(); await ruhe(); await ruhe();
    expect(gesendet[1]).toEqual({ name: 'anna-eins', role: 'editor', gueltig_tage: 90, board: 'b1' });
  });

  it('Service-Board: einzeln wählbar, aber nicht in Auswahl und „alle“ (K-4)', async () => {
    liste.boards.push({ id: 'service', title: 'Mail-Eingang', role: 'owner' });
    const { sel } = await oeffnen();
    const werte = [...sel.options].map(o => o.value);
    expect(werte).toContain('service');
    expect(sel.options[sel.options.length - 1].textContent).toMatch(/\(2[,)]/);
    const ids = [...document.querySelectorAll('.agz-auswahl input[type=checkbox]')].map(c => c.value);
    expect(ids).toEqual(['b1', 'b2']);
  });

  it('Admin-Dialog zeigt die Board-Liste mit Titeln', async () => {
    adminTokens = [{ id: 'x', name: 'anna-mehr', board_id: '', mehrere: true, boards: ['b1', 'b9'], role: 'editor',
      user_email: 'anna@firma.de', expires_at: ZUKUNFT }];
    AdminDialog.open();
    await ruhe();
    document.querySelector('.admin-tab[data-section="agents"]').click();
    for (let i = 0; i < 4; i++) await ruhe();
    const zeile = [...document.querySelectorAll('.admin-agents-table tbody tr')][0];
    expect(zeile.children[1].textContent).toMatch(/^2 [Bb]oards \(.+\): Team, b9$/);
    AdminDialog.close?.();
  });
});
