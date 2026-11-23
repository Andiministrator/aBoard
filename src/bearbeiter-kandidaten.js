/* Bearbeiter-Kandidaten im Server-Modus (Karte #698).

   Wer taucht in Owner-Dropdown, Assignee-Auswahl und Bulk-Menü „Owner setzen“ auf?
   Im Server-Modus: die Personen mit Rolle am Board plus die angemeldete Person
   (mit ihrem Namen aus GET /me, nicht dem lokalen `userName`). KEINE Agenten-
   Zugänge (Betreiber und Maintainer, 05.10.2026: nur Personen). Die Claude-
   Modelle erscheinen als eigene Gruppe NACH den Personen — aber nur auf Boards,
   die sie schon benutzen (`boardNutztClaude`); ein reines Teamboard ohne
   Claude-Karten bietet sie nicht an (Entscheidung Orchestrator, KO-1).

   Quellen und Rechte:
   - GET /me                → angemeldete Person (jeder Angemeldete).
   - GET /boards/{id}/acl   → Personen mit Rolle. Seit F-261 NUR für Owner des
                              Boards; Editor/Viewer bekommen 403 (der Client
                              meldet dann []). `GET /boards/{id}/ich` ist
                              Agenten-Token-Sache und taugt hier nicht.
   - GET /boards/{id}/mitglieder → Namen der Personen, für JEDE Rolle, ohne
                              Adressen (Karte #699). Geht vor. `[]` heißt „der
                              Server kennt keine Personen mit Namen“ → direkt
                              die Personen aus dem Board (KO-8c), NICHT die ACL.
                              `null` („nicht verfügbar“: älterer Server, Fehler)
                              → ACL, und erst wenn die leer ist, das Board.
   - Rückfall: Personen, die auf dem Board schon vorkommen — Owner/Assignees
     bestehender Karten, Kommentar-Autoren, Nutzer-Identitäten
     (`board.identities`).

   Gespeichert wird der ANZEIGENAME. owner/assignees sind im ganzen Produkt
   freie Label (Identity-Abgleich, Benachrichtigung über `identity.email`,
   `awaitingUser` je Person); der Name aus /me ist derselbe Wert, den
   `eigenePerson()` (renderer.js) für die angemeldete Person ansetzt. Eine ACL-
   Adresse wird über `nameForEmail` zum Namen (Identität mit dieser E-Mail,
   sonst Teil vor dem @).

   Reiner Server-Code: alle Aufrufer stehen hinter IS_SERVER_BUILD, damit das
   Modul im Solo-Template per DCE verschwindet. */

import { StoragePlugin } from './storage-plugin.js';
import { nameForEmail } from './identity.js';
import { Config } from './config.js';

const TTL_MS = 60_000;
let _cache = null; /* { boardId, person, ts, ich: {name,email}|null, acl: [], mitglieder: string[]|null } */

/* Die festen Claude-Owner, die das Modal im Solo-Betrieb anbietet (Fable seit #1172). */
export const CLAUDE_MODELLE = ['Claude', 'Claude Sonnet', 'Claude Opus', 'Claude Haiku', 'Claude Fable'];

/* Spiegel von `claudeKennung`/`keinePerson` in server/schleuse/auszug.go (Kritiker
   KS-3): „Claude“, „Claude Opus“, „Claude Opus 5 (1M context)“, „claude-opus“ —
   nach „Claude“ muss Modellfamilie oder Ziffer folgen. „Claude Dupont“ ist ein
   Mensch und bleibt Kandidat. Go-`[0-9]`/`\p{L}\p{N}` ↔ JS mit u-Flag. */
const CLAUDE_KENNUNG = /^claude(?:[ \-_](?:opus|sonnet|haiku|fable|code|instant|[0-9])[\p{L}\p{N} .,()\-_]*)?$/iu;

/** Trifft der Name die Claude-Kennung („Claude“, „Claude Opus 5“, „claude-opus“)? */
export function istClaudeKennung(name) {
  return CLAUDE_KENNUNG.test(String(name ?? '').trim().toLowerCase());
}

/** KO-1: Benutzt das Board Claude-Modelle? Mindestens eine Karte hat einen
    `owner` oder `assignees`-Eintrag, der die Claude-Kennung trifft. */
export function boardNutztClaude(board) {
  for (const sp of (Array.isArray(board?.sprints) ? board.sprints : [])) {
    for (const c of (Array.isArray(sp?.cards) ? sp.cards : [])) {
      if (istClaudeKennung(c?.owner)) return true;
      if ((Array.isArray(c?.assignees) ? c.assignees : []).some(istClaudeKennung)) return true;
    }
  }
  return false;
}

/** KO-1: Die Claude-Gruppe für Auswahllisten — leer, wenn das Board sie nicht benutzt. */
export function claudeGruppe(board) {
  return boardNutztClaude(board) ? [...CLAUDE_MODELLE] : [];
}

/** Keine Person: leer, „user“, „system“, „agent:…“, Claude-Modell (wie der Server)
    plus `external:` (Frontend-Sonderfall: Fremdadresse aus dem Mail-Ingest). */
export function istKeinePerson(name) {
  const l = String(name ?? '').trim().toLowerCase();
  return l === '' || l === 'user' || l === 'system' || l.startsWith('agent:')
    || l.startsWith('external:') || CLAUDE_KENNUNG.test(l);
}

/** Personen, die auf dem Board schon vorkommen (Rückfall ohne ACL-Lesezugriff). */
export function personenAusBoard(board) {
  const out = [];
  const add = (n) => {
    const s = typeof n === 'string' ? n.trim() : '';
    if (s && !istKeinePerson(s) && !out.includes(s)) out.push(s);
  };
  for (const i of (Array.isArray(board?.identities) ? board.identities : [])) {
    if (i?.kind === 'user') add(i.label);
  }
  for (const sp of (Array.isArray(board?.sprints) ? board.sprints : [])) {
    for (const c of (Array.isArray(sp?.cards) ? sp.cards : [])) {
      add(c?.owner);
      (Array.isArray(c?.assignees) ? c.assignees : []).forEach(add);
      (Array.isArray(c?.comments) ? c.comments : []).forEach(k => add(k?.author));
    }
  }
  return out;
}

/**
 * Reine Zusammenstellung (testbar ohne Netz).
 * @param {object} p  { board, ich: {name,email}|null, acl: [{email,role}] }
 * @returns {string[]} Anzeigenamen, die angemeldete Person zuerst.
 */
export function baueKandidaten({ board, ich, acl, mitglieder = null }) {
  const out = [];
  const add = (n) => {
    const s = typeof n === 'string' ? n.trim() : '';
    if (s && !out.some(o => o.toLowerCase() === s.toLowerCase())) out.push(s);
  };
  const meMail = (ich?.email || '').trim().toLowerCase();
  /* KO-7: Enthält der Name ein „@“ (Proxy ohne Anzeigename liefert die Adresse),
     gilt nur der Teil davor — nie die volle Adresse als Owner anbieten. */
  const meName = (ich?.name || ich?.email || '').trim().split('@')[0].trim();
  if (meName) add(meName);
  else {
    /* Legacy-Bypass ohne Anmeldung: die lokale Einstellung ist die einzige Auskunft. */
    const lokal = (Config.get('userName') || '').trim();
    if (lokal) add(lokal);
  }
  /* Karte #699: Die Namensliste des Servers (jede Rolle, ohne Adressen) geht
     vor; die ACL (nur Owner) und die Personen aus dem Board sind Rückfälle für
     ältere Server, Fehler oder eine leere Liste. */
  const namenGeliefert = Array.isArray(mitglieder);
  const namen = namenGeliefert ? mitglieder.filter(n => typeof n === 'string') : [];
  const eintraege = (Array.isArray(acl) ? acl : []).filter(e => e && typeof e.email === 'string');
  const personen = eintraege.filter(e => e.email.trim() && !e.email.trim().startsWith('@'));
  if (namen.length) {
    namen.forEach(add);
  } else if (namenGeliefert) {
    /* KO-8c: `[]` ist eine Auskunft des Servers (keine Personen mit Namen),
       kein Fehler — dann nicht auf die Owner-only-ACL ausweichen. */
    personenAusBoard(board).forEach(add);
  } else if (personen.length) {
    for (const e of personen) {
      if (meMail && e.email.trim().toLowerCase() === meMail) continue;
      add(nameForEmail(e.email, board));
    }
  } else {
    personenAusBoard(board).forEach(add);
  }
  /* Ausnahme: die angemeldete Person selbst bleibt immer wählbar. Eine Browser-
     Sitzung ist per Definition ein Mensch (Agenten laufen über Tokens und
     heißen „agent:…“); hieße jemand wirklich „Claude Opus“, dürfte er sich
     sonst nicht selbst zuweisen. Das ist ein bewusst zugelassener Grenzfall. */
  return out.filter(n => !istKeinePerson(n) || n === meName);
}

function plugin() { return StoragePlugin.getActivePlugin(); }

/* KO-8b: Wer ist gerade angemeldet? Der Cache (Namensliste/ACL) gilt nur für
   diese Person; /me selbst wird bei jedem Laden frisch gefragt (ein kleiner
   Aufruf) — so verwirft ein Personenwechsel im selben Tab den Cache. */
function personVon(ich) { return String(ich?.email || ich?.name || '').trim().toLowerCase(); }

/** Sofort verfügbarer Stand (Cache oder Rückfall) — blockiert nie. */
export function kandidatenSofort(board) {
  const hit = (_cache && _cache.boardId === board?.id) ? _cache : null;
  return baueKandidaten({ board, ich: hit?.ich ?? null, acl: hit?.acl ?? [], mitglieder: hit?.mitglieder ?? null });
}

/** Lädt /me (immer frisch) sowie Namensliste und ACL (Cache 60 s je Board und
    Person). Wirft nie; liefert die Kandidaten. Nur erfolgreiche Antworten
    werden gemerkt (KO-8a): ein /me-Fehlschlag ({name:null,email:null}) wird nicht
    gemerkt (kein Cache-Eintrag), der nächste Aufruf fragt erneut. */
export async function ladeKandidaten(board) {
  try {
    const boardId = board?.id;
    const p = plugin();
    let ich = null;
    try { ich = (await p?.getCurrentUser?.()) ?? null; } catch { ich = null; }
    const person = personVon(ich);
    /* VO-11: Ein /me-FEHLSCHLAG (Exception, kein Ergebnis, `fehler: true` vom
       REST-Plugin) wird nie gemerkt (KO-8a). Der erfolgreiche Legacy-Bypass
       ({name:null,email:null} ohne `fehler`) ist dagegen eine stabile Auskunft
       und wird unter dem Schlüssel „ohne Person“ (leere Person) gecacht. */
    const meFehlschlag = ich === null || ich.fehler === true;
    const gueltig = _cache && _cache.boardId === boardId && _cache.person === person
      && Date.now() - _cache.ts <= TTL_MS;
    if (!gueltig) {
      let acl = [];
      let mitglieder = null;
      try {
        if (boardId) mitglieder = (await p?.mitgliederNamen?.(boardId)) ?? null;
        if (!Array.isArray(mitglieder)) mitglieder = null;
      } catch { mitglieder = null; }
      /* ACL nur, wenn die Namensliste fehlt (`null`, älterer Server) — sie ist
         Owner-only und kostet Nicht-Owner sonst nur ein 403. Bei `[]` (Server
         kennt keine Personen mit Namen) kein ACL-Rückfall (KO-8c). */
      if (mitglieder === null) {
        try {
          if (boardId) acl = (await p?.aclList?.(boardId)) ?? [];
          if (!Array.isArray(acl)) acl = [];
        } catch { acl = []; }
      }
      if (!meFehlschlag) _cache = { boardId, person, ts: Date.now(), ich, acl, mitglieder };
      else {
        /* /me gescheitert: nichts merken. */
        _cache = null;
        return baueKandidaten({ board, ich, acl, mitglieder });
      }
    } else {
      _cache.ich = ich;
    }
  } catch { /* Kandidatenliste ist Beiwerk — nie das Modal blockieren */ }
  return kandidatenSofort(board);
}

/** Nur für Tests. */
export function vergesseKandidatenCache() { _cache = null; }
