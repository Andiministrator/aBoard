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
    const eintrag = (b, gruppe, g, subtitle) => ({ id: b.id, label: b.name, event: 'sync:switch-to-board', gruppe, g, subtitle });
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

export const BoardMenu = { build };
