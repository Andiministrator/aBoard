/* @vitest-environment happy-dom */

/**
 * Karte #831 (ADR-0141 Nachtrag, F-2233): Der Browser gibt eine außen
 * entkoppelte Kopie frei. Den Kopie-Stand liest er aus GET /boards/{id}/schatten
 * (Seite außen): 404 oder `kopie: false` → keine Kopie, die Spiegel-Marken
 * wirken nicht (wie IstKopie am Server). Fehler, andere Seite, kein Abruf →
 * gesperrt (fail-closed).
 *
 * Server-only (`describeServer`): Spiegel-Sperre und Schatten-Abruf gibt es
 * nur im Server-Build.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

let seite = 'aussen';
vi.mock('../src/boardpaar.js', async (orig) => ({ ...(await orig()),
  boardpaarSeite: () => Promise.resolve(seite),
  vergissBoardpaarSeite: () => {},
}));
vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));
vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName: vi.fn(() => null), listKnownBoards: vi.fn(async () => []), getInboxOpCount: vi.fn(() => 0),
    requestCompaction: vi.fn(async () => true), getChatTail: vi.fn(() => []), setChatTailListener: vi.fn(() => {}),
    setChatTailEnabled: vi.fn(async () => {}), readChatTail: vi.fn(async () => {}),
  },
}));
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }));

const { SchattenAussen } = await import('../src/schatten-aussen.js');
const { State } = await import('../src/state.js');
const { EventBus } = await import('../src/eventbus.js');
const { I18n } = await import('../src/i18n.js');
const { Renderer } = await import('../src/renderer.js');
const { spiegelGebunden, terminModus, markeWirkt, kopieFrei, _resetBoardpaarForTest } = await import('../src/boardpaar.js');

const ruhe = () => new Promise(r => setTimeout(r, 0));
async function ruhig() { for (let i = 0; i < 6; i++) await ruhe(); }

const LANE = 'L'.repeat(43);
const karte = (o = {}) => ({
  id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: [], owner: '',
  complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'col-backlog', assignees: [], ...o,
});
function kopieBoard(id = 'kopie-1') {
  return {
    id, title: 'Kopie', identities: [],
    columnDefs: [{ id: 'col-backlog', title: 'Backlog', spiegel: { id: 'col-backlog' } }],
    sprints: [{ id: 'aussen-lane', title: 'Sprint innen', kind: 'sprint', spiegel: { id: LANE },
      cards: [karte({ id: 'sp', spiegel: { id: 'i1' } })] }],
  };
}

let antwort;
function json(status, body) {
  return { status, ok: status >= 200 && status < 300, json: async () => body };
}
class EsStub {
  constructor() { this.readyState = 1; }
  addEventListener() {}
  close() { this.readyState = 2; }
}

let zeichnungen;
const zaehle = () => { zeichnungen++; };
beforeEach(() => {
  I18n.setLang('de');
  seite = 'aussen';
  zeichnungen = 0;
  _resetBoardpaarForTest();
  antwort = () => json(404, { error: 'not found' });
  document.body.innerHTML = '<div id="app"><header id="board-header"></header><div id="board-columns"></div></div>';
  vi.stubGlobal('EventSource', EsStub);
  vi.stubGlobal('fetch', vi.fn(async (url) => (String(url).includes('/schatten') ? antwort() : json(200, {}))));
  EventBus.on('boardpaar:kopie-frei', zaehle);
});
afterEach(() => {
  EventBus.off('boardpaar:kopie-frei', zaehle);
  SchattenAussen._resetForTest();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function oeffne(board = kopieBoard()) {
  SchattenAussen.init();
  State.set({ version: '2', board });
  await ruhig();
}
const sp = () => State.get().board.sprints[0].cards[0];
const gesperrt = () => spiegelGebunden(sp(), 'aussen-lane') && markeWirkt(State.get().board.sprints[0]) &&
  terminModus({ id: 't', spiegel: { id: 'x' } }) === 'lesen';

describeServer('#831 F-2233: entkoppelte Kopie im Browser frei', () => {
  it('ohne Antwort des Servers ist die Kopie gesperrt (Ausgangslage)', () => {
    State.set({ version: '2', board: kopieBoard() });
    expect(kopieFrei()).toBe(false);
    expect(gesperrt()).toBe(true);
  });

  it('404 auf der Seite außen (außen entkoppelt): Marken wirken nicht, Board neu gezeichnet', async () => {
    await oeffne();
    expect(kopieFrei()).toBe(true);
    expect(spiegelGebunden(sp(), 'aussen-lane')).toBe(false);
    expect(markeWirkt(State.get().board.sprints[0])).toBe(false);
    expect(terminModus({ id: 't', spiegel: { id: 'x' } })).toBeNull();
    expect(zeichnungen).toBe(1);
    Renderer.renderBoard(State.get());
    const el = document.querySelector('.card[data-id="sp"]');
    expect(el.classList.contains('card--gebunden')).toBe(false);
    expect(el.classList.contains('card--spiegel')).toBe(false);
    expect(el.querySelector('.card-spiegel-badge')).toBeNull();
    expect(document.querySelector('.sprint-header--spiegel')).toBeNull();
    expect(document.querySelector('.column--spiegel')).toBeNull();
  });

  it('200 mit kopie: false (Stufe-1-Kopplung) heißt ebenfalls frei', async () => {
    antwort = () => json(200, { letzter_abgleich: null, schatten: [], kopie: false });
    await oeffne();
    expect(kopieFrei()).toBe(true);
    expect(gesperrt()).toBe(false);
  });

  it('200 mit kopie: true bleibt gesperrt, im Renderer gebunden', async () => {
    antwort = () => json(200, { letzter_abgleich: null, schatten: [], kopie: true });
    await oeffne();
    expect(kopieFrei()).toBe(false);
    expect(gesperrt()).toBe(true);
    Renderer.renderBoard(State.get());
    expect(document.querySelector('.card[data-id="sp"]').classList.contains('card--gebunden')).toBe(true);
  });

  it('Fehler (503, Netz, kaputte Antwort) bleibt gesperrt — fail-closed', async () => {
    for (const a of [() => json(503, {}), () => { throw new Error('netz'); }, () => json(200, { kaputt: 1 })]) {
      SchattenAussen._resetForTest();
      antwort = a;
      await oeffne();
      expect(kopieFrei()).toBe(false);
      expect(gesperrt()).toBe(true);
    }
  });

  it('Seite innen/aus: kein Abruf, gesperrt', async () => {
    for (const s of ['innen', 'aus']) {
      SchattenAussen._resetForTest();
      seite = s;
      await oeffne();
      expect(kopieFrei()).toBe(false);
      expect(gesperrt()).toBe(true);
    }
  });

  it('frei gilt nur für das abgefragte Board, ein späterer Fehler sperrt wieder', async () => {
    await oeffne();
    expect(kopieFrei()).toBe(true);
    /* Board-Wechsel: der Stand des alten Boards zählt nicht. */
    antwort = () => json(503, {});
    State.set({ version: '2', board: kopieBoard('kopie-2') });
    await ruhig();
    expect(kopieFrei()).toBe(false);
    expect(gesperrt()).toBe(true);
    /* Zurück auf eine freie Kopie, dann scheitert der nächste Abruf. */
    antwort = () => json(404, {});
    State.set({ version: '2', board: kopieBoard('kopie-1') });
    await ruhig();
    expect(kopieFrei()).toBe(true);
    antwort = () => json(503, {});
    await SchattenAussen.ladeJetzt();
    expect(kopieFrei()).toBe(false);
  });

  it('nach einem Board-Wechsel zählt ein alter Freistand nicht, solange die neue Antwort aussteht', async () => {
    await oeffne();
    expect(kopieFrei()).toBe(true);
    /* Ab jetzt hängt jede Antwort: weder kopie-2 noch die Rückkehr bekommt eine. */
    antwort = () => new Promise(() => {});
    State.set({ version: '2', board: kopieBoard('kopie-2') });
    await ruhig();
    /* Zurück zur früher freien Kopie. */
    State.set({ version: '2', board: kopieBoard('kopie-1') });
    await ruhig();
    expect(kopieFrei()).toBe(false);
    expect(gesperrt()).toBe(true);
  });
});
