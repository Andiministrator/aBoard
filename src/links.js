/* Karten-Verweise (→ ADR-0032)

   Datenmodell:
   card.links: [{ targetId: <uuid>, type: <LinkType>, created: <iso> }]

   Typen + inverse Beziehung:
     blocks       ↔ blocked-by      (Workflow-hart)
     depends-on   ↔ required-for    (Workflow-weich)
     related      ↔ related         (selbst-invers, symmetrisch)

   Symmetrie-Pflicht: Beim Anlegen/Löschen eines Links wird der inverse Link
   auf der Zielkarte mit-gepflegt — auf beiden Karten persistiert (kein Computed). */

export const LINK_TYPES = ['blocks', 'blocked-by', 'depends-on', 'required-for', 'related'];

const INVERSE = {
  'blocks':       'blocked-by',
  'blocked-by':   'blocks',
  'depends-on':   'required-for',
  'required-for': 'depends-on',
  'related':      'related',
};

export function inverseType(type) {
  return INVERSE[type] || null;
}

export function isValidType(type) {
  return type in INVERSE;
}

/* Findet eine Karte über alle Sprints anhand ihrer UUID */
export function findCardById(board, cardId) {
  if (!board?.sprints) return null;
  for (const sprint of board.sprints) {
    const card = sprint.cards?.find(c => c.id === cardId);
    if (card) return { sprint, card };
  }
  return null;
}

/* Sprint, in dem eine Karte liegt (oder null) */
export function sprintOfCard(board, cardId) {
  const hit = findCardById(board, cardId);
  return hit ? hit.sprint : null;
}

/* Prüft ob ein Link mit (targetId, type) bereits auf der Karte vorhanden ist */
function hasLink(card, targetId, type) {
  return (card.links || []).some(l => l.targetId === targetId && l.type === type);
}

function nowIso() {
  return new Date().toISOString().slice(0, 10);
}

/* Fügt einen Verweis an Quell- und Ziel-Karte hinzu (idempotent, mit Symmetrie).
   Mutiert das Board direkt. Gibt true zurück, wenn etwas geändert wurde. */
export function addLink(board, sourceId, targetId, type) {
  if (!isValidType(type))     return false;
  if (sourceId === targetId)  return false;   /* Selbst-Verweis verbieten */

  const src = findCardById(board, sourceId);
  const tgt = findCardById(board, targetId);
  if (!src || !tgt) return false;

  const inv = inverseType(type);
  let changed = false;
  const created = nowIso();

  if (!Array.isArray(src.card.links)) src.card.links = [];
  if (!Array.isArray(tgt.card.links)) tgt.card.links = [];

  if (!hasLink(src.card, targetId, type)) {
    src.card.links.push({ targetId, type, created });
    changed = true;
  }
  /* Inverse Richtung — bei selbst-invers (related) trotzdem nur ein Eintrag pro Karte */
  if (!hasLink(tgt.card, sourceId, inv)) {
    tgt.card.links.push({ targetId: sourceId, type: inv, created });
    changed = true;
  }
  return changed;
}

/* Entfernt einen Verweis incl. inversem Eintrag auf der Zielkarte.
   Mutiert das Board direkt. Gibt true zurück, wenn etwas geändert wurde. */
export function removeLink(board, sourceId, targetId, type) {
  if (!isValidType(type)) return false;

  const src = findCardById(board, sourceId);
  const tgt = findCardById(board, targetId);
  if (!src || !tgt) return false;

  const inv = inverseType(type);
  let changed = false;

  if (Array.isArray(src.card.links)) {
    const before = src.card.links.length;
    src.card.links = src.card.links.filter(l => !(l.targetId === targetId && l.type === type));
    if (src.card.links.length !== before) changed = true;
  }
  if (Array.isArray(tgt.card.links)) {
    const before = tgt.card.links.length;
    tgt.card.links = tgt.card.links.filter(l => !(l.targetId === sourceId && l.type === inv));
    if (tgt.card.links.length !== before) changed = true;
  }
  return changed;
}

/* Entfernt sämtliche Rückverweise auf eine Karte (Aufräumen beim Karten-Löschen).
   Wird beim Karten-Delete vor dem eigentlichen Splice aufgerufen. */
export function purgeLinksTo(board, removedCardId) {
  if (!board?.sprints) return;
  for (const sprint of board.sprints) {
    for (const card of sprint.cards || []) {
      if (!Array.isArray(card.links) || card.links.length === 0) continue;
      card.links = card.links.filter(l => l.targetId !== removedCardId);
    }
  }
}

/* Lokalisierte Labels für die fünf Typen — Quelle: I18n-Keys, hier nur Mapping */
export const TYPE_KEYS = {
  'blocks':       'link.type.blocks',
  'blocked-by':   'link.type.blocked-by',
  'depends-on':   'link.type.depends-on',
  'required-for': 'link.type.required-for',
  'related':      'link.type.related',
};

/* Gruppiert die Links einer Karte nach Typ. Reihenfolge analog LINK_TYPES. */
export function groupLinksByType(card) {
  const groups = {};
  for (const t of LINK_TYPES) groups[t] = [];
  (card.links || []).forEach(l => {
    if (groups[l.type]) groups[l.type].push(l);
  });
  return groups;
}

/* ── Erledigt-Erkennung (F-127) ──────────────────────────────────── */

/* F-127: Die Workflow-Effekte unten verglichen die Spalte einer Karte hart
   gegen die id `col-erledigt`. Manuell angelegte Spalten bekommen über
   `createColumnDef()` aber eine UUID-id — eine so entstandene oder umbenannte
   „Erledigt"-Spalte wurde vom Blocker-Pfad nie als erledigt erkannt, der
   blocked-by-Verweis also nie freigegeben. `renderer.js` (F-250/F-141) und
   `main.js` erkennen dieselbe Spalte längst über die Titel-Semantik; diese
   Liste ist ab hier die geteilte Quelle für den Renderer, der sie importiert.
   Seit F-807 importiert auch `main.js` von hier — es gibt keine zweite Kopie
   der Liste mehr im Repo. */
export const DONE_COLUMN_NAMES = ['erledigt', 'done', 'fertig', 'closed', 'abgeschlossen'];

/* Erkennt eine Erledigt-Spalte an ihrem Titel (nicht an ihrer id). */
export function isDoneColumn(colDef) {
  return DONE_COLUMN_NAMES.includes((colDef?.title ?? '').trim().toLowerCase());
}

/* Erledigt-Prüfung über die columnId einer Karte — der Regelfall für Aufrufer,
   die nur die id kennen (card.columnId), nicht das columnDef-Objekt.
   Fällt bewusst auf den id-Vergleich zurück, wenn zu der id kein columnDef
   existiert (Alt-Board ohne columnDefs, schmale Test-Fixture) — sonst gälte
   dort plötzlich KEINE Spalte mehr als erledigt und jeder Blocker bliebe
   für immer stehen. Exportiert seit F-830/F-834 (Karte #454): das ist genau
   der Rückweg, dessen Fehlen in main.js F-834 auslöst, wenn Aufrufer stattdessen
   direkt `isDoneColumn(def)` ohne Fallback aufrufen. */
export function isDoneColumnId(board, columnId) {
  const defs = board?.columnDefs;
  const def  = Array.isArray(defs) ? defs.find(cd => cd.id === columnId) : null;
  if (!def) return columnId === 'col-erledigt';
  return isDoneColumn(def);
}

/* ── Workflow-Effekte (ADR-0032, Karte #54) ──────────────────────── */

/* Prüft ob eine Karte durch offene blocked-by-Verweise blockiert ist (computed, nicht persistiert).
   Gibt true zurück, wenn mindestens ein blocked-by-Ziel NICHT in einer Erledigt-Spalte liegt. */
export function isLinkBlocked(board, card) {
  if (!Array.isArray(card.links) || card.links.length === 0) return false;
  for (const link of card.links) {
    if (link.type !== 'blocked-by') continue;
    const hit = findCardById(board, link.targetId);
    if (!hit) continue; /* verwaister Verweis — ignorieren */
    if (!isDoneColumnId(board, hit.card.columnId)) return true;
  }
  return false;
}

/* Gibt die Liste offener Blocker-Karten zurück (für Tooltip/Verschiebe-Warnung).
   Nur blocked-by-Ziele, die NICHT in einer Erledigt-Spalte liegen. */
export function openBlockers(board, card) {
  if (!Array.isArray(card.links) || card.links.length === 0) return [];
  const result = [];
  for (const link of card.links) {
    if (link.type !== 'blocked-by') continue;
    const hit = findCardById(board, link.targetId);
    if (!hit) continue;
    if (!isDoneColumnId(board, hit.card.columnId)) result.push(hit.card);
  }
  return result;
}

/* Gibt die Liste offener depends-on-Ziele zurück (für Verschiebe-Warnung nur → erledigt).
   Nur depends-on-Ziele, die NICHT in einer Erledigt-Spalte liegen. */
export function openDependencies(board, card) {
  if (!Array.isArray(card.links) || card.links.length === 0) return [];
  const result = [];
  for (const link of card.links) {
    if (link.type !== 'depends-on') continue;
    const hit = findCardById(board, link.targetId);
    if (!hit) continue;
    if (!isDoneColumnId(board, hit.card.columnId)) result.push(hit.card);
  }
  return result;
}

/* Prüft ob eine Verschiebe-Warnung nötig ist (user-initiiert).
   Gibt ein Objekt { warn: true, type: 'blocked-by'|'depends-on', blockers: Card[] } zurück
   oder { warn: false } wenn kein Einwand.

   Logik:
   - blocked-by (hart): warnt bei → col-in-arbeit UND → col-erledigt
   - depends-on (weich): warnt NUR bei → col-erledigt */
export function checkMoveWarning(board, card, toColId) {
  const isDone   = isDoneColumnId(board, toColId);
  const isInWork = toColId === 'col-in-arbeit';

  /* blocked-by warnt bei in-arbeit und erledigt */
  if (isInWork || isDone) {
    const blockers = openBlockers(board, card);
    if (blockers.length > 0) return { warn: true, type: 'blocked-by', blockers };
  }

  /* depends-on warnt NUR bei erledigt */
  if (isDone) {
    const deps = openDependencies(board, card);
    if (deps.length > 0) return { warn: true, type: 'depends-on', blockers: deps };
  }

  return { warn: false };
}

/* Erkennt welche Karten durch die Erledigung von completedCardId potenziell entsperrt wurden.
   Eine Karte ist "entsperrt" wenn:
   - Sie einen blocked-by-Verweis auf completedCardId hat
   - Sie KEINE weiteren offenen blocked-by-Verweise hat (außer auf completedCardId selbst)
   Gibt Array von Karten zurück (kein Auto-Mutate). */
export function detectUnlockedCards(board, completedCardId) {
  const unlocked = [];
  if (!board?.sprints) return unlocked;
  for (const sprint of board.sprints) {
    for (const card of sprint.cards || []) {
      if (!Array.isArray(card.links) || card.links.length === 0) continue;
      /* Hat die Karte einen blocked-by-Verweis auf die gerade erledigte Karte? */
      const hasBlockerLink = card.links.some(
        l => l.type === 'blocked-by' && l.targetId === completedCardId,
      );
      if (!hasBlockerLink) continue;
      /* Hat sie noch ANDERE offene blocked-by-Verweise? */
      const otherOpenBlockers = card.links.filter(l => {
        if (l.type !== 'blocked-by') return false;
        if (l.targetId === completedCardId) return false; /* der gerade erledigte zählt nicht */
        const hit = findCardById(board, l.targetId);
        if (!hit) return false; /* verwaist — ignorieren */
        return !isDoneColumnId(board, hit.card.columnId);
      });
      if (otherOpenBlockers.length === 0) unlocked.push(card);
    }
  }
  return unlocked;
}
