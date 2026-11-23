/* @vitest-environment happy-dom */

/**
 * Karte #681 — Oberflächen-Findings, die nur im Server-Build existieren:
 *   F-1554  ältere Antworten im KI-Verlauf: „Als Kommentar übernehmen“
 *   F-1549  openai_werkzeuge: 'aus' blendet Vorschläge und Nachladen aus
 *   F-1551  delegierendes Agenten-Token im Admin-Dialog
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }))

let tokenListe = []
vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listAgentTokens: vi.fn(async () => ({ ok: true, body: tokenListe })),
    listAgentSelbstbedienung: vi.fn(async () => ({ ok: true, body: { boards: [] } })),
  },
}))
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({ name: null, email: null }) }) },
}))

import { LLMTab } from '../src/llm-tab.js'
import { AdminDialog } from '../src/admin-dialog.js'
import { I18n }   from '../src/i18n.js'
import { State }  from '../src/state.js'
import { Config } from '../src/config.js'

const antwort = (status, body) => ({ ok: status >= 200 && status < 300, status, json: async () => body })
const ruhe = () => new Promise(r => setTimeout(r, 0))

const FIRMEN = { providers: [], keys: [], firmenmodus: { ziel: 'llm', anbieter: ['openai'], modelle: ['m'] } }
const OHNE   = { providers: ['openai'], keys: [{ provider: 'openai' }] }

function boardMitKarte() {
  return { version: 1, board: { id: 'b1', title: 'T',
    columnDefs: [{ id: 'col-ready', title: 'Ready' }],
    sprints: [{ id: 'sp-1', title: 'S', kind: 'sprint', cards: [
      { id: 'c1', displayId: 1, title: 'K', description: 'Hi', columnId: 'col-ready', labels: [],
        comments: [], qa: false, critical: false, blocked: false, activity: [] }] }] } }
}
const TURNS = [{ role: 'user', content: 'Frage' }, { role: 'assistant', content: 'Alte Antwort' }]

describeServer('KI-Verlauf: ältere Antworten übernehmen (F-1554)', () => {
  let rolle
  beforeEach(() => {
    I18n.setLang('de'); document.body.innerHTML = ''
    Config.load(); Config.set('userName', 'Andi')
    State.set(boardMitKarte()); LLMTab._resetCachesForTest()
    rolle = 'editor'
    globalThis.fetch = vi.fn(async (url) => String(url) === '/boards'
      ? antwort(200, [{ id: 'b1', title: 'T', role: rolle }]) : antwort(404, {}))
  })
  afterEach(() => { LLMTab.stopPolling(); document.body.innerHTML = ''; State.set(null) })

  function verlauf(keys) {
    LLMTab._setKeysCacheForTest(keys)
    const panel = LLMTab.build(); document.body.appendChild(panel)
    LLMTab._setStateForTest({ id: 'c1', title: 'K', comments: [], activity: [], links: [] }, 'b1')
    LLMTab._setConversationsForTest([{ id: 'v1', provider: 'openai', model: 'm' }], 'v1')
    LLMTab._renderTurnHistoryForTest(panel, TURNS)
    return panel
  }

  it('Firmenmodus: nur die Assistenten-Antwort trägt einen Knopf, nicht die Nutzerfrage', () => {
    const panel = verlauf(FIRMEN)
    const rows = panel.querySelectorAll('#llm-turn-history .llm-turn')
    expect(rows[0].querySelector('.llm-adopt-turn')).toBeNull()
    expect(rows[1].querySelector('.llm-adopt-turn')).toBeTruthy()
  })

  it('ohne Firmenmodus: kein Knopf (die Antwort steht schon als Kommentar an der Karte)', () => {
    expect(verlauf(OHNE).querySelector('.llm-adopt-turn')).toBeNull()
  })

  it('Klick als editor: ein Kommentar der Person mit Vorspann, zweiter Klick wirkungslos', async () => {
    const panel = verlauf(FIRMEN)
    const btn = panel.querySelector('.llm-adopt-turn')
    btn.click(); btn.click()
    await ruhe(); await ruhe()
    const c = State.get().board.sprints[0].cards[0].comments
    expect(c.length).toBe(1)
    expect(c[0].author).toBe('Andi')
    expect(c[0].text.startsWith(I18n.t('llm.uebernahme.vorspann', 'openai/m'))).toBe(true)
    expect(c[0].text).toContain('Alte Antwort')
    expect(btn.textContent).toBe(I18n.t('llm.preview.uebernommen'))
  })

  it('Klick als Betrachter (herabgestuft): verständliche Meldung, nichts geschrieben, Knopf wieder frei', async () => {
    rolle = 'viewer'
    const panel = verlauf(FIRMEN)
    const btn = panel.querySelector('.llm-adopt-turn')
    btn.click()
    await ruhe(); await ruhe()
    expect(State.get().board.sprints[0].cards[0].comments).toEqual([])
    expect(panel.querySelector('#llm-status').textContent).toBe(I18n.t('llm.error-acl'))
    expect(btn.disabled).toBe(false)
  })
})

describeServer('KI-Tab: Werkzeuge-Schalter des Betreibers (F-1549)', () => {
  beforeEach(() => {
    I18n.setLang('de'); document.body.innerHTML = ''
    Config.load(); State.set(boardMitKarte()); LLMTab._resetCachesForTest()
  })
  afterEach(() => { LLMTab.stopPolling(); document.body.innerHTML = ''; State.set(null) })

  async function oeffne(keys) {
    globalThis.fetch = vi.fn(async (url) => {
      const u = String(url)
      if (u.includes('/users/me/llm-keys')) return antwort(200, keys)
      if (u.includes('/llm/pricing')) return antwort(200, {})
      if (u.includes('/llm-pending')) return antwort(200, {})
      if (u.includes('/conversations')) return antwort(200, [])
      return antwort(404, {})
    })
    const panel = LLMTab.build(); document.body.appendChild(panel)
    await LLMTab.render({ id: 'c1', title: 'K', description: 'Hi', comments: [], activity: [], links: [] }, 'b1')
    await ruhe()
    return panel
  }

  it('openai_werkzeuge:"aus" → beide Angebote ausgeblendet und abgewählt, Hinweis nennt den Grund', async () => {
    const panel = await oeffne({ ...OHNE, openai_werkzeuge: 'aus' })
    expect(panel.querySelector('#llm-allow-mut-lbl').style.display).toBe('none')
    expect(panel.querySelector('#llm-allow-fetch-lbl').style.display).toBe('none')
    expect(panel.querySelector('#llm-allow-mutations').checked).toBe(false)
    expect(panel.querySelector('#llm-fetch-note').textContent).toBe(I18n.t('llm.werkzeuge-aus'))
  })

  it('ohne den Schalter bleiben beide Angebote sichtbar (Antwort wie bisher)', async () => {
    const panel = await oeffne(OHNE)
    expect(panel.querySelector('#llm-allow-mut-lbl').style.display).not.toBe('none')
    expect(panel.querySelector('#llm-allow-fetch-lbl').style.display).not.toBe('none')
    expect(panel.querySelector('#llm-fetch-note').textContent).not.toBe(I18n.t('llm.werkzeuge-aus'))
  })

  it('Senden bei "aus": Anfrage trägt weder enableMutations noch enableFetch, selbst wenn das Häkchen vorher gesetzt war', async () => {
    const panel = await oeffne({ ...OHNE, openai_werkzeuge: 'aus' })
    panel.querySelector('#llm-allow-mutations').checked = true
    panel.querySelector('#llm-allow-fetch').checked = true
    let body = null
    const alt = globalThis.fetch
    globalThis.fetch = vi.fn(async (url, opt) => {
      if (String(url).includes('/llm-send-stream')) { body = JSON.parse(opt.body); return antwort(500, {}) }
      return alt(url, opt)
    })
    panel.querySelector('#llm-send-btn').click()
    await new Promise(r => setTimeout(r, 10))
    expect(body).toBeTruthy()
    expect(body.enableMutations).toBeUndefined()
    expect(body.enableFetch).toBeUndefined()
  })
})

describeServer('Admin-Dialog: delegierendes Token (F-1551)', () => {
  beforeEach(() => { document.body.innerHTML = ''; I18n.setLang('de') })
  afterEach(() => { AdminDialog.close?.() })

  it('Board- und Person-Zelle erklären das delegierende Token; normale Tokens zeigen weiter ihre Werte', async () => {
    tokenListe = [
      { id: 'a1', name: 'owui', board_id: '', role: 'editor', delegierend: true, kennung: 'k1' },
      { id: 'a2', name: 'cli', board_id: 'board-42', role: 'viewer', user_email: 'p@x.de', kennung: 'k2' },
    ]
    AdminDialog.open(); await ruhe()
    document.querySelector('.admin-tab[data-section="agents"]').click()
    await ruhe(); await ruhe()
    const zeilen = [...document.querySelectorAll('.admin-agents-table tbody tr')]
    const zellen = (i) => [...zeilen[i].querySelectorAll('td')].map(td => td.textContent)
    expect(zellen(0)[1]).toBe(I18n.t('admin.agents.delegierend-board'))
    expect(zellen(0)[1]).toMatch(/alle Boards der Person/)
    expect(zellen(0)[2]).toBe(I18n.t('admin.agents.delegierend-person'))
    expect(zellen(1)[1]).toBe('board-42')
    expect(zellen(1)[2]).toBe('p@x.de')
  })
})
