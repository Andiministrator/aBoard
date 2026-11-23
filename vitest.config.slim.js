/* Test-Kontext: slim — spiegelt vite.config.template.js (aboard.template.html).
   __SERVER_BUILD__=false, __PROJECTOR_BUILD__=false.

   Neu in Sprint 53 (ADR-0116, F-434). Bis dahin lief KEIN Test-Kontext mit
   __PROJECTOR_BUILD__='false' — die Build-Konfiguration des öffentlichen
   Distributables war damit strukturell ungetestet, und „beide Kontexte grün"
   sagte über sie nichts aus (Beleg: F-428 lief unentdeckt durch).

   Aufruf: bun run test:slim */

import { makeVitestConfig } from './vitest.config.base.js'

export default makeVitestConfig({ server: false, projektor: false })
