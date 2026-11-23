/* @vitest-environment happy-dom */

/**
 * Karte #827 E4 (ADR-0141 §7, Plan D10): Rückweg an — außen ist eine
 * gebundene Karte (Spiegel oder in einer gespiegelten Lane angelegt)
 * bearbeitbar, soweit der Rückweg es nach innen trägt.
 *   - Modal: frei sind Titel, Beschreibung samt Checkliste, Priorität,
 *     kritisch, blockiert, Owner, Bearbeiter, Labels, Datum, archivieren,
 *     Kommentar anfügen, „vertraulich“ einschalten; gesperrt bleiben
 *     Löschen, qa, Komplexität, Aufwand, Wiederholung, Anhänge, Verweise,
 *     Datei am Kommentar, Agent-Kontext, KI, „vertraulich“ aus.
 *   - Renderer: gebundene Karten ziehbar, aber nicht wählbar; „Karte
 *     hinzufügen“ in gespiegelten Spalten gespiegelter Lanes.
 *   - DragDrop: gebunden nur in `column--rueckweg-ziel`.
 *   - boardpaar.js: Schalter aus /me, Modus je Karte und Termin.
 *
 * Gegenstück mit „aus“: tests/boardpaar-browser-826.test.js.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))
let meAntwort = { boardpaar: 'aussen', boardpaar_rueckweg: 'an' }
/* Upload-Test: ein Backend, das jeden Hochlade-Versuch zählt — ohne Backend
   scheiterte der Upload im Test still, „nichts hochgeladen“ wäre keine Aussage. */
let backendFuerTest = null
vi.mock('../src/storage-plugin.js', async (orig) => {
  const echt = await orig()
  return { ...echt, StoragePlugin: { ...echt.StoragePlugin,
    getActivePlugin: () => ({ getCurrentUser: async () => meAntwort,
      ...(backendFuerTest ? { attachments: () => backendFuerTest } : {}) }) } }
})
vi.mock('../src/sync.js', () => ({
  Sync: {
    getDirectoryName: vi.fn(() => null), listKnownBoards: vi.fn(async () => []), getInboxOpCount: vi.fn(() => 0),
    requestCompaction: vi.fn(async () => true), getChatTail: vi.fn(() => []), setChatTailListener: vi.fn(() => {}),
    setChatTailEnabled: vi.fn(async () => {}), readChatTail: vi.fn(async () => {}),
  },
}))
vi.mock('../assets/aBoard-icon.svg?raw', () => ({ default: '<svg/>' }))

const { EventBus } = await import('../src/eventbus.js')
const { Modal } = await import('../src/modal.js')
const { State } = await import('../src/state.js')
const { I18n } = await import('../src/i18n.js')
const { Renderer } = await import('../src/renderer.js')
const { DragDrop } = await import('../src/dragdrop.js')
const Bp = await import('../src/boardpaar.js')
void Modal

const warte = () => new Promise(r => setTimeout(r, 0))

const karte = (o = {}) => ({
  id: 'c1', displayId: 1, title: 'K', description: '', agentContext: '', comments: [],
  attachments: [], links: [], activity: [], priority: 'mittel', labels: [], owner: '',
  complexity: '', effort: null, qa: false, critical: false, blocked: false,
  archived: false, columnId: 'aussen-review', assignees: [], ...o,
})

const LANE = 'L'.repeat(43)
const REVIEW = 'R'.repeat(43)

function kopieBoard(spiegelKarten = [], eigeneKarten = []) {
  return {
    id: 'kopie-1', title: 'Kopie', identities: [],
    columnDefs: [
      { id: 'aussen-review', title: 'Review', spiegel: { id: REVIEW } },
      { id: 'eigen-spalte', title: 'Eigene Spalte' },
    ],
    sprints: [
      { id: 'aussen-lane', title: 'Sprint innen', kind: 'sprint', spiegel: { id: LANE }, cards: spiegelKarten },
      { id: 'eigen-lane', title: 'Eigene Lane', kind: 'lane', cards: eigeneKarten },
    ],
  }
}

let updates, geloescht
const merkeUpdate = c => updates.push(c)
const merkeLoeschen = id => geloescht.push(id)
beforeEach(() => {
  I18n.setLang('de')
  Bp._setzeRueckwegForTest(true)
  updates = []; geloescht = []
  document.body.innerHTML = '<div id="app"><header id="board-header"></header><div id="board-columns"></div></div>'
  EventBus.on('card:updated', merkeUpdate)
  EventBus.on('card:deleted', merkeLoeschen)
})
afterEach(() => {
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
  EventBus.off('card:updated', merkeUpdate)
  EventBus.off('card:deleted', merkeLoeschen)
  document.body.innerHTML = ''
  Bp._setzeRueckwegForTest(false)
  vi.unstubAllGlobals()
})

function oeffne(c, b) {
  State.set({ version: '2', board: b ?? kopieBoard([c]) })
  EventBus.emit('card:edit', c)
}
const klick = sel => document.querySelector(sel).dispatchEvent(new MouseEvent('click', { bubbles: true }))
const speichere = async () => { klick('.modal-actions-right .btn-primary'); await warte() }

describeServer('#827 boardpaar.js: Schalter und Modus', () => {
  beforeEach(() => { Bp._resetBoardpaarForTest() })

  it('übernimmt den Schalter aus /me nur außen und meldet einen Wechsel einmal', async () => {
    /* Karte #836: Der Schalter aus /me wirkt nur auf einem Board, das der
       Board-Abruf als bearbeitbar gemeldet hat — hier das offene Board. */
    State.set({ version: '2', board: kopieBoard([]) })
    Bp.setzeKopieStand(State.get().board.id, false, true)
    const meldungen = []
    const hoere = e => meldungen.push(e.an)
    EventBus.on('boardpaar:rueckweg', hoere)
    try {
      for (const [antwort, soll] of [
        [{ boardpaar: 'aussen', boardpaar_rueckweg: 'an' }, true],
        [{ boardpaar: 'aussen', boardpaar_rueckweg: 'an', boardpaar_fehler: true }, false],
        [{ boardpaar: 'aussen', boardpaar_rueckweg: 'an' }, true],
        [{ boardpaar: 'innen', boardpaar_rueckweg: 'an' }, false],
        [{ boardpaar: 'aussen', boardpaar_rueckweg: 'an' }, true],
        [{ boardpaar: 'aussen' }, false],
      ]) {
        meAntwort = antwort
        Bp.vergissBoardpaarSeite()
        await Bp.boardpaarSeite()
        expect(Bp.rueckwegAn(), JSON.stringify(antwort)).toBe(soll)
      }
      meAntwort = { boardpaar: 'aussen', boardpaar_rueckweg: 'an' }
      Bp.vergissBoardpaarSeite(); await Bp.boardpaarSeite()
      Bp.vergissBoardpaarSeite(); await Bp.boardpaarSeite()
      expect(meldungen).toEqual([true, false, true, false, true, false, true])
    } finally {
      EventBus.off('boardpaar:rueckweg', hoere)
    }
  })

  it('Modus: Spiegel und Karten gespiegelter Lanes sind gebunden, Vorlagen und eigene nicht', () => {
    const b = kopieBoard([karte({ id: 'sp', spiegel: { id: 'i' } }), karte({ id: 'w' })], [karte({ id: 'e', columnId: 'eigen-spalte' })])
    State.set({ version: '2', board: b })
    const k = id => b.sprints.flatMap(s => s.cards).find(c => c.id === id)
    Bp._setzeRueckwegForTest(true)
    expect(Bp.spiegelModus(k('sp'))).toBe('rueckweg')
    expect(Bp.spiegelModus(k('w'))).toBe('rueckweg')
    expect(Bp.spiegelModus(k('e'))).toBe(null)
    expect(Bp.spiegelModus(karte({ id: 'x', spiegel: { id: 'i' }, isTemplate: true }))).toBe(null)
    Bp._setzeRueckwegForTest(false)
    expect(Bp.spiegelModus(k('sp'))).toBe('lesen')
    expect(Bp.spiegelModus(k('w'))).toBe('lesen')
    expect(Bp.spiegelModus(k('e'))).toBe(null)
  })

  it('Termine: Spiegeltermine nach Schalter; eigene Termine einer Kopie nur bei an gebunden', () => {
    State.set({ version: '2', board: kopieBoard() })
    Bp._setzeRueckwegForTest(true)
    expect(Bp.terminModus({ id: 't', spiegel: { id: 'x' } })).toBe('rueckweg')
    expect(Bp.terminModus({ id: 'e' })).toBe('rueckweg')
    Bp._setzeRueckwegForTest(false)
    expect(Bp.terminModus({ id: 't', spiegel: { id: 'x' } })).toBe('lesen')
    expect(Bp.terminModus({ id: 'e' })).toBe(null)
    /* Ein Board ohne gespiegelte Lane ist keine Kopie: eigene Termine frei. */
    Bp._setzeRueckwegForTest(true)
    State.set({ version: '2', board: { id: 'b', columnDefs: [], sprints: [{ id: 'l', cards: [] }] } })
    expect(Bp.terminModus({ id: 'e' })).toBe(null)
  })
})

describeServer('#827 F-2224: Massenaktionen lassen gebundene Karten aus', () => {
  it('ohneGebundene: eine nach dem Wählen gebundene Karte fällt heraus, eigene bleiben', () => {
    State.set({ version: '2', board: kopieBoard([karte({ id: 'sp', spiegel: { id: 'i' } }), karte({ id: 'jetzt-gebunden' })],
      [karte({ id: 'eg', columnId: 'eigen-spalte' })]) })
    for (const an of [true, false]) {
      Bp._setzeRueckwegForTest(an)
      const { frei, weg } = Bp.ohneGebundene(new Set(['eg', 'jetzt-gebunden', 'sp', 'unbekannt']))
      expect([...frei].sort(), `an=${an}`).toEqual(['eg', 'unbekannt'])
      expect(weg.sort(), `an=${an}`).toEqual(['jetzt-gebunden', 'sp'])
    }
  })

  it('Quelltext-Wächter: jeder bulk:*-Handler in main.js liest die Auswahl über bulkAuswahl()', async () => {
    const { readFileSync } = await import('node:fs')
    const { join } = await import('node:path')
    const src = readFileSync(join(process.cwd(), 'src/main.js'), 'utf8')
    const handler = [...src.matchAll(/EventBus\.on\('bulk:([a-z-]+)'/g)].map(m => m[1])
    expect(handler.sort()).toEqual(['delete', 'label-add', 'label-remove', 'move', 'set-owner'])
    expect(src.match(/BulkSelect\.getSelected\(\)/g)?.length).toBe(1) /* nur in bulkAuswahl selbst */
    expect(src.match(/const selected = bulkAuswahl\(\);/g)?.length).toBe(5)
    /* Verify V-1: auch die Rückgabe — `return sel;` ließe den Filter wirkungslos. */
    const rumpf = src.match(/function bulkAuswahl\(\) \{([\s\S]*?)\n\}/)?.[1] ?? ''
    expect(rumpf).toMatch(/const \{ frei, weg \} = ohneGebundene\(sel\);/)
    expect(rumpf).toMatch(/weg\.forEach\(id => BulkSelect\.deselect\(id\)\);/)
    expect(rumpf.trim().split('\n').at(-1).trim()).toBe('return frei;')
  })
})

describeServer('#827 Modal: gebundene Karte bei Rückweg an', () => {
  it('Hinweisband „Rückweg“; frei ist die Feldliste, gesperrt bleiben Projekt-, Agenten- und Anhangsfelder', async () => {
    oeffne(karte({ spiegel: { id: 'i1' }, description: '- [ ] Punkt', dueDate: '2026-10-20' })); await warte()
    const band = document.querySelector('.modal-body > .modal-nur-lesen-band')
    expect(band?.textContent).toBe(I18n.t('card.spiegel.rueckweg'))
    /* F-2226: das Band nennt auch die gesperrten Projektfelder. */
    for (const wort of ['Löschen', 'Anhänge', 'QA', 'Komplexität', 'Aufwand', 'Wiederholung', 'Projekt']) expect(band.textContent, wort).toContain(wort)
    for (const sel of ['#modal-title', '#modal-priority', '#modal-critical', '#modal-blocked', '#modal-owner',
      '#modal-due-date', '#modal-due-time', '#modal-start-date', '#modal-label-neu', '.btn-archive',
      '#desc-pane .md-checkbox', '#comment-input', '.comment-form > .btn-primary', '.modal-actions-right .btn-primary',
      '#modal-confidential']) {
      expect(document.querySelector(sel)?.disabled, sel).toBe(false)
    }
    for (const sel of ['.btn-card-delete', '#modal-qa', '#modal-complexity', '#modal-effort',
      '#modal-recurrence-interval', '.comment-upload-btn', '#link-add-btn']) {
      const e = document.querySelector(sel)
      expect(e, sel).not.toBeNull()
      expect(e.disabled, sel).toBe(true)
    }
  })

  it('Titel und Priorität ändern und speichern geht nach außen raus — nur diese Felder ändern sich', async () => {
    const c = karte({ spiegel: { id: 'i1' }, qa: false, complexity: 'mittel' })
    oeffne(c); await warte()
    const titel = document.querySelector('#modal-title')
    titel.value = 'Neu außen'
    titel.dispatchEvent(new Event('input', { bubbles: true }))
    await speichere()
    expect(updates.length).toBe(1)
    expect(updates[0].title).toBe('Neu außen')
    expect(updates[0].complexity).toBe('mittel')
    expect(updates[0].qa).toBe(false)
    expect(updates[0].spiegel).toEqual({ id: 'i1' })
  })

  it('Löschen bleibt gesperrt — auch per Skript entsperrt fängt die Klick-Sperre es ab', async () => {
    vi.stubGlobal('confirm', () => true)
    oeffne(karte({ spiegel: { id: 'i1' } })); await warte()
    const weg = document.querySelector('.btn-card-delete')
    weg.disabled = false
    weg.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    expect(geloescht).toEqual([])
  })

  it('qa per Skript entsperrt: die Klick-Sperre fängt die Änderung ab', async () => {
    oeffne(karte({ spiegel: { id: 'i1' } })); await warte()
    const qa = document.querySelector('#modal-qa')
    qa.disabled = false
    qa.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    await speichere()
    expect(updates.every(u => u.qa === false)).toBe(true)
  })

  it('Häkchen der Checkliste speichert die Beschreibung', async () => {
    oeffne(karte({ spiegel: { id: 'i1' }, description: '- [ ] Angebot prüfen' })); await warte()
    const cb = document.querySelector('#desc-pane .md-checkbox')
    cb.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    cb.checked = true
    cb.dispatchEvent(new Event('change', { bubbles: true }))
    await warte()
    expect(updates.map(u => u.description)).toContain('- [x] Angebot prüfen')
  })

  it('Doppelklick öffnet den Editor der Beschreibung, der leere Agent-Kontext bleibt zu', async () => {
    oeffne(karte({ spiegel: { id: 'i1' }, description: 'Ein Absatz', owner: 'Claude', agentContext: '' })); await warte()
    document.querySelector('#desc-pane .typora-block').dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    await warte()
    expect(document.querySelector('#desc-pane .typora-editing')).not.toBeNull()
    klick('.tab-btn[data-tab="agent"]'); await warte()
    document.querySelector('#agent-pane .agent-placeholder').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    expect(document.querySelector('#agent-pane .typora-new')).toBeNull()
  })

  it('ein vorhandener Agent-Kontext öffnet per Doppelklick keinen Editor', async () => {
    oeffne(karte({ spiegel: { id: 'i1' }, owner: 'Claude', agentContext: 'Ein Hinweis' })); await warte()
    klick('.tab-btn[data-tab="agent"]'); await warte()
    const block = document.querySelector('#agent-pane .typora-block')
    expect(block).not.toBeNull()
    /* Der Agent-Kontext öffnet je nach Modus per Klick oder Doppelklick — beides. */
    block.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    block.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    expect(document.querySelector('#agent-pane .typora-editing')).toBeNull()
    expect(document.querySelector('#agent-pane textarea')).toBeNull()
  })

  it('Kommentar anfügen geht raus, eine Datei am Kommentar nicht', async () => {
    oeffne(karte({ spiegel: { id: 'i1' } })); await warte()
    klick('.tab-btn[data-tab="comments"]'); await warte()
    document.querySelector('#comment-input').value = 'Von außen'
    klick('.comment-form > .btn-primary'); await warte()
    await speichere()
    const letzte = updates.at(-1)
    expect(letzte?.comments?.map(k => k.text)).toContain('Von außen')
    expect(document.querySelector('.comment-upload-btn')?.disabled ?? true).toBe(true)
  })

  it('Anhänge: kein Ablege-Hinweis, Ablegen lädt nichts hoch, der Upload-Knopf ist gesperrt', async () => {
    oeffne(karte({ spiegel: { id: 'i1' } })); await warte()
    klick('.tab-btn[data-tab="attachments"]'); await warte()
    expect([...document.querySelectorAll('#tab-panel-attachments button')].every(b => b.disabled)).toBe(true)
    const hochgeladen = vi.fn(async () => ({ id: 'a1', filename: 'x.txt', mime: 'text/plain', size: 1 }))
    backendFuerTest = { kartenAnhangImBoardState: () => false, checkUpload: () => ({ ok: true }), uploadFile: hochgeladen,
      humanSize: () => '1 B', computeBoardBytes: () => 0 }
    try {
      const dt = { types: ['Files'], files: [new File(['x'], 'x.txt', { type: 'text/plain' })], dropEffect: '' }
      const sende = typ => {
        const ev = new Event(typ, { bubbles: true, cancelable: true })
        Object.defineProperty(ev, 'dataTransfer', { value: dt })
        document.querySelector('.modal').dispatchEvent(ev)
      }
      sende('dragenter')
      const ablage = document.querySelector('.attachment-drop-overlay')
      expect(ablage === null || ablage.style.display === 'none').toBe(true)
      sende('drop')
      for (let i = 0; i < 5; i++) await warte()
      expect(hochgeladen).not.toHaveBeenCalled()
    } finally {
      backendFuerTest = null
    }
  })

  it('#862: Hinweis „Anhänge nicht möglich“ im Anhänge-Tab und am Kommentar — Spiegel und wartende Karte, nicht bei eigener Karte', async () => {
    const hinweis = I18n.t('attachment.spiegel-gesperrt')
    expect(hinweis).not.toBe('attachment.spiegel-gesperrt')
    for (const c of [karte({ spiegel: { id: 'i1' } }), karte({ id: 'wartet' })]) {
      oeffne(c); await warte()
      klick('.tab-btn[data-tab="attachments"]'); await warte()
      const h = [...document.querySelectorAll('.anhang-sperr-hinweis')]
      expect(h.map(e => e.parentElement.className.split(' ')[0]).sort()).toEqual(['attachments-actions', 'comment-form'])
      expect(h.every(e => e.textContent === hinweis)).toBe(true)
      /* F-2410: Klasse statt Inline-Stil; die Regel überspannt beide Spalten des .comment-form */
      expect(h.every(e => !e.hasAttribute('style'))).toBe(true)
      expect(document.querySelector('#attachments-upload, .attachments-actions button').disabled).toBe(true)
      expect(document.querySelector('.comment-upload-btn').disabled).toBe(true)
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); await warte()
    }
    const n = karte({ id: 'n2', columnId: 'eigen-spalte' })
    oeffne(n, kopieBoard([], [n])); await warte()
    expect(document.querySelector('.anhang-sperr-hinweis')).toBeNull()
    expect(document.querySelector('.attachments-actions button').disabled).toBe(false)
  })

  it('F-2410: der gegatete Zweig spritzt für .anhang-sperr-hinweis grid-column über alle Spalten ein (nicht style.css, V-4)', async () => {
    const c = karte({ spiegel: { id: 'i1' } })
    oeffne(c); await warte()
    const css = document.getElementById('anhang-sperr-hinweis-stil')?.textContent ?? ''
    expect(css).toMatch(/\.anhang-sperr-hinweis\{[^}]*grid-column:1\/-1/)
    const { readFileSync } = await import('node:fs')
    expect(readFileSync(`${process.cwd()}/src/style.css`, 'utf8')).not.toMatch(/anhang-sperr-hinweis/)
  })

  it('Gegenprobe zum Upload: auf einer eigenen Karte lädt das Test-Backend hoch', async () => {
    const n = karte({ id: 'n1', columnId: 'eigen-spalte' })
    oeffne(n, kopieBoard([], [n])); await warte()
    const hochgeladen = vi.fn(async () => ({ id: 'a1', filename: 'x.txt', mime: 'text/plain', size: 1 }))
    backendFuerTest = { kartenAnhangImBoardState: () => false, checkUpload: () => ({ ok: true }), uploadFile: hochgeladen,
      humanSize: () => '1 B', computeBoardBytes: () => 0 }
    try {
      const ev = new Event('drop', { bubbles: true, cancelable: true })
      Object.defineProperty(ev, 'dataTransfer', { value: { types: ['Files'], files: [new File(['x'], 'x.txt', { type: 'text/plain' })] } })
      document.querySelector('.modal').dispatchEvent(ev)
      for (let i = 0; i < 5; i++) await warte()
      expect(hochgeladen).toHaveBeenCalledTimes(1)
    } finally {
      backendFuerTest = null
    }
  })

  it('nach einem Tab-Wechsel neu gebaute Knöpfe sind gesperrt und wirkungslos', async () => {
    const ziel = karte({ id: 'eigen', columnId: 'eigen-spalte' })
    const c = karte({ id: 's1', spiegel: { id: 'i1' }, links: [{ type: 'related', targetId: 'eigen' }] })
    let entfernt = 0
    const zaehle = () => { entfernt++ }
    EventBus.on('card:link-remove', zaehle)
    oeffne(c, kopieBoard([c], [ziel])); await warte()
    klick('.tab-btn[data-tab="links"]'); await warte()
    klick('.tab-btn[data-tab="desc"]'); await warte()
    klick('.tab-btn[data-tab="links"]'); await warte()
    const x = document.querySelector('.link-remove-btn')
    expect(x).not.toBeNull()
    expect(x.disabled).toBe(true)
    x.disabled = false
    x.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await warte()
    EventBus.off('card:link-remove', zaehle)
    expect(entfernt).toBe(0)
  })

  it('Verweise: auf einer gebundenen Karte gesperrt; der Picker einer eigenen Karte bietet keine gebundene an', async () => {
    const sp = karte({ id: 'sp', title: 'Spiegelkarte', spiegel: { id: 'i1' } })
    const w = karte({ id: 'w', title: 'Wartende' })
    const e2 = karte({ id: 'e2', title: 'Andere eigene', columnId: 'eigen-spalte' })
    const n = karte({ id: 'n1', title: 'Eigen', columnId: 'eigen-spalte', links: [{ type: 'related', targetId: 'w' }] })
    const b = kopieBoard([sp, w], [n, e2])
    oeffne(n, b); await warte()
    klick('.tab-btn[data-tab="links"]'); await warte()
    expect(document.querySelector('.link-entry .link-remove-btn')).toBeNull()
    klick('#link-add-btn'); await warte()
    const zeilen = [...document.querySelectorAll('.link-picker-row')].map(r => r.textContent)
    expect(zeilen.some(z => z.includes('Andere eigene'))).toBe(true)
    expect(zeilen.some(z => z.includes('Spiegelkarte') || z.includes('Wartende'))).toBe(false)
  })

  it('KI-Tab bleibt aus', async () => {
    const { Profile } = await import('../src/profile.js')
    const { LLMTab } = await import('../src/llm-tab.js')
    vi.spyOn(Profile, 'has').mockImplementation(k => k === 'feature.restStorage')
    vi.spyOn(LLMTab, 'hasAnyKey').mockResolvedValue(true)
    oeffne(karte({ spiegel: { id: 'i1' } })); await warte(); await warte()
    expect(document.querySelector('#tab-llm').style.display).toBe('none')
    vi.restoreAllMocks()
  })

  it('„vertraulich“ lässt sich einschalten — und steht es schon an, nicht mehr aus', async () => {
    oeffne(karte({ spiegel: { id: 'i1' } })); await warte()
    const cb = document.querySelector('#modal-confidential')
    expect(cb.hidden).toBe(false)
    cb.checked = true
    cb.dispatchEvent(new Event('change', { bubbles: true }))
    await speichere()
    expect(updates.at(-1)?.confidential).toBe(true)
    updates = []
    oeffne(karte({ spiegel: { id: 'i1' }, confidential: true })); await warte()
    const an = document.querySelector('#modal-confidential')
    expect(an.hidden).toBe(false)
    expect(an.disabled).toBe(true)
    /* Selbst entsperrt fängt die Klick-Sperre ihn ab. */
    an.disabled = false
    an.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    await speichere()
    expect(updates.every(u => u.confidential !== false)).toBe(true)
  })

  it('eine Karte ohne Marke in einer gespiegelten Lane (wartend) ist gebunden — bei aus nur lesbar', async () => {
    const w = karte({ id: 'w', title: 'Wartend' })
    oeffne(w, kopieBoard([w])); await warte()
    expect(document.querySelector('.modal-nur-lesen-band')?.textContent).toBe(I18n.t('card.spiegel.rueckweg'))
    expect(document.querySelector('.btn-card-delete').disabled).toBe(true)
    klick('.btn-modal-close'); await warte()
    Bp._setzeRueckwegForTest(false)
    oeffne(w, kopieBoard([w])); await warte()
    expect(document.querySelector('.modal-nur-lesen-band')?.textContent).toBe(I18n.t('card.spiegel.nur-lesen'))
    expect(document.querySelector('#modal-title').disabled).toBe(true)
  })

  it('eine eigene Karte bleibt frei — ohne Band, Löschen offen', async () => {
    const n = karte({ id: 'n1', columnId: 'eigen-spalte' })
    oeffne(n, kopieBoard([], [n])); await warte()
    expect(document.querySelector('.modal-nur-lesen-band')).toBeNull()
    expect(document.querySelector('.btn-card-delete').disabled).toBe(false)
    expect(document.querySelector('#modal-qa').disabled).toBe(false)
  })

  /* Sprint-94-Kritik F-B: ADR-0141 Punkt 30 und das Handbuch sagen, die
     Übernahme lasse sich außen aufheben. Der Knopf wird erst mit einem Grund
     aktiv — war das Grund-Feld gesperrt, blieb er dauerhaft grau. */
  it('Übernahme aufheben: Grund-Feld frei, Knopf wird mit Grund aktiv und schickt ab', async () => {
    const bis = new Date(Date.now() + 3600_000).toISOString()
    const gesendet = []
    const fang = e => gesendet.push(e)
    EventBus.on('card:claim-release', fang)
    try {
      oeffne(karte({ spiegel: { id: 'i1' }, claim: { by: 'agent:a', session: 's', since: '2026-10-14T08:00:00.000Z', until: bis } }))
      await warte()
      const grund = document.querySelector('#modal-claim-grund')
      const knopf = document.querySelector('#modal-claim-aufheben')
      expect(grund.disabled).toBe(false)
      expect(knopf.disabled).toBe(true)
      grund.value = 'Sitzung hängt'
      grund.dispatchEvent(new Event('input'))
      expect(knopf.disabled).toBe(false)
      knopf.click()
      expect(gesendet).toHaveLength(1)
      expect(gesendet[0]).toMatchObject({ cardId: 'c1', grund: 'Sitzung hängt' })
    } finally {
      EventBus.off('card:claim-release', fang)
    }
  })
})

describeServer('#827 Renderer: Rückweg an', () => {
  function zeichne(b) {
    State.set({ version: '2', board: b })
    Renderer.renderBoard(State.get())
  }

  it('gebundene Karten tragen card--gebunden ohne card--nur-lesen; die wartende ohne Abzeichen', () => {
    zeichne(kopieBoard([karte({ id: 'sp', spiegel: { id: 'i1' } }), karte({ id: 'w' })], [karte({ id: 'e', columnId: 'eigen-spalte' })]))
    const k = id => document.querySelector(`.card[data-id="${id}"]`)
    expect(k('sp').classList.contains('card--gebunden')).toBe(true)
    expect(k('sp').classList.contains('card--nur-lesen')).toBe(false)
    expect(k('w').classList.contains('card--gebunden')).toBe(true)
    expect(k('w').querySelector('.card-spiegel-badge')).toBeNull()
    expect(k('e').classList.contains('card--gebunden')).toBe(false)
    expect(k('sp').querySelector('.card-spiegel-badge').title).toBe(I18n.t('card.spiegel.hint.rueckweg'))
  })

  it('„Karte hinzufügen“ in der gespiegelten Spalte der gespiegelten Lane, nicht in ihrer eigenen Spalte', () => {
    zeichne(kopieBoard())
    const spalte = id => document.querySelector(`.swimlane[data-sprint-id="aussen-lane"] .column[data-id="${id}"]`)
    expect(spalte('aussen-review').classList.contains('column--rueckweg-ziel')).toBe(true)
    expect(spalte('aussen-review').querySelector('.btn-add-card')).not.toBeNull()
    expect(spalte('eigen-spalte').classList.contains('column--rueckweg-ziel')).toBe(false)
    expect(spalte('eigen-spalte').querySelector('.btn-add-card')).toBeNull()
    /* Leeren und Massen-Archiv bleiben dem Spaltenkopf gesperrt. */
    expect(spalte('aussen-review').querySelector('.btn-col-clear')).toBeNull()
    /* In der eigenen Lane: die gespiegelte Spalte ist kein Rückweg-Ziel. */
    const eigen = document.querySelector('.swimlane[data-sprint-id="eigen-lane"] .column[data-id="aussen-review"]')
    expect(eigen.classList.contains('column--rueckweg-ziel')).toBe(false)
    expect(eigen.querySelector('.btn-add-card')).not.toBeNull()
    /* F-2225: Der Rückweg-Hinweis steht nur am Rückweg-Ziel, in der eigenen Lane der aus 2b. */
    expect(spalte('aussen-review').querySelector('.column-title').title).toBe(I18n.t('lane.spiegel.hint.rueckweg'))
    expect(eigen.querySelector('.column-title').title).toBe(I18n.t('lane.spiegel.hint'))
  })

  it('Lane-Kopf bleibt schlank, mit dem Hinweis für den Rückweg', () => {
    zeichne(kopieBoard())
    const kopf = document.querySelector('.swimlane[data-sprint-id="aussen-lane"] .sprint-header')
    expect(kopf.classList.contains('sprint-header--spiegel')).toBe(true)
    expect(kopf.querySelector('.sprint-drag-handle')).toBeNull()
    expect(kopf.querySelector('.sprint-title').title).toBe(I18n.t('lane.spiegel.hint.rueckweg'))
  })

  it('gebundene Karten bleiben unwählbar (Strg-Klick, Umschalt-Klick, Strg+A)', async () => {
    const { BulkSelect } = await import('../src/bulk-select.js')
    const { Shortcuts } = await import('../src/shortcuts.js')
    Shortcuts.init()
    Shortcuts.setContext('board')
    BulkSelect.clearAll()
    const op = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetParent')
    Object.defineProperty(HTMLElement.prototype, 'offsetParent', { configurable: true, get() { return document.body } })
    try {
      zeichne(kopieBoard([karte({ id: 'sp', spiegel: { id: 'i1' } }), karte({ id: 'w' })], [karte({ id: 'eg', columnId: 'eigen-spalte' })]))
      for (const id of ['sp', 'w']) {
        document.querySelector(`.card[data-id="${id}"]`).dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true }))
      }
      expect(BulkSelect.count()).toBe(0)
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', ctrlKey: true, bubbles: true }))
      expect([...BulkSelect.getSelected()]).toEqual(['eg'])
      document.querySelector('.card[data-id="w"]').dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
      expect([...BulkSelect.getSelected()]).toEqual(['eg'])
      /* Bereich über eine gespiegelte Lane hinweg: die gebundenen dazwischen fehlen. */
      const b = kopieBoard([karte({ id: 'w' })], [karte({ id: 'eg', columnId: 'eigen-spalte' })])
      b.sprints.push({ id: 'eigen-2', title: 'Zweite', kind: 'lane', cards: [karte({ id: 'eg2', columnId: 'eigen-spalte' })] })
      /* Die gespiegelte Lane zwischen die beiden eigenen legen (gleiche Art, damit die Reihenfolge bleibt). */
      b.sprints = [b.sprints[1], { ...b.sprints[0], kind: 'lane' }, b.sprints[2]]
      BulkSelect.clearAll()
      zeichne(b)
      document.querySelector('.card[data-id="eg2"]').dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true }))
      document.querySelector('.card[data-id="eg"]').dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
      expect([...BulkSelect.getSelected()].sort()).toEqual(['eg', 'eg2'])
    } finally {
      if (op) Object.defineProperty(HTMLElement.prototype, 'offsetParent', op)
      else delete HTMLElement.prototype.offsetParent
      BulkSelect.clearAll()
    }
  })
})

describe('#827 DragDrop: gebunden nur in Rückweg-Ziele', () => {
  let bewegt
  let initialisiert = false
  const merke = p => bewegt.push(p)
  beforeEach(() => {
    bewegt = []
    EventBus.on('card:moved', merke)
    Element.prototype.setPointerCapture ??= function () {}
    document.body.innerHTML = `
      <div class="column column--spiegel column--struktur-spiegel column--rueckweg-ziel" data-id="a" data-sprint-id="aussen-lane">
        <div class="card card--spiegel card--gebunden" data-id="spiegel"></div>
      </div>
      <div class="column column--spiegel" data-id="b" data-sprint-id="aussen-lane"><div class="card" data-id="x"></div></div>
      <div class="column" data-id="c" data-sprint-id="eigen-lane">
        <div class="card" data-id="eigen"></div>
      </div>`
    if (!initialisiert) { DragDrop.init(); initialisiert = true }
  })
  afterEach(() => {
    EventBus.off('card:moved', merke)
    vi.restoreAllMocks()
  })
  function ziehe(el, ziel) {
    el.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, button: 0, clientX: 0, clientY: 0, pointerId: 1 }))
    vi.spyOn(document, 'elementFromPoint').mockReturnValue(document.querySelector(ziel))
    el.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientX: 50, clientY: 50, pointerId: 1 }))
    el.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientX: 50, clientY: 50, pointerId: 1 }))
  }

  it('gebundene Karte in ein Rückweg-Ziel wird verschoben', () => {
    ziehe(document.querySelector('[data-id="spiegel"]'), '.column[data-id="a"]')
    expect(bewegt.length).toBe(1)
  })
  it('gebundene Karte nicht in eine eigene Spalte und nicht in eine gespiegelte Lane ohne Ziel', () => {
    ziehe(document.querySelector('[data-id="spiegel"]'), '.column[data-id="c"]')
    ziehe(document.querySelector('[data-id="spiegel"]'), '.column[data-id="b"]')
    expect(bewegt).toEqual([])
  })
  it('eigene Karte in ein Rückweg-Ziel ja, in eine gespiegelte Lane ohne Ziel nein', () => {
    ziehe(document.querySelector('[data-id="eigen"]'), '.column[data-id="b"]')
    expect(bewegt).toEqual([])
    ziehe(document.querySelector('[data-id="eigen"]'), '.column[data-id="a"]')
    expect(bewegt.length).toBe(1)
  })
})
