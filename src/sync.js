/* Projekt-Modus: aboard-sync.json via FSA + 15s-Polling (→ ADR-0021)
   Handle pro Board-ID in IndexedDB — verhindert Cross-Board-Kapern bei
   gemeinsamer file://-Origin (→ ADR-0028).
   F-103: IndexedDB-Helper in idb.js gemeinsam mit storage.js. */

import { idbPut, idbGet, idbDelete, idbListWithPrefix } from './idb.js';
import { fold, makeOp, PROJECTION_FILE, BASE_FILE, INBOX_DIR, REBASE_MARKE, FOLD_MARKE, UEBERNAHME_ARTEN, uebernahmeMeldung, feldGeaendertMeldung, effortTypMeldung } from './ops.js';
import { diffToOps } from './diff.js';
/* ADR-0145 Nachtrag (#984): Lesedatei, ein Aufschrieb mit dem CLI-Projektor. Nur im
   gegateten Projektor-Pfad benutzt — im slim Build fällt sie per DCE mit. */
import { LESEDATEI_FILE, lesedateiText, lesedateiGesperrt, lesedateiAktuell } from './lesedatei.js';
/* ADR-0127: derselbe Normalisierungsschritt wie im headless-Projektor. Der
   Browser ist der ZWEITE Projektor (ADR-0067) — liefe er ohne, machte er die
   Normalisierung bei jedem Poll rückgängig und meldete dabei den Fehlalarm,
   den die ADR für beseitigt erklärt. Gefunden vom Kritiker-Lauf 2026-09-17. */
import { normalisiereProjektion } from './storage.js';
/* F-1046: Feld-Massstab für die F-925-Meldung aus der Karten-Fabrik. Nur im
   gegateten Projektor-Pfad benutzt — im slim Build fällt er per DCE mit. */
import { kartenFeldMassstab, teileFehlendeFelder } from './feldmassstab.js';
import { partitionInbox, opFileName, makeHeartbeat, detectProjectionAnomaly, formatUnknownKey,
         findOrphanOps, isRotatedBaseName, findFutureOps, findImplausibleTs,
         findOpsVorMarke, findFutureKollisionen,
         FUTURE_TS_TOLERANCE_MS,
         deuteWaise, WAISEN_DEUTUNG } from './inbox.js';
/* #327/F-347/F-363: gemeinsamer Focus-Trap-Helfer für den Mass-Delete-Guard-
   Dialog (ADR-0106). BEWUSST UNGEGATED importiert — siehe Begründung in
   src/focus-trap.js (F-428-Fehlerklasse: der Guard lebt im claude-code-Build,
   der IS_SERVER_BUILD=false hat). */
import { focusInitial, trapTabKey, captureFocus, restoreFocus } from './focus-trap.js';

/* ADR-0106 (#331): Mass-Delete-Guard — Schwellwerte für den Browser-seitigen
   Diff-Guard. Überschreitet ein berechneter Op-Diff diese Grenzen, erscheint
   ein modaler Bestätigungs-Dialog, bevor die Ops an die Inbox angehängt werden.
   Setzt auf IS_PROJECTOR_BUILD (wie der Projektor-Pfad selbst) — im slim Build
   DCE-eliminiert. Whitelist: 'system' und 'Claude *'-Aktoren werden nicht geblockt.
   Beide Konstanten intentional modular benannt, kein Setting nötig (ADR-0106). */
const MASS_DELETE_CARD_THRESHOLD      = 20;  /* mehr als N card.delete → Guard */
const MASS_DELETE_SWIMLANE_THRESHOLD  = 2;   /* mehr als N swimlane.delete → Guard */

/* ADR-0072: Compile-Zeit-Flag (Muster wie IS_SERVER_BUILD / ADR-0064).
   true  → aboard.html (Dogfooding/claude-code/Konsumenten): volle
           ADR-0067-Projektor-Schicht. Auch beide Test-Kontexte (Projektor
           ist sicherheitskritisch → IMMER getestet, vitest.config*.js).
   false → Public-Distributable aboard.template.html + Server-Frontend:
           esbuild eliminiert die Projektor-Funktionsrümpfe per DCE und
           tree-shaked die FUNKTIONEN aus ops.js/diff.js/inbox.js aus dem
           Bundle (Hartlimit < 360000 B, ADR-0114 samt Nachträgen
           2026-10-06/2026-10-12). NICHT „komplett": exportierte
           `Object.freeze`-KONSTANTEN (OP_TYPES, OP_PAYLOAD_SPEC) überleben DCE,
           solange sie keine PURE-Annotation tragen — genau daran hingen 886 B
           in der ersten F-451-Fassung. isMigrated() + der Backstop bleiben
           ungegatet — ein migriertes Board wird NIE per Legacy-Write
           geclobbert (F-283-Wiederkehr verhindert). */
const IS_PROJECTOR_BUILD = (typeof __PROJECTOR_BUILD__ !== 'undefined') &&
  (__PROJECTOR_BUILD__ === true || __PROJECTOR_BUILD__ === 'true');

const SYNC_FILE     = 'aboard-sync.json';
const POLL_MS       = 15_000;
/* F-477: Sentinel fuer "Lesen ist fehlgeschlagen, Datei existiert vermutlich
   trotzdem" -- bewusst UNGLEICH `null`, das weiterhin ausschliesslich "Datei
   existiert nicht" bedeutet. Ein Symbol laesst sich nicht mit einem echten
   Dateiinhalt (String) oder mit `null` verwechseln und ist per `===` eindeutig
   pruefbar. Aufrufer MUESSEN diesen Fall als Abbruch behandeln, nicht als
   "Datei fehlt" -- sonst ueberschreibt ein transienter I/O-Fehler die
   Projektion mit dem lokalen Board (siehe read() unten). */
const SYNC_READ_ERROR = Symbol('sync-read-error');
/* Leaf-Namen für den FSA-Pfad (Browser navigiert Handle-für-Handle, der
   node-CLI nutzt die vollen Pfade aus ops.js — eine Quelle, kein Drift). */
const INBOX_LEAF    = INBOX_DIR;            /* 'aboard-sync.inbox' (top-level) */
const REJECTED_LEAF = '.rejected';          /* in INBOX_DIR */
const HEARTBEAT_LEAF = '.heartbeat.json';   /* in INBOX_DIR */
const LEGACY_KEY    = 'projectDir';            /* prä-ADR-0048 Single-Slot */
const SCOPE_PREFIX  = 'projectDir@';           /* + location.pathname (ADR-0048) */

/* ── IndexedDB ────────────────────────────────────────────────────── */

/* ADR-0048 / F-246: Der Verzeichnis-Handle wird pro DEPLOYMENT (Pfad der
   aboard.html) gescopt, NICHT pro board.id. Grund: im gebauten Single-File
   ist der eingebettete board.id eine Build-Konstante (`aboard-project`) und
   in jeder kopierten aboard.html identisch; bei `file://` teilen alle Kopien
   denselben localStorage/IndexedDB → board.id-Keying kollidiert
   (mehrere Projekte schrieben alle auf `projectDir-aboard-project`).
   `location.pathname` ist eindeutig pro Projektverzeichnis und beim Start
   synchron bekannt. */
function deploymentScope() {
  const p = (typeof globalThis !== 'undefined'
    && globalThis.location && globalThis.location.pathname) || '';
  return p || null;
}

/* arg = explizite Scope-ID aus listKnownBoards (ein pathname, beginnt mit '/')
   ODER ein boardId/undefined von Legacy-Aufrufern → dann das aktuelle
   Deployment. So bleibt die Switch-UI funktional, ohne dass der board.id der
   Legacy-Aufrufer (init/preload) den Key bestimmt. */
function keyFor(arg) {
  const scope = (typeof arg === 'string' && arg.startsWith('/'))
    ? arg : deploymentScope();
  return scope ? SCOPE_PREFIX + scope : LEGACY_KEY;
}

async function storeHandle(handle, arg) {
  await idbPut(keyFor(arg), handle);
}

async function loadHandle(arg) {
  return await idbGet(keyFor(arg));
}

async function deleteHandle(arg) {
  await idbDelete(keyFor(arg));
}

/* Liste bekannter Deployments (Scope = location.pathname) — für Switch-UI.
   `id` ist der Scope (pathname), `name` der Projektordner-Name. */
async function listKnownBoards() {
  const entries = await idbListWithPrefix(SCOPE_PREFIX);
  return entries.map(({ id, value }) => {
    const dir = id.split('/').filter(Boolean).slice(-2, -1)[0];
    return { id, name: value?.name ?? dir ?? id };
  });
}

/* ── Modul-Zustand ────────────────────────────────────────────────── */

export const Sync = (() => {
  let dirHandle      = null;
  let activeBoardId  = null;       /* zu welchem Board gehört dirHandle */
  let pollTimer      = null;
  let lastHash       = '';
  let pendingState   = null;       /* bei Modal-offen zwischengespeichert */
  let onUpdate       = null;       /* callback(state) wenn extern geändert */
  let permNeeded     = false;      /* true → Klick nötig zum Re-Autorisieren */

  /* ── ADR-0067 Projektor-Zustand (claude-code) ───────────────────────
     lastAdopted: die Projektion, die der Browser AKTUELL anzeigt (sein
     optimistischer Diff-Bezug). Der Diff wird gegen DAS gebildet (nicht
     gegen den frisch gefalteten Stand) — sonst würde ein zwischenzeitlich
     extern angehängter Op beim nächsten User-Edit zurückgedreht.
     baseSnapshot: Basis-Snapshot — gecacht. Stufe 1 war er immutabel;
     ADR-0071-Kompaktierung (#230) rotiert ihn (F-302). Der Cache wird
     daher per mtime+size-Signatur gegen Rotation invalidiert. */
  let lastAdopted    = null;
  /* F-814 (Karte #453): `lastAdopted` ist der Diff-Bezug — er muss dem Stand
     auf Platte entsprechen und darf sich NUR hier ändern. Er teilte bisher
     Objektidentität mit fremdem, veränderlichem Zustand: `lastAdopted = proj;
     onUpdate?.(proj)` gab genau das Bezugsobjekt an main.js weiter, wo
     `validateExternalDelta` es durch `Storage.checkVersion` schickt — und die
     Migrationen dort mutieren IN PLACE (`migrateSprintMeta` setzt
     `card.columnId`/`qa` direkt auf der Karte). Jede Migration schrieb sich
     damit in ihren eigenen Diff-Bezug: der nächste `diffToOps(lastAdopted, …)`
     sah keine Differenz mehr, es entstand keine Op, und die Migration wirkte
     im Speicher, erreichte die Platte aber nie. Am Live-Board hat so die
     Sprint-Auto-Aktivierung von Karte #427 monatelang gewirkt, ohne je
     anzukommen. Dasselbe galt für `markAdopted(State.get())`, das direkt den
     lebenden App-State-Baum als Bezug ablegte.
     Deshalb: hereinkommende Stände werden geklont abgelegt, hinausgehende
     geklont übergeben. JSON-Klon (nicht structuredClone), weil der Bezug
     ohnehin gegen den JSON-serialisierten Stand auf Platte steht — was der
     Klon nicht überlebt, überlebt auch den Persist nicht. */
  const klonBezug = (p) => (p == null ? p : JSON.parse(JSON.stringify(p)));
  let baseSnapshot   = null;
  /* F-302: `${mtime}:${size}` der base.json, aus der baseSnapshot stammt.
     Ändert sich die Signatur (headless-Projektor rotiert base bei
     Kompaktierung), ist der Cache stale → verwerfen + neu lesen, sonst
     faltet der Browser stale base + (geprunte) leere Inbox → Regress
     = F-283-Datenverlust-Klasse, von ADR-0071 Stufe 2 reintroduziert. */
  let baseSnapshotSig = null;

  /* ADR-0071 Achse 2D (#230 Task 5): zuletzt von projectorReconcile gezählte
     Anzahl valider Inbox-Ops (== der `opCount`, den der headless-Projektor in
     shouldCompact() auswertet). Reine Lese-Kennzahl für das Kompaktierungs-
     Banner im Renderer — kein Korrektheits-State, default 0 (nicht migriert
     ODER noch nie gefaltet → kein Banner). */
  let _inboxOpCount  = 0;

  /* Karte #337 — Inbox-Bloat-Heuristik:
     _baseMtimeMs: file.lastModified der base.json beim letzten readBaseSnapshot.
     Proxy für „zuletzt kompaktiert" — der headless-Projektor schreibt die base
     nur bei Kompaktierung, daher ändert sich die mtime nur dann.
     _inboxOldestOpMs: Timestamp der ältesten validen Op in der Inbox, aus dem
     Dateinamen rekonstruiert (lexikografisch ältester Name == älteste Op).
     Beide Werte bleiben 0 bis zum ersten projectorReconcile. */
  let _baseMtimeMs        = 0;
  let _inboxOldestOpMs    = 0;

  /* ADR-0070 §3 (F-286): Provider liefert den aktuellen lokalen Editierstand
     { dirty, state, actor } (von main.js: State.isDirty()/State.get()/userName).
     projectorPoll flusht damit einen ausstehenden non-modal Edit als Ops, BEVOR
     es eine extern geänderte Projektion adoptiert — sonst clobbert der Adopt
     den dirty In-Memory-Edit. board-unabhängig (liest die zentrale State). */
  let localStateProvider = null;
  let pollGen        = 0;            /* gegen Stop/Restart-Race der async Migr.-Probe */

  /* F-405/F-456 (Karte #371): Der 15-s-Intervall-Tick ist kein verlässlicher
     Auslöser. Chrome und Firefox throttlen `setInterval` in verborgenen Tabs
     von 15 s auf ≥ 60 s, in Hintergrund-Fenstern oft auf Minuten. Ein Setup,
     das NUR über einen offenen Browser-Tab auf das Board schaut (kein
     SessionStart-Hook, kein CLI-Lauf — der reale Fall eines Konsumenten-Reports,
     Karte #349), faltet die Inbox dann praktisch nicht mehr und zeigt einen
     veralteten Stand. Gegenmittel: beim Zurückwechseln auf den Tab SOFORT
     einmal falten, statt auf den nächsten gethrottelten Tick zu warten.

     Bewusst NICHT hinter IS_PROJECTOR_BUILD: der Auslöser hängt am
     Polling-Grundgerüst, nicht an der ADR-0067-Projektor-Schicht (F-428) —
     der Legacy-`poll()`-Tick eines nicht migrierten Boards profitiert genauso.

     `activeTick` hält den in startPolling aufgelösten Tick, `tickInFlight`
     verhindert Überlappung von Sichtbarkeits- und Intervall-Auslöser.

     F-480 (Sprint-51-Kritiker V6, korrigiert Sprint 61): Die ursprüngliche
     Begründung hier lautete, zwei parallele Läufe würden sich per
     F-457-Wächter „gegenseitig entwerten". Das stimmt nicht — der Wächter
     vergleicht gegen eine FRISCH von der Platte gelesene base-Signatur, nicht
     gegen den Zustand des anderen Laufs; zwei gleichzeitige Ticks entwerten
     sich also gerade nicht. Der Guard ist trotzdem richtig, nur aus einem
     anderen Grund: zwei überlappende Ticks lesen dieselbe Inbox doppelt,
     schreiben dieselbe Projektion doppelt und feuern `onUpdate` doppelt in
     den App-State — Arbeit ohne Nutzen mit einem Fenster für widersprüchliche
     Adoptionen. Der zugehörige Ausstiegs-Fall (Flag bleibt hängen) ist in
     F-469 geschlossen. */
  let activeTick        = null;
  let tickInFlight      = false;
  let visibilityHandler = null;

  /* F-432 (Karte #372): Zustand VOR dem letzten pickDirectory. Siehe
     revertPickedDirectory. `pickInProgress` hält währenddessen die Tick-Läufe
     an (Sprint-51-Kritiker K2) — zwischen dem Umschalten des Handles und der
     Entscheidung des Aufrufers darf nichts auf das fremde Verzeichnis
     schreiben. */
  let pickedPrevious = null;
  let pickInProgress = false;

  /* Karte #290 (Chat-Tail, claude-code-only): Live-Chat-Sicht via Hook +
     FSA-Tail. Read-Pfad ist hinter IS_PROJECTOR_BUILD gegated — der slim
     Public-Distributable enthält keinen Tail-Code. */
  let chatTailEntries  = [];
  let chatTailListener = null;
  let chatTailLastHash = '';
  let chatTailEnabled  = false;

  /* ── Ordner-Handle ──────────────────────────────────────────────── */

  async function pickDirectory(boardId) {
    if (!('showDirectoryPicker' in window)) return null;
    try {
      const h = await window.showDirectoryPicker({ mode: 'readwrite' });
      /* F-432 (Karte #372): Vorzustand sichern, BEVOR umgeschaltet wird.
         Ob der Wechsel gilt, steht erst NACH dem Lesen der Datei fest
         (invalides Schema, Dirty-Rückfrage) — bis dahin muss ein Rückweg
         existieren, sonst zeigt der Handle nach einem Abbruch auf das falsche
         Verzeichnis und der nächste autoPersist schreibt dorthin. Gesichert
         wird BEIDES: der In-Memory-Zustand und der IndexedDB-Eintrag, den das
         folgende storeHandle() überschreibt. */
      pickedPrevious = {
        handle:     dirHandle,
        boardId:    activeBoardId,
        permNeeded,
        stored:     await loadHandle().catch(() => null),
        /* Sprint-51-Kritiker (K1): Die erste Fassung hat diese Felder beim
           Revert auf null/'' gesetzt — mit der Begründung, sie gehörten zum
           zwischenzeitlich gewählten Verzeichnis. Das stimmt nicht: zwischen
           pickDirectory und dem Abbruch läuft KEIN Reconcile, die Werte
           beschreiben also weiter das ursprüngliche Verzeichnis. Sie zu
           nullen entwaffnete zwei Schutzmechanismen — den ADR-0050-Read-
           before-Write-Guard (`lastHash === ''` heisst „Guard aus") und den
           F-286-Pre-Flush in projectorPoll (`if (localStateProvider &&
           lastAdopted)`). Deshalb: exakt sichern, exakt zurücklegen. Das ist
           auch für den Sonderfall richtig, dass vorher gar kein Handle aktiv
           war — dann setzt read() `lastHash` auf den Inhalt der FREMDEN
           Datei, und der Revert muss ihn wieder leeren. */
        lastHash,
        lastAdopted,
        baseSnapshot,
        baseSnapshotSig,
        historyIds:  _historyIds,
        historyKey:  _historyKey,
        inboxOpCount: _inboxOpCount,
      };
      /* K2: Ab hier zeigt `dirHandle` auf das fremde Verzeichnis, während der
         Aufrufer noch liest und entscheidet. Ein Intervall- oder Sichtbarkeits-
         Tick in dieses Fenster hinein würde mit dem FREMDEN Handle falten,
         Ops in dessen Inbox anhängen und dessen Board in die UI adoptieren —
         der Revert kann davon nichts zurücknehmen, er dreht nur den Handle.
         Das Gate hält Tick-Läufe UND den Schreibpfad (`persistGated`) an, bis
         commitPickedDirectory oder revertPickedDirectory entschieden hat.
         Sprint-51-Kritiker (K-B): die erste Fassung deckte nur `runTick` ab,
         versprach im Kommentar aber „nichts" — der 500-ms-`debouncedPersist`
         fiel durch die Lücke. */
      pickInProgress = true;
      dirHandle = h;
      activeBoardId = boardId ?? null;
      /* ADR-0048: unter dem Deployment-Scope ablegen (kein boardId) — eine
         andere file://-Kopie hat einen anderen pathname und überschreibt
         diesen Eintrag nicht mehr (F-246). */
      await storeHandle(h);
      permNeeded = false;
      return h.name;
    } catch (e) {
      if (e.name === 'AbortError') return null;
      throw e;
    }
  }

  /* F-432: Der Wechsel gilt — Vorzustand verwerfen und das Tick-Gate lösen.
     Vom Erfolgspfad in main.js zu rufen. Ohne das bliebe der Revert-Slot
     unbegrenzt scharf (Sprint-51-Kritiker V1): `revertPickedDirectory` ist
     öffentliche API, ein späterer Aufruf würde einen längst akzeptierten
     Verzeichniswechsel stillschweigend zurückdrehen. */
  function commitPickedDirectory() {
    pickedPrevious = null;
    pickInProgress = false;
  }

  /* F-432: Rückweg für einen Abbruch NACH pickDirectory. Stellt den kompletten
     gesicherten Vorzustand wieder her — Handle, Board-Zuordnung, Berechtigungs-
     Flag, Schreib-Baseline und Projektor-Caches — und macht den IndexedDB-
     Eintrag rückgängig; war vorher keiner gespeichert, wird der neue gelöscht
     statt ein alter zurückgeschrieben.
     Der Slot gilt bis zum nächsten pickDirectory oder bis commitPickedDirectory;
     ein Aufruf ohne offenen Slot ist ein No-Op.
     Rückgabe: ob danach wieder ein aktiver (schreibfähiger) Handle steht. */
  async function revertPickedDirectory() {
    pickInProgress = false;
    if (!pickedPrevious) return isActive();
    const prev = pickedPrevious;
    pickedPrevious = null;

    dirHandle     = prev.handle;
    activeBoardId = prev.boardId;
    permNeeded    = prev.permNeeded;
    /* K1: exakt zurücklegen statt nullen — siehe Begründung in pickDirectory. */
    lastHash        = prev.lastHash;
    lastAdopted     = prev.lastAdopted;
    baseSnapshot    = prev.baseSnapshot;
    baseSnapshotSig = prev.baseSnapshotSig;            /* F-302 */
    _historyIds     = prev.historyIds;                 /* F-448 */
    _historyKey     = prev.historyKey;
    _inboxOpCount   = prev.inboxOpCount;

    try {
      if (prev.stored) await storeHandle(prev.stored);
      else             await deleteHandle();
    } catch { /* IndexedDB unerreichbar → der In-Memory-Handle zählt */ }
    return isActive();
  }

  /* Handle dieses Deployments (oder einer explizit übergebenen Scope-ID aus
     der Switch-UI) laden + Berechtigung prüfen. ADR-0048: KEINE board.id-
     Legacy-Migration mehr — die alten `projectDir-<id>`-Einträge sind genau
     die geteilten/kollidierenden (F-246) und dürfen nicht adoptiert werden.
     Fehlt der Deployment-Eintrag → false → Nutzer wählt einmalig neu. */
  async function tryRestoreHandle(arg) {
    const h = await loadHandle(arg);
    if (!h) return false;
    if (typeof arg === 'string' && !arg.startsWith('/')) activeBoardId = arg;
    try {
      const perm = await h.queryPermission({ mode: 'readwrite' });
      if (perm === 'granted') { dirHandle = h; permNeeded = false; return true; }
      if (perm === 'prompt')  { dirHandle = h; permNeeded = true;  return true; }
    } catch {}
    return false;
  }

  async function requestPermission() {
    if (!dirHandle) return false;
    try {
      const perm = await dirHandle.requestPermission({ mode: 'readwrite' });
      permNeeded = perm !== 'granted';
      return perm === 'granted';
    } catch { return false; }
  }

  function getDirectoryName()   { return dirHandle?.name ?? null; }
  function getDirectoryHandle() { return dirHandle; }
  function getActiveBoardId()   { return activeBoardId; }
  function isPermissionNeeded() { return permNeeded; }
  function isActive()           { return !!dirHandle && !permNeeded; }

  /* Aktiven Handle vergessen (z.B. bei Reset auf neues Board) — Eintrag
     in IndexedDB bleibt erhalten für späteren Wechsel via Switch-UI. */
  function clearActiveHandle() {
    stopPolling();
    dirHandle = null;
    activeBoardId = null;
    permNeeded = false;
    lastHash = '';
    /* ADR-0067: Projektor-Bezug + base-Cache gehören zum alten Board/Handle */
    lastAdopted = null;
    baseSnapshot = null;
    baseSnapshotSig = null;                            /* F-302 */
    _historyIds = null; _historyKey = '';              /* F-448 */
    _gemeldeteWaisen.clear();                          /* F-459 */
    _gemeldeteZeitstempel.clear();                     /* F-831 */
    pickedPrevious = null; pickInProgress = false;     /* F-432 */
  }

  /* ADR-0048: Der Handle-Key hängt am Deployment-Pfad, nicht an board.id.
     Das frühere Umkeyen+Löschen (oldId→newId) war genau die F-246-Ursache:
     es überschrieb/löschte den geteilten `projectDir-aboard-project`-Eintrag
     anderer Deployments. Hier nur noch die informative activeBoardId pflegen,
     keine IndexedDB-Mutation. */
  async function setBoardId(newBoardId) {
    activeBoardId = newBoardId ?? activeBoardId;
  }

  /* ── aboard-sync.json schreiben / lesen ─────────────────────────── */

  /* F-247 / ADR-0050: Read-before-Write-Guard. Vor dem Writeback prüfen, ob
     die Datei seit unserem letzten Lesen/Schreiben (`lastHash`) extern
     verändert wurde. Falls ja → NICHT überschreiben, sondern den Konflikt
     melden (`{ conflict, diskState, diskRaw }`). `lastHash` bleibt dabei
     unverändert (sticky), damit weitere Auto-Writebacks ebenfalls geblockt
     bleiben, bis der User entschieden hat. `opts.force` umgeht den Guard
     (board:reset, „Meine Version behalten"). */
  async function write(state, opts = {}) {
    if (!isActive()) return false;
    const force = opts.force === true;
    try {
      const fh = await dirHandle.getFileHandle(SYNC_FILE, { create: true });
      if (!force && lastHash !== '') {
        let disk = null;
        try { disk = await (await fh.getFile()).text(); } catch { disk = null; }
        if (disk !== null && disk !== lastHash) {
          let diskState = null;
          try { diskState = JSON.parse(disk); } catch { diskState = null; }
          /* Parsebarer Fremd-Stand → Konflikt melden, lastHash NICHT bewegen.
             Korruptes/teilgeschriebenes JSON → durchfallen lassen und
             überschreiben (gegen Save-Deadlock, ADR-0050 Rest-Risiko). */
          if (diskState) return { conflict: true, diskState, diskRaw: disk };
        }
      }
      const w  = await fh.createWritable();
      const json = JSON.stringify({ version: state.version, board: state.board }, null, 2);
      await w.write(json);
      await w.close();
      lastHash = json;
      return true;
    } catch { return false; }
  }

  async function read() {
    if (!dirHandle) return null;
    try {
      const fh   = await dirHandle.getFileHandle(SYNC_FILE);
      const file = await fh.getFile();
      const text = await file.text();
      /* F-247 / ADR-0050: Beim ersten erfolgreichen Lesen die Baseline
         setzen, damit der Schreib-Guard ab App-Start scharf ist (schließt
         das 0–15 s-Fenster vor dem ersten poll()). poll()/write() besitzen
         lastHash danach; ein nicht-leerer Stand wird hier nie überschrieben. */
      if (lastHash === '' && text) lastHash = text;
      return text;
    } catch (err) {
      /* F-477: `null` bedeutet "Datei existiert nicht" und darf NUR bei
         genau diesem Fehler zurückkommen -- Aufrufer in main.js deuten `null`
         als Startsignal, aboard-sync.json frisch anzulegen (Sync.write mit
         dem lokalen Board). Jeder andere Fehler (transienter I/O-Fehler,
         Berechtigungs-Grenzfall, o.ä.) muss stattdessen als Abbruch ankommen,
         sonst überschreibt der nächste Aufrufer eine vorhandene, nur gerade
         unlesbare Datei mit dem lokalen Stand -- die Verlustklasse aus F-432
         eine Verzweigung tiefer.
         Bewusst KEIN `instanceof DOMException`: Die File System Access API
         wirft bei fehlender Datei einen DOMException mit `name ===
         'NotFoundError'`, aber Tests/Mocks (happy-dom, Fakes) liefern dafür
         häufig ein Objekt, das nur `.name` trägt, keine echte DOMException
         ist. Die Prüfung ist deshalb absichtlich defensiv auf `.name` statt
         auf den Typ -- und die Richtung ist ebenso absichtlich einseitig:
         "unbekannter Fehler" fällt auf die sichere Seite (Abbruch), nicht auf
         die riskante ("Datei fehlt" → überschreiben). Ein falsch als "fehlt"
         gedeuteter Fehler kostet die ganze Datei; ein falsch als "Fehler"
         gedeuteter Fehlschlag im „Datei fehlt wirklich"-Fall kostet im
         schlimmsten Fall nur einen ausbleibenden Neuanlage-Versuch. */
      if (err && err.name === 'NotFoundError') return null;
      return SYNC_READ_ERROR;
    }
  }

  /* F-247 / ADR-0050: Baseline explizit auf einen bekannten Disk-Stand
     heben — nötig nach „Disk-Version laden", sonst re-konfliktiert der
     soeben adoptierte Stand beim nächsten Writeback. */
  function markBaseline(raw) {
    if (typeof raw === 'string') lastHash = raw;
  }

  /* ── ADR-0067 Projektor (claude-code) ────────────────────────────────
     Behebt F-283: der Browser schreibt NICHT mehr die ganze Datei. Statt
     dessen: User-Edit → Diff gegen lastAdopted → semantische Ops → in die
     Inbox angehängt → der Browser (als Projektor, weil offen) faltet
     base+ALLE Inbox-Ops → schreibt die Projektion → adoptiert sie.
     `aboard-sync.json` ist damit projektor-geschrieben und für externe
     Akteure read-only; die Wahrheit liegt in base+Inbox, nicht im
     Browser-Memory. Die FSA-`close()` ist atomar (Swap-Datei) — kein
     manuelles tmp+rename nötig.

     ADR-0071-INVARIANTE (Achse 2D): Der Browser-Projektor KOMPAKTIERT
     NIE — er fasst `aboard-sync.base.json` ausschließlich LESEND an
     (`readBaseSnapshot`, per mtime+size-Signatur gegen Rotation
     invalidiert gecacht — F-302) und rotiert/prunt nichts.
     Kompaktierung/Rotation (neue base, Op-Pruning, Snapshot-Rotation)
     macht AUSSCHLIESSLICH der headless-Projektor (tools/aboard-
     projektor.mjs); der Banner-Button hängt nur eine append-only
     `maintenance.compact`-Marker-Op an. Würde der Browser base schreiben,
     wäre das die F-283/F-294-Bug-Klasse auf der Wahrheit selbst. */

  async function fsaReadText(handle, name) {
    try {
      const fh = await handle.getFileHandle(name);
      return await (await fh.getFile()).text();
    } catch { return null; }
  }

  async function fsaWriteText(handle, name, text) {
    const fh = await handle.getFileHandle(name, { create: true });
    const w  = await fh.createWritable();
    await w.write(text);
    await w.close();           /* FSA: atomarer Commit beim close() */
  }

  /* ADR-0145 Nachtrag (#984, F-2688): nach jedem Schreiben der Projektion
     auch die Lesedatei aboard-sync.offen.json erneuern — derselbe Aufschrieb
     wie im CLI-Projektor (src/lesedatei.js). Nur erneuern, nie anlegen: Die
     git-Regel aus F-2691 kann der Browser nicht prüfen; eine vorhandene Datei
     hat der CLI-Projektor unter dieser Regel angelegt. Ein Fehler wird nur
     gemeldet (ADR-0145 §6), die Projektion steht schon. */
  async function schreibeLesedatei(projText, board) {
    if (!IS_PROJECTOR_BUILD) return;                  /* ADR-0072: DCE im slim Build */
    try {
      /* Nur über den Handle der VORHANDENEN Datei schreiben, nie mit create
         (Kritik B-2: sonst legte ein Löschen zwischen Prüfung und Schreiben sie
         doch an). Eine vom CLI-Projektor gesperrte Datei bleibt, wie sie ist (B-1). */
      let fh;
      try { fh = await dirHandle.getFileHandle(LESEDATEI_FILE); } catch { return; }
      const vorhanden = await (await fh.getFile()).text();
      if (lesedateiGesperrt(vorhanden)) return;
      const roh = new TextEncoder().encode(projText);
      const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', roh));
      const sha256 = Array.from(digest, b => b.toString(16).padStart(2, '0')).join('');
      /* F-2702: unverändert (gleiche Projektion) → nicht neu schreiben. */
      if (lesedateiAktuell(vorhanden, sha256)) return;
      const { text } = lesedateiText(board, { datei: PROJECTION_FILE, bytes: roh.length, sha256 }, new Date());
      const w = await fh.createWritable();
      await w.write(text);
      await w.close();
    } catch (e) {
      console.warn('aBoard: Lesedatei aboard-sync.offen.json nicht geschrieben (ADR-0145 §6):', e?.message || e);
    }
  }

  async function getInboxDir(create = true) {
    return await dirHandle.getDirectoryHandle(INBOX_LEAF, { create });
  }

  /* ADR-0070 §1 (F-291): NUR lesen — KEIN implizites base-Schreiben mehr.
     Fehlt base.json → Board ist nicht migriert → null → Projektor-Pfad inert
     (der Aufrufer routet dann auf den Legacy-ADR-0050-Pfad). Die Migration
     ist ausschließlich die explizite migrate()-Aktion. */
  async function readBaseSnapshot() {
    /* F-302: base.json statten (mtime+size) — billig, kein text()/parse.
       Fehlt sie → Board nicht migriert (Verhalten wie fsaReadText: null). */
    let file;
    try {
      file = await (await dirHandle.getFileHandle(BASE_FILE)).getFile();
    } catch { return null; }                          /* nicht migriert */
    const sig = file.lastModified + ':' + file.size;
    /* Cache nur gültig, solange die base.json-Signatur unverändert ist.
       ADR-0071-Kompaktierung rotiert base (neuer Inhalt → andere size,
       i.d.R. auch mtime) → Signatur ändert → Cache verwerfen, sonst
       faltet der Browser stale base + leere (geprunte) Inbox = Regress.
       Restrisiko gleiche size+mtime(ms) zweier Kompaktierungen derselben
       ms vernachlässigbar (fold+write > 1 ms; geänderter Inhalt ändert
       i.d.R. size). Datei-Snapshot konsistent: sig UND text() stammen aus
       demselben File-Objekt (spätere Rotation → nächster Tick neue sig). */
    if (baseSnapshot && baseSnapshotSig === sig) return baseSnapshot;
    let baseRaw;
    try { baseRaw = await file.text(); } catch { return null; }
    if (baseRaw == null) return null;
    let parsed;
    try { parsed = JSON.parse(baseRaw); } catch { return null; }
    baseSnapshot = parsed;
    baseSnapshotSig = sig;
    /* Karte #337: mtime der base.json als Proxy fuer den letzten Projektor-Lauf
       (Kompaktierung oder regulaere Faltung). Nur aktualisieren, wenn sich die
       Signatur aendert (neuer Cache-Miss = neue base-Version). */
    _baseMtimeMs = file.lastModified || 0;
    return baseSnapshot;
  }

  /* Laufzeit-Gate (ADR-0070 §1/§2): Board ist ADR-0067-migriert gdw.
     aboard-sync.base.json existiert. Probe-only — legt NICHTS an. */
  async function isMigrated() {
    if (!dirHandle) return false;
    return (await fsaReadText(dirHandle, BASE_FILE)) != null;
  }

  /* Deliberate, idempotente Einmal-Migration (ADR-0070 §1). KEIN automatischer
     Aufrufer im Browser — nur explizite Operator-Aktion (CLI `--migrate`) bzw.
     ein bewusst gegateter UI-/Test-Pfad. Projektion 1:1 → immutabler base +
     Inbox-Verzeichnis. Existiert base bereits → No-Op (true). */
  async function migrate() {
    if (!IS_PROJECTOR_BUILD) return false;            /* ADR-0072: slim Build migriert nie */
    if (!isActive()) return false;
    if ((await fsaReadText(dirHandle, BASE_FILE)) != null) return true;
    const projRaw = await fsaReadText(dirHandle, PROJECTION_FILE);
    if (projRaw == null) return false;                /* noch kein Board */
    await fsaWriteText(dirHandle, BASE_FILE, projRaw);
    await getInboxDir(true);
    baseSnapshot = null;
    baseSnapshotSig = null;                            /* F-302 */
    return true;
  }

  /* Inbox lesen, Poison nach .rejected/ aussortieren, base+Ops falten.
     Reine fold-Wahrheit — unabhängig davon, WER faltet (Konvergenz). */
  async function projectorReconcile() {
    if (!IS_PROJECTOR_BUILD) return null;             /* ADR-0072: DCE im slim Build */
    const base = await readBaseSnapshot();
    if (!base) return null;
    /* F-448: Signatur der gelesenen base festhalten. Ändert sie sich bis zum
       Ende dieses Laufs, hat das CLI dazwischen kompaktiert — dann ist das
       Verhältnis base/Inbox nicht mehr konsistent und die Waisen-Diagnose
       schweigt (siehe reportOrphanOps). */
    const baseSig = baseSnapshotSig;
    const inbox = await getInboxDir(true);
    const entries = [];
    for await (const [name, h] of inbox.entries()) {
      if (h.kind !== 'file') continue;                /* .rejected/ überspringen */
      if (name.startsWith('.')) continue;             /* .heartbeat.json */
      if (!name.endsWith('.json')) continue;
      try { entries.push({ name, text: await (await h.getFile()).text() }); } catch {}
    }
    const { ops, rejected, warnings } = partitionInbox(entries);
    _inboxOpCount = ops.length;          /* reine Lese-Kennzahl fürs Banner */
    /* F-451: unbekannte Payload-Schlüssel auch im Browser-Projektor melden.
       Reines Logging ausserhalb von fold() — die Projektion bleibt davon
       unberührt (ADR-0067 §2). Anders als die Forensik-Schicht aus F-448
       braucht das keine rotierten Snapshots und ist darum hier schon
       vollständig; ein Op-Schreiber im claude-code-Modus sieht seinen
       Namensdreher damit ohne headless-Lauf. */
    for (const w of warnings) {
      console.warn(`[aBoard] Unbekannte Payload-Schlüssel in ${w.name} (${w.type}): `
        + `${w.unknown.map(formatUnknownKey).join(', ')}`
        + ' — Op gefaltet, diese Felder ohne Wirkung (F-451).');
    }
    /* F-831: Die Zeitstempel-Wächter liefen bisher NUR im CLI. Bei offenem
       Tab ist der Browser der aktive Projektor (ADR-0067) — eine vordatierte
       oder rückdatierte Op wurde also erst gemeldet, wenn irgendwann ein
       headless-Lauf kam. Genau dafür wurde die Waisen-Diagnose (F-448)
       seinerzeit hierher gezogen; dieselbe Begründung, dieselbe Stelle.
       Wie dort: melden, nie ablehnen — `fold` bleibt unberührt. */
    meldeZeitstempelAuffaelligkeiten(ops);
    /* F-1014 (Kritiker-Lauf Sprint 66, Befund A2): dieselbe Begründung wie bei
       F-831 direkt darüber, nur eine Version später — und der Fehler war
       derselbe. Der Schutz vor Ops, die älter sind als ein
       `--rebase-from-projection`, kam zuerst NUR ins CLI. Bei offenem Tab ist
       aber der Browser der aktive Projektor (ADR-0067): Er hätte die
       doppelt wirkenden Ops lautlos ein zweites Mal angewandt, und die Meldung
       wäre erst gekommen, wenn irgendwann ein headless-Lauf folgt. Genau das
       beschreibt §25/§26 in knowledge/topics/nebenlaeufigkeit-und-lesefenster.md
       („wer eine Rolle ändert, die zweimal implementiert ist, hat sie erst halb
       geändert") — hier gegen die eigene, am selben Tag notierte Regel verstossen
       und im Kritiker-Lauf gefunden. */
    const _rebaseGemeldet = await meldeOpsVorRebaseMarke(ops);
    await meldeOpsVorFoldMarke(ops, _rebaseGemeldet);
    /* Karte #337: älteste valide Op aus Dateinamen ableiten.
       Dateiname-Format: YYYY-MM-DDTHH-MM-SS-sssZ-<uuid>.json (opFileName).
       Rekonstruktion: ersten 24 Zeichen aufteilen (ISO ohne Trenner) */
    if (ops.length > 0) {
      /* Op-ts-Felder sind zuverlässiger als Dateinamen (Unicode-Safe),
         und `fold()` sortiert ohnehin nach ts. Ältester ts = MIN(ops[].ts). */
      let minMs = Infinity;
      for (const op of ops) {
        const t = op.ts ? Date.parse(op.ts) : NaN;
        if (Number.isFinite(t) && t < minMs) minMs = t;
      }
      _inboxOldestOpMs = Number.isFinite(minMs) ? minMs : 0;
    } else {
      _inboxOldestOpMs = 0;
    }
    if (rejected.length) {
      const rej = await inbox.getDirectoryHandle(REJECTED_LEAF, { create: true });
      for (const r of rejected) {
        /* F-451-Kritiker: den GRUND mitgeben. Ohne ihn verschwindet die Op
           lautlos in .rejected/ — ausgerechnet die stärkere Neuerung
           (`payload-missing:toSprintId` statt `invalid-op`) erreichte den
           Browser-Nutzer sonst nie, während die schwächere Warnung ankam. */
        console.warn(`[aBoard] Op nach .rejected/ aussortiert: ${r.name} (${r.reason})`);
        try {
          const txt = await (await (await inbox.getFileHandle(r.name)).getFile()).text();
          await fsaWriteText(rej, r.name, txt);
          await inbox.removeEntry(r.name);
        } catch { /* best-effort — Poison bleibt sonst liegen, blockiert aber nicht */ }
      }
    }
    /* F-457 (Sprint-50-Kritiker, K1): Der Wettlauf-Schutz galt zunächst NUR
       der Diagnose — das war zu wenig. Kompaktiert das CLI zwischen unserem
       base-Read (oben) und dem Inbox-Read (gerade eben), sieht dieser Lauf
       die ALTE base neben der bereits geprunten Inbox. `fold(base_alt, [])`
       ist dann exakt der alte Stand, und der Aufrufer schreibt ihn als
       Projektion zurück, setzt ihn als `lastAdopted` und schiebt ihn per
       onUpdate in den App-State: ein Rückfall um bis zu AUTO_COMPACT_THRESHOLD
       Ops, aus dem der nächste User-Edit einen Lösch-Diff bauen würde.
       Seit Karte #342 ist das kein Randfall mehr — vorher brauchte eine
       Kompaktierung 150 Ops UND einen Menschen am Banner-Button, jetzt feuert
       sie ab 50 unbeaufsichtigt aus dem SessionStart-Hook, während jeder
       offene Tab alle 15 s pollt.
       `null` ist hier gefahrlos: alle drei Aufrufer brechen bei fehlender
       Projektion ab und der nächste Tick faltet mit frischer base neu —
       fold ist rein, der Zustand konvergiert (ADR-0067 §2). */
    if (baseSig && await baseSignature() !== baseSig) {
      console.info('[aBoard] base.json hat sich während des Faltens geändert'
        + ' (Kompaktierung durch den headless-Projektor) — dieser Lauf wird'
        + ' verworfen, der nächste Tick faltet mit frischer base.');
      return null;
    }
    /* F-417-Nacharbeit: Klemmungen melden, je Op einmal pro Sitzung — der
       Browser faltet alle 15 s neu. Dieselbe Stelle und dasselbe Muster wie
       die Payload- und Zeitstempel-Warnungen; `fold` selbst bleibt rein, das
       Protokoll ist ein Parameter des Aufrufers. */
    const klemmungen = [];
    const feldMassstab = kartenFeldMassstab();
    const projection = fold(base, ops, klemmungen, { feldMassstab: feldMassstab.felder, effortTyp: true });
    for (const k of klemmungen) {
      /* F-926 (Karte #484): Dieselbe Meldung wie im CLI — und sie steht hier,
         weil der Browser-Projektor der aktive ist, solange ein Tab offen ist
         (§27 in knowledge/topics/nebenlaeufigkeit-und-lesefenster.md: wer eine
         der beiden Projektor-Implementierungen anfasst, zieht die andere nach).
         Der Präzedenzfall steht zwölf Zeilen höher: F-831 hat die
         Zeitstempel-Wächter aus genau diesem Grund vom CLI hierher geholt. */
      if (k.art === 'wirkungslos') {
        /* F-965 (b), Karte #490 — siehe CLI-Gegenstück in
           tools/aboard-projektor.mjs. Beide Projektor-Implementierungen melden
           es, weil bei offenem Tab dieser hier der aktive ist (§27). */
        if (_gemeldeteZeitstempel.has(`wirkungslos:${k.op}`)) continue;
        _gemeldeteZeitstempel.add(`wirkungslos:${k.op}`);
        console.warn(`[aBoard] Op ${k.op} (${k.typ}) wurde beim Falten von einer Regel`
          + ` verworfen und hat NICHTS bewirkt (F-965): ${k.text}`);
        continue;
      }
      if (k.art === 'ohne-ziel') {
        /* F-3094 (Karte #1171) — CLI-Gegenstück in tools/aboard-projektor.mjs (§27). */
        if (_gemeldeteZeitstempel.has(`ohneziel:${k.op}`)) continue;
        _gemeldeteZeitstempel.add(`ohneziel:${k.op}`);
        console.warn(`[aBoard] Op ${k.op} (card.upsert) legt die neue Karte ${k.cardId} nicht an`
          + ` (F-3094): ${k.text}`);
        continue;
      }
      if (k.art === 'unvollstaendig') {
        /* F-925 (Karte #508) — CLI-Gegenstück in tools/aboard-projektor.mjs.
           §27: beide Projektoren melden, bei offenem Tab ist dieser der aktive.
           F-1046: nur was DAUERHAFT fehlt, ist ein Befund; was die
           Normalisierung nachträgt, steht in der Projektion ohnehin. */
        const { ergaenzt, dauerhaft } = teileFehlendeFelder(k.felder, feldMassstab);
        if (dauerhaft.length === 0) continue;
        if (_gemeldeteZeitstempel.has(`unvollst:${k.op}`)) continue;
        _gemeldeteZeitstempel.add(`unvollst:${k.op}`);
        console.warn(`[aBoard] Die per Op neu angelegte Karte ${k.cardId} trägt Felder nicht,`
          + ' die `Model.createCard` jeder Karte gibt (F-925/F-1046). Fehlt dauerhaft —'
          + ` ${dauerhaft.join(', ')}`
          + (ergaenzt.length ? `; wird beim Normalisieren ergänzt — ${ergaenzt.join(', ')}` : '')
          + '. Karten besser mit `tools/aboard-op.mjs` anlegen — das füllt auf.');
        continue;
      }
      if (k.art === 'entkernung') {
        if (_gemeldeteZeitstempel.has(`entkern:${k.op}`)) continue;
        _gemeldeteZeitstempel.add(`entkern:${k.op}`);
        console.warn(`[aBoard] Op ${k.op} (card.upsert) hat auf der BESTEHENDEN Karte`
          + ` ${k.cardId} ${k.felder.length} Feld(er) fallen lassen — ${k.felder.join(', ')}`
          + ' (F-926). `applyOp` ersetzt bei card.upsert den kompletten Karteninhalt;'
          + ' bewahrt werden nur displayId und columnId. Einzelne Felder ändert man'
          + ' mit `card.field`, Kommentare mit `comment.add`/`comment.delete`.');
        continue;
      }
      if (k.art === 'feld-geaendert') {
        /* Sprint-77-Kritiker — CLI-Gegenstück in tools/aboard-projektor.mjs (§27). */
        if (_gemeldeteZeitstempel.has(`feldif:${k.op}`)) continue;
        _gemeldeteZeitstempel.add(`feldif:${k.op}`);
        console.warn(`[aBoard] ${feldGeaendertMeldung(k)}`);
        continue;
      }
      if (k.art === 'effort-typ') {
        /* Karte #772 — CLI-Gegenstück in tools/aboard-projektor.mjs (§27). */
        if (_gemeldeteZeitstempel.has(`effort:${k.op}`)) continue;
        _gemeldeteZeitstempel.add(`effort:${k.op}`);
        console.warn(`[aBoard] ${effortTypMeldung(k)}`);
        continue;
      }
      if (UEBERNAHME_ARTEN.includes(k.art)) {
        /* Karte #606, K-4 — CLI-Gegenstück in tools/aboard-projektor.mjs (§27). */
        if (_gemeldeteZeitstempel.has(`uebernahme:${k.op}`)) continue;
        _gemeldeteZeitstempel.add(`uebernahme:${k.op}`);
        console.warn(`[aBoard] ${uebernahmeMeldung(k)}`);
        continue;
      }
      /* Rest-Zweig (Kritiker-Lauf zu Karte #490): Der Klemm-Zweig war der
         Default — eine künftige vierte Art wäre hier als Klemm-Meldung mit
         lauter `undefined` gelandet. Jetzt ist er festgenagelt, und Unbekanntes
         meldet sich als Unbekanntes. */
      if (k.art !== 'klemmung') {
        if (_gemeldeteZeitstempel.has(`unbekannt:${k.op}`)) continue;
        _gemeldeteZeitstempel.add(`unbekannt:${k.op}`);
        console.warn(`[aBoard] Protokoll-Eintrag mit unbekannter Art "${k.art}" (Op ${k.op}) —`
          + ' der Apply-Core meldet etwas, das dieser Projektor nicht kennt.'
          + ' Beim Ergänzen einer neuen Art wurde src/sync.js nicht nachgezogen.');
        continue;
      }
      if (_gemeldeteZeitstempel.has(`klemm:${k.op}`)) continue;
      _gemeldeteZeitstempel.add(`klemm:${k.op}`);
      console.warn(`[aBoard] Op ${k.op} (${k.typ}) zeigte auf die Spalte "${k.von}",`
        + ` die es im Board nicht gibt — Karte ${k.cardId} wurde auf "${k.nach}" geklemmt`
        + ' statt unsichtbar zu werden (F-417). Die Korrektur ist endgültig.');
    }
    await reportOrphanOps(base, projection, ops);
    /* ADR-0127: Die Projektion ist `fold(base, ops)`, NORMALISIERT — und zwar
       in BEIDEN Projektoren. Ohne diese Zeile schrieb der Browser bei jedem
       Poll den rohen Fold zurück: Die Feld-Defaults aus F-896/F-951 kehrten
       zurück, sobald ein Tab offen war, die Datei pendelte zwischen zwei
       Fassungen (sie ist git-getrackt), und der Anomalie-Check verglich eine
       normalisierte Datei gegen einen rohen Fold — also derselbe
       Dauer-Fehlalarm „Direkt-Patch", nur auf der Browser-Seite. */
    return normalisiereProjektion(projection) ?? projection;
  }

  /* ── Forensik im Browser-Projektor (F-448, Karte #367) ──────────────
   *
   * Warum überhaupt hier: `projectorReconcile` ist der Projektor, der läuft
   * solange ein Tab offen ist — also genau die Lage des F-327-Vorfalls. Bisher
   * kannte nur das CLI die Forensik; der Bleed wurde erst beim nächsten
   * headless-Lauf sichtbar, im Vorfall vom 2026-05-20 wären das Stunden gewesen.
   *
   * Warum nur MELDEN und nicht — wie im CLI — nach `.rejected/` verschieben:
   * Poison und verwaiste Ops sehen ähnlich aus, sind aber grundverschieden.
   * Poison ist ein Urteil über EINE Datei (kaputtes JSON, fremdes Schema) —
   * das kann kein Wettlauf erzeugen, deshalb darf der Browser sie verschieben.
   * „Verwaist" ist dagegen ein Urteil über das VERHÄLTNIS von base, Inbox und
   * Projektion. Kompaktiert das CLI genau zwischen unserem base-Read und dem
   * Inbox-Read, sieht der Browser eine alte base neben einer schon geprunten
   * Inbox — und völlig legitime Ops sähen verwaist aus. Sie dann zu
   * quarantänisieren wäre echter Datenverlust aus einem Wettlauf heraus.
   * Also gilt hier dieselbe Arbeitsteilung wie bei der Kompaktierung
   * (ADR-0071 Achse 2D): der Browser beobachtet, das CLI fasst an.
   *
   * Gegen exakt diesen Wettlauf schützt zusätzlich der Signatur-Vergleich:
   * hat sich base.json zwischen Anfang und Ende dieses Laufs geändert, wird
   * gar nichts gemeldet. Ein Fehlalarm ist bei einem Forensik-Feature der
   * teuerste Fehler — wer die Warnung grundlos sieht, klickt sie weg, und der
   * echte Vorfall geht unter (F-446-Lehre).
   */
  async function reportOrphanOps(base, projection, ops) {
    if (!IS_PROJECTOR_BUILD) return;
    let orphans;
    try { orphans = findOrphanOps(base, projection, ops); }
    catch { return; }                        /* Diagnose darf nie den Poll kippen */
    if (!orphans.length) return;             /* Normalfall: kein Snapshot-Read */

    /* Die Wettlauf-Probe liegt seit F-457 im Aufrufer und schützt dort den
       Schreibpfad mit; wer hier ankommt, hat ein konsistentes base/Inbox-Paar. */

    /* ── F-459: jede Waise genau EINMAL pro Sitzung melden ──────────────
     *
     * Der Browser meldet Waisen, verschiebt sie aber bewusst nicht (Karte
     * #367, Begründung oben) — nur das CLI fasst an. Folge ohne Merkliste:
     * dieselbe Waise wird bei JEDEM 15-Sekunden-Poll neu gefunden und
     * geloggt, bis ein headless-Lauf kommt. Bei einem echten Bleed mit 200
     * Ops sind das 800 `console.warn` pro Minute — ausgerechnet im Ernstfall
     * wird die Konsole unlesbar, und der Betreiber sucht die eine Zeile, die
     * ihm sagt, WELCHE Karte betroffen ist, in tausenden Wiederholungen.
     *
     * Die Merkliste hält Op-IDs, nicht Karten-IDs: eine zweite Op auf
     * dieselbe verwaiste Karte ist ein neuer Vorfall und wird gemeldet.
     * Sitzungslokal und bewusst ohne Verfall — beim Board-Wechsel geleert
     * (siehe _reset), sonst würde die Unterdrückung ein fremdes Board
     * verschlucken. */
    const neue = orphans.filter(({ op, cardId }) =>
      !_gemeldeteWaisen.has(waisenSchluessel(op, cardId)));
    if (!neue.length) return;                /* alles schon gemeldet — auch KEIN Verzeichnis-Listing */

    /* Erst JETZT die Historie beschaffen — sie kostet Datei-Reads und wird
       im Normalfall (keine Waisen) nie gebraucht. Ohne Historie wäre jede
       Meldung `unbekannt` und die Fehlalarme verdoppelten sich (F-446).
       Seit F-459 steht der Merklisten-Filter DAVOR: bei unveränderter
       Waisen-Menge entfällt damit auch das `dirHandle.entries()`-Listing,
       das der alte Ablauf pro Poll ausgelöst hat (der Historie-Cache griff
       erst NACH dem Listing). */
    const history = await historyCardIds();
    for (const { op, cardId } of neue) {
      _gemeldeteWaisen.add(waisenSchluessel(op, cardId));
      const spaet = history && history.has(cardId);
      const wo = op && op.type ? op.type : '?';
      if (spaet) {
        console.info(`[aBoard] Spätläufer-Op ${wo} auf Karte ${cardId} — die Karte`
          + ' gab es hier einmal, die Op kommt zu spät. Normalfall bei parallelen'
          + ' Tabs; der nächste headless-Projektor-Lauf sortiert sie aus.');
      } else if (history && deuteWaise({ op, kind: 'unbekannt' }) === WAISEN_DEUTUNG.GELOESCHT_VOR_BASE) {
        /* F-595: Ein `card.delete` ins Leere ist der Fingerabdruck eines
           ZWEITEN Löschversuchs auf eine Karte, die schon vor dem base-Stand
           weg war — die Spätläufer-Erkennung greift dort nicht, weil die
           Historie erst beim base beginnt. Beim Aufräumen eines Konsumenten-Projekts kostete
           die pauschale Bleed-Meldung für genau diesen Fall eine
           Forensik-Runde. Beide Deutungen nennen, mit Rangfolge. */
        console.info(`[aBoard] Op ${wo} auf die unbekannte Karte ${cardId} — wahrscheinlich`
          + ' ein Doppel-Delete: die Karte war schon vor dem base-Stand gelöscht.'
          + ' Kein Datenverlust (die Op wollte ohnehin löschen). Ein Cross-Board-Bleed'
          + ' (F-327) ist nicht ausgeschlossen, aber unwahrscheinlicher — wer fremde'
          + ' Karten hier hineinschreibt, verschiebt und kommentiert sie meist auch.');
      } else if (history) {
        console.warn(`[aBoard] VERWAISTE Op ${wo} auf Karte ${cardId} — diese Karte`
          + ' gab es auf diesem Board nie. Das ist der F-327-Fingerabdruck:'
          + ' prüfen, ob ein fremdes Board in dieses Verzeichnis schreibt.');
      } else {
        /* Keine rotierten Snapshots vorhanden → die Unterscheidung ist mangels
           Historie nicht möglich. Entsprechend vorsichtig formulieren, statt
           einen Bleed zu behaupten, den wir nicht belegen können. */
        console.warn(`[aBoard] Op ${wo} zielt auf die unbekannte Karte ${cardId}.`
          + ' Ohne rotierte base-Snapshots ist hier nicht unterscheidbar, ob die'
          + ' Karte einmal existierte (harmlos) oder nie (F-327). Der nächste'
          + ' headless-Projektor-Lauf klassifiziert es.');
      }
    }
  }

  /* F-459: Op-IDs (ersatzweise Typ+Karte), die diese Sitzung schon gemeldet
     hat. Kein LRU, kein Verfall — die Menge ist durch die Inbox begrenzt und
     wird beim Board-Wechsel geleert. */
  const _gemeldeteWaisen = new Set();
  /* F-831: dasselbe Prinzip für die Zeitstempel-Wächter — der Browser faltet
     alle 15 Sekunden die volle Inbox neu; ohne diese Menge stünde dieselbe
     Warnung viermal pro Minute in der Konsole, bis jemand sie wegklickt. */
  const _gemeldeteZeitstempel = new Set();

  /* F-831/F-826: die beiden Zeitstempel-Wächter aus src/inbox.js, im
     Browser-Projektor. Ein Treffer wird je Op-ID genau einmal pro Sitzung
     gemeldet. Kein Ablehnen (F-700-Begründung: eine falsch gehende Uhr darf
     keinen Datenverlust erzeugen), keine Wirkung auf `fold`. */
  function meldeZeitstempelAuffaelligkeiten(ops) {
    if (!IS_PROJECTOR_BUILD) return;                  /* ADR-0072: DCE im slim Build */
    const jetzt = Date.now();
    /* Der Schlüssel trägt den GRUND, nicht nur die Op-ID (Kritiker-Lauf
       Sprint 61): Eine Op mit `ts: '2027'` trifft beide Wächter — `Date.parse`
       ist nachsichtig genug, sie als Zukunft zu lesen, und die Regex lehnt sie
       zu Recht als Nicht-ISO ab. Mit einer gemeinsamen Menge trug die zuerst
       laufende Schleife die ID ein und unterdrückte die zweite Meldung: Der
       Leser bekam „Uhr des schreibenden Systems prüfen", obwohl der Defekt das
       Format ist und `makeOp` die Abhilfe. */
    for (const { op, aheadMs } of findFutureOps(ops, jetzt)) {
      if (!op || _gemeldeteZeitstempel.has(`zukunft:${op.id}`)) continue;
      _gemeldeteZeitstempel.add(`zukunft:${op.id}`);
      console.warn(`[aBoard] Op mit Zeitstempel in der ZUKUNFT: ${opFileName(op)}`
        + ` (${op.type}, ${(aheadMs / 3_600_000).toFixed(1)} h voraus,`
        + ` Aktor: ${op.actor || '?'}; Toleranz`
        + ` ${Math.round(FUTURE_TS_TOLERANCE_MS / 60000)} min). Sie sortiert HINTER`
        + ' jede korrekt datierte Op und verliert damit ihre eigene displayId'
        + ' an die nächste (F-700/F-826). Uhr des schreibenden Systems prüfen.');
    }
    /* F-1041: die Zukunfts-Op, die eine zweite Op derselben Karte trifft UND
       denselben Teil der Karte schreibt — OHNE Toleranz, weil dort der `ts`
       entscheidet, welcher INHALT überlebt. Seit F-1101/F-1110 eingegrenzt auf
       Paarungen, bei denen die Reihenfolge tatsächlich den Inhalt entscheidet
       (`card.field columnId` gegen `card.move`, `card.field comments` gegen
       jede Kommentar-Op — aber NICHT zwei comment.add derselben commentId,
       Begründung bei `findFutureKollisionen` in src/inbox.js). CLI-Gegenstück in
       tools/aboard-projektor.mjs (§27: beide Projektoren). Eigener
       Schlüssel-Präfix: eine Op kann zugleich unter `zukunft:` (mehr als
       5 min voraus) und hier auftauchen, und das sind zwei Befunde. */
    for (const { op, aheadMs, cardId, andere } of findFutureKollisionen(ops, jetzt)) {
      if (!op || _gemeldeteZeitstempel.has(`kollision:${op.id}`)) continue;
      _gemeldeteZeitstempel.add(`kollision:${op.id}`);
      console.warn(`[aBoard] Op mit Zeitstempel in der ZUKUNFT trifft die Karte ${cardId},`
        + ` auf die noch ${andere.length} weitere Op(s) mit demselben Ziel (Feld,`
        + ` Platzierung, Kommentar oder die ganze Karte) zielen: ${opFileName(op)}`
        + ` (${op.type}, ${Math.ceil(aheadMs / 1000)} s voraus, Aktor: ${op.actor || '?'}).`
        + ' Beim Falten gewinnt die mit dem späteren Zeitstempel — also die vordatierte,'
        + ' auch wenn sie die überholte Fassung ist (F-1041/F-1101). Die überholte Op'
        + ' entfernen, bevor ihr Zeitstempel erreicht ist.');
    }
    for (const { op, grund, hinterMs } of findImplausibleTs(ops, jetzt)) {
      if (!op || _gemeldeteZeitstempel.has(`ts:${op.id}`)) continue;
      _gemeldeteZeitstempel.add(`ts:${op.id}`);
      const detail = grund === 'nicht-iso'
        ? `ts=${JSON.stringify(op.ts)} ist kein ISO-8601 und sortiert rein lexikografisch`
        : `${(hinterMs / 86_400_000).toFixed(0)} Tage zurückdatiert — sie sortiert VOR`
          + ' bestehende Ops und nimmt einer bestehenden Karte ihre displayId';
      console.warn(`[aBoard] Op mit unplausiblem Zeitstempel: ${opFileName(op)}`
        + ` (${op.type}, ${detail}, Aktor: ${op.actor || '?'}) — F-826.`);
    }
  }

  /* F-831-Schwester für F-1014: Ops, die älter sind als der letzte
     `--rebase-from-projection`. Ihre Wirkung steckt bereits in der neuen base
     (die ja die bisherige Projektion war) — sie wirken hier ein zweites Mal,
     auf einen weiterentwickelten Stand, und der Zugehörigkeits-Riegel sieht
     das nicht (er vergleicht MENGEN, es verschwindet keine Karte).

     Melden, nie ablehnen — F-700-Begründung, `fold` bleibt unberührt. Je Op
     einmal pro Sitzung, sonst stünde die Warnung viermal pro Minute da.

     Die Marke ist ein Dotfile in der Inbox und wird vom Einlesen oben
     übersprungen; sie muss deshalb eigens gelesen werden. Schlägt das fehl,
     schweigt der Wächter — eine fehlende Marke ist der Normalfall (es gab
     keinen Rebase), und ein Lesefehler darf den Poll nicht abreissen. */
  async function meldeOpsVorRebaseMarke(ops) {
    if (!IS_PROJECTOR_BUILD) return;                  /* ADR-0072: DCE im slim Build */
    if (!Array.isArray(ops) || ops.length === 0) return;
    let marke = null;
    try {
      const inbox = await getInboxDir(false);
      if (!inbox) return;
      const h = await inbox.getFileHandle(REBASE_MARKE);
      marke = JSON.parse(await (await h.getFile()).text());
    } catch { return; }                               /* keine Marke = kein Rebase */
    const treffer = findOpsVorMarke(ops, marke && marke.ts);
    if (!treffer) return;
    for (const b of treffer.betroffen) {
      const schluessel = `rebase:${(b.op && b.op.id) || b.ts}`;
      if (_gemeldeteZeitstempel.has(schluessel)) continue;
      _gemeldeteZeitstempel.add(schluessel);
      console.warn(`[aBoard] Op ist ÄLTER als der letzte --rebase-from-projection`
        + ` (${treffer.markeTs}): ${opFileName(b.op)} (${b.type}`
        + `${b.cardId ? `, Karte ${b.cardId}` : ''}). Ihre Wirkung steckt bereits`
        + ' im Basis-Snapshot — sie wirkt jetzt ein ZWEITES Mal und kann einen'
        + ' jüngeren Kartenstand überschreiben (F-1014). Ist der Rebase'
        + ` verarbeitet, kann ${REBASE_MARKE} gelöscht werden.`);
    }
    /* Rückmeldung für den Fold-Marken-Wächter unten: die Ops, die hier schon
       gemeldet sind — er schweigt NUR für diese, nicht für den ganzen Stapel
       (Sprint-67-Kritiker: mit alter Rebase-Marke T1 < Fold-Marke T2 blieb
       eine Op zwischen T1 und T2 sonst stumm, sobald eine vor T1 lag). */
    return new Set(treffer.betroffen.map((b) => b.op));
  }
  /* F-1014 (offener Rest, Karte #512): derselbe Wächter OHNE Rebase-Kontext.
     §27 (knowledge/topics/nebenlaeufigkeit-und-lesefenster.md): Wer eine der
     beiden Projektor-Implementierungen anfasst, zieht die andere nach — bei
     offenem Tab ist DIESER hier der aktive, ein Wächter nur im CLI schwiege
     also genau dann, wenn die meisten Ops gefaltet werden.

     Die Fold-Marke schreibt das CLI bei jeder Kompaktierung; der Browser
     kompaktiert nicht selbst (er legt nur den `maintenance.compact`-Marker an),
     liest sie hier aber genauso. Ops, die der Rebase-Wächter schon gemeldet
     hat, werden übersprungen — zwei Meldungen über dieselbe Op-Datei lesen
     sich wie zwei Befunde. Je Op, nicht je Stapel (Sprint-67-Kritiker). */
  async function meldeOpsVorFoldMarke(ops, schonGemeldet) {
    if (!IS_PROJECTOR_BUILD) return;                  /* ADR-0072: DCE im slim Build */
    if (!Array.isArray(ops) || ops.length === 0) return;
    let marke = null;
    try {
      const inbox = await getInboxDir(false);
      if (!inbox) return;
      const h = await inbox.getFileHandle(FOLD_MARKE);
      marke = JSON.parse(await (await h.getFile()).text());
    } catch { return; }                               /* keine Marke = nie kompaktiert */
    const treffer = findOpsVorMarke(ops, marke && marke.ts);
    if (!treffer) return;
    for (const b of treffer.betroffen) {
      if (schonGemeldet && schonGemeldet.has(b.op)) continue;
      const schluessel = `foldmarke:${(b.op && b.op.id) || b.ts}`;
      if (_gemeldeteZeitstempel.has(schluessel)) continue;
      _gemeldeteZeitstempel.add(schluessel);
      console.warn('[aBoard] Op ist ÄLTER als der Stand, der beim letzten Aufräum-Lauf'
        + ` in den Basis-Snapshot gefaltet wurde (${treffer.markeTs}):`
        + ` ${opFileName(b.op)} (${b.type}${b.cardId ? `, Karte ${b.cardId}` : ''}).`
        + ' Sie wirkt auf einen Stand, der sie bereits enthält, und überschreibt dabei,'
        + ' was seither dazugekommen ist — der Zugehörigkeits-Riegel sieht das nicht,'
        + ' er vergleicht Mengen (F-1014).');
    }
  }

  const waisenSchluessel = (op, cardId) =>
    (op && typeof op.id === 'string' && op.id)
      ? op.id
      : `${(op && op.type) || '?'}:${cardId}`;

  /* Karten-IDs aus den rotierten base-Snapshots — die Historie-Quelle für die
     Spätläufer-Klassifikation. Zwischengespeichert über die Namensliste der
     Snapshots: die ändert sich nur, wenn das CLI kompaktiert hat. */
  let _historyIds = null;
  let _historyKey = '';
  async function historyCardIds() {
    if (!dirHandle) return null;
    let names = [];
    try {
      for await (const [name, h] of dirHandle.entries()) {
        if (h.kind === 'file' && isRotatedBaseName(name)) names.push(name);
      }
    } catch { return null; }
    if (!names.length) return null;          /* keine Historie → Aufrufer formuliert vorsichtig */
    names.sort();
    const key = names.join('|');
    if (_historyIds && _historyKey === key) return _historyIds;
    const ids = new Set();
    for (const name of names) {
      try {
        const txt = await (await (await dirHandle.getFileHandle(name)).getFile()).text();
        const snap = JSON.parse(txt);
        const sprints = (snap && snap.board ? snap.board.sprints : snap && snap.sprints) || [];
        for (const s of sprints) for (const c of (s && s.cards) || []) {
          if (c && typeof c.id === 'string') ids.add(c.id);
        }
      } catch { /* unlesbarer Snapshot — für die Klassifikation unkritisch */ }
    }
    _historyIds = ids;
    _historyKey = key;
    return ids;
  }

  /* Billige Signatur der base.json (mtime+size, kein text()/parse) — dieselbe
     Bildungsregel wie in readBaseSnapshot, damit beide vergleichbar sind. */
  async function baseSignature() {
    try {
      const f = await (await dirHandle.getFileHandle(BASE_FILE)).getFile();
      return f.lastModified + ':' + f.size;
    } catch { return null; }
  }

  async function appendOps(ops) {
    if (!IS_PROJECTOR_BUILD) return;                  /* ADR-0072: DCE im slim Build */
    const inbox = await getInboxDir(true);
    for (const op of ops) {
      await fsaWriteText(inbox, opFileName(op), JSON.stringify(op));
    }
  }

  /* ADR-0106 (#331): Browser-seitiger Mass-Delete-Guard.
     Prüft ob ein berechneter Op-Batch die Lösch-Schwellen überschreitet.
     Gibt { blocked: true } zurück, wenn der User den Dialog abbricht.
     Gibt { blocked: false } zurück, wenn Batch OK oder User bestätigt hat.

     Whitelist (F-359 gehärtet): NUR exakter Aktor 'system' wird durchgelassen.
     'system' ist ausschließlich Code-intern gesetzt (migrateSprintMeta-
     Auto-Aktivierung in src/storage.js, Op-Folder-Sweeps) — kein User-Pfad
     kann diesen Wert produzieren. Die frühere Whitelist über
     `actor.startsWith('Claude ')` war trivial bypassbar: jeder User mit
     Profil-`userName` "Claude Foo" deaktivierte seinen eigenen Schutz.
     Im Browser-Pfad gibt es keinen legitimen Massendelete eines Claude-
     Aktors — Claude-Modell-Ops werden außerhalb des Browsers per
     /tmp/*.mjs-Skript in die Inbox geschrieben und umgehen den Guard
     ohnehin (Browser-only). */
  async function massDeleteGuard(ops, actor) {
    if (!IS_PROJECTOR_BUILD) return { blocked: false };

    /* Whitelist: ausschließlich Code-interne 'system'-Ops */
    if (actor === 'system') {
      return { blocked: false };
    }

    const cardDeletes     = ops.filter(o => o.type === 'card.delete').length;
    const swimlaneDeletes = ops.filter(o => o.type === 'swimlane.delete').length;

    const exceedsCard     = cardDeletes     > MASS_DELETE_CARD_THRESHOLD;
    const exceedsSwimlane = swimlaneDeletes > MASS_DELETE_SWIMLANE_THRESHOLD;

    if (!exceedsCard && !exceedsSwimlane) return { blocked: false };

    /* Modaler Bestätigungs-Dialog — gibt Promise<boolean> zurück */
    return new Promise(resolve => {
      /* Overlay */
      const overlay = document.createElement('div');
      overlay.className = 'modal-overlay mass-delete-guard-overlay';
      overlay.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,.55);z-index:9999;display:flex;align-items:center;justify-content:center;';

      const box = document.createElement('div');
      box.style.cssText = 'background:var(--bg);color:var(--text);padding:1.5rem 2rem;border-radius:.5rem;max-width:480px;width:90%;box-shadow:0 4px 32px rgba(0,0,0,.25);';
      /* #327/F-347/F-363: role/aria-modal/aria-labelledby fehlten komplett —
         dieses Custom-Modal wurde nie wie die übrigen Dialoge (role="dialog"
         etc.) ausgezeichnet. */
      box.setAttribute('role', 'dialog');
      box.setAttribute('aria-modal', 'true');
      box.setAttribute('aria-labelledby', 'mass-delete-guard-heading');

      const heading = document.createElement('h3');
      heading.id = 'mass-delete-guard-heading';
      heading.style.cssText = 'margin:0 0 .75rem;color:#c0392b;font-size:1.1rem;';
      heading.textContent = '⚠️ Massenlöschung erkannt';

      const msg = document.createElement('p');
      msg.style.cssText = 'margin:0 0 1rem;line-height:1.5;';
      const parts = [];
      if (exceedsCard)     parts.push(`${cardDeletes} Karten`);
      if (exceedsSwimlane) parts.push(`${swimlaneDeletes} Swimlanes`);
      msg.textContent = `Du bist dabei, ${parts.join(' und ')} zu löschen. Das ist ungewöhnlich viel — wurde das Board versehentlich überschrieben?`;

      const hint = document.createElement('p');
      hint.style.cssText = 'margin:0 0 1.25rem;font-size:.85rem;color:var(--text-muted);';
      hint.textContent = 'Klicke „Abbrechen", um die Aktion rückgängig zu machen. Nur auf „Trotzdem speichern" klicken, wenn du dir sicher bist.';

      const btnRow = document.createElement('div');
      btnRow.style.cssText = 'display:flex;gap:.75rem;justify-content:flex-end;';

      const cancelBtn = document.createElement('button');
      cancelBtn.className = 'btn btn-primary';
      cancelBtn.textContent = 'Abbrechen (empfohlen)';

      const confirmBtn = document.createElement('button');
      confirmBtn.className = 'btn';
      confirmBtn.textContent = 'Trotzdem speichern';
      confirmBtn.style.cssText = 'color:#c0392b;border-color:#c0392b;';

      /* #327: Fokus-Restore-Ziel merken, BEVOR das Modal den Fokus übernimmt. */
      const prevFocus = captureFocus();

      function cleanup(confirmed) {
        document.removeEventListener('keydown', onModalKey);
        overlay.remove();
        restoreFocus(prevFocus); /* #327: Fokus zurück ans auslösende Element */
        resolve({ blocked: !confirmed });
      }

      cancelBtn.addEventListener('click',  () => cleanup(false));
      confirmBtn.addEventListener('click', () => cleanup(true));
      /* #327: Esc + Tab-Trap global statt nur am Overlay — konsistent mit den
         übrigen Server-Modus-Dialogen (admin/llm-keys/email). */
      function onModalKey(e) {
        if (e.key === 'Escape') { e.preventDefault(); cleanup(false); return; }
        trapTabKey(e, box);
      }
      document.addEventListener('keydown', onModalKey);

      btnRow.appendChild(cancelBtn);
      btnRow.appendChild(confirmBtn);
      box.appendChild(heading);
      box.appendChild(msg);
      box.appendChild(hint);
      box.appendChild(btnRow);
      overlay.appendChild(box);
      document.body.appendChild(overlay);
      /* #327: Initial-Focus — cancelBtn ist ohnehin das erste <button>
         (kein <input> im Dialog), Fallback nur zur Sicherheit. */
      focusInitial(box, cancelBtn);
    });
  }

  async function writeHeartbeat(who) {
    if (!IS_PROJECTOR_BUILD) return;                  /* ADR-0072: DCE im slim Build */
    try {
      const inbox = await getInboxDir(true);
      await fsaWriteText(inbox, HEARTBEAT_LEAF,
        JSON.stringify(makeHeartbeat(who, new Date().toISOString())));
    } catch { /* Heartbeat ist nur Effizienz, nicht korrektheitsrelevant */ }
  }

  function sameProjection(a, b) {
    return JSON.stringify(a) === JSON.stringify(b);
  }

  /* Persist-Grenze (ersetzt den Ganzdatei-Write für claude-code).
     Liefert true bei Erfolg. Bei IO-Fehler false — KEIN Datenverlust:
     bereits angehängte Ops bleiben, der nächste Persist/Poll versöhnt. */
  async function projectorPersist(state, actor) {
    if (!IS_PROJECTOR_BUILD) {
      /* ADR-0072-Backstop: das slim Public-Distributable kennt den Projektor
         nicht. Ein migriertes Board NIEMALS per Legacy-Ganzdatei-`write()`
         anfassen (F-283-Wiederkehr auf der Wahrheit). Rückgabe false → die
         Aufrufer (storage-plugin save/autoPersist, persistGated) schreiben
         bei false NICHT, sondern melden — kein Clobber. Bei migriertem Board
         zusätzlich ein klarer, handlungsleitender Konsolen-Hinweis. */
      if (await isMigrated()) {
        console.error('aBoard: migriertes Board — aboard.html (Projektor-Build) nötig; nicht gespeichert (F-283-Schutz).');
      }
      return false;
    }
    if (!isActive()) return false;
    try {
      if (!lastAdopted) {
        /* Startsicherung: erstmaliger Persist ohne adoptierten Bezug →
           aktuellen Wahrheitsstand als Bezug nehmen. */
        lastAdopted = await projectorReconcile();
        /* F-462 (QA-Lauf nach F-457): NICHT auf den lokalen State zurückfallen.
           Der frühere Fallback stammt aus der Zeit, als `null` nur eines heißen
           konnte — „nicht migriert". Seit F-457 heißt es zusätzlich „Wettlauf
           mit der Kompaktierung, dieser Lauf ist verworfen". Auf einem
           migrierten Board (und nur dort landet dieser Pfad) wäre der lokale
           State als Diff-Bezug fatal: `diffToOps(lokal, lokal)` ist leer, es
           entstünde KEINE Op, und der zweite `projectorReconcile()` weiter
           unten adoptierte die Disk-Wahrheit — die Änderung des Nutzers wäre
           still verschwunden.
           `false` ist hier gefahrlos und richtig: der State bleibt dirty im
           Speicher, der nächste Auto-Save-Tick versucht es erneut, und dann
           ist der Wettlauf vorbei. */
        if (!lastAdopted) return false;
      }
      const ops = diffToOps(lastAdopted, { version: state.version, board: state.board }, actor);
      if (ops.length) {
        /* ADR-0106 (#331): Mass-Delete-Guard vor dem Inbox-Append.
           Nur im Projektor-Build (IS_PROJECTOR_BUILD), kein DOM-Zugriff im slim Build. */
        const guard = await massDeleteGuard(ops, actor);
        if (guard.blocked) return false;   /* User hat Abbrechen gewählt — kein Datenverlust */
        /* Diagnose-Marker voranstellen wenn der Guard eine hohe Löschzahl durchgelassen hat */
        const cardDeletes = ops.filter(o => o.type === 'card.delete').length;
        const slDeletes   = ops.filter(o => o.type === 'swimlane.delete').length;
        if (cardDeletes > MASS_DELETE_CARD_THRESHOLD || slDeletes > MASS_DELETE_SWIMLANE_THRESHOLD) {
          await appendOps([makeOp('maintenance.diagnostic',
            { reason: 'mass-delete-guard-bestätigt', cardDeletes, swimlaneDeletes: slDeletes },
            actor)]);
        }
        await appendOps(ops);
      }
      const proj = await projectorReconcile();
      if (!proj) return false;
      const projText = JSON.stringify(proj, null, 2) + '\n';
      await fsaWriteText(dirHandle, PROJECTION_FILE, projText);
      await schreibeLesedatei(projText, proj.board);
      await writeHeartbeat('browser');
      lastAdopted = proj;
      /* Versöhnung: weicht die materialisierte Projektion vom optimistisch
         angezeigten Stand ab (projektor-vergebene displayId neuer Karten
         ODER zwischenzeitlich gefaltete externe Ops) → adoptieren. Die
         applyExternalState-Maschinerie (Modal-Guard, dirty-clean,
         Echo-PUT-Schutz) wird via onUpdate wiederverwendet; sie ist
         No-Op bei Gleichheit. */
      if (!sameProjection(proj, { version: state.version, board: state.board })) {
        onUpdate?.(klonBezug(proj));       /* F-814: nie das Bezugsobjekt selbst herausgeben */
      }
      return true;
    } catch { return false; }
  }

  /* F-294 (ADR-0067 Stufe 2): migrierte-Board-sicherer Ganzdaten-Persist
     für die user-initiierten main.js-Direktpfade (board:save-Konflikt
     „Meine Version", board:switch-Flush, Initial-Load-Reconcile). Auf
     einem migrierten Board würde ein roher write() base+Inbox umgehen und
     die F-283-Bug-Klasse zurückbringen (Browser-Memory clobbert die
     Projektion) — exakt das F-291-Gate-Muster wie im storage-plugin.
       migriert  → projectorPersist (Diff→Ops→Inbox→Projektion); true|false
       nicht mig.→ Legacy-write, unveränderter ADR-0050-Vertrag
                   (force/Konflikt-Rückgabe {conflict,diskState,diskRaw})
     board:reset (= base-Reset, neue board.id) gehört bewusst NICHT
     hierher — ein projectorPersist erzeugte dort einen Lösch-Diff gegen
     ein fremdes Board. Das ist eine 3-K-Entscheidung der Stufe-2-
     Kompaktierungs-ADR; main.js stoppt board:reset auf migrierten Boards
     bis dahin hart. */
  /* Sprint-51-Kritiker (K-B): Waehrend einer laufenden Ordnerwahl zeigt
     `dirHandle` bereits auf das fremde Verzeichnis, die Entscheidung faellt aber
     erst spaeter. Das F-468-Gate hielt nur `runTick` an — der Kommentar dort
     versprach jedoch, es duerfe „nichts" schreiben. Der 500-ms-`debouncedPersist`
     konnte in dieses Fenster fallen und Ops in die Inbox des FREMDEN Projekts
     haengen: exakt der Schaden, den F-468 beschreibt. Deshalb gilt das Gate jetzt
     auch fuer den Schreibpfad. Rueckgabe `false` heisst fuer den Aufrufer „nicht
     geschrieben, State bleibt dirty" — der naechste Auto-Save-Tick versucht es
     erneut, dann mit entschiedenem Handle. */
  function pickLaeuft() { return pickInProgress; }

  async function persistGated(state, opts = {}) {
    if (pickInProgress) return false;
    if (await isMigrated()) {
      return (await projectorPersist(state, opts.actor || '')) === true;
    }
    return await write(state, opts);
  }

  /* ── Kompaktierung (ADR-0071 Achse 2D, #230 Task 5) ───────────────── */

  /* Synchroner Getter der zuletzt gefalteten Inbox-Op-Anzahl. Der Renderer
     liest das während render() (wie board.awaitingUser) und zeigt ab
     COMPACT_OP_THRESHOLD ein rein informatives Banner. Aktualisiert bei
     jedem projectorReconcile (Persist + 15s-Poll) → kein eigener FSA-Read
     im Renderpfad, kein Server-Modus-Bezug (dort bleibt der Wert 0). */
  function getInboxOpCount() { return IS_PROJECTOR_BUILD ? _inboxOpCount : 0; }

  /* Karte #337 — Inbox-Bloat-Heuristik.
     Liefert { bloat: boolean, daysSinceBase: number, oldestOpDays: number }
     oder null (nicht migriert / noch kein Reconcile). Schwellen:
       - Op-Count > BLOAT_OP_THRESHOLD (50)
       - AND base-mtime ODER älteste Op > BLOAT_HOURS_THRESHOLD Stunden alt
     Der Renderer wertet das aus und zeigt ein zusaetzliches Warning-Banner.
     Synchron, kein FSA-Zugriff im Renderpfad. */
  const BLOAT_OP_THRESHOLD    = 50;
  const BLOAT_HOURS_THRESHOLD = 48;
  function getInboxBloatInfo() {
    if (!IS_PROJECTOR_BUILD) return null;
    if (_inboxOpCount === 0 && _baseMtimeMs === 0) return null;    /* noch kein Reconcile */
    const nowMs = Date.now();
    const baseDays = _baseMtimeMs > 0
      ? (nowMs - _baseMtimeMs) / 86_400_000 : 0;
    const oldestOpDays = _inboxOldestOpMs > 0
      ? (nowMs - _inboxOldestOpMs) / 86_400_000 : 0;
    /* Bloat: Inbox voll UND (base veraltet ODER älteste Op veraltet) */
    const tooManyOps  = _inboxOpCount > BLOAT_OP_THRESHOLD;
    const staleBase   = baseDays   > BLOAT_HOURS_THRESHOLD / 24;
    const staleOldest = oldestOpDays > BLOAT_HOURS_THRESHOLD / 24;
    const bloat = tooManyOps && (staleBase || staleOldest);
    return { bloat, daysSinceBase: baseDays, oldestOpDays };
  }

  /* Banner-Button-Aktion: hängt AUSSCHLIESSLICH einen append-only
     `maintenance.compact`-Marker an die Inbox an — der Browser fasst
     `aboard-sync.base.json` NIE an (ADR-0071-Invariante, sonst F-283/F-294-
     Bug-Klasse auf der Wahrheit). Die eigentliche Kompaktierung/Rotation
     macht ausschließlich der headless-Projektor, sobald er den Marker sieht
     (shouldCompact). Bewusst KEIN projectorPersist (kein State-Diff, keine
     Projektion-Schreibung). Nur auf aktiven, migrierten Boards wirksam;
     sonst No-Op (false). Idempotent: mehrere Marker sind harmlos
     (board-neutraler fold + Marker werden bei der Kompaktierung selbst
     mit gelöscht). */
  async function requestCompaction(actor) {
    if (!IS_PROJECTOR_BUILD)    return false;          /* ADR-0072: DCE im slim Build */
    if (!isActive())            return false;
    if (!(await isMigrated()))  return false;
    try {
      await appendOps([makeOp('maintenance.compact', {}, actor || '')]);
      return true;
    } catch { return false; }
  }

  /* ADR-0130 §1 (Karte #606): Ops anhängen, die kein State-Diff erzeugt —
     `diffToOps` überträgt `claim` nie, „Übernahme aufheben“ braucht deshalb
     einen eigenen Weg in die Inbox. Danach sofort einen Tick, damit die Karte
     nicht bis zum nächsten Intervall übernommen aussieht. `false`, wenn das
     Board kein migriertes, aktives Projektor-Board ist oder das Schreiben
     scheitert — der Aufrufer meldet es, statt Erfolg anzuzeigen (§41). */
  /* Karte #618: `vorbedingung(projektion)` prüft vor dem Anhängen gegen den
     frisch gefalteten Stand; liefert sie etwas anderes als null, wird nichts
     angehängt und genau das zurückgegeben. */
  async function haengeOpsAn(ops, vorbedingung) {
    if (!IS_PROJECTOR_BUILD)    return false;          /* ADR-0072: DCE im slim Build */
    if (!isActive())            return false;
    if (!(await isMigrated()))  return false;
    if (typeof vorbedingung === 'function') {
      let einwand = null;
      try {
        const projektion = await projectorReconcile();
        if (!projektion) return false;
        einwand = vorbedingung(projektion);
      } catch { return false; }
      if (einwand != null) return einwand;
    }
    try { await appendOps(ops); } catch { return false; }
    runTick();
    return true;
  }

  /* ── Polling ────────────────────────────────────────────────────── */

  /* Legacy-Ganzdatei-Poll — NUR noch für sync.test.js / Alt-Pfade
     direkt aufrufbar; NICHT mehr im Intervall (ADR-0067). */
  async function poll() {
    const raw = await read();
    if (!raw || raw === lastHash) return;
    try {
      const data = JSON.parse(raw);
      lastHash = raw;
      onUpdate?.(data);
    } catch { /* kaputtes JSON ignorieren */ }
  }

  /* ADR-0067-Poll: der Browser ist (offen = ) der aktive Projektor.
     base+Inbox falten, Projektion materialisieren + Heartbeat, und bei
     Abweichung vom adoptierten Stand (externe Ops von Sub-Agents/CLI/
     anderen) den neuen Stand adoptieren. */
  async function projectorPoll() {
    if (!IS_PROJECTOR_BUILD) return;                  /* ADR-0072: DCE im slim Build */
    if (!isActive()) return;
    try {
      /* ADR-0070 §3 (F-286): ausstehenden lokalen non-modal Edit ALS OPS
         sichern, BEVOR wir falten/adoptieren — gediff't gegen das AKTUELLE
         lastAdopted (alter Bezug, bevor der Poll ihn bewegt). Die folgende
         Reconcile-Faltung enthält damit externen Op UND lokalen Edit; der
         anschließende Adopt clobbert nichts mehr (Konvergenz, ADR-0067-Modell:
         Edit → semantische Ops → fold). Modal-Pfad bleibt der pendingState-
         Puffer (unverändert). Fehler beim Flush dürfen den Poll nicht killen,
         aber wir adoptieren dann NICHT (sonst doch Clobber) → früher Return. */
      if (localStateProvider && lastAdopted) {
        const lp = localStateProvider();
        if (lp && lp.dirty && lp.state && lp.state.board) {
          const ops = diffToOps(
            lastAdopted,
            { version: lp.state.version, board: lp.state.board },
            lp.actor || '');
          if (ops.length) {
            /* ADR-0106 (#331): Mass-Delete-Guard auch im Poll-Flush-Pfad.
               Bei Guard-Block: Return ohne Adopt → der ausstehende Edit bleibt
               im Speicher, kein Clobber (nächster Poll versucht es erneut). */
            const guard = await massDeleteGuard(ops, lp.actor || '');
            if (guard.blocked) return;
            await appendOps(ops);
          }
        }
      }
      const proj = await projectorReconcile();
      if (!proj) return;
      /* Karte #349 / ADR-0067 Self-Heal: Drift- und Stall-Detection vor dem
         Write. Vergleicht die Disk-Projektion mit fold(base+inbox); meldet
         Anomalien in der Konsole. Der nachfolgende Write überschreibt jede
         Anomalie autoritativ (fold ist die einzige Wahrheit, ADR-0067 §2). */
      try {
        const diskRaw = await fsaReadText(dirHandle, PROJECTION_FILE);
        if (diskRaw != null) {
          const onDisk = JSON.parse(diskRaw);
          /* `baseSnapshot` ist genau die base, gegen die projectorReconcile
             soeben gefaltet hat (der Cache wird erst beim nächsten
             readBaseSnapshot ersetzt). Ohne dieses vierte Argument meldete
             JEDER 15-Sekunden-Poll „Stall", solange überhaupt Ops in der
             Inbox lagen — gefaltete Ops bleiben bis zur Kompaktierung liegen.
             Begründung an detectProjectionAnomaly (src/inbox.js). */
          /* ADR-0127: beide Vergleichsseiten auf dieselbe Normalisierungsstufe.
             `proj` ist normalisiert (siehe projectorReconcile), `onDisk` und
             `baseSnapshot` kommen roh von der Platte bzw. aus dem Cache. Roh
             gegen normalisiert verglichen meldet der eine Zweig dauerhaft
             „Direkt-Patch" und der andere schaltet die Stall-Erkennung ab. */
          const anomaly = detectProjectionAnomaly(
            normalisiereProjektion(onDisk) ?? onDisk,
            proj,
            _inboxOpCount,
            normalisiereProjektion(baseSnapshot) ?? baseSnapshot);
          if (anomaly?.type === 'drift-without-ops') {
            console.warn('aBoard: Projektion-Drift ohne Inbox-Ops — möglicher Direkt-Patch auf aboard-sync.json (ADR-0067-Verstoß). Wird mit fold(base) überschrieben.');
          } else if (anomaly?.type === 'stall') {
            console.warn(`aBoard: Projektion-Stall — ${anomaly.opCount} Op(s) in Inbox bewirken gegenüber der base nichts (gelöschte Ziele, Idempotenz oder Stall-Symptom); nächster Poll-Tick versöhnt.`);
          }
        }
      } catch (e) {
        /* Anomalie-Check ist nur Diagnose; Fehler darf den Poll nicht killen.
           Fehler aber NICHT still schlucken (F-404): genau ein kaputtes
           aboard-sync.json (unvollständiger git checkout etc.) ist die
           Klasse Vorfall, vor der die Detection warnen soll. */
        console.warn('aBoard: Projektion-Anomalie-Check übersprungen:', e?.message || e);
      }
      const projText = JSON.stringify(proj, null, 2) + '\n';
      await fsaWriteText(dirHandle, PROJECTION_FILE, projText);
      await schreibeLesedatei(projText, proj.board);
      await writeHeartbeat('browser');
      if (!lastAdopted || !sameProjection(proj, lastAdopted)) {
        lastAdopted = proj;
        onUpdate?.(klonBezug(proj));       /* F-814: nie das Bezugsobjekt selbst herausgeben */
      }
      /* Chat-Tail-Tick (Karte #290) am SELBEN Polling-Intervall — kein eigener
         Timer, kein eigener Heartbeat-Effekt. Lesefehler dürfen den Poll nicht
         killen, daher .catch — readChatTail ist intern bereits tolerant. */
      if (chatTailEnabled) await readChatTail().catch(() => {});
    } catch { /* IO transient — nächster Tick versöhnt */ }
  }

  /* ── Chat-Tail (Karte #290, claude-code-only) ──────────────────────
     Marker-gated auf Hook-Seite (aboard-sync.chat-tail.enabled) und in
     diesem Modul zusätzlich per `chatTailEnabled`-Flag, das Settings setzt.
     Funktionsrümpfe hinter IS_PROJECTOR_BUILD → DCE im slim. */

  const CHAT_TAIL_FILE   = 'aboard-sync.chat-tail.jsonl';
  const CHAT_TAIL_MARKER = 'aboard-sync.chat-tail.enabled';

  function getChatTail() { return chatTailEntries.slice(); }
  function setChatTailListener(fn) { chatTailListener = (typeof fn === 'function') ? fn : null; }

  /* Settings-Toggle ruft das auf: enabled=true legt Marker an + startet
     Tail-Lesung. enabled=false löscht Marker (Hook ist dann no-op) +
     leert den lokalen State. Lines wandert als JSON `{"lines": N}` in
     den Marker — der Hook respektiert das beim Append/Rotate. */
  async function setChatTailEnabled(enabled, lines) {
    if (!IS_PROJECTOR_BUILD) return;
    if (!isActive()) { chatTailEnabled = !!enabled; return; }
    try {
      if (enabled) {
        const payload = JSON.stringify({ lines: Number(lines) || 20 });
        await fsaWriteText(dirHandle, CHAT_TAIL_MARKER, payload);
        chatTailEnabled = true;
        await readChatTail().catch(() => {});
      } else {
        try { await dirHandle.removeEntry(CHAT_TAIL_MARKER); } catch { /* fehlt schon */ }
        chatTailEnabled = false;
        chatTailEntries = [];
        chatTailLastHash = '';
        chatTailListener?.([]);
      }
    } catch { /* FSA transient — beim nächsten Toggle erneut versuchen */ }
  }

  async function readChatTail() {
    if (!IS_PROJECTOR_BUILD) return;
    if (!isActive()) return;
    const raw = await fsaReadText(dirHandle, CHAT_TAIL_FILE);
    if (raw === null) {
      /* Datei fehlt = noch kein Hook-Append → leerer Tail. Bei vorherigem
         Inhalt aber leerer Wechsel-Lesung trotzdem Listener feuern, damit
         der Footer das „leer"-Update erfasst. */
      if (chatTailLastHash !== '') {
        chatTailLastHash = '';
        chatTailEntries  = [];
        chatTailListener?.([]);
      }
      return;
    }
    if (raw === chatTailLastHash) return;
    chatTailLastHash = raw;
    const next = [];
    for (const line of raw.split('\n')) {
      const s = line.trim();
      if (!s) continue;
      try {
        const obj = JSON.parse(s);
        if (obj && typeof obj === 'object' && typeof obj.kind === 'string') next.push(obj);
      } catch { /* poisoned line — überspringen */ }
    }
    chatTailEntries = next;
    chatTailListener?.(chatTailEntries.slice());
  }

  /* Markiert den Stand, den der Browser jetzt anzeigt, als Diff-Bezug.
     Vom Aufrufer (main.js) nach der initialen State-Adoption zu rufen,
     damit der erste Diff nicht gegen einen leeren Bezug läuft. */
  function markAdopted(state) {
    /* F-814: geklont ablegen. `state.board` ist der LEBENDE App-State-Baum;
       als Bezug abgelegt, wandert jede spätere In-place-Mutation daran (die
       Storage-Migrationen tun genau das) lautlos in den Diff-Bezug und macht
       sich selbst unsichtbar. */
    if (state && state.board) lastAdopted = klonBezug({ version: state.version, board: state.board });
  }

  /* ADR-0070 §2 (F-291): Intervall-Tick je nach Migrations-Status —
     migriert → projectorPoll (ADR-0067); nicht migriert → Legacy-Ganzdatei-
     poll() (Pre-#228, ADR-0050-Bezug via lastHash). Die isMigrated()-Probe ist
     async; pollGen schützt gegen ein stopPolling()/Re-start, das vor Auflösung
     der Probe feuert (sonst Intervall-Leak/falscher Tick). */
  function startPolling(callback) {
    clearInterval(pollTimer); /* Guard gegen Doppel-Aufruf (F-80) */
    pollTimer  = null;
    onUpdate   = callback;
    const myGen = ++pollGen;
    (async () => {
      let tick;
      try { tick = (await isMigrated()) ? projectorPoll : poll; }
      catch { tick = poll; }
      if (myGen !== pollGen) return;          /* zwischenzeitlich gestoppt/neu */
      activeTick = tick;
      bindVisibilityTrigger();                /* F-405/F-456 */
      /* Sofort einmal falten/adoptieren — schließt das 0–15-s-Fenster, in
         dem Startup-Reads noch nicht gefaltete Inbox-Ops verpassen würden. */
      await runTick();
      if (myGen !== pollGen) return;
      pollTimer = setInterval(() => { runTick(); }, POLL_MS);
    })();
  }

  function stopPolling() {
    clearInterval(pollTimer);
    pollTimer = null;
    onUpdate  = null;
    pollGen++;                                /* laufende async Probe entwerten */
    activeTick = null;
    /* Sprint-51-Kritiker (K4): auch das In-Flight-Flag lösen. Bleibt es nach
       einem Stopp während eines laufenden Ticks auf true stehen, fällt das
       sofortige runTick() des nächsten startPolling lautlos aus — und settlet
       die alte Tick-Promise nie (hängender FSA-Call, offener confirm), wäre der
       Projektor dieses Tabs dauerhaft tot, ohne sichtbaren Fehler. Der alte
       Lauf kann nichts mehr kaputtmachen: activeTick ist null, pollGen erhöht. */
    tickInFlight = false;
    /* Sprint-51-Kritiker (K-A): dasselbe fuer das Pick-Gate. Es hatte strikt
       weniger Ausstiege als sein Zwilling `tickInFlight` — geraeumt wurde es nur
       von clearActiveHandle und dem Test-Reset. Jede Ausnahme zwischen
       `pickInProgress = true` und commit/revert (der pick-Handler in main.js ist
       async) haette das Gate dauerhaft zugelassen: runTick liefert dann bis zum
       Tab-Reload nur noch Promise.resolve(), der Projektor des Tabs ist still
       tot. Genau die Failure-Klasse, die F-469 fuer tickInFlight geschlossen hat. */
    pickInProgress = false;
    unbindVisibilityTrigger();
  }

  /* Ein Tick-Lauf mit Überlappungs-Schutz. Beide Auslöser (Intervall und
     Sichtbarkeitswechsel) gehen hier durch; ein zweiter Aufruf während eines
     laufenden Ticks fällt lautlos aus — der laufende Tick liefert ohnehin
     denselben Stand. (F-480: NICHT, weil ein paralleler Lauf sich per
     F-457-Wächter selbst entwertete — der Wächter vergleicht gegen die
     Platte, nicht gegen den anderen Lauf. Siehe die ausführliche Begründung
     an `tickInFlight` oben.) */
  function runTick() {
    if (!activeTick || tickInFlight) return Promise.resolve();
    if (pickInProgress) return Promise.resolve();      /* K2: Ordnerwahl läuft */
    tickInFlight = true;
    return activeTick()
      .catch(() => {})
      .finally(() => { tickInFlight = false; });
  }

  /* Beim Zurückwechseln auf den Tab sofort falten. Nur bei
     `visibilityState === 'visible'` — das Event feuert auch beim
     Verbergen, und dort wäre ein Tick sinnlos (der Browser throttlet ihn
     unmittelbar danach ohnehin). Der Listener wird einmal verdrahtet und
     bei stopPolling wieder abgeräumt, damit ein geschlossenes/gewechseltes
     Board keinen Tick mehr auslöst (F-432-Nachbarschaft). */
  function bindVisibilityTrigger() {
    if (visibilityHandler) return;
    if (typeof document === 'undefined' || !document.addEventListener) return;
    visibilityHandler = () => {
      if (document.visibilityState !== 'visible') return;
      runTick();
    };
    document.addEventListener('visibilitychange', visibilityHandler);
  }

  function unbindVisibilityTrigger() {
    if (!visibilityHandler) return;
    document.removeEventListener('visibilitychange', visibilityHandler);
    visibilityHandler = null;
  }

  /* ── Pending-State (Modal-Guard) ────────────────────────────────── */

  function setPending(state) { pendingState = state; }
  function takePending()     { const s = pendingState; pendingState = null; return s; }
  function hasPending()      { return !!pendingState; }

  /* ADR-0070 §3 (F-286): main.js verdrahtet hier den Live-State-Zugriff für
     den projectorPoll-Pre-Flush. board-unabhängig (zentrale State) → bleibt
     über Board-Wechsel gesetzt; nur _reset() (Test-Isolation) löscht es. */
  function setLocalStateProvider(fn) { localStateProvider = fn; }

  /* ── Init ───────────────────────────────────────────────────────── */

  async function init(callback, boardId) {
    const restored = await tryRestoreHandle(boardId);
    if (!restored) return false;
    if (isActive()) startPolling(callback);
    return true;
  }

  /* Test-Hook (ADR-0050): erlaubt das Injizieren eines Fake-Verzeichnis-
     Handles, um den Guard/Konflikt-/Force-/Baseline-Pfad in write()/read()
     ohne echte FSA-API zu prüfen. Produktiv ungenutzt. */
  const __testing__ = {
    _setDirHandle(h)  { dirHandle = h; permNeeded = false; },
    _setLastHash(v)   { lastHash  = v; },
    _getLastHash()    { return lastHash; },
    _setOnUpdate(cb)  { onUpdate  = cb; },
    _reset() {
      dirHandle = null; activeBoardId = null; lastHash = '';
      pendingState = null; onUpdate = null; permNeeded = false;
      lastAdopted = null; baseSnapshot = null; baseSnapshotSig = null; _inboxOpCount = 0;
      _historyIds = null; _historyKey = '';            /* F-448 */
      _gemeldeteWaisen.clear();                        /* F-459 */
      _gemeldeteZeitstempel.clear();                   /* F-831 */
      _baseMtimeMs = 0; _inboxOldestOpMs = 0;
      localStateProvider = null; pollGen++;
      clearInterval(pollTimer); pollTimer = null;
      activeTick = null; tickInFlight = false; unbindVisibilityTrigger();  /* F-405 */
      pickedPrevious = null; pickInProgress = false;                       /* F-432 */
    },
    _getLastAdopted() { return lastAdopted; },
    /* F-405/F-456: sichtbar machen, ob der Sichtbarkeits-Auslöser verdrahtet
       ist — der Test prüft Verdrahtung UND Abräumen bei stopPolling. */
    _hasVisibilityTrigger() { return !!visibilityHandler; },
    /* Sprint-51-Kritiker (K2/K4): den Tick-Läufer direkt ausüben. `runTick` ist
       intern, seine Guards (tickInFlight, pickInProgress) sind aber genau die
       Stellen, an denen die beiden Befunde sassen — ohne Zugriff bliebe das
       Verhalten strukturell ungetestet. */
    _setActiveTickForTest(fn) { activeTick = fn; },
    _runTick() { return runTick(); },
    _setLocalStateProvider(fn) { localStateProvider = fn; },
    projectorReconcile, projectorPoll, isMigrated, migrate,
  };

  return {
    init, pickDirectory, revertPickedDirectory, commitPickedDirectory, pickLaeuft,
    requestPermission, tryRestoreHandle,
    write, read, READ_ERROR: SYNC_READ_ERROR, markBaseline, startPolling, stopPolling, poll,
    persistGated,
    getInboxOpCount, getInboxBloatInfo, requestCompaction, haengeOpsAn,
    projectorPersist, projectorPoll, projectorReconcile, markAdopted,
    isMigrated, migrate, setLocalStateProvider,
    /* Chat-Tail (Karte #290, claude-code-only — slim DCE't die Rümpfe) */
    getChatTail, setChatTailListener, setChatTailEnabled, readChatTail,
    getDirectoryName, getDirectoryHandle, getActiveBoardId,
    isPermissionNeeded, isActive, clearActiveHandle, setBoardId, listKnownBoards,
    setPending, takePending, hasPending,
    __testing__,
  };
})();
