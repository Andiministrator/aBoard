/* @vitest-environment happy-dom */

/* Konto-Einstellungen (Karte #1083, ADR-0149 §3, §4): Dialog nur im
   Server-Build. Testdaten nur example.org. */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

let currentUser = { name: 'Anna', email: 'anna@example.org', anmeldung: 'oidc' }
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      getCurrentUser: async () => currentUser,
      listKnownBoards: async () => [],
    }),
  },
}))

import { KontoEinstellungen } from '../src/konto-einstellungen.js'
import { I18n } from '../src/i18n.js'

const STAND = {
  email: 'anna@example.org', konto: true, passwort: true, zweiter_faktor: true, codes_offen: 7,
  nachfrage: { passwort: true, code: true }, wege_aenderbar: true,
  wege: [
    { anbieter: 'google', label: 'Google', verknuepft: true, konfiguriert: true, loesbar: true },
    { anbieter: 'authelia', label: 'Firmen-Konto', verknuepft: false, konfiguriert: true, loesbar: false },
  ],
}

let routen = {}
let aufrufe = []
function stubFetch() {
  vi.stubGlobal('fetch', vi.fn(async (url, opt = {}) => {
    const methode = opt.method || 'GET'
    aufrufe.push({ url, methode, body: opt.body ? JSON.parse(opt.body) : null })
    const a = routen[methode + ' ' + url] ?? { status: 404, body: {} }
    const r = typeof a === 'function' ? a() : a
    return { ok: r.status >= 200 && r.status < 300, status: r.status, json: async () => r.body }
  }))
}
const warte = () => new Promise(r => setTimeout(r, 0))
const knopf = (text) => [...document.querySelectorAll('.ke-overlay button')].find(b => b.textContent === text || b.getAttribute('aria-label') === text)
const feld = (name) => document.querySelector(`.ke-overlay input[name="${name}"]`)

describeServer('KontoEinstellungen', () => {
  beforeEach(() => {
    I18n.setLang('de')
    document.body.innerHTML = ''
    aufrufe = []
    routen = { 'GET /users/me/konto': { status: 200, body: STAND } }
    stubFetch()
    vi.stubGlobal('confirm', vi.fn(() => true))
    window.confirm = vi.fn(() => true)
  })
  afterEach(() => {
    KontoEinstellungen.close()
    vi.unstubAllGlobals()
  })

  it('zeigt Wege, Bestätigung und Abschnitte', async () => {
    await KontoEinstellungen.open()
    const t = document.querySelector('.ke-overlay').textContent
    expect(t).toContain('anna@example.org')
    expect(t).toContain('Google')
    expect(t).toContain('Firmen-Konto')
    expect(feld('aktuell')).not.toBeNull()
    expect(feld('code')).not.toBeNull()
    expect(feld('neu').minLength).toBe(12)
    expect(feld('aktuell').autocomplete).toBe('current-password')
    expect(knopf('2FA neu einrichten')).toBeTruthy()
    expect(knopf('Andere Sitzungen beenden')).toBeTruthy()
    /* Felder haben Beschriftungen (label umschließt input) */
    for (const n of ['aktuell', 'code', 'neu', 'neu2']) expect(feld(n).closest('label')).not.toBeNull()
  })

  it('ohne Konto: keine Bestätigung, kein Passwort-Abschnitt', async () => {
    routen['GET /users/me/konto'] = { status: 200, body: { ...STAND, konto: false, passwort: false, zweiter_faktor: false, wege_aenderbar: false, nachfrage: { passwort: false, code: false } } }
    await KontoEinstellungen.open()
    expect(feld('aktuell')).toBeNull()
    expect(feld('neu')).toBeNull()
    expect(document.querySelector('.ke-overlay').textContent).toContain('Verwaltung')
  })

  it('ohne Passwort: Wege nur angezeigt, keine Knöpfe, Erklärung', async () => {
    routen['GET /users/me/konto'] = { status: 200, body: { ...STAND, passwort: false, zweiter_faktor: false, wege_aenderbar: false, nachfrage: { passwort: false, code: false },
      wege: [{ anbieter: 'google', label: 'Google', verknuepft: true, konfiguriert: true, loesbar: false }] } }
    await KontoEinstellungen.open()
    expect(knopf('Google lösen')).toBeUndefined()
    expect([...document.querySelectorAll('.ke-overlay button')].some(b => /verknüpfen/.test(b.textContent))).toBe(false)
    expect(document.querySelector('.ke-overlay').textContent).toContain('wende dich an die Verwaltung')
    expect(document.querySelector('.ke-overlay').textContent).toContain('Google')
  })

  it('409 frische_anmeldung_noetig wird erklärt (seit #1098, früher nur_mit_passwort)', async () => {
    routen['POST /users/me/konto/wege/loesen'] = { status: 409, body: { fehler: 'frische_anmeldung_noetig' } }
    await KontoEinstellungen.open()
    knopf('Google lösen').click()
    await warte(); await warte()
    expect(document.querySelector('.ke-overlay [role="alert"]').textContent).toContain('neu anmelden')
  })

  it('Passwort ändern schickt Bestätigung, leert den Code und meldet Erfolg', async () => {
    routen['POST /users/me/konto/passwort'] = { status: 200, body: { ok: true } }
    await KontoEinstellungen.open()
    feld('aktuell').value = 'altes-passwort-123'
    feld('code').value = '123456'
    feld('neu').value = 'ganz-neues-passwort-1'
    feld('neu2').value = 'ganz-neues-passwort-1'
    knopf('Passwort ändern').click()
    await warte(); await warte()
    const post = aufrufe.find(a => a.methode === 'POST')
    expect(post.body).toEqual({ aktuell: 'altes-passwort-123', code: '123456', neu: 'ganz-neues-passwort-1' })
    expect(document.querySelector('.ke-overlay [role="status"]').textContent).toContain('Passwort geändert')
    expect(feld('code').value).toBe('')
  })

  it('ungleiche Wiederholung sendet nichts', async () => {
    await KontoEinstellungen.open()
    feld('neu').value = 'ganz-neues-passwort-1'
    feld('neu2').value = 'anderes-passwort-1234'
    knopf('Passwort ändern').click()
    await warte()
    expect(aufrufe.some(a => a.methode === 'POST')).toBe(false)
    expect(document.querySelector('.ke-overlay [role="alert"]').textContent).toContain('nicht gleich')
  })

  it('falsche Bestätigung: Fehlertext als Alert, Code geleert', async () => {
    routen['POST /users/me/konto/codes'] = { status: 403, body: { fehler: 'falsch' } }
    await KontoEinstellungen.open()
    feld('code').value = '123456'
    knopf('Neue Wiederherstellungscodes erzeugen').click()
    await warte(); await warte()
    expect(document.querySelector('.ke-overlay [role="alert"]').textContent).toContain('stimmen nicht')
    expect(feld('code').value).toBe('')
  })

  it('401 heißt Sitzung abgelaufen', async () => {
    routen['POST /users/me/konto/sitzungen/beenden'] = { status: 401, body: { error: 'x' } }
    await KontoEinstellungen.open()
    knopf('Andere Sitzungen beenden').click()
    await warte(); await warte()
    expect(document.querySelector('.ke-overlay [role="alert"]').textContent).toContain('Sitzung ist abgelaufen')
  })

  it('andere Sitzungen beenden nennt die Zahl', async () => {
    routen['POST /users/me/konto/sitzungen/beenden'] = { status: 200, body: { beendet: 3 } }
    await KontoEinstellungen.open()
    knopf('Andere Sitzungen beenden').click()
    await warte(); await warte()
    expect(document.querySelector('.ke-overlay [role="status"]').textContent).toContain('3')
  })

  it('Wiederherstellungscodes erscheinen nach dem Erzeugen', async () => {
    routen['POST /users/me/konto/codes'] = { status: 200, body: { codes: ['aaaa-bbbb-cccc', 'dddd-eeee-ffff'] } }
    await KontoEinstellungen.open()
    knopf('Neue Wiederherstellungscodes erzeugen').click()
    await warte(); await warte(); await warte()
    expect([...document.querySelectorAll('.ke-codes li')].map(l => l.textContent)).toEqual(['aaaa-bbbb-cccc', 'dddd-eeee-ffff'])
    /* Abbruch der Rückfrage ändert nichts */
    window.confirm = vi.fn(() => false)
    const vorher = aufrufe.length
    knopf('Neue Wiederherstellungscodes erzeugen').click()
    await warte()
    expect(aufrufe.length).toBe(vorher)
  })

  it('2FA neu in zwei Schritten mit QR-Code', async () => {
    const qr = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 3 3"><path d="M0 0h1v1H0z"/></svg>'
    routen['POST /users/me/konto/2fa/start'] = { status: 200, body: { zwischen: 'Z', geheimnis: 'ABCD EFGH', link: 'otpauth://totp/x', qr } }
    routen['POST /users/me/konto/2fa/abschluss'] = { status: 200, body: { codes: ['aaaa-bbbb-cccc'] } }
    await KontoEinstellungen.open()
    feld('aktuell').value = 'pw'; feld('code').value = '111111'
    knopf('2FA neu einrichten').click()
    await warte(); await warte()
    expect(document.querySelector('.ke-qr svg')).not.toBeNull()
    expect(document.querySelector('.ke-qr svg').getAttribute('role')).toBe('img')
    expect(document.querySelector('.ke-schluessel').textContent).toBe('ABCD EFGH')
    expect(aufrufe.find(a => a.url.endsWith('/2fa/start')).body).toEqual({ aktuell: 'pw', code: '111111' })
    feld('code-neu').value = '654321'
    knopf('Neue App bestätigen').click()
    await warte(); await warte(); await warte()
    expect(aufrufe.find(a => a.url.endsWith('/2fa/abschluss')).body).toEqual({ zwischen: 'Z', code: '654321' })
    expect(document.querySelector('.ke-codes li').textContent).toBe('aaaa-bbbb-cccc')
    expect(document.querySelector('.ke-qr')).toBeNull()
  })

  it('QR-Code mit Skript wird nicht eingesetzt', () => {
    const q = KontoEinstellungen.__testing.qrKnoten
    expect(q('<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>')).toBeNull()
    expect(q('<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0" onload="x()"/></svg>')).toBeNull()
    expect(q('<div>kein svg</div>')).toBeNull()
    expect(q(null)).toBeNull()
  })

  it('Verknüpfen leitet nur zu https-Zielen weiter', async () => {
    const nav = vi.fn()
    KontoEinstellungen.__testing.setNavigiere(nav)
    routen['POST /users/me/konto/wege/verknuepfen'] = { status: 200, body: { ziel: 'https://accounts.example.org/auth?x=1' } }
    await KontoEinstellungen.open()
    feld('aktuell').value = 'pw'
    knopf('Firmen-Konto verknüpfen').click()
    await warte(); await warte()
    expect(nav).toHaveBeenCalledWith('https://accounts.example.org/auth?x=1')
    expect(aufrufe.find(a => a.url.endsWith('/wege/verknuepfen')).body).toMatchObject({ anbieter: 'authelia', aktuell: 'pw' })

    routen['POST /users/me/konto/wege/verknuepfen'] = { status: 200, body: { ziel: 'javascript:alert(1)' } }
    nav.mockClear()
    knopf('Firmen-Konto verknüpfen').click()
    await warte(); await warte()
    expect(nav).not.toHaveBeenCalled()
  })

  it('bereits verknüpft: Ersetzen fragt vorher', async () => {
    const nav = vi.fn()
    KontoEinstellungen.__testing.setNavigiere(nav)
    window.confirm = vi.fn(() => false)
    await KontoEinstellungen.open()
    knopf('Anderes Google-Konto verknüpfen').click()
    await warte()
    expect(window.confirm).toHaveBeenCalled()
    expect(aufrufe.some(a => a.methode === 'POST')).toBe(false)
  })

  it('Lösen: Rückfrage, dann Anfrage, 409 letzter Weg wird erklärt', async () => {
    routen['POST /users/me/konto/wege/loesen'] = { status: 409, body: { fehler: 'letzter_weg' } }
    await KontoEinstellungen.open()
    knopf('Google lösen').click()
    await warte(); await warte()
    expect(aufrufe.find(a => a.url.endsWith('/wege/loesen')).body).toMatchObject({ anbieter: 'google' })
    expect(document.querySelector('.ke-overlay [role="alert"]').textContent).toContain('einziger Anmeldeweg')
  })

  it('Rückkehr vom Anbieter öffnet den Dialog mit dem Ergebnis und bereinigt die Adresse', async () => {
    history.replaceState(null, '', '/?konto=belegt&x=1')
    expect(KontoEinstellungen.pruefeRueckkehr()).toBe(true)
    await warte(); await warte()
    expect(location.search).toBe('?x=1')
    expect(document.querySelector('.ke-overlay [role="alert"]').textContent).toContain('anderen Person')
    history.replaceState(null, '', '/')
    expect(KontoEinstellungen.pruefeRueckkehr()).toBe(false)
    history.replaceState(null, '', '/?konto=unsinn')
    expect(KontoEinstellungen.pruefeRueckkehr()).toBe(false)
    history.replaceState(null, '', '/')
  })

  it('Ladefehler zeigt einen Hinweis', async () => {
    routen['GET /users/me/konto'] = { status: 500, body: {} }
    await KontoEinstellungen.open()
    expect(document.querySelector('.ke-overlay').textContent).toContain('nicht erreichbar')
  })
})
