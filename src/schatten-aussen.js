/* schatten-aussen.js — Anzeige der Schatten interner Karten auf dem externen
   Board (Karte #781, ADR-0140 §5 „Außen“ und §6 Kalender).

   Nur im Server-Build und nur auf einer Instanz mit boardpaar.seite = aussen,
   für ein gekoppeltes Board: Das Modul holt `GET /boards/{id}/schatten` und
   zeigt die Schatten in einem eigenen Band „Intern“ über dem Board —
   ausgegraut, nicht ziehbar, nicht editierbar. Auf aus/innen (und für ein
   nicht gekoppeltes Board, Antwort 404) gibt es kein UI-Element und keinen
   weiteren Abruf.

   Klick auf einen Schatten: Hinweis „Diese Karte ist intern. Bitte ins
   Firmennetz einwählen (VPN).“ mit dem Link „Trotzdem öffnen“. KEINE
   automatische Erreichbarkeitsprüfung beim Laden (Chrome/Firefox fragen seit
   142/153 nach der Erlaubnis für private Adressen; eine abgelehnte Abfrage
   macht die Prüfung dauerhaft unbrauchbar — knowledge/topics/
   browser-zwei-server.md §1). Geprüft wird erst beim Klick, und nur, wenn
   `permissions.query({name:'local-network'})` schon `granted` meldet.

   Aktualität: „Interne Karten zuletzt abgeglichen: vor N Minuten“, ab 30
   Minuten als Warnung. Aktualisiert wird über das SSE-Ereignis
   `schatten:aktualisiert` des Boards (der Browser hatte bisher keinen
   Abonnenten), dazu eine Minuten-Uhr für die Anzeige. Beides endet beim
   Board-Wechsel (stop()).

   Der Kalender (calendar-view.js) liest dieselben Daten über daten() und
   schattenAlsKalenderEintraege().

   Externe Kopie (Karte #825, ADR-0141 §8): Meldet der Server `kopie: true`,
   stehen die Schatten in ihrer Lane und Spalte — renderColumn holt sie über
   inSpalte() und zeichnet sie mit karte() ans Ende der Spalte (eigene Klasse
   `.schatten-karte`, nie `.card`: kein Ziehen, kein Modal, kein Zähler, keine
   Mehrfachauswahl, kein j/k, kein FLIP). Gefunden werden Lane und Spalte über
   die Marke `spiegel.id`; Standardspalten und der Backlog auch über ihre
   Klartext-ID. Das Band zeigt dann nur noch Schatten, deren Lane oder Spalte
   es außen (noch) nicht gibt — damit nichts still verschwindet. */

import { I18n } from './i18n.js';
import { State } from './state.js';
import { EventBus } from './eventbus.js';
import { Config } from './config.js';
import { Search } from './search.js';
import { boardpaarSeite, setzeKopieStand } from './boardpaar.js';
import { trapTabKey, getFocusableElements } from './focus-trap.js';
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

/** Ab so vielen Minuten seit dem letzten Abgleich gilt die Anzeige als Warnung. */
export const STAND_WARN_MIN = 30;
const TICK_MS = 60_000;
/** Mit offenem Kanal holt die Uhr trotzdem neu, wenn der letzte erfolgreiche Abruf so alt ist:
    der Server meldet „schatten:aktualisiert“ nur bei inhaltlicher Änderung, der Abgleich
    selbst läuft aber öfter — ohne Nachholen warnte die Anzeige im Normalbetrieb nach 30 min. */
const NACHLADEN_MS = 5 * 60_000;
const PROBE_MS = 3_000;
/** Nach so vielen SSE-Fehlern in Folge übernimmt die Minuten-Uhr das Nachladen. */
const SSE_FEHLER_MAX = 5;

let _boardId = null;
let _gen = 0;
let _daten = null;      /* { letzter_abgleich, schatten } | null */
let _fehler = false;    /* letzter Abruf gescheitert (503/Netz) */
let _letzterErfolg = 0; /* Date.now() des letzten erfolgreichen Abrufs */
let _live = null;       /* Live-Region (role=status), ändert sich nur bei Zustandswechsel */
let _liveZustand = null; /* 'ok' | 'warn' | 'fehler' */
let _strukturSig = null; /* F-2137: Fingerabdruck der Lanes und Spalten (beiStandWechsel) */
let _sig = null;        /* Fingerabdruck der gezeichneten Daten: nur bei Änderung wird das Band neu gebaut */
let _aktiv = false;     /* Seite ist „aussen“: erst dann darf irgendetwas abgerufen werden */
let _es = null;
let _esFehler = 0;
let _timer = null;
let _bereich = null;
let _offen = true;      /* Band aufgeklappt */
let _hinweis = null;
let _hinweisAnker = null;
let _hinweisKey = null;
let _initialisiert = false;
let _probeLauf = 0;

/* ── Reine Helfer (testbar) ───────────────────────────────────────────── */

/** Minuten seit `iso` (UTC, RFC 3339), nie negativ; null bei ungültigem Wert. */
export function minutenSeit(iso, jetzt = Date.now()) {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return null;
  return Math.max(0, Math.floor((jetzt - t) / 60_000));
}

/** Aktualitäts-Zeile: { text, warn }. Ohne Abgleich (null) gilt die Warnung. */
export function standAnzeige(letzterAbgleich, jetzt = Date.now()) {
  const min = letzterAbgleich ? minutenSeit(letzterAbgleich, jetzt) : null;
  if (min == null) return { text: I18n.t('schatten.stand-nie'), warn: true };
  return { text: I18n.t('schatten.stand', min), warn: min >= STAND_WARN_MIN };
}

/** Nur http(s)-Links gehen in ein href; alles andere (und Leeres) → ''. */
export function sichererLink(link) {
  if (typeof link !== 'string' || !link) return '';
  try {
    const u = new URL(link);
    return u.protocol === 'https:' || u.protocol === 'http:' ? u.href : '';
  } catch { return ''; }
}

/** Suche außen: Titel (nicht der Platzhalter — entschieden am Feld `titel_platzhalter`,
    nicht am Text: eine echte Karte „Interne Karte“ bleibt per Titel findbar) und Nummer („i-12“, „#i-12“, „12“). */
export function schattenTrifft(eintrag, query) {
  const q = String(query ?? '').trim().toLowerCase();
  if (!q) return true;
  const nr = String(eintrag.nummer ?? '').toLowerCase();
  if (nr && (nr.includes(q.replace(/^#/, '')) || nr === 'i-' + q.replace(/^#?i?-?/, ''))) return true;
  const titel = String(eintrag.titel ?? '');
  return !eintrag.titel_platzhalter && titel.toLowerCase().includes(q);
}

function datumKurz(iso) {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso ?? '');
  return m ? `${m[3]}.${m[2]}.` : '';
}

/** Kurzzeile unter dem Titel: Spalte · Priorität · Frist. */
export function metaText(e) {
  if (!IS_SERVER_BUILD) return '';
  const teile = [];
  if (e.art === 'termin') teile.push(I18n.t('schatten.termin'));
  if (e.spalte) teile.push(e.spalte);
  if (e.prioritaet) teile.push(e.prioritaet);
  const von = datumKurz(e.start_datum), bis = datumKurz(e.ende_datum);
  if (von && bis && von !== bis) teile.push(`${von}–${bis}`);
  else if (von || bis) teile.push(von || bis);
  return teile.join(' · ');
}

/**
 * Schatten mit Datum als Einträge der Kalenderansicht (Form wie
 * terminAlsEintrag, `_schatten` trägt den Eintrag): Start = start_datum oder
 * ende_datum, Ende = ende_datum oder start_datum; Uhrzeiten gehören zum
 * jeweiligen Tag. Ohne Datum kein Eintrag (wie im Feed).
 */
export function schattenAlsKalenderEintraege(liste) {
  const out = [];
  for (const e of Array.isArray(liste) ? liste : []) {
    const start = e.start_datum || e.ende_datum || '';
    if (!/^\d{4}-\d{2}-\d{2}$/.test(start)) continue;
    let ende = e.ende_datum || start;
    if (!/^\d{4}-\d{2}-\d{2}$/.test(ende) || ende < start) ende = start;
    out.push({
      sprintId: null,
      card: {
        id: 'schatten:' + e.schatten_id,
        title: (e.nummer && !e.titel_platzhalter ? e.nummer + ' ' : '') + e.titel, /* wie der Feed (ical.go): Nummer nur vor einem echten Titel */
        startDate: start,
        dueDate: ende,
        startTime: e.start_datum && e.start_zeit ? e.start_zeit : null,
        dueTime: e.ende_datum && e.ende_zeit ? e.ende_zeit : null,
        _schatten: e,
      },
    });
  }
  return out;
}

/* ── Abruf ────────────────────────────────────────────────────────────── */

function baseUrl() {
  return (Config.get('serverUrl') ?? '').replace(/\/$/, '');
}

/** { status: 'ok', daten } | { status: 'keine' } (404: nicht außen/nicht gekoppelt) | { status: 'fehler' } */
export async function ladeSchatten(boardId) {
  try {
    const res = await holen(`${baseUrl()}/boards/${encodeURIComponent(boardId)}/schatten`, { cache: 'no-store' });
    if (res.status === 404) return { status: 'keine' };
    if (!res.ok) return { status: 'fehler' };
    const d = await res.json();
    if (!d || !Array.isArray(d.schatten)) return { status: 'fehler' };
    return { status: 'ok', daten: { letzter_abgleich: d.letzter_abgleich ?? null, schatten: d.schatten, kopie: d.kopie === true,
      bearbeitbar: d.bearbeitbar === true } };
  } catch { return { status: 'fehler' }; }
}

async function lade(gen) {
  const boardId = _boardId;
  if (!boardId) return;
  const r = await ladeSchatten(boardId);
  if (gen !== _gen) return; /* Board gewechselt oder gestoppt: Antwort verwerfen */
  /* F-2233 (Karte #831): Kopie-Stand für die Spiegel-Sperre. 404 (außen
     nicht gekoppelt, also auch außen entkoppelt) und `kopie: false` heißen
     „keine Kopie“ — wie IstKopie am Server; ein Fehler heißt „unbekannt“ und
     sperrt wieder (fail-closed).
     Kritik #831 K-3: Der Weg liefert 404 auch, wenn der Server nicht (mehr) auf
     der Seite außen steht, keine Konfiguration hat oder ein Proxy 404 meldet —
     alle drei geben hier frei. Vertretbar, weil (1) beginne() nur auf der Seite
     außen überhaupt lädt und (2) die Freigabe nur die Sperre im Browser löst:
     Die Schranke an den Spiegeln hält der Server an kopie = 1 (istKopieMit),
     ein Schreibversuch an einer echten Kopie scheitert dort trotzdem. Ein 404
     sperrt also nie fälschlich weg, gibt aber auch nichts frei, was der Server
     nicht selbst noch prüft. */
  /* Karte #836: Mit demselben Abruf kommt der Schalter „bearbeitbar“ der
     Kopie; nur ein ausdrückliches `true` gibt den Rückweg für dieses Board
     frei (zusammen mit /me). Fehler und fehlendes Feld heißen „nur lesen“. */
  setzeKopieStand(boardId, r.status === 'keine' || (r.status === 'ok' && r.daten.kopie !== true),
    r.status === 'ok' && r.daten.kopie === true && r.daten.bearbeitbar === true);
  if (r.status === 'ok') {
    _daten = r.daten; _fehler = false; _letzterErfolg = Date.now();
    /* Wieder gekoppelt (oder der erste Abruf war 404): Kanal und Uhr starten, wenn sie
       nicht laufen — dieselbe Startfunktion wie beginne(), kein Doppelstart (W-1). */
    if (_aktiv) starteLauf(gen);
  }
  else if (r.status === 'keine') {
    /* Nachträglich entkoppelt (404 zur Laufzeit): wie beim ersten 404 in beginne() —
       Kanal zu, Uhr aus, Band weg. Sonst fragte die Uhr jede Minute weiter (V-1). */
    _daten = null; _fehler = false;
    schliesseEs();
    if (_timer) { clearInterval(_timer); _timer = null; }
    schliesseHinweis(); /* sein „Trotzdem öffnen“ führt zu einem entkoppelten Board (W-2) */
    if (_bereich) { _bereich.remove(); _bereich = null; _live = null; _liveZustand = null; }
  }
  else _fehler = true; /* alter Stand bleibt stehen — die Aktualität warnt */
  /* Neu bauen nur bei geänderten Daten: ein unveränderter Abruf (Nachholen der Uhr)
     darf Fokus und Hinweis-Anker im Band nicht verlieren (K-8). */
  const sig = JSON.stringify([_daten?.schatten ?? null, _fehler]);
  const geaendert = sig !== _sig;
  _sig = sig;
  if (!geaendert && _bereich?.isConnected) { aktualisiereStand(); return; }
  zeichne();
  EventBus.emit('schatten:geladen');
}

/* ── Lebenslauf ───────────────────────────────────────────────────────── */

function schliesseEs() {
  if (_es) { try { _es.close(); } catch { /* schon zu */ } _es = null; }
  _esFehler = 0;
}

/** Kanal vorhanden und nicht endgültig geschlossen (EventSource.CLOSED = 2). */
function kanalOffen() {
  return !!_es && _es.readyState !== 2;
}

function oeffneEs(gen) {
  if (typeof EventSource === 'undefined') return;
  try {
    const es = new EventSource(`${baseUrl()}/boards/${encodeURIComponent(_boardId)}/events`);
    es.addEventListener('schatten:aktualisiert', () => { _esFehler = 0; lade(gen); });
    es.addEventListener('open', () => { _esFehler = 0; });
    es.addEventListener('error', () => {
      /* Der Browser verbindet selbst neu; nach SSE_FEHLER_MAX Fehlern in Folge
         geben wir den Kanal auf, die Minuten-Uhr holt dann nach. Steht er schon
         endgültig auf CLOSED (302/401/404 beim Neuverbinden: genau ein error),
         kommt kein weiterer — dann sofort aufgeben (K-2). */
      if (es.readyState === 2 || ++_esFehler >= SSE_FEHLER_MAX) schliesseEs();
    });
    _es = es;
  } catch { _es = null; }
}

function tick(gen) {
  if (gen !== _gen) return;
  /* Ohne (oder mit endgültig geschlossenem) Ereigniskanal, nach einem Fehler
     und bei veraltetem Abruf holt die Uhr nach; sonst tickt nur die Anzeige. */
  if (_es && !kanalOffen()) schliesseEs();
  if (!kanalOffen() || _fehler || Date.now() - _letzterErfolg >= NACHLADEN_MS) lade(gen);
  else aktualisiereStand();
}

function stop() {
  _gen++;
  _boardId = null;
  setzeKopieStand(null, false); /* F-2233: Stand gilt nur für das abgefragte Board */
  _aktiv = false;
  _daten = null;
  _fehler = false;
  _letzterErfolg = 0;
  _sig = null;
  _liveZustand = null;
  schliesseEs();
  if (_timer) { clearInterval(_timer); _timer = null; }
  schliesseHinweis();
  if (_bereich) { _bereich.remove(); _bereich = null; _live = null; }
  EventBus.emit('schatten:geladen');
}

async function beginne(boardId) {
  const gen = ++_gen;
  _boardId = boardId;
  const seite = await boardpaarSeite();
  if (gen !== _gen || seite !== 'aussen') return; /* aus/innen: kein UI, kein weiterer Abruf */
  _aktiv = true;
  await lade(gen);
  if (gen !== _gen) return;
  if (!_daten && !_fehler) return; /* 404: nicht gekoppelt — kein Kanal, keine Uhr */
  starteLauf(gen);
}

/** Ereigniskanal und Minuten-Uhr starten, wenn die Uhr nicht läuft (idempotent). Ein
    Kanal, der bei laufender Uhr aufgegeben wurde (CLOSED, K-2), wird nicht neu geöffnet:
    dann holt die Uhr nach. */
function starteLauf(gen) {
  if (gen !== _gen || _timer) return;
  schliesseEs();
  oeffneEs(gen);
  _timer = setInterval(() => tick(gen), TICK_MS);
}

/** Kennungen der Lanes und Spalten — davon hängt ab, welche Schatten im Band landen (ohnePlatz). */
function strukturSig(board) {
  const k = liste => (Array.isArray(liste) ? liste : []).map(kennungVon);
  return JSON.stringify([k(board?.sprints), k(board?.columnDefs)]);
}

function beiStandWechsel() {
  const board = State.get()?.board;
  const id = board?.id ?? null;
  if (id === _boardId) {
    /* F-2137 (Karte #826): Gleiches Board, aber Lanes oder Spalten haben sich
       geändert (Eingang hat die Struktur geschrieben, Spalte gelöscht) — das
       Band neu rechnen, sonst stünde ein Schatten doppelt (Band und Spalte)
       oder gar nicht. Ohne Strukturwechsel nichts tun (Fokus im Band, K-8). */
    const sig = strukturSig(board);
    if (sig !== _strukturSig) { _strukturSig = sig; if (_daten) zeichne(); }
    return;
  }
  stop();
  _strukturSig = strukturSig(board);
  if (id) beginne(id);
}

/** Einmal beim Start (main.js, nur Server-Build). */
function init() {
  if (!IS_SERVER_BUILD || _initialisiert) return;
  _initialisiert = true;
  EventBus.on('state:changed', beiStandWechsel);
  Search.onChange(() => { if (_bereich) zeichne(); });
  beiStandWechsel();
}

/* ── Klick: Hinweis oder direktes Öffnen ──────────────────────────────── */

/** Chromes/Firefox' Berechtigung für private Adressen schon erteilt? Nie fragen. */
async function berechtigungErteilt() {
  try {
    const s = await navigator.permissions.query({ name: 'local-network' });
    return s?.state === 'granted';
  } catch { return false; } /* Name unbekannt (TypeError) oder keine Permissions-API */
}

/** no-cors-Probe gegen /healthz des internen Servers, ohne Anmeldedaten, mit Frist.
    Jede HTTP-Antwort (auch ein 302 zur Anmeldung) zählt als „erreichbar“ — sie
    kommt als opaque zurück; ohne Verbindung wirft fetch (knowledge §2). */
async function probeErreichbar(link) {
  const ctl = typeof AbortController === 'function' ? new AbortController() : null;
  const frist = setTimeout(() => ctl?.abort(), PROBE_MS);
  try {
    /* Ausnahme F-2977: fremder Ursprung, no-cors — Antwort opaque, Status nicht lesbar und nicht unser Server. */
    await fetch(new URL(link).origin + '/healthz',
      { mode: 'no-cors', credentials: 'omit', cache: 'no-store', signal: ctl?.signal });
    return true;
  } catch { return false; } finally { clearTimeout(frist); }
}

/** Automatische Prüfung NUR bei schon erteilter Berechtigung (nie beim Laden). */
async function automatischErreichbar(link) {
  return (await berechtigungErteilt()) && probeErreichbar(link);
}

function oeffneLink(link) {
  const a = document.createElement('a');
  a.href = link; a.target = '_blank'; a.rel = 'noopener noreferrer';
  document.body.appendChild(a);
  a.click();
  a.remove();
}

function schliesseHinweis() {
  if (!_hinweis) return;
  document.removeEventListener('keydown', _hinweisKey, true);
  _hinweis.remove();
  const anker = _hinweisAnker;
  _hinweis = _hinweisAnker = _hinweisKey = null;
  try { anker?.isConnected && anker.focus(); } catch { /* nichts zu fokussieren */ }
}

function zeigeHinweis(link, anker) {
  if (!IS_SERVER_BUILD) return;
  schliesseHinweis();
  ensureStyles();
  const box = document.createElement('div');
  box.className = 'sch-hinweis';
  box.setAttribute('role', 'alertdialog');
  box.setAttribute('aria-labelledby', 'sch-hinweis-text');
  const p = document.createElement('p');
  p.id = 'sch-hinweis-text';
  p.textContent = I18n.t('schatten.hinweis');
  box.appendChild(p);
  const zeile = document.createElement('div');
  zeile.className = 'sch-hinweis-knoepfe';
  let erste = null;
  if (link) {
    const a = document.createElement('a');
    a.className = 'btn btn-primary btn-sm sch-trotzdem';
    a.href = link; a.target = '_blank'; a.rel = 'noopener noreferrer';
    a.textContent = I18n.t('schatten.trotzdem');
    zeile.appendChild(a);
    erste = a;
  }
  const zu = document.createElement('button');
  zu.type = 'button';
  zu.className = 'btn btn-sm sch-zu';
  zu.textContent = I18n.t('schatten.schliessen');
  zu.addEventListener('click', schliesseHinweis);
  zeile.appendChild(zu);
  box.appendChild(zeile);
  /* Bei offenem Kalender gehört der Hinweis ins Dialog-Panel (aria-modal, Fokus-Fang
     des Kalenders); sonst an den Body. */
  (document.querySelector('.cal-overlay[aria-hidden="false"] .cal-panel') ?? document.body).appendChild(box);
  _hinweis = box;
  _hinweisAnker = anker ?? null;
  /* Capture + stopPropagation wie die anderen Dialoge im Kalender: Esc schließt nur
     den Hinweis, nicht zugleich den Kalender darunter (K-3). Tab bleibt im Hinweis. */
  _hinweisKey = (ev) => {
    /* Auf dem Board ohne Kalender greift der Hinweis nicht ins Dokument ein: Esc und Tab
       gehören dann dem, was gerade den Fokus hat (Kopf-Eingabefeld, Karten-Modal) — außer
       der Fokus liegt im Hinweis selbst (V-2). */
    const imPanel = !!box.closest('.cal-panel');
    const imHinweis = box.contains(document.activeElement);
    if (ev.key === 'Escape') {
      if (!imPanel && !imHinweis) return;
      ev.stopPropagation(); schliesseHinweis(); return;
    }
    if (ev.key !== 'Tab' || !imPanel) return;
    if (imHinweis) { trapTabKey(ev, box); return; }
    const liste = getFocusableElements(box);
    if (!liste.length) return;
    ev.preventDefault();
    (ev.shiftKey ? liste[liste.length - 1] : liste[0]).focus();
  };
  document.addEventListener('keydown', _hinweisKey, true);
  (erste ?? zu).focus();
}

/** Klick auf einen Schatten (Board-Band und Kalender): Hinweis, oder — nur bei
    erteilter Berechtigung und erreichbarem Server — direkt öffnen. */
async function klick(eintrag, anker) {
  const lauf = ++_probeLauf;
  const link = sichererLink(eintrag?.link);
  if (link && await automatischErreichbar(link)) {
    if (lauf === _probeLauf) oeffneLink(link);
    return;
  }
  if (lauf === _probeLauf) zeigeHinweis(link, anker);
}

/* ── Darstellung ──────────────────────────────────────────────────────── */

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}

function mountPunkt() {
  if (_bereich?.isConnected) return _bereich;
  ensureStyles();
  _bereich = el('section', 'sch-bereich');
  _bereich.id = 'schatten-aussen';
  _live = el('span', 'sch-live');
  _live.setAttribute('role', 'status');
  _bereich.appendChild(_live);
  _liveZustand = null;
  _bereich.setAttribute('aria-label', I18n.t('schatten.titel'));
  const kopf = document.getElementById('board-header');
  if (kopf?.parentNode) kopf.after(_bereich);
  else (document.getElementById('app') ?? document.body).prepend(_bereich);
  return _bereich;
}

function zeile(e) {
  if (!IS_SERVER_BUILD) return null;
  const li = el('li', 'sch-eintrag');
  const b = el('button', 'sch-zeile');
  b.type = 'button';
  b.draggable = false;
  b.setAttribute('draggable', 'false');
  b.dataset.schatten = e.schatten_id ?? '';
  b.title = I18n.t('schatten.zeile-titel');
  if (e.nummer) b.appendChild(el('span', 'sch-nr', e.nummer));
  if (e.von_agent) {
    const a = el('span', 'sch-agent', '🤖');
    /* F-2029: von_agent heisst bei Karten „Titel von einem Agenten geändert“,
       bei Terminen „zuletzt durch einen Agenten geändert“ (ADR-0140 §2) —
       nicht „angelegt“. */
    const agentText = I18n.t(e.art === 'termin' ? 'schatten.von-agent-termin' : 'schatten.von-agent-karte');
    a.title = agentText;
    a.setAttribute('aria-label', agentText);
    b.appendChild(a);
  }
  const text = el('span', 'sch-text');
  text.appendChild(el('span', 'sch-titel', e.titel));
  const meta = metaText(e);
  if (meta) text.appendChild(el('span', 'sch-meta', meta));
  b.appendChild(text);
  b.addEventListener('click', () => klick(e, b));
  li.appendChild(b);
  return li;
}

/** Nur die Aktualitäts-Zeile nachziehen (Minuten-Uhr) — Liste, Fokus und Anker bleiben. */
function aktualisiereStand() {
  if (!IS_SERVER_BUILD || !_bereich || !_daten) return;
  const st = _bereich.querySelector('.sch-stand');
  if (!st) return;
  const stand = standAnzeige(_daten.letzter_abgleich);
  const text = (stand.warn ? '⚠ ' : '') + stand.text;
  if (st.textContent !== text) st.textContent = text;
  st.classList.toggle('sch-stand--warn', stand.warn);
  aktualisiereLive(stand.warn);
}

/** Live-Region für Screenreader: nur bei Zustandswechsel (ok ⇄ Warnung, Fehler) — die
    Minutenzeile ändert sich jede Minute und ist deshalb keine Live-Region (V-4). */
function aktualisiereLive(warn) {
  if (!_live) return;
  const z = _fehler ? 'fehler' : warn ? 'warn' : 'ok';
  if (z === _liveZustand) return;
  const erstmals = _liveZustand === null;
  _liveZustand = z;
  _live.textContent = z === 'fehler' ? I18n.t('schatten.fehler')
    : z === 'warn' ? I18n.t('schatten.live-warn')
    : erstmals ? '' : I18n.t('schatten.live-ok');
}

/** Alles im Band außer der dauerhaften Live-Region entfernen. */
function leereBand(b) {
  for (const c of [...b.children]) if (c !== _live) c.remove();
}

/* ── Externe Kopie: Schatten in Lane und Spalte (#825) ─────────────────── */

/** Kennung einer Lane bzw. Spalte, wie die Schatten sie tragen: die Marke, sonst die
    Klartext-ID (Backlog, Standardspalten). */
function kennungVon(obj) {
  const m = obj?.spiegel;
  return (m && typeof m === 'object' && typeof m.id === 'string') ? m.id : (obj?.id ?? null);
}

/** Nummer „i-12“ → 12 (für die Reihenfolge); ohne Nummer ans Ende. */
function nummerWert(e) {
  const m = /(\d+)$/.exec(String(e?.nummer ?? ''));
  return m ? Number(m[1]) : Number.MAX_SAFE_INTEGER;
}

/** Ist das offene Board eine externe Kopie (Server meldet `kopie`)? */
function istKopie() {
  return IS_SERVER_BUILD && _aktiv && _daten?.kopie === true;
}

/** Schatten einer Lane und Spalte — nach Nummer sortiert, gefiltert wie die Suche.
    Leer, wenn das Board keine Kopie ist. */
export function inSpalte(sprint, colDef, liste = _daten?.schatten, query = Search.getQuery()) {
  if (!IS_SERVER_BUILD || !sprint || !colDef || !Array.isArray(liste)) return [];
  const lane = kennungVon(sprint), spalte = kennungVon(colDef);
  return liste
    .filter(e => e.art === 'karte' && e.lane && e.lane === lane && e.spalte_id === spalte)
    .filter(e => !query || schattenTrifft(e, query))
    .sort((a, b) => nummerWert(a) - nummerWert(b));
}

/** Schatten, deren Lane oder Spalte es im Board-Zustand nicht gibt (Band als Auffang). */
export function ohnePlatz(liste, board) {
  if (!Array.isArray(liste)) return [];
  const lanes = new Set((Array.isArray(board?.sprints) ? board.sprints : []).map(kennungVon));
  const spalten = new Set((Array.isArray(board?.columnDefs) ? board.columnDefs : []).map(kennungVon));
  return liste.filter(e => e.art !== 'karte' ? false : !lanes.has(e.lane) || !spalten.has(e.spalte_id));
}

/** Ein Schatten als graue Karte in der Spalte (nie `.card`). */
function karte(e) {
  if (!IS_SERVER_BUILD) return null;
  ensureStyles();
  const b = el('button', 'schatten-karte');
  b.type = 'button';
  b.draggable = false;
  b.setAttribute('draggable', 'false');
  b.dataset.schatten = e.schatten_id ?? '';
  b.title = I18n.t('schatten.zeile-titel');
  const kopf = el('span', 'schatten-karte-kopf');
  if (e.nummer) kopf.appendChild(el('span', 'sch-nr', e.nummer));
  if (e.von_agent) {
    const agentText = I18n.t('schatten.von-agent-karte');
    const a = el('span', 'sch-agent', '🤖');
    a.title = agentText;
    a.setAttribute('aria-label', agentText);
    kopf.appendChild(a);
  }
  kopf.appendChild(el('span', 'schatten-karte-schloss', I18n.t('schatten.intern-marke')));
  b.appendChild(kopf);
  b.appendChild(el('span', 'schatten-karte-titel', e.titel));
  const teile = [];
  if (e.prioritaet) teile.push(e.prioritaet);
  if (Array.isArray(e.bearbeiter) && e.bearbeiter.length) teile.push(e.bearbeiter.join(', '));
  const von = datumKurz(e.start_datum), bis = datumKurz(e.ende_datum);
  if (von && bis && von !== bis) teile.push(`${von}–${bis}`);
  else if (von || bis) teile.push(von || bis);
  if (teile.length) b.appendChild(el('span', 'schatten-karte-meta', teile.join(' · ')));
  b.addEventListener('click', (ev) => { ev.stopPropagation(); klick(e, b); });
  return b;
}

function zeichne() {
  if (!IS_SERVER_BUILD) return;
  /* #825: Auf einer Kopie stehen die Schatten in der Spalte; das Band fängt nur auf,
     was dort keinen Platz findet. */
  const alle = istKopie() ? ohnePlatz(_daten?.schatten, State.get()?.board) : (_daten?.schatten ?? []);
  const q = Search.getQuery();
  const liste = q ? alle.filter(e => schattenTrifft(e, q)) : alle;
  if (!_daten || alle.length === 0 || liste.length === 0) {
    if (_bereich) { _bereich.hidden = true; leereBand(_bereich); }
    return;
  }
  const b = mountPunkt();
  b.hidden = false;
  leereBand(b);
  const kopf = el('div', 'sch-kopf');
  const umschalter = el('button', 'sch-umschalter',
    (_offen ? '▾ ' : '▸ ') + I18n.t('schatten.titel') + ' (' + liste.length + ')');
  umschalter.type = 'button';
  umschalter.setAttribute('aria-expanded', String(_offen));
  umschalter.setAttribute('aria-controls', 'sch-liste');
  umschalter.addEventListener('click', () => { _offen = !_offen; zeichne(); });
  kopf.appendChild(umschalter);
  const stand = standAnzeige(_daten.letzter_abgleich);
  const st = el('span', 'sch-stand' + (stand.warn ? ' sch-stand--warn' : ''), (stand.warn ? '⚠ ' : '') + stand.text);
  kopf.appendChild(st);
  if (_fehler) kopf.appendChild(el('span', 'sch-stand sch-stand--warn', '⚠ ' + I18n.t('schatten.fehler')));
  b.appendChild(kopf);
  const ul = el('ul', 'sch-liste');
  ul.id = 'sch-liste';
  ul.hidden = !_offen;
  for (const e of liste) ul.appendChild(zeile(e));
  b.appendChild(ul);
  aktualisiereLive(stand.warn);
}

/* F-2027: Schatten-Zeilen und -Chips ohne opacity. Gemessen auf
   --surface-2 mit --text-muted: Deckkraft .8 ergab 3,3:1 (hell) bzw. 3,8:1
   (dunkel), die Metazeile mit zusätzlich .85 nur 2,7:1 — unter WCAG 4,5:1.
   Voll deckend 4,9:1 bzw. 5,0:1. „Ausgegraut“ zeigen gestrichelter Rand und
   Kursivschrift. Die Kalender-Chips (transparent) liegen auf der Tageszelle:
   --surface-2 4,9/5,0, Wochenende --surface 5,4/5,8, --bg 4,7/6,2 (hell/dunkel,
   nachgemessen in der Sitzungs-QA, K-6). */
function ensureStyles() {
  if (document.getElementById('sch-aussen-styles')) return;
  const st = document.createElement('style');
  st.id = 'sch-aussen-styles';
  st.textContent = '.sch-live{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}.sch-bereich{position:relative;padding:6px 20px;border-bottom:1px dashed var(--border);background:var(--surface-2);color:var(--text-muted)}.sch-bereich[hidden],.sch-liste[hidden]{display:none}.sch-kopf{display:flex;flex-wrap:wrap;align-items:center;gap:4px 12px;font-size:12px}.sch-umschalter{background:none;border:none;color:inherit;font:inherit;font-weight:600;cursor:pointer;padding:4px 0}.sch-stand{font-size:11px}.sch-stand--warn{background:var(--prio-high-bg);color:var(--prio-high-fg);padding:1px 6px;border-radius:4px}.sch-liste{list-style:none;margin:4px 0 2px;padding:0;display:flex;flex-wrap:wrap;gap:6px;max-height:30vh;overflow-y:auto}.sch-eintrag{flex:0 1 280px;min-width:0}.sch-zeile{display:flex;align-items:baseline;gap:6px;width:100%;box-sizing:border-box;text-align:left;font:inherit;font-size:12px;color:inherit;background:transparent;border:1px dashed var(--border);border-radius:6px;padding:5px 8px;font-style:italic;cursor:pointer;overflow-wrap:anywhere}.sch-zeile:hover{border-style:solid}.sch-nr{font-weight:600;white-space:nowrap}.sch-text{display:flex;flex-direction:column;min-width:0}.sch-meta{font-size:11px}.sch-hinweis{position:fixed;left:16px;right:16px;bottom:16px;max-width:460px;margin:0 auto;box-sizing:border-box;z-index:11500;background:var(--surface);color:var(--text);border:1px solid var(--border);border-radius:8px;padding:12px 14px;box-shadow:0 8px 24px rgba(0,0,0,.45)}.sch-hinweis p{margin:0 0 10px;font-size:14px;overflow-wrap:anywhere}.sch-hinweis-knoepfe{display:flex;flex-wrap:wrap;gap:8px}.schatten-karte{display:flex;flex-direction:column;gap:2px;width:100%;box-sizing:border-box;margin:0 0 6px;text-align:left;font:inherit;font-size:12px;color:var(--text-muted);background:var(--surface-2);border:1px dashed var(--border);border-radius:6px;padding:6px 8px;font-style:italic;cursor:pointer;overflow-wrap:anywhere}.schatten-karte:hover{border-style:solid}.schatten-karte-kopf{display:flex;gap:6px;align-items:baseline;font-style:normal}.schatten-karte-schloss{margin-left:auto;font-size:11px}.schatten-karte-titel{font-size:13px}.schatten-karte-meta{font-size:11px}.cal-chip.cal-chip-schatten{background:transparent;color:var(--text-muted);border:1px dashed var(--border);font-style:italic;cursor:pointer}.cal-agenda-schatten{color:var(--text-muted);font-style:italic}@media (max-width:640px){.sch-bereich{padding:6px 14px}.sch-liste{flex-direction:column;flex-wrap:nowrap;max-height:40vh}.sch-eintrag{flex:none;width:100%}}';
  document.head.appendChild(st);
}

/* ── Schnittstelle ────────────────────────────────────────────────────── */

/** Ist das offene Board eine externe Kopie? (renderColumn, main.js) */
function kopie() {
  return istKopie();
}

/** Die Schatten des offenen Boards (leer, wenn es keine gibt oder die Seite nicht „außen“ ist). */
function schatten() {
  return _daten?.schatten ?? [];
}

/** Sofort neu holen (Kalender beim Öffnen) — nur auf der Seite „außen“. */
function ladeJetzt() {
  return _boardId && _aktiv ? lade(_gen) : Promise.resolve();
}

/** Test-only: Zustand zurücksetzen. */
function _resetForTest() {
  stop();
  _initialisiert = false;
  _offen = true;
  _probeLauf = 0;
  _strukturSig = null;
  EventBus.off('state:changed', beiStandWechsel);
}

export const SchattenAussen = { init, stop, schatten, ladeJetzt, klick, schliesseHinweis, stile: ensureStyles, kopie, inSpalte, karte, _resetForTest };
