/* Vitest-Kontext NUR für den Aufzeichnungslauf der Server-Fold-Fixtures
 * (Karte #551; aus dem Spike #546 übernommen). Entspricht dem
 * standalone-Kontext (vitest.config.js) plus Aufzeichner.
 *
 * Ablauf: server/fold/README.md, Abschnitt „Fixtures neu erzeugen“.
 */
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { makeVitestConfig } from '../../../../vitest.config.base.js';

const wurzel = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../../..');
const basis = makeVitestConfig({ server: false, projektor: true });

export default {
  ...basis,
  root: wurzel,
  test: {
    ...basis.test,
    setupFiles: [...basis.test.setupFiles, './server/fold/werkzeug/aufzeichnung/aufzeichner.js'],
  },
};
