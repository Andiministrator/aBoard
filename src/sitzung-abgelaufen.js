/* sitzung-abgelaufen.js — abgelaufene Anmeldung im OIDC-Betrieb (Karte #1053,
 * ADR-0148 §5, Teil von #816).
 *
 * Im OIDC-Betrieb antwortet der Server ohne gültige Sitzung auf jeden fetch mit
 * 401. Dann gilt: KEINE ungesendete Änderung geht verloren (der Zustand bleibt
 * „dirty“, die Ops werden aus dem Diff gegen den Bezug neu gerechnet), das Senden
 * pausiert (kein Sturm von 401-Wiederholungen), ein Hinweis bietet „Neu
 * anmelden“ (neuer Tab, nur aus dem Klick — Popup-Blocker) und „Weiter senden“.
 * Sobald irgendeine Anfrage nicht mehr mit 401 antwortet (Poll-Takt, Fokus des
 * Tabs, Knopf), geht es von selbst weiter. Kein location.reload().
 *
 * Greift nur im OIDC-Betrieb (`GET /me` meldet `anmeldung: "oidc"`). Kommt ein 401,
 * bevor die Betriebsart bekannt ist (F-2830), klärt `klaereBetriebsart()` sie mit
 * einem einmaligen /me-Abruf und zeigt das Band nachträglich; ein 401 von /me
 * selbst trägt im OIDC-Betrieb „session required“ (Forward-Auth: „forward-auth
 * identity required“). Alle Fetch-Wege (Sync, Admin, Clients/Projekte, LLM-Stream,
 * Anhang-Upload) melden ihre Antwort über `meldeAntwort`.
 * Im Forward-Auth-Betrieb (Authelia) und im Standalone bleibt alles wie zuvor.
 *
 * Nur im Server-Build (DCE: im Standalone-Template entfällt die ganze Datei,
 * Texte stehen bewusst HIER und nicht in i18n.js, damit das Template nicht wächst).
 * Optik: vorhandene `recovery-banner`/`conflict-banner`-Klassen.
 */

import { I18n } from './i18n.js';
import { EventBus } from './eventbus.js';
import { State } from './state.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let Sitzung = null;

if (IS_SERVER_BUILD) {
  const KLASSE = 'sitzung-abgelaufen-banner';
  const ANMELDUNG_START = '/anmeldung';
  const ANMELDUNG_ENDE = '/anmeldung/ende';
  const ABMELDUNG_ZIEL = '/anmeldung/abgemeldet';

  const TEXTE = {
    de: {
      meldung: 'Sitzung abgelaufen — neu anmelden. Ungesendete Änderungen bleiben erhalten und werden danach gesendet. Nach der Anmeldung den neuen Tab schließen und hier weitermachen.',
      anmelden: 'Neu anmelden',
      weiter: 'Weiter senden',
      noch401: 'Noch nicht angemeldet — bitte zuerst im neuen Tab anmelden.',
      abmelden: 'Abmelden',
      warnung: 'Es gibt ungesendete Änderungen. Beim Abmelden gehen sie verloren. Trotzdem abmelden?',
      abmeldenFehler: 'Abmelden fehlgeschlagen.',
      wechselGesperrt: 'Sitzung abgelaufen — erst neu anmelden, dann wechseln. Ungesendete Änderungen bleiben erhalten.',
      nochUngesendet: 'Änderungen noch nicht gesendet — Wechsel abgebrochen. Bitte gleich erneut versuchen.',
      anhangLoeschenFehler: 'Anhang konnte am Server nicht gelöscht werden — er bleibt in der Karte. Bitte später erneut versuchen.',
      anhangLoeschen403: 'Keine Berechtigung, diesen Anhang zu löschen — er bleibt in der Karte.',
      anhangPurgeFehler: 'Karte gelöscht, aber ihre Anhänge konnten am Server nicht entfernt werden — das wird automatisch erneut versucht.',
      anhangPurgeFehlerN: 'Karten gelöscht, aber bei {n} Karten konnten die Anhänge am Server nicht entfernt werden — das wird automatisch erneut versucht.',
      anhangPurge403: 'Karte gelöscht, aber für ihre Anhänge fehlt die Berechtigung — sie bleiben am Server.',
      anhangPurge403N: 'Karten gelöscht, aber bei {n} Karten fehlt die Berechtigung für die Anhänge — sie bleiben am Server.',
      anhangPurgeBleibt: 'Karte gelöscht, aber ihre Anhänge konnten am Server nicht entfernt werden.',
      anhangPurgeBleibtN: 'Karten gelöscht, aber bei {n} Karten konnten die Anhänge am Server nicht entfernt werden.',
      anhangPurgeVerworfen: 'Die Anhänge einer gelöschten Karte wurden am Server nicht entfernt (die Karte besteht dort noch oder die Entfernung wurde abgelehnt) — es wird nicht weiter versucht.',
      anhangPurgeVerworfenN: 'Die Anhänge von {n} gelöschten Karten wurden am Server nicht entfernt (die Karten bestehen dort noch oder die Entfernung wurde abgelehnt) — es wird nicht weiter versucht.',
    },
    en: {
      meldung: 'Session expired — please sign in again. Unsent changes are kept and will be sent afterwards. After signing in, close the new tab and continue here.',
      anmelden: 'Sign in again',
      weiter: 'Continue sending',
      noch401: 'Not signed in yet — please sign in in the new tab first.',
      abmelden: 'Sign out',
      warnung: 'There are unsent changes. They will be lost when you sign out. Sign out anyway?',
      abmeldenFehler: 'Sign-out failed.',
      wechselGesperrt: 'Session expired — sign in again before switching. Unsent changes are kept.',
      nochUngesendet: 'Changes not sent yet — switch cancelled. Please try again shortly.',
      anhangLoeschenFehler: 'The attachment could not be deleted on the server — it stays in the card. Please try again later.',
      anhangLoeschen403: 'No permission to delete this attachment — it stays in the card.',
      anhangPurgeFehler: 'Card deleted, but its attachments could not be removed on the server — removal will be retried automatically.',
      anhangPurgeFehlerN: 'Cards deleted, but the attachments of {n} cards could not be removed on the server — removal will be retried automatically.',
      anhangPurge403: 'Card deleted, but there is no permission to remove its attachments — they stay on the server.',
      anhangPurge403N: 'Cards deleted, but there is no permission to remove the attachments of {n} cards — they stay on the server.',
      anhangPurgeBleibt: 'Card deleted, but its attachments could not be removed on the server.',
      anhangPurgeBleibtN: 'Cards deleted, but the attachments of {n} cards could not be removed on the server.',
      anhangPurgeVerworfen: 'The attachments of a deleted card were not removed on the server (the card still exists there, or removal was refused) — no further attempts.',
      anhangPurgeVerworfenN: 'The attachments of {n} deleted cards were not removed on the server (the cards still exist there, or removal was refused) — no further attempts.',
    },
  };
  const txt = k => (TEXTE[I18n.getLang()] || TEXTE.en)[k];

  /* oidc: null = Betriebsart noch unbekannt, true = OIDC, false = anderer Betrieb */
  const z = { oidc: null, abgelaufen: false, probe: null, laeuft: false,
    abruf: null, klaerung: null, offen401: false };

  function bannerEntfernen() {
    document.querySelector('.' + KLASSE)?.remove();
    document.removeEventListener('visibilitychange', beiSichtbar);
    window.removeEventListener('focus', beiFokus);
  }

  function beiFokus() { pruefeJetzt(); }
  function beiSichtbar() { if (document.visibilityState === 'visible') pruefeJetzt(); }

  function bannerZeigen() {
    if (document.querySelector('.' + KLASSE)) return;
    const banner = document.createElement('div');
    banner.className = 'recovery-banner conflict-banner ' + KLASSE;
    banner.setAttribute('role', 'alert');
    const msg = document.createElement('span');
    msg.textContent = txt('meldung');
    const neu = document.createElement('button');
    neu.className = 'btn btn-primary';
    neu.textContent = txt('anmelden');
    /* Nur aus dem Klick heraus: automatisch geöffnete Tabs blockt der Browser. */
    neu.addEventListener('click', () => { window.open(ANMELDUNG_START, '_blank'); });
    const weiter = document.createElement('button');
    weiter.className = 'btn';
    weiter.textContent = txt('weiter');
    weiter.addEventListener('click', async () => {
      await pruefeJetzt();
      if (z.abgelaufen) EventBus.emit('hinweis', txt('noch401'));
    });
    banner.append(msg, neu, weiter);
    document.body.prepend(banner);
    document.addEventListener('visibilitychange', beiSichtbar);
    window.addEventListener('focus', beiFokus);
  }

  function wiederDa() {
    z.abgelaufen = false;
    bannerEntfernen();
    EventBus.emit('sitzung:wieder-da', {});
  }

  /* Eine Antwort ist da. 401 im OIDC-Betrieb → pausieren, Hinweis; true.
     Nur eine Erfolgsantwort (2xx/304) beweist, dass die Sitzung wieder gilt
     → war pausiert, geht es weiter. 502/503 eines Proxys oder ein Netzfehler
     sagen darüber nichts (Kritik B-3: sonst flackerte das Band). */
  function meldeAntwort(status) {
    if (status === 401) {
      if (z.oidc === null) { z.offen401 = true; klaereBetriebsart(); return false; }
      if (!z.oidc) return false;
      if (!z.abgelaufen) { z.abgelaufen = true; bannerZeigen(); }
      return true;
    }
    if (z.abgelaufen && ((status >= 200 && status < 300) || status === 304)) wiederDa();
    return false;
  }

  /* F-2830: Betriebsart einmalig über /me klären (Einzelflug). `abruf` liefert
     { status, body } oder wirft. Ergebnis OIDC + zuvor gesehener 401 → Band. */
  function klaereBetriebsart() {
    if (z.oidc !== null || z.klaerung || typeof z.abruf !== 'function') return z.klaerung;
    z.klaerung = (async () => {
      try {
        const r = await z.abruf();
        const st = r?.status;
        if (st >= 200 && st < 300) z.oidc = r?.body?.anmeldung === 'oidc';
        else if (st === 401) z.oidc = /session required/i.test(JSON.stringify(r?.body ?? '')); /* sonst Forward-Auth */
      } catch { /* Netz weg: unbekannt, nächster 401 fragt erneut */ }
      finally { z.klaerung = null; }
      if (z.oidc === true && z.offen401) { z.offen401 = false; meldeAntwort(401); }
      else if (z.oidc !== null) z.offen401 = false;
    })();
    return z.klaerung;
  }

  async function pruefeJetzt() {
    if (!z.abgelaufen || z.laeuft || typeof z.probe !== 'function') return;
    z.laeuft = true;
    try {
      const status = await z.probe();
      if (Number.isInteger(status)) meldeAntwort(status);
    } catch { /* Netz weg: weiter pausiert */ }
    finally { z.laeuft = false; }
  }

  async function abmelden() {
    if (State.isDirty() && !window.confirm(txt('warnung'))) return false;
    try {
      const res = await fetch(ANMELDUNG_ENDE, { method: 'POST' });
      if (!res.ok) { EventBus.emit('hinweis', txt('abmeldenFehler')); return false; }
    } catch { EventBus.emit('hinweis', txt('abmeldenFehler')); return false; }
    /* Nicht '/': dort leitete der Server zur Anmeldung, und bei offener
       Anbieter-Sitzung wäre man still wieder drin (Sprint-Abschluss 106–108, K2 F-B). */
    location.assign(ABMELDUNG_ZIEL);
    return true;
  }

  /* #1053 Kritik B-1 / F-2851: Ein Board-Wechsel bei abgelaufener Sitzung darf die
     ungesendeten Änderungen nicht mit dem neuen Board überschreiben. Erst prüfen,
     ob die Anmeldung wieder gilt, dann noch einmal senden (`flush`); bleibt etwas
     ungesendet, wird nicht gewechselt. true = Wechsel abbrechen. */
  async function sperrtWechsel(flush) {
    if (!z.abgelaufen || !State.isDirty()) return false;
    await pruefeJetzt();
    if (!z.abgelaufen) {
      await flush?.();
      if (!State.isDirty()) return false;
      /* Verify V-5: Sitzung gilt wieder, Senden scheiterte aus anderem Grund. */
      EventBus.emit('hinweis', txt('nochUngesendet'));
      return true;
    }
    EventBus.emit('hinweis', txt('wechselGesperrt'));
    return true;
  }

  Sitzung = {
    setzeOIDC(an) {
      z.oidc = an === true;
      if (z.oidc && z.offen401) { z.offen401 = false; meldeAntwort(401); }
      else z.offen401 = false;
    },
    istOIDC() { return z.oidc === true; },
    betriebsartBekannt() { return z.oidc !== null; },
    setzeBetriebsartAbruf(fn) { z.abruf = fn; },
    klaereBetriebsart,
    sperrtWechsel,
    abgelaufen() { return z.abgelaufen; },
    setzeProbe(fn) { z.probe = fn; },
    meldeAntwort,
    pruefeJetzt,
    abmelden,
    text: txt,
    /* F-2990: Ergebnisse (true | false | 'verboten' | 'verworfen') einer oder mehrerer
       gelöschter Karten → höchstens EINE Meldung. false wird erneut versucht; 'verboten'
       (403) und 'verworfen' (übrige 4xx, volle Liste) bleiben am Server. Den
       Wiederhol-Text gibt es nur, wenn ALLE Fehlschläge wiederholt werden, den
       403-Text nur, wenn alle 403 waren (F-3014); sonst ein neutraler Text. */
    anhangPurgeMelden(ergebnisse) {
      const nochmal = ergebnisse.filter(e => e === false).length;
      const verboten = ergebnisse.filter(e => e === 'verboten').length;
      const n = nochmal + verboten + ergebnisse.filter(e => e === 'verworfen').length;
      if (!n) return;
      const key = nochmal === n ? 'anhangPurgeFehler' : verboten === n ? 'anhangPurge403' : 'anhangPurgeBleibt';
      EventBus.emit('hinweis', n === 1 ? txt(key) : txt(key + 'N').replace('{n}', n));
    },
    /* F-3017 (#1142): ein gemerkter Purge wurde beim Nachholen verworfen (Karte steht im
       Server-Stand noch, 409, 403, übrige 4xx) — vorher hieß es „wird erneut versucht“. */
    anhangPurgeVerworfen(n) {
      if (!n) return;
      EventBus.emit('hinweis', n === 1 ? txt('anhangPurgeVerworfen') : txt('anhangPurgeVerworfenN').replace('{n}', n));
    },
    /* nur für Tests */
    __zuruecksetzen() { z.oidc = null; z.abgelaufen = false; z.probe = null; z.laeuft = false; z.abruf = null; z.klaerung = null; z.offen401 = false; bannerEntfernen(); },
  };
}

/* F-2977 (#1130): gemeinsame Fetch-Hülle. Führt `fetch` aus und gibt jede Antwort an
   `Sitzung.meldeAntwort` (401 → Band „Sitzung abgelaufen“). Liefert die Antwort
   unverändert zurück und wirft wie `fetch` (Netzfehler bleiben Sache des Aufrufers).
   Wer wissen will, ob das Band ausgelöst wurde, fragt `Sitzung.abgelaufen()`.
   Außerhalb des Server-Builds (Standalone) ist es schlicht `fetch`. */
const holen = IS_SERVER_BUILD
  ? async (...a) => { const res = await fetch(...a); Sitzung.meldeAntwort(res.status); return res; }
  : (...a) => fetch(...a);

export { Sitzung, holen };
