/* Inbox-/Base-/Projektions-Layout-Logik — ADR-0067 Stufe 1 (Karte #228)
 *
 * FS-AGNOSTISCH und runtime-neutral: kein node:fs, keine File-System-Access-
 * API, kein DOM. Genau wie src/ops.js läuft dieses Modul IDENTISCH im
 * Browser-Bundle und im headless Node-CLI — die tatsächlichen Lese-/Schreib-
 * Operationen liefert der jeweilige Aufrufer (FSA-Directory-Handle im Browser,
 * node:fs im CLI) und reicht hier nur geparste Daten/Listen herein.
 *
 * Verantwortung dieses Moduls:
 *  - deterministische Op-Datei-Benennung (lexikografisch == chronologisch)
 *  - Inbox-Einträge → {gültige Ops, Poison} aufteilen (Poison NIE still droppen)
 *  - Migrations-Entscheidung (idempotent: base nie überschreiben)
 *  - Heartbeat-Frische (best-effort „genau einer", ADR-0067 §2)
 */

import { validateOp, inspectOpPayload, OP_SCHEMA_VERSION, OP_TYPES,
         tsSchluessel, stabil } from './ops.js';

/* Hüllen-Prüfung, identisch zum Hüllen-Teil von `validateOp`. Dient allein
   der korrekten ETIKETTIERUNG eines Poison-Grundes (Kritiker-Lauf F-451) —
   die Gültigkeitsentscheidung trifft weiterhin ausschliesslich `validateOp`. */
function validHull(op) {
  if (!op || typeof op !== 'object') return false;
  if (typeof op.id !== 'string' || !op.id) return false;
  if (typeof op.ts !== 'string' || !op.ts) return false;
  if (!OP_TYPES.includes(op.type)) return false;
  if (op.payload == null || typeof op.payload !== 'object') return false;
  return true;
}

/** Formatiert einen `unknown`-Eintrag für die Ausgabe: `colId (meintest du columnId?)`. */
export function formatUnknownKey(u) {
  if (typeof u === 'string') return u;                 /* defensiv: Altformat */
  return u.meant ? `${u.key} (meintest du ${u.meant}?)` : u.key;
}

/* Heartbeat gilt als „frisch", wenn jünger als das. Großzügig — der
   Heartbeat ist nur Effizienz (ein Projektor statt zwei), kein
   Korrektheits-Muss; ein abgestürzter Projektor blockiert max. so lange. */
export const HEARTBEAT_TTL_MS = 30_000;

/**
 * Datei-Name für eine Op: `<ts-sanitisiert>-<uuid>.json`.
 *
 * Der ISO-Zeitstempel wird so umkodiert (`:` `.` → `-`), dass die
 * LEXIKOGRAFISCHE Sortierung der Dateinamen exakt der CHRONOLOGISCHEN
 * entspricht (feste Breite des ISO-8601-Formats). Damit kann der Projektor
 * Inbox-Dateien allein per Namens-Sort grob vorordnen; die endgültige
 * Totalordnung macht ohnehin `fold()` (ts dann id).
 *
 * @param {{id:string, ts:string}} op
 * @returns {string}
 */
export function opFileName(op) {
  const tsSafe = String(op.ts).replace(/[:.]/g, '-');
  return `${tsSafe}-${op.id}.json`;
}

/**
 * Nächster `seq`-Wert für eine Op mit Zeitstempel `ts` gegen einen Bestand.
 *
 * Karte #434: `_seq` in `src/ops.js` ist ein MODUL-Zähler und startet je
 * Prozess bei 0. Für den Browser ist das richtig — er erzeugt einen ganzen
 * Diff-Batch in einem Lauf, und `seq` hält dessen Reihenfolge zusammen. Für
 * ein CLI, das pro Aufruf EINE Op schreibt, ist es wertlos: Jede Op trüge 0,
 * und bei gleicher Millisekunde entschiede wieder die Zufalls-`id`. Das
 * Werkzeug, das die Falle beseitigen soll, schriebe sie fest.
 *
 * Bezugsmenge sind deshalb die Ops mit DEMSELBEN `ts` — und nur die. Genau
 * dort wirkt `seq`: `orderedOps` sortiert `ts` → `seq` → `id`; bei
 * abweichendem `ts` ist `seq` bedeutungslos. Ein globales Maximum wäre
 * unnötig gross und machte jeden Schreibvorgang von der ganzen Inbox abhängig.
 *
 * REIN — keine Zeit-, Datei- oder Zufallsquelle. Der Aufrufer reicht den
 * Bestand herein; damit ist die Ableitung ohne Uhrenspiel testbar.
 *
 * @param {Array<{ts?:string, seq?:number}>} ops  bereits vorhandene Ops
 * @param {string} ts                             ISO-Zeitstempel der neuen Op
 * @returns {number}
 */
export function naechsteSeq(ops, ts) {
  if (!Array.isArray(ops)) return 0;
  let max = -1;
  for (const o of ops) {
    if (!o || o.ts !== ts) continue;
    const s = Number.isFinite(o.seq) ? o.seq : 0;
    if (s > max) max = s;
  }
  return max + 1;
}

/* ── seq-Kollisionen zwischen gleichzeitigen Schreibern (F-929) ─────────────
 *
 * `naechsteSeq` allein reicht NICHT, sobald zwei Prozesse dasselbe Board
 * bedienen. Zwischen dem Lesen des Bestands und dem `rename` der eigenen
 * Op-Datei liegt ein Fenster; wer darin liest, sieht die fremde Op noch nicht
 * und leitet denselben `seq` ab. Gemessen (Sprint-64-Kritiker, hier
 * nachgestellt): 12 parallele CLI-Aufrufe, 5 Läufe → 31 von 60 Ops in 14
 * Kollisionsgruppen, ausnahmslos bei `seq: 0`. Dann entscheidet in
 * `orderedOps` wieder die Zufalls-`id` — genau die Falle, gegen die `seq`
 * gebaut ist.
 *
 * WARUM COMPARE-AND-SET UND KEIN LOCK: Der Dateiname einer Op ist
 * `<ts>-<id>.json` und enthält `seq` NICHT. Ein `seq`-Wechsel ändert also den
 * Zielpfad nicht — die Op lässt sich per tmp+rename an derselben Stelle
 * ersetzen, atomar und ohne Zwischenzustand. Damit wird das Nachprüfen zum
 * billigsten Mittel: kein Lock-Zustand, den ein abgestürzter Aufruf
 * hinterliesse, keine Aufräum-Pflicht, kein blockiertes Board.
 *
 * DIE REGEL: WER KOLLIDIERT, WEICHT — IMMER, und auf einen Platz, den kein
 * anderer Weichender derselben Gruppe wählt.
 *
 *   ausweichSeq = (höchstes seq der ANDEREN Ops dieser Millisekunde) + 1
 *               + (Anzahl der Kollisionspartner mit KLEINERER id)
 *
 * Beide Summanden haben je einen Zweck:
 *
 *   Der ERSTE springt über alles, was in dieser Millisekunde schon liegt. Das
 *   ist der Browser-Fall: dessen `_seq` ist ein Modul-Zähler ab 0, und er
 *   prüft NICHT nach. Wer über den ganzen Diff-Batch springt, statt sich per
 *   Zufalls-id hineinzusortieren, zerschneidet ihn nicht — das Zerschneiden
 *   ist der eigentliche Schaden.
 *
 *   Der ZWEITE bricht die Symmetrie zwischen gleichzeitig Weichenden. Ohne
 *   ihn rechnen zwei Aufrufe, die einander sehen, dasselbe `max+1` aus und
 *   landen wieder gemeinsam; mit ihm ist der Rang in der id-Ordnung der
 *   Gruppe der Abstand, und die Ziele sind verschieden. Zwei Schreiber, die
 *   voneinander wissen, kollidieren also GARANTIERT nicht erneut.
 *
 * WARUM NICHT „DIE KLEINSTE id HÄLT DIE STELLUNG" — GEMESSEN WIDERLEGT: Die
 * erste Fassung liess den Kleinsten der Gruppe stehen und nur die anderen
 * weichen. Das terminiert ebenfalls, ist aber FALSCH, und der 12-fach-Test
 * hat es innerhalb von drei Läufen vorgeführt (eine überlebende Gruppe bei
 * `seq: 1`). Der Grund: „der andere weicht schon" ist eine Annahme über einen
 * Prozess, der längst FERTIG sein kann. Wer seine Nachprüfung bestanden und
 * sich beendet hat, bewegt sich nie wieder — wer dann auf seinen Platz
 * beharrt, erzeugt genau die Kollision, die er verhindern sollte. Deshalb
 * darf niemand beharren; die Symmetrie muss über das ZIEL gebrochen werden,
 * nicht über die Frage, wer bleiben darf.
 *
 * TERMINIERUNG: Jedes Ausweichen landet ÜBER allem, was der Weichende gerade
 * sieht — mit einem bereits liegenden, unbeweglichen Op kann es also nie
 * erneut kollidieren, nur mit einem anderen Weichenden. Sehen die beiden
 * einander, trennt sie der Rang; sieht einer den anderen (noch) nicht, ist er
 * gar nicht in der Kollision und bleibt stehen. `maxRunden` ist die
 * Reissleine für den verbleibenden Fall, dass sich der Bestand unter beiden
 * fortlaufend verschiebt — sie bricht nichts ab, sie meldet nur (die Op steht
 * dann bereits gültig auf der Platte, nur eben mit möglicher Kollision).
 *
 * FS-AGNOSTISCH wie der Rest dieses Moduls: Lesen und Schreiben reicht der
 * Aufrufer als Rückrufe herein. Genau deshalb ist die Regel ohne echte
 * Nebenläufigkeit prüfbar — der Test spielt den Bestand vor, statt auf ein
 * Zeitfenster zu hoffen.
 */

/** Reissleine für `stabilisiereSeq` — grosszügig, siehe TERMINIERUNG oben. */
export const SEQ_CAS_MAX_RUNDEN = 32;

/**
 * Ops aus `ops`, die mit `op` in `ts` UND `seq` kollidieren — `op` selbst
 * (nach `id` erkannt) ausgenommen.
 *
 * @param {Array<{id?:string, ts?:string, seq?:number}>} ops
 * @param {{id:string, ts:string, seq?:number}} op
 * @returns {Array<object>}
 */
export function seqKollisionen(ops, op) {
  if (!Array.isArray(ops) || !op) return [];
  const eigen = Number.isFinite(op.seq) ? op.seq : 0;
  return ops.filter(o => o && o.id !== op.id && o.ts === op.ts
                      && (Number.isFinite(o.seq) ? o.seq : 0) === eigen);
}

/**
 * Der Platz, auf den `op` bei einer Kollision ausweicht: über alles, was in
 * dieser Millisekunde schon liegt, plus den Rang in der id-Ordnung der
 * Kollisionsgruppe. Herleitung beider Summanden im Blockkommentar oben.
 *
 * @param {Array<{id?:string, ts?:string, seq?:number}>} ops  Bestand inkl. `op`
 * @param {{id:string, ts:string, seq?:number}} op
 * @returns {number}
 */
export function ausweichSeq(ops, op) {
  const andere = (Array.isArray(ops) ? ops : []).filter(o => o && o.id !== op.id);
  const rang = seqKollisionen(ops, op)
    .filter(o => typeof o.id === 'string' && o.id < op.id).length;
  return naechsteSeq(andere, op.ts) + rang;
}

/**
 * Prüft nach dem Schreiben, ob `op.seq` mit einem gleichzeitigen Schreiber
 * kollidiert, und weicht so lange aus, bis der Wert eindeutig ist.
 *
 * Verändert `op.seq` IN PLACE und ruft `schreibe(op)` für jede Ausweich-Runde
 * — der Aufrufer ersetzt damit die bereits geschriebene Datei (gleicher Pfad,
 * `seq` steht nicht im Dateinamen). `schreibe` darf `false` zurückgeben, wenn
 * die Datei nicht mehr existiert: dann wurde die Op zwischenzeitlich gefaltet
 * und darf NICHT neu geschrieben werden — ein Neuschreiben wäre eine zweite
 * Faltung derselben Op (bei `comment.add` ein doppelter Kommentar).
 *
 * @param {{id:string, ts:string, seq?:number}} op
 * @param {{leseOps:()=>Array<object>, schreibe:(op:object)=>boolean|void,
 *          maxRunden?:number}} io
 * @returns {{stabil:boolean, runden:number, seq:number, verlauf:number[],
 *            kollision:Array<object>, gefaltet:boolean}}
 */
export function stabilisiereSeq(op, io) {
  const { leseOps, schreibe } = io || {};
  const maxRunden = Number.isFinite(io && io.maxRunden) ? io.maxRunden : SEQ_CAS_MAX_RUNDEN;
  const verlauf = [Number.isFinite(op.seq) ? op.seq : 0];
  let runden = 0;
  for (;;) {
    const ops = leseOps() || [];
    const kollision = seqKollisionen(ops, op);
    if (!kollision.length) return { stabil: true, runden, seq: op.seq, verlauf, kollision: [], gefaltet: false };
    if (runden >= maxRunden) {
      return { stabil: false, runden, seq: op.seq, verlauf, kollision, gefaltet: false };
    }
    /* Niemand beharrt — Begründung im Blockkommentar oben, samt der Messung,
       die die erste Fassung („die kleinste id hält die Stellung") widerlegt
       hat. */
    runden++;
    const vorherigesSeq = op.seq;
    op.seq = ausweichSeq(ops, op);
    verlauf.push(op.seq);
    if (schreibe(op) === false) {
      /* KORREKTUR F-963 (Sprint-65-Kritiker, B4): `op.seq` wird ZURÜCKGENOMMEN.
         Diese Funktion mutiert `op` in place — schlägt das Schreiben fehl, weil
         die Datei zwischenzeitlich gefaltet und geprunt wurde, dann steht auf
         der Platte (bzw. in der bereits gefalteten Projektion) noch der alte
         Wert. Ein `op`-Objekt mit dem neuen `seq` wäre eine Behauptung über
         einen Zustand, den es nirgends gibt — und der Aufrufer druckt genau
         dieses Feld in seiner Abschlusszeile. Der `verlauf` behält den
         versuchten Wert, damit die Meldung zeigt, was probiert wurde. */
      op.seq = vorherigesSeq;
      return { stabil: false, runden, seq: op.seq, verlauf, kollision, gefaltet: true };
    }
  }
}

/**
 * Teilt rohe Inbox-Datei-Einträge in gültige Ops und Poison.
 *
 * Poison wird NICHT still verworfen (ADR-0067 §Negativ): der Aufrufer
 * verschiebt `rejected[]` nach `aboard-sync.inbox/.rejected/`. Kaputtes JSON
 * und schema-fremde/zukünftige Ops sind beides Poison.
 *
 * F-451: Ops mit fehlenden PFLICHT-Payload-Feldern sind ebenfalls Poison
 * (sie konnten per Konstruktion nichts bewirken — siehe OP_PAYLOAD_SPEC).
 * Ops mit unbekannten ZUSATZ-Schlüsseln bleiben gültig, werden aber in
 * `warnings[]` gemeldet: das ist der einzige Melder für den halb wirksamen
 * Pfad (`toColumnId` statt `toColId` → Swimlane wechselt, Spalte bleibt).
 *
 * @param {Array<{name:string, text:string}>} entries  Roh-Inhalt der Inbox-Dateien
 * @returns {{ops: Array<object>, rejected: Array<{name:string, reason:string, type:string|null}>, warnings: Array<{name:string, type:string, unknown:string[]}>}}
 */
/* Op-Typ aus dem ROHTEXT lesen (F-975).
 *
 * Gebraucht wird er genau dort, wo er am schwersten zu bekommen ist: Eine Op
 * mit kaputtem JSON laesst sich nicht parsen, und ihr Schreiber sucht spaeter
 * in `.rejected/` nach SEINER Datei. Der Dateiname ist ein Zeitstempel plus
 * UUID und sagt ihm nichts; der Typ sagt es ihm sofort.
 *
 * Bewusst eine Zeichensuche und kein Parser-Ersatz: Sie liefert eine
 * Vermutung fuer die Anzeige, nie eine Entscheidung — abgewiesen ist die Op
 * ohnehin. Bei einer leeren Datei (der haeufigste Fall im Bestand: neun von
 * dreizehn abgewiesenen Dateien in einem Konsumenten-Projekt sind 0 Byte) bleibt es
 * bei `null`, und die Log-Zeile sagt dann ausdruecklich "unbekannt". */
function typAusRohtext(text) {
  /* `[^"\t\r\n]` statt `[^"]` (Kritiker-Lauf 2026-09-17, P2): Der Wert landet
     als Spalte in `.rejected/.abgewiesen.log`, einer tabgetrennten Datei. Ein
     Tab oder Zeilenumbruch im Typ-Feld einer abgewiesenen Op erzeugte dort
     eine zweite, frei erfundene Zeile — eine falsche Spur, die nach
     Beweismaterial aussieht. Gemessen mit
     `{"type":"x\nFAKE\tnix.json\tcard.delete\tok"` als Op-Datei.
     Die Datei ist Fremddatum: Sie kommt von einem Schreiber, den wir gerade
     abgelehnt haben. */
  const m = /"type"\s*:\s*"([^"\t\r\n]{1,64})"/.exec(typeof text === 'string' ? text : '');
  return m ? m[1] : null;
}

export function partitionInbox(entries) {
  const ops = [];
  const rejected = [];
  const warnings = [];
  for (const e of entries || []) {
    let parsed;
    try {
      parsed = JSON.parse(e.text);
    } catch {
      rejected.push({ name: e.name, reason: 'invalid-json', type: typAusRohtext(e.text) });
      continue;
    }
    if (!validateOp(parsed)) {
      let reason;
      if (parsed && typeof parsed === 'object' && parsed.schemaVersion !== OP_SCHEMA_VERSION) {
        reason = `schema-version-${parsed && parsed.schemaVersion}`;
      } else if (!validHull(parsed)) {
        /* Kritiker-Lauf F-451: Scheitert schon die HÜLLE (fehlende id/ts,
           unbekannter Typ), darf der Grund nicht `payload-missing:…` lauten —
           das etikettiert die Ursache falsch und schickt den Schreiber auf die
           falsche Fährte. Hüllenfehler zuerst prüfen, Payload nur wenn die
           Hülle steht. */
        reason = 'invalid-op';
      } else {
        /* F-451: fehlende Pflichtfelder benennen — „invalid-op" allein
           liesse den Schreiber raten, WAS an seiner Op falsch war. */
        const missing = inspectOpPayload(parsed).missing;
        reason = missing.length ? `payload-missing:${missing.join(',')}` : 'invalid-op';
      }
      rejected.push({
        name: e.name,
        reason,
        type: (parsed && typeof parsed === 'object' && typeof parsed.type === 'string')
          ? parsed.type
          : typAusRohtext(e.text),
      });
      continue;
    }
    const { unknown } = inspectOpPayload(parsed);
    if (unknown.length) warnings.push({ name: e.name, type: parsed.type, unknown });
    ops.push(parsed);
  }
  return { ops, rejected, warnings };
}

/* Op-Typen, deren payload.cardId auf eine BESTEHENDE Karte zeigen muss.
   `card.upsert` fehlt bewusst: dort ist `payload.card.id` eine Neuanlage. */
const CARD_TARGET_OPS = new Set([
  'card.move', 'card.delete', 'card.field', 'card.fieldIf', 'card.listAdd', 'card.listRemove',
  'comment.add', 'comment.delete', 'comment.edit',
  'card.claim', 'card.release',
  /* ADR-0141 §5.3 (Karte #827): die bedingten Rückweg-Ops. */
  'card.moveIf', 'card.checkItemIf', 'comment.editIf',
]);

/** Sammelt alle Karten-IDs eines Board-Objekts (defensiv gegen Teilstrukturen). */
function collectCardIds(board, into = new Set()) {
  const sprints = (board && board.board ? board.board.sprints : board && board.sprints) || [];
  for (const s of sprints) {
    for (const c of (s && s.cards) || []) {
      if (c && typeof c.id === 'string') into.add(c.id);
    }
  }
  return into;
}

/**
 * Findet Ops, die auf Karten zeigen, die es nie gab — „verwaiste" Ops (F-327).
 *
 * Hintergrund: Am 2026-05-20 tauchten fünf Op-Dateien mit fremdem Board-Inhalt
 * (eines anderen Boards) in der aBoard-Inbox auf. Der Projektor faltete sie als
 * stilles No-op, weil ihre Karten-IDs hier nicht existieren — die Projektion
 * blieb sauber, aber es gab **keine Spur**. Der Vorfall fiel nur zufällig beim
 * Vor-Push-Diff auf, und die Ops wären beinahe mitcommittet worden.
 *
 * Diese Funktion macht solche Ops sichtbar. Sie ist bewusst **konservativ** —
 * lieber eine verwaiste Op übersehen als eine gültige melden:
 *
 *   - `card.upsert` wird nie gemeldet (legt Karten an, hat kein Vorher-Ziel).
 *   - Eine Op gilt nur dann als verwaist, wenn ihre `cardId` WEDER im
 *     Ausgangs-Snapshot, NOCH in der Ergebnis-Projektion, NOCH in einem
 *     `card.upsert` desselben Batches vorkommt. Damit ist der legitime Fall
 *     „Op referenziert eine Karte, die ein anderer Op im selben Batch erst
 *     anlegt" ausgeschlossen — unabhängig von der Reihenfolge im Batch.
 *   - Board-weite Ops (`board.field`, `swimlane.*`, `maintenance.*`) haben
 *     kein Karten-Ziel und werden nie gemeldet.
 *
 * Eine so erkannte Op hat garantiert nichts bewirkt: Ihr Ziel existierte zu
 * keinem Zeitpunkt des Faltens. Sie kann daher verlustfrei nach `.rejected/`
 * aussortiert werden.
 *
 * ## Zwei Klassen, nicht eine (F-446)
 *
 * Die erste Fassung meldete jede Op auf eine unbekannte Karte als Bleed. Das
 * erzeugte Fehlalarme im Alltagsbetrieb: Wird eine Karte gelöscht und danach
 * kompaktiert, ist sie weder in `base` noch in der Projektion, und die
 * `card.delete`-Op ist geprunt. Jede danach eintreffende Op auf diese Karte
 * (zweiter Tab, Skript mit älterem Board-Stand, doppeltes Löschen) sah damit
 * exakt wie ein Bleed aus — obwohl sie völlig legitim entstanden ist.
 *
 * Für ein Forensik-Feature ist das der teuerste Fehler: Wer die Warnung
 * regelmässig grundlos sieht, klickt sie weg, und der echte Vorfall geht unter.
 * Deshalb liefert die Funktion `kind` mit:
 *
 *   - `'spaetlaeufer'` — die cardId taucht in einem der übergebenen historischen
 *     Karten-ID-Sets auf (rotierte base-Snapshots). Die Karte gab es hier
 *     einmal; die Op kommt schlicht zu spät. Aussortieren ist in Ordnung,
 *     aber ohne Bleed-Wortlaut.
 *   - `'unbekannt'` — die cardId wurde hier nie gesehen. Das ist der
 *     F-327-Fingerabdruck.
 *
 * Ohne `historyCardIds` (z. B. im Browser) ist alles `'unbekannt'` — dann ist
 * die Unterscheidung mangels Historie nicht möglich, und die Meldung muss
 * entsprechend vorsichtig formuliert werden.
 *
 * @param {object} snapshot    Board-Zustand VOR dem Falten (base)
 * @param {object} projection  Board-Zustand NACH dem Falten
 * @param {Array<object>} ops  die gefalteten Ops
 * @param {Set<string>} [historyCardIds]  Karten-IDs aus rotierten base-Snapshots
 * @returns {Array<{op: object, cardId: string, kind: 'spaetlaeufer'|'unbekannt'}>}
 */
export function findOrphanOps(snapshot, projection, ops, historyCardIds = null) {
  const bekannt = collectCardIds(snapshot);
  collectCardIds(projection, bekannt);
  for (const op of ops || []) {
    if (op && op.type === 'card.upsert' && op.payload && op.payload.card &&
        typeof op.payload.card.id === 'string') {
      bekannt.add(op.payload.card.id);
    }
  }

  const orphans = [];
  for (const op of ops || []) {
    if (!op || !CARD_TARGET_OPS.has(op.type)) continue;
    const cardId = op.payload && op.payload.cardId;
    if (typeof cardId === 'string' && cardId && !bekannt.has(cardId)) {
      const kind = (historyCardIds && historyCardIds.has(cardId)) ? 'spaetlaeufer' : 'unbekannt';
      orphans.push({ op, cardId, kind });
    }
  }
  return orphans;
}

/* ── Deutung einer Waise (F-595) ────────────────────────────────────────── */

/** Mögliche Deutungen einer verwaisten Op, von harmlos nach ernst. */
export const WAISEN_DEUTUNG = /*#__PURE__*/ Object.freeze({
  SPAETLAEUFER:       'spaetlaeufer',
  GELOESCHT_VOR_BASE: 'geloescht-vor-base',
  BLEED_VERDACHT:     'bleed-verdacht',
});

/**
 * Ordnet einer verwaisten Op die WAHRSCHEINLICHERE Deutung zu (F-595).
 *
 * `findOrphanOps` beantwortet die Frage „kennen wir diese Karten-ID?" und
 * liefert dafür `kind`. Das genügt nicht, um zu sagen, was passiert IST:
 *
 * Beim Aufräumen eines Konsumenten-Projekts nach F-543 meldete der Lauf vier Ops als
 * F-327-Cross-Board-Bleed-Fingerabdruck. Nachgeprüft existierten die vier
 * IDs in keinem der drei Boards, auch nicht in deren `base.json` — die
 * Karten waren also schon vor dem base-Stand gelöscht, aus dem die Historie
 * stammt, und die `card.delete`-Ops liefen einen Tag später ins Leere.
 * **Kein Bleed, ein Doppel-Delete.** Kosten des Fehlalarms: eine
 * Forensik-Runde von rund einer Viertelstunde.
 *
 * Der Unterschied hängt am Op-Typ, nicht an der ID:
 *
 * - Ein `card.delete` auf eine unbekannte Karte ist genau das, was ein
 *   ZWEITER Löschversuch erzeugt (zweiter Tab, älterer Board-Stand, Skript
 *   mit veralteter Sicht). Die Karte war schon weg, bevor die Historie
 *   beginnt — deshalb greift die `spaetlaeufer`-Erkennung nicht, obwohl der
 *   Vorgang derselbe harmlose ist.
 * - Ein `card.move`, `card.field` oder `comment.add` auf eine nie gesehene
 *   Karte ist etwas anderes: Wer verschiebt, ändert oder kommentiert, hat
 *   die Karte VOR SICH. Das setzt einen Board-Zustand voraus, den es hier
 *   nie gab — der F-327-Fingerabdruck.
 *
 * Die Heuristik bleibt bewusst laut: `GELOESCHT_VOR_BASE` heisst
 * „wahrscheinlicher", nicht „sicher". Ein Bleed KANN Delete-Ops enthalten.
 * Die Meldung muss deshalb beide Deutungen nennen und nur die Rangfolge
 * angeben — genau das war der Befund: sie nannte nur die schlimmere.
 *
 * @param {{op: object, kind: string}} waise Eintrag aus `findOrphanOps`
 * @returns {string} ein Wert aus `WAISEN_DEUTUNG`
 */
export function deuteWaise(waise) {
  const kind = waise && waise.kind;
  if (kind === 'spaetlaeufer') return WAISEN_DEUTUNG.SPAETLAEUFER;
  const typ = waise && waise.op && waise.op.type;
  if (typ === 'card.delete') return WAISEN_DEUTUNG.GELOESCHT_VOR_BASE;
  return WAISEN_DEUTUNG.BLEED_VERDACHT;
}

/**
 * Muss diese Deutung LAUT gemeldet werden (Wächter-Kanal), oder genügt der
 * normale Hinweis-Kanal?
 *
 * Die Frage ist nicht Kosmetik, sondern die Lehre aus dem F-451-Kritiker, die
 * beim ersten F-595-Fix übersehen wurde (Sprint-54-Server-Kritiker, P1):
 * `--quiet` ist genau der Modus, in dem der SessionStart-Hook faltet. Eine
 * Meldung, die dort auf dem stillen Kanal läuft, existiert im automatischen
 * Produktionspfad **nicht** — kein Konsolen-Text, kein Eintrag in
 * `.projektor-warnungen.log`. Der erste Anlauf legte
 * `GELOESCHT_VOR_BASE` auf den stillen Kanal und tauschte damit den lauten
 * Fehlalarm gegen einen stillen blinden Fleck: Ein Cross-Board-Bleed, der
 * ausschliesslich aus `card.delete`-Ops besteht — etwa eine über
 * Board-Grenzen fehlgeleitete Massenaktion — wäre danach vollständig
 * unsichtbar gewesen, also SCHLECHTER als vor dem Fix.
 *
 * Deshalb: laut ist alles, dessen Einordnung eine **Vermutung** ist.
 *
 * - `SPAETLAEUFER` ist leise, weil die Einordnung **belegt** ist: die
 *   Karten-ID steht in einem rotierten base-Snapshot, es gab sie hier
 *   wirklich (F-446).
 * - `GELOESCHT_VOR_BASE` ist laut, weil „wahrscheinlich Doppel-Delete" eine
 *   Vermutung aus dem Op-Typ ist, keine Feststellung. Die Meldung muss beide
 *   Deutungen nennen — leiser werden darf sie nicht.
 * - `BLEED_VERDACHT` ist laut, offensichtlich.
 *
 * @param {string} deutung Wert aus `WAISEN_DEUTUNG`
 * @returns {boolean}
 */
export function istLautZuMelden(deutung) {
  return deutung !== WAISEN_DEUTUNG.SPAETLAEUFER;
}

/**
 * Zählt Waisen nach Deutung — die Zahlen für die Meldung.
 * @param {Array<{op:object, kind:string}>} waisen
 * @returns {{spaetlaeufer:number, geloeschtVorBase:number, bleedVerdacht:number}}
 */
export function zaehleWaisenDeutungen(waisen) {
  const z = { spaetlaeufer: 0, geloeschtVorBase: 0, bleedVerdacht: 0 };
  for (const w of waisen || []) {
    switch (deuteWaise(w)) {
      case WAISEN_DEUTUNG.SPAETLAEUFER:       z.spaetlaeufer++;      break;
      case WAISEN_DEUTUNG.GELOESCHT_VOR_BASE: z.geloeschtVorBase++;  break;
      default:                                z.bleedVerdacht++;
    }
  }
  return z;
}

/* Op-Typen mit einem SWIMLANE-Ziel und dem Feld, das es trägt. */
const SPRINT_TARGET_FIELD = new Map([
  ['card.move',        'toSprintId'],
  ['card.moveIf',      'toSprintId'],   /* Karte #827 */
  ['card.upsert',      'sprintId'],
  ['swimlane.move',    'sprintId'],
  ['swimlane.delete',  'sprintId'],
]);

/**
 * Findet Ops, deren SWIMLANE-Ziel nicht existiert (Kritiker-Lauf F-451).
 *
 * Lücke, die das schliesst: F-451 fängt „Pflichtfeld **fehlt**". Ein Feld, das
 * DA ist, aber **ins Leere zeigt**, blieb weiterhin ein stilles No-op — und
 * bei `card.upsert` sogar schlimmer als das: `applyOp` fällt dort auf
 * `backlogSprint(board)` zurück, die Karte landet also klammheimlich im
 * Backlog statt im gewünschten Sprint. Das ist exakt die „vom Fallback
 * aufgefangen"-Klasse aus §5a des Wissensdokuments — hier ohne das rettende
 * Zufallsglück, das den `colId`-Fall harmlos machte.
 *
 * Bewusst **konservativ**, nach demselben Muster wie `findOrphanOps`: gemeldet
 * wird nur, was weder im Ausgangs-Snapshot, noch in der Ergebnis-Projektion,
 * noch in einem `swimlane.upsert` desselben Batches vorkommt. Damit ist der
 * legitime Fall „Op zielt auf eine Swimlane, die ein anderer Op im selben
 * Batch erst anlegt" reihenfolge-unabhängig ausgeschlossen.
 *
 * `__backlog__` gilt immer als bekannt (strukturell garantiert).
 *
 * @param {object} snapshot    Board-Zustand VOR dem Falten
 * @param {object} projection  Board-Zustand NACH dem Falten
 * @param {Array<object>} ops  die gefalteten Ops
 * @returns {Array<{op:object, sprintId:string, field:string}>}
 */
export function findOrphanSprintTargets(snapshot, projection, ops) {
  const bekannt = new Set(['__backlog__']);
  for (const src of [snapshot, projection]) {
    const sprints = (src && src.board ? src.board.sprints : src && src.sprints) || [];
    for (const s of sprints) if (s && typeof s.id === 'string') bekannt.add(s.id);
  }
  for (const op of ops || []) {
    if (op && op.type === 'swimlane.upsert' && op.payload && op.payload.sprint &&
        typeof op.payload.sprint.id === 'string') {
      bekannt.add(op.payload.sprint.id);
    }
  }

  const orphans = [];
  for (const op of ops || []) {
    const field = op && SPRINT_TARGET_FIELD.get(op.type);
    if (!field) continue;
    const sprintId = op.payload && op.payload[field];
    if (typeof sprintId === 'string' && sprintId && !bekannt.has(sprintId)) {
      orphans.push({ op, sprintId, field });
    }
  }
  return orphans;
}

/**
 * Idempotente Migrations-Entscheidung (ADR-0067 §5).
 *
 * Erste-Lauf-Regel: existiert die Projektion (`aboard-sync.json`) aber noch
 * KEIN Basis-Snapshot → der aktuelle Projektions-Inhalt WIRD der Basis-
 * Snapshot (1:1, kein Datenverlust). Existiert base bereits → nichts tun
 * (Migration lief schon; base ist in Stufe 1 immutabel — Kompaktierung erst
 * #230). Gibt es weder Projektion noch base → frisches Board (Aufrufer legt
 * ein leeres an); kein Migrationsschritt nötig.
 *
 * ADR-0070 §1 (F-291): Die Migration (`createBase`) ist ausschließlich eine
 * explizite, deliberate Opt-in-Aktion — `allowCreate` (Default `false`) MUSS
 * vom Aufrufer aktiv gesetzt werden (Operator: `--migrate`). Ohne Opt-in fasst
 * ein nicht-migriertes Board WEDER `base.json` NOCH `aboard-sync.inbox/` an
 * (kein Teil-Migrations-Marker) → der Projektor-Pfad bleibt inert, das Board
 * läuft auf dem Legacy-ADR-0050-Pfad. Verhindert versehentliche Auto-Migration
 * beim bloßen Browser-Open/Read/Fold.
 *
 * @param {{hasProjection:boolean, hasBase:boolean, allowCreate?:boolean}} fs
 * @returns {{createBase:boolean, ensureInbox:boolean}}
 */
export function planMigration({ hasProjection, hasBase, allowCreate = false }) {
  const migrating = !!allowCreate && !!hasProjection && !hasBase;
  return {
    createBase:  migrating,
    ensureInbox: !!hasBase || migrating,
  };
}

/**
 * Heartbeat-Auswertung: läuft bereits ein ANDERER Projektor frisch?
 *
 * @param {object|null} hb        geparster Heartbeat-Inhalt ({who, ts}) oder null
 * @param {string} selfWho        eigene Projektor-Kennung ('browser' | 'headless:<pid>')
 * @param {number} nowMs          aktuelle Zeit (Date.now()) — vom Aufrufer, fold-Reinheit unberührt
 * @returns {boolean} true → ein anderer, frischer Projektor aktiv → eigenen Lauf überspringen
 */
export function otherProjectorFresh(hb, selfWho, nowMs) {
  if (!hb || typeof hb !== 'object') return false;
  if (!hb.who || hb.who === selfWho) return false;
  const t = Date.parse(hb.ts);
  if (Number.isNaN(t)) return false;
  return (nowMs - t) < HEARTBEAT_TTL_MS;
}

/**
 * Heartbeat-Objekt für den eigenen Projektor-Lauf.
 * @param {string} who    Projektor-Kennung
 * @param {string} nowISO aktueller ISO-Zeitstempel (vom Aufrufer)
 */
export function makeHeartbeat(who, nowISO) {
  return { who, ts: nowISO };
}

/* ── ADR-0071 Kompaktierung / Snapshot-Rotation ─────────────────────────
 *
 * REINE, fs-agnostische Politik-/Namenslogik — identisch im Browser-Bundle
 * und im headless-CLI. Die tatsächlichen fs-Operationen (neue base via
 * tmp+rename, Op-Dateien löschen, alte base rotieren, rotierte Snapshots
 * prunen) macht AUSSCHLIESSLICH der headless-Projektor (ADR-0071 Achse 2D:
 * der Browser kompaktiert NIE — er bleibt reiner optimistic Consumer).
 * Hier liegt nur das deterministische „WANN/WELCHE" — kein „WIE".
 */

/* Schwellen-/Rotations-Politik. Zentral + per Folge-Tuning justierbar
   (ADR-0071: kein 3-K-Entscheid, dokumentierte Mechanik). */
export const COMPACT_OP_THRESHOLD = 150;   /* Banner/Trigger ab so vielen Op-Dateien */
/* F-791 (Karte #450, 2026-08-28): reiner Ringpuffer statt „Anzahl ODER Alter".
   Vorher galt hier dieselbe ODER-Politik wie für `.rejected/`: behalten, wenn
   unter den jüngsten 3 ODER jünger als 14 Tage. Das ODER hält bei täglicher
   Kompaktierung alles der letzten zwei Wochen — gemessen am 2026-08-28 waren
   es 24 Dateien mit zusammen 45 MB, während die Beschreibung „3 Snapshots"
   nahelegt.

   WARUM RINGPUFFER UND NICHT „UND": Ein UND (jüngste 3 UND jünger als 14 Tage)
   klingt wie die naheliegende Verschärfung, ist aber die gefährliche: In einem
   Projekt, das ein paar Wochen ruht, sind irgendwann ALLE Snapshots älter als
   14 Tage — und dann löscht der nächste Lauf das komplette Netz, gerade weil
   lange nichts passiert ist. Die Anzahl allein ist vorhersagbar (immer genau
   so viele Dateien, unabhängig vom Tempo) und lässt nie nichts übrig.

   Fünf statt drei, weil der Platz nicht das Problem war: 5 × ~2 MB = ~10 MB
   gegen die gemessenen 45 MB, und fünf Läufe zurück deckt mehr ab als drei.

   `.rejected/` behält bewusst das ODER (siehe unten) — das ist ein
   FORENSIK-Verzeichnis, dort ist „alt" kein Grund zum Wegwerfen. */
export const ROTATION_KEEP_COUNT  = 5;     /* Ringpuffer: so viele rotierte base-Snapshots, sonst keine */
export const ROTATION_KEEP_DAYS   = null;  /* kein Alters-Schutz mehr — siehe Kommentar oben (F-791) */

/* Schwelle für die AUTOMATISCHE Kompaktierung im headless-Projektor
   (Karte #342). Bewusst deutlich niedriger als COMPACT_OP_THRESHOLD, weil
   die beiden Zahlen zwei verschiedene Fragen beantworten:
     - COMPACT_OP_THRESHOLD (150) = „ab wann soll der MENSCH es sehen?"
       Banner im Browser + Klick auf den Aufräum-Button (ADR-0071 Achse 2D).
     - AUTO_COMPACT_THRESHOLD (50) = „ab wann räumt der Projektor SELBST auf?"
       Er läuft ohnehin bei jedem SessionStart-Hook; dort ist Aufräumen
       kostenlos und braucht niemanden, der hinschaut.
   Vorfall-Beleg (2026-05-22): 87 Op-Dateien sammelten sich an, weil weder
   die 150 erreicht noch je ein Marker gesetzt wurde — genau der Blindfleck
   zwischen beiden Mechanismen. Override zur Laufzeit: `--compact-threshold`
   bzw. `ABOARD_AUTOCOMPACT_THRESHOLD` (siehe tools/aboard-projektor.mjs). */
export const AUTO_COMPACT_THRESHOLD = 50;

/* Aktive (lebende) base — wird rotiert, nie als „rotiert" erkannt. */
export const BASE_FILE_NAME = 'aboard-sync.base.json';

/* Fester ISO-8601-Breitenmuster nach opFileName-Sanitierung
   (`:`/`.` → `-`): YYYY-MM-DDTHH-MM-SS-sssZ. Feste Breite → lexikografische
   Dateinamen-Sortierung == chronologisch (gleiche Eigenschaft wie opFileName). */
const ROTATION_RE = /^aboard-sync\.base\.(\d{4}-\d{2}-\d{2})T(\d{2})-(\d{2})-(\d{2})-(\d{3})Z\.json$/;

/**
 * Dateiname für den abgelösten (rotierten) base-Snapshot.
 * @param {string} nowISO ISO-Zeitstempel (vom Aufrufer — fold-Reinheit unberührt)
 * @returns {string} z. B. `aboard-sync.base.2026-05-18T10-00-00-000Z.json`
 */
export function baseRotationName(nowISO) {
  return `aboard-sync.base.${String(nowISO).replace(/[:.]/g, '-')}.json`;
}

/**
 * Ist `name` ein ABSICHTLICH abgelegter base-Stand (ein „Anker")?
 *
 * Ein Anker ist alles, was wie ein base-Stand heisst, aber weder die aktive
 * base noch ein Rotations-Snapshot ist — also ein Name mit einem BUCHSTABEN
 * nach `base.`: `aboard-sync.base.ANKER-2026-09-16.json`,
 * `aboard-sync.base.CORRUPTED-2026-05-21.json`.
 *
 * WOZU (F-1007): Ein bewusst abgelegter Stand und ein Wegwerf-Snapshot sind am
 * Dateinamen nur durch dieses eine Zeichen zu unterscheiden. Die Rotation
 * (Ringpuffer der jüngsten 5, OHNE Alters-Schutz seit F-791) trifft den Anker
 * nicht — `ROTATION_RE` verlangt eine Ziffer. Er ist damit geschützt, aber
 * auch UNSICHTBAR: Der Aufräum-Lauf nennt Rotationsziel und Löschliste, ein
 * Anker kommt in beiden nicht vor. Wer wissen will, ob sein Anker noch liegt,
 * müsste das Verzeichnis von Hand ansehen.
 *
 * Diese Funktion macht ihn nennbar. Sie entscheidet NICHT über Löschen — ein
 * Anker wird nie gelöscht, das ist der Punkt.
 */
export function isAnkerBaseName(name) {
  return name !== BASE_FILE_NAME
    /* BUCHSTABE nach `base.`, wie oben dokumentiert (Sprint-67-Kritiker):
       Mit `.+` galt auch `aboard-sync.base.2026-09-16T10-00-00Z.json` (ohne
       Millisekunden, also nicht ROTATION_RE) als Anker — dieselbe Datei trifft
       aber das .gitignore-Muster und ist die einzige Kopie. Sie als
       „absichtlich abgelegt, unangetastet" zu nennen, wäre eine falsche
       Sicherheit. */
    && /^aboard-sync\.base\.[A-Za-z].*\.json$/.test(name)
    && !ROTATION_RE.test(name);
}

/** Ist `name` ein rotierter base-Snapshot (NICHT die aktive base)? */
export function isRotatedBaseName(name) {
  return name !== BASE_FILE_NAME && ROTATION_RE.test(name);
}

/* ISO-ts aus einem Rotations-Dateinamen rekonstruieren (deterministisch,
   feste Breite) → ms seit Epoche; NaN wenn das Muster nicht passt. */
function rotationTs(name) {
  const m = ROTATION_RE.exec(name);
  if (!m) return NaN;
  return Date.parse(`${m[1]}T${m[2]}:${m[3]}:${m[4]}.${m[5]}Z`);
}

/**
 * Soll kompaktiert werden? NUR der headless-Projektor ruft das auf.
 * Trigger: ein `maintenance.compact`-Marker liegt in der Inbox (expliziter
 * User-Wunsch via Banner-Button, ADR-0071 Achse 2D) ODER die Op-Anzahl
 * überschreitet die Schwelle (Selbstheilung, falls der Button nie geklickt
 * wird). Reine Entscheidung — kein fs.
 *
 * @param {{opCount:number, hasCompactMarker:boolean, threshold?:number}} a
 * @returns {boolean}
 */
export function shouldCompact({ opCount, hasCompactMarker, threshold = COMPACT_OP_THRESHOLD } = {}) {
  if (hasCompactMarker) return true;
  return Number.isFinite(opCount) && opCount >= threshold;
}

/**
 * Welche rotierten Snapshots dürfen gelöscht werden? Ein Snapshot bleibt,
 * wenn ihn EINE der beiden Regeln schützt: er ist unter den jüngsten
 * `keepCount` ODER jünger als `keepDays`. Snapshots mit unparsbarem Namen
 * werden NIE gelöscht (defensiv — nichts wegwerfen, das man nicht datieren
 * kann). Reine Funktion — Aufrufer liefert die Namensliste + `nowMs`.
 *
 * @param {Array<{name:string}>} rotations  rotierte base-Dateien (z. B. via isRotatedBaseName gefiltert)
 * @param {number} nowMs                     Date.now() (vom Aufrufer)
 * @param {{keepCount?:number, keepDays?:number}} [opts]
 * @returns {string[]} zu löschende Dateinamen
 */
export function selectStaleRotations(rotations, nowMs, opts = {}) {
  return _selectStale(rotations, rotationTs, nowMs,
    opts.keepCount ?? ROTATION_KEEP_COUNT,
    opts.keepDays  ?? ROTATION_KEEP_DAYS);
}

/* Gemeinsamer Kern für beide Aufräum-Politiken (Rotationen + `.rejected/`,
   Karte #367). Ein Eintrag bleibt, wenn ihn EINE der beiden Regeln schützt:
   er ist unter den jüngsten `keepCount` ODER jünger als `keepDays`. Einträge
   mit unparsbarem Namen werden NIE zurückgegeben — nichts wegwerfen, das man
   nicht datieren kann.

   SEIT F-791 GILT DAS ODER NUR NOCH FÜR `.rejected/`: Die Rotationen rufen
   mit `keepDays = null` und sind damit ein reiner Ringpuffer (siehe die
   Konstanten oben). Wer diesen Kommentar liest, um die Aufbewahrungsdauer
   der Board-Snapshots zu verstehen, ist hier an der falschen Stelle. */
function _selectStale(entries, tsOf, nowMs, keepCount, keepDays) {
  /* `keepDays === null` schaltet den Alters-Schutz ab: dann gilt allein die
     Anzahl, der Bestand ist ein Ringpuffer (F-791). Bewusst `null` und nicht
     `0` — `0` hiesse „nichts ist jung genug" und käme aufs Gleiche hinaus,
     liest sich an der Konstanten aber wie ein vergessener Wert. */
  const keepMs = keepDays == null ? null : keepDays * 86_400_000;
  const dated = (entries || [])
    .map(r => ({ name: r.name, t: tsOf(r.name) }))
    .filter(r => Number.isFinite(r.t))
    .sort((a, b) => b.t - a.t);              /* jüngste zuerst */
  const stale = [];
  dated.forEach((r, i) => {
    const protectedByCount = i < keepCount;
    const protectedByAge   = keepMs != null && (nowMs - r.t) < keepMs;
    if (!protectedByCount && !protectedByAge) stale.push(r.name);
  });
  return stale;
}

/* ── `.rejected/`-Aufräumpolitik (F-449, Karte #367) ─────────────────── */

/* F-460 (Sprint-50-Kritiker V6, umgesetzt Sprint 61): vorher 20 / 30 Tage.
   Das war fuer ein FORENSIK-Verzeichnis zu knapp: ein Cross-Board-Bleed mit
   200 Op-Dateien waere nach einem Monat auf 20 Stichproben eingedampft
   gewesen, bevor ihn jemand angesehen hat — und die Sprint-41-Bomben liegen
   nur deshalb noch vollstaendig vor, weil sie jemand von Hand in
   Unterverzeichnisse geschoben hat. F-449 begruendete die Rotation damit,
   dass in ein nur wachsendes Verzeichnis niemand hineinsieht; dieselbe
   Praemisse spricht dagegen, Beweismaterial nach genau einem Monat
   automatisch zu vernichten. 200 Op-Dateien sind wenige hundert Kilobyte —
   die Zahlen kosten nichts und decken einen vollstaendigen Vorfall ab. */
export const REJECTED_KEEP_COUNT = 200;  /* so viele aussortierte Ops immer behalten */
export const REJECTED_KEEP_DAYS  = 180;  /* ODER: alles jünger als so viele Tage behalten */

/* Zeitstempel aus einem Op-Dateinamen (`opFileName`-Konvention:
   `YYYY-MM-DDTHH-MM-SS-sssZ-<uuid>.json`). Feste Breite → deterministisch
   rekonstruierbar, ohne die Datei zu öffnen. NaN, wenn das Muster nicht passt —
   und genau das ist die Schutzfunktion: alles, was nicht wie eine aussortierte
   Op heißt, ist für die Rotation unsichtbar. */
const OP_FILE_RE = /^(\d{4}-\d{2}-\d{2})T(\d{2})-(\d{2})-(\d{2})-(\d{3})Z-.+\.json$/;
export function opFileTs(name) {
  const m = OP_FILE_RE.exec(String(name));
  if (!m) return NaN;
  return Date.parse(`${m[1]}T${m[2]}:${m[3]}:${m[4]}.${m[5]}Z`);
}

/**
 * Welche Einträge in `aboard-sync.inbox/.rejected/` dürfen gelöscht werden?
 *
 * Gleiche Politik wie bei den base-Snapshots (jüngste `keepCount` ODER jünger
 * als `keepDays`), aber mit zwei zusätzlichen Sicherungen, die hier
 * sicherheitsrelevant sind:
 *
 *  1. **Nur Dateien.** Der Aufrufer übergibt ausschliesslich Einträge vom Typ
 *     Datei. In `.rejected/` liegen Unterverzeichnisse (`recovery-2026-05-21*`)
 *     mit dem Beweismaterial des Sprint-41-Datenverlusts — die dürfen von
 *     keiner automatischen Rotation erfasst werden, niemals.
 *  2. **Nur Namen im Op-Dateimuster.** Alles andere liefert `NaN` und ist
 *     damit undatierbar → bleibt liegen. Wer eine Notiz oder einen Dump dort
 *     ablegt, verliert sie nicht.
 *
 * Warum überhaupt aufräumen: `.rejected/` ist das Verzeichnis, in dem die
 * Forensik landet — und wächst als einziges unbegrenzt (F-449). Ein Verzeichnis,
 * das nur wächst, schaut sich niemand an; die Rotation hält es auf eine Grösse,
 * bei der ein Blick hinein noch lohnt.
 *
 * @param {Array<{name:string}>} entries  Datei-Einträge aus `.rejected/` (KEINE Verzeichnisse)
 * @param {number} nowMs                  Date.now() (vom Aufrufer — Reinheit)
 * @param {{keepCount?:number, keepDays?:number}} [opts]
 * @returns {string[]} zu löschende Dateinamen
 */
export function selectStaleRejected(entries, nowMs, opts = {}) {
  return _selectStale(entries, opFileTs, nowMs,
    opts.keepCount ?? REJECTED_KEEP_COUNT,
    opts.keepDays  ?? REJECTED_KEEP_DAYS);
}

/* ── Karte #349 — Projektor-Anomalie-Detection (Self-Heal) ──────────────
 *
 * Reine Diagnose-Funktion: vergleicht die zuvor materialisierte Projektion
 * mit dem aktuellen fold(base+inbox)-Output und benennt erkennbare
 * Anomalien. NIEMALS Mutation: fold(base+inbox) ist und bleibt die einzige
 * Wahrheit (ADR-0067 §2). Aufrufer (Browser-Poll, headless-CLI) reagieren
 * mit Logging/Banner; der authoritative Write geschieht unabhängig davon.
 *
 * Anomalie-Typen:
 *  - `drift-without-ops`: alte Projektion ≠ fold-Output, aber Inbox leer.
 *    Einzige plausible Ursache: Direkt-Patch auf aboard-sync.json (ADR-0067-
 *    Verstoß). Symptom-Klasse einer Konsumenten-Beobachtung (Vermutung
 *    Karte #349). Self-Heal: Write überschreibt mit base authoritativ.
 *  - `stall`: alte Projektion == fold-Output, aber Inbox enthält Ops, und
 *    die Ops bewirken gegenüber der base NICHTS (gelöschte Ziele,
 *    Idempotenz). Wiederholtes Auftreten = Warnzeichen.
 *
 *    F-832 (d): Hier stand bis Sprint 61, das sei „das Symptom ‚neue Ops,
 *    Projektion stagniert' dieser Beobachtung". Das ist die falsche
 *    Zuschreibung. In jener Lage — Projektion alt, Ops WIRKSAM — kann diese
 *    Klausel nach dem F-812-Umbau per Konstruktion nicht anschlagen: sie
 *    verlangt `alteProjektion == fold(base, ops)`, und wirksame Ops
 *    erzeugen genau diese Gleichheit nicht. Auch vorher traf sie nur den
 *    Fall wirkungsloser Ops. Der Umbau ist richtig; was er erkennt, ist
 *    „Ops liegen da und bewirken nichts", nicht „die Projektion hängt
 *    hinterher". Für Letzteres ist `drift-without-ops` oben zuständig —
 *    und für den Fall selbst die Karte im Backlog (F-543).
 *
 * WARUM DER `baseSnapshot`-PARAMETER (Sprint-61-Werkzeug-Lauf): Ohne ihn
 * schlug `stall` bei JEDEM Lauf an, sobald überhaupt Ops in der Inbox lagen.
 * Der Grund ist Bauart, nicht Zufall: gefaltete Ops bleiben bis zur
 * Kompaktierung liegen (nur der headless-Projektor prunt, ADR-0071 Achse 2D).
 * Ein zweiter Lauf über dieselbe Inbox findet also zwangsläufig
 * `alteProjektion == fold(base, ops)` — mit 41 Ops im Bestandsboard hiess
 * das: Meldung bei jedem SessionStart-Hook und bei jedem 15-Sekunden-Poll
 * des Browsers, unmittelbar gefolgt von „Projektion aktualisiert". Ein
 * Wächter, der immer anschlägt, wird ignoriert und bewacht damit nichts
 * (Klasse 20 in knowledge/topics/claude-projekt-framework.md).
 *
 * Der Zusatz-Vergleich trennt die beiden Lagen, die die Gleichheit erzeugen
 * können:
 *   (a) `fold(base, ops) != base` → die Ops HABEN gewirkt, die Projektion
 *       auf Platte war bereits aktuell. Normalbetrieb, keine Anomalie.
 *   (b) `fold(base, ops) == base` → die Ops bewirken nichts. Genau das
 *       Symptom, das die Meldung meint.
 * Ohne `baseSnapshot` (Aufruf mit drei Argumenten) bleibt das alte
 * Verhalten erhalten — kein stiller Bruch für fremde Aufrufer.
 *
 * @param {object|null} oldProjection  vorherige Disk-Projektion oder null
 * @param {object} newProjection       Output von fold(base, ops)
 * @param {number} opCount             Anzahl gültiger Ops in der Inbox
 * @param {object} [baseSnapshot]      Basis-Snapshot, gegen den gefaltet wurde
 * @returns {null | { type: 'drift-without-ops' | 'stall', opCount: number }}
 */
export function detectProjectionAnomaly(oldProjection, newProjection, opCount, baseSnapshot) {
  if (oldProjection == null) return null;
  const equal = _deepEqual(oldProjection, newProjection);
  const safeOpCount = Number.isFinite(opCount) ? opCount : 0;
  if (!equal && safeOpCount === 0) return { type: 'drift-without-ops', opCount: 0 };
  if (equal && safeOpCount > 0) {
    /* Fall (a): die Ops haben gewirkt, die Projektion war nur schon aktuell. */
    if (baseSnapshot != null && !_deepEqual(baseSnapshot, newProjection)) return null;
    return { type: 'stall', opCount: safeOpCount };
  }
  return null;
}

/* Die Kennung der Backlog-Swimlane. Spiegelt `src/ops.js` (dort als Literal
   in `backlogSprint` und im `swimlane.delete`-Zweig). Hier benannt, weil die
   Erklärungslogik unten sich auf genau dieses Sonderverhalten stützt — ein
   drittes rohes '__backlog__' würde die Kopplung unsichtbar machen. */
const BACKLOG_SPRINT_ID = '__backlog__';

/* ── Karte #491 — struktureller Verlust zwischen Projektion und Faltergebnis
 *
 * Beantwortet die Frage, an der `detectProjectionAnomaly` oben vorbeisieht:
 * **gehören base und Projektion überhaupt zusammen?**
 *
 * Die Vorgeschichte in drei Vorfällen (F-967, F-968, F-977):
 *
 *  - 2026-09-04: In einem fremden Projektverzeichnis lag die base DIESES
 *    Projekts neben der dortigen Projektion. Der SessionStart-Trigger sah
 *    zwei Dateien mit den erwarteten Namen, hielt das für Betriebsbereitschaft
 *    und faltete 47 fremde Ops. Ergebnis: ein Mischboard.
 *  - Wiederherstellung nach dem Datenverlust: in einem Projekt lag eine
 *    base vom 20.08. neben einer Projektion vom 31.08. — zwei Wiederherstellungsstände.
 *    Ein Lauf hätte 14 Karten und zwei Sprints gelöscht.
 *  - 2026-09-17: ein zweites Projekt trägt dieselbe `board.id` wie dieses Projekt
 *    (`aboard-project`) bei 11 statt 443 Karten — die Kopie eines Distributables
 *    hat nie eine eigene Kennung bekommen.
 *
 * WARUM NICHT `board.id`, TITEL ODER EIN ANDERES FELD: Der dritte Vorfall hat
 * den naheliegenden Anker widerlegt, bevor er gebaut war. Ein Feldvergleich
 * prüft, ob zwei Dateien **dasselbe behaupten** — und eine Kopie behauptet
 * dasselbe. Was hier trennt, ist der **Bestand**: zwei Boards teilen keine
 * Karten-IDs (UUIDs), ein veralteter Stand desselben Boards teilt fast alle.
 * Damit kommt die Prüfung ohne Namensgleichheit aus und trägt auch bei einem
 * Konsumenten, der Titel UND Kennung übernimmt (Widerlegungsbedingung F-977).
 *
 * DIE RICHTUNG IST ASYMMETRISCH, UND NUR EINE IST GEFÄHRLICH
 * (knowledge/topics/nebenlaeufigkeit-und-lesefenster.md §20): Dass das
 * Faltergebnis MEHR enthält als die Projektion, ist der Normalfall — jede Op
 * und jede additive Schema-Migration erzeugt ihn. Dass es WENIGER enthält, hat
 * genau drei zulässige Gründe: eine `card.delete`-Op, eine `swimlane.delete`-Op,
 * oder ein Migrations-Lauf, in dem die Projektion authoritativ ist. Bleibt
 * danach etwas übrig, ist der Verlust unerklärt — und der Aufrufer darf nicht
 * schreiben.
 *
 * REIN — wie alles in dieser Datei. Die Erkennung ist geteilt (Browser wie
 * CLI), die Vollstreckung nicht: Ein Urteil über das VERHÄLTNIS von
 * Ausgangszustand, Stapel und Ergebnis vollstreckt nur, wer den Zustand
 * exklusiv hält (§5 in knowledge/topics/eingangsvalidierung-und-forensik.md).
 *
 * @param {object|null} oldProjection  vorherige Disk-Projektion oder null
 * @param {object} newProjection       Output von fold(base, ops)
 * @param {Array<object>} ops          gültige Ops des Batches
 * @returns {null | {
 *   type: 'struktureller-verlust',
 *   karten: number, swimlanes: number,
 *   kartenGesamt: number, gemeinsameKarten: number,
 *   nurImFaltergebnis: number,
 *   fremdesBoard: boolean, beispiele: string[]
 * }}
 */
export function detectStructuralLoss(oldProjection, newProjection, ops) {
  if (oldProjection == null) return null;
  const altKarten   = _cardIds(oldProjection);
  const altSprints  = _sprintIds(oldProjection);
  /* Eine leere Projektion kann nichts verlieren — und ein frisch angelegtes
     Board sähe sonst bei der ersten Karte wie ein Fremdboard aus. */
  if (altKarten.size === 0 && altSprints.size === 0) return null;

  const neuKarten  = _cardIds(newProjection);
  const neuSprints = _sprintIds(newProjection);
  const erklaert   = _erklaerteLoeschungen(ops, oldProjection);

  const karten    = [...altKarten].filter(id => !neuKarten.has(id)  && !erklaert.karten.has(id));
  const swimlanes = [...altSprints].filter(id => !neuSprints.has(id) && !erklaert.swimlanes.has(id));
  if (karten.length === 0 && swimlanes.length === 0) return null;

  let gemeinsameKarten = 0;
  for (const id of altKarten) if (neuKarten.has(id)) gemeinsameKarten++;

  /* Die GEGENRICHTUNG (F-1013): Karten, die im Faltergebnis stehen, aber in
     der alten Projektion fehlen. Sie entscheidet, ob --rebase-from-projection
     wirklich verlustfrei ist — nur wenn sie 0 ist, kostet dieser Weg nichts.
     Ist sie > 0, verlieren BEIDE Auswege Bestand, und die Lage braucht eine
     echte Zusammenführung statt eines Flags. Die Funktion bleibt rein: sie
     rechnet nur, die Bewertung/Meldung passiert beim Aufrufer.
     🛑 Fehlalarm-Korrektur (Nacharbeit zu F-1013, gegengeprüft): Das
     Faltergebnis ist `fold(base, ops)` — darin steckt auch, was eine
     `card.upsert`-Op GERADE NEU anlegt. Genau das ist im Rebase-Fall der
     Regelfall, nicht die Ausnahme: Ein Board läuft in den Riegel, WEIL es
     lange nicht gefaltet hat, und dann liegen viele Ops in der Inbox. Nach
     `--rebase-from-projection` bleibt die Inbox absichtlich liegen und wird
     erneut gegen die neue base (= die bisherige Projektion) gefaltet — eine
     `card.upsert`-Op legt die Karte dabei ein zweites Mal an. Sie geht also
     NICHT verloren, obwohl sie nur im Faltergebnis steht, nicht in der alten
     Projektion. Ohne Abzug behauptete die Meldung hier fälschlich einen
     Verlust in der Gegenrichtung — und genau in der Lage, für die sie gebaut
     ist (F-812: eine im Regelfall falsche Warnung wird weggelesen).

     🛑 GRENZE (Gegenprobe des Koordinators, gleicher Tag): Der Abzug trägt
     NUR die Mengen-Aussage — dass die Karte nach dem Rebase wieder EXISTIERT,
     weil `applyOp` (src/ops.js, `case 'card.upsert'`) bei fehlender
     Bestandskarte den `if (existing)`-Zweig verlässt und sie neu einfügt.
     Geprüft ist das am `fold`-Verhalten selbst, nicht nur behauptet. Über den
     INHALT sagt das nichts: Trägt die Op nur eine Teil-Payload, kommt die
     Karte laut F-926 (Karte #484, noch offen) ENTKERNT zurück — nur die
     genannten Felder, der Rest auf Default. `nurImFaltergebnis === 0` heisst
     also „keine Karte verschwindet", nicht „kein Bestand verändert sich". */
  const opAngelegt = _opAngelegteKarten(ops);
  let nurImFaltergebnis = 0;
  for (const id of neuKarten) {
    if (!altKarten.has(id) && !opAngelegt.has(id)) nurImFaltergebnis++;
  }

  return {
    type: 'struktureller-verlust',
    karten: karten.length,
    swimlanes: swimlanes.length,
    kartenGesamt: altKarten.size,
    gemeinsameKarten,
    nurImFaltergebnis,
    /* Keine einzige gemeinsame Karte bei nicht-leerem Bestand auf beiden
       Seiten: zwei verschiedene Boards, nicht zwei Stände desselben. Die
       Unterscheidung ändert nichts an der Konsequenz, aber alles an der
       Meldung — „falsches Verzeichnis" und „veralteter Stand" haben
       verschiedene Abhilfen. */
    fremdesBoard: gemeinsameKarten === 0 && neuKarten.size > 0,
    /* Dritte Lage, die von den beiden oberen zu trennen ist: Das
       Faltergebnis enthält ÜBERHAUPT keine Karte — abgeschnittene aber
       parsbare base, `{"board":{"sprints":[]}}`, verlorene `sprints`. Ohne
       eigenes Merkmal fiele sie unter „veralteter Stand" und schickte den
       Leser ins Datieren von Dateien, während die Abhilfe „base prüfen"
       lautet. Die Konsequenz ist in allen drei Fällen dieselbe (Abbruch);
       verschieden ist, wonach der Bediener sucht. */
    leeresFaltergebnis: neuKarten.size === 0,
    beispiele: karten.slice(0, 5),
  };
}

function _boardOf(p) { return (p && p.board) ? p.board : p; }

function _cardIds(projection) {
  const ids = new Set();
  const b = _boardOf(projection);
  for (const s of (b && b.sprints) || []) {
    for (const c of (s && s.cards) || []) {
      if (c && typeof c.id === 'string') ids.add(c.id);
    }
  }
  return ids;
}

function _sprintIds(projection) {
  const ids = new Set();
  const b = _boardOf(projection);
  for (const s of (b && b.sprints) || []) {
    if (s && typeof s.id === 'string') ids.add(s.id);
  }
  return ids;
}

/* Welche Verschwindungen erklärt der Op-Stapel?
 *
 * 🛑 Entscheidend ist nicht, welche Op DA IST, sondern was `applyOp` mit ihr
 * TUT. Die erste Fassung dieser Funktion trug für jede `swimlane.delete`-Op
 * pauschal alle Karten ein, die die Projektion in dieser Swimlane führte —
 * und übersah damit, dass `applyOp` (src/ops.js, case 'swimlane.delete')
 * zwei Dinge anders macht:
 *
 *   1. Auf `__backlog__` verwirft es die Op vollständig („Backlog ist
 *      unlöschbar"). Sie entfernt KEINE Karte — entschuldigte aber jede.
 *   2. Bei jeder anderen Swimlane rettet es die Nicht-Meta-Karten in den
 *      Backlog und löscht nur die Meta-Karten mit.
 *
 * Gemessen am echten Board (Kritiker-Lauf Sprint 65): eine einzige
 * Op-Datei `{type:'swimlane.delete', payload:{sprintId:'__backlog__'}}`
 * genügte, damit eine base mit fünf fehlenden Backlog-Karten kommentarlos
 * durchlief — Exit 0, 443 → 438 Karten. Und das ist kein konstruierter
 * Fall: `src/diff.js` emittiert `swimlane.delete` für jede im neuen Zustand
 * fehlende Swimlane OHNE `__backlog__`-Ausnahme, also erzeugt ausgerechnet
 * der fremde Browser-Zustand diese Op selbst.
 *
 * Die Lehre ist allgemeiner als der Fix: Wer aus einem Op-Stapel ableitet,
 * was „erlaubt" ist, baut die Semantik des Ausführers ein zweites Mal nach —
 * und zwei Nachbauten derselben Regel driften. Deshalb steht hier bewusst
 * kein zweiter `applyOp`: gelesen wird nur, was die Op-Typen an der Stelle
 * tun, an der sie Bestand ENTFERNEN, und diese Stelle ist in `src/ops.js`
 * mit dem Kommentar „Parität zu main.js sprint:delete" markiert. Ändert sie
 * sich, muss diese Funktion mitgezogen werden. */
function _erklaerteLoeschungen(ops, oldProjection) {
  const karten = new Set();
  const swimlanes = new Set();
  const list = Array.isArray(ops) ? ops : [];
  if (!list.length) return { karten, swimlanes };

  let proSprint = null;   /* erst bei Bedarf aufbauen */
  /* Ohne Backlog rettet `applyOp` nichts — dann entfernt die Op tatsächlich
     alle Karten der Swimlane. Ein Board ohne `__backlog__` ist kein
     regulärer Zustand, aber die Annahme kostet hier nichts und verhindert
     einen Fehlalarm in genau dem Fall, in dem die Rettung ausfällt. */
  const hatBacklog = _sprintIds(oldProjection).has(BACKLOG_SPRINT_ID);

  for (const op of list) {
    const p = (op && op.payload) || {};
    if (op && op.type === 'card.delete') {
      if (typeof p.cardId === 'string') karten.add(p.cardId);
      continue;
    }
    if (!op || op.type !== 'swimlane.delete') continue;
    if (typeof p.sprintId !== 'string') continue;
    /* (1) `applyOp` verwirft die Op — sie erklärt nichts, auch nicht die
       Swimlane selbst. */
    if (p.sprintId === BACKLOG_SPRINT_ID) continue;
    swimlanes.add(p.sprintId);
    if (proSprint == null) proSprint = _kartenJeSprint(oldProjection);
    const eintrag = proSprint.get(p.sprintId);
    if (!eintrag) continue;
    /* (2) Nur was `applyOp` wirklich mitlöscht. */
    for (const id of (hatBacklog ? eintrag.meta : eintrag.alle)) karten.add(id);
  }
  return { karten, swimlanes };
}

/* Welche Karten-IDs legt der Op-Stapel per `card.upsert` an?
 *
 * Spiegelbild zu `_erklaerteLoeschungen` oben, nur für die andere Richtung:
 * Dort werden Verschwindungen erklärt, hier Neuanlagen, die im Faltergebnis
 * auftauchen, ohne dass die alte Projektion sie kannte. `applyOp` (src/ops.js,
 * case 'card.upsert') legt eine neue Karte an, wenn `payload.card.id` noch
 * keiner bestehenden Karte gehört — ist sie es doch, ändert die Op nur Inhalt,
 * keine Identität, und die ID steht dann ohnehin schon in `altKarten`. Ein
 * Abgleich mit dem Vorzustand ist deshalb hier NICHT nötig: Eine ID, die
 * bereits in der alten Projektion stand, wird von der Gegenrichtungs-Zählung
 * in `detectStructuralLoss` unabhängig davon schon ausgeschlossen (sie ist ja
 * kein `neuKarten \ altKarten`-Element mehr) — diese Funktion muss also nur
 * ALLE per `card.upsert` referenzierten IDs sammeln, nicht nur die neuen.
 *
 * Andere Op-Typen legen keine Karten an: `card.move`/`card.field`/
 * `card.delete` setzen eine bestehende Karte voraus (siehe `findCard` in
 * `src/ops.js`) und wirken bei fehlendem Ziel als No-op. */
function _opAngelegteKarten(ops) {
  const ids = new Set();
  for (const op of (Array.isArray(ops) ? ops : [])) {
    if (!op || op.type !== 'card.upsert') continue;
    const card = op.payload && op.payload.card;
    if (card && typeof card.id === 'string') ids.add(card.id);
  }
  return ids;
}

/* Karten je Swimlane, getrennt nach „alle" und „nur Meta" — die Trennung
   entspricht dem Rettungsverhalten von `applyOp`. */
function _kartenJeSprint(projection) {
  const m = new Map();
  const b = _boardOf(projection);
  for (const s of (b && b.sprints) || []) {
    if (!s || typeof s.id !== 'string') continue;
    const alle = [];
    const meta = [];
    for (const c of (s.cards) || []) {
      if (!c || typeof c.id !== 'string') continue;
      alle.push(c.id);
      if (c.kind === 'sprint-meta') meta.push(c.id);
    }
    m.set(s.id, { alle, meta });
  }
  return m;
}

/* Struktureller Gleichheits-Vergleich (F-406): rein, JSON-Werte (kein Date,
   kein Map/Set/undefined im Board-Modell). Reihenfolge-insensitiv für
   Objekt-Schlüssel — verhindert False-Positive `drift-without-ops`, wenn
   Schema-Migrationen in storage.js Felder an unterschiedlichen Positionen
   einsortieren (z. B. migrateSprintMeta, migrateLaneKind, checkVersion). */
function _deepEqual(a, b) {
  if (a === b) return true;
  if (a == null || b == null) return a === b;
  if (typeof a !== typeof b) return false;
  if (typeof a !== 'object') return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  if (Array.isArray(a)) {
    if (a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (!_deepEqual(a[i], b[i])) return false;
    return true;
  }
  const ka = Object.keys(a);
  const kb = Object.keys(b);
  if (ka.length !== kb.length) return false;
  for (const k of ka) {
    if (!Object.prototype.hasOwnProperty.call(b, k)) return false;
    if (!_deepEqual(a[k], b[k])) return false;
  }
  return true;
}

/* ── F-700 — Zeitstempel-Plausibilität (Meldung aus einem Konsumenten-Projekt) ───────
 *
 * `orderedOps` (src/ops.js) sortiert nach `ts` — AUFSTEIGEND —, und `applyOp`
 * vergibt die `displayId` in dieser Reihenfolge, bei JEDEM Lauf neu. In einem
 * Konsumenten-Projekt sind so drei Karten um eine Nummer gerutscht, obwohl
 * ihre Nummern in einer Übergabe-Notiz und in Kundendokumenten standen. Die
 * dortige Abhilfe war Vordatieren, was beim nächsten Schreiber dieselbe Lage
 * erzeugt; acht Ops trugen am Ende erfundene Zeitstempel.
 *
 * F-826 (Sprint-61-Kritiker) — die Richtung stand hier bis Sprint 61 falsch:
 * eine vordatierte Op sortiere sich „vor jede später korrekt geschriebene
 * Op". Sie sortiert DAHINTER. Nachgemessen mit `fold`:
 *
 *   A (vordatiert) allein        → 11:A
 *   A, dann B (korrekt) dazu     → 11:B, 12:A     ← A verliert seine 11
 *   E (bestehend) allein         → 11:E
 *   E, dann M (rückdatiert) dazu → 11:M, 12:E     ← E verliert seine 11
 *
 * Beide Richtungen verschieben Nummern — aber nicht dieselben. Vordatiert
 * verliert die auffällige Op ihre eigene Nummer; rückdatiert verliert eine
 * unbeteiligte, längst bestehende Karte ihre. Der zweite Fall ist der
 * schwerere, und `findFutureOps` sieht ihn per Konstruktion nie. Was dagegen
 * hilft und was nicht, steht bei `findImplausibleTs` unten.
 *
 * Der schwerere Teil ist nicht die Nummer: `orderedOps` bestimmt auch die
 * Positions-Auflösung beim Einfügen (Anker aus ADR-0079). Erfundene
 * Zeitstempel verschieben also die Reihenfolge von Karten INNERHALB einer
 * Spalte — und dort gibt es keine Nummer, deren Änderung jemandem auffiele.
 *
 * WARNUNG, KEIN POISON — bewusst: Eine Op abzulehnen, weil eine Uhr falsch
 * ging, wäre schlimmer als das Problem (der Schreiber hat sie in gutem
 * Glauben erzeugt, und `fold` bleibt deterministisch). Die Toleranz deckt
 * gewöhnliche Uhren-Schieflage zwischen zwei Maschinen ab; alles darüber ist
 * kein Gangunterschied mehr, sondern eine Eingabe.
 */
export const FUTURE_TS_TOLERANCE_MS = 5 * 60 * 1000;

/**
 * Findet Ops, deren `ts` mehr als `toleranceMs` in der Zukunft liegt.
 *
 * @param {Array<object>} ops     gültige Ops (Poison vorher aussortiert)
 * @param {number} nowMs          Bezugszeit (Date.now() beim Aufrufer —
 *                                injizierbar, damit der Test nicht wartet)
 * @param {number} [toleranceMs]  erlaubte Uhren-Schieflage
 * @returns {Array<{op: object, aheadMs: number}>} absteigend nach Vorsprung
 */
export function findFutureOps(ops, nowMs, toleranceMs = FUTURE_TS_TOLERANCE_MS) {
  if (!Array.isArray(ops) || !Number.isFinite(nowMs)) return [];
  const treffer = [];
  for (const op of ops) {
    const ts = op && typeof op.ts === 'string' ? Date.parse(op.ts) : NaN;
    if (!Number.isFinite(ts)) continue;      /* unparsbar → nicht unsere Zuständigkeit */
    const aheadMs = ts - nowMs;
    if (aheadMs > toleranceMs) treffer.push({ op, aheadMs });
  }
  treffer.sort((a, b) => b.aheadMs - a.aheadMs);
  return treffer;
}

/* ── F-1041 — die Zukunfts-Op, die auf eine zweite Op derselben Karte trifft ──
 *
 * `findFutureOps` begründet seine Toleranz mit Uhren-Schieflage und seine
 * Warnung mit der Reihenfolge INNERHALB einer Spalte. Beides trägt nicht für
 * den Fall, der am 2026-09-19 aus einem Konsumenten-Projekt gemeldet wurde: Liegen ZWEI
 * Ops auf DERSELBEN Karte in der Inbox, entscheidet der `ts` (LWW über
 * `orderedOps`), welcher INHALT überlebt. Eine um drei Minuten vordatierte,
 * inhaltlich überholte Fassung gewinnt dann gegen die spätere, korrekt
 * datierte — und lag unter der 5-Minuten-Schwelle, also ohne ein Wort.
 *
 * DIE BAUFORM IST ENTSCHIEDEN (F-1041, präzisiert vom Melder): Der Schaden
 * hängt an der KOLLISION, nicht am Vorsprung. Deshalb hier GAR KEINE Toleranz
 * — jede Op mit `ts` nach `nowMs`, deren Karte von mindestens einer weiteren
 * Op (andere `id`) in derselben Op-Menge getroffen wird. Eine einzelne
 * vordatierte Op bleibt Sache von `findFutureOps` und seiner Toleranz,
 * unverändert auch im Server-Modus mit mehreren Uhren.
 *
 * Was „trifft die Karte" heisst: `payload.cardId` (card.move/.delete/.field,
 * comment.add/.delete) oder `payload.card.id` (card.upsert). Jede Paarung
 * zählt, auch Kommentar gegen Feld — `card.upsert` ersetzt die ganze Karte,
 * und welche Seite zuletzt wirkt, entscheidet der `ts`.
 *
 * GRENZE, ausdrücklich: Erkennbar ist die Lage nur, SOLANGE der Stempel in der
 * Zukunft liegt. Danach ist die vordatierte Op von einer rechtzeitig
 * geschriebenen nicht mehr zu unterscheiden — es gibt für die echte
 * Schreibzeit keinen unabhängigen Zeugen (Begründung bei `findImplausibleTs`:
 * Dateiname = `op.ts`, mtime überlebt keinen Checkout). Der Fold selbst
 * bleibt aber bei jedem Lauf gleich: Was die Meldung einmal beschrieben hat,
 * steht danach dauerhaft so in der Projektion.
 *
 * MELDEN, NICHT VERWERFEN — dieselbe Linie wie F-700: Welche der beiden Ops
 * die gewollte ist, weiss der Leser nicht. REIN, wie alles in dieser Datei.
 *
 * ── F-1101 (Kritiker A, V1) — NICHT jede Paarung auf derselben Karte zählt ──
 * Die erste Fassung meldete JEDE zweite Op auf derselben Karte, auch wenn die
 * Reihenfolge der beiden am Ergebnis nichts ändert — `comment.add` gegen
 * `card.field`, oder `card.field` auf zwei VERSCHIEDENEN Feldern. Das ist
 * Rauschen: Es lädt zum Weglesen ein (F-812), und der Meldetext („gewinnt die
 * vordatierte, auch wenn sie überholt ist") ist für diese Fälle schlicht
 * falsch — niemand verliert etwas, wenn ein Kommentar und ein Feld in
 * beliebiger Reihenfolge angewandt werden.
 *
 * Gemeldet wird nur noch, wenn zwei Ops denselben Teil der Karte SCHREIBEN —
 * `opSchreibSchluessel` bildet das ab:
 *   - `card.upsert`/`card.delete` ersetzen bzw. entfernen die GANZE Karte →
 *     Schlüssel `'*'`, kollidiert mit JEDER anderen Op derselben Karte.
 *   - `card.field` schreibt genau `payload.field` → Schlüssel `feld:<name>`.
 *     Das gilt unterschiedslos für Skalar- und Array-Felder (`links`,
 *     `assignees`, …): `OP_PAYLOAD_SPEC` kennt bei `card.field` nur `value`,
 *     die Payload ersetzt also immer den GANZEN Feldwert, gleich welchen
 *     Typs. Eine Sonderbehandlung für Array-Felder wäre eine zweite Regel für
 *     denselben Mechanismus.
 *   - **Ausnahme `field:'columnId'` (F-1110a):** `applyOp` behandelt das genau
 *     wie `card.move` — beide klemmen den Wert über `klemmeSpalte` und setzen
 *     `card.columnId`; `card.move` schreibt daneben nur noch die
 *     Sprint-Zugehörigkeit und die Einfügeposition, kein weiteres Feld. Ein
 *     `card.field columnId` ist also derselbe Schreibzugriff wie ein
 *     `card.move` auf dieselbe Karte, nur ohne Sprintwechsel — Schlüssel
 *     `'position'` statt `feld:columnId`, sonst würde die Kollision mit einem
 *     gleichzeitigen `card.move` unentdeckt bleiben.
 *   - **Ausnahme `field:'comments'` (F-1110b):** `applyOp` ersetzt bei
 *     `card.field` immer `loc.card[p.field]` komplett — bei `field:'comments'`
 *     also das GANZE Kommentar-Array, nicht nur einen Eintrag. Das trifft
 *     denselben Teil der Karte wie JEDES `comment.add`/`comment.delete`
 *     derselben Karte (die einen einzelnen Eintrag anhängen bzw. entfernen):
 *     Wendet der Fold den Feld-Ersatz zuletzt an, ist der Kommentar weg oder
 *     wieder da, unabhängig davon, was die Kommentar-Op wollte. Kollidiert
 *     deshalb mit JEDER Kommentar-Op derselben Karte, nicht nur mit einer
 *     zweiten `field:'comments'`-Op.
 *   - `card.move` schreibt die Platzierung (Spalte/Sprint) → Schlüssel
 *     `'position'`, unabhängig vom konkreten Ziel.
 *   - `comment.add`/`comment.delete` schreiben GENAU den Kommentar ihrer
 *     `commentId` → Schlüssel `kommentar:<id>`. Zwei Kommentar-Ops mit
 *     VERSCHIEDENER `commentId` treffen unterschiedliche Einträge und
 *     kollidieren nicht; dieselbe `commentId` ist derselbe Wettlauf wie ein
 *     Feld (hinzufügen vs. löschen DESSELBEN Kommentars) — MIT EINER
 *     AUSNAHME (F-1110c): zwei `comment.add` derselben `commentId` sind KEINE
 *     Kollision. `applyOp` hängt einen Kommentar nur an, wenn seine `id` noch
 *     nicht existiert (Zeile „Idempotent: gleiche comment.id nicht doppelt
 *     anhängen") — die Op ist idempotent, und `orderedOps` sortiert nach
 *     `ts`: die FRÜHERE gewinnt, nicht die spätere. Eine vordatierte
 *     `comment.add`-Wiederholung sortiert sich also ans ENDE der Gruppe und
 *     wird beim Falten schlicht ignoriert (die Karte hat den Kommentar
 *     bereits) — anders als bei jedem anderen Schlüssel, wo die spätere die
 *     frühere überschreibt. `comment.delete` gegen `comment.add` derselben
 *     `commentId` bleibt eine echte Kollision: dort entscheidet die
 *     Reihenfolge, ob der Kommentar am Ende existiert oder nicht.
 * Zwei Ops kollidieren, wenn einer der beiden Schlüssel `'*'` ist, wenn genau
 * einer `feld:comments` ist und der andere ein `kommentar:<id>`-Schlüssel,
 * oder wenn beide Schlüssel exakt gleich sind — AUSSER es sind zwei
 * `comment.add` derselben `commentId` (F-1110c, siehe oben). Ein nicht
 * bestimmbarer Schlüssel (fremder Op-Typ, fehlendes Payload-Feld) kollidiert
 * mit nichts — solche Ops erreichen `findFutureKollisionen` ohnehin nur über
 * kaputte/fremde Daten, die `validateOp` eigentlich schon als Poison
 * behandelt.
 *
 * @param {Array<object>} ops  gültige Ops (Poison vorher aussortiert)
 * @param {number} nowMs       Bezugszeit (injizierbar für Tests)
 * @returns {Array<{op: object, aheadMs: number, cardId: string, andere: object[]}>}
 *          absteigend nach Vorsprung; `andere` = die Ops, mit denen diese Op
 *          tatsächlich kollidiert (nicht: alle übrigen Ops derselben Karte)
 */
export function findFutureKollisionen(ops, nowMs) {
  if (!Array.isArray(ops) || !Number.isFinite(nowMs)) return [];
  const jeKarte = new Map();
  for (const op of ops) {
    const cardId = zielKarteVonOp(op);
    if (cardId == null) continue;
    let gruppe = jeKarte.get(cardId);
    if (!gruppe) { gruppe = new Map(); jeKarte.set(cardId, gruppe); }
    /* Nach `id` entdoppelt: Dieselbe Op zweimal (Kopie in der Inbox) ist keine
       Kollision — `fold` wendet sie ohnehin nur einmal an. */
    if (!gruppe.has(op.id)) gruppe.set(op.id, op);
  }
  const treffer = [];
  for (const [cardId, gruppe] of jeKarte) {
    if (gruppe.size < 2) continue;
    for (const op of gruppe.values()) {
      const k = tsSchluessel(op.ts);
      if (!k) continue;                 /* nicht deutbar → findImplausibleTs */
      const aheadMs = k.ms - nowMs;
      if (!(aheadMs > 0)) continue;
      const andere = [...gruppe.values()].filter(o => o !== op && opsKollidieren(op, o));
      if (andere.length === 0) continue;  /* F-1101: keine Paarung, bei der die Reihenfolge zählt */
      treffer.push({ op, aheadMs, cardId, andere });
    }
  }
  treffer.sort((a, b) => b.aheadMs - a.aheadMs);
  return treffer;
}

/* F-1101/F-1110: welchen Teil der Karte schreibt diese Op? `null` =
   unbestimmbar (kollidiert mit nichts). Siehe Begründung im Kopfkommentar von
   `findFutureKollisionen`. */
function opSchreibSchluessel(op) {
  const type = op && op.type;
  const p = (op && op.payload) || {};
  if (type === 'card.upsert' || type === 'card.delete') return '*';
  if (type === 'card.field' || type === 'card.fieldIf') {
    if (typeof p.field !== 'string') return null;
    /* F-1110a: `card.field columnId` klemmt/schreibt `card.columnId` genau wie
       `card.move` — derselbe Schreibzugriff, eigener Schlüssel wäre eine
       zweite Regel für denselben Mechanismus. */
    if (p.field === 'columnId') return 'position';
    return `feld:${p.field}`;
  }
  if (type === 'card.move' || type === 'card.moveIf') return 'position';
  /* Karte #827: ein Häkchen ist Teil der Beschreibung — es kollidiert mit
     jeder anderen Änderung an ihr (eigener Schlüssel wäre eine zweite Regel). */
  if (type === 'card.checkItemIf') return 'feld:description';
  /* ADR-0130 §1: Übernehmen und Freigeben schreiben beide `card.claim` — ob
     eine Übernahme gelingt, hängt an der Reihenfolge. */
  if (type === 'card.claim' || type === 'card.release') return 'feld:claim';
  /* ADR-0130 §3: eine Mengen-Op schreibt genau EIN Element ihrer Liste. Zwei
     Mengen-Ops mit verschiedenen Elementen kollidieren nicht; card.field auf
     dieselbe Liste kollidiert mit jeder (Regel in opsKollidieren). */
  if (type === 'card.listAdd' || type === 'card.listRemove') {
    return typeof p.field === 'string' && p.item !== undefined ? `menge:${p.field}:${stabil(p.item)}` : null;
  }
  if (type === 'comment.add') {
    return p.comment && typeof p.comment.id === 'string' ? `kommentar:${p.comment.id}` : null;
  }
  /* comment.edit (ADR-0130 §4) schreibt genau den Kommentar seiner commentId —
     derselbe Schlüssel wie add/delete: edit gegen delete oder zwei edits
     desselben Kommentars sind echte Wettläufe (die Reihenfolge entscheidet). */
  if (type === 'comment.delete' || type === 'comment.edit' || type === 'comment.editIf') return typeof p.commentId === 'string' ? `kommentar:${p.commentId}` : null;
  return null;
}

/* Kollidieren zwei Ops derselben Karte — entscheidet ihre Reihenfolge (der
   `ts`) über den INHALT? Siehe `opSchreibSchluessel` für die Herleitung. */
function opsKollidieren(a, b) {
  const ka = opSchreibSchluessel(a);
  const kb = opSchreibSchluessel(b);
  if (ka == null || kb == null) return false;
  /* F-1110c: zwei `comment.add` derselben commentId sind idempotent — die
     FRÜHERE gewinnt (siehe `applyOp`), eine vordatierte Wiederholung sortiert
     sich ans Ende und wird beim Falten ignoriert. Keine Kollision, obwohl die
     Schlüssel gleich sind.
     Grenze (Verify-Lauf 3, F-1120): Das gilt für eine INHALTSGLEICHE
     Wiederholung. Tragen beide Ops verschiedenen Text, entscheidet der `ts`,
     welcher überlebt, und es wird nicht gemeldet. `src/diff.js` erzeugt
     `comment.add` nur für neu angehängte Kommentare, der Fall braucht also
     handgeschriebene Ops mit fester Id; bewusst hingenommen.
  */
  if (a && b && a.type === 'comment.add' && b.type === 'comment.add' && ka === kb) return false;
  if (ka === '*' || kb === '*') return true;
  /* F-1110b: `card.field field:'comments'` ersetzt das GANZE Kommentar-Array
     — das kollidiert mit jeder Kommentar-Op derselben Karte, nicht nur mit
     einer zweiten `field:'comments'`-Op (die `ka === kb`-Prüfung unten deckt
     nur Letzteres ab). */
  if ((ka === 'feld:comments' && kb.startsWith('kommentar:')) ||
      (kb === 'feld:comments' && ka.startsWith('kommentar:'))) return true;
  /* F-1580 (ADR-0135): `comment.delete` verkürzt zusätzlich `card.attachments`
     der Karte (Einträge mit dieser commentId) und leert `coverAttachmentId`,
     wenn das Titelbild an einem dieser Anhänge hängt. Ob der Kommentar Anhänge hat,
     steht in der Op nicht; ohne Board-Stand ist es nicht entscheidbar. Sichere
     Richtung: melden — ein Fehlalarm kostet einen Hinweis, eine unentdeckte
     Kollision einen Anhang, den die Kaskade wieder entfernt. */
  const loeschtAnhaenge = (x, k) => x && x.type === 'comment.delete' &&
    (k === 'feld:attachments' || k === 'feld:coverAttachmentId' || (k.startsWith('menge:') && k.slice(6, k.indexOf(':', 6)) === 'attachments'));
  if (loeschtAnhaenge(a, kb) || loeschtAnhaenge(b, ka)) return true;
  /* Dasselbe für Mengen-Listen (ADR-0130 §3): card.field ersetzt die ganze
     Liste und kollidiert deshalb mit jeder Mengen-Op auf ihr. Zwei gleiche
     Mengen-Op-Schlüssel (add/add oder remove/remove desselben Elements) sind
     idempotent, add gegen remove desselben Elements ist ein echter Wettlauf. */
  const mengeVon = k => (k.startsWith('menge:') ? k.slice(6, k.indexOf(':', 6)) : null);
  if ((ka.startsWith('feld:') && mengeVon(kb) === ka.slice(5)) ||
      (kb.startsWith('feld:') && mengeVon(ka) === kb.slice(5))) return true;
  if (ka.startsWith('menge:') && ka === kb) return a.type !== b.type;
  return ka === kb;
}

/* Die Karte, die eine Op trifft, oder `null` für Nicht-Karten-Ops. Dieselbe
   Lesart wie `findOpsVorMarke` (`p.cardId || p.card.id`), nur typgebunden:
   Ein fremder Schlüssel `cardId` auf einer `board.field`-Op trifft keine Karte. */
function zielKarteVonOp(op) {
  if (!op || typeof op !== 'object') return null;
  const p = op.payload || {};
  if (op.type === 'card.upsert') {
    return p.card && typeof p.card.id === 'string' ? p.card.id : null;
  }
  if (CARD_TARGET_OPS.has(op.type)) return typeof p.cardId === 'string' ? p.cardId : null;
  return null;
}

/* ── F-826 — Zeitstempel, die keine Uhr erklärt ─────────────────────────
 *
 * `findFutureOps` deckt die Uhren-Schieflage ab. Zwei Formen bleiben dort
 * per Konstruktion unsichtbar, beide gemessen:
 *
 *   a) ein `ts`, der `validateOp` besteht (geprüft wird nur
 *      `typeof === 'string' && !== ''`), aber nicht ISO ist — gemessen mit
 *      `ts: 'morgen frueh'`: gültige Op, rein lexikografisch einsortiert,
 *      null Treffer bei der Zukunfts-Prüfung. Lexikografisch heisst hier:
 *      'm' > '2', die Op landet ganz hinten, ohne dass es jemand merkt.
 *   b) ein absurd alter `ts` (`0001-01-01T00:00:00.000Z`) — die
 *      Rückdatierung, die einer bestehenden Karte ihre Nummer wegnimmt.
 *
 * WAS DIESE PRÜFUNG NICHT LEISTET — ausdrücklich, damit niemand sich darauf
 * verlässt: Eine um Stunden oder Tage rückdatierte Op bleibt unsichtbar. Es
 * gibt für die tatsächliche Schreibzeit keinen unabhängigen Zeugen: der
 * Dateiname wird aus `op.ts` erzeugt (`opFileName`), ist also dasselbe
 * Zeugnis, und die Datei-mtime überlebt keinen `git clone` — die Op-Dateien
 * dieses Repos sind versioniert, ein frischer Checkout setzt sie alle auf
 * die Checkout-Zeit und ergäbe einen Massen-Fehlalarm. Deshalb wird hier nur
 * gemeldet, was keine Uhr und kein Checkout erklären kann: unparsbar oder
 * jenseits jeder plausiblen Projektlaufzeit.
 *
 * Warum die Vergangenheits-Grenze so viel grosszügiger ist als die 5 Minuten
 * der Zukunfts-Toleranz: Ops dürfen legitim alt sein. Eine Inbox wird erst
 * ab 50 Ops kompaktiert, ein Konsumenten-Board lag nachweislich Monate
 * ungefaltet (F-543). Alt ist normal; „vor der Erfindung der Zeitzone" ist
 * eine Eingabe. */
export const PAST_TS_TOLERANCE_MS = 365 * 24 * 60 * 60 * 1000;   /* 1 Jahr */

/**
 * Findet Ops mit einem `ts`, den weder eine Uhr noch ein Checkout erklärt.
 *
 * @param {Array<object>} ops     gültige Ops (Poison vorher aussortiert)
 * @param {number} nowMs          Bezugszeit (injizierbar für Tests)
 * @param {number} [maxPastMs]    ab wann „absurd alt"
 * @returns {Array<{op: object, grund: 'nicht-iso'|'absurd-alt', hinterMs?: number}>}
 */
/** Der Zeitstempel-FORMATVERTRAG dieses Projekts — exakt die Form, die
 *  `makeOp` erzeugt. Bewusst enger als das, was `tsSchluessel` in `src/ops.js`
 *  noch deuten kann: Dort ist eine Obermenge richtig (sonst landete ein
 *  fremder, aber eindeutiger Zeitstempel still hinten), hier eine engere
 *  Menge (sonst schwiege der Melder über eine Abweichung vom Vertrag).
 *
 *  EXPORTIERT seit F-963 (Sprint-65-Kritiker, B13): `tests/ops.test.js` hatte
 *  dieses Muster ABGESCHRIEBEN, um zu prüfen, dass `TS_MUSTER` es vollständig
 *  abdeckt. Eine Kopie prüft aber nur gegen sich selbst — ändert sich das
 *  Original, bleibt der Test grün und die Deckung ist weg. Das ist die
 *  Fehlerklasse „Guard gegen das Modell geprüft, aus dem er gebaut ist".
 *  Wer das Muster ändert, ändert es hier und nur hier. */
export const ISO_TS_MUSTER = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

export function findImplausibleTs(ops, nowMs, maxPastMs = PAST_TS_TOLERANCE_MS) {
  if (!Array.isArray(ops) || !Number.isFinite(nowMs)) return [];
  const treffer = [];
  for (const op of ops) {
    const roh = op && typeof op.ts === 'string' ? op.ts : null;
    if (roh == null) continue;                 /* Hüllen-Prüfung ist validateOp's Sache */
    const t = Date.parse(roh);
    if (!Number.isFinite(t)) { treffer.push({ op, grund: 'nicht-iso' }); continue; }
    /* Date.parse ist nachsichtig ('2026' oder 'Aug 28 2026' parsen). Der
       Vertrag ist ISO-8601 mit Zeitzone, so wie makeOp ihn erzeugt — alles
       andere sortiert zwar, aber nicht nachvollziehbar. */
    if (!ISO_TS_MUSTER.test(roh)) {
      treffer.push({ op, grund: 'nicht-iso' });
      continue;
    }
    const hinterMs = nowMs - t;
    if (hinterMs > maxPastMs) treffer.push({ op, grund: 'absurd-alt', hinterMs });
  }
  treffer.sort((a, b) => (b.hinterMs ?? Infinity) - (a.hinterMs ?? Infinity));
  return treffer;
}

/* ── Ops, die älter sind als der Basis-Snapshot (F-1014) ─────────────────
 *
 * DAS PROBLEM, GEGEN DAS DAS HIER STEHT: Nach einem
 * `--rebase-from-projection` ist die Projektion die neue base — sie enthält
 * die Wirkung der bis dahin gefalteten Ops also bereits. Die Inbox bleibt
 * dabei absichtlich unangetastet (der Bediener soll nichts verlieren), und
 * genau deshalb werden dieselben Ops beim nächsten Lauf ein ZWEITES Mal
 * angewandt — diesmal auf einen weiterentwickelten Stand. Eine `card.field`-Op
 * vom 19.08. setzt dann einen Titel zurück, der seit dem 27.08. anders lautet.
 *
 * Gemessen (F-1014): Der Lauf meldet dabei `Exit 0` und „0 Poison". Der
 * Zugehörigkeits-Riegel (`detectStructuralLoss`) schlägt nicht an, weil er
 * MENGEN vergleicht — es verschwindet ja keine Karte, nur ihr Feldinhalt
 * fällt zurück. Der Verlust ist damit über nichts sichtbar.
 *
 * WARUM DAS KEIN RANDFALL IST: Ein Board läuft in den Rebase, WEIL es lange
 * nicht gefaltet hat. Dass dann alte Ops in der Inbox liegen, ist keine
 * unglückliche Zusatzbedingung, sondern dieselbe Ursache.
 *
 * WARUM DER ZEITSTEMPEL NICHT AUS DER base KOMMT: Ein Feld in der base wäre
 * der naheliegende Ort und wäre falsch. `detectProjectionAnomaly` hält den
 * base-Snapshot gegen die Projektion; ein Feld, das nur auf einer Seite steht,
 * macht die beiden dauerhaft ungleich und schaltet die Stall-Erkennung still
 * ab — die Falle aus §25 in knowledge/topics/nebenlaeufigkeit-und-lesefenster.md,
 * dort mit genau diesem Wächter belegt. Die Marke gehört deshalb NEBEN die
 * Daten (Dotfile in der Inbox, vom Einlesen ohnehin übersprungen), nicht
 * hinein.
 *
 * VERGLICHEN WIRD ÜBER `tsSchluessel`, NICHT ALS ZEICHENKETTE ([[F-927]]):
 * `'…40.435Z' > '…40.435778917Z'` ist als Zeichenkette wahr und chronologisch
 * falsch. Sobald ein fremdes Werkzeug mit anderer Präzision dieselbe Inbox
 * bedient, hinge die Meldung sonst am Format des Schreibers.
 *
 * MELDEN, NICHT ABLEHNEN — dieselbe Begründung wie bei F-700/[[F-926]]: Die
 * Ops sind bereits geschrieben. Sie hier zu verwerfen hiesse, Bestand wegen
 * eines Verdachts zu löschen, den der Leser nicht verursacht hat. Und der
 * Verdacht ist nicht sicher: Eine Op, die nach dem letzten erfolgreichen
 * Faltlauf hinzukam, aber einen älteren `ts` trägt (nachgetragene Op,
 * fremdes Werkzeug, Uhrversatz), ist legitim und würde mit verworfen.
 *
 * REIN — wie alles in dieser Datei.
 *
 * @param {Array<object>} ops   gültige Ops des Batches
 * @param {string|null} markeTs ISO-Zeitstempel der Rebase-Marke, oder null
 * @returns {null | { anzahl: number, markeTs: string,
 *                    betroffen: Array<{op: object, type: string,
 *                                      cardId: string|null, ts: string}> }}
 */
export function findOpsVorMarke(ops, markeTs) {
  if (!Array.isArray(ops) || ops.length === 0) return null;
  const grenze = tsSchluessel(markeTs);
  if (!grenze) return null;

  const betroffen = [];
  for (const op of ops) {
    const k = tsSchluessel(op && op.ts);
    /* Ein nicht deutbarer ts gilt NICHT als älter. Er wird an anderer Stelle
       gemeldet (`findImplausibleTs`, `nicht-iso`); ihn hier zusätzlich als
       Verdacht zu führen, hiesse denselben Befund zweimal zu melden und die
       eigentliche Meldung zu verwässern. */
    if (!k) continue;
    if (k.ms > grenze.ms || (k.ms === grenze.ms && k.sub >= grenze.sub)) continue;
    const p = (op && op.payload) || {};
    /* Die Op SELBST zurückgeben, nicht einen Dateinamen daraus ableiten:
       `opFileName` lebt ebenfalls hier, der Aufrufer formatiert damit — so wie
       `findFutureOps` es hält. Eine reine Funktion, die Dateinamen erfindet,
       hätte eine zweite Wahrheit über die Benennung. */
    betroffen.push({
      op,
      type:   op.type,
      /* `cardId` deckt card.field/move/delete; card.upsert trägt die Kennung
         eine Ebene tiefer. Beides ist das, wonach ein Mensch die betroffene
         Karte sucht. */
      cardId: p.cardId || (p.card && p.card.id) || null,
      ts:     op.ts,
    });
  }
  if (betroffen.length === 0) return null;
  /* Älteste zuerst (Kritiker-Lauf Sprint 66, Befund A4): Der Aufrufer zeigt
     nur die ersten zehn. Unsortiert wäre das die Reihenfolge von `readdirSync`
     — also eine dateisystem-abhängige Zufallsauswahl statt der Ops, die am
     weitesten vor der Marke liegen und damit am ehesten Schaden angerichtet
     haben. `findFutureOps` und `findImplausibleTs` sortieren aus demselben
     Grund, dort nach Vorsprung bzw. Rückstand. */
  betroffen.sort((a, b) => {
    const ka = tsSchluessel(a.ts), kb = tsSchluessel(b.ts);
    return ka.ms - kb.ms || ka.sub - kb.sub;
  });
  return { anzahl: betroffen.length, markeTs, betroffen };
}
