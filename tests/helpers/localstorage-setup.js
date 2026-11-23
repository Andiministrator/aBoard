/* Zentraler localStorage-Stub für alle Testdateien (Karte #712, F-1746).

   PROBLEM
   Unter vitest + happy-dom + Node 26 ist `localStorage` nicht definiert
   („--localstorage-file was not provided“). Code mit try/catch um den Zugriff
   läuft dann in jedem Test durch den Fehlerzweig und wirkt grün — die
   „zuletzt benutzt“-Liste des Board-Menüs hat in keinem Test je geschrieben,
   bis ein Test sich selbst einen Stub baute (Fehlerklasse 147).

   LÖSUNG
   Vor jedem Test wird der Stub aus `localstorage-stub.js` IMMER frisch gesetzt
   (`erzwingen`, F-1766) — auch wenn der Vorgängertest ein Wurf-Objekt oder
   `undefined` hinterlassen hat (z. B. bei rotem expect vor der Rücknahme).
   Dadurch erbt kein Test Reste oder eine Sperre des Vorgängers. Tests, die ein
   gesperrtes oder fehlendes localStorage brauchen, setzen es im Test selbst
   (Wurf-Objekt bzw. `= undefined`); der nächste Test bekommt wieder einen
   heilen Stub. Gemessen in tests/localstorage-setup.test.js. */
import { beforeEach } from 'vitest'
import { installiereLocalStorage } from './localstorage-stub.js'

installiereLocalStorage()
beforeEach(() => {
  installiereLocalStorage({ erzwingen: true })
})
