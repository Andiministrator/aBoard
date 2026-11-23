/* @vitest-environment happy-dom */
/* Unit-Tests: Storage-Plugin-Interface (ADR-0044, Karte #67)

   Verifiziert die schmale Fassade über Storage und Sync:
   - Factory liefert das Plugin passend zu Config.get('boardMode')
   - Plugin-Instanzen sind pro `kind` gecacht
   - Standalone-Plugin delegiert an Storage, kein Polling
   - Project-Plugin delegiert an Sync, Polling unterstützt
   - autoPersist liefert die richtige Erfolgs-Semantik

   Karte #203 / ADR-0064 / F-272: Zwei Test-Kontexte (beide Pflicht via `bun run test`):
   - test:standalone (vitest.config.js, __SERVER_BUILD__=false):
     REST-Plugin-Code abwesend (DCE) → createRESTPlugin null → REST-Tests skipIf(!hasRESTPlugin).
     Factory-Tests für solo/team/claude-code laufen hier.
   - test:server (vitest.config.server.js, __SERVER_BUILD__=true, F-272):
     REST-Plugin-Code vorhanden → createRESTPlugin ist eine Funktion → REST-Tests laufen.
     Factory-Tests für solo/team/claude-code werden oversprungen (boardMode='server' einziger gültiger Wert).
*/

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { StoragePlugin, __testing__ } from '../src/storage-plugin.js'
import { Storage } from '../src/storage.js'
import { Sync }    from '../src/sync.js'
import { Config }  from '../src/config.js'

/* Karte #203 / ADR-0064: REST-Plugin-Code nur im Server-Build vorhanden.
   Im Standalone-Kontext (test:standalone, IS_SERVER_BUILD=false) ist createRESTPlugin null
   → REST-Tests überspringen.
   Im Server-Kontext (test:server, IS_SERVER_BUILD=true, F-272) ist createRESTPlugin vorhanden
   → REST-Tests laufen und bestehen. */
const hasRESTPlugin = typeof __testing__.createRESTPlugin === 'function'

/* F-272 / ADR-0064: Im Server-Build-Kontext (test:server) ist boardMode='server' der einzige
   gültige Wert — Factory-Tests die solo/team/claude-code als boardMode setzen, sind
   Standalone-only (laufen nur im test:standalone-Kontext). */
const isServerBuildCtx = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true')

describe('Storage-Plugin — Factory', () => {
  beforeEach(() => {
    /* Cache vor jedem Test leeren — via Modul-Reset wäre sauberer, aber selectPlugin
       cacht idempotent und überschreibt; die Plugin-Identität pro kind bleibt stabil. */
  })

  /* Standalone-only (F-272): Im Server-Kontext clampt Config.set('boardMode','solo') auf
     'server' — getActivePlugin() liefert dann das REST-Plugin, nicht das Standalone-Plugin. */
  it.skipIf(isServerBuildCtx)('getActivePlugin() liefert in DEFAULT-Config (solo) das Standalone-Plugin', () => {
    Config.set('boardMode', 'solo')
    const p = StoragePlugin.getActivePlugin()
    expect(p.kind).toBe('standalone')
  })

  /* Standalone-only (F-272): Analog zu oben für boardMode='team'. */
  it.skipIf(isServerBuildCtx)('getActivePlugin() liefert Standalone-Plugin auch für team-Profil', () => {
    Config.set('boardMode', 'team')
    const p = StoragePlugin.getActivePlugin()
    expect(p.kind).toBe('standalone')
  })

  /* Standalone-only (F-272): Config.set('boardMode','claude-code') clampt im Server-Kontext
     auf 'server' — getActivePlugin() liefert REST-Plugin statt Project-Plugin. */
  it.skipIf(isServerBuildCtx)('getActivePlugin() liefert Project-Plugin wenn boardMode=claude-code', () => {
    Config.set('boardMode', 'claude-code')
    const p = StoragePlugin.getActivePlugin()
    expect(p.kind).toBe('project')
  })

  it('getActivePlugin() Legacy-Werte standalone/project werden noch akzeptiert (Übergangs-Kompat)', () => {
    /* F-181: Legacy-Werte sollten via Config.load()-Migration verschwinden, aber
       selectPlugin akzeptiert sie defensiv als zusätzliche Sicherung. */
    Config.set('boardMode', 'project')
    expect(StoragePlugin.selectPlugin('project').kind).toBe('project')
    Config.set('boardMode', 'solo')
  })

  it('selectPlugin() cacht Instanzen pro kind', () => {
    const a = StoragePlugin.selectPlugin('standalone')
    const b = StoragePlugin.selectPlugin('standalone')
    expect(a).toBe(b)

    const p1 = StoragePlugin.selectPlugin('project')
    const p2 = StoragePlugin.selectPlugin('project')
    expect(p1).toBe(p2)

    /* Verschiedene Kinds → verschiedene Instanzen */
    expect(a).not.toBe(p1)
  })

  it('selectPlugin() mit unbekanntem kind fällt auf Standalone zurück', () => {
    const p = StoragePlugin.selectPlugin('foo')
    expect(p.kind).toBe('standalone')
  })
})

describe('Storage-Plugin — Standalone-Implementierung', () => {
  let plugin

  beforeEach(() => {
    plugin = __testing__.createStandalonePlugin()
  })

  it('kind === "standalone"', () => {
    expect(plugin.kind).toBe('standalone')
  })

  it('isPollingSupported() ist false', () => {
    expect(plugin.isPollingSupported()).toBe(false)
  })

  it('isPollingActive() ist immer false', () => {
    expect(plugin.isPollingActive()).toBe(false)
  })

  it('startPolling()/stopPolling() sind no-ops (werfen nicht)', () => {
    expect(() => plugin.startPolling(() => {})).not.toThrow()
    expect(() => plugin.stopPolling()).not.toThrow()
  })

  it('autoPersist() delegiert an Storage.autoSave (mit projectMode=false) und liefert true', async () => {
    const spy = vi.spyOn(Storage, 'autoSave').mockImplementation(() => {})
    const state = { version: '2', board: { id: 'b-1', title: 'Test', sprints: [] } }
    const ok = await plugin.autoPersist(state)
    expect(spy).toHaveBeenCalledWith(state, false)
    expect(ok).toBe(true)
    spy.mockRestore()
  })

  it('save() delegiert an Storage.save mit dirHandle=null', async () => {
    const spy = vi.spyOn(Storage, 'save').mockResolvedValue({ ok: true, method: 'fsa' })
    const state = { version: '2', board: { id: 'b-1', title: 'Test', sprints: [] } }
    const result = await plugin.save(state)
    expect(spy).toHaveBeenCalledWith(state, null)
    expect(result.ok).toBe(true)
    spy.mockRestore()
  })

  it('listKnownBoards() delegiert an Storage.listKnownFiles', async () => {
    const spy = vi.spyOn(Storage, 'listKnownFiles').mockResolvedValue([{ id: 'b-1', name: 'aboard.html' }])
    const list = await plugin.listKnownBoards()
    expect(spy).toHaveBeenCalled()
    expect(list).toEqual([{ id: 'b-1', name: 'aboard.html' }])
    spy.mockRestore()
  })

  it('preload() ruft Storage.preloadFileHandle wenn boardId gegeben', async () => {
    const spy = vi.spyOn(Storage, 'preloadFileHandle').mockResolvedValue(undefined)
    await plugin.preload('b-123')
    expect(spy).toHaveBeenCalledWith('b-123')
    spy.mockRestore()
  })

  it('preload() ohne boardId ruft Storage.preloadFileHandle NICHT', async () => {
    const spy = vi.spyOn(Storage, 'preloadFileHandle').mockResolvedValue(undefined)
    await plugin.preload()
    expect(spy).not.toHaveBeenCalled()
    spy.mockRestore()
  })

  it('parseAndMigrate() parst JSON und delegiert an Storage.checkVersion (F-187)', () => {
    const migrated = { version: '2', board: { id: 'b-1', title: 'Test', sprints: [] } }
    const spy = vi.spyOn(Storage, 'checkVersion').mockReturnValue(migrated)
    const raw = JSON.stringify({ version: '1', board: { id: 'b-1' } })
    const result = plugin.parseAndMigrate(raw)
    expect(spy).toHaveBeenCalled()
    expect(result).toBe(migrated)
    spy.mockRestore()
  })

  it('parseAndMigrate() liefert null bei ungültigem JSON', () => {
    const result = plugin.parseAndMigrate('kein-json{{{')
    expect(result).toBeNull()
  })
})

describe('Storage-Plugin — Project-Implementierung', () => {
  let plugin

  beforeEach(() => {
    plugin = __testing__.createProjectPlugin()
  })

  it('kind === "project"', () => {
    expect(plugin.kind).toBe('project')
  })

  it('isPollingSupported() ist true', () => {
    expect(plugin.isPollingSupported()).toBe(true)
  })

  /* ADR-0070 §2 (F-291): Das Project-Plugin gated auf Sync.isMigrated().
     migriert  → ADR-0067-Projektor (Diff→Ops→Inbox, kein Konflikt-Kanal).
     nicht mig.→ Pre-#228 Legacy Sync.write + ADR-0050-Konflikt-Übersetzung. */

  it('migriert: autoPersist() delegiert an Sync.projectorPersist, true bei Erfolg', async () => {
    const mig  = vi.spyOn(Sync, 'isMigrated').mockResolvedValue(true)
    const spy  = vi.spyOn(Sync, 'projectorPersist').mockResolvedValue(true)
    const state = { version: '2', board: { id: 'b-1', title: 'Test', sprints: [] } }
    const ok = await plugin.autoPersist(state)
    expect(spy).toHaveBeenCalledWith(state, expect.any(String))
    expect(ok).toBe(true)
    mig.mockRestore(); spy.mockRestore()
  })

  it('migriert: autoPersist() false wenn projectorPersist false', async () => {
    const mig = vi.spyOn(Sync, 'isMigrated').mockResolvedValue(true)
    const spy = vi.spyOn(Sync, 'projectorPersist').mockResolvedValue(false)
    const ok = await plugin.autoPersist({ version: '2', board: { id: 'b-1' } })
    expect(ok).toBe(false)
    mig.mockRestore(); spy.mockRestore()
  })

  it('migriert: autoPersist() liefert boolean (kein Konflikt-Kanal im Projektor-Pfad)', async () => {
    const mig = vi.spyOn(Sync, 'isMigrated').mockResolvedValue(true)
    const spy = vi.spyOn(Sync, 'projectorPersist').mockResolvedValue(true)
    const res = await plugin.autoPersist({ version: '2', board: { id: 'mine' } })
    expect(res).toBe(true)
    expect(typeof res).toBe('boolean')
    mig.mockRestore(); spy.mockRestore()
  })

  it('NICHT migriert: autoPersist() nutzt Legacy Sync.write; Konflikt → kind:fsa-Rückkanal (ADR-0050)', async () => {
    const mig = vi.spyOn(Sync, 'isMigrated').mockResolvedValue(false)
    const wr  = vi.spyOn(Sync, 'write').mockResolvedValue({ conflict: true, diskState: { version: '2', board: { id: 'd' } }, diskRaw: '{}' })
    const cv  = vi.spyOn(Storage, 'checkVersion').mockReturnValue({ version: '2', board: { id: 'd' } })
    const res = await plugin.autoPersist({ version: '2', board: { id: 'mine' } })
    expect(res).toEqual({ ok: false, conflict: { serverState: { version: '2', board: { id: 'd' } }, diskRaw: '{}', kind: 'fsa' } })
    mig.mockRestore(); wr.mockRestore(); cv.mockRestore()
  })

  it('NICHT migriert: autoPersist() true wenn Sync.write true', async () => {
    const mig = vi.spyOn(Sync, 'isMigrated').mockResolvedValue(false)
    const wr  = vi.spyOn(Sync, 'write').mockResolvedValue(true)
    const ok  = await plugin.autoPersist({ version: '2', board: { id: 'b' } })
    expect(ok).toBe(true)
    mig.mockRestore(); wr.mockRestore()
  })

  it('migriert: save() ruft Sync.projectorPersist UND Storage.save mit dirHandle (ADR-0014)', async () => {
    const mig        = vi.spyOn(Sync, 'isMigrated').mockResolvedValue(true)
    const persistSpy = vi.spyOn(Sync, 'projectorPersist').mockResolvedValue(true)
    const dirSpy     = vi.spyOn(Sync, 'getDirectoryHandle').mockReturnValue({ kind: 'directory', name: 'proj' })
    const saveSpy    = vi.spyOn(Storage, 'save').mockResolvedValue({ ok: true, method: 'fsa' })
    const state = { version: '2', board: { id: 'b-1', title: 'Test', sprints: [] } }
    const result = await plugin.save(state)
    expect(persistSpy).toHaveBeenCalledWith(state, expect.any(String))
    expect(saveSpy).toHaveBeenCalledWith(state, { kind: 'directory', name: 'proj' })
    expect(result.ok).toBe(true)
    mig.mockRestore(); persistSpy.mockRestore(); dirSpy.mockRestore(); saveSpy.mockRestore()
  })

  it('NICHT migriert: save() bei Konflikt → kind:fsa, KEIN Storage.save (kein aboard.html-Write)', async () => {
    const mig     = vi.spyOn(Sync, 'isMigrated').mockResolvedValue(false)
    const wr      = vi.spyOn(Sync, 'write').mockResolvedValue({ conflict: true, diskState: { version: '2', board: { id: 'd' } }, diskRaw: '{}' })
    const cv      = vi.spyOn(Storage, 'checkVersion').mockReturnValue({ version: '2', board: { id: 'd' } })
    const saveSpy = vi.spyOn(Storage, 'save').mockResolvedValue({ ok: true })
    const result  = await plugin.save({ version: '2', board: { id: 'mine' } })
    expect(result).toEqual({ ok: false, conflict: { serverState: { version: '2', board: { id: 'd' } }, diskRaw: '{}', kind: 'fsa' } })
    expect(saveSpy).not.toHaveBeenCalled()
    mig.mockRestore(); wr.mockRestore(); cv.mockRestore(); saveSpy.mockRestore()
  })

  it('startPolling() delegiert an Sync.startPolling', () => {
    const spy = vi.spyOn(Sync, 'startPolling').mockImplementation(() => {})
    const cb = () => {}
    plugin.startPolling(cb)
    expect(spy).toHaveBeenCalledWith(cb)
    spy.mockRestore()
  })

  it('stopPolling() delegiert an Sync.stopPolling', () => {
    const spy = vi.spyOn(Sync, 'stopPolling').mockImplementation(() => {})
    plugin.stopPolling()
    expect(spy).toHaveBeenCalled()
    spy.mockRestore()
  })

  it('isPollingActive() folgt Sync.isActive()', () => {
    const spy = vi.spyOn(Sync, 'isActive').mockReturnValue(true)
    expect(plugin.isPollingActive()).toBe(true)
    spy.mockReturnValue(false)
    expect(plugin.isPollingActive()).toBe(false)
    spy.mockRestore()
  })

  it('preload() ruft Sync.tryRestoreHandle wenn boardId gegeben', async () => {
    const spy = vi.spyOn(Sync, 'tryRestoreHandle').mockResolvedValue(true)
    await plugin.preload('b-123')
    expect(spy).toHaveBeenCalledWith('b-123')
    spy.mockRestore()
  })

  it('listKnownBoards() delegiert an Sync.listKnownBoards', async () => {
    const spy = vi.spyOn(Sync, 'listKnownBoards').mockResolvedValue([{ id: 'b-1', name: 'proj-dir' }])
    const list = await plugin.listKnownBoards()
    expect(spy).toHaveBeenCalled()
    expect(list).toEqual([{ id: 'b-1', name: 'proj-dir' }])
    spy.mockRestore()
  })

  it('parseAndMigrate() parst JSON und delegiert an Storage.checkVersion (F-187)', () => {
    const migrated = { version: '2', board: { id: 'b-1', title: 'Test', sprints: [] } }
    const spy = vi.spyOn(Storage, 'checkVersion').mockReturnValue(migrated)
    const raw = JSON.stringify({ version: '1', board: { id: 'b-1' } })
    const result = plugin.parseAndMigrate(raw)
    expect(spy).toHaveBeenCalled()
    expect(result).toBe(migrated)
    spy.mockRestore()
  })

  it('parseAndMigrate() liefert null bei ungültigem JSON', () => {
    const result = plugin.parseAndMigrate('kein-json{{{')
    expect(result).toBeNull()
  })
})

describe('Storage-Plugin — Lifecycle (init/destroy)', () => {
  /* Vertragsmethoden aus ADR-0044: init/destroy existieren in beiden Plugins,
     sind asynchron, idempotent und werfen nicht. */

  it('Standalone.init() ist async und wirft nicht', async () => {
    const plugin = __testing__.createStandalonePlugin()
    await expect(plugin.init()).resolves.toBeUndefined()
  })

  it('Standalone.init() ist idempotent (mehrfacher Aufruf wirft nicht — F-193)', async () => {
    const plugin = __testing__.createStandalonePlugin()
    await plugin.init()
    await expect(plugin.init()).resolves.toBeUndefined()
  })

  it('Standalone.destroy() ist async und wirft nicht', async () => {
    const plugin = __testing__.createStandalonePlugin()
    await expect(plugin.destroy()).resolves.toBeUndefined()
  })

  it('Standalone.destroy() ist idempotent (mehrfacher Aufruf wirft nicht)', async () => {
    const plugin = __testing__.createStandalonePlugin()
    await plugin.destroy()
    await expect(plugin.destroy()).resolves.toBeUndefined()
  })

  it('Project.init() ist async und wirft nicht', async () => {
    const plugin = __testing__.createProjectPlugin()
    await expect(plugin.init()).resolves.toBeUndefined()
  })

  it('Project.init() ist idempotent (mehrfacher Aufruf wirft nicht — F-193)', async () => {
    const plugin = __testing__.createProjectPlugin()
    await plugin.init()
    await expect(plugin.init()).resolves.toBeUndefined()
  })

  it('Project.destroy() stoppt Polling UND vergisst den aktiven Handle', async () => {
    const stopSpy  = vi.spyOn(Sync, 'stopPolling').mockImplementation(() => {})
    const clearSpy = vi.spyOn(Sync, 'clearActiveHandle').mockImplementation(() => {})
    const plugin = __testing__.createProjectPlugin()
    await plugin.destroy()
    expect(stopSpy).toHaveBeenCalled()
    expect(clearSpy).toHaveBeenCalled()
    stopSpy.mockRestore(); clearSpy.mockRestore()
  })

  it('Project.destroy() ist idempotent — zweiter Aufruf wirft nicht', async () => {
    const stopSpy  = vi.spyOn(Sync, 'stopPolling').mockImplementation(() => {})
    const clearSpy = vi.spyOn(Sync, 'clearActiveHandle').mockImplementation(() => {})
    const plugin = __testing__.createProjectPlugin()
    await plugin.destroy()
    await expect(plugin.destroy()).resolves.toBeUndefined()
    stopSpy.mockRestore(); clearSpy.mockRestore()
  })
})

describe('Storage-Plugin — switchBoard(boardId)', () => {
  /* Vertrag aus ADR-0044: { ok, state?, error? } — Aufrufer fängt error-Codes
     ab und mappt sie auf Toast-Strings. Im Fehlerpfad bleibt kein halb-
     restorierter Handle zurück. */

  it('Standalone.switchBoard() delegiert an Storage.openByBoardId', async () => {
    const state = { version: '2', board: { id: 'b-2', title: 'Anderes', sprints: [] } }
    const spy = vi.spyOn(Storage, 'openByBoardId').mockResolvedValue(state)
    const plugin = __testing__.createStandalonePlugin()
    const result = await plugin.switchBoard('b-2')
    expect(spy).toHaveBeenCalledWith('b-2')
    expect(result).toEqual({ ok: true, state })
    spy.mockRestore()
  })

  it('Standalone.switchBoard() liefert error=no-handle wenn openByBoardId null gibt', async () => {
    const spy = vi.spyOn(Storage, 'openByBoardId').mockResolvedValue(null)
    const plugin = __testing__.createStandalonePlugin()
    const result = await plugin.switchBoard('b-x')
    expect(result.ok).toBe(false)
    expect(result.error).toBe('no-handle')
    spy.mockRestore()
  })

  it('Standalone.switchBoard() liefert error=invalid-file wenn State ohne board', async () => {
    const spy = vi.spyOn(Storage, 'openByBoardId').mockResolvedValue({ version: '2' })
    const plugin = __testing__.createStandalonePlugin()
    const result = await plugin.switchBoard('b-x')
    expect(result.ok).toBe(false)
    expect(result.error).toBe('invalid-file')
    spy.mockRestore()
  })

  it('Project.switchBoard() vollständiger Erfolgs-Pfad: stop+clear → restore → read → migrate', async () => {
    const stopSpy    = vi.spyOn(Sync, 'stopPolling').mockImplementation(() => {})
    const clearSpy   = vi.spyOn(Sync, 'clearActiveHandle').mockImplementation(() => {})
    const restoreSpy = vi.spyOn(Sync, 'tryRestoreHandle').mockResolvedValue(true)
    const permSpy    = vi.spyOn(Sync, 'isPermissionNeeded').mockReturnValue(false)
    const stateLike  = { version: '2', board: { id: 'b-3', title: 'X', sprints: [] } }
    const readSpy    = vi.spyOn(Sync, 'read').mockResolvedValue(JSON.stringify(stateLike))
    const checkSpy   = vi.spyOn(Storage, 'checkVersion').mockReturnValue(stateLike)

    const plugin = __testing__.createProjectPlugin()
    const result = await plugin.switchBoard('b-3')

    expect(stopSpy).toHaveBeenCalled()
    expect(clearSpy).toHaveBeenCalled()
    expect(restoreSpy).toHaveBeenCalledWith('b-3')
    expect(result).toEqual({ ok: true, state: stateLike })

    stopSpy.mockRestore(); clearSpy.mockRestore(); restoreSpy.mockRestore()
    permSpy.mockRestore(); readSpy.mockRestore(); checkSpy.mockRestore()
  })

  /* F-854 (Karte #488): Das Sentinel aus F-477 wird geprüft, nicht dem
     TypeError von `JSON.parse` überlassen. Vor dem Fix war das Ergebnis
     bit-identisch — `JSON.parse(Symbol)` wirft, der catch schluckt, `parsed`
     bleibt null, es kam `invalid-file` heraus. Richtig war daran nur das
     Ergebnis: Die Korrektheit hing an einem Nebeneffekt, und die Fehlerart
     war für einen transienten Lesefehler irreführend. */
  it('Project.switchBoard() liefert error=read-error beim Sentinel Sync.READ_ERROR', async () => {
    vi.spyOn(Sync, 'stopPolling').mockImplementation(() => {})
    const clearSpy = vi.spyOn(Sync, 'clearActiveHandle').mockImplementation(() => {})
    vi.spyOn(Sync, 'tryRestoreHandle').mockResolvedValue(true)
    vi.spyOn(Sync, 'isPermissionNeeded').mockReturnValue(false)
    vi.spyOn(Sync, 'read').mockResolvedValue(Sync.READ_ERROR)
    /* Der Parser darf gar nicht erst drankommen — sonst hinge das Ergebnis
       wieder an seinem Wurfverhalten statt an der Prüfung. */
    const parseSpy = vi.spyOn(JSON, 'parse')

    const plugin = __testing__.createProjectPlugin()
    const result = await plugin.switchBoard('b-3')

    expect(result.ok).toBe(false)
    expect(result.error).toBe('read-error')
    expect(parseSpy).not.toHaveBeenCalled()
    /* Aufräumen bleibt Sache des Plugins (ADR-0059 §Aufrufer-Schicht). */
    expect(clearSpy).toHaveBeenCalled()
    vi.restoreAllMocks()
  })

  it('Project.switchBoard() liefert weiterhin invalid-file, wenn die Datei fehlt', async () => {
    /* Gegenrichtung: Die neue Verzweigung darf den bestehenden Fall nicht
       mit übernehmen — `null` ist "fehlt", nicht "unlesbar". */
    vi.spyOn(Sync, 'stopPolling').mockImplementation(() => {})
    vi.spyOn(Sync, 'clearActiveHandle').mockImplementation(() => {})
    vi.spyOn(Sync, 'tryRestoreHandle').mockResolvedValue(true)
    vi.spyOn(Sync, 'isPermissionNeeded').mockReturnValue(false)
    vi.spyOn(Sync, 'read').mockResolvedValue(null)
    const plugin = __testing__.createProjectPlugin()
    const result = await plugin.switchBoard('b-3')
    expect(result).toEqual({ ok: false, error: 'invalid-file' })
    vi.restoreAllMocks()
  })

  it('Project.switchBoard() liefert error=no-handle wenn tryRestoreHandle false', async () => {
    vi.spyOn(Sync, 'stopPolling').mockImplementation(() => {})
    vi.spyOn(Sync, 'clearActiveHandle').mockImplementation(() => {})
    const restoreSpy = vi.spyOn(Sync, 'tryRestoreHandle').mockResolvedValue(false)
    const plugin = __testing__.createProjectPlugin()
    const result = await plugin.switchBoard('b-unknown')
    expect(result.ok).toBe(false)
    expect(result.error).toBe('no-handle')
    expect(restoreSpy).toHaveBeenCalledWith('b-unknown')
    vi.restoreAllMocks()
  })

  it('Project.switchBoard() fragt Permission an und liefert error=permission-denied bei Ablehnung', async () => {
    vi.spyOn(Sync, 'stopPolling').mockImplementation(() => {})
    const clearSpy = vi.spyOn(Sync, 'clearActiveHandle').mockImplementation(() => {})
    vi.spyOn(Sync, 'tryRestoreHandle').mockResolvedValue(true)
    vi.spyOn(Sync, 'isPermissionNeeded').mockReturnValue(true)
    const reqSpy = vi.spyOn(Sync, 'requestPermission').mockResolvedValue(false)

    const plugin = __testing__.createProjectPlugin()
    const result = await plugin.switchBoard('b-4')

    expect(reqSpy).toHaveBeenCalled()
    expect(result).toEqual({ ok: false, error: 'permission-denied' })
    /* Fehlerpfad räumt auf — nicht nur das initiale clear, sondern zusätzlich nach Ablehnung */
    expect(clearSpy.mock.calls.length).toBeGreaterThanOrEqual(2)
    vi.restoreAllMocks()
  })

  it('Project.switchBoard() liefert error=invalid-file wenn Sync.read null gibt', async () => {
    vi.spyOn(Sync, 'stopPolling').mockImplementation(() => {})
    vi.spyOn(Sync, 'clearActiveHandle').mockImplementation(() => {})
    vi.spyOn(Sync, 'tryRestoreHandle').mockResolvedValue(true)
    vi.spyOn(Sync, 'isPermissionNeeded').mockReturnValue(false)
    vi.spyOn(Sync, 'read').mockResolvedValue(null)
    const plugin = __testing__.createProjectPlugin()
    const result = await plugin.switchBoard('b-5')
    expect(result).toEqual({ ok: false, error: 'invalid-file' })
    vi.restoreAllMocks()
  })

  it('Project.switchBoard() liefert error=invalid-file wenn checkVersion null/ohne board liefert', async () => {
    vi.spyOn(Sync, 'stopPolling').mockImplementation(() => {})
    vi.spyOn(Sync, 'clearActiveHandle').mockImplementation(() => {})
    vi.spyOn(Sync, 'tryRestoreHandle').mockResolvedValue(true)
    vi.spyOn(Sync, 'isPermissionNeeded').mockReturnValue(false)
    vi.spyOn(Sync, 'read').mockResolvedValue('{"version":"2"}')
    vi.spyOn(Storage, 'checkVersion').mockReturnValue(null)
    const plugin = __testing__.createProjectPlugin()
    const result = await plugin.switchBoard('b-6')
    expect(result).toEqual({ ok: false, error: 'invalid-file' })
    vi.restoreAllMocks()
  })
})

describe('Storage-Plugin — attachments()', () => {
  /* Vertrag aus ADR-0044: attachments() liefert ein AttachmentBackend-Objekt
     (kein einzelnes Methoden-Bouquet auf dem Plugin selbst). Phase 1: beide
     Plugins liefern dasselbe FSA-Backend; Phase 2 (REST-Plugin) wird ein
     eigenes Backend liefern. */

  const REQUIRED_BACKEND_METHODS = [
    'uploadFile', 'readAttachment', 'attachmentUrl', 'deleteAttachment',
    'downloadAttachment', 'purgeCardAttachments',
    'uploadBoardFile', 'boardAttachmentUrl', 'boardAttachmentObjectUrl',
    'deleteBoardAttachment',
    'boardSizeLabel', 'checkUpload', 'checkBoardUpload', 'computeBoardBytes',
    'humanSize', 'safeMime',
    /* Karte #450: Auskünfte über den Speicherort. Sie stehen hier, weil
       `modal.js` und `configdialog.js` sie UNGESCHÜTZT aufrufen — ein
       Backend ohne sie stirbt beim Upload mit `TypeError`, nicht mit einer
       Fehlermeldung. ADR-0033 §Nachtrag Sprint 61 Punkt 1 erklärt sie zum
       Interface-Bestandteil; bis zum Frontend-Kritiker desselben Sprints
       stand das nur in der ADR und in keinem Test. */
    'kartenAnhangImBoardState', 'boardAnhangImBoardState',
    /* Karte #443: `boardBildVerkleinern` steht getrennt daneben, weil es
       eine andere Frage beantwortet als der Transportweg-Schalter — ob
       ein Anzeigebild durch die Leitung geht, nicht ob es im State
       liegt. `configdialog.js` ruft es ebenso ungeschützt auf. */
    'boardBildVerkleinern',
  ]

  it('Standalone.attachments() liefert ein Backend-Objekt mit dem vollen Vertrag', () => {
    const plugin  = __testing__.createStandalonePlugin()
    const backend = plugin.attachments()
    expect(typeof backend).toBe('object')
    for (const fn of REQUIRED_BACKEND_METHODS) {
      expect(typeof backend[fn]).toBe('function')
    }
  })

  it('Project.attachments() liefert ein Backend-Objekt mit dem vollen Vertrag', () => {
    const plugin  = __testing__.createProjectPlugin()
    const backend = plugin.attachments()
    for (const fn of REQUIRED_BACKEND_METHODS) {
      expect(typeof backend[fn]).toBe('function')
    }
  })

  /* Das REST-Backend wurde hier bis Sprint 61 GAR NICHT gegen den Vertrag
     geprüft — ausgerechnet dasjenige, dessen Methoden nicht 1:1 an
     `attachments.js` delegieren, sondern eigene Implementierungen sind. Genau
     dort fehlten die drei neuen Auskünfte am längsten unbemerkt. */
  it.skipIf(!hasRESTPlugin)('REST.attachments() liefert ein Backend-Objekt mit dem vollen Vertrag', () => {
    const backend = __testing__.createRESTPlugin().attachments()
    for (const fn of REQUIRED_BACKEND_METHODS) {
      expect(typeof backend[fn], `REST-Backend fehlt ${fn} — Aufrufer rufen ungeschützt`).toBe('function')
    }
  })

  it('attachments() ist eine Factory — jeder Aufruf liefert ein eigenes Backend-Objekt', () => {
    /* Wichtig für späteres REST-Backend, das pro Aufruf einen frischen Snapshot
       liefern können soll (z. B. mit aktualisiertem Auth-Token). */
    const plugin = __testing__.createStandalonePlugin()
    const a = plugin.attachments()
    const b = plugin.attachments()
    expect(a).not.toBe(b)
  })
})

describe('Storage-Plugin — Vertrags-Komplett-Check (ADR-0044, 10 Methoden)', () => {
  const VERTRAG = [
    'init', 'destroy', 'preload', 'load', 'save', 'autoPersist',
    'switchBoard', 'startPolling', 'stopPolling', 'isPollingSupported',
    'isPollingActive', 'listKnownBoards', 'attachments',
  ]

  it('Standalone-Plugin implementiert alle Vertragsmethoden', () => {
    const plugin = __testing__.createStandalonePlugin()
    for (const m of VERTRAG) {
      expect(typeof plugin[m]).toBe('function')
    }
  })

  it('Project-Plugin implementiert alle Vertragsmethoden', () => {
    const plugin = __testing__.createProjectPlugin()
    for (const m of VERTRAG) {
      expect(typeof plugin[m]).toBe('function')
    }
  })
})

/* Standalone-only (F-272): Prüft den Modus-Wechsel claude-code→solo mit destroy()-Aufruf.
   Im Server-Kontext ist nur boardMode='server' gültig — kein Plugin-Wechsel möglich. */
describe.skipIf(isServerBuildCtx)('Storage-Plugin — Cache-Invalidation nutzt destroy()', () => {
  it('Modus-Wechsel ruft destroy() auf dem alten Plugin', async () => {
    /* F-179 + #127: getActivePlugin() ruft beim Variant-Wechsel destroy() statt
       direkt stopPolling() — räumt den gesamten Lifecycle-Zustand ab. */
    Config.set('boardMode', 'claude-code')
    const projectPlugin = StoragePlugin.getActivePlugin()
    const destroySpy = vi.spyOn(projectPlugin, 'destroy').mockResolvedValue(undefined)

    /* Wechsel auf solo → destroy() auf project-Plugin */
    Config.set('boardMode', 'solo')
    StoragePlugin.getActivePlugin()
    /* destroy() ist fire-and-forget — Microtask abwarten */
    await Promise.resolve()
    expect(destroySpy).toHaveBeenCalled()
    destroySpy.mockRestore()
  })
})

describe('Storage-Plugin — load() liefert migrierten State', () => {
  /* load() delegiert in beiden Plugins an Storage.load() — Migration ist also
     bereits durch checkVersion abgedeckt; hier nur das Delegations-Verhalten testen. */

  it('Standalone-Plugin.load() ruft Storage.load()', () => {
    const spy = vi.spyOn(Storage, 'load').mockReturnValue({ state: null, fromLocalStorage: false })
    const plugin = __testing__.createStandalonePlugin()
    const result = plugin.load()
    expect(spy).toHaveBeenCalled()
    expect(result).toEqual({ state: null, fromLocalStorage: false })
    spy.mockRestore()
  })

  it('Project-Plugin.load() ruft Storage.load() (symmetrischer Vertrag)', () => {
    const spy = vi.spyOn(Storage, 'load').mockReturnValue({ state: null, fromLocalStorage: false })
    const plugin = __testing__.createProjectPlugin()
    const result = plugin.load()
    expect(spy).toHaveBeenCalled()
    expect(result).toEqual({ state: null, fromLocalStorage: false })
    spy.mockRestore()
  })

  /* Läuft im test:server-Kontext (F-272): hasRESTPlugin=true wenn __SERVER_BUILD__=true */
  it.skipIf(!hasRESTPlugin)('REST-Plugin.load() ruft Storage.load() (symmetrischer Vertrag)', () => {
    const spy = vi.spyOn(Storage, 'load').mockReturnValue({ state: null, fromLocalStorage: false })
    const plugin = __testing__.createRESTPlugin()
    const result = plugin.load()
    expect(spy).toHaveBeenCalled()
    spy.mockRestore()
  })
})

/* ── REST-Plugin ─────────────────────────────────────────────────── */

describe.skipIf(!hasRESTPlugin)('Storage-Plugin — REST-Implementierung (Karte #78)', () => {
  let plugin

  beforeEach(() => {
    plugin = __testing__.createRESTPlugin()
    vi.stubGlobal('fetch', undefined) /* sicherheitshalber zurücksetzen */
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('kind === "rest"', () => {
    expect(plugin.kind).toBe('rest')
  })

  it('isPollingSupported() ist true', () => {
    expect(plugin.isPollingSupported()).toBe(true)
  })

  it('isPollingActive() ist false vor startPolling', () => {
    expect(plugin.isPollingActive()).toBe(false)
  })

  it('init() ist async und wirft nicht', async () => {
    await expect(plugin.init()).resolves.toBeUndefined()
  })

  it('init() ist idempotent — zweiter Aufruf wirft nicht', async () => {
    await plugin.init()
    await expect(plugin.init()).resolves.toBeUndefined()
  })

  it('destroy() ist async, stoppt Polling und löscht boardId', async () => {
    await plugin.preload('b-1')
    await plugin.destroy()
    expect(plugin.isPollingActive()).toBe(false)
  })

  it('destroy() ist idempotent', async () => {
    await plugin.destroy()
    await expect(plugin.destroy()).resolves.toBeUndefined()
  })

  it('preload() speichert boardId im Plugin-Closure', async () => {
    await plugin.preload('board-xyz')
    /* boardId ist intern — wir überprüfen sie indirekt via startPolling() */
    vi.useFakeTimers()
    const mockFetch = vi.fn().mockResolvedValue({ ok: false, status: 404, headers: { get: () => null } })
    vi.stubGlobal('fetch', mockFetch)
    const cb = vi.fn()
    plugin.startPolling(cb)
    await vi.advanceTimersByTimeAsync(11_000) /* ein Polling-Tick (Default 10 s) */
    /* fetch wurde mit der gespeicherten boardId aufgerufen */
    expect(mockFetch).toHaveBeenCalledWith(
      expect.stringContaining('board-xyz'),
      expect.any(Object)
    )
    plugin.stopPolling()
  })

  it('startPolling() setzt isPollingActive() auf true', async () => {
    await plugin.preload('b-1')
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 500, headers: { get: () => null } }))
    plugin.startPolling(vi.fn())
    expect(plugin.isPollingActive()).toBe(true)
    plugin.stopPolling()
  })

  it('startPolling() ruft Callback mit State wenn fetch 200 zurückgibt', async () => {
    const state = { version: '2', board: { id: 'b-1', title: 'Test', sprints: [] } }
    await plugin.preload('b-1')
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      headers: { get: (h) => h === 'ETag' ? '"etag-1"' : null },
      json: () => Promise.resolve(state),
    }))
    const cb = vi.fn()
    plugin.startPolling(cb)
    await vi.advanceTimersByTimeAsync(11_000)
    expect(cb).toHaveBeenCalledWith(state)
    plugin.stopPolling()
  })

  it('startPolling() ruft Callback NICHT bei 304 (nicht geändert)', async () => {
    await plugin.preload('b-1')
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false,
      status: 304,
      headers: { get: () => null },
    }))
    const cb = vi.fn()
    plugin.startPolling(cb)
    await vi.advanceTimersByTimeAsync(11_000)
    expect(cb).not.toHaveBeenCalled()
    plugin.stopPolling()
  })

  it('startPolling() sendet If-None-Match nach erstem erfolgreichen Fetch', async () => {
    const state = { version: '2', board: { id: 'b-1', title: 'T', sprints: [] } }
    await plugin.preload('b-1')
    vi.useFakeTimers()
    const mockFetch = vi.fn()
      .mockResolvedValueOnce({
        ok: true, status: 200,
        headers: { get: (h) => h === 'ETag' ? '"etag-1"' : null },
        json: () => Promise.resolve(state),
      })
      .mockResolvedValue({ ok: false, status: 304, headers: { get: () => null } })
    vi.stubGlobal('fetch', mockFetch)
    /* #566: Die ETag rückt erst nach, wenn main.js übernimmt (bezugUebernehmen). */
    plugin.startPolling(st => plugin.bezugUebernehmen(st))
    /* erster Tick */
    await vi.advanceTimersByTimeAsync(11_000)
    /* zweiter Tick */
    await vi.advanceTimersByTimeAsync(11_000)
    const secondCall = mockFetch.mock.calls[1]
    expect(secondCall[1].headers['If-None-Match']).toBe('"etag-1"')
    plugin.stopPolling()
  })

  it('startPolling() ohne preload() oder mit null-boardId: kein Timer (kein crash)', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn())
    plugin.startPolling(vi.fn())
    /* isPollingActive soll false bleiben, da kein boardId */
    expect(plugin.isPollingActive()).toBe(false)
  })

  it('stopPolling() setzt isPollingActive() auf false', async () => {
    await plugin.preload('b-1')
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 500, headers: { get: () => null } }))
    plugin.startPolling(vi.fn())
    expect(plugin.isPollingActive()).toBe(true)
    plugin.stopPolling()
    expect(plugin.isPollingActive()).toBe(false)
  })

  it('save() sendet PUT mit If-Match-Header und liefert ok:true', async () => {
    const state = { version: '2', board: { id: 'b-1', title: 'T', sprints: [] } }
    /* ETag vorab via preload + fetch setzen */
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce({
        ok: true, status: 200,
        headers: { get: (h) => h === 'ETag' ? '"etag-save"' : null },
        json: () => Promise.resolve(state),
      })
      .mockResolvedValueOnce({
        ok: true, status: 200,
        headers: { get: (h) => h === 'ETag' ? '"etag-save2"' : null },
      }))
    await plugin.preload('b-1')
    vi.useFakeTimers()
    /* erstes poll um ETag zu setzen — #566: erst mit Übernahme */
    plugin.startPolling(st => plugin.bezugUebernehmen(st))
    await vi.advanceTimersByTimeAsync(11_000)
    plugin.stopPolling()
    vi.useRealTimers()
    /* jetzt den PUT testen — #565: nach der Übernahme gibt es einen Bezug,
       `save` ginge über Ops; der PUT-Weg ist speichereGanz. */
    const result = await plugin.speichereGanz(state)
    expect(result.ok).toBe(true)
    expect(result.method).toBe('server')
    const putCall = globalThis.fetch.mock.calls.find(c => c[1]?.method === 'PUT')
    expect(putCall[1].headers['If-Match']).toBe('"etag-save"')
  })

  it('save() liefert conflict-Objekt bei 409', async () => {
    const clientState = { version: '2', board: { id: 'b-2', title: 'Client', sprints: [] } }
    const serverState = { version: '2', board: { id: 'b-2', title: 'Server', sprints: [] } }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false,
      status: 409,
      headers: { get: (h) => h === 'ETag' ? '"etag-conflict"' : null },
      json: () => Promise.resolve(serverState),
    }))
    await plugin.preload('b-2')
    const result = await plugin.save(clientState)
    expect(result.ok).toBe(false)
    expect(result.conflict).toBeDefined()
    expect(result.conflict.serverState).toEqual(serverState)
    expect(result.conflict.etag).toBe('"etag-conflict"')
  })

  it('autoPersist() liefert true bei erfolgreichem PUT', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true, status: 200,
      headers: { get: () => null },
    }))
    const state = { version: '2', board: { id: 'b-3', title: 'T', sprints: [] } }
    const ok = await plugin.autoPersist(state)
    expect(ok).toBe(true)
  })

  it('autoPersist() liefert false bei fehlgeschlagenem PUT', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false, status: 500,
      headers: { get: () => null },
    }))
    const ok = await plugin.autoPersist({ version: '2', board: { id: 'b-3', title: 'T' } })
    expect(ok).toBe(false)
  })

  it('F-1622: abgelehnter PUT (422) reicht den Grund vom Server durch — save und autoPersist', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false, status: 422,
      headers: { get: () => null },
      json: () => Promise.resolve({ error: 'comments of other people would be removed' }),
    }))
    const state = { version: '2', board: { id: 'b-3', title: 'T', sprints: [] } }
    const erwartet = { ok: false, abgewiesen: { status: 422, grund: 'comments of other people would be removed' } }
    expect(await plugin.autoPersist(state)).toEqual(erwartet)
    expect(await plugin.save(state)).toMatchObject(erwartet)
    /* „Meine Fassung behalten“ (main.js behalteMeine) geht über speichereGanz. */
    expect(await plugin.speichereGanz(state)).toEqual(erwartet)
  })

  it('switchBoard() ruft GET /boards/{id} und liefert State zurück', async () => {
    const serverState = { version: '2', board: { id: 'b-5', title: 'Other', sprints: [] } }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true, status: 200,
      headers: { get: (h) => h === 'ETag' ? '"etag-sw"' : null },
      json: () => Promise.resolve(serverState),
    }))
    const result = await plugin.switchBoard('b-5')
    expect(result.ok).toBe(true)
    expect(result.state).toEqual(serverState)
    const call = globalThis.fetch.mock.calls[0]
    expect(call[0]).toContain('b-5')
  })

  it('switchBoard() liefert error=no-handle bei HTTP-Fehler', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false, status: 404,
      headers: { get: () => null },
    }))
    const result = await plugin.switchBoard('b-missing')
    expect(result.ok).toBe(false)
    expect(result.error).toBe('no-handle')
  })

  it('switchBoard() liefert error=no-handle bei Netzwerkfehler', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network')))
    const result = await plugin.switchBoard('b-offline')
    expect(result.ok).toBe(false)
    expect(result.error).toBe('no-handle')
  })

  it('listKnownBoards() ruft GET /boards und mappt Ergebnis', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true, status: 200,
      json: () => Promise.resolve([{ id: 'b-1', title: 'Eins', updated_at: 1 }]),
    }))
    const list = await plugin.listKnownBoards()
    expect(list).toEqual([{ id: 'b-1', name: 'Eins', title: 'Eins', updatedAt: 1 }])
  })

  it('listKnownBoards() wirft bei Netzwerkfehler (F-1440: nicht mehr still [])', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
    await expect(plugin.listKnownBoards()).rejects.toThrow('offline')
  })

  it('parseAndMigrate() delegiert an Storage.checkVersion', () => {
    const migrated = { version: '2', board: { id: 'b-1', title: 'T', sprints: [] } }
    const spy = vi.spyOn(Storage, 'checkVersion').mockReturnValue(migrated)
    const result = plugin.parseAndMigrate(JSON.stringify({ version: '1', board: { id: 'b-1' } }))
    expect(spy).toHaveBeenCalled()
    expect(result).toBe(migrated)
    spy.mockRestore()
  })

  it('parseAndMigrate() liefert null bei ungültigem JSON', () => {
    expect(plugin.parseAndMigrate('nope{{')).toBeNull()
  })

  /* ── F-251 / ADR-0053: ETag-Race im Save-Pfad ──────────────────────── */

  const R = (status, etag, body) => ({
    ok: status >= 200 && status < 300, status,
    headers: { get: (h) => (h === 'ETag' ? (etag ?? null) : null) },
    json: () => Promise.resolve(body ?? {}),
  })
  const seq = (...rs) => {
    let i = 0
    return vi.fn().mockImplementation(() => Promise.resolve(rs[i++] ?? rs[rs.length - 1]))
  }

  it('B1: save() ohne bekannten ETag holt ihn erst per GET, dann PUT mit If-Match', async () => {
    await plugin.preload('b-1')
    vi.stubGlobal('fetch', seq(R(200, '"111"'), R(200, '"112"')))
    const result = await plugin.save({ version: '2', board: { id: 'b-1', title: 't' } })
    expect(result.ok).toBe(true)
    const calls = globalThis.fetch.mock.calls
    expect(calls).toHaveLength(2)
    expect(calls[0][1]?.method ?? 'GET').toBe('GET')          /* 1. GET zum ETag-Holen */
    expect(calls[1][1].method).toBe('PUT')
    expect(calls[1][1].headers['If-Match']).toBe('"111"')      /* PUT trägt den geholten ETag */
  })

  it('B2: persistenter 428 → ETag-Refresh + genau ein Retry, dann Erfolg', async () => {
    await plugin.preload('b-2')
    /* GET("a") → PUT 428 → GET("b") → PUT 200 */
    vi.stubGlobal('fetch', seq(R(200, '"a"'), R(428), R(200, '"b"'), R(200, '"c"')))
    const result = await plugin.save({ version: '2', board: { id: 'b-2', title: 't' } })
    expect(result.ok).toBe(true)
    const calls = globalThis.fetch.mock.calls
    expect(calls).toHaveLength(4)
    expect(calls[3][1].method).toBe('PUT')
    expect(calls[3][1].headers['If-Match']).toBe('"b"')        /* Retry-PUT mit frischem ETag */
  })

  it('B2: kein endloses Retry — zweiter 428 wird als {ok:false} gemeldet, nicht still', async () => {
    await plugin.preload('b-3')
    vi.stubGlobal('fetch', seq(R(200, '"x"'), R(428), R(200, '"y"'), R(428)))
    const result = await plugin.save({ version: '2', board: { id: 'b-3', title: 't' } })
    expect(result.ok).toBe(false)
    expect(result.conflict).toBeUndefined()
    expect(globalThis.fetch.mock.calls).toHaveLength(4)       /* GET,PUT,GET,PUT — genau 1 Retry */
  })

  it('Regression: 409 bleibt echter Konflikt (Karte #77), nicht vom 428-Pfad verschluckt', async () => {
    await plugin.preload('b-4')
    const serverState = { version: '2', board: { id: 'b-4', title: 'server' } }
    vi.stubGlobal('fetch', seq(R(200, '"e1"'), R(409, '"e2"', serverState)))
    const result = await plugin.save({ version: '2', board: { id: 'b-4', title: 'mine' } })
    expect(result.ok).toBe(false)
    expect(result.conflict).toBeDefined()
    expect(result.conflict.serverState).toEqual(serverState)
  })

  it('B1: einmal etablierter ETag wird wiederverwendet — kein erneuter GET', async () => {
    await plugin.preload('b-5')
    vi.stubGlobal('fetch', seq(R(200, '"g1"'), R(200, '"g2"'), R(200, '"g3"')))
    await plugin.save({ version: '2', board: { id: 'b-5', title: 'a' } }) /* GET + PUT */
    /* #565: nach dem ersten PUT gibt es einen Bezug — `save` ginge jetzt über
       Ops. Die ETag-Wiederverwendung gilt für den PUT-Weg, also speichereGanz. */
    await plugin.speichereGanz({ version: '2', board: { id: 'b-5', title: 'b' } }) /* nur PUT */
    const calls = globalThis.fetch.mock.calls
    expect(calls).toHaveLength(3)
    expect(calls[2][1].method).toBe('PUT')
    expect(calls[2][1].headers['If-Match']).toBe('"g2"')       /* nutzt ETag aus 1. PUT-Response */
  })
})

/* ── ADR-0054 / F-253: Server-Default-Board + adopt-or-create ──────────── */

describe.skipIf(!hasRESTPlugin)('Storage-Plugin — REST adopt-or-create (ADR-0054, F-253)', () => {
  let plugin

  beforeEach(() => {
    plugin = __testing__.createRESTPlugin()
    vi.stubGlobal('fetch', undefined)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  const R = (status, etag, body) => ({
    ok: status >= 200 && status < 300, status,
    headers: { get: (h) => (h === 'ETag' ? (etag ?? null) : null) },
    json: () => Promise.resolve(body ?? {}),
  })

  /* URL-/Methoden-abhängiger Fetch-Mock: trennt GET /boards (Liste),
     GET /boards/{id} (Adopt), POST /boards, PUT /boards/{id}. */
  function routedFetch(routes) {
    return vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET'
      const isCollection = /\/boards$/.test(url)
      if (method === 'POST' && isCollection)  return Promise.resolve(routes.post())
      if (method === 'PUT')                   return Promise.resolve(routes.put())
      if (method === 'GET' && isCollection)   return Promise.resolve(routes.list())
      return Promise.resolve(routes.get())     /* GET /boards/{id} */
    })
  }

  /* F-1440 (Karte #637): „Liste leer“ und „Liste nicht ladbar“ sind verschiedene Zustände. */
  it('listKnownBoards wirft bei HTTP-Fehler statt [] zu liefern (F-1440)', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(R(500))))
    await expect(plugin.listKnownBoards()).rejects.toThrow(/500/)
  })

  it('listKnownBoards wirft bei Netzfehler (F-1440)', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))
    await expect(plugin.listKnownBoards()).rejects.toThrow()
  })

  it('listKnownBoards liefert bei leerer Serverliste weiter [] (kein Fehler)', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(R(200, null, []))))
    await expect(plugin.listKnownBoards()).resolves.toEqual([])
  })

  it('adoptOrCreate legt bei Ladefehler der Liste KEIN neues Board an (F-1440)', async () => {
    const post = vi.fn(() => R(201, '"e"', {}))
    vi.stubGlobal('fetch', routedFetch({ list: () => R(500), get: () => R(500), post, put: () => R(500) }))
    const res = await plugin.adoptOrCreate({ version: '2', board: { id: 'x', title: 'x' } })
    expect(res.ok).toBe(false)
    expect(post).not.toHaveBeenCalled()
  })

  /* F-1453 (Sprint-80-Kritiker K-7): Boot mit 500 auf GET /boards → ok:false, der
     Browser arbeitet auf einem lokalen Board, das der Server nicht kennt. Nach
     sperreSpeichern() geht KEIN Schreib-Request mehr raus (sonst dauerhaft 428,
     Änderungen still weg); ein echtes Server-Board (switchBoard) hebt die Sperre. */
  it('Boot mit 500 auf GET /boards: nach sperreSpeichern() kein Schreib-Request, gesperrt gemeldet (F-1453)', async () => {
    const fetchSpy = routedFetch({ list: () => R(500), get: () => R(500), post: () => R(201, '"e"', {}), put: () => R(428) })
    vi.stubGlobal('fetch', fetchSpy)
    const local = { version: '2', board: { id: 'zufall-1', title: 'x', sprints: [] } }
    const boot = await plugin.adoptOrCreate(local)
    expect(boot.ok).toBe(false)
    plugin.sperreSpeichern()
    fetchSpy.mockClear()
    const auto = await plugin.autoPersist(local)
    const manuell = await plugin.save(local)
    const ganz = await plugin.speichereGanz(local)
    expect(auto).toEqual({ ok: false, gesperrt: true })
    expect(manuell).toEqual({ ok: false, gesperrt: true })
    expect(ganz.ok).toBe(false)
    expect(fetchSpy).not.toHaveBeenCalled()
  })

  it('switchBoard auf ein echtes Server-Board hebt die Sperre auf (F-1453)', async () => {
    const serverState = { version: '2', board: { id: 'default', title: 'B', sprints: [] } }
    vi.stubGlobal('fetch', routedFetch({ list: () => R(500), get: () => R(200, '"e1"', serverState), post: () => R(500), put: () => R(500) }))
    plugin.sperreSpeichern()
    expect((await plugin.autoPersist(serverState)).gesperrt).toBe(true)
    const r = await plugin.switchBoard('default')
    expect(r.ok).toBe(true)
    const nachher = await plugin.autoPersist(serverState)
    expect(nachher && nachher.gesperrt).toBeFalsy()
  })

  it('adoptOrCreate adoptiert das vorhandene "default"-Board (≥1 Board, behebt L2)', async () => {
    const serverState = { version: '2', board: { id: 'default', title: 'Board', sprints: [] } }
    vi.stubGlobal('fetch', routedFetch({
      list: () => R(200, null, [{ id: 'default', title: 'Board', updated_at: 5 }]),
      get:  () => R(200, '"etag-def"', serverState),
      post: () => R(500),
      put:  () => R(500),
    }))
    const local = { version: '2', board: { id: 'wuerfel-uuid', title: 'Board', sprints: [] } }
    const res = await plugin.adoptOrCreate(local)
    expect(res.ok).toBe(true)
    expect(res.adopted).toBe(true)
    expect(res.state).toEqual(serverState)
    /* die lokal gewürfelte id wurde verworfen — Adoption gewinnt */
    expect(res.state.board.id).toBe('default')
  })

  it('adoptOrCreate bevorzugt "default" auch wenn es nicht das erste Board ist', async () => {
    const serverState = { version: '2', board: { id: 'default', title: 'B', sprints: [] } }
    let getUrl = null
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET'
      if (method === 'GET' && /\/boards$/.test(url)) {
        return Promise.resolve(R(200, null, [
          { id: 'andere', title: 'Andere', updated_at: 9 },
          { id: 'default', title: 'B',      updated_at: 1 },
        ]))
      }
      getUrl = url
      return Promise.resolve(R(200, '"e"', serverState))
    }))
    const res = await plugin.adoptOrCreate({ version: '2', board: { id: 'x', title: 'x' } })
    expect(res.ok).toBe(true)
    expect(getUrl).toContain('default')
  })

  it('adoptOrCreate nimmt das erste Board wenn kein "default" existiert', async () => {
    const serverState = { version: '2', board: { id: 'erstes', title: 'E', sprints: [] } }
    let getUrl = null
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET'
      if (method === 'GET' && /\/boards$/.test(url)) {
        return Promise.resolve(R(200, null, [{ id: 'erstes', title: 'E', updated_at: 9 }]))
      }
      getUrl = url
      return Promise.resolve(R(200, '"e"', serverState))
    }))
    const res = await plugin.adoptOrCreate({ version: '2', board: { id: 'x', title: 'x' } })
    expect(res.ok).toBe(true)
    expect(getUrl).toContain('erstes')
  })

  it('adoptOrCreate legt das lokale Board per POST an wenn der Server leer ist (behebt L1)', async () => {
    let postBody = null
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET'
      if (method === 'GET' && /\/boards$/.test(url)) return Promise.resolve(R(200, null, []))
      if (method === 'POST') { postBody = JSON.parse(opts.body); return Promise.resolve(R(201, '"new-etag"')) }
      return Promise.resolve(R(500))
    }))
    const local = { version: '2', board: { id: 'lokal-1', title: 'Board', sprints: [] } }
    const res = await plugin.adoptOrCreate(local)
    expect(res.ok).toBe(true)
    expect(res.adopted).toBe(false)
    expect(res.state).toEqual(local)
    expect(postBody.board.id).toBe('lokal-1')
  })

  it('adoptOrCreate stabilisiert die boardId nach Create (Polling trifft die richtige id)', async () => {
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET'
      if (method === 'GET' && /\/boards$/.test(url)) return Promise.resolve(R(200, null, []))
      if (method === 'POST') return Promise.resolve(R(201, '"e"'))
      return Promise.resolve(R(304))
    }))
    await plugin.adoptOrCreate({ version: '2', board: { id: 'lokal-2', title: 'B' } })
    vi.useFakeTimers()
    plugin.startPolling(vi.fn())
    await vi.advanceTimersByTimeAsync(11_000)
    const pollCall = globalThis.fetch.mock.calls.find(
      c => (c[1]?.method ?? 'GET') === 'GET' && /\/boards\/lokal-2/.test(c[0]))
    expect(pollCall).toBeTruthy()
    plugin.stopPolling()
  })

  it('adoptOrCreate liefert ok:false wenn Server leer UND POST scheitert', async () => {
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET'
      if (method === 'GET' && /\/boards$/.test(url)) return Promise.resolve(R(200, null, []))
      return Promise.resolve(R(500)) /* POST 500 */
    }))
    const res = await plugin.adoptOrCreate({ version: '2', board: { id: 'l', title: 'L' } })
    expect(res.ok).toBe(false)
  })

  it('putBoard: 404 (Board serverseitig weg) → POST-Fallback statt stillem Fehlschlag', async () => {
    await plugin.preload('b-gone')
    /* GET (ETag) → PUT 404 → POST 201 */
    const calls = []
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET'
      calls.push(method + ' ' + url)
      if (method === 'GET') return Promise.resolve(R(200, '"etg"'))
      if (method === 'PUT') return Promise.resolve(R(404))
      if (method === 'POST') return Promise.resolve(R(201, '"recreated"'))
      return Promise.resolve(R(500))
    }))
    const result = await plugin.save({ version: '2', board: { id: 'b-gone', title: 't' } })
    expect(result.ok).toBe(true)
    expect(calls.some(c => c.startsWith('POST'))).toBe(true)
  })

  it('SR-1 (F-1685): Server meldet veränderten Stand → die ETag des gesendeten Stands wird nicht übernommen', async () => {
    await plugin.preload('b-umn')
    const ifMatch = []
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET'
      if (method === 'GET') return Promise.resolve(R(200, '"alt"', { version: '2', board: { id: 'b-umn', title: 't' } }))
      if (method === 'PUT') {
        ifMatch.push(opts?.headers?.['If-Match'] ?? null)
        return Promise.resolve({
          ok: true, status: 200,
          headers: { get: (h) => (h === 'ETag' ? '"neu"' : h === 'X-Aboard-Stand-Geaendert' ? '1' : null) },
          json: () => Promise.resolve({}),
        })
      }
      return Promise.resolve(R(500))
    }))
    expect((await plugin.save({ version: '2', board: { id: 'b-umn', title: 't' } })).ok).toBe(true)
    expect((await plugin.save({ version: '2', board: { id: 'b-umn', title: 't2' } })).ok).toBe(true)
    /* Zweiter PUT trägt NICHT die ETag des gesendeten (vom Server veränderten) Stands. */
    expect(ifMatch.at(-1)).not.toBe('"neu"')
  })

  it('putBoard-404→POST mit 409-Race endet NICHT in Endlosschleife (allowPostFallback-Guard)', async () => {
    await plugin.preload('b-race')
    let putCount = 0, postCount = 0
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET'
      if (method === 'GET')  return Promise.resolve(R(200, '"e"'))
      if (method === 'PUT')  { putCount++;  return Promise.resolve(putCount === 1 ? R(404) : R(200, '"ok"')) }
      if (method === 'POST') { postCount++; return Promise.resolve(R(409)) }
      return Promise.resolve(R(500))
    }))
    const result = await plugin.save({ version: '2', board: { id: 'b-race', title: 't' } })
    /* PUT 404 → POST 409 → PUT(allowPostFallback:false) 200 → ok, kein weiteres POST */
    expect(result.ok).toBe(true)
    expect(postCount).toBe(1)
    expect(putCount).toBe(2)
  })
})

/* ── ADR-0056 / Karte #195: createBoard() im REST-Plugin ───────────────── */

describe.skipIf(!hasRESTPlugin)('Storage-Plugin — REST createBoard (ADR-0056, Karte #195)', () => {
  let plugin

  beforeEach(() => {
    plugin = __testing__.createRESTPlugin()
    vi.stubGlobal('fetch', undefined)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  const R = (status, etag, body) => ({
    ok: status >= 200 && status < 300, status,
    headers: { get: (h) => (h === 'ETag' ? (etag ?? null) : null) },
    json: () => Promise.resolve(body ?? {}),
  })

  function routedFetch(routes) {
    return vi.fn((url, opts) => {
      const method = opts?.method ?? 'GET'
      const isCollection = /\/boards$/.test(url)
      if (method === 'POST' && isCollection)  return Promise.resolve(routes.post(url, opts))
      if (method === 'GET' && !isCollection)  return Promise.resolve(routes.get(url, opts))
      return Promise.resolve(routes.fallback?.() ?? R(500))
    })
  }

  it('createBoard() legt neues Board per POST an und wechselt darauf', async () => {
    let postedId = null
    let fetchedId = null
    vi.stubGlobal('fetch', routedFetch({
      post: (_url, opts) => {
        postedId = JSON.parse(opts.body).board.id
        return R(201, '"new-etag"')
      },
      get: (url) => {
        fetchedId = url.split('/').pop()
        return R(200, '"new-etag"', { version: '2', board: { id: fetchedId, title: 'Mein Board', sprints: [] } })
      },
    }))
    const result = await plugin.createBoard('Mein Board')
    expect(result.ok).toBe(true)
    expect(result.state?.board?.title).toBe('Mein Board')
    expect(postedId).toBeTruthy()
    /* POST-id und GET-id müssen übereinstimmen */
    expect(fetchedId).toBe(postedId)
  })

  it('createBoard() verwendet übergebenen Titel für das neue Board', async () => {
    let postBody = null
    vi.stubGlobal('fetch', routedFetch({
      post: (_url, opts) => { postBody = JSON.parse(opts.body); return R(201, '"e"') },
      get: (_url) => R(200, '"e"', { version: '2', board: { id: postBody?.board?.id, title: 'Sprint-Planning', sprints: [] } }),
    }))
    await plugin.createBoard('Sprint-Planning')
    expect(postBody?.board?.title).toBe('Sprint-Planning')
  })

  it('createBoard() fällt auf "Board" zurück wenn kein Titel übergeben wird', async () => {
    let postBody = null
    vi.stubGlobal('fetch', routedFetch({
      post: (_url, opts) => { postBody = JSON.parse(opts.body); return R(201, '"e"') },
      get: (_url) => R(200, '"e"', { version: '2', board: { id: postBody?.board?.id, title: 'Board', sprints: [] } }),
    }))
    await plugin.createBoard('')
    expect(postBody?.board?.title).toBe('Board')
  })

  it('createBoard() liefert ok:false wenn POST fehlschlägt', async () => {
    vi.stubGlobal('fetch', routedFetch({
      post: () => R(500),
      get:  () => R(500),
    }))
    const result = await plugin.createBoard('Test')
    expect(result.ok).toBe(false)
    expect(result.error).toBe('create-failed')
  })

  it('createBoard() setzt boardId + ETag sodass Polling danach auf den neuen Board zeigt', async () => {
    let postBody = null
    vi.stubGlobal('fetch', routedFetch({
      post: (_url, opts) => { postBody = JSON.parse(opts.body); return R(201, '"etag-new"') },
      get: (_url) => R(200, '"etag-new"', { version: '2', board: { id: postBody?.board?.id, title: 'X', sprints: [] } }),
    }))
    await plugin.createBoard('X')
    const newId = postBody?.board?.id
    vi.useFakeTimers()
    plugin.startPolling(vi.fn())
    await vi.advanceTimersByTimeAsync(11_000)
    const pollCall = globalThis.fetch.mock.calls.find(
      c => (c[1]?.method ?? 'GET') === 'GET' && new RegExp(`/${newId}`).test(c[0]))
    expect(pollCall).toBeTruthy()
    plugin.stopPolling()
    vi.useRealTimers()
  })

  it('Standalone-Plugin: createBoard() liefert ok:false (kein Server)', async () => {
    const standalone = __testing__.createStandalonePlugin()
    const result = await standalone.createBoard('Test')
    expect(result.ok).toBe(false)
  })

  it('Projekt-Plugin: createBoard() liefert ok:false (kein Server)', async () => {
    const project = __testing__.createProjectPlugin()
    const result = await project.createBoard('Test')
    expect(result.ok).toBe(false)
  })
})

describe.skipIf(!hasRESTPlugin)('Storage-Plugin — Factory (REST-Erweiterung)', () => {
  it('getActivePlugin() liefert REST-Plugin wenn boardMode=server', () => {
    Config.set('boardMode', 'server')
    const p = StoragePlugin.getActivePlugin()
    /* Nach server gesetzt (via Config.set): boardMode wird im Server-Build
       auf 'server' gehalten; im Test-Build ist VALID_BOARD_MODES ohne 'server',
       daher fällt Config.set auf 'solo' zurück — wir testen selectPlugin direkt */
    Config.set('boardMode', 'solo') /* cleanup */
  })

  it('selectPlugin("server") liefert REST-Plugin', () => {
    const p = StoragePlugin.selectPlugin('server')
    expect(p.kind).toBe('rest')
  })

  it('selectPlugin("server") ist gecacht — gleiche Instanz', () => {
    const a = StoragePlugin.selectPlugin('server')
    const b = StoragePlugin.selectPlugin('server')
    expect(a).toBe(b)
  })
})

/* ── #296 / Sprint 43: Cross-Board-Transfer (Etappe 2 Frontend) ───────── */

describe.skipIf(!hasRESTPlugin)('Storage-Plugin — REST transferCard (#296)', () => {
  let plugin

  beforeEach(() => {
    plugin = __testing__.createRESTPlugin()
    vi.stubGlobal('fetch', undefined)
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  const R = (status, body) => ({
    ok: status >= 200 && status < 300, status,
    headers: { get: () => null },
    json: () => Promise.resolve(body ?? {}),
  })

  it('transferCard() POSTet auf /boards/{src}/cards/{c}/transfer mit toBoardId/toColumnId', async () => {
    await plugin.preload('src-board')
    let postedUrl = null
    let postedBody = null
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      postedUrl = url
      postedBody = JSON.parse(opts.body)
      return Promise.resolve(R(200, { newDisplayId: 7, oldDisplayId: 4 }))
    }))
    const res = await plugin.transferCard('card-uuid-42', 'dst-board', 'col-inbox')
    expect(res.ok).toBe(true)
    expect(res.body.newDisplayId).toBe(7)
    expect(res.body.oldDisplayId).toBe(4)
    expect(postedUrl).toContain('/boards/src-board/cards/card-uuid-42/transfer')
    expect(postedBody).toEqual({ toBoardId: 'dst-board', toColumnId: 'col-inbox' })
  })

  it('transferCard() ohne boardId liefert ok:false (preload-Vertrag)', async () => {
    /* Frisches Plugin ohne preload(); s.boardId ist null. */
    const fresh = __testing__.createRESTPlugin()
    const res = await fresh.transferCard('c', 'dst', 'col-a')
    expect(res.ok).toBe(false)
    expect(res.status).toBe(0)
  })

  it('transferCard() propagiert 403 (Permission-denied auf Ziel-Board)', async () => {
    await plugin.preload('src-board')
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(R(403, { error: 'forbidden' }))))
    const res = await plugin.transferCard('c', 'dst', 'col-a')
    expect(res.ok).toBe(false)
    expect(res.status).toBe(403)
  })

  it('transferCard() fängt Netzwerk-Fehler ab und liefert ok:false', async () => {
    await plugin.preload('src-board')
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('net'))))
    const res = await plugin.transferCard('c', 'dst', 'col-a')
    expect(res.ok).toBe(false)
    expect(res.status).toBe(0)
  })

  it('peekBoardColumns() GETtet /boards/{id} und extrahiert columnDefs', async () => {
    let getUrl = null
    vi.stubGlobal('fetch', vi.fn((url) => {
      getUrl = url
      return Promise.resolve(R(200, {
        version: '2',
        board: {
          id: 'b-x', title: 'Ziel',
          columnDefs: [{ id: 'col-a', title: 'A' }, { id: 'col-b', title: 'B' }],
        },
      }))
    }))
    const r = await plugin.peekBoardColumns('b-x')
    expect(r.ok).toBe(true)
    expect(r.title).toBe('Ziel')
    expect(r.columnDefs).toHaveLength(2)
    expect(getUrl).toContain('/boards/b-x')
  })

  it('peekBoardColumns() liefert ok:false bei 404', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(R(404, null))))
    const r = await plugin.peekBoardColumns('missing')
    expect(r.ok).toBe(false)
  })

  it('peekBoardColumns() mutiert s.boardId/ETag NICHT (reine Lese-Operation)', async () => {
    await plugin.preload('src-original')
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(R(200, {
      version: '2',
      board: { id: 'other', title: 'O', columnDefs: [{ id: 'c1', title: 'C1' }] },
    }))))
    await plugin.peekBoardColumns('other')
    /* Folgender save() muss noch unter src-original PUT — nicht 'other'. */
    let putUrl = null
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      putUrl = url
      return Promise.resolve(R(200, null))
    }))
    await plugin.save({ version: '2', board: { id: 'src-original', title: 't' } })
    expect(putUrl).toContain('src-original')
    expect(putUrl).not.toContain('/boards/other')
  })
})

/* ── Karte #845 / Sprint-97-Kritik: deleteBoard() im REST-Plugin ──────── */

describe.skipIf(!hasRESTPlugin)('Storage-Plugin — REST deleteBoard (#845, Kritiker S97)', () => {
  let plugin
  const R = (status, body) => ({ ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body ?? {}) })
  beforeEach(() => { plugin = __testing__.createRESTPlugin() })
  afterEach(() => { vi.unstubAllGlobals() })

  it('409 liefert offene_aenderungen aus der Antwort', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(R(409, { error: 'x', offene_aenderungen: 3 }))))
    expect(await plugin.deleteBoard('b1')).toEqual({ ok: false, status: 409, offene: 3, gone: false })
  })
  it('404 vom Server („board not found“) ist „schon weg“', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(R(404, { error: 'board not found' }))))
    expect((await plugin.deleteBoard('b1')).gone).toBe(true)
  })
  it('404 ohne diese Antwort (Proxy, alter Server) ist nicht „schon weg“', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({ ok: false, status: 404, json: () => Promise.reject(new Error('kein JSON')) })))
    expect((await plugin.deleteBoard('b1')).gone).toBe(false)
  })
  it('204 ist ok, Netzfehler ist status 0', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(R(204))))
    expect((await plugin.deleteBoard('b1')).ok).toBe(true)
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('netz'))))
    expect(await plugin.deleteBoard('b1')).toEqual({ ok: false, status: 0 })
  })
})

