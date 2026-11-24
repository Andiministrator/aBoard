/* @vitest-environment happy-dom */
/* Unit-Tests: Profil-System (ADR-0044, Karte #68)
 *
 * Verifiziert:
 * - PROFILES-Konstante enthält alle vier Profile
 * - migrateBoardMode() migriert Legacy-Werte korrekt und ist idempotent
 * - has() liefert korrekte Capability-Werte pro Profil
 * - getActive() liest boardMode aus Config und liefert das richtige Profil
 */

import { describe, it, expect, beforeEach } from 'vitest'
import { Profile, PROFILES }                from '../src/profile.js'
import { Config }                           from '../src/config.js'

/* F-272 / ADR-0064: Im Server-Build-Kontext (test:server) ist 'server' der einzige
   gültige boardMode — Config.set('boardMode', 'solo/team/claude-code') clampt auf 'server'.
   Tests die Standalone-boardModes aktiv nutzen (via Config.set), laufen nur im
   Standalone-Kontext (test:standalone). Tests mit explizitem Override-Parameter
   (Profile.has/getActive mit boardModeOverride) sind kontextunabhängig. */
const isServerBuildCtx = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true')

/* ── PROFILES-Struktur ──────────────────────────────────────────────── */

describe('PROFILES — Struktur', () => {
  it('enthält genau vier Profile', () => {
    expect(Object.keys(PROFILES)).toHaveLength(4)
    expect(PROFILES).toHaveProperty('solo')
    expect(PROFILES).toHaveProperty('team')
    expect(PROFILES).toHaveProperty('claude-code')
    expect(PROFILES).toHaveProperty('server')
  })

  it('jedes Profil hat id und capabilities als Set', () => {
    for (const [key, profile] of Object.entries(PROFILES)) {
      expect(profile.id).toBe(key)
      expect(profile.capabilities).toBeInstanceOf(Set)
    }
  })
})

/* ── migrateBoardMode ───────────────────────────────────────────────── */

describe('migrateBoardMode()', () => {
  it('migriert standalone → solo', () => {
    expect(Profile.migrateBoardMode('standalone')).toBe('solo')
  })

  it('migriert project → claude-code', () => {
    expect(Profile.migrateBoardMode('project')).toBe('claude-code')
  })

  it('ist idempotent für solo', () => {
    expect(Profile.migrateBoardMode('solo')).toBe('solo')
  })

  it('ist idempotent für team', () => {
    expect(Profile.migrateBoardMode('team')).toBe('team')
  })

  it('ist idempotent für claude-code', () => {
    expect(Profile.migrateBoardMode('claude-code')).toBe('claude-code')
  })

  it('ist idempotent für server', () => {
    expect(Profile.migrateBoardMode('server')).toBe('server')
  })

  it('unbekannter Wert fällt auf solo zurück', () => {
    expect(Profile.migrateBoardMode('unknown')).toBe('solo')
  })

  it('leerer String fällt auf solo zurück', () => {
    expect(Profile.migrateBoardMode('')).toBe('solo')
  })
})

/* ── has() — Capability-Checks ──────────────────────────────────────── */

describe('has() — feature.polling', () => {
  /* Standalone-only: Config.set('boardMode','claude-code') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist true bei claude-code', () => {
    Config.set('boardMode', 'claude-code')
    expect(Profile.has('feature.polling')).toBe(true)
  })

  it('ist true bei server', () => {
    expect(Profile.has('feature.polling', 'server')).toBe(true)
  })

  /* Standalone-only: Config.set('boardMode','solo') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei solo', () => {
    Config.set('boardMode', 'solo')
    expect(Profile.has('feature.polling')).toBe(false)
  })

  /* Standalone-only: Config.set('boardMode','team') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei team', () => {
    Config.set('boardMode', 'team')
    expect(Profile.has('feature.polling')).toBe(false)
  })
})

/* F-194: Capability-Trennung zwischen Polling (Sync-Refresh) und
   FSA-Volume-Attachments (Filesystem-Pfad). Server-Profil hat zwar polling=true,
   aber Attachments laufen via REST — nicht FSA. */
describe('has() — feature.fsaVolumeAttachments (F-194)', () => {
  /* Standalone-only: Config.set('boardMode','claude-code') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist true bei claude-code', () => {
    Config.set('boardMode', 'claude-code')
    expect(Profile.has('feature.fsaVolumeAttachments')).toBe(true)
  })

  it('ist false bei server (REST-Upload statt FSA)', () => {
    expect(Profile.has('feature.fsaVolumeAttachments', 'server')).toBe(false)
  })

  /* Standalone-only: Config.set('boardMode','solo') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei solo (Attachments base64 im HTML)', () => {
    Config.set('boardMode', 'solo')
    expect(Profile.has('feature.fsaVolumeAttachments')).toBe(false)
  })

  /* Standalone-only: Config.set('boardMode','team') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei team', () => {
    Config.set('boardMode', 'team')
    expect(Profile.has('feature.fsaVolumeAttachments')).toBe(false)
  })
})

describe('has() — feature.agentContext', () => {
  /* Standalone-only: Config.set('boardMode','claude-code') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist true bei claude-code', () => {
    Config.set('boardMode', 'claude-code')
    expect(Profile.has('feature.agentContext')).toBe(true)
  })

  it('ist true bei server', () => {
    expect(Profile.has('feature.agentContext', 'server')).toBe(true)
  })

  /* Standalone-only: Config.set('boardMode','solo') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei solo', () => {
    Config.set('boardMode', 'solo')
    expect(Profile.has('feature.agentContext')).toBe(false)
  })

  /* Standalone-only: Config.set('boardMode','team') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei team', () => {
    Config.set('boardMode', 'team')
    expect(Profile.has('feature.agentContext')).toBe(false)
  })
})

describe('has() — feature.attachmentLimits', () => {
  /* Standalone-only: Config.set('boardMode','solo') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist true bei solo', () => {
    Config.set('boardMode', 'solo')
    expect(Profile.has('feature.attachmentLimits')).toBe(true)
  })

  /* Standalone-only: Config.set('boardMode','team') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist true bei team', () => {
    Config.set('boardMode', 'team')
    expect(Profile.has('feature.attachmentLimits')).toBe(true)
  })

  /* Standalone-only: Config.set('boardMode','claude-code') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei claude-code', () => {
    Config.set('boardMode', 'claude-code')
    expect(Profile.has('feature.attachmentLimits')).toBe(false)
  })

  it('ist true bei server (Karte #450 — der PUT-Body ist die Grenze, nicht die Platte)', () => {
    /* Bis Sprint 61 stand hier `false`, begruendet mit „Server hat ausreichend
       Volume-Platz". Die Annahme trifft nicht zu: Ein Anhang landet im
       Server-Modus gar nicht auf dem Volume, sondern reist als base64 im
       Board-State (kein `feature.fsaVolumeAttachments`, F-194) und geht bei
       jedem Speichern gegen `MaxBodyBytes = 10 MB`. Ohne das Flag stieg
       `checkUpload` dort sofort mit `{ok:true}` aus — der Nutzer bekam statt
       einer Erklaerung ein „Speichern fehlgeschlagen" (Meldung, #450).
       Wird der Upload auf das REST-Plugin umgestellt (Punkt 1 der Karte),
       gehoert diese Zeile erneut geprueft. */
    expect(Profile.has('feature.attachmentLimits', 'server')).toBe(true)
  })
})

describe('has() — feature.ownerClaude', () => {
  /* Standalone-only: Config.set('boardMode','claude-code') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist true bei claude-code', () => {
    Config.set('boardMode', 'claude-code')
    expect(Profile.has('feature.ownerClaude')).toBe(true)
  })

  it('ist true bei server', () => {
    expect(Profile.has('feature.ownerClaude', 'server')).toBe(true)
  })

  /* Standalone-only: Config.set('boardMode','solo') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei solo', () => {
    Config.set('boardMode', 'solo')
    expect(Profile.has('feature.ownerClaude')).toBe(false)
  })

  /* Standalone-only: Config.set('boardMode','team') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei team', () => {
    Config.set('boardMode', 'team')
    expect(Profile.has('feature.ownerClaude')).toBe(false)
  })
})

describe('has() — feature.complexityEstimate', () => {
  /* Standalone-only: Config.set('boardMode','claude-code') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist true bei claude-code', () => {
    Config.set('boardMode', 'claude-code')
    expect(Profile.has('feature.complexityEstimate')).toBe(true)
  })

  it('ist true bei server', () => {
    expect(Profile.has('feature.complexityEstimate', 'server')).toBe(true)
  })

  /* Standalone-only: Config.set('boardMode','solo') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei solo', () => {
    Config.set('boardMode', 'solo')
    expect(Profile.has('feature.complexityEstimate')).toBe(false)
  })
})

describe('has() — feature.userNameMulti', () => {
  /* Standalone-only: Config.set('boardMode','team') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist true bei team', () => {
    Config.set('boardMode', 'team')
    expect(Profile.has('feature.userNameMulti')).toBe(true)
  })

  /* Standalone-only: Config.set('boardMode','solo') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei solo', () => {
    Config.set('boardMode', 'solo')
    expect(Profile.has('feature.userNameMulti')).toBe(false)
  })

  /* Standalone-only: Config.set('boardMode','claude-code') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('ist false bei claude-code', () => {
    Config.set('boardMode', 'claude-code')
    expect(Profile.has('feature.userNameMulti')).toBe(false)
  })
})

/* ── getActive() — liest Config ─────────────────────────────────────── */

describe('getActive()', () => {
  /* Standalone-only: Config.set('boardMode','solo') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('liefert solo-Profil bei boardMode=solo', () => {
    Config.set('boardMode', 'solo')
    expect(Profile.getActive().id).toBe('solo')
  })

  /* Standalone-only: Config.set('boardMode','team') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('liefert team-Profil bei boardMode=team', () => {
    Config.set('boardMode', 'team')
    expect(Profile.getActive().id).toBe('team')
  })

  /* Standalone-only: Config.set('boardMode','claude-code') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('liefert claude-code-Profil bei boardMode=claude-code', () => {
    Config.set('boardMode', 'claude-code')
    expect(Profile.getActive().id).toBe('claude-code')
  })

  it('liefert server-Profil bei boardMode=server', () => {
    expect(Profile.getActive('server').id).toBe('server')
  })

  /* Standalone-only: Im Server-Kontext mappt Config 'standalone' → bleibt 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('migriert Legacy-Wert standalone → solo automatisch', () => {
    /* Config.set setzt den Wert direkt, ohne Migration — daher manuell */
    Config.set('boardMode', 'standalone')
    expect(Profile.getActive().id).toBe('solo')
  })

  /* Standalone-only: Im Server-Kontext mappt Config 'project' → bleibt 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('migriert Legacy-Wert project → claude-code automatisch', () => {
    Config.set('boardMode', 'project')
    expect(Profile.getActive().id).toBe('claude-code')
  })

  /* Standalone-only: Im Server-Kontext fällt unbekannter Wert auf 'server' statt 'solo' (F-272) */
  it.skipIf(isServerBuildCtx)('fällt bei unbekanntem Wert auf solo zurück', () => {
    Config.set('boardMode', 'unbekannt')
    expect(Profile.getActive().id).toBe('solo')
  })

  /* Standalone-only: Config.set('boardMode','solo') clampt im Server-Kontext auf 'server' (F-272) */
  it.skipIf(isServerBuildCtx)('akzeptiert optionalen boardModeOverride', () => {
    Config.set('boardMode', 'solo')
    expect(Profile.getActive('claude-code').id).toBe('claude-code')
  })
})

/* ── has() mit boardModeOverride ────────────────────────────────────── */

describe('has() mit boardModeOverride', () => {
  it('nutzt Override statt aktiven Config-Wert', () => {
    Config.set('boardMode', 'solo')
    /* Auch wenn Config=solo, fragt der Override nach claude-code */
    expect(Profile.has('feature.polling', 'claude-code')).toBe(true)
  })

  it('liefert false für solo-Override bei polling', () => {
    Config.set('boardMode', 'claude-code')
    expect(Profile.has('feature.polling', 'solo')).toBe(false)
  })
})

/* ── F-182: Config.set/load — Legacy-Migration + F-186 Guard ─────────── */

/* Standalone-only (F-272): Diese describe-Gruppe prüft ausschließlich das Standalone-
   boardMode-Validierungsverhalten (VALID_BOARD_MODES={solo,team,claude-code}) und Legacy-
   Migration. Im Server-Build-Kontext (test:server) gilt VALID_BOARD_MODES={'server'} —
   Config.set('boardMode','solo/team/claude-code') clampt auf 'server', alle Erwartungen
   wären grundlegend anders. Die Gruppe läuft nur im Standalone-Kontext (test:standalone). */
describe.skipIf(isServerBuildCtx)('Config-Migration + Guard (F-182, F-186)', () => {
  it('Config.set mappt Legacy-standalone auf solo', () => {
    Config.set('boardMode', 'standalone')
    expect(Config.get('boardMode')).toBe('solo')
  })

  it('Config.set mappt Legacy-project auf claude-code', () => {
    Config.set('boardMode', 'project')
    expect(Config.get('boardMode')).toBe('claude-code')
  })

  it('Config.set blockt unbekannte Werte und klemmt auf solo', () => {
    Config.set('boardMode', 'foo-bar')
    expect(Config.get('boardMode')).toBe('solo')
  })

  it('Config.set blockt das disabled Server-Profil (Phase 2)', () => {
    /* F-186: server ist im MVP nicht setzbar — sonst landet User im toten Modus */
    Config.set('boardMode', 'server')
    expect(Config.get('boardMode')).toBe('solo')
  })

  it('Config.set lässt gültige Profile-Werte unverändert', () => {
    Config.set('boardMode', 'team')
    expect(Config.get('boardMode')).toBe('team')
    Config.set('boardMode', 'claude-code')
    expect(Config.get('boardMode')).toBe('claude-code')
    Config.set('boardMode', 'solo')
    expect(Config.get('boardMode')).toBe('solo')
  })

  /* F-183: Defense-in-depth — präparierte Werte (__proto__, constructor) dürfen
     Profile.has() nicht zu Wahr-Werten ausserhalb der Capability-Liste verleiten.
     Config.set klemmt sie auf 'solo'; Profile.has fällt auf solo-Capabilities. */
  it('Profile.has bleibt sicher bei prototype-pollution-Versuchen', () => {
    Config.set('boardMode', '__proto__')
    expect(Profile.has('feature.polling')).toBe(false)
    expect(Profile.has('feature.agentContext')).toBe(false)
    /* solo behält nur attachmentLimits */
    expect(Profile.has('feature.attachmentLimits')).toBe(true)

    Config.set('boardMode', 'constructor')
    expect(Profile.has('feature.polling')).toBe(false)
    Config.set('boardMode', 'solo')
  })
})
