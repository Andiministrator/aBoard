/* llm-tab-674.test.js — Karte #674 (ADR-0133 Nachtrag 2 A5/A6) im KI-Tab:
 * eigene Meldung für die Obergrenze je Person (429 anfragen-obergrenze) und
 * der Hinweis an der Verlaufsleiste im Firmenmodus.
 *
 * Nur im Server-Build aktiv (llm-tab.js ist ausserhalb DCE't). */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

import { LLMTab } from '../src/llm-tab.js';
import { I18n }   from '../src/i18n.js';
import { State }  from '../src/state.js';
import { Config } from '../src/config.js';
import { EventBus } from '../src/eventbus.js';
import { describeServer } from './helpers/build-flags.js';
import { diffToOps } from '../src/diff.js';

function antwort(status, body) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

describeServer('KI-Tab, Karte #674', () => {
  beforeEach(() => {
    I18n.setLang('de');
    LLMTab._resetCachesForTest();
  });

  it('429 anfragen-obergrenze → Meldung mit Grenze und Wartezeit in Minuten', () => {
    const txt = LLMTab._errorText(429, { kind: 'anfragen-obergrenze', grenze: 60, wartenSekunden: 125 });
    expect(txt).toContain('60');
    expect(txt).toContain('3 Min');
    expect(txt).not.toMatch(/Mutationen/);
  });

  it('429 ohne kind bleibt bei der bisherigen Meldung (Schlüssel-Grenze)', () => {
    expect(LLMTab._errorText(429, {})).toMatch(/Mutationen/);
  });

  it('Firmenmodus: Verlaufsleiste sagt, dass nur eigene Verläufe stehen und Antworten nur im Verlauf bleiben', async () => {
    globalThis.fetch = vi.fn(async (url) => String(url).includes('/users/me/llm-keys')
      ? antwort(200, { providers: [], keys: [], firmenmodus: { ziel: 'llm', anbieter: ['openai'], modelle: ['m'] } })
      : antwort(200, []));
    await LLMTab.hasAnyKey();
    const panel = document.createElement('div');
    panel.innerHTML = '<div id="llm-conv-bar"></div>';
    LLMTab._setStateForTest({ id: 'c1', title: 'K', comments: [], activity: [], links: [] }, 'b1');
    await LLMTab._loadConversationsForTest(panel);
    const label = panel.querySelector('.llm-conv-label');
    expect(label.title).toBe(I18n.t('llm.firmenmodus.verlaeufe-eigene'));
    expect(label.title).toMatch(/nicht an die Karte/);
    expect(label.title).not.toMatch(/trotzdem als Kommentar/);
  });

  it('ohne Firmenmodus: kein Hinweis an der Verlaufsleiste', async () => {
    globalThis.fetch = vi.fn(async (url) => String(url).includes('/users/me/llm-keys')
      ? antwort(200, { providers: [], keys: [{ provider: 'openai' }] })
      : antwort(200, []));
    await LLMTab.hasAnyKey();
    const panel = document.createElement('div');
    panel.innerHTML = '<div id="llm-conv-bar"></div>';
    LLMTab._setStateForTest({ id: 'c1', title: 'K', comments: [], activity: [], links: [] }, 'b1');
    await LLMTab._loadConversationsForTest(panel);
    expect(panel.querySelector('.llm-conv-label').title).toBe('');
  });
});

/* ── Nacharbeit #674: A5 vollständig — keine KI-Inhalte ungefragt an die Karte ── */

function sseAntwort(events) {
  const data = new TextEncoder().encode(events.map(e => `event: ${e.event}\ndata: ${JSON.stringify(e.data)}\n\n`).join(''));
  return {
    ok: true, status: 200, headers: { get: () => 'text/event-stream' },
    body: { getReader: () => { let weg = false; return { read: async () => {
      if (weg) return { done: true, value: undefined };
      weg = true; return { done: false, value: data };
    } }; } },
  };
}

const FIRMEN_KEYS = { providers: [], keys: [], pii: 'aus', firmenmodus: { ziel: 'llm', anbieter: ['openai'], modelle: ['m'] } };

function boardMitKarte() {
  return { version: 1, board: { id: 'b1', title: 'T',
    columnDefs: [{ id: 'col-backlog', title: 'Backlog' }, { id: 'col-ready', title: 'Ready' }],
    sprints: [{ id: 'sp-1', title: 'S', kind: 'sprint', cards: [
      { id: 'c1', displayId: 1, title: 'K', description: 'Hi', columnId: 'col-ready', labels: [],
        comments: [], qa: false, critical: false, blocked: false, activity: [] }] }] } };
}

describeServer('KI-Tab, Karte #674 Nacharbeit (Antwort nicht an der Karte)', () => {
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
    Config.load();
    Config.set('userName', 'Andi');
    State.set(boardMitKarte());
    LLMTab._resetCachesForTest();
  });
  afterEach(() => {
    LLMTab.stopPolling();
    document.body.innerHTML = '';
    State.set(null);
  });

  async function sendeMitDone(keys, done, waehrendStream = null) {
    globalThis.fetch = vi.fn(async (url) => {
      const u = String(url);
      if (u.includes('/users/me/llm-keys')) return antwort(200, keys);
      if (u.includes('/llm/pricing')) return antwort(200, {});
      if (u.includes('/llm-pending')) return antwort(200, {});
      if (u.includes('/conversations')) return antwort(200, []);
      if (u.includes('/llm-send-stream')) {
        if (waehrendStream) waehrendStream();
        return sseAntwort([
          { event: 'token', data: { text: 'Geheime Antwort' } },
          { event: 'done', data: done },
        ]);
      }
      return antwort(404, {});
    });
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    await LLMTab.render({ id: 'c1', title: 'K', description: 'Hi', comments: [], activity: [], links: [] }, 'b1');
    await new Promise(r => setTimeout(r, 0));
    panel.querySelector('#llm-send-btn').click();
    await new Promise(r => setTimeout(r, 10));
    return panel;
  }

  const FIRMEN_DONE = { inputTokens: 1, outputTokens: 1, costEstimateUSD: 0, finishReason: 'stop', firmenmodus: true };

  it('Firmenmodus: done ohne commentId → Antwort sichtbar, Behalten/Verwerfen ausgeblendet, Hinweis „nicht gespeichert“ samt Knopf-Hinweis', async () => {
    const panel = await sendeMitDone(FIRMEN_KEYS, FIRMEN_DONE);
    expect(panel.querySelector('#llm-preview-body').textContent).toMatch(/Geheime Antwort/);
    expect(panel.querySelector('.llm-keep-btn').style.display).toBe('none');
    expect(panel.querySelector('.llm-discard-btn').style.display).toBe('none');
    const st = panel.querySelector('#llm-status').textContent;
    expect(st).toContain(I18n.t('llm.firmenmodus.nicht-gespeichert'));
    expect(st).toContain(I18n.t('llm.firmenmodus.hinweis-uebernehmen'));
  });

  it('Firmenmodus: „Als Kommentar übernehmen“ schreibt EINEN Kommentar der Person mit Kennzeichnung — kein Doppel-Klick', async () => {
    const panel = await sendeMitDone(FIRMEN_KEYS, FIRMEN_DONE);
    const vorher = JSON.parse(JSON.stringify(State.get()));
    const actions = panel.querySelector('#llm-preview-actions');
    const btn = panel.querySelector('#llm-adopt-btn');
    expect(actions.style.display).not.toBe('none');
    expect(btn.style.display).not.toBe('none');
    expect(btn.disabled).toBe(false);
    btn.click();
    btn.click();                                   /* zweiter Klick: wirkungslos */
    LLMTab._onUebernehmenForTest(panel);           /* auch am Knopf vorbei */
    const card = State.get().board.sprints[0].cards[0];
    expect(card.comments.length).toBe(1);
    const k = card.comments[0];
    expect(k.author).toBe('Andi');
    expect(k.text.startsWith(I18n.t('llm.uebernahme.vorspann', 'openai/m'))).toBe(true);
    expect(k.text).toContain('Übernommene KI-Antwort');
    expect(k.text).toContain('Geheime Antwort');
    expect(btn.disabled).toBe(true);
    /* Der normale Kommentarweg: Aus der State-Änderung wird genau eine
       comment.add-Op — dieselbe, die das Karten-Modal erzeugt. */
    const ops = diffToOps(vorher, State.get(), 'Andi');
    const typen = ops.map(o => o.type || o[0]);
    expect(typen).toEqual(['comment.add']);
  });

  it('ohne Firmenmodus: kein Übernehmen-Knopf, und der Aufruf schreibt nichts', async () => {
    const panel = await sendeMitDone({ providers: [], keys: [{ provider: 'openai' }] },
      { commentId: 'cm-1', inputTokens: 1, outputTokens: 1, costEstimateUSD: 0, finishReason: 'stop', firmenmodus: false });
    expect(panel.querySelector('#llm-adopt-btn').style.display).toBe('none');
    LLMTab._onUebernehmenForTest(panel);
    expect(State.get().board.sprints[0].cards[0].comments).toEqual([]);
  });

  it('Firmenmodus als Betrachter: Send scheitert am ACLGuard (403) → kein aktiver Übernehmen-Knopf', async () => {
    globalThis.fetch = vi.fn(async (url) => {
      const u = String(url);
      if (u.includes('/users/me/llm-keys')) return antwort(200, FIRMEN_KEYS);
      if (u.includes('/llm/pricing')) return antwort(200, {});
      if (u.includes('/llm-pending')) return antwort(200, {});
      if (u.includes('/conversations')) return antwort(200, []);
      if (u.includes('/llm-send-stream')) return antwort(403, { kind: 'internal', message: 'forbidden' });
      return antwort(404, {});
    });
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    await LLMTab.render({ id: 'c1', title: 'K', description: 'Hi', comments: [], activity: [], links: [] }, 'b1');
    await new Promise(r => setTimeout(r, 0));
    panel.querySelector('#llm-send-btn').click();
    await new Promise(r => setTimeout(r, 10));
    const btn = panel.querySelector('#llm-adopt-btn');
    expect(btn.style.display === 'none' || btn.disabled).toBe(true);
    expect(panel.querySelector('#llm-preview-actions').style.display).toBe('none');
    LLMTab._onUebernehmenForTest(panel);
    expect(State.get().board.sprints[0].cards[0].comments).toEqual([]);
  });

  it('ohne Firmenmodus: done mit commentId → Behalten/Verwerfen sichtbar, kein Hinweis', async () => {
    const panel = await sendeMitDone({ providers: [], keys: [{ provider: 'openai' }] },
      { commentId: 'cm-1', inputTokens: 1, outputTokens: 1, costEstimateUSD: 0, finishReason: 'stop', firmenmodus: false });
    expect(panel.querySelector('#llm-preview-actions').style.display).not.toBe('none');
    expect(panel.querySelector('.llm-keep-btn').style.display).not.toBe('none');
    expect(panel.querySelector('.llm-discard-btn').style.display).not.toBe('none');
    expect(panel.querySelector('#llm-status').textContent).not.toContain(I18n.t('llm.firmenmodus.nicht-gespeichert'));
  });

  function verwerfeVorschlag(doneMeta = {}) {
    const panel = LLMTab.build();
    document.body.appendChild(panel);
    LLMTab._setStateForTest({ id: 'c1' }, 'b1');
    LLMTab._openMutationDialogForTest(panel, [
      { id: 't1', name: 'add-comment', allowed: true, args: { text: 'Kundendaten XY' } },
    ], doneMeta);
    document.querySelector('.llm-mut-discard').click();
    return State.get().board.sprints[0].cards[0];
  }

  it('Firmenmodus: verworfener Vorschlag hinterlässt nichts im Kartenverlauf', () => {
    LLMTab._setKeysCacheForTest(FIRMEN_KEYS);
    const card = verwerfeVorschlag({ firmenmodus: true });
    expect(card.activity).toEqual([]);
    expect(card.comments).toEqual([]);
  });

  it('ohne Firmenmodus: verworfener Vorschlag wird wie bisher vermerkt', () => {
    LLMTab._setKeysCacheForTest({ providers: [], keys: [{ provider: 'openai' }] });
    const card = verwerfeVorschlag({ firmenmodus: false });
    expect(card.activity.some(a => a.action === 'llm-mutation-discarded')).toBe(true);
  });

  /* ── Kritiker K2-4: Der Lauf entscheidet, nicht der Schlüssel-Cache ── */

  it('K2-4: Cache verworfen (invalidateKeysCache) — Lauf im Firmenmodus protokolliert nichts', () => {
    LLMTab._setKeysCacheForTest(FIRMEN_KEYS);
    LLMTab.invalidateKeysCache();                 /* Lücke: _keysCache === null */
    const card = verwerfeVorschlag({ firmenmodus: true });
    expect(card.activity).toEqual([]);
  });

  it('K2-4: Ladefehler hat „ohne Firmenmodus“ gecacht — Lauf im Firmenmodus protokolliert trotzdem nichts', () => {
    LLMTab._setKeysCacheForTest({ providers: [], keys: [] });   /* so cacht _loadKeys einen Fehler */
    const card = verwerfeVorschlag({ firmenmodus: true });
    expect(card.activity).toEqual([]);
  });

  it('K2-4: Lauf ohne Angabe gilt als Firmenmodus — kein Eintrag, auch wenn der Cache „ohne“ sagt', () => {
    LLMTab._setKeysCacheForTest({ providers: [], keys: [{ provider: 'openai' }] });
    const card = verwerfeVorschlag({});
    expect(card.activity).toEqual([]);
  });

  it('K2-4: Cache während des Streams verworfen und leer nachgeladen — Übernehmen-Knopf und Hinweis kommen aus dem done-Ereignis', async () => {
    /* Der Schlüssel-Dialog invalidiert, das Nachladen scheitert: so cacht
       _loadKeys einen Fehler ({ providers: [], keys: [] }). */
    const panel = await sendeMitDone(FIRMEN_KEYS, FIRMEN_DONE,
      () => LLMTab._setKeysCacheForTest({ providers: [], keys: [] }));
    const btn = panel.querySelector('#llm-adopt-btn');
    expect(btn.style.display).not.toBe('none');
    expect(panel.querySelector('#llm-status').textContent).toContain(I18n.t('llm.firmenmodus.hinweis-uebernehmen'));
    btn.click();
    expect(State.get().board.sprints[0].cards[0].comments.length).toBe(1);
  });

  it('K2-4: Lauf ohne Firmenmodus, aber ohne commentId (Persist gescheitert) — kein Übernehmen, auch wenn der Cache Firmenmodus sagt', async () => {
    const panel = await sendeMitDone(FIRMEN_KEYS, { ...FIRMEN_DONE, firmenmodus: false });
    expect(panel.querySelector('#llm-adopt-btn').style.display).toBe('none');
    LLMTab._onUebernehmenForTest(panel);
    expect(State.get().board.sprints[0].cards[0].comments).toEqual([]);
  });
});

/* F-1576: Eine spätere Ablehnung der comment.add-Op durch den Server erreicht
   den Übernehmen-Knopf (Ereignis 'ops:abgelehnt' der Op-Schicht). */
describeServer('KI-Tab, F-1576: Ablehnung nach dem Übernehmen', () => {
  const warte = () => new Promise(r => setTimeout(r, 0));

  async function uebernommenerVerlaufsKnopf() {
    LLMTab._resetCachesForTest();
    I18n.setLang('de');
    State.set({ version: '2', board: { id: 'b1', title: 'T', sprints: [{ id: 's1', title: 'S', cards: [
      { id: 'c1', title: 'K', comments: [], activity: [], links: [] }] }] } });
    LLMTab._setStateForTest({ id: 'c1' }, 'b1');
    globalThis.fetch = vi.fn(async () => antwort(200, [{ id: 'b1', role: 'editor' }]));
    const panel = document.createElement('div');
    panel.innerHTML = '<div id="llm-status"></div>';
    const btn = document.createElement('button');
    await LLMTab._onUebernehmenTurnForTest(panel, 'Antwort', { provider: 'p', model: 'm' }, btn);
    return { btn, status: panel.querySelector('#llm-status') };
  }

  /* comment.id des Kommentars, den der Knopf gerade angelegt hat */
  const eigeneId = () => State.get().board.sprints[0].cards[0].comments[0].id;

  it('eigene comment.add-Op bei 403 abgelehnt → Knopf wieder frei, Meldung llm.error-acl', async () => {
    const { btn, status } = await uebernommenerVerlaufsKnopf();
    expect(btn.textContent).toBe(I18n.t('llm.preview.uebernommen'));
    EventBus.emit('ops:abgelehnt', { boardId: 'b1', status: 403,
      ops: [{ type: 'comment.add', cardId: 'c1', commentId: eigeneId() }] });
    expect(btn.disabled).toBe(false);
    expect(btn.textContent).toBe(I18n.t('llm.preview.uebernehmen'));
    expect(status.textContent).toBe(I18n.t('llm.error-acl'));
  });

  it('eigene comment.add-Op bei 422 abgelehnt → Knopf frei', async () => {
    const { btn, status } = await uebernommenerVerlaufsKnopf();
    EventBus.emit('ops:abgelehnt', { boardId: 'b1', status: 422,
      ops: [{ type: 'comment.add', cardId: 'c1', commentId: eigeneId() }] });
    expect(btn.disabled).toBe(false);
    expect(status.textContent).toBe(I18n.t('ops.abgelehnt.sonst', '?', ''));
  });

  it('F-1579: abgelehnte eigene comment.add-Op → der Kommentar verschwindet aus dem lokalen Stand', async () => {
    await uebernommenerVerlaufsKnopf();
    const kommentare = () => State.get().board.sprints[0].cards[0].comments;
    expect(kommentare()).toHaveLength(1);
    EventBus.emit('ops:abgelehnt', { boardId: 'b1', status: 403,
      ops: [{ type: 'comment.add', cardId: 'c1', commentId: eigeneId() }] });
    expect(kommentare()).toHaveLength(0);
  });

  it('F-1579: fremde Ablehnung (anderer Kommentar, anderes Board) lässt den Kommentar stehen', async () => {
    await uebernommenerVerlaufsKnopf();
    const kommentare = () => State.get().board.sprints[0].cards[0].comments;
    EventBus.emit('ops:abgelehnt', { boardId: 'b1', status: 422,
      ops: [{ type: 'comment.add', cardId: 'c1', commentId: 'fremd' }] });
    EventBus.emit('ops:abgelehnt', { boardId: 'b2', status: 403,
      ops: [{ type: 'comment.add', cardId: 'c1', commentId: eigeneId() }] });
    expect(kommentare()).toHaveLength(1);
  });

  it('fremde Op derselben Karte (anderes Feld) abgelehnt → Knopf bleibt „übernommen“', async () => {
    const { btn } = await uebernommenerVerlaufsKnopf();
    EventBus.emit('ops:abgelehnt', { boardId: 'b1', status: 422,
      ops: [{ type: 'card.field', cardId: 'c1', commentId: null }] });
    expect(btn.textContent).toBe(I18n.t('llm.preview.uebernommen'));
    expect(btn.disabled).toBe(true);
  });

  it('fremdes comment.add derselben Karte oder 403 eines Batches ohne die eigene Op → bleibt', async () => {
    const { btn } = await uebernommenerVerlaufsKnopf();
    EventBus.emit('ops:abgelehnt', { boardId: 'b1', status: 422,
      ops: [{ type: 'comment.add', cardId: 'c1', commentId: 'fremd' }] });
    EventBus.emit('ops:abgelehnt', { boardId: 'b1', status: 403,
      ops: [{ type: 'card.field', cardId: 'c2', commentId: null }] });
    EventBus.emit('ops:abgelehnt', { boardId: 'b2', status: 403,
      ops: [{ type: 'comment.add', cardId: 'c1', commentId: eigeneId() }] });
    expect(btn.textContent).toBe(I18n.t('llm.preview.uebernommen'));
    expect(btn.disabled).toBe(true);
  });
});

/* KA-5: Eine ältere KI-Antwort lässt sich nur einmal als Kommentar übernehmen
   — auch nach dem Neuzeichnen der Verlaufsliste und über beide Knöpfe. */
describeServer('KI-Tab, KA-5: Übernahme nur einmal', () => {
  const kommentare = () => State.get().board.sprints[0].cards[0].comments;

  async function vorbereiten() {
    LLMTab._resetCachesForTest();
    I18n.setLang('de');
    State.set({ version: '2', board: { id: 'b1', title: 'T', sprints: [{ id: 's1', title: 'S', cards: [
      { id: 'c1', title: 'K', comments: [], activity: [], links: [] }] }] } });
    LLMTab._setStateForTest({ id: 'c1' }, 'b1');
    const turns = [{ role: 'assistant', content: 'Antwort' }];
    globalThis.fetch = vi.fn(async (url) => {
      const u = String(url);
      if (u.includes('/users/me/llm-keys')) return antwort(200, { providers: [], keys: [], firmenmodus: { ziel: 's', anbieter: ['openai'], modelle: ['m'] } });
      if (u.includes('/turns')) return antwort(200, turns);
      return antwort(200, [{ id: 'b1', role: 'editor' }]);
    });
    await LLMTab.hasAnyKey();
    LLMTab._setConversationsForTest([{ id: 'v1', provider: 'p', model: 'm' }], 'v1');
    const panel = document.createElement('div');
    panel.innerHTML = '<div id="llm-status"></div><div id="llm-turn-history"></div>';
    await LLMTab._loadAndRenderTurnsForTest(panel);
    return panel;
  }

  it('nach der Übernahme und Neuzeichnen ist der Verlaufsknopf gesperrt; ein Klick legt keinen zweiten Kommentar an', async () => {
    const panel = await vorbereiten();
    let btn = panel.querySelector('.llm-adopt-turn');
    expect(btn.disabled).toBe(false);
    btn.click();
    await vi.waitFor(() => expect(kommentare()).toHaveLength(1));
    await LLMTab._loadAndRenderTurnsForTest(panel);          /* Neuzeichnen */
    btn = panel.querySelector('.llm-adopt-turn');
    expect(btn.disabled).toBe(true);
    expect(btn.textContent).toBe(I18n.t('llm.preview.uebernommen'));
    btn.click();
    await LLMTab._onUebernehmenTurnForTest(panel, 'Antwort', { provider: 'p', model: 'm' }, document.createElement('button'));
    expect(kommentare()).toHaveLength(1);
  });

  /* Verify VO-4: dieselbe Antwort erst über den Verlauf übernommen, dann der
     Stream-Knopf („Als Kommentar übernehmen“ unter der Vorschau) → kein Doppel. */
  it('nach Übernahme über den Verlauf legt der Stream-Knopf derselben Antwort keinen zweiten Kommentar an', async () => {
    const panel = await vorbereiten();
    panel.querySelector('.llm-adopt-turn').click();
    await vi.waitFor(() => expect(kommentare()).toHaveLength(1));
    const knopf = document.createElement('button');
    knopf.id = 'llm-adopt-btn';
    panel.appendChild(knopf);
    LLMTab._setLastReplyForTest({ cardId: 'c1', boardId: 'b1', replyText: 'Antwort',
      firmenmodus: true, provider: 'p', model: 'm' });
    LLMTab._onUebernehmenForTest(panel);
    expect(kommentare()).toHaveLength(1);
    expect(knopf.disabled).toBe(true);
    expect(LLMTab._getLastReplyForTest().uebernommen).toBe(true);
  });

  it('Kartenwechsel während der Rollenabfrage gibt den Knopf wieder frei', async () => {
    const panel = await vorbereiten();
    const btn = panel.querySelector('.llm-adopt-turn');
    globalThis.fetch = vi.fn(async () => {
      LLMTab._setStateForTest({ id: 'c2' }, 'b1');
      return antwort(200, [{ id: 'b1', role: 'editor' }]);
    });
    await LLMTab._onUebernehmenTurnForTest(panel, 'Antwort', { provider: 'p', model: 'm' }, btn);
    expect(btn.disabled).toBe(false);
    expect(kommentare()).toHaveLength(0);
  });
});
