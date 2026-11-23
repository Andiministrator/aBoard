/* Settings-Klassifikation — Single Source of Truth (ADR-0063)
 *
 * Zwei Dimensionen pro Settings-Schlüssel:
 *
 * persistScope — Persistenz-Schicht (formalisiert ADR-0048):
 *   'global'     → nutzer-/geräteweit (aboard-config); entspricht GLOBAL_KEYS in config.js
 *   'deployment' → deployment-scoped (aboard-config@<pathname>); projektgebunden
 *
 * serverScope — Verhalten im Server-Modus (feature.restStorage):
 *   'user'      → USER-lokal: im Server-Modus editierbar (nur dieser Browser/Nutzer betroffen)
 *   'board'     → BOARD/server-autoritativ: im Server-Modus read-only + Hinweis
 *                 (Forward-Auth-Identität oder geteilter Board-State betroffen)
 *   'dangerous' → Gefährliche UI-Aktion: im Server-Modus komplett versteckt
 *                 (würde das geteilte Board für alle Mitglieder betreffen)
 *
 * UI-Ableitungsregeln (ADR-0063 §2):
 *   'user'      → immer sichtbar + editierbar
 *   'board'     → sichtbar + read-only im Server-Modus; editierbar im Solo-/CC-Modus
 *   'dangerous' → versteckt im Server-Modus; sichtbar + aktiv im Solo-/CC-Modus
 *
 * Hinweis: Diese Datei definiert nur die Klassifikation — keine Logik.
 * Der Modus wird via Profile.has('feature.restStorage') ermittelt (ADR-0059).
 *
 * Dritte Dimension — Modus-Relevanz (ADR-0069):
 *   isSectionRelevant(section, boardMode) → true wenn die Sektion im gegebenen Modus sinnvoll ist.
 *   Wird aus Profile.has(...) abgeleitet (F-269: Single Source, keine Doppel-Wahrheit).
 *   Orthogonal zu serverScope — blendet modus-irrelevante Sektionen komplett aus,
 *   unabhängig davon ob sie server-restricted wären. */

import { Profile } from './profile.js';

/* Compile-Time-Flag (Vite-Define, ADR-0072): nur im Projektor-Build (`aboard.html`)
   true. Im slim-Build (`aboard.template.html`) und Server-Build → DCE entfernt die
   chat-tail-bezogenen Einträge (Karte #325). */
const IS_PROJECTOR_BUILD = (typeof __PROJECTOR_BUILD__ !== 'undefined') &&
  (__PROJECTOR_BUILD__ === true || __PROJECTOR_BUILD__ === 'true');

/* Karte #335 / ADR-0064: nur im Server-Frontend-Build true. */
const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

/**
 * Sektion→Dialog-Zuordnung (ADR-0065 §1) — Single Source of Truth für die
 * Dialog-Partition in configdialog.js. Modus-Relevanz via isSectionRelevant() (ADR-0069).
 *
 * @type {{ user: string[], board: string[] }}
 */
export const SECTION_DIALOG_MAP = {
  user:  ['identities', 'theme', 'language'],
  board: ['mode', 'project', 'server', 'attachments', 'background', 'templates', 'danger'],
};
/* Karte #335: Backup-Sektion nur im Server-Build im Bundle. Im slim- und
   claude-code-Build gibt es weder Backup-Endpunkte noch eine Board-ACL —
   der Eintrag würde eine Sektion ankündigen, die configdialog.js dort gar
   nicht baut. `unshift` vor 'danger' wäre positionsempfindlich; angehängt
   wird er, weil die Reihenfolge im Dialog von der Einhänge-Reihenfolge in
   buildOverlay kommt, nicht von dieser Liste. */
if (IS_SERVER_BUILD) SECTION_DIALOG_MAP.board.push('backup');
/* Karte #702: Freigabe („Board teilen“) ganz oben im Board-Dialog, nur Server-Build. */
if (IS_SERVER_BUILD) SECTION_DIALOG_MAP.board.unshift('share');
/* Karte #703: KI-Anbieter (Schlüssel je Person) im Benutzer-Dialog, nur Server-Build. */
if (IS_SERVER_BUILD) SECTION_DIALOG_MAP.user.push('llm');
/* Karte #325: chat-tail nur im Projektor-Build sichtbar — slim-Build DCE't den
   Block, Sektion fällt aus dem Dialog. */
if (IS_PROJECTOR_BUILD) SECTION_DIALOG_MAP.board.splice(2, 0, 'chat-tail');

/**
 * Klassifikationstabelle aller Settings-Schlüssel und UI-Elemente.
 *
 * @type {Record<string, { persistScope: 'global'|'deployment', serverScope: 'user'|'board'|'dangerous' }>}
 */
export const SETTINGS_CLASSIFICATION = {
  /* ── USER-lokale globale Präferenzen ──────────────────────────────────── */
  theme:    { persistScope: 'global',     serverScope: 'user' },
  density:  { persistScope: 'global',     serverScope: 'user' }, /* Karte #973: Kartendichte */
  corners:  { persistScope: 'global',     serverScope: 'user' }, /* ADR-0153: Darstellung */
  font:     { persistScope: 'global',     serverScope: 'user' },
  accent:   { persistScope: 'global',     serverScope: 'user' },
  sortierung: { persistScope: 'global',   serverScope: 'user' }, /* Karte #1004 */
  language: { persistScope: 'global',     serverScope: 'user' },
  userName: { persistScope: 'global',     serverScope: 'board' }, /* im Server-Modus nicht gesetzt (Feld ausgeblendet); wer „ich“ bin, sagt dort getCurrentUser() — für awaitingUser zählen Name und E-Mail daraus (Verify Sprint 78, V-1) */
  version:  { persistScope: 'global',     serverScope: 'user' },

  /* Attachment-Limits — global; seit Karte #450 auch im Server-Modus relevant (F-132) */
  attachmentMaxBytes:       { persistScope: 'global', serverScope: 'user' },
  attachmentBoardWarnBytes: { persistScope: 'global', serverScope: 'user' },
  attachmentBoardHardBytes: { persistScope: 'global', serverScope: 'user' },

  /* ── BOARD/deployment-scoped Einstellungen ────────────────────────────── */
  boardMode:       { persistScope: 'deployment', serverScope: 'board' },     /* im Server-Modus nicht wechselbar */
  projectName:     { persistScope: 'deployment', serverScope: 'board' },
  collapsedColumns:{ persistScope: 'deployment', serverScope: 'user' },      /* geräte-lokale UI-Präferenz */
  serverUrl:       { persistScope: 'deployment', serverScope: 'board' },
  serverPollInterval: { persistScope: 'deployment', serverScope: 'board' },
  serverPollEnabled:  { persistScope: 'deployment', serverScope: 'board' },

  /* ── UI-Elemente (keine Config-Werte, aber klassifiziert für UI-Ableitungen) ── */
  /* Hinweis: 'ui.project-dir' (Ordner-wählen-Button) ist NICHT hier klassifiziert,
     weil refreshProjectArea() die Sichtbarkeit über (hasPolling && !isServer) steuert —
     eine Kombination aus Profil-Capability (hasPolling) und Server-Flag. SettingsScope
     modelliert keine gemischten Capability+Server-Bedingungen; der Ad-hoc-Pfad bleibt
     absichtlich erhalten und ist in ADR-0063 §3 dokumentiert. */
  'ui.reset-board':  { persistScope: 'deployment', serverScope: 'dangerous' }, /* Neues leeres Board */
  'ui.board-mode':   { persistScope: 'deployment', serverScope: 'dangerous' }, /* Mode-Switch (widersprüchlich im Server-Modus) */
  'ui.open-file':    { persistScope: 'deployment', serverScope: 'dangerous' }, /* Datei öffnen (im Server-Modus funktionslos) */
  'ui.ki-context':   { persistScope: 'deployment', serverScope: 'dangerous' }, /* KI-Kontext-Export (im Server-/Multi-User-Betrieb irrelevant, Karte #222) */
};

/* Karte #290/#325/#329: Chat-Tail- und Activity-Tail-Settings nur im
   Projektor-Build klassifiziert — slim-Build (kein IS_PROJECTOR_BUILD)
   DCE't den Block, die Keys existieren dort nicht im Bundle. */
if (IS_PROJECTOR_BUILD) {
  SETTINGS_CLASSIFICATION.chatTailEnabled    = { persistScope: 'deployment', serverScope: 'user' };
  SETTINGS_CLASSIFICATION.chatTailLines      = { persistScope: 'deployment', serverScope: 'user' };
  SETTINGS_CLASSIFICATION.activityTailEnabled = { persistScope: 'deployment', serverScope: 'user' };
  SETTINGS_CLASSIFICATION.activityTailLines   = { persistScope: 'deployment', serverScope: 'user' };
}

/**
 * Gibt true zurück, wenn das Setting im Server-Modus eingeschränkt ist
 * (read-only oder versteckt). Für Solo-/CC-Modus immer false.
 *
 * @param {string} key - Settings-Schlüssel oder UI-Element-Key (z. B. 'ui.reset-board')
 * @param {boolean} isServerMode - Profile.has('feature.restStorage')
 * @returns {boolean}
 */
export function isServerRestricted(key, isServerMode) {
  if (!isServerMode) return false;
  const entry = SETTINGS_CLASSIFICATION[key];
  if (!entry) return false;
  return entry.serverScope === 'board' || entry.serverScope === 'dangerous';
}

/**
 * Gibt true zurück, wenn das Setting im Server-Modus komplett versteckt werden soll
 * (nur für serverScope: 'dangerous'). Für Solo-/CC-Modus immer false.
 *
 * @param {string} key - Settings-Schlüssel oder UI-Element-Key
 * @param {boolean} isServerMode - Profile.has('feature.restStorage')
 * @returns {boolean}
 */
export function isServerHidden(key, isServerMode) {
  if (!isServerMode) return false;
  const entry = SETTINGS_CLASSIFICATION[key];
  if (!entry) return false;
  return entry.serverScope === 'dangerous';
}

/**
 * Gibt true zurück, wenn eine Settings-Sektion im gegebenen Board-Modus sinnvoll/relevant ist.
 * Leitet die Relevanz aus Profile.has() ab — keine parallele Modus-Liste (F-269, ADR-0069 §2).
 *
 * Schichtenmodell (ADR-0069 §1):
 *   1. Dialog-Partition (ADR-0065): In welchem Dialog erscheint die Sektion?
 *   2. Modus-Relevanz  (diese Funktion): Ist die Sektion im aktuellen Modus überhaupt sinnvoll?
 *   3. Server-Filterung (ADR-0063): read-only oder versteckt im Server-Modus?
 *
 * ADR-0059-Invariante: Diese Funktion ändert den Datenpfad nicht — nur die UI-Sichtbarkeit.
 *
 * @param {string} section - Sektions-ID aus SECTION_DIALOG_MAP ('project', 'server', 'attachments', …)
 * @param {string} boardMode - Aktiver Board-Modus ('solo'|'team'|'claude-code'|'server')
 * @returns {boolean}
 */
export function isSectionRelevant(section, boardMode) {
  /* Karte #290/#325: Chat-Tail-Footer nur im claude-code-Modus relevant — und
     nur im Projektor-Build überhaupt im Bundle. slim DCE't den ganzen Zweig. */
  if (IS_PROJECTOR_BUILD && section === 'chat-tail') return boardMode === 'claude-code';
  switch (section) {
    /* FSA-Projektordner-Bereich: nur claude-code (hat polling, aber kein restStorage).
       Server-Modus hat restStorage → Ordner-Wählen sinnlos. Solo/Team: kein polling. */
    case 'project':
      return Profile.has('feature.polling', boardMode) &&
             !Profile.has('feature.restStorage', boardMode);
    /* Server-URL/Polling-Bereich: nur im Server-Modus (restStorage). */
    case 'server':
      return Profile.has('feature.restStorage', boardMode);
    /* Karte #335: Board-Backup gibt es nur im Server-Modus — die Endpunkte
       liegen am Go-Server. Ob der aktuelle Nutzer die Sektion tatsächlich
       SIEHT, entscheidet zusätzlich ein asynchroner Owner-/Admin-Check in
       configdialog.js; das hier ist nur die Modus-Frage. */
    case 'backup':
      return IS_SERVER_BUILD && Profile.has('feature.restStorage', boardMode);
    /* Karte #702: Freigabe — Server-Build und Server-Modus. Ob die Person sie
       verwalten darf (Owner), entscheidet share-section.js zur Laufzeit. */
    case 'share':
      return IS_SERVER_BUILD && Profile.has('feature.restStorage', boardMode);
    /* Karte #703: KI-Anbieter — Server-Build und Server-Modus. */
    case 'llm':
      return IS_SERVER_BUILD && Profile.has('feature.restStorage', boardMode);
    /* Attachment-Limits: bei Solo, Team und Server (feature.attachmentLimits, seit Karte #450). */
    case 'attachments':
      return Profile.has('feature.attachmentLimits', boardMode);
    /* Gefahrenzone (Board-Reset, ADR-0071 Achse 3): nur im Single-File-
       Modell sinnvoll (Solo/Team — Wahrheit liegt in HTML/localStorage,
       Single-Writer). claude-code (Wahrheit = aboard-sync.*, Op-Inbox/
       Projektor) und Server (Wahrheit = Go-Server) haben beide
       feature.polling → dort ist ein UI-Reset bedeutungslos und wird
       komplett entfernt. F-294-Hard-Stop im board:reset-Handler bleibt
       als Defense-in-depth-Backstop. */
    case 'danger':
      return !Profile.has('feature.polling', boardMode);
    /* Alle anderen Sektionen sind modus-unabhängig immer relevant:
       theme, language, mode, background, templates, identities.
       ADR-0080 (revidiert ADR-0078 §identities): Die identities-Sektion ist
       nicht mehr server-abhängig false. Farbe/Icon sind lokale kosmetische
       Präferenzen ohne Forward-Auth-Bezug → im Server-Modus sichtbar.
       Nur der Anzeigename wird auf Feld-Ebene in configdialog.js ausgeblendet. */
    default:
      return true;
  }
}

export const SettingsScope = {
  CLASSIFICATION: SETTINGS_CLASSIFICATION,
  SECTION_DIALOG_MAP,
  isServerRestricted,
  isServerHidden,
  isSectionRelevant,
};
