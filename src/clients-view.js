/* clients-view.js — Client-Verwaltung (#307, ADR-0102).
 *
 * Modal-Overlay mit Liste aller aktiven Clients + Add-Form. Klick auf eine
 * Zeile öffnet eine Inline-Detail-Sicht mit den zugehörigen Projekten.
 *
 * Sichtbar nur im Server-Modus + IS_SERVER_BUILD. Im Slim/Claude-Code-Build
 * per DCE eliminiert (kein Bundle-Beitrag).
 *
 * Admin-only-Aktionen (Add/Edit/Delete) werden vom Server mit 403 abgewehrt
 * — UI zeigt den Fehler an, lädt aber die Listen für jeden authentifizierten
 * User (viewer+).
 *
 * #348/ADR-0118: Overlay/Panel/Header/Body kommen aus der gemeinsamen
 * Modal-Shell (src/modal-shell.js, Variante 'settings') statt aus eigenem
 * Markup — das bringt Focus-Trap/ESC/Klick-außerhalb mit, die dieser Dialog
 * vorher nicht hatte. Die `cp-*`-Inhaltsklassen (Liste/Formular/Tabelle)
 * bleiben dialogspezifisch, sind aber jetzt in `ensureCpContentStyles()`
 * EINMAL definiert und werden von projects-view.js (Schwester-Dialog mit
 * praktisch identischem Content-CSS) importiert statt dupliziert.
 */

import { I18n } from './i18n.js';
import { ClientsProjectsClient } from './clients-projects-client.js';
import { createModal } from './modal-shell.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let ClientsView = null;

/* #348: einmal injiziertes Content-CSS für beide cp-*-Dialoge (clients-view.js
 * UND projects-view.js) — vorher wortgleich in beiden Dateien dupliziert
 * (~1,5 KB). `cp-row-detail` gehört funktional nur zu projects-view.js
 * (Inline-Kartenliste), liegt aber hier mit, weil ein einziger gemeinsamer
 * Block einfacher zu pflegen ist als zwei fast-identische. */

/* F-770 (c), behoben Karte #527: `ensureCpContentStyles` MUSS ein exportierter
   Top-Level-Binding sein (projects-view.js importiert sie modulübergreifend)
   — ein `export function …` lässt sich aber syntaktisch nicht in ein
   `if (IS_SERVER_BUILD) {}` verschachteln (export-Deklarationen sind nur auf
   Modul-Top-Level gültig). Deshalb hier dieselbe Loesung wie beim
   `ClientsView`-Export direkt darunter: ein No-op-Stub als Top-Level-`let`,
   dessen Wert NUR im Server-Build durch die echte, ~1,9 KB grosse Funktion
   ersetzt wird. Im Slim-/Claude-Code-Build bleibt `IS_SERVER_BUILD` beim
   Bau `false` (Compile-Zeit-Konstante) — esbuild faltet den `if`-Block dann
   zu totem Code und der CSS-String verschwindet per DCE aus dem Bundle,
   ohne dass der Import in projects-view.js bricht (die Bindung existiert
   immer, nur ihr Wert unterscheidet sich je Build). Vorbild `_ensureDocViewerStyles`
   in src/sidebar.js passt hier NICHT direkt: die ist eine reine
   modul-interne Funktion ohne Cross-Modul-Export. */
let ensureCpContentStyles = () => {};

if (IS_SERVER_BUILD) {

  let _cpContentStylesInjected = false;
  ensureCpContentStyles = function () {
    if (_cpContentStylesInjected) return;
    _cpContentStylesInjected = true;
    const style = document.createElement('style');
    style.setAttribute('data-aboard', 'cp-content');
    style.textContent = '.cp-empty{color:var(--text-muted);font-style:italic}.cp-list{display:flex;flex-direction:column;gap:8px;margin-bottom:18px}.cp-row{display:flex;align-items:center;justify-content:space-between;padding:8px 10px;background:var(--surface-2);border-radius:4px;gap:8px}.cp-row-main{flex:1;min-width:0}.cp-row-name{font-weight:600;font-size:14px}.cp-row-desc{font-size:12px;color:var(--text-muted);margin-top:2px}.cp-row-meta{font-size:11px;color:var(--text-muted);margin-top:2px}.cp-row-actions{display:flex;gap:6px}.cp-row-detail{padding:8px 10px;background:var(--surface-2);border-radius:4px;margin-top:-4px}.cp-form{border-top:1px solid var(--border);padding-top:12px;display:flex;flex-direction:column;gap:6px}.cp-form-title{margin:0 0 4px;font-size:14px;font-weight:600}.cp-input,.cp-select{padding:6px 8px;background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;font-size:13px}.cp-btn{padding:6px 12px;background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;cursor:pointer;font-size:13px;align-self:flex-start}.cp-btn-primary{background:var(--accent-btn);color:#fff;border-color:var(--accent-btn)}.cp-btn-danger{color:#f99}.cp-flash{font-size:12px;color:#f99;min-height:14px}.cp-loading{color:var(--text-muted);font-style:italic}.cp-section-divider{margin:18px 0 10px;font-size:15px;font-weight:600;border-top:1px solid var(--border);padding-top:12px}.cp-cards-table{width:100%;border-collapse:collapse;font-size:13px;margin-top:4px}.cp-cards-table th,.cp-cards-table td{text-align:left;padding:5px 8px;border-bottom:1px solid var(--border)}.cp-cards-table th{font-weight:600;color:var(--text-muted)}.cp-cards-table .cp-card-archived{opacity:.6}';
    document.head.appendChild(style);
  };

  let _modal = null;
  let _bodyEl = null;
  let _clients = [];
  let _projectsByClient = new Map();

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
      extraOverlayClass: 'cp-overlay',
      extraPanelClass: 'cp-panel',
      titleId: 'cp-clients-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
      variant: 'settings',
    });
    _modal.setTitle(I18n.t('clients.title'));
    _bodyEl = _modal.body;
  }

  async function refreshData() {
    const res = await ClientsProjectsClient.listClients();
    _clients = (res.ok && Array.isArray(res.body)) ? res.body : [];
    /* Projekt-Anzahl pro Client für die Übersicht — eine Bulk-Abfrage. */
    const projRes = await ClientsProjectsClient.listProjects();
    _projectsByClient = new Map();
    if (projRes.ok && Array.isArray(projRes.body)) {
      for (const p of projRes.body) {
        const key = p.client_id || '';
        if (!_projectsByClient.has(key)) _projectsByClient.set(key, []);
        _projectsByClient.get(key).push(p);
      }
    }
  }

  function renderList() {
    _bodyEl.innerHTML = '';

    if (_clients.length === 0) {
      _bodyEl.appendChild(el('p', { cls: 'cp-empty', text: I18n.t('clients.empty') }));
    } else {
      const list = el('div', { cls: 'cp-list' });
      _clients.forEach(c => {
        const row = el('div', { cls: 'cp-row' });
        const nameBox = el('div', { cls: 'cp-row-main' });
        nameBox.appendChild(el('div', { cls: 'cp-row-name', text: c.name }));
        if (c.description) nameBox.appendChild(el('div', { cls: 'cp-row-desc', text: c.description }));
        const projects = _projectsByClient.get(c.id) || [];
        nameBox.appendChild(el('div', { cls: 'cp-row-meta',
          text: I18n.t('clients.projects-count', projects.length) }));
        row.appendChild(nameBox);

        const actions = el('div', { cls: 'cp-row-actions' });
        const delBtn = el('button', { cls: 'cp-btn cp-btn-danger', text: I18n.t('clients.delete') });
        delBtn.addEventListener('click', async () => {
          if (!confirm(I18n.t('clients.delete-confirm', c.name))) return;
          const r = await ClientsProjectsClient.deleteClient(c.id);
          if (!r.ok) {
            alert(r.status === 403 ? I18n.t('clients.error-403') : I18n.t('clients.error-server'));
            return;
          }
          await refresh();
        });
        actions.appendChild(delBtn);
        row.appendChild(actions);
        list.appendChild(row);
      });
      _bodyEl.appendChild(list);
    }

    /* Add-Form */
    const form = el('div', { cls: 'cp-form' });
    form.appendChild(el('h3', { cls: 'cp-form-title', text: I18n.t('clients.add') }));
    const nameInp = el('input', { cls: 'cp-input', type: 'text', attrs: { placeholder: I18n.t('clients.name') } });
    const descInp = el('input', { cls: 'cp-input', type: 'text', attrs: { placeholder: I18n.t('clients.description') } });
    const contactInp = el('input', { cls: 'cp-input', type: 'text', attrs: { placeholder: I18n.t('clients.contact') } });
    const flash = el('div', { cls: 'cp-flash' });
    const saveBtn = el('button', { cls: 'cp-btn cp-btn-primary', text: I18n.t('clients.save') });
    saveBtn.addEventListener('click', async () => {
      if (!nameInp.value.trim()) { flash.textContent = I18n.t('clients.name'); return; }
      saveBtn.disabled = true;
      flash.textContent = '';
      const r = await ClientsProjectsClient.createClient({
        name: nameInp.value.trim(),
        description: descInp.value.trim(),
        contact: contactInp.value.trim(),
      });
      saveBtn.disabled = false;
      if (!r.ok) {
        if (r.status === 409) flash.textContent = I18n.t('clients.error-409');
        else if (r.status === 403) flash.textContent = I18n.t('clients.error-403');
        else flash.textContent = I18n.t('clients.error-server');
        return;
      }
      nameInp.value = ''; descInp.value = ''; contactInp.value = '';
      await refresh();
    });
    form.appendChild(nameInp);
    form.appendChild(descInp);
    form.appendChild(contactInp);
    form.appendChild(saveBtn);
    form.appendChild(flash);
    _bodyEl.appendChild(form);
  }

  async function refresh() {
    _bodyEl.innerHTML = '';
    _bodyEl.appendChild(el('p', { cls: 'cp-loading', text: I18n.t('clients.loading') }));
    await refreshData();
    renderList();
  }

  function open() {
    ensureCpContentStyles();
    ensureModal();
    _modal.open();
    refresh().then(() => _modal.focusAfterRender());
  }

  function close() {
    _modal?.close();
  }

  ClientsView = { open, close };
}

export { ClientsView, ensureCpContentStyles };
