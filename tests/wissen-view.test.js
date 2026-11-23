/* @vitest-environment happy-dom */

/* Lesesicht der Wissensquelle (Karte #739, ADR-0137 §3/§6).
   loeseSeitenlink ist rein und läuft in jedem Kontext; Dialog und
   Seitenleisten-Eintrag gibt es nur im Server-Build (describeServer). */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

let currentUser = { name: 'Anna', email: 'anna@x.de' }
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      getCurrentUser: async () => currentUser,
      listKnownBoards: async () => [],
    }),
  },
}))

import { WissenView, __testingWissen__ } from '../src/wissen-view.js'
import { Sidebar } from '../src/sidebar.js'
import { EventBus } from '../src/eventbus.js'
import { State } from '../src/state.js'

const { loeseSeitenlink } = __testingWissen__

describe('loeseSeitenlink', () => {
  const seiten = new Set([
    'Datenschutz & Consent/beispiel__crawling__cookies.md',
    'Datenschutz & Consent/übersicht.md',
    'aboard/start.md',
    'aboard/karten/anlegen.md',
    'README.md',
    'a/dup.md',
    'b/dup.md',
  ])
  it.each([
    ['aboard/start.md', 'karten/anlegen.md', 'aboard/karten/anlegen.md'],
    ['aboard/karten/anlegen.md', '../start.md', 'aboard/start.md'],
    ['aboard/start.md', './karten/anlegen.md#abschnitt', 'aboard/karten/anlegen.md'],
    ['aboard/start.md', '../Datenschutz%20%26%20Consent/beispiel__crawling__cookies.md', 'Datenschutz & Consent/beispiel__crawling__cookies.md'],
    ['aboard/start.md', '../Datenschutz & Consent/beispiel__crawling__cookies.md', 'Datenschutz & Consent/beispiel__crawling__cookies.md'],
    ['aboard/start.md', '/README.md', 'README.md'],
    ['aboard/start.md', 'karten/anlegen', 'aboard/karten/anlegen.md'],
    /* NFD im Link, NFC im Index */
    ['aboard/start.md', '../Datenschutz & Consent/übersicht.md', 'Datenschutz & Consent/übersicht.md'],
    /* Ordner umgebaut, Link zeigt auf alten Ort — eindeutiger Dateiname trägt */
    ['aboard/start.md', '../alt/beispiel__crawling__cookies.md', 'Datenschutz & Consent/beispiel__crawling__cookies.md'],
  ])('%s + %s → %s', (basis, ziel, erwartet) => {
    expect(loeseSeitenlink(basis, ziel, seiten)).toBe(erwartet)
  })
  it.each([
    ['aboard/start.md', '../../README.md'],     /* über die Wurzel hinaus */
    ['aboard/start.md', 'gibts-nicht.md'],
    ['aboard/start.md', '../x/dup.md'],          /* Dateiname nicht eindeutig */
    ['aboard/start.md', '#nur-anker'],
    ['aboard/start.md', ''],
    ['aboard/start.md', '%E0%A4%A'],            /* kaputte Kodierung → roh, nicht gefunden */
    /* K-5: Ordnerlink und Name ohne Endung lösen NICHT über den Dateinamen auf */
    ['README.md', 'karten/'],
    ['README.md', 'anlegen'],
    ['aboard/start.md', '../woanders/anlegen'],
    ['aboard/start.md', '../alt/anlegen.md/'],    /* Ordnerlink, der wie eine Seite heisst */
  ])('%s + %s → null', (basis, ziel) => {
    expect(loeseSeitenlink(basis, ziel, seiten)).toBeNull()
  })
})

/* fetch-Stub: Antworten je Pfad-Präfix. */
let antworten = {}
let aufrufe = []
function stubFetch() {
  vi.stubGlobal('fetch', vi.fn(async (url) => {
    aufrufe.push(url)
    const key = Object.keys(antworten).find(k => url.startsWith(k))
    const a = key ? antworten[key] : { status: 404, body: { error: 'not found' } }
    return { ok: a.status === 200, status: a.status, json: async () => a.body }
  }))
}
const warte = () => new Promise(r => setTimeout(r, 0))

describeServer('WissenView', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    aufrufe = []
    antworten = {
      '/wissen/index': { status: 200, body: {
        name: 'Team-Wissen',
        stand: { commit: 'c0db7e3', datum: '2026-10-08' },
        seiten: [
          { pfad: 'README.md', titel: 'Übersicht', groesse: 10 },
          { pfad: 'Datenschutz & Consent/cookies.md', titel: 'Cookies', groesse: 10 },
          { pfad: 'aboard/start.md', titel: 'Start', groesse: 10 },
        ],
      } },
      '/wissen/seite?pfad=aboard%2Fstart.md': { status: 200, body: {
        pfad: 'aboard/start.md', titel: 'Start',
        inhalt: '# Start\n\n![pixel](https://x.example/?d=1)\n\n<div><img src="/a.png"></div>\n\nSiehe [Cookies](../Datenschutz%20%26%20Consent/cookies.md) und [weg](nirgends.md).',
        bearbeiten: 'https://forge.example/edit/aboard/start.md',
      } },
      '/wissen/seite?pfad=Datenschutz%20%26%20Consent%2Fcookies.md': { status: 200, body: {
        pfad: 'Datenschutz & Consent/cookies.md', titel: 'Cookies', inhalt: 'Consent', bearbeiten: 'javascript:alert(1)',
      } },
    }
    stubFetch()
  })
  afterEach(() => {
    WissenView.close()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('Übersicht: gruppiert nach erstem Ordner, Name als Titel, Stand im Fuss', async () => {
    await WissenView.open('Wissen')
    expect(document.querySelector('#wv-dialog-title').textContent).toBe('Team-Wissen')
    const ordner = [...document.querySelectorAll('.wv-ordner')].map(h => h.textContent)
    expect(ordner).toEqual(['Datenschutz & Consent', 'aboard'])
    expect(document.querySelectorAll('.wv-eintrag').length).toBe(3)
    expect(document.querySelector('.wv-fuss').textContent).toContain('c0db7e3')
  })

  it('Seite: Modus sicher, Seitenlink folgt, Bearbeiten-Link nur https', async () => {
    await WissenView.open('Wissen')
    const start = [...document.querySelectorAll('.wv-eintrag')].find(b => b.textContent === 'Start')
    start.click()
    await warte()
    expect(aufrufe).toContain('/wissen/seite?pfad=aboard%2Fstart.md')
    const inhalt = document.querySelector('.wv-inhalt')
    expect(inhalt.querySelector('img')).toBeNull()
    expect(inhalt.textContent).toContain('pixel')
    expect(inhalt.textContent).toContain('<img src="/a.png">')
    const edit = document.querySelector('.wv-fuss a')
    expect(edit.href).toBe('https://forge.example/edit/aboard/start.md')
    expect(edit.rel).toBe('noopener noreferrer')

    inhalt.querySelector('a.md-seitenlink').click()
    await warte()
    expect(aufrufe).toContain('/wissen/seite?pfad=Datenschutz%20%26%20Consent%2Fcookies.md')
    expect(document.querySelector('.wv-inhalt').textContent).toContain('Consent')
    /* Server-Antwort mit javascript:-Link → kein Link */
    expect(document.querySelector('.wv-fuss a')).toBeNull()
  })

  it('Seitenlink ins Leere → Hinweis über der Seite, Seite bleibt, kein Abruf', async () => {
    await WissenView.open('Wissen')
    ;[...document.querySelectorAll('.wv-eintrag')].find(b => b.textContent === 'Start').click()
    await warte()
    const vorher = aufrufe.length
    const tot = () => [...document.querySelectorAll('a.md-seitenlink')].find(a => a.textContent === 'weg')
    tot().click()
    await warte()
    expect(aufrufe.length).toBe(vorher)
    expect(document.querySelector('.wv-linkhinweis').textContent).toContain('nirgends.md')
    expect(document.querySelector('.wv-inhalt h1').textContent).toBe('Start')
    tot().click()
    expect(document.querySelectorAll('.wv-linkhinweis').length).toBe(1)
    /* F-2876: der frisch erzeugte Hinweis trägt kein role=status, sondern wird über die dauerhafte Live-Region angesagt */
    const hinweis = document.querySelector('.wv-linkhinweis')
    expect(hinweis.getAttribute('role')).toBeNull()
    expect(hinweis.getAttribute('aria-hidden')).toBe('true')
    const region = document.body.querySelector(':scope > .visually-hidden[role="status"]')
    expect(region.textContent.trim()).toBe(hinweis.textContent)
  })

  it('Fokus wandert nach dem Öffnen einer Seite auf den Inhalt', async () => {
    await WissenView.open('Wissen')
    ;[...document.querySelectorAll('.wv-eintrag')].find(b => b.textContent === 'Start').click()
    await warte()
    expect(document.activeElement).toBe(document.querySelector('.wv-inhalt'))
  })

  it('K-4a: eine noch wartende Suche überschreibt eine danach geöffnete Seite nicht', async () => {
    antworten['/wissen/suche?q='] = { status: 200, body: { treffer: [{ pfad: 'README.md', titel: 'TREFFERLISTE' }] } }
    await WissenView.open('Wissen')
    vi.useFakeTimers()
    const input = document.querySelector('.wv-suche')
    input.value = 'st'
    input.dispatchEvent(new Event('input'))
    ;[...document.querySelectorAll('.wv-eintrag')].find(b => b.textContent === 'Start').click()
    vi.advanceTimersByTime(1000)
    vi.useRealTimers()
    await warte()
    expect(aufrufe.some(u => u.startsWith('/wissen/suche'))).toBe(false)
    expect(document.querySelector('.wv-inhalt').textContent).not.toContain('TREFFERLISTE')
  })

  it('späte Antwort nach Ansichtswechsel wird verworfen', async () => {
    let loese
    antworten['/wissen/seite?pfad=README.md'] = { status: 200, body: { pfad: 'README.md', titel: 'Ü', inhalt: 'SPAETE SEITE' } }
    await WissenView.open('Wissen')
    const orig = globalThis.fetch
    vi.stubGlobal('fetch', vi.fn((url) => url.includes('README')
      ? new Promise(r => { loese = () => r({ ok: true, status: 200, json: async () => antworten['/wissen/seite?pfad=README.md'].body }) })
      : orig(url)))
    ;[...document.querySelectorAll('.wv-eintrag')].find(b => b.textContent === 'Übersicht').click()
    document.querySelector('.wv-zurueck').click()
    loese()
    await warte(); await warte()
    expect(document.querySelector('.wv-inhalt').textContent).not.toContain('SPAETE SEITE')
    expect(document.querySelectorAll('.wv-eintrag').length).toBe(3)
  })

  it('unter zwei Zeichen (nach NFC) wieder Übersicht statt alter Treffer', async () => {
    antworten['/wissen/suche?q='] = { status: 200, body: { treffer: [{ pfad: 'README.md', titel: 'TREFFER' }] } }
    await WissenView.open('Wissen')
    vi.useFakeTimers()
    const input = document.querySelector('.wv-suche')
    input.value = 'ab'
    input.dispatchEvent(new Event('input'))
    vi.advanceTimersByTime(300)
    vi.useRealTimers()
    await warte()
    expect(document.querySelector('.wv-inhalt').textContent).toContain('TREFFER')
    vi.useFakeTimers()
    input.value = 'e\u0301'
    input.dispatchEvent(new Event('input'))
    vi.advanceTimersByTime(1000)
    vi.useRealTimers()
    await warte()
    expect(aufrufe.filter(u => u.startsWith('/wissen/suche')).length).toBe(1)
    expect(document.querySelectorAll('.wv-ordner').length).toBe(2)
  })

  it('ein einzelnes Zeichen lässt eine gelesene Seite stehen (Verify V-4)', async () => {
    await WissenView.open('Wissen')
    ;[...document.querySelectorAll('.wv-eintrag')].find(b => b.textContent === 'Start').click()
    await warte()
    const input = document.querySelector('.wv-suche')
    input.value = 'a'
    input.dispatchEvent(new Event('input'))
    expect(document.querySelector('.wv-inhalt h1').textContent).toBe('Start')
  })

  it('W-2: Feld geleert, während die Suche noch läuft → späte Treffer erscheinen nicht', async () => {
    let loese
    await WissenView.open('Wissen')
    const orig = globalThis.fetch
    vi.stubGlobal('fetch', vi.fn((url) => url.startsWith('/wissen/suche')
      ? new Promise(r => { loese = () => r({ ok: true, status: 200, json: async () => ({ treffer: [{ pfad: 'README.md', titel: 'SPAET' }] }) }) })
      : orig(url)))
    vi.useFakeTimers()
    const input = document.querySelector('.wv-suche')
    input.value = 'st'
    input.dispatchEvent(new Event('input'))
    vi.advanceTimersByTime(300)
    vi.useRealTimers()
    input.value = ''
    input.dispatchEvent(new Event('input'))
    loese()
    await warte(); await warte()
    expect(document.querySelector('.wv-inhalt').textContent).not.toContain('SPAET')
    expect(document.querySelectorAll('.wv-ordner').length).toBe(2)
  })

  it('403 nennt fehlenden Zugriff', async () => {
    antworten['/wissen/index'] = { status: 403, body: { error: 'x' } }
    await WissenView.open('Wissen')
    expect(document.querySelector('.wv-hinweis').textContent).toMatch(/Zugriff|access/)
  })

  it('Suche erst ab 2 Zeichen, verzögert, Schnipsel als Text', async () => {
    antworten['/wissen/suche?q='] = { status: 200, body: { treffer: [
      { pfad: 'aboard/start.md', titel: 'Start', schnipsel: '<b>fett</b> im Text', zeile: 3 },
    ] } }
    await WissenView.open('Wissen')
    vi.useFakeTimers()
    const input = document.querySelector('.wv-suche')
    input.value = 'a'
    input.dispatchEvent(new Event('input'))
    vi.advanceTimersByTime(500)
    expect(aufrufe.some(u => u.startsWith('/wissen/suche'))).toBe(false)
    input.value = 'Start & mehr'
    input.dispatchEvent(new Event('input'))
    vi.advanceTimersByTime(299)
    expect(aufrufe.some(u => u.startsWith('/wissen/suche'))).toBe(false)
    vi.advanceTimersByTime(1)
    vi.useRealTimers()
    await warte()
    expect(aufrufe).toContain('/wissen/suche?q=Start%20%26%20mehr')
    const schn = document.querySelector('.wv-schnipsel')
    expect(schn.textContent).toBe('<b>fett</b> im Text')
    expect(schn.querySelector('b')).toBeNull()
  })

  it('Fehler: 429 mit eigenem Text', async () => {
    antworten['/wissen/suche?q='] = { status: 429, body: { error: 'rate' } }
    await WissenView.open('Wissen')
    vi.useFakeTimers()
    const input = document.querySelector('.wv-suche')
    input.value = 'xy'
    input.dispatchEvent(new Event('input'))
    vi.advanceTimersByTime(300)
    vi.useRealTimers()
    await warte()
    expect(document.querySelector('.wv-hinweis').textContent).toMatch(/Abrufe|requests/)
  })

  it('Index nicht erreichbar → Hinweis, kein Absturz', async () => {
    antworten['/wissen/index'] = { status: 503, body: { error: 'x' } }
    await WissenView.open('Wissen')
    expect(document.querySelector('.wv-hinweis').textContent).toMatch(/erreichbar|reachable/)
  })
})

describeServer('Seitenleiste: Eintrag Wissen', () => {
  let spy
  beforeEach(async () => {
    document.body.innerHTML = ''
    State.set({ board: { id: 'b1', title: 'Test-Board' } })
    const { Profile } = await import('../src/profile.js')
    spy = vi.spyOn(Profile, 'has').mockImplementation((cap) => cap === 'feature.restStorage')
  })
  afterEach(() => {
    Sidebar.close()
    spy.mockRestore()
  })

  it('/me nennt wissen → Eintrag mit diesem Namen, Klick öffnet die Lesesicht', async () => {
    currentUser = { name: 'Anna', email: 'anna@x.de', wissen: 'Team-Wissen' }
    const gehoert = vi.fn()
    EventBus.on('wissen-view:open', gehoert)
    await Sidebar.open()
    const row = [...document.querySelectorAll('.app-sidebar-row')].find(b => b.textContent.includes('Team-Wissen'))
    expect(row).toBeTruthy()
    row.click()
    expect(gehoert).toHaveBeenCalledWith('Team-Wissen')
  })

  it('/me ohne wissen → kein Eintrag', async () => {
    currentUser = { name: 'Anna', email: 'anna@x.de' }
    await Sidebar.open()
    const titel = [...document.querySelectorAll('.app-sidebar-section-title')].map(t => t.textContent)
    expect(titel).not.toContain('Wissen')
    expect(titel).not.toContain('Knowledge')
  })
})
