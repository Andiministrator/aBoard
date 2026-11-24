/* Bündel-Einstieg für den Fold am Server (Karte #551, ADR-0129 §4).
 *
 * `src/ops.js` ist ein ES-Modul ohne eigene Importe; goja kann kein ESM
 * (K-12). `bun run build:ops-bundle` bündelt diesen Einstieg deshalb zu einem
 * IIFE, dessen einzige Wirkung die Zuweisung an `globalThis.AboardOps` ist
 * (bun kennt kein `--global-name`). Das Ergebnis `ops.bundle.js` liegt
 * eingecheckt daneben und wird per go:embed eingebunden.
 *
 * Maßgeblich ist die EINGECHECKTE Datei: Genau sie prüfen die Go-
 * Konformitätstests, und genau sie landet im Binary. Dass sie zur Quelle
 * passt, prüft `tests/ops-buendel.test.js` bei jedem `bun run test`.
 *
 * Build-Konstanten (`__PROJECTOR_BUILD__`, `__SERVER_BUILD__`) kommen in
 * `src/ops.js` nicht vor — es braucht kein `--define`. Kommt eine hinzu,
 * bricht der Bündel-Test nicht, wohl aber `TestBuendelLaedt` (ReferenceError).
 */
import * as ops from '../../src/ops.js';

globalThis.AboardOps = ops;
