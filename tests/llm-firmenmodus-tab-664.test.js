/* @vitest-environment happy-dom */

/* llm-firmenmodus-tab-664.test.js — Karte #664 (ADR-0133 §2 + Nachtrag):
 * Firmenmodus und PII-Vorgabe des Betreibers im KI-Tab.
 *
 * Der Server meldet beides in GET /users/me/llm-keys (`firmenmodus`, `pii`).
 * Geprüft wird, dass der KI-Tab
 *   - im Firmenmodus ohne eigenen Schlüssel bereitsteht (hasAnyKey, Dropdown
 *     mit dem Anbieter des Betreibers und NUR den freigegebenen Modellen),
 *   - die PII-Vorgabe über die Nutzerwahl in localStorage stellt,
 *   - die neuen Fehlerarten verständlich benennt,
 * und dass ohne die neuen Felder alles bleibt, wie es war (Gegenproben).
 *
 * Nur im Server-Build-Kontext (LLMTab ist sonst per DCE null). */

import { it, expect, vi, beforeEach, afterEach } from 'vitest';

import { LLMTab } from '../src/llm-tab.js';
import { I18n }   from '../src/i18n.js';
import { describeServer } from './helpers/build-flags.js';

const FIRMENMODUS = { ziel: 'llm.example:8000', anbieter: ['openai'], modelle: ['qwen3-30b'] };

function karte(overrides = {}) {
  return {
    id: 'card-664', displayId: 664, title: 'Karte', description: 'Text',
    comments: [], links: [], activity: [], attachments: [], ...overrides,
  };
}

function sseAntwort() {
  const text = 'event: token\ndata: {"text":"ok"}\n\nevent: done\ndata: {"commentId":"cm-1"}\n\n';
  const data = new TextEncoder().encode(text);
  return {
    ok: true, status: 200, headers: { get: () => 'text/event-stream' },
    body: { getReader: () => { let weg = false; return { read: async () => {
      if (weg) return { done: true, value: undefined };
      weg = true; return { done: false, value: data };
    } }; } },
  };
}

/* fetch-Attrappe: Schlüssel-Liste, Preise, Konversationen; den Stream-Aufruf
   zeichnet sie auf. */
function stubFetch(listBody) {
  const streamBodies = [];
  globalThis.fetch = vi.fn(async (url, init) => {
    const u = String(url);
    if (u.includes('/users/me/llm-keys')) return { ok: true, status: 200, json: async () => listBody };
    if (u.includes('llm-send-stream')) { streamBodies.push(JSON.parse(init.body)); return sseAntwort(); }
    if (u.includes('/llm/pricing')) return { ok: true, status: 200, json: async () => ({}) };
    return { ok: true, status: 200, json: async () => ({ conversations: [], holder: null }) };
  });
  return streamBodies;
}

async function rendere(listBody, card = karte()) {
  const streamBodies = stubFetch(listBody);
  LLMTab._resetCachesForTest();
  const panel = LLMTab.build();
  document.body.appendChild(panel);
  await LLMTab.render(card, 'board-1');
  await new Promise(r => setTimeout(r, 0));
  return { panel, streamBodies };
}

async function sende(panel) {
  panel.querySelector('#llm-send-btn').click();
  await new Promise(r => setTimeout(r, 10));
}

describeServer('KI-Tab — Firmenmodus und PII-Vorgabe (#664)', () => {
  beforeEach(() => {
    I18n.setLang('de');
    document.body.innerHTML = '';
    LLMTab._resetCachesForTest();
  });
  afterEach(() => {
    LLMTab.stopPolling();
    vi.restoreAllMocks();
  });

  it('Firmenmodus: KI-Tab steht ohne eigenen Schlüssel bereit (hasAnyKey)', async () => {
    stubFetch({ providers: [], keys: [], firmenmodus: FIRMENMODUS });
    expect(await LLMTab.hasAnyKey()).toBe(true);
  });

  it('Gegenprobe: ohne Firmenmodus und ohne Schlüssel kein KI-Tab', async () => {
    stubFetch({ providers: [], keys: [] });
    expect(await LLMTab.hasAnyKey()).toBe(false);
  });

  it('Firmenmodus: Dropdown bietet den Anbieter des Betreibers und nur die freigegebenen Modelle', async () => {
    const { panel } = await rendere({ providers: [], keys: [], firmenmodus: FIRMENMODUS,
      openai_kompatibel: { ziel: 'llm.example:8000', modelle: ['qwen3-30b', 'anderes-modell'] } });
    const anbieter = [...panel.querySelectorAll('#llm-provider-sel option')].map(o => o.value);
    const modelle = [...panel.querySelectorAll('#llm-model-sel option')].map(o => o.value);
    expect(anbieter).toEqual(['openai']);
    expect(modelle).toEqual(['qwen3-30b']);
    expect(panel.querySelector('#llm-send-btn').disabled).toBe(false);
  });

  it('Firmenmodus ohne Basis-URL: Modelle kommen aus der Freigabe, nicht aus der OpenAI-Liste', async () => {
    const { panel } = await rendere({ providers: [], keys: [], firmenmodus: { ziel: 'api.openai.com', anbieter: ['openai'], modelle: ['gpt-4o-mini'] } });
    const modelle = [...panel.querySelectorAll('#llm-model-sel option')].map(o => o.value);
    expect(modelle).toEqual(['gpt-4o-mini']);
  });

  it('Gegenprobe: ohne Firmenmodus gelten die eigenen Schlüssel und die OpenAI-Modellliste', async () => {
    const { panel } = await rendere({ providers: [], keys: [{ provider: 'openai' }] });
    const modelle = [...panel.querySelectorAll('#llm-model-sel option')].map(o => o.value);
    expect(modelle).toContain('gpt-4o');
  });

  it('PII „pflicht“ übersteuert die Nutzerwahl „never“: Send meldet aktiven Filter, kein Nachladen', async () => {
    localStorage.setItem('aboard.pii.mode', 'never');
    const { panel, streamBodies } = await rendere({ providers: [], keys: [], firmenmodus: FIRMENMODUS, pii: 'pflicht' });
    expect(panel.querySelector('#llm-fetch-note').textContent).toBe(I18n.t('llm.fetch.pii-blocked'));
    panel.querySelector('#llm-allow-fetch').checked = true;
    await sende(panel);
    expect(streamBodies.length).toBe(1);
    expect(streamBodies[0].piiFilterActive).toBe(true);
    expect(streamBodies[0].enableFetch).toBeUndefined();
    /* Die eigene Wahl bleibt gespeichert — sie gilt wieder, wenn die
       Vorgabe wegfällt. */
    expect(localStorage.getItem('aboard.pii.mode')).toBe('never');
  });

  it('PII „aus“ übersteuert die Nutzerwahl „always“: kein Filter, Nachladen möglich', async () => {
    localStorage.setItem('aboard.pii.mode', 'always');
    const { panel, streamBodies } = await rendere(
      { providers: [], keys: [], firmenmodus: FIRMENMODUS, pii: 'aus' },
      karte({ description: 'Kontakt: max.mustermann@example.org' }));
    expect(panel.querySelector('#llm-fetch-note').textContent).toBe(I18n.t('llm.fetch.pii-note'));
    panel.querySelector('#llm-allow-fetch').checked = true;
    await sende(panel);
    /* Bei aktivem Filter hätte die E-Mail das Vorschau-Modal geöffnet und der
       Stream wäre nie gestartet. */
    expect(streamBodies.length).toBe(1);
    expect(streamBodies[0].piiFilterActive).toBeUndefined();
    expect(streamBodies[0].userPromptOverride).toBeUndefined();
    expect(streamBodies[0].enableFetch).toBe(true);
  });

  it('Gegenprobe: ohne Vorgabe gilt die Nutzerwahl wie bisher', async () => {
    localStorage.setItem('aboard.pii.mode', 'never');
    const { panel, streamBodies } = await rendere({ providers: [], keys: [{ provider: 'openai' }] });
    await sende(panel);
    expect(streamBodies.length).toBe(1);
    expect(streamBodies[0].piiFilterActive).toBeUndefined();
  });

  it('Neue Fehlerarten des Servers werden benannt', () => {
    expect(LLMTab._errorText(400, { kind: 'model-not-allowed' })).toBe(I18n.t('llm.error-model-not-allowed'));
    expect(LLMTab._errorText(400, { kind: 'pii-pflicht' })).toBe(I18n.t('llm.error-pii-pflicht'));
    expect(LLMTab._errorText(400, { kind: 'etwas-anderes' })).toBe(I18n.t('llm.error-bad-request'));
  });
});
