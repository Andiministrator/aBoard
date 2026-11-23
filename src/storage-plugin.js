/* Storage-Plugin-Interface (ADR-0044, Karte #67)
 *
 * Schmale Fassade über `Storage` und `Sync`. Macht das Frontend storage-agnostisch:
 * Aufrufer sprechen nur das Plugin-Interface an, die zwei Phase-1-Implementierungen
 * delegieren intern an die bisherigen Module. Die Phase-2-Implementierung
 * (`StorageREST`) wird denselben Vertrag erfüllen — ohne weitere Eingriffe am
 * Aufrufer.
 *
 * Scope von #67: das Interface + zwei Implementierungen + Factory.
 * Capability-Refactor der ~22 `boardMode`-Verzweigungen → Karte #68 (Profil-System).
 *
 * Karte #203 / ADR-0064: REST-Plugin-Code (createRESTPlugin, createRESTAttachmentBackend,
 * ACL-Methoden) wird hinter IS_SERVER_BUILD gegated — im Standalone-/Template-Build
 * (IS_SERVER_BUILD=false) eliminiert esbuild den gesamten Block per DCE.
 */

import { Storage } from './storage.js';
import { EventBus } from './eventbus.js';
import { Sync }    from './sync.js';
import { Config }  from './config.js';
import { Profile } from './profile.js';
import { Model }   from './model.js';
import * as Attachments from './attachments.js';
/* ADR-0129 §4, Nachtrag Sprint 72 (Karte #564): Op-Algebra für den
   Ops-Schreibweg des REST-Plugins. Nur im IS_SERVER_BUILD-Block benutzt —
   im slim Template fällt sie mit diesem Block heraus (Punkt 7). */
import { bereiteOpsVor, fuehreBezugNach, bedingungsloseFeldOps } from './rest-ops.js';
/* Karte #1053 (ADR-0148 §5): 401 im OIDC-Betrieb. Nur Server-Build (null sonst). */
import { Sitzung, holen } from './sitzung-abgelaufen.js';

/* Compile-Time-Flag (Vite-Define, wie in config.js): im Server-Frontend-Build auf true gesetzt.
   Bei IS_SERVER_BUILD=false eliminiert esbuild alle REST-Plugin-Blöcke per DCE (Karte #203). */
const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

/**
 * @typedef {Object} StoragePlugin
 * @property {'standalone'|'project'} kind
 * @property {() => Promise<void>} init
 *   Lifecycle-Hook: explizite Initialisierung (DB-Connection, Auth-Token, …).
 *   Standalone/Project: noop. REST-Plugin (Phase 2): Auth-Setup. Idempotent.
 * @property {() => Promise<void>} destroy
 *   Lifecycle-Hook: Cleanup beim Profil-Wechsel (Polling stoppen, Handles freigeben).
 *   Idempotent — wiederholter Aufruf wirft nicht.
 * @property {(boardId?: string) => Promise<void>} preload
 *   Handles wiederherstellen / Permissions prüfen. Beim App-Init aufrufen.
 * @property {() => { state: object|null, fromLocalStorage: boolean }} load
 *   Synchroner Initial-Load (Embedded JSON + localStorage-Fallback). Migration läuft
 *   bereits in `checkVersion` — Rückgabe ist migrierter State oder `null`.
 * @property {(state: object) => Promise<{ok: boolean, conflict?: ConflictInfo, method?: string}>} save
 *   Manuelles Speichern (Save-Button). Standalone: FSA-Datei via Picker.
 *   Projekt: aboard-sync.json + aboard.html via Directory-Handle.
 *   `conflict` ist für Phase-2 (REST-Plugin) reserviert — wird von Phase-1-Plugins nie gesetzt.
 *   `method` bleibt erhalten (informativ für Button-Text im board:save-Handler).
 * @property {(raw: string) => object|null} parseAndMigrate
 *   Parst einen rohen JSON-String und führt Schema-Migration durch (delegiert an
 *   `Storage.checkVersion`). Kapselt den Migrations-Pfad hinter dem Plugin-Interface —
 *   externe Aufrufer (main.js) sprechen nur diesen Vertrag an, kein direktes
 *   `Storage.checkVersion`-Aufrufen nötig. REST-Plugin überschreibt dies mit
 *   server-spezifischer Migration (Phase 2).
 * @property {(state: object) => Promise<boolean>} autoPersist
 *   Debouncter Auto-Save. Standalone: localStorage-Puffer (synchron erfolgreich).
 *   Projekt: aboard-sync.json. Liefert `true` bei Erfolg, `false` bei Schreibfehler.
 * @property {(boardId: string) => Promise<{ok: boolean, state?: object, error?: 'no-handle'|'permission-denied'|'invalid-file'|'read-error'|'cancelled'}>} switchBoard
 *   Wechsel zu einem bekannten Board. Kapselt Permission-Check + Restore + Read +
 *   Migration. Im Fehlerpfad wird der Plugin-Zustand nicht mutiert (kein halb-
 *   restorierter Handle hinterlassen). Aufrufer fängt den Toast pro `error`-Code ab.
 * @property {(callback: (state: object) => void) => void} startPolling
 *   Externe State-Änderungen beobachten. Standalone: noop. Projekt: 15-s-Polling.
 * @property {() => void} stopPolling
 * @property {() => boolean} isPollingSupported
 * @property {() => boolean} isPollingActive
 *   Projekt-Modus mit gültigem Handle? Liefert in Standalone immer `false`.
 * @property {() => Promise<Array<{id: string, name: string, title?: string, role?: string}>>} listKnownBoards
 *   Bekannte Boards für Switch-UI. Standalone: aus Storage-IDB. Projekt: aus Sync-IDB.
 *   REST-Plugin (Forward-Auth AN): Einträge haben zusätzlich `role` (owner/editor/viewer).
 * @property {(title: string) => Promise<{ok: boolean, state?: object, error?: string}>} createBoard
 *   Neues Board anlegen (Server-Modus: POST /boards + switchBoard). Standalone/Projekt:
 *   liefert { ok: false } — Board-Anlage ist dort UI-seitig nicht vorgesehen.
 * @property {() => Promise<{name: string|null, email: string|null}>} getCurrentUser
 *   Liefert den identifizierten User (Forward-Auth AN) oder { name: null, email: null }
 *   im Legacy-Bypass (Single-User, kein Forward-Auth). Standalone/Projekt: immer null-Felder.
 * @property {() => AttachmentBackend} attachments
 *   Liefert das Attachment-Backend (ADR-0033). Standalone: Base64-im-State.
 *   Projekt: FSA-Volume. Server: Multipart-Upload (Phase 2).
 *   Wird als Factory gehalten, damit das REST-Plugin später ein eigenes Backend liefert,
 *   ohne dass die Aufrufer ihre Imports umbiegen müssen.
 */

/**
 * Standard-Attachment-Backend für FSA-/Embedded-Welt (Standalone + Project).
 * Die einzelnen Funktionen aus `attachments.js` entscheiden selbst (via
 * Profile/Config), ob in den State-Base64-Pfad oder ins FSA-Volume geschrieben
 * wird — beide Plugins können denselben Backend-Snapshot zurückgeben.
 * Das REST-Plugin (Phase 2) wird hier eine alternative Implementierung liefern.
 */
export function createFsaAttachmentBackend() {
  return {
    /* Karte #450: „Landet ein Karten-Anhang im Board-State?" — die Frage
       entscheidet, ob verkleinert wird (verlustbehaftet, nur lohnend wenn sie
       Platz im State spart) und ob die Board-Grössenprüfung greift. Sie gehört
       ins Backend, weil nur das den Transportweg kennt. Vorher stand sie als
       `!Profile.has('feature.fsaVolumeAttachments')` im Modal — richtig,
       solange es nur FSA-Volume und State gab, und stillschweigend falsch,
       sobald ein dritter Weg (REST) dazukommt. */
    kartenAnhangImBoardState: () => !Profile.has('feature.fsaVolumeAttachments'),
    /* Board-Anhänge (Hintergrundbild) nehmen in dieser Welt denselben Weg wie
       Karten-Anhänge — deshalb dieselbe Antwort und dieselbe Prüfung. */
    boardAnhangImBoardState:  () => !Profile.has('feature.fsaVolumeAttachments'),
    /* Karte #443: eigene Frage, eigener Schalter. Verkleinert wird, damit
       ein Anzeigebild nicht in Originalgrösse durch die Leitung geht —
       nicht, um Platz zu sparen. Hier fällt beides zusammen: Im
       FSA-Volume liegt die Datei lokal und wird beim Anzeigen nicht
       übertragen, im base64-Fall steckt sie im State. */
    boardBildVerkleinern:    () => !Profile.has('feature.fsaVolumeAttachments'),
    /* SPÄT gebunden, nicht früh (Karte #450): `uploadFile: Attachments.uploadFile`
       greift schon beim ERZEUGEN des Backends auf jeden Export zu. Das ist aus
       zwei Gründen schlechter als ein Wrapper. Praktisch: Ein Test, der
       `attachments.js` nur teilweise mockt — der Normalfall, niemand mockt
       vierzehn Funktionen für einen Drag-and-Drop-Test — lässt das Backend
       beim Konstruieren scheitern, an einer Funktion, die der Test gar nicht
       braucht. Grundsätzlich: Eine zur Erzeugungszeit eingefrorene
       Modulreferenz lässt sich später nicht mehr ersetzen, und genau das ist
       der Zweck eines austauschbaren Backends. */
    checkBoardUpload:      (...a) => Attachments.checkUpload(...a),
    uploadFile:            (...a) => Attachments.uploadFile(...a),
    readAttachment:        (...a) => Attachments.readAttachment(...a),
    attachmentUrl:         (...a) => Attachments.attachmentUrl(...a),
    deleteAttachment:      (...a) => Attachments.deleteAttachment(...a),
    downloadAttachment:    (...a) => Attachments.downloadAttachment(...a),
    purgeCardAttachments:  (...a) => Attachments.purgeCardAttachments(...a),
    uploadBoardFile:       (...a) => Attachments.uploadBoardFile(...a),
    boardAttachmentUrl:    (...a) => Attachments.boardAttachmentUrl(...a),
    boardAttachmentObjectUrl: (...a) => Attachments.boardAttachmentObjectUrl(...a),
    deleteBoardAttachment: (...a) => Attachments.deleteBoardAttachment(...a),
    boardSizeLabel:        (...a) => Attachments.boardSizeLabel(...a),
    checkUpload:           (...a) => Attachments.checkUpload(...a),
    computeBoardBytes:     (...a) => Attachments.computeBoardBytes(...a),
    humanSize:             (...a) => Attachments.humanSize(...a),
    safeMime:              (...a) => Attachments.safeMime(...a),
  };
}

/* ── Standalone-Plugin ───────────────────────────────────────────────── */
/* Wrappt das `Storage`-Modul (FSA-File-Handle + Embedded + localStorage). */

function createStandalonePlugin() {
  return {
    kind: 'standalone',

    async init() {
      /* Standalone hat keinen expliziten Initialisierungs-Schritt — Modul-Singleton
         genügt. Lifecycle-Hook bleibt trotzdem da, damit das REST-Plugin (Phase 2)
         denselben Vertrag erfüllen kann ohne Aufrufer-Verzweigung. */
    },

    async destroy() {
      /* Standalone hat keinen Lifecycle-Zustand zu räumen. Idempotent per Vertrag. */
    },

    async preload(boardId) {
      if (boardId) await Storage.preloadFileHandle(boardId);
    },

    load() {
      return Storage.load();
    },

    async save(state) {
      return await Storage.save(state, null);
    },

    async autoPersist(state) {
      /* Synchron via localStorage — liefert immer `true` (best-effort, F-24-konform). */
      Storage.autoSave(state, false);
      return true;
    },

    async switchBoard(boardId) {
      /* Standalone-Wechsel geht über den gespeicherten FSA-File-Handle.
         openByBoardId prüft Permission, liest die Datei und parst sie (HTML- oder
         JSON-Pfad). Bei Permission-prompt führt openByBoardId den Request selbst. */
      const state = await Storage.openByBoardId(boardId);
      if (!state) return { ok: false, error: 'no-handle' };
      if (!state.board) return { ok: false, error: 'invalid-file' };
      return { ok: true, state };
    },

    startPolling(_callback) { /* nicht unterstützt */ },
    stopPolling()           { /* noop */ },
    isPollingSupported()    { return false; },
    isPollingActive()       { return false; },

    parseAndMigrate(raw) {
      try {
        const parsed = JSON.parse(raw);
        return Storage.checkVersion(parsed);
      } catch { return null; }
    },

    async listKnownBoards() {
      return await Storage.listKnownFiles();
    },

    async createBoard(_title) {
      /* Standalone: Board-Anlage auf Server nicht unterstützt — Vertrag erfüllen. */
      return { ok: false };
    },

    async getCurrentUser() {
      /* Standalone: kein Server, kein Forward-Auth — immer null-Felder. */
      return { name: null, email: null };
    },

    attachments() {
      return createFsaAttachmentBackend();
    },
  };
}

/* ── Project-Plugin ──────────────────────────────────────────────────── */
/* Wrappt das `Sync`-Modul (FSA-Directory-Handle + aboard-sync.json + Polling).
   Für das manuelle Speichern wird zusätzlich `Storage.save` mit dem Directory-
   Handle aufgerufen, damit auch `aboard.html` aktualisiert wird. */

function createProjectPlugin() {
  return {
    kind: 'project',

    async init() {
      /* Projekt-Modus hat keinen App-weiten Initialisierungs-Schritt — der
         Directory-Handle wird via preload(boardId) auf Verlangen restauriert.
         Lifecycle-Hook bleibt für Vertrag-Symmetrie da. */
    },

    async destroy() {
      /* Polling stoppen und aktiven Handle vergessen — der IDB-Eintrag bleibt
         erhalten, damit beim erneuten Aktivieren des Profils restoriert werden kann.
         Idempotent: zweite Aufruf-Welle findet nichts zu räumen und wirft nicht. */
      Sync.stopPolling();
      Sync.clearActiveHandle();
    },

    async preload(boardId) {
      if (boardId) await Sync.tryRestoreHandle(boardId);
    },

    load() {
      /* Initial-Load läuft weiter über Storage.load() — das Sync-File wird
         erst NACH dem Embedded-Load gelesen (siehe `init()` in main.js).
         Der Plugin-Vertrag bleibt symmetrisch zu Standalone. */
      return Storage.load();
    },

    async save(state) {
      /* ADR-0070 §2 (F-291): Gate auf isMigrated().
         migriert  → ADR-0067 Projektor (Diff→Ops→Inbox→Projektion).
         nicht mig.→ Pre-#228 Legacy mit Read-before-Write-Guard
                     (F-247/ADR-0050) + Konflikt-Übersetzung (kind:'fsa').
         aboard.html wird in beiden Fällen geschrieben (ADR-0014). */
      if (await Sync.isMigrated()) {
        const ok = await Sync.projectorPersist(state, Config.get('userName') || '');
        if (!ok) console.warn('aboard-sync-Projektion konnte nicht geschrieben werden');
      } else {
        const r = await Sync.write(state);
        if (r && r.conflict) {
          /* NICHT an Storage.save durchreichen — würde aboard.html schreiben,
             obwohl der User den Konflikt noch nicht entschieden hat. */
          return { ok: false, conflict: {
            serverState: Storage.checkVersion(r.diskState),
            diskRaw:     r.diskRaw,
            kind:        'fsa',
          } };
        }
        if (r !== true) console.warn('aboard-sync.json konnte nicht geschrieben werden');
      }
      const dirHandle = Sync.getDirectoryHandle();
      return await Storage.save(state, dirHandle);
    },

    async autoPersist(state) {
      /* Projekt-Modus: localStorage-Puffer ist deaktiviert (ADR-0014).
         ADR-0070 §2: migriert → Projektor; nicht migriert → Legacy-Guard
         (Konflikt → Banner via debouncedPersistInner, F-247/ADR-0050). */
      if (await Sync.isMigrated()) {
        return await Sync.projectorPersist(state, Config.get('userName') || '') === true;
      }
      const r = await Sync.write(state);
      if (r === true) return true;
      if (r && r.conflict) {
        return { ok: false, conflict: {
          serverState: Storage.checkVersion(r.diskState),
          diskRaw:     r.diskRaw,
          kind:        'fsa',
        } };
      }
      return false;
    },

    async switchBoard(boardId) {
      /* Kapselt den heutigen `sync:switch-to-board`-Pfad aus main.js: aktuellen
         Handle vergessen, neuen restoren, ggf. Permission anfordern, Datei lesen,
         migrieren. Mutations des Plugin-Zustands (Polling, Handles) übernimmt der
         Aufrufer (main.js) — switchBoard liefert nur den restorierten State zurück,
         damit der Vertrag testbar bleibt und die Polling-Wiederaufnahme dort sitzt,
         wo auch der State-Update-Eventbus läuft. */
      Sync.stopPolling();
      Sync.clearActiveHandle();

      const restored = await Sync.tryRestoreHandle(boardId);
      if (!restored) return { ok: false, error: 'no-handle' };

      if (Sync.isPermissionNeeded()) {
        const granted = await Sync.requestPermission();
        if (!granted) {
          Sync.clearActiveHandle();
          return { ok: false, error: 'permission-denied' };
        }
      }

      const raw = await Sync.read();
      /* F-854: Das Sentinel VOR der Falsy-Pruefung abfangen. `Sync.READ_ERROR`
         ist ein Symbol und damit truthy — es lief bis Sprint 65 durch nach
         `JSON.parse(raw)`, das an einem Symbol mit `TypeError` scheitert, den
         das `try {} catch {}` darunter schluckt. Das Ergebnis war zufaellig
         richtig (`invalid-file`), haftete aber an einem Nebeneffekt von
         `JSON.parse` statt an einer Pruefung: Wer hier spaeter ein
         `if (typeof raw === 'string')` ergaenzt oder den Parser tauscht,
         aendert das Verhalten, ohne es zu merken.
         Und die Fehlerart war irrefuehrend: Ein transienter Lesefehler ist
         nicht dasselbe wie eine kaputte Datei — dieselbe Unterscheidung, die
         F-477 in `main.js` eingefuehrt hat (dort `sync.read-error` statt
         Neuanlage, um die Verlustklasse aus F-432 zu vermeiden). */
      if (raw === Sync.READ_ERROR) {
        Sync.clearActiveHandle();
        return { ok: false, error: 'read-error' };
      }
      if (!raw) {
        Sync.clearActiveHandle();
        return { ok: false, error: 'invalid-file' };
      }

      let parsed = null;
      try { parsed = JSON.parse(raw); } catch {}
      const validated = parsed ? Storage.checkVersion(parsed) : null;
      if (!validated?.board) {
        Sync.clearActiveHandle();
        return { ok: false, error: 'invalid-file' };
      }
      return { ok: true, state: validated };
    },

    startPolling(callback) { Sync.startPolling(callback); },
    stopPolling()          { Sync.stopPolling(); },
    isPollingSupported()   { return true; },
    isPollingActive()      { return Sync.isActive(); },

    parseAndMigrate(raw) {
      try {
        const parsed = JSON.parse(raw);
        return Storage.checkVersion(parsed);
      } catch { return null; }
    },

    async listKnownBoards() {
      return await Sync.listKnownBoards();
    },

    async createBoard(_title) {
      /* Projekt-Modus: Board-Anlage auf Server nicht unterstützt — Vertrag erfüllen. */
      return { ok: false };
    },

    async getCurrentUser() {
      /* Projekt-Modus: kein Server, kein Forward-Auth — immer null-Felder. */
      return { name: null, email: null };
    },

    attachments() {
      return createFsaAttachmentBackend();
    },
  };
}

/* ── REST-Attachment-Backend + REST-Plugin ───────────────────────────── */
/* Karte #203 / ADR-0064: Gesamter REST-Block nur im Server-Build.
   IS_SERVER_BUILD ist eine Vite-Define-Compile-Konstante → bei false eliminiert
   esbuild den gesamten if-Block per DCE. pluginKindFor() kann im Nicht-Server-Build
   nie 'rest' zurückgeben, weil config.js normalizeBoardMode() jeden boardMode auf
   solo/team/claude-code klemmt (VALID_BOARD_MODES enthält 'server' nur im
   Server-Build) — createRESTPlugin() wird nie aufgerufen. (F-276: NICHT
   Profile.has-basiert — Profile.has('feature.restStorage','server') würde true
   liefern; die echte Schutzschicht ist die boardMode-Klemmung in config.js.) */

/* Im Nicht-Server-Build: Platzhaltervariable → bleibt null, wird nie aufgerufen.
   Im Server-Build: wird durch den if-Block weiter unten befüllt. */
let createRESTPlugin = null;
/* F-2988 (#1135): gemerkte, gescheiterte Karten-Purges erneut senden. Nur im
   Server-Build gesetzt, im Standalone null. */
let anhangPurgeNachholen = null;

if (IS_SERVER_BUILD) {

/* Implementiert das AttachmentBackend-Interface für den Server-Modus (Karte #74).
   Ersetzt den FSA/Base64-Pfad aus attachments.js durch REST-Endpoints. */

function fileToBase64REST(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const r = reader.result ?? '';
      const idx = r.indexOf(',');
      resolve(idx >= 0 ? r.slice(idx + 1) : r);
    };
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });
}

/* F-2988 (#1135): Scheitert das Aufräumen der Anhänge einer gelöschten Karte
   (401, 408, 429, 5xx, Netzfehler), merkt sich der Client Board und Karte und
   sendet das DELETE erneut, sobald ein Server-Stand ankommt, der die Karte
   nicht mehr enthält (main.js `restEingang`).
   Nacharbeit Kritiker S115 (F-3009..F-3011) und Verify (F-3015):
   - Maßstab ist allein ein vom Server gelieferter Stand, nie der lokale:
     `markClean` kommt auch nach teilweise abgelehntem Speichern (F-3015).
     Fehlt die Karte darin, wird gesendet; steht sie dort noch (Löschen nicht
     angekommen, Konflikt mit Wiederherstellung), wird der Eintrag verworfen —
     deleteCard am Server löschte sonst die Dateien einer lebenden Karte
     (F-3009). Anderes Board → Eintrag bleibt.
   - Die Liste lebt nur im Speicher dieses Tabs: kein Nachholen nach Neuladen,
     Abmelden oder für einen anderen Nutzer im selben Browser (F-3009, F-3010).
   - Nur vorübergehende Fehler werden wiederholt; übrige 4xx verworfen (F-3011).
   403 kommt nie auf die Liste: ohne Recht hilft keine Wiederholung.
   F-3019/F-3020 (#1143): Das Nachholen sendet `?nurVerwaist=1`; den Beweis, dass
   die Karte fehlt, führt der Server (knowledge §56). Der Sofort-Purge beim
   Löschen trägt den Parameter nicht. */
const purgeOffen = new Map(); /* url → { boardId, cardId } */
const PURGE_OFFEN_MAX = 100;

/* Ein DELETE auf den Karten-Ordner. 'ok' = weg (auch 404), 'rest' = geschützte Dateien blieben, 'verboten' = 403,
   'nochmal' = 401/408/429/5xx/Netzfehler, 'verwerfen' = übrige Status (400, 409 …). */
async function purgeSenden(url) {
  try {
    const res = await fetch(url, { method: 'DELETE' });
    Sitzung?.meldeAntwort(res.status); /* F-2953 (#1120): Karten-Anhänge purgen */
    /* F-3013 (#1141): 200 mit `geschuetztGeblieben` → Dateien fremder Kommentare blieben an
       der (noch lebenden) Karte; nach dem Speichern als Rest nachholen. */
    if (res.status === 200) {
      const n = await res.json().then(j => j?.geschuetztGeblieben, () => 0);
      if (n > 0) return 'rest';
    }
    if (res.ok || res.status === 404) return 'ok';
    if (res.status === 403) return 'verboten';
    return [401, 408, 429].includes(res.status) || res.status >= 500 ? 'nochmal' : 'verwerfen';
  } catch { return 'nochmal'; }
}

let purgeNachholLauf = null;

/* Lage einer gemerkten Karte gegenüber einem Server-Stand: 'weg' | 'da' | 'unklar'. */
function purgeLage(stand, boardId, cardId) {
  const board = stand?.board;
  if (!board || board.id !== boardId) return 'unklar';
  return (board.sprints || []).some(sp => (sp.cards || []).some(c => c?.id === cardId)) ? 'da' : 'weg';
}

/* Arbeitet die gemerkten Purges gegen `serverStand` ab (Einzelflug). Beim ersten
   'nochmal' hört der Lauf auf — der Server ist dann noch nicht wieder da. Ohne
   Stand passiert nichts. */
anhangPurgeNachholen = function anhangPurgeNachholen(serverStand) {
  if (purgeNachholLauf) return purgeNachholLauf;
  purgeNachholLauf = (async () => {
    let verworfen = 0;
    for (const [url, { boardId, cardId, rest }] of [...purgeOffen]) {
      if (Sitzung?.abgelaufen()) break;
      const l = purgeLage(serverStand, boardId, cardId);
      /* F-3013 (#1141): ein Rest-Eintrag, dessen Karte noch lebt, bleibt still — dann ist
         Stehenlassen richtig (ADR-0136 Nachtrag 2026-11-11). */
      if (l === 'da') { purgeOffen.delete(url); if (!rest) verworfen++; continue; }
      if (l !== 'weg') continue;
      /* F-3019/F-3020 (#1143): Der Stand oben ist nur ein Vorfilter. Ob die Karte am
         Server wirklich fehlt, prüft der Server selbst (`nurVerwaist=1` → 409, wenn
         sie im gespeicherten Stand steht; 409 heißt hier 'verwerfen'). Ein veralteter
         oder zurückgehaltener Stand und ein Einzelflug über einen neueren Stand
         hinweg treffen so keine lebende Karte mehr. */
      const e = await purgeSenden(`${url}?nurVerwaist=1`);
      if (e === 'nochmal') break;
      purgeOffen.delete(url);
      if (!rest && (e === 'verboten' || e === 'verwerfen')) verworfen++; /* 403, 409, übrige 4xx */
    }
    /* F-3017 (#1142): Vorher hieß es „wird automatisch erneut versucht“ — wer den
       Eintrag jetzt verwirft, sagt es einmal (eine Meldung für den ganzen Lauf). */
    if (verworfen) Sitzung?.anhangPurgeVerworfen(verworfen);
  })().finally(() => { purgeNachholLauf = null; });
  return purgeNachholLauf;
};

/**
 * Erzeugt ein Attachment-Backend das die Server-REST-API nutzt.
 * @param {() => string|null} getBoardId  - Gibt die aktive Board-ID zurück
 * @param {() => string}      getBaseUrl  - Gibt die REST-Basis-URL zurück (ohne Slash am Ende)
 */
const createRESTAttachmentBackend = function createRESTAttachmentBackend(getBoardId, getBaseUrl, anstossen) {
  function attachPath(cardId, attId) {
    const boardId = getBoardId();
    if (!boardId) return null;
    const base = `${getBaseUrl()}/boards/${encodeURIComponent(boardId)}/attachments/${encodeURIComponent(cardId)}`;
    return attId ? `${base}/${encodeURIComponent(attId)}` : base;
  }

  /* Board-weite Anhänge (Hintergrund, Avatare) — eigener Endpunkt ohne
     Karten-id (Karte #443). Bis Sprint 61 gab es ihn nicht; deshalb reisten
     sie als base64 im Board-State und liefen gegen MaxBodyBytes. */
  function boardAttachPath(attId) {
    const boardId = getBoardId();
    if (!boardId) return null;
    const base = `${getBaseUrl()}/boards/${encodeURIComponent(boardId)}/board-attachments`;
    return attId ? `${base}/${encodeURIComponent(attId)}` : base;
  }

  async function fetchBoardBlob(att) {
    const url = boardAttachPath(att?.id);
    if (!url) return null;
    try {
      const res = await fetch(url);
      Sitzung?.meldeAntwort(res.status); /* F-2953 (#1120): Board-Anhang lesen */
      if (!res.ok) return null;
      return await res.blob();
    } catch { return null; }
  }

  /* Base64-Inline-Daten → Blob. Bewusst eine freie Funktion und keine Methode:
     als `this.readAttachment` aufgerufen bräche der Pfad, sobald ein Aufrufer
     die Backend-Methode destrukturiert — und genau das tun mehrere. */
  async function base64Blob(att) {
    try {
      /* Ausnahme F-2977: data:-URL, kein Server-Aufruf, kein 401 möglich. */
      const res = await fetch(`data:${Attachments.safeMime(att.mime)};base64,${att.data}`);
      return res.ok ? await res.blob() : null;
    } catch { return null; }
  }

  async function fetchBlob(card, att) {
    const url = attachPath(card.id, att.id);
    if (!url) return null;
    try {
      const res = await fetch(url);
      Sitzung?.meldeAntwort(res.status); /* F-2953 (#1120): Anhang lesen */
      if (!res.ok) return null;
      return await res.blob();
    } catch { return null; }
  }

  return {
    async uploadFile(file, card) {
      const url = attachPath(card.id, null);
      if (!url) throw new Error('no-board-id');
      const attId = crypto.randomUUID();
      const fd = new FormData();
      fd.append('file', file);
      fd.append('attachmentId', attId);
      const res = await fetch(url, { method: 'POST', body: fd });
      Sitzung?.meldeAntwort(res.status); /* F-2830: Anhang-Upload */
      if (!res.ok) throw new Error(`upload-failed: HTTP ${res.status}`);
      return res.json(); /* { id, filename, mime, size, created } */
    },

    async readAttachment(card, att) {
      /* Base64-Inline-Daten (importierte Standalone-Boards, Board-Background,
         und seit Karte #450 vor allem: jeder Anhang aus der Zeit vor dem
         Umbau). Immer zuerst — solche Anhänge liegen auf dem Server unter
         keiner id. */
      if (att.data) return base64Blob(att);
      return fetchBlob(card, att);
    },

    safeMime:  (...a) => Attachments.safeMime(...a),
    humanSize: (...a) => Attachments.humanSize(...a),

    async attachmentUrl(card, att) {
      /* Base64-Fallback (z. B. aus Standalone-Import) */
      if (att.data) return `data:${Attachments.safeMime(att.mime)};base64,${att.data}`;
      const blob = await fetchBlob(card, att);
      return blob ? URL.createObjectURL(blob) : null;
    },

    async deleteAttachment(card, att) {
      const url = attachPath(card.id, att.id);
      if (!url) return true; /* kein Board-Bezug: am Server gibt es nichts zu löschen */
      /* F-2978 (#1131): Ergebnis an den Aufrufer. true = Datei weg (404 = war schon weg),
         false = Server hat nicht gelöscht (401/5xx/Netz/409) — der Aufrufer behält den Anhang,
         'verboten' = 403 (F-2990): keine Berechtigung, Wiederholen hilft nicht. */
      try {
        const res = await fetch(url, { method: 'DELETE' });
        Sitzung?.meldeAntwort(res.status); /* F-2953 (#1120): Anhang löschen */
        if (res.ok || res.status === 404) return true;
        return res.status === 403 ? 'verboten' : false; /* F-2990: 403 = keine Berechtigung, Wiederholen hilft nicht */
      } catch { return false; }
    },

    async downloadAttachment(card, att) {
      /* Karte #450: `att.data` ZUERST — genau wie `readAttachment` und
         `attachmentUrl` es tun. Ohne diesen Zweig war der Download der
         einzige der drei Lesewege, der Bestands-Anhänge nicht fand: Alles,
         was vor dem Umbau als base64 in den State geschrieben wurde (und im
         Server-Modus war das ALLES), liegt auf dem Server unter keiner id —
         `fetchBlob` liefert 404, der Download meldete stumm `false`. */
      const blob = att.data ? await base64Blob(att) : await fetchBlob(card, att);
      if (!blob) return false;
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = att.filename || 'datei';
      a.click();
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
      return true;
    },

    async purgeCardAttachments(cardId) {
      const url = attachPath(cardId, null);
      if (!url) return true;
      /* F-2978 (#1131): true/false wie deleteAttachment; die Karte ist dann schon weg,
         der Aufrufer meldet nur, dass Anhänge am Server übrig blieben.
         F-2988 (#1135): 'nochmal' kommt auf die Liste für die Wiederholung (nur im Tab,
         höchstens PURGE_OFFEN_MAX; abgearbeitet beim nächsten Server-Stand, main.js).
         F-2990/F-3011: 'verboten' (403) und 'verwerfen' (übrige 4xx) werden nicht
         wiederholt — der Aufrufer meldet sie als „bleibt am Server“. */
      const ergebnis = await purgeSenden(url);
      /* F-3013 (#1141, ADR-0136 Nachtrag 2026-11-11): Dateien fremder Kommentare blieben,
         weil die Karte am Server noch stand. Merken und nach dem Speichern mit
         `nurVerwaist=1` nachholen — ohne Hinweis (der Nutzer hat nichts falsch gemacht), mit
         gebündeltem Anstoß (F-3029). Liste voll → bleibt wie bisher. */
      if (ergebnis === 'rest') {
        if (purgeOffen.has(url) || purgeOffen.size < PURGE_OFFEN_MAX) {
          purgeOffen.set(url, { boardId: getBoardId(), cardId, rest: true });
          /* Kritiker S116 F-3029: Kommt die Antwort erst nach dem Abruf, der aufs Speichern folgt,
             brächte sonst nichts mehr einen Server-Stand. Wie bei 'nochmal' gebündelt anstoßen. */
          try { anstossen?.(); } catch { /* Anstoß ist Zugabe */ }
        }
        return true;
      }
      if (ergebnis === 'nochmal') {
        if (!purgeOffen.has(url) && purgeOffen.size >= PURGE_OFFEN_MAX) return 'verworfen';
        purgeOffen.set(url, { boardId: getBoardId(), cardId });
        /* F-3017 (#1142): Scheitert der Sofort-Purge erst, nachdem der Server-Stand schon
           da ist, käme das Nachholen erst mit der nächsten Board-Änderung. Einmal
           anstoßen (kein Polling, keine Schleife): ein frischer Server-Stand ohne ETag. */
        try { anstossen?.(); } catch { /* Anstoß ist Zugabe */ }
        return false;
      }
      return ergebnis === 'ok' ? true : (ergebnis === 'verboten' ? 'verboten' : 'verworfen');
    },

    /* Board-Anhänge reisen seit Karte #443 über `POST /boards/{id}/board-attachments`
       — eigener Endpunkt, eigenes (höheres) Limit, nichts davon im Board-State.
       Der zurückgegebene Anhang trägt bewusst KEIN `data`-Feld. */
    async uploadBoardFile(file) {
      const url = boardAttachPath(null);
      if (!url) throw new Error('no-board-id');
      const attId = crypto.randomUUID();
      const fd = new FormData();
      fd.append('file', file);
      fd.append('attachmentId', attId);
      const res = await fetch(url, { method: 'POST', body: fd });
      Sitzung?.meldeAntwort(res.status); /* F-2830: Anhang-Upload */
      if (!res.ok) throw new Error(`upload-failed: HTTP ${res.status}`);
      return res.json(); /* { id, filename, mime, size, created } */
    },

    /* `att.data` ZUERST — und das bleibt so. Board-Anhänge aus der Zeit vor
       Karte #443 liegen als base64 im State und auf dem Server unter keiner
       id; ein Leseweg ohne diesen Zweig fände sie nicht. Genau diese Hälfte
       war beim Karten-Umbau die Lücke (F-844). */
    async boardAttachmentUrl(att) {
      if (att?.data) return `data:${Attachments.safeMime(att.mime)};base64,${att.data}`;
      const blob = await fetchBoardBlob(att);
      return blob ? URL.createObjectURL(blob) : null;
    },

    /* Karte #450-Nacharbeit: Der Renderer braucht für den Board-Hintergrund
       eine blob:-URL (er verwaltet deren Lebensdauer über Render-Generationen,
       eine data:-URL wäre dort ein stiller Speicherfresser). Diese Variante
       fehlte im Interface, weshalb `renderer.js` sie direkt aus
       `attachments.js` importierte und damit am Backend vorbeigriff. Heute
       folgenlos — Board-Anhänge tragen weiterhin `att.data` —, aber genau die
       Annahme hebt Karte #443 auf. */
    async boardAttachmentObjectUrl(att) {
      const blob = att?.data ? await base64Blob(att) : await fetchBoardBlob(att);
      return blob ? URL.createObjectURL(blob) : null;
    },

    /* Rückgabe `true`/`false` — der Vertrag aus F-871: Wer `false` bekommt,
       darf den Eintrag NICHT aus `board.attachments` entfernen, sonst liegt
       die Datei unreferenziert unter `<board>/__board__/` und es gibt keinen
       Aufräumweg mehr (die Purge-Route ist für die Board-Ablage seit Karte
       #443 bewusst gesperrt, `DeleteBoard` greift erst beim Löschen des ganzen
       Boards). Bis Sprint 62 verschluckte diese Funktion jeden Fehler und der
       Aufrufer entfernte den Eintrag in jedem Fall — genau so entstanden die
       verwaisten Dateien.

       404 zählt als Erfolg: Der Server ist an dieser Stelle idempotent, „schon
       weg" ist das gewünschte Ergebnis, nicht ein Fehlschlag. */
    async deleteBoardAttachment(att) {
      /* Bestandsdaten liegen nur im State — dort gibt es nichts zu löschen;
         der Aufrufer entfernt den Eintrag aus `board.attachments`. */
      if (att?.data) return true;
      const url = boardAttachPath(att?.id);
      if (!url) return false;
      try {
        const res = await fetch(url, { method: 'DELETE' });
        Sitzung?.meldeAntwort(res.status); /* F-2953 (#1120): Board-Anhang löschen */
        return res.ok || res.status === 404;
      } catch { return false; }
    },

    /* ── F-835 / Karte #450: die drei Zeilen, die das Gegenteil dessen
       behaupteten, was direkt darüber passiert ──────────────────────────

       Hier stand bis Sprint 61:

         boardSizeLabel()      → ''   „Server-Volume — keine localStorage-Quota"
         computeBoardBytes(_b) → 0    „keine Base64-Attachments im State"
         checkUpload()         → {ok:true}  „Server setzt Limit via 413"

       Alle drei sind falsch, und die mittlere widerlegt `uploadBoardFile`
       vierzig Zeilen weiter oben, das selbst `att.data = base64` setzt. Der
       Board-Hintergrund liegt in dieser Phase als base64 IM STATE und geht
       damit gegen `MaxBodyBytes` — genau die Grenze, an der der gemeldete Upload
       gescheitert ist. Und „Server setzt Limit via 413" beschreibt keinen
       Schutz, sondern seinen Ausfall: Der Nutzer sieht „Speichern
       fehlgeschlagen", nachdem er gewartet hat, statt vorher zu erfahren,
       dass die Datei zu gross ist.

       Diese Kommentare waren die eigentliche Vorbedingung des Umbaus: Wer die
       Aufrufer auf das Plugin umstellt, ohne sie anzufassen, verliert wortlos
       jede Schutzmassnahme aus Karte #450. */

    /* Karten-Anhänge reisen seit Karte #450 per Multipart an
       `POST /boards/{id}/attachments/{cardId}` — eigener Endpunkt, eigenes
       (höheres) Limit, nichts davon im Board-State. */
    kartenAnhangImBoardState: () => false,
    /* Board-Anhänge seit Karte #443 ebenfalls nicht mehr: `uploadBoardFile`
       schickt sie an `/boards/{id}/board-attachments` und gibt einen Anhang
       OHNE `data` zurück. Der Schalter ist die Stelle, an der die Grenzen am
       Transportweg hängen (F-845) — er steuert die Board-Grössenprüfung in
       `checkBoardUpload`. Nicht mehr steuert er die Bildverkleinerung: Die
       hat einen eigenen Zweck und ist in `configdialog.js` entkoppelt. */
    boardAnhangImBoardState:  () => false,
    /* Verkleinert wird trotzdem — und hier liegt der Unterschied zum
       Transportweg-Schalter darüber: Ein Avatar wird mit 40 px Kante
       gezeigt, ein Hintergrund als Fläche. Über REST lädt JEDER
       Betrachter das Original, bei jedem Aufruf. Punkt 3 der Karte
       hätte die Verkleinerung mit dem Transportweg abgeschaltet; das
       wäre eine Regression gewesen, die niemand als solche sieht —
       sie zeigt sich nur als langsames Laden. */
    boardBildVerkleinern:    () => true,

    boardSizeLabel:    (...a) => Attachments.boardSizeLabel(...a),
    computeBoardBytes: (...a) => Attachments.computeBoardBytes(...a),

    /* Karten-Anhang: Pro-Datei-Prüfung ja (sonst sieht der Nutzer das 413 des
       Attachment-Endpunkts erst nach dem Hochladen), Board-Grössenprüfung
       nein — die Datei trägt zum State nichts bei. Genau hier zogen Schritt 1
       und Schritt 3 der Kartenbeschreibung gegeneinander; die Auflösung ist
       nicht „eins von beidem", sondern die Unterscheidung, welche der beiden
       Prüfungen für welchen Transportweg etwas aussagt. */
    checkUpload(file, board) {
      /* Pro-Datei-Limit gegen das Limit des ATTACHMENT-Endpunkts (50 MB),
         nicht gegen `attachmentMaxBytes` (2 MB, für den base64-Weg gedacht).
         Die erste Fassung dieses Umbaus reichte an `Attachments.checkUpload`
         durch und lehnte damit das 4-MB-Foto ab — den Anlassfall der Karte,
         nur mit anderem Meldungstext. */
      return Attachments.checkUpload(file, board, {
        imBoardState: false,
        maxBytes: Config.SERVER_ATTACHMENT_MAX_FILE_BYTES,
      });
    },

    /* Board-Anhang: seit Karte #443 derselbe Weg wie der Karten-Anhang, also
       auch dieselbe Prüfung — Pro-Datei gegen das Limit des Attachment-
       Endpunkts, keine Board-Grössenprüfung. Wäre diese Zeile beim Umbau
       stehengeblieben, hätte der Nutzer weiterhin die 2-MB-Absage für eine
       Datei bekommen, die der Server mit 50 MB annimmt: genau der Fall aus
       F-845, in dem der neue Transportweg die alten Grenzen erbte. */
    checkBoardUpload(file, board) {
      return Attachments.checkUpload(file, board, {
        imBoardState: false,
        maxBytes: Config.SERVER_ATTACHMENT_MAX_FILE_BYTES,
      });
    },
  };
};

/* ── REST-Plugin ─────────────────────────────────────────────────────── */
/* Wrappt die REST-API des Server-Backends. Polling via GET /boards/{id}
   mit ETag/If-None-Match (304 = kein Update). Save seit Karte #565 über
   POST /boards/{id}/ops (ADR-0129 §4, Nachtrag Sprint 72); PUT mit If-Match
   (409 = Konflikt, Body enthält Server-State + neuen ETag für Karte #77)
   bleibt für Import, Anlage, „Meine Fassung“ und die Rückfälle. */

createRESTPlugin = function createRESTPlugin() {
  const s = {
    boardId: null,   /* gesetzt in preload(boardId) */
    etag:    null,   /* zuletzt bekannter ETag vom Server */
    timer:   null,   /* setInterval-Handle */
    active:  false,  /* Polling läuft */
    cb:      null,   /* Callback für externe Updates */
    /* Ops-Schreibweg (ADR-0129 §4, Nachtrag Sprint 72; Karte #565) */
    bezug:    null,  /* Bezug B: eigene tiefe Kopie, nie ein Objekt aus State */
    bezugGen: 0,     /* zählt jedes Setzen von B von AUSSEN (Laden, Übernahme) */
    laeuft:   null,  /* Promise des laufenden Speicherns — höchstens eines je Tab */
    gesperrt: false, /* F-1453: Boot ohne Server-Board — Speichern verweigert (kein 428-Ins-Leere) */
    ganz:     false, /* nächstes Speichern als PUT (JSON-Import, Punkt 6) */
    fehlschlaege: 0, /* aufeinanderfolgende 429/503/Netzfehler, für die Wartezeit */
    angebot:  null,  /* #566: zuletzt geholter Stand + seine ETag, noch nicht übernommen */
  };

  function baseUrl() {
    return (Config.get('serverUrl') ?? '').replace(/\/$/, '');
  }

  function resolvedIntervalMs() {
    return Math.max(5_000, Math.min(60_000, (Config.get('serverPollInterval') ?? 10) * 1_000));
  }

  async function poll() {
    /* Karte #565: Kein Poll, solange ein Speichern läuft. Ein Stand, der
       mitten im POST übernommen wird, setzte B auf einen Server-Stand ohne
       die gerade gesendeten Ops; das anschliessende B := S verlöre dann die
       Übernahme, und der nächste Diff schickte fremde Änderungen als
       Rücknahme zurück (knowledge §33). Das Speichern holt selbst nach. */
    if (s.laeuft) return;
    return hole();
  }

  /* `ohneEtag` (F-3017, #1142): Vollabruf ohne If-None-Match, damit ein unveränderter
     Stand nicht als 304 verschwindet (Anstoß fürs Nachholen gemerkter Purges). */
  async function hole(ohneEtag = false) {
    if (!s.boardId || !s.cb) return;
    try {
      const headers = {};
      if (s.etag && !ohneEtag) headers['If-None-Match'] = s.etag;
      const res = await fetch(`${baseUrl()}/boards/${encodeURIComponent(s.boardId)}`, { headers });
      /* #1053: 401 im OIDC-Betrieb pausiert das Senden; jede andere Antwort hebt
         die Pause auf (der Poll ist die Wiederaufnahme-Probe). */
      if (Sitzung?.meldeAntwort(res.status)) return;
      if (res.status === 304) return; /* nicht geändert — kein Update */
      if (!res.ok) return;
      const newEtag = res.headers.get('ETag');
      const state = await res.json();
      if (!state?.board) return;
      /* #566 (ADR-0129 §4, Punkt 3; knowledge §33): Die ETag gehört zum
         Bezug. Sie wird erst festgeschrieben, wenn main.js den Stand wirklich
         übernimmt (bezugUebernehmen mit genau diesem Objekt). Lehnt main.js
         ab, weil L ungesicherte Änderungen trägt, fragt der nächste Poll mit
         der alten ETag und bekommt den Stand erneut — sonst hiesse es 304,
         und der Browser sähe ihn nie. */
      s.angebot = { roh: state, etag: newEtag };
      s.cb(state);
    } catch { /* Netzwerkfehler ignorieren — nächster Poll-Versuch folgt */ }
  }

  function restartTimer(ms) {
    clearInterval(s.timer);
    s.timer = setInterval(poll, ms);
  }

  function onVisibilityChange() {
    if (!s.active) return;
    /* Tab im Hintergrund: doppeltes Intervall (Battery-Friendliness) */
    restartTimer(document.visibilityState === 'hidden' ? resolvedIntervalMs() * 2 : resolvedIntervalMs());
  }

  /* F-251 / ADR-0053: Der REST-Plugin erhält seinen ETag sonst nur per Poll
     (erster Tick erst nach ≥5 s) oder aus einer PUT-Response. Wer vorher (oder
     bei dauerhaft klemmendem Polling) speichert, sendet kein If-Match → Server
     antwortet 428 → der Save ging bisher STILL verloren („Container speichert
     keine Änderungen"). Daher: ETag bei Bedarf per GET nachziehen und 428/412
     mit genau einem ETag-Refresh + Retry abfangen statt zu verwerfen. */
  async function fetchEtag(id) {
    try {
      const res = await holen(`${baseUrl()}/boards/${encodeURIComponent(id)}`); /* F-2977 */
      if (!res.ok) return false;
      const e = res.headers.get('ETag');
      if (e) { s.etag = e; return true; }
    } catch { /* Netzwerkfehler — Aufrufer wertet das Endergebnis aus */ }
    return false;
  }

  function sendPut(id, state) {
    const headers = { 'Content-Type': 'application/json' };
    if (s.etag) headers['If-Match'] = s.etag;
    return holen(`${baseUrl()}/boards/${encodeURIComponent(id)}`, {
      method: 'PUT', headers, body: JSON.stringify(state),
    });
  }

  function sendPost(state) {
    return holen(`${baseUrl()}/boards`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(state),
    });
  }

  /* ADR-0054 (F-253, L1): Board serverseitig anlegen. Genutzt vom
     Bootstrap (0 Boards) und als putBoard-404-Fallback (Board serverseitig
     weg). 201 → ok + ETag; 409 (id existiert bereits — Race) → einmaliger
     PUT-Fallback OHNE erneutes POST (Endlos-Pingpong-Schutz); sonst
     {ok:false}. */
  async function postBoard(state) {
    const id = state?.board?.id;
    if (!id) return { ok: false };
    let res;
    try {
      res = await sendPost(state);
    } catch { return { ok: false }; }
    if (Sitzung?.meldeAntwort(res.status)) return { ok: false, sitzungAbgelaufen: true }; /* #1053, Kritik B-2 */
    if (res.status === 409) {
      s.etag = null;
      return putBoard(state, { allowPostFallback: false });
    }
    if (!res.ok) return { ok: false };
    const newEtag = res.headers.get('ETag');
    if (newEtag) s.etag = newEtag;
    return { ok: true };
  }

  async function putBoard(state, { allowPostFallback = true } = {}) {
    const id = state?.board?.id;
    if (!id) return { ok: false };
    /* B1: ohne bekannten ETag zuerst einen holen — sonst garantiert 428. */
    if (!s.etag) await fetchEtag(id);
    let res;
    try {
      res = await sendPut(id, state);
      /* B2: fehlendes/veraltetes If-Match → ETag frisch holen, genau einmal
         retryen. 409 bleibt echter Konflikt und wird unten behandelt. */
      if (res.status === 428 || res.status === 412) {
        s.etag = null;
        if (await fetchEtag(id)) res = await sendPut(id, state);
      }
    } catch { return { ok: false }; }
    if (Sitzung?.meldeAntwort(res.status)) return { ok: false, sitzungAbgelaufen: true }; /* #1053 */
    /* ADR-0054 (F-253): Board serverseitig nicht (mehr) vorhanden — der
       Server prüft If-Match VOR der Existenz, ein 404 kommt also nur mit
       gültigem ETag (Board nach Adoption gelöscht). Statt still scheitern:
       neu anlegen. Guard verhindert POST→409→PUT→404→POST-Schleife. */
    if (res.status === 404 && allowPostFallback) {
      s.etag = null;
      return postBoard(state);
    }
    if (res.status === 409) {
      /* Konflikt: Server-State + ETag für Karte #77 (Konflikt-Dialog) zurückliefern */
      const serverState = await res.json().catch(() => null);
      const newEtag = res.headers.get('ETag');
      if (newEtag) s.etag = newEtag;
      return { ok: false, conflict: { serverState, etag: newEtag } };
    }
    if (res.status === 403 || res.status === 413 || res.status === 422) {
      /* F-1622: Der Server hat den Inhalt abgelehnt (Kommentar-Löschrecht,
         Verlaufsschutz, Finding-Prüfung, Grösse, Rolle). Den Grund
         weiterreichen — ein erneuter Versuch mit demselben Stand ändert
         daran nichts, „bitte erneut versuchen“ wäre falsch. */
      const j = await res.json().catch(() => null);
      return { ok: false, abgewiesen: { status: res.status, grund: typeof j?.error === 'string' ? j.error : '' } };
    }
    if (!res.ok) return { ok: false };
    /* SR-1 (F-1685): Hat der Server den Stand verändert (Finding umnummeriert,
       Kommentare gestempelt), ist der gesendete Stand NICHT der Server-Stand.
       Dann die alte ETag behalten: Der nächste Poll bekommt den echten Stand
       und bietet ihn zur Übernahme an — sonst hiesse es 304, und der Browser
       zeigte z. B. eine alte Finding-Nummer, bis jemand anders schreibt. */
    /* ADR-0140 §10 (Karte #778): Der Server hat „vertraulich“ stehen
       lassen, das der PUT abschalten wollte. VOR dem frühen return lesen —
       ein bewahrter Wert ändert den Stand, und dann greift genau der. */
    const bewahrt = leseVertraulichBewahrt(res.headers.get('X-Aboard-Vertraulich-Bewahrt'));
    const zusatz = bewahrt ? { vertraulichBewahrt: bewahrt } : {};
    if (res.headers.get('X-Aboard-Stand-Geaendert') === '1') return { ok: true, geaendert: true, ...zusatz };
    const newEtag = res.headers.get('ETag');
    if (newEtag) s.etag = newEtag;
    return { ok: true, ...zusatz };
  }

  /* ── Speichern über Ops (ADR-0129 §4, Nachtrag Sprint 72; Karte #565) ──
     Statt des ganzen Boards gehen die Ops aus `diffToOps(B, L)` an
     `POST /boards/{id}/ops`. Der Server faltet sie auf seinen aktuellen
     Stand; zwei Schreiber stossen damit nicht mehr per 409 zusammen. Der PUT
     bleibt für Import, Anlage, „Meine Fassung“ und die Rückfälle (Punkt 6). */

  function klon(x) { return JSON.parse(JSON.stringify(x)); }

  /* B von aussen setzen: Laden, Wechseln, Anlegen, Übernahme (Punkt 1). Roh,
     vor checkVersion — sonst verschwinden Lade-Migrationen aus dem Diff. */
  function setzeBezug(roh) {
    s.bezug = roh?.board ? klon(roh) : null;
    s.bezugGen++;
    if (roh && s.angebot?.roh === roh && s.angebot.etag) s.etag = s.angebot.etag;
    s.angebot = null;
  }

  function sendOps(id, ops) {
    return holen(`${baseUrl()}/boards/${encodeURIComponent(id)}/ops`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ops }),
    });
  }

  /* 429/503/Netz: L bleibt ungesichert, der nächste Versuch folgt nach einer
     Wartezeit (Punkt 4) — `Retry-After`, sonst verdoppelnd bis 60 s. */
  function spaeter(res) {
    s.fehlschlaege++;
    const ra = Number(res?.headers?.get?.('Retry-After'));
    const ms = ra > 0 ? ra * 1000 : 2000 * 2 ** Math.min(s.fehlschlaege - 1, 5);
    return { ok: false, erneutNachMs: Math.min(ms, 60_000) };
  }

  /* PUT des Schnappschusses S. Nach Erfolg ist S der Server-Stand, und die
     ETag der Antwort beschreibt genau ihn — hier darf beides übernommen werden. */
  async function speichereGanz(S, gen) {
    const r = await putBoard(S);
    if (r.ok) {
      s.ganz = false;
      s.fehlschlaege = 0;
      if (gen === s.bezugGen) s.bezug = S;
    }
    return r;
  }

  /* Höchstens ein Speichern je Tab (Punkt 2). Wer kommt, während eines
     läuft, wartet und rechnet danach gegen den nachgeführten B — sonst
     schickte er die Ops des ersten noch einmal. Rückweg über `finally` für
     jeden Ausgang (knowledge §6). */
  async function nacheinander(fn) {
    /* F-1453: Nach gescheitertem Boot kennt der Server das lokale Board nicht;
       ein Schreibversuch liefe in ein dauerhaftes 428 und sähe aus wie
       „gleich nochmal“. Ohne Request ablehnen, Zustand bleibt dirty. */
    if (s.gesperrt) return { ok: false, gesperrt: true };
    /* #1053: abgelaufene Sitzung — nichts senden, der Zustand bleibt dirty.
       Die Wiederaufnahme kommt über 'sitzung:wieder-da' (main.js). */
    if (Sitzung?.abgelaufen()) return { ok: false, sitzungAbgelaufen: true };
    while (s.laeuft) await s.laeuft;
    let fertig;
    s.laeuft = new Promise(r => { fertig = r; });
    try { return await fn(); }
    finally { s.laeuft = null; fertig(); }
  }

  /* Ein Protokolleintrag in Worten: welche Karte, welcher Grund. Die Nummer
     kommt aus S, weil die Karte am Server womöglich schon fehlt. */
  function beschreibe(e, S, batch) {
    const karten = (S.board.sprints || []).flatMap(sp => sp.cards || []);
    const k = e?.cardId ? karten.find(c => c.id === e.cardId) : null;
    const d = { art: String(e?.art || ''), cardId: e?.cardId || null, displayId: k?.displayId ?? null, titel: k?.title || '' };
    /* Karte #604 (ADR-0130 §2): Beim Feldkonflikt beide Fassungen mitgeben —
       die am Server (`aktuell` aus dem Protokoll) und die eigene (`value` der
       abgelehnten Op, gefunden über den Index im Batch). */
    if (d.art === 'feld-geaendert') {
      const op = Number.isInteger(e?.index) ? batch?.[e.index] : null;
      d.feld = typeof e?.feld === 'string' ? e.feld : null;
      d.aktuell = e?.aktuell;
      d.mein = op?.payload?.value;
    }
    return d;
  }

  /* F-1576: Wer eine eigene Op beobachtet (KI-Verlauf „Als Kommentar
     übernehmen“), erfährt, welche Ops der Server nicht angenommen hat: je Op
     Typ, Karte und bei comment.add die Kommentar-id. Bei 403 ist es der ganze
     Batch. Nur ein Ereignis, am Ablauf ändert sich nichts. */
  function meldeAbgelehnt(boardId, status, ops) {
    EventBus.emit('ops:abgelehnt', { boardId, status, ops: ops.filter(Boolean).map(o => ({
      type: o.type, cardId: o.payload?.cardId ?? null, commentId: o.payload?.comment?.id ?? null,
    })) });
  }

  async function speichereUeberOps(state) {
    const S = klon(state);
    const id = S?.board?.id;
    if (!id) return { ok: false };
    const gen = s.bezugGen;
    /* Kein Bezug (Boot ohne Server, fremdes Board im State) oder Import:
       ganz ersetzen, wie bisher. */
    if (s.ganz || !s.bezug || s.bezug.board?.id !== id) return speichereGanz(S, gen);

    const { ops, batches, zuGross } = bereiteOpsVor(s.bezug, S, '');
    if (!ops.length) return { ok: true };           /* leerer Diff: kein Request */
    if (zuGross.length) return speichereGanz(S, gen); /* einzelne Op > 900 KiB */

    let bezug = s.bezug;
    const abgelehnt = [];
    for (const batch of batches) {
      let res;
      try { res = await sendOps(id, batch); }
      /* Antwort verloren: B bleibt, der nächste Versuch sendet denselben
         Batch neu — der Server lässt No-op-Wiederholungen durch (#561). */
      catch { return spaeter(null); }
      if (Sitzung?.meldeAntwort(res.status)) return { ok: false, sitzungAbgelaufen: true }; /* #1053: B bleibt, nichts geht verloren */
      if (gen !== s.bezugGen) return { ok: true }; /* inzwischen gewechselt/übernommen */

      /* Was der Server von diesem Batch wirklich angenommen hat — nur das darf
         in den Bezug gefaltet werden (K-A2, Session-Kritiker Sprint 72). */
      let angenommen = batch;
      if (res.status === 422) {
        const body = await res.json().catch(() => null);
        const eintraege = Array.isArray(body?.protokoll) ? body.protokoll : [];
        const idx = new Set(eintraege.map(e => e?.index).filter(Number.isInteger));
        if (!idx.size) {
          /* 422 ohne Op-Bezug (Hülle verletzt): kein Konflikt, sondern ein
             Fehler im Diff — wie 400 behandeln. */
          console.error('aBoard: Ops abgelehnt ohne Protokoll, Rückfall auf PUT', body);
          return speichereGanz(S, gen);
        }
        /* Echter Konflikt: abgelehnte Ops heraus, den Rest EINMAL neu (Punkt 4).
           Kein automatischer PUT — er überschriebe genau die fremde Änderung. */
        for (const e of eintraege) abgelehnt.push(beschreibe(e, S, batch));
        const rest = batch.filter((_, i) => !idx.has(i));
        angenommen = [];
        if (!rest.length) meldeAbgelehnt(id, 422, eintraege.map(e => batch[e?.index]));
        if (rest.length) {
          let r2 = null;
          try { r2 = await sendOps(id, rest); } catch { /* nichts mehr automatisch */ }
          if (r2 && Sitzung?.meldeAntwort(r2.status)) return { ok: false, sitzungAbgelaufen: true }; /* #1053 */
          if (r2?.ok) angenommen = rest;
          else abgelehnt.push({ art: 'rest', cardId: null, displayId: null, titel: '' });
          /* F-1576: abgelehnt ist, was der Server genannt hat, und der Rest,
             wenn auch er scheiterte. */
          meldeAbgelehnt(id, 422, [...eintraege.map(e => batch[e?.index]), ...(r2?.ok ? [] : rest)]);
          if (gen !== s.bezugGen) return { ok: true, abgelehnt, meineFassung: S };
        }
      } else if (res.status === 429 || res.status === 503) {
        return spaeter(res);
      } else if (!res.ok) {
        /* 400/404/405/413 und Unerwartetes: Fehler im Diff oder im Vokabular,
           kein Konflikt. Genau ein Rückfall auf den PUT mit If-Match; derselbe
           Batch wird nicht wiederholt (Punkt 4). */
        const text = await res.text?.().catch(() => '') ?? '';
        console.error(`aBoard: Ops-Speichern mit ${res.status} abgelehnt, Rückfall auf PUT`, text);
        /* F-1576: 403 = Rolle am Board reicht nicht (z. B. nach Herabstufung). */
        if (res.status === 403) meldeAbgelehnt(id, 403, batch);
        return speichereGanz(S, gen);
      }
      /* B nachführen (Punkt 5). Bei mehreren Batches nur um das Angenommene:
         Eine abgelehnte Op (z. B. `klemmung`) würde lokal gefaltet etwas
         anderes ergeben als am Server, wo der Batch atomar verworfen wurde.
         Läuft die Schleife durch, setzt `B := S` unten ohnehin den Endstand;
         relevant ist das, wenn ein späterer Batch mit Netzfehler abbricht. */
      bezug = batches.length > 1 ? fuehreBezugNach(bezug, angenommen) : S;
      s.bezug = bezug;
    }
    /* Alle Batches durch: B := S (Punkt 1). Die ETag der POST-Antwort bleibt
       liegen — sie beschreibt einen Stand, den der Browser nie gesehen hat
       (Punkt 2, knowledge §33). Das sofortige GET danach stösst main.js an
       (`holeJetzt`), nachdem es seine Änderungs-Generation nachgeführt hat —
       erst dann weiss es, ob L seit dem Absenden unverändert ist (#566). */
    s.bezug = S;
    s.fehlschlaege = 0;
    return abgelehnt.length
      ? { ok: true, gepostet: true, abgelehnt, meineFassung: S }
      : { ok: true, gepostet: true };
  }

  /* #1053: Probe für „Weiter senden“/Fokus bei abgelaufener Sitzung: eine
     leichte Anfrage; geliefert wird nur der Statuscode (null = Netz weg). */
  Sitzung?.setzeProbe(async () => {
    /* Ausnahme F-2977: /me-Probe der Betriebsart (klaereBetriebsart wertet den 401 selbst aus). */
    try { return (await fetch(`${baseUrl()}/me`, { cache: 'no-store' })).status; } catch { return null; }
  });

  /* F-2830: Betriebsart über /me klären, wenn ein 401 vor dem ersten /me kommt. */
  Sitzung?.setzeBetriebsartAbruf(async () => {
    /* Ausnahme F-2977: /me-Abruf der Betriebsart, meldet selbst. */
    const res = await fetch(`${baseUrl()}/me`, { cache: 'no-store' });
    return { status: res.status, body: await res.json().catch(() => null) };
  });

  const plugin = {
    kind: 'rest',

    /* Karte #564 (ADR-0129 §4, Nachtrag Sprint 72, Punkte 1, 5, 7): die
       Op-Schicht für den Ops-Schreibweg; `save`/`autoPersist` nutzen sie seit
       #565 (speichereUeberOps oben).
       `bereiteOpsVor(bezug, lokal, actor)` → { ops, batches, zuGross }
       `fuehreBezugNach(bezug, batch)`      → fold(bezug, batch) wie am Server */
    bereiteOpsVor,
    fuehreBezugNach,

    async init() {
      /* Phase 2: Auth-Setup, Token-Refresh */
    },

    /* F-1453: Boot ohne Server-Board (Liste nicht ladbar) — alle
       Schreibwege lehnen ab, bis ein Server-Board geladen ist (switchBoard). */
    sperreSpeichern() { s.gesperrt = true; },

    async destroy() {
      plugin.stopPolling();
      s.gesperrt = false;
      s.etag    = null;
      s.boardId = null;
      s.bezug   = null;
      s.bezugGen++;
    },

    async preload(boardId) {
      /* Board-ID für Polling und PUT-Requests merken — wird in init() vor
         Storage.load() aufgerufen; boardId kommt aus dem embedded JSON. */
      s.boardId = boardId ?? null;
    },

    load() {
      /* Initial-Load: aus Embedded-JSON / localStorage-Fallback — wie andere Plugins.
         Erster Poll holt danach den aktuellen Server-State. */
      return Storage.load();
    },

    async save(state) {
      const result = await nacheinander(() => speichereUeberOps(state));
      if (result.ok) return { ...result, method: 'server' };
      if (result.conflict) return { ok: false, conflict: result.conflict };
      if (result.gesperrt) return { ok: false, gesperrt: true }; /* F-1453 */
      if (result.sitzungAbgelaufen) return { ok: false, sitzungAbgelaufen: true }; /* #1053 */
      if (result.abgewiesen) return { ok: false, abgewiesen: result.abgewiesen }; /* F-1622 */
      return { ok: false, erneutNachMs: result.erneutNachMs };
    },

    /* Karte #565: Ganz-Board-PUT mit If-Match — „Meine Fassung trotzdem
       speichern“ und „Meine Version behalten“ im Konflikt-Dialog (Punkt 6).
       Über `save` ginge das inzwischen als Ops hinaus und wäre keine bewusste
       Ersetzung mehr. */
    async speichereGanz(state) {
      const result = await nacheinander(() => speichereGanz(klon(state), s.bezugGen));
      if (result.ok) {
        /* Karte #778: den Hinweis „vertraulich bewahrt“ weiterreichen — dies
           ist der Weg „Meine Version behalten“. */
        return result.vertraulichBewahrt
          ? { ok: true, method: 'server', vertraulichBewahrt: result.vertraulichBewahrt }
          : { ok: true, method: 'server' };
      }
      if (result.conflict) return { ok: false, conflict: result.conflict };
      if (result.sitzungAbgelaufen) return { ok: false, sitzungAbgelaufen: true }; /* #1053 */
      if (result.abgewiesen) return { ok: false, abgewiesen: result.abgewiesen }; /* F-1622 */
      return { ok: false };
    },

    /* Karte #604: „Meine Fassung übernehmen“ beim Feldkonflikt — nur die
       genannten Felder, als card.field. Danach holt main.js den Stand. */
    async ueberschreibeFelder(eintraege) {
      return nacheinander(async () => {
        const id = s.bezug?.board?.id || s.boardId;
        const ops = bedingungsloseFeldOps(eintraege, '');
        if (!id || !ops.length) return { ok: false };
        try {
          const res = await sendOps(id, ops);
          Sitzung?.meldeAntwort(res.status); /* #1053 */
          return { ok: res.ok, status: res.status };
        } catch { return { ok: false }; }
      });
    },

    /* Karte #606 (ADR-0130 §1): fremde Übernahme aufheben — card.release mit
       erzwingen und der Grund als Kommentar, in EINEM Batch: beides wirkt
       oder keins. Der Server lässt erzwingen nur für Menschen zu. Danach holt
       main.js den Stand. */
    async hebeUebernahmeAuf(ops) {
      return nacheinander(async () => {
        const id = s.bezug?.board?.id || s.boardId;
        if (!id || !ops?.length) return { ok: false };
        try {
          const res = await sendOps(id, ops);
          Sitzung?.meldeAntwort(res.status); /* #1053 */
          /* Karte #618: 422 halter-geaendert → den Halter, den der Server
             jetzt kennt, mitgeben (null = keine Übernahme mehr). */
          if (res.status === 422) {
            const body = await res.json().catch(() => null);
            const e = (Array.isArray(body?.protokoll) ? body.protokoll : []).find(x => x?.art === 'halter-geaendert');
            if (e) {
              return { ok: false, status: 422, halterAktuell: e.halter
                ? { by: e.halter, session: e.session ?? null, since: e.since ?? null, until: e.until ?? null } : null };
            }
          }
          return { ok: res.ok, status: res.status };
        } catch { return { ok: false }; }
      });
    },

    /* Karte #565: Ein Server-Stand wurde übernommen — B folgt ihm (Punkt 1).
       Aufgerufen von main.js, weil erst dort feststeht, ob übernommen wurde
       (Modal offen → zurückgehalten). */
    bezugUebernehmen(roh) { setzeBezug(roh); },

    /* Karte #565: Das nächste Speichern ersetzt das Board ganz (JSON-Import). */
    naechstesGanz() { s.ganz = true; },

    /* #566: Das GET nach einem angenommenen POST (Punkt 2). Kein eigenes,
       solange ein Speichern läuft — das holt danach selbst. */
    async holeJetzt() { if (!s.laeuft) await hole(); },

    parseAndMigrate(raw) {
      try { return Storage.checkVersion(JSON.parse(raw)); } catch { return null; }
    },

    async autoPersist(state) {
      const result = await nacheinander(() => speichereUeberOps(state));
      if (result.ok) return result.abgelehnt || result.gepostet || result.vertraulichBewahrt ? result : true;
      /* Konflikt-Info zurückgeben, damit debouncedPersist den Banner zeigen kann */
      if (result.conflict) return { ok: false, conflict: result.conflict };
      if (result.gesperrt) return { ok: false, gesperrt: true }; /* F-1453 */
      if (result.sitzungAbgelaufen) return { ok: false, sitzungAbgelaufen: true }; /* #1053 */
      if (result.abgewiesen) return { ok: false, abgewiesen: result.abgewiesen }; /* F-1622 */
      return result.erneutNachMs ? { ok: false, erneutNachMs: result.erneutNachMs } : false;
    },

    async switchBoard(boardId) {
      try {
        const res = await holen(`${baseUrl()}/boards/${encodeURIComponent(boardId)}`); /* F-2977 */
        /* 401 bei abgelaufener Sitzung: Band steht schon, keine irreführende „no-handle“-Meldung.
           Ist die Betriebsart noch offen, erst klären (Kritiker S114 K-6) — sonst kam „no-handle“ vor dem Band. */
        if (res.status === 401 && Sitzung) {
          await Sitzung.klaereBetriebsart();
          if (Sitzung.abgelaufen()) return { ok: false, sitzungAbgelaufen: true };
        }
        if (!res.ok) return { ok: false, error: 'no-handle' };
        const newEtag = res.headers.get('ETag');
        if (newEtag) s.etag = newEtag;
        const state = await res.json();
        if (!state?.board) return { ok: false, error: 'invalid-file' };
        s.boardId = boardId;
        s.gesperrt = false; /* F-1453: ein echtes Server-Board ist geladen */
        setzeBezug(state); /* #565: roh, vor checkVersion (Punkt 1) */
        return { ok: true, state };
      } catch { return { ok: false, error: 'no-handle' }; }
    },

    /* ADR-0054 (F-253): Server-Modus-Bootstrap. VOR dem Verwenden der lokal
       gewürfelten Board-id zuerst den Server fragen:
         • ≥1 Board → das `default` (sonst erstes) adoptieren — stabile id
           über Reloads (behebt L2). switchBoard() setzt boardId + ETag.
         • 0 Boards (alter Server ohne Server-Default-Board) → das lokale
           Board per POST anlegen, dessen id stabilisieren (behebt L1).
       Liefert { ok, state, adopted }. Bei ok=false soll der Aufrufer lokal
       weiterarbeiten (der nächste Save versucht es via putBoard-404→POST). */
    async adoptOrCreate(localState) {
      Sitzung?.klaereBetriebsart(); /* F-2830: /me beim Boot garantiert einmal */
      let list = [];
      /* F-1440: Ladefehler ≠ „keine Boards“ — sonst legte der Fehlerfall ein
         neues Board an. Bei Fehler lokal weiterarbeiten (ok:false). */
      try { list = await plugin.listKnownBoards(); } catch { return { ok: false }; }
      if (Array.isArray(list) && list.length > 0) {
        const pick = list.find(b => b.id === 'default') || list[0];
        const r = await plugin.switchBoard(pick.id);
        if (r.ok && r.state?.board) return { ok: true, state: r.state, adopted: true };
        return { ok: false };
      }
      const r = await postBoard(localState);
      if (r.ok) {
        s.boardId = localState?.board?.id ?? null;
        setzeBezug(localState); /* #565: angelegt = Server-Stand (Punkt 1) */
        return { ok: true, state: localState, adopted: false };
      }
      return { ok: false };
    },

    startPolling(cb) {
      plugin.stopPolling();
      if (!s.boardId) return;
      s.cb     = cb;
      s.active = true;
      restartTimer(resolvedIntervalMs());
      document.addEventListener('visibilitychange', onVisibilityChange);
    },

    stopPolling() {
      clearInterval(s.timer);
      s.timer  = null;
      s.active = false;
      s.cb     = null;
      document.removeEventListener('visibilitychange', onVisibilityChange);
    },

    isPollingSupported() { return true; },
    isPollingActive()    { return s.active; },

    /* F-1440: Fehler (Netz, !res.ok, kaputte Antwort) WERFEN — „leer“ und
       „nicht ladbar“ sind verschiedene Zustände. Aufrufer mit optionaler
       Liste (Umschalter, Backup-Auswahl) fangen und bleiben still leer; die
       Board-Übersicht zeigt den Fehlertext. */
    /* Karte #741: Vorschaubild (verkleinertes Hintergrundbild) für die
       Kachelansicht. 200 = JPEG, 204 = kein Hintergrund. */
    boardVorschauUrl(boardId) {
      return `${baseUrl()}/boards/${encodeURIComponent(boardId)}/vorschaubild`;
    },

    async listKnownBoards() {
      const res = await holen(`${baseUrl()}/boards`); /* F-2977 */
      if (!res.ok) throw new Error(`listKnownBoards: HTTP ${res.status}`);
      const items = await res.json();
      if (!Array.isArray(items)) throw new Error('listKnownBoards: unerwartete Antwort');
      /* Karte #201: role-Feld (owner/editor/viewer) wird vom Server mitgeliefert
         wenn Forward-Auth AN ist — im Legacy-Bypass fehlt es (undefined). */
      return items.map(b => ({ id: b.id, name: b.title, title: b.title, role: b.role, updatedAt: b.updated_at }));
    },

    /* Karte #201, ADR-0059: Identifizierten User vom Server abfragen.
       GET /me liefert { name, email } oder { name: null, email: null }
       im Legacy-Bypass. Kein Crash bei Netzwerkfehler — gibt null-Felder zurück. */
    async getCurrentUser() {
      try {
        const res = await fetch(`${baseUrl()}/me`);
        if (Sitzung?.meldeAntwort(res.status)) return { name: null, email: null, fehler: true }; /* #1053 */
        /* `fehler: true` unterscheidet einen Fehlschlag vom Legacy-Bypass
           (200 mit null-Feldern) — ladeKandidaten cacht nur Letzteren (VO-11). */
        if (!res.ok) return { name: null, email: null, fehler: true };
        const me = await res.json();
        /* #1053: Der Server meldet die Betriebsart; nur `oidc` schaltet den Ablauf bei 401 ein. */
        Sitzung?.setzeOIDC(me?.anmeldung === 'oidc');
        return me;
      } catch { return { name: null, email: null, fehler: true }; }
    },

    /* ADR-0056 (Karte #195): Neues Board auf dem Server anlegen.
       Erzeugt ein frisches v2-Board, legt es per POST /boards an und
       wechselt via switchBoard() darauf (boardId + ETag setzen).
       Liefert { ok, state } bei Erfolg, { ok: false, error } im Fehlerfall. */
    async createBoard(title) {
      const fresh = { version: '2', board: Model.createBoard(title || 'Board') };
      const postResult = await postBoard(fresh);
      if (!postResult.ok) return { ok: false, error: 'create-failed' };
      /* Nach erfolgreichem POST: ETag + boardId via switchBoard holen */
      const swResult = await plugin.switchBoard(fresh.board.id);
      if (!swResult.ok) return { ok: false, error: 'switch-after-create-failed' };
      return { ok: true, state: swResult.state };
    },

    attachments() {
      /* Karte #74: REST-Attachment-Backend — Upload/Download via Server-Volume */
      /* F-3017 (#1142): `hole(true)` fragt ohne If-None-Match — der Stand ist dem Client
         schon bekannt, ein 304 ließe `restEingang` (und damit das Nachholen) ausfallen. */
      return createRESTAttachmentBackend(() => s.boardId, baseUrl, () => {
        /* Gebündelt: mehrere gleichzeitige späte Fehlschläge (Karten-Mehrfachlöschen,
           main.js Promise.all) lösen zusammen nur EINEN Vollabruf aus. */
        if (s.anstossLaeuft) return;
        s.anstossLaeuft = true;
        /* Kritiker S116 F-3031: Läuft gerade ein Speichern, nicht überspringen, sondern es
           abwarten und danach holen — ein Speichern ohne Änderung holt selbst keinen Stand. */
        (async () => { while (s.laeuft) await s.laeuft; await hole(true); })()
          .catch(() => {}).finally(() => { s.anstossLaeuft = false; });
      });
    },

    /* ── ACL-Share-API (Karte #199, ADR-0059) ─────────────────────────
     * Drei Methoden für GET/POST/DELETE /boards/{id}/acl.
     * Nur im Server-Modus (REST-Plugin) implementiert — Standalone/Project
     * haben kein ACL-Konzept. Liefern [] / null / undefined bei Fehler. */

    async aclList(boardId) {
      try {
        const res = await fetch(`${baseUrl()}/boards/${encodeURIComponent(boardId)}/acl`);
        Sitzung?.meldeAntwort(res.status); /* F-2953 (#1120) */
        if (!res.ok) return [];
        return await res.json(); /* [{email, role, created}] */
      } catch { return []; }
    },

    /* Karte #699: Namen der Personen des Boards, für jede Rolle lesbar —
       ohne Adressen und Rollen (F-261 bleibt gewahrt). `null` heisst „nicht
       verfügbar“ (älterer Server, Fehler), damit der Aufrufer zurückfallen
       kann (dann ACL); ein leeres Array heisst „der Server kennt keine Personen mit
       Namen“ — der Aufrufer fällt dann NICHT auf die ACL zurück, sondern direkt
       auf die Personen aus dem Board (KO-8c). */
    async mitgliederNamen(boardId) {
      try {
        const res = await fetch(`${baseUrl()}/boards/${encodeURIComponent(boardId)}/mitglieder`);
        Sitzung?.meldeAntwort(res.status); /* F-2953 (#1120) */
        if (!res.ok) return null;
        const body = await res.json();
        return Array.isArray(body?.namen) ? body.namen.filter(n => typeof n === 'string') : null;
      } catch { return null; }
    },

    async aclSet(boardId, email, role) {
      try {
        const res = await fetch(`${baseUrl()}/boards/${encodeURIComponent(boardId)}/acl`, {
          method:  'POST',
          headers: { 'Content-Type': 'application/json' },
          body:    JSON.stringify({ email, role }),
        });
        Sitzung?.meldeAntwort(res.status); /* F-2953 (#1120) */
        return { ok: res.ok, status: res.status };
      } catch { return { ok: false, status: 0 }; }
    },

    /* #845 (F-2237): Board löschen — DELETE /boards/{id}. Das Recht („nur
       Eigentümer“) entscheidet der Server (403/404/500); hier steht nur der
       Aufruf. status 0 = Netz weg. */
    async deleteBoard(boardId) {
      try {
        const res = await fetch(`${baseUrl()}/boards/${encodeURIComponent(boardId)}`, { method: 'DELETE' });
        Sitzung?.meldeAntwort(res.status); /* F-2953 (#1120) */
        /* Kritiker S97 K-1: 409 nennt offene Rückweg-Änderungen einer Kopie.
           Verify S97: 404 heißt nur dann „schon weg“, wenn der Server selbst es
           sagt — ein 404 eines Proxys oder eines Servers ohne die Route nicht. */
        let offene = null, gone = false;
        if (res.status === 409 || res.status === 404) {
          let body = null;
          try { body = await res.json(); } catch { body = null; }
          if (res.status === 409) offene = Number.isFinite(body?.offene_aenderungen) ? body.offene_aenderungen : null;
          if (res.status === 404) gone = body?.error === 'board not found';
        }
        return { ok: res.ok, status: res.status, offene, gone };
      } catch { return { ok: false, status: 0 }; }
    },

    async aclDelete(boardId, email) {
      try {
        const res = await fetch(
          `${baseUrl()}/boards/${encodeURIComponent(boardId)}/acl?email=${encodeURIComponent(email)}`,
          { method: 'DELETE' },
        );
        Sitzung?.meldeAntwort(res.status); /* F-2953 (#1120) */
        return { ok: res.ok, status: res.status };
      } catch { return { ok: false, status: 0 }; }
    },

    /* ADR-0066 §3 (Karte #224): bekannte Nutzermenge vom Server laden.
     * GET /known-users → { users: [{email, name, status}] }
     * status: "aktiv" | "ausstehend"
     * Liefert { ok: false, status, users: [] } bei Fehler — kein Crash. */
    async knownUsers() {
      try {
        const res = await holen(`${baseUrl()}/known-users`); /* F-2977 */
        if (!res.ok) return { ok: false, status: res.status, users: [], domains: [] };
        const data = await res.json();
        /* ADR-0132: erlaubte Domains für „Alle mit @<domain>“. */
        return { ok: true, status: 200, users: data.users ?? [], domains: data.domains ?? [] };
      } catch { return { ok: false, status: 0, users: [], domains: [] }; }
    },

    /* #293/ADR-0091: Karte als Mail versenden — POST /boards/{b}/cards/{c}/email.
       payload = { to: [], cc?: [], subject?, message? }. Liefert
       { ok, status, body } analog Admin-Client. */
    async sendCardEmail(cardId, payload) {
      try {
        const res = await holen( /* F-2977 */
          `${baseUrl()}/boards/${encodeURIComponent(s.boardId)}/cards/${encodeURIComponent(cardId)}/email`,
          {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload || {}),
          });
        const body = await res.json().catch(() => null);
        return { ok: res.ok, status: res.status, body };
      } catch { return { ok: false, status: 0, body: null }; }
    },

    /* #296: Karte zwischen Boards verschieben — POST /boards/{src}/cards/{c}/transfer.
       Body { toBoardId, toColumnId }. Erfolg → { ok:true, body:{ newDisplayId, oldDisplayId } }.
       Backend (ADR-0067 §5): atomar (Card-Delete am src + Card-Insert in dst-__backlog__),
       UUID bleibt, displayId wird neu vergeben, Attachments via Volume-Saga (Etappe 1b).
       ACL: editor+ auf BEIDEN Boards. */
    async transferCard(cardId, toBoardId, toColumnId) {
      if (!s.boardId) return { ok: false, status: 0, body: null };
      try {
        const res = await holen( /* F-2977 */
          `${baseUrl()}/boards/${encodeURIComponent(s.boardId)}/cards/${encodeURIComponent(cardId)}/transfer`,
          {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ toBoardId, toColumnId }),
          });
        const body = await res.json().catch(() => null);
        return { ok: res.ok, status: res.status, body };
      } catch { return { ok: false, status: 0, body: null }; }
    },

    /* #296: Spalten-Defs eines Boards lesen — GET /boards/{id} ohne s.boardId-/etag-Mutation.
       Wird vom Modal-Transfer-Picker genutzt, um die ColumnDefs des Ziel-Boards für das
       Mapping-Dropdown zu holen. Liefert { ok, columnDefs?, title? }. */
    async peekBoardColumns(boardId) {
      try {
        const res = await holen(`${baseUrl()}/boards/${encodeURIComponent(boardId)}`); /* F-2977 */
        if (!res.ok) return { ok: false };
        const state = await res.json();
        const board = state?.board;
        if (!board) return { ok: false };
        return { ok: true, columnDefs: board.columnDefs || [], title: board.title || '' };
      } catch { return { ok: false }; }
    },
  };

  return plugin;
};

} /* Ende if (IS_SERVER_BUILD) — REST-Plugin-Block (Karte #203 / ADR-0064) */

/* ── Factory + Active-Plugin-Cache ──────────────────────────────────── */

const cache = { standalone: null, project: null, rest: null };

/**
 * Mapt einen boardMode-Wert (Profil-ID oder Legacy) auf die Plugin-Variante.
 * Server-Profil → 'rest' (REST-API-Polling statt FSA-Polling).
 * Claude-code-Profil → 'project' (FSA-Polling via Sync-Modul).
 * Solo/Team → 'standalone'.
 * Akzeptiert defensiv auch Legacy-Werte standalone/project.
 *
 * @param {string} kind
 * @returns {'standalone'|'project'|'rest'}
 */
function pluginKindFor(kind) {
  /* Legacy-Aliase explizit halten — falls vor Migration aufgerufen */
  if (kind === 'standalone') return 'standalone';
  if (kind === 'project')    return 'project';
  /* Server-Profil: feature.restStorage unterscheidet REST von FSA-Polling */
  if (Profile.has('feature.restStorage', kind)) return 'rest';
  /* Alle anderen Profile mit Polling-Capability → FSA-basiertes Project-Plugin */
  return Profile.has('feature.polling', kind) ? 'project' : 'standalone';
}

/**
 * Wählt das Plugin für den gegebenen Modus. Cacht die Instanzen pro Plugin-
 * Variante. Aufrufer dürfen das Ergebnis nicht behalten, sondern immer neu
 * via `getActivePlugin()` abholen, damit ein Modus-Wechsel sofort wirkt.
 *
 * @param {string} kind - Profil-ID ('solo'|'team'|'claude-code'|'server') oder Legacy ('standalone'|'project')
 * @returns {StoragePlugin}
 */
function selectPlugin(kind) {
  const variant = pluginKindFor(kind);
  if (!cache[variant]) {
    if (variant === 'rest')    cache[variant] = createRESTPlugin();
    else if (variant === 'project') cache[variant] = createProjectPlugin();
    else                       cache[variant] = createStandalonePlugin();
  }
  return cache[variant];
}

/* F-179: Cache-Invalidierung beim Modus-Wechsel — räumt den Lifecycle des bisherigen
   Plugins via destroy() ab, damit kein gecachtes Project-Plugin in Standalone
   weiterläuft. Wird bei jedem `getActivePlugin()` geprüft. */
let lastVariant = null;

/**
 * Liefert das Plugin, das zum aktuellen `Config.get('boardMode')` passt. Bei
 * Modus-Wechseln räumt das vorherige Plugin sich selbst auf (Polling stoppen +
 * Handles freigeben), bevor das neue zurückgegeben wird.
 *
 * @returns {StoragePlugin}
 */
function getActivePlugin() {
  const mode    = Config.get('boardMode');
  const variant = pluginKindFor(mode);
  if (lastVariant && lastVariant !== variant) {
    /* Modus-Wechsel: Lifecycle des alten Plugins beenden. destroy() ist async,
       wird hier fire-and-forget aufgerufen — Aufrufer brauchen das Resultat nicht,
       und destroy() ist idempotent + best-effort. */
    cache[lastVariant]?.destroy().catch(() => {});
  }
  lastVariant = variant;
  return selectPlugin(mode);
}

export const StoragePlugin = { selectPlugin, getActivePlugin };
export { anhangPurgeNachholen };

/* Test-Helper: erlaubt Tests, frische Plugin-Instanzen ohne Cache anzulegen.
   createRESTPlugin ist im Nicht-Server-Build null (IS_SERVER_BUILD=false → DCE). */
export const __testing__ = { createStandalonePlugin, createProjectPlugin, createRESTPlugin };

/* ADR-0140 §10 (Karte #778): Header `X-Aboard-Vertraulich-Bewahrt:
   <anzahl>; ids=<id>,…` lesen. Liefert { anzahl, ids } oder null (fehlt oder
   unlesbar). Die Anzahl zählt alle, die Liste ist auf 20 gekappt. */
export function leseVertraulichBewahrt(wert) {
  if (typeof wert !== 'string') return null;
  const m = /^\s*(\d+)\s*;\s*ids=([A-Za-z0-9_,-]*)\s*$/.exec(wert);
  if (!m) return null;
  const anzahl = Number(m[1]);
  if (!(anzahl > 0)) return null;
  return { anzahl, ids: m[2] ? m[2].split(',').filter(Boolean) : [] };
}
