/* rueckmeldung.js — gemeinsame Rückmeldungs-Helfer der Server-Dialoge
 * (F-1459, Sprint-80-Kritiker V-1).
 *
 * Vorher bauten admin-dialog (`meldung`/`markiereKnopf`), agent-zugaenge-dialog
 * (`melde`/`blinke`) und boards-view (`setze`) dieselbe Logik je einmal, und
 * die gekürzte Board-ID (`id.slice(0,8)+'…'`) stand doppelt in admin-dialog.
 * Karte #637(b) hatte Wiederverwendung verlangt. Die Dialoge unterscheiden sich
 * nur im CSS-Präfix — der kommt als Parameter.
 *
 * Nur im Server-Build (DCE im Slim-/Claude-Code-Build, kein Template-Zuwachs).
 */

const IS_SERVER_BUILD = (typeof __SERVER_BUILD__ !== 'undefined') &&
  (__SERVER_BUILD__ === true || __SERVER_BUILD__ === 'true');

let Rueckmeldung = null;

if (IS_SERVER_BUILD) {
  /* Meldungszeile färben: `<modifikator>--busy|ok|err` ersetzt einen früheren
     Zustand, übrige Klassen bleiben. role: err → alert, sonst status. */
  function meldeZeile(ziel, modifikator, art, text, rolle) {
    ziel.textContent = text;
    for (const c of [...ziel.classList]) {
      if (c.startsWith(modifikator + '--')) ziel.classList.remove(c);
    }
    ziel.classList.add(modifikator + '--' + art);
    ziel.setAttribute('role', rolle || (art === 'err' ? 'alert' : 'status'));
  }

  /* Knopf kurz aufleuchten lassen (`<basis>--ok|err`). */
  function blinkeKnopf(btn, basis, art) {
    if (!btn || !btn.classList) return;
    btn.classList.remove(basis + '--ok', basis + '--err');
    btn.classList.add(basis + '--' + art);
    setTimeout(() => btn.classList.remove(basis + '--' + art), 1500);
  }

  /* Gekürzte Board-ID für Anzeigen, die gleichnamige Boards unterscheiden
     müssen (lange IDs: erste 8 Zeichen + „…“). */
  function kurzeId(id) {
    const s = String(id);
    return s.length > 12 ? s.slice(0, 8) + '…' : s;
  }

  Rueckmeldung = { meldeZeile, blinkeKnopf, kurzeId };
}

export { Rueckmeldung };
