/**
 * Termine ohne Karte in der Kalenderansicht (ADR-0138 §4, Karte #743).
 *
 * Reine, DOM-freie Helfer. Ein Termin liegt in `board.termine[]`, nicht in den
 * Swimlanes — für die Kalenderansicht wird er in einen Eintrag der Form
 * `{ card, sprintId }` übersetzt, damit `cardsByDay`, `uhrzeitAmTag` und die
 * Tageslisten ihn wie eine terminierte Karte einsortieren. Der Eintrag trägt
 * `_termin` (der Termin selbst), daran erkennt der Renderer ihn.
 *
 * Abbildung: `datum` → startDate, `bisDatum || datum` → dueDate, `von` →
 * startTime (gehört zum ersten Tag), `bis` → dueTime (gehört zum letzten Tag).
 * Damit gelten dieselben Regeln wie für Karten (ADR-0138 §1): ein Tag mit
 * beiden Uhrzeiten zeigt „von–bis“, ein mehrtägiger Termin trägt die Uhrzeiten
 * an seinem ersten bzw. letzten Tag.
 *
 * Genutzt nur hinter IS_SERVER_BUILD (calendar-view.js, main.js) — im
 * Solo-Template fällt das Modul per Tree-Shaking weg.
 */

import { parseISO, istUhrzeit } from './date-helpers.js';

/** Gültiger Termin für die Anzeige (Form wie der Fold sie verlangt). */
export function istAnzeigbarerTermin(t) {
  return !!t && typeof t === 'object' && typeof t.id === 'string' && t.id !== ''
    && typeof t.titel === 'string' && t.titel.trim() !== '' && !!parseISO(t.datum);
}

/**
 * Termin → Kalender-Eintrag. `extra` ergänzt Felder des Pseudo-Eintrags
 * (Cross-Board: `_crossBoard`, `_boardId`, `_boardTitle`).
 */
export function terminAlsEintrag(t, extra = {}) {
  const bis = parseISO(t.bisDatum) && t.bisDatum >= t.datum ? t.bisDatum : t.datum;
  return {
    sprintId: null,
    card: {
      id: 'termin:' + t.id,
      title: t.titel,
      startDate: t.datum,
      dueDate: bis,
      startTime: istUhrzeit(t.von) ? t.von : null,
      dueTime: istUhrzeit(t.bis) ? t.bis : null,
      _termin: t,
      ...extra,
    },
  };
}

/** Alle anzeigbaren Termine eines Boards als Kalender-Einträge. */
export function termineAlsEintraege(board) {
  const liste = Array.isArray(board?.termine) ? board.termine : [];
  return liste.filter(istAnzeigbarerTermin).map(t => terminAlsEintrag(t));
}

/**
 * Fehler eines Termins aus dem Dialog, als i18n-Schlüssel — oder null. Die
 * verbindliche Prüfung macht der Fold (src/ops.js terminVerwerfung); diese
 * hier gibt der Person eine Meldung, bevor etwas gesendet wird.
 */
export function terminFehler(t) {
  if (!t || typeof t.titel !== 'string' || !t.titel.trim()) return 'termin.fehler.titel';
  if (!parseISO(t.datum)) return 'termin.fehler.datum';
  if (t.bisDatum != null && !(parseISO(t.bisDatum) && t.bisDatum >= t.datum)) return 'termin.fehler.bisdatum';
  if ((t.von != null && !istUhrzeit(t.von)) || (t.bis != null && !istUhrzeit(t.bis))) return 'termin.fehler.zeit';
  if (t.bisDatum == null && t.von != null && t.bis != null && t.bis <= t.von) return 'termin.fehler.reihenfolge';
  return null;
}

/**
 * Termin im Board anlegen oder ersetzen (Mutation am übergebenen Board —
 * main.js ruft das in mutateBoard). Herkunft (`erstellt`, `erstelltVon`)
 * bleibt bei einem vorhandenen Termin die des Bestands, wie im Fold
 * (src/ops.js TERMIN_HERKUNFT) — sonst wiche der lokale Stand vom Server ab.
 */
export function setzeTermin(board, t) {
  if (!board || !t || typeof t.id !== 'string') return;
  if (board.termine != null && !Array.isArray(board.termine)) return;
  if (!Array.isArray(board.termine)) board.termine = [];
  const idx = board.termine.findIndex(x => x && x.id === t.id);
  if (idx === -1) { board.termine.push({ ...t }); return; }
  const alt = board.termine[idx];
  const neu = { ...t };
  for (const k of ['erstellt', 'erstelltVon']) {
    if (k in alt) neu[k] = alt[k];
    else delete neu[k];
  }
  /* ADR-0140 §10: Schalter „vertraulich“ bleibt, wenn der Termin ihn nicht
     trägt (wie termin.upsert im Fold). */
  if (neu.confidential == null && Object.prototype.hasOwnProperty.call(alt, 'confidential')) {
    neu.confidential = alt.confidential;
  }
  board.termine[idx] = neu;
}

/** Termin entfernen (Mutation am übergebenen Board). */
export function entferneTermin(board, id) {
  if (!board || !Array.isArray(board.termine)) return;
  board.termine = board.termine.filter(x => !(x && x.id === id));
}

/** Termin mit dieser id im Board, sonst null. */
export function findeTermin(board, id) {
  const liste = Array.isArray(board?.termine) ? board.termine : [];
  return liste.find(x => x && x.id === id) || null;
}
