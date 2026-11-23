/* App-Sidebar (Karte #176) — Slide-in von links via Logo-Klick.
   Konsolidiert die früher im Header verstreuten Aktionen:
   Settings, Öffnen, Exportieren/Backup. (Die Board-Auswahl wohnt seit
   Karte #704 im Menü am Board-Titel, board-menu.js.)

   Verhalten:
   - Overlay (über Inhalt), Backdrop-Klick schließt, Esc schließt.
   - Spring-Animation analog #175 C — reduced-motion respektiert.
   - Logo dient gleichzeitig als Hamburger-Trigger (siehe renderer.js).

   API:
   - Sidebar.init() — einmaliger DOM-Mount + EventBus-Subscribe.
   - Sidebar.open(board) — explizit öffnen (Inhalt rendert mit aktuellem Board).
   - Sidebar.close() — explizit schließen.

   Gehört bewusst NICHT in den Renderer-Code, weil die Sidebar
   board-übergreifend und persistent in body sitzt.
 */

import { EventBus }                from './eventbus.js';
import { State }                   from './state.js';
import { I18n }                    from './i18n.js';
import { Sync }                    from './sync.js';
import { StoragePlugin }           from './storage-plugin.js';
import { Profile }                 from './profile.js';
import { Sitzung }                 from './sitzung-abgelaufen.js'; /* #1053, nur Server-Build */
import { SettingsScope }           from './settings-scope.js';
import { Markdown }                from './markdown.js';
/* #306/ADR-0101: AdminDialog ist im Nicht-Server-Build null und alle
   Konsumenten-Sites hier sind hinter IS_SERVER_BUILD-Gates → DCE. */
import { AdminDialog }             from './admin-dialog.js';
/* #307/ADR-0102: Clients-+Projekte-Sektion liest Toggle-Status server-seitig. */
import { AdminClient }             from './admin-client.js';
/* #307/ADR-0102: View-Module — Liste + Detail für Clients und Projekte. */
import { ClientsView }             from './clients-view.js';
import { ProjectsView }            from './projects-view.js';
import { BoardsView }              from './boards-view.js';
import { CalendarView }            from './calendar-view.js';
/* Karte #739/ADR-0137: Lesesicht der Wissensquelle — im Nicht-Server-Build null. */
import { WissenView }              from './wissen-view.js';
/* Karte #1083/ADR-0149: Konto-Einstellungen — im Nicht-Server-Build null. */
import { KontoEinstellungen }      from './konto-einstellungen.js';

/* Compile-Time-Flag (Vite-Define, wie in config.js und storage-plugin.js):
   im Server-Frontend-Build auf true gesetzt. Bei IS_SERVER_BUILD=false
   eliminiert esbuild alle Server-UI-Blöcke per DCE (Karte #203 / ADR-0064). */
const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

/* ADR-0072 / Karte #274: Compile-Zeit-Flag für den Projektor-Build (aboard.html).
   IS_PROJECTOR_BUILD=true → README-Menüpunkt sichtbar.
   IS_PROJECTOR_BUILD=false (aboard.template.html) → esbuild eliminiert den
   Block per DCE → slim-Build wächst NICHT (Muster identisch zu sync.js/renderer.js). */
const IS_PROJECTOR_BUILD = (typeof __PROJECTOR_BUILD__ !== 'undefined') &&
  (__PROJECTOR_BUILD__ === true || __PROJECTOR_BUILD__ === 'true');

let _sidebar  = null;
let _backdrop = null;
let _isOpen   = false;
let _initialized = false;

function el(tag, opts = {}) {
  const e = document.createElement(tag);
  if (typeof opts === 'string') { e.className = opts; return e; }
  if (opts.cls)  e.className = opts.cls;
  if (opts.text != null) e.textContent = opts.text;
  if (opts.id)   e.id = opts.id;
  if (opts.type) e.setAttribute('type', opts.type);
  return e;
}

function buildShell() {
  /* Backdrop — fixed über alles, klick schließt. */
  _backdrop = el('div', { cls: 'app-sidebar-backdrop' });
  _backdrop.setAttribute('aria-hidden', 'true');
  _backdrop.addEventListener('click', close);

  /* Sidebar selbst — links, slide-in. */
  _sidebar = el('aside', { cls: 'app-sidebar' });
  _sidebar.setAttribute('role', 'dialog');
  _sidebar.setAttribute('aria-modal', 'true');
  _sidebar.setAttribute('aria-labelledby', 'app-sidebar-title');
  _sidebar.setAttribute('aria-hidden', 'true');
  _sidebar.tabIndex = -1;

  document.body.appendChild(_backdrop);
  document.body.appendChild(_sidebar);
}

function buildHeader() {
  const head = el('div', { cls: 'app-sidebar-header' });
  const title = el('div', { cls: 'app-sidebar-title', id: 'app-sidebar-title',
                             text: I18n.t('sidebar.title') });
  const closeBtn = el('button', { cls: 'btn-modal-close', type: 'button',
                                   text: '×' });
  closeBtn.setAttribute('aria-label', I18n.t('btn.close-modal'));
  closeBtn.addEventListener('click', close);
  head.appendChild(title);
  head.appendChild(closeBtn);
  return head;
}

function buildSection(titleKey) {
  const section = el('section', { cls: 'app-sidebar-section' });
  const title   = el('div', { cls: 'app-sidebar-section-title',
                              text: I18n.t(titleKey) });
  section.appendChild(title);
  return section;
}

function makeRow(label, onClick, opts = {}) {
  const btn = el('button', { cls: 'app-sidebar-row', type: 'button' });
  if (opts.icon) {
    const ico = el('span', { cls: 'app-sidebar-row-icon', text: opts.icon });
    ico.setAttribute('aria-hidden', 'true');
    btn.appendChild(ico);
  }
  const lbl = el('span', { cls: 'app-sidebar-row-label', text: label });
  btn.appendChild(lbl);
  btn.addEventListener('click', () => {
    if (!opts.keepOpen) close();
    if (typeof onClick === 'function') onClick();
  });
  return btn;
}

function buildFileSection() {
  const section = buildSection('sidebar.file');
  section.appendChild(makeRow(I18n.t('btn.open'),
    () => EventBus.emit('board:open'), { icon: '📂' }));
  section.appendChild(makeRow(I18n.t('btn.export-json'),
    () => EventBus.emit('board:export-json'), { icon: '⤓' }));
  return section;
}

/* #306/ADR-0101: Server-Administration-Section. Sichtbar genau dann, wenn
   IS_SERVER_BUILD + boardMode === 'server' + currentUser.is_admin === true.
   Lädt /me asynchron — Section wird angefügt sobald is_admin bestätigt ist.
   Im Slim/Claude-Code-Build wird der gesamte Block via DCE eliminiert
   (IS_SERVER_BUILD=false → Konsumenten unten gegated, AdminDialog null). */
let buildAdminSection = null;
/* #307/ADR-0102: Clients + Projekte — toggle-gated, async. */
let buildClientsProjectsSection = null;
/* Karte #739/ADR-0137: Wissen — sichtbar, wenn /me `wissen` (den Namen)
   liefert: der Server nennt ihn nur, wenn die Funktion an ist und die Person
   sie sehen darf (wissen.sichtbar). */
let buildWissenSection = null;
/* Karte #1053 (ADR-0148 §5): „Abmelden“ — nur, wenn /me `anmeldung: "oidc"` meldet. */
let buildAbmeldenSection = null;
if (IS_SERVER_BUILD) {
  buildAbmeldenSection = async function buildAbmeldenSection() {
    if (!Profile.has('feature.restStorage')) return null;
    let user = null;
    try {
      user = await StoragePlugin.getActivePlugin().getCurrentUser?.();
    } catch { /* Lookup-Fehler — kein Knopf */ }
    if (user?.anmeldung !== 'oidc') return null;
    const section = el('section', { cls: 'app-sidebar-section' }); /* ohne Titel: gehört optisch zu den Einstellungen */
    section.appendChild(makeRow(KontoEinstellungen.text('titel'),
      () => EventBus.emit('konto:open'), { icon: '👤' }));
    section.appendChild(makeRow(Sitzung.text('abmelden'),
      () => { Sitzung.abmelden(); }, { icon: '⎋' }));
    return section;
  };
  buildWissenSection = async function buildWissenSection() {
    if (!Profile.has('feature.restStorage')) return null;
    let user = null;
    try {
      user = await StoragePlugin.getActivePlugin().getCurrentUser?.();
    } catch { /* Lookup-Fehler — Section unsichtbar lassen */ }
    const name = typeof user?.wissen === 'string' ? user.wissen : '';
    if (!name) return null;
    const section = buildSection('sidebar.wissen');
    section.appendChild(makeRow(name,
      () => EventBus.emit('wissen-view:open', name), { icon: '📚' }));
    return section;
  };
  buildAdminSection = async function buildAdminSection() {
    const isServerMode = Profile.has('feature.restStorage');
    if (!isServerMode) return null;
    let user = null;
    try {
      user = await StoragePlugin.getActivePlugin().getCurrentUser?.();
    } catch { /* Lookup-Fehler — Section unsichtbar lassen */ }
    if (!user || !user.is_admin) return null;
    const section = buildSection('sidebar.admin');
    section.appendChild(makeRow(I18n.t('admin.menu'),
      () => EventBus.emit('admin:open'), { icon: '🔧' }));
    return section;
  };
  /* #307/ADR-0102: Clients + Projekte — server-only, toggle-aktivierbar.
     Sichtbarkeit hängt von Server-Config (clients-projects.{projects,clients}_enabled).
     Async wegen Config-Lookup. Null = keine Toggle aktiv → keine Section. */
  buildClientsProjectsSection = async function buildClientsProjectsSection() {
    const isServerMode = Profile.has('feature.restStorage');
    if (!isServerMode) return null;
    let cfg = null;
    try { cfg = await AdminClient.listConfig('clients-projects'); }
    catch { return null; }
    if (!cfg || !cfg.ok) return null;
    const map = {};
    (Array.isArray(cfg.body?.entries) ? cfg.body.entries : []).forEach(e => { map[e.key] = e; });
    const isBool = (v) => v === 'true' || v === '1';
    const projEnabled = isBool(map['clients-projects.projects_enabled']?.value);
    const clientEnabled = isBool(map['clients-projects.clients_enabled']?.value);
    if (!projEnabled && !clientEnabled) return null;
    const section = buildSection('sidebar.clients-projects');
    if (clientEnabled) {
      section.appendChild(makeRow(I18n.t('sidebar.clients'),
        () => EventBus.emit('clients-view:open'), { icon: '🏢' }));
    }
    if (projEnabled) {
      section.appendChild(makeRow(I18n.t('sidebar.projects'),
        () => EventBus.emit('projects-view:open'), { icon: '📁' }));
    }
    return section;
  };
}

/* #308/ADR-0103: Kalender-Ansicht — NICHT server-gegated. Basiert auf den
   Karten-Datumsfeldern (ADR-0034), die in allen Modi existieren. Nur die
   Cross-Board-/iCal-Teile (Etappen 5/6) sind server-only. */
function buildCalendarSection() {
  const section = buildSection('sidebar.views');
  section.appendChild(makeRow(I18n.t('sidebar.calendar'),
    () => EventBus.emit('calendar-view:open'), { icon: '📅' }));
  return section;
}

function buildSettingsSection() {
  /* ADR-0065 §3: Zwei Menüpunkte — Menü-Labels via config.menu.* (F-281).
     config.title.* bleibt für die Dialog-Titel (ADR-0065 §2), hier nur Menü-Zeilen. */
  const section = buildSection('sidebar.settings');
  section.appendChild(makeRow(I18n.t('config.menu.user'),
    () => EventBus.emit('config:open-user'), { icon: '👤' }));
  section.appendChild(makeRow(I18n.t('config.menu.board'),
    () => EventBus.emit('config:open-board'), { icon: '⚙' }));
  return section;
}

/* Karte #702: Die Freigabe („Board teilen“) liegt nicht mehr in der Sidebar,
   sondern als Sektion „Freigabe“ im Dialog Board-Einstellungen (share-section.js). */

/* ── Dokument-Viewer (Karte #274, IS_PROJECTOR_BUILD) ────────────────────
   Zeigt Markdown-Dateien aus dem FSA-Projektverzeichnis an.
   Nur im aboard.html-Build verfügbar (IS_PROJECTOR_BUILD=true).
   IS_PROJECTOR_BUILD=false (slim/template) → esbuild eliminiert den Block
   per DCE → keine Größenauswirkung auf aboard.template.html (ADR-0072).

   Content-Quelle: FSA-Verzeichnis-Handle (Sync.getDirectoryHandle).
   Begründung: Das Projektverzeichnis ist bereits geöffnet (Polling-Modus,
   ADR-0021). Die README.md liegt dort und kann ohne Netzwerk oder Build-
   Injektion direkt gelesen werden. Graceful Fallback wenn kein Handle
   vorhanden oder die Datei fehlt (Standalone-Modus ohne Projekt-Verzeichnis).

   Struktur für Erweiterbarkeit (Folgekarte knowledge/-Reader): Zwei Ebenen —
   `readDocFile(filename)` ist quell-generisch; `openDocViewer(opts)` ist der
   wiederverwendbare Viewer-Aufrufer. Eine Folgekarte hängt nur einen weiteren
   makeRow-Eintrag in buildDocEntriesSection() mit anderem filename an. */

let _docOverlay = null; /* Singleton — wird bei Bedarf lazily erzeugt */
let _docStylesInjected = false; /* Einmal-Flag für JS-injizierten CSS-Block */

/* CSS für Doc-Viewer + Knowledge-Liste lazily injizieren.
   Liegt in JS statt style.css, damit der slim-Build (IS_PROJECTOR_BUILD=false)
   das komplette CSS via DCE los wird (ADR-0088-Folgekarte #286 / F-320).
   Aufruf ist idempotent: Style-Tag wird nur beim ersten Mal angelegt. */
function _ensureDocViewerStyles() {
  if (_docStylesInjected) return;
  _docStylesInjected = true;
  const style = document.createElement('style');
  style.setAttribute('data-aboard', 'doc-viewer');
  style.textContent = '.doc-viewer-overlay{display:none;position:fixed;inset:0;z-index:400;background:rgba(0,0,0,.45);align-items:center;justify-content:center;padding:24px}.doc-viewer-overlay--open{display:flex}.doc-viewer-panel{background:var(--surface);border-radius:var(--radius);box-shadow:0 8px 32px rgba(0,0,0,.22);display:flex;flex-direction:column;width:min(720px,100%);max-height:calc(100vh - 48px);opacity:0;transform:translateY(12px);transition:opacity 180ms ease-out,transform 180ms ease-out}.doc-viewer-panel--open{opacity:1;transform:translateY(0)}.doc-viewer-header{display:flex;align-items:center;justify-content:space-between;padding:12px 16px 10px;border-bottom:1px solid var(--border);flex-shrink:0}.doc-viewer-title{font-weight:600;font-size:15px}.doc-viewer-body{overflow-y:auto;padding:16px 20px;flex:1}.doc-viewer-loading,.doc-viewer-empty{color:var(--text-muted);font-size:13px;font-style:italic;padding:8px 0}.doc-knowledge-list{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:2px}.doc-knowledge-entry{display:block;width:100%;text-align:left;background:none;border:none;border-radius:var(--radius);padding:7px 10px;font-size:13px;color:var(--text);cursor:pointer;transition:background 120ms}.doc-knowledge-entry:hover,.doc-knowledge-entry:focus-visible{background:var(--btn-bg-hover);outline:none}';
  document.head.appendChild(style);
}

/* Liest eine Textdatei aus dem aktiven FSA-Projektverzeichnis.
   Gibt null zurück wenn kein Handle, keine Datei oder kein Lesezugriff. */
async function readDocFile(filename) {
  const dirHandle = Sync.getDirectoryHandle?.();
  if (!dirHandle) return null;
  try {
    const fh   = await dirHandle.getFileHandle(filename);
    const file = await fh.getFile();
    return await file.text();
  } catch { return null; }
}

/* Liest eine Datei anhand eines relativen Pfads mit Unterordnern.
   path: z.B. 'knowledge/spurensicherung.md' oder 'knowledge/sub/datei.md'
   Segmente werden per '/' gesplittet; alle außer dem letzten sind Verzeichnisse.
   Gibt null zurück bei fehlendem Handle, nicht vorhandener Datei oder Fehler. */
async function readDocFileAtPath(path) {
  const dirHandle = Sync.getDirectoryHandle?.();
  if (!dirHandle) return null;
  try {
    const parts = path.split('/');
    let currentDir = dirHandle;
    for (let i = 0; i < parts.length - 1; i++) {
      currentDir = await currentDir.getDirectoryHandle(parts[i]);
    }
    const fh   = await currentDir.getFileHandle(parts[parts.length - 1]);
    const file = await fh.getFile();
    return await file.text();
  } catch { return null; }
}

/* F-319: Härtungsgrenzen für den rekursiven knowledge/-Walk. Kein
   Sicherheits-, sondern reines Robustheitsanliegen — das FSA-Threat-Model
   ist das vom User selbst gewählte Projektverzeichnis (ADR-0021), kein
   adversarieller Input. Aktuelle Browser exponieren über `entries()` keine
   zyklischen/Symlink-Handles, der realistische Worst Case ist ein echt
   tiefer/riesiger Baum → Tiefen- + Dateimengen-Deckel genügen. */
const KNOWLEDGE_MAX_DEPTH = 8;
const KNOWLEDGE_MAX_FILES = 500;

/* Rekursiver Verzeichnis-Walk: sammelt alle .md-Dateien unter einem Handle.
   prefix: relativer Pfad-Präfix (z.B. 'knowledge') für die Rückgabe.
   depth/budget: F-319-Härtung — Rekursionstiefe begrenzt, geteiltes
   Datei-Budget über alle Rekursionszweige (Default-Argumente halten die
   Signatur rückwärtskompatibel).
   Gibt ein Array von relativen Pfaden zurück (z.B. ['knowledge/foo.md']).
   Sortiert: Dateien im gleichen Ordner alphabetisch, Unterordner nach Dateien. */
async function _collectMdFiles(handle, prefix, depth = 0, budget = { count: 0 }) {
  if (depth > KNOWLEDGE_MAX_DEPTH) return [];
  const files   = [];
  const subdirs = [];
  for await (const [name, entry] of handle.entries()) {
    if (budget.count >= KNOWLEDGE_MAX_FILES) break;
    if (entry.kind === 'file' && name.endsWith('.md')) {
      files.push(prefix ? `${prefix}/${name}` : name);
      budget.count++;
    } else if (entry.kind === 'directory') {
      subdirs.push([name, entry]);
    }
  }
  files.sort();
  subdirs.sort((a, b) => a[0].localeCompare(b[0]));
  const result = [...files];
  for (const [name, entry] of subdirs) {
    if (budget.count >= KNOWLEDGE_MAX_FILES) break;
    const subPrefix = prefix ? `${prefix}/${name}` : name;
    const subFiles = await _collectMdFiles(entry, subPrefix, depth + 1, budget);
    result.push(...subFiles);
  }
  return result;
}

/* Listet alle .md-Dateien unter knowledge/ im FSA-Projektverzeichnis.
   Gibt ein leeres Array zurück wenn kein Handle oder kein knowledge/-Ordner. */
async function listKnowledgeFiles() {
  const dirHandle = Sync.getDirectoryHandle?.();
  if (!dirHandle) return [];
  try {
    const knowledgeDir = await dirHandle.getDirectoryHandle('knowledge');
    return await _collectMdFiles(knowledgeDir, 'knowledge');
  } catch { return []; }
}

/* Öffnet einen leichtgewichtigen Overlay-Viewer für Markdown-Inhalt.
   opts.titleKey: i18n-Key für den Viewer-Titel
   opts.filename: Dateiname im FSA-Verzeichnis (z.B. 'README.md') — ODER
   opts.path: relativer Pfad mit Unterordnern (z.B. 'knowledge/sub/datei.md')
   Wiederverwendbar für Folgekarten (knowledge/-Reader etc.) — nur opts anpassen. */
async function openDocViewer(opts) {
  /* Lazy-init des Overlays — einmaliger DOM-Mount */
  _ensureDocViewerStyles();
  if (!_docOverlay || !_docOverlay.isConnected) {
    _docOverlay = document.createElement('div');
    _docOverlay.className = 'doc-viewer-overlay';
    _docOverlay.setAttribute('role', 'dialog');
    _docOverlay.setAttribute('aria-modal', 'true');
    _docOverlay.setAttribute('aria-hidden', 'true');
    _docOverlay.tabIndex = -1;
    document.body.appendChild(_docOverlay);
  }

  /* Inhalt aufbauen */
  _docOverlay.textContent = '';

  const panel = document.createElement('div');
  panel.className = 'doc-viewer-panel';

  /* Header mit Titel und Schließen-Button */
  const header = document.createElement('div');
  header.className = 'doc-viewer-header';
  const titleEl = document.createElement('div');
  titleEl.className = 'doc-viewer-title';
  titleEl.textContent = opts.titleKey ? I18n.t(opts.titleKey) : (opts.titleText || '');
  const closeBtn = document.createElement('button');
  closeBtn.className = 'btn-modal-close';
  closeBtn.type = 'button';
  closeBtn.textContent = '×';
  closeBtn.setAttribute('aria-label', I18n.t('btn.close-modal'));
  closeBtn.addEventListener('click', closeDocViewer);
  header.appendChild(titleEl);
  header.appendChild(closeBtn);
  panel.appendChild(header);

  /* Inhalt-Bereich — Lade-Spinner zunächst */
  const body = document.createElement('div');
  body.className = 'doc-viewer-body';
  const loadingEl = document.createElement('div');
  loadingEl.className = 'doc-viewer-loading';
  loadingEl.textContent = I18n.t('doc.loading');
  body.appendChild(loadingEl);
  panel.appendChild(body);
  _docOverlay.appendChild(panel);

  /* Backdrop-Klick schließt */
  _docOverlay.addEventListener('click', (e) => {
    if (e.target === _docOverlay) closeDocViewer();
  });

  /* Esc-Taste schließt */
  _docOverlay.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { e.preventDefault(); closeDocViewer(); }
  });

  /* Anzeigen */
  _docOverlay.setAttribute('aria-hidden', 'false');
  _docOverlay.classList.add('doc-viewer-overlay--open');
  requestAnimationFrame(() => {
    panel.classList.add('doc-viewer-panel--open');
    _docOverlay.focus({ preventScroll: true });
  });

  /* Sidebar schließen, Viewer bleibt offen */
  close();

  /* Datei laden + rendern — opts.path hat Vorrang vor opts.filename */
  const text = opts.path
    ? await readDocFileAtPath(opts.path)
    : await readDocFile(opts.filename);
  body.textContent = '';
  if (!text) {
    const fallback = document.createElement('div');
    fallback.className = 'doc-viewer-empty';
    fallback.textContent = I18n.t('doc.not-found', opts.path || opts.filename);
    body.appendChild(fallback);
    return;
  }
  /* Bestehenden Markdown-Renderer verwenden (ADR-0009/0010 — kein neues Surface) */
  body.appendChild(Markdown.render(ohneHtmlKommentare(text)));
}

/* F-3109 (Karte #1186): HTML-Kommentare blendet ein Markdown-Leser aus — hier vor allem die
   Abzugs-Marken (`<!-- abzug:… -->`, ADR-0152) samt Inhalt der `abzug:nur`-Blöcke. Der Renderer
   zeigt rohes HTML als Text, deshalb vorher entfernen; Code-Zäune bleiben unberührt. */
export function ohneHtmlKommentare(text) {
  return text.split(/(^ {0,3}(?:```|~~~)[\s\S]*?^ {0,3}(?:```|~~~)[^\n]*$)/m)
    .map((teil, i) => (i % 2 ? teil : teil.replace(/^[ \t]*<!--[\s\S]*?-->[ \t]*\n?|<!--[\s\S]*?-->/gm, '')))
    .join('');
}

function closeDocViewer() {
  if (!_docOverlay) return;
  _docOverlay.setAttribute('aria-hidden', 'true');
  _docOverlay.classList.remove('doc-viewer-overlay--open');
  const panel = _docOverlay.querySelector('.doc-viewer-panel');
  if (panel) panel.classList.remove('doc-viewer-panel--open');
}

/* ── Knowledge-Reader (Karte #275, IS_PROJECTOR_BUILD) ────────────────────
   Zeigt alle .md-Dateien aus knowledge/ (inkl. Unterordner) des FSA-Projekt-
   verzeichnisses als klickbare Dateiliste im Overlay an.
   Kein zweites Viewer-Surface — wiederverwendet _docOverlay + openDocViewer. */

/* Leitet den Dateipfad in einen lesbaren Anzeige-Titel um.
   Entfernt 'knowledge/'-Präfix und '.md'-Suffix, ersetzt '/' durch ' / '. */
function _knowledgePathToLabel(path) {
  return path
    .replace(/^knowledge\//, '')
    .replace(/\.md$/, '')
    .replace(/\//g, ' / ');
}

/* Öffnet den Knowledge-Index: lädt Dateiliste, zeigt sie im Overlay an.
   Klick auf einen Eintrag → openDocViewer mit opts.path.
   Kein Handle / leeres Verzeichnis → Leer-Meldung. */
async function openKnowledgeIndex() {
  /* Lazy-init des Overlays — identisch zu openDocViewer */
  _ensureDocViewerStyles();
  if (!_docOverlay || !_docOverlay.isConnected) {
    _docOverlay = document.createElement('div');
    _docOverlay.className = 'doc-viewer-overlay';
    _docOverlay.setAttribute('role', 'dialog');
    _docOverlay.setAttribute('aria-modal', 'true');
    _docOverlay.setAttribute('aria-hidden', 'true');
    _docOverlay.tabIndex = -1;
    document.body.appendChild(_docOverlay);
  }
  _docOverlay.textContent = '';

  const panel = document.createElement('div');
  panel.className = 'doc-viewer-panel';

  /* Header */
  const header = document.createElement('div');
  header.className = 'doc-viewer-header';
  const titleEl = document.createElement('div');
  titleEl.className = 'doc-viewer-title';
  titleEl.textContent = I18n.t('sidebar.knowledge');
  const closeBtn = document.createElement('button');
  closeBtn.className = 'btn-modal-close';
  closeBtn.type = 'button';
  closeBtn.textContent = '×';
  closeBtn.setAttribute('aria-label', I18n.t('btn.close-modal'));
  closeBtn.addEventListener('click', closeDocViewer);
  header.appendChild(titleEl);
  header.appendChild(closeBtn);
  panel.appendChild(header);

  /* Body — zunächst Lade-Spinner */
  const body = document.createElement('div');
  body.className = 'doc-viewer-body';
  const loadingEl = document.createElement('div');
  loadingEl.className = 'doc-viewer-loading';
  loadingEl.textContent = I18n.t('doc.loading');
  body.appendChild(loadingEl);
  panel.appendChild(body);
  _docOverlay.appendChild(panel);

  /* Backdrop- und Esc-Handler */
  _docOverlay.addEventListener('click', (e) => {
    if (e.target === _docOverlay) closeDocViewer();
  });
  _docOverlay.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') { e.preventDefault(); closeDocViewer(); }
  });

  /* Anzeigen */
  _docOverlay.setAttribute('aria-hidden', 'false');
  _docOverlay.classList.add('doc-viewer-overlay--open');
  requestAnimationFrame(() => {
    panel.classList.add('doc-viewer-panel--open');
    _docOverlay.focus({ preventScroll: true });
  });

  /* Sidebar schließen */
  close();

  /* Dateiliste laden */
  const files = await listKnowledgeFiles();
  body.textContent = '';

  if (!files.length) {
    const empty = document.createElement('div');
    empty.className = 'doc-viewer-empty';
    empty.textContent = I18n.t('doc.knowledge-empty');
    body.appendChild(empty);
    return;
  }

  /* Dateiliste als klickbare Einträge darstellen */
  const list = document.createElement('ul');
  list.className = 'doc-knowledge-list';
  for (const path of files) {
    const item = document.createElement('li');
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'doc-knowledge-entry';
    btn.textContent = _knowledgePathToLabel(path);
    btn.addEventListener('click', () => {
      const title = _knowledgePathToLabel(path);
      openDocViewer({ titleKey: null, titleText: title, path });
    });
    item.appendChild(btn);
    list.appendChild(item);
  }
  body.appendChild(list);
}

/* Erzeugt den Sidebar-Abschnitt mit Dokumentations-Einträgen.
   Karte #274: README.md. Karte #275: Knowledge-Index. */
function buildDocEntriesSection() {
  const section = buildSection('sidebar.docs');
  /* README-Eintrag */
  section.appendChild(makeRow(
    I18n.t('doc.readme'),
    () => openDocViewer({ titleKey: 'doc.readme', filename: 'README.md' }),
    { icon: '📖', keepOpen: true },
  ));
  /* Knowledge-Index-Eintrag (Karte #275) */
  section.appendChild(makeRow(
    I18n.t('doc.knowledge'),
    () => openKnowledgeIndex(),
    { icon: '🧠', keepOpen: true },
  ));
  return section;
}

/* Exportiert für Tests */
export const __testingDocs__ = IS_PROJECTOR_BUILD
  ? {
      readDocFile,
      readDocFileAtPath,
      listKnowledgeFiles,
      _collectMdFiles,
      _knowledgePathToLabel,
      openDocViewer,
      openKnowledgeIndex,
      closeDocViewer,
      buildDocEntriesSection,
      KNOWLEDGE_MAX_DEPTH,
      KNOWLEDGE_MAX_FILES,
    }
  : null;

/* ── Build-Version im Sidebar-Fuss (Karte #347) ───────────────────────
   Anlass: F-543 — in einem Konsumenten-Projekt lief drei Monate lang ein veralteter
   Stand, ohne dass es jemandem auffiel, weil man einem laufenden Board seinen
   Build nicht ansieht. Dieselbe Frage stellte sich nach jedem Server-Update
   („zieht der Container das neue Image?").

   Bewusst am FLOW-Ende der Sidebar statt als floating Overlay: ein
   positioniertes Element würde mit dem Chat-Tail-Footer kollidieren
   (#283-Lehre). `margin-top: auto` schiebt die Zeile bei kurzem Inhalt nach
   unten, bei langem läuft sie einfach mit.

   In JEDEM Build sichtbar — die Frage „welcher Stand?" stellt sich im slim
   Template genauso wie im Server. Deshalb hier kein IS_*-Gate und das CSS in
   style.css statt per JS-Injection. */
function buildVersionFooter() {
  const zeile = el('div', { cls: 'app-sidebar-version' });
  /* __APP_VERSION__ wird in allen Build- und Test-Konfigurationen definiert
     (vite.config.base.js appVersion()). Der typeof-Test ist trotzdem kein
     toter Code: er hält die Sidebar am Leben, falls ein künftiger Build-Pfad
     das Define vergisst — dann fehlt die Zeile, statt dass die Sidebar mit
     einem ReferenceError gar nicht mehr rendert. */
  const version = (typeof __APP_VERSION__ !== 'undefined' && __APP_VERSION__) || '';
  if (!version) return null;
  zeile.textContent = version;
  zeile.title = `${I18n.t('sidebar.version')}: ${version}`;
  return zeile;
}

async function renderContent(board) {
  /* Bestehenden Inhalt leeren (außer Header). */
  while (_sidebar.lastChild) _sidebar.removeChild(_sidebar.lastChild);
  _sidebar.appendChild(buildHeader());
  /* Board-Umschalter: seit Karte #704 am Titel im Kopf (board-menu.js). */
  const isServerMode = Profile.has('feature.restStorage');
  /* ADR-0063: ui.open-file → dangerous → im Server-Modus verstecken.
     Standalone-/Projektmodus bleibt unverändert (ADR-0059-Invariante). */
  if (!SettingsScope.isServerHidden('ui.open-file', isServerMode)) {
    _sidebar.appendChild(buildFileSection());
  }
  /* Karte #274: Dokumentations-Abschnitt (README + ggf. Folgekarten).
     Nur im Projektor-Build (aboard.html) sichtbar — nur dort ist ein
     FSA-Projektverzeichnis mit README.md verfügbar.
     IS_PROJECTOR_BUILD=false → esbuild eliminiert diesen Aufruf per DCE. */
  if (IS_PROJECTOR_BUILD && Sync.getDirectoryHandle?.()) {
    _sidebar.appendChild(buildDocEntriesSection());
  }
  /* #306/ADR-0101: Server-Administration-Section (server-Modus + is_admin).
     (Karte #706: „Konto“ liegt in den Benutzer-Einstellungen.)
     IS_SERVER_BUILD=false → DCE eliminiert den Block. */
  if (IS_SERVER_BUILD) {
    /* Karte #739/ADR-0137: Wissen (an + sichtbar laut /me, sonst null). */
    const wissenSection = await buildWissenSection();
    if (wissenSection) _sidebar.appendChild(wissenSection);
    /* #307/ADR-0102: Clients-+Projekte-Section (toggle-gated, sonst null). */
    const cpSection = await buildClientsProjectsSection();
    if (cpSection) _sidebar.appendChild(cpSection);
    const adminSection = await buildAdminSection();
    if (adminSection) _sidebar.appendChild(adminSection);
  }
  /* #308/ADR-0103: Kalender-Ansicht — in allen Builds sichtbar (nicht gegated). */
  _sidebar.appendChild(buildCalendarSection());
  _sidebar.appendChild(buildSettingsSection());
  if (IS_SERVER_BUILD) {
    const abmelden = await buildAbmeldenSection();
    if (abmelden) _sidebar.appendChild(abmelden);
  }
  /* Karte #347: immer als LETZTES — die Zeile ist Fussnote, kein Menüpunkt. */
  const version = buildVersionFooter();
  if (version) _sidebar.appendChild(version);
}

function onKey(e) {
  if (!_isOpen) return;
  if (e.key === 'Escape') { e.preventDefault(); close(); }
}

async function open(board = null) {
  init(); /* idempotent — sorgt dafür, dass _sidebar im DOM hängt */
  if (_isOpen) return;
  const b = board ?? State.get()?.board;
  await renderContent(b);
  _isOpen = true;
  _sidebar.setAttribute('aria-hidden', 'false');
  _backdrop.classList.add('app-sidebar-backdrop--open');
  /* Spring-Animation: Klasse erst im nächsten Frame, damit Transition läuft. */
  requestAnimationFrame(() => {
    _sidebar.classList.add('app-sidebar--open');
    _sidebar.focus({ preventScroll: true });
  });
  document.addEventListener('keydown', onKey);
}

function close() {
  if (!_isOpen) return;
  _isOpen = false;
  _sidebar.classList.remove('app-sidebar--open');
  _backdrop.classList.remove('app-sidebar-backdrop--open');
  _sidebar.setAttribute('aria-hidden', 'true');
  document.removeEventListener('keydown', onKey);
}

function init() {
  /* DOM-Mount ist idempotent solange die Nodes noch im document hängen.
     In Tests wird body.innerHTML zwischen den Cases geleert — dann müssen
     wir neu mounten, ohne die EventBus-Subscriber zu verdoppeln. */
  if (!_sidebar || !_sidebar.isConnected) {
    buildShell();
    _isOpen = false;
  }
  if (_initialized) return;
  _initialized = true;
  EventBus.on('sidebar:open',  () => open());
  EventBus.on('sidebar:close', () => close());
  /* Bei Board-Wechsel automatisch schließen (Inhalt würde stale werden). */
  EventBus.on('sync:switch-to-board',       () => close());
  EventBus.on('standalone:switch-to-file',  () => close());
  /* #308/ADR-0103: Kalender-View öffnen — in allen Builds (nicht gegated). */
  EventBus.on('calendar-view:open', () => CalendarView?.open?.());
  /* #306/ADR-0101: Admin-Dialog öffnen (server-only, DCE'd im Slim). */
  if (IS_SERVER_BUILD) {
    EventBus.on('admin:open',         () => AdminDialog?.open?.());
    /* #307/ADR-0102: Clients-/Projekte-View öffnen (server-only, DCE'd im Slim). */
    EventBus.on('clients-view:open',  () => ClientsView?.open?.());
    EventBus.on('projects-view:open', () => ProjectsView?.open?.());
    EventBus.on('boards-view:open',   () => BoardsView?.open?.());
    /* Karte #739/ADR-0137: Lesesicht der Wissensquelle. */
    EventBus.on('wissen-view:open',   (name) => WissenView?.open?.(name));
    /* Karte #1083: Konto-Einstellungen (nur mit eingebauter Anmeldung). */
    EventBus.on('konto:open',         () => KontoEinstellungen?.open?.());
  }
}

export const Sidebar = { init, open, close };
