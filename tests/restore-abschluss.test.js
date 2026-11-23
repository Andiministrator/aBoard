/**
 * Karte #726: Eine Rückspielung („Überschreiben“) hat einen eigenen Abschluss.
 *
 * Gemeldet war: Restore, danach „Save“ im Einstellungsdialog → der Restore sei
 * wieder überschrieben. Nachgestellt (HEAD und v1.48.0, fünf Varianten) blieb
 * er am Server immer erhalten; reproduzierbar war etwas anderes: Solange der
 * Dialog offen ist, hielt `restEingang` den neuen Stand zurück — hinter
 * „✓ Restored.“ stand weiter das alte Board. Und ein Ergebnis, das nach dem
 * Schliessen des Dialogs eintraf, sah niemand.
 *
 * Geprüft wird der echte Quelltext aus src/main.js (Restore-Block,
 * `uebernimmServerStand`, `debounce`) mit dem echten REST-Plugin — kein
 * Spiegel. `fetch` ist gemockt.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { buildBackupSection } from '../src/backup-section.js';
import { StoragePlugin, __testing__ } from '../src/storage-plugin.js';
import { EventBus } from '../src/eventbus.js';
import { State } from '../src/state.js';
import { Storage } from '../src/storage.js';
import { I18n } from '../src/i18n.js';
import { describeServer } from './helpers/build-flags.js';

const klon = x => JSON.parse(JSON.stringify(x));

function antwortJson(body, { ok = true, status = 200, etag = null } = {}) {
  return Promise.resolve({
    ok, status,
    headers: { get: h => (h === 'ETag' ? etag : null) },
    json: () => Promise.resolve(klon(body)),
    text: () => Promise.resolve(JSON.stringify(body)),
  });
}

/* ── Teil 1: die Sektion meldet ihr Ergebnis als Ereignis ─────────────── */

describe('#726 — Backup-Sektion meldet das Restore-Ergebnis', () => {
  let ereignisse;
  const mitschreiben = name => payload => ereignisse.push({ name, payload });
  const ok = mitschreiben('ok');
  const fehler = mitschreiben('fehler');

  beforeEach(() => {
    ereignisse = [];
    vi.spyOn(StoragePlugin, 'getActivePlugin').mockReturnValue({
      getCurrentUser: vi.fn(async () => null), aclList: vi.fn(async () => []),
    });
    vi.stubGlobal('confirm', () => true);
    EventBus.on('backup:restored', ok);
    EventBus.on('backup:restore-failed', fehler);
  });

  afterEach(() => {
    EventBus.off?.('backup:restored', ok);
    EventBus.off?.('backup:restore-failed', fehler);
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    document.querySelectorAll('.config-backup-container').forEach(n => n.remove());
  });

  async function restoreKlicken(fetchImpl, modus = 'overwrite') {
    globalThis.fetch = vi.fn(fetchImpl);
    const { el, bereit } = buildBackupSection('b1');
    await bereit;
    Object.defineProperty(el.querySelector('input[type="file"]'), 'files', {
      value: [new File(['x'], 'sicherung.tar.gz')], configurable: true,
    });
    el.querySelector('select').value = modus;
    [...el.querySelectorAll('button.btn-danger')].find(b => b.textContent === I18n.t('admin.backup.upload.btn')).click();
    return el;
  }

  it('Erfolg: Ereignis mit Ziel-Board und Modus', async () => {
    await restoreKlicken(url => (url === '/backup/restore' ? antwortJson({ board_id: 'b1' }) : antwortJson({ files: [] })));
    await vi.waitFor(() => expect(ereignisse).toHaveLength(1));
    expect(ereignisse[0]).toEqual({ name: 'ok', payload: { boardId: 'b1', mode: 'overwrite' } });
  });

  it('Modus wird beim Absenden festgehalten, nicht beim Eintreffen der Antwort', async () => {
    let freigeben;
    const el = await restoreKlicken(url => (url === '/backup/restore'
      ? new Promise(r => { freigeben = () => r(antwortJson({ board_id: 'neu' })); }).then(x => x)
      : antwortJson({ files: [] })), 'as-new');
    await vi.waitFor(() => expect(freigeben).toBeTypeOf('function'));
    el.querySelector('select').value = 'overwrite';   /* umgestellt, während der Restore läuft */
    freigeben();
    await vi.waitFor(() => expect(ereignisse).toHaveLength(1));
    expect(ereignisse[0].payload.mode).toBe('as-new');
  });

  it('Ablehnung durch den Server: Fehler-Ereignis mit dem angezeigten Text', async () => {
    const el = await restoreKlicken(url => (url === '/backup/restore'
      ? antwortJson({ error: 'backup corrupted' }, { ok: false, status: 400 })
      : antwortJson({ files: [] })));
    await vi.waitFor(() => expect(ereignisse).toHaveLength(1));
    expect(ereignisse[0].name).toBe('fehler');
    expect(ereignisse[0].payload.text).toBe(el.querySelector('.config-backup-flash').textContent);
    expect(ereignisse[0].payload.text).toBeTruthy();
  });

  it('Netzfehler: Fehler-Ereignis, kein Erfolg', async () => {
    await restoreKlicken(url => (url === '/backup/restore' ? Promise.reject(new Error('weg')) : antwortJson({ files: [] })));
    await vi.waitFor(() => expect(ereignisse).toHaveLength(1));
    expect(ereignisse[0]).toEqual({ name: 'fehler', payload: { text: I18n.t('admin.error-server') } });
  });
});

/* ── Teil 2: main.js übernimmt den Server-Stand sofort ────────────────── */

function ausschnitt(q, anfang, ende) {
  const a = q.indexOf(anfang);
  expect(a, anfang).toBeGreaterThan(-1);
  const e = q.indexOf(ende, a);
  expect(e, ende).toBeGreaterThan(-1);
  return q.slice(a, e + ende.length);
}

/* Restore-Block, uebernimmServerStand und debounce aus dem echten Quelltext. */
function restoreBlockLaden({ plugin, toast, syncToast, save }) {
  const q = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8');
  const debounceQ = ausschnitt(q, 'function debounce(fn, ms) {', '\n}\n');
  const uebernimmQ = ausschnitt(q, 'function uebernimmServerStand(plugin, serverState) {', '\n}\n');
  const blockQ = ausschnitt(q, "const dialogOffen = () =>", "EventBus.on('backup:restore-failed', ({ text }) => {\n  if (!dialogOffen()) showExportToast(text);\n});");
  const sync = { takePending: vi.fn() };
  // eslint-disable-next-line no-new-func
  const fabrik = new Function('IS_SERVER_BUILD', 'EventBus', 'StoragePlugin', 'State', 'Storage', 'Sync', 'I18n',
    'showExportToast', 'showSyncToast', 'save',
    `let _applyingExternalState = false;
     const bezugIstLokal = () => {};
     ${debounceQ}
     ${uebernimmQ}
     const inner = debounce(st => save(st), 500);
     const debouncedPersist = Object.assign(st => inner(st), { flush: () => inner.flush(), cancel: () => inner.cancel(), idle: () => inner.idle() });
     ${blockQ}
     return debouncedPersist;`);
  const off = [];
  const bus = { on: (n, f) => { EventBus.on(n, f); off.push([n, f]); } };
  const debouncedPersist = fabrik(true, bus, { getActivePlugin: () => plugin }, State, Storage, sync, I18n, toast, syncToast, save);
  return { debouncedPersist, sync, abmelden: () => off.forEach(([n, f]) => EventBus.off?.(n, f)) };
}

const brett = karten => ({
  version: '2',
  board: {
    id: 'b1', title: 'B', nextDisplayId: karten.length + 1,
    columnDefs: [{ id: 'col-backlog', title: 'Backlog' }],
    sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: karten.map((t, i) => (
      { id: `c${i}`, displayId: i + 1, title: t, description: '', columnId: 'col-backlog', comments: [] })) }],
  },
});
const titel = st => st.board.sprints.flatMap(s => s.cards).map(c => c.title);

describeServer('#726 — main.js übernimmt nach „Überschreiben“ sofort den Server-Stand', () => {
  let plugin, serverStand, posts, ctx, toast, syncToast, getAntwort, postAntwort, aufrufe;

  beforeEach(async () => {
    serverStand = brett(['alt-1', 'alt-2', 'alt-3']);
    posts = [];
    aufrufe = [];
    getAntwort = null;   /* Test kann GET verzögern oder scheitern lassen */
    postAntwort = null;  /* Test kann POST /ops verzögern */
    globalThis.fetch = vi.fn((url, init = {}) => {
      aufrufe.push(init.method || 'GET');
      if ((init.method || 'GET') === 'GET') return getAntwort ? getAntwort() : antwortJson(serverStand, { etag: '"e1"' });
      if (url.endsWith('/ops')) { posts.push(JSON.parse(init.body)); return postAntwort ? postAntwort() : antwortJson({}); }
      return Promise.reject(new Error(`unerwartet: ${init.method} ${url}`));
    });
    plugin = __testing__.createRESTPlugin();
    const sw = await plugin.switchBoard('b1');
    State.set(Storage.checkVersion(sw.state));
    toast = vi.fn();
    syncToast = vi.fn();
    ctx = restoreBlockLaden({ plugin, toast, syncToast, save: st => plugin.save(st) });
    /* Einstellungsdialog offen — wie im gemeldeten Ablauf. */
    const ov = document.createElement('div');
    ov.className = 'modal-overlay config-overlay modal-overlay--blocks-sync';
    ov.setAttribute('aria-hidden', 'false');
    document.body.appendChild(ov);
  });

  afterEach(() => {
    ctx.abmelden();
    plugin.stopPolling();
    document.querySelectorAll('.config-overlay').forEach(n => n.remove());
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('bei offenem Dialog: das Board zeigt sofort den restaurierten Stand, Save danach sendet nichts zurück', async () => {
    serverStand = brett(['restauriert']);                 /* der Server hat überschrieben */
    EventBus.emit('backup:restored', { boardId: 'b1', mode: 'overwrite' });
    await vi.waitFor(() => expect(titel(State.get())).toEqual(['restauriert']));
    expect(State.isDirty()).toBe(false);
    expect(syncToast).toHaveBeenCalledTimes(1);
    expect(toast).not.toHaveBeenCalled();                 /* Dialog offen → dort steht die Meldung */
    expect(ctx.sync.takePending).toHaveBeenCalled();      /* zurückgehaltener Altstand verworfen */
    const res = await plugin.save(State.get());           /* „Save“/Hintergrund-Speichern danach */
    expect(res.ok).toBe(true);
    expect(posts).toHaveLength(0);                        /* leerer Diff gegen den neuen Bezug */
  });

  it('Admin-Dialog offen statt Einstellungen: kein zusätzlicher Toast (F-1827)', async () => {
    document.querySelectorAll('.config-overlay').forEach(n => n.remove());
    const adm = document.createElement('div');
    adm.className = 'modal-overlay admin-overlay modal-overlay--open';
    document.body.appendChild(adm);
    try {
      serverStand = brett(['restauriert']);
      EventBus.emit('backup:restored', { boardId: 'b1', mode: 'overwrite' });
      await vi.waitFor(() => expect(titel(State.get())).toEqual(['restauriert']));
      expect(toast).not.toHaveBeenCalled();               /* die Meldung steht im Admin-Dialog */
    } finally { adm.remove(); }
  });

  it('ein vorher geplantes Speichern mit dem ALTEN Stand dreht den Restore nicht zurück', async () => {
    vi.useFakeTimers();
    const alt = klon(State.get());
    alt.board.sprints[0].cards[0].title = 'alt-1 bearbeitet';
    State.set(alt);
    ctx.debouncedPersist(alt);                            /* 500 ms geplant, noch nicht gesendet */
    serverStand = brett(['restauriert']);
    EventBus.emit('backup:restored', { boardId: 'b1', mode: 'overwrite' });
    await vi.waitFor(() => expect(titel(State.get())).toEqual(['restauriert']));
    await vi.advanceTimersByTimeAsync(2000);
    expect(posts).toHaveLength(0);
    expect(titel(State.get())).toEqual(['restauriert']);
  });

  it('Abruf nach dem Restore scheitert: Board bleibt, Hinweis „neu laden“ statt Stille (Kritiker A-5)', async () => {
    getAntwort = () => antwortJson({ error: 'weg' }, { ok: false, status: 503 });
    EventBus.emit('backup:restored', { boardId: 'b1', mode: 'overwrite' });
    await vi.waitFor(() => expect(toast).toHaveBeenCalledWith(I18n.t('admin.backup.msg-restored-reload')));
    expect(titel(State.get())).toEqual(['alt-1', 'alt-2', 'alt-3']);
    expect(syncToast).not.toHaveBeenCalled();
  });

  it('Board während des Abrufs gewechselt: nichts übernehmen, kein Hinweis', async () => {
    let freigeben;
    getAntwort = () => new Promise(r => { freigeben = () => r(antwortJson(brett(['restauriert']), { etag: '"e2"' })); });
    EventBus.emit('backup:restored', { boardId: 'b1', mode: 'overwrite' });
    await vi.waitFor(() => expect(freigeben).toBeTypeOf('function'));
    const anderes = klon(State.get()); anderes.board.id = 'b2'; anderes.board.sprints[0].cards = [];
    State.set(anderes);
    freigeben();
    await new Promise(r => setTimeout(r, 0));
    expect(State.get().board.id).toBe('b2');
    expect(toast).not.toHaveBeenCalled();
    expect(syncToast).not.toHaveBeenCalled();
  });

  it('ein schon gefeuertes Speichern wird abgewartet, bevor der Server-Stand geholt wird (Kritiker A-3)', async () => {
    vi.useFakeTimers();
    let postFreigeben;
    postAntwort = () => new Promise(r => { postFreigeben = () => r(antwortJson({})); });
    const alt = klon(State.get());
    alt.board.sprints[0].cards[0].title = 'alt-1 bearbeitet';
    State.set(alt);
    ctx.debouncedPersist(alt);
    await vi.advanceTimersByTimeAsync(600);            /* Timer gefeuert, POST unterwegs */
    expect(posts).toHaveLength(1);
    aufrufe.length = 0;
    serverStand = brett(['restauriert']);
    EventBus.emit('backup:restored', { boardId: 'b1', mode: 'overwrite' });
    await vi.advanceTimersByTimeAsync(50);
    expect(aufrufe).not.toContain('GET');              /* wartet auf das laufende Speichern */
    postFreigeben();
    await vi.waitFor(() => expect(titel(State.get())).toEqual(['restauriert']));
    expect(aufrufe[0]).toBe('GET');
  });

  it('Modus „als neues Board“: das offene Board bleibt, wie es ist', async () => {
    serverStand = brett(['fremd']);
    EventBus.emit('backup:restored', { boardId: 'b1', mode: 'as-new' });
    await new Promise(r => setTimeout(r, 0));
    expect(titel(State.get())).toEqual(['alt-1', 'alt-2', 'alt-3']);
  });

  it('anderes Board offen: nichts übernehmen', async () => {
    serverStand = brett(['restauriert']);
    EventBus.emit('backup:restored', { boardId: 'anderes', mode: 'overwrite' });
    await new Promise(r => setTimeout(r, 0));
    expect(titel(State.get())).toEqual(['alt-1', 'alt-2', 'alt-3']);
  });

  it('Dialog schon geschlossen: Erfolg und Fehler kommen als Toast', async () => {
    document.querySelector('.config-overlay').setAttribute('aria-hidden', 'true');
    serverStand = brett(['restauriert']);
    EventBus.emit('backup:restored', { boardId: 'b1', mode: 'overwrite' });
    await vi.waitFor(() => expect(titel(State.get())).toEqual(['restauriert']));
    expect(toast).toHaveBeenCalledWith(I18n.t('admin.backup.msg-restored'));
    EventBus.emit('backup:restore-failed', { text: 'Backup beschädigt' });
    expect(toast).toHaveBeenLastCalledWith('Backup beschädigt');
  });

  it('Dialog offen: ein Fehler bleibt im Dialog, kein zusätzlicher Toast', () => {
    EventBus.emit('backup:restore-failed', { text: 'Backup beschädigt' });
    expect(toast).not.toHaveBeenCalled();
  });
});
