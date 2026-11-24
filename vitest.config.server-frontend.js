/* Test-Kontext: server-frontend — spiegelt vite.config.server.js.
   __SERVER_BUILD__=true, __PROJECTOR_BUILD__=false.

   Neu in Sprint 53 (ADR-0116). Der Befund, der zu diesem Kontext führte:
   vite.config.server.js setzt __PROJECTOR_BUILD__='false' (Z. 44), der
   bestehende Server-Test-Kontext aber 'true'. Das ausgelieferte Server-
   Frontend lief damit in einer Flag-Kombination, die kein Test je gesehen
   hat — dieselbe Lücke wie beim slim Template, nur unbemerkt geblieben,
   weil F-434 nur die slim-Hälfte benannte.

   Aufruf: bun run test:server-frontend */

import { makeVitestConfig } from './vitest.config.base.js'

export default makeVitestConfig({ server: true, projektor: false })
