/* Tastatur-Shortcut-System (ADR-0035)
   Zentraler Dispatcher für globale Board-Shortcuts.
   Modal-interne Shortcuts bleiben in modal.js lokal (bewusste Abgrenzung). */

import { I18n }        from './i18n.js';
import { EventBus }    from './eventbus.js';
import { BulkSelect }  from './bulk-select.js';

export const Shortcuts = (() => {
  /* Aktuell aktiver Kontext — nur Board-Shortcuts feuern im 'board'-Kontext */
  let context = 'board'; /* 'board' | 'modal' */

  /* Aktive Karte für j/k-Navigation und Enter */
  let activeCardId = null;

  /* Help-Overlay-Referenz */
  let helpOverlay = null;

  /* ── Kontext-Switching ───────────────────────────────────────────── */

  function setContext(ctx) {
    context = ctx;
    if (ctx === 'modal') {
      /* Karte demarkieren wenn Modal öffnet */
      deactivateCard();
    }
  }

  function getContext() { return context; }

  /* ── Karten-Highlight ───────────────────────────────────────────── */

  function deactivateCard() {
    if (activeCardId != null) {
      const el = document.querySelector(`.card[data-id="${CSS.escape(String(activeCardId))}"]`);
      el?.classList.remove('card--keyboard-active');
    }
    activeCardId = null;
  }

  function activateCard(cardEl) {
    deactivateCard();
    if (!cardEl) return;
    activeCardId = cardEl.dataset.id;
    cardEl.classList.add('card--keyboard-active');
    /* Karte ins Sichtfeld scrollen — smooth wenn prefers-reduced-motion nicht gesetzt */
    cardEl.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'smooth' });
  }

  /* ── Sichtbare Karten ermitteln ─────────────────────────────────── */

  /* Alle sichtbaren, nicht-archivierten Karten in DOM-Reihenfolge.
     "Sichtbar" bedeutet: kein display:none-Vorfahre (also nicht in eingeklappter Spalte/Swimlane).

     F-157 (entschieden Sprint 61): Sprint-Meta-Karten (`kind:'sprint-meta'`,
     CSS-Klasse `card--sprint-meta`) bleiben in der j/k-Navigation — bewusst,
     nicht aus Versehen. ADR-0035 äussert sich dazu nicht; das Kriterium liefert
     diese Datei selbst: Ctrl+A (Karte #63, unten) nimmt Meta-Karten heraus,
     weil die anschliessenden Massen-Aktionen (Löschen, Verschieben,
     Archivieren) für sie nach ADR-0036 verboten sind. Für j/k gilt das nicht —
     die Navigation führt zu `Enter` = Karte öffnen, und Öffnen ist bei
     Meta-Karten ausdrücklich erlaubt (Beschreibung und Kommentare sind
     editierbar; genau dort liegen die Übergabe-Notizen zwischen Chats).
     Ein Filter hier würde die einzige Karte, die per Tastatur wirklich
     gebraucht wird, tastaturunerreichbar machen — eine A11y-Verschlechterung
     als „Fix". Der Ausschluss gehört also an die Aktion, nicht an die
     Navigation. */
  function getVisibleCards() {
    return [...document.querySelectorAll('.card')].filter(el => {
      /* archivierte Karten überspringen (CSS-Klasse reicht als Indikator) */
      if (el.classList.contains('card--archived')) return false;
      /* Karten in eingeklappten Spalten/Swimlanes sind nicht sichtbar */
      if (!el.offsetParent) return false;
      return true;
    });
  }

  /* ── Navigation ─────────────────────────────────────────────────── */

  function navigateNext() {
    const cards = getVisibleCards();
    if (cards.length === 0) return;
    if (activeCardId == null) {
      activateCard(cards[0]);
      return;
    }
    const idx = cards.findIndex(el => el.dataset.id === String(activeCardId));
    activateCard(cards[(idx + 1) % cards.length]);
  }

  function navigatePrev() {
    const cards = getVisibleCards();
    if (cards.length === 0) return;
    if (activeCardId == null) {
      activateCard(cards[cards.length - 1]);
      return;
    }
    const idx = cards.findIndex(el => el.dataset.id === String(activeCardId));
    activateCard(cards[(idx - 1 + cards.length) % cards.length]);
  }

  /* ── Neue Karte (n) ─────────────────────────────────────────────── */

  function triggerNewCard() {
    /* Ersten sichtbaren „+ Karte hinzufügen"-Button klicken */
    const addBtn = document.querySelector('.column-cards:not([style*="display: none"]) ~ .column-footer .btn-add-card, .btn-add-card');
    if (addBtn) {
      addBtn.click();
    }
  }

  /* ── Help-Overlay ────────────────────────────────────────────────── */

  const SHORTCUTS_LIST = [
    { key: '/',       descKey: 'shortcuts.help.search'         },
    { key: 'n',       descKey: 'shortcuts.help.new-card'       },
    { key: 'j',       descKey: 'shortcuts.help.next'           },
    { key: 'k',       descKey: 'shortcuts.help.prev'           },
    { key: 'Enter',   descKey: 'shortcuts.help.open'           },
    { key: 'Esc',     descKey: 'shortcuts.help.deactivate'     },
    { key: '?',       descKey: 'shortcuts.help.toggle-help'    },
    { key: 'Ctrl+A',  descKey: 'shortcuts.help.select-all'     },
  ];

  function buildHelpOverlay() {
    const ov = document.createElement('div');
    ov.className = 'shortcuts-overlay';
    ov.setAttribute('role', 'dialog');
    ov.setAttribute('aria-modal', 'true');
    ov.setAttribute('aria-labelledby', 'shortcuts-title');

    const box = document.createElement('div');
    box.className = 'shortcuts-box';

    const title = document.createElement('h2');
    title.id = 'shortcuts-title';
    title.className = 'shortcuts-title';
    title.textContent = I18n.t('shortcuts.help.title');
    box.appendChild(title);

    const table = document.createElement('table');
    table.className = 'shortcuts-table';
    SHORTCUTS_LIST.forEach(({ key, descKey }) => {
      const tr = document.createElement('tr');
      const tdKey  = document.createElement('td');
      const kbd = document.createElement('kbd');
      kbd.textContent = key;
      tdKey.appendChild(kbd);
      const tdDesc = document.createElement('td');
      tdDesc.textContent = I18n.t(descKey);
      tr.appendChild(tdKey);
      tr.appendChild(tdDesc);
      table.appendChild(tr);
    });
    box.appendChild(table);

    const closeBtn = document.createElement('button');
    closeBtn.className = 'btn shortcuts-close';
    closeBtn.textContent = I18n.t('btn.close-modal');
    closeBtn.addEventListener('click', closeHelp);
    box.appendChild(closeBtn);

    ov.appendChild(box);

    /* Hintergrund-Klick schließt */
    ov.addEventListener('click', (e) => { if (e.target === ov) closeHelp(); });

    /* Fokus-Trap */
    ov.addEventListener('keydown', (e) => {
      if (e.key === 'Escape') { e.stopPropagation(); closeHelp(); return; }
      if (e.key !== 'Tab') return;
      const focusable = [...ov.querySelectorAll('button')];
      if (focusable.length === 0) return;
      const first = focusable[0], last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    });

    return { ov, closeBtn };
  }

  function openHelp() {
    if (helpOverlay) { closeHelp(); return; }
    const { ov, closeBtn } = buildHelpOverlay();
    helpOverlay = ov;
    document.body.appendChild(ov);
    /* Einblend-Animation */
    requestAnimationFrame(() => ov.classList.add('shortcuts-overlay--open'));
    closeBtn.focus();
  }

  function closeHelp() {
    if (!helpOverlay) return;
    helpOverlay.remove();
    helpOverlay = null;
  }

  /* ── Eingabe-Guard ──────────────────────────────────────────────── */

  function isInputFocused(e) {
    const tag = e.target?.tagName?.toLowerCase();
    if (tag === 'input' || tag === 'textarea' || tag === 'select') return true;
    if (e.target?.isContentEditable) return true;
    return false;
  }

  /* ── Globaler Keydown-Handler ────────────────────────────────────── */

  function onKeyDown(e) {
    /* IME-Komposition: nicht feuern */
    if (e.isComposing) return;

    /* Karte #63: Ctrl+A / Cmd+A — alle sichtbaren Karten selektieren (kein normales Browser-Select) */
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key === 'a') {
      /* Nur im Board-Kontext (kein Modal, kein Input) */
      if (context !== 'modal' && !helpOverlay && !isInputFocused(e)) {
        e.preventDefault();
        /* Alle sichtbaren Nicht-Meta-Karten selektieren */
        const visibleIds = getVisibleCards()
          .filter(el => !el.classList.contains('card--sprint-meta') && !el.classList.contains('card--gebunden'))
          .map(el => el.dataset.id)
          .filter(Boolean);
        BulkSelect.selectAll(visibleIds);
      }
      return;
    }

    /* Andere Modifier-Combis dem Browser überlassen */
    if (e.ctrlKey || e.metaKey || e.altKey) return;

    /* Help-Overlay ist offen — nur Esc/Tab bearbeiten (Fokus-Trap im Overlay selbst) */
    /* Fokus auf body (Klick in die Tabelle): der Overlay-Handler sieht das Escape
       nicht — hier schließen (Sitzungs-QA Sprint 85). */
    if (helpOverlay) { if (e.key === 'Escape') closeHelp(); return; }

    /* Modal-Kontext: globale Shortcuts nicht feuern */
    if (context === 'modal') return;

    const key = e.key;

    /* Esc: immer erlaubt (auch in Inputs — zum Blur) */
    if (key === 'Escape') {
      /* Karte #63: Bulk-Selektion zuerst aufheben wenn aktiv */
      if (BulkSelect.count() > 0) {
        BulkSelect.clearAll();
        e.preventDefault();
        return;
      }
      if (activeCardId != null) {
        deactivateCard();
        e.preventDefault();
      }
      return;
    }

    /* Alle anderen Shortcuts: in Inputs nicht feuern */
    if (isInputFocused(e)) return;

    if (key === '/') {
      e.preventDefault();
      const searchInput = document.getElementById('search-input');
      if (searchInput) searchInput.focus();
      return;
    }

    if (key === '?') {
      e.preventDefault();
      openHelp();
      return;
    }

    if (key === 'n') {
      e.preventDefault();
      triggerNewCard();
      return;
    }

    if (key === 'j') {
      e.preventDefault();
      navigateNext();
      return;
    }

    if (key === 'k') {
      e.preventDefault();
      navigatePrev();
      return;
    }

    /* Enter auf einem Knopf/Link/Menüeintrag gehört dem Element, nicht der markierten
       Karte (sonst öffnete Enter im Bulk-Menü die Karte, Sitzungs-QA Sprint 85).
       Dazu zählen die eigenen role="button"-Elemente (Sprint-Titel, Logo,
       Swimlane-Griff, Chat-Kopf — Kritiker Sprint 85, F-1828). */
    if (key === 'Enter' && activeCardId != null && !e.target?.closest?.('button,a,[role=menuitem],[role=button]')) {
      e.preventDefault();
      /* Aktive Karte öffnen via card:edit-Event */
      const cardEl = document.querySelector(`.card[data-id="${CSS.escape(String(activeCardId))}"]`);
      if (cardEl) cardEl.click();
      return;
    }
  }

  /* ── Init ────────────────────────────────────────────────────────── */

  let initialized = false;

  function init() {
    if (initialized) return;
    initialized = true;
    document.addEventListener('keydown', onKeyDown);

    /* Modal öffnet → Board-Shortcuts deaktivieren */
    EventBus.on('card:edit', () => setContext('modal'));

    /* Listener auf modal:closed würde einen neuen Event brauchen — stattdessen
       hören wir auf card:updated, card:deleted, config:open und board:save.
       Zuverlässiger: Modal setzt den Kontext über setContext('board') zurück.
       Das Modal-Modul ruft Shortcuts.setContext('board') beim Schließen auf. */
  }

  /* Karte demarkieren wenn Board neu gerendert wird (Karten-IDs können sich ändern) */
  function onBoardRender() {
    /* Prüfen ob aktive Karte noch existiert */
    if (activeCardId == null) return;
    const el = document.querySelector(`.card[data-id="${CSS.escape(String(activeCardId))}"]`);
    if (!el) {
      activeCardId = null;
    } else {
      /* Highlight-Klasse nach Re-Render wieder setzen */
      el.classList.add('card--keyboard-active');
    }
  }

  return { init, setContext, getContext, onBoardRender, openHelp, closeHelp };
})();
