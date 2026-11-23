/* Gemeinsame Basis der vitest-Kontexte (ADR-0116, F-434).
 *
 * Warum eine Factory: Bis Sprint 53 gab es zwei Kontexte, die sich in genau
 * einer `define`-Zeile unterschieden und ihre Test-Optionen wortgleich
 * kopierten. Mit vier Kontexten wäre die vierfache Kopie eine Drift-Quelle —
 * ändert jemand `environment` oder `include` in dreien von vier, fällt es
 * niemandem auf, weil alle grün bleiben.
 *
 * Die Flag-Matrix steht bewusst HIER als Kommentar und nicht verstreut über
 * die vier Dateien — sie ist die eigentliche Aussage dieses Aufbaus:
 *
 *   | Kontext              | __SERVER_BUILD__ | __PROJECTOR_BUILD__ | Artefakt                |
 *   |----------------------|------------------|---------------------|-------------------------|
 *   | vitest.config.js     | false            | true                | aboard.html             |
 *   | vitest.config.slim   | false            | false               | aboard.template.html    |
 *   | vitest.config.server | true             | true                | — (ADR-0072-Invariante) |
 *   | vitest.config.server-frontend | true    | false               | server/web/dist         |
 *
 * **Die Zeilenreihenfolge IST die Ausführungsreihenfolge von `bun run test`**
 * (standalone → slim → server → server-frontend, siehe `package.json`). Bis
 * zum Sprint-61-Abschluss standen die letzten beiden Zeilen hier vertauscht —
 * ausgerechnet in der Datei, auf die CLAUDE.md für diese Matrix verweist, und
 * CLAUDE.md sagt seit Sprint 60 ausdrücklich, die Reihenfolge sei verbindlich
 * (F-757: wer die vier Ausgabeblöcke der Kette von oben nach unten gegen eine
 * falsche Tabelle liest, ordnet die Skip-Zahlen falsch zu).
 *
 * Drei der vier Zeilen sind je ein ausgeliefertes Artefakt. Die dritte ist die
 * einzige Kombination ohne Artefakt: sie testet die datenverlust-kritische
 * Projektor-Schicht zusätzlich unter Server-Bedingungen (ADR-0072).
 */

import { defineConfig } from 'vitest/config'
import { appVersion } from './vite.config.base.js'

/**
 * @param {{ server: boolean, projektor: boolean }} flags
 */
export function makeVitestConfig({ server, projektor }) {
  return defineConfig({
    define: {
      __SERVER_BUILD__:    server    ? 'true' : 'false',
      __PROJECTOR_BUILD__: projektor ? 'true' : 'false',
      /* Karte #347: auch im Test-Kontext gesetzt — fehlt das Define, ist der
         Bezeichner ein ReferenceError statt eines leeren Strings, und jede
         Suite, die src/sidebar.js lädt, stirbt beim Import. */
      __APP_VERSION__:     JSON.stringify(appVersion()),
    },
    test: {
      environment: 'happy-dom',
      globals: true,
      include: ['tests/**/*.test.js'],
      /* Karte #397 / F-599: gemeinsamer fetch-Fallback-Stub gegen echte
         Netzwerkverbindungen im Testlauf (Begruendung in der Datei selbst). */
      setupFiles: ['./tests/helpers/fetch-stub.js', './tests/helpers/sitzungs-umgebung.js', './tests/helpers/localstorage-setup.js'],
    },
  })
}
