/* @vitest-environment happy-dom */

/* Konto-Einstellungen ohne Passwort (Karte #1098, F-2918) und Passwort
   nachträglich anlegen (Karte #1099, F-2919), ADR-0149 Nachtrag 2026-11-04.
   Nur Server-Build. Testdaten nur example.org. */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { describeServer } from './helpers/build-flags.js'

vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: {
    getActivePlugin: () => ({
      getCurrentUser: async () => ({ name: 'Pia', email: 'pm@example.org', anmeldung: 'oidc' }),
      listKnownBoards: async () => [],
    }),
  },
}))

import { KontoEinstellungen } from '../src/konto-einstellungen.js'
import { I18n } from '../src/i18n.js'

const STAND = {
  email: 'pm@example.org', konto: false, passwort: false, zweiter_faktor: false, codes_offen: 0,
  nachfrage: { passwort: false, code: false }, wege_aenderbar: true,
  frisch_noetig: true, frisch_anbieter: [{ anbieter: 'google', label: 'Google' }], passwort_anlegen: false,
  wege: [
    { anbieter: 'google', label: 'Google', verknuepft: true, konfiguriert: true, loesbar: false },
    { anbieter: 'authelia', label: 'Firmen-Konto', verknuepft: true, konfiguriert: true, loesbar: true },
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
const warte = async () => { for (let i = 0; i < 4; i++) await new Promise(r => setTimeout(r, 0)) }
const knopf = (text) => [...document.querySelectorAll('.ke-overlay button')].find(b => b.textContent === text || b.getAttribute('aria-label') === text)
const text = () => document.querySelector('.ke-overlay').textContent

describeServer('KontoEinstellungen ohne Passwort (#1098, #1099)', () => {
  let nav
  beforeEach(() => {
    I18n.setLang('de')
    document.body.innerHTML = ''
    aufrufe = []
    routen = { 'GET /users/me/konto': { status: 200, body: STAND } }
    stubFetch()
    window.confirm = vi.fn(() => true)
    nav = vi.fn()
    KontoEinstellungen.__testing.setNavigiere(nav)
  })
  afterEach(() => {
    KontoEinstellungen.close()
    vi.unstubAllGlobals()
  })

  it('zeigt statt des Passwortfelds die Bestätigung durch neue Anmeldung', async () => {
    await KontoEinstellungen.open()
    expect(document.querySelector('.ke-overlay input[name="aktuell"]')).toBeNull()
    const titel = [...document.querySelectorAll('.ke-abschnitt > h3')].map(h => h.textContent)
    expect(titel).toContain('Bestätigung durch neue Anmeldung')
    /* #1108: Ohne Vorhaben steht noch kein Aufforderungs-Kasten da */
    expect(document.querySelector('.ke-frisch')).toBeNull()
    expect(knopf('Firmen-Konto lösen')).toBeTruthy()
  })

  it('Lösen ohne Nachweis: erst neu anmelden, dann Weiterleitung mit Aktion', async () => {
    routen['POST /users/me/konto/frisch/start'] = { status: 200, body: { ziel: 'https://accounts.example.org/auth?prompt=login' } }
    await KontoEinstellungen.open()
    knopf('Firmen-Konto lösen').click()
    await warte()
    /* Noch keine Änderung angefragt, stattdessen der Anmelde-Knopf mit Fokus */
    expect(aufrufe.some(a => a.url.endsWith('/wege/loesen'))).toBe(false)
    const k = knopf('Erst bei Google neu anmelden')
    expect(k).toBeTruthy()
    expect(document.activeElement).toBe(document.querySelector('.ke-frisch button'))
    expect(text()).toContain('Für „Firmen-Konto lösen“')
    /* #1108 (F-2941): Der Kasten steht in derselben Zeile wie der Lösen-Knopf */
    const kasten = document.querySelector('.ke-frisch')
    expect(kasten.closest('li')).toBe(knopf('Firmen-Konto lösen').closest('li'))
    expect(document.querySelectorAll('.ke-frisch').length).toBe(1)
    k.click()
    await warte()
    expect(aufrufe.find(a => a.url.endsWith('/frisch/start')).body).toEqual({ anbieter: 'google', aktion: 'loesen', ziel: 'authelia' })
    expect(nav).toHaveBeenCalledWith('https://accounts.example.org/auth?prompt=login')
  })

  it('kein https-Ziel: keine Weiterleitung', async () => {
    routen['POST /users/me/konto/frisch/start'] = { status: 200, body: { ziel: 'javascript:alert(1)' } }
    await KontoEinstellungen.open()
    knopf('Firmen-Konto lösen').click()
    await warte()
    knopf('Erst bei Google neu anmelden').click()
    await warte()
    expect(nav).not.toHaveBeenCalled()
  })

  it('Rückkehr bestätigt: Nachweis wird genannt und „Jetzt ausführen“ löst', async () => {
    routen['GET /users/me/konto'] = { status: 200, body: { ...STAND, frisch: { aktion: 'loesen', ziel: 'authelia', bis: 1893456000 } } }
    routen['POST /users/me/konto/wege/loesen'] = { status: 200, body: { ok: true } }
    history.replaceState(null, '', '/?konto=bestaetigt')
    expect(KontoEinstellungen.pruefeRueckkehr()).toBe(true)
    await warte()
    expect(location.search).toBe('')
    expect(document.querySelector('.ke-overlay [role="status"]').textContent).toContain('neu angemeldet')
    expect(text()).toContain('Bestätigt für „Firmen-Konto lösen“')
    knopf('Jetzt ausführen').click()
    await warte()
    expect(window.confirm).toHaveBeenCalled()
    /* Ohne Passwort: keine Passwortfelder im Körper */
    expect(aufrufe.find(a => a.url.endsWith('/wege/loesen')).body).toEqual({ anbieter: 'authelia' })
    history.replaceState(null, '', '/')
  })

  it('mit passendem Nachweis führt der Wegknopf direkt aus', async () => {
    routen['GET /users/me/konto'] = { status: 200, body: { ...STAND, frisch: { aktion: 'verknuepfen', ziel: 'google', bis: 1893456000 } } }
    routen['POST /users/me/konto/wege/verknuepfen'] = { status: 200, body: { ziel: 'https://accounts.example.org/auth?prompt=select_account' } }
    await KontoEinstellungen.open()
    knopf('Anderes Google-Konto verknüpfen').click()
    await warte()
    expect(aufrufe.find(a => a.url.endsWith('/wege/verknuepfen')).body).toEqual({ anbieter: 'google' })
    expect(nav).toHaveBeenCalled()
  })

  it('Rückkehr nicht bestätigt: Fehlermeldung als Alert', async () => {
    history.replaceState(null, '', '/?konto=nicht_bestaetigt')
    expect(KontoEinstellungen.pruefeRueckkehr()).toBe(true)
    await warte()
    expect(document.querySelector('.ke-overlay [role="alert"]').textContent).toContain('nicht anerkannt')
    history.replaceState(null, '', '/')
  })

  it('Rückkehr nicht unterstützt (#1101): eigener Grund, kein Rat zum Ab- und Anmelden', async () => {
    history.replaceState(null, '', '/?konto=nicht_unterstuetzt')
    expect(KontoEinstellungen.pruefeRueckkehr()).toBe(true)
    await warte()
    const t = document.querySelector('.ke-overlay [role="alert"]').textContent
    expect(t).toContain('bestätigt keine frische Anmeldung')
    expect(t).toContain('ABOARD_OIDC_REAUTH_FALLBACK')
    expect(t).not.toContain('accounts.google.com')
    history.replaceState(null, '', '/')
  })

  it('Rückkehr vorgang_alt (F-2945): eigener Text, kein Rat zum Ab- und Anmelden bei Google', async () => {
    history.replaceState(null, '', '/?konto=vorgang_alt')
    expect(KontoEinstellungen.pruefeRueckkehr()).toBe(true)
    await warte()
    const t = document.querySelector('.ke-overlay [role="alert"]').textContent
    expect(t).toContain('nicht zu diesem Vorgang')
    expect(t).toContain('neu')
    expect(t).not.toContain('accounts.google.com')
    history.replaceState(null, '', '/')
  })

  it('Passwort anlegen nur, wenn der Server es anbietet', async () => {
    await KontoEinstellungen.open()
    expect(knopf('Passwort mit zweitem Faktor anlegen')).toBeUndefined()
  })

  it('Passwort anlegen ohne Nachweis (#1108): Aufforderung direkt unter dem Knopf, hervorgehoben, mit Fokus', async () => {
    routen['GET /users/me/konto'] = { status: 200, body: { ...STAND, passwort_anlegen: true } }
    await KontoEinstellungen.open()
    knopf('Passwort mit zweitem Faktor anlegen').click()
    await warte()
    /* Nach dem Klick ist neu gezeichnet: den Knopf frisch holen */
    const anlegen = knopf('Passwort mit zweitem Faktor anlegen')
    const kasten = document.querySelector('.ke-frisch')
    expect(kasten).toBeTruthy()
    expect(kasten.closest('.ke-abschnitt')).toBe(anlegen.closest('.ke-abschnitt'))
    expect(anlegen.compareDocumentPosition(kasten) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(document.activeElement).toBe(kasten.querySelector('button'))
    expect(kasten.textContent).toContain('Erst bei Google neu anmelden')
  })

  it('Passwort anlegen: Nachweis, Start, Abschluss, Codes', async () => {
    routen['GET /users/me/konto'] = { status: 200, body: { ...STAND, passwort_anlegen: true, frisch: { aktion: 'passwort', bis: 1893456000 } } }
    routen['POST /users/me/konto/passwort/anlegen/start'] = { status: 200, body: { zwischen: 'z1', geheimnis: 'ABCD EFGH', link: 'otpauth://totp/x', qr: '<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0h1v1H0z"/></svg>' } }
    routen['POST /users/me/konto/passwort/anlegen/abschluss'] = () => {
      routen['GET /users/me/konto'] = { status: 200, body: { ...STAND, konto: true, passwort: true, zweiter_faktor: true, codes_offen: 10, frisch_noetig: false, passwort_anlegen: false, nachfrage: { passwort: true, code: true } } }
      return { status: 200, body: { codes: ['aaaa-bbbb-cccc', 'dddd-eeee-ffff'] } }
    }
    await KontoEinstellungen.open()
    knopf('Passwort mit zweitem Faktor anlegen').click()
    await warte()
    expect(text()).toContain('ABCD EFGH')
    expect(document.querySelector('.ke-anlegen svg').getAttribute('aria-label')).toBe('QR-Code für die Authenticator-App')
    const f = (n) => document.querySelector(`.ke-overlay input[name="${n}"]`)
    for (const n of ['anlegen-neu', 'anlegen-neu2', 'anlegen-code']) expect(f(n).closest('label')).not.toBeNull()
    f('anlegen-neu').value = 'ganz-neues-passwort-1'
    f('anlegen-neu2').value = 'anderes-passwort-2'
    knopf('Passwort anlegen').click()
    await warte()
    expect(aufrufe.some(a => a.url.endsWith('/abschluss'))).toBe(false)
    expect(document.querySelector('.ke-overlay [role="alert"]').textContent).toContain('nicht gleich')
    f('anlegen-neu').value = 'ganz-neues-passwort-1'
    f('anlegen-neu2').value = 'ganz-neues-passwort-1'
    f('anlegen-code').value = '123456'
    knopf('Passwort anlegen').click()
    await warte()
    expect(aufrufe.find(a => a.url.endsWith('/abschluss')).body).toEqual({ zwischen: 'z1', neu: 'ganz-neues-passwort-1', code: '123456' })
    expect(text()).toContain('aaaa-bbbb-cccc')
    expect(text()).toContain('Passwort und zweiter Faktor sind angelegt')
  })

  it('Passwort anlegen ohne Nachweis führt zur neuen Anmeldung mit Aktion passwort', async () => {
    routen['GET /users/me/konto'] = { status: 200, body: { ...STAND, passwort_anlegen: true } }
    routen['POST /users/me/konto/frisch/start'] = { status: 200, body: { ziel: 'https://accounts.example.org/auth' } }
    await KontoEinstellungen.open()
    knopf('Passwort mit zweitem Faktor anlegen').click()
    await warte()
    expect(aufrufe.some(a => a.url.endsWith('/passwort/anlegen/start'))).toBe(false)
    knopf('Erst bei Google neu anmelden').click()
    await warte()
    expect(aufrufe.find(a => a.url.endsWith('/frisch/start')).body).toEqual({ anbieter: 'google', aktion: 'passwort', ziel: '' })
  })

  it('nicht bestätigt nennt den Weg über Abmelden beim Anbieter', async () => {
    history.replaceState(null, '', '/?konto=nicht_bestaetigt')
    KontoEinstellungen.pruefeRueckkehr()
    await warte()
    expect(document.querySelector('.ke-overlay [role="alert"]').textContent).toContain('melde dich dort erst ab')
    history.replaceState(null, '', '/')
  })

  it('Codes erscheinen auch, wenn die Sitzung danach fehlt (neu_anmelden)', async () => {
    routen['GET /users/me/konto'] = { status: 200, body: { ...STAND, passwort_anlegen: true, frisch: { aktion: 'passwort', bis: 1893456000 } } }
    routen['POST /users/me/konto/passwort/anlegen/start'] = { status: 200, body: { zwischen: 'z1', geheimnis: 'ABCD', link: 'otpauth://totp/x', qr: '' } }
    routen['POST /users/me/konto/passwort/anlegen/abschluss'] = () => {
      routen['GET /users/me/konto'] = { status: 401, body: {} }
      return { status: 200, body: { codes: ['aaaa-bbbb-cccc'], neu_anmelden: true } }
    }
    await KontoEinstellungen.open()
    knopf('Passwort mit zweitem Faktor anlegen').click()
    await warte()
    const f = (n) => document.querySelector(`.ke-overlay input[name="${n}"]`)
    f('anlegen-neu').value = 'ganz-neues-passwort-1'
    f('anlegen-neu2').value = 'ganz-neues-passwort-1'
    f('anlegen-code').value = '123456'
    knopf('Passwort anlegen').click()
    await warte()
    expect(text()).toContain('aaaa-bbbb-cccc')
    expect(document.querySelector('.ke-overlay [role="alert"]').textContent).toContain('neu an')
  })

  it('englische Texte', async () => {
    I18n.setLang('en')
    await KontoEinstellungen.open()
    knopf('Unlink Firmen-Konto').click()
    await warte()
    expect(knopf('Sign in again at Google first')).toBeTruthy()
  })
})
