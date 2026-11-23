/* email-dialog.js — Server-Mail-Versand-Dialog (#293, ADR-0091).
 *
 * Triggered vom #292-Export-Submenü "Per E-Mail senden" wenn boardMode='server'
 * UND IS_SERVER_BUILD. Im Standalone/Claude-Code-Build per DCE eliminiert
 * (Konsumenten-Site in modal.js ist ebenfalls IS_SERVER_BUILD-gated).
 *
 * Felder: An (Komma-Liste), CC, Subject (vorbefüllt), Notiz (optional Text).
 * Versand via Sync.sendCardEmail(card.id, payload).
 */

import { I18n } from './i18n.js';
/* #348/ADR-0118: Overlay/Panel/Header/Body + ESC/Klick-außerhalb/Focus-Trap
   kommen aus der gemeinsamen Modal-Shell — kein eigener buildShell()/onKey()
   mehr. */
import { createModal } from './modal-shell.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let EmailDialog = null;

if (IS_SERVER_BUILD) {

  let _modal = null;
  let _card = null;
  let _board = null;
  let _send = null; /* async (cardId, payload) → { ok, status, body } */

  function el(tag, opts = {}) {
    const node = document.createElement(tag);
    if (opts.cls) node.className = opts.cls;
    if (opts.text != null) node.textContent = opts.text;
    if (opts.type) node.type = opts.type;
    if (opts.attrs) for (const [k, v] of Object.entries(opts.attrs)) node.setAttribute(k, v);
    return node;
  }

  function ensureModal() {
    if (_modal) return;
    _modal = createModal({
      extraOverlayClass: 'email-overlay',
      extraPanelClass: 'email-panel',
      titleId: 'email-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
    });
    _modal.setTitle(I18n.t('card.email.dialog-title'));
  }

  function renderBody() {
    const body = _modal.body;
    body.innerHTML = '';

    function field(labelKey, inputType, value, placeholder) {
      const row = el('div', { cls: 'email-row' });
      row.appendChild(el('label', { cls: 'email-label', text: I18n.t(labelKey) }));
      const input = el(inputType === 'textarea' ? 'textarea' : 'input',
        { cls: 'email-input' + (inputType === 'textarea' ? ' email-input--ta' : '') });
      if (inputType !== 'textarea') input.type = inputType;
      input.value = value || '';
      if (placeholder) input.placeholder = placeholder;
      row.appendChild(input);
      body.appendChild(row);
      return input;
    }

    const toInput  = field('card.email.to',  'text', '', 'name@example.org, other@example.org');
    const ccInput  = field('card.email.cc',  'text', '', '(optional)');
    const subInput = field('card.email.subject', 'text',
      `[aBoard] Karte #${_card?.displayId ?? ''}: ${_card?.title ?? ''}`.slice(0, 200));
    const noteInput = field('card.email.note', 'textarea', '',
      I18n.t('card.email.note-placeholder'));

    /* Preview-Block (read-only) */
    body.appendChild(el('h3', { cls: 'email-subhead', text: I18n.t('card.email.preview') }));
    const preview = el('pre', { cls: 'email-preview', text: _card?.description || '' });
    body.appendChild(preview);

    const actions = el('div', { cls: 'email-actions' });
    const sendBtn = el('button', { cls: 'email-btn email-btn--primary', text: I18n.t('card.email.send') });
    const cancelBtn = el('button', { cls: 'email-btn', text: I18n.t('card.email.cancel') });
    actions.appendChild(sendBtn);
    actions.appendChild(cancelBtn);
    body.appendChild(actions);

    const flash = el('div', { cls: 'email-flash' });
    body.appendChild(flash);

    cancelBtn.addEventListener('click', () => closeDialog());
    sendBtn.addEventListener('click', async () => {
      flash.textContent = '';
      const to = toInput.value.split(',').map(s => s.trim()).filter(Boolean);
      const cc = ccInput.value.split(',').map(s => s.trim()).filter(Boolean);
      if (to.length === 0) {
        flash.textContent = I18n.t('card.email.error-no-recipient');
        return;
      }
      sendBtn.disabled = true;
      const res = await _send(_card.id, {
        to, cc,
        subject: subInput.value.trim() || undefined,
        message: noteInput.value || undefined,
      });
      sendBtn.disabled = false;
      if (res.ok) {
        flash.textContent = I18n.t('card.email.ok');
        setTimeout(closeDialog, 1500);
      } else {
        flash.textContent = errorMessage(res.status);
      }
    });
  }

  function errorMessage(status) {
    if (status === 400) return I18n.t('card.email.error-bad-recipient');
    if (status === 403) return I18n.t('card.email.error-acl');
    if (status === 404) return I18n.t('card.email.error-card');
    if (status === 429) return I18n.t('card.email.error-rate');
    if (status === 502) return I18n.t('card.email.error-smtp');
    if (status === 503) return I18n.t('card.email.error-no-smtp-config');
    return I18n.t('card.email.error-server');
  }

  function ensureStyles() {
    if (document.getElementById('email-dialog-style')) return;
    const css = `
.email-overlay { z-index: 1100; background: rgba(0,0,0,0.5); }
.email-panel { background: #fff; color: #222; border-radius: 8px; width: min(640px, 92vw); max-height: 88vh; box-shadow: 0 12px 36px rgba(0,0,0,0.3); }
.email-panel .modal-header-row { background: #fff; border-bottom: 1px solid #eee; }
.email-row { display: flex; flex-direction: column; gap: 4px; margin-bottom: 10px; }
.email-label { font-weight: 600; color: #444; font-size: 0.85rem; }
.email-input { padding: 6px 8px; border: 1px solid #ccc; border-radius: 4px; font-family: inherit; }
.email-input--ta { min-height: 60px; resize: vertical; }
.email-subhead { margin: 16px 0 4px; font-size: 0.95rem; color: #444; }
.email-preview { background: #f8f8f8; padding: 8px 10px; border-radius: 4px; font-size: 0.85rem; max-height: 200px; overflow: auto; white-space: pre-wrap; }
.email-actions { display: flex; gap: 8px; margin-top: 12px; }
.email-btn { padding: 6px 12px; border: 1px solid #bbb; background: #f8f8f8; cursor: pointer; border-radius: 4px; font-size: 0.85rem; }
.email-btn--primary { background: #2563eb; color: #fff; border-color: #2563eb; }
.email-btn:disabled { opacity: 0.5; }
.email-flash { margin-top: 8px; min-height: 1em; font-size: 0.9rem; }
`;
    const style = document.createElement('style');
    style.id = 'email-dialog-style';
    style.textContent = css;
    document.head.appendChild(style);
  }

  /* open(card, board, sendFn) — sendFn: async(cardId, payload) → { ok, status, body } */
  function open(card, board, sendFn) {
    _card = card;
    _board = board;
    _send = sendFn;
    ensureStyles();
    ensureModal();
    _modal.open();
    renderBody();
    /* #327: renderBody() ist synchron — Initial-Focus kann direkt danach greifen. */
    _modal.focusAfterRender();
  }

  function closeDialog() {
    _modal?.close();
  }

  EmailDialog = { open, close: closeDialog };
}

export { EmailDialog };
