/* @vitest-environment happy-dom */

/**
 * Karte #608 (ADR-0130 §6) — Oberfläche der Agenten-Zugänge:
 *   1. „Meine Agenten-Zugänge“: Liste, Anlage, Token GENAU EINMAL sichtbar,
 *      nie in der Konsole, nach dem Schliessen aus dem DOM.
 *   2. Admin-Abschnitt: Tokens mit Person/Ablauf, Widerruf je Token und je
 *      Person, Selbstbedienungs-Schalter je Board.
 *   3. Kommentar eines Agenten zeigt „(für <Name>)“.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));

const revokeTokenSpy = vi.fn(async () => ({ ok: true, body: {} }));
const revokePersonSpy = vi.fn(async () => ({ ok: true, body: { widerrufen: 1 } }));
const setSelbstSpy = vi.fn(async () => ({ ok: true, body: {} }));
let adminTokens = [];
vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listAgentTokens: vi.fn(async () => ({ ok: true, body: adminTokens })),
    revokeAgentToken: (...a) => revokeTokenSpy(...a),
    revokeAgentTokensOfUser: (...a) => revokePersonSpy(...a),
    listAgentSelbstbedienung: vi.fn(async () => ({ ok: true, body: { boards: [{ id: 'b1', title: 'Team', an: false }] } })),
    setAgentSelbstbedienung: (...a) => setSelbstSpy(...a),
  },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({ name: null, email: null }) }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { AgentZugaengeDialog } from '../src/agent-zugaenge-dialog.js';
import { nameForEmail } from '../src/identity.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
/* Das Token steht nur in der value-EIGENSCHAFT des Feldes, nicht im HTML-Text —
   eine Prüfung über innerHTML wäre immer „nicht enthalten“ (Verify V-2,
   beim Gegenprobieren gefunden). */
const tokenSichtbar = () => [...document.querySelectorAll('input')].some(i => String(i.value).includes('GEHEIM'));
const ZUKUNFT = '2099-01-01T00:00:00Z';

describe('nameForEmail', () => {
  it('Label der Identität mit dieser E-Mail, sonst der Teil vor dem @', () => {
    const board = { identities: [{ kind: 'user', label: 'Andi', email: 'Andi@Firma.de' }] };
    expect(nameForEmail('andi@firma.de', board)).toBe('Andi');
    expect(nameForEmail('bert@firma.de', board)).toBe('bert');
    expect(nameForEmail('', board)).toBe('');
  });
});

describeServer('Meine Agenten-Zugänge (Karte #608)', () => {
  let fetchSpy, konsole;
  const TOKEN = 'abt_kkkkkkkkkk_GEHEIMGEHEIMGEHEIMGEHEIMGEHEIMGEHEIMGEHEIMG'; // abzug:testwert
  let liste;

  beforeEach(() => {
    document.body.innerHTML = '';
    liste = { tokens: [], boards: [{ id: 'b1', title: 'Team', role: 'editor' }], gueltig_standard: 90, gueltig_max: 365 };
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, opt = {}) => {
      if (opt.method === 'POST') {
        liste.tokens = [{ id: 't1', name: 'annas-claude', board_id: 'b1', role: 'editor', kennung: 'kkkkkkkkkk', expires_at: ZUKUNFT }];
        return { ok: true, status: 201, json: async () => ({ token: TOKEN, agent: liste.tokens[0] }) };
      }
      if (opt.method === 'DELETE') return { ok: true, status: 200, json: async () => ({}) };
      return { ok: true, status: 200, json: async () => liste };
    });
    konsole = ['log', 'info', 'warn', 'error', 'debug'].map(m => vi.spyOn(console, m).mockImplementation(() => {}));
  });
  afterEach(() => {
    AgentZugaengeDialog.close();
    fetchSpy.mockRestore();
    konsole.forEach(k => k.mockRestore());
  });

  it('legt an, zeigt das Token einmal, loggt es nie und entfernt es beim Schliessen', async () => {
    AgentZugaengeDialog.open();
    await ruhe();
    const panel = document.querySelector('.agz-panel');
    expect(panel.textContent).toContain('Team');
    const name = panel.querySelector('.agz-form input:not([type=number])');
    name.value = 'annas-claude';
    panel.querySelector('.agz-btn--primary').click();
    await ruhe(); await ruhe();
    const feld = document.querySelector('.agz-token');
    expect(feld?.value).toBe(TOKEN);
    const post = fetchSpy.mock.calls.find(c => c[1]?.method === 'POST');
    expect(JSON.parse(post[1].body)).toEqual({ board: 'b1', name: 'annas-claude', role: 'editor', gueltig_tage: 90 });
    /* Die Tabelle zeigt nur die Kennung, nie das Geheimnis. */
    expect(document.querySelector('.agz-tabelle').textContent).toContain('abt_kkkkkkkkkk_…');
    expect(document.querySelector('.agz-tabelle').textContent).not.toContain('GEHEIM');
    for (const k of konsole) for (const c of k.mock.calls) expect(JSON.stringify(c)).not.toContain('GEHEIM');
    AgentZugaengeDialog.close();
    expect(tokenSichtbar()).toBe(false);
  });

  it('ungültiger Name: kein Request, Meldung', async () => {
    AgentZugaengeDialog.open();
    await ruhe();
    document.querySelector('.agz-form input:not([type=number])').value = 'Mit Leerzeichen';
    document.querySelector('.agz-btn--primary').click();
    await ruhe();
    expect(fetchSpy.mock.calls.some(c => c[1]?.method === 'POST')).toBe(false);
    expect(document.querySelector('.agz-meldung').textContent).not.toBe('');
  });

  it('ohne Board mit Selbstbedienung: Hinweis statt Formular', async () => {
    liste.boards = [];
    AgentZugaengeDialog.open();
    await ruhe();
    expect(document.querySelector('.agz-form')).toBeNull();
    expect(document.querySelector('.agz-anlage').textContent).toMatch(/Admin|admin/);
  });

  it('Zustand: aktiv / widerrufen / abgelaufen', () => {
    const { zustand } = AgentZugaengeDialog.__testing__;
    expect(zustand({ expires_at: ZUKUNFT })).toBe('aktiv');
    expect(zustand({ revoked_at: '2026-01-01', expires_at: ZUKUNFT })).toBe('widerrufen');
    expect(zustand({ expires_at: '2020-01-01T00:00:00Z' })).toBe('abgelaufen');
    expect(zustand({})).toBe('aktiv');   /* Altbestand ohne Ablauf */
  });
});

describeServer('Admin-Abschnitt Agenten-Zugänge (Karte #608)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    revokeTokenSpy.mockClear(); revokePersonSpy.mockClear(); setSelbstSpy.mockClear();
    adminTokens = [
      { id: 't1', name: 'annas-claude', board_id: 'b1', role: 'editor', user_id: 'u-anna', user_email: 'anna@firma.de', kennung: 'aaaaaaaaaa', expires_at: ZUKUNFT },
      { id: 't2', name: 'agent-container', board_id: 'b1', role: 'editor', created_at: '2026-01-01' },
      { id: 't3', name: 'alt', board_id: 'b1', role: 'viewer', revoked_at: '2026-02-01', expires_at: ZUKUNFT },
    ];
    globalThis.confirm = vi.fn(() => true);
  });
  afterEach(() => { AdminDialog.close?.(); delete globalThis.confirm; });

  async function oeffne() {
    AdminDialog.open();
    await ruhe();
    document.querySelector('.admin-tab[data-section="agents"]').click();
    await ruhe(); await ruhe();
    return document.querySelector('.admin-body');
  }

  it('Tabelle mit Person, Kennung, Ablauf und Status; Widerruf nur für aktive', async () => {
    const b = await oeffne();
    const text = b.textContent;
    expect(text).toContain('anna@firma.de');
    expect(text).toContain('abt_aaaaaaaaaa_…');
    expect(text).toContain('2099-01-01');
    expect(text).toMatch(/unbefristet|no expiry/);
    const zeilen = [...b.querySelectorAll('.admin-agents-table tbody tr')];
    expect(zeilen).toHaveLength(3);
    expect(zeilen[2].querySelector('button')).toBeNull();          /* widerrufen */
    zeilen[0].querySelector('button').click();
    await ruhe();
    expect(revokeTokenSpy).toHaveBeenCalledWith('t1');
  });

  it('alle Zugänge einer Person widerrufen, und der Selbstbedienungs-Schalter', async () => {
    const b = await oeffne();
    const personBtn = [...b.querySelectorAll('button')].find(x => x.textContent.includes('anna@firma.de'));
    expect(personBtn).toBeTruthy();
    personBtn.click();
    await ruhe();
    expect(revokePersonSpy).toHaveBeenCalledWith('u-anna');
    const chk = document.querySelector('.admin-agents-switch input');
    chk.checked = true;
    chk.dispatchEvent(new Event('change'));
    await ruhe();
    expect(setSelbstSpy).toHaveBeenCalledWith('b1', true);
  });
});

describeServer('Meine Agenten-Zugänge — Token überlebt Neuladen, nicht das Schliessen (K-11)', () => {
  let fetchSpy, antwortHalten;
  const TOKEN = 'abt_kkkkkkkkkk_GEHEIMGEHEIMGEHEIMGEHEIMGEHEIMGEHEIMGEHEIMG'; // abzug:testwert
  beforeEach(() => {
    document.body.innerHTML = '';
    antwortHalten = null;
    const liste = { tokens: [], boards: [{ id: 'b1', title: 'Team', role: 'editor' }], gueltig_standard: 90, gueltig_max: 365 };
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, opt = {}) => {
      if (opt.method === 'POST') {
        liste.tokens = [{ id: 't1', name: 'x', board_id: 'b1', role: 'editor', kennung: 'kkkkkkkkkk', expires_at: ZUKUNFT }];
        return { ok: true, status: 201, json: async () => ({ token: TOKEN }) };
      }
      if (opt.method === 'DELETE') return { ok: true, status: 200, json: async () => ({}) };
      if (antwortHalten) await antwortHalten;
      return { ok: true, status: 200, json: async () => liste };
    });
    globalThis.confirm = vi.fn(() => true);
  });
  afterEach(() => { AgentZugaengeDialog.close(); fetchSpy.mockRestore(); delete globalThis.confirm; });

  it('Widerruf lädt neu — das Token bleibt sichtbar; nach dem Schliessen ist es weg', async () => {
    AgentZugaengeDialog.open(); await ruhe();
    document.querySelector('.agz-form input:not([type=number])').value = 'x';
    document.querySelector('.agz-btn--primary').click();
    await ruhe(); await ruhe();
    expect(document.querySelector('.agz-token')?.value).toBe(TOKEN);
    document.querySelector('.agz-btn--danger').click();
    await ruhe(); await ruhe();
    expect(document.querySelector('.agz-token')?.value).toBe(TOKEN);
    AgentZugaengeDialog.close();
    expect(tokenSichtbar()).toBe(false);
    AgentZugaengeDialog.open(); await ruhe();
    expect(tokenSichtbar()).toBe(false);
  });

  it('Schliessen während des Ladens: nichts wird nachträglich gerendert', async () => {
    AgentZugaengeDialog.open(); await ruhe();
    document.querySelector('.agz-form input:not([type=number])').value = 'x';
    let freigeben;
    antwortHalten = new Promise(r => { freigeben = r; });
    document.querySelector('.agz-btn--primary').click();
    await ruhe();
    AgentZugaengeDialog.close();
    freigeben(); await ruhe(); await ruhe();
    expect(tokenSichtbar()).toBe(false);
  });
});

describeServer('Meine Agenten-Zugänge — Schliessen während der Anlage (Verify V-2)', () => {
  let fetchSpy, postHalten;
  const TOKEN = 'abt_kkkkkkkkkk_GEHEIMGEHEIMGEHEIMGEHEIMGEHEIMGEHEIMGEHEIMG'; // abzug:testwert
  beforeEach(() => {
    document.body.innerHTML = '';
    const liste = { tokens: [], boards: [{ id: 'b1', title: 'Team', role: 'editor' }], gueltig_standard: 90, gueltig_max: 365 };
    fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, opt = {}) => {
      if (opt.method === 'POST') { await postHalten; return { ok: true, status: 201, json: async () => ({ token: TOKEN }) }; }
      return { ok: true, status: 200, json: async () => liste };
    });
  });
  afterEach(() => { AgentZugaengeDialog.close(); fetchSpy.mockRestore(); });

  it('POST kommt nach dem Schliessen an: Token erscheint weder jetzt noch beim nächsten Öffnen', async () => {
    let freigeben;
    postHalten = new Promise(r => { freigeben = r; });
    AgentZugaengeDialog.open(); await ruhe();
    document.querySelector('.agz-form input:not([type=number])').value = 'x';
    document.querySelector('.agz-btn--primary').click();
    await ruhe();
    AgentZugaengeDialog.close();
    freigeben(); await ruhe(); await ruhe();
    expect(tokenSichtbar()).toBe(false);
    AgentZugaengeDialog.open(); await ruhe(); await ruhe();
    expect(tokenSichtbar()).toBe(false);
  });
});
