/* @vitest-environment happy-dom */

/**
 * F-389 — Wettlauf im Cross-Board-Transfer-Picker (Karte #343).
 *
 * Der `change`-Handler des Board-Dropdowns holt die Spalten des Ziel-Boards
 * per `await plugin.peekBoardColumns(id)`. Ohne Generations-Marke gewinnt die
 * Antwort, die zuletzt EINTRIFFT — nicht die zum zuletzt GEWÄHLTEN Board.
 * Bei schnellen Wechseln (A→B→A) landen dann fremde Spalten im Dropdown,
 * während das Board-Dropdown etwas anderes anzeigt.
 *
 * Beide Schutzmechanismen werden einzeln geprüft:
 *   - Generations-Marke → verspätete Antwort für DASSELBE Board (A→B→A)
 *   - boardSel.value-Abgleich → verspätete Antwort für ein ANDERES Board (A→B)
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));

let boardModeValue = 'server';
vi.mock('../src/config.js', () => ({
  Config: {
    get: vi.fn((key) => {
      if (key === 'boardMode') return boardModeValue;
      if (key === 'userName')  return 'TestUser';
      return '';
    }),
    set: vi.fn(),
  },
}));

import { EventBus }      from '../src/eventbus.js';
import { Modal }         from '../src/modal.js';
import { StoragePlugin } from '../src/storage-plugin.js';

void Modal; /* Import lädt das Modul, das sich per EventBus registriert. */

/* Kein Netz aus Tests — Offline-first gilt auch hier (Sprint-54-Nacharbeit).
 *
 * Der Config-Mock oben stellt `boardMode: 'server'` ein. Das Karten-Modal lädt
 * daraufhin server-seitige Nebendaten (Projekte, Identitäten, LLM-Anbieter) per
 * `fetch` mit RELATIVER URL. `serverUrl` ist leer (= gleicher Ursprung hinter
 * einem Reverse-Proxy), und happy-dom löst relative URLs gegen seinen
 * Default-Ursprung `localhost:3000` auf — der Testlauf produzierte dadurch ~80
 * echte Verbindungsversuche und entsprechend viele `ECONNREFUSED`-Zeilen.
 *
 * Das ist mehr als Lärm: Läuft auf der Maschine (oder im CI-Runner) irgendetwas
 * auf Port 3000, schickt der Testlauf echte Requests dorthin. Deshalb ein
 * Stub, der jeden Versuch abfängt und eine leere, aber wohlgeformte Antwort
 * liefert — der Picker braucht diese Nebendaten nicht.
 */
const _origFetch = globalThis.fetch;
beforeEach(() => {
  globalThis.fetch = vi.fn(async () => new Response('{}', {
    status: 200, headers: { 'Content-Type': 'application/json' },
  }));
});
afterEach(() => { globalThis.fetch = _origFetch; });

/** Lässt Mikro- UND Makrotasks durch — das Modal baut sich nicht rein
    synchron auf, fünf `Promise.resolve()` genügen dafür nicht. */
const tick = () => new Promise((r) => setTimeout(r, 0));

/** Ein von Hand auflösbares Promise — erlaubt beliebige Antwort-Reihenfolge. */
function deferred() {
  let resolve;
  const promise = new Promise((r) => { resolve = r; });
  return { promise, resolve };
}

function makeCard(overrides = {}) {
  return {
    id: 'card-race-001', displayId: 42, title: 'Testkarte', description: '',
    agentContext: '', comments: [], attachments: [], links: [], activity: [],
    priority: 'mittel', labels: [], owner: '', complexity: '', effort: null,
    qa: false, critical: false, blocked: false, archived: false,
    columnId: 'col-backlog', ...overrides,
  };
}

describeServer('Transfer-Picker — out-of-order-Antworten (F-389)', () => {
  let origGetActivePlugin;
  let peekCalls;     /* [{ id, deferred }] in Aufrufreihenfolge */
  let transferCalls; /* dito für transferCard */

  beforeEach(() => {
    peekCalls = [];
    transferCalls = [];
    origGetActivePlugin = StoragePlugin.getActivePlugin;
    StoragePlugin.getActivePlugin = () => ({
      kind: 'rest',
      listKnownBoards: async () => ([
        { id: 'board-a', title: 'Board A' },
        { id: 'board-b', title: 'Board B' },
      ]),
      peekBoardColumns: (id) => {
        const d = deferred();
        peekCalls.push({ id, deferred: d });
        return d.promise;
      },
      transferCard: (...args) => {
        const d = deferred();
        transferCalls.push({ args, deferred: d });
        return d.promise;
      },
    });
  });

  afterEach(() => {
    StoragePlugin.getActivePlugin = origGetActivePlugin;
    EventBus.emit('config:language-changed'); /* Overlay-Singleton verwerfen */
    document.body.innerHTML = '';
  });

  /** Modal öffnen, Transfer-Picker aufklappen, Board-Liste abwarten. */
  async function openPicker() {
    EventBus.emit('card:edit', makeCard());
    const overlay = document.querySelector('.modal-overlay');
    overlay.querySelector('#modal-transfer-btn').click();
    /* listKnownBoards ist ein aufgelöstes Promise — ein paar Microtask-Ticks
       genügen, damit der Picker die Optionen gerendert hat. */
    for (let i = 0; i < 5; i++) await Promise.resolve();
    const boardSel = overlay.querySelector('#transfer-board-sel');
    const colSel   = overlay.querySelector('#transfer-col-sel');
    expect(boardSel, 'Board-Dropdown fehlt — Picker nicht geöffnet').toBeTruthy();
    expect(boardSel.options.length).toBeGreaterThan(1);
    return { overlay, boardSel, colSel };
  }

  async function selectBoard(boardSel, id) {
    boardSel.value = id;
    boardSel.dispatchEvent(new Event('change'));
    await Promise.resolve();
  }

  const colsOf = (sel) => [...sel.options].map(o => o.value);

  it('A→B→A: die verspätete Antwort des ERSTEN A-Requests wird verworfen', async () => {
    const { boardSel, colSel } = await openPicker();

    await selectBoard(boardSel, 'board-a'); /* Request 1 */
    await selectBoard(boardSel, 'board-b'); /* Request 2 */
    await selectBoard(boardSel, 'board-a'); /* Request 3 */
    expect(peekCalls.map(c => c.id)).toEqual(['board-a', 'board-b', 'board-a']);

    /* Request 3 (aktuell) antwortet zuerst … */
    peekCalls[2].deferred.resolve({ ok: true, columnDefs: [
      { id: 'col-aktuell', title: 'Aktuell' },
    ] });
    for (let i = 0; i < 5; i++) await Promise.resolve();
    expect(colsOf(colSel)).toEqual(['col-aktuell']);

    /* … erst danach der lange unterwegs gewesene Request 1 — für DASSELBE
       Board, der value-Abgleich greift hier also nicht. Nur die Generation
       unterscheidet die beiden. */
    peekCalls[0].deferred.resolve({ ok: true, columnDefs: [
      { id: 'col-veraltet', title: 'Veraltet' },
    ] });
    for (let i = 0; i < 5; i++) await Promise.resolve();

    expect(colsOf(colSel)).toEqual(['col-aktuell']);
    expect(colSel.value).toBe('col-aktuell');
  });

  it('A→B: die verspätete A-Antwort füllt nicht das Dropdown des B-Boards', async () => {
    const { boardSel, colSel } = await openPicker();

    await selectBoard(boardSel, 'board-a'); /* Request 1 */
    await selectBoard(boardSel, 'board-b'); /* Request 2 */

    peekCalls[1].deferred.resolve({ ok: true, columnDefs: [
      { id: 'col-b1', title: 'B-Eins' },
    ] });
    for (let i = 0; i < 5; i++) await Promise.resolve();
    expect(colsOf(colSel)).toEqual(['col-b1']);

    peekCalls[0].deferred.resolve({ ok: true, columnDefs: [
      { id: 'col-a1', title: 'A-Eins' },
    ] });
    for (let i = 0; i < 5; i++) await Promise.resolve();

    expect(boardSel.value).toBe('board-b');
    expect(colsOf(colSel)).toEqual(['col-b1']);
  });

  it('verspätete Transfer-Antwort schliesst NICHT die inzwischen offene andere Karte', async () => {
    /* Sprint-54-Frontend-Kritiker (P2). F-389 sicherte die Dropdown-Anzeige
       während der Auswahl ab — nicht den Erfolgspfad nach dem Absenden.

       Ablauf: Karte A öffnen, Transfer absenden (Antwort bleibt offen), Modal
       schliessen, Karte B öffnen. Dann antwortet der Transfer für A.

       Ohne die Prüfung ruft dieser Callback `close()` und schliesst Karte B
       ungefragt — samt `revokeTrackedUrls()` auf deren Anhang-URLs und einem
       Fokus-Sprung auf das Element, das vor Karte A aktiv war. */
    const { overlay, boardSel, colSel } = await openPicker();
    await selectBoard(boardSel, 'board-b');
    peekCalls[peekCalls.length - 1].deferred.resolve({
      ok: true, columnDefs: [{ id: 'col-x', title: 'X' }],
    });
    for (let i = 0; i < 5; i++) await Promise.resolve();

    colSel.value = 'col-x';
    /* Die Picker-Knöpfe haben keine IDs — der Absenden-Knopf ist der
       btn-primary in der Knopfreihe. */
    const okB = overlay.querySelector('.modal-transfer-btnrow .btn-primary');
    expect(okB, 'Absenden-Knopf nicht gefunden').toBeTruthy();
    okB.click();
    await Promise.resolve();
    expect(transferCalls).toHaveLength(1);

    /* Während der Transfer läuft, schliesst der Nutzer die Karte über den
       ×-Knopf und öffnet eine ANDERE. Genau der Zustand, in dem `close()`
       das Falsche träfe. (Der Escape-Weg aus dem Kritiker-Szenario endet
       gleich; entscheidend ist allein, dass `currentCard` gewechselt hat.) */
    overlay.querySelector('.btn-modal-close').click();
    await tick();
    EventBus.emit('card:edit', makeCard({ id: 'card-race-002', displayId: 43, title: 'Karte B' }));
    await tick();

    /* Zwei Messgrössen, bewusst getrennt:
       - `#43` (displayId-Badge) belegt, dass `currentCard` gewechselt hat.
       - `aria-hidden` belegt, ob `close()` gelaufen ist. Das ist der Punkt,
         auf den es ankommt.

       NICHT über `textContent` messen: `close()` räumt den Inhalt gar nicht ab,
       es setzt nur `aria-hidden`, entfernt die `--open`-Klasse und gibt die
       Anhang-URLs frei. Die erste Fassung dieses Tests prüfte `textContent` —
       und blieb deshalb auch dann grün, wenn man den Fix vollständig
       zurückbaute (Sprint-54-Verify-Lauf). Ein Test, der nichts prüft. */
    const zweitesOverlay = document.querySelector('.modal-overlay');
    expect(zweitesOverlay?.textContent,
      'Vorbedingung: das Modal muss jetzt Karte B (#43) zeigen').toContain('#43');
    expect(zweitesOverlay?.getAttribute('aria-hidden'),
      'Vorbedingung: Karte B muss offen sein').toBe('false');

    /* Jetzt kommt die Antwort für die ERSTE Karte. */
    transferCalls[0].deferred.resolve({ ok: true, status: 200, body: {} });
    await tick();

    /* Karte B muss OFFEN geblieben sein. Ohne den Fix hätte `close()`
       gegriffen: aria-hidden auf "true", `currentCard` auf null, Anhang-URLs
       freigegeben, Fokus zurück auf das Element von VOR Karte A. */
    const danach = document.querySelector('.modal-overlay');
    expect(danach?.getAttribute('aria-hidden'),
      'die verspätete Antwort hat die falsche Karte geschlossen').toBe('false');
    expect(danach?.textContent, 'und sie zeigt weiterhin Karte B').toContain('#43');
  });
});
