/* Erzeugt die Konformitäts-Fixtures für den Server-Fold (server/fold, Karte #551;
 * übernommen aus dem Spike #546).
 *
 * Eingabe:  Aufnahmen aus dem Aufzeichnungslauf (GOJA_AUFNAHME_DIR, siehe
 *           aufzeichnung/vitest.aufzeichnung.config.js).
 * Referenz: das GEBÜNDELTE `ops.bundle.js`, ausgeführt unter bun — also
 *           dieselbe Datei, die Go per go:embed lädt. Keine Handarbeit.
 * Kreuzprobe: das Bündel-Ergebnis (auf JSON-Eingaben) muss dem im Testlauf
 *           aufgezeichneten Ergebnis der QUELLE `src/ops.js` (auf den
 *           Original-Eingaben) gleichen. Wo nicht, verändert der JSON-Umweg
 *           die Eingabe — solche Fälle sind nicht übertragbar und werden
 *           gezählt, nicht versteckt.
 *
 * Aufruf: siehe server/fold/README.md, Abschnitt „Fixtures neu erzeugen“.
 *
 */
import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';

const hier = path.dirname(new URL(import.meta.url).pathname);
const bündel = fs.readFileSync(path.join(hier, '..', 'ops.bundle.js'), 'utf8');
/* Strikt wie in goja (goja.Compile(..., strict=true)) — ESM-Quelle ist strikt. */
new Function('"use strict";\n' + bündel)();
const Ops = globalThis.AboardOps;
if (!Ops || typeof Ops.fold !== 'function') throw new Error('Bündel setzt AboardOps nicht');

const dir = process.env.GOJA_AUFNAHME_DIR;
if (!dir) throw new Error('GOJA_AUFNAHME_DIR fehlt');

const statistik = {};
const zähle = (datei, schlüssel) => {
  statistik[datei] ||= { aufrufe: 0, nichtSerialisierbar: 0, jsonUmwegAbweichend: 0, doppelt: 0, fixtures: 0, tests: new Set(), testsMitFixture: new Set() };
  statistik[datei][schlüssel]++;
};
const fixtures = [];
const gesehen = new Set();
const abweichend = [];

for (const name of fs.readdirSync(dir).filter(n => n.endsWith('.json')).sort()) {
  const aufnahmen = JSON.parse(fs.readFileSync(path.join(dir, name), 'utf8'));
  for (const a of aufnahmen) {
    const d = a.datei || name;
    zähle(d, 'aufrufe');
    statistik[d].tests.add(a.test);
    /* args === null heisst: JSON.stringify ist beim Aufzeichnen gescheitert. */
    if (a.args === null) { zähle(d, 'nichtSerialisierbar'); continue; }
    const args = JSON.parse(a.args);
    const r = Ops[a.fn](...args);
    const ergebnis = JSON.stringify(r === undefined ? null : r);
    const argsNach = JSON.stringify(args);
    if (ergebnis !== a.ergebnis || argsNach !== a.argsNach) {
      zähle(d, 'jsonUmwegAbweichend');
      abweichend.push({ datei: d, test: a.test, fn: a.fn });
      continue;
    }
    const schlüssel = a.fn + '\u0000' + a.args;
    if (gesehen.has(schlüssel)) { zähle(d, 'doppelt'); statistik[d].testsMitFixture.add(a.test); continue; }
    gesehen.add(schlüssel);
    zähle(d, 'fixtures');
    statistik[d].testsMitFixture.add(a.test);
    fixtures.push({ fn: a.fn, datei: d, test: a.test, args: a.args, erwartetErgebnis: ergebnis, erwartetArgsNach: argsNach });
  }
}

fs.mkdirSync(path.join(hier, '..', 'testdata'), { recursive: true });
/* gzip: unkomprimiert ~5 MB (jede fold-Fixture trägt ihren Snapshot), so ~45 KB. */
fs.writeFileSync(path.join(hier, '..', 'testdata', 'fixtures.json.gz'), zlib.gzipSync(JSON.stringify(fixtures), { level: 9 }));

const jeFn = {};
for (const f of fixtures) jeFn[f.fn] = (jeFn[f.fn] || 0) + 1;
for (const [d, s] of Object.entries(statistik)) {
  console.log(d, JSON.stringify({ ...s, tests: s.tests.size, testsMitFixture: s.testsMitFixture.size }));
}
console.log('Fixtures je Funktion:', JSON.stringify(jeFn), 'gesamt', fixtures.length);
if (abweichend.length) console.log('JSON-Umweg abweichend:', JSON.stringify(abweichend, null, 1));
