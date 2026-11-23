/* focus-trap.js — Gemeinsamer A11y-Helfer für Server-Modus-Dialoge
 * (#327, F-347/F-363).
 *
 * Extrahiert aus dem in src/modal.js bereits bewährten Muster: Initial-Focus
 * beim Öffnen, Tab/Shift+Tab-Umlauf innerhalb des Dialogs, Focus-Restore auf
 * das vor dem Öffnen fokussierte Element beim Schließen.
 *
 * ⚠️ BEWUSST NICHT hinter IS_SERVER_BUILD oder IS_PROJECTOR_BUILD gegated.
 * src/sync.js importiert dieses Modul außerhalb jedes Compile-Flag-Blocks
 * für den Mass-Delete-Guard-Dialog (ADR-0106) — der ist im claude-code-Build
 * (aboard.html, IS_SERVER_BUILD=false) aktiv. Läge dieser Helfer hinter
 * IS_SERVER_BUILD, würde er per DCE genau in dem Build verschwinden, in dem
 * sein Aufrufer noch lebt — die F-428-Fehlerklasse (Gate vor der falschen
 * Schicht). Die drei anderen Konsumenten (admin-dialog.js, llm-keys-dialog.js,
 * email-dialog.js) sind selbst vollständig IS_SERVER_BUILD-gegated — dort
 * verschwindet der Helfer im Slim-Build automatisch MIT dem aufrufenden Modul,
 * ohne dass dieses Modul selbst ein Gate braucht.
 */

/* Vollständige Liste der von Browsern fokussierbaren Elemente.
 *
 * Sprint-54-Kritiker: Die erste Fassung kannte nur Formularelemente, Links und
 * `[tabindex]`. Kein heutiger Konsument enthält mehr als das — der Fehler wäre
 * also nicht aufgefallen. Genau das ist das Problem eines als „gemeinsamer
 * Helfer" deklarierten Moduls: Der nächste Dialog mit einem `<details>` oder
 * einem `contenteditable` verliert den Fokus an dieser Stelle lautlos, und der
 * Fehler liegt nicht in dem neuen Dialog, den man dann reviewt, sondern hier.
 * Deshalb vollständig, obwohl aktuell nichts davon gebraucht wird. */
const FOCUSABLE_SELECTOR = [
  'input:not([type="hidden"])',
  'textarea',
  'select',
  'button',
  'a[href]',
  'area[href]',
  /* `details > summary` ja, `details` selbst NEIN: Das Eltern-Element ist in
     echten Browsern nicht per Tab erreichbar und nicht programmatisch
     fokussierbar — der Tab-Umlauf würde dort ins Leere fokussieren und der
     Fokus die Falle faktisch verlassen. happy-dom bildet das nicht ab
     (`details.focus()` setzt dort activeElement), ein Test hätte den Fehler
     also nicht gezeigt (Sprint-54-Verify-Lauf). */
  'details > summary',
  'audio[controls]',
  'video[controls]',
  '[contenteditable]:not([contenteditable="false"])',
  'iframe',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

/**
 * Alle tatsächlich fokussierbaren Elemente innerhalb `container`.
 *
 * Die Sichtbarkeitsprüfung ist bewusst dreiteilig — jeder Teil fängt etwas,
 * das die anderen nicht sehen (Sprint-54-Kritiker):
 *
 * - `offsetParent !== null` erkennt `display:none` (auch bei Vorfahren),
 *   ist aber blind für `visibility:hidden`: dort behält das Element sein
 *   Layout und damit einen offsetParent.
 * - `visibility` muss deshalb getrennt geprüft werden. Ein so verstecktes
 *   Element im Trap heisst für den Nutzer: der Fokus verschwindet.
 * - `inert` entfernt die Fokussierbarkeit im Browser, ohne das Layout zu
 *   ändern. Ein inertes Element als „letztes fokussierbares" führt dazu, dass
 *   `trapTabKey` dorthin fokussieren will, der Browser das verweigert und der
 *   Fokus irgendwo anders landet — der Trap ist an dieser Stelle offen.
 *
 * `closest('[inert]')` statt `node.inert`: `inert` wirkt auf den ganzen
 * Teilbaum, ein Kind eines inerten Containers ist ebenfalls nicht
 * fokussierbar. happy-dom kennt die Eigenschaft nicht durchgängig, deshalb
 * über das Attribut.
 */
export function getFocusableElements(container) {
  if (!container) return [];
  return [...container.querySelectorAll(FOCUSABLE_SELECTOR)].filter((node) => {
    if (node.disabled) return false;
    if (node.offsetParent === null) return false;
    if (typeof node.closest === 'function' && node.closest('[inert]')) return false;
    /* getComputedStyle ist in happy-dom vorhanden, aber defensiv geklammert:
       ein fehlender Stil darf nicht dazu führen, dass der Trap gar nichts
       findet — dann wäre er ganz wirkungslos statt nur unvollständig. */
    try {
      const stil = getComputedStyle(node);
      if (stil.visibility === 'hidden') return false;
      /* `display` zusätzlich über den berechneten Stil, nicht nur über
         offsetParent: In happy-dom ist `offsetParent` nicht implementiert und
         damit wirkungslos — die Prüfung oben fängt dort NICHTS, und ein Test,
         der sie belegen soll, wäre falsch grün (Sprint-54-Verify-Lauf, beim
         Schreiben der ersten focus-trap-Tests aufgefallen). Im Browser greifen
         beide; offsetParent bleibt, weil es zusätzlich versteckte VORFAHREN
         erwischt, die der eigene berechnete Stil nicht zeigt. */
      if (stil.display === 'none') return false;
    } catch { /* ohne Stil-Auskunft gilt das Element als sichtbar */ }
    return true;
  });
}

/** Merkt sich das aktuell fokussierte Element (vor dem Öffnen eines Dialogs aufrufen). */
export function captureFocus() {
  return document.activeElement;
}

/** Gibt den Fokus auf `el` zurück (kein Scroll-Jump). No-op wenn el nicht mehr im DOM ist. */
export function restoreFocus(el) {
  if (el && typeof el.focus === 'function' && document.contains(el)) {
    el.focus({ preventScroll: true });
  }
}

/* Initial-Focus-Reihenfolge (ADR/#327): erstes <input>, sonst erster
 * <button>, sonst der übergebene Fallback (typischerweise der Close-Button),
 * sonst das erste fokussierbare Element überhaupt. */
export function focusInitial(container, fallback) {
  const focusable = getFocusableElements(container);
  const target =
    focusable.find(node => node.tagName === 'INPUT') ||
    focusable.find(node => node.tagName === 'BUTTON') ||
    fallback ||
    focusable[0] ||
    null;
  target?.focus({ preventScroll: true });
  return target;
}

/* Tab/Shift+Tab am Rand von `container` umlaufen lassen. In einen
 * bestehenden keydown-Handler einhängen (z. B. neben Escape). Kein No-Op-
 * Check auf e.key — Aufrufer ruft dies für jedes keydown-Event auf. */
export function trapTabKey(e, container) {
  if (e.key !== 'Tab') return;
  const focusable = getFocusableElements(container);
  if (focusable.length === 0) return;
  const first = focusable[0];
  const last  = focusable[focusable.length - 1];
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault();
    last.focus();
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault();
    first.focus();
  }
}
