import { idbPut, idbGet, idbDelete, idbListWithPrefix } from './idb.js';
import { Model } from './model.js';
import { defaultUserIdentity } from './identity.js';
import { awaitingAlsAbbildung, awaitingOderNull } from './awaiting.js';
import { istUhrzeit } from './calendar/date-helpers.js';

/* F-37: pro Board eigener Slot. ADR-0087/F-318: deployment-scoped wie
   ADR-0048 (Config) — sonst teilen sich auf `file://` alle `aboard.html`
   einer Maschine (Opaque-Origin) denselben Autosave-Puffer und ein frischer
   Projektor-Konsument adoptiert ein fremdes Board. Legacy-Schlüssel
   (`aboard-autosave-<id>`, ungescopt) werden in `load()` aktiv weggeräumt. */
const LS_BASE        = 'aboard-autosave';
const LS_LEGACY_PREFIX = LS_BASE + '-';
function lsPrefix() {
  const path = (typeof globalThis !== 'undefined'
    && globalThis.location && globalThis.location.pathname) || '';
  return path ? `${LS_BASE}@${path}-` : `${LS_LEGACY_PREFIX}`;
}

/* ── IndexedDB für FSA File-Handle ───────────────────────────────── */
/* ADR-0028: Multi-Handle pro board.id — analog zu sync.js
   F-103: gemeinsame IDB-Helper in idb.js */

const LEGACY_KEY = 'saveFile';        /* Legacy Single-Slot vor ADR-0028 */
const KEY_PREFIX = 'saveFile-';

function keyFor(boardId) {
  return boardId ? KEY_PREFIX + boardId : LEGACY_KEY;
}

/* Ein echtes FileSystemHandle erkennt man an `queryPermission` als Methode —
   verlässlicher als ein Vergleich auf `kind === 'file'`, weil der Type-Guard
   auch bei einem Wrapper greift, der zufällig ein `kind`-Feld trägt. */
function isHandleLike(value) {
  return !!value && typeof value === 'object' && typeof value.queryPermission === 'function';
}

/* Wie viele Wrap-Schichten `unwrap` höchstens abschält (Sprint-51-Kritiker K6).
   Ohne Grenze könnte ein zyklischer Eintrag (`obj.handle === obj`) den Tab
   einfrieren — der Structured-Clone-Algorithmus von IndexedDB unterstützt
   Zyklen ausdrücklich, ein solcher Wert ist also speicher- und lesbar. Kein
   heutiger Schreibpfad erzeugt einen; Härtung gegen korrupte Wrapper ist aber
   genau der Zweck von F-139. Acht ist grosszügig: real tritt Tiefe 1 auf. */
const MAX_UNWRAP_TIEFE = 8;

/* F-104: ab Sprint 17 wird ein Wrapper `{ handle, title }` persistiert,
   damit das Switch-Popover Board-Titel statt Dateinamen zeigen kann.
   Legacy-Einträge (raw FileSystemFileHandle, `kind==='file'`) werden weiter
   akzeptiert; sie behalten title=null bis zum nächsten save().

   F-139: unwrap war nicht idempotent — ein Doppel-Wrap
   `{ handle: { handle: rawHandle, title }, title }` (z. B. wenn ein bereits
   gewrapptes Objekt versehentlich erneut an storeFileHandle() gereicht
   wurde) lieferte als `.handle` selbst wieder einen Wrapper statt eines
   echten FileSystemHandle. Jeder nachfolgende `queryPermission()`-Aufruf
   (openByBoardId, save) warf dann, weil der Wrapper diese Methode nicht
   hat. Die Schleife schält Wrap-Schichten ab, bis entweder ein echtes
   Handle (Type-Guard) oder kein weiterer `.handle`-Layer mehr da ist —
   verschachtelt bis MAX_UNWRAP_TIEFE, nicht nur einfach oder doppelt. Der
   äußerste gesetzte Titel gewinnt, weil er beim letzten save() zuletzt
   geschrieben wurde.

   RICHTIGSTELLUNG (Sprint-51-Kritiker D1): Commit `b4e91ea`, der Karten-
   Kommentar zu #375 und der F-139-Eintrag begründeten den `'directory'`-Zweig
   unten damit, `src/sync.js` reiche Verzeichnis-Handles durch dieselbe
   Funktion. Das ist falsch. `unwrap` ist modul-intern und wird ausschliesslich
   aus `loadFileHandle` und `listKnownFiles` erreicht — beide unter
   `KEY_PREFIX = 'saveFile-'`. `sync.js` legt seine Verzeichnis-Handles unter
   `projectDir@…` ab und liest sie roh per `idbGet`; gemeinsam ist nur `idb.js`,
   und das wrappt nichts. Der Zweig bleibt trotzdem stehen: er kostet nichts und
   deckt einen Alt-/Fremd-Eintrag ohne `queryPermission` ab. Er ist aber
   Vorsorge, kein Fix für einen realen Pfad — und damit heute unerreichbar. */
function unwrap(value) {
  let title = null;
  let sawTitle = false;
  /* V2: Titel auch dann übernehmen, wenn gar kein `handle`-Layer folgt — sonst
     verlöre `{ title: 'X' }` seinen Titel, den die alte Fassung noch lieferte. */
  for (let tiefe = 0; tiefe < MAX_UNWRAP_TIEFE; tiefe++) {
    if (!value || typeof value !== 'object' || isHandleLike(value)) break;
    if (!sawTitle && value.title != null) { title = value.title; sawTitle = true; }
    if (!('handle' in value)) break;
    value = value.handle;
  }
  if (isHandleLike(value) || (value && typeof value === 'object' && (value.kind === 'file' || value.kind === 'directory'))) {
    return { handle: value, title };
  }
  return { handle: null, title };
}

async function storeFileHandle(h, boardId, title = null) {
  await idbPut(keyFor(boardId), { handle: h, title });
}

async function loadFileHandle(boardId) {
  const value = await idbGet(keyFor(boardId));
  return unwrap(value).handle;
}

async function deleteFileHandle(boardId) {
  await idbDelete(keyFor(boardId));
}

/* Liste aller bekannten Save-File-Handles (Standalone-Boards) — für Switch-UI
   F-104: liefert zusätzlich `title` (oder null bei Legacy-Einträgen). */
async function listKnownFiles() {
  const entries = await idbListWithPrefix(KEY_PREFIX);
  return entries.map(({ id, value }) => {
    const { handle, title } = unwrap(value);
    return { id, name: handle?.name ?? id.slice(0, 8), title };
  });
}

/* ── Modul-Zustand ───────────────────────────────────────────────── */

let fileHandle    = null;
let activeBoardId = null;

/* Handle beim Seitenstart vorladen für die aktuelle board.id.
   Legacy-Migration: alter `saveFile`-Key wird ohne board-id-Check übernommen
   (Save-Handle gehört zwingend zur aktuellen aboard.html, weil er beim
   letzten Speichern dieser Datei gesetzt wurde). */
async function preloadFileHandle(boardId) {
  if (fileHandle && activeBoardId === boardId) return;
  activeBoardId = boardId ?? null;
  let stored = await loadFileHandle(boardId);
  if (!stored && boardId) {
    const legacy = await loadFileHandle(null);
    if (legacy) {
      await storeFileHandle(legacy, boardId);
      await deleteFileHandle(null);
      stored = legacy;
    }
  }
  fileHandle = stored || null;
}

/* ── Schema-Migration v1 → v2 (→ ADR-0019) ──────────────────────── */

function migrateV1toV2(state) {
  const oldCols  = state.board?.columns || [];
  const SPRINT_RE = /^(sprint|wave|phase)\s+/i;

  const sprintCols   = oldCols.filter(c => SPRINT_RE.test(c.title));
  const workflowCols = oldCols.filter(c => !SPRINT_RE.test(c.title));

  if (sprintCols.length > 0 && workflowCols.length > 0) {
    const columnDefs = workflowCols.map(c => ({ id: c.id, title: c.title }));
    const defaultCol = workflowCols.find(c => /^backlog$/i.test(c.title)) ?? workflowCols[0];
    const sprints = sprintCols.map(col => ({
      id:    crypto.randomUUID(),
      title: col.title,
      cards: (col.cards || []).map(card => ({ ...card, columnId: defaultCol.id })),
    }));
    const backlogCards = [];
    workflowCols.forEach(col => {
      (col.cards || []).forEach(card => backlogCards.push({ ...card, columnId: col.id }));
    });
    sprints.push({ id: '__backlog__', title: 'Backlog', cards: backlogCards });
    return { version: '2', board: { id: state.board.id, title: state.board.title, columnDefs, sprints } };
  }

  const columnDefs   = oldCols.map(col => ({ id: col.id, title: col.title }));
  const backlogCards = [];
  oldCols.forEach(col => {
    (col.cards || []).forEach(card => backlogCards.push({ ...card, columnId: col.id }));
  });
  return {
    version: '2',
    board: { id: state.board.id, title: state.board.title, columnDefs,
             sprints: [{ id: '__backlog__', title: 'Backlog', cards: backlogCards }] },
  };
}

/* ADR-0028: displayId-Migration — läuft idempotent bei jedem Laden */
function migrateDisplayIds(state) {
  if (!state?.board) return null; /* F-117: ungültiger State → null (F-34-Konvention) */
  const board = state.board;
  const allCardsHaveIds = board.sprints.every(s => s.cards.every(c => c.displayId != null));
  if (board.nextDisplayId != null && allCardsHaveIds) return state;

  /* counter über vorhandenen IDs starten — verhindert Duplikate bei gemischten Beständen */
  const maxExisting = board.sprints
    .flatMap(s => s.cards.map(c => c.displayId ?? 0))
    .reduce((a, b) => Math.max(a, b), 0);
  let counter = maxExisting + 1;
  board.sprints.forEach(sprint => {
    sprint.cards.forEach(card => {
      if (card.displayId == null) card.displayId = counter++;
    });
  });
  board.nextDisplayId = counter;
  return state;
}

/* ADR-0032: links-Feld auf jeder Karte sicherstellen (additive Migration) */
function migrateLinks(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (!Array.isArray(card.links)) card.links = [];
    });
  });
  return state;
}

/* ADR-0033: attachments-Feld auf jeder Karte sicherstellen (additive Migration) */
function migrateAttachments(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (!Array.isArray(card.attachments)) card.attachments = [];
    });
  });
  return state;
}

/* ADR-0034: dueDate / startDate sanitisieren — beim Import können beliebige
   Strings als Datum landen (z. B. via JSON-Import). Ungültige ISO-8601-Werte
   werden auf null normalisiert, damit Renderer/Export keine NaN-Vergleiche
   stillschweigend „erfolgreich" verarbeiten. */
const ISO_DATE_RE = /^\d{4}-\d{2}-\d{2}$/;
function isValidIsoDate(s) {
  if (typeof s !== 'string' || !ISO_DATE_RE.test(s)) return false;
  const d = new Date(s);
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === s;
}
function migrateDates(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (card.dueDate !== undefined && !isValidIsoDate(card.dueDate)) card.dueDate = null;
      if (card.startDate !== undefined && !isValidIsoDate(card.startDate)) card.startDate = null;
      /* ADR-0138 §1: Uhrzeiten sind eigene Felder — dueDate/startDate werden NIE
         um eine Uhrzeit erweitert, sonst setzte jede ältere Fassung sie oben auf
         null. Eine Uhrzeit ohne zugehöriges Datum oder in falscher Form → null. */
      if (card.dueTime !== undefined && !(istUhrzeit(card.dueTime) && card.dueDate)) card.dueTime = null;
      if (card.startTime !== undefined && !(istUhrzeit(card.startTime) && card.startDate)) card.startTime = null;
    });
  });
  return state;
}

/* #295/ADR-0093: senderEmail + messageId kommen aus IMAP-Ingest. Defense-in-
   depth: gegen unerwartete Werte mit Regex sanitisieren — Server prüft selbst,
   aber der Client soll nichts Verdächtiges weiterreichen. Verstoß → leerer
   String (Feld bleibt erhalten, damit Spam-Modal nicht nach prompt() fragt). */
const _MAIL_SENDER_RE = /^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$/;
const _MAIL_MSGID_RE  = /^<[A-Za-z0-9._%+\-@!#$&*=?^`{|}~\/]+>$/;
function migrateMailFields(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (typeof card.senderEmail === 'string' && card.senderEmail !== '' && !_MAIL_SENDER_RE.test(card.senderEmail)) card.senderEmail = '';
      if (typeof card.messageId === 'string' && card.messageId !== '' && !_MAIL_MSGID_RE.test(card.messageId)) card.messageId = '';
    });
  });
  return state;
}

/* Karte #60: archived-Feld auf jeder Karte sicherstellen (additive Migration, nach migrateDates) */
function migrateArchived(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (card.archived === undefined) card.archived = false;
    });
  });
  return state;
}

/* ADR-0117 (Karte #346): `archived` auf jeder Swimlane sicherstellen.
   Additive Migration analog migrateArchived/migrateLaneKind — idempotent, setzt
   nur, was fehlt. `__backlog__` und `kind:'service'` bekommen den Default
   ebenfalls (das Feld existiert dort, ist aber per Guard nie auf true zu
   bringen — ein fehlendes Feld waere schwerer zu pruefen als ein festes false). */
function migrateSprintArchived(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    if (sprint.archived === undefined) sprint.archived = false;
  });
  return state;
}

/* ADR-0037: activity-Feld auf jeder Karte sicherstellen (additive Migration, nach migrateArchived) */
function migrateActivity(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (!Array.isArray(card.activity)) card.activity = [];
    });
  });
  return state;
}

/* #302: assignees-Feld auf jeder Karte sicherstellen (additive Migration, idempotent).
   Owner bleibt Single-String + Spawn-Quelle (ADR-0043); assignees ist String-Array
   zusätzlich Beteiligter. */
function migrateAssignees(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (!Array.isArray(card.assignees)) card.assignees = [];
    });
  });
  return state;
}

/* #307/ADR-0102: project_id-Feld auf jeder Karte sicherstellen (additive Migration).
   Karten-Projekt-Zuordnung lebt im Karten-JSON (Server bleibt opak gegenüber dem
   Karten-Inhalt). null = kein Projekt; sonst UUID-String aus projects.id. Ungültige
   Werte (kein String) werden auf null normalisiert. */
function migrateProjectId(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (card.project_id === undefined) { card.project_id = null; return; }
      if (card.project_id !== null && typeof card.project_id !== 'string') card.project_id = null;
    });
  });
  return state;
}

/* Karte #62: coverAttachmentId auf jeder Karte sicherstellen + dangling IDs auf null setzen.
   Idempotent — läuft bei jedem Laden und prüft auf Konsistenz.

   F-165 (Karte #376) — mit Richtigstellung der Finding-Prämisse: Das Finding
   sagte, ein `coverAttachmentId`, das auf einen Board-Anhang
   (`board.attachments`, Karte #114) zeigt, „überlebt die Migration". Das
   stimmte so nicht. Die alte Prüfung fragte `card.attachments.some(...)` —
   eine reine Board-Anhang-ID steht dort nicht, wurde also bereits genullt.
   Nur eben aus dem falschen Grund: nicht weil sie als Board-Anhang erkannt
   wurde, sondern weil sie zufällig nirgends im Karten-Array vorkam.

   Real überlebt hat genau ein Fall: dieselbe ID in BEIDEN Listen (kaputter
   oder manipulierter Import). Der ist jetzt zu — geprüft werden beide
   Quellen, ein Treffer in `board.attachments` nullt das Cover unabhängig
   davon, ob die Karte die ID auch führt. Ein Cover auf einem Board-Anhang
   ist im Datenmodell nicht vorgesehen; die Migration soll den Zustand nicht
   nachträglich legitimieren. */
function migrateCover(state) {
  if (!state?.board?.sprints) return state;
  /* Board-Anhang-IDs einmal einsammeln statt pro Karte neu (bei vielen
     Karten sonst O(Karten × Board-Anhänge)). */
  const boardAttIds = new Set(
    (Array.isArray(state.board.attachments) ? state.board.attachments : [])
      .map(a => a && a.id).filter(id => typeof id === 'string'),
  );
  state.board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (card.coverAttachmentId === undefined) {
        card.coverAttachmentId = null;
        return;
      }
      /* Dangling-ID: Attachment existiert nicht mehr → null setzen.
         Ein Treffer in board.attachments ist KEIN gültiges Cover (siehe
         oben), wird aber ausdrücklich mitgeprüft, damit er nicht als
         „existiert irgendwo" durchrutscht. */
      if (card.coverAttachmentId !== null) {
        const inCard = Array.isArray(card.attachments) &&
          card.attachments.some(a => a && a.id === card.coverAttachmentId);
        if (!inCard || boardAttIds.has(card.coverAttachmentId)) {
          card.coverAttachmentId = null;
        }
      }
    });
  });
  return state;
}

/* ADR-0041: recurrence-Feld auf jeder Karte sicherstellen — fehlend/ungültig → null, idempotent.
   Gültig = null | Objekt mit interval ('daily'|'weekly'|'monthly') und lastSpawned (ISO-Datum). */
const VALID_INTERVALS = new Set(['daily', 'weekly', 'monthly']);
function migrateRecurrence(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (card.recurrence === undefined || card.recurrence === null) {
        card.recurrence = null;
        return;
      }
      const r = card.recurrence;
      /* Validierung: muss Objekt mit gültigem interval + lastSpawned-String sein */
      const valid = r && typeof r === 'object' &&
        VALID_INTERVALS.has(r.interval) &&
        typeof r.lastSpawned === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(r.lastSpawned);
      if (!valid) card.recurrence = null;
    });
  });
  return state;
}

/* Karte #65 (ADR-0040): board.templates sicherstellen — fehlend → leeres Array, idempotent.
   F-168: bisher wurde nur das Array selbst geprüft, nicht die Einträge — ein Template
   ohne id oder ohne isTemplate:true landete unverändert im Add-Dropdown und brach dort
   (id-lose Updates/Deletes in main.js laufen ins Leere, ein Nicht-Objekt-Eintrag wirft
   beim `.forEach`/`tpl.title`-Zugriff in renderer.js/configdialog.js). Nicht-Objekt-
   Einträge sind nicht reparierbar → überspringen (nur der kaputte Eintrag fällt weg,
   nicht das ganze Array — Bestandsdaten-Schutz). Fehlende/ungültige id bzw. isTemplate
   sind reparierbar → reparieren statt löschen. Jeder Fall sichtbar per console.warn,
   nicht still geschluckt. */
function migrateTemplates(state) {
  if (!state?.board) return state;
  if (!Array.isArray(state.board.templates)) state.board.templates = [];
  state.board.templates = state.board.templates.filter(tpl => {
    if (!tpl || typeof tpl !== 'object') {
      console.warn('migrateTemplates: ungültiger (Nicht-Objekt-)Template-Eintrag übersprungen', tpl);
      return false;
    }
    return true;
  });
  state.board.templates.forEach(tpl => {
    if (typeof tpl.id !== 'string' || !tpl.id) {
      console.warn(`migrateTemplates: Template "${tpl.title || '(ohne Titel)'}" ohne gültige id — repariert`);
      tpl.id = Model.newId();
    }
    if (tpl.isTemplate !== true) {
      console.warn(`migrateTemplates: Template "${tpl.title || '(ohne Titel)'}" ohne isTemplate:true — repariert`);
      tpl.isTemplate = true;
    }
  });
  return state;
}

/* Karte #122: columnDefs[i].color sicherstellen — fehlend → null, idempotent.
   Ungültige Werte (kein String) werden ebenfalls auf null normalisiert.
   Der konkrete HEX-Wert wird nicht strikt validiert — Browser-native <input type="color">
   liefert immer gültiges HEX; manuelle Eingaben werden roh gespeichert (pragmatisch). */
function migrateColumnColor(state) {
  if (!state?.board?.columnDefs) return state;
  state.board.columnDefs.forEach(colDef => {
    if (colDef.color === undefined || (colDef.color !== null && typeof colDef.color !== 'string')) {
      colDef.color = null;
    }
  });
  return state;
}

/* F-249/ADR-0052: fehlende oder leere columnDefs → kanonischer Standard-
   Spaltensatz. Behebt das spaltenlose Board (vorher gab es NIRGENDS eine
   Default-Injektion: createBoard lieferte [], keine Migration füllte es).
   Idempotent: greift nur bei fehlendem/leerem Array, überschreibt nie
   bestehende Spalten (auch nicht die von migrateV1toV2 abgeleiteten). */
function migrateDefaultColumns(state) {
  if (!state?.board) return state;
  if (!Array.isArray(state.board.columnDefs) || state.board.columnDefs.length === 0) {
    state.board.columnDefs = Model.createDefaultColumnDefs();
  }
  return state;
}

/* Karte #115 / Karte #609 (ADR-0130 §7): board.awaitingUser sicherstellen.
   Sollform: `null` oder `{ <Person>: { question, since, askedBy } }`.
   - fehlend / ungültig → null
   - alte Einzelform `{ question, since, askedBy }` → Eintrag der Standardperson
     (erste Nutzer-Identität, sonst „User" — dieselbe Regel wie im Fold, ohne
     Config, damit der headless Projektor dasselbe Ergebnis liefert)
   - Abbildung → nur gültige Einträge bleiben; leer → null.
   Idempotent und würfelfrei (normalisierende Kette, ADR-0127). */
function migrateAwaitingUser(state) {
  if (!state?.board) return state;
  const b = state.board;
  if (b.awaitingUser === undefined || b.awaitingUser === null) {
    b.awaitingUser = null;
    return state;
  }
  b.awaitingUser = awaitingOderNull(awaitingAlsAbbildung(b.awaitingUser, b));
  return state;
}

/* Karte #123: board.identities sicherstellen — additive Migration, idempotent.
   Karte #171: Neue Boards bekommen NUR die User-Identity. Claude-Identitäten
   werden bei Bedarf on-the-fly aus CLAUDE_DEFAULTS in resolveIdentity gebaut
   (src/identity.js:96-102) — sie sind statisch, nicht editierbar.

   Bestandsboards mit Claude-Einträgen in board.identities bleiben unverändert
   (Daten-Schonung): die zweite Schleife normalisiert nur Felder, löscht
   keine Einträge. Der Settings-Dialog filtert sie aus der Anzeige.

   Hinweis: Wir importieren Config NICHT, um circulare Abhängigkeiten zu
   vermeiden. */
function migrateIdentities(state) {
  if (!state?.board) return state;
  const b = state.board;
  if (!Array.isArray(b.identities)) {
    b.identities = [
      defaultUserIdentity(''), /* Label „User" — bei erstem Settings-Öffnen anpassbar */
    ];
    return state;
  }
  /* Idempotent: bestehende identities unverändert lassen.
     Defensiv: Felder normalisieren, damit ältere Backups konsistent bleiben. */
  b.identities.forEach(idn => {
    if (typeof idn.id !== 'string') idn.id = `identity-${idn.kind || crypto.randomUUID()}`;
    if (typeof idn.kind !== 'string') idn.kind = 'user';
    if (typeof idn.label !== 'string') idn.label = 'User';
    if (typeof idn.color !== 'string') idn.color = '#7c3aed';
    if (idn.icon !== null && typeof idn.icon !== 'string') idn.icon = null;
    if (idn.imageAttachmentId !== null && typeof idn.imageAttachmentId !== 'string') {
      idn.imageAttachmentId = null;
    }
    /* ADR-0108 (Karte #339): optionales email-Feld. Additiv — Bestands-
       Identities bekommen einen leeren String und funktionieren unverändert
       weiter; sie sind damit „nicht zugeordnet" und werden beim Auflösen
       von Benachrichtigungs-Empfängern still übersprungen.
       Ein Nicht-String (aus einem manipulierten Import) wird auf '' normiert
       statt durchgereicht: Ein `null` oder eine Zahl landete sonst als
       Adresse in `recipient_emails`. */
    if (typeof idn.email !== 'string') idn.email = '';
  });
  return state;
}

/* Karte #114: board.background + board.attachments sicherstellen + dangling ID auf null setzen.
   Idempotent — läuft bei jedem Laden. */
function migrateBoardBackground(state) {
  if (!state?.board) return state;
  const b = state.board;
  if (!Array.isArray(b.attachments)) b.attachments = [];
  if (b.background === undefined) b.background = null;
  else if (b.background && !b.attachments.some(a => a.id === b.background.attachmentId)) b.background = null;
  /* #727: Darstellung normalisieren — nur 'voll'/'abgedunkelt'/'getoent' gelten, sonst getönt. */
  const d = b.background?.darstellung;
  if (d !== undefined && d !== 'voll' && d !== 'abgedunkelt' && d !== 'getoent') b.background.darstellung = 'getoent';
  return state;
}

/* ADR-0046 / ADR-0092: kind-Feld auf jeder Swimlane sicherstellen.
   - __backlog__ → 'backlog'
   - alle anderen ohne kind → 'sprint' (rückwärts-kompatibel)
   - ungültige Werte (kein String oder nicht in Whitelist) → wie fehlend behandeln
   Idempotent: bereits gültige Werte bleiben unverändert.
   ADR-0092: 'service' ist ein per Server-Bootstrap erzeugter Kind ohne
   Sprint-Mechanik (Verhalten analog 'lane'); die Frontend-UI kann ihn nicht
   selbst erzeugen, muss ihn aber rendern können. */
const VALID_SPRINT_KINDS = ['sprint', 'lane', 'backlog', 'service'];
function migrateLaneKind(state) {
  if (!state?.board?.sprints) return state;
  state.board.sprints.forEach(sprint => {
    const isBacklog = sprint.id === '__backlog__';
    const current = sprint.kind;
    const isValid = typeof current === 'string' && VALID_SPRINT_KINDS.includes(current);
    if (!isValid) {
      sprint.kind = isBacklog ? 'backlog' : 'sprint';
      return;
    }
    /* Defensive Konsistenz: __backlog__ darf nie ein anderes kind führen */
    if (isBacklog && current !== 'backlog') sprint.kind = 'backlog';
    /* Karte #332: Die frühere Gegenkorrekttur (!isBacklog && 'backlog' → 'sprint')
       wurde entfernt — sie griff fälschlich bei UUID-ID-Backlog-Swimlanes
       (gültiger ADR-0067-Zustand) und erzeugte dort Sprint-Meta-Karten.
       UUID-Backlog-Swimlanes tragen 'backlog' als kind, was gültig ist. */
  });
  return state;
}

/* Backlog-Swimlane ans Ende (ADR-0127: aus migrateLaneKind herausgelöst).
 *
 * Bis Sprint 65 steckte das in `migrateLaneKind` mit drin — dieselbe Funktion
 * füllte also ein fehlendes Feld UND sortierte das Board um. Das ist eine
 * Doppelverantwortung, die erst auffiel, als der Projektor die eine Hälfte
 * brauchte und die andere nicht haben durfte: Ein Feld zu füllen, das
 * CLAUDE.md ohnehin für jede Swimlane zusagt, ist unsichtbar; eine Swimlane zu
 * verschieben sieht der Nutzer.
 *
 * Der Grund für die Sortierung selbst bleibt: Der Backlog ist IMMER die
 * unterste Swimlane. Neue Sprints werden oberhalb gerendert — bleibt der
 * Backlog nicht unten, steht eine frisch angelegte Swimlane darunter und wird
 * leicht übersehen. Idempotent: ist der Backlog schon letzter, passiert nichts.
 */
function migrateBacklogPosition(state) {
  if (!state?.board?.sprints) return state;
  const sprints = state.board.sprints;
  const bIdx = sprints.findIndex(s => s.id === '__backlog__');
  if (bIdx !== -1 && bIdx !== sprints.length - 1) {
    const [backlog] = sprints.splice(bIdx, 1);
    sprints.push(backlog);
  }
  return state;
}

/* ADR-0036: Sprint-Meta-Sonderkarten für bestehende Sprints nachrüsten.
   Läuft idempotent — prüft per metaType ob Karten bereits existieren.
   ADR-0046: Nur für Swimlanes mit kind === 'sprint' (Lanes bekommen keine Meta-Karten).
   ADR-0047: Auto-Aktivierung der Start-Karte sobald de-facto-Arbeit beginnt.
   Karte #330: Cleanup-Pass — Meta-Karten in unerlaubten Swimlanes entfernen. */
/* F-364 (Karte #373): „Meta-Karte ohne Kommentare ist leer" war der Denkfehler.
   Die Übergabe-Notizen zwischen zwei Chats stehen per Konvention in der
   BESCHREIBUNG der Sprint-Start-Karte (docs/claude/board-felder.md §Sprint-Meta-Sonderkarten),
   nicht in den Kommentaren — eine Kind-Umstellung sprint→lane hat sie deshalb
   beim nächsten Board-Load still gelöscht. Das Aktivitäts-Log (ADR-0037) zählt
   ebenfalls als Inhalt: es belegt, dass an der Karte gearbeitet wurde.
   Bewusst konservativ: `activity` mitzuzählen heisst, dass eine aktivierte
   Start-Karte in einer Lane stehen bleibt (mit Warnung) statt aufgeräumt zu
   werden. Das ist die gewollte Richtung — lieber warnen als still löschen.
   Frisch erzeugte Meta-Karten haben `description: ''` (Model.createSprintMetaCard),
   der #330-Cleanup-Pass bleibt für sie also wirksam. */
export function istMetaKarteLeer(card) {
  const hatKommentare   = Array.isArray(card?.comments) && card.comments.length > 0;
  const hatBeschreibung = typeof card?.description === 'string' && card.description.trim() !== '';
  /* Sprint-51-Kritiker (K-D): NUR Einträge zählen, die nicht vom System stammen.
     Die erste Fassung zählte jeden `activity`-Eintrag als Inhalt — dadurch hoben
     sich zwei Fixes desselben Commits gegenseitig auf: das `col:delete`-Rehoming
     schreibt selbst einen `actor: 'system'`-Eintrag, und schon war die Karte
     gegen den #330-Cleanup immunisiert. Eine leere Meta-Karte in einer Lane wäre
     nach einem einzigen Spalten-Löschen dauerhaft unaufräumbar gewesen.
     Systemeinträge (`sprint-activate`, Rehoming) sind Buchhaltung, kein
     Nutzerinhalt — sie zu schützen war nie die Absicht von F-364. Damit ist auch
     die in F-364 dokumentierte „bewusste Verschärfung" (aktivierte Start-Karte
     bleibt stehen) zurückgenommen: sie kostete Aufräumbarkeit, ohne etwas zu
     schützen, das ein Mensch geschrieben hat. */
  const hatVerlauf = Array.isArray(card?.activity)
    && card.activity.some(e => e && e.actor !== 'system');
  return !hatKommentare && !hatBeschreibung && !hatVerlauf;
}

/* Sprint-51-Kritiker (K-C): Zielspalte für eine Meta-Karte, deren Spalte gerade
   verschwindet oder nicht (mehr) existiert.
   Meta-Karten kennen nach ADR-0036/0042 genau zwei sinnvolle Spalten:
   `col-backlog` (Sprint nicht aktiviert) und `col-erledigt` (aktiviert). Die
   erste Fassung nahm blind `columnDefs[0]` — löschte der Nutzer die
   Backlog-Spalte, landete die Start-Karte damit in „Ready". Zwei Folgen: die
   Karte stand in einer Spalte, die ADR-0036 für sie nicht kennt, UND
   `autoActivateSprintStart` verlangt `columnId === 'col-backlog'`, konnte also
   nie wieder greifen — der Sprint war dauerhaft nicht mehr aktivierbar, und
   `col-backlog` kommt nicht zurück, weil neue Spalten Zufalls-IDs bekommen.
   Deshalb: bevorzugt die jeweils ANDERE Meta-Spalte, und nur wenn beide fehlen,
   die erste verbleibende (dann ist die Sprint-Mechanik ohnehin nicht mehr
   darstellbar, aber die Karte bleibt wenigstens sichtbar). */
export function metaZielSpalte(columnDefs, aktuelleSpalte) {
  const ids = new Set((Array.isArray(columnDefs) ? columnDefs : []).map(c => c?.id).filter(Boolean));
  const bevorzugt = aktuelleSpalte === 'col-erledigt' ? 'col-backlog' : 'col-erledigt';
  if (ids.has(bevorzugt)) return bevorzugt;
  const andere = bevorzugt === 'col-backlog' ? 'col-erledigt' : 'col-backlog';
  if (ids.has(andere)) return andere;
  return columnDefs?.[0]?.id ?? null;
}

/* F-1087 (Karte #537): Werden Meta-Karten headless per Op angelegt, während
   gleichzeitig ein offener Browser-Tab `migrateSprintMeta` durchläuft, kann
   ein Sprint zwei `start`- (bzw. `end`-)Karten bekommen — der Fold lässt
   beliebig viele `sprint-meta`-Karten je Sprint und `metaType` zu, und die
   Prüfung unten fragt nur „gibt es schon eine" auf dem gerade geladenen
   Stand, nicht „gibt es höchstens eine". Real aufgetreten beim Anlegen von
   Sprint 69 (2026-09-28): Orchestrator-Op und Auto-Migration liefen
   gegeneinander, Ergebnis waren zwei Start- und zwei End-Karten.

   Schutzregel wie beim #330-Cleanup (F-364): Inhalt wird nie stillschweigend
   gelöscht. Haben mehrere Doubletten Inhalt, bleiben ALLE stehen, nur eine
   Warnung — das Zusammenführen von zwei mit Inhalt befüllten Übergabe-Notizen
   ist eine redaktionelle Entscheidung, keine automatisierbare. Ist höchstens
   eine mit Inhalt, gewinnt sie (die leeren Doubletten sind ohnehin nur ein
   Nebenprodukt der Wettlauf-Situation).

   F-1095 (Kritiker A, K2): Sind ALLE Doubletten leer im Sinn von
   `istMetaKarteLeer`, gewinnt zuerst die AKTIVIERTE bzw. ABGESCHLOSSENE
   Karte (`columnId === 'col-erledigt'`) — erst danach entscheidet die
   kleinere `displayId`. `istMetaKarteLeer` zählt den
   `sprint-activate`-Activity-Eintrag NICHT als Inhalt (er stammt von
   `actor: 'system'`), eine aktivierte Start-Karte ohne eigene Beschreibung
   gilt also weiterhin als „leer" — genau deshalb entschied vorher blind die
   `displayId`, und eine grössere `displayId` auf der aktivierten Karte
   löschte sie zugunsten einer nie aktivierten. Ergebnis: der Sprint galt
   nach dem Merge als nicht gestartet, eine geschlossene End-Karte verlor
   ihren Abschluss-Zustand. `col-erledigt` ist bei BEIDEN Meta-Typen der
   Zielzustand aus ADR-0042/0047 (Start: aktiviert, End: abgeschlossen), die
   Regel gilt also für beide ohne Fallunterscheidung. Bei Gleichstand
   (mehrere in `col-erledigt`, oder keine) bleibt `displayId` der
   Tie-Breaker — deterministisch wie zuvor. */
function mergeSprintMetaDoubletten(sprint) {
  for (const metaType of ['start', 'end']) {
    const doppelte = sprint.cards.filter(c => c.kind === 'sprint-meta' && c.metaType === metaType);
    if (doppelte.length <= 1) continue;

    const mitInhalt = doppelte.filter(c => !istMetaKarteLeer(c));
    if (mitInhalt.length > 1) {
      console.warn(
        `[migrateSprintMeta F-1087] Sprint "${sprint.id}" hat ${doppelte.length} ` +
        `${metaType}-Meta-Karten, davon ${mitInhalt.length} mit Inhalt ` +
        `(${mitInhalt.map(c => c.id).join(', ')}) → keine wird entfernt. Bitte manuell zusammenführen.`
      );
      continue;
    }

    const gewinner = mitInhalt[0]
      ?? [...doppelte].sort((a, b) => {
        const erledigtDiff = (b.columnId === 'col-erledigt' ? 1 : 0) - (a.columnId === 'col-erledigt' ? 1 : 0);
        return erledigtDiff !== 0 ? erledigtDiff : (a.displayId ?? Infinity) - (b.displayId ?? Infinity);
      })[0];
    const verliererIds = new Set(doppelte.filter(c => c !== gewinner).map(c => c.id));

    console.info(
      `[migrateSprintMeta F-1087] Sprint "${sprint.id}": ${doppelte.length} ` +
      `${metaType}-Meta-Karten gefunden → "${gewinner.id}" behalten, ` +
      `${verliererIds.size} leere Doublette(n) entfernt.`
    );
    sprint.cards = sprint.cards.filter(c => !verliererIds.has(c.id));
  }
}

function migrateSprintMeta(state) {
  if (!state?.board?.sprints) return state;
  const board = state.board;

  /* Sprint-51-Kritiker (K3): Spalten sind löschbar, Meta-Karten tragen aber
     hart kodierte Spalten-IDs (ADR-0042). Landet eine Meta-Karte in einer
     Spalte, die es nicht (mehr) gibt, zeigt der Renderer sie nicht mehr an —
     ihr Inhalt steckt weiter im JSON, ist per UI aber unerreichbar. Für den
     Nutzer sieht das aus wie gelöscht, und genau das sollten F-153/F-364
     verhindern. Deshalb hier zwei Dinge: die Menge der gültigen Spalten für
     die Auto-Aktivierung unten, und ein Selbstheilungs-Pass, der bereits
     verwaiste Meta-Karten in die erste vorhandene Spalte zurückholt (Boards,
     die den Fehler schon erlitten haben, reparieren sich beim nächsten Load).
     Nur Meta-Karten: normale Karten mit ungültiger columnId sind ein eigenes
     Thema und hier bewusst nicht angefasst. */
  const gueltigeSpalten = new Set(
    (Array.isArray(board.columnDefs) ? board.columnDefs : []).map(c => c?.id).filter(Boolean));
  if (gueltigeSpalten.size > 0) {
    board.sprints.forEach(sprint => {
      (sprint.cards || []).forEach(card => {
        if (card.kind !== 'sprint-meta' || gueltigeSpalten.has(card.columnId)) return;
        /* Sprint-51-Kritiker (K-C): semantisch passende Spalte statt blind die
           erste — siehe metaZielSpalte. */
        const ziel = metaZielSpalte(board.columnDefs, card.columnId);
        if (!ziel) return;
        console.warn(
          `[migrateSprintMeta K3] Meta-Karte "${card.title}" (${card.id}) lag in der ` +
          `nicht existierenden Spalte "${card.columnId}" → zurückgeholt nach "${ziel}".`
        );
        card.columnId = ziel;
      });
    });
  }

  /* Sprint-60-Kritiker: `archived:true` auf einer Meta-Karte idempotent
     zuruecknehmen. ADR-0036 schliesst das Archivieren von Meta-Karten aus,
     aber das Flag kann sie trotzdem erreichen — ueber ein importiertes Board
     oder eine `card.field`-Op (die Blockliste in ops.js kennt nur
     `id`/`displayId`). Wirkung waere fatal genug, um sie hier zu heilen
     statt nur zu melden: `renderer.js` blendet archivierte Karten ohne
     Meta-Ausnahme aus, der Archiv-Button ist bei Meta-Karten versteckt — die
     Sprint-Abschluss-Karte mitsamt Recap waere weg. Selbe Logik wie der
     K3-Pass darueber: Boards, die den Zustand schon haben, reparieren sich
     beim naechsten Load. Die Gegenrichtung (Guard beim Setzen) sitzt in
     `modal.js archiveAndClose`. */
  board.sprints.forEach(sprint => {
    (sprint.cards || []).forEach(card => {
      if (card.kind !== 'sprint-meta' || card.archived !== true) return;
      console.warn(
        `[migrateSprintMeta] Meta-Karte "${card.title}" (${card.id}) war archiviert — ` +
        `zurueckgesetzt (ADR-0036: Meta-Karten sind nicht archivierbar).`
      );
      card.archived = false;
    });
  });

  /* Karte #330 / Bug 2: Cleanup-Pass — sprint-meta-Karten aus Swimlanes entfernen,
   * in denen sie nicht sein dürfen (Backlog + Lanes). Tritt auf wenn z. B. eine
   * Swimlane nachträglich von 'sprint' auf 'lane' umgestellt wird, oder wenn
   * alte Daten eine fehlerhafte Zuweisung enthalten.
   * Schutz vor Datenverlust: Meta-Karten mit Inhalt werden NICHT gelöscht — sie
   * verbleiben im Board und werden im Browser-Log gemeldet. Nur wirklich leere
   * Meta-Karten werden entfernt (`istMetaKarteLeer`, siehe dort: F-364 hat den
   * ursprünglichen `comments.length === 0`-Test als zu eng entlarvt). */
  board.sprints.forEach(sprint => {
    const isForbiddenLane =
      sprint.id === '__backlog__' ||
      (sprint.kind && sprint.kind !== 'sprint');
    if (!isForbiddenLane) return; /* Erlaubte Swimlanes überspringen */

    const before = sprint.cards.length;
    sprint.cards = sprint.cards.filter(card => {
      if (card.kind !== 'sprint-meta') return true; /* Normale Karte behalten */
      if (!istMetaKarteLeer(card)) {
        /* Inhalt vorhanden → nicht löschen, nur warnen (F-364) */
        console.warn(
          `[migrateSprintMeta #330] Meta-Karte "${card.title}" (${card.id}) ` +
          `in Swimlane "${sprint.id}" (kind="${sprint.kind ?? 'sprint'}") hat Inhalt ` +
          `(Beschreibung, Kommentare oder Verlauf) → wird NICHT entfernt. Bitte manuell prüfen.`
        );
        return true;
      }
      /* Activity-Log-Eintrag: Entfernung wird beim nächsten Schreiben sichtbar */
      console.info(
        `[migrateSprintMeta #330] Meta-Karte "${card.title}" (${card.id}) ` +
        `aus unerlaubter Swimlane "${sprint.id}" entfernt.`
      );
      return false; /* Leere Meta-Karte entfernen */
    });
    if (sprint.cards.length !== before) {
      /* Idempotenz-Marker: beim nächsten Load sind die Karten bereits weg */
    }
  });

  board.sprints.forEach(sprint => {
    /* Backlog bekommt keine Meta-Karten */
    if (sprint.id === '__backlog__') return;
    /* ADR-0141 §4 (VF-8, Karte #825): Eine gespiegelte Lane (Marke `spiegel`,
       externe Kopie des Board-Paars) hat keine Sprint-Mechanik — Meta-Karten,
       Velocity und Sprint-Abschluss gehören nach innen. Ohne diese Zeile legte
       jeder Browser außen Meta-Karten an und schriebe damit in die Kopie. */
    if (sprint.spiegel) return;
    /* ADR-0046: Lanes haben keine Sprint-Mechanik → keine Meta-Karten */
    if (sprint.kind && sprint.kind !== 'sprint') return;

    /* F-1087 (Karte #537): Doubletten je metaType zusammenführen, BEVOR
       hasStartCard/hasEndCard geprüft wird — siehe mergeSprintMetaDoubletten. */
    mergeSprintMetaDoubletten(sprint);

    const hasStartCard = sprint.cards.some(c => c.kind === 'sprint-meta' && c.metaType === 'start');
    const hasEndCard   = sprint.cards.some(c => c.kind === 'sprint-meta' && c.metaType === 'end');

    if (!hasStartCard) {
      const displayId = Model.assignDisplayId(board);
      /* ADR-0042 (revidiert ADR-0036): Sprint-Start initial in col-backlog/qa:false.
         Erst beim tatsächlichen Sprint-Beginn („Starte Sprint XX") nach col-erledigt
         + qa:true verschieben. Das signalisiert visuell, welcher Sprint aktiv ist.
         ADR-0047: Die Auto-Aktivierung unten kümmert sich darum, sobald eine Karte
         den Backlog verlässt. */
      const startCard = Model.createSprintMetaCard('start', displayId, 'col-backlog', false);
      /* Sort-Anker: Start ganz vorne im cards-Array */
      sprint.cards.unshift(startCard);
    }

    if (!hasEndCard) {
      /* Sprint abgeschlossen? Alle Nicht-Meta-Karten in col-erledigt mit qa:true */
      const normalCards = sprint.cards.filter(c => c.kind !== 'sprint-meta');
      const allDone = normalCards.length > 0 &&
        normalCards.every(c => c.columnId === 'col-erledigt' && c.qa === true);
      const endColId = allDone ? 'col-erledigt' : 'col-backlog';
      const endQa    = allDone;
      const displayId = Model.assignDisplayId(board);
      const endCard   = Model.createSprintMetaCard('end', displayId, endColId, endQa);
      /* Sort-Anker: End ganz hinten im cards-Array */
      sprint.cards.push(endCard);
    }

    /* ADR-0047: Auto-Aktivierung der Start-Karte.
       Sobald eine echte Karte den Backlog verlässt, gilt der Sprint als „de facto aktiv".
       Die Start-Karte wandert dann automatisch nach col-erledigt/qa:true.
       Idempotent: Die nächste Prüfung greift nicht mehr, sobald die Karte bewegt wurde. */
    autoActivateSprintStart(sprint, gueltigeSpalten);
  });

  return state;
}

/* ADR-0047: Hilfsfunktion. Verschiebt die Start-Karte nach col-erledigt/qa:true,
   falls (a) die Start-Karte existiert und in col-backlog steht und (b) mindestens eine
   Nicht-Meta-Karte den Backlog verlassen hat. Schreibt einen Activity-Eintrag mit
   actor="system". Wird bei jedem Board-Load aus migrateSprintMeta heraus aufgerufen.

   F-224/F-225/F-226 (Kritiker Sprint 27):
   - Activity-Eintrag nur einmal pro Karten-Lebenszeit (sucht vorhandenen sprint-activate-
     Eintrag) — schützt vor User-Reset-Backlog-Pfad gegen Doppel-Aktivierungs-Einträge.
   - card.updated wird auf today gesetzt (ADR-0037-Konformität).
   - Activity-ID hat zusätzlich einen Zufalls-Suffix, falls mehrere Sprints im selben
     ms-Tick aktiviert werden. */
function autoActivateSprintStart(sprint, gueltigeSpalten) {
  const startCard = sprint.cards.find(c => c.kind === 'sprint-meta' && c.metaType === 'start');
  if (!startCard || startCard.columnId !== 'col-backlog') return;

  const sprintIsActive = sprint.cards.some(c =>
    c.kind !== 'sprint-meta' && c.columnId !== 'col-backlog'
  );
  if (!sprintIsActive) return;

  /* Sprint-51-Kritiker (K3): NUR aktivieren, wenn es die Zielspalte überhaupt
     gibt. `col-erledigt` steht hier hart kodiert (ADR-0042: die Spalte der
     Start-Karte IST der Aktivierungszustand), aber Spalten sind löschbar. Ohne
     diese Prüfung bekam die Karte eine columnId, die in `columnDefs` nicht
     vorkommt — der Renderer zeigt solche Karten nicht, und die Übergabe-Notizen
     in ihrer Beschreibung waren per UI nicht mehr erreichbar. Für den Nutzer
     nicht von „gelöscht" zu unterscheiden, also genau der Datenverlust, den
     F-153/F-364 verhindern sollten.
     Ist die Spalte weg, bleibt die Karte sichtbar im Backlog stehen und der
     Sprint gilt als nicht aktiviert — kosmetisch falsch, aber verlustfrei, und
     der nächste Load holt es nach, sobald wieder eine Spalte `col-erledigt`
     existiert.

     WICHTIG — `size > 0` ist kein Schnörkel: `migrateDefaultColumns` läuft in
     `checkVersion` NACH `migrateSprintMeta` (F-249/ADR-0052, damit es ein
     weiterhin leeres `columnDefs` füllt). Ein frisches Board hat hier also noch
     gar keine Spalten. Ohne diese Bedingung wäre die Auto-Aktivierung für jedes
     neue Board dauerhaft blockiert — fünf bestehende Tests haben genau das
     sofort gemeldet. Leere Spaltenliste heisst „noch unbekannt", nicht
     „col-erledigt existiert nicht". */
  if (gueltigeSpalten && gueltigeSpalten.size > 0 && !gueltigeSpalten.has('col-erledigt')) return;

  startCard.columnId = 'col-erledigt';
  startCard.qa = true;
  startCard.updated = new Date().toISOString().slice(0, 10);

  if (!Array.isArray(startCard.activity)) startCard.activity = [];
  /* F-224: Doppel-Aktivierungs-Einträge vermeiden — auch wenn User die Karte
     manuell zurück in col-backlog gesetzt hat und der Sprint weiter aktiv ist.

     F-814 (Karte #453) — die Reihenfolge ist tragend, nicht kosmetisch:
     Spalte und `qa` werden OBERHALB gesetzt, der Activity-Eintrag erst hier.
     Nur deshalb holt sich die Aktivierung bei jedem Board-Load erneut nach,
     wenn die Karte wieder im Backlog steht — der vorhandene Eintrag
     unterdrückt das Protokollieren, nicht die Wirkung. F-814 hatte hier eine
     dauerhafte Selbstsperre vermutet („kann sich nie wieder selbst
     nachholen"); der Code widerlegt das an genau dieser Stelle, und
     `tests/sprint-meta.test.js` („F-224: Manueller Reset auf col-backlog …")
     nagelt den Rückweg seit Sprint 27 fest. Die tatsächliche Wurzel des
     beobachteten Vorfalls sass im Diff-Bezug von `src/sync.js` (dort
     dokumentiert). Deshalb hier ein bedingter Push statt eines frühen
     `return`: wer den Guard später nach oben zieht, weil es aufgeräumter
     aussieht, nähme der Selbstheilung ihren Weg. */
  const alreadyActivated = startCard.activity.some(e => e?.type === 'sprint-activate');
  if (!alreadyActivated) {
    const now = new Date().toISOString();
    /* F-226: Zufalls-Suffix gegen ms-Tick-Kollision bei mehreren Aktivierungen im selben Lauf. */
    const rand = Math.random().toString(36).slice(2, 8);
    startCard.activity.push({
      id: `${now}-auto-activate-${rand}`,
      actor: 'system',
      timestamp: now,
      type: 'sprint-activate',
      note: 'Automatisch aktiviert via migrateSprintMeta (ADR-0047)'
    });
  }
}

/* F-842 (Karte #455): Der Eingang wird geklont, BEVOR die Migrations-Kette
   darauf läuft. Bis Sprint 62 mutierten die Migrationen den übergebenen
   Zustand in place — ohne dass Signatur oder Kommentar das sagten
   (`migrateSprintMeta` setzt `card.columnId`/`qa` auf der Karte,
   `migrateDefaultColumns` füllt `board.columnDefs`). Wer den Eingang danach
   noch als UNVERÄNDERTEN Bezug brauchte, hatte ihn nicht mehr; genau daran
   ist F-814 monatelang unentdeckt geblieben (Diff-Bezug des Projektors und
   der migrierte Stand waren dasselbe Objekt, die Sprint-Aktivierung wirkte
   im Speicher und erreichte die Platte nie).

   JSON-Klon, nicht `structuredClone` — dieselbe Begründung wie bei
   `klonBezug` in `src/sync.js`: Der Board-Zustand IST JSON, und was den
   Klon nicht überlebt, überlebt auch den Persist nicht. Das macht den
   Rückgabewert zugleich garantiert persistierbar. `structuredClone` wäre
   schneller, liesse aber Werte durch (Date, Map), die beim nächsten
   Schreiben still verschwänden.

   Kosten gemessen, nicht geschätzt (Dogfooding-Board, 2,2 MB State):
   JSON-Klon 7,4 ms, `structuredClone` 3,3 ms. `checkVersion` läuft beim
   Board-Load und im Poll-Pfad über `validateExternalDelta` — der pollt alle
   15 s (`POLL_MS` in sync.js) und fährt dort ohnehin zwei `JSON.stringify`
   über denselben Zustand. 7,4 ms alle 15 s sind 0,05 % einer CPU. */
const klonZustand = (s) => JSON.parse(JSON.stringify(s));

/* Die Migrationskette — als Liste, nicht als Verschachtelung (ADR-0127).
 *
 * Bis Sprint 65 stand hier ein 20-fach verschachtelter Ausdruck, bei dem die
 * INNERSTE Funktion zuerst lief. Sie war damit rückwärts zu lesen, und es
 * gab keine Möglichkeit, eine Teilmenge auszuführen. (20 Aufrufe, nicht 21 —
 * der 21. Schritt entstand erst, als `migrateBacklogPosition` aus
 * `migrateLaneKind` herausgelöst wurde.) Die Reihenfolge war damit rückwärts zu lesen,
 * und es gab keine Möglichkeit, eine Teilmenge auszuführen — genau das braucht
 * aber der headless-Projektor (ADR-0127).
 *
 * ⚠️ Die Reihenfolge ist Vertrag, nicht Geschmack. Die Abhängigkeiten, die
 * dahinterstehen und die beim Umsortieren brechen:
 *   - `migrateLaneKind` VOR `migrateSprintMeta` (ADR-0046) — sonst greift
 *     dessen kind-Guard nicht und es entstehen Meta-Karten in Backlog-Lanes.
 *   - `migrateDefaultColumns` NACH `migrateV1toV2` (F-249/ADR-0052) — Letzteres
 *     leitet columnDefs aus alten Workflow-Spalten ab, Ersteres füllt nur ein
 *     dann noch leeres columnDefs.
 * Ein Test hält die Reihenfolge namentlich fest; wer sie ändert, ändert ihn mit.
 */

/* Die Kette in Ausführungsreihenfolge, jeder Schritt mit seiner Einstufung.
 *
 * EINE Liste, nicht zwei — das ist nach dem Kritiker-Lauf zur Sitzung
 * 2026-09-17 bewusst so: Zwei getrennte Arrays können auseinanderlaufen, wenn
 * jemand eine Migration ergänzt und nur eines pflegt. Hier ist die Teilmenge
 * ein Filter über dieselbe Quelle und kann es strukturell nicht.
 *
 * `bestandsaendernd: true` heisst: Der Schritt darf NICHT ohne Browser laufen
 * (ADR-0127). Zwei Kriterien führen dazu, und beide sind an Vorfällen gemessen:
 *
 *   (a) NICHT DETERMINISTISCH — der Schritt würfelt (UUID, Zeitstempel).
 *       Zwei Läufe über dieselbe base liefern dann verschiedene Projektionen,
 *       und `detectProjectionAnomaly` meldet bei JEDEM Lauf einen
 *       „Direkt-Patch", den es nie gab. Gemessen am Kritiker-Fixture:
 *       `migrateIdentities` und `migrateTemplates` vergeben frische UUIDs für
 *       defekte Einträge — ein Wächter, der immer anschlägt, ist keiner (F-812).
 *
 *   (b) LEGT ENTITÄTEN AN ODER ENTFERNT SIE — Karten, Swimlanes, Templates,
 *       Identities, Spalten. `migrateTemplates` filtert kaputte Einträge weg,
 *       `migrateIdentities` legt bei fehlendem Feld eine Identity an,
 *       `migrateDefaultColumns` vier Spalten. Über die Kompaktierung landet
 *       das in der base — also in der Wahrheit, ohne dass eine Op es erklärt.
 *
 * Werte INNERHALB einer bestehenden Entität zu normalisieren bleibt erlaubt
 * (ein ungültiges Datum auf `null`, ein defektes `awaitingUser` auf `null`):
 * Das Schema verlangt es, der Browser tut es ohnehin bei jedem Load, und ohne
 * diese Schritte driftete die Projektion vom Browser-Verhalten weg.
 *
 * ⚠️ Die REIHENFOLGE ist Vertrag. Zwei dokumentierte Abhängigkeiten hängen
 * daran, beide brechen beim Umsortieren lautlos:
 *   - `migrateLaneKind` VOR `migrateSprintMeta` (ADR-0046) — sonst greift
 *     dessen kind-Guard nicht und es entstehen Meta-Karten in Backlog-Lanes.
 *   - `migrateDefaultColumns` NACH `migrateV1toV2` (F-249/ADR-0052) — Letzteres
 *     leitet columnDefs aus alten Workflow-Spalten ab, Ersteres füllt nur ein
 *     dann noch leeres columnDefs.
 * Ein Test hält die Reihenfolge namentlich fest; wer sie ändert, ändert ihn mit.
 */
const MIGRATIONSKETTE = [
  { fn: migrateDisplayIds,       bestandsaendernd: false },
  { fn: migrateLinks,            bestandsaendernd: false },
  { fn: migrateAttachments,      bestandsaendernd: false },
  { fn: migrateDates,            bestandsaendernd: false },
  { fn: migrateArchived,         bestandsaendernd: false },
  { fn: migrateActivity,         bestandsaendernd: false },
  { fn: migrateAssignees,        bestandsaendernd: false },
  { fn: migrateProjectId,        bestandsaendernd: false },
  { fn: migrateCover,            bestandsaendernd: false },
  { fn: migrateLaneKind,         bestandsaendernd: false },
  { fn: migrateBacklogPosition,  bestandsaendernd: true  },  /* sortiert Swimlanes um */
  { fn: migrateSprintMeta,       bestandsaendernd: true  },  /* legt Karten an, bewegt sie */
  { fn: migrateBoardBackground,  bestandsaendernd: false },
  { fn: migrateTemplates,        bestandsaendernd: true  },  /* (a) UUID + (b) entfernt Einträge */
  { fn: migrateAwaitingUser,     bestandsaendernd: false },
  { fn: migrateRecurrence,       bestandsaendernd: false },
  { fn: migrateColumnColor,      bestandsaendernd: false },
  { fn: migrateIdentities,       bestandsaendernd: true  },  /* (a) UUID + (b) legt Identity an */
  { fn: migrateDefaultColumns,   bestandsaendernd: true  },  /* (b) legt vier Spalten an */
  { fn: migrateMailFields,       bestandsaendernd: false },
  { fn: migrateSprintArchived,   bestandsaendernd: false },
];

const MIGRATIONEN_ALLE = MIGRATIONSKETTE.map(m => m.fn);
const MIGRATIONEN_NUR_NORMALISIEREND =
  MIGRATIONSKETTE.filter(m => !m.bestandsaendernd).map(m => m.fn);

function fahreMigrationen(state, kette) {
  return kette.reduce((s, m) => m(s), state);
}

/* F-3069 (11.11.): Ein von Hand angelegtes Board trug `"version": 2` als ZAHL.
   Der Browser brach den Projekt-Modus mit „ungültiges Schema“ ab, der headless-Projektor
   reichte die Zahl wortlos durch (normalisiereProjektion prüfte `=== '2'` und kehrte
   unverändert zurück). Beide lesen die Versionsnummer jetzt als Text; geschrieben wird
   immer `"2"`. Andere Werte bleiben unverändert und werden wie bisher abgelehnt. */
function versionAlsText(state) {
  return state && (state.version === 1 || state.version === 2) ? { ...state, version: String(state.version) } : state;
}

function checkVersion(state) {
  if (!state) return state;
  state = versionAlsText(state);
  if (state.version === '1') {
    return fahreMigrationen(migrateV1toV2(klonZustand(state)), MIGRATIONEN_ALLE);
  }
  if (state.version === '2') {
    return fahreMigrationen(klonZustand(state), MIGRATIONEN_ALLE);
  }
  console.error(`Board-Schema-Version "${state.version}" inkompatibel — Laden abgebrochen`);
  return null;
}

/* Normalisiert die Struktur eines v2-Boards, OHNE Bestand zu ändern (ADR-0127).
 *
 * Für den headless-Projektor gebaut. Er braucht dieselben Feld-Defaults wie der
 * Browser — sonst fehlen in der Projektion Schlüssel, die CLAUDE.md für jede
 * Swimlane zusagt (F-896, F-951) —, darf aber nicht dieselben Karten anlegen.
 *
 * Wie gross der Unterschied ist, zeigt eine Messung statt einer Vermutung: Auf
 * einem Fixture-Board mit zwei Sprints legt `checkVersion` VIER Karten an (die
 * 📋/🏁-Meta-Karten) und schiebt eine davon nach `col-erledigt` — das ist die
 * Sprint-Aktivierung. Täte der Projektor das, änderte ein Lauf den Bestand,
 * ohne dass eine Op es erklärt: genau die Lage, gegen die ADR-0126 den
 * Zugehörigkeits-Riegel gebaut hat, nur von innen statt von aussen.
 *
 * Bewusst NUR für `version === '2'`: Ein v1-Board zu migrieren ist ein
 * Strukturumbau (neue Backlog-Swimlane), und der gehört nicht in einen Lauf,
 * dem niemand zusieht. Ein v1-Board kommt unverändert zurück; der Aufrufer
 * entscheidet, was er meldet.
 *
 * Einzige Ausnahme vom „Bestand unverändert“ (F-3069, ADR-0127 Nachtrag 2026-11-11): Die
 * Versionsnummer 2 als ZAHL wird zu "2". Eine Zahl 1 bleibt unangetastet (s. o.: v1 kommt
 * unverändert zurück).
 *
 * @param {object} state  Board-Zustand (wird NICHT verändert — es wird geklont)
 * @returns {object} normalisierter Klon, oder der Eingang bei fremder Version
 */
export function normalisiereProjektion(state) {
  if (state?.version === 2) state = { ...state, version: '2' };
  if (!state || state.version !== '2') return state;
  /* Struktur-Mindestprüfung (Kritiker-Lauf 2026-09-17, P1). Ohne sie lief eine
     base, die ihre `sprints` verloren hat, in `migrateDisplayIds` auf
     `board.sprints.every` und riss den ganzen Lauf mit einem TypeError ab —
     Exit 1, bevor der ADR-0126-Riegel und `detectStructuralLoss` überhaupt
     drankamen. Ausgerechnet DEREN Diagnose ist für genau diesen Stand gebaut
     („abgeschnittene aber parsbare base"), und sie war damit unerreichbar.
     `fold` selbst verkraftet den Stand; der Absturz kam erst mit dieser
     Funktion hinzu. Hier also durchreichen, nicht reparieren: Ein Board ohne
     `sprints` gehört angesehen, nicht normalisiert. */
  if (!Array.isArray(state.board?.sprints)) return state;
  /* …und dieselbe Frage eine Ebene tiefer (Verify-Lauf 2026-09-17, P1). Der
     erste Guard deckte nur `board.sprints`; eine Swimlane OHNE `cards`-Array
     riss `migrateDisplayIds` genauso ab, und der Riegel blieb genauso
     unerreichbar. Der Fall stand wörtlich in der Aufgabenstellung des
     Verify-Laufs — geprüft worden war er trotzdem nicht. Merksatz für die
     nächste Struktur-Mindestprüfung: Eine Sammlung hat zwei Ebenen, und die
     Prüfung der äusseren sagt nichts über die innere. */
  if (!state.board.sprints.every(sp => Array.isArray(sp?.cards))) return state;
  return fahreMigrationen(klonZustand(state), MIGRATIONEN_NUR_NORMALISIEREND);
}

function loadFromEmbedded() {
  try {
    const el = document.getElementById('app-data');
    if (!el) return null;
    const parsed = JSON.parse(el.textContent);
    /* Karte #330 / Bug 1 (Option C — Defense-in-depth): Ein aboard.html das nicht via
     * vite.config.js (Option A) geleert wurde, trägt die board.id 'aboard-project' als
     * Repo-Dogfooding-Daten. Ohne FSA/localStorage-Daten würde dieses Board in einem
     * frischen Projekt-Import als Initial-Board erscheinen — unerwünscht.
     * Schutz: Wenn die eingebetteten Daten die bekannte Dogfooding-ID 'aboard-project'
     * tragen (und KEINE überlegte Leerung stattfand), werden sie ignoriert → Storage.load()
     * fällt auf localStorage / null zurück und erzeugt ein sauberes neues Board.
     * Wichtig: Diese Prüfung gilt NUR für den Fallback-Pfad — ein aktiv genutztes Board mit
     * dieser ID im Projektor-Modus wird immer über FSA/aboard-sync.json geladen, nicht
     * über loadFromEmbedded (das wird in load() nur aufgerufen, wenn kein FSA-Pfad aktiv ist). */
    if (parsed?.board?.id === 'aboard-project') return null;
    return parsed;
  } catch { return null; }
}

/* ADR-0087/F-318: ungescopte Legacy-Autosave-Schlüssel (`aboard-autosave-<id>`,
   ohne `@<pathname>`) sind die file://-Bleed-Quelle.
   **F-NEU-1 (Sprint-38-Kritiker, [K] P1):** im Solo-Modus ist dieser Puffer
   die EINZIGE Persistenz (kein FSA/Server) — ein Hard-Discard wäre
   Datenverlust für Bestands-Solo-Nutzer beim ersten Laden des neuen Builds.
   Daher: **Inhalt einmalig in den scoped Slot migrieren** (nur falls dort
   noch nichts liegt — scoped ist autoritativ/neuer), DANN den Legacy-Key
   entfernen. Idempotent, kein Datenverlust; die file://-Bleed-Mitigation
   ergibt sich aus Baustein 2 (Projektor-Default claude-code → FSA-Prompt)
   plus dem Einmal-/Self-Healing-Charakter. Korrekt-gescopte Schlüssel
   anderer Deployments (`aboard-autosave@/x-…`) beginnen mit
   `aboard-autosave@`, NICHT mit `aboard-autosave-` → unangetastet.
   Pathname-loser Fallback: scoped == legacy → nichts zu tun. */
function migrateLegacyAutosave() {
  const scoped = lsPrefix();
  if (scoped === LS_LEGACY_PREFIX) return; /* kein Pathname → scoped==legacy */
  try {
    const legacyKeys = [];
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (key && key.startsWith(LS_LEGACY_PREFIX) && !key.startsWith(scoped)) {
        legacyKeys.push(key);
      }
    }
    for (const lk of legacyKeys) {
      try {
        const id = lk.slice(LS_LEGACY_PREFIX.length);
        const sk = scoped + id;
        if (localStorage.getItem(sk) == null) {
          const v = localStorage.getItem(lk);
          if (v != null) localStorage.setItem(sk, v); /* F-NEU-1: migrieren, NICHT verwerfen */
        }
        localStorage.removeItem(lk); /* Bleed-Quelle erst NACH Migration entfernen */
      } catch {}
    }
  } catch {}
}

function load() {
  migrateLegacyAutosave();
  const PREFIX = lsPrefix();
  const embedded = loadFromEmbedded();
  if (embedded?.board !== undefined) {
    const state = checkVersion(embedded);
    if (state?.board?.id) {
      try { localStorage.removeItem(PREFIX + state.board.id); } catch {}
    }
    return { state, fromLocalStorage: false };
  }
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (key?.startsWith(PREFIX)) {
        const raw = localStorage.getItem(key);
        if (raw) return { state: checkVersion(JSON.parse(raw)), fromLocalStorage: true };
      }
    }
  } catch {}
  return { state: null, fromLocalStorage: false };
}

function autoSave(state, projectMode = false) {
  if (projectMode || !state?.board?.id) return;
  try {
    localStorage.setItem(lsPrefix() + state.board.id, JSON.stringify(state));
  } catch {}
}

/* ADR-0087: zentraler, deployment-gescopter Autosave-Discard. main.js nutzt
   dies statt den Schlüssel selbst zu bauen (sonst ungescopt → F-318). */
function discardAutosave(boardId) {
  if (!boardId) return;
  try { localStorage.removeItem(lsPrefix() + boardId); } catch {}
  try { localStorage.removeItem(LS_LEGACY_PREFIX + boardId); } catch {}
}

/* projectDirHandle: im Projekt-Modus den bereits freigegebenen Verzeichnis-Handle übergeben
   → kein separater Datei-Picker nötig, da Verzeichnis-Berechtigung already granted */
async function save(state, projectDirHandle = null) {
  const root = document.documentElement.cloneNode(true);
  root.querySelector('#app-data').textContent =
    JSON.stringify({ version: '2', board: state.board }, null, 2);
  root.querySelectorAll('.recovery-banner, .modal-overlay, .col-add-popover, .overflow-menu, .sync-toast, .export-toast, .sync-badge, .sync-permission-banner').forEach(el => el.remove());

  const html = '<!DOCTYPE html>\n' + root.outerHTML;
  const blob = new Blob([html], { type: 'text/html;charset=utf-8' });

  /* Projekt-Modus: aboard.html direkt über Verzeichnis-Handle schreiben — kein Dialog */
  if (projectDirHandle) {
    try {
      const fh = await projectDirHandle.getFileHandle('aboard.html', { create: true });
      const w  = await fh.createWritable();
      await w.write(blob);
      await w.close();
      return { ok: true, method: 'fsa' };
    } catch {
      /* Fallback auf normales FSA wenn Verzeichnis-Handle nicht mehr gültig */
    }
  }

  if (typeof window.showSaveFilePicker !== 'function') {
    const url = URL.createObjectURL(blob);
    const a   = document.createElement('a');
    a.href = url; a.download = 'aboard.html'; a.click();
    setTimeout(() => URL.revokeObjectURL(url), 60000);
    return { ok: true, method: 'download' };
  }

  /* Standalone-Modus: vorgeladenen Handle nutzen */
  if (fileHandle) {
    try {
      const perm = await fileHandle.queryPermission({ mode: 'readwrite' });
      if (perm === 'prompt') await fileHandle.requestPermission({ mode: 'readwrite' });
      const w = await fileHandle.createWritable();
      await w.write(blob);
      await w.close();
      /* F-104: Titel im Wrapper aktualisieren, damit Switch-Popover sprechend bleibt */
      await storeFileHandle(fileHandle, activeBoardId, state?.board?.title ?? null);
      return { ok: true, method: 'fsa' };
    } catch (e) {
      if (e.name === 'AbortError') return { ok: false };
      fileHandle = null;
    }
  }

  /* Kein gültiger Handle → Datei-Picker einmalig */
  try {
    /* F-476 (Sprint-51-Kritiker D4): „erst abreissen, dann prüfen" — bis
       Sprint 61 wurden `fileHandle`, `activeBoardId` UND der IndexedDB-Eintrag
       gesetzt, BEVOR das erste `createWritable()/write()` überhaupt lief.
       Schlug das Schreiben fehl, setzte der catch zwar `fileHandle = null`,
       der IndexedDB-Eintrag blieb aber stehen — das Switch-Popover listete
       danach ein Board, dessen Datei nie geschrieben wurde.
       Jetzt symmetrisch zu `board:reset`/`switchBoard`/`Storage.open()`
       (Karte #372) und zum Zweig darüber: erst schreiben, dann übernehmen.
       Der lokale `handle` ist dafür nötig — würde er direkt nach `fileHandle`
       geschrieben, wäre der Zustand bei einem Schreibfehler wieder halb. */
    const handle = await window.showSaveFilePicker({
      suggestedName: 'aboard.html',
      types: [{ description: 'HTML-Datei', accept: { 'text/html': ['.html'] } }],
    });
    const w = await handle.createWritable();
    await w.write(blob);
    await w.close();
    fileHandle    = handle;
    activeBoardId = state?.board?.id ?? null;
    await storeFileHandle(fileHandle, activeBoardId, state?.board?.title ?? null);
    return { ok: true, method: 'fsa' };
  } catch (e) {
    if (e.name === 'AbortError') return { ok: false };
    fileHandle = null;
    throw e;
  }
}

async function open() {
  const hasFSA  = typeof window.showOpenFilePicker === 'function';
  const TYPES   = [{ description: 'Board-Datei', accept: { 'text/html': ['.html'], 'application/json': ['.json'] } }];

  if (hasFSA) {
    try {
      const [handle] = await window.showOpenFilePicker({ types: TYPES });
      const file = await handle.getFile();
      const state = parseFileData(await file.text(), file.name);
      if (state?.board?.id) {
        fileHandle    = handle;
        activeBoardId = state.board.id;
        await storeFileHandle(handle, activeBoardId, state.board.title ?? null);
      }
      return state;
    } catch (e) {
      if (e.name === 'AbortError') return null;
      throw e;
    }
  } else {
    return new Promise(resolve => {
      const input  = document.createElement('input');
      input.type   = 'file';
      input.accept = '.html,.json';
      input.onchange = async (e) => {
        const file = e.target.files[0];
        if (!file) { resolve(null); return; }
        resolve(parseFileData(await file.text(), file.name));
      };
      input.click();
    });
  }
}

/* Bekanntes Standalone-Board direkt laden (ohne File-Picker) — für Switch-UI */
async function openByBoardId(boardId) {
  const handle = await loadFileHandle(boardId);
  if (!handle) return null;
  try {
    let perm = await handle.queryPermission({ mode: 'readwrite' });
    if (perm === 'prompt') perm = await handle.requestPermission({ mode: 'readwrite' });
    if (perm !== 'granted') return null;
    const file = await handle.getFile();
    const state = parseFileData(await file.text(), file.name);
    if (state?.board?.id) {
      fileHandle    = handle;
      activeBoardId = state.board.id;
      /* Falls Dateiinhalt mittlerweile andere board.id hat als der Key, umsortieren */
      if (state.board.id !== boardId) {
        await storeFileHandle(handle, state.board.id, state.board.title ?? null);
        await deleteFileHandle(boardId);
      } else {
        /* F-104: Titel aktualisieren, falls dieser sich extern geändert hat */
        await storeFileHandle(handle, boardId, state.board.title ?? null);
      }
    }
    return state;
  } catch { return null; }
}

function parseFileData(text, filename = '') {
  if (filename.endsWith('.json') || text.trimStart()[0] === '{') {
    try { return checkVersion(JSON.parse(text)); } catch { return null; }
  }
  return parseHtmlData(text);
}

function parseHtmlData(htmlText) {
  const parser  = new DOMParser();
  const doc     = parser.parseFromString(htmlText, 'text/html');
  const dataEl  = doc.getElementById('app-data');
  if (!dataEl) return null;
  try { return checkVersion(JSON.parse(dataEl.textContent)); } catch { return null; }
}

export const Storage = { load, autoSave, discardAutosave, save, open, openByBoardId, listKnownFiles, checkVersion, preloadFileHandle, normalisiereProjektion };

/* Für den Reihenfolge-Test (ADR-0127): die Kette namentlich, damit eine
   Umsortierung nicht stillschweigend durchgeht. Kein Produktivpfad. */
export const __MIGRATIONSKETTE__ = {
  alle: MIGRATIONEN_ALLE.map(f => f.name),
  normalisierend: MIGRATIONEN_NUR_NORMALISIEREND.map(f => f.name),
  bestandsaendernd: MIGRATIONSKETTE.filter(m => m.bestandsaendernd).map(m => m.fn.name),
};
