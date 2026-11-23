/* boot-hinweis.js — dauerhafter Hinweis, wenn der Server-Modus-Start die
 * Board-Liste nicht laden konnte (F-1453, Sprint-80-Kritiker K-7).
 *
 * Seit #637 liefert `adoptOrCreate` bei 5xx/Login-Redirect `ok:false` (statt ein
 * neues Server-Board anzulegen, F-1440). Der Browser arbeitet dann auf einem
 * lokal erzeugten Board mit Zufalls-id, das der Server nicht kennt; jedes
 * Speichern scheiterte bisher still (428), die Änderungen waren beim Reload
 * weg. Ein Toast von 2,5 s reicht dafür nicht: der Hinweis bleibt stehen, bis
 * neu geladen oder auf ein Server-Board gewechselt wird, und das Plugin
 * verweigert das Speichern (`sperreSpeichern`), statt es ins Leere zu
 * schicken. KEIN automatisches Anlegen eines Server-Boards.
 *
 * Nur im Server-Build (DCE im Slim-/Claude-Code-Build, kein Template-Zuwachs).
 * Optik: vorhandene `recovery-banner`/`conflict-banner`-Klassen (style.css).
 */

import { I18n } from './i18n.js';

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let BootHinweis = null;

if (IS_SERVER_BUILD) {
  const KLASSE = 'server-boot-banner';

  function zeige() {
    if (document.querySelector('.' + KLASSE)) return;
    const banner = document.createElement('div');
    banner.className = 'recovery-banner conflict-banner ' + KLASSE;
    banner.setAttribute('role', 'alert');
    const msg = document.createElement('span');
    msg.textContent = I18n.t('sync.boardliste-fehlt');
    const btn = document.createElement('button');
    btn.className = 'btn btn-primary';
    btn.textContent = I18n.t('sync.boardliste-neu-laden');
    btn.addEventListener('click', () => { location.reload(); });
    banner.appendChild(msg);
    banner.appendChild(btn);
    document.body.prepend(banner);
  }

  function entferne() {
    document.querySelector('.' + KLASSE)?.remove();
  }

  BootHinweis = { zeige, entferne };
}

export { BootHinweis };
