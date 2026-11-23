/* board-loeschen.js — „Board löschen…“ im Board-Menü (Karte #845, F-2237).

   Im Server-Modus gab es im Browser keinen Weg, ein Board zu löschen
   (`board:reset` ist dort gesperrt). Gebraucht wird es für den Umstieg eines
   Stufe-1-Boards (ADR-0141 §11 Schritt 3, docs/betrieb.md §4.5) und das
   Wegräumen einer außen entkoppelten Kopie.

   Das Recht entscheidet der Server (DELETE /boards/{id}: nur Eigentümer);
   das Menü blendet den Punkt nur aus. Bestätigung wie beim Notweg „Kopie
   außen löschen“ (admin-dialog.js, #831): der Knopf wird erst aktiv, wenn
   der Board-Titel (ohne Titel: die Board-ID) exakt eingetippt ist.

   Reihenfolge ist wichtig: Ein geplantes oder laufendes Speichern würde das
   gerade gelöschte Board per PUT-404→POST sofort wieder anlegen, und das
   Polling würde es weiter beobachten. Deshalb hält `haken.anhalten()` beides
   an, BEVOR gelöscht wird (main.js liefert die Haken); schlägt das Löschen
   fehl, nimmt `haken.fortsetzen()` das Polling wieder auf und das Board
   bleibt unverändert stehen.

   Nur im Server-Build (DCE im Slim-/Claude-Code-Build). CSS als String in
   `_ensureStyles()`, nicht in style.css. */

import { I18n } from './i18n.js';
import { EventBus } from './eventbus.js';
import { State } from './state.js';
import { StoragePlugin } from './storage-plugin.js';
import { createModal } from './modal-shell.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let BoardLoeschen = null;

if (IS_SERVER_BUILD) {

  let _modal = null;
  /* Kritiker S97 K-2: Während DELETE läuft, schließt der Dialog nicht — sonst
     stünde eine Fehlermeldung in einem unsichtbaren Dialog. */
  let _laeuft = false;
  let _bodyEl = null;
  let _stylesInjected = false;
  let _haken = { anhalten: async () => {}, fortsetzen: () => {}, melde: () => {} };

  function _ensureStyles() {
    if (_stylesInjected) return;
    _stylesInjected = true;
    const style = document.createElement('style');
    style.setAttribute('data-aboard', 'board-loeschen');
    style.textContent = '.bl-panel{max-width:min(520px,calc(100vw - 32px))}.bl-warn{margin:0 0 10px;color:var(--text)}.bl-label{display:block;margin:10px 0 4px;font-size:13px;color:var(--text)}.bl-input{width:100%;box-sizing:border-box;padding:6px 8px;background:var(--surface);border:1px solid var(--border);border-radius:4px;color:var(--text)}.bl-actions{display:flex;gap:8px;margin-top:14px}.bl-msg{margin:10px 0 0;font-size:13px;color:var(--text)}.bl-msg:empty{display:none}.bl-msg--err{font-weight:600}';
    document.head.appendChild(style);
  }

  function el(tag, opts = {}) {
    const node = document.createElement(tag);
    if (opts.cls) node.className = opts.cls;
    if (opts.text != null) node.textContent = opts.text;
    if (opts.attrs) for (const [k, v] of Object.entries(opts.attrs)) node.setAttribute(k, v);
    return node;
  }

  /* Wie kopieLoeschKnopf (#831, K-2): beide Seiten trimmen + NFC; die Board-ID
     gilt immer als Bestätigung. */
  const norm = (t) => String(t ?? '').trim().normalize('NFC');
  function bestaetigt(eingabe, titel, id) {
    const e = norm(eingabe);
    return e !== '' && (e === (norm(titel) || id) || e === id);
  }

  function fehlerText(status, offene) {
    if (status === 403) return I18n.t('board.delete.err-403');
    if (status === 409 && Number.isFinite(offene)) return I18n.t('board.delete.err-409', offene);
    if (status === 0) return I18n.t('board.delete.err-netz');
    return I18n.t('board.delete.err-server');
  }

  /* Nach dem Löschen: auf ein anderes Board (jüngst geändert) wechseln; gibt es
     keins mehr, ein neues anlegen — so steht der Browser nie vor einem Board,
     das es nicht mehr gibt. */
  async function wechselWeg(geloeschtId) {
    let liste = [];
    try { liste = await StoragePlugin.getActivePlugin().listKnownBoards(); } catch { liste = []; }
    const ziel = (Array.isArray(liste) ? liste : []).filter(b => b.id !== geloeschtId)
      .sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0))[0];
    if (ziel) EventBus.emit('sync:switch-to-board', { boardId: ziel.id });
    else EventBus.emit('server:new-board', { title: I18n.t('board.delete.new-title') });
  }

  function ensureModal() {
    if (_modal) return;
    _modal = createModal({
      extraOverlayClass: 'bl-overlay',
      extraPanelClass: 'bl-panel',
      titleId: 'bl-dialog-title',
      closeAriaLabel: I18n.t('btn.close-modal'),
      variant: 'settings',
      onRequestClose: () => { if (!_laeuft) _modal.close(); },
    });
  }

  function open(board) {
    const id = board?.id;
    if (!id) return;
    const titel = board.title ?? '';
    const name = norm(titel) || id;
    _ensureStyles();
    ensureModal();
    _modal.setTitle(I18n.t('board.delete.title'));
    _bodyEl = _modal.body;
    _bodyEl.innerHTML = '';
    _bodyEl.appendChild(el('p', { cls: 'bl-warn', text: I18n.t('board.delete.warning', name) }));
    const feldId = 'bl-bestaetigung';
    _bodyEl.appendChild(el('label', { cls: 'bl-label', text: I18n.t('board.delete.label', name), attrs: { for: feldId } }));
    const feld = el('input', { cls: 'bl-input', attrs: { id: feldId, autocomplete: 'off' } });
    feld.type = 'text';
    _bodyEl.appendChild(feld);
    const msg = el('p', { cls: 'bl-msg', attrs: { role: 'alert' } });
    const leiste = el('div', { cls: 'bl-actions' });
    const ok = el('button', { cls: 'btn btn-primary bl-ok', text: I18n.t('board.delete.ok'), attrs: { type: 'button' } });
    ok.disabled = true;
    const ab = el('button', { cls: 'btn bl-cancel', text: I18n.t('board.delete.cancel'), attrs: { type: 'button' } });
    leiste.appendChild(ok);
    leiste.appendChild(ab);
    _bodyEl.appendChild(leiste);
    _bodyEl.appendChild(msg);

    feld.addEventListener('input', () => { ok.disabled = !bestaetigt(feld.value, titel, id); });
    ab.addEventListener('click', () => _modal.close());
    ok.addEventListener('click', async () => {
      if (ok.disabled || !bestaetigt(feld.value, titel, id)) return;
      ok.disabled = true; ab.disabled = true; feld.disabled = true;
      msg.className = 'bl-msg';
      msg.textContent = I18n.t('admin.working');
      let r;
      _laeuft = true;
      try {
        await _haken.anhalten();
        r = await StoragePlugin.getActivePlugin().deleteBoard(id);
      } catch { r = { ok: false, status: 0 }; } finally { _laeuft = false; }
      /* Kritiker S97 K-2: 404 heißt „schon weg“ (anderer Tab, anderer
         Eigentümer) — wie ein Erfolg wegwechseln, statt auf einem Board stehen
         zu bleiben, das es nicht mehr gibt. */
      const schonWeg = r?.status === 404 && r?.gone === true;
      if (!r?.ok && !schonWeg) {
        /* Das Board bleibt: Polling wieder aufnehmen, Fehler sichtbar lassen. */
        try { _haken.fortsetzen(); } catch { /* best effort */ }
        ab.disabled = false; feld.disabled = false;
        ok.disabled = !bestaetigt(feld.value, titel, id);
        msg.className = 'bl-msg bl-msg--err';
        msg.textContent = fehlerText(r?.status ?? 0, r?.offene);
        return;
      }
      State.markClean(); /* Änderungen am gelöschten Board gehören niemandem mehr */
      _modal.close();
      _haken.melde(I18n.t(schonWeg ? 'board.delete.gone' : 'board.delete.done', name));
      await wechselWeg(id);
    });

    _modal.open();
    feld.focus();
  }

  /** main.js liefert, was nur dort liegt: Speichern/Polling anhalten bzw.
      wieder aufnehmen und die Toast-Meldung. */
  function konfiguriere(haken) { _haken = { ..._haken, ...haken }; }

  BoardLoeschen = { open, konfiguriere };
}

export { BoardLoeschen };
