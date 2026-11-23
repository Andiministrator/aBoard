/* @vitest-environment node */
/* Karte #727 / Kritiker K-10 (Sprint 85): Die CSS-WIRKUNG der Darstellungs-Klassen
   (board--bg-voll / board--bg-dunkel) war ungetestet — die Klassen am #app prüft
   board-background-darstellung-727.test.js, nicht ob das Stylesheet sie auch umsetzt.
   Quelltext-Test über src/style.css (analog admin-dialog-design-728.test.js). */
import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

const css = readFileSync(join(process.cwd(), 'src', 'style.css'), 'utf8');

/* Körper der Regel zu genau diesem Selektor (kommentarfrei), sonst null. */
function regelKoerper(quelle, selektor) {
  const rein = quelle.replace(/\/\*[\s\S]*?\*\//g, '');
  const esc = selektor.replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replace(/,/g, '\\s*,\\s*');
  const m = rein.match(new RegExp('(?:^|[}\\s])' + esc + '\\s*\\{([^}]*)\\}'));
  return m ? m[1].replace(/\s+/g, '') : null;
}

const VOLL = '#app.board--bg-voll::before,#app.board--bg-dunkel::before';
const DUNKEL = '#app.board--bg-dunkel::before';

function pruefeRegeln(quelle) {
  const voll = regelKoerper(quelle, VOLL);
  const dunkel = regelKoerper(quelle, DUNKEL);
  return {
    vollDeckend: !!voll && /(^|;)opacity:1(;|$)/.test(voll),
    dunkelVerlauf: !!dunkel && /background-image:linear-gradient\(rgba\(0,0,0,\.55\),rgba\(0,0,0,\.55\)\),var\(--board-bg-image\)/.test(dunkel),
  };
}

describe('Board-Hintergrund Darstellung: CSS-Wirkung (#727, K-10)', () => {
  it('voll + dunkel: Hintergrundbild deckend (opacity:1)', () => {
    expect(pruefeRegeln(css).vollDeckend).toBe(true);
  });
  it('dunkel: Schwarz-Verlauf (55 %) über dem Bild', () => {
    expect(pruefeRegeln(css).dunkelVerlauf).toBe(true);
  });
  it('Standard bleibt dezent: .board--has-bg::before hat opacity .15', () => {
    const b = regelKoerper(css, '#app.board--has-bg::before');
    expect(b).not.toBeNull();
    expect(b).toMatch(/opacity:\.15/);
  });

  it('Selbsttest: kippt man Wert oder Regel, wird es erkannt', () => {
    expect(pruefeRegeln(css.replace(VOLL + '{opacity:1}', VOLL + '{opacity:.5}')).vollDeckend).toBe(false);
    expect(pruefeRegeln(css.replace(VOLL + '{opacity:1}', '')).vollDeckend).toBe(false);
    expect(pruefeRegeln(css.replace('rgba(0,0,0,.55),rgba(0,0,0,.55)', 'rgba(0,0,0,.1),rgba(0,0,0,.1)')).dunkelVerlauf).toBe(false);
    expect(pruefeRegeln(css.replace(/#app\.board--bg-dunkel::before\{background-image[^}]*\}/, '')).dunkelVerlauf).toBe(false);
    /* Kommentar-Attrappe zählt nicht */
    expect(pruefeRegeln('/* ' + VOLL + '{opacity:1} */').vollDeckend).toBe(false);
  });
});
