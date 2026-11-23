/* @vitest-environment happy-dom */

/**
 * Karte #1078 (ADR-0149 §6): Reiter „Benutzer“ um Konten und Einladungen
 * erweitert — Zustand je Person, Einladen mit Link-Anzeige (einmal), wartende
 * Registrierungen mit gemeldeter Adresse, Bestätigen/Ablehnen/Sperren/
 * Entsperren/Zurücksetzen. Server-only (`describeServer`).
 */

import { it, expect, beforeEach, afterEach, vi } from 'vitest';
import { describeServer } from './helpers/build-flags.js';

vi.mock('../src/swipe.js', () => ({ addSwipeToDismiss: vi.fn() }));

let kontenAntwort;
let einladenAntwort;
let aktionAntwort;
const listKonten = vi.fn(async () => kontenAntwort);
const einladen = vi.fn(async () => einladenAntwort);
const widerrufe = vi.fn(async () => ({ ok: true, status: 204, body: null }));
const kontoAktion = vi.fn(async () => aktionAntwort);
const deleteUser = vi.fn(async () => ({ ok: true, status: 200, body: { pseudonym: 'Geloescht-1' } }));
const listUsers = vi.fn(async () => ({ ok: true, status: 200, body: { users: [
  { id: 'u1', email: 'ich@example.org', name: 'Ich', is_admin: true, boards_owner: 1, boards_einzel_owner: 0 },
  { id: 'u2', email: 'bea@example.org', name: 'Bea', is_admin: false, boards_owner: 0, boards_einzel_owner: 0 }] } }));

vi.mock('../src/admin-client.js', () => ({
  AdminClient: {
    status: vi.fn(async () => ({ ok: true, body: {} })),
    audit: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listConfig: vi.fn(async () => ({ ok: true, body: { entries: [] } })),
    listUsers: (...a) => listUsers(...a),
    listKonten: (...a) => listKonten(...a),
    einladen: (...a) => einladen(...a),
    widerrufeEinladung: (...a) => widerrufe(...a),
    kontoAktion: (...a) => kontoAktion(...a),
    deleteUser: (...a) => deleteUser(...a),
  },
}));
vi.mock('../src/storage-plugin.js', () => ({
  StoragePlugin: { getActivePlugin: () => ({ getCurrentUser: async () => ({ email: 'ich@example.org' }) }) },
}));

import { AdminDialog } from '../src/admin-dialog.js';
import { I18n } from '../src/i18n.js';

const ruhe = () => new Promise(r => setTimeout(r, 0));
async function ruhig() { for (let i = 0; i < 8; i++) await ruhe(); }
async function oeffneBenutzer() {
  AdminDialog.open();
  await ruhig();
  document.querySelector('.admin-tab[data-section="users"]').click();
  await ruhig();
}
const zeile = (id) => document.querySelector(`.admin-konten-table tr[data-konto="${id}"]`);
const knopf = (id, aktion) => document.querySelector(`.admin-konten-table button[data-konto="${id}"][data-aktion="${aktion}"]`);
const flash = () => document.querySelector('.admin-konten-flash');

function konto(extra) {
  return { id: 'k', email: 'x@example.org', zustand: 'active', weg: 'password', gemeldete_email: '', gemeldeter_name: '',
    angelegt: '2026-11-01T10:00:00Z', geaendert: '2026-11-01T10:00:00Z', link_abgelaufen: false,
    hat_passwort: true, hat_zwei_faktor: false, ...extra };
}

beforeEach(() => {
  I18n.setLang('de');
  vi.stubGlobal('confirm', vi.fn(() => true));
  vi.spyOn(Element.prototype, 'scrollIntoView').mockImplementation(() => {});
  kontenAntwort = { ok: true, status: 200, body: { konten: [
    konto({ id: 'k1', email: 'eing@example.org', zustand: 'invited', weg: '', link_ablauf: '2026-11-10T10:00:00Z' }),
    konto({ id: 'k2', email: 'alt@example.org', zustand: 'invited', weg: '', link_abgelaufen: true }),
    konto({ id: 'k3', email: 'wart@example.org', zustand: 'pending', weg: 'google',
      gemeldete_email: 'andere@example.org', gemeldeter_name: 'Wart Name' }),
    konto({ id: 'k4', email: 'gleich@example.org', zustand: 'pending', weg: 'google',
      gemeldete_email: 'GLEICH@example.org', gemeldeter_name: 'Gleich' }),
    konto({ id: 'k5', email: 'aktiv@example.org', zustand: 'active', weg: 'standard' }),
    konto({ id: 'k6', email: 'gesp@example.org', zustand: 'blocked', weg: 'password', hat_zwei_faktor: true }),
    konto({ id: 'k7', email: 'ich@example.org', zustand: 'active', weg: 'password' }),
    konto({ id: 'k8', email: 'zur@example.org', zustand: 'invited', weg: '', link_abgelaufen: true, hat_zugang: true, neuer_link: true, widerrufbar: false }),
  ] } };
  einladenAntwort = { ok: true, status: 200, body: { ergebnisse: [] } };
  aktionAntwort = { ok: true, status: 200, body: { id: 'k3', zustand: 'active' } };
  [listKonten, einladen, widerrufe, kontoAktion, listUsers, deleteUser].forEach(f => f.mockClear());
});
afterEach(() => { AdminDialog?.close(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describeServer('#1078 Reiter Benutzer: Konten und Einladungen', () => {
  it('zeigt Zustände, Anmeldeweg, Linkablauf; die Nutzerliste bleibt daneben', async () => {
    await oeffneBenutzer();
    expect(zeile('k1').textContent).toContain('Eingeladen');
    expect(zeile('k1').textContent).toContain('gültig bis 2026-11-10 10:00');
    expect(zeile('k2').textContent).toContain('abgelaufen');
    expect(zeile('k3').textContent).toContain('Wartet auf Bestätigung');
    expect(zeile('k6').textContent).toContain('Gesperrt');
    expect(zeile('k6').textContent).toContain('Passwort + zweiter Faktor');
    expect(zeile('k3').textContent).toContain('google');
    /* Altbeschriftung `standard` nie als eingerichteter Weg */
    expect(zeile('k5').textContent).toContain('bisheriger Anbieter');
    expect(zeile('k5').textContent).not.toMatch(/standard/);
    /* '' → Strich */
    expect(zeile('k1').children[2].textContent).toBe('—');
    /* vorhandene Nutzerliste intakt */
    expect(document.querySelector('.admin-body table.admin-users-table:not(.admin-konten-table)').textContent).toContain('bea@example.org');
    expect(document.querySelectorAll('.admin-tab[data-section="users"]').length).toBe(1);
  });

  it('Knöpfe je Zustand; eigenes Konto ohne Sperren/Zurücksetzen', async () => {
    await oeffneBenutzer();
    expect(knopf('k1', 'widerrufen')).toBeTruthy();
    /* invited: kein Zurücksetzen (Server: 409), nur Widerrufen */
    expect(knopf('k1', 'zuruecksetzen')).toBeNull();
    expect(knopf('k2', 'zuruecksetzen')).toBeNull();
    expect(knopf('k3', 'bestaetigen')).toBeTruthy();
    expect(knopf('k3', 'ablehnen')).toBeTruthy();
    expect(knopf('k3', 'sperren')).toBeTruthy();
    expect(knopf('k3', 'zuruecksetzen')).toBeTruthy();
    expect(knopf('k5', 'sperren')).toBeTruthy();
    expect(knopf('k5', 'zuruecksetzen')).toBeTruthy();
    expect(knopf('k6', 'entsperren')).toBeTruthy();
    expect(knopf('k6', 'zuruecksetzen')).toBeTruthy();
    expect(knopf('k6', 'sperren')).toBeNull();
    expect(knopf('k7', 'sperren')).toBeNull();
    expect(knopf('k7', 'zuruecksetzen')).toBeNull();
    expect(zeile('k7').textContent).toContain('dein eigenes Konto');
  });

  it('hebt Abweichung zwischen eingeladener und gemeldeter Adresse hervor', async () => {
    await oeffneBenutzer();
    const abw = zeile('k3').querySelector('.admin-konten-gemeldet');
    expect(abw.classList.contains('admin-konten-gemeldet--abweichend')).toBe(true);
    expect(abw.textContent).toContain('andere@example.org');
    expect(abw.textContent).toContain('Wart Name');
    expect(abw.textContent).toContain('weicht von der eingeladenen Adresse ab');
    /* gleiche Adresse (Groß-/Kleinschreibung egal): keine Hervorhebung */
    const gleich = zeile('k4').querySelector('.admin-konten-gemeldet');
    expect(gleich.classList.contains('admin-konten-gemeldet--abweichend')).toBe(false);
    expect(gleich.textContent).not.toContain('weicht');
  });

  it('F-2961: unbestätigte Adresse zeigt Flag-Grund, Adresse bleibt rein', async () => {
    kontenAntwort.body.konten[3].gemeldete_email_unbestaetigt = true; /* k4: gleiche Adresse wie eingeladen */
    await oeffneBenutzer();
    const g = zeile('k4').querySelector('.admin-konten-gemeldet');
    expect(g.classList.contains('admin-konten-gemeldet--abweichend')).toBe(true);
    expect(g.textContent).toContain('Adresse vom Anbieter nicht bestätigt');
    expect(g.textContent).not.toContain('(unbestätigt)');
    expect(g.textContent).not.toContain('weicht von der eingeladenen Adresse ab');
  });

  it('F-2979: abweichende und unbestätigte Adresse zeigt beide Hinweise', async () => {
    kontenAntwort.body.konten[2].gemeldete_email_unbestaetigt = true; /* k3: andere Adresse als eingeladen */
    await oeffneBenutzer();
    const g = zeile('k3').querySelector('.admin-konten-gemeldet');
    expect(g.classList.contains('admin-konten-gemeldet--abweichend')).toBe(true);
    expect(g.textContent).toContain('andere@example.org');
    expect(g.textContent).toContain('weicht von der eingeladenen Adresse ab');
    expect(g.textContent).toContain('Adresse vom Anbieter nicht bestätigt');
    expect(g.textContent).not.toContain('(unbestätigt)');
  });

  it('gemeldeter Name mit HTML erscheint nur als Text', async () => {
    kontenAntwort.body.konten[2].gemeldeter_name = '<img src=x onerror=alert(1)>';
    kontenAntwort.body.konten[2].gemeldete_email = '<b>x</b>@example.org';
    await oeffneBenutzer();
    const g = zeile('k3');
    expect(g.querySelector('img')).toBeNull();
    expect(g.querySelector('b')).toBeNull();
    expect(g.textContent).toContain('<img src=x onerror=alert(1)>');
  });

  it('Einladen zeigt den Link einmal zum Kopieren, Mail-Status und Fehler je Adresse', async () => {
    einladenAntwort = { ok: true, status: 200, body: { ergebnisse: [
      { email: 'neu@example.org', konto_id: 'kn', link: 'https://x.example.org/einladung/TOKEN1', ablauf: '2026-11-10T10:00:00Z', mail: 'nicht_eingerichtet' },
      { email: 'kaputt', fehler: 'adresse' },
      { email: 'da@example.org', fehler: 'hat_zugang' },
    ] } };
    const schreibe = vi.fn(async () => {});
    vi.stubGlobal('navigator', { clipboard: { writeText: schreibe } });
    await oeffneBenutzer();
    const area = document.querySelector('#admin-konten-mails');
    area.value = 'neu@example.org, kaputt\nda@example.org';
    [...document.querySelectorAll('.admin-konten-form button')].find(b => b.textContent === 'Einladen').click();
    await ruhig();
    expect(einladen).toHaveBeenCalledWith(['neu@example.org', 'kaputt', 'da@example.org']);
    const feld = document.querySelector('.admin-konten-links input[readonly]');
    expect(feld.value).toBe('https://x.example.org/einladung/TOKEN1');
    const box = document.querySelector('.admin-konten-links');
    expect(box.textContent).toContain('Mailversand nicht eingerichtet');
    expect(box.textContent).toContain('kaputt: keine gültige E-Mail-Adresse');
    expect(box.textContent).toContain('da@example.org: hat schon Zugang');
    expect(box.getAttribute('role')).toBe('status');
    /* Nur die Fehleradressen bleiben im Feld */
    expect(area.value).toBe('kaputt\nda@example.org');
    /* Liste wurde neu geladen */
    expect(listKonten.mock.calls.length).toBeGreaterThanOrEqual(2);
    box.querySelector('.admin-konten-link button').click();
    await ruhig();
    expect(schreibe).toHaveBeenCalledWith('https://x.example.org/einladung/TOKEN1');
    /* Ausblenden entfernt die Links aus dem DOM */
    [...box.querySelectorAll('button')].find(b => b.textContent === 'Links ausblenden').click();
    expect(document.querySelector('.admin-konten-links')).toBeNull();
    expect([...document.querySelectorAll('input')].map(i => i.value).join(' ')).not.toContain('TOKEN1');
  });

  it('Schließen des Dialogs nimmt die Links aus dem DOM', async () => {
    einladenAntwort = { ok: true, status: 200, body: { ergebnisse: [
      { email: 'neu@example.org', link: 'https://x.example.org/einladung/TOKEN2', mail: 'gesendet' }] } };
    await oeffneBenutzer();
    document.querySelector('#admin-konten-mails').value = 'neu@example.org';
    [...document.querySelectorAll('.admin-konten-form button')].find(b => b.textContent === 'Einladen').click();
    await ruhig();
    const werte = () => [...document.querySelectorAll('input')].map(i => i.value).join(' ');
    expect(werte()).toContain('TOKEN2');
    AdminDialog.close();
    expect(werte()).not.toContain('TOKEN2');
    expect(document.querySelector('.admin-konten-links')).toBeNull();
  });

  /* Sitzungs-Kritik 04.11. (C): Folgefehler im Zusammenspiel von Konten und Nutzerliste */
  const einladeLink = (token) => {
    einladenAntwort = { ok: true, status: 200, body: { ergebnisse: [
      { email: 'neu@example.org', link: 'https://x.example.org/einladung/' + token, mail: 'gesendet' }] } };
  };
  const einladenKlick = async () => {
    document.querySelector('#admin-konten-mails').value = 'neu@example.org';
    [...document.querySelectorAll('.admin-konten-form button')].find(b => b.textContent === 'Einladen').click();
    await ruhig();
  };
  const werte = () => [...document.querySelectorAll('input')].map(i => i.value).join(' ');

  it('C1: nach Bestätigen wird auch die Nutzerliste neu geladen', async () => {
    await oeffneBenutzer();
    const vorher = listUsers.mock.calls.length;
    knopf('k3', 'bestaetigen').click();
    await ruhig();
    expect(listUsers.mock.calls.length).toBe(vorher + 1);
    expect(document.querySelectorAll('table.admin-users-table:not(.admin-konten-table)').length).toBe(1);
  });

  it('C2: Nutzer löschen zeichnet neu, der eingeblendete Einladungslink bleibt', async () => {
    einladeLink('TOKEN3');
    await oeffneBenutzer();
    await einladenKlick();
    expect(werte()).toContain('TOKEN3');
    const loeschen = [...document.querySelectorAll('table.admin-users-table:not(.admin-konten-table) button')]
      .find(b => b.textContent === I18n.t('admin.users.btn-delete'));
    loeschen.click();
    const eingabe = document.querySelector('.admin-users-confirm input');
    eingabe.value = 'bea@example.org';
    eingabe.dispatchEvent(new Event('input'));
    document.querySelector('.admin-users-confirm .btn-primary').click();
    await ruhig();
    expect(deleteUser).toHaveBeenCalled();
    expect(werte()).toContain('TOKEN3');
    /* und das Ausblenden funktioniert nach dem Neuzeichnen weiterhin */
    [...document.querySelectorAll('.admin-konten-links button')].find(b => b.textContent === 'Links ausblenden').click();
    expect(werte()).not.toContain('TOKEN3');
  });

  it('C3: Reiterwechsel während Einladen — die Antwort landet nicht im neuen Reiter', async () => {
    einladeLink('TOKEN4');
    let fertig;
    einladen.mockImplementationOnce(() => new Promise(r => { fertig = () => r(einladenAntwort); }));
    await oeffneBenutzer();
    document.querySelector('#admin-konten-mails').value = 'neu@example.org';
    [...document.querySelectorAll('.admin-konten-form button')].find(b => b.textContent === 'Einladen').click();
    await ruhig();
    document.querySelector('.admin-tab[data-section="status"]').click();
    await ruhig();
    fertig();
    await ruhig();
    expect(werte()).not.toContain('TOKEN4');
    expect(document.querySelector('.admin-konten-links')).toBeNull();
    /* Konten-Liste wurde für den abgehängten Teil nicht noch einmal geladen */
    expect(listKonten.mock.calls.length).toBe(1);
  });

  it('leere Eingabe wird abgefangen, ohne den Server zu fragen', async () => {
    await oeffneBenutzer();
    [...document.querySelectorAll('.admin-konten-form button')].find(b => b.textContent === 'Einladen').click();
    await ruhig();
    expect(einladen).not.toHaveBeenCalled();
    expect(flash().textContent).toContain('mindestens eine E-Mail-Adresse');
    expect(flash().getAttribute('role')).toBe('alert');
  });

  it('Bestätigen ohne Rückfrage; danach Meldung und neu geladene Liste', async () => {
    await oeffneBenutzer();
    knopf('k3', 'bestaetigen').click();
    await ruhig();
    expect(kontoAktion).toHaveBeenCalledWith('k3', 'bestaetigen');
    expect(flash().textContent).toContain('wart@example.org bestätigt');
    expect(listKonten.mock.calls.length).toBe(2);
  });

  it('gefährliche Aktionen verlangen das Panel (kein confirm), Abbrechen ruft nichts auf', async () => {
    await oeffneBenutzer();
    knopf('k3', 'ablehnen').click();
    const panel = zeile('k3').querySelector('.admin-konten-confirm');
    expect(panel).toBeTruthy();
    expect(kontoAktion).not.toHaveBeenCalled();
    expect(confirm).not.toHaveBeenCalled();
    [...panel.querySelectorAll('button')].find(b => b.textContent === 'Abbrechen').click();
    expect(zeile('k3').querySelector('.admin-konten-confirm')).toBeNull();
    expect(kontoAktion).not.toHaveBeenCalled();
    expect(knopf('k3', 'ablehnen').disabled).toBe(false);
    /* nochmal, diesmal bestätigen */
    aktionAntwort = { ok: true, status: 200, body: { id: 'k3', zustand: '' } };
    knopf('k3', 'ablehnen').click();
    [...zeile('k3').querySelectorAll('.admin-konten-confirm button')].find(b => b.textContent === 'Ablehnen').click();
    await ruhig();
    expect(kontoAktion).toHaveBeenCalledWith('k3', 'ablehnen');
    expect(flash().textContent).toContain('abgelehnt');
  });

  it('Widerrufen: Panel, dann DELETE; 409 „schon eingelöst“ wird verständlich gemeldet', async () => {
    widerrufe.mockImplementationOnce(async () => ({ ok: false, status: 409, body: { error: 'x' } }));
    await oeffneBenutzer();
    knopf('k1', 'widerrufen').click();
    [...zeile('k1').querySelectorAll('.admin-konten-confirm button')].find(b => b.textContent === 'Einladung widerrufen').click();
    await ruhig();
    expect(widerrufe).toHaveBeenCalledWith('k1');
    expect(flash().textContent).toContain('schon eingelöst');
    expect(flash().getAttribute('role')).toBe('alert');
    expect(listKonten.mock.calls.length).toBe(2);
  });

  it('409 bei Ablehnen nennt, dass die Person schon Zugang hat', async () => {
    aktionAntwort = { ok: false, status: 409, body: { error: 'x' } };
    await oeffneBenutzer();
    knopf('k3', 'ablehnen').click();
    [...zeile('k3').querySelectorAll('.admin-konten-confirm button')].find(b => b.textContent === 'Ablehnen').click();
    await ruhig();
    expect(flash().textContent).toContain('schon Zugang');
  });

  it('503 heißt „gleich nochmal versuchen“, Liste und Panel bleiben, erneuter Versuch möglich', async () => {
    aktionAntwort = { ok: false, status: 503, body: { error: 'busy' } };
    await oeffneBenutzer();
    knopf('k5', 'sperren').click();
    const ok = [...zeile('k5').querySelectorAll('.admin-konten-confirm button')].find(b => b.textContent === 'Sperren');
    ok.click();
    await ruhig();
    expect(flash().textContent).toContain('gleich nochmal versuchen');
    expect(listKonten.mock.calls.length).toBe(1);
    expect(ok.disabled).toBe(false);
  });

  it('Entsperren führt zu „wartet“ und bietet danach Bestätigen an (Fokus darauf)', async () => {
    aktionAntwort = { ok: true, status: 200, body: { id: 'k6', zustand: 'pending' } };
    await oeffneBenutzer();
    kontenAntwort = { ok: true, status: 200, body: { konten: kontenAntwort.body.konten.map(k => k.id === 'k6' ? { ...k, zustand: 'pending' } : k) } };
    knopf('k6', 'entsperren').click();
    await ruhig();
    expect(kontoAktion).toHaveBeenCalledWith('k6', 'entsperren');
    expect(flash().textContent).toContain('Wartet auf Bestätigung');
    expect(flash().textContent).toContain('„Bestätigen“ drücken');
    const b = knopf('k6', 'bestaetigen');
    expect(b).toBeTruthy();
    expect(document.activeElement).toBe(b);
  });

  it('F-2921: zurückgesetztes Konto (neuer_link) zeigt „Neuer Link“ statt „Widerrufen“', async () => {
    aktionAntwort = { ok: true, status: 200, body: { id: 'k8', zustand: 'invited', link: 'https://x.example.org/einladung/NEU2', ablauf: '2026-11-12T10:00:00Z', mail: 'nicht_eingerichtet' } };
    await oeffneBenutzer();
    expect(knopf('k8', 'widerrufen')).toBeNull();
    expect(knopf('k8', 'zuruecksetzen')).toBeNull();
    const b = knopf('k8', 'neuerlink');
    expect(b.textContent).toBe('Neuer Link');
    b.click();
    /* F-2926: mit Rückfrage, erst der Bestätigungsknopf ruft den Server */
    expect(kontoAktion).not.toHaveBeenCalled();
    [...zeile('k8').querySelectorAll('.admin-konten-confirm button')].find(x => x.textContent === 'Neuer Link').click();
    await ruhig();
    expect(kontoAktion).toHaveBeenCalledWith('k8', 'zuruecksetzen');
    expect(document.querySelector('.admin-konten-links input[readonly]').value).toBe('https://x.example.org/einladung/NEU2');
  });

  it('F-2926/F-2930: invited mit Nutzerzeile, ohne Rücksetz-Link: „Widerrufen“ und „Neuer Link“', async () => {
    kontenAntwort.body.konten.push(konto({ id: 'k9', email: 'gelistet@example.org', zustand: 'invited', weg: '', hat_zugang: true, neuer_link: true, widerrufbar: true }));
    await oeffneBenutzer();
    expect(knopf('k9', 'widerrufen')).toBeTruthy();
    expect(knopf('k9', 'neuerlink')).toBeTruthy();
  });

  it('F-2931: Rücksetz-Link ohne Nutzerzeile: beide Knöpfe; mit Nutzerzeile (widerrufbar false) nur „Neuer Link“', async () => {
    kontenAntwort.body.konten.push(konto({ id: 'k10', email: 'ohne@example.org', zustand: 'invited', weg: '', hat_zugang: false, neuer_link: true, widerrufbar: true }));
    await oeffneBenutzer();
    expect(knopf('k10', 'widerrufen')).toBeTruthy();
    expect(knopf('k10', 'neuerlink')).toBeTruthy();
    expect(knopf('k8', 'widerrufen')).toBeNull();
    expect(knopf('k8', 'neuerlink')).toBeTruthy();
    expect(knopf('k1', 'neuerlink')).toBeNull();
    expect(knopf('k1', 'widerrufen')).toBeTruthy();
  });

  it('F-2932: Rückfrage zu „Widerrufen“ nennt die Folge, wenn die Person schon Zugang hatte', async () => {
    kontenAntwort.body.konten.push(konto({ id: 'k11', email: 'zugang@example.org', zustand: 'invited', weg: '', hat_zugang: true, neuer_link: true, widerrufbar: true }));
    await oeffneBenutzer();
    knopf('k11', 'widerrufen').click();
    expect(zeile('k11').querySelector('.admin-konten-confirm').textContent).toContain('hatte schon Zugang');
    knopf('k1', 'widerrufen').click();
    expect(zeile('k1').querySelector('.admin-konten-confirm').textContent).not.toContain('hatte schon Zugang');
  });

  it('F-2922: Bestätigen mit 409 nennt den Bindungs-Konflikt', async () => {
    aktionAntwort = { ok: false, status: 409, body: { error: 'x' } };
    await oeffneBenutzer();
    knopf('k3', 'bestaetigen').click();
    await ruhig();
    expect(flash().textContent).toContain('schon eine Bindung');
  });

  it('Zurücksetzen zeigt den neuen Link genau einmal an', async () => {
    aktionAntwort = { ok: true, status: 200, body: { id: 'k5', zustand: 'invited', link: 'https://x.example.org/einladung/NEU', ablauf: '2026-11-12T10:00:00Z', mail: 'fehlgeschlagen' } };
    await oeffneBenutzer();
    knopf('k5', 'zuruecksetzen').click();
    [...zeile('k5').querySelectorAll('.admin-konten-confirm button')].find(b => b.textContent === 'Zurücksetzen').click();
    await ruhig();
    expect(document.querySelector('.admin-konten-links input[readonly]').value).toBe('https://x.example.org/einladung/NEU');
    expect(document.querySelector('.admin-konten-links').textContent).toContain('Mail konnte nicht verschickt werden');
  });

  it('404 auf /admin/konten blendet den Konten-Teil aus, die Nutzerliste bleibt', async () => {
    kontenAntwort = { ok: false, status: 404, body: { error: 'x' } };
    await oeffneBenutzer();
    expect(document.querySelector('.admin-konten')).toBeNull();
    expect(document.querySelector('.admin-users-table').textContent).toContain('bea@example.org');
  });

  it('Englisch: Texte vorhanden, kein roher Schlüssel', async () => {
    I18n.setLang('en');
    await oeffneBenutzer();
    expect(zeile('k3').textContent).toContain('Awaiting approval');
    expect(document.querySelector('.admin-konten').textContent).not.toMatch(/admin\.konten\./);
  });
});
