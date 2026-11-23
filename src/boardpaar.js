/* boardpaar.js — Seite dieser Instanz im Board-Paar intern/extern
   (ADR-0140 §4, Karte #778) und Rückweg-Schalter außen (ADR-0141 §7,
   Karte #827 E4).

   Die Seite steht in /me (`boardpaar`: "innen" | "aussen"; fehlt bei „aus“).
   Sie wird je Seitenaufruf einmal gefragt; ein Fehlschlag gilt als „aus“ und
   wird nicht gemerkt, damit der nächste Aufruf neu fragt. Nur im Server-Build
   — im Standalone und im slim Template liefert sie immer „aus“ (DCE).

   Mit derselben Antwort kommt außen `boardpaar_rueckweg: "an"` (Plan D10):
   Dann sind Spiegelkarten bearbeitbar, soweit der Rückweg es nach innen
   trägt. Bis die Antwort da ist und bei jedem Fehlschlag gilt „aus“ — die
   Karten bleiben nur lesbar (in Richtung Schutz). Ändert sich der Wert,
   meldet `boardpaar:rueckweg` das, damit Board und Kalender neu zeichnen.

   Seit Karte #836 (ADR-0141 Nachtrag „bearbeitbar“ je Kopie-Board) ist der
   Schalter aus /me nur die Hälfte: Bearbeitbar ist eine Kopie erst, wenn
   zusätzlich ihr eigener Schalter steht. Den meldet der Board-Abruf
   GET /boards/{id}/schatten (`bearbeitbar`, über schatten-aussen.js und
   setzeKopieStand) — je Board, für das offene Board. Solange er fehlt (vor
   der Antwort, bei Fehler, anderes Board), gilt „nur lesen“ (fail-closed). */

import { StoragePlugin } from './storage-plugin.js';
import { State } from './state.js';
import { EventBus } from './eventbus.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let _seite = null;
let _rueckweg = false;
/* F-2233 (Karte #831): Board-ID, für die der Server zuletzt „keine Kopie“
   gemeldet hat (GET /boards/{id}/schatten: 404 oder kopie: false, Seite
   außen). Nur dann wirken Spiegel-Marken im Browser nicht — sonst (Stand
   unbekannt, Fehler, anderes Board) bleibt gesperrt (fail-closed). */
let _kopieFreiFuer = null;
/* Karte #836: Board-ID, für die der Server zuletzt `bearbeitbar: true`
   gemeldet hat. Nur Tests setzen ALLE_BOARDS_TEST (jedes offene Board). */
const ALLE_BOARDS_TEST = Symbol('alle-boards-test');
let _bearbeitbarFuer = null;

function bearbeitbarOffen() {
  return _bearbeitbarFuer === ALLE_BOARDS_TEST ||
    (_bearbeitbarFuer !== null && _bearbeitbarFuer === State.get()?.board?.id);
}

function setzeRueckweg(an) {
  if (an === _rueckweg) return;
  _rueckweg = an;
  EventBus.emit('boardpaar:rueckweg', { an });
}

/** @returns {Promise<'innen'|'aussen'|'aus'>} */
export function boardpaarSeite() {
  if (!IS_SERVER_BUILD) return Promise.resolve('aus');
  if (_seite) return _seite;
  const plugin = StoragePlugin.getActivePlugin();
  if (typeof plugin?.getCurrentUser !== 'function') return Promise.resolve('aus');
  _seite = plugin.getCurrentUser().then(u => {
    /* F-2020: `boardpaar_fehler` = Server konnte die Seite nicht lesen —
       wie ein Fehlschlag nicht merken, damit der nächste Aufruf neu fragt. */
    if (u?.fehler || u?.boardpaar_fehler) { _seite = null; setzeRueckweg(false); return 'aus'; }
    const seite = u?.boardpaar === 'innen' || u?.boardpaar === 'aussen' ? u.boardpaar : 'aus';
    setzeRueckweg(seite === 'aussen' && u?.boardpaar_rueckweg === 'an');
    return seite;
  }).catch(() => { _seite = null; setzeRueckweg(false); return 'aus'; });
  return _seite;
}

/** Gemerkte Seite vergessen — der nächste Aufruf fragt /me neu. Der
    Admin-Reiter „Board-Paar“ ruft es nach dem Umschalten (Karte #788), damit
    dieser Browser den Schalter „vertraulich“ ohne Neuladen richtig zeigt;
    andere offene Browser sehen es nach dem Neuladen. */
export function vergissBoardpaarSeite() { _seite = null; }

/** F-2233 (Karte #831): Kopie-Stand des offenen Boards setzen — gerufen von
    schatten-aussen.js nach jedem Abruf. `frei` = der Server hat gemeldet, dass
    das Board keine Kopie ist (außen entkoppelt oder nie eine); dann wirken die
    Spiegel-Marken nicht, genau wie die Spiegel-Schranke am Server (IstKopie).
    `boardId` null oder `frei` false = gesperrt. Ändert sich etwas, meldet
    `boardpaar:kopie-frei` es, damit Board und Kalender neu zeichnen. */
export function setzeKopieStand(boardId, frei, bearbeitbar = false) {
  /* Karte #836: `bearbeitbar` = der Schalter des Boards (nur bei einer Kopie
     bedeutsam). Ändert sich der wirksame Rückweg, meldet `boardpaar:rueckweg`
     das — dieselben Hörer wie beim Schalter aus /me. */
  const vorher = rueckwegAn();
  _bearbeitbarFuer = IS_SERVER_BUILD && boardId && !frei && bearbeitbar ? boardId : null;
  if (rueckwegAn() !== vorher) EventBus.emit('boardpaar:rueckweg', { an: rueckwegAn() });
  const neu = IS_SERVER_BUILD && boardId && frei ? boardId : null;
  if (neu === _kopieFreiFuer) return;
  _kopieFreiFuer = neu;
  EventBus.emit('boardpaar:kopie-frei', { boardId: neu });
}

/** Hat der Server für das offene Board „keine Kopie“ gemeldet? */
export function kopieFrei() {
  return IS_SERVER_BUILD && _kopieFreiFuer !== null && _kopieFreiFuer === State.get()?.board?.id;
}

/** Wirkt die Marke `spiegel` an Karte, Termin, Lane oder Spalte? Nein, wenn
    keine da ist oder das offene Board keine Kopie mehr ist (F-2233). */
export function markeWirkt(obj) {
  return IS_SERVER_BUILD && !!obj?.spiegel && !kopieFrei();
}

/** Steht außen der Rückweg für das offene Board an? Der Schalter des Servers
    (zuletzt gemeldet von /me) UND der des Boards (`bearbeitbar` aus
    GET /boards/{id}/schatten, Karte #836). Synchron, für Renderer und Modal;
    vor den ersten Antworten false. */
export function rueckwegAn() { return IS_SERVER_BUILD && _rueckweg && bearbeitbarOffen(); }

/** Ist die Karte an das interne Board gebunden — Spiegel (Marke `spiegel`)
    oder in einer gespiegelten Lane angelegt (wartet auf ihre Marke,
    ADR-0141 §5.1)? `laneId` ist die Lane der Karte; ohne sie wird sie im
    Stand gesucht. Vorlagen sind nie gebunden. */
export function spiegelGebunden(card, laneId) {
  if (!IS_SERVER_BUILD || !card || card.isTemplate) return false;
  if (kopieFrei()) return false; /* F-2233: entkoppelte Kopie ist frei */
  if (card.spiegel) return true;
  const sprints = State.get()?.board?.sprints ?? [];
  const lane = laneId != null
    ? sprints.find(s => s.id === laneId)
    : sprints.find(s => (s.cards ?? []).some(c => c.id === card.id));
  return !!lane?.spiegel;
}

/** F-2224: Auswahl-IDs ohne gebundene Karten. `frei` geht in die Massenaktion,
    `weg` fällt aus der Auswahl (eine eigene Karte kann nach dem Wählen gebunden
    werden, etwa ins Rückweg-Ziel gezogen). Außerhalb des Server-Builds alles frei. */
export function ohneGebundene(ids) {
  const frei = new Set(ids);
  const weg = [];
  if (!IS_SERVER_BUILD) return { frei, weg };
  for (const s of State.get()?.board?.sprints ?? []) {
    for (const c of s.cards ?? []) {
      if (frei.has(c.id) && spiegelGebunden(c, s.id)) { frei.delete(c.id); weg.push(c.id); }
    }
  }
  return { frei, weg };
}

/** Modus einer Karte auf der externen Kopie: null (frei), 'lesen' (Rückweg
    aus, Stand 2b) oder 'rueckweg' (bearbeitbar ohne Löschen, Anhänge,
    Verweise, „vertraulich aus“, Agent-Kontext und KI). */
export function spiegelModus(card, laneId) {
  if (!spiegelGebunden(card, laneId)) return null;
  return rueckwegAn() ? 'rueckweg' : 'lesen';
}

/** Modus eines Termins. Spiegeltermine wie Spiegelkarten. Bei Rückweg an
    ist auf einer Kopie (eine Lane trägt die Marke) jeder Termin gebunden:
    Ein dort angelegter Termin wartet auf seine Marke und ist außen nicht
    mehr löschbar (ADR-0141 §5.1); welcher eigene Termin schon wartet, sieht
    der Browser nicht — also alle. Bei Rückweg aus bleiben eigene Termine frei
    wie in Stufe 2b. */
export function terminModus(termin) {
  if (!IS_SERVER_BUILD || !termin || kopieFrei()) return null;
  if (termin.spiegel) return rueckwegAn() ? 'rueckweg' : 'lesen';
  if (rueckwegAn() && (State.get()?.board?.sprints ?? []).some(s => s.spiegel)) return 'rueckweg';
  return null;
}

/** Nur für Tests: gemerkten Zustand verwerfen bzw. den Schalter setzen. */
export function _resetBoardpaarForTest() { _seite = null; _rueckweg = false; _kopieFreiFuer = null; _bearbeitbarFuer = null; }
/** `bearbeitbar` (Karte #836): ohne Angabe gilt jedes offene Board als
    freigegeben, damit Tests aus der Zeit vor dem Schalter je Board weiter
    allein den Server-Schalter prüfen. */
export function _setzeRueckwegForTest(an, bearbeitbar = true) {
  _bearbeitbarFuer = bearbeitbar ? ALLE_BOARDS_TEST : null;
  setzeRueckweg(!!an);
}
