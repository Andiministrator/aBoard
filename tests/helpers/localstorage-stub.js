/**
 * Minimal-`localStorage` für happy-dom (F-402).
 *
 * happy-dom liefert in dieser Konfiguration kein Default-`localStorage`. Jeder
 * Test, der eins braucht, hat sich den Stub bis Sprint 61 selbst in sein
 * `beforeEach` kopiert (`tests/llm-tab.test.js`, `tests/llm-mutation-dialog.
 * test.js` — Zeichen für Zeichen dieselben acht Zeilen). Das ist genau die
 * Duplikation, die `tests/helpers/build-flags.js` für die Compile-Flags
 * beseitigt hat: solange es keinen benannten Ort gibt, wandert die nächste
 * Kopie mit dem nächsten Test mit, und eine Abweichung darin fällt niemandem
 * auf.
 *
 * Ablage bewusst unter `tests/helpers/` (die Finding-Notiz schlug
 * `tests/_setup-localstorage.js` vor) — das ist das im Repo etablierte
 * Verzeichnis für geteilte Test-Hilfen.
 *
 * SEIT KARTE #712 (F-1746) zusätzlich als `setupFiles`-Eintrag
 * (`tests/helpers/localstorage-setup.js`): Vorher stand hier, ein globaler
 * Stub beeinflusse Tests, die das Fehlen gerade prüfen wollen. Gemessen:
 * keiner tut das — wer ein *kaputtes* localStorage braucht, setzt es selbst
 * (Wurf-Stub, `= undefined`). Die Gegenseite war teurer: ohne Stub lief jeder
 * Code mit try/catch um localStorage in allen Tests still durch den
 * Fehlerzweig und wirkte grün (Fehlerklasse 147). Der Setup-Eintrag legt den
 * Stub vor jedem Test frisch an (F-1766: auch über ein Wurf-Objekt des
 * Vorgängers hinweg).
 */

/**
 * Stellt sicher, dass `globalThis.localStorage` existiert, und leert es.
 *
 * Ein bereits vorhandenes `localStorage` wird hier nicht ersetzt, nur geleert.
 * Das Setup (`localstorage-setup.js`) setzt den Stub seit F-1766 vor jedem Test
 * frisch (auch beim Modul-Import). `ensureLocalStorage` ist deshalb nur noch
 * für Tests nötig, die mitten im Test (nach einem Wurf-Stub oder
 * `= undefined`) wieder einen heilen, leeren Speicher brauchen; in einem
 * beforeEach leert es lediglich, was das Setup ohnehin frisch angelegt hat.
 *
 * @returns {Storage} das aktive (ggf. frisch gestubbte) localStorage
 */
export function ensureLocalStorage() {
  installiereLocalStorage()
  globalThis.localStorage.clear()
  return globalThis.localStorage
}

/**
 * Legt den Stub an, wenn `globalThis.localStorage` fehlt (undefined) — ohne
 * vorhandene Werte anzufassen. Mit `key()`/`length`, damit Code, der den
 * Speicher aufzählt, nicht an der Lücke des Stubs scheitert.
 *
 * Mit `{ erzwingen: true }` wird der Stub IMMER neu gesetzt, auch über ein
 * Wurf-Objekt eines Vorgängertests hinweg (F-1766) — so nutzt es das Setup
 * vor jedem Test.
 */
export function installiereLocalStorage({ erzwingen = false } = {}) {
  let vorhanden
  try { vorhanden = globalThis.localStorage } catch { vorhanden = undefined }
  if (!erzwingen && typeof vorhanden !== 'undefined') return
  const _store = new Map()
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    writable: true,
    value: {
      getItem:    (k) => (_store.has(k) ? _store.get(k) : null),
      setItem:    (k, v) => { _store.set(k, String(v)) },
      removeItem: (k) => { _store.delete(k) },
      clear:      () => { _store.clear() },
      key:        (i) => [..._store.keys()][i] ?? null,
      get length() { return _store.size },
    },
  })
}
