/* @vitest-environment happy-dom */

/**
 * Karte #678 (ADR-0135): Dateien an Kommentaren (Server-Modus).
 *
 * Eine Kommentar-Datei ist ein gewöhnlicher Karten-Anhang mit `commentId` —
 * gleiche Ablage, gleiche Grenze, gleiche Rechte wie die Karte. Geprüft wird:
 *   1. FOLD — `comment.delete` nimmt die Anhang-Einträge seines Kommentars
 *      (und einen darauf zeigenden Cover-Verweis) mit, sonst nichts.
 *   2. OBERFLÄCHE (nur Server-Build) — Dateien wählen, mit dem Kommentar
 *      hochladen (mit `commentId`), unter dem Kommentar anzeigen.
 *   3. OHNE SERVER — kein Dateiknopf im Kommentar-Formular.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer, describeOhneServer } from './helpers/build-flags.js';

const fake = vi.hoisted(() => ({
  hochgeladen: [],
  backend: null,
}));

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn(k => (k === 'userName' ? 'Andi' : '')), set: vi.fn() },
}));
vi.mock('../src/storage-plugin.js', async (orig) => {
  const m = await orig();
  fake.backend = {
    kartenAnhangImBoardState: () => false,
    checkUpload: () => ({ ok: true }),
    humanSize: (n) => `${n} B`,
    computeBoardBytes: () => 0,
    safeMime: (x) => x,
    uploadFile: vi.fn(async (datei, karte) => {
      fake.hochgeladen.push({ name: datei.name, cardId: karte.id });
      return { id: 'att-' + fake.hochgeladen.length, filename: datei.name, mime: 'text/plain', size: 3, created: '2026-10-04' };
    }),
    downloadAttachment: vi.fn(async () => true),
    attachmentUrl: vi.fn(async () => null),
    deleteAttachment: vi.fn(async () => {}),
  };
  return {
    ...m,
    StoragePlugin: { ...m.StoragePlugin, getActivePlugin: () => ({ attachments: () => fake.backend }) },
  };
});

import { fold } from '../src/ops.js';
import { EventBus } from '../src/eventbus.js';
import { State } from '../src/state.js';
import '../src/modal.js';

/* ── 1. Fold ─────────────────────────────────────────────────────────── */

function stand() {
  return {
    version: '2',
    board: {
      id: 'b1', title: 'T', nextDisplayId: 2,
      columnDefs: [{ id: 'col-ready', title: 'Ready', color: null }],
      sprints: [{ id: 's1', title: 'S', kind: 'sprint', cards: [{
        id: 'c1', displayId: 1, title: 'K', columnId: 'col-ready', coverAttachmentId: 'a1',
        comments: [{ id: 'k1', text: 'mit Datei' }, { id: 'k2', text: 'auch' }],
        attachments: [
          { id: 'a1', filename: 'bild.png', commentId: 'k1' },
          { id: 'a2', filename: 'b.txt', commentId: 'k2' },
          { id: 'a3', filename: 'c.txt' },
          'a4',
        ],
      }] }],
    },
  };
}
const op = (type, payload, id) => ({ id, ts: '2026-10-04T12:00:00.000Z', actor: 'Andi', schemaVersion: 1, type, payload });

describe('Karte #678 — comment.delete nimmt die Dateien seines Kommentars mit', () => {
  it('Einträge mit dieser commentId fallen, Cover-Verweis wird geleert, alles andere bleibt', () => {
    const proj = fold(stand(), [op('comment.delete', { cardId: 'c1', commentId: 'k1' }, 'o1')]);
    const karte = proj.board.sprints[0].cards[0];
    expect(karte.comments.map(c => c.id)).toEqual(['k2']);
    expect(karte.attachments.map(a => (typeof a === 'string' ? a : a.id))).toEqual(['a2', 'a3', 'a4']);
    expect(karte.coverAttachmentId).toBeNull();
  });

  it('Kommentar ohne Dateien: card.attachments und Cover bleiben unverändert', () => {
    const vorher = stand();
    const proj = fold(vorher, [op('comment.delete', { cardId: 'c1', commentId: 'k-fremd' }, 'o2')]);
    expect(proj.board.sprints[0].cards[0]).toEqual(stand().board.sprints[0].cards[0]);
  });

  it('Karte ohne attachments-Feld: kein neues Feld entsteht (Byte-Gleichheit für Bestandsboards)', () => {
    const s = stand();
    delete s.board.sprints[0].cards[0].attachments;
    const proj = fold(s, [op('comment.delete', { cardId: 'c1', commentId: 'k1' }, 'o3')]);
    expect('attachments' in proj.board.sprints[0].cards[0]).toBe(false);
    expect(proj.board.sprints[0].cards[0].coverAttachmentId).toBe('a1');
  });
});

/* ── 2. Oberfläche im Server-Build ───────────────────────────────────── */

const karte = (extra = {}) => ({
  id: 'c1', displayId: 1, title: 'T', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: [], owner: '', assignees: [],
  complexity: '', effort: null, qa: false, critical: false, blocked: false, archived: false, columnId: 'col-ready',
  ...extra,
});

function setzeStand(k) {
  State.set({ version: '2', board: { id: 'b1', title: 'T', columnDefs: [], sprints: [{ id: 's1', cards: [k] }] } });
}

/* Ersatz für den Handler in main.js: Metadaten in den State übernehmen. */
function anhangInStand({ cardId, attachment }) {
  const k = State.get().board.sprints[0].cards.find(c => c.id === cardId);
  k.attachments = [...(k.attachments || []), attachment];
}

const warte = () => new Promise(r => setTimeout(r, 0));

describeServer('Karte #678 — Kommentar mit Dateien (Server-Build)', () => {
  beforeEach(() => {
    fake.hochgeladen.length = 0;
    document.body.innerHTML = '';
    EventBus.on('card:attachment-add', anhangInStand);
  });
  afterEach(() => {
    EventBus.off?.('card:attachment-add', anhangInStand);
    document.body.innerHTML = '';
  });

  it('Datei wählen → Hinzufügen lädt sie mit commentId hoch und zeigt sie unter dem Kommentar', async () => {
    const k = karte();
    setzeStand(k);
    EventBus.emit('card:edit', k);

    const input = document.querySelector('#comment-upload');
    expect(input, 'Dateiknopf im Kommentar-Formular fehlt').not.toBeNull();
    expect(input.multiple).toBe(true);
    const datei = new File(['abc'], 'beleg.txt', { type: 'text/plain' });
    Object.defineProperty(input, 'files', { value: [datei], configurable: true });
    input.dispatchEvent(new Event('change'));
    expect([...document.querySelectorAll('#comment-files .attachment-name')].map(e => e.textContent)).toEqual(['beleg.txt']);

    document.querySelector('#comment-input').value = 'siehe Anhang';
    document.querySelector(".comment-form .btn-primary").click();
    await warte(); await warte(); await warte();

    expect(fake.hochgeladen).toEqual([{ name: 'beleg.txt', cardId: 'c1' }]);
    const gespeichert = State.get().board.sprints[0].cards[0].attachments;
    expect(gespeichert).toHaveLength(1);
    expect(typeof gespeichert[0].commentId).toBe('string');
    expect(gespeichert[0].commentId.length).toBeGreaterThan(0);

    const unterKommentar = [...document.querySelectorAll('.comment-item .comment-attachment .attachment-name')]
      .map(e => e.textContent);
    expect(unterKommentar).toEqual(['beleg.txt']);
    expect(document.querySelectorAll('#comment-files .comment-file')).toHaveLength(0);
  });

  it('ein Kommentar darf nur aus Dateien bestehen', async () => {
    const k = karte();
    setzeStand(k);
    EventBus.emit('card:edit', k);
    const input = document.querySelector('#comment-upload');
    Object.defineProperty(input, 'files', { value: [new File(['x'], 'nur.txt')], configurable: true });
    input.dispatchEvent(new Event('change'));
    document.querySelector(".comment-form .btn-primary").click();
    await warte(); await warte(); await warte();
    expect(fake.hochgeladen.map(h => h.name)).toEqual(['nur.txt']);
    expect(document.querySelectorAll('.comment-item')).toHaveLength(1);
  });

  it('bestehender Kommentar zeigt nur SEINE Dateien; Karten-Anhänge ohne commentId nicht', () => {
    const k = karte({
      comments: [{ id: 'k1', author: 'Andi', text: 'a', created: '2026-10-04' },
                 { id: 'k2', author: 'Andi', text: 'b', created: '2026-10-04' }],
      attachments: [{ id: 'a1', filename: 'eins.pdf', commentId: 'k1', size: 1 },
                    { id: 'a2', filename: 'karte.pdf', size: 1 }],
    });
    setzeStand(k);
    EventBus.emit('card:edit', k);
    const items = [...document.querySelectorAll('.comment-item')];
    const namen = items.map(i => [...i.querySelectorAll('.comment-attachment .attachment-name')].map(e => e.textContent));
    expect(namen).toEqual([['eins.pdf'], []]);
  });

  it('Klick auf die Datei lädt sie über das Anhang-Backend herunter', async () => {
    const k = karte({
      comments: [{ id: 'k1', author: 'Andi', text: 'a', created: '2026-10-04' }],
      attachments: [{ id: 'a1', filename: 'eins.pdf', commentId: 'k1', size: 1 }],
    });
    setzeStand(k);
    EventBus.emit('card:edit', k);
    document.querySelector('.comment-attachment').click();
    await warte();
    expect(fake.backend.downloadAttachment).toHaveBeenCalledWith(expect.objectContaining({ id: 'c1' }),
      expect.objectContaining({ id: 'a1' }));
  });
});

/* ── 2b. KB-7: reiner Datei-Kommentar, alle Uploads scheitern ───────── */

describeServer('Karte #678 / KB-7 — leerer Datei-Kommentar wird entfernt, wenn alle Uploads scheitern', () => {
  let aktualisiert;
  const merke = (k) => {
    aktualisiert.push(k);
    const s = State.get();
    const liste = s.board.sprints[0].cards;
    const i = liste.findIndex(c => c.id === k.id);
    if (i >= 0) liste[i] = k; /* Ersatz für den Handler in main.js */
  };
  let alertSpy;
  beforeEach(() => {
    aktualisiert = [];
    document.body.innerHTML = '';
    EventBus.on('card:updated', merke);
    alertSpy = vi.fn();
    vi.stubGlobal('alert', alertSpy);
  });
  afterEach(() => {
    EventBus.off?.('card:updated', merke);
    fake.backend.uploadFile.mockReset();
    fake.backend.uploadFile.mockImplementation(async (datei, karte) => {
      fake.hochgeladen.push({ name: datei.name, cardId: karte.id });
      return { id: 'att-' + fake.hochgeladen.length, filename: datei.name, mime: 'text/plain', size: 3, created: '2026-10-04' };
    });
    vi.unstubAllGlobals();
    document.body.innerHTML = '';
  });

  function dateienWaehlen(...namen) {
    const input = document.querySelector('#comment-upload');
    Object.defineProperty(input, 'files', { value: namen.map(n => new File(['x'], n)), configurable: true });
    input.dispatchEvent(new Event('change'));
  }
  const hinzufuegen = () => document.querySelector('.comment-form .btn-primary').click();
  const speichern = () => document.querySelector('.modal-actions-right .btn-primary').click();
  const ruhe = async () => { for (let i = 0; i < 6; i++) await warte(); };

  it('noch ungespeichert: alle Uploads scheitern → Kommentar verschwindet, Speichern schreibt keinen', async () => {
    fake.backend.uploadFile.mockImplementation(async () => { throw new Error('kaputt'); });
    const k = karte();
    setzeStand(k);
    EventBus.emit('card:edit', k);
    dateienWaehlen('a.txt', 'b.txt');
    hinzufuegen();
    expect(document.querySelectorAll('.comment-item')).toHaveLength(1);
    await ruhe();
    expect(document.querySelectorAll('.comment-item')).toHaveLength(0);
    speichern();
    expect(State.get().board.sprints[0].cards[0].comments).toEqual([]);
  });

  it('schon gespeichert: alle Uploads scheitern danach → Entfernen über card:updated (→ comment.delete)', async () => {
    let scheitern;
    fake.backend.uploadFile.mockImplementation(() => new Promise((_, rej) => { scheitern = rej; }));
    const k = karte({ comments: [{ id: 'alt', author: 'Andi', text: 'bleibt', created: '2026-10-01' }] });
    setzeStand(k);
    EventBus.emit('card:edit', k);
    dateienWaehlen('a.txt');
    hinzufuegen();
    await warte();
    speichern(); /* Kommentar ist jetzt im State, Upload läuft noch */
    expect(State.get().board.sprints[0].cards[0].comments).toHaveLength(2);
    aktualisiert.length = 0;
    scheitern(new Error('kaputt'));
    await ruhe();
    expect(aktualisiert).toHaveLength(1);
    expect(aktualisiert[0].comments.map(c => c.id)).toEqual(['alt']);
    expect(State.get().board.sprints[0].cards[0].comments.map(c => c.id)).toEqual(['alt']);
  });

  it('mit Text: alle Uploads scheitern → Kommentar bleibt', async () => {
    fake.backend.uploadFile.mockImplementation(async () => { throw new Error('kaputt'); });
    const k = karte();
    setzeStand(k);
    EventBus.emit('card:edit', k);
    dateienWaehlen('a.txt');
    document.querySelector('#comment-input').value = 'mit Text';
    hinzufuegen();
    await ruhe();
    expect(document.querySelectorAll('.comment-item')).toHaveLength(1);
    speichern();
    expect(State.get().board.sprints[0].cards[0].comments.map(c => c.text)).toEqual(['mit Text']);
  });

  it('nur EIN Upload scheitert → Kommentar bleibt', async () => {
    let n = 0;
    fake.backend.uploadFile.mockImplementation(async (datei) => {
      if (++n === 1) throw new Error('kaputt');
      return { id: 'att-ok', filename: datei.name, mime: 'text/plain', size: 1, created: '2026-10-04' };
    });
    const k = karte();
    setzeStand(k);
    EventBus.emit('card:edit', k);
    dateienWaehlen('a.txt', 'b.txt');
    hinzufuegen();
    await ruhe();
    expect(document.querySelectorAll('.comment-item')).toHaveLength(1);
  });
});

/* ── 3. Ohne Server ──────────────────────────────────────────────────── */

describeOhneServer('Karte #678 — ohne Server-Build keine Kommentar-Dateien', () => {
  it('kein Dateiknopf, keine Dateiliste unter Kommentaren', () => {
    document.body.innerHTML = '';
    const k = karte({
      comments: [{ id: 'k1', author: 'Andi', text: 'a', created: '2026-10-04' }],
      attachments: [{ id: 'a1', filename: 'eins.pdf', commentId: 'k1', size: 1 }],
    });
    setzeStand(k);
    EventBus.emit('card:edit', k);
    expect(document.querySelector('#comment-upload')).toBeNull();
    expect(document.querySelector('.comment-attachment')).toBeNull();
  });
});
