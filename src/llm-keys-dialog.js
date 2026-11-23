/* llm-keys-dialog.js — KI-Anbieter-Verwaltung (#298, ADR-0094).
 *
 * Per-User-Modal-Dialog für die Verwaltung der LLM-API-Keys eines Users.
 * Sichtbar nur im Server-Modus + IS_SERVER_BUILD. Im Slim/Claude-Code-Build
 * per DCE eliminiert.
 *
 * Pro Provider eine Zeile mit Status, Input und Aktionen — Speichern führt
 * einen Pre-Save-Test gegen den Provider durch (Server-side), zeigt bei
 * 422 "Key ungültig" und persistiert nur bei OK.
 */

import { I18n } from './i18n.js';
import { LLMKeysClient } from './llm-keys-client.js';
/* #299/ADR-0095: KI-Tab cached den Provider-Key-Status pro Session.
   Cache muss nach Mutationen (set/delete) invalidiert werden, sonst zeigt
   das Karten-Modal den KI-Tab nach dem Hinterlegen des ersten Keys nicht
   sofort an. */
import { LLMTab } from './llm-tab.js';
/* #348/ADR-0118: Overlay/Panel/Header/Body + ESC/Klick-außerhalb/Focus-Trap
   kommen jetzt aus der gemeinsamen Modal-Shell — kein eigener buildShell()/
   open()/close()/onKey() mehr (bringt Focus-Trap NICHT neu mit, den hatte
   dieser Dialog schon seit #327; hier geht es um die Deduplikation selbst). */
import { createModal } from './modal-shell.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let LLMKeysDialog = null;

if (IS_SERVER_BUILD) {

  /* MVP-Provider — Reihenfolge in der UI (matched mit Server-Strategy). */
  const PROVIDERS = [
    { id: 'anthropic', label: 'Anthropic Claude' },
    { id: 'openai',    label: 'OpenAI ChatGPT / OpenAI-kompatibel' },
    { id: 'google',    label: 'Google Gemini' },
    { id: 'mistral',   label: 'Mistral' },
  ];

  let _modal = null;
  let _bodyEl = null;
  let _stored = new Set(); /* Provider mit hinterlegtem Key */
  let _meta = new Map();   /* provider → { created_at, last_used_at } */
  let _kompatibel = null;  /* Karte #570: { ziel, modelle } wenn „openai“ umgestellt ist */
  /* Karte #664 (ADR-0133 §2): { ziel, anbieter, modelle } im Firmenmodus —
     dann gibt es keine eigenen Schlüssel, nur den des Betreibers. */
  let _firmenmodus = null;
  /* Karte #664: Betreiber-Vorgabe für den PII-Filter ('aus' | 'pflicht'),
     null = Nutzerwahl wie bisher. */
  let _piiVorgabe = null;

  function el(tag, opts = {}) {
    const node = document.createElement(tag);
    if (opts.cls)  node.className = opts.cls;
    if (opts.text != null) node.textContent = opts.text;
    if (opts.type) node.type = opts.type;
    if (opts.attrs) for (const [k, v] of Object.entries(opts.attrs)) node.setAttribute(k, v);
    return node;
  }

  function ensureModal() {
    if (_modal) return;
    _modal = createModal({
      extraOverlayClass: 'llm-overlay',
      extraPanelClass: 'llm-panel',
      titleId: 'llm-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
    });
    _modal.setTitle(I18n.t('llm.title'));
    _bodyEl = _modal.body;
  }

  async function refreshList() {
    const res = await LLMKeysClient.list();
    _stored = new Set();
    _meta = new Map();
    _kompatibel = null;
    _firmenmodus = null;
    _piiVorgabe = null;
    if (res.ok && res.body) {
      _kompatibel = res.body.openai_kompatibel || null;
      _firmenmodus = res.body.firmenmodus || null;
      _piiVorgabe = (res.body.pii === 'aus' || res.body.pii === 'pflicht') ? res.body.pii : null;
      (res.body.providers || []).forEach(p => _stored.add(p));
      (res.body.keys || []).forEach(k => _meta.set(k.provider, k));
    } else if (res.status === 503) {
      _bodyEl.innerHTML = '';
      _bodyEl.appendChild(el('p', { cls: 'llm-error', text: I18n.t('llm.error-503') }));
      return false;
    }
    return true;
  }

  function renderProviderRow(p) {
    const row = el('div', { cls: 'llm-provider-row' });

    const head = el('div', { cls: 'llm-provider-head' });
    /* Karte #570 (K-B2): Bei umgestelltem „openai“ geht der Schlüssel an das
       eigene Gegenüber, nicht an OpenAI — das muss hier stehen, bevor jemand
       einen echten OpenAI-Schlüssel einträgt oder testet. */
    const label = (p.id === 'openai' && _kompatibel)
      ? I18n.t('llm.openai-kompatibel', _kompatibel.ziel || '?') : p.label;
    head.appendChild(el('span', { cls: 'llm-provider-label', text: label }));
    if (p.id === 'openai' && _kompatibel) {
      head.appendChild(el('span', { cls: 'llm-provider-hinweis',
        text: I18n.t('llm.openai-kompatibel-hinweis', _kompatibel.ziel || '?') }));
    }
    const statusBadge = el('span', { cls: 'llm-provider-status' });
    if (_stored.has(p.id)) {
      const meta = _meta.get(p.id);
      const lastUsed = meta?.last_used_at ? ` · ${I18n.t('llm.last-used')} ${meta.last_used_at.slice(0, 10)}` : '';
      statusBadge.textContent = `🔑 ${I18n.t('llm.status-set')}${lastUsed}`;
      statusBadge.classList.add('llm-status-ok');
    } else {
      statusBadge.textContent = `🚫 ${I18n.t('llm.status-empty')}`;
      statusBadge.classList.add('llm-status-empty');
    }
    head.appendChild(statusBadge);
    row.appendChild(head);

    const form = el('div', { cls: 'llm-provider-form' });
    const input = el('input', { type: 'password', cls: 'llm-key-input', attrs: {
      placeholder: I18n.t('llm.input-placeholder'),
      autocomplete: 'off',
      spellcheck: 'false',
    } });
    form.appendChild(input);

    const saveBtn = el('button', { cls: 'llm-btn llm-btn--primary', text: I18n.t('llm.btn-save') });
    saveBtn.addEventListener('click', async () => {
      const key = input.value.trim();
      if (!key) { flash(row, 'error', I18n.t('llm.error-empty-key')); return; }
      saveBtn.disabled = true;
      try {
        const res = await LLMKeysClient.set(p.id, key);
        if (res.ok) {
          if (LLMTab) LLMTab.invalidateKeysCache();
          flash(row, 'ok', I18n.t('llm.msg-saved'));
          await refresh();
        } else {
          flash(row, 'error', errorMessage(res.status));
        }
      } finally {
        /* Sprint-40-Kritiker-Fix [S]P3: Klartext-Key NIE im DOM stehen lassen
           — auch bei Fehler nicht (Browser-Memory-Snapshot / DevTools-Leak). */
        input.value = '';
        saveBtn.disabled = false;
      }
    });
    form.appendChild(saveBtn);

    if (_stored.has(p.id)) {
      const testBtn = el('button', { cls: 'llm-btn', text: I18n.t('llm.btn-test') });
      testBtn.addEventListener('click', async () => {
        testBtn.disabled = true;
        const res = await LLMKeysClient.test(p.id);
        testBtn.disabled = false;
        flash(row, res.ok ? 'ok' : 'error', res.ok ? I18n.t('llm.msg-test-ok') : errorMessage(res.status));
      });
      form.appendChild(testBtn);

      const delBtn = el('button', { cls: 'llm-btn llm-btn--danger', text: I18n.t('llm.btn-delete') });
      delBtn.addEventListener('click', async () => {
        if (!window.confirm(I18n.t('llm.confirm-delete', p.label))) return;
        const res = await LLMKeysClient.delete(p.id);
        if (res.ok) {
          if (LLMTab) LLMTab.invalidateKeysCache();
          flash(row, 'ok', I18n.t('llm.msg-deleted'));
          await refresh();
        } else {
          flash(row, 'error', errorMessage(res.status));
        }
      });
      form.appendChild(delBtn);
    }
    row.appendChild(form);

    const flashEl = el('div', { cls: 'llm-flash' });
    row.appendChild(flashEl);

    return row;
  }

  function flash(row, kind, msg) {
    const f = row.querySelector('.llm-flash');
    if (!f) return;
    f.textContent = msg;
    f.className = `llm-flash llm-flash--${kind}`;
  }

  function errorMessage(status) {
    if (status === 409) return I18n.t('llm.firmenmodus.kein-eigener-schluessel');
    if (status === 422) return I18n.t('llm.error-key-invalid');
    if (status === 502) return I18n.t('llm.error-provider-unreachable');
    if (status === 503) return I18n.t('llm.error-503');
    if (status === 429) return I18n.t('llm.error-429');
    if (status === 403) return I18n.t('llm.error-403');
    return I18n.t('llm.error-server');
  }

  async function refresh() {
    _bodyEl.innerHTML = '';
    _bodyEl.appendChild(el('p', { cls: 'llm-loading', text: I18n.t('llm.loading') }));
    const ok = await refreshList();
    if (!ok) return;

    _bodyEl.innerHTML = '';
    /* Karte #664 (ADR-0133 §2): Im Firmenmodus „vom Betreiber eingerichtet“
       und das Ziel — keine Eingabe, kein Test, kein Löschen. */
    if (_firmenmodus) {
      _bodyEl.appendChild(renderFirmenmodus());
      _bodyEl.appendChild(renderPiiSection());
      return;
    }
    _bodyEl.appendChild(el('p', { cls: 'llm-intro', text: I18n.t('llm.intro') }));
    const list = el('div', { cls: 'llm-providers' });
    PROVIDERS.forEach(p => list.appendChild(renderProviderRow(p)));
    _bodyEl.appendChild(list);
    _bodyEl.appendChild(el('p', { cls: 'llm-privacy-hint', text: I18n.t('llm.privacy-hint') }));
    /* PII-Filter-Sektion (#301, ADR-0097) — per-User-Toggle, lokal in
       localStorage. DSGVO Art. 25/32: Default ist "Immer prüfen". */
    _bodyEl.appendChild(renderPiiSection());
  }

  function renderFirmenmodus() {
    const box = el('div', { cls: 'llm-firmenmodus' });
    box.appendChild(el('p', { cls: 'llm-firmenmodus-titel', text: I18n.t('llm.firmenmodus.titel') }));
    box.appendChild(el('p', { cls: 'llm-firmenmodus-ziel',
      text: I18n.t('llm.firmenmodus.ziel', _firmenmodus.ziel || '?') }));
    return box;
  }

  function renderPiiSection() {
    const sec = el('div', { cls: 'llm-pii-section' });
    sec.appendChild(el('h3', { cls: 'llm-pii-title', text: I18n.t('llm.pii.title') }));
    sec.appendChild(el('p', { cls: 'llm-pii-intro', text: I18n.t('llm.pii.intro') }));

    /* Karte #664: Die Vorgabe des Betreibers übersteuert die Nutzerwahl und
       sperrt die Auswahl. localStorage bleibt unangetastet — fällt die
       Vorgabe weg, gilt wieder, was die Person selbst gewählt hatte. */
    const gesperrt = _piiVorgabe !== null;
    const mode = _piiVorgabe === 'pflicht' ? 'always'
      : _piiVorgabe === 'aus' ? 'never'
      : (() => { try { return localStorage.getItem('aboard.pii.mode') || 'always'; }
        catch { return 'always'; } })(); /* F-1774 */
    if (gesperrt) {
      sec.appendChild(el('p', { cls: 'llm-pii-vorgabe',
        text: I18n.t(_piiVorgabe === 'pflicht' ? 'llm.pii.vorgabe-pflicht' : 'llm.pii.vorgabe-aus') }));
    }
    const options = [
      { value: 'always', label: I18n.t('llm.pii.mode-always') },
      { value: 'custom', label: I18n.t('llm.pii.mode-custom') },
      { value: 'never',  label: I18n.t('llm.pii.mode-never') },
    ];
    const group = el('div', { cls: 'llm-pii-group' });
    for (const o of options) {
      const lbl = el('label', { cls: 'llm-pii-opt' });
      const r = el('input', { type: 'radio', attrs: { name: 'aboard-pii-mode', value: o.value } });
      if (mode === o.value) r.checked = true;
      if (gesperrt) r.disabled = true;
      r.addEventListener('change', () => {
        if (gesperrt) return;
        if (r.checked) {
          try { localStorage.setItem('aboard.pii.mode', o.value); } catch { /* F-1774: Bedienung bleibt */ }
        }
      });
      lbl.appendChild(r);
      lbl.appendChild(document.createTextNode(' ' + o.label));
      group.appendChild(lbl);
    }
    sec.appendChild(group);
    return sec;
  }

  function ensureStyles() {
    if (document.getElementById('llm-keys-dialog-style')) return;
    const css = `
.llm-overlay { z-index: 1100; background: rgba(0,0,0,0.5); }
.llm-panel { background: #fff; color: #222; border-radius: 8px; width: min(720px, 92vw); max-height: 88vh; box-shadow: 0 12px 36px rgba(0,0,0,0.3); }
.llm-panel .modal-header-row { background: #fff; border-bottom: 1px solid #eee; }
.llm-intro { color: #555; font-size: 0.9rem; margin: 0 0 12px; }
.llm-providers { display: flex; flex-direction: column; gap: 16px; }
.llm-provider-row { border: 1px solid #e5e5e5; border-radius: 6px; padding: 12px; }
.llm-provider-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; flex-wrap: wrap; gap: 8px; }
.llm-provider-label { font-weight: 600; }
.llm-provider-hinweis { display: block; font-size: 0.85em; color: #6b4a00; margin-top: 2px; }
.llm-provider-status { font-size: 0.85rem; color: #666; }
.llm-status-ok { color: #1a7c1a; }
.llm-status-empty { color: #666; }
.llm-provider-form { display: flex; gap: 6px; flex-wrap: wrap; align-items: center; }
.llm-key-input { flex: 1 1 280px; min-width: 160px; padding: 6px 8px; border: 1px solid #ccc; border-radius: 4px; font-family: monospace; }
.llm-btn { padding: 6px 12px; border: 1px solid #bbb; background: #f8f8f8; cursor: pointer; border-radius: 4px; font-size: 0.85rem; }
.llm-btn--primary { background: #2563eb; color: #fff; border-color: #2563eb; }
.llm-btn--danger { color: #c00; border-color: #f4baba; background: #fff5f5; }
.llm-btn:disabled { opacity: 0.5; cursor: not-allowed; }
.llm-flash { font-size: 0.85rem; margin-top: 6px; min-height: 1em; }
.llm-flash--ok { color: #1a7c1a; }
.llm-flash--error { color: #c00; }
.llm-error { color: #c00; }
.llm-loading { color: #666; }
.llm-privacy-hint { margin-top: 16px; padding: 8px 10px; background: #fff8e1; border-left: 3px solid #f5b800; font-size: 0.85rem; color: #555; }
.llm-pii-section { margin-top: 20px; padding: 12px 14px; background: #f5f9ff; border: 1px solid #cfe0f5; border-radius: 6px; }
.llm-pii-title { margin: 0 0 6px; font-size: 0.95rem; }
.llm-pii-intro { margin: 0 0 10px; font-size: 0.85rem; color: #555; line-height: 1.5; }
.llm-pii-group { display: flex; flex-direction: column; gap: 4px; }
.llm-pii-opt { font-size: 0.9rem; cursor: pointer; }
.llm-pii-vorgabe { margin: 0 0 8px; font-size: 0.85rem; font-weight: 600; color: #6b4a00; }
.llm-firmenmodus { border: 1px solid #e5e5e5; border-radius: 6px; padding: 12px; }
.llm-firmenmodus-titel { margin: 0 0 4px; font-weight: 600; }
.llm-firmenmodus-ziel { margin: 0; font-size: 0.9rem; color: #555; }
`;
    const style = document.createElement('style');
    style.id = 'llm-keys-dialog-style';
    style.textContent = css;
    document.head.appendChild(style);
  }

  function open() {
    ensureStyles();
    ensureModal();
    _modal.open();
    /* #327: Initial-Focus erst NACH dem (asynchronen) Provider-Listing —
       vorher existieren die Provider-Zeilen noch nicht im DOM. */
    refresh().then(() => _modal.focusAfterRender());
  }

  function close() {
    _modal?.close();
  }

  LLMKeysDialog = { open, close };
}

export { LLMKeysDialog };
