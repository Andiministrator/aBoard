/* Board-Titel im Kopf mit Umschalt-Menü und Umbenennen-Stift (Karte #704).

   Ersetzt die frühere „Boards“-Sektion der Seitenleiste:
   - Klick auf den Titel öffnet ein Aufklappmenü unter dem Titel (aktuelles Board,
     die 5 zuletzt benutzten übrigen Boards, „Alle Boards anzeigen…“, im Server
     „Neues Board“). „Zuletzt benutzt“ = Liste der zuletzt gezeigten Board-IDs je
     Browser (localStorage, nur Komfort; fehlt sie, gilt die Quellreihenfolge),
     fortgeschrieben in build() — also bei jedem Board, das der Kopf zeigt, egal ob
     über Menü, Übersicht, Datei öffnen oder Deep-Link geladen. Server und lokal
     haben getrennte Listen. „Alle Boards anzeigen…“ öffnet im Server die
     Übersicht #624, lokal klappt es die volle Liste im Menü auf. Ein „Neues
     Board“ gibt es lokal nicht: der Listener `board:new-standalone` hatte seit
     b2303cb4 (#195, vor v1.20.0) keinen Auslöser und ist in Sprint 84 entfernt
     worden (F-1743/F-1744).
   - Der Stift ✎ rechts daneben (Hover, Tastaturfokus, auf Touch immer) startet
     das Inline-Umbenennen — nur wer umbenennen darf (Server: Rolle ≥ editor).

   Muster wie die Bulk-Menüs (F-1718): Inhalt entsteht erst beim Öffnen,
   geschlossen ist das Menü `inert` + leer. Ein offenes Menü übersteht einen
   Neuaufbau des Kopfes (F-1732, Zustand in `offenInst`). Der Außenklick-Listener hängt nur,
   solange das Menü offen ist (F-1729); ein spät fertiger Ladelauf baut kein
   bereits bedientes oder geschlossenes Menü neu, dafür zählt der Öffnungslauf
   (F-1730).

   Server-Zweige hinter IS_SERVER_BUILD (DCE im Solo-/Claude-Code-Build). */

import { EventBus }      from './eventbus.js';
import { State }         from './state.js';
import { I18n }          from './i18n.js';
import { Storage }       from './storage.js';
import { StoragePlugin } from './storage-plugin.js';
import { Profile }       from './profile.js';
import { BoardLoeschen } from './board-loeschen.js';
import { eigeneAwaitingSchluessel } from './awaiting.js';

/** Liegt ein offener modaler Dialog über der Seite (Hilfe, Karte, Einstellungen …)?
    Geschlossene tragen `aria-hidden="true"` an sich oder einem Vorfahren oder sind
    entfernt. Die Escape-Capture-Handler der Menüs lassen ihm dann das Escape
    (Sprint-85-Kritiker: sonst schloss Escape das Hilfe-Overlay nicht mehr). */
export const dialogOffen = () => [...document.querySelectorAll('[aria-modal="true"]')]
  .some(d => !d.closest('[aria-hidden="true"]'));

/** Steht der Fokus in einem Eingabefeld außerhalb des Menüs (Suche, Karten-Formular,
    Umbenennen)? Dann gehört ihm das Escape (Abbrechen/Leeren) — Sitzungs-QA Sprint 85.
    Ein Knopf außerhalb zählt nicht: dort schließt Escape weiter das Menü. */
export const fremderFokus = wrap => {
  const a = document.activeElement;
  return !!a && !wrap.contains(a) && !a.closest('[inert]') && (a.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName));
};

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');
const IS_PROJECTOR_BUILD = (typeof __PROJECTOR_BUILD__ !== 'undefined') && (__PROJECTOR_BUILD__ === true || __PROJECTOR_BUILD__ === 'true');

/* So viele zuletzt benutzte Boards stehen höchstens direkt im Menü. */
const MAX_BOARDS = 5;

/* „Zuletzt benutzt“: Board-IDs, jüngste zuerst, je Browser. Server-IDs und
   Datei-IDs getrennt; lokal zusätzlich je Pfad (file://, vgl. storage.js). */
const zKey = () => 'aboard-recent:' + (IS_SERVER_BUILD && Profile.has('feature.restStorage') ? 'srv' : location.pathname);
const zuletzt = () => { try { return [].concat(JSON.parse(localStorage.getItem(zKey())) || []); } catch { return []; } };
function merkeBenutzt(id) {
  try {
    const l = zuletzt();
    l[0] === id || localStorage.setItem(zKey(), JSON.stringify([id, ...l.filter(x => x !== id)].slice(0, 30)));
  } catch { /* nur Komfort */ }
}
/* Stabil nach „zuletzt benutzt“; unbekannte Boards behalten ihre Quellreihenfolge hinten. */
function nachZuletzt(liste) {
  const z = zuletzt();
  return liste.sort((a, b) => (z.indexOf(a.id) + 1 || 1e9) - (z.indexOf(b.id) + 1 || 1e9));
}

/* Das gerade offene Menü (F-1732): überlebt renderHeader, das den Kopf neu baut. */
let offenInst = null;

/* Eigene Rolle je Board-id (Server, aus GET /boards). Eintrag fehlt = noch
   unbekannt → Stift versteckt; { role: undefined } = bekannt, aber ohne Rolle
   (Legacy-Bypass oder Board noch nicht in der Liste) → Stift erlaubt. */
const _rollen = new Map();
const ROLLEN_TTL_MS = 60000;
let _rollenLauf = null;
let _rollenFehler = 0; /* Zeitpunkt des letzten Fehlversuchs (F-1733: Negativ-Cache) */
let _listeFehler = 0;  /* dasselbe für den Listen-Neuabruf; getrennt, sonst versteckt er den Stift (F-1751) */

function ladeRollen() {
  if (!IS_SERVER_BUILD) return Promise.resolve();
  if (_rollenLauf) return _rollenLauf;
  _rollenLauf = (async () => {
    try {
      const boards = await StoragePlugin.getActivePlugin().listKnownBoards?.();
      merkeRollen(boards);
      _rollenFehler = 0;
    } catch { _rollenFehler = Date.now(); /* Stift bleibt versteckt — der Server lehnt Schreiben ohnehin ab */ }
    finally { _rollenLauf = null; }
  })();
  return _rollenLauf;
}

function merkeRollen(boards) {
  const jetzt = Date.now();
  const id = State.get()?.board?.id;
  for (const b of boards ?? []) _rollen.set(b.id, { role: b.role, ts: jetzt });
  if (id && !_rollen.has(id)) _rollen.set(id, { role: undefined, ts: jetzt });
}

function darfUmbenennen(boardId) {
  if (!IS_SERVER_BUILD || !Profile.has('feature.restStorage')) return true;
  const e = _rollen.get(boardId);
  return !!e && e.role !== 'viewer';
}

function el(tag, cls, text, attrs) {
  const n = document.createElement(tag);
  n.className = cls;
  if (text != null) n.textContent = text;
  for (const k in attrs) n.setAttribute(k, attrs[k]);
  return n;
}

/* Menüeintrag; Aussehen von .bulk-dropdown-item, Untertitel als <small>. */
function bauePunkt(label, onSelect, opts = {}) {
  const btn = el('button', 'bulk-dropdown-item board-menu-item', label,
    { type: 'button', role: 'menuitem', tabindex: -1 });
  if (opts.subtitle) btn.appendChild(el('small', '', opts.subtitle));
  if (opts.current) { btn.classList.add('board-menu-item--current'); btn.setAttribute('aria-current', 'true'); }
  btn.addEventListener('click', onSelect);
  return btn;
}

/** Baut den Titelblock für den Kopf.
    @param {object} board  aktuelles Board
    @param {{onRename: Function}} opts  onRename(wrap) startet das Inline-Umbenennen */
function build(board, { onRename }) {
  merkeBenutzt(board.id);
  const wechsel = I18n.t('board.menu.switch');
  const vorher = offenInst;
  vorher?.ende();
  const wrap = el('div', 'board-title-wrap');
  const h1 = el('h1', 'board-title-h1');
  /* Dirty-Indikator im sichtbaren Board-Titel; das ▾ kommt per CSS (::after). */
  const titel = el('button', '', (State.isDirty() ? '* ' : '') + board.title,
    { type: 'button', id: 'board-title', title: wechsel, 'aria-haspopup': 'menu', 'aria-expanded': 'false' });
  h1.appendChild(titel);
  wrap.appendChild(h1);

  const stift = el('button', 'board-title-edit', '✎',
    { type: 'button', title: I18n.t('col.rename-tip'), 'aria-label': I18n.t('board.rename') });
  stift.addEventListener('click', () => { schliessen(false); onRename(wrap); });
  wrap.appendChild(stift);

  const setzeStift = () => { stift.hidden = !darfUmbenennen(board.id); };
  setzeStift();
  if (IS_SERVER_BUILD && Profile.has('feature.restStorage')) {
    const e = _rollen.get(board.id);
    if ((!e || Date.now() - e.ts > ROLLEN_TTL_MS) && Date.now() - _rollenFehler > ROLLEN_TTL_MS) {
      ladeRollen().then(() => { if (wrap.isConnected) setzeStift(); });
    }
  }

  const menu = el('div', 'board-menu', null, { role: 'menu', 'aria-label': wechsel, inert: '' });
  menu.hidden = true;
  wrap.appendChild(menu);

  let offen = false;
  let lauf = 0; /* Nummer des jüngsten Öffnungslaufs (F-1730) */
  /* inst.alle: lokal die volle Liste aufgeklappt; inst.fokus: Index des fokussierten Eintrags */
  const inst = { id: board.id, wrap, fokus: 0,
    ende() { offen = false; lauf++; document.removeEventListener('pointerdown', aussen, true); document.removeEventListener('focusin', merkeFokus, true); document.removeEventListener('keydown', esc, true); if (offenInst === inst) offenInst = null; } };
  /* F-1738: liegt der Fokus im Menü? Der Kopf ist beim Neuaufbau schon geleert (activeElement = body). */
  function merkeFokus(e) { inst.imMenu = menu.contains(e.target); }

  function punkte() { return [...menu.querySelectorAll('button')]; }
  function fokus(i) {
    const p = punkte();
    if (p.length) p[inst.fokus = (i + p.length) % p.length].focus(); /* inst.fokus: für das Wiederöffnen nach einem Neuaufbau */
  }

  function schliessen(fokusZurueck = true) {
    if (!offen) return;
    inst.ende();
    inst.alle = false;
    const hatteFokus = menu.contains(document.activeElement);
    menu.setAttribute('inert', '');
    menu.hidden = true;
    menu.textContent = '';
    titel.setAttribute('aria-expanded', 'false');
    if (fokusZurueck && hatteFokus) titel.focus();
  }

  /* Escape im Capture, solange offen: der Fokus kann auf body stehen (Kopf-Klick,
     Safari/Firefox auf macOS), dann erreicht Escape `wrap` nie (F-1785, Klasse 149). */
  function esc(e) {
    if (e.key !== 'Escape') return;
    if (!wrap.isConnected) { inst.ende(); return; }
    if (dialogOffen() || fremderFokus(wrap)) return; /* das Escape gehört dem Dialog darüber bzw. dem fokussierten Feld */
    e.preventDefault(); e.stopPropagation(); schliessen(); titel.focus();
  }

  function aussen(e) {
    if (!wrap.isConnected) { inst.ende(); return; }
    if (!wrap.contains(e.target)) schliessen(false);
  }

  /* Überschriften im role="menu": nur presentation/group/separator/menuitem erlaubt. */
  const kopf = text => menu.appendChild(el('div', 'board-menu-head', text, { role: 'presentation' }));

  function rendern(daten) {
    menu.textContent = '';
    kopf(I18n.t('board.switch.current'));
    menu.appendChild(bauePunkt(daten.name, () => schliessen(), { current: true, subtitle: daten.rolle }));
    if (!daten.liste.length) kopf(I18n.t(IS_SERVER_BUILD && daten.fehler ? 'board.switch.error' : 'board.switch.none')) /* Fehlertext nur im Server-Build (F-1768) */;
    /* Gruppen zusammenrücken (g = Gruppenrang); aufgeklappt bleiben die 5 sichtbaren vorn (F-1741). */
    const nachG = l => l.slice().sort((a, c) => a.g - c.g);
    const sicht = nachG(daten.liste.slice(0, MAX_BOARDS));
    if (inst.alle) sicht.push(...nachG(daten.liste.slice(MAX_BOARDS)));
    let gruppe;
    sicht.forEach(e => {
      if (e.gruppe !== gruppe) kopf(I18n.t(gruppe = e.gruppe)); /* auch im angehängten Rest bei jedem Wechsel (F-1749) */
      menu.appendChild(bauePunkt(e.label, () => { schliessen(false); EventBus.emit(e.event, { boardId: e.id }); },
        { subtitle: e.subtitle }));
    });
    if (IS_SERVER_BUILD && daten.server) {
      /* #624: Übersicht aller sichtbaren Boards mit kopierbarer Board-ID. */
      menu.appendChild(bauePunkt(I18n.t('board.switch.all-boards'),
        () => { schliessen(false); EventBus.emit('boards-view:open'); }));
      menu.appendChild(bauePunkt(I18n.t('board.switch.new-server'), () => {
        schliessen(false);
        const t = window.prompt(I18n.t('board.switch.new-server-placeholder'), '');
        if (t?.trim()) EventBus.emit('server:new-board', { title: t.trim() });
      }));
      /* #845: Löschen nur für Eigentümer des aktuellen Boards (der Server prüft es ohnehin). */
      if (daten.eigentuemer) {
        menu.appendChild(bauePunkt(I18n.t('board.delete.menu'), () => {
          schliessen(false);
          BoardLoeschen?.open({ id: board.id, title: board.title });
        }));
      }
    } else if (!daten.server && !inst.alle && daten.liste.length > MAX_BOARDS) {
      /* Lokal gibt es keine Übersicht: die volle Liste klappt im Menü auf. */
      menu.appendChild(bauePunkt(I18n.t('board.switch.all-boards'), () => {
        inst.alle = true; rendern(daten); fokus(1 + MAX_BOARDS);
      }));
    }
    menu.removeAttribute('inert');
    menu.hidden = false;
  }

  /* `alt` = Zustand des vor einem Neuaufbau offenen Menüs (F-1732): Daten,
     aufgeklappte Liste und Fokus werden übernommen. */
  async function oeffnen(alt) {
    offen = true;
    const mein = ++lauf;
    offenInst = inst;
    if (alt) inst.alle = alt.alle;
    titel.setAttribute('aria-expanded', 'true');
    document.addEventListener('pointerdown', aussen, true);
    document.addEventListener('focusin', merkeFokus, true);
    document.addEventListener('keydown', esc, true);
    if (alt) inst.imMenu = alt.imMenu;
    /* Ohne alt.daten läuft das erste Laden noch (F-1747) → wie beim Erstöffnen laden. */
    const alte = alt?.daten;
    /* Der Neuabruf respektiert den Negativ-Cache (F-1748). */
    const frisch = alte && Date.now() - _listeFehler > ROLLEN_TTL_MS ? ladeDaten(board) : null;
    /* Name aus dem aktuellen Board (F-1739); die Liste lädt im Hintergrund neu (unten). */
    const daten = alte ? { ...alte, name: (board.title || '').trim() || board.id || '' } : await ladeDaten(board); /* immer ein Await: der Kopf ist erst nach build() im DOM */
    if (alte) await 0;
    inst.daten = daten;
    /* Zwischenzeitlich geschlossen oder neu geöffnet → nichts mehr anfassen. */
    if (!offen || mein !== lauf || !wrap.isConnected) return;
    if (daten.rollenBekannt) setzeStift();
    rendern(daten);
    if (!alte) fokus(0); /* auch beim Neuaufbau während des ersten Ladens (F-1753) */
    else if (alt.imMenu) fokus(alt.fokus); /* Fokus stand draußen → nicht ins Menü ziehen (F-1738) */
    /* Frische Liste/Rolle (F-1739) — ein gerade bedientes Menü nicht unter dem Cursor umbauen (F-1730-Muster). */
    /* `mein === lauf` schließt „offen“ ein: ende() zählt lauf hoch. */
    frisch?.then(d => {
      if (!d.fehler && mein === lauf && wrap.isConnected && !menu.contains(document.activeElement)) { inst.daten = d; rendern(d); }
    });
  }

  titel.addEventListener('click', () => { if (offen) schliessen(); else oeffnen(); });
  wrap.addEventListener('keydown', e => {
    const imMenu = menu.contains(document.activeElement);
    if (e.key === 'Tab' && offen) { schliessen(false); return; }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      if (!offen && document.activeElement === titel) { e.preventDefault(); oeffnen(); return; }
      if (imMenu) {
        e.preventDefault();
        const p = punkte();
        const i = p.indexOf(document.activeElement);
        fokus(i + (e.key === 'ArrowDown' ? 1 : -1));
      }
    }
  });

  /* Kopf wurde bei offenem Menü neu gebaut: Menü mit demselben Board wieder öffnen. */
  if (vorher && vorher.id === board.id && !vorher.wrap.isConnected) {
    oeffnen(vorher);
  }

  return wrap;
}

const lies = async f => { try { return (await f()) || []; } catch { return []; /* Liste optional — kein Hard-Fail */ } };

/* Server: GET /boards — nur im Server-Build (DCE sonst), die i18n-Keys der
   Rollen sind Server-only. */
let ladeServer = null;
if (IS_SERVER_BUILD) {
  ladeServer = async function ladeServer(board) {
    const id = board.id;
    const rolle = b => b.role === 'editor' ? I18n.t('share.role-editor') : I18n.t('share.role-viewer');
    let fehler = false; /* Fehlschlag ≠ leer: der Neuabruf behält dann die alte Liste (F-1748) */
    const known = await lies(async () => {
      try {
        const l = await StoragePlugin.getActivePlugin().listKnownBoards();
        merkeRollen(l);
        _rollenFehler = _listeFehler = 0;
        return l;
      } catch (e) { fehler = true; _listeFehler = Date.now(); throw e; }
    });
    const ich = known.find(b => b.id === id);
    const andere = nachZuletzt(known.filter(b => b.id !== id)
      .sort((a, c) => (c.updatedAt || 0) - (a.updatedAt || 0)));
    /* Rollen nur gruppieren, wenn der Server sie liefert (ADR-0059). */
    const mitRollen = andere.some(b => b.role != null);
    const eintrag = (b, gruppe, g, subtitle) => ({ id: b.id, label: b.name, event: 'sync:switch-to-board', gruppe, g, subtitle,
                                                    awaitingUser: b.awaitingUser ?? null /* #1197 */ });
    return {
      server: true,
      fehler,
      rollenBekannt: known.length > 0,
      eigentuemer: ich?.role === 'owner', /* #845: Menüpunkt „Board löschen…“ */
      name: (board.title || '').trim() || id || '',
      rolle: ich && ich.role !== 'owner' && ich.role ? rolle(ich) : '',
      liste: !mitRollen ? andere.map(b => eintrag(b, 'board.switch.server-boards', 0))
        : andere.filter(b => b.role === 'owner' || b.role === 'editor' || b.role === 'viewer')
            .map(b => b.role === 'owner' ? eintrag(b, 'board.switch.own-boards', 0) : eintrag(b, 'board.switch.shared-boards', 1, rolle(b))),
    };
  };
}

/* Quellen wie früher in der Sidebar: Server → GET /boards (Quellreihenfolge:
   nach letzter Änderung); lokal → Claude-Code-Projekte, dann lokale Dateien in
   der Reihenfolge des Speichers. Beide nach „zuletzt benutzt“ geordnet. Der
   Standalone-Plugin liefert dieselbe Liste wie Storage.listKnownFiles —
   Treffer dort zählen nur einmal, als Datei. */
async function ladeDaten(board) {
  if (IS_SERVER_BUILD && Profile.has('feature.restStorage')) return ladeServer(board);
  const id = board.id;
  const dateien = (await lies(() => Storage.listKnownFiles())).filter(f => f.id !== id);
  const projekte = (await lies(() => StoragePlugin.getActivePlugin().listKnownBoards()))
    .filter(b => b.id !== id && !dateien.some(f => f.id === b.id));
  return {
    name: (board.title || '').trim() || id || '',
    liste: nachZuletzt([
      ...projekte.map(b => ({ id: b.id, label: b.name, event: 'sync:switch-to-board', gruppe: 'board.switch.claude-code', g: 0 })),
      ...dateien.map(f => {
        /* F-144: `f.title` erst trimmen — ein Titel aus reinem Whitespace ist
           truthy und erzeugte sonst ein leeres Label plus leeren Untertitel. */
        const t = (f.title ?? '').trim();
        return { id: f.id, label: t || f.name || f.id, event: 'standalone:switch-to-file',
                 subtitle: t && f.name ? f.name : '', gruppe: 'board.switch.local', g: 1 };
      }),
    ]),
  };
}

/* Karte #1195: Reiterleiste unter dem Kopf — die zuletzt benutzten Boards als Reiter
   (Idee aus einem anderen Web-Portal). Ein Schnellwechsler, keine gleichzeitig geladenen
   Boards: Klick = derselbe Wechsel wie im Menü. Reihenfolge stabil wie Browser-Tabs
   (neue hinten); über MAX_REITER fällt der am längsten nicht benutzte heraus, nie das
   aktuelle. × nimmt einen Reiter heraus. Unter zwei Reitern bleibt die Leiste
   unsichtbar (eine Datei allein: keine Leiste). Je Browser in localStorage, nur Komfort.
   Die Boardliste wird je Board höchstens alle 60 s neu geholt — renderHeader läuft oft;
   sie enthält das aktuelle Board nicht, deshalb gilt der Zwischenspeicher nur für dasselbe Board. */
/* Nur Server- und Claude-Code-Build: dort gibt es typisch mehrere Boards; im Solo-Template
   (meist eine Datei, Bundle-Limit ADR-0114) fällt alles per DCE weg, CSS kommt mit dem JS. */
let reiter = null;
if (IS_SERVER_BUILD || IS_PROJECTOR_BUILD) {
  const REITER_CSS = '.board-reiter{display:flex;gap:4px;padding:6px 20px 0;overflow-x:auto;flex-shrink:0;background:var(--bg);border-bottom:1px solid var(--border)}.board-reiter[hidden]{display:none}.board-reiter-tab{display:flex;align-items:center;flex-shrink:0;margin-bottom:-1px;background:var(--surface-2);border:1px solid var(--border);border-bottom:none;border-radius:var(--radius) var(--radius) 0 0;color:var(--text-muted)}.board-reiter-tab--aktiv{background:var(--surface);color:var(--text);font-weight:600;border-bottom:1px solid var(--surface)}.board-reiter-knopf{background:none;border:0;color:inherit;font:inherit;cursor:pointer;padding:6px 12px;max-width:16em;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.board-reiter-tab--aktiv .board-reiter-knopf{cursor:default}.board-reiter-zu{background:none;border:0;color:inherit;cursor:pointer;padding:0 8px 0 0;opacity:.6}.board-reiter-zu:hover,.board-reiter-zu:focus-visible{opacity:1}.board-reiter-wartet{margin-right:4px}@media (max-width:600px){.board-reiter{padding:4px 8px 0}}';
  const MAX_REITER = 6;
  const rKey = () => zKey().replace('aboard-recent:', 'aboard-reiter:');
  const reiterIds = () => { try { return [].concat(JSON.parse(localStorage.getItem(rKey())) || []); } catch { return []; } };
  const setzeReiter = l => { try { localStorage.setItem(rKey(), JSON.stringify(l)); } catch { /* nur Komfort */ } };
  /* Sitzungs-Kritik 23.11. A-2: je Board-Schlüssel höchstens ein laufender Abruf; ein Fehlschlag
     sperrt 60 s (wie _listeFehler im Menü), solange gilt die letzte gute Liste. */
  const _reiterDaten = new Map(); /* key → { ts, liste } */
  const _reiterLauf = new Map();  /* key → Promise */
  let _reiterFehler = 0;

  function holeListe(board) {
    const key = rKey() + board.id;
    const c = _reiterDaten.get(key);
    const frisch = c && Date.now() - c.ts < ROLLEN_TTL_MS;
    if (frisch || (c && Date.now() - _reiterFehler < ROLLEN_TTL_MS)) return Promise.resolve(c.liste);
    if (Date.now() - _reiterFehler < ROLLEN_TTL_MS) return Promise.resolve([]);
    if (!_reiterLauf.has(key)) {
      _reiterLauf.set(key, ladeDaten(board).then(d => {
        if (d.fehler) { _reiterFehler = Date.now(); return c ? c.liste : d.liste; }
        _reiterDaten.set(key, { ts: Date.now(), liste: d.liste });
        return d.liste;
      }).finally(() => _reiterLauf.delete(key)));
    }
    return _reiterLauf.get(key);
  }

  /* Karte #1197 (ADR-0156): Wartet auf einem ANDEREN Board eine Frage auf mich? Dieselbe
     Zuordnung wie der Banner (eigeneAwaitingSchluessel, Server-Regel: nur passende Personen).
     Nur Server: GET /boards liefert awaitingUser; im Claude-Code-Build gibt es das Feld nicht. */
  const wartetAufMich = (e, personen) => IS_SERVER_BUILD && !!e?.awaitingUser
    && eigeneAwaitingSchluessel({ awaitingUser: e.awaitingUser }, personen, { server: true }).length > 0;

  /* Sitzungs-Kritik A-1/A-3: die Leiste lebt in einem festen Halter direkt unter dem Kopf
     (renderHeader legt ihn einmal an und verschiebt ihn nie), wird nur bei geändertem Inhalt
     neu gebaut und gibt den Fokus an denselben bzw. den Nachbar-Reiter zurück.
     `personen`: die eigene Person wie beim Banner (renderer.js eigenePerson), #1197. */
  reiter = function reiter(board, halter, { personen = [] } = {}) {
    if (!document.querySelector('style[data-aboard="board-reiter"]')) {
      document.head.appendChild(el('style', '', REITER_CSS, { 'data-aboard': 'board-reiter' }));
    }
    let ids = reiterIds();
    if (!ids.includes(board.id)) {
      ids.push(board.id);
      const z = zuletzt();
      const rang = i => { const p = z.indexOf(i); return p < 0 ? 1e9 : p; };
      while (ids.length > MAX_REITER) {
        const weg = ids.filter(i => i !== board.id).reduce((a, b) => (rang(b) > rang(a) ? b : a));
        ids = ids.filter(i => i !== weg);
      }
      setzeReiter(ids);
    }
    const zeige = (liste, fokusId) => {
      if (!halter.isConnected) return; /* Kopf inzwischen weg (Testaufbau, Ansichtswechsel) */
      const bekannt = new Map(liste.map(e => [e.id, e]));
      const sicht = reiterIds().filter(i => i === board.id || bekannt.has(i));
      const titel = (board.title || '').trim() || board.id;
      const sig = JSON.stringify([board.id, titel, I18n.t('board.reiter'),
        sicht.map(i => [i, bekannt.get(i)?.label, wartetAufMich(bekannt.get(i), personen)])]);
      const alt = halter.firstChild;
      if (alt && alt.dataset.sig === sig && fokusId === undefined) return;
      const a = document.activeElement;
      const fokus = fokusId ?? (alt && alt.contains(a) ? a.closest('.board-reiter-tab')?.dataset.id : null);
      const nav = el('nav', 'board-reiter', null, { 'aria-label': I18n.t('board.reiter') });
      nav.dataset.sig = sig;
      nav.hidden = sicht.length < 2;
      for (const id of sicht) {
        const e = id === board.id ? null : bekannt.get(id);
        const tab = el('div', e ? 'board-reiter-tab' : 'board-reiter-tab board-reiter-tab--aktiv');
        tab.dataset.id = id;
        const label = e ? e.label : titel;
        const wartet = wartetAufMich(e, personen);
        const wText = wartet ? I18n.t('board.reiter.wartet') : '';
        const knopf = el('button', 'board-reiter-knopf', label, { type: 'button',
          title: (e?.subtitle || label) + (wartet ? ' — ' + wText : '') });
        if (e) knopf.addEventListener('click', () => EventBus.emit(e.event, { boardId: id }));
        else knopf.setAttribute('aria-current', 'page');
        tab.appendChild(knopf);
        /* #1197, Klasse 340: Zeichen neben dem Knopf (lange Namen kürzt der Knopf ab); der Name
           des Knopfs beginnt mit dem sichtbaren Board-Namen und sagt den Zustand mit an. */
        if (wartet) {
          knopf.setAttribute('aria-label', `${label}, ${wText}`);
          tab.appendChild(el('span', 'board-reiter-wartet', '💬', { 'aria-hidden': 'true', title: wText }));
        }
        if (e) {
          const zuText = I18n.t('board.reiter.close', label);
          const zu = el('button', 'board-reiter-zu', '×', { type: 'button', 'aria-label': zuText, title: zuText });
          zu.addEventListener('click', () => {
            const i = sicht.indexOf(id);
            setzeReiter(reiterIds().filter(x => x !== id));
            zeige(liste, sicht[i + 1] ?? sicht[i - 1] ?? board.id);
          });
          tab.appendChild(zu);
        }
        nav.appendChild(tab);
      }
      halter.replaceChildren(nav);
      if (fokus && !nav.hidden) [...nav.children].find(t => t.dataset.id === fokus)?.firstChild.focus();
    };
    holeListe(board).then(liste => zeige(liste));
  };
}

export const BoardMenu = { build, reiter };
