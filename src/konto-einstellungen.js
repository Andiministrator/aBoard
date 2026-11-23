/* konto-einstellungen.js — Konto-Einstellungen der angemeldeten Person
 * (Karte #1083, ADR-0149 §3, §4 und Nachtrag): weiteren Anmeldeweg verknüpfen
 * oder lösen, Passwort ändern, 2FA neu einrichten, Wiederherstellungscodes
 * erneuern, andere Sitzungen beenden.
 *
 * Seitenleiste → „Konto-Einstellungen“ (nur mit eingebauter Anmeldung, /me
 * meldet `anmeldung: "oidc"`) → dieser Dialog. Der Server (server/api/
 * konto_einstellungen.go) entscheidet alles; der Dialog zeigt nur, was /users/me/
 * konto als möglich nennt.
 *
 * Erneute Bestätigung: Hat die Person ein Passwort, steht oben EIN Block
 * „Bestätigung“ (aktuelles Passwort, bei 2FA zusätzlich ein Code); er gilt für
 * jede Änderung darunter. Nach jeder Änderung ist der Code verbraucht und wird
 * geleert — der nächste Code kommt nach etwa 30 Sekunden aus der App.
 *
 * Ohne Passwort (nur Anbieter oder Vorab-Liste, Karte #1098): Statt des
 * Passwortfelds bestätigt eine frische Anmeldung beim schon gebundenen Anbieter
 * jede Änderung. Wer eine Änderung wählt, bekommt den Knopf „Erst bei <Anbieter>
 * neu anmelden“; nach der Rückkehr (`/?konto=bestaetigt`) nennt der Stand den
 * Nachweis (`frisch`), und die gewählte Änderung ist einmal ausführbar.
 * Passwort nachträglich anlegen (#1099) erscheint nur, wenn der Server es
 * anbietet (`passwort_anlegen`).
 *
 * Rückkehr vom Anbieter nach dem Verknüpfen: der Server leitet nach `/?konto=…`;
 * beim Laden öffnet sich der Dialog mit dem Ergebnis, der Parameter verschwindet
 * aus der Adresse.
 *
 * Texte: eigene Tabelle Deutsch/Englisch (wie sitzung-abgelaufen.js). Der QR-Code
 * kommt als SVG vom Server und wird nur eingesetzt, wenn er aus nichts als Pfaden
 * und Rechtecken besteht.
 *
 * Gesamtes Modul hinter IS_SERVER_BUILD → im Slim/Projektor-Build per DCE weg.
 */

import { I18n } from './i18n.js';
import { createModal } from './modal-shell.js';
import { holen } from './sitzung-abgelaufen.js'; /* F-2977: fetch + Meldung an Sitzung.meldeAntwort */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let KontoEinstellungen = null;

if (IS_SERVER_BUILD) {
  const API = '/users/me/konto';
  const ERGEBNISSE = ['verknuepft', 'ersetzt', 'belegt', 'abgebrochen', 'abgelaufen', 'fehler', 'bestaetigt', 'nicht_bestaetigt', 'nicht_unterstuetzt', 'vorgang_alt'];
  const ERGEBNISSE_OK = ['verknuepft', 'ersetzt', 'bestaetigt'];

  const TEXTE = {
    de: {
      titel: 'Konto-Einstellungen',
      schliessen: 'Schließen',
      adresse: 'Angemeldet als %s.',
      laden: 'Lade …',
      ladeFehler: 'Die Konto-Einstellungen sind gerade nicht erreichbar. Bitte gleich noch einmal versuchen.',
      bestaetigung: 'Bestätigung',
      bestHinweis: 'Zur Sicherheit brauchen alle Änderungen auf dieser Seite dein aktuelles Passwort%s.',
      bestHinweisCode: ' und einen Code aus deiner Authenticator-App (oder einen Wiederherstellungscode)',
      passwortAktuell: 'Aktuelles Passwort',
      code: 'Code aus der App',
      codeNeu: 'Der Code ist verbraucht. Warte auf den nächsten Code aus der App (etwa 30 Sekunden).',
      wege: 'Anmeldewege',
      wegPasswort: 'Passwort mit zweitem Faktor',
      wegPasswortDa: 'eingerichtet',
      wegVerknuepft: 'verknüpft',
      wegNicht: 'nicht verknüpft',
      wegAltAnbieter: '(vom Betreiber nicht mehr eingerichtet)',
      verknuepfen: '%s verknüpfen',
      andereKonto: 'Anderes %s-Konto verknüpfen',
      loesen: 'Lösen',
      loesenLabel: '%s lösen',
      letzterWeg: 'Das ist dein einziger Anmeldeweg und kann nicht gelöst werden.',
      nurMitPasswort: 'Anmeldewege lassen sich hier nur mit Passwort oder mit einer neuen Anmeldung bei einem verknüpften Anbieter ändern – zum Schutz vor Missbrauch einer offenen Sitzung. Beides geht für deinen Zugang gerade nicht; für einen Wechsel wende dich an die Verwaltung (Betreiber oder Admin).',
      ersetzenFrage: 'Das neue Konto ersetzt das bisherige Konto bei %s. Mit dem bisherigen Konto kommst du danach nicht mehr herein. Fortfahren?',
      loesenFrage: 'Anmeldeweg %s wirklich lösen? Danach kommst du damit nicht mehr herein.',
      verknuepfenWeiter: 'Du wirst zum Anbieter weitergeleitet …',
      passwortAendern: 'Passwort ändern',
      passwortNeu: 'Neues Passwort (mindestens %d Zeichen)',
      passwortWdh: 'Neues Passwort wiederholen',
      passwortKnopf: 'Passwort ändern',
      ungleich: 'Die beiden neuen Passwörter sind nicht gleich.',
      passwortOk: 'Passwort geändert. Alle anderen Sitzungen sind beendet; du bleibst hier angemeldet.',
      zweiter: 'Zweiter Faktor',
      codesOffen: 'Noch %d unbenutzte Wiederherstellungscodes.',
      zweiNeu: '2FA neu einrichten',
      codesNeu: 'Neue Wiederherstellungscodes erzeugen',
      codesNeuFrage: 'Die bisherigen Wiederherstellungscodes werden ungültig. Fortfahren?',
      zweiSchritt: 'Richte die neue App ein: Scanne den QR-Code oder tippe den Schlüssel ein. Dann gib den ersten Code aus der neuen App ein. Bis dahin gilt die bisherige App weiter.',
      qrName: 'QR-Code für die Authenticator-App',
      schluessel: 'Schlüssel zum Eintippen',
      appLink: 'Link für Apps auf diesem Gerät',
      codeNeuApp: 'Erster Code aus der neuen App',
      zweiBestaetigen: 'Neue App bestätigen',
      zweiAbbrechen: 'Abbrechen',
      zweiOk: '2FA ist neu eingerichtet. Alle anderen Sitzungen sind beendet; du bleibst hier angemeldet.',
      codesTitel: 'Deine neuen Wiederherstellungscodes',
      codesHinweis: 'Diese Codes erscheinen nur jetzt. Schreibe sie auf oder speichere sie an einem sicheren Ort; jeder gilt einmal. Die bisherigen Codes sind ungültig.',
      sitzungen: 'Sitzungen',
      sitzungenText: 'Beendet die Anmeldung auf allen anderen Geräten und Browsern. Diese hier bleibt.',
      andere: 'Andere Sitzungen beenden',
      beendet: 'Andere Sitzungen beendet: %d.',
      keinKonto: 'Dein Zugang hat kein eigenes Passwort; Passwort und zweiter Faktor gibt es hier nicht. Du kannst weitere Anmeldewege verknüpfen.',
      f_falsch: 'Passwort oder Code stimmen nicht. Ein verbrauchter Code gilt nicht noch einmal – warte auf den nächsten.',
      f_gesperrt: 'Zu viele falsche Eingaben. Bitte in einigen Minuten noch einmal versuchen.',
      f_laeuft: 'Es läuft gerade schon eine Änderung. Bitte gleich noch einmal.',
      f_zu_kurz: 'Das neue Passwort ist zu kurz.',
      f_zu_lang: 'Das neue Passwort ist zu lang.',
      f_letzter_weg: 'Das ist dein einziger Anmeldeweg und bleibt.',
      f_ueberlastet: 'Der Server ist gerade ausgelastet. Bitte gleich noch einmal.',
      f_nicht_erreichbar: 'Der Anbieter ist gerade nicht erreichbar.',
      f_zwischen: 'Die Einrichtung ist abgelaufen oder überholt. Bitte noch einmal von vorn beginnen.',
      f_sonst: 'Das hat nicht geklappt. Bitte noch einmal versuchen.',
      abgelaufen: 'Deine Sitzung ist abgelaufen. Bitte neu anmelden.',
      r_verknuepft: 'Der Anmeldeweg ist verknüpft.',
      r_ersetzt: 'Das bisherige Konto ist durch das neue ersetzt.',
      r_belegt: 'Dieses Anbieter-Konto gehört schon zu einer anderen Person. Es wurde nichts verknüpft.',
      r_abgebrochen: 'Das Verknüpfen wurde beim Anbieter abgebrochen.',
      r_abgelaufen: 'Das Verknüpfen ist abgelaufen oder die Sitzung hat gewechselt. Es wurde nichts verknüpft. Bitte noch einmal beginnen.',
      r_fehler: 'Das Verknüpfen hat nicht geklappt. Es wurde nichts verknüpft.',
      r_bestaetigt: 'Du hast dich neu angemeldet. Die gewählte Änderung kannst du jetzt einmal ausführen (in den nächsten 5 Minuten).',
      r_nicht_unterstuetzt: 'Dein Anmeldedienst bestätigt keine frische Anmeldung (bei Google ohne verifizierte App üblich). Ab- und wieder anmelden hilft hier nicht. Es wurde nichts geändert. Bitte die Person, die diesen Server betreibt: Sie kann die Bestätigung mit ABOARD_OIDC_REAUTH_FALLBACK erlauben.',
      r_nicht_bestaetigt: 'Die neue Anmeldung wurde nicht anerkannt: Es muss dasselbe Konto beim Anbieter sein, mit dem du hier angemeldet bist, und die Anmeldung muss gerade eben erfolgt sein. Es wurde nichts geändert. Hat der Anbieter dich nicht nach deinem Passwort gefragt (bei Google üblich), melde dich dort erst ab (zum Beispiel auf accounts.google.com), dann wieder an, und versuche es danach erneut.',
      r_vorgang_alt: 'Die Anmeldung beim Anbieter gehört nicht zu diesem Vorgang. Es wurde nichts geändert. Ab- und wieder anmelden hilft hier nicht: Beginne die Bestätigung neu und folge dem Weg zum Anbieter ohne Umwege, am besten in einem einzigen Fenster.',
      frischTitel: 'Bestätigung durch neue Anmeldung',
      frischHinweis: 'Dein Zugang hat kein eigenes Passwort. Zur Sicherheit bestätigst du jede Änderung, indem du dich beim Anbieter neu anmeldest – mit demselben Konto wie bisher.',
      frischFuer: 'Für „%s“ melde dich erst neu an:',
      frischKnopf: 'Erst bei %s neu anmelden',
      frischAbbrechen: 'Abbrechen',
      frischWeiter: 'Du wirst zum Anbieter weitergeleitet …',
      frischBereit: 'Bestätigt für „%s“. Gilt einmal, bis %s Uhr.',
      frischAusfuehren: 'Jetzt ausführen',
      a_verknuepfen: '%s verknüpfen',
      a_ersetzen: 'Anderes %s-Konto verknüpfen',
      a_loesen: '%s lösen',
      a_passwort: 'Passwort anlegen',
      anlegen: 'Passwort anlegen',
      anlegenHinweis: 'Du kannst dir zusätzlich ein eigenes Passwort mit zweitem Faktor (Authenticator-App) anlegen. Danach meldest du dich wahlweise damit oder wie bisher beim Anbieter an.',
      anlegenKnopf: 'Passwort mit zweitem Faktor anlegen',
      anlegenSchritt: 'Richte die Authenticator-App ein: Scanne den QR-Code oder tippe den Schlüssel ein. Dann wähle dein Passwort und gib den ersten Code aus der App ein.',
      anlegenOk: 'Passwort und zweiter Faktor sind angelegt. Alle anderen Sitzungen sind beendet; du bleibst hier angemeldet.',
      anlegenNeuAnmelden: 'Passwort und zweiter Faktor sind angelegt. Deine Sitzung ließ sich danach nicht neu ausstellen: Schreibe die Wiederherstellungscodes unten jetzt auf und melde dich dann bitte neu an.',
      f_frische_anmeldung_noetig: 'Für diese Änderung musst du dich erst beim Anbieter neu anmelden.',
      f_passwort_nutzen: 'Dein Zugang hat ein Passwort; bestätige mit Passwort und Code.',
      f_nicht_gebunden: 'Bei diesem Anbieter bist du nicht angemeldet.',
      f_hat_passwort: 'Du hast schon ein Passwort.',
      f_konto_zustand: 'Dein Konto wartet auf eine Bestätigung oder ist gesperrt; ein Passwort lässt sich jetzt nicht anlegen.',
      f_aus: 'Das Anlegen eines Passworts ist auf diesem Server nicht eingeschaltet.',
    },
    en: {
      titel: 'Account settings',
      schliessen: 'Close',
      adresse: 'Signed in as %s.',
      laden: 'Loading …',
      ladeFehler: 'Account settings are not reachable right now. Please try again shortly.',
      bestaetigung: 'Confirmation',
      bestHinweis: 'For your safety, every change on this page needs your current password%s.',
      bestHinweisCode: ' and a code from your authenticator app (or a recovery code)',
      passwortAktuell: 'Current password',
      code: 'Code from the app',
      codeNeu: 'That code is used up. Wait for the next code from the app (about 30 seconds).',
      wege: 'Sign-in methods',
      wegPasswort: 'Password with second factor',
      wegPasswortDa: 'set up',
      wegVerknuepft: 'linked',
      wegNicht: 'not linked',
      wegAltAnbieter: '(no longer set up by the operator)',
      verknuepfen: 'Link %s',
      andereKonto: 'Link a different %s account',
      loesen: 'Unlink',
      loesenLabel: 'Unlink %s',
      letzterWeg: 'This is your only sign-in method and cannot be unlinked.',
      nurMitPasswort: 'Sign-in methods can only be changed here with a password or by signing in again at a linked provider – this protects against misuse of an open session. Neither works for your access right now; to switch, please contact administration (operator or admin).',
      ersetzenFrage: 'The new account replaces the current one at %s. You will no longer get in with the current one. Continue?',
      loesenFrage: 'Really unlink the sign-in method %s? You will no longer get in with it.',
      verknuepfenWeiter: 'Redirecting you to the provider …',
      passwortAendern: 'Change password',
      passwortNeu: 'New password (at least %d characters)',
      passwortWdh: 'Repeat new password',
      passwortKnopf: 'Change password',
      ungleich: 'The two new passwords do not match.',
      passwortOk: 'Password changed. All other sessions are ended; you stay signed in here.',
      zweiter: 'Second factor',
      codesOffen: '%d unused recovery codes left.',
      zweiNeu: 'Set up 2FA again',
      codesNeu: 'Create new recovery codes',
      codesNeuFrage: 'The current recovery codes become invalid. Continue?',
      zweiSchritt: 'Set up the new app: scan the QR code or type in the key. Then enter the first code from the new app. Until then the current app keeps working.',
      qrName: 'QR code for the authenticator app',
      schluessel: 'Key to type in',
      appLink: 'Link for apps on this device',
      codeNeuApp: 'First code from the new app',
      zweiBestaetigen: 'Confirm new app',
      zweiAbbrechen: 'Cancel',
      zweiOk: '2FA is set up again. All other sessions are ended; you stay signed in here.',
      codesTitel: 'Your new recovery codes',
      codesHinweis: 'These codes appear only now. Write them down or keep them somewhere safe; each works once. The previous codes are invalid.',
      sitzungen: 'Sessions',
      sitzungenText: 'Ends the sign-in on all other devices and browsers. This one stays.',
      andere: 'End other sessions',
      beendet: 'Other sessions ended: %d.',
      keinKonto: 'Your access has no password of its own; password and second factor do not apply here. You can link more sign-in methods.',
      f_falsch: 'Password or code is wrong. A used code does not work twice – wait for the next one.',
      f_gesperrt: 'Too many wrong entries. Please try again in a few minutes.',
      f_laeuft: 'Another change is running. Please try again shortly.',
      f_zu_kurz: 'The new password is too short.',
      f_zu_lang: 'The new password is too long.',
      f_letzter_weg: 'This is your only sign-in method and stays.',
      f_ueberlastet: 'The server is busy. Please try again shortly.',
      f_nicht_erreichbar: 'The provider is not reachable right now.',
      f_zwischen: 'The setup expired or was superseded. Please start over.',
      f_sonst: 'That did not work. Please try again.',
      abgelaufen: 'Your session has expired. Please sign in again.',
      r_verknuepft: 'The sign-in method is linked.',
      r_ersetzt: 'The previous account is replaced by the new one.',
      r_belegt: 'That provider account already belongs to another person. Nothing was linked.',
      r_abgebrochen: 'Linking was cancelled at the provider.',
      r_abgelaufen: 'Linking expired or the session changed. Nothing was linked. Please start again.',
      r_fehler: 'Linking did not work. Nothing was linked.',
      r_bestaetigt: 'You signed in again. You can now carry out the chosen change once (within the next 5 minutes).',
      r_nicht_unterstuetzt: 'Your sign-in provider does not confirm a fresh sign-in (usual with Google when the app is not verified). Signing out and in again does not help here. Nothing was changed. Ask the person who runs this server: they can allow the confirmation with ABOARD_OIDC_REAUTH_FALLBACK.',
      r_nicht_bestaetigt: 'The new sign-in was not accepted: it must be the same provider account you are signed in with here, and the sign-in must have happened just now. Nothing was changed. If the provider did not ask for your password (usual with Google), sign out there first (for example on accounts.google.com), sign in again, and then try once more.',
      r_vorgang_alt: 'The sign-in at the provider does not belong to this attempt. Nothing was changed. Signing out and in again does not help here: start the confirmation again and follow the path to the provider without detours, ideally in a single window.',
      frischTitel: 'Confirmation by signing in again',
      frischHinweis: 'Your access has no password of its own. For your safety you confirm every change by signing in again at the provider – with the same account as before.',
      frischFuer: 'For “%s”, sign in again first:',
      frischKnopf: 'Sign in again at %s first',
      frischAbbrechen: 'Cancel',
      frischWeiter: 'Redirecting you to the provider …',
      frischBereit: 'Confirmed for “%s”. Works once, until %s.',
      frischAusfuehren: 'Carry out now',
      a_verknuepfen: 'Link %s',
      a_ersetzen: 'Link a different %s account',
      a_loesen: 'Unlink %s',
      a_passwort: 'Create password',
      anlegen: 'Create password',
      anlegenHinweis: 'You can additionally create a password of your own with a second factor (authenticator app). Afterwards you sign in either with it or at the provider as before.',
      anlegenKnopf: 'Create password with second factor',
      anlegenSchritt: 'Set up the authenticator app: scan the QR code or type in the key. Then choose your password and enter the first code from the app.',
      anlegenOk: 'Password and second factor are set up. All other sessions are ended; you stay signed in here.',
      anlegenNeuAnmelden: 'Password and second factor are set up. Your session could not be renewed afterwards: write down the recovery codes below now, then please sign in again.',
      f_frische_anmeldung_noetig: 'For this change, sign in again at the provider first.',
      f_passwort_nutzen: 'Your access has a password; confirm with password and code.',
      f_nicht_gebunden: 'You are not signed in with this provider.',
      f_hat_passwort: 'You already have a password.',
      f_konto_zustand: 'Your account is awaiting confirmation or is blocked; a password cannot be created now.',
      f_aus: 'Creating a password is not switched on on this server.',
    },
  };
  const txt = (k, ...a) => {
    let s = (TEXTE[I18n.getLang()] || TEXTE.en)[k] ?? k;
    for (const x of a) s = s.replace(/%[sd]/, String(x));
    return s;
  };

  let _navigiere = (url) => { location.assign(url); };

  let _stylesInjected = false;
  const ensureStyles = () => {
    if (_stylesInjected) return;
    _stylesInjected = true;
    const style = document.createElement('style');
    style.setAttribute('data-aboard', 'konto-einstellungen');
    style.textContent = '.ke-panel{width:min(640px,100%)}.ke-abschnitt{margin:18px 0 0;padding:0;border:0}.ke-abschnitt>h3,.ke-abschnitt>legend{margin:0 0 6px;font-size:14px;font-weight:600;padding:0}.ke-hinweis{margin:4px 0 8px;font-size:13px;color:var(--text-muted)}.ke-feld{display:block;margin:8px 0}.ke-feld input{display:block;margin-top:3px;width:100%;max-width:340px;padding:7px 9px;background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;font-size:14px;box-sizing:border-box}.ke-knopf{padding:7px 12px;background:var(--surface-2);border:1px solid var(--border);border-radius:4px;color:inherit;cursor:pointer;font-size:13px;margin:2px 6px 2px 0}.ke-knopf:hover,.ke-knopf:focus-visible{background:var(--btn-bg-hover)}.ke-knopf[disabled]{opacity:.55;cursor:not-allowed}.ke-liste{list-style:none;margin:0;padding:0}.ke-liste li{display:flex;flex-wrap:wrap;gap:6px 12px;align-items:center;padding:6px 0;border-bottom:1px solid var(--border)}.ke-name{font-weight:600}.ke-zustand{font-size:13px;color:var(--text-muted)}.ke-meldung{margin:10px 0;padding:8px 10px;border:1px solid var(--border);border-radius:4px;font-size:13px}.ke-meldung--fehler{border-color:var(--danger,#c0392b)}.ke-codes{font-family:monospace;font-size:14px;columns:2;list-style:none;padding:8px 10px;margin:6px 0;border:1px dashed var(--border);border-radius:4px}.ke-qr svg{width:220px;height:220px;background:#fff;color:#000;display:block;margin:8px 0}.ke-schluessel{font-family:monospace;word-break:break-all}.ke-frisch{flex-basis:100%;margin:8px 0 4px;padding:10px 12px;border:1px solid var(--accent);border-left-width:4px;border-radius:4px;background:var(--accent-dim)}.ke-frisch p{margin:0 0 8px;font-weight:600}';
    document.head.appendChild(style);
  };

  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }

  async function anfrage(methode, pfad, body) {
    try {
      const res = await holen(API + pfad, {
        method: methode,
        headers: body ? { 'Content-Type': 'application/json' } : undefined,
        body: body ? JSON.stringify(body) : undefined,
      });
      const b = await res.json().catch(() => null);
      return { ok: res.ok, status: res.status, body: b };
    } catch {
      return { ok: false, status: 0, body: null };
    }
  }

  /* Der QR-Code kommt als SVG-Text vom Server (nur <svg> und <path>/<rect>).
     Trotzdem nie roh in innerHTML: geparst, geprüft, übernommen. */
  function qrKnoten(svgText) {
    if (typeof svgText !== 'string' || !svgText.includes('<svg')) return null;
    try {
      const doc = new DOMParser().parseFromString(svgText, 'image/svg+xml');
      const wurzel = doc.documentElement;
      if (!wurzel || wurzel.nodeName.toLowerCase() !== 'svg' || doc.querySelector('parsererror')) return null;
      const erlaubt = new Set(['svg', 'path', 'rect', 'title', 'desc']);
      for (const n of wurzel.querySelectorAll('*')) {
        if (!erlaubt.has(n.nodeName.toLowerCase())) return null;
        for (const a of n.attributes) if (/^on/i.test(a.name) || /javascript:/i.test(a.value)) return null;
      }
      return document.importNode(wurzel, true);
    } catch { return null; }
  }

  let _modal = null;
  let _inhalt = null;
  let _stand = null;
  let _meldung = null;   /* { art, text } — bleibt über das Neuzeichnen stehen */
  let _zwischen = null;  /* laufende 2FA-Erneuerung */
  let _anlegen = null;   /* laufendes Passwort-Anlegen (#1099) */
  let _vorhaben = null;  /* gewählte Änderung, die eine frische Anmeldung braucht (#1098) */
  let _codes = null;     /* frisch erzeugte Wiederherstellungscodes */
  let _lauf = 0;
  let _felder = { pw: null, code: null };

  function ensureModal() {
    if (_modal) return;
    ensureStyles();
    _modal = createModal({
      extraOverlayClass: 'ke-overlay', extraPanelClass: 'ke-panel', titleId: 'ke-title',
      closeAriaLabel: txt('schliessen'),
    });
    _modal.setTitle(txt('titel'));
    _inhalt = el('div', 'ke-inhalt');
    _modal.body.appendChild(_inhalt);
  }

  function setzeMeldung(art, text) { _meldung = text ? { art, text } : null; }

  function fehlerText(r) {
    if (r.status === 401) return txt('abgelaufen');
    const k = r.body?.fehler;
    return txt('f_' + (k || 'sonst')) === 'f_' + k ? txt('f_sonst') : txt('f_' + (k || 'sonst'));
  }

  function bestaetigungInhalt(extra = {}) {
    const b = { ...extra };
    if (_stand?.nachfrage?.passwort) b.aktuell = _felder.pw?.value ?? '';
    if (_stand?.nachfrage?.code) b.code = _felder.code?.value ?? '';
    return b;
  }

  /* Nach einer Änderung: Code ist verbraucht. */
  function codeLeeren() {
    if (_felder.code) _felder.code.value = '';
  }

  async function laden() {
    const lauf = ++_lauf;
    const r = await anfrage('GET', '');
    if (lauf !== _lauf) return;
    if (!r.ok || !r.body || !Array.isArray(r.body.wege)) {
      _stand = null;
      _inhalt.textContent = '';
      _inhalt.appendChild(el('p', 'ke-meldung ke-meldung--fehler', r.status === 401 ? txt('abgelaufen') : txt('ladeFehler')));
      return;
    }
    _stand = r.body;
    zeichne();
  }

  function abschnitt(titel, tag = 'section') {
    const s = el(tag, 'ke-abschnitt');
    s.appendChild(el(tag === 'fieldset' ? 'legend' : 'h3', '', titel));
    return s;
  }

  function knopf(text, onClick, opts = {}) {
    const b = el('button', 'ke-knopf', text);
    b.type = 'button';
    if (opts.disabled) b.disabled = true;
    if (opts.label) b.setAttribute('aria-label', opts.label);
    b.addEventListener('click', onClick);
    return b;
  }

  function feld(beschriftung, name, typ, auto) {
    const l = el('label', 'ke-feld');
    l.appendChild(document.createTextNode(beschriftung));
    const i = el('input');
    i.type = typ; i.name = name;
    if (auto) i.autocomplete = auto;
    l.appendChild(i);
    return { label: l, input: i };
  }

  function zeichne(fokus) {
    const s = _stand;
    _inhalt.textContent = '';
    if (!s) return;
    _felder = { pw: null, code: null };
    _inhalt.appendChild(el('p', '', txt('adresse', s.email)));

    const status = el('div');
    status.setAttribute('role', _meldung?.art === 'fehler' ? 'alert' : 'status');
    status.setAttribute('aria-live', 'polite');
    if (_meldung) {
      status.className = 'ke-meldung' + (_meldung.art === 'fehler' ? ' ke-meldung--fehler' : '');
      status.textContent = _meldung.text;
    }
    _inhalt.appendChild(status);

    if (!s.konto) _inhalt.appendChild(el('p', 'ke-hinweis', txt('keinKonto')));

    /* Bestätigung */
    if (s.nachfrage?.passwort) {
      const f = abschnitt(txt('bestaetigung'), 'fieldset');
      f.appendChild(el('p', 'ke-hinweis', txt('bestHinweis', s.nachfrage.code ? txt('bestHinweisCode') : '')));
      const pw = feld(txt('passwortAktuell'), 'aktuell', 'password', 'current-password');
      f.appendChild(pw.label);
      _felder.pw = pw.input;
      if (s.nachfrage.code) {
        const c = feld(txt('code'), 'code', 'text', 'one-time-code');
        c.input.inputMode = 'text'; c.input.autocapitalize = 'off'; c.input.spellcheck = false;
        f.appendChild(c.label);
        _felder.code = c.input;
      }
      _inhalt.appendChild(f);
    }

    /* Ohne Passwort: Bestätigung durch frische Anmeldung (#1098) */
    if (s.frisch_noetig && s.wege_aenderbar) _inhalt.appendChild(frischAbschnitt());

    /* Anmeldewege */
    const w = abschnitt(txt('wege'));
    const ul = el('ul', 'ke-liste');
    if (s.passwort) {
      const li = el('li');
      li.appendChild(el('span', 'ke-name', txt('wegPasswort')));
      li.appendChild(el('span', 'ke-zustand', txt('wegPasswortDa')));
      ul.appendChild(li);
    }
    for (const weg of s.wege) {
      const li = el('li');
      li.appendChild(el('span', 'ke-name', weg.label));
      li.appendChild(el('span', 'ke-zustand', (weg.verknuepft ? txt('wegVerknuepft') : txt('wegNicht')) + (weg.konfiguriert ? '' : ' ' + txt('wegAltAnbieter'))));
      if (weg.konfiguriert && s.wege_aenderbar) {
        li.appendChild(knopf(weg.verknuepft ? txt('andereKonto', weg.label) : txt('verknuepfen', weg.label), () => verknuepfen(weg)));
      }
      if (weg.verknuepft && s.wege_aenderbar) {
        li.appendChild(knopf(txt('loesen'), () => loesen(weg), { disabled: !weg.loesbar, label: txt('loesenLabel', weg.label) }));
        if (!weg.loesbar) li.appendChild(el('span', 'ke-hinweis', txt('letzterWeg')));
      }
      for (const art of ['verknuepfen', 'loesen']) {
        const kasten = frischKasten({ aktion: art, ziel: weg.anbieter });
        if (kasten) li.appendChild(kasten);
      }
      ul.appendChild(li);
    }
    w.appendChild(ul);
    if (!s.wege_aenderbar) w.appendChild(el('p', 'ke-hinweis', txt('nurMitPasswort')));
    _inhalt.appendChild(w);

    /* Passwort nachträglich anlegen (#1099) */
    if (s.passwort_anlegen) {
      const pa = abschnitt(txt('anlegen'));
      pa.appendChild(el('p', 'ke-hinweis', txt('anlegenHinweis')));
      if (_anlegen) pa.appendChild(anlegenAnsicht());
      else {
        pa.appendChild(knopf(txt('anlegenKnopf'), () => mitFrisch({ aktion: 'passwort', ziel: '' }, anlegenStart)));
        const kasten = frischKasten({ aktion: 'passwort', ziel: '' });
        if (kasten) pa.appendChild(kasten);
      }
      if (_codes && !s.passwort) pa.appendChild(codesAnsicht());
      _inhalt.appendChild(pa);
    }

    /* Passwort */
    if (s.passwort) {
      const p = abschnitt(txt('passwortAendern'));
      const neu = feld(txt('passwortNeu', 12), 'neu', 'password', 'new-password');
      neu.input.minLength = 12;
      const wdh = feld(txt('passwortWdh'), 'neu2', 'password', 'new-password');
      p.appendChild(neu.label);
      p.appendChild(wdh.label);
      p.appendChild(knopf(txt('passwortKnopf'), () => passwortAendern(neu.input, wdh.input)));
      _inhalt.appendChild(p);
    }

    /* Zweiter Faktor */
    if (s.zweiter_faktor) {
      const z = abschnitt(txt('zweiter'));
      z.appendChild(el('p', 'ke-hinweis', txt('codesOffen', s.codes_offen)));
      if (_zwischen) z.appendChild(zweiSchrittAnsicht());
      else {
        z.appendChild(knopf(txt('zweiNeu'), zweiStart));
        z.appendChild(knopf(txt('codesNeu'), codesNeu));
      }
      if (_codes) z.appendChild(codesAnsicht());
      _inhalt.appendChild(z);
    }

    /* Sitzungen */
    const si = abschnitt(txt('sitzungen'));
    si.appendChild(el('p', 'ke-hinweis', txt('sitzungenText')));
    si.appendChild(knopf(txt('andere'), sitzungenBeenden));
    _inhalt.appendChild(si);

    if (fokus === 'status') status.tabIndex = -1, status.focus({ preventScroll: false });
    else if (fokus === 'code' && _felder.code) _felder.code.focus();
    else if (fokus === 'codes') _inhalt.querySelector('.ke-codes')?.focus();
    else if (fokus === 'zwischen') _inhalt.querySelector('.ke-zwischen input')?.focus();
    else if (fokus === 'frisch') _inhalt.querySelector('.ke-frisch button')?.focus();
  }

  /* Beschreibung einer Aktion für Hinweise und Knöpfe. */
  function aktionText(a) {
    if (!a) return '';
    const weg = _stand?.wege?.find(w => w.anbieter === a.ziel);
    const label = weg?.label || a.ziel;
    if (a.aktion === 'verknuepfen') return txt(weg?.verknuepft ? 'a_ersetzen' : 'a_verknuepfen', label);
    if (a.aktion === 'loesen') return txt('a_loesen', label);
    if (a.aktion === 'passwort') return txt('a_passwort');
    return a.aktion;
  }

  const gleicheAktion = (a, b) => !!a && !!b && a.aktion === b.aktion && (a.ziel || '') === (b.ziel || '');

  /* Der Abschnitt „Bestätigung durch neue Anmeldung“: Hinweis, offener
     Nachweis (mit „Jetzt ausführen“) oder die Anbieter-Knöpfe für das
     gewählte Vorhaben. */
  function frischAbschnitt() {
    const f = abschnitt(txt('frischTitel'));
    f.appendChild(el('p', 'ke-hinweis', txt('frischHinweis')));
    return f;
  }

  /* #1108 (F-2941): Aufforderung bzw. „Jetzt ausführen“ erscheinen direkt am
     Knopf, der sie ausgelöst hat, hervorgehoben — nicht mehr oben im
     Erklär-Abschnitt. null, wenn für diese Aktion nichts ansteht. */
  function frischKasten(aktion) {
    const s = _stand;
    if (!s?.frisch_noetig) return null;
    if (_vorhaben && gleicheAktion(_vorhaben, aktion)) {
      const f = el('div', 'ke-frisch');
      f.appendChild(el('p', '', txt('frischFuer', aktionText(_vorhaben))));
      for (const an of s.frisch_anbieter || []) {
        f.appendChild(knopf(txt('frischKnopf', an.label), () => frischStarten(an.anbieter, _vorhaben)));
      }
      f.appendChild(knopf(txt('frischAbbrechen'), () => { _vorhaben = null; setzeMeldung(null); zeichne(); }));
      return f;
    }
    if (!_vorhaben && s.frisch && gleicheAktion(s.frisch, aktion)) {
      const bis = new Date(s.frisch.bis * 1000);
      const uhr = String(bis.getHours()).padStart(2, '0') + ':' + String(bis.getMinutes()).padStart(2, '0');
      const f = el('div', 'ke-frisch');
      f.appendChild(el('p', '', txt('frischBereit', aktionText(s.frisch), uhr)));
      f.appendChild(knopf(txt('frischAusfuehren'), () => ausfuehren(s.frisch)));
      return f;
    }
    return null;
  }

  /* Ohne Passwort: liegt ein Nachweis für genau diese Aktion vor, ausführen;
     sonst das Vorhaben merken und die Anmelde-Knöpfe zeigen. Mit Passwort:
     gleich ausführen (Bestätigung über die Felder oben). */
  function mitFrisch(aktion, ausfuehrung) {
    if (!_stand?.frisch_noetig || gleicheAktion(_stand.frisch, aktion)) return ausfuehrung();
    _vorhaben = aktion;
    setzeMeldung(null);
    zeichne('frisch');
  }

  function ausfuehren(a) {
    if (a.aktion === 'passwort') return anlegenStart();
    const weg = _stand?.wege?.find(w => w.anbieter === a.ziel);
    if (!weg) return;
    if (a.aktion === 'verknuepfen') return verknuepfenSenden(weg);
    if (a.aktion === 'loesen') return loesenSenden(weg);
  }

  async function frischStarten(anbieter, a) {
    const r = await anfrage('POST', '/frisch/start', { anbieter, aktion: a.aktion, ziel: a.ziel || '' });
    if (sitzungWeg(r)) return;
    if (!r.ok || typeof r.body?.ziel !== 'string' || !/^https:\/\//i.test(r.body.ziel)) {
      setzeMeldung('fehler', r.ok ? txt('f_sonst') : fehlerText(r));
      zeichne('status');
      return;
    }
    setzeMeldung('ok', txt('frischWeiter'));
    zeichne('status');
    _navigiere(r.body.ziel);
  }

  function anlegenAnsicht() {
    const box = el('div', 'ke-zwischen ke-anlegen');
    box.appendChild(el('p', 'ke-hinweis', txt('anlegenSchritt')));
    const qr = qrKnoten(_anlegen.qr);
    if (qr) {
      const q = el('div', 'ke-qr');
      qr.setAttribute('role', 'img');
      qr.setAttribute('aria-label', txt('qrName'));
      q.appendChild(qr);
      box.appendChild(q);
    }
    const p1 = el('p');
    p1.appendChild(document.createTextNode(txt('schluessel') + ': '));
    p1.appendChild(el('span', 'ke-schluessel', _anlegen.geheimnis));
    box.appendChild(p1);
    if (typeof _anlegen.link === 'string' && _anlegen.link.startsWith('otpauth://')) {
      const p2 = el('p');
      const a = el('a', '', txt('appLink'));
      a.href = _anlegen.link;
      p2.appendChild(a);
      box.appendChild(p2);
    }
    const neu = feld(txt('passwortNeu', 12), 'anlegen-neu', 'password', 'new-password');
    neu.input.minLength = 12;
    const wdh = feld(txt('passwortWdh'), 'anlegen-neu2', 'password', 'new-password');
    const c = feld(txt('code'), 'anlegen-code', 'text', 'one-time-code');
    c.input.inputMode = 'numeric'; c.input.spellcheck = false;
    box.appendChild(neu.label);
    box.appendChild(wdh.label);
    box.appendChild(c.label);
    box.appendChild(knopf(txt('anlegen'), () => anlegenAbschluss(neu.input, wdh.input, c.input)));
    box.appendChild(knopf(txt('zweiAbbrechen'), () => { _anlegen = null; setzeMeldung(null); zeichne(); }));
    return box;
  }

  async function anlegenStart() {
    const r = await anfrage('POST', '/passwort/anlegen/start', {});
    if (sitzungWeg(r)) return;
    if (!r.ok) {
      setzeMeldung('fehler', fehlerText(r));
      await laden();
      zeichne('status');
      return;
    }
    _vorhaben = null;
    _codes = null;
    _anlegen = r.body;
    setzeMeldung(null);
    await laden();
    zeichne('zwischen');
  }

  async function anlegenAbschluss(neu, wdh, code) {
    if (neu.value !== wdh.value) {
      setzeMeldung('fehler', txt('ungleich'));
      zeichne('status');
      return;
    }
    const r = await anfrage('POST', '/passwort/anlegen/abschluss', { zwischen: _anlegen?.zwischen, neu: neu.value, code: code.value });
    if (sitzungWeg(r)) return;
    if (!r.ok) {
      setzeMeldung('fehler', fehlerText(r));
      if (r.body?.fehler === 'zwischen' || r.body?.fehler === 'hat_passwort') _anlegen = null;
      zeichne('status');
      return;
    }
    _anlegen = null;
    _codes = Array.isArray(r.body?.codes) ? r.body.codes : null;
    if (r.body?.neu_anmelden) {
      /* Angelegt, aber ohne neue Sitzung (B4): Codes trotzdem zeigen, nicht
         neu laden (das ergäbe 401 und verdeckte die Codes). */
      setzeMeldung('fehler', txt('anlegenNeuAnmelden'));
      _stand = { ..._stand, passwort_anlegen: true };
      zeichne('codes');
      return;
    }
    setzeMeldung('ok', txt('anlegenOk'));
    await laden();
    zeichne('codes');
  }

  function codesAnsicht() {
    const box = el('div');
    box.appendChild(el('h4', '', txt('codesTitel')));
    box.appendChild(el('p', 'ke-hinweis', txt('codesHinweis')));
    const ul = el('ul', 'ke-codes');
    ul.tabIndex = -1;
    for (const c of _codes) ul.appendChild(el('li', '', c));
    box.appendChild(ul);
    return box;
  }

  function zweiSchrittAnsicht() {
    const box = el('div', 'ke-zwischen');
    box.appendChild(el('p', 'ke-hinweis', txt('zweiSchritt')));
    const qr = qrKnoten(_zwischen.qr);
    if (qr) {
      const q = el('div', 'ke-qr');
      qr.setAttribute('role', 'img');
      qr.setAttribute('aria-label', txt('qrName'));
      q.appendChild(qr);
      box.appendChild(q);
    }
    const p1 = el('p');
    p1.appendChild(document.createTextNode(txt('schluessel') + ': '));
    p1.appendChild(el('span', 'ke-schluessel', _zwischen.geheimnis));
    box.appendChild(p1);
    /* Der otpauth-Link nur als Text-Link mit festem Schema. */
    if (typeof _zwischen.link === 'string' && _zwischen.link.startsWith('otpauth://')) {
      const p2 = el('p');
      const a = el('a', '', txt('appLink'));
      a.href = _zwischen.link;
      p2.appendChild(a);
      box.appendChild(p2);
    }
    const c = feld(txt('codeNeuApp'), 'code-neu', 'text', 'one-time-code');
    c.input.inputMode = 'numeric'; c.input.spellcheck = false;
    box.appendChild(c.label);
    box.appendChild(knopf(txt('zweiBestaetigen'), () => zweiAbschluss(c.input.value)));
    box.appendChild(knopf(txt('zweiAbbrechen'), () => { _zwischen = null; setzeMeldung(null); zeichne(); }));
    return box;
  }

  /* --- Aktionen --------------------------------------------------------- */

  function sitzungWeg(r) {
    if (r.status === 401) { setzeMeldung('fehler', txt('abgelaufen')); zeichne('status'); return true; }
    return false;
  }

  async function aenderung(pfad, body, onOk, opts = {}) {
    const r = await anfrage('POST', pfad, bestaetigungInhalt(body));
    if (sitzungWeg(r)) return;
    if (!r.ok) {
      setzeMeldung('fehler', fehlerText(r));
      /* Bei falscher Bestätigung ist der Code verbraucht oder falsch. */
      if (r.body?.fehler === 'falsch') codeLeeren();
      zeichne('status');
      return;
    }
    codeLeeren();
    _codes = null;
    await onOk(r.body);
    if (!opts.ohneLaden) await laden();
  }

  async function passwortAendern(neu, wdh) {
    if (neu.value !== wdh.value) {
      setzeMeldung('fehler', txt('ungleich'));
      zeichne('status');
      return;
    }
    await aenderung('/passwort', { neu: neu.value }, () => {
      setzeMeldung('ok', txt('passwortOk'));
    });
    zeichne('status');
  }

  async function zweiStart() {
    const r = await anfrage('POST', '/2fa/start', bestaetigungInhalt());
    if (sitzungWeg(r)) return;
    if (!r.ok) {
      setzeMeldung('fehler', fehlerText(r));
      if (r.body?.fehler === 'falsch') codeLeeren();
      zeichne('status');
      return;
    }
    codeLeeren();
    _codes = null;
    _zwischen = r.body;
    setzeMeldung(null);
    zeichne('zwischen');
  }

  async function zweiAbschluss(code) {
    const r = await anfrage('POST', '/2fa/abschluss', { zwischen: _zwischen?.zwischen, code });
    if (sitzungWeg(r)) return;
    if (!r.ok) {
      setzeMeldung('fehler', fehlerText(r));
      /* Zwischenstand abgelaufen/überholt: zurück zum Anfang. */
      if (r.body?.fehler === 'zwischen') _zwischen = null;
      zeichne(r.body?.fehler === 'zwischen' ? 'status' : 'zwischen');
      return;
    }
    _zwischen = null;
    _codes = Array.isArray(r.body?.codes) ? r.body.codes : null;
    setzeMeldung('ok', txt('zweiOk'));
    await laden();
    zeichne('codes');
  }

  async function codesNeu() {
    if (!window.confirm(txt('codesNeuFrage'))) return;
    const r = await anfrage('POST', '/codes', bestaetigungInhalt());
    if (sitzungWeg(r)) return;
    if (!r.ok) {
      setzeMeldung('fehler', fehlerText(r));
      if (r.body?.fehler === 'falsch') codeLeeren();
      zeichne('status');
      return;
    }
    codeLeeren();
    _codes = Array.isArray(r.body?.codes) ? r.body.codes : null;
    setzeMeldung(null);
    await laden();
    zeichne('codes');
  }

  async function sitzungenBeenden() {
    const r = await anfrage('POST', '/sitzungen/beenden');
    if (sitzungWeg(r)) return;
    setzeMeldung(r.ok ? 'ok' : 'fehler', r.ok ? txt('beendet', r.body?.beendet ?? 0) : fehlerText(r));
    zeichne('status');
  }

  function verknuepfen(weg) {
    return mitFrisch({ aktion: 'verknuepfen', ziel: weg.anbieter }, () => verknuepfenSenden(weg));
  }

  async function verknuepfenSenden(weg) {
    if (weg.verknuepft && !window.confirm(txt('ersetzenFrage', weg.label))) return;
    const r = await anfrage('POST', '/wege/verknuepfen', bestaetigungInhalt({ anbieter: weg.anbieter }));
    if (sitzungWeg(r)) return;
    if (!r.ok || typeof r.body?.ziel !== 'string' || !/^https:\/\//i.test(r.body.ziel)) {
      setzeMeldung('fehler', r.ok ? txt('f_sonst') : fehlerText(r));
      if (r.body?.fehler === 'falsch') codeLeeren();
      zeichne('status');
      return;
    }
    setzeMeldung('ok', txt('verknuepfenWeiter'));
    zeichne('status');
    _navigiere(r.body.ziel);
  }

  function loesen(weg) {
    return mitFrisch({ aktion: 'loesen', ziel: weg.anbieter }, () => loesenSenden(weg));
  }

  async function loesenSenden(weg) {
    if (!window.confirm(txt('loesenFrage', weg.label))) return;
    _vorhaben = null;
    await aenderung('/wege/loesen', { anbieter: weg.anbieter }, () => { setzeMeldung(null); });
    zeichne('status');
  }

  /* --- Öffnen ------------------------------------------------------------ */

  async function open(ergebnis) {
    ensureModal();
    _zwischen = null;
    _anlegen = null;
    _vorhaben = null;
    _codes = null;
    _stand = null;
    setzeMeldung(ergebnis && ERGEBNISSE.includes(ergebnis)
      ? (ERGEBNISSE_OK.includes(ergebnis) ? 'ok' : 'fehler') : null,
      ergebnis && ERGEBNISSE.includes(ergebnis) ? txt('r_' + ergebnis) : '');
    _modal.setTitle(txt('titel'));
    _inhalt.textContent = '';
    _inhalt.appendChild(el('p', 'ke-hinweis', txt('laden')));
    _modal.open();
    await laden();
    _modal.focusAfterRender(_meldung ? _inhalt.querySelector('[role]') : (_felder.pw || _inhalt.querySelector('button')));
  }

  function close() {
    _lauf++;
    _zwischen = null;
    _anlegen = null;
    _vorhaben = null;
    _codes = null;
    _modal?.close();
  }

  /* Rückkehr vom Anbieter: /?konto=<ergebnis> → Dialog mit dem Ergebnis, die
     Adresse wird bereinigt (kein Neuladen mit demselben Ergebnis). */
  function pruefeRueckkehr() {
    let ergebnis = null;
    try {
      const p = new URLSearchParams(location.search);
      ergebnis = p.get('konto');
      if (!ERGEBNISSE.includes(ergebnis)) return false;
      p.delete('konto');
      const rest = p.toString();
      history.replaceState(history.state, '', location.pathname + (rest ? '?' + rest : '') + location.hash);
    } catch { return false; }
    open(ergebnis);
    return true;
  }

  KontoEinstellungen = {
    open, close, pruefeRueckkehr, text: txt,
    __testing: { setNavigiere(fn) { _navigiere = fn; }, qrKnoten },
  };

  if (typeof document !== 'undefined') {
    const los = () => KontoEinstellungen.pruefeRueckkehr();
    if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', los, { once: true });
    else setTimeout(los, 0);
  }
}

export { KontoEinstellungen };
