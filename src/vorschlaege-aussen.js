/* vorschlaege-aussen.js — Vorschläge des internen Agenten auf dem externen
   Board (Karte #783, ADR-0140 §8, linux ADR-0048 3a/9.6).

   Nur im Server-Build und nur auf einer Instanz mit boardpaar.seite = aussen:
   Das Modul holt `GET /boards/{id}/vorschlaege` und zeigt offene Vorschläge
   in einem eigenen Band über dem Board — mit „Bestätigen“ und „Ablehnen“.
   Wer das Board nicht bearbeiten darf (Antwort 403) oder wo es keine
   Vorschläge gibt (404, leere Liste), entsteht kein UI-Element.

   Alle Texte des Vorschlags stammen von einem Agenten, der fremde Dokumente
   gelesen haben kann: Sie werden ausschließlich als Text gesetzt
   (textContent), nie als Markdown oder HTML (linux K-3). Bestätigt wird mit
   dem Hash, den der Server zur angezeigten Fassung geliefert hat — hat sich
   der Vorschlag dazwischen geändert, lehnt der Server mit 409 ab.

   Aktualisiert wird beim Board-Wechsel, nach jeder Aktion und jede Minute,
   solange das Board offen ist (Vorschläge lösen kein Board-Ereignis aus). */

import { I18n } from './i18n.js';
import { State } from './state.js';
import { EventBus } from './eventbus.js';
import { Config } from './config.js';
import { boardpaarSeite } from './boardpaar.js';
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

const TICK_MS = 60_000;

let _boardId = null;
let _gen = 0;
let _liste = [];
let _timer = null;
let _bereich = null;
let _meldung = '';
let _laeuft = false; /* eine Aktion ist unterwegs: Knöpfe gesperrt */
let _veraltet = false; /* letzter Abruf gescheitert: die Liste ist womöglich nicht aktuell */
let _anfrage = 0;     /* Zähler der Abrufe: nur die Antwort des jüngsten zählt (K-B3) */
let _sig = null;      /* Fingerabdruck der gezeichneten Liste: neu gebaut wird nur bei Änderung (K-B2) */
let _live = null;     /* dauerhafte Live-Region für Meldungen (K-B2) */
let _fokusNachAktion = false; /* der Fokus lag beim Klick im Band — nach dem Neubau dorthin zurück (Verify V-8) */
let _aktionsAbruf = false;    /* der Abruf nach einer Aktion läuft — die Minuten-Uhr wartet (Verify V-10) */
let _initialisiert = false;

function baseUrl() {
  return (Config.get('serverUrl') ?? '').replace(/\/$/, '');
}

/** { status: 'ok', liste } | { status: 'keine' } (403: nicht editor, 404: nicht außen) | { status: 'fehler' } */
export async function ladeVorschlaege(boardId) {
  try {
    const res = await holen(`${baseUrl()}/boards/${encodeURIComponent(boardId)}/vorschlaege`, { cache: 'no-store' });
    if (res.status === 403 || res.status === 404) return { status: 'keine' };
    if (!res.ok) return { status: 'fehler' };
    const d = await res.json();
    if (!d || !Array.isArray(d.vorschlaege)) return { status: 'fehler' };
    return { status: 'ok', liste: d.vorschlaege };
  } catch { return { status: 'fehler' }; }
}

/** Meldung zu einer abgelehnten Aktion: der Grund aus dem Status, übersetzt; der Text
    des Servers (deutsch) nur als Zusatz in der deutschen Oberfläche (K-B4). */
async function fehlerMeldung(res) {
  const key = { 403: 'vorschlag.err-recht', 404: 'vorschlag.err-weg', 409: 'vorschlag.err-konflikt',
    410: 'vorschlag.err-abgelaufen', 429: 'vorschlag.err-zuviel' }[res.status];
  let zusatz = '';
  if (I18n.getLang() === 'de') {
    try {
      const d = await res.json();
      const innen = d?.details?.error;
      zusatz = [d?.error, typeof innen === 'string' ? innen : ''].filter(Boolean).join(' ');
    } catch { /* kein JSON */ }
  }
  const grund = key ? I18n.t(key) : I18n.t('vorschlag.err-status', res.status);
  return I18n.t('vorschlag.fehler') + grund + (zusatz ? ' — ' + zusatz : '');
}

/** mitMeldung: die Meldung der letzten Aktion stehen lassen (direkt danach);
    die Minuten-Uhr räumt sie ab. */
async function lade(gen, mitMeldung = false) {
  const boardId = _boardId;
  if (!boardId) return;
  const nr = ++_anfrage;
  const r = await ladeVorschlaege(boardId);
  /* Board gewechselt oder ein jüngerer Abruf unterwegs (Aktion während der Minuten-Uhr):
     diese Antwort ist veraltet (K-B3). */
  if (gen !== _gen || nr !== _anfrage) return;
  if (!mitMeldung) _meldung = '';
  _veraltet = r.status === 'fehler'; /* alter Stand bleibt stehen, mit Hinweis */
  if (r.status === 'ok') _liste = r.liste;
  else if (r.status === 'keine') {
    /* Nicht editor oder nicht (mehr) außen: kein Band, und die Uhr fragt nicht weiter. */
    _liste = [];
    _meldung = '';
    if (_timer) { clearInterval(_timer); _timer = null; }
  }
  zeichne();
}

async function aktion(v, bestaetigen) {
  if (_laeuft || !_boardId) return;
  _laeuft = true;
  const gen = _gen;
  /* Ein gesperrter Knopf verliert den Fokus an body — vorher merken (Verify V-8). */
  _fokusNachAktion = !!_bereich?.contains(document.activeElement);
  sperreKnoepfe();
  const pfad = `${baseUrl()}/boards/${encodeURIComponent(_boardId)}/vorschlaege/${encodeURIComponent(v.id)}`;
  let meldung;
  try {
    const res = bestaetigen
      ? await holen(pfad + '/bestaetigen', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ hash: v.hash }) })
      : await holen(pfad, { method: 'DELETE' });
    meldung = res.ok ? I18n.t(bestaetigen ? 'vorschlag.ok-bestaetigt' : 'vorschlag.ok-abgelehnt') : await fehlerMeldung(res);
  } catch {
    meldung = I18n.t('vorschlag.fehler') + I18n.t('vorschlag.netz');
  }
  /* Board inzwischen gewechselt: Meldung und Sperre gehören dem alten Board (K-B3). */
  if (gen !== _gen) return;
  _meldung = meldung;
  _laeuft = false;
  _aktionsAbruf = true;
  try { await lade(gen, true); } finally { if (gen === _gen) _aktionsAbruf = false; }
}

function stop() {
  _gen++;
  _boardId = null;
  _liste = [];
  _meldung = '';
  _laeuft = false;
  _veraltet = false;
  _sig = null;
  _anfrage++;
  _fokusNachAktion = false;
  _aktionsAbruf = false;
  if (_timer) { clearInterval(_timer); _timer = null; }
  if (_bereich) { _bereich.remove(); _bereich = null; _live = null; }
}

async function beginne(boardId) {
  const gen = ++_gen;
  _boardId = boardId;
  const seite = await boardpaarSeite();
  if (gen !== _gen || seite !== 'aussen') return; /* aus/innen: kein UI, kein Abruf */
  const r = await ladeVorschlaege(boardId);
  if (gen !== _gen) return;
  if (r.status === 'keine') return; /* nicht editor oder nicht außen: keine Uhr */
  if (r.status === 'ok') _liste = r.liste;
  /* Scheitert schon der erste Abruf, sieht der Editor den Hinweis statt nichts —
     es kann offene Vorschläge geben (Sprint-91-Kritiker F-E). */
  _veraltet = r.status === 'fehler';
  zeichne();
  if (_timer) return;
  _timer = setInterval(() => { if (gen === _gen && !_laeuft && !_aktionsAbruf) lade(gen); }, TICK_MS);
}

function beiStandWechsel() {
  const id = State.get()?.board?.id ?? null;
  if (id === _boardId) return;
  stop();
  if (id) beginne(id);
}

/** Einmal beim Start (main.js, nur Server-Build). */
function init() {
  if (!IS_SERVER_BUILD || _initialisiert) return;
  _initialisiert = true;
  EventBus.on('state:changed', beiStandWechsel);
  beiStandWechsel();
}

/* ── Darstellung ──────────────────────────────────────────────────────── */

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}

/** Name eines Felds: übersetzt über den festen Schlüssel, sonst der Name vom Server. */
export function feldName(art, f) {
  const key = 'vorschlag.feld.' + (art === 'karte.text' ? 'neu-' : '') + f.schluessel;
  const t = I18n.t(key);
  return t && t !== key ? t : (f.name ?? '');
}

/** Zeitpunkt (UTC, RFC 3339) als Ortszeit „TT.MM. HH:MM“; leer bei ungültigem Wert. */
export function zeitKurz(iso) {
  const t = Date.parse(iso ?? '');
  if (!Number.isFinite(t)) return '';
  const d = new Date(t);
  const z = (n) => String(n).padStart(2, '0');
  return `${z(d.getDate())}.${z(d.getMonth() + 1)}. ${z(d.getHours())}:${z(d.getMinutes())}`;
}

function eintrag(v) {
  if (!IS_SERVER_BUILD) return null;
  const li = el('li', 'vsl-eintrag');
  const kopf = el('div', 'vsl-kopf');
  const artKey = 'vorschlag.art.' + v.art;
  const artText = I18n.t(artKey);
  const artName = artText && artText !== artKey ? artText : (v.artText ?? v.art);
  kopf.appendChild(el('strong', 'vsl-art', artName));
  kopf.appendChild(el('span', 'vsl-von', I18n.t('vorschlag.von', v.agent ?? '', v.fuer ?? '')));
  const ab = zeitKurz(v.laeuftAb);
  if (ab) kopf.appendChild(el('span', 'vsl-ablauf', I18n.t('vorschlag.laeuft-ab', ab)));
  li.appendChild(kopf);
  if (v.karte) li.appendChild(el('div', 'vsl-karte', I18n.t('vorschlag.karte', v.karte.displayId, v.karte.titel ?? '')));
  if (v.karteFehlt) li.appendChild(el('div', 'vsl-karte vsl-warn', I18n.t('vorschlag.karte-fehlt')));
  const gesperrt = !!v.karteFehlt || v.zugangAktiv === false;
  if (v.zugangAktiv === false) li.appendChild(el('div', 'vsl-karte vsl-warn', I18n.t('vorschlag.zugang-weg')));
  const dl = el('dl', 'vsl-felder');
  for (const f of Array.isArray(v.felder) ? v.felder : []) {
    dl.appendChild(el('dt', null, feldName(v.art, f)));
    /* Feste Wirkungstexte übersetzt; alles andere ist Text des Agenten, wörtlich. */
    const wertKey = 'vorschlag.wert.' + f.schluessel;
    const wertText = String(f.schluessel ?? '').startsWith('wirkung-') ? I18n.t(wertKey) : '';
    dl.appendChild(el('dd', null, wertText && wertText !== wertKey ? wertText : (f.wert ?? '')));
  }
  li.appendChild(dl);
  const knoepfe = el('div', 'vsl-knoepfe');
  /* Bei mehreren Vorschlägen hört ein Screenreader sonst N-mal „Bestätigen“ ohne Bezug
     (Sprint-91-Kritiker F-D); der Bezug aus vorhandenen Texten, kein neuer Schlüssel. */
  const bezug = [artName, v.karte ? I18n.t('vorschlag.karte', v.karte.displayId, v.karte.titel ?? '') : ''].filter(Boolean).join(', ');
  const ja = el('button', 'btn btn-primary btn-sm vsl-ja', I18n.t('vorschlag.bestaetigen'));
  ja.type = 'button';
  ja.setAttribute('aria-label', I18n.t('vorschlag.bestaetigen') + ': ' + bezug);
  ja.dataset.vid = v.id;
  ja.dataset.fehlt = gesperrt ? '1' : '';
  ja.disabled = _laeuft || gesperrt;
  ja.addEventListener('click', () => aktion(v, true));
  const nein = el('button', 'btn btn-sm vsl-nein', I18n.t('vorschlag.ablehnen'));
  nein.type = 'button';
  nein.setAttribute('aria-label', I18n.t('vorschlag.ablehnen') + ': ' + bezug);
  nein.dataset.vid = v.id;
  nein.disabled = _laeuft;
  nein.addEventListener('click', () => aktion(v, false));
  knoepfe.append(ja, nein);
  li.appendChild(knoepfe);
  return li;
}

/** Während einer Aktion nur die Knöpfe sperren — ohne Neubau, Fokus und Scrollstand bleiben. */
function sperreKnoepfe() {
  for (const b of _bereich?.querySelectorAll('.vsl-ja, .vsl-nein') ?? []) b.disabled = _laeuft || (b.classList.contains('vsl-ja') && !!b.dataset.fehlt);
}

function zeichne() {
  if (!IS_SERVER_BUILD) return;
  if (_liste.length === 0 && !_meldung && !_veraltet) {
    if (_bereich) { _bereich.remove(); _bereich = null; _live = null; }
    _sig = null;
    return;
  }
  if (!_bereich?.isConnected) {
    ensureStyles();
    _bereich = el('section', 'vsl-bereich');
    _bereich.id = 'vorschlaege-aussen';
    _bereich.setAttribute('aria-label', I18n.t('vorschlag.titel'));
    _live = el('p', 'vsl-meldung');
    _live.setAttribute('role', 'status');
    _bereich.appendChild(_live);
    const anker = document.getElementById('schatten-aussen') ?? document.getElementById('board-header');
    if (anker?.parentNode) anker.after(_bereich);
    else (document.getElementById('app') ?? document.body).prepend(_bereich);
    _sig = null;
  }
  /* Die Live-Region bleibt dasselbe Element; nur ihr Text ändert sich (K-B2). */
  const meldung = [_meldung, _veraltet ? I18n.t('vorschlag.veraltet') : ''].filter(Boolean).join(' ');
  if (_live.textContent !== meldung) _live.textContent = meldung;
  /* Alles, was angezeigt wird — auch Feldwerte, die der Server aus dem Board auflöst
     (Spaltenname), und der Agent (Verify V-9). */
  const sig = JSON.stringify(_liste.map(v => [v.id, v.hash, v.karteFehlt, v.zugangAktiv, v.karte?.displayId, v.karte?.titel,
    v.agent, v.fuer, v.laeuftAb, (v.felder ?? []).map(f => [f.schluessel, f.wert])]));
  if (sig === _sig) {
    sperreKnoepfe();
    if (_fokusNachAktion && !_laeuft) { _fokusNachAktion = false; zielNachAktion()?.focus(); }
    return;
  }
  _sig = sig;
  /* Neu gebaut wird nur bei geänderter Liste — dann Fokus und Scrollstand mitnehmen. */
  const nachAktion = _fokusNachAktion && !_laeuft;
  if (nachAktion) _fokusNachAktion = false;
  const fokus = _bereich.contains(document.activeElement) ? document.activeElement : null;
  const fokusVid = fokus?.dataset?.vid ?? null;
  const fokusJa = !!fokus?.classList?.contains('vsl-ja');
  const scroll = _bereich.querySelector('.vsl-liste')?.scrollTop ?? 0;
  for (const c of [..._bereich.children]) if (c !== _live) c.remove();
  const titel = el('div', 'vsl-titel', I18n.t('vorschlag.titel') + ' (' + _liste.length + ')');
  titel.tabIndex = -1;
  _bereich.insertBefore(titel, _live);
  if (_liste.length) _bereich.insertBefore(el('p', 'vsl-hinweis', I18n.t('vorschlag.hinweis')), _live);
  const ul = el('ul', 'vsl-liste');
  for (const v of _liste) ul.appendChild(eintrag(v));
  _bereich.appendChild(ul);
  ul.scrollTop = scroll;
  if (fokus) {
    const ziel = fokusVid && ul.querySelector((fokusJa ? '.vsl-ja' : '.vsl-nein') + `[data-vid="${CSS.escape(fokusVid)}"]`);
    (ziel && !ziel.disabled ? ziel : titel).focus();
  } else if (nachAktion) {
    titel.focus();
  }
}

/** Fokusziel nach einer Aktion ohne Neubau: der erste freie Knopf, sonst der Titel. */
function zielNachAktion() {
  return _bereich?.querySelector('.vsl-ja:not([disabled]), .vsl-nein:not([disabled])') ?? _bereich?.querySelector('.vsl-titel');
}

function ensureStyles() {
  if (document.getElementById('vsl-styles')) return;
  const st = document.createElement('style');
  st.id = 'vsl-styles';
  st.textContent = '.vsl-bereich{padding:8px 20px;border-bottom:1px solid var(--border);background:var(--surface);color:var(--text)}.vsl-titel{font-weight:600;font-size:13px}.vsl-hinweis,.vsl-meldung{margin:4px 0;font-size:12px;color:var(--text-muted)}.vsl-liste{list-style:none;margin:6px 0 0;padding:0;display:flex;flex-direction:column;gap:8px;max-height:40vh;overflow-y:auto}.vsl-eintrag{border:1px solid var(--border);border-radius:6px;padding:8px 10px;background:var(--surface-2)}.vsl-kopf{display:flex;flex-wrap:wrap;gap:4px 10px;align-items:baseline;font-size:12px}.vsl-von,.vsl-ablauf{color:var(--text-muted)}.vsl-karte{font-size:12px;margin-top:2px;overflow-wrap:anywhere}.vsl-warn{color:var(--prio-high-fg)}.vsl-felder{margin:6px 0;display:grid;grid-template-columns:max-content 1fr;gap:2px 10px;font-size:13px}.vsl-felder dt{color:var(--text-muted)}.vsl-felder dd{margin:0;white-space:pre-wrap;overflow-wrap:anywhere}.vsl-knoepfe{display:flex;flex-wrap:wrap;gap:8px}@media (max-width:640px){.vsl-bereich{padding:8px 14px}.vsl-felder{grid-template-columns:1fr}}';
  document.head.appendChild(st);
}

/** Test-only: Zustand zurücksetzen. */
function _resetForTest() {
  stop();
  _initialisiert = false;
  EventBus.off('state:changed', beiStandWechsel);
}

export const VorschlaegeAussen = { init, stop, ladeJetzt: () => (_boardId ? lade(_gen) : Promise.resolve()), _resetForTest };
