/* Aufzeichner für die Konformitäts-Fixtures des Server-Folds
 * (server/fold, Karte #551; übernommen aus dem Spike #546).
 *
 * Idee: die Fixtures NICHT von Hand aus den Tests abschreiben, sondern die
 * bestehenden Tests unverändert laufen lassen und dabei jeden Aufruf der
 * reinen Exporte von `src/ops.js` mitschneiden (Argumente VOR dem Aufruf,
 * Rückgabe, Argumente NACH dem Aufruf — Letzteres fängt das befüllte
 * `protokoll` von `fold` ein). Die Tests selbst prüfen weiter wie gewohnt;
 * der Wrapper reicht Rückgaben unverändert durch.
 *
 * Die Referenzergebnisse für Go entstehen NICHT hier, sondern in
 * `werkzeug/erzeuge-fixtures.mjs` aus dem gebündelten `server/fold/ops.bundle.js`
 * unter bun. Das
 * hier Aufgezeichnete dient nur als Eingabe und als Kreuzprobe
 * „Bündel == Quelle".
 *
 * `makeOp` wird bewusst nicht aufgezeichnet: es ist nicht rein (Uhr, UUID)
 * und gehört am Server ohnehin nicht in den Faltpfad (ADR-0129 §4: `ts`
 * stempelt der Server).
 */
import { vi, afterAll } from 'vitest';
import fs from 'node:fs';
import path from 'node:path';

vi.mock('../../../../src/ops.js', async (importOriginal) => {
  const orig = await importOriginal();
  const ZIELE = ['fold', 'tsSchluessel', 'validateOp', 'inspectOpPayload',
    'regelVerwerfung', 'fehlendeMassstabFelder'];
  const huelle = { ...orig };
  const ablage = (globalThis.__gojaAufnahmen ||= []);
  const alsJson = (v) => {
    try { return JSON.stringify(v === undefined ? null : v); } catch { return null; }
  };
  for (const name of ZIELE) {
    huelle[name] = (...args) => {
      const vorher = alsJson(args);
      const r = orig[name](...args);
      const st = globalThis.expect?.getState?.() || {};
      ablage.push({
        fn: name,
        datei: st.testPath ? path.basename(st.testPath) : '',
        test: st.currentTestName || '',
        args: vorher,
        argsNach: alsJson(args),
        ergebnis: alsJson(r),
      });
      return r;
    };
  }
  return huelle;
});

afterAll(() => {
  const ablage = globalThis.__gojaAufnahmen || [];
  if (ablage.length === 0) return;
  const ziel = process.env.GOJA_AUFNAHME_DIR;
  if (!ziel) throw new Error('GOJA_AUFNAHME_DIR fehlt');
  fs.mkdirSync(ziel, { recursive: true });
  const datei = ablage[0].datei || 'unbekannt';
  fs.writeFileSync(path.join(ziel, datei + '.json'), JSON.stringify(ablage));
});
