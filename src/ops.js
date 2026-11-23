/* Op-Inbox + deterministischer Apply-Core — ADR-0067 Stufe 1 (Karte #228)
 *
 * Behebt F-283 strukturell: statt dass Browser UND externe Akteure die
 * komplette `aboard-sync.json` schreiben (Lost-Update-Race), schreibt jeder
 * nur noch eine unveränderliche Op-Datei in die Inbox. Ein deterministischer
 * Projektor faltet Basis-Snapshot + geordnete Ops → Projektion.
 *
 * DIESES MODUL IST DER GETEILTE APPLY-CORE — es läuft IDENTISCH im
 * Browser-Bundle (via Vite) UND im headless Node-CLI (tools/aboard-projektor.mjs).
 * Daraus folgen harte Regeln:
 *
 *   1. KEINE DOM-/window-/Browser-Abhängigkeit. Nur reines JS.
 *   2. `fold()` ist eine REINE Funktion: kein Date.now(), kein Math.random(),
 *      kein crypto.randomUUID() im Faltpfad. Op-`id`/`ts` werden vom Schreiber
 *      EINMAL bei Op-Erzeugung gesetzt (makeOp), nie im fold. Nur so
 *      konvergieren zwei gleichzeitige Projektoren auf dasselbe Ergebnis
 *      (ADR-0067 §2 — Race ist gutartig, Heartbeat ist nur Effizienz).
 *   3. `displayId` vergibt der Projektor beim Apply (NICHT der Schreiber) —
 *      Kollisionen wie F-284 sind damit strukturell unmöglich.
 *
 * Stufenschnitt (ADR-0067): #228 = Kern (dieses Modul + Layout + CLI +
 * Browser-Verdrahtung). Kompaktierung/Rotation, Optimistic-UI-Politur,
 * Op↔Server-REST-Angleichung sind explizit #230 (Stufe 2) — hier bewusst NICHT
 * enthalten: der Projektor faltet in Stufe 1 immer `base + ALLE Inbox-Ops`
 * (kein Pruning, kein Sidecar-State) — voll deterministisch, ohne Delete-Race.
 */

/* Karte #609: die Prüfung der Abbildung liegt in einem reinen Modul, das auch
   Diff, Migration und Renderer nutzen (eine Regel, keine Kopien). */
import {
  AWAITING_PERSON_MAX, awaitingPersonGueltig, istAwaitingEintrag,
  awaitingAlsAbbildung, awaitingOderNull, awaitingSitzungGueltig, awaitingSitzungsSchluessel,
  kappeAwaitingSitzungen,
} from './awaiting.js';
/* Karte #666 (ADR-0133 §1): Wortschatz und Wertprüfung der Finding-Karten —
   ebenfalls ein reines Modul, das die Oberfläche mitbenutzen kann. */
import { FINDING_KIND, FINDING_FELDER, findingFehler, nextFindingNr } from './finding.js';

/* Schema-Version der Ops. Erhöhen bei inkompatibler Payload-Änderung;
   ein Konsument darf eine unbekannte (höhere) Version als Poison behandeln. */
export const OP_SCHEMA_VERSION = 1;

/* Datei-Layout (ADR-0067 §4/§5). Single Source of Truth — Browser-Bundle
   UND headless-CLI importieren diese Konstanten, damit der Pfad-Vertrag
   nirgends driftet. Pfade sind relativ zum Projektverzeichnis (dort, wo
   heute schon aboard-sync.json liegt — Pfad BLEIBT, ADR-0067 §4). */
export const PROJECTION_FILE = 'aboard-sync.json';        /* Projektion (read-only für alle außer Projektor) */
export const BASE_FILE       = 'aboard-sync.base.json';   /* immutabler Basis-Snapshot (einmalig via Migration) */
export const INBOX_DIR       = 'aboard-sync.inbox';       /* append-only Op-Dateien <ts>-<uuid>.json */
export const REJECTED_DIR    = 'aboard-sync.inbox/.rejected';   /* Poison-Ops (nicht still droppen) */
export const HEARTBEAT_FILE  = 'aboard-sync.inbox/.heartbeat.json'; /* „genau einer"-Koordination (best-effort) */
/* F-1014: Marke, die ein `--rebase-from-projection` hinterlässt. Dotfile, damit
   das Inbox-Einlesen sie ohnehin überspringt — und bewusst NEBEN den Daten statt
   als Feld in der base: Ein Feld dort stünde nur auf einer Seite des Vergleichs
   base ↔ Projektion und schaltete die Stall-Erkennung still ab (§25 in
   knowledge/topics/nebenlaeufigkeit-und-lesefenster.md). */
export const REBASE_MARKE    = '.rebase-marke.json';
/* F-1014 (offener Rest, Karte #512): Dieselbe Idee für den Fall OHNE Rebase.
   Nach einer Kompaktierung steckt die Wirkung aller gefalteten Ops in der
   neuen base; eine Op, die DANACH mit einem ÄLTEREN `ts` eintrifft, wirkt auf
   einen Stand, der sie schon enthält — und überschreibt dabei, was seither
   dazukam. `detectStructuralLoss` kann das nicht sehen: es vergleicht MENGEN,
   und hier verschwindet keine Karte, nur ihr Feldinhalt fällt zurück.

   WARUM EINE MARKE UND KEIN FELD AN DER KARTE: Ein `kartenVersion`-Feld, das
   `applyOp` pflegt, war der erste Entwurf dieser Karte und ist gemessen
   gescheitert — es bricht die Round-Trip-Invariante
   `fold(prev, diff(prev, next)) ≡ next` (ADR-0079): `fold` setzt ein Feld, das
   `next` nicht hat, der nächste Diff sieht einen Unterschied und emittiert
   erneut. Zehn Tests in `diff.test.js` und
   `diff-card-upsert-huckepack-464.test.js` haben das sofort gezeigt. Eine
   Marke NEBEN den Daten berührt weder Board noch Diff — dieselbe Begründung,
   aus der REBASE_MARKE oben kein base-Feld ist. */
export const FOLD_MARKE      = '.fold-marke.json';

export const OP_TYPES = /*#__PURE__*/ Object.freeze([
  'card.upsert', 'card.move', 'card.delete', 'card.field', 'comment.add', 'comment.delete',
  /* ADR-0130 §4 (Karte #601): Text eines EINZELNEN Kommentars ändern, statt
     die ganze Karte per card.upsert zu ersetzen. Eigener Typ (ADR-0130 §2,
     K-2): ein älterer Falter kennt ihn nicht und sortiert ihn als Poison aus,
     statt ihn halb anzuwenden. */
  'comment.edit',
  /* ADR-0130 §2 (Karte #604): card.field mit erwartetem Vorwert. Weicht der
     Stand ab, wirkt die Op nicht und meldet `feld-geaendert` — statt still zu
     überschreiben. Eigener Typ statt optionalem Feld an card.field: ein alter
     Falter meldet unbekannte Zusatzschlüssel nur und wendete die Op dann
     bedingungslos an (K-2). */
  'card.fieldIf',
  /* ADR-0130 §3 (Karte #605): ein Element einer Mengen-Liste hinzufügen bzw.
     entfernen, statt die ganze Liste per card.field zu ersetzen — zwei
     gleichzeitige Einträge (zwei Beteiligte, zwei Labels) bleiben beide. */
  'card.listAdd', 'card.listRemove',
  /* ADR-0130 §1 (Karte #606): Karte übernehmen und freigeben, mit Lease.
     Eigene Typen, denn sie SCHRÄNKEN ein (knowledge §40): Ein älterer Falter
     sortiert sie als Poison aus, statt eine Übernahme still zu übergehen. */
  'card.claim', 'card.release',
  'swimlane.upsert', 'swimlane.move', 'swimlane.delete', 'board.field',
  /* ADR-0130 §7 (Karte #609): `awaitingUser` je Person. Setzt genau EINEN
     Schlüssel der Abbildung (`value: null` löscht ihn). Eigener Typ statt
     board.field: das ersetzte die ganze Abbildung, und zwei gleichzeitige
     Fragen verlören eine (Kritiker K-1). Ein älterer Falter kennt den Typ
     nicht und sortiert ihn als Poison aus — gewollt (K-2). */
  'board.awaiting',
  /* ADR-0138 §4 (Karte #743): Termine ohne Karte in `board.termine[]`.
     Eigene Typen statt board.field: board.field ersetzte die ganze Liste, und
     zwei gleichzeitig angelegte Termine verlören einen (dieselbe Lage wie bei
     awaitingUser, Kritiker K-1 zu ADR-0130 §7). Ein älterer Falter kennt die
     Typen nicht und sortiert sie als Poison aus, statt sie halb anzuwenden. */
  'termin.upsert', 'termin.delete',
  /* ADR-0141 §5.3 (Karte #827, Board-Paar Stufe 2c): bedingte Ops für den
     Rückweg von der externen Kopie nach innen. Je eine Bedingung („die Stelle
     steht noch, wie außen gesehen“); weicht der Stand ab, wirkt die Op nicht
     und meldet `feld-geaendert` — der Server schreibt dann den
     Konflikt-Kommentar (§6). Eigene Typen statt Zusatzfeldern an card.move,
     comment.edit und termin.upsert (knowledge §40: ein Merkmal, das
     einschränkt, braucht einen eigenen Typ — ein älterer Falter sortiert ihn
     als Poison aus, statt bedingungslos anzuwenden). Am Server nimmt sie nur
     der Rückweg des Board-Paars an (server/api/boards_ops.go). */
  'card.moveIf', 'card.checkItemIf', 'comment.editIf', 'termin.fieldIf',
  /* ADR-0071 Achse 2 (#230): Wartungs-/Signal-Op. KEINE Board-Mutation
     (board-neutral im fold) — reines Seitenkanal-Signal, das NUR der
     headless-Projektor auswertet (Browser ignoriert es, kompaktiert nie).
     Muss in OP_TYPES stehen, sonst sortierte partitionInbox ihn als Poison
     nach .rejected/ aus. Append-only wie jede Op → kann nichts korrumpieren. */
  'maintenance.compact',
  /* ADR-0106 (#331): Diagnose-Marker. KEINE Board-Mutation (board-neutral).
     Wird vor einem Mass-Delete-Guard-Batch vorangestellt, damit der Projektor
     den ungewöhnlichen Batch in der Inbox nachvollziehen kann. */
  'maintenance.diagnostic',
]);

/* Felder, die NICHT per card.field/board.field gesetzt werden dürfen —
   Identität & projektor-eigene Buchhaltung sind tabu (Determinismus/F-284).

   F-940 (Sprint-64-Verify): Dazu die drei Prototyp-Schlüssel. `applyOp` macht
   unten `loc.card[p.field] = clone(p.value)` — ein dynamischer Schlüsselzugriff.
   Mit `field: '__proto__'` setzt das den PROTOTYP der Karte statt einer
   Eigenschaft: In der Projektion ist davon nichts zu sehen (`JSON.stringify`
   überspringt den Prototyp), im Speicher trägt die Karte danach jedes Feld des
   untergeschobenen Objekts — etwa ein `qa: true` auf einer Karte, die selbst
   kein `qa` hat.

   Der Schutz gehört HIERHER und nicht (nur) in `tools/aboard-op.mjs`: Der Fold
   ist die Vertrauensgrenze, an der Fremd-Ops ankommen. Die erste Fassung des
   Fixes sass allein im CLI — jedes Skript, das `makeOp` direkt importiert (die
   in CLAUDE.md dokumentierte Op-Append-Praxis!), umging sie vollständig. Ein
   Guard am Komfort-Werkzeug schützt den, der das Werkzeug benutzt; ein Guard
   im Fold schützt das Board.

   Rein und deterministisch: eine Mengenabfrage über einen konstanten Set,
   keine Zeit-/Zufallsquelle (ADR-0067 §2). Bit-identisch für Browser und CLI.
   Nachgemessen vor dem Einbau: In der gesamten git-Historie dieses Repos gibt
   es keine einzige Op mit einem dieser Feldnamen — die Projektion bestehender
   Boards ändert sich also nicht. */
const CARD_FIELD_BLOCKLIST  = new Set([
  'id', 'displayId',
  '__proto__', 'constructor', 'prototype',
  /* ADR-0130 §1 (Karte #606): Die Übernahme setzen nur card.claim und
     card.release. Über card.field könnte jeder die Lease eines anderen
     überschreiben, ohne dass die Bedingung „ist die Karte frei?“ geprüft wird. */
  'claim',
  /* ADR-0141 §7 (Karte #826): Die Marke `spiegel` setzt nur der Eingang des
     Board-Paars (Server, außen), `boardpaarUrsprung` ab 2c nur der Akteur
     `boardpaar` beim Anlegen (server/fold/fold.go) — beide nie über card.field. */
  'spiegel', 'boardpaarUrsprung',
]);

/* ADR-0130 §1 (Karte #606): Felder, die ausschließlich eigene Ops des Folds
   setzen. `card.upsert` übernimmt sie IMMER von der Bestandskarte und
   ignoriert den Wert aus der Payload (bei Neuanlage: entfernt ihn) — anders
   als `VOM_FOLD_AUF_BESTAND_BEWAHRT`, wo ein ausdrücklicher Payload-Wert
   gewinnt. Sonst schriebe ein Volltext-Upsert aus `src/diff.js` den
   veralteten Übernahme-Stand des Browsers zurück. `src/diff.js` überträgt
   diese Felder nie, `tools/aboard-op.mjs` zählt sie nicht als Verlust.
   ADR-0141 §7 (Karte #826): dazu die Marke `spiegel` einer Spiegelkarte und
   `boardpaarUrsprung` (Außen-ID einer außen angelegten Karte) — der Bestand
   gewinnt immer, eine Payload nie; auf einer neuen Karte (auch beim
   Duplizieren oder aus einer Vorlage) fallen beide weg. Die Ausnahme für den
   Akteur `boardpaar` beim Anlegen (2c) steht nicht hier, sondern im
   Server-Fold (server/fold/fold.go, `boardpaarAnlage`). */
export const VOM_FOLD_VERWALTET = /*#__PURE__*/ Object.freeze(['claim', 'spiegel', 'boardpaarUrsprung']);

/* Lease-Dauer in Sekunden (ADR-0130 W1: 2 Stunden, Erneuerung durch den
   Halter). Ein Wert aus der Payload wird auf [MIN, MAX] geklemmt. */
export const CLAIM_LEASE_STANDARD = 7200;
export const CLAIM_LEASE_MIN = 60;
export const CLAIM_LEASE_MAX = 86400;
const CLAIM_SESSION_MAX = 200;
const ERLEDIGT_SPALTE = 'col-erledigt';

/**
 * In welchem Zustand ist die Übernahme einer Karte aus Sicht eines Akteurs?
 * (ADR-0130 §1, Karte #606)
 *
 * EIN Aufschrieb für alle Leser: applyOp (card.claim/card.release), die
 * Server-Brücke (Uhr-Gate in server/fold/fold.go), das CLI und die Anzeige im
 * Browser. Wer die Regel ein zweites Mal aufschreibt, driftet (knowledge §30).
 *
 * - `frei`             — keine Übernahme
 * - `eigen`            — derselbe Halter (Akteur UND Sitzung), aktiv oder abgelaufen
 * - `fremd-aktiv`      — ein anderer hält sie, das Lease-Ende liegt nach `jetztMs`
 * - `fremd-abgelaufen` — ein anderer hielt sie, das Lease-Ende ist erreicht
 * - `erloschen`        — ein anderer hielt sie, die Karte liegt in col-erledigt;
 *                        das gilt unabhängig von der Uhr als beendet
 *
 * Nicht deutbares Lease-Ende oder `jetztMs` → `fremd-aktiv`: Wer nicht sicher
 * weiß, dass die Frist um ist, lässt den Halter Halter bleiben (Ausfall in
 * Richtung Nichtstun). Ein Mensch kann jederzeit erzwingen.
 *
 * REIN: liest nur Karte und Argumente, keine Uhr. „Jetzt“ ist die `ts` der Op.
 *
 * @param {object} card
 * @param {number} jetztMs     Epochen-Millisekunden (die ts der Op)
 * @param {string} actor       wer fragt (op.actor)
 * @param {string|null} [session]
 * @returns {'frei'|'eigen'|'fremd-aktiv'|'fremd-abgelaufen'|'erloschen'}
 */
export function claimZustand(card, jetztMs, actor, session) {
  const c = card && card.claim;
  if (!c || typeof c !== 'object') return 'frei';
  if (c.by === (actor || '') && (c.session || null) === (session || null)) return 'eigen';
  if (card.columnId === ERLEDIGT_SPALTE) return 'erloschen';
  const ende = tsSchluessel(c.until);
  if (!ende || !Number.isFinite(jetztMs)) return 'fremd-aktiv';
  return jetztMs < ende.ms ? 'fremd-aktiv' : 'fremd-abgelaufen';
}

/**
 * Zustand für die Frage „darf DIESE Op die Karte übernehmen?“ (Fold und
 * Server-Uhr-Gate). Wie `claimZustand`, aber `erloschen` löst sich nach der
 * Uhr auf: Eine noch laufende fremde Lease auf einer Karte in col-erledigt
 * blockiert weiter (`fremd-aktiv`), eine abgelaufene ist `fremd-abgelaufen`.
 *
 * Sitzungs-QA #606, K-2: Mit `erloschen` als Freibrief übernahm ein Batch
 * `[card.move → col-erledigt, card.claim]` jede fremd gehaltene Karte, ohne
 * Meldung und ohne Erzwingen. `erloschen` bleibt eine Aussage fürs LESEN
 * (Anzeige: keine Übernahme mehr zu sehen), keine Erlaubnis zum Übernehmen.
 */
export function claimZustandFuerUebernahme(card, jetztMs, actor, session) {
  const z = claimZustand(card, jetztMs, actor, session);
  if (z !== 'erloschen') return z;
  const ende = tsSchluessel(card.claim.until);
  if (!ende || !Number.isFinite(jetztMs)) return 'fremd-aktiv';
  return jetztMs < ende.ms ? 'fremd-aktiv' : 'fremd-abgelaufen';
}

/* Protokoll-Arten der Übernahme — beide Projektoren melden sie mit
   `uebernahmeMeldung` (Sitzungs-QA #606, K-4: vorher liefen sie dort in den
   Rest-Zweig „unbekannte Art“, obwohl das Rennen-Verlieren der Normalfall ist). */
export const UEBERNAHME_ARTEN = /*#__PURE__*/ Object.freeze(['beansprucht', 'nicht-halter', 'uhr-unsicher', 'halter-geaendert']);

/**
 * Hält die Karte noch den Halter, den der Schreiber gesehen hat? (card.release
 * mit `expect`, Karte #618, F-1257). Verglichen werden `by`, `session` und
 * `since` — `since` unterscheidet eine neue Übernahme desselben Halters von
 * der alten (eine Erneuerung behält `since`). Fehlende Werte gelten als null.
 * Keine Übernahme passt zu keinem erwarteten Halter.
 */
export function halterWieErwartet(claim, expect) {
  if (!claim || typeof claim !== 'object') return false;
  const n = v => (v === undefined ? null : v);
  return n(claim.by) === n(expect.by) && n(claim.session) === n(expect.session)
    && n(claim.since) === n(expect.since);
}

/**
 * Vorprüfung für den lokalen Weg (Karte #618, src/main.js → Sync.haengeOpsAn):
 * Dort gibt es keinen atomaren Batch, der Grund-Kommentar wirkte auch, wenn
 * die Aufhebung scheitert. `null` = die Karte hält noch den erwarteten Halter
 * (oder fehlt — das meldet der Fold selbst); sonst `{ halterAktuell }` in der
 * Form, die auch das REST-Plugin aus dem 422 liefert (null = keine Übernahme).
 */
export function halterEinwand(projektion, cardId, erwartet) {
  const loc = projektion && projektion.board ? findCard(projektion.board, cardId) : null;
  const karte = loc && loc.card;
  if (!karte || halterWieErwartet(karte.claim, erwartet)) return null;
  return { halterAktuell: karte.claim ? clone(karte.claim) : null };
}

/** Meldetext zu `feld-geaendert` (card.fieldIf, ADR-0130 §2) — ein Aufschrieb für
    beide Projektoren (Sprint-77-Kritiker: lokal lief die Art vorher in den
    Rest-Zweig „unbekannte Art“; am Server wird sie 422). */
export function feldGeaendertMeldung(k) {
  return `Op ${k.op} (${k.typ}): Karte ${k.cardId}, Feld „${k.feld}“ hatte nicht den erwarteten Wert`
    + ` (aktuell ${JSON.stringify(k.aktuell ?? null)}) — die Änderung hat nichts bewirkt.`;
}

/** Ist `wert` als `effort` KEINE gültige Schätzung? (Karte #772, ADR-0026)
    Gültig sind `null` (zurückgesetzt) und endliche Zahlen >= 0; `undefined`
    (Feld nicht mitgesendet) ist keine Aussage und gilt nicht als ungültig.
    Dieselbe Linie wie `effortFehler` in `tools/aboard-op.mjs` (#734) — dort
    allerdings mit Vorschlag zur Korrektur, hier wird nur beanstandet. Typ per
    `typeof`, nie über die Text-Darstellung (Fehlerklasse 152: `"3"` und `3`
    sehen in JSON-Ausgaben verwechselbar aus). */
export function effortKeineZahl(wert) {
  if (wert === undefined || wert === null) return false;
  return !(typeof wert === 'number' && Number.isFinite(wert) && wert >= 0);
}

/** Meldetext zu `effort-typ` (Karte #772) — ein Aufschrieb für beide Projektoren. */
export function effortTypMeldung(k) {
  return `Op ${k.op} (${k.typ}): Karte ${k.cardId}, effort ist ${k.wert} (${k.typname}) und keine Zahl >= 0 `
    + '(ADR-0026: Stunden als Zahl, z. B. 0.5 oder 4, oder null) — die Op wirkt trotzdem. '
    + 'Summen (src/metrics.js) ignorieren den Wert. Korrektur: '
    + `\`node tools/aboard-op.mjs card.field --cardId ${k.cardId} --field effort --value <zahl> --actor "<wer>"\` `
    + '(in anderen Projekten: `node ~/.aboard-framework/tools/aboard-op.mjs … --dir .`).';
}

/** Meldetext zu einem Übernahme-Protokolleintrag (ein Aufschrieb für beide Projektoren). */
export function uebernahmeMeldung(k) {
  const halter = `${k.halter || '?'}${k.session ? ` (Sitzung ${k.session})` : ''}`;
  const bis = k.until ? ` bis ${k.until}` : '';
  if (k.art === 'beansprucht') {
    return `Op ${k.op} (${k.typ}): Karte ${k.cardId} hält ${halter}${bis} — die Übernahme hat nichts bewirkt.`;
  }
  if (k.art === 'halter-geaendert') {
    return k.halter
      ? `Op ${k.op} (${k.typ}): Karte ${k.cardId} hält inzwischen ${halter}${bis}, nicht der erwartete Halter — nichts aufgehoben.`
      : `Op ${k.op} (${k.typ}): Karte ${k.cardId} ist nicht mehr übernommen — nichts aufgehoben.`;
  }
  if (k.art === 'nicht-halter') {
    return `Op ${k.op} (${k.typ}): Karte ${k.cardId} hält ${halter}${bis} — nur der Halter gibt frei (oder ein Mensch mit erzwingen).`;
  }
  if (!k.halter) {
    return `Op ${k.op} (${k.typ}): Karte ${k.cardId} — Systemuhr nicht gestellt, keine Übernahme möglich (das Lease-Ende wäre falsch).`;
  }
  return `Op ${k.op} (${k.typ}): Karte ${k.cardId} — Systemuhr nicht gestellt, die Übernahme von ${halter} bleibt bestehen.`;
}

/** Epochen-Millisekunden der ts einer Op, oder NaN. */
export function opZeitMs(op) {
  const k = tsSchluessel(op && op.ts);
  return k ? k.ms : NaN;
}

/* Lease-Dauer aus der Payload: Zahl → geklemmt, sonst die Voreinstellung. */
function leaseSekunden(wert) {
  if (typeof wert !== 'number' || !Number.isFinite(wert)) return CLAIM_LEASE_STANDARD;
  return Math.min(CLAIM_LEASE_MAX, Math.max(CLAIM_LEASE_MIN, Math.round(wert)));
}
/* F-290: per Drift-Test (tests/board-field-drift.test.js) gegen
   Model.createBoard-Keys + diff.js BOARD_FIELDS abgesichert. */
/* `awaitingUser` steht seit Karte #609 NICHT mehr hier (ADR-0130 §7): es ist
   eine Abbildung je Person, und board.field ersetzte sie ganz. */
export const BOARD_FIELD_ALLOWLIST = new Set(['title', 'columnDefs', 'templates', 'background', 'identities', 'attachments']);

/* F-1163 (Karte #576): Array-Felder, die `card.field` GANZ ersetzt statt zu
   ergänzen — dieselbe Liste, die der CLI-Hilfetext in `tools/aboard-op.mjs`
   nennt ("CARD.FIELD SETZT, ES ERGAENZT NICHT"). `applyOp` macht unten
   `loc.card[p.field] = clone(p.value)` ohne Typprüfung: Ein Nicht-Array-Wert
   ersetzt die bestehende Liste lautlos. Anlass: `node tools/aboard-op.mjs
   card.field --field links --value @a.json` — `@a.json` landete (vor dem
   Fix in `payloadAusFlags`) als wörtliche Zeichenkette in der Payload, und
   genau diese Zeichenkette ersetzte `links: [...]` durch eine Nicht-Array-
   Zeichenkette, die beim nächsten Lesen als `[]` erschien. Diese Liste ist
   EIN Aufschrieb (F-965 b) — `regelVerwerfung` unten fragt sie, das CLI fragt
   dieselbe Funktion, keine zweite Kopie. */
/* ADR-0130 §3 (Karte #605): Listenfelder, die card.listAdd/card.listRemove
   annehmen. Nur Felder, deren Reihenfolge keine fachliche Bedeutung hat und
   deren Elemente sich über ihren Inhalt identifizieren. `comments`,
   `activity` (eigene Ops/Protokoll) und `attachments` (Ids mit Dateibezug)
   bewusst NICHT. */
export const MENGEN_FELDER = /*#__PURE__*/ Object.freeze(['assignees', 'labels', 'links']);

export const CARD_ARRAY_FIELDS = /*#__PURE__*/ Object.freeze([
  'comments', 'links', 'labels', 'assignees', 'attachments', 'activity',
]);
/* `attachments` fehlte bis zum Sprint-75-Kritiker (F-1217) — die Liste war
   aus dem Hilfetext abgeschrieben statt aus dem Kartenmodell abgeleitet.
   tests/ops.test.js prüft sie seitdem gegen alle Array-Felder von
   Model.createCard(). */

/* ── Op-Erzeugung (Schreiberseite — NICHT im fold aufrufen) ──────────── */

/* Prozess-monotone Sequenz. Zweck: Ops, die in EINEM Batch erzeugt werden
   (z. B. ein State-Diff), teilen sich häufig denselben Millisekunden-`ts`.
   Ohne strikten Tiebreak entschiede die Zufalls-`id` → die Batch-Reihenfolge
   (z. B. „erst Karte verschieben, dann Geschwister neu indizieren") würde
   zerwürfelt. `seq` wird bei Op-ERZEUGUNG eingefroren (nicht im fold) und ist
   in der Op-Datei immutabel → jeder Projektor sortiert identisch
   (Konvergenz bleibt gewahrt, fold bleibt rein). */
let _seq = 0;

function uuid() {
  /* globalThis.crypto.randomUUID: Browser + Node ≥ 16.7. Reiner Fallback
     nur als Notnagel (Testumgebungen ohne WebCrypto). */
  const g = (typeof globalThis !== 'undefined') ? globalThis : {};
  if (g.crypto && typeof g.crypto.randomUUID === 'function') return g.crypto.randomUUID();
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, c => {
    const r = Math.floor(Math.random() * 16);
    const v = c === 'x' ? r : (r & 0x3) | 0x8;
    return v.toString(16);
  });
}

/**
 * Erzeugt eine schema-konforme Op. NUR auf der Schreiberseite aufrufen
 * (Browser-Diff, Sub-Agent, CLI-Helfer) — niemals im Faltpfad.
 *
 * @param {string} type   - einer aus OP_TYPES
 * @param {object} payload - typ-spezifische Nutzlast
 * @param {string} actor  - wer die Änderung verursacht hat (User/Modell/Skript)
 * @returns {object} Op mit id/ts/actor/schemaVersion
 */
export function makeOp(type, payload, actor) {
  return {
    id:            uuid(),
    ts:            new Date().toISOString(),
    seq:           _seq++,
    actor:         actor || '',
    schemaVersion: OP_SCHEMA_VERSION,
    type,
    payload:       payload ?? {},
  };
}

/* ── Payload-Vertrag je Op-Typ (F-451) ──────────────────────────────────
 *
 * Vor F-451 prüfte `validateOp` ausschließlich die Op-HÜLLE — kein einziges
 * Payload-Feld. Ein Schreiber, der einen Schlüssel falsch benannte, bekam
 * keinerlei Signal: der Projektor meldete unverändert „N Op(s) gefaltet,
 * 0 Poison aussortiert", und die Op verpuffte still (oder — schlimmer —
 * halb, siehe unten). Belegt über die git-Historie dieses Repos: von 1246
 * Op-Blobs waren **17 auffällig** — 16 mit unbekanntem Schlüssel, 7 mit
 * fehlendem Pflichtfeld (Schnittmenge 6) —, in sechs Namensvarianten.
 *
 * Warum das kein exotischer Tippfehler ist: Der Payload-Schlüssel heißt
 * bewusst anders als das Feld, das er setzt (`toColId` setzt
 * `card.columnId`). Wer eine Op an der Projektion orientiert baut — der
 * Normalfall für Agenten-Sessions — trifft fast zwangsläufig daneben.
 *
 * ZWEI Schärfegrade, bewusst getrennt:
 *
 *   `required` → fehlt eines dieser Felder, kann die Op per Konstruktion
 *     NICHTS bewirken (`applyOp` fiele ohnehin durch einen frühen `return`).
 *     Solche Ops sind Poison: sie wandern sichtbar nach `.rejected/` statt
 *     still zu verpuffen. Für alle historisch vorgekommenen Formen ist das
 *     KEINE Verhaltensänderung an der Projektion — Alt-Ops falten
 *     bit-identisch weiter (additiv, kein SCHEMA-Bump).
 *     EINE Ausnahme, ehrlichkeitshalber (Kritiker-Lauf): `card.field` ohne
 *     `field` schrieb vorher `card["undefined"] = value` — einen Müll-Schlüssel
 *     — und wird jetzt korrekt abgelehnt. Das ist eine Verbesserung, aber eben
 *     doch ein Unterschied; im historischen Korpus kommt die Form nicht vor.
 *
 *   `known` → alles darüber hinaus ist ein unbekannter Schlüssel. Der wird
 *     NUR GEMELDET, nie zu Poison: unbekannte Schlüssel stehen häufig
 *     ZUSÄTZLICH zu korrekten (in unserer Historie 8 von 16 Fällen —
 *     `fromSprintId`/`fromColumnId` neben korrektem `toSprintId`/`toColId`).
 *     Diese Ops sind voll wirksam; sie zu verwerfen wäre Datenverlust.
 *
 * Die Meldung ist zugleich der einzige Melder für den heimtückischsten Pfad:
 * `toColumnId` statt `toColId` lässt `p.toColId` undefined, der Fallback in
 * `applyOp` greift, und die Karte wechselt die Swimlane, BEHÄLT aber die
 * alte Spalte. Die Op wirkt — nur nicht, was dasteht. `toColId` darf darum
 * NICHT `required` werden: „Swimlane wechseln, Spalte behalten" ist ein
 * legitimer Aufruf. Diese Klasse fängt ausschließlich die `known`-Hälfte.
 *
 * Werte-Typen prüft die Spec bewusst NICHT — nur Anwesenheit von Schlüsseln.
 * Typ-/Enum-Validierung ist eine eigene Frage (F-412/F-417).
 */
export const OP_PAYLOAD_SPEC = /*#__PURE__*/ Object.freeze({
  'card.upsert':      { required: ['card'],                 known: ['card', 'sprintId', 'columnId', 'targetIndex', 'anchor'] },
  'card.move':        { required: ['cardId', 'toSprintId'], known: ['cardId', 'toSprintId', 'toColId', 'targetIndex', 'anchor'] },
  'card.delete':      { required: ['cardId'],               known: ['cardId'] },
  'card.field':       { required: ['cardId', 'field'],      known: ['cardId', 'field', 'value'] },
  /* `expect` nicht required: `null` ist ein gültiger erwarteter Wert, und
     required behandelt null als fehlend. Dass der Schlüssel VORHANDEN ist,
     prüft regelVerwerfung (erwartung-fehlt). */
  'card.fieldIf':     { required: ['cardId', 'field'],      known: ['cardId', 'field', 'value', 'expect'] },
  'card.listAdd':     { required: ['cardId', 'field', 'item'], known: ['cardId', 'field', 'item'] },
  'card.listRemove':  { required: ['cardId', 'field', 'item'], known: ['cardId', 'field', 'item'] },
  /* ADR-0130 §1: `session` unterscheidet zwei Sitzungen desselben Akteurs
     (kein Sicherheitsmerkmal), `erzwingen` hebt eine fremde Übernahme auf
     (am Server nur für Menschen). `onBehalfOf` stempelt NUR der Server
     (E-Mail der Person hinter dem Agenten-Token, Karte #602); einen
     mitgeschickten Wert entfernt er, das CLI lehnt ihn ab. */
  'card.claim':       { required: ['cardId'],               known: ['cardId', 'leaseSeconds', 'session', 'onBehalfOf'] },
  'card.release':     { required: ['cardId'],               known: ['cardId', 'session', 'erzwingen', 'expect'] },
  'comment.add':     { required: ['cardId', 'comment'],    known: ['cardId', 'comment'] },
  'comment.delete':   { required: ['cardId', 'commentId'],  known: ['cardId', 'commentId'] },
  /* `edited` optional: der Schreiber setzt den Bearbeitungsvermerk, der Fold
     erfindet ihn nicht — sonst bräche die Round-Trip-Zusage von diff.js. */
  'comment.edit':     { required: ['cardId', 'commentId', 'text'], known: ['cardId', 'commentId', 'text', 'edited'] },
  'swimlane.upsert':  { required: ['sprint'],               known: ['sprint'] },
  'swimlane.move':    { required: ['sprintId'],             known: ['sprintId', 'targetIndex'] },
  'swimlane.delete':  { required: ['sprintId'],             known: ['sprintId'] },
  /* `value` NICHT required: `board.field` ohne value kommt in der Historie
     vor und ist ein legitimes „Feld leeren" (clone(undefined)). */
  'board.field':      { required: ['field'],                known: ['field', 'value'] },
  /* `value` NICHT required: `null` ist das Löschen und zählt bei required als
     fehlend. Dass der Schlüssel VORHANDEN ist, prüft regelVerwerfung
     (awaiting-wert-fehlt) — sonst löschte eine vergessene Angabe still. */
  'board.awaiting':   { required: ['person'],               known: ['person', 'value', 'session'] },
  /* ADR-0138 §4 (Karte #743): `termin` ist der GANZE Termin (wie card.upsert
     die ganze Karte), die Form prüft regelVerwerfung (terminVerwerfung). */
  'termin.upsert':    { required: ['termin'],               known: ['termin'] },
  'termin.delete':    { required: ['terminId'],             known: ['terminId'] },
  /* ADR-0141 §5.3 (Karte #827). `expect` steht wie bei card.fieldIf nicht in
     required (dort zählt null als fehlend, und null ist bei termin.fieldIf ein
     gültiger Vorwert); dass der Schlüssel da ist, prüft regelVerwerfung. Bei
     card.moveIf ist der erwartete Ort immer gesetzt, daher required. */
  'card.moveIf':      { required: ['cardId', 'toSprintId', 'toColId', 'expectSprintId', 'expectColId'],
                        known: ['cardId', 'toSprintId', 'toColId', 'expectSprintId', 'expectColId', 'targetIndex', 'anchor'] },
  'card.checkItemIf': { required: ['cardId', 'text', 'vorkommen', 'erledigt'],
                        known: ['cardId', 'text', 'vorkommen', 'erledigt', 'expect'] },
  'comment.editIf':   { required: ['cardId', 'commentId', 'text'],
                        known: ['cardId', 'commentId', 'text', 'edited', 'expect'] },
  'termin.fieldIf':   { required: ['terminId', 'field'],    known: ['terminId', 'field', 'value', 'expect'] },
  /* Board-neutrale Signal-Ops — kein Pflichtfeld, aber definierte Nutzlast. */
  'maintenance.compact':    { required: [], known: [] },
  'maintenance.diagnostic': { required: [], known: ['reason', 'cardDeletes', 'swimlaneDeletes'] },
});

/* Nächstverwandter bekannter Schlüssel zu einem unbekannten — für die
   Meldung („meintest du toColId?"). Vorschlag aus einem Konsumenten-Projekt: bei sechs
   beobachteten Namensvarianten trifft schon ein simpler Ähnlichkeitsvergleich
   die meisten Fälle, und der erwartete Name ist genau die Information, die
   dem Schreiber fehlt. Rein + deterministisch (Levenshtein, keine Heuristik
   mit Zufall) — Reinheit von `fold` bleibt unberührt.
   `null`, wenn nichts hinreichend ähnlich ist: ein falscher Vorschlag wäre
   schlimmer als keiner. */
function nearestKnownKey(key, known) {
  const k = String(key).toLowerCase();
  let best = null;
  let bestDist = Infinity;
  for (const cand of known) {
    const c = cand.toLowerCase();
    /* Schwelle: bis zur Hälfte der längeren Länge. Grosszügig genug für die
       real beobachteten Varianten (`targetSprintId` → `toSprintId` hat
       Distanz 5 bei Länge 14), aber eng genug, dass nichts Falsches
       vorgeschlagen wird. Empirisch kalibriert: bei 50 % trifft die Heuristik
       10 von 12 echten Namensdrehern und erzeugt NULL Falschvorschläge; bei
       60 % steigt die Trefferquote nicht nennenswert, aber `columnId` würde
       zu `cardId` und `colId` zu `card` — beides schlimmer als kein
       Vorschlag, weil es den Schreiber aktiv in die Irre führt. */
    const limit = Math.max(2, Math.floor(Math.max(k.length, c.length) / 2));
    const d = _levenshtein(k, c);
    if (d <= limit && d < bestDist) { bestDist = d; best = cand; }
  }
  return best;
}

function _levenshtein(a, b) {
  if (a === b) return 0;
  let prev = Array.from({ length: b.length + 1 }, (_, i) => i);
  for (let i = 1; i <= a.length; i++) {
    const cur = [i];
    for (let j = 1; j <= b.length; j++) {
      cur[j] = Math.min(
        prev[j] + 1,
        cur[j - 1] + 1,
        prev[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1),
      );
    }
    prev = cur;
  }
  return prev[b.length];
}

/**
 * Prüft das Payload einer Op gegen `OP_PAYLOAD_SPEC` (F-451).
 *
 * REIN — keine Seiteneffekte, kein Logging, keine Zeit-/Zufallsquelle. Darf
 * daher gefahrlos aus dem Faltpfad UND aus der Validierungsschicht davor
 * aufgerufen werden (ADR-0067 §2: fold bleibt deterministisch).
 *
 * `missing` prüft auf `== null`, fängt also `undefined` UND explizites `null`.
 * Letzteres war ein Restloch der ersten Fassung (Kritiker-Lauf F-451): ein
 * Schreiber, der ein Pflichtfeld auf `null` setzt — bei generiertem JSON
 * naheliegend —, umging den Wächter vollständig. Für keinen der Pflicht-Keys
 * ist `null` ein legitimer Wert (Zweig für Zweig in `applyOp` geprüft).
 *
 * @param {object} op
 * @returns {{missing: string[], unknown: Array<{key:string, meant:string|null}>}}
 */
export function inspectOpPayload(op) {
  const spec = op && OP_PAYLOAD_SPEC[op.type];
  const p = op && op.payload;
  if (!spec || p == null || typeof p !== 'object') return { missing: [], unknown: [] };
  const missing = spec.required.filter(k => p[k] == null);
  const knownSet = new Set(spec.known);
  const unknown = Object.keys(p)
    .filter(k => !knownSet.has(k))
    .map(k => ({ key: k, meant: nearestKnownKey(k, spec.known) }));
  return { missing, unknown };
}

/**
 * Strukturvalidierung einer Op (Poison-Erkennung).
 * Streng genug, um kaputte/fremde Dateien auszusortieren, tolerant gegenüber
 * Zusatzfeldern. Unbekannte/höhere schemaVersion → Poison (Forward-Schutz).
 *
 * Seit F-451 zusätzlich: fehlende PFLICHT-Payload-Felder → Poison. Das ist
 * bewusst additiv — betroffene Ops konnten schon vorher nichts bewirken,
 * die Projektion bleibt also bit-identisch; neu ist allein, dass sie
 * sichtbar werden statt still zu verpuffen. Unbekannte ZUSATZ-Schlüssel
 * machen eine Op NICHT ungültig (siehe OP_PAYLOAD_SPEC).
 *
 * @returns {boolean}
 */
export function validateOp(op) {
  if (!op || typeof op !== 'object') return false;
  if (typeof op.id !== 'string' || !op.id) return false;
  if (typeof op.ts !== 'string' || !op.ts) return false;
  if (!OP_TYPES.includes(op.type)) return false;
  if (op.schemaVersion !== OP_SCHEMA_VERSION) return false;
  if (op.payload == null || typeof op.payload !== 'object') return false;
  if (inspectOpPayload(op).missing.length > 0) return false;
  return true;
}

/* ── Apply-Core (reiner Faltpfad) ───────────────────────────────────── */

/* Kanonische JSON-Form für den Tiefenvergleich in card.fieldIf: Objekt-
   schlüssel sortiert, damit {a,b} und {b,a} gleich sind. `undefined` bleibt
   von `null` unterscheidbar (eigene Marke) — „Feld fehlt“ ist nicht „Feld ist
   null“. Rein. */
export function stabil(v) {
  if (v === undefined) return '\u0000undefined';
  /* JSON.stringify(NaN) ist 'null' — ohne eigene Marke wäre NaN gleich null
     (Kritiker K-2). Über JSON kommt NaN nie an, im Prozess aber schon. */
  if (typeof v === 'number' && Number.isNaN(v)) return '\u0000NaN';
  if (v === null || typeof v !== 'object') return JSON.stringify(v);
  if (Array.isArray(v)) return '[' + v.map(stabil).join(',') + ']';
  return '{' + Object.keys(v).sort().filter(k => v[k] !== undefined)
    .map(k => JSON.stringify(k) + ':' + stabil(v[k])).join(',') + '}';
}

function clone(o) {
  /* structuredClone: Node ≥ 17 + moderne Browser. JSON-Fallback genügt für
     unsere Daten (kein Date/Map/Set/undefined im Board-Modell). */
  if (typeof structuredClone === 'function') return structuredClone(o);
  return JSON.parse(JSON.stringify(o));
}

/* ── Zeitstempel-Schlüssel für die Totalordnung (F-927) ─────────────────
 *
 * Bis Sprint 65 verglich `orderedOps` den `ts` als ZEICHENKETTE. Solange
 * jeder Schreiber `makeOp` benutzt, ist das korrekt: alle Zeitstempel haben
 * dann die feste Form `YYYY-MM-DDTHH:MM:SS.mmmZ`, und für gleich lange
 * Zeichenketten dieser Form fällt die lexikografische Ordnung mit der
 * chronologischen zusammen.
 *
 * Sobald ein zweites Werkzeug dieselbe Inbox bedient, gilt das nicht mehr.
 * Gemessen (Meldung aus einem Konsumenten-Projekt, dort schreibt ein Go-Werkzeug
 * RFC3339Nano):
 *
 *   '2026-08-31T07:40:40.435Z' > '2026-08-31T07:40:40.435778917Z'   → true
 *
 * `Z` (0x5A) sortiert über `7` (0x37) — der Millisekunden-Op landet HINTER
 * dem Nanosekunden-Op derselben Millisekunde, obwohl er 778 µs früher liegt.
 * Und `seq` rettet nichts: die beiden Zeichenketten sind ungleich, der
 * Tiebreak kommt gar nicht zum Zug. Dieselbe Wirkung wie F-700 — falsch
 * einsortierte Ops verschieben `displayId` und die Anker-Auflösung beim
 * Einfügen, also Karten-Positionen innerhalb einer Spalte.
 *
 * Der Schlüssel ist deshalb ein PAAR aus Epochen-Millisekunden und dem
 * Bruchteil UNTERHALB der Millisekunde:
 *
 *   - `ms`  — ganze Millisekunden seit Epoch, inklusive Zonen-Offset.
 *             `Date.parse` allein taugt hier nicht: es rundet/schneidet den
 *             Sub-Millisekunden-Anteil weg, zwei Nanosekunden-Ops derselben
 *             Millisekunde wären ununterscheidbar.
 *   - `sub` — die Stellen NACH der Millisekunde, rechtsbündig auf sechs
 *             Ziffern (Mikro + Nano) aufgefüllt. `.435` → 0, `.435778917`
 *             → 778917, `.4357` → 700000.
 *
 * REIN und UMGEBUNGSUNABHÄNGIG (ADR-0067 §2): nur `Date.UTC` bzw. die
 * UTC-Setter, keine lokale Zeitzone, kein `Date.now()`, keine Locale. Browser
 * und CLI erzeugen bit-identische Schlüssel.
 *
 * Absichtlich KEINE Bereichsprüfung der Komponenten (Monat 13, Stunde 99):
 * `Date.UTC` rollt sie deterministisch über. Eine strengere Prüfung würde
 * genau die Zeitstempel in den „nicht deutbar"-Topf werfen, die
 * `findImplausibleTs` (src/inbox.js) für ISO-konform hält — dann sortierte
 * etwas still ans Ende, ohne dass der Melde-Kanal etwas sagt. Das Muster hier
 * ist bewusst ein Obermenge des dortigen ISO-Musters.
 */
const TS_MUSTER = /^(\d{4})-(\d{2})-(\d{2})[Tt ](\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(?:[Zz]|([+-])(\d{2}):?(\d{2}))$/;

/**
 * Normalisiert einen ISO-8601-Zeitstempel zu einem vergleichbaren Paar.
 * REIN. `null`, wenn der Wert nicht deutbar ist (kein String, Müll, fehlende
 * Zone) — der Aufrufer entscheidet, was mit solchen Ops geschieht.
 *
 * GRENZE (F-963, Sprint-65-Kritiker B12): `sub` fasst sechs Stellen unterhalb
 * der Millisekunde, also Mikro- und Nanosekunden. Was darunter liegt, wird
 * ABGESCHNITTEN — `…40.435778917123Z` und `…40.435778917Z` liefern denselben
 * Schlüssel und fallen auf `seq`/`id` zurück. Bewusst so: RFC3339Nano geht
 * nicht feiner, und ein BigInt-Schlüssel wäre teurer als der Fall wert ist.
 * Wer je einen Schreiber mit feinerer Auflösung anbindet, muss hier ran.
 *
 * @param {*} ts
 * @returns {{ms: number, sub: number} | null}
 */
export function tsSchluessel(ts) {
  if (typeof ts !== 'string') return null;
  const m = TS_MUSTER.exec(ts);
  if (!m) return null;
  const [, jahr, monat, tag, std, min, sek, bruch, vz, offStd, offMin] = m;
  let ms = Date.UTC(+jahr, +monat - 1, +tag, +std, +min, +sek, 0);
  if (!Number.isFinite(ms)) return null;
  /* Date.UTC bildet zweistellige Jahre auf 1900+N ab — `0001-01-01` wäre
     sonst 1901. Der UTC-Setter kennt diese Sonderregel nicht. */
  if (+jahr >= 0 && +jahr <= 99) {
    const d = new Date(ms);
    d.setUTCFullYear(+jahr);
    ms = d.getTime();
    if (!Number.isFinite(ms)) return null;
  }
  if (vz) {
    const offset = (+offStd * 60 + +offMin) * 60000;
    ms += (vz === '-' ? offset : -offset);
  }
  const nk = bruch || '';
  ms += Number((nk + '000').slice(0, 3));            /* Millisekunden-Anteil */
  const sub = Number((nk.slice(3) + '000000').slice(0, 6));  /* darunter, µs+ns */
  return { ms, sub };
}

/* Totalordnung (Single-Maschine — Uhren ausreichend, ADR-0067 §1):
   1. `ts` als ZEITPUNKT (nicht als Zeichenkette — F-927, siehe oben),
   2. `seq` (prozess-monoton — hält die Reihenfolge eines Op-Batches bei
   ts-Kollision, default 0 für handgeschriebene/Alt-Ops),
   3. `id` (deterministischer Letzt-Tiebreak). Reine Vergleichsfunktion über
   immutable Op-Felder → jeder Projektor sortiert identisch (Konvergenz).
   Liefert eine NEUE Liste — mutiert die Eingabe nicht.

   Ein `ts`, der sich NICHT deuten lässt (Müll, fehlende Zone, kein String),
   sortiert ans ENDE — hinter jeden deutbaren Zeitstempel, untereinander
   lexikografisch über den Rohwert, dann `seq`, dann `id`. Drei Gründe:
   1. Es ist genau das Verhalten, das der alte Zeichenketten-Vergleich für
      den gemessenen Fall (`ts: 'morgen frueh'`) hatte und das der
      Kopfkommentar von `findImplausibleTs` als „landet ganz hinten"
      dokumentiert — die Projektion bestehender Boards ändert sich nicht.
   2. Es ist definiert und deterministisch statt willkürlich: kein Wurf,
      keine Ausnahme mitten im Fold (`fold` läuft im Browser alle 15 s über
      die volle Inbox; eine Ausnahme dort risse die Persistenz ab).
   3. Der Melde-Kanal existiert bereits und bleibt zuständig:
      `findImplausibleTs` meldet `grund: 'nicht-iso'`. Diese Ordnung ist der
      stille Teil, die Warnung der laute — sie ersetzt sie nicht.

   Exportiert seit Karte #602: `tools/aboard-remote.mjs` sendet den Ausgang
   in GENAU dieser Ordnung, nicht in der der Dateinamen (Hinweis framework:
   ein umgestempelter `ts` im Inhalt weicht vom Dateinamen ab, F-113). */
export function orderedOps(ops) {
  /* Schlüssel EINMAL je Op berechnen (Schwartzian). Die Eingabe-Ops werden
     dabei nicht angefasst — der Schlüssel lebt in der Hülle. */
  const dekoriert = [...ops].map(op => ({
    op,
    k:   tsSchluessel(op && op.ts),
    roh: (op && typeof op.ts === 'string') ? op.ts : '',
    /* `seq`/`id` gleich mit auslesen: `orderedOps` läuft VOR `validateOp`
       (siehe `fold`), die Liste kann also `null`-Einträge enthalten. Der alte
       Vergleich wäre daran mit einer TypeError gestorben — mitten im Fold, der
       im Browser alle 15 s läuft. */
    seq: (op && Number.isFinite(op.seq)) ? op.seq : 0,
    id:  (op && typeof op.id === 'string') ? op.id : '',
  }));
  dekoriert.sort((a, b) => {
    if (a.k && b.k) {
      if (a.k.ms  !== b.k.ms)  return a.k.ms  - b.k.ms;
      if (a.k.sub !== b.k.sub) return a.k.sub - b.k.sub;
    } else if (a.k || b.k) {
      return a.k ? -1 : 1;                 /* deutbar vor nicht deutbar */
    } else if (a.roh !== b.roh) {
      return a.roh < b.roh ? -1 : 1;       /* beide nicht deutbar: Rohwert */
    }
    if (a.seq !== b.seq) return a.seq - b.seq;
    if (a.id  !== b.id)  return a.id < b.id ? -1 : 1;
    return 0;
  });
  return dekoriert.map(e => e.op);
}

function findCard(board, cardId) {
  for (const sprint of board.sprints || []) {
    const idx = (sprint.cards || []).findIndex(c => c.id === cardId);
    if (idx !== -1) return { sprint, idx, card: sprint.cards[idx] };
  }
  return null;
}

/* F-3094 (Karte #1171): Boards, die nicht im Browser angelegt wurden, führen
   ihre Backlog-Swimlane teils unter einer anderen id (datenschutz: `00000000-…`
   mit `kind: 'backlog'`). Bis dahin sah der Fold nur `__backlog__` und verwarf
   eine neue Karte ohne `sprintId` dort wortlos. Rückfall daher: erst die
   kanonische id, dann die ERSTE Swimlane mit `kind: 'backlog'` (Reihenfolge der
   Swimlanes, deterministisch). */
function backlogSprint(board) {
  const sprints = board.sprints || [];
  return sprints.find(s => s && s.id === '__backlog__')
      /* Kritik 20.11. K-3: nie in eine archivierte Swimlane (ADR-0117). */
      || sprints.find(s => s && s.kind === 'backlog' && s.archived !== true)
      || null;
}

/** Wohin der Fold eine NEUE Karte aus `card.upsert` legt — dieselbe Regel wie
 *  im Neu-Zweig von `applyOp`: die genannte Swimlane, sonst der Backlog
 *  (`backlogSprint`). `null` heisst: Der Fold verwirft die Op (F-3094).
 *  Exportiert für `tools/aboard-op.mjs`, damit Schreibpfad und Fold dieselbe
 *  Regel fragen statt sie zweimal aufzuschreiben. */
export function zielSwimlaneNeueKarte(board, payload) {
  const p = payload || {};
  return (board?.sprints || []).find(s => s && s.id === p.sprintId)
      || backlogSprint(board || {});
}

/* Vergibt die nächste displayId aus der projektor-internen Sequenz.
   Deterministisch über den geordneten Op-Strom + base.nextDisplayId. */
function nextDisplayId(board) {
  if (!Number.isInteger(board.nextDisplayId) || board.nextDisplayId < 1) {
    /* Defensive Reparatur: aus Bestand ableiten (sollte aus base korrekt sein) */
    let max = 0;
    for (const s of board.sprints || []) for (const c of s.cards || []) {
      if (Number.isInteger(c.displayId) && c.displayId > max) max = c.displayId;
    }
    board.nextDisplayId = max + 1;
  }
  return board.nextDisplayId++;
}

/* Karte #666 (ADR-0133 §1): die nächste Finding-Nummer vergibt
   `nextFindingNr` aus `src/finding.js` — dieselbe Fassung, die auch der
   Browser beim Anlegen nutzt (F-1547). */

/* Nummer eines bestehenden Findings oder `undefined`. */
function bestandsFindingNr(card) {
  const nr = card && card.finding && typeof card.finding === 'object' ? card.finding.nr : undefined;
  return Number.isInteger(nr) ? nr : undefined;
}

/* Nächstverwandter Feldname für einen unbekannten Schlüssel in `card.finding`
   — derselbe Vorschlag wie bei unbekannten Payload-Schlüsseln (F-451). */
function findingVorschlag(fehler) {
  if (!fehler.unbekannt) return fehler.text;
  /* Dazu Präfixe: `prio` ist von `prioritaet` zu weit entfernt für die
     Ähnlichkeitsschwelle, aber die naheliegende Abkürzung. */
  const k = String(fehler.unbekannt).toLowerCase();
  const gemeint = nearestKnownKey(fehler.unbekannt, FINDING_FELDER)
    || (k.length >= 3 ? FINDING_FELDER.find(f => f.startsWith(k) || k.startsWith(f)) : null);
  return gemeint ? `${fehler.text} Meintest du \`${gemeint}\`?` : fehler.text;
}

/**
 * Regeln für Finding-Karten (Karte #666, ADR-0133 §1) — Teil von
 * `regelVerwerfung`, eigene Funktion nur der Lesbarkeit wegen.
 *
 * 1. Kein Umwidmen: Eine bestehende Karte wird nicht nachträglich zum Finding
 *    und ein Finding nicht zur normalen Karte — weder per `card.upsert` noch
 *    per `card.field kind`. Die Nummer gehört der Karte für ihre ganze
 *    Lebenszeit; umwidmen hiesse, Nummern nachzuschieben oder zu verwaisen.
 *    Der Ausweg für ein Finding, das keines ist, ist `status: 'hinfällig'`.
 * 2. Wortschatz: `card.finding` muss `findingFehler` bestehen, sobald die
 *    Karte (nach der Op) ein Finding ist. Eine Karte ohne `kind: 'finding'`
 *    trägt kein `finding`.
 * 3. Nummer: vergibt der Fold. Auf Bestand muss eine mitgeschickte `nr` der
 *    vorhandenen gleichen (der Volltext-Upsert aus `src/diff.js` schickt sie
 *    mit); eine andere wird abgelehnt statt still übergangen. Bei Neuanlage
 *    wird ein Schreiber-Wert ignoriert wie bei `displayId` (F-284).
 *
 * Ziel-Unabhängigkeit wie bei den übrigen Regeln: Der Wortschatz gilt auch,
 * wenn die Karte fehlt; was den Bestand braucht (Umwidmen, Nummer), greift nur,
 * wenn es die Karte gibt.
 */
function findingVerwerfung(board, op) {
  const p = (op && op.payload) || {};
  const ablehnen = (grund, cardId, feld, text) => ({ grund, feld, cardId, text });

  if (op.type === 'card.upsert') {
    const karte = p.card;
    if (!karte || typeof karte !== 'object' || typeof karte.id !== 'string') return null;
    const loc = findCard(board, karte.id);
    const bestandIstFinding = !!loc && loc.card.kind === FINDING_KIND;
    /* `kind` bewahrt der Fold auf Bestand, wenn die Payload es nicht trägt
       (VOM_FOLD_AUF_BESTAND_BEWAHRT) — massgeblich ist die Art NACH der Op. */
    const kindDanach = karte.kind != null ? karte.kind : (loc ? loc.card.kind : karte.kind);
    const wirdFinding = kindDanach === FINDING_KIND;
    if (loc && bestandIstFinding !== wirdFinding) {
      return ablehnen('finding-umwidmung', karte.id, 'kind', bestandIstFinding
        ? `Karte ${karte.id} ist ein Finding — es bleibt eins (die Nummer gehört ihr). Wer es`
          + ' zurückziehen will, setzt `finding.status` auf „hinfällig“.'
        : `Karte ${karte.id} besteht schon als normale Karte. Ein Finding entsteht nur bei`
          + ' Neuanlage (`card.upsert` mit neuer Karten-ID und `kind: \'finding\'`) — sonst'
          + ' entstünden Lücken oder doppelte Nummern.');
    }
    if (!wirdFinding) {
      if (karte.finding != null) {
        return ablehnen('finding-ohne-kind', karte.id, 'finding',
          '`finding` trägt nur eine Karte mit `kind: \'finding\'`.');
      }
      return null;
    }
    const fehler = findingFehler(karte.finding);
    if (fehler) return ablehnen('finding-wert-ungueltig', karte.id, fehler.feld, findingVorschlag(fehler));
    if (loc && karte.finding.nr != null && karte.finding.nr !== bestandsFindingNr(loc.card)) {
      return ablehnen('finding-nr-gesperrt', karte.id, 'finding.nr',
        `\`finding.nr\` vergibt der Fold; Karte ${karte.id} hat ${JSON.stringify(bestandsFindingNr(loc.card) ?? null)},`
        + ` nicht ${JSON.stringify(karte.finding.nr)}. \`nr\` weglassen oder unverändert lassen.`);
    }
    return null;
  }

  /* card.field und card.fieldIf */
  const loc = findCard(board, p.cardId);
  const istFinding = !!loc && loc.card.kind === FINDING_KIND;
  if (p.field === 'kind') {
    if (p.value === FINDING_KIND && !istFinding) {
      return ablehnen('finding-umwidmung', p.cardId, 'kind',
        'Eine bestehende Karte wird nicht nachträglich zum Finding — ein Finding entsteht nur'
        + ' bei Neuanlage (`card.upsert` mit `kind: \'finding\'`), sonst entstünden Lücken oder'
        + ' doppelte Nummern.');
    }
    if (istFinding && p.value !== FINDING_KIND) {
      return ablehnen('finding-umwidmung', p.cardId, 'kind',
        `Karte ${p.cardId} ist ein Finding — es bleibt eins (die Nummer gehört ihr). Wer es`
        + ' zurückziehen will, setzt `finding.status` auf „hinfällig“.');
    }
    return null;
  }
  if (p.field !== 'finding') return null;
  if (loc && !istFinding) {
    if (p.value == null) return null;     /* nichts zu entfernen, nichts falsch */
    return ablehnen('finding-ohne-kind', p.cardId, 'finding',
      '`finding` trägt nur eine Karte mit `kind: \'finding\'`; eine bestehende Karte wird nicht'
      + ' nachträglich zum Finding.');
  }
  if (!loc && p.value == null) return null;
  const fehler = findingFehler(p.value);
  if (fehler) return ablehnen('finding-wert-ungueltig', p.cardId, fehler.feld, findingVorschlag(fehler));
  if (loc && p.value.nr != null && p.value.nr !== bestandsFindingNr(loc.card)) {
    return ablehnen('finding-nr-gesperrt', p.cardId, 'finding.nr',
      `\`finding.nr\` vergibt der Fold; Karte ${p.cardId} hat ${JSON.stringify(bestandsFindingNr(loc.card) ?? null)},`
      + ` nicht ${JSON.stringify(p.value.nr)}. \`nr\` weglassen oder unverändert lassen.`);
  }
  return null;
}

/* Einfügen einer Karte in einen Sprint.

   ADR-0079 (F-311): Bevorzugt ein identitätsstabiler `anchor`
   { mode:'before'|'after'|'end', cardId? } statt eines absoluten
   `targetIndex`. Grund: der absolute Index aus dem Diff-Zielzustand vs.
   der TRANSIENTE Spaltenzustand während des sequenziellen Foldens
   driftet, sobald ≥2 Karten in EINEM Batch in dieselbe (Sprint,Spalte)
   eintreten (F-311). Ein Anker zeigt auf eine Karten-IDENTITÄT und ist
   positionsinvariant — `diff.js` ankert dabei so, dass die Auflösung im
   Normalbetrieb beim ersten Versuch greift (Anker = stabile Karte ODER
   der per seq zuvor schon eingefügte Ziel-Vorgänger).

   Reinheit/Konvergenz (ADR-0067 §2) gewahrt: Auflösung ist eine reine
   Funktion aus transientem Board + immutablen Op-Feldern (anchor.cardId,
   seq) — Browser- und CLI-Projektor falten bit-identisch.

   `targetIndex` bleibt als Legacy-/Defensiv-Pfad: Alt-Inbox-Ops ohne
   `anchor` (vor ADR-0079 geschrieben) falten unverändert weiter — KEIN
   OP_SCHEMA_VERSION-Bump, keine Inbox-Migration (additiv). */
/* F-417 (Karte #453): Zielspalte gegen `board.columnDefs` klemmen.
   Bis hierher übernahm der Apply-Core jede `toColId`/`columnId` ungeprüft —
   jeder Writer (Drag-and-Drop, Fremd-Werkzeuge, ein Tippfehler in einer
   handgeschriebenen Op) konnte eine Karte in eine Spalte schieben, die es
   nicht gibt. Der Renderer zeichnet Spalten aus `columnDefs`; eine Karte
   in einer Phantom-Spalte ist damit in KEINER Spalte sichtbar und für den
   Nutzer nicht von „gelöscht" zu unterscheiden — dieselbe Verlustklasse,
   die F-153/F-364 für Meta-Karten und der K3-Pass in `migrateSprintMeta`
   für den Load-Pfad abdecken. Hier ist der Op-Core-weite Riegel.

   Drei Entscheidungen, alle bewusst:
   1. **Klemmen statt ablehnen.** ADR-0067 kennt im Apply-Core kein Poison,
      nur Fallback — eine abgelehnte Op wäre stiller Datenverlust, eine
      geklemmte ist eine sichtbare Karte an der falschen Stelle.
   2. **Nur bei nicht-leerem `columnDefs`.** Leer heisst „noch unbekannt",
      nicht „keine Spalte existiert": `migrateDefaultColumns` füllt es erst
      beim Board-Load (F-249/ADR-0052), und `tools/aboard-projektor.mjs`
      ruft die Migrationen gar nicht. Ohne diese Bedingung landete auf einem
      Alt-Board jede bewegte Karte im Backlog.
   3. **Keine Warnung von hier aus.** `fold` ist eine reine Funktion
      (ADR-0067 §2) und läuft im Browser alle 15 Sekunden über die volle
      Inbox — ein `console.warn` wäre Dauerspam und ein Seiteneffekt in
      genau der Funktion, deren Reinheit die Konvergenz zweier Projektoren
      trägt.
   Der legitime Fall „neue Spalte anlegen und sofort eine Karte hineinziehen"
   ist nicht betroffen: gemessen emittiert `diffToOps` die
   `board.field(columnDefs)`-Op mit `seq=0` VOR dem `card.move` (seq=1), und
   `orderedOps` hält diese Reihenfolge. */
function klemmeSpalte(board, colId, protokoll, kontext) {
  const defs = Array.isArray(board?.columnDefs) ? board.columnDefs : [];
  if (defs.length === 0) return colId;                     /* noch unbekannt → nicht anfassen */
  if (defs.some(d => d?.id === colId)) return colId;       /* gültig */
  const ziel = defs.some(d => d?.id === 'col-backlog') ? 'col-backlog' : (defs[0]?.id ?? colId);
  /* Nacharbeit zum Kritiker-Lauf Sprint 61: Die Klemmung war vollständig
     stumm — und sie ist ENDGÜLTIG. Gemessen: Sortiert eine `card.move`-Op
     vor die `board.field(columnDefs)`-Op, die ihre Zielspalte anlegt, landet
     die Karte im Backlog und bleibt dort, auch wenn die Spalte in derselben
     Projektion existiert; der nächste Persist schreibt den geklemmten Stand
     als Wahrheit zurück. Ohne Meldung merkt niemand, dass etwas verschoben
     wurde, und die ADR-Zusage „eine sichtbare Karte an der falschen Stelle —
     die kann ein Mensch beheben" setzt genau das voraus.
     Die Nachbarstelle derselben Fehlerklasse, `migrateSprintMeta` K3 in
     src/storage.js, warnt seit Sprint 51 mit Titel, alter und neuer Spalte.
     `fold` bleibt rein: Es wird kein Modul-Zustand angefasst, sondern ein
     Protokoll-Array befüllt, das der AUFRUFER mitgibt. Wer keins mitgibt,
     bekommt exakt das alte Verhalten — und das Ergebnis-Board ist in beiden
     Fällen bit-identisch, worauf die Konvergenz zweier Projektoren beruht
     (ADR-0067 §2). */
  if (Array.isArray(protokoll)) protokoll.push({ art: 'klemmung', ...kontext, von: colId, nach: ziel });
  return ziel;
}

function insertCard(sprint, card, toColId, targetIndex, anchor) {
  card.columnId = toColId;

  if (anchor && typeof anchor === 'object') {
    if (anchor.mode === 'end') { sprint.cards.push(card); return; }
    if ((anchor.mode === 'after' || anchor.mode === 'before') && anchor.cardId != null) {
      const a = sprint.cards.find(c => c.id === anchor.cardId && c.columnId === toColId);
      if (a) {
        const absIdx = sprint.cards.indexOf(a);
        sprint.cards.splice(anchor.mode === 'after' ? absIdx + 1 : absIdx, 0, card);
        return;
      }
      /* Anker (noch) nicht präsent: defensiver Fallback ans Spaltenende.
         Im Normalbetrieb unerreicht (diff.js ankert auf stabile/zuvor
         eingefügte Karten); greift nur bei Alt-/Fremd-/nebenläufigen
         Op-Mischungen — append-only, kann nichts korrumpieren. */
    }
    /* unbekannter/halb-spezifizierter anchor → Legacy-Pfad unten */
  }

  if (Number.isInteger(targetIndex) && targetIndex >= 0) {
    const colCards = sprint.cards.filter(c => c.columnId === toColId);
    if (targetIndex < colCards.length) {
      const at = colCards[targetIndex];
      const absIdx = sprint.cards.indexOf(at);
      sprint.cards.splice(absIdx, 0, card);
      return;
    }
  }
  sprint.cards.push(card);
}

/* ── Termine ohne Karte (ADR-0138 §4, Karte #743) ─────────────────────────
 *
 * `board.termine[]` ist ein eigener Objekttyp neben den Karten. Je Eintrag:
 *   id, titel, datum (YYYY-MM-DD) — Pflicht
 *   bisDatum (YYYY-MM-DD ≥ datum), von/bis ("HH:MM"), ort, beschreibung,
 *   personen (Zeichenketten), karteId, erstellt, erstelltVon — optional
 * Weitere Schlüssel lässt der Fold stehen (ein neuerer Schreiber darf Felder
 * ergänzen, wie bei Karten); geprüft wird nur die Form der bekannten.
 *
 * Kein board.field: `termine` steht bewusst NICHT in BOARD_FIELD_ALLOWLIST
 * (und nicht in BOARD_FIELDS von src/diff.js). Eine ganze Liste je Op verlöre
 * gleichzeitig angelegte Termine — Schreibweg ist termin.upsert/termin.delete.
 *
 * HERKUNFT gehört dem Bestand: `erstellt` und `erstelltVon` übernimmt ein
 * termin.upsert auf einen vorhandenen Termin IMMER vom Bestand (wie displayId
 * bei Karten). Sonst könnte jede Änderung die Urheberschaft umschreiben, und
 * der Server-Stempel für Agenten (server/api/boards_ops.go agentPayload) gälte
 * nur bei der Anlage. */
export const TERMIN_HERKUNFT = /*#__PURE__*/ Object.freeze(['erstellt', 'erstelltVon']);

/* ADR-0141 §7 (Karte #826, Sitzungs-QA QA-5): Die Marke `spiegel` eines
   Spiegeltermins setzt nur der Eingang des Board-Paars, `boardpaarUrsprung`
   ab 2c nur der Akteur `boardpaar`. Für termin.upsert gilt dasselbe wie für
   Karten (VOM_FOLD_VERWALTET): der Bestand gewinnt immer, auf einem neuen
   Termin fallen beide weg. */
export const TERMIN_VERWALTET = /*#__PURE__*/ Object.freeze(['spiegel', 'boardpaarUrsprung']);

/* ADR-0141 §7 (Karte #825): Die Marke `spiegel` an einer Spalte setzt nur der
   Eingang v2 des Board-Paars (Server, außen). Ein `board.field columnDefs`
   ersetzt die ganze Liste — die Marke einer bestehenden Spalte (gleiche id)
   kommt deshalb aus dem Bestand, nie aus der Payload; eine Spalte ohne Marke
   im Bestand bekommt auch durch die Payload keine. Gegenstück für Lanes:
   `swimlane.upsert` mergt nur title/kind/archived und legt neue Lanes ohne
   Marke an — dort braucht es keinen eigenen Schritt. Rein, keine Uhr. */
function spaltenMarkenBewahren(alt, neu) {
  if (!Array.isArray(neu)) return;
  const marken = new Map();
  for (const d of Array.isArray(alt) ? alt : []) {
    if (d && typeof d === 'object' && typeof d.id === 'string' && d.spiegel !== undefined) marken.set(d.id, d.spiegel);
  }
  for (const d of neu) {
    if (!d || typeof d !== 'object') continue;
    if (marken.has(d.id)) d.spiegel = clone(marken.get(d.id));
    else delete d.spiegel;
  }
}
const TERMIN_TEXTFELDER = /*#__PURE__*/ Object.freeze(['ort', 'beschreibung', 'karteId', 'erstellt', 'erstelltVon', 'boardpaarUrsprung']);

/* ADR-0141 §4/§5.3 (Karte #827): die Felder eines Spiegeltermins, die
   termin.fieldIf setzen darf — die Feldliste des Hinwegs. Herkunft, Marke,
   Ursprung, `confidential` und die id gehören nicht dazu. */
export const TERMIN_FELDLISTE = /*#__PURE__*/ Object.freeze(
  ['titel', 'beschreibung', 'datum', 'bisDatum', 'von', 'bis', 'ort', 'personen', 'karteId']);

/* ADR-0141 „Umsetzung Schritt 2b“ Punkt 1 (Karte #827, D8 des Etappenplans):
   die Checklisten-Punkte einer Beschreibung, in der ENGEN Fassung, die der
   Rückweg des Board-Paars verwendet. Ein Punkt ist eine Zeile
   `- [ ] Text` / `- [x] Text` / `- [X] Text` — genau ein Bindestrich am
   Zeilenanfang, ohne Einrückung —, die nicht in einem Code-Zaun (``` oder ~~~,
   bis zu drei Leerzeichen eingerückt) steht. Ein `\r` am Zeilenende zählt
   nicht zum Text. `vorkommen` zählt ab 1 gleichlautende Punkte davor.
   Dieselbe Definition steht in Go (server/schatten/checkliste.go); beide
   prüfen dieselbe Vektordatei (server/schatten/testdata/checkliste-827.json).
   Was diese Fassung nicht erkennt (eingerückte Punkte, `*`-Listen), geht auf
   dem Rückweg als Änderung der ganzen Beschreibung — nie still verloren.
   Rein, keine Uhr. */
export function checklistePunkte(beschreibung) {
  const punkte = [];
  if (typeof beschreibung !== 'string') return punkte;
  const zeilen = beschreibung.split('\n');
  let zaun = null;
  const gezaehlt = new Map();
  for (let i = 0; i < zeilen.length; i++) {
    const zeile = zeilen[i].endsWith('\r') ? zeilen[i].slice(0, -1) : zeilen[i];
    const z = /^ {0,3}(`{3,}|~{3,})/.exec(zeile);
    if (z) {
      if (zaun === null) zaun = z[1][0];
      else if (z[1][0] === zaun) zaun = null;
      continue;
    }
    if (zaun !== null) continue;
    const m = /^- \[( |x|X)\] ([^\n]*)$/.exec(zeile);   /* nicht (.*): `.` trifft in JS kein \r/U+2028, in Go schon (Kritik A-3) */
    if (!m) continue;
    const vorkommen = (gezaehlt.get(m[2]) || 0) + 1;
    gezaehlt.set(m[2], vorkommen);
    punkte.push({ text: m[2], vorkommen, erledigt: m[1] !== ' ', zeile: i });
  }
  return punkte;
}

/* Setzt das Häkchen eines Punkts (Zeilenindex aus checklistePunkte); alle
   übrigen Zeichen der Beschreibung bleiben, auch `\r`. */
function setzeChecklistePunkt(beschreibung, zeile, erledigt) {
  const zeilen = beschreibung.split('\n');
  zeilen[zeile] = '- [' + (erledigt ? 'x' : ' ') + ']' + zeilen[zeile].slice(5);
  return zeilen.join('\n');
}

/* Kalendergültiges Datum YYYY-MM-DD (kein 2026-02-30). Rein: Date.UTC ist
   deterministisch, kein „jetzt“. */
export function istTerminDatum(s) {
  if (typeof s !== 'string' || !/^\d{4}-\d{2}-\d{2}$/.test(s)) return false;
  const [y, m, d] = s.split('-').map(Number);
  const t = new Date(Date.UTC(y, m - 1, d));
  return t.getUTCFullYear() === y && t.getUTCMonth() === m - 1 && t.getUTCDate() === d;
}

/* "HH:MM", 24 h — dieselbe Form wie startTime/dueTime (ADR-0138 §1,
   src/calendar/date-helpers.js istUhrzeit). Hier eigens, weil ops.js nichts
   importiert (ADR-0067 §2). */
export function istTerminZeit(s) {
  return typeof s === 'string' && /^([01]\d|2[0-3]):[0-5]\d$/.test(s);
}

function terminVerwerfung(board, op) {
  const p = op.payload || {};
  const nein = (grund, text) => ({ grund, terminId: typeof p.terminId === 'string' ? p.terminId
    : (p.termin && typeof p.termin.id === 'string' ? p.termin.id : undefined), text });
  if (board.termine != null && !Array.isArray(board.termine)) {
    return nein('termine-kein-array', '`board.termine` ist keine Liste — ein Termin-Op würde den Bestand'
      + ' überschreiben. Erst den Board-Zustand reparieren.');
  }
  if (op.type === 'termin.delete') {
    return typeof p.terminId === 'string' && p.terminId ? null
      : nein('termin-id-ungueltig', '`terminId` ist eine nicht leere Zeichenkette.');
  }
  const t = p.termin;
  if (!t || typeof t !== 'object' || Array.isArray(t)) {
    return nein('termin-kein-objekt', '`termin` ist ein Objekt { id, titel, datum, … } — der ganze Termin.');
  }
  if (typeof t.id !== 'string' || !t.id) return nein('termin-id-ungueltig', '`termin.id` ist eine nicht leere Zeichenkette.');
  if (typeof t.titel !== 'string' || !t.titel.trim()) return nein('termin-titel-fehlt', '`termin.titel` ist ein nicht leerer Text.');
  if (!istTerminDatum(t.datum)) return nein('termin-datum-ungueltig', '`termin.datum` ist ein Datum YYYY-MM-DD.');
  if (t.bisDatum != null && !(istTerminDatum(t.bisDatum) && t.bisDatum >= t.datum)) {
    return nein('termin-bisdatum-ungueltig', '`termin.bisDatum` ist ein Datum YYYY-MM-DD, nicht vor `datum` (oder fehlt).');
  }
  for (const k of ['von', 'bis']) {
    if (t[k] != null && !istTerminZeit(t[k])) return nein('termin-zeit-ungueltig', `\`termin.${k}\` ist eine Uhrzeit "HH:MM" (oder fehlt).`);
  }
  for (const k of TERMIN_TEXTFELDER) {
    if (t[k] != null && typeof t[k] !== 'string') return nein('termin-feld-ungueltig', `\`termin.${k}\` ist Text (oder fehlt).`);
  }
  if (t.personen != null && !(Array.isArray(t.personen) && t.personen.every(x => typeof x === 'string'))) {
    return nein('termin-feld-ungueltig', '`termin.personen` ist eine Liste von Zeichenketten (oder fehlt).');
  }
  return null;
}

/**
 * Verwirft `applyOp` diese Op aus einer REGEL heraus?
 *
 * F-965 (b), Karte #490. Bis Sprint 65 lagen diese Regeln **zweimal** im Repo:
 * einmal als Frühausstieg in `applyOp` und einmal als Guard in
 * `tools/aboard-op.mjs`. Zwei Aufschriebe derselben Regel driften, sobald sich
 * einer ändert (§24 in knowledge/topics/nebenlaeufigkeit-und-lesefenster.md —
 * „wer aus einem Op-Stapel ableitet, was erlaubt ist, baut den Ausführer ein
 * zweites Mal nach"). Jetzt gibt es einen Aufschrieb; `applyOp` und das CLI
 * fragen dieselbe Funktion.
 *
 * ABGRENZUNG — was hier NICHT hineingehört: Frühausstiege, die AUSSCHLIESSLICH
 * am fehlenden ZIEL liegen (Karte oder Swimlane gibt es nicht). Die sind kein
 * Regelverstoss, sondern eine Aussage über den Bestand, und für sie existiert
 * die Waisen-Forensik (`reportOrphanOps`, F-327/F-448).
 *
 * ⚠️ Zwei der drei Regeln gelten UNABHÄNGIG vom Ziel (gesperrtes Kartenfeld,
 * Board-Feld ausserhalb der Allowlist), die dritte braucht den Bestand
 * (Meta-Archivierung — `kind` steht auf der Karte, nicht in der Op). Eine
 * `card.field`-Op mit vertippter `cardId` UND gesperrtem Feld erscheint deshalb
 * in beiden Meldungen: hier und in der Waisen-Forensik. Das ist kein
 * Widerspruch, sondern zwei zutreffende Aussagen über dieselbe Op — die
 * Meldetexte dürfen deshalb NICHT behaupten, das Ziel existiere. Die erste
 * Fassung tat das und ist im Kritiker-Lauf zu Karte #490 aufgefallen.
 *
 * REIN: liest ausschliesslich `board` und `op`, keine Zeit-/Zufallsquelle.
 * `fold` bleibt damit bit-identisch zwischen Browser und CLI (ADR-0067 §2).
 *
 * @param {object} board - Board-Zustand zum Zeitpunkt des Anwendens
 * @param {object} op
 * @returns {null|{grund:string, feld?:string, cardId?:string, text:string}}
 *   `null` = keine Regel greift. Sonst ein Befund mit maschinenlesbarem
 *   `grund` und einem Satz `text` für die Meldung.
 */
export function regelVerwerfung(board, op) {
  const p = (op && op.payload) || {};

  /* card.fieldIf (ADR-0130 §2) unterliegt denselben Feldregeln wie
     card.field — die Bedingung kommt hinzu, sie ersetzt keine Regel. */
  if (op && op.type === 'card.fieldIf' && !('expect' in p)) {
    return {
      grund: 'erwartung-fehlt', feld: p.field, cardId: p.cardId,
      text: 'card.fieldIf ohne `expect` — ohne erwarteten Vorwert gibt es nichts zu prüfen.'
          + ' Für eine bedingungslose Änderung card.field verwenden; `expect: null` erwartet'
          + ' ausdrücklich den Wert null.',
    };
  }

  /* ADR-0141 §5.3 (Karte #827): Formen der bedingten Rückweg-Ops. Nur die
     Form — ob die Stelle noch steht, entscheidet applyOp gegen den Bestand. */
  if (op && op.type === 'card.moveIf') {
    for (const k of ['toSprintId', 'toColId', 'expectSprintId', 'expectColId']) {
      if (typeof p[k] !== 'string' || !p[k]) {
        return { grund: 'ort-ungueltig', cardId: p.cardId,
          text: `card.moveIf: \`${k}\` ist eine nicht leere Zeichenkette (Ziel und erwarteter Ort sind Pflicht).` };
      }
    }
    return null;
  }
  if (op && op.type === 'card.checkItemIf') {
    if (typeof p.text !== 'string' || !Number.isInteger(p.vorkommen) || p.vorkommen < 1
        || typeof p.erledigt !== 'boolean' || typeof p.expect !== 'boolean') {
      return { grund: 'checkpunkt-ungueltig', cardId: p.cardId,
        text: 'card.checkItemIf: `text` ist eine Zeichenkette, `vorkommen` eine ganze Zahl ab 1,'
            + ' `erledigt` und `expect` sind true oder false.' };
    }
    return null;
  }
  if (op && op.type === 'comment.editIf') {
    if (typeof p.commentId !== 'string' || !p.commentId || typeof p.text !== 'string' || typeof p.expect !== 'string') {
      return { grund: 'kommentar-ungueltig', cardId: p.cardId,
        text: 'comment.editIf: `commentId` ist eine nicht leere Zeichenkette, `text` und `expect` (der Text,'
            + ' den der Schreiber gesehen hat) sind Zeichenketten.' };
    }
    return null;
  }
  if (op && op.type === 'termin.fieldIf') {
    if (!('expect' in p)) {
      return { grund: 'erwartung-fehlt', feld: p.field, terminId: p.terminId,
        text: 'termin.fieldIf ohne `expect` — `expect: null` erwartet ausdrücklich ein leeres Feld.' };
    }
    if (!TERMIN_FELDLISTE.includes(p.field)) {
      return { grund: 'termin-feld-nicht-erlaubt', feld: p.field, terminId: p.terminId,
        text: `\`${p.field}\` ist über termin.fieldIf nicht setzbar. Erlaubt sind: ${TERMIN_FELDLISTE.join(', ')}.` };
    }
    if (board.termine != null && !Array.isArray(board.termine)) {
      return terminVerwerfung(board, { type: 'termin.upsert', payload: { termin: null } });
    }
    /* Das Ergebnis muss ein gültiger Termin bleiben (dieselbe Prüfung wie
       termin.upsert). Fehlt der Termin, prüft das der Server (ziel-fehlt). */
    const alt = (board.termine || []).find(x => x && x.id === p.terminId);
    if (alt) {
      /* Flache Kopie, NICHT clone(): Der Server faltet ohne Klon
         (server/fold/fold.go, ADR-0129 §4 Nachtrag) — dort ist clone() die
         Identität, und die Probe veränderte den echten Termin. Danach stand
         der Wert schon so, und der Konflikt fiel still weg (Karte #827, E3b). */
      const neu = { ...alt };
      if (p.value == null) delete neu[p.field];
      else neu[p.field] = clone(p.value);
      return terminVerwerfung(board, { type: 'termin.upsert', payload: { termin: neu } });
    }
    return null;
  }

  /* Karte #666 (ADR-0133 §1): Finding-Karten — Umwidmen, Wortschatz, Nummer.
     Vor den card.field-Regeln, weil deren Zweig mit `return null` endet. */
  if (op && (op.type === 'card.upsert' || op.type === 'card.field' || op.type === 'card.fieldIf')) {
    const befund = findingVerwerfung(board, op);
    if (befund) return befund;
  }

  if (op && (op.type === 'card.field' || op.type === 'card.fieldIf')) {
    if (CARD_FIELD_BLOCKLIST.has(p.field)) {
      return {
        grund: 'card-feld-gesperrt', feld: p.field, cardId: p.cardId,
        text: `\`${p.field}\` ist über card.field nicht setzbar (CARD_FIELD_BLOCKLIST):`
            + ' id und displayId vergibt der Projektor selbst, `claim` setzen nur'
            + ' card.claim/card.release (ADR-0130 §1), die Prototyp-Schlüssel'
            + ' würden statt einer Eigenschaft den Prototyp setzen.',
      };
    }
    /* ADR-0036: Sprint-Meta-Karten sind nicht archivierbar — auch nicht per Op.
       Braucht den Bestand, weil `kind` auf der KARTE steht, nicht in der Op. */
    if (p.field === 'archived' && p.value === true) {
      const loc = findCard(board, p.cardId);
      if (loc && loc.card && loc.card.kind === 'sprint-meta') {
        return {
          grund: 'meta-archivierung', feld: 'archived', cardId: p.cardId,
          text: `Karte ${p.cardId} trägt \`kind: 'sprint-meta'\` — Sprint-Meta-Karten sind`
              + ' nicht archivierbar (ADR-0036), sonst wäre die Sprint-Abschluss-Karte samt'
              + ' Recap für jeden Leser der Projektion weg.',
        };
      }
    }
    /* F-1163: `card.field` auf ein Array-Feld (CARD_ARRAY_FIELDS) mit einem
       Nicht-Array-Wert. `null`/`undefined` bleibt erlaubt (bewusstes Leeren,
       dieselbe Grenze wie bei `board.field` ohne `value`) — abgewiesen wird
       nur ein tatsächlich vorhandener Nicht-Array-Wert. Ziel-unabhängig wie
       die CARD_FIELD_BLOCKLIST-Regel oben: eine Zeichenkette auf `links` ist
       falsch, ob die Karte existiert oder nicht. */
    if (CARD_ARRAY_FIELDS.includes(p.field) && p.value != null && !Array.isArray(p.value)) {
      return {
        grund: 'card-array-feld-kein-array', feld: p.field, cardId: p.cardId,
        text: `\`${p.field}\` ist ein Array-Feld — card.field ersetzt es GANZ, nicht nur`
            + ` einen Eintrag. Der Wert ${JSON.stringify(p.value)} ist aber kein Array.`
            + ' `applyOp` würde die bestehende Liste lautlos durch diesen einen Wert'
            + ' ersetzen (F-1163); jeder Leser, der ein Array erwartet (Karten-Renderer,'
            + ' Filter, Export), stolperte danach über eine Zeichenkette statt einer Liste.'
            + ' Ein Array übergeben, z. B. `[]` zum Leeren.',
      };
    }
    return null;
  }

  if (op && (op.type === 'card.listAdd' || op.type === 'card.listRemove')) {
    if (!MENGEN_FELDER.includes(p.field)) {
      return {
        grund: 'kein-mengen-feld', feld: p.field, cardId: p.cardId,
        text: `\`${p.field}\` nimmt keine Mengen-Ops an. Erlaubt sind: ${MENGEN_FELDER.join(', ')}.`,
      };
    }
    if (p.field === 'links' && !(p.item && typeof p.item === 'object'
        && typeof p.item.type === 'string' && typeof p.item.targetId === 'string')) {
      return {
        grund: 'link-element-ungueltig', feld: 'links', cardId: p.cardId,
        text: 'Ein Element von `links` ist ein Objekt mit mindestens `type` und `targetId` als'
            + ' Zeichenketten. Weitere Schlüssel sind erlaubt und zählen beim Vergleich mit.',
      };
    }
    return null;
  }

  if (op && (op.type === 'card.claim' || op.type === 'card.release')) {
    /* ADR-0130 §1 (Karte #606). Nur die Form, nie den Stand — ob die Karte
       frei ist, entscheidet applyOp gegen den Bestand. */
    /* Gezählt in Unicode-Codepunkten ([...s]), wie der Server (len([]rune)) —
       Sitzungs-QA K-7: `.length` zählte UTF-16-Einheiten, ein Emoji doppelt. */
    if (p.session != null && (typeof p.session !== 'string' || [...p.session].length > CLAIM_SESSION_MAX)) {
      return {
        grund: 'session-ungueltig', cardId: p.cardId,
        text: `\`session\` ist eine Zeichenkette mit höchstens ${CLAIM_SESSION_MAX} Zeichen.`,
      };
    }
    if (op.type === 'card.claim') {
      if (p.leaseSeconds != null && !(typeof p.leaseSeconds === 'number' && Number.isFinite(p.leaseSeconds))) {
        return {
          grund: 'lease-ungueltig', cardId: p.cardId,
          text: '`leaseSeconds` ist eine Zahl (Sekunden). Werte ausserhalb von'
              + ` ${CLAIM_LEASE_MIN}..${CLAIM_LEASE_MAX} werden geklemmt.`,
        };
      }
      /* Ohne deutbaren Zeitpunkt lässt sich weder das Ende der neuen Lease
         berechnen noch entscheiden, ob eine vorhandene abgelaufen ist. */
      if (tsSchluessel(op.ts) === null) {
        return {
          grund: 'claim-ts-ungueltig', cardId: p.cardId,
          text: `Der Zeitstempel ${JSON.stringify(op.ts)} ist nicht deutbar — ohne ihn gibt es`
              + ' kein Lease-Ende.',
        };
      }
    }
    if (op.type === 'card.release' && p.erzwingen != null && typeof p.erzwingen !== 'boolean') {
      return {
        grund: 'erzwingen-ungueltig', cardId: p.cardId,
        text: '`erzwingen` ist true oder false.',
      };
    }
    /* Karte #618 (F-1257): der erwartete Halter ist ein Objekt aus
       Zeichenketten (oder null) für by/session/since — oder fehlt ganz. */
    if (op.type === 'card.release' && p.expect != null) {
      const e = p.expect;
      const ok = typeof e === 'object' && !Array.isArray(e)
        && ['by', 'session', 'since'].every(k => e[k] == null || typeof e[k] === 'string');
      if (!ok) {
        return {
          grund: 'expect-ungueltig', cardId: p.cardId,
          text: '`expect` ist ein Objekt { by, session, since } — der Halter, den der Schreiber gesehen hat.',
        };
      }
    }
    return null;
  }

  if (op && op.type === 'board.awaiting') {
    if (!awaitingPersonGueltig(p.person)) {
      return {
        grund: 'awaiting-person-ungueltig',
        text: `\`person\` ist eine nicht leere Zeichenkette mit höchstens ${AWAITING_PERSON_MAX} Zeichen,`
            + ' kein Prototyp-Schlüssel (__proto__, constructor, prototype).',
      };
    }
    if (!('value' in p)) {
      return {
        grund: 'awaiting-wert-fehlt',
        text: 'board.awaiting ohne `value` — zum Löschen `value: null` angeben, sonst löschte'
            + ' eine vergessene Angabe den Eintrag still.',
      };
    }
    /* F-1336: `person`/`session` im Eintrag stempelt der Fold — ein Schreiber,
       der sie selbst mitgibt, könnte einen Eintrag einer anderen Person unter
       seinem Schlüssel ablegen. */
    if (p.value !== null && (!istAwaitingEintrag(p.value)
        || 'person' in p.value || 'session' in p.value)) {
      return {
        grund: 'awaiting-wert-ungueltig',
        text: '`value` ist `null` (löschen) oder ein Objekt { question, since, askedBy } —'
            + ' question und since nicht leer, askedBy eine Zeichenkette; `person`/`session`'
            + ' gehören in die Op, nicht in den Eintrag.',
      };
    }
    if ('session' in p && p.session !== null && p.session !== undefined && !awaitingSitzungGueltig(p.session)) {
      return {
        grund: 'awaiting-sitzung-ungueltig',
        text: '`session` ist eine Sitzungskennung aus [A-Za-z0-9._:@-], 1–200 Zeichen (oder weglassen).',
      };
    }
    return null;
  }

  if (op && (op.type === 'termin.upsert' || op.type === 'termin.delete')) {
    return terminVerwerfung(board, op);
  }

  if (op && op.type === 'board.field' && p.field === 'awaitingUser') {
    return {
      grund: 'board-feld-nicht-erlaubt', feld: p.field,
      text: '`awaitingUser` ist über board.field nicht mehr setzbar (ADR-0130 §7): es ist eine'
          + ' Abbildung je Person, und board.field ersetzte sie ganz — zwei Sitzungen, die'
          + ' gleichzeitig fragen, verlören eine Frage. Stattdessen board.awaiting'
          + ' { person, value } verwenden; ein alter Hook muss per install.sh erneuert werden.',
    };
  }

  if (op && op.type === 'board.field' && !BOARD_FIELD_ALLOWLIST.has(p.field)) {
    return {
      grund: 'board-feld-nicht-erlaubt', feld: p.field,
      text: `\`${p.field}\` ist über board.field nicht setzbar (BOARD_FIELD_ALLOWLIST).`
          + ` Erlaubt sind: ${[...BOARD_FIELD_ALLOWLIST].join(', ')}.`,
    };
  }

  return null;
}

/* Zwei Mengen, nicht eine (F-1049, Karte #514). Bis Sprint 67 stand hier eine
   einzige Konstante `VOM_FOLD_GESETZT`, die an zwei Stellen zwei verschiedene
   Fragen beantwortete — und die dritte Stelle, die echte Bewahrung in
   `applyOp`, las sie gar nicht. Für `kind` (F-1015) gab es damit keine
   richtige Wahl: dort eingetragen, hätte die Neuanlage-Meldung (F-925) ein
   Feld verschwiegen, das der Fold bei Neuanlage NICHT setzt.

   BEI NEUANLAGE GESETZT — fehlt in jeder legitimen Neuanlage-Payload und darf
   deshalb nicht als „unvollständig" gelten (F-925, Karte #508): `displayId`
   vergibt der Projektor (ADR-0067 §1), `columnId` setzt `insertCard`. */
export const VOM_FOLD_BEI_NEUANLAGE_GESETZT = /*#__PURE__*/ Object.freeze(['displayId', 'columnId']);

/* AUF BESTAND BEWAHRT — `card.upsert` auf eine Bestandskarte übernimmt diese
   Felder von der Bestandskarte, wenn die Payload sie nicht trägt (`== null`).
   `applyOp` iteriert über GENAU diese Liste, `tools/aboard-op.mjs` zählt sie
   deshalb nicht als Entkernungs-Verlust — ein Aufschrieb für beide.

   `kind` (F-1015): Der Meta-Karten-Schutz (`regelVerwerfung`, ADR-0036) hängt
   an `card.kind === 'sprint-meta'`. Ging `kind` mit einer Teil-Payload
   verloren, war die Karte danach archivierbar. Bewahrt wird nur ein
   VORHANDENER Wert — eine Karte ohne `kind` bekommt keins; aufgefüllt wird
   nichts, was die Bestandskarte nicht trägt. Der Rückweg für eine
   versehentlich als Meta angelegte Karte bleibt offen: ein ausdrücklicher
   Wert in der Payload gewinnt, und `card.field kind` ist nicht gesperrt
   (Widerlegungsbedingung von F-1015).

   `metaType` gehört dazu (Kritiker Sprint 68): Mit `kind` allein blieb eine
   entkernte Meta-Karte `sprint-meta` OHNE Typ. `migrateSprintMeta` sucht
   `kind` UND `metaType`, legte beim nächsten Browser-Load eine zweite Karte
   desselben Typs an, und die alte war per UI weder lösch- noch
   verschiebbar. Eine Meta-Karte ohne Typ ist kein sinnvoller Zustand.

   ⚠️ Grenze, bewusst hingenommen: Die Round-Trip-Invariante aus ADR-0079
   (`fold(prev, diff(prev, next)) ≡ next`) gilt für das ENTFERNEN eines
   dieser Felder nicht mehr. `src/diff.js` drückt ein entferntes Feld als
   Volltext-`card.upsert` ohne das Feld aus, und hier wird es wieder
   eingesetzt. Kein Code-Pfad im Browser entfernt `kind` oder `metaType`;
   wer es will, setzt einen ausdrücklichen Wert.

   `confidential` (ADR-0140 §10, Karte #778): Der Schalter „vertraulich“
   darf nicht dadurch abgehen, dass eine Payload ihn nicht trägt — ein
   Ganzersatz aus einem veralteten Tab oder von einem Agenten entschärfte
   sonst still. Abschalten geht nur mit einem ausdrücklichen `false`. Anders
   als bei den übrigen Feldern bewahrt `applyOp` hier auch einen Bestand
   `null`, denn `null` gilt als vertraulich (istVertraulich). Für dieses Feld
   gilt die Round-Trip-Grenze oben ausdrücklich: Fehlen oder `null` schalten
   nie ab. */
export const VOM_FOLD_AUF_BESTAND_BEWAHRT = /*#__PURE__*/ Object.freeze(['displayId', 'columnId', 'kind', 'metaType', 'confidential']);

/* Das eine Prädikat „vertraulich“ (ADR-0140 §10, W-2): fehlender Schlüssel
   und echtes `false` heißen „nicht vertraulich“, jeder andere Wert — `true`,
   `null`, `""`, `0`, `"false"` — heißt „vertraulich“. Gegenstück am Server:
   schatten.Vertraulich (server/schatten/schatten.go), dieselben Testfälle. */
export function istVertraulich(obj) {
  return !!obj && typeof obj === 'object' && Object.prototype.hasOwnProperty.call(obj, 'confidential') && obj.confidential !== false;
}

/**
 * Welche Felder der Bestandskarte verlören durch das Ersetzen mit `neu` ihren
 * Wert? (F-926, F-1161)
 *
 * Gezählt wird nur ein Schlüssel, der im Bestand einen Wert trägt. Ein
 * Schlüssel mit `null` (oder `undefined`) trägt keinen Inhalt; fehlt er danach,
 * geht nichts verloren. Bis F-1161 zählte jeder Schlüssel aus
 * `Object.keys(bestand)`. Eine Karte mit `kind: null` löste so beim
 * Volltext-Upsert aus `src/diff.js` eine Entkernungs-Meldung aus, obwohl der
 * Zielstand exakt entstand. Am Server wird aus jeder Meldung ein 422
 * (ADR-0129 §4), der Browser hätte also einen richtigen Batch nicht speichern
 * können. Gemessen an der aBoard-Historie war das 1 von 723 Board-Übergängen
 * (Karte #563).
 *
 * Dieselbe `!= null`-Grenze nutzt die Bewahrung davor (`merged[k] == null &&
 * bestand[k] != null`). Beide müssen gleich zählen, sonst meldet die eine, was
 * die andere absichtlich nicht zurückholt.
 *
 * Einziger Aufschrieb der Regel: `applyOp` und `tools/aboard-op.mjs` rufen
 * beide diese Funktion (knowledge `nebenlaeufigkeit-und-lesefenster.md` §30).
 */
export function verloreneFelder(bestand, neu) {
  if (!bestand || typeof bestand !== 'object') return [];
  const ziel = neu && typeof neu === 'object' ? neu : {};
  return Object.keys(bestand).filter(k => bestand[k] != null && !(k in ziel));
}

/**
 * Welche Felder des Massstabs fehlen dieser neu angelegten Karte?
 * (F-925, Karte #508; Massstab seit F-1046 von aussen)
 *
 * WOZU: Eine handgebaute `card.upsert`-Payload trägt nur die Felder, die im
 * erzeugenden Skript standen. `tools/aboard-op.mjs` füllt seit Karte #434 aus
 * `Model.createCard` auf; diese Stelle erreicht den Weg AM CLI VORBEI — jedes
 * Skript, das `makeOp` direkt importiert.
 *
 * WOHER DER MASSSTAB KOMMT — und warum nicht mehr aus der base (F-1046):
 * Bis Sprint 68 bildete `bestandsFeldmenge` ihn als Schnittmenge der Felder
 * aller Bestandskarten. Nach der ersten Kompaktierung stand eine
 * unvollständige Neuanlage aber SELBST in der base, und der Massstab des
 * nächsten Laufs hatte ihre Lücken übernommen — der Wächter wurde genau für die
 * Felder stumm, die dauerhaft fehlen (Fehlerklasse 88a: ein Massstab, dessen
 * Quelle das Beanstandete aufnehmen kann, ist falsch gewählt, egal wie früh er
 * eingefroren wird). Der Massstab kommt jetzt aus der Karten-Fabrik
 * (`kartenFeldMassstab` in `src/feldmassstab.js`) und wird vom Aufrufer
 * übergeben: `src/ops.js` ist die reine Fold-Schicht und importiert bewusst
 * nichts (ADR-0067 §2, DCE).
 *
 * GEMELDET, NICHT AUFGEFÜLLT: Auffüllen hiesse, dass `fold` ein Feld setzt,
 * das die Op nicht trägt — und damit bräche die Round-Trip-Invariante
 * `fold(prev, diff(prev, next)) ≡ next` (ADR-0079), gemessen in Karte #512 an
 * zehn Tests (Fehlerklasse 87; der verworfene Entwurf `kartenVersion` steht
 * dort). Ablehnen kann ohnehin nur die Schreibseite.
 *
 * @param {string[]|null} massstab  Feldnamen, die eine Neuanlage tragen sollte
 * @param {object} neueKarte        die Karte aus der Payload
 * @returns {string[]} fehlende Felder (leer ohne Massstab)
 */
export function fehlendeMassstabFelder(massstab, neueKarte) {
  if (!Array.isArray(massstab) || massstab.length === 0) return [];
  return massstab.filter(k => !VOM_FOLD_BEI_NEUANLAGE_GESETZT.includes(k) && !(k in neueKarte));
}

/* Ein Kartenfeld per card.field/card.fieldIf setzen. Für `finding` (Karte
   #666) bleibt die Nummer der Karte: `regelVerwerfung` hat eine abweichende
   schon abgelehnt, eine fehlende wird hier ergänzt — sonst löschte ein
   `card.field finding` ohne `nr` sie. */
function setzeKartenFeld(card, feld, wert) {
  const nr = feld === 'finding' ? bestandsFindingNr(card) : undefined;
  card[feld] = clone(wert);
  if (nr !== undefined && card[feld] && typeof card[feld] === 'object') card[feld].nr = nr;
}

/* Karte #772: `effort`, das nach dieser Op Text/Wahrheitswert/... statt Zahl ist.
   Nur die Op wird beurteilt, nie der Bestand: Text-effort steht in fremden
   Boards massenhaft (gemessen 09.10.: >80 Karten in 5 Boards), eine
   Bestandsprüfung füllte das Warnungs-Log dauerhaft. Deshalb nur, wenn die Op
   den Wert GEGENÜBER dem Vorstand ändert. Opt-in über `optionen.effortTyp`
   wie `feldMassstab`: der Server (422 bei jedem Protokolleintrag) fragt es
   nicht ab, sein Verhalten bleibt unverändert.
   Kopien im Browser (Karte duplizieren, Wiederholungsinstanz, Karte aus Vorlage)
   übernehmen `effort` unverändert: Eine Text-Altkarte erzeugt beim Kopieren also
   eine Neuanlage-Meldung. Das bleibt bewusst so — die Op setzt den Wert wirklich
   auf einer neuen Karte, und der Hinweis zeigt den Altwert; dauerhaft stumm
   schaltet ihn `--warnungen-quittieren` (Merkdatei neben dem Log). */
function meldeEffortTyp(protokoll, effortTyp, op, cardId, wert, vorher) {
  if (!effortTyp || !Array.isArray(protokoll)) return;
  if (!effortKeineZahl(wert)) return;
  if (stabil(wert) === stabil(vorher)) return;
  protokoll.push({ art: 'effort-typ', op: op.id, typ: op.type, cardId,
    wert: stabil(wert), typname: Array.isArray(wert) ? 'Array' : typeof wert });
}

function applyOp(board, op, protokoll, feldMassstab, effortTyp) {
  const p = op.payload || {};

  /* F-965 (b), Karte #490: Regelbedingte Verwerfungen an EINER Stelle prüfen —
     und melden. Vorher war der einzige Schutz davor ein Guard in
     `tools/aboard-op.mjs`; wer `makeOp` direkt importiert (die in CLAUDE.md
     dokumentierte Op-Append-Praxis) oder ein fremdes Werkzeug benutzt, schrieb
     eine Op, die beim Falten stillschweigend nichts tat. Gemessen an der
     gesamten git-Historie ist das kein Gedankenspiel: vier `board.field`-Ops
     auf `nextDisplayId` sind so wirkungslos versickert.
     Die Meldung läuft über dasselbe Protokoll-Array wie Klemmung (F-417) und
     Entkernung (F-926); `fold` bleibt rein, das Ergebnis-Board ist mit und ohne
     Protokoll bit-identisch. */
  const verwurf = regelVerwerfung(board, op);
  if (verwurf) {
    if (Array.isArray(protokoll)) {
      protokoll.push({ art: 'wirkungslos', op: op.id, typ: op.type, ...verwurf });
    }
    return;
  }

  switch (op.type) {

    case 'card.upsert': {
      if (!p.card || typeof p.card !== 'object' || typeof p.card.id !== 'string') return;
      const existing = findCard(board, p.card.id);
      if (existing) {
        /* Bestehende Karte: Inhalt ersetzen (LWW), aber Identität, Position
           und Kartenart bewahren (VOM_FOLD_AUF_BESTAND_BEWAHRT). Position
           ändert ausschließlich card.move. */
        const merged = clone(p.card);
        for (const k of VOM_FOLD_AUF_BESTAND_BEWAHRT) {
          if (merged[k] == null && existing.card[k] != null) merged[k] = existing.card[k];
        }
        /* ADR-0140 §10: auch ein Bestand `null` ist vertraulich und bleibt. */
        if (merged.confidential == null && Object.prototype.hasOwnProperty.call(existing.card, 'confidential')) {
          merged.confidential = existing.card.confidential;
        }
        /* displayId zusätzlich NIE überschreiben, auch nicht durch einen
           Payload-Wert (F-284). */
        merged.displayId = existing.card.displayId;
        /* Karte #666: dasselbe für die Finding-Nummer. `regelVerwerfung` hat
           eine abweichende `nr` schon abgelehnt; hier wird eine fehlende
           ergänzt, damit ein Upsert ohne `nr` sie nicht löscht. */
        if (merged.kind === FINDING_KIND && merged.finding && typeof merged.finding === 'object'
            && bestandsFindingNr(existing.card) !== undefined) {
          merged.finding.nr = bestandsFindingNr(existing.card);
        }
        /* ADR-0130 §1: die Übernahme gehört dem Bestand, nie der Payload. */
        for (const k of VOM_FOLD_VERWALTET) {
          if (k in existing.card) merged[k] = clone(existing.card[k]);
          else delete merged[k];
        }
        /* F-417-Nacharbeit (Kritiker Sprint 61): auch hier klemmen. Die erste
           Fassung deckte nur `card.move` und den Neu-Zweig unten — gemessen
           blieb `card.upsert` auf eine BESTEHENDE Karte ungeprüft, und das ist
           kein Randfall: `src/diff.js` emittiert genau diese Op als
           Volltext-Fallback, sobald der feingranulare Weg nicht trägt. */
        merged.columnId = klemmeSpalte(board, merged.columnId, protokoll,
          { op: op.id, typ: op.type, cardId: merged.id });
        /* F-926 (Karte #484): Eine TEIL-Payload entkernt die Bestandskarte —
           `merged` ersetzt den kompletten Inhalt, alles was hier fehlt, ist
           danach weg. Gemessen an einer Karte mit 11 Feldern blieben nach
           einem `{id, title}`-Upsert vier übrig; ohne Fehler, ohne Spur.

           Gemeldet wird, NICHT abgelehnt — dieselbe Begründung wie bei der
           Klemmung darüber und bei F-700: Die Op ist bereits geschrieben; sie
           im Leser zu verwerfen wäre Datenverlust an einem Zustand, den der
           Leser nicht verursacht hat. Der Schreibpfad lehnt sehr wohl ab, aber
           nur dort, wo das ohne Verlust geht (`tools/aboard-op.mjs`, Karte
           #434) — und genau deshalb gibt es diese Stelle: Sie erreicht JEDEN
           Schreiber, auch den, der `makeOp` direkt importiert.

           Gemessen wird gegen die BESTANDSKARTE, nicht gegen `Model.createCard`.
           Die Fabrik-Feldmenge ist in beide Richtungen das falsche Maß: Auf
           unseren 459 Karten (Stand 2026-09-18) fehlen Fabrik-Felder
           massenhaft (`dueDate` auf 199), und 57 Karten tragen ein `checklist`, das die Fabrik nicht
           kennt — ein Guard gegen die Fabrik wäre gleichzeitig Dauer-Blocker
           und blind (Fehlerklasse 68).

           Die Felder aus `VOM_FOLD_AUF_BESTAND_BEWAHRT` stehen nie in der
           Verlustliste: sie wurden oben auf `merged` übernommen, sind also per
           Konstruktion vorhanden. `tools/aboard-op.mjs` liest dieselbe
           Konstante — eine Liste für Bewahrung und Guard (F-1049).

           Zur Lautstärke: Der legitime Volltext-Fallback aus `src/diff.js`
           lässt nur dann ein Feld fallen, wenn die Karte im Browser wirklich
           eines VERLOREN hat (`cardContentOps` → `if (!(k in nc)) return null`);
           alle anderen Fallback-Gründe schicken die vollständige Karte.
           Gemessen über alle 2074 Op-Dateien, die dieses Repo je getrackt hat
           (Stand 2026-09-18, `git rev-list --all --objects`, gefiltert auf
           `aboard-sync.inbox/*.json`): 225 `card.upsert`, davon 1 ohne
           brauchbare `card.id`. Die tragende Zahl ist die des Wächters selbst:
           ein Fold-Replay der ganzen Historie mit Protokoll ergibt **0**
           Entkernungen, zweimal unabhängig gemessen. Er feuert also nicht im
           Alltag (F-812).
           ⚠️ Zwei Messfallen, beide im Prüflauf aufgeschlagen:
           (1) `git log --diff-filter=A` zählt hier 2059 statt 2074 — es macht
               still Rename-Erkennung, und Op-Dateien sehen einander ähnlich
               genug. `--no-renames` liefert 2074.
           (2) Die Aufschlüsselung „216 Neuanlagen + 8 Volltext" steht hier
               bewusst NICHT mehr: Sie hängt an der Replay-Methode (mit Seed aus
               der ersten getrackten base 216/8, ohne Seed 217/7) und ging in der
               ersten Fassung nicht auf (224 statt 225). Eine Aufschlüsselung,
               deren Summe nicht stimmt, lenkt jeden Prüfer auf die Gesamtzahl —
               genau das ist zweimal passiert (Fehlerklassen 81/81a). */
        if (Array.isArray(protokoll)) {
          const verloren = verloreneFelder(existing.card, merged);
          if (verloren.length) {
            protokoll.push({ art: 'entkernung', op: op.id, typ: op.type,
                             cardId: merged.id, felder: verloren });
          }
        }
        meldeEffortTyp(protokoll, effortTyp, op, merged.id, merged.effort, existing.card.effort);
        existing.sprint.cards[existing.idx] = merged;
        return;
      }
      /* Neue Karte: Projektor vergibt displayId — der Schreiber-Wert wird
         IGNORIERT (ADR-0067 §1, eliminiert F-284-Klasse strukturell). */
      const card = clone(p.card);
      /* ADR-0130 §1: eine neue Karte ist nicht übernommen — das tut erst card.claim. */
      for (const k of VOM_FOLD_VERWALTET) delete card[k];
      /* Gemessen wird die Karte, wie der Schreiber sie schickte — also VOR der
         displayId-Vergabe; gemeldet wird erst nach der Swimlane-Prüfung (unten). */
      const fehlen = Array.isArray(protokoll) ? fehlendeMassstabFelder(feldMassstab, card) : [];
      card.displayId = nextDisplayId(board);
      const targetSprint = zielSwimlaneNeueKarte(board, p);
      if (!targetSprint) {
        /* F-3094: nicht mehr wortlos. Die displayId ist oben schon vergeben
           (F-1152) — das bleibt so, sonst verschöben sich beim erneuten Falten
           einer Inbox mit solcher Op die Nummern aller späteren Karten. */
        if (Array.isArray(protokoll)) {
          protokoll.push({ art: 'ohne-ziel', op: op.id, typ: op.type, cardId: card.id,
            text: `neue Karte ohne Ziel: Swimlane ${JSON.stringify(p.sprintId ?? null)} gibt es nicht,`
                + ' und das Board hat keinen Backlog (weder `__backlog__` noch eine Swimlane mit'
                + " `kind: 'backlog'`). Die Karte wurde NICHT angelegt — Op mit gültiger `sprintId`"
                + ' neu schreiben.' });
        }
        return;
      }
      /* F-925 (Karte #508): Meldung, bevor die Karte eingefügt wird. Der
         Massstab hängt seit F-1046 nicht mehr am Bestand, die Reihenfolge ist
         damit nicht mehr tragend. Seit dem Kritiker zu #772 (E-3) steht sie
         HINTER der Swimlane-Prüfung: Eine verworfene Op hat keine Karte
         angelegt und darf weder „unvollständig“ noch (effort-typ) „die Op
         wirkt trotzdem“ melden. */
      if (fehlen.length) {
        protokoll.push({ art: 'unvollstaendig', op: op.id, typ: op.type,
                         cardId: card.id, felder: fehlen });
      }
      meldeEffortTyp(protokoll, effortTyp, op, card.id, card.effort, undefined);
      if (!Array.isArray(targetSprint.cards)) targetSprint.cards = [];
      /* Karte #666 (ADR-0133 §1): Finding-Nummer vergibt der Fold, ein
         Schreiber-Wert wird ignoriert (wie displayId). Erst NACH der
         Swimlane-Prüfung, damit eine verworfene Karte keine Nummer verbraucht
         (bei displayId ist das F-1152). Den Wortschatz hat regelVerwerfung
         geprüft, `card.finding` ist hier also ein Objekt. */
      if (card.kind === FINDING_KIND) card.finding.nr = nextFindingNr(board);
      const colId = klemmeSpalte(board, p.columnId || card.columnId || 'col-backlog',
        protokoll, { op: op.id, typ: op.type, cardId: card.id });   /* F-417 */
      insertCard(targetSprint, card, colId, p.targetIndex ?? null, p.anchor);
      return;
    }

    case 'card.move': {
      const loc = findCard(board, p.cardId);
      if (!loc) return;
      const toSprint = (board.sprints || []).find(s => s.id === p.toSprintId);
      if (!toSprint) return;
      const [card] = loc.sprint.cards.splice(loc.idx, 1);
      if (!Array.isArray(toSprint.cards)) toSprint.cards = [];
      insertCard(toSprint, card,
        klemmeSpalte(board, p.toColId || card.columnId, protokoll,
          { op: op.id, typ: op.type, cardId: card.id }),
        p.targetIndex ?? null, p.anchor);   /* F-417 */
      return;
    }

    case 'card.delete': {
      const loc = findCard(board, p.cardId);
      if (!loc) return;
      loc.sprint.cards.splice(loc.idx, 1);
      return;
    }

    case 'card.fieldIf': {
      /* ADR-0130 §2 (Karte #604): nur anwenden, wenn der aktuelle Wert dem
         erwarteten gleicht (Tiefenvergleich, Schlüsselreihenfolge egal).
         Sonst: nichts ändern und `feld-geaendert` mit dem aktuellen Wert
         melden — der Server macht daraus 422, der Browser zeigt beide
         Fassungen. Fehlende Karte: no-op wie card.field (Ziel prüft der
         Server gesondert, ziel-fehlt). Ein FEHLENDES Feld gilt als `null`
         (Sprint-77-Kritiker): Über JSON lässt sich `undefined` nicht erwarten,
         eine Altkarte ohne das Feld hätte sonst immer `feld-geaendert`
         gemeldet — und ohne `aktuell`, weil undefined beim Serialisieren
         wegfällt. */
      const loc = findCard(board, p.cardId);
      if (!loc) return;
      const aktuell = loc.card[p.field] === undefined ? null : loc.card[p.field];
      if (stabil(aktuell) !== stabil(p.expect)) {
        if (Array.isArray(protokoll)) {
          protokoll.push({ art: 'feld-geaendert', op: op.id, typ: op.type,
            cardId: p.cardId, feld: p.field, aktuell: clone(aktuell) });
        }
        return;
      }
      if (p.field === 'columnId') {
        loc.card.columnId = klemmeSpalte(board, clone(p.value), protokoll,
          { op: op.id, typ: op.type, cardId: p.cardId });
        return;
      }
      if (p.field === 'effort') meldeEffortTyp(protokoll, effortTyp, op, p.cardId, p.value, loc.card.effort);
      setzeKartenFeld(loc.card, p.field, p.value);
      return;
    }

    case 'card.moveIf': {
      /* ADR-0141 §5.3 (Karte #827): verschieben, wenn die Karte noch in Lane
         und Spalte steht, die außen gesehen wurden. Sonst `feld-geaendert`
         mit dem aktuellen Ort (feld `ort`). Fehlende Karte oder Ziel-Lane:
         no-op wie card.move (der Server meldet ziel-fehlt). */
      const loc = findCard(board, p.cardId);
      if (!loc) return;
      const aktuell = { sprintId: loc.sprint.id, columnId: loc.card.columnId === undefined ? null : loc.card.columnId };
      /* Steht sie schon am Ziel (Wiederholung nach Verbindungsabbruch), gilt
         das nicht als Konflikt — nur die Position wird gesetzt (Verify V-5). */
      const amZiel = aktuell.sprintId === p.toSprintId && aktuell.columnId === p.toColId;
      if (!amZiel && (aktuell.sprintId !== p.expectSprintId || aktuell.columnId !== p.expectColId)) {
        if (Array.isArray(protokoll)) {
          protokoll.push({ art: 'feld-geaendert', op: op.id, typ: op.type, cardId: p.cardId, feld: 'ort', aktuell });
        }
        return;
      }
      const toSprint = (board.sprints || []).find(s => s.id === p.toSprintId);
      if (!toSprint) return;
      const [card] = loc.sprint.cards.splice(loc.idx, 1);
      if (!Array.isArray(toSprint.cards)) toSprint.cards = [];
      insertCard(toSprint, card,
        klemmeSpalte(board, p.toColId, protokoll, { op: op.id, typ: op.type, cardId: card.id }),
        p.targetIndex ?? null, p.anchor);
      return;
    }

    case 'card.checkItemIf': {
      /* ADR-0141 „Umsetzung Schritt 2b“ Punkt 1 (Karte #827): genau einen
         Checklisten-Punkt der Beschreibung abhaken oder zurücksetzen — auch
         wenn die Beschreibung inzwischen anderswo geändert wurde. Gefunden
         über Text und Vorkommen (checklistePunkte). Fehlt der Punkt oder
         steht sein Häkchen nicht auf `expect`: `feld-geaendert` (feld
         `checkpunkt`, aktuell = Häkchen oder null). */
      const loc = findCard(board, p.cardId);
      if (!loc) return;
      const beschreibung = typeof loc.card.description === 'string' ? loc.card.description : '';
      const punkt = checklistePunkte(beschreibung).find(x => x.text === p.text && x.vorkommen === p.vorkommen);
      if (punkt && punkt.erledigt === p.erledigt) return;   /* steht schon so: angewendet (Verify V-5) */
      if (!punkt || punkt.erledigt !== p.expect) {
        if (Array.isArray(protokoll)) {
          protokoll.push({ art: 'feld-geaendert', op: op.id, typ: op.type, cardId: p.cardId, feld: 'checkpunkt',
            text: p.text, vorkommen: p.vorkommen, aktuell: punkt ? punkt.erledigt : null });
        }
        return;
      }
      if (punkt.erledigt === p.erledigt) return;
      loc.card.description = setzeChecklistePunkt(beschreibung, punkt.zeile, p.erledigt);
      return;
    }

    case 'card.listAdd':
    case 'card.listRemove': {
      /* ADR-0130 §3 (Karte #605): vertauschbar und wiederholbar. Hinzufügen
         eines vorhandenen / Entfernen eines fehlenden Elements ist wirkungslos,
         aber kein Fehler (Wiederholung nach Verbindungsabbruch). Gleichheit
         über die kanonische Form (stabil). Hinzugefügt wird am Ende. */
      const loc = findCard(board, p.cardId);
      if (!loc) return;
      const liste = Array.isArray(loc.card[p.field]) ? loc.card[p.field] : [];
      const form = stabil(p.item);
      const drin = liste.some(x => stabil(x) === form);
      if (op.type === 'card.listAdd') {
        if (!drin) loc.card[p.field] = [...liste, clone(p.item)];
        else if (!Array.isArray(loc.card[p.field])) loc.card[p.field] = liste;
      } else if (drin) {
        loc.card[p.field] = liste.filter(x => stabil(x) !== form);
      }
      return;
    }

    case 'card.claim': {
      /* ADR-0130 §1 (Karte #606): Karte übernehmen. „Jetzt“ ist die ts der Op
         (am Server gestempelt), nie Date.now() — der Fold bleibt rein.
         - frei / eigen → Lease setzen bzw. erneuern (`since` bleibt beim
           eigenen Halter);
         - fremd-aktiv → nichts ändern, `beansprucht` melden (Server: 422, und
           weil der Batch atomar ist, wirkt auch ein mitgeschicktes card.move
           nicht);
         - fremd-abgelaufen → übernehmen (auch in col-erledigt erst nach
           Ablauf, K-2), den vorigen Halter in
           `vorher` festhalten. Keine Protokoll-Meldung: am Server wäre jede
           Meldung ein 422, und die Übernahme einer verwaisten Karte ist gewollt.
         Fehlende Karte: no-op wie card.field (Ziel prüft der Server gesondert). */
      const loc = findCard(board, p.cardId);
      if (!loc) return;
      const jetzt = opZeitMs(op);
      const alt = loc.card.claim;
      const zustand = claimZustandFuerUebernahme(loc.card, jetzt, op.actor, p.session);
      if (zustand === 'fremd-aktiv') {
        if (Array.isArray(protokoll)) {
          protokoll.push({ art: 'beansprucht', op: op.id, typ: op.type, cardId: p.cardId,
            halter: alt.by, session: alt.session ?? null, until: alt.until ?? null });
        }
        return;
      }
      const neu = {
        by: op.actor || '',
        onBehalfOf: typeof p.onBehalfOf === 'string' && p.onBehalfOf ? p.onBehalfOf : null,
        session: p.session || null,
        since: zustand === 'eigen' && alt.since ? alt.since : op.ts,
        until: new Date(jetzt + leaseSekunden(p.leaseSeconds) * 1000).toISOString(),
      };
      if (zustand === 'eigen') {
        if (neu.onBehalfOf == null && alt.onBehalfOf != null) neu.onBehalfOf = clone(alt.onBehalfOf);
        if (alt.vorher) neu.vorher = clone(alt.vorher);
      } else if (zustand !== 'frei') {
        neu.vorher = { by: alt.by ?? '', session: alt.session ?? null, until: alt.until ?? null };
      }
      loc.card.claim = neu;
      return;
    }

    case 'card.release': {
      /* ADR-0130 §1: Übernahme aufheben. Der Halter darf es; mit
         `erzwingen: true` jeder (am Server nur Menschen, der Grund steht dann
         als Kommentar im selben Batch). Wer nicht Halter ist, bekommt
         `nicht-halter` — auch bei abgelaufener Lease, denn dort gibt es nichts
         aufzuheben, und eine stille Quittung wäre ein No-op mit 200 (F-1152).
         Keine Übernahme: no-op, damit eine Wiederholung nach verlorener
         Antwort nicht scheitert.
         Mit `expect` (Karte #618, F-1257) zuerst die Bedingung: Hält die
         Karte nicht mehr genau diesen Halter — ein anderer, eine neue
         Übernahme, oder gar keiner —, wirkt die Op nicht und meldet
         `halter-geaendert` mit dem aktuellen Halter (Server: 422, der
         Grund-Kommentar im selben Batch wirkt dann auch nicht). Der Browser
         schickt die Bedingung immer; dieselbe Op noch einmal quittiert der
         Endpunkt (#607), ein neuer Klick meldet halter-geaendert mit halter
         null — es gibt nichts mehr aufzuheben. */
      const loc = findCard(board, p.cardId);
      if (!loc) return;
      if (p.expect != null && !halterWieErwartet(loc.card.claim, p.expect)) {
        if (Array.isArray(protokoll)) {
          const c = loc.card.claim;
          protokoll.push({ art: 'halter-geaendert', op: op.id, typ: op.type, cardId: p.cardId,
            halter: c ? c.by : null, session: c ? c.session ?? null : null,
            since: c ? c.since ?? null : null, until: c ? c.until ?? null : null });
        }
        return;
      }
      if (!loc.card.claim) return;
      const alt = loc.card.claim;
      if (p.erzwingen !== true
          && claimZustand(loc.card, opZeitMs(op), op.actor, p.session) !== 'eigen') {
        if (Array.isArray(protokoll)) {
          protokoll.push({ art: 'nicht-halter', op: op.id, typ: op.type, cardId: p.cardId,
            halter: alt.by, session: alt.session ?? null, until: alt.until ?? null });
        }
        return;
      }
      delete loc.card.claim;
      return;
    }

    case 'card.field': {
      /* CARD_FIELD_BLOCKLIST und die Meta-Archivierung stehen nicht mehr hier,
         sondern in `regelVerwerfung` oben — EIN Aufschrieb, den auch das CLI
         und jeder Bibliotheks-Aufrufer befragen kann, und der beim Falten eine
         Meldung erzeugt statt eines stillen No-ops (F-965 b, Karte #490). */
      const loc = findCard(board, p.cardId);
      if (!loc) return;
      /* F-417-Nacharbeit (Kritiker Sprint 61): `columnId` per `card.field`
         ebenfalls klemmen. Das ist der WAHRSCHEINLICHSTE Weg in eine
         Phantom-Spalte, nicht der unwahrscheinlichste: Wer eine Op von Hand
         schreibt und sich dabei an der Projektion orientiert, findet dort das
         Feld `columnId` — nicht `toColId`. Genau diese Verwechslung
         beschreibt [[F-451]], und der Apply-Core deckte sie nicht ab.
         Feldbezogen statt in der BLOCKLIST, weil eine Blockliste den Wert
         still verwerfen würde; klemmen setzt ihn auf eine existierende
         Spalte, und die Karte bleibt sichtbar. */
      if (p.field === 'columnId') {
        loc.card.columnId = klemmeSpalte(board, clone(p.value), protokoll,
          { op: op.id, typ: op.type, cardId: p.cardId });
        return;
      }
      if (p.field === 'effort') meldeEffortTyp(protokoll, effortTyp, op, p.cardId, p.value, loc.card.effort);
      setzeKartenFeld(loc.card, p.field, p.value);
      return;
    }

    case 'comment.add': {
      const loc = findCard(board, p.cardId);
      if (!loc || !p.comment || typeof p.comment !== 'object') return;
      if (!Array.isArray(loc.card.comments)) loc.card.comments = [];
      /* Idempotent: gleiche comment.id nicht doppelt anhängen */
      if (p.comment.id != null && loc.card.comments.some(c => c.id === p.comment.id)) return;
      loc.card.comments.push(clone(p.comment));
      return;
    }

    case 'comment.delete': {
      /* #299/ADR-0095 Etappe 4: LLM-Antwort verwerfen. payload: { cardId, commentId }.
         Idempotent — fehlende Karte oder fehlender Kommentar = no-op (Konvergenz). */
      const loc = findCard(board, p.cardId);
      if (!loc || typeof p.commentId !== 'string') return;
      if (!Array.isArray(loc.card.comments)) return;
      loc.card.comments = loc.card.comments.filter(c => c && c.id !== p.commentId);
      /* Karte #678 (ADR-0135): Anhänge eines Kommentars fallen mit ihm. Sie
         stehen als gewöhnliche Karten-Anhänge mit `commentId` in
         `card.attachments`; ohne diesen Schritt blieben sie als Einträge
         stehen, deren Kommentar es nicht mehr gibt. Die Dateien selbst räumt
         der Server nach dem Fold weg (server/api/kommentar_anhaenge.go).
         Nur anfassen, wenn wirklich ein Eintrag fällt — sonst bliebe das
         Ergebnis für Karten ohne Kommentar-Anhänge nicht byte-gleich. */
      if (Array.isArray(loc.card.attachments)
          && loc.card.attachments.some(a => a && a.commentId === p.commentId)) {
        const weg = new Set(loc.card.attachments
          .filter(a => a && a.commentId === p.commentId).map(a => a.id));
        loc.card.attachments = loc.card.attachments.filter(a => !(a && a.commentId === p.commentId));
        if (loc.card.coverAttachmentId != null && weg.has(loc.card.coverAttachmentId)) {
          loc.card.coverAttachmentId = null;
        }
      }
      return;
    }

    case 'comment.edit': {
      /* ADR-0130 §4 (Karte #601): genau EINEN Kommentar ändern — `text` und,
         falls mitgesendet, `edited`. Alle übrigen Felder des Kommentars
         (author, created, id) bleiben, die übrigen Kommentare der Karte
         ebenso. Das ist der Unterschied zum Voll-card.upsert, der einen
         gleichzeitig angehängten fremden Kommentar verlor.
         Fehlt Karte oder Kommentar: no-op (Konvergenz, wie comment.delete).
         Der Server meldet das eigens als ziel-fehlt (server/fold/fold.go). */
      const loc = findCard(board, p.cardId);
      if (!loc || typeof p.commentId !== 'string' || typeof p.text !== 'string') return;
      if (!Array.isArray(loc.card.comments)) return;
      const k = loc.card.comments.find(c => c && c.id === p.commentId);
      if (!k) return;
      /* Karte #686 (ADR-0134 §3.6): Die Antwort eines externen Modells
         (kind „extern“) ist unveränderlich — gelöscht werden darf sie,
         bearbeitet nicht. Verbindlich prüft der Server am Ergebnis
         (kommentar_loeschrecht.go); hier bleibt der Fold überall gleich. */
      if (k.kind === 'extern') return;
      k.text = p.text;
      if (p.edited !== undefined) k.edited = clone(p.edited);
      return;
    }

    case 'comment.editIf': {
      /* ADR-0141 §5.3 (Karte #827): wie comment.edit, aber nur, wenn der Text
         noch der ist, den der Schreiber gesehen hat (`expect`). Sonst
         `feld-geaendert` (feld `kommentar`, aktuell = Text). Fehlt Karte oder
         Kommentar: no-op (der Server meldet ziel-fehlt). Welche Kommentare der
         Rückweg bearbeiten darf (nur Autor „extern: …“), entscheidet der
         Server; hier gilt nur die Regel des Folds für kind „extern“. */
      const loc = findCard(board, p.cardId);
      if (!loc || !Array.isArray(loc.card.comments)) return;
      const k = loc.card.comments.find(c => c && c.id === p.commentId);
      if (!k) return;
      /* Eine Modellantwort (kind „extern“) bleibt unveränderlich — gemeldet,
         damit der Rückweg es nicht als „angewendet“ quittiert (Kritik A-4). */
      if (k.kind !== 'extern' && k.text === p.text) return;   /* steht schon so: angewendet (Verify V-5) */
      if (k.kind === 'extern' || k.text !== p.expect) {
        if (Array.isArray(protokoll)) {
          protokoll.push({ art: 'feld-geaendert', op: op.id, typ: op.type, cardId: p.cardId, feld: 'kommentar',
            commentId: p.commentId, aktuell: typeof k.text === 'string' ? k.text : null });
        }
        return;
      }
      k.text = p.text;
      if (p.edited !== undefined) k.edited = clone(p.edited);
      return;
    }

    case 'swimlane.upsert': {
      const sp = p.sprint;
      if (!sp || typeof sp !== 'object' || typeof sp.id !== 'string') return;
      if (!Array.isArray(board.sprints)) board.sprints = [];
      const found = board.sprints.find(s => s.id === sp.id);
      if (found) {
        /* Nur Meta-Felder mergen — Karten NIE clobbern (kommen via card.*) */
        if (sp.title !== undefined) found.title = sp.title;
        if (sp.kind  !== undefined) found.kind  = sp.kind;
        /* ADR-0117: `archived` ist das dritte Meta-Feld. Bewusst `!== undefined`
           und nicht truthy-Pruefung — sonst liesse sich ein archivierter Sprint
           nie wieder zurueckholen (false ginge verloren). Alt-Ops ohne das Feld
           lassen es unangetastet, deshalb ist der Zusatz rueckwaertskompatibel. */
        if (sp.archived !== undefined) found.archived = !!sp.archived;
        return;
      }
      /* ADR-0117: `archived` nur uebernehmen, wenn die Op es wirklich fuehrt —
         eine neu gefaltete Swimlane darf kein Feld bekommen, das der Quell-
         Zustand nicht hatte (Round-Trip-Invariante). Gleiche Konvention wie
         bei Karten: das Feld setzt die Migration, nicht die Fabrik. */
      const fresh = { id: sp.id, title: sp.title || '', kind: sp.kind || 'sprint', cards: [] };
      if (sp.archived !== undefined) fresh.archived = !!sp.archived;
      const backlogIdx = board.sprints.findIndex(s => s.id === '__backlog__');
      if (backlogIdx >= 0) board.sprints.splice(backlogIdx, 0, fresh);
      else board.sprints.push(fresh);
      return;
    }

    case 'swimlane.move': {
      if (!Array.isArray(board.sprints)) return;
      const idx = board.sprints.findIndex(s => s.id === p.sprintId);
      if (idx === -1) return;
      const [sp] = board.sprints.splice(idx, 1);
      const ti = Number.isInteger(p.targetIndex) ? Math.max(0, Math.min(p.targetIndex, board.sprints.length)) : board.sprints.length;
      board.sprints.splice(ti, 0, sp);
      return;
    }

    case 'swimlane.delete': {
      if (p.sprintId === '__backlog__') return; /* Backlog ist unlöschbar */
      if (!Array.isArray(board.sprints)) return;
      const sprint = board.sprints.find(s => s.id === p.sprintId);
      if (!sprint) return;
      /* Parität zu main.js sprint:delete: Nicht-Meta-Karten in den Backlog
         retten, Meta-Karten mitlöschen. Idempotent — beim Diff-Pfad ist der
         Sprint bereits leer (Karten schon via card.move verschoben). */
      const backlog = backlogSprint(board);
      /* Kritik 20.11. K-3: Ist die zu löschende Swimlane selbst der Backlog
         (kind 'backlog' ohne `__backlog__`), gibt es kein Rettungsziel —
         wie `__backlog__` bleibt sie stehen, statt ihre Karten mitzunehmen. */
      if (backlog === sprint) return;
      if (backlog) {
        const normal = (sprint.cards || []).filter(c => c.kind !== 'sprint-meta');
        if (!Array.isArray(backlog.cards)) backlog.cards = [];
        backlog.cards.push(...normal);
      }
      board.sprints = board.sprints.filter(s => s.id !== p.sprintId);
      return;
    }

    case 'board.field': {
      /* Die Allowlist-Prüfung steht in `regelVerwerfung` oben (F-965 b). */
      const alt = board[p.field];
      board[p.field] = clone(p.value);
      if (p.field === 'columnDefs') spaltenMarkenBewahren(alt, board.columnDefs);
      return;
    }

    case 'board.awaiting': {
      /* ADR-0130 §7 (Karte #609): genau EIN Schlüssel der Abbildung. Form und
         Person prüft `regelVerwerfung`. Eine alte Einzelform im Bestand wird
         dabei zur Abbildung (awaitingAlsAbbildung), statt überschrieben zu
         werden. Rein: keine Uhr — `since` bringt der Schreiber mit. */
      const abbildung = awaitingAlsAbbildung(board.awaitingUser, board);
      /* F-1336 (Karte #645): mit `session` ein Eintrag je Sitzung, unter
         `<Person> · <Sitzung>` und mit `person`/`session` im Eintrag; Löschen
         trifft dann nur diese Sitzung. Ohne `session` wie bisher. */
      const mitSitzung = typeof p.session === 'string';
      const k = mitSitzung ? awaitingSitzungsSchluessel(p.person, p.session) : p.person;
      if (p.value === null) delete abbildung[k];
      else abbildung[k] = mitSitzung ? { ...clone(p.value), person: p.person, session: p.session } : clone(p.value);
      if (mitSitzung && p.value !== null) kappeAwaitingSitzungen(abbildung, p.person, k);
      board.awaitingUser = awaitingOderNull(abbildung);
      return;
    }

    case 'termin.upsert': {
      /* ADR-0138 §4 (Karte #743): ganzer Termin, Last-Write-Wins je TERMIN
         (nicht je Feld): Ändern zwei Personen gleichzeitig verschiedene
         Felder desselben Termins, gewinnt der letzte Upsert ganz (Kritiker
         07.10., K-8 — bei Karten zerlegt der Diff in card.field, bei Terminen
         nicht; für kurze Termin-Einträge bewusst hingenommen). Die Form hat
         terminVerwerfung geprüft.
         Vorhanden → an seiner Stelle ersetzen, Herkunft vom Bestand; neu →
         hinten anhängen. Die Liste entsteht erst mit dem ersten Termin. */
      const t = clone(p.termin);
      if (!Array.isArray(board.termine)) board.termine = [];
      const idx = board.termine.findIndex(x => x && x.id === t.id);
      if (idx === -1) {
        for (const k of TERMIN_VERWALTET) delete t[k];
        board.termine.push(t);
        return;
      }
      const alt = board.termine[idx];
      for (const k of [...TERMIN_HERKUNFT, ...TERMIN_VERWALTET]) {
        if (k in alt) t[k] = clone(alt[k]);
        else delete t[k];
      }
      /* ADR-0140 §10 (Karte #778): Schalter „vertraulich“ — fehlt er in der
         Payload (oder ist `null`), bleibt der Bestand; nur ein ausdrücklicher
         Wert ändert ihn. Bewusst NICHT über TERMIN_HERKUNFT: dort zählt der
         Payload-Wert nie, und abschalten könnte dann niemand mehr. Gespiegelt
         in setzeTermin (src/calendar/termine.js). */
      if (t.confidential == null && Object.prototype.hasOwnProperty.call(alt, 'confidential')) {
        t.confidential = clone(alt.confidential);
      }
      board.termine[idx] = t;
      return;
    }

    case 'termin.fieldIf': {
      /* ADR-0141 §5.3 (Karte #827): ein Feld eines Termins setzen, wenn es
         noch den erwarteten Wert hat (Tiefenvergleich wie card.fieldIf, ein
         fehlendes Feld gilt als null). `value: null` leert das Feld. Feld und
         Ergebnis-Form hat regelVerwerfung geprüft. Fehlt der Termin: no-op
         (der Server meldet ziel-fehlt). */
      if (!Array.isArray(board.termine)) return;
      const t = board.termine.find(x => x && x.id === p.terminId);
      if (!t) return;
      const aktuell = t[p.field] === undefined ? null : t[p.field];
      if (stabil(aktuell) === stabil(p.value == null ? null : p.value)) return;   /* steht schon so (Verify V-5) */
      if (stabil(aktuell) !== stabil(p.expect === undefined ? null : p.expect)) {
        if (Array.isArray(protokoll)) {
          protokoll.push({ art: 'feld-geaendert', op: op.id, typ: op.type, terminId: p.terminId,
            feld: p.field, aktuell: clone(aktuell) });
        }
        return;
      }
      if (p.value == null) delete t[p.field];
      else t[p.field] = clone(p.value);
      return;
    }

    case 'termin.delete': {
      /* Fehlt der Termin: no-op (Wiederholung nach Verbindungsabbruch). Nur
         anfassen, wenn wirklich einer fällt — das Ergebnis bleibt sonst
         byte-gleich (auch kein leeres `termine` auf Boards ohne Termine). */
      if (!Array.isArray(board.termine) || !board.termine.some(x => x && x.id === p.terminId)) return;
      board.termine = board.termine.filter(x => !(x && x.id === p.terminId));
      return;
    }

    case 'maintenance.compact':
      /* ADR-0071 Achse 2: bewusst board-NEUTRAL — diese Op verändert die
         Projektion NICHT. Sie ist ein append-only Signal „bitte
         kompaktieren", das ausschließlich der headless-Projektor
         (tools/aboard-projektor.mjs) außerhalb des fold-Pfades auswertet.
         Explizit hier (statt via default), damit die Board-Neutralität
         Vertrag + testbar ist und der fold-Determinismus gewahrt bleibt
         (gleiche Ops → bit-identische Projektion, mit oder ohne Marker). */
      return;

    case 'maintenance.diagnostic':
      /* ADR-0106 (#331): Diagnose-Marker — bewusst board-NEUTRAL.
         Wird vom Browser-Mass-Delete-Guard (sync.js) vor einem bestätigten
         Groß-Lösch-Batch vorangestellt, damit der Projektor den Kontext
         nachvollziehen kann. Keine Board-Mutation — fold-Determinismus bleibt. */
      return;

    default:
      /* Unbekannter Typ — defensiv ignorieren (validateOp filtert das
         eigentlich schon vorher als Poison heraus). */
      return;
  }
}

/**
 * Deterministischer Fold: projection = fold(snapshot, ops).
 *
 * REINE Funktion — mutiert weder `snapshot` noch `ops`. Gegeben derselbe
 * Basis-Snapshot und dieselbe Op-Menge ist die Ausgabe bit-identisch,
 * unabhängig von Eingabe-Reihenfolge der Ops (ts+id-Totalordnung) und
 * unabhängig davon, WER faltet (Browser oder headless-CLI). Genau diese
 * Eigenschaft macht den Projektor-Race gutartig (ADR-0067 §2).
 *
 * Idempotent: doppelte Ops (gleiche op.id) werden genau einmal angewandt.
 *
 * @param {{version:string, board:object}} snapshot - Basis-Snapshot
 * @param {Array<object>} ops - rohe Ops (unsortiert erlaubt; Poison vorher via validateOp filtern)
 * @param {Array<object>} [protokoll] - optional: Sammelliste stiller Vorgänge.
 *   Jeder Eintrag trägt ein `art`-Feld: `'klemmung'` (F-417 — Zielspalte auf
 *   eine existierende geklemmt, Felder `von`/`nach`) oder `'entkernung'`
 *   (F-926 — eine `card.upsert` hat auf einer Bestandskarte Felder fallen
 *   lassen, Feld `felder`). Wird NUR befüllt, nie gelesen; das Ergebnis-Board
 *   ist mit und ohne bit-identisch. Der Aufrufer entscheidet, ob er die
 *   stillen Vorgänge sehen will — `fold` bleibt rein.
 * @param {{feldMassstab?: string[], effortTyp?: boolean}} [optionen] - `feldMassstab`: Felder, die
 *   eine per Op neu angelegte Karte tragen sollte (F-925/F-1046). Ohne ihn
 *   entfällt die Meldung `'unvollstaendig'`; das Ergebnis-Board berührt er nie.
 *   `effortTyp: true` schaltet die Meldung `'effort-typ'` ein (Karte #772).
 * @returns {{version:string, board:object}} neue Projektion
 */
export function fold(snapshot, ops, protokoll, optionen) {
  const base = clone(snapshot);
  const board = base.board;
  if (!board || !Array.isArray(board.sprints)) return base;

  /* F-925/F-1046: Der Feld-Massstab kommt vom Aufrufer (Karten-Fabrik), nicht
     aus dem Bestand — sonst übernimmt er nach einer Kompaktierung die Lücken
     der Karten, die er beanstanden soll (Begründung bei
     `fehlendeMassstabFelder`). Nur genutzt, wenn ein Protokoll mitläuft. */
  const feldMassstab = Array.isArray(protokoll) && Array.isArray(optionen?.feldMassstab)
    ? optionen.feldMassstab : null;

  /* Karte #772: Opt-in wie der Feld-Massstab; ohne Protokoll nutzlos. */
  const effortTyp = Array.isArray(protokoll) && optionen?.effortTyp === true;

  const seen = new Set();
  for (const op of orderedOps(ops || [])) {
    if (!validateOp(op)) continue;       /* Poison überspringen (Projektor schiebt es nach .rejected/) */
    if (seen.has(op.id)) continue;       /* Idempotenz: jede op.id genau einmal */
    seen.add(op.id);
    applyOp(board, op, protokoll, feldMassstab, effortTyp);
  }
  return base;
}
