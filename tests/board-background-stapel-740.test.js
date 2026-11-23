/* @vitest-environment node */
/* Karte #740: Mit Hintergrundbild gibt `#app.board--has-bg>*{position:relative;z-index:1}`
   Kopf und Board-Fläche je einen eigenen Stapelkontext. Bei gleichem z-index malt die im DOM
   spätere Fläche (#board-columns) über den Kopf — das Board-Menü (.board-menu, z-index 210)
   lag dadurch hinter Swimlanes und Karten. Der Kopf muss über der Fläche liegen.
   Quelltext-Test über src/style.css (Muster: board-background-css-727.test.js). */
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const css = readFileSync(join(process.cwd(), 'src', 'style.css'), 'utf8');

/* z-index der Regel zu genau diesem Selektor (kommentarfrei), sonst null. */
function zIndex(quelle, selektor) {
  const rein = quelle.replace(/\/\*[\s\S]*?\*\//g, '');
  const esc = selektor.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const m = rein.match(new RegExp('(?:^|[}\\s])' + esc + '\\s*\\{([^}]*)\\}'));
  if (!m) return null;
  const z = m[1].replace(/\s+/g, '').match(/(?:^|;)z-index:(-?\d+)/);
  return z ? Number(z[1]) : null;
}

function kopfLiegtOben(quelle) {
  const alle = zIndex(quelle, '#app.board--has-bg>*');
  const kopf = zIndex(quelle, '#app.board--has-bg>#board-header');
  return alle !== null && kopf !== null && kopf > alle;
}

describe('Board-Hintergrund: Kopf liegt über der Board-Fläche (#740)', () => {
  it('#board-header hat mit Hintergrundbild einen höheren z-index als die übrigen #app-Kinder', () => {
    expect(kopfLiegtOben(css)).toBe(true);
  });

  it('Selbsttest: ohne die Regel oder mit gleichem Wert wird es erkannt', () => {
    expect(kopfLiegtOben(css.replace('#app.board--has-bg>#board-header{z-index:2}', ''))).toBe(false);
    expect(kopfLiegtOben(css.replace('#app.board--has-bg>#board-header{z-index:2}', '#app.board--has-bg>#board-header{z-index:1}'))).toBe(false);
    expect(kopfLiegtOben('/* #app.board--has-bg>*{z-index:1} #app.board--has-bg>#board-header{z-index:2} */')).toBe(false);
  });
});
