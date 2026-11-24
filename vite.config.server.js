/* Build-Target: server-frontend — erzeugt dist/server-frontend/ ohne Single-File
 * (ADR-0044, Karte #69). Kein viteSingleFile-Plugin, REST-Profil hartcodiert. */

import { defineConfig } from 'vite'
import { sharedBuild, appVersion, emptyAppDataPlugin, i18nSchluesselEinmal } from './vite.config.base.js'

/* F-249/ADR-0052: index.html trägt im #app-data das interne aBoard-Dev-Board
 * (ADR-0014 Source-of-Truth des Standalone-Builds). Im Server-Build ist die
 * Quelle der Wahrheit die REST-API/SQLite — das eingebettete Board MUSS leer
 * sein, sonst liefert jede frische Server-Instanz unser Dev-Board aus
 * (Funktionsfehler + Internal-Info-Leak, Befund F-249 aus v1.18.0).
 * columnDefs bleibt [] — storage.js migrateDefaultColumns füllt beim Laden den
 * Standard-Spaltensatz (Single Source of Truth in model.js, getestet). */
const EMPTY_BOARD = JSON.stringify({
  version: '2',
  board: {
    id: 'aboard-server', title: 'aBoard', nextDisplayId: 1,
    columnDefs: [],
    sprints: [{ id: '__backlog__', title: 'Backlog', kind: 'backlog', cards: [] }],
    templates: [], background: null, attachments: [], awaitingUser: null,
  },
}, null, 2)

export default defineConfig({
  /* Anders als die beiden anderen Ziele wird hier nicht auf `{}` geleert,
     sondern auf ein leeres Board MIT fester id — Begruendung oben. Die Regex
     teilen sich seit F-371 alle drei. */
  plugins: [i18nSchluesselEinmal(), emptyAppDataPlugin('aboard-empty-embedded-board', EMPTY_BOARD)],
  define: {
    /* Signalisiert config.js: boardMode auf 'server' fixieren, kein localStorage-Lookup.
       Signalisiert storage-plugin.js: FSA/Embedded-Plugin-Code faellt heraus. */
    __SERVER_BUILD__: 'true',
    /* ADR-0072: Server-Frontend synct via REST-Backend, nicht über den
       lokalen FSA-Op-Projektor → Projektor-Schicht per DCE entfernt. */
    __PROJECTOR_BUILD__: 'false',
    /* Karte #347: sichtbare Build-Version im UI. Muss in ALLEN Build-Configs
       gesetzt sein — sonst ist der Bezeichner im betroffenen Build undefiniert. */
    __APP_VERSION__: JSON.stringify(appVersion()),
  },
  build: {
    ...sharedBuild,
    outDir: 'dist/server-frontend',
    assetsInlineLimit: 0,
  },
})
