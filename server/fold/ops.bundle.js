(() => {
  var __defProp = Object.defineProperty;
  var __returnValue = (v) => v;
  function __exportSetter(name, newValue) {
    this[name] = __returnValue.bind(null, newValue);
  }
  var __export = (target, all) => {
    for (var name in all)
      __defProp(target, name, {
        get: all[name],
        enumerable: true,
        configurable: true,
        set: __exportSetter.bind(all, name)
      });
  };

  // src/ops.js
  var exports_ops = {};
  __export(exports_ops, {
    BASE_FILE: () => BASE_FILE,
    BOARD_FIELD_ALLOWLIST: () => BOARD_FIELD_ALLOWLIST,
    CARD_ARRAY_FIELDS: () => CARD_ARRAY_FIELDS,
    CLAIM_LEASE_MAX: () => CLAIM_LEASE_MAX,
    CLAIM_LEASE_MIN: () => CLAIM_LEASE_MIN,
    CLAIM_LEASE_STANDARD: () => CLAIM_LEASE_STANDARD,
    FOLD_MARKE: () => FOLD_MARKE,
    HEARTBEAT_FILE: () => HEARTBEAT_FILE,
    INBOX_DIR: () => INBOX_DIR,
    MENGEN_FELDER: () => MENGEN_FELDER,
    OP_PAYLOAD_SPEC: () => OP_PAYLOAD_SPEC,
    OP_SCHEMA_VERSION: () => OP_SCHEMA_VERSION,
    OP_TYPES: () => OP_TYPES,
    PROJECTION_FILE: () => PROJECTION_FILE,
    REBASE_MARKE: () => REBASE_MARKE,
    REJECTED_DIR: () => REJECTED_DIR,
    TERMIN_FELDLISTE: () => TERMIN_FELDLISTE,
    TERMIN_HERKUNFT: () => TERMIN_HERKUNFT,
    TERMIN_VERWALTET: () => TERMIN_VERWALTET,
    UEBERNAHME_ARTEN: () => UEBERNAHME_ARTEN,
    VOM_FOLD_AUF_BESTAND_BEWAHRT: () => VOM_FOLD_AUF_BESTAND_BEWAHRT,
    VOM_FOLD_BEI_NEUANLAGE_GESETZT: () => VOM_FOLD_BEI_NEUANLAGE_GESETZT,
    VOM_FOLD_VERWALTET: () => VOM_FOLD_VERWALTET,
    checklistePunkte: () => checklistePunkte,
    claimZustand: () => claimZustand,
    claimZustandFuerUebernahme: () => claimZustandFuerUebernahme,
    effortKeineZahl: () => effortKeineZahl,
    effortTypMeldung: () => effortTypMeldung,
    fehlendeMassstabFelder: () => fehlendeMassstabFelder,
    feldGeaendertMeldung: () => feldGeaendertMeldung,
    fold: () => fold,
    halterEinwand: () => halterEinwand,
    halterWieErwartet: () => halterWieErwartet,
    inspectOpPayload: () => inspectOpPayload,
    istTerminDatum: () => istTerminDatum,
    istTerminZeit: () => istTerminZeit,
    istVertraulich: () => istVertraulich,
    makeOp: () => makeOp,
    opZeitMs: () => opZeitMs,
    orderedOps: () => orderedOps,
    regelVerwerfung: () => regelVerwerfung,
    stabil: () => stabil,
    tsSchluessel: () => tsSchluessel,
    uebernahmeMeldung: () => uebernahmeMeldung,
    validateOp: () => validateOp,
    verloreneFelder: () => verloreneFelder,
    zielSwimlaneNeueKarte: () => zielSwimlaneNeueKarte
  });

  // src/awaiting.js
  var AWAITING_PERSON_MAX = 200;
  var AWAITING_FRAGE_MAX = 2000;
  var AWAITING_FELD_MAX = 200;
  var PROTOTYP = new Set(["__proto__", "constructor", "prototype"]);
  var AWAITING_SITZUNG_TRENNER = " · ";
  var SITZUNG_MUSTER = /^[A-Za-z0-9._:@-]{1,200}$/;
  function awaitingSitzungGueltig(session) {
    return typeof session === "string" && SITZUNG_MUSTER.test(session);
  }
  function awaitingSitzungsSchluessel(person, session) {
    return person + AWAITING_SITZUNG_TRENNER + session;
  }
  var AWAITING_SITZUNGEN_MAX = 10;
  function kappeAwaitingSitzungen(abbildung, person, schonen = null) {
    const eigene = Object.keys(abbildung).filter((k) => k !== schonen && abbildung[k] && abbildung[k].session !== undefined && abbildung[k].person === person).sort((a, b) => {
      const sa = abbildung[a].since, sb = abbildung[b].since;
      return sa < sb ? -1 : sa > sb ? 1 : a < b ? -1 : a > b ? 1 : 0;
    });
    const platz = AWAITING_SITZUNGEN_MAX - (schonen !== null && abbildung[schonen] ? 1 : 0);
    for (let i = 0;i < eigene.length - platz; i++)
      delete abbildung[eigene[i]];
  }
  function awaitingPersonGueltig(person) {
    return typeof person === "string" && person.trim() !== "" && [...person].length <= AWAITING_PERSON_MAX && !PROTOTYP.has(person) && !person.includes(AWAITING_SITZUNG_TRENNER);
  }
  function istAwaitingEintrag(e) {
    return !!e && typeof e === "object" && !Array.isArray(e) && typeof e.question === "string" && e.question.length > 0 && [...e.question].length <= AWAITING_FRAGE_MAX && typeof e.since === "string" && e.since.length > 0 && [...e.since].length <= AWAITING_FELD_MAX && typeof e.askedBy === "string" && [...e.askedBy].length <= AWAITING_FELD_MAX && (e.session === undefined ? e.person === undefined : awaitingSitzungGueltig(e.session) && awaitingPersonGueltig(e.person));
  }
  function schluesselPasst(k, e) {
    if (e.session !== undefined)
      return k === awaitingSitzungsSchluessel(e.person, e.session);
    return awaitingPersonGueltig(k);
  }
  function istAlteAwaitingForm(v) {
    return !!v && typeof v === "object" && !Array.isArray(v) && typeof v.question === "string";
  }
  function standardPerson(board) {
    const ids = Array.isArray(board?.identities) ? board.identities : [];
    for (const i of ids) {
      if (i && i.kind === "user" && typeof i.label === "string" && awaitingPersonGueltig(i.label.trim())) {
        return i.label.trim();
      }
    }
    return "User";
  }
  function awaitingAlsAbbildung(v, board) {
    const out = {};
    if (!v || typeof v !== "object" || Array.isArray(v))
      return out;
    if (istAlteAwaitingForm(v)) {
      if (istAwaitingEintrag(v))
        out[standardPerson(board)] = v;
      return out;
    }
    for (const k of Object.keys(v)) {
      if (!PROTOTYP.has(k) && istAwaitingEintrag(v[k]) && schluesselPasst(k, v[k]))
        out[k] = v[k];
    }
    return out;
  }
  function awaitingOderNull(abbildung) {
    return Object.keys(abbildung).length ? abbildung : null;
  }

  // src/finding.js
  var FINDING_KIND = "finding";
  var FINDING_TYPEN = /* @__PURE__ */ Object.freeze(["S", "K", "B", "V", "D"]);
  var FINDING_PRIORITAETEN = /* @__PURE__ */ Object.freeze(["P0", "P1", "P2", "P3"]);
  var FINDING_STATUS = /* @__PURE__ */ Object.freeze([
    "offen",
    "sofort",
    "eingeplant",
    "backlog",
    "teilweise behoben",
    "behoben",
    "akzeptiert",
    "behandelt",
    "aufgelöst",
    "hinfällig"
  ]);
  var FINDING_STATUS_OFFEN = /* @__PURE__ */ Object.freeze(FINDING_STATUS.slice(0, 5));
  var FINDING_FELDER = /* @__PURE__ */ Object.freeze(["nr", "typ", "prioritaet", "beleg", "lehre", "status"]);
  function nextFindingNr(board) {
    let max = 0;
    for (const s of board.sprints || [])
      for (const c of s.cards || []) {
        const nr = c && c.kind === FINDING_KIND && c.finding ? c.finding.nr : null;
        if (Number.isInteger(nr) && nr > max)
          max = nr;
      }
    if (!Number.isInteger(board.nextFindingNr) || board.nextFindingNr <= max)
      board.nextFindingNr = max + 1;
    return board.nextFindingNr++;
  }
  var WORTSCHATZ = {
    typ: FINDING_TYPEN,
    prioritaet: FINDING_PRIORITAETEN,
    status: FINDING_STATUS
  };
  function findingFehler(wert) {
    if (wert == null) {
      return { feld: "finding", text: "Eine Finding-Karte braucht `finding` — ein Objekt" + " { typ, prioritaet, status, beleg?, lehre? }." };
    }
    if (typeof wert !== "object" || Array.isArray(wert)) {
      return { feld: "finding", text: `\`finding\` ist ein Objekt, nicht ${JSON.stringify(wert)}.` };
    }
    for (const k of Object.keys(wert)) {
      if (!FINDING_FELDER.includes(k)) {
        return {
          feld: `finding.${k}`,
          unbekannt: k,
          text: `\`finding.${k}\` gibt es nicht. Erlaubt sind: ${FINDING_FELDER.join(", ")}.`
        };
      }
    }
    for (const feld of ["typ", "prioritaet", "status"]) {
      const v = wert[feld];
      const erlaubt = WORTSCHATZ[feld];
      if (v == null) {
        return { feld: `finding.${feld}`, text: `\`finding.${feld}\` fehlt. Erlaubt: ${erlaubt.join(", ")}.` };
      }
      if (!erlaubt.includes(v)) {
        let text = `\`finding.${feld}\` ist ${JSON.stringify(v)} — erlaubt: ${erlaubt.join(", ")}.`;
        const wort = typeof v === "string" ? erlaubt.find((w) => v.startsWith(w + " ")) : null;
        if (feld === "status" && wort) {
          text += ` Ohne Zusatz schreiben („${wort}“); den Sprint zeigt die Swimlane der Karte,` + " Einzelheiten gehören in Beschreibung oder Kommentar.";
        }
        return { feld: `finding.${feld}`, text };
      }
    }
    for (const feld of ["beleg", "lehre"]) {
      const v = wert[feld];
      if (v != null && typeof v !== "string") {
        return { feld: `finding.${feld}`, text: `\`finding.${feld}\` ist Text (Zeichenkette) oder fehlt.` };
      }
    }
    return null;
  }

  // src/ops.js
  var OP_SCHEMA_VERSION = 1;
  var PROJECTION_FILE = "aboard-sync.json";
  var BASE_FILE = "aboard-sync.base.json";
  var INBOX_DIR = "aboard-sync.inbox";
  var REJECTED_DIR = "aboard-sync.inbox/.rejected";
  var HEARTBEAT_FILE = "aboard-sync.inbox/.heartbeat.json";
  var REBASE_MARKE = ".rebase-marke.json";
  var FOLD_MARKE = ".fold-marke.json";
  var OP_TYPES = /* @__PURE__ */ Object.freeze([
    "card.upsert",
    "card.move",
    "card.delete",
    "card.field",
    "comment.add",
    "comment.delete",
    "comment.edit",
    "card.fieldIf",
    "card.listAdd",
    "card.listRemove",
    "card.claim",
    "card.release",
    "swimlane.upsert",
    "swimlane.move",
    "swimlane.delete",
    "board.field",
    "board.awaiting",
    "termin.upsert",
    "termin.delete",
    "card.moveIf",
    "card.checkItemIf",
    "comment.editIf",
    "termin.fieldIf",
    "maintenance.compact",
    "maintenance.diagnostic"
  ]);
  var CARD_FIELD_BLOCKLIST = new Set([
    "id",
    "displayId",
    "__proto__",
    "constructor",
    "prototype",
    "claim",
    "spiegel",
    "boardpaarUrsprung"
  ]);
  var VOM_FOLD_VERWALTET = /* @__PURE__ */ Object.freeze(["claim", "spiegel", "boardpaarUrsprung"]);
  var CLAIM_LEASE_STANDARD = 7200;
  var CLAIM_LEASE_MIN = 60;
  var CLAIM_LEASE_MAX = 86400;
  var CLAIM_SESSION_MAX = 200;
  var ERLEDIGT_SPALTE = "col-erledigt";
  function claimZustand(card, jetztMs, actor, session) {
    const c = card && card.claim;
    if (!c || typeof c !== "object")
      return "frei";
    if (c.by === (actor || "") && (c.session || null) === (session || null))
      return "eigen";
    if (card.columnId === ERLEDIGT_SPALTE)
      return "erloschen";
    const ende = tsSchluessel(c.until);
    if (!ende || !Number.isFinite(jetztMs))
      return "fremd-aktiv";
    return jetztMs < ende.ms ? "fremd-aktiv" : "fremd-abgelaufen";
  }
  function claimZustandFuerUebernahme(card, jetztMs, actor, session) {
    const z = claimZustand(card, jetztMs, actor, session);
    if (z !== "erloschen")
      return z;
    const ende = tsSchluessel(card.claim.until);
    if (!ende || !Number.isFinite(jetztMs))
      return "fremd-aktiv";
    return jetztMs < ende.ms ? "fremd-aktiv" : "fremd-abgelaufen";
  }
  var UEBERNAHME_ARTEN = /* @__PURE__ */ Object.freeze(["beansprucht", "nicht-halter", "uhr-unsicher", "halter-geaendert"]);
  function halterWieErwartet(claim, expect) {
    if (!claim || typeof claim !== "object")
      return false;
    const n = (v) => v === undefined ? null : v;
    return n(claim.by) === n(expect.by) && n(claim.session) === n(expect.session) && n(claim.since) === n(expect.since);
  }
  function halterEinwand(projektion, cardId, erwartet) {
    const loc = projektion && projektion.board ? findCard(projektion.board, cardId) : null;
    const karte = loc && loc.card;
    if (!karte || halterWieErwartet(karte.claim, erwartet))
      return null;
    return { halterAktuell: karte.claim ? clone(karte.claim) : null };
  }
  function feldGeaendertMeldung(k) {
    return `Op ${k.op} (${k.typ}): Karte ${k.cardId}, Feld „${k.feld}“ hatte nicht den erwarteten Wert` + ` (aktuell ${JSON.stringify(k.aktuell ?? null)}) — die Änderung hat nichts bewirkt.`;
  }
  function effortKeineZahl(wert) {
    if (wert === undefined || wert === null)
      return false;
    return !(typeof wert === "number" && Number.isFinite(wert) && wert >= 0);
  }
  function effortTypMeldung(k) {
    return `Op ${k.op} (${k.typ}): Karte ${k.cardId}, effort ist ${k.wert} (${k.typname}) und keine Zahl >= 0 ` + "(ADR-0026: Stunden als Zahl, z. B. 0.5 oder 4, oder null) — die Op wirkt trotzdem. " + "Summen (src/metrics.js) ignorieren den Wert. Korrektur: " + `\`node tools/aboard-op.mjs card.field --cardId ${k.cardId} --field effort --value <zahl> --actor "<wer>"\` ` + "(in anderen Projekten: `node ~/.aboard-framework/tools/aboard-op.mjs … --dir .`).";
  }
  function uebernahmeMeldung(k) {
    const halter = `${k.halter || "?"}${k.session ? ` (Sitzung ${k.session})` : ""}`;
    const bis = k.until ? ` bis ${k.until}` : "";
    if (k.art === "beansprucht") {
      return `Op ${k.op} (${k.typ}): Karte ${k.cardId} hält ${halter}${bis} — die Übernahme hat nichts bewirkt.`;
    }
    if (k.art === "halter-geaendert") {
      return k.halter ? `Op ${k.op} (${k.typ}): Karte ${k.cardId} hält inzwischen ${halter}${bis}, nicht der erwartete Halter — nichts aufgehoben.` : `Op ${k.op} (${k.typ}): Karte ${k.cardId} ist nicht mehr übernommen — nichts aufgehoben.`;
    }
    if (k.art === "nicht-halter") {
      return `Op ${k.op} (${k.typ}): Karte ${k.cardId} hält ${halter}${bis} — nur der Halter gibt frei (oder ein Mensch mit erzwingen).`;
    }
    if (!k.halter) {
      return `Op ${k.op} (${k.typ}): Karte ${k.cardId} — Systemuhr nicht gestellt, keine Übernahme möglich (das Lease-Ende wäre falsch).`;
    }
    return `Op ${k.op} (${k.typ}): Karte ${k.cardId} — Systemuhr nicht gestellt, die Übernahme von ${halter} bleibt bestehen.`;
  }
  function opZeitMs(op) {
    const k = tsSchluessel(op && op.ts);
    return k ? k.ms : NaN;
  }
  function leaseSekunden(wert) {
    if (typeof wert !== "number" || !Number.isFinite(wert))
      return CLAIM_LEASE_STANDARD;
    return Math.min(CLAIM_LEASE_MAX, Math.max(CLAIM_LEASE_MIN, Math.round(wert)));
  }
  var BOARD_FIELD_ALLOWLIST = new Set(["title", "columnDefs", "templates", "background", "identities", "attachments"]);
  var MENGEN_FELDER = /* @__PURE__ */ Object.freeze(["assignees", "labels", "links"]);
  var CARD_ARRAY_FIELDS = /* @__PURE__ */ Object.freeze([
    "comments",
    "links",
    "labels",
    "assignees",
    "attachments",
    "activity"
  ]);
  var _seq = 0;
  function uuid() {
    const g = typeof globalThis !== "undefined" ? globalThis : {};
    if (g.crypto && typeof g.crypto.randomUUID === "function")
      return g.crypto.randomUUID();
    return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
      const r = Math.floor(Math.random() * 16);
      const v = c === "x" ? r : r & 3 | 8;
      return v.toString(16);
    });
  }
  function makeOp(type, payload, actor) {
    return {
      id: uuid(),
      ts: new Date().toISOString(),
      seq: _seq++,
      actor: actor || "",
      schemaVersion: OP_SCHEMA_VERSION,
      type,
      payload: payload ?? {}
    };
  }
  var OP_PAYLOAD_SPEC = /* @__PURE__ */ Object.freeze({
    "card.upsert": { required: ["card"], known: ["card", "sprintId", "columnId", "targetIndex", "anchor"] },
    "card.move": { required: ["cardId", "toSprintId"], known: ["cardId", "toSprintId", "toColId", "targetIndex", "anchor"] },
    "card.delete": { required: ["cardId"], known: ["cardId"] },
    "card.field": { required: ["cardId", "field"], known: ["cardId", "field", "value"] },
    "card.fieldIf": { required: ["cardId", "field"], known: ["cardId", "field", "value", "expect"] },
    "card.listAdd": { required: ["cardId", "field", "item"], known: ["cardId", "field", "item"] },
    "card.listRemove": { required: ["cardId", "field", "item"], known: ["cardId", "field", "item"] },
    "card.claim": { required: ["cardId"], known: ["cardId", "leaseSeconds", "session", "onBehalfOf"] },
    "card.release": { required: ["cardId"], known: ["cardId", "session", "erzwingen", "expect"] },
    "comment.add": { required: ["cardId", "comment"], known: ["cardId", "comment"] },
    "comment.delete": { required: ["cardId", "commentId"], known: ["cardId", "commentId"] },
    "comment.edit": { required: ["cardId", "commentId", "text"], known: ["cardId", "commentId", "text", "edited"] },
    "swimlane.upsert": { required: ["sprint"], known: ["sprint"] },
    "swimlane.move": { required: ["sprintId"], known: ["sprintId", "targetIndex"] },
    "swimlane.delete": { required: ["sprintId"], known: ["sprintId"] },
    "board.field": { required: ["field"], known: ["field", "value"] },
    "board.awaiting": { required: ["person"], known: ["person", "value", "session"] },
    "termin.upsert": { required: ["termin"], known: ["termin"] },
    "termin.delete": { required: ["terminId"], known: ["terminId"] },
    "card.moveIf": {
      required: ["cardId", "toSprintId", "toColId", "expectSprintId", "expectColId"],
      known: ["cardId", "toSprintId", "toColId", "expectSprintId", "expectColId", "targetIndex", "anchor"]
    },
    "card.checkItemIf": {
      required: ["cardId", "text", "vorkommen", "erledigt"],
      known: ["cardId", "text", "vorkommen", "erledigt", "expect"]
    },
    "comment.editIf": {
      required: ["cardId", "commentId", "text"],
      known: ["cardId", "commentId", "text", "edited", "expect"]
    },
    "termin.fieldIf": { required: ["terminId", "field"], known: ["terminId", "field", "value", "expect"] },
    "maintenance.compact": { required: [], known: [] },
    "maintenance.diagnostic": { required: [], known: ["reason", "cardDeletes", "swimlaneDeletes"] }
  });
  function nearestKnownKey(key, known) {
    const k = String(key).toLowerCase();
    let best = null;
    let bestDist = Infinity;
    for (const cand of known) {
      const c = cand.toLowerCase();
      const limit = Math.max(2, Math.floor(Math.max(k.length, c.length) / 2));
      const d = _levenshtein(k, c);
      if (d <= limit && d < bestDist) {
        bestDist = d;
        best = cand;
      }
    }
    return best;
  }
  function _levenshtein(a, b) {
    if (a === b)
      return 0;
    let prev = Array.from({ length: b.length + 1 }, (_, i) => i);
    for (let i = 1;i <= a.length; i++) {
      const cur = [i];
      for (let j = 1;j <= b.length; j++) {
        cur[j] = Math.min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1));
      }
      prev = cur;
    }
    return prev[b.length];
  }
  function inspectOpPayload(op) {
    const spec = op && OP_PAYLOAD_SPEC[op.type];
    const p = op && op.payload;
    if (!spec || p == null || typeof p !== "object")
      return { missing: [], unknown: [] };
    const missing = spec.required.filter((k) => p[k] == null);
    const knownSet = new Set(spec.known);
    const unknown = Object.keys(p).filter((k) => !knownSet.has(k)).map((k) => ({ key: k, meant: nearestKnownKey(k, spec.known) }));
    return { missing, unknown };
  }
  function validateOp(op) {
    if (!op || typeof op !== "object")
      return false;
    if (typeof op.id !== "string" || !op.id)
      return false;
    if (typeof op.ts !== "string" || !op.ts)
      return false;
    if (!OP_TYPES.includes(op.type))
      return false;
    if (op.schemaVersion !== OP_SCHEMA_VERSION)
      return false;
    if (op.payload == null || typeof op.payload !== "object")
      return false;
    if (inspectOpPayload(op).missing.length > 0)
      return false;
    return true;
  }
  function stabil(v) {
    if (v === undefined)
      return "\x00undefined";
    if (typeof v === "number" && Number.isNaN(v))
      return "\x00NaN";
    if (v === null || typeof v !== "object")
      return JSON.stringify(v);
    if (Array.isArray(v))
      return "[" + v.map(stabil).join(",") + "]";
    return "{" + Object.keys(v).sort().filter((k) => v[k] !== undefined).map((k) => JSON.stringify(k) + ":" + stabil(v[k])).join(",") + "}";
  }
  function clone(o) {
    if (typeof structuredClone === "function")
      return structuredClone(o);
    return JSON.parse(JSON.stringify(o));
  }
  var TS_MUSTER = /^(\d{4})-(\d{2})-(\d{2})[Tt ](\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(?:[Zz]|([+-])(\d{2}):?(\d{2}))$/;
  function tsSchluessel(ts) {
    if (typeof ts !== "string")
      return null;
    const m = TS_MUSTER.exec(ts);
    if (!m)
      return null;
    const [, jahr, monat, tag, std, min, sek, bruch, vz, offStd, offMin] = m;
    let ms = Date.UTC(+jahr, +monat - 1, +tag, +std, +min, +sek, 0);
    if (!Number.isFinite(ms))
      return null;
    if (+jahr >= 0 && +jahr <= 99) {
      const d = new Date(ms);
      d.setUTCFullYear(+jahr);
      ms = d.getTime();
      if (!Number.isFinite(ms))
        return null;
    }
    if (vz) {
      const offset = (+offStd * 60 + +offMin) * 60000;
      ms += vz === "-" ? offset : -offset;
    }
    const nk = bruch || "";
    ms += Number((nk + "000").slice(0, 3));
    const sub = Number((nk.slice(3) + "000000").slice(0, 6));
    return { ms, sub };
  }
  function orderedOps(ops) {
    const dekoriert = [...ops].map((op) => ({
      op,
      k: tsSchluessel(op && op.ts),
      roh: op && typeof op.ts === "string" ? op.ts : "",
      seq: op && Number.isFinite(op.seq) ? op.seq : 0,
      id: op && typeof op.id === "string" ? op.id : ""
    }));
    dekoriert.sort((a, b) => {
      if (a.k && b.k) {
        if (a.k.ms !== b.k.ms)
          return a.k.ms - b.k.ms;
        if (a.k.sub !== b.k.sub)
          return a.k.sub - b.k.sub;
      } else if (a.k || b.k) {
        return a.k ? -1 : 1;
      } else if (a.roh !== b.roh) {
        return a.roh < b.roh ? -1 : 1;
      }
      if (a.seq !== b.seq)
        return a.seq - b.seq;
      if (a.id !== b.id)
        return a.id < b.id ? -1 : 1;
      return 0;
    });
    return dekoriert.map((e) => e.op);
  }
  function findCard(board, cardId) {
    for (const sprint of board.sprints || []) {
      const idx = (sprint.cards || []).findIndex((c) => c.id === cardId);
      if (idx !== -1)
        return { sprint, idx, card: sprint.cards[idx] };
    }
    return null;
  }
  function backlogSprint(board) {
    const sprints = board.sprints || [];
    return sprints.find((s) => s && s.id === "__backlog__") || sprints.find((s) => s && s.kind === "backlog" && s.archived !== true) || null;
  }
  function zielSwimlaneNeueKarte(board, payload) {
    const p = payload || {};
    return (board?.sprints || []).find((s) => s && s.id === p.sprintId) || backlogSprint(board || {});
  }
  function nextDisplayId(board) {
    if (!Number.isInteger(board.nextDisplayId) || board.nextDisplayId < 1) {
      let max = 0;
      for (const s of board.sprints || [])
        for (const c of s.cards || []) {
          if (Number.isInteger(c.displayId) && c.displayId > max)
            max = c.displayId;
        }
      board.nextDisplayId = max + 1;
    }
    return board.nextDisplayId++;
  }
  function bestandsFindingNr(card) {
    const nr = card && card.finding && typeof card.finding === "object" ? card.finding.nr : undefined;
    return Number.isInteger(nr) ? nr : undefined;
  }
  function findingVorschlag(fehler) {
    if (!fehler.unbekannt)
      return fehler.text;
    const k = String(fehler.unbekannt).toLowerCase();
    const gemeint = nearestKnownKey(fehler.unbekannt, FINDING_FELDER) || (k.length >= 3 ? FINDING_FELDER.find((f) => f.startsWith(k) || k.startsWith(f)) : null);
    return gemeint ? `${fehler.text} Meintest du \`${gemeint}\`?` : fehler.text;
  }
  function findingVerwerfung(board, op) {
    const p = op && op.payload || {};
    const ablehnen = (grund, cardId, feld, text) => ({ grund, feld, cardId, text });
    if (op.type === "card.upsert") {
      const karte = p.card;
      if (!karte || typeof karte !== "object" || typeof karte.id !== "string")
        return null;
      const loc = findCard(board, karte.id);
      const bestandIstFinding = !!loc && loc.card.kind === FINDING_KIND;
      const kindDanach = karte.kind != null ? karte.kind : loc ? loc.card.kind : karte.kind;
      const wirdFinding = kindDanach === FINDING_KIND;
      if (loc && bestandIstFinding !== wirdFinding) {
        return ablehnen("finding-umwidmung", karte.id, "kind", bestandIstFinding ? `Karte ${karte.id} ist ein Finding — es bleibt eins (die Nummer gehört ihr). Wer es` + " zurückziehen will, setzt `finding.status` auf „hinfällig“." : `Karte ${karte.id} besteht schon als normale Karte. Ein Finding entsteht nur bei` + " Neuanlage (`card.upsert` mit neuer Karten-ID und `kind: 'finding'`) — sonst" + " entstünden Lücken oder doppelte Nummern.");
      }
      if (!wirdFinding) {
        if (karte.finding != null) {
          return ablehnen("finding-ohne-kind", karte.id, "finding", "`finding` trägt nur eine Karte mit `kind: 'finding'`.");
        }
        return null;
      }
      const fehler = findingFehler(karte.finding);
      if (fehler)
        return ablehnen("finding-wert-ungueltig", karte.id, fehler.feld, findingVorschlag(fehler));
      if (loc && karte.finding.nr != null && karte.finding.nr !== bestandsFindingNr(loc.card)) {
        return ablehnen("finding-nr-gesperrt", karte.id, "finding.nr", `\`finding.nr\` vergibt der Fold; Karte ${karte.id} hat ${JSON.stringify(bestandsFindingNr(loc.card) ?? null)},` + ` nicht ${JSON.stringify(karte.finding.nr)}. \`nr\` weglassen oder unverändert lassen.`);
      }
      return null;
    }
    const loc = findCard(board, p.cardId);
    const istFinding = !!loc && loc.card.kind === FINDING_KIND;
    if (p.field === "kind") {
      if (p.value === FINDING_KIND && !istFinding) {
        return ablehnen("finding-umwidmung", p.cardId, "kind", "Eine bestehende Karte wird nicht nachträglich zum Finding — ein Finding entsteht nur" + " bei Neuanlage (`card.upsert` mit `kind: 'finding'`), sonst entstünden Lücken oder" + " doppelte Nummern.");
      }
      if (istFinding && p.value !== FINDING_KIND) {
        return ablehnen("finding-umwidmung", p.cardId, "kind", `Karte ${p.cardId} ist ein Finding — es bleibt eins (die Nummer gehört ihr). Wer es` + " zurückziehen will, setzt `finding.status` auf „hinfällig“.");
      }
      return null;
    }
    if (p.field !== "finding")
      return null;
    if (loc && !istFinding) {
      if (p.value == null)
        return null;
      return ablehnen("finding-ohne-kind", p.cardId, "finding", "`finding` trägt nur eine Karte mit `kind: 'finding'`; eine bestehende Karte wird nicht" + " nachträglich zum Finding.");
    }
    if (!loc && p.value == null)
      return null;
    const fehler = findingFehler(p.value);
    if (fehler)
      return ablehnen("finding-wert-ungueltig", p.cardId, fehler.feld, findingVorschlag(fehler));
    if (loc && p.value.nr != null && p.value.nr !== bestandsFindingNr(loc.card)) {
      return ablehnen("finding-nr-gesperrt", p.cardId, "finding.nr", `\`finding.nr\` vergibt der Fold; Karte ${p.cardId} hat ${JSON.stringify(bestandsFindingNr(loc.card) ?? null)},` + ` nicht ${JSON.stringify(p.value.nr)}. \`nr\` weglassen oder unverändert lassen.`);
    }
    return null;
  }
  function klemmeSpalte(board, colId, protokoll, kontext) {
    const defs = Array.isArray(board?.columnDefs) ? board.columnDefs : [];
    if (defs.length === 0)
      return colId;
    if (defs.some((d) => d?.id === colId))
      return colId;
    const ziel = defs.some((d) => d?.id === "col-backlog") ? "col-backlog" : defs[0]?.id ?? colId;
    if (Array.isArray(protokoll))
      protokoll.push({ art: "klemmung", ...kontext, von: colId, nach: ziel });
    return ziel;
  }
  function insertCard(sprint, card, toColId, targetIndex, anchor) {
    card.columnId = toColId;
    if (anchor && typeof anchor === "object") {
      if (anchor.mode === "end") {
        sprint.cards.push(card);
        return;
      }
      if ((anchor.mode === "after" || anchor.mode === "before") && anchor.cardId != null) {
        const a = sprint.cards.find((c) => c.id === anchor.cardId && c.columnId === toColId);
        if (a) {
          const absIdx = sprint.cards.indexOf(a);
          sprint.cards.splice(anchor.mode === "after" ? absIdx + 1 : absIdx, 0, card);
          return;
        }
      }
    }
    if (Number.isInteger(targetIndex) && targetIndex >= 0) {
      const colCards = sprint.cards.filter((c) => c.columnId === toColId);
      if (targetIndex < colCards.length) {
        const at = colCards[targetIndex];
        const absIdx = sprint.cards.indexOf(at);
        sprint.cards.splice(absIdx, 0, card);
        return;
      }
    }
    sprint.cards.push(card);
  }
  var TERMIN_HERKUNFT = /* @__PURE__ */ Object.freeze(["erstellt", "erstelltVon"]);
  var TERMIN_VERWALTET = /* @__PURE__ */ Object.freeze(["spiegel", "boardpaarUrsprung"]);
  function spaltenMarkenBewahren(alt, neu) {
    if (!Array.isArray(neu))
      return;
    const marken = new Map;
    for (const d of Array.isArray(alt) ? alt : []) {
      if (d && typeof d === "object" && typeof d.id === "string" && d.spiegel !== undefined)
        marken.set(d.id, d.spiegel);
    }
    for (const d of neu) {
      if (!d || typeof d !== "object")
        continue;
      if (marken.has(d.id))
        d.spiegel = clone(marken.get(d.id));
      else
        delete d.spiegel;
    }
  }
  var TERMIN_TEXTFELDER = /* @__PURE__ */ Object.freeze(["ort", "beschreibung", "karteId", "erstellt", "erstelltVon", "boardpaarUrsprung"]);
  var TERMIN_FELDLISTE = /* @__PURE__ */ Object.freeze(["titel", "beschreibung", "datum", "bisDatum", "von", "bis", "ort", "personen", "karteId"]);
  function checklistePunkte(beschreibung) {
    const punkte = [];
    if (typeof beschreibung !== "string")
      return punkte;
    const zeilen = beschreibung.split(`
`);
    let zaun = null;
    const gezaehlt = new Map;
    for (let i = 0;i < zeilen.length; i++) {
      const zeile = zeilen[i].endsWith("\r") ? zeilen[i].slice(0, -1) : zeilen[i];
      const z = /^ {0,3}(`{3,}|~{3,})/.exec(zeile);
      if (z) {
        if (zaun === null)
          zaun = z[1][0];
        else if (z[1][0] === zaun)
          zaun = null;
        continue;
      }
      if (zaun !== null)
        continue;
      const m = /^- \[( |x|X)\] ([^\n]*)$/.exec(zeile);
      if (!m)
        continue;
      const vorkommen = (gezaehlt.get(m[2]) || 0) + 1;
      gezaehlt.set(m[2], vorkommen);
      punkte.push({ text: m[2], vorkommen, erledigt: m[1] !== " ", zeile: i });
    }
    return punkte;
  }
  function setzeChecklistePunkt(beschreibung, zeile, erledigt) {
    const zeilen = beschreibung.split(`
`);
    zeilen[zeile] = "- [" + (erledigt ? "x" : " ") + "]" + zeilen[zeile].slice(5);
    return zeilen.join(`
`);
  }
  function istTerminDatum(s) {
    if (typeof s !== "string" || !/^\d{4}-\d{2}-\d{2}$/.test(s))
      return false;
    const [y, m, d] = s.split("-").map(Number);
    const t = new Date(Date.UTC(y, m - 1, d));
    return t.getUTCFullYear() === y && t.getUTCMonth() === m - 1 && t.getUTCDate() === d;
  }
  function istTerminZeit(s) {
    return typeof s === "string" && /^([01]\d|2[0-3]):[0-5]\d$/.test(s);
  }
  function terminVerwerfung(board, op) {
    const p = op.payload || {};
    const nein = (grund, text) => ({ grund, terminId: typeof p.terminId === "string" ? p.terminId : p.termin && typeof p.termin.id === "string" ? p.termin.id : undefined, text });
    if (board.termine != null && !Array.isArray(board.termine)) {
      return nein("termine-kein-array", "`board.termine` ist keine Liste — ein Termin-Op würde den Bestand" + " überschreiben. Erst den Board-Zustand reparieren.");
    }
    if (op.type === "termin.delete") {
      return typeof p.terminId === "string" && p.terminId ? null : nein("termin-id-ungueltig", "`terminId` ist eine nicht leere Zeichenkette.");
    }
    const t = p.termin;
    if (!t || typeof t !== "object" || Array.isArray(t)) {
      return nein("termin-kein-objekt", "`termin` ist ein Objekt { id, titel, datum, … } — der ganze Termin.");
    }
    if (typeof t.id !== "string" || !t.id)
      return nein("termin-id-ungueltig", "`termin.id` ist eine nicht leere Zeichenkette.");
    if (typeof t.titel !== "string" || !t.titel.trim())
      return nein("termin-titel-fehlt", "`termin.titel` ist ein nicht leerer Text.");
    if (!istTerminDatum(t.datum))
      return nein("termin-datum-ungueltig", "`termin.datum` ist ein Datum YYYY-MM-DD.");
    if (t.bisDatum != null && !(istTerminDatum(t.bisDatum) && t.bisDatum >= t.datum)) {
      return nein("termin-bisdatum-ungueltig", "`termin.bisDatum` ist ein Datum YYYY-MM-DD, nicht vor `datum` (oder fehlt).");
    }
    for (const k of ["von", "bis"]) {
      if (t[k] != null && !istTerminZeit(t[k]))
        return nein("termin-zeit-ungueltig", `\`termin.${k}\` ist eine Uhrzeit "HH:MM" (oder fehlt).`);
    }
    for (const k of TERMIN_TEXTFELDER) {
      if (t[k] != null && typeof t[k] !== "string")
        return nein("termin-feld-ungueltig", `\`termin.${k}\` ist Text (oder fehlt).`);
    }
    if (t.personen != null && !(Array.isArray(t.personen) && t.personen.every((x) => typeof x === "string"))) {
      return nein("termin-feld-ungueltig", "`termin.personen` ist eine Liste von Zeichenketten (oder fehlt).");
    }
    return null;
  }
  function regelVerwerfung(board, op) {
    const p = op && op.payload || {};
    if (op && op.type === "card.fieldIf" && !("expect" in p)) {
      return {
        grund: "erwartung-fehlt",
        feld: p.field,
        cardId: p.cardId,
        text: "card.fieldIf ohne `expect` — ohne erwarteten Vorwert gibt es nichts zu prüfen." + " Für eine bedingungslose Änderung card.field verwenden; `expect: null` erwartet" + " ausdrücklich den Wert null."
      };
    }
    if (op && op.type === "card.moveIf") {
      for (const k of ["toSprintId", "toColId", "expectSprintId", "expectColId"]) {
        if (typeof p[k] !== "string" || !p[k]) {
          return {
            grund: "ort-ungueltig",
            cardId: p.cardId,
            text: `card.moveIf: \`${k}\` ist eine nicht leere Zeichenkette (Ziel und erwarteter Ort sind Pflicht).`
          };
        }
      }
      return null;
    }
    if (op && op.type === "card.checkItemIf") {
      if (typeof p.text !== "string" || !Number.isInteger(p.vorkommen) || p.vorkommen < 1 || typeof p.erledigt !== "boolean" || typeof p.expect !== "boolean") {
        return {
          grund: "checkpunkt-ungueltig",
          cardId: p.cardId,
          text: "card.checkItemIf: `text` ist eine Zeichenkette, `vorkommen` eine ganze Zahl ab 1," + " `erledigt` und `expect` sind true oder false."
        };
      }
      return null;
    }
    if (op && op.type === "comment.editIf") {
      if (typeof p.commentId !== "string" || !p.commentId || typeof p.text !== "string" || typeof p.expect !== "string") {
        return {
          grund: "kommentar-ungueltig",
          cardId: p.cardId,
          text: "comment.editIf: `commentId` ist eine nicht leere Zeichenkette, `text` und `expect` (der Text," + " den der Schreiber gesehen hat) sind Zeichenketten."
        };
      }
      return null;
    }
    if (op && op.type === "termin.fieldIf") {
      if (!("expect" in p)) {
        return {
          grund: "erwartung-fehlt",
          feld: p.field,
          terminId: p.terminId,
          text: "termin.fieldIf ohne `expect` — `expect: null` erwartet ausdrücklich ein leeres Feld."
        };
      }
      if (!TERMIN_FELDLISTE.includes(p.field)) {
        return {
          grund: "termin-feld-nicht-erlaubt",
          feld: p.field,
          terminId: p.terminId,
          text: `\`${p.field}\` ist über termin.fieldIf nicht setzbar. Erlaubt sind: ${TERMIN_FELDLISTE.join(", ")}.`
        };
      }
      if (board.termine != null && !Array.isArray(board.termine)) {
        return terminVerwerfung(board, { type: "termin.upsert", payload: { termin: null } });
      }
      const alt = (board.termine || []).find((x) => x && x.id === p.terminId);
      if (alt) {
        const neu = { ...alt };
        if (p.value == null)
          delete neu[p.field];
        else
          neu[p.field] = clone(p.value);
        return terminVerwerfung(board, { type: "termin.upsert", payload: { termin: neu } });
      }
      return null;
    }
    if (op && (op.type === "card.upsert" || op.type === "card.field" || op.type === "card.fieldIf")) {
      const befund = findingVerwerfung(board, op);
      if (befund)
        return befund;
    }
    if (op && (op.type === "card.field" || op.type === "card.fieldIf")) {
      if (CARD_FIELD_BLOCKLIST.has(p.field)) {
        return {
          grund: "card-feld-gesperrt",
          feld: p.field,
          cardId: p.cardId,
          text: `\`${p.field}\` ist über card.field nicht setzbar (CARD_FIELD_BLOCKLIST):` + " id und displayId vergibt der Projektor selbst, `claim` setzen nur" + " card.claim/card.release (ADR-0130 §1), die Prototyp-Schlüssel" + " würden statt einer Eigenschaft den Prototyp setzen."
        };
      }
      if (p.field === "archived" && p.value === true) {
        const loc = findCard(board, p.cardId);
        if (loc && loc.card && loc.card.kind === "sprint-meta") {
          return {
            grund: "meta-archivierung",
            feld: "archived",
            cardId: p.cardId,
            text: `Karte ${p.cardId} trägt \`kind: 'sprint-meta'\` — Sprint-Meta-Karten sind` + " nicht archivierbar (ADR-0036), sonst wäre die Sprint-Abschluss-Karte samt" + " Recap für jeden Leser der Projektion weg."
          };
        }
      }
      if (CARD_ARRAY_FIELDS.includes(p.field) && p.value != null && !Array.isArray(p.value)) {
        return {
          grund: "card-array-feld-kein-array",
          feld: p.field,
          cardId: p.cardId,
          text: `\`${p.field}\` ist ein Array-Feld — card.field ersetzt es GANZ, nicht nur` + ` einen Eintrag. Der Wert ${JSON.stringify(p.value)} ist aber kein Array.` + " `applyOp` würde die bestehende Liste lautlos durch diesen einen Wert" + " ersetzen (F-1163); jeder Leser, der ein Array erwartet (Karten-Renderer," + " Filter, Export), stolperte danach über eine Zeichenkette statt einer Liste." + " Ein Array übergeben, z. B. `[]` zum Leeren."
        };
      }
      return null;
    }
    if (op && (op.type === "card.listAdd" || op.type === "card.listRemove")) {
      if (!MENGEN_FELDER.includes(p.field)) {
        return {
          grund: "kein-mengen-feld",
          feld: p.field,
          cardId: p.cardId,
          text: `\`${p.field}\` nimmt keine Mengen-Ops an. Erlaubt sind: ${MENGEN_FELDER.join(", ")}.`
        };
      }
      if (p.field === "links" && !(p.item && typeof p.item === "object" && typeof p.item.type === "string" && typeof p.item.targetId === "string")) {
        return {
          grund: "link-element-ungueltig",
          feld: "links",
          cardId: p.cardId,
          text: "Ein Element von `links` ist ein Objekt mit mindestens `type` und `targetId` als" + " Zeichenketten. Weitere Schlüssel sind erlaubt und zählen beim Vergleich mit."
        };
      }
      return null;
    }
    if (op && (op.type === "card.claim" || op.type === "card.release")) {
      if (p.session != null && (typeof p.session !== "string" || [...p.session].length > CLAIM_SESSION_MAX)) {
        return {
          grund: "session-ungueltig",
          cardId: p.cardId,
          text: `\`session\` ist eine Zeichenkette mit höchstens ${CLAIM_SESSION_MAX} Zeichen.`
        };
      }
      if (op.type === "card.claim") {
        if (p.leaseSeconds != null && !(typeof p.leaseSeconds === "number" && Number.isFinite(p.leaseSeconds))) {
          return {
            grund: "lease-ungueltig",
            cardId: p.cardId,
            text: "`leaseSeconds` ist eine Zahl (Sekunden). Werte ausserhalb von" + ` ${CLAIM_LEASE_MIN}..${CLAIM_LEASE_MAX} werden geklemmt.`
          };
        }
        if (tsSchluessel(op.ts) === null) {
          return {
            grund: "claim-ts-ungueltig",
            cardId: p.cardId,
            text: `Der Zeitstempel ${JSON.stringify(op.ts)} ist nicht deutbar — ohne ihn gibt es` + " kein Lease-Ende."
          };
        }
      }
      if (op.type === "card.release" && p.erzwingen != null && typeof p.erzwingen !== "boolean") {
        return {
          grund: "erzwingen-ungueltig",
          cardId: p.cardId,
          text: "`erzwingen` ist true oder false."
        };
      }
      if (op.type === "card.release" && p.expect != null) {
        const e = p.expect;
        const ok = typeof e === "object" && !Array.isArray(e) && ["by", "session", "since"].every((k) => e[k] == null || typeof e[k] === "string");
        if (!ok) {
          return {
            grund: "expect-ungueltig",
            cardId: p.cardId,
            text: "`expect` ist ein Objekt { by, session, since } — der Halter, den der Schreiber gesehen hat."
          };
        }
      }
      return null;
    }
    if (op && op.type === "board.awaiting") {
      if (!awaitingPersonGueltig(p.person)) {
        return {
          grund: "awaiting-person-ungueltig",
          text: `\`person\` ist eine nicht leere Zeichenkette mit höchstens ${AWAITING_PERSON_MAX} Zeichen,` + " kein Prototyp-Schlüssel (__proto__, constructor, prototype)."
        };
      }
      if (!("value" in p)) {
        return {
          grund: "awaiting-wert-fehlt",
          text: "board.awaiting ohne `value` — zum Löschen `value: null` angeben, sonst löschte" + " eine vergessene Angabe den Eintrag still."
        };
      }
      if (p.value !== null && (!istAwaitingEintrag(p.value) || ("person" in p.value) || ("session" in p.value))) {
        return {
          grund: "awaiting-wert-ungueltig",
          text: "`value` ist `null` (löschen) oder ein Objekt { question, since, askedBy } —" + " question und since nicht leer, askedBy eine Zeichenkette; `person`/`session`" + " gehören in die Op, nicht in den Eintrag."
        };
      }
      if ("session" in p && p.session !== null && p.session !== undefined && !awaitingSitzungGueltig(p.session)) {
        return {
          grund: "awaiting-sitzung-ungueltig",
          text: "`session` ist eine Sitzungskennung aus [A-Za-z0-9._:@-], 1–200 Zeichen (oder weglassen)."
        };
      }
      return null;
    }
    if (op && (op.type === "termin.upsert" || op.type === "termin.delete")) {
      return terminVerwerfung(board, op);
    }
    if (op && op.type === "board.field" && p.field === "awaitingUser") {
      return {
        grund: "board-feld-nicht-erlaubt",
        feld: p.field,
        text: "`awaitingUser` ist über board.field nicht mehr setzbar (ADR-0130 §7): es ist eine" + " Abbildung je Person, und board.field ersetzte sie ganz — zwei Sitzungen, die" + " gleichzeitig fragen, verlören eine Frage. Stattdessen board.awaiting" + " { person, value } verwenden; ein alter Hook muss per install.sh erneuert werden."
      };
    }
    if (op && op.type === "board.field" && !BOARD_FIELD_ALLOWLIST.has(p.field)) {
      return {
        grund: "board-feld-nicht-erlaubt",
        feld: p.field,
        text: `\`${p.field}\` ist über board.field nicht setzbar (BOARD_FIELD_ALLOWLIST).` + ` Erlaubt sind: ${[...BOARD_FIELD_ALLOWLIST].join(", ")}.`
      };
    }
    return null;
  }
  var VOM_FOLD_BEI_NEUANLAGE_GESETZT = /* @__PURE__ */ Object.freeze(["displayId", "columnId"]);
  var VOM_FOLD_AUF_BESTAND_BEWAHRT = /* @__PURE__ */ Object.freeze(["displayId", "columnId", "kind", "metaType", "confidential"]);
  function istVertraulich(obj) {
    return !!obj && typeof obj === "object" && Object.prototype.hasOwnProperty.call(obj, "confidential") && obj.confidential !== false;
  }
  function verloreneFelder(bestand, neu) {
    if (!bestand || typeof bestand !== "object")
      return [];
    const ziel = neu && typeof neu === "object" ? neu : {};
    return Object.keys(bestand).filter((k) => bestand[k] != null && !(k in ziel));
  }
  function fehlendeMassstabFelder(massstab, neueKarte) {
    if (!Array.isArray(massstab) || massstab.length === 0)
      return [];
    return massstab.filter((k) => !VOM_FOLD_BEI_NEUANLAGE_GESETZT.includes(k) && !(k in neueKarte));
  }
  function setzeKartenFeld(card, feld, wert) {
    const nr = feld === "finding" ? bestandsFindingNr(card) : undefined;
    card[feld] = clone(wert);
    if (nr !== undefined && card[feld] && typeof card[feld] === "object")
      card[feld].nr = nr;
  }
  function meldeEffortTyp(protokoll, effortTyp, op, cardId, wert, vorher) {
    if (!effortTyp || !Array.isArray(protokoll))
      return;
    if (!effortKeineZahl(wert))
      return;
    if (stabil(wert) === stabil(vorher))
      return;
    protokoll.push({
      art: "effort-typ",
      op: op.id,
      typ: op.type,
      cardId,
      wert: stabil(wert),
      typname: Array.isArray(wert) ? "Array" : typeof wert
    });
  }
  function applyOp(board, op, protokoll, feldMassstab, effortTyp) {
    const p = op.payload || {};
    const verwurf = regelVerwerfung(board, op);
    if (verwurf) {
      if (Array.isArray(protokoll)) {
        protokoll.push({ art: "wirkungslos", op: op.id, typ: op.type, ...verwurf });
      }
      return;
    }
    switch (op.type) {
      case "card.upsert": {
        if (!p.card || typeof p.card !== "object" || typeof p.card.id !== "string")
          return;
        const existing = findCard(board, p.card.id);
        if (existing) {
          const merged = clone(p.card);
          for (const k of VOM_FOLD_AUF_BESTAND_BEWAHRT) {
            if (merged[k] == null && existing.card[k] != null)
              merged[k] = existing.card[k];
          }
          if (merged.confidential == null && Object.prototype.hasOwnProperty.call(existing.card, "confidential")) {
            merged.confidential = existing.card.confidential;
          }
          merged.displayId = existing.card.displayId;
          if (merged.kind === FINDING_KIND && merged.finding && typeof merged.finding === "object" && bestandsFindingNr(existing.card) !== undefined) {
            merged.finding.nr = bestandsFindingNr(existing.card);
          }
          for (const k of VOM_FOLD_VERWALTET) {
            if (k in existing.card)
              merged[k] = clone(existing.card[k]);
            else
              delete merged[k];
          }
          merged.columnId = klemmeSpalte(board, merged.columnId, protokoll, { op: op.id, typ: op.type, cardId: merged.id });
          if (Array.isArray(protokoll)) {
            const verloren = verloreneFelder(existing.card, merged);
            if (verloren.length) {
              protokoll.push({
                art: "entkernung",
                op: op.id,
                typ: op.type,
                cardId: merged.id,
                felder: verloren
              });
            }
          }
          meldeEffortTyp(protokoll, effortTyp, op, merged.id, merged.effort, existing.card.effort);
          existing.sprint.cards[existing.idx] = merged;
          return;
        }
        const card = clone(p.card);
        for (const k of VOM_FOLD_VERWALTET)
          delete card[k];
        const fehlen = Array.isArray(protokoll) ? fehlendeMassstabFelder(feldMassstab, card) : [];
        card.displayId = nextDisplayId(board);
        const targetSprint = zielSwimlaneNeueKarte(board, p);
        if (!targetSprint) {
          if (Array.isArray(protokoll)) {
            protokoll.push({
              art: "ohne-ziel",
              op: op.id,
              typ: op.type,
              cardId: card.id,
              text: `neue Karte ohne Ziel: Swimlane ${JSON.stringify(p.sprintId ?? null)} gibt es nicht,` + " und das Board hat keinen Backlog (weder `__backlog__` noch eine Swimlane mit" + " `kind: 'backlog'`). Die Karte wurde NICHT angelegt — Op mit gültiger `sprintId`" + " neu schreiben."
            });
          }
          return;
        }
        if (fehlen.length) {
          protokoll.push({
            art: "unvollstaendig",
            op: op.id,
            typ: op.type,
            cardId: card.id,
            felder: fehlen
          });
        }
        meldeEffortTyp(protokoll, effortTyp, op, card.id, card.effort, undefined);
        if (!Array.isArray(targetSprint.cards))
          targetSprint.cards = [];
        if (card.kind === FINDING_KIND)
          card.finding.nr = nextFindingNr(board);
        const colId = klemmeSpalte(board, p.columnId || card.columnId || "col-backlog", protokoll, { op: op.id, typ: op.type, cardId: card.id });
        insertCard(targetSprint, card, colId, p.targetIndex ?? null, p.anchor);
        return;
      }
      case "card.move": {
        const loc = findCard(board, p.cardId);
        if (!loc)
          return;
        const toSprint = (board.sprints || []).find((s) => s.id === p.toSprintId);
        if (!toSprint)
          return;
        const [card] = loc.sprint.cards.splice(loc.idx, 1);
        if (!Array.isArray(toSprint.cards))
          toSprint.cards = [];
        insertCard(toSprint, card, klemmeSpalte(board, p.toColId || card.columnId, protokoll, { op: op.id, typ: op.type, cardId: card.id }), p.targetIndex ?? null, p.anchor);
        return;
      }
      case "card.delete": {
        const loc = findCard(board, p.cardId);
        if (!loc)
          return;
        loc.sprint.cards.splice(loc.idx, 1);
        return;
      }
      case "card.fieldIf": {
        const loc = findCard(board, p.cardId);
        if (!loc)
          return;
        const aktuell = loc.card[p.field] === undefined ? null : loc.card[p.field];
        if (stabil(aktuell) !== stabil(p.expect)) {
          if (Array.isArray(protokoll)) {
            protokoll.push({
              art: "feld-geaendert",
              op: op.id,
              typ: op.type,
              cardId: p.cardId,
              feld: p.field,
              aktuell: clone(aktuell)
            });
          }
          return;
        }
        if (p.field === "columnId") {
          loc.card.columnId = klemmeSpalte(board, clone(p.value), protokoll, { op: op.id, typ: op.type, cardId: p.cardId });
          return;
        }
        if (p.field === "effort")
          meldeEffortTyp(protokoll, effortTyp, op, p.cardId, p.value, loc.card.effort);
        setzeKartenFeld(loc.card, p.field, p.value);
        return;
      }
      case "card.moveIf": {
        const loc = findCard(board, p.cardId);
        if (!loc)
          return;
        const aktuell = { sprintId: loc.sprint.id, columnId: loc.card.columnId === undefined ? null : loc.card.columnId };
        const amZiel = aktuell.sprintId === p.toSprintId && aktuell.columnId === p.toColId;
        if (!amZiel && (aktuell.sprintId !== p.expectSprintId || aktuell.columnId !== p.expectColId)) {
          if (Array.isArray(protokoll)) {
            protokoll.push({ art: "feld-geaendert", op: op.id, typ: op.type, cardId: p.cardId, feld: "ort", aktuell });
          }
          return;
        }
        const toSprint = (board.sprints || []).find((s) => s.id === p.toSprintId);
        if (!toSprint)
          return;
        const [card] = loc.sprint.cards.splice(loc.idx, 1);
        if (!Array.isArray(toSprint.cards))
          toSprint.cards = [];
        insertCard(toSprint, card, klemmeSpalte(board, p.toColId, protokoll, { op: op.id, typ: op.type, cardId: card.id }), p.targetIndex ?? null, p.anchor);
        return;
      }
      case "card.checkItemIf": {
        const loc = findCard(board, p.cardId);
        if (!loc)
          return;
        const beschreibung = typeof loc.card.description === "string" ? loc.card.description : "";
        const punkt = checklistePunkte(beschreibung).find((x) => x.text === p.text && x.vorkommen === p.vorkommen);
        if (punkt && punkt.erledigt === p.erledigt)
          return;
        if (!punkt || punkt.erledigt !== p.expect) {
          if (Array.isArray(protokoll)) {
            protokoll.push({
              art: "feld-geaendert",
              op: op.id,
              typ: op.type,
              cardId: p.cardId,
              feld: "checkpunkt",
              text: p.text,
              vorkommen: p.vorkommen,
              aktuell: punkt ? punkt.erledigt : null
            });
          }
          return;
        }
        if (punkt.erledigt === p.erledigt)
          return;
        loc.card.description = setzeChecklistePunkt(beschreibung, punkt.zeile, p.erledigt);
        return;
      }
      case "card.listAdd":
      case "card.listRemove": {
        const loc = findCard(board, p.cardId);
        if (!loc)
          return;
        const liste = Array.isArray(loc.card[p.field]) ? loc.card[p.field] : [];
        const form = stabil(p.item);
        const drin = liste.some((x) => stabil(x) === form);
        if (op.type === "card.listAdd") {
          if (!drin)
            loc.card[p.field] = [...liste, clone(p.item)];
          else if (!Array.isArray(loc.card[p.field]))
            loc.card[p.field] = liste;
        } else if (drin) {
          loc.card[p.field] = liste.filter((x) => stabil(x) !== form);
        }
        return;
      }
      case "card.claim": {
        const loc = findCard(board, p.cardId);
        if (!loc)
          return;
        const jetzt = opZeitMs(op);
        const alt = loc.card.claim;
        const zustand = claimZustandFuerUebernahme(loc.card, jetzt, op.actor, p.session);
        if (zustand === "fremd-aktiv") {
          if (Array.isArray(protokoll)) {
            protokoll.push({
              art: "beansprucht",
              op: op.id,
              typ: op.type,
              cardId: p.cardId,
              halter: alt.by,
              session: alt.session ?? null,
              until: alt.until ?? null
            });
          }
          return;
        }
        const neu = {
          by: op.actor || "",
          onBehalfOf: typeof p.onBehalfOf === "string" && p.onBehalfOf ? p.onBehalfOf : null,
          session: p.session || null,
          since: zustand === "eigen" && alt.since ? alt.since : op.ts,
          until: new Date(jetzt + leaseSekunden(p.leaseSeconds) * 1000).toISOString()
        };
        if (zustand === "eigen") {
          if (neu.onBehalfOf == null && alt.onBehalfOf != null)
            neu.onBehalfOf = clone(alt.onBehalfOf);
          if (alt.vorher)
            neu.vorher = clone(alt.vorher);
        } else if (zustand !== "frei") {
          neu.vorher = { by: alt.by ?? "", session: alt.session ?? null, until: alt.until ?? null };
        }
        loc.card.claim = neu;
        return;
      }
      case "card.release": {
        const loc = findCard(board, p.cardId);
        if (!loc)
          return;
        if (p.expect != null && !halterWieErwartet(loc.card.claim, p.expect)) {
          if (Array.isArray(protokoll)) {
            const c = loc.card.claim;
            protokoll.push({
              art: "halter-geaendert",
              op: op.id,
              typ: op.type,
              cardId: p.cardId,
              halter: c ? c.by : null,
              session: c ? c.session ?? null : null,
              since: c ? c.since ?? null : null,
              until: c ? c.until ?? null : null
            });
          }
          return;
        }
        if (!loc.card.claim)
          return;
        const alt = loc.card.claim;
        if (p.erzwingen !== true && claimZustand(loc.card, opZeitMs(op), op.actor, p.session) !== "eigen") {
          if (Array.isArray(protokoll)) {
            protokoll.push({
              art: "nicht-halter",
              op: op.id,
              typ: op.type,
              cardId: p.cardId,
              halter: alt.by,
              session: alt.session ?? null,
              until: alt.until ?? null
            });
          }
          return;
        }
        delete loc.card.claim;
        return;
      }
      case "card.field": {
        const loc = findCard(board, p.cardId);
        if (!loc)
          return;
        if (p.field === "columnId") {
          loc.card.columnId = klemmeSpalte(board, clone(p.value), protokoll, { op: op.id, typ: op.type, cardId: p.cardId });
          return;
        }
        if (p.field === "effort")
          meldeEffortTyp(protokoll, effortTyp, op, p.cardId, p.value, loc.card.effort);
        setzeKartenFeld(loc.card, p.field, p.value);
        return;
      }
      case "comment.add": {
        const loc = findCard(board, p.cardId);
        if (!loc || !p.comment || typeof p.comment !== "object")
          return;
        if (!Array.isArray(loc.card.comments))
          loc.card.comments = [];
        if (p.comment.id != null && loc.card.comments.some((c) => c.id === p.comment.id))
          return;
        loc.card.comments.push(clone(p.comment));
        return;
      }
      case "comment.delete": {
        const loc = findCard(board, p.cardId);
        if (!loc || typeof p.commentId !== "string")
          return;
        if (!Array.isArray(loc.card.comments))
          return;
        loc.card.comments = loc.card.comments.filter((c) => c && c.id !== p.commentId);
        if (Array.isArray(loc.card.attachments) && loc.card.attachments.some((a) => a && a.commentId === p.commentId)) {
          const weg = new Set(loc.card.attachments.filter((a) => a && a.commentId === p.commentId).map((a) => a.id));
          loc.card.attachments = loc.card.attachments.filter((a) => !(a && a.commentId === p.commentId));
          if (loc.card.coverAttachmentId != null && weg.has(loc.card.coverAttachmentId)) {
            loc.card.coverAttachmentId = null;
          }
        }
        return;
      }
      case "comment.edit": {
        const loc = findCard(board, p.cardId);
        if (!loc || typeof p.commentId !== "string" || typeof p.text !== "string")
          return;
        if (!Array.isArray(loc.card.comments))
          return;
        const k = loc.card.comments.find((c) => c && c.id === p.commentId);
        if (!k)
          return;
        if (k.kind === "extern")
          return;
        k.text = p.text;
        if (p.edited !== undefined)
          k.edited = clone(p.edited);
        return;
      }
      case "comment.editIf": {
        const loc = findCard(board, p.cardId);
        if (!loc || !Array.isArray(loc.card.comments))
          return;
        const k = loc.card.comments.find((c) => c && c.id === p.commentId);
        if (!k)
          return;
        if (k.kind !== "extern" && k.text === p.text)
          return;
        if (k.kind === "extern" || k.text !== p.expect) {
          if (Array.isArray(protokoll)) {
            protokoll.push({
              art: "feld-geaendert",
              op: op.id,
              typ: op.type,
              cardId: p.cardId,
              feld: "kommentar",
              commentId: p.commentId,
              aktuell: typeof k.text === "string" ? k.text : null
            });
          }
          return;
        }
        k.text = p.text;
        if (p.edited !== undefined)
          k.edited = clone(p.edited);
        return;
      }
      case "swimlane.upsert": {
        const sp = p.sprint;
        if (!sp || typeof sp !== "object" || typeof sp.id !== "string")
          return;
        if (!Array.isArray(board.sprints))
          board.sprints = [];
        const found = board.sprints.find((s) => s.id === sp.id);
        if (found) {
          if (sp.title !== undefined)
            found.title = sp.title;
          if (sp.kind !== undefined)
            found.kind = sp.kind;
          if (sp.archived !== undefined)
            found.archived = !!sp.archived;
          return;
        }
        const fresh = { id: sp.id, title: sp.title || "", kind: sp.kind || "sprint", cards: [] };
        if (sp.archived !== undefined)
          fresh.archived = !!sp.archived;
        const backlogIdx = board.sprints.findIndex((s) => s.id === "__backlog__");
        if (backlogIdx >= 0)
          board.sprints.splice(backlogIdx, 0, fresh);
        else
          board.sprints.push(fresh);
        return;
      }
      case "swimlane.move": {
        if (!Array.isArray(board.sprints))
          return;
        const idx = board.sprints.findIndex((s) => s.id === p.sprintId);
        if (idx === -1)
          return;
        const [sp] = board.sprints.splice(idx, 1);
        const ti = Number.isInteger(p.targetIndex) ? Math.max(0, Math.min(p.targetIndex, board.sprints.length)) : board.sprints.length;
        board.sprints.splice(ti, 0, sp);
        return;
      }
      case "swimlane.delete": {
        if (p.sprintId === "__backlog__")
          return;
        if (!Array.isArray(board.sprints))
          return;
        const sprint = board.sprints.find((s) => s.id === p.sprintId);
        if (!sprint)
          return;
        const backlog = backlogSprint(board);
        if (backlog === sprint)
          return;
        if (backlog) {
          const normal = (sprint.cards || []).filter((c) => c.kind !== "sprint-meta");
          if (!Array.isArray(backlog.cards))
            backlog.cards = [];
          backlog.cards.push(...normal);
        }
        board.sprints = board.sprints.filter((s) => s.id !== p.sprintId);
        return;
      }
      case "board.field": {
        const alt = board[p.field];
        board[p.field] = clone(p.value);
        if (p.field === "columnDefs")
          spaltenMarkenBewahren(alt, board.columnDefs);
        return;
      }
      case "board.awaiting": {
        const abbildung = awaitingAlsAbbildung(board.awaitingUser, board);
        const mitSitzung = typeof p.session === "string";
        const k = mitSitzung ? awaitingSitzungsSchluessel(p.person, p.session) : p.person;
        if (p.value === null)
          delete abbildung[k];
        else
          abbildung[k] = mitSitzung ? { ...clone(p.value), person: p.person, session: p.session } : clone(p.value);
        if (mitSitzung && p.value !== null)
          kappeAwaitingSitzungen(abbildung, p.person, k);
        board.awaitingUser = awaitingOderNull(abbildung);
        return;
      }
      case "termin.upsert": {
        const t = clone(p.termin);
        if (!Array.isArray(board.termine))
          board.termine = [];
        const idx = board.termine.findIndex((x) => x && x.id === t.id);
        if (idx === -1) {
          for (const k of TERMIN_VERWALTET)
            delete t[k];
          board.termine.push(t);
          return;
        }
        const alt = board.termine[idx];
        for (const k of [...TERMIN_HERKUNFT, ...TERMIN_VERWALTET]) {
          if (k in alt)
            t[k] = clone(alt[k]);
          else
            delete t[k];
        }
        if (t.confidential == null && Object.prototype.hasOwnProperty.call(alt, "confidential")) {
          t.confidential = clone(alt.confidential);
        }
        board.termine[idx] = t;
        return;
      }
      case "termin.fieldIf": {
        if (!Array.isArray(board.termine))
          return;
        const t = board.termine.find((x) => x && x.id === p.terminId);
        if (!t)
          return;
        const aktuell = t[p.field] === undefined ? null : t[p.field];
        if (stabil(aktuell) === stabil(p.value == null ? null : p.value))
          return;
        if (stabil(aktuell) !== stabil(p.expect === undefined ? null : p.expect)) {
          if (Array.isArray(protokoll)) {
            protokoll.push({
              art: "feld-geaendert",
              op: op.id,
              typ: op.type,
              terminId: p.terminId,
              feld: p.field,
              aktuell: clone(aktuell)
            });
          }
          return;
        }
        if (p.value == null)
          delete t[p.field];
        else
          t[p.field] = clone(p.value);
        return;
      }
      case "termin.delete": {
        if (!Array.isArray(board.termine) || !board.termine.some((x) => x && x.id === p.terminId))
          return;
        board.termine = board.termine.filter((x) => !(x && x.id === p.terminId));
        return;
      }
      case "maintenance.compact":
        return;
      case "maintenance.diagnostic":
        return;
      default:
        return;
    }
  }
  function fold(snapshot, ops, protokoll, optionen) {
    const base = clone(snapshot);
    const board = base.board;
    if (!board || !Array.isArray(board.sprints))
      return base;
    const feldMassstab = Array.isArray(protokoll) && Array.isArray(optionen?.feldMassstab) ? optionen.feldMassstab : null;
    const effortTyp = Array.isArray(protokoll) && optionen?.effortTyp === true;
    const seen = new Set;
    for (const op of orderedOps(ops || [])) {
      if (!validateOp(op))
        continue;
      if (seen.has(op.id))
        continue;
      seen.add(op.id);
      applyOp(board, op, protokoll, feldMassstab, effortTyp);
    }
    return base;
  }

  // server/fold/eintrag.js
  globalThis.AboardOps = exports_ops;
})();
