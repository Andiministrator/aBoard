/* @vitest-environment happy-dom */

/**
 * Karte #670 (ADR-0133 §6) — Admin-Abschnitt „Agenten-Zugänge“: Projekt-Zugang
 * anlegen mit Rollenauswahl editor / viewer / vorschlag. Das Token erscheint
 * GENAU EINMAL und ist nach dem Schliessen aus dem DOM.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));

let antwort = null;
let haengend = null;
const createSpy = vi.fn(async () => (haengend ? haengend : antwort));
vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listAgentTokens: vi.fn(async () => ({ ok: true, body: [] })),
    listAgentSelbstbedienung: vi.fn(async () => ({ ok: true, body: { boards: [{ id: 'b1', title: 'Team', an: false }] } })),
    setAgentSelbstbedienung: vi.fn(async () => ({ ok: true, body: {} })),
    createAgentToken: (...a) => createSpy(...a),
  },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({ name: null, email: null }) }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
/* Das Token steht nur in der value-EIGENSCHAFT des Feldes, nicht im HTML-Text. */
let scrollSpy;
const tokenSichtbar = () => [...document.querySelectorAll('input')].some(i => String(i.value).includes('GEHEIM'));

async function oeffne() {
  AdminDialog.open();
  await ruhe();
  document.querySelector('.admin-tab[data-section="agents"]').click();
  await ruhe(); await ruhe();
  return document.querySelector('.admin-body');
}

function formular(b) {
  const f = b.querySelector('.admin-agents-new');
  const [name, board, rolle, tage] = f.querySelectorAll('input, select');
  return { f, name, board, rolle, tage, btn: f.querySelector('button') };
}

describeServer('Admin: Projekt-Zugang anlegen (Karte #670)', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
    createSpy.mockClear();
    haengend = null;
    antwort = { ok: true, body: { token: 'abt_GEHEIM', agent: { name: 'recherche' } } }; // abzug:testwert
    /* happy-dom scrollt nicht — mitschneiden, welches Element sichtbar gemacht wird */
    scrollSpy = vi.spyOn(Element.prototype, 'scrollIntoView').mockImplementation(() => {});
  });
  afterEach(() => { AdminDialog.close?.(); scrollSpy.mockRestore(); });

  it('Rollenauswahl kennt vorschlag; Anlage schickt Projekt-Token ohne Person', async () => {
    const b = await oeffne();
    const { name, board, rolle, tage, btn } = formular(b);
    expect([...rolle.options].map(o => o.value)).toEqual(['editor', 'viewer', 'vorschlag', 'intern']);
    name.value = 'recherche';
    rolle.value = 'vorschlag';
    tage.value = '30';
    expect(board.value).toBe('b1');
    btn.click();
    await ruhe(); await ruhe(); await ruhe();
    expect(createSpy).toHaveBeenCalledWith({ name: 'recherche', board: 'b1', role: 'vorschlag', gueltig_tage: 30 });
    expect(createSpy.mock.calls[0][0]).not.toHaveProperty('user');
    expect(tokenSichtbar()).toBe(true);
    /* Sitzungs-QA 13.10. (QA-2): das Einmal-Token wird sichtbar gescrollt */
    const box = document.querySelector('.admin-agents-once');
    expect(scrollSpy).toHaveBeenCalledTimes(1);
    expect(scrollSpy.mock.contexts[0]).toBe(box);
  });

  it('ungültiger Name: kein Request', async () => {
    const b = await oeffne();
    const { name, btn } = formular(b);
    name.value = 'Mit Leerzeichen';
    btn.click();
    await ruhe();
    expect(createSpy).not.toHaveBeenCalled();
    expect(name.getAttribute('aria-invalid')).toBe('true');
  });

  it('Token nur einmal: nach Schliessen weg, beim Wiederöffnen nicht mehr da', async () => {
    const b = await oeffne();
    const { name, btn } = formular(b);
    name.value = 'recherche';
    btn.click();
    await ruhe(); await ruhe(); await ruhe();
    expect(tokenSichtbar()).toBe(true);
    AdminDialog.close();
    expect(tokenSichtbar()).toBe(false);
    await oeffne();
    expect(tokenSichtbar()).toBe(false);
  });

  it('Antwort kommt nach dem Schliessen: Token erscheint nicht', async () => {
    let freigeben;
    haengend = new Promise(r => { freigeben = r; });
    const b = await oeffne();
    const { name, btn } = formular(b);
    name.value = 'recherche';
    btn.click();
    await ruhe();
    AdminDialog.close();
    freigeben(antwort);
    await ruhe(); await ruhe(); await ruhe();
    expect(tokenSichtbar()).toBe(false);
    haengend = null;
    await oeffne();
    expect(tokenSichtbar()).toBe(false);
  });
});
