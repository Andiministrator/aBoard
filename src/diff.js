/* State-Diff → semantische Ops — ADR-0067 Stufe 1 (Karte #228)
 *
 * Übersetzt den Unterschied zweier Board-Stände (zuletzt persistierte
 * Projektion → aktueller optimistischer In-Memory-Stand) in semantische Ops
 * (src/ops.js OP_TYPES). So bleibt das bestehende Mutations-/EventBus-System
 * im Browser UNVERÄNDERT (mutateBoard erzeugt weiter den vollen Folge-State);
 * nur an der Persist-Grenze wird statt eines Ganzdatei-Writes der Diff als
 * Ops in die Inbox angehängt. Das hält die Regressionsfläche auf einem
 * 12,9-kLOC-Dogfooding-Werkzeug klein.
 *
 * REIN & runtime-agnostisch (kein DOM/node:fs) — wie ops.js/inbox.js, weil es
 * mit ins Browser-Bundle wandert.
 *
 * Granularität (ADR-0074 Teil B, realisiert #230/#238; Fallback-Bedingung
 * revidiert durch ADR-0124, Karte #464):
 *  - geänderte bestehende Karte → feingranulare `card.field`-Ops je Feld +
 *    `comment.add` je angehängtem Kommentar, WO risikoarm zerlegbar. Ein
 *    DAZUGEKOMMENES Feld ist dabei risikoarm (`card.field` trifft es exakt);
 *    ein ENTFERNTES nicht (keine Op-Form löscht einen Schlüssel).
 *    Nebenläufige Edits verschiedener Felder derselben Karte überleben so
 *    (Feld-LWW). Nicht sauber zerlegbar → harter Fallback auf EINEN
 *    Volltext-`card.upsert` (bewährtes Stufe-1-Verhalten).
 *  - neue Karte → `card.upsert` (kein Bestand zum Mergen).
 *  - Swimlane-Umordnung → ein `swimlane.move` je Sprint in Zielreihenfolge.
 *
 * Reichweite der Round-Trip-Zusage (ADR-0079 §Nachtrag Sprint 76, F-1162):
 * `fold(prev, diffToOps(prev, next))` stimmt mit `next` überein je
 * (Swimlane, Spalte) und ohne die vom Falter vergebenen Nummern
 * (`displayId`, `nextDisplayId`). Die rohe Position in `sprint.cards` quer
 * über Spalten hält sie nicht (`insertCard` mit `anchor: end` hängt ans
 * Listenende); neue Karten bekommen ihre Nummer in Op-Reihenfolge, nicht in
 * der Reihenfolge, in der sie tatsächlich angelegt wurden. Wer eines von
 * beidem auswertet, verlässt sich auf etwas, das hier nicht zugesagt ist.
 */

import { makeOp, MENGEN_FELDER, stabil, VOM_FOLD_VERWALTET } from './ops.js';
import { awaitingAlsAbbildung } from './awaiting.js';

/* F-290: gegen Model.createBoard-Keys per Drift-Test abgesichert
   (tests/board-field-drift.test.js) — muss synchron zu ops.js
   BOARD_FIELD_ALLOWLIST bleiben. */
export const BOARD_FIELDS = ['title', 'columnDefs', 'templates', 'background', 'identities', 'attachments'];
/* `awaitingUser` fehlt hier bewusst (ADR-0130 §7, Karte #609): je Person eine
   board.awaiting-Op statt einer board.field-Op, die die ganze Abbildung ersetzte.
   `termine` ebenso (ADR-0138 §4, Karte #743): je Termin termin.upsert/-delete. */

function eq(a, b) { return JSON.stringify(a) === JSON.stringify(b); }

/* Flache Karten-Indexierung: id → { card, sprintId, colIndex }.
   colIndex = Position der Karte UNTER Karten gleicher columnId im selben
   Sprint (Parität zur targetIndex-Semantik in ops.js/main.js). */
function indexCards(board) {
  const map = new Map();
  for (const sprint of board.sprints || []) {
    const perCol = new Map();
    for (const card of sprint.cards || []) {
      const n = perCol.get(card.columnId) || 0;
      perCol.set(card.columnId, n + 1);
      map.set(card.id, { card, sprintId: sprint.id, colIndex: n });
    }
  }
  return map;
}

/* ADR-0079 (F-311/F-307): geordnete Karten-id-Liste je (Sprint,Spalte).
   key = `${sprintId} ${columnId}`. Reihenfolge = sprint.cards-Array-
   Reihenfolge gefiltert auf die Spalte (== kanonische Spaltenordnung,
   Parität zum Renderer/colIndex). */
const ckey = (sprintId, colId) => sprintId + '\u0000' + colId;

function colSeqMap(board) {
  const m = new Map();
  for (const sprint of board.sprints || []) {
    for (const card of sprint.cards || []) {
      const k = ckey(sprint.id, card.columnId);
      let arr = m.get(k);
      if (!arr) { arr = []; m.set(k, arr); }
      arr.push(card.id);
    }
  }
  return m;
}


/* ADR-0079: identitätsstabiler Einfüge-Anker, den `diff.js` so wählt, dass
   ops.js insertCard im sequenziellen Fold beim ERSTEN Versuch trifft —
   Anker ist entweder eine STABILE (nicht-op'te, durchgehend präsente)
   Karte ODER der per Ziel-/seq-Ordnung zuvor schon eingefügte Vorgänger.
     idx>0  → after(Vorgänger in Zielordnung)  [stabil ⇒ präsent; op't ⇒
              kleinere seq ⇒ zuvor gefaltet ⇒ präsent]
     idx==0 → before(nächste stabile Nachfolgekarte) ⇒ präsent; sonst end. */
function emittedAnchor(list, idx, opd) {
  if (idx > 0) return { mode: 'after', cardId: list[idx - 1] };
  for (let j = 1; j < list.length; j++) {
    if (!opd.has(list[j])) return { mode: 'before', cardId: list[j] };
  }
  return { mode: 'end' };
}

/* ADR-0079 / F-316: Längste gemeinsame Teilfolge (deterministisch).
   Liefert das Set der ids, die in a UND b in GLEICHER Relativordnung
   vorkommen — exakt die Karten, die als stabiles, NICHT zu op'tendes
   Skelett dienen dürfen (ihre Relativordnung ist beidseitig identisch →
   sie stehen transient bereits korrekt; alle übrigen ko-residenten
   Karten werden op't und via Anker um das Skelett herum platziert).

   F-316-Lehre: Der frühere „unmittelbarer ko-resident-Vorgänger"-Dedup
   ließ Karten un-op't, deren ABSOLUTposition sich ändert (reine
   Block-Rotation einer Spalte → Drift). Eine echte gemeinsame Teilfolge
   garantiert beidseitig identische Relativordnung des Skeletts — der
   Round-Trip schließt. F-307 bleibt mitbehoben: ein WEG-verschobenes
   Geschwister fällt aus beiden Seiten gleichermaßen → die LCS der
   übrigen ändert sich nicht → kein Phantom-card.move. */
function lcsKeep(a, b) {
  const m = a.length, n = b.length;
  if (m === 0 || n === 0) return new Set();
  const dp = Array.from({ length: m + 1 }, () => new Int32Array(n + 1));
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j]
        ? dp[i + 1][j + 1] + 1
        : (dp[i + 1][j] >= dp[i][j + 1] ? dp[i + 1][j] : dp[i][j + 1]);
    }
  }
  const keep = new Set();
  let i = 0, j = 0;
  while (i < m && j < n) {
    if (a[i] === b[j]) { keep.add(a[i]); i++; j++; }
    else if (dp[i + 1][j] >= dp[i][j + 1]) i++;
    else j++;
  }
  return keep;
}

/* Identität (ops.js CARD_FIELD_BLOCKLIST) + Position (macht card.move) —
   nie als card.field emittieren. */
const CARD_NON_FIELD = new Set(['id', 'displayId', 'columnId']);
const VERWALTET = new Set(VOM_FOLD_VERWALTET);

/* nc.comments == pc.comments + N angehängte (unveränderter Prefix, jeder
   neue Kommentar mit id für fold-Idempotenz). Sonst null = kein reines
   Anhängen (editiert/gelöscht) → Aufrufer nutzt Volltext-Upsert. */
function appendedComments(pComments, nComments) {
  const p = pComments || [], n = nComments || [];
  if (!Array.isArray(n) || n.length < p.length) return null;
  for (let i = 0; i < p.length; i++) if (!eq(p[i], n[i])) return null;
  const extra = n.slice(p.length);
  for (const c of extra) if (!c || c.id == null) return null;
  return extra;
}

/* ADR-0130 §4 (Karte #601): Kommentar-Änderungen, die KEIN reines Anhängen
   sind, trotzdem je Kommentar zerlegen — statt wie bis Sprint 76 auf den
   Voll-card.upsert zu fallen, der einen gleichzeitig angehängten fremden
   Kommentar derselben Karte verlor (ADR-0129 §4 „Bekannte Grenze").

   Zulässig ist genau diese Form:
     - jeder Kommentar in prev UND next trägt eine eindeutige Zeichenketten-id
       (sonst lässt sich kein einzelner Kommentar adressieren);
     - die Überlebenden (id in beiden) stehen in next in derselben Reihenfolge
       wie in prev und VOR allen neuen — die Ops ordnen nicht um;
     - ein Überlebender unterscheidet sich höchstens in `text` und `edited`,
       und keines der beiden Felder fehlt in next, das prev hatte (keine
       Op-Form löscht ein Kommentarfeld — dieselbe Asymmetrie wie §20).
   Ergebnis: comment.delete je entfallenem, comment.edit je geändertem,
   comment.add je neuem Kommentar. Jede andere Form → null → Volltext.

   Reihenfolge der Ops: delete, edit, add. Das Ziel ist per Konstruktion
   erreicht: nach den Löschungen stehen die Überlebenden in ihrer (gleichen)
   Reihenfolge, edit ändert nur Inhalte, add hängt in next-Reihenfolge an. */
function commentOps(cardId, pComments, nComments) {
  const p = pComments || [], n = nComments || [];
  if (!Array.isArray(p) || !Array.isArray(n)) return null;
  const idsOk = arr => {
    const seen = new Set();
    for (const c of arr) {
      if (!c || typeof c !== 'object' || typeof c.id !== 'string' || seen.has(c.id)) return false;
      seen.add(c.id);
    }
    return true;
  };
  if (!idsOk(p) || !idsOk(n)) return null;
  const nById = new Map(n.map(c => [c.id, c]));
  const pIds = new Set(p.map(c => c.id));
  const survivors = p.filter(c => nById.has(c.id));
  /* Überlebende zuerst, in unveränderter Reihenfolge; danach nur Neue. */
  for (let i = 0; i < survivors.length; i++) {
    if (n[i].id !== survivors[i].id) return null;
  }
  for (let i = survivors.length; i < n.length; i++) {
    if (pIds.has(n[i].id)) return null;
  }
  const out = [];
  for (const c of p) if (!nById.has(c.id)) out.push(['comment.delete', { cardId, commentId: c.id }]);
  for (const pc of survivors) {
    const nc = nById.get(pc.id);
    if (eq(pc, nc)) continue;
    for (const k of Object.keys(pc)) if (!(k in nc)) return null;
    for (const k of new Set([...Object.keys(pc), ...Object.keys(nc)])) {
      if (k === 'text' || k === 'edited') continue;
      if (!eq(pc[k], nc[k])) return null;
    }
    if (typeof nc.text !== 'string') return null;
    const payload = { cardId, commentId: pc.id, text: nc.text };
    if ('edited' in nc && !eq(pc.edited, nc.edited)) payload.edited = nc.edited;
    out.push(['comment.edit', payload]);
  }
  for (let i = survivors.length; i < n.length; i++) out.push(['comment.add', { cardId, comment: n[i] }]);
  return out;
}

/* ADR-0130 §3 (Karte #605): eine geänderte Mengen-Liste als card.listRemove/
   card.listAdd statt ganzer Liste. Nur, wenn beide Seiten Listen ohne
   Doppelte sind UND fold(prev, Mengen-Ops) die Reihenfolge von next genau
   trifft (listAdd hängt ans Ende an) — sonst null, und der Aufrufer schickt
   wie bisher card.field mit der ganzen Liste. Das hält die Round-Trip-Zusage
   auch beim bewussten Umsortieren (Kritiker K-4). */
function mengenOps(cardId, field, pList, nList) {
  if (!Array.isArray(pList) || !Array.isArray(nList)) return null;
  const pForm = pList.map(stabil), nForm = nList.map(stabil);
  if (new Set(pForm).size !== pForm.length || new Set(nForm).size !== nForm.length) return null;
  const nSet = new Set(nForm), pSet = new Set(pForm);
  const bleibt = pForm.filter(f => nSet.has(f));
  const neu = nForm.filter(f => !pSet.has(f));
  if (JSON.stringify([...bleibt, ...neu]) !== JSON.stringify(nForm)) return null;
  const out = [];
  pList.forEach((x, i) => { if (!nSet.has(pForm[i])) out.push(['card.listRemove', { cardId, field, item: x }]); });
  nList.forEach((x, i) => { if (!pSet.has(nForm[i])) out.push(['card.listAdd', { cardId, field, item: x }]); });
  return out;
}

/* Zerlegt eine geänderte BESTEHENDE Karte feingranular (ADR-0074 Teil B):
   je geändertes Nicht-Positions-Feld eine card.field-Op, angehängte
   Kommentare als comment.add, entfernte/geänderte als comment.delete/
   comment.edit (ADR-0130 §4). Gibt null zurück, sobald nicht sicher
   zerlegbar (Feld ENTFERNT / Kommentarliste in keiner der beiden Formen) — dann fällt der
   Aufrufer deterministisch auf Volltext-card.upsert zurück, die
   Round-Trip-Invariante bleibt unberührt.

   Karte #464: Bis Sprint 64 brach diese Funktion schon bei ABWEICHENDER
   SCHLÜSSELMENGE ab (`nKeys.length !== Object.keys(pc).length`) — also auch
   dann, wenn nur ein Feld DAZUGEKOMMEN war. Genau das tut jede additive
   Migration beim Board-Load, und der Volltext-Fallback schickte daraufhin
   die ganze Karte samt `description`, `agentContext`, `comments` und
   `activity` los. Gemessen am Vorfall vom 2026-08-30 (Commit `a1d40b2`):
   Jemand archiviert eine Swimlane, drei unbeteiligte Backlog-Karten gehen als
   Volltext mit — sie hatten 23 Felder in der Projektion und 25 im Speicher,
   ergänzt hatten `migrateProjectId` (ADR-0102) und
   `migrateRecurrence` (ADR-0041). Da `applyOp` bei `card.upsert` auf
   eine bestehende Karte den kompletten Inhalt ersetzt (LWW), ist das
   derselbe Verlustweg wie in Karte #449, nur eine Ebene tiefer.

   Die beiden Richtungen sind NICHT symmetrisch, und das ist der Kern:
     - Feld dazugekommen → `card.field` trifft den Zustand exakt (die
       Blocklist in ops.js sperrt nur `id`/`displayId`, nicht neue Felder).
     - Feld entfernt     → keine Op-Form trifft das. `card.field` mit
       `undefined` SCHREIBT den Schlüssel, statt ihn zu löschen. Hier bleibt
       der Volltext-Fallback die einzige Form, die den Zustand herstellt. */
function cardContentOps(cardId, pc, nc, bedingt) {
  const out = [];
  /* ADR-0130 §1 (Karte #606): `claim` setzen nur card.claim/card.release.
     Der Diff überträgt es nie, in keine Richtung — auch ein im Browser
     verschwundenes `claim` erzwingt keinen Volltext. Die Round-Trip-Zusage
     (ADR-0079) gilt ohne diese Felder, wie bei displayId. */
  for (const k of Object.keys(pc)) if (!VERWALTET.has(k) && !(k in nc)) return null;   /* entfernt → Volltext */
  for (const k of Object.keys(nc)) {
    if (VERWALTET.has(k)) continue;
    const neuerKey = !(k in pc);
    if (CARD_NON_FIELD.has(k)) {
      /* Identität und Position nie als card.field. Taucht so ein Schlüssel
         NEU auf, trifft ihn keine feingranulare Op → Volltext. Real kommt
         das nicht vor (jede Karte hat id/displayId/columnId); der Zweig ist
         die ehrliche Antwort auf einen Zustand, den wir nicht abbilden. */
      if (neuerKey) return null;
      continue;
    }
    if (!neuerKey && eq(pc[k], nc[k])) continue;
    if (k === 'comments' && !neuerKey) {
      const added = appendedComments(pc.comments, nc.comments);
      if (added !== null) {
        for (const c of added) out.push(['comment.add', { cardId, comment: c }]);
        continue;
      }
      /* Kein reines Anhängen → je Kommentar zerlegen (ADR-0130 §4). Erst wenn
         auch das nicht geht, bleibt der Volltext. */
      const einzeln = commentOps(cardId, pc.comments, nc.comments);
      if (einzeln === null) return null;
      out.push(...einzeln);
      continue;
    }
    /* `comments` als NEUER Schlüssel läuft bewusst unten in den
       card.field-Zweig und setzt das ganze Array. `appendedComments` würde
       hier bei einer leeren Liste GAR KEINE Op erzeugen (`extra` ist leer) —
       der Schlüssel fehlte danach, und der Round-Trip bräche an einer
       Stelle, an der niemand ihn vermutet. Ein Feld, das erst entsteht, ist
       kein Anhängen. */
    /* Hinweis (Sprint-33-Kritiker): Ist nc[k] === undefined, droppt
       JSON.stringify den Key in der Inbox-Datei → applyOp setzt
       loc.card[field]=undefined. In-Memory- und Disk-fold konvergieren
       hier zufällig deckungsgleich; das Board-Modell produziert real kein
       undefined (Load sanitiert). Latente In-Memory↔Disk-Asymmetrie —
       würde relevant, falls je undefined-Werte ins Karten-Modell gelangen
       (dann hier explizit auf null normalisieren). */
    /* ADR-0130 §2 (Karte #604): für die vom Aufrufer genannten Felder mit
       erwartetem Vorwert — der Vorwert ist prev, also der Bezug B. Nur, wenn
       das Feld in prev einen Wert hatte: `undefined` überlebt JSON nicht, und
       eine Op ohne `expect` wiese der Fold als erwartung-fehlt ab. */
    if (!neuerKey && MENGEN_FELDER.includes(k)) {
      const mengen = mengenOps(cardId, k, pc[k], nc[k]);
      if (mengen !== null) { out.push(...mengen); continue; }
    }
    if (bedingt && bedingt.has(k) && !neuerKey && pc[k] !== undefined) {
      out.push(['card.fieldIf', { cardId, field: k, value: nc[k], expect: pc[k] }]);
      continue;
    }
    out.push(['card.field', { cardId, field: k, value: nc[k] }]);
  }
  return out;
}

/**
 * Erzeugt die Op-Liste, die `prev` in `next` überführt.
 * Reihenfolge: board.field → board.awaiting → termin.delete/upsert → swimlane.upsert/delete → swimlane.move →
 * card.delete → card.upsert(neu) → card.move → card.upsert(geändert).
 * (Löschen vor Neu/Move vermeidet Phantom-Kollisionen; move vor
 *  Content-Upsert hält Position & Inhalt konsistent — siehe ops.js.)
 *
 * @param {{board:object}} prev  zuletzt persistierte Projektion
 * @param {{board:object}} next  aktueller In-Memory-Stand
 * @param {string} actor
 * @param {{bedingteFelder?: Iterable<string>}} [optionen]
 *   `bedingteFelder`: Kartenfelder, die als card.fieldIf mit dem Wert aus
 *   `prev` als erwartetem Vorwert gehen (ADR-0130 §2). Ohne Angabe wie bisher
 *   nur card.field — der lokale Projektor-Weg bleibt unverändert.
 * @returns {Array<object>} Ops (leer wenn nichts geändert)
 */
/* Kritiker K2-2 / Verify V1-1 (Sprint 82, ADR-0133 §1): `finding.nr` geht nie
   als Op hinaus — auf KEINEM Weg (Browser im Server-Modus über
   src/rest-ops.js, Projektor/Standalone über src/sync.js, Werkzeuge mit
   diffToOps). Die Nummer vergibt der Fold. Eine lokal vergebene (src/main.js
   `card:quick-add`) weicht ab, sobald woanders inzwischen ein Finding
   entstanden ist; bis der fremde Stand übernommen ist, ist der Diff-Bezug der
   lokale Stand mit der lokalen Nummer. Jede Bearbeitung schickte sie als
   `card.field finding` oder Volltext-`card.upsert` mit, und der Fold lehnte die
   Op als `finding-nr-gesperrt` ab. Ohne `nr` nimmt der Fold die Nummer des
   Bestands (`setzeKartenFeld`, `card.upsert`-Zweig in src/ops.js) bzw.
   vergibt bei Neuanlage eine. Kopie statt Mutation: `payload.card` ist die
   Karte aus `next`. */
function ohneFindingNr(type, payload) {
  if (type === 'card.upsert' && payload.card && payload.card.finding
      && typeof payload.card.finding === 'object' && 'nr' in payload.card.finding) {
    const { nr, ...finding } = payload.card.finding;
    void nr;
    return { ...payload, card: { ...payload.card, finding } };
  }
  if (type === 'card.field' && payload.field === 'finding' && payload.value
      && typeof payload.value === 'object' && 'nr' in payload.value) {
    const { nr, ...value } = payload.value;
    void nr;
    return { ...payload, value };
  }
  return payload;
}

export function diffToOps(prev, next, actor, optionen) {
  const bedingt = optionen?.bedingteFelder ? new Set(optionen.bedingteFelder) : null;
  const ops = [];
  const add = (type, payload) => ops.push(makeOp(type, ohneFindingNr(type, payload), actor));

  const pB = prev?.board;
  const nB = next?.board;
  if (!pB || !nB) return ops;

  /* ── Board-Level-Felder ──────────────────────────────────────────── */
  for (const f of BOARD_FIELDS) {
    if (!eq(pB[f], nB[f])) add('board.field', { field: f, value: nB[f] });
  }

  /* ── awaitingUser je Person (ADR-0130 §7, Karte #609) ─────────────────
     Eine Op je geänderten Schlüssel. Ein Schlüssel, den `next` nicht mehr
     trägt, geht als `value: null`. Wer nur seinen eigenen Eintrag ändert,
     sendet damit nur diesen — die Einträge der anderen bleiben unberührt. */
  {
    const pA = awaitingAlsAbbildung(pB.awaitingUser, pB);
    const nA = awaitingAlsAbbildung(nB.awaitingUser, nB);
    for (const k of new Set([...Object.keys(pA), ...Object.keys(nA)])) {
      const alt = Object.prototype.hasOwnProperty.call(pA, k) ? pA[k] : null;
      const neu = Object.prototype.hasOwnProperty.call(nA, k) ? nA[k] : null;
      if (eq(alt, neu)) continue;
      /* F-1336: ein Sitzungs-Eintrag geht als { person, session } zurück —
         der Schlüssel `<Person> · <Sitzung>` ist keine Person, und `person`/
         `session` im Eintrag stempelt der Fold selbst. */
      const s = (neu ?? alt)?.session;
      if (typeof s === 'string') {
        const { person: pers, session: _s, ...wert } = neu ?? alt;
        add('board.awaiting', { person: pers, session: s, value: neu ? wert : null });
      } else {
        add('board.awaiting', { person: k, value: neu });
      }
    }
  }

  /* ── Termine ohne Karte (ADR-0138 §4, Karte #743) ───────────────────
     Je Termin eine Op, nie die ganze Liste: termin.delete für jeden, der in
     `next` fehlt, termin.upsert für jeden neuen oder geänderten. Unveränderte
     Termine erzeugen nichts — auch nicht, wenn die Liste nur umsortiert ist
     oder ganz fehlt statt leer zu sein. `termine` steht deshalb NICHT in
     BOARD_FIELDS. Einträge ohne Zeichenketten-id lassen sich nicht
     adressieren und bleiben aussen vor (der Fold kennt sie ohnehin nur aus
     dem Bestand). Herkunft (`erstellt`, `erstelltVon`) bewahrt der Fold vom
     Bestand; eine Änderung daran überträgt dieser Weg also nicht. */
  {
    const termine = b => new Map((Array.isArray(b.termine) ? b.termine : [])
      .filter(t => t && typeof t.id === 'string' && t.id).map(t => [t.id, t]));
    const pT = termine(pB);
    const nT = termine(nB);
    for (const id of pT.keys()) if (!nT.has(id)) add('termin.delete', { terminId: id });
    for (const [id, t] of nT) if (!pT.has(id) || !eq(pT.get(id), t)) add('termin.upsert', { termin: t });
  }

  const pSprints = pB.sprints || [];
  const nSprints = nB.sprints || [];
  const pSprintById = new Map(pSprints.map(s => [s.id, s]));
  const nSprintById = new Map(nSprints.map(s => [s.id, s]));

  /* ── Swimlanes: neu / Meta-Änderung ──────────────────────────────── */
  for (const s of nSprints) {
    const old = pSprintById.get(s.id);
    /* ADR-0117: `archived` ist das dritte Swimlane-Metafeld.
       Zwei Feinheiten, die beide aus der Round-Trip-Invariante
       `fold(prev, diff(prev,next)) === next` folgen:

       1. VERGLICHEN wird mit `!!` — `undefined` und `false` sind derselbe
          Zustand. Ohne die Normalisierung meldete jeder Diff eines noch nicht
          migrierten Boards eine Aenderung und erzeugte endlos Phantom-Ops.
       2. MITGESENDET wird das Feld nur, wenn die Quelle es wirklich hat. Der
          Fold darf `next` nicht um ein Feld ergaenzen, das dort nicht steht —
          sonst bricht die Invariante. Das ist dieselbe Konvention wie bei
          `card.archived`: gesetzt wird das Feld von der Migration beim Laden,
          nicht von der Fabrik. */
    const swimlaneMeta = (sp, mitArchived) => {
      const meta = { id: sp.id, title: sp.title, kind: sp.kind };
      if (mitArchived && sp.archived !== undefined) meta.archived = !!sp.archived;
      return meta;
    };
    if (!old) {
      add('swimlane.upsert', { sprint: swimlaneMeta(s, true) });
    } else if (old.title !== s.title || old.kind !== s.kind || !!old.archived !== !!s.archived) {
      /* F-795/Karte #449: `archived` NUR mitsenden, wenn es sich tatsächlich
         geändert hat — nicht, sobald es definiert ist. Sonst schreibt jede
         Titel- oder Kind-Änderung den migrations-gesetzten Default fest, und
         eine parallel gefaltete Archivierung wird stillschweigend
         zurückgenommen. Gemessen (Karte #449): `{id, title:'Alt', kind:'lane'}`
         gegen `{…, title:'Neu', archived:false}` erzeugte eine Op mit
         `archived:false`; auf eine base mit `archived:true` gefaltet, stand
         danach `false` — der Vorfall vom 2026-08-28, reproduziert.

         Punkt 2 des Kommentars oben („mitgesendet nur, wenn die Quelle es
         wirklich hat") begründete das Huckepack mit der Round-Trip-Invariante.
         Das trägt nicht, und zwar nachgemessen: Bei `prev` OHNE `archived`
         gegen `next` MIT `archived:false` und sonst gleichem Inhalt entsteht
         schon vorher GAR KEINE Op — der Round-Trip liefert dort einen Stand
         ohne das Feld und ist damit bereits byte-ungleich zu `next`. Die
         Invariante hielt für dieses Feld also nur dann, wenn zufällig eine
         Titel- oder Kind-Änderung mitlief. Das ist kein Schutz, sondern ein
         Nebeneffekt — und er kostet mehr, als er trägt. `undefined` und
         `false` sind nach Punkt 1 derselbe Zustand; der Round-Trip ist für
         dieses Feld semantisch, nicht byte-genau (siehe
         `tests/diff-swimlane-archived-449.test.js`). */
      add('swimlane.upsert', { sprint: swimlaneMeta(s, !!old.archived !== !!s.archived) });
    }
  }
  /* ── Swimlanes: gelöscht ─────────────────────────────────────────── */
  for (const s of pSprints) {
    if (!nSprintById.has(s.id)) add('swimlane.delete', { sprintId: s.id });
  }
  /* ── Swimlanes: Umordnung ────────────────────────────────────────────
     Sobald die VOLLständige Sprint-id-Sequenz abweicht (auch: neuer Sprint
     VOR einem bestehenden, was die reine upsert-vor-__backlog__-Einfügung
     nicht trifft), die Zielreihenfolge per „move jeden Sprint an seinen
     Zielindex" exakt erzwingen. fold wendet die Moves dank seq-Tiebreak in
     Emissions-Reihenfolge an → deterministisch genau die nextIds-Folge
     (Selection-to-target, korrekt da upserts/deletes davor laufen). */
  const prevIds = pSprints.map(s => s.id);
  const nextIds = nSprints.map(s => s.id);
  if (!eq(prevIds, nextIds)) {
    nSprints.forEach((s, idx) => add('swimlane.move', { sprintId: s.id, targetIndex: idx }));
  }

  /* ── Karten ──────────────────────────────────────────────────────── */
  const pCards = indexCards(pB);
  const nCards = indexCards(nB);

  /* gelöscht */
  for (const [id] of pCards) {
    if (!nCards.has(id)) add('card.delete', { cardId: id });
  }
  /* ── Positions-Ops: neu + verschoben, EIN Pass in Zielordnung (ADR-0079) ──
     F-311: früher emittierten zwei getrennte Pässe `card.upsert(neu)` und
     `card.move` je mit ABSOLUTEM targetIndex. Der Absolut-Index aus dem
     Zielzustand driftet aber gegen den TRANSIENTEN Spaltenzustand im
     sequenziellen Fold, sobald ≥2 Karten in EINEM Batch in dieselbe
     (Sprint,Spalte) eintreten. Jetzt: ein Pass in Zielordnung (nCards ist
     in sprint.cards-Array-/Zielreihenfolge) mit monoton steigender seq +
     identitätsstabilem `anchor` (insertCard löst ihn auf). `targetIndex`
     bleibt als Legacy-/Defensiv-Feld (Alt-Inbox-Ops ohne anchor falten
     unverändert — kein Schema-Bump, additiv).

     F-307: `card.move` wird NICHT mehr bei reinem colIndex-Shift emittiert.
     Eine bestehende Karte (gleicher Sprint+Spalte) ist nur dann op't, wenn
     sich ihre Reihenfolge RELATIV ZU DEN IN BEIDEN ZUSTÄNDEN KO-RESIDENTEN
     Karten derselben (Sprint,Spalte) ändert (gemeinsamer Vorgänger weicht
     ab). Verschiebt ein Geschwister WEG, ändert das den gemeinsamen
     Vorgänger der Bleiber NICHT → kein Phantom-Move (eliminiert F-307;
     Korrektheit bleibt: Bleiber stehen transient bereits in Zielordnung,
     ihre Nachbarn werden via deren eigene Anker um sie herum platziert). */
  const pColSeq = colSeqMap(pB);
  const nColSeq = colSeqMap(nB);

  /* Pass 1 — op'te Menge bestimmen (deterministisch, vor der Emission).
     Schritt A: neu / Sprint- bzw. Spaltenwechsel → immer op't.
     Schritt B (F-316): pro (Sprint,Spalte) das stabile Skelett = eine
     LCS der ko-residenten Karten in prev- vs. next-Ordnung; jede
     ko-residente Karte, die NICHT im Skelett liegt, wird op't. Damit ist
     die un-op'te Menge garantiert beidseitig relativordnungs-identisch
     → Round-Trip schließt auch bei reiner Mehrkarten-Umsortierung;
     F-307-Phantommove bleibt eliminiert (s. lcsKeep). */
  const opd = new Set();
  const groupKeys = new Set();
  for (const [id, nLoc] of nCards) {
    const pLoc = pCards.get(id);
    if (!pLoc) { opd.add(id); continue; }                       /* neue Karte */
    if (pLoc.sprintId !== nLoc.sprintId
        || pLoc.card.columnId !== nLoc.card.columnId) { opd.add(id); continue; }
    groupKeys.add(ckey(nLoc.sprintId, nLoc.card.columnId));     /* ko-resident-Kandidat */
  }
  for (const key of groupKeys) {
    const pSeq = pColSeq.get(key) || [];
    const nSeq = nColSeq.get(key) || [];
    const pSet = new Set(pSeq);
    const nSet = new Set(nSeq);
    /* ko-resident = in dieser (Sprint,Spalte) in BEIDEN Zuständen, und
       nicht bereits anderweitig op't (neu / Sprint-/Spaltenwechsel). */
    const pCo = pSeq.filter(x => nSet.has(x) && !opd.has(x));
    const nCo = nSeq.filter(x => pSet.has(x) && !opd.has(x));
    const keep = lcsKeep(pCo, nCo);
    for (const x of nCo) if (!keep.has(x)) opd.add(x);
  }

  /* Pass 2 — in Zielordnung emittieren (nCards-Map = Zielreihenfolge). */
  for (const [id, nLoc] of nCards) {
    if (!opd.has(id)) continue;
    const nSeq   = nColSeq.get(ckey(nLoc.sprintId, nLoc.card.columnId)) || [];
    const anchor = emittedAnchor(nSeq, nLoc.colIndex, opd);
    if (!pCards.has(id)) {
      add('card.upsert', {
        sprintId: nLoc.sprintId,
        columnId: nLoc.card.columnId,
        targetIndex: nLoc.colIndex,
        anchor,
        card: nLoc.card,
      });
    } else {
      add('card.move', {
        cardId: id,
        toSprintId: nLoc.sprintId,
        toColId: nLoc.card.columnId,
        targetIndex: nLoc.colIndex,
        anchor,
      });
    }
  }
  /* Inhalt geändert (alles außer Position — die macht card.move).
     Feingranular wo risikoarm (ADR-0074 Teil B): je geändertes Feld eine
     card.field-Op, angehängte Kommentare als comment.add → nebenläufige
     Edits VERSCHIEDENER Felder derselben Karte überleben (Feld-LWW statt
     Ganzkarten-LWW). Nicht sauber zerlegbar → Volltext-card.upsert
     (bewährtes Stufe-1-Verhalten; Round-Trip-Invariante unberührt). */
  for (const [id, nLoc] of nCards) {
    const pLoc = pCards.get(id);
    if (!pLoc) continue;
    if (eq(pLoc.card, nLoc.card)) continue;
    const granular = cardContentOps(id, pLoc.card, nLoc.card, bedingt);
    if (granular === null) add('card.upsert', { sprintId: nLoc.sprintId, card: nLoc.card });
    else for (const [t, p] of granular) add(t, p);
  }

  return ops;
}
