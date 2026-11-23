/* Bulk-Selektion (Karte #63)
   Multi-Select-State als UI-only-Modul — wird NICHT in board persistiert.
   selectedCardIds: Set<string> (UUIDs)

   Architektur:
   - Dieser Modul hält den UI-State (welche Karten selektiert sind).
   - Die Action-Bar wird von renderer.js eingebettet und über Events gesteuert.
   - Bulk-Aktionen feuern Events, die in main.js abgehört werden.

   Shortcuts: Ctrl+A (alle sichtbaren), Esc (clear) — zentral in shortcuts.js eingehängt.
*/

import { EventBus } from './eventbus.js';

export const BulkSelect = (() => {
  /* ── State ─────────────────────────────────────────────────────── */

  const selectedCardIds = new Set();

  /* Letzte angeklickte Karte für Shift-Range-Selektion */
  let lastClickedCardId = null;

  /* ── Getter ────────────────────────────────────────────────────── */

  function getSelected() {
    return new Set(selectedCardIds);
  }

  function isSelected(cardId) {
    return selectedCardIds.has(cardId);
  }

  function count() {
    return selectedCardIds.size;
  }

  /* ── Mutationen ────────────────────────────────────────────────── */

  function select(cardId) {
    if (!cardId) return;
    selectedCardIds.add(cardId);
    lastClickedCardId = cardId;
    _emitChange();
  }

  function deselect(cardId) {
    if (!cardId) return;
    selectedCardIds.delete(cardId);
    _emitChange();
  }

  function toggle(cardId) {
    if (!cardId) return;
    if (selectedCardIds.has(cardId)) {
      selectedCardIds.delete(cardId);
    } else {
      selectedCardIds.add(cardId);
      lastClickedCardId = cardId;
    }
    _emitChange();
  }

  /** Range-Selektion: alle sichtbaren Karten zwischen lastClickedCardId und cardId. */
  function selectRange(cardId, visibleCardIds) {
    if (!lastClickedCardId || !cardId) {
      select(cardId);
      return;
    }
    const fromIdx = visibleCardIds.indexOf(lastClickedCardId);
    const toIdx   = visibleCardIds.indexOf(cardId);
    if (fromIdx === -1 || toIdx === -1) {
      select(cardId);
      return;
    }
    const lo = Math.min(fromIdx, toIdx);
    const hi = Math.max(fromIdx, toIdx);
    for (let i = lo; i <= hi; i++) {
      selectedCardIds.add(visibleCardIds[i]);
    }
    lastClickedCardId = cardId;
    _emitChange();
  }

  /** Alle übergebenen IDs selektieren (für Ctrl+A). */
  function selectAll(cardIds) {
    cardIds.forEach(id => selectedCardIds.add(id));
    if (cardIds.length > 0) lastClickedCardId = cardIds[cardIds.length - 1];
    _emitChange();
  }

  function clearAll() {
    selectedCardIds.clear();
    lastClickedCardId = null;
    _emitChange();
  }

  /* ── Internes ──────────────────────────────────────────────────── */

  function _emitChange() {
    EventBus.emit('bulk:changed', { count: selectedCardIds.size, ids: new Set(selectedCardIds) });
    /* DOM-Klassen synchron aktualisieren */
    _syncDomClasses();
  }

  /** Synchronisiert .card--selected-Klassen im DOM nach State-Änderung. */
  function _syncDomClasses() {
    document.querySelectorAll('.card').forEach(el => {
      const id = el.dataset.id;
      if (!id) return;
      const selected = selectedCardIds.has(id);
      el.classList.toggle('card--selected', selected);
      el.setAttribute('aria-selected', String(selected));
    });
    /* ARIA-Live-Region aktualisieren */
    const liveEl = document.getElementById('bulk-select-live');
    if (liveEl) {
      liveEl.textContent = selectedCardIds.size > 0
        ? `${selectedCardIds.size} Karte${selectedCardIds.size === 1 ? '' : 'n'} ausgewählt`
        : '';
    }
  }

  /* Nach Board-Re-Render: DOM-Klassen neu setzen (Karten wurden neu gebaut). */
  function onBoardRender() {
    _syncDomClasses();
  }

  /* Boardwechsel: Selektion zurücksetzen und melden — sonst bleiben Leiste,
     Zähler und alte Karten-IDs stehen (F-1786; main.js sendet `board:loaded`,
     `board:load` hat nie jemand gesendet). */
  EventBus.on('board:loaded', () => {
    selectedCardIds.clear();
    lastClickedCardId = null;
    _emitChange();
  });

  return {
    getSelected,
    isSelected,
    count,
    select,
    deselect,
    toggle,
    selectRange,
    selectAll,
    clearAll,
    onBoardRender,
  };
})();
