/* Build-Target: standalone — erzeugt aboard.html als Single-File (ADR-0044, Karte #69).
 * Zwei separate Config-Files statt Mode-Switch gewählt: kein impliziter Laufzeit-Zweig,
 * jedes Target ist eigenständig lesbar. */

import { defineConfig } from 'vite'
import { viteSingleFile } from 'vite-plugin-singlefile'
import { sharedBuild, stripServerCss, appVersion, emptyAppDataPlugin, i18nSchluesselEinmal } from './vite.config.base.js'

/* Karte #330 / Bug 1 (Option A): Projektor-Build leert app-data analog zum Template-Build.
 * Hintergrund: aboard.html wird seit 2026-05-18 (Commit d8b1ebd, ADR-0067-Migration) FSA-basiert
 * betrieben; die eingebetteten Daten sind inert. Beim Framework-Import in ein neues Projekt
 * liefert ein nicht-geleerte aboard.html ein volles Repo-Board statt eines leeren Boards.
 * Defense-in-depth für loadFromEmbedded() liegt zusätzlich in src/storage.js (#330 Option C).
 * Die Regex selbst liegt seit F-371 in vite.config.base.js — sie war dreimal kopiert. */

export default defineConfig({
  plugins: [
    i18nSchluesselEinmal(),
    emptyAppDataPlugin('aboard-empty-standalone-board'),
    viteSingleFile({ removeViteModuleLoader: true }),
    stripServerCss(),
  ],
  define: {
    /* Tree-Shake-Hebel: REST-Plugin-Code faellt im Standalone-Bundle heraus.
       config.js und storage-plugin.js pruefen dieses Flag zur Compile-Zeit. */
    __SERVER_BUILD__: 'false',
    /* ADR-0072: aboard.html (Dogfooding/claude-code/Konsumenten) trägt die
       ADR-0067-Projektor-Schicht voll — ADR-0014: kein Public-Bundle-Limit. */
    __PROJECTOR_BUILD__: 'true',
    /* Karte #347: sichtbare Build-Version im UI. Muss in ALLEN Build-Configs
       gesetzt sein — sonst ist der Bezeichner im betroffenen Build undefiniert. */
    __APP_VERSION__: JSON.stringify(appVersion()),
  },
  build: {
    ...sharedBuild,
    outDir: 'dist',
    assetsInlineLimit: 100000000,
  },
})
