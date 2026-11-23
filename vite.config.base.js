/* Gemeinsame Vite-Konfiguration für beide Build-Targets (ADR-0044, Karte #69).
 * Wird von vite.config.js (standalone) und vite.config.server.js importiert. */

export const sharedBuild = {
  target: 'esnext',
};

/* ── Server-CSS-Strip-Plugin (Karte #216 / F-274, erweitert #328) ──────────
 * Entfernt server-only CSS-Regeln aus dem fertigen HTML des Nicht-Server-Builds.
 * Wird von vite.config.js und vite.config.template.js eingebunden;
 * vite.config.server.js lässt es weg → Server-Frontend behält die Regeln.
 *
 * Gestrippte Selektor-Muster (alle ausschließlich in IS_SERVER_BUILD-JS-Pfaden
 * referenziert):
 *   - .share-* (#199, ADR-0059 Share-Dialog)
 *   - .board-header-avatar (#201, ADR-0059 Header-Avatar — #328-Gate)
 *   - .config-advanced (#223, Erweitert-Sektion im Board-Einstellungen-Dialog —
 *     nur im `IS_SERVER_BUILD && has('server')`-Zweig von configdialog.js gebaut;
 *     Karte #729, Bundle-Aufräumrunde)
 *
 * Technik: closeBundle-Hook auf der fertigen index.html nach viteSingleFile.
 * viteSingleFile bettet CSS inline ein; dieser Hook läuft danach und entfernt
 * serverseitige CSS-Regeln aus dem HTML-Source.
 *
 * Regex-Klasse \.x[^{]*\{[^{}]*\} ist F-244-sicher:
 *   - Selektoren sind isoliert (keine Mehrfach-Selektor-Listen — verifiziert).
 *   - Keine CSS-Nesting in den Regel-Blöcken (verifiziert in src/style.css).
 *   - [^{}]* verhindert falsches Matching bei verschachtelten Blöcken.
 *
 * enforce:'post' + closeBundle stellt sicher, dass viteSingleFile (das selbst
 * einen generateBundle-Hook nutzt) zuerst inlined hat. */
import { readFileSync, writeFileSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { execFileSync } from 'node:child_process';
import { parseAst } from 'vite';

/**
 * Build-Version für die Frontend-Anzeige (Karte #347).
 *
 * Quellen in dieser Reihenfolge:
 *  1. `ABOARD_BUILD_VERSION` aus der Umgebung — im Container-Build der einzige
 *     Weg: `.dockerignore` schliesst `.git` aus, `git describe` kann dort also
 *     gar nicht laufen. Das Dockerfile reicht das VERSION-build-arg durch.
 *  2. `git describe --tags --always` beim lokalen Build. Bewusst NICHT
 *     `package.json`.version: die steht seit dem MVP auf 0.1.0 und wird nicht
 *     gepflegt — sie würde eine falsche Zahl anzeigen, und das ist schlimmer
 *     als gar keine. Ein Zwischenstand erscheint als `v1.34.1-9-g0db8d82` und
 *     ist damit als Zwischenstand erkennbar.
 *  3. `'dev'` — weder Env noch git verfügbar.
 *
 * NEBENWIRKUNG auf die Bundle-Ratsche (ADR-0112/0114): Die Bundle-Grösse hängt
 * jetzt an der LÄNGE des Versionsstrings. `v1.34.1-9-g0db8d82` (19 Zeichen) ist
 * länger als ein Tag-Build `v1.35.0` (7) oder ein Hash-Fallback ohne Tags (8).
 * Die Schwankung liegt im einstelligen Byte-Bereich, also weit unter der
 * 1000-B-Toleranz — aber wer eine Ratschen-Meldung um wenige Bytes debuggt,
 * sollte diesen Beitrag kennen, statt ihn für einen Leak zu halten.
 */
export function appVersion() {
  const ausEnv = process.env.ABOARD_BUILD_VERSION;
  if (ausEnv && ausEnv.trim()) return ausEnv.trim();
  try {
    return execFileSync('git', ['describe', '--tags', '--always'], {
      encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'],
    }).trim() || 'dev';
  } catch {
    return 'dev';
  }
}

/* Selektor-Präfixe, deren CSS-Regeln im Nicht-Server-Build entfernt werden.
   Jedes Pattern matcht „<Selektor>{ ... }" inkl. evtl. davor stehender
   :hover/:focus-Suffixe (über [^{]* im Selektor-Teil). */
const SERVER_ONLY_CSS_PATTERNS = [
  /\.share-[^{]+\{[^{}]*\}/g,
  /\.board-header-avatar[^{]*\{[^{}]*\}/g,
  /\.config-advanced[^{]*\{[^{}]*\}/g,
];

/* Ersetzt den Inhalt des eingebetteten `#app-data`-Blocks (F-371, Karte #488).
 *
 * Alle drei Build-Ziele brauchen das, mit verschiedenem Inhalt und aus
 * verschiedenen Gruenden — und trugen bis Sprint 65 je eine eigene Kopie
 * derselben Regex:
 *
 *   standalone  → `{}`, weil aboard.html seit der ADR-0067-Migration
 *                 FSA-basiert laeuft und die eingebetteten Daten inert sind;
 *                 ein nicht geleertes Artefakt liefert beim Framework-Import
 *                 ein volles Repo-Board statt eines leeren (Karte #330).
 *   template    → `{}`, damit jede frische Installation ueber
 *                 `Model.createBoard()` eine eigene UUID bekommt und nicht
 *                 die geteilte Kennung `aboard-project` erbt (F-246-Klasse).
 *   server      → ein leeres Board MIT fester id `aboard-server`, weil dort
 *                 die REST-API die Quelle der Wahrheit ist; ein eingebettetes
 *                 Dev-Board waere Funktionsfehler und Informationsleck
 *                 zugleich (F-249, Konsumenten-Report v1.18.0).
 *
 * Die Gruende sind verschieden, der Regex-Griff ist derselbe — und genau der
 * ist die Stelle, an der ein Vergessen teuer wird: Wer das Markup des
 * `#app-data`-Blocks aendert und nur zwei der drei Kopien nachzieht, baut ein
 * Artefakt, das sein Board still weiter mitliefert. Der Build laeuft dabei
 * durch; auffallen wuerde es erst beim Konsumenten.
 *
 * @param {string} name    Plugin-Name (erscheint in vite-Fehlermeldungen)
 * @param {string} inhalt  Was zwischen die Script-Tags geschrieben wird
 */
export function emptyAppDataPlugin(name, inhalt = '{}') {
  return {
    name,
    transformIndexHtml(html) {
      /* Function-Replacer, kein String (Kritiker-Lauf 2026-09-17): Ein
         Ersatz-String wird von `String.replace()` erneut nach `$`-Mustern
         durchsucht — `$&` im Inhalt holte den KOMPLETTEN alten #app-data-Block
         zurück, also genau die Board-Daten, die hier gerade entfernt werden
         sollen (die Gegenrichtung von F-249). Heute enthält kein Aufrufer ein
         `$`; die Fabrik ist aber für drei Ziele gebaut und der nächste
         Aufrufer sieht dem Aufruf nicht an, dass sein Inhalt interpretiert
         wird. Die Funktionsform kennt keine `$`-Sonderbedeutung. */
      return html.replace(
        /(<script type="application\/json" id="app-data">)[\s\S]*?(<\/script>)/,
        (_treffer, oeffnend, schliessend) => `${oeffnend}\n${inhalt}\n${schliessend}`,
      )
    },
  }
}

export function stripServerCss() {
  let outDir;
  return {
    name: 'aboard-strip-server-css',
    enforce: 'post',
    configResolved(config) {
      outDir = resolve(config.root, config.build.outDir);
    },
    closeBundle() {
      /* Bearbeite die fertige index.html nach viteSingleFile-Inlining.
         generateBundle greift zu früh (viteSingleFile schreibt in closeBundle). */
      const htmlPath = join(outDir, 'index.html');
      let html;
      try { html = readFileSync(htmlPath, 'utf8'); } catch { return; }
      let stripped = html;
      for (const re of SERVER_ONLY_CSS_PATTERNS) {
        stripped = stripped.replace(re, '');
      }
      if (stripped !== html) writeFileSync(htmlPath, stripped, 'utf8');
    },
  };
}

/* ── i18n-Schlüssel nur einmal im Bundle (Karte #1064, F-2861, ADR-0114 Nachtrag 2026-11-02) ──
 * `src/i18n.js` führt zwei Objektliterale `DE` und `EN` mit denselben
 * Schlüsseln. Der Minifier kann die Schlüssel nicht teilen; jeder steht im
 * fertigen Bundle zweimal (gemessen ~7 KB im Template). Dieser Transform
 * schreibt beide Literale beim Bauen in EINE Schlüsselliste und zwei
 * Wertelisten um und setzt die Objekte zur Laufzeit wieder zusammen:
 *
 *   const __I18N_K = ['btn.save', …]
 *   const DE = __i18nZip([`Speichern`, …])
 *   const EN = __i18nZip([`Save`, …])
 *
 * Der Transform läuft in allen Builds und auch im Dev-Server (`vite`); dort ist
 * er unschädlich, nur die Zeilenzuordnung für i18n.js verschiebt sich.
 *
 * Danach sind DE und EN gewöhnliche Objekte wie vorher; die
 * `Object.assign(DE, …)`-Blöcke für Server/Projektor bleiben unberührt.
 * Fehlt ein Schlüssel in einer Sprache, steht dort ein Loch im Array, und der
 * Schlüssel fehlt im Objekt — wie im Quelltext.
 *
 * Bewusst streng: Alles, was nicht `'schlüssel': wert` ist (berechneter
 * Schlüssel, Spread, Methode, Kurzform, doppelter Schlüssel, fehlendes DE/EN),
 * bricht den Build ab, statt still anders zu bauen. Die Tests laufen gegen den
 * Quelltext; `tests/i18n-schluessel-einmal.test.js` prüft, dass der
 * umgeschriebene Code dieselben Wörterbücher ergibt. */
const I18N_DATEI = /[\\/]src[\\/]i18n\.js$/;

export function faltI18nSchluessel(code) {
  const ast = parseAst(code);
  const decls = {};
  for (const st of ast.body) {
    if (st.type !== 'VariableDeclaration') continue;
    for (const d of st.declarations) {
      if (d.id.type === 'Identifier' && (d.id.name === 'DE' || d.id.name === 'EN')) {
        if (decls[d.id.name]) throw new Error(`i18n-Faltung: ${d.id.name} doppelt deklariert`);
        if (st.declarations.length !== 1 || d.init?.type !== 'ObjectExpression') {
          throw new Error(`i18n-Faltung: ${d.id.name} ist keine einzelne Deklaration mit Objektliteral`);
        }
        decls[d.id.name] = { st, obj: d.init };
      }
    }
  }
  if (!decls.DE || !decls.EN) throw new Error('i18n-Faltung: DE oder EN nicht gefunden');

  const eintraege = (name) => {
    const m = new Map();
    for (const p of decls[name].obj.properties) {
      if (p.type !== 'Property' || p.kind !== 'init' || p.computed || p.shorthand || p.method ||
          p.key.type !== 'Literal' || typeof p.key.value !== 'string') {
        throw new Error(`i18n-Faltung: unerwartete Eigenschaft in ${name} an Position ${p.start}`);
      }
      if (m.has(p.key.value)) throw new Error(`i18n-Faltung: Schlüssel doppelt in ${name}: ${p.key.value}`);
      /* Geklammert einsetzen: ESTree-Offsets schließen umgebende Klammern nicht
         ein, ein Komma-Ausdruck `('x', 'y')` zerfiele sonst in zwei Werte
         (Sitzungs-Kritik 02.11. K-1). Der Minifier entfernt die Klammern. */
      m.set(p.key.value, '(' + code.slice(p.value.start, p.value.end) + ')');
    }
    return m;
  };
  const de = eintraege('DE');
  const en = eintraege('EN');
  const schluessel = [...de.keys(), ...[...en.keys()].filter((k) => !de.has(k))];
  const werte = (m) => '[' + schluessel.map((k) => (m.has(k) ? m.get(k) : '')).join(',') + ']';

  const kopf = `const __I18N_K=${JSON.stringify(schluessel)};` +
    'function __i18nZip(v){const o={};for(let i=0;i<__I18N_K.length;i++)if(v[i]!==void 0)o[__I18N_K[i]]=v[i];return o}\n';

  /* Von hinten nach vorn ersetzen, damit die Offsets davor gültig bleiben. */
  const ersatz = [
    { start: decls.DE.obj.start, end: decls.DE.obj.end, text: `__i18nZip(${werte(de)})` },
    { start: decls.EN.obj.start, end: decls.EN.obj.end, text: `__i18nZip(${werte(en)})` },
    { start: Math.min(decls.DE.st.start, decls.EN.st.start), end: Math.min(decls.DE.st.start, decls.EN.st.start), text: kopf },
  ].sort((a, b) => b.start - a.start || b.end - a.end);
  let aus = code;
  for (const e of ersatz) aus = aus.slice(0, e.start) + e.text + aus.slice(e.end);
  return aus;
}

export function i18nSchluesselEinmal() {
  return {
    name: 'aboard-i18n-schluessel-einmal',
    transform(code, id) {
      if (!I18N_DATEI.test(id.split('?')[0])) return null;
      return { code: faltI18nSchluessel(code), map: null };
    },
  };
}
