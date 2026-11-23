/* Profil-System (ADR-0044, Karte #68)
 *
 * Vier Profile ersetzen das alte 2-Modi-Schema (standalone | project).
 * Ein Profil bündelt Storage-Plugin + Feature-Capabilities. Die Capability-
 * Abfrage via `Profile.has(...)` ersetzt die ~22 `boardMode`-Verzweigungen
 * im Frontend-Code.
 *
 * Config-Key-Name `boardMode` bleibt aus Kompatibilitätsgründen unverändert —
 * nur die Werte ändern sich: solo | team | claude-code | server.
 * Legacy-Werte (standalone → solo, project → claude-code) werden beim Load
 * in config.js migriert (inline, ohne Import von profile.js).
 */

import { Config } from './config.js';

/**
 * Capability-Listen pro Profil.
 *
 * feature.agentContext         — agentContext-Tab im Karten-Modal sichtbar
 * feature.polling              — Sync-Polling unterstützt (Projekt + Server)
 * feature.restStorage          — REST-API-basiertes Storage-Plugin (Server-Modus).
 *                                 Unterscheidet server von claude-code in pluginKindFor():
 *                                 beide haben feature.polling, nur server hat restStorage.
 * feature.fsaVolumeAttachments — Attachments leben im FSA-Directory-Volume neben
 *                                 aboard-sync.json (nur claude-code).
 *                                 Server-Profil nutzt REST-Upload, nicht FSA (F-194).
 * feature.ownerClaude          — Owner=Claude* erlaubt + Schätz-Trigger
 * feature.attachmentLimits     — Attachment-Größen-Limits aktiv (Solo, Team, Server).
 *                                 Karte #450: Bis Sprint 61 stand hier „Server hat
 *                                 ausreichend Volume-Platz" — die Annahme war falsch,
 *                                 weil ein Anhang im Server-Modus gar nicht auf dem
 *                                 Volume landet, sondern als base64 IM BOARD-STATE
 *                                 reist (kein feature.fsaVolumeAttachments, F-194) und
 *                                 bei jedem Speichern gegen MaxBodyBytes = 10 MB geht.
 *                                 Die Grenze ist also nicht der Plattenplatz, sondern
 *                                 der PUT-Body. Ohne das Flag stieg checkUpload dort
 *                                 sofort mit {ok:true} aus und der Nutzer bekam statt
 *                                 einer Erklärung ein „Speichern fehlgeschlagen".
 *                                 Wenn Punkt 1 der Karte #450 umgesetzt ist (Upload
 *                                 über das REST-Plugin statt base64), gehört diese
 *                                 Entscheidung erneut geprüft — dann gilt der alte
 *                                 Satz wieder.
 * feature.userNameMulti        — mehrere Owner-Namen (Team-Modus)
 * feature.complexityEstimate   — Komplexitäts- und Aufwand-Felder sichtbar
 */
export const PROFILES = {
  solo: {
    id: 'solo',
    capabilities: new Set([
      'feature.attachmentLimits',
    ]),
  },
  team: {
    id: 'team',
    capabilities: new Set([
      'feature.attachmentLimits',
      'feature.userNameMulti',
    ]),
  },
  'claude-code': {
    id: 'claude-code',
    capabilities: new Set([
      'feature.agentContext',
      'feature.polling',
      'feature.fsaVolumeAttachments',
      'feature.ownerClaude',
      'feature.complexityEstimate',
    ]),
  },
  server: {
    id: 'server',
    capabilities: new Set([
      'feature.agentContext',
      'feature.polling',
      'feature.restStorage',
      'feature.ownerClaude',
      'feature.complexityEstimate',
      /* Karte #450: siehe Begründung im Kopfkommentar — solange Anhänge als
         base64 im State reisen, ist der PUT-Body die Grenze, nicht die Platte. */
      'feature.attachmentLimits',
    ]),
  },
};

/**
 * Migriert Legacy-boardMode-Werte auf neue Profil-IDs.
 * Idempotent: bekannte neue Werte werden unverändert zurückgegeben.
 * Unbekannte Werte fallen auf 'solo' zurück.
 *
 * @param {string} value - Legacy- oder aktueller boardMode-Wert
 * @returns {'solo'|'team'|'claude-code'|'server'}
 */
function migrateBoardMode(value) {
  const LEGACY = { standalone: 'solo', project: 'claude-code' };
  if (LEGACY[value]) return LEGACY[value];
  if (PROFILES[value]) return value;
  return 'solo';
}

/**
 * Liefert das Profil-Objekt für einen gegebenen boardMode-Wert.
 * Ohne Parameter wird Config.get('boardMode') verwendet.
 *
 * @param {string} [boardModeOverride] - Optionaler boardMode (z. B. aus dem Select vor dem Speichern)
 * @returns {{ id: string, capabilities: Set<string> }}
 */
function getActive(boardModeOverride) {
  const mode = boardModeOverride !== undefined ? boardModeOverride : Config.get('boardMode');
  const profileId = migrateBoardMode(mode);
  return PROFILES[profileId] ?? PROFILES.solo;
}

/**
 * Prüft, ob ein Profil eine bestimmte Capability hat.
 * Ohne boardModeOverride wird das aktive Profil (Config) genutzt.
 *
 * @param {string} capability - z. B. 'feature.polling'
 * @param {string} [boardModeOverride] - Optionaler boardMode-Wert
 * @returns {boolean}
 */
function has(capability, boardModeOverride) {
  return getActive(boardModeOverride).capabilities.has(capability);
}

export const Profile = { getActive, has, PROFILES, migrateBoardMode };
