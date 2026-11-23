import { EventBus }        from './eventbus.js';
import { State }           from './state.js';
import { checkMoveWarning } from './links.js';
import { I18n }            from './i18n.js';
import { Config }          from './config.js';

/* Drag-and-Drop via Pointer Events API (→ ADR-0009) */
export const DragDrop = (() => {
  let drag = null; /* { type, id, sprintId?, colId?, ghost, origEl, offsetX, offsetY, startX, startY, hasMoved } */

  /* FLIP-Snapshot: Positionen aller Karten vor dem Drop-State-Update */
  let pendingFlip = null; /* { rects: Map<cardId, DOMRect>, droppedCardId: string } */

  /** Gibt die FLIP-Daten zurück und leert den Puffer (einmaliger Verbrauch). */
  function takePendingFlip() {
    const data = pendingFlip;
    pendingFlip = null;
    return data;
  }

  function init() {
    document.addEventListener('pointerdown', onPointerDown);
  }

  /* ── Drag-Start ──────────────────────────────────────────────────── */

  function onPointerDown(e) {
    if (e.button !== 0) return;

    const swimlaneHandle = e.target.closest('.sprint-drag-handle');
    const card = !swimlaneHandle && e.target.closest('.card');
    const colHeader = !swimlaneHandle && !card && e.target.closest('.column-header');

    if (swimlaneHandle) {
      const laneEl = swimlaneHandle.closest('.swimlane');
      if (laneEl && laneEl.dataset.sprintId && laneEl.dataset.sprintId !== '__backlog__') {
        startDrag(e, laneEl, { type: 'swimlane', id: laneEl.dataset.sprintId });
      }
    } else if (card) {
      /* ADR-0036: Sprint-Meta-Karten dürfen nicht per Drag verschoben werden */
      if (card.classList.contains('card--sprint-meta')) return;
      /* ADR-0141 §8 (Karte #826): Spiegelkarten der externen Kopie bleiben
         stehen, solange der Rückweg aus ist (Karte #827: `card--nur-lesen`). */
      if (card.classList.contains('card--nur-lesen')) return;
      const col = card.closest('.column');
      startDrag(e, card, {
        type:     'card',
        id:       card.dataset.id,
        sprintId: col?.dataset.sprintId,
        colId:    col?.dataset.id,
      });
    } else if (colHeader) {
      if (e.target.closest('.column-title, .btn-col-delete, .column-title-input, input, button')) return;
      const colEl = colHeader.closest('.column');
      /* Karte #826: gespiegelte Spalten ordnet nur das interne Board. */
      if (colEl.classList.contains('column--struktur-spiegel')) return;
      startDrag(e, colEl, { type: 'col', id: colEl.dataset.id });
    }
  }

  /* Generischer Drag-Start für card/col/swimlane. */
  function startDrag(e, el, extra) {
    e.preventDefault();
    const rect = el.getBoundingClientRect();
    const ghost = buildGhost(el, rect);

    drag = {
      ...extra,
      ghost,
      origEl:   el,
      offsetX:  e.clientX - rect.left,
      offsetY:  e.clientY - rect.top,
      startX:   e.clientX,
      startY:   e.clientY,
      hasMoved: false,
    };

    el.setPointerCapture(e.pointerId);
    el.addEventListener('pointermove', onPointerMove);
    el.addEventListener('pointerup',   onPointerUp);
    el.addEventListener('pointercancel', cancelDrag);
  }

  /* ── Ghost-Element ───────────────────────────────────────────────── */

  function buildGhost(el, rect) {
    const ghost = el.cloneNode(true);
    ghost.className += ' drag-ghost';
    ghost.style.cssText = [
      `position:fixed`,
      `left:${rect.left}px`,
      `top:${rect.top}px`,
      `width:${rect.width}px`,
      `pointer-events:none`,
      `z-index:1000`,
      `display:none`,
    ].join(';');
    document.body.appendChild(ghost);
    return ghost;
  }

  /* ── Bewegung ────────────────────────────────────────────────────── */

  function onPointerMove(e) {
    if (!drag) return;

    if (!drag.hasMoved) {
      const dx = Math.abs(e.clientX - drag.startX);
      const dy = Math.abs(e.clientY - drag.startY);
      if (dx < 5 && dy < 5) return;
      drag.hasMoved = true;
      drag.ghost.style.display = '';
      drag.origEl.classList.add('card-dragging');
    }

    drag.ghost.style.left = `${e.clientX - drag.offsetX}px`;
    drag.ghost.style.top  = `${e.clientY - drag.offsetY}px`;

    if (drag.type === 'card') { highlightDropTarget(e, '.column', 'column-drop-target', istKartenZiel); edgeScroll(e); }
    else if (drag.type === 'swimlane') highlightDropTarget(e, '.swimlane', 'swimlane-drop-target', isValidSwimlaneTarget);
  }

  /* Generischer Drop-Target-Highlighter. selector = '.column' | '.swimlane',
     cls = passende Highlight-Klasse, validate = optionaler Filter (z. B.
     __backlog__-Ausschluss + Eigene-Lane-Ausschluss bei Swimlane-Drag). */
  function highlightDropTarget(e, selector, cls, validate) {
    document.querySelectorAll('.' + cls).forEach(el => el.classList.remove(cls));
    const target = getElementBelow(e)?.closest(selector);
    if (!target) return;
    if (validate && !validate(target)) return;
    target.classList.add(cls);
  }

  /* Karte #826: In eine gespiegelte Lane der externen Kopie wird nichts
     hineingeschoben — der Server weist das ab (ADR-0141 §7). Karte #827 E4:
     Bei Rückweg an nimmt eine gespiegelte Spalte einer gespiegelten Lane
     Karten an (`column--rueckweg-ziel`); eine gebundene Karte darf nur dorthin. */
  function istKartenZiel(colEl) {
    if (eigeneSortSpalte(colEl)) return false;
    if (colEl.classList.contains('column--rueckweg-ziel')) return true;
    if (colEl.classList.contains('column--spiegel')) return false;
    return !drag?.origEl?.classList?.contains('card--gebunden');
  }

  /* F-2763: Bei „nach Dringlichkeit“ ist die eigene Spalte kein Ziel (Karte #1004). */
  function eigeneSortSpalte(colEl) {
    return Config.get('sortierung') === 'dringlichkeit' &&
      colEl.dataset.id === drag.colId && colEl.dataset.sprintId === drag.sprintId;
  }

  /* #326: __backlog__ ist nie Ziel; eigene Lane (no-op) auch nicht. */
  function isValidSwimlaneTarget(lane) {
    const id = lane.dataset.sprintId;
    return id && id !== '__backlog__' && id !== drag.id;
  }

  /* ── Drop ────────────────────────────────────────────────────────── */

  function onPointerUp(e) {
    if (!drag) return;

    if (drag.hasMoved) {
      const below = getElementBelow(e);
      if (drag.type === 'card') dropCard(e, below);
      else if (drag.type === 'swimlane') dropSwimlane(e, below);
      else dropCol(e, below);
    }

    cancelDrag();
  }

  function dropCard(e, below) {
    const targetColEl  = below?.closest('.column');
    if (!targetColEl) return;
    if (eigeneSortSpalte(targetColEl)) { EventBus.emit('hinweis', I18n.t('drag.sort-gesperrt')); return; }
    if (!istKartenZiel(targetColEl)) return;
    const toColId    = targetColEl.dataset.id;
    const toSprintId = targetColEl.dataset.sprintId;

    /* Karte #1004: Bei „nach Dringlichkeit“ ist die Anzeige nicht die gespeicherte
       Reihenfolge. Innerhalb der Spalte gibt es nichts zu ordnen (die Karte spränge
       zurück, die Reihenfolge änderte sich unsichtbar); in eine andere Spalte
       wird angehängt, der DOM-Index passte nicht zum Speicher. */
    const sortiert = Config.get('sortierung') === 'dringlichkeit';

    const targetCardEl = sortiert ? null : below?.closest('.card');
    let   targetIndex  = null;
    if (targetCardEl && targetCardEl !== drag.origEl) {
      /* Index relativ zu Karten dieser Spalte+Sprint-Kombination */
      const cards = [...targetColEl.querySelectorAll('.card')];
      targetIndex = cards.indexOf(targetCardEl);
    }

    /* ADR-0032 Karte #54: Verschiebe-Warnung bei offenen blocked-by / depends-on Verweisen.
       NUR bei user-initiierten Drops — sync-Updates rufen dropCard nie auf. */
    const board = State.get()?.board;
    if (board) {
      /* Aktuelle Karte aus Board finden */
      let dragCard = null;
      for (const sprint of board.sprints) {
        const found = sprint.cards.find(c => c.id === drag.id);
        if (found) { dragCard = found; break; }
      }
      if (dragCard) {
        const check = checkMoveWarning(board, dragCard, toColId);
        if (check.warn) {
          const ids = check.blockers
            .map(c => c.displayId != null ? `#${c.displayId}` : c.title)
            .join(', ');
          const cardLabel = dragCard.displayId != null ? dragCard.displayId : dragCard.title;
          const msg = check.type === 'blocked-by'
            ? I18n.t('link.move-warn-blocked-by', cardLabel, ids)
            : I18n.t('link.move-warn-depends-on', cardLabel, ids);
          if (!confirm(msg)) return; /* Drop abbrechen */
        }
      }
    }

    /* FLIP-Snapshot: Positionen aller sichtbaren Karten vor dem Re-Render erfassen */
    if (!prefersReducedMotion()) {
      pendingFlip = { rects: snapshotRects('.card'), droppedCardId: drag.id };
    }

    EventBus.emit('card:moved', {
      cardId: drag.id,
      toSprintId,
      toColId,
      targetIndex,
    });
  }

  /* #326: Swimlane-Drop. Berechnet targetIndex aus der DOM-Reihenfolge der
     .swimlane-Elemente (ohne __backlog__, das immer Letztes bleibt).
     FLIP-Snapshot der Swimlane-Positionen wird mitgenommen, damit der
     Re-Render sanft umsortiert (analog Karten-FLIP). */
  function dropSwimlane(_e, below) {
    const targetLaneEl = below?.closest('.swimlane');
    if (!targetLaneEl) return;
    const targetId = targetLaneEl.dataset.sprintId;
    if (!targetId || targetId === '__backlog__' || targetId === drag.id) return;

    /* targetIndex ist die Position der Ziel-Swimlane in der reorderbaren Liste
       (alle .swimlane in DOM-Reihenfolge OHNE __backlog__). */
    const lanes = [...document.querySelectorAll('.swimlane')]
      .map(l => l.dataset.sprintId)
      .filter(id => id && id !== '__backlog__');
    const targetIndex = lanes.indexOf(targetId);
    if (targetIndex === -1) return;

    /* FLIP-Snapshot für Swimlanes — analog dropCard. */
    if (!prefersReducedMotion()) {
      pendingFlip = { rects: new Map(), laneRects: snapshotRects('.swimlane', 'sprintId') };
    }

    /* ADR-0117: targetIndex zaehlt in der DOM- und damit SICHTBAREN Ordnung.
       Sind archivierte Swimlanes ausgeblendet, ist das nicht die Ordnung in
       board.sprints — die id der Ziel-Swimlane ist die verlaessliche Angabe. */
    EventBus.emit('swimlane:moved', { sprintId: drag.id, targetIndex, ankerSprintId: targetId });
  }

  function dropCol(_e, below) {
    const targetColEl = below?.closest('.column');
    if (!targetColEl || targetColEl.dataset.id === drag.id) return;

    /* In Swimlane-Modus: gleiche colId erscheint mehrfach → deduplizieren */
    const seen = new Set();
    const uniqueIds = [...document.querySelectorAll('.column')]
      .map(c => c.dataset.id)
      .filter(id => { if (seen.has(id)) return false; seen.add(id); return true; });

    const targetIndex = uniqueIds.indexOf(targetColEl.dataset.id);
    EventBus.emit('col:moved', { colId: drag.id, targetIndex });
  }

  /* ── Edge-Scroll beim Drag (mobile) ─────────────────────────────── */

  function edgeScroll(e) {
    const ZONE = 60;
    const SPEED = 12;
    const flat = document.querySelector('#board-columns:not(.swimlane-mode)');
    const container = flat ?? drag.origEl.closest('.swimlane-columns');
    if (!container) return;
    const rect = container.getBoundingClientRect();
    if      (e.clientX < rect.left  + ZONE) container.scrollLeft -= SPEED;
    else if (e.clientX > rect.right - ZONE) container.scrollLeft += SPEED;
  }

  /* ── Hilfsmethoden ───────────────────────────────────────────────── */

  function prefersReducedMotion() {
    return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  }

  function snapshotRects(selector, keyAttr) {
    const map = new Map();
    document.querySelectorAll(selector).forEach(el => {
      const key = keyAttr ? el.dataset[keyAttr] : el.dataset.id;
      if (key) map.set(key, el.getBoundingClientRect());
    });
    return map;
  }

  function getElementBelow(e) {
    drag.ghost.style.display = 'none';
    const el = document.elementFromPoint(e.clientX, e.clientY);
    drag.ghost.style.display = '';
    return el;
  }

  function cancelDrag() {
    if (!drag) return;
    drag.ghost.remove();
    drag.origEl.classList.remove('card-dragging');
    drag.origEl.removeEventListener('pointermove', onPointerMove);
    drag.origEl.removeEventListener('pointerup',   onPointerUp);
    drag.origEl.removeEventListener('pointercancel', cancelDrag);
    for (const cls of ['column-drop-target', 'swimlane-drop-target']) {
      document.querySelectorAll('.' + cls).forEach(el => el.classList.remove(cls));
    }
    drag = null;
  }

  /* F-388 (Karte #344): Der Tastatur-Weg zum Swimlane-Verschieben liegt im
     Renderer, braucht aber denselben FLIP-Snapshot wie der Maus-Drop — sonst
     springt die Umsortierung, während sie beim Ziehen sanft läuft. Statt die
     Snapshot-Logik zu verdoppeln, wird sie hier zugänglich gemacht: `pendingFlip`
     und `snapshotRects` bleiben modul-privat, und `prefersReducedMotion` gilt
     an einer Stelle für beide Wege. */
  function snapshotSwimlanesForKeyboardMove() {
    if (prefersReducedMotion()) return;
    pendingFlip = { rects: new Map(), laneRects: snapshotRects('.swimlane', 'sprintId') };
  }

  return { init, takePendingFlip, snapshotSwimlanesForKeyboardMove };
})();
