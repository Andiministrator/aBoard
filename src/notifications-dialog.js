/* notifications-dialog.js — Benachrichtigungs-Einstellungen pro User
 * (#305, ADR-0107).
 *
 * Sichtbar nur im Server-Modus + IS_SERVER_BUILD. Im Slim/Claude-Code-Build
 * per DCE eliminiert (kein Bundle-Beitrag).
 *
 * Pro Event-Typ × Kanal eine Checkbox. Zusätzlich Gotify-Server-URL +
 * Token (write-only) + Test-Button für beide Kanäle.
 *
 * #348/ADR-0118: Overlay/Panel/Header/Body kommen aus der gemeinsamen
 * Modal-Shell (src/modal-shell.js, Variante 'settings') — bringt
 * Focus-Trap/Initial-Focus/Focus-Restore mit, die dieser Dialog vorher
 * nicht hatte (F-347).
 */

import { I18n } from './i18n.js';
import { NotificationsClient } from './notifications-client.js';
import { createModal } from './modal-shell.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let NotificationsDialog = null;

if (IS_SERVER_BUILD) {

  let _modal = null;
  let _bodyEl = null;
  let _eventTypes = [];   // [{ type, label, default_email, default_gotify }]
  let _subs = [];         // [{ event_type, channel, enabled, explicit }]
  let _gotifyState = null; // { is_set, url? }
  /* #420: Zustand des serverseitigen Versand-Hauptschalters. Default true —
     ein aelterer Server ohne dieses Feld soll KEINEN falschen Warnbanner
     ausloesen. */
  let _deliveryEnabled = true;

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
      extraOverlayClass: 'notif-overlay',
      extraPanelClass: 'notif-panel',
      titleId: 'notif-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
      variant: 'settings',
    });
    _modal.setTitle(I18n.t('notif.title'));
    _bodyEl = _modal.body;
  }

  async function refreshData() {
    const [typesRes, subsRes, gotifyRes] = await Promise.all([
      NotificationsClient.eventTypes(),
      NotificationsClient.listSubs(),
      NotificationsClient.getConfig('gotify'),
    ]);
    if (typesRes.ok && typesRes.body) {
      _eventTypes = typesRes.body.event_types || [];
      /* #420: Der Server sagt, ob wirklich zugestellt wird. Fehlt das Feld
         (älterer Server), wird NICHT „aus" angenommen — ein Warnbanner, der
         fälschlich behauptet, es gehe nichts raus, ist schlimmer als keiner. */
      _deliveryEnabled = (typesRes.body.delivery_enabled !== false);
    }
    if (subsRes.ok && subsRes.body) {
      _subs = subsRes.body.subscriptions || [];
    }
    if (gotifyRes.ok && gotifyRes.body) {
      _gotifyState = gotifyRes.body;
    } else {
      _gotifyState = { is_set: false };
    }
  }

  function getSub(eventType, channel) {
    return _subs.find(s => s.event_type === eventType && s.channel === channel) || null;
  }

  function renderSubsTable() {
    const table = el('table', { cls: 'notif-table' });
    const thead = el('thead');
    const trh = el('tr');
    trh.appendChild(el('th', { text: I18n.t('notif.col-event') }));
    trh.appendChild(el('th', { text: '✉️ ' + I18n.t('notif.col-email'), cls: 'notif-col-chan' }));
    trh.appendChild(el('th', { text: '📱 ' + I18n.t('notif.col-gotify'), cls: 'notif-col-chan' }));
    thead.appendChild(trh);
    table.appendChild(thead);

    const tbody = el('tbody');
    _eventTypes.forEach(et => {
      const row = el('tr');
      row.appendChild(el('td', { text: et.label, cls: 'notif-event-label' }));
      ['email', 'gotify'].forEach(ch => {
        const td = el('td', { cls: 'notif-col-chan' });
        const sub = getSub(et.type, ch);
        const checked = sub ? sub.enabled : (ch === 'email' ? et.default_email : et.default_gotify);
        const cb = el('input', { type: 'checkbox', attrs: {
          'data-event': et.type,
          'data-channel': ch,
        } });
        cb.checked = checked;
        cb.addEventListener('change', async (e) => {
          const res = await NotificationsClient.setSub(et.type, ch, e.target.checked);
          if (!res.ok) {
            e.target.checked = !e.target.checked;
            alert(I18n.t('notif.save-failed'));
          } else {
            // Lokal mirror'n statt komplettem Reload.
            const existing = getSub(et.type, ch);
            if (existing) existing.enabled = e.target.checked;
            else _subs.push({ event_type: et.type, channel: ch, enabled: e.target.checked, explicit: true });
          }
        });
        td.appendChild(cb);
        row.appendChild(td);
      });
      tbody.appendChild(row);
    });
    table.appendChild(tbody);
    return table;
  }

  function renderGotifySection() {
    const section = el('div', { cls: 'notif-gotify-section' });
    section.appendChild(el('h3', { text: I18n.t('notif.gotify-title'), cls: 'notif-section-title' }));

    if (!_gotifyState || _gotifyState.secrets_enabled === false) {
      section.appendChild(el('p', { cls: 'notif-warn', text: I18n.t('notif.gotify-secrets-disabled') }));
      return section;
    }

    const urlRow = el('div', { cls: 'notif-row' });
    urlRow.appendChild(el('label', { text: I18n.t('notif.gotify-url'), cls: 'notif-label', attrs: { for: 'notif-gotify-url' } }));
    const urlInput = el('input', { type: 'url', attrs: { id: 'notif-gotify-url', placeholder: 'https://gotify.example' } });
    urlInput.value = _gotifyState.url || '';
    urlRow.appendChild(urlInput);
    section.appendChild(urlRow);

    const tokRow = el('div', { cls: 'notif-row' });
    tokRow.appendChild(el('label', { text: I18n.t('notif.gotify-token'), cls: 'notif-label', attrs: { for: 'notif-gotify-token' } }));
    const tokInput = el('input', { type: 'password', attrs: { id: 'notif-gotify-token', placeholder: _gotifyState.is_set ? '••••••••' : I18n.t('notif.gotify-token-placeholder') } });
    tokRow.appendChild(tokInput);
    section.appendChild(tokRow);

    const btns = el('div', { cls: 'notif-btns' });
    const saveBtn = el('button', { text: I18n.t('btn.save'), cls: 'notif-btn-primary' });
    const testBtn = el('button', { text: I18n.t('notif.test') });
    const delBtn = el('button', { text: I18n.t('btn.delete'), cls: 'notif-btn-danger' });

    saveBtn.addEventListener('click', async () => {
      const url = urlInput.value.trim();
      const token = tokInput.value.trim();
      if (!url) { alert(I18n.t('notif.gotify-url-required')); return; }
      if (!token) { alert(I18n.t('notif.gotify-token-required')); return; }
      const res = await NotificationsClient.setConfig('gotify', { url, token });
      if (res.ok) {
        _gotifyState = { is_set: true, url };
        tokInput.value = '';
        tokInput.placeholder = '••••••••';
        flash(saveBtn, 'ok', I18n.t('notif.saved'));
      } else {
        flash(saveBtn, 'err', (res.body && res.body.error) || I18n.t('notif.save-failed'));
      }
    });

    testBtn.addEventListener('click', async () => {
      const res = await NotificationsClient.test('gotify');
      if (res.ok) flash(testBtn, 'ok', I18n.t('notif.test-ok'));
      else flash(testBtn, 'err', (res.body && res.body.error) || I18n.t('notif.test-failed'));
    });

    delBtn.addEventListener('click', async () => {
      if (!confirm(I18n.t('notif.gotify-delete-confirm'))) return;
      const res = await NotificationsClient.deleteConfig('gotify');
      if (res.ok) {
        _gotifyState = { is_set: false };
        urlInput.value = '';
        tokInput.value = '';
      }
    });

    btns.appendChild(saveBtn);
    btns.appendChild(testBtn);
    if (_gotifyState.is_set) btns.appendChild(delBtn);
    section.appendChild(btns);
    return section;
  }

  function renderEmailSection() {
    const section = el('div', { cls: 'notif-email-section' });
    section.appendChild(el('h3', { text: I18n.t('notif.email-title'), cls: 'notif-section-title' }));
    section.appendChild(el('p', { cls: 'notif-info', text: I18n.t('notif.email-info') }));
    const btns = el('div', { cls: 'notif-btns' });
    const testBtn = el('button', { text: I18n.t('notif.test') });
    testBtn.addEventListener('click', async () => {
      const res = await NotificationsClient.test('email');
      if (res.ok) flash(testBtn, 'ok', I18n.t('notif.test-ok'));
      else flash(testBtn, 'err', (res.body && res.body.error) || I18n.t('notif.test-failed'));
    });
    btns.appendChild(testBtn);
    section.appendChild(btns);
    return section;
  }

  function flash(node, kind, msg) {
    const old = node.textContent;
    node.textContent = msg;
    node.classList.add(kind === 'ok' ? 'notif-flash-ok' : 'notif-flash-err');
    setTimeout(() => {
      node.textContent = old;
      node.classList.remove('notif-flash-ok', 'notif-flash-err');
    }, 2200);
  }

  async function refresh() {
    if (!_bodyEl) return;
    _bodyEl.textContent = I18n.t('notif.loading');
    await refreshData();
    _bodyEl.innerHTML = '';
    _bodyEl.appendChild(el('p', { cls: 'notif-intro', text: I18n.t('notif.intro') }));
    /* #420: zweite Auflage — dass der Versand aus ist, darf nicht still
       passieren. Ohne diesen Hinweis sieht der Nutzer eine vollständig
       konfigurierbare Oberfläche, die nichts zustellt, und sucht den Fehler
       bei sich (oder in seiner SMTP-Konfiguration).

       Der frühere Phase-2-Hinweis stand hier bis Sprint 59 und sagte, die
       Karten-Events „folgen in Karte #339". Sie sind mit dieser Karte da —
       ein Banner, der das Gegenteil behauptet, wäre ab jetzt schlicht
       falsch. */
    if (!_deliveryEnabled) {
      _bodyEl.appendChild(el('p', { cls: 'notif-warn', text: I18n.t('notif.delivery-off') }));
    }
    // Karte #340 / ADR-0113 Option E: board.awaiting-user wird LOKAL im
    // Stop-Hook konfiguriert und erscheint deshalb nicht in der Event-Tabelle
    // unten. Ohne diesen Hinweis sucht ein Nutzer dort vergeblich — die
    // Randbedingung der Karte, nicht bloss Kosmetik.
    _bodyEl.appendChild(el('p', { cls: 'notif-info', text: I18n.t('notif.local-hint') }));
    _bodyEl.appendChild(renderSubsTable());
    _bodyEl.appendChild(renderEmailSection());
    _bodyEl.appendChild(renderGotifySection());
  }

  /* #348: nur noch der dialogspezifische Content — Overlay/Panel/Header/Body
     kommen aus der Modal-Shell (src/modal-shell.js, Variante 'settings'). */
  function ensureStyles() {
    if (document.getElementById('notif-dialog-styles')) return;
    const css = `.notif-intro{margin:0 0 12px;color:var(--text-muted);font-size:13px}.notif-table{width:100%;border-collapse:collapse;margin-bottom:18px;font-size:14px}.notif-table th{text-align:left;padding:6px 8px;border-bottom:1px solid var(--border);font-weight:600}.notif-table td{padding:6px 8px;border-bottom:1px solid var(--border)}.notif-event-label{color:var(--text)}.notif-col-chan{text-align:center;width:80px}.notif-section-title{margin:18px 0 8px;font-size:15px;font-weight:600}.notif-row{display:flex;align-items:center;gap:8px;margin-bottom:8px}.notif-label{flex:0 0 140px;font-size:13px;color:var(--text-muted)}.notif-row input[type="url"],.notif-row input[type="password"]{flex:1;padding:6px 8px;background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;font-size:13px}.notif-btns{display:flex;gap:8px;flex-wrap:wrap;margin-top:6px}.notif-btns button{padding:6px 12px;background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;cursor:pointer;font-size:13px}.notif-btns button:hover{background:var(--btn-bg-hover)}.notif-btn-primary{background:var(--accent-btn) !important;color:#fff !important;border-color:var(--accent-btn) !important}.notif-btn-danger{color:#f99 !important}.notif-flash-ok{color:#9c9 !important}.notif-flash-err{color:#f99 !important}.notif-warn{padding:8px 10px;background:rgba(255,165,0,.12);border:1px solid rgba(255,165,0,.3);border-radius:4px;color:#fc8;font-size:13px}.notif-info{color:var(--text-muted);font-size:13px;margin:4px 0 8px}.notif-email-section,.notif-gotify-section{margin-top:12px;padding-top:8px;border-top:1px solid var(--border)}`;
    const style = document.createElement('style');
    style.id = 'notif-dialog-styles';
    style.textContent = css;
    document.head.appendChild(style);
  }

  function open() {
    ensureStyles();
    ensureModal();
    _modal.open();
    refresh().then(() => _modal.focusAfterRender());
  }

  function close() {
    _modal?.close();
  }

  NotificationsDialog = { open, close };
}

export { NotificationsDialog };
