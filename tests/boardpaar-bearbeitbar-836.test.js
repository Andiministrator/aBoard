/* @vitest-environment happy-dom */

/**
 * Karte #836 (ADR-0141 Nachtrag „Schalter ‚bearbeitbar‘ je Kopie-Board“): Im
 * Browser ist eine Spiegelkarte nur bearbeitbar, wenn /me den Rückweg meldet
 * (Server-Schalter) UND der Board-Abruf GET /boards/{id}/schatten für das
 * offene Board `bearbeitbar: true` liefert. Sonst 'lesen' — auch bei Fehler,
 * fehlendem Feld oder einem anderen offenen Board (fail-closed).
 *
 * Server-only (`describeServer`): Spiegel-Sperre und Schatten-Abruf gibt es
 * nur im Server-Build.
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/boardpaar.js', async (orig) => ({ ...(await orig()),
  boardpaarSeite: () => Promise.resolve('aussen'),
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
const { spiegelModus, terminModus, rueckwegAn, _resetBoardpaarForTest, _setzeRueckwegForTest } = await import('../src/boardpaar.js');

const ruhe = () => new Promise(r => setTimeout(r, 0));
async function ruhig() { for (let i = 0; i < 6; i++) await ruhe(); }

const LANE = 'L'.repeat(43);
const karte = (o = {}) => ({
  id: 'sp', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: [], owner: '',
  complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'col-backlog', assignees: [], spiegel: { id: 'i1' }, ...o,
});
function kopieBoard(id = 'kopie-1') {
  return {
    id, title: 'Kopie', identities: [],
    columnDefs: [{ id: 'col-backlog', title: 'Backlog', spiegel: { id: 'col-backlog' } }],
    sprints: [{ id: 'aussen-lane', title: 'Sprint innen', kind: 'sprint', spiegel: { id: LANE }, cards: [karte()] }],
  };
}

let antworten;
function json(status, body) {
  return { status, ok: status >= 200 && status < 300, json: async () => body };
}
class EsStub {
  constructor() { this.readyState = 1; }
  addEventListener() {}
  close() { this.readyState = 2; }
}
let rueckwegEreignisse;
const zaehle = () => { rueckwegEreignisse++; };

beforeEach(() => {
  I18n.setLang('de');
  _resetBoardpaarForTest();
  rueckwegEreignisse = 0;
  /* Antwort je Board: kopie-1 nicht freigegeben, kopie-2 freigegeben. */
  antworten = {
    'kopie-1': () => json(200, { letzter_abgleich: null, schatten: [], kopie: true, bearbeitbar: false }),
    'kopie-2': () => json(200, { letzter_abgleich: null, schatten: [], kopie: true, bearbeitbar: true }),
  };
  document.body.innerHTML = '<div id="app"><header id="board-header"></header><div id="board-columns"></div></div>';
  vi.stubGlobal('EventSource', EsStub);
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    const m = /\/boards\/([^/]+)\/schatten/.exec(String(url));
    return m ? antworten[decodeURIComponent(m[1])]() : json(200, {});
  }));
  EventBus.on('boardpaar:rueckweg', zaehle);
});
afterEach(() => {
  EventBus.off('boardpaar:rueckweg', zaehle);
  SchattenAussen._resetForTest();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

async function oeffne(board) {
  SchattenAussen.init();
  State.set({ version: '2', board });
  await ruhig();
}
const sp = () => State.get().board.sprints[0].cards[0];

describeServer('#836 Schalter „bearbeitbar“ je Kopie im Browser', () => {
  it('Server-Schalter an, Board nicht freigegeben: nur lesen', async () => {
    _setzeRueckwegForTest(true, false);
    await oeffne(kopieBoard('kopie-1'));
    expect(rueckwegAn()).toBe(false);
    expect(spiegelModus(sp(), 'aussen-lane')).toBe('lesen');
    expect(terminModus({ id: 't', spiegel: { id: 'x' } })).toBe('lesen');
  });

  it('Server-Schalter an, Board freigegeben: Rückweg, und die Freigabe meldet boardpaar:rueckweg', async () => {
    _setzeRueckwegForTest(true, false);
    const vorher = rueckwegEreignisse;
    await oeffne(kopieBoard('kopie-2'));
    expect(rueckwegAn()).toBe(true);
    expect(spiegelModus(sp(), 'aussen-lane')).toBe('rueckweg');
    expect(rueckwegEreignisse).toBeGreaterThan(vorher);
  });

  it('Server-Schalter aus, Board freigegeben: nur lesen', async () => {
    await oeffne(kopieBoard('kopie-2'));
    expect(rueckwegAn()).toBe(false);
    expect(spiegelModus(sp(), 'aussen-lane')).toBe('lesen');
  });

  it('Feld fehlt, Fehler oder kaputte Antwort: nur lesen (fail-closed)', async () => {
    _setzeRueckwegForTest(true, false);
    for (const a of [() => json(200, { letzter_abgleich: null, schatten: [], kopie: true }),
      () => json(200, { letzter_abgleich: null, schatten: [], kopie: true, bearbeitbar: 'true' }),
      () => json(503, {}), () => { throw new Error('netz'); }]) {
      SchattenAussen._resetForTest();
      antworten['kopie-2'] = a;
      await oeffne(kopieBoard('kopie-2'));
      expect(spiegelModus(sp(), 'aussen-lane')).toBe('lesen');
    }
  });

  it('die Freigabe gilt nur für das Board, für das sie gemeldet wurde', async () => {
    _setzeRueckwegForTest(true, false);
    await oeffne(kopieBoard('kopie-2'));
    expect(spiegelModus(sp(), 'aussen-lane')).toBe('rueckweg');
    /* Anderes Board im Stand, ohne neue Antwort: nicht freigegeben. */
    State.set({ version: '2', board: kopieBoard('kopie-3') });
    expect(spiegelModus(sp(), 'aussen-lane')).toBe('lesen');
  });
});
