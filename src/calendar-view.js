/* calendar-view.js — Kalender-Ansicht (#308, ADR-0103).
 *
 * Top-Level-Modal-Overlay analog Clients/Projekte (ADR-0102), ABER bewusst
 * NICHT hinter IS_SERVER_BUILD: der Kalender basiert auf den Karten-Datumsfeldern
 * (ADR-0034), die in allen Modi existieren → er gehört ins Slim-Bundle. Nur die
 * Cross-Board- und iCal-Teile (Etappen 5/6) werden server-gegated.
 *
 * Etappe 2: Monatsansicht + Sidebar + URL-Hash-Routing.
 * Etappe 3: Woche + Agenda + Filter (Erledigt-Toggle, Owner/Assignee).
 *           Projekt-Filter (#307) ist server-only Datenquelle → Etappe 5.
 * Etappe 4 (offen): Quick-Add + Drag-and-Drop.
 *
 * Monats-/Wochentagsnamen via Intl.DateTimeFormat (kein i18n-Hardcoding,
 * Bundle-Argument ADR-0103).
 */

import { I18n } from './i18n.js';
import { State } from './state.js';
import { EventBus } from './eventbus.js';
import {
  monthGrid, toISO, parseISO, isInMonth, isWeekend,
  todayISO, getISOWeek, weekDays, addDays,
} from './calendar/date-helpers.js';
import { collectDatedCards, cardsByDay, upcomingByDue, shiftCardDates, uhrzeitAmTag } from './calendar/collect.js';
import { ClientsProjectsClient } from './clients-projects-client.js';
import { isDoneColumnId } from './links.js';
/* Karte #743 (ADR-0138 §4): Termine ohne Karte — nur im Server-Build genutzt,
   im Solo-Template per Tree-Shaking weg. */
import { termineAlsEintraege, terminAlsEintrag, terminFehler, findeTermin, istAnzeigbarerTermin } from './calendar/termine.js';
import { Config } from './config.js';
import { istVertraulich } from './ops.js'; /* Karte #778, nur gegatet benutzt */
import { boardpaarSeite, spiegelModus, terminModus, rueckwegAn, markeWirkt } from './boardpaar.js'; /* Karte #778/#827/#831 */
import { SchattenAussen, schattenAlsKalenderEintraege } from './schatten-aussen.js'; /* Karte #781, nur gegatet benutzt */
import { captureFocus, restoreFocus, trapTabKey, getFocusableElements } from './focus-trap.js';

/* Server-only-Teile (Cross-Board-Modus + Projekt-Filter, #308 Etappe 5).
   Im Slim/Claude-Code-Build per DCE eliminiert — der Kalender-Kern (Monat/
   Woche/Agenda/DnD) bleibt build-übergreifend, nur die /me/cards- und
   /projects-Datenquelle ist server-gebunden (ADR-0103). */
const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

const AGENDA_DAYS = 30;

let _overlay = null;
let _panel = null;
let _bodyEl = null;
let _controlsEl = null;
let _headerLabel = null;
let _onKeyHandler = null;
let _rueckwegHoerer = null;
let _schattenHoerer = null; /* Karte #781: Listener auf 'schatten:geladen' (server-only) */

/* View-Zustand. `_anchor` = Referenztag (Monat: beliebiger Tag im Zielmonat;
   Woche: Tag in der Zielwoche; Agenda: Starttag des Vorschau-Fensters). */
let _view = 'month'; // 'month' | 'week' | 'agenda'
let _anchor = null;  // ISO, default heute
const _filter = { showDone: false, owner: '', project: '', crossBoard: false };

/* Server-only-Caches (#308 Etappe 5). Nur im Server-Build befüllt.
   `_projects`: [{id,name}] für den Projekt-Filter (lazy geladen).
   `_remoteEntries`: Cross-Board-Karten als {card}-Entries oder null (nicht geladen). */
let _projects = null;
let _remoteEntries = null;
/* Karte #743: Cross-Board-Termine (GET /me/termine) als Kalender-Einträge. */
let _remoteTermine = null;
let _remoteLoading = false;
let _remoteError = false;

function el(tag, opts = {}) {
  const node = document.createElement(tag);
  if (opts.cls) node.className = opts.cls;
  if (opts.text != null) node.textContent = opts.text;
  if (opts.attrs) for (const [k, v] of Object.entries(opts.attrs)) node.setAttribute(k, v);
  return node;
}

function locale() {
  return I18n.getLang() === 'de' ? 'de-DE' : 'en-US';
}

/* F-830: Titel-Semantik statt Id-Hartvergleich für „ist diese Karte erledigt"
   — Links.isDoneColumnId braucht die columnDefs des BOARDS, auf dem die Karte
   liegt. Für lokale Karten ist das `State.get()?.board`. Für Cross-Board-
   Einträge (#308 Etappe 5, `card._crossBoard`) fehlen die columnDefs des
   fremden Boards völlig — `loadRemoteCards()` lädt nur eine Karten-Summary
   ohne Spalten-Metadaten des Herkunftsboards. `isDoneColumnId(null, …)` fällt
   dort auf denselben Id-Vergleich wie vorher zurück (siehe Fallback in
   links.js) — für Cross-Board bleibt eine umbenannte Erledigt-Spalte auf dem
   FREMDEN Board also weiterhin unerkannt. Das ist keine Regression (identisch
   zum bisherigen Verhalten), aber auch kein Fix — dafür müsste die REST-Antwort
   die columnDefs des Herkunftsboards mitliefern, was ausserhalb dieses Findings
   liegt. */
function karteIstErledigt(card) {
  return isDoneColumnId(card?._crossBoard ? null : State.get()?.board, card?.columnId);
}

/* Anzeige-only → Lokalzeit-Date erlaubt (Zeitzonen-Doktrin: nur Arithmetik UTC). */
function monthTitle(year, month) {
  const d = new Date(year, month - 1, 1);
  return new Intl.DateTimeFormat(locale(), { month: 'long', year: 'numeric' }).format(d);
}

function weekdayLabels() {
  const fmt = new Intl.DateTimeFormat(locale(), { weekday: 'short' });
  // 2024-01-01 ist ein Montag → 7 aufeinanderfolgende Tage = Mo…So.
  return Array.from({ length: 7 }, (_, i) => fmt.format(new Date(2024, 0, 1 + i)));
}

function dayLongLabel(iso) {
  const p = parseISO(iso);
  const d = new Date(p.year, p.month - 1, p.day);
  return new Intl.DateTimeFormat(locale(), { weekday: 'short', day: 'numeric', month: 'short' }).format(d);
}

function anchorParts() {
  const p = parseISO(_anchor) || parseISO(todayISO());
  return { year: p.year, month: p.month, day: p.day };
}

/* ── Daten + Filter ───────────────────────────────────────────────────── */

/* Basis-Entries je nach Modus: lokales Board oder (Server) Cross-Board.
   Cross-Board-Entries sind bereits {card}-geformt (siehe loadRemoteCards). */
function baseEntries() {
  if (IS_SERVER_BUILD && _filter.crossBoard) {
    return (_remoteEntries || []).filter(({ card }) =>
      _filter.showDone || !karteIstErledigt(card));
  }
  const board = State.get()?.board;
  return collectDatedCards(board, { includeDone: _filter.showDone });
}

/* Alle terminierten Entries der Quelle (Filter showDone ignoriert) — für
   stabile Owner-/Projekt-Listen unabhängig vom aktiven Filter. */
function allSourceEntries() {
  if (IS_SERVER_BUILD && _filter.crossBoard) return _remoteEntries || [];
  return collectDatedCards(State.get()?.board, { includeDone: true });
}

function visibleEntries() {
  let entries = baseEntries();
  if (_filter.owner) {
    entries = entries.filter(({ card }) =>
      card.owner === _filter.owner ||
      (Array.isArray(card.assignees) && card.assignees.includes(_filter.owner)));
  }
  if (IS_SERVER_BUILD && _filter.project) {
    entries = entries.filter(({ card }) => projectIdOf(card) === _filter.project);
  }
  if (IS_SERVER_BUILD) entries = entries.concat(terminEintraege(), schattenEintraege());
  return entries;
}

/* Karte #743 (ADR-0138 §4): Termine ohne Karte. Lokal aus `board.termine`,
   im Modus „alle Boards“ aus GET /me/termine. Ein Termin hat keinen Owner und
   kein Projekt: Mit Personen-Filter erscheinen nur Termine, die die Person
   unter `personen` nennen, mit Projekt-Filter keine. Erledigt ist ein Termin
   nie. */
function terminEintraege() {
  if (!IS_SERVER_BUILD) return [];
  let liste = _filter.crossBoard ? (_remoteTermine || []) : termineAlsEintraege(State.get()?.board);
  if (_filter.project) return [];
  if (_filter.owner) {
    liste = liste.filter(({ card }) => Array.isArray(card._termin.personen) && card._termin.personen.includes(_filter.owner));
  }
  return liste;
}

/* Karte #781 (ADR-0140 §6): Schatten interner Karten mit Datum — nur auf einem
   externen Board (die Daten kommen von GET /boards/{id}/schatten, dem Weg der
   Board-Ansicht), nur lesbar, ausgegraut. Im Modus „alle Boards“ gibt es sie
   nicht (/me/cards kennt sie nicht). Mit Projekt-Filter keine; mit
   Personen-Filter nur die, die die Person als Bearbeiter nennen. */
function schattenEintraege() {
  if (!IS_SERVER_BUILD || _filter.crossBoard || _filter.project) return [];
  let liste = schattenAlsKalenderEintraege(SchattenAussen.schatten());
  if (_filter.owner) {
    liste = liste.filter(({ card }) => Array.isArray(card._schatten.bearbeiter) && card._schatten.bearbeiter.includes(_filter.owner));
  }
  return liste;
}

function projectIdOf(card) {
  return card.project_id || card.projectId || '';
}

/* Distinct Owners/Assignees über ALLE terminierten Karten (Filter-stabil,
   unabhängig vom aktuellen Filter). */
function distinctOwners() {
  const set = new Set();
  for (const { card } of allSourceEntries()) {
    if (card.owner) set.add(card.owner);
    for (const a of card.assignees || []) set.add(a);
  }
  return [...set].sort();
}

/* ── Server-only-Datenquellen (#308 Etappe 5) ─────────────────────────── */

/* Projekt-Liste lazy laden (einmalig pro Öffnen). Fehler still: ohne Liste
   erscheint kein Projekt-Filter. */
async function loadProjects() {
  if (!IS_SERVER_BUILD || _projects !== null || !ClientsProjectsClient) return;
  _projects = []; // markiert „in Arbeit/leer", verhindert Doppel-Fetch
  const res = await ClientsProjectsClient.listProjects();
  if (res.ok && Array.isArray(res.body)) {
    _projects = res.body.map((p) => ({ id: p.id, name: p.name }));
    renderControls();
  }
}

/* Cross-Board-Karten vom Server holen und in {card}-Entries umformen, sodass
   cardsByDay/upcomingByDue/makeChip unverändert funktionieren. */
/* F-424 (Karte #355): Halbe Breite des Zeitfensters, das im Cross-Board-Modus
   vom Server geholt wird. ADR-0103 §Technik/Performance verlangt einen
   server-seitigen `dueDateRange`-Filter statt der vollständigen Übertragung;
   der Aufruf hier ging bis Sprint 54 ohne Bereich raus und bekam ALLE
   terminierten Karten ALLER zugänglichen Boards.

   Ein Jahr in beide Richtungen ist bewusst grosszügig gewählt: Ein enges
   Fenster (nur der sichtbare Monat) müsste bei JEDER Navigation neu laden —
   das tauscht eine grosse Übertragung gegen viele kleine und macht das
   Blättern spürbar zäh. Ein Jahr deckt normales Blättern vollständig ab und
   begrenzt die Nutzlast trotzdem auf einen Bruchteil. */
const REMOTE_FENSTER_TAGE = 365;

/* Ab diesem Abstand zum Fensterrand wird nachgeladen. 60 Tage geben zwei
   Monatssprünge Vorlauf — der Nachladevorgang beginnt also, bevor der Nutzer
   den Rand tatsächlich sieht. */
const REMOTE_NACHLADE_ABSTAND = 60;

/* Das aktuell geladene Fenster, {from, to} als ISO. null = nichts geladen. */
let _remoteRange = null;

/* Generations-Marke gegen Antworten in falscher Reihenfolge. Ohne sie
   entscheidet bei schneller Navigation die Reihenfolge der ANTWORTEN, welcher
   Datensatz stehen bleibt — dieselbe Klasse wie F-238/F-164 an anderer
   Stelle, hier mit dem Ergebnis, dass der Kalender Karten eines Zeitraums
   zeigt, den der Nutzer längst verlassen hat. */
let _remoteGeneration = 0;

function remoteFensterFuer(anchorISO) {
  const mitte = anchorISO || todayISO();
  return { from: addDays(mitte, -REMOTE_FENSTER_TAGE), to: addDays(mitte, REMOTE_FENSTER_TAGE) };
}

/* Reicht das geladene Fenster für diesen Anker noch — mit Sicherheitsabstand?
   ISO-Datumsstrings sind lexikografisch vergleichbar, deshalb genügt `<`/`>`. */
function remoteFensterReicht(anchorISO) {
  if (!_remoteRange) return false;
  const mitte = anchorISO || todayISO();
  return addDays(mitte, -REMOTE_NACHLADE_ABSTAND) >= _remoteRange.from
      && addDays(mitte,  REMOTE_NACHLADE_ABSTAND) <= _remoteRange.to;
}

/* Nach einer Navigation aufrufen: lädt nur nach, wenn das Fenster nicht mehr
   reicht. Im Regelfall ein reiner Vergleich zweier Zeichenketten. */
function ensureRemoteFenster() {
  if (!IS_SERVER_BUILD || !_filter.crossBoard) return;
  if (_remoteLoading) return;
  if (remoteFensterReicht(_anchor)) return;
  loadRemoteCards();
}

async function loadRemoteCards() {
  if (!IS_SERVER_BUILD || !ClientsProjectsClient) return;
  const gen = ++_remoteGeneration;
  const fenster = remoteFensterFuer(_anchor);
  _remoteLoading = true;
  _remoteError = false;
  renderBody();
  /* Karte #743: Termine desselben Fensters parallel. Ein Fehlschlag dort
     lässt die Karten stehen — der Kalender zeigt sie dann ohne Termine. */
  const [res, resT] = await Promise.all([
    ClientsProjectsClient.listMyCards({ dueDateRange: `${fenster.from}~${fenster.to}` }),
    ClientsProjectsClient.listMyTermine?.({ dueDateRange: `${fenster.from}~${fenster.to}` }),
  ]);
  /* Überholt — eine neuere Anfrage ist unterwegs oder schon angekommen.
     Ergebnis verwerfen, und `_remoteLoading` NICHT zurücksetzen: das gehört
     der laufenden Anfrage, sonst verschwindet deren Ladeanzeige zu früh. */
  if (gen !== _remoteGeneration) return;
  _remoteLoading = false;
  _remoteTermine = (resT && resT.ok && Array.isArray(resT.body))
    ? resT.body.filter(istAnzeigbarerTermin).map((t) =>
      terminAlsEintrag(t, { _crossBoard: true, _boardId: t.board_id, _boardTitle: t.board_title }))
    : [];
  if (res.ok && Array.isArray(res.body)) {
    _remoteRange = fenster;
    _remoteEntries = res.body.map((c) => ({
      sprintId: null,
      card: {
        id: c.card_id,
        displayId: c.display_id,
        title: c.title,
        columnId: c.column_id,
        dueDate: c.dueDate,
        startDate: c.startDate || null,
        dueTime: c.dueTime || null,
        startTime: c.startTime || null,
        owner: c.owner || '',
        assignees: c.assignees || [],
        project_id: c.project_id || '',
        _crossBoard: true,
        _boardId: c.board_id,
        _boardTitle: c.board_title,
      },
    }));
  } else {
    _remoteEntries = [];
    _remoteError = true;
    /* Kein gültiges Fenster nach einem Fehlschlag — sonst gälte der nächste
       Blick auf denselben Zeitraum als bereits geladen und der Kalender
       bliebe dauerhaft leer, ohne es erneut zu versuchen. */
    _remoteRange = null;
  }
  renderBody();
}

/* ── Navigation ───────────────────────────────────────────────────────── */

function setAnchor(iso) {
  _anchor = iso;
  syncHash();
  /* F-424: Verlässt die Navigation das geladene Fenster, wird nachgeladen.
     Im Regelfall ein Zeichenketten-Vergleich ohne Netzzugriff. */
  ensureRemoteFenster();
  renderBody();
}

function shiftUnit(delta) {
  if (_view === 'month') {
    const { year, month } = anchorParts();
    const idx = (year * 12 + (month - 1)) + delta;
    setAnchor(toISO(Math.floor(idx / 12), (idx % 12) + 1, 1));
  } else {
    // Woche/Agenda: um eine Woche verschieben.
    setAnchor(addDays(_anchor || todayISO(), delta * 7));
  }
}

function switchView(view) {
  if (view === _view) return;
  _view = view;
  if (!_anchor) _anchor = todayISO();
  syncHash();
  renderBody();
}

/* ── URL-Hash-Routing ─────────────────────────────────────────────────
   #/calendar?view=month&date=YYYY-MM  (Monat)
   #/calendar?view=week|agenda&date=YYYY-MM-DD  (Woche/Agenda). */

function syncHash() {
  const { year, month } = anchorParts();
  const date = _view === 'month'
    ? `${String(year).padStart(4, '0')}-${String(month).padStart(2, '0')}`
    : (_anchor || todayISO());
  const hash = `#/calendar?view=${_view}&date=${date}`;
  if (location.hash !== hash) {
    try { history.replaceState(null, '', hash); } catch { /* sandbox */ }
  }
}

/** Parst `#/calendar?...` → `{view, anchor}` oder null, wenn kein Kalender-Hash.
 *  `anchor` ist ein ISO-Tag (YYYY-MM-DD) oder null bei fehlendem/ungültigem Datum. */
export function parseCalendarHash(hash = location.hash) {
  if (!hash || !hash.startsWith('#/calendar')) return null;
  const q = hash.indexOf('?');
  const params = new URLSearchParams(q >= 0 ? hash.slice(q + 1) : '');
  const v = params.get('view');
  const view = (v === 'week' || v === 'agenda') ? v : 'month';
  const date = params.get('date') || '';
  let anchor = null;
  const full = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  const ym = /^(\d{4})-(\d{2})$/.exec(date);
  if (full && parseISO(date)) anchor = date;
  else if (ym && Number(ym[2]) >= 1 && Number(ym[2]) <= 12) anchor = `${ym[1]}-${ym[2]}-01`;
  return { view, anchor };
}

/** Beim App-Start / hashchange: Kalender-Hash → Overlay öffnen + Zustand setzen. */
export function maybeRouteFromHash() {
  const parsed = parseCalendarHash();
  if (!parsed) return false;
  _view = parsed.view;
  if (parsed.anchor) _anchor = parsed.anchor;
  open({ skipHash: true });
  return true;
}

/* ── Shell ────────────────────────────────────────────────────────────── */

function buildShell() {
  _overlay = el('div', { cls: 'cal-overlay', attrs: { 'aria-hidden': 'true' } });
  _panel = el('div', { cls: 'cal-panel', attrs: { role: 'dialog', 'aria-modal': 'true', 'aria-label': I18n.t('calendar.title') } });

  const header = el('div', { cls: 'cal-header' });
  const nav = el('div', { cls: 'cal-nav' });
  const prev = el('button', { cls: 'cal-nav-btn', text: '‹', attrs: { 'aria-label': I18n.t('calendar.prev') } });
  prev.addEventListener('click', () => shiftUnit(-1));
  const today = el('button', { cls: 'cal-nav-btn cal-today-btn', text: I18n.t('calendar.today') });
  today.addEventListener('click', () => setAnchor(todayISO()));
  const next = el('button', { cls: 'cal-nav-btn', text: '›', attrs: { 'aria-label': I18n.t('calendar.next') } });
  next.addEventListener('click', () => shiftUnit(1));
  nav.appendChild(prev); nav.appendChild(today); nav.appendChild(next);
  _headerLabel = el('h2', { cls: 'cal-title' });
  header.appendChild(nav);
  header.appendChild(_headerLabel);

  /* iCal-Abo-Button (#308 Etappe 6, server-only). */
  if (IS_SERVER_BUILD) {
    const icalBtn = el('button', { cls: 'cal-nav-btn cal-ical-btn', text: '🔗 ' + I18n.t('calendar.ical.subscribe'), attrs: { 'aria-label': I18n.t('calendar.ical.subscribe') } });
    icalBtn.addEventListener('click', () => openICalPanel());
    header.appendChild(icalBtn);
    /* Karte #743: Termin ohne Karte anlegen — im aktuellen Board. */
    const terminBtn = el('button', { cls: 'cal-nav-btn cal-termin-btn', text: I18n.t('termin.neu') });
    terminBtn.addEventListener('click', () => openTerminDialog(null, _anchor || todayISO()));
    header.appendChild(terminBtn);
  }

  const closeBtn = el('button', { cls: 'cal-close', text: '✕', attrs: { 'aria-label': I18n.t('btn.close-modal') } });
  closeBtn.addEventListener('click', () => close());
  header.appendChild(closeBtn);
  _panel.appendChild(header);

  _controlsEl = el('div', { cls: 'cal-controls' });
  _panel.appendChild(_controlsEl);

  _bodyEl = el('div', { cls: 'cal-body' });
  _panel.appendChild(_bodyEl);

  _overlay.appendChild(_panel);
  _overlay.addEventListener('click', (e) => { if (e.target === _overlay) close(); });
}

function renderControls() {
  _controlsEl.innerHTML = '';

  /* View-Tabs */
  const tabs = el('div', { cls: 'cal-tabs', attrs: { role: 'tablist' } });
  for (const v of ['month', 'week', 'agenda']) {
    const btn = el('button', {
      cls: 'cal-tab' + (v === _view ? ' cal-tab-active' : ''),
      text: I18n.t('calendar.view.' + v),
      attrs: { role: 'tab', 'aria-selected': v === _view ? 'true' : 'false' },
    });
    btn.addEventListener('click', () => switchView(v));
    tabs.appendChild(btn);
  }
  _controlsEl.appendChild(tabs);

  /* Filter */
  const filters = el('div', { cls: 'cal-filters' });

  const doneLabel = el('label', { cls: 'cal-filter-done' });
  const doneCb = el('input', { attrs: { type: 'checkbox' } });
  doneCb.checked = _filter.showDone;
  doneCb.addEventListener('change', () => { _filter.showDone = doneCb.checked; renderBody(); });
  doneLabel.appendChild(doneCb);
  doneLabel.appendChild(el('span', { text: I18n.t('calendar.filter.done') }));
  filters.appendChild(doneLabel);

  const owners = distinctOwners();
  if (owners.length) {
    const sel = el('select', { cls: 'cal-owner-select', attrs: { 'aria-label': I18n.t('calendar.filter.owner') } });
    const optAll = el('option', { text: I18n.t('calendar.all-owners'), attrs: { value: '' } });
    sel.appendChild(optAll);
    for (const o of owners) {
      const opt = el('option', { text: o, attrs: { value: o } });
      if (o === _filter.owner) opt.setAttribute('selected', 'selected');
      sel.appendChild(opt);
    }
    sel.value = _filter.owner;
    sel.addEventListener('change', () => { _filter.owner = sel.value; renderBody(); });
    filters.appendChild(sel);
  }

  /* Projekt-Filter (#307, server-only Datenquelle). */
  if (IS_SERVER_BUILD && Array.isArray(_projects) && _projects.length) {
    const psel = el('select', { cls: 'cal-owner-select', attrs: { 'aria-label': I18n.t('calendar.filter.project') } });
    psel.appendChild(el('option', { text: I18n.t('calendar.all-projects'), attrs: { value: '' } }));
    for (const p of _projects) {
      psel.appendChild(el('option', { text: p.name, attrs: { value: p.id } }));
    }
    psel.value = _filter.project;
    psel.addEventListener('change', () => { _filter.project = psel.value; renderBody(); });
    filters.appendChild(psel);
  }

  /* Cross-Board-Toggle (#308 Etappe 5, server-only): alle meine Karten über
     alle Boards. Schaltet die Datenquelle von State.board auf GET /me/cards. */
  if (IS_SERVER_BUILD) {
    /* Eigene Klasse neben der geerbten: `cal-filter-done` stammt aus dem
       Erledigt-Filter darüber und war hier nur mitkopiert — zwei Filter mit
       demselben Namen sind im DOM nicht auseinanderzuhalten (weder für CSS
       noch für Tests). Die alte Klasse bleibt, damit bestehende Regeln
       greifen. */
    const cbLabel = el('label', { cls: 'cal-filter-done cal-filter-crossboard' });
    const cbCb = el('input', { attrs: { type: 'checkbox' } });
    cbCb.checked = _filter.crossBoard;
    cbCb.addEventListener('change', () => {
      _filter.crossBoard = cbCb.checked;
      if (_filter.crossBoard) {
        if (_remoteEntries === null && !_remoteLoading) loadRemoteCards();
        else renderBody();
      } else {
        renderBody();
      }
    });
    cbLabel.appendChild(cbCb);
    cbLabel.appendChild(el('span', { text: I18n.t('calendar.filter.cross-board') }));
    filters.appendChild(cbLabel);
  }

  _controlsEl.appendChild(filters);
}

/* ── Rendering ────────────────────────────────────────────────────────── */

function renderBody() {
  if (!_bodyEl) return;
  renderControls();
  _bodyEl.innerHTML = '';
  /* Cross-Board-Lade-/Fehlerzustand (#308 Etappe 5). */
  if (IS_SERVER_BUILD && _filter.crossBoard) {
    if (_remoteLoading) {
      _bodyEl.appendChild(el('div', { cls: 'cal-empty', text: I18n.t('calendar.cross-board-loading') }));
      return;
    }
    if (_remoteError) {
      _bodyEl.appendChild(el('div', { cls: 'cal-empty', text: I18n.t('calendar.cross-board-error') }));
      return;
    }
  }
  if (_view === 'week') {
    _headerLabel.textContent = weekTitle();
    _bodyEl.appendChild(renderWeek());
  } else if (_view === 'agenda') {
    _headerLabel.textContent = I18n.t('calendar.view.agenda');
    _bodyEl.appendChild(renderAgenda());
  } else {
    const { year, month } = anchorParts();
    _headerLabel.textContent = monthTitle(year, month);
    _bodyEl.appendChild(renderMonth(year, month));
  }
}

function makeChip(card, fromISO) {
  if (IS_SERVER_BUILD && card._termin) return terminChip(card, fromISO);
  if (IS_SERVER_BUILD && card._schatten) return schattenChip(card, fromISO);
  /* Cross-Board-Karten (#308 Etappe 5) sind read-only: sie liegen auf anderen
     Boards → kein DnD, kein Quick-Edit (Board-Wechsel ist eigener Scope). Der
     Board-Name wandert in Präfix + Tooltip. */
  const cross = !!card._crossBoard;
  /* Kritik K-3 (Karte #826): Spiegelkarten der externen Kopie nicht verschieben —
     Karte #827 E4: nur, solange der Rückweg aus ist (Datum gehört zur Feldliste). */
  const fest = cross || (IS_SERVER_BUILD && spiegelModus(card) === 'lesen');
  const idStr = card.displayId != null ? `#${card.displayId} ` : '';
  const boardPrefix = cross && card._boardTitle ? `[${card._boardTitle}] ` : '';
  /* ADR-0138 §1: Uhrzeit vorn am Eintrag */
  const zeit = uhrzeitAmTag(card, fromISO).label;
  const zeitStr = zeit ? `${zeit} ` : '';
  const chip = el('button', {
    cls: 'cal-chip' + (karteIstErledigt(card) ? ' cal-chip-done' : '') + (cross ? ' cal-chip-cross' : ''),
    attrs: {
      title: `${zeitStr}${boardPrefix}${idStr}${card.title || ''}`.trim(),
      draggable: fest ? 'false' : 'true',
    },
  });
  chip.textContent = zeitStr + boardPrefix + idStr + (card.title || '');
  if (!cross) {
    chip.addEventListener('click', (e) => {
      e.stopPropagation();
      close();
      EventBus.emit('card:edit', card);
    });
    /* Etappe 4: Drag-and-Drop → dueDate verschieben. `fromISO` ist der Tag, von
       dem aus gezogen wird (bei Range-Karten relevant fürs Delta). */
    if (!fest) chip.addEventListener('dragstart', (e) => {
      e.dataTransfer.setData('text/plain', JSON.stringify({ cardId: card.id, fromISO }));
      e.dataTransfer.effectAllowed = 'move';
    });
  } else {
    chip.addEventListener('click', (e) => e.stopPropagation());
  }
  return chip;
}

/* ── Drag-and-Drop + Quick-Add (Etappe 4) ─────────────────────────────── */

function findCardById(id) {
  const board = State.get()?.board;
  for (const s of board?.sprints ?? []) {
    const c = (s.cards ?? []).find((x) => x.id === id);
    if (c) return c;
  }
  return null;
}

function applyDrop(cardId, fromISO, toISO) {
  if (fromISO === toISO) return;
  const card = findCardById(cardId);
  if (!card) return;
  if (IS_SERVER_BUILD && spiegelModus(card) === 'lesen') return; /* Kritik K-3: auch per anderem Ziehweg nicht */
  const { dueDate, startDate } = shiftCardDates(card, fromISO, toISO);
  EventBus.emit('card:updated', { ...card, dueDate, startDate });
  renderBody();
}

function attachDayInteractions(node, dayISO) {
  /* Cross-Board-Modus (#308 Etappe 5): nur Lese-Ansicht — kein Drop-Target,
     kein Quick-Add (Karten liegen auf fremden Boards). */
  if (IS_SERVER_BUILD && _filter.crossBoard) return;
  node.addEventListener('dragover', (e) => { e.preventDefault(); node.classList.add('cal-drop-hover'); });
  node.addEventListener('dragleave', () => node.classList.remove('cal-drop-hover'));
  node.addEventListener('drop', (e) => {
    e.preventDefault();
    node.classList.remove('cal-drop-hover');
    let data = null;
    try { data = JSON.parse(e.dataTransfer.getData('text/plain')); } catch { /* fremder Drop */ }
    if (data && data.cardId && data.fromISO) applyDrop(data.cardId, data.fromISO, dayISO);
  });
  node.addEventListener('click', (e) => {
    if (e.target.closest('.cal-chip') || e.target.closest('.cal-quickadd')) return;
    openQuickAdd(node, dayISO);
  });
}

function openQuickAdd(dayCell, dayISO) {
  if (dayCell.querySelector('.cal-quickadd')) return;
  /* Karte #826: Auf einer externen Kopie ist der Backlog gespiegelt — eine
     neue Karte dort wiese der Server ab (ADR-0141 §7). Karte #827 E4: bei
     Rückweg an nicht, wenn die Zielspalte gespiegelt ist (die Karte wartet
     dann auf ihre Marke und geht nach innen). */
  if (IS_SERVER_BUILD) {
    const b = State.get()?.board;
    /* F-2233 (#831): markeWirkt — auf einer außen entkoppelten Kopie nicht. */
    const backlogGespiegelt = markeWirkt((b?.sprints ?? []).find(s => s.id === '__backlog__'));
    const spalteGespiegelt = markeWirkt((b?.columnDefs ?? []).find(c => c.id === 'col-backlog'));
    if (backlogGespiegelt && !(rueckwegAn() && spalteGespiegelt)) return;
  }
  const form = el('div', { cls: 'cal-quickadd' });
  const inp = el('input', { attrs: { type: 'text', placeholder: I18n.t('calendar.quick-add') } });
  form.appendChild(inp);
  dayCell.appendChild(form);
  inp.focus();
  const submit = () => {
    const title = inp.value.trim();
    if (!title) { form.remove(); return; }
    /* Neue Kalender-Karte → Backlog/col-backlog, dueDate = angeklickter Tag. */
    EventBus.emit('card:quick-add', { sprintId: '__backlog__', colId: 'col-backlog', title, dueDate: dayISO });
    renderBody();
  };
  inp.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); submit(); }
    else if (e.key === 'Escape') { e.stopPropagation(); form.remove(); } // nicht das Overlay schließen
  });
  inp.addEventListener('blur', () => { if (!inp.value.trim()) form.remove(); });
}

function renderMonth(year, month) {
  const wrap = el('div', { cls: 'cal-month' });

  const head = el('div', { cls: 'cal-grid cal-grid-head' });
  head.appendChild(el('div', { cls: 'cal-kw-head', text: I18n.t('calendar.kw') }));
  for (const w of weekdayLabels()) head.appendChild(el('div', { cls: 'cal-wd', text: w }));
  wrap.appendChild(head);

  const byDay = cardsByDay(visibleEntries());
  const today = todayISO();
  const grid = monthGrid(year, month);

  for (let i = 0; i < grid.length; i += 7) {
    const row = el('div', { cls: 'cal-grid cal-grid-row' });
    const wk = getISOWeek(grid[i]);
    row.appendChild(el('div', { cls: 'cal-kw', text: String(wk.week) }));
    for (let j = 0; j < 7; j++) {
      const iso = grid[i + j];
      row.appendChild(renderDay(iso, year, month, today, byDay.get(iso) || []));
    }
    wrap.appendChild(row);
  }
  return wrap;
}

function renderDay(iso, year, month, today, entries) {
  const p = parseISO(iso);
  const cls = ['cal-day'];
  if (!isInMonth(iso, year, month)) cls.push('cal-day-out');
  if (isWeekend(iso)) cls.push('cal-day-we');
  if (iso === today) cls.push('cal-day-today');
  const day = el('div', { cls: cls.join(' '), attrs: { 'data-iso': iso } });
  day.appendChild(el('div', { cls: 'cal-day-num', text: String(p.day) }));
  const list = el('div', { cls: 'cal-day-cards' });
  for (const { card } of entries) list.appendChild(makeChip(card, iso));
  day.appendChild(list);
  attachDayInteractions(day, iso);
  return day;
}

function weekTitle() {
  const days = weekDays(_anchor || todayISO());
  const wk = getISOWeek(days[0]);
  const a = parseISO(days[0]);
  const b = parseISO(days[6]);
  const fmt = (p) => new Intl.DateTimeFormat(locale(), { day: 'numeric', month: 'short' }).format(new Date(p.year, p.month - 1, p.day));
  return `${I18n.t('calendar.kw')} ${wk.week} · ${fmt(a)} – ${fmt(b)}`;
}

function renderWeek() {
  const wrap = el('div', { cls: 'cal-week' });
  const days = weekDays(_anchor || todayISO());
  const byDay = cardsByDay(visibleEntries());
  const today = todayISO();
  const labels = weekdayLabels();
  for (let i = 0; i < 7; i++) {
    const iso = days[i];
    const cls = ['cal-week-col'];
    if (isWeekend(iso)) cls.push('cal-day-we');
    if (iso === today) cls.push('cal-day-today');
    const col = el('div', { cls: cls.join(' '), attrs: { 'data-iso': iso } });
    const head = el('div', { cls: 'cal-week-head' });
    head.appendChild(el('span', { cls: 'cal-week-wd', text: labels[i] }));
    head.appendChild(el('span', { cls: 'cal-week-num', text: String(parseISO(iso).day) }));
    col.appendChild(head);
    const list = el('div', { cls: 'cal-day-cards' });
    for (const { card } of (byDay.get(iso) || [])) list.appendChild(makeChip(card, iso));
    col.appendChild(list);
    attachDayInteractions(col, iso);
    wrap.appendChild(col);
  }
  return wrap;
}

function renderAgenda() {
  const wrap = el('div', { cls: 'cal-agenda' });
  const from = _anchor || todayISO();
  const entries = visibleEntries();
  /* Karte #743: Termine stehen in der Agenda an ihrem ERSTEN Tag (Karten an
     ihrer Frist) — deshalb getrennt einsortiert. */
  let items = upcomingByDue(IS_SERVER_BUILD ? entries.filter(({ card }) => !card._termin && !card._schatten) : entries, from, AGENDA_DAYS);
  if (IS_SERVER_BUILD) items = mitTerminenInAgenda(items, entries, from);
  if (!items.length) {
    wrap.appendChild(el('div', { cls: 'cal-empty', text: I18n.t('calendar.agenda-empty') }));
    return wrap;
  }
  let lastDay = null;
  for (const { card } of items) {
    if (IS_SERVER_BUILD && (card._termin || card._schatten)) {
      if (card.startDate !== lastDay) {
        lastDay = card.startDate;
        wrap.appendChild(el('div', { cls: 'cal-agenda-day', text: dayLongLabel(card.startDate) }));
      }
      wrap.appendChild(card._schatten ? schattenAgendaZeile(card) : terminAgendaZeile(card));
      continue;
    }
    if (card.dueDate !== lastDay) {
      lastDay = card.dueDate;
      wrap.appendChild(el('div', { cls: 'cal-agenda-day', text: dayLongLabel(card.dueDate) }));
    }
    const row = el('button', { cls: 'cal-agenda-row' + (karteIstErledigt(card) ? ' cal-chip-done' : '') });
    const idStr = card.displayId != null ? `#${card.displayId} ` : '';
    const zeit = uhrzeitAmTag(card, card.dueDate).label;
    row.textContent = (zeit ? `${zeit} ` : '') + idStr + (card.title || '');
    row.addEventListener('click', () => { close(); EventBus.emit('card:edit', card); });
    wrap.appendChild(row);
  }
  return wrap;
}

/* ── Termine ohne Karte (Karte #743, ADR-0138 §4; server-only) ──────────── */

/* Agenda: Karten nach Frist (upcomingByDue), Termine nach erstem Tag — je Tag
   ganztags zuerst, dann nach Uhrzeit. Stabil, die Karten-Reihenfolge bleibt. */
function mitTerminenInAgenda(items, entries, from) {
  if (!IS_SERVER_BUILD) return items;
  const ende = addDays(from, AGENDA_DAYS);
  const termine = entries.filter(({ card }) => (card._termin || card._schatten) && card.startDate >= from && card.startDate < ende);
  if (!termine.length) return items;
  const tag = (c) => (c._termin || c._schatten ? c.startDate : c.dueDate);
  const zeit = (c) => uhrzeitAmTag(c, tag(c)).sort || '';
  const cmp = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
  return [...items, ...termine].sort((a, b) => cmp(tag(a.card), tag(b.card)) || cmp(zeit(a.card), zeit(b.card)));
}

/* Text eines Termins: Uhrzeit vorn (ADR-0138 §1), dann das Termin-Symbol,
   bei Cross-Board der Board-Name. */
function terminText(card, iso) {
  if (!IS_SERVER_BUILD) return '';
  const zeit = uhrzeitAmTag(card, iso).label;
  const board = card._crossBoard && card._boardTitle ? `[${card._boardTitle}] ` : '';
  return (zeit ? `${zeit} ` : '') + '📅 ' + board + (card.title || '');
}

function terminChip(card, fromISO) {
  if (!IS_SERVER_BUILD) return null;
  const text = terminText(card, fromISO);
  const ort = card._termin.ort ? ` · ${card._termin.ort}` : '';
  const chip = el('button', {
    cls: 'cal-chip cal-chip-termin' + (card._crossBoard ? ' cal-chip-cross' : ''),
    text, attrs: { title: I18n.t('termin.symbol-titel') + ': ' + text + ort, draggable: 'false' },
  });
  chip.addEventListener('click', (e) => { e.stopPropagation(); oeffneTerminEintrag(card); });
  return chip;
}

function terminAgendaZeile(card) {
  if (!IS_SERVER_BUILD) return null;
  const row = el('button', { cls: 'cal-agenda-row cal-agenda-termin', text: terminText(card, card.startDate) });
  row.addEventListener('click', () => oeffneTerminEintrag(card));
  return row;
}

/* Schatten (Karte #781): ausgegraut, nur lesbar, nicht ziehbar. Klick zeigt den
   Hinweis „Diese Karte ist intern …“ — nie einen Dialog zum Bearbeiten. */
function schattenText(card, iso) {
  if (!IS_SERVER_BUILD) return '';
  const zeit = uhrzeitAmTag(card, iso).label;
  const e = card._schatten;
  return (zeit ? `${zeit} ` : '') + (e.von_agent ? '🤖 ' : '') + (card.title || '');
}

function schattenChip(card, fromISO) {
  if (!IS_SERVER_BUILD) return null;
  SchattenAussen.stile();
  const text = schattenText(card, fromISO);
  const chip = el('button', {
    cls: 'cal-chip cal-chip-schatten',
    text, attrs: { title: I18n.t('schatten.zeile-titel') + ': ' + text, draggable: 'false' },
  });
  chip.addEventListener('click', (e) => { e.stopPropagation(); SchattenAussen.klick(card._schatten, chip); });
  return chip;
}

function schattenAgendaZeile(card) {
  if (!IS_SERVER_BUILD) return null;
  SchattenAussen.stile();
  const row = el('button', { cls: 'cal-agenda-row cal-agenda-schatten', text: schattenText(card, card.startDate),
    attrs: { title: I18n.t('schatten.zeile-titel'), draggable: 'false' } });
  row.addEventListener('click', () => SchattenAussen.klick(card._schatten, row));
  return row;
}

/* Ein Termin auf dem offenen Board öffnet den Dialog. Einer auf einem
   anderen Board (Modus „alle Boards“) öffnet erst dieses Board, über den
   Deep-Link — derselbe Weg wie `#board=…&card=…` (src/main.js). */
function oeffneTerminEintrag(card) {
  if (!IS_SERVER_BUILD) return;
  const aktuell = State.get()?.board?.id;
  if (card._crossBoard && card._boardId && card._boardId !== aktuell) {
    close();
    location.hash = `#board=${encodeURIComponent(card._boardId)}&termin=${encodeURIComponent(card._termin.id)}`;
    return;
  }
  /* Kein Rückgriff auf den Stand im Eintrag: Fehlt der Termin auf dem offenen
     Board, ist er inzwischen gelöscht. Ein Dialog mit dem alten Stand legte
     ihn beim Speichern wieder an (Kritiker 07.10., K-1). */
  const t = findeTermin(State.get()?.board, card._termin.id);
  if (!t) { fuehreRemoteNach(aktuell, card._termin.id, null); renderBody(); return; }
  openTerminDialog(t);
}

/* Modus „alle Boards“: Die Liste kommt vom Server (`/me/termine`) und kennt
   eigene Änderungen erst beim nächsten Laden. Speichern und Löschen auf dem
   offenen Board führen sie deshalb sofort nach (Kritiker 07.10., K-1); ein
   Neuladen käme vor dem Eintreffen der Op am Server und sähe den alten Stand. */
function fuehreRemoteNach(boardId, id, termin) {
  if (!Array.isArray(_remoteTermine) || !boardId) return;
  _remoteTermine = _remoteTermine.filter(({ card }) => !(card._boardId === boardId && card._termin?.id === id));
  if (termin && istAnzeigbarerTermin(termin)) {
    _remoteTermine.push(terminAlsEintrag(termin,
      { _crossBoard: true, _boardId: boardId, _boardTitle: State.get()?.board?.title || '' }));
  }
}

/** Deep-Link `#board=<id>&termin=<id>` (src/main.js): Kalender auf dem Tag
 *  des Termins öffnen und den Termin zeigen. false, wenn es ihn hier nicht gibt
 *  (dann bleibt der Kalender offen, ohne Dialog). Eigener Export statt eines
 *  Felds in `CalendarView`: nur so fällt er im Solo-Template weg. */
export function oeffneTermin(id) {
  if (!IS_SERVER_BUILD) return false;
  const t = findeTermin(State.get()?.board, id);
  if (t && istAnzeigbarerTermin(t)) _anchor = t.datum;
  open();
  if (!t || !istAnzeigbarerTermin(t)) return false;
  openTerminDialog(t);
  return true;
}

function ensureTerminStyles() {
  if (document.getElementById('cal-termin-styles')) return;
  const st = document.createElement('style');
  st.id = 'cal-termin-styles';
  st.textContent = '.cal-chip-termin{background:var(--surface);color:var(--text);border:1px solid var(--accent)}.cal-agenda-termin{font-style:italic}.cal-termin-form{display:grid;grid-template-columns:auto 1fr;gap:6px 10px;align-items:center;padding:10px 16px}.cal-termin-form label{font-size:12px;color:var(--text-muted)}.cal-termin-form input,.cal-termin-form textarea{font:inherit;font-size:13px;padding:4px 6px;background:var(--surface-2);border:1px solid var(--border);border-radius:3px;color:inherit;min-width:0}.cal-termin-form textarea{min-height:70px;resize:vertical}.cal-termin-fehler{color:var(--danger,#e5534b);font-size:12px;padding:0 16px;margin:0;min-height:1em}';
  document.head.appendChild(st);
}

/* Schlichter Dialog: Titel, Datum, bis-Datum, von/bis, Ort, Beschreibung.
   Speichern und Löschen laufen über den EventBus nach src/main.js
   (`termin:speichern`, `termin:loeschen`) und von dort als Ops zum Server. */
function openTerminDialog(termin, datumVorgabe) {
  if (!IS_SERVER_BUILD) return;
  ensureICalStyles();
  ensureTerminStyles();
  /* Karte #826 (ADR-0141 §7): Spiegeltermin der externen Kopie — nur lesen.
     Karte #827 E4: bei Rückweg an bearbeitbar, aber nicht löschbar und
     „vertraulich“ nur einschaltbar (terminModus). */
  const modus = terminModus(termin);
  const nurLesen = modus === 'lesen';
  const rueckweg = modus === 'rueckweg';
  const titel = I18n.t(termin ? 'termin.bearbeiten' : 'termin.neu-titel');
  /* F-1892b: Fokus-Rückgabe — dorthin, von wo der Dialog geöffnet wurde. */
  const vorher = captureFocus();
  const ov = el('div', { cls: 'cal-ical-overlay' });
  const pan = el('div', { cls: 'cal-ical-panel cal-termin-panel', attrs: { role: 'dialog', 'aria-modal': 'true', 'aria-label': titel } });
  const head = el('div', { cls: 'cal-ical-head' });
  head.appendChild(el('h3', { cls: 'cal-ical-title', text: titel }));
  const x = el('button', { cls: 'cal-close', text: '✕', attrs: { 'aria-label': I18n.t('btn.close-modal') } });
  head.appendChild(x);
  pan.appendChild(head);

  const form = el('div', { cls: 'cal-termin-form' });
  const felder = {};
  const feld = (name, typ, wert) => {
    const id = 'cal-termin-' + name;
    form.appendChild(el('label', { text: I18n.t('termin.feld.' + name), attrs: { for: id } }));
    const inp = el(typ === 'textarea' ? 'textarea' : 'input', { attrs: typ === 'textarea' ? { id } : { id, type: typ } });
    inp.value = wert || '';
    form.appendChild(inp);
    felder[name] = inp;
  };
  feld('titel', 'text', termin?.titel);
  feld('datum', 'date', termin?.datum || datumVorgabe || todayISO());
  feld('bisdatum', 'date', termin?.bisDatum);
  feld('von', 'time', termin?.von);
  feld('bis', 'time', termin?.bis);
  feld('ort', 'text', termin?.ort);
  feld('beschreibung', 'textarea', termin?.beschreibung);
  /* ADR-0140 §10 (Karte #778): Schalter „vertraulich“ — nur auf der Seite
     „innen“ eines Board-Paars, oder wenn der Termin ihn schon trägt. Gesetzt
     wird er nur, wenn er bedient wurde; sonst bleibt der Bestand (der Termin
     wird oben mit allen unbekannten Feldern übernommen). */
  const vertraulichAnfang = istVertraulich(termin);
  const vertraulichCb = el('input', { attrs: { type: 'checkbox', id: 'cal-termin-vertraulich' } });
  vertraulichCb.checked = vertraulichAnfang;
  const vertraulichLbl = el('label', { text: I18n.t('card.confidential'), attrs: { for: 'cal-termin-vertraulich', title: I18n.t('card.confidential.hint') } });
  const vertraulichRow = el('div', { cls: 'cal-termin-vertraulich' });
  vertraulichRow.appendChild(vertraulichCb);
  vertraulichRow.appendChild(vertraulichLbl);
  vertraulichRow.hidden = !vertraulichAnfang && !rueckweg;
  form.appendChild(vertraulichRow);
  if (!vertraulichAnfang && !rueckweg) boardpaarSeite().then(seite => { vertraulichRow.hidden = seite !== 'innen'; });
  if (nurLesen) {
    form.querySelectorAll('input, textarea').forEach(e => { e.disabled = true; });
    pan.appendChild(el('p', { cls: 'cal-ical-hint', text: I18n.t('termin.spiegel.nur-lesen'), attrs: { role: 'note' } }));
  }
  if (rueckweg) {
    /* Zurück auf „nicht vertraulich“ geht nur innen. */
    if (vertraulichAnfang) vertraulichCb.disabled = true;
    pan.appendChild(el('p', { cls: 'cal-ical-hint', text: I18n.t('termin.spiegel.rueckweg'), attrs: { role: 'note' } }));
  }
  pan.appendChild(form);

  const info = [];
  const board = State.get()?.board;
  if (board?.title) info.push(I18n.t('termin.board', board.title));
  if (termin?.erstelltVon) info.push(I18n.t('termin.herkunft', termin.erstelltVon, termin.erstellt || ''));
  if (info.length) pan.appendChild(el('p', { cls: 'cal-ical-hint', text: info.join(' · ') }));
  const fehlerEl = el('p', { cls: 'cal-termin-fehler', attrs: { role: 'alert' } });
  pan.appendChild(fehlerEl);

  /* F-1892b: Fokus-Fang — Tab und Shift+Tab bleiben im Dialog (aria-modal
     verspricht das); steht der Fokus außerhalb, holt Tab ihn zurück. Hängt
     der Dialog nicht mehr im DOM (ohne schliessen() entfernt), meldet sich
     der Handler ab, statt Tab im ganzen Board abzufangen. */
  const onKey = (e) => {
    if (!ov.isConnected) { document.removeEventListener('keydown', onKey, true); return; }
    if (e.key === 'Escape') { e.stopPropagation(); schliessen(); return; }
    if (e.key !== 'Tab') return;
    if (pan.contains(document.activeElement)) { trapTabKey(e, pan); return; }
    const liste = getFocusableElements(pan);
    if (!liste.length) return;
    e.preventDefault();
    (e.shiftKey ? liste[liste.length - 1] : liste[0]).focus();
  };
  /* Nach Speichern/Löschen baut renderBody() die Ansicht neu — der Chip, von
     dem aus geöffnet wurde, ist dann ersetzt. Deshalb erst neu aufbauen
     (neuAufbauen), dann zurückgeben; ohne den alten Auslöser an den
     Termin-Knopf, der den Neuaufbau übersteht. */
  const schliessen = (neuAufbauen) => {
    document.removeEventListener('keydown', onKey, true);
    ov.remove();
    if (neuAufbauen) renderBody();
    restoreFocus(vorher?.isConnected ? vorher : document.querySelector('.cal-termin-btn'));
  };
  document.addEventListener('keydown', onKey, true);
  x.addEventListener('click', () => schliessen());

  const actions = el('div', { cls: 'cal-ical-actions' });
  const speichern = el('button', { cls: 'cal-tab cal-tab-active', text: I18n.t('termin.speichern') });
  speichern.addEventListener('click', () => {
    const wert = (n) => felder[n].value.trim() || null;
    /* Unbekannte Felder des Bestands (personen, karteId, Felder neuerer
       Fassungen) bleiben stehen; nur die Dialog-Felder werden ersetzt. */
    const t = { ...(termin || {}) };
    for (const k of ['bisDatum', 'von', 'bis', 'ort', 'beschreibung']) delete t[k];
    t.id = termin?.id || crypto.randomUUID();
    t.titel = felder.titel.value.trim();
    t.datum = wert('datum');
    for (const [k, n] of [['bisDatum', 'bisdatum'], ['von', 'von'], ['bis', 'bis'], ['ort', 'ort'], ['beschreibung', 'beschreibung']]) {
      const v = wert(n);
      if (v != null) t[k] = v;
    }
    if (t.bisDatum && t.bisDatum === t.datum) delete t.bisDatum;
    if (!vertraulichRow.hidden && vertraulichCb.checked !== vertraulichAnfang) t.confidential = vertraulichCb.checked;
    if (!termin) {
      t.erstellt = todayISO();
      t.erstelltVon = Config.get('userName') || 'User';
    }
    const f = terminFehler(t);
    if (f) { fehlerEl.textContent = I18n.t(f); return; }
    EventBus.emit('termin:speichern', t);
    fuehreRemoteNach(State.get()?.board?.id, t.id, t);
    schliessen(true);
  });
  if (!nurLesen) actions.appendChild(speichern);
  if (termin && !nurLesen && !rueckweg) {
    const loeschen = el('button', { cls: 'cal-tab', text: I18n.t('termin.loeschen') });
    loeschen.addEventListener('click', () => {
      if (!confirm(I18n.t('termin.loeschen-frage', termin.titel))) return;
      EventBus.emit('termin:loeschen', termin.id);
      fuehreRemoteNach(State.get()?.board?.id, termin.id, null);
      schliessen(true);
    });
    actions.appendChild(loeschen);
  }
  const abbrechen = el('button', { cls: 'cal-tab', text: I18n.t('termin.abbrechen') });
  abbrechen.addEventListener('click', () => schliessen());
  actions.appendChild(abbrechen);
  pan.appendChild(actions);

  ov.appendChild(pan);
  ov.addEventListener('click', (e) => { if (e.target === ov) schliessen(); });
  document.body.appendChild(ov);
  (nurLesen ? abbrechen : felder.titel).focus();
}

/* ── iCal-Abo-Panel (#308 Etappe 6, server-only — DCE't im Slim) ──────── */

/* Feature-gegatetes CSS via JS-Inject (Memory feedback_slim_css_via_js_inject):
   gehört NICHT in den geteilten ensureStyles-String, sonst landet es im Slim. */
function ensureICalStyles() {
  if (document.getElementById('cal-ical-styles')) return;
  const st = document.createElement('style');
  st.id = 'cal-ical-styles';
  st.textContent = '.cal-ical-overlay{position:fixed;inset:0;background:rgba(0,0,0,.5);z-index:11100;display:flex;align-items:flex-start;justify-content:center;padding:48px 16px;overflow-y:auto}.cal-ical-panel{background:var(--surface);color:var(--text);border-radius:8px;width:100%;max-width:560px;box-shadow:0 12px 32px rgba(0,0,0,.6);padding:0 0 12px}.cal-ical-head{display:flex;align-items:center;justify-content:space-between;padding:12px 16px;border-bottom:1px solid var(--border)}.cal-ical-title{margin:0;font-size:16px}.cal-ical-hint{font-size:12px;color:var(--text-muted);padding:8px 16px 0;margin:0}.cal-ical-list{display:flex;flex-direction:column;gap:6px;padding:10px 16px}.cal-ical-row{display:flex;align-items:center;gap:6px}.cal-ical-scope{font-size:11px;font-weight:600;color:var(--text-muted);min-width:72px}.cal-ical-url{flex:1;font-size:11px;padding:3px 6px;background:var(--surface-2);border:1px solid var(--border);border-radius:3px;color:inherit;min-width:0}.cal-ical-actions{display:flex;gap:6px;padding:0 16px}';
  document.head.appendChild(st);
}

function openICalPanel() {
  if (!IS_SERVER_BUILD || !ClientsProjectsClient) return;
  ensureICalStyles();
  ensureTerminStyles(); /* Sitzungs-QA C-5: .cal-termin-fehler für die Fehlerzeile */
  const ov = el('div', { cls: 'cal-ical-overlay' });
  const pan = el('div', { cls: 'cal-ical-panel', attrs: { role: 'dialog', 'aria-modal': 'true', 'aria-label': I18n.t('calendar.ical.title') } });
  const head = el('div', { cls: 'cal-ical-head' });
  head.appendChild(el('h3', { cls: 'cal-ical-title', text: I18n.t('calendar.ical.title') }));
  const x = el('button', { cls: 'cal-close', text: '✕', attrs: { 'aria-label': I18n.t('btn.close-modal') } });
  /* Eigener ESC-Handler (F-421): sonst feuert der globale Kalender-ESC-Handler
     und schließt das ganze Overlay, während dieses Panel als Waise hängenbleibt. */
  const onKey = (e) => {
    if (e.key === 'Escape') { e.stopPropagation(); closePanel(); }
  };
  const closePanel = () => {
    document.removeEventListener('keydown', onKey, true);
    ov.remove();
  };
  document.addEventListener('keydown', onKey, true); // Capture-Phase → vor dem Overlay-Handler
  x.addEventListener('click', closePanel);
  head.appendChild(x);
  pan.appendChild(head);

  pan.appendChild(el('p', { cls: 'cal-ical-hint', text: I18n.t('calendar.ical.hint') }));

  const listEl = el('div', { cls: 'cal-ical-list' });
  pan.appendChild(listEl);

  const actions = el('div', { cls: 'cal-ical-actions' });
  const boardId = State.get()?.board?.id;
  /* Kritiker #748, K-4: Fehler nicht verschlucken. (Das Abo „für außen“
     ist mit Karte #795 zurückgebaut.) */
  const fehler = el('p', { cls: 'cal-termin-fehler', attrs: { role: 'alert' } });
  const neu = async (opts) => {
    fehler.textContent = '';
    const r = await ClientsProjectsClient.createICalSub(opts);
    /* Karte #782 (ADR-0140 §6): Auf der internen Seite des Board-Paars legt der
       Server keine Abos mehr an (409) — der Hinweis nennt den Ort statt „konnte
       nicht angelegt werden“. */
    if (r && r.ok === false) fehler.textContent = I18n.t(r.status === 409 ? 'calendar.ical.err-innen' : 'calendar.ical.err-create');
    refresh();
  };
  const addBoard = el('button', { cls: 'cal-tab', text: I18n.t('calendar.ical.add-board') });
  addBoard.addEventListener('click', () => neu({ boardId }));
  const addAll = el('button', { cls: 'cal-tab', text: I18n.t('calendar.ical.add-all') });
  addAll.addEventListener('click', () => neu({}));
  if (boardId) actions.appendChild(addBoard);
  actions.appendChild(addAll);
  pan.appendChild(actions);
  pan.appendChild(fehler);

  function loeschKnopf(s) {
    const del = el('button', { cls: 'cal-nav-btn', text: I18n.t('calendar.ical.revoke'), attrs: { 'aria-label': I18n.t('calendar.ical.revoke') } });
    del.addEventListener('click', async () => { await ClientsProjectsClient.deleteICalSub(s.id); refresh(); });
    return del;
  }

  async function refresh() {
    listEl.innerHTML = '';
    const res = await ClientsProjectsClient.listICalSubs();
    const subs = (res.ok && Array.isArray(res.body)) ? res.body : [];
    if (!subs.length) {
      listEl.appendChild(el('div', { cls: 'cal-empty', text: I18n.t('calendar.ical.none') }));
      return;
    }
    for (const s of subs) {
      const row = el('div', { cls: 'cal-ical-row' });
      const scope = (s.scope === 'board'
        ? I18n.t('calendar.ical.scope-board')
        : I18n.t('calendar.ical.scope-all')) + (s.aussen ? ' · ' + I18n.t('calendar.ical.scope-aussen') : '');
      row.appendChild(el('span', { cls: 'cal-ical-scope', text: scope }));
      if (s.defekt) {
        /* Kritiker Sprint 88 (F-1957): nicht entschlüsselbar — kein Link, nur
           Löschen. */
        row.appendChild(el('span', { cls: 'cal-empty', text: I18n.t('calendar.ical.defekt') }));
        row.appendChild(loeschKnopf(s));
        listEl.appendChild(row);
        continue;
      }
      if (s.aussen) {
        /* Karte #795: Bestand-Abo „für außen“ — die Außenstelle ist
           zurückgebaut, das Abo liefert nichts mehr. Nur noch Löschen. */
        row.appendChild(el('span', { cls: 'cal-empty', text: I18n.t('calendar.ical.aussen-eingestellt') }));
        row.appendChild(loeschKnopf(s));
        listEl.appendChild(row);
        continue;
      }
      const url = location.origin + s.path;
      const inp = el('input', { cls: 'cal-ical-url', attrs: { type: 'text', readonly: 'readonly', value: url } });
      inp.value = url;
      row.appendChild(inp);
      const copy = el('button', { cls: 'cal-nav-btn', text: I18n.t('calendar.ical.copy') });
      copy.addEventListener('click', () => {
        inp.select();
        try { navigator.clipboard?.writeText(url); } catch { /* Fallback: Auswahl steht */ }
      });
      row.appendChild(copy);
      row.appendChild(loeschKnopf(s));
      listEl.appendChild(row);
    }
  }

  ov.appendChild(pan);
  ov.addEventListener('click', (e) => { if (e.target === ov) closePanel(); });
  document.body.appendChild(ov);
  x.focus(); // Initial-Fokus ins Panel (A11y, F-421)
  refresh();
}

/* ── CSS (Slim-relevant → kompakt; Theme-Variablen wie im Board) ──────── */

function ensureStyles() {
  if (document.getElementById('cal-view-styles')) return;
  const style = document.createElement('style');
  style.id = 'cal-view-styles';
  style.textContent = '.cal-overlay{position:fixed;inset:0;background:rgba(0,0,0,.45);z-index:11000;display:none;align-items:flex-start;justify-content:center;overflow-y:auto;padding:32px 16px}.cal-overlay[aria-hidden="false"]{display:flex}.cal-panel{background:var(--surface);color:var(--text);border-radius:8px;width:100%;max-width:1040px;box-shadow:0 12px 32px rgba(0,0,0,.6);display:flex;flex-direction:column}.cal-header{display:flex;align-items:center;gap:16px;padding:12px 16px;border-bottom:1px solid var(--border)}.cal-nav{display:flex;gap:4px;align-items:center}.cal-nav-btn{background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;cursor:pointer;font-size:15px;padding:4px 10px;line-height:1}.cal-today-btn{font-size:13px}.cal-title{margin:0;font-size:17px;font-weight:600;flex:1}.cal-close{background:transparent;border:none;color:inherit;font-size:18px;cursor:pointer;padding:4px 8px}.cal-controls{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;padding:8px 16px;border-bottom:1px solid var(--border)}.cal-tabs{display:flex;gap:4px}.cal-tab{background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;cursor:pointer;font-size:13px;padding:4px 12px}.cal-tab-active{background:var(--accent-btn);color:#fff;border-color:var(--accent-btn)}.cal-filters{display:flex;align-items:center;gap:12px}.cal-filter-done{display:flex;align-items:center;gap:5px;font-size:13px;color:var(--text-muted);cursor:pointer}.cal-owner-select{padding:4px 8px;background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;font-size:13px}.cal-body{padding:12px 14px;overflow-x:auto}.cal-grid{display:grid;grid-template-columns:34px repeat(7,1fr);gap:4px}.cal-grid-head{margin-bottom:4px}.cal-grid-row{margin-bottom:4px}.cal-kw-head,.cal-wd{font-size:11px;color:var(--text-muted);text-align:center;padding:2px 0;font-weight:600}.cal-kw{font-size:11px;color:var(--text-muted);display:flex;align-items:center;justify-content:center}.cal-day{background:var(--surface-2);border:1px solid var(--border);border-radius:4px;min-height:78px;padding:3px 4px;display:flex;flex-direction:column;gap:2px;overflow:hidden}.cal-day-out{opacity:.4}.cal-day-we{background:var(--surface)}.cal-day-today{border-color:var(--accent);box-shadow:inset 0 0 0 1px var(--accent)}.cal-day-num{font-size:12px;font-weight:600;color:var(--text-muted);align-self:flex-end}.cal-day-today .cal-day-num{color:var(--accent)}.cal-day-cards{display:flex;flex-direction:column;gap:2px;min-width:0}.cal-chip{display:block;width:100%;text-align:left;font-size:11px;line-height:1.3;padding:1px 4px;border:none;border-radius:3px;background:var(--accent-btn);color:#fff;cursor:pointer;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.cal-chip-done{background:var(--surface);color:var(--text-muted);text-decoration:line-through}.cal-week{display:grid;grid-template-columns:repeat(7,1fr);gap:4px}.cal-week-col{background:var(--surface-2);border:1px solid var(--border);border-radius:4px;min-height:220px;padding:4px;display:flex;flex-direction:column;gap:3px}.cal-week-head{display:flex;align-items:baseline;justify-content:space-between;border-bottom:1px solid var(--border);padding-bottom:3px;margin-bottom:2px}.cal-week-wd{font-size:11px;color:var(--text-muted);font-weight:600}.cal-week-num{font-size:13px;font-weight:600}.cal-agenda{display:flex;flex-direction:column;gap:2px}.cal-agenda-day{font-size:12px;font-weight:600;color:var(--text-muted);margin-top:10px;padding-bottom:2px;border-bottom:1px solid var(--border)}.cal-agenda-row{display:block;width:100%;text-align:left;font-size:13px;padding:5px 6px;border:none;border-radius:3px;background:transparent;color:inherit;cursor:pointer}.cal-agenda-row:hover{background:var(--surface-2)}.cal-empty{color:var(--text-muted);font-style:italic;padding:12px 4px}.cal-chip{cursor:grab}.cal-chip:active{cursor:grabbing}.cal-chip-cross{cursor:default;border-left:3px solid var(--accent);padding-left:4px}.cal-day,.cal-week-col{cursor:pointer}.cal-drop-hover{outline:2px dashed var(--accent);outline-offset:-2px}.cal-quickadd{margin-top:2px}.cal-quickadd input{width:100%;font-size:11px;padding:2px 4px;background:var(--surface);border:1px solid var(--accent);border-radius:3px;color:inherit;box-sizing:border-box}';
  document.head.appendChild(style);
}

/* ── Lifecycle ────────────────────────────────────────────────────────── */

function open(opts = {}) {
  ensureStyles();
  if (!_overlay) buildShell();
  if (!_overlay.parentNode) document.body.appendChild(_overlay);
  if (!_anchor) _anchor = todayISO();
  _overlay.setAttribute('aria-hidden', 'false');
  _onKeyHandler = (e) => { if (e.key === 'Escape') close(); };
  document.addEventListener('keydown', _onKeyHandler);
  if (!opts.skipHash) syncHash();
  /* Projekt-Filter-Quelle lazy laden (server-only, still bei Fehler). */
  if (IS_SERVER_BUILD) loadProjects();
  /* Cross-Board war beim letzten Öffnen aktiv → Daten neu holen (frischer Stand). */
  if (IS_SERVER_BUILD && _filter.crossBoard) loadRemoteCards();
  /* Karte #781: Schatten frisch holen; neue Daten zeichnen den Kalender neu. */
  if (IS_SERVER_BUILD) {
    if (!_schattenHoerer) { _schattenHoerer = () => { if (_overlay?.getAttribute('aria-hidden') === 'false') renderBody(); }; EventBus.on('schatten:geladen', _schattenHoerer); }
    /* Karte #827 E4: Der Rückweg-Schalter entscheidet, ob Spiegelkarten ziehbar sind. */
    if (!_rueckwegHoerer) { _rueckwegHoerer = () => { if (_overlay?.getAttribute('aria-hidden') === 'false') renderBody(); }; EventBus.on('boardpaar:rueckweg', _rueckwegHoerer); EventBus.on('boardpaar:kopie-frei', _rueckwegHoerer); /* F-2233 */ }
    SchattenAussen.ladeJetzt();
  }
  renderBody();
}

function close() {
  if (!_overlay) return;
  _overlay.setAttribute('aria-hidden', 'true');
  if (IS_SERVER_BUILD) SchattenAussen.schliesseHinweis(); /* #781 K-3: der Hinweis hängt im Panel und geht mit */
  if (_onKeyHandler) {
    document.removeEventListener('keydown', _onKeyHandler);
    _onKeyHandler = null;
  }
  /* Kalender-Hash beim Schließen entfernen (Karten-Hash o. Ä. nicht anfassen). */
  if (location.hash.startsWith('#/calendar')) {
    try { history.replaceState(null, '', location.pathname + location.search); } catch { /* sandbox */ }
  }
}

/* Test-only: Modul-State zurücksetzen (Filter/View/Anker persistieren in
   Produktion bewusst über Öffnen/Schließen — Tests brauchen Isolation). */
function _resetForTest() {
  _view = 'month';
  _anchor = null;
  _filter.showDone = false;
  _filter.owner = '';
  _filter.project = '';
  _filter.crossBoard = false;
  _projects = null;
  _remoteEntries = null;
  _remoteTermine = null;
  _remoteLoading = false;
  _remoteError = false;
  /* F-424: sonst schleppt der nächste Test das Fenster des vorigen mit und
     `ensureRemoteFenster` hält es für bereits geladen. */
  _remoteRange = null;
  _remoteGeneration = 0;
}

export const CalendarView = { open, close, maybeRouteFromHash, parseCalendarHash, _resetForTest };
