import './style.css';
import { EventBus }    from './eventbus.js';
import { ansagen }     from './ansage.js'; /* F-2862: Live-Region vor dem ersten Hinweis */
import { Model }       from './model.js';
import { State }       from './state.js';
import { Storage }     from './storage.js';
import { Sync }        from './sync.js';   /* getDirectoryHandle für Projekt-Modus-Save */
import { StoragePlugin, createFsaAttachmentBackend, anhangPurgeNachholen } from './storage-plugin.js'; /* ADR-0044, Karte #67; F-2988 */
import { Sitzung } from './sitzung-abgelaufen.js'; /* #1053, Kritik B-1 (null außerhalb des Server-Builds) */
import { Renderer }    from './renderer.js';
import { DragDrop }    from './dragdrop.js';
import { Modal }       from './modal.js';
import { Export }      from './export.js';
import { Config }      from './config.js';
import { I18n }        from './i18n.js';
import { ConfigDialog } from './configdialog.js';
import { Filter }       from './filter.js';
import { Search }       from './search.js';
import { Markdown }    from './markdown.js'; /* eslint-disable-line no-unused-vars */
import * as Links       from './links.js';
import { spawnRecurringCards } from './recurrence.js'; /* F-169, Sprint 69; F-1103 */

/* Karte #450: Anhang-Operationen gehen über das Storage-Plugin, damit im
   Server-Modus das REST-Backend greift statt base64-im-State. Der
   `??`-Fallback ist defensiv und heute unerreichbar (F-853) — Herleitung und
   Begründung, warum er trotzdem bleibt, ausführlich in src/modal.js. */
function anhangBackend() {
  return StoragePlugin.getActivePlugin()?.attachments?.() ?? createFsaAttachmentBackend();
}
import { Activity }     from './activity.js';
import { Shortcuts }   from './shortcuts.js';
import { Profile }     from './profile.js';
import { BulkSelect }  from './bulk-select.js';
import { ohneGebundene } from './boardpaar.js'; /* F-2224 — nur gegatet benutzt */
import { Sidebar }     from './sidebar.js'; /* Karte #176 */
import { BootHinweis } from './boot-hinweis.js'; /* F-1453, nur Server-Build */
import { BoardLoeschen } from './board-loeschen.js'; /* #845, nur Server-Build */
/* Karte #467: Cover-Thumbnail — die Logik samt Race-Absicherung liegt in
   attachments.js und bekommt ihre Abhängigkeiten hier injiziert (die
   main.js-Handler selbst sind nicht exportiert und damit nicht prüfbar). */
import { reicheCoverThumbnailNach, entferneThumbnail } from './attachments.js';
import { CalendarView, oeffneTermin } from './calendar-view.js'; /* #308/ADR-0103; #743 */
import { SchattenAussen } from './schatten-aussen.js'; /* #781, ADR-0140 §5 — nur im Server-Build benutzt */
import { VorschlaegeAussen } from './vorschlaege-aussen.js'; /* #783, ADR-0140 §8 — nur im Server-Build benutzt */
import { setzeTermin, entferneTermin } from './calendar/termine.js'; /* #743, ADR-0138 §4 */
import { parseCardHash, findCardByRef, parseTerminRef } from './deeplink.js'; /* F-447/#366; #743 */
import * as NotificationTriggers from './notifications-triggers.js'; /* #339/#420 — ADR-0107 §1 */
import { eigeneFassungAlsText, kopiereEigeneFassung } from './feldkonflikt.js'; /* #604, K-1 */
import { makeOp, halterEinwand } from './ops.js';   /* Karte #606: card.release — nur in gegateten Zweigen benutzt */
import { FINDING_KIND, nextFindingNr } from './finding.js'; /* ADR-0133 §1, F-1547 — rein, ohne Importe */

/* Karte #203 / ADR-0064 / F-275: Server-Build-Compile-Konstante (Vite-Define).
   Bei __SERVER_BUILD__='false' (Standalone/Template) → false → esbuild eliminiert
   IS_SERVER_BUILD-gegatete Blöcke per DCE. Muster identisch zu sidebar.js:35 /
   storage-plugin.js:26. */
const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');
/* F-2978 (#1131): Karte ist beim Löschen schon weg; scheitert nur das Aufräumen der
   Server-Anhänge, kommt eine kurze Meldung — das Löschen selbst scheitert daran nie. */
/* F-2990: Ergebnisse (true | false | 'verboten' | 'verworfen') einer oder mehrerer Karten →
   höchstens EINE Meldung. false wird automatisch erneut versucht, 'verboten'/'verworfen' nicht. */
const anhangPurgeMelden = IS_SERVER_BUILD ? e => Sitzung.anhangPurgeMelden(e) : null;
/* #328: Projektor-Build-Flag analog ADR-0072. Im slim Public-Distributable
   ist Sync.isActive() definitionsgemäß false (sync.js gatet den Funktionsrumpf),
   aber esbuild kann den Funktionsaufruf nicht statisch DCE'n. Dieses
   IS_PROJECTOR_BUILD-Prefix vor jedem Sync.isActive()-Branch lässt
   den slim-Build die zugehörigen Spätzweige als toten Code entfernen. */
const IS_PROJECTOR_BUILD = (typeof __PROJECTOR_BUILD__ !== 'undefined') && (__PROJECTOR_BUILD__ === true || __PROJECTOR_BUILD__ === 'true');

/* ── ADR-0041: Wiederkehrende Karten ─────────────────────────────── */


/* spawnRecurringCards() lebt seit F-1103 in ./recurrence.js (importiert oben) —
   main.js und die Tests nutzen dieselbe Funktion statt einer zweiten Kopie. */

/* ── Hilfsmethoden ───────────────────────────────────────────────── */

/* Modul-globale debounced Persist-Funktion — wird in init() initialisiert,
   aber von Event-Handlern außerhalb von init() per flush() genutzt.
   Delegiert an das aktive Storage-Plugin (ADR-0044, Karte #67): Standalone
   schreibt in localStorage, Projekt in aboard-sync.json. */
let debouncedPersist     = null;

function debounce(fn, ms) {
  let timer, lastArgs;
  let laufend = null; /* nur Server-Build: Promise des gerade laufenden fn-Aufrufs */
  const wrapped = (...args) => {
    lastArgs = args;
    clearTimeout(timer);
    timer = setTimeout(() => {
      timer = null;
      const p = fn(...args);
      /* Karte #726 (Kritiker A-3): das laufende Speichern merken, damit eine
         Zwangsübernahme es abwarten kann — `cancel` erreicht es nicht mehr. */
      if (IS_SERVER_BUILD) {
        const q = Promise.resolve(p).catch(() => {}).finally(() => { if (laufend === q) laufend = null; });
        laufend = q;
      }
    }, ms);
  };
  wrapped.flush = async () => {
    if (timer) {
      clearTimeout(timer);
      timer = null;
      if (lastArgs) await fn(...lastArgs);
    }
  };
  /* Karte #726: ein geplantes Speichern verwerfen, dessen Stand überholt ist.
     Nur der Server-Build braucht es (Restore-Abschluss) — DCE im Template. */
  if (IS_SERVER_BUILD) wrapped.cancel = () => { clearTimeout(timer); timer = null; lastArgs = null; };
  /* Karte #726 (Kritiker A-3): wartet ein bereits gefeuertes Speichern ab. */
  if (IS_SERVER_BUILD) wrapped.idle = () => laufend ?? Promise.resolve();
  return wrapped;
}

function deepClone(obj) {
  return JSON.parse(JSON.stringify(obj));
}

/* ── Recovery-Banner (F-22, ADR-0106 #331) ────────────────────────── */

/* ADR-0106 (#331): Autosave-Restore gehärtet gegen das Diff-Bomben-Muster.
 *
 * Wahre Ursache (Bombe-Pfad 1): State.set(autosave) im FSA-Modus erzeugt
 * sofort einen Diff (autosave vs. aboard-sync.json) → diffToOps → 286+
 * card.delete + 8 swimlane.delete → Projektor faltet → Board zerstört.
 *
 * Gewählte Lösung — Option 1a:
 *   Im FSA-Modus (Sync.isActive() = migriertes Board mit Projektor): der
 *   „Laden"-Button wird vollständig entfernt. Nur Verwerfen bleibt übrig.
 *   Das eliminiert den gefährlichen State.set(autosave)-Pfad im Projekt-Modus
 *   komplett — der Autosave ist dort ohnehin veraltet (FSA-Stand ist autoritativ).
 *
 *   Im Standalone-Modus: Laden bleibt sicher (kein Sync-Diff-Risikopfad).
 *   State.set(autosave) → kein Projektor → kein Diff-Save → keine Bombe.
 *
 * FSA-Modus-Edge-Case: Banner zeigt auch ohne Laden-Button — der User kann
 * verwerfen, oder einfach weitermachen (Autosave wird beim nächsten
 * FSA-Save automatisch überschrieben/verworfen).
 */
/* Karte #1170: Der Browser-Zwischenstand-Hinweis ist nur im Standalone-Modus
   (ohne verbundenen Projektordner) eine echte Wahl. Ist der Projektordner
   verbunden und freigegeben (`projektAktiv`), ist dessen Stand maßgeblich —
   dann wird gar nicht erst gefragt, sondern der Projektordner geladen (siehe
   Aufrufer); die Browser-Kopie wird erst verworfen, wenn das gelungen ist. */
function recoveryBannerNoetig(fromLocalStorage, state, projektAktiv, freigabeFehlt) {
  /* F-3101/#1175: Ordner bekannt, Freigabe offen (`isPermissionNeeded`, auch
     „hängend“): `isActive()` ist false, aber das ist KEIN Standalone-Modus —
     „Laden“ setzte die alte Kopie in den State des Projekt-Boards (ADR-0106).
     Dann zeigt allein der Freigabe-Banner; die Kopie bleibt unangetastet. */
  return !!(fromLocalStorage && state && !projektAktiv && !freigabeFehlt);
}

/* Karte #1170 / Verify V-2 (#1175): Ladeweg im Projekt-Modus, als Funktion
   herausgelöst und damit testbar. Liefert der Ordner ein Board (Fold vor
   Projektion), gilt es und die Browser-Kopie geht still weg; sonst Hinweis,
   State unverändert, Kopie bleibt. */
async function uebernehmeOrdnerStand(state) {
  let ausOrdner = null;
  try {
    /* Kritik 20.11. K-2: dieselbe Quellenreihenfolge wie im Pfad
       `!state && isActive` (F-795/#449) — auf einem migrierten Board ist
       `fold(base + inbox)` die Wahrheit, die Projektion nur Abschrift.
       Sonst wäre der Diff-Bezug (`markAdopted`) eine veraltete Projektion. */
    if (await Sync.isMigrated()) {
      const gefaltet = await Sync.projectorReconcile();
      if (gefaltet?.board) ausOrdner = Storage.checkVersion(gefaltet);
    }
    if (!ausOrdner) {
      const raw = await Sync.read();
      if (raw && raw !== Sync.READ_ERROR) {
        const syncData = StoragePlugin.getActivePlugin().parseAndMigrate(raw);
        if (syncData?.board) ausOrdner = syncData;
      }
    }
  } catch { ausOrdner = null; }
  if (ausOrdner) {
    State.set(ausOrdner);
    State.markClean();
    Sync.markAdopted(State.get());
    if (state.board?.id) Storage.discardAutosave(state.board.id);
  } else {
    showRecoveryBanner(state);
    State.markClean();
  }
}

function showRecoveryBanner(autosaveState) {
  /* Im FSA-Modus: Laden-Button ist gesperrt (Bombe-Schutz, ADR-0106).
     F-428: das #328-`IS_PROJECTOR_BUILD`-Gate war hier falsch. FSA ist
     Basis-Funktionalität und in JEDEM Build vorhanden — nur die Projektor-
     SCHICHT (ops/diff/inbox) ist slim-DCE't. Seit F-427 ist claude-code auch
     im slim Template erreichbar; mit Gate wäre `fsaActive` dort immer false
     und der Laden-Button trotz aktivem FSA-Handle freigegeben → genau der
     ADR-0106-Bombe-Pfad. */
  const fsaActive = Sync.isActive();

  const banner = document.createElement('div');
  banner.className = 'recovery-banner';

  const msg = document.createElement('span');
  /* Karte #1170: Im Projekt-Modus gibt es nichts zu entscheiden — klarer Text.
     Kritik 20.11. K-6: Der Hinweis erscheint dort nur noch, wenn der
     Projektordner gerade KEIN Board lieferte (unlesbar, fehlt). Dann ist die
     Browser-Kopie womöglich der einzige Stand — sie bleibt erhalten. */
  msg.textContent = I18n.t(fsaActive ? 'recovery.msg-projekt' : 'recovery.msg');

  banner.appendChild(msg);

  if (!fsaActive) {
    /* Standalone-Modus: Laden ist sicher (kein Sync-Diff-Risikopfad) */
    const loadBtn = document.createElement('button');
    loadBtn.className = 'btn btn-primary';
    loadBtn.textContent = I18n.t('recovery.load');
    loadBtn.addEventListener('click', () => {
      banner.remove();
      State.set(autosaveState);
    });
    banner.appendChild(loadBtn);
  } else {
    /* FSA-Modus: kein Laden-Button — Autosave-Laden würde Board zerstören.
       Konsolen-Hinweis für Debugging. */
    console.info('aBoard Recovery: Autosave-Laden im Projekt-Modus gesperrt (ADR-0106/Bombe-Schutz). Die Browser-Kopie bleibt erhalten, bis der Projektordner wieder ein Board liefert (Karte #1170).');
  }

  const discardBtn = document.createElement('button');
  discardBtn.className = 'btn';
  discardBtn.textContent = I18n.t(fsaActive ? 'recovery.ok' : 'recovery.discard');
  discardBtn.addEventListener('click', () => {
    banner.remove();
    /* K-6: im Projekt-Modus nur schließen, nichts verwerfen (siehe oben). */
    if (!fsaActive && autosaveState?.board?.id) {
      Storage.discardAutosave(autosaveState.board.id); /* ADR-0087: deployment-gescopt (F-318) */
    }
  });

  banner.appendChild(discardBtn);
  document.body.prepend(banner);
}

/* ── State-Mutationen ────────────────────────────────────────────── */

function mutateBoard(fn) {
  const state = deepClone(State.get());
  fn(state);
  State.set(state);
}

/* Findet eine Karte über alle Sprints */
function findCard(state, cardId) {
  for (const sprint of state.board.sprints) {
    const idx = sprint.cards.findIndex(c => c.id === cardId);
    if (idx !== -1) return { sprint, idx };
  }
  return null;
}

/* ── Event-Handler ───────────────────────────────────────────────── */

/* Karte #743 (ADR-0138 §4): Termine ohne Karte aus der Kalenderansicht. Nur
   im Server-Build — der Diff (src/diff.js) macht daraus termin.upsert bzw.
   termin.delete. Im Solo-Template fallen beide Handler weg. */
if (IS_SERVER_BUILD) {
  EventBus.on('termin:speichern', (termin) => {
    mutateBoard(state => setzeTermin(state.board, termin));
  });
  EventBus.on('termin:loeschen', (id) => {
    mutateBoard(state => entferneTermin(state.board, id));
  });
}

EventBus.on('card:quick-add', ({ sprintId, colId, title, template, dueDate, finding }) => {
  /* #339 / Sprint-59-Kritiker [K] P2: Eine über ein Template angelegte Karte
     kann von Anfang an einen Owner haben (ADR-0040). Ohne die Ableitung hier
     bekäme dieser Owner NIE ein card.assigned — nicht weil ihm eine Adresse
     fehlte, sondern weil dieser Anlage-Pfad die Trigger gar nicht erst
     berührte. Die reine Funktion behandelt `alt = null` ausdrücklich als
     „alles neu", und ein Test deckte das ab — nur rief es aus der Anwendung
     heraus niemand mit `null` auf. Ein Test für eine Funktion ist kein Test
     für ihren Aufrufer. */
  let notifEvents = [];
  let notifBoard = null;
  let notifCard = null;
  mutateBoard(state => {
    const sprint = state.board.sprints.find(s => s.id === sprintId)
                ?? state.board.sprints.find(s => s.id === '__backlog__');
    if (sprint) {
      const displayId = Model.assignDisplayId(state.board);
      const card = Model.createCard(title, colId, displayId);
      /* #308/ADR-0103: Quick-Add aus der Kalender-Ansicht setzt dueDate vor. */
      if (dueDate) card.dueDate = dueDate;
      /* ADR-0040: Template-Felder auf neue Karte übertragen wenn Template gewählt */
      if (template) {
        card.description = template.description || '';
        card.labels      = Array.isArray(template.labels) ? [...template.labels] : [];
        card.owner       = template.owner       || '';
        card.complexity  = template.complexity  ?? '';
        card.effort      = template.effort      ?? null;
      }
      /* ADR-0133 §1: Finding — Nummer mit derselben Funktion wie im Fold
         (src/finding.js nextFindingNr, F-1547). Vor `kind` setzen: die neue
         Karte liegt noch nicht im Board, zählt also nicht mit. */
      if (finding) {
        const nr = nextFindingNr(state.board);
        card.kind = FINDING_KIND;
        card.finding = { nr, typ: 'B', prioritaet: 'P2', status: 'offen' };
      }
      sprint.cards.push(card);
      if (IS_SERVER_BUILD) {
        notifBoard = state.board;
        notifCard  = card;
        notifEvents = NotificationTriggers.ereignisseFuerAenderung(null, card, state.board);
      }
    }
  });
  if (IS_SERVER_BUILD && notifEvents.length) {
    NotificationTriggers.sendeEreignisse(notifBoard?.id, notifBoard, notifCard, notifEvents);
  }
});

/* card:add-cancel: inline restore in renderCardAddForm — kein Re-Render nötig (F-39) */

EventBus.on('card:updated', updatedCard => {
  /* #339/#420: Die Benachrichtigungs-Ereignisse werden IM mutateBoard
     abgeleitet — nur dort liegen alter und neuer Zustand nebeneinander —,
     aber erst DANACH verschickt. Innerhalb der Mutation zu senden hiesse,
     einen Netzaufruf in den Persistenzpfad zu hängen. */
  let notifEvents = [];
  let notifBoard = null;
  mutateBoard(state => {
    const found = findCard(state, updatedCard.id);
    if (!found) return;
    const oldCard = found.sprint.cards[found.idx];
    if (IS_SERVER_BUILD) {
      notifBoard = state.board;
      notifEvents = NotificationTriggers.ereignisseFuerAenderung(oldCard, updatedCard, state.board);
    }
    /* ADR-0037: Änderungen tracken — activity[] übernehmen aus updatedCard,
       dann diff gegen alte Karte laufen lassen (nur lokale Mutationen, kein Sync-Polling) */
    const author = Config.get('userName') || '';
    Activity.diffAndRecord(oldCard, updatedCard, author);
    found.sprint.cards[found.idx] = updatedCard;
  });
  if (IS_SERVER_BUILD && notifEvents.length) {
    NotificationTriggers.sendeEreignisse(notifBoard?.id, notifBoard, updatedCard, notifEvents);
  }
});

EventBus.on('card:deleted', cardId => {
  /* ADR-0036: Sprint-Meta-Karten dürfen nicht gelöscht werden */
  const currentState = State.get();
  const metaCard = currentState?.board?.sprints
    .flatMap(s => s.cards)
    .find(c => c.id === cardId && c.kind === 'sprint-meta');
  if (metaCard) {
    showUnlockedToast(I18n.t('sprint-meta.cannot-delete'));
    return;
  }

  /* ADR-0033: Attachment-Ablage der Karte vor der State-Mutation entsorgen.
     Fire-and-forget — der State darf nicht auf I/O warten.
     Karte #450: über das Plugin, nicht direkt. Im Server-Modus liegen die
     Anhänge seit dem Umbau auf dem Server; ein direkter Aufruf räumte nur den
     FSA-Ordner und liesse die Server-Dateien als Waisen zurück. */
  (IS_SERVER_BUILD ? anhangBackend().purgeCardAttachments(cardId).then(e => anhangPurgeMelden([e]))
    : anhangBackend().purgeCardAttachments(cardId)).catch(() => {}); /* F-2978: Standalone ohne .then (Template-Größe) */
  mutateBoard(state => {
    /* ADR-0032: Rückverweise auf die zu löschende Karte aufräumen,
       bevor die Karte selbst entfernt wird */
    Links.purgeLinksTo(state.board, cardId);
    for (const sprint of state.board.sprints) {
      sprint.cards = sprint.cards.filter(c => c.id !== cardId);
    }
  });
});

/* ADR-0032: Karten-Verweise anlegen/entfernen — Symmetrie wird im Links-Modul gepflegt */
EventBus.on('card:link-add', ({ sourceId, targetId, type }) => {
  mutateBoard(state => {
    Links.addLink(state.board, sourceId, targetId, type);
  });
});

EventBus.on('card:link-remove', ({ sourceId, targetId, type }) => {
  mutateBoard(state => {
    Links.removeLink(state.board, sourceId, targetId, type);
  });
});

/* Karte #606 (ADR-0130 §1): „Übernahme aufheben“ aus dem Karten-Modal. Zwei
   Ops in EINEM Batch — card.release mit erzwingen und der Grund als Kommentar
   —, damit es keine aufgehobene Übernahme ohne Begründung gibt. Kein State-
   Diff: diffToOps überträgt `claim` nie. Das Modal blendet die Zeile erst aus,
   wenn `fertig(true)` kommt (knowledge §41: kein Erfolg ohne Beleg). */
EventBus.on('card:claim-release', async ({ cardId, halter, grund, erwartet, fertig }) => {
  if (IS_SERVER_BUILD || IS_PROJECTOR_BUILD) {
    const autor = Config.get('userName') || 'User';
    const kommentar = { id: crypto.randomUUID(), author: autor,
      text: I18n.t('claim.kommentar', halter, grund), created: new Date().toISOString().slice(0, 10) };
    const ops = [
      /* Karte #618 (F-1257): `expect` = der Halter, den das Modal zeigte.
         Hält inzwischen ein anderer (oder keiner mehr), wirkt der Batch nicht
         — auch der Grund-Kommentar nicht — und das Modal zeigt den aktuellen. */
      makeOp('card.release', erwartet ? { cardId, erzwingen: true, expect: erwartet }
        : { cardId, erzwingen: true }, autor),
      makeOp('comment.add', { cardId, comment: kommentar }, autor),
    ];
    let ok = false;
    let aktuell;   /* undefined = kein Halter-Befund; sonst Objekt oder null */
    /* Verify V-2: fertig() in finally — sonst bliebe das Sperr-Flag im Modal
       (claimLaeuft) nach einer Ausnahme stehen (knowledge §6). */
    try {
      if (IS_SERVER_BUILD) {
        const plugin = StoragePlugin.getActivePlugin();
        if (typeof plugin?.hebeUebernahmeAuf === 'function') {
          const r = await plugin.hebeUebernahmeAuf(ops).catch(() => null);
          ok = !!r?.ok;
          if (!ok && r && 'halterAktuell' in r) aktuell = r.halterAktuell;
          plugin.holeJetzt?.();
        }
      } else {
        /* Lokal gibt es keinen atomaren Batch: die Bedingung vor dem Anhängen
           gegen den frisch gefalteten Stand prüfen, damit kein Grund-Kommentar
           ohne Aufhebung entsteht. Die Op trägt sie trotzdem (Restfenster). */
        const r = await Sync.haengeOpsAn(ops, erwartet
          ? projektion => halterEinwand(projektion, cardId, erwartet) : null).catch(() => false);
        ok = r === true;
        if (r && typeof r === 'object' && 'halterAktuell' in r) aktuell = r.halterAktuell;
      }
      showExportToast(ok ? I18n.t('claim.aufgehoben')
        : aktuell === undefined ? I18n.t('claim.fehlgeschlagen')
        : aktuell ? I18n.t('claim.halter-geaendert', (aktuell.by || '?')
            + (aktuell.session ? I18n.t('claim.sitzung', aktuell.session) : ''))
        : I18n.t('claim.nicht-mehr'));
    } finally {
      fertig?.(ok, aktuell);
    }
    return;
  }
  fertig?.(false);
});

/* ADR-0033: Attachments live persistieren — Metadaten landen sofort im State,
   Binärdaten wurden vorher schon in FSA bzw. Base64 abgelegt. */
EventBus.on('card:attachment-add', ({ cardId, attachment }) => {
  mutateBoard(state => {
    const found = findCard(state, cardId);
    if (!found) return;
    const card = found.sprint.cards[found.idx];
    if (!Array.isArray(card.attachments)) card.attachments = [];
    if (card.attachments.some(a => a.id === attachment.id)) return; /* idempotent */
    card.attachments.push(attachment);
  });
});

EventBus.on('card:attachment-remove', ({ cardId, attachmentId }) => {
  mutateBoard(state => {
    const found = findCard(state, cardId);
    if (!found) return;
    const card = found.sprint.cards[found.idx];
    if (!Array.isArray(card.attachments)) return;
    card.attachments = card.attachments.filter(a => a.id !== attachmentId);
    /* Cover-Referenz aufräumen wenn der Cover-Anhang gelöscht wird */
    if (card.coverAttachmentId === attachmentId) card.coverAttachmentId = null;
  });
});

/* Karte #62: Cover-Anhang setzen oder entfernen.
 *
 * Karte #467: Beim Setzen entsteht zusätzlich ein Thumbnail (420 px) am
 * Anhang-Objekt, aus dem der Renderer das Cover synchron zeichnet. Es wird
 * NUR hier erzeugt, nicht beim Upload jedes Bildes — ein Anhang, der nie
 * Cover wird, soll den Board-State nicht belasten.
 *
 * Zwei Eigenschaften, die zusammengehören:
 * - Die Cover-Zuweisung läuft SOFORT und synchron. Das Thumbnail kommt
 *   danach in einem zweiten `mutateBoard`. Wer das Cover setzt, sieht es
 *   also unverzüglich (über den bisherigen asynchronen Weg) und bekommt das
 *   Thumbnail nachgereicht — statt auf das Verkleinern zu warten.
 * - Schlägt die Erzeugung fehl oder gibt es nichts zu verkleinern, bleibt
 *   das Feld weg. Der Renderer fällt dann auf das Original zurück; ein
 *   fehlendes Thumbnail ist ein normaler Zustand, kein Fehler. */
EventBus.on('card:cover-set', ({ cardId, attachmentId }) => {
  mutateBoard(state => {
    const found = findCard(state, cardId);
    if (!found) return;
    const card = found.sprint.cards[found.idx];
    const vorher = card.coverAttachmentId;
    card.coverAttachmentId = attachmentId ?? null;

    /* Thumbnail des ABGEWÄHLTEN Covers wegräumen (Kritiker-Befund Sprint 63).
       Ohne das bleibt es für immer am Anhang stehen: Wer bei fünf Bildern das
       Cover durchprobiert und sich für keins entscheidet, hinterlässt fünf
       verwaiste Thumbnails im Board-State — sichtbar in keiner Oberfläche,
       aber gezählt von `computeBoardBytes` und mitgeschickt bei jedem PUT.
       Nur `card:attachment-remove` räumte bisher auf, und das auch nur, weil
       dort der ganze Anhang verschwindet. */
    if (vorher && vorher !== attachmentId) entferneThumbnail(card, vorher);
  });
  if (attachmentId) coverThumbnailNachreichen(cardId, attachmentId);
});

/* Reine Verdrahtung — die Logik samt Race-Absicherung steht in
   `reicheCoverThumbnailNach` (src/attachments.js) und ist dort mit
   injizierten Abhängigkeiten geprüft. Bewusst „fire and forget": Der
   Aufrufer oben wartet nicht auf das Verkleinern. */
function coverThumbnailNachreichen(cardId, attachmentId) {
  reicheCoverThumbnailNach(cardId, attachmentId, {
    findeKarte: (id) => {
      const gefunden = findCard(State.get(), id);
      return gefunden ? gefunden.sprint.cards[gefunden.idx] : null;
    },
    leseBlob: (karte, att) => anhangBackend().readAttachment(karte, att),
    /* Deckelt den Zuwachs durch das Thumbnail selbst. `checkUpload` sieht
       diesen Weg nicht — er läuft nur beim Hochladen einer Datei. */
    passtNochRein: (thumbBase64) => {
      const backend = anhangBackend();
      const roh = Math.floor((thumbBase64.length * 3) / 4);
      const pruefung = backend.checkUpload(
        { name: 'thumbnail', size: roh, type: 'image/webp' },
        State.get()?.board,
        { imBoardState: true, maxBytes: Number.POSITIVE_INFINITY },
      );
      /* Eine WARNUNG darf das Thumbnail nicht verhindern — sie richtet sich an
         den Nutzer, und der hat hier nichts hochgeladen. Nur die harte Grenze
         (`ok: false`) hält es zurück. */
      return pruefung.ok !== false;
    },
    mutiere: (fn) => mutateBoard(state => {
      const gefunden = findCard(state, cardId);
      fn(gefunden ? gefunden.sprint.cards[gefunden.idx] : null);
    }),
  }).catch(() => {});
}

/* ADR-0040: Template-Verwaltung (Karte #65)
   template:save   — neue Template aus Karten-Skelett anlegen
   template:update — bestehendes Template in board.templates aktualisieren
   template:delete — Template aus board.templates entfernen
   template:saved-toast — kurze Bestätigung nach Speichern */

EventBus.on('template:save', (skelett) => {
  mutateBoard(state => {
    if (!Array.isArray(state.board.templates)) state.board.templates = [];
    state.board.templates.push({
      id:          crypto.randomUUID(),
      isTemplate:  true,
      title:       skelett.title       || '',
      description: skelett.description || '',
      labels:      Array.isArray(skelett.labels) ? [...skelett.labels] : [],
      owner:       skelett.owner       || '',
      complexity:  skelett.complexity  ?? '',
      effort:      skelett.effort      ?? null,
    });
  });
});

EventBus.on('template:update', (updated) => {
  mutateBoard(state => {
    if (!Array.isArray(state.board.templates)) return;
    const idx = state.board.templates.findIndex(t => t.id === updated.id);
    if (idx === -1) return;
    state.board.templates[idx] = {
      ...state.board.templates[idx],
      title:       updated.title       || '',
      description: updated.description || '',
      labels:      Array.isArray(updated.labels) ? [...updated.labels] : [],
      owner:       updated.owner       || '',
      complexity:  updated.complexity  ?? '',
      effort:      updated.effort      ?? null,
    };
  });
});

EventBus.on('template:delete', ({ templateId }) => {
  mutateBoard(state => {
    if (!Array.isArray(state.board.templates)) return;
    state.board.templates = state.board.templates.filter(t => t.id !== templateId);
  });
});

EventBus.on('hinweis', showExportToast); /* F-2763: kurze Rückmeldung, z. B. gesperrter Drop */

EventBus.on('template:saved-toast', (title) => {
  showExportToast(I18n.t('template.saved-toast', title));
});

/* ── Bulk-Aktionen (Karte #63) ───────────────────────────────────── */

/**
 * Bulk-Move: Verschiebt alle selektierten Karten in eine andere Spalte
 * und/oder einen anderen Sprint. Sprint-Meta-Karten werden übersprungen.
 *
 * Parameter: { toColId?: string, toSprintId?: string }
 * Ohne toColId bleibt die Spalte der jeweiligen Karte erhalten.
 */
/* Karte #826 (Opus-Kritik O-1, Verify W-2): Gespiegelte Lanes einer externen
   Kopie sind kein Ziel. Die einzige Sperre dafür ist der Filter im Menü
   „Verschieben“ (src/renderer.js, Mehrfachauswahl-Leiste) — wer `bulk:move`
   von einer weiteren Stelle auslöst, muss dort ebenso filtern. */
/* F-2224 (Karte #827 E4, Sitzungs-Kritik K-1): Gebundene Karten einer externen
   Kopie (Spiegel oder Karte in einer gespiegelten Lane) gehören nicht in eine
   Massenaktion — der Server wiese Löschen oder Verschieben in eigene Spalten ab.
   Die Auswahl sperrt sie beim Wählen (renderer.js, shortcuts.js), aber eine
   eigene Karte kann NACH dem Wählen gebunden werden (ins Rückweg-Ziel gezogen).
   Deshalb hier, an der einen Stelle, die alle bulk:*-Handler lesen: solche
   Karten fallen aus der Auswahl, bevor etwas geschieht. */
function bulkAuswahl() {
  const sel = BulkSelect.getSelected();
  if (!IS_SERVER_BUILD) return sel;
  const { frei, weg } = ohneGebundene(sel);
  weg.forEach(id => BulkSelect.deselect(id));
  return frei;
}

EventBus.on('bulk:move', ({ toColId, toSprintId }) => {
  const selected = bulkAuswahl();
  if (selected.size === 0) return;

  mutateBoard(state => {
    for (const cardId of selected) {
      /* Sprint-Meta-Karten überspringen */
      const found = findCard(state, cardId);
      if (!found) continue;
      const card = found.sprint.cards[found.idx];
      if (card.kind === 'sprint-meta') continue;

      /* Ziel-Sprint ermitteln */
      let targetSprint;
      if (toSprintId) {
        targetSprint = state.board.sprints.find(s => s.id === toSprintId);
      } else {
        targetSprint = found.sprint; /* Karte bleibt im selben Sprint */
      }
      if (!targetSprint) continue;

      /* Karte aus aktuellem Sprint entfernen */
      found.sprint.cards.splice(found.idx, 1);

      /* Spalte setzen wenn angegeben */
      if (toColId) {
        const oldColumnId = card.columnId;
        card.columnId = toColId;
        if (oldColumnId !== toColId) {
          const author = Config.get('userName') || '';
          Activity.recordChange(card, 'column-moved', author, { from: oldColumnId, to: toColId });
        }
      }

      /* In Ziel-Sprint einfügen */
      targetSprint.cards.push(card);
    }
  });
  BulkSelect.clearAll();
});

/**
 * Bulk-Owner: Setzt den Owner für alle selektierten Karten.
 * Sprint-Meta-Karten werden übersprungen.
 */
EventBus.on('bulk:set-owner', ({ owner }) => {
  const selected = bulkAuswahl();
  if (selected.size === 0) return;

  mutateBoard(state => {
    for (const cardId of selected) {
      const found = findCard(state, cardId);
      if (!found) continue;
      const card = found.sprint.cards[found.idx];
      if (card.kind === 'sprint-meta') continue;
      const author = Config.get('userName') || '';
      if (card.owner !== owner) {
        Activity.recordChange(card, 'owner-changed', author, { from: card.owner, to: owner });
      }
      card.owner = owner;
    }
  });
  BulkSelect.clearAll();
});

/**
 * Bulk-Label-Add: Fügt ein Label zu allen selektierten Karten hinzu.
 * Sprint-Meta-Karten werden übersprungen. Duplikate werden vermieden.
 */
EventBus.on('bulk:label-add', ({ label }) => {
  if (!label) return;
  const selected = bulkAuswahl();
  if (selected.size === 0) return;

  mutateBoard(state => {
    for (const cardId of selected) {
      const found = findCard(state, cardId);
      if (!found) continue;
      const card = found.sprint.cards[found.idx];
      if (card.kind === 'sprint-meta') continue;
      if (!Array.isArray(card.labels)) card.labels = [];
      if (!card.labels.includes(label)) card.labels.push(label);
    }
  });
  /* Selektion bleibt erhalten — User kann weitere Aktionen ausführen */
});

/**
 * Bulk-Label-Remove: Entfernt ein Label von allen selektierten Karten.
 * Sprint-Meta-Karten werden übersprungen.
 */
EventBus.on('bulk:label-remove', ({ label }) => {
  if (!label) return;
  const selected = bulkAuswahl();
  if (selected.size === 0) return;

  mutateBoard(state => {
    for (const cardId of selected) {
      const found = findCard(state, cardId);
      if (!found) continue;
      const card = found.sprint.cards[found.idx];
      if (card.kind === 'sprint-meta') continue;
      if (Array.isArray(card.labels)) {
        card.labels = card.labels.filter(l => l !== label);
      }
    }
  });
  /* Selektion bleibt erhalten */
});

/**
 * Bulk-Delete: Löscht alle selektierten Karten.
 * Sprint-Meta-Karten werden übersprungen.
 * Links-Rückverweise (links-Symmetrie) werden aufgeräumt.
 */
EventBus.on('bulk:delete', () => {
  const selected = bulkAuswahl();
  if (selected.size === 0) return;

  /* Attachments aller Karten bereinigen (Fire-and-forget).
     Karte #450: über das Plugin — siehe Begründung beim Einzel-Löschen. */
  const purges = [];
  for (const cardId of selected) {
    purges.push(anhangBackend().purgeCardAttachments(cardId).catch(() => true)); /* F-2978: Standalone ohne .then (Template-Größe) */
  }
  /* F-2990: eine Sammelmeldung mit Anzahl statt je Karte eine */
  if (IS_SERVER_BUILD) Promise.all(purges).then(anhangPurgeMelden).catch(() => {});

  mutateBoard(state => {
    for (const cardId of selected) {
      /* Sprint-Meta-Karten überspringen — Guard */
      const found = findCard(state, cardId);
      if (!found) continue;
      const card = found.sprint.cards[found.idx];
      if (card.kind === 'sprint-meta') continue;

      /* Links-Rückverweise aufräumen (ADR-0032) */
      Links.purgeLinksTo(state.board, cardId);
      /* Karte entfernen */
      found.sprint.cards.splice(found.idx, 1);
    }
  });
  BulkSelect.clearAll();
});

EventBus.on('card:moved', ({ cardId, toSprintId, toColId, targetIndex }) => {
  /* ADR-0036: Sprint-Meta-Karten dürfen nicht verschoben werden */
  const moveState = State.get();
  const movedMeta = moveState?.board?.sprints
    .flatMap(s => s.cards)
    .find(c => c.id === cardId && c.kind === 'sprint-meta');
  if (movedMeta) {
    showUnlockedToast(I18n.t('sprint-meta.cannot-move'));
    return;
  }

  /* ADR-0032 Karte #54: Auto-Entsperren-Toast — vor der State-Mutation berechnen,
     damit wir noch Zugriff auf den alten columnId-Stand haben */
  let unlockedCards = [];
  const stateBefore = State.get();
  /* F-127/F-807: nicht `toColId === 'col-erledigt'` — auf einem Board mit
     manuell angelegter oder umbenannter Erledigt-Spalte wurde
     `detectUnlockedCards` sonst gar nicht erst gerufen, und die Titel-Semantik
     in `links.js` lief dort ins Leere. Geteilte Quelle ist `Links.isDoneColumn`.

     F-834: `Links.isDoneColumnId` statt manuellem `columnDefs.find` +
     `isDoneColumn(toDefVorher)` — Letzteres hat KEINEN Rückweg, wenn zur id
     kein columnDef existiert (`toDefVorher === undefined` → `isDoneColumn`
     liefert `false`, das Gate feuert nie). `links.js` selbst baut genau
     deswegen bewusst einen Fallback auf den id-Vergleich ein (siehe
     `isDoneColumnId`, Kommentar dort: „sonst gälte dort plötzlich KEINE
     Spalte mehr als erledigt") — dieser Aufruf hier ging bislang daran
     vorbei. Praktisch entschärft durch storage.js/F-249 (leere columnDefs
     werden beim Laden gefüllt), aber zwei Stellen derselben Entscheidung
     sollten nicht unterschiedlich robust sein. */
  if (Links.isDoneColumnId(stateBefore?.board, toColId) && stateBefore?.board) {
    unlockedCards = Links.detectUnlockedCards(stateBefore.board, cardId);
  }

  /* #339: siehe card:updated — Ableitung drinnen, Versand draussen. */
  let notifEvent = null;
  let notifBoard = null;
  let notifCard  = null;
  mutateBoard(state => {
    const found = findCard(state, cardId);
    if (!found) return;
    const [card] = found.sprint.cards.splice(found.idx, 1);

    const toSprint = state.board.sprints.find(s => s.id === toSprintId);
    if (!toSprint) return;

    /* ADR-0037: Spalten-Wechsel tracken (Drag-and-Drop, lokale Mutation) */
    const oldColumnId = card.columnId;
    /* #339: Ausgangslage für das card.moved-Ereignis festhalten, BEVOR die
       Karte umgehängt wird — danach ist sie nicht mehr rekonstruierbar. */
    const notifVorher = IS_SERVER_BUILD
      ? { columnId: card.columnId, sprintId: found.sprint.id }
      : null;
    card.columnId = toColId;
    if (oldColumnId !== toColId) {
      const author = Config.get('userName') || '';
      Activity.recordChange(card, 'column-moved', author, { from: oldColumnId, to: toColId });
    }

    const toDef = state.board.columnDefs.find(c => c.id === toColId);
    if (Links.isDoneColumn(toDef)) card.priority = 'mittel';

    if (targetIndex !== null && targetIndex >= 0) {
      /* targetIndex ist relativ zu Karten mit gleicher columnId im Ziel-Sprint */
      const colCards = toSprint.cards.filter(c => c.columnId === toColId);
      if (targetIndex < colCards.length) {
        const anchor = colCards[targetIndex];
        const absIdx = toSprint.cards.indexOf(anchor);
        toSprint.cards.splice(absIdx, 0, card);
      } else {
        toSprint.cards.push(card);
      }
    } else {
      toSprint.cards.push(card);
    }

    if (IS_SERVER_BUILD) {
      notifBoard = state.board;
      notifCard  = card;
      notifEvent = NotificationTriggers.ereignisFuerVerschieben(
        notifVorher,
        {
          ...card,
          sprintId: toSprintId,
          columnTitle: state.board.columnDefs.find(c => c.id === toColId)?.title,
        },
        state.board,
      );
    }
  });
  if (IS_SERVER_BUILD && notifEvent) {
    NotificationTriggers.sendeEreignisse(notifBoard?.id, notifBoard, notifCard, [notifEvent]);
  }

  /* ADR-0032 Karte #54: Toast für jede entsperrte Karte anzeigen (kein Auto-Mutate) */
  for (const unlockedCard of unlockedCards) {
    const id = unlockedCard.displayId != null ? unlockedCard.displayId : unlockedCard.title;
    showUnlockedToast(I18n.t('link.unlocked-toast', id));
  }
});

EventBus.on('col:quick-add', ({ title }) => {
  mutateBoard(state => { state.board.columnDefs.push(Model.createColumnDef(title)); });
});

/* col:add-cancel: Popover-basiert, kein Event mehr nötig */

EventBus.on('col:rename', ({ colId, title }) => {
  mutateBoard(state => {
    const col = state.board.columnDefs.find(c => c.id === colId);
    if (col) col.title = title;
  });
});

/* F-153 (Karte #373): Sprint-Meta-Karten sind von „Spalte leeren" ausgenommen.
   ADR-0036/CLAUDE.md sagen zu, dass Meta-Karten nicht gelöscht werden können —
   dieser Handler hielt die Zusage nicht: eine aktivierte Sprint-Start-Karte
   liegt in `col-erledigt`, also genau dort, wo „Spalte leeren" zuschlägt.
   `migrateSprintMeta` legt sie beim nächsten Load zwar neu an, aber leer — die
   Übergabe-Notizen für den nächsten Chat waren weg.
   Bewusst KEIN Leer-Test wie in `istMetaKarteLeer` (die Karte schlug einen
   gemeinsamen Helfer vor): hier ist der kategorische Schutz richtig, weil er
   der Zusage aus ADR-0036 entspricht und dem Muster von `col:archive-done`
   folgt, das Meta-Karten schon immer ausnimmt. Eine leere Meta-Karte zu löschen
   hätte ohnehin keinen Effekt — der nächste Board-Load legt sie neu an. */
EventBus.on('col:clear', ({ colId }) => {
  mutateBoard(state => {
    /* F-122: Rückverweise putzen bevor Karten gelöscht werden */
    state.board.sprints.forEach(sprint => {
      const trifft = c => c.columnId === colId && c.kind !== 'sprint-meta';
      sprint.cards.filter(trifft).forEach(c => Links.purgeLinksTo(state.board, c.id));
      sprint.cards = sprint.cards.filter(c => !trifft(c));
    });
  });
});

/* Karte #279: Massen-Archiv einer (Erledigt-)Spalte. Setzt archived:true für
   alle nicht-archivierten Nicht-Meta-Karten der Spalte. F-NEU-2: NUR im
   auslösenden Sprint (`sprintId`) — sonst wich die bestätigte Zahl (pro
   Sprint gezählt) von der Wirkung (sprint-übergreifend) ab. Fehlt sprintId
   (Defensiv/Alt-Emitter), gilt der Alt-Fallback „alle Sprints".
   Sprint-Meta-Karten (kind:'sprint-meta') sind ausgenommen (ADR-0036) —
   anders als col:clear (F-153) hat diese Aktion den Meta-Guard. */
EventBus.on('col:archive-done', ({ colId, sprintId }) => {
  mutateBoard(state => {
    state.board.sprints.forEach(sprint => {
      if (sprintId && sprint.id !== sprintId) return;
      sprint.cards.forEach(c => {
        if (c.columnId === colId && !c.archived && c.kind !== 'sprint-meta') {
          c.archived = true;
        }
      });
    });
  });
});

EventBus.on('col:delete', ({ colId }) => {
  mutateBoard(state => {
    /* Titel der zu löschenden Spalte VOR dem Filtern merken — der
       Verlaufseintrag unten soll den Namen nennen, nicht die rohe ID (D-F). */
    const alteSpaltenTitel = new Map(state.board.columnDefs.map(c => [c.id, c.title]));
    state.board.columnDefs = state.board.columnDefs.filter(c => c.id !== colId);
    /* F-153 (Karte #373), dritte Stelle derselben Klasse — vom Finding nicht
       genannt, bei der Suche gefunden: „Spalte löschen" nahm Meta-Karten mit.
       Anders als bei `col:clear` verschwindet hier die Spalte, die Karte kann
       also nicht bleiben, wo sie ist. Sie wandert deshalb in die erste
       verbleibende Spalte statt gelöscht zu werden; gibt es keine mehr, bleibt
       sie unverändert stehen (Board ohne Spalten ist ein Sonderzustand, den
       der nächste Spalten-Anlage-Schritt auflöst). */
    const titelVon = id => state.board.columnDefs.find(c => c.id === id)?.title ?? id;
    const geloeschterTitel = alteSpaltenTitel.get(colId) ?? colId;
    /* F-122: Rückverweise putzen bevor Karten gelöscht werden */
    state.board.sprints.forEach(sprint => {
      sprint.cards.forEach(c => {
        if (c.columnId !== colId || c.kind !== 'sprint-meta') return;
        /* Sprint-51-Kritiker (K-C): semantisch passende Zielspalte statt blind
           `columnDefs[0]`. Landete die Start-Karte in „Ready", konnte
           `autoActivateSprintStart` nie wieder greifen (es verlangt
           `col-backlog`) — der Sprint war dauerhaft nicht mehr aktivierbar. */
        const ziel = Storage.metaZielSpalte(state.board.columnDefs, c.columnId);
        if (!ziel) return;                       /* keine Spalte übrig → stehen lassen */
        c.columnId = ziel;
        /* Sprint-51-Kritiker (D3/D-F): ADR-0037 verlangt einen Verlaufseintrag für
           jede Spalten-Verschiebung. Diese hier passiert programmatisch, ohne
           Zutun des Nutzers — genau deshalb gehört sie ins Log. Format wie alle
           anderen Spalten-Bewegungen (`action: 'column-moved'` mit `from`/`to`),
           damit die Verlaufs-Anzeige es übersetzt statt deutschen Freitext zu
           zeigen; Spalten-TITEL statt roher IDs; Zufalls-Suffix in der ID gegen
           Kollision, wenn Start- und Abschluss-Karte im selben Löschvorgang
           umziehen (F-226-Muster); `updated` mitziehen wie in
           `autoActivateSprintStart`. */
        if (!Array.isArray(c.activity)) c.activity = [];
        const jetzt = new Date().toISOString();
        const rand  = Math.random().toString(36).slice(2, 8);
        c.activity.push({
          id:        `${jetzt}-col-delete-rehome-${rand}`,
          actor:     'system',
          timestamp: jetzt,
          action:    'column-moved',
          from:      geloeschterTitel,
          to:        titelVon(ziel),
        });
        c.updated = jetzt.slice(0, 10);
        /* D-D: Der Verlaufs-Tab ist für Meta-Karten ausgeblendet (ADR-0036), der
           Eintrag ist also nur im JSON/Export sichtbar. Damit wenigstens die
           Konsole die Bewegung erklärt: */
        console.info(
          `[col:delete] Meta-Karte "${c.title}" aus gelöschter Spalte "${geloeschterTitel}" ` +
          `nach "${titelVon(ziel)}" verschoben statt entfernt (F-153).`
        );
      });
      const trifft = c => c.columnId === colId && c.kind !== 'sprint-meta';
      sprint.cards.filter(trifft).forEach(c => Links.purgeLinksTo(state.board, c.id));
      sprint.cards = sprint.cards.filter(c => !trifft(c));
    });
  });
  /* F-214: Stale collapsedColumns-Keys aufräumen (Format: `${sprintId}:${colId}`) */
  Renderer.cleanupCollapsedColumns(key => key.endsWith(`:${colId}`));
});

EventBus.on('col:color', ({ colId, color }) => {
  mutateBoard(state => {
    const col = state.board.columnDefs.find(c => c.id === colId);
    if (col) col.color = color ?? null;
  });
});

EventBus.on('col:moved', ({ colId, targetIndex }) => {
  mutateBoard(state => {
    const idx = state.board.columnDefs.findIndex(c => c.id === colId);
    if (idx === -1) return;
    const [col] = state.board.columnDefs.splice(idx, 1);
    state.board.columnDefs.splice(targetIndex, 0, col);
  });
});

/* #326: Swimlane-Reordering — bewegt board.sprints[sprintId] an targetIndex.
   __backlog__ ist im Drag-Source-Check (renderSprintHeader/dragdrop.js) bereits
   ausgeschlossen. Der Op-Apply-Pfad in ops.js (case 'swimlane.move') übernimmt
   die mutation auf migrierten Boards; auf nicht migrierten greift der Diff-
   Pfad in diff.js (vergleicht alte vs. neue sprints-Reihenfolge). */
EventBus.on('swimlane:moved', ({ sprintId, targetIndex, ankerSprintId }) => {
  if (!sprintId || sprintId === '__backlog__') return;
  mutateBoard(state => {
    /* ADR-0117: Die Positionsrechnung steht als reine Funktion in model.js —
       dieser Handler ist nicht exportiert und waere sonst nur ueber eine
       Referenz-Implementierung im Test erreichbar, die sich selbst prueft.
       `ankerSprintId` ist die id der Ziel-Swimlane und damit unabhaengig
       davon, welche Swimlanes gerade ausgeblendet sind; `targetIndex` bleibt
       als Rueckfall fuer Aufrufer ohne Anker. */
    Model.verschiebeSwimlane(state.board.sprints, sprintId, { ankerSprintId, targetIndex });
  });
});

EventBus.on('sprint:add', ({ title, kind }) => {
  mutateBoard(state => {
    /* ADR-0046: kind = 'sprint' (default) oder 'lane'. Lanes bekommen keine Meta-Karten. */
    const sprint = Model.createSprint(title, kind === 'lane' ? 'lane' : 'sprint');
    if (sprint.kind === 'sprint') {
      /* ADR-0036/0042: Start- und End-Sonderkarte beim Sprint-Anlegen erzeugen.
         Start startet in col-backlog/qa:false; erst bei tatsaechlicher Sprint-Aktivierung
         (ADR-0042) wird sie nach col-erledigt/qa:true verschoben — sonst sieht jeder
         frisch angelegte Sprint sofort wie der laufende aus. */
      const startId = Model.assignDisplayId(state.board);
      const endId   = Model.assignDisplayId(state.board);
      const startCard = Model.createSprintMetaCard('start', startId, 'col-backlog', false);
      const endCard   = Model.createSprintMetaCard('end',   endId,   'col-backlog', false);
      sprint.cards.push(startCard, endCard);
    }

    const backlogIdx = state.board.sprints.findIndex(s => s.id === '__backlog__');
    if (backlogIdx >= 0) {
      state.board.sprints.splice(backlogIdx, 0, sprint);
    } else {
      state.board.sprints.push(sprint);
    }
  });
});

/* ADR-0046: Swimlane-Kind umstellen (sprint ↔ lane).
   - sprint → lane: Meta-Karten bleiben als Historie erhalten (verstecken via archived wäre destruktiv).
   - lane → sprint: Meta-Karten werden beim nächsten Load via migrateSprintMeta ergänzt.
   Eine sofortige In-Place-Ergänzung über mutateBoard wäre möglich, ist aber redundant —
   die Migration läuft bei jedem checkVersion-Aufruf idempotent. */
EventBus.on('sprint:set-kind', ({ sprintId, kind }) => {
  if (sprintId === '__backlog__') return;
  if (kind !== 'sprint' && kind !== 'lane') return;
  mutateBoard(state => {
    const sprint = state.board.sprints.find(s => s.id === sprintId);
    if (!sprint || sprint.kind === kind) return;
    sprint.kind = kind;
    /* Bei lane → sprint: Meta-Karten direkt nachrüsten, damit sie ohne Reload erscheinen */
    if (kind === 'sprint') {
      const hasStart = sprint.cards.some(c => c.kind === 'sprint-meta' && c.metaType === 'start');
      const hasEnd   = sprint.cards.some(c => c.kind === 'sprint-meta' && c.metaType === 'end');
      if (!hasStart) {
        const startId = Model.assignDisplayId(state.board);
        sprint.cards.unshift(Model.createSprintMetaCard('start', startId, 'col-backlog', false));
      }
      if (!hasEnd) {
        const endId = Model.assignDisplayId(state.board);
        sprint.cards.push(Model.createSprintMetaCard('end', endId, 'col-backlog', false));
      }
    }
  });
});

/* ADR-0117 (Karte #346): Swimlane archivieren / zurueckholen.
   Guards doppelt — die UI zeigt fuer __backlog__ und kind:'service' schon
   keinen Knopf, aber ein Guard, der nur in der UI sitzt, ist keiner: das Event
   kann aus einem Skript, einem Test oder einem spaeteren Aufrufer kommen.
   BEWUSST KEINE Kaskade auf die Karten der Swimlane (ADR-0117 §4): Sie waere
   gegenstandslos (die ganze Swimlane ist verborgen) und nicht umkehrbar — nach
   dem Zurueckholen liesse sich nicht mehr unterscheiden, welche Karten der
   Nutzer selbst archiviert hatte. */
EventBus.on('sprint:set-archived', ({ sprintId, archived }) => {
  if (!sprintId || sprintId === '__backlog__') return;
  mutateBoard(state => {
    const sprint = state.board.sprints.find(s => s.id === sprintId);
    /* Derselbe Test wie in der UI — eine Funktion, zwei Aufrufer, damit die
       beiden Guards nicht auseinanderdriften koennen (ADR-0117 §5). */
    if (!Model.darfArchiviertWerden(sprint)) return;
    const neu = !!archived;
    if (!!sprint.archived === neu) return;   /* idempotent, kein Leer-Diff */
    sprint.archived = neu;
  });
});

EventBus.on('sprint:delete', ({ sprintId }) => {
  if (sprintId === '__backlog__') return;
  mutateBoard(state => {
    const sprint  = state.board.sprints.find(s => s.id === sprintId);
    if (!sprint) return;
    const backlog = state.board.sprints.find(s => s.id === '__backlog__');

    /* ADR-0036: Meta-Karten werden nicht in den Backlog verschoben — sie gehören zum Sprint
       und werden mitgelöscht. Normale Karten weiterhin in den Backlog verschieben. */
    const metaCards    = sprint.cards.filter(c => c.kind === 'sprint-meta');
    const normalCards  = sprint.cards.filter(c => c.kind !== 'sprint-meta');

    /* Rückverweis-Cleanup für Meta-Karten (keine Links, aber sauber) */
    for (const card of metaCards) {
      Links.purgeLinksTo(state.board, card.id);
    }

    if (backlog) {
      /* Normale Karten in den Backlog verschieben — Links bleiben intakt */
      backlog.cards.push(...normalCards);
    } else {
      /* ADR-0032 Karte #54: Kein Backlog-Sprint — alle Karten wirklich entfernen */
      for (const card of normalCards) {
        Links.purgeLinksTo(state.board, card.id);
      }
    }
    state.board.sprints = state.board.sprints.filter(s => s.id !== sprintId);
  });
  /* F-214: Stale collapsedColumns-Keys aufräumen (Format: `${sprintId}:${colId}`) */
  Renderer.cleanupCollapsedColumns(key => key.startsWith(`${sprintId}:`));
});

EventBus.on('sprint:rename', ({ sprintId, title }) => {
  mutateBoard(state => {
    const sprint = state.board.sprints.find(s => s.id === sprintId);
    if (sprint) sprint.title = title;
  });
});

EventBus.on('board:title-change', ({ title }) => {
  mutateBoard(state => { state.board.title = title; });
});

EventBus.on('board:save', async () => {
  const plugin = StoragePlugin.getActivePlugin();
  /* ADR-0044: Plugin entscheidet, was passiert — Standalone schreibt FSA-Datei,
     Projekt schreibt aboard-sync.json + aboard.html, REST-Plugin PUT /boards/{id}. */
  let result;
  const genBeimSenden = _aenderungsGen;
  try {
    result = await plugin.save(State.get());
  } catch (err) {
    console.error('Speichern fehlgeschlagen:', err);
    return;
  }
  if (!result) return;
  /* F-178: Returntyp ist {ok, conflict?, method?} */
  if (result.ok) {
    if (IS_SERVER_BUILD && plugin.kind === 'rest') restGesichert(plugin, result, genBeimSenden);
    else State.markClean();
    showSaveConfirm(result.method);
  } else if (result.conflict) {
    const conflict = result.conflict;
    if (conflict.kind === 'fsa') {
      /* F-247 / ADR-0050: aboard-sync.json extern geändert beim manuellen Save. */
      showConflictBanner(
        async () => {
          /* F-294: auf migrierten Boards Projektor-Pfad statt roher
             Ganzdatei-write (sonst F-283-Klasse). Auf migrierten Boards
             ist dieser fsa-Konflikt zwar via plugin.save() unerreichbar
             (projectorPersist liefert nie {conflict}) — defensiv gegated. */
          const w = await Sync.persistGated(State.get(), { force: true, actor: Config.get('userName') || '' }).catch(() => false);
          if (w === true) { State.markClean(); showSaveConfirm('fsa'); }
          else showExportToast(I18n.t('conflict.retry-failed'));
        },
        () => {
          Sync.markBaseline(conflict.diskRaw);
          State.set(conflict.serverState);
          State.markClean();
        },
      );
    } else if (IS_SERVER_BUILD) {
      /* Nicht-FSA-Konflikte liefert nur das REST-Plugin (Karte #565: gegated,
         damit die Helfer nicht im slim Template stehen).
         Optimistic-Concurrency-Konflikt (Karte #77): Board wurde parallel geändert.
         Plugin hat in putBoard() den Server-ETag gespeichert — der PUT hinter
         "Meine Version" (speichereGanz) sendet If-Match: Server-ETag → Server akzeptiert. */
      showConflictBanner(
        () => behalteMeine(plugin, State.get()),
        () => uebernimmServerStand(plugin, conflict.serverState),
      );
    }
  } else if (IS_SERVER_BUILD && result.gesperrt) {
    /* F-1453: Speichern-Knopf nach gescheitertem Boot — nicht stumm bleiben. */
    BootHinweis?.zeige();
    showExportToast(I18n.t('sync.boardliste-fehlt'));
  } else if (IS_SERVER_BUILD && result.sitzungAbgelaufen) {
    /* #1053: Der Hinweis „Sitzung abgelaufen“ steht; Änderungen bleiben ungesendet. */
  } else if (IS_SERVER_BUILD && result.erneutNachMs) {
    /* K-A1 (Session-Kritiker Sprint 72): 429/503/Netz beim Ops-Speichern.
       Vorher blieb der Knopf hier stumm — kein Hinweis, keine Wiederholung,
       anders als im Hintergrund-Speichern. Jetzt derselbe Weg wie dort. */
    restNichtGesichert(result);
  } else if (IS_SERVER_BUILD && result.abgewiesen) {
    zeigeAbgewiesen(result.abgewiesen);
  }
});

EventBus.on('board:open', () => {
  Storage.open().then(loaded => {
    if (!loaded) return;
    /* Karte #565: Ein Import ersetzt das Board bewusst ganz — per PUT, nicht
       als Ops-Diff auf den Server-Stand (ADR-0129 §4, Punkt 6). */
    if (IS_SERVER_BUILD) StoragePlugin.getActivePlugin().naechstesGanz?.();
    BulkSelect.clearAll(); /* Boardwechsel ohne board:loaded (Sprint-85-Kritiker, F-1786) */
    State.set(loaded);
  }).catch(err => {
    console.error('Öffnen fehlgeschlagen:', err);
  });
});

/* ADR-0028: Wechsel zu bekanntem Standalone-Board (aus Switch-Popover) */
EventBus.on('standalone:switch-to-file', async ({ boardId }) => {
  /* Dirty-Guard: Standalone-Modus braucht Nutzer-Bestätigung */
  if (State.isDirty()) {
    const isProject = IS_PROJECTOR_BUILD && Profile.has('feature.polling') && Sync.isActive();
    if (isProject) {
      /* Projekt-Modus: pending Save flushen, kein Confirm */
      await debouncedPersist.flush();
    } else {
      /* Standalone: Zwei-Stufen-Confirm */
      const saveFirst = confirm(I18n.t('dirty.save-first'));
      if (saveFirst) {
        const dirHandle = Profile.has('feature.polling') ? Sync.getDirectoryHandle() : null;
        await Storage.save(State.get(), dirHandle);
      } else {
        if (!confirm(I18n.t('dirty.confirm-switch'))) return;
      }
    }
  }
  const loaded = await Storage.openByBoardId(boardId);
  if (!loaded) {
    showExportToast(I18n.t('sync.permission-needed'));
    return;
  }
  /* Projekt-Modus deaktivieren falls aktiv — Standalone-Board hat keinen Sync-Ordner */
  if (Profile.has('feature.polling')) {
    Sync.stopPolling();
    Sync.clearActiveHandle();
    Config.set('boardMode',   'solo');
    Config.set('projectName', '');
  }
  State.set(loaded);
  State.markClean();
  EventBus.emit('board:loaded');
  EventBus.emit('config:project-changed');
});

/* ADR-0056 (Karte #195): Neues Board im Server-Modus anlegen.
   Karte #203 / F-275: nur im Server-Build registrieren — der einzige Emitter
   (Eintrag „Neues Server-Board“ im Board-Menü, board-menu.js, Karte #704) ist
   im Standalone-/Template-Build DCE-eliminiert; IS_SERVER_BUILD=false → esbuild entfernt diesen Block per DCE
   (konsistent zum sidebar.js/storage-plugin.js-Gating-Muster). */
if (IS_SERVER_BUILD) {
EventBus.on('server:new-board', async ({ title }) => {
  /* Dirty-Guard: offene Änderungen am aktuellen Board flushen */
  if (State.isDirty()) {
    await debouncedPersist.flush();
  }
  if (await sitzungSperrtWechsel()) return; /* #1053, Kritik B-1 */
  const result = await StoragePlugin.getActivePlugin().createBoard(title);
  if (!result.ok) {
    showExportToast(I18n.t('board.switch.create-failed'));
    return;
  }
  State.set(result.state);
  bezugIstLokal(); /* #566 */
  Renderer.resetSwimlaneState();
  EventBus.emit('board:loaded');
  /* boardId im Plugin ist nach createBoard (via switchBoard) bereits gesetzt.
     Polling neu starten, damit der neue Board-Kanal beobachtet wird. */
  StoragePlugin.getActivePlugin().stopPolling();
  if (Config.get('serverPollEnabled') !== false) {
    StoragePlugin.getActivePlugin().startPolling(onExternalUpdate);
  }
  State.markClean();
  showExportToast(I18n.t('sync.server-board-created'));
  EventBus.emit('config:project-changed');
});
} /* Ende if (IS_SERVER_BUILD) — server:new-board (Karte #203 / F-275) */

/* Karte #845 (F-2237): „Board löschen…“ — was nur main.js kennt. Vor dem DELETE
   müssen geplantes Speichern und Polling anhalten: ein Speichern auf das
   gelöschte Board legte es per PUT-404→POST sofort wieder an. */
if (IS_SERVER_BUILD) {
BoardLoeschen?.konfiguriere({
  /* Offene Änderungen werden gespeichert, nicht verworfen: Scheitert das
     Löschen (403, Netz), bleibt das Board — und mit ihm, was zuletzt
     getippt wurde (Orchestrator-Korrektur, vorher cancel + takePending). */
  anhalten: async () => {
    await debouncedPersist.flush();
    await debouncedPersist.idle();
    StoragePlugin.getActivePlugin().stopPolling();
  },
  fortsetzen: () => {
    if (Config.get('serverPollEnabled') !== false) StoragePlugin.getActivePlugin().startPolling(onExternalUpdate);
  },
  melde: (text) => showExportToast(text),
});
} /* Ende if (IS_SERVER_BUILD) — Board löschen (Karte #845) */

/* Karte #726: Eine Rückspielung („Überschreiben“) ersetzt das offene Board am
   Server. Der Client übernimmt den neuen Stand SOFORT — auch bei offenem
   Einstellungsdialog und auch mit ungesicherten Änderungen, denn genau die
   hat der Nutzer mit „Überschreiben“ verworfen. Ohne diesen Schritt hielt
   `restEingang` den Stand zurück, solange der Dialog offen war: Hinter
   „✓ Restored.“ stand weiter das alte Board, und wer daraufhin „Save“
   drückte, hielt den Restore für überschrieben (06.10., Server-Betrieb).
   Ein noch geplantes Hintergrund-Speichern trägt den ALTEN Stand; nach der
   Übernahme würde es als Diff gegen den neuen Bezug gerechnet und den Restore
   Stück für Stück zurückdrehen. Deshalb wird es vorher verworfen.
   Ergebnisse, die nach dem Schliessen des Dialogs eintreffen, landen als
   Toast — im geschlossenen Dialog sähe sie niemand. */
if (IS_SERVER_BUILD) {
/* Board-Einstellungen ODER Admin-Dialog (F-1827): wer im offenen Dialog die
   Meldung schon sieht, braucht keinen zusätzlichen Toast. */
const dialogOffen = () => !!document.querySelector('.config-overlay[aria-hidden="false"], .admin-overlay.modal-overlay--open');
EventBus.on('backup:restored', async ({ boardId, mode }) => {
  const plugin = StoragePlugin.getActivePlugin();
  if (!dialogOffen()) showExportToast(I18n.t('admin.backup.msg-restored'));
  if (plugin.kind !== 'rest' || mode !== 'overwrite' || State.get()?.board?.id !== boardId) return;
  debouncedPersist.cancel();
  /* Ein schon gefeuertes Speichern läuft weiter; abwarten, damit der danach
     geholte Stand es enthält und sein Ergebnis nicht hinter der Übernahme
     eintrifft (Kritiker A-3). */
  await debouncedPersist.idle();
  Sync.takePending();
  const sw = await plugin.switchBoard(boardId).catch(() => null);
  /* Während des Abrufs gewechselt: nicht in ein fremdes Board übernehmen. */
  if (State.get()?.board?.id !== boardId) return;
  const validated = sw?.ok ? Storage.checkVersion(sw.state) : null;
  if (!validated?.board) {
    /* Restore geglückt, Abruf gescheitert: sonst bliebe still das alte Board
       stehen — genau das gemeldete Symptom (Kritiker A-5). */
    showExportToast(I18n.t('admin.backup.msg-restored-reload'));
    return;
  }
  debouncedPersist.cancel();
  uebernimmServerStand(plugin, validated);
  showSyncToast();
});
EventBus.on('backup:restore-failed', ({ text }) => {
  if (!dialogOffen()) showExportToast(text);
});
}

EventBus.on('board:export-json', () => Export.toJSON(State.get()));

EventBus.on('board:export-ki', async () => {
  const isProject = IS_PROJECTOR_BUILD && Profile.has('feature.polling') && Sync.isActive();
  if (isProject) {
    const ok = await Export.toKIKontextFSA(State.get(), Sync.getDirectoryHandle());
    showExportToast(I18n.t(ok ? 'export.ki-written' : 'export.ki-failed'));
  } else {
    Export.toKIKontextDownload(State.get());
  }
});

EventBus.on('config:open',       () => {});
EventBus.on('config:language-changed', () => {
  State.set(deepClone(State.get()));
});

EventBus.on('filter:changed', () => {
  State.set(deepClone(State.get()));
});

EventBus.on('search:changed', () => {
  State.set(deepClone(State.get()));
});

/* Such-Ergebnis-Änderungen via Search-Modul → Board neu rendern */
Search.onChange(() => {
  State.set(deepClone(State.get()));
});

/* ── Sync — externe State-Änderungen verarbeiten (→ ADR-0021) ────── */

/* F-254/ADR-0055: externer Stand ist autoritativ und darf KEINEN Echo-PUT
   auslösen. State.set() emittiert state:changed synchron → der Persist-
   Handler (init()) prüft dieses Flag und überspringt debouncedPersist für
   extern angewandten State. Sonst: jeder Poll → State.set → Echo-PUT →
   Server bumpt updated_at → nächster Poll „geändert" → Schleife + Save-Race. */
let _applyingExternalState = false;

/* Karte #566 (ADR-0129 §4, Punkt 3): Änderungs-Generation für den Server-
   Modus. `_aenderungsGen` zählt jedes `state:changed`, das nicht aus einer
   Übernahme stammt; `_genBezug` hält fest, bei welcher Generation L zuletzt
   dem Bezug des REST-Plugins entsprach (nach Übernahme oder angenommenem
   Speichern). Ungleich heisst: L trägt Ungesichertes, ein Server-Stand wird
   dann nicht übernommen — erst speichern, danach holen. `State.isDirty()`
   taugt dafür nicht: `debouncedPersistInner` setzt es nach dem `await`
   zurück, auch wenn während der Anfrage weitergetippt wurde (knowledge §11). */
let _aenderungsGen = 0;
let _genBezug = 0;
function bezugIstLokal() { _genBezug = _aenderungsGen; }

/* F-254/F-310: Validiert einen externen Stand und prüft, ob er sich vom
   aktuellen unterscheidet. Single Source von Schema-Normalisierung +
   Deep-Equal-Vergleich — beide Aufrufer (applyExternalState UND
   onExternalUpdate) MÜSSEN exakt dieselbe Normalisierung nutzen, sonst
   Symmetriebruch (falsch-negativ/positiv, vgl. F-310). Liefert das
   validierte Objekt oder null bei No-Op (ungültiges Schema ODER
   byte-identisch zum aktuellen Stand). Greift auch gegen Server OHNE
   304-Support (ältere Images): jeder Poll kommt als 200, läuft hier aber
   ins No-Op statt in die Echo-Schleife. */
function validateExternalDelta(newState) {
  /* F-814-Nacharbeit (Kritiker Sprint 61): Der Rohstand VOR `checkVersion`
     festhalten, um feststellen zu können, ob eine Migration gegriffen hat —
     genau das muss der Aufrufer wissen.

     ACHTUNG, die Begründung hat sich geändert (F-842, Karte #455, Sprint 62):
     Hier stand „`checkVersion` mutiert in place, also ist danach nicht mehr
     feststellbar …". Das stimmt seit Sprint 62 nicht mehr — `checkVersion`
     klont seinen Eingang und lässt ihn unangetastet. Der Snapshot bleibt
     trotzdem richtig und nötig: `newState` ist zwar jetzt auch nach dem
     Aufruf noch der Rohstand, aber der Vergleich braucht ihn serialisiert,
     und die Zeile hier festzuhalten ist billiger als sich darauf zu
     verlassen, dass niemand `newState` zwischenzeitlich anfasst. */
  const vorher   = JSON.stringify(newState);
  const validated = Storage.checkVersion(newState); /* F-81: Schema-Check */
  if (!validated?.board) return null;
  const nachher  = JSON.stringify(validated);
  const cur = State.get();
  if (cur && JSON.stringify(cur) === nachher) return null;
  return { state: validated, migriert: nachher !== vorher };
}

/* F-814-Nacharbeit: einmal pro Sitzung melden, dass eine Migration im
   Adopt-Pfad gegriffen hat. Zweck ist nicht Information, sondern
   Sichtbarkeit einer möglichen Schleife: Eine Migration, deren Wirkung
   `diffToOps` nicht als Op ausdrücken kann, würde bei jedem Poll erneut
   greifen und den State erneut dirty machen. Das wäre kein Datenverlust,
   aber es gehört gesehen. */
let _migrationImAdoptGemeldet = false;

function applyExternalState(newState) {
  /* F-254 Defense-in-depth: ungültiger ODER identischer externer Stand →
     komplett ignorieren (kein State.set/Render/Toast). */
  const delta = validateExternalDelta(newState);
  if (!delta) return;
  const validated = delta.state;
  /* F-163: Spawn NICHT hier — externer Stand ist autoritativ; das schreibende Tab
     hat den Spawn (inkl. lastSpawned-Anchor) bereits durchgeführt. Ein lokaler
     Spawn würde bei Race zwischen zwei Tabs zur Doppel-Instanz führen. Der nächste
     reguläre Init-/Save-Zyklus deckt verpasste Spawns ab. */
  /* Karte #565: Im Server-Modus folgt der Diff-Bezug des REST-Plugins jeder
     Übernahme, roh (ADR-0129 §4, Punkt 1). Die Funktion teilt sich der
     Server- mit dem Projekt-Modus (knowledge §25): Das Projekt-Plugin hat die
     Methode nicht, dort ist der Aufruf wirkungslos. */
  if (IS_SERVER_BUILD) StoragePlugin.getActivePlugin().bezugUebernehmen?.(newState);
  _applyingExternalState = true;
  try {
    State.set(validated);
    if (IS_SERVER_BUILD) bezugIstLokal(); /* #566: eine Übernahme zählt keine Generation hoch */
    /* F-814-Nacharbeit (Kritiker Sprint 61): `markClean` NUR, wenn der
       adoptierte Stand unverändert übernommen wurde.

       Warum das der fehlende Teil des F-814-Fixes ist: Der Adopt-Pfad ist der
       einzige Ort, an dem `Storage.checkVersion` die Migrationen auf einen
       frisch gelesenen Stand anwendet. Er markierte anschliessend `clean`
       (hier) UND unterdrückte den Persist (`_applyingExternalState` im
       `state:changed`-Handler). Ergebnis: State im Speicher migriert, Platte
       unmigriert, kein Persist geplant — der nächste Poll fand
       `sameProjection` wahr und tat nichts. Auf einem Board, das nur
       ANGESEHEN wird (der Fall, aus dem F-814 stammt), erreichte
       die Sprint-Auto-Aktivierung die Platte deshalb weiterhin nie. Der
       Klon-Fix in `src/sync.js` hat das Gift aus dem Diff-Bezug entfernt;
       ohne diese Zeile fehlte der Auslöser.

       Dirty zu lassen genügt und ist der schonendste Eingriff: `State.set`
       setzt das Flag ohnehin, und der Poll-Flush in `sync.js projectorPoll`
       (`lp.dirty`) schreibt die Migration beim nächsten Tick als Ops heraus.
       Kein zusätzlicher Persist-Aufruf, kein Anfassen des F-254-Echo-Schutzes.
       Konvergent, weil die Migration idempotent ist und ihre Wirkung als Op
       persistiert: Nach dem Flush trägt die Platte den migrierten Stand, der
       nächste Adopt findet nichts mehr zu migrieren, `markClean` greift
       wieder. */
    if (!delta.migriert) {
      State.markClean(); /* Externe Updates sind autoritativ — kein dirty (#86) */
    } else if (IS_SERVER_BUILD && StoragePlugin.getActivePlugin().kind === 'rest') {
      /* #566 (F-1164, zweiter Befund): Im Server-Modus gibt es keinen
         `lp.dirty`-Flush wie im Projektor-Poll. Ohne diesen Anstoss bliebe die
         Migration ungeschrieben, bis der Nutzer etwas ändert. Konvergent: Nach
         dem Speichern ist der Server-Stand migriert, der nächste Stand kommt
         unverändert an. Lässt sich die Migration nicht als Op ausdrücken, ist
         der Diff leer, und es geht kein Request hinaus. */
      debouncedPersist(State.get());
    }
    if (delta.migriert && !_migrationImAdoptGemeldet) {
      _migrationImAdoptGemeldet = true;
      console.info('aBoard: Schema-Migration beim Adoptieren eines externen Stands angewandt'
        + ' — der State bleibt dirty, damit sie beim nächsten Persist als Ops herausgeschrieben'
        + ' wird (F-814). Wiederholt sich diese Meldung nach einem Persist, kann die Migration'
        + ' nicht als Op ausgedrückt werden.');
    }
  } finally {
    _applyingExternalState = false;
  }
  showSyncToast();
}

/* Karte #566 (ADR-0129 §4, Punkt 3): Eingang eines Server-Stands im
   Server-Modus — für alle drei Wege: Poll, GET nach dem POST, und der beim
   offenen Karten-Modal zurückgehaltene Stand. Übernommen wird nur, wenn L
   seit dem letzten Setzen des Bezugs unverändert ist. Sonst wird zuerst
   gespeichert (der Server faltet die lokalen Ops auf seinen Stand), danach
   geholt und übernommen. Kein Konflikt-Dialog für diesen Fall. Eigene
   Funktion statt einer Änderung an `applyExternalState`, die sich der
   Server- mit dem Projekt-Modus teilt (knowledge §25). */
function restEingang(plugin, roh) {
  const validated = Storage.checkVersion(roh);
  if (!validated?.board) return;
  if (_aenderungsGen !== _genBezug) {
    /* L trägt Ungesichertes: nicht übernehmen, sondern speichern. Die ETag
       bleibt beim alten Stand, der nächste Poll bringt ihn wieder. */
    debouncedPersist(State.get());
    return;
  }
  /* F-2988 (#1135), F-3009/F-3015: gescheiterte Purges gelöschter Karten nur gegen
     einen Server-Stand nachholen — hier ist L gesichert, `validated` ist der Server. */
  anhangPurgeNachholen?.(validated).catch(() => {});
  const nachher = JSON.stringify(validated);
  if (JSON.stringify(State.get()) === nachher) {
    /* Inhaltlich schon da: nur Bezug und ETag nachführen (sonst fragte jeder
       Poll mit der alten ETag und bekäme denselben Stand erneut). Trägt der
       Rohstand eine Migration, die L schon hat, geht sie als Op hinaus. */
    plugin.bezugUebernehmen?.(roh);
    if (nachher !== JSON.stringify(roh)) debouncedPersist(State.get());
    return;
  }
  const modal = document.querySelector('.modal-overlay--blocks-sync[aria-hidden="false"]');
  if (!modal) {
    applyExternalState(roh);
    return;
  }
  /* Modal offen → zurückhalten. Beim Schliessen NEU prüfen: Wer im Modal
     gespeichert hat, hat die Generation erhöht, und der zurückgehaltene Stand
     darf das nicht überschreiben (F-1164). */
  Sync.setPending(roh);
  showSyncBadge();
  const obs = new MutationObserver(() => {
    if (modal.getAttribute('aria-hidden') === 'true') {
      obs.disconnect();
      const pending = Sync.takePending();
      if (pending) restEingang(StoragePlugin.getActivePlugin(), pending);
    }
  });
  obs.observe(modal, { attributes: true, attributeFilter: ['aria-hidden'] });
}

function onExternalUpdate(newState) {
  if (IS_SERVER_BUILD) {
    const plugin = StoragePlugin.getActivePlugin();
    if (plugin.kind === 'rest') { restEingang(plugin, newState); return; }
  }
  /* F-310: No-Op-Guard VOR die Modal-Verzweigung ziehen. Sonst zeigt ein
     byte-identischer Poll (claude-code projectorPoll tickt ≤15 s und faltet
     i. d. R. dieselbe base+inbox neu) bei offenem Karten-Modal ein
     irreführendes „↺ Änderungen warten…"-Badge, obwohl nichts wartet.
     Symmetrisch zu applyExternalState (gemeinsame validateExternalDelta-
     Normalisierung) — ein ECHT abweichender externer Stand bei offenem
     Modal wird weiterhin korrekt gependet + badged. */
  if (!validateExternalDelta(newState)) return;
  /* F-749: NICHT '.modal-overlay[aria-hidden="false"]' — seit #348 teilen
     sich alle acht Settings-Dialoge (modal-shell.js) die Basisklasse
     `.modal-overlay`; ein reiner Basisklassen-Selektor triffe seither auch
     z. B. den Benachrichtigungs-Dialog und hielte externe Updates unnötig
     zurück. `.modal-overlay--blocks-sync` markiert gezielt die zwei
     Dialoge, die das schon vor #348 taten: Karten-Modal (modal.js) und
     Einstellungen (configdialog.js) — am Parent-Commit 3d96506 nachgemessen. */
  const modal = document.querySelector('.modal-overlay--blocks-sync[aria-hidden="false"]');
  if (!modal) {
    applyExternalState(newState);
    return;
  }
  /* Modal offen → zwischenspeichern + Badge zeigen */
  Sync.setPending(newState);
  showSyncBadge();
  /* MutationObserver wartet auf Modal-Schließen */
  const obs = new MutationObserver(() => {
    if (modal.getAttribute('aria-hidden') === 'true') {
      obs.disconnect();
      const pending = Sync.takePending();
      if (pending) applyExternalState(pending);
    }
  });
  obs.observe(modal, { attributes: true, attributeFilter: ['aria-hidden'] });
}

function showSyncToast() {
  document.querySelector('.sync-toast')?.remove();
  const toast = document.createElement('div');
  toast.className = 'sync-toast';
  toast.textContent = I18n.t('sync.updated');
  document.body.appendChild(toast);
  setTimeout(() => toast.remove(), 2500);
}

function showExportToast(msg) {
  document.querySelector('.export-toast')?.remove();
  const toast = document.createElement('div');
  toast.className = 'sync-toast export-toast';
  toast.setAttribute('aria-hidden', 'true'); /* F-2862: angesagt wird über die Live-Region (ansage.js) */
  toast.textContent = msg;
  document.body.appendChild(toast);
  ansagen(msg);
  setTimeout(() => toast.remove(), Math.min(8000, Math.max(2500, msg.length * 60))); /* F-2990: lange Sätze länger */
}

/* ADR-0032 Karte #54: Toast für Auto-Entsperren (kein Auto-Mutate, nur Info) */
function showUnlockedToast(msg) {
  const toast = document.createElement('div');
  toast.className = 'sync-toast unlocked-toast';
  toast.textContent = msg;
  document.body.appendChild(toast);
  setTimeout(() => toast.remove(), 3500);
}

function showSyncBadge() {
  if (document.querySelector('.sync-badge')) return;
  const badge = document.createElement('div');
  badge.className = 'sync-badge';
  badge.textContent = I18n.t('sync.pending');
  document.body.appendChild(badge);
  /* Badge entfernt sich wenn Modal schließt und State applied wird */
  const obs = new MutationObserver(() => {
    /* F-749: derselbe verengte Selektor wie in onExternalUpdate — Badge
       verschwindet erst, wenn der Dialog schliesst, der es ausgelöst hat. */
    const modal = document.querySelector('.modal-overlay--blocks-sync[aria-hidden="false"]');
    if (!modal) { badge.remove(); obs.disconnect(); }
  });
  obs.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['aria-hidden'] });
}

EventBus.on('sync:pick-directory', async () => {
  /* Sprint-51-Kritiker (K-A): try/finally um den ganzen Handler. `pickDirectory`
     setzt ein Gate, das Tick- und Schreibpfad anhält; entschieden wird es erst
     weiter unten per commit/revert. Wirft irgendetwas dazwischen — und dieser
     Handler ruft `State.set`, was synchron den Renderer auslöst —, bliebe das
     Gate ohne diesen Rückweg dauerhaft zu und der Projektor des Tabs wäre still
     tot. `EventBus.emit` isoliert Listener nicht, die Ausnahme käme also auch
     nirgends sichtbar an. Der Rückweg im finally ist bewusst der Revert und
     nicht der Commit: wenn wir nicht wissen, ob der Wechsel gültig war, ist der
     alte Zustand die sichere Annahme. Nach einem erfolgreichen
     `commitPickedDirectory` ist der Slot leer und der Revert ein No-Op. */
  let name;
  try {
    name = await Sync.pickDirectory(State.get()?.board?.id);
  } catch (e) {
    await Sync.revertPickedDirectory().catch(() => {});
    throw e;
  }
  if (!name) { await Sync.revertPickedDirectory().catch(() => {}); return; }
  try {

  /* ADR-0028: aboard-sync.json hat Vorrang vor HTML-Embedding */
  const raw = await Sync.read();
  if (raw === Sync.READ_ERROR) {
    /* F-477: Lesen ist fehlgeschlagen, die Datei existiert vermutlich
       trotzdem (transienter I/O-Fehler, Berechtigungs-Grenzfall) — das ist
       NICHT dasselbe wie "Datei fehlt" (raw === null, Zweig unten). Würde
       dieser Fall in den "fehlt"-Zweig durchfallen, überschriebe
       `Sync.write(State.get())` dort eine vorhandene, nur gerade unlesbare
       Datei mit dem lokalen Board — die Verlustklasse aus F-432 eine
       Verzweigung tiefer. Stattdessen: abbrechen, Handle zurückdrehen,
       nichts anfassen. */
    await Sync.revertPickedDirectory();
    showExportToast(I18n.t('sync.read-error'));
    return;
  }
  if (raw !== null) {
    /* F-187: Migration via Plugin-Interface kapseln — kein direktes Storage.checkVersion */
    const validated = raw ? StoragePlugin.getActivePlugin().parseAndMigrate(raw) : null;
    if (validated?.board) {
      /* F-429: Dirty-Guard. Bis F-427 war dieser Einstieg nur aus claude-code
         erreichbar; jetzt liegt er im Erstkontakt jedes Solo-Nutzers, wo ein
         Fehlgriff beim Ordner am wahrscheinlichsten ist. Ohne Rückfrage würde
         ein ungespeichertes lokales Board kommentarlos durch das Projekt-Board
         ersetzt (Muster analog `sync:switch-to-board`). */
      if (State.isDirty() && !confirm(I18n.t('dirty.confirm-switch'))) {
        /* F-432 (Karte #372): „Nicht wechseln" muss auch den Handle
           zurückdrehen — `pickDirectory` hat oben bereits umgeschaltet und
           in IndexedDB persistiert. Ohne den Rückweg bliebe der Abbruch eine
           Behauptung: der nächste autoPersist schriebe in den eben gewählten
           Ordner, im Projekt-Modus ohne localStorage-Puffer (ADR-0014). */
        await Sync.revertPickedDirectory();
        return;
      }
      /* F-430: `boardMode` VOR `State.set` — `State.set` emittiert synchron
         `state:changed` → `debouncedPersist`, das `getActivePlugin()` ebenfalls
         synchron auflöst. In der alten Reihenfolge war das noch das Standalone-
         Plugin → `Storage.autoSave` hätte das fremde Projekt-Board in den
         localStorage-Puffer geschrieben (ADR-0014: im Projekt-Modus aus). */
      Config.set('boardMode',   'claude-code');
      BulkSelect.clearAll(); /* wie board:open */
      State.set(validated);
      Config.set('projectName', name);
      await Sync.setBoardId(validated.board.id); /* Handle der echten board.id zuordnen */
      /* Sprint-51-Kritiker (K5): Diff-Bezug und Dirty-Flag auf das NEUE Projekt
         setzen. Ohne das zeigt `lastAdopted` weiter auf die Projektion des
         vorherigen Projekts, und `State.set` hat `_dirty` gesetzt — der nächste
         `debouncedPersist` diffte also Alt-Board gegen Neu-Board und hängte
         Lösch-Ops für sämtliche Alt-Karten an die Inbox des NEUEN Projekts.
         Kein Datenverlust (die IDs existieren dort nicht), aber Inbox-Müll und
         ab 20 Karten ein Mass-Delete-Guard-Fehlalarm direkt nach dem Wechsel.
         (V1): den Pick-Slot schliessen — der Wechsel gilt. */
      Sync.commitPickedDirectory();
      Sync.markAdopted(validated);
      State.markClean();
      showExportToast(I18n.t('sync.using-existing'));
      Sync.startPolling(onExternalUpdate);
      EventBus.emit('config:project-changed');
      return;
    }
    /* Existiert + invalides Schema → Abbruch, kein Overwrite.
       F-432 (Karte #372): Der Abbruch war bis hierher unwahr — `pickDirectory`
       hatte den Handle schon umgeschaltet und gespeichert, die Meldung „Datei
       ungültig" ließ den Nutzer aber glauben, es sei nichts passiert. Erst der
       Rückweg macht die Aussage wahr. Gleiches Muster wie F-457/K2 beim
       Board-Wechsel, hier an der zweiten Stelle. */
    await Sync.revertPickedDirectory();
    showExportToast(I18n.t('sync.invalid-file'));
    return;
  }

  /* Datei existiert nicht → Nutzer fragen: leeres Board oder aktuelle Daten? */
  const wantEmpty = confirm(I18n.t('sync.confirm-empty'));
  if (wantEmpty) {
    BulkSelect.clearAll(); /* wie board:open */
    State.set({ version: '2', board: Model.createBoard(document.title || 'Board') });
  }
  Config.set('boardMode',   'claude-code');
  Config.set('projectName', name);
  await Sync.setBoardId(State.get().board.id);
  /* F-294: bewusst KEIN persistGated — dieser Zweig erreicht nur den Fall
     „Sync-Datei existiert NICHT" (raw === null oben → return). Ohne Datei
     gibt es kein aboard-sync.base.json → das Board ist per Definition
     nicht migriert; persistGated fiele ohnehin auf write() zurück. Erst-
     anlage einer frischen Sync-Datei, kein Projektions-Clobber möglich. */
  await Sync.write(State.get());
  Sync.commitPickedDirectory();          /* V1: auch dieser Zweig ist ein Erfolg */
  Sync.startPolling(onExternalUpdate);
  EventBus.emit('config:project-changed');
  } finally {
    /* No-Op nach commitPickedDirectory; greift nur, wenn oben etwas geworfen
       hat oder ein Pfad ohne Entscheidung zurückkehrt (K-A). */
    await Sync.revertPickedDirectory().catch(() => {});
  }
});

EventBus.on('sync:request-permission', async () => {
  const granted = await Sync.requestPermission();
  if (granted) {
    Sync.startPolling(onExternalUpdate);
    EventBus.emit('config:project-changed');
  }
});

EventBus.on('sync:disable', () => {
  Sync.stopPolling();
  Sync.clearActiveHandle();
  Config.set('boardMode',   'solo');
  Config.set('projectName', '');
  EventBus.emit('config:project-changed');
});

/* ADR-0028: Wechsel zu anderem bekannten Board */
EventBus.on('sync:switch-to-board', async ({ boardId }) => {
  /* Dirty-Guard: vor Board-Wechsel prüfen */
  if (State.isDirty()) {
    const isAutoSave = (IS_PROJECTOR_BUILD && Profile.has('feature.polling') && Sync.isActive())
                    || (IS_SERVER_BUILD && Profile.has('feature.restStorage'));
    if (isAutoSave) {
      /* Server- oder Projekt-Modus mit Auto-Save: pending Save flushen */
      await debouncedPersist.flush();
      if (await sitzungSperrtWechsel()) return; /* #1053, Kritik B-1 */
    } else {
      /* Standalone: Nutzer-Bestätigung */
      const saveFirst = confirm(I18n.t('dirty.save-first'));
      if (saveFirst) {
        const dirHandle = Profile.has('feature.polling') ? Sync.getDirectoryHandle() : null;
        await Storage.save(State.get(), dirHandle);
      } else {
        if (!confirm(I18n.t('dirty.confirm-switch'))) return;
      }
    }
  }
  /* Aktuelle Änderungen flushen, falls Sync aktiv. F-294: migriert →
     Projektor (Diff→Ops→Inbox), nicht migriert → Legacy-write. Auf einem
     migrierten Board würde ein roher write() base+Inbox umgehen. */
  if (IS_PROJECTOR_BUILD && Sync.isActive()) {
    try { await Sync.persistGated(State.get(), { actor: Config.get('userName') || '' }); } catch {}
  }

  /* F-192: Plugin-Interface aufrufen statt Pfad inline zu kopieren (ADR-0044
     §"Aufrufer-Schicht"). switchBoard() kapselt stopPolling, clearActiveHandle,
     tryRestoreHandle, Permission-Request, read und Migration. Aufrufseitige
     Aufräumarbeit (clearActiveHandle bei Fehler) übernimmt das Plugin selbst,
     daher entfällt das success-Flag aus F-101 — clearActiveHandle wird in
     allen Plugin-Fehlerpfaden bereits gerufen. */
  const result = await StoragePlugin.getActivePlugin().switchBoard(boardId);
  if (!result.ok) {
    const key = result.error === 'no-handle'         ? 'sync.no-handle-for-board'
              : result.error === 'permission-denied' ? 'sync.permission-needed'
              : result.error === 'permission-hangs'  ? 'sync.permission-hangs'
              /* F-854: transienter Lesefehler ist nicht "Datei kaputt" — der
                 Nutzer soll es erneut versuchen, nicht die Datei verdaechtigen. */
              : result.error === 'read-error'        ? 'sync.read-error'
              :                                        'sync.invalid-file';
    /* F-2977: abgelaufene Sitzung — das Band steht schon, keine zweite (irreführende) Meldung. */
    if (!(IS_SERVER_BUILD && result.sitzungAbgelaufen)) showExportToast(I18n.t(key));
    /* F-457 (Sprint-50-Kritiker, K2): Ausgangszustand wiederherstellen.
       Das FSA-Plugin reisst in `switchBoard` ZUERST ab (stopPolling +
       clearActiveHandle) und probiert das neue Ziel ERST DANACH. Scheitert
       es — Berechtigung im Prompt abgelehnt, Datei transient unlesbar —,
       stand der Nutzer bisher ohne Sync da: Board weiter sichtbar und
       editierbar, aber `Sync.isActive()` false, jeder Save läuft ins Leere,
       und im Projekt-Modus gibt es per ADR-0014 keinen localStorage-Puffer,
       der das auffängt. Der Verlust wäre still bis zum Reload.
       Bis Karte #366 war dieser Pfad nur per Sidebar-Klick erreichbar (und
       die Sidebar listet nur Boards, die es wirklich gibt); seit dem
       Deep-Link-Router löst ihn jede URL aus — deshalb jetzt der Rückweg.
       `tryRestoreHandle()` ohne Argument holt den Handle dieses Deployments
       (ADR-0048-Scope), also genau den, der vorher aktiv war.
       Server-Modus braucht das nicht: dessen `switchBoard` fasst bei einem
       Fehlschlag nichts an. */
    if (!(IS_SERVER_BUILD && Profile.has('feature.restStorage'))) {
      try {
        if (await Sync.tryRestoreHandle()) {
          /* F-464 (Karte #372): `tryRestoreHandle` liefert auch `true`, wenn
             die Berechtigung auf `prompt` steht. Der Handle ist dann zwar
             zurück, `Sync.isActive()` bleibt aber false — ein Polling darauf
             zu starten erzeugt genau den stillen Zustand, den der Rückweg
             verhindern soll: es sieht wiederhergestellt aus, geschrieben wird
             nichts. Stattdessen den Nutzer zum Berechtigungs-Prompt führen
             (die Sidebar zeigt den Button, sobald neu gerendert wurde). */
          if (Sync.isPermissionNeeded()) {
            showExportToast(I18n.t(Sync.isFreigabeHaengend?.() ? 'sync.permission-hangs' : 'sync.permission-needed'));
          } else {
            /* F-464, zweiter Teil: `clearActiveHandle()` hat `activeBoardId`
               genullt, und `tryRestoreHandle()` OHNE Argument setzt sie nicht
               wieder — der Sync wüsste danach nicht mehr, zu welchem Board
               sein Handle gehört. Das angezeigte Board hat sich nicht
               geändert, also die ID von dort nachziehen. */
            const zurueck = State.get()?.board?.id;
            if (zurueck) await Sync.setBoardId(zurueck);
            Sync.startPolling(onExternalUpdate);
          }
          EventBus.emit('config:project-changed');
        }
      } catch { /* best effort — schlimmer als vorher wird es dadurch nicht */ }
    }
    return;
  }
  State.set(result.state);
  /* F-223 (Karte #162/#201): Board-Swap → Module-Singletons in Renderer zurücksetzen.
     Direkt + via board:loaded-Event (Renderer-Listener, Karte #201). */
  Renderer.resetSwimlaneState();
  EventBus.emit('board:loaded');
  if (IS_SERVER_BUILD && Profile.has('feature.restStorage')) {
    BootHinweis?.entferne(); /* F-1453: ein Server-Board ist geladen — Speichern ist wieder möglich (switchBoard hebt die Sperre) */
    bezugIstLokal(); /* #566: switchBoard hat den Bezug auf genau diesen Stand gesetzt */
    /* REST-Modus: boardId im Plugin aktualisieren + Polling neu starten.
       #328: IS_SERVER_BUILD-Gate → DCE im slim Distributable. */
    await StoragePlugin.getActivePlugin().preload(result.state?.board?.id);
    StoragePlugin.getActivePlugin().stopPolling();
    if (Config.get('serverPollEnabled') !== false) {
      StoragePlugin.getActivePlugin().startPolling(onExternalUpdate);
    }
  } else {
    /* FSA-Projekt-Modus */
    Config.set('boardMode',   'claude-code');
    Config.set('projectName', Sync.getDirectoryName() || '');
    Sync.startPolling(onExternalUpdate);
  }
  showExportToast(I18n.t('sync.using-existing'));
  EventBus.emit('config:project-changed');
});

/* ADR-0028: Reset auf leeres Board (Notausgang für Standalone-Nutzer).
   Aktiven Handle dem neuen Board zuordnen, damit der Sync-Ordner erhalten bleibt.
   Karte #207 / F-265: Im Server-Modus ist Reset strukturell unterbunden (UI-Hide
   in configdialog.js), aber zur Sicherheit auch hier abgesichert — falls der
   Event aus einem unbekannten Pfad kommt. */
EventBus.on('board:reset', async () => {
  if (IS_SERVER_BUILD && Profile.has('feature.restStorage')) {
    /* Im Server-Modus darf kein Reset durchgeführt werden — würde das geteilte
       Board für ALLE Mitglieder löschen. Lautlos abbrechen (UI-Button ist ausgeblendet,
       dieser Pfad sollte nicht erreichbar sein). #328: IS_SERVER_BUILD-Gate → DCE im slim. */
    return;
  }
  /* F-294 (ADR-0067 Stufe 2): board:reset auf einem migrierten Board ist
     semantisch ein base-Reset (leeres Board mit NEUER board.id). Das passt
     nicht in das append-only Op-Modell — ein projectorPersist erzeugte
     einen Lösch-Diff gegen ein fremdes Board, ein roher force-write
     clobbert die Projektion (F-283-Klasse). Die base-Reset-Semantik ist
     eine bewusste 3-K-Entscheidung der Stufe-2-Kompaktierungs-ADR. Bis
     dahin: hart stoppen, BEVOR State mutiert wird — kein halber Zustand
     (UI leer / Disk voll), Board bleibt unverändert. */
  if (IS_PROJECTOR_BUILD && Profile.has('feature.polling') && Sync.isActive() && await Sync.isMigrated()) {
    showExportToast(I18n.t('sync.reset-migrated-deferred'));
    return;
  }
  const fresh = { version: '2', board: Model.createBoard('Board') };
  State.set(fresh);
  /* F-227 (Kritiker Sprint 27): collapsedColumns-Config kennt nur sprintId/colId,
     nicht das Board — beim Reset bleiben Keys des alten Boards bestehen und
     können fälschlich auf das frische Board matchen (col-backlog ist deterministisch).
     Komplett purgen, dann Renderer-State zurücksetzen. */
  Config.set('collapsedColumns', []);
  Renderer.resetSwimlaneState();
  EventBus.emit('board:loaded');
  if (IS_PROJECTOR_BUILD && Profile.has('feature.polling') && Sync.isActive()) {
    await Sync.setBoardId(fresh.board.id);
    /* F-247 / ADR-0050: board:reset ist eine bewusste destruktive
       User-Aktion — Read-before-Write-Guard hier umgehen, sonst würde der
       Reset bei abweichendem Disk-Stand still verworfen.
       F-294: nur noch nicht-migrierter Pfad — migrierte Boards sind oben
       hart gestoppt (base-Reset = 3-K, Stufe-2-Kompaktierungs-ADR). */
    await Sync.write(fresh, { force: true });
  }
});

/* ── App-Init ────────────────────────────────────────────────────── */

(async function init() {
  /* ADR-0044, Karte #67: Persistenz geht durch das aktive Storage-Plugin —
     Standalone schreibt in localStorage, Projekt in aboard-sync.json.
     F-180: Plugin-Referenz wird beim Enqueue gesnapshotted (durch den
     Wrapper unten), nicht beim Timer-Feuern — damit ein Modus-Wechsel
     zwischen Enqueue und Persist nicht den State an die falsche Destination
     schreibt. Der Wrapper-Aufrufer übergibt das Plugin als zweites Argument. */
  const debouncedPersistInner = debounce(async (state, plugin) => {
    const genBeimSenden = _aenderungsGen;
    const result = await plugin.autoPersist(state);
    /* Vertrag: boolean (FSA-Plugins) oder { ok, conflict? } (REST-Plugin bei 409) */
    const ok = result === true || result?.ok === true;
    if (ok) {
      if (IS_SERVER_BUILD && plugin.kind === 'rest') {
        restGesichert(plugin, result, genBeimSenden);
      } else {
        State.markClean();
      }
    } else if (typeof result === 'object' && result?.conflict) {
      const conflict = result.conflict;
      if (conflict.kind === 'fsa') {
        /* F-247 / ADR-0050: aboard-sync.json wurde extern geändert.
           „Meine Version" → Guard umgehen und überschreiben.
           „Disk-Version" → Baseline auf Disk heben, dann adoptieren. */
        showConflictBanner(
          async () => {
            /* F-294: Projektor-Pfad auf migrierten Boards (sonst F-283-
               Klasse). Auto-Save-Konflikt fsa entsteht nur im nicht-
               migrierten autoPersist-Zweig — defensiv gegated. */
            const w = await Sync.persistGated(State.get(), { force: true, actor: Config.get('userName') || '' }).catch(() => false);
            if (w === true) { State.markClean(); showSaveConfirm('fsa'); }
            else showExportToast(I18n.t('conflict.retry-failed'));
          },
          () => {
            Sync.markBaseline(conflict.diskRaw);
            State.set(conflict.serverState);
            State.markClean();
          },
        );
      } else if (IS_SERVER_BUILD) {
        /* 409 im Hintergrund-Save (REST) — gleicher Banner wie beim manuellen Save */
        showConflictBanner(
          () => behalteMeine(plugin, State.get()),
          () => uebernimmServerStand(plugin, conflict.serverState),
        );
      }
    } else {
      /* F-251 / B2: kein Erfolg UND kein Konflikt (z. B. Netz weg oder
         persistenter 428 trotz ETag-Refresh in putBoard). Nicht still
         verschlucken — State bleibt dirty (kein markClean), der nächste
         Persist-Versuch folgt; der User bekommt einen sichtbaren Hinweis. */
      if (IS_SERVER_BUILD && result?.gesperrt) { /* F-1453: der dauerhafte Hinweis steht schon */ }
      else if (IS_SERVER_BUILD && result?.sitzungAbgelaufen) { /* #1053: Hinweis steht schon */ }
      else if (IS_SERVER_BUILD && result?.erneutNachMs) restNichtGesichert(result);
      else if (IS_SERVER_BUILD && result?.abgewiesen) zeigeAbgewiesen(result.abgewiesen);
      else showExportToast(I18n.t('conflict.retry-failed'));
    }
  }, 500);
  debouncedPersist = Object.assign(
    (state) => debouncedPersistInner(state, StoragePlugin.getActivePlugin()),
    IS_SERVER_BUILD
      ? { flush: () => debouncedPersistInner.flush(), cancel: () => debouncedPersistInner.cancel(),
          idle: () => debouncedPersistInner.idle() }
      : { flush: () => debouncedPersistInner.flush() }
  );

  function updateDocTitle(state) {
    if (!state?.board?.title) return;
    document.title = (State.isDirty() ? '* ' : '') + state.board.title;
  }

  EventBus.on('state:changed', state => {
    Renderer.renderBoard(state);
    Shortcuts.onBoardRender();
    /* F-254/ADR-0055: extern angewandter (autoritativer) Stand löst keinen
       Echo-PUT aus — sonst Poll→State.set→PUT→Server-Bump→Poll-Schleife. */
    if (!_applyingExternalState) {
      if (IS_SERVER_BUILD) _aenderungsGen++; /* #566 */
      debouncedPersist(state);
    }
    updateDocTitle(state);
  });

  /* Titel auch aktualisieren wenn dirty-Flag sich ändert (nach Persist) */
  EventBus.on('state:dirty-changed', () => {
    updateDocTitle(State.get());
    /* Header-Stern synchron aktualisieren — kein voller Re-Render (#86) */
    const titleEl = document.getElementById('board-title');
    const board   = State.get()?.board;
    if (titleEl && board) {
      titleEl.textContent = (State.isDirty() ? '* ' : '') + board.title;
    }
  });

  Config.load();
  I18n.setLang(Config.get('language'));

  /* ADR-0044 / F-193: Plugin-Lifecycle-Hook vor jeder Persistenz-Aktion.
     Phase 1 (Standalone/Project): No-Op. Phase 2 (REST-Plugin): Auth-Setup. */
  await StoragePlugin.getActivePlugin().init();

  /* ADR-0070 §3 (F-286): Live-State-Provider für den projectorPoll-Pre-Flush.
     Einmalig, board-unabhängig; nur vom Projektor-Pfad migrierter FSA-Boards
     genutzt (sonst inert). Sichert dirty non-modal Edits als Ops, bevor ein
     externer Poll-Adopt sie clobbern könnte. */
  Sync.setLocalStateProvider(() => ({
    dirty: State.isDirty(),
    state: State.get(),
    actor: Config.get('userName') || '',
  }));

  /* FSA File-Handle vorab laden — board.id steht erst nach Storage.load() fest */

  DragDrop.init();
  Modal;
  ConfigDialog;
  Sidebar.init(); /* Karte #176: App-Sidebar mounten + EventBus subscriben */

  /* State zuerst laden — board.id wird für board-spezifischen Sync-Handle gebraucht (ADR-0028) */
  const { state: rawState, fromLocalStorage } = Storage.load();
  /* ADR-0041: Spawn fälliger wiederkehrender Karten beim Board-Load */
  if (rawState?.board) {
    const today = new Date().toISOString().slice(0, 10);
    spawnRecurringCards(rawState.board, today);
  }
  const state = rawState;
  const embeddedBoardId = state?.board?.id;

  /* FSA File-Handle vorab laden — pro board.id, Legacy-Slot wird migriert */
  if (embeddedBoardId) Storage.preloadFileHandle(embeddedBoardId);

  /* Polling-Init je nach Profil: Server-Modus → REST-Plugin, claude-code → FSA-Sync.
     stateResolved: der Server-Modus-Bootstrap übernimmt seine eigene
     State.set/markClean-Logik — der generische Block unten läuft dann nicht. */
  let stateResolved = false;
  if (IS_SERVER_BUILD && Profile.has('feature.restStorage')) {
    /* Server-Modus (ADR-0054, F-253): adopt-or-create VOR dem Verwenden der
       lokal gewürfelten Board-id. Der Server liefert sein Frontend mit
       geleertem #app-data (F-249) → state ist hier i. d. R. null; das lokale
       Kandidaten-Board ist nur der Create-Vorschlag, falls der Server leer
       ist. Behebt L1 (Server legt Board nie an) + L2 (Reload würfelt id). */
    const localCandidate = state ?? { version: '2', board: Model.createBoard(document.title || 'Board') };
    const boot = await StoragePlugin.getActivePlugin().adoptOrCreate(localCandidate);
    if (boot.ok) {
      State.set(boot.state);
      bezugIstLokal(); /* #566: L = geladener Stand = Bezug (bis auf Migrationen, die als Ops folgen) */
      if (!boot.adopted) showExportToast(I18n.t('sync.server-board-created'));
    } else {
      /* Server nicht erreichbar / Bootstrap gescheitert. F-1453: Das lokale
         Board ist dem Server unbekannt (Zufalls-id), jedes Speichern liefe in
         ein 428 — Änderungen wären beim Reload still weg. Deshalb: dauerhafter
         Hinweis (kein 2,5-s-Toast) und das Plugin verweigert das Speichern.
         Bewusst KEIN automatisches Anlegen eines Server-Boards (F-1440). */
      State.set(localCandidate);
      StoragePlugin.getActivePlugin().sperreSpeichern?.();
      BootHinweis?.zeige();
    }
    State.markClean();
    if (Config.get('serverPollEnabled') !== false) {
      StoragePlugin.getActivePlugin().startPolling(onExternalUpdate);
    }
    stateResolved = true;
  } else if (Profile.has('feature.polling')) {
    /* F-428: KEIN `IS_PROJECTOR_BUILD`-Gate mehr. Der FSA-Bootstrap ist die
       Voraussetzung dafür, dass claude-code einen Reload überlebt — ohne
       `Sync.init()` gibt es keinen Handle-Restore, `Sync.isActive()` bleibt
       false, und das Projekt-Plugin (kein localStorage-Fallback, ADR-0014)
       kann nicht mehr speichern. Das #328-Gate beruhte auf der Annahme, das
       slim Bundle sei nie in claude-code — F-427 hebt diese Annahme auf.
       FSA-Basis ≠ Projektor-Schicht: `Sync.persistGated`/`isMigrated` bleiben
       gegated, `Sync.init`/`isActive` nicht.

       FSA-Projekt-Modus (claude-code): Directory-Handle aus IDB wiederherstellen.
       ADR-0106 (#331, Bombe-Pfad 2): `embeddedBoardId` kann undefined sein, wenn
       das vite-Plugin (#330) `#app-data` auf `{}` leert (leeres embedded-Board).
       Frühere Bedingung `&& embeddedBoardId` ließ Sync.init überspringen → frisches
       Board im State → Diff-Bombe (287 card.delete). `embeddedBoardId` wird jetzt
       als nachrangiger Hint an Sync.init übergeben (Deployment-Scope-Keying nach
       ADR-0048 nutzt ihn ohnehin nicht primär); bei undefined schadet er nichts. */
    const restored = await Sync.init(onExternalUpdate, embeddedBoardId);
    if (restored && Sync.isPermissionNeeded()) {
      showPermissionBanner(Sync.isFreigabeHaengend?.());
    } else if (restored && !state && Sync.isActive()) {
      /* F-357 (Sprint-41-Kritiker, Re-Install von 0d7cd40): state war null
         (embedded leer + kein localStorage), aber FSA-Handle existiert —
         Board-State direkt aus aboard-sync.json laden. Ohne diesen Pfad
         würde unten in Z. ~1451 ein frisches Board mit neuer UUID erzeugt
         → kein Bezug zum FSA-Handle, Reload zeigt leer; und der erste
         Sync-Save würde einen Mass-Delete-Diff gegen die existierende
         Projektion erzeugen (Diff-Bombe Pfad 2, zweite Schutzschicht zu
         Mass-Delete-Guard aus ADR-0106). */
      try {
        /* F-795 / Karte #449 — Quellen-Mismatch: Auf einem MIGRIERTEN Board
           ist `fold(base + inbox)` die Wahrheit (ADR-0067 §2), die Projektion
           `aboard-sync.json` nur deren Abschrift. Bis Sprint 62 las dieser
           Pfad die Abschrift, während `projectorPersist` seinen Diff-Bezug
           aus dem Fold nimmt — zwei Wahrheitsquellen im selben Prozess.
           Hatte eine ältere App die Projektion degradiert (ihr `fold` merzte
           beim Falten ein Feld aus, das sie nicht kannte), sah die Differenz
           zwischen beiden aus wie eine bewusste Rücknahme durch den Nutzer
           und wurde als reguläre, gültige Op geschrieben: kein Poison, keine
           Projektor-Meldung, kein Wächter. Genau so ist am 2026-08-28 im
           einem Konsumenten-Projekt eine Swimlane-Archivierung
           verschwunden.

           Deshalb hier zuerst der Fold. Der Rückfall auf die Projektion
           bleibt: `projectorReconcile()` liefert auch dann `null`, wenn das
           CLI zwischen base- und Inbox-Read kompaktiert hat (F-457) — in dem
           Fall ist die Projektion der beste verfügbare Stand, und der nächste
           Poll-Tick versöhnt ohnehin mit frischer base. */
        let syncData = null;
        if (await Sync.isMigrated()) {
          const gefaltet = await Sync.projectorReconcile();
          if (gefaltet?.board) syncData = Storage.checkVersion(gefaltet);
        }
        /* F-477: `raw` ist bei einem Lesefehler das Sentinel `Sync.READ_ERROR`
           (truthy!) statt `null` — ohne die explizite Ungleichprüfung würde
           `if (raw)` hier zuschlagen und `parseAndMigrate` auf dem Symbol
           aufrufen. Das äußere `catch {}` fängt die daraus resultierende
           Ausnahme zwar ab, aber implizit über einen Fehlerpfad zu laufen ist
           kein Fix — bei Lesefehler bleibt `stateResolved` bewusst `false`,
           genau wie beim vorherigen `raw === null`-Verhalten (kein
           Overwrite, kein Fortschritt an dieser Stelle). */
        if (!syncData) {
          const raw = await Sync.read();
          if (raw && raw !== Sync.READ_ERROR) {
            syncData = StoragePlugin.getActivePlugin().parseAndMigrate(raw);
          }
        }
        if (syncData?.board) {
          State.set(syncData);
          State.markClean();
          Sync.markAdopted(State.get());
          stateResolved = true;
        }
      } catch {}
    } else if (!restored && embeddedBoardId) {
      /* Kein passender Handle für dieses Board — Projekt-Modus passiv halten */
      showExportToast(I18n.t('sync.no-handle-for-board'));
    }
  }

  if (!stateResolved) {
    const projektAktiv = Profile.has('feature.polling') && Sync.isActive();
    const freigabeFehlt = Profile.has('feature.polling') && Sync.isPermissionNeeded();
    if (fromLocalStorage && state && freigabeFehlt && !projektAktiv) {
      /* F-3101/#1175: nur der Freigabe-Banner (oben); Kopie weder zeigen noch
         laden noch verwerfen noch als Bezug übernehmen. */
      State.markClean();
    } else if (recoveryBannerNoetig(fromLocalStorage, state, projektAktiv, freigabeFehlt)) {
      showRecoveryBanner(state);
      /* Recovery-Banner zeigt an — dirty bleibt false, User entscheidet selbst */
      State.markClean();
    } else if (fromLocalStorage && state && projektAktiv) {
      /* Karte #1170: Projektordner verbunden, im Browser liegt eine alte
         Zwischenkopie. Die Kopie wird hier NIE angezeigt und NIE geschrieben
         (ADR-0106: ein Diff gegen einen veralteten Bezug wäre die Bombe) —
         auch nicht, wenn die Projektdatei unlesbar ist, fehlt oder kein Board
         trägt. Nur wenn dort ein Board steht, gilt dieses und die Kopie geht
         still weg; sonst Hinweis, nichts geladen — und die Kopie bleibt
         erhalten (Kritik 20.11. K-6: sie kann der einzige Stand sein). */
      await uebernehmeOrdnerStand(state);
    } else if (state) {
      /* Im Projekt-Modus: aboard-sync.json bevorzugen wenn neuer.
         F-428: entgatet — sonst zeigt das slim Template in claude-code den
         lokalen Stand statt des Projekt-Boards (F-318-Symptom, ein Reload
         später). `persistGated` fällt ohne Projektor auf write() zurück. */
      if (Profile.has('feature.polling') && Sync.isActive()) {
        try {
          const raw = await Sync.read();
          /* F-294: Initial-Load-Reconcile schreibt den Embedded-/
             localStorage-Stand in die Sync-Destination, wenn die
             Sync-Datei fehlt/invalide ist. Auf migrierten Boards muss
             das durch den Projektor (Diff→Ops→Inbox), nie roh — sonst
             clobbert der Browser-Memory die Projektion (F-283-Klasse). */
          const reconcileActor = Config.get('userName') || '';
          if (raw === Sync.READ_ERROR) {
            /* F-477: Der eigentliche Verlustfall. Vorher fiel ein Lesefehler
               (raw === null, ununterscheidbar von "Datei fehlt") in den
               `else`-Zweig unten — der schreibt IMMER `state` (den lokalen/
               embedded Stand) via persistGated in die Sync-Destination.
               War die Datei nur transient unlesbar, aber vorhanden, wurde
               sie damit überschrieben. Jetzt: lokalen Stand nur ANZEIGEN,
               NICHT persistieren — kein Schreiben, bis ein künftiger Poll
               die Datei wieder lesen kann. */
            State.set(state);
          } else if (raw) {
            /* F-187: Migration via Plugin-Interface kapseln — kein direktes Storage.checkVersion */
            const syncData = StoragePlugin.getActivePlugin().parseAndMigrate(raw);
            State.set(syncData?.board ? syncData : state);
            if (!syncData?.board) await Sync.persistGated(state, { actor: reconcileActor });
          } else {
            /* raw === null: Datei existiert nachweislich nicht (F-477
               unterscheidet das jetzt von einem Lesefehler) — Erstanlage
               ist hier weiterhin der richtige Schritt. */
            State.set(state);
            await Sync.persistGated(state, { actor: reconcileActor });
          }
        } catch {
          State.set(state);
        }
      } else {
        State.set(state);
      }
      /* Initial-Load abgeschlossen — kein ungespeicherter Stand */
      State.markClean();
      /* ADR-0067: den initial angezeigten Stand als Projektor-Diff-Bezug
         markieren — der erste User-Edit-Diff läuft sonst gegen einen leeren
         Bezug. (Der Sofort-projectorPoll aus startPolling deckt das zwar
         ebenfalls ab; dies macht den Bezug unabhängig vom Async-Timing.) */
      if (Profile.has('feature.polling') && Sync.isActive()) {   /* F-428: entgatet */
        Sync.markAdopted(State.get());
      }
    } else {
      if (fromLocalStorage && state?.board?.id) {
        Storage.discardAutosave(state.board.id); /* ADR-0087: deployment-gescopt (F-318) */
      }
      State.set({ version: '2', board: Model.createBoard(document.title || 'Board') });
      State.markClean();
    }
  }

  /* ADR-0028: Hash-Routing — Direkt-Link auf Karte (#card-42).
     #308/ADR-0103: zusätzlich Kalender-Deep-Link (#/calendar?...). */
  handleCardHash();
  CalendarView.maybeRouteFromHash();
  window.addEventListener('hashchange', handleCardHash);
  window.addEventListener('hashchange', () => CalendarView.maybeRouteFromHash());

  /* Karte #781 (ADR-0140 §5): Schatten interner Karten auf dem externen Board.
     Wirkt nur auf einer Instanz mit boardpaar.seite = aussen; sonst bleibt es
     ohne Abruf und ohne UI. Im Standalone-/Template-Build fällt es per DCE weg. */
  if (IS_SERVER_BUILD) {
    SchattenAussen.init();
    /* Karte #825: Auf einer externen Kopie stehen die Schatten in den Spalten —
       nach jedem Laden der Schatten das Board neu zeichnen. */
    EventBus.on('schatten:geladen', () => {
      if (!SchattenAussen.kopie()) return;
      Renderer.renderBoard(State.get());
      Shortcuts.onBoardRender();
    });
    /* Karte #827 E4 (Plan D10): /me meldet den Rückweg-Schalter. Ändert er
       sich, sind gebundene Karten anders bedienbar — neu zeichnen. Die erste
       Frage stellt SchattenAussen beim Board-Wechsel; hier nur der Hörer. */
    EventBus.on('boardpaar:rueckweg', () => {
      Renderer.renderBoard(State.get());
      Shortcuts.onBoardRender();
    });
    /* F-2233 (Karte #831): Der Server meldet, ob das offene Board noch eine
       Kopie ist — eine außen entkoppelte Kopie ist frei, die Spiegel-Sperre
       fällt; bei Fehler oder Wiederaufnahme kommt sie zurück. */
    EventBus.on('boardpaar:kopie-frei', () => {
      Renderer.renderBoard(State.get());
      Shortcuts.onBoardRender();
    });
  }
  /* Karte #783 (ADR-0140 §8): Vorschläge des internen Agenten bestätigen oder
     ablehnen — dieselben Bedingungen (Seite außen, Rolle editor), sonst ohne UI. */
  if (IS_SERVER_BUILD) VorschlaegeAussen.init();

  /* Dirty-Guard: Browser-Warn-Dialog beim Tab schließen (nur Standalone-Modus) */
  window.addEventListener('beforeunload', (e) => {
    if (!State.isDirty()) return;
    /* Projekt-Modus (FSA) und Server-Modus (REST): Auto-Save läuft — kein Dialog nötig */
    /* F-428: entgatet — im slim Template mit aktivem FSA läuft Auto-Save
       ebenfalls, der Dialog wäre dort ein Fehlalarm. */
    if ((Profile.has('feature.polling') && Sync.isActive())
        || (IS_SERVER_BUILD && Profile.has('feature.restStorage'))) return;
    e.preventDefault();
    e.returnValue = '';
  });

  /* Tastatur-Shortcuts (ADR-0035): zentrales Shortcut-System initialisieren.
     Ersetzt den ad-hoc '/'-Listener aus Karte #56. */
  Shortcuts.init();
})();

/* F-447 (#366): Ziel eines Deep-Links, der erst einen Board-Wechsel braucht.
   Wird gesetzt, bevor `sync:switch-to-board` läuft, und beim `board:loaded`
   eingelöst — aber NUR, wenn das geladene Board auch das erwartete ist.
   Damit öffnet ein fehlgeschlagener Wechsel (kein Handle, Rechte verweigert,
   Nutzer bricht den Dirty-Dialog ab) niemals versehentlich eine gleichnamige
   Karte des Boards, auf dem wir geblieben sind. */
let pendingDeepLink = null;

function handleCardHash() {
  const ziel = parseCardHash(location.hash);
  if (!ziel) return;
  pendingDeepLink = null;             /* neue Navigation → altes Ziel verfällt */
  /* Karte #743: `#board=…&termin=…` öffnet den Termin in der Kalenderansicht. */
  if (IS_SERVER_BUILD) ziel.terminRef = parseTerminRef(location.hash);

  const state = State.get();
  const aktuellesBoard = state?.board?.id;

  /* Kurzform `#card-42` oder Link auf das ohnehin offene Board → direkt hier. */
  if (!ziel.boardId || !aktuellesBoard || ziel.boardId === aktuellesBoard) {
    if (IS_SERVER_BUILD && ziel.terminRef) { oeffneTermin(ziel.terminRef); return; }
    const card = findCardByRef(state?.board, ziel.cardRef);
    if (card) EventBus.emit('card:edit', card);
    return;
  }

  /* Anderes Board: über denselben Pfad wechseln, den das Board-Menü nutzt —
     inklusive Dirty-Guard, Plugin-Wechsel und Polling-Neustart. Wir bauen
     hier bewusst keinen eigenen Ladeweg. */
  pendingDeepLink = ziel;
  EventBus.emit('sync:switch-to-board', { boardId: ziel.boardId });
}

EventBus.on('board:loaded', () => {
  if (!pendingDeepLink) return;
  const state = State.get();
  const ziel = pendingDeepLink;
  /* F-457 (Sprint-50-Kritiker, V4): Das Ziel verfällt beim ERSTEN
     `board:loaded`, egal ob es gepasst hat. Vorher blieb es unbegrenzt
     scharf, wenn der Wechsel scheiterte (abgelehnte Berechtigung,
     abgebrochener Dirty-Dialog) — und sprang dann Stunden später
     unvermittelt auf, sobald jenes Board auf irgendeinem Weg geladen wurde.
     Ein Deep-Link ist eine Absicht für genau einen Board-Wechsel, nicht ein
     Dauerauftrag. */
  pendingDeepLink = null;
  if (state?.board?.id !== ziel.boardId) return;   /* nicht unser Wechsel */
  if (IS_SERVER_BUILD && ziel.terminRef) { oeffneTermin(ziel.terminRef); return; }   /* #743 */
  if (!ziel.cardRef) return;                       /* `#board=…` ohne Karte */
  const card = findCardByRef(state?.board, ziel.cardRef);
  if (card) EventBus.emit('card:edit', card);
});

function showSaveConfirm(method) {
  const saveBtn = document.getElementById('btn-save');
  if (!saveBtn) return;
  const msg = method === 'download' ? I18n.t('save.downloaded') : I18n.t('save.saved');
  saveBtn.textContent = msg;
  setTimeout(() => { saveBtn.textContent = I18n.t('btn.save'); }, 2500);
}

/* Karte #1053, Kritik B-1: Ein Board-Wechsel bei abgelaufener Sitzung darf die
   ungesendeten Änderungen nicht mit dem neuen Board überschreiben. Erst prüfen,
   ob die Anmeldung inzwischen wieder gilt, dann noch einmal senden; bleibt
   etwas ungesendet, wird nicht gewechselt. true = Wechsel abbrechen. */
function sitzungSperrtWechsel() {
  if (!IS_SERVER_BUILD || !Sitzung) return Promise.resolve(false);
  return Sitzung.sperrtWechsel(() => debouncedPersist.flush());
}

/* Karte #1053: Nach neuer Anmeldung weiter senden, was liegen blieb. */
if (IS_SERVER_BUILD) {
  EventBus.on('sitzung:wieder-da', () => { if (State.isDirty()) debouncedPersist(State.get()); });
}

/* Karte #565 (ADR-0129 §4, Punkt 4): 429/503/Netz beim Ops-Speichern — nach
   der Wartezeit von selbst erneut, nicht erst beim nächsten Tastendruck. Der
   Diff wird dann neu gerechnet, verworfen wird nichts. Gemeinsam für das
   Hintergrund-Speichern und den Speichern-Knopf (K-A1). */
function restNichtGesichert(result) {
  showExportToast(I18n.t('conflict.retry-failed'));
  setTimeout(() => { if (State.isDirty()) debouncedPersist(State.get()); }, result.erneutNachMs);
}

/* Karte #566: Ein Speichern im Server-Modus ist angenommen. Der Bezug
   entspricht jetzt dem Stand zur Generation `gen` — hat der Nutzer seither
   weitergetippt, bleibt L dirty (das nächste Speichern ist schon geplant).
   Danach sofort holen (ADR-0129 §4, Punkt 2); übernommen wird nach der
   Regel in `restEingang`. */
function restGesichert(plugin, result, gen) {
  /* Nie zurück: Hat während der Anfrage eine Übernahme den Bezug auf einen
     neueren Stand gesetzt (bezugIstLokal), gilt deren Generation (Restrisiko
     aus dem Session-Kritiker Sprint 72). */
  _genBezug = Math.max(_genBezug, gen);
  if (_aenderungsGen === gen) State.markClean();
  if (result?.abgelehnt) zeigeAbgelehnt(plugin, result);
  if (result?.gepostet) plugin.holeJetzt?.();
  zeigeVertraulichBewahrt(result);
}

/* ADR-0140 §10 (Karte #778): Der Server hat „vertraulich“ stehen lassen,
   weil das Abschalten über den Ganz-Board-PUT kam (Rückfall oder „Meine
   Version behalten“). Abschalten geht nur über eine Op — still bliebe das
   ein Rätsel. */
function zeigeVertraulichBewahrt(result) {
  if (!IS_SERVER_BUILD) return;
  const b = result?.vertraulichBewahrt;
  if (b?.anzahl > 0) showExportToast(I18n.t('vertraulich.bewahrt', b.anzahl));
}

/* Karte #565: „Meine Version behalten“ im REST-Konflikt ersetzt das Board
   bewusst ganz — PUT mit If-Match, nicht über den Ops-Weg von `save`. */
async function behalteMeine(plugin, state) {
  const retry = await (plugin.speichereGanz || plugin.save).call(plugin, state).catch(() => null);
  if (retry?.ok) {
    if (state !== State.get()) {
      _applyingExternalState = true;
      try { State.set(state); } finally { _applyingExternalState = false; }
    }
    bezugIstLokal();
    State.markClean();
    showSaveConfirm(retry.method);
    zeigeVertraulichBewahrt(retry);
  } else if (retry?.conflict) {
    showConflictBanner(
      () => behalteMeine(plugin, state),
      () => uebernimmServerStand(plugin, retry.conflict.serverState),
    );
  } else if (retry?.abgewiesen) zeigeAbgewiesen(retry.abgewiesen);
  else showExportToast(I18n.t('conflict.retry-failed'));
}

/* F-1622: Der Ganz-Board-PUT ist abgelehnt (403/413/422). Den Grund nennen
   und sagen, dass Wiederholen nichts ändert — vorher hiess es nur
   „Speichern fehlgeschlagen — bitte erneut versuchen“. Der Grund kommt
   englisch vom Server; er steht in Anführungszeichen dahinter. */
function zeigeAbgewiesen(abgewiesen) {
  showConflictBanner(() => {}, null, I18n.t('sync.ganz-abgewiesen', abgewiesen.grund || String(abgewiesen.status)),
    I18n.t('ops.abgelehnt.ok'), null);
}

function uebernimmServerStand(plugin, serverState) {
  plugin.bezugUebernehmen?.(serverState);
  _applyingExternalState = true;
  try { State.set(serverState); } finally { _applyingExternalState = false; }
  bezugIstLokal();
  State.markClean();
}

/* Karte #565 (ADR-0129 §4, Punkt 4): Der Server hat einzelne Ops als echten
   Konflikt abgelehnt (422) — die Karte ist inzwischen gelöscht, die Spalte
   fehlt, oder die Karte trägt am Server mehr. Nichts wird automatisch
   überschrieben; die Meldung nennt Karte und Grund und bietet die bewusste
   Ersetzung an. */
function zeigeAbgelehnt(plugin, result) {
  /* Karte #604 (ADR-0130 §2): Sind ausschliesslich Feldkonflikte abgelehnt,
     gibt es einen eigenen Banner mit beiden Fassungen — „Meine Fassung
     trotzdem speichern“ als Ganz-Board-PUT wäre hier zu grob, er überschriebe
     auch fremde Änderungen an anderen Karten. */
  const feld = result.abgelehnt.filter(a => a.art === 'feld-geaendert');
  const sonst = result.abgelehnt.filter(a => a.art !== 'feld-geaendert' && a.art !== 'rest');
  if (IS_SERVER_BUILD && feld.length && !sonst.length && typeof plugin.ueberschreibeFelder === 'function') {
    zeigeFeldKonflikt(plugin, feld, result.abgelehnt.some(a => a.art === 'rest'));
    return;
  }
  /* ADR-0136 Nachtrag (F-1612): Fremde Kommentare — entfernt, bearbeitet
     oder mit fremder Urheberschaft. Hier gibt es KEIN „Meine Fassung trotzdem
     speichern“: Der Ganz-Board-PUT lehnt dasselbe ab, und eine Fassung, die
     es durchsetzte, entfernte den Kommentar ohne Verlauf und ohne Nachricht
     an den Autor. Den Server-Stand holt restGesichert ohnehin gleich danach
     (der Kommentar kommt lokal zurück); „Verstanden“ holt ihn noch einmal,
     falls jenes GET verloren ging. Ebenso für die beiden Gründe, die der Server bei derselben
     Prüfung meldet (Schattenschlüssel, unlesbarer Stand). */
  /* F-1621: Verlaufsschutz — dieselbe Prüfung am Ergebnis, dieselbe Regel:
     kein Knopf, der es per PUT durchsetzen wollte (Fehlerklasse 124). */
  /* KF-6 (Sprint-82-Abschluss): auch der Antwortkommentar „extern“ (#686) und die
     doppelte Karten-id (KF-1) — der PUT lehnt beides ebenso ab. */
  const kommentar = ['fremder-kommentar', 'schattenschluessel', 'stand-unlesbar', 'verlauf-geaendert', 'verlauf-gefaelscht',
    'extern-kommentar', 'karte-doppelt'];
  const kommentarText = art => art === 'fremder-kommentar' ? 'ops.abgelehnt.fremder-kommentar'
    : art.startsWith('verlauf-') ? 'ops.abgelehnt.verlauf'
    : art === 'extern-kommentar' || art === 'karte-doppelt' ? 'ops.abgelehnt.' + art : 'ops.abgelehnt.sonst';
  if (result.abgelehnt.some(a => kommentar.includes(a.art))) {
    const zeilen = result.abgelehnt.filter(a => kommentar.includes(a.art)).map(a =>
      I18n.t(kommentarText(a.art), a.displayId ?? '?', a.titel || ''));
    const rest = result.abgelehnt.some(a => !kommentar.includes(a.art)) ? ' ' + I18n.t('ops.abgelehnt.rest') : '';
    showConflictBanner(() => plugin.holeJetzt?.(), null, [...new Set(zeilen)].join(' ') + rest,
      I18n.t('ops.abgelehnt.ok'), null);
    return;
  }
  const bekannt = ['ziel-fehlt', 'klemmung', 'entkernung', 'rest'];
  const zeilen = result.abgelehnt.map(a =>
    I18n.t('ops.abgelehnt.' + (bekannt.includes(a.art) ? a.art : 'sonst'), a.displayId ?? '?', a.titel || ''));
  showConflictBanner(
    () => behalteMeine(plugin, result.meineFassung),
    /* K-A3: „Verstanden“ holt den Server-Stand selbst — das GET direkt nach
       dem Speichern ist best effort (Netzfehler, laufendes Speichern). */
    () => plugin.holeJetzt?.(),
    [...new Set(zeilen)].join(' '),
    I18n.t('ops.abgelehnt.meine'),
    I18n.t('ops.abgelehnt.ok'),
  );
}

/* Karte #604: Feldkonflikt — jemand hat Titel, Beschreibung oder Agent-Kontext
   geändert, während man selbst daran schrieb. Beide Fassungen stehen im
   Banner (aufklappbar); kein Zeichen-Merge (ADR-0130 §2). */
function zeigeFeldKonflikt(plugin, feld, mitRest) {
  /* I18n.t liefert bei unbekanntem Schlüssel den Schlüssel selbst zurück. */
  const feldName = f => { const k = 'ops.feld.' + f; const t = I18n.t(k); return t === k ? String(f) : t; };
  const text = [...new Set(feld.map(a => I18n.t('ops.abgelehnt.feld-geaendert', a.displayId ?? '?', a.titel || '',
    feldName(a.feld))))].join(' ') + (mitRest ? ' ' + I18n.t('ops.abgelehnt.rest') : '');
  const zeige = v => { const t = typeof v === 'string' ? v : JSON.stringify(v ?? ''); return t.length > 600 ? t.slice(0, 600) + ' …' : t; };
  const details = document.createElement('details');
  details.className = 'conflict-details';
  /* Inline statt style.css: der Banner gehört zum Server-Build, das slim
     Template soll kein totes CSS tragen. */
  details.style.flexBasis = '100%';
  const summary = document.createElement('summary');
  summary.textContent = I18n.t('ops.feldkonflikt.zeigen');
  details.appendChild(summary);
  for (const a of feld) {
    for (const [label, wert] of [[I18n.t('ops.feldkonflikt.meine-fassung'), a.mein], [I18n.t('ops.feldkonflikt.server-fassung'), a.aktuell]]) {
      const h = document.createElement('div'); h.className = 'conflict-details-label'; h.textContent = label;
      const pre = document.createElement('pre'); pre.className = 'conflict-details-text'; pre.textContent = zeige(wert);
      pre.style.cssText = 'white-space:pre-wrap;max-height:10em;overflow:auto;margin:2px 0 6px;font-size:12px';
      details.appendChild(h); details.appendChild(pre);
    }
  }
  showConflictBanner(
    async () => {
      const r = await plugin.ueberschreibeFelder(feld).catch(() => null);
      if (!r?.ok) showExportToast(I18n.t('conflict.retry-failed'));
      plugin.holeJetzt?.();
    },
    async () => {
      /* „Ihre behalten“: die eigene Fassung geht nicht verloren (ADR-0130 §2).
         Gelingt das Kopieren nicht sicher, bleibt sie in einem eigenen Banner
         sichtbar, statt mit dem Konflikt-Banner zu verschwinden (K-1). */
      const meine = eigeneFassungAlsText(feld);
      if (await kopiereEigeneFassung(meine, navigator.clipboard)) {
        showExportToast(I18n.t('ops.feldkonflikt.kopiert'));
      } else {
        const bleibt = document.createElement('details');
        bleibt.open = true;
        bleibt.style.flexBasis = '100%';
        const pre = document.createElement('pre');
        pre.textContent = meine;
        pre.style.cssText = 'white-space:pre-wrap;max-height:10em;overflow:auto;margin:2px 0 6px;font-size:12px;user-select:all';
        bleibt.appendChild(pre);
        /* Ein Knopf genügt (Verify V-3): beide hätten nur geschlossen. */
        showConflictBanner(() => {}, null, I18n.t('ops.feldkonflikt.nicht-kopiert'),
          I18n.t('ops.feldkonflikt.schliessen'), null, bleibt);
      }
      plugin.holeJetzt?.();
    },
    text,
    I18n.t('ops.feldkonflikt.meine'),
    I18n.t('ops.feldkonflikt.ihre'),
    details,
  );
}

function showConflictBanner(onMine, onServer, text, mineLabel, serverLabel, detailsNode) {
  document.querySelector('.conflict-banner')?.remove();
  const banner = document.createElement('div');
  banner.className = 'recovery-banner conflict-banner';
  const msg = document.createElement('span');
  msg.textContent = text || I18n.t('conflict.message');
  const keepBtn = document.createElement('button');
  keepBtn.className = 'btn btn-primary btn-sm';
  keepBtn.textContent = mineLabel || I18n.t('conflict.keep-mine');
  keepBtn.addEventListener('click', () => { banner.remove(); onMine(); });
  const loadBtn = document.createElement('button');
  loadBtn.className = 'btn btn-sm';
  loadBtn.textContent = serverLabel || I18n.t('conflict.load-server');
  loadBtn.addEventListener('click', () => { banner.remove(); onServer(); });
  banner.appendChild(msg);
  if (detailsNode) banner.appendChild(detailsNode);
  banner.appendChild(keepBtn);
  /* onServer === null: Banner mit nur einem Knopf (Feldkonflikt-Rückfall). */
  if (onServer !== null) banner.appendChild(loadBtn);
  document.body.prepend(banner);
}

/* F-3106 (Karte #1179): `haengt` = Chrome hat die Rechteabfrage für den
   gespeicherten Ordner nicht beantwortet. „Erlauben“ fragte denselben Handle
   erneut und bliebe stumm; geholfen hat in der Diagnose nur das Neu-Wählen. */
function showPermissionBanner(haengt = false) {
  const banner = document.createElement('div');
  banner.className = 'recovery-banner sync-permission-banner';
  const msg = document.createElement('span');
  msg.textContent = I18n.t(haengt ? 'sync.permission-hangs' : 'sync.permission-needed');
  const btn = document.createElement('button');
  btn.className = 'btn btn-primary';
  btn.textContent = I18n.t(haengt ? 'sync.pick-again' : 'sync.grant-permission');
  btn.addEventListener('click', async () => {
    /* Verify B2: bei „neu wählen“ bleibt das Banner stehen, bis der Ordner wirklich verbunden ist —
       ein abgebrochener Dialog oder ein zurückgenommener Wechsel ließ sonst keinen Weg zurück. */
    if (!haengt) banner.remove();
    EventBus.emit(haengt ? 'sync:pick-directory' : 'sync:request-permission');
  });
  if (haengt) {
    const weg = () => {
      if (Sync.isPermissionNeeded()) return;
      banner.remove();
      EventBus.off('config:project-changed', weg);
    };
    EventBus.on('config:project-changed', weg);
  }
  banner.appendChild(msg);
  banner.appendChild(btn);
  document.body.prepend(banner);
}
