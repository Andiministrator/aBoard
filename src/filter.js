/* Filter-Modul — UI-only, wird nicht persistiert */

import { istVertraulich } from './ops.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

/* Karte #798: Hat ein Agent die Karte innen offen angelegt (Marke des Servers),
   ist sie nicht vertraulich und stammt sie von heute oder dem vorigen Werktag? `created` ist
   ein Datum ohne Uhrzeit (UTC, model.js) — daher Tage statt 24 Stunden. Der vorige Werktag
   überspringt Samstag und Sonntag (montags zählt ab Freitag, F-2407); Feiertage kennt der
   Filter nicht (bekannte Grenze, ADR-0141). */
export function vorigerWerktag(jetzt = Date.now()) {
  const d = new Date(jetzt);
  do { d.setUTCDate(d.getUTCDate() - 1); } while (d.getUTCDay() === 0 || d.getUTCDay() === 6);
  return d.toISOString().slice(0, 10);
}

export function vonAgentNachAussen(card, jetzt = Date.now()) {
  if (!IS_SERVER_BUILD || card?.angelegtVonAgent !== true || istVertraulich(card)) return false;
  const ab = vorigerWerktag(jetzt);
  return typeof card.created === 'string' && card.created.slice(0, 10) >= ab;
}

export const Filter = (() => {
  let owner = '';
  let label = '';
  const flags = new Set(); /* 'critical' | 'blocked' | 'qa' | 'vonAgent' (Karte #798, nur Server) */
  /* Karte #60: Archiv-Toggle — false = archivierte Karten ausblenden (Default) */
  let showArchived = false;
  /* ADR-0133 §1: Finding-Filter (typ/prioritaet/status); gesetzt = nur passende Findings */
  const finding = {};

  function set(patch) {
    if ('owner' in patch) owner = patch.owner;
    if ('label' in patch) label = patch.label;
    if ('flagToggle' in patch) {
      const f = patch.flagToggle;
      flags.has(f) ? flags.delete(f) : flags.add(f);
    }
    if ('finding' in patch) { const [k, v] = patch.finding; if (v) finding[k] = v; else delete finding[k]; }
    if ('showArchived' in patch) showArchived = !!patch.showArchived;
  }

  function clear() { owner = ''; label = ''; flags.clear(); for (const k in finding) delete finding[k]; }

  function isActive() { return !!(owner || label || flags.size || Object.keys(finding).length); }

  function get() { return { owner, label, flags: new Set(flags), showArchived, finding: { ...finding } }; }

  /* Gibt true zurück wenn eine Karte im aktuellen Filter-Zustand sichtbar sein soll.
     Archivierte Karten passieren matches() nur wenn showArchived aktiv ist. */
  function matches(card) {
    /* #302: Owner-Filter / „Meine Karten" — passt jetzt auch wenn der Wert
       in card.assignees[] steht (Owner = primär, Assignees = sichtbar beteiligt). */
    if (owner && card.owner !== owner && !(Array.isArray(card.assignees) && card.assignees.includes(owner))) return false;
    if (label && !card.labels?.includes(label)) return false;
    if (flags.has('critical') && !card.critical) return false;
    if (flags.has('blocked')  && !card.blocked)  return false;
    if (flags.has('qa')       && !card.qa)        return false;
    if (IS_SERVER_BUILD && flags.has('vonAgent') && !vonAgentNachAussen(card)) return false;
    for (const k in finding) if (card.kind !== 'finding' || card.finding?.[k] !== finding[k]) return false;
    return true;
  }

  /* Karte #60: archivierte Karten-Sichtbarkeitsregel (unabhängig vom normalen Filter) */
  function isVisible(card) {
    if (card.archived && !showArchived) return false;
    return true;
  }

  function isShowingArchived() { return showArchived; }

  return { set, clear, isActive, get, matches, isVisible, isShowingArchived };
})();
