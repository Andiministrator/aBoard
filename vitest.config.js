/* Test-Kontext: claude-code — spiegelt vite.config.js (aboard.html).
   __SERVER_BUILD__=false, __PROJECTOR_BUILD__=true.

   Aufruf: bun run test:standalone
   Flag-Matrix + Begründung der vier Kontexte: vitest.config.base.js / ADR-0116. */

import { makeVitestConfig } from './vitest.config.base.js'

export default makeVitestConfig({ server: false, projektor: true })
