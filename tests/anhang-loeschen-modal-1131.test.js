/* @vitest-environment happy-dom */
/**
 * Karte #1131 / F-2978: Der Löschknopf im Karten-Modal entfernt den Anhang nur, wenn
 * das Anhang-Backend das Löschen bestätigt. Liefert es `false` (Server hat nicht
 * gelöscht), bleibt der Anhang in der Karte und eine kurze Meldung erscheint.
 * Backends ohne Rückgabewert (FSA/Standalone: undefined) verhalten sich wie bisher.
 * Nur Server-Build.
 */
import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

const fake = vi.hoisted(() => ({ backend: null }));

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));
vi.mock('../src/config.js', () => ({
  Config: { get: vi.fn(k => (k === 'userName' ? 'Person A' : '')), set: vi.fn() },
}));
vi.mock('../src/storage-plugin.js', async (orig) => {
  const m = await orig();
  fake.backend = {
    kartenAnhangImBoardState: () => false,
    checkUpload: () => ({ ok: true }),
    humanSize: (n) => `${n} B`,
    computeBoardBytes: () => 0,
    safeMime: (x) => x,
    uploadFile: vi.fn(),
    downloadAttachment: vi.fn(async () => true),
    attachmentUrl: vi.fn(async () => null),
    deleteAttachment: vi.fn(async () => true),
  };
  return { ...m, StoragePlugin: { ...m.StoragePlugin, getActivePlugin: () => ({ attachments: () => fake.backend }) } };
});

import { EventBus } from '../src/eventbus.js';
import { State } from '../src/state.js';
import { I18n } from '../src/i18n.js';
import '../src/modal.js';

const karte = () => ({
  id: 'c1', displayId: 1, title: 'T', description: '', agentContext: '', comments: [],
  attachments: [{ id: 'a1', filename: 'beleg.pdf', size: 1 }], links: [], activity: [], priority: 'mittel',
  labels: [], owner: '', assignees: [], complexity: '', effort: null, qa: false, critical: false,
  blocked: false, archived: false, columnId: 'col-ready',
});
const entfernen = ({ cardId, attachmentId }) => {
  const k = State.get().board.sprints[0].cards.find(c => c.id === cardId);
  k.attachments = k.attachments.filter(a => (a.id ?? a) !== attachmentId);
};
const warte = () => new Promise(r => setTimeout(r, 0));

describeServer('Anhang löschen im Modal (F-2978, #1131)', () => {
  let hinweise;
  const auf = t => hinweise.push(t);
  beforeEach(() => {
    document.body.innerHTML = '';
    I18n.setLang('de');
    hinweise = [];
    EventBus.on('hinweis', auf);
    EventBus.on('card:attachment-remove', entfernen);
    vi.stubGlobal('confirm', () => true);
    const k = karte();
    State.set({ version: '2', board: { id: 'b1', title: 'T', columnDefs: [], sprints: [{ id: 's1', cards: [k] }] } });
    EventBus.emit('card:edit', k);
  });
  afterEach(() => {
    EventBus.off?.('hinweis', auf);
    EventBus.off?.('card:attachment-remove', entfernen);
    vi.unstubAllGlobals();
    document.body.innerHTML = '';
  });
  const klick = async () => { document.querySelector('.attachment-delete-btn').click(); await warte(); await warte(); };

  it('Backend bestätigt (true): Anhang verschwindet aus der Karte, keine Meldung', async () => {
    fake.backend.deleteAttachment.mockResolvedValueOnce(true);
    await klick();
    expect(State.get().board.sprints[0].cards[0].attachments).toHaveLength(0);
    expect(hinweise).toEqual([]);
  });

  it('Backend meldet Fehlschlag (false): Anhang bleibt, kurze Meldung erscheint', async () => {
    fake.backend.deleteAttachment.mockResolvedValueOnce(false);
    await klick();
    expect(State.get().board.sprints[0].cards[0].attachments).toHaveLength(1);
    expect(document.querySelectorAll('.attachment-delete-btn')).toHaveLength(1);
    expect(hinweise).toHaveLength(1);
    expect(hinweise[0]).toMatch(/nicht gelöscht/);
  });

  it('Backend ohne Rückgabewert (undefined, FSA/Standalone): wie bisher entfernt', async () => {
    fake.backend.deleteAttachment.mockResolvedValueOnce(undefined);
    await klick();
    expect(State.get().board.sprints[0].cards[0].attachments).toHaveLength(0);
  });
});
