/**
 * Kalender-Datensammlung (ADR-0103, Karte #308, Etappe 2).
 *
 * Reine, DOM-freie Helfer, die aus dem Board-Modell die terminierten Karten
 * extrahieren und auf Kalendertage abbilden. Bewusst getrennt vom Renderer,
 * damit die Auswahl-/Verteilungslogik isoliert testbar ist.
 *
 * Datenquelle (ADR-0103): Karten mit gültigem `dueDate`. Sprint-Meta-Karten und
 * (per Default) archivierte Karten werden ausgeschlossen. Range-Karten
 * (`startDate`→`dueDate`) erscheinen an jedem überspannten Tag.
 */

import { parseISO, dateRangeDays, addDays, diffDays, istUhrzeit } from './date-helpers.js';
import { isDoneColumnId } from '../links.js';

/**
 * Sammelt alle terminierten Karten des Boards.
 * @param {object} board  Board-Modell (`board.sprints[].cards[]`)
 * @param {object} [opts]
 * @param {boolean} [opts.includeArchived=false]  Archivierte Karten mitnehmen?
 * @param {boolean} [opts.includeDone=true]       Erledigte (col-erledigt) mitnehmen?
 * @returns {Array<{card:object, sprintId:string}>}
 */
export function collectDatedCards(board, opts = {}) {
  const includeArchived = opts.includeArchived ?? false;
  const includeDone = opts.includeDone ?? true;
  const out = [];
  for (const sprint of board?.sprints ?? []) {
    for (const card of sprint?.cards ?? []) {
      if (card?.kind === 'sprint-meta') continue;
      if (!includeArchived && card?.archived) continue;
      /* F-830: Titel-Semantik statt Id-Hartvergleich — sonst greift
         `includeDone=false` auf einer umbenannten Erledigt-Spalte nicht. */
      if (!includeDone && isDoneColumnId(board, card?.columnId)) continue;
      if (!parseISO(card?.dueDate)) continue;
      out.push({ card, sprintId: sprint.id });
    }
  }
  return out;
}

/**
 * Bildet terminierte Karten auf Kalendertage ab (Range-Karten an jedem Tag).
 * @param {Array<{card:object, sprintId:string}>} datedCards
 * @returns {Map<string, Array<{card:object, sprintId:string}>>} ISO-Tag → Einträge
 */
export function cardsByDay(datedCards) {
  const map = new Map();
  for (const entry of datedCards) {
    const days = dateRangeDays(entry.card?.startDate, entry.card?.dueDate);
    for (const iso of days) {
      if (!map.has(iso)) map.set(iso, []);
      map.get(iso).push(entry);
    }
  }
  /* ADR-0138 §1: je Tag Ganztags zuerst, dann nach Uhrzeit; sonst bleibt die
     Board-Reihenfolge (Array.prototype.sort ist stabil). */
  for (const [iso, list] of map) {
    list.sort((a, b) => vergleicheUhrzeit(uhrzeitAmTag(a.card, iso).sort, uhrzeitAmTag(b.card, iso).sort));
  }
  return map;
}

function vergleicheUhrzeit(a, b) {
  if (a === b) return 0;
  if (a === null) return -1;
  if (b === null) return 1;
  return a < b ? -1 : 1;
}

/**
 * Uhrzeit einer Karte an einem Kalendertag (ADR-0138 §1). `startTime` gehört
 * zum Starttag, `dueTime` zum Fälligkeitstag; an den Tagen dazwischen ist die
 * Karte ganztags. Liegen beide am selben Tag, entsteht „von–bis“ — liegt das
 * Ende vor dem Beginn, zählt nur der Beginn (wie im Feed, ADR-0138 §2).
 * @returns {{label: string|null, sort: string|null}}
 */
export function uhrzeitAmTag(card, iso) {
  const von = card?.startDate === iso && istUhrzeit(card?.startTime) ? card.startTime : null;
  const bis = card?.dueDate === iso && istUhrzeit(card?.dueTime) ? card.dueTime : null;
  if (von && bis) return { label: bis >= von ? `${von}–${bis}` : von, sort: von };
  const t = von || bis;
  return { label: t, sort: t };
}

/**
 * Ist `iso` der erste Tag der Karten-Range (für Balken-Beschriftung im Renderer)?
 * Bei reinen `dueDate`-Karten ist der Einzeltag immer der „erste".
 */
export function isRangeStart(card, iso) {
  const days = dateRangeDays(card?.startDate, card?.dueDate);
  return days.length > 0 && days[0] === iso;
}

/**
 * Liefert die nächsten Termine ab `fromISO` (inkl.), nach `dueDate` sortiert —
 * Datengrundlage für die Agenda-Ansicht (Etappe 3). Stabil sortiert: bei
 * gleichem `dueDate` nach Uhrzeit (ganztags zuerst), dann nach displayId.
 * @param {Array<{card:object, sprintId:string}>} datedCards
 * @param {string} fromISO
 * @param {number} [days=30]  Zeitfenster ab fromISO
 * @returns {Array<{card:object, sprintId:string}>}
 */
/**
 * Berechnet die neuen Datumsfelder, wenn eine Karte per Drag-and-Drop von
 * `fromISO` auf `toISO` verschoben wird (ADR-0103 Etappe 4).
 * Range-Karten (`startDate`+`dueDate`) verschieben sich gemeinsam um das Delta —
 * die Range-Länge bleibt erhalten. Einzeltag-Karten: nur `dueDate` wandert.
 * @returns {{dueDate:string, startDate:string|null}}
 */
export function shiftCardDates(card, fromISO, toISO) {
  const cur = { dueDate: card?.dueDate, startDate: card?.startDate ?? null };
  const delta = diffDays(fromISO, toISO);
  if (delta == null || delta === 0) return cur;
  const dueDate = addDays(card?.dueDate, delta) ?? card?.dueDate;
  const startDate = parseISO(card?.startDate) ? addDays(card.startDate, delta) : (card?.startDate ?? null);
  return { dueDate, startDate };
}

export function upcomingByDue(datedCards, fromISO, days = 30) {
  if (!parseISO(fromISO)) return [];
  const toISO = addDays(fromISO, days); // exklusive obere Schranke
  const within = datedCards.filter(({ card }) => {
    const due = card?.dueDate;
    return due >= fromISO && due < toISO; // ISO-String-Vergleich == chronologisch
  });
  return within
    .slice()
    .sort((a, b) => {
      if (a.card.dueDate !== b.card.dueDate) return a.card.dueDate < b.card.dueDate ? -1 : 1;
      const z = vergleicheUhrzeit(uhrzeitAmTag(a.card, a.card.dueDate).sort, uhrzeitAmTag(b.card, b.card.dueDate).sort);
      if (z) return z;
      return (a.card.displayId ?? 0) - (b.card.displayId ?? 0);
    });
}
