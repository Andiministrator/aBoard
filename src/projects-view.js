/* projects-view.js — Projekt-Verwaltung (#307, ADR-0102).
 *
 * Modal-Overlay mit Projekt-Liste + Add-Form + Inline-Karten-Auflistung
 * (board-übergreifend) für jedes Projekt. Bei Clients-aktiv werden Projekte
 * nach Client gruppiert (optgroup-Pattern in der Anzeige).
 *
 * Sichtbar nur im Server-Modus + IS_SERVER_BUILD. Im Slim/Claude-Code-Build
 * per DCE eliminiert.
 *
 * Karten-Liste pro Projekt nutzt GET /projects/:id/cards (ACL-gefiltert,
 * board-übergreifend). Klick auf eine Karte navigiert zum Board (Hash-Anker
 * #card-<displayId>, einfacher Direkt-Sprung — kein Board-Switch-UI nötig).
 *
 * #348/ADR-0118: Overlay/Panel/Header/Body kommen wie bei clients-view.js
 * (Schwester-Dialog) aus der gemeinsamen Modal-Shell (src/modal-shell.js,
 * Variante 'settings'). Das Content-CSS (`cp-*`) ist nicht mehr dupliziert,
 * sondern wird von clients-view.js importiert — vorher hatte diese Datei
 * einen absichtlich als "Fallback-Stub" bezeichneten Zweit-Inject desselben
 * ~1,5-KB-Strings.
 */

import { I18n } from './i18n.js';
import { ClientsProjectsClient } from './clients-projects-client.js';
import { createModal } from './modal-shell.js';
import { ensureCpContentStyles } from './clients-view.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let ProjectsView = null;

if (IS_SERVER_BUILD) {

  let _modal = null;
  let _bodyEl = null;
  let _projects = [];
  let _clients = [];
  let _expandedId = null;
  let _cardsCache = new Map(); // projectId → [cardEntry...]

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
      titleId: 'cp-projects-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
      variant: 'settings',
    });
    _modal.setTitle(I18n.t('projects.title'));
    _bodyEl = _modal.body;
  }

  async function refreshData() {
    const [projRes, clientRes] = await Promise.all([
      ClientsProjectsClient.listProjects(),
      ClientsProjectsClient.listClients(),
    ]);
    _projects = (projRes.ok && Array.isArray(projRes.body)) ? projRes.body : [];
    _clients = (clientRes.ok && Array.isArray(clientRes.body)) ? clientRes.body : [];
    _cardsCache.clear();
  }

  function clientName(clientId) {
    if (!clientId) return I18n.t('projects.group-no-client');
    const c = _clients.find(x => x.id === clientId);
    return c ? c.name : I18n.t('projects.group-no-client');
  }

  /* Karten eines Projekts laden (lazy) und unterhalb der Zeile rendern. */
  async function renderProjectCards(container, projectId) {
    container.innerHTML = '';
    container.appendChild(el('p', { cls: 'cp-loading',
      text: I18n.t('clients.loading') }));
    let cards = _cardsCache.get(projectId);
    if (!cards) {
      const r = await ClientsProjectsClient.listCardsForProject(projectId);
      cards = (r.ok && Array.isArray(r.body)) ? r.body : [];
      _cardsCache.set(projectId, cards);
    }
    container.innerHTML = '';
    if (cards.length === 0) {
      container.appendChild(el('p', { cls: 'cp-empty', text: I18n.t('projects.cards-empty') }));
      return;
    }
    const table = el('table', { cls: 'cp-cards-table' });
    const thead = el('thead');
    const trh = el('tr');
    [I18n.t('projects.cards-board-col'),
     I18n.t('projects.cards-sprint-col'),
     I18n.t('projects.cards-card-col'),
     I18n.t('projects.cards-column-col')].forEach(h => trh.appendChild(el('th', { text: h })));
    thead.appendChild(trh);
    table.appendChild(thead);
    const tbody = el('tbody');
    cards.forEach(c => {
      const tr = el('tr');
      if (c.archived) tr.classList.add('cp-card-archived');
      tr.appendChild(el('td', { text: c.board_title || c.board_id }));
      tr.appendChild(el('td', { text: c.sprint_title || '—' }));
      const cardCell = el('td');
      const link = el('a', { text: `#${c.display_id} ${c.title}`,
        attrs: { href: `#board=${encodeURIComponent(c.board_id)}&card=${c.display_id}` } });
      cardCell.appendChild(link);
      tr.appendChild(cardCell);
      tr.appendChild(el('td', { text: c.column_id || '—' }));
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    container.appendChild(table);
  }

  function buildProjectRow(p) {
    const row = el('div', { cls: 'cp-row' });
    const main = el('div', { cls: 'cp-row-main' });
    main.appendChild(el('div', { cls: 'cp-row-name', text: p.name }));
    if (p.description) main.appendChild(el('div', { cls: 'cp-row-desc', text: p.description }));
    main.appendChild(el('div', { cls: 'cp-row-meta', text: clientName(p.client_id) }));
    row.appendChild(main);

    const actions = el('div', { cls: 'cp-row-actions' });
    const expandBtn = el('button', { cls: 'cp-btn',
      text: _expandedId === p.id ? '▴' : '▾' });
    expandBtn.addEventListener('click', () => {
      _expandedId = _expandedId === p.id ? null : p.id;
      renderList();
    });
    actions.appendChild(expandBtn);
    const delBtn = el('button', { cls: 'cp-btn cp-btn-danger', text: I18n.t('projects.delete') });
    delBtn.addEventListener('click', async () => {
      if (!confirm(I18n.t('projects.delete-confirm', p.name))) return;
      const r = await ClientsProjectsClient.deleteProject(p.id);
      if (!r.ok) {
        alert(r.status === 403 ? I18n.t('projects.error-403') : I18n.t('projects.error-server'));
        return;
      }
      _cardsCache.delete(p.id);
      await refresh();
    });
    actions.appendChild(delBtn);
    row.appendChild(actions);

    return row;
  }

  function renderList() {
    _bodyEl.innerHTML = '';
    if (_projects.length === 0) {
      _bodyEl.appendChild(el('p', { cls: 'cp-empty', text: I18n.t('projects.empty') }));
    } else {
      /* Bei Clients aktiv: gruppiert nach Client; sonst flache Liste. */
      const groups = new Map();
      for (const p of _projects) {
        const key = p.client_id || '';
        if (!groups.has(key)) groups.set(key, []);
        groups.get(key).push(p);
      }
      const wrap = el('div', { cls: 'cp-list' });
      for (const [clientId, list] of groups.entries()) {
        if (groups.size > 1 || clientId !== '') {
          wrap.appendChild(el('div', { cls: 'cp-section-divider', text: clientName(clientId) }));
        }
        list.forEach(p => {
          wrap.appendChild(buildProjectRow(p));
          if (_expandedId === p.id) {
            const detail = el('div', { cls: 'cp-row-detail' });
            renderProjectCards(detail, p.id);
            wrap.appendChild(detail);
          }
        });
      }
      _bodyEl.appendChild(wrap);
    }

    /* Add-Form */
    const form = el('div', { cls: 'cp-form' });
    form.appendChild(el('h3', { cls: 'cp-form-title', text: I18n.t('projects.add') }));
    const nameInp = el('input', { cls: 'cp-input', type: 'text', attrs: { placeholder: I18n.t('projects.name') } });
    const descInp = el('input', { cls: 'cp-input', type: 'text', attrs: { placeholder: I18n.t('projects.description') } });
    const clientSel = el('select', { cls: 'cp-select' });
    const noneOpt = el('option', { text: I18n.t('projects.client-none') });
    noneOpt.value = '';
    clientSel.appendChild(noneOpt);
    _clients.filter(c => c.active !== false).forEach(c => {
      const opt = el('option', { text: c.name });
      opt.value = c.id;
      clientSel.appendChild(opt);
    });
    const flash = el('div', { cls: 'cp-flash' });
    const saveBtn = el('button', { cls: 'cp-btn cp-btn-primary', text: I18n.t('projects.save') });
    saveBtn.addEventListener('click', async () => {
      if (!nameInp.value.trim()) { flash.textContent = I18n.t('projects.name'); return; }
      saveBtn.disabled = true;
      flash.textContent = '';
      const r = await ClientsProjectsClient.createProject({
        name: nameInp.value.trim(),
        description: descInp.value.trim(),
        clientId: clientSel.value || undefined,
      });
      saveBtn.disabled = false;
      if (!r.ok) {
        if (r.status === 409) flash.textContent = I18n.t('projects.error-409');
        else if (r.status === 403) flash.textContent = I18n.t('projects.error-403');
        else flash.textContent = I18n.t('projects.error-server');
        return;
      }
      nameInp.value = ''; descInp.value = ''; clientSel.value = '';
      await refresh();
    });
    form.appendChild(nameInp);
    form.appendChild(descInp);
    form.appendChild(clientSel);
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

  ProjectsView = { open, close };
}

export { ProjectsView };
