/* Nutzer-Einstellungen (→ ADR-0018) — Persistenz seit ADR-0048 zweischichtig:
   globale Nutzer-Prefs unter `aboard-config`, board-/projektgebundene Felder
   unter einem deployment-spezifischen Schlüssel `aboard-config@<pathname>`.

   Warum pathname statt board.id (F-246): Der in `aboard.html` eingebettete
   `board.id` ist eine BUILD-Konstante (`aboard-project`) — in jeder deployten
   Kopie identisch. Bei `file://` teilen alle Kopien dieselbe Origin (also
   localStorage). Der einzige stabile, eindeutige, synchron bekannte
   Per-Projekt-Identifikator ist der Pfad der `aboard.html` selbst
   (`location.pathname`). */

const CONFIG_KEY     = 'aboard-config';        /* global + Legacy-Migrationsquelle */
const SCOPE_PREFIX   = 'aboard-config@';       /* + location.pathname (ADR-0048) */
const CONFIG_VERSION = '1';

/* ADR-0044: Legacy-boardMode-Migration — inline, ohne Profile.js zu importieren.
   F-186: VALID_BOARD_MODES schliesst 'server' im Standalone-Build explizit aus;
   im Server-Build (__SERVER_BUILD__ === true) ist 'server' der einzige gueltige Wert. */
const LEGACY_MODE_MAP = { standalone: 'solo', project: 'claude-code' };

/* Compile-Time-Flag (Vite-Define, Karte #69): im Server-Frontend-Build auf true gesetzt. */
const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

/* Compile-Time-Flag (Vite-Define, ADR-0072): true im Projektor-Build
   (`aboard.html`, das claude-code-Konsumenten-Artefakt, ADR-0014).
   ADR-0087/F-318: dort ist 'solo' ein Fehl-Default — ein frischer Konsument
   landet ohne FSA-Prompt im Solo-Modus und sieht ein fremdes Board.
   slim-Template (`'false'`) bleibt 'solo', Server-Build 'server'. */
const IS_PROJECTOR_BUILD = (typeof __PROJECTOR_BUILD__ !== 'undefined') && (__PROJECTOR_BUILD__ === true || __PROJECTOR_BUILD__ === 'true');

const VALID_BOARD_MODES = IS_SERVER_BUILD
  ? new Set(['server'])
  : new Set(['solo', 'team', 'claude-code']);

/* F-828: Muss mit MaxBodyBytes in server/api/middleware.go SYNCHRON gehalten
   werden — dort steht die tatsächliche PUT-Body-Grenze. Die Zusage steht nicht
   nur hier: `tests/maxbody-drift.test.js` liest die Go-Datei als Text und
   vergleicht die Zahl. Eine Regel, die an zwei Stellen steht und von niemandem
   gemessen wird, driftet — und die Folge wäre lautlos (zu klein: Uploads
   scheitern erst am PUT; zu gross: nutzbare Grösse verschenkt). Diese Kopie existiert,
   weil das Frontend sie kennen muss, um VOR dem Request zu warnen statt den
   Nutzer in ein stilles 413 laufen zu lassen. */
const SERVER_MAX_BODY_BYTES = 10 * 1024 * 1024;

/* Sicherheitsabstand zur PUT-Body-Grenze (F-828): `checkUpload` rechnet nur
   die base64-kodierten Anhänge gegen die Grenze — der tatsächliche PUT-Body
   trägt zusätzlich alle Karten, Kommentare und Aktivitäts-Logs als JSON, und
   dieser Anteil fehlt in der Projektion bewusst (er ist unbestimmt gross).
   Eine Hartgrenze GENAU auf MaxBodyBytes liesse also Boards durch, die am
   eigentlichen PUT trotzdem an derselben Grenze scheitern — zwischen „gerade
   noch erlaubt" und „Server lehnt ab" lägen dann 0 Bytes. 2 MB Marge deckt
   den JSON-Overhead eines durchschnittlichen Boards ab, ohne die nutzbare
   Anhang-Grösse spürbar zu verkleinern. Nur im Server-Build relevant — Solo/
   Team haben keine PUT-Body-Grenze (lokales Storage bzw. FSA-Volume). */
const SERVER_ATTACHMENT_HARD_CAP_BYTES = SERVER_MAX_BODY_BYTES - 2 * 1024 * 1024; /* 8 MB */

/* Pro-Datei-Limit des ATTACHMENT-Endpunkts — eine andere Grenze als die
   PUT-Body-Grenze darüber, und der Unterschied ist der ganze Punkt von
   Karte #450 (F-NEU: Frontend-Kritiker Sprint 61).

   Muss mit `defaultMaxAttachmentBytes` in `server/api/attachments.go`
   synchron bleiben; `tests/maxbody-drift.test.js` misst das.

   Warum das nötig wurde: Seit Karte #450 reisen Karten-Anhänge per Multipart
   an `POST /boards/{id}/attachments/{cardId}` — dieser Endpunkt nimmt 50 MB.
   Der Client prüfte trotzdem weiter gegen `attachmentMaxBytes` (2 MB), das
   für den base64-im-State-Weg gedacht ist. Zusammen mit der (richtigerweise)
   abgeschalteten Verkleinerung ergab das die Regression, die den Anlassfall
   der Karte reproduziert: ein 4-MB-Foto wurde nach dem Umbau mit „zu gross"
   abgelehnt, obwohl der Server es klaglos genommen hätte — 25× strenger als
   nötig.

   EHRLICHE GRENZE: Der Betreiber kann `ABOARD_ATTACHMENT_MAX_BYTES` kleiner
   setzen; dann ist diese Client-Prüfung zu lax und der Nutzer sieht ein 413
   nach dem Hochladen. Das ist der bessere Fehler von beiden — zu streng
   verhindert einen Upload, der funktioniert hätte, zu lax kostet Wartezeit
   und zeigt eine Server-Meldung. */
const SERVER_ATTACHMENT_MAX_FILE_BYTES = 50 * 1024 * 1024;

const DEFAULTS = {
  version:         CONFIG_VERSION,
  theme:           'system',      /* 'system' | 'light' | 'dark'     */
  density:         'normal',      /* 'normal' | 'kompakt' (Karte #973)  */
  /* ADR-0153 (Karte #1183): Darstellung je Gerät */
  corners:         'square',      /* 'square' | 'round' (Karte #1183) */
  font:            'system',      /* 'system' | 'serif' | 'mono'     */
  accent:          'teal',        /* 'teal' | 'blue' | 'green' | 'violet' | 'graphite' */
  sortierung:      'manuell',     /* 'manuell' | 'dringlichkeit' (Karte #1004) */
  language:        'en',          /* 'en' | 'de'                     */
  userName:        '',            /* Autor-Tag in Kommentaren        */
  boardMode:       IS_SERVER_BUILD ? 'server' : (IS_PROJECTOR_BUILD ? 'claude-code' : 'solo'),  /* ADR-0044: Server hartcodiert; ADR-0087: Projektor-Build → claude-code (F-318) */
  projectName:     '',            /* Anzeigename des Projektordners  */
  /* ADR-0033: Attachment-Limits — gelten nur im Standalone-Modus (FSA-Pfad limit-frei) */
  attachmentMaxBytes:       2 * 1024 * 1024,   /* Pro-Datei-Limit: 2 MB */
  /* F-828: Im Server-Build liegen Warn-/Hartgrenze unter SERVER_ATTACHMENT_HARD_CAP_BYTES
     (8 MB) statt bei den alten 8/10 MB — die alte Hartgrenze lag exakt auf
     MaxBodyBytes und liess keinen Puffer für den restlichen Board-JSON. Solo/Team
     bleiben bei den bisherigen 8/10 MB (keine PUT-Body-Grenze). */
  attachmentBoardWarnBytes: IS_SERVER_BUILD ? 6 * 1024 * 1024 : 8 * 1024 * 1024,
  attachmentBoardHardBytes: IS_SERVER_BUILD ? SERVER_ATTACHMENT_HARD_CAP_BYTES : 10 * 1024 * 1024,
  /* Server-Modus (Karte #78): REST-Plugin-Einstellungen */
  serverUrl:          '',   /* REST-API-Basis-URL — leer = gleicher Ursprung (Reverse-Proxy) */
  serverPollInterval: 10,   /* Polling-Intervall in Sekunden (5–60) */
  serverPollEnabled:  true, /* Polling an/aus */
  /* Karte #151: Eingeklappte Spalten — Array von 'sprintId:colId'-Keys, geräte-spezifisch */
  collapsedColumns:   [],
};

/* Karte #290/#325/#329: Chat-Tail- + Activity-Tail-Settings sind nur im
   Projektor-Build sinnvoll (#activity-panel + .chat-tail-footer sind beide
   IS_PROJECTOR_BUILD-gegated). Slim/Server DCE't den ganzen Block; die Keys
   existieren dort nicht (Config.get() liefert undefined → Feature inaktiv). */
if (IS_PROJECTOR_BUILD) {
  DEFAULTS.chatTailEnabled    = false;
  DEFAULTS.chatTailLines      = 20;
  /* Karte #329: board.activity-Verlauf als Footer-Tab. IS_PROJECTOR_BUILD-
     gegated (DCE im slim/server), aber NICHT claude-code-only — auch
     project/team/solo dürfen den Verlauf sehen. Default on. */
  DEFAULTS.activityTailEnabled = true;
  DEFAULTS.activityTailLines   = 20;
}

/* ADR-0048: Felder, die bewusst NICHT pro Deployment getrennt werden — echte
   nutzer-/geräteweite Präferenzen. Alles andere in DEFAULTS ist
   deployment-scoped (boardMode, projectName, collapsedColumns, serverUrl,
   serverPoll*) und liegt unter `aboard-config@<pathname>`. */
/* Exportiert für den Drift-Konsistenztest (ADR-0063 §4 / F-269): settings-scope.js
   spiegelt diese Menge als persistScope:'global' — der Test difft gegen den Import,
   nicht gegen eine hartcodierte Kopie. */
export const GLOBAL_KEYS = new Set([
  'version', 'theme', 'density', 'corners', 'font', 'accent', 'sortierung', 'language', 'userName',
  'attachmentMaxBytes', 'attachmentBoardWarnBytes', 'attachmentBoardHardBytes',
]);

/* ADR-0029: Typora ist der einzige Edit-Modus. Altes `defaultEditMode`-Feld
   wird beim Laden stillschweigend ignoriert (kein Migrations-Aufwand). */

let current = { ...DEFAULTS };

/* Deployment-Scope-Schlüssel: im Server-Build null (echte Per-Deployment-Origin,
   kein geteilter file://-localStorage → Legacy-Einzelschlüssel genügt). Im
   Standalone/file://-Build der Pfad der aboard.html. Ohne ermittelbaren
   pathname → null (Fallback = Legacy-Verhalten, kein Datenverlust). */
function deploymentKey() {
  if (IS_SERVER_BUILD) return null;
  const path = (typeof globalThis !== 'undefined'
    && globalThis.location && globalThis.location.pathname) || '';
  return path ? SCOPE_PREFIX + path : null;
}

function readJSON(key) {
  try {
    const raw = key && localStorage.getItem(key);
    return raw ? JSON.parse(raw) : null;
  } catch { return null; }
}

/* boardMode auf gueltige Profil-ID klemmen (ADR-0044 / F-186). No-Op im
   Server-Build — dort ist boardMode compile-time fest. */
function normalizeBoardMode() {
  if (IS_SERVER_BUILD) return;
  if (LEGACY_MODE_MAP[current.boardMode]) current.boardMode = LEGACY_MODE_MAP[current.boardMode];
  if (!VALID_BOARD_MODES.has(current.boardMode)) current.boardMode = 'solo';
}

/* F-828: attachmentBoardHardBytes im Server-Build auf SERVER_ATTACHMENT_HARD_CAP_BYTES
   deckeln — unabhängig davon, ob der Wert per UI (`set`), aus einem älteren
   Legacy-Blob (`load`) oder von einem Server-Build vor diesem Fix stammt. Nur
   ein Deckel, kein Boden: kleinere/eigene Werte bleiben unangetastet, sie sind
   nur sicherer als der Default. Solo/Team sind unbetroffen (keine PUT-Body-Grenze). */
function clampAttachmentHardBytes() {
  if (!IS_SERVER_BUILD) return;
  /* `Number.isFinite`, nicht `typeof === 'number'` (Frontend-Kritiker
     Sprint 61): `NaN` ist `typeof 'number'`, und `NaN > CAP` ist `false` —
     der Wert überlebte den Clamp. In `checkUpload` ist dann `projected > NaN`
     ebenfalls immer `false`, die Hartgrenze wäre stumm abgeschaltet. Über den
     Einstellungsdialog nicht erreichbar (`readMB` filtert `!isFinite`), über
     einen manipulierten localStorage-Blob schon. */
  if (!Number.isFinite(current.attachmentBoardHardBytes) ||
      current.attachmentBoardHardBytes > SERVER_ATTACHMENT_HARD_CAP_BYTES) {
    current.attachmentBoardHardBytes = SERVER_ATTACHMENT_HARD_CAP_BYTES;
  }
}

function load() {
  /* 1) Legacy-/Global-Blob laden. Enthält bei Bestandsnutzern noch
     deployment-scoped Felder — die dienen als einmalige Migrationsquelle
     (Seed) und als Rückwärtskompatibilität, falls kein Scope ermittelbar. */
  const parsed = readJSON(CONFIG_KEY);
  if (parsed) {
    if (IS_SERVER_BUILD) delete parsed.boardMode;
    current = { ...DEFAULTS, ...parsed };
  }

  /* 2) Deployment-Scope auflösen und scoped Felder überlagern. */
  const dk = deploymentKey();
  if (dk) {
    const scoped = readJSON(dk);
    if (scoped) {
      if (IS_SERVER_BUILD) delete scoped.boardMode;
      for (const k of Object.keys(DEFAULTS)) {
        if (!GLOBAL_KEYS.has(k) && k in scoped) current[k] = scoped[k];
      }
    }
    /* Kein scoped-Eintrag → current behält die Seed-Werte aus Schritt 1
       (einmalige Migration). normalizeBoardMode + sofort getrennt
       persistieren; der Legacy-Blob wird dabei NICHT gestrippt (sonst
       würden noch nicht migrierte Parallel-Deployments ihren boardMode
       verlieren — Aushunger-Schutz, F-246). */
  }
  if (!IS_SERVER_BUILD) normalizeBoardMode();
  clampAttachmentHardBytes();
  if (dk) save();          /* Migration/Split persistieren */
  apply();
  return current;
}

function save() {
  try {
    const dk = deploymentKey();
    if (!dk) {
      /* Server-Build / kein pathname → Legacy-Einzelschlüssel. */
      localStorage.setItem(CONFIG_KEY, JSON.stringify(current));
      return;
    }
    /* Global-Blob: vorhandene (Legacy-)board-Felder UNVERÄNDERT lassen
       (Seed für noch nicht migrierte Deployments — kein Aushungern), nur
       die globalen Felder aus current überschreiben. */
    const legacy = readJSON(CONFIG_KEY) || {};
    for (const k of Object.keys(current)) {
      if (GLOBAL_KEYS.has(k)) legacy[k] = current[k];
    }
    localStorage.setItem(CONFIG_KEY, JSON.stringify(legacy));

    /* Deployment-scoped Blob: ausschliesslich nicht-globale Felder. */
    const scoped = {};
    for (const [k, v] of Object.entries(current)) {
      if (!GLOBAL_KEYS.has(k)) scoped[k] = v;
    }
    localStorage.setItem(dk, JSON.stringify(scoped));
  } catch { /* localStorage voll */ }
}

function get(key) {
  return current[key] ?? DEFAULTS[key];
}

function set(key, value) {
  let v = value;
  if (key === 'boardMode') {
    /* Im Server-Build ist boardMode unveraenderlich — compile-time festgelegt. */
    if (IS_SERVER_BUILD) return;
    if (LEGACY_MODE_MAP[v]) v = LEGACY_MODE_MAP[v];
    if (!VALID_BOARD_MODES.has(v)) v = 'solo';
  }
  /* F-828: die Hartgrenze darf im Server-Build nicht über den PUT-Body-Deckel
     hinaus gesetzt werden — auch nicht über den Einstellungsdialog. */
  if (key === 'attachmentBoardHardBytes' && IS_SERVER_BUILD &&
      typeof v === 'number' && v > SERVER_ATTACHMENT_HARD_CAP_BYTES) {
    v = SERVER_ATTACHMENT_HARD_CAP_BYTES;
  }
  current[key] = v;
  save();
  apply();
}

/* ADR-0153: Darstellungs-Schlüssel, je als data-<schlüssel> an <html> */
export const DARSTELLUNG = ['corners', 'font', 'accent'];

/* Einstellungen sofort im DOM wirksam machen */
function apply() {
  const root = document.documentElement;
  root.setAttribute('data-theme', current.theme);
  root.setAttribute('data-density', current.density);
  for (const k of DARSTELLUNG) root.setAttribute('data-' + k, current[k]);
  root.setAttribute('lang', current.language === 'de' ? 'de' : 'en');
}

/* F-828: exportiert, damit der Einstellungsdialog das Eingabefeld mit einem
   `max`-Attribut versehen kann (UX — die Hartgrenze in `set()` gilt ohnehin
   unabhängig davon) und damit Tests den Deckel gegen dieselbe Quelle prüfen. */
export const Config = { load, get, set, DEFAULTS,
  SERVER_ATTACHMENT_HARD_CAP_BYTES,
  SERVER_ATTACHMENT_MAX_FILE_BYTES,
  /* Verify zu F-828: auch die Ausgangszahl exportieren, damit der
     Drift-Wächter (tests/maxbody-drift.test.js) sie gegen
     server/api/middleware.go prüfen kann, statt sie zurückzurechnen —
     eine zurückgerechnete Zahl wäre eine dritte Kopie. */
  SERVER_MAX_BODY_BYTES };
