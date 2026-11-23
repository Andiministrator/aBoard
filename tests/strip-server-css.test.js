/**
 * stripServerCss() — Server-only-CSS aus aboard.html und dem Template (Karte #729).
 *
 * Das Plugin entfernt Regeln per Regex aus der fertigen index.html. Bis Karte
 * #729 hatte es keinen eigenen Test; mit `.config-advanced` kam ein drittes
 * Muster hinzu. Geprüft wird gegen die ECHTE src/style.css, nicht gegen ein
 * Beispiel — sonst bleibt unbemerkt, wenn jemand einen der Selektoren in eine
 * Selektorliste zieht und das Muster mitten in die Liste schneidet (F-244-Klasse).
 */

import { describe, it, expect, afterEach } from 'vitest';
import { mkdtempSync, writeFileSync, readFileSync, rmSync, mkdirSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { stripServerCss } from '../vite.config.base.js';

const STYLE = readFileSync(resolve(process.cwd(), 'src/style.css'), 'utf8');

let dir = null;
afterEach(() => { if (dir) rmSync(dir, { recursive: true, force: true }); dir = null; });

function strippe(css) {
  dir = mkdtempSync(join(tmpdir(), 'aboard-strip-'));
  mkdirSync(join(dir, 'out'));
  const htmlPath = join(dir, 'out', 'index.html');
  writeFileSync(htmlPath, `<html><head><style>${css}</style></head></html>`, 'utf8');
  const plugin = stripServerCss();
  plugin.configResolved({ root: dir, build: { outDir: 'out' } });
  plugin.closeBundle();
  return readFileSync(htmlPath, 'utf8');
}

/* Kommentare entfernen, damit Selektor-Erwähnungen in Kommentaren nicht zählen. */
const ohneKommentare = s => s.replace(/\/\*[\s\S]*?\*\//g, '');

describe('stripServerCss (Karte #729)', () => {
  it('entfernt .config-advanced, .share-* und .board-header-avatar aus der echten style.css', () => {
    const vorher = ohneKommentare(STYLE);
    expect(vorher).toMatch(/\.config-advanced\b/);
    const nachher = strippe(vorher);
    expect(nachher).not.toMatch(/\.config-advanced\b/);
    expect(nachher).not.toMatch(/\.share-/);
    expect(nachher).not.toMatch(/\.board-header-avatar/);
  });

  it('lässt die benachbarte, geteilte Regel .config-row stehen', () => {
    const nachher = strippe(ohneKommentare(STYLE));
    expect(nachher).toMatch(/\.config-row\s*\{/);
  });

  it('kein gestrippter Selektor steht in einer Selektorliste — weder vorne noch dahinter', () => {
    /* Das Muster frisst ab dem Treffer bis zur nächsten `{`. Steht der Selektor
       in einer Liste, geht es in BEIDEN Richtungen schief (Sitzungs-QA #729, C-1):
       `.foo, .config-advanced {…}` lässt `.foo, ` hängen (verschmilzt mit der
       nächsten Regel), `.config-advanced, .config-row {…}` nimmt die geteilte
       `.config-row`-Regel lautlos mit. Deshalb je REGEL prüfen, nicht je Treffer. */
    const css = ohneKommentare(STYLE);
    const betroffen = /\.(?:config-advanced|share-|board-header-avatar)/;
    const verstoesse = [];
    for (const m of css.matchAll(/([^{}]+)\{/g)) {
      const selektor = m[1].trim();
      if (!betroffen.test(selektor) || selektor.startsWith('@')) continue;
      if (selektor.includes(',')) verstoesse.push(selektor.replace(/\s+/g, ' '));
    }
    expect(verstoesse).toEqual([]);
  });

  it('die Listenprüfung erkennt beide Richtungen (Selbsttest mit künstlichem CSS)', () => {
    const pruefe = css => [...css.matchAll(/([^{}]+)\{/g)]
      .map(m => m[1].trim())
      .filter(sel => /\.config-advanced/.test(sel) && sel.includes(','));
    expect(pruefe('.foo, .config-advanced { a: 1 }')).toHaveLength(1);
    expect(pruefe('.config-advanced, .config-row { a: 1 }')).toHaveLength(1);
    expect(pruefe('.config-advanced[open] .config-advanced__summary { a: 1 }')).toHaveLength(0);
  });

  it('hält die geschweiften Klammern im Gleichgewicht', () => {
    const nachher = strippe(ohneKommentare(STYLE));
    const auf = (nachher.match(/\{/g) || []).length;
    const zu = (nachher.match(/\}/g) || []).length;
    expect(auf).toBe(zu);
  });
});
