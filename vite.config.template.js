/* Build-Target: template — leeres Distributable (ADR-0049, Karte #187).
 *
 * Wie `vite.config.js` (standalone): Single-File via viteSingleFile,
 * __SERVER_BUILD__ = 'false', eingebettetes #app-data-Board auf `{}` geleert
 * (das leert `vite.config.js` inzwischen auch). Der eigentliche Unterschied:
 * __PROJECTOR_BUILD__ = 'false' — ohne die Projektor-Schicht (ADR-0072), damit
 * das Template unter dem Bundle-Limit bleibt (F-1858 berichtigt den früheren
 * Satz „EINZIGER Unterschied: Board geleert“).
 *
 * Wirkung: `Storage.load()` sieht kein `board` → liefert {state:null} →
 * `main.js` ruft `Model.createBoard()` → jede frische Installation bekommt
 * eine eigene UUID + den Default-Spaltensatz. Damit ist die F-246-Klasse
 * (geteilte board.id-Konstante `aboard-project` in Kopien) strukturell
 * verhindert — zusätzlich zum pathname-Scoping aus ADR-0048 (Defense-in-depth).
 *
 * Bewusst KEINE feste id (anders als vite.config.server.js, das für die
 * REST-Welt `aboard-server` fixiert). Das Distributable bleibt id-los. */

import { defineConfig }   from 'vite'
import { viteSingleFile } from 'vite-plugin-singlefile'
import { sharedBuild, stripServerCss, appVersion, emptyAppDataPlugin, i18nSchluesselEinmal } from './vite.config.base.js'

/* Das Leeren selbst liegt seit F-371 in vite.config.base.js; der Grund dafuer
 * steht oben im Dateikopf und ist ein anderer als beim Standalone-Build. */

export default defineConfig({
  plugins: [
    i18nSchluesselEinmal(),
    emptyAppDataPlugin('aboard-empty-template-board'),
    viteSingleFile({ removeViteModuleLoader: true }),
    stripServerCss(),
  ],
  define: {
    /* Wie standalone: REST-Plugin-Code fällt aus dem Bundle (Tree-Shake). */
    __SERVER_BUILD__: 'false',
    /* ADR-0072: Public-Distributable (solo/team) — ADR-0067-Projektor-Schicht
       per DCE entfernt. Hartlimit < 360000 B (ADR-0114 samt Nachträgen 2026-10-06/2026-10-12, revidiert ADR-0112/0111/0110;
       Build-Modell unverändert). claude-code nutzt aboard.html. */
    __PROJECTOR_BUILD__: 'false',
    /* Karte #347: sichtbare Build-Version im UI. Muss in ALLEN Build-Configs
       gesetzt sein — sonst ist der Bezeichner im betroffenen Build undefiniert. */
    __APP_VERSION__: JSON.stringify(appVersion()),
  },
  build: {
    ...sharedBuild,
    outDir: 'dist-template',
    assetsInlineLimit: 100000000,
  },
})
