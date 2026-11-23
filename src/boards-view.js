/* boards-view.js — Board-Übersicht im Server-Modus (Karte #624).
 *
 * Zeigt alle Boards, die die angemeldete Person sehen darf (GET /boards,
 * ACL-gefiltert), getrennt nach „Eigene“ (owner) und „Mit mir geteilt“
 * (editor/viewer) — dieselbe Trennung wie im Board-Umschalter der Seitenleiste.
 * Anlass: die Board-ID war nur über die Roh-Antwort von GET /boards zu finden,
 * gebraucht wird sie für `aboard-server agent-token create --board <id>`, den
 * Backup-Reiter und `aboard-remote.json`. Deshalb steht die ID sichtbar in
 * jeder Zeile und lässt sich per Knopf kopieren.
 *
 * Karte #741: Kachelansicht wie bei Trello. Jede Kachel zeigt das verkleinerte
 * Hintergrundbild des Boards (GET /boards/{id}/vorschaubild, serverseitig auf
 * Kachelgröße gerechnet; 204 = kein Bild → Grundfläche), den Titel als Knopf
 * und darunter Rolle, Änderungsdatum und die kopierbare ID (#624 bleibt).
 *
 * Nur im Server-Build (DCE im Slim/Claude-Code-Build). CSS als String in
 * `_ensureBoardsViewStyles()` (nicht in style.css, damit es per DCE verschwindet).
 * Overlay/Panel/Header/Body aus der Modal-Shell (ADR-0118, Variante 'settings').
 */

import { I18n } from './i18n.js';
import { EventBus } from './eventbus.js';
import { State } from './state.js';
import { StoragePlugin } from './storage-plugin.js';
import { createModal } from './modal-shell.js';
import { Rueckmeldung } from './rueckmeldung.js';
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let BoardsView = null;

if (IS_SERVER_BUILD) {

  let _modal = null;
  let _bodyEl = null;
  let _stylesInjected = false;
  /* Nummer des jüngsten Ladelaufs — ein älterer, der später fertig wird,
     rendert nicht mehr (Weg-und-zurück-Wechsel). */
  let _lauf = 0;
  /* Objekt-URLs der geladenen Vorschaubilder (je wenige KB). Freigegeben bei
     close() — auch ✕/Escape/Klick daneben laufen dorthin (onRequestClose) —
     und beim nächsten Öffnen. */
  let _urls = [];
  function freigeben() {
    _urls.forEach(u => { try { URL.revokeObjectURL(u); } catch { /* egal */ } });
    _urls = [];
  }

  /* Farben ausschließlich über Variablen, die im hellen UND dunklen Stil
     lesbar sind (--text/--text-muted/--surface/--surface-2/--border). */
  function _ensureBoardsViewStyles() {
    if (_stylesInjected) return;
    _stylesInjected = true;
    const style = document.createElement('style');
    style.setAttribute('data-aboard', 'boards-view');
    style.textContent = '.bv-panel{max-width:min(1100px,calc(100vw - 32px))}.bv-list{display:grid;grid-template-columns:repeat(auto-fill,minmax(220px,1fr));gap:12px;margin-bottom:12px}.bv-group{margin:14px 0 6px;font-size:15px;font-weight:600;color:var(--text)}.bv-row{display:flex;flex-direction:column;min-width:0;background:var(--surface-2);border:1px solid var(--border);border-radius:6px;overflow:hidden;color:var(--text)}.bv-row--current{border-color:var(--accent);box-shadow:0 0 0 1px var(--accent)}.bv-title{position:relative;display:flex;align-items:flex-end;width:100%;aspect-ratio:16/9;padding:0;border:0;border-bottom:1px solid var(--border);background:var(--surface) center/cover no-repeat;color:var(--text);font-size:15px;font-weight:600;text-align:left;cursor:pointer}.bv-title>span{display:block;width:100%;padding:6px 10px;background:rgba(0,0,0,.62);color:#fff;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;text-shadow:0 1px 2px rgba(0,0,0,.6)}.bv-title:hover>span,.bv-title:focus-visible>span{text-decoration:underline}.bv-title:focus-visible{outline:2px solid var(--accent);outline-offset:-2px}.bv-main{padding:6px 10px 0;min-width:0}.bv-meta{font-size:12px;color:var(--text-muted)}.bv-role{font-weight:600}.bv-idz{display:flex;flex-wrap:wrap;align-items:center;gap:6px;padding:6px 10px 8px}.bv-id{flex:1 1 140px;min-width:0;padding:3px 5px;background:var(--surface);border:1px solid var(--border);border-radius:4px;color:var(--text);font-family:monospace;font-size:11px}.bv-btn{padding:3px 8px;background:var(--surface);border:1px solid var(--border);border-radius:4px;color:var(--text);cursor:pointer;font-size:12px}.bv-btn--ok{border-color:#1a7f37;font-weight:600}.bv-btn--err{border-color:#c00;font-weight:600}.bv-msg{flex-basis:100%;font-size:12px;color:var(--text)}.bv-msg:empty{display:none}.bv-msg--err{font-weight:600}.bv-empty,.bv-loading{color:var(--text-muted);font-style:italic}';
    document.head.appendChild(style);
  }

  function el(tag, opts = {}) {
    const node = document.createElement(tag);
    if (opts.cls) node.className = opts.cls;
    if (opts.text != null) node.textContent = opts.text;
    if (opts.attrs) for (const [k, v] of Object.entries(opts.attrs)) node.setAttribute(k, v);
    return node;
  }

  function ensureModal() {
    if (_modal) return;
    _modal = createModal({
      extraOverlayClass: 'bv-overlay',
      extraPanelClass: 'bv-panel',
      titleId: 'bv-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
      variant: 'settings',
      /* K-8 (Kritiker 07.10.): ✕, Escape und Klick daneben laufen über
         close(), damit das Nachladen der Vorschaubilder stoppt und die
         Objekt-URLs sofort frei werden. */
      onRequestClose: () => close(),
    });
    _modal.setTitle(I18n.t('boards-view.title'));
    _bodyEl = _modal.body;
  }

  function rollenText(role) {
    if (role === 'owner') return I18n.t('share.role-owner');
    if (role === 'editor') return I18n.t('share.role-editor');
    if (role === 'viewer') return I18n.t('share.role-viewer');
    return '';
  }

  function datumText(ms) {
    if (!ms) return '';
    const d = new Date(Number(ms));
    return Number.isNaN(d.getTime()) ? '' : d.toLocaleString();
  }

  async function kopiereId(board, feld, btn, msg) {
    const setze = (art, text) => {
      Rueckmeldung.meldeZeile(msg, 'bv-msg', art, text);
      Rueckmeldung.blinkeKnopf(btn, 'bv-btn', art);
    };
    try {
      await navigator.clipboard.writeText(board.id);
      setze('ok', I18n.t('boards-view.copied'));
    } catch {
      /* Zwischenablage verweigert: ID markieren, damit Strg+C reicht. */
      try { feld.focus(); feld.select(); } catch { /* ohne Auswahl weiter */ }
      setze('err', I18n.t('boards-view.copy-failed'));
    }
  }

  function buildRow(b, currentId) {
    const row = el('div', { cls: 'bv-row' + (b.id === currentId ? ' bv-row--current' : '') });
    /* K-7 (Kritiker 07.10.): Der Titel wird auf der Kachel gekürzt — der volle
       Name steht im Tooltip. */
    const titel = el('button', { cls: 'bv-title', attrs: { type: 'button', 'data-board': b.id, title: b.name || b.id } });
    titel.appendChild(el('span', { text: b.name || b.id }));
    titel.addEventListener('click', () => {
      close();
      if (b.id !== currentId) EventBus.emit('sync:switch-to-board', { boardId: b.id });
    });
    row.appendChild(titel);

    const main = el('div', { cls: 'bv-main' });
    const teile = [];
    const rolle = rollenText(b.role);
    if (rolle) teile.push(rolle);
    const datum = datumText(b.updatedAt);
    if (datum) teile.push(I18n.t('boards-view.updated', datum));
    if (b.id === currentId) teile.push(I18n.t('boards-view.current'));
    main.appendChild(el('div', { cls: 'bv-meta', text: teile.join(' · ') }));
    row.appendChild(main);

    const idz = el('div', { cls: 'bv-idz' });
    const feld = el('input', { cls: 'bv-id', attrs: {
      readonly: '', spellcheck: 'false', 'aria-label': I18n.t('boards-view.id-aria', b.name || b.id),
    } });
    feld.value = b.id;
    idz.appendChild(feld);
    /* F-1463: sichtbarer Text bleibt „ID kopieren“, der zugängliche Name nennt das Board. */
    const kopieren = el('button', { cls: 'bv-btn', text: I18n.t('boards-view.copy'),
      attrs: { type: 'button', 'aria-label': I18n.t('boards-view.copy-aria', b.name || b.id) } });
    const msg = el('span', { cls: 'bv-msg', attrs: { role: 'status' } });
    kopieren.addEventListener('click', () => kopiereId(b, feld, kopieren, msg));
    idz.appendChild(kopieren);
    idz.appendChild(msg);
    row.appendChild(idz);
    return row;
  }

  /* Vorschaubilder nachladen, höchstens vier gleichzeitig. 204 (kein Bild)
     und jeder Fehler lassen die Grundfläche stehen — eine Kachel ohne Bild
     ist kein Fehlerfall. Ein überholter Ladelauf setzt nichts mehr. */
  async function ladeVorschauen(knoepfe, lauf) {
    const plugin = StoragePlugin.getActivePlugin();
    if (typeof plugin?.boardVorschauUrl !== 'function') return;
    const schlange = [...knoepfe];
    const arbeiter = async () => {
      while (schlange.length && lauf === _lauf) {
        const knopf = schlange.shift();
        try {
          const res = await holen(plugin.boardVorschauUrl(knopf.dataset.board));
          if (res.status !== 200) continue;
          const blob = await res.blob();
          if (lauf !== _lauf) return;
          const url = URL.createObjectURL(blob);
          _urls.push(url);
          knopf.style.backgroundImage = `url("${url}")`;
        } catch { /* Grundfläche bleibt */ }
      }
    };
    await Promise.all([arbeiter(), arbeiter(), arbeiter(), arbeiter()]);
  }

  function renderListe(boards, currentId, lauf) {
    _bodyEl.innerHTML = '';
    if (!boards.length) {
      _bodyEl.appendChild(el('p', { cls: 'bv-empty', text: I18n.t('boards-view.empty') }));
      return;
    }
    const sortiert = [...boards].sort((a, c) => (c.updatedAt || 0) - (a.updatedAt || 0));
    const gruppe = (titelKey, liste) => {
      if (!liste.length) return;
      _bodyEl.appendChild(el('h3', { cls: 'bv-group', text: I18n.t(titelKey) }));
      const wrap = el('div', { cls: 'bv-list' });
      liste.forEach(b => wrap.appendChild(buildRow(b, currentId)));
      _bodyEl.appendChild(wrap);
    };
    if (sortiert.some(b => b.role != null)) {
      gruppe('board.switch.own-boards', sortiert.filter(b => b.role === 'owner'));
      gruppe('board.switch.shared-boards', sortiert.filter(b => b.role !== 'owner'));
    } else {
      /* Legacy-Bypass ohne Forward-Auth: keine Rollen, eine flache Liste. */
      gruppe('board.switch.server-boards', sortiert);
    }
    ladeVorschauen(_bodyEl.querySelectorAll('.bv-title'), lauf);
  }

  async function laden() {
    const lauf = ++_lauf;
    freigeben();
    _bodyEl.innerHTML = '';
    _bodyEl.appendChild(el('p', { cls: 'bv-loading', text: I18n.t('admin.loading') }));
    let boards = [];
    let fehler = false;
    try { boards = await StoragePlugin.getActivePlugin().listKnownBoards(); }
    catch { fehler = true; }
    if (lauf !== _lauf) return;
    if (fehler || !Array.isArray(boards)) {
      _bodyEl.innerHTML = '';
      _bodyEl.appendChild(el('p', { cls: 'bv-msg bv-msg--err', text: I18n.t('boards-view.error'), attrs: { role: 'alert' } }));
      return;
    }
    renderListe(boards, State.get()?.board?.id ?? null, lauf);
  }

  function open() {
    _ensureBoardsViewStyles();
    ensureModal();
    _modal.open();
    laden().then(() => _modal.focusAfterRender());
  }

  function close() { _lauf++; _modal?.close(); freigeben(); }

  BoardsView = { open, close };
}

export { BoardsView };
