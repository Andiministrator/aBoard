/* @vitest-environment happy-dom */

/**
 * Karte #672 (ADR-0133 §7) — Knopf „In Open WebUI weiterbearbeiten“.
 *
 * Nur im Server-Build (DCE im Solo-Template) und nur, wenn /me eine
 * `owui_url` meldet. Die Adresse trägt `submit=false`, Kartennummer und Titel,
 * aber NIE den Karteninhalt.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));
vi.mock('../src/config.js', () => ({
  Config: {
    get: vi.fn((key) => (key === 'boardMode' ? 'server' : key === 'userName' ? 'TestUser' : '')),
    set: vi.fn(),
  },
}));

import { EventBus }      from '../src/eventbus.js';
import { Modal }         from '../src/modal.js';
import { StoragePlugin } from '../src/storage-plugin.js';

void Modal;

const _origFetch = globalThis.fetch;
beforeEach(() => {
  globalThis.fetch = vi.fn(async () => new Response('{}', {
    status: 200, headers: { 'Content-Type': 'application/json' },
  }));
});
afterEach(() => { globalThis.fetch = _origFetch; });

const tick = async () => { for (let i = 0; i < 10; i++) await new Promise(r => setTimeout(r, 0)); };

function makeCard(overrides = {}) {
  return {
    id: 'card-owui-001', displayId: 672, title: 'Testkarte', description: 'GEHEIME-BESCHREIBUNG',
    agentContext: '', comments: [{ id: 'c1', author: 'x', text: 'GEHEIMER-KOMMENTAR', created: '2026-10-04' }],
    attachments: [], links: [], activity: [],
    priority: 'mittel', labels: [], owner: '', complexity: '', effort: null,
    qa: false, critical: false, blocked: false, archived: false,
    columnId: 'col-backlog', ...overrides,
  };
}

describeServer('Open-WebUI-Knopf (#672)', () => {
  let origGetActivePlugin;
  let openSpy;
  let origOpen;

  beforeEach(() => {
    origGetActivePlugin = StoragePlugin.getActivePlugin;
    origOpen = window.open;
    openSpy = vi.fn();
    window.open = openSpy;
    vi.resetModules();
  });
  afterEach(() => {
    StoragePlugin.getActivePlugin = origGetActivePlugin;
    window.open = origOpen;
    EventBus.emit('config:language-changed');
    document.body.innerHTML = '';
  });

  async function oeffne(me, card = makeCard()) {
    /* Neues Modul, damit der gemerkte owuiUrl-Wert nicht aus dem Vortest stammt. */
    const { EventBus: EB } = await import('../src/eventbus.js');
    const { StoragePlugin: SP } = await import('../src/storage-plugin.js');
    await import('../src/modal.js');
    const { State: St } = await import('../src/state.js');
    St.set({ board: { id: 'b-4711', title: 'Titel doppelt', columnDefs: [], sprints: [] } });
    SP.getActivePlugin = () => ({ kind: 'rest', getCurrentUser: async () => me });
    EB.emit('card:edit', card);
    await tick();
    return document.querySelector('.modal-overlay').querySelector('#modal-owui-btn');
  }

  it('ohne owui_url bleibt der Knopf verborgen', async () => {
    const btn = await oeffne({ name: null, email: null, is_admin: false, owui_url: '' });
    expect(btn).toBeTruthy();
    expect(btn.style.display).toBe('none');
  });

  it('mit owui_url ist er sichtbar; Klick öffnet submit=false + Nummer + Titel, ohne Karteninhalt', async () => {
    const btn = await oeffne({ name: 'A', email: 'a@x.de', is_admin: false, owui_url: 'https://chat.example.de/' });
    expect(btn.style.display).toBe('');
    btn.click();
    expect(openSpy).toHaveBeenCalledTimes(1);
    const [url, ziel, feat] = openSpy.mock.calls[0];
    expect(ziel).toBe('_blank');
    expect(feat).toContain('noopener');
    const u = new URL(url);
    expect(u.origin + u.pathname).toBe('https://chat.example.de/');
    expect(u.searchParams.get('submit')).toBe('false');
    const q = u.searchParams.get('q');
    expect(q).toContain('#672');
    expect(q).toContain('Testkarte');
    expect(q).toContain('karte_zeigen');
    expect(q).toContain('boardId b-4711');   /* K3-7: Titel sind nicht eindeutig, die boardId schon */
    expect(url).not.toContain('GEHEIME');
    expect(decodeURIComponent(url)).not.toContain('GEHEIME');
  });

  it('kürzt sehr lange Titel auf 200 Zeichen', async () => {
    const btn = await oeffne({ owui_url: 'https://chat.example.de' }, makeCard({ title: 'T'.repeat(500) }));
    btn.click();
    const q = new URL(openSpy.mock.calls[0][0]).searchParams.get('q');
    expect(q).toContain('T'.repeat(200));
    expect(q).not.toContain('T'.repeat(201));
  });

  it('Sprint-Meta-Karten bekommen keinen sichtbaren Knopf', async () => {
    const btn = await oeffne({ owui_url: 'https://chat.example.de' }, makeCard({ kind: 'sprint-meta' }));
    expect(btn.style.display).toBe('none');
  });
});
