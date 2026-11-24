/* llm-tab.js — KI-Tab im Karten-Modal (#299, ADR-0095, Etappe 3).
 *
 * IS_SERVER_BUILD-gegated → im Slim/Claude-Code-Build per DCE eliminiert
 * (Tab wäre dort ohnehin nicht sichtbar, Modul + Styles werden komplett
 * aus dem Bundle entfernt).
 *
 * Verträge:
 *  - build()                     → DOM-Panel (einmal beim Modal-Build)
 *  - render(card, boardId)       → Pane-Inhalt für die geöffnete Karte
 *  - estimateTokens(text)        → 4-Zeichen-Heuristik (ADR-0095 §6)
 *  - hasAnyKey()                 → Promise<boolean> (Cache-gestützt)
 *  - invalidateKeysCache()       → Cache nach Key-Mutation droppen
 *
 * Sende-Flow: POST /boards/:b/cards/:c/llm-send → Antwort als Markdown im
 * Preview-Panel. Discard-Pfad ist Etappe 4 (braucht comment.delete-Op +
 * Activity-Eintrag 'llm-response-discarded') — in Etappe 3 nur „Behalten".
 */

import { I18n }            from './i18n.js';
import { Markdown }        from './markdown.js';
import { LLMKeysClient }   from './llm-keys-client.js';
import { LLMPii }          from './llm-pii.js';
import { LLMStream }       from './llm-stream.js';
import { LLMMutations }    from './llm-mutations.js';
import { captureFocus, restoreFocus, focusInitial, trapTabKey } from './focus-trap.js';
import { State }           from './state.js';
import { EventBus }        from './eventbus.js';
import { Config }          from './config.js';
import { findCardById }    from './links.js';
import { normalisiereAnhang } from './attachments.js';
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let LLMTab = null;

if (IS_SERVER_BUILD) {
  /* Modelle pro Provider (synchron zu server/llm/pricing.go). Frontend kennt
     die Liste, damit Modell-Dropdown sofort befüllt ist — Pricing-Fetch
     liefert nur Preise (Latenz tolerierbar). */
  const MODELS = {
    anthropic: ['claude-opus-5-5', 'claude-sonnet-5-5', 'claude-haiku-5-5', 'claude-fable-5-2'],
    openai:    ['gpt-4o', 'gpt-4o-mini', 'o1', 'o1-mini'],
    google:    ['gemini-1.5-pro', 'gemini-1.5-flash', 'gemini-2.0-flash'],
    mistral:   ['mistral-large-latest', 'mistral-medium-latest', 'mistral-small-latest'],
  };
  const PROVIDER_LABEL = {
    anthropic: 'Anthropic', openai: 'OpenAI', google: 'Google', mistral: 'Mistral',
  };
  const PRESET_KEYS = [
    'summarize', 'subtasks', 'risks', 'plan', 'tests',
    'translate-en', 'translate-de', 'custom',
  ];
  const COST_CONFIRM_USD = 0.10;
  /* Polling-Intervall für Pending-Lock-Status (ADR-0095 §5, Etappe 4).
     5s ist ein guter Kompromiss zwischen Latenz und Server-Last. */
  const PENDING_POLL_INTERVAL_MS = 5000;

  /* Modul-scoped Caches (Session-Leben). _keysCache wird vom Keys-Dialog
     via invalidateKeysCache() gedroppt, sobald der User Keys mutiert. */
  let _keysCache    = null;  /* { providers, keys } | null = uncached */
  let _pricingCache = null;  /* { anthropic: { model: {InputPerMillionUSD,OutputPerMillionUSD} } } */
  let _stylesInjected = false;
  let _currentCard = null;
  let _currentBoardId = null;
  /* _lastReply: { commentId, boardId, cardId, replyText } — für Discard-Pfad (Etappe 4) */
  let _lastReply = null;
  /* Pending-Lock-Polling-Timer-Handle. null wenn kein Polling läuft. */
  let _pendingPollTimer = null;
  /* Multi-Turn-State (#300 Etappe 3, ADR-0096 §2):
     - _currentConvId: null = Single-Turn (kein conversationId mitgeschickt)
     - _conversations: Liste der Conversations für die aktuelle Karte
     - _activeStreamAbort: AbortController für laufenden Stream (null = idle)
     - _streamReplyBuf: Akkumulierter Token-Text während des Streams */
  let _currentConvId   = null;
  let _conversations   = [];
  let _activeStreamAbort = null;
  let _streamReplyBuf  = '';
  /* #300 Etappe 5: Pro Stream gesammelte Mutations-Vorschläge (server-validiert,
     ggf. blocked:true). Cleared bei Send-Start, ausgewertet nach done-Event. */
  let _pendingMutations = [];
  /* #428 (ADR-0120): Pro Stream gemeldete Abrufe verknüpfter Karten. Anders
     als _pendingMutations sind das KEINE Vorschläge — sie sind bereits
     geschehen und werden nur angezeigt. */
  let _pendingFetches = [];
  /* #429: Abmessungen je Anhang-id (null = nicht ermittelbar). Wird beim
     Kartenwechsel geleert — ids sind pro Karte eindeutig, aber ein Cache,
     der über Karten hinweg lebt, waechst unbegrenzt. */
  let _bildMasseCache = new Map();
  /* Generations-Marke (#428/#429, Kritiker-Lauf). Jeder render() zählt hoch;
     asynchrone Aufgaben fangen ihren Stand beim Start ein und schreiben nur
     dann in die Oberfläche, wenn er noch gilt.

     Der Anlass: `_refreshEstimate` liest `_currentCard` synchron, hängt dann
     aber bis zu vier Sekunden am Laden der Bildabmessungen — wechselt der
     Nutzer inzwischen die Karte, landete die Schätzung der ALTEN Karte in der
     Ansicht der neuen. Reproduziert im Kritiker-Lauf (1.033 Tokens von Karte
     A über 34 Tokens von Karte B). Vor dieser Session war die Lücke
     theoretisch — `_loadPricing()` ist gecacht und kehrt in Millisekunden
     zurück; das Bild-Laden hat sie auf Sekunden aufgezogen.
     Muster: knowledge/topics/nebenlaeufigkeit-und-lesefenster.md §11.

     GELTUNGSBEREICH, ausdrücklich: Geprüft wird die Marke in
     `_refreshEstimate`, `_refreshAnhangZeilen`, seit F-861 (Karte #488) in
     `_loadConversations`, `_newConversation`, `_deleteConversation`,
     `_loadAndRenderTurns` und `_fetchPendingOnce`, und seit dem Kritiker-Lauf
     desselben Tages in `_onSend` und `_onDiscard`, und seit F-858 (Karte #529)
     in `_renderAnhangAuswahl` (wartet auf die Bild-Grenzen aus
     `GET /llm/pricing`). `_onSend` hält Karte und
     Board zusätzlich in lokalen Kopien (`sendCard`/`sendBoardId`) — dort ist
     die Kopie der wirksamere Schutz, weil sonst die Anfrage-URL selbst an die
     falsche Karte ginge. Das ist eine Aufzählung,
     KEINE modulweite Zusage: Wer eine weitere `async`-Funktion ergänzt, die
     nach einem `await` in den DOM oder in `_conversations`/`_currentConvId`
     schreibt, muss die Marke selbst ziehen und diese Liste erweitern.
     Geprüft wird vor JEDEM Schreibvorgang nach dem `await`, Fehlerpfade
     eingeschlossen — eine geleerte Leiste über der falschen Karte ist
     derselbe Fehler wie eine gefüllte. */
  let _renderGen = 0;
  /* Per-Stream Provider/Model-Snapshot — für Mutation-Activity-Meta. */
  let _lastSendMeta = null;

  function invalidateKeysCache() { _keysCache = null; }

  async function _loadKeys() {
    if (_keysCache !== null) return _keysCache;
    if (!LLMKeysClient) { _keysCache = { providers: [], keys: [] }; return _keysCache; }
    const res = await LLMKeysClient.list();
    _keysCache = (res && res.ok && res.body) ? res.body : { providers: [], keys: [] };
    return _keysCache;
  }

  async function hasAnyKey() {
    const k = await _loadKeys();
    /* Karte #664: Im Firmenmodus hat niemand einen eigenen Schlüssel — der
       KI-Tab steht trotzdem bereit, über den Schlüssel des Betreibers. */
    if (_firmenAnbieter(k).length > 0) return true;
    return Array.isArray(k.keys) && k.keys.length > 0;
  }

  /* Karte #664 (ADR-0133 §2): Firmenmodus. Der Server meldet ihn in
     GET /users/me/llm-keys als { ziel, anbieter, modelle }; ohne Firmenmodus
     fehlt das Feld (Antwort wie vorher). */
  function _firmenAnbieter(k) {
    const fm = k && k.firmenmodus;
    return (fm && Array.isArray(fm.anbieter)) ? fm.anbieter : [];
  }

  /* Karte #674 (ADR-0133 Nachtrag 2 A5): Firmenmodus aktiv? Dann schreibt
     der Server die Antwort nicht an die Karte, und auch diese Oberfläche
     trägt keine KI-Inhalte ungefragt in die Karte. */
  function _istFirmenmodus() {
    return _firmenAnbieter(_keysCache).length > 0;
  }

  /* Kritiker K2-4 (Sprint 82): Lief DIESER Lauf im Firmenmodus? Der Server
     meldet es im `done`-Ereignis (`firmenmodus: true|false`). Für alles, was
     KI-Inhalte an die Karte schreiben könnte (Verwerfen-Protokoll, Übernehmen-
     Knopf), zählt dieser Wert — nicht `_istFirmenmodus()`: Der Schlüssel-Cache
     ist invalidierbar (`invalidateKeysCache` setzt ihn auf null) und wird bei
     einem Ladefehler als „ohne Firmenmodus“ gefüllt. In dieser Lücke schrieb
     `_recordDiscard` die KI-Zusammenfassung in die öffentliche Aktivität (A5).
     Unbekannt (kein Feld, kein Lauf) gilt als Firmenmodus: lieber ein
     Protokolleintrag zu wenig als ein Inhalt zu viel. */
  function _laufImFirmenmodus(meta) {
    return !(meta && meta.firmenmodus === false);
  }

  /* Karte #664: PII-Modus für den nächsten Send. Die Vorgabe des Betreibers
     (`pii` in der Schlüssel-Liste: 'aus' | 'pflicht') übersteuert die
     Nutzerwahl aus localStorage; ohne Vorgabe gilt die Nutzerwahl wie bisher.
     Eine Stelle für alle Leser — _onSend und _piiAktiv dürfen nicht driften. */
  function _piiModus() {
    const vorgabe = _keysCache && _keysCache.pii;
    if (vorgabe === 'pflicht') return 'always';
    if (vorgabe === 'aus') return 'never';
    try { return localStorage.getItem('aboard.pii.mode') || 'always'; }
    catch { return 'always'; } /* fehlt/gesperrt (F-1774): sicherer Rückfall */
  }

  async function _loadPricing() {
    if (_pricingCache !== null) return _pricingCache;
    try {
      const res = await holen('/llm/pricing');
      _pricingCache = res.ok ? (await res.json()) : {};
    } catch {
      _pricingCache = {};
    }
    return _pricingCache;
  }

  function estimateTokens(text) {
    /* ADR-0095 §6: 1 Token ≈ 4 Zeichen (überschätzt sicher für Kostenanzeige). */
    return Math.ceil((text || '').length / 4);
  }

  /* CSS lazy eininjizieren — Slim-Pattern (feedback_slim_css_via_js_inject):
     bei IS_SERVER_BUILD=false wird _ensureLLMTabStyles selbst nie aufgerufen
     UND der String per DCE eliminiert; bei IS_SERVER_BUILD=true einmalig
     pro Session. */
  function _ensureLLMTabStyles() {
    if (_stylesInjected) return;
    _stylesInjected = true;
    const st = document.createElement('style');
    st.setAttribute('data-aboard', 'llm-tab');
    st.textContent = '#tab-panel-llm{padding:12px}.llm-wrap{display:grid;grid-template-columns:1fr 1fr;gap:14px;align-items:start}@media(max-width:1023px){.llm-wrap{grid-template-columns:1fr}}.llm-subtab-bar{display:none;grid-column:1/-1;gap:6px}@media(max-width:639px){.llm-subtab-bar{display:flex}.llm-composer[data-sub-active="0"],.llm-preview[data-sub-active="0"]{display:none}}.llm-subtab{flex:1;padding:6px 10px;font-size:13px;background:var(--surface);border:1px solid var(--border);border-radius:var(--radius);cursor:pointer}.llm-subtab-active{background:var(--accent-btn);color:#fff;border-color:var(--accent-btn)}.llm-composer,.llm-preview{display:flex;flex-direction:column;gap:6px;min-width:0}.llm-section-h{margin:0 0 4px;font-size:13px;text-transform:uppercase;letter-spacing:.05em;color:var(--text-muted)}.llm-ctx-list{display:flex;flex-direction:column;gap:3px;font-size:13px}.llm-ctx-label{display:flex;align-items:center;gap:6px;cursor:pointer}.llm-attach-note{margin:0;font-size:12px;color:var(--text-muted);font-style:italic}.llm-field-label{font-size:12px;color:var(--text-muted);margin-top:6px}.llm-preset-sel,.llm-provider-sel,.llm-model-sel,.llm-system-prompt{padding:6px 8px;font-size:13px;width:100%;box-sizing:border-box}.llm-system-prompt{resize:vertical;min-height:60px;font-family:inherit}.llm-estimate{font-size:12px;color:var(--text-muted);margin-top:4px}.llm-privacy{padding:8px 10px;font-size:12px;background:#fff8e1;color:#6b4a00;border:1px solid #ffcc02;border-radius:4px;margin-top:4px}.llm-pending-banner{padding:8px 10px;font-size:12px;background:#e3f2fd;border:1px solid #1976d2;border-radius:4px;margin-top:4px;color:#0d47a1}.llm-send-btn{padding:8px 14px;font-size:14px;background:var(--accent-btn);color:#fff;border:none;border-radius:var(--radius);cursor:pointer;margin-top:8px}.llm-send-btn:disabled{opacity:.6;cursor:wait}.llm-status{margin-top:6px;font-size:12px;min-height:1.2em;color:var(--danger,#c33)}.llm-preview-body{padding:10px;background:var(--surface);border:1px solid var(--border);border-radius:4px;min-height:80px;max-height:400px;overflow-y:auto;font-size:13px}.llm-preview-empty{color:var(--text-muted);font-style:italic;margin:0}.llm-preview-actions{display:flex;gap:8px;margin-top:8px;justify-content:flex-end}@media(max-width:639px){.llm-preview-actions{position:sticky;bottom:0;background:var(--surface);padding:8px 0;border-top:1px solid var(--border);justify-content:space-between}}.llm-conv-bar{display:flex;flex-wrap:wrap;gap:4px;margin-bottom:8px;align-items:center}.llm-conv-label{font-size:11px;color:var(--text-muted);text-transform:uppercase;letter-spacing:.05em;margin-right:4px}.llm-conv-chip{padding:4px 8px;cursor:pointer;border:1px solid var(--border);border-radius:12px;background:var(--surface);font-size:12px;display:inline-flex;align-items:center;gap:4px}.llm-conv-chip:hover{background:#f5f5f5;color:#333}.llm-conv-chip-active{background:var(--accent-btn);color:#fff;border-color:var(--accent-btn)}.llm-conv-chip-new{border-style:dashed;color:var(--accent)}.llm-conv-x{background:none;border:none;color:inherit;cursor:pointer;font-size:14px;padding:0 2px;opacity:.6;line-height:1}.llm-conv-x:hover{opacity:1}.llm-budget-warn{padding:8px 10px;font-size:12px;background:#fff3e0;border:1px solid #ff9800;border-radius:4px;margin-top:6px;color:#8a3400}.llm-cancel-btn{padding:8px 14px;font-size:14px;background:#d32f2f;color:#fff;border:none;border-radius:var(--radius);cursor:pointer;margin-top:8px;margin-left:8px}.llm-turn{padding:6px 8px;border-radius:4px;margin-bottom:4px;font-size:13px}.llm-turn-u{background:#e3f2fd;color:#0d47a1}.llm-turn-a{background:#f5f5f5;color:#333}.llm-turn-r{font-weight:bold;font-size:11px;text-transform:uppercase;color:var(--text-muted);margin-bottom:2px}.llm-turn-h{max-height:200px;overflow-y:auto;margin-bottom:8px;padding:4px;background:#fafafa;color:#333;border:1px solid #eee;border-radius:4px}.llm-mut-allow{display:flex;align-items:center;gap:6px;font-size:12px;color:var(--text-muted);margin-top:6px;cursor:pointer}.llm-mut-overlay{position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:10000;display:flex;align-items:center;justify-content:center;padding:16px}.llm-mut-dialog{background:var(--surface);border-radius:8px;max-width:640px;width:100%;max-height:80vh;display:flex;flex-direction:column;box-shadow:0 8px 32px rgba(0,0,0,.3)}.llm-mut-h{padding:14px 18px;border-bottom:1px solid var(--border);margin:0;font-size:16px}.llm-mut-i{padding:8px 18px;font-size:12px;color:var(--text-muted)}.llm-mut-list{flex:1;overflow-y:auto;padding:8px 18px}.llm-mut-row{padding:10px;border:1px solid var(--border);border-radius:4px;margin-bottom:8px;display:flex;flex-direction:column;gap:6px}.llm-mut-row[data-state="applied"]{background:#e8f5e9;color:#1b5e20;border-color:#66bb6a}.llm-mut-row[data-state="discarded"]{background:#fafafa;color:#666;opacity:.6}.llm-mut-row[data-state="blocked"]{background:#fff3e0;color:#8a3400;border-color:#ff9800}.llm-mut-row[data-state="error"]{background:#ffebee;color:#b71c1c;border-color:#c33}.llm-mut-tool{font-weight:bold;font-size:13px}.llm-mut-summ{font-size:13px;font-family:monospace;background:#f5f5f5;color:#333;padding:4px 6px;border-radius:3px;word-break:break-word;white-space:pre-wrap}.llm-mut-err{font-size:12px;color:var(--danger,#c33)}.llm-mut-row[data-state="discarded"] .llm-mut-stale{opacity:1}.llm-mut-stale{font-size:12px;background:#fff3e0;color:#8a3400;border:1px solid #ff9800;border-radius:4px;padding:6px 8px;opacity:1}.llm-mut-retry{margin-top:2px;padding:6px 12px;font-size:13px;border:1px solid #ff9800;border-radius:4px;cursor:pointer;background:var(--surface);color:var(--prio-high-fg)}.llm-mut-acts{display:flex;gap:6px;justify-content:flex-end}.llm-mut-acts button{padding:6px 12px;font-size:13px;border:1px solid var(--border);border-radius:4px;cursor:pointer;background:var(--surface)}.llm-mut-acts .llm-mut-apply{background:var(--accent-btn);color:#fff;border-color:var(--accent-btn)}.llm-mut-acts button:disabled{opacity:.5;cursor:default}.llm-mut-f{padding:10px 18px;border-top:1px solid var(--border);display:flex;justify-content:flex-end;gap:8px}.llm-fetch-list{margin-bottom:8px;padding:6px 8px;background:var(--surface);border:1px solid var(--border);border-radius:4px;font-size:12px}.llm-fetch-h{font-weight:bold;font-size:11px;text-transform:uppercase;letter-spacing:.05em;color:var(--text-muted);margin-bottom:4px}.llm-fetch-row{display:flex;flex-wrap:wrap;align-items:baseline;line-height:1.5}.llm-fetch-row-blocked{color:#8a3400}.llm-fetch-name{font-weight:500}.llm-fetch-reason{color:var(--text-muted);font-style:italic}.llm-turn-sum{margin-top:6px;padding-top:4px;border-top:1px solid #eee;font-size:11px;color:var(--text-muted);text-align:right}.llm-att-box{margin-top:6px;padding:6px 8px;border:1px solid var(--border);border-radius:4px;background:var(--surface)}.llm-att-row{display:flex;flex-wrap:wrap;align-items:center;gap:4px;font-size:12px;cursor:pointer;line-height:1.6}.llm-att-row-blocked{color:var(--text-muted);cursor:default}.llm-att-name{font-weight:500}.llm-att-meta{color:var(--text-muted)}.llm-att-reason{color:#8a3400;font-style:italic}.llm-att-status{color:#8a3400;margin-top:4px}';
    document.head.appendChild(st);
  }

  function el(tag, attrs, ...children) {
    const e = document.createElement(tag);
    if (attrs) {
      for (const k in attrs) {
        const v = attrs[k];
        if (k === 'cls') e.className = v;
        else if (k === 'text') e.textContent = v;
        else if (k === 'html') e.innerHTML = v;
        else e.setAttribute(k, v);
      }
    }
    for (const c of children) { if (c != null) e.appendChild(c); }
    return e;
  }

  /* ── DOM-Aufbau (build) ─────────────────────────────────────────── */

  function build() {
    _ensureLLMTabStyles();
    const panel = el('div', { cls: 'tab-panel', id: 'tab-panel-llm' });
    panel.setAttribute('role', 'tabpanel');
    panel.setAttribute('aria-labelledby', 'tab-llm');
    panel.dataset.tabPanel = 'llm';

    const wrap = el('div', { cls: 'llm-wrap' });

    /* Mobile-only Sub-Tab-Switcher */
    const subBar = el('div', { cls: 'llm-subtab-bar' });
    const subSent  = el('button', { cls: 'llm-subtab llm-subtab-active', type: 'button',
                                     'data-sub': 'sent',  text: I18n.t('llm.subtab.sent') });
    const subReply = el('button', { cls: 'llm-subtab', type: 'button',
                                     'data-sub': 'reply', text: I18n.t('llm.subtab.reply') });
    subSent.addEventListener('click',  () => _switchSubTab(panel, 'sent'));
    subReply.addEventListener('click', () => _switchSubTab(panel, 'reply'));
    subBar.appendChild(subSent);
    subBar.appendChild(subReply);
    wrap.appendChild(subBar);

    /* Composer (linke Spalte) */
    const composer = el('div', { cls: 'llm-composer' });
    composer.dataset.subPane = 'sent';
    composer.dataset.subActive = '1';

    /* Conversation-Bar (#300 Etappe 3) — über dem Kontext-Header.
       Wird in render() mit den geladenen Konversationen befüllt. */
    const convBar = el('div', { cls: 'llm-conv-bar', id: 'llm-conv-bar' });
    composer.appendChild(convBar);

    composer.appendChild(el('h3', { cls: 'llm-section-h', text: I18n.t('llm.tab.context-heading') }));

    const ctxList = el('div', { cls: 'llm-ctx-list' });
    [
      ['description',  true],
      ['comments',     true],
      ['links',        true],
      ['agentContext', true],
      ['activity',     false],
    ].forEach(([field, defaultOn]) => {
      const id = `llm-ctx-${field}`;
      const lbl = el('label', { cls: 'llm-ctx-label', for: id });
      const cb = el('input', { type: 'checkbox', id, 'data-ctx-field': field });
      if (defaultOn) cb.checked = true;
      cb.addEventListener('change', () => _refreshEstimate(panel));
      const i18nKey = field === 'agentContext' ? 'llm.context.agent' : `llm.context.${field}`;
      lbl.appendChild(cb);
      lbl.appendChild(document.createTextNode(' ' + I18n.t(i18nKey)));
      ctxList.appendChild(lbl);
    });
    composer.appendChild(ctxList);
    composer.appendChild(el('p', { cls: 'llm-attach-note', id: 'llm-attach-note' }));

    /* #429 (ADR-0121): Auswahl der Bild-Anhaenge, die mitgeschickt werden.
       Der Nutzer waehlt — es gibt bewusst kein Werkzeug, mit dem das Modell
       sich Anhaenge selbst holt: Nur so lassen sich die Kosten VOR dem
       Senden zeigen, und genau das verlangt die Karte. */
    const attBox = el('div', { cls: 'llm-att-box', id: 'llm-att-box' });
    attBox.style.display = 'none';
    composer.appendChild(attBox);

    composer.appendChild(el('label', { cls: 'llm-field-label', for: 'llm-preset-sel',
                                       text: I18n.t('llm.preset.label') }));
    const presetSel = el('select', { cls: 'llm-preset-sel', id: 'llm-preset-sel' });
    PRESET_KEYS.forEach(k => {
      presetSel.appendChild(el('option', { value: k, text: I18n.t(`llm.preset.${k}`) }));
    });
    composer.appendChild(presetSel);

    composer.appendChild(el('label', { cls: 'llm-field-label', for: 'llm-system-prompt',
                                       text: I18n.t('llm.preset.system-label') }));
    const sysPrompt = el('textarea', { cls: 'llm-system-prompt', id: 'llm-system-prompt', rows: '3' });
    composer.appendChild(sysPrompt);

    presetSel.addEventListener('change', () => {
      const k = presetSel.value;
      if (k !== 'custom') sysPrompt.value = I18n.t(`llm.preset.system.${k}`);
      /* Der Preset-Wechsel kann im Modus „custom" den Filter ein- oder
         ausschalten — und damit, ob nachgeladen wird. */
      _refreshFetchHinweis(panel);
      _refreshEstimate(panel);
    });
    sysPrompt.addEventListener('input', () => _refreshEstimate(panel));

    composer.appendChild(el('label', { cls: 'llm-field-label', for: 'llm-provider-sel',
                                       text: I18n.t('llm.provider.label') }));
    const providerSel = el('select', { cls: 'llm-provider-sel', id: 'llm-provider-sel' });
    composer.appendChild(providerSel);
    composer.appendChild(el('label', { cls: 'llm-field-label', for: 'llm-model-sel',
                                       text: I18n.t('llm.provider.model-label') }));
    const modelSel = el('select', { cls: 'llm-model-sel', id: 'llm-model-sel' });
    composer.appendChild(modelSel);

    providerSel.addEventListener('change', () => {
      _populateModels(modelSel, providerSel.value);
      _refreshPrivacyBanner(panel);
      _refreshFetchHinweis(panel);
      /* Modellwechsel kann die Bild-Faehigkeit aendern — die Auswahl muss
         dann gesperrt und geleert werden, sonst schickt der naechste Send
         Anhaenge an ein Modell, das sie nicht lesen kann. */
      _refreshAnhangZeilen(panel);
      _refreshEstimate(panel);
    });
    modelSel.addEventListener('change', () => {
      _refreshAnhangZeilen(panel);
      _refreshEstimate(panel);
    });

    composer.appendChild(el('div', { cls: 'llm-estimate', id: 'llm-estimate' }));
    composer.appendChild(el('div', { cls: 'llm-privacy',  id: 'llm-privacy'  }));

    /* Pending-Lock-Banner (ADR-0095 §5) — zeigt, wenn ein anderer User
       gerade einen LLM-Send auf dieser Karte ausführt. Wird vom Polling
       (GET /llm-pending) befüllt; standardmäßig versteckt. */
    const pendingBanner = el('div', { cls: 'llm-pending-banner', id: 'llm-pending-banner' });
    pendingBanner.style.display = 'none';
    composer.appendChild(pendingBanner);

    const sendBtn = el('button', { cls: 'llm-send-btn', type: 'button', id: 'llm-send-btn',
                                   text: I18n.t('llm.send.button') });
    composer.appendChild(sendBtn);
    sendBtn.addEventListener('click', () => _onSend(panel));

    /* Cancel-Button (#300 Etappe 3) — nur während aktivem Stream sichtbar. */
    const cancelBtn = el('button', { cls: 'llm-cancel-btn', type: 'button', id: 'llm-cancel-btn',
                                     text: I18n.t('llm.streaming.cancel') });
    cancelBtn.style.display = 'none';
    cancelBtn.addEventListener('click', () => _onCancel(panel));
    composer.appendChild(cancelBtn);

    /* #300 Etappe 5 (ADR-0096 §3+§4): Opt-in-Checkbox „Mutations erlauben".
       Setzt enableMutations:true im Stream-Body. Default OFF — der LLM
       erhält ohne Opt-in keine Tools und kann nichts vorschlagen. */
    const allowLbl = el('label', { cls: 'llm-mut-allow', id: 'llm-allow-mut-lbl', for: 'llm-allow-mutations' });
    const allowCb = el('input', { type: 'checkbox', id: 'llm-allow-mutations' });
    allowLbl.appendChild(allowCb);
    allowLbl.appendChild(el('span', { text: I18n.t('llm.mutation.allow-checkbox') }));
    composer.appendChild(allowLbl);

    /* #428 (ADR-0120): Opt-in „verknuepfte Karten nachladen". Der Server gibt
       nur Karten heraus, die genau eine Verknuepfungs-Ebene entfernt liegen
       und auf die der sendende Nutzer Zugriff hat; hoechstens 10 pro Antwort.
       Default OFF — ohne Opt-in bekommt das Modell das Werkzeug nicht. */
    const fetchLbl = el('label', { cls: 'llm-mut-allow', id: 'llm-allow-fetch-lbl', for: 'llm-allow-fetch' });
    const fetchCb = el('input', { type: 'checkbox', id: 'llm-allow-fetch' });
    fetchLbl.appendChild(fetchCb);
    fetchLbl.appendChild(el('span', { text: I18n.t('llm.fetch.allow-checkbox') }));
    composer.appendChild(fetchLbl);
    /* Die Grenze gehoert sichtbar an die Bedienung, nicht in einen Kommentar:
       Mit aktivem PII-Filter laedt der Server nichts nach — eine vom Server
       geholte Karte hat den Browser-Filter strukturell nicht passiert. */
    composer.appendChild(el('p', { cls: 'llm-attach-note', id: 'llm-fetch-note' }));

    composer.appendChild(el('div', { cls: 'llm-status', id: 'llm-status' }));

    wrap.appendChild(composer);

    /* Preview (rechte Spalte) */
    const preview = el('div', { cls: 'llm-preview' });
    preview.dataset.subPane = 'reply';
    preview.dataset.subActive = '1';
    preview.appendChild(el('h3', { cls: 'llm-section-h', text: I18n.t('llm.tab.preview-heading') }));

    /* Turn-History (#300 Etappe 3) — nur bei aktiver Conversation befüllt. */
    const turnHistory = el('div', { cls: 'llm-turn-h', id: 'llm-turn-history' });
    turnHistory.style.display = 'none';
    preview.appendChild(turnHistory);

    /* #428 (ADR-0120): Liste der serverseitig nachgeladenen Karten — und der
       abgelehnten Anfragen. Steht ueber der Antwort, weil es der Kontext ist,
       aus dem sie entstanden ist. */
    const fetchList = el('div', { cls: 'llm-fetch-list', id: 'llm-fetch-list' });
    /* F-861-Nachbarbefund F-860: Der Kasten wird nachtraeglich befuellt — wer
       nicht gerade dort steht, bekaeme die Aenderung sonst nie angesagt.
       `polite` und nicht `assertive`: Es ist Begleitinformation zur Antwort,
       kein Alarm, und darf eine laufende Vorlesung nicht unterbrechen. */
    fetchList.setAttribute('aria-live', 'polite');
    fetchList.style.display = 'none';
    preview.appendChild(fetchList);

    const previewBody = el('div', { cls: 'llm-preview-body', id: 'llm-preview-body' });
    previewBody.appendChild(el('p', { cls: 'llm-preview-empty', text: I18n.t('llm.tab.empty-state') }));
    preview.appendChild(previewBody);

    /* Actions-Bar: Etappe 4 — Discard-Pfad aktiviert (POST /llm-discard).
       Server entfernt den Kommentar + hängt 'llm-response-discarded'-Activity an. */
    const actions = el('div', { cls: 'llm-preview-actions', id: 'llm-preview-actions' });
    actions.style.display = 'none';
    const keepBtn = el('button', { cls: 'btn llm-keep-btn', type: 'button',
                                   text: I18n.t('llm.preview.keep') });
    keepBtn.addEventListener('click', () => _onKeep(panel));
    actions.appendChild(keepBtn);
    const discardBtn = el('button', { cls: 'btn llm-discard-btn', type: 'button',
                                      text: I18n.t('llm.preview.discard') });
    discardBtn.addEventListener('click', () => _onDiscard(panel));
    actions.appendChild(discardBtn);
    /* Karte #674 (ADR-0133 Nachtrag 2 A5): Im Firmenmodus schreibt der Server
       die Antwort nicht an die Karte. Dieser Knopf übernimmt sie auf
       ausdrücklichen Wunsch der Person als IHREN Kommentar (Nutzerwunsch, 04.10.).
       Nur im Firmenmodus sichtbar; ohne Firmenmodus gibt es weiter den
       automatischen Kommentar mit Behalten/Verwerfen. */
    const adoptBtn = el('button', { cls: 'btn llm-adopt-btn', type: 'button', id: 'llm-adopt-btn',
                                    text: I18n.t('llm.preview.uebernehmen') });
    adoptBtn.style.display = 'none';
    adoptBtn.addEventListener('click', () => _onUebernehmen(panel));
    actions.appendChild(adoptBtn);
    preview.appendChild(actions);

    wrap.appendChild(preview);
    panel.appendChild(wrap);
    return panel;
  }

  /* ── Render-Hook (pro Karten-Öffnung) ───────────────────────────── */

  async function render(card, boardId) {
    _renderGen += 1;
    _currentCard = card || null;
    _currentBoardId = boardId || null;
    /* Beim Karten-Wechsel: aktive Conv + Stream zurücksetzen. */
    _currentConvId = null;
    _conversations = [];
    if (_activeStreamAbort) { try { _activeStreamAbort.abort(); } catch {} _activeStreamAbort = null; }
    const panel = document.querySelector('#tab-panel-llm');
    if (!panel || !card) return;

    /* Reset Preview */
    _resetPreview(panel);
    _renderConvBar(panel);
    _hideTurnHistory(panel);
    _refreshProviderLockState(panel);

    /* Anhänge-Hinweis */
    const attNote = panel.querySelector('#llm-attach-note');
    const nAttach = Array.isArray(card.attachments) ? card.attachments.length : 0;
    if (attNote) attNote.textContent = nAttach > 0 ? I18n.t('llm.context.attachments-note', nAttach) : '';
    _bildMasseCache = new Map();
    /* Baut nur die Liste — ob das gewaehlte Modell Bilder kann, wird erst am
       Ende von render() bewertet: Provider- und Modell-Auswahl sind hier noch
       leer, und eine Bewertung gegen ein leeres Modell sperrt die Auswahl mit
       der Begruendung „ kann keine Bilder verarbeiten" (ohne Modellnamen). */
    _renderAnhangAuswahl(panel, card, { bewerten: false });
    _refreshFetchHinweis(panel);

    /* Default-System-Prompt setzen (nur wenn leer — User-Edits bewahren) */
    const presetSel = panel.querySelector('#llm-preset-sel');
    const sysPrompt = panel.querySelector('#llm-system-prompt');
    if (presetSel && sysPrompt && !sysPrompt.value) {
      const k = presetSel.value || 'summarize';
      sysPrompt.value = I18n.t(`llm.preset.system.${k}`);
    }

    /* Provider-Dropdown lazy befüllen (einmal pro Modal-Build via dataset-Flag) */
    const provSel = panel.querySelector('#llm-provider-sel');
    const modelSel = panel.querySelector('#llm-model-sel');
    const status = panel.querySelector('#llm-status');
    const sendBtn = panel.querySelector('#llm-send-btn');
    if (provSel && !provSel.dataset.populated) {
      provSel.disabled = true;
      provSel.innerHTML = '';
      provSel.appendChild(el('option', { value: '', text: I18n.t('llm.provider.loading') }));
      const keys = await _loadKeys();
      /* Karte #664: Im Firmenmodus bietet der Server genau seine Anbieter an
         (heute nur „openai“) — ohne eigenen Schlüssel der Person. */
      const firmenAnbieter = _firmenAnbieter(keys);
      const providers = firmenAnbieter.length > 0 ? firmenAnbieter
        : (Array.isArray(keys.keys) ? keys.keys.map(k => k.provider) : []);
      provSel.innerHTML = '';
      if (providers.length === 0) {
        if (status) status.textContent = I18n.t('llm.provider.no-keys');
        if (sendBtn) sendBtn.disabled = true;
        provSel.dataset.populated = '1';
        return;
      }
      providers.forEach(p => {
        provSel.appendChild(el('option', { value: p, text: _anbieterLabel(p) }));
      });
      provSel.dataset.populated = '1';
      _populateModels(modelSel, provSel.value);
      _refreshPrivacyBanner(panel);
      _refreshProviderLockState(panel);
      if (sendBtn) sendBtn.disabled = false;
      if (status) status.textContent = '';
    }
    /* Jetzt erst: Provider und Modell stehen fest (oder es gibt keine Keys —
       dann ist die Funktion oben schon zurueckgekehrt). */
    _refreshAnhangZeilen(panel);
    /* Karte #664: Erst jetzt ist die PII-Vorgabe des Betreibers geladen — der
       Hinweis an der Nachlade-Checkbox wurde oben ggf. ohne sie geschrieben. */
    _refreshFetchHinweis(panel);
    _refreshEstimate(panel);
    /* Pending-Lock-Polling starten (Multi-User-Sichtbarkeit, ADR-0095 §5). */
    _startPendingPoll(panel);
    /* Conversations asynchron laden + Bar neu rendern (#300 Etappe 3). */
    _loadConversations(panel).catch(() => {});
  }

  /** Public stopPolling — Modal-Schließen oder Tab-Wechsel ruft das, um den
   *  Timer aufzuräumen. Im Polling-Test ebenfalls genutzt. */
  function stopPolling() {
    _stopPendingPoll();
  }

  function _resetPreview(panel) {
    const body = panel.querySelector('#llm-preview-body');
    const actions = panel.querySelector('#llm-preview-actions');
    if (body) {
      body.innerHTML = '';
      body.appendChild(el('p', { cls: 'llm-preview-empty', text: I18n.t('llm.tab.empty-state') }));
    }
    if (actions) actions.style.display = 'none';
    /* Die Abruf-Liste gehört zur gezeigten Antwort — beim Kartenwechsel wäre
       sie sonst die Liste der VORHERIGEN Karte. */
    _pendingFetches = [];
    _renderFetchListe(panel);
    _lastReply = null;
  }

  function _switchSubTab(panel, sub) {
    panel.querySelectorAll('.llm-subtab').forEach(b => {
      b.classList.toggle('llm-subtab-active', b.dataset.sub === sub);
    });
    panel.querySelectorAll('[data-sub-pane]').forEach(p => {
      p.dataset.subActive = p.dataset.subPane === sub ? '1' : '0';
    });
  }

  /* Karte #570 (K-B1/K-B2, Session-Kritiker Sprint 72): Ist „openai“ auf ein
     eigenes Gegenüber umgestellt (z. B. lokales vLLM), meldet der Server das
     in der Schlüssel-Liste. Dann gelten dessen Modellnamen, und Beschriftung
     wie Datenschutz-Hinweis nennen den echten Empfänger. */
  function _kompatibel(provider) {
    return provider === 'openai' ? (_keysCache?.openai_kompatibel || null) : null;
  }
  function _anbieterLabel(provider) {
    const k = _kompatibel(provider);
    if (k) return I18n.t('llm.openai-kompatibel', k.ziel || '?');
    return PROVIDER_LABEL[provider] || provider;
  }
  function _modelleFuer(provider) {
    /* Karte #664: Im Firmenmodus nur die freigegebenen Modelle — der Server
       lehnt jedes andere ab (ADR-0133 Nachtrag A1/A2). */
    const fm = _keysCache && _keysCache.firmenmodus;
    if (fm && _firmenAnbieter(_keysCache).includes(provider) && Array.isArray(fm.modelle)) {
      return fm.modelle;
    }
    const k = _kompatibel(provider);
    if (k && Array.isArray(k.modelle) && k.modelle.length) return k.modelle;
    return MODELS[provider] || [];
  }

  function _populateModels(sel, provider) {
    if (!sel) return;
    sel.innerHTML = '';
    _modelleFuer(provider).forEach(m => {
      sel.appendChild(el('option', { value: m, text: m }));
    });
  }

  function _refreshPrivacyBanner(panel) {
    const provSel = panel.querySelector('#llm-provider-sel');
    const banner = panel.querySelector('#llm-privacy');
    if (!provSel || !banner) return;
    if (!provSel.value) { banner.textContent = ''; return; }
    banner.textContent = I18n.t('llm.privacy-warning', _anbieterLabel(provSel.value));
  }

  function _collectFields(panel) {
    const out = [];
    panel.querySelectorAll('[data-ctx-field]').forEach(cb => {
      if (cb.checked) out.push(cb.dataset.ctxField);
    });
    return out;
  }

  /* Kontext-Vorschau bauen — Format MUSS deterministisch sein und MUSS
     dem Server-BuildPrompt aus server/llm/context.go entsprechen (ADR-0095
     §2). Frontend nutzt sie nur für die Token-Schätzung; gesendet wird
     `includeFields` und der Server serialisiert kanonisch. */
  /** Sammelt RefSummaries für alle in card.links referenzierten Karten,
   *  damit LLMPii.buildPrompt server-äquivalent serialisieren kann
   *  (#301, ADR-0097). Lookup über alle Sprints im State.board. */
  function _buildRefsByID(card) {
    const out = {};
    if (!card || !Array.isArray(card.links) || card.links.length === 0) return out;
    const st = (State && typeof State.get === 'function') ? State.get() : null;
    const board = st && st.board;
    if (!board || !Array.isArray(board.sprints)) return out;
    const wanted = new Set(card.links.map((l) => l && l.targetId).filter(Boolean));
    for (const sp of board.sprints) {
      if (!Array.isArray(sp.cards)) continue;
      for (const c of sp.cards) {
        if (!wanted.has(c.id)) continue;
        out[c.id] = {
          displayId: Number(c.displayId) || 0,
          title: c.title || '',
          snippet: c.description || '',
        };
      }
    }
    return out;
  }

  function _buildContextPreview(card, fields) {
    if (!card) return '';
    const parts = [`# Karte #${card.displayId || '?'}: ${card.title || ''}`];
    if (fields.includes('description') && card.description) {
      parts.push('## Beschreibung\n' + card.description);
    }
    if (fields.includes('comments') && Array.isArray(card.comments) && card.comments.length) {
      const last = card.comments.slice(-10);
      parts.push('## Kommentare\n' + last.map(c =>
        `- [${c.created || ''} ${c.author || ''}]: ${c.text || ''}`).join('\n'));
    }
    if (fields.includes('links') && Array.isArray(card.links) && card.links.length) {
      parts.push('## Verweise\n' + card.links.map(l =>
        `- ${l.type}: ${l.targetId}`).join('\n'));
    }
    if (fields.includes('agentContext') && card.agentContext) {
      parts.push('## Agent-Kontext\n' + card.agentContext);
    }
    if (fields.includes('activity') && Array.isArray(card.activity) && card.activity.length) {
      parts.push('## Aktivität\n' + card.activity.map(a =>
        `- [${a.ts || ''} ${a.actor || ''}] ${a.action || ''}: ${a.from || ''} → ${a.to || ''}`).join('\n'));
    }
    return parts.join('\n\n');
  }

  /* F-3102 (Karte #1176): Input-Satz je Million Tokens passend zur Prompt-Länge.
     `entry.Staffel` (optional, aus GET /llm/pricing) gilt, wenn der Prompt
     MEHR als `UeberPromptTokens` Tokens hat — gleiche Regel wie der Server
     (server/llm/pricing.go, PricingEntry.Satz). Fehlt das Feld (älterer
     Server), gilt der Grundsatz. */
  function _inputSatz(entry, tokens) {
    const st = entry && entry.Staffel;
    if (st && typeof st.UeberPromptTokens === 'number' && tokens > st.UeberPromptTokens
        && typeof st.InputPerMillionUSD === 'number') {
      return st.InputPerMillionUSD;
    }
    return entry ? entry.InputPerMillionUSD : 0;
  }

  async function _refreshEstimate(panel) {
    const gen = _renderGen;
    const provSel = panel.querySelector('#llm-provider-sel');
    const modelSel = panel.querySelector('#llm-model-sel');
    const sysPrompt = panel.querySelector('#llm-system-prompt');
    const out = panel.querySelector('#llm-estimate');
    if (!provSel || !modelSel || !sysPrompt || !out) return;
    if (!_currentCard) { out.textContent = ''; return; }
    const fields = _collectFields(panel);
    const totalText = (sysPrompt.value || '') + '\n' + _buildContextPreview(_currentCard, fields);
    /* #429: Bilder zaehlen mit — und zwar VOR dem Senden. Ein Bild kostet
       leicht mehr Tokens als die ganze Karte drumherum; die Zahl erst
       hinterher zu zeigen waere die nutzlose Haelfte der Auskunft. */
    const bild = await _bildTokensDerAuswahl(panel);
    const tokens = estimateTokens(totalText) + bild.tokens;
    const pricing = await _loadPricing();
    const entry = pricing && pricing[provSel.value] && pricing[provSel.value][modelSel.value];
    let text;
    if (entry && (entry.InputPerMillionUSD || entry.OutputPerMillionUSD)) {
      const cost = (tokens / 1_000_000) * _inputSatz(entry, tokens);
      text = I18n.t('llm.send.estimate', tokens, cost);
    } else {
      text = I18n.t('llm.send.estimate-no-price', tokens);
    }
    if (bild.unbekannt > 0) text += ' ' + I18n.t('llm.attach.size-unknown', bild.unbekannt);
    /* Inzwischen eine andere Karte? Dann gehört diese Zahl nicht mehr hierher. */
    if (gen !== _renderGen) return;
    out.textContent = text;
  }

  async function _onSend(panel) {
    const status = panel.querySelector('#llm-status');
    const sendBtn = panel.querySelector('#llm-send-btn');
    if (!_currentCard || !sendBtn) return;

    /* Karte und Board EINMAL festhalten (Kritiker-Lauf 2026-09-17, P1).
       `_onSend` las beide Modul-Variablen bis Sprint 65 nach jedem `await`
       neu — auch nach `_loadPricing()` und nach dem PII-Modal, das laut
       eigenem Kommentar „minutenlang offen bleiben" kann. Wechselt die Karte
       in diesem Fenster, ging die Anfrage an die falsche Adresse und die
       Antwort wurde dort persistiert: Die URL (Z. ~740) und `_lastSendMeta`
       entstehen erst NACH dem Modal.

       Die lokale Kopie ist dabei der wirksamere Teil und der Gen-Check der
       zweite: Selbst wenn das Fenster praktisch schwer zu treffen ist (das
       PII-Overlay fängt den Fokus), adressiert der Sendepfad damit
       strukturell immer die Karte, für die der Nutzer auf „Senden" gedrückt
       hat — unabhängig davon, was ein künftiger Umbau am Modal ändert. */
    const sendCard = _currentCard;
    const sendBoardId = _currentBoardId;
    const gen = _renderGen;

    const provider = panel.querySelector('#llm-provider-sel')?.value;
    const model = panel.querySelector('#llm-model-sel')?.value;
    let systemPrompt = panel.querySelector('#llm-system-prompt')?.value || '';
    const includeFields = _collectFields(panel);
    const presetSel = panel.querySelector('#llm-preset-sel');
    const isCustomPrompt = presetSel && presetSel.value === 'custom';
    if (!provider || !model) {
      if (status) status.textContent = I18n.t('llm.error-bad-request');
      return;
    }
    if (!sendBoardId) {
      if (status) status.textContent = I18n.t('llm.error-server');
      return;
    }

    /* Pre-Send-Confirm bei hohen Kosten (ADR-0095 §7) */
    const pricing = await _loadPricing();
    const entry = pricing && pricing[provider] && pricing[provider][model];
    if (entry && entry.InputPerMillionUSD) {
      const ctxText = _buildContextPreview(sendCard, includeFields);
      const tokens = estimateTokens((systemPrompt || '') + '\n' + ctxText);
      const estCost = (tokens / 1_000_000) * _inputSatz(entry, tokens);
      if (estCost >= COST_CONFIRM_USD) {
        if (!confirm(I18n.t('llm.send.confirm-expensive', estCost))) return;
      }
    }

    /* PII-Preview vor Send (#301, ADR-0097).
       Filter-Modus aus localStorage; "never" überspringt. Bei "custom" greift
       der Filter nur wenn der User einen Custom-System-Prompt eingegeben hat
       (Standard-Presets gelten als geprüft). */
    let userPromptOverride = '';
    /* Karte #664: Die Betreiber-Vorgabe steht in der Schlüssel-Liste. Sie ist
       normalerweise längst geladen (render); nach invalidateKeysCache() aber
       nicht — dann hier nachladen, sonst gälte still die Nutzerwahl. */
    await _loadKeys();
    const piiMode = _piiModus();
    const piiActive = LLMPii && (piiMode === 'always' || (piiMode === 'custom' && isCustomPrompt));
    if (piiActive) {
      const refsByID = _buildRefsByID(sendCard);
      const userPrompt = LLMPii.buildPrompt(sendCard, includeFields, refsByID);
      const allFinds = LLMPii.detect((systemPrompt || '') + '\n' + userPrompt);
      /* Auch OHNE Textreffer öffnen, wenn Bilder mitgehen (#429, ADR-0121 §5).
         Sonst passiert genau das Falsche: Der Filter findet im Text nichts,
         das Modal bleibt zu — und der Nutzer liest das als „geprüft, sauber",
         während ein Ausweisfoto im Anhang ungesehen rausgeht. Der Filter kann
         Bildinhalte grundsätzlich nicht sehen; das muss er in dem Moment
         sagen, in dem entschieden wird. */
      const anhangAnzahl = _collectAnhaenge(panel).length;
      if (allFinds.length > 0 || anhangAnzahl > 0) {
        /* F-396: Lost-Update-Schutz. Das PII-Modal kann minutenlang offen
           bleiben (Treffer prüfen/redacten), während eine andere Person die
           Karte per Op-Inbox editiert — _currentCard bleibt seit render()
           unverändert im Modul-Scope hängen. getFreshUserPrompt liefert bei
           jedem Send-Klick im Modal den aktuell im (gepollten) State
           liegenden Stand, server-äquivalent serialisiert wie userPrompt
           selbst, damit LLMPii.openPreviewModal einen fairen Hash-Vergleich
           ziehen kann (Karte #378). */
        const getFreshUserPrompt = () => {
          const st = (State && typeof State.get === 'function') ? State.get() : null;
          const board = st && st.board;
          const hit = board ? findCardById(board, sendCard.id) : null;
          if (!hit) return null;
          return LLMPii.buildPrompt(hit.card, includeFields, _buildRefsByID(hit.card));
        };
        const r = await LLMPii.openPreviewModal({
          systemPrompt: systemPrompt || '',
          userPrompt,
          estimateTokens,
          getFreshUserPrompt,
          /* #429: Der Filter sieht Bildinhalte nicht — das gehoert in den
             Moment, in dem der Nutzer ueber das Senden entscheidet. */
          anhangAnzahl,
        });
        /* Nach dem Modal: Ist noch dieselbe Karte offen? Wenn nicht, wurde
           hier nichts gesendet — abbrechen ist die einzige Antwort, die den
           Nutzer nicht überrascht. */
        if (gen !== _renderGen) {
          if (sendBtn) sendBtn.disabled = false;
          return;
        }
        if (!r || r.action !== 'send') {
          /* User hat abgebrochen — Send-Button bleibt freigegeben */
          return;
        }
        systemPrompt = r.systemPrompt;
        userPromptOverride = r.userPrompt;
      }
    }

    /* Stream-Pfad (#300 Etappe 3, ADR-0096 §1+§6) — Token-für-Token-Render
       via SSE. Bei aktiver Conversation: conversationId mitschicken (Server
       lädt History + prüft Token-Budget). Cancel via AbortController. */
    const oldText = sendBtn.textContent;
    const cancelBtn = panel.querySelector('#llm-cancel-btn');
    sendBtn.disabled = true;
    sendBtn.textContent = I18n.t('llm.send.sending');
    if (cancelBtn) cancelBtn.style.display = '';
    if (status) status.textContent = '';

    const previewBody = panel.querySelector('#llm-preview-body');
    _streamReplyBuf = '';

    const sendBody = { provider, model, systemPrompt, includeFields };
    if (userPromptOverride) sendBody.userPromptOverride = userPromptOverride;
    if (_currentConvId) sendBody.conversationId = _currentConvId;
    /* #300 Etappe 5: Mutations-Opt-in nur wenn Checkbox aktiv. */
    const allowMut = !!panel.querySelector('#llm-allow-mutations')?.checked;
    if (allowMut && !_werkzeugeAus(provider)) sendBody.enableMutations = true;
    /* #428: Nachladen nur auf Opt-in — und nur ohne PII-Override. Der Server
       prueft das ebenfalls (fetchVerfuegbar); hier wird es zusaetzlich
       BENANNT, damit niemand auf eine Anreicherung wartet, die nicht kommt. */
    const allowFetch = !!panel.querySelector('#llm-allow-fetch')?.checked;
    if (allowFetch && !userPromptOverride && !piiActive && !_werkzeugeAus(provider)) sendBody.enableFetch = true;
    const fetchUnterdrueckt = allowFetch && (!!userPromptOverride || piiActive) && !_werkzeugeAus(provider);
    /* #429: ausgewaehlte Bild-Anhaenge. Es reisen nur die ids — die Bytes
       laedt der Server aus dem Attachment-Store (der Request-Body ist auf
       64 KB begrenzt, und ein zweiter Upload waere ohnehin verschwendet). */
    const anhaenge = _collectAnhaenge(panel);
    if (anhaenge.length > 0) sendBody.attachmentIds = anhaenge;
    /* ADR-0120 §4, korrigiert nach dem Kritiker-Lauf: Der Server muss wissen,
       ob der Filter AKTIV war — nicht, ob er etwas gefunden hat. Vorher war
       `userPromptOverride` der Stellvertreter, und der entsteht nur bei
       Treffern: Bei aktivem Filter ohne Treffer lief das Nachladen, obwohl
       die ADR das Gegenteil zusagt. */
    if (piiActive) sendBody.piiFilterActive = true;

    /* Frische Mutations-Sammlung + Provider-/Model-Snapshot für Audit-Meta. */
    _pendingMutations = [];
    _pendingFetches = [];
    _renderFetchListe(panel);
    _lastSendMeta = { provider, model, cardId: sendCard.id, boardId: sendBoardId };

    _activeStreamAbort = new AbortController();
    let doneMeta = null;
    let streamErr = null;
    let firstToken = true;

    try {
      await LLMStream.streamLLM({
        url: `/boards/${encodeURIComponent(sendBoardId)}/cards/${encodeURIComponent(sendCard.id)}/llm-send-stream`,
        body: sendBody,
        signal: _activeStreamAbort.signal,
        on: {
          token: (d) => {
            _streamReplyBuf += (d && d.text) || '';
            if (previewBody) {
              if (firstToken) {
                previewBody.innerHTML = '';
                _switchSubTab(panel, 'reply');
                firstToken = false;
              }
              previewBody.innerHTML = '';
              previewBody.appendChild(Markdown.render(_streamReplyBuf));
            }
          },
          toolCall: (d) => {
            /* Server-Etappe 4: payload {id,name,allowed:true,args:{...}} ODER
               {id,name,blocked:true,reason:'...'}. Beide sammeln — der Dialog
               zeigt blockierte Vorschläge fail-loud (ADR-0096 §4). */
            if (d && typeof d === 'object') _pendingMutations.push(d);
          },
          fetch: (d) => {
            /* Kein Vorschlag zum Bestaetigen — der Abruf ist bereits
               geschehen. Wir zeigen ihn, damit sichtbar bleibt, was in den
               Kontext gewandert ist (und was abgelehnt wurde). */
            if (d && typeof d === 'object') _pendingFetches.push(d);
            _renderFetchListe(panel);
          },
          done: (d) => { doneMeta = d || {}; },
          error: (d) => { streamErr = d || { kind: 'internal', message: 'unknown stream error' }; },
        },
      });
    } catch (e) {
      if (e && e.name === 'StreamPreError') {
        /* Pre-Stream-Fehler: 423 Lock, 400 context-budget, 412 no-key, ... */
        if (e.status === 400 && e.body && e.body.kind === 'context-budget') {
          if (status) status.textContent = I18n.t('llm.streaming.budget-exceeded');
        } else {
          if (status) status.textContent = _errorText(e.status, e.body);
        }
      } else if (e && (e.name === 'AbortError' || (e.message || '').includes('abort'))) {
        if (status) status.textContent = I18n.t('llm.streaming.cancelled');
      } else {
        if (status) status.textContent = I18n.t('llm.error-provider-unreachable');
      }
      _activeStreamAbort = null;
      if (cancelBtn) cancelBtn.style.display = 'none';
      sendBtn.disabled = false;
      sendBtn.textContent = oldText;
      return;
    }

    _activeStreamAbort = null;
    if (cancelBtn) cancelBtn.style.display = 'none';
    sendBtn.disabled = false;
    sendBtn.textContent = oldText;

    if (streamErr) {
      if (status) status.textContent = _streamErrorText(streamErr);
      return;
    }
    if (!doneMeta) {
      /* Stream ohne done-Event = unvollständig (z. B. Verbindung abgebrochen). */
      if (status) status.textContent = I18n.t('llm.error-provider-unreachable');
      return;
    }

    /* Erfolgreich abgeschlossen — UI wie im klassischen Pfad. */
    if (status) {
      let text = I18n.t('llm.preview.success',
        doneMeta.inputTokens || 0, doneMeta.outputTokens || 0, doneMeta.costEstimateUSD || 0);
      if (fetchUnterdrueckt) text += ' ' + I18n.t('llm.fetch.suppressed-pii');
      /* Karte #674: Im Firmenmodus ohne Verlauf ist die Antwort nirgends
         gespeichert — das muss die Person wissen, bevor sie die Karte schliesst. */
      if (!doneMeta.commentId && !_currentConvId) {
        text += ' ' + I18n.t('llm.firmenmodus.nicht-gespeichert');
        if (_laufImFirmenmodus(doneMeta) && _streamReplyBuf) text += ' ' + I18n.t('llm.firmenmodus.hinweis-uebernehmen');
      }
      status.textContent = text;
    }
    /* `sendCard`/`sendBoardId`, nicht die Live-Variablen (Verify-Lauf
       2026-09-17): Diese Zuweisung stand zwei Zeilen unter der Begründung,
       warum es die Kopien gibt, und las trotzdem den Modulzustand. `_lastReply`
       trägt die Adresse, unter der `_onDiscard` später verwirft — sie muss die
       Karte meinen, für die gesendet wurde, nicht die gerade offene.
       Im Normalfall bricht `render()` den Stream bei einem Kartenwechsel ab
       und der Code kommt hier gar nicht an; das Restfenster (Stream fertig,
       Abbruch eine Zeile zu spät) bleibt aber, und die Kopie kostet nichts. */
    _lastReply = {
      commentId: doneMeta.commentId,
      boardId: sendBoardId,
      cardId: sendCard.id,
      replyText: _streamReplyBuf,
      /* Karte #674: für die Kennzeichnung beim Übernehmen als Kommentar. */
      provider: _lastSendMeta?.provider || '',
      model: _lastSendMeta?.model || '',
      /* K2-4: Firmenmodus laut Server für genau diesen Lauf. */
      firmenmodus: _laufImFirmenmodus(doneMeta),
    };
    /* Actions-Bar (Keep/Discard) erst nach done sichtbar — und nur, wenn es
       einen Kommentar gibt. Im Firmenmodus (Karte #674) schreibt der Server
       keinen; „In Kommentar belassen“ wäre falsch, „Verwerfen“ liefe ins Leere. */
    /* Karte #674: Im Firmenmodus statt Behalten/Verwerfen nur „Als Kommentar
       übernehmen“. Dass die Person schreiben darf (editor+), ist hier schon
       bewiesen: Ohne editor lehnt der ACLGuard den Send vor dem Stream ab, es
       gibt also kein done und keinen Knopf. Geschrieben wird trotzdem über
       den normalen, vom Server ACL-geprüften Kommentarweg. */
    _zeigeAktionen(panel, !!doneMeta.commentId, !doneMeta.commentId && _laufImFirmenmodus(doneMeta) && !!_streamReplyBuf);

    /* Bei aktiver Conversation: Turn-History neu laden, damit der frische
       Turn-Block sichtbar wird. Sonst Single-Turn-Modus (kein Reload). */
    if (_currentConvId) {
      _loadAndRenderTurns(panel).catch(() => {});
    }

    /* #300 Etappe 5: Mutations-Vorschläge → Confirm-Dialog (KEIN „Apply All"). */
    if (_pendingMutations.length > 0) {
      const calls = _pendingMutations.slice();
      _pendingMutations = [];
      _openMutationDialog(panel, calls, doneMeta || {});
    }
  }

  /* ── Bild-Anhänge mitschicken (#429, ADR-0121) ───────────────────── */

  /* Die MIME-Typen, die alle vier Anbieter verstehen. Gespiegelt zu
     AllowedImageMIME in server/llm/vision.go — beide Seiten müssen dieselbe
     Menge kennen, sonst zeigt die Oberfläche eine Auswahl an, die der Server
     ablehnt (oder blendet eine aus, die gegangen wäre). Wer hier etwas
     ergänzt, fasst beide Stellen an. */
  const BILD_MIME = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];

  /* F-858: Die Grenzen selbst kommen NICHT mehr als Literal hierher — genau
     die zweite Liste, die der Kommentar bei `Vision` (siehe `_modellKannBilder`)
     ausdrücklich vermeidet. Sie reisen stattdessen als `limits` in derselben
     `GET /llm/pricing`-Antwort mit, aus der auch die Preise und `Vision`
     gelesen werden (ADR-0121 §4). Diese beiden Werte sind nur der Rückfall
     für einen älteren Server, der `limits` noch nicht liefert — vorher waren
     es die einzigen, jetzt vertriebsbereiten Werte. */
  const MAX_BILD_BYTES_FALLBACK = 5 * 1024 * 1024;
  const MAX_BILDER_FALLBACK = 5;

  /**
   * Bild-Grenzen aus `GET /llm/pricing` (F-858, ADR-0121 §4): `limits: {
   * maxImageBytes, maxImages, maxImageBytesTotal }`. Fehlt `limits` (älterer
   * Server), gelten die Fallback-Werte — und für die Summe gibt es dann
   * KEINEN Vorab-Check im Frontend (`bytesGesamt: null`), weil es dafür nie
   * einen Frontend-Wert gab; der Server prüft die Summe ohnehin (ADR-0121
   * §3). `_loadPricing()` ist gecacht, ein zusätzlicher Aufruf hier kostet
   * also keine weitere Anfrage.
   */
  async function _bildGrenzen() {
    const pricing = await _loadPricing();
    const limits = pricing && pricing.limits;
    const bytes = limits && Number.isFinite(limits.maxImageBytes)
      ? limits.maxImageBytes : MAX_BILD_BYTES_FALLBACK;
    const anzahl = limits && Number.isFinite(limits.maxImages)
      ? limits.maxImages : MAX_BILDER_FALLBACK;
    const bytesGesamt = limits && Number.isFinite(limits.maxImageBytesTotal)
      ? limits.maxImageBytesTotal : null;
    return { bytes, anzahl, bytesGesamt };
  }

  /**
   * Token-Schätzung für ein Bild aus seinen Abmessungen.
   *
   * Die Formel (Breite × Höhe / 750) ist die von Anthropic dokumentierte;
   * OpenAI und Google rechnen anders, liegen aber in derselben
   * Größenordnung. Das ist eine Schätzung und wird in der Oberfläche auch so
   * benannt — eine Zahl, die genauer aussieht, als sie ist, wäre schlechter
   * als eine ehrliche Näherung.
   *
   * Ohne Abmessungen gibt es KEINE Zahl (null), nicht etwa eine aus der
   * Dateigröße geratene: Bei Bildern sagt die Byte-Zahl fast nichts über die
   * Pixelzahl — dieselben 200 KB sind ein kleines PNG oder ein grosses JPEG.
   */
  function _bildTokenSchaetzung(breite, hoehe) {
    if (!breite || !hoehe || breite <= 0 || hoehe <= 0) return null;
    return Math.ceil((breite * hoehe) / 750);
  }

  /** Bild-Anhänge einer Karte (die anderen können ohnehin nicht mit).
   *
   * F-1088: Mail-Ingest-Altbestand vor Sprint 69 (F-358) steht als reiner
   * id-String in card.attachments — normalisiereAnhang macht daraus {id},
   * ohne mime/filename/size. Ein solcher Eintrag KANN ein Bild sein (der
   * Mail-Ingest schrieb vor F-855 jeden Anhang so, nicht nur Bilder), lässt
   * sich hier aber nicht ausschliessen — also als Kandidat aufnehmen statt
   * ihn stillschweigend zu verstecken. Den Typ prüft der Server beim Senden
   * (F-855, http.DetectContentType); die Auswahl-Zeile weist unten darauf
   * hin (siehe llm.attach.type-unknown). Ein Eintrag MIT explizitem,
   * nicht-Bild-mime (normaler Neubestand, z. B. PDF) fliegt weiterhin raus. */
  function _bildAnhaenge(card) {
    if (!card || !Array.isArray(card.attachments)) return [];
    return card.attachments.map(normalisiereAnhang).filter(a => {
      if (!a) return false;
      if (a.mime == null) return true;
      return BILD_MIME.includes(String(a.mime).toLowerCase());
    });
  }

  /** Lädt die Abmessungen eines Bildes. null, wenn das nicht geht. */
  function _ladeBildMasse(url) {
    return new Promise((resolve) => {
      if (typeof Image !== 'function') { resolve(null); return; }
      let fertig = false;
      const img = new Image();
      const ende = (wert) => { if (!fertig) { fertig = true; resolve(wert); } };
      img.onload = () => ende({ breite: img.naturalWidth, hoehe: img.naturalHeight });
      img.onerror = () => ende(null);
      /* Kein Warten ohne Ende: Bleibt das Laden hängen, zeigt die Zeile die
         Größe ohne Token-Zahl, statt dass die Schätzung nie erscheint. */
      setTimeout(() => ende(null), 4000);
      img.src = url;
    });
  }

  function _fmtBytes(n) {
    const b = Number(n) || 0;
    if (b >= 1024 * 1024) return (b / (1024 * 1024)).toFixed(1) + ' MB';
    if (b >= 1024) return Math.round(b / 1024) + ' KB';
    return b + ' B';
  }

  /**
   * Baut die Anhang-Auswahl. Zu grosse Bilder werden gezeigt, aber gesperrt —
   * mit Grund: Ein Anhang darf 50 MB gross sein und trotzdem nicht an ein
   * Modell gehen; ihn wegzulassen liesse den Nutzer suchen.
   */
  async function _renderAnhangAuswahl(panel, card, opts) {
    const gen = _renderGen;
    const box = panel.querySelector('#llm-att-box');
    if (!box) return;
    const bilder = _bildAnhaenge(card);
    if (bilder.length === 0) { box.innerHTML = ''; box.style.display = 'none'; return; }
    const grenzen = await _bildGrenzen();
    /* F-858: Kartenwechsel waehrend `GET /llm/pricing` (Grenzen) laedt — die
       Liste unten gehoert dann schon zur naechsten Karte. Dieselbe
       Wettlauf-Klasse wie bei `_refreshEstimate`/`_refreshAnhangZeilen`
       (siehe die Erlaeuterung zu `_renderGen` weiter oben). */
    if (gen !== _renderGen) return;
    box.innerHTML = '';
    box.style.display = '';
    box.appendChild(el('div', { cls: 'llm-section-h', text: I18n.t('llm.attach.heading') }));

    bilder.forEach((a) => {
      /* F-1088: Altbestand ohne mime hat auch keine verlässliche size — die
         Vorab-Größenprüfung ergibt für ihn keinen Sinn (0 B wäre eine
         Behauptung, keine Messung) und wird übersprungen. Der Server prüft
         Typ UND Größe ohnehin beim Senden (F-855); eine zu grosse Datei
         scheitert dort mit derselben Ablehnung wie sonst auch. */
      const typUnbekannt = a.mime == null;
      const zuGross = !typUnbekannt && Number(a.size) > grenzen.bytes;
      const id = `llm-att-${a.id}`;
      const lbl = el('label', { cls: 'llm-att-row' + (zuGross ? ' llm-att-row-blocked' : ''), for: id });
      const cb = el('input', { type: 'checkbox', id, 'data-att-id': a.id });
      if (zuGross) cb.disabled = true;
      cb.addEventListener('change', () => {
        _refreshAnhangZeilen(panel);
        _refreshEstimate(panel);
      });
      lbl.appendChild(cb);
      lbl.appendChild(el('span', { cls: 'llm-att-name', text: ' ' + (a.filename || a.id) }));
      lbl.appendChild(el('span', { cls: 'llm-att-meta', id: `llm-att-meta-${a.id}`,
                                   text: ' · ' + (typUnbekannt ? I18n.t('llm.attach.type-unknown') : _fmtBytes(a.size)) }));
      if (zuGross) {
        lbl.appendChild(el('span', { cls: 'llm-att-reason',
                                     text: ' — ' + I18n.t('llm.attach.too-large', _fmtBytes(grenzen.bytes)) }));
      }
      box.appendChild(lbl);
    });

    /* Die Grenze des PII-Filters gehört an die Auswahl, nicht in einen
       Kommentar: Der Filter liest Text und sieht Bildinhalte grundsätzlich
       nicht. Wer das nicht weiss, verlässt sich darauf. */
    box.appendChild(el('p', { cls: 'llm-attach-note', text: I18n.t('llm.attach.pii-blind') }));
    const attStatus = el('p', { cls: 'llm-attach-note llm-att-status', id: 'llm-att-status' });
    /* F-860: Hier steht die Begruendung, warum eine Anhang-Auswahl gesperrt
       ist — und gesperrte Elemente sind mit `disabled` nicht fokussierbar.
       Ohne Ansage ist die Begruendung fuer einen Screenreader-Nutzer also
       ausgerechnet dann unerreichbar, wenn er sie braucht. */
    attStatus.setAttribute('aria-live', 'polite');
    box.appendChild(attStatus);
    if (!opts || opts.bewerten !== false) _refreshAnhangZeilen(panel);
  }

  /** Ausgewählte Anhang-ids in Dokument-Reihenfolge. */
  function _collectAnhaenge(panel) {
    const out = [];
    panel.querySelectorAll('[data-att-id]').forEach(cb => {
      if (cb.checked && !cb.disabled) out.push(cb.dataset.attId);
    });
    return out;
  }

  /**
   * Aktualisiert Sperren und Statuszeile der Anhang-Auswahl: Modell ohne
   * Bild-Fähigkeit → alles gesperrt mit Grund; Höchstzahl erreicht → die
   * übrigen gesperrt; Gesamtgrösse erreicht (F-858) → die übrigen gesperrt,
   * soweit sie die Summe sprengen würden.
   */
  async function _refreshAnhangZeilen(panel) {
    const gen = _renderGen;
    const box = panel.querySelector('#llm-att-box');
    const status = panel.querySelector('#llm-att-status');
    if (!box || box.style.display === 'none') return;

    const kannBilder = await _modellKannBilder(panel);
    /* Kartenwechsel während des Pricing-Ladens: Die Auswahl unten gehört
       dann schon zur nächsten Karte — nicht mehr anfassen. */
    if (gen !== _renderGen) return;
    const grenzen = await _bildGrenzen();
    if (gen !== _renderGen) return;

    const gewaehlt = _collectAnhaenge(panel);
    /* F-858: Summe der bereits gewählten Bilder — Basis für den Summen-Check.
       Grösse kommt aus dem Karten-Attachment (Behauptung des Uploads), nicht
       aus dem DOM; dieselbe Quelle wie beim Einzel-Limit oben in
       `_renderAnhangAuswahl`. */
    const bilder = _bildAnhaenge(_currentCard);
    const groesseVon = (id) => {
      const bild = bilder.find(a => a.id === id);
      return bild ? (Number(bild.size) || 0) : 0;
    };
    const summeGewaehlt = gewaehlt.reduce((summe, id) => summe + groesseVon(id), 0);
    let summeSperrtWeitere = false;

    box.querySelectorAll('[data-att-id]').forEach((cb) => {
      const zuGross = cb.closest('.llm-att-row')?.classList.contains('llm-att-row-blocked');
      if (zuGross) { cb.disabled = true; return; }
      if (!kannBilder) { cb.checked = false; cb.disabled = true; return; }
      if (cb.checked) { cb.disabled = false; return; } // gewählte bleiben abwählbar
      if (gewaehlt.length >= grenzen.anzahl) { cb.disabled = true; return; }
      /* Ohne `limits` vom Server (aelterer Server) ist `bytesGesamt` null —
         dann gibt es hier bewusst KEINEN Vorab-Check (der Server prüft die
         Summe ohnehin, ADR-0121 §3). */
      if (grenzen.bytesGesamt !== null
          && summeGewaehlt + groesseVon(cb.dataset.attId) > grenzen.bytesGesamt) {
        cb.disabled = true;
        summeSperrtWeitere = true;
        return;
      }
      cb.disabled = false;
    });
    if (!status) return;
    if (!kannBilder) {
      const modelSel = panel.querySelector('#llm-model-sel');
      status.textContent = I18n.t('llm.attach.no-vision', modelSel ? modelSel.value : '');
      return;
    }
    if (gewaehlt.length >= grenzen.anzahl) {
      status.textContent = I18n.t('llm.attach.limit-reached', grenzen.anzahl);
    } else if (summeSperrtWeitere) {
      status.textContent = I18n.t('llm.attach.total-limit-reached', _fmtBytes(grenzen.bytesGesamt));
    } else {
      status.textContent = '';
    }
  }

  /** Ist der PII-Filter für den nächsten Send aktiv? Dieselbe Bedingung wie
   *  in _onSend — eine zweite Schreibweise derselben Frage würde driften. */
  function _piiAktiv(panel) {
    if (!LLMPii) return false;
    const modus = _piiModus();
    if (modus === 'always') return true;
    const presetSel = panel.querySelector('#llm-preset-sel');
    return modus === 'custom' && !!presetSel && presetSel.value === 'custom';
  }

  /**
   * Schreibt den Hinweis an der Nachlade-Checkbox.
   *
   * Er war zuerst ein fester Satz („Nur ohne PII-Filter …"), der unabhängig
   * vom tatsächlichen Zustand dastand — ADR-0120 §4 sagt aber zu, dass die
   * Oberfläche den GRUND nennt, nicht bloss die Regel. Bei aktivem Filter
   * steht jetzt dort, dass nichts nachgeladen wird; sonst die Bedingung.
   */
  /* F-1549: Meldet der Server `openai_werkzeuge: 'aus'`
     (ABOARD_LLM_OPENAI_WERKZEUGE=aus), schickt er dem Anbieter „openai“ kein
     `tools`. Änderungsvorschläge und Karten-Nachladen hängen daran — die
     Oberfläche bietet sie für diesen Anbieter dann nicht an, statt Schalter
     zu zeigen, die ins Leere laufen. */
  function _werkzeugeAus(provider) {
    return provider === 'openai' && !!_keysCache && _keysCache.openai_werkzeuge === 'aus';
  }

  function _refreshWerkzeugAngebote(panel) {
    const aus = _werkzeugeAus(panel.querySelector('#llm-provider-sel')?.value);
    for (const id of ['llm-allow-mut', 'llm-allow-fetch']) {
      const lbl = panel.querySelector('#' + id + '-lbl');
      const cb = panel.querySelector(id === 'llm-allow-mut' ? '#llm-allow-mutations' : '#llm-allow-fetch');
      if (lbl) lbl.style.display = aus ? 'none' : '';
      if (cb && aus) cb.checked = false;
    }
    const note = panel.querySelector('#llm-fetch-note');
    if (note && aus) note.textContent = I18n.t('llm.werkzeuge-aus');
    return aus;
  }

  function _refreshFetchHinweis(panel) {
    const note = panel.querySelector('#llm-fetch-note');
    if (!note) return;
    if (_refreshWerkzeugAngebote(panel)) return;
    note.textContent = _piiAktiv(panel)
      ? I18n.t('llm.fetch.pii-blocked')
      : I18n.t('llm.fetch.pii-note');
  }

  /** Kann das gewählte Modell Bilder? Quelle ist die Pricing-Tabelle vom
   *  Server (Feld `Vision`) — es gibt bewusst keine zweite Liste im
   *  Frontend, die still veralten könnte. Unbekannt → false. */
  async function _modellKannBilder(panel) {
    const prov = panel.querySelector('#llm-provider-sel')?.value;
    const model = panel.querySelector('#llm-model-sel')?.value;
    if (!prov || !model) return false;
    const pricing = await _loadPricing();
    const entry = pricing && pricing[prov] && pricing[prov][model];
    return !!(entry && entry.Vision);
  }

  /**
   * Token-Schätzung der ausgewählten Bilder. Liefert {tokens, unbekannt}:
   * `unbekannt` zählt die Bilder, deren Abmessungen nicht zu ermitteln waren
   * — die Oberfläche sagt das, statt sie stillschweigend als 0 zu zählen.
   */
  async function _bildTokensDerAuswahl(panel) {
    const ids = _collectAnhaenge(panel);
    if (ids.length === 0 || !_currentCard || !_currentBoardId) return { tokens: 0, unbekannt: 0 };
    let tokens = 0, unbekannt = 0;
    for (const id of ids) {
      let masse = _bildMasseCache.get(id);
      if (masse === undefined) {
        const url = `/boards/${encodeURIComponent(_currentBoardId)}/attachments/${encodeURIComponent(_currentCard.id)}/${encodeURIComponent(id)}`;
        masse = await _ladeBildMasse(url);
        _bildMasseCache.set(id, masse);
      }
      const t = masse ? _bildTokenSchaetzung(masse.breite, masse.hoehe) : null;
      if (t === null) unbekannt += 1; else tokens += t;
    }
    return { tokens, unbekannt };
  }

  /* ── Abrufe verknüpfter Karten (#428, ADR-0120) ──────────────────── */

  /**
   * Zeigt, welche verknüpften Karten der Server für diese Antwort nachgeladen
   * hat — und welche Anfragen er abgelehnt hat, mit Grund.
   *
   * Die Ablehnungen mitzuzeigen ist Absicht: Ein Modell, das nach einer nicht
   * verknüpften Karte fragt, ist der Fall, für den die Schranke gebaut ist.
   * Bliebe das unsichtbar, wüsste niemand, dass sie gegriffen hat.
   */
  function _renderFetchListe(panel) {
    const box = panel.querySelector('#llm-fetch-list');
    if (!box) return;
    box.innerHTML = '';
    if (_pendingFetches.length === 0) { box.style.display = 'none'; return; }
    box.style.display = '';
    box.appendChild(el('div', { cls: 'llm-fetch-h', text: I18n.t('llm.fetch.list-heading') }));
    _pendingFetches.forEach((f) => {
      const ok = f && f.ok === true;
      const row = el('div', { cls: 'llm-fetch-row' + (ok ? '' : ' llm-fetch-row-blocked') });
      const name = ok
        ? ((f.label ? f.label + ' ' : '') + (f.title || '')).trim()
        : (f && f.cardId ? String(f.cardId) : '?');
      row.appendChild(el('span', { cls: 'llm-fetch-name', text: (ok ? '✓ ' : '⊘ ') + name }));
      if (!ok && f && f.reason) {
        row.appendChild(el('span', { cls: 'llm-fetch-reason', text: ' — ' + f.reason }));
      }
      box.appendChild(row);
    });
  }

  /**
   * Kumulierte Kosten einer Unterhaltung (#428: „Frontend zeigt sie
   * akkumuliert pro Conversation").
   *
   * Summiert wird aus den persistierten Turns, nicht aus einem
   * Laufzeit-Zähler — sonst stünde die Zahl nach jedem Reload wieder auf
   * null, obwohl die Kosten angefallen sind. Turns ohne Kostenangabe
   * (user-Turns) tragen nichts bei.
   */
  function _summiereTurnKosten(turns) {
    let anfragen = 0, tokens = 0, kosten = 0;
    (turns || []).forEach((t) => {
      if (!t || t.role !== 'assistant') return;
      anfragen += 1;
      tokens += (Number(t.inputTokens) || 0) + (Number(t.outputTokens) || 0);
      kosten += Number(t.costEstimate) || 0;
    });
    return { anfragen, tokens, kosten };
  }

  /* ── Mutations-Confirm-Dialog (#300 Etappe 5, ADR-0096 §3+§4) ──────── */

  /**
   * Öffnet den modalen Confirm-Dialog für die gesammelten Tool-Call-Vorschläge.
   * Jeder Vorschlag wird einzeln bestätigt — kein „Apply All"-Button (Safety).
   * Apply → State.set + Activity-Eintrag llm-mutation-applied; Discard →
   * Activity-Eintrag llm-mutation-discarded (knapp, ohne args-Spam).
   */
  function _openMutationDialog(panel, calls, doneMeta) {
    const author = Config.get('userName') || 'User';
    const meta = {
      conversationId: doneMeta.conversationId || _currentConvId || null,
      turnId:         doneMeta.turnId         || doneMeta.commentId || null,
      provider:       _lastSendMeta?.provider || null,
      model:          _lastSendMeta?.model    || null,
      /* K2-4: Firmenmodus des Laufs, aus dem die Vorschläge stammen. */
      firmenmodus:    _laufImFirmenmodus(doneMeta),
    };

    /* F-622: Der Modal-Vertrag des Projekts — `role`/`aria-modal` am OVERLAY
       (so macht es das Vorbild src/llm-pii.js), `aria-labelledby` auf die
       Überschrift, Initialfokus und Fokus-Rückgabe über src/focus-trap.js.
       Vier andere Dialoge nutzen diesen Standard bereits; dieser hier war die
       Ausnahme, und der Tab-Fokus wanderte hinter das Overlay in das darunter
       liegende Karten-Modal. */
    const titelId = `llm-mut-h-${_newUUID()}`;
    const overlay = el('div', {
      cls: 'llm-mut-overlay', role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': titelId,
    });
    const dialog  = el('div', { cls: 'llm-mut-dialog' });
    const titel = el('h3', { cls: 'llm-mut-h', text: I18n.t('llm.mutation.confirm-title') });
    titel.id = titelId;
    dialog.appendChild(titel);
    dialog.appendChild(el('p',  { cls: 'llm-mut-i', text: I18n.t('llm.mutation.confirm-intro') }));

    const list = el('div', { cls: 'llm-mut-list' });
    let appliedCount = 0;
    let discardedCount = 0;
    let openCount = calls.length;

    /* F-622(b): Merkt sich je Ziel-Feld den zuletzt SELBST in DIESEM Dialog
       angewendeten Wert. Grundlage, um einen Stale-Konflikt vom eigenen
       Dialog (zwei Vorschläge auf dasselbe Feld, der erste schon angewendet)
       von einem externen Akteur zu unterscheiden — sonst meldet der zweite
       Vorschlag "Jemand anderes war schneller", obwohl es niemand Fremdes
       war. Schlüssel im selben JSON-kodierten Format wie `_basisSchluessel`
       in llm-mutations.js (dort nicht exportiert, deshalb hier neu, aber
       gleich berechnet: `JSON.stringify(wert)`). */
    const eigeneAenderungen = new Map();
    function _zielSchluessel(c) {
      if (c.name === 'set-field' && c.args && typeof c.args.field === 'string') return `field:${c.args.field}`;
      if (c.name === 'move') return 'move:columnId';
      return null; /* add-comment/extend-description/add-label sind kommutativ, siehe llm-mutations.js */
    }
    function _neuerWertSchluessel(pvOk) {
      const wert = pvOk.args && ('value' in pvOk.args ? pvOk.args.value : pvOk.args.toColumnId);
      return JSON.stringify(wert === undefined ? null : wert);
    }

    calls.forEach((call) => {
      const row = el('div', { cls: 'llm-mut-row' });
      row.dataset.state = 'pending';
      const toolKey = `llm.mutation.tool-${call.name || 'unknown'}`;
      const toolLabel = I18n.t(toolKey) || call.name || '?';
      row.appendChild(el('div', { cls: 'llm-mut-tool', text: toolLabel }));

      /* Server-blockierte Vorschläge fail-loud zeigen, KEIN Apply-Button. */
      if (call.blocked) {
        row.dataset.state = 'blocked';
        row.appendChild(el('div', { cls: 'llm-mut-err', role: 'alert',
          text: I18n.t('llm.mutation.blocked-by-server', call.reason || '') }));
        list.appendChild(row);
        openCount--;
        discardedCount++;
        return;
      }

      /* Erlaubter Vorschlag: Pre-Apply-Validate gegen aktuellen Karten-Snapshot.
         Gültige Spalten-IDs mitgeben (F-410: move-Ziel muss existieren). */
      const cur = _currentCardFromState();
      const validCols = ((State.get()?.board?.columnDefs) || []).map(cd => cd.id).filter(Boolean);
      /* `let`, nicht `const`: F-622(b) — nach einem Stale-Konflikt kann der
         Nutzer über den Retry-Knopf neu bewerten; dann wird diese Bindung auf
         die frische Vorschau umgehängt und die unten registrierten Klick-
         Handler (Closures über `pv`) sehen automatisch den neuen Stand. */
      let pv = LLMMutations.preview({ name: call.name, args: call.args }, cur, validCols);
      if (!pv.ok) {
        row.dataset.state = 'error';
        const hint = pv.code === 'card-stale'      ? I18n.t('llm.mutation.stale-card')
                   : pv.code === 'forbidden-field' ? I18n.t('llm.mutation.forbidden-field', pv.hint)
                   : pv.code === 'unknown-column'  ? I18n.t('llm.mutation.unknown-column', pv.hint)
                   : pv.code === 'too-large'       ? I18n.t('llm.mutation.too-large', pv.hint)
                   : pv.code === 'blocked'         ? I18n.t('llm.mutation.blocked-by-client', pv.hint)
                   /* F-645: eigener Text statt Default-Zweig. Der Hinweis kam zwar
                      auch vorher an (pv.hint nennt Feld und erlaubte Werte), aber
                      unter der Ueberschrift "Ungueltige Argumente" — und die Args
                      waren strukturell in Ordnung, nur der Wert nicht. */
                   : pv.code === 'invalid-value'   ? I18n.t('llm.mutation.invalid-value', pv.hint)
                   :                                  I18n.t('llm.mutation.invalid-args', pv.hint);
        row.appendChild(el('div', { cls: 'llm-mut-err', role: 'alert', text: hint }));
        list.appendChild(row);
        openCount--;
        discardedCount++;
        return;
      }

      row.appendChild(el('div', { cls: 'llm-mut-summ', text: pv.summary }));
      const acts = el('div', { cls: 'llm-mut-acts' });
      const applyBtn   = el('button', { cls: 'llm-mut-apply',   type: 'button', text: I18n.t('llm.mutation.apply') });
      const discardBtn = el('button', { cls: 'llm-mut-discard', type: 'button', text: I18n.t('llm.mutation.discard') });
      acts.appendChild(discardBtn);
      acts.appendChild(applyBtn);
      row.appendChild(acts);
      list.appendChild(row);

      const _finalize = (state) => {
        row.dataset.state = state;
        applyBtn.disabled = true;
        discardBtn.disabled = true;
        openCount--;
        if (state === 'applied') appliedCount++; else discardedCount++;
        _updateMutDialogStatus(panel, appliedCount, discardedCount);
        if (openCount === 0) closeBtn.focus();
      };

      applyBtn.addEventListener('click', () => {
        const res = _applyMutation(call, pv, author, meta);
        /* F-411: drei Ausgänge statt zwei. `true` = angewendet, `false` =
           Karte weg, Objekt mit `stale:true` = die Ausgangslage hat sich
           geändert. Der dritte Fall ist KEIN Fehler und darf nicht so
           aussehen: der Vorschlag war gültig, die Welt hat sich gedreht. */
        if (res && res.stale) {
          /* F-622(b): Kam der neue Wert von einem VORHER in diesem selben
             Dialog angewendeten Vorschlag (zwei Vorschläge auf dasselbe
             Feld), ist "Jemand anderes war schneller" falsch — es war
             niemand Fremdes. Eigener Text für den Fall. */
          const zs = _zielSchluessel(call);
          const eigen = zs !== null && eigeneAenderungen.get(zs) === res.basisJetzt;
          const staleText = eigen
            ? I18n.t('llm.mutation.stale-basis-self', _fmtBasis(res.basisVorher), _fmtBasis(res.basisJetzt))
            : I18n.t('llm.mutation.stale-basis',      _fmtBasis(res.basisVorher), _fmtBasis(res.basisJetzt));
          /* F-621: Auch der dritte Ausgang gehört ins Aktivitäts-Log — ADR-0096
             §3 verlangt, dass verworfene Vorschläge vermerkt werden, und diese
             Zeile zählt ihn ohnehin als verworfen. */
          _recordDiscard(call, pv, author, meta, staleText, eigen ? 'stale-basis-self' : 'stale-basis');
          _finalize('discarded');
          /* F-646: role=alert — die Meldung erscheint NACH dem Klick, also
             moeglicherweise weit weg vom Fokus. Ohne Live-Region liest ein
             Screenreader sie gar nicht vor, und der Nutzer haelt den Klick
             fuer wirkungslos. */
          row.appendChild(el('div', { cls: 'llm-mut-stale', role: 'alert', text: staleText }));
          /* F-622(b): Rückweg. Die alte Meldung sagte "bitte neu bewerten",
             ohne dafür einen Weg anzubieten — die Zeile war endgültig
             deaktiviert. Der Knopf holt den aktuellen Karten-Stand und
             öffnet die Zeile wieder, statt den Nutzer auf "Dialog schliessen,
             KI-Tab neu aufrufen" zu verweisen. */
          const retryBtn = el('button', { cls: 'llm-mut-retry', type: 'button', text: I18n.t('llm.mutation.retry') });
          row.appendChild(retryBtn);
          /* F-1106: `_finalize()` hat den Fokus oben schon auf "Schließen" gelegt,
             falls dies der letzte offene Vorschlag war — der Retry-Knopf gab es da
             noch nicht. Jetzt existiert er: Tastatur-/Screenreader-Nutzer sollen
             direkt auf ihn treffen statt auf "Schließen" vorbeizulaufen. */
          if (openCount === 0) retryBtn.focus();
          retryBtn.addEventListener('click', () => {
            row.querySelectorAll('.llm-mut-stale, .llm-mut-retry').forEach(n => n.remove());
            const freshCard = _currentCardFromState();
            const freshCols = ((State.get()?.board?.columnDefs) || []).map(cd => cd.id).filter(Boolean);
            const pv2 = freshCard
              ? LLMMutations.preview({ name: call.name, args: call.args }, freshCard, freshCols)
              : { ok: false, code: 'card-stale', hint: '' };
            if (!pv2.ok) {
              /* Karte inzwischen ganz verschwunden — kein Zurueck mehr,
                 bleibt beim bisherigen 'discarded'-Zustand, nur mit
                 erklärendem Hinweis statt der Retry-Option. */
              row.appendChild(el('div', { cls: 'llm-mut-err', role: 'alert', text: I18n.t('llm.mutation.stale-card') }));
              /* Der Retry-Knopf hat sich entfernt; ohne Ziel fiele der Fokus
                 auf body (Verify-Lauf 3, F-1118). */
              closeBtn.focus();
              return;
            }
            pv = pv2; /* Closures von applyBtn/discardBtn sehen die neue Vorschau. */
            row.dataset.state = 'pending';
            const summ = row.querySelector('.llm-mut-summ');
            if (summ) summ.textContent = pv2.summary;
            applyBtn.disabled = false;
            discardBtn.disabled = false;
            /* F-1113 (Verify-Lauf 2, V2-5): `retryBtn` entfernt sich hier oben
               (`row.querySelectorAll('.llm-mut-stale, .llm-mut-retry')...remove()`)
               selbst aus dem DOM — war er der fokussierte Knopf (Klick oder, im
               F-1106-Fall, automatisch fokussiert), fällt der Fokus sonst auf
               `body`. Der wieder-aktivierte Apply-Knopf ist der sinnvolle
               nächste Tab-Stopp. */
            applyBtn.focus();
            openCount += 1;
            discardedCount -= 1;
            _updateMutDialogStatus(panel, appliedCount, discardedCount);
          });
          return;
        }
        _finalize(res === true ? 'applied' : 'error');
        if (res === true) {
          /* F-622(b): Diesen Vorschlag für spätere Stale-Vergleiche vermerken
             — Basis für die Eigen-/Fremd-Unterscheidung oben. */
          const zs = _zielSchluessel(call);
          if (zs !== null) eigeneAenderungen.set(zs, _neuerWertSchluessel(pv));
        } else {
          /* Karte zwischenzeitlich verschwunden → fail-Hinweis nachschieben. */
          row.appendChild(el('div', { cls: 'llm-mut-err', role: 'alert', text: I18n.t('llm.mutation.stale-card') }));
        }
      });
      discardBtn.addEventListener('click', () => {
        _recordDiscard(call, pv, author, meta);
        _finalize('discarded');
      });
    });

    dialog.appendChild(list);

    const footer = el('div', { cls: 'llm-mut-f' });
    const closeBtn = el('button', { cls: 'btn', type: 'button', text: I18n.t('llm.mutation.close') });

    /* F-415: Der Dialog liess sich bis Sprint 55 NUR über den Schliessen-Knopf
       verlassen — kein Escape, kein Klick auf den Hintergrund. Beides ist bei
       einem modalen Overlay erwartetes Verhalten; fehlt es, wirkt der Dialog
       wie eingefroren. Muster übernommen aus dem PII-Modal (F-395/F-397,
       src/llm-pii.js:659-751): ein `_done`-Flag gegen Doppelausführung, und
       der keydown-Listener wird abgebaut, BEVOR das Overlay verschwindet —
       sonst bleibt er am document hängen und fängt später fremde
       Escape-Tastendrücke ab.

       Schliessen verwirft NICHTS: Bereits angewendete Mutationen bleiben,
       offene Vorschläge bleiben offen. Der Dialog ist eine Ansicht, kein
       Formular mit Abbrechen-Semantik. */
    const _fokusVorher = captureFocus();
    let _geschlossen = false;
    function _schliessen() {
      if (_geschlossen) return;
      _geschlossen = true;
      window.removeEventListener('keydown', _aufTaste, true);
      overlay.remove();
      restoreFocus(_fokusVorher);
      _updateMutDialogStatus(panel, appliedCount, discardedCount, true);
    }
    function _aufTaste(ev) {
      /* Selbst-Abmeldung, falls das Overlay auf einem anderen Weg aus dem
         Dokument verschwunden ist als über `_schliessen()` — etwa weil das
         umgebende Karten-Modal seinen Inhalt abgeräumt hat. Ohne diese Zeile
         bleibt der Listener an `window` hängen und fängt fremde
         Escape-Tastendrücke ab; seit er in der Capture-Phase liegt und
         stopImmediatePropagation ruft, würde er sie sogar VERSCHLUCKEN und
         damit den nächsten Dialog blockieren. Genau das ist beim Bauen dieser
         Tests aufgetreten: ein Dialog aus einem früheren Testfall fing das
         Escape des nächsten ab.

         Der Rückweg ist bewusst hier und nicht in `_schliessen()`: Ein
         Zustands-Flag ohne Rückweg ist die Fehlerklasse, die dieses Projekt
         schon mehrfach getroffen hat. */
      if (!overlay.isConnected) {
        window.removeEventListener('keydown', _aufTaste, true);
        return;
      }
      if (ev.key === 'Tab') {
        /* Tab bleibt im Dialog — sonst wandert der Fokus hinter das Overlay
           in das darunter liegende Karten-Modal (F-622). */
        trapTabKey(ev, dialog);
        return;
      }
      if (ev.key !== 'Escape') return;
      /* F-609: Der Dialog lebt INNERHALB des Karten-Modals, und das hört
         seinerseits auf Escape an `document` (src/modal.js). Ohne
         stopImmediatePropagation schliesst ein Tastendruck beide — und die
         noch offenen Vorschläge sind weg, weil der Stream beendet ist und die
         Liste nur im Dialog existiert. Das widerspräche der Zusage direkt
         darüber, dass Schliessen nichts verwirft.
         `stopImmediatePropagation` statt `stopPropagation`, weil beide
         Listener am SELBEN Ziel (document) hängen: die schwächere Variante
         stoppt nur die weitere Ausbreitung, nicht die Geschwister. */
      ev.stopImmediatePropagation();
      ev.preventDefault();
      _schliessen();
    }

    closeBtn.addEventListener('click', _schliessen);
    footer.appendChild(closeBtn);
    dialog.appendChild(footer);

    overlay.appendChild(dialog);
    /* Backdrop-Klick: nur der Overlay SELBST schliesst, nicht ein
       durchgereichter Klick aus dem Dialog-Inhalt. */
    overlay.addEventListener('click', (ev) => {
      if (ev.target === overlay) _schliessen();
    });
    /* CAPTURE-Phase (drittes Argument `true`) — das ist der Kern von F-609 und
       nicht optional: Beide Listener hängen an `document`, und in der
       Bubble-Phase entscheidet die Registrierungs-Reihenfolge. src/modal.js hat
       LÄNGST registriert, als dieser Dialog aufgeht; ein Bubble-Listener liefe
       also NACH ihm, und stopImmediatePropagation käme zu spät — das Modal wäre
       schon zu. In der Capture-Phase läuft dieser Listener vor allen
       Bubble-Listenern, unabhängig davon, wer sich zuerst registriert hat.
       Beim Entfernen muss `true` mitgegeben werden, sonst findet
       removeEventListener den Listener nicht.

       An `window` und nicht an `document`: Die Capture-Phase beginnt bei
       window und läuft von dort abwärts zum Ziel. Ein Capture-Listener an
       `document` feuert deshalb nicht, wenn das Ereignis auf `document` selbst
       ausgelöst wird — dann ist document das Ziel und nicht sein eigener
       Vorfahre (AT_TARGET). Genau so senden die Tests, und genau daran ist die
       erste Fassung dieses Fixes gescheitert. An window greift beides: das
       ausgelöste Ereignis im Test und der echte Tastendruck im Browser, der
       vom fokussierten Element ausgeht. */
    window.addEventListener('keydown', _aufTaste, true);
    document.body.appendChild(overlay);
    focusInitial(dialog, closeBtn);
    /* Initial-Status falls alle Vorschläge bereits blocked/error sind. */
    _updateMutDialogStatus(panel, appliedCount, discardedCount);
  }

  function _updateMutDialogStatus(panel, appliedN, discardedN, final) {
    const status = panel?.querySelector('#llm-status');
    if (!status) return;
    const parts = [];
    if (appliedN > 0)   parts.push(I18n.t('llm.mutation.applied-status',   appliedN));
    if (discardedN > 0) parts.push(I18n.t('llm.mutation.discarded-status', discardedN));
    if (parts.length) status.textContent = parts.join(' ');
  }

  /* F-411: `basis` ist JSON-kodiert (damit false/0/'' unterscheidbar bleiben).
     Für die Anzeige wieder auspacken — der Nutzer soll den Wert sehen, nicht
     seine Kodierung. Bei kaputtem JSON lieber die Rohform zeigen als gar
     nichts: die Meldung ist der einzige Hinweis auf den Konflikt. */
  function _fmtBasis(roh) {
    try {
      const v = JSON.parse(roh);
      if (v === null || v === '') return '–';
      return String(v);
    } catch { return String(roh); }
  }

  /* Frische Karten-Referenz aus State (Lost-Update-Guard: Karte kann
     inzwischen verschoben/gelöscht worden sein, _currentCard ist nur die
     Modal-Open-Zeit-Snapshot). */
  function _currentCardFromState() {
    const board = State.get()?.board;
    if (!board || !_currentCard) return null;
    for (const sprint of (board.sprints || [])) {
      const c = (sprint.cards || []).find(x => x.id === _currentCard.id);
      if (c) return c;
    }
    return null;
  }

  /**
   * Apply einer einzelnen Mutation: mutiert die Karte im State + hängt
   * llm-mutation-applied-Activity-Entry an. State.set() triggert den
   * Standard-Persist-Pfad (state:changed → debouncedPersist →
   * Sync.persistGated → diffToOps → Inbox).
   */
  function _applyMutation(call, pv, author, meta) {
    const stateClone = JSON.parse(JSON.stringify(State.get()));
    const board = stateClone?.board;
    if (!board || !_currentCard) return false;
    let card = null;
    for (const sprint of (board.sprints || [])) {
      const c = (sprint.cards || []).find(x => x.id === _currentCard.id);
      if (c) { card = c; break; }
    }
    if (!card) return false;

    /* F-411: Der Lost-Update-Guard griff bisher nur beim ÖFFNEN des Dialogs.
       `pv` stammt aus einem preview()-Aufruf gegen den damaligen Karten-Stand;
       zwischen Öffnen und Klick können Minuten liegen, in denen jemand anderes
       dasselbe Feld geändert hat. `pv.apply(card)` schrieb bis Sprint 55 blind
       auf den frischen Klon — der fremde Wert war lautlos weg.

       Also hier neu auswerten und die Ausgangslage vergleichen. `basis` ist
       der Wert, gegen den der Vorschlag berechnet wurde; `null` bedeutet
       „kein Lost-Update möglich" (Append-Operationen sind kommutativ). */
    /* Die Spaltenliste MUSS mitgegeben werden. Ohne sie ist `_cols` in
       preview() null, und der F-410-Check `if (_cols && !_cols.has(toColId))`
       fällt ersatzlos aus — die Zweit-Auswertung wäre schwächer als die erste
       (F-608). Fehlschlag ohne diese Zeile: Ein `move`-Vorschlag nach
       col-review steht im Dialog, jemand löscht die Spalte, der Klick setzt
       columnId trotzdem — die Karte verschwindet aus allen gerenderten
       Spalten. Frisch aus dem State gelesen, nicht die beim Öffnen ermittelte
       Liste: die Spalte kann ja gerade erst verschwunden sein. */
    const colsJetzt = ((State.get()?.board?.columnDefs) || []).map(cd => cd.id).filter(Boolean);
    const pvJetzt = LLMMutations.preview(call, card, colsJetzt);
    if (!pvJetzt || pvJetzt.ok !== true) return false;
    if (pv.basis != null && pvJetzt.basis !== pv.basis) {
      /* NICHT anwenden. Der Vorschlag ist nicht falsch, aber er beruht auf
         einer Lage, die es nicht mehr gibt — das zu entscheiden ist Sache
         des Menschen, nicht dieses Codes. */
      return { stale: true, basisVorher: pv.basis, basisJetzt: pvJetzt.basis };
    }
    /* Ab hier den FRISCH berechneten Vorschlag anwenden, nicht den alten:
       Beide beruhen jetzt nachweislich auf derselben Basis, aber pvJetzt hat
       auch die übrigen Prüfungen (Spalten-Existenz, Wert-Validierung) gegen
       den aktuellen Stand durchlaufen. */
    if (!pvJetzt.apply(card)) return false;
    if (!Array.isArray(card.activity)) card.activity = [];
    card.activity.push({
      id:        _newUUID(),
      author,
      action:    'llm-mutation-applied',
      timestamp: new Date().toISOString(),
      tool:      call.name,
      note:      pv.summary,
      meta: {
        conversationId: meta.conversationId,
        turnId:         meta.turnId,
        toolCallId:     call.id || null,
        provider:       meta.provider,
        model:          meta.model,
      },
    });
    State.set(stateClone);
    return true;
  }

  /**
   * Discard-Audit: knapper Activity-Eintrag ohne args-Spam — Tool-Name +
   * Summary + Meta reichen für den Audit-Pfad (ADR-0096 §3 „verworfene
   * Vorschläge im Conversation-Verlauf vermerkt"). KEIN Apply auf der Karte.
   */
  function _recordDiscard(call, pv, author, meta, grund, weil) {
    /* Karte #674 (A5): Im Firmenmodus kein Eintrag. Die Zusammenfassung ist
       Inhalt der KI-Antwort, und die Person hat ihn gerade NICHT übernommen —
       er gehört nicht in den Verlauf der Karte, den alle am Board sehen.
       Übernommene Vorschläge (_applyMutation) bleiben protokolliert: das ist
       eine bewusste Änderung der Person.
       K2-4: Massgeblich ist der Firmenmodus des Laufs (meta.firmenmodus aus
       dem done-Ereignis), nicht der Schlüssel-Cache — siehe _laufImFirmenmodus. */
    if (!meta || meta.firmenmodus !== false) return;
    const stateClone = JSON.parse(JSON.stringify(State.get()));
    const board = stateClone?.board;
    if (!board || !_currentCard) return;
    let card = null;
    for (const sprint of (board.sprints || [])) {
      const c = (sprint.cards || []).find(x => x.id === _currentCard.id);
      if (c) { card = c; break; }
    }
    if (!card) return;
    if (!Array.isArray(card.activity)) card.activity = [];
    card.activity.push({
      id:        _newUUID(),
      author,
      action:    'llm-mutation-discarded',
      timestamp: new Date().toISOString(),
      tool:      call.name,
      /* F-621: Beim Stale-Ausgang wird der Grund mitgeschrieben. Ohne ihn steht
         im Verlauf nur „verworfen" — und niemand kann später unterscheiden, ob
         ein Mensch abgelehnt hat oder ob die Ausgangslage weggelaufen ist. Der
         Stale-Fall ist genau der, in dem man es wissen will. */
      note:      grund ? `${pv.summary || ''} — ${grund}` : (pv.summary || ''),
      meta: {
        conversationId: meta.conversationId,
        turnId:         meta.turnId,
        toolCallId:     call.id || null,
        provider:       meta.provider,
        model:          meta.model,
        /* F-622(b): `weil` unterscheidet Eigen- von Fremd-Konflikt (siehe
           `_zielSchluessel`/`eigeneAenderungen` in `_openMutationDialog`);
           Default 'stale-basis' erhält das Verhalten vor F-622(b), wenn nur
           `grund` ohne `weil` übergeben wird. */
        ...(grund ? { verworfenWeil: weil || 'stale-basis' } : {}),
      },
    });
    State.set(stateClone);
  }

  function _newUUID() {
    const g = (typeof globalThis !== 'undefined') ? globalThis : {};
    if (g.crypto && typeof g.crypto.randomUUID === 'function') return g.crypto.randomUUID();
    return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, c => {
      const r = Math.floor(Math.random() * 16);
      const v = c === 'x' ? r : (r & 0x3) | 0x8;
      return v.toString(16);
    });
  }

  /* In-Stream-Errors (event:error nach Stream-Start) — strukturierte
     ADR-0096-§1-Fehler. Inhaltlich überlappend mit _errorText, aber andere
     HTTP-Status-Information (gibt's hier nicht). */
  function _streamErrorText(err) {
    const k = err && err.kind;
    if (k === 'too-large') return I18n.t('llm.streaming.too-large');
    if (k === 'too-many-mutations') return I18n.t('llm.mutation.too-many');
    if (k === 'invalid-key') return I18n.t('llm.error-key-invalid');
    if (k === 'unreachable') return I18n.t('llm.error-provider-unreachable');
    /* Karte #665: Fehlerart aus der 4xx-Antwort des Anbieters; die Meldung
       kommt gekürzt vom Server und wird nur als Text gesetzt. */
    if (k === 'context-too-long' || k === 'tools-unsupported' || k === 'provider-rejected') {
      return I18n.t('llm.error-' + k, err.message || '');
    }
    if (k === 'stream-not-supported') return I18n.t('llm.streaming.not-supported');
    /* Karte #529/#528: Der Server bricht einen agentischen Turn seit Sprint 69
       auch MITTEN IM STREAM ab, sobald das Kontext-Budget während der
       Werkzeug-Runden überschritten wird (event:error, kind:'context-budget')
       — bisher kannte diese Funktion `context-budget` nur als HTTP-400 VOR
       Stream-Start (siehe `e.body.kind === 'context-budget'` im catch-Block
       von `_onSend`). Ohne diesen Zweig fiel der Fall in den generischen
       Server-Fehler-Text, obwohl bereits ein Teil der Antwort sichtbar war —
       ein eigener Text, weil "Budget überschritten" allein nicht sagt, dass
       mitten in den Werkzeug-Runden abgebrochen wurde, nicht vor dem Senden. */
    if (k === 'context-budget') return I18n.t('llm.streaming.budget-exceeded-mid-stream');
    return I18n.t('llm.error-server');
  }

  function _onCancel(panel) {
    if (_activeStreamAbort) {
      try { _activeStreamAbort.abort(); } catch {}
    }
    /* UI wird im streamLLM-catch-Block aufgeräumt (AbortError-Pfad). */
  }

  /* ── Conversation-Sidebar (#300 Etappe 3, ADR-0096 §2) ──────────── */

  async function _loadConversations(panel) {
    if (!_currentCard || !_currentBoardId) return;
    const gen = _renderGen;
    let res, body;
    try {
      res = await holen(`/boards/${encodeURIComponent(_currentBoardId)}/cards/${encodeURIComponent(_currentCard.id)}/conversations`);
      try { body = await res.json(); } catch { body = null; }
    } catch {
      /* Auch der Fehlerpfad schreibt — eine leere Leiste fuer die alte Karte
         ueber der neuen ist derselbe Fehler wie eine gefuellte. */
      if (gen !== _renderGen) return;
      _conversations = [];
      _renderConvBar(panel);
      return;
    }
    if (gen !== _renderGen) return;
    _conversations = (res.ok && Array.isArray(body)) ? body : [];
    _renderConvBar(panel);
  }

  function _renderConvBar(panel) {
    const bar = panel.querySelector('#llm-conv-bar');
    if (!bar) return;
    bar.innerHTML = '';
    const label = el('span', { cls: 'llm-conv-label', text: I18n.t('llm.conversation.label') });
    /* Karte #674 (A5): Im Firmenmodus liefert der Server nur eigene Verläufe.
       Der Hinweis sagt das — und dass die Antwort nicht an die Karte geht,
       also nur im Verlauf dauerhaft bleibt. */
    if (_istFirmenmodus()) label.title = I18n.t('llm.firmenmodus.verlaeufe-eigene');
    bar.appendChild(label);
    /* „Single-Turn"-Chip (default state, kein conversationId mitschicken) */
    const single = el('button', {
      cls: 'llm-conv-chip' + (_currentConvId === null ? ' llm-conv-chip-active' : ''),
      type: 'button',
      text: I18n.t('llm.conversation.single'),
    });
    single.addEventListener('click', () => _selectConversation(panel, null));
    bar.appendChild(single);

    _conversations.forEach((c) => {
      const chip = el('button', {
        cls: 'llm-conv-chip' + (_currentConvId === c.id ? ' llm-conv-chip-active' : ''),
        type: 'button',
        title: `${c.provider}/${c.model} · ${c.createdAt}`,
      });
      chip.appendChild(document.createTextNode(_convLabel(c)));
      const x = el('button', { cls: 'llm-conv-x', type: 'button', title: I18n.t('llm.conversation.delete'),
                                'aria-label': I18n.t('llm.conversation.delete'), text: '×' });
      x.addEventListener('click', (ev) => {
        ev.stopPropagation();
        _deleteConversation(panel, c.id);
      });
      chip.appendChild(x);
      chip.addEventListener('click', () => _selectConversation(panel, c.id));
      bar.appendChild(chip);
    });

    /* „+ Neue Konversation"-Chip — legt eine neue Conv mit aktuellen Provider/
       Model-Werten an und macht sie aktiv. */
    const newChip = el('button', { cls: 'llm-conv-chip llm-conv-chip-new', type: 'button',
                                    id: 'llm-conv-new', text: I18n.t('llm.conversation.new') });
    newChip.addEventListener('click', () => _newConversation(panel));
    bar.appendChild(newChip);
  }

  function _convLabel(c) {
    /* Kurzes Provider-Kürzel + Zeitstempel: „Anth 13:42" */
    const prov = (c.provider || '').slice(0, 4);
    const ts = (c.createdAt || '').slice(11, 16); // HH:MM aus ISO
    return prov + (ts ? ' ' + ts : '');
  }

  async function _newConversation(panel) {
    if (!_currentCard || !_currentBoardId) return;
    const gen = _renderGen;
    const provider = panel.querySelector('#llm-provider-sel')?.value;
    const model = panel.querySelector('#llm-model-sel')?.value;
    const status = panel.querySelector('#llm-status');
    if (!provider || !model) {
      if (status) status.textContent = I18n.t('llm.conversation.need-provider');
      return;
    }
    let res, body;
    try {
      res = await holen(`/boards/${encodeURIComponent(_currentBoardId)}/cards/${encodeURIComponent(_currentCard.id)}/conversations`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ provider, model }),
      });
      try { body = await res.json(); } catch { body = null; }
    } catch {
      if (gen !== _renderGen) return;
      if (status) status.textContent = I18n.t('llm.error-provider-unreachable');
      return;
    }
    /* Die Unterhaltung ist serverseitig angelegt und bleibt es — sie gehoert
       aber zur Karte, die beim Klick offen war. Sie jetzt noch aktiv zu
       schalten hiesse, die neue Karte auf eine fremde Unterhaltung zu
       stellen; beim naechsten Oeffnen der alten Karte laedt sie regulaer. */
    if (gen !== _renderGen) return;
    if (res.ok && body && body.id) {
      _conversations.unshift(body);
      _currentConvId = body.id;
      _renderConvBar(panel);
      _refreshProviderLockState(panel);
      _hideTurnHistory(panel);
      if (status) status.textContent = '';
    } else {
      if (status) status.textContent = _errorText(res ? res.status : 0, body);
    }
  }

  function _selectConversation(panel, convId) {
    _currentConvId = convId;
    const conv = convId ? _conversations.find((c) => c.id === convId) : null;
    if (conv) {
      /* Provider + Modell auf Conv-Werte setzen (Mismatch wäre Server-400). */
      const provSel = panel.querySelector('#llm-provider-sel');
      const modelSel = panel.querySelector('#llm-model-sel');
      if (provSel && conv.provider) {
        provSel.value = conv.provider;
        _populateModels(modelSel, conv.provider);
      }
      if (modelSel && conv.model) modelSel.value = conv.model;
      _refreshPrivacyBanner(panel);
      _loadAndRenderTurns(panel).catch(() => {});
    } else {
      _hideTurnHistory(panel);
    }
    _renderConvBar(panel);
    _refreshProviderLockState(panel);
    _refreshEstimate(panel);
  }

  async function _deleteConversation(panel, convId) {
    if (!_currentCard || !_currentBoardId || !convId) return;
    if (!confirm(I18n.t('llm.conversation.delete-confirm'))) return;
    /* Marke NACH dem confirm: der Dialog blockiert, ein Kartenwechsel
       waehrend er steht ist nicht moeglich. Vor dem confirm gelesen waere
       sie dieselbe Zahl — nur weniger offensichtlich richtig. */
    const gen = _renderGen;
    const status = panel.querySelector('#llm-status');
    try {
      const res = await holen(`/boards/${encodeURIComponent(_currentBoardId)}/cards/${encodeURIComponent(_currentCard.id)}/conversations/${encodeURIComponent(convId)}`, {
        method: 'DELETE',
      });
      if (!res.ok) {
        let body = null; try { body = await res.json(); } catch {}
        if (gen !== _renderGen) return;
        if (status) status.textContent = _errorText(res.status, body);
        return;
      }
    } catch {
      if (gen !== _renderGen) return;
      if (status) status.textContent = I18n.t('llm.error-provider-unreachable');
      return;
    }
    /* Geloescht ist geloescht — aber `_conversations` gehoert inzwischen
       womoeglich einer anderen Karte, und dort ist diese Id nie gewesen. */
    if (gen !== _renderGen) return;
    _conversations = _conversations.filter((c) => c.id !== convId);
    if (_currentConvId === convId) {
      _currentConvId = null;
      _hideTurnHistory(panel);
      _refreshProviderLockState(panel);
    }
    _renderConvBar(panel);
    if (status) status.textContent = '';
  }

  async function _loadAndRenderTurns(panel) {
    if (!_currentCard || !_currentBoardId || !_currentConvId) return;
    const gen = _renderGen;
    let res, body;
    try {
      res = await holen(`/boards/${encodeURIComponent(_currentBoardId)}/cards/${encodeURIComponent(_currentCard.id)}/conversations/${encodeURIComponent(_currentConvId)}/turns`);
      try { body = await res.json(); } catch { body = null; }
    } catch {
      if (gen !== _renderGen) return;
      _hideTurnHistory(panel);
      return;
    }
    if (gen !== _renderGen) return;
    if (!res.ok || !Array.isArray(body)) {
      _hideTurnHistory(panel);
      return;
    }
    _renderTurnHistory(panel, body);
  }

  function _renderTurnHistory(panel, turns) {
    const h = panel.querySelector('#llm-turn-history');
    if (!h) return;
    h.innerHTML = '';
    if (!turns || turns.length === 0) {
      h.style.display = 'none';
      return;
    }
    turns.forEach((t) => {
      const row = el('div', { cls: 'llm-turn ' + (t.role === 'user' ? 'llm-turn-u' : 'llm-turn-a') });
      row.appendChild(el('div', { cls: 'llm-turn-r', text: t.role === 'user'
        ? I18n.t('llm.conversation.role-user') : I18n.t('llm.conversation.role-assistant') }));
      row.appendChild(Markdown.render(t.content || ''));
      /* F-1554: Auch ältere Antworten lassen sich im Firmenmodus als Kommentar
         übernehmen (nicht nur die frische Stream-Antwort). Ohne Firmenmodus
         steht die Antwort ohnehin als Kommentar an der Karte. */
      if (t.role !== 'user' && t.content && _istFirmenmodus()) {
        const conv = _conversations.find((c) => c.id === _currentConvId) || {};
        const btn = el('button', { cls: 'btn llm-adopt-btn llm-adopt-turn', type: 'button',
                                   text: I18n.t('llm.preview.uebernehmen') });
        btn.addEventListener('click', () => _onUebernehmenTurn(panel, t.content, conv, btn));
        /* KA-5: Der Merker überlebt das Neuzeichnen, weil er am Kommentar der
           Karte hängt, nicht am Knopf. */
        if (_currentCard && _istSchonUebernommen(_currentCard.id, t.content)) {
          btn.disabled = true;
          btn.textContent = I18n.t('llm.preview.uebernommen');
        }
        row.appendChild(btn);
      }
      h.appendChild(row);
    });
    /* #428: kumulierte Kosten der Unterhaltung. Relevant geworden ist das mit
       dem Nachladen — ein Turn kann jetzt mehrere Provider-Aufrufe kosten,
       und die Vorab-Schätzung kann das nicht vorhersagen. */
    const summe = _summiereTurnKosten(turns);
    if (summe.anfragen > 0) {
      h.appendChild(el('div', { cls: 'llm-turn-sum',
        text: I18n.t('llm.conversation.cost-total', summe.anfragen, summe.tokens, summe.kosten) }));
    }
    h.style.display = '';
  }

  function _hideTurnHistory(panel) {
    const h = panel.querySelector('#llm-turn-history');
    if (h) { h.innerHTML = ''; h.style.display = 'none'; }
  }

  function _refreshProviderLockState(panel) {
    /* Bei aktiver Conv: Provider+Modell-Dropdowns sperren (Mismatch = Server-400). */
    const lock = _currentConvId !== null;
    const provSel = panel.querySelector('#llm-provider-sel');
    const modelSel = panel.querySelector('#llm-model-sel');
    if (provSel) provSel.disabled = lock || !provSel.dataset.populated;
    if (modelSel) modelSel.disabled = lock;
  }

  function _errorText(status, body) {
    if (status === 423 && body && body.holder) return I18n.t('llm.error-locked', body.holder);
    if (status === 412) return I18n.t('llm.error-no-card-key');
    if (status === 422 && body && body.kind === 'invalid-key') return I18n.t('llm.error-key-invalid');
    if (status === 503) return I18n.t('llm.error-503');
    if (status === 403) return I18n.t('llm.error-acl');
    if (status === 404) return I18n.t('llm.error-card-not-found');
    if (status === 502) return I18n.t('llm.error-provider-unreachable');
    /* #429: Die beiden Ablehnungen rund um Bild-Anhänge kommen als 400 mit
       eigenem `kind` und einem konkreten Grund. Ohne diese Zweige fielen sie
       in „Ungültige Anfrage" — der Nutzer erführe nicht, warum sein Bild
       nicht ankam, und ADR-0121 §4 sagt „sauber scheitern" zu. Sauber
       scheitern heisst auch: verständlich scheitern.
       Der Grund kommt vom Server (`message`) und ist bewusst durchgereicht:
       er nennt Dateiname und Zahlen, die das Wörterbuch nicht kennen kann. */
    if (status === 400 && body && body.kind === 'vision-not-supported') {
      return I18n.t('llm.error-vision-not-supported', body.message || '');
    }
    if (status === 400 && body && body.kind === 'attachment-rejected') {
      return I18n.t('llm.error-attachment-rejected', body.message || '');
    }
    /* Karte #665: Fehlerart aus der 4xx-Antwort des Anbieters. */
    if (status === 400 && body && (body.kind === 'context-too-long' || body.kind === 'tools-unsupported' || body.kind === 'provider-rejected')) {
      return I18n.t('llm.error-' + body.kind, body.message || '');
    }
    /* Karte #664: Vorgaben des Betreibers (Firmenmodus / ABOARD_LLM_PII). */
    if (status === 400 && body && body.kind === 'model-not-allowed') return I18n.t('llm.error-model-not-allowed');
    if (status === 400 && body && body.kind === 'pii-pflicht') return I18n.t('llm.error-pii-pflicht');
    if (status === 400) return I18n.t('llm.error-bad-request');
    /* Karte #674 (A6): Obergrenze je Person im Firmenmodus — eigene Meldung
       mit den Zahlen des Servers; das allgemeine error-429 nennt die
       Schlüssel-Grenze (10 Mutationen/Stunde) und wäre hier falsch. */
    if (status === 429 && body && body.kind === 'anfragen-obergrenze') {
      return I18n.t('llm.error-anfragen-obergrenze', body.grenze, Math.max(1, Math.ceil((body.wartenSekunden || 0) / 60)));
    }
    if (status === 429) return I18n.t('llm.error-429');
    return I18n.t('llm.error-server');
  }

  function _showReply(panel, body) {
    const previewBody = panel.querySelector('#llm-preview-body');
    const actions = panel.querySelector('#llm-preview-actions');
    if (!previewBody) return;
    previewBody.innerHTML = '';
    if (body.replyText) {
      previewBody.appendChild(Markdown.render(body.replyText));
    } else {
      previewBody.appendChild(el('p', { cls: 'llm-preview-empty', text: I18n.t('llm.tab.empty-state') }));
    }
    if (actions) actions.style.display = '';
  }

  /* Karte #674: welche Knöpfe der Aktionsleiste sichtbar sind.
     kommentar — der Server hat einen Kommentar geschrieben (Behalten/Verwerfen);
     uebernehmen — Firmenmodus, Antwort liegt vor (Als Kommentar übernehmen). */
  function _zeigeAktionen(panel, kommentar, uebernehmen) {
    const actions = panel.querySelector('#llm-preview-actions');
    if (!actions) return;
    const keep = actions.querySelector('.llm-keep-btn');
    const discard = actions.querySelector('.llm-discard-btn');
    const adopt = actions.querySelector('#llm-adopt-btn');
    if (keep) keep.style.display = kommentar ? '' : 'none';
    if (discard) discard.style.display = kommentar ? '' : 'none';
    if (adopt) {
      adopt.style.display = uebernehmen ? '' : 'none';
      adopt.disabled = !uebernehmen;
      adopt.textContent = I18n.t('llm.preview.uebernehmen');
    }
    actions.style.display = (kommentar || uebernehmen) ? '' : 'none';
  }

  /* Karte #674: Antwort als Kommentar DER ANGEMELDETEN PERSON übernehmen.
     Kennzeichnung als Vorspann im Text, nicht als Zusatzfeld: Das
     Kommentarmodell kennt nur id/author/text/created, und genau diese Felder
     zeigen Kartenansicht, Export, Mail und Benachrichtigung. Ein eigenes Feld
     wäre überall unsichtbar — die Herkunft soll aber jeder sehen, der den
     Kommentar liest. Geschrieben wird wie im Karten-Modal (State.set →
     comment.add-Op → ACL am Server); ein eigener Endpunkt entsteht nicht. */
  /* F-1554: Rolle der Person am aktuellen Board aus der Board-Liste
     (`role` je Eintrag). `/boards/{id}/ich` ist Agenten-Token-Sache, nicht
     für die Browser-Sitzung. Unbekannt (Netzfehler, Legacy-Bypass ohne
     Rollenfeld) → null: dann entscheidet der Server beim Speichern. */
  async function _meineRolle(boardId) {
    try {
      const res = await holen('/boards');
      if (!res.ok) return null;
      const liste = await res.json();
      const b = Array.isArray(liste) ? liste.find((x) => x && x.id === boardId) : null;
      return (b && b.role) || null;
    } catch { return null; }
  }

  /* F-1554: Übernehmen einer älteren Antwort aus der Verlaufsliste. Wer nur
     lesen darf (Herabstufung nach dem Gespräch), bekommt eine verständliche
     Meldung statt eines Kommentars, den der Server später still verwirft. */
  async function _onUebernehmenTurn(panel, text, conv, btn) {
    const status = panel.querySelector('#llm-status');
    if (!text || btn.disabled) return;
    const gen = _renderGen;
    const cardId = _currentCard && _currentCard.id;
    const boardId = _currentBoardId;
    if (!cardId || !boardId) return;
    /* KA-5: dieselbe Antwort steht schon als Kommentar an der Karte (auch über
       den Stream-Knopf) — kein zweiter. */
    if (_istSchonUebernommen(cardId, text)) {
      btn.disabled = true;
      btn.textContent = I18n.t('llm.preview.uebernommen');
      return;
    }
    btn.disabled = true;
    const rolle = await _meineRolle(boardId);
    /* Karte inzwischen gewechselt: nicht auf eine andere Karte schreiben —
       der Knopf gehört zum alten Panel und wird wieder freigegeben. */
    if (gen !== _renderGen || !_currentCard || _currentCard.id !== cardId || _currentBoardId !== boardId) {
      btn.disabled = false;
      return;
    }
    if (rolle === 'viewer') {
      btn.disabled = false;
      if (status) status.textContent = I18n.t('llm.error-acl');
      return;
    }
    const modell = [conv.provider, conv.model].filter(Boolean).join('/');
    const { fehler, commentId } = _kommentarAnKarte(cardId, text, modell);
    if (fehler) {
      btn.disabled = false;
      if (status) status.textContent = fehler;
      return;
    }
    btn.textContent = I18n.t('llm.preview.uebernommen');
    if (status) status.textContent = I18n.t('llm.preview.uebernommen');
    _beobachteAblehnung(boardId, commentId, btn, status);
  }

  /* F-1576: Das Speichern läuft entkoppelt (State.set → Entprellung → Ops).
     Lehnt der Server die comment.add-Op später ab — 403 nach Herabstufung
     oder unbekannter Rolle, 422 bei gelöschter Karte —, meldet die Op-Schicht
     das als 'ops:abgelehnt'. Der Knopf, der den Kommentar angestoßen hat,
     nimmt „übernommen“ dann zurück und nennt den Grund. Beobachtet wird
     höchstens _ABLEHNUNG_FENSTER_MS lang, und nur für die eigene comment.id; danach gilt der Kommentar als
     gespeichert. */
  const _ABLEHNUNG_FENSTER_MS = 15000;
  const _beobachtet = new Set();
  let _ablehnungHoerer = false;

  function _beobachteAblehnung(boardId, commentId, btn, status, zuruecksetzen) {
    if (!_ablehnungHoerer) {
      _ablehnungHoerer = true;
      EventBus.on('ops:abgelehnt', _onOpsAbgelehnt);
    }
    _beobachtet.add({ commentId, boardId, btn, status, zuruecksetzen, bis: Date.now() + _ABLEHNUNG_FENSTER_MS });
  }

  function _onOpsAbgelehnt(e) {
    const jetzt = Date.now();
    for (const b of [..._beobachtet]) {
      if (b.bis < jetzt) { _beobachtet.delete(b); continue; }
      if (b.boardId !== e?.boardId) continue;
      /* Nur die eigene comment.add-Op zählt. Fehlt sie unter den abgelehnten,
         kam der Kommentar an — ein Zurücksetzen erlaubte einen zweiten Klick
         und damit ein Doppel. */
      const betrifft = (e.ops || []).some(o => o.type === 'comment.add' && o.commentId === b.commentId);
      if (!betrifft) continue;
      _beobachtet.delete(b);
      _entferneKommentarLokal(b.commentId);
      if (b.btn) {
        b.btn.disabled = false;
        b.btn.textContent = I18n.t('llm.preview.uebernehmen');
      }
      if (b.zuruecksetzen) b.zuruecksetzen();
      const msg = e.status === 403 ? I18n.t('llm.error-acl') : I18n.t('ops.abgelehnt.sonst', '?', '');
      if (b.status) b.status.textContent = msg;
    }
  }

  /* F-1579: Der abgelehnte Kommentar gehört nicht in den lokalen Stand — sonst
     steht er auf dem Bildschirm, ohne je gespeichert zu sein, und jedes
     weitere Speichern schickte ihn erneut. Entfernt wird nur der Kommentar mit
     genau dieser id. Das Entfernen läuft über State.set und erzeugt beim
     nächsten Speichern höchstens eine comment.delete-Op; die ist am Server
     idempotent und prüft ihr Ziel nicht (src/ops.js, fold.js), kann also
     weder an einer gelöschten Karte noch an fehlendem Kommentar scheitern. Nach
     403 (Betrachter) kennt der Bezug den Kommentar gar nicht, es entsteht
     keine Op. Ein Kommentar frisch übernommener Text hat keine Anhänge, die
     die Kaskade mitnähme. */
  function _entferneKommentarLokal(commentId) {
    if (!commentId) return;
    const klon = JSON.parse(JSON.stringify(State.get()));
    let geaendert = false;
    for (const sprint of (klon?.board?.sprints || [])) {
      for (const c of (sprint.cards || [])) {
        if (!Array.isArray(c.comments)) continue;
        const rest = c.comments.filter(k => k && k.id !== commentId);
        if (rest.length !== c.comments.length) { c.comments = rest; geaendert = true; }
      }
    }
    if (geaendert) State.set(klon);
  }

  /* KA-5: Steht die Antwort schon als übernommener Kommentar an der Karte?
     Verglichen wird der Text (Vorspann + Leerzeile + Antwort, wie ihn
     _kommentarAnKarte bildet) statt eines neuen Kommentarfeldes: Stream-Knopf
     und Verlaufsknopf kennen keine gemeinsame Turn-Kennung, der Text ist
     beiden gemeinsam. Wird der Kommentar gelöscht oder vom Server abgelehnt,
     ist die Antwort wieder übernehmbar. */
  function _istSchonUebernommen(cardId, text) {
    if (!cardId || !text) return false;
    const endung = '\n\n' + text;
    for (const sprint of (State.get()?.board?.sprints || [])) {
      const c = (sprint.cards || []).find(x => x.id === cardId);
      if (c) return (c.comments || []).some(k => k && typeof k.text === 'string' && k.text.endsWith(endung));
    }
    return false;
  }

  /* Kern des Übernehmens: Kommentar der angemeldeten Person an die Karte.
     Rückgabe: { fehler: null, commentId } bei Erfolg, sonst { fehler: Meldungstext }. */
  function _kommentarAnKarte(cardId, text, modell) {
    const stateClone = JSON.parse(JSON.stringify(State.get()));
    const board = stateClone?.board;
    let card = null;
    for (const sprint of (board?.sprints || [])) {
      const c = (sprint.cards || []).find(x => x.id === cardId);
      if (c) { card = c; break; }
    }
    if (!card) return { fehler: I18n.t('llm.error-card-not-found') };
    if (!Array.isArray(card.comments)) card.comments = [];
    const commentId = _newUUID();
    card.comments.push({
      id:      commentId,
      author:  Config.get('userName') || 'User',
      text:    I18n.t('llm.uebernahme.vorspann', modell) + '\n\n' + text,
      created: new Date().toISOString().slice(0, 10),
    });
    State.set(stateClone);
    return { fehler: null, commentId };
  }

  function _onUebernehmen(panel) {
    const btn = panel.querySelector('#llm-adopt-btn');
    const status = panel.querySelector('#llm-status');
    const r = _lastReply;
    /* K2-4: nur für einen Lauf im Firmenmodus, und nur, wenn der Server
       keinen Kommentar geschrieben hat (sonst entstünde ein Doppel). */
    if (!r || r.uebernommen || !r.replyText || !r.firmenmodus || r.commentId) return;
    /* Kartenwechsel seit dem Send: nicht auf eine andere Karte schreiben. */
    if (!_currentCard || r.cardId !== _currentCard.id || r.boardId !== _currentBoardId) return;
    /* KA-5: schon über den Verlaufsknopf übernommen → nicht doppelt. */
    if (_istSchonUebernommen(r.cardId, r.replyText)) {
      r.uebernommen = true;
      if (btn) { btn.disabled = true; btn.textContent = I18n.t('llm.preview.uebernommen'); }
      return;
    }
    /* Sofort sperren — ein zweiter Klick darf keinen zweiten Kommentar
       erzeugen, auch nicht während State.set läuft. */
    r.uebernommen = true;
    if (btn) btn.disabled = true;
    const modell = [r.provider, r.model].filter(Boolean).join('/');
    const { fehler, commentId } = _kommentarAnKarte(r.cardId, r.replyText, modell);
    if (fehler) {
      r.uebernommen = false;
      if (btn) btn.disabled = false;
      if (status) status.textContent = fehler;
      return;
    }
    if (btn) btn.textContent = I18n.t('llm.preview.uebernommen');
    if (status) status.textContent = I18n.t('llm.preview.uebernommen');
    /* F-1576: spätere Ablehnung durch den Server → Sperre wieder lösen. */
    _beobachteAblehnung(r.boardId, commentId, btn, status, () => { r.uebernommen = false; });
  }

  function _onKeep(panel) {
    /* ADR-0095 §3 Default-Pfad: Kommentar bleibt persistiert (server-side
       AddLLMReply hat ihn schon angehängt). Panel-State zurücksetzen. */
    _resetPreview(panel);
  }

  /* ── Pending-Lock-Polling (ADR-0095 §5, Etappe 4) ────────────────── */

  /** Holt den aktuellen Lock-Status für die geöffnete Karte und aktualisiert
   *  Banner + Send-Button-State. Returns die Response für Tests. */
  async function _fetchPendingOnce(panel) {
    if (!_currentCard || !_currentBoardId) return null;
    const gen = _renderGen;
    let res, body;
    try {
      res = await holen(`/boards/${encodeURIComponent(_currentBoardId)}/cards/${encodeURIComponent(_currentCard.id)}/llm-pending`);
      try { body = await res.json(); } catch { body = null; }
    } catch {
      return null; /* Netzwerk-Fehler — Banner bleibt wie er ist (kein Flackern) */
    }
    if (!res.ok || !body) return null;
    /* Der Poll laeuft im Sekundentakt weiter; eine verspaetete Antwort zur
       alten Karte wuerde sonst einen fremden Sperr-Banner ueber die neue
       legen — und mit ihm den Senden-Knopf sperren. Der Rueckgabewert bleibt,
       Aufrufer duerfen die Antwort auswerten, nur anzeigen nicht. */
    if (gen !== _renderGen) return body;
    _renderPendingBanner(panel, body);
    return body;
  }

  function _renderPendingBanner(panel, body) {
    const banner = panel.querySelector('#llm-pending-banner');
    const sendBtn = panel.querySelector('#llm-send-btn');
    if (!banner) return;
    /* Eigene Sends NICHT anzeigen — der Composer hat ohnehin Send-Spinner.
       Banner zeigt nur Fremd-Locks (Multi-User-Sichtbarkeit). */
    if (body.holder && !body.isSelf) {
      banner.textContent = I18n.t('llm.pending.banner', body.holder);
      banner.style.display = '';
      if (sendBtn) sendBtn.disabled = true;
    } else {
      banner.style.display = 'none';
      banner.textContent = '';
      /* Send-Button wieder aktivieren, falls nicht aus anderem Grund deaktiviert
         (z. B. keine Keys). Den keine-Keys-Pfad erkennen wir am leeren provSel. */
      const provSel = panel.querySelector('#llm-provider-sel');
      const hasProviders = provSel && provSel.options.length > 0 && provSel.value;
      if (sendBtn && hasProviders) sendBtn.disabled = false;
    }
  }

  function _startPendingPoll(panel) {
    _stopPendingPoll();
    _fetchPendingOnce(panel); /* sofort einmal beim Tab-Aktivieren */
    _pendingPollTimer = setInterval(() => _fetchPendingOnce(panel), PENDING_POLL_INTERVAL_MS);
  }

  function _stopPendingPoll() {
    if (_pendingPollTimer !== null) {
      clearInterval(_pendingPollTimer);
      _pendingPollTimer = null;
    }
  }

  async function _onDiscard(panel) {
    /* ADR-0095 §3, Etappe 4: POST /boards/:b/cards/:c/llm-discard
       { commentId, provider, model } → Server entfernt Kommentar + hängt
       'llm-response-discarded'-Activity-Entry an (Audit bleibt erhalten). */
    if (!_lastReply) return;
    /* Kritiker-Lauf 2026-09-17 (P2): Die URL adressiert korrekt über
       `_lastReply`, aber `panel` ist der modulweite Knoten. Wechselt die Karte
       während des Requests, leerte `_resetPreview(panel)` die Vorschau der
       NEUEN Karte — ein Verwerfen, das der Nutzer nie angefordert hat. */
    const gen = _renderGen;
    if (!confirm(I18n.t('llm.preview.discard-confirm'))) return;

    const status = panel.querySelector('#llm-status');
    const provSel = panel.querySelector('#llm-provider-sel');
    const modelSel = panel.querySelector('#llm-model-sel');
    const provider = provSel?.value || '';
    const model = modelSel?.value || '';

    try {
      const res = await holen(
        `/boards/${encodeURIComponent(_lastReply.boardId)}/cards/${encodeURIComponent(_lastReply.cardId)}/llm-discard`,
        {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ commentId: _lastReply.commentId, provider, model }),
        }
      );
      if (res.ok) {
        if (gen !== _renderGen) return;
        if (status) status.textContent = '';
        _resetPreview(panel);
      } else {
        let body = null;
        try { body = await res.json(); } catch {}
        if (gen !== _renderGen) return;
        if (status) status.textContent = _errorText(res.status, body);
      }
    } catch {
      if (gen !== _renderGen) return;
      if (status) status.textContent = I18n.t('llm.error-provider-unreachable');
    }
  }

  LLMTab = {
    /* Public API */
    build, render, hasAnyKey, invalidateKeysCache, estimateTokens, stopPolling,
    /* Test-/Debug-Hooks (nicht für Produktivpfade nutzen) */
    _buildContextPreview, _errorText, _streamErrorText, _populateModels, _inputSatz,
    _resetCachesForTest() { _keysCache = null; _pricingCache = null; },
    /* Karte #570 (K-B1/K-B2): Anbieter-Beschriftung und Modellliste bei
       umgestelltem „openai“. */
    _setKeysCacheForTest(c) { _keysCache = c; },
    _anbieterLabelForTest(p) { return _anbieterLabel(p); },
    _modelleFuerForTest(p) { return _modelleFuer(p); },
    _setStateForTest(card, boardId) { _currentCard = card; _currentBoardId = boardId; },
    _setLastReplyForTest(r) { _lastReply = r; },
    _getLastReplyForTest() { return _lastReply; },
    _fetchPendingOnceForTest(panel) { return _fetchPendingOnce(panel); },
    /* Kritiker-Lauf 2026-09-17: auch die beiden Sende-/Verwerfen-Pfade sind
       von aussen ausloesbar, sonst bleibt ihr Gen-Schutz ungetestet. */
    _onDiscardForTest(panel) { return _onDiscard(panel); },
    _onUebernehmenForTest(panel) { return _onUebernehmen(panel); },
    _onUebernehmenTurnForTest(panel, text, conv, btn) { return _onUebernehmenTurn(panel, text, conv, btn); },
    /* F-861: dieselben Hooks fuer die vier uebrigen Funktionen, die nach
       einem `await` schreiben — ohne sie ist der Wettlauf von aussen nicht
       deterministisch ausloesbar. */
    _loadConversationsForTest(panel) { return _loadConversations(panel); },
    _newConversationForTest(panel) { return _newConversation(panel); },
    _deleteConversationForTest(panel, convId) { return _deleteConversation(panel, convId); },
    _loadAndRenderTurnsForTest(panel) { return _loadAndRenderTurns(panel); },
    _setConversationsForTest(convs, currentId) {
      _conversations = convs;
      if (currentId !== undefined) _currentConvId = currentId;
    },
    _getConversationsForTest() { return _conversations; },
    _renderPendingBannerForTest(panel, body) { _renderPendingBanner(panel, body); },
    /* #300 Etappe 5: Mutations-Confirm-Dialog (für UI-Tests) */
    _openMutationDialogForTest(panel, calls, doneMeta) { return _openMutationDialog(panel, calls, doneMeta || {}); },
    _pendingMutationsForTest() { return _pendingMutations; },
    /* #428 (ADR-0120): Abruf-Anzeige + Kostensumme (für UI-Tests) */
    _summiereTurnKosten,
    /* #429 (ADR-0121): Bild-Anhänge (für UI-Tests) */
    _bildTokenSchaetzung, _bildAnhaenge, _collectAnhaenge,
    _renderAnhangAuswahlForTest(panel, card) { return _renderAnhangAuswahl(panel, card); },
    _refreshFetchHinweisForTest(panel) { _refreshFetchHinweis(panel); },
    _refreshEstimateForTest(panel) { return _refreshEstimate(panel); },
    _bumpRenderGenForTest() { _renderGen += 1; },
    _refreshAnhangZeilenForTest(panel) { return _refreshAnhangZeilen(panel); },
    _renderTurnHistoryForTest(panel, turns) { _renderTurnHistory(panel, turns); },
    MODELS, PROVIDER_LABEL,
  };
}

export { LLMTab };
