/**
 * rest-ops.js — Op-Schicht des Browsers im Server-Modus (ADR-0129 §4,
 * Nachtrag Sprint 72, Punkte 1, 5 und 7; Karte #564).
 *
 * Der Browser speichert im Server-Modus über `POST /boards/{id}/ops` statt über
 * den Ganz-Board-PUT. Dafür braucht er zweierlei aus der Op-Algebra:
 *   - `teileInBatches`: einen Diff so teilen, dass jeder Batch unter den
 *     Grenzen des Servers bleibt (200 Ops, 1 MiB Body; `server/api/boards_ops.go`).
 *   - `fuehreBezugNach`: den Diff-Bezug nach einem angenommenen Batch
 *     nachführen, ohne auf den Server-Stand zu springen (Punkt 1: der Bezug
 *     muss kein Server-Stand sein, nur einer, von dem der lokale Stand abstammt).
 *
 * Keine Build-Konstanten, kein DOM, kein Netz: Das Modul ist in jedem Build
 * gleich und in jedem Test-Kontext prüfbar. Ins ausgelieferte Bündel gelangt es
 * nur über den `IS_SERVER_BUILD`-Block von `src/storage-plugin.js`. Im slim
 * Template fällt es mit diesem Block heraus (ADR-0072, genauer gefasst im
 * Nachtrag Sprint 72, Punkt 7: Projektor-Schicht nur im Projektor-Build,
 * Op-Algebra in jedem Build, der sie braucht).
 */

import { diffToOps } from './diff.js';
import { fold, makeOp } from './ops.js';

/* Grenzen des Servers (server/api/boards_ops.go: MaxOpsJeBatch,
   MaxOpsBodyBytes). Die Byte-Grenze liegt bewusst unter 1 MiB: Die Hülle
   `{"ops":[…]}` und die Kommas zählen mit, und ein Batch knapp an der Grenze
   soll nicht an ein paar Bytes Rundung scheitern (ADR-Nachtrag, Punkt 5). */
export const MAX_OPS_JE_BATCH   = 200;
export const MAX_BATCH_BYTES    = 900 * 1024;

/* PURE: sonst bleibt der Aufruf als Nebenwirkung im slim Template stehen,
   obwohl das Modul dort sonst komplett herausfällt (gemessen +16 B, #564). */
const _enc = /*#__PURE__*/ new TextEncoder();
function bytesVon(op) {
  return _enc.encode(JSON.stringify(op)).length;
}

/**
 * Teilt eine Op-Folge in Batches, deren Reihenfolge die der Eingabe ist.
 *
 * Jeder Batch ist ein zusammenhängendes Stück der Folge, deshalb ist jeder
 * Zwischenstand nach einem angenommenen Batch ein gültiges Board (Punkt 5).
 * Eine einzelne Op, die mit Trennzeichen nicht unter `maxBytes` bleibt, passt in
 * keinen Batch. Sie wird nicht
 * zerteilt, sondern gemeldet (`zuGross`); der Aufrufer nimmt dafür den PUT mit
 * `If-Match` (Punkt 5, letzter Absatz). In diesem Fall ist `batches` leer —
 * ein Teil-Versand VOR der zu grossen Op und danach ein PUT würde dieselbe
 * Änderung auf zwei Wegen schicken.
 *
 * @param {object[]} ops
 * @param {{maxOps?: number, maxBytes?: number}} [grenzen]
 * @returns {{ batches: object[][], zuGross: object[] }}
 */
export function teileInBatches(ops, { maxOps = MAX_OPS_JE_BATCH, maxBytes = MAX_BATCH_BYTES } = {}) {
  const liste = Array.isArray(ops) ? ops : [];
  /* Dasselbe Maß wie unten beim Füllen (Op + 1 Byte Komma). Mit `bytesVon(op)
     > maxBytes` rutschte eine Op an der Grenze allein in einen Batch, der
     dann über `maxBytes` lag (Session-Kritiker K-3, Sprint 72). */
  const zuGross = liste.filter(op => bytesVon(op) + 1 > maxBytes);
  if (zuGross.length) return { batches: [], zuGross };

  const batches = [];
  let aktuell = [];
  let bytes = 0;
  for (const op of liste) {
    const b = bytesVon(op) + 1; /* +1 für das Komma zwischen den Ops */
    if (aktuell.length && (aktuell.length >= maxOps || bytes + b > maxBytes)) {
      batches.push(aktuell);
      aktuell = [];
      bytes = 0;
    }
    aktuell.push(op);
    bytes += b;
  }
  if (aktuell.length) batches.push(aktuell);
  return { batches, zuGross: [] };
}

/* Fester Zeitstempel für das lokale Nachfalten. Er ordnet nichts gegen fremde
   Ops, weil hier keine fremden Ops gefaltet werden. Er muss nur für alle Ops
   gleich sein, damit wie am Server allein `seq` = Position entscheidet. */
const NACHFALT_TS = '1970-01-01T00:00:00.000Z';

/**
 * Führt den Diff-Bezug nach einem angenommenen Batch nach: `fold(bezug, batch)`.
 *
 * Gestempelt wird wie am Server (`stempleOps` in server/api/boards_ops.go):
 * gleicher `ts` für den ganzen Batch, `seq` = Position. So entscheidet die
 * Reihenfolge, die `diffToOps` erzeugt hat, und nicht die Uhr oder die
 * Zufalls-`id`. Die Eingaben werden nicht verändert: `fold` klont seinen
 * Eingang, die Ops werden kopiert, nicht umgestempelt.
 *
 * `displayId` neuer Karten vergibt hier der lokale Fold, nicht der Server. Für
 * den Bezug ist das gleichgültig: `diffToOps` sendet `displayId` nie als
 * `card.field`, und die echten Nummern kommen mit dem GET nach dem POST
 * (Punkt 2).
 *
 * @param {{version: string, board: object}} bezug
 * @param {object[]} batch
 * @returns {{version: string, board: object}}
 */
export function fuehreBezugNach(bezug, batch) {
  const gestempelt = (Array.isArray(batch) ? batch : [])
    .map((op, i) => ({ ...op, ts: NACHFALT_TS, seq: i }));
  return fold(bezug, gestempelt);
}

/**
 * Ops, die den lokalen Stand gegenüber dem Bezug ausdrücken, fertig geteilt.
 * Dünne Hülle um `diffToOps` + `teileInBatches`, damit das REST-Plugin nur
 * einen Einstieg hat.
 *
 * @returns {{ ops: object[], batches: object[][], zuGross: object[] }}
 */
/* ADR-0130 §2 (Karte #604): Freitext geht am Server mit erwartetem Vorwert.
   Ändert jemand anders dasselbe Feld, wird das eine sichtbare Ablehnung statt
   eines stillen Überschreibens. Strukturierte Felder bleiben Feld-LWW. */
export const BEDINGTE_FELDER = Object.freeze(['title', 'description', 'agentContext']);

/* Karte #604: „Meine Fassung übernehmen“ nach einer feld-geaendert-Ablehnung
   — dieselben Felder, diesmal bedingungslos als card.field. Bewusst NICHT der
   Ganz-Board-PUT: der überschriebe auch fremde Änderungen an anderen Karten. */
export function bedingungsloseFeldOps(eintraege, actor) {
  return (eintraege || [])
    .filter(e => e && typeof e.cardId === 'string' && typeof e.feld === 'string')
    .map(e => makeOp('card.field', { cardId: e.cardId, field: e.feld, value: e.mein }, actor || ''));
}

/* Kritiker K2-2 / Verify V1-1: `finding.nr` lässt `diffToOps` selbst weg
   (src/diff.js `ohneFindingNr`) — für diesen Weg wie für Projektor und
   Standalone (src/sync.js), die `diffToOps` direkt aufrufen. Hier ist dafür
   nichts mehr zu tun. */
export function bereiteOpsVor(bezug, lokal, actor) {
  const ops = diffToOps(bezug, lokal, actor || '', { bedingteFelder: BEDINGTE_FELDER });
  return { ops, ...teileInBatches(ops) };
}
