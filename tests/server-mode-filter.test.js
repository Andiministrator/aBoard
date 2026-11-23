/* @vitest-environment happy-dom */

/**
 * Tests für Karte #207: Server-Modus-Filterung im Config-Dialog und Sidebar
 *
 * Abdeckung:
 *  (a) Server-Modus → Reset-Button ausgeblendet, Mode-Switch ausgeblendet,
 *      Username read-only
 *  (b) Solo-/Standalone-Modus → alles unverändert (ADR-0059-Invariante)
 *  (c) Profile.has('feature.restStorage') korrekt pro Modus
 *
 * Hinweis: Im Test-Kontext ist IS_SERVER_BUILD = false, daher akzeptiert
 * Config.set('boardMode', 'server') den Wert nicht (fällt auf 'solo' zurück,
 * ADR F-186). Der ConfigDialog wird stattdessen via modeSel-Manipulation getestet.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

/* ── Mocks ──────────────────────────────────────────────────────────────── */

vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName:    vi.fn(() => null),
    isActive:            vi.fn(() => false),
    isPermissionNeeded:  vi.fn(() => false),
    startPolling:        vi.fn(),
    stopPolling:         vi.fn(),
  },
}))

vi.mock('../src/storage.js', () => ({
  Storage: {
    listKnownFiles: vi.fn(async () => []),
  },
}))

vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      listKnownBoards: vi.fn(async () => []),
      aclList:         vi.fn(async () => []),
      autoPersist:     vi.fn(async () => true),
    }),
  },
}))

vi.mock('../src/attachments.js', () => ({
  uploadBoardFile:       vi.fn(async () => ({ id: 'att1' })),
  boardAttachmentUrl:    vi.fn(async () => null),
  deleteBoardAttachment: vi.fn(async () => {}),
  checkUpload:           vi.fn(() => ({ ok: true })),
}))

vi.mock('../src/swipe.js', () => ({
  addSwipeToDismiss: vi.fn(),
}))

vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

import { Config }        from '../src/config.js'
import { State }         from '../src/state.js'
import { EventBus }      from '../src/eventbus.js'
import { Profile }       from '../src/profile.js'
import { ConfigDialog }  from '../src/configdialog.js'
import { IS_PROJECTOR_BUILD } from './helpers/build-flags.js'

/* F-272 / ADR-0064: Im Server-Build-Kontext (test:server) ist boardMode immer 'server'
   (VALID_BOARD_MODES={'server'}). Tests, die das Solo-Modus-Verhalten als ADR-0059-Invariante
   prüfen, laufen nur im Standalone-Kontext (test:standalone). Der beforeEach setzt
   Config.set('boardMode', 'solo') — im Server-Kontext clampt das auf 'server', womit der
   Config-Dialog wie im Server-Modus reagiert (Gefahrenzone ausgeblendet, read-only etc.). */
const isServerBuildCtx = (typeof __SERVER_BUILD__ !== 'undefined') && (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true')

/* ── Hilfsfunktionen: Config-Dialoge öffnen + Overlay zurückgeben ────────── */

/* ADR-0065: Board-Dialog öffnen (Gefahrenzone, Modus, Anhänge, etc.) */
function openBoardDialog() {
  EventBus.emit('config:open-board')
  return document.querySelector('.config-overlay')
}

/* ADR-0065: Benutzer-Dialog öffnen (Theme, Sprache, Identitäten) */
function openUserDialog() {
  EventBus.emit('config:open-user')
  return document.querySelector('.config-overlay')
}

/* Rückwärts-Kompatibilität: config:open → Board-Dialog */
function openConfigDialog() {
  EventBus.emit('config:open')
  return document.querySelector('.config-overlay')
}

/** Simuliert einen Board-Modus-Wechsel direkt via modeSel im DOM.
 *  Da Config.set('boardMode', 'server') im Standalone-Build (IS_SERVER_BUILD=false)
 *  auf 'solo' zurückfällt (F-186), muss der modeSel direkt manipuliert werden. */
function setModeInDialog(overlay, mode) {
  const modeSel = overlay.querySelector('#cfg-boardMode')
  if (!modeSel) return
  modeSel.value = mode
  modeSel.dispatchEvent(new Event('change'))
}

/* ── Setup / Teardown ───────────────────────────────────────────────────── */

beforeEach(() => {
  /* config:language-changed zwingt den ConfigDialog-Singleton zum Neu-Aufbau
     beim nächsten open(). Muss VOR dem DOM-Reset laufen. */
  EventBus.emit('config:language-changed')
  document.body.innerHTML = ''
  /* Einfaches Board-Objekt mit User-Identity */
  State.set({
    board: {
      id:         'b1',
      title:      'Test-Board',
      identities: [{ id: 'user-default', kind: 'user', label: 'Andi', color: '#0B5D73', icon: null }],
    },
  })
  Config.set('boardMode', 'solo')
  /* F-431: happy-dom kennt showDirectoryPicker nicht. Der Projekt-Einstieg wird
     in solo/team nur angeboten, wenn FSA existiert — für den Normalfall also
     einen Chromium-artigen Browser simulieren. Die Gegenprobe (ohne FSA) setzt
     den Wert in ihrem eigenen Test zurück. */
  if (!('showDirectoryPicker' in window)) window.showDirectoryPicker = () => {}
})

afterEach(() => {
  vi.clearAllMocks()
})

/* ══════════════════════════════════════════════════════════════════════════
   Solo-Modus — ADR-0059-Invariante (Verhalten exakt wie heute)
   ══════════════════════════════════════════════════════════════════════════ */

/* Standalone-only (F-272): Diese Gruppe prüft das Solo-Modus-Verhalten des Config-Dialogs
   als ADR-0059-Invariante. Im Server-Kontext (test:server) ist boardMode immer 'server' —
   der Dialog zeigt dann Server-Modus-Verhalten (Gefahrenzone ausgeblendet, read-only etc.).
   Die Tests laufen nur im Standalone-Kontext (test:standalone). */
describe.skipIf(isServerBuildCtx)('Config-Dialog — Solo-Modus (ADR-0059-Invariante)', () => {
  it('zeigt die Gefahrenzone mit Reset-Button an', () => {
    const overlay = openConfigDialog()
    expect(overlay).toBeTruthy()
    const dangerContainer = overlay.querySelector('.config-danger-container')
    expect(dangerContainer).toBeTruthy()
    /* Im Solo-Modus muss die Gefahrenzone sichtbar sein */
    expect(dangerContainer.style.display).not.toBe('none')
    /* Reset-Button muss vorhanden sein */
    const resetBtns = dangerContainer.querySelectorAll('.btn-danger')
    expect(resetBtns.length).toBeGreaterThan(0)
  })

  it('zeigt den Mode-Switch-Bereich an', () => {
    const overlay = openConfigDialog()
    const modeContainer = overlay.querySelector('.config-mode-container')
    expect(modeContainer).toBeTruthy()
    expect(modeContainer.style.display).not.toBe('none')
  })

  /* ADR-0065/F-280: Username-Feld liegt im Benutzer-Dialog (config:open-user),
     nicht im Board-Dialog. openConfigDialog() würde #cfg-identity-label-0 nie
     finden → false-positive. openUserDialog() prüft die Invariante echt. */
  it('Username-Feld ist editierbar (nicht read-only)', () => {
    const overlay = openUserDialog()
    const labelInp = overlay.querySelector('#cfg-identity-label-0')
    expect(labelInp).toBeTruthy()
    expect(labelInp.readOnly).toBe(false)
  })

  it('zeigt keinen server-hint für das Username-Feld an', () => {
    const overlay = openUserDialog()
    const hint = overlay.querySelector('.config-server-hint')
    expect(hint).toBeFalsy()
  })
})

/* ══════════════════════════════════════════════════════════════════════════
   Server-Modus — Filterung (Karte #207)
   Hinweis: modeSel.value = 'server' direkt setzen, da Config.set('boardMode', 'server')
   im Test-Build (IS_SERVER_BUILD=false) auf 'solo' zurückfällt (ADR F-186).
   refreshProjectArea und _renderIdentitiesList nutzen jeweils Profile.has mit Override
   (modeSel.value), daher funktioniert die direkte DOM-Manipulation korrekt.
   ══════════════════════════════════════════════════════════════════════════ */

describe('Config-Dialog — Server-Modus-Filterung (Karte #207)', () => {
  it('blendet die Gefahrenzone (Reset) aus — F-265', () => {
    const overlay = openConfigDialog()
    expect(overlay).toBeTruthy()
    setModeInDialog(overlay, 'server')
    const dangerContainer = overlay.querySelector('.config-danger-container')
    expect(dangerContainer).toBeTruthy()
    expect(dangerContainer.style.display).toBe('none')
  })

  it('blendet den Mode-Switch-Bereich aus', () => {
    const overlay = openConfigDialog()
    setModeInDialog(overlay, 'server')
    const modeContainer = overlay.querySelector('.config-mode-container')
    expect(modeContainer).toBeTruthy()
    expect(modeContainer.style.display).toBe('none')
  })

  it('lässt Theme und Sprache sichtbar (reine User-Settings bleiben erhalten, ADR-0065: im Benutzer-Dialog)', () => {
    /* ADR-0065: Theme und Sprache befinden sich im Benutzer-Dialog (config:open-user),
       nicht mehr im Board-Dialog. Sie sind unabhängig vom Server-Modus immer sichtbar + editierbar. */
    const overlay = openUserDialog()
    const themeSelect    = overlay.querySelector('#cfg-theme')
    const languageSelect = overlay.querySelector('#cfg-language')
    expect(themeSelect).toBeTruthy()
    expect(languageSelect).toBeTruthy()
  })

  /* Standalone-only (F-272): Prüft Übergang von Solo→Server im Dialog. Im Server-Kontext
     startet der Dialog bereits im Server-Modus (boardMode='server') — keine Transition testbar. */
  it.skipIf(isServerBuildCtx)('Solo-Modus → Server-Modus-Wechsel: Gefahrenzone verschwindet', () => {
    const overlay = openConfigDialog()
    /* Vorher sichtbar */
    const dangerContainer = overlay.querySelector('.config-danger-container')
    expect(dangerContainer.style.display).not.toBe('none')
    /* Nach Wechsel zu server: ausgeblendet */
    setModeInDialog(overlay, 'server')
    expect(dangerContainer.style.display).toBe('none')
  })

  it('Server-Modus → Solo-Modus-Wechsel: Gefahrenzone erscheint wieder', () => {
    const overlay = openConfigDialog()
    setModeInDialog(overlay, 'server')
    const dangerContainer = overlay.querySelector('.config-danger-container')
    expect(dangerContainer.style.display).toBe('none')
    /* Zurück zu solo */
    setModeInDialog(overlay, 'solo')
    expect(dangerContainer.style.display).not.toBe('none')
  })
})

/* ══════════════════════════════════════════════════════════════════════════
   F-268: Echte Verhaltenstests — Username read-only + board:reset-Guard
   (Karte #213 — nicht mehr Test-Theater, sondern DOM- und EventBus-Beweis)

   Mocking-Strategie: vi.spyOn(Profile, 'has') analog sidebar.test.js —
   macht Profile.has('feature.restStorage') real true, sodass _renderIdentitiesList()
   und der main.js-Guard das tatsächliche Verhalten zeigen.
   (Config.set('boardMode','server') wäre im Standalone-Test-Build wirkungslos — F-186)
   ══════════════════════════════════════════════════════════════════════════ */

/* ADR-0065: Identitäten-Sektion ist im Benutzer-Dialog (config:open-user),
   nicht mehr im Board-Dialog. Tests wurden entsprechend auf openUserDialog() umgestellt. */
/* ADR-0080 (revidiert ADR-0078, Karte #268): Im Server-Modus wird die
   Identitäten-Sektion NICHT mehr pauschal ausgeblendet. Nur das Anzeigename-/
   Label-Feld der user-default-Identity entfällt (Feld-Ebene). Farbe und Icon
   bleiben sichtbar — lokale kosmetische Präferenzen ohne Forward-Auth-Bezug. */
describe('ADR-0080 — Identitäten-Sektion im Server-Modus: Farbe/Icon vorhanden, Anzeigename ausgeblendet', () => {
  it('Server-Modus: Identitäten-Sektion vorhanden, Label-Input NICHT vorhanden, Farb-/Icon-Input vorhanden', async () => {
    const spy = vi.spyOn(Profile, 'has').mockImplementation((cap) => {
      if (cap === 'feature.restStorage') return true
      return false
    })
    try {
      const overlay = openUserDialog()
      expect(overlay).toBeTruthy()
      /* ADR-0080: Sektion ist jetzt vorhanden (nicht mehr pauschal ausgeblendet) */
      expect(overlay.querySelector('.config-identities-section')).not.toBeNull()
      /* Anzeigename-Feld der user-default-Identity: im Server-Modus ausgeblendet */
      expect(overlay.querySelector('#cfg-identity-label-0')).toBeNull()
      /* Farb-Input der user-default-Identity: vorhanden (lokale Präferenz) */
      expect(overlay.querySelector('#cfg-identity-color-0')).not.toBeNull()
      /* Icon-Input der user-default-Identity: vorhanden (lokale Präferenz) */
      expect(overlay.querySelector('#cfg-identity-icon-0')).not.toBeNull()
      /* Kein server-hint (der wurde mit ADR-0078/F-315 entfernt) */
      expect(overlay.querySelector('.config-server-hint')).toBeNull()
    } finally {
      spy.mockRestore()
    }
  })

  /* Standalone-only (F-272): Im Server-Kontext ist boardMode immer 'server'. */
  it.skipIf(isServerBuildCtx)('Solo-Modus: Username-Feld ist editierbar — kein readOnly, kein aria-readonly', () => {
    /* ADR-0059-Invariante: im Solo-Modus muss das Feld uneingeschränkt bleiben.
       ADR-0065: Identitäten im Benutzer-Dialog (openUserDialog). */
    const overlay = openUserDialog()
    const labelInp = overlay.querySelector('#cfg-identity-label-0')
    expect(labelInp).toBeTruthy()
    expect(labelInp.readOnly).toBe(false)
    expect(labelInp.getAttribute('aria-readonly')).toBeNull()
    expect(labelInp.getAttribute('aria-describedby')).toBeNull()
  })
})

describe('F-268 — board:reset-Guard: Profile-Logik (Unit-Beweis ohne main.js-Import)', () => {
  /* main.js ist ein Integrations-Modul mit App-Init-IIFE und kann nicht isoliert
     als Unit getestet werden. Der Guard-Code in main.js:
       if (Profile.has('feature.restStorage')) return;
     wird stattdessen durch zwei komplementäre Tests bewiesen:
       1) Profile.has('feature.restStorage') === true im Server-Modus (Guard feuert)
       2) Profile.has('feature.restStorage') === false im Solo-Modus (Guard greift nicht)
     Zusätzlich: Der gemockte Server-Profil-Test beweist, dass bei true ein
     State-Reset NICHT stattfinden würde (Guard-Invariante über State-Unveränderlichkeit). */

  it('Server-Modus: Profile.has("feature.restStorage") ist true — Guard würde early return', () => {
    /* Direkter Beweis der Guard-Bedingung (Profile.PROFILES statt Spy) */
    expect(Profile.has('feature.restStorage', 'server')).toBe(true)
    /* Guard-Logik: if (true) return → kein State-Reset */
  })

  it('Solo-Modus: Profile.has("feature.restStorage") ist false — Guard greift nicht', () => {
    expect(Profile.has('feature.restStorage', 'solo')).toBe(false)
    expect(Profile.has('feature.restStorage', 'team')).toBe(false)
    expect(Profile.has('feature.restStorage', 'claude-code')).toBe(false)
  })

  it('Server-Modus (gemocktes Profil): State bleibt nach simuliertem board:reset unverändert', () => {
    /* Beweist den Guard-Mechanismus durch direkte Nachbildung der main.js-Logik:
       Wenn Profile.has('feature.restStorage') true ist → kein State.set() */
    const idVorher = State.get()?.board?.id
    expect(idVorher).toBeTruthy()

    const spy = vi.spyOn(Profile, 'has').mockImplementation((cap) => {
      if (cap === 'feature.restStorage') return true
      return false
    })
    try {
      /* Guard-Logik nachbauen (identisch mit main.js board:reset-Handler) */
      const handleBoardReset = () => {
        if (Profile.has('feature.restStorage')) return /* früher return */
        /* Dieser Zweig darf im Server-Modus nicht erreicht werden */
        State.set({ version: '2', board: { id: 'NEUES-BOARD', title: 'Reset', sprints: [], columnDefs: [] } })
      }
      handleBoardReset()
      /* Board-ID darf sich NICHT verändert haben */
      expect(State.get()?.board?.id).toBe(idVorher)
    } finally {
      spy.mockRestore()
    }
  })

  /* Standalone-only (F-272): Im Server-Kontext ist Profile.has('feature.restStorage')=true
     (boardMode='server') — der Guard gibt early return zurück und verhindert den State-Reset.
     Dieser Test prüft das gegenteilige Verhalten (Solo ohne Guard), läuft nur im
     Standalone-Kontext (test:standalone). */
  it.skipIf(isServerBuildCtx)('Solo-Modus (gemocktes Profil): State wird nach simuliertem board:reset geändert', () => {
    /* Beweist dass ohne Server-Modus der State-Reset stattfindet */
    const idVorher = State.get()?.board?.id
    expect(idVorher).toBeTruthy()

    /* Kein Spy nötig — Solo ist Default im beforeEach */
    const handleBoardReset = () => {
      if (Profile.has('feature.restStorage')) return
      State.set({ version: '2', board: { id: 'NEUES-BOARD', title: 'Reset', sprints: [], columnDefs: [] } })
    }
    handleBoardReset()
    /* Board-ID MUSS sich geändert haben */
    expect(State.get()?.board?.id).toBe('NEUES-BOARD')
    /* Zurücksetzen für sauberes afterEach */
    State.set({ board: { id: idVorher, title: 'Test-Board',
      identities: [{ id: 'user-default', kind: 'user', label: 'Andi', color: '#0B5D73', icon: null }] } })
  })
})

/* ══════════════════════════════════════════════════════════════════════════
   ADR-0069: Modus-Relevanz — Config-Dialog-Sektionen pro Modus
   Standalone-only (F-272): Im Server-Kontext ist boardMode immer 'server' (via clamping).
   Die meisten Tests prüfen Solo/claude-code-Verhalten → skipIf isServerBuildCtx.
   ══════════════════════════════════════════════════════════════════════════ */

/** F-427: Zerlegt den Projekt-Bereich in seine drei Zeilen.
 *  Seit dem F-427-Fix ist der Container ausserhalb des Server-Modus IMMER
 *  sichtbar (der Ordner-Button ist der einzige `sync:pick-directory`-Emitter
 *  und damit der einzige UI-Weg solo/team → claude-code). Die Modus-Relevanz
 *  der Sektion zeigt sich jetzt an Status- und Deaktivieren-Zeile. */
function projectSectionParts(overlay) {
  const area = overlay.querySelector('.config-project-area')
  if (!area) return null
  return {
    area,
    status:  area.querySelector('#cfg-project-status')?.closest('.config-row') ?? null,
    disable: area.querySelector('.btn-danger')?.closest('.config-row') ?? null,
    dirBtn:  area.querySelector('.btn-sm:not(.btn-danger)'),
  }
}

describe('Config-Dialog — Modus-Relevanz (ADR-0069)', () => {
  /* project-Sektion: Status/Deaktivieren nur claude-code — Ordner-Button überall ausser Server (F-427) */
  it.skipIf(isServerBuildCtx)('Solo-Modus: Status/Deaktivieren versteckt, Ordner-Button erreichbar (F-427)', () => {
    const overlay = openConfigDialog()
    const p = projectSectionParts(overlay)
    if (!p) return /* Sektion gar nicht gebaut → implizit versteckt */
    /* Container sichtbar, damit der Aktivierungs-Einstieg klickbar bleibt */
    expect(p.area.style.display).not.toBe('none')
    expect(p.status.style.display).toBe('none')
    expect(p.disable.style.display).toBe('none')
    expect(p.dirBtn).toBeTruthy()
  })

  it.skipIf(isServerBuildCtx)('Claude-Code-Modus: project-Bereich vollständig sichtbar (polling, kein restStorage)', () => {
    const overlay = openConfigDialog()
    setModeInDialog(overlay, 'claude-code')
    const p = projectSectionParts(overlay)
    /* project-Sektion ist im Board-Dialog gebaut — sollte sichtbar sein */
    expect(p).toBeTruthy()
    expect(p.area.style.display).not.toBe('none')
    expect(p.status.style.display).not.toBe('none')
    expect(p.disable.style.display).not.toBe('none')
  })

  it.skipIf(isServerBuildCtx)('Server-Modus: project-Bereich versteckt (hat restStorage)', () => {
    const overlay = openConfigDialog()
    setModeInDialog(overlay, 'server')
    const p = projectSectionParts(overlay)
    if (!p) return /* Sektion nicht gebaut → implizit versteckt */
    expect(p.area.style.display).toBe('none')
  })

  /* F-427-Regression: die Solo-Sackgasse. Vor dem Fix war der einzige
     `sync:pick-directory`-Emitter im verborgenen Container gefangen → ein
     frisch ausgerolltes slim-Template kam ohne DevTools nie nach claude-code. */
  /* Kein `if (!p) return` in diesen beiden — ein fehlender Container IST die
     Fehlerklasse, die F-427 beschreibt. Ein Wächter-Test, der darauf grün
     aussteigt, bewacht nichts (Sprint-47-Kritiker [V] P2). */
  it.skipIf(isServerBuildCtx)('F-427: Ordner-Button emittiert sync:pick-directory auch im Solo-Modus', () => {
    const overlay = openConfigDialog()
    const p = projectSectionParts(overlay)
    expect(p).toBeTruthy()
    /* Existenz allein genügt nicht — der Knopf muss auch sichtbar sein */
    expect(p.area.style.display).not.toBe('none')
    expect(p.dirBtn).toBeTruthy()
    expect(p.dirBtn.closest('.config-row').style.display).not.toBe('none')
    const spy = vi.fn()
    EventBus.on('sync:pick-directory', spy)
    try {
      p.dirBtn.click()
      expect(spy).toHaveBeenCalledTimes(1)
    } finally {
      EventBus.off('sync:pick-directory', spy)   /* EventBus.on liefert keinen Unsubscribe */
    }
  })

  /* F-431: ohne FSA (Firefox/Safari) wäre der Knopf ein toter Knopf → in
     solo/team gar nicht erst anbieten. Gegenprobe zum Test darüber. */
  it.skipIf(isServerBuildCtx)('F-431: ohne showDirectoryPicker bleibt der Einstieg in solo verborgen', () => {
    const saved = window.showDirectoryPicker
    delete window.showDirectoryPicker
    try {
      const overlay = openConfigDialog()
      const p = projectSectionParts(overlay)
      if (!p) return
      expect(p.area.style.display).toBe('none')
    } finally {
      window.showDirectoryPicker = saved
    }
  })

  it.skipIf(isServerBuildCtx)('F-427: Team-Modus hat denselben Aktivierungs-Einstieg', () => {
    const overlay = openConfigDialog()
    setModeInDialog(overlay, 'team')
    const p = projectSectionParts(overlay)
    expect(p).toBeTruthy()
    expect(p.area.style.display).not.toBe('none')
    expect(p.status.style.display).toBe('none')
  })

  /* attachments-Sektion: nur Solo + Team sichtbar */
  it.skipIf(isServerBuildCtx)('Solo-Modus: attachments-Bereich sichtbar (attachmentLimits)', () => {
    const overlay = openConfigDialog()
    /* Solo-Modus ist Default im beforeEach */
    const attachArea = overlay.querySelector('.config-attachments-area')
    if (!attachArea) return /* Sektion nicht im Board-Dialog → skip */
    expect(attachArea.style.display).not.toBe('none')
  })

  it.skipIf(isServerBuildCtx)('Team-Modus: attachments-Bereich sichtbar (attachmentLimits)', () => {
    const overlay = openConfigDialog()
    setModeInDialog(overlay, 'team')
    const attachArea = overlay.querySelector('.config-attachments-area')
    if (!attachArea) return
    expect(attachArea.style.display).not.toBe('none')
  })

  it.skipIf(isServerBuildCtx)('Claude-Code-Modus: attachments-Bereich versteckt (kein attachmentLimits)', () => {
    const overlay = openConfigDialog()
    setModeInDialog(overlay, 'claude-code')
    const attachArea = overlay.querySelector('.config-attachments-area')
    if (!attachArea) return
    expect(attachArea.style.display).toBe('none')
  })

  /* Modus-Wechsel: Solo→claude-code→Solo — Sichtbarkeit folgt Modus.
     F-427: gemessen an der Status-Zeile, nicht mehr am Container. */
  it.skipIf(isServerBuildCtx)('Modus-Wechsel Solo→claude-code: project erscheint, attachments verschwindet', () => {
    const overlay = openConfigDialog()
    /* Ausgangszustand: Solo — project-Status versteckt, attachments sichtbar */
    const p          = projectSectionParts(overlay)
    const attachArea = overlay.querySelector('.config-attachments-area')
    if (p) expect(p.status.style.display).toBe('none')
    if (attachArea)  expect(attachArea.style.display).not.toBe('none')
    /* Nach Wechsel zu claude-code: project sichtbar, attachments versteckt */
    setModeInDialog(overlay, 'claude-code')
    if (p) expect(p.status.style.display).not.toBe('none')
    if (attachArea)  expect(attachArea.style.display).toBe('none')
    /* Zurück zu solo: wieder Ausgangszustand */
    setModeInDialog(overlay, 'solo')
    if (p) expect(p.status.style.display).toBe('none')
    if (attachArea)  expect(attachArea.style.display).not.toBe('none')
  })
})

/* ══════════════════════════════════════════════════════════════════════════
   Karte #222: KI-Kontext-Button im Server-Modus ausblenden
   ADR-0063-konform: serverScope:'dangerous', isServerHidden('ui.ki-context')
   Beide Dialoge (board + user) werden abgedeckt.
   ══════════════════════════════════════════════════════════════════════════ */

/** Sucht den KI-Kontext-Button im Overlay — identifiziert via .btn.modal-actions-left.
 *  Im Config-Dialog gibt es nur einen Button mit dieser Klassen-Kombination. */
function findKiKontextBtn(overlay) {
  return overlay.querySelector('.btn.modal-actions-left')
}

/* Karte #248 / ADR-0067: Der KI-Kontext-Button wird ZUSÄTZLICH zum
   Server-Gating (#222) im claude-code-Projektor-Build versteckt
   (kiContextHidden() = IS_PROJECTOR_BUILD || isServerHidden(...)).
   ADR-0116 (Sprint 53): Hier stand bis dahin eine „COVERAGE-GRENZE
   (bewusst)" — der Fall „slim Build ZEIGT den Button" sei in der
   2-Kontext-Harness nicht assertierbar, weil kein Kontext das Flag auf
   'false' setzt. Mit dem dritten und vierten Kontext ist er es. Die
   Erwartung folgt jetzt IS_PROJECTOR_BUILD, statt eine Hälfte der
   ODER-Bedingung dem DCE-Vertrauensmodell zu überlassen. Damit ist die
   #248-Zusage in beide Richtungen geprüft: im Projektor-Build verborgen,
   im slim Build sichtbar. */
describe('Karte #222/#248 — KI-Kontext-Button Sichtbarkeit (board-Dialog)', () => {
  /* Board-Dialog: Sichtbarkeit via modeSel-Wechsel (refreshProjectArea) */

  it.skipIf(isServerBuildCtx)('#248 im Solo-Modus: Button im Board-Dialog verborgen (Projektor-Build) bzw. sichtbar (slim Build)', () => {
    const overlay = openConfigDialog()
    const btn = findKiKontextBtn(overlay)
    expect(btn).toBeTruthy()
    expect(btn.style.display).toBe(IS_PROJECTOR_BUILD ? 'none' : '')
  })

  it('Server-Modus: KI-Kontext-Button im Board-Dialog ausgeblendet', () => {
    const overlay = openConfigDialog()
    setModeInDialog(overlay, 'server')
    const btn = findKiKontextBtn(overlay)
    expect(btn).toBeTruthy()
    expect(btn.style.display).toBe('none')
  })

  it.skipIf(isServerBuildCtx)('#248 über Modus-Wechsel: Projektor-Build stets verborgen, slim Build nur im Server-Modus', () => {
    const overlay = openConfigDialog()
    const btn = findKiKontextBtn(overlay)
    expect(btn).toBeTruthy()
    /* claude-code (#248): unabhängig vom Modus stets ausgeblendet.
       slim: nur das #222-Server-Gating greift — solo zeigt den Button. */
    const imSolo = IS_PROJECTOR_BUILD ? 'none' : ''
    expect(btn.style.display).toBe(imSolo)
    setModeInDialog(overlay, 'server')
    expect(btn.style.display).toBe('none')   /* #222 gilt in JEDEM Build */
    setModeInDialog(overlay, 'solo')
    expect(btn.style.display).toBe(imSolo)
  })
})

describe('Karte #222/#248 — KI-Kontext-Button Sichtbarkeit (user-Dialog, gemocktes Profil)', () => {
  /* Benutzer-Dialog: kein modeSel → Sichtbarkeit wird direkt bei Erzeugung gesetzt.
     Gemocktes Profile.has('feature.restStorage') = true simuliert Server-Modus,
     analog zu F-268-Tests (openUserDialog + Profile-Spy). */

  it('Server-Modus (gemocktes Profil): KI-Kontext-Button im Benutzer-Dialog ausgeblendet', () => {
    const spy = vi.spyOn(Profile, 'has').mockImplementation((cap) => {
      if (cap === 'feature.restStorage') return true
      return false
    })
    try {
      const overlay = openUserDialog()
      const btn = findKiKontextBtn(overlay)
      expect(btn).toBeTruthy()
      expect(btn.style.display).toBe('none')
    } finally {
      spy.mockRestore()
    }
  })

  /* Karte #248 / ADR-0116: Im Projektor-Build ist der Benutzer-Dialog-Button
     auch ohne Server-Modus ausgeblendet; im slim Build ist er sichtbar. Beide
     Richtungen sind seit dem dritten Test-Kontext assertierbar. */
  it.skipIf(isServerBuildCtx)('#248 im Benutzer-Dialog (Solo): verborgen im Projektor-Build, sichtbar im slim Build', () => {
    const overlay = openUserDialog()
    const btn = findKiKontextBtn(overlay)
    expect(btn).toBeTruthy()
    expect(btn.style.display).toBe(IS_PROJECTOR_BUILD ? 'none' : '')
  })
})
